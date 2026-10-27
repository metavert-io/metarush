package editspec

import (
	"math"
	"sort"
)

// Framing that follows movement (D115): where the spec says so
// (output.framing "follow", or a clip's framing.auto "follow"), a cropped
// clip's focus moves with its subject — the main face of each shot, or what
// draws the eye where no face is seen — smoothly, never across the footage's
// own cuts. Go computes the path; the plan carries it as keyframes
// (PlanFraming.Path) that the renderer and the preview follow verbatim, so a
// revision renders the same every time its footage is indexed the same way.

// SubjectSample is where a clip's subject is at one moment (source seconds;
// x and y 0–1 of the picture from the left and top).
type SubjectSample struct {
	At   float64
	X, Y float64
	Face bool
}

// FollowFacts supplies a clip's subject over a source range and the footage's
// own cuts there (the on-screen index). ok is false when nothing is known.
type FollowFacts interface {
	Subject(assetID string, from, to float64) (samples []SubjectSample, cuts []float64, ok bool)
}

// FocusKey is where the crop's focus is at frame F of the clip (output frames
// from the clip's start); between keys the focus moves in a straight line, and
// two keys a frame apart are a jump (at a cut in the footage).
type FocusKey struct {
	F int     `json:"f"`
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// FacesFramed reports a cut whose crops keep faces in frame, standing still
// (faces) or following movement (follow).
func FacesFramed(out Output) bool {
	return out.Framing == FramingFaces || out.Framing == FramingFollow
}

// FollowsMovement reports whether the spec asks this clip's crop to follow its
// subject as it moves: the clip says so (framing.auto follow), or the cut
// does (output.framing follow) and the clip has neither a focus nor an
// automatic framing of its own. A clip shown whole has nothing to follow.
func FollowsMovement(c Clip, out Output) bool {
	if c.Framing != nil && c.Framing.Mode == "fit" {
		return false
	}
	if c.Framing != nil && c.Framing.Auto != "" {
		return c.Framing.Auto == FramingFollow
	}
	ownFocus := c.Framing != nil && (c.Framing.FocusX != nil || c.Framing.FocusY != nil)
	return out.Framing == FramingFollow && !ownFocus
}

// How the virtual camera moves, in shares of the part of the picture that is
// shown (so a tight crop moves less of the picture than a loose one).
const (
	followDeadZone = 0.12 // the subject may wander this far from the middle before the crop moves
	followSettle   = 0.03 // …and the crop moves until the subject is this close again
	followGain     = 2.5  // how fast the crop closes the gap, per second
	followMaxSpeed = 0.6  // the fastest it moves, shown-widths per second
	followSmooth   = 0.8  // seconds: how much the subject's path is smoothed first
	followEase     = 0.25 // seconds: how softly the crop starts and stops
	followStep     = 0.1  // seconds of footage between simulated moments
	followKeyEvery = 0.2  // seconds of footage between keyframes
)

// visibleShare is how much of the source's width and height a fill crop shows.
func visibleShare(srcW, srcH, outW, outH int, zoom float64) (float64, float64) {
	if srcW <= 0 || srcH <= 0 || outW <= 0 || outH <= 0 {
		return 1, 1
	}
	if zoom < 1 {
		zoom = 1
	}
	sc := math.Max(float64(outW)/float64(srcW), float64(outH)/float64(srcH)) * zoom
	return math.Min(1, float64(outW)/(float64(srcW)*sc)), math.Min(1, float64(outH)/(float64(srcH)*sc))
}

// FollowPath is the focus path of a clip that follows movement (FollowsMovement)
// when its crop cuts something off and the index says where its subject is.
// static is the focus to hold where a shot shows nothing to follow. ok is
// false when nothing moves the focus (the plan then keeps one focus point).
func FollowPath(c Clip, a AssetFacts, out Output, durFrames int, static [2]float64, facts Facts) ([]FocusKey, bool) {
	ff, isFollow := facts.(FollowFacts)
	if !isFollow || !FollowsMovement(c, out) || durFrames < 2 || out.FPS <= 0 {
		return nil, false
	}
	zoom := 1.0
	if c.Framing != nil && c.Framing.Zoom > 1 {
		zoom = c.Framing.Zoom
	}
	vw, vh := visibleShare(a.Width, a.Height, out.Width, out.Height, zoom)
	if vw > 0.995 && vh > 0.995 {
		return nil, false // the whole picture shows: nothing to follow
	}
	samples, cuts, ok := ff.Subject(c.AssetID, c.In, c.Out)
	if !ok || len(samples) == 0 {
		return nil, false
	}
	speed := c.Speed
	if speed <= 0 {
		speed = 1
	}
	fps := float64(out.FPS)
	frameOf := func(t float64) int {
		f := int(math.Round((t - c.In) * fps / speed))
		return max(0, min(durFrames-1, f))
	}
	bounds := []float64{c.In}
	for _, x := range cuts {
		if x > c.In+0.05 && x < c.Out-0.05 {
			bounds = append(bounds, x)
		}
	}
	sort.Float64s(bounds[1:])
	bounds = append(bounds, c.Out)
	var keys []FocusKey
	for k := 0; k+1 < len(bounds); k++ {
		s0, s1 := bounds[k], bounds[k+1]
		var xs, ys []float64
		var ts []float64
		for _, p := range samples {
			if p.At >= s0 && p.At < s1 {
				ts, xs, ys = append(ts, p.At), append(xs, p.X), append(ys, p.Y)
			}
		}
		var px, py []float64
		times := stepTimes(s0, s1)
		if len(ts) == 0 {
			for range times {
				px, py = append(px, static[0]), append(py, static[1])
			}
		} else {
			px = followAxis(ts, xs, times, vw, static[0])
			py = followAxis(ts, ys, times, vh, static[1])
		}
		f0, f1 := frameOf(s0), frameOf(s1)-1
		if k+1 == len(bounds)-1 {
			f1 = durFrames - 1
		}
		if f1 < f0 {
			f1 = f0
		}
		last := -1
		for i, t := range times {
			f := frameOf(t)
			if i == len(times)-1 {
				f = f1
			}
			if f < f0 || f > f1 || f == last {
				continue
			}
			keys = append(keys, FocusKey{F: f, X: r4(px[i]), Y: r4(py[i])})
			last = f
		}
	}
	keys = trimKeys(keys)
	moves := false
	for _, k := range keys[1:] {
		if k.X != keys[0].X || k.Y != keys[0].Y {
			moves = true
		}
	}
	if !moves {
		return nil, false
	}
	return keys, true
}

// stepTimes are the simulated moments of a shot [s0, s1): every followStep,
// the keyframes every followKeyEvery among them, ending a hair before s1.
func stepTimes(s0, s1 float64) []float64 {
	var out []float64
	n := int(math.Ceil((s1 - s0) / followKeyEvery))
	for i := 0; i <= n; i++ {
		t := s0 + float64(i)*followKeyEvery
		if t >= s1 {
			t = s1 - 1e-6
		}
		out = append(out, t)
		if t >= s1-1e-6 {
			break
		}
	}
	return out
}

func r4(v float64) float64 { return math.Round(v*10000) / 10000 }

// followAxis is the crop's focus along one axis at the given times: the
// subject's positions (ts, vs) cleaned of single misreadings, smoothed, then
// followed by a camera that holds still while the subject stays near the
// middle, closes the gap at a limited speed when it doesn't, and starts and
// stops softly. vis is the share of the axis the crop shows.
func followAxis(ts, vs, times []float64, vis, static float64) []float64 {
	lo, hi := vis/2, 1-vis/2
	clampV := func(v float64) float64 {
		if vis >= 0.995 {
			return 0.5
		}
		return math.Max(lo, math.Min(hi, v))
	}
	if vis >= 0.995 {
		out := make([]float64, len(times))
		for i := range out {
			out[i] = clampV(static)
		}
		return out
	}
	// A single odd reading between two that agree is a misreading.
	clean := append([]float64(nil), vs...)
	for i := 1; i+1 < len(vs); i++ {
		a, b, c := vs[i-1], vs[i], vs[i+1]
		clean[i] = math.Max(math.Min(a, b), math.Min(math.Max(a, b), c))
	}
	// Smoothed subject at any time (Gaussian weights over the readings).
	subject := func(t float64) float64 {
		var sw, s float64
		for i, ti := range ts {
			d := (t - ti) / followSmooth
			w := math.Exp(-0.5 * d * d)
			sw += w
			s += w * clean[i]
		}
		if sw < 1e-9 {
			return static
		}
		return clampV(s / sw)
	}
	// The camera, simulated at a fine step from the shot's start.
	t0, t1 := times[0], times[len(times)-1]
	nsteps := int(math.Ceil((t1-t0)/followStep)) + 1
	cam := make([]float64, nsteps)
	at := make([]float64, nsteps)
	c := subject(t0)
	moving := false
	dz, settle, vmax := followDeadZone*vis, followSettle*vis, followMaxSpeed*vis
	for i := 0; i < nsteps; i++ {
		t := t0 + float64(i)*followStep
		if t > t1 {
			t = t1
		}
		at[i] = t
		if i > 0 {
			d := subject(t) - c
			if !moving && math.Abs(d) > dz {
				moving = true
			}
			if moving {
				step := d * math.Min(1, followGain*followStep)
				step = math.Max(-vmax*followStep, math.Min(vmax*followStep, step))
				c += step
				if math.Abs(subject(t)-c) < settle {
					moving = false
				}
			}
		}
		cam[i] = c
	}
	// Soft starts and stops: smoothed forward and backward (no lag).
	alpha := 1 - math.Exp(-followStep/followEase)
	for i := 1; i < nsteps; i++ {
		cam[i] = cam[i-1] + alpha*(cam[i]-cam[i-1])
	}
	for i := nsteps - 2; i >= 0; i-- {
		cam[i] = cam[i+1] + alpha*(cam[i]-cam[i+1])
	}
	out := make([]float64, len(times))
	for k, t := range times {
		i := sort.SearchFloat64s(at, t)
		switch {
		case i <= 0:
			out[k] = cam[0]
		case i >= nsteps:
			out[k] = cam[nsteps-1]
		default:
			f := (t - at[i-1]) / math.Max(1e-9, at[i]-at[i-1])
			out[k] = cam[i-1] + f*(cam[i]-cam[i-1])
		}
		out[k] = clampV(out[k])
	}
	return out
}

// trimKeys drops keys in the middle of a stretch where the focus stands
// still (the first and last of the stretch stay, so the motion around it is
// unchanged).
func trimKeys(keys []FocusKey) []FocusKey {
	if len(keys) < 3 {
		return keys
	}
	same := func(a, b FocusKey) bool { return a.X == b.X && a.Y == b.Y }
	out := []FocusKey{keys[0]}
	for i := 1; i+1 < len(keys); i++ {
		if same(keys[i-1], keys[i]) && same(keys[i], keys[i+1]) && keys[i+1].F-keys[i-1].F > 1 {
			continue
		}
		out = append(out, keys[i])
	}
	return append(out, keys[len(keys)-1])
}

// CropRect is where a clip's picture sits in the W×H frame at clip frame f:
// its left and top edges and its size, in output pixels (the picture is
// larger than the frame where it is cropped). It mirrors framingStyle in
// video/src/clips/framing.ts exactly, so quality checks see the crop the
// render drew.
func CropRect(fr PlanFraming, sw, sh, W, H float64, f float64) (left, top, w, h float64) {
	fx, fy := fr.FocusX, fr.FocusY
	if len(fr.Path) > 0 {
		fx, fy = FocusAt(fr.Path, f)
	}
	clamp01 := func(v float64) float64 {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0.5
		}
		return math.Max(0, math.Min(1, v))
	}
	fx, fy = clamp01(fx), clamp01(fy)
	zoom := math.Max(1, fr.Zoom)
	if sw <= 0 || sh <= 0 {
		return 0, 0, W, H
	}
	base := math.Max(W/sw, H/sh)
	if fr.Mode == "fit" {
		base = math.Min(W/sw, H/sh)
	}
	sc := base * zoom
	w, h = sw*sc, sh*sc
	place := func(frame, size, focus float64) float64 {
		if size <= frame+0.5 {
			return (frame - size) / 2
		}
		ideal := frame/2 - focus*size
		return math.Max(frame-size, math.Min(0, ideal))
	}
	return place(W, w, fx), place(H, h, fy), w, h
}

// FocusAt is the focus at clip frame f along a path (the plan's semantics:
// straight lines between keys, held before the first and after the last).
func FocusAt(path []FocusKey, f float64) (float64, float64) {
	if len(path) == 0 {
		return 0.5, 0.5
	}
	if f <= float64(path[0].F) {
		return path[0].X, path[0].Y
	}
	for i := 1; i < len(path); i++ {
		if f <= float64(path[i].F) {
			a, b := path[i-1], path[i]
			u := (f - float64(a.F)) / math.Max(1, float64(b.F-a.F))
			return a.X + u*(b.X-a.X), a.Y + u*(b.Y-a.Y)
		}
	}
	last := path[len(path)-1]
	return last.X, last.Y
}
