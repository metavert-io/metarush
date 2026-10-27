package worker

import (
	"context"
	"errors"
	"runtime"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/vision"
)

// On a Mac, Remove the background finds the subject of a photo with Apple
// Vision (the on-screen helper's mask command, macOS 14 or later); elsewhere
// only a plain-coloured background can be removed.
func init() {
	if runtime.GOOS != "darwin" {
		return
	}
	fixes.SegmentSubject = func(ctx context.Context, a *app.App, in, out string) error {
		if _, _, err := vision.Ensure(ctx, a.Tools, a.Cfg.Home()); err != nil {
			return err
		}
		found, err := vision.Mask(ctx, a.Tools, in, out)
		if err != nil {
			return err
		}
		if !found {
			return fixes.ErrNoSubject
		}
		return nil
	}
}

// inspectHandler measures a clip for its fixes (internal/fixes, D153).
func inspectHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	return fixesErr(fixes.Inspect(ctx, w.App, j, func(p float64, m string) { report(p, m) }))
}

// prepareHandler makes a fixed copy of a clip or a picture (internal/fixes, D152).
func prepareHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	return fixesErr(fixes.Prepare(ctx, w.App, j, func(p float64, m string) { report(p, m) }))
}

// fixesErr makes a fix's final error permanent (no retry).
func fixesErr(res map[string]any, err error) (map[string]any, error) {
	var fe *fixes.FinalError
	if errors.As(err, &fe) {
		return nil, &Permanent{Err: err}
	}
	return res, err
}

// storeVariant keeps a derived file (preview copy, thumbnail, sound wave,
// steady copy) with the media it was made from (D152). as is the asset as it
// was when the job read its file: while the asset still uses that media the
// variant is the asset's own; when a fix switched it meanwhile, the variant
// goes where that media's variants are kept (the original's set aside, or the
// fixed copy's record), so a switch never mixes two files' previews.
func storeVariant(ctx context.Context, a *app.App, as *models.Asset, v *models.Variant) error {
	now := time.Now().UTC()
	coll := a.DB.C(store.CAssets)
	if c := as.Fixes; c != nil && c.Copy != nil {
		res, err := coll.UpdateOne(ctx, bson.M{"_id": as.ID, "workspaceId": a.WorkspaceID(), "fixes.copy.sha256": c.Copy.SHA256},
			bson.M{"$set": bson.M{"variants." + v.Kind: v, "updatedAt": now}})
		if err != nil || res.MatchedCount > 0 {
			return err
		}
		// The source switched away from this copy: the variant stays with the copy.
		_, err = a.DB.C(store.CSourceCopies).UpdateOne(ctx, bson.M{"_id": c.Copy.ID, "workspaceId": a.WorkspaceID()},
			bson.M{"$set": bson.M{"variants." + v.Kind: v, "updatedAt": now}})
		return err
	}
	res, err := coll.UpdateOne(ctx, bson.M{"_id": as.ID, "workspaceId": a.WorkspaceID(), "fixes.copy": bson.M{"$in": bson.A{nil}}},
		bson.M{"$set": bson.M{"variants." + v.Kind: v, "updatedAt": now}})
	if err != nil || res.MatchedCount > 0 {
		return err
	}
	// A fixed copy became current meanwhile: this is the original's.
	_, err = coll.UpdateOne(ctx, bson.M{"_id": as.ID, "workspaceId": a.WorkspaceID(), "fixes.copy": bson.M{"$ne": nil}},
		bson.M{"$set": bson.M{"fixes.originalVariants." + v.Kind: v, "updatedAt": now}})
	return err
}
