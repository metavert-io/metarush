package aiwork_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
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
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeai"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/videogen"
)

// fakeKey is a key-shaped value made at run time (no literal for the secret scanner).
func fakeKey() string { return "fk-" + strings.Repeat("Qz7", 10) + "-fake" }

type env struct {
	a       *app.App
	fake    *fakeai.Fake
	project string
}

func setup(t *testing.T) *env {
	t.Helper()
	return setupWith(t, fakeai.Options{})
}

// setupWith is setup with the fake service described as asked.
func setupWith(t *testing.T, o fakeai.Options) *env {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	keyring.MockInit() // never the real keychain
	generate.PollEvery, generate.FirstPoll = 20*time.Millisecond, 2*time.Millisecond
	key := fakeKey()
	fake := fakeai.InstallWith(t, key, o)
	t.Setenv(providers.EnvVar(fakeai.Name), key)
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
	return &env{a: a, fake: fake, project: pid}
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

// run does a job in this process, as a worker would.
func (e *env) run(t *testing.T, jobID string) (map[string]any, error) {
	t.Helper()
	ctx := context.Background()
	j, err := e.a.Queue.Get(ctx, jobID)
	if err != nil {
		t.Fatal(err)
	}
	if !aiwork.Handles(j) {
		t.Fatalf("a generate job for AI work isn't handled as such: %+v", j.Payload)
	}
	return aiwork.Run(ctx, e.a, j, nil)
}

func (e *env) gen(t *testing.T, id string) models.Generation {
	t.Helper()
	var g models.Generation
	if err := e.a.DB.GetByID(context.Background(), store.CGenerations, e.a.WorkspaceID(), id, &g); err != nil {
		t.Fatal(err)
	}
	return g
}

// make prices, agrees and runs one piece of work to its end.
func (e *env) make(t *testing.T, in aiwork.MakeIn) (*aiwork.MakeOut, models.Generation) {
	t.Helper()
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if !est.Allowed {
		t.Fatalf("not allowed: %v", est.Problems)
	}
	in.MaxUSD = usd(est.USD)
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatalf("job: %v", err)
	}
	return out, e.gen(t, out.Work.ID)
}

// sound adds a clip of a tone, seconds long.
func (e *env) sound(t *testing.T, name string, seconds float64) *models.Asset {
	t.Helper()
	p := filepath.Join(t.TempDir(), name+".wav")
	if err := os.WriteFile(p, aiservices.ToneWAV(seconds, 300), 0o600); err != nil {
		t.Fatal(err)
	}
	res := ingest.File(context.Background(), e.a, p, ingest.Options{ProjectID: e.project, FootageOnly: true})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	var as models.Asset
	if err := e.a.DB.GetByID(context.Background(), store.CAssets, e.a.WorkspaceID(), res.AssetID, &as); err != nil {
		t.Fatal(err)
	}
	return &as
}

func ledgerSpent(t *testing.T, a *app.App, scope, key string) int64 {
	var l models.GenSpend
	_ = a.DB.C(store.CGenSpend).FindOne(context.Background(), bson.M{"_id": a.WorkspaceID() + ":" + scope + ":" + key}).Decode(&l)
	if l.HeldMicros != 0 {
		t.Errorf("%s %s still holds %d", scope, key, l.HeldMicros)
	}
	return l.SpentMicros
}

// A picture from words: priced first with the model's terms, refused without
// the agreed price, then made, kept in the resources library with how it was
// made, settled, deleted at the provider — and recorded like every other kind.
func TestPictureFromWords(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in := aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "a watercolour kite over a hill", Options: models.GenOptions{Size: "2K", AspectRatio: "16:9"}}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if !est.Allowed || est.USD != 0.06 || est.Choice != "asked" || est.Licence.Summary == "" || est.Data == "" || est.Cost.About != "about $0.06" || len(est.Cost.Parts) != 1 {
		t.Fatalf("estimate %+v", est)
	}
	if r := plainwords.Check(est.Summary); len(r) > 0 {
		t.Errorf("summary %q: %v", est.Summary, r)
	}
	if _, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("no price: %v", err)
	}
	if e.fake.Calls("submit") != 0 {
		t.Fatal("nothing may reach the provider before the price is agreed")
	}
	out, g := e.make(t, in)
	if out.Work.State != aiwork.StateWaiting || !strings.Contains(out.Summary, "resources library") {
		t.Fatalf("make %+v", out)
	}
	if g.Kind != aiservices.KindPicture || g.Operation != models.GenMake || g.Outcome == nil || g.Outcome.State != models.GenDone || g.Settled == nil || g.Forgotten == nil || !g.Forgotten.OK {
		t.Fatalf("record %+v", g)
	}
	if g.Terms == nil || g.Terms.Commercial != "yes" || g.Choice == nil || g.Choice.By != "asked" || g.Options == nil || g.Options.Size != "2K" || g.RequestHash == "" || g.Ticket == nil {
		t.Fatalf("record parts %+v %+v %+v", g.Terms, g.Choice, g.Options)
	}
	o := g.Outcome.Outputs[0]
	var res models.Resource
	if err := e.a.DB.GetByID(ctx, store.CResources, e.a.WorkspaceID(), o.ResourceID, &res); err != nil {
		t.Fatal(err)
	}
	v := res.Latest()
	if v == nil || v.Source.MadeWith == nil || v.Source.MadeWith.Kind != "ai" || v.Source.MadeWith.GenerationID != g.ID || !strings.Contains(strings.Join(res.Tags, ","), models.TagAIGenerated) {
		t.Fatalf("resource %+v", res)
	}
	if e.fake.Kept() != 0 {
		t.Error("the provider still keeps the request")
	}
	if spent := ledgerSpent(t, e.a, "project", e.project); spent != 60000 {
		t.Errorf("project spent %d micro-dollars", spent)
	}
	shown, err := call[aiwork.WorkView](t, e.a, "ai.show", aiwork.IDIn{ID: o.ResourceID}, false)
	if err != nil || shown.ID != g.ID || shown.State != aiwork.StateDone || len(shown.Outputs) != 1 || shown.Outputs[0].Kind != "picture" {
		t.Fatalf("show %+v %v", shown, err)
	}
	// Generated video's lists keep to video.
	vl, err := call[generate.GenListOut](t, e.a, "generate.list", generate.GenListIn{}, false)
	if err != nil || len(vl.Generations) != 0 {
		t.Fatalf("generate.list shows other kinds: %+v %v", vl, err)
	}
	if _, err := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: g.ID}, false); codeOf(err) != apperr.NotFound {
		t.Fatalf("generate.show of a picture: %v", err)
	}
	// Extending or editing it as a video says what it is.
	for op, in := range map[string]any{"generate.extend": generate.GenExtendIn{ID: g.ID, DryRun: true}, "generate.edit": generate.GenEditIn{ID: g.ID, Instruction: "make it night", DryRun: true}} {
		_, err := call[generate.GenRunOut](t, e.a, op, in, false)
		if codeOf(err) != apperr.NotFound || !strings.Contains(err.Error(), "isn't a generated video") {
			t.Errorf("%s of a picture: %v", op, err)
		}
	}
}

