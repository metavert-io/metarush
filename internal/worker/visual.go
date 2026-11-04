package worker

import (
	"context"
	"fmt"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/looker"
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
// second plus the moments the picture changes, with this computer's looker —
// Apple Vision on a Mac, the open models elsewhere (D405, D408) — into the
// clip's on-screen index, its faces and "Is this …?" questions.
func visualHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (res map[string]any, err error) {
	a := w.App
	as, err := loadAsset(ctx, a, j)
	if err != nil {
		return nil, err
	}
	if !as.Media.HasVideo {
		return nil, permanent("“%s” has no picture to look at", as.DisplayTitle())
	}
	var keep *visual.Index
	sameMedia := false
	if looker.Chosen(a.Cfg) == looker.OpenModels {
		// What looked at the very media the clip uses now: a fixed copy's
		// index, or the original's — never the original's in a copy's place
		// (D467).
		mine, err := visual.LoadMedia(ctx, a.DB, a.WorkspaceID(), as.ID, as.MediaSHA())
		if err != nil {
			return nil, err
		}
		if mine != nil && mine.Index != nil && !mine.Index.ByOpenModels() {
			// Apple Vision looked at this media before (D447): a Mac looks again
			// while one is about; after a long while without one, this computer
			// does and keeps what Apple Vision saw that the open models don't.
			if wait := macAgain(ctx, w, j); wait != nil {
				return nil, wait
			}
		} else if wait := macFirst(ctx, w, j); wait != nil {
			// A Mac in the workspace sees more (D410): it gets the first go, also
			// at a fixed copy whose original Apple Vision looked at (D467).
			return nil, wait
		}
		if mine != nil {
			keep, sameMedia = mine.Index, true
		} else if prev := visual.Load(ctx, a.DB, a.WorkspaceID(), as.ID); prev != nil {
			// A fixed copy's first look: the original's labels come over, not
			// where things were in its picture (D467).
			keep = prev.Index
		}
	}
	lk, st, built, err := looker.Open(ctx, a.Cfg, a.Tools)
	if err != nil {
		// This computer can't look after all (left out by a person, a helper
		// that didn't build, models changed after they were added): it stops
		// offering to until its components or settings change, and the clip
		// goes back for another computer that can (D446).
		w.stopOffering(jobs.TypeVisual)
		if lookerElsewhere(ctx, w) {
			return nil, &released{waiting: "Waiting for another computer in this workspace to look at what's on screen: this one can't now."}
		}
	}
	if apperr.Is(err, apperr.ToolMissing) {
		return nil, &Permanent{Err: err} // left out by a person, or not added here: its own words and the one step (D325)
	}
	if err != nil {
		if st.Backend == looker.AppleVision {
			return nil, permanent("the on-screen helper could not be built: %v", err)
		}
		return nil, &Permanent{Err: err}
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
	}, visual.RunOptions{Prints: facePrinter(ctx, w, lk), Looker: lk})
	if err != nil {
		return nil, err
	}
	idx.KeepFrom(keep, sameMedia)
	report(0.92, "saving")
	// The index belongs to the media it looked at (D152): a fixed copy's, or the original's.
	_, mediaSHA, _ := as.CurrentMedia()
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind, Provider: lk.Backend(), Model: lk.Model(),
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
		"summary": idx.Summary, "questions": asked, "recognized": len(recognized), "fingerprints": printed(idx), "lookedWith": lk.Backend()}
	return res, nil
}

// facePrinter is this machine's face fingerprinting (D114): onnxruntime from
// the sherpa-onnx install and the face model, downloaded the first time. Where
// either is missing faces are still found, only not recognized across clips.
// Apple Vision gives the five points only from a helper of this protocol; the
// open models always do.
func facePrinter(ctx context.Context, w *Worker, lk looker.Looker) visual.Printer {
	a := w.App
	lib := onnx.Library(a.Tools.Path(tools.Sherpa))
	if lib == "" || (lk.Backend() == looker.AppleVision && !vision.Rich(ctx, a.Tools)) {
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

// screenIndex is how this computer looks at clips, when it does (D408).
func (w *Worker) screenIndex() string {
	if !w.Can(jobs.TypeVisual) {
		return ""
	}
	return looker.Chosen(w.App.Cfg)
}

// A Mac in the workspace sees more in a clip than the open models do
// (labels, animals, bodies and gestures, text in any language; D410), so a
// computer with the open models leaves a clip to such a Mac for a while: while
// one that takes big jobs has been seen in the last couple of minutes, the
// job waits, at most macWaits times macWait, then this computer looks.
const (
	macWait  = 2 * time.Minute
	macWaits = 5
	macSeen  = 2 * time.Minute
)

// macFirst puts the job back for a moment while a Mac that looks with Apple
// Vision is about (nil: look here now).
func macFirst(ctx context.Context, w *Worker, j *models.Job) error {
	if jobs.Postponed(j) >= macWaits {
		return nil
	}
	a := w.App
	f := canLook(a, macSeen)
	f["screenIndex"], f["acceptsHeavy"] = looker.AppleVision, true
	n, err := a.DB.C(store.CWorkers).CountDocuments(ctx, f)
	if err != nil || n == 0 {
		return nil
	}
	return &postponed{after: macWait, reason: "A Mac in this workspace sees more in a clip (things, animals, gestures, words in more languages), so it gets the first look; this computer looks if it hasn't in a few minutes."}
}

// An index Apple Vision made is looked at again only by a Mac while one that
// looks was seen in the workspace this recently (D447); the job waits
// macWait, then twice as long each time, at most appleWait.
const (
	appleSeen = 30 * 24 * time.Hour
	appleWait = 3 * time.Hour
)

// macAgain puts back a clip Apple Vision looked at before while a Mac that
// looks has been seen in the last 30 days (nil: look here, keeping what Apple
// Vision saw).
func macAgain(ctx context.Context, w *Worker, j *models.Job) error {
	f := canLook(w.App, appleSeen)
	f["screenIndex"] = looker.AppleVision
	n, err := w.App.DB.C(store.CWorkers).CountDocuments(ctx, f)
	if err != nil || n == 0 {
		return nil
	}
	after := min(appleWait, macWait<<min(jobs.Postponed(j), 7))
	return &postponed{after: after, reason: "Apple Vision looked at this clip before and sees more in it (things, animals, gestures), so a Mac in this workspace looks at it again."}
}

// canLook finds the other computers of the workspace that look at clips — they
// offer the visual job and lack nothing for it (D446) — seen within a while
// (the database server's time).
func canLook(a *app.App, seen time.Duration) bson.M {
	f := store.SeenWithin("lastHeartbeat", seen)
	f["workspaceId"], f["jobTypes"] = a.WorkspaceID(), jobs.TypeVisual
	f["lacks.jobType"] = bson.M{"$ne": jobs.TypeVisual}
	f["_id"] = bson.M{"$ne": a.Cfg.Installation.ID}
	return f
}

// elsewhereSeen is how recently another computer that looks must have been
// seen for a clip this one can't look at to wait for it (a Mac asleep for the
// night still counts).
const elsewhereSeen = 24 * time.Hour

// lookerElsewhere reports another computer in the workspace that can look at
// clips (D446).
func lookerElsewhere(ctx context.Context, w *Worker) bool {
	n, err := w.App.DB.C(store.CWorkers).CountDocuments(ctx, canLook(w.App, elsewhereSeen))
	return err == nil && n > 0
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
