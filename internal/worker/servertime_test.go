package worker

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/power"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/transcribe"
)

func serverNowOf(t *testing.T, a *app.App) time.Time {
	t.Helper()
	now, err := a.DB.ServerNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return now
}

// A terminal running one job on a computer whose clock is ten minutes fast
// still sees that another computer's lease runs (D360): it says the job is
// running there instead of waiting for a lease it thinks ended; on a computer
// ten minutes slow, a lease that ended by the server's time is taken over.
func TestAnInlineWorkerGoesByTheServersTime(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeProxy, IdempotencyKey: "proxy:inline-skew", MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	other := jobs.Worker{ID: ids.New(ids.Installation), Name: "studio-mac", JobTypes: []string{jobs.TypeProxy}}
	if lj, err := a.Queue.LeaseByID(ctx, other, j.ID); err != nil || lj == nil {
		t.Fatalf("the other computer's lease: %+v %v", lj, err)
	}
	ran := 0
	w := New(a)
	w.Log, w.Battery = t.Logf, pluggedIn
	w.handlers = map[string]Handler{jobs.TypeProxy: func(context.Context, *Worker, *models.Job, Report) (map[string]any, error) {
		ran++
		return map[string]any{}, nil
	}}
	w.OnlyJobID = j.ID
	restore := testutil.SkewClock(t, 10*time.Minute)
	rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := w.Run(rctx); err == nil || !strings.Contains(err.Error(), "already running on studio-mac") || ran != 0 {
		t.Fatalf("a computer ten minutes fast, a lease running elsewhere: %v (ran %d)", err, ran)
	}
	restore()
	if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID}, bson.M{"$set": bson.M{"lease.expiresAt": serverNowOf(t, a).Add(-time.Second)}}); err != nil {
		t.Fatal(err)
	}
	testutil.SkewClock(t, -10*time.Minute)
	if err := w.Run(rctx); err != nil || ran != 1 {
		t.Fatalf("a computer ten minutes slow, a lease that ended: %v (ran %d)", err, ran)
	}
}

// A worker's heartbeat is the database server's time, so other computers
// judge it online by their own view of the server (D360); a restart within
// a while of the last heartbeat (by the server's time) carries on its battery
// episode.
func TestHeartbeatsAreTheServersTime(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	w := New(a)
	w.AcceptsHeavy = true
	w.Battery = func() power.Battery { return power.Battery{Present: true, Percent: 15, Source: "test"} }
	restore := testutil.SkewClock(t, -10*time.Minute)
	w.beat(ctx)
	restore()
	var d models.WorkerDevice
	if err := a.DB.GetByID(ctx, store.CWorkers, a.WorkspaceID(), a.Cfg.Installation.ID, &d); err != nil {
		t.Fatal(err)
	}
	if since := serverNowOf(t, a).Sub(d.LastHeartbeat); since < -time.Second || since > time.Minute {
		t.Fatalf("a heartbeat on a computer ten minutes slow: %v before the server's now", since)
	}
	if len(d.JobTypes) > 0 {
		restore := testutil.SkewClock(t, 10*time.Minute)
		if ws, err := ops.OnlineWorkers(ctx, a, d.JobTypes[0]); err != nil || len(ws) != 1 {
			t.Fatalf("a computer ten minutes fast doesn't see a worker that just heartbeated: %+v %v", ws, err)
		}
		restore()
	}

	since := serverNowOf(t, a).Add(-time.Hour).Truncate(time.Millisecond)
	resumes := func(skew, ago time.Duration) bool {
		t.Helper()
		prev := models.WorkerDevice{ID: a.Cfg.Installation.ID, WorkspaceID: a.WorkspaceID(), Name: "studio-mac", DeferringHeavy: true, DeferSince: &since,
			LastHeartbeat: serverNowOf(t, a).Add(-ago)}
		if _, err := a.DB.C(store.CWorkers).ReplaceOne(ctx, bson.M{"_id": prev.ID}, prev, options.Replace().SetUpsert(true)); err != nil {
			t.Fatal(err)
		}
		restore := testutil.SkewClock(t, skew)
		defer restore()
		w := New(a)
		w.AcceptsHeavy, w.Battery = true, func() power.Battery { return power.Battery{Present: true, Percent: 15, Source: "test"} }
		w.resumeDeferral(ctx)
		return w.deferSince != nil
	}
	if !resumes(10*time.Minute, resumeEpisodeWindow-time.Minute) {
		t.Fatal("a computer ten minutes fast started a new battery episode 14 minutes after its last heartbeat")
	}
	if resumes(-10*time.Minute, resumeEpisodeWindow+time.Minute) {
		t.Fatal("a computer ten minutes slow carried on a battery episode 16 minutes after its last heartbeat")
	}
}

