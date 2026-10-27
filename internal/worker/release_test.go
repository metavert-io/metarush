package worker

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A terminal that waits for a job (`render submit --wait` with no worker
// online) runs only that job: the follow-up it queues (the render's automatic
// quality check) is never cut off half way. And a worker that stops while a
// job runs puts the job back as it was; it never fails for it.
func TestWaitingTerminalRunsOnlyItsJobsAndStoppingReleases(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	q := a.Queue
	first, _, err := q.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeProxy, IdempotencyKey: "proxy:release-test", Priority: 1, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	check, _, err := q.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeQA, IdempotencyKey: "qa:release-test", Priority: 50, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{}, 4)
	finish := make(chan struct{})
	handlers := map[string]Handler{
		jobs.TypeProxy: func(context.Context, *Worker, *models.Job, Report) (map[string]any, error) {
			return map[string]any{}, nil
		},
		jobs.TypeQA: func(ctx context.Context, _ *Worker, _ *models.Job, _ Report) (map[string]any, error) {
			started <- struct{}{}
			select {
			case <-ctx.Done():
				return nil, fmt.Errorf("decoding the render failed: %w", ctx.Err())
			case <-finish:
				return map[string]any{"ok": true}, nil
			}
		},
	}
	worker := func(only ...string) (*Worker, func()) {
		w := New(a)
		w.handlers, w.Concurrency, w.OnlyJobIDs, w.Log = handlers, 1, only, t.Logf
		wctx, stop := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = w.Run(wctx)
		}()
		return w, func() {
			stop()
			<-done
		}
	}
	state := func(id, want string) *models.Job {
		t.Helper()
		end := time.Now().Add(20 * time.Second)
		for {
			j, err := q.Get(ctx, id)
			if err == nil && j.State == want {
				return j
			}
			if time.Now().After(end) {
				t.Fatalf("job %s: %+v (want %s)", id, j, want)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}

	// The terminal's worker runs the job it waits for, and nothing else.
	term, stopTerm := worker(first.ID)
	state(first.ID, models.JobSucceeded)
	if j, err := term.lease(ctx); err != nil || j != nil {
		t.Fatalf("the terminal leased another job: %+v %v", j, err)
	}
	stopTerm()
	if j, _ := q.Get(ctx, check.ID); j.State != models.JobQueued || j.AttemptCount != 0 {
		t.Fatalf("the quality check was touched: %+v", j)
	}

	// A worker stops while the quality check runs: it waits again, unfailed.
	_, stopWorker := worker()
	select {
	case <-started:
	case <-time.After(20 * time.Second):
		t.Fatal("the quality check never started")
	}
	stopWorker()
	back := state(check.ID, models.JobQueued)
	if back.AttemptCount != 0 || back.Error != "" || back.Lease != nil || len(back.Attempts) != 1 || back.Attempts[0].Outcome != "released" {
		t.Fatalf("released job %+v", back)
	}

	// The next worker runs it to the end.
	close(finish)
	_, stopNext := worker()
	done := state(check.ID, models.JobSucceeded)
	stopNext()
	if done.AttemptCount != 1 {
		t.Fatalf("attempts counted: %d", done.AttemptCount)
	}
}
