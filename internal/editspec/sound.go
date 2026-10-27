package editspec

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Sound at cuts. The picture timeline (Timeline) places every clip; this file
// decides where each clip's *sound* plays. By default it plays exactly under its
// picture, as cuts always have. A transition's audio setting turns a cut into a
// split edit (the incoming sound leads the picture, or the outgoing sound carries
// over), and the mix's cutBlend gives straight cuts a short blend so they never
// click. Either way only one clip's sound is primary at any moment: the other one
// fades in or out over the blend.
//
// Everything that cares about what is heard (the mix envelopes, ducking under
// dialogue, captions, QA's speech expectations) reads the same resolved windows.

// cutSound is the resolved sound change at the cut into clip idx.
type cutSound struct {
	idx       int
	follow    bool   // the sound follows the picture transition (no split, no blend)
	mode      string // asked: cut | lead | carry | auto
	result    string // cut | lead | carry
	cut       int    // timeline frame of the picture cut
	edit      int    // timeline frame where the incoming sound starts
	blend     int    // frames the outgoing sound fades over, from edit
	vfade     bool   // no footage to overlap: each side fades inside its own window
	power     bool   // equal-power blend (long blends)
	requested int    // lead/carry asked for (frames)
	limited   string // footage | neighbours
	footage   int    // frames of sound the footage has beyond the picture on the side that limited it
	noPause   bool   // auto found no pause within reach
	words     cutWords
}

// cutWords is what a cut does to speech.
type cutWords struct {
	clipped  []string // words cut partway (either side)
	droppedA []string // words of the outgoing clip that are no longer heard
	droppedB []string // words of the incoming clip that are no longer heard
	overlap  bool     // both voices are heard at once
}

// soundWindow is where one clip's sound plays (timeline frames [from, to)).
type soundWindow struct {
	from, to           int
	blendIn, blendOut  int
	splitIn, splitOut  bool // the in/out side is a split or blend (not the picture's own transition)
	powerIn, powerOut  bool
	edgeIn, edgeOut    int  // short fades at the very start or end of the sound (cut blends)
	separate           bool // the window differs from the picture window
	heardIn, heardOut  float64
	ownFadeOut         int // the clip's own fade-out (frames), without what a dip adds
	legacy             bool
	hasTranscriptWords bool
}

type soundPlan struct {
	fps      float64
	cutBlend int
	cuts     []cutSound
	win      []soundWindow
}

// Word margins: transcript word times are approximate and consonant tails ring
// on, so a sound change keeps this far from every word.
const (
	wordPreMargin  = 0.03
	wordPostMargin = 0.06
)

// srcAt is the source time a clip plays at timeline frame f (also outside its picture).
func srcAt(pc PlanClip, fps float64, f float64) float64 {
	return pc.SourceIn + (f-float64(pc.From))/fps*pc.PlaybackRate
}

// tlAt is the (fractional) timeline frame where a clip plays source time t.
func tlAt(pc PlanClip, fps float64, t float64) float64 {
	return float64(pc.From) + (t-pc.SourceIn)/pc.PlaybackRate*fps
}

func framesOf(sec, fps float64) int { return int(math.Round(sec * fps)) }

// blendFrames is a split's blend: the transition's own, else the mix's cut blend,
// else the default.
func blendFrames(s *Spec, tr *Transition, fps float64) int {
	switch {
	case tr != nil && tr.Audio != nil && tr.Audio.Blend != nil:
		return max(0, framesOf(*tr.Audio.Blend, fps))
	case s.Mix != nil && s.Mix.CutBlend != nil:
		return max(0, framesOf(*s.Mix.CutBlend, fps))
	}
	return max(0, framesOf(DefaultSplitBlend, fps))
}

