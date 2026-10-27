package jobs

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil"
)

func TestLeaseIsExclusiveAndRecovers(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	q := &Queue{DB: db, WorkspaceID: "ws_test"}
	j, created, err := q.Enqueue(ctx, EnqueueOptions{Type: TypeProxy, IdempotencyKey: "proxy:a", MaxAttempts: 2})
	if err != nil || !created {
		t.Fatalf("enqueue: %v %v", created, err)
	}
	if again, created, _ := q.Enqueue(ctx, EnqueueOptions{Type: TypeProxy, IdempotencyKey: "proxy:a"}); created || again.ID != j.ID {
		t.Fatal("idempotency key must return the existing job")
	}
	// Ten workers race; exactly one wins.
	var wins int
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			got, err := q.Lease(ctx, Worker{ID: "ins_" + string(rune('a'+i)), JobTypes: []string{TypeProxy}, AcceptsHeavy: true})
			if err == nil && got != nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("expected exactly one lease winner, got %d", wins)
	}
	// Simulate the winner dying: expire the lease, another worker recovers it.
	old := LeaseDuration
	LeaseDuration = 50 * time.Millisecond
	defer func() { LeaseDuration = old }()
	j2, _, _ := q.Enqueue(ctx, EnqueueOptions{Type: TypeRender, IdempotencyKey: "render:x", MaxAttempts: 2})
	a, _ := q.Lease(ctx, Worker{ID: "ins_dead", JobTypes: []string{TypeRender}, AcceptsHeavy: true})
	if a == nil || a.ID != j2.ID {
		t.Fatal("first lease failed")
	}
	time.Sleep(120 * time.Millisecond)
	b, _ := q.Lease(ctx, Worker{ID: "ins_alive", JobTypes: []string{TypeRender}, AcceptsHeavy: true})
	if b == nil || b.ID != j2.ID || b.AttemptCount != 2 {
		t.Fatalf("expired lease not recovered: %+v", b)
	}
	// The dead worker can no longer complete or heartbeat.
	if err := q.Succeed(ctx, a, "ins_dead", nil); !errors.Is(err, ErrLostLease) {
		t.Fatalf("stale owner completed a job: %v", err)
	}
	if _, err := q.Heartbeat(ctx, a, "ins_dead", models.Progress{}); !errors.Is(err, ErrLostLease) {
		t.Fatalf("stale heartbeat accepted: %v", err)
	}
	if err := q.Succeed(ctx, b, "ins_alive", map[string]any{"ok": true}); err != nil {
		t.Fatal(err)
	}
	done, _ := q.Get(ctx, j2.ID)
	if done.State != models.JobSucceeded {
		t.Fatalf("state %s", done.State)
	}
}

