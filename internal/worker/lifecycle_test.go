package worker

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/power"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/storagemove"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/videogen"
)

var someone = models.Actor{UserID: "usr_01ada000000000000000000000", UserName: "Ada", Via: "cli"}

func pluggedIn() power.Battery {
	return power.Battery{Present: true, Percent: 80, OnAC: true, Charging: true, Source: "test"}
}

// leaseAndRun takes a queued job as this machine and runs it to its end.
func leaseAndRun(t *testing.T, w *Worker, id string) (*models.Job, time.Time) {
	t.Helper()
	ctx := context.Background()
	me := jobs.Worker{ID: w.App.Cfg.Installation.ID, Name: w.App.Cfg.Installation.Name, JobTypes: w.JobTypes(), AcceptsHeavy: true}
	j, err := w.App.Queue.LeaseByID(ctx, me, id)
	if err != nil || j == nil {
		t.Fatalf("lease %s: %+v %v", id, j, err)
	}
	again := w.execute(ctx, j)
	got, err := w.App.Queue.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return got, again
}

func lastAttempt(j *models.Job) models.Attempt {
	if len(j.Attempts) == 0 {
		return models.Attempt{}
	}
	return j.Attempts[len(j.Attempts)-1]
}

func insertRender(t *testing.T, a *app.App, state string, follow bool) string {
	t.Helper()
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), RevisionID: ids.New(ids.Revision), State: state, Quality: "review", Preset: "vertical-1080p",
		Follow: follow, CreatedBy: someone, CreatedAt: time.Now().UTC()}
	if err := a.DB.Insert(context.Background(), store.CRenders, rd); err != nil {
		t.Fatal(err)
	}
	return rd.ID
}

// How a job ends decides what happens to it: a wait for something it needs
// puts it back in the queue without counting the try and says why; a failure
// for good fails it at once, the render and the clip saying so; a result is
// kept with how long it took; work a stopped process left (narration, moving
// the media) is closed plainly.
func TestHowAJobEnds(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "pier.mp4", State: models.AssetAvailable, CreatedBy: someone, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	w := New(a)
	w.Log = t.Logf
	w.Battery = pluggedIn
	queue := func(typ, key, renderID string) string {
		t.Helper()
		j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: typ, IdempotencyKey: key, AssetID: as.ID, RenderID: renderID, MaxAttempts: 3, Actor: someone,
			Payload: map[string]any{"assetId": as.ID, "noQA": true}})
		if err != nil {
			t.Fatal(err)
		}
		return j.ID
	}
	with := func(typ string, h Handler) { w.handlers = map[string]Handler{typ: h} }

	// Waiting for something it needs.
	with(jobs.TypeProxy, func(context.Context, *Worker, *models.Job, Report) (map[string]any, error) {
		return nil, &postponed{after: 2 * time.Minute, reason: "The clip is still being copied to this computer; MetaRush looks again in 2 minutes."}
	})
	before := time.Now()
	got, again := leaseAndRun(t, w, queue(jobs.TypeProxy, "proxy:wait", ""))
	if got.State != models.JobQueued || got.AttemptCount != 0 || lastAttempt(got).Outcome != jobs.OutcomePostponed || got.Lease != nil ||
		!strings.HasPrefix(got.Progress.Message, "The clip is still being copied") || again.Before(before.Add(2*time.Minute-time.Second)) || again.Sub(got.NextRunAt).Abs() > 10*time.Second { // the two differ by the database call's time
		t.Fatalf("a wait: %+v (again %v)", got, again)
	}

	// For good: no retry; the render says so plainly, the clip's analysis says it failed.
	rid := insertRender(t, a, models.RenderRendering, false)
	with(jobs.TypeProxy, func(context.Context, *Worker, *models.Job, Report) (map[string]any, error) {
		return nil, &Permanent{Err: errors.New("decoder exploded at frame 12")}
	})
	got, again = leaseAndRun(t, w, queue(jobs.TypeProxy, "proxy:fail", rid))
	if got.State != models.JobFailed || got.AttemptCount != 1 || !again.IsZero() || got.Error != "decoder exploded at frame 12" {
		t.Fatalf("for good: %+v", got)
	}
	var rd models.Render
	if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), rid, &rd); err != nil || rd.State != models.RenderFailed ||
		rd.Error != "The render stopped with an error. Render it again; if it fails again, its details say what went wrong." {
		t.Fatalf("the render: %q %q %v", rd.State, rd.Error, err)
	}
	var cur models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &cur); err != nil || cur.Analysis[jobs.TypeProxy] != "failed" {
		t.Fatalf("the clip: %v %v", cur.Analysis, err)
	}
	// A failure in plain words keeps them for the render.
	rid = insertRender(t, a, models.RenderRendering, false)
	with(jobs.TypeProxy, func(context.Context, *Worker, *models.Job, Report) (map[string]any, error) {
		return nil, failed("The finished video couldn't be read, so it wasn't kept. Render it again.", permanent("render output unreadable"))
	})
	if got, _ = leaseAndRun(t, w, queue(jobs.TypeProxy, "proxy:plain", rid)); got.State != models.JobFailed || got.Error != "render output unreadable" {
		t.Fatalf("plain failure: %+v", got)
	}
	if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), rid, &rd); err != nil || rd.Error != "The finished video couldn't be read, so it wasn't kept. Render it again." {
		t.Fatalf("the render keeps the plain words: %q %v", rd.Error, err)
	}

	// Done: the result, with how long it took.
	with(jobs.TypeProxy, func(_ context.Context, _ *Worker, _ *models.Job, report Report) (map[string]any, error) {
		report(0.5, "halfway")
		return nil, nil
	})
	if got, again = leaseAndRun(t, w, queue(jobs.TypeProxy, "proxy:done", "")); got.State != models.JobSucceeded || !again.IsZero() || got.Result["seconds"] == nil {
		t.Fatalf("done: %+v", got)
	}

	// Narration a stopped process left, and a move of the media: closed, saying what to do.
	all := New(a)
	all.Log, all.Battery = t.Logf, pluggedIn
	got, _ = leaseAndRun(t, all, queue(jobs.TypeVoiceover, "voiceover:left", ""))
	if got.State != models.JobFailed || got.AttemptCount != 1 || !strings.Contains(got.Error, "Start it again: lines that were already made are reused at no cost.") ||
		len(plainwords.Check(got.Error)) > 0 {
		t.Fatalf("narration left behind: %+v", got)
	}
	if got, _ = leaseAndRun(t, all, queue(jobs.TypeStorageMove, "storage-move:left", "")); got.State != models.JobFailed || got.Error != storagemove.Interrupted {
		t.Fatalf("a move of the media left behind: %+v", got)
	}

	// A machine without the programs jobs need says what to do.
	none := New(a)
	none.handlers = map[string]Handler{}
	err := none.Run(ctx)
	if ae := apperr.From(err); ae.Code != apperr.ToolMissing || !strings.Contains(ae.Message, "can't do any MetaRush jobs yet") || ae.Command() != "metarush components add ffmpeg" {
		t.Fatalf("no handlers: %v", err)
	}
}

