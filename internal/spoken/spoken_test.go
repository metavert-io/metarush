package spoken_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/spoken"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeai"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/voice"
)

func fakeKey() string { return "fk-" + strings.Repeat("Qz7", 10) + "-fake" }

func usd(v float64) *float64 { return &v }

type env struct {
	a       *app.App
	project string
	clip    *models.Asset
	person  models.Person
	consent models.Consent
	copyGen string
	head    string
	heard   *hearing
}

// hearing stands in for the recognizer on this computer: the speech made is
// heard as the words asked for, spread over it; the fitted sound as the new
// words where they were put (or whatever a test says).
type hearing struct {
	mu      sync.Mutex
	context string
	words   string
	fitted  func(dur float64) []models.Word // nil: the new words, in the middle
	calls   int
	onHear  func() // runs once, as the speech made is first heard (a race in a test)
}

func wavSeconds(t *testing.T, path string) float64 {
	pcm, err := voice.ReadWAV(path)
	if err != nil {
		t.Fatal(err)
	}
	return float64(len(pcm)) / voice.SampleRate
}

func spread(text string, from, to float64) []models.Word {
	ws := strings.Fields(text)
	step := (to - from) / float64(len(ws))
	var out []models.Word
	for i, w := range ws {
		out = append(out, models.Word{Text: w, From: from + float64(i)*step, To: from + float64(i+1)*step - 0.02})
	}
	return out
}

func setup(t *testing.T) *env {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	keyring.MockInit()
	generate.PollEvery, generate.FirstPoll = 20*time.Millisecond, 2*time.Millisecond
	key := fakeKey()
	fakeai.Install(t, key)
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
	e := &env{a: a, project: pid, heard: &hearing{}}
	// The checks on this computer: a stand-in recognizer and fingerprints.
	oldCheck, oldHear, oldEmbed := spoken.CanCheck, spoken.Hear, spoken.Embed
	t.Cleanup(func() { spoken.CanCheck, spoken.Hear, spoken.Embed = oldCheck, oldHear, oldEmbed })
	spoken.CanCheck = func(*app.App) error { return nil }
	spoken.Hear = func(ctx context.Context, a *app.App, wav, scratch, lang string) ([]models.Word, error) {
		h := e.heard
		h.mu.Lock()
		defer h.mu.Unlock()
		h.calls++
		if h.onHear != nil {
			fn := h.onHear
			h.onHear = nil
			h.mu.Unlock()
			fn()
			h.mu.Lock()
		}
		d := wavSeconds(t, wav)
		if h.calls%2 == 1 { // the speech made: the whole context
			return spread(h.context, 0.1, d-0.1), nil
		}
		if h.fitted != nil {
			return h.fitted(d), nil
		}
		return spread(h.words, d/2-0.3, d/2+0.3), nil
	}
	spoken.Embed = func(ctx context.Context, a *app.App, pcm []float32) ([]float32, error) { return adaVoice(), nil }
	return e
}

func adaVoice() []float32 {
	v := make([]float32, 192)
	v[0], v[1] = 0.8, 0.6
	return v
}

func otherVoice() []float32 {
	v := make([]float32, 192)
	v[2] = 1
	return v
}

func call[O any](t *testing.T, a *app.App, name string, in any, human bool) (*O, error) {
	t.Helper()
	return ops.CallTyped[O](context.Background(), a, name, in, ops.Caller{Human: human})
}

