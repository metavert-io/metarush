package conversation

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/visual"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// Call clean-up (D103): what online calls leave behind, found and offered as
// suggestions a person accepts or skips — never applied on its own. A frozen
// webcam (the recording's own freeze evidence), a sound dropout (digital
// silence inside someone's talk), dead air (nobody speaks), call talk ("can
// you hear me?", "you're on mute") and the app's chrome on a picture (on-screen
// text at an edge that stays: name labels, a recording banner).

// ActionCleanup names tracked clean-up work.
const ActionCleanup = "conversation-cleanup"

// Clean-up kinds and fixes.
const (
	KindFreeze   = "freeze"
	KindDropout  = "dropout"
	KindDeadAir  = "dead-air"
	KindCallTalk = "call-talk"
	KindChrome   = "chrome"

	FixCut    = "cut"
	FixSwitch = "switch"
	FixCrop   = "crop"
	FixFlag   = "flag"
)

// CleanupKinds lists the kinds in the order people see them.
var CleanupKinds = []string{KindCallTalk, KindDeadAir, KindFreeze, KindDropout, KindChrome}

// Thresholds.
const (
	deadAirDefault = 2.5  // nobody speaks this long (seconds)
	deadAirKeep    = 0.6  // ... and this much of it stays
	dropoutMin     = 0.08 // digital silence at least this long ...
	dropoutMax     = 2.0  // ... and at most this long, inside someone's talk
	dropoutAround  = 0.6  // their talk within this long on both sides
	freezeMin      = 1.5  // a frozen picture at least this long (shorter stills happen when someone sits still)
	chromeShare    = 0.5  // on-screen text present this share of the recording ...
	chromeEdge     = 0.15 // ... within this far of an edge is the app's chrome
	chromeMaxCrop  = 0.22 // never crop more than this off one side
)

func init() {
	voiceover.ActionTitles[ActionCleanup] = "Checking the call for glitches"
	ops.Def("conversation.cleanup", "Look a conversation over for what calls leave behind: frozen webcams, sound dropouts, long silences, call talk (\"can you hear me?\", \"you're on mute\") and the app's chrome on a picture; each is a suggestion to accept or skip", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallCleanupIn) (*CallCleanupOut, error) {
			return cleanup(ctx, a, in)
		})
	ops.Def("conversation.decide", "Accept or skip clean-up suggestions: accepting cuts the stretch out of the cut, shows someone else over a frozen picture or crops the app's chrome off (checks the plan; apply makes it a new version)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallDecideIn) (*CallDecideOut, error) {
			return decideWith(ctx, a, in, true)
		})
}

// CallCleanupIn looks a conversation over.
type CallCleanupIn struct {
	CallRefIn
	Kinds   []string `json:"kinds,omitempty" jsonschema:"description=Only these kinds: call-talk\\, dead-air\\, freeze\\, dropout\\, chrome (default all)."`
	DeadAir float64  `json:"deadAir,omitempty" jsonschema:"minimum=0,maximum=60,description=Silences at least this long count as dead air (seconds; default 2.5)."`
}

// CallCleanupOut is what clean-up found.
type CallCleanupOut struct {
	Conversation CallInfo      `json:"conversation"`
	Suggestions  []CleanupInfo `json:"suggestions"`
	// Notes say what couldn't be checked and why.
	Notes   []string `json:"notes,omitempty"`
	Summary string   `json:"summary"`
	JobID   string   `json:"jobId,omitempty"`
}

// SetJobID implements voiceover.Trackable.
func (o *CallCleanupOut) SetJobID(id string) { o.JobID = id }

// JobResult implements voiceover.Trackable.
func (o *CallCleanupOut) JobResult() map[string]any {
	return map[string]any{"conversationId": o.Conversation.ID, "suggestions": len(o.Suggestions), "summary": o.Summary}
}

// CleanupInfo is a suggestion in plain words.
type CleanupInfo struct {
	models.CleanupSuggestion
	When string `json:"when" jsonschema:"description=Where on the conversation's clock (0:31–0:33)."`
	Who  string `json:"who,omitempty" jsonschema:"description=The member's name."`
	// Does says what accepting it does.
	Does string `json:"does"`
}

