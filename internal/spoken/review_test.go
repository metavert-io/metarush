package spoken_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/receipt"
	"github.com/metavert-io/metarush/internal/reviewlinks"
	"github.com/metavert-io/metarush/internal/spoken"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeai"
	"github.com/metavert-io/metarush/internal/voice"
)

// New words are said in a copy of someone's voice, so a consent for new
// words without one for their voice covers nothing: it can't be recorded, and
// one that says so anyway (made before this rule) isn't enough when the
// consent behind the voice copy has ended.
func TestNewWordsNeedConsentForTheVoiceToo(t *testing.T) {
	e := setup(t)
	e.voiceCopy(t, nil) // consent for shots and voice; the voice copy is made with it
	e.clipWith(t, models.SpeakerByPerson, models.NamedByPerson)
	e.cut(t)
	ctx := context.Background()
	_, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Ada Lovelace", Self: true, Adult: true,
		Statement: "I, Ada Lovelace, agree to a word I said being changed in my voice copy.", Uses: []string{models.ConsentUseNewWords}, Providers: []string{fakeai.Name}}, true)
	if codeOf(err) != apperr.InvalidInput || !strings.Contains(apperr.From(err).Message, "voice") {
		t.Fatalf("a consent for new words that leaves out their voice must be refused: %v", err)
	}
	// One recorded before the rule: new words only. The voice copy's own consent has ended.
	past := time.Now().Add(-time.Hour)
	_ = e.a.DB.Update(ctx, store.CConsents, e.a.WorkspaceID(), e.consent.ID, bson.M{"$set": bson.M{"expiresAt": past}})
	words := models.Consent{ID: ids.New(ids.Consent), WorkspaceID: e.a.WorkspaceID(), PersonID: e.person.ID, PersonName: e.person.Name, Self: true,
		Statement: "I agree to new words.", Scope: models.ConsentScope{AllProjects: true}, Uses: []string{models.ConsentUseNewWords},
		Providers: []string{fakeai.Name}, Adult: true, RecordedAt: time.Now().UTC()}
	if err := e.a.DB.Insert(ctx, store.CConsents, words); err != nil {
		t.Fatal(err)
	}
	est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", twelve(e), false)
	if est.Allowed || est.Code != "consent" {
		t.Fatalf("new words without consent for the voice must be refused: %+v", est)
	}
}

// Work can't pass itself off as a change's speech: an AI request that names
// the change as where it came from is kept as ordinary AI speech, and the
// change only takes the speech it asked for itself.
func TestOnlyTheChangesOwnSpeechIsTaken(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	in := twelve(e)
	est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", in, false)
	in.MaxUSD = usd(est.USD)
	e.heard.context, e.heard.words = est.Context, "fifteen"
	mo := mustCall[spoken.SayMakeOut](t, e.a, "cut.say.make", in, true)
	// Someone else's request (not a person's), claiming to be the change's.
	g := models.Generation{}
	if err := e.a.DB.GetByID(context.Background(), store.CGenerations, e.a.WorkspaceID(), e.copyGen, &g); err != nil {
		t.Fatal(err)
	}
	voice := ""
	for _, o := range g.Outcome.Outputs {
		voice = firstOf(voice, o.VoiceID)
	}
	forged := aiwork.MakeIn{Kind: "voice", ProjectID: e.project, Model: est.Model, Prompt: est.Context, Options: models.GenOptions{Voice: voice},
		MaxUSD: usd(1), Origin: spoken.Origin + mo.Change.ID}
	fo := mustCall[aiwork.MakeOut](t, e.a, "ai.make", forged, false)
	e.runJob(t, fo.JobID)
	v := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: mo.Change.ID}, false)
	if v.State != models.SpokenMaking || v.Sound != "" {
		t.Fatalf("a request that only names the change was taken as its speech: %+v %s", v.State, v.Sound)
	}
	// Its own speech is taken.
	e.runJob(t, mo.JobID)
	if v := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: mo.Change.ID}, false); v.State != models.SpokenReady {
		t.Fatalf("the change's own speech: %+v %s", v.State, v.Why)
	}
}

