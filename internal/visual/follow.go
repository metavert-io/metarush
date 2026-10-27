package visual

import (
	"math"
	"sort"
)

// SubjectAt is where a clip's subject is at one moment, for framing that
// follows movement (D115): the main face of the shot when one is seen, else
// where the eye is drawn (attention), 0–1 from the left and top.
type SubjectAt struct {
	At   float64
	X, Y float64
	Face bool
}

// attentionMin is how strongly something must stand out to be followed when
// no face is seen.
const attentionMin = 0.25

// Subject lists where the subject is in [from, to) and the footage's own cuts
// there. In each shot the main face is the face track seen biggest over the
// shot (so the crop does not hop between two people); frames without it
// follow what stands out. ok is false when the index knows nothing to follow.
func Subject(idx *Index, from, to float64) (out []SubjectAt, cuts []float64, ok bool) {
	if idx == nil {
		return nil, nil, false
	}
	for _, c := range idx.Cuts {
		if c > from && c < to {
			cuts = append(cuts, c)
		}
	}
	shotOf := func(t float64) int { return sort.Search(len(cuts), func(i int) bool { return cuts[i] > t+0.05 }) }
	// The main face track of each shot: the most face area over the shot.
	area := map[int]map[int]float64{}
	for _, f := range idx.Faces {
		if f.At < from || f.At >= to {
			continue
		}
		sh := shotOf(f.At)
		if area[sh] == nil {
			area[sh] = map[int]float64{}
		}
		for i, b := range f.Boxes {
			if i < len(f.Tracks) {
				area[sh][f.Tracks[i]] += b[2] * b[3]
			}
		}
	}
	main := map[int]int{}
	for sh, m := range area {
		best, bestA := -1, -1.0
		for tr, a := range m {
			if a > bestA || (a == bestA && tr < best) {
				best, bestA = tr, a
			}
		}
		main[sh] = best
	}
	faceAt := map[float64]bool{}
	for _, f := range idx.Faces {
		if f.At < from || f.At >= to || len(f.Boxes) == 0 {
			continue
		}
		k := -1
		for i := range f.Boxes {
			if i < len(f.Tracks) && f.Tracks[i] == main[shotOf(f.At)] {
				k = i
			}
		}
		if k < 0 {
			continue // the main face isn't in this frame: what stands out decides (below)
		}
		b := f.Boxes[k]
		out = append(out, SubjectAt{At: f.At, X: r3(b[0] + b[2]/2), Y: r3(b[1] + b[3]/2), Face: true})
		faceAt[f.At] = true
	}
	for _, a := range idx.Attention {
		if a.At < from || a.At >= to || faceAt[a.At] || a.Strength < attentionMin {
			continue
		}
		x, y := a.X, a.Y
		if a.Box != nil { // the region that stands out, when there is one, is steadier than the heat's middle
			x, y = a.Box[0]+a.Box[2]/2, a.Box[1]+a.Box[3]/2
		}
		out = append(out, SubjectAt{At: a.At, X: r3(x), Y: r3(y)})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out, cuts, len(out) > 0
}

// Spread is how far a subject moves in [from, to): the widest swing of its
// position across or down (0 still … 1 the whole picture).
func Spread(s []SubjectAt) float64 {
	if len(s) == 0 {
		return 0
	}
	x0, x1, y0, y1 := math.Inf(1), math.Inf(-1), math.Inf(1), math.Inf(-1)
	for _, p := range s {
		x0, x1, y0, y1 = math.Min(x0, p.X), math.Max(x1, p.X), math.Min(y0, p.Y), math.Max(y1, p.Y)
	}
	return math.Max(x1-x0, y1-y0)
}
