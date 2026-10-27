package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/power"
	"github.com/metavert-io/metarush/internal/qa"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakenode"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// shop is a test installation with a project; its footage is made with ffmpeg.
type shop struct {
	t       *testing.T
	a       *app.App
	project string
	head    string // the cut's latest version
}

func newShop(t *testing.T) *shop {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	a := testapp.New(t)
	p, err := ops.CallTyped[ops.ProjectOut](context.Background(), a, "project.create", ops.ProjectCreateIn{Name: "Harbour film"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	return &shop{t: t, a: a, project: p.Project.ID}
}

// footage makes a clip: a test pattern (then a plain colour from halfway, when
// cut) with a tone, or the picture alone.
func (s *shop) footage(name string, seconds float64, cut, sound bool) string {
	s.t.Helper()
	p := filepath.Join(s.t.TempDir(), name+".mp4")
	// Inputs first, then what goes into the file.
	in := []string{"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=320x240:rate=25:duration=%g", seconds)}
	out := []string{"-map", "0:v"}
	if cut {
		half := seconds / 2
		in = []string{"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=320x240:rate=25:duration=%g", half),
			"-f", "lavfi", "-i", fmt.Sprintf("color=c=0x1f4fbf:size=320x240:rate=25:duration=%g", half)}
		out = []string{"-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]", "-map", "[v]"}
	}
	if sound {
		n := len(in) / 4 // each input is four arguments
		in = append(in, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=330:duration=%g", seconds))
		out = append(out, "-map", fmt.Sprintf("%d:a", n), "-c:a", "aac")
	}
	args := append(append([]string{"-hide_banner", "-loglevel", "error", "-y"}, in...), out...)
	args = append(args, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-t", fmt.Sprint(seconds), "-metadata", "comment="+name, p)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		s.t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return p
}

func (s *shop) asset(id string) *models.Asset {
	s.t.Helper()
	var as models.Asset
	if err := s.a.DB.GetByID(context.Background(), store.CAssets, s.a.WorkspaceID(), id, &as); err != nil {
		s.t.Fatal(err)
	}
	return &as
}

// worker is this machine's worker, kept from what this computer happens to
// have: plugged in, no voice models, a transcriber that always hears one sentence.
func (s *shop) worker() *Worker {
	w := New(s.a)
	w.Log = s.t.Logf
	w.Concurrency = 2
	w.AcceptsHeavy = true
	w.Battery = func() power.Battery {
		return power.Battery{Present: true, Percent: 80, OnAC: true, Charging: true, Source: "test"}
	}
	w.Spk = nil
	w.Tx, w.Langs = &oneSentence{}, nil
	w.Handle(jobs.TypeTranscribe, transcribeHandler)
	return w
}

// runJobs runs a worker on these jobs only (as a terminal waiting for them
// does) until each has ended, and returns them as they ended.
func runJobs(t *testing.T, w *Worker, jobIDs ...string) map[string]*models.Job {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	w.OnlyJobIDs = jobIDs
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() {
		stop()
		<-done
	}()
	end := time.Now().Add(3 * time.Minute)
	for {
		out := map[string]*models.Job{}
		for _, id := range jobIDs {
			j, err := w.App.Queue.Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if j.State == models.JobQueued || j.State == models.JobRunning {
				break
			}
			out[id] = j
		}
		if len(out) == len(jobIDs) {
			return out
		}
		if time.Now().After(end) {
			t.Fatalf("jobs never ended: %v (ended: %d)", jobIDs, len(out))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// jobsOf lists an asset's or a render's jobs of one type.
func (s *shop) jobsOf(typ string, filter bson.M) []models.Job {
	s.t.Helper()
	filter["type"] = typ
	var js []models.Job
	if err := s.a.DB.Find(context.Background(), store.CJobs, s.a.WorkspaceID(), filter, &js); err != nil {
		s.t.Fatal(err)
	}
	return js
}

// New footage is looked at by the worker of the machine it landed on: the
// preview copy, the thumbnail (the project's cover), the frame grid and the
// sound wave are made and stored, the evidence is found, the words written
// down (voices unavailable here, and the clip says so), "file ready" is sent
// once, and the evidence's cut asks for the picture switches, found to the frame.
func TestNewFootageIsLookedAtHere(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	res := ingest.File(ctx, s.a, s.footage("harbour", 6, true, true), ingest.Options{ProjectID: s.project, Via: "cli", Transcribe: true})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	var here []string
	for _, id := range res.JobIDs {
		j, err := s.a.Queue.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		// What's on screen and scene descriptions need a Mac's Vision and Claude Code.
		if j.Type != jobs.TypeVisual && j.Type != jobs.TypeDescribe {
			here = append(here, id)
		}
	}
	w := s.worker()
	ran := map[string]bool{}
	for _, j := range runJobs(t, w, here...) {
		if j.State != models.JobSucceeded {
			t.Fatalf("%s %s: %s", j.Type, j.State, j.Error)
		}
		ran[j.Type] = true
	}
	for _, typ := range []string{jobs.TypeThumbnail, jobs.TypeProxy, jobs.TypeContactSheet, jobs.TypeWaveform, jobs.TypeEvidence, jobs.TypeTranscribe} {
		if !ran[typ] {
			t.Errorf("no %s job ran", typ)
		}
	}
	as := s.asset(res.AssetID)
	for _, kind := range []string{models.VariantProxy, models.VariantThumbnail, models.VariantContactSheet, models.VariantPeaks} {
		v, ok := as.Variants[kind]
		if !ok || v.StorageKey == "" || v.SHA256 == "" || v.Size == 0 {
			t.Errorf("variant %s: %+v", kind, v)
			continue
		}
		if info, err := s.a.Media.Stat(ctx, v.StorageKey); err != nil || info.Size != v.Size {
			t.Errorf("variant %s isn't in the media store: %+v %v", kind, info, err)
		}
	}
	if as.Analysis["evidence"] != "available" {
		t.Fatalf("evidence %v", as.Analysis)
	}
	if n, _ := s.a.DB.C(store.CInsights).CountDocuments(ctx, bson.M{"assetId": as.ID, "kind": "technical"}); n != 1 {
		t.Fatalf("%d technical insights", n)
	}
	if as.Transcript == nil || as.Transcript.State != "available" || as.Transcript.Provider != "fake" || as.Transcript.Words != 7 || as.Transcript.Precision != "word" {
		t.Fatalf("transcript %+v", as.Transcript)
	}
	if as.Speakers == nil || as.Speakers.State != "unavailable" || len(plainwords.Check(as.Speakers.Error)) > 0 {
		t.Fatalf("voices on a machine without them: %+v", as.Speakers)
	}
	var p models.Project
	if err := s.a.DB.GetByID(ctx, store.CProjects, s.a.WorkspaceID(), s.project, &p); err != nil || p.CoverAssetID != as.ID {
		t.Fatalf("the project's cover: %q %v", p.CoverAssetID, err)
	}
	if n, _ := s.a.DB.C(store.CNotifications).CountDocuments(ctx, bson.M{"kind": models.NotifyFileReady, "assetId": as.ID}); n != 1 {
		t.Fatalf("file ready sent %d times", n)
	}
	// The cut halfway asks for the picture switches; they are found to the frame.
	sw := s.jobsOf(jobs.TypeSwitches, bson.M{"assetId": as.ID})
	if len(sw) != 1 {
		t.Fatalf("switches jobs: %d", len(sw))
	}
	j := runJobs(t, w, sw[0].ID)[sw[0].ID]
	if j.State != models.JobSucceeded || j.Result["switches"] == nil {
		t.Fatalf("switches: %+v", j)
	}
	if as = s.asset(as.ID); as.Analysis["switches"] != "available" {
		t.Fatalf("switches analysis %v", as.Analysis)
	}
	// The steady copy (asked for footage whose frame rate varies) is made the same way.
	mz, _, err := s.a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeMezzanine, IdempotencyKey: "mezzanine:" + as.ID, Heavy: true, AssetID: as.ID, Payload: map[string]any{"assetId": as.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if j := runJobs(t, w, mz.ID)[mz.ID]; j.State != models.JobSucceeded {
		t.Fatalf("steady copy: %+v", j)
	}
	if v, ok := s.asset(as.ID).Variants[models.VariantMezzanine]; !ok || v.StorageKey == "" {
		t.Fatalf("steady copy variant %+v", v)
	}
}

// What can't be looked at is said: a clip with no sound has nothing to write
// down (failed for good, and the clip says so), a clip that is gone fails at
// once, a clip still being added is tried again later.
func TestFootageThatCantBeLookedAt(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	silent := ingest.File(ctx, s.a, s.footage("silent", 3, false, false), ingest.Options{ProjectID: s.project, Via: "cli"})
	if silent.AssetID == "" {
		t.Fatalf("ingest %+v", silent)
	}
	enqueue := func(typ, assetID string) *models.Job {
		t.Helper()
		j, _, err := s.a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: typ, IdempotencyKey: typ + ":again:" + assetID, AssetID: assetID, Payload: map[string]any{"assetId": assetID}, MaxAttempts: 3})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	w := s.worker()
	tx := enqueue(jobs.TypeTranscribe, silent.AssetID)
	if j := runJobs(t, w, tx.ID)[tx.ID]; j.State != models.JobFailed || j.AttemptCount != 1 || !strings.Contains(j.Error, "no audio") {
		t.Fatalf("a clip without sound: %+v", j)
	}
	if as := s.asset(silent.AssetID); as.Analysis[jobs.TypeTranscribe] != "failed" || as.Transcript == nil || as.Transcript.State != "failed" {
		t.Fatalf("the clip says so: %v %+v", as.Analysis, as.Transcript)
	}
	gone := enqueue(jobs.TypeThumbnail, "ast_00000000000000000000000000")
	if j := runJobs(t, w, gone.ID)[gone.ID]; j.State != models.JobFailed || j.AttemptCount != 1 || !strings.Contains(j.Error, "not found") {
		t.Fatalf("a clip that is gone: %+v", j)
	}
	// Still being added: back in the queue for another try, not failed.
	if err := s.a.DB.Update(ctx, store.CAssets, s.a.WorkspaceID(), silent.AssetID, bson.M{"$set": bson.M{"state": models.AssetStaging}}); err != nil {
		t.Fatal(err)
	}
	busy := enqueue(jobs.TypeProxy, silent.AssetID)
	me := jobs.Worker{ID: s.a.Cfg.Installation.ID, Name: s.a.Cfg.Installation.Name, JobTypes: w.JobTypes()}
	lj, err := s.a.Queue.LeaseByID(ctx, me, busy.ID)
	if err != nil || lj == nil {
		t.Fatalf("lease: %+v %v", lj, err)
	}
	again := w.execute(ctx, lj)
	got, _ := s.a.Queue.Get(ctx, busy.ID)
	if again.IsZero() || got.State != models.JobQueued || got.AttemptCount != 1 || !strings.Contains(got.Error, "not yet available") || !again.Equal(got.NextRunAt) {
		t.Fatalf("a clip still being added: %+v (again %v)", got, again)
	}
}

// A render is drawn by this version's renderer (here a stand-in that draws a
// test pattern), checked (its length and size), stored with its thumbnail and
// recorded; then it gets its quality check, for the person who asked, which
// watches and listens to it and stores the report on the render.
func TestRenderThenItsQualityCheck(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	fake := s.renderer()
	res := ingest.File(ctx, s.a, s.footage("harbour", 4, false, true), ingest.Options{ProjectID: s.project, Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	w := s.worker()
	// The evidence the check reads about the footage.
	for _, id := range res.JobIDs {
		if j, _ := s.a.Queue.Get(ctx, id); j.Type == jobs.TypeEvidence {
			runJobs(t, w, id)
		}
	}
	rev := s.cut(res.AssetID, 0, 2)
	r, err := ops.CallTyped[ops.RenderOut](ctx, s.a, "render.submit", ops.RenderSubmitIn{RevisionID: rev, Quality: "review"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if j := runJobs(t, w, r.Job.ID)[r.Job.ID]; j.State != models.JobSucceeded || j.Result["width"] == nil {
		t.Fatalf("render job %+v", j)
	}
	var rd models.Render
	if err := s.a.DB.GetByID(ctx, store.CRenders, s.a.WorkspaceID(), r.Render.ID, &rd); err != nil {
		t.Fatal(err)
	}
	pkg, _ := renderer.Embedded()
	if rd.State != models.RenderAvailable || rd.Output == nil || rd.Output.Width != 540 || rd.Output.Height != 960 || rd.Output.Duration < 1.9 || rd.Output.Duration > 2.1 ||
		rd.Output.ThumbnailKey == "" || rd.RendererVersion != "remotion "+pkg.Remotion() || rd.TemplateVersions["title.card"] != "1" || rd.RenderedByName != "test-machine" {
		t.Fatalf("render %+v %+v", rd, rd.Output)
	}
	if info, err := s.a.Media.Stat(ctx, rd.Output.StorageKey); err != nil || info.Size != rd.Output.Size {
		t.Fatalf("the render isn't stored: %+v %v", info, err)
	}
	if fake.Count(t, "render") != 1 {
		t.Fatalf("rendered %d times", fake.Count(t, "render"))
	}
	// Its quality check, keyed like `metarush qa run`.
	checks := s.jobsOf(jobs.TypeQA, bson.M{"payload.renderId": rd.ID})
	if len(checks) != 1 || checks[0].IdempotencyKey != "qa:"+rd.ID+":"+qa.Version || checks[0].Payload["auto"] != true || checks[0].CreatedBy.UserID == "" {
		t.Fatalf("quality check queued: %+v", checks)
	}
	j := runJobs(t, w, checks[0].ID)[checks[0].ID]
	if j.State != models.JobSucceeded {
		t.Fatalf("quality check %s: %s", j.State, j.Error)
	}
	reportID, _ := j.Result["reportId"].(string)
	var rep models.QAReport
	if err := s.a.DB.GetByID(ctx, store.CQAReports, s.a.WorkspaceID(), reportID, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.RenderID != rd.ID || rep.RevisionID != rev || rep.RenderSHA256 != rd.Output.SHA256 || rep.Verdict == "" || rep.RunBy != "test-machine" || rep.Quality != "review" {
		t.Fatalf("report %+v", rep)
	}
	if err := s.a.DB.GetByID(ctx, store.CRenders, s.a.WorkspaceID(), rd.ID, &rd); err != nil || rd.QA == nil || rd.QA.ReportID != reportID || rd.QA.Verdict != rep.Verdict {
		t.Fatalf("the render's check: %+v %v", rd.QA, err)
	}
	if n, _ := s.a.DB.C(store.CNotifications).CountDocuments(ctx, bson.M{"kind": models.NotifyQADone}); n != 1 {
		t.Fatalf("%d quality-check notifications", n)
	}
	// Checking again is the same check: nothing new is queued.
	w.queueQA(ctx, &models.Job{RenderID: rd.ID, CreatedBy: j.CreatedBy})
	if n := len(s.jobsOf(jobs.TypeQA, bson.M{"payload.renderId": rd.ID})); n != 1 {
		t.Fatalf("%d quality checks", n)
	}
}

// A render that comes out the wrong size or the wrong length is not kept: the
// render says so plainly, the job fails for good (no retry would draw it
// differently), the person is told.
func TestARenderThatComesOutWrongIsNotKept(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	fake := s.renderer()
	res := ingest.File(ctx, s.a, s.footage("harbour", 4, false, true), ingest.Options{ProjectID: s.project, Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	w := s.worker()
	for _, c := range []struct {
		file, value string
		in, out     float64
		says        string
	}{
		{"render-size", "320x180", 0, 2, "came out the wrong size"},
		{"render-seconds", "4", 1, 3, "came out a different length"},
	} {
		if err := os.WriteFile(filepath.Join(fake.Dir, c.file), []byte(c.value), 0o644); err != nil {
			t.Fatal(err)
		}
		rev := s.cut(res.AssetID, c.in, c.out)
		r, err := ops.CallTyped[ops.RenderOut](ctx, s.a, "render.submit", ops.RenderSubmitIn{RevisionID: rev, Quality: "review", NoQA: true}, ops.Caller{Human: true})
		if err != nil {
			t.Fatal(err)
		}
		j := runJobs(t, w, r.Job.ID)[r.Job.ID]
		if j.State != models.JobFailed || j.AttemptCount != 1 {
			t.Fatalf("%s: job %+v", c.file, j)
		}
		var rd models.Render
		if err := s.a.DB.GetByID(ctx, store.CRenders, s.a.WorkspaceID(), r.Render.ID, &rd); err != nil {
			t.Fatal(err)
		}
		if rd.State != models.RenderFailed || !strings.Contains(rd.Error, c.says) || !strings.HasSuffix(rd.Error, "Render it again.") || len(plainwords.Check(rd.Error)) > 0 {
			t.Fatalf("%s: render %q %q", c.file, rd.State, rd.Error)
		}
		if n, _ := s.a.DB.C(store.CNotifications).CountDocuments(ctx, bson.M{"kind": models.NotifyRenderFailed, "renderId": rd.ID}); n != 1 {
			t.Fatalf("%s: %d notifications", c.file, n)
		}
		_ = os.Remove(filepath.Join(fake.Dir, c.file))
	}
}

// renderer sets this version's renderer up with a stand-in node (fakenode).
func (s *shop) renderer() *fakenode.Fake {
	s.t.Helper()
	pkg, err := renderer.Embedded()
	if err != nil {
		s.t.Fatal(err)
	}
	fake := fakenode.Write(s.t, pkg.Remotion())
	s.a.Cfg.Tools.Node, s.a.Cfg.Tools.NPM = fake.Node, fake.NPM
	s.a.Tools = tools.New(s.a.Cfg.Tools)
	if _, err := renderer.New(s.a.Cfg, s.a.Tools).Install(context.Background(), renderer.InstallOptions{}); err != nil {
		s.t.Fatal(err)
	}
	return fake
}

// cut saves a version playing [in, out) of a clip (vertical, 30 fps).
func (s *shop) cut(assetID string, in, out float64) string {
	s.t.Helper()
	base := ""
	if s.head != "" {
		base = fmt.Sprintf(`"baseRevisionId":%q,`, s.head)
	}
	plan := fmt.Sprintf(`{"projectId":%q,%s"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":%g,"out":%g}]}}`,
		s.project, base, assetID, in, out)
	ctx := context.Background()
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, s.a, "edit.check", ops.EditCheckIn{Plan: json.RawMessage(plan)}, ops.Caller{})
	if err != nil || !chk.Valid {
		s.t.Fatalf("check: %+v %v", chk, err)
	}
	res, err := ops.CallTyped[ops.EditApplyOut](ctx, s.a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken}, ops.Caller{})
	if err != nil {
		s.t.Fatal(err)
	}
	s.head = res.Revision.ID
	return res.Revision.ID
}
