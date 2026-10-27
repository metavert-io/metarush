package ops_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// counts is how many renders a version has and how many jobs use a key.
func counts(t *testing.T, a *app.App, revisionID, key string) (renders, queued int64) {
	t.Helper()
	ctx := context.Background()
	renders, err := a.DB.C(store.CRenders).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "revisionId": revisionID})
	if err != nil {
		t.Fatal(err)
	}
	queued, err = a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "idempotencyKey": key})
	if err != nil {
		t.Fatal(err)
	}
	return renders, queued
}

// A worker may lease a render's job the instant the queue saves it, so the
// render is saved first. The fastest worker there could be — one that leases
// the job from inside the queue's own "queued" event, before render.submit
// does anything else — finds the render, pointing at that job. (Queueing first,
// as before, failed this every time: the job came before its render.) Asking
// again with the same key gives the same render and job, and nothing twice.
func TestRenderIsSavedBeforeItsJobCanBeLeased(t *testing.T) {
	a := testapp.New(t)
	p := project(t, a, "Garden story")
	c := clip(t, a, "Garage interview", p.ID)
	rev := saveCut(t, a, p.ID, "", c.ID)

	fast := jobs.Worker{ID: "ins_fast", Name: "fast machine", JobTypes: []string{jobs.TypeRender}, AcceptsHeavy: true}
	type lease struct {
		job   *models.Job
		found error // looking up the job's render at that moment
	}
	var leases []lease
	prev := a.Queue.OnEvent
	a.Queue.OnEvent = func(ctx context.Context, ev jobs.Event) {
		if prev != nil {
			prev(ctx, ev)
		}
		if ev.Kind != jobs.EventQueued || ev.Job.Type != jobs.TypeRender {
			return
		}
		j, err := a.Queue.Lease(ctx, fast)
		if err != nil || j == nil {
			t.Errorf("the render job could not be leased the moment it was queued: %+v %v", j, err)
			return
		}
		rid, _ := j.Payload["renderId"].(string)
		var rd models.Render
		found := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), rid, &rd)
		if found == nil && (rd.JobID != j.ID || rd.State != models.RenderQueued) {
			found = fmt.Errorf("the render points at job %q in state %q, not at %q", rd.JobID, rd.State, j.ID)
		}
		leases = append(leases, lease{j, found})
	}
	defer func() { a.Queue.OnEvent = prev }()

	in := ops.RenderSubmitIn{RevisionID: rev, Quality: "review", IdempotencyKey: "fastest-worker", NoQA: true}
	out := do[ops.RenderOut](t, a, "render.submit", in)
	if len(leases) != 1 {
		t.Fatalf("%d render jobs leased while submitting, want 1", len(leases))
	}
	if leases[0].found != nil {
		t.Fatalf("a worker that leased the job at once found no render: %v", leases[0].found)
	}
	if !out.Created || out.Job == nil || out.Job.ID != leases[0].job.ID || out.Render.JobID != out.Job.ID || out.Render.ID != leases[0].job.RenderID {
		t.Fatalf("submit returned %+v (leased %+v)", out, leases[0].job)
	}

	// The same key again (the job is running now): that render and job, nothing new.
	again := do[ops.RenderOut](t, a, "render.submit", in)
	if again.Created || again.Render.ID != out.Render.ID || again.Job == nil || again.Job.ID != out.Job.ID || len(leases) != 1 {
		t.Fatalf("asking again: %+v", again)
	}
	if r, j := counts(t, a, rev, in.IdempotencyKey); r != 1 || j != 1 {
		t.Fatalf("%d renders and %d jobs after asking twice", r, j)
	}
}

// Requests with the same key at the same moment save one render and queue one
// job, and every request gets that render; a request whose job couldn't be
// queued takes its render back, so no render waits for a job that doesn't exist.
func TestRenderSubmitSavesOnceAndTakesBackWhatItCouldNotQueue(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Garden story")
	c := clip(t, a, "Garage interview", p.ID)
	rev := saveCut(t, a, p.ID, "", c.ID)

	in := ops.RenderSubmitIn{RevisionID: rev, Quality: "review", IdempotencyKey: "same-moment", NoQA: true}
	const n = 8
	outs := make([]*ops.RenderOut, n)
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			outs[i], errs[i] = ops.CallTyped[ops.RenderOut](ctx, a, "render.submit", in, person)
		}()
	}
	close(start)
	wg.Wait()
	created := 0
	for i := range n {
		if errs[i] != nil {
			t.Fatalf("request %d: %v", i, errs[i])
		}
		if outs[i].Created {
			created++
		}
		if outs[i].Render.ID != outs[0].Render.ID || outs[i].Job == nil || outs[i].Job.ID != outs[0].Job.ID || outs[i].Render.JobID != outs[0].Job.ID {
			t.Fatalf("request %d got render %s / job %+v, request 0 got %s / %+v", i, outs[i].Render.ID, outs[i].Job, outs[0].Render.ID, outs[0].Job)
		}
	}
	if created != 1 {
		t.Fatalf("%d requests made the render, want 1", created)
	}
	if r, j := counts(t, a, rev, in.IdempotencyKey); r != 1 || j != 1 {
		t.Fatalf("%d renders and %d jobs after %d requests at once", r, j, n)
	}

	// The queue refuses every new job (a validator on this test database's
	// jobs): the request fails and its render is taken back.
	if err := a.DB.C(store.CJobs).Database().RunCommand(ctx, bson.D{{Key: "collMod", Value: store.CJobs},
		{Key: "validator", Value: bson.M{"neverSet": bson.M{"$exists": true}}}, {Key: "validationAction", Value: "error"}}).Err(); err != nil {
		t.Fatal(err)
	}
	p2 := project(t, a, "Kitchen chat")
	other := saveCut(t, a, p2.ID, "", clip(t, a, "Kitchen", p2.ID).ID)
	if _, err := ops.CallTyped[ops.RenderOut](ctx, a, "render.submit", ops.RenderSubmitIn{RevisionID: other, Quality: "review", NoQA: true}, person); err == nil {
		t.Fatal("the render was submitted although its job couldn't be queued")
	}
	if r, _ := counts(t, a, other, ""); r != 0 {
		t.Fatalf("%d renders left behind by a request whose job couldn't be queued", r)
	}
}
