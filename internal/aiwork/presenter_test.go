package aiwork_test

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/did"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeai"
	"github.com/metavert-io/metarush/internal/testutil/fakedid"
	"github.com/metavert-io/metarush/internal/testutil/fakeheygen"
)

// video adds a clip with a picture and sound, seconds long.
func (e *env) video(t *testing.T, name string, seconds float64) *models.Asset {
	t.Helper()
	p := filepath.Join(t.TempDir(), name+".mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=320x180:rate=25:duration=%.2f", seconds),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=%.2f", 200+len(name)*7, seconds), "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-shortest", p)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
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

var tokenRe = regexp.MustCompile(`[\p{L}\p{N}'’-]+`)

// face marks a clip as looked at for faces and puts a face on screen all
// through it: named as a person, or unnamed (and maybe recognized by its looks).
func (e *env) faceOf(t *testing.T, as *models.Asset, personID, name string, recognized *models.FaceRecognized) {
	t.Helper()
	ctx := context.Background()
	if err := e.a.DB.Update(ctx, store.CAssets, e.a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"analysis.visual": "available"}}); err != nil {
		t.Fatal(err)
	}
	if as.Analysis == nil {
		as.Analysis = map[string]string{}
	}
	as.Analysis["visual"] = "available"
	now := time.Now().UTC()
	f := models.FaceTrack{ID: ids.New(ids.Face), WorkspaceID: e.a.WorkspaceID(), AssetID: as.ID, Track: int(now.UnixNano() % 1000), From: 0, To: as.Media.Duration, At: 1,
		Box: []float64{0.3, 0.2, 0.3, 0.4}, PersonID: personID, Name: name, Recognized: recognized, CreatedAt: now, UpdatedAt: now}
	if personID != "" {
		f.Naming = byPerson(e)
	}
	if err := e.a.DB.Insert(ctx, store.CFaces, f); err != nil {
		t.Fatal(err)
	}
}

// person adds someone with their recorded consent (covering what's asked,
// naming the services) and an approved look. The consent recording is a
// video of them: their voice and face named as them (D274).
func (e *env) person(t *testing.T, name string, uses, services []string) (models.Person, models.Consent, *models.Asset) {
	t.Helper()
	ctx := context.Background()
	rec := e.video(t, name+" consent", 30)
	out, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: name, Self: true, Adult: true, Recording: rec.ID, Uses: uses, Providers: services,
		Statement: "I, " + name + ", agree to a presenter of me kept at these services for my own videos."}, true)
	if err != nil {
		t.Fatal(err)
	}
	var p models.Person
	_ = e.a.DB.C(store.CPeople).FindOne(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "name": name}).Decode(&p)
	e.words(t, rec, p, strings.Fields("i agree to a presenter of me"), func(i int) float64 { return float64(i) * 0.3 })
	e.faceOf(t, rec, p.ID, p.Name, nil)
	now := time.Now().UTC()
	look := models.GenLook{ID: ids.New(ids.Look), WorkspaceID: e.a.WorkspaceID(), Name: strings.ToLower(strings.Fields(name)[0]), Title: name, Subject: models.LookPerson,
		PersonID: p.ID, PersonName: name, Style: models.LookRealistic, Description: "a person", Version: "v1abcdef0000", State: models.LookApproved, ConsentID: out.Consent.ID,
		CreatedBy: e.a.Actor, CreatedAt: now, UpdatedAt: now}
	if err := e.a.DB.Insert(ctx, store.CLooks, look); err != nil {
		t.Fatal(err)
	}
	return p, out.Consent.Consent, rec
}

// presenterReady begins a presenter at a service, and has the person's
// reading of its words and a training take ready to send.
func (e *env) presenterReady(t *testing.T, name, model string) (aiwork.PresenterMakeIn, models.Person, models.Consent) {
	return e.presenterReadyTake(t, name, model, 9)
}

