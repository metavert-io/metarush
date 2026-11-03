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

// MotionShapeGap is a motion graphic a cut shows in a shape nobody approved
// it in (D379, D381): never drawn in the cut's ratio (Undrawn), or drawn
// there — by a check anyone may run — but approved by a person only in other
// shapes.
type MotionShapeGap struct {
	Name, Hash string
	// Overlays are its places in the cut (indexes into Spec.Overlays) and
	// OverlayIDs their IDs.
	Overlays   []int
	OverlayIDs []string
	// Shape is the cut's shape; Drawn the shapes the version was drawn in,
	// Approved the ones a person approved it in.
	Shape    OutputShape
	Drawn    []OutputShape
	Approved []OutputShape
	// Undrawn: never drawn in the cut's ratio. Else it was, and waits for a
	// person's approval in that shape.
	Undrawn bool
	// DrawnAt is the size it was drawn at in the cut's ratio (when drawn).
	DrawnAt OutputShape
}

func shortVersion(hash string) string {
	if len(hash) > 8 {
		return hash[:8]
	}
	return hash
}

// Command is what fills the gap: drawing the same version in the cut's ratio
// at a test size (its code and approval unchanged, D377), or — drawn there
// already — a person approving it in that shape (people only).
func (g MotionShapeGap) Command() string {
	if g.Undrawn {
		return fmt.Sprintf("metarush motion check --version %s@%s --shape %s", g.Name, shortVersion(g.Hash), TestSize(g.Shape.Width, g.Shape.Height).Key())
	}
	return fmt.Sprintf("metarush motion approve %s@%s --shape %s", g.Name, shortVersion(g.Hash), g.DrawnAt.Key())
}

func ratioIn(xs []OutputShape, s OutputShape) (OutputShape, bool) {
	for _, x := range xs {
		if x.RatioKey() == s.RatioKey() {
			return x, true
		}
	}
	return OutputShape{}, false
}

// MotionShapeGaps lists the checked motion graphics a cut shows at w × h in a
// shape nobody approved them in (D379, D381): a version drawn in a shape
// covers every size of the same ratio; an approved one only in the shapes a
// person approved it in. Versions with no test frames say nothing; one not
// approved at all is the approval's own error.
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
		g := MotionShapeGap{Name: firstNonEmptyStr(info.Name, o.Motion.Name), Hash: o.Motion.Hash, Overlays: []int{i}, OverlayIDs: []string{o.ID}, Shape: cut, Drawn: drawn, Approved: info.ApprovedShapes}
		d, isDrawn := ratioIn(drawn, cut)
		switch {
		case !isDrawn:
			g.Undrawn = true
		case info.State == MotionApproved:
			if _, ok := ratioIn(info.ApprovedShapes, cut); ok {
				continue
			}
			g.DrawnAt = d
		default:
			continue // drawn; not approved at all is motion_not_approved's to say
		}
		at[o.Motion.Hash] = len(out)
		out = append(out, g)
	}
	return out
}

func shapeWords(xs []OutputShape) string {
	var ws []string
	for _, x := range xs {
		ws = append(ws, x.Word+" ("+x.Ratio+")")
	}
	return joinAnd(ws)
}

// Words says a gap plainly: what was drawn and approved, the cut's shape,
// and what a person does.
func (g MotionShapeGap) Words() string {
	cut := g.Shape.Word + " (" + g.Shape.Ratio + ")"
	if g.Undrawn {
		return fmt.Sprintf("The motion graphic %s was only ever drawn %s, never %s like this cut, so nobody has seen how it looks here. Draw it in this shape, then a person looks at it and approves it that way; its code stays the same.",
			g.Name, shapeWords(g.Drawn), cut)
	}
	return fmt.Sprintf("The motion graphic %s has test frames %s like this cut, but a person approved it only %s. A person looks at those frames and approves it in this shape; drawing it again doesn't count.",
		g.Name, cut, shapeWords(g.Approved))
}

// motionShapeIssues warns about graphics nobody approved in the cut's shape (D379, D381).
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