func cleanup(ctx context.Context, a *app.App, in *CallCleanupIn) (*CallCleanupOut, error) {
	c, err := load(ctx, a, in.Conversation, in.ProjectID)
	if err != nil {
		return nil, err
	}
	kinds := map[string]bool{}
	for _, k := range in.Kinds {
		if !containsStr(CleanupKinds, k) {
			return nil, invalid("clean-up kinds are %s, not %q", strings.Join(CleanupKinds, ", "), k)
		}
		kinds[k] = true
	}
	if len(kinds) == 0 {
		for _, k := range CleanupKinds {
			kinds[k] = true
		}
	}
	dead := in.DeadAir
	if dead <= 0 {
		dead = deadAirDefault
	}
	run := func(ctx context.Context) (*CallCleanupOut, error) {
		found, notes, err := detect(ctx, a, c, kinds, dead)
		if err != nil {
			return nil, err
		}
		c.Cleanup = mergeCleanup(c.Cleanup, found, kinds)
		now := time.Now().UTC()
		c.CleanupAt = &now
		if err := save(ctx, a, c); err != nil {
			return nil, err
		}
		a.Audit(ctx, "conversation.cleanup", "conversation", c.ID, first(c.ProjectIDs), map[string]any{"found": len(found)})
		out := &CallCleanupOut{Conversation: view(ctx, a, c), Suggestions: []CleanupInfo{}, Notes: notes}
		open := 0
		for _, s := range c.Cleanup {
			out.Suggestions = append(out.Suggestions, cleanupInfo(c, s))
			if s.State == "open" {
				open++
			}
		}
		switch open {
		case 0:
			out.Summary = "Nothing to clean up in " + c.Name + "."
		case 1:
			out.Summary = "1 thing to clean up in " + c.Name + ": accept or skip it."
		default:
			out.Summary = fmt.Sprintf("%d things to clean up in %s: accept or skip each.", open, c.Name)
		}
		return out, nil
	}
	return voiceover.TrackWork(ctx, a, ActionCleanup, "", first(c.ProjectIDs), run)
}

// cleanupInfo says a suggestion in plain words.
func cleanupInfo(c *models.Conversation, s models.CleanupSuggestion) CleanupInfo {
	ci := CleanupInfo{CleanupSuggestion: s, When: editspec.Clock(s.From) + "–" + editspec.Clock(s.To)}
	if m, _ := c.Member(s.Member); m != nil {
		ci.Who = m.Name
	}
	switch s.Fix {
	case FixCut:
		if s.Keep > 0 {
			ci.Does = fmt.Sprintf("Shortens it to %s.", secs(s.Keep))
		} else {
			ci.Does = "Cuts it out."
		}
	case FixSwitch:
		ci.Does = "Shows someone else while it lasts."
	case FixCrop:
		ci.Does = "Crops it off " + possessive(ci.Who) + " picture."
	default:
		ci.Does = "Nothing to do automatically; it's worth a look."
	}
	return ci
}

// mergeCleanup keeps what people decided about suggestions found again (same
// kind, member and moment) and the accepted ones (history); the rest is new.
func mergeCleanup(old, found []models.CleanupSuggestion, kinds map[string]bool) []models.CleanupSuggestion {
	key := func(s models.CleanupSuggestion) string {
		return fmt.Sprintf("%s|%s|%.1f", s.Kind, s.Member, s.From)
	}
	prev := map[string]models.CleanupSuggestion{}
	for _, s := range old {
		prev[key(s)] = s
	}
	var out []models.CleanupSuggestion
	seen := map[string]bool{}
	for _, s := range found {
		if p, ok := prev[key(s)]; ok {
			s.State, s.By, s.At, s.Revision = p.State, p.By, p.At, p.Revision
		}
		if s.State == "" {
			s.State = "open"
		}
		seen[key(s)] = true
		out = append(out, s)
	}
	for _, s := range old {
		if !seen[key(s)] && (s.State == "accepted" || !kinds[s.Kind]) {
			out = append(out, s) // history, or a kind not looked at this time
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].Kind < out[j].Kind
	})
	for i := range out {
		out[i].ID = fmt.Sprintf("c%d", i+1)
	}
	return out
}

// callView is a conversation resolved for clean-up and moments: everyone's
// words and activity on its clock (transcripts may be missing).
func callView(ctx context.Context, a *app.App, c *models.Conversation) (*editspec.ConversationView, editspec.Facts, error) {
	pin := c.Pin()
	spec := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Output: editspec.Output{Preset: editspec.PresetLandscape}, Conversations: map[string]editspec.ConversationPin{convName: pin},
		Clips: []editspec.Clip{{ID: "talk", AssetID: editspec.ConvKey(convName), In: 0, Out: math.Max(1, c.Length)}}}
	editspec.Normalize(spec)
	facts, err := ops.LoadFacts(ctx, a, first(c.ProjectIDs), spec)
	if err != nil {
		return nil, nil, err
	}
	v := editspec.Conversation(spec, facts, convName)
	if v == nil {
		return nil, nil, invalid("the conversation's recordings couldn't be read")
	}
	return v, facts, nil
}