func firstOf(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// A change's state only moves on from what it was: a consent withdrawn while
// the new words are checked keeps the change withdrawn (no sound kept), and a
// sweep that read a change as lost before another computer finished it
// leaves it as that computer left it.
func TestStateMovesOnOnlyFromWhereItWas(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	ctx := context.Background()
	ask := func() spoken.SayMakeOut {
		in := twelve(e)
		est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", in, false)
		in.MaxUSD = usd(est.USD)
		e.heard.context, e.heard.words = est.Context, "fifteen"
		return *mustCall[spoken.SayMakeOut](t, e.a, "cut.say.make", in, true)
	}
	// 1. The sweep read it as lost; meanwhile another computer finished it.
	first := ask()
	_ = e.a.DB.Update(ctx, store.CJobs, e.a.WorkspaceID(), first.JobID, bson.M{"$set": bson.M{"state": models.JobSucceeded}})
	_ = e.a.DB.Update(ctx, store.CSpokenChanges, e.a.WorkspaceID(), first.Change.ID, bson.M{"$set": bson.M{"updatedAt": time.Now().Add(-time.Hour)}})
	restore := spoken.SetSweepPause(func() {
		_ = e.a.DB.Update(ctx, store.CSpokenChanges, e.a.WorkspaceID(), first.Change.ID, bson.M{"$set": bson.M{"state": models.SpokenReady, "sound": "ast_01jabcdefghjkmnpqrstvwxyz0", "updatedAt": time.Now().UTC()}})
	})
	spoken.Sweep(ctx, e.a)
	restore()
	if v := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: first.Change.ID}, false); v.State != models.SpokenReady {
		t.Fatalf("the sweep overwrote a change another computer finished: %s (%s)", v.State, v.Why)
	}
	// 2. The consent is withdrawn while the new words are being checked.
	second := ask()
	e.heard.onHear = func() {
		mustCall[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: e.consent.ID}, true)
	}
	e.runJob(t, second.JobID)
	v := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: second.Change.ID}, false)
	if v.State != models.SpokenWithdrawn || v.Sound != "" {
		t.Fatalf("a change withdrawn during its check must stay withdrawn, with no sound kept: %s %q", v.State, v.Sound)
	}
	// 3. A job that couldn't be read just now is still running, not gone.
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if why := spoken.Stopped(gone, e.a, "", "job_01jabcdefghjkmnpqrstvwxyz0", false); why != "" {
		t.Fatalf("a job that couldn't be read was taken as gone: %q", why)
	}
}

// Changed words go into a cut only through Put it in the cut (or, by hand, by
// a person whose change still holds): an edit by anything else that adds them
// back is refused, and once the consent is withdrawn nobody can.
func TestChangedWordsGoInOnlyThroughAPerson(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	v := e.makeIt(t)
	place := ops.CutSayPlaceIn{ID: v.ID, NoRender: true}
	place.ConfirmToken = mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.place", withDry(place), true).Check.ConfirmToken
	po := mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.place", place, true)
	said := mustCall[ops.RevisionShowOut](t, e.a, "revision.show", ops.RevisionShowIn{ID: po.Applied.Revision.ID}, false).Spec.Mix.Said[0]
	undo := mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", ops.CutSayPlaceIn{ID: v.ID, NoRender: true, DryRun: true}, false)
	uo := mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", ops.CutSayPlaceIn{ID: v.ID, NoRender: true, ConfirmToken: undo.Check.ConfirmToken}, false)
	again := func(human bool) error {
		plan := map[string]any{"projectId": e.project, "baseRevisionId": uo.Applied.Revision.ID,
			"ops": []map[string]any{{"op": "setMix", "mix": map[string]any{"said": []editspec.SaidSound{said}}}}}
		raw, _ := json.Marshal(plan)
		chk, err := call[ops.EditCheckOut](t, e.a, "edit.check", ops.EditCheckIn{Plan: raw}, human)
		if err != nil {
			return err
		}
		_, err = call[ops.EditApplyOut](t, e.a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: chk.ConfirmToken}, human)
		return err
	}
	if err := again(false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("an edit that isn't a person's put the changed words back: %v", err)
	}
	mustCall[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: e.consent.ID}, true)
	if err := again(true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("changed words whose consent was withdrawn went back in: %v", err)
	}
	if w := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: v.ID}, false); w.InCut {
		t.Fatal("the cut plays changed words whose consent was withdrawn")
	}
}

func withDry(in ops.CutSayPlaceIn) ops.CutSayPlaceIn {
	in.DryRun = true
	return in
}

// finalRender stores a finished final render of a version (random bytes).
func (e *env) finalRender(t *testing.T, revisionID string) {
	t.Helper()
	ctx := context.Background()
	var r models.Revision
	if err := e.a.DB.GetByID(ctx, store.CRevisions, e.a.WorkspaceID(), revisionID, &r); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4096)
	_, _ = rand.Read(b)
	p := filepath.Join(t.TempDir(), "final.mp4")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(e.a.WorkspaceID(), sha)
	if _, err := e.a.Media.PutFile(ctx, key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: e.a.WorkspaceID(), ProjectID: r.ProjectID, RevisionID: r.ID, RevisionNumber: r.Number, SpecHash: r.SpecHash,
		Preset: r.Spec.Output.Preset, Quality: "final", State: models.RenderAvailable, CreatedAt: now, CompletedAt: &now,
		Output: &models.RenderOutput{StorageKey: key, SHA256: sha, Size: int64(len(b)), Duration: r.Duration, Width: 1920, Height: 1080, FPS: 30}}
	if err := e.a.DB.Insert(ctx, store.CRenders, rd); err != nil {
		t.Fatal(err)
	}
}

