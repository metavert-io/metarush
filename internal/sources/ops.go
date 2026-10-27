package sources

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// NamesIn lists the better names MetaRush has for sources.
type NamesIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Only this project's sources (ID or name); default: the whole workspace."`
	Limit     int64  `json:"limit,omitempty"`
}

// NameRow is one source with MetaRush's better name for it.
type NameRow struct {
	AssetID    string `json:"assetId"`
	Title      string `json:"title" jsonschema:"description=Its title now."`
	FileName   string `json:"fileName"`
	Suggestion string `json:"suggestion"`
	Why        string `json:"why,omitempty"`
	TitleBy    string `json:"titleBy,omitempty"`
	// Use says "use all" would rename it; Kept says why it wouldn't (a name a
	// person gave is never replaced).
	Use  bool   `json:"use"`
	Kept string `json:"kept,omitempty"`
}

// NamesOut is the list.
type NamesOut struct {
	Names   []NameRow `json:"names"`
	Summary string    `json:"summary"`
}

// NamesAcceptIn applies MetaRush's better names.
type NamesAcceptIn struct {
	ProjectID string   `json:"projectId,omitempty" jsonschema:"description=Only this project's sources (ID or name)."`
	AssetIDs  []string `json:"assetIds,omitempty" jsonschema:"description=Only these sources; default: every source a better name is waiting for."`
}

// NamesAcceptOut says what was renamed and what was kept.
type NamesAcceptOut struct {
	Renamed []NameRow `json:"renamed"`
	Kept    []NameRow `json:"kept"`
	Summary string    `json:"summary"`
}

func init() {
	ops.Def("asset.names", "List the better, descriptive names MetaRush suggests for sources (who is in them, what they are about), and which ones “use all” would apply (a name a person gave is never replaced)", ops.Flags{}, func(ctx context.Context, a *app.App, in *NamesIn) (*NamesOut, error) {
		rows, err := suggestions(ctx, a, in.ProjectID, nil, in.Limit)
		if err != nil {
			return nil, err
		}
		use := 0
		for _, r := range rows {
			if r.Use {
				use++
			}
		}
		out := &NamesOut{Names: rows}
		switch {
		case len(rows) == 0:
			out.Summary = "No better names right now: every source is named as well as MetaRush can."
		case use == len(rows):
			out.Summary = fmt.Sprintf("Better names for %s.", plural(len(rows), "source"))
		default:
			out.Summary = fmt.Sprintf("Better names for %s; %s keep the names people gave them.", plural(use, "source"), plural(len(rows)-use, "other"))
		}
		return out, nil
	})
	ops.Def("asset.names.accept", "Use MetaRush's better names for sources: all that are waiting, or the ones given; names people gave are kept", ops.Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *NamesAcceptIn) (*NamesAcceptOut, error) {
		rows, err := suggestions(ctx, a, in.ProjectID, in.AssetIDs, 0)
		if err != nil {
			return nil, err
		}
		out := &NamesAcceptOut{Renamed: []NameRow{}, Kept: []NameRow{}}
		for _, r := range rows {
			if !r.Use {
				out.Kept = append(out.Kept, r)
				continue
			}
			// Only while the suggestion is still the one listed (a concurrent rename wins).
			res, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": r.AssetID, "workspaceId": a.WorkspaceID(), "nameSuggestion.title": r.Suggestion, "titleBy": bson.M{"$ne": models.TitleByPerson}},
				bson.M{"$set": bson.M{"title": r.Suggestion, "titleBy": models.TitleByMetaRush, "updatedAt": time.Now().UTC()}, "$unset": bson.M{"nameSuggestion": "", "nameAuto": ""}})
			if err != nil {
				return nil, err
			}
			if res.ModifiedCount == 0 {
				continue
			}
			a.Audit(ctx, "asset.name", "asset", r.AssetID, "", map[string]any{"title": r.Suggestion, "previous": r.Title, "by": models.TitleByMetaRush, "accepted": true})
			ops.RefreshAssetTodos(ctx, a, r.AssetID)
			r.Title = r.Suggestion
			out.Renamed = append(out.Renamed, r)
		}
		switch {
		case len(out.Renamed) == 0 && len(out.Kept) == 0:
			out.Summary = "No better names were waiting."
		case len(out.Kept) == 0:
			out.Summary = fmt.Sprintf("Renamed %s.", plural(len(out.Renamed), "source"))
		default:
			out.Summary = fmt.Sprintf("Renamed %s; kept the names people gave %s.", plural(len(out.Renamed), "source"), plural(len(out.Kept), "other"))
		}
		return out, nil
	})
}

// suggestions lists sources with a better name waiting.
func suggestions(ctx context.Context, a *app.App, projectID string, assetIDs []string, limit int64) ([]NameRow, error) {
	f := bson.M{"nameSuggestion": bson.M{"$ne": nil}, "tags": bson.M{"$ne": models.TagEnhanced}}
	if projectID != "" {
		p, err := ops.ResolveProject(ctx, a, projectID)
		if err != nil {
			return nil, err
		}
		f["projectIds"] = p.ID
	}
	if len(assetIDs) > 0 {
		f["_id"] = bson.M{"$in": assetIDs}
	}
	if limit <= 0 || limit > 2000 {
		limit = 2000
	}
	var list []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), f, &list, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(limit)); err != nil {
		return nil, err
	}
	if len(assetIDs) > 0 && len(list) == 0 {
		return nil, apperr.New(apperr.NotFound, "MetaRush has no better names waiting for those sources.")
	}
	rows := make([]NameRow, 0, len(list))
	for _, as := range list {
		s := as.NameSuggestion
		if s == nil || strings.TrimSpace(s.Title) == "" || s.Title == as.DisplayTitle() {
			continue
		}
		r := NameRow{AssetID: as.ID, Title: as.DisplayTitle(), FileName: as.Name, Suggestion: s.Title, Why: s.Why, TitleBy: as.TitleBy}
		switch {
		case as.TitleBy == models.TitleByPerson:
			r.Kept = "someone named it"
		case as.TitleBy == "" && strings.TrimSpace(as.Title) != "" && as.Title != models.DefaultTitle(as.Name):
			r.Kept = "it already had a name that isn't its file name (someone may have given it)"
		default:
			r.Use = true
		}
		rows = append(rows, r)
	}
	return rows, nil
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
