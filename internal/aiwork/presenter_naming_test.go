package aiwork_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeai"
	"github.com/metavert-io/metarush/internal/testutil/fakeheygen"
)

// A consent record MetaRush can't read just now (an unreadable document, a
// database that doesn't answer, a daemon stopping) is never taken for an
// ended one: nothing is retired or thrown away, and the work waits; a record
// that is really gone retires its presenters (D277).
func TestUnreadableConsentRetiresNothing(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in, ada, consent := e.presenterReady(t, "Ada Lovelace", "fake/fake-presenter-make")
	pr := e.makePresenter(t, in)
	line := e.sound(t, "ada line", 3)
	e.words(t, line, ada, strings.Fields("welcome to the garden"), func(i int) float64 { return float64(i) * 0.5 })
	clip := e.askTest(t, pr, line.ID)

	// The consent document can't be read (a field of the wrong type).
	if _, err := e.a.DB.C(store.CConsents).UpdateOne(ctx, bson.M{"_id": consent.ID}, bson.M{"$set": bson.M{"expiresAt": "not a time"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := generate.LoadConsent(ctx, e.a, consent.ID); generate.ConsentGone(err) || !generate.Unreadable(err) {
		t.Fatalf("an unreadable consent: %v", err)
	}
	if n := aiwork.Sweep(ctx, e.a); n != 0 {
		t.Fatalf("swept %d", n)
	}
	v, _ := call[aiwork.PresenterView](t, e.a, "presenter.show", aiwork.PresenterIDIn{ID: pr.Name}, false)
	if v.State == models.PresenterRetired || len(e.fake.Presenters()) != 1 {
		t.Fatalf("retired on a consent that couldn't be read: %+v", v.Presenter)
	}
	before := e.fake.Calls("submit")
	if _, err := e.run(t, clip.JobID); err == nil {
		t.Fatal("the job went on without its consent")
	}
	if g := e.gen(t, clip.Work.ID); g.Outcome != nil || e.fake.Calls("submit") != before {
		t.Fatalf("ended or sent on a consent that couldn't be read: %+v", g.Outcome)
	}
	// Readable again: the clip is made.
	exp := time.Now().AddDate(1, 0, 0).UTC()
	_, _ = e.a.DB.C(store.CConsents).UpdateOne(ctx, bson.M{"_id": consent.ID}, bson.M{"$set": bson.M{"expiresAt": exp}})
	if _, err := e.run(t, clip.JobID); err != nil {
		t.Fatal(err)
	}
	if g := e.gen(t, clip.Work.ID); g.Outcome == nil || g.Outcome.State != models.GenDone {
		t.Fatalf("once readable: %+v", g.Outcome)
	}
	// Really gone: retired.
	_, _ = e.a.DB.C(store.CConsents).DeleteOne(ctx, bson.M{"_id": consent.ID})
	if n := aiwork.Sweep(ctx, e.a); n == 0 {
		t.Fatal("a presenter whose consent is gone wasn't retired")
	}
	v, _ = call[aiwork.PresenterView](t, e.a, "presenter.show", aiwork.PresenterIDIn{ID: pr.Name}, false)
	if v.State != models.PresenterRetired || !strings.Contains(v.RetireNote, "is gone") {
		t.Fatalf("after the consent was deleted: %+v", v.Presenter)
	}
}

// Who named a face or a voice is recorded, and only a person's name counts
// for a presenter: an AI app naming the consent recording's face and voice
// doesn't make it the person's; a person naming them does, and the estimate
// says who did. Auto's names are recorded as Auto's (D274).
func TestOnlyNamesAPersonGaveCount(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in, eve, consent := e.presenterReady(t, "Eve Example", "fake/fake-presenter-make")
	rec := consent.RecordingID
	// The consent recording's voice and face, not named by anyone yet.
	_, _ = e.a.DB.C(store.CSpeakers).DeleteMany(ctx, bson.M{"assetId": rec})
	_ = e.a.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: e.a.WorkspaceID(), AssetID: rec, Label: "speaker_00", Seconds: 5})
	_, _ = e.a.DB.C(store.CFaces).DeleteMany(ctx, bson.M{"assetId": rec})
	var recAsset models.Asset
	_ = e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), rec, &recAsset)
	e.faceOf(t, &recAsset, "", "", nil)
	var face models.FaceTrack
	_ = e.a.DB.C(store.CFaces).FindOne(ctx, bson.M{"assetId": rec}).Decode(&face)

	nameBoth := func(a *app.App, caller ops.Caller) {
		t.Helper()
		if _, err := ops.CallTyped[ops.SpeakerRenameOut](ctx, a, "footage.speakers.rename", ops.SpeakerRenameIn{AssetID: rec, Speaker: "speaker_00", PersonID: eve.ID}, caller); err != nil {
			t.Fatal(err)
		}
		if _, err := ops.CallTyped[ops.FaceOut](ctx, a, "footage.faces.name", ops.FaceNameIn{FaceID: face.ID, PersonID: eve.ID}, caller); err != nil {
			t.Fatal(err)
		}
	}
	dry := func() *aiwork.PresenterOut {
		t.Helper()
		d := in
		d.DryRun = true
		out, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", d, true)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	// An AI app with change access names them as Eve.
	agent := *e.a
	agent.Actor.Via, agent.Actor.Agent = "mcp", "codex"
	nameBoth(&agent, ops.Caller{})
	var v models.AssetSpeaker
	_ = e.a.DB.C(store.CSpeakers).FindOne(ctx, bson.M{"assetId": rec, "label": "speaker_00"}).Decode(&v)
	if v.PersonID != eve.ID || v.Naming == nil || v.Naming.By != models.NamedByAgent || v.Naming.Actor.Agent != "codex" {
		t.Fatalf("the app's naming wasn't recorded as the app's: %+v", v.Naming)
	}
	out := dry()
	p := strings.Join(out.Estimate.Problems, " ")
	if out.Estimate.Allowed || !strings.Contains(p, "by an AI app (codex)") || !strings.Contains(p, "not by a person") {
		t.Fatalf("an app's names counted: allowed %v, %s", out.Estimate.Allowed, p)
	}

	// Auto's names are Auto's.
	auto := *e.a
	auto.Actor.Via = models.ViaAssistantAuto
	if _, err := ops.CallTyped[ops.SpeakerRenameOut](ctx, &auto, "footage.speakers.rename", ops.SpeakerRenameIn{AssetID: rec, Speaker: "speaker_00", PersonID: eve.ID}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	_ = e.a.DB.C(store.CSpeakers).FindOne(ctx, bson.M{"assetId": rec, "label": "speaker_00"}).Decode(&v)
	if v.Naming == nil || v.Naming.By != models.NamedByAuto {
		t.Fatalf("Auto's naming: %+v", v.Naming)
	}

	// A person names them: allowed, and the estimate says who named them.
	nameBoth(e.a, ops.Caller{Human: true})
	out = dry()
	notes := strings.Join(out.Estimate.Notes, " ")
	if !out.Estimate.Allowed || !strings.Contains(notes, "voice in the consent recording was named by") || !strings.Contains(notes, "face in the consent recording was named by") {
		t.Fatalf("a person's names: allowed %v, problems %v, notes %s", out.Estimate.Allowed, out.Estimate.Problems, notes)
	}
}

// A remake after a failure whose job ends before presenter.make has recorded
// it ends failed (not "training" for ever), and can be made again: the
// presenter is claimed before anything is sent (D277).
func TestRemakeAfterAFailureEndsFailed(t *testing.T) {
	e := setup(t)
	in, _, _ := e.presenterReady(t, "Rosa Pell", "fake/fake-presenter-make")
	makeIt := func(fn func(jobID string)) *aiwork.PresenterOut {
		t.Helper()
		d := in
		d.DryRun = true
		dry, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", d, true)
		if err != nil || !dry.Estimate.Allowed {
			t.Fatalf("dry: %+v %v", dry, err)
		}
		d.DryRun, d.MaxUSD = false, usd(dry.Estimate.USD)
		defer aiwork.SetMakeStarted(fn)()
		out, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", d, true)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	show := func() models.Presenter {
		v, _ := call[aiwork.PresenterView](t, e.a, "presenter.show", aiwork.PresenterIDIn{ID: in.ID}, false)
		return v.Presenter
	}
	e.fake.Script(fakeai.Behavior{Kind: fakeai.Fail})
	first := makeIt(nil)
	if _, err := e.run(t, first.JobID); err == nil {
		t.Fatal("the failed making reported success")
	}
	if pr := show(); pr.State != models.PresenterFailed {
		t.Fatalf("first: %s", pr.State)
	}
	// Made again; this job fails before presenter.make records it.
	e.fake.Script(fakeai.Behavior{Kind: fakeai.Fail})
	second := makeIt(func(jobID string) { _, _ = e.run(t, jobID) })
	pr := show()
	if pr.State != models.PresenterFailed || pr.TrainingID == "" || e.gen(t, pr.TrainingID).JobID != second.JobID {
		t.Fatalf("second: %s training %s", pr.State, pr.TrainingID)
	}
	// And again, this time made.
	third := makeIt(nil)
	if _, err := e.run(t, third.JobID); err != nil {
		t.Fatal(err)
	}
	if pr := show(); pr.State != models.PresenterDraft {
		t.Fatalf("third: %s", pr.State)
	}
}

// A clip whose answer was lost and that isn't at the service (yet) is looked
// for again for a while, then recorded as not found — never as deleted
// (D277). The sound it uploaded is deleted at once.
func TestLostClipNotFoundIsLookedForAgain(t *testing.T) {
	e := setup(t)
	key := "hg" + strings.Repeat("Nf4", 11) + "fake"
	fake := fakeheygen.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvHeyGenBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.HeyGenName), key)
	line := e.sound(t, "a plain line", 3)
	out := e.ask(t, aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:lk_amber_studio", Model: "heygen/avatar-iv", Clip: line.ID})
	fake.Next(fakeheygen.GarbledUnmade)
	if _, err := e.run(t, out.JobID); err == nil || !strings.Contains(err.Error(), "looks for it again") {
		t.Fatalf("not found yet: %v", err)
	}
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Forgotten != nil || fake.Assets() != 0 {
		t.Fatalf("after one look: outcome %+v forgotten %+v sound kept %d", g.Outcome, g.Forgotten, fake.Assets())
	}
	old := aiwork.LostVoiceLookFor
	aiwork.LostVoiceLookFor = 0
	t.Cleanup(func() { aiwork.LostVoiceLookFor = old })
	_, _ = e.run(t, out.JobID)
	g = e.gen(t, out.Work.ID)
	if g.Forgotten == nil || g.Forgotten.OK || !strings.Contains(g.Forgotten.Detail, "found none") || fake.Calls("POST /v3/videos") != 1 {
		t.Fatalf("after looking: %+v (asked %d)", g.Forgotten, fake.Calls("POST /v3/videos"))
	}
}