func mustCall[O any](t *testing.T, a *app.App, name string, in any, human bool) *O {
	t.Helper()
	o, err := call[O](t, a, name, in, human)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func codeOf(err error) string {
	if err == nil {
		return ""
	}
	return apperr.From(err).Code
}

// clipWith adds an 8-second clip in which Ada says "we planted twelve rows of
// beans" from 1 s (her voice named by a person), with a quiet pause after.
func (e *env) clipWith(t *testing.T, source, namedBy string) {
	t.Helper()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "garden.mp4")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=24:duration=8",
		"-f", "lavfi", "-i", "sine=frequency=220:duration=8", "-shortest", "-pix_fmt", "yuv420p", "-c:a", "aac", p).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	res := ingest.File(ctx, e.a, p, ingest.Options{ProjectID: e.project, FootageOnly: true})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	var as models.Asset
	if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), res.AssetID, &as); err != nil {
		t.Fatal(err)
	}
	e.clip = &as
	if err := e.a.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: e.a.WorkspaceID(), AssetID: as.ID, Label: "speaker_00",
		Name: e.person.Name, PersonID: e.person.ID, Source: source, Seconds: 5, Centroid: adaVoice(), Naming: naming(namedBy)}); err != nil {
		t.Fatal(err)
	}
	text := "we planted twelve rows of beans"
	var words []models.Word
	for i, w := range strings.Fields(text) {
		words = append(words, models.Word{Text: w, From: 1 + float64(i)*0.45, To: 1 + float64(i)*0.45 + 0.38})
	}
	if err := e.a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: e.a.WorkspaceID(), AssetID: as.ID, From: 1, To: 3.7,
		Text: text, Speaker: "speaker_00", SpeakerName: e.person.Name, Words: words, Provider: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := e.a.DB.Update(ctx, store.CAssets, e.a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"transcript": models.TranscriptStatus{State: "available", Precision: "word"}}}); err != nil {
		t.Fatal(err)
	}
}

// voiceCopy records Ada's consent (covering new words with the fake service
// when uses says so) and makes her voice copy.
func (e *env) voiceCopy(t *testing.T, uses []string) {
	t.Helper()
	ctx := context.Background()
	in := generate.ConsentRecordIn{Person: "Ada Lovelace", Self: true, Adult: true, Statement: "I, Ada Lovelace, agree to MetaRush copying my voice for my own videos."}
	if len(uses) > 0 {
		// Named uses replace the defaults (D274): the voice copy needs voice.
		in.Uses, in.Providers = append([]string{models.ConsentVoice}, uses...), []string{fakeai.Name}
	}
	out := mustCall[generate.ConsentOut](t, e.a, "consent.record", in, true)
	e.consent = out.Consent.Consent
	_ = e.a.DB.C(store.CPeople).FindOne(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "name": "Ada Lovelace"}).Decode(&e.person)
	// A sample and Google-style consent reading for the fake service's script.
	sample := e.tone(t, "sample", 12, "hello there")
	reading := e.tone(t, "reading", 6, fakeai.Catalog().ConsentScripts["en-US"])
	cin := aiwork.CopyIn{ProjectID: e.project, Model: "fake/fake-voice-copy", Person: "Ada Lovelace", Name: "Ada", Sample: sample, ConsentClip: reading, DryRun: true}
	est := mustCall[aiwork.MakeOut](t, e.a, "ai.voices.copy", cin, true)
	if !est.Estimate.Allowed {
		t.Fatalf("voice copy %+v", est.Estimate.Problems)
	}
	cin.DryRun, cin.MaxUSD = false, usd(est.Estimate.USD)
	mo := mustCall[aiwork.MakeOut](t, e.a, "ai.voices.copy", cin, true)
	e.runJob(t, mo.JobID)
	e.copyGen = mo.Work.ID
}

func (e *env) tone(t *testing.T, name string, secs float64, text string) string {
	t.Helper()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), name+".wav")
	if err := os.WriteFile(p, aiservices.ToneWAV(secs, 300), 0o600); err != nil {
		t.Fatal(err)
	}
	res := ingest.File(ctx, e.a, p, ingest.Options{ProjectID: e.project, FootageOnly: true})
	_ = e.a.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: e.a.WorkspaceID(), AssetID: res.AssetID, Label: "speaker_00",
		Name: e.person.Name, PersonID: e.person.ID, Source: "person", Seconds: 5, Naming: naming(models.NamedByPerson)})
	var words []models.Word
	for i, w := range strings.Fields(text) {
		words = append(words, models.Word{Text: w, From: float64(i) * 0.3, To: float64(i)*0.3 + 0.25})
	}
	_ = e.a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: e.a.WorkspaceID(), AssetID: res.AssetID, From: 0, To: 5,
		Text: text, Speaker: "speaker_00", Words: words, Provider: "test"})
	return res.AssetID
}

