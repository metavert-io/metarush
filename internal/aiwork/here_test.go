package aiwork_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/aiconformance"
	"github.com/metavert-io/metarush/internal/aiservices/local"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeai"
	"github.com/metavert-io/metarush/internal/testutil/fakelocal"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/vision"
)

// The local recognizer is a transcription provider (D278): offered as the
// free alternative to a cloud transcript, made without a price to agree,
// and its words reach the clip the way a cloud transcript's do, named as a
// local transcript is.
func TestTranscriptOnThisComputer(t *testing.T) {
	e := setup(t)
	fakelocal.Hearing(t)
	ctx := context.Background()
	clip := e.sound(t, "talk", 4)
	cloud, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "fake/fake-transcription", Clip: clip.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	offered := false
	for _, alt := range cloud.Alternatives {
		if alt.Provider == local.Name && alt.Why == "cheaper" && strings.Contains(alt.Summary, "on this computer: free") {
			offered = true
		}
	}
	if !offered {
		t.Fatalf("the recognizer on this computer isn't offered: %+v", cloud.Alternatives)
	}
	in := aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "local/parakeet-v2", Clip: clip.ID}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || !est.Here || est.USD != 0 || !est.Allowed || !est.KeyReady || !strings.Contains(est.Summary, "on this computer: free") {
		t.Fatalf("estimate %+v %v", est, err)
	}
	// Nothing to pay: no price to agree.
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Summary, "on this computer") {
		t.Errorf("summary %q", out.Summary)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenDone || g.Outcome.Outputs[0].Words != 3 || costOfGen(g) != 0 {
		t.Fatalf("outcome %+v", g.Outcome)
	}
	if g.Forgotten == nil || !g.Forgotten.OK {
		t.Errorf("nothing to delete anywhere, and it says so: %+v", g.Forgotten)
	}
	var js []models.Job
	_ = e.a.DB.Find(ctx, store.CJobs, e.a.WorkspaceID(), bson.M{"type": jobs.TypeTranscribe, "assetId": clip.ID, "payload.generationId": g.ID}, &js)
	if len(js) != 1 {
		t.Fatalf("transcribe jobs %d", len(js))
	}
	tr, name, model, err := aiwork.CloudTranscript(ctx, e.a, g.ID)
	if err != nil || name != "parakeet" || model != "tdt-0.6b-v2-int8" || len(tr.Segments) != 1 {
		t.Fatalf("transcript %+v %s %s %v", tr, name, model, err)
	}
}

func costOfGen(g models.Generation) float64 {
	if g.Outcome == nil || g.Outcome.Actual == nil {
		return -1
	}
	return g.Outcome.Actual.USD
}

// Content Settings that allow no AI services still allow models on this
// computer — nothing of the client's leaves it — unless they name them.
func TestNoAIServicesStillAllowsThisComputer(t *testing.T) {
	e := setup(t)
	fakelocal.Hearing(t)
	clip := e.sound(t, "talk", 3)
	if _, err := ops.CallTyped[ops.ContentShowOut](context.Background(), e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	none := true
	if out, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, None: &none}, true); err != nil || !strings.Contains(out.Summary, "no AI service outside this computer") {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "fake/fake-transcription", Clip: clip.ID}, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("a cloud transcript under no AI services: %v", err)
	}
	// Without asking, the one model allowed — this computer's — is chosen.
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Clip: clip.ID}, false)
	if err != nil || est.Provider != local.Name || !est.Allowed {
		t.Fatalf("under no AI services: %+v %v", est, err)
	}
	if _, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, Forbid: []string{local.Name}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "local/parakeet-v2", Clip: clip.ID}, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("named in the forbidden list: %v", err)
	}
}

