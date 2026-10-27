package editspec

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/metavert-io/metarush/internal/textalign"
)

// Words changed in the speaker's own voice (D308).
//
// A said change is part of the cut's sound, like a clearer copy (mix.enhanced):
// wherever the cut plays the recording over [From, To), the new sound plays
// instead of the recording's own. The recording, its transcript and every
// other cut stay as they were; the cut hears (captions, the speech check, its
// transcript) the new words in place of the old.

// SaidEdge is how much of the recording's own sound a new sound carries at
// each end (seconds): the plan crossfades the recording into it over that
// stretch, where both play the same samples, so the change has no seam.
const SaidEdge = 0.1

// SaidSound is one change: a word or short phrase someone said, said again
// differently in their own voice copy, with their consent (D309).
type SaidSound struct {
	// ID is the change's record (who asked, the voice copy, the consent, the checks).
	ID      string  `json:"id" bson:"id" jsonschema:"pattern=^swc_[0-9a-z]{26}$,description=The change's record: who asked\\, whose voice copy and consent\\, and how it was checked."`
	AssetID string  `json:"assetId" bson:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=The recording whose words changed."`
	From    float64 `json:"from" bson:"from" jsonschema:"minimum=0,description=Where in the recording the new sound starts (seconds; it begins and ends with the recording's own sound)."`
	To      float64 `json:"to" bson:"to" jsonschema:"minimum=0,description=Where it ends."`
	// Sound is the new sound: To−From long, the recording's own sound at both
	// ends, the new words between them over the room's own quiet.
	Sound string `json:"sound" bson:"sound" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=The new sound (a clip MetaRush made\\, as long as the stretch)."`
	// WordsFrom and WordsTo are where the new words are heard (recording seconds).
	WordsFrom float64 `json:"wordsFrom" bson:"wordsFrom" jsonschema:"minimum=0"`
	WordsTo   float64 `json:"wordsTo" bson:"wordsTo" jsonschema:"minimum=0"`
	Words     string  `json:"words" bson:"words" jsonschema:"minLength=1,maxLength=200,description=What is said now."`
	Before    string  `json:"before" bson:"before" jsonschema:"maxLength=400,description=What was said."`
	// Who is whose voice it is (the name a person gave the voice).
	Who string `json:"who,omitempty" bson:"who,omitempty" jsonschema:"maxLength=120"`
	// Picture is the same stretch with the mouth matched to the new words
	// (lip sync, D312): a clip that plays from To−From… in place of the
	// recording's picture where a clip of the cut plays it.
	Picture string `json:"picture,omitempty" bson:"picture,omitempty" jsonschema:"pattern=^(ast_[0-9a-z]{26})?$,description=The stretch with the mouth matched to the new words (a clip MetaRush made\\, as long as the stretch)."`
}

// weight is how much of the new sound plays at recording time t (0–1): none
// outside the stretch, all of it inside, a crossfade over each edge.
func (c *SaidSound) weight(t float64) float64 {
	if t <= c.From || t >= c.To {
		return 0
	}
	e := math.Min(SaidEdge, (c.To-c.From)/2)
	if e <= 0 {
		return 1
	}
	switch {
	case t < c.From+e:
		return (t - c.From) / e
	case t > c.To-e:
		return (c.To - t) / e
	}
	return 1
}

