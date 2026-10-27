package cutsheet

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

// A cut's fixed copies (D152) are part of its sheet: `fixed:` names the
// footage by its name and the copy by its SHA-256, and they round-trip; a
// changed pin reads in words; `change: note` gives the next version its note
// (D150).
func TestFixedPinsAndVersionNotes(t *testing.T) {
	a := specgen.Assets[0]
	sha := strings.Repeat("ef", 32)
	s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetVertical, FPS: 30}, Clips: []editspec.Clip{{ID: "hook", AssetID: a, In: 1, Out: 4}},
		Fixed: map[string]editspec.FixedPin{a: {SHA256: sha}}}
	text := mustRoundTrip(t, "fixed", s, Options{Info: testInfo()})
	if !strings.Contains(text, "fixed:") || !strings.Contains(text, "sha256:"+sha) {
		t.Fatalf("the sheet names the fixed copy:\n%s", text)
	}

	withNote := strings.Replace(text, "cut: 1\n", "cut: 1\nchange: {request: \"use the steadier walk\", note: \"steadier walk\"}\n", 1)
	sheet, probs := Parse(withNote, ParseOptions{Info: testInfo()})
	if HasErrors(probs) || sheet.Note != "steadier walk" || sheet.Request != "use the steadier walk" || sheet.Spec.FixedSHA(a) != sha {
		t.Fatalf("change note: %+v %s", sheet, problemsText(probs))
	}
	bad := strings.Replace(text, "sha256:"+sha, "sha256:nope", 1)
	if _, probs := Parse(bad, ParseOptions{Info: testInfo()}); !HasErrors(probs) {
		t.Fatal("a fixed copy that isn't a SHA-256 is refused")
	}

	back := editspec.Clone(s)
	back.Fixed = nil
	name := func(string) string { return "interview-a" }
	if got := Describe(s, back, name); len(got) != 1 || !strings.Contains(got[0], "original") {
		t.Fatalf("back to the original: %v", got)
	}
	if got := Describe(back, s, name); len(got) != 1 || !strings.Contains(got[0], "fixed copy") {
		t.Fatalf("to the fixed copy: %v", got)
	}
}