// What may leave (D290): a model on this computer sends nothing anywhere, so
// a client's "nothing leaves" still allows it; a service elsewhere is
// refused. Lip sync sends a part of a clip — footage with faces and voices —
// so a client that keeps faces here keeps it from the service.
func TestWhatMayLeaveKeepsThisComputersWork(t *testing.T) {
	t.Setenv("CLAUDECODE", "") // a person's terminal, even when Claude Code runs the tests
	e := setup(t)
	fakelocal.Hearing(t)
	clip := e.sound(t, "talk", 3)
	if _, err := ops.CallTyped[ops.ContentShowOut](context.Background(), e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	setLeave := func(in leave.LeaveSetIn) {
		in.ProjectID = e.project
		if _, err := call[leave.LeaveOut](t, e.a, "content.leave.set", in, true); err != nil {
			t.Fatal(err)
		}
	}
	nothing := true
	setLeave(leave.LeaveSetIn{Nothing: &nothing})
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "fake/fake-transcription", Clip: clip.ID}, true); codeOf(err) != apperr.KeptHere {
		t.Fatalf("a cloud transcript when nothing may leave: %v", err)
	}
	in := aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "local/parakeet-v2", Clip: clip.ID}
	// A person asks (an AI app reads nothing of a client that keeps everything here).
	if est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, true); err != nil || !est.Allowed {
		t.Fatalf("a transcript on this computer when nothing may leave: %+v %v", est, err)
	}
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	if g := e.gen(t, out.Work.ID); g.Outcome == nil || g.Outcome.State != models.GenDone {
		t.Fatalf("made here: %+v", g.Outcome)
	}

	off := false
	setLeave(leave.LeaveSetIn{Nothing: &off, Keep: map[string][]string{models.PlaceAI: {models.LeaveFaces}}})
	footage, sound := e.footage(t, "interview", 4), e.sound(t, "new line", 3)
	lip := aiwork.MakeIn{Kind: aiservices.KindLipSync, ProjectID: e.project, Model: "fake/fake-lip-sync", Clip: footage.ID, Sound: sound.ID}
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", lip, true); codeOf(err) != apperr.KeptHere || !strings.Contains(err.Error(), "face") {
		t.Fatalf("lip sync when faces stay here: %v", err)
	}
}

// A model that isn't set up on this computer isn't offered, and asking for
// it says how to set it up; nothing is queued.
func TestNotSetUpHereIsSaid(t *testing.T) {
	e := setup(t)
	old := local.Recognizer
	local.Recognizer = func(aiservices.Env, string, string) (transcribe.Provider, error) {
		return nil, apperr.New(apperr.NotConfigured, "Parakeet isn't set up on this computer, so it can't write the words down here.").WithCommand("metarush doctor")
	}
	t.Cleanup(func() { local.Recognizer = old })
	clip := e.sound(t, "talk", 3)
	cloud, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "fake/fake-transcription", Clip: clip.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, alt := range cloud.Alternatives {
		if alt.Provider == local.Name {
			t.Errorf("offered though it can't run here: %+v", alt)
		}
	}
	in := aiwork.MakeIn{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "local/parakeet-v2", Clip: clip.ID}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || est.Allowed || est.KeyReady || !strings.Contains(strings.Join(est.Problems, " "), "isn't set up on this computer") {
		t.Fatalf("estimate %+v %v", est, err)
	}
	if _, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false); err == nil {
		t.Fatal("made though it can't run here")
	}
	ms, err := call[aiwork.ModelsOut](t, e.a, "ai.models", aiwork.ModelsIn{Kind: aiservices.KindTranscription}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms.Kinds[0].Models {
		if m.Provider == local.Name && (m.KeyReady || m.NotHere == "") {
			t.Errorf("%s: %+v", m.ID, m)
		}
	}
}

