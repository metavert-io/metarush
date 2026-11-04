package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/looker"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakescreen"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visionmodels"
	"github.com/metavert-io/metarush/internal/visual"
)

// A computer with the open models looks at new footage itself (D405, D408):
// it offers the visual job, says how it looks, and the clip's index says
// the open models made it and what they look for; the insight records them.
func TestOpenModelsLookAtFootageHere(t *testing.T) {
	s := newShop(t)
	staged := fakescreen.Stage(t)
	staged.Apply(&s.a.Cfg.Tools)
	s.a.Tools.Rescan(s.a.Cfg.Tools)
	s.a.Cfg.SetScreenIndexChoice(config.ScreenOpenModels)
	w := s.worker()
	if !w.Can(jobs.TypeVisual) || w.screenIndex() != looker.OpenModels {
		t.Fatalf("the open models: %v %q", w.JobTypes(), w.screenIndex())
	}
	if slices.ContainsFunc(w.Lacks(), func(l models.WorkerLack) bool { return l.JobType == jobs.TypeVisual }) {
		t.Fatalf("lacks %v", w.Lacks())
	}
	ctx := context.Background()
	res := ingest.File(ctx, s.a, s.footage("quay", 4, false, true), ingest.Options{ProjectID: s.project, Via: "cli", Transcribe: false})
	var visualJob, proxy string
	for _, id := range res.JobIDs {
		j, _ := s.a.Queue.Get(ctx, id)
		switch j.Type {
		case jobs.TypeVisual:
			visualJob = id
		case jobs.TypeProxy:
			proxy = id
		}
	}
	if visualJob == "" {
		t.Fatalf("no visual job queued where the open models can look: %v", res.JobIDs)
	}
	for _, j := range runJobs(t, w, proxy, visualJob) {
		if j.State != models.JobSucceeded {
			t.Fatalf("%s: %s %s", j.Type, j.State, j.Error)
		}
		if j.Type == jobs.TypeVisual && j.Result["lookedWith"] != looker.OpenModels {
			t.Fatalf("result %v", j.Result)
		}
	}
	var found []models.Insight
	if err := s.a.DB.Find(ctx, store.CInsights, s.a.WorkspaceID(), bson.M{"assetId": res.AssetID, "kind": visual.InsightKind}, &found); err != nil || len(found) != 1 {
		t.Fatalf("one index: %d %v", len(found), err)
	}
	ins := found[0]
	if ins.Provider != looker.OpenModels || ins.Model != visionmodels.ModelName || ins.PromptVersion != visual.Version {
		t.Fatalf("insight %+v", ins)
	}
	st := visual.Load(ctx, s.a.DB, s.a.WorkspaceID(), res.AssetID)
	if st == nil || !st.Index.ByOpenModels() || st.Index.Looked(looker.SeesLabels) || !st.Index.Looked(looker.SeesFaces) || st.Index.Frames == 0 {
		t.Fatalf("index %+v", st)
	}
	if s.asset(res.AssetID).Analysis[jobs.TypeVisual] != "available" {
		t.Fatalf("analysis %v", s.asset(res.AssetID).Analysis)
	}
}

// A Mac in the workspace sees more in a clip (D410): while one that takes
// big jobs was seen in the last couple of minutes, a computer with the open
// models leaves the clip to it for a while, then looks itself; an Apple
// Vision Mac that went quiet, or this computer's own record, holds nothing.
func TestAMacGetsTheFirstLook(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	w := s.worker()
	mac := func(id string, seen time.Duration, heavy bool) {
		t.Helper()
		_, err := s.a.DB.C(store.CWorkers).UpdateOne(ctx, bson.M{"_id": id},
			bson.M{"$set": bson.M{"workspaceId": s.a.WorkspaceID(), "screenIndex": looker.AppleVision, "acceptsHeavy": heavy, "jobTypes": []string{jobs.TypeVisual}}},
			options.UpdateOne().SetUpsert(true))
		if err != nil {
			t.Fatal(err)
		}
		// The heartbeat in the database server's time, as workers write it.
		if _, err := s.a.DB.C(store.CWorkers).UpdateOne(ctx, bson.M{"_id": id}, bson.A{bson.M{"$set": bson.M{"lastHeartbeat": bson.M{"$subtract": bson.A{"$$NOW", seen.Milliseconds()}}}}}); err != nil {
			t.Fatal(err)
		}
	}
	j := &models.Job{ID: "job_look", Type: jobs.TypeVisual}
	if err := macFirst(ctx, w, j); err != nil {
		t.Fatalf("no Mac about: %v", err)
	}
	mac("ins_quietmac", 10*time.Minute, true)
	if err := macFirst(ctx, w, j); err != nil {
		t.Fatalf("a Mac that went quiet: %v", err)
	}
	mac("ins_busymac", 10*time.Second, false)
	if err := macFirst(ctx, w, j); err != nil {
		t.Fatalf("a Mac not taking big jobs (on battery): %v", err)
	}
	mac(s.a.Cfg.Installation.ID, 5*time.Second, true)
	if err := macFirst(ctx, w, j); err != nil {
		t.Fatalf("this computer's own record: %v", err)
	}
	mac("ins_mac", 5*time.Second, true)
	err := macFirst(ctx, w, j)
	var wait *postponed
	if err == nil || !errors.As(err, &wait) || wait.after != macWait || !strings.Contains(wait.reason, "A Mac in this workspace sees more") {
		t.Fatalf("a Mac about: %v", err)
	}
	// After waiting macWaits times, this computer looks.
	for i := 0; i < macWaits; i++ {
		end := time.Now()
		j.Attempts = append(j.Attempts, models.Attempt{EndedAt: &end, Outcome: jobs.OutcomePostponed})
	}
	j.Attempts = append(j.Attempts, models.Attempt{})
	if err := macFirst(ctx, w, j); err != nil {
		t.Fatalf("after waiting: %v", err)
	}
}

