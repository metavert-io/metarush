package generate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/videogen"
)

// fakeKey is a key-shaped value made at run time (no literal for the secret scanner).
func fakeKey() string { return "gk-" + strings.Repeat("Vw4", 10) + "-fake" }

type env struct {
	a       *app.App
	fake    *fakevideogen.Server
	key     string
	project string
}

func setup(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	keyring.MockInit() // never the real keychain
	generate.PollEvery, generate.FirstPoll = 40*time.Millisecond, 5*time.Millisecond
	key := fakeKey()
	fake := fakevideogen.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(videogen.EnvGeminiBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(videogen.GeminiName), "")
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	pid := ids.New(ids.Project)
	if err := a.DB.Insert(ctx, store.CProjects, models.Project{ID: pid, WorkspaceID: ws, Name: "Pond film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[providers.Status](ctx, a, "provider.set", providers.SetIn{Name: videogen.GeminiName, Key: key}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	return &env{a: a, fake: fake, key: key, project: pid}
}

func call[O any](t *testing.T, a *app.App, name string, in any, human bool) (*O, error) {
	t.Helper()
	return ops.CallTyped[O](context.Background(), a, name, in, ops.Caller{Human: human})
}

func codeOf(err error) string {
	if err == nil {
		return ""
	}
	return apperr.From(err).Code
}

func usd(v float64) *float64 { return &v }

// run does a generation's job in this process, as a worker would.
func (e *env) run(t *testing.T, ctx context.Context, jobID string) (map[string]any, error) {
	t.Helper()
	j, err := e.a.Queue.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	return generate.Run(ctx, e.a, j, nil)
}

func ledger(t *testing.T, a *app.App, scope, key string) models.GenSpend {
	var l models.GenSpend
	_ = a.DB.C(store.CGenSpend).FindOne(context.Background(), bson.M{"_id": a.WorkspaceID() + ":" + scope + ":" + key}).Decode(&l)
	return l
}

// A shot from words: priced first, refused without the agreed price, then
// reserved, sent, downloaded, checked, added as an ordinary clip with its
// provenance, settled with what it cost and deleted at the provider.
func TestShotFromWords(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in := generate.GenShotIn{ProjectID: e.project, Prompt: "a red paper boat drifting on a pond", Seconds: 3, Resolution: "360p"}
	est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if !est.Allowed || est.Seconds != 3 || est.Resolution != "360p" || est.Task != videogen.TaskTextToVideo || est.USD <= 0 || est.USD > 0.5 || est.Likeness {
		t.Fatalf("estimate %+v", est)
	}
	if !strings.Contains(est.Summary, "costs about $0.1") {
		t.Fatalf("summary %q", est.Summary)
	}
	// Nothing happens without the agreed price, or with too little.
	if _, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("no price: %v", err)
	}
	in.MaxUSD = usd(est.USD / 2)
	if _, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("too little: %v", err)
	}
	if e.fake.Calls("create") != 0 {
		t.Fatal("nothing may reach the provider before the price is agreed")
	}
	in.MaxUSD = usd(est.USD)
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Generations) != 1 || len(out.JobIDs) != 1 || out.Generations[0].State != generate.StateWaiting {
		t.Fatalf("out %+v", out)
	}
	g := out.Generations[0]
	if g.Budget == nil || g.Budget.ReservedUSD != est.USD || g.RequestHash == "" || g.Machine != "test-machine" || g.Channel != "cli" {
		t.Fatalf("record %+v", g.Generation)
	}
	if l := ledger(t, e.a, "project", e.project); l.HeldMicros == 0 || l.SpentMicros != 0 {
		t.Fatalf("money held: %+v", l)
	}
	res, err := e.run(t, ctx, out.JobIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	if res["state"] != models.GenDone || res["assetId"] == "" {
		t.Fatalf("result %v", res)
	}
	v, err := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: g.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != generate.StateDone || v.Ticket == nil || v.Outcome == nil || v.Settled == nil || v.Forgotten == nil || !v.Forgotten.OK || len(v.Clips) != 1 {
		t.Fatalf("view %+v", v)
	}
	if a := v.Outcome.Actual; a == nil || a.Basis != "usage" || a.VideoTokens != 3*1931 {
		t.Fatalf("actual %+v", v.Outcome.Actual)
	}
	// The clip is an ordinary clip of the project, marked as AI-generated, with its provenance.
	var as models.Asset
	if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), v.Clips[0].AssetID, &as); err != nil {
		t.Fatal(err)
	}
	if as.Source == nil || !as.Source.IsGenerated() || as.Source.Generated.GenerationID != g.ID || as.Source.Generated.Watermark != "SynthID" ||
		!as.HasTag(models.TagAIGenerated) || len(as.ProjectIDs) != 1 || as.ProjectIDs[0] != e.project || !as.Media.HasVideo {
		t.Fatalf("asset %+v %+v", as, as.Source)
	}
	if as.SHA256 != v.Outcome.Outputs[0].SHA256 {
		t.Fatal("the provider's original file is kept as it came")
	}
	// Settled: nothing held any more, the cost counted once.
	l := ledger(t, e.a, "project", e.project)
	if l.HeldMicros != 0 || l.SpentMicros == 0 || l.Count != 1 {
		t.Fatalf("settled ledger %+v", l)
	}
	day := ledger(t, e.a, "day", time.Now().UTC().Format("2006-01-02"))
	if day.HeldMicros != 0 || day.SpentMicros != l.SpentMicros {
		t.Fatalf("day ledger %+v", day)
	}
	// Settling again (a restart after the end) changes nothing.
	if err := generate.Settle(ctx, e.a, &v.Generation, 5, ""); err != nil {
		t.Fatal(err)
	}
	if l2 := ledger(t, e.a, "project", e.project); l2.SpentMicros != l.SpentMicros {
		t.Fatal("settled twice")
	}
	if ki, kf := e.fake.Kept(); ki != 0 || kf != 0 {
		t.Fatalf("left at the provider: %d %d", ki, kf)
	}
	// A notification for the person who asked.
	var n models.Notification
	if err := e.a.DB.C(store.CNotifications).FindOne(ctx, bson.M{"groupKey": "generation:" + g.ID}).Decode(&n); err != nil || n.Kind != models.NotifyGenerationReady {
		t.Fatalf("notification %+v %v", n, err)
	}
	// Generate again: a new take made the same way, never replacing the first.
	again, err := call[generate.GenRunOut](t, e.a, "generate.again", generate.GenAgainIn{ID: g.ID, Takes: 2, MaxUSD: usd(1)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Generations) != 2 || again.Generations[0].Take != 2 || again.Generations[1].Take != 3 || again.Generations[0].GroupID != g.GroupID ||
		again.Generations[0].ParentID != g.ID || again.Generations[0].Operation != models.GenVariation || again.Generations[0].RequestHash != g.RequestHash {
		t.Fatalf("again %+v", again.Generations[0].Generation)
	}
	for _, id := range again.JobIDs {
		if _, err := e.run(t, ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := call[generate.GenListOut](t, e.a, "generate.list", generate.GenListIn{ProjectID: e.project}, false)
	if len(list.Generations) != 3 {
		t.Fatalf("list %d", len(list.Generations))
	}
	seen := map[string]bool{}
	for _, gv := range list.Generations {
		if gv.State != generate.StateDone || len(gv.Clips) != 1 || seen[gv.Clips[0].AssetID] {
			t.Fatalf("each take is its own clip: %+v", gv)
		}
		seen[gv.Clips[0].AssetID] = true
	}
}

// The budget: money is reserved before a request is sent and the project's
// budget and the day's cap refuse what would go over.
func TestBudgetReserveAndSettle(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := call[generate.GenSettingsOut](t, e.a, "generate.configure", generate.GenConfigureIn{ProjectID: e.project, BudgetUSD: usd(0.25)}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("spending limits are people only: %v", err)
	}
	st, err := call[generate.GenSettingsOut](t, e.a, "generate.configure", generate.GenConfigureIn{ProjectID: e.project, BudgetUSD: usd(0.25)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.ProjectBudgetUSD == nil || *st.ProjectBudgetUSD != 0.25 || st.ProjectBudgetDefault || st.DailyCapUSD != 10 || !st.DailyCapDefault {
		t.Fatalf("settings %+v", st)
	}
	in := generate.GenShotIn{ProjectID: e.project, Prompt: "a paper boat", Seconds: 3, Resolution: "360p", MaxUSD: usd(1)}
	first, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if err != nil {
		t.Fatal(err)
	}
	// A second one fits only if the first one's money is still free: it isn't (held).
	_, err = call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if codeOf(err) != apperr.Budget || !strings.Contains(apperr.From(err).Message, "project's budget") {
		t.Fatalf("over the project budget: %v", err)
	}
	if day := ledger(t, e.a, "day", time.Now().UTC().Format("2006-01-02")); len(dayHolds(t, e.a)) != 1 || day.HeldMicros == 0 {
		t.Fatalf("a refused request holds nothing: %+v", day)
	}
	// The estimate says so before anything is asked.
	est, _ := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if est.Allowed || len(est.Problems) == 0 {
		t.Fatalf("estimate %+v", est)
	}
	// Once the first is made it costs its usage; the rest is free again.
	if _, err := e.run(t, ctx, first.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false); err != nil {
		t.Fatalf("after settling: %v", err)
	}
	// The daily cap applies to the whole team.
	if _, err := call[generate.GenSettingsOut](t, e.a, "generate.configure", generate.GenConfigureIn{ProjectID: e.project, BudgetUSD: usd(0)}, true); err != nil {
		t.Fatal(err)
	}
	big := generate.GenShotIn{ProjectID: e.project, Prompt: "a paper boat", Seconds: 10, Resolution: "4k", Takes: 4, MaxUSD: usd(100)}
	_, err = call[generate.GenRunOut](t, e.a, "generate.shot", big, false)
	if codeOf(err) != apperr.Budget || !strings.Contains(apperr.From(err).Message, "today's limit") {
		t.Fatalf("over the daily cap: %v", err)
	}
	// The takes that fit before the cap was reached were stopped and given back.
	var gs []models.Generation
	_ = e.a.DB.Find(ctx, store.CGenerations, e.a.WorkspaceID(), bson.M{"params.resolution": "4k"}, &gs)
	for _, g := range gs {
		if g.Outcome == nil || g.Outcome.State != models.GenCanceled || g.Settled == nil || g.Settled.USD != 0 {
			t.Fatalf("a take of a refused request: %+v", g.Outcome)
		}
	}
	if e.fake.Calls("create") != 1 {
		t.Fatalf("sent %d requests", e.fake.Calls("create"))
	}
}

func dayHolds(t *testing.T, a *app.App) []any {
	var raw bson.M
	_ = a.DB.C(store.CGenSpend).FindOne(context.Background(), bson.M{"_id": a.WorkspaceID() + ":day:" + time.Now().UTC().Format("2006-01-02")}).Decode(&raw)
	h, _ := raw["holds"].(bson.A)
	return h
}

// A clip with a person on screen, from the faces index.
func (e *env) clipWithFace(t *testing.T, name, personID, personName string, indexed bool) *models.Asset {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, name+".mp4")
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=4", "-f", "lavfi",
		"-i", "sine=frequency=330:duration=4", "-shortest", "-pix_fmt", "yuv420p", "-metadata", "comment="+name, p).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	res := ingest.File(ctx, e.a, p, ingest.Options{ProjectID: e.project, FootageOnly: true})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	if indexed {
		_ = e.a.DB.Update(ctx, store.CAssets, e.a.WorkspaceID(), res.AssetID, bson.M{"$set": bson.M{"analysis.visual": "available"}})
	}
	if personID != "" {
		f := models.FaceTrack{ID: ids.New(ids.Face), WorkspaceID: e.a.WorkspaceID(), AssetID: res.AssetID, Track: 1, From: 0, To: 4, At: 1, Box: []float64{0.3, 0.2, 0.3, 0.4},
			PersonID: personID, Name: personName, Naming: &models.Naming{By: models.NamedByPerson, Actor: e.a.Actor, At: time.Now()}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
		if err := e.a.DB.Insert(ctx, store.CFaces, f); err != nil {
			t.Fatal(err)
		}
	}
	var as models.Asset
	_ = e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), res.AssetID, &as)
	return &as
}

func (e *env) person(t *testing.T, name string) string {
	t.Helper()
	p := models.Person{ID: ids.New(ids.Person), WorkspaceID: e.a.WorkspaceID(), Name: name, NameKey: strings.ToLower(name), CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := e.a.DB.Insert(context.Background(), store.CPeople, p); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

// Consent (D126): a person's likeness needs their consent — refused without
// it, for children and public figures, and once it is withdrawn.
func TestConsentIsRequired(t *testing.T) {
	e := setup(t)
	maya := e.person(t, "Maya")
	withMaya := e.clipWithFace(t, "kitchen", maya, "Maya", true)
	unknown := e.clipWithFace(t, "garden", "", "", false)
	from := func(as *models.Asset) *generate.FrameRef { return &generate.FrameRef{AssetID: as.ID, At: 1} }

	// A frame showing Maya, without her consent.
	_, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "she walks out of the kitchen", From: from(withMaya), Seconds: 4, MaxUSD: usd(5)}, false)
	if codeOf(err) != apperr.ConsentRequired || !strings.Contains(apperr.From(err).Message, "Maya") {
		t.Fatalf("a frame showing someone: %v", err)
	}
	// A frame MetaRush can't check, unless the person says it shows no one.
	_, err = call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "the garden at dusk", From: from(unknown), Seconds: 4, MaxUSD: usd(5)}, false)
	if codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("a frame nobody checked: %v", err)
	}
	if _, err := call[generate.GenEstimate](t, e.a, "generate.estimate", generate.GenShotIn{ProjectID: e.project, Prompt: "the garden at dusk", From: from(unknown), Seconds: 4, NoPeople: true}, false); err != nil {
		t.Fatal(err)
	}
	// Naming her in the prompt is her likeness too.
	_, err = call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "Maya dancing on the beach", Seconds: 4, MaxUSD: usd(5)}, false)
	if codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("a name in the prompt: %v", err)
	}
	// Consent is recorded by people only, never for a child or a public figure.
	rec := generate.ConsentRecordIn{Person: "Maya", Statement: "I, Maya, agree to my likeness in generated video for this garden club film.", Recording: withMaya.ID, Adult: true}
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", rec, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("people only: %v", err)
	}
	child := rec
	child.Adult = false
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", child, true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("a child: %v", err)
	}
	famous := rec
	famous.PublicFigure = true
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", famous, true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("a public figure: %v", err)
	}
	noRec := rec
	noRec.Recording = ""
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", noRec, true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("someone else's consent needs their recording: %v", err)
	}
	short := rec
	short.Statement = "ok"
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", short, true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("a statement in their words: %v", err)
	}
	c, err := call[generate.ConsentOut](t, e.a, "consent.record", rec, true)
	if err != nil {
		t.Fatal(err)
	}
	if c.Consent.Status != "active" || c.Consent.ExpiresAt == nil || c.Consent.Self {
		t.Fatalf("consent %+v", c.Consent)
	}
	// Now the frame of her may be used; the record names the consent.
	est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", generate.GenShotIn{ProjectID: e.project, Prompt: "she walks out of the kitchen", From: from(withMaya), Seconds: 4}, false)
	if err != nil || !est.Allowed || !est.Likeness || len(est.Consent.ConsentIDs) != 1 {
		t.Fatalf("with consent: %+v %v", est, err)
	}
	// Withdrawn: refused again.
	if _, err := call[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: c.Consent.ID}, true); err != nil {
		t.Fatal(err)
	}
	_, err = call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "she walks out of the kitchen", From: from(withMaya), Seconds: 4, MaxUSD: usd(5)}, false)
	if codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("after withdrawing: %v", err)
	}
	if e.fake.Calls("create") != 0 {
		t.Fatal("no likeness request may reach the provider without consent")
	}
}

