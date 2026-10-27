package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// The first try of a render failed the way it did on a busy machine (a frame
// fetched through the renderer's frame server not answered within the page's
// time limit); that is not a permanent error, so the job went back in the
// queue for a second try.
var stalledFrame = errors.New(`remotion: A delayRender() "Fetching http://localhost:65251/proxy?src=http%3A%2F%2F127.0.0.1%3A65247%2Ftoken%2Fast_broll&time=0.1666 from server" was called but not cleared after 28000ms. See https://remotion.dev/docs/timeout for help.`)

// tries is a stand-in handler that fails its first n tries the way a stalled
// frame fails a render (retryable), then succeeds, recording when each try
// started.
type tries struct {
	mu    sync.Mutex
	fail  int
	start []time.Time
}

func (tr *tries) handler(context.Context, *Worker, *models.Job, Report) (map[string]any, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.start = append(tr.start, time.Now())
	if len(tr.start) <= tr.fail {
		return nil, failed("The video couldn't be drawn: the renderer stopped with an error.", stalledFrame)
	}
	return map[string]any{"ok": true}, nil
}

func (tr *tries) started() []time.Time {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]time.Time(nil), tr.start...)
}

// A terminal running one job itself (`render submit --wait` or `qa run --wait`
// with no machine running MetaRush online, or with --local) keeps the job
// across a failed try: the job goes back in the queue with a short wait before
// its next try (jobs.RetryBackoff), and the terminal takes it again the moment
// that wait is over. Before, the terminal's worker stopped right after the
// failed try, the job waited in the queue for a machine that never came, and
// the command waited for it forever.
func TestTerminalTakesItsJobAgainAfterAFailedTry(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	old := jobs.RetryBase
	jobs.RetryBase = 400 * time.Millisecond
	defer func() { jobs.RetryBase = old }()

	tr := &tries{fail: 1}
	w := New(a)
	w.Log = t.Logf
	w.handlers = map[string]Handler{jobs.TypeRender: tr.handler}
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeRender, IdempotencyKey: "render:terminal-retry", Priority: 15, MaxAttempts: 2,
		Payload: map[string]any{"noQA": true}})
	if err != nil {
		t.Fatal(err)
	}
	w.OnlyJobID = j.ID
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := w.Run(rctx); err != nil {
		t.Fatalf("the terminal's worker stopped with an error: %v", err)
	}
	got, err := a.Queue.Get(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != models.JobSucceeded {
		t.Fatalf("the terminal stopped after the failed try and left the job %s (try %d of %d, next at %s): nothing would ever run it",
			got.State, got.AttemptCount, got.MaxAttempts, got.NextRunAt.Format(time.RFC3339Nano))
	}
	if len(got.Attempts) != 2 || got.Attempts[0].Outcome != "retry" || got.Attempts[0].Error != stalledFrame.Error() || got.Attempts[1].Outcome != models.JobSucceeded {
		t.Fatalf("tries: %+v", got.Attempts)
	}
	// The second try waited for the retry's wait, and started as soon as it was over.
	st := tr.started()
	failedAt := *got.Attempts[0].EndedAt
	wait := jobs.RetryBackoff(1)
	if len(st) != 2 || st[1].Before(failedAt.Add(wait-50*time.Millisecond)) || st[1].After(failedAt.Add(wait+1500*time.Millisecond)) {
		t.Fatalf("the second try started %v after the first failed (its wait: %v)", st[len(st)-1].Sub(failedAt), wait)
	}

	// A try that fails for good, or the last try, ends the terminal's run at once.
	for _, c := range []struct {
		name string
		h    Handler
		max  int
	}{
		{"failed for good", func(context.Context, *Worker, *models.Job, Report) (map[string]any, error) {
			return nil, permanent("the version this render is for isn't there any more")
		}, 2},
		{"last try", (&tries{fail: 5}).handler, 1},
	} {
		w.handlers[jobs.TypeRender] = c.h
		j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeRender, IdempotencyKey: "render:terminal-" + c.name, Priority: 15, MaxAttempts: c.max})
		if err != nil {
			t.Fatal(err)
		}
		w.OnlyJobID = j.ID
		began := time.Now()
		if err := w.Run(rctx); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		got, _ := a.Queue.Get(ctx, j.ID)
		if got.State != models.JobFailed || len(got.Attempts) != 1 || time.Since(began) > 5*time.Second {
			t.Fatalf("%s: the terminal kept the job (%s, %d tries, %v)", c.name, got.State, len(got.Attempts), time.Since(began))
		}
	}
}

// A terminal waiting for several jobs (`--wait` with no worker online) runs
// them until its wait ends: a job back in the queue after a failed try is
// taken again as soon as its wait is over (the worker looks every 1.5 s).
func TestTerminalForSeveralJobsTakesARetryPromptly(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	old := jobs.RetryBase
	jobs.RetryBase = 400 * time.Millisecond
	defer func() { jobs.RetryBase = old }()

	tr := &tries{fail: 1}
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeQA, IdempotencyKey: "qa:terminal-retry", Priority: 12, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	w := New(a)
	w.handlers, w.Concurrency, w.OnlyJobIDs, w.Log, w.IgnoreBattery = map[string]Handler{jobs.TypeQA: tr.handler}, 2, []string{j.ID}, t.Logf, true
	wctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = w.Run(wctx)
	}()
	defer func() {
		stop()
		<-done
	}()
	end := time.Now().Add(20 * time.Second)
	var got *models.Job
	for {
		got, err = a.Queue.Get(ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == models.JobSucceeded || time.Now().After(end) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got.State != models.JobSucceeded || len(got.Attempts) != 2 {
		t.Fatalf("the retry was never taken: %s after %d tries", got.State, len(got.Attempts))
	}
	st := tr.started()
	if late := st[1].Sub(got.Attempts[0].EndedAt.Add(jobs.RetryBackoff(1))); late < -50*time.Millisecond || late > 2500*time.Millisecond {
		t.Fatalf("the retry started %v after its wait was over", late)
	}
}

// The wait before a failed job's next try doubles each time: 10 s, 20 s, 40 s.
func TestRetryBackoffDoubles(t *testing.T) {
	want := []time.Duration{10 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second}
	for attempt, w := range want {
		if got := jobs.RetryBackoff(attempt); got != w {
			t.Fatalf("after try %d: %v, want %v", attempt, got, w)
		}
	}
}