// picture adds a resources-library picture to the project.
func (e *env) picture(t *testing.T, name string, w, h int) *models.Resource {
	t.Helper()
	p := filepath.Join(t.TempDir(), name+".png")
	if err := os.WriteFile(p, aiconformance.SamplePicture(w, h), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := resources.AddFile(context.Background(), e.a, p, resources.AddOptions{Scope: "project", ProjectID: e.project, Title: "Garden sign", Name: name, Via: "cli"})
	if err != nil || res.Resource == nil {
		t.Fatalf("add picture: %+v %v", res, err)
	}
	return res.Resource
}

// A picture's background taken away, and a picture made larger, on this
// computer (D279): free, its own file sent nowhere, kept as a new picture
// with how it was made; a cut-out isn't marked as AI-generated, a larger
// picture is (its detail is made up).
func TestPicturesChangedOnThisComputer(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	handed := fakelocal.Masker(t)
	fakelocal.Upscaler(t)
	pic := e.picture(t, "garden-sign", 48, 32)

	in := aiwork.MakeIn{Kind: aiservices.KindBackground, ProjectID: e.project, Pictures: []string{pic.Name}}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || !est.Here || est.Provider != local.Name || est.USD != 0 || !est.Allowed {
		t.Fatalf("estimate %+v %v", est, err)
	}
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	if len(*handed) != 1 || (*handed)[0] != "image/png" {
		t.Errorf("the picture was handed over as %v, not its own PNG", *handed)
	}
	g := e.gen(t, out.Work.ID)
	var cut models.Resource
	if err := e.a.DB.GetByID(ctx, store.CResources, e.a.WorkspaceID(), g.Outcome.Outputs[0].ResourceID, &cut); err != nil {
		t.Fatal(err)
	}
	v := cut.Latest()
	if cut.Title != "Garden sign (no background)" || strings.Contains(strings.Join(cut.Tags, ","), models.TagAIGenerated) || !v.Source.MadeWith.Edit ||
		len(v.Source.Lineage) != 1 || !strings.Contains(v.Source.Lineage[0].Step, "background taken away on this computer") {
		t.Fatalf("cut-out %+v %+v", cut, v.Source)
	}

	in = aiwork.MakeIn{Kind: aiservices.KindUpscale, ProjectID: e.project, Pictures: []string{pic.Name}, Options: models.GenOptions{Scale: 4}}
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindUpscale, ProjectID: e.project, Pictures: []string{pic.Name}, Options: models.GenOptions{Scale: 3}}, false); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("3×: %v", err)
	}
	out, err = call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	g = e.gen(t, out.Work.ID)
	var big models.Resource
	if err := e.a.DB.GetByID(ctx, store.CResources, e.a.WorkspaceID(), g.Outcome.Outputs[0].ResourceID, &big); err != nil {
		t.Fatal(err)
	}
	if v := big.Latest(); big.Title != "Garden sign (4× larger)" || v.Width != 192 || v.Height != 128 || !strings.Contains(strings.Join(big.Tags, ","), models.TagAIGenerated) {
		t.Fatalf("larger %+v %+v", big, v)
	}
}

// An offline voice speaks through the same job: free, a clip in Sources.
func TestOfflineVoiceThroughTheJob(t *testing.T) {
	e := setup(t)
	fakelocal.Voices(t)
	in := aiwork.MakeIn{Kind: aiservices.KindVoice, ProjectID: e.project, Model: "local/kitten-nano", Prompt: "Welcome to the garden."}
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	g := e.gen(t, out.Work.ID)
	if g.Outcome.State != models.GenDone || g.Outcome.Outputs[0].AssetID == "" || costOfGen(g) != 0 {
		t.Fatalf("outcome %+v", g.Outcome)
	}
}

// growAbility is an ability that provides larger pictures (D281): it copies
// the picture it is given, a stand-in for a model.
var growAbility = map[string]string{"ability.yaml": `ability: 1
name: grow
title: Grow pictures
does: Makes a picture larger.
runs: python3 grow.py
inputs:
  picture: {kind: picture, optional: true}
  scale: {kind: number, min: 2, max: 4, default: 2}
outputs: [picture]
provides: [upscale]
cost: free
needs: {programs: [python3]}
licence: MIT
test: {inputs: {}}
`, "grow.py": `import json, os, shutil
inputs = json.load(open(os.environ["METARUSH_INPUTS"]))
out = os.environ["METARUSH_OUTPUT"]
src = inputs.get("picture")
if src:
    data = open(src, "rb").read() + b"grown"  # a picture after its end: still a PNG, other bytes
    open(os.path.join(out, "bigger.png"), "wb").write(data)
else:
    open(os.path.join(out, "bigger.svg"), "w").write('<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"/>')
`}