// A look (D126): people create, approve and revoke it; a person's look needs
// their consent; new shots use only approved looks.
func TestLooks(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	me := e.person(t, "Ada")
	clip := e.clipWithFace(t, "studio", me, "Ada", true)
	in := generate.LookCreateIn{Title: "Ada", Person: "Ada", Description: "A woman in her thirties with short dark hair and round glasses.",
		Stills: []generate.LookStillIn{{AssetID: clip.ID, At: 1}, {AssetID: clip.ID, At: 2.5}}, Clips: []generate.LookClipIn{{AssetID: clip.ID, From: 0.5, To: 3}}}
	if _, err := call[generate.LookOut](t, e.a, "looks.create", in, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("people only: %v", err)
	}
	if _, err := call[generate.LookOut](t, e.a, "looks.create", in, true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("a person's look needs their consent: %v", err)
	}
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Ada", Self: true, Statement: "I, Ada, agree to MetaRush making videos with my likeness for my own projects.", Adult: true}, true); err != nil {
		t.Fatal(err)
	}
	l, err := call[generate.LookOut](t, e.a, "looks.create", in, true)
	if err != nil {
		t.Fatal(err)
	}
	if l.Look.State != models.LookDraft || l.Look.Usable || len(l.Look.Stills) != 2 || len(l.Look.Clips) != 1 || l.Look.Clips[0].Seconds > 3.1 || l.Look.ConsentID == "" {
		t.Fatalf("look %+v", l.Look)
	}
	// A draft look isn't used for shots.
	_, err = call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "walks into the garage", Look: "ada", Seconds: 6, MaxUSD: usd(5)}, false)
	if codeOf(err) != apperr.Conflict {
		t.Fatalf("a draft look: %v", err)
	}
	if _, err := call[generate.LookOut](t, e.a, "looks.approve", generate.LookDecideIn{Look: "ada"}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("approving is people only: %v", err)
	}
	if _, err := call[generate.LookOut](t, e.a, "looks.approve", generate.LookDecideIn{Look: "ada"}, true); err != nil {
		t.Fatal(err)
	}
	est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", generate.GenShotIn{ProjectID: e.project, Prompt: "walks into the garage", Look: "ada", Seconds: 6}, false)
	if err != nil || !est.Allowed || est.Task != videogen.TaskReferenceToVideo || !est.Likeness {
		t.Fatalf("estimate %+v %v", est, err)
	}
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "walks into the garage", Look: "ada", Seconds: 6, MaxUSD: usd(est.USD)}, false)
	if err != nil {
		t.Fatal(err)
	}
	g := out.Generations[0]
	if g.Look == nil || g.Look.Version != l.Look.Version || len(g.References) != 3 || !strings.Contains(g.SentPrompt, "<IMAGE_REF_0>") || g.Template.Name != "look" {
		t.Fatalf("how it was made: %+v", g.Generation)
	}
	if _, err := e.run(t, ctx, out.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	it := e.fake.Interactions()[0]
	if it.Images != 2 || len(it.Videos) != 1 || it.Task != videogen.TaskReferenceToVideo {
		t.Fatalf("sent %+v", it)
	}
	if ki, kf := e.fake.Kept(); ki != 0 || kf != 0 {
		t.Fatalf("the likeness pictures were left at the provider: %d %d", ki, kf)
	}
	// Revoked: no new shots with it.
	if _, err := call[generate.LookOut](t, e.a, "looks.revoke", generate.LookDecideIn{Look: "ada"}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call[generate.GenRunOut](t, e.a, "generate.again", generate.GenAgainIn{ID: g.ID, MaxUSD: usd(5)}, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("again with a withdrawn look: %v", err)
	}
	// A character can't be made from a real person's face.
	_, err = call[generate.LookOut](t, e.a, "looks.create", generate.LookCreateIn{Title: "Captain Blue", Description: "A cartoon sea captain with a blue coat.",
		Stills: []generate.LookStillIn{{AssetID: clip.ID, At: 1}}}, true)
	if codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("a character from someone's face: %v", err)
	}
}

