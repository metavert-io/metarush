package editspec

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// libFacts: a graphic lifted from a Remotion project (D171) that imports the
// shared library "film-theme", whose code draws the font "brandfont".
type libFacts struct{ slideFacts }

var (
	liftedSHA     = strings.Repeat("a1", 32)
	themeSHA      = strings.Repeat("b2", 32) // approved
	themeNextSHA  = strings.Repeat("b3", 32) // waiting for a person
	notLibrarySHA = slideMotionSHA           // a graphic, not a library
)

func (f libFacts) Motion(hash string) (MotionInfo, bool) {
	switch hash {
	case liftedSHA:
		return MotionInfo{Name: "end-card", Hash: hash, State: MotionApproved, Region: "full", MinDuration: 1, MaxDuration: 8,
			Schema: json.RawMessage(`{"type":"object","properties":{"tagline":{"type":"string"}}}`), Libraries: []string{"film-theme"}}, true
	case themeSHA:
		return MotionInfo{Name: "film-theme", Hash: hash, State: MotionApproved, Kind: MotionKindLibrary, Fonts: []string{"brandfont"}}, true
	case themeNextSHA:
		return MotionInfo{Name: "film-theme", Hash: hash, State: MotionWaiting, Kind: MotionKindLibrary, Fonts: []string{"brandfont"}}, true
	}
	return f.slideFacts.Motion(hash)
}

func liftedSpec(libs map[string]string, extra ...Overlay) *Spec {
	s := slideSpec(append([]Overlay{{ID: "end", At: 1, Duration: 2, Motion: &MotionRef{Name: "end-card", Hash: liftedSHA, Libs: libs}, Params: json.RawMessage(`{"tagline":"Made here"}`)}}, extra...)...)
	s.Resources["brandfont"] = ResourcePin{ID: "res_selftestfontzzzzzzzzzzzzzz", SHA256: selftestFontSHA}
	return s
}

func TestSharedLibraryPins(t *testing.T) {
	ok := liftedSpec(map[string]string{"film-theme": themeSHA})
	if errs := codesOf(Validate(ok, libFacts{}, nil, false)); len(errs) != 0 {
		t.Fatalf("a graphic pinned to an approved version of its library: %v", errs)
	}
	for name, c := range map[string]struct {
		s    *Spec
		want string
	}{
		"no version of its library":    {liftedSpec(nil), "motion_library_unpinned"},
		"a library nobody checked":     {liftedSpec(map[string]string{"film-theme": strings.Repeat("e4", 32)}), "motion_library_unknown"},
		"a graphic given as a library": {liftedSpec(map[string]string{"film-theme": notLibrarySHA}), "motion_library_unknown"},
		"a version waiting for people": {liftedSpec(map[string]string{"film-theme": themeNextSHA}), "motion_library_not_approved"},
		"two versions in one cut": {liftedSpec(map[string]string{"film-theme": themeSHA},
			Overlay{ID: "end2", At: 3, Duration: 1, Motion: &MotionRef{Name: "end-card", Hash: liftedSHA, Libs: map[string]string{"film-theme": themeNextSHA}}}), "motion_library_mixed"},
	} {
		errs := codesOf(Validate(c.s, libFacts{}, nil, false))
		if !strings.Contains(strings.Join(errs, ","), c.want) {
			t.Errorf("%s: want %s, got %v", name, c.want, errs)
		}
	}
}

func TestSharedLibraryFontsAndArchives(t *testing.T) {
	s := liftedSpec(map[string]string{"film-theme": themeSHA})
	// The library's font is a use of the cut's font resource (not a picture).
	var font *ResourceUse
	for _, u := range ResourceUses(s, libFacts{}) {
		if u.Name == "brandfont" {
			font = &u
		}
	}
	if font == nil || !font.Font || font.Motion != "end-card" {
		t.Fatalf("the library's font is the graphic's font use: %+v", font)
	}
	// A picture where the library draws a font is refused, and the other way round.
	bad := liftedSpec(map[string]string{"film-theme": themeSHA})
	bad.Resources["brandfont"] = ResourcePin{ID: "res_selftestlogozzzzzzzzzzzzzz", SHA256: selftestLogoSHA}
	if errs := codesOf(Validate(bad, libFacts{}, nil, false)); !slices.ContainsFunc(errs, func(e string) bool { return strings.HasPrefix(e, "resource_kind") }) {
		t.Errorf("a picture pinned where a font is drawn: %v", errs)
	}
	// The renderer is given the graphic's archive and its library's.
	if got := MotionHashes(s); !slices.Equal(got, []string{liftedSHA, themeSHA}) {
		t.Errorf("the spec's motion hashes: %v", got)
	}
	plan := Resolve(Clone(s), libFacts{}, "rev", "prj", Hash(s))
	if got := PlanMotionHashes(plan); !slices.Equal(got, []string{liftedSHA, themeSHA}) {
		t.Errorf("the plan's motion hashes (what the worker fetches): %v", got)
	}
	var po *PlanOverlay
	for i := range plan.Overlays {
		if plan.Overlays[i].ID == "end" {
			po = &plan.Overlays[i]
		}
	}
	if po == nil || po.Motion == nil || po.Motion.Libs["film-theme"] != themeSHA {
		t.Fatalf("the plan says which library version the graphic is bundled with: %+v", po)
	}
}
