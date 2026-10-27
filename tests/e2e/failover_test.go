package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/worker"
)

// twoMachines sets up one database and one emulated bucket shared by two
// independent installations (separate homes and caches).
func twoMachines(t *testing.T) (a, b *install, ts *tools.Set) {
	t.Helper()
	ts = tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) || !ts.Has(tools.FFprobe) {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	db := testutil.DB(t)
	backend := s3mem.New()
	_ = backend.CreateBucket("media")
	s3srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(s3srv.Close)
	wsID := ids.New(ids.Workspace)
	if err := db.Insert(context.Background(), store.CWorkspaces, models.Workspace{ID: wsID, WorkspaceID: wsID, Name: "E2E", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return newInstall(t, db, s3srv.URL, wsID, "laptop-a"), newInstall(t, db, s3srv.URL, wsID, "studio-b"), ts
}

// reopen brings a closed installation back online (same home and config).
func reopen(t *testing.T, in *install) *app.App {
	t.Helper()
	a, err := app.OpenWith(context.Background(), in.app.Cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func waitJob(t *testing.T, q *jobs.Queue, id string, timeout time.Duration) *models.Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		j, err := q.Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		switch j.State {
		case models.JobSucceeded:
			return j
		case models.JobFailed, models.JobCanceled:
			t.Fatalf("job %s %s ended %s: %s", j.ID, j.Type, j.State, j.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s %s still %s (%s) after %s", j.ID, j.Type, j.State, j.Progress.Message, timeout)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// Machine A's worker leases a derive job and dies mid-job (no completion, no
// more heartbeats). Machine B's worker recovers the expired lease and completes
// it; A's late completion is rejected; the result is visible from A.
func TestWorkerFailoverAcrossMachines(t *testing.T) {
	A, B, ts := twoMachines(t)
	ctx := context.Background()
	old := jobs.LeaseDuration
	jobs.LeaseDuration = 3 * time.Second // A's lease; B's worker gets the normal length below
	defer func() { jobs.LeaseDuration = old }()

	proj := call[ops.ProjectOut](t, A.app, "project.create", ops.ProjectCreateIn{Name: "failover"})
	src := clip(t, ts, t.TempDir(), "take.mp4", "testsrc2=size=1280x720:rate=30:duration=4")
	res := ingest.Paths(ctx, A.app, []string{src}, ingest.Options{ProjectID: proj.Project.ID, Via: "cli"})
	if len(res) != 1 || res[0].Status != "ingested" {
		t.Fatalf("ingest: %+v", res)
	}
	assetID := res[0].AssetID

	// A's worker claims the proxy job exactly as worker.lease does and reports progress…
	aw := jobs.Worker{ID: A.app.Cfg.Installation.ID, Name: A.app.Cfg.Installation.Name, JobTypes: []string{jobs.TypeProxy}, AcceptsHeavy: true}
	leased, err := A.app.Queue.Lease(ctx, aw)
	if err != nil || leased == nil || leased.Type != jobs.TypeProxy {
		t.Fatalf("A lease: %+v %v", leased, err)
	}
	if _, err := A.app.Queue.Heartbeat(ctx, leased, aw.ID, models.Progress{Pct: 0.4, Message: "transcoding proxy"}); err != nil {
		t.Fatal(err)
	}
	// …and while A's lease is live, nobody else can take the job.
	bw := jobs.Worker{ID: B.app.Cfg.Installation.ID, Name: B.app.Cfg.Installation.Name, JobTypes: []string{jobs.TypeProxy}, AcceptsHeavy: true}
	if stolen, _ := B.app.Queue.Lease(ctx, bw); stolen != nil {
		t.Fatalf("B leased %s while A's lease was live", stolen.ID)
	}
	// Then A dies: its connection goes away mid-job, with no completion written.
	A.app.Close()
	jobs.LeaseDuration = old

	// B's worker (empty cache) recovers the expired lease and finishes everything.
	wctx, stop := context.WithCancel(ctx)
	defer stop()
	w := worker.New(B.app)
	w.Log = func(string, ...any) {}
	w.IgnoreBattery = true // a laptop on battery would hold heavy jobs back
	go func() { _ = w.Run(wctx) }()
	j := waitJob(t, B.app.Queue, leased.ID, 90*time.Second)
	for _, id := range res[0].JobIDs {
		waitJob(t, B.app.Queue, id, 90*time.Second)
	}
	stop()
	if j.AttemptCount != 2 || len(j.Attempts) != 2 || j.Lease != nil {
		t.Fatalf("expected two attempts and no lease: count %d, attempts %+v, lease %+v", j.AttemptCount, j.Attempts, j.Lease)
	}
	if a0, a1 := j.Attempts[0], j.Attempts[1]; a0.Worker != aw.ID || a0.Outcome != "" || a0.EndedAt != nil || a1.Worker != bw.ID || a1.Outcome != models.JobSucceeded {
		t.Fatalf("attempts: A=%+v B=%+v", a0, a1)
	}

	// A comes back: its late completion and heartbeat are rejected, and B's result stands.
	A2 := reopen(t, A)
	if err := A2.Queue.Succeed(ctx, leased, aw.ID, map[string]any{"late": true}); !errors.Is(err, jobs.ErrLostLease) {
		t.Fatalf("late Succeed from A: %v", err)
	}
	if _, err := A2.Queue.Heartbeat(ctx, leased, aw.ID, models.Progress{Pct: 0.9}); !errors.Is(err, jobs.ErrLostLease) {
		t.Fatalf("late heartbeat from A: %v", err)
	}
	if err := A2.Queue.Fail(ctx, leased, aw.ID, errors.New("late failure"), true); !errors.Is(err, jobs.ErrLostLease) {
		t.Fatalf("late Fail from A: %v", err)
	}
	after, _ := A2.Queue.Get(ctx, leased.ID)
	if after.State != models.JobSucceeded || after.Result["late"] != nil || after.Error != "" {
		t.Fatalf("A's late result overwrote B's: %+v", after)
	}

	// The proxy B made is visible from A and readable through A's own cache.
	ps := call[ops.ProjectShowOut](t, A2, "project.show", ops.ProjectShowIn{ID: proj.Project.ID})
	if len(ps.Assets) != 1 || !strings.Contains(strings.Join(ps.Assets[0].Variants, ","), "proxy") {
		t.Fatalf("A does not see the proxy: %+v", ps.Assets)
	}
	var as models.Asset
	if err := A2.DB.GetByID(ctx, store.CAssets, A2.WorkspaceID(), assetID, &as); err != nil {
		t.Fatal(err)
	}
	pv := as.Variants[models.VariantProxy]
	p, err := A2.Cache.Ensure(ctx, A2.Media, pv.StorageKey, pv.SHA256, pv.Size, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sum, _ := media.HashFile(p); sum != pv.SHA256 {
		t.Fatal("proxy bytes differ from the recorded hash")
	}
	if mi, err := probe.Probe(ctx, ts, p); err != nil || !mi.HasVideo {
		t.Fatalf("proxy unreadable on A: %v", err)
	}
}

// A revision is created and its render queued on A; A goes offline; B's worker
// picks the render up from the shared queue, renders from the canonical store and
// publishes it; A sees the finished render when it is back.
func TestRenderPickupAcrossMachines(t *testing.T) {
	A, B, _ := twoMachines(t)
	ctx := context.Background()
	if dir, err := filepath.Abs(filepath.Join("..", "..", "video")); err == nil {
		B.app.Cfg.Tools.VideoDir, B.app.Cfg.Tools.DevRenderer = dir, true // the repository's checkout (developer setting)
		B.app.Tools = tools.New(B.app.Cfg.Tools)
	}
	if err := worker.RenderReady(B.app); err != nil {
		t.Skipf("rendering is not available here: %v", err)
	}
	ts := A.app.Tools
	proj := call[ops.ProjectOut](t, A.app, "project.create", ops.ProjectCreateIn{Name: "render pickup"})
	src := clip(t, ts, t.TempDir(), "take.mp4", "testsrc2=size=1280x720:rate=30:duration=4")
	res := ingest.Paths(ctx, A.app, []string{src}, ingest.Options{ProjectID: proj.Project.ID, Via: "cli"})
	if len(res) != 1 || res[0].Status != "ingested" {
		t.Fatalf("ingest: %+v", res)
	}
	plan, _ := json.Marshal(map[string]any{"projectId": proj.Project.ID, "instruction": "two seconds", "spec": map[string]any{
		"schemaVersion": 1, "output": map[string]any{"preset": "landscape-1080p", "fps": 30},
		"clips": []any{map[string]any{"id": "a", "assetId": res[0].AssetID, "in": 0.5, "out": 2.5}}}})
	chk := call[ops.EditCheckOut](t, A.app, "edit.check", ops.EditCheckIn{Plan: plan})
	if !chk.Valid {
		t.Fatalf("plan invalid: %+v", chk.Errors)
	}
	rev := call[ops.EditApplyOut](t, A.app, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken})
	sub := call[ops.RenderOut](t, A.app, "render.submit", ops.RenderSubmitIn{RevisionID: rev.Revision.ID, Quality: "review"})
	if sub.Job == nil || sub.Workers != 0 {
		t.Fatalf("render submit: %+v", sub)
	}
	A.app.Close() // A goes offline with the render still queued

	wctx, stop := context.WithCancel(ctx)
	defer stop()
	w := worker.New(B.app)
	w.IgnoreBattery = true
	w.Log = func(string, ...any) {}
	go func() { _ = w.Run(wctx) }()
	j := waitJob(t, B.app.Queue, sub.Job.ID, 6*time.Minute)
	stop()
	if j.Attempts[len(j.Attempts)-1].Worker != B.app.Cfg.Installation.ID {
		t.Fatalf("render ran on %s, not B", j.Attempts[len(j.Attempts)-1].Worker)
	}
	// Every finished render gets a quality check (it also listens to the speech), for the person who asked for the render.
	// The worker queues it just after marking the render done (even while stopping), so give it a moment.
	var checks []models.Job
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		checks = nil
		if err := B.app.DB.Find(ctx, store.CJobs, B.app.WorkspaceID(), bson.M{"type": jobs.TypeQA, "payload.renderId": sub.Render.ID}, &checks); err != nil {
			t.Fatal(err)
		}
		if len(checks) > 0 || time.Now().After(deadline) {
			break
		}
	}
	if len(checks) != 1 || checks[0].Payload["auto"] != true || checks[0].Payload["noSpeechCheck"] != false || checks[0].CreatedBy.UserID != sub.Render.CreatedBy.UserID {
		t.Fatalf("automatic quality check after the render: %+v", checks)
	}

	A2 := reopen(t, A)
	st := call[ops.RenderOut](t, A2, "render.status", ops.RenderIDIn{ID: sub.Render.ID})
	if st.Render.State != models.RenderAvailable || st.Render.RenderedBy != B.app.Cfg.Installation.ID || st.Render.Output == nil {
		t.Fatalf("render as seen from A: %+v", st.Render)
	}
	out := st.Render.Output
	p, err := A2.Cache.Ensure(ctx, A2.Media, out.StorageKey, out.SHA256, out.Size, nil)
	if err != nil {
		t.Fatal(err)
	}
	mi, err := probe.Probe(ctx, ts, p)
	if err != nil || !mi.HasVideo || mi.Duration < 1.9 || mi.Duration > 2.2 {
		t.Fatalf("render fetched on A: %+v %v", mi, err)
	}
}