func (e *env) placeIt(t *testing.T, id string) *ops.CutSayPlaceOut {
	t.Helper()
	place := ops.CutSayPlaceIn{ID: id, NoRender: true}
	place.ConfirmToken = mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.place", withDry(place), true).Check.ConfirmToken
	return mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.place", place, true)
}

func consentHold(g *ops.ShipGateOut) *ops.GateHold {
	for i := range g.Holds {
		if g.Holds[i].Kind == "consent" {
			return &g.Holds[i]
		}
	}
	return nil
}

// Changed words whose consent was withdrawn never go out as if it held: the
// version is held back from delivery (with the ship gate off too), nobody
// can send it out anyway, and its captions file no longer says "with their
// consent" but that the consent is no longer in force.
func TestWithdrawnConsentHoldsTheVersionBack(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	v := e.makeIt(t)
	rev := e.placeIt(t, v.ID).Applied.Revision.ID
	e.finalRender(t, rev)
	ready := func() *ops.ShipGateOut {
		return mustCall[ops.ShipGateOut](t, e.a, "delivery.ready", ops.ShipGateIn{ProjectID: e.project}, false)
	}
	vtt := func() string {
		return mustCall[ops.CaptionsFileOut](t, e.a, "captions.file", ops.CaptionsFileIn{ProjectID: e.project, Format: "vtt"}, false).Content
	}
	deliver := func() error {
		_, err := call[ops.DeliverOut](t, e.a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rev}, Out: t.TempDir(), AllowDraft: true}, true)
		return err
	}
	if g := ready(); consentHold(g) != nil {
		t.Fatalf("their consent holds, so it holds nothing back: %+v", g.Holds)
	}
	if n := vtt(); !strings.Contains(n, "with their consent") {
		t.Fatalf("the captions file names the changed words: %s", n)
	}
	if err := deliver(); err != nil {
		t.Fatalf("it goes out while their consent holds: %v", err)
	}
	mustCall[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: e.consent.ID}, true)
	g := ready()
	h := consentHold(g)
	if g.On || h == nil || h.Overridable || g.Ready || !strings.Contains(h.Says, "“fifteen”") || !strings.Contains(h.Says, "no longer") && !strings.Contains(h.Says, "withdrawn") {
		t.Fatalf("a version playing words whose consent was withdrawn must be held back, with the gate off too: %+v %+v", g, h)
	}
	if err := deliver(); codeOf(err) != apperr.Held {
		t.Fatalf("it went out with words whose consent was withdrawn: %v", err)
	}
	if _, err := call[ops.ShipGateOut](t, e.a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: e.project}, Note: "the client wants it today", Holds: []string{h.Key}}, true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("nobody sends it out anyway past a withdrawn consent: %v", err)
	}
	if n := vtt(); strings.Contains(n, "with their consent") || !strings.Contains(n, "no longer in force") {
		t.Fatalf("the captions file still says their consent holds: %s", n)
	}
}

