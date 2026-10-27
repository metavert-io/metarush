package worker

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// "File ready" is completed by the clip's derivative jobs; a later look at the
// clip (what's on screen, scene descriptions) does not send it again, even when
// the clip was renamed in between.
func TestLookingAtAClipDoesNotResendFileReady(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	user := models.Actor{UserID: "usr_test", UserName: "Ada"}
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "garden-chat.mp4", State: models.AssetAvailable,
		CreatedBy: user, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	job := func(typ string) *models.Job {
		f := now
		j := &models.Job{ID: ids.New(ids.Job), WorkspaceID: a.WorkspaceID(), Type: typ, State: models.JobSucceeded, AssetID: as.ID, CreatedBy: user,
			Payload: map[string]any{}, Attempts: []models.Attempt{}, IdempotencyKey: ids.New("k"), MaxAttempts: 3, CreatedAt: now, UpdatedAt: now, NextRunAt: now, FinishedAt: &f}
		if err := a.DB.Insert(ctx, store.CJobs, *j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	w := New(a)
	w.settled(ctx, job(jobs.TypeProxy), models.JobSucceeded, map[string]any{})
	ready := func() []models.Notification {
		var ns []models.Notification
		if err := a.DB.Find(ctx, store.CNotifications, a.WorkspaceID(), bson.M{"kind": models.NotifyFileReady, "assetId": as.ID}, &ns); err != nil {
			t.Fatal(err)
		}
		return ns
	}
	ns := ready()
	if len(ns) != 1 || ns[0].Title != "garden-chat is ready" {
		t.Fatalf("file ready once the derivatives are done: %+v", ns)
	}
	// Read, then the clip is renamed and looked at.
	if _, err := a.DB.C(store.CNotifications).UpdateOne(ctx, bson.M{"_id": ns[0].ID}, bson.M{"$set": bson.M{"readAt": now}}); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"title": "Garden chat"}}); err != nil {
		t.Fatal(err)
	}
	w.settled(ctx, job(jobs.TypeVisual), models.JobSucceeded, map[string]any{})
	w.settled(ctx, job(jobs.TypeDescribe), models.JobSucceeded, map[string]any{})
	if ns := ready(); len(ns) != 1 || ns[0].ReadAt == nil || ns[0].Title != "garden-chat is ready" {
		t.Fatalf("not sent again: %+v", ns)
	}
}