// Sound effects and music land in Sources as clips marked as AI-generated.
func TestSoundBecomesClips(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for _, kind := range []string{aiservices.KindSoundEffect, aiservices.KindMusic} {
		_, g := e.make(t, aiwork.MakeIn{Kind: kind, ProjectID: e.project, Model: "fake/fake-" + kind, Prompt: "a calm morning in the garden", Options: models.GenOptions{Seconds: 4}})
		if g.Outcome.State != models.GenDone || len(g.Outcome.Outputs) != 1 {
			t.Fatalf("%s: %+v", kind, g.Outcome)
		}
		var as models.Asset
		if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), g.Outcome.Outputs[0].AssetID, &as); err != nil {
			t.Fatal(err)
		}
		if !as.Source.IsGenerated() || as.Source.Generated.Kind != kind || as.Source.Generated.GenerationID != g.ID || !strings.Contains(strings.Join(as.Tags, ","), models.TagAIGenerated) {
			t.Fatalf("%s clip %+v", kind, as.Source)
		}
		if !strings.HasPrefix(as.Source.GeneratedLine(), "A") || !strings.Contains(as.Source.GeneratedLine(), "Fake AI") {
			t.Errorf("line %q", as.Source.GeneratedLine())
		}
		if g.Settled == nil || g.Settled.USD <= 0 || g.Outcome.Actual.Basis != "usage" {
			t.Errorf("%s settled %+v %+v", kind, g.Settled, g.Outcome.Actual)
		}
	}
	list, err := call[aiwork.ListOut](t, e.a, "ai.list", aiwork.ListIn{ProjectID: e.project}, false)
	if err != nil || len(list.Work) != 2 || list.SpentUSD <= 0 {
		t.Fatalf("list %+v %v", list, err)
	}
}

// A dub runs a while at the provider: the ticket is saved before waiting, a
// restart resumes it without sending again, and the dub is a new clip.
func TestDubbingWaitsAndResumes(t *testing.T) {
	e := setup(t)
	clip := e.sound(t, "interview", 3)
	e.fake.ReadyAfter = 3
	in := aiwork.MakeIn{Kind: aiservices.KindDubbing, ProjectID: e.project, Model: "fake/fake-dubbing", Clip: clip.ID, Options: models.GenOptions{TargetLanguage: "es"}}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || !est.Allowed || est.Units != "3 seconds heard" {
		t.Fatalf("estimate %+v %v", est, err)
	}
	in.MaxUSD = usd(est.USD)
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	// A machine that stops while waiting resumes later from the saved ticket.
	e.fake.ReadyAfter = 1000
	ctx, cancel := context.WithCancel(context.Background())
	j, _ := e.a.Queue.Get(ctx, out.JobID)
	go func() {
		for i := 0; i < 500; i++ {
			if g := e.gen(t, out.Work.ID); g.Ticket != nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	if _, err := aiwork.Run(ctx, e.a, j, nil); err == nil {
		t.Fatal("a stopped job reported success")
	}
	g := e.gen(t, out.Work.ID)
	if g.Ticket == nil || g.Ticket.ID == "" || g.Outcome != nil {
		t.Fatalf("after stopping: %+v", g)
	}
	e.fake.ReadyAfter = 3
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	g = e.gen(t, out.Work.ID)
	if e.fake.Calls("submit") != 1 {
		t.Fatalf("sent %d times", e.fake.Calls("submit"))
	}
	if g.Outcome == nil || g.Outcome.State != models.GenDone || g.Outcome.Outputs[0].AssetID == "" || g.Outcome.Outputs[0].Language != "es" {
		t.Fatalf("outcome %+v", g.Outcome)
	}
	var as models.Asset
	_ = e.a.DB.GetByID(context.Background(), store.CAssets, e.a.WorkspaceID(), g.Outcome.Outputs[0].AssetID, &as)
	if as.DisplayTitle() != "interview (dubbed into Spanish)" {
		t.Errorf("title %q", as.DisplayTitle())
	}
	if len(g.SourceAssets) != 1 || g.SourceAssets[0] != clip.ID || len(g.References) != 1 || g.References[0].SHA256 == "" {
		t.Errorf("lineage %+v %+v", g.SourceAssets, g.References)
	}
}

// A cloud transcript (opt-in) replaces the clip's words through the ordinary
// transcribe job, with the provider recorded.
func TestCloudTranscriptGoesToTheClip(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	clip := e.sound(t, "talk", 4)
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindTranscription, Clip: clip.ID, From: usd(1)}, false); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("a part of a clip: %v", err)
	}
	_, g := e.make(t, aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "fake/fake-transcription", Clip: clip.ID})
	o := g.Outcome.Outputs[0]
	if o.AssetID != clip.ID || o.StorageKey == "" || o.Words == 0 || o.Delivery != "transcript" {
		t.Fatalf("output %+v", o)
	}
	var js []models.Job
	_ = e.a.DB.Find(ctx, store.CJobs, e.a.WorkspaceID(), bson.M{"type": jobs.TypeTranscribe, "assetId": clip.ID, "payload.generationId": g.ID}, &js)
	if len(js) != 1 {
		t.Fatalf("transcribe jobs %d", len(js))
	}
	tr, name, model, err := aiwork.CloudTranscript(ctx, e.a, g.ID)
	if err != nil || name != fakeai.Name || model != "fake-transcription" || len(tr.Segments) != 2 || tr.Segments[0].Words[0].Text != "this" {
		t.Fatalf("transcript %+v %s %s %v", tr, name, model, err)
	}
}

// What the provider's filters decline costs nothing; a send that may have
// reached the provider is never sent again and counts as spent.
func TestRefusalsAndUncertainSends(t *testing.T) {
	e := setup(t)
	in := aiwork.MakeIn{Kind: aiservices.KindSoundEffect, ProjectID: e.project, Model: "fake/fake-sound-effect", Prompt: "a door [fake:safety]", Options: models.GenOptions{Seconds: 2}}
	est, _ := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	in.MaxUSD = usd(est.USD)
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err == nil {
		t.Fatal("a refusal succeeded")
	}
	g := e.gen(t, out.Work.ID)
	if g.Outcome.State != models.GenRefused || g.Settled == nil || g.Settled.USD != 0 || g.Outcome.Safety == nil {
		t.Fatalf("refused %+v %+v", g.Outcome, g.Settled)
	}
	in.Prompt = "a door [fake:uncertain]"
	out, err = call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = e.run(t, out.JobID)
	_, _ = e.run(t, out.JobID) // a second try never sends again
	g = e.gen(t, out.Work.ID)
	if g.Outcome.State != models.GenFailed || g.Settled == nil || g.Settled.USD != g.Estimate.USD || e.fake.Calls("submit") != 2 {
		t.Fatalf("uncertain %+v %+v submits %d", g.Outcome, g.Settled, e.fake.Calls("submit"))
	}
}

// A project's budget for generated video covers every kind of AI work.
func TestBudgetCoversEveryKind(t *testing.T) {
	e := setup(t)
	if _, err := call[generate.GenSettingsOut](t, e.a, "generate.configure", generate.GenConfigureIn{ProjectID: e.project, BudgetUSD: usd(0.05)}, true); err != nil {
		t.Fatal(err)
	}
	in := aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "a big poster", Options: models.GenOptions{Size: "4K"}, MaxUSD: usd(1)}
	est, _ := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if est.Allowed || !strings.Contains(strings.Join(est.Problems, " "), "project's budget") {
		t.Fatalf("estimate %+v", est)
	}
	_, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if codeOf(err) != apperr.Budget || !strings.Contains(err.Error(), "budget for AI work") {
		t.Fatalf("over budget: %v", err)
	}
	if e.fake.Calls("submit") != 0 {
		t.Fatal("sent over budget")
	}
}

