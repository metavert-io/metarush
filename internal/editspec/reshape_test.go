package editspec

import (
	"encoding/json"
	"testing"
)

func planJSON(t *testing.T, p *RenderPlan) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A render in the version's own shape draws exactly the version's plan,
// whichever rule it was asked for under (D388).
func TestRenderInItsOwnShapeIsTheVersion(t *testing.T) {
	for name, c := range shapePlan2Cases() {
		saved := Clone(c.spec)
		Normalize(saved)
		own := planJSON(t, Resolve(Clone(saved), c.facts, "rev_shape", "prj_shape", Hash(saved)))
		for _, preset := range []string{"", saved.Output.Preset} {
			if got := planJSON(t, Resolve(ForRender(saved, preset, ReshapeRule), c.facts, "rev_shape", "prj_shape", Hash(saved))); got != own {
				t.Errorf("%s in its own shape (%q) is drawn differently:\n want %s\n  got %s", name, preset, own, got)
			}
		}
		// A preset of the same shape changes only the size (a 4:5 cut drawn 9:16).
		if saved.Output.Preset == "" {
			if p := PresetFor(saved.Output.Shape().Shape); p != "" {
				was := planJSON(t, Resolve(renderedSpec(saved, p), c.facts, "rev_shape", "prj_shape", Hash(saved)))
				if got := planJSON(t, Resolve(ForRender(saved, p, ReshapeRule), c.facts, "rev_shape", "prj_shape", Hash(saved))); got != was {
					t.Errorf("%s drawn %s (its own shape) re-picked something", name, p)
				}
			}
		}
	}
}

// A vertical copy of a widescreen call cut gets a vertical layout and a
// phone's margins, while the saved version's plan — and a copy asked for
// before stage 2 — stay as they were (D388).
func TestVerticalCopyOfALandscapeCallCut(t *testing.T) {
	f, s := convFixture()
	s.Output.Platform = "youtube"
	s.Output.Framing = ""
	s.Captions.Position = "middle"
	Normalize(s)
	saved := Clone(s)
	own := planJSON(t, Resolve(Clone(saved), f, "rev_call", "prj_call", Hash(saved)))

	copySpec := ForRender(saved, PresetVertical, ReshapeRule)
	if copySpec.Output.Platform != "shorts" || copySpec.Output.Framing != FramingFaces || copySpec.Output.Width != 1080 || copySpec.Output.Height != 1920 {
		t.Fatalf("vertical copy's output: %+v", copySpec.Output)
	}
	if copySpec.Captions.Position != "middle" {
		t.Fatalf("middle captions stay in the middle of a phone video: %+v", copySpec.Captions)
	}
	plan := Resolve(copySpec, f, "rev_call", "prj_call", Hash(saved))
	shots := plan.Clips[0].Conversation.Shots
	if shots[2].Layout != LayoutStacked {
		t.Fatalf("the two people should be stacked in the vertical copy: %+v", shots[2])
	}
	if want := SafeAreaFor("shorts", 1080, 1920); plan.SafeArea != want {
		t.Fatalf("margins %+v, want a phone's %+v", plan.SafeArea, want)
	}
	if plan.SpecHash != Hash(saved) {
		t.Fatalf("the copy is of the same version: %s", plan.SpecHash)
	}

	// The saved version is untouched by making the copy, and draws as before.
	if got := planJSON(t, Resolve(Clone(saved), f, "rev_call", "prj_call", Hash(saved))); got != own {
		t.Fatal("making the vertical copy changed the saved version's plan")
	}
	if saved.Clips[0].Shots[2].Layout != LayoutSideBySide || saved.Output.Platform != "youtube" {
		t.Fatalf("the saved version itself changed: %+v", saved.Output)
	}
	// A copy asked for before stage 2 (rule 0) still only changes the size.
	old := Resolve(ForRender(saved, PresetVertical, 0), f, "rev_call", "prj_call", Hash(saved))
	if old.Clips[0].Conversation.Shots[2].Layout != LayoutSideBySide || old.SafeArea != SafeAreaFor("youtube", 1080, 1920) {
		t.Fatalf("a copy asked for before stage 2 is drawn differently: %+v %+v", old.Clips[0].Conversation.Shots[2].Layout, old.SafeArea)
	}

	// Back the other way: a vertical call cut drawn widescreen sits side by
	// side, for YouTube, with its middle captions lower.
	v := Clone(saved)
	v.Output = Output{Preset: PresetVertical, Width: 1080, Height: 1920, FPS: 30, Platform: "shorts", Framing: FramingFaces}
	v.Clips[0].Shots[2].Layout = LayoutStacked
	wide := ForRender(v, PresetLandscape, ReshapeRule)
	if wide.Clips[0].Shots[2].Layout != LayoutSideBySide || wide.Output.Platform != "youtube" || wide.Captions.Position != "lower" {
		t.Fatalf("widescreen copy of a vertical cut: %+v %+v %+v", wide.Clips[0].Shots[2], wide.Output, wide.Captions)
	}
}

