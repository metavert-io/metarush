package recipes

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// First picks (D215): before Claude looks, a chosen section's moments are
// MetaRush's first picks from the footage's moments (footage.moments): whole
// stretches of speech that start and end cleanly, scored the way the
// conversation finder scores shorts — a question and its answer, people
// laughing, a strong first line, two voices, about the length a moment should
// be — else what moves on screen. A pack's shorts get theirs from the
// conversation's best moments (conversation.moments). They are suggestions:
// the run waits for Claude to keep them or change them (recipe.picks, or a cut
// sheet), and footage without either kind of moment gets stand-ins spread
// over it, as before.

// footMoment is a candidate moment in one clip.
type footMoment struct {
	AssetID  string
	From, To float64 // in the footage
	First    string  // its first sentence
	Text     string  // what is said in it
	Why      []string
	Score    float64
	Ref      string // the transcript segment it starts with
}

var (
	pickClaim = regexp.MustCompile(`(?i)\b(trick|secret|mistake|surprised|surprising|never|always|most people|everyone|nobody|best|worst|first time|the truth|the problem|the key|actually|years|\d+)\b`)
	pickLaugh = regexp.MustCompile(`(?i)\b(ha|haha|hahaha|lol|laughs?|laughing)\b`)
)

// momentsOf loads the moments of the clips a run gives the chosen sections
// whose footage changed (footage.moments, per clip).
func momentsOf(ctx context.Context, a *app.App, d *models.RecipeDoc, values map[string]*models.RecipeRunInput, changed map[string]bool) map[string][]ops.Moment {
	out := map[string][]ops.Moment{}
	for _, sec := range d.Sections {
		if sec.Kind != models.RecipeChosen || !changed[sec.From] || values[sec.From] == nil {
			continue
		}
		for _, it := range values[sec.From].Items {
			if _, done := out[it.ID]; done || it.ID == "" {
				continue
			}
			mo, err := ops.CallTyped[ops.MomentsOut](ctx, a, "footage.moments", ops.MomentsIn{AssetID: it.ID}, ops.Caller{})
			if err != nil {
				out[it.ID] = nil
				continue
			}
			out[it.ID] = sentencesOf(ctx, a, it.ID, mo.Moments)
		}
	}
	return out
}

// longSpeech is how long a stretch of speech in the moments may be before
// first picks split it at its sentences.
const longSpeech = 6.0

// sentencesOf splits a clip's long stretches of speech at the ends of their
// sentences (from the transcript's words, footage.transcript), so a moment can
// start and end with a sentence even when the recognizer wrote long segments.
func sentencesOf(ctx context.Context, a *app.App, assetID string, ms []ops.Moment) []ops.Moment {
	long := false
	for _, m := range ms {
		long = long || (m.Kind == "speech" && m.To-m.From > longSpeech)
	}
	if !long {
		return ms
	}
	tr, err := ops.CallTyped[ops.TranscriptOut](ctx, a, "footage.transcript", ops.TranscriptIn{AssetID: assetID, Words: true}, ops.Caller{})
	if err != nil {
		return ms
	}
	segs := map[string]models.TranscriptSegment{}
	for _, s := range tr.Segments {
		segs[s.ID] = s
	}
	return splitSentences(ms, segs)
}

// splitSentences replaces each long stretch of speech that has words by its sentences.
func splitSentences(ms []ops.Moment, segs map[string]models.TranscriptSegment) []ops.Moment {
	var out []ops.Moment
	for _, m := range ms {
		seg, ok := segs[m.Ref]
		if m.Kind != "speech" || m.To-m.From <= longSpeech || !ok || len(seg.Words) == 0 {
			out = append(out, m)
			continue
		}
		start := 0
		for i, w := range seg.Words {
			if i < len(seg.Words)-1 && !endsSentence(w.Text) {
				continue
			}
			ws := seg.Words[start : i+1]
			var texts []string
			for _, x := range ws {
				texts = append(texts, strings.TrimSpace(x.Text))
			}
			out = append(out, ops.Moment{AssetID: m.AssetID, Asset: m.Asset, From: ws[0].From, To: ws[len(ws)-1].To, Kind: "speech", Text: strings.Join(texts, " "),
				Ref: m.Ref, Detail: m.Detail})
			start = i + 1
		}
	}
	return out
}

