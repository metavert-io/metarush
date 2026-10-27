package qa

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// Outside footage gets one information note per source, at the time it shows,
// and never a warning or an error (it is informational, never blocking).
func TestOutsideFootageIsInformational(t *testing.T) {
	r := &run{in: Input{Credits: map[string]string{"ast_b": "@BlenderOfficial on YouTube"}}, fps: 30,
		plan: &editspec.RenderPlan{FPS: 30, Clips: []editspec.PlanClip{
			{ID: "me", AssetID: "ast_a", From: 0, DurationInFrames: 90},
			{ID: "bunny", AssetID: "ast_b", From: 90, DurationInFrames: 120},
			{ID: "bunny2", AssetID: "ast_b", From: 300, DurationInFrames: 60},
		}}}
	r.outsideFootage()
	if len(r.pend) != 1 {
		t.Fatalf("one note per source: %d", len(r.pend))
	}
	f := r.pend[0]
	if f.Severity != models.QAInfo || f.Kind != "outside_footage" || f.From != 3 || f.To != 12 || len(f.ClipIDs) != 2 ||
		!strings.Contains(f.Message, "@BlenderOfficial on YouTube") || !strings.Contains(f.Message, "0:03") {
		t.Fatalf("%+v", f)
	}
	r2 := &run{in: Input{}, fps: 30, plan: &editspec.RenderPlan{FPS: 30, Clips: []editspec.PlanClip{{ID: "me", AssetID: "ast_a", DurationInFrames: 90}}}}
	r2.outsideFootage()
	if len(r2.pend) != 0 {
		t.Fatal("no outside footage, no note")
	}
	// Footage inside a nested cut is credited at the nested clip's time.
	key := "cut:prj_aaaaaaaaaaaaaaaaaaaaaaaaaa/rev_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	r3 := &run{in: Input{Credits: map[string]string{"ast_b": "@BlenderOfficial on YouTube"}}, fps: 30, plan: &editspec.RenderPlan{FPS: 30,
		Clips:  []editspec.PlanClip{{ID: "intro", AssetID: key, From: 60, DurationInFrames: 90}},
		Nested: map[string]*editspec.NestedPlan{key: {Plan: &editspec.RenderPlan{Clips: []editspec.PlanClip{{ID: "b", AssetID: "ast_b", DurationInFrames: 200}}}}}}}
	r3.outsideFootage()
	if len(r3.pend) != 1 || r3.pend[0].From != 2 || r3.pend[0].To != 5 || r3.pend[0].ClipIDs[0] != "intro" {
		t.Fatalf("nested credit %+v", r3.pend)
	}
}

// Web pictures without a recorded licence get one information note each;
// files people added, and pictures with a licence, get none.
func TestOutsideResourcesAreInformational(t *testing.T) {
	r := &run{in: Input{Resources: []ResourceCredit{
		{Name: "logo", Title: "Acme logo", Kind: models.ResourceSVG, Origin: models.FromFile},
		{Name: "chart", Title: "Market chart", Kind: models.ResourceImage, Origin: models.FromURL, Site: "example.com"},
		{Name: "diagram", Title: "Diagram", Kind: models.ResourceImage, Origin: models.FromRepo, Site: "github.com/acme/app", License: "MIT"},
	}}, fps: 30, plan: &editspec.RenderPlan{FPS: 30}}
	r.outsideResources()
	if len(r.pend) != 1 || r.pend[0].Severity != models.QAInfo || r.pend[0].Kind != "resource_license" || !strings.Contains(r.pend[0].Message, "Market chart") || !strings.Contains(r.pend[0].Message, "example.com") {
		t.Fatalf("%+v", r.pend)
	}
}
