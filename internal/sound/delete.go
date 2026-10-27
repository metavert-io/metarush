package sound

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Deleting footage for good deletes the voices made clearer from it too: the
// processed voices and the clearer copies are its sound, so they go with it
// (asset.delete runs this; the copies are never used by anything else).
func init() { ops.RegisterDeleteHook("clearer voices", deleteClearer) }

func deleteClearer(ctx context.Context, a *app.App, assetID string) (int, error) {
	ws := a.WorkspaceID()
	var recs []record
	cur, err := a.DB.C(Collection).Find(ctx, bson.M{"workspaceId": ws, "sourceAssetId": assetID})
	if err != nil {
		return 0, err
	}
	if err := cur.All(ctx, &recs); err != nil {
		return 0, err
	}
	n := 0
	for _, r := range recs {
		switch r.Kind {
		case "wet":
			if r.StorageKey != "" && a.Media.Delete(ctx, r.StorageKey) == nil {
				n++
			}
			if r.SHA256 != "" {
				_, _ = a.Cache.Remove(r.SHA256)
			}
		case "copy":
			if r.AssetID == "" {
				continue
			}
			var cp models.Asset
			if a.DB.GetByID(ctx, store.CAssets, ws, r.AssetID, &cp) != nil || !cp.HasTag(TagEnhanced) {
				continue // only the hidden copies MetaRush made
			}
			for _, coll := range []string{store.CInsights, store.CIngest, store.CNotifications} {
				_, _ = a.DB.C(coll).DeleteMany(ctx, bson.M{"workspaceId": ws, "assetId": cp.ID})
			}
			keys := []struct{ key, sha string }{{cp.StorageKey, cp.SHA256}}
			for _, v := range cp.Variants {
				keys = append(keys, struct{ key, sha string }{v.StorageKey, v.SHA256})
			}
			for _, k := range keys {
				if k.key != "" {
					_ = a.Media.Delete(ctx, k.key)
				}
				if k.sha != "" {
					_, _ = a.Cache.Remove(k.sha)
				}
			}
			if _, err := a.DB.C(store.CAssets).DeleteOne(ctx, bson.M{"_id": cp.ID, "workspaceId": ws}); err == nil {
				n++
			}
		}
	}
	_, _ = a.DB.C(Collection).DeleteMany(ctx, bson.M{"workspaceId": ws, "sourceAssetId": assetID})
	return n, nil
}