// The battery notice counts the heavy jobs due by the database server's time
// and the other computers online by it (D360): on a computer ten minutes slow
// a job due a second ago waits, and on one ten minutes fast a computer that
// heartbeated 30 s ago still takes it.
func TestTheBatteryNoticeGoesByTheServersTime(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg (the heavy job here is a steady copy)")
	}
	a := testapp.New(t)
	ctx := context.Background()
	ws := a.WorkspaceID()
	heavy, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeMezzanine, IdempotencyKey: "mezzanine:skew", Heavy: true, MaxAttempts: 2, Actor: someone})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": heavy.ID}, bson.M{"$set": bson.M{"nextRunAt": serverNowOf(t, a).Add(-time.Second)}}); err != nil {
		t.Fatal(err)
	}
	w := New(a)
	w.Log, w.AcceptsHeavy = t.Logf, true
	w.Battery = func() power.Battery { return power.Battery{Present: true, Percent: 12, Source: "test"} }
	other := models.WorkerDevice{ID: ids.New(ids.Installation), WorkspaceID: ws, Name: "studio-mac", AcceptsHeavy: true, JobTypes: []string{jobs.TypeMezzanine},
		LastHeartbeat: serverNowOf(t, a).Add(-30 * time.Second)}
	if _, err := a.DB.C(store.CWorkers).ReplaceOne(ctx, bson.M{"_id": other.ID}, other, options.Replace().SetUpsert(true)); err != nil {
		t.Fatal(err)
	}
	told := func() int {
		t.Helper()
		n, err := a.DB.C(store.CNotifications).CountDocuments(ctx, bson.M{"workspaceId": ws, "kind": models.NotifyBatteryPaused})
		if err != nil {
			t.Fatal(err)
		}
		return int(n)
	}
	restore := testutil.SkewClock(t, 10*time.Minute)
	w.trackDeferral(ctx)
	w.lastDeferChk = time.Time{}
	w.trackDeferral(ctx)
	restore()
	if n := told(); n != 0 {
		t.Fatalf("a computer ten minutes fast took a computer seen 30 s ago for gone: told %d", n)
	}
	if _, err := a.DB.C(store.CWorkers).DeleteOne(ctx, bson.M{"_id": other.ID}); err != nil {
		t.Fatal(err)
	}
	testutil.SkewClock(t, -10*time.Minute)
	w.lastDeferChk = time.Time{}
	w.trackDeferral(ctx)
	if n := told(); n != 1 {
		t.Fatalf("a computer ten minutes slow didn't count a heavy job due a second ago: told %d", n)
	}
}

// A person who sets a clip's language on a computer whose clock is ten
// minutes slow, while a transcription started on one ten minutes fast, still
// wins: both times are the database server's (D360).
func TestALanguageSetOnASlowComputerWins(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "quay.wav")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"aevalsrc=0.25*sin(2*PI*330*t):s=48000:d=4", "-c:a", "pcm_s16le", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	r := ingest.File(ctx, a, path, ingest.Options{Via: "cli"})
	if r.AssetID == "" {
		t.Fatalf("ingest: %+v", r)
	}
	en, other := &recognizer{name: "english"}, &recognizer{name: "other"}
	set := false
	var fast func()
	other.during = func() {
		if set {
			return
		}
		set = true
		fast() // the person's computer is ten minutes slow
		testutil.SkewClock(t, -10*time.Minute)
		if _, err := ops.CallTyped[ops.FootageLanguageSetOut](ctx, a, "footage.language.set", ops.FootageLanguageSetIn{AssetID: r.AssetID, Language: "fr"}, ops.Caller{Human: true}); err != nil {
			t.Error(err)
		}
	}
	w := New(a)
	w.Spk = nil
	w.Tx, w.Langs = en, transcribe.LanguagesWith("en", en, other, &heardAs{h: transcribe.Heard{Language: "es", Score: 0.97}})
	fast = testutil.SkewClock(t, 10*time.Minute) // the worker's computer is ten minutes fast
	out, err := transcribeHandler(ctx, w, &models.Job{ID: ids.New(ids.Job), Type: jobs.TypeTranscribe, AssetID: r.AssetID, Payload: map[string]any{"assetId": r.AssetID}}, func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	var as models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, ws, r.AssetID, &as)
	if out["superseded"] != true || as.Language == nil || as.Language.Code != "fr" || as.Language.By != models.LanguagePerson {
		t.Fatalf("the person's choice lost to a computer whose clock is fast: %+v %+v", out, as.Language)
	}
	if d := serverNowOf(t, a).Sub(as.Language.At); d < 0 || d > time.Minute {
		t.Fatalf("the person's choice is stamped %v before the server's now", d)
	}
}