// Resume after a restart: the ticket is saved before waiting, so a job that
// stopped while the video was being made picks it up again and nothing is
// sent twice. A send cut off before an answer is never repeated blindly.
func TestResumeAfterRestart(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.fake.ReadyAfter = 3
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "a slow boat", Seconds: 3, Resolution: "360p", MaxUSD: usd(1)}, false)
	if err != nil {
		t.Fatal(err)
	}
	// The first run stops (this machine shuts down) while the video is being made.
	cctx, cancel := context.WithCancel(ctx)
	go func() {
		for e.fake.Calls("get") < 1 {
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	if _, err := e.run(t, cctx, out.JobIDs[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("a stopped run: %v", err)
	}
	g, _ := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	if g.Ticket == nil || g.Outcome != nil || g.State != generate.StateMaking {
		t.Fatalf("after the stop: %+v", g.Generation)
	}
	// The next run resumes from the ticket.
	if _, err := e.run(t, ctx, out.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.Calls("create"); n != 1 {
		t.Fatalf("sent %d times", n)
	}
	g, _ = call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: g.ID}, false)
	if g.State != generate.StateDone {
		t.Fatal(g.State)
	}
	// A send that began and never got an answer recorded (the process died
	// mid-request): not sent again; the estimate counts as spent.
	out2, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "another boat", Seconds: 3, Resolution: "360p", MaxUSD: usd(1)}, false)
	if err != nil {
		t.Fatal(err)
	}
	gid := out2.Generations[0].ID
	_ = e.a.DB.Update(ctx, store.CGenerations, e.a.WorkspaceID(), gid, bson.M{"$push": bson.M{"sending": time.Now().UTC()}})
	if _, err := e.run(t, ctx, out2.JobIDs[0]); err == nil {
		t.Fatal("an interrupted send must not succeed silently")
	}
	g2, _ := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: gid}, false)
	if g2.State != generate.StateFailed || e.fake.Calls("create") != 1 || g2.Settled == nil || g2.Settled.USD != g2.Estimate.USD {
		t.Fatalf("interrupted send: %+v %d", g2.Outcome, e.fake.Calls("create"))
	}
}

