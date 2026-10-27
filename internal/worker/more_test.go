package worker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/qa"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Scene descriptions (D74) are written by this machine's Claude Code (here a
// stand-in at the same seam) and kept as the clip's searchable descriptions;
// a program that can't run Claude Code, an answer that fails, or descriptions
// that can't be used fail the job for good and the clip says so.
func TestSceneDescriptionsComeFromThisMachinesClaude(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	res := ingest.File(ctx, s.a, s.footage("garden", 3, false, false), ingest.Options{ProjectID: s.project, Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	defer func(r func(context.Context, *app.App, *models.Asset, string) (*ops.DescribeRun, error)) {
		ops.DescribeRunner = r
	}(ops.DescribeRunner)
	w := s.worker()
	w.Handle(jobs.TypeDescribe, describeHandler)
	describe := func(key string) *models.Job {
		t.Helper()
		j, _, err := s.a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeDescribe, IdempotencyKey: "describe:" + key, AssetID: res.AssetID, ProjectID: s.project,
			Payload: map[string]any{"assetId": res.AssetID, "projectId": s.project}, MaxAttempts: 3, Actor: someone})
		if err != nil {
			t.Fatal(err)
		}
		return runJobs(t, w, j.ID)[j.ID]
	}
	analysis := func() string { return s.asset(res.AssetID).Analysis["describe"] }

	ops.DescribeRunner = nil
	if j := describe("none"); j.State != models.JobFailed || j.AttemptCount != 1 || !strings.Contains(j.Error, "cannot run Claude Code") || analysis() != "failed" {
		t.Fatalf("no Claude Code here: %+v %q", j, analysis())
	}
	ops.DescribeRunner = func(context.Context, *app.App, *models.Asset, string) (*ops.DescribeRun, error) {
		return nil, errors.New("Claude Code is not signed in")
	}
	if j := describe("refused"); j.State != models.JobFailed || j.AttemptCount != 1 || !strings.Contains(j.Error, "not signed in") {
		t.Fatalf("Claude Code failed: %+v", j)
	}
	ops.DescribeRunner = func(context.Context, *app.App, *models.Asset, string) (*ops.DescribeRun, error) {
		return &ops.DescribeRun{Scenes: []ops.SceneDescription{{From: 0, To: 1, Text: "   "}}, Model: "stand-in"}, nil
	}
	if j := describe("unusable"); j.State != models.JobFailed || !strings.Contains(j.Error, "were not usable") {
		t.Fatalf("unusable descriptions: %+v", j)
	}
	var asked string
	ops.DescribeRunner = func(_ context.Context, _ *app.App, as *models.Asset, projectID string) (*ops.DescribeRun, error) {
		asked = as.ID + " " + projectID
		return &ops.DescribeRun{ConversationID: "cnv_standin", Model: "stand-in", Scenes: []ops.SceneDescription{
			{From: 0, To: 1.5, Text: "A test pattern with coloured bars."}, {From: 1.5, To: 9, Text: "The bars keep moving across the frame."}}}, nil
	}
	j := describe("works")
	if j.State != models.JobSucceeded || j.Result["scenes"] != int32(2) && j.Result["scenes"] != int64(2) && j.Result["scenes"] != 2 || asked != res.AssetID+" "+s.project {
		t.Fatalf("described: %+v (asked %q)", j, asked)
	}
	var ins models.Insight
	if err := s.a.DB.C(store.CInsights).FindOne(ctx, bson.M{"assetId": res.AssetID, "kind": ops.DescriptionKind}).Decode(&ins); err != nil || ins.Provider != "claude-code" || ins.Model != "stand-in" {
		t.Fatalf("descriptions kept: %+v %v", ins, err)
	}
}

