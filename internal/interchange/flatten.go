package interchange

import (
	"container/heap"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
)

// Flatten turns a timeline another editing app wrote into what a MetaRush cut
// plays (D304): one picture — at every moment the topmost clip that shows —
// as shots in order, with the transitions between them; each shot's own
// sound where it plays with the picture (split edits up to 3 s); every other
// sound as an added sound; chapter markers as chapters and other markers as
// notes. Stretches with no picture are closed up. Everything that can't be
// carried over is listed in plain words, never dropped silently.

// Shot is one stretch of the new cut's picture.
type Shot struct {
	Name   string  `json:"name"`
	Source string  `json:"source"`
	At     float64 `json:"at"`     // seconds on the new cut (MetaRush's own timing, transitions included)
	Length float64 `json:"length"` // seconds it shows on the old timeline, edit point to edit point
	In     float64 `json:"in"`     // seconds into the source at its first edit point
	Speed  float64 `json:"speed"`
	// Enter is how it enters from the shot before (Kind "" is a cut).
	Enter Join `json:"enter"`
	// HandleBefore and HandleAfter are seconds of the source used beyond the
	// edit points under overlapping transitions.
	HandleBefore float64 `json:"handleBefore,omitempty"`
	HandleAfter  float64 `json:"handleAfter,omitempty"`
	FadeOut      float64 `json:"fadeOut,omitempty"`
	// OwnSound: it plays its own sound; Lead/Carry are seconds its sound
	// starts before it, or the previous shot's carries over into it.
	OwnSound bool     `json:"ownSound"`
	Level    *float64 `json:"level,omitempty"` // its sound's level (dB)
	Lead     float64  `json:"lead,omitempty"`
	Carry    float64  `json:"carry,omitempty"`
	Notes    []string `json:"notes,omitempty"`
	Group    string   `json:"-"`
	oldAt    float64
}

// Sound is a sound added on top of the shots (music, narration, effects, or
// dialogue that plays under other pictures).
type Sound struct {
	Name    string   `json:"name"`
	Source  string   `json:"source"`
	At      float64  `json:"at"`
	Length  float64  `json:"length"`
	In      float64  `json:"in"`
	Speed   float64  `json:"speed"`
	Role    string   `json:"role"` // dialogue | music | effects | narration
	Level   *float64 `json:"level,omitempty"`
	FadeIn  float64  `json:"fadeIn,omitempty"`
	FadeOut float64  `json:"fadeOut,omitempty"`
	lane    *Lane
}

// FlatChapter is where a part of the video starts.
type FlatChapter struct {
	At    float64 `json:"at"`
	Title string  `json:"title"`
}

// FlatNote is a marker that becomes a note on the shot it falls in.
type FlatNote struct {
	At   float64 `json:"at"`
	Shot int     `json:"shot"`
	Text string  `json:"text"`
}

// Flat is a timeline ready to become a cut.
type Flat struct {
	Name     string        `json:"name"`
	Format   string        `json:"format"`
	FPS      float64       `json:"fps"`
	Width    int           `json:"width,omitempty"`
	Height   int           `json:"height,omitempty"`
	Length   float64       `json:"length"` // seconds of the new cut
	Shots    []Shot        `json:"shots"`
	Sounds   []Sound       `json:"sounds"`
	Chapters []FlatChapter `json:"chapters"`
	Notes    []FlatNote    `json:"notes"`
	// Lost says, in plain words, what doesn't carry over.
	Lost []string `json:"lost"`
	// Closed are the stretches with no picture that were closed up (old timeline seconds).
	Closed [][2]float64 `json:"closed,omitempty"`
	// MetaRush: the timeline is one of MetaRush's own exports.
	MetaRush bool `json:"metarush,omitempty"`
}

const (
	maxSplit = 3.0 // seconds a split edit may lead or carry (editspec's limit)
	// maxChapters is how many chapters a version's delivery notes take.
	maxChapters = 100
	// maxNotes is how many markers become notes.
	maxNotes = 500
	// maxLost is how many things not carried over are listed one by one.
	maxLost = 200
	eps     = 1e-4
)

type picSeg struct {
	p      *Piece
	lane   int
	a, b   float64
	joinIn *Join
}