// detect runs the detectors asked for.
func detect(ctx context.Context, a *app.App, c *models.Conversation, kinds map[string]bool, dead float64) ([]models.CleanupSuggestion, []string, error) {
	as, err := assets(ctx, a, c)
	if err != nil {
		return nil, nil, err
	}
	v, facts, err := callView(ctx, a, c)
	if err != nil {
		return nil, nil, err
	}
	var out []models.CleanupSuggestion
	var notes []string
	pictures := 0
	var noWords []string
	for _, m := range c.Members {
		if m.HasPicture() && as[m.AssetID] != nil && as[m.AssetID].Media.HasVideo {
			pictures++
		}
		if m.HasSound() {
			if af, ok := facts.Asset(m.AssetID); ok && af.HasAudio && af.TranscriptPrecision == "" {
				noWords = append(noWords, m.Name)
			}
		}
	}
	if len(noWords) > 0 && (kinds[KindCallTalk] || kinds[KindDeadAir] || kinds[KindDropout]) {
		notes = append(notes, possessive(joinAnd(noWords))+" recording has no transcript yet, so their talk wasn't checked.")
	}
	total := 2*len(c.Members) + 2
	done := 0
	step := func(msg string) {
		done++
		_ = voiceover.Step(ctx, done, total, msg)
	}
	if kinds[KindCallTalk] {
		step("Reading the conversation for call talk")
		out = append(out, callTalk(v.Words)...)
	}
	if kinds[KindDeadAir] {
		out = append(out, deadAir(c, v, dead)...)
	}
	if kinds[KindFreeze] {
		var idList []string
		for _, m := range c.Members {
			idList = append(idList, m.AssetID)
		}
		stored := evidence.LoadStored(ctx, a.DB, a.WorkspaceID(), idList)
		for _, m := range c.Members {
			x := as[m.AssetID]
			if !m.HasPicture() || x == nil || !x.Media.HasVideo {
				continue
			}
			// The original shows a stalled webcam exactly (the call app repeats
			// its last frame); a preview copy's evidence may smooth it over.
			step("Watching " + possessive(m.Name) + " picture for freezes")
			if path, err := local(ctx, a, x); err == nil {
				if spans, err := pictureFreezes(ctx, a.Tools, path, x.Media.Duration); err == nil {
					out = append(out, freezes(m.ConversationMember, spans, pictures > 1)...)
					continue
				}
			}
			ev := stored[m.AssetID]
			if ev == nil {
				notes = append(notes, possessive(m.Name)+" picture hasn't been analyzed yet, so freezes weren't checked.")
				continue
			}
			out = append(out, freezes(m.ConversationMember, ev.Freezes, pictures > 1)...)
		}
	}
	if kinds[KindDropout] {
		for _, m := range c.Members {
			x := as[m.AssetID]
			if !m.HasSound() || x == nil || !x.Media.HasAudio {
				continue
			}
			step("Listening for dropouts in " + possessive(m.Name) + " recording")
			path, err := local(ctx, a, x)
			if err != nil {
				notes = append(notes, possessive(m.Name)+" recording isn't on this computer, so dropouts weren't checked.")
				continue
			}
			snd, err := decodeSound(ctx, a.Tools, path, 0, 0)
			if err != nil {
				notes = append(notes, possessive(m.Name)+" sound couldn't be read, so dropouts weren't checked.")
				continue
			}
			out = append(out, dropouts(m.ConversationMember, snd, v)...)
		}
	}
	if kinds[KindChrome] {
		var idList []string
		for _, m := range c.Members {
			idList = append(idList, m.AssetID)
		}
		idx := visual.LoadMany(ctx, a.DB, a.WorkspaceID(), idList)
		var unseen []string
		for _, m := range c.Members {
			x := as[m.AssetID]
			if !m.HasPicture() || x == nil || !x.Media.HasVideo {
				continue
			}
			st := idx[m.AssetID]
			if st == nil || st.Index == nil {
				unseen = append(unseen, m.Name)
				continue
			}
			if s := chrome(m.ConversationMember, st.Index, c.Length); s != nil {
				out = append(out, *s)
			}
		}
		if len(unseen) > 0 {
			notes = append(notes, possessive(joinAnd(unseen))+" picture hasn't been looked at yet (on a Mac), so the app's chrome wasn't checked.")
		}
	}
	step("Done")
	return out, notes, nil
}

// ---- call talk ----