func (e *env) runJob(t *testing.T, jobID string) {
	t.Helper()
	j, err := e.a.Queue.Get(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := aiwork.Run(context.Background(), e.a, j, nil); err != nil {
		t.Fatalf("job: %v", err)
	}
}

// cut saves the project's first cut, playing the whole clip.
func (e *env) cut(t *testing.T) {
	t.Helper()
	plan := fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":0,"out":8}]}}`, e.project, e.clip.ID)
	chk := mustCall[ops.EditCheckOut](t, e.a, "edit.check", ops.EditCheckIn{Plan: json.RawMessage(plan)}, false)
	out := mustCall[ops.EditApplyOut](t, e.a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken}, true)
	e.head = out.Revision.ID
}

func (e *env) ready(t *testing.T, uses []string) {
	t.Helper()
	e.voiceCopy(t, uses)
	e.clipWith(t, models.SpeakerByPerson, models.NamedByPerson)
	e.cut(t)
}

func twelve(e *env) spoken.SayIn {
	from, to := 1.9, 2.28 // "twelve"
	return spoken.SayIn{ProjectID: e.project, AssetID: e.clip.ID, From: &from, To: &to, Words: "fifteen"}
}

// A word Ada said becomes new words in her own voice copy: priced first,
// people only, checked by the recognizer and her voice's fingerprint, fitted
// into the stretch over the room's quiet, put in the cut as a new version
// through the edit check, and taken out again with undo.
func TestChangeASpokenWord(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	ctx := context.Background()
	in := twelve(e)
	est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", in, false)
	if !est.Allowed || est.Before != "twelve" || est.Speaker == nil || est.Speaker.Name != "Ada Lovelace" || est.Voice == nil || est.ConsentID != e.consent.ID ||
		est.Branch != "main" || est.Context != "we planted fifteen rows of beans" || est.USD <= 0 || len(est.Cost.Parts) == 0 {
		t.Fatalf("estimate %+v", est)
	}
	if !strings.HasPrefix(est.Model, fakeai.Name+"/") {
		t.Fatalf("the speech must be made where the voice copy is kept: %s", est.Model)
	}
	if p := plainwords.Check(est.Summary); len(p) > 0 {
		t.Fatalf("summary not plain: %q %v", est.Summary, p)
	}
	// People only, and priced first.
	if _, err := call[spoken.SayMakeOut](t, e.a, "cut.say.make", in, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("an AI or a script must not change someone's words: %v", err)
	}
	if _, err := call[spoken.SayMakeOut](t, e.a, "cut.say.make", in, true); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("without the agreed price nothing is sent: %v", err)
	}
	var n int64
	n, _ = e.a.DB.C(store.CSpokenChanges).CountDocuments(ctx, bson.M{})
	if n != 0 {
		t.Fatal("a refused change left a record")
	}
	in.MaxUSD = usd(est.USD)
	e.heard.context, e.heard.words = est.Context, "fifteen"
	mo := mustCall[spoken.SayMakeOut](t, e.a, "cut.say.make", in, true)
	if mo.Change.State != models.SpokenMaking || mo.JobID == "" {
		t.Fatalf("make %+v", mo)
	}
	e.runJob(t, mo.JobID)
	v := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: mo.Change.ID}, false)
	if v.State != models.SpokenReady || v.Sound == "" || v.Fit == nil || v.Check == nil || !v.Check.Passed || !v.Check.WordsMatch || !v.Check.VoiceMatch {
		t.Fatalf("after the job: %+v %+v %+v", v, v.Fit, v.Check)
	}
	f := v.Fit
	if f.From >= 1.9 || f.To <= 2.28 || f.WordsFrom < f.From || f.WordsTo > f.To || f.Speed < 1 || f.Speed > 1.25 {
		t.Fatalf("fit %+v", f)
	}
	var sound models.Asset
	if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), v.Sound, &sound); err != nil {
		t.Fatal(err)
	}
	if !sound.Source.IsGenerated() || sound.Source.Generated.Said == nil || sound.Source.Generated.Said.Before != "twelve" || !sound.Source.Generated.Likeness ||
		!sound.HasTag(models.TagAIGenerated) || math.Abs(sound.Media.Duration-(f.To-f.From)) > 0.03 {
		t.Fatalf("the new sound: %+v %+v (%.3f s for %.3f)", sound.Source, sound.Tags, sound.Media.Duration, f.To-f.From)
	}
	if v.InCut {
		t.Fatal("nothing is in the cut before a person puts it there")
	}
	// Placing: people only; checked with a confirm token like every edit.
	place := ops.CutSayPlaceIn{ID: v.ID, NoRender: true}
	if _, err := call[ops.CutSayPlaceOut](t, e.a, "cut.say.place", place, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("placing is a person's step: %v", err)
	}
	dry := place
	dry.DryRun = true
	chk := mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.place", dry, true)
	if chk.Check == nil || chk.Check.ConfirmToken == "" || len(chk.Changes) == 0 || !strings.Contains(strings.Join(chk.Changes, " "), "“fifteen” in place of “twelve”") {
		t.Fatalf("the check says what changes: %+v", chk.Changes)
	}
	place.ConfirmToken = chk.Check.ConfirmToken
	po := mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.place", place, true)
	if !po.Saved || po.Applied == nil {
		t.Fatalf("placed %+v", po)
	}
	rev := po.Applied.Revision
	spec := mustCall[ops.RevisionShowOut](t, e.a, "revision.show", ops.RevisionShowIn{ID: rev.ID}, false).Spec
	if spec.Mix == nil || len(spec.Mix.Said) != 1 || spec.Mix.Said[0].Sound != v.Sound || spec.Mix.Said[0].Before != "twelve" {
		t.Fatalf("the version's mix: %+v", spec.Mix)
	}
	plan := mustCall[editspec.RenderPlan](t, e.a, "revision.plan", ops.RevisionPlanIn{ID: rev.ID}, false)
	found := false
	for _, pa := range plan.Audio {
		found = found || (pa.AssetID == v.Sound && pa.AIVoice)
	}
	if !found {
		t.Fatalf("the plan doesn't play the new words: %+v", plan.Audio)
	}
	v = mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: v.ID}, false)
	if v.State != models.SpokenPlaced || !v.InCut || len(v.Placed) != 1 || v.Placed[0].RevisionID != rev.ID {
		t.Fatalf("after placing: %+v", v)
	}
	// Undo: a new version plays what was said; the change is kept.
	undo := ops.CutSayPlaceIn{ID: v.ID, NoRender: true, DryRun: true}
	uc := mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", undo, false)
	undo.DryRun, undo.ConfirmToken = false, uc.Check.ConfirmToken
	uo := mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", undo, false)
	if !uo.Saved {
		t.Fatalf("undo %+v", uo)
	}
	if us := mustCall[ops.RevisionShowOut](t, e.a, "revision.show", ops.RevisionShowIn{ID: uo.Applied.Revision.ID}, false).Spec; us.Mix != nil && len(us.Mix.Said) > 0 {
		t.Fatalf("undo left %+v", us.Mix)
	}
	list := mustCall[ops.CutSayListOut](t, e.a, "cut.say.list", ops.CutSayListIn{ProjectID: e.project}, false)
	if len(list.Changes) != 1 || list.Changes[0].State != models.SpokenUndone || list.Changes[0].InCut {
		t.Fatalf("list %+v", list)
	}
	// Withdrawing the consent: it can't go back in.
	rv := mustCall[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: e.consent.ID}, true)
	if !strings.Contains(strings.Join(rv.AlsoWithdrawn, " "), "1 change of words") {
		t.Fatalf("withdrawing the consent says what it stops: %+v", rv.AlsoWithdrawn)
	}
	if w := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: v.ID}, false); w.State != models.SpokenWithdrawn {
		t.Fatalf("the change relied on the withdrawn consent: %+v", w.State)
	}
	again := ops.CutSayPlaceIn{ID: v.ID, NoRender: true, DryRun: true}
	if _, err := call[ops.CutSayPlaceOut](t, e.a, "cut.say.place", again, true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("withdrawn consent must stop it: %v", err)
	}
}