// A mouth matched to the new words is shown only while everyone's consent
// for lip sync holds: put in the cut after it ended, the change plays the
// recording's own picture (and says why); in a cut when it ends, the version
// is held back.
func TestAMouthWhoseConsentEndedIsNotShown(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords, models.ConsentUseLipSync})
	ctx := context.Background()
	v := e.makeIt(t)
	mouth := func(t *testing.T) (string, string) {
		secs := v.Fit.To - v.Fit.From
		p := filepath.Join(t.TempDir(), "mouth.mp4")
		if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=160x90:rate=24:duration=%.3f", secs),
			"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=330:duration=%.3f", secs), "-shortest", "-pix_fmt", "yuv420p", "-c:a", "aac", p).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v %s", err, out)
		}
		res := ingest.File(ctx, e.a, p, ingest.Options{ProjectID: e.project, FootageOnly: true})
		// Ada's own consent for lip sync, recorded apart from the one for new words.
		lips := models.Consent{ID: ids.New(ids.Consent), WorkspaceID: e.a.WorkspaceID(), PersonID: e.person.ID, PersonName: e.person.Name, Self: true,
			Statement: "I agree to lip sync.", Scope: models.ConsentScope{AllProjects: true}, Uses: []string{models.ConsentUseLipSync},
			Providers: []string{fakeai.Name}, Adult: true, RecordedAt: time.Now().UTC()}
		g := models.Generation{ID: ids.New(ids.Generation), WorkspaceID: e.a.WorkspaceID(), ProjectID: e.project, Kind: "lip-sync", Origin: spoken.MouthOrigin + v.ID,
			ConsentIDs: []string{lips.ID}, CreatedAt: time.Now().UTC()}
		if err := e.a.DB.Insert(ctx, store.CConsents, lips); err != nil {
			t.Fatal(err)
		}
		if err := e.a.DB.Insert(ctx, store.CGenerations, g); err != nil {
			t.Fatal(err)
		}
		_ = e.a.DB.Update(ctx, store.CSpokenChanges, e.a.WorkspaceID(), v.ID, bson.M{"$set": bson.M{"mouth": models.SpokenMouth{GenerationID: g.ID, AssetID: res.AssetID, State: models.SpokenReady}}})
		return res.AssetID, lips.ID
	}
	// 1. Its consent ended before it went in (no word came of it here).
	asset, lips := mouth(t)
	_ = e.a.DB.Update(ctx, store.CConsents, e.a.WorkspaceID(), lips, bson.M{"$set": bson.M{"expiresAt": time.Now().Add(-time.Minute)}})
	po := e.placeIt(t, v.ID)
	spec := mustCall[ops.RevisionShowOut](t, e.a, "revision.show", ops.RevisionShowIn{ID: po.Applied.Revision.ID}, false).Spec
	for _, c := range spec.Clips {
		if c.AssetID == asset {
			t.Fatalf("a mouth whose consent ended went into the cut: %+v", spec.Clips)
		}
	}
	if spec.Mix.Said[0].Picture != "" || !strings.Contains(po.Summary, "mouth isn't matched") {
		t.Fatalf("the change plays without the matched mouth, and says why: %+v %q", spec.Mix.Said[0], po.Summary)
	}
	undo := ops.CutSayPlaceIn{ID: v.ID, NoRender: true}
	undo.ConfirmToken = mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", withDry(undo), false).Check.ConfirmToken
	mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", undo, false)
	// 2. Its consent is withdrawn while the cut shows it.
	asset, lips = mouth(t)
	po = e.placeIt(t, v.ID)
	if !strings.Contains(po.Summary, "mouth matches") {
		t.Fatalf("the matched mouth goes in while its consent holds: %q", po.Summary)
	}
	if g := mustCall[ops.ShipGateOut](t, e.a, "delivery.ready", ops.ShipGateIn{ProjectID: e.project}, false); consentHold(g) != nil {
		t.Fatalf("every consent holds: %+v", g.Holds)
	}
	mustCall[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: lips}, true)
	g := mustCall[ops.ShipGateOut](t, e.a, "delivery.ready", ops.ShipGateIn{ProjectID: e.project}, false)
	if h := consentHold(g); h == nil || !strings.Contains(h.Says, "mouth") {
		t.Fatalf("a version showing a mouth whose consent was withdrawn must be held back: %+v", g.Holds)
	}
	if w := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: v.ID}, false); w.Mouth == nil || w.Mouth.State != models.SpokenWithdrawn {
		t.Fatalf("the change's mouth is marked withdrawn: %+v", w.Mouth)
	}
}