// callPhrases are things people say to the call, not to the audience.
var callPhrases = regexp.MustCompile(`\b(can (you|everyone|everybody) (hear|see) (me|us)|do you hear me|can you hear me|you'?re (on )?mut(e|ed)|you are (on )?mut(e|ed)|i was (on )?mut(e|ed)|still (on )?mut(e|ed)|(un)?mute (yourself|myself|me)|let me unmute|can you see my screen|share my screen|sharing my screen|you'?re breaking up|you are breaking up|you froze|you'?re frozen|my (internet|connection|wifi|wi-fi)|bad connection|i lost you|lost you there|you cut out|you dropped|is it recording|are we recording|we'?re recording|hit record|start(ed)? recording|stop recording|testing,? testing|mic check|sound check|is my (audio|sound|mic|microphone) (ok|okay|alright|good|working))\b`)

// replyStart is a short reply to call talk ("Yes, I can hear you").
var replyStart = regexp.MustCompile(`^(yes|yeah|yep|yup|no|nope|i can|we can|loud and clear|perfect|got it|all good|sounds good|i hear you|i can hear you|i see it|ok|okay|great)\b`)

type sentence struct {
	speaker  string
	from, to float64
	text     string
	words    int
}

// sentences splits segments at sentence ends (words ending with . ? !).
func sentences(segs []editspec.TranscriptSegment) []sentence {
	var out []sentence
	for _, sg := range segs {
		if len(sg.Words) == 0 {
			if strings.TrimSpace(sg.Text) != "" {
				out = append(out, sentence{speaker: sg.Speaker, from: sg.From, to: sg.To, text: strings.TrimSpace(sg.Text), words: len(strings.Fields(sg.Text))})
			}
			continue
		}
		cur := sentence{speaker: sg.Speaker, from: sg.Words[0].From}
		var parts []string
		for i, w := range sg.Words {
			if len(parts) == 0 {
				cur.from = w.From
			}
			parts = append(parts, w.Text)
			cur.to = w.To
			t := strings.TrimSpace(w.Text)
			if strings.HasSuffix(t, ".") || strings.HasSuffix(t, "?") || strings.HasSuffix(t, "!") || i == len(sg.Words)-1 {
				cur.text, cur.words = strings.Join(parts, " "), len(parts)
				out = append(out, cur)
				cur = sentence{speaker: sg.Speaker}
				parts = nil
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].from < out[j].from })
	return out
}

// plainText is a sentence for matching: lower case, curly quotes straight,
// punctuation but apostrophes gone.
func plainText(s string) string {
	s = strings.ToLower(strings.NewReplacer("’", "'", "‘", "'").Replace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\'' || r == '-' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// callTalk finds sentences said to the call (and the short replies to them),
// joined into stretches to cut.
func callTalk(segs []editspec.TranscriptSegment) []models.CleanupSuggestion {
	ss := sentences(segs)
	mark := make([]bool, len(ss))
	for i, s := range ss {
		if callPhrases.MatchString(plainText(s.text)) {
			mark[i] = true
		}
	}
	// A short answer from someone else right after a question about the call
	// ("Can you hear me?" "Yes, I can.") is part of it.
	for i := range ss {
		if !mark[i] || !strings.HasSuffix(strings.TrimSpace(ss[i].text), "?") {
			continue
		}
		for j := i + 1; j < len(ss) && ss[j].from-ss[i].to < 3; j++ {
			if ss[j].speaker != ss[i].speaker && ss[j].words <= 8 && replyStart.MatchString(plainText(ss[j].text)) {
				mark[j] = true
				break
			}
		}
	}
	var out []models.CleanupSuggestion
	for i := 0; i < len(ss); {
		if !mark[i] {
			i++
			continue
		}
		j := i
		quote := []string{ss[i].text}
		for j+1 < len(ss) && mark[j+1] && ss[j+1].from-ss[j].to < 1.5 {
			j++
			quote = append(quote, ss[j].text)
		}
		from, to := math.Max(0, ss[i].from-0.05), ss[j].to+0.1
		out = append(out, models.CleanupSuggestion{Kind: KindCallTalk, From: round3(from), To: round3(to), Fix: FixCut, Quote: strings.Join(quote, " "),
			Text: "Talk about the call itself, not for the audience."})
		i = j + 1
	}
	return out
}

// ---- dead air ----

// deadAir finds stretches where nobody makes a sound: the start, the gaps and
// the end, each shortened rather than removed.
func deadAir(c *models.Conversation, v *editspec.ConversationView, min float64) []models.CleanupSuggestion {
	var all []editspec.Span
	for _, sp := range v.Activity {
		all = append(all, sp...)
	}
	all = editspec.MergeSpans(all, 0)
	if len(all) == 0 {
		return nil
	}
	var out []models.CleanupSuggestion
	add := func(from, to, keep float64, text string) {
		if to-from >= min {
			out = append(out, models.CleanupSuggestion{Kind: KindDeadAir, From: round3(from), To: round3(to), Fix: FixCut, Keep: keep, Text: text})
		}
	}
	add(0, all[0].From, 0.3, fmt.Sprintf("Nobody speaks for the first %s.", secs(all[0].From)))
	for i := 1; i < len(all); i++ {
		g := all[i].From - all[i-1].To
		add(all[i-1].To, all[i].From, deadAirKeep, fmt.Sprintf("Nobody speaks for %s.", secs(g)))
	}
	if end := c.Length; end > 0 {
		add(all[len(all)-1].To, end, 0.3, fmt.Sprintf("Nobody speaks for the last %s.", secs(end-all[len(all)-1].To)))
	}
	return out
}

// ---- frozen pictures ----

// pictureFreezes finds where a recording's picture stops changing (half a
// second or more), on the original.
func pictureFreezes(ctx context.Context, ts *tools.Set, path string, dur float64) ([]evidence.Span, error) {
	res, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Args: []string{"-hide_banner", "-nostdin", "-nostats", "-i", path, "-map", "0:v:0",
		"-vf", fmt.Sprintf("freezedetect=n=-60dB:d=%.2f", freezeMin), "-f", "null", "-"}})
	if err != nil {
		return nil, err
	}
	return evidence.Parse(string(res.Stderr), dur).Freezes, nil
}