// SaidFor lists the changes to a recording's words, by time.
func (m *Mix) SaidFor(assetID string) []SaidSound {
	if m == nil || assetID == "" {
		return nil
	}
	var out []SaidSound
	for _, c := range m.Said {
		if c.AssetID == assetID {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// saidAt is the change whose stretch an element of assetID plays at its own
// time t, with t as recording time: the recording itself, or the clip with
// its mouth matched (whose time 0 is the stretch's start).
func (m *Mix) saidOf(assetID string) []saidPlay {
	if m == nil || assetID == "" {
		return nil
	}
	var out []saidPlay
	for i := range m.Said {
		c := &m.Said[i]
		switch assetID {
		case c.AssetID:
			out = append(out, saidPlay{c: c})
		case c.Picture:
			out = append(out, saidPlay{c: c, offset: c.From})
		}
	}
	return out
}

type saidPlay struct {
	c      *SaidSound
	offset float64 // added to the element's own time to get recording time
}

// SaidWords is a recording's transcript as a cut with changed words hears
// it: the words said only where the new sound plays are left out and the new
// words take their place, spread over where they are heard by their letters
// and marked as said in a voice copy.
func (m *Mix) SaidWords(assetID string, segs []TranscriptSegment) []TranscriptSegment {
	said := m.SaidFor(assetID)
	if len(said) == 0 || len(segs) == 0 {
		return segs
	}
	out := make([]TranscriptSegment, len(segs))
	for i, sg := range segs {
		out[i] = sg
		out[i].Words = append([]TranscriptWord(nil), sg.Words...)
	}
	for _, c := range said {
		lo, hi := c.From+math.Min(SaidEdge, (c.To-c.From)/2), c.To-math.Min(SaidEdge, (c.To-c.From)/2)
		at, pos := -1, 0
		for i := range out {
			sg := &out[i]
			if len(sg.Words) == 0 {
				continue
			}
			kept := sg.Words[:0:0]
			for _, w := range sg.Words {
				mid := (w.From + w.To) / 2
				if mid >= lo && mid <= hi {
					if at < 0 {
						at, pos = i, len(kept)
					}
					continue
				}
				kept = append(kept, w)
			}
			sg.Words = kept
		}
		if at < 0 { // nothing was said there: the segment the new words fall in
			for i := range out {
				if len(out[i].Words) > 0 && out[i].From <= c.WordsFrom && c.WordsFrom < out[i].To {
					at = i
					pos = sort.Search(len(out[i].Words), func(k int) bool { return out[i].Words[k].From >= c.WordsFrom })
					break
				}
			}
		}
		if at < 0 {
			continue
		}
		nw := spreadWords(c.Words, c.WordsFrom, c.WordsTo)
		sg := &out[at]
		ws := make([]TranscriptWord, 0, len(sg.Words)+len(nw))
		ws = append(ws, sg.Words[:pos]...)
		ws = append(ws, nw...)
		ws = append(ws, sg.Words[pos:]...)
		sg.Words = ws
	}
	for i := range out {
		sg := &out[i]
		if !changedSeg(segs[i], sg) {
			continue
		}
		texts := make([]string, len(sg.Words))
		for k, w := range sg.Words {
			texts[k] = w.Text
		}
		sg.Text = textalign.JoinWords(texts)
	}
	return out
}

func changedSeg(a TranscriptSegment, b *TranscriptSegment) bool {
	if len(a.Words) != len(b.Words) {
		return true
	}
	for i := range a.Words {
		if a.Words[i] != b.Words[i] {
			return true
		}
	}
	return false
}

// spreadWords times words over [from, to) by their letters, marked as said
// in a voice copy.
func spreadWords(text string, from, to float64) []TranscriptWord {
	toks := strings.Fields(text)
	if len(toks) == 0 {
		return nil
	}
	if to < from {
		to = from
	}
	weights := make([]float64, len(toks))
	total := 0.0
	for i, t := range toks {
		n := 0
		for _, r := range t {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				n++
			}
		}
		weights[i] = float64(max(1, n))
		total += weights[i]
	}
	out := make([]TranscriptWord, len(toks))
	t := from
	for i, tok := range toks {
		d := (to - from) * weights[i] / total
		out[i] = TranscriptWord{Text: tok, From: round3(t), To: round3(t + d), Said: true}
		t += d
	}
	out[len(out)-1].To = round3(to)
	return out
}

// normalizeSaid keeps changes in a canonical form: times to the millisecond,
// by recording and time.
func normalizeSaid(m *Mix) {
	if m == nil || len(m.Said) == 0 {
		return
	}
	for i := range m.Said {
		c := &m.Said[i]
		c.From, c.To, c.WordsFrom, c.WordsTo = round3(c.From), round3(c.To), round3(c.WordsFrom), round3(c.WordsTo)
		c.Words, c.Before, c.Who = strings.TrimSpace(c.Words), strings.TrimSpace(c.Before), strings.TrimSpace(c.Who)
	}
	sort.SliceStable(m.Said, func(i, j int) bool {
		if m.Said[i].AssetID != m.Said[j].AssetID {
			return m.Said[i].AssetID < m.Said[j].AssetID
		}
		return m.Said[i].From < m.Said[j].From
	})
}

// sayInMix lays the new sounds over the clips that play their stretches: the
// clip's own sound fades out over the edge as the new sound fades in (both
// play the same samples there) and comes back the same way. A new sound
// follows the clip's own gain (levels, ducking, fades) and is marked as AI
// speech. Clips played faster or slower keep their own sound (Validate says so).
func sayInMix(s *Spec, elems []*audioElement, fps float64, facts Facts) []*audioElement {
	m := s.Mix
	if m == nil || len(m.Said) == 0 {
		return elems
	}
	var added []*audioElement
	for _, e := range elems {
		pc := e.clip
		if pc == nil || IsConversation(pc.AssetID) || IsNested(pc.AssetID) || math.Abs(pc.PlaybackRate-1) > 1e-6 {
			continue
		}
		plays := m.saidOf(pc.AssetID)
		if len(plays) == 0 {
			continue
		}
		pcv, from, g0 := *pc, e.from, e.gain
		src := func(f float64) float64 { return srcAt(pcv, fps, f) } // the element's own time at timeline frame f
		s0, s1 := src(float64(e.from)), src(float64(e.from+e.dur))
		var mine []saidPlay
		for _, p := range plays {
			c := p.c
			if !saidReady(c, facts) {
				continue
			}
			a0, a1 := c.From-p.offset, c.To-p.offset // the stretch in the element's own time
			if a1 <= s0 || a0 >= s1 {
				continue
			}
			mine = append(mine, p)
			fa := max(e.from, int(math.Ceil(tlAt(pcv, fps, math.Max(a0, s0))-1e-6)))
			fb := min(e.from+e.dur, int(math.Floor(tlAt(pcv, fps, math.Min(a1, s1))+1e-6)))
			if fb <= fa {
				continue
			}
			exact := (src(float64(fa)) + p.offset - c.From) * fps
			trim, frac := SplitTrim(exact)
			cc, off := c, p.offset
			pa := &PlanAudio{ID: "said-" + c.ID, AssetID: c.Sound, Role: RoleDialogue, Bus: BusSource, From: fa, DurationInFrames: fb - fa,
				TrimBefore: trim, TrimFraction: frac, PlaybackRate: 1, AIVoice: true}
			start := fa
			added = append(added, &audioElement{assetID: c.Sound, from: fa, dur: fb - fa, rate: 1, peakDB: e.peakDB, audio: pa,
				gain: func(f int) float64 { return g0(start-from+f) * cc.weight(src(float64(start+f))+off) }})
		}
		if len(mine) == 0 {
			continue
		}
		e.gain = func(f int) float64 {
			t := src(float64(from + f))
			w := 0.0
			for _, p := range mine {
				w = math.Max(w, p.c.weight(t+p.offset))
			}
			return g0(f) * (1 - w)
		}
	}
	return append(elems, added...)
}

// saidReady reports whether a change's new sound can play (without facts, as
// in plain plans of a spec, it is taken as written).
func saidReady(c *SaidSound, facts Facts) bool {
	if c.Sound == "" || c.To <= c.From {
		return false
	}
	if facts == nil {
		return true
	}
	v, ok := facts.Asset(c.Sound)
	return ok && v.HasAudio && (v.State == "" || v.State == "available")
}

// validateSaid checks the changed words: each new sound is there, as long as
// its stretch, and plays where the cut plays the recording.
func validateSaid(s *Spec, facts Facts, clips []PlanClip) []Issue {
	m := s.Mix
	if m == nil || len(m.Said) == 0 {
		return nil
	}
	var out []Issue
	for i, c := range m.Said {
		p := fmt.Sprintf("/mix/said/%d", i)
		words := "“" + c.Words + "”"
		if c.To <= c.From || c.WordsTo < c.WordsFrom {
			out = append(out, errI("bad_said", p, "The new words %s have no stretch to play in; undo the change and make it again.", words))
			continue
		}
		for j, o := range m.Said[:i] {
			if o.AssetID == c.AssetID && o.From < c.To && c.From < o.To {
				out = append(out, errI("said_overlap", p, "The new words %s overlap other new words (%d) in the same recording; undo one of them.", words, j+1))
			}
		}
		if facts == nil {
			continue
		}
		src, ok := facts.Asset(c.AssetID)
		if !ok {
			out = append(out, errI("missing_asset", p+"/assetId", "The recording whose words changed to %s isn't in this workspace any more; undo the change.", words))
			continue
		}
		v, ok := facts.Asset(c.Sound)
		switch {
		case !ok:
			out = append(out, errI("missing_asset", p+"/sound", "The new words %s in %s are missing; undo the change and make it again.", words, src.Name))
			continue
		case !v.HasAudio:
			out = append(out, errI("no_audio_stream", p+"/sound", "The new words %s in %s have no sound; undo the change and make it again.", words, src.Name))
			continue
		case v.State != "" && v.State != "available":
			out = append(out, errI("asset_not_ready", p+"/sound", "The new words %s are still being saved; wait a moment, then check again.", words))
			continue
		case v.Duration > 0 && math.Abs(v.Duration-(c.To-c.From)) > 0.06:
			out = append(out, errI("bad_said", p+"/sound", "The new words %s don't fit their stretch of %s; undo the change and make it again.", words, src.Name))
			continue
		}
		played, cutInside, faster := false, false, false
		for _, pc := range clips {
			off := -1.0
			switch pc.AssetID {
			case c.AssetID:
				off = 0
			case c.Picture:
				if c.Picture != "" {
					off = c.From
				}
			}
			if off < 0 {
				continue
			}
			a, b := pc.SourceIn+off, pc.SourceOut+off
			if b <= c.From || a >= c.To {
				continue
			}
			if math.Abs(pc.PlaybackRate-1) > 1e-6 {
				faster = true
				continue
			}
			played = true
			if (a > c.From+SaidEdge && a < c.To-SaidEdge) || (b > c.From+SaidEdge && b < c.To-SaidEdge) {
				cutInside = true
			}
		}
		switch {
		case faster:
			out = append(out, warnI("said_speed", p, "A clip plays %s faster or slower, so it keeps what was said there; the new words %s play only at its normal speed.", src.Name, words))
		case !played:
			out = append(out, warnI("said_unused", p, "The cut doesn't play the stretch of %s where %s is said any more, so the change does nothing; undo it to tidy up.", src.Name, words))
		case cutInside:
			out = append(out, warnI("said_cut", p, "A clip starts or ends inside the new words %s in %s, so only part of them is heard.", words, src.Name))
		}
	}
	return out
}

// SaidPlay is where a cut plays a change's new words: its own changes and
// those of the cuts it nests (D311). From and To are seconds on the cut's
// timeline (the first play to the end of the last); Nested names the clip
// whose nested cut plays it ("" for the cut's own).
type SaidPlay struct {
	ChangeID string
	From, To float64
	Nested   string
}

// SaidChangeID is the change a plan's sound plays ("" when it isn't new
// words): "said-<change>", or "<clip>/said-<change>" in a nested cut.
func SaidChangeID(audioID string) string {
	last := audioID[strings.LastIndex(audioID, "/")+1:]
	if !strings.HasPrefix(last, "said-") {
		return ""
	}
	return strings.TrimPrefix(last, "said-")
}

// SaidPlays lists the changes a plan plays, once each, in the order they are
// first heard.
func SaidPlays(p *RenderPlan) []SaidPlay {
	if p == nil || p.FPS <= 0 {
		return nil
	}
	fps := float64(p.FPS)
	at := map[string]int{}
	var out []SaidPlay
	for _, pa := range p.Audio {
		id := SaidChangeID(pa.ID)
		if id == "" || !pa.AIVoice || pa.DurationInFrames <= 0 {
			continue
		}
		from, to := float64(pa.From)/fps, float64(pa.From+pa.DurationInFrames)/fps
		if i, ok := at[id]; ok {
			out[i].From, out[i].To = math.Min(out[i].From, from), math.Max(out[i].To, to)
			continue
		}
		at[id] = len(out)
		out = append(out, SaidPlay{ChangeID: id, From: from, To: to, Nested: pa.Nested})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// AssetPlays is where a plan plays an asset (seconds on its timeline, the
// first play to the end of the last): as a sound, as a clip, or inside a
// nested cut (where the clip that plays that cut is).
func AssetPlays(p *RenderPlan, id string) (from, to float64, ok bool) {
	if p == nil || p.FPS <= 0 || id == "" {
		return 0, 0, false
	}
	fps := float64(p.FPS)
	add := func(f0, n int) {
		a, b := float64(f0)/fps, float64(f0+n)/fps
		if !ok {
			from, to, ok = a, b, true
			return
		}
		from, to = math.Min(from, a), math.Max(to, b)
	}
	for _, pa := range p.Audio {
		if pa.AssetID == id && pa.DurationInFrames > 0 {
			add(pa.From, pa.DurationInFrames)
		}
	}
	for _, c := range p.Clips {
		if c.AssetID == id {
			add(c.From, c.DurationInFrames)
		} else if np := p.Nested[c.AssetID]; IsNested(c.AssetID) && np != nil && planHas(np.Plan, id, 0) {
			add(c.From, c.DurationInFrames)
		}
	}
	return from, to, ok
}

// planHas reports whether a plan shows or plays an asset, its nested cuts' included.
func planHas(p *RenderPlan, id string, depth int) bool {
	if p == nil || depth > MaxNestDepth {
		return false
	}
	for _, pa := range p.Audio {
		if pa.AssetID == id {
			return true
		}
	}
	for _, c := range p.Clips {
		if c.AssetID == id {
			return true
		}
		if np := p.Nested[c.AssetID]; np != nil && planHas(np.Plan, id, depth+1) {
			return true
		}
	}
	return false
}