// Fixes start by measuring the clip (D153): the inspect job runs here and
// marks what could be fixed; a clip that is gone fails it for good at once.
func TestFixesAreMeasuredHere(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	res := ingest.File(ctx, s.a, s.footage("pier", 3, false, true), ingest.Options{ProjectID: s.project, Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	j, err := fixes.QueueInspect(ctx, s.a, s.asset(res.AssetID), false)
	if err != nil {
		t.Fatal(err)
	}
	w := s.worker()
	if got := runJobs(t, w, j.ID)[j.ID]; got.State != models.JobSucceeded {
		t.Fatalf("inspect: %s %s", got.State, got.Error)
	}
	if as := s.asset(res.AssetID); as.Fixes == nil || as.Fixes.MarkedAt == nil || as.Fixes.MarksFor != as.SHA256 {
		t.Fatalf("marks: %+v", as.Fixes)
	}
	gone, _, err := s.a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeInspect, IdempotencyKey: "inspect:gone", AssetID: ids.New(ids.Asset), MaxAttempts: 3,
		Payload: map[string]any{"assetId": ids.New(ids.Asset)}})
	if err != nil {
		t.Fatal(err)
	}
	if got := runJobs(t, w, gone.ID)[gone.ID]; got.State != models.JobFailed || got.AttemptCount != 1 || !strings.Contains(got.Error, "not found") {
		t.Fatalf("a clip that is gone: %+v", got)
	}
}