// presenterReadyTake is presenterReady with a training take of some seconds.
func (e *env) presenterReadyTake(t *testing.T, name, model string, seconds float64) (aiwork.PresenterMakeIn, models.Person, models.Consent) {
	t.Helper()
	provider, _ := aiservices.SplitRef(model)
	person, consent, rec := e.person(t, name, []string{models.ConsentShots, models.ConsentVoice, models.ConsentPresenters}, []string{provider})
	start, err := call[aiwork.PresenterOut](t, e.a, "presenter.start", aiwork.PresenterStartIn{Person: name, Model: model, GreenScreen: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	pr := start.Presenter
	if pr.State != models.PresenterWaiting || pr.Words == "" || !strings.Contains(pr.Words, name) || !strings.Contains(start.Summary, pr.Words) {
		t.Fatalf("start: %+v", start)
	}
	// The reading of the service's words: in the consent recording, after their own (D274).
	e.words(t, rec, person, tokenRe.FindAllString(pr.Words, -1), func(i int) float64 { return 3 + float64(i)*0.3 })
	take := e.video(t, name+" take", seconds)
	e.words(t, take, person, strings.Fields("hello this is me talking to the camera for a while"), func(i int) float64 { return float64(i) * 0.6 })
	return aiwork.PresenterMakeIn{ID: pr.Name, Take: take.ID, Reading: rec.ID, ShowsPerson: true, ProjectID: e.project}, person, consent
}

// makePresenter makes a presenter through its job, priced first.
func (e *env) makePresenter(t *testing.T, in aiwork.PresenterMakeIn) models.Presenter {
	t.Helper()
	in.DryRun = true
	dry, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", in, true)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.Estimate.Allowed {
		t.Fatalf("not allowed: %v", dry.Estimate.Problems)
	}
	in.DryRun, in.MaxUSD = false, usd(dry.Estimate.USD)
	out, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", in, true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Presenter.State != models.PresenterTraining || out.JobID == "" {
		t.Fatalf("make: %+v", out)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatalf("job: %v", err)
	}
	v, err := call[aiwork.PresenterView](t, e.a, "presenter.show", aiwork.PresenterIDIn{ID: in.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	return v.Presenter
}

// A person's presenter from start to retirement (D273, D274, D277): begun
// only by a person, made from the person reading the service's words and a
// training take with their recorded consent, tested, approved, reading
// lines in clips marked as AI presenters, and retired when the consent is
// withdrawn — deleted at the service with its consent request.
func TestPresenterFromStartToRetirement(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := call[aiwork.PresenterOut](t, e.a, "presenter.start", aiwork.PresenterStartIn{Person: "Nobody Yet"}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("an agent began a presenter: %v", err)
	}
	in, ada, consent := e.presenterReady(t, "Ada Lovelace", "fake/fake-presenter-make")

	// The reading is part of the consent recording: by itself when none is
	// chosen, never another recording (D274).
	noReading := in
	noReading.Reading, noReading.DryRun = "", true
	dry, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", noReading, true)
	if err != nil || !dry.Estimate.Allowed {
		t.Fatalf("the consent recording as the reading: %+v %v", dry, err)
	}
	elsewhere := e.video(t, "Ada Lovelace reads elsewhere", 8)
	noReading.Reading = elsewhere.ID
	if _, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", noReading, true); codeOf(err) != apperr.ConsentRequired || !strings.Contains(err.Error(), "same recording") {
		t.Fatalf("a reading outside the consent recording: %v", err)
	}
	if _, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", in, true); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("made without the agreed price: %v", err)
	}
	pr := e.makePresenter(t, in)
	if pr.State != models.PresenterDraft || pr.ProviderID == "" || len(e.fake.Presenters()) != 1 {
		t.Fatalf("made: %+v kept %v", pr, e.fake.Presenters())
	}
	tg := e.gen(t, pr.TrainingID)
	if tg.Kind != aiservices.KindPresenterMake || len(tg.References) != 2 || tg.References[0].Role != aiservices.RoleConsent || tg.References[1].Role != aiservices.RoleFootage ||
		tg.Settled == nil || tg.Settled.USD != 0.5 || tg.Forgotten == nil || len(tg.ConsentIDs) != 1 || tg.ConsentIDs[0] != consent.ID {
		t.Fatalf("training record: %+v", tg)
	}

	// Not approved yet: no clips but test clips. The line is Ada speaking.
	line := e.sound(t, "line one", 4)
	e.words(t, line, ada, strings.Fields("welcome to the garden"), func(i int) float64 { return float64(i) * 0.5 })
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: pr.Name, Clip: line.ID}, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("a clip of a presenter not approved: %v", err)
	}
	if _, err := call[aiwork.PresenterOut](t, e.a, "presenter.approve", aiwork.PresenterDecideIn{ID: pr.Name}, true); codeOf(err) != apperr.Conflict {
		t.Fatalf("approved untested: %v", err)
	}
	test, err := call[aiwork.MakeOut](t, e.a, "presenter.test", aiwork.PresenterTestIn{ID: pr.Name, Clip: line.ID, ProjectID: e.project, DryRun: true}, true)
	if err != nil || !test.Estimate.Allowed || test.Estimate.Units != "15 seconds on camera" {
		t.Fatalf("test estimate: %+v %v", test, err)
	}
	test, err = call[aiwork.MakeOut](t, e.a, "presenter.test", aiwork.PresenterTestIn{ID: pr.Name, Clip: line.ID, ProjectID: e.project, MaxUSD: usd(test.Estimate.USD)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, test.JobID); err != nil {
		t.Fatal(err)
	}
	if _, err := call[aiwork.PresenterOut](t, e.a, "presenter.approve", aiwork.PresenterDecideIn{ID: pr.Name}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("an agent approved it: %v", err)
	}
	appr, err := call[aiwork.PresenterOut](t, e.a, "presenter.approve", aiwork.PresenterDecideIn{ID: pr.Name}, true)
	if err != nil || appr.Presenter.State != models.PresenterApproved {
		t.Fatalf("approve: %+v %v", appr, err)
	}

	// A clip: priced first, made, MetaRush's own sound under the picture, marked everywhere.
	_, g := e.make(t, aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: pr.Name, Clip: line.ID, Options: models.GenOptions{Background: "green"}})
	if g.Outcome == nil || g.Outcome.State != models.GenDone || g.Options.Presenter != pr.ProviderID || g.Options.Background != aiwork.GreenScreen || len(g.ConsentIDs) != 1 {
		t.Fatalf("clip: %+v", g)
	}
	var clip models.Asset
	if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), g.Outcome.Outputs[0].AssetID, &clip); err != nil {
		t.Fatal(err)
	}
	src := clip.Source
	if src == nil || src.Generated == nil || src.Generated.Kind != aiservices.KindPresenter || src.Generated.Presenter != pr.ID || !src.Generated.Likeness ||
		!strings.Contains(src.GeneratedLine(), "An AI presenter made with Fake AI") || !clip.Media.HasVideo || !clip.Media.HasAudio {
		t.Fatalf("the clip: %+v %+v", src, clip.Media)
	}
	if !strings.Contains(clip.DisplayTitle(), "Ada Lovelace reads") {
		t.Errorf("title: %q", clip.DisplayTitle())
	}
	if g.Forgotten == nil || !g.Forgotten.OK {
		t.Errorf("not deleted at the service: %+v", g.Forgotten)
	}
	// Counted in the service's 15-second steps, like the estimate: 4 seconds count as 15.
	if g.Settled == nil || g.Settled.USD != 0.3 || g.Outcome.Actual == nil || g.Outcome.Actual.Basis != "usage" {
		t.Errorf("settled %+v actual %+v", g.Settled, g.Outcome.Actual)
	}
	shown, err := call[aiwork.PresenterView](t, e.a, "presenter.show", aiwork.PresenterIDIn{ID: pr.Name}, false)
	if err != nil || shown.Clips != 2 || shown.Training == nil {
		t.Fatalf("show: %+v %v", shown, err)
	}

	// Withdrawing the consent retires it and deletes it at the service.
	rev, err := call[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: consent.ID}, true)
	if err != nil || !strings.Contains(strings.Join(rev.AlsoWithdrawn, " "), "Retired") {
		t.Fatalf("revoke: %+v %v", rev, err)
	}
	after, _ := call[aiwork.PresenterView](t, e.a, "presenter.show", aiwork.PresenterIDIn{ID: pr.Name}, false)
	if after.State != models.PresenterRetired || after.Removed == nil || len(e.fake.Presenters()) != 0 || len(e.fake.ConsentRequests()) != 0 || after.Erasure == "" {
		t.Fatalf("after: %+v presenters %v consents %v", after.Presenter, e.fake.Presenters(), e.fake.ConsentRequests())
	}
	tg = e.gen(t, pr.TrainingID)
	if tg.Withdrawn == nil || tg.Removed == nil {
		t.Errorf("the making's record doesn't say it was deleted: %+v %+v", tg.Withdrawn, tg.Removed)
	}
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: pr.Name, Clip: line.ID}, false); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("a retired presenter read a line: %v", err)
	}
}

