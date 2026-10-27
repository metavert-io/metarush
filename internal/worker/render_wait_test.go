package worker

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A render job that finds no render waits for it (render.submit saves the
// render before it queues the job, so this is a render still being saved, or
// one that is gone): back in the queue for a moment, twice as long each time,
// renderWaits times, never counted as an attempt, saying why in plain words;
// then it fails, saying so. A render that turns up while its job waits is
// found. A terminal running the job itself (`render submit --wait --local`)
// takes it again after each wait instead of giving up on it.
func TestRenderJobWaitsForItsRender(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	old := renderWait
	renderWait = 40 * time.Millisecond
	defer func() { renderWait = old }()
	w := New(a)
	w.Log = t.Logf
	w.handlers = map[string]Handler{jobs.TypeRender: renderHandler}
	me := jobs.Worker{ID: a.Cfg.Installation.ID, Name: a.Cfg.Installation.Name, JobTypes: []string{jobs.TypeRender}, AcceptsHeavy: true}
	queue := func(renderID string) *models.Job {
		t.Helper()
		j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeRender, IdempotencyKey: "render:" + renderID, Priority: 15, RenderID: renderID,
			Payload: map[string]any{"renderId": renderID, "revisionId": "rev_missing", "noQA": true}, MaxAttempts: 2})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	// lease takes the job once its wait is over, like a worker polling.
	lease := func(id string) *models.Job {
		t.Helper()
		end := time.Now().Add(10 * time.Second)
		for {
			j, err := a.Queue.Lease(ctx, me)
			if err != nil {
				t.Fatal(err)
			}
			if j != nil {
				if j.ID != id {
					t.Fatalf("leased %s, want %s", j.ID, id)
				}
				return j
			}
			if time.Now().After(end) {
				t.Fatalf("job %s never came back to the queue", id)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	get := func(id string) *models.Job {
		t.Helper()
		j, err := a.Queue.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return j
	}

	// No render at all: it waits renderWaits times, then fails.
	gone := queue(ids.New(ids.Render))
	for i := 0; i < renderWaits; i++ {
		j := lease(gone.ID)
		before := time.Now()
		again := w.execute(ctx, j)
		got := get(gone.ID)
		wait := renderWait << i
		last := got.Attempts[len(got.Attempts)-1]
		if again.IsZero() || got.State != models.JobQueued || got.AttemptCount != 0 || got.Lease != nil || got.Error != "" ||
			last.Outcome != jobs.OutcomePostponed || got.NextRunAt.Before(before.Add(wait-5*time.Millisecond)) || got.NextRunAt.After(time.Now().Add(wait)) {
			t.Fatalf("wait %d: %+v (again %v)", i+1, got, again)
		}
		if want := fmt.Sprintf("(%d of %d)", i+1, renderWaits); !strings.Contains(got.Progress.Message, want) || last.Error != got.Progress.Message {
			t.Fatalf("wait %d says %q / %q", i+1, got.Progress.Message, last.Error)
		}
		if f := plainwords.Check(got.Progress.Message); len(f) > 0 {
			t.Fatalf("wait %d is not said plainly: %v", i+1, f)
		}
	}
	if again := w.execute(ctx, lease(gone.ID)); !again.IsZero() {
		t.Fatal("it waited more than renderWaits times")
	}
	got := get(gone.ID)
	if got.State != models.JobFailed || got.AttemptCount != 1 || len(got.Attempts) != renderWaits+1 || !strings.Contains(got.Error, "not found") {
		t.Fatalf("after waiting: %+v", got)
	}
	detail, err := ops.JobDetail(ctx, a, gone.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail.Advice, "Render the version again") || len(plainwords.Check(detail.Advice)) > 0 {
		t.Fatalf("advice %q", detail.Advice)
	}

	// The render turns up while its job waits: the job finds it and goes on
	// (here to the version, which this test doesn't have, so it stops there and
	// the render says so).
	late := ids.New(ids.Render)
	lj := queue(late)
	if again := w.execute(ctx, lease(lj.ID)); again.IsZero() {
		t.Fatal("the job didn't wait for its render")
	}
	if err := a.DB.Insert(ctx, store.CRenders, models.Render{ID: late, WorkspaceID: a.WorkspaceID(), RevisionID: "rev_missing", JobID: lj.ID,
		State: models.RenderQueued, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	w.execute(ctx, lease(lj.ID))
	got = get(lj.ID)
	var rd models.Render
	if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), late, &rd); err != nil {
		t.Fatal(err)
	}
	if got.State != models.JobFailed || len(got.Attempts) != 2 || got.Attempts[0].Outcome != jobs.OutcomePostponed ||
		!strings.Contains(got.Error, "revision") || rd.State != models.RenderFailed || !strings.Contains(rd.Error, "version") {
		t.Fatalf("after the render turned up: job %+v, render %+v", got, rd)
	}

	// A terminal running the job itself keeps it across the waits.
	term := New(a)
	term.Log = t.Logf
	term.handlers = map[string]Handler{jobs.TypeRender: renderHandler}
	tj := queue(ids.New(ids.Render))
	term.OnlyJobID = tj.ID
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := term.Run(rctx); err != nil {
		t.Fatalf("the terminal stopped: %v", err)
	}
	got = get(tj.ID)
	if got.State != models.JobFailed || len(got.Attempts) != renderWaits+1 || jobs.Postponed(&models.Job{Attempts: got.Attempts[:renderWaits]}) != renderWaits {
		t.Fatalf("the terminal's job: %+v", got)
	}
}
