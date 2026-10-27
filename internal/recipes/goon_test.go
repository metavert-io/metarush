package recipes

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
	"github.com/metavert-io/metarush/internal/videogen"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// A step that costs more than its share of the agreement asks for its own
// part, and how much goes with the run (waitingUsd): the later steps' shares
// stay theirs, so the AI voice after a generated shot that cost more doesn't
// ask again. Going on needs a person's OK for at least that much (the
// dashboard's Go on, the CLI's prompt and the to-do's "Go on" agree to it).
func TestTheShotAsksForItsOwnPart(t *testing.T) {
	f := setup(t)
	noSamples(t) // about something else: the sample first is TestASampleOfAShot
	generate.PollEvery, generate.FirstPoll = 40*time.Millisecond, 5*time.Millisecond
	key := "AIza" + strings.Repeat("R", 35)
	fake := fakevideogen.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(videogen.EnvGeminiBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(videogen.GeminiName), "")
	if _, err := ops.CallTyped[providers.Status](f.ctx, f.a, "provider.set", providers.SetIn{Name: videogen.GeminiName, Key: key}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	runJob := func(id string) {
		t.Helper()
		j, err := f.a.Queue.Get(f.ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := generate.Run(f.ctx, f.a, j, nil); err != nil {
			t.Fatal(err)
		}
	}
	shot := generate.GenShotIn{ProjectID: f.teaser, Prompt: "a red paper boat on a pond", Seconds: 3, Resolution: "360p", NoPeople: true}
	est := must[generate.GenEstimate](t, f.a, "generate.estimate", shot, false)
	shot.MaxUSD = &est.USD
	gen := must[generate.GenRunOut](t, f.a, "generate.shot", shot, true)
	runJob(gen.JobIDs[0])
	gv := must[generate.GenView](t, f.a, "generate.show", generate.GenIDIn{ID: gen.Generations[0].ID}, false)
	if gv.State != "done" || len(gv.Clips) != 1 {
		t.Fatalf("shot %+v", gv)
	}
	spec := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
		Clips: []editspec.Clip{{ID: "boat", AssetID: gv.Clips[0].AssetID, In: 0, Out: 3, Label: "the boat"}, {ID: "hook", AssetID: f.interview, In: 1, Out: 4, Label: "the hook"}},
		Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{
			{ID: "line-1", Text: "Meet the boat.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "hook", Offset: 0.2}},
			{ID: "line-2", Text: "It sails tonight.", Anchor: editspec.Anchor{Kind: editspec.AnchorAfter}}}}}
	rev := f.apply(editspec.Plan{ProjectID: f.teaser, Branch: "boats", Instruction: "boats", Spec: spec})
	tts := must[voiceover.GenerateOut](t, f.a, "voiceover.tts", voiceover.TTSIn{RevisionID: rev, Apply: true}, true)
	must[ops.RevisionSummary](t, f.a, "revision.approve", ops.RevisionApproveIn{ID: tts.Applied.RevisionID}, true)
	must[RecipeSaveOut](t, f.a, "recipe.save", RecipeSaveIn{From: f.teaser, Branch: "boats", Title: "Boats", Name: "boats", NewProjects: true, Vary: []string{"boat-prompt"}}, true)

	in := RecipePlanIn{Recipe: "boats", Inputs: []RecipeInputValue{{Name: "boat-prompt", Text: "a blue paper boat on a pond"}, {Name: "voice", Text: "Bram"}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	planned := &models.RecipeRun{Steps: plan.Steps}
	shotEst, voiceEst := stepOf(planned, stepShots).Estimate, stepOf(planned, stepVoice).Estimate
	if !plan.Ready || !plan.NewProject || shotEst <= 0 || voiceEst <= 0 {
		t.Fatalf("the plan prices the shot and the voice: %+v / %+v", plan.Estimate, plan.Steps)
	}
	advanceOnStart = false
	defer func() { advanceOnStart = true }()
	agreed := plan.Estimate.USD
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint, MaxUSD: &agreed}, true).Run
	// The shot costs more than planned (here: it was planned at half its price).
	half := costs.Round(shotEst / 2)
	shotAt := -1
	for i, s := range run.Steps {
		if s.ID == stepShots {
			shotAt = i
		}
	}
	if _, err := f.a.DB.C(store.CRecipeRuns).UpdateOne(f.ctx, bson.M{"_id": run.ID}, bson.M{"$set": bson.M{"agreed.upTo": costs.Round(half + voiceEst),
		"steps." + strconv.Itoa(shotAt) + ".estimate": half}}); err != nil {
		t.Fatal(err)
	}
	calls := fake.Calls("create")

	waiting := f.advance(run.ID)
	if waiting.State != models.RunWaiting || waiting.WaitingFor != models.RunWaitsForPerson || stepOf(waiting, stepShots).State != models.StepWaiting {
		t.Fatalf("the shot waits for a person: %+v / %+v", waiting.State, waiting.Steps)
	}
	if fake.Calls("create") != calls || !strings.Contains(waiting.Waiting, costs.Money(waiting.WaitingUSD)+" more than you agreed to") {
		t.Fatalf("it asks, nothing sent: %q (calls %d)", waiting.Waiting, fake.Calls("create"))
	}
	// Its own part: the price less its share — not less the voice's share too.
	if d := waiting.WaitingUSD - costs.Round(shotEst-half); d < -0.0015 || d > 0.0015 {
		t.Fatalf("asks %v, the shot's own part is %v", waiting.WaitingUSD, shotEst-half)
	}
	// Going on needs the amount, and a person.
	if _, err := call[RecipeRunOut](t, f.a, "recipe.continue", RecipeContinueIn{Run: run.ID}, true); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("an amount is needed: %v", err)
	}
	more := waiting.WaitingUSD
	if _, err := call[RecipeRunOut](t, f.a, "recipe.continue", RecipeContinueIn{Run: run.ID, MoreUSD: &more}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("people only: %v", err)
	}
	// The to-do says what it asks; its "Go on" agrees to that.
	list := must[ops.TodoListOut](t, f.a, "todos.list", ops.TodoListIn{Kind: models.TodoRecipeRun}, false)
	if len(list.Todos) != 1 || !strings.Contains(list.Todos[0].Question, "more than you agreed to") {
		t.Fatalf("to-dos %+v", list.Todos)
	}
	must[ops.TodoAnswerOut](t, f.a, "todos.answer", ops.TodoAnswerIn{ID: list.Todos[0].ID, Answer: "go on"}, true)
	cont, err := getRun(f.ctx, f.a, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	shots := stepOf(cont, stepShots)
	if cont.WaitingUSD != 0 || shots.State != models.StepRunning || len(shots.Jobs) != 1 || cont.Agreed == nil || len(cont.Agreed.More) != 1 || cont.Agreed.More[0].USD != more {
		t.Fatalf("went on: %+v / %+v", shots, cont.Agreed)
	}
	runJob(shots.Jobs[0])
	moved := f.advance(run.ID)
	if moved.State != models.RunRunning || stepOf(moved, stepVoice).State != models.StepDone || moved.Spent.USD > moved.Agreed.UpTo+0.001 {
		t.Fatalf("the voice keeps its share and doesn't ask again: %s %q / %+v / spent %+v of %v", moved.State, moved.Waiting, stepOf(moved, stepVoice), moved.Spent, moved.Agreed.UpTo)
	}
	f.finishReview(run.ID)
	if done := f.advance(run.ID); done.State != models.RunDone {
		t.Fatalf("done %+v", done.Steps)
	}
}
