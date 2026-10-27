package recipes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Records (D201): recipes by short name, versions by their document's
// SHA-256 (<workspace>/<sha256>), numbered with a parent; runs numbered per
// recipe. Versions are written once; only their state and check change.

// loadRecipe finds a recipe by ID, short name or title.
func loadRecipe(ctx context.Context, a *app.App, ref string) (*models.Recipe, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say which recipe.").WithCommand("metarush recipe list")
	}
	var r models.Recipe
	if ids.Valid(ids.Recipe, ref) {
		if err := a.DB.GetByID(ctx, store.CRecipes, a.WorkspaceID(), ref, &r); err == nil {
			return &r, nil
		}
		return nil, notFound(ref)
	}
	err := a.DB.C(store.CRecipes).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "name": strings.ToLower(ref)}).Decode(&r)
	if err == nil {
		return &r, nil
	}
	if !errors.Is(err, mongo.ErrNoDocuments) {
		return nil, err
	}
	var all []models.Recipe
	if err := a.DB.Find(ctx, store.CRecipes, a.WorkspaceID(), bson.M{}, &all); err != nil {
		return nil, err
	}
	var found []models.Recipe
	for _, x := range all {
		if strings.EqualFold(strings.TrimSpace(x.Title), ref) {
			found = append(found, x)
		}
	}
	switch len(found) {
	case 1:
		return &found[0], nil
	case 0:
		return nil, notFound(ref)
	}
	return nil, apperr.New(apperr.InvalidInput, "More than one recipe is called “%s”; use its short name.", ref).WithCommand("metarush recipe list")
}

func notFound(ref string) error {
	return apperr.New(apperr.NotFound, "There's no recipe called “%s”.", ref).WithHint("The Library's Recipes tab lists them.").WithCommand("metarush recipe list")
}

// versionID is a version's record ID.
func versionID(workspace, hash string) string { return workspace + "/" + hash }

// listVersions lists a recipe's versions, newest first.
func listVersions(ctx context.Context, a *app.App, recipeID string) ([]models.RecipeVersion, error) {
	var out []models.RecipeVersion
	err := a.DB.Find(ctx, store.CRecipeVersions, a.WorkspaceID(), bson.M{"recipeId": recipeID}, &out, options.Find().SetSort(bson.D{{Key: "number", Value: -1}}))
	return out, err
}

var hexPrefix = regexp.MustCompile(`^[0-9a-f]{7,64}$`)

// loadVersion finds a version of a recipe: "" the current one (else the
// newest draft), a number (3), "draft", "current", or 7+ characters of its hash.
func loadVersion(ctx context.Context, a *app.App, r *models.Recipe, ref string) (*models.RecipeVersion, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if len(ref) > 1 && ref[0] == 'v' && ref[1] >= '0' && ref[1] <= '9' {
		ref = ref[1:] // v3
	}
	hash := ""
	switch {
	case ref == "" || ref == "current":
		hash = r.Current
		if hash == "" && ref == "" {
			hash = r.Draft
		}
		if hash == "" {
			return nil, apperr.New(apperr.NotFound, "“%s” has no current version yet: a person makes one current.", r.Title).
				WithCommand("metarush recipe publish " + r.Name)
		}
	case ref == "draft":
		hash = r.Draft
		if hash == "" {
			return nil, apperr.New(apperr.NotFound, "“%s” has no draft newer than its current version.", r.Title)
		}
	}
	var v models.RecipeVersion
	if hash != "" {
		if err := a.DB.GetByID(ctx, store.CRecipeVersions, a.WorkspaceID(), versionID(a.WorkspaceID(), hash), &v); err != nil {
			return nil, err
		}
		return &v, nil
	}
	if n, err := strconv.Atoi(ref); err == nil {
		err := a.DB.C(store.CRecipeVersions).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "recipeId": r.ID, "number": n}).Decode(&v)
		if err == nil {
			return &v, nil
		}
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperr.New(apperr.NotFound, "“%s” has no version %d.", r.Title, n).WithCommand("metarush recipe show " + r.Name)
		}
		return nil, err
	}
	if hexPrefix.MatchString(ref) {
		vs, err := listVersions(ctx, a, r.ID)
		if err != nil {
			return nil, err
		}
		for i := range vs {
			if strings.HasPrefix(vs[i].Hash, ref) {
				return &vs[i], nil
			}
		}
	}
	return nil, apperr.New(apperr.NotFound, "“%s” has no version like that.", r.Title).WithCommand("metarush recipe show " + r.Name)
}