func freezes(m editspec.ConversationMember, spans []evidence.Span, others bool) []models.CleanupSuggestion {
	var out []models.CleanupSuggestion
	for _, z := range spans {
		from, to := m.ClockAt(z.From), m.ClockAt(z.To)
		if to-from < freezeMin {
			continue
		}
		fix := FixFlag
		if others {
			fix = FixSwitch
		}
		out = append(out, models.CleanupSuggestion{Kind: KindFreeze, Member: m.ID, From: round3(from), To: round3(to), Fix: fix,
			Text: fmt.Sprintf("%s picture freezes for %s.", possessive(m.Name), secs(to-from))})
	}
	return out
}

// ---- dropouts ----

// dropouts finds digital silence inside a member's talk: their sound stops
// dead (far below their own background) and they are talking on both sides.
// It is safe to cut out when nobody's words fall in it.
func dropouts(m editspec.ConversationMember, snd *sound, v *editspec.ConversationView) []models.CleanupSuggestion {
	noise, _ := snd.levels()
	thr := float32(math.Min(noise-30, -80))
	act := v.Activity[m.ID]
	var out []models.CleanupSuggestion
	n := len(snd.db)
	for k := 0; k < n; {
		if snd.db[k] >= thr {
			k++
			continue
		}
		j := k
		for j+1 < n && snd.db[j+1] < thr {
			j++
		}
		// Each value is a 30 ms window: the silence reaches one step further each way.
		tau0, tau1 := float64(k-1)/envRate, float64(j+2)/envRate
		k = j + 1
		if tau1-tau0 < dropoutMin || tau1-tau0 > dropoutMax {
			continue
		}
		from, to := m.ClockAt(tau0), m.ClockAt(tau1)
		if !spanNear(act, from-dropoutAround, from) || !spanNear(act, to, to+dropoutAround) {
			continue
		}
		fix, text := FixCut, fmt.Sprintf("%s sound drops out for %s.", possessive(m.Name), secs(to-from))
		if wordsIn(v.Words, from, to) {
			fix, text = FixFlag, fmt.Sprintf("%s sound drops out for %s, in the middle of a word.", possessive(m.Name), secs(to-from))
		}
		out = append(out, models.CleanupSuggestion{Kind: KindDropout, Member: m.ID, From: round3(from), To: round3(to), Fix: fix, Text: text})
	}
	return out
}

// spanNear reports whether any span overlaps [a, b).
func spanNear(spans []editspec.Span, a, b float64) bool {
	for _, s := range spans {
		if s.From < b && s.To > a {
			return true
		}
	}
	return false
}

// wordsIn reports whether anyone's word overlaps [from, to) by more than 30 ms.
func wordsIn(segs []editspec.TranscriptSegment, from, to float64) bool {
	for _, sg := range segs {
		if sg.To < from || sg.From > to {
			continue
		}
		for _, w := range sg.Words {
			if math.Min(w.To, to)-math.Max(w.From, from) > 0.03 {
				return true
			}
		}
	}
	return false
}