// What the provider does wrong is said plainly and costs only what it cost:
// a safety refusal, the spending limit, a partial result, an expired file.
func TestProviderTrouble(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	shot := func(prompt string, res string, secs int) *generate.GenRunOut {
		t.Helper()
		out, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: prompt, Seconds: secs, Resolution: res, NoPeople: true, MaxUSD: usd(10)}, false)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Blocked when sent.
	out := shot("[fake:celebrity] a famous actor waves", "360p", 3)
	_, err := e.run(t, ctx, out.JobIDs[0])
	var fin *generate.Final
	if !errors.As(err, &fin) || codeOf(fin.Err) != apperr.SafetyBlocked {
		t.Fatalf("refusal: %v", err)
	}
	g, _ := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	if g.State != generate.StateRefused || g.Outcome.Safety == nil || len(g.Outcome.Safety.SupportCodes) != 1 || g.Settled.USD != 0 {
		t.Fatalf("refused %+v %+v", g.Outcome, g.Settled)
	}
	// Blocked after making (no video): what it counted is charged.
	out = shot("[fake:child] a birthday party", "360p", 3)
	if _, err := e.run(t, ctx, out.JobIDs[0]); !errors.As(err, &fin) {
		t.Fatalf("output refusal: %v", err)
	}
	g, _ = call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	if g.State != generate.StateRefused || g.Outcome.Safety.Side != "output" || g.Outcome.Safety.Category != "child" {
		t.Fatalf("output refused %+v", g.Outcome.Safety)
	}
	// The spending limit: the job waits ten minutes (not a failure).
	out = shot("[fake:spend] a boat", "360p", 3)
	_, err = e.run(t, ctx, out.JobIDs[0])
	var w *generate.Wait
	if !errors.As(err, &w) || w.After < 10*time.Minute {
		t.Fatalf("spend limit: %v", err)
	}
	// A rate limit: a short wait.
	out = shot("[fake:rate] a boat", "360p", 3)
	if _, err := e.run(t, ctx, out.JobIDs[0]); !errors.As(err, &w) || w.After > 2*time.Minute {
		t.Fatalf("rate limit: %v", err)
	}
	// Partial: a shorter clip, said so.
	out = shot("[fake:partial] a boat", "360p", 6)
	if _, err := e.run(t, ctx, out.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	g, _ = call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	if g.State != generate.StateDone || !g.Outcome.Partial || !strings.Contains(g.Outcome.Message, "instead of the 6 seconds") {
		t.Fatalf("partial %+v", g.Outcome)
	}
	// Expired output: charged (it was made), said plainly.
	out = shot("[fake:expired] a boat", "1080p", 10)
	if _, err := e.run(t, ctx, out.JobIDs[0]); !errors.As(err, &fin) {
		t.Fatalf("expired: %v", err)
	}
	g, _ = call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	if g.State != generate.StateFailed || g.Settled.USD == 0 || !strings.Contains(g.Outcome.Message, "no longer has") {
		t.Fatalf("expired %+v", g.Outcome)
	}
	// A retired model.
	e.fake.Retired["gemini-omni-1.1-flash"] = true
	out = shot("a boat", "360p", 3)
	if _, err := e.run(t, ctx, out.JobIDs[0]); !errors.As(err, &fin) {
		t.Fatalf("retired: %v", err)
	}
	// Cancel while waiting to start: nothing sent, nothing spent.
	delete(e.fake.Retired, "gemini-omni-1.1-flash")
	out = shot("a boat to stop", "360p", 3)
	c, err := call[generate.GenCancelOut](t, e.a, "generate.cancel", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	if err != nil || c.Generation.State != generate.StateCanceled || c.Generation.Settled == nil || c.Generation.Settled.USD != 0 {
		t.Fatalf("cancel %+v %v", c, err)
	}
}

// A key whose Google project is on the free tier (2026-10-20): Google accepted
// the request, then answered every status check with a 429 "limit: 0 … on Free
// Tier", and the shot sat at "being made" for a quarter of an hour. Now the
// first such answer ends it (D178): failed in plain words with Google's page
// behind the hint, nothing charged, the money held for it given back, Google
// asked to stop it and to delete it, the person told. Refused when sent, the same.
func TestFreeTierEndsAtOnce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.fake.SetFreeTier(fakevideogen.FreeTierOnStatus)
	in := generate.GenShotIn{ProjectID: e.project, Prompt: "a red paper boat drifting on a pond", Seconds: 3, Resolution: "360p", MaxUSD: usd(0.5)}
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	j, _ := e.a.Queue.Get(ctx, out.JobIDs[0])
	_, err = generate.Run(ctx, e.a, j, func(p models.Progress) { said = append(said, p.Message) })
	var fin *generate.Final
	if !errors.As(err, &fin) || codeOf(fin.Err) != apperr.Provider || !strings.Contains(apperr.From(fin.Err).Message, "free tier") {
		t.Fatalf("the job ends for good: %v", err)
	}
	if n := e.fake.Calls("get"); n != 1 {
		t.Fatalf("checked %d times: the first free-tier answer ends it", n)
	}
	g, _ := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	oc := g.Outcome
	if g.State != generate.StateFailed || oc == nil || oc.Code != apperr.Provider || g.Ticket == nil ||
		oc.Message != "This Google project is on the free tier, which can't make video. Turn on billing for the project in Google AI Studio, then try again." ||
		!strings.Contains(oc.Hint, "https://ai.dev/rate-limit") || !strings.Contains(oc.Detail, "limit: 0 input tokens per minute on Free Tier") {
		t.Fatalf("outcome %+v", oc)
	}
	if oc.Actual == nil || oc.Actual.Basis != "none" || oc.Actual.USD != 0 || g.Settled == nil || g.Settled.USD != 0 || g.CostUSD != 0 {
		t.Fatalf("nothing was made, so nothing is charged: %+v %+v", oc.Actual, g.Settled)
	}
	if g.Summary != oc.Message {
		t.Fatalf("summary %q", g.Summary)
	}
	for _, scope := range [][2]string{{"project", e.project}, {"day", time.Now().UTC().Format("2006-01-02")}} {
		if l := ledger(t, e.a, scope[0], scope[1]); l.HeldMicros != 0 || l.SpentMicros != 0 || l.Count != 0 {
			t.Fatalf("the money held for it is free again (%s): %+v", scope[0], l)
		}
	}
	if e.fake.Calls("cancel") != 1 || e.fake.Calls("delete") != 1 || g.Forgotten == nil || !g.Forgotten.OK {
		t.Fatalf("Google is asked to stop it and to delete it: cancel %d, delete %d, %+v", e.fake.Calls("cancel"), e.fake.Calls("delete"), g.Forgotten)
	}
	if ki, kf := e.fake.Kept(); ki != 0 || kf != 0 {
		t.Fatalf("left at the provider: %d %d", ki, kf)
	}
	for _, s := range said {
		if strings.Contains(s, "too busy") {
			t.Fatalf("the free tier isn't a busy provider: %q", said)
		}
	}
	// The person hears it plainly: the bell, and the job's own record.
	var n models.Notification
	if err := e.a.DB.C(store.CNotifications).FindOne(ctx, bson.M{"groupKey": "generation:" + g.ID}).Decode(&n); err != nil || n.Kind != models.NotifyGenerationFailed ||
		!strings.Contains(n.Body, "free tier") || !strings.Contains(n.Body, "https://ai.dev/rate-limit") {
		t.Fatalf("notification %+v %v", n, err)
	}
	if f := plainwords.Check(n.Title + ". " + n.Body); len(f) > 0 {
		t.Fatalf("not plain: %v", f)
	}
	j, _ = e.a.Queue.Get(ctx, out.JobIDs[0])
	if adv, _ := j.Payload["advice"].(string); !strings.Contains(adv, "Turn on billing") {
		t.Fatalf("job advice %q", adv)
	}

	// Refused when sent: no ticket, nothing to stop or delete, the same words and no charge.
	e.fake.SetFreeTier(fakevideogen.FreeTierOnStart)
	out, err = call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, ctx, out.JobIDs[0]); !errors.As(err, &fin) {
		t.Fatalf("refused when sent: %v", err)
	}
	g, _ = call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	if g.State != generate.StateFailed || g.Ticket != nil || len(g.Sends) != 1 || g.Sends[0].Result != "rejected" || g.Sends[0].Code != videogen.KindTier ||
		!strings.Contains(g.Outcome.Message, "free tier") || g.Settled == nil || g.Settled.USD != 0 || g.Outcome.Actual.Basis != "none" {
		t.Fatalf("refused when sent: %+v %+v", g.Outcome, g.Sends)
	}
	if e.fake.Calls("create") != 2 || e.fake.Calls("cancel") != 1 {
		t.Fatalf("sent once each, nothing more to stop: create %d cancel %d", e.fake.Calls("create"), e.fake.Calls("cancel"))
	}
	if l := ledger(t, e.a, "project", e.project); l.HeldMicros != 0 || l.SpentMicros != 0 {
		t.Fatalf("ledger %+v", l)
	}
}

