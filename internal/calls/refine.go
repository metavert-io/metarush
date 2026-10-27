package calls

import (
	"math"

	"github.com/metavert-io/metarush/internal/visual"
)

// refineWindow: a boundary of the on-screen index or a cut this close to a
// switch is that switch (the index looks about once a second; the evidence's
// cuts can be a sampler step off).
const refineWindow = 0.6

// Refine puts the on-screen index's shot and text boundaries and the
// evidence's cuts on the frame-accurate switches (D160): a boundary within
// 0.6 s of a switch moves to the switch's exact time (the first frame of the
// new picture), and a switch no cut was near becomes a cut. The index given
// is not changed; a copy is returned. With no switches, nothing changes.
func Refine(idx *visual.Index, cuts []float64, st *Stored) (*visual.Index, []float64) {
	if st == nil || len(st.Switches) == 0 || idx == nil {
		return idx, cuts
	}
	at := make([]float64, 0, len(st.Switches))
	for _, s := range st.Switches {
		at = append(at, s.At)
	}
	snap := func(t float64) float64 {
		best, bestD := t, refineWindow+1e-9
		for _, s := range at {
			if d := math.Abs(s - t); d < bestD {
				best, bestD = s, d
			}
		}
		return best
	}
	cp := *idx
	cp.Shots = make([]visual.ShotSpan, len(idx.Shots))
	for i, s := range idx.Shots {
		s.From, s.To = snap(s.From), snap(s.To)
		cp.Shots[i] = s
	}
	cp.Text = make([]visual.TextSpan, len(idx.Text))
	for i, x := range idx.Text {
		x.From, x.To = snap(x.From), snap(x.To)
		cp.Text[i] = x
	}
	out := make([]float64, 0, len(cuts)+len(at))
	seen := map[float64]bool{}
	add := func(t float64) {
		k := math.Round(t*1000) / 1000
		if !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	for _, c := range cuts {
		add(snap(c))
	}
	for _, s := range at {
		add(s)
	}
	return &cp, out
}