// What is refused, and said plainly: a voice MetaRush only recognized, no
// consent naming new words, a child or a public figure, another person's
// voice in the new words, new words that weren't heard.
func TestChangeASpokenWordRefusals(t *testing.T) {
	t.Run("recognized only", func(t *testing.T) {
		e := setup(t)
		e.voiceCopy(t, []string{models.ConsentUseNewWords})
		e.clipWith(t, models.SpeakerByAuto, "")
		e.cut(t)
		est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", twelve(e), false)
		if est.Allowed || est.Code != "speaker" {
			t.Fatalf("a voice nobody confirmed: %+v", est)
		}
	})
	t.Run("named by an AI app", func(t *testing.T) {
		e := setup(t)
		e.voiceCopy(t, []string{models.ConsentUseNewWords})
		e.clipWith(t, models.SpeakerByPerson, models.NamedByAgent)
		e.cut(t)
		est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", twelve(e), false)
		if est.Allowed || est.Code != "speaker" || !strings.Contains(strings.Join(est.Problems, " "), "not by a person") {
			t.Fatalf("only a name a person gave counts: %+v", est)
		}
	})
	t.Run("no consent for new words", func(t *testing.T) {
		e := setup(t)
		e.ready(t, nil)
		est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", twelve(e), false)
		if est.Allowed || est.Code != "consent" {
			t.Fatalf("consent that doesn't name new words: %+v", est)
		}
		in := twelve(e)
		in.MaxUSD = usd(1)
		if _, err := call[spoken.SayMakeOut](t, e.a, "cut.say.make", in, true); codeOf(err) != apperr.ConsentRequired {
			t.Fatalf("make: %v", err)
		}
	})
	t.Run("public figure", func(t *testing.T) {
		e := setup(t)
		e.ready(t, []string{models.ConsentUseNewWords})
		_ = e.a.DB.Update(context.Background(), store.CConsents, e.a.WorkspaceID(), e.consent.ID, bson.M{"$set": bson.M{"publicFigure": true}})
		est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", twelve(e), false)
		if est.Allowed || est.Code != "consent" || !strings.Contains(strings.Join(est.Problems, " "), "public figure") {
			t.Fatalf("a public figure: %+v", est)
		}
	})
	t.Run("not in a cut", func(t *testing.T) {
		e := setup(t)
		e.voiceCopy(t, []string{models.ConsentUseNewWords})
		e.clipWith(t, models.SpeakerByPerson, models.NamedByPerson)
		est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", twelve(e), false)
		if est.Allowed || est.Code != "cut" {
			t.Fatalf("no cut plays it: %+v", est)
		}
	})
	t.Run("another voice", func(t *testing.T) {
		e := setup(t)
		e.ready(t, []string{models.ConsentUseNewWords})
		spoken.Embed = func(context.Context, *app.App, []float32) ([]float32, error) { return otherVoice(), nil }
		c := e.makeIt(t)
		if c.State != models.SpokenFailed || c.Check == nil || c.Check.VoiceMatch || c.Sound != "" || !strings.Contains(c.Why, "sound enough like") {
			t.Fatalf("another voice must fail the check: %+v %+v", c, c.Check)
		}
	})
	t.Run("other words heard", func(t *testing.T) {
		e := setup(t)
		e.ready(t, []string{models.ConsentUseNewWords})
		e.heard.fitted = func(d float64) []models.Word { return spread("fifty", d/2-0.2, d/2+0.2) }
		c := e.makeIt(t)
		if c.State != models.SpokenFailed || c.Check == nil || c.Check.WordsMatch || !strings.Contains(c.Why, "“fifty”") {
			t.Fatalf("other words must fail the check: %+v %+v", c, c.Check)
		}
	})
}