// sectionTarget is how many moments a chosen section's first picks are and
// how long each should be (seconds): the number it was saved with (within its
// count), sharing its length.
func sectionTarget(sec models.RecipeSection) (n int, each float64) {
	n = len(sec.Clips)
	if c := sec.Count; c != nil {
		if c.Min > 0 && float64(n) < c.Min {
			n = int(math.Ceil(c.Min))
		}
		if c.Max > 0 && float64(n) > c.Max {
			n = int(math.Floor(c.Max))
		}
	}
	if n < 1 {
		n = 1
	}
	total := sectionSeconds(sec)
	if total <= 0 {
		total = 4 * float64(n)
	}
	return n, total / float64(n)
}

// firstPicks are a chosen section's first picks from its footage's moments
// ("" none found: the section gets stand-ins).
func firstPicks(sec models.RecipeSection, v *models.RecipeRunInput, moments map[string][]ops.Moment) []footMoment {
	if v == nil || len(v.Items) == 0 || moments == nil {
		return nil
	}
	n, each := sectionTarget(sec)
	lo, hi := math.Max(1.5, 0.5*each), math.Max(math.Max(1.5, 0.5*each)+1, 1.6*each)
	var all []footMoment
	order := map[string]int{}
	for i, it := range v.Items {
		if _, seen := order[it.ID]; !seen {
			order[it.ID] = i
			all = append(all, candidates(it.ID, it.Duration, moments[it.ID], lo, hi, each)...)
		}
	}
	return pickMoments(all, n, order)
}

// candidates are a clip's candidate moments: complete stretches of speech of
// lo–hi seconds that start cleanly (the first words, after a pause or another
// voice) and end cleanly (a pause, another voice, the end), scored; without
// speech, what moves on screen.
func candidates(assetID string, duration float64, ms []ops.Moment, lo, hi, target float64) []footMoment {
	var segs, seen []ops.Moment
	for _, m := range ms {
		if m.AssetID != assetID && m.AssetID != "" {
			continue
		}
		switch m.Kind {
		case "speech":
			if strings.TrimSpace(m.Text) != "" {
				segs = append(segs, m)
			}
		case "visual", "scene":
			seen = append(seen, m)
		}
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].From < segs[j].From })
	clamp := func(t float64) float64 {
		if duration > 0 && t > duration {
			return duration
		}
		return math.Max(0, t)
	}
	var out []footMoment
	for i := range segs {
		if i > 0 && segs[i].From-segs[i-1].To < 0.3 && segs[i].Detail == segs[i-1].Detail && !endsSentence(segs[i-1].Text) {
			continue // in the middle of someone's sentence
		}
		for j := i; j < len(segs); j++ {
			if j > i && segs[j].From-segs[j-1].To >= 2 {
				break // a long pause: the moment is over
			}
			from, to := clamp(segs[i].From-0.15), clamp(segs[j].To+0.3)
			length := to - from
			if length > hi {
				break
			}
			if length < lo {
				continue
			}
			if end := j+1 == len(segs) || segs[j+1].From-segs[j].To >= 0.6 || segs[j+1].Detail != segs[j].Detail || endsSentence(segs[j].Text); !end {
				continue
			}
			out = append(out, scoreSpeech(assetID, segs[i:j+1], from, to, target, lo, hi))
		}
	}
	if len(out) > 0 || len(segs) > 0 {
		return out
	}
	// No words: what moves on screen, a moment of the right length around it.
	for _, m := range seen {
		mid := (m.From + m.To) / 2
		length := math.Min(target, math.Max(lo, m.To-m.From))
		if duration > 0 {
			length = math.Min(length, duration)
		}
		from := clamp(mid - length/2)
		to := clamp(from + length)
		from = clamp(to - length)
		if to-from < math.Min(lo, 1) {
			continue
		}
		fm := footMoment{AssetID: assetID, From: round3(from), To: round3(to)}
		switch m.Change {
		case "motion":
			fm.Score, fm.Why = 1, []string{"movement on screen"}
		case "gradual":
			fm.Score, fm.Why = 0.8, []string{"a slow change on screen (a pan, a zoom, the light, someone coming into view)"}
		default:
			fm.Score, fm.Why = 0.4, []string{"a cut in the footage"}
		}
		out = append(out, fm)
	}
	return out
}

