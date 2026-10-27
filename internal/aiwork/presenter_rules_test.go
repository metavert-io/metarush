package aiwork_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeheygen"
)

// What a presenter needs is checked again just before anything is sent and
// when the clip comes back (D273–D277).

// ask prices and agrees to a piece of work, leaving its job queued.
func (e *env) ask(t *testing.T, in aiwork.MakeIn) *aiwork.MakeOut {
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
	return out
}

// askTest asks for a test clip of a presenter, leaving its job queued.
func (e *env) askTest(t *testing.T, pr models.Presenter, clip string) *aiwork.MakeOut {
	t.Helper()
	dry, err := call[aiwork.MakeOut](t, e.a, "presenter.test", aiwork.PresenterTestIn{ID: pr.Name, Clip: clip, ProjectID: e.project, DryRun: true}, true)
	if err != nil || !dry.Estimate.Allowed {
		t.Fatalf("test estimate: %+v %v", dry, err)
	}
	out, err := call[aiwork.MakeOut](t, e.a, "presenter.test", aiwork.PresenterTestIn{ID: pr.Name, Clip: clip, ProjectID: e.project, MaxUSD: usd(dry.Estimate.USD)}, true)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// lease takes a queued job as a worker would, so stopping it before it is
// sent is no longer possible (another computer has it).
func (e *env) lease(t *testing.T, jobID string) *models.Job {
	t.Helper()
	j, err := e.a.Queue.LeaseByID(context.Background(), jobs.Worker{ID: e.a.Cfg.Installation.ID, Name: "test", JobTypes: []string{jobs.TypeGenerate}, AcceptsHeavy: true}, jobID)
	if err != nil || j == nil || j.ID != jobID {
		t.Fatalf("lease %+v %v", j, err)
	}
	return j
}

func problems(est *aiwork.AIEstimate) string { return strings.Join(est.Problems, " ") }

// A presenter retired while its clip waited, or while it was being made: the
// clip is never sent, or what comes back isn't kept and is deleted at the
// service (D277).
func TestRetiredPresenterIsNeverSentOrKept(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in, ada, _ := e.presenterReady(t, "Ada Lovelace", "fake/fake-presenter-make")
	pr := e.makePresenter(t, in)
	line := e.sound(t, "ada line", 3)
	e.words(t, line, ada, strings.Fields("welcome to the garden"), func(i int) float64 { return float64(i) * 0.5 })

	// Waiting, taken by a computer, then retired: nothing is sent.
	first := e.askTest(t, pr, line.ID)
	j := e.lease(t, first.JobID)
	if _, err := call[aiwork.PresenterOut](t, e.a, "presenter.retire", aiwork.PresenterDecideIn{ID: pr.Name, Note: "for the test"}, true); err != nil {
		t.Fatal(err)
	}
	before := e.fake.Calls("submit")
	_, _ = aiwork.Run(ctx, e.a, j, nil)
	g := e.gen(t, first.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || !strings.Contains(g.Outcome.Message, "was retired") || e.fake.Calls("submit") != before {
		t.Fatalf("a retired presenter's clip was sent: %+v (%d sent)", g.Outcome, e.fake.Calls("submit")-before)
	}

	// Being made when it is retired: not kept, deleted at the service.
	in2, bea, _ := e.presenterReady(t, "Beatrix Potter", "fake/fake-presenter-make")
	pr2 := e.makePresenter(t, in2)
	line2 := e.sound(t, "bea line", 3.5)
	e.words(t, line2, bea, strings.Fields("welcome to the garden"), func(i int) float64 { return float64(i) * 0.5 })
	second := e.askTest(t, pr2, line2.ID)
	e.runHeld(t, second.JobID, func(func()) {
		if _, err := call[aiwork.PresenterOut](t, e.a, "presenter.retire", aiwork.PresenterDecideIn{ID: pr2.Name}, true); err != nil {
			t.Error(err)
		}
	})
	g = e.gen(t, second.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || !strings.Contains(g.Outcome.Message, "wasn't kept") || len(g.Outcome.Outputs) != 0 {
		t.Fatalf("a retired presenter's clip was kept: %+v", g.Outcome)
	}
	if g.Forgotten == nil || !g.Forgotten.OK || e.fake.Kept() != 0 {
		t.Fatalf("left at the service: %+v kept %d", g.Forgotten, e.fake.Kept())
	}
	if n, _ := e.a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "source.generated.generationId": second.Work.ID}); n != 0 {
		t.Fatalf("%d clips kept", n)
	}
}

// "No presenters" set in a project's Content Settings after a clip was asked
// for: it is checked again before sending, and nothing is sent (D273).
func TestNoPresentersCheckedBeforeSending(t *testing.T) {
	e := setup(t)
	line := e.sound(t, "a line", 3)
	out := e.ask(t, aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:fake-stock-amber", Model: "fake/fake-presenter", Clip: line.ID})
	if _, err := ops.CallTyped[ops.ContentShowOut](context.Background(), e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if _, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, ForbidKinds: []string{"presenters"}}, true); err != nil {
		t.Fatal(err)
	}
	before := e.fake.Calls("submit")
	_, _ = e.run(t, out.JobID)
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || !strings.Contains(g.Outcome.Message, "presenters") || !strings.Contains(g.Outcome.Message, "Nothing was sent") ||
		e.fake.Calls("submit") != before {
		t.Fatalf("sent despite No presenters: %+v (%d sent)", g.Outcome, e.fake.Calls("submit")-before)
	}
}

// A presenter clip whose answer was lost is never sent again: it is looked
// up at the service by its tag and deleted with the sound sent for it (D277).
func TestLostPresenterClipIsFoundAndDeleted(t *testing.T) {
	e := setup(t)
	key := "hg" + strings.Repeat("Lc3", 11) + "fake"
	fake := fakeheygen.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvHeyGenBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.HeyGenName), key)
	line := e.sound(t, "a plain line", 3)
	out := e.ask(t, aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:lk_amber_studio", Model: "heygen/avatar-iv", Clip: line.ID})
	fake.Next(fakeheygen.Garbled)
	_, _ = e.run(t, out.JobID)
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || !strings.Contains(g.Outcome.Message, "wasn't sent again") {
		t.Fatalf("outcome: %+v", g.Outcome)
	}
	if g.Forgotten == nil || !g.Forgotten.OK || !strings.Contains(g.Forgotten.Detail, "1 clip was found") || fake.Kept() != 0 || fake.Calls("POST /v3/videos") != 1 {
		t.Fatalf("forgotten %+v kept %d asked %d", g.Forgotten, fake.Kept(), fake.Calls("POST /v3/videos"))
	}
	// Running the job again (a restart) sends nothing more.
	_, _ = e.run(t, out.JobID)
	if fake.Calls("POST /v3/videos") != 1 {
		t.Fatal("sent again")
	}
}

