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

// HeadsUsing lists the cuts whose latest version plays an asset, wherever a
// cut plays footage (editspec.AssetIDs: clips, the cut's sound, conversations'
// recordings and cutaways, narration and its cleaned-up copies, the mix's
// copies).
func HeadsUsing(ctx context.Context, a *app.App, assetID string) ([]HeadUse, error) {
	return headsUsing(ctx, a, assetID, false)
}

// HeadsPlaying is HeadsUsing plus the cuts whose latest version nests a cut
// that plays the asset, at any depth: what a reference (someone else's video,
// D398, D440) must not be in.
func HeadsPlaying(ctx context.Context, a *app.App, assetID string) ([]HeadUse, error) {
	return headsUsing(ctx, a, assetID, true)
}

func headsUsing(ctx context.Context, a *app.App, assetID string, nested bool) ([]HeadUse, error) {
	var ps []models.Project
	if err := a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"archived": bson.M{"$ne": true}}, &ps); err != nil {
		return nil, err
	}
	byID := map[string]models.Project{}
	var heads []string
	for _, p := range ps {
		byID[p.ID] = p
		for _, b := range p.Branches {
			if b.Head != "" {
				heads = append(heads, b.Head)
			}
		}
	}
	if len(heads) == 0 {
		return nil, nil
	}
	// Only the latest versions, and only those that name the asset where a
	// cut can play it (a conversation's recordings are in a map by name).
	members := bson.M{"$reduce": bson.M{
		"input":        bson.M{"$objectToArray": bson.M{"$ifNull": bson.A{"$spec.conversations", bson.M{}}}},
		"initialValue": bson.A{},
		"in":           bson.M{"$concatArrays": bson.A{"$$value", bson.M{"$ifNull": bson.A{"$$this.v.members.assetId", bson.A{}}}}},
	}}
	or := bson.A{
		bson.M{"spec.clips.assetId": assetID}, bson.M{"spec.clips.shots.broll.assetId": assetID}, bson.M{"spec.audio.assetId": assetID},
		bson.M{"spec.voiceover.beats.take.assetId": assetID}, bson.M{"spec.voiceover.beats.take.processed.assetId": assetID},
		bson.M{"spec.voiceover.continuous.assetId": assetID}, bson.M{"spec.voiceover.continuous.processed.assetId": assetID},
		bson.M{"spec.mix.enhanced.variant": assetID}, bson.M{"spec.mix.said.sound": assetID}, bson.M{"spec.mix.said.picture": assetID},
		bson.M{"spec.fixed." + assetID: bson.M{"$exists": true}},
		bson.M{"$expr": bson.M{"$in": bson.A{assetID, members}}},
	}
	if nested {
		or = append(or, bson.M{"spec.clips.assetId": bson.M{"$regex": "^cut:"}})
	}
	filter := bson.M{"_id": bson.M{"$in": heads}, "detached": bson.M{"$ne": true}, "$or": or}
	var revs []models.Revision
	if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), filter, &revs, options.Find().SetProjection(bson.M{"spec.notes": 0})); err != nil {
		return nil, err
	}
	var out []HeadUse
	for _, r := range revs {
		p, ok := byID[r.ProjectID]
		if !ok || p.Branches[r.Branch].Head != r.ID {
			continue
		}
		plays := slices.Contains(editspec.AssetIDs(&r.Spec), assetID)
		if !plays && nested {
			plays = nestedPlays(ctx, a, &r.Spec, assetID, 1)
		}
		if plays {
			out = append(out, HeadUse{Project: p, Branch: r.Branch, Revision: r, Fixed: r.Spec.FixedSHA(assetID)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Project.Name != out[j].Project.Name {
			return out[i].Project.Name < out[j].Project.Name
		}
		return out[i].Branch < out[j].Branch
	})
	return out, nil
}

// nestedPlays reports whether a cut a spec nests plays the asset, at any depth.
func nestedPlays(ctx context.Context, a *app.App, s *editspec.Spec, assetID string, depth int) bool {
	if depth > editspec.MaxNestDepth {
		return false
	}
	for _, c := range s.Clips {
		_, revID, ok := editspec.NestedRef(c.AssetID)
		if !ok {
			continue
		}
		var r models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), revID, &r); err != nil {
			continue
		}
		if slices.Contains(editspec.AssetIDs(&r.Spec), assetID) || nestedPlays(ctx, a, &r.Spec, assetID, depth+1) {
			return true
		}
	}
	return false
}