// What a presenter's consent must say (D274): recorded (one's own too),
// covering a presenter, naming the service.
func TestPresenterConsentMustBeRecordedAndNameTheService(t *testing.T) {
	e := setup(t)
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Grace Hopper", Self: true, Adult: true, Uses: []string{"presenters"},
		Providers: []string{"fake"}, Statement: "I, Grace Hopper, agree to a presenter of me at the fake service."}, true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("a typed statement covered a presenter: %v", err)
	}
	rec := e.video(t, "grace says", 5)
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Grace Hopper", Self: true, Adult: true, Uses: []string{"presenters"},
		Recording: rec.ID, Statement: "I, Grace Hopper, agree to a presenter of me at the fake service."}, true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("no service named: %v", err)
	}
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Grace Hopper", Self: true, Adult: true, Uses: []string{"presenters"},
		Recording: rec.ID, Providers: []string{"nowhere"}, Statement: "I, Grace Hopper, agree to a presenter of me at the fake service."}, true); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("an unknown service: %v", err)
	}
	// Shots only: the presenter can be begun, but not made.
	person, _, _ := e.person(t, "Grace Hopper", []string{models.ConsentShots}, nil)
	start, err := call[aiwork.PresenterOut](t, e.a, "presenter.start", aiwork.PresenterStartIn{Person: "Grace Hopper", Model: "fake/fake-presenter-make"}, true)
	if err != nil {
		t.Fatal(err)
	}
	take := e.video(t, "grace take", 7)
	e.words(t, take, person, strings.Fields("hello this is grace"), func(i int) float64 { return float64(i) })
	dry, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", aiwork.PresenterMakeIn{ID: start.Presenter.Name, Take: take.ID, ShowsPerson: true, DryRun: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Estimate.Allowed || !strings.Contains(strings.Join(dry.Estimate.Problems, " "), "covers a presenter and names Fake AI") {
		t.Fatalf("shots-only consent: %v", dry.Estimate.Problems)
	}
	// Someone else's face in the take.
	other := e.video(t, "not grace take", 7)
	e.words(t, other, models.Person{ID: ids.New(ids.Person), Name: "Someone Else"}, strings.Fields("hello this is someone"), func(i int) float64 { return float64(i) })
	dry, _ = call[aiwork.PresenterOut](t, e.a, "presenter.make", aiwork.PresenterMakeIn{ID: start.Presenter.Name, Take: other.ID, ShowsPerson: true, DryRun: true}, true)
	if dry == nil || !strings.Contains(strings.Join(dry.Estimate.Problems, " "), "Someone other than Grace Hopper speaks") {
		t.Fatalf("someone else speaks: %+v", dry)
	}
}