// Content Settings keep a client's work away from providers, pin a model per
// kind, or keep it away from every AI service — generated video included.
func TestContentSettingsPinAndForbid(t *testing.T) {
	e := setup(t)
	if _, err := ops.CallTyped[ops.ContentShowOut](context.Background(), e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if _, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, Pin: map[string]string{aiservices.KindMusic: "fake/fake-music"}}, true); err != nil {
		t.Fatal(err)
	}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindMusic, ProjectID: e.project, Prompt: "calm guitar", Options: models.GenOptions{Seconds: 10}}, false)
	if err != nil || est.Provider != fakeai.Name || est.Choice != "content-settings" {
		t.Fatalf("pinned %+v %v", est, err)
	}
	note := "the client's contract"
	if _, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, Forbid: []string{"fake"}, Unpin: []string{aiservices.KindMusic}, Note: &note}, true); err != nil {
		t.Fatal(err)
	}
	_, err = call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindMusic, ProjectID: e.project, Model: "fake/fake-music", Prompt: "calm guitar"}, false)
	if codeOf(err) != apperr.Conflict || !strings.Contains(apperr.From(err).Hint, "contract") {
		t.Fatalf("forbidden: %v", err)
	}
	// Without asking, the usual model (not the forbidden provider's) is chosen.
	est, err = call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindMusic, ProjectID: e.project, Prompt: "calm guitar", Options: models.GenOptions{Seconds: 10}}, false)
	if err != nil || est.Provider == fakeai.Name {
		t.Fatalf("default %+v %v", est, err)
	}
	for _, alt := range est.Alternatives {
		if alt.Provider == fakeai.Name {
			t.Errorf("a forbidden provider offered: %+v", alt)
		}
	}
	none := true
	// Lifting or setting a client's rule is a person's decision (an agent, an MCP client: refused).
	if _, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, None: &none}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("not a person: %v", err)
	}
	if _, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, None: &none}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindMusic, ProjectID: e.project, Prompt: "x"}, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("none: %v", err)
	}
	vest, err := call[generate.GenEstimate](t, e.a, "generate.estimate", generate.GenShotIn{ProjectID: e.project, Prompt: "a boat", Seconds: 4, Resolution: "360p"}, false)
	if err != nil || vest.Allowed || !strings.Contains(strings.Join(vest.Problems, " "), "nothing goes to an AI service") {
		t.Fatalf("video under none: %+v %v", vest, err)
	}
	shown, err := call[aiwork.RulesOut](t, e.a, "ai.rules", aiwork.RulesIn{ProjectID: e.project}, false)
	if err != nil || !shown.Rules.None || shown.Rules.Note != note {
		t.Fatalf("rules %+v %v", shown, err)
	}
	models, err := call[aiwork.ModelsOut](t, e.a, "ai.models", aiwork.ModelsIn{ProjectID: e.project, Kind: aiservices.KindMusic}, false)
	if err != nil || len(models.Kinds) != 1 || len(models.Kinds[0].Models) < 2 {
		t.Fatalf("models %+v %v", models, err)
	}
	for _, m := range models.Kinds[0].Models {
		if m.Allowed || m.Rate == "" {
			t.Errorf("model %s: allowed %v rate %q", m.ID, m.Allowed, m.Rate)
		}
	}
}

// A voice copy needs the person's consent, their own named voice in the
// sample, and — for a provider that wants it — them reading its consent
// script; only a person asks for one; withdrawing the consent deletes it at
// the provider.
func TestVoiceCopyConsent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	sample := e.sound(t, "ada-sample", 12)
	consentClip := e.sound(t, "ada-consent", 6)
	in := aiwork.CopyIn{ProjectID: e.project, Model: "fake/fake-voice-copy", Person: "Ada Lovelace", Name: "Ada narration", Sample: sample.ID}
	if _, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("not a person: %v", err)
	}
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Ada Lovelace", Self: true, Adult: true,
		Statement: "I, Ada Lovelace, agree to MetaRush copying my voice for my own videos."}, true); err != nil {
		t.Fatal(err)
	}
	in.DryRun = true
	out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Estimate.Allowed || !strings.Contains(strings.Join(out.Estimate.Problems, " "), "consent script") {
		t.Fatalf("no consent recording: %+v", out.Estimate.Problems)
	}
	// Name Ada's voice in both clips, and give the consent clip the script's words.
	var person models.Person
	_ = e.a.DB.C(store.CPeople).FindOne(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "name": "Ada Lovelace"}).Decode(&person)
	script := fakeai.Catalog().ConsentScripts["en-US"]
	for _, c := range []*models.Asset{sample, consentClip} {
		if err := e.a.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: e.a.WorkspaceID(), AssetID: c.ID, Label: "speaker_00",
			Name: person.Name, PersonID: person.ID, Source: "person", Naming: byPerson(e), Seconds: 5}); err != nil {
			t.Fatal(err)
		}
		text := "hello there"
		if c == consentClip {
			text = script
		}
		var words []models.Word
		for i, w := range strings.Fields(text) {
			words = append(words, models.Word{Text: w, From: float64(i) * 0.3, To: float64(i)*0.3 + 0.25})
		}
		if err := e.a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: e.a.WorkspaceID(), AssetID: c.ID, From: 0, To: 5,
			Text: text, Speaker: "speaker_00", Words: words, Provider: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	in.ConsentClip = consentClip.ID
	out, err = call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil || !out.Estimate.Allowed || len(out.Estimate.Consent.ConsentIDs) != 1 {
		t.Fatalf("with consent %+v %v", out, err)
	}
	in.DryRun, in.MaxUSD = false, usd(out.Estimate.USD)
	out, err = call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	g := e.gen(t, out.Work.ID)
	if g.Kind != aiservices.KindVoiceCopy || g.Outcome.Outputs[0].VoiceID == "" || len(g.References) != 2 {
		t.Fatalf("voice copy %+v", g)
	}
	voices, err := call[aiwork.VoicesOut](t, e.a, "ai.voices", aiwork.VoicesIn{Person: "Ada Lovelace"}, false)
	if err != nil || len(voices.Voices) != 1 || voices.Voices[0].State != "ready" {
		t.Fatalf("voices %+v %v", voices, err)
	}
	// Speech in her voice relies on her consent too.
	voice := g.Outcome.Outputs[0].VoiceID
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindVoice, ProjectID: e.project, Model: "fake/fake-voice", Prompt: "Welcome back.", Options: models.GenOptions{Voice: voice}}, false)
	if err != nil || !est.Allowed || len(est.Consent.ConsentIDs) != 1 {
		t.Fatalf("speech with the copy %+v %v", est, err)
	}
	if _, err := call[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: g.ConsentIDs[0]}, true); err != nil {
		t.Fatal(err)
	}
	if left := e.fake.Voices(); len(left) != 0 {
		t.Fatalf("the provider still keeps %v", left)
	}
	g = e.gen(t, g.ID)
	if g.Withdrawn == nil || g.Removed == nil || !g.Removed.OK {
		t.Fatalf("withdrawn %+v removed %+v", g.Withdrawn, g.Removed)
	}
	est, err = call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindVoice, ProjectID: e.project, Model: "fake/fake-voice", Prompt: "Welcome back.", Options: models.GenOptions{Voice: voice}}, false)
	if err != nil || est.Allowed {
		t.Fatalf("speech after withdrawal %+v %v", est, err)
	}
}