// The consent recording is the person (D274): filmed, their voice named as
// them, every face on screen named as them; the service's words are read in
// that same recording.
func TestPresenterConsentRecordingIsThePerson(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	// A presenter's consent on sound only is refused.
	sound := e.sound(t, "rachel says", 6)
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Rachel Carson", Self: true, Adult: true, Uses: []string{"presenters"},
		Providers: []string{"fake"}, Recording: sound.ID, Statement: "I, Rachel Carson, agree to a presenter of me at the fake service."}, true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("a presenter's consent on sound only: %v", err)
	}
	dry := func(in aiwork.PresenterMakeIn) string {
		t.Helper()
		in.DryRun = true
		out, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", in, true)
		if err != nil {
			t.Fatal(err)
		}
		if out.Estimate.Allowed {
			return ""
		}
		return strings.Join(out.Estimate.Problems, " ")
	}
	in, _, consent := e.presenterReady(t, "Rachel Carson", "fake/fake-presenter-make")
	if p := dry(in); p != "" {
		t.Fatalf("the person's own recording: %s", p)
	}
	// Someone else's face in it.
	var rec models.Asset
	_ = e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), consent.RecordingID, &rec)
	e.faceOf(t, &rec, ids.New(ids.Person), "Someone Else", nil)
	if p := dry(in); !strings.Contains(p, "The consent recording shows Someone Else") {
		t.Fatalf("someone else on screen: %s", p)
	}
	// Not looked at for faces: no one can say it shows them.
	_, _ = e.a.DB.C(store.CFaces).DeleteMany(ctx, bson.M{"assetId": rec.ID})
	_ = e.a.DB.Update(ctx, store.CAssets, e.a.WorkspaceID(), rec.ID, bson.M{"$unset": bson.M{"analysis.visual": ""}})
	if p := dry(in); !strings.Contains(p, "hasn't looked for faces in the consent recording") {
		t.Fatalf("faces not looked for: %s", p)
	}
	// An older consent on sound only no longer covers a presenter.
	_ = e.a.DB.Update(ctx, store.CConsents, e.a.WorkspaceID(), consent.ID, bson.M{"$set": bson.M{"recordingId": sound.ID}})
	if p := dry(in); !strings.Contains(p, "sound only") {
		t.Fatalf("a consent on sound only: %s", p)
	}

	// Another person: a voice in the consent recording nobody named as them.
	in2, _, consent2 := e.presenterReady(t, "Marie Curie", "fake/fake-presenter-make")
	_, _ = e.a.DB.C(store.CSpeakers).UpdateMany(ctx, bson.M{"assetId": consent2.RecordingID}, bson.M{"$set": bson.M{"personId": "", "name": "", "source": ""}})
	if p := dry(in2); !strings.Contains(p, "Someone other than Marie Curie speaks in the consent recording") {
		t.Fatalf("an unnamed voice in the consent recording: %s", p)
	}
}

