package editspec_test

import (
	"math"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

func ptr(v float64) *float64 { return &v }

func lengthFrames(s *editspec.Spec) int {
	_, total := editspec.Timeline(s)
	return total
}

// Random studio ops on random cuts never panic, keep every clip's range in
// order, and change the length the way each gesture should: a split and a roll
// keep it, a slip keeps it, a removal never makes it longer. DiffOps
// reproduces every result.
func TestStudioOpsOnRandomCuts(t *testing.T) {
	for seed := int64(1); seed <= 400; seed++ {
		g := specgen.New(seed)
		base := g.Spec()
		editspec.Normalize(base)
		for i := range base.Clips {
			base.Clips[i].Locked = false
		}
		r := g.R
		c := base.Clips[r.Intn(len(base.Clips))]
		ci := 0
		for i := range base.Clips {
			if base.Clips[i].ID == c.ID {
				ci = i
			}
		}
		mid := c.In + (c.Out-c.In)*r.Float64()
		total := float64(lengthFrames(base)) / float64(base.Output.FPS)
		a, b := total*r.Float64(), total*r.Float64()
		if a > b {
			a, b = b, a
		}
		var op editspec.Op
		keep := false // the length stays the same
		switch r.Intn(6) {
		case 0:
			op, keep = editspec.Op{Op: "splitClip", ID: c.ID, At: ptr(mid)}, true
		case 1:
			op = editspec.Op{Op: "trimClip", ID: c.ID, Edge: []string{"in", "end"}[r.Intn(2)], To: ptr(mid)}
		case 2:
			if ci == 0 {
				continue
			}
			op, keep = editspec.Op{Op: "trimClip", ID: c.ID, Edge: "in", To: ptr(mid), Mode: editspec.TrimRoll}, true
		case 3:
			op, keep = editspec.Op{Op: "slipClip", ID: c.ID, By: ptr(r.Float64() - 0.5)}, true
		case 4:
			op = editspec.Op{Op: "removeWords", ID: c.ID, From: ptr(mid - 0.3), To: ptr(mid + 0.3)}
		default:
			op = editspec.Op{Op: "deleteRange", From: ptr(a), To: ptr(b)}
		}
		next, _, err := editspec.ApplyOps(base, []editspec.Op{op}, true)
		if err != nil {
			continue // refused (outside the clip, the whole cut, before the footage…): fine
		}
		editspec.Normalize(next)
		for _, x := range next.Clips {
			if x.Out <= x.In || x.In < 0 {
				t.Fatalf("seed %d %s: clip %s runs %v → %v", seed, op.Op, x.ID, x.In, x.Out)
			}
		}
		before, after := lengthFrames(base), lengthFrames(next)
		// Overlapping transitions are clamped to half the shorter shot, so cutting
		// a shot in two can shorten them; the length only holds without them.
		overlaps := false
		for _, x := range next.Clips {
			if x.Transition != nil && editspec.IsOverlapTransition(x.Transition.Type) {
				overlaps = true
			}
		}
		if keep && !overlaps && math.Abs(float64(after-before)) > 1 {
			t.Fatalf("seed %d %s: length %d → %d", seed, op.Op, before, after)
		}
		if (op.Op == "deleteRange" || op.Op == "removeWords") && !overlaps && after > before+2 {
			t.Fatalf("seed %d %s made the cut longer: %d → %d", seed, op.Op, before, after)
		}
		ops, err := editspec.DiffOps(base, next)
		if err != nil {
			t.Fatalf("seed %d %s: diff: %v", seed, op.Op, err)
		}
		got, _, err := editspec.ApplyOps(base, ops, true)
		if err != nil {
			t.Fatalf("seed %d: apply diff: %v", seed, err)
		}
		editspec.Normalize(got)
		if editspec.Hash(got) != editspec.Hash(next) {
			t.Fatalf("seed %d %s: diff ops did not reproduce the edit", seed, op.Op)
		}
	}
}