// approveAbility checks, approves and sets an ability up on this computer, as owners and people do.
func (e *env) approveAbility(t *testing.T, files map[string]string) {
	t.Helper()
	if iso := sandbox.Here(e.a.Tools); !iso.OK() {
		t.Skip("no sandbox on this computer: " + iso.Problem)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := call[abilities.AbilityCheckOut](t, e.a, "ability.check", abilities.AbilityCheckIn{Path: dir}, false)
	if err != nil || !out.OK {
		t.Fatalf("check %+v %v", out, err)
	}
	open := strings.Contains(files["ability.yaml"], "network: open")
	if _, err := call[abilities.AbilityDecisionOut](t, e.a, "ability.approve", abilities.AbilityApproveIn{Ref: out.Name + "@" + out.Version, OpenNetwork: open}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call[abilities.AbilitySetupOut](t, e.a, "ability.setup", abilities.AbilitySetupIn{Ref: out.Name}, true); err != nil {
		t.Fatal(err)
	}
}

// An approved ability that provides a kind of work is chosen, priced and
// recorded like any provider, and runs in the sandbox through the job; its
// picture lands in the resources library with how it was made.
func TestAbilityProvidesThroughTheJob(t *testing.T) {
	e := setup(t)
	e.approveAbility(t, growAbility)
	pic := e.picture(t, "garden-sign", 48, 32)
	in := aiwork.MakeIn{Kind: aiservices.KindUpscale, ProjectID: e.project, Model: "ability-grow/grow", Pictures: []string{pic.Name}}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || !est.Allowed || est.USD != 0 || est.Provider != "ability-grow" || !strings.Contains(est.Data, "nothing leaves this computer") {
		t.Fatalf("estimate %+v %v", est, err)
	}
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenDone || g.Ticket == nil || g.Ticket.Extra["abilityRun"] == "" {
		t.Fatalf("outcome %+v ticket %+v", g.Outcome, g.Ticket)
	}
	var big models.Resource
	if err := e.a.DB.GetByID(context.Background(), store.CResources, e.a.WorkspaceID(), g.Outcome.Outputs[0].ResourceID, &big); err != nil {
		t.Fatal(err)
	}
	if v := big.Latest(); v.Source.MadeWith == nil || v.Source.MadeWith.Provider != "ability-grow" || !strings.Contains(big.Title, "larger") {
		t.Fatalf("picture %+v", big)
	}
	// Content Settings can rule the ability out by name, like any provider.
	if _, err := ops.CallTyped[ops.ContentShowOut](context.Background(), e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if _, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, Forbid: []string{"ability-grow"}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("forbidden: %v", err)
	}
}

// Work waiting in the queue runs with the version of the ability it was
// planned with: once another version is approved, it isn't run (D281).
func TestAbilityRunsOnlyThePlannedVersion(t *testing.T) {
	e := setup(t)
	e.approveAbility(t, grownAs("grow-pinned", ""))
	pic := e.picture(t, "garden-sign", 48, 32)
	in := aiwork.MakeIn{Kind: aiservices.KindUpscale, ProjectID: e.project, Model: "ability-grow-pinned/grow-pinned", Pictures: []string{pic.Name}}
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	planned := e.gen(t, out.Work.ID)
	if len(planned.ModelVersion) < 12 {
		t.Fatalf("no version recorded: %+v", planned)
	}
	v2 := grownAs("grow-pinned", "")
	v2["grow.py"] += "# the second version\n"
	e.approveAbility(t, v2)
	if _, err := e.run(t, out.JobID); err == nil || !strings.Contains(err.Error(), "changed after this was asked for") {
		t.Fatalf("job: %v", err)
	}
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || !strings.Contains(g.Outcome.Message, "changed after this was asked for") || g.Ticket != nil || len(g.Sending) != 0 {
		t.Fatalf("outcome %+v ticket %+v", g.Outcome, g.Ticket)
	}
	var runs []models.AbilityRun
	_ = e.a.DB.Find(context.Background(), store.CAbilityRuns, e.a.WorkspaceID(), bson.M{"work": g.ID}, &runs)
	if len(runs) != 0 {
		t.Fatalf("the newer version ran: %+v", runs)
	}
	// Asked again, it plans with the version approved now, and runs.
	_, again := e.make(t, in)
	if again.ModelVersion == planned.ModelVersion || again.Outcome == nil || again.Outcome.State != models.GenDone {
		t.Fatalf("again: %+v %+v", again.ModelVersion, again.Outcome)
	}
}

// grownAs is the grow ability under another name, with more manifest lines.
func grownAs(name, more string) map[string]string {
	y := strings.Replace(growAbility["ability.yaml"], "name: grow\n", "name: "+name+"\n", 1)
	return map[string]string{"grow.py": growAbility["grow.py"], "ability.yaml": strings.Replace(y, "cost: free\n", "cost: free\n"+more, 1)}
}

// Content Settings can pin or rule out an ability straight after it is
// approved, from a fresh terminal (nothing asked for the catalogue first);
// and ruling a service out rules out an ability that reaches it, by its
// hosts or an open network (D281).
func TestContentSettingsRuleOutWhatAnAbilityReaches(t *testing.T) {
	e := setup(t)
	if _, err := ops.CallTyped[ops.ContentShowOut](context.Background(), e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	set := func(in aiwork.RulesSetIn) error {
		in.ProjectID = e.project
		_, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", in, true)
		return err
	}
	e.approveAbility(t, grownAs("grow-fresh", ""))
	if err := set(aiwork.RulesSetIn{Pin: map[string]string{aiservices.KindUpscale: "ability-grow-fresh/grow-fresh"}}); err != nil {
		t.Fatalf("pin straight after approval: %v", err)
	}
	if err := set(aiwork.RulesSetIn{Forbid: []string{"ability-grow-fresh"}, Unpin: []string{aiservices.KindUpscale}}); err != nil {
		t.Fatalf("forbid straight after approval: %v", err)
	}

	pic := e.picture(t, "garden-sign", 48, 32)
	estimate := func(model string) (*aiwork.AIEstimate, error) {
		return call[aiwork.AIEstimate](t, e.a, "ai.estimate", aiwork.MakeIn{Kind: aiservices.KindUpscale, ProjectID: e.project, Model: model, Pictures: []string{pic.Name}, NoPeople: true}, false)
	}
	e.approveAbility(t, grownAs("grow-eleven", "hosts: [\"*.elevenlabs.io\"]\nsends: [pictures]\n")) // every name under it, as the proxy allows
	if est, err := estimate("ability-grow-eleven/grow-eleven"); err != nil || !est.Allowed {
		t.Fatalf("before: %+v %v", est, err)
	}
	if err := set(aiwork.RulesSetIn{Forbid: []string{"elevenlabs"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := estimate("ability-grow-eleven/grow-eleven"); codeOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "ElevenLabs, which") {
		t.Fatalf("an ability reaching a forbidden service: %v", err)
	}
	if err := set(aiwork.RulesSetIn{Pin: map[string]string{aiservices.KindUpscale: "ability-grow-eleven/grow-eleven"}}); codeOf(err) != apperr.InvalidInput || !strings.Contains(err.Error(), "reaches ElevenLabs") {
		t.Fatalf("pinned an ability that reaches a forbidden service: %v", err)
	}
	list, err := call[aiwork.ModelsOut](t, e.a, "ai.models", aiwork.ModelsIn{ProjectID: e.project, Kind: aiservices.KindUpscale}, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list.Providers {
		if p.Name == "ability-grow-eleven" && p.Allowed {
			t.Error("the list says an ability reaching a forbidden service is allowed")
		}
	}

	e.approveAbility(t, grownAs("grow-open", "network: open\nsends: [pictures]\n"))
	if _, err := estimate("ability-grow-open/grow-open"); codeOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "any service on the internet") {
		t.Fatalf("an ability with an open network under rules that forbid a service: %v", err)
	}
	if err := set(aiwork.RulesSetIn{Allow: []string{"elevenlabs", "ability-grow-fresh"}}); err != nil {
		t.Fatal(err)
	}
	if est, err := estimate("ability-grow-open/grow-open"); err != nil || !est.Allowed {
		t.Fatalf("nothing forbidden: %+v %v", est, err)
	}
}

// An ability whose runs send pictures away is started only by a person,
// as its own runs are (D234).
func TestAbilityThatSendsPicturesNeedsAPerson(t *testing.T) {
	e := setup(t)
	files := map[string]string{"grow.py": growAbility["grow.py"], "ability.yaml": strings.Replace(strings.Replace(growAbility["ability.yaml"],
		"name: grow\n", "name: grow-online\n", 1), "cost: free\n", "cost: free\nhosts: [api.example.com]\nsends: [pictures]\n", 1)}
	e.approveAbility(t, files)
	pic := e.picture(t, "garden-sign", 48, 32)
	in := aiwork.MakeIn{Kind: aiservices.KindUpscale, ProjectID: e.project, Model: "ability-grow-online/grow-online", Pictures: []string{pic.Name}, NoPeople: true}
	if _, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("not a person: %v", err)
	}
	if out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, true); err != nil || out.JobID == "" {
		t.Fatalf("a person: %+v %v", out, err)
	}
}

// footage adds a clip with picture and sound, seconds long.
func (e *env) footage(t *testing.T, name string, seconds float64) *models.Asset {
	t.Helper()
	p := filepath.Join(t.TempDir(), name+".mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=64x36:rate=24:duration=%.2f", seconds),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=330:duration=%.2f", seconds), "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", p)
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

// speaks names who speaks in a clip, as a person would.
func (e *env) speaks(t *testing.T, as *models.Asset, person models.Person) {
	t.Helper()
	ctx := context.Background()
	if err := e.a.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: e.a.WorkspaceID(), AssetID: as.ID, Label: "speaker_00",
		Name: person.Name, PersonID: person.ID, Source: "person", Naming: byPerson(e), Seconds: 3}); err != nil {
		t.Fatal(err)
	}
	if err := e.a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: e.a.WorkspaceID(), AssetID: as.ID, From: 0, To: 3,
		Text: "plant the beans", Speaker: "speaker_00", Provider: "test"}); err != nil {
		t.Fatal(err)
	}
}