// resolveSound places every clip's sound. clips is the picture timeline.
func resolveSound(s *Spec, clips []PlanClip, facts Facts) *soundPlan {
	fps := float64(s.Output.FPS)
	n := len(clips)
	sp := &soundPlan{fps: fps, cuts: make([]cutSound, n), win: make([]soundWindow, n)}
	if s.Mix != nil && s.Mix.CutBlend != nil {
		sp.cutBlend = max(0, framesOf(*s.Mix.CutBlend, fps))
	}
	words := make([][]tlWord, n)
	sourceMuted := mixFor(s).busMuted[BusSource]
	wordsOf := func(i int) []tlWord {
		if words[i] == nil {
			words[i] = []tlWord{}
			// Words of a clip nobody hears do not steer splits.
			if i < len(s.Clips) && !sourceMuted && audibleClip(s.Clips[i], facts) {
				words[i] = clipTimelineWords(clips[i], facts, fps)
			}
		}
		return words[i]
	}
	for i := range clips {
		sp.cuts[i] = cutSound{idx: i, follow: true, cut: clips[i].From}
	}
	for i := 1; i < n; i++ {
		a, b := clips[i-1], clips[i]
		var tr *Transition
		if i < len(s.Clips) {
			tr = s.Clips[i].Transition
		}
		ov := 0
		if IsOverlapTransition(b.TransitionIn.Type) {
			ov = max(0, a.From+a.DurationInFrames-b.From)
		}
		cs := cutSound{idx: i, follow: true, cut: b.From + ov/2}
		switch {
		case tr != nil && tr.Audio != nil && tr.Audio.Mode != "":
			cs.follow, cs.mode = false, tr.Audio.Mode
		case b.TransitionIn.Type == TransitionCut && sp.cutBlend > 0:
			cs.follow, cs.mode = false, SoundCut
		}
		if !cs.follow {
			blend := sp.cutBlend
			if tr != nil && tr.Audio != nil && tr.Audio.Mode != "" {
				blend = blendFrames(s, tr, fps)
			}
			dur := 0.0
			if tr != nil && tr.Audio != nil {
				dur = tr.Audio.Duration
			}
			sp.place(&cs, a, b, blend, dur, facts, wordsOf(i-1), wordsOf(i))
		} else if ov > 0 {
			// The sound follows a dissolve-like transition: both are heard during it.
			wa, wb := wordsOf(i-1), wordsOf(i)
			wa, wb = ownWords(clipSpeakers(a, b, wa, wb))
			cs.words = overlapWords(wa, wb, float64(b.From), float64(a.From+a.DurationInFrames))
		}
		sp.cuts[i] = cs
	}
	for i := range clips {
		pc := clips[i]
		w := soundWindow{from: pc.From, to: pc.From + pc.DurationInFrames}
		if i < len(s.Clips) {
			w.ownFadeOut = max(0, framesOf(s.Clips[i].FadeOut, fps))
		}
		if i > 0 && !sp.cuts[i].follow {
			c := sp.cuts[i]
			w.splitIn, w.powerIn = true, c.power
			if c.vfade {
				w.from, w.blendIn = c.cut, c.blend/2
			} else {
				w.from, w.blendIn = c.edit, c.blend
			}
		}
		if i+1 < n && !sp.cuts[i+1].follow {
			c := sp.cuts[i+1]
			w.splitOut, w.powerOut = true, c.power
			if c.vfade {
				w.to, w.blendOut = c.cut, c.blend-c.blend/2
			} else {
				w.to, w.blendOut = c.edit+c.blend, c.blend
			}
		}
		if sp.cutBlend > 0 {
			if i == 0 && pc.TransitionIn.Type == TransitionCut {
				w.edgeIn = sp.cutBlend // the very first sound starts with a short fade
			}
			if i == n-1 && pc.FadeOutFrames == 0 {
				w.edgeOut = sp.cutBlend // ... and the last one ends with one
			}
			if i+1 < n && sp.cuts[i+1].follow && clips[i+1].TransitionIn.Type == TransitionFade && pc.FadeOutFrames == 0 {
				w.edgeOut = sp.cutBlend // before a fade-in from the background
			}
		}
		if w.to <= w.from {
			w.to = w.from + 1
		}
		w.separate = w.from != pc.From || w.to != pc.From+pc.DurationInFrames
		w.legacy = !w.separate && !w.splitIn && !w.splitOut && w.edgeIn == 0 && w.edgeOut == 0
		if w.legacy || (!w.separate && w.blendIn == 0 && w.blendOut == 0) {
			w.heardIn, w.heardOut = pc.SourceIn, pc.SourceOut
		} else {
			w.heardIn = math.Max(0, srcAt(pc, fps, float64(w.from)+float64(w.blendIn)/2))
			w.heardOut = srcAt(pc, fps, float64(w.to)-float64(w.blendOut)/2)
			if facts != nil {
				if af, ok := facts.Asset(pc.AssetID); ok && af.Duration > 0 {
					w.heardOut = math.Min(w.heardOut, af.Duration)
				}
			}
		}
		sp.win[i] = w
	}
	return sp
}

