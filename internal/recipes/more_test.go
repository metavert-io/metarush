package recipes

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/activity"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
)

// Pictures from a linked folder: the recipe takes the folder's newest, so a
// run (and a run again) with new art in the folder uses it without being told.
func TestFolderPicturesTakeTheNewest(t *testing.T) {
	f := setup(t)
	ws := f.a.WorkspaceID()
	now := time.Now().UTC()
	fld := models.LinkedFolder{ID: ids.New(ids.LinkedFolder), WorkspaceID: ws, Name: "Character art", Scope: models.ScopeProject, ProjectID: f.teaser, Upload: "small", CreatedAt: now}
	if err := f.a.DB.Insert(f.ctx, store.CLinkedFolders, fld); err != nil {
		t.Fatal(err)
	}
	if err := f.a.DB.Update(f.ctx, store.CResources, ws, f.hero.ID, bson.M{"$set": bson.M{"folder": models.ResourceFolderRef{FolderID: fld.ID, RelPath: "hero.png"}, "updatedAt": now}}); err != nil {
		t.Fatal(err)
	}
	out := f.save(RecipeSaveIn{})
	var art *RecipeInputRow
	for i, in := range out.Recipe.Inputs {
		if in.Kind == models.RecipePictures {
			art = &out.Recipe.Inputs[i]
		}
	}
	if art == nil || art.Name != "character-art" || !strings.Contains(art.From, "Character art") {
		t.Fatalf("the folder's input: %+v", out.Recipe.Inputs)
	}
	// New art arrives in the folder.
	if err := f.a.DB.Update(f.ctx, store.CResources, ws, f.villain.ID, bson.M{"$set": bson.M{"folder": models.ResourceFolderRef{FolderID: fld.ID, RelPath: "villain.png"}, "updatedAt": now.Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: out.Recipe.Name}, false)
	var got *models.RecipeRunInput
	for i := range plan.Inputs {
		if plan.Inputs[i].Name == "character-art" {
			got = &plan.Inputs[i]
		}
	}
	if got == nil || len(got.Items) != 1 || got.Items[0].ID != f.villain.ID || !got.Changed {
		t.Fatalf("the newest picture: %+v", got)
	}
}

// What a run waits for shows on the activity bar, plainly; rules the quality
// check can't measure become sign-offs on the run's cut (D207).
func TestWaitingShowsAndRulesBecomeSignoffs(t *testing.T) {
	f := setup(t)
	if _, err := f.a.DB.C(store.CProjects).UpdateOne(f.ctx, bson.M{"_id": f.teaser}, bson.M{"$push": bson.M{"rules": models.ProjectRule{ID: "rul_2", Text: "Public-domain worlds only", AddedAt: time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	f.save(RecipeSaveIn{})
	in := RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: "footage", Items: []string{f.interview2}}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint}, true).Run
	sum, err := activity.Build(context.Background(), f.a, &activity.SummaryIn{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range sum.Items {
		if it.Category == activity.CatRecipe && it.RunID == run.ID {
			found = true
			if it.State != activity.StateWaiting || !strings.Contains(it.StageLabel, "Waiting for Claude to choose the moments") || it.Title != "Run 1 of “App teaser”" {
				t.Fatalf("activity row %+v", it)
			}
		}
	}
	if !found || sum.Counts.Recipes != 1 || !strings.Contains(sum.Headline, "run 1 of “App teaser”: waiting for Claude") {
		t.Fatalf("activity %q %+v", sum.Headline, sum.Counts)
	}
	var p models.Project
	_ = f.a.DB.GetByID(f.ctx, store.CProjects, f.a.WorkspaceID(), f.teaser, &p)
	signed := false
	for _, s := range p.Signoffs {
		if s.Branch == run.Branch && s.Text == "Checked: Public-domain worlds only" {
			signed = true
		}
		if strings.Contains(s.Text, "35 seconds") {
			t.Fatalf("a measured rule is no sign-off: %+v", s)
		}
	}
	if !signed {
		t.Fatalf("sign-offs %+v", p.Signoffs)
	}
	// Runs make the current version: a draft can be planned, not run.
	must[RecipeUpdateOut](t, f.a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "app-teaser"}, AddHints: []string{"Open on a face."}}, false)
	dp := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser", Version: "draft"}, false)
	if _, err := call[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser", Version: "draft"}, Fingerprint: dp.Fingerprint}, true); codeOf(err) != apperr.Validation {
		t.Fatalf("a draft doesn't run: %v", err)
	}
}

// A recipe whose runs make a project each (episodes, events): the run makes
// the project, links the recipe's project (its intro and pictures stay
// where they are), adds the footage it was given, and builds the cut there.
func TestEachRunMakesAProject(t *testing.T) {
	f := setup(t)
	saved := f.save(RecipeSaveIn{NewProjects: true, Title: "Event recap", Name: "event-recap"})
	if saved.Recipe == nil || !strings.Contains(saved.Recipe.Lands, "a new project each time") {
		t.Fatalf("saved %+v", saved.Recipe)
	}
	in := RecipePlanIn{Recipe: "event-recap", Inputs: []RecipeInputValue{{Name: "headline", Text: "Spring gala"}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if !plan.Ready || !plan.NewProject || plan.Project != "Event recap 1" {
		t.Fatalf("plan %+v", plan)
	}
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint}, true).Run
	if run.ProjectID == "" || run.ProjectID == f.teaser || run.Branch != "main" || run.Revision == "" {
		t.Fatalf("run %+v", run.RecipeRun)
	}
	var p models.Project
	if err := f.a.DB.GetByID(f.ctx, store.CProjects, f.a.WorkspaceID(), run.ProjectID, &p); err != nil || p.Name != "Event recap 1" {
		t.Fatalf("project %+v %v", p, err)
	}
	var links []models.ProjectLink
	_ = f.a.DB.Find(f.ctx, store.CProjectLinks, f.a.WorkspaceID(), bson.M{"projectId": run.ProjectID}, &links)
	if len(links) != 1 || links[0].SourceID != f.teaser {
		t.Fatalf("it uses the recipe's project: %+v", links)
	}
	var as models.Asset
	_ = f.a.DB.GetByID(f.ctx, store.CAssets, f.a.WorkspaceID(), f.interview, &as)
	in2 := false
	for _, id := range as.ProjectIDs {
		in2 = in2 || id == run.ProjectID
	}
	if !in2 {
		t.Fatalf("the footage joined the run's project: %v", as.ProjectIDs)
	}
	rev := f.revision(run.Revision)
	if rev.ProjectID != run.ProjectID || len(rev.Spec.Clips) != 3 || paramText(findOverlay(&rev.Spec, "end").Params, "headline") != "Spring gala" {
		t.Fatalf("the cut %+v", rev.Spec)
	}
	f.finishReview(run.ID)
	if done := f.advance(run.ID); done.State != models.RunDone {
		t.Fatalf("done %+v", done.Steps)
	}
}

// Clearer voices: new footage gets the recipe's voice settings, priced with
// sound's own price when they are made with ElevenLabs (an upper bound: the
// new footage's length in the cut); the same footage needs nothing.
func TestClearerVoicesArePriced(t *testing.T) {
	d := doc()
	d.Cut.Mix = &editspec.Mix{Voices: []editspec.VoiceSound{{Name: "Maya", Enhance: &editspec.Enhance{Mode: editspec.EnhanceCloud}},
		{Name: "Sam", Enhance: &editspec.Enhance{Mode: editspec.EnhanceClean}}, {Name: "Ada", Enhance: &editspec.Enhance{Mode: editspec.EnhanceCloud, Off: true}}}}
	p := &planned{Doc: d, Changed: map[string]bool{}, Estimate: costs.Of(), stepUSD: map[string]float64{}}
	if need, _ := planSound(p); need {
		t.Fatal("the same footage needs no clearer voices")
	}
	p.Changed["footage"] = true
	need, detail := planSound(p)
	if !need || len(p.Estimate.Parts) != 1 || !strings.Contains(detail, "Sam on this computer (free)") {
		t.Fatalf("planned %v %q %+v", need, detail, p.Estimate.Parts)
	}
	l := p.Estimate.Parts[0]
	if l.Step != "Make the voices clearer" || l.Service != "ElevenLabs" || l.Model != providers.IsolationModel || l.Amount != 200 || l.Unit != "credits" ||
		!strings.Contains(l.Source, "Maya") || l.Units != "up to 12 s of sound (200 credits)" || l.USD != providers.IsolationUSD(200) || p.stepUSD[stepSound] != costs.Round(l.USD) {
		t.Fatalf("part %+v (step %v)", l, p.stepUSD)
	}
	if len(p.Estimate.Notes) != 1 || p.Estimate.Notes[0] != providers.PlanCreditsNote {
		t.Fatalf("notes %v", p.Estimate.Notes)
	}
}
