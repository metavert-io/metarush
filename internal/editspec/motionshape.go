package editspec

import "fmt"

// DrawnShapes are the exact sizes a checked motion graphic version was drawn
// in (its test frames'), first seen first. None: never drawn (nothing to say).
func (m MotionInfo) DrawnShapes() []OutputShape {
	var out []OutputShape
	seen := map[string]bool{}
	for _, st := range m.Stills {
		s := ShapeOfSize(st.Width, st.Height)
		if k := s.Key(); k != "" && !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	return out
}

// MotionShapeGap is a motion graphic a cut shows in a shape it was never
// drawn in (D379): nobody has seen how it looks there.
type MotionShapeGap struct {
	Name, Hash string
	// Overlays are its places in the cut (indexes into Spec.Overlays) and
	// OverlayIDs their IDs.
	Overlays   []int
	OverlayIDs []string
	// Shape is the cut's shape; Drawn the shapes the version was drawn in.
	Shape OutputShape
	Drawn []OutputShape
}

// Command is what draws the version in the cut's shape: the same code drawn
// again, its approval unchanged (D377).
func (g MotionShapeGap) Command() string {
	short := g.Hash
	if len(short) > 8 {
		short = short[:8]
	}
	return fmt.Sprintf("metarush motion check --version %s@%s --shape %s", g.Name, short, g.Shape.Key())
}

// MotionShapeGaps lists the checked motion graphics a cut shows at w × h
// that were never drawn at exactly that size (D379); versions with no test
// frames say nothing.
func MotionShapeGaps(s *Spec, f Facts, w, h int) []MotionShapeGap {
	cut := ShapeOfSize(w, h)
	if cut.Shape == "" {
		return nil
	}
	var out []MotionShapeGap
	at := map[string]int{}
	for i, o := range s.Overlays {
		if o.Motion == nil {
			continue
		}
		if k, ok := at[o.Motion.Hash]; ok {
			if k >= 0 {
				out[k].Overlays = append(out[k].Overlays, i)
				out[k].OverlayIDs = append(out[k].OverlayIDs, o.ID)
			}
			continue
		}
		at[o.Motion.Hash] = -1
		info, ok := motionInfo(f, o.Motion.Hash)
		if !ok || info.Kind == MotionKindLibrary {
			continue
		}
		drawn := info.DrawnShapes()
		if len(drawn) == 0 {
			continue
		}
		missing := true
		for _, d := range drawn {
			if d.Key() == cut.Key() {
				missing = false
			}
		}
		if !missing {
			continue
		}
		at[o.Motion.Hash] = len(out)
		out = append(out, MotionShapeGap{Name: firstNonEmptyStr(info.Name, o.Motion.Name), Hash: o.Motion.Hash, Overlays: []int{i}, OverlayIDs: []string{o.ID}, Shape: cut, Drawn: drawn})
	}
	return out
}

// Words says a gap plainly: what was drawn, and the cut's shape.
func (g MotionShapeGap) Words() string {
	var drawn []string
	for _, d := range g.Drawn {
		drawn = append(drawn, d.Word+" ("+d.Ratio+")")
	}
	return fmt.Sprintf("The motion graphic %s was only ever drawn %s, never %s (%s) like this cut, so nobody has seen how it looks here. Draw it in this shape and look at its test frames before a final render; its code and approval stay the same.",
		g.Name, joinAnd(drawn), g.Shape.Word, g.Shape.Ratio)
}

// motionShapeIssues warns about graphics never drawn in the cut's shape (D379).
func motionShapeIssues(s *Spec, f Facts) []Issue {
	sh := s.Output.Shape()
	var out []Issue
	for _, g := range MotionShapeGaps(s, f, sh.Width, sh.Height) {
		is := warnI("motion_shape_undrawn", fmt.Sprintf("/overlays/%d/motion", g.Overlays[0]), "%s", g.Words())
		is.Command = g.Command()
		out = append(out, is)
	}
	return out
}
