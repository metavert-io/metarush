package mattes

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Deleting footage for good deletes its cut-outs too (D505): they are its
// picture, so they go with it — the work still being made stopped, the
// files deleted from the media store and this computer's cache (asset.delete
// runs this only when no cut uses the footage, so no version pins them).
func init() { ops.RegisterDeleteHook("cut-outs", deleteCutOuts) }

func deleteCutOuts(ctx context.Context, a *app.App, assetID string) (int, error) {
	docs, err := OfClip(ctx, a, assetID)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range docs {
		m := &docs[i]
		if m.JobID != "" && (m.State == models.MatteWaiting || m.State == models.MatteMaking) {
			_, _ = a.Queue.RequestCancel(ctx, m.JobID)
		}
		if f := m.File; f != nil {
			// Another record of the same bytes (another fingerprint that made
			// the same file) keeps them.
			others, _ := a.DB.C(store.CMattes).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "file.sha256": f.SHA256, "assetId": bson.M{"$ne": assetID}})
			if others == 0 {
				if f.Key != "" {
					_ = a.Media.Delete(ctx, f.Key)
				}
				_, _ = a.Cache.Remove(f.SHA256)
			}
		}
		if _, err := a.DB.C(store.CMattes).DeleteOne(ctx, bson.M{"_id": m.ID, "workspaceId": a.WorkspaceID()}); err == nil {
			n++
		}
	}
	return n, nil
}