// Checks Google answers only with "too many requests" never wait for ever
// (D178): a stretch of 429s that ends is only a wait (the activity bar says
// why); 10 minutes of nothing else (a moment here) puts the job back in the
// queue with a plain reason; after two such waits in a row it stops, asks
// Google to stop the request and says so; the estimate counts, in case it was
// made.
func TestPollRateLimitsDontWaitForEver(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	defer func(w time.Duration) { generate.PollLimitWait = w }(generate.PollLimitWait)
	shot := func(prompt string) *generate.GenRunOut {
		t.Helper()
		out, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: prompt, Seconds: 3, Resolution: "360p", NoPeople: true, MaxUSD: usd(1)}, false)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Two 429s, then answers (a second apart, as Google says): made as usual.
	generate.PollLimitWait = time.Minute
	out := shot("[fake:poll-rate:2] a boat")
	var said []string
	j, _ := e.a.Queue.Get(ctx, out.JobIDs[0])
	if _, err := generate.Run(ctx, e.a, j, func(p models.Progress) { said = append(said, p.Message) }); err != nil {
		t.Fatal(err)
	}
	g, _ := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	if g.State != generate.StateDone {
		t.Fatalf("a stretch of 429s that ends: %+v", g.Outcome)
	}
	if !strings.Contains(strings.Join(said, " | "), "Google Gemini is too busy with this key's requests to say how it's going") {
		t.Fatalf("progress %q", said)
	}
	// Nothing but 429s: after the time is up, a wait in the queue with a plain reason (never a failure).
	generate.PollLimitWait = 300 * time.Millisecond
	out = shot("[fake:poll-rate] a slow boat")
	_, err := e.run(t, ctx, out.JobIDs[0])
	var w *generate.Wait
	if !errors.As(err, &w) || w.After < generate.PollLimitPause || !strings.Contains(w.Reason, "too busy with this key's requests") {
		t.Fatalf("a wait: %v", err)
	}
	if f := plainwords.Check(w.Reason); len(f) > 0 {
		t.Fatalf("not plain: %v", f)
	}
	gid := out.Generations[0].ID
	g, _ = call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: gid}, false)
	if g.Outcome != nil || g.State != generate.StateMaking || e.fake.Calls("cancel") != 0 {
		t.Fatalf("still being made: %+v", g.Outcome)
	}
	// Two such waits in a row (as the queue records them), and still nothing but 429s: it stops.
	now := time.Now().UTC()
	for i := 0; i < generate.PollLimitPauses; i++ {
		at := models.Attempt{Worker: "test", StartedAt: now, EndedAt: &now, Outcome: jobs.OutcomePostponed, Error: w.Reason}
		if err := e.a.DB.Update(ctx, store.CJobs, e.a.WorkspaceID(), out.JobIDs[0], bson.M{"$push": bson.M{"attempts": at}}); err != nil {
			t.Fatal(err)
		}
	}
	var fin *generate.Final
	if _, err := e.run(t, ctx, out.JobIDs[0]); !errors.As(err, &fin) {
		t.Fatalf("it gives up: %v", err)
	}
	g, _ = call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: gid}, false)
	if g.State != generate.StateFailed || !strings.Contains(g.Outcome.Message, "kept turning away MetaRush's checks") || !strings.Contains(g.Outcome.Hint, "https://ai.dev/rate-limit") ||
		g.Outcome.Actual.Basis != "assumed" || g.Settled == nil || g.Settled.USD != g.Estimate.USD {
		t.Fatalf("given up: %+v %+v", g.Outcome, g.Settled)
	}
	if e.fake.Calls("cancel") != 1 {
		t.Fatalf("Google is asked to stop it: %d", e.fake.Calls("cancel"))
	}
	if f := plainwords.Check(g.Outcome.Message + " " + g.Outcome.Hint); len(f) > 0 {
		t.Fatalf("not plain: %v", f)
	}
}