// A cut that plays another project's cut with changed words in it discloses
// them like its own: the export's notes list them (where each is heard), its
// captions note names them, the receipt counts them in the version, and when
// the consent is withdrawn this version is held back too.
func TestChangesInANestedCutAreDisclosed(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	ctx := context.Background()
	v := e.makeIt(t)
	inner := e.placeIt(t, v.ID).Applied.Revision.ID
	// "Launch film" plays the garden cut from 0:01.
	outer := ids.New(ids.Project)
	now := time.Now().UTC()
	if err := e.a.DB.Insert(ctx, store.CProjects, models.Project{ID: outer, WorkspaceID: e.a.WorkspaceID(), Name: "Launch film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mustCall[ops.ProjectLinkOut](t, e.a, "project.link", ops.ProjectLinkIn{ProjectID: outer, From: e.project}, true)
	plan := fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"intro","assetId":%q,"in":0,"out":1},{"id":"garden","assetId":%q,"in":0,"out":8}]}}`,
		outer, e.clip.ID, editspec.NestedKey(e.project, inner))
	chk := mustCall[ops.EditCheckOut](t, e.a, "edit.check", ops.EditCheckIn{Plan: json.RawMessage(plan)}, false)
	rev := mustCall[ops.EditApplyOut](t, e.a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken}, true).Revision.ID
	e.finalRender(t, rev)
	dir := t.TempDir()
	out := mustCall[ops.DeliverOut](t, e.a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rev}, Out: dir, AllowDraft: true}, true)
	b, err := os.ReadFile(out.Files[0].Sidecar)
	if err != nil {
		t.Fatal(err)
	}
	var sc ops.DeliverySidecar
	_ = json.Unmarshal(b, &sc)
	if len(sc.ChangedWords) != 1 || sc.ChangedWords[0].Words != "fifteen" || !sc.ChangedWords[0].In || sc.ChangedWords[0].At < 1 || sc.ChangedWords[0].Sound != v.Sound {
		t.Fatalf("the export's notes list the nested cut's changed words, where they're heard: %+v", sc.ChangedWords)
	}
	if vtt := mustCall[ops.CaptionsFileOut](t, e.a, "captions.file", ops.CaptionsFileIn{ProjectID: outer, Format: "vtt"}, false).Content; !strings.Contains(vtt, "“fifteen” in place of “twelve”") {
		t.Fatalf("the captions note names them: %s", vtt)
	}
	rc := mustCall[receipt.ProjectReceiptOut](t, e.a, "project.receipt", receipt.ProjectReceiptIn{ProjectID: outer}, false)
	found := false
	for _, g := range rc.Generated {
		found = found || (g.AssetID == v.Sound && g.InVersion && strings.Contains(g.What, "“fifteen”"))
	}
	if !found {
		t.Fatalf("the receipt counts the nested cut's changed words in the version: %+v", rc.Generated)
	}
	mustCall[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: e.consent.ID}, true)
	g := mustCall[ops.ShipGateOut](t, e.a, "delivery.ready", ops.ShipGateIn{ProjectID: outer}, false)
	if h := consentHold(g); h == nil || !strings.Contains(h.Says, "a cut this one plays") {
		t.Fatalf("a version playing a nested cut's changed words whose consent was withdrawn is held back: %+v", g.Holds)
	}
}

// A change goes into the cut only while a person still names its voice as
// the same person: renamed by an AI app since, it's refused until a person
// names it again.
func TestTheVoiceMustStillBeNamedByAPerson(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	ctx := context.Background()
	v := e.makeIt(t)
	byAgent := bson.M{"$set": bson.M{"naming": models.Naming{By: models.NamedByAgent, At: time.Now().UTC()}}}
	if _, err := e.a.DB.C(store.CSpeakers).UpdateMany(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "assetId": e.clip.ID}, byAgent); err != nil {
		t.Fatal(err)
	}
	place := ops.CutSayPlaceIn{ID: v.ID, NoRender: true, DryRun: true}
	if _, err := call[ops.CutSayPlaceOut](t, e.a, "cut.say.place", place, true); codeOf(err) != apperr.Conflict || !strings.Contains(apperr.From(err).Message, "by a person") {
		t.Fatalf("a voice an AI app named since must be named again by a person: %v", err)
	}
	byPerson := bson.M{"$set": bson.M{"naming": models.Naming{By: models.NamedByPerson, At: time.Now().UTC()}}}
	_, _ = e.a.DB.C(store.CSpeakers).UpdateMany(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "assetId": e.clip.ID}, byPerson)
	if _, err := call[ops.CutSayPlaceOut](t, e.a, "cut.say.place", place, true); err != nil {
		t.Fatalf("named again by a person, it goes in: %v", err)
	}
}

// The voice bars at their edges: speech of 3 s needs 50% alike, 2 s 45%,
// 1 s 40%, and anything shorter can't be told (refused); the voice where the
// words are passes at exactly 40% like the person's other recordings. The
// new words' own stretch is at least 1.5 s of the speech made, centred on
// them and inside it.
func TestVoiceBarsAtTheirEdges(t *testing.T) {
	for _, c := range []struct{ secs, need float64 }{{3, 0.50}, {2.999, 0.45}, {2, 0.45}, {1.999, 0.40}, {1, 0.40}} {
		if got := spoken.VoiceNeeded(c.secs); got != c.need {
			t.Errorf("%.3f s needs %.2f, got %.2f", c.secs, c.need, got)
		}
	}
	if got := spoken.VoiceNeeded(0.999); got <= 1 {
		t.Errorf("under a second can't be told: %v", got)
	}
	if spoken.MatchTooLow(spoken.MatchNeeded) || !spoken.MatchTooLow(spoken.MatchNeeded-0.001) {
		t.Error("exactly the bar passes; just under it doesn't")
	}
	for _, c := range []struct{ from, to, secs, w0, w1 float64 }{
		{2, 2.4, 6, 1.45, 2.95}, // a lone word: 1.5 s around it
		{0.1, 0.5, 6, 0, 1.5},   // at the start: kept inside the speech
		{5.6, 5.9, 6, 4.5, 6},   // at the end
		{1, 4, 6, 1, 4},         // longer than the window: the words themselves
		{0.2, 0.6, 1.2, 0, 1.2}, // the speech is shorter than the window: all of it
	} {
		w0, w1 := spoken.WordsWindowOf(c.from, c.to, c.secs)
		if math.Abs(w0-c.w0) > 1e-9 || math.Abs(w1-c.w1) > 1e-9 {
			t.Errorf("words %.2f–%.2f of %.1f s: %.2f–%.2f, want %.2f–%.2f", c.from, c.to, c.secs, w0, w1, c.w0, c.w1)
		}
	}
}

// The new words can't pass the voice check on the words around them: when
// the speech made sounds like the person overall but the stretch of the new
// words doesn't, the change fails and says so.
func TestTheNewWordsThemselvesMustSoundLikeThePerson(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	spoken.Embed = func(_ context.Context, _ *app.App, pcm []float32) ([]float32, error) {
		if float64(len(pcm))/voice.SampleRate <= spoken.WordsWindow+0.01 {
			return otherVoice(), nil // the new words' own stretch
		}
		return adaVoice(), nil
	}
	v := e.makeIt(t)
	if v.State != models.SpokenFailed || v.Check == nil || v.Check.WordsVoice == nil || !strings.Contains(v.Why, "new words themselves") {
		t.Fatalf("the new words passed on the words around them: %s %q %+v", v.State, v.Why, v.Check)
	}
}

// Undo takes changed words out of one cut: another cut that still plays them
// keeps them (the change stays placed, the clip's transcript still marks
// them, and undo with no cut named takes them out of the cut that plays them).
func TestUndoInOneCutLeavesTheOtherAsItIs(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	v := e.makeIt(t)
	head := e.placeIt(t, v.ID).Applied.Revision.ID
	// A second cut, "short", made from main with the changed words in it.
	plan := fmt.Sprintf(`{"projectId":%q,"branch":"short","baseRevisionId":%q,"ops":[{"op":"setTitle","text":"Short"}]}`, e.project, head)
	chk := mustCall[ops.EditCheckOut](t, e.a, "edit.check", ops.EditCheckIn{Plan: json.RawMessage(plan)}, true)
	mustCall[ops.EditApplyOut](t, e.a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken}, true)
	undo := func(branch string) {
		t.Helper()
		in := ops.CutSayPlaceIn{ID: v.ID, Branch: branch, NoRender: true}
		in.ConfirmToken = mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", withDry(in), false).Check.ConfirmToken
		mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", in, false)
	}
	undo("main")
	w := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: v.ID}, false)
	if w.State != models.SpokenPlaced || !w.InCut || len(w.Branches) != 1 || w.Branches[0] != "short" {
		t.Fatalf("the short cut still plays the changed words: %s %v %v", w.State, w.InCut, w.Branches)
	}
	tr := mustCall[ops.TranscriptOut](t, e.a, "footage.transcript", ops.TranscriptIn{AssetID: e.clip.ID}, false)
	if len(tr.Said) != 1 || !tr.Said[0].InCut {
		t.Fatalf("the clip's transcript still marks the words the short cut says: %+v", tr.Said)
	}
	undo("") // the cut that plays them
	if w := mustCall[ops.SpokenView](t, e.a, "cut.say.show", ops.CutSayIDIn{ID: v.ID}, false); w.State != models.SpokenUndone || w.InCut {
		t.Fatalf("taken out of every cut: %s %v", w.State, w.InCut)
	}
}

func applyPlan(t *testing.T, e *env, plan string, human bool) (*ops.EditApplyOut, error) {
	t.Helper()
	chk, err := call[ops.EditCheckOut](t, e.a, "edit.check", ops.EditCheckIn{Plan: json.RawMessage(plan)}, human)
	if err != nil {
		return nil, err
	}
	if len(chk.Errors) > 0 {
		return nil, fmt.Errorf("check errors: %+v", chk.Errors)
	}
	return call[ops.EditApplyOut](t, e.a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken}, human)
}

// saveAsIs saves a version of the cut as written, past every check (as a
// version saved before a rule existed would be), as the cut's latest.
func (e *env) saveAsIs(t *testing.T, edit func(s *editspec.Spec)) string {
	t.Helper()
	ctx := context.Background()
	var p models.Project
	if err := e.a.DB.GetByID(ctx, store.CProjects, e.a.WorkspaceID(), e.project, &p); err != nil {
		t.Fatal(err)
	}
	var r models.Revision
	if err := e.a.DB.GetByID(ctx, store.CRevisions, e.a.WorkspaceID(), p.Branches["main"].Head, &r); err != nil {
		t.Fatal(err)
	}
	spec := editspec.Clone(&r.Spec)
	edit(spec)
	r.ID, r.Number, r.ParentID, r.Spec, r.SpecHash, r.CreatedAt = ids.New(ids.Revision), r.Number+100, r.ID, *spec, editspec.Hash(spec), time.Now().UTC()
	if err := e.a.DB.Insert(ctx, store.CRevisions, r); err != nil {
		t.Fatal(err)
	}
	if err := e.a.DB.Update(ctx, store.CProjects, e.a.WorkspaceID(), e.project, bson.M{"$set": bson.M{"branches.main.head": r.ID}}); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

// The new words play only where they replace what was said: their sound as
// an ordinary track, or the matched mouth anywhere but over those words, is
// refused for anyone; and a version that plays them some other way anyway
// (saved before this rule) is disclosed and held back like any other once
// the consent is withdrawn.
func TestNewWordsPlayOnlyWhereTheyReplaceWhatWasSaid(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	v := e.makeIt(t)
	track := fmt.Sprintf(`{"projectId":%q,"baseRevisionId":%q,"ops":[{"op":"addAudio","track":{"id":"newwords","assetId":%q,"role":"dialogue","in":0,"out":%.3f,"at":%.3f}}]}`,
		e.project, e.head, v.Sound, v.Fit.To-v.Fit.From, v.Fit.From)
	for _, human := range []bool{false, true} {
		if _, err := applyPlan(t, e, track, human); codeOf(err) != apperr.InvalidInput {
			t.Fatalf("the new words' sound went in as an ordinary track (a person: %v): %v", human, err)
		}
	}
	// The matched mouth as an ordinary clip.
	mouth := e.videoClip(t, v.Fit.To-v.Fit.From)
	_ = e.a.DB.Update(context.Background(), store.CSpokenChanges, e.a.WorkspaceID(), v.ID, bson.M{"$set": bson.M{"mouth": models.SpokenMouth{AssetID: mouth, State: models.SpokenReady}}})
	clip := fmt.Sprintf(`{"projectId":%q,"baseRevisionId":%q,"ops":[{"op":"insertClip","clip":{"id":"m","assetId":%q,"in":0,"out":0.5}}]}`, e.project, e.head, mouth)
	if _, err := applyPlan(t, e, clip, true); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("the matched mouth went in away from the new words: %v", err)
	}
	_ = e.a.DB.Update(context.Background(), store.CSpokenChanges, e.a.WorkspaceID(), v.ID, bson.M{"$unset": bson.M{"mouth": ""}})
	// A version that plays the sound as a track anyway.
	rev := e.saveAsIs(t, func(s *editspec.Spec) {
		s.Audio = append(s.Audio, editspec.AudioTrack{ID: "newwords", AssetID: v.Sound, Role: editspec.RoleDialogue, In: 0, Out: v.Fit.To - v.Fit.From, At: v.Fit.From})
	})
	e.finalRender(t, rev)
	out := mustCall[ops.DeliverOut](t, e.a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rev}, Out: t.TempDir(), AllowDraft: true}, true)
	b, _ := os.ReadFile(out.Files[0].Sidecar)
	var sc ops.DeliverySidecar
	_ = json.Unmarshal(b, &sc)
	if len(sc.ChangedWords) != 1 || sc.ChangedWords[0].Words != "fifteen" {
		t.Fatalf("the export's notes list the new words however the cut plays them: %+v", sc.ChangedWords)
	}
	mustCall[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: e.consent.ID}, true)
	if g := mustCall[ops.ShipGateOut](t, e.a, "delivery.ready", ops.ShipGateIn{ProjectID: e.project}, false); consentHold(g) == nil {
		t.Fatalf("a version playing the new words as a track is held back once the consent is withdrawn: %+v", g.Holds)
	}
	if _, err := call[ops.DeliverOut](t, e.a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rev}, Out: t.TempDir(), AllowDraft: true}, true); codeOf(err) != apperr.Held {
		t.Fatalf("it went out: %v", err)
	}
}

// videoClip adds a short clip with picture and sound (a stand-in for a matched mouth).
func (e *env) videoClip(t *testing.T, secs float64) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "mouth.mp4")
	if out, err := exec.Command("ffmpeg", "-nostdin", "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=160x90:rate=24:duration=%.3f", secs),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=330:duration=%.3f", secs), "-shortest", "-pix_fmt", "yuv420p", "-c:a", "aac", p).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	res := ingest.File(context.Background(), e.a, p, ingest.Options{ProjectID: e.project, FootageOnly: true})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	return res.AssetID
}

// A consent for new words covers the projects it names: a cut in another
// project that plays them (nesting the cut they're in) is held back from
// going out, and its captions note doesn't say "with their consent".
func TestConsentCoversOnlyTheProjectsItNames(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	ctx := context.Background()
	if err := e.a.DB.Update(ctx, store.CConsents, e.a.WorkspaceID(), e.consent.ID, bson.M{"$set": bson.M{"scope": models.ConsentScope{Projects: []string{e.project}}}}); err != nil {
		t.Fatal(err)
	}
	v := e.makeIt(t)
	inner := e.placeIt(t, v.ID).Applied.Revision.ID
	if g := mustCall[ops.ShipGateOut](t, e.a, "delivery.ready", ops.ShipGateIn{ProjectID: e.project}, false); consentHold(g) != nil {
		t.Fatalf("the project the consent names: nothing about it holds the version: %+v", g.Holds)
	}
	outer := ids.New(ids.Project)
	now := time.Now().UTC()
	if err := e.a.DB.Insert(ctx, store.CProjects, models.Project{ID: outer, WorkspaceID: e.a.WorkspaceID(), Name: "Launch film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	mustCall[ops.ProjectLinkOut](t, e.a, "project.link", ops.ProjectLinkIn{ProjectID: outer, From: e.project}, true)
	plan := fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"garden","assetId":%q,"in":0,"out":8}]}}`,
		outer, editspec.NestedKey(e.project, inner))
	if _, err := applyPlan(t, e, plan, true); err != nil {
		t.Fatal(err)
	}
	g := mustCall[ops.ShipGateOut](t, e.a, "delivery.ready", ops.ShipGateIn{ProjectID: outer}, false)
	if h := consentHold(g); h == nil || !strings.Contains(h.Says, "Launch film") {
		t.Fatalf("a project the consent doesn't name is held back, saying so: %+v", g.Holds)
	}
	vtt := mustCall[ops.CaptionsFileOut](t, e.a, "captions.file", ops.CaptionsFileIn{ProjectID: outer, Format: "vtt"}, false).Content
	if strings.Contains(vtt, "with their consent") || !strings.Contains(vtt, "doesn't cover") {
		t.Fatalf("the captions note says the consent doesn't cover this video: %s", vtt)
	}
}

