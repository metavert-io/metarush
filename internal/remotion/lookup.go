package remotion

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Cut sheets name compositions as `remotion <source> <composition>[@version]`
// (D135); checking the sheet finds the clip MetaRush made of exactly that.

func init() {
	ops.RemotionClipLookup = lookupClip
	ops.RemotionRenderCheck = checkRevision
}

// checkRevision refuses a new render of a cut whose clips come from a
// Remotion version no person approved (the worker checks nested cuts too).
func checkRevision(ctx context.Context, a *app.App, rev *models.Revision) error {
	seen := map[string]bool{}
	var ids []string
	for _, c := range rev.Spec.Clips {
		if c.AssetID != "" && !seen[c.AssetID] {
			seen[c.AssetID] = true
			ids = append(ids, c.AssetID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}, "source.platform": models.PlatformRemotion}, &assets); err != nil {
		return err
	}
	return RequireApproved(ctx, a, assets)
}

func lookupClip(ctx context.Context, a *app.App, projectID, source, composition, version, props string) cutsheet.RemotionResolved {
	s, err := findSource(ctx, a, projectID, source)
	if err != nil {
		return cutsheet.RemotionResolved{Code: "remotion_unknown", Message: apperrMessage(err), Hint: "The Remotion projects and their compositions are listed under Sources."}
	}
	if version == "" && s.Approved == "" {
		return cutsheet.RemotionResolved{Code: "remotion_not_approved", Message: fmt.Sprintf("No version of “%s” is approved yet, so its compositions can't go into a cut.", s.Title),
			Hint: "A person looks at it and approves it under Sources in the dashboard."}
	}
	v, err := resolveVersion(ctx, a, s, version)
	if err != nil {
		code := "remotion_unknown"
		if ae := apperr.From(err); ae.Code == apperr.Conflict {
			code = "remotion_ambiguous"
		}
		return cutsheet.RemotionResolved{Code: code, Message: apperrMessage(err), Hint: "Use its number (like @3) or more of its version."}
	}
	if v.State != models.RemotionApproved {
		return cutsheet.RemotionResolved{Code: "remotion_not_approved", Message: fmt.Sprintf("Version %d of “%s” isn't approved (%s), so its compositions can't go into a cut.", v.Number, s.Title, stateLabel(v.State, v.PackagesAllowed)),
			Hint: "A person looks at it and approves it under Sources in the dashboard."}
	}
	c := v.Composition(composition)
	if c == nil {
		var ids []string
		for _, x := range v.Compositions {
			ids = append(ids, x.ID)
		}
		sort.Strings(ids)
		return cutsheet.RemotionResolved{Code: "remotion_unknown", Message: fmt.Sprintf("Version %d of “%s” has no composition called “%s”.", v.Number, s.Title, composition),
			Hint: "It has " + strings.Join(ids, ", ") + "."}
	}
	words, wn := wordsFor(s, v, c.ID)
	pics, _, _ := picturesFor(s, v, c.ID, nil)
	key := renderKey(s, v, c.ID, props, nil, variant{Words: words, WordsNumber: wn, Pictures: pics})
	r, err := getRender(ctx, a, key)
	if err == nil && r.State == models.RemotionRenderDone && r.AssetID != "" {
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), r.AssetID, &as) == nil && as.State == models.AssetAvailable {
			return cutsheet.RemotionResolved{AssetID: as.ID, Duration: as.Media.Duration, Name: clipID(c.ID, "", nil) + fmt.Sprintf("-v%d", v.Number)}
		}
	}
	msg := fmt.Sprintf("MetaRush hasn't made “%s” (version %d of “%s”) into a clip yet.", c.ID, v.Number, s.Title)
	if err == nil && (r.State == models.RemotionRenderQueued || r.State == models.RemotionRenderRendering) {
		msg = fmt.Sprintf("MetaRush is making “%s” (version %d of “%s”) into a clip now.", c.ID, v.Number, s.Title)
	}
	return cutsheet.RemotionResolved{Code: "remotion_not_made", Message: msg,
		Hint: "Make it into a clip first (Use in the cut under Sources, or render it), then check the sheet again."}
}