// Flatten makes a timeline into shots and sounds.
func Flatten(r *Read) *Flat {
	f := &Flat{Name: r.Name, Format: r.FormatName, FPS: r.FPS, Width: r.Width, Height: r.Height, Shots: []Shot{}, Sounds: []Sound{}, Chapters: []FlatChapter{}, Notes: []FlatNote{}, Lost: []string{}}
	f.MetaRush = r.MetaRush != nil
	for _, s := range r.Sources {
		if s.AssetID != "" {
			f.MetaRush = true
		}
	}
	var lostSoFar lostList
	lost := lostSoFar.add
	defer func() {
		// What the file has that wasn't read, last (also when there is no picture).
		for _, u := range r.Unread {
			lost(u + " isn't carried over")
		}
		if r.UnreadMore > 0 {
			lost(fmt.Sprintf("%d more things in the timeline like these aren't carried over", r.UnreadMore))
		}
		f.Lost = lostSoFar.done()
	}()
	frame := 1 / math.Max(r.FPS, 1)
	clock := func(t float64) string { return clockSec(t) }

	// 1. The picture: the topmost clip at every moment.
	type cand struct {
		p    *Piece
		lane int
	}
	var cands []cand
	bounds := []float64{}
	var off []string
	for li, l := range r.Video {
		for _, p := range l.Pieces {
			if p.Length <= eps {
				continue
			}
			if l.Off || p.Off {
				off = append(off, p.Name)
				continue
			}
			switch p.Kind {
			case PieceClip:
				if s := r.Source(p.Source); s != nil && !s.HasVideo && s.HasAudio {
					continue
				}
				cands = append(cands, cand{p, li})
				bounds = append(bounds, p.At, p.End())
			case PieceTitle:
				what := "the title"
				if p.Text != "" {
					what = fmt.Sprintf("the title “%s”", clip(p.Text, 60))
				}
				lost(fmt.Sprintf("%s at %s (%s) isn't carried over: add it as a graphic", what, clock(p.At), secondsWords(p.Length)))
			case PieceGenerator:
				lost(fmt.Sprintf("“%s” at %s (a picture the editing app draws, %s) isn't carried over", p.Name, clock(p.At), secondsWords(p.Length)))
			}
		}
	}
	if len(off) > 0 {
		lost(fmt.Sprintf("%s switched off in the editing app, so %s left out", listWords(off, 4), pluralVerb(len(off), "is", "are")))
	}
	sort.Float64s(bounds)
	bounds = uniq(bounds)
	var segs []picSeg
	// Sweep the stretches between bounds in time order, keeping the clips that
	// have started on a heap with the topmost (then the first listed) on top.
	byStart := make([]int, len(cands))
	for k := range byStart {
		byStart[k] = k
	}
	sort.SliceStable(byStart, func(i, j int) bool { return cands[byStart[i]].p.At < cands[byStart[j]].p.At })
	on := &topmost{less: func(x, y int) bool {
		if cands[x].lane != cands[y].lane {
			return cands[x].lane > cands[y].lane
		}
		return x < y
	}}
	next := 0
	for i := 0; i+1 < len(bounds); i++ {
		a, b := bounds[i], bounds[i+1]
		for next < len(byStart) && cands[byStart[next]].p.At <= a+eps {
			heap.Push(on, byStart[next])
			next++
		}
		if b-a <= eps {
			continue
		}
		for on.Len() > 0 && cands[on.ks[0]].p.End() < b-eps {
			heap.Pop(on) // it has ended (stretches only move on)
		}
		if on.Len() == 0 {
			continue
		}
		c := cands[on.ks[0]]
		if n := len(segs); n > 0 && segs[n-1].p == c.p && near(segs[n-1].b, a) {
			segs[n-1].b = b
			continue
		}
		segs = append(segs, picSeg{p: c.p, lane: c.lane, a: a, b: b})
	}
	if len(segs) == 0 {
		lost("there is no footage in the picture to make a cut from")
		return f
	}

	// 2. Close up stretches with no picture.
	var gaps [][2]float64
	if segs[0].a > eps {
		gaps = append(gaps, [2]float64{0, segs[0].a})
	}
	for i := 1; i < len(segs); i++ {
		if segs[i].a-segs[i-1].b > eps {
			gaps = append(gaps, [2]float64{segs[i-1].b, segs[i].a})
		}
	}
	f.Closed = gaps
	shift := closing(gaps)
	for i, g := range gaps {
		if i == 0 && g[0] < eps {
			lost(fmt.Sprintf("the %s before the first picture %s left out: the cut starts with the first picture", secondsWords(g[1]-g[0]), pluralVerb(1, "is", "are")))
			continue
		}
		if i >= 6 {
			lost(fmt.Sprintf("%d more stretches with no picture were closed up", len(gaps)-i))
			break
		}
		lost(fmt.Sprintf("the %s with no picture at %s was closed up", secondsWords(g[1]-g[0]), clock(g[0])))
	}

	// 3. Transitions: a join on a segment's lane at its edge.
	joinAt := func(lane int, t float64) *Join {
		if lane < 0 || lane >= len(r.Video) {
			return nil
		}
		// Joins are in time order (finish sorts them): the first within half a frame.
		js := r.Video[lane].Joins
		for i := sort.Search(len(js), func(i int) bool { return js[i].At >= t-frame/2 }); i < len(js) && js[i].At <= t+frame/2; i++ {
			if math.Abs(js[i].At-t) <= frame/2 {
				return &js[i]
			}
		}
		return nil
	}
	for i := range segs {
		s := &segs[i]
		sh := Shot{Name: s.p.Name, Source: s.p.Source, Length: s.b - s.a, In: s.p.In + (s.a-s.p.At)*s.p.Speed, Speed: s.p.Speed, Group: s.p.Group, oldAt: s.a}
		if sh.Speed == 0 {
			sh.Speed = 1
		}
		prevJoined := i > 0 && near(segs[i-1].b, s.a)
		j := joinAt(s.lane, s.a)
		switch {
		case j != nil && (j.Kind == TransFadeIn || (!prevJoined && isPictureJoin(j.Kind))):
			if prevJoined {
				// A clip fading in over the picture below dissolves from it.
				sh.Enter = Join{Kind: TransDissolve, At: s.a, After: j.After + j.Before, Name: j.Name}
			} else {
				sh.Enter = Join{Kind: TransFadeIn, At: s.a, After: j.After + j.Before, Name: j.Name}
			}
		case j != nil && prevJoined && segs[i-1].lane == s.lane && isPictureJoin(j.Kind):
			sh.Enter = *j
		case prevJoined && segs[i-1].lane > s.lane:
			// The clip above ends: does it fade out over this one?
			if pj := joinAt(segs[i-1].lane, s.a); pj != nil && pj.Kind == TransFadeOut {
				sh.Enter = Join{Kind: TransDissolve, At: s.a, Before: pj.Before + pj.After, Name: pj.Name}
			}
		}
		if sh.Enter.Kind == "" && s.p.PictureFadeIn > 0 && near(s.a, s.p.At) {
			sh.Enter = Join{Kind: TransFadeIn, At: s.a, After: s.p.PictureFadeIn}
			if i > 0 && prevJoined && segs[i-1].p.PictureFadeOut > 0 && near(segs[i-1].b, segs[i-1].p.End()) {
				sh.Enter = Join{Kind: TransDipBlack, At: s.a, Before: segs[i-1].p.PictureFadeOut, After: s.p.PictureFadeIn, Name: "Fade through black"}
			}
		}
		if s.p.PictureFadeOut > 0 && near(s.b, s.p.End()) && !(i+1 < len(segs) && near(segs[i+1].a, s.b) && segs[i+1].p.PictureFadeIn > 0) {
			sh.FadeOut = s.p.PictureFadeOut
		}
		if j := joinAt(s.lane, s.b); j != nil && j.Kind == TransFadeOut && (i+1 == len(segs) || !near(segs[i+1].a, s.b)) {
			sh.FadeOut = j.Before + j.After
		}
		if !prevJoined && sh.Enter.Kind != TransFadeIn {
			sh.Enter = Join{}
		}
		f.Shots = append(f.Shots, sh)
	}
	// Overlapping transitions use the sources beyond the edit points.
	for i := range f.Shots {
		sh := &f.Shots[i]
		e := sh.Enter
		switch e.Kind {
		case "", TransFadeIn, TransFadeOut, TransDipBlack, TransDipWhite:
			continue
		case TransAudioCrossfade:
			sh.Enter = Join{}
			continue
		}
		if i == 0 {
			sh.Enter = Join{Kind: TransFadeIn, After: e.Before + e.After}
			continue
		}
		prev := &f.Shots[i-1]
		before, after := e.Before, e.After
		if s := r.Source(sh.Source); s != nil && !s.TimecodeOnly {
			before = math.Min(before, math.Max(0, sh.In/sh.Speed))
		}
		if s := r.Source(prev.Source); s != nil && s.Duration > 0 && !s.TimecodeOnly {
			end := prev.In + prev.Length*prev.Speed
			after = math.Min(after, math.Max(0, (s.Duration-end)/prev.Speed))
		}
		if before+after < e.Before+e.After-frame/2 {
			if before+after < frame {
				lost(fmt.Sprintf("the %s at %s became a cut: the footage has no frames beyond the cut for it", joinWords(e), clock(e.At)))
				sh.Enter = Join{}
				continue
			}
			lost(fmt.Sprintf("the %s at %s is shorter (%s): the footage has fewer frames beyond the cut", joinWords(e), clock(e.At), secondsWords(before+after)))
		}
		if e.Kind == JoinOther {
			lost(fmt.Sprintf("the “%s” transition at %s is a dissolve of the same length", firstNonEmptyStr(e.Name, "unnamed"), clock(e.At)))
			sh.Enter.Kind = TransDissolve
		}
		sh.Enter.Before, sh.Enter.After = before, after
		sh.HandleBefore = before
		prev.HandleAfter = after
	}
	// Positions on the new cut: MetaRush plays an overlapping transition by
	// starting the next shot early, so each shot starts where it starts on the
	// old timeline (gaps closed) less the handle it shows first.
	for i := range f.Shots {
		sh := &f.Shots[i]
		sh.At = shift(sh.oldAt) - sh.HandleBefore
	}
	if n := len(f.Shots); n > 0 {
		last := f.Shots[n-1]
		f.Length = shift(last.oldAt) + last.Length
	}

	// 4. Sound.
	type snd struct {
		p    *Piece
		lane *Lane
	}
	var sounds []snd
	seen := map[string]bool{}
	for _, l := range cloneLanes(r.Audio) {
		// A crossfade on a sound lane plays both sounds through it, using the
		// sound beyond each edit point: widen the pieces to what is heard.
		// (Each piece against its place before any widening.)
		starts, ends := byTime(l.Pieces, (*Piece).startAt), byTime(l.Pieces, (*Piece).End)
		grow := make([]float64, len(l.Pieces))
		pull := make([]float64, len(l.Pieces))
		for _, j := range l.Joins {
			if j.Kind == TransFadeIn || j.Kind == TransFadeOut || j.Before+j.After <= eps {
				continue
			}
			ending := ends.near(j.At)
			for _, k := range ending {
				grow[k] += j.After
			}
			for _, k := range starts.near(j.At) {
				if !slices.Contains(ending, k) {
					pull[k] += j.Before
				}
			}
		}
		for k, p := range l.Pieces {
			p.At -= pull[k]
			p.In -= pull[k] * p.Speed
			p.Length += pull[k] + grow[k]
		}
		for _, p := range l.Pieces {
			if p.Kind != PieceClip || p.Length <= eps {
				continue
			}
			if l.Off || p.Off {
				continue
			}
			key := fmt.Sprintf("%s|%.4f|%.4f|%.4f", p.Source, p.At, p.Length, p.In)
			if seen[key] {
				continue // the other side of a stereo pair
			}
			seen[key] = true
			sounds = append(sounds, snd{p, l})
		}
	}
	aligned := func(p *Piece, sh *Shot) bool {
		if p.Source != sh.Source || math.Abs(p.Speed-sh.Speed) > 1e-3 {
			return false
		}
		// The same source time at the same timeline time (within a frame and a half).
		return math.Abs(p.In-(sh.In-(sh.oldAt-p.At)*sh.Speed)) <= 1.5*frame*math.Max(1, sh.Speed)
	}
	linked := func(p *Piece, sh *Shot) bool {
		if p.End() <= sh.oldAt+eps || p.At >= sh.oldAt+sh.Length-eps {
			return false
		}
		if p.Group != "" && sh.Group != "" && p.Group != sh.Group {
			return false
		}
		return aligned(p, sh)
	}
	// Each shot's own sound: a linked sound that starts and ends within 3
	// seconds of its picture (a split edit) and no further than half the shot.
	var unsaid []string // sounds the timeline doesn't say are music, narration or effects
	used := make([]bool, len(sounds))
	dIn := make([]float64, len(f.Shots))  // seconds its sound starts before its picture (negative: after)
	dOut := make([]float64, len(f.Shots)) // seconds its sound ends after its picture
	// The sounds of each source in time order, to look only near each shot.
	bySource := map[string][]int{}
	for k, s := range sounds {
		bySource[s.p.Source] = append(bySource[s.p.Source], k)
	}
	for _, ks := range bySource {
		sort.SliceStable(ks, func(i, j int) bool { return sounds[ks[i]].p.At < sounds[ks[j]].p.At })
	}
	for i := range f.Shots {
		sh := &f.Shots[i]
		// A linked sound starts within maxSplit before the shot's window and
		// half the shot after it; the first listed of those that fit wins.
		ks := bySource[sh.Source]
		from := (sh.oldAt - sh.HandleBefore) - maxSplit - 2*eps
		to := (sh.oldAt - sh.HandleBefore) + math.Min(maxSplit, sh.Length/2) + 2*eps
		var nearby []int
		for x := sort.Search(len(ks), func(x int) bool { return sounds[ks[x]].p.At >= from }); x < len(ks) && sounds[ks[x]].p.At <= to; x++ {
			nearby = append(nearby, ks[x])
		}
		sort.Ints(nearby)
		for _, k := range nearby {
			s := sounds[k]
			if used[k] || !linked(s.p, sh) {
				continue
			}
			// Against MetaRush's own window for the shot: under an
			// overlapping transition both sounds play, as both pictures do.
			in := (sh.oldAt - sh.HandleBefore) - s.p.At
			out := s.p.End() - (sh.oldAt + sh.Length + sh.HandleAfter)
			limit := math.Min(maxSplit, sh.Length/2)
			if in > maxSplit+eps || out > maxSplit+eps || in < -limit-eps || out < -limit-eps {
				continue
			}
			// Leading into the shot before, or carrying into the next, needs that shot right there.
			if in > frame && (i == 0 || !near(f.Shots[i-1].oldAt+f.Shots[i-1].Length, sh.oldAt) || in > f.Shots[i-1].Length/2+eps) {
				continue
			}
			if out > frame && (i+1 >= len(f.Shots) || !near(f.Shots[i+1].oldAt, sh.oldAt+sh.Length) || out > f.Shots[i+1].Length/2+eps) {
				continue
			}
			used[k] = true
			sh.OwnSound = true
			dIn[i], dOut[i] = in, out
			if s.p.Level != nil && !f.MetaRush {
				lv := *s.p.Level
				sh.Level = &lv
			}
			if spread(s.p) > 1 && !f.MetaRush {
				lost(fmt.Sprintf("the level changes inside the sound of “%s” at %s aren't carried over: it plays at one level", sh.Name, clock(sh.oldAt)))
			}
			if (s.p.FadeIn > 0.5 || s.p.FadeOut > 0.5) && !f.MetaRush {
				lost(fmt.Sprintf("the fade on the sound of “%s” at %s isn't carried over", sh.Name, clock(sh.oldAt)))
			}
			break
		}
	}
	// Split edits at each cut: the next sound starting early (lead) or the
	// last one carrying on (carry); a cut does one or the other.
	for i := 1; i < len(f.Shots); i++ {
		sh, prev := &f.Shots[i], &f.Shots[i-1]
		if !near(prev.oldAt+prev.Length, sh.oldAt) {
			continue
		}
		// MetaRush plays a short blend after a split edit's change; its own
		// exports include it in the outgoing sound.
		blend := 0.0
		if f.MetaRush {
			blend = 0.1
		}
		lead, carry := 0.0, 0.0
		if sh.OwnSound && dIn[i] > frame {
			lead = dIn[i]
		}
		if prev.OwnSound && dOut[i-1] > frame {
			switch {
			case sh.OwnSound && dIn[i] < -frame:
				carry = -dIn[i] // the next sound starts where the last one hands over
			case lead > 0 && dOut[i-1] <= blend+2*frame:
				// The blend after a lead.
			default:
				carry = dOut[i-1] - blend
			}
			if carry <= frame {
				carry = 0
			}
		}
		switch {
		case lead > 0 && carry > 0:
			lost(fmt.Sprintf("at %s both sounds play across the cut: only the longer split edit is kept", clock(sh.oldAt)))
			if lead >= carry {
				sh.Lead = lead
			} else {
				sh.Carry = carry
			}
		case lead > 0:
			sh.Lead = lead
		case carry > 0:
			sh.Carry = carry
		}
	}
	for k, s := range sounds {
		if used[k] {
			continue
		}
		p := s.p
		role := firstNonEmptyStr(p.Role, s.lane.Role, soundRole(s.lane.Name))
		switch role {
		case RoleClipSound, RoleDialogue, "":
			said := role == RoleDialogue
			role = RoleDialogue
			if src := r.Source(p.Source); src != nil && !src.HasVideo || r.Format != FormatOTIO {
				if guess := soundRole(p.Name); guess != "" {
					role = guess
				} else if !said && (src == nil || !src.HasVideo) {
					unsaid = append(unsaid, p.Name)
				}
			}
		}
		// Keep the parts that play over picture (gaps are closed up).
		parts := [][2]float64{{p.At, p.End()}}
		for _, g := range gapsWithin(gaps, p.At, p.End()) {
			var next [][2]float64
			for _, x := range parts {
				if g[1] <= x[0] || g[0] >= x[1] {
					next = append(next, x)
					continue
				}
				if g[0] > x[0] {
					next = append(next, [2]float64{x[0], g[0]})
				}
				if g[1] < x[1] {
					next = append(next, [2]float64{g[1], x[1]})
				}
			}
			parts = next
		}
		if len(parts) == 0 {
			lost(fmt.Sprintf("the sound “%s” at %s plays only where there is no picture, so it was left out", p.Name, clock(p.At)))
			continue
		}
		if len(parts) > 1 || !near(parts[0][0], p.At) || !near(parts[0][1], p.End()) {
			lost(fmt.Sprintf("the sound “%s” at %s is cut where the stretches with no picture were closed up", p.Name, clock(p.At)))
		}
		for _, x := range parts {
			if x[1]-x[0] < frame {
				continue
			}
			so := Sound{Name: p.Name, Source: p.Source, At: shift(x[0]), Length: x[1] - x[0], In: p.In + (x[0]-p.At)*p.Speed, Speed: p.Speed, Role: role, lane: s.lane}
			if p.Level != nil {
				lv := *p.Level
				so.Level = &lv
			}
			if near(x[0], p.At) {
				so.FadeIn = p.FadeIn
			}
			if near(x[1], p.End()) {
				so.FadeOut = p.FadeOut
			}
			f.Sounds = append(f.Sounds, so)
		}
		if spread(p) > 1 {
			lost(fmt.Sprintf("the level changes inside “%s” at %s aren't carried over: it plays at one level", p.Name, clock(p.At)))
		}
	}
	if len(unsaid) > 0 {
		lost(fmt.Sprintf("the timeline doesn't say whether %s %s music, narration or effects: %s added as dialogue (change %s on the Sound tab if not)",
			listWords(dedupe(unsaid), 3), pluralVerb(len(dedupe(unsaid)), "is", "are"), pluralVerb(len(dedupe(unsaid)), "it is", "they are"), pluralVerb(len(dedupe(unsaid)), "it", "them")))
	}
	// Fades on sound lanes (transitions from and to silence).
	type laneKey struct {
		name string
		z    float64
	}
	soundsOn := map[laneKey][]int{}
	for i, so := range f.Sounds {
		if so.lane != nil {
			k := laneKey{so.lane.Name, so.lane.z}
			soundsOn[k] = append(soundsOn[k], i)
		}
	}
	for _, l := range r.Audio {
		on := soundsOn[laneKey{l.Name, l.z}] // only the fades on its own lane
		if len(on) == 0 || len(l.Joins) == 0 {
			continue
		}
		startsAt := make([]int, len(on))
		copy(startsAt, on)
		sort.SliceStable(startsAt, func(a, b int) bool { return f.Sounds[startsAt[a]].At < f.Sounds[startsAt[b]].At })
		endsAt := make([]int, len(on))
		copy(endsAt, on)
		end := func(i int) float64 { return f.Sounds[i].At + f.Sounds[i].Length }
		sort.SliceStable(endsAt, func(a, b int) bool { return end(endsAt[a]) < end(endsAt[b]) })
		for _, j := range l.Joins {
			t := shift(j.At)
			switch j.Kind {
			case TransFadeIn:
				for x := sort.Search(len(startsAt), func(x int) bool { return f.Sounds[startsAt[x]].At >= t-frame }); x < len(startsAt) && f.Sounds[startsAt[x]].At <= t+frame; x++ {
					if so := &f.Sounds[startsAt[x]]; so.FadeIn == 0 {
						so.FadeIn = j.Before + j.After
					}
				}
			case TransFadeOut:
				for x := sort.Search(len(endsAt), func(x int) bool { return end(endsAt[x]) >= t-frame }); x < len(endsAt) && end(endsAt[x]) <= t+frame; x++ {
					if so := &f.Sounds[endsAt[x]]; so.FadeOut == 0 {
						so.FadeOut = j.Before + j.After
					}
				}
			}
		}
	}

	// 5. Markers.
	captions, graphics := 0, 0
	for _, m := range r.Markers {
		if m.At < -eps || m.At > r.Length+eps {
			continue
		}
		switch m.Kind {
		case "caption":
			captions++
			continue
		case "graphic":
			graphics++
			lost(fmt.Sprintf("%s at %s isn't carried over: add it again", strings.TrimPrefix(m.Name, "Graphic: "), clock(m.At)))
			continue
		case "narration":
			lost(fmt.Sprintf("%s (at %s) isn't carried over", m.Name, clock(m.At)))
			continue
		}
		at := shift(m.At)
		if m.Chapter {
			title := strings.TrimSpace(m.Name)
			if isChapterName(title) {
				if _, rest, ok := strings.Cut(title, ":"); ok && strings.TrimSpace(rest) != "" {
					title = strings.TrimSpace(rest)
				}
			}
			if len(f.Chapters) >= maxChapters {
				lost(fmt.Sprintf("a version has at most %d chapters: the chapter “%s” at %s and those after it aren't carried over", maxChapters, clip(title, 60), clock(m.At)))
				continue
			}
			f.Chapters = append(f.Chapters, FlatChapter{At: roundTo(at, 0.001), Title: clip(firstNonEmptyStr(title, "Chapter"), 120)})
			continue
		}
		if len(f.Notes) >= maxNotes {
			lost(fmt.Sprintf("only the first %d markers become notes: the rest aren't carried over", maxNotes))
			continue
		}
		text := strings.TrimSpace(m.Name)
		if m.Note != "" {
			if text != "" {
				text += ": "
			}
			text += strings.TrimSpace(m.Note)
		}
		if text == "" {
			text = "(a marker)"
		}
		// The last shot starting at or before it (shots are in time order).
		shot := max(0, sort.Search(len(f.Shots), func(i int) bool { return m.At < f.Shots[i].oldAt-eps })-1)
		f.Notes = append(f.Notes, FlatNote{At: roundTo(at, 0.001), Shot: shot, Text: clip(text, 300)})
	}
	if captions > 0 {
		lost("the captions were markers in the timeline: turn captions on for the cut to have them again")
	}
	_ = graphics
	for i := range f.Shots {
		sh := &f.Shots[i]
		if sh.Speed < 0.25 || sh.Speed > 4 {
			lost(fmt.Sprintf("“%s” at %s plays at %.0f%% speed; MetaRush plays 25%% to 400%%, so it was set to the nearest", sh.Name, clock(sh.oldAt), sh.Speed*100))
		}
	}
	return f
}

