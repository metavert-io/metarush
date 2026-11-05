package ops

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// A preview gives every cut-out a plan plays its address — nested cuts' too —
// except one MetaRush doesn't have (D510): with no file to show, the preview
// plays that clip as recorded, while a render fails for it.
func TestAPreviewGivesNoAddressForAMissingCutOut(t *testing.T) {
	have, gone, inner := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	plan := &editspec.RenderPlan{
		Clips: []editspec.PlanClip{
			{ID: "x", Background: &editspec.PlanBackground{With: "blur", CutOut: have}},
			{ID: "y", Background: &editspec.PlanBackground{With: "blur", CutOut: gone, Missing: true}},
		},
		CutOutSources: map[string]string{have: "", gone: ""},
		Nested: map[string]*editspec.NestedPlan{"cut:p/r": {Plan: &editspec.RenderPlan{Clips: []editspec.PlanClip{
			{ID: "z", Background: &editspec.PlanBackground{With: "colour", CutOut: inner}}}}}},
	}
	fillCutOuts(plan)
	if plan.CutOutSources[have] != CutOutURL(have) || plan.CutOutSources[inner] != CutOutURL(inner) {
		t.Fatalf("the cut-outs MetaRush has: %v", plan.CutOutSources)
	}
	if plan.CutOutSources[gone] != "" {
		t.Fatalf("a missing cut-out got an address: %v", plan.CutOutSources)
	}
}