// ---- the app's chrome ----

// chrome finds on-screen text that stays at an edge of a member's picture (a
// name label, a recording banner) and the crop that takes it off.
func chrome(m editspec.ConversationMember, idx *visual.Index, length float64) *models.CleanupSuggestion {
	dur := idx.Duration
	if dur <= 0 {
		return nil
	}
	type side struct {
		cut  float64
		text []string
	}
	sides := map[string]*side{}
	for _, t := range idx.Text {
		if t.Changing || (t.To-t.From) < chromeShare*dur {
			continue
		}
		x, y, w, h := t.Box[0], t.Box[1], t.Box[2], t.Box[3]
		// The side it is nearest, and how much to crop to take it off.
		cands := map[string]float64{}
		if y+h > 1-chromeEdge {
			cands["bottom"] = 1 - y + 0.01
		}
		if y < chromeEdge {
			cands["top"] = y + h + 0.01
		}
		if x+w > 1-chromeEdge {
			cands["right"] = 1 - x + 0.01
		}
		if x < chromeEdge {
			cands["left"] = x + w + 0.01
		}
		best, bc := "", math.Inf(1)
		for _, k := range []string{"bottom", "top", "left", "right"} {
			if v, ok := cands[k]; ok && v < bc {
				best, bc = k, v
			}
		}
		if best == "" || bc > chromeMaxCrop {
			continue
		}
		s := sides[best]
		if s == nil {
			s = &side{}
			sides[best] = s
		}
		s.cut = math.Max(s.cut, bc)
		s.text = append(s.text, strings.TrimSpace(t.Text))
	}
	if len(sides) == 0 {
		return nil
	}
	cr := &editspec.Crop{}
	if s := sides["left"]; s != nil {
		cr.Left = round3(s.cut)
	}
	if s := sides["top"]; s != nil {
		cr.Top = round3(s.cut)
	}
	if s := sides["right"]; s != nil {
		cr.Right = round3(s.cut)
	}
	if s := sides["bottom"]; s != nil {
		cr.Bottom = round3(s.cut)
	}
	var where, texts []string
	for _, k := range []string{"top", "bottom", "left", "right"} {
		if s := sides[k]; s != nil {
			where = append(where, fmt.Sprintf("%s %.0f%%", k, s.cut*100))
			texts = append(texts, s.text...)
		}
	}
	return &models.CleanupSuggestion{Kind: KindChrome, Member: m.ID, From: 0, To: round3(length), Fix: FixCrop, Crop: cr, Quote: strings.Join(texts, " · "),
		Text: fmt.Sprintf("%s picture shows the app's labels (%s): crop the %s off.", possessive(m.Name), strings.Join(texts, ", "), joinAnd(where))}
}

// ---- deciding ----

// CallDecideIn accepts or skips clean-up suggestions.
type CallDecideIn struct {
	CallRefIn
	// IDs are the suggestions (c1, c2…); "open" means every open one.
	IDs      []string `json:"ids" jsonschema:"minItems=1,maxItems=500"`
	Decision string   `json:"decision" jsonschema:"enum=accept,enum=skip,enum=reopen"`
	Project  string   `json:"project,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$,description=The project whose cut changes (default: the conversation's)."`
	Branch   string   `json:"branch,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]*$"`
	Apply    bool     `json:"apply,omitempty" jsonschema:"description=Put accepted changes in the cut as a new version (skips and reopens are saved at once)."`
}

// CallDecideOut is the decision and, for accepted ones, the change to the cut.
type CallDecideOut struct {
	CallPlanOut
	Suggestions []CleanupInfo `json:"suggestions"`
}

// PreviewDecide is a decision about clean-up suggestions as a card shows it
// before a person confirms it: for accepted ones, the checked change to the
// cut. Nothing is saved: no suggestion is marked, no crop is kept.
func PreviewDecide(ctx context.Context, a *app.App, in CallDecideIn) (*CallDecideOut, error) {
	in.Apply = false
	return decideWith(ctx, a, &in, false)
}

