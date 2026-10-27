package editspec

import (
	"slices"
	"strings"
	"testing"
)

// nestTestFacts: solid-color footage (selftestFacts) plus a chain of cuts,
// each playing the next: cut 1 → cut 2 → cut 3 → cut 4 (green footage).
type nestTestFacts struct {
	selftestFacts
	visible bool
	latest  int // the newest version number of every cut (0: the pinned one)
}

func nestProject(n int) string {
	return "prj_" + strings.Repeat(string(rune('a'+n)), 26)
}

func nestRevision(n int) string {
	return "rev_" + strings.Repeat(string(rune('a'+n)), 26)
}

func (f nestTestFacts) Nested(projectID, revisionID string) (*NestedSource, bool) {
	for n := 1; n <= 4; n++ {
		if projectID != nestProject(n) || revisionID != nestRevision(n) {
			continue
		}
		s := &Spec{SchemaVersion: 1, Output: Output{Width: 1280, Height: 720, FPS: 30, Platform: "youtube"}}
		if n < 4 {
			s.Clips = []Clip{{ID: "inner", AssetID: NestedKey(nestProject(n+1), nestRevision(n+1)), In: 0, Out: 2}}
		} else {
			s.Clips = []Clip{{ID: "green", AssetID: "green", In: 0, Out: 2}}
		}
		Normalize(s)
		src := &NestedSource{ProjectID: projectID, RevisionID: revisionID, ProjectName: "Project " + string(rune('A'+n)), Title: "Cut", Number: 1,
			Spec: s, Facts: f, Visible: f.visible, LatestID: revisionID, LatestNumber: 1}
		if f.latest > 1 {
			src.LatestID, src.LatestNumber = "rev_"+strings.Repeat("z", 26), f.latest
		}
		return src, true
	}
	return &NestedSource{ProjectID: projectID, RevisionID: revisionID, Missing: true}, true
}

func nestedCodes(s *Spec, f Facts) []string {
	var out []string
	for _, is := range Validate(Clone(s), f, nil, false) {
		out = append(out, is.Code)
	}
	return out
}

func TestNestedCutRules(t *testing.T) {
	out16 := Output{Width: 1280, Height: 720, FPS: 30, Platform: "youtube"}
	nest := func(n int, c Clip) *Spec {
		c.AssetID = NestedKey(nestProject(n), nestRevision(n))
		s := &Spec{SchemaVersion: 1, Output: out16, Clips: []Clip{c}}
		Normalize(s)
		return s
	}
	ok := nestTestFacts{visible: true}

	// Three levels (this cut → 3 → 4 → footage) resolve: picture and length as the inner cut plays.
	s := nest(3, Clip{ID: "intro", In: 0.5, Out: 2})
	if codes := nestedCodes(s, ok); slices.Contains(codes, "nested_too_deep") || slices.Contains(codes, "nested_not_linked") {
		t.Fatalf("a short chain was refused: %v", codes)
	}
	plan := Resolve(Clone(s), ok, "rev_test", "prj_test", "h")
	key := NestedKey(nestProject(3), nestRevision(3))
	inner := plan.Nested[key]
	if inner == nil || inner.Plan == nil || plan.DurationInFrames != 45 || inner.Plan.Nested[NestedKey(nestProject(4), nestRevision(4))] == nil {
		t.Fatalf("plan %+v nested %+v", plan.DurationInFrames, inner)
	}
	if ids := PlanSourceIDs(plan); !slices.Equal(ids, []string{"green"}) {
		t.Fatalf("the footage the nested cuts play: %v", ids)
	}

	// Deeper than three levels is refused.
	if codes := nestedCodes(nest(1, Clip{ID: "intro", In: 0, Out: 2}), ok); !slices.Contains(codes, "nested_too_deep") {
		t.Fatalf("four levels: %v", codes)
	}
	// A cut of a project that isn't linked is refused.
	if codes := nestedCodes(nest(3, Clip{ID: "intro", In: 0, Out: 2}), nestTestFacts{visible: false}); !slices.Contains(codes, "nested_not_linked") {
		t.Fatalf("not linked: %v", codes)
	}
	// It plays at its own speed; framing is ignored (a warning); a newer version is pointed out.
	codes := nestedCodes(nest(3, Clip{ID: "intro", In: 0, Out: 2, Speed: 2, Framing: &Framing{Mode: "fill"}}), nestTestFacts{visible: true, latest: 3})
	for _, want := range []string{"nested_speed", "nested_framing", "nested_newer"} {
		if !slices.Contains(codes, want) {
			t.Fatalf("missing %s in %v", want, codes)
		}
	}
	// A malformed reference.
	bad := &Spec{SchemaVersion: 1, Output: out16, Clips: []Clip{{ID: "x", AssetID: "cut:nope", In: 0, Out: 1}}}
	Normalize(bad)
	if codes := nestedCodes(bad, ok); !slices.Contains(codes, "bad_cut_ref") && !slices.Contains(codes, "schema") {
		t.Fatalf("malformed reference: %v", codes)
	}
}