// A job a person stops is stopped where it runs (its render says so) and
// nothing it returns afterwards is kept; a job another machine took over
// (this one's lease ran out) is let go here, its result thrown away.
func TestStoppedAndTakenOverJobs(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	started, ended := make(chan string, 4), make(chan string, 4)
	waitForStop := func(ctx context.Context, _ *Worker, j *models.Job, _ Report) (map[string]any, error) {
		started <- j.ID
		<-ctx.Done()
		ended <- j.ID
		return map[string]any{"madeAnyway": true}, nil
	}
	rid := insertRender(t, a, models.RenderRendering, false)
	stop, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeQA, IdempotencyKey: "qa:stop", RenderID: rid, MaxAttempts: 2, Payload: map[string]any{"renderId": rid}})
	if err != nil {
		t.Fatal(err)
	}
	taken, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeProxy, IdempotencyKey: "proxy:taken", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	w := New(a)
	w.Log, w.Battery, w.Concurrency = t.Logf, pluggedIn, 2
	w.handlers = map[string]Handler{jobs.TypeQA: waitForStop, jobs.TypeProxy: waitForStop}
	w.OnlyJobIDs = []string{stop.ID, taken.ID}
	rctx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- w.Run(rctx) }()
	defer func() {
		cancel()
		<-done
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(30 * time.Second):
			t.Fatal("the jobs never started")
		}
	}
	if _, err := a.Queue.RequestCancel(ctx, stop.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": taken.ID}, bson.M{"$set": bson.M{"lease.owner": "inst_01other00000000000000000000", "lease.ownerName": "studio-mac"}}); err != nil {
		t.Fatal(err)
	}
	// The next heartbeat (every 5 s) notices both.
	for i := 0; i < 2; i++ {
		select {
		case <-ended:
		case <-time.After(30 * time.Second):
			t.Fatal("a job kept running")
		}
	}
	waitState := func(id, want string) *models.Job {
		t.Helper()
		end := time.Now().Add(20 * time.Second)
		for {
			j, err := a.Queue.Get(ctx, id)
			if err == nil && j.State == want {
				return j
			}
			if time.Now().After(end) {
				t.Fatalf("job %s: %+v (want %s)", id, j, want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if j := waitState(stop.ID, models.JobCanceled); j.Result["madeAnyway"] != nil {
		t.Fatalf("a stopped job's late result was kept: %+v", j.Result)
	}
	// The render is marked right after the job (await it).
	var rd models.Render
	for end := time.Now().Add(20 * time.Second); ; {
		if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), rid, &rd); err == nil && rd.State == models.RenderCanceled {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("the stopped job's render: %q", rd.State)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// The taken-over job stays the other machine's, untouched.
	time.Sleep(200 * time.Millisecond) // anything this worker still did would show by now
	j, err := a.Queue.Get(ctx, taken.ID)
	if err != nil || j.State != models.JobRunning || j.Lease == nil || j.Lease.OwnerName != "studio-mac" || j.Result != nil {
		t.Fatalf("taken over: %+v %v", j, err)
	}
}

// Heavy jobs wait on a low battery, never silently: the machine publishes
// that it defers them and why; the people whose heavy jobs wait are told
// once — unless another online machine takes them — and it takes them again
// once plugged in.
func TestHeavyJobsWaitOnALowBatteryAndSaySo(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg (the heavy job here is a steady copy)")
	}
	a := testapp.New(t)
	ctx := context.Background()
	ws := a.WorkspaceID()
	heavy, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeMezzanine, IdempotencyKey: "mezzanine:battery", Heavy: true, MaxAttempts: 2, Actor: someone})
	if err != nil {
		t.Fatal(err)
	}
	w := New(a)
	w.Log, w.AcceptsHeavy = t.Logf, true
	bat := power.Battery{Present: true, Percent: 12, Source: "test"}
	w.Battery = func() power.Battery { return bat }
	other := models.WorkerDevice{ID: ids.New(ids.Installation), WorkspaceID: ws, Name: "studio-mac", AcceptsHeavy: true, JobTypes: []string{jobs.TypeMezzanine},
		LastHeartbeat: time.Now().UTC()}
	if _, err := a.DB.C(store.CWorkers).ReplaceOne(ctx, bson.M{"_id": other.ID}, other, options.Replace().SetUpsert(true)); err != nil {
		t.Fatal(err)
	}
	told := func() []models.Notification {
		t.Helper()
		var ns []models.Notification
		if err := a.DB.Find(ctx, store.CNotifications, ws, bson.M{"kind": models.NotifyBatteryPaused}, &ns); err != nil {
			t.Fatal(err)
		}
		return ns
	}
	me := func() models.WorkerDevice {
		t.Helper()
		var d models.WorkerDevice
		if err := a.DB.GetByID(ctx, store.CWorkers, ws, a.Cfg.Installation.ID, &d); err != nil {
			t.Fatal(err)
		}
		return d
	}

	// Deferring starts: published at once, with the reason; another machine takes the job, so nobody is told.
	w.trackDeferral(ctx)
	if d := me(); !d.DeferringHeavy || d.AcceptsHeavy || d.DeferSince == nil || d.DeferReason != "battery at 12%, below the 25% set for big jobs" || d.BatteryPercent == nil || *d.BatteryPercent != 12 {
		t.Fatalf("published %+v", d)
	}
	if ns := told(); len(ns) != 0 {
		t.Fatalf("told while another machine takes the job: %+v", ns)
	}
	// That machine goes away: the person whose heavy job waits is told, once per episode.
	if _, err := a.DB.C(store.CWorkers).DeleteOne(ctx, bson.M{"_id": other.ID}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		w.lastDeferChk = time.Time{}
		w.trackDeferral(ctx)
	}
	ns := told()
	if len(ns) != 1 || ns[0].UserID != someone.UserID || !strings.Contains(ns[0].Body, "Battery at 12% (below 25%)") || !strings.Contains(ns[0].Title, "paused on test-machine") {
		t.Fatalf("told %+v", ns)
	}
	// Meanwhile the heavy job isn't taken here.
	if j, err := w.lease(ctx); err != nil || j != nil {
		t.Fatalf("leased a heavy job on a low battery: %+v %v", j, err)
	}
	// Plugged in: heavy jobs again, said at once.
	bat.OnAC, bat.Charging = true, true
	w.trackDeferral(ctx)
	if d := me(); d.DeferringHeavy || !d.AcceptsHeavy || d.DeferReason != "" || w.deferSince != nil {
		t.Fatalf("plugged in: %+v", d)
	}
	if j, err := w.lease(ctx); err != nil || j == nil || j.ID != heavy.ID {
		t.Fatalf("the heavy job once plugged in: %+v %v", j, err)
	}
}

