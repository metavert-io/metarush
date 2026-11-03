package editspec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Saved versions are drawn the same whatever MetaRush's shape rule says
// (D376, D383): specs from the other golden tests — older cuts, conversations
// with and without shots, a nested cut, a motion graphic — at sizes either
// side of every shape threshold resolve to exactly the plans the engine made
// before the shape work. testdata/shape-plans.golden.json was written from
// main at c75d552 (the commit before it) and must never change; regenerate it
// only on such a commit:
//
//	go test ./internal/editspec -run TestPlansAtEveryShapeStayTheSame -update

var shapePlanSizes = [][2]int{{1080, 1200}, {1080, 1240}, {1080, 1242}, {1000, 1150}, {1087, 1250}, {1080, 1350}, {1200, 1080}, {1000, 1000}, {1920, 1080}, {1080, 1920}}

type shapePlanCase struct {
	spec  *Spec
	facts Facts
}

func shapePlanCases() map[string]shapePlanCase {
	out := map[string]shapePlanCase{}
	legacy := legacySpecs()
	for _, name := range []string{"cuts-crossfade-dip", "captions-music-ducking", "voiceover-captions-music"} {
		out["legacy/"+name] = shapePlanCase{legacy[name], voFacts()}
	}
	cf, conv := convFixture()
	out["conversation/shots"] = shapePlanCase{conv, cf}
	noShots := Clone(conv)
	for i := range noShots.Clips {
		noShots.Clips[i].Shots = nil
	}
	out["conversation/no-shots"] = shapePlanCase{noShots, cf}
	pf := convPlacementFacts()
	for name, s := range convPlacementSpecs() {
		if name == "gallery-view" || name == "two-parts-muted" {
			out["conversation/"+name] = shapePlanCase{s, pf}
		}
	}
	nested := &Spec{SchemaVersion: 1, Output: Output{Width: 1280, Height: 720, FPS: 30, Platform: "youtube"},
		Clips: []Clip{{ID: "intro", AssetID: NestedKey(nestProject(2), nestRevision(2)), In: 0, Out: 2}}}
	Normalize(nested)
	out["nested"] = shapePlanCase{nested, nestTestFacts{visible: true}}
	out["motion"] = shapePlanCase{routeSpec(`{"from":"Lisbon","to":"Porto"}`), withMotion(MotionApproved)}
	return out
}

func TestPlansAtEveryShapeStayTheSame(t *testing.T) {
	path := filepath.Join("testdata", "shape-plans.golden.json")
	got := map[string]json.RawMessage{}
	for name, c := range shapePlanCases() {
		for _, wh := range shapePlanSizes {
			s := Clone(c.spec)
			s.Output.Width, s.Output.Height = wh[0], wh[1]
			Normalize(s)
			plan := Resolve(Clone(s), c.facts, "rev_shape", "prj_shape", Hash(s))
			b, err := json.Marshal(plan)
			if err != nil {
				t.Fatal(err)
			}
			got[fmt.Sprintf("%s@%dx%d", name, wh[0], wh[1])] = b
		}
	}
	if *updateGolden {
		keys := make([]string, 0, len(got))
		for k := range got {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// One plan a line, compact: readable diffs, a small file.
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