// decideWith decides; commit saves the decision (a preview leaves the
// conversation as it is).
func decideWith(ctx context.Context, a *app.App, in *CallDecideIn, commit bool) (*CallDecideOut, error) {
	c, err := load(ctx, a, in.Conversation, in.ProjectID)
	if err != nil {
		return nil, err
	}
	var picked []int
	for _, id := range in.IDs {
		found := false
		for i, s := range c.Cleanup {
			if s.ID == id || (id == "open" && s.State == "open") {
				if !containsInt(picked, i) {
					picked = append(picked, i)
				}
				found = true
			}
		}
		if !found && id != "open" {
			return nil, invalid("there's no clean-up suggestion %q in %s", id, c.Name)
		}
	}
	out := &CallDecideOut{}
	if len(picked) == 0 {
		out.Summary = "Nothing open to decide."
		out.Changes = []string{}
		return out, nil
	}
	now := time.Now().UTC()
	actor := a.Actor
	mark := func(state, rev string) {
		for _, i := range picked {
			c.Cleanup[i].State, c.Cleanup[i].At, c.Cleanup[i].By = state, &now, &actor
			if rev != "" {
				c.Cleanup[i].Revision = rev
			}
		}
	}
	finish := func() (*CallDecideOut, error) {
		if commit {
			if err := save(ctx, a, c); err != nil {
				return nil, err
			}
			a.Audit(ctx, "conversation.decide", "conversation", c.ID, first(c.ProjectIDs), map[string]any{"decision": in.Decision, "suggestions": len(picked)})
		}
		for _, s := range c.Cleanup {
			out.Suggestions = append(out.Suggestions, cleanupInfo(c, s))
		}
		if out.Changes == nil {
			out.Changes = []string{}
		}
		return out, nil
	}
	switch in.Decision {
	case "skip":
		mark("skipped", "")
		out.Summary = fmt.Sprintf("Skipped %s.", count(len(picked), "suggestion"))
		return finish()
	case "reopen":
		mark("open", "")
		out.Summary = fmt.Sprintf("Reopened %s.", count(len(picked), "suggestion"))
		return finish()
	case "accept":
	default:
		return nil, invalid("decide accept, skip or reopen, not %q", in.Decision)
	}
	pid := in.Project
	if pid == "" {
		pid = first(c.ProjectIDs)
	}
	rev, err := latest(ctx, a, pid, in.Branch)
	if err != nil {
		return nil, err
	}
	var sugg []models.CleanupSuggestion
	for _, i := range picked {
		sugg = append(sugg, c.Cleanup[i])
	}
	var eops []editspec.Op
	if rev != nil {
		eops, out.Changes = acceptOps(&rev.Spec, c, sugg)
	}
	// Crops also go on the conversation, so new cuts start with them.
	for _, s := range sugg {
		if s.Fix == FixCrop && s.Crop != nil {
			if m, _ := c.Member(s.Member); m != nil {
				cr := *s.Crop
				m.Crop = &cr
			}
		}
	}
	if len(eops) == 0 {
		mark("accepted", "")
		out.Summary = fmt.Sprintf("Accepted %s; the cut doesn't play %s, so nothing in it changes.", count(len(picked), "suggestion"), map[bool]string{true: "them", false: "it"}[len(picked) > 1])
		if rev != nil && len(out.Changes) == 0 {
			out.Summary = fmt.Sprintf("Accepted %s; there was nothing to change in the cut.", count(len(picked), "suggestion"))
		}
		return finish()
	}
	if out.Result, err = planCheck(ctx, a, rev, "Clean up "+c.Name+": "+strings.Join(out.Changes, "; "), eops); err != nil {
		return nil, err
	}
	out.Summary = "Ready: " + strings.Join(out.Changes, "; ") + "."
	if in.Apply {
		out.Applied = applyResult(ctx, a, out.Result)
		out.Summary = settle(out.Summary, out.Applied)
		if out.Applied != nil && out.Applied.Problem == "" {
			mark("accepted", out.Applied.RevisionID)
		}
		return finish()
	}
	out.Suggestions = []CleanupInfo{}
	for _, s := range c.Cleanup {
		out.Suggestions = append(out.Suggestions, cleanupInfo(c, s))
	}
	return out, nil
}