// The person's word that the take shows them counts only where MetaRush
// hasn't looked for faces; once it has, the faces must be named as the
// person, and one that looks like someone else refuses (D274).
func TestTakeShowsThePersonByTheFaces(t *testing.T) {
	e := setup(t)
	in, person, _ := e.presenterReady(t, "Ida Wells", "fake/fake-presenter-make")
	check := func(name string, take *models.Asset, said bool, want string) {
		t.Helper()
		in.Take, in.ShowsPerson, in.DryRun = take.ID, said, true
		out, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", in, true)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(out.Estimate.Problems, " ")
		if (want == "") != out.Estimate.Allowed || (want != "" && !strings.Contains(got, want)) {
			t.Errorf("%s: allowed %v, problems %q, want %q", name, out.Estimate.Allowed, got, want)
		}
	}
	take := func(name string) *models.Asset {
		as := e.video(t, name, 9)
		e.words(t, as, person, strings.Fields("hello this is me talking to the camera"), func(i int) float64 { return float64(i) * 0.6 })
		return as
	}
	notLooked := take("ida take not looked at")
	check("not looked at, said", notLooked, true, "")
	check("not looked at, not said", notLooked, false, "hasn't looked for faces in the training take")

	unnamed := take("ida take unnamed face")
	e.faceOf(t, unnamed, "", "", nil)
	check("an unnamed face, said", unnamed, true, "can't tell that every face in the training take")

	other := models.Person{ID: ids.New(ids.Person), Name: "Someone Else"}
	lookalike := take("ida take lookalike")
	e.faceOf(t, lookalike, "", "", &models.FaceRecognized{PersonID: other.ID, Name: other.Name, Score: 0.9, At: time.Now()})
	check("a face like someone else's, said", lookalike, true, "looks like Someone Else")

	named := take("ida take named")
	e.faceOf(t, named, person.ID, person.Name, nil)
	check("named as her, not said", named, false, "")
}

// A person's presenter speaks only their own voice — a recording in which
// every voice is named as them, or AI speech in a copy of their voice — or
// AI speech in a voice that is no one's; never a stranger's recording or a
// dub of one (D273).
func TestPresenterReadsOnlyTheirOwnVoice(t *testing.T) {
	e := setup(t)
	in, mary, _ := e.presenterReady(t, "Mary Somerville", "fake/fake-presenter-make")
	pr := e.makePresenter(t, in)
	bob, _, _ := e.person(t, "Bob Baker", []string{models.ConsentVoice}, []string{"fake"})
	check := func(name, clip, want string) {
		t.Helper()
		out, err := call[aiwork.MakeOut](t, e.a, "presenter.test", aiwork.PresenterTestIn{ID: pr.Name, Clip: clip, ProjectID: e.project, DryRun: true}, true)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(out.Estimate.Problems, " ")
		if (want == "") != out.Estimate.Allowed || (want != "" && !strings.Contains(got, want)) {
			t.Errorf("%s: allowed %v, problems %q, want %q", name, out.Estimate.Allowed, got, want)
		}
	}
	// (Each sound is a different length: the same bytes would be one clip.)
	hers := e.sound(t, "mary speaks", 4)
	e.words(t, hers, mary, strings.Fields("welcome to the garden"), func(i int) float64 { return float64(i) * 0.5 })
	check("her own recording", hers.ID, "")

	his := e.sound(t, "bob speaks", 4.5)
	e.words(t, his, bob, strings.Fields("welcome to the garden"), func(i int) float64 { return float64(i) * 0.5 })
	check("Bob's recording", his.ID, "this is Bob Baker's voice")

	nobody := e.sound(t, "nobody named", 5)
	e.words(t, nobody, models.Person{}, strings.Fields("welcome to the garden"), func(i int) float64 { return float64(i) * 0.5 })
	check("a voice nobody named", nobody.ID, "Nobody has named a voice")

	_, dub := e.make(t, aiwork.MakeIn{Kind: aiservices.KindDubbing, ProjectID: e.project, Clip: his.ID, Model: "fake/fake-dubbing", Options: models.GenOptions{TargetLanguage: "es"}})
	check("a dub of Bob's recording", dub.Outcome.Outputs[0].AssetID, "this is Bob Baker's voice")

	_, speech := e.make(t, aiwork.MakeIn{Kind: aiservices.KindVoice, ProjectID: e.project, Model: "fake/fake-voice", Prompt: "Welcome to the garden.", Options: models.GenOptions{Voice: "Kore"}})
	check("AI speech in a voice that is no one's", speech.Outcome.Outputs[0].AssetID, "")
}