// Where a service trains on uploads unless opted out, a person's presenter
// is made there only once a person recorded the opt-out for this key (D276).
func TestTrainingOptOutComesFirst(t *testing.T) {
	e := setupWith(t, fakeai.Options{Trains: true})
	e.person(t, "Katherine Johnson", []string{models.ConsentPresenters}, []string{"fake"})
	_, err := call[aiwork.PresenterOut](t, e.a, "presenter.start", aiwork.PresenterStartIn{Person: "Katherine Johnson", Model: "fake/fake-presenter-make"}, true)
	if codeOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "unless the account opts out") {
		t.Fatalf("no opt-out: %v", err)
	}
	view, _ := call[aiwork.PresenterOptOutOut](t, e.a, "presenter.optout", aiwork.PresenterOptOutIn{Provider: "fake"}, false)
	if view == nil || view.Trains != aiservices.TrainsUnlessOptedOut || view.OptOut != nil {
		t.Fatalf("view: %+v", view)
	}
	if _, err := call[aiwork.PresenterOptOutOut](t, e.a, "presenter.optout.set", aiwork.PresenterOptOutIn{Provider: "fake", How: "wrote to its privacy team"}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("an agent recorded an opt-out: %v", err)
	}
	set, err := call[aiwork.PresenterOptOutOut](t, e.a, "presenter.optout.set", aiwork.PresenterOptOutIn{Provider: "fake", How: "wrote to its privacy team and got a confirmation"}, true)
	if err != nil || set.OptOut == nil || set.OptOut.KeyEnding == "" || strings.Contains(set.OptOut.KeyEnding, e.fake.Key) {
		t.Fatalf("set: %+v %v", set, err)
	}
	if _, err := call[aiwork.PresenterOut](t, e.a, "presenter.start", aiwork.PresenterStartIn{Person: "Katherine Johnson", Model: "fake/fake-presenter-make"}, true); err != nil {
		t.Fatalf("after the opt-out: %v", err)
	}
	if _, err := call[aiwork.PresenterOptOutOut](t, e.a, "presenter.optout.set", aiwork.PresenterOptOutIn{Provider: "fake", Remove: true}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call[aiwork.PresenterOut](t, e.a, "presenter.start", aiwork.PresenterStartIn{Person: "Katherine Johnson", Model: "fake/fake-presenter-make"}, true); codeOf(err) != apperr.Conflict {
		t.Fatalf("after taking it back: %v", err)
	}
}

