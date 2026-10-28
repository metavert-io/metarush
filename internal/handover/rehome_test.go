package handover_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/handover"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

func queuedJob(t *testing.T, c *computer, typ string, payload map[string]any) string {
	t.Helper()
	j, _, err := c.a.Queue.Enqueue(context.Background(), jobs.EnqueueOptions{Type: typ, Actor: c.a.Actor, Installation: c.a.Cfg.Installation.ID,
		IdempotencyKey: ids.New("tst"), MaxAttempts: 3, Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	return j.ID
}

func pinnedTo(t *testing.T, w *workspace, job string) string {
	t.Helper()
	var j models.Job
	if err := w.db.GetByID(context.Background(), store.CJobs, w.id, job, &j); err != nil {
		t.Fatal(err)
	}
	return j.Installation
}

// The receiving computer slept after taking the conversations, before the
// work moved, and the old computer took it over meanwhile: when it wakes, its
// receive takes no role and moves nothing — the old computer's work stays
// with it, never on a computer that does no work of its own.
func TestAReceiveAfterATakeOverMovesNoWork(t *testing.T) {
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	talk(t, old.at(t, remembers()), "", "A conversation.")
	job := queuedJob(t, old, jobs.TypeFetch, nil)
	pctx := handover.WithPassphrase(ctx, pass)
	out := filepath.Join(t.TempDir(), "h.age")
	call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	nu.at(t, remembers())
	defer handover.WhileReceiving(func(string) {
		ago := bson.D{{Key: "$subtract", Value: bson.A{"$$NOW", int64(2 * time.Hour / time.Millisecond)}}}
		_, _ = w.db.C(store.CInstallations).UpdateOne(ctx, bson.M{"_id": nu.a.Cfg.Installation.ID},
			mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "lastSeenAt", Value: ago}, {Key: "lastUsedAt", Value: ago}}}}})
		call[handover.HandoverTakeoverOut](t, ctx, old.a, "machine.takeover", handover.HandoverTakeoverIn{From: "desk", ConfirmedOff: "desk", Folders: []handover.HandoverFolderChoice{}})
	})()
	_, err := ops.CallTyped[handover.HandoverReceiveOut](pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out, Folders: []handover.HandoverFolderChoice{}}, person())
	refused(t, err, apperr.Conflict, "")
	if got := pinnedTo(t, w, job); got != old.a.Cfg.Installation.ID {
		t.Fatalf("the laptop's queued work moved to the desk: %s", got)
	}
	if r, _ := handover.RoleOf(ctx, nu.a, nu.a.Cfg.Installation.ID); r.Role != handover.RoleClient {
		t.Fatalf("the desk was taken over: %+v", r)
	}
	if r, _ := handover.RoleOf(ctx, old.a, old.a.Cfg.Installation.ID); r.Role != handover.RoleHost {
		t.Fatalf("the laptop is the host: %+v", r)
	}
}

// A receive that stopped right after taking the role: its MetaRush in the
// background moves the work and sets the folders the person placed, once;
// the old computer steps down only when nothing of its own is left.
func TestAReceiveStoppedAfterTakingTheRoleIsFinished(t *testing.T) {
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	talk(t, old.at(t, remembers()), "", "A conversation.")
	inbox, placed := t.TempDir(), t.TempDir()
	raw, _ := json.Marshal(map[string]any{"dir": inbox})
	if _, err := ops.Call(ctx, old.a, "watch.set", raw, person()); err != nil {
		t.Fatal(err)
	}
	job := queuedJob(t, old, jobs.TypeFetch, nil)
	pctx := handover.WithPassphrase(ctx, pass)
	out := filepath.Join(t.TempDir(), "h.age")
	ex := call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	if ex.Folders != 1 {
		t.Fatalf("the watched folder travels: %+v", ex)
	}
	nu.at(t, remembers())
	stop := handover.StopAfterReceiving()
	_, err := ops.CallTyped[handover.HandoverReceiveOut](pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out,
		Folders: []handover.HandoverFolderChoice{{Path: inbox, To: placed}}}, person())
	stop()
	if err == nil {
		t.Fatal("the receive was meant to stop")
	}
	var rec handover.HandoverRecord
	if err := w.db.GetByID(ctx, store.CHandovers, w.id, ex.Handover.ID, &rec); err != nil || rec.State != handover.StateReceived || rec.WorkMovedAt != nil {
		t.Fatalf("the role was taken, the work not moved yet: %+v %v", rec, err)
	}
	if pinnedTo(t, w, job) != old.a.Cfg.Installation.ID {
		t.Fatal("work moved before the step")
	}
	// The desk's MetaRush in the background finishes it.
	if role, _, err := handover.Look(ctx, nu.a); err != nil || role.Role != handover.RoleHost {
		t.Fatalf("the desk's look: %+v %v", role, err)
	}
	if pinnedTo(t, w, job) != nu.a.Cfg.Installation.ID {
		t.Fatal("the look didn't move the work")
	}
	cfg, err := config.Load(nu.a.Cfg.Home())
	if err != nil || cfg.Worker.Watch.Dir != placed {
		t.Fatalf("the folder wasn't set where the person placed it: %q %v", cfg.Worker.Watch.Dir, err)
	}
	if err := w.db.GetByID(ctx, store.CHandovers, w.id, ex.Handover.ID, &rec); err != nil || rec.WorkMovedAt == nil || rec.Moved.Jobs != 1 || rec.Moved.Folders != 1 {
		t.Fatalf("recorded once: %+v %v", rec.Moved, err)
	}
	if _, _, err := handover.Look(ctx, nu.a); err != nil {
		t.Fatal(err)
	}
	if err := w.db.GetByID(ctx, store.CHandovers, w.id, ex.Handover.ID, &rec); err != nil || rec.Moved.Jobs != 1 {
		t.Fatalf("counted twice: %+v", rec.Moved)
	}
	// The old computer had nothing of its own left: it steps down.
	if role, waiting, err := handover.Look(ctx, old.a); err != nil || role.Role != handover.RoleClient || len(waiting) != 0 {
		t.Fatalf("the laptop steps down: %+v %d %v", role, len(waiting), err)
	}
}

