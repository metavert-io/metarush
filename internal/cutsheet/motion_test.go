package cutsheet

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

var (
	routeV1 = "3fa9c2d1" + strings.Repeat("0", 56)
	routeV2 = "3fa9c2d2" + strings.Repeat("1", 56)
	logoV1  = "9b1c0de4" + strings.Repeat("2", 56)
)

// motionInfo adds the workspace's checked motion graphics to the test info.
type motionInfo struct {
	fakeInfo
	versions []string
	notes    map[string]string
}

func (m motionInfo) MotionVersions(prefix string) []string {
	var out []string
	for _, h := range m.versions {
		if strings.HasPrefix(h, prefix) {
			out = append(out, h)
		}
	}
	return out
}

func (m motionInfo) MotionNote(hash string) string { return m.notes[hash] }

func withMotion() motionInfo {
	return motionInfo{fakeInfo: testInfo(), versions: []string{routeV1, routeV2, logoV1}, notes: map[string]string{routeV1: "approved by Ada · 18 Oct 2026"}}
}

func motionSpec(overlays ...editspec.Overlay) *editspec.Spec {
	s := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetVertical, FPS: 30},
		Clips:    []editspec.Clip{{ID: "hook", AssetID: specgen.Assets[0], In: 0, Out: 20}},
		Overlays: overlays}
	editspec.Normalize(s)
	return s
}

func routeOverlay(id, hash string) editspec.Overlay {
	return editspec.Overlay{ID: id, Motion: &editspec.MotionRef{Name: "route-map", Hash: hash}, At: 12, Duration: 4, Params: json.RawMessage(`{"from":"Lisbon","to":"Porto"}`)}
}

func TestMotionLinePrintsShortVersionAndPin(t *testing.T) {
	s := motionSpec(routeOverlay("route", routeV1))
	text := mustRoundTrip(t, "motion", s, Options{ProjectID: testProject, BaseRevisionID: testBase, Info: withMotion()})
	for _, want := range []string{
		"motion:\n  route-map: sha256:" + routeV1 + "   # approved by Ada · 18 Oct 2026\n",
		"- route: motion route-map@3fa9c2d1 at 0:12 for 4 s · with from \"Lisbon\", to \"Porto\"\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	// Without info the pin still makes it lossless.
	mustRoundTrip(t, "motion without info", s, Options{})
}

func TestMotionTwoVersionsOfOneName(t *testing.T) {
	s := motionSpec(routeOverlay("first", routeV1), routeOverlay("second", routeV2))
	s.Overlays[1].At = 2
	text := mustRoundTrip(t, "two versions", s, Options{})
	if strings.Contains(text, "\nmotion:") {
		t.Fatalf("a name with two versions is not pinned in the settings:\n%s", text)
	}
	if !strings.Contains(text, "motion route-map@"+routeV1+" at") || !strings.Contains(text, "motion route-map@"+routeV2+" at") {
		t.Fatalf("both versions are written in full:\n%s", text)
	}
}

func TestMotionShortVersionResolvesInTheWorkspace(t *testing.T) {
	body := sheet("## Clips\n1. hook: interview-a 0:00 → 0:20\n## Graphics\n- logo: motion: logo-sting@9b1c0de4 at 0:01 for 2 s\n")
	sh, probs := Parse(body, ParseOptions{Info: withMotion()})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	o := sh.Spec.Overlays[0]
	if o.Motion == nil || o.Motion.Hash != logoV1 || o.Motion.Name != "logo-sting" || o.Template != "" {
		t.Fatalf("motion overlay %+v", o)
	}
	// Two checked versions share 3fa9c2d: ambiguous.
	_, probs = Parse(sheet("## Clips\n1. hook: interview-a 0:00 → 0:20\n## Graphics\n- r: motion route-map@3fa9c2d at 0:01 for 2 s\n"), ParseOptions{Info: withMotion()})
	if !hasCode(probs, "bad_motion") {
		t.Fatalf("7 characters are too few: %s", problemsText(probs))
	}
	info := withMotion()
	info.versions = append(info.versions, "3fa9c2d1"+strings.Repeat("f", 56))
	_, probs = Parse(sheet("## Clips\n1. hook: interview-a 0:00 → 0:20\n## Graphics\n- r: motion route-map@3fa9c2d1 at 0:01 for 2 s\n"), ParseOptions{Info: info})
	if !hasCode(probs, "motion_ambiguous") {
		t.Fatalf("ambiguous: %s", problemsText(probs))
	}
	// A full version needs no lookup at all.
	sh, probs = Parse(sheet("## Clips\n1. hook: interview-a 0:00 → 0:20\n## Graphics\n- r: route-map@"+routeV2+" at 0:01 for 2 s\n"), ParseOptions{})
	if HasErrors(probs) || sh.Spec.Overlays[0].Motion.Hash != routeV2 {
		t.Fatalf("full version: %s", problemsText(probs))
	}
}

func TestMotionChangesInWords(t *testing.T) {
	a := motionSpec(routeOverlay("route", routeV1))
	b := motionSpec(routeOverlay("route", routeV2))
	got := strings.Join(Describe(a, b, nil), " ")
	if !strings.Contains(got, "route now uses another version of route-map (3fa9c2d2, was 3fa9c2d1)") {
		t.Fatalf("version change: %q", got)
	}
	c := motionSpec(editspec.Overlay{ID: "route", Template: editspec.TplTitleCard, At: 12, Duration: 4, Params: json.RawMessage(`{"title":"x"}`)})
	if got := strings.Join(Describe(c, a, nil), " "); !strings.Contains(got, "route is now the motion graphic route-map") {
		t.Fatalf("template to motion: %q", got)
	}
	if got := strings.Join(Describe(motionSpec(), a, nil), " "); !strings.Contains(got, "Added the motion graphic route (route-map)") {
		t.Fatalf("added: %q", got)
	}
	// The saved change is one updateOverlay (merge patch), like any field.
	ops, err := editspec.DiffOps(c, a)
	if err != nil || len(ops) != 1 || ops[0].Op != "updateOverlay" || !strings.Contains(string(ops[0].Set), `"template":null`) || !strings.Contains(string(ops[0].Set), `"motion"`) {
		t.Fatalf("diff ops: %v %+v", err, ops)
	}
}