// Words a person took out of the cut don't come back through a new cut made
// from an older version: an AI app's fork of the version that played them is
// refused; a person's goes through the usual checks.
func TestAForkDoesntBringBackWordsAPersonTookOut(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	v := e.makeIt(t)
	placed := e.placeIt(t, v.ID).Applied.Revision.ID
	undo := ops.CutSayPlaceIn{ID: v.ID, NoRender: true}
	undo.ConfirmToken = mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", withDry(undo), true).Check.ConfirmToken
	mustCall[ops.CutSayPlaceOut](t, e.a, "cut.say.undo", undo, true)
	fork := func(branch string) string {
		return fmt.Sprintf(`{"projectId":%q,"branch":%q,"baseRevisionId":%q,"ops":[{"op":"updateClip","id":"a","set":{"notes":"tidy"}}]}`, e.project, branch, placed)
	}
	if _, err := applyPlan(t, e, fork("alt"), false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("an AI app made a new cut that plays words a person took out: %v", err)
	}
	out, err := applyPlan(t, e, fork("mine"), true)
	if err != nil {
		t.Fatalf("a person may: %v", err)
	}
	if s := mustCall[ops.RevisionShowOut](t, e.a, "revision.show", ops.RevisionShowIn{ID: out.Revision.ID}, false).Spec; s.Mix == nil || len(s.Mix.Said) != 1 {
		t.Fatalf("a person's new cut keeps them: %+v", s.Mix)
	}
}

