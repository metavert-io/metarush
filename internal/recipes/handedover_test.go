package recipes

import (
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// On a computer that handed its role over (D349), a run started there goes
// on where the role is now — that computer's engine takes it; this one never
// does — and "Planning by itself" is looked at there too.
func TestRunsGoWhereTheRoleIs(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	host := testapp.HandedOver(t, f.a, "studio-mini")
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: plan.Fingerprint}, true).Run
	if run.Installation != host || run.Revision != "" || run.State != models.RunRunning {
		t.Fatalf("the run waits for the computer with the role: %+v", run.RecipeRun)
	}
	if got, err := Advance(f.ctx, f.a, run.ID); err != nil || got.Revision != "" {
		t.Fatalf("this computer never takes its steps: %+v %v", got, err)
	}
	f.fromCollection()
	on := true
	set := must[RecipeTriggersOut](t, f.a, "recipe.triggers.set", RecipeTriggersSetIn{Recipe: "app-teaser", NewFiles: &on}, true)
	if row := set.Recipes[0]; !row.On || row.Computer != "studio-mini" {
		t.Fatalf("planning by itself looks where the role is: %+v", row)
	}
	var r models.Recipe
	if err := f.a.DB.GetByID(f.ctx, store.CRecipes, f.a.WorkspaceID(), f.recipe().ID, &r); err != nil || r.Triggers == nil || r.Triggers.Installation != host {
		t.Fatalf("triggers %+v %v", r.Triggers, err)
	}
}