// A consent's last day ends at midnight in the time zone recorded with it,
// not in UTC on whichever computer looks (D274).
func TestConsentEndsAtTheEndOfItsDayWhereItWasGiven(t *testing.T) {
	e := setup(t)
	rec := func(zone string) (*generate.ConsentOut, error) {
		return call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Grace Hopper", Self: true, Adult: true, ExpiresOn: "2030-01-15", TimeZone: zone,
			Statement: "I, Grace Hopper, agree to generated shots of me for these videos."}, true)
	}
	out, err := rec("America/Los_Angeles")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2030, 1, 16, 7, 59, 59, 0, time.UTC)
	if c := out.Consent.Consent; c.ExpiresAt == nil || !c.ExpiresAt.Equal(want) || c.ExpiresZone != "America/Los_Angeles" || !strings.Contains(out.Summary, "15 Jan 2030 (America/Los_Angeles)") {
		t.Fatalf("Los Angeles: %v %q %s", c.ExpiresAt, c.ExpiresZone, out.Summary)
	}
	out, err = rec("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2030, 1, 15, 14, 59, 59, 0, time.UTC); !out.Consent.Consent.ExpiresAt.Equal(want) {
		t.Fatalf("Tokyo: %v", out.Consent.Consent.ExpiresAt)
	}
	if _, err := rec("Nowhere/Atlantis"); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("an unknown zone: %v", err)
	}
}