// Generated video through the worker (internal/generate): a limit of
// Google's puts the job back in the queue to wait (never a failure, the
// reason said); a refusal fails it for good (no retry changes Google's mind);
// a video made is a done job with its clip.
func TestGeneratedVideoThroughTheWorker(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	keyring.MockInit() // never the real keychain
	defer func(e, f time.Duration) { generate.PollEvery, generate.FirstPoll = e, f }(generate.PollEvery, generate.FirstPoll)
	generate.PollEvery, generate.FirstPoll = 40*time.Millisecond, 5*time.Millisecond
	key := "gk-" + strings.Repeat("Vw4", 10) + "-fake" // made at run time: no literal for the secret scanner
	fake := fakevideogen.New(key)
	defer fake.Close()
	t.Setenv(videogen.EnvGeminiBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(videogen.GeminiName), "")
	a := testapp.New(t)
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Pond film"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[providers.Status](ctx, a, "provider.set", providers.SetIn{Name: videogen.GeminiName, Key: key}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	shot := func(prompt string) string {
		t.Helper()
		out, err := ops.CallTyped[generate.GenRunOut](ctx, a, "generate.shot", generate.GenShotIn{ProjectID: p.Project.ID, Prompt: prompt, Seconds: 3, Resolution: "360p",
			NoPeople: true, MaxUSD: func(v float64) *float64 { return &v }(1)}, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		return out.JobIDs[0]
	}
	w := New(a)
	w.Log, w.Battery = t.Logf, pluggedIn

	got, again := leaseAndRun(t, w, shot("[fake:rate] a boat"))
	if got.State != models.JobQueued || got.AttemptCount != 0 || lastAttempt(got).Outcome != jobs.OutcomePostponed || again.IsZero() ||
		!strings.Contains(got.Progress.Message, "Google is busy with this key's requests") {
		t.Fatalf("a limit: %+v", got)
	}
	got, again = leaseAndRun(t, w, shot("[fake:celebrity] a famous actor waves"))
	if got.State != models.JobFailed || got.AttemptCount != 1 || got.MaxAttempts <= 1 || !again.IsZero() {
		t.Fatalf("a refusal: %+v", got)
	}
	got, _ = leaseAndRun(t, w, shot("a red paper boat"))
	if id, _ := got.Result["assetId"].(string); got.State != models.JobSucceeded || id == "" {
		t.Fatalf("made: %+v", got)
	}
}

