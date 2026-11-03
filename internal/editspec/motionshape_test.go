package editspec

import (
	"encoding/json"
	"strings"
	"testing"
)

// A cut that shows a motion graphic in a shape nobody approved it in gets a
// plain warning with the command that fills the gap (D379, D381): drawn in a
// ratio covers every size of it; an approved version covers only the shapes a
// person approved it in; never drawn says nothing.
func TestMotionShapeUndrawnWarning(t *testing.T) {
	sizes := func(xs ...[2]int) []OutputShape {
		var out []OutputShape
		for _, x := range xs {
			out = append(out, ShapeOfSize(x[0], x[1]))
		}
		return out
	}
	facts := func(state string, drawn [][2]int, approved [][2]int) motionFacts {
		f := withMotion(state)
		info := f.motion[mapHash]
		for i, s := range drawn {
			info.Stills = append(info.Stills, MotionStill{Index: i, Label: "middle", Width: s[0], Height: s[1]})
		}
		info.ApprovedShapes = sizes(approved...)
		f.motion[mapHash] = info
		return f
	}
	approvedAs := func(xs ...[2]int) motionFacts { return facts(MotionApproved, xs, xs) }
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
	v916, w169 := [2]int{1080, 1920}, [2]int{1920, 1080}

	// Drawn and approved only vertical; the cut is widescreen: one warning for the graphic, however often it shows.
	got := warns(cut(1920, 1080), approvedAs(v916))
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
	// The same ratio at another size is covered: 720p and 4K cuts of a graphic drawn at 1080p.
	for _, wh := range [][2]int{{1280, 720}, {3840, 2160}} {
		if got := warns(cut(wh[0], wh[1]), approvedAs(w169)); len(got) != 0 {
			t.Fatalf("drawn 16:9, cut %dx%d: %+v", wh[0], wh[1], got)
		}
	}
	// A 4K cut of a graphic never drawn widescreen: drawn at a test size, never 4K.
	if got := warns(cut(3840, 2160), approvedAs(v916)); len(got) != 1 || !strings.HasSuffix(got[0].Command, "--shape 1920x1080") {
		t.Fatalf("a 4K cut suggests a test size: %+v", got)
	}
	// Two vertical ratios are two shapes: 9:16 says nothing about 4:5.
	if got := warns(cut(1080, 1350), approvedAs(v916)); len(got) != 1 || !strings.HasSuffix(got[0].Command, "--shape 1080x1350") {
		t.Fatalf("drawn 9:16, cut 4:5: %+v", got)
	}
	// Drawn widescreen later (by anyone) but approved only vertical: still noted, and only a person's approval fills it.
	got = warns(cut(1920, 1080), facts(MotionApproved, [][2]int{v916, w169}, [][2]int{v916}))
	if len(got) != 1 || got[0].Command != "metarush motion approve route-map@"+mapHash[:8]+" --shape 1920x1080" || !strings.Contains(got[0].Message, "approved it only vertical (9:16)") {
		t.Fatalf("drawn but not approved widescreen: %+v", got)
	}
	// Approved in the cut's shape: nothing to say.
	if got := warns(cut(1920, 1080), approvedAs(v916, w169)); len(got) != 0 {
		t.Fatalf("approved in the cut's shape: %+v", got)
	}
	// Waiting for a person and drawn in the cut's shape: the approval's own error says it.
	if got := warns(cut(1920, 1080), facts(MotionWaiting, [][2]int{w169}, nil)); len(got) != 0 {
		t.Fatalf("waiting, drawn in the cut's shape: %+v", got)
	}
	// Never drawn (no test frames recorded): nothing to claim.
	if got := warns(cut(1920, 1080), facts(MotionApproved, nil, nil)); len(got) != 0 {
		t.Fatalf("never drawn: %+v", got)
	}
	// The same gaps at a render's size.
	gaps := MotionShapeGaps(cut(1080, 1920), approvedAs(v916), 1920, 1080)
	if len(gaps) != 1 || strings.Join(gaps[0].OverlayIDs, ",") != "route,route-2" || gaps[0].Shape.Key() != "1920x1080" || !gaps[0].Undrawn {
		t.Fatalf("gaps at a render's size: %+v", gaps)
	}
}

// Test frames are drawn at a sane size of a ratio: the short side at least
// 1080, both sides even, never 4K for a 16:9 cut (D379).
func TestTestSize(t *testing.T) {
	for in, want := range map[[2]int]string{{3840, 2160}: "1920x1080", {1280, 720}: "1920x1080", {1080, 1350}: "1080x1350", {720, 1280}: "1080x1920",
		{2560, 1080}: "2560x1080", {1000, 1000}: "1080x1080", {1000, 1149}: "2000x2298", {4096, 64}: "4096x64"} {
		if got := TestSize(in[0], in[1]).Key(); got != want {
			t.Errorf("TestSize(%v) = %s, want %s", in, got, want)
		}
	}
	if ShapeOfSize(1280, 720).RatioKey() != "16:9" || ShapeOfSize(1000, 1149).RatioKey() != "1000:1149" {
		t.Fatal("ratio keys")
	}
}
