package remotion

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

func init() {
	ops.RegisterMediaUsers("Remotion projects' files", filesInUse)
}

// filesInUse reports which stored files (by SHA-256) the versions of Remotion
// projects other than the given projects' list in their manifests: clearing a
// project out for good (D196) keeps a file of its renders that a Remotion
// project elsewhere has too (a video in its public folder, say). The manifests
// are stored files themselves, so the database alone can't tell; a manifest
// that can't be read stops the check (nothing is deleted), one that is gone
// lists nothing.
func filesInUse(ctx context.Context, a *app.App, shas []string, exceptProjects []string) (map[string]bool, error) {
	want := map[string]bool{}
	for _, s := range shas {
		want[s] = true
	}
	used := map[string]bool{}
	var vs []models.RemotionVersion
	filter := bson.M{}
	if len(exceptProjects) > 0 {
		filter["projectId"] = bson.M{"$nin": exceptProjects}
	}
	if err := a.DB.Find(ctx, store.CRemotionVersions, a.WorkspaceID(), filter, &vs, options.Find().SetProjection(bson.M{"hash": 1, "manifest": 1})); err != nil {
		return nil, err
	}
	for i := range vs {
		m, err := loadManifest(ctx, a, &vs[i])
		if errors.Is(err, media.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, f := range m.Files {
			if want[f.SHA256] {
				used[f.SHA256] = true
			}
		}
	}
	return used, nil
}