// scoreSpeech scores a stretch of speech.
func scoreSpeech(assetID string, ss []ops.Moment, from, to, target, lo, hi float64) footMoment {
	m := footMoment{AssetID: assetID, From: round3(from), To: round3(to), First: firstSentence(ss[0].Text), Ref: ss[0].Ref}
	var texts []string
	voices := map[string]bool{}
	for _, s := range ss {
		texts = append(texts, strings.TrimSpace(s.Text))
		if s.Detail != "" {
			voices[s.Detail] = true
		}
	}
	m.Text = strings.Join(texts, " ")
	score := 0.0
	if strings.HasSuffix(strings.TrimSpace(ss[0].Text), "?") && len(ss) > 1 && ss[1].Detail != ss[0].Detail {
		score += 2
		m.Why = append(m.Why, "a question and its answer")
	}
	if pickLaugh.MatchString(m.Text) {
		score += 1.5
		m.Why = append(m.Why, "people laugh")
	}
	if pickClaim.MatchString(m.First) {
		score += 1
		m.Why = append(m.Why, "a strong first line")
	}
	if len(voices) >= 2 {
		score += 0.5
		m.Why = append(m.Why, "two voices")
	}
	score += 0.5 * (1 - math.Min(1, math.Abs((to-from)-target)/math.Max(1, hi-lo)))
	m.Score = math.Round(score*100) / 100
	if len(m.Why) == 0 {
		m.Why = []string{"a complete stretch that starts and ends cleanly"}
	}
	return m
}

// endsSentence reports whether words end a sentence.
func endsSentence(s string) bool {
	s = strings.TrimSpace(s)
	return strings.HasSuffix(s, ".") || strings.HasSuffix(s, "?") || strings.HasSuffix(s, "!")
}

// firstSentence is a text's first sentence.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	for i, r := range s {
		if (r == '.' || r == '?' || r == '!') && (i+1 == len(s) || s[i+1] == ' ') {
			return s[:i+1]
		}
	}
	return s
}

// pickMoments chooses n moments: the best first, none overlapping another,
// each pick from a clip already picked from counting a little less (so the
// moments spread over the footage), then in the footage's order.
func pickMoments(all []footMoment, n int, order map[string]int) []footMoment {
	var out []footMoment
	from := map[string]int{}
	overlaps := func(c footMoment) bool {
		for _, o := range out {
			if o.AssetID != c.AssetID {
				continue
			}
			if inter := math.Min(c.To, o.To) - math.Max(c.From, o.From); inter > 0.3*math.Min(c.To-c.From, o.To-o.From) {
				return true
			}
		}
		return false
	}
	for len(out) < n {
		best, bestScore := -1, math.Inf(-1)
		for i, c := range all {
			if overlaps(c) {
				continue
			}
			s := c.Score - 0.25*float64(from[c.AssetID])
			if s > bestScore+1e-9 || (math.Abs(s-bestScore) <= 1e-9 && best >= 0 && earlier(c, all[best], order)) {
				best, bestScore = i, s
			}
		}
		if best < 0 {
			break
		}
		out = append(out, all[best])
		from[all[best].AssetID]++
	}
	sort.SliceStable(out, func(i, j int) bool { return earlier(out[i], out[j], order) })
	return out
}

func earlier(a, b footMoment, order map[string]int) bool {
	if order[a.AssetID] != order[b.AssetID] {
		return order[a.AssetID] < order[b.AssetID]
	}
	return a.From < b.From
}

// pickClips makes a section's first picks its clips (named like stand-ins:
// moments-1, moments-2…), each saying why it was picked.
func pickClips(sec models.RecipeSection, picks []footMoment, run string, taken map[string]bool) ([]editspec.Clip, []models.RecipeStandIn) {
	var clips []editspec.Clip
	var ins []models.RecipeStandIn
	purpose := strings.TrimSpace(sec.Purpose)
	if purpose == "" {
		purpose = "a moment for this part of the video"
	}
	for i, pk := range picks {
		id := fmt.Sprintf("%s-%d", sec.Name, i+1)
		for x := 2; taken[id]; x++ {
			id = fmt.Sprintf("%s-%d-%d", sec.Name, i+1, x)
		}
		taken[id] = true
		why := joinAnd(pk.Why)
		note := fmt.Sprintf("A first pick: MetaRush chose this moment for the section “%s” (%s) — %s — because it is %s. Claude keeps it or changes it. Made for %s.",
			sec.Name, sectionNeeds(sec), purpose, why, run)
		c := editspec.Clip{ID: id, AssetID: pk.AssetID, In: pk.From, Out: pk.To, Label: shorten("First pick: "+why, 120), Rationale: shorten(upperFirst(why)+".", 2000),
			Notes: shorten(note, 4000)}
		if pk.First != "" {
			from, to := pk.From, pk.To
			c.Evidence = []editspec.Evidence{{Kind: "transcript", AssetID: pk.AssetID, From: &from, To: &to, Ref: pk.Ref, Quote: shorten(pk.First, 200)}}
		}
		clips = append(clips, c)
		ins = append(ins, models.RecipeStandIn{Section: sec.Name, ClipID: id, AssetID: pk.AssetID, In: pk.From, Out: pk.To, First: true, Why: pk.Why, Words: shorten(pk.Text, 300)})
	}
	return clips, ins
}
