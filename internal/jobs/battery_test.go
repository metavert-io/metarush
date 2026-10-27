package jobs

import (
	"context"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil"
)

// Heavy jobs wait only while a worker defers them for battery; light jobs never
// wait; "Process now" (Expedite) lets a deferred heavy job run on battery.
func TestLeaseHonorsBatteryDeferralAndAllowOnBattery(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	q := &Queue{DB: db, WorkspaceID: "ws_test"}
	tx, _, _ := q.Enqueue(ctx, EnqueueOptions{Type: TypeTranscribe, IdempotencyKey: "tx:a", Heavy: true})
	px, _, _ := q.Enqueue(ctx, EnqueueOptions{Type: TypeProxy, IdempotencyKey: "px:a"})
	types := []string{TypeTranscribe, TypeProxy}

	deferring := Worker{ID: "ins_laptop", Name: "laptop", JobTypes: types, AcceptsHeavy: true, DeferHeavy: true}
	got, err := q.Lease(ctx, deferring)
	if err != nil || got == nil || got.ID != px.ID {
		t.Fatalf("deferring worker should lease the light job, got %+v %v", got, err)
	}
	if again, _ := q.Lease(ctx, deferring); again != nil {
		t.Fatalf("deferring worker leased heavy job %s", again.ID)
	}
	if again, _ := q.Lease(ctx, Worker{ID: "ins_cfg", JobTypes: types, AcceptsHeavy: false}); again != nil {
		t.Fatal("worker with heavy jobs disabled leased a heavy job")
	}
	changed, err := q.Expedite(ctx, []string{tx.ID, px.ID})
	if err != nil || len(changed) != 1 || changed[0] != tx.ID {
		t.Fatalf("expedite changed %v (%v); only the queued job should change", changed, err)
	}
	got, err = q.Lease(ctx, deferring)
	if err != nil || got == nil || got.ID != tx.ID || !got.AllowOnBattery {
		t.Fatalf("expedited heavy job should run on battery, got %+v %v", got, err)
	}
	// Plugged in (not deferring): heavy jobs run without any override.
	tx2, _, _ := q.Enqueue(ctx, EnqueueOptions{Type: TypeTranscribe, IdempotencyKey: "tx:b", Heavy: true})
	if got, _ := q.Lease(ctx, Worker{ID: "ins_desk", JobTypes: types, AcceptsHeavy: true}); got == nil || got.ID != tx2.ID {
		t.Fatalf("a worker on AC should lease heavy jobs, got %+v", got)
	}
}

func TestWhyExplainsWaiting(t *testing.T) {
	now := time.Now().UTC()
	pct := 18
	laptop := models.WorkerDevice{ID: "ins_l", Name: "laptop", JobTypes: []string{TypeTranscribe, TypeProxy}, Concurrency: 2, HeavyEnabled: true,
		DeferringHeavy: true, BatteryPercent: &pct, HeavyBatteryThreshold: 25, LastHeartbeat: now}
	heavy := models.Job{ID: "job_1", Type: TypeTranscribe, State: models.JobQueued, Heavy: true, NextRunAt: now.Add(-time.Second)}
	r := Why(heavy, []models.WorkerDevice{laptop}, now)
	if r == nil || r.Code != WaitBattery || !r.CanProcessNow || r.Text != "Paused: battery at 18% on laptop." {
		t.Fatalf("battery reason: %+v", r)
	}
	// Allowed on battery: no longer paused.
	h2 := heavy
	h2.AllowOnBattery = true
	if r := Why(h2, []models.WorkerDevice{laptop}, now); r == nil || r.Code != WaitStarting {
		t.Fatalf("allowOnBattery reason: %+v", r)
	}
	// A second machine on AC takes it: not paused.
	desk := laptop
	desk.ID, desk.Name, desk.DeferringHeavy, desk.BatteryPercent = "ins_d", "desk", false, nil
	if r := Why(heavy, []models.WorkerDevice{laptop, desk}, now); r == nil || r.Code != WaitStarting || r.Machine != "desk" {
		t.Fatalf("with an AC worker: %+v", r)
	}
	// Offline workers do not count.
	stale := laptop
	stale.LastHeartbeat = now.Add(-2 * time.Minute)
	if r := Why(heavy, []models.WorkerDevice{stale}, now); r == nil || r.Code != WaitNoWorker ||
		r.Text != "No machine running MetaRush can make transcripts right now. It starts as soon as one is." {
		t.Fatalf("offline: %+v", r)
	}
	// Heavy disabled by config.
	off := laptop
	off.HeavyEnabled, off.DeferringHeavy = false, false
	if r := Why(heavy, []models.WorkerDevice{off}, now); r == nil || r.Code != WaitHeavyDisabled ||
		r.Text != "Big jobs like transcripts and final renders are turned off on laptop." {
		t.Fatalf("heavy disabled: %+v", r)
	}
	// Light job on a deferring laptop starts; a busy worker makes it wait.
	light := models.Job{ID: "job_2", Type: TypeProxy, State: models.JobQueued, NextRunAt: now}
	if r := Why(light, []models.WorkerDevice{laptop}, now); r == nil || r.Code != WaitStarting {
		t.Fatalf("light: %+v", r)
	}
	busy := laptop
	busy.Running = 2
	if r := Why(light, []models.WorkerDevice{busy}, now); r == nil || r.Code != WaitBusy || r.Text != "Waiting for laptop to finish what it's doing." {
		t.Fatalf("busy: %+v", r)
	}
	retry := light
	retry.NextRunAt = now.Add(40 * time.Second)
	if r := Why(retry, []models.WorkerDevice{laptop}, now); r == nil || r.Code != WaitRetry || r.Text != "It ran into a problem and tries again in 40 seconds." {
		t.Fatalf("retry: %+v", r)
	}
	retry.NextRunAt = now.Add(2*time.Minute + 5*time.Second)
	if r := Why(retry, []models.WorkerDevice{laptop}, now); r == nil || r.Text != "It ran into a problem and tries again in about 2 minutes." {
		t.Fatalf("retry in minutes: %+v", r)
	}
	running := light
	running.State = models.JobRunning
	if Why(running, []models.WorkerDevice{laptop}, now) != nil {
		t.Fatal("running jobs are not waiting")
	}
}
