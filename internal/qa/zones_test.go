package qa

import (
	"bytes"
	"encoding/json"
	"flag"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

var updateZones = flag.Bool("update", false, "rewrite video/render/fixtures/qa-zones.json")

// zonesPath is the Remotion self-test's fixture for the on-screen checks'
// geometry (D116): a Go-resolved plan of captions and a lower third over plain
// dark footage, and where QA says each is drawn. The self-test renders it and
// checks every bright pixel falls inside QA's box, so "the captions cover a
// face" means what it says.
//
//	go test ./internal/qa -run TestSelftestZones -update
const zonesPath = "../../video/render/fixtures/qa-zones.json"

func TestSelftestZones(t *testing.T) {
	spec := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Width: 720, Height: 1280, FPS: 30, Platform: "shorts"},
		Clips: []editspec.Clip{{ID: "dark", AssetID: "dark", In: 0, Out: 6}},
		Captions: &editspec.Captions{Enabled: true, Source: "manual", Position: "bottom", Segments: []editspec.CaptionSegment{
			{From: 0.2, To: 1.8, Text: "Captions sit in their band"}, {From: 4.2, To: 5.8, Text: "low on the screen, two lines at most"}}},
		Overlays: []editspec.Overlay{{ID: "who", Template: editspec.TplLowerThirdClean, At: 2.1, Duration: 1.8, Params: json.RawMessage(`{"name":"A builder","title":"Teacher"}`)}}}
	editspec.Normalize(spec)
	fx := &fakeFacts{assets: map[string]editspec.AssetFacts{"dark": {ID: "dark", Name: "dark.mp4", State: "available", InProject: true, Duration: 6, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30}}}
	plan := editspec.Resolve(editspec.Clone(spec), fx, "rev_selftest_qa", "prj_selftest", editspec.Hash(spec))
	plan.Media = "original"
	g := newGeom(plan)
	frac := func(b box) [4]float64 {
		r := func(v float64) float64 { return math.Round(v*10000) / 10000 }
		return [4]float64{r(b.L / g.W), r(b.T / g.H), r(b.R / g.W), r(b.B / g.H)}
	}
	cz := g.captionZone(plan.Captions.Position, plan.Captions.Style, plan.Captions.Size)
	lt := g.boxOf(plan.Overlays[0])
	doc := map[string]any{
		"generatedBy": "go test ./internal/qa -run TestSelftestZones -update (do not edit)",
		"media":       map[string]string{"dark": "dark.mp4"},
		"plans":       map[string]any{"qa-zones": plan},
		"checks": []map[string]any{
			{"frame": 30, "what": "captions", "box": frac(cz)},
			{"frame": 90, "what": "lower third", "box": frac(lt)},
			{"frame": 150, "what": "captions", "box": frac(cz)},
		},
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	b = append(b, '\n')
	if *updateZones {
		if err := os.WriteFile(zonesPath, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	cur, err := os.ReadFile(filepath.Clean(zonesPath))
	if err != nil {
		t.Skipf("video package not present: %v", err)
	}
	if !bytes.Equal(cur, b) {
		t.Fatal("video/render/fixtures/qa-zones.json is stale: go test ./internal/qa -run TestSelftestZones -update")
	}
}