func containsInt(xs []int, x int) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func count(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// acceptOps turns accepted suggestions into edits of a cut: new shots over
// frozen pictures and crops first, then the stretches to cut out, latest
// first, so each one's place on the timeline still holds.
func acceptOps(spec *editspec.Spec, c *models.Conversation, sugg []models.CleanupSuggestion) ([]editspec.Op, []string) {
	s := editspec.Clone(spec)
	editspec.Normalize(s)
	var names []string
	for _, n := range sortedNames(s.Conversations) {
		if s.Conversations[n].ID == c.ID {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	clips, _ := editspec.Timeline(s)
	fps := float64(s.Output.FPS)
	var eops []editspec.Op
	var changes []string
	// Shots over frozen pictures, per clip.
	shots := map[int][]editspec.Shot{}
	for _, sg := range sugg {
		if sg.Fix != FixSwitch {
			continue
		}
		for i, cl := range s.Clips {
			name := editspec.ConvName(cl.AssetID)
			if !containsStr(names, name) || sg.To <= cl.In || sg.From >= cl.Out {
				continue
			}
			pin := s.Conversations[name]
			other := ""
			for _, m := range pin.Members {
				if m.ID != sg.Member && m.HasPicture() {
					other = m.ID
					break
				}
			}
			if other == "" {
				continue
			}
			cur, ok := shots[i]
			if !ok {
				cur = editspec.ClipShots(s, cl)
			}
			who := sg.Member
			if m, ok := pin.Member(sg.Member); ok {
				who = m.Name
			}
			shots[i] = editspec.CoverShots(cur, math.Max(sg.From, cl.In), math.Min(sg.To, cl.Out), other, possessive(who)+" picture is frozen here.", "Back after the frozen moment.")
			changes = append(changes, fmt.Sprintf("show someone else while %s picture is frozen at %s", possessive(who), editspec.Clock(sg.From)))
		}
	}
	idxs := make([]int, 0, len(shots))
	for i := range shots {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)
	for _, i := range idxs {
		eops = append(eops, editspec.Op{Op: "updateClip", ID: s.Clips[i].ID, Set: mustJSON(map[string]any{"shots": shots[i]})})
	}
	// Crops on the cut's pins.
	for _, name := range names {
		pin := s.Conversations[name]
		changed := false
		for _, sg := range sugg {
			if sg.Fix != FixCrop || sg.Crop == nil {
				continue
			}
			for k := range pin.Members {
				if pin.Members[k].ID == sg.Member || pin.Members[k].AssetID == memberAsset(c, sg.Member) {
					cr := *sg.Crop
					pin.Members[k].Crop = &cr
					changed = true
					changes = append(changes, "crop the app's labels off "+possessive(pin.Members[k].Name)+" picture")
				}
			}
		}
		if changed {
			p := pin
			eops = append(eops, editspec.Op{Op: "setConversation", ID: name, Conversation: &p})
		}
	}
	// Stretches to cut out, on the timeline.
	type cut struct{ from, to float64 }
	var cuts []cut
	for _, sg := range sugg {
		if sg.Fix != FixCut {
			continue
		}
		a, b := sg.From, sg.To
		if sg.Keep > 0 {
			a, b = a+sg.Keep/2, b-sg.Keep/2
		}
		if b-a < 0.05 {
			continue
		}
		hit := false
		for i, cl := range s.Clips {
			if !containsStr(names, editspec.ConvName(cl.AssetID)) || i >= len(clips) {
				continue
			}
			x, y := math.Max(a, cl.In), math.Min(b, cl.Out)
			if y-x < 0.05 {
				continue
			}
			pc := clips[i]
			rate := pc.PlaybackRate
			if rate <= 0 {
				rate = 1
			}
			t0 := float64(pc.From)/fps + (x-pc.SourceIn)/rate
			t1 := float64(pc.From)/fps + (y-pc.SourceIn)/rate
			cuts = append(cuts, cut{round3(t0), round3(t1)})
			hit = true
		}
		if hit {
			switch sg.Kind {
			case KindDeadAir:
				changes = append(changes, fmt.Sprintf("shorten the silence at %s", editspec.Clock(sg.From)))
			case KindDropout:
				changes = append(changes, fmt.Sprintf("cut out the dropout at %s", editspec.Clock(sg.From)))
			default:
				changes = append(changes, fmt.Sprintf("cut out %s at %s", quoteShort(sg.Quote), editspec.Clock(sg.From)))
			}
		}
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].from > cuts[j].from })
	for _, ct := range cuts {
		f, t := ct.from, ct.to
		eops = append(eops, editspec.Op{Op: "deleteRange", From: &f, To: &t})
	}
	return eops, changes
}

func memberAsset(c *models.Conversation, id string) string {
	if m, _ := c.Member(id); m != nil {
		return m.AssetID
	}
	return ""
}

// quoteShort quotes the first words of what was said.
func quoteShort(q string) string {
	f := strings.Fields(q)
	if len(f) == 0 {
		return "the call talk"
	}
	if len(f) > 6 {
		return "“" + strings.Join(f[:6], " ") + "…”"
	}
	return "“" + strings.Join(f, " ") + "”"
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("conversation.cleanup", ops.Offering{ID: "call-clean-up", Group: "edit", Title: "Call clean-up",
		Does:  "Finds what calls leave behind — call talk, dead air, frozen webcams, sound dropouts — for you to accept or skip.",
		Words: []string{"can you hear me", "frozen video"}, Needs: ops.Needs{Works: []string{"editing"}}})
}