// cloneLanes copies lanes and their pieces (Flatten never changes the Read).
func cloneLanes(ls []*Lane) []*Lane {
	out := make([]*Lane, 0, len(ls))
	for _, l := range ls {
		c := *l
		c.Pieces = make([]*Piece, len(l.Pieces))
		for i, p := range l.Pieces {
			q := *p
			c.Pieces[i] = &q
		}
		out = append(out, &c)
	}
	return out
}

func isPictureJoin(kind string) bool {
	switch kind {
	case "", TransAudioCrossfade, TransFadeOut:
		return false
	}
	return true
}

func joinWords(j Join) string {
	switch j.Kind {
	case TransDipBlack:
		return "fade through black"
	case TransDipWhite:
		return "fade through white"
	case TransWipe, TransSlide, TransPush, TransZoom, TransWhip:
		return j.Kind
	case JoinOther:
		return "“" + firstNonEmptyStr(j.Name, "unnamed") + "” transition"
	}
	return "dissolve"
}

// spread is how far a piece's level keys stray from each other (dB).
func spread(p *Piece) float64 {
	if len(p.LevelKeys) < 2 {
		return 0
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, k := range p.LevelKeys {
		if k.At < 0 || k.At > p.Length {
			continue
		}
		lo, hi = math.Min(lo, k.DB), math.Max(hi, k.DB)
	}
	if math.IsInf(lo, 0) {
		return 0
	}
	return hi - lo
}

func uniq(v []float64) []float64 {
	out := v[:0]
	for _, x := range v {
		if len(out) == 0 || x-out[len(out)-1] > eps {
			out = append(out, x)
		}
	}
	return out
}

// clockSec prints seconds as m:ss (h:mm:ss past an hour).
func clockSec(t float64) string {
	if t < 0 {
		t = 0
	}
	s := int(math.Floor(t + 1e-6))
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func secondsWords(d float64) string {
	switch {
	case d < 0.95:
		return fmt.Sprintf("%.1f seconds", d)
	case d < 59.5:
		n := math.Round(d*10) / 10
		if n == math.Trunc(n) {
			if n == 1 {
				return "1 second"
			}
			return fmt.Sprintf("%.0f seconds", n)
		}
		return fmt.Sprintf("%.1f seconds", n)
	}
	return clockSec(d)
}

func listWords(names []string, n int) string {
	var q []string
	for i, x := range names {
		if i == n {
			q = append(q, fmt.Sprintf("%d more", len(names)-n))
			break
		}
		q = append(q, "“"+x+"”")
	}
	switch len(q) {
	case 1:
		return q[0]
	case 2:
		return q[0] + " and " + q[1]
	}
	return strings.Join(q[:len(q)-1], ", ") + " and " + q[len(q)-1]
}

func pluralVerb(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// lostList collects what doesn't carry over: each thing once, at most
// maxLost one by one, then how many more.
type lostList struct {
	list []string
	seen map[string]bool
	more int
}

func (l *lostList) add(msg string) {
	if l.seen == nil {
		l.seen = map[string]bool{}
	}
	if l.seen[msg] {
		return
	}
	if len(l.list) >= maxLost {
		if len(l.seen) < maxLost*50 {
			l.seen[msg] = true
		}
		l.more++
		return
	}
	l.seen[msg] = true
	l.list = append(l.list, msg)
}

func (l *lostList) done() []string {
	out := append([]string{}, l.list...)
	if l.more > 0 {
		out = append(out, fmt.Sprintf("%d more things like these", l.more))
	}
	return out
}

// topmost is a heap of candidate indices, the one to show on top.
type topmost struct {
	ks   []int
	less func(x, y int) bool
}

func (h *topmost) Len() int           { return len(h.ks) }
func (h *topmost) Less(i, j int) bool { return h.less(h.ks[i], h.ks[j]) }
func (h *topmost) Swap(i, j int)      { h.ks[i], h.ks[j] = h.ks[j], h.ks[i] }
func (h *topmost) Push(x any)         { h.ks = append(h.ks, x.(int)) }
func (h *topmost) Pop() any {
	n := len(h.ks)
	x := h.ks[n-1]
	h.ks = h.ks[:n-1]
	return x
}

// closing is a timeline time with closed stretches taken out: t less the
// stretches before it (and the part of one it is inside). The stretches are
// in time order and don't overlap.
func closing(gaps [][2]float64) func(float64) float64 {
	ends := make([]float64, len(gaps))
	sum := make([]float64, len(gaps)+1)
	for i, g := range gaps {
		ends[i] = g[1]
		sum[i+1] = sum[i] + g[1] - g[0]
	}
	return func(t float64) float64 {
		i := sort.Search(len(ends), func(i int) bool { return ends[i] > t })
		d := sum[i]
		if i < len(gaps) && t > gaps[i][0] {
			d += t - gaps[i][0]
		}
		return t - d
	}
}

// gapsWithin are the stretches (in time order, not overlapping) that touch a..b.
func gapsWithin(gaps [][2]float64, a, b float64) [][2]float64 {
	i := sort.Search(len(gaps), func(i int) bool { return gaps[i][1] > a })
	j := i
	for j < len(gaps) && gaps[j][0] < b {
		j++
	}
	return gaps[i:j]
}

func (p *Piece) startAt() float64 { return p.At }

// timeIndex is pieces' indices in order of one of their times.
type timeIndex struct {
	ks []int
	t  []float64
}

func byTime(ps []*Piece, at func(*Piece) float64) timeIndex {
	x := timeIndex{ks: make([]int, len(ps)), t: make([]float64, len(ps))}
	for k := range ps {
		x.ks[k] = k
	}
	sort.SliceStable(x.ks, func(i, j int) bool { return at(ps[x.ks[i]]) < at(ps[x.ks[j]]) })
	for i, k := range x.ks {
		x.t[i] = at(ps[k])
	}
	return x
}

// near is the pieces whose time is within a microsecond of t.
func (x timeIndex) near(t float64) []int {
	var out []int
	for i := sort.SearchFloat64s(x.t, t-1e-6); i < len(x.t) && x.t[i] < t+1e-6; i++ {
		out = append(out, x.ks[i])
	}
	return out
}
