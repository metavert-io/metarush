package cutsheet

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

var (
	themeV1 = "7e3a91c0" + strings.Repeat("3", 56)
	themeV2 = "7e3a91c1" + strings.Repeat("4", 56)
)

// sharedLibInfo adds shared libraries (D171): route-map imports "brand-theme",
// whose newest approved version is themeV2.
type sharedLibInfo struct{ motionInfo }

func (l sharedLibInfo) MotionLibraries(hash string) []string {
	if hash == routeV1 || hash == routeV2 {
		return []string{"brand-theme"}
	}
	return nil
}

func (l sharedLibInfo) LatestMotionLibrary(name string) string {
	if name == "brand-theme" {
		return themeV2
	}
	return ""
}

func withLibs() sharedLibInfo {
	m := withMotion()
	m.versions = append(m.versions, themeV1, themeV2)
	return sharedLibInfo{m}
}

func TestMotionLibraryPinsRoundTrip(t *testing.T) {
	o := routeOverlay("route", routeV1)
	o.Motion.Libs = map[string]string{"brand-theme": themeV1}
	s := motionSpec(o)
	text := mustRoundTrip(t, "a graphic and its library", s, Options{ProjectID: testProject, BaseRevisionID: testBase, Info: withLibs()})
	for _, want := range []string{"  brand-theme: sha256:" + themeV1, "- route: motion route-map@3fa9c2d1+brand-theme@7e3a91c0 at 0:12"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	mustRoundTrip(t, "without info", s, Options{})
}

func TestMotionLineFillsInTheNewestApprovedLibrary(t *testing.T) {
	body := sheet("## Clips\n1. hook: interview-a 0:00 → 0:20\n## Graphics\n- route: motion route-map@3fa9c2d1 at 0:12 for 4 s\n")
	sh, probs := Parse(body, ParseOptions{Info: withLibs()})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	m := sh.Spec.Overlays[0].Motion
	if m == nil || m.Libs["brand-theme"] != themeV2 {
		t.Fatalf("a line naming no library version pins the newest approved one: %+v", m)
	}
	// A version written on the line wins.
	body = sheet("## Clips\n1. hook: interview-a 0:00 → 0:20\n## Graphics\n- route: motion route-map@3fa9c2d1+brand-theme@7e3a91c0 at 0:12 for 4 s\n")
	sh, probs = Parse(body, ParseOptions{Info: withLibs()})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	if got := sh.Spec.Overlays[0].Motion.Libs["brand-theme"]; got != themeV1 {
		t.Fatalf("the written version: %s", got)
	}
	_ = editspec.MotionRef{}
}
