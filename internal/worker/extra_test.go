package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// A fix's copy is made by the worker (D152): the inspect job measures the
// clip, ticking the hum fix queues the prepare job, which makes the fixed copy
// the clip plays from then on; what is derived afterwards belongs to that copy,
// and a variant made from media the clip no longer plays goes where that
// media's variants are kept.
func TestAFixedCopyIsMadeByTheWorker(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "kitchen-voice.wav")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"aevalsrc=0.25*sin(2*PI*220*t)*(0.6+0.4*sin(2*PI*3*t))+0.08*sin(2*PI*60*t)+0.05*sin(2*PI*120*t)+0.03*sin(2*PI*180*t):s=48000:d=6",
		"-c:a", "pcm_s16le", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	res := ingest.File(ctx, s.a, path, ingest.Options{ProjectID: s.project, Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	w := s.worker()
	ij, err := fixes.QueueInspect(ctx, s.a, s.asset(res.AssetID), false)
	if err != nil {
		t.Fatal(err)
	}
	if j := runJobs(t, w, ij.ID)[ij.ID]; j.State != models.JobSucceeded {
		t.Fatalf("inspect: %+v", j)
	}
	v, err := ops.CallTyped[fixes.View](ctx, s.a, "asset.fixes.set", fixes.SetIn{ID: res.AssetID, Tick: []string{models.FixHum}}, ops.Caller{Human: true})
	if err != nil || len(v.JobIDs) != 1 {
		t.Fatalf("ticking the hum fix: %+v %v", v, err)
	}
	if j := runJobs(t, w, v.JobIDs[0])[v.JobIDs[0]]; j.State != models.JobSucceeded {
		t.Fatalf("prepare: %s %s", j.State, j.Error)
	}
	as := s.asset(res.AssetID)
	if !as.Fixed() || as.Fixes.Copy == nil || as.MediaSHA() != as.Fixes.Copy.SHA256 {
		t.Fatalf("the clip plays its fixed copy: %+v", as.Fixes)
	}
	// Its sound wave is made again, from the fixed copy, and kept on the clip.
	wf, _, err := s.a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeWaveform, IdempotencyKey: "waveform:fixed:" + as.ID, AssetID: as.ID, Payload: map[string]any{"assetId": as.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if j := runJobs(t, w, wf.ID)[wf.ID]; j.State != models.JobSucceeded {
		t.Fatalf("sound wave of the fixed copy: %+v", j)
	}
	if pk, ok := s.asset(as.ID).Variants[models.VariantPeaks]; !ok || pk.SHA256 == "" {
		t.Fatalf("the fixed copy's sound wave: %+v", pk)
	}
	// Made from the original while the fixed copy is what plays: the original's.
	orig := *as
	orig.Fixes = nil
	if err := storeVariant(ctx, s.a, &orig, &models.Variant{Kind: models.VariantThumbnail, StorageKey: "k-orig", SHA256: "sha-orig"}); err != nil {
		t.Fatal(err)
	}
	if got := s.asset(as.ID); got.Fixes.OriginalVariants[models.VariantThumbnail].SHA256 != "sha-orig" || got.Variants[models.VariantThumbnail].SHA256 == "sha-orig" {
		t.Fatalf("the original's variant: %+v", got.Fixes.OriginalVariants)
	}
	// Made from a fixed copy the clip switched away from: kept with that copy.
	stale := *as
	fx := *as.Fixes
	cp := *as.Fixes.Copy
	cp.SHA256 = strings.Repeat("e", 64)
	fx.Copy = &cp
	stale.Fixes = &fx
	if err := storeVariant(ctx, s.a, &stale, &models.Variant{Kind: models.VariantThumbnail, StorageKey: "k-old", SHA256: "sha-old"}); err != nil {
		t.Fatal(err)
	}
	var copyDoc struct {
		Variants map[string]models.Variant `bson:"variants"`
	}
	if err := s.a.DB.C(store.CSourceCopies).FindOne(ctx, bson.M{"_id": cp.ID}).Decode(&copyDoc); err != nil {
		t.Fatal(err)
	}
	if copyDoc.Variants[models.VariantThumbnail].SHA256 != "sha-old" {
		t.Fatalf("the old copy keeps its variant: %+v", copyDoc.Variants)
	}
	if got := s.asset(as.ID); got.Variants[models.VariantThumbnail].SHA256 == "sha-old" {
		t.Fatal("a variant of another copy landed on the clip")
	}
}

// A renderer that stops with an error is tried again (the job goes back in the
// queue, the try counted); the next try draws it.
func TestARendererErrorIsTriedAgain(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	defer func(b time.Duration) { jobs.RetryBase = b }(jobs.RetryBase)
	jobs.RetryBase = 300 * time.Millisecond
	fake := s.renderer()
	res := ingest.File(ctx, s.a, s.footage("pier", 3, false, true), ingest.Options{ProjectID: s.project, Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	r, err := ops.CallTyped[ops.RenderOut](ctx, s.a, "render.submit", ops.RenderSubmitIn{RevisionID: s.cut(res.AssetID, 0, 2), Quality: "review", NoQA: true}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fake.Dir, "render-size"), []byte("not-a-size"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := s.worker()
	me := jobs.Worker{ID: s.a.Cfg.Installation.ID, Name: s.a.Cfg.Installation.Name, JobTypes: w.JobTypes(), AcceptsHeavy: true}
	lj, err := s.a.Queue.LeaseByID(ctx, me, r.Job.ID)
	if err != nil || lj == nil {
		t.Fatalf("lease: %+v %v", lj, err)
	}
	again := w.execute(ctx, lj)
	j, _ := s.a.Queue.Get(ctx, r.Job.ID)
	if again.IsZero() || j.State != models.JobQueued || j.AttemptCount != 1 || !strings.Contains(j.Error, "fake render failed") {
		t.Fatalf("a renderer error: %+v", j)
	}
	_ = os.Remove(filepath.Join(fake.Dir, "render-size"))
	if j := runJobs(t, w, r.Job.ID)[r.Job.ID]; j.State != models.JobSucceeded || j.AttemptCount != 2 {
		t.Fatalf("the next try: %+v", j)
	}
}

// A quality check that can't run says why, and doesn't try again: the render
// is gone, isn't finished, or its version is gone.
func TestAQualityCheckWithoutItsRender(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	w := New(a)
	nop := func(float64, string) {}
	unfinished := insertRender(t, a, models.RenderRendering, false)
	orphan := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), RevisionID: ids.New(ids.Revision), State: models.RenderAvailable,
		Output: &models.RenderOutput{StorageKey: "k", SHA256: "s", Size: 1}, CreatedAt: time.Now().UTC()}
	if err := a.DB.Insert(ctx, store.CRenders, orphan); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ renderID, says string }{
		{ids.New(ids.Render), "not found"},
		{unfinished, "not available"},
		{orphan.ID, "revision"},
	} {
		_, err := qaHandler(ctx, w, &models.Job{ID: ids.New(ids.Job), Payload: map[string]any{"renderId": c.renderID}}, nop)
		var p *Permanent
		if !errors.As(err, &p) || !strings.Contains(err.Error(), c.says) {
			t.Errorf("render %s: %v", c.renderID, err)
		}
	}
}

// hinted is a transcriber that takes spelling hints and hears "Fern Brook".
type hinted struct {
	oneSentence
	mu    sync.Mutex
	hints []string
}

func (h *hinted) TranscribeHinted(ctx context.Context, wav, scratch, language string, hints []string) (*transcribe.Transcript, error) {
	h.mu.Lock()
	h.hints = append(h.hints, hints...)
	h.mu.Unlock()
	return h.Transcribe(ctx, wav, scratch, language)
}

// Spelling lists (D147) reach the transcription: a transcriber that takes
// hints is told the list's words, and the words it writes are corrected by
// the list.
func TestTranscriptionTakesTheSpellingList(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	if _, err := ops.CallTyped[ops.SpellingAddOut](ctx, s.a, "transcript.spelling.add", ops.SpellingAddIn{Word: "FernBrook"}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	res := ingest.File(ctx, s.a, s.footage("kitchen", 4, false, true), ingest.Options{ProjectID: s.project, Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	w := s.worker()
	h := &hinted{}
	w.Tx, w.Langs = h, nil
	tx, _, err := s.a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeTranscribe, IdempotencyKey: "transcribe:spelled:" + res.AssetID, AssetID: res.AssetID,
		Payload: map[string]any{"assetId": res.AssetID}, Heavy: true})
	if err != nil {
		t.Fatal(err)
	}
	j := runJobs(t, w, tx.ID)[tx.ID]
	if j.State != models.JobSucceeded || j.Result["spelled"] == nil {
		t.Fatalf("transcribed: %+v", j)
	}
	h.mu.Lock()
	hints := strings.Join(h.hints, " ")
	h.mu.Unlock()
	if !strings.Contains(hints, "FernBrook") {
		t.Fatalf("hints %q", hints)
	}
	var segs []models.TranscriptSegment
	if err := s.a.DB.Find(ctx, store.CSegments, s.a.WorkspaceID(), bson.M{"assetId": res.AssetID}, &segs); err != nil || len(segs) != 1 || !strings.Contains(segs[0].Text, "FernBrook") {
		t.Fatalf("spelled %+v %v", segs, err)
	}
}

// A linked folder's file is copied to the storage by the machine that has it
// (D81) the first time a cut uses it; one already there is left alone; one
// that is gone or that this machine no longer has fails for good, saying so.
func TestALinkedFoldersFileIsUploadedHere(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	w := New(a)
	w.Log, w.Battery = t.Logf, pluggedIn
	src := filepath.Join(t.TempDir(), "logo.png")
	png := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("logo bytes ", 40))
	if err := os.WriteFile(src, png, 0o600); err != nil {
		t.Fatal(err)
	}
	sha, err := media.HashFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Cache.Adopt(src, sha); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	res := models.Resource{ID: ids.New(ids.Resource), WorkspaceID: a.WorkspaceID(), Name: "logo", Title: "Logo", Kind: models.ResourceImage, CreatedAt: now, UpdatedAt: now,
		Versions: []models.ResourceVersion{{SHA256: sha, Size: int64(len(png)), MIME: "image/png", Ext: "png", StorageKey: media.ObjectKey(a.WorkspaceID(), sha),
			Holders: []string{a.Cfg.Installation.ID}}}}
	if err := a.DB.Insert(ctx, store.CResources, res); err != nil {
		t.Fatal(err)
	}
	upload := func(key, resourceID, version string) *models.Job {
		t.Helper()
		j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeResourceUpload, IdempotencyKey: "upload:" + key, MaxAttempts: 3,
			Payload: map[string]any{"resourceId": resourceID, "sha256": version}, Installation: a.Cfg.Installation.ID})
		if err != nil {
			t.Fatal(err)
		}
		got, _ := leaseAndRun(t, w, j.ID)
		return got
	}
	if j := upload("first", res.ID, sha); j.State != models.JobSucceeded || j.Result["already"] != nil {
		t.Fatalf("uploaded: %+v", j)
	}
	if info, err := a.Media.Stat(ctx, media.ObjectKey(a.WorkspaceID(), sha)); err != nil || info.Size != int64(len(png)) {
		t.Fatalf("in the storage: %+v %v", info, err)
	}
	if j := upload("again", res.ID, sha); j.State != models.JobSucceeded || j.Result["already"] != true {
		t.Fatalf("already there: %+v", j)
	}
	for _, c := range []struct{ key, id, version, says string }{
		{"gone", ids.New(ids.Resource), sha, "not found"},
		{"no-version", res.ID, strings.Repeat("0", 64), "has no version"},
	} {
		if j := upload(c.key, c.id, c.version); j.State != models.JobFailed || j.AttemptCount != 1 || !strings.Contains(j.Error, c.says) {
			t.Errorf("%s: %+v", c.key, j)
		}
	}
	// Not stored, not here, no folder: this machine no longer has it.
	other := res
	other.ID, other.Name = ids.New(ids.Resource), "banner"
	other.Versions = []models.ResourceVersion{{SHA256: strings.Repeat("b", 64), Size: 10, StorageKey: "k", Holders: []string{"ins_elsewhere"}}}
	if err := a.DB.Insert(ctx, store.CResources, other); err != nil {
		t.Fatal(err)
	}
	if j := upload("missing", other.ID, other.Versions[0].SHA256); j.State != models.JobFailed || !strings.Contains(j.Error, "no longer has Logo") {
		t.Fatalf("not on this machine: %+v", j)
	}
}