// Work left pinned to a computer that stepped down (it runs no worker) goes,
// by the host's look, to the computer that has its role — but never running
// work with a live lease, nor work only that computer can do.
func TestWorkLeftOnAClientGoesToItsRoleHolder(t *testing.T) {
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	talk(t, old.at(t, remembers()), "", "A conversation.")
	pctx := handover.WithPassphrase(ctx, pass)
	out := filepath.Join(t.TempDir(), "h.age")
	call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	nu.at(t, remembers())
	call[handover.HandoverReceiveOut](t, pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out})
	if role, _, err := handover.Look(ctx, old.a); err != nil || role.Role != handover.RoleClient {
		t.Fatalf("the laptop stepped down: %+v %v", role, err)
	}
	// Left on it afterwards, by whatever path.
	fetch := queuedJob(t, old, jobs.TypeFetch, nil)
	leased := queuedJob(t, old, jobs.TypeFetch, map[string]any{"url": "https://example.com/b"})
	if j, err := old.a.Queue.LeaseAmong(ctx, jobs.Worker{ID: old.a.Cfg.Installation.ID, Name: "laptop", JobTypes: []string{jobs.TypeFetch}}, []string{leased}); err != nil || j == nil {
		t.Fatalf("lease %v %v", j, err)
	}
	shot := queuedJob(t, old, jobs.TypeGenerate, nil)
	signedIn := queuedJob(t, old, jobs.TypeImport, map[string]any{"browser": "chrome"})
	run := ids.New("rrn")
	if err := w.db.Insert(ctx, store.CRecipeRuns, models.RecipeRun{ID: run, WorkspaceID: w.id, RecipeID: "rcp_teaser", RecipeTitle: "Teaser", Number: 1,
		State: models.RunRunning, Installation: old.a.Cfg.Installation.ID, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	recipe := ids.New("rcp")
	if err := w.db.Insert(ctx, store.CRecipes, models.Recipe{ID: recipe, WorkspaceID: w.id, Name: "teaser", Title: "Teaser",
		Triggers: &models.RecipeTriggers{NewFiles: true, Installation: old.a.Cfg.Installation.ID, Computer: "laptop", At: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	if role, _, err := handover.Look(ctx, nu.a); err != nil || role.Role != handover.RoleHost {
		t.Fatalf("the desk's look: %+v %v", role, err)
	}
	desk, laptop := nu.a.Cfg.Installation.ID, old.a.Cfg.Installation.ID
	if pinnedTo(t, w, fetch) != desk {
		t.Fatal("queued work stayed on the client")
	}
	if pinnedTo(t, w, leased) != laptop || pinnedTo(t, w, shot) != laptop || pinnedTo(t, w, signedIn) != laptop {
		t.Fatal("work with a live lease, or only that computer can do, moved")
	}
	var r models.RecipeRun
	if err := w.db.GetByID(ctx, store.CRecipeRuns, w.id, run, &r); err != nil || r.Installation != desk {
		t.Fatalf("the recipe run: %s %v", r.Installation, err)
	}
	var rc models.Recipe
	if err := w.db.GetByID(ctx, store.CRecipes, w.id, recipe, &rc); err != nil || rc.Triggers.Installation != desk || rc.Triggers.Computer != "desk" {
		t.Fatalf("planning by itself: %+v %v", rc.Triggers, err)
	}
	// Once the lease ends, that work moves too.
	if _, err := w.db.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": leased}, bson.M{"$set": bson.M{"lease.expiresAt": time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := handover.Look(ctx, nu.a); err != nil {
		t.Fatal(err)
	}
	if pinnedTo(t, w, leased) != desk {
		t.Fatal("work whose lease ended stayed on the client")
	}
}

// …or the person receives the same file again, which finishes it the same way.
func TestReceivingAgainFinishesAStoppedReceive(t *testing.T) {
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	talk(t, old.at(t, remembers()), "", "A conversation.")
	job := queuedJob(t, old, jobs.TypeFetch, nil)
	pctx := handover.WithPassphrase(ctx, pass)
	out := filepath.Join(t.TempDir(), "h.age")
	call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	nu.at(t, remembers())
	stop := handover.StopAfterReceiving()
	_, err := ops.CallTyped[handover.HandoverReceiveOut](pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out}, person())
	stop()
	if err == nil || pinnedTo(t, w, job) != old.a.Cfg.Installation.ID {
		t.Fatalf("stopped before the work: %v", err)
	}
	again := call[handover.HandoverReceiveOut](t, pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out})
	if again.Handover == nil || again.Handover.WorkMovedAt == nil || pinnedTo(t, w, job) != nu.a.Cfg.Installation.ID {
		t.Fatalf("receiving again finishes it: %+v", again.Handover)
	}
}