// The render's media server hands out exactly the files it was given, under a
// random path, with ranges for seeking; a resource served with its type never
// runs script (D79).
func TestTheRendersMediaServerServesOnlyItsFiles(t *testing.T) {
	dir := t.TempDir()
	clip, logo := filepath.Join(dir, "clip.bin"), filepath.Join(dir, "logo.svg")
	if err := os.WriteFile(clip, []byte("0123456789"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logo, []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "secret.txt")
	if err := os.WriteFile(other, []byte("not for the renderer"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv, base, err := serveFiles(map[string]string{"ast_clip": clip, "r-abc": logo}, map[string]string{"r-abc": "image/svg+xml"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	if !strings.HasPrefix(base, "http://127.0.0.1:") || len(strings.TrimPrefix(base[strings.LastIndex(base, "/"):], "/")) != 32 {
		t.Fatalf("base %q: loopback, behind a random path", base)
	}
	get := func(url string, hdr map[string]string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", url, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, string(b)
	}
	if res, body := get(base+"/ast_clip", map[string]string{"Range": "bytes=2-5"}); res.StatusCode != http.StatusPartialContent || body != "2345" ||
		res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("a range of a clip: %d %q", res.StatusCode, body)
	}
	if res, _ := get(base+"/r-abc", nil); res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/svg+xml" ||
		!strings.Contains(res.Header.Get("Content-Security-Policy"), "sandbox") || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("an SVG: %d %v", res.StatusCode, res.Header)
	}
	root := base[:strings.LastIndex(base, "/")]
	for _, u := range []string{base + "/secret.txt", base + "/../secret.txt", root + "/ast_clip", base + "/" + other} {
		if res, body := get(u, nil); res.StatusCode != http.StatusNotFound || strings.Contains(body, "not for the renderer") {
			t.Errorf("%s: %d %q", u, res.StatusCode, body)
		}
	}
}

// A terminal running one job (inline) never takes over a job another machine
// is running — it says so — and stops at once for a job that has ended.
func TestATerminalLeavesAJobAnotherMachineRuns(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeProxy, IdempotencyKey: "proxy:elsewhere", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	other := jobs.Worker{ID: ids.New(ids.Installation), Name: "studio-mac", JobTypes: []string{jobs.TypeProxy}}
	if lj, err := a.Queue.LeaseByID(ctx, other, j.ID); err != nil || lj == nil {
		t.Fatalf("the other machine's lease: %+v %v", lj, err)
	}
	w := New(a)
	w.Log, w.Battery = t.Logf, pluggedIn
	w.handlers = map[string]Handler{jobs.TypeProxy: func(context.Context, *Worker, *models.Job, Report) (map[string]any, error) {
		t.Error("ran a job another machine is running")
		return nil, nil
	}}
	w.OnlyJobID = j.ID
	rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := w.Run(rctx); err == nil || !strings.Contains(err.Error(), "already running on studio-mac") {
		t.Fatalf("a job running elsewhere: %v", err)
	}
	// Ended meanwhile: nothing to do.
	if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID}, bson.M{"$set": bson.M{"state": models.JobSucceeded}, "$unset": bson.M{"lease": ""}}); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(rctx); err != nil {
		t.Fatalf("a job that ended: %v", err)
	}
	// retryAt: a job that moved on has no next try here; one the database can't find waits the usual backoff.
	if at := retryAt(ctx, a, &models.Job{ID: j.ID}); !at.IsZero() {
		t.Fatalf("a finished job's next try: %v", at)
	}
	if at := retryAt(ctx, a, &models.Job{ID: ids.New(ids.Job), AttemptCount: 1}); at.Before(time.Now()) {
		t.Fatalf("an unknown job's next try: %v", at)
	}
}

// A render whose footage was deleted for good fails at once, saying so
// plainly (no retry brings the clip back).
func TestARenderOfFootageThatIsGoneFailsPlainly(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	s.renderer()
	res := ingest.File(ctx, s.a, s.footage("pier", 3, false, true), ingest.Options{ProjectID: s.project, Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	rev := s.cut(res.AssetID, 0, 2)
	if _, err := s.a.DB.C(store.CAssets).DeleteOne(ctx, bson.M{"_id": res.AssetID}); err != nil {
		t.Fatal(err)
	}
	r, err := ops.CallTyped[ops.RenderOut](ctx, s.a, "render.submit", ops.RenderSubmitIn{RevisionID: rev, Quality: "review", NoQA: true}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	j := runJobs(t, s.worker(), r.Job.ID)[r.Job.ID]
	var rd models.Render
	if err := s.a.DB.GetByID(ctx, store.CRenders, s.a.WorkspaceID(), r.Render.ID, &rd); err != nil {
		t.Fatal(err)
	}
	if j.State != models.JobFailed || j.AttemptCount != 1 || !strings.Contains(j.Error, "missing source media") || rd.State != models.RenderFailed ||
		rd.Error != "Some footage this version uses isn't in MetaRush any more, so it can't be rendered. Put the clip back, or take it out of the cut." {
		t.Fatalf("job %+v, render %q %q", j, rd.State, rd.Error)
	}
	if fs := plainwords.Check(rd.Error); len(fs) > 0 {
		t.Fatalf("not plain: %v", fs)
	}
}

// The render's facts answer exactly as the preview's would for what a cut may
// ask: music beats and grids, loudness over time, fixed copies, approved
// graphics, faces — and nothing for what isn't known.
func TestRenderFactsAnswerForWhatIsKnown(t *testing.T) {
	f := &renderFacts{
		facts:  map[string]editspec.AssetFacts{"ast_a": {ID: "ast_a", Duration: 10}},
		tx:     map[string][]editspec.TranscriptSegment{"ast_a": {{From: 0, To: 1, Text: "hello"}}},
		series: map[string]*evidence.Series{"ast_a": {Step: 0.1, LUFS: []float64{-20, -21, -22, -20, -19, -20, -21, -22, -20, -19}}},
		ch:     map[string]int{"ast_a": 2},
		beats:  map[string]evidence.BeatsOf{"ast_m": {Tempo: 120, Times: []float64{0.5, 1, 1.5}}},
		grids:  map[string]evidence.GridOf{"ast_m": {Tempo: 120, Beats: []float64{0.5, 1, 1.5}}},
		motion: map[string]*models.MotionComponent{},
		copies: map[string]*models.SourceCopy{"ast_a": {SHA256: "fixedsha", State: models.FixReady}},
	}
	if x, ok := f.Asset("ast_a"); !ok || x.Duration != 10 {
		t.Fatal("asset")
	}
	if _, ok := f.Asset("ast_nope"); ok {
		t.Fatal("unknown asset")
	}
	if len(f.Transcript("ast_a")) != 1 || f.Transcript("ast_nope") != nil {
		t.Fatal("transcript")
	}
	if step, lufs, ok := f.LoudnessSeries("ast_a"); !ok || step != 0.1 || len(lufs) != 10 {
		t.Fatal("loudness series")
	}
	if _, _, ok := f.LoudnessSeries("ast_nope"); ok {
		t.Fatal("unknown loudness series")
	}
	if _, ok := f.RangeLoudness("ast_a", 0, 1); !ok {
		t.Fatal("range loudness")
	}
	if _, ok := f.RangeLoudness("ast_nope", 0, 1); ok {
		t.Fatal("unknown range loudness")
	}
	if f.AudioChannels("ast_a") != 2 || f.AudioChannels("ast_nope") != 0 {
		t.Fatal("channels")
	}
	if b, ok := f.Beats("ast_m"); !ok || len(b) != 3 {
		t.Fatal("beats")
	}
	if g, ok := f.MusicGrid("ast_m"); !ok || g == nil {
		t.Fatal("grid")
	}
	if _, ok := f.MusicGrid("ast_a"); ok {
		t.Fatal("a grid for footage")
	}
	if c, ok := f.FixedCopy("ast_a", "fixedsha"); !ok || !c.Ready {
		t.Fatalf("fixed copy %+v %v", c, ok)
	}
	if _, ok := f.FixedCopy("ast_a", "othersha"); ok {
		t.Fatal("another fixed copy")
	}
	if _, ok := f.Motion("abc"); ok {
		t.Fatal("an unapproved graphic")
	}
	if _, _, ok := f.FaceFocus("ast_a", 0, 1); ok {
		t.Fatal("faces without an index")
	}
	if _, ok := f.FaceBox("ast_a", 0, 1); ok {
		t.Fatal("a face box without an index")
	}
	if _, _, ok := f.Subject("ast_a", 0, 1); ok {
		t.Fatal("a subject without an index")
	}
	if _, ok := f.Footage("ast_a"); ok {
		t.Fatal("switches nobody loaded")
	}
	if _, ok := f.Resource("res_1", "sha"); ok {
		t.Fatal("a resource without extras")
	}
	if _, ok := f.Nested("prj_1", "rev_1"); ok {
		t.Fatal("a nested cut without extras")
	}
	if got := fittedMusic(&editspec.Spec{Audio: []editspec.AudioTrack{{AssetID: "ast_m", Fit: editspec.TrackFitAuto}, {AssetID: "ast_x"}}}); len(got) != 1 || got[0] != "ast_m" {
		t.Fatalf("fitted music %v", got)
	}
	if audioChannels(&models.Asset{Media: models.MediaInfo{Streams: []models.Stream{{Type: "video"}, {Type: "audio", Channels: 1}}}}) != 1 || audioChannels(&models.Asset{}) != 0 {
		t.Fatal("audio channels")
	}
	if absInt(-3) != 3 || absInt(4) != 4 {
		t.Fatal("absInt")
	}
	if got := anyStrings([]any{"a.example", 3, "b.example"}); len(got) != 2 || got[1] != "b.example" {
		t.Fatalf("anyStrings %v", got)
	}
	if anyStrings("nope") != nil {
		t.Fatal("anyStrings of a string")
	}
}

// The quality check reads the evidence's stored spans however the database
// gives them back.
func TestStoredDetectorSpansAreRead(t *testing.T) {
	want := []qa.Span{{From: 1, To: 2}, {From: 3, To: 4.5}, {From: 5, To: 6}, {From: 7, To: 8}}
	in := bson.A{bson.D{{Key: "from", Value: 1.0}, {Key: "to", Value: int32(2)}}, bson.M{"from": int64(3), "to": 4.5}, map[string]any{"from": 5, "to": 6.0}, "junk",
		bson.D{{Key: "from", Value: 7.0}, {Key: "to", Value: 8.0}}}
	got := insightSpans(in)
	if len(got) != len(want) {
		t.Fatalf("spans %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("span %d: %v, want %v", i, got[i], want[i])
		}
	}
	if got := insightSpans([]any{bson.M{"from": 1.0, "to": 2.0}}); len(got) != 1 {
		t.Fatalf("a plain list: %v", got)
	}
	if insightSpans(nil) != nil || num("x") != 0 {
		t.Fatal("nothing")
	}
}

// Speakers: a transcript made again keeps people's names only when its words
// are the same; voices recognized automatically are told to the person who
// started the job, once per person.
func TestSameWordsAndRecognizedVoices(t *testing.T) {
	w := func(t string) models.Word { return models.Word{Text: t, From: 0, To: 0.3} }
	a := []models.TranscriptSegment{{Text: "hello there", Words: []models.Word{w("hello"), w("there")}}}
	b := []models.TranscriptSegment{{Text: "hello there", Words: []models.Word{w("hello"), w("there")}}}
	if !sameWords(a, b) {
		t.Fatal("the same words")
	}
	b[0].Words[1] = w("their")
	if sameWords(a, b) {
		t.Fatal("a different word")
	}
	if sameWords(a, nil) || sameWords(a, []models.TranscriptSegment{{Text: "hello there"}}) || sameWords(a, []models.TranscriptSegment{{Text: "hi", Words: a[0].Words}}) {
		t.Fatal("different lines")
	}

	app := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	pid := ids.New(ids.Person)
	if err := app.DB.Insert(ctx, store.CPeople, models.Person{ID: pid, WorkspaceID: app.WorkspaceID(), Name: "Jess", NameKey: "jess", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := app.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New("spk"), WorkspaceID: app.WorkspaceID(), AssetID: ids.New(ids.Asset), Label: "speaker_00",
		PersonID: pid, Name: "Jess", Source: models.SpeakerByAuto, Score: 0.91, Seconds: 12}); err != nil {
		t.Fatal(err)
	}
	recognized(ctx, app, &models.Job{}, &speakers.StoreResult{Recognized: map[string][]string{pid: {"ast_1"}}}) // nobody to tell
	recognized(ctx, app, &models.Job{CreatedBy: someone}, nil)
	recognized(ctx, app, &models.Job{CreatedBy: someone}, &speakers.StoreResult{Recognized: map[string][]string{pid: {"ast_1"}}, Library: &speakers.LibraryChange{People: []string{pid}}})
	var ns []models.Notification
	if err := app.DB.Find(ctx, store.CNotifications, app.WorkspaceID(), bson.M{"userId": someone.UserID}, &ns); err != nil || len(ns) != 1 {
		t.Fatalf("told %+v %v", ns, err)
	}
}

// A Remotion job that can't go on (the render, the task, the refresh or the
// push it was for is gone) fails for good, not after retries.
func TestRemotionJobsThatCantGoOnFailForGood(t *testing.T) {
	a := testapp.New(t)
	w := New(a)
	ctx := context.Background()
	nop := func(float64, string) {}
	for name, h := range map[string]Handler{"render": remotionRenderHandler, "task": remotionTaskHandler, "refresh": remotionRefreshHandler, "push": remotionPushHandler,
		"import": remotionImportHandler} {
		_, err := h(ctx, w, &models.Job{ID: ids.New(ids.Job), Payload: map[string]any{}, CreatedBy: someone}, nop)
		var p *Permanent
		if err == nil || !errors.As(err, &p) {
			t.Errorf("%s without what it was for: %v", name, err)
		}
	}
	if v := remotionVersion(a); v == "" {
		t.Fatal("the renderer's Remotion version is said")
	}
}