// Stock presenters are offered only where their licence covers commercial
// use and client work; the licence and the ads it doesn't cover are said (D275).
func TestStockPresentersByLicence(t *testing.T) {
	e := setup(t)
	list, err := call[aiwork.PresenterStockOut](t, e.a, "presenter.stock", aiwork.PresenterStockIn{Provider: "fake"}, false)
	if err != nil || len(list.Services) != 1 || !list.Services[0].Offered || len(list.Services[0].Presenters) != 2 || !strings.Contains(list.Services[0].Note, "ads") {
		t.Fatalf("stock: %+v %v", list, err)
	}
	line := e.sound(t, "a line", 3)
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:fake-stock-amber",
		Model: "fake/fake-presenter", Clip: line.ID}, false)
	if err != nil || !est.Allowed || !strings.Contains(strings.Join(est.Notes, " "), "doesn't cover ads") {
		t.Fatalf("stock estimate: %+v %v", est, err)
	}
	_, g := e.make(t, aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:fake-stock-amber", Model: "fake/fake-presenter", Clip: line.ID})
	var clip models.Asset
	_ = e.a.DB.GetByID(context.Background(), store.CAssets, e.a.WorkspaceID(), g.Outcome.Outputs[0].AssetID, &clip)
	if !g.Options.Stock || clip.Source.Generated.Presenter != "stock:fake-stock-amber" || clip.Source.Generated.Likeness {
		t.Fatalf("stock clip: %+v %+v", g.Options, clip.Source.Generated)
	}
	// D-ID's stock presenters: their licence is unstated, so they aren't offered.
	ds, err := call[aiwork.PresenterStockOut](t, e.a, "presenter.stock", aiwork.PresenterStockIn{Provider: "d-id"}, false)
	if err != nil || ds.Services[0].Offered || len(ds.Services[0].Presenters) != 0 {
		t.Fatalf("d-id stock: %+v %v", ds, err)
	}
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:v2_public_x",
		Model: "d-id/express", Clip: line.ID}, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("d-id stock offered: %v", err)
	}
}