// face puts a face in the on-screen index of a clip, named or not.
func (e *env) face(t *testing.T, as *models.Asset, from, to float64, person *models.Person) {
	t.Helper()
	ctx := context.Background()
	f := models.FaceTrack{ID: ids.New(ids.Face), WorkspaceID: e.a.WorkspaceID(), AssetID: as.ID, From: from, To: to, Samples: 4, At: from, Box: []float64{0.3, 0.2, 0.3, 0.4}}
	if person != nil {
		f.PersonID, f.Name, f.Naming = person.ID, person.Name, byPerson(e) // named by a person (D274)
	}
	if err := e.a.DB.Insert(ctx, store.CFaces, f); err != nil {
		t.Fatal(err)
	}
}

// looked marks a clip as looked at by the on-screen index.
func (e *env) looked(t *testing.T, as *models.Asset) {
	t.Helper()
	if err := e.a.DB.Update(context.Background(), store.CAssets, e.a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"analysis.visual": "available"}}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) consentFor(t *testing.T, name string, uses, services []string) models.Person {
	t.Helper()
	ctx := context.Background()
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: name, Self: true, Adult: true, Uses: uses, Services: services,
		Statement: "I, " + name + ", agree to MetaRush matching my lips to new sound in my own videos."}, true); err != nil {
		t.Fatal(err)
	}
	var p models.Person
	_ = e.a.DB.C(store.CPeople).FindOne(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "name": name}).Decode(&p)
	return p
}

