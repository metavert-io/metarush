package editspec

import (
	"encoding/json"
	"strings"
	"testing"
)

// A cut that shows a motion graphic in a shape it was never drawn in gets a
// plain warning with the command that draws it there (D379); a graphic drawn
// at the cut's exact size, or never drawn at all, says nothing.
func TestMotionShapeUndrawnWarning(t *testing.T) {
	drawn := func(sizes ...[2]int) motionFacts {
		f := withMotion(MotionApproved)
		info := f.motion[mapHash]
		for i, s := range sizes {
			info.Stills = append(info.Stills, MotionStill{Index: i, Label: "middle", Width: s[0], Height: s[1]})
		}
		f.motion[mapHash] = info
		return f
	}
	cut := func(w, h int) *Spec {
		s := routeSpec(`{"from":"Lisbon","to":"Porto"}`)
		s.Output = Output{Width: w, Height: h, FPS: 30}
		s.Overlays = append(s.Overlays, Overlay{ID: "route-2", Motion: &MotionRef{Name: "route-map", Hash: mapHash}, At: 6, Duration: 3, Params: json.RawMessage(`{"from":"Porto","to":"Faro"}`)})
		return s
	}
	warns := func(s *Spec, f Facts) []Issue {
		var out []Issue
		for _, is := range Validate(s, f, nil, false) {
			if is.Code == "motion_shape_undrawn" {
				out = append(out, is)
			}
		}
		return out
	}

	// Drawn only vertical; the cut is widescreen: one warning for the graphic, however often it shows.
	got := warns(cut(1920, 1080), drawn([2]int{1080, 1920}))
	if len(got) != 1 || got[0].Severity != "warning" || got[0].Path != "/overlays/0/motion" {
		t.Fatalf("drawn vertical, cut widescreen: %+v", got)
	}
	w := got[0]
	if !strings.Contains(w.Message, "route-map") || !strings.Contains(w.Message, "vertical (9:16)") || !strings.Contains(w.Message, "widescreen (16:9)") || strings.Contains(w.Message, "metarush") {
		t.Fatalf("the words: %q", w.Message)
	}
	if w.Command != "metarush motion check --version route-map@"+mapHash[:8]+" --shape 1920x1080" {
		t.Fatalf("the command: %q", w.Command)
	}
	// Two vertical sizes are two shapes: drawn 9:16 says nothing about 4:5.
	if got := warns(cut(1080, 1350), drawn([2]int{1080, 1920})); len(got) != 1 || !strings.Contains(got[0].Command, "--shape 1080x1350") {
		t.Fatalf("drawn 9:16, cut 4:5: %+v", got)
	}
	// Drawn at the cut's size: nothing to say.
	if got := warns(cut(1920, 1080), drawn([2]int{1080, 1920}, [2]int{1920, 1080})); len(got) != 0 {
		t.Fatalf("drawn in the cut's shape: %+v", got)
	}
	// Never drawn (no test frames recorded): nothing to claim.
	if got := warns(cut(1920, 1080), drawn()); len(got) != 0 {
		t.Fatalf("never drawn: %+v", got)
	}
	// The same gaps at a render's size.
	gaps := MotionShapeGaps(cut(1080, 1920), drawn([2]int{1080, 1920}), 1920, 1080)
	if len(gaps) != 1 || strings.Join(gaps[0].OverlayIDs, ",") != "route,route-2" || gaps[0].Shape.Key() != "1920x1080" {
		t.Fatalf("gaps at a render's size: %+v", gaps)
	}
}