// Combining two people names their voices anew, as whoever combined them
// (D274): an AI app merging Bob into Eve doesn't make Bob's voice in Eve's
// consent recording a voice a person named as Eve.
func TestAnAppMergingPeopleDoesntNameVoices(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in, _, consent := e.presenterReady(t, "Eve Example", "fake/fake-presenter-make")
	rec := consent.RecordingID
	// Bob speaks in Eve's consent recording, named as Bob by a person.
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Bob Baker", Self: true, Adult: true,
		Statement: "I, Bob Baker, agree to generated shots of me for these videos."}, true); err != nil {
		t.Fatal(err)
	}
	var bob models.Person
	_ = e.a.DB.C(store.CPeople).FindOne(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "name": "Bob Baker"}).Decode(&bob)
	_ = e.a.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: e.a.WorkspaceID(), AssetID: rec, Label: "speaker_01",
		Name: bob.Name, PersonID: bob.ID, Source: models.SpeakerByPerson, Naming: byPerson(e), Seconds: 3, First: 20})
	_ = e.a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: e.a.WorkspaceID(), AssetID: rec, From: 20, To: 22,
		Text: "hello from bob", Speaker: "speaker_01", Words: []models.Word{{Text: "hello", From: 20, To: 20.4}, {Text: "from", From: 20.5, To: 20.8}, {Text: "bob", From: 21, To: 21.4}},
		Provider: "test"})
	dry := func() *aiwork.PresenterOut {
		t.Helper()
		d := in
		d.DryRun = true
		out, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", d, true)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := dry(); out.Estimate.Allowed || !strings.Contains(strings.Join(out.Estimate.Problems, " "), "Someone other than Eve Example speaks") {
		t.Fatalf("Bob speaks: %v", out.Estimate.Problems)
	}
	// An AI app says Bob is really Eve.
	agent := *e.a
	agent.Actor.Via, agent.Actor.Agent = "mcp", "codex"
	if _, err := ops.CallTyped[ops.PersonOut](ctx, &agent, "people.merge", ops.PersonMergeIn{From: bob.ID, Into: "Eve Example"}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	out := dry()
	p := strings.Join(out.Estimate.Problems, " ")
	if out.Estimate.Allowed || !strings.Contains(p, "by an AI app (codex)") || !strings.Contains(p, "not by a person") {
		t.Fatalf("an app's merge counted as a person's naming: allowed %v, %s", out.Estimate.Allowed, p)
	}
}

