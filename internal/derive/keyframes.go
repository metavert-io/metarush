package derive

import (
	"math"
	"sort"
)

// Smart contact sheets (D72): twelve evenly spaced frames miss short shots and
// everything that happens between them. A smart sheet picks its frames by
// priority under a tile cap: one per shot first, then the peaks of visual
// changes, then the moments someone new starts speaking, and finally frames
// that keep any stretch from going unseen for too long.

// Keyframe reasons, highest priority first.
const (
	ReasonShot     = "shot"     // one frame per shot
	ReasonChange   = "change"   // a slow change (pan, zoom, light, someone coming into view)
	ReasonMovement = "movement" // a burst of movement
	ReasonSpeaker  = "speaker"  // someone starts speaking
	ReasonCoverage = "coverage" // so no stretch goes unseen too long
	ReasonEven     = "even"     // evenly spaced (mode even)
)

// KeyframeChange is a visual change to sample (from technical evidence).
type KeyframeChange struct {
	Kind   string // gradual | motion
	At     float64
	Score  float64
	Frames []float64
}

// KeyframeInput is everything the picker considers, in source seconds.
type KeyframeInput struct {
	From, To float64
	Cuts     []float64        // hard cuts (shot boundaries)
	Changes  []KeyframeChange // slow changes and movement
	Turns    []float64        // times someone starts speaking (a new speaker)
	Cap      int              // tile cap (default 20)
}

// Keyframe is one chosen frame.
type Keyframe struct {
	At     float64 `json:"at"`
	Reason string  `json:"reason"`
	Shot   int     `json:"shot" jsonschema:"description=Which shot of the range it belongs to (1-based)."`
}

// DefaultTiles is the smart sheet's usual tile cap.
const DefaultTiles = 20

// CoverageGap is the longest stretch the picker tries to leave unseen for a
// range of span seconds under a tile cap: coverage alone uses at most about
// half the tiles, never sampling more often than every 4 seconds.
func CoverageGap(span float64, capTiles int) float64 {
	if capTiles <= 0 {
		capTiles = DefaultTiles
	}
	return math.Max(4, span/(float64(capTiles)*0.5))
}

type cand struct {
	t      float64
	reason string
	score  float64
}

// PickKeyframes chooses the frames of a smart sheet.
func PickKeyframes(in KeyframeInput) []Keyframe {
	capTiles := in.Cap
	if capTiles <= 0 {
		capTiles = DefaultTiles
	}
	span := in.To - in.From
	if span <= 0 {
		return nil
	}
	if span < 2 && capTiles > 4 {
		capTiles = 4
	}
	end := in.To - 0.05
	clamp := func(t float64) float64 { return math.Max(in.From, math.Min(end, t)) }
	inside := func(t float64) bool { return t >= in.From && t <= end }
	minGap := math.Max(0.6, span/float64(capTiles*3))

	// Shots inside the range.
	var bounds []float64
	for _, c := range in.Cuts {
		if c > in.From+0.05 && c < in.To-0.05 {
			bounds = append(bounds, c)
		}
	}
	sort.Float64s(bounds)
	shotStarts := append([]float64{in.From}, bounds...)
	shotEnds := append(append([]float64{}, bounds...), in.To)
	shotOf := func(t float64) int {
		i := sort.SearchFloat64s(bounds, t+1e-9)
		return i + 1
	}

	levels := make([][]cand, 3)
	for i := range shotStarts {
		a, b := shotStarts[i], shotEnds[i]
		// A third of the way in (past any transition blur), or the middle of a short shot.
		t := a + math.Min(1.2, (b-a)/3)
		if b-a < 1.2 {
			t = (a + b) / 2
		}
		levels[0] = append(levels[0], cand{clamp(t), ReasonShot, b - a})
	}
	for _, c := range in.Changes {
		r := ReasonChange
		if c.Kind == "motion" {
			r = ReasonMovement
		}
		pts := c.Frames
		if r == ReasonMovement {
			pts = []float64{c.At}
		}
		for _, t := range pts {
			if inside(t) {
				levels[1] = append(levels[1], cand{t, r, c.Score})
			}
		}
	}
	for _, t := range in.Turns {
		if inside(t) {
			levels[2] = append(levels[2], cand{t, ReasonSpeaker, 0})
		}
	}
	gap := CoverageGap(span, capTiles)
	// A frame this close to one already chosen adds little: changes need a
	// second, a new speaker a second and a half (the shot's frame shows them).
	spacing := []float64{minGap, math.Max(minGap, 1), math.Max(minGap, 1.5)}

	var chosen []cand
	near := func(t float64) float64 {
		d := math.Inf(1)
		for _, c := range chosen {
			d = math.Min(d, math.Abs(c.t-t))
		}
		return d
	}
	for li, lvl := range levels {
		// Spread each level out: always take the candidate farthest from what is
		// already chosen (ties: the bigger score, then the earlier time).
		left := append([]cand(nil), lvl...)
		for len(chosen) < capTiles && len(left) > 0 {
			best, bestD := -1, -1.0
			for i, c := range left {
				d := near(c.t)
				if d < spacing[li] {
					continue
				}
				if d > bestD+1e-9 || (math.Abs(d-bestD) <= 1e-9 && (c.score > left[best].score || (c.score == left[best].score && c.t < left[best].t))) {
					best, bestD = i, d
				}
			}
			if best < 0 {
				break
			}
			chosen = append(chosen, left[best])
			left = append(left[:best], left[best+1:]...)
		}
	}
	// Coverage: while room is left, the middle of the longest stretch without a
	// frame, until no stretch is longer than the coverage gap.
	for len(chosen) < capTiles {
		sort.Slice(chosen, func(i, j int) bool { return chosen[i].t < chosen[j].t })
		a, b := in.From, end
		if len(chosen) > 0 {
			a, b = in.From, chosen[0].t
		}
		longest := b - a
		for i := 0; i <= len(chosen); i++ {
			lo, hi := in.From, end
			if i > 0 {
				lo = chosen[i-1].t
			}
			if i < len(chosen) {
				hi = chosen[i].t
			}
			if hi-lo > longest {
				a, b, longest = lo, hi, hi-lo
			}
		}
		if longest <= gap || longest < 2*minGap {
			break
		}
		chosen = append(chosen, cand{clamp((a + b) / 2), ReasonCoverage, 0})
	}
	sort.Slice(chosen, func(i, j int) bool { return chosen[i].t < chosen[j].t })
	out := make([]Keyframe, 0, len(chosen))
	for _, c := range chosen {
		out = append(out, Keyframe{At: math.Round(c.t*1000) / 1000, Reason: c.reason, Shot: shotOf(c.t)})
	}
	return out
}

// EvenKeyframes is the classic sheet: n frames evenly spaced in [from, to).
func EvenKeyframes(from, to float64, n int) []Keyframe {
	if n <= 0 {
		n = 12
	}
	span := to - from
	if span < 2 && n > 4 {
		n = 4
	}
	out := make([]Keyframe, n)
	for i := range out {
		out[i] = Keyframe{At: math.Round((from+span*(float64(i)+0.5)/float64(n))*1000) / 1000, Reason: ReasonEven}
	}
	return out
}