// A named person's voice goes to a service that trains on what it is sent
// only with the opt-out recorded for this key and their consent naming the
// service — a recording of them as much as a copy of their voice — and the
// opt-out is checked again just before sending (D276).
func TestTrainingOptOutForEveryNamedVoice(t *testing.T) {
	e := setup(t)
	key := "hg" + strings.Repeat("Tr8", 11) + "fake"
	fake := fakeheygen.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvHeyGenBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.HeyGenName), key)
	bob, _, _ := e.person(t, "Bob Baker", []string{models.ConsentVoice}, []string{"fake"})
	his := e.sound(t, "bob speaks", 3)
	e.words(t, his, bob, strings.Fields("welcome to the garden"), func(i int) float64 { return float64(i) * 0.5 })
	in := aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:lk_adam_studio", Model: "heygen/avatar-iv", Clip: his.ID}
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false); codeOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "Bob Baker's voice would go to HeyGen") {
		t.Fatalf("Bob's recording without the opt-out: %v", err)
	}
	if _, err := call[aiwork.PresenterOptOutOut](t, e.a, "presenter.optout.set", aiwork.PresenterOptOutIn{Provider: "heygen", How: "wrote to HeyGen's privacy team and got a confirmation"}, true); err != nil {
		t.Fatal(err)
	}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || est.Allowed || !strings.Contains(problems(est), "takes Bob Baker's recorded consent naming HeyGen") {
		t.Fatalf("Bob's consent doesn't name HeyGen: %+v %v", est, err)
	}
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Bob Baker", Self: true, Adult: true, Uses: []string{models.ConsentVoice},
		Providers: []string{"heygen"}, Statement: "I, Bob Baker, agree to my voice going to HeyGen for these videos."}, true); err != nil {
		t.Fatal(err)
	}
	out := e.ask(t, in)
	// The opt-out taken back while it waits: refused before sending, saying whose voice.
	if _, err := call[aiwork.PresenterOptOutOut](t, e.a, "presenter.optout.set", aiwork.PresenterOptOutIn{Provider: "heygen", Remove: true}, true); err != nil {
		t.Fatal(err)
	}
	_, _ = e.run(t, out.JobID)
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || !strings.Contains(g.Outcome.Message, "Bob Baker") || !strings.Contains(g.Outcome.Message, "opts out") ||
		fake.Calls("POST /v3/videos") != 0 {
		t.Fatalf("sent without the opt-out: %+v (%d sent)", g.Outcome, fake.Calls("POST /v3/videos"))
	}
}

// A consent that ends (its last day passed) retires the presenters relying
// on it: the daemon's sweep finds them and deletes them at the service (D277).
func TestEndedConsentRetiresItsPresenters(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in, _, consent := e.presenterReady(t, "Lise Meitner", "fake/fake-presenter-make")
	pr := e.makePresenter(t, in)
	if n := aiwork.Sweep(ctx, e.a); n != 0 {
		t.Fatalf("swept %d while the consent holds", n)
	}
	past := time.Now().Add(-time.Hour).UTC()
	_ = e.a.DB.Update(ctx, store.CConsents, e.a.WorkspaceID(), consent.ID, bson.M{"$set": bson.M{"expiresAt": past}})
	if n := aiwork.Sweep(ctx, e.a); n == 0 {
		t.Fatal("nothing swept")
	}
	v, _ := call[aiwork.PresenterView](t, e.a, "presenter.show", aiwork.PresenterIDIn{ID: pr.Name}, false)
	if v.State != models.PresenterRetired || !strings.Contains(v.RetireNote, "has ended") || v.Removed == nil || len(e.fake.Presenters()) != 0 {
		t.Fatalf("after the consent ended: %+v kept %v", v.Presenter, e.fake.Presenters())
	}
}