// A picture of someone named on the People page needs their consent; pictures
// sent along need a person to say they show no one.
func TestPicturesOfPeopleNeedConsent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := e.a.DB.Insert(ctx, store.CPeople, models.Person{ID: ids.New(ids.Person), WorkspaceID: e.a.WorkspaceID(), Name: "Maya Lindqvist", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "Maya waving at the camera"}, false)
	if err != nil || est.Allowed || est.Consent.OK {
		t.Fatalf("named person %+v %v", est, err)
	}
	_, err = call[aiwork.MakeOut](t, e.a, "ai.make", aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "Maya waving", MaxUSD: usd(1)}, false)
	if codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("make: %v", err)
	}
}

// The rest of the catalogue's kinds go through the same job: speech and a
// voice change are clips whose words are written down.
func TestSpeechAndVoiceChange(t *testing.T) {
	e := setup(t)
	clip := e.sound(t, "take-3", 2)
	_, g := e.make(t, aiwork.MakeIn{Kind: aiservices.KindVoice, ProjectID: e.project, Model: "fake/fake-voice", Prompt: "Welcome to the garden.", Options: models.GenOptions{Voice: "Kore"}})
	if g.Outcome.State != models.GenDone || g.Outcome.Outputs[0].AssetID == "" {
		t.Fatalf("speech %+v", g.Outcome)
	}
	_, g = e.make(t, aiwork.MakeIn{Kind: aiservices.KindVoiceChange, Model: "fake/fake-voice-change", Clip: clip.ID, Options: models.GenOptions{Voice: "Kore"}})
	if g.Outcome.State != models.GenDone || g.ProjectID != e.project {
		t.Fatalf("voice change %+v (project %q)", g.Outcome, g.ProjectID)
	}
	var as models.Asset
	_ = e.a.DB.GetByID(context.Background(), store.CAssets, e.a.WorkspaceID(), g.Outcome.Outputs[0].AssetID, &as)
	if as.DisplayTitle() != "take-3 (another voice)" {
		t.Errorf("title %q", as.DisplayTitle())
	}
}

// Google's adapters run through the same job against the fake Gemini API:
// a picture (the usual model), a cloud transcript, and speech.
func TestGoogleThroughTheJob(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	gkey := "gk-" + strings.Repeat("Mn3", 10) + "-fake"
	gfake := fakevideogen.New(gkey)
	t.Cleanup(gfake.Close)
	t.Setenv(videogen.EnvGeminiBaseURL, gfake.URL)
	t.Setenv(providers.EnvVar(videogen.GeminiName), "")
	if _, err := ops.CallTyped[providers.Status](ctx, e.a, "provider.set", providers.SetIn{Name: videogen.GeminiName, Key: gkey}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	_, g := e.make(t, aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Prompt: "a watercolour kite", Options: models.GenOptions{Size: "1K", AspectRatio: "1:1"}})
	if g.Provider != videogen.GeminiName || g.Model != "gemini-nano-banana-2.1" || g.Outcome.State != models.GenDone || g.Outcome.Outputs[0].ResourceID == "" || g.Choice.By != "default" {
		t.Fatalf("picture %+v %+v", g.Outcome, g.Choice)
	}
	if g.Settled == nil || g.Settled.USD < 0.0336 {
		t.Errorf("settled %+v", g.Settled)
	}
	clip := e.sound(t, "talk", 3)
	_, g = e.make(t, aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "gemini/gemini-3.5-transcribe", Clip: clip.ID})
	if g.Outcome.State != models.GenDone || g.Outcome.Outputs[0].Words != 8 {
		t.Fatalf("transcript %+v", g.Outcome)
	}
	_, g = e.make(t, aiwork.MakeIn{Kind: aiservices.KindVoice, ProjectID: e.project, Model: "gemini/gemini-3.8-flash-tts", Prompt: "Welcome to the garden.", Options: models.GenOptions{Voice: "Kore"}})
	if g.Outcome.State != models.GenDone || g.Outcome.Actual == nil || g.Outcome.Actual.Basis != "usage" {
		t.Fatalf("speech %+v", g.Outcome)
	}
	if ps := gfake.Problems(); len(ps) > 0 {
		t.Errorf("the fake saw: %v", ps)
	}
}