// Keys never reach the database, the activity log or its spool, and never
// travel anywhere but the provider's request header.
func TestKeyNeverWritten(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "a boat", Seconds: 3, Resolution: "360p", MaxUSD: usd(1)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, ctx, out.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	// A failed request too (its error text is recorded).
	out, _ = call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "[fake:celebrity] x", Seconds: 3, Resolution: "360p", NoPeople: true, MaxUSD: usd(1)}, false)
	_, _ = e.run(t, ctx, out.JobIDs[0])
	_, _ = call[providers.TestOut](t, e.a, "provider.test", providers.TestIn{Name: videogen.GeminiName}, false)
	e.a.Log.Flush(ctx)
	names, err := e.a.DB.DB.ListCollectionNames(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		cur, err := e.a.DB.C(n).Find(ctx, bson.M{})
		if err != nil {
			t.Fatal(err)
		}
		var docs []bson.M
		_ = cur.All(ctx, &docs)
		b, _ := json.Marshal(docs)
		if bytes.Contains(b, []byte(e.key)) {
			t.Fatalf("the key is in the %s collection", n)
		}
	}
	// The home folder (state, logs, spool, cache) never holds it either.
	_ = filepath.Walk(e.a.Cfg.Home(), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || info.Size() > 64<<20 {
			return nil
		}
		b, _ := os.ReadFile(p)
		if bytes.Contains(b, []byte(e.key)) {
			t.Errorf("the key is in %s", p)
		}
		return nil
	})
	if p := e.fake.Problems(); len(p) > 0 {
		t.Fatal(p)
	}
}