// place resolves one split (or blend) between the outgoing clip a and the
// incoming clip b: where b's sound starts, how long they blend, and what that
// does to the words on either side.
func (sp *soundPlan) place(cs *cutSound, a, b PlanClip, blend int, dur float64, facts Facts, wa, wb []tlWord) {
	fps := sp.fps
	i := cs.idx
	C := cs.cut
	// The footage limits how far each sound can extend past its picture.
	bMin := b.From - int(math.Floor(b.SourceIn*fps/b.PlaybackRate+1e-6))
	aMax := a.From + a.DurationInFrames
	if facts != nil {
		if af, ok := facts.Asset(a.AssetID); ok && af.Duration > 0 {
			aMax = a.From + int(math.Floor((af.Duration-a.SourceIn)*fps/a.PlaybackRate+1e-6))
		}
	}
	// The neighbouring cuts limit it too: each clip keeps some sound of its own.
	aStart, prevBlend := a.From, 0
	if i-1 >= 1 && !sp.cuts[i-1].follow {
		p := sp.cuts[i-1]
		aStart, prevBlend = p.edit, p.blend
		if p.vfade {
			aStart, prevBlend = p.cut, p.blend/2
		}
	}
	nbLo := aStart + prevBlend + 1
	nbHi := b.From + b.DurationInFrames - 1
	cs.power = float64(blend)/fps >= 0.25
	cs.blend = blend
	// Words of the other clip's person (picked up by this clip's microphone too)
	// are heard through that clip: they are neither cut nor lost here.
	wa, wb = ownWords(clipSpeakers(a, b, wa, wb))
	center := C - blend/2
	want := center
	switch cs.mode {
	case SoundLead:
		cs.requested = framesOf(dur, fps)
		want = C - cs.requested
	case SoundCarry:
		cs.requested = framesOf(dur, fps)
		want = C + cs.requested
	}
	lo, hi := max(bMin, nbLo), min(aMax, nbHi)-blend
	if lo > hi {
		// Not enough room for the whole blend: shorten it, or fade each side
		// inside its own window.
		if room := min(aMax, nbHi) - lo; room >= 1 && max(bMin, nbLo) <= C && C <= min(aMax, nbHi) {
			blend = min(blend, room)
			cs.blend = blend
			center = C - blend/2
			lo, hi = max(bMin, nbLo), min(aMax, nbHi)-blend
		}
		if lo > hi {
			cs.vfade, cs.edit, cs.result = true, C, SoundCut
			switch {
			case cs.mode == SoundLead && cs.requested > 0:
				cs.limited, cs.footage = "footage", max(0, b.From-bMin)
			case cs.mode == SoundCarry && cs.requested > 0:
				cs.limited, cs.footage = "footage", max(0, aMax-(a.From+a.DurationInFrames))
			}
			cs.words = splitWords(wa, wb, C, C, C, float64(aStart))
			return
		}
	}
	if cs.mode == SoundAuto {
		reach := framesOf(DefaultAutoSplit, fps)
		if dur > 0 {
			reach = framesOf(dur, fps)
		}
		want, cs.noPause = autoEdit(wa, wb, C, center, blend, max(lo, C-reach), min(hi, C+reach), fps, float64(aStart))
	}
	e := want
	if e < lo {
		e = lo
		if cs.mode == SoundLead {
			cs.limited = "neighbours"
			if bMin >= nbLo {
				cs.limited, cs.footage = "footage", b.From-bMin
			}
		}
	}
	if e > hi {
		e = hi
		if cs.mode == SoundCarry {
			cs.limited = "neighbours"
			if aMax <= nbHi {
				cs.limited, cs.footage = "footage", aMax-(a.From+a.DurationInFrames)
			}
		}
	}
	cs.edit = e
	switch {
	case cs.mode == SoundCut:
		cs.result = SoundCut
	case e < center-1 || (cs.mode == SoundLead && e < C):
		cs.result = SoundLead
	case e > center+1 || (cs.mode == SoundCarry && e > C):
		cs.result = SoundCarry
	default:
		cs.result = SoundCut
	}
	cs.words = splitWords(wa, wb, C, e, e+blend, float64(aStart))
}

