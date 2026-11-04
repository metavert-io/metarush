package mattes_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/local"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/mattes"
	"github.com/metavert-io/metarush/internal/matting/mattingtest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeai"
	"github.com/metavert-io/metarush/internal/testutil/fakelocal"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

type env struct {
	a       *app.App
	fake    *fakeai.Fake
	model   *mattingtest.Model
	project string
	clip    *models.Asset
}

func setup(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	keyring.MockInit()
	generate.PollEvery, generate.FirstPoll = 20*time.Millisecond, 2*time.Millisecond
	key := "fake-key-for-cut-outs-0001"
	fake := fakeai.Install(t, key)
	t.Setenv(providers.EnvVar(fakeai.Name), key)
	m := fakelocal.Matter(t)
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	pid := ids.New(ids.Project)
	if err := a.DB.Insert(ctx, store.CProjects, models.Project{ID: pid, WorkspaceID: ws, Name: "Garden film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	e := &env{a: a, fake: fake, model: m, project: pid}
	e.clip = e.footage(t, "host")
	return e
}

// footage brings in a clip of a white box moving over black (the stand-in
// model's "person").
func (e *env) footage(t *testing.T, name string) *models.Asset {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := mattingtest.Footage(t, dir, 2, 320, 180, 25)
	named := filepath.Join(dir, name+".mp4")
	if err := os.Rename(path, named); err != nil {
		t.Fatal(err)
	}
	res := ingest.File(context.Background(), e.a, named, ingest.Options{ProjectID: e.project, Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("ingest: %+v", res)
	}
	return e.asset(t, res.AssetID)
}

func (e *env) asset(t *testing.T, id string) *models.Asset {
	t.Helper()
	var as models.Asset
	if err := e.a.DB.GetByID(context.Background(), store.CAssets, e.a.WorkspaceID(), id, &as); err != nil {
		t.Fatal(err)
	}
	return &as
}

func (e *env) cutout(t *testing.T, in mattes.CutoutIn) (*mattes.CutoutOut, error) {
	t.Helper()
	if in.AssetID == "" {
		in.AssetID = e.clip.ID
	}
	return ops.CallTyped[mattes.CutoutOut](context.Background(), e.a, "footage.cutout", in, ops.Caller{Human: true})
}

// run does a matte job in this process, as a worker would.
func (e *env) run(t *testing.T, jobID string) (map[string]any, error) {
	t.Helper()
	j, err := e.a.Queue.LeaseByID(context.Background(), jobs.Worker{ID: e.a.Cfg.Installation.ID, Name: "test", JobTypes: []string{jobs.TypeMatte}, AcceptsHeavy: true}, jobID)
	if err != nil || j == nil {
		t.Fatalf("lease %v %v", j, err)
	}
	return mattes.Run(context.Background(), e.a, j, func(float64, string) {})
}

func (e *env) matte(t *testing.T, id string) *models.Matte {
	t.Helper()
	m, err := mattes.Load(context.Background(), e.a, id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func f(v float64) *float64 { return &v }

// alphaAt reads the cut-out's alpha at a frame, as grey bytes w×h.
func alphaAt(t *testing.T, a *app.App, m *models.Matte, frame int) []byte {
	t.Helper()
	p, err := a.Cache.Ensure(context.Background(), a.Media, m.File.Key, m.File.SHA256, m.File.Size, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-c:v", "libvpx-vp9", "-i", p, "-vf",
		"select=eq(n\\,"+strconv.Itoa(frame)+"),alphaextract,format=gray", "-frames:v", "1", "-f", "rawvideo", "pipe:1").Output()
	if err != nil {
		t.Fatalf("reading the cut-out's alpha: %v", err)
	}
	return out
}

// A clip's cut-out is made once by a computer that can (its own job, with
// how far along it is), kept with its fingerprint and used again for any
// part it covers; its file is the clip's picture with the people kept and
// the rest see-through, frame for frame.
func TestACutOutIsMadeOnceAndUsedAgain(t *testing.T) {
	e := setup(t)
	dry, err := e.cutout(t, mattes.CutoutIn{DryRun: true})
	if err != nil || dry.Matte.State != models.MatteWaiting || !dry.Here || dry.JobID != "" || dry.Seconds <= 0 || dry.Matte.ID != "" || !strings.Contains(dry.Summary, "would be made") {
		t.Fatalf("a dry run: %+v %v", dry, err)
	}
	out, err := e.cutout(t, mattes.CutoutIn{})
	if err != nil || out.JobID == "" || out.Reused || out.Matte.Model != local.ModelSubject || out.Matte.Part != "the whole clip" {
		t.Fatalf("asked: %+v %v", out, err)
	}
	again, err := e.cutout(t, mattes.CutoutIn{})
	if err != nil || again.JobID != out.JobID || again.Matte.ID != out.Matte.ID {
		t.Fatalf("asked again while it is made: %+v %v", again, err)
	}
	res, err := e.run(t, out.JobID)
	if err != nil {
		t.Fatal(err)
	}
	m := e.matte(t, out.Matte.ID)
	if m.State != models.MatteReady || m.File == nil || m.File.MIME != "video/webm" || m.Frames != 50 || m.First != 0 || m.Rate != "25/1" ||
		m.Width != 320 || m.Height != 180 || m.Measures == nil || m.Measures.People != 1 || m.MadeOnName != "test-machine" || m.ReadyAt == nil || res["sha256"] != m.File.SHA256 {
		t.Fatalf("made: %+v %+v", m, res)
	}
	// The box is kept, the black around it see-through.
	alpha := alphaAt(t, e.a, m, 0)
	if len(alpha) != 320*180 {
		t.Fatalf("alpha of %d bytes", len(alpha))
	}
	box, around := alpha[90*320+(10+40)], alpha[10*320+300]
	if box < 200 || around > 40 {
		t.Fatalf("the box %d, around it %d", box, around)
	}
	// Used again for the whole clip, and for any part of it.
	whole, err := e.cutout(t, mattes.CutoutIn{})
	if err != nil || !whole.Reused || whole.JobID != "" || whole.Matte.SHA256 != m.File.SHA256 {
		t.Fatalf("the same again: %+v %v", whole, err)
	}
	part, err := e.cutout(t, mattes.CutoutIn{From: f(0.4), To: f(1.6)})
	if err != nil || !part.Reused || part.Matte.ID != m.ID {
		t.Fatalf("a part of it: %+v %v", part, err)
	}
	if n, _ := e.a.DB.C(store.CMattes).CountDocuments(context.Background(), bson.M{"workspaceId": e.a.WorkspaceID()}); n != 1 {
		t.Fatalf("%d cut-outs made", n)
	}
	list, err := ops.CallTyped[mattes.CutoutsOut](context.Background(), e.a, "footage.cutouts", mattes.CutoutsIn{AssetID: e.clip.ID}, ops.Caller{})
	if err != nil || len(list.Cutouts) != 1 || !strings.HasPrefix(list.Cutouts[0].Words, "Ready: made on test-machine") || !strings.Contains(list.Summary, "one cut-out") {
		t.Fatalf("listed: %+v %v", list, err)
	}
	if got, err := mattes.BySHA(context.Background(), e.a, m.File.SHA256); err != nil || got == nil || got.ID != m.ID {
		t.Fatalf("by its file: %+v %v", got, err)
	}
	// Another model is another cut-out.
	modnet, err := e.cutout(t, mattes.CutoutIn{Model: "local/modnet"})
	if err != nil || modnet.Reused || modnet.Matte.ID == m.ID || modnet.Matte.Model != local.ModelMODNet {
		t.Fatalf("another model: %+v %v", modnet, err)
	}
}

// The fingerprint names what a cut-out is made from and how.
func TestFingerprint(t *testing.T) {
	a := mattes.Fingerprint("m1", 0, 10, "local", "modnet")
	if a != mattes.Fingerprint("m1", 0.0001, 10.0004, "local", "modnet") || len(a) != 64 {
		t.Fatal("the same request has another fingerprint")
	}
	for _, b := range []string{mattes.Fingerprint("m2", 0, 10, "local", "modnet"), mattes.Fingerprint("m1", 1, 10, "local", "modnet"),
		mattes.Fingerprint("m1", 0, 9, "local", "modnet"), mattes.Fingerprint("m1", 0, 10, "local", "apple-vision")} {
		if b == a {
			t.Fatal("a different request has the same fingerprint")
		}
	}
	if mattes.RateOf("30000/1001") < 29.97 || mattes.RateOf("25") != 25 || mattes.RateOf("x") != 0 || mattes.RateOf("1/0") != 0 {
		t.Fatal("rates")
	}
	if mattes.PartWords(0, 10, 10) != "the whole clip" || mattes.PartWords(12, 84, 100) != "from 0:12 to 1:24" {
		t.Fatal("parts in words")
	}
}

// Someone else's video is never processed (D441): no cut-out, no preview,
// and a job asked before the clip was marked stops.
func TestReferencesAreRefused(t *testing.T) {
	e := setup(t)
	out, err := e.cutout(t, mattes.CutoutIn{})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.a.DB.Update(context.Background(), store.CAssets, e.a.WorkspaceID(), e.clip.ID, bson.M{"$set": bson.M{"reference": models.AssetReference{Why: "marked", At: time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cutout(t, mattes.CutoutIn{From: f(0.5)}); apperr.From(err).Code != apperr.Conflict || !strings.Contains(apperr.From(err).Message, "someone else's video") {
		t.Fatalf("a reference: %v", err)
	}
	if _, _, err := mattes.Preview(context.Background(), e.a, &mattes.CutoutPreviewIn{AssetID: e.clip.ID, At: 1, With: mattes.WithBlur}); err == nil {
		t.Fatal("a preview of someone else's video")
	}
	var fin *mattes.Final
	if _, err := e.run(t, out.JobID); !errors.As(err, &fin) {
		t.Fatalf("the job asked before: %v", err)
	}
	if m := e.matte(t, out.Matte.ID); m.State != models.MatteFailed || !strings.Contains(m.Error, "someone else's video") {
		t.Fatalf("the cut-out: %+v", m)
	}
}

// A cut-out follows its job: stopped, failed or lost, it says so.
func TestCutOutsFollowTheirJobs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	out, err := e.cutout(t, mattes.CutoutIn{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.Queue.RequestCancel(ctx, out.JobID); err != nil {
		t.Fatal(err)
	}
	m := e.matte(t, out.Matte.ID)
	mattes.Refresh(ctx, e.a, m)
	if m = e.matte(t, out.Matte.ID); m.State != models.MatteStopped {
		t.Fatalf("stopped: %+v", m)
	}
	// Asked again: the same cut-out, made again.
	again, err := e.cutout(t, mattes.CutoutIn{})
	if err != nil || again.Matte.ID != m.ID || again.JobID == "" || again.JobID == out.JobID {
		t.Fatalf("asked again: %+v %v", again, err)
	}
	// Its job lost: it can't be made that way any more.
	if _, err := e.a.DB.C(store.CJobs).DeleteOne(ctx, bson.M{"_id": again.JobID}); err != nil {
		t.Fatal(err)
	}
	m = e.matte(t, m.ID)
	mattes.Refresh(ctx, e.a, m)
	if m.State != models.MatteFailed || !strings.Contains(m.Error, "lost") {
		t.Fatalf("lost: %+v", m)
	}
	// A model that fails: the last try leaves it failed, in plain words.
	e.model.FailAt = 3
	third, err := e.cutout(t, mattes.CutoutIn{})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.a.DB.Update(ctx, store.CJobs, e.a.WorkspaceID(), third.JobID, bson.M{"$set": bson.M{"maxAttempts": 1}})
	if _, err := e.run(t, third.JobID); err == nil {
		t.Fatal("a failing model made a cut-out")
	}
	if m = e.matte(t, m.ID); m.State != models.MatteFailed || m.Error == "" {
		t.Fatalf("failed: %+v", m)
	}
}

// Where no computer of the workspace can tell people from their background,
// asking says which component adds it; where another computer can, the job
// waits for it.
func TestCutOutsGoWhereAModelIs(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	local.MatterReady = func(aiservices.Env, string) error {
		return apperr.New(apperr.NotConfigured, "The model that finds the people in footage isn't on this computer (about 26 MB).")
	}
	if _, err := e.cutout(t, mattes.CutoutIn{}); apperr.From(err).Code != apperr.ToolMissing || !strings.Contains(apperr.From(err).Message, "No computer in this workspace") {
		t.Fatalf("nowhere: %v", err)
	}
	// Another computer of the workspace makes them with Apple Vision.
	if err := e.a.DB.Insert(ctx, store.CWorkers, models.WorkerDevice{ID: ids.New(ids.Installation), WorkspaceID: e.a.WorkspaceID(), Name: "studio-mac",
		JobTypes: []string{jobs.TypeMatte}, Mattes: []string{"local/apple-vision"}, LastHeartbeat: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	out, err := e.cutout(t, mattes.CutoutIn{})
	if err != nil || out.Matte.Model != local.ModelSubject {
		t.Fatalf("for the other computer: %+v %v", out, err)
	}
	var wait *mattes.Wait
	if _, err := e.run(t, out.JobID); !errors.As(err, &wait) || wait.After <= 0 || !strings.Contains(wait.Why, "Apple Vision") {
		t.Fatalf("this computer can't: %v", err)
	}
}

// A service elsewhere (the fake one here; none is built) goes through AI
// work: priced first, What may leave asked — a client keeping footage from AI
// services refuses it, leaving nothing behind — the picture sent without its
// sound, the cost recorded, and the matte that comes back kept as a cut-out.
func TestCutOutsElsewhereFollowWhatMayLeaveAndRecordTheirCost(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	e := setup(t)
	ctx := context.Background()
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	in := mattes.CutoutIn{ProjectID: e.project, Model: "fake/fake-video-background", DryRun: true}
	dry, err := e.cutout(t, in)
	if err != nil || dry.Estimate == nil || dry.Estimate.USD <= 0 || dry.Here || !strings.Contains(strings.Join(dry.Estimate.Inputs, " "), "without its sound") {
		t.Fatalf("priced: %+v %v", dry, err)
	}
	in.DryRun = false
	if _, err := e.cutout(t, in); apperr.From(err).Code != apperr.ConfirmRequired {
		t.Fatalf("no price agreed: %v", err)
	}
	if _, err := ops.CallTyped[leave.LeaveOut](ctx, e.a, "content.leave.set", leave.LeaveSetIn{ProjectID: e.project, Keep: map[string][]string{"ai": {"footage"}}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	in.MaxUSD = f(dry.Estimate.USD)
	if _, err := e.cutout(t, in); apperr.From(err).Code != apperr.KeptHere {
		t.Fatalf("a client keeping footage here: %v", err)
	}
	if n, _ := e.a.DB.C(store.CMattes).CountDocuments(ctx, bson.M{"workspaceId": e.a.WorkspaceID()}); n != 0 {
		t.Fatalf("%d cut-outs left behind by a refusal", n)
	}
	if _, err := ops.CallTyped[leave.LeaveOut](ctx, e.a, "content.leave.set", leave.LeaveSetIn{ProjectID: e.project, Let: map[string][]string{"ai": {"footage"}}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	out, err := e.cutout(t, in)
	if err != nil || out.Matte.GenerationID == "" || out.JobID == "" {
		t.Fatalf("asked of the service: %+v %v", out, err)
	}
	j, err := e.a.Queue.Get(ctx, out.JobID)
	if err != nil || j.Type != jobs.TypeGenerate || j.Installation != e.a.Cfg.Installation.ID {
		t.Fatalf("the work runs where the key is: %+v %v", j, err)
	}
	if _, err := aiwork.Run(ctx, e.a, j, nil); err != nil {
		t.Fatal(err)
	}
	m := e.matte(t, out.Matte.ID)
	if m.State != models.MatteReady || m.File == nil || m.Frames != 50 {
		t.Fatalf("kept: %+v", m)
	}
	var g models.Generation
	if err := e.a.DB.GetByID(ctx, store.CGenerations, e.a.WorkspaceID(), m.GenerationID, &g); err != nil {
		t.Fatal(err)
	}
	if g.Kind != aiservices.KindVideoBackground || g.Estimate.USD <= 0 || g.Outcome == nil || g.Outcome.State != models.GenDone || len(g.References) != 1 ||
		g.References[0].Role != aiservices.RoleClip {
		t.Fatalf("the record of the work: %+v", g)
	}
	if kinds := leave.GenerationKinds(&g); strings.Join(kinds, ",") != models.LeaveFootage+","+models.LeaveFaces {
		t.Fatalf("what left: %v", kinds)
	}
	if e.fake.Calls("submit") != 1 {
		t.Fatalf("sent %d times", e.fake.Calls("submit"))
	}
}

// A still of a moment with the background replaced: a colour, a blur of the
// clip, another clip, with soft edges.
func TestPreview(t *testing.T) {
	e := setup(t)
	other := e.footage(t, "beach")
	for _, in := range []mattes.CutoutPreviewIn{
		{AssetID: e.clip.ID, At: 1, With: mattes.WithColour, Colour: "#00b140", Feather: 3},
		{AssetID: e.clip.ID, At: 1, With: mattes.WithBlur, Width: 320},
		{AssetID: e.clip.ID, At: 0.2, With: mattes.WithFootage, Footage: other.ID, FootageAt: 1.5},
	} {
		b, info, err := mattes.Preview(context.Background(), e.a, &in)
		if err != nil {
			t.Fatalf("%s: %v", in.With, err)
		}
		if !bytes.HasPrefix(b, []byte{0xff, 0xd8}) || info.Width != 320 || info.Height != 180 || info.Model != "local/apple-vision" {
			t.Fatalf("%s: %+v", in.With, info)
		}
	}
	if _, _, err := mattes.Preview(context.Background(), e.a, &mattes.CutoutPreviewIn{AssetID: e.clip.ID, With: "sky"}); err == nil {
		t.Fatal("an unknown background")
	}
	out := filepath.Join(t.TempDir(), "still.jpg")
	p, err := ops.CallTyped[mattes.CutoutPreviewOut](context.Background(), e.a, "footage.cutout.preview",
		mattes.CutoutPreviewIn{AssetID: e.clip.ID, At: 1, With: mattes.WithColour, Colour: "#ffffff", Out: out}, ops.Caller{})
	if err != nil || p.Path != out {
		t.Fatalf("the CLI's still: %+v %v", p, err)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Fatalf("no still: %v", err)
	}
}