// saveVersion writes a document as a new version of a recipe (a draft), or
// returns the version it already is. parent is the version it came from.
func saveVersion(ctx context.Context, a *app.App, r *models.Recipe, d *models.RecipeDoc, parent, note string, check *models.RecipeCheck) (*models.RecipeVersion, bool, error) {
	return saveVersionWith(ctx, a, r, d, parent, note, check, nil)
}

// saveVersionWith is saveVersion for a version brought in from a recipe file
// (D219): it records where it came from.
func saveVersionWith(ctx context.Context, a *app.App, r *models.Recipe, d *models.RecipeDoc, parent, note string, check *models.RecipeCheck, imported *models.RecipeImported) (*models.RecipeVersion, bool, error) {
	d.Format = models.RecipeFormat
	NormalizeDoc(d) // its sheet reads back the same (D213)
	_, hash := Canonical(d)
	id := versionID(a.WorkspaceID(), hash)
	var had models.RecipeVersion
	if err := a.DB.GetByID(ctx, store.CRecipeVersions, a.WorkspaceID(), id, &had); err == nil {
		if had.RecipeID != r.ID {
			var other models.Recipe
			_ = a.DB.GetByID(ctx, store.CRecipes, a.WorkspaceID(), had.RecipeID, &other)
			return nil, false, apperr.New(apperr.Conflict, "This is exactly the recipe “%s” already is; run that one, or change something first.", other.Title).
				WithCommand("metarush recipe show " + other.Name)
		}
		return &had, false, nil
	}
	var bumped models.Recipe
	err := a.DB.C(store.CRecipes).FindOneAndUpdate(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$inc": bson.M{"versions": 1}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&bumped)
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	v := models.RecipeVersion{ID: id, WorkspaceID: a.WorkspaceID(), RecipeID: r.ID, Hash: hash, Number: bumped.Versions, Parent: parent,
		Doc: *Clone(d), Note: strings.TrimSpace(note), State: models.RecipeDraft, By: a.Actor, At: now, Check: check, Imported: imported}
	if err := a.DB.Insert(ctx, store.CRecipeVersions, v); err != nil {
		if store.IsDuplicateKey(err) {
			if err := a.DB.GetByID(ctx, store.CRecipeVersions, a.WorkspaceID(), id, &had); err == nil {
				return &had, false, nil
			}
		}
		return nil, false, err
	}
	set := bson.M{"draft": v.Hash, "draftNumber": v.Number, "updatedAt": now, "updatedBy": a.Actor, "title": d.Title, "makes": MakesWords(d)}
	if _, err := a.DB.C(store.CRecipes).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": set}); err != nil {
		return nil, false, err
	}
	r.Draft, r.DraftNumber, r.Versions, r.Title, r.Makes = v.Hash, v.Number, bumped.Versions, d.Title, MakesWords(d)
	return &v, true, nil
}