func (e *env) makeIt(t *testing.T) ops.SpokenView {
	t.Helper()
	in := twelve(e)
	est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", in, false)
	if !est.Allowed {
		t.Fatalf("estimate %+v", est.Problems)
	}
	in.MaxUSD = usd(est.USD)
	e.heard.context, e.heard.words = est.Context, "fifteen"
	mo := mustCall[spoken.SayMakeOut](t, e.a, "cut.say.make", in, true)
	e.runJob(t, mo.JobID)
	return *mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: mo.Change.ID}, false)
}

// New words are matched however the recognizer writes numbers.
func TestSameWordsReadNumbers(t *testing.T) {
	for _, c := range []struct {
		want, heard string
		ok          bool
	}{{"fifteen", "15", true}, {"Fifteen!", "fifteen.", true}, {"one hundred and five", "105", true}, {"fifteen", "fifty", false}, {"two rows", "two", false}} {
		if got := spoken.SameWords(c.want, c.heard); got != c.ok {
			t.Errorf("%q vs %q = %v", c.want, c.heard, got)
		}
	}
}

// The mouth matches only when a person asks: footage nobody looked at is
// refused plainly; a matched stretch plays in place of the recording's
// picture when the change goes in (the clip split around it), and undo plays
// the recording again.
func TestTheMouthMatches(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords, models.ConsentUseLipSync})
	ctx := context.Background()
	v := e.makeIt(t)
	if v.State != models.SpokenReady {
		t.Fatalf("not ready: %+v", v)
	}
	if _, err := call[spoken.MouthOut](t, e.a, "cut.say.mouth", spoken.MouthIn{ID: v.ID, MaxUSD: usd(1)}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("matching the mouth is a person's step: %v", err)
	}
	dry := mustCall[spoken.MouthOut](t, e.a, "cut.say.mouth", spoken.MouthIn{ID: v.ID, DryRun: true}, true)
	if dry.Estimate == nil || dry.Estimate.Allowed || len(dry.Estimate.Problems) == 0 {
		t.Fatalf("footage nobody looked at can't have its mouth matched: %+v", dry.Estimate)
	}
	// A matched stretch (as the lip sync would bring back), the length of the new sound.
	secs := v.Fit.To - v.Fit.From
	p := filepath.Join(t.TempDir(), "mouth.mp4")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=160x90:rate=24:duration=%.3f", secs),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=330:duration=%.3f", secs), "-shortest", "-pix_fmt", "yuv420p", "-c:a", "aac", p).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	res := ingest.File(ctx, e.a, p, ingest.Options{ProjectID: e.project, FootageOnly: true})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	_ = e.a.DB.Update(ctx, store.CSpokenChanges, e.a.WorkspaceID(), v.ID, bson.M{"$set": bson.M{"mouth": models.SpokenMouth{AssetID: res.AssetID, State: models.SpokenReady}}})
	place := ops.CutSayPlaceIn{ID: v.ID, NoRender: true, DryRun: true}
	chk := mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.place", place, true)
	place.DryRun, place.ConfirmToken = false, chk.Check.ConfirmToken
	po := mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.place", place, true)
	spec := mustCall[ops.RevisionShowOut](t, e.a, "revision.show", ops.RevisionShowIn{ID: po.Applied.Revision.ID}, false).Spec
	if len(spec.Clips) != 3 || spec.Clips[1].AssetID != res.AssetID || spec.Clips[1].In != 0 || spec.Mix.Said[0].Picture != res.AssetID ||
		spec.Clips[0].Out != v.Fit.From || spec.Clips[2].In != v.Fit.To {
		t.Fatalf("the matched stretch plays in place of the picture: %+v", spec.Clips)
	}
	plan := mustCall[editspec.RenderPlan](t, e.a, "revision.plan", ops.RevisionPlanIn{ID: po.Applied.Revision.ID}, false)
	said := false
	for _, pa := range plan.Audio {
		said = said || (pa.AssetID == v.Sound && pa.AIVoice)
	}
	if !said {
		t.Fatalf("the new words play over the matched stretch too: %+v", plan.Audio)
	}
	undo := ops.CutSayPlaceIn{ID: v.ID, NoRender: true, DryRun: true}
	uc := mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", undo, false)
	undo.DryRun, undo.ConfirmToken = false, uc.Check.ConfirmToken
	uo := mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", undo, false)
	back := mustCall[ops.RevisionShowOut](t, e.a, "revision.show", ops.RevisionShowIn{ID: uo.Applied.Revision.ID}, false).Spec
	for _, c := range back.Clips {
		if c.AssetID != e.clip.ID {
			t.Fatalf("undo leaves the matched stretch in: %+v", back.Clips)
		}
	}
	if back.Clips[1].In != v.Fit.From || back.Clips[1].Out != v.Fit.To {
		t.Fatalf("undo plays the recording's own stretch again: %+v", back.Clips[1])
	}
}