// clipSpeakers names the person each of two neighbouring clips is about: who
// talks most inside its picture. Unnamed voices ("Speaker 2") only identify a
// person within one recording, so they count only when both clips come from it.
func clipSpeakers(a, b PlanClip, wa, wb []tlWord) (string, string, []tlWord, []tlWord) {
	same := a.AssetID == b.AssetID
	return clipSpeaker(a, wa, same), clipSpeaker(b, wb, same), wa, wb
}

// clipSpeaker is the person who talks most inside a clip's picture ("" when
// unknown, or unnamed and not comparable).
func clipSpeaker(pc PlanClip, ws []tlWord, allowUnnamed bool) string {
	by := map[string]float64{}
	from, to := float64(pc.From), float64(pc.From+pc.DurationInFrames)
	for _, w := range ws {
		if w.speaker == "" || (!allowUnnamed && (reSpeakerLabel.MatchString(w.speaker) || reRawSpeaker.MatchString(w.speaker))) {
			continue
		}
		if d := math.Min(w.to, to) - math.Max(w.from, from); d > 0 {
			by[w.speaker] += d
		}
	}
	best, bestT := "", 0.0
	for k, v := range by {
		if v > bestT || (v == bestT && k < best) {
			best, bestT = k, v
		}
	}
	return best
}

// ownWords drops, from each side, the words spoken by the other clip's person.
func ownWords(sa, sb string, wa, wb []tlWord) ([]tlWord, []tlWord) {
	if sa == "" || sb == "" || sa == sb {
		return wa, wb
	}
	keep := func(ws []tlWord, other string) []tlWord {
		out := make([]tlWord, 0, len(ws))
		for _, w := range ws {
			if w.speaker != other {
				out = append(out, w)
			}
		}
		return out
	}
	return keep(wa, sb), keep(wb, sa)
}

// audibleClip reports whether a clip's own sound can be heard at all.
func audibleClip(c Clip, facts Facts) bool {
	if c.Muted || (c.Volume != nil && *c.Volume < 0.05) || facts == nil {
		return false
	}
	a, ok := facts.Asset(c.AssetID)
	return ok && a.HasAudio
}

// tlWord is a transcript word on the timeline (fractional frames), with margins applied.
type tlWord struct {
	text     string
	from, to float64
	speaker  string
}

// clipTimelineWords maps a clip's transcript words (all of them, also outside its
// in/out) onto the timeline through the clip's own timing.
func clipTimelineWords(pc PlanClip, facts Facts, fps float64) []tlWord {
	out := []tlWord{}
	if facts == nil || pc.PlaybackRate <= 0 {
		return out
	}
	pre, post := wordPreMargin/pc.PlaybackRate*fps, wordPostMargin/pc.PlaybackRate*fps
	for _, seg := range facts.Transcript(pc.AssetID) {
		if len(seg.Words) == 0 {
			if strings.TrimSpace(seg.Text) != "" {
				out = append(out, tlWord{text: strings.TrimSpace(seg.Text), from: tlAt(pc, fps, seg.From) - pre, to: tlAt(pc, fps, seg.To) + post, speaker: seg.Speaker})
			}
			continue
		}
		for _, w := range seg.Words {
			out = append(out, tlWord{text: strings.TrimSpace(w.Text), from: tlAt(pc, fps, w.From) - pre, to: tlAt(pc, fps, w.To) + post, speaker: seg.Speaker})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].from < out[j].from })
	return out
}