// ElevenLabs' adapters run through the same job against the fake: a sound
// effect (kept there: no way to delete it, and the record says so), a dub
// that waits, and a voice change whose history item is deleted.
func TestElevenLabsThroughTheJob(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	xkey := "xk-" + strings.Repeat("Wp2", 10) + "-fake"
	xfake := fakeeleven.New(xkey)
	t.Cleanup(xfake.Close)
	xfake.SetDubPolls(2)
	t.Setenv(providers.EnvElevenLabsBaseURL, xfake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	if _, err := ops.CallTyped[providers.Status](ctx, e.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: xkey}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	_, g := e.make(t, aiwork.MakeIn{Kind: aiservices.KindSoundEffect, ProjectID: e.project, Prompt: "a wooden door creaks", Options: models.GenOptions{Seconds: 3}})
	if g.Provider != providers.ElevenLabsName || g.Outcome.State != models.GenDone || g.Forgotten == nil || !g.Forgotten.Kept || g.Forgotten.OK {
		t.Fatalf("sound effect %+v %+v", g.Outcome, g.Forgotten)
	}
	if g.Settled.USD != 0.006 {
		t.Errorf("3 seconds at $0.12 a minute: %v", g.Settled.USD)
	}
	clip := e.sound(t, "interview", 3)
	_, g = e.make(t, aiwork.MakeIn{Kind: aiservices.KindDubbing, ProjectID: e.project, Clip: clip.ID, Options: models.GenOptions{TargetLanguage: "fr"}})
	if g.Outcome.State != models.GenDone || g.Ticket.ID == "" || !g.Forgotten.OK || g.Outcome.Outputs[0].Language != "fr" {
		t.Fatalf("dub %+v %+v", g.Outcome, g.Forgotten)
	}
	_, g = e.make(t, aiwork.MakeIn{Kind: aiservices.KindVoiceChange, ProjectID: e.project, Clip: clip.ID, Options: models.GenOptions{Voice: "voice_bram"}})
	if g.Outcome.State != models.GenDone || !g.Forgotten.OK {
		t.Fatalf("voice change %+v %+v", g.Outcome, g.Forgotten)
	}
	if xfake.Kept() != 0 {
		t.Errorf("ElevenLabs still keeps %d things MetaRush could delete", xfake.Kept())
	}
}

// A provider that answers 200 with nothing usable has been paid: the job ends
// there, failed with the estimate counted, and nothing is sent again however
// often the queue runs it.
func TestAnsweredButUnusableIsNeverSentAgain(t *testing.T) {
	e := setup(t)
	gkey := "gk-" + strings.Repeat("Mn3", 10) + "-fake"
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != gkey {
			w.WriteHeader(401)
			return
		}
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, ":generateContent") {
			posts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"(silence)"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3200,"candidatesTokenCount":4}}`))
			return
		}
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(videogen.EnvGeminiBaseURL, srv.URL)
	t.Setenv(providers.EnvVar(videogen.GeminiName), gkey)
	clip := e.sound(t, "quiet", 3)
	in := aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "gemini/gemini-3.5-transcribe", Clip: clip.ID}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	in.MaxUSD = usd(est.USD)
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ { // the queue would run it again after an error
		_, _ = e.run(t, out.JobID)
	}
	g := e.gen(t, out.Work.ID)
	if posts.Load() != 1 {
		t.Fatalf("sent %d times", posts.Load())
	}
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || g.Settled == nil || g.Settled.USD != g.Estimate.USD || !strings.Contains(g.Outcome.Message, "no words") {
		t.Fatalf("outcome %+v settled %+v", g.Outcome, g.Settled)
	}
}

// A project whose Content Settings keep its work away from AI services keeps
// its clips away too: asked for without a project, or from another project,
// transcription and dubbing of its clip are refused; and work queued before
// the rule was set is checked again before it is sent (refused at $0).
func TestRulesFollowTheClipsProjects(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	clip := e.sound(t, "client-interview", 4)
	// Queued while allowed…
	in := aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "fake/fake-transcription", Clip: clip.ID}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	in.MaxUSD = usd(est.USD)
	queued, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	none := true
	if _, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, None: &none}, true); err != nil {
		t.Fatal(err)
	}
	// …and refused when it runs, before anything is sent.
	_, _ = e.run(t, queued.JobID)
	g := e.gen(t, queued.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || g.Settled == nil || g.Settled.USD != 0 || e.fake.Calls("submit") != 0 || !strings.Contains(g.Outcome.Message, "Nothing was sent") {
		t.Fatalf("queued before the rule: %+v %+v submits %d", g.Outcome, g.Settled, e.fake.Calls("submit"))
	}
	other := ids.New(ids.Project)
	now := time.Now().UTC()
	if err := e.a.DB.Insert(ctx, store.CProjects, models.Project{ID: other, WorkspaceID: e.a.WorkspaceID(), Name: "Other", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{aiservices.KindTranscription, aiservices.KindDubbing} {
		for _, from := range []string{"", other} {
			in := aiwork.MakeIn{Kind: kind, ProjectID: from, Model: "fake/fake-" + kind, Clip: clip.ID, Options: models.GenOptions{TargetLanguage: "fr"}, MaxUSD: usd(5)}
			if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false); codeOf(err) != apperr.Conflict {
				t.Errorf("%s from %q: %v", kind, from, err)
			}
			if _, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false); codeOf(err) != apperr.Conflict {
				t.Errorf("%s made from %q: %v", kind, from, err)
			}
		}
	}
	if e.fake.Calls("submit") != 0 {
		t.Fatal("a clip of a project that allows no AI service was sent")
	}
}

// copyReady sets up a person with consent and a sample and consent clip in
// their named voice (the fake provider's consent script), and returns the
// request for a voice copy, priced.
func (e *env) copyReady(t *testing.T, name string) (aiwork.CopyIn, models.Consent) {
	t.Helper()
	ctx := context.Background()
	out, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: name, Self: true, Adult: true,
		Statement: "I, " + name + ", agree to MetaRush copying my voice for my own videos."}, true)
	if err != nil {
		t.Fatal(err)
	}
	var person models.Person
	_ = e.a.DB.C(store.CPeople).FindOne(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "name": name}).Decode(&person)
	sample, consentClip := e.sound(t, name+" sample", 12), e.sound(t, name+" consent", 6)
	script := fakeai.Catalog().ConsentScripts["en-US"]
	for _, c := range []*models.Asset{sample, consentClip} {
		if err := e.a.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: e.a.WorkspaceID(), AssetID: c.ID, Label: "speaker_00",
			Name: person.Name, PersonID: person.ID, Source: "person", Naming: byPerson(e), Seconds: 5}); err != nil {
			t.Fatal(err)
		}
		text := "hello there"
		if c == consentClip {
			text = script
		}
		var words []models.Word
		for i, w := range strings.Fields(text) {
			words = append(words, models.Word{Text: w, From: float64(i) * 0.3, To: float64(i)*0.3 + 0.25})
		}
		if err := e.a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: e.a.WorkspaceID(), AssetID: c.ID, From: 0, To: 5,
			Text: text, Speaker: "speaker_00", Words: words, Provider: "test"}); err != nil {
			t.Fatal(err)
		}
	}
	in := aiwork.CopyIn{ProjectID: e.project, Model: "fake/fake-voice-copy", Person: name, Name: name + " narration", Sample: sample.ID, ConsentClip: consentClip.ID, DryRun: true}
	est, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil || !est.Estimate.Allowed {
		t.Fatalf("voice copy estimate %+v %v", est, err)
	}
	in.DryRun, in.MaxUSD = false, usd(est.Estimate.USD)
	return in, out.Consent.Consent
}

// A voice copy still waiting when its consent is withdrawn is stopped, and if
// its job runs anyway it is refused at $0 before anything is sent.
func TestVoiceCopyQueuedWhenConsentWithdrawn(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in, consent := e.copyReady(t, "Ada Lovelace")
	out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := call[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: consent.ID}, true)
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := e.a.Queue.Get(ctx, out.JobID); !j.CancelRequested && j.State != models.JobCanceled {
		t.Errorf("the queued copy wasn't stopped: %+v (%v)", j.State, rev.AlsoWithdrawn)
	}
	_, _ = e.run(t, out.JobID)
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || g.Outcome.Code != apperr.ConsentRequired || g.Settled == nil || g.Settled.USD != 0 {
		t.Fatalf("outcome %+v settled %+v", g.Outcome, g.Settled)
	}
	if e.fake.Calls("submit") != 0 || len(e.fake.Voices()) != 0 {
		t.Fatal("a voice was copied after its consent was withdrawn")
	}
}

// A voice copy finished while its consent was being withdrawn doesn't stay at
// the provider: the job withdraws and deletes it as it ends.
func TestVoiceCopyMadeDuringWithdrawalIsDeleted(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in, consent := e.copyReady(t, "Grace Hopper")
	e.fake.Script(fakeai.Behavior{Kind: fakeai.Delay, N: 1}) // made at the provider when sent; finishes on a later check
	out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithCancel(ctx)
	j, _ := e.a.Queue.Get(ctx, out.JobID)
	go func() {
		for i := 0; i < 500; i++ {
			if g := e.gen(t, out.Work.ID); g.Ticket != nil {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	_, _ = aiwork.Run(cctx, e.a, j, nil)
	// Withdrawn after it was sent (straight in the record: the race the hook can't see).
	now := time.Now().UTC()
	if err := e.a.DB.Update(ctx, store.CConsents, e.a.WorkspaceID(), consent.ID, bson.M{"$set": bson.M{"revokedAt": now}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	g := e.gen(t, out.Work.ID)
	if g.Outcome.State != models.GenDone || g.Withdrawn == nil || g.Removed == nil || len(e.fake.Voices()) != 0 {
		t.Fatalf("withdrawn %+v removed %+v voices %v", g.Withdrawn, g.Removed, e.fake.Voices())
	}
}

// Withdrawn on a computer without the provider's key, a voice copy is deleted
// by a job a computer with the key takes, tried again until it is done; the
// consent says where each copy stands.
func TestVoiceCopyDeletionIsAJob(t *testing.T) {
	e := setup(t)
	in, consent := e.copyReady(t, "Katherine Johnson")
	out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	key := os.Getenv(providers.EnvVar(fakeai.Name))
	t.Setenv(providers.EnvVar(fakeai.Name), "") // this computer has no key now
	if _, err := call[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: consent.ID}, true); err != nil {
		t.Fatal(err)
	}
	g := e.gen(t, out.Work.ID)
	if g.Withdrawn == nil || g.Withdrawn.JobID == "" || g.Removed != nil || len(e.fake.Voices()) != 1 {
		t.Fatalf("withdrawn %+v removed %+v", g.Withdrawn, g.Removed)
	}
	shown, err := call[generate.ConsentView](t, e.a, "consent.show", generate.ConsentIDIn{ID: consent.ID}, false)
	if err != nil || len(shown.VoiceCopies) != 1 || !strings.Contains(shown.VoiceCopies[0], "being deleted") {
		t.Fatalf("consent page %+v %v", shown.VoiceCopies, err)
	}
	var wait *generate.Wait
	if _, err := e.run(t, g.Withdrawn.JobID); !errors.As(err, &wait) {
		t.Fatalf("without the key the job passes it on: %v", err)
	}
	t.Setenv(providers.EnvVar(fakeai.Name), key) // a computer with the key
	if _, err := e.run(t, g.Withdrawn.JobID); err != nil {
		t.Fatal(err)
	}
	g = e.gen(t, out.Work.ID)
	if g.Removed == nil || len(e.fake.Voices()) != 0 {
		t.Fatalf("removed %+v voices %v", g.Removed, e.fake.Voices())
	}
	shown, _ = call[generate.ConsentView](t, e.a, "consent.show", generate.ConsentIDIn{ID: consent.ID}, false)
	if len(shown.VoiceCopies) != 1 || !strings.Contains(shown.VoiceCopies[0], "deleted at") {
		t.Errorf("consent page after %+v", shown.VoiceCopies)
	}
}

// byPerson is a naming a person gave (D274), for voices and faces tests name.
func byPerson(e *env) *models.Naming {
	return &models.Naming{By: models.NamedByPerson, Actor: e.a.Actor, At: time.Now().UTC()}
}

// words writes a clip's transcript: each word with its start time, said by person.
func (e *env) words(t *testing.T, as *models.Asset, person models.Person, ws []string, at func(i int) float64) {
	t.Helper()
	ctx := context.Background()
	_ = e.a.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: e.a.WorkspaceID(), AssetID: as.ID, Label: "speaker_00",
		Name: person.Name, PersonID: person.ID, Source: "person", Naming: byPerson(e), Seconds: 5})
	var words []models.Word
	for i, w := range ws {
		words = append(words, models.Word{Text: w, From: at(i), To: at(i) + 0.25})
	}
	if err := e.a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: e.a.WorkspaceID(), AssetID: as.ID, From: 0, To: at(len(ws)-1) + 0.3,
		Text: strings.Join(ws, " "), Speaker: "speaker_00", Words: words, Provider: "test"}); err != nil {
		t.Fatal(err)
	}
}

// Google's consent script must be read: most of its words in order, within
// about the time reading it takes, in a recording that isn't the sample.
func TestConsentScriptMustBeRead(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Ada Lovelace", Self: true, Adult: true,
		Statement: "I, Ada Lovelace, agree to MetaRush copying my voice for my own videos."}, true); err != nil {
		t.Fatal(err)
	}
	var ada models.Person
	_ = e.a.DB.C(store.CPeople).FindOne(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "name": "Ada Lovelace"}).Decode(&ada)
	script := strings.Fields(strings.NewReplacer(".", "", ",", "").Replace(fakeai.Catalog().ConsentScripts["en-US"]))
	quick := func(i int) float64 { return float64(i) * 0.3 }
	check := func(name string, consent *models.Asset, consentFrom, consentTo *float64, sample *models.Asset, want bool) {
		t.Helper()
		in := aiwork.CopyIn{ProjectID: e.project, Model: "fake/fake-voice-copy", Person: "Ada Lovelace", Name: "Ada", Sample: sample.ID, ConsentClip: consent.ID,
			ConsentFrom: consentFrom, ConsentTo: consentTo, DryRun: true}
		if sample.ID == consent.ID {
			in.From, in.To = usd(0), usd(12)
		}
		out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if out.Estimate.Consent.OK != want {
			t.Errorf("%s: consent ok %v, want %v (%v)", name, out.Estimate.Consent.OK, want, out.Estimate.Problems)
		}
		for _, pr := range out.Estimate.Problems {
			// Who should be heard reading is the person, the script the provider's.
			if strings.Contains(pr, "consent script, in order") && !strings.Contains(pr, "doesn't have Ada Lovelace reading Fake AI's consent script") {
				t.Errorf("%s: %q", name, pr)
			}
		}
	}
	sample := e.sound(t, "ada sample", 12)
	e.words(t, sample, ada, strings.Fields("hello there this is my voice"), quick)

	read := e.sound(t, "ada reads it", 8)
	e.words(t, read, ada, script, quick)
	check("the script read", read, nil, nil, sample, true)

	shuffled := e.sound(t, "ada shuffled", 9) // a length of its own: the same bytes would be the same clip
	mixed := append([]string{}, script...)
	for i, j := 0, len(mixed)-1; i < j; i, j = i+1, j-1 {
		mixed[i], mixed[j] = mixed[j], mixed[i]
	}
	e.words(t, shuffled, ada, mixed, quick)
	check("the script's words in another order", shuffled, nil, nil, sample, false)

	spread := e.sound(t, "ada rambles", 60)
	e.words(t, spread, ada, script, func(i int) float64 { return float64(i) * 4 })
	check("the script's words scattered over a minute", spread, nil, nil, sample, false)

	// One clip: the sample can't be the consent; a part the sample doesn't cover can.
	both := e.sound(t, "ada both", 20)
	e.words(t, both, ada, append(strings.Fields("hello there this is my voice"), script...), func(i int) float64 {
		if i < 6 {
			return float64(i) * 0.5
		}
		return 12 + float64(i-6)*0.3
	})
	check("the sample as its own consent", both, usd(0), usd(12), both, false)
	check("another part of the same clip", both, usd(12), usd(20), both, true)
}

// Generated video follows the Content Settings of every project whose clips
// it sends, too (D225): extending a clip of a project where nothing goes to an
// AI service, or starting a shot on one of its frames, is refused from any
// other project, and a video queued before the rule changed is stopped at $0
// before it is sent.
func TestVideoFollowsTheClipsProjects(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	gkey := "gk-" + strings.Repeat("Mn3", 10) + "-fake"
	gfake := fakevideogen.New(gkey)
	t.Cleanup(gfake.Close)
	t.Setenv(videogen.EnvGeminiBaseURL, gfake.URL)
	t.Setenv(providers.EnvVar(videogen.GeminiName), gkey)
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	other := ids.New(ids.Project)
	now := time.Now().UTC()
	if err := e.a.DB.Insert(ctx, store.CProjects, models.Project{ID: other, WorkspaceID: e.a.WorkspaceID(), Name: "Other", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "client-shot.mp4")
	if out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=24", "-f", "lavfi", "-i", "sine=frequency=300",
		"-t", "4", "-pix_fmt", "yuv420p", "-shortest", p).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %v %s", err, out)
	}
	res := ingest.File(ctx, e.a, p, ingest.Options{ProjectID: e.project, FootageOnly: true})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	// Queued from the other project while the client's project allowed it…
	ext := generate.GenExtendIn{ID: res.AssetID, ProjectID: other, Seconds: 4, Resolution: "720p", NoPeople: true, DryRun: true}
	est, err := call[generate.GenRunOut](t, e.a, "generate.extend", ext, false)
	if err != nil || !est.Estimate.Allowed {
		t.Fatalf("allowed at first: %+v %v", est, err)
	}
	ext.DryRun, ext.MaxUSD = false, usd(est.Estimate.USD)
	queued, err := call[generate.GenRunOut](t, e.a, "generate.extend", ext, false)
	if err != nil || len(queued.JobIDs) != 1 {
		t.Fatalf("queued %+v %v", queued, err)
	}
	none := true
	if _, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, None: &none}, true); err != nil {
		t.Fatal(err)
	}
	// …refused now from any project, priced or asked for.
	ext.DryRun, ext.MaxUSD = true, nil
	est, err = call[generate.GenRunOut](t, e.a, "generate.extend", ext, false)
	if err != nil || est.Estimate.Allowed || !strings.Contains(strings.Join(est.Estimate.Problems, " "), "nothing goes to an AI service") {
		t.Fatalf("extend into another project: %+v %v", est, err)
	}
	ext.DryRun, ext.MaxUSD = false, usd(5)
	if _, err := call[generate.GenRunOut](t, e.a, "generate.extend", ext, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("extend into another project, asked for: %v", err)
	}
	shot := generate.GenShotIn{ProjectID: other, Prompt: "the scene goes on", Seconds: 4, Resolution: "720p", NoPeople: true, From: &generate.FrameRef{AssetID: res.AssetID, At: 1}}
	vest, err := call[generate.GenEstimate](t, e.a, "generate.estimate", shot, false)
	if err != nil || vest.Allowed {
		t.Fatalf("a shot starting on the client's frame: %+v %v", vest, err)
	}
	shot.MaxUSD = usd(5)
	if _, err := call[generate.GenRunOut](t, e.a, "generate.shot", shot, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("a shot starting on the client's frame, asked for: %v", err)
	}
	// The queued one checks again before it sends: refused at $0.
	j, err := e.a.Queue.Get(ctx, queued.JobIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	_, _ = generate.Run(ctx, e.a, j, nil)
	g := e.gen(t, queued.Generations[0].ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || !strings.Contains(g.Outcome.Message, "Nothing was sent") || g.Settled == nil || g.Settled.USD != 0 {
		t.Fatalf("queued video: %+v settled %+v", g.Outcome, g.Settled)
	}
	if gfake.Calls("create") != 0 {
		t.Fatal("the client's footage was sent to the video model")
	}
}

// The deletion of a withdrawn voice copy can't be dropped: its job belongs to
// no project, so clearing the project out leaves it; a job that is gone
// (never queued, or stopped by someone) is queued again by the sweep; and it
// is tried until the voice is deleted.
func TestVoiceCopyDeletionIsNeverDropped(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in, consent := e.copyReady(t, "Mary Somerville")
	out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	key := os.Getenv(providers.EnvVar(fakeai.Name))
	t.Setenv(providers.EnvVar(fakeai.Name), "") // this computer has no key
	if _, err := call[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: consent.ID}, true); err != nil {
		t.Fatal(err)
	}
	g := e.gen(t, out.Work.ID)
	first, err := e.a.Queue.Get(ctx, g.Withdrawn.JobID)
	if err != nil || first.ProjectID != "" || first.Installation != "" {
		t.Fatalf("the deletion job is tied to a project or a computer: %+v %v", first, err)
	}
	// Clearing the project out doesn't call it off.
	if _, err := call[ops.ProjectArchiveOut](t, e.a, "project.delete", ops.ProjectDeleteIn{ID: e.project}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call[ops.ProjectPurgeOut](t, e.a, "project.purge", ops.ProjectPurgeIn{ID: e.project, Forever: true}, true); err != nil {
		t.Fatal(err)
	}
	if j, _ := e.a.Queue.Get(ctx, first.ID); j.State != models.JobQueued {
		t.Fatalf("clearing the project out stopped the deletion: %s", j.State)
	}
	// Stopped by someone: the sweep queues it again.
	if _, err := e.a.Queue.RequestCancel(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if n := aiwork.Sweep(ctx, e.a); n < 1 {
		t.Fatalf("the sweep didn't queue the deletion again (%d)", n)
	}
	var again []models.Job
	if err := e.a.DB.Find(ctx, store.CJobs, e.a.WorkspaceID(), bson.M{"payload.generationId": g.ID, "payload.action": aiwork.ActionRemoveVoice, "state": models.JobQueued}, &again); err != nil || len(again) != 1 {
		t.Fatalf("queued again: %d %v", len(again), err)
	}
	// Never queued at all (the queue didn't answer): the sweep queues it too.
	if _, err := e.a.DB.C(store.CJobs).DeleteMany(ctx, bson.M{"payload.generationId": g.ID}); err != nil {
		t.Fatal(err)
	}
	if n := aiwork.Sweep(ctx, e.a); n < 1 {
		t.Fatal("the sweep didn't queue a deletion that was never queued")
	}
	if n := aiwork.Sweep(ctx, e.a); n != 0 {
		t.Fatalf("a deletion already waiting was queued twice (%d)", n)
	}
	_ = e.a.DB.Find(ctx, store.CJobs, e.a.WorkspaceID(), bson.M{"payload.generationId": g.ID, "state": models.JobQueued}, &again)
	if len(again) != 1 {
		t.Fatalf("waiting deletions: %d", len(again))
	}
	t.Setenv(providers.EnvVar(fakeai.Name), key) // a computer with the key takes it
	if _, err := e.run(t, again[0].ID); err != nil {
		t.Fatal(err)
	}
	if g = e.gen(t, out.Work.ID); g.Removed == nil || len(e.fake.Voices()) != 0 {
		t.Fatalf("removed %+v voices %v", g.Removed, e.fake.Voices())
	}
	if n := aiwork.Sweep(ctx, e.a); n != 0 {
		t.Fatalf("a deleted voice was queued again (%d)", n)
	}
}

// runHeld leases a job like a worker, runs it while the fake holds the
// request in flight, does during() meanwhile (with the job's own stop, as the
// worker's heartbeat would), then lets the request finish.
func (e *env) runHeld(t *testing.T, jobID string, during func(stop func())) {
	t.Helper()
	ctx := context.Background()
	j, err := e.a.Queue.LeaseByID(ctx, jobs.Worker{ID: e.a.Cfg.Installation.ID, Name: "test", JobTypes: []string{jobs.TypeGenerate}, AcceptsHeavy: true}, jobID)
	if err != nil || j == nil || j.ID != jobID {
		t.Fatalf("lease %+v %v", j, err)
	}
	e.fake.Script(fakeai.Behavior{Kind: fakeai.Hold})
	before := e.fake.Calls("submit")
	jctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = aiwork.Run(jctx, e.a, j, nil)
	}()
	for i := 0; e.fake.Calls("submit") == before; i++ {
		if i > 1000 {
			t.Fatal("the request was never sent")
		}
		time.Sleep(5 * time.Millisecond)
	}
	during(stop)
	e.fake.Release()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the job never ended")
	}
}

// A voice copy being sent when its consent is withdrawn is never cut off
// (its answer would be lost and the voice left at the provider): it finishes,
// then is withdrawn and deleted there.
func TestVoiceCopyBeingSentIsNeverCutOff(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in, consent := e.copyReady(t, "Rosalind Franklin")
	out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil {
		t.Fatal(err)
	}
	e.runHeld(t, out.JobID, func(stop func()) {
		rev, err := call[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: consent.ID}, true)
		if err != nil {
			t.Fatal(err)
		}
		if j, _ := e.a.Queue.Get(ctx, out.JobID); j.CancelRequested {
			t.Error("withdrawing the consent cut off a voice copy being sent")
		}
		if !strings.Contains(strings.Join(rev.AlsoWithdrawn, " "), "as soon as it is done") {
			t.Errorf("the withdrawal didn't say what happens to the copy being made: %v", rev.AlsoWithdrawn)
		}
		stop() // a stop meanwhile (a person, this computer) doesn't lose the answer either
	})
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenDone || g.Withdrawn == nil || g.Removed == nil || len(e.fake.Voices()) != 0 {
		t.Fatalf("outcome %+v withdrawn %+v removed %+v voices %v", g.Outcome, g.Withdrawn, g.Removed, e.fake.Voices())
	}
}

// A voice copy whose answer was lost may have been made all the same: it is
// found at the provider by the tag in its name and deleted; when none turns
// up, the record says none was made.
func TestLostVoiceCopyIsFoundAndDeleted(t *testing.T) {
	e := setup(t)
	in, _ := e.copyReady(t, "Lise Meitner")
	e.fake.Script(fakeai.Behavior{Kind: fakeai.Lost})
	out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = e.run(t, out.JobID)
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || !strings.Contains(g.Outcome.Message, "looks for the voice") {
		t.Fatalf("outcome %+v", g.Outcome)
	}
	if g.Withdrawn == nil || g.Removed == nil || !strings.Contains(g.Removed.Detail, "1 voice was found") || len(e.fake.Voices()) != 0 || e.fake.Calls("submit") != 1 {
		t.Fatalf("withdrawn %+v removed %+v voices %v submits %d", g.Withdrawn, g.Removed, e.fake.Voices(), e.fake.Calls("submit"))
	}
}

// A voice copy whose send was cut off (MetaRush stopped while sending) is
// never sent again; it is looked for at the provider until the time is up,
// and when the provider never made it the record says so.
func TestVoiceCopyCutOffWhileSendingIsLookedFor(t *testing.T) {
	e := setup(t)
	in2, _ := e.copyReady(t, "Chien-Shiung Wu")
	out2, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in2, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := generate.AppendTo(context.Background(), e.a, out2.Work.ID, "sending", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, _ = e.run(t, out2.JobID)
	g := e.gen(t, out2.Work.ID)
	if g.Withdrawn == nil || g.Removed != nil || g.Withdrawn.JobID == "" {
		t.Fatalf("looked for once, and again later: withdrawn %+v removed %+v", g.Withdrawn, g.Removed)
	}
	var wait *generate.Wait
	if _, err := e.run(t, g.Withdrawn.JobID); !errors.As(err, &wait) {
		t.Fatalf("not found yet: looked for again later, got %v", err)
	}
	old := aiwork.LostVoiceLookFor
	aiwork.LostVoiceLookFor = 0
	t.Cleanup(func() { aiwork.LostVoiceLookFor = old })
	if _, err := e.run(t, g.Withdrawn.JobID); err != nil {
		t.Fatal(err)
	}
	if g = e.gen(t, out2.Work.ID); g.Removed == nil || !strings.Contains(g.Removed.Detail, "none was made") {
		t.Fatalf("removed %+v", g.Removed)
	}
	if e.fake.Calls("submit") != 0 {
		t.Fatal("a voice copy cut off while it was being sent was sent again")
	}
}

// Other AI work relying on a consent withdrawn while it is being sent isn't
// cut off either, and what comes back isn't kept.
func TestWorkBeingSentWhenConsentWithdrawnIsNotKept(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	rec, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Maya Lindqvist", Self: true, Adult: true,
		Statement: "I, Maya Lindqvist, agree to MetaRush making pictures of me for my own videos."}, true)
	if err != nil {
		t.Fatal(err)
	}
	in := aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "Maya waving at the camera"}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || !est.Allowed {
		t.Fatalf("with consent %+v %v", est, err)
	}
	in.MaxUSD = usd(est.USD)
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	e.runHeld(t, out.JobID, func(stop func()) {
		if _, err := call[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: rec.Consent.Consent.ID}, true); err != nil {
			t.Fatal(err)
		}
		if j, _ := e.a.Queue.Get(ctx, out.JobID); j.CancelRequested {
			t.Error("withdrawing the consent cut off a request being sent")
		}
	})
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || g.Outcome.Code != apperr.ConsentRequired || !strings.Contains(g.Outcome.Message, "wasn't kept") {
		t.Fatalf("outcome %+v", g.Outcome)
	}
	if len(g.Outcome.Outputs) != 0 || g.Forgotten == nil {
		t.Fatalf("kept %+v forgotten %+v", g.Outcome.Outputs, g.Forgotten)
	}
}

// MetaRush stopped after saving a voice copy's ticket and before writing its
// outcome: the one-shot answer (the voice's ID) is gone and the provider has
// nothing to ask about, but the voice may be there. On resume it is found by
// the tag in its name and deleted, never left behind.
func TestVoiceCopyStoppedAfterItsTicketIsFoundAndDeleted(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in, _ := e.copyReady(t, "Dorothy Hodgkin")
	out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil {
		t.Fatal(err)
	}
	g := e.gen(t, out.Work.ID)
	// What the job had done when it stopped: sent it (the provider made the
	// voice, named with MetaRush's tag), saved the ticket, nothing more.
	ad, err := aiservices.Get(fakeai.Name, aiservices.KindVoiceCopy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ad.Submit(ctx, e.fake.Key, &aiservices.Request{Kind: aiservices.KindVoiceCopy, Model: "fake-voice-copy", Options: *g.Options, Tag: aiwork.VoiceTag(&g)}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := generate.AppendTo(ctx, e.a, g.ID, "sending", now); err != nil {
		t.Fatal(err)
	}
	if err := generate.AppendTo(ctx, e.a, g.ID, "sends", models.GenSend{At: now, Result: "accepted"}); err != nil {
		t.Fatal(err)
	}
	if err := generate.SetOnce(ctx, e.a, g.ID, "ticket", models.GenTicket{SubmittedAt: now}); err != nil {
		t.Fatal(err)
	}
	if len(e.fake.Voices()) != 1 {
		t.Fatalf("set-up: voices %v", e.fake.Voices())
	}
	_, _ = e.run(t, out.JobID)
	g = e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || !strings.Contains(g.Outcome.Message, "looks for the voice") {
		t.Fatalf("outcome %+v", g.Outcome)
	}
	if g.Withdrawn == nil || g.Removed == nil || !strings.Contains(g.Removed.Detail, "1 voice was found") || len(e.fake.Voices()) != 0 {
		t.Fatalf("withdrawn %+v removed %+v voices %v", g.Withdrawn, g.Removed, e.fake.Voices())
	}
	if e.fake.Calls("submit") != 1 {
		t.Fatal("a voice copy whose ticket was saved was sent again")
	}
}