// A client's Content Settings can forbid presenters (D277).
func TestContentSettingsForbidPresenters(t *testing.T) {
	e := setup(t)
	if _, err := ops.CallTyped[ops.ContentShowOut](context.Background(), e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if _, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, ForbidKinds: []string{"presenters"}}, true); err != nil {
		t.Fatal(err)
	}
	rules, _ := call[aiwork.RulesOut](t, e.a, "ai.rules", aiwork.RulesIn{ProjectID: e.project}, false)
	if rules == nil || !strings.Contains(rules.Summary, "No presenters") {
		t.Fatalf("rules: %+v", rules)
	}
	line := e.sound(t, "a line", 3)
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:fake-stock-amber",
		Model: "fake/fake-presenter", Clip: line.ID}, false); codeOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "don't allow presenters") {
		t.Fatalf("forbidden presenters: %v", err)
	}
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindSoundEffect, ProjectID: e.project, Prompt: "a door", Options: models.GenOptions{Seconds: 2}}, false); err != nil {
		t.Fatalf("other kinds still allowed: %v", err)
	}
}

// A person's presenter speaks only their own voice: never another person's
// voice copy (D273).
func TestPresenterSpeaksOnlyTheirVoice(t *testing.T) {
	e := setup(t)
	in, _, _ := e.presenterReady(t, "Mary Somerville", "fake/fake-presenter-make")
	pr := e.makePresenter(t, in)
	// Bob's voice copy, and speech made with it.
	bob, _ := e.copyReady(t, "Bob Baker")
	out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", bob, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	voice := e.gen(t, out.Work.ID).Outcome.Outputs[0].VoiceID
	_, speech := e.make(t, aiwork.MakeIn{Kind: aiservices.KindVoice, ProjectID: e.project, Prompt: "Welcome to the garden.", Model: "fake/fake-voice", Options: models.GenOptions{Voice: voice}})
	est, err := call[aiwork.MakeOut](t, e.a, "presenter.test", aiwork.PresenterTestIn{ID: pr.Name, Clip: speech.Outcome.Outputs[0].AssetID, ProjectID: e.project, DryRun: true}, true)
	if err != nil || est.Estimate.Allowed || !strings.Contains(strings.Join(est.Estimate.Problems, " "), "a copy of Bob Baker's voice") {
		t.Fatalf("another person's voice: %+v %v", est, err)
	}
}

// A presenter whose answer was lost may have been made all the same: it is
// retired, found at the service by the tag in its name and deleted (D277).
func TestLostPresenterIsFoundAndDeleted(t *testing.T) {
	e := setup(t)
	in, _, _ := e.presenterReady(t, "Lise Meitner", "fake/fake-presenter-make")
	in.DryRun = true
	dry, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", in, true)
	if err != nil {
		t.Fatal(err)
	}
	in.DryRun, in.MaxUSD = false, usd(dry.Estimate.USD)
	e.fake.Script(fakeai.Behavior{Kind: fakeai.Lost})
	out, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", in, true)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = e.run(t, out.JobID)
	v, _ := call[aiwork.PresenterView](t, e.a, "presenter.show", aiwork.PresenterIDIn{ID: in.ID}, false)
	if v.State != models.PresenterRetired || v.Removed == nil || !strings.Contains(v.Removed.Detail, "1 presenter was found") || len(e.fake.Presenters()) != 0 {
		t.Fatalf("lost presenter: %+v kept %v", v.Presenter, e.fake.Presenters())
	}
}