// otherComputer records another computer of the workspace as workers write
// themselves: what it looks with, the job types it offers, what it lacks, and
// a heartbeat that long ago in the database server's time.
func otherComputer(t *testing.T, s *shop, id, looksWith string, jobTypes []string, lacks []models.WorkerLack, seen time.Duration) {
	t.Helper()
	ctx := context.Background()
	set := bson.M{"workspaceId": s.a.WorkspaceID(), "screenIndex": looksWith, "acceptsHeavy": true, "jobTypes": jobTypes, "lacks": lacks}
	if _, err := s.a.DB.C(store.CWorkers).UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set}, options.UpdateOne().SetUpsert(true)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.a.DB.C(store.CWorkers).UpdateOne(ctx, bson.M{"_id": id}, bson.A{bson.M{"$set": bson.M{"lastHeartbeat": bson.M{"$subtract": bson.A{"$$NOW", seen.Milliseconds()}}}}}); err != nil {
		t.Fatal(err)
	}
}

// Only a Mac that can look gets the first look (D446): one that doesn't offer
// the visual job, or lacks what it needs for it, is never waited for.
func TestAMacThatCantLookIsntWaitedFor(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	w := s.worker()
	j := &models.Job{ID: "job_look", Type: jobs.TypeVisual}
	otherComputer(t, s, "ins_nohelper", looker.AppleVision, []string{jobs.TypeProxy}, nil, 5*time.Second)
	otherComputer(t, s, "ins_leftout", looker.AppleVision, []string{jobs.TypeProxy, jobs.TypeVisual},
		[]models.WorkerLack{{JobType: jobs.TypeVisual, Component: looker.Component, Says: "Seeing what's on screen needs the on-screen helper."}}, 5*time.Second)
	if err := macFirst(ctx, w, j); err != nil {
		t.Fatalf("waited for a Mac that can't look: %v", err)
	}
	otherComputer(t, s, "ins_mac", looker.AppleVision, []string{jobs.TypeVisual}, nil, 5*time.Second)
	var wait *postponed
	if err := macFirst(ctx, w, j); !errors.As(err, &wait) {
		t.Fatalf("a Mac that can look: %v", err)
	}
}

