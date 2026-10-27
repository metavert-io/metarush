package remotion

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
)

// Work on a source that runs its code in the jail (D167–D171, D209) is one job type
// (remotion-task): its payload says which task, and says it in plain words.

// Tasks.
const (
	taskMap        = "map"
	taskWordsSetup = "words-setup"
	taskWords      = "words"
	taskLift       = "lift"
	// Pictures as settings (D209).
	taskPicturesSetup = "pictures-setup"
	taskPictures      = "pictures"
	taskPicturesRead  = "pictures-read"
)

// queueTask queues a remotion-task job (heavy; any machine with Node.js).
func queueTask(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, task, what string, extra map[string]any, idem string) (*models.Job, error) {
	pl := map[string]any{"task": task, "source": s.ID, "version": v.Hash, "versionNumber": v.Number, "title": upperFirst(what), "what": what}
	for k, x := range extra {
		pl[k] = x
	}
	if idem == "" {
		idem = "remotion-task:" + task + ":" + v.Hash + ":" + time.Now().UTC().Format("20060102T150405.000")
	}
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeRemotionTask, Payload: pl, IdempotencyKey: idem, Priority: 20, Heavy: true, MaxAttempts: 2,
		ProjectID: s.ProjectID, Actor: a.Actor})
	return j, err
}

// RunTask runs a remotion-task job.
func RunTask(ctx context.Context, a *app.App, j *models.Job, report func(pct float64, msg string)) (map[string]any, error) {
	if report == nil {
		report = func(float64, string) {}
	}
	str := func(k string) string { x, _ := j.Payload[k].(string); return x }
	ra := *a
	ra.Actor = j.CreatedBy // the person who asked, not the worker
	a = &ra
	s, err := getSource(ctx, a, str("source"))
	if err != nil {
		return nil, &Final{Err: err}
	}
	v, err := getVersion(ctx, a, str("version"))
	if err != nil {
		return nil, &Final{Err: err}
	}
	switch str("task") {
	case taskMap:
		return runMapTask(ctx, a, s, v, j, report)
	case taskWordsSetup:
		return runWordsSetup(ctx, a, s, v, j, report)
	case taskWords:
		return runWordsTask(ctx, a, s, v, j, report)
	case taskLift:
		return runLiftTask(ctx, a, s, v, j, report)
	case taskPicturesSetup:
		return runPicturesSetup(ctx, a, s, v, j, report)
	case taskPictures:
		return runPicturesTask(ctx, a, s, v, j, report)
	case taskPicturesRead:
		return runPicturesRead(ctx, a, s, v, report)
	}
	return nil, finalf("MetaRush doesn't know that kind of work on a Remotion project.")
}

// runMapTask makes a version's map.
func runMapTask(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, j *models.Job, report func(float64, string)) (map[string]any, error) {
	var comps []string
	if xs, ok := j.Payload["compositions"].(bson.A); ok {
		for _, x := range xs {
			if c, ok := x.(string); ok {
				comps = append(comps, c)
			}
		}
	}
	doc, err := buildMap(ctx, a, s, v, mapOptions{Compositions: comps, Say: report})
	if err != nil {
		final := IsFinal(err) || len(j.Attempts) >= j.MaxAttempts
		if final {
			_ = setVersion(ctx, a, v.Hash, bson.M{"map": models.RemotionMapRef{State: models.RemotionMapFailed, JobID: j.ID, At: time.Now().UTC(), Machine: a.Cfg.Installation.Name, Problem: apperrMessage(err)}})
		}
		var er *errRun
		if asErrRun(err, &er) && er.slow() {
			return nil, err
		}
		return nil, &Final{Err: err}
	}
	scenes := 0
	for _, c := range doc.Compositions {
		scenes += len(c.Scenes)
	}
	msg := fmt.Sprintf("Made the map of “%s” (version %d): %s, %s.", s.Title, v.Number, plural(len(doc.Compositions), "composition", "compositions"), plural(scenes, "scene", "scenes"))
	report(1, msg)
	return map[string]any{"message": msg, "compositions": len(doc.Compositions), "scenes": scenes, "versionNumber": v.Number, "projectId": s.ProjectID, "sourceId": s.ID}, nil
}

// requestMap makes sure a version's map is made or being made; it returns the
// job when one is queued or running.
func requestMap(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, again bool) (*models.Job, error) {
	if v.Map != nil && v.Map.State == models.RemotionMapMaking && v.Map.JobID != "" {
		if j, err := a.Queue.Get(ctx, v.Map.JobID); err == nil && (j.State == models.JobQueued || j.State == models.JobRunning) {
			return j, nil
		}
	}
	if v.Map != nil && v.Map.State == models.RemotionMapDone && !again {
		return nil, nil
	}
	if !v.PackagesAllowed {
		return nil, nil // nothing can run before a person allows its packages
	}
	j, err := queueTask(ctx, a, s, v, taskMap, fmt.Sprintf("making the map of “%s” (version %d)", s.Title, v.Number), nil, "")
	if err != nil {
		return nil, err
	}
	ref := models.RemotionMapRef{State: models.RemotionMapMaking, JobID: j.ID, At: time.Now().UTC()}
	if err := setVersion(ctx, a, v.Hash, bson.M{"map": ref}); err != nil {
		return nil, err
	}
	v.Map = &ref
	return j, nil
}

// lowerWords is a short plain phrase for a composition list.
func lowerWords(xs []string) string { return strings.Join(xs, ", ") }
