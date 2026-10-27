package sources

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

func init() {
	ops.OnSourceFacts(func(ctx context.Context, a *app.App, assetID, why string) {
		_, _ = Refresh(ctx, a, assetID)
	})
}

// firstLookTypes are the analyses of MetaRush's first look at a source.
var firstLookTypes = []string{jobs.TypeProxy, jobs.TypeTranscribe, jobs.TypeDiarize, jobs.TypeVisual, jobs.TypeEvidence, jobs.TypeImport}

// FirstLookWindow is how long after import MetaRush may still rename a source
// by itself while analyses wait (a clip whose on-screen look waits for a Mac
// keeps its name after a day).
var FirstLookWindow = 24 * time.Hour

// firstLookPending reports whether a source's first analyses are still to run.
func firstLookPending(ctx context.Context, a *app.App, as *models.Asset) bool {
	if time.Since(as.CreatedAt) > FirstLookWindow {
		return false
	}
	n, err := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": as.ID,
		"type": bson.M{"$in": firstLookTypes}, "state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}})
	return err == nil && n > 0
}

// named reports whether a source shouldn't be renamed by MetaRush: a person
// named it.
func named(as *models.Asset) bool { return as.TitleBy == models.TitleByPerson }

// Refresh works out a source's descriptive name again (D150). While its first
// look runs (nameAuto) a better name replaces MetaRush's own; afterwards it
// is kept as a suggestion a person applies. A person's name is never
// replaced. It returns the title the source has afterwards.
func Refresh(ctx context.Context, a *app.App, assetID string) (string, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return "", err
	}
	if as.HasTag(models.TagEnhanced) || as.MadeNarration() || as.Kind == "image" {
		return as.DisplayTitle(), nil
	}
	sug := Suggest(ctx, a, &as)
	title := as.DisplayTitle()
	set, unset := bson.M{}, bson.M{}
	dropSuggestion := func() {
		if as.NameSuggestion != nil {
			unset["nameSuggestion"] = ""
		}
	}
	switch {
	case as.NameAuto && !named(&as):
		if sug != nil && sug.Title != title {
			set["title"], set["titleBy"] = sug.Title, models.TitleByMetaRush
		}
		dropSuggestion()
		if !firstLookPending(ctx, a, &as) {
			unset["nameAuto"] = "" // the first look is done: the name stops moving
		}
	case named(&as):
		dropSuggestion() // a person's name is final: no better name is offered over it
	case sug != nil && sug.Title != title:
		if as.NameSuggestion == nil || as.NameSuggestion.Title != sug.Title || as.NameSuggestion.Why != sug.Why {
			set["nameSuggestion"] = sug
		}
	default:
		dropSuggestion()
	}
	if len(set) == 0 && len(unset) == 0 {
		return title, nil // nothing changes (most analyses don't change the name)
	}
	upd := bson.M{}
	if len(set) > 0 {
		set["updatedAt"] = time.Now().UTC()
		upd["$set"] = set
	}
	if len(unset) > 0 {
		upd["$unset"] = unset
	}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, upd); err != nil {
		return title, err
	}
	if t, ok := set["title"].(string); ok {
		a.Audit(ctx, "asset.name", "asset", as.ID, firstOf(as.ProjectIDs), map[string]any{"title": t, "previous": title, "by": models.TitleByMetaRush})
		ops.RefreshAssetTodos(ctx, a, as.ID) // questions name the clip by its title
		return t, nil
	}
	return title, nil
}

func firstOf(xs []string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return ""
}