// A computer that finds it can't look after all (here: a model changed after
// it was added) stops offering to and hands the clip back for another
// computer that can, the attempt not counted (D446); with none, the clip fails
// with what to do, as before.
func TestAComputerThatCantLookHandsTheClipOn(t *testing.T) {
	s := newShop(t)
	staged := fakescreen.Stage(t)
	staged.Apply(&s.a.Cfg.Tools)
	s.a.Tools.Rescan(s.a.Cfg.Tools)
	s.a.Cfg.SetScreenIndexChoice(config.ScreenOpenModels)
	w := s.worker()
	if !w.Can(jobs.TypeVisual) {
		t.Fatal("the open models are ready here")
	}
	ctx := context.Background()
	res := ingest.File(ctx, s.a, s.footage("pier", 3, false, false), ingest.Options{ProjectID: s.project, Via: "cli", Transcribe: false})
	var visualJob string
	for _, id := range res.JobIDs {
		if j, _ := s.a.Queue.Get(ctx, id); j.Type == jobs.TypeVisual {
			visualJob = id
		}
	}
	f, _ := visionmodels.Store{}.ByID("screen-attention")
	if err := os.WriteFile(filepath.Join(staged.ModelsDir, visionmodels.Folder, f.Name), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Nobody else can look: it fails with the step that fixes it, and this computer stops offering.
	j, _ := s.a.Queue.Get(ctx, visualJob)
	_, err := visualHandler(ctx, w, j, func(float64, string) {})
	var perm *Permanent
	if !errors.As(err, &perm) || !apperr.Is(err, apperr.ToolMissing) || w.Can(jobs.TypeVisual) {
		t.Fatalf("no other computer: %v (offers %v)", err, w.JobTypes())
	}
	if !slices.ContainsFunc(w.Lacks(), func(l models.WorkerLack) bool { return l.JobType == jobs.TypeVisual }) {
		t.Fatalf("it doesn't say it lacks it: %v", w.Lacks())
	}
	// Another computer can: the clip goes back in the queue for it, not counted as a try.
	otherComputer(t, s, "ins_linux", looker.OpenModels, []string{jobs.TypeVisual}, nil, time.Hour)
	w.Handle(jobs.TypeVisual, visualHandler)
	ctx2, stop := context.WithCancel(ctx)
	defer stop()
	w.OnlyJobIDs = []string{visualJob}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx2) }()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		j, _ = s.a.Queue.Get(ctx, visualJob)
		if n := len(j.Attempts); n > 0 && j.Attempts[n-1].Outcome == "released" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not handed back: %s %+v", j.State, j.Attempts)
		}
		time.Sleep(100 * time.Millisecond)
	}
	stop()
	<-done
	if j.State != models.JobQueued || j.AttemptCount != 0 || !strings.Contains(j.Progress.Message, "another computer") || w.Can(jobs.TypeVisual) {
		t.Fatalf("handed back: %s attempts %d %q (offers %v)", j.State, j.AttemptCount, j.Progress.Message, w.JobTypes())
	}
}

// Apple Vision's index of a clip is looked at again only by a Mac while one
// that looks was seen in the last 30 days (D447); after that the open models
// look, keeping what Apple Vision saw that they don't (its labels).
func TestAMacLooksAgainAtWhatAppleVisionSaw(t *testing.T) {
	s := newShop(t)
	staged := fakescreen.Stage(t)
	staged.Apply(&s.a.Cfg.Tools)
	s.a.Tools.Rescan(s.a.Cfg.Tools)
	s.a.Cfg.SetScreenIndexChoice(config.ScreenOpenModels)
	w := s.worker()
	ctx := context.Background()
	res := ingest.File(ctx, s.a, s.footage("harbour", 4, false, false), ingest.Options{ProjectID: s.project, Via: "cli", Transcribe: false})
	var visualJob, proxy string
	for _, id := range res.JobIDs {
		j, _ := s.a.Queue.Get(ctx, id)
		switch j.Type {
		case jobs.TypeVisual:
			visualJob = id
		case jobs.TypeProxy:
			proxy = id
		}
	}
	for _, j := range runJobs(t, w, proxy) {
		if j.State != models.JobSucceeded {
			t.Fatalf("%s: %s", j.Type, j.Error)
		}
	}
	// The clip's index as Apple Vision made it (before the backend was recorded): a dog on the harbour.
	as := s.asset(res.AssetID)
	apple := visual.Build([]visual.Sample{{At: 0.5, Frame: vision.Frame{W: 320, H: 240, Labels: []vision.Label{{ID: "dog", Conf: 0.95, Precise: true}}}},
		{At: 1.5, Frame: vision.Frame{W: 320, H: 240, Labels: []vision.Label{{ID: "dog", Conf: 0.95, Precise: true}}}}}, 1, 4, nil, "proxy")
	if err := s.a.DB.Insert(ctx, store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: s.a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind,
		Provider: "apple-vision", PromptVersion: "vision-1", Digest: "d", Data: visual.ToMap(apple), CreatedAt: time.Now().UTC(), Media: as.MediaSHA()}); err != nil {
		t.Fatal(err)
	}
	// A Mac that looks was here three days ago: the clip waits for it, a little longer each time.
	otherComputer(t, s, "ins_mac", looker.AppleVision, []string{jobs.TypeVisual}, nil, 72*time.Hour)
	j, _ := s.a.Queue.Get(ctx, visualJob)
	_, err := visualHandler(ctx, w, j, func(float64, string) {})
	var wait *postponed
	if !errors.As(err, &wait) || wait.after != macWait || !strings.Contains(wait.reason, "a Mac in this workspace looks at it again") {
		t.Fatalf("a Mac seen three days ago: %v", err)
	}
	j.Attempts = []models.Attempt{{Outcome: jobs.OutcomePostponed}, {Outcome: jobs.OutcomePostponed}, {}}
	if err := macAgain(ctx, w, j); !errors.As(err, &wait) || wait.after != 4*macWait {
		t.Fatalf("waiting again: %v", err)
	}
	// Last seen 40 days ago: this computer looks, and keeps the dog.
	otherComputer(t, s, "ins_mac", looker.AppleVision, []string{jobs.TypeVisual}, nil, 40*24*time.Hour)
	for _, j := range runJobs(t, w, visualJob) {
		if j.State != models.JobSucceeded {
			t.Fatalf("%s: %s", j.Type, j.Error)
		}
	}
	st := visual.Load(ctx, s.a.DB, s.a.WorkspaceID(), as.ID)
	if st == nil || !st.Index.ByOpenModels() || len(st.Index.Kept) == 0 || !st.Index.Looked(looker.SeesLabels) ||
		!slices.ContainsFunc(st.Index.Labels, func(l visual.LabelSpan) bool { return l.Label == "dog" }) {
		t.Fatalf("the new index: %+v", st)
	}
}