func TestRetryBackoffAndGiveUp(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	q := &Queue{DB: db, WorkspaceID: "ws_test"}
	j, _, _ := q.Enqueue(ctx, EnqueueOptions{Type: TypeProxy, MaxAttempts: 2})
	w := Worker{ID: "ins_w", JobTypes: []string{TypeProxy}}
	l, _ := q.Lease(ctx, w)
	if err := q.Fail(ctx, l, w.ID, errors.New("boom"), true); err != nil {
		t.Fatal(err)
	}
	got, _ := q.Get(ctx, j.ID)
	if got.State != models.JobQueued || !got.NextRunAt.After(time.Now()) {
		t.Fatalf("expected backoff requeue, got %s next=%v", got.State, got.NextRunAt)
	}
	if again, _ := q.Lease(ctx, w); again != nil {
		t.Fatal("job leased before its backoff elapsed")
	}
	_, _ = db.C("jobs").UpdateByID(ctx, j.ID, map[string]any{"$set": map[string]any{"nextRunAt": time.Now().Add(-time.Second)}})
	l2, _ := q.Lease(ctx, w)
	if err := q.Fail(ctx, l2, w.ID, errors.New("boom again"), true); err != nil {
		t.Fatal(err)
	}
	got, _ = q.Get(ctx, j.ID)
	if got.State != models.JobFailed || got.Error != "boom again" {
		t.Fatalf("expected permanent failure, got %s", got.State)
	}
	if _, err := q.Retry(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	// Cancel a queued job.
	c, _ := q.RequestCancel(ctx, j.ID)
	if c.State != models.JobCanceled {
		t.Fatalf("cancel: %s", c.State)
	}
}

// A terminal waiting for some jobs leases only those; a worker that stops puts
// its job back as it was (the attempt does not count), never failed.
func TestLeaseAmongAndRelease(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	q := &Queue{DB: db, WorkspaceID: "ws_test"}
	var mu sync.Mutex
	var events []string
	q.OnEvent = func(_ context.Context, ev Event) {
		mu.Lock()
		events = append(events, ev.Kind)
		mu.Unlock()
	}
	render, _, _ := q.Enqueue(ctx, EnqueueOptions{Type: TypeRender, IdempotencyKey: "render:r", Priority: 1, MaxAttempts: 1})
	qa, _, _ := q.Enqueue(ctx, EnqueueOptions{Type: TypeQA, IdempotencyKey: "qa:r", Priority: 50, MaxAttempts: 1})
	w := Worker{ID: "ins_term", JobTypes: []string{TypeRender, TypeQA}, AcceptsHeavy: true}

	// The QA job has the higher priority, but the terminal waits only for the render.
	got, err := q.LeaseAmong(ctx, w, []string{render.ID})
	if err != nil || got == nil || got.ID != render.ID {
		t.Fatalf("lease among: %+v %v", got, err)
	}
	if again, _ := q.LeaseAmong(ctx, w, []string{render.ID}); again != nil {
		t.Fatalf("leased twice: %+v", again)
	}
	if none, _ := q.LeaseAmong(ctx, w, nil); none != nil {
		t.Fatalf("an empty list leases nothing: %+v", none)
	}
	// The worker stops before the render finishes: it waits again, attempt not counted.
	if err := q.Release(ctx, got, w.ID); err != nil {
		t.Fatal(err)
	}
	back, _ := q.Get(ctx, render.ID)
	if back.State != models.JobQueued || back.AttemptCount != 0 || back.Lease != nil || back.Error != "" ||
		len(back.Attempts) != 1 || back.Attempts[0].Outcome != "released" || back.Attempts[0].EndedAt == nil {
		t.Fatalf("released job %+v", back)
	}
	if err := q.Release(ctx, got, w.ID); !errors.Is(err, ErrLostLease) {
		t.Fatalf("releasing a job this worker no longer holds: %v", err)
	}
	// Its single attempt is still there for the next worker, which finishes it.
	next, _ := q.Lease(ctx, Worker{ID: "ins_daemon", JobTypes: []string{TypeRender}, AcceptsHeavy: true})
	if next == nil || next.ID != render.ID || next.AttemptCount != 1 {
		t.Fatalf("the next worker: %+v", next)
	}
	if err := q.Succeed(ctx, next, "ins_daemon", nil); err != nil {
		t.Fatal(err)
	}
	// The QA job was never touched.
	untouched, _ := q.Get(ctx, qa.ID)
	if untouched.State != models.JobQueued || untouched.AttemptCount != 0 {
		t.Fatalf("qa %+v", untouched)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(events, EventReleased) {
		t.Fatalf("events %v", events)
	}
}

// A job that needs something that isn't there yet goes back in the queue for a
// moment (Postpone): not a failure, the attempt not counted, the reason kept in
// plain words, and no worker takes it before the wait is over. Postponed counts
// the waits in a row; a caller can choose the new job's ID.
func TestPostponeWaitsWithoutCounting(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	q := &Queue{DB: db, WorkspaceID: "ws_test"}
	var mu sync.Mutex
	var events []Event
	q.OnEvent = func(_ context.Context, ev Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	}
	j, created, err := q.Enqueue(ctx, EnqueueOptions{ID: "job_chosen", Type: TypeRender, IdempotencyKey: "render:wait", MaxAttempts: 2})
	if err != nil || !created || j.ID != "job_chosen" {
		t.Fatalf("enqueue with a chosen ID: %+v %v %v", j, created, err)
	}
	w := Worker{ID: "ins_w", Name: "studio", JobTypes: []string{TypeRender}, AcceptsHeavy: true}
	const reason = "This render isn't saved yet, so MetaRush waits a moment and looks again (1 of 3)."
	for i := 1; i <= 2; i++ {
		l, err := q.Lease(ctx, w)
		if err != nil || l == nil || l.ID != j.ID {
			t.Fatalf("lease %d: %+v %v", i, l, err)
		}
		if n := Postponed(l); n != i-1 {
			t.Fatalf("lease %d: postponed %d times before it, want %d", i, n, i-1)
		}
		before := time.Now()
		// A wait long enough that a busy machine can't run past it before the
		// next lease below (the test moves it into the past itself after).
		if err := q.Postpone(ctx, l, w.ID, 30*time.Second, reason); err != nil {
			t.Fatal(err)
		}
		got, _ := q.Get(ctx, j.ID)
		last := got.Attempts[len(got.Attempts)-1]
		if got.State != models.JobQueued || got.AttemptCount != 0 || got.Lease != nil || got.Error != "" || got.Progress.Message != reason ||
			last.Outcome != OutcomePostponed || last.Error != reason || last.EndedAt == nil ||
			got.NextRunAt.Before(before.Add(29*time.Second)) || got.NextRunAt.After(time.Now().Add(30*time.Second)) {
			t.Fatalf("postponed job %+v", got)
		}
		if again, _ := q.Lease(ctx, w); again != nil {
			t.Fatal("leased before its wait was over")
		}
		if err := q.Postpone(ctx, l, w.ID, time.Second, reason); !errors.Is(err, ErrLostLease) {
			t.Fatalf("postponing a job this worker no longer holds: %v", err)
		}
		_, _ = db.C("jobs").UpdateByID(ctx, j.ID, map[string]any{"$set": map[string]any{"nextRunAt": time.Now().Add(-time.Second)}})
	}
	// The next attempt sees both waits, and is the job's first counted attempt.
	l, _ := q.Lease(ctx, w)
	if l == nil || Postponed(l) != 2 || l.AttemptCount != 1 {
		t.Fatalf("after two waits: %+v", l)
	}
	if err := q.Fail(ctx, l, w.ID, errors.New("boom"), false); err != nil {
		t.Fatal(err)
	}
	// A person retries it: the waits before the failure no longer count.
	if _, err := q.Retry(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if l, _ = q.Lease(ctx, w); l == nil || Postponed(l) != 0 {
		t.Fatalf("after a retry: %+v", l)
	}
	mu.Lock()
	defer mu.Unlock()
	waits := 0
	for _, ev := range events {
		if ev.Kind == EventRetry && ev.Error == reason {
			waits++
			if ev.Job.State != models.JobQueued || ev.NextRunAt.IsZero() || ev.Job.Progress.Message != reason {
				t.Fatalf("postpone event %+v", ev)
			}
		}
	}
	if waits != 2 {
		t.Fatalf("%d postpone events, want 2", waits)
	}
}