// A face MetaRush recognized only by its looks (D114) is a hint, not a name:
// consent recorded for the person it looks like doesn't cover it until a
// person confirms who it is.
func TestRecognizedFaceIsNotANameForConsent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	maya := e.person(t, "Maya")
	withMaya := e.clipWithFace(t, "kitchen", maya, "Maya", true)
	porch := e.clipWithFace(t, "porch", "", "", true)
	f := models.FaceTrack{ID: ids.New(ids.Face), WorkspaceID: e.a.WorkspaceID(), AssetID: porch.ID, Track: 1, From: 0, To: 4, At: 1, Box: []float64{0.3, 0.2, 0.3, 0.4},
		Recognized: &models.FaceRecognized{PersonID: maya, Name: "Maya", Score: 0.93, At: time.Now()}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := e.a.DB.Insert(ctx, store.CFaces, f); err != nil {
		t.Fatal(err)
	}
	rec := generate.ConsentRecordIn{Person: "Maya", Statement: "I, Maya, agree to my likeness in generated video for this garden club film.", Recording: withMaya.ID, Adult: true}
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", rec, true); err != nil {
		t.Fatal(err)
	}
	_, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "she waves from the porch",
		From: &generate.FrameRef{AssetID: porch.ID, At: 1}, Seconds: 4, MaxUSD: usd(5)}, false)
	if codeOf(err) != apperr.ConsentRequired || !strings.Contains(apperr.From(err).Message, "looks like Maya") {
		t.Fatalf("a face recognized by its looks only: %v", err)
	}
}