// splitWords accounts for what a sound change does to the words around the
// picture cut c: the incoming sound starts at e0 and the outgoing one fades out
// over [e0, e1) (e0 == e1: a hard switch). aStart is where the outgoing clip's
// sound starts (earlier words are not its concern). The outgoing clip includes
// its words that start before the picture cut; the incoming clip includes its
// words that end after it.
func splitWords(wa, wb []tlWord, c, e0, e1 int, aStart float64) cutWords {
	var cw cutWords
	C, E0, E1 := float64(c), float64(e0), float64(e1)
	inFade := func(w tlWord) bool { return e1 > e0 && w.from < E1 && w.to > E0 }
	seen := map[string]bool{}
	clip := func(t string) {
		if !seen[t] {
			seen[t] = true
			cw.clipped = append(cw.clipped, t)
		}
	}
	var aTalks, bTalks bool
	for _, w := range wa {
		if w.to <= aStart {
			continue
		}
		// Cut: still sounding when the outgoing sound has faded out. A word that
		// ends during the fade is only softened.
		if w.from < E1 && w.to > E1 {
			clip(w.text)
		}
		if inFade(w) {
			aTalks = true
		}
		if w.from < C && w.from >= math.Max(E1, E0) {
			cw.droppedA = append(cw.droppedA, w.text) // included, but never heard
		}
	}
	for _, w := range wb {
		// Cut: already under way when the incoming sound starts. A word that
		// starts during the fade-in is only softened.
		if w.from < E0 && w.to > E0 {
			clip(w.text)
		}
		if inFade(w) {
			bTalks = true
		}
		if w.to > C && w.to <= E0 {
			cw.droppedB = append(cw.droppedB, w.text)
		}
	}
	cw.overlap = aTalks && bTalks
	return cw
}

// overlapWords reports two voices sounding together during an overlapping
// transition whose sound follows the picture.
func overlapWords(wa, wb []tlWord, from, to float64) cutWords {
	var cw cutWords
	var a, b bool
	for _, w := range wa {
		if w.from < to && w.to > from {
			a = true
		}
	}
	for _, w := range wb {
		if w.from < to && w.to > from {
			b = true
		}
	}
	cw.overlap = a && b
	return cw
}

// autoEdit finds where the incoming sound should start so that no word on either
// side is cut: the candidate in [lo, hi] closest to the straight cut that keeps
// every word whole, drops none of the words the clips include and brings in none
// they leave out. When nothing is clean it takes the least damaging one.
func autoEdit(wa, wb []tlWord, C, center, blend, lo, hi int, fps, aStart float64) (int, bool) {
	if lo > hi {
		return center, true
	}
	best, bestCost, bestClipped := center, math.Inf(1), 1
	c := float64(C)
	for e := lo; e <= hi; e++ {
		E0, E1 := float64(e), float64(e+blend)
		clipped, dropped, extra := 0, 0, 0
		for _, w := range wa {
			if w.to <= aStart {
				continue
			}
			if w.from < E1 && w.to > E0 {
				clipped++
			}
			kept := w.from < c
			heard := w.to <= E0
			switch {
			case kept && w.from >= E1:
				dropped++
			case !kept && heard:
				extra++
			}
		}
		for _, w := range wb {
			if w.from < E1 && w.to > E0 {
				clipped++
			}
			kept := w.to > c
			heard := w.from >= E1
			switch {
			case kept && w.to <= E0:
				dropped++
			case !kept && heard:
				extra++
			}
		}
		cost := 1000*float64(clipped) + 40*float64(dropped) + 25*float64(extra) + math.Abs(float64(e-center))
		if cost < bestCost {
			best, bestCost, bestClipped = e, cost, clipped
		}
	}
	return best, bestClipped > 0
}

// speechSeconds is how much speech a clip's sound carries (words heard in its window).
func speechSeconds(pc PlanClip, w soundWindow, facts Facts) float64 {
	if facts == nil {
		return 0
	}
	total := 0.0
	for _, seg := range facts.Transcript(pc.AssetID) {
		if len(seg.Words) == 0 {
			a, b := math.Max(seg.From, w.heardIn), math.Min(seg.To, w.heardOut)
			if b > a && strings.TrimSpace(seg.Text) != "" {
				total += b - a
			}
			continue
		}
		for _, wd := range seg.Words {
			if mid := (wd.From + wd.To) / 2; mid >= w.heardIn && mid < w.heardOut {
				total += math.Max(0, wd.To-wd.From)
			}
		}
	}
	return total
}