// Retiring on a computer without the service's key leaves a deletion job
// that any computer with the key takes; it is tried until done (D277).
func TestPresenterDeletionIsAJob(t *testing.T) {
	e := setup(t)
	in, _, _ := e.presenterReady(t, "Emmy Noether", "fake/fake-presenter-make")
	pr := e.makePresenter(t, in)
	t.Setenv(providers.EnvVar(fakeai.Name), "")
	out, err := call[aiwork.PresenterOut](t, e.a, "presenter.retire", aiwork.PresenterDecideIn{ID: pr.Name, Note: "She changed her mind."}, true)
	if err != nil || out.Presenter.Removed != nil || !strings.Contains(out.Summary, "A computer with the Fake AI key deletes it there") {
		t.Fatalf("retire without the key: %+v %v", out, err)
	}
	if len(e.fake.Presenters()) != 1 {
		t.Fatal("deleted without a key")
	}
	t.Setenv(providers.EnvVar(fakeai.Name), e.fake.Key)
	if _, err := e.run(t, out.Presenter.RetireJobID); err != nil {
		t.Fatal(err)
	}
	v, _ := call[aiwork.PresenterView](t, e.a, "presenter.show", aiwork.PresenterIDIn{ID: pr.Name}, false)
	if v.Removed == nil || len(e.fake.Presenters()) != 0 {
		t.Fatalf("after the job: %+v %v", v.Removed, e.fake.Presenters())
	}
}

// A dub of a presenter clip is read again by the same presenter, so the
// lips follow the new language (D273).
func TestDubbedPresenterClipIsReadAgain(t *testing.T) {
	e := setup(t)
	line := e.sound(t, "a line", 3)
	_, g := e.make(t, aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:fake-stock-adam", Model: "fake/fake-presenter", Clip: line.ID})
	_, dub := e.make(t, aiwork.MakeIn{Kind: aiservices.KindDubbing, ProjectID: e.project, Clip: g.Outcome.Outputs[0].AssetID, Model: "fake/fake-dubbing",
		Options: models.GenOptions{TargetLanguage: "es"}})
	_, again := e.make(t, aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Model: "fake/fake-presenter", Clip: dub.Outcome.Outputs[0].AssetID})
	if again.Options == nil || again.Options.Presenter != "fake-stock-adam" || !again.Options.Stock {
		t.Fatalf("the dub wasn't read by the same presenter: %+v", again.Options)
	}
}