// A Mac whose person left Seeing what's on screen out, with no helper built,
// doesn't offer the visual job or say it looks with Apple Vision (so no
// computer waits for it), and says what it lacks (D446).
func TestALeftOutMacDoesntOfferToLook(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple Vision runs only on a Mac")
	}
	s := newShop(t)
	// FFmpeg and Swift here (the helper would be built on first use), no helper.
	dir := t.TempDir()
	for _, p := range []string{"ffmpeg", "ffprobe", "swiftc"} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(tools.EnvToolDirs, dir)
	t.Setenv("METARUSH_VISION_CACHE", t.TempDir())
	s.a.Cfg.Tools.FFmpeg, s.a.Cfg.Tools.FFprobe, s.a.Cfg.Tools.Vision = filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "ffprobe"), ""
	s.a.Tools.Rescan(s.a.Cfg.Tools)
	if err := choice.Set(s.a.Cfg.Home(), map[string]bool{looker.Component: false}, "Sam"); err != nil {
		t.Fatal(err)
	}
	w := s.worker()
	if s.a.Tools.Has(tools.Vision) {
		t.Skip("a helper is configured on this computer")
	}
	if w.Can(jobs.TypeVisual) || w.screenIndex() != "" || !slices.ContainsFunc(w.Lacks(), func(l models.WorkerLack) bool { return l.JobType == jobs.TypeVisual }) {
		t.Fatalf("offers %v, looks with %q, lacks %v", w.JobTypes(), w.screenIndex(), w.Lacks())
	}
}