func naming(by string) *models.Naming {
	if by == "" {
		return nil
	}
	return &models.Naming{By: by, At: time.Now().UTC()}
}

// Making that stops without a word — its job failed or is gone — fails the
// change with why, so the person can try again (AI work's sweep runs it).
func TestLostMakingFailsSoThePersonCanTryAgain(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	ctx := context.Background()
	ask := func() spoken.SayMakeOut {
		in := twelve(e)
		est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", in, false)
		in.MaxUSD = usd(est.USD)
		return *mustCall[spoken.SayMakeOut](t, e.a, "cut.say.make", in, true)
	}
	old := time.Now().Add(-10 * time.Minute)
	// 1. Its job failed before anything came back: AI work's sweep closes the speech and the change hears it.
	first := ask()
	_ = e.a.DB.Update(ctx, store.CJobs, e.a.WorkspaceID(), first.JobID, bson.M{"$set": bson.M{"state": models.JobFailed, "error": "the computer went to sleep"}})
	_ = e.a.DB.Update(ctx, store.CGenerations, e.a.WorkspaceID(), first.Change.GenerationID, bson.M{"$set": bson.M{"createdAt": old}})
	aiwork.Sweep(ctx, e.a)
	v := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: first.Change.ID}, false)
	if v.State != models.SpokenFailed || v.Next == "" {
		t.Fatalf("a change whose making failed must say so: %+v (%s)", v.State, v.Next)
	}
	// 2. Its job is gone altogether: the change's own sweep fails it.
	second := ask()
	_, _ = e.a.DB.C(store.CJobs).DeleteOne(ctx, bson.M{"_id": second.JobID})
	_, _ = e.a.DB.C(store.CGenerations).DeleteOne(ctx, bson.M{"_id": second.Change.GenerationID})
	_ = e.a.DB.Update(ctx, store.CSpokenChanges, e.a.WorkspaceID(), second.Change.ID, bson.M{"$set": bson.M{"updatedAt": old}})
	if n := spoken.Sweep(ctx, e.a); n != 1 {
		t.Fatalf("the sweep failed %d changes, want 1", n)
	}
	v = mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: second.Change.ID}, false)
	if v.State != models.SpokenFailed || !strings.Contains(v.Why, "try again") {
		t.Fatalf("a change whose making is gone must fail: %+v (%s)", v.State, v.Why)
	}
	// 3. The person tries again: it is made and checked.
	e.heard.context, e.heard.words = mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", twelve(e), false).Context, "fifteen"
	again := ask()
	e.runJob(t, again.JobID)
	if v := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: again.Change.ID}, false); v.State != models.SpokenReady {
		t.Fatalf("trying again: %+v %s", v.State, v.Why)
	}
}