// facesAt stands in for the on-screen helper looking at a lip sync's part:
// faces(at) faces in the frame that many seconds into the part.
func facesAt(t *testing.T, faces func(at float64) int) {
	aiwork.LookAtFrames(t, func(dir string) ([]vision.Frame, error) {
		names, _ := filepath.Glob(filepath.Join(dir, "f-*.jpg"))
		var out []vision.Frame
		for _, n := range names {
			var k int
			if _, err := fmt.Sscanf(filepath.Base(n), "f-%05d.jpg", &k); err != nil {
				return nil, err
			}
			f := vision.Frame{File: filepath.Base(n)}
			for i := 0; i < faces(float64(k-1)*0.25); i++ {
				f.Faces = append(f.Faces, vision.Face{Conf: 0.9})
			}
			out = append(out, f)
		}
		return out, nil
	})
}

// The on-screen index looks about once a second; before sending, lip sync
// looks at every quarter second, and a face nobody named between two of the
// index's looks stops it before anything is sent (D279).
func TestLipSyncLooksAtEveryQuarterSecond(t *testing.T) {
	e := setup(t)
	clip, sound := e.footage(t, "interview", 4), e.sound(t, "new line", 3)
	in := aiwork.MakeIn{Kind: aiservices.KindLipSync, ProjectID: e.project, Model: "fake/fake-lip-sync", Clip: clip.ID, Sound: sound.ID}
	rosa := e.consentFor(t, "Rosa Diaz", []string{models.ConsentUseLipSync}, []string{fakeai.Name})
	e.speaks(t, clip, rosa)
	e.speaks(t, sound, rosa)
	e.looked(t, clip)
	e.face(t, clip, 0, 4, &rosa)
	looked := 0
	facesAt(t, func(at float64) int {
		looked++
		if at >= 2.25 && at < 2.75 { // someone passes between two of the index's looks
			return 2
		}
		return 1
	})
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || !est.Allowed {
		t.Fatalf("the index saw only Rosa: %+v %v", est, err)
	}
	in.MaxUSD = usd(est.USD)
	out, err := call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err == nil || !strings.Contains(err.Error(), "At 0:02 “interview” shows 2 faces, and only one person is named there") {
		t.Fatalf("job: %v", err)
	}
	if looked < 15 {
		t.Errorf("looked at %d frames of 4 seconds", looked)
	}
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenFailed || len(g.Sending) != 0 || g.Ticket != nil || !strings.Contains(g.Outcome.Message, "Nothing was sent") {
		t.Fatalf("outcome %+v sending %v", g.Outcome, g.Sending)
	}
	// Only Rosa all along: sent.
	facesAt(t, func(float64) int { return 1 })
	if _, g := e.make(t, in); g.Outcome == nil || g.Outcome.State != models.GenDone {
		t.Fatalf("only Rosa: %+v", g.Outcome)
	}
	// A computer that can't look at what's on screen doesn't send it.
	aiwork.LookAtFrames(t, func(string) ([]vision.Frame, error) { return nil, vision.ErrNotMac })
	out, err = call[aiwork.MakeOut](t, e.a, "ai.make", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err == nil || !strings.Contains(err.Error(), "looks at every quarter second") {
		t.Fatalf("no helper: %v", err)
	}
}

func lipProblems(t *testing.T, e *env, in aiwork.MakeIn) string {
	t.Helper()
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if est.Allowed {
		return ""
	}
	return strings.Join(est.Problems, " | ")
}

// Lip sync (D279) changes every face the footage shows in that part: each
// must be named by a person and have consent naming lip sync and this
// service; a face nobody named, footage nobody looked at, a part where no one
// can be told apart, and a brief unnamed voice are all refused; consent for
// other uses doesn't cover it; the result is marked as a likeness.
func TestLipSyncNeedsEveryonesConsent(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	clip, sound := e.footage(t, "interview", 4), e.sound(t, "new line", 3)
	in := aiwork.MakeIn{Kind: aiservices.KindLipSync, ProjectID: e.project, Model: "fake/fake-lip-sync", Clip: clip.ID, Sound: sound.ID}
	rosa := e.consentFor(t, "Rosa Diaz", []string{models.ConsentUseLipSync}, []string{fakeai.Name})
	e.speaks(t, clip, rosa)
	e.speaks(t, sound, rosa)
	facesAt(t, func(at float64) int { // Rosa all along, Ben from 1 s to 3 s
		if at >= 1 && at <= 3 {
			return 2
		}
		return 1
	})
	if got := lipProblems(t, e, in); !strings.Contains(got, "hasn't looked at what's on screen") {
		t.Fatalf("footage nobody looked at: %q", got)
	}
	e.looked(t, clip)
	if got := lipProblems(t, e, in); !strings.Contains(got, "sees no face it can tell apart") {
		t.Fatalf("nobody seen: %q", got)
	}
	e.face(t, clip, 0, 4, &rosa)
	if got := lipProblems(t, e, in); got != "" {
		t.Fatalf("Rosa alone, with her consent to lip sync: %q", got)
	}
	// Someone else on screen, silent, named, without consent: refused.
	ben := models.Person{ID: ids.New(ids.Person), WorkspaceID: e.a.WorkspaceID(), Name: "Ben Okafor", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := e.a.DB.Insert(ctx, store.CPeople, ben); err != nil {
		t.Fatal(err)
	}
	e.face(t, clip, 1, 3, &ben)
	if got := lipProblems(t, e, in); !strings.Contains(got, "Ben Okafor's face is in the footage") {
		t.Fatalf("a silent named face without consent: %q", got)
	}
	// Consent recorded for other uses doesn't cover lip sync.
	e.consentFor(t, "Ben Okafor", nil, nil)
	if got := lipProblems(t, e, in); !strings.Contains(got, "no consent from Ben Okafor to lip sync") {
		t.Fatalf("consent without lip sync: %q", got)
	}
	e.consentFor(t, "Ben Okafor", []string{models.ConsentUseLipSync}, []string{"sync"})
	if got := lipProblems(t, e, in); !strings.Contains(got, "no consent from Ben Okafor to lip sync with Fake AI") {
		t.Fatalf("consent naming another service: %q", got)
	}
	e.consentFor(t, "Ben Okafor", []string{models.ConsentUseLipSync}, []string{fakeai.Name})
	if got := lipProblems(t, e, in); got != "" {
		t.Fatalf("both consented: %q", got)
	}
	// A face nobody named: refused.
	e.face(t, clip, 2, 4, nil)
	if got := lipProblems(t, e, in); !strings.Contains(got, "shows someone nobody has named") {
		t.Fatalf("an unnamed face: %q", got)
	}
	_, _ = e.a.DB.C(store.CFaces).DeleteMany(ctx, bson.M{"assetId": clip.ID, "personId": bson.M{"$exists": false}})
	// A brief voice nobody named: refused too.
	if err := e.a.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: e.a.WorkspaceID(), AssetID: clip.ID, Label: "speaker_01",
		Minor: true, Seconds: 0.6}); err != nil {
		t.Fatal(err)
	}
	if err := e.a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: e.a.WorkspaceID(), AssetID: clip.ID, From: 3, To: 3.6,
		Text: "yes", Speaker: "speaker_01", Provider: "test"}); err != nil {
		t.Fatal(err)
	}
	if got := lipProblems(t, e, in); !strings.Contains(got, "Someone speaking in the footage") {
		t.Fatalf("a brief unnamed voice: %q", got)
	}
	_, _ = e.a.DB.C(store.CSpeakers).DeleteMany(ctx, bson.M{"assetId": clip.ID, "label": "speaker_01"})
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
	if err != nil || !est.Allowed || len(est.Consent.People) != 2 || est.Units != "4 seconds of footage" {
		t.Fatalf("estimate %+v %v", est, err)
	}
	_, g := e.make(t, in)
	if g.Outcome == nil || g.Outcome.State != models.GenDone || len(g.ConsentIDs) != 2 || g.References[0].MIME != "video/mp4" {
		t.Fatalf("record %+v %+v", g.Outcome, g.References)
	}
	var made models.Asset
	if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), g.Outcome.Outputs[0].AssetID, &made); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(made.Title, "lips matched to new sound") || !made.HasTag(models.TagAIGenerated) || made.Source.Generated == nil || !made.Source.Generated.Likeness {
		t.Fatalf("clip %+v %+v", made.Title, made.Source)
	}
}

