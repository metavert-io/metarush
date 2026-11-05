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
// recordings and cutaways, clips shown behind people, narration and its
// cleaned-up copies, the mix's copies).
func HeadsUsing(ctx context.Context, a *app.App, assetID string) ([]HeadUse, error) {
	return headsUsing(ctx, a, assetID, false)
}

// HeadsPlaying is HeadsUsing plus the cuts whose latest version shows a
// picture taken from the asset (D457) or nests a cut that plays or shows it,
// at any depth: what a reference (someone else's video, D398, D440) must not
// be in.
func HeadsPlaying(ctx context.Context, a *app.App, assetID string) ([]HeadUse, error) {
	return headsUsing(ctx, a, assetID, true)
}

// framesOf are the pictures with a version taken from a clip (D457).
func framesOf(ctx context.Context, a *app.App, assetID string) (map[string]*models.Resource, error) {
	var rs []models.Resource
	if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"versions.source.frameOf.assetId": assetID}, &rs); err != nil {
		return nil, err
	}
	out := map[string]*models.Resource{}
	for i := range rs {
		out[rs[i].ID] = &rs[i]
	}
	return out, nil
}

// showsFrame reports whether a spec pins a version of a picture taken from the clip.
func showsFrame(s *editspec.Spec, frames map[string]*models.Resource, assetID string) bool {
	for _, pin := range s.Resources {
		if r := frames[pin.ID]; r != nil {
			if f := r.FrameOf(pin.SHA256); f != nil && f.AssetID == assetID {
				return true
			}
		}
	}
	return false
}

// playsFootage are the queries that find the versions playing a clip, one
// for each place editspec.AssetIDs reads — clips, cutaways, conversations'
// recordings (in a map by name), the cut's sound, clips shown behind people
// (D510), narration and its cleaned-up copies, the mix's copies — and the
// fixed copy a version pins for it. asset.where, the delete check and
// HeadsUsing all ask them; a place AssetIDs gains needs its query here
// (TestEveryPlaceACutPlaysAClipIsFound).
func playsFootage(assetID string) bson.A {
	members := bson.M{"$reduce": bson.M{
		"input":        bson.M{"$objectToArray": bson.M{"$ifNull": bson.A{"$spec.conversations", bson.M{}}}},
		"initialValue": bson.A{},
		"in":           bson.M{"$concatArrays": bson.A{"$$value", bson.M{"$ifNull": bson.A{"$$this.v.members.assetId", bson.A{}}}}},
	}}
	return bson.A{
		bson.M{"spec.clips.assetId": assetID}, bson.M{"spec.clips.shots.broll.assetId": assetID}, bson.M{"spec.audio.assetId": assetID},
		bson.M{"spec.clips.background.footage.assetId": assetID},
		bson.M{"spec.voiceover.beats.take.assetId": assetID}, bson.M{"spec.voiceover.beats.take.processed.assetId": assetID},
		bson.M{"spec.voiceover.continuous.assetId": assetID}, bson.M{"spec.voiceover.continuous.processed.assetId": assetID},
		bson.M{"spec.mix.enhanced.variant": assetID}, bson.M{"spec.mix.said.sound": assetID}, bson.M{"spec.mix.said.picture": assetID},
		bson.M{"spec.fixed." + assetID: bson.M{"$exists": true}},
		bson.M{"$expr": bson.M{"$in": bson.A{assetID, members}}},
	}
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
	// cut can play it.
	or := playsFootage(assetID)
	var frames map[string]*models.Resource
	if nested {
		or = append(or, bson.M{"spec.clips.assetId": bson.M{"$regex": "^cut:"}})
		var err error
		if frames, err = framesOf(ctx, a, assetID); err != nil {
			return nil, err
		}
		if len(frames) > 0 {
			ids := bson.A{}
			for id := range frames {
				ids = append(ids, id)
			}
			pinned := bson.M{"$map": bson.M{"input": bson.M{"$objectToArray": bson.M{"$ifNull": bson.A{"$spec.resources", bson.M{}}}}, "in": "$$this.v.id"}}
			or = append(or, bson.M{"$expr": bson.M{"$gt": bson.A{bson.M{"$size": bson.M{"$setIntersection": bson.A{pinned, ids}}}, 0}}})
		}
	}
	uses := func(s *editspec.Spec) bool {
		return slices.Contains(editspec.AssetIDs(s), assetID) || (nested && showsFrame(s, frames, assetID))
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
		plays := uses(&r.Spec)
		if !plays && nested {
			plays = nestedPlays(ctx, a, &r.Spec, uses, 1)
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

// nestedPlays reports whether a cut a spec nests uses the asset, at any depth.
func nestedPlays(ctx context.Context, a *app.App, s *editspec.Spec, uses func(*editspec.Spec) bool, depth int) bool {
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
		if uses(&r.Spec) || nestedPlays(ctx, a, &r.Spec, uses, depth+1) {
			return true
		}
	}
	return false
}
