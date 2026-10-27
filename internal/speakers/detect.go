package speakers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// CatchUpVersion versions the "find voices in clips that never had speaker
// detection" catch-up: a new version queues detection again for clips still
// without voices (idempotency keys carry it).
const CatchUpVersion = 1

// NeedsDetection is the query for clips with sound and a finished transcript
// (with speech) but no speaker detection: imported before MetaRush found
// voices, or transcribed where speaker detection was not installed.
func NeedsDetection() bson.M {
	return bson.M{"state": models.AssetAvailable, "media.hasAudio": true, "transcript.state": "available", "transcript.segments": bson.M{"$gt": 0},
		"$or": bson.A{bson.M{"speakers": bson.M{"$exists": false}}, bson.M{"speakers": nil}, bson.M{"speakers.state": "unavailable"}}}
}

// Queued is one clip's speaker detection job.
type Queued struct {
	AssetID string `json:"assetId"`
	Title   string `json:"title"`
	JobID   string `json:"jobId"`
	// Created is false when the clip already had a detection waiting or running.
	Created bool `json:"created"`
}

// QueueOptions say why detection is queued.
type QueueOptions struct {
	// CatchUp keys the jobs as the catch-up after an upgrade (one job per clip
	// per catch-up version, never duplicated).
	CatchUp bool
	// Priority of the jobs (heavy, so the battery policy applies).
	Priority int
}

// QueueDetection queues speaker detection for clips (a heavy job: it waits on
// battery like transcription, shows in the activity bar and tells the person
// who started it when it is done). A clip that already has a detection queued
// or running keeps that one.
func QueueDetection(ctx context.Context, a *app.App, assets []models.Asset, o QueueOptions) ([]Queued, error) {
	if o.Priority == 0 {
		o.Priority = 9
	}
	out := []Queued{}
	for _, as := range assets {
		var open models.Job
		err := a.DB.C(store.CJobs).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": as.ID, "type": bson.M{"$in": bson.A{jobs.TypeDiarize, jobs.TypeTranscribe}},
			"state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}}).Decode(&open)
		if err == nil {
			out = append(out, Queued{AssetID: as.ID, Title: as.DisplayTitle(), JobID: open.ID})
			continue
		}
		key := fmt.Sprintf("%s:%s:%s", jobs.TypeDiarize, as.ID, strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000"), ".", ""))
		if o.CatchUp {
			key = fmt.Sprintf("%s:catchup@%d:%s", jobs.TypeDiarize, CatchUpVersion, as.ID)
		}
		j, created, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeDiarize, IdempotencyKey: key, Priority: o.Priority, Heavy: true, AssetID: as.ID,
			Payload: map[string]any{"assetId": as.ID}, Actor: a.Actor})
		if err != nil {
			return out, err
		}
		out = append(out, Queued{AssetID: as.ID, Title: as.DisplayTitle(), JobID: j.ID, Created: created})
	}
	return out, nil
}

// MissingDetection lists the clips that never had speaker detection.
func MissingDetection(ctx context.Context, a *app.App) ([]models.Asset, error) {
	var out []models.Asset
	err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), NeedsDetection(), &out,
		options.Find().SetProjection(bson.M{"name": 1, "title": 1, "projectIds": 1, "media": 1, "transcript": 1, "speakers": 1, "state": 1}).SetSort(bson.D{{Key: "createdAt", Value: 1}}))
	return out, err
}
