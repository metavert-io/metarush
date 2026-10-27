package recipes

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A recipe's sheet (D213): checked out and read back unchanged; a change saved
// as a draft on the version it came from (whoever writes it); a new name with
// no base is a new recipe; what can't be saved says why; a version saved
// before versions were normalized reads back as itself.
func TestSheetCheckoutCheckWrite(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{Hints: []string{"Name the app in the first two seconds."}})
	co := must[RecipeCheckoutOut](t, f.a, "recipe.checkout", RecipeRefIn{Recipe: "app-teaser"}, false)
	if co.Name != "app-teaser" || co.File != "app-teaser.recipe.md" || co.Version.Number != 1 || !strings.Contains(co.Sheet, "\nbase: "+co.Version.Hash) {
		t.Fatalf("checkout %+v", co)
	}
	same := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Sheet: co.Sheet}, false)
	if !same.Valid || !same.Same || len(same.Changes) != 0 || same.Base == nil || same.Base.Number != 1 || !strings.HasPrefix(same.Message, "Nothing changed") {
		t.Fatalf("read back: %+v", same)
	}
	if w := must[RecipeWriteOut](t, f.a, "recipe.write", RecipeWriteIn{Sheet: co.Sheet}, false); !w.Same || w.Version.Number != 1 {
		t.Fatalf("nothing to save: %+v", w)
	}
	// A hint and a rule, written by Claude: a draft built on version 1.
	edited := strings.Replace(co.Sheet, "- Name the app in the first two seconds.\n", "- Name the app in the first two seconds.\n- End on the logo.\n", 1)
	edited = strings.Replace(edited, "## Rules\n", "## Rules\n- \"Captions at the top\" · check\n", 1)
	chk := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Sheet: edited}, false)
	if !chk.Valid || chk.Same || !chk.Check.OK || len(chk.Changes) != 2 || !strings.Contains(strings.Join(chk.Changes, " "), "Added the hint “End on the logo.”") {
		t.Fatalf("changes %+v", chk)
	}
	w := must[RecipeWriteOut](t, f.a, "recipe.write", RecipeWriteIn{Sheet: edited, Note: "end on the logo"}, false)
	if w.Same || w.New || w.Version.Number != 2 || w.Version.State != models.RecipeDraft || w.Version.Parent != 1 || w.Version.Note != "end on the logo" {
		t.Fatalf("draft %+v", w.Version)
	}
	// A person's sheet is a draft too: only publishing makes a version current.
	again := strings.Replace(edited, "- End on the logo.\n", "- End on the logo, never on black.\n", 1)
	if pw := must[RecipeWriteOut](t, f.a, "recipe.write", RecipeWriteIn{Sheet: again}, true); pw.Version.State != models.RecipeDraft || pw.Version.Parent != 1 {
		t.Fatalf("a person's sheet: %+v", pw.Version)
	}
	r, _ := loadRecipe(f.ctx, f.a, "app-teaser")
	if r.CurrentNumber != 1 || r.DraftNumber != 3 {
		t.Fatalf("still version 1 current: %+v", r)
	}
	// The saved draft's sheet builds on it.
	if !strings.Contains(w.Sheet, "\nbase: "+w.Version.Hash) {
		t.Fatalf("the draft's sheet:\n%s", w.Sheet)
	}
	// A new recipe from a sheet: a new name, no base.
	fresh := strings.Replace(co.Sheet, "name: app-teaser\n", "name: teaser-two\n", 1)
	fresh = strings.Replace(fresh, "\nbase: "+co.Version.Hash+"   # version 1, current", "", 1)
	fresh = strings.Replace(fresh, "title: App teaser\n", "title: Teaser two\n", 1)
	nw := must[RecipeWriteOut](t, f.a, "recipe.write", RecipeWriteIn{Sheet: fresh}, true)
	if !nw.New || nw.Recipe.Name != "teaser-two" || nw.Version.Number != 1 || nw.Version.State != models.RecipeDraft || nw.Recipe.Current != 0 {
		t.Fatalf("a new recipe: %+v %+v", nw.Recipe, nw.Version)
	}
	// Refusals: an existing name without a base, a base of another recipe, a sheet with problems.
	noBase := strings.Replace(co.Sheet, "\nbase: "+co.Version.Hash+"   # version 1, current", "", 1)
	if _, err := call[RecipeWriteOut](t, f.a, "recipe.write", RecipeWriteIn{Sheet: noBase}, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("an existing name without a base: %v", err)
	}
	otherBase := strings.Replace(fresh, "name: teaser-two\n", "name: teaser-two\nbase: "+co.Version.Hash+"\n", 1)
	if _, err := call[RecipeWriteOut](t, f.a, "recipe.write", RecipeWriteIn{Sheet: otherBase}, false); codeOf(err) != apperr.NotFound {
		t.Fatalf("a base of another recipe: %v", err)
	}
	broken := strings.Replace(co.Sheet, "## Hints", "## Tips", 1)
	_, err := call[RecipeWriteOut](t, f.a, "recipe.write", RecipeWriteIn{Sheet: broken}, false)
	if codeOf(err) != apperr.Validation || !strings.Contains(apperr.From(err).Message, "1 problem") {
		t.Fatalf("a sheet with a problem: %v", err)
	}
	if bc := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Sheet: broken}, false); bc.Valid || len(bc.Problems) == 0 || bc.Problems[0].Line == 0 {
		t.Fatalf("problems by line: %+v", bc)
	}
	// A version saved before versions were normalized (Tier 1) reads back as itself.
	v, _ := loadVersion(f.ctx, f.a, r, "1")
	old := Clone(&v.Doc)
	old.Cut.Output.Background, old.Cut.Output.Platform = "", ""
	_, h := Canonical(old)
	now := time.Now().UTC()
	ov := models.RecipeVersion{ID: versionID(f.a.WorkspaceID(), h), WorkspaceID: f.a.WorkspaceID(), RecipeID: r.ID, Hash: h, Number: 9, Doc: *old, State: models.RecipeEarlier, By: f.a.Actor, At: now}
	if err := f.a.DB.Insert(f.ctx, store.CRecipeVersions, ov); err != nil {
		t.Fatal(err)
	}
	oco := must[RecipeCheckoutOut](t, f.a, "recipe.checkout", RecipeRefIn{Recipe: "app-teaser", Version: "9"}, false)
	if oc := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Sheet: oco.Sheet}, false); !oc.Same {
		t.Fatalf("an older version reads back as itself: %+v", oc)
	}
	if ow := must[RecipeWriteOut](t, f.a, "recipe.write", RecipeWriteIn{Sheet: oco.Sheet}, false); !ow.Same || ow.Version.Number != 9 {
		t.Fatalf("nothing to save for it: %+v", ow.Version)
	}
	var n int64
	n, _ = f.a.DB.C(store.CRecipeVersions).CountDocuments(f.ctx, bson.M{"recipeId": r.ID})
	if n != 4 {
		t.Fatalf("versions: %d", n)
	}
}
