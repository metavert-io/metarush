package editspec

// ReshapeRule is how a render of a version in another shape than its own is
// drawn (models.Render.ShapeRule, D388). Renders asked for before it carry 0
// and change only the size, as MetaRush always did — that stays so for them,
// their quality checks included; 1 also re-picks what depends on the shape
// for that render (Reshape).
const ReshapeRule = 1

// ForRender is the spec a render of a version draws: a copy of the version's
// own, at the size of the render's preset when it asks for another. With
// rule ReshapeRule, a render in another shape also re-picks what depends on
// the shape (Reshape). The version itself never changes, and a render in its
// own shape draws exactly its plan whatever the rule
// (TestRendersInAnotherShapeStayTheSame).
func ForRender(spec *Spec, preset string, rule int) *Spec {
	s := Clone(spec)
	if preset == "" || preset == s.Output.Preset {
		return s
	}
	was := s.Output
	d := Presets[preset]
	s.Output.Preset, s.Output.Width, s.Output.Height = preset, d[0], d[1]
	if rule >= ReshapeRule {
		Reshape(s, was)
	}
	return s
}

// Reshape re-picks, for a render of a cut in another shape than its own
// (was), what depends on the shape (D388) — by the rules the cut's own
// resolution uses, so a vertical copy of a widescreen cut looks like a
// vertical cut would:
//   - conversation clips' people side by side or stacked: stacked when the
//     frame is taller than wide, else side by side (the engine's rule for a
//     clip without shots, defaultShot);
//   - the platform, and so the margins captions and graphics keep
//     (PlatformFor: a widescreen cut for YouTube becomes Shorts);
//   - captions in the middle of a phone video sit lower in a wider one;
//   - framing: a cut that says none (made before framing existed: the middle)
//     keeps faces in frame, as new cuts do; faces and follow are worked out
//     for the frame they are drawn in already.
//
// Nothing changes when the shape is the same (a 4:5 cut drawn 9:16).
func Reshape(s *Spec, was Output) {
	from, to := was.Shape().Shape, s.Output.Shape().Shape
	if from == "" || to == "" || from == to {
		return
	}
	s.Output.Platform = PlatformFor(to, s.Output.Platform)
	if s.Output.Framing == "" {
		s.Output.Framing = FramingFaces
	}
	split := LayoutSideBySide
	if s.Output.Height > s.Output.Width {
		split = LayoutStacked
	}
	for i := range s.Clips {
		for k := range s.Clips[i].Shots {
			if sh := &s.Clips[i].Shots[k]; sh.Layout == LayoutSideBySide || sh.Layout == LayoutStacked {
				sh.Layout = split
			}
		}
	}
	if c := s.Captions; c != nil && c.Position == "middle" && to != ShapeVertical {
		c.Position = "lower"
	}
}