// D-ID through the job: the reading and the take reach D-ID by links (on
// this computer, its fake), the presenter is made, a clip read, and retiring
// deletes the presenter and D-ID's consent record.
func TestDIDThroughTheJob(t *testing.T) {
	e := setup(t)
	key := "ZGlk" + strings.Repeat("Mt4", 9) + ":fake"
	fake := fakedid.New(key)
	fake.Polls = 1
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvDIDBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.DIDName), key)
	did.ConsentWait = 5 * time.Second
	in, hedy, _ := e.presenterReadyTake(t, "Hedy Lund", "d-id/express", 61)
	pr := e.makePresenter(t, in)
	if pr.State != models.PresenterDraft || !strings.HasPrefix(pr.ProviderID, "avt_") || len(fake.Fetched()) != 2 {
		t.Fatalf("made at D-ID: %+v fetched %v problems %v", pr, fake.Fetched(), fake.Problems())
	}
	line := e.sound(t, "hedy line", 4)
	e.words(t, line, hedy, strings.Fields("welcome to the garden"), func(i int) float64 { return float64(i) * 0.5 })
	test, err := call[aiwork.MakeOut](t, e.a, "presenter.test", aiwork.PresenterTestIn{ID: pr.Name, Clip: line.ID, ProjectID: e.project, DryRun: true}, true)
	if err != nil || !test.Estimate.Allowed {
		t.Fatalf("test estimate: %+v %v", test, err)
	}
	test, err = call[aiwork.MakeOut](t, e.a, "presenter.test", aiwork.PresenterTestIn{ID: pr.Name, Clip: line.ID, ProjectID: e.project, MaxUSD: usd(test.Estimate.USD)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, test.JobID); err != nil {
		t.Fatal(err)
	}
	g := e.gen(t, test.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenDone || g.Forgotten == nil || !g.Forgotten.OK || fake.Kept() != 0 {
		t.Fatalf("clip at D-ID: %+v forgotten %+v kept %d", g.Outcome, g.Forgotten, fake.Kept())
	}
	if _, err := call[aiwork.PresenterOut](t, e.a, "presenter.retire", aiwork.PresenterDecideIn{ID: pr.Name}, true); err != nil {
		t.Fatal(err)
	}
	if ps, cs := fake.Presenters(), fake.Consents(); len(ps) != 0 || len(cs) != 0 {
		t.Fatalf("left at D-ID: presenters %v consents %v", ps, cs)
	}
	if ps := fake.Problems(); len(ps) > 0 {
		t.Errorf("the fake saw: %v", ps)
	}
}

// HeyGen's stock presenters through the job: listed, a clip of one read from
// a plain sound and made; a clip of a person's voice copy goes there only once
// HeyGen's training opt-out is recorded for this key (D275, D276).
func TestHeyGenStockThroughTheJob(t *testing.T) {
	e := setup(t)
	key := "hg" + strings.Repeat("Nw2", 11) + "fake"
	fake := fakeheygen.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvHeyGenBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.HeyGenName), key)
	list, err := call[aiwork.PresenterStockOut](t, e.a, "presenter.stock", aiwork.PresenterStockIn{Provider: "heygen"}, false)
	if err != nil || len(list.Services) != 1 || !list.Services[0].Offered || len(list.Services[0].Presenters) != 3 {
		t.Fatalf("stock: %+v %v", list, err)
	}
	line := e.sound(t, "a plain line", 3)
	_, g := e.make(t, aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:lk_amber_studio", Model: "heygen/avatar-iv", Clip: line.ID,
		Options: models.GenOptions{AspectRatio: "9:16"}})
	if g.Outcome == nil || g.Outcome.State != models.GenDone || g.Forgotten == nil || !g.Forgotten.OK || fake.Kept() != 0 || g.Terms == nil || !strings.Contains(g.Terms.Licence, "client work") {
		t.Fatalf("heygen clip: %+v forgotten %+v kept %d terms %+v", g.Outcome, g.Forgotten, fake.Kept(), g.Terms)
	}
	// A person's voice copy: not until the opt-out is recorded.
	bob, _ := e.copyReady(t, "Bob Baker")
	out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", bob, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	voice := e.gen(t, out.Work.ID).Outcome.Outputs[0].VoiceID
	_, speech := e.make(t, aiwork.MakeIn{Kind: aiservices.KindVoice, ProjectID: e.project, Prompt: "Welcome to the garden.", Model: "fake/fake-voice", Options: models.GenOptions{Voice: voice}})
	in := aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:lk_adam_studio", Model: "heygen/avatar-iv", Clip: speech.Outcome.Outputs[0].AssetID}
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false); codeOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "Bob Baker's voice would go to HeyGen") ||
		!strings.Contains(err.Error(), "opts out") {
		t.Fatalf("a voice copy at a service that trains: %v", err)
	}
	if _, err := call[aiwork.PresenterOptOutOut](t, e.a, "presenter.optout.set", aiwork.PresenterOptOutIn{Provider: "heygen", How: "wrote to HeyGen's privacy team and got a confirmation"}, true); err != nil {
		t.Fatal(err)
	}
	// The opt-out isn't enough: Bob's consent must name HeyGen (D276).
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || est.Allowed || !strings.Contains(strings.Join(est.Problems, " "), "takes Bob Baker's recorded consent naming HeyGen") {
		t.Fatalf("Bob's consent doesn't name HeyGen: %+v %v", est, err)
	}
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Bob Baker", Self: true, Adult: true, Uses: []string{models.ConsentVoice},
		Providers: []string{"heygen"}, Statement: "I, Bob Baker, agree to my voice going to HeyGen for these videos."}, true); err != nil {
		t.Fatal(err)
	}
	est, err = call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || !est.Allowed || !est.Consent.OK {
		t.Fatalf("after the opt-out and Bob's consent naming HeyGen: %+v %v", est, err)
	}
}
