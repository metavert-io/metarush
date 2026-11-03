package editspec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Stage 2 of the shape work (D385–D393) chooses shapes for new things — new
// cuts, generated media, renders in another shape asked for from now on —
// and never redraws a saved version. This guard extends
// TestPlansAtEveryShapeStayTheSame to every rule stage 2 touches:
//   - a version drawn in another preset by a render asked for before stage 2
//     (Render.ShapeRule 0) and in its own preset by any render: every case of
//     the stage 1 guard and the ones below, in each preset;
//   - a phone platform (shorts) at sizes either side of every threshold: its
//     margins (SafeAreaFor) stay what they were, whatever the shape;
//   - a spec that names no shape: Normalize still makes it vertical.
//
// testdata/shape-plans-2.golden.json was written from main at 761c769 (the
// commit before stage 2), with the worker's own size swap from there as
// renderedSpec, and must never change; regenerate it only on such a commit:
//
//	go test ./internal/editspec -run TestShapeChoicesLeaveSavedPlansAlone -update

var renderPresets = []string{PresetVertical, PresetLandscape, PresetSquare}

// renderedSpec is the spec a render in a preset draws, as a render asked for
// before stage 2 draws it (Render.ShapeRule 0).
func renderedSpec(s *Spec, preset string) *Spec { return ForRender(s, preset, 0) }

func shapePlan2Cases() map[string]shapePlanCase {
	out := shapePlanCases()
	legacy := legacySpecs()
	phone := Clone(legacy["captions-music-ducking"])
	phone.Output.Platform = "shorts"
	out["phone-platform"] = shapePlanCase{phone, voFacts()}
	none := Clone(legacy["cuts-crossfade-dip"])
	none.Output = Output{FPS: none.Output.FPS}
	out["no-output"] = shapePlanCase{none, voFacts()}
	return out
}

func shapePlans2(t *testing.T) map[string]json.RawMessage {
	got := map[string]json.RawMessage{}
	put := func(key string, plan *RenderPlan) {
		b, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		got[key] = b
	}
	cases := shapePlan2Cases()
	phone := cases["phone-platform"]
	for _, wh := range shapePlanSizes {
		s := Clone(phone.spec)
		s.Output.Width, s.Output.Height = wh[0], wh[1]
		Normalize(s)
		put(fmt.Sprintf("sizes/phone-platform@%dx%d", wh[0], wh[1]), Resolve(Clone(s), phone.facts, "rev_shape", "prj_shape", Hash(s)))
	}
	none := Clone(cases["no-output"].spec)
	Normalize(none)
	put("no-output", Resolve(Clone(none), cases["no-output"].facts, "rev_shape", "prj_shape", Hash(none)))
	for name, c := range cases {
		saved := Clone(c.spec) // as a version is saved: normalized
		Normalize(saved)
		for _, preset := range renderPresets {
			put(fmt.Sprintf("render/%s@%s", name, preset), Resolve(renderedSpec(saved, preset), c.facts, "rev_shape", "prj_shape", Hash(saved)))
		}
	}
	return got
}

func TestShapeChoicesLeaveSavedPlansAlone(t *testing.T) {
	path := filepath.Join("testdata", "shape-plans-2.golden.json")
	got := shapePlans2(t)
	if *updateGolden {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b := []byte("{\n")
		for i, k := range keys {
			name, _ := json.Marshal(k)
			b = append(append(append(append(b, ' '), name...), ": "...), got[k]...)
			if i < len(keys)-1 {
				b = append(b, ',')
			}
			b = append(b, '\n')
		}
		b = append(b, "}\n"...)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d plans)", path, len(keys))
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s missing: %v", path, err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("golden has %d plans, test builds %d", len(want), len(got))
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s: no longer built", name)
			continue
		}
		var a, b any
		_ = json.Unmarshal(w, &a)
		_ = json.Unmarshal(g, &b)
		if !equalJSON(a, b) {
			t.Errorf("%s: a saved version would be drawn differently\n want %s\n  got %s", name, w, g)
		}
	}
}