// Jobs that end tell who should know: a link import that failed, a speaker
// detection someone asked for.
func TestEndedJobsTellWhoShouldKnow(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "interview.mp4", State: models.AssetAvailable, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	insert := func(typ, assetID string, payload map[string]any) *models.Job {
		t.Helper()
		f := now
		j := &models.Job{ID: ids.New(ids.Job), WorkspaceID: a.WorkspaceID(), Type: typ, State: models.JobFailed, AssetID: assetID, CreatedBy: someone, Payload: payload,
			Attempts: []models.Attempt{}, IdempotencyKey: ids.New("k"), MaxAttempts: 1, Error: "the video is private", CreatedAt: now, UpdatedAt: now, NextRunAt: now, FinishedAt: &f}
		if err := a.DB.Insert(ctx, store.CJobs, *j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	w := New(a)
	w.settled(ctx, insert(jobs.TypeImport, "", map[string]any{"url": "https://www.youtube.com/watch?v=private01", "platform": "youtube", "advice": "private"}), models.JobFailed, nil)
	w.settled(ctx, insert(jobs.TypeDiarize, as.ID, map[string]any{"assetId": as.ID}), models.JobFailed, nil)
	for _, kind := range []string{models.NotifyImportFailed, models.NotifySpeakers} {
		var ns []models.Notification
		if err := a.DB.Find(ctx, store.CNotifications, a.WorkspaceID(), bson.M{"kind": kind, "userId": someone.UserID}, &ns); err != nil || len(ns) != 1 {
			t.Fatalf("%s: %+v %v", kind, ns, err)
		}
		if fs := plainwords.Check(ns[0].Title); len(fs) > 0 {
			t.Errorf("%s not plain: %q %v", kind, ns[0].Title, fs)
		}
	}
	// A link import with no link fails for good; a wait says its reason.
	_, err := importHandler(ctx, w, &models.Job{ID: ids.New(ids.Job), Payload: map[string]any{}}, func(float64, string) {})
	var p *Permanent
	if !errors.As(err, &p) {
		t.Fatalf("an import without a link: %v", err)
	}
	if (&postponed{after: time.Minute, reason: "The clip is still arriving."}).Error() != "The clip is still arriving." {
		t.Fatal("a wait's reason")
	}
}

// A render draws the pictures its cut pins by their exact version, served
// from this machine (D79); a picture only another machine's linked folder has
// makes the render wait for its upload (tried again, never failed for it).
func TestARenderDrawsTheCutsPictures(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	defer func(b time.Duration) { jobs.RetryBase = b }(jobs.RetryBase)
	jobs.RetryBase = 200 * time.Millisecond
	s.renderer()
	clip := ingest.File(ctx, s.a, s.footage("pier", 3, false, true), ingest.Options{ProjectID: s.project, Via: "cli"})
	if clip.AssetID == "" {
		t.Fatalf("ingest %+v", clip)
	}
	logoPath := filepath.Join(t.TempDir(), "harbour-logo.png")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c=0x2266cc:size=64x64:duration=1", "-frames:v", "1", logoPath).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	added, err := resources.AddFile(ctx, s.a, logoPath, resources.AddOptions{ProjectID: s.project})
	if err != nil || added.Resource == nil {
		t.Fatalf("add the logo: %+v %v", added, err)
	}
	logo := added.Resource
	sha := logo.Latest().SHA256
	s.cut(clip.AssetID, 0, 2)
	spec := fmt.Sprintf(`{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":0,"out":2}],`+
		`"resources":{%q:{"id":%q,"sha256":%q}},"overlays":[{"id":"logo","template":"logo.corner","at":0,"duration":2,"params":{"logo":%q}}]}`,
		clip.AssetID, logo.Name, logo.ID, sha, logo.Name)
	plan := fmt.Sprintf(`{"projectId":%q,"baseRevisionId":%q,"instruction":"the logo in the corner","spec":%s}`, s.project, s.head, spec)
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, s.a, "edit.check", ops.EditCheckIn{Plan: json.RawMessage(plan)}, ops.Caller{})
	if err != nil || !chk.Valid {
		t.Fatalf("check: %+v %v", chk, err)
	}
	applied, err := ops.CallTyped[ops.EditApplyOut](ctx, s.a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	w := s.worker()
	submit := func(quality string) *ops.RenderOut {
		t.Helper()
		r, err := ops.CallTyped[ops.RenderOut](ctx, s.a, "render.submit", ops.RenderSubmitIn{RevisionID: applied.Revision.ID, Quality: quality, NoQA: true}, ops.Caller{Human: true})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	r := submit("review")
	if j := runJobs(t, w, r.Job.ID)[r.Job.ID]; j.State != models.JobSucceeded {
		t.Fatalf("a render with a picture: %s %s", j.State, j.Error)
	}
	// The same version kept only in another machine's linked folder: the render waits for it.
	if _, err := s.a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": logo.ID}, bson.M{"$set": bson.M{"versions.0.stored": false, "versions.0.holders": bson.A{"ins_elsewhere"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.a.Cache.Remove(sha); err != nil {
		t.Fatal(err)
	}
	r = submit("final")
	me := jobs.Worker{ID: s.a.Cfg.Installation.ID, Name: s.a.Cfg.Installation.Name, JobTypes: w.JobTypes(), AcceptsHeavy: true}
	lj, err := s.a.Queue.LeaseByID(ctx, me, r.Job.ID)
	if err != nil || lj == nil {
		t.Fatalf("lease: %+v %v", lj, err)
	}
	again := w.execute(ctx, lj)
	j, _ := s.a.Queue.Get(ctx, r.Job.ID)
	if again.IsZero() || j.State != models.JobQueued || j.AttemptCount != 1 || !strings.Contains(j.Error, "to be uploaded from the machine that has its folder") {
		t.Fatalf("waiting for the picture: %+v", j)
	}
}