// Never a child's or a public figure's face, whatever a consent says.
func TestLipSyncRefusesChildrenAndPublicFigures(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	clip, sound := e.footage(t, "interview", 4), e.sound(t, "new line", 3)
	in := aiwork.MakeIn{Kind: aiservices.KindLipSync, ProjectID: e.project, Model: "fake/fake-lip-sync", Clip: clip.ID, Sound: sound.ID}
	kit := e.consentFor(t, "Kit Moreno", []string{models.ConsentUseLipSync}, []string{fakeai.Name})
	e.speaks(t, clip, kit)
	e.speaks(t, sound, kit)
	e.looked(t, clip)
	e.face(t, clip, 0, 4, &kit)
	if got := lipProblems(t, e, in); got != "" {
		t.Fatalf("an adult who consented: %q", got)
	}
	for _, set := range []bson.M{{"adult": false}, {"adult": true, "publicFigure": true}} {
		if _, err := e.a.DB.C(store.CConsents).UpdateMany(ctx, bson.M{"personId": kit.ID}, bson.M{"$set": set}); err != nil {
			t.Fatal(err)
		}
		if got := lipProblems(t, e, in); !strings.Contains(got, "never changes the likeness of a child or a public figure") {
			t.Fatalf("%v: %q", set, got)
		}
	}
	// A sentence ending in sync.'s name has one period.
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, e.a, "content.own", ops.ContentOwnIn{ProjectID: e.project}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if _, err := call[aiwork.RulesOut](t, e.a, "ai.rules.set", aiwork.RulesSetIn{ProjectID: e.project, Forbid: []string{"sync"}}, true); err != nil {
		t.Fatal(err)
	}
	in.Model = "sync/lipsync-2"
	if _, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false); codeOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "allow sync.") || strings.Contains(err.Error(), "sync..") {
		t.Fatalf("forbidden: %v", err)
	}
}

// Consent can name lip sync only with a service, and nothing else.
func TestConsentUsesNeedAService(t *testing.T) {
	e := setup(t)
	for _, c := range []struct {
		uses, services []string
		want           string
	}{{[]string{"lip-sync"}, nil, "Name the AI service"}, {nil, []string{"sync"}, "Say what the services may do"}, {[]string{"face-swap"}, []string{"sync"}, "isn't something it covers"}} {
		_, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Ada Lovelace", Self: true, Adult: true, Uses: c.uses, Services: c.services,
			Statement: "I, Ada Lovelace, agree to MetaRush using my likeness in my own videos."}, true)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v %v: %v", c.uses, c.services, err)
		}
	}
}