func TestPlatformFor(t *testing.T) {
	for _, c := range []struct {
		shape     Shape
		was, want string
	}{
		{ShapeVertical, "youtube", "shorts"}, {ShapeVertical, "tiktok", "tiktok"}, {ShapeVertical, "generic", "generic"}, {ShapeVertical, "", ""},
		{ShapeLandscape, "shorts", "youtube"}, {ShapeLandscape, "tiktok", "generic"}, {ShapeLandscape, "reels", "generic"}, {ShapeLandscape, "youtube", "youtube"},
		{ShapeSquare, "shorts", "generic"}, {ShapeSquare, "youtube", "youtube"}, {ShapeSquare, "generic", "generic"},
	} {
		if got := PlatformFor(c.shape, c.was); got != c.want {
			t.Errorf("PlatformFor(%s, %q) = %q, want %q", c.shape, c.was, got, c.want)
		}
	}
}

func TestRatioFor(t *testing.T) {
	gemini := []string{"16:9", "9:16"}
	pictures := []string{"1:1", "3:2", "2:3", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"}
	for _, c := range []struct {
		w, h     int
		offered  []string
		fallback string
		want     string
	}{
		{1080, 1920, gemini, "16:9", "9:16"},
		{1920, 1080, gemini, "16:9", "16:9"},
		{1080, 1350, gemini, "16:9", "9:16"}, // 4:5 is vertical: the vertical one
		{1080, 1080, gemini, "16:9", "16:9"}, // square: as near either way, the service's default
		{1080, 1080, gemini, "9:16", "9:16"},
		{1080, 1350, pictures, "1:1", "4:5"},
		{1080, 1080, pictures, "16:9", "1:1"},
		{2560, 1080, pictures, "1:1", "21:9"},
		{1080, 1920, nil, "16:9", ""},
		{0, 0, gemini, "16:9", ""},
	} {
		if got := RatioFor(c.w, c.h, c.offered, c.fallback); got != c.want {
			t.Errorf("RatioFor(%dx%d, %v) = %q, want %q", c.w, c.h, c.offered, got, c.want)
		}
	}
}

func TestCutSizeFor(t *testing.T) {
	for _, c := range []struct {
		w, h          int
		preset        string
		width, height int
		exact         bool
	}{
		{1920, 1080, PresetLandscape, 0, 0, true},
		{3840, 2160, PresetLandscape, 0, 0, true},
		{720, 1280, PresetVertical, 0, 0, true},
		{1080, 1080, PresetSquare, 0, 0, true},
		{1080, 1350, "", 1080, 1350, true}, // 4:5 stays 4:5
		{864, 1080, "", 1080, 1350, true},
		{1440, 1080, "", 1440, 1080, true}, // 4:3
		{2048, 858, "", 2578, 1080, false},
	} {
		o, exact := CutSizeFor(c.w, c.h)
		if o.Preset != c.preset || o.Width != c.width || o.Height != c.height || exact != c.exact {
			t.Errorf("CutSizeFor(%d, %d) = %+v %v, want %s %dx%d %v", c.w, c.h, o, exact, c.preset, c.width, c.height, c.exact)
		}
	}
}