// A fixed copy's first look isn't a look again (D467): Apple Vision looked
// only at the original, so a Mac seen three days ago doesn't hold the copy —
// only one about now gets the first look, as at any clip — and the open models
// look, keeping the original's labels but not its gestures (where a hand was
// in the original's picture). The original's index stays as it was; an Apple
// Vision index of the copy itself is looked at again by a Mac.
func TestAFixedCopysFirstLookIsntALookAgain(t *testing.T) {
	s := newShop(t)
	staged := fakescreen.Stage(t)
	staged.Apply(&s.a.Cfg.Tools)
	s.a.Tools.Rescan(s.a.Cfg.Tools)
	s.a.Cfg.SetScreenIndexChoice(config.ScreenOpenModels)
	w := s.worker()
	ctx := context.Background()
	res := ingest.File(ctx, s.a, s.footage("jetty", 4, false, false), ingest.Options{ProjectID: s.project, Via: "cli", Transcribe: false})
	var visualJob, proxy string
	for _, id := range res.JobIDs {
		j, _ := s.a.Queue.Get(ctx, id)
		switch j.Type {
		case jobs.TypeVisual:
			visualJob = id
		case jobs.TypeProxy:
			proxy = id
		}
	}
	for _, j := range runJobs(t, w, proxy) {
		if j.State != models.JobSucceeded {
			t.Fatalf("%s: %s", j.Type, j.Error)
		}
	}
	// Apple Vision's index of the original: a dog, and a hand pointing left.
	as := s.asset(res.AssetID)
	apple := visual.Build([]visual.Sample{{At: 0.5, Frame: vision.Frame{W: 320, H: 240, Labels: []vision.Label{{ID: "dog", Conf: 0.95, Precise: true}}}},
		{At: 1.5, Frame: vision.Frame{W: 320, H: 240, Labels: []vision.Label{{ID: "dog", Conf: 0.95, Precise: true}}}}}, 1, 4, nil, "proxy")
	apple.Gestures = []visual.GestureSpan{{Kind: visual.GesturePoint, From: 1, To: 2, Toward: "left", Box: vision.Box{0.1, 0.4, 0.1, 0.1}, Conf: 0.9}}
	appleInsight := func(media string) {
		t.Helper()
		if err := s.a.DB.Insert(ctx, store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: s.a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind,
			Provider: "apple-vision", PromptVersion: "vision-1", Digest: "d", Data: visual.ToMap(apple), CreatedAt: time.Now().UTC(), Media: media}); err != nil {
			t.Fatal(err)
		}
	}
	appleInsight("")
	// The clip now uses a steadied copy, which nothing has looked at.
	copySHA := strings.Repeat("c", 64)
	if _, err := s.a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": as.ID}, bson.M{"$set": bson.M{"fixes.copy": models.FixedCopyRef{ID: "copy_steady", SHA256: copySHA,
		StorageKey: as.StorageKey, Size: as.Size, MIME: "video/mp4", Fixes: []string{models.FixSteady}, At: time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	// A Mac about now gets the first look, as at any clip.
	otherComputer(t, s, "ins_mac", looker.AppleVision, []string{jobs.TypeVisual}, nil, 5*time.Second)
	j, _ := s.a.Queue.Get(ctx, visualJob)
	_, err := visualHandler(ctx, w, j, func(float64, string) {})
	var wait *postponed
	if !errors.As(err, &wait) || wait.after != macWait || !strings.Contains(wait.reason, "gets the first look") {
		t.Fatalf("a Mac about now: %v", err)
	}
	// One seen three days ago doesn't hold the copy, as it would a look again at Apple Vision's index.
	otherComputer(t, s, "ins_mac", looker.AppleVision, []string{jobs.TypeVisual}, nil, 72*time.Hour)
	for _, j := range runJobs(t, w, visualJob) {
		if j.State != models.JobSucceeded {
			t.Fatalf("the copy's first look: %s %s (%+v)", j.State, j.Error, j.Attempts)
		}
	}
	look, err := visual.LoadMedia(ctx, s.a.DB, s.a.WorkspaceID(), as.ID, copySHA)
	if err != nil || look == nil || !look.Index.ByOpenModels() {
		t.Fatalf("the copy's index: %+v %v", look, err)
	}
	if !slices.ContainsFunc(look.Index.Labels, func(l visual.LabelSpan) bool { return l.Label == "dog" }) || !look.Index.Looked(looker.SeesLabels) {
		t.Fatalf("the original's labels didn't come over: %+v (kept %v)", look.Index.Labels, look.Index.Kept)
	}
	if len(look.Index.Gestures) != 0 || look.Index.Looked("gestures") {
		t.Fatalf("the original's gestures came over: %+v (kept %v)", look.Index.Gestures, look.Index.Kept)
	}
	orig, err := visual.LoadMedia(ctx, s.a.DB, s.a.WorkspaceID(), as.ID, "")
	if err != nil || orig == nil || orig.Index.ByOpenModels() || len(orig.Index.Gestures) != 1 {
		t.Fatalf("the original's index changed: %+v %v", orig, err)
	}
	if st := visual.Load(ctx, s.a.DB, s.a.WorkspaceID(), as.ID); st == nil || !st.Index.ByOpenModels() {
		t.Fatalf("the clip doesn't show the copy's index: %+v", st)
	}
	// Looking at the copy again keeps what its first look kept, and still waits for no Mac seen days ago.
	j, _ = s.a.Queue.Get(ctx, visualJob)
	if _, err := visualHandler(ctx, w, j, func(float64, string) {}); err != nil {
		t.Fatalf("looking again at the copy: %v", err)
	}
	if again, err := visual.LoadMedia(ctx, s.a.DB, s.a.WorkspaceID(), as.ID, copySHA); err != nil || again == nil ||
		!slices.ContainsFunc(again.Index.Labels, func(l visual.LabelSpan) bool { return l.Label == "dog" }) || len(again.Index.Gestures) != 0 {
		t.Fatalf("the copy looked at again: %+v %v", again, err)
	}
	// Apple Vision's own index of the copy is looked at again by a Mac.
	if _, err := s.a.DB.C(store.CInsights).DeleteMany(ctx, bson.M{"assetId": as.ID, "media": copySHA}); err != nil {
		t.Fatal(err)
	}
	appleInsight(copySHA)
	if _, err := visualHandler(ctx, w, j, func(float64, string) {}); !errors.As(err, &wait) || !strings.Contains(wait.reason, "a Mac in this workspace looks at it again") {
		t.Fatalf("Apple Vision's index of the copy: %v", err)
	}
}