// clipSoundGain is a clip's linear gain at timeline frame t for a sound window
// that is not the plain picture window: volume × the in and out ramps (split
// blends, or the picture transition's own ramps on a side that follows it) ×
// short edge fades. Mix gains (bus, ducking, leveling, master) are applied on top.
func clipSoundGain(pc *PlanClip, next *PlanClip, w soundWindow, t int) float64 {
	g := math.Max(0, pc.Volume)
	f := t - pc.From
	ramp := func(p float64, power bool) float64 {
		p = math.Max(0, math.Min(1, p))
		if power {
			return math.Sin(p * math.Pi / 2)
		}
		return p
	}
	if w.splitIn {
		if k := t - w.from; w.blendIn > 0 && k < w.blendIn {
			g *= ramp(float64(k+1)/float64(w.blendIn+1), w.powerIn)
		}
	} else {
		g *= legacyIn(pc, f)
	}
	if w.edgeIn > 0 {
		if k := t - w.from; k < w.edgeIn {
			g *= float64(k+1) / float64(w.edgeIn+1)
		}
	}
	if w.splitOut {
		if k := t - (w.to - w.blendOut); w.blendOut > 0 && k >= 0 {
			g *= ramp(1-float64(k+1)/float64(w.blendOut+1), w.powerOut)
		}
		if w.ownFadeOut > 0 {
			g *= math.Min(1, math.Max(0, float64(w.to-t)/float64(w.ownFadeOut)))
		}
	} else {
		g *= legacyOut(pc, next, f)
	}
	if w.edgeOut > 0 {
		if k := w.to - 1 - t; k < w.edgeOut {
			g *= float64(k+1) / float64(w.edgeOut+1)
		}
	}
	return g
}

// legacyIn is the picture transition's ramp on a clip's sound (clip frame f).
func legacyIn(c *PlanClip, f int) float64 {
	t := c.TransitionIn.Type
	fadeIn := 0
	switch {
	case t == TransitionCut || t == "":
	case IsDipTransition(t):
		d := max(0, c.TransitionIn.DurationInFrames)
		fadeIn = d - d/2
	default:
		fadeIn = max(0, c.TransitionIn.DurationInFrames)
	}
	if fadeIn <= 0 {
		return 1
	}
	p := math.Min(1, math.Max(0, float64(f)/float64(fadeIn)))
	if IsOverlapTransition(t) {
		return math.Sin(p * math.Pi / 2)
	}
	return p
}

// legacyOut is the clip's fade-out and an overlapping transition into the next
// clip, on the clip's sound (clip frame f).
func legacyOut(c, next *PlanClip, f int) float64 {
	g := 1.0
	if out := max(0, c.FadeOutFrames); out > 0 {
		g *= math.Min(1, math.Max(0, float64(c.DurationInFrames-f)/float64(out)))
	}
	if next != nil && IsOverlapTransition(next.TransitionIn.Type) {
		d := max(0, next.TransitionIn.DurationInFrames)
		start := next.From - c.From
		if d > 0 && f >= start {
			p := math.Min(1, float64(f-start)/float64(d))
			g *= math.Sin((1 - p) * math.Pi / 2)
		}
	}
	return g
}

var (
	reSpeakerLabel = regexp.MustCompile(`^Speaker \d+$`)
	reRawSpeaker   = regexp.MustCompile(`^speaker_\d+$`)
)

// mainSpeaker is the named person who talks most in a clip's heard range ("" when
// nobody named speaks there).
func mainSpeaker(pc PlanClip, w soundWindow, facts Facts) string {
	if facts == nil {
		return ""
	}
	return SpeakerIn(facts.Transcript(pc.AssetID), w.heardIn, w.heardOut)
}

// SpeakerIn is the named person who talks most between from and to (source
// seconds) in a recording's transcript; "" when nobody named speaks there
// (unnamed voices such as "Speaker 2" don't count). Cut sheets use it to say
// whose voice starts early or carries over.
func SpeakerIn(segs []TranscriptSegment, from, to float64) string {
	by := map[string]float64{}
	for _, seg := range segs {
		a, b := math.Max(seg.From, from), math.Min(seg.To, to)
		if b > a && seg.Speaker != "" && !reSpeakerLabel.MatchString(seg.Speaker) && !reRawSpeaker.MatchString(seg.Speaker) {
			by[seg.Speaker] += b - a
		}
	}
	best, bestT := "", 0.0
	for k, v := range by {
		if v > bestT || (v == bestT && k < best) {
			best, bestT = k, v
		}
	}
	return best
}
