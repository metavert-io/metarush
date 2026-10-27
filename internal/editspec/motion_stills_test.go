package editspec

import (
	"fmt"
	"testing"
)

// The live preview shows a checked motion graphic's still in its place
// (D187): the stills of the shape the cut has (else the closest), earliest
// first; a graphic never drawn, and shared libraries, get none (they keep the
// placeholder); nested cuts see the same stills; renders never carry them.
func TestMotionStillsForThePreview(t *testing.T) {
	wide := []MotionStill{
		{Index: 0, Label: "first", At: 0, Width: 1920, Height: 1080},
		{Index: 2, Label: "middle", At: 2, Width: 1920, Height: 1080},
		{Index: 1, Label: "start", At: 0.4, Width: 1920, Height: 1080},
		{Index: 3, Label: "middle", At: 2, Width: 1080, Height: 1920},
		{Index: 4, Label: "middle", At: 2, Width: 1080, Height: 1080},
	}
	got := StillsFor(wide, 1080, 1920)
	if len(got) != 1 || got[0].Index != 3 {
		t.Fatalf("a vertical cut: %+v", got)
	}
	got = StillsFor(wide, 1920, 1080)
	if len(got) != 3 || got[0].Label != "first" || got[1].Label != "start" || got[2].Label != "middle" {
		t.Fatalf("a widescreen cut, earliest first: %+v", got)
	}
	// No still in the cut's very shape: the closest one (4:5 is nearest square).
	if got = StillsFor(wide, 1080, 1350); len(got) != 1 || got[0].Index != 4 {
		t.Fatalf("a 4:5 cut: %+v", got)
	}
	if StillsFor(nil, 1920, 1080) != nil {
		t.Fatal("stills of nothing")
	}

	checked, unchecked, lib := hashOf("a"), hashOf("b"), hashOf("c")
	nestedPlan := &RenderPlan{Width: 1920, Height: 1080, Overlays: []PlanOverlay{{ID: "n1", Motion: &PlanMotion{Name: "bar", Hash: checked}}}}
	p := &RenderPlan{Width: 1920, Height: 1080,
		Overlays: []PlanOverlay{{ID: "o1", Motion: &PlanMotion{Name: "bar", Hash: checked, Libs: map[string]string{"theme": lib}}}, {ID: "o2", Motion: &PlanMotion{Name: "card", Hash: unchecked}}},
		Nested:   map[string]*NestedPlan{"cut:prj/rev": {Plan: nestedPlan}}}
	infos := map[string]MotionInfo{
		checked:   {Hash: checked, Stills: wide, CheckSeconds: 4},
		unchecked: {Hash: unchecked},
		lib:       {Hash: lib, Kind: MotionKindLibrary, Stills: wide},
	}
	FillMotionStills(p, func(h string) (MotionInfo, bool) { m, ok := infos[h]; return m, ok }, func(h string, n int) string { return fmt.Sprintf("/still/%s/%d", h[:4], n) })
	if len(p.MotionStills) != 1 {
		t.Fatalf("stills for %d graphics, want only the checked one: %+v", len(p.MotionStills), p.MotionStills)
	}
	s := p.MotionStills[checked]
	if s == nil || s.Seconds != 4 || s.Width != 1920 || len(s.Frames) != 3 || s.Frames[2].URL != "/still/"+checked[:4]+"/2" || s.Frames[1].At != 0.4 {
		t.Fatalf("the checked graphic's stills: %+v", s)
	}
	if nestedPlan.MotionStills[checked] != s {
		t.Fatal("the nested cut doesn't see the stills")
	}
	if p.MotionSources != nil {
		t.Fatal("stills touched the render's sources")
	}
}

func hashOf(c string) string {
	h := ""
	for len(h) < 64 {
		h += c
	}
	return h
}
