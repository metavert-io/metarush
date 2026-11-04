package worker

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/looker"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Remove the background finds the subject of a photo with this computer's
// cut-out engine (D414): Apple Vision on a Mac (the on-screen helper's mask
// command, macOS 14 or later), BiRefNet where the open models are this
// computer's way of looking and the cut-out model is added; elsewhere only a
// plain-coloured background can be removed, and the copy says what to add.
func init() {
	fixes.SegmentSubject = func(ctx context.Context, a *app.App, in, out string) (string, error) {
		if err := looker.CutOutReady(a.Cfg, a.Tools); err != nil {
			return "", &fixes.ErrCantSegment{Why: cantCutOut(a)}
		}
		found, err := looker.CutOut(ctx, a.Cfg, a.Tools, in, out)
		if err != nil {
			return "", err
		}
		if !found {
			return "", fixes.ErrNoSubject
		}
		if looker.Chosen(a.Cfg) == looker.AppleVision {
			return "Apple Vision", nil
		}
		return "BiRefNet", nil
	}
}

// cantCutOut says why this computer can't cut a subject out, to end a sentence.
func cantCutOut(a *app.App) string {
	if looker.Chosen(a.Cfg) == looker.AppleVision {
		return "the on-screen helper isn't set up on this Mac (Seeing what's on screen, in Settings → Components)"
	}
	return "the cut-out model isn't on this computer (Cutting out a picture's subject, in Settings → Components)"
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