// Lip sync counts only names a person gave (D274, D279): an AI app naming the
// face, or the voice, in the part it changes doesn't make it the consenting
// person's.
func TestLipSyncCountsOnlyAPersonsNames(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	clip, sound := e.footage(t, "interview", 4), e.sound(t, "new line", 3)
	in := aiwork.MakeIn{Kind: aiservices.KindLipSync, ProjectID: e.project, Model: "fake/fake-lip-sync", Clip: clip.ID, Sound: sound.ID}
	rosa := e.consentFor(t, "Rosa Diaz", []string{models.ConsentUseLipSync}, []string{fakeai.Name})
	e.speaks(t, clip, rosa)
	e.speaks(t, sound, rosa)
	e.looked(t, clip)
	e.face(t, clip, 0, 4, nil)
	facesAt(t, func(float64) int { return 1 })
	var face models.FaceTrack
	_ = e.a.DB.C(store.CFaces).FindOne(ctx, bson.M{"assetId": clip.ID}).Decode(&face)
	agent := *e.a
	agent.Actor.Via, agent.Actor.Agent = "mcp", "codex"

	// An AI app names the face as Rosa: refused.
	if _, err := ops.CallTyped[ops.FaceOut](ctx, &agent, "footage.faces.name", ops.FaceNameIn{FaceID: face.ID, PersonID: rosa.ID}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if got := lipProblems(t, e, in); !strings.Contains(got, "by an AI app (codex)") || !strings.Contains(got, "not by a person") {
		t.Fatalf("an app named the face: %q", got)
	}
	// A person names it: allowed.
	if _, err := ops.CallTyped[ops.FaceOut](ctx, e.a, "footage.faces.name", ops.FaceNameIn{FaceID: face.ID, PersonID: rosa.ID}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if got := lipProblems(t, e, in); got != "" {
		t.Fatalf("a person named the face: %q", got)
	}
	// An AI app names the voice in the footage: refused again.
	if _, err := ops.CallTyped[ops.SpeakerRenameOut](ctx, &agent, "footage.speakers.rename", ops.SpeakerRenameIn{AssetID: clip.ID, Speaker: "speaker_00", PersonID: rosa.ID}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if got := lipProblems(t, e, in); !strings.Contains(got, "hasn't been named by a person") || !strings.Contains(got, "AI app (codex)") {
		t.Fatalf("an app named the voice: %q", got)
	}
}
