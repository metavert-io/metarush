package worker

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// describeHandler has this machine's Claude Code describe a clip's scenes
// (D74) and keeps them as the clip's searchable descriptions.
func describeHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (res map[string]any, err error) {
	a := w.App
	as, err := loadAsset(ctx, a, j)
	if err != nil {
		return nil, err
	}
	projectID, _ := j.Payload["projectId"].(string)
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"analysis.describe": "running"}})
	defer func() {
		if err != nil {
			_ = a.DB.Update(context.WithoutCancel(ctx), store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"analysis.describe": "failed"}})
		}
	}()
	report(0.1, "Claude Code is looking at the clip")
	if ops.DescribeRunner == nil {
		return nil, permanent("this program cannot run Claude Code; the dashboard's daemon or the metarush command can")
	}
	out, err := ops.DescribeRunner(ctx, a, as, projectID)
	if err != nil {
		return nil, &Permanent{Err: err}
	}
	report(0.9, "saving")
	n, err := ops.SaveDescriptions(ctx, a, as, out.Scenes, "claude-code", out.Model)
	if err != nil {
		return nil, &Permanent{Err: fmt.Errorf("the scene descriptions Claude Code wrote were not usable: %w", err)}
	}
	return map[string]any{"scenes": n, "conversationId": out.ConversationID}, nil
}

// visualHandler looks at what's on screen in a clip (D73): about one frame a
// second plus the moments the picture changes, through Apple Vision on this
// Mac, into the clip's on-screen index, its faces and "Is this …?" questions.
func visualHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (res map[string]any, err error) {
	a := w.App
	as, err := loadAsset(ctx, a, j)
	if err != nil {
		return nil, err
	}
	if !as.Media.HasVideo {
		return nil, permanent("“%s” has no picture to look at", as.DisplayTitle())
	}
	st, built, err := vision.Ensure(ctx, a.Tools, a.Cfg.Home())
	if apperr.Is(err, apperr.ToolMissing) {
		return nil, &Permanent{Err: err} // left out by a person: its own words and the one step (D325)
	}
	if err != nil {
		return nil, permanent("the on-screen helper could not be built: %v", err)
	}
	if !st.Ready {
		return nil, permanent("%s %s", st.Text, st.Fix)
	}
	if built {
		w.Log("built the on-screen helper (%s)", st.Path)
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"analysis.visual": "running"}})
	defer func() {
		if err != nil {
			_ = a.DB.Update(context.WithoutCancel(ctx), store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"analysis.visual": "failed"}})
		}
	}()
	src, srcName := "", "original"
	if px, ok := as.Variants[models.VariantProxy]; ok {
		if p, err := a.Cache.Ensure(ctx, a.Media, px.StorageKey, px.SHA256, px.Size, nil); err == nil {
			src, srcName = p, "proxy"
		}
	}
	if src == "" {
		if src, err = original(ctx, a, as, report); err != nil {
			return nil, err
		}
	}
	dir, err := scratchDir(a, j)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	// The moments the picture changes get a frame of their own, and faces are not followed across cuts.
	var extra, cuts []float64
	if ev := evidence.LoadStored(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]; ev != nil {
		if ev.VisualChanges != nil {
			for _, c := range ev.VisualChanges {
				if c.Kind == evidence.ChangeCut {
					cuts = append(cuts, c.At)
				}
				extra = append(extra, c.Frames...)
			}
		} else {
			for _, sc := range ev.Scenes {
				cuts = append(cuts, sc.At)
				extra = append(extra, sc.At+0.4)
			}
		}
	}
	report(0.05, "looking at what's on screen")
	idx, err := visual.Run(ctx, a.Tools, src, as.Media.Duration, extra, cuts, srcName, dir, func(done, total int) {
		report(0.05+0.85*float64(done)/float64(max(1, total)), fmt.Sprintf("looked at %d of %d frames", done, total))
	}, visual.RunOptions{Prints: facePrinter(ctx, w)})
	if err != nil {
		return nil, err
	}
	report(0.92, "saving")
	model := fmt.Sprintf("metarush-vision %s", st.Protocol)
	// The index belongs to the media it looked at (D152): a fixed copy's, or the original's.
	_, mediaSHA, _ := as.CurrentMedia()
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind, Provider: "apple-vision", Model: model,
		PromptVersion: visual.Version, Digest: mediaSHA + ":" + visual.Version, Data: visual.ToMap(idx), CreatedAt: time.Now().UTC(), Media: as.MediaSHA()}
	old := store.SameMedia(as.ID, as.MediaSHA())
	old["workspaceId"], old["kind"] = a.WorkspaceID(), visual.InsightKind
	if _, err := a.DB.C(store.CInsights).DeleteMany(ctx, old); err != nil {
		return nil, err
	}
	if err := a.DB.Insert(ctx, store.CInsights, ins); err != nil {
		return nil, err
	}
	if err := faces.Save(ctx, a, as.ID, idx); err != nil {
		return nil, err
	}
	asked, recognized, err := faces.Recognize(ctx, a, as.ID)
	if err != nil {
		w.Log("face questions for %s: %v", as.ID, err)
	}
	for _, pid := range recognized {
		notify.RecognizedFaces(ctx, a, j.CreatedBy.UserID, pid)
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"analysis.visual": "available", "updatedAt": time.Now().UTC()}})
	res = map[string]any{"insightId": ins.ID, "frames": idx.Frames, "faces": len(idx.Tracks), "text": len(idx.Text), "labels": len(idx.Labels),
		"summary": idx.Summary, "questions": asked, "recognized": len(recognized), "fingerprints": printed(idx)}
	return res, nil
}

// facePrinter is this machine's face fingerprinting (D114): onnxruntime from
// the sherpa-onnx install and the face model, downloaded the first time. Where
// either is missing faces are still found, only not recognized across clips.
func facePrinter(ctx context.Context, w *Worker) visual.Printer {
	a := w.App
	lib := onnx.Library(a.Tools.Path(tools.Sherpa))
	if lib == "" || !vision.Rich(ctx, a.Tools) {
		return nil
	}
	model, err := faceid.Setup{Home: a.Cfg.Home()}.Ensure(ctx)
	if err != nil {
		w.Log("recognizing faces is not set up: %v", err)
		return nil
	}
	e, err := faceid.Open(lib, model)
	if err != nil {
		w.Log("recognizing faces is not set up: %v", err)
		return nil
	}
	return e
}

// printed counts the faces that got fingerprints.
func printed(idx *visual.Index) int {
	n := 0
	for _, t := range idx.Tracks {
		if len(t.Prints) > 0 {
			n++
		}
	}
	return n
}
