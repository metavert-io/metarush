package ops

import (
	"context"
	"slices"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// HeadUse is a cut whose latest version plays a piece of footage, and which
// media that version plays (D152).
type HeadUse struct {
	Project  models.Project
	Branch   string
	Revision models.Revision
	// Fixed is the fixed copy the version pins ("" plays the original).
	Fixed string
}

// HeadsUsing lists the cuts whose latest version plays an asset.
func HeadsUsing(ctx context.Context, a *app.App, assetID string) ([]HeadUse, error) {
	filter := bson.M{"detached": bson.M{"$ne": true}, "$or": bson.A{bson.M{"spec.clips.assetId": assetID}, bson.M{"spec.audio.assetId": assetID},
		bson.M{"spec.fixed." + assetID: bson.M{"$exists": true}}}}
	var revs []models.Revision
	if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), filter, &revs, options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(400).
		SetProjection(bson.M{"spec.notes": 0})); err != nil {
		return nil, err
	}
	if len(revs) == 0 {
		return nil, nil
	}
	pids := map[string]bool{}
	for _, r := range revs {
		pids[r.ProjectID] = true
	}
	var list []string
	for id := range pids {
		list = append(list, id)
	}
	var ps []models.Project
	if err := a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": list}}, &ps); err != nil {
		return nil, err
	}
	byID := map[string]models.Project{}
	for _, p := range ps {
		byID[p.ID] = p
	}
	var out []HeadUse
	for _, r := range revs {
		p, ok := byID[r.ProjectID]
		if !ok || p.Archived || p.Branches[r.Branch].Head != r.ID || !slices.Contains(editspec.AssetIDs(&r.Spec), assetID) {
			continue
		}
		out = append(out, HeadUse{Project: p, Branch: r.Branch, Revision: r, Fixed: r.Spec.FixedSHA(assetID)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project.Name != out[j].Project.Name {
			return out[i].Project.Name < out[j].Project.Name
		}
		return out[i].Branch < out[j].Branch
	})
	return out, nil
}