// A review copy MetaRush made by itself to follow the latest version rings
// the bell only when it failed (D187); one a person asked for always does.
func TestFollowCopiesRingOnlyWhenTheyFail(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	w := New(a)
	w.Log = t.Logf
	job := func(rid string, follow bool) *models.Job {
		return &models.Job{ID: ids.New(ids.Job), Type: jobs.TypeRender, RenderID: rid, CreatedBy: someone, Payload: map[string]any{"follow": follow, "noQA": true}}
	}
	rings := func(rid string) []models.Notification {
		var ns []models.Notification
		if err := a.DB.Find(ctx, store.CNotifications, a.WorkspaceID(), bson.M{"renderId": rid}, &ns); err != nil {
			t.Fatal(err)
		}
		return ns
	}
	quiet := insertRender(t, a, models.RenderAvailable, true)
	w.settled(ctx, job(quiet, true), models.JobSucceeded, nil)
	if ns := rings(quiet); len(ns) != 0 {
		t.Fatalf("a follow copy that worked rang: %+v", ns)
	}
	broke := insertRender(t, a, models.RenderFailed, true)
	w.settled(ctx, job(broke, true), models.JobFailed, nil)
	if ns := rings(broke); len(ns) != 1 || ns[0].Kind != models.NotifyRenderFailed {
		t.Fatalf("a follow copy that failed: %+v", ns)
	}
	asked := insertRender(t, a, models.RenderAvailable, false)
	w.settled(ctx, job(asked, false), models.JobSucceeded, nil)
	if ns := rings(asked); len(ns) != 1 || ns[0].Kind != models.NotifyRenderFinished {
		t.Fatalf("a render a person asked for: %+v", ns)
	}
	stopped := insertRender(t, a, models.RenderCanceled, false)
	w.settled(ctx, job(stopped, false), models.JobCanceled, nil)
	if ns := rings(stopped); len(ns) != 0 {
		t.Fatalf("a stopped render rang: %+v", ns)
	}
}
