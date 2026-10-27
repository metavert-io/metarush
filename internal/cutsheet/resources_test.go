package cutsheet

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

// libInfo adds the resources library and other projects' cuts to testInfo.
type libInfo struct {
	fakeInfo
	pins map[string]editspec.ResourcePin
	cuts map[string]CutDetails
}

func (l libInfo) ResourceDetails(id, sha string) (ResourceDetails, bool) {
	for n, p := range l.pins {
		if p.ID == id {
			return ResourceDetails{Title: "Acme " + n, Kind: "svg", Newer: p.SHA256 != sha}, true
		}
	}
	return ResourceDetails{}, false
}

func (l libInfo) ResourceByName(name string) (editspec.ResourcePin, bool) {
	p, ok := l.pins[name]
	return p, ok
}

func (l libInfo) CutDetails(key string) (CutDetails, bool) { d, ok := l.cuts[key]; return d, ok }

func (l libInfo) FindCut(project string, number int) (string, string) {
	for k, d := range l.cuts {
		if strings.EqualFold(d.ProjectName, project) && (number == 0 || d.Number == number) {
			return k, ""
		}
	}
	return "", "There's no version " + itoa(number) + " of " + project + "."
}

var brandIntro = specgen.NestedCuts[0]

func testLib() libInfo {
	return libInfo{fakeInfo: testInfo(),
		pins: map[string]editspec.ResourcePin{"logo": {ID: specgen.Resources[0], SHA256: strings.Repeat("e1", 32)}, "deck": {ID: specgen.Resources[2], SHA256: strings.Repeat("d4", 32)}},
		cuts: map[string]CutDetails{brandIntro: {ProjectName: "Brand", Title: "Brand intro", Number: 4, Duration: 12.5, NewerNumber: 6}}}
}

func nestedSpec() *editspec.Spec {
	s := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetVertical},
		Clips: []editspec.Clip{
			{ID: "hook", AssetID: specgen.Assets[0], In: 1, Out: 4},
			{ID: "intro", AssetID: brandIntro, In: 0, Out: 12.5, Transition: &editspec.Transition{Type: editspec.TransitionCrossfade, Duration: 0.4}},
		},
		Overlays:  []editspec.Overlay{{ID: "title", Template: editspec.TplTitleCard, At: 0, Duration: 3, Params: json.RawMessage(`{"title":"Hello","logo":"logo"}`)}},
		Resources: map[string]editspec.ResourcePin{"logo": {ID: specgen.Resources[0], SHA256: strings.Repeat("aa", 32)}},
	}
	editspec.Normalize(s)
	return s
}

func TestNestedCutAndResourcePinsRoundTrip(t *testing.T) {
	text := mustRoundTrip(t, "nested", nestedSpec(), Options{ProjectID: testProject, BaseRevisionID: testBase, Info: testLib()})
	for _, want := range []string{
		"resources:\n  logo: " + specgen.Resources[0] + " sha256:" + strings.Repeat("aa", 32) + "   # “Acme logo” · SVG · a newer version is available",
		"cuts:\n  brand: prj_01jabcdefghjkmnpqrstvwxyz5/rev_01jabcdefghjkmnpqrstvwxyz5   # “Brand intro” · Brand · version 4 · 0:12.5 · a newer version (6) is available",
		"2. intro: cut from brand 0:00 → 0:12.5",
		"> Brand intro from Brand, version 4 (version 6 is newer)",
		`logo "logo"`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in:\n%s", want, text)
		}
	}
	// Without lookups the names are still unique and the sheet still reads back.
	mustRoundTrip(t, "nested, no info", nestedSpec(), Options{})
}

func TestHandWrittenCutAndUnpinnedResource(t *testing.T) {
	sheet := `---
cut: 1
project: ` + testProject + `
footage:
  interview-a: ` + specgen.Assets[0] + `
brand:
  font file: logo
---
## Clips
1. hook: interview-a 0:01 → 0:04
2. intro: cut from Brand r4 0:00 → 0:10

## Graphics
- title: title.card at 0:00 for 3 s · with title "Hello", logo "logo", image "deck#2"
`
	s, probs := Parse(sheet, ParseOptions{Info: testLib()})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	if s.Spec.Clips[1].AssetID != brandIntro {
		t.Fatalf("cut from Brand r4 → %s", s.Spec.Clips[1].AssetID)
	}
	if s.Spec.Resources["logo"].SHA256 != strings.Repeat("e1", 32) || s.Spec.Resources["deck"].ID != specgen.Resources[2] {
		t.Fatalf("names used without a pin are pinned to the library's version: %+v", s.Spec.Resources)
	}
	if !hasCode(probs, "resource_pinned") {
		t.Fatalf("pinning says so: %s", problemsText(probs))
	}
	if s.Spec.Brand == nil || s.Spec.Brand.FontFile != "logo" {
		t.Fatalf("brand font file: %+v", s.Spec.Brand)
	}
	// An unknown version and an unknown resource are plain problems.
	if _, probs := Parse(strings.Replace(sheet, "Brand r4", "Brand r9", 1), ParseOptions{Info: testLib()}); !hasCode(probs, "unknown_cut") {
		t.Fatalf("expected unknown_cut: %s", problemsText(probs))
	}
	if _, probs := Parse(strings.Replace(sheet, `logo "logo"`, `logo "nope"`, 1), ParseOptions{Info: testLib()}); !hasCode(probs, "resource_unknown") {
		t.Fatalf("expected resource_unknown: %s", problemsText(probs))
	}
	// A footage name "cut" still works as footage.
	plain := strings.Replace(strings.Replace(sheet, "interview-a:", "cut:", 1), "1. hook: interview-a", "1. hook: cut", 1)
	if s, probs := Parse(plain, ParseOptions{Info: testLib()}); HasErrors(probs) || s.Spec.Clips[0].AssetID != specgen.Assets[0] {
		t.Fatalf("footage called cut: %s", problemsText(probs))
	}
}

func TestBadPins(t *testing.T) {
	base := "---\ncut: 1\nfootage:\n  a: " + specgen.Assets[0] + "\n%s---\n## Clips\n1. a 0:00 → 0:01\n"
	for _, c := range []struct{ settings, code string }{
		{"resources:\n  Logo: " + specgen.Resources[0] + " sha256:" + strings.Repeat("a", 64) + "\n", "bad_resource"},
		{"resources:\n  logo: " + specgen.Resources[0] + " sha256:abcd\n", "bad_resource"},
		{"cuts:\n  brand: prj_x/rev_y\n", "bad_cut"},
	} {
		_, probs := Parse(strings.Replace(base, "%s", c.settings, 1), ParseOptions{})
		if !hasCode(probs, c.code) {
			t.Errorf("%q: expected %s, got %s", c.settings, c.code, problemsText(probs))
		}
	}
}