// A shot can start from a picture the person added (Generate video in the
// drop box, D154): it goes as the first frame, after the person says it shows
// no one (MetaRush can't tell from a photo alone).
func TestShotFromAPicture(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 255 / 320), 120, uint8(y * 255 / 180), 255})
		}
	}
	path := filepath.Join(t.TempDir(), "pond.png")
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := resources.AddFile(ctx, e.a, path, resources.AddOptions{ProjectID: e.project})
	if err != nil || added.Resource == nil {
		t.Fatalf("add the picture: %+v %v", added, err)
	}
	in := generate.GenShotIn{ProjectID: e.project, Prompt: "the pond at dawn, mist rising", From: &generate.FrameRef{Picture: added.Resource.Name}, Seconds: 4, Resolution: "720p"}
	est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if est.Allowed || est.Task != videogen.TaskImageToVideo || !strings.Contains(est.Consent.Message+strings.Join(est.Problems, " "), "the picture “pond”") {
		t.Fatalf("a picture nobody checked for people: %+v", est)
	}
	in.NoPeople = true
	est, err = call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil || !est.Allowed || len(est.References) != 1 || est.References[0] != "Starts from the picture “pond”" {
		t.Fatalf("estimate %+v %v", est, err)
	}
	in.MaxUSD = usd(est.USD)
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if err != nil {
		t.Fatal(err)
	}
	g := out.Generations[0]
	if len(g.References) != 1 || g.References[0].Role != models.RefStartFrame || g.References[0].ResourceID != added.Resource.ID {
		t.Fatalf("references %+v", g.References)
	}
	if _, err := e.run(t, ctx, out.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	if e.fake.Calls("create") != 1 {
		t.Fatalf("calls %d", e.fake.Calls("create"))
	}
	// A frame is a clip and a time, or a picture: not both, not neither.
	bad := generate.GenShotIn{ProjectID: e.project, Prompt: "x", From: &generate.FrameRef{}, NoPeople: true}
	if _, err := call[generate.GenEstimate](t, e.a, "generate.estimate", bad, false); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("an empty frame: %v", err)
	}
}
