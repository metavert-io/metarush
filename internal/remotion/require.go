package remotion

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// RequireApproved refuses to render (or check the render of) a cut that plays
// a composition MetaRush rendered from a version no person approved, or
// whose approval was withdrawn (D136). Videos a project already had (out/)
// are ordinary footage and never refused.
func RequireApproved(ctx context.Context, a *app.App, assets []models.Asset) error {
	ids := map[string]*models.RemotionClipSource{}
	var keys []string
	for i := range assets {
		if s := assets[i].Source; s.IsRemotion() && s.Remotion.Kind == models.SourceRemotionRender {
			if _, ok := ids[s.Remotion.Version]; !ok {
				keys = append(keys, docID(a.WorkspaceID(), s.Remotion.Version))
			}
			ids[s.Remotion.Version] = s.Remotion
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var vs []models.RemotionVersion
	if err := a.DB.Find(ctx, store.CRemotionVersions, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": keys}}, &vs); err != nil {
		return err
	}
	state := map[string]string{}
	for _, v := range vs {
		state[v.Hash] = v.State
	}
	for hash, rc := range ids {
		st := state[hash]
		if st == models.RemotionApproved {
			continue
		}
		why := "no person has approved it"
		switch st {
		case models.RemotionRevoked, "":
			why = "its approval was withdrawn"
		case models.RemotionRejected:
			why = "a person turned it down"
		}
		name := rc.SourceTitle
		if name == "" {
			name = rc.SourceName
		}
		return apperr.New(apperr.Validation, "This cut plays “%s” from version %d of the Remotion project “%s”, and %s, so it can't be rendered.", rc.Composition, rc.VersionNumber, name, why).
			WithHint("A person can approve that version again under Sources in the dashboard, or the cut's clips can move to an approved version.").
			WithDetails(map[string]any{"reason": "remotion_not_approved", "version": hash, "state": st}).
			WithCommand("metarush remotion update " + rc.SourceName + " --project <project>")
	}
	return nil
}