// A computer on an older MetaRush doesn't know changed words can hold a
// version back, so while one does (with the ship gate off too) the version
// says such a computer may still deliver it.
func TestOlderComputersAreWarnedAboutWhileChangedWordsHold(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	ctx := context.Background()
	v := e.makeIt(t)
	e.placeIt(t, v.ID)
	now := time.Now().UTC()
	if err := e.a.DB.Insert(ctx, store.CInstallations, models.Installation{ID: ids.New(ids.Installation), WorkspaceID: e.a.WorkspaceID(), Name: "old laptop", Version: "0.1.0", CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	if g := mustCall[ops.ShipGateOut](t, e.a, "delivery.ready", ops.ShipGateIn{ProjectID: e.project}, false); g.On || len(g.Warnings) != 0 {
		t.Fatalf("with the gate off and nothing holding it, no warning: %+v", g.Warnings)
	}
	mustCall[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: e.consent.ID}, true)
	g := mustCall[ops.ShipGateOut](t, e.a, "delivery.ready", ops.ShipGateIn{ProjectID: e.project}, false)
	if g.OlderComputers != 1 || len(g.Warnings) != 1 || !strings.Contains(g.Warnings[0], "older MetaRush") || !strings.Contains(g.Warnings[0], "consent") {
		t.Fatalf("an older computer that may deliver it anyway is warned about: %+v", g.Warnings)
	}
}

// A version held back by changed words whose consent no longer holds isn't
// posted to a video platform or shared for review either (with the ship gate
// off, as here): both say what holds it.
func TestWithdrawnConsentHoldsPostingAndSharingToo(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	v := e.makeIt(t)
	rev := e.placeIt(t, v.ID).Applied.Revision.ID
	e.finalRender(t, rev)
	mustCall[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: e.consent.ID}, true)
	pp := mustCall[ops.PublishPlanOut](t, e.a, "publish.plan", ops.PublishIn{ProjectID: e.project}, true)
	held := false
	for _, h := range pp.Holds {
		held = held || (strings.Contains(h.Says, "“fifteen”") && h.Code == apperr.ConsentRequired)
	}
	if pp.Ready || !held {
		t.Fatalf("posting must be held by the withdrawn consent: %+v", pp.Holds)
	}
	sp := mustCall[reviewlinks.SharePlanOut](t, e.a, "share.plan", reviewlinks.SharePlanIn{ProjectID: e.project}, true)
	if sp.May || !sp.Held || len(sp.Holds) == 0 || !strings.Contains(strings.Join(sp.Holds, " "), "“fifteen”") {
		t.Fatalf("sharing for review must be held by the withdrawn consent: %+v", sp)
	}
}