// makeCurrent makes a version the one runs make (people only, D206): the
// version before becomes an earlier one.
func makeCurrent(ctx context.Context, a *app.App, r *models.Recipe, v *models.RecipeVersion) error {
	now := time.Now().UTC()
	if r.Current != "" && r.Current != v.Hash {
		if _, err := a.DB.C(store.CRecipeVersions).UpdateOne(ctx, bson.M{"_id": versionID(a.WorkspaceID(), r.Current), "workspaceId": a.WorkspaceID()},
			bson.M{"$set": bson.M{"state": models.RecipeEarlier}}); err != nil {
			return err
		}
	}
	who := a.Actor
	if _, err := a.DB.C(store.CRecipeVersions).UpdateOne(ctx, bson.M{"_id": v.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$set": bson.M{"state": models.RecipeCurrent, "madeCurrentBy": who, "madeCurrentAt": now}}); err != nil {
		return err
	}
	set := bson.M{"current": v.Hash, "currentNumber": v.Number, "updatedAt": now, "updatedBy": who, "title": v.Doc.Title, "makes": MakesWords(&v.Doc)}
	unset := bson.M{}
	if r.Draft == v.Hash || r.DraftNumber <= v.Number {
		unset["draft"], unset["draftNumber"] = "", ""
		r.Draft, r.DraftNumber = "", 0
	}
	update := bson.M{"$set": set}
	if len(unset) > 0 {
		update["$unset"] = unset
	}
	if _, err := a.DB.C(store.CRecipes).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()}, update); err != nil {
		return err
	}
	v.State, v.MadeCurrentBy, v.MadeCurrentAt = models.RecipeCurrent, &who, &now
	r.Current, r.CurrentNumber, r.Title, r.Makes = v.Hash, v.Number, v.Doc.Title, MakesWords(&v.Doc)
	return nil
}

var nameCleaner = regexp.MustCompile(`[^a-z0-9]+`)

// shortName makes a short name from a title ("App teaser" → app-teaser).
func shortName(title string) string {
	s := strings.Trim(nameCleaner.ReplaceAllString(strings.ToLower(title), "-"), "-")
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "recipe-" + s
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s
}

// freeName is a short name no other recipe has (app-teaser, app-teaser-2…).
func freeName(ctx context.Context, a *app.App, want string) (string, error) {
	base := want
	for i := 2; i < 1000; i++ {
		n, err := a.DB.C(store.CRecipes).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "name": want})
		if err != nil {
			return "", err
		}
		if n == 0 {
			return want, nil
		}
		suffix := fmt.Sprintf("-%d", i)
		b := base
		if len(b)+len(suffix) > 40 {
			b = strings.TrimRight(b[:40-len(suffix)], "-")
		}
		want = b + suffix
	}
	return "", apperr.New(apperr.Conflict, "Every short name like “%s” is taken.", base)
}

// loadRun finds a run by ID, or as recipe#number (app-teaser#4).
func loadRun(ctx context.Context, a *app.App, ref string) (*models.RecipeRun, error) {
	ref = strings.TrimSpace(ref)
	var run models.RecipeRun
	if ids.Valid(ids.RecipeRun, ref) {
		if err := a.DB.GetByID(ctx, store.CRecipeRuns, a.WorkspaceID(), ref, &run); err != nil {
			return nil, apperr.New(apperr.NotFound, "That run isn't in this workspace.").WithCommand("metarush recipe runs")
		}
		return &run, nil
	}
	name, num, ok := strings.Cut(ref, "#")
	if !ok {
		name, num, ok = strings.Cut(ref, " ")
	}
	n, err := strconv.Atoi(strings.TrimSpace(num))
	if !ok || err != nil {
		return nil, apperr.New(apperr.InvalidInput, "Say which run: the recipe and the run's number, like app-teaser#4.").WithCommand("metarush recipe runs")
	}
	r, err := loadRecipe(ctx, a, name)
	if err != nil {
		return nil, err
	}
	if err := a.DB.C(store.CRecipeRuns).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "recipeId": r.ID, "number": n}).Decode(&run); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperr.New(apperr.NotFound, "“%s” has no run %d.", r.Title, n).WithCommand("metarush recipe runs " + r.Name)
		}
		return nil, err
	}
	return &run, nil
}

// runRef is the way people and commands name a run (app-teaser#4).
func runRef(r *models.RecipeRun) string { return fmt.Sprintf("%s#%d", r.RecipeName, r.Number) }
