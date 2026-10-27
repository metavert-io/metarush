package recipes

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/receipt"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
	"github.com/metavert-io/metarush/internal/videogen"
)

// cheapSamples makes every priced kind of work expensive enough for a sample
// (the fakes' prices are tiny).
func cheapSamples(t *testing.T) {
	t.Helper()
	was := SampleFloorUSD
	SampleFloorUSD = 0
	t.Cleanup(func() { SampleFloorUSD = was })
}

// noSamples makes the run start without a sample (tests about other things).
func noSamples(t *testing.T) {
	t.Helper()
	was := SampleFloorUSD
	SampleFloorUSD = 1e9
	t.Cleanup(func() { SampleFloorUSD = was })
}

func stepIDs(steps []models.RecipeStep) string {
	var out []string
	for _, s := range steps {
		out = append(out, s.ID)
	}
	return strings.Join(out, ",")
}

// A run whose estimate has the AI voice starts with a sample (D294): one short
// line in the new voice, in a short cut of its own with a review copy; the
// person agrees only to the sample's price; nothing goes on until a person
// approves it; then the rest is planned (the sample's line used again, free)
// and asks for its own agreement.
func TestASampleFirst(t *testing.T) {
	f := setup(t)
	cheapSamples(t)
	f.save(RecipeSaveIn{})
	in := RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: "voice", Text: "Bram"}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if !plan.Ready || plan.Sample == nil || len(plan.Sample.Kinds) != 1 || plan.Sample.Kinds[0].Kind != models.SampleVoice {
		t.Fatalf("the plan asks for a sample of the AI voice: %+v", plan.Sample)
	}
	k := plan.Sample.Kinds[0]
	if k.Line != "line-1" || k.Text != "Meet the app." || k.USD <= 0 || k.USD >= plan.Estimate.USD || k.RunUSD <= 0 || !strings.Contains(k.What, "Bram") {
		t.Fatalf("the sample is the first line, priced on its own: %+v (run %v)", k, plan.Estimate.USD)
	}
	if got := stepIDs(plan.Steps); !strings.HasPrefix(got, "inputs,place,sample,approve,") {
		t.Fatalf("the sample's steps come first: %s", got)
	}
	if !strings.Contains(plan.Summary, "A sample first") || plan.Sample.Estimate.USD != k.USD {
		t.Fatalf("summary %q / %+v", plan.Summary, plan.Sample.Estimate)
	}
	// Agreeing to the sample's price starts it; less doesn't.
	low := 0.0 // the fakes' prices are tiny: under a cent
	if _, err := call[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint, MaxUSD: &low}, true); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("the sample's price is asked for: %v", err)
	}
	advanceOnStart = false
	defer func() { advanceOnStart = true }()
	all := plan.Estimate.USD
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint, MaxUSD: &all}, true).Run
	if stepIDs(run.Steps) != "inputs,place,sample,approve" || run.Sample == nil || run.Sample.State != models.SampleMaking || run.Agreed.UpTo != plan.Sample.Estimate.USD {
		t.Fatalf("a sample stage, the sample's price agreed: %s / %+v / %+v", stepIDs(run.Steps), run.Sample, run.Agreed)
	}
	r := f.advance(run.ID)
	if r.Sample.Branch != "app-teaser-1-sample" || r.Sample.Render == "" {
		t.Fatalf("the sample's cut and review copy: %+v / %+v", r.Sample, r.Steps)
	}
	sample := f.revision(r.Sample.Revision)
	if sample.Branch != "app-teaser-1-sample" || sample.Spec.Voiceover == nil || len(sample.Spec.Voiceover.Beats) != 1 || sample.Spec.Voiceover.Beats[0].Take == nil {
		t.Fatalf("one line in the AI voice in the sample's cut: %+v", sample.Spec.Voiceover)
	}
	if r.Spent.USD <= 0 || r.Spent.USD > r.Agreed.UpTo+0.0005 {
		t.Fatalf("the sample spends within its price: %+v of %v", r.Spent, r.Agreed.UpTo)
	}
	f.finishRender(r.Sample.Render)
	r = f.advance(run.ID)
	if r.State != models.RunWaiting || r.WaitingFor != models.RunWaitsForPerson || r.Sample.State != models.SampleWaiting || stepOf(r, stepApprove).State != models.StepWaiting {
		t.Fatalf("it waits for a person to approve the sample: %s %q / %+v", r.State, r.Waiting, r.Sample)
	}
	if again := f.advance(run.ID); again.State != models.RunWaiting || len(again.Steps) != 4 {
		t.Fatalf("nothing goes on by itself: %+v", again.Steps)
	}
	// Going on isn't approving; Claude can't approve; a person can.
	more := 1.0
	if _, err := call[RecipeRunOut](t, f.a, "recipe.continue", RecipeContinueIn{Run: run.ID, MoreUSD: &more}, true); codeOf(err) != apperr.Conflict {
		t.Fatalf("go on refused while the sample waits: %v", err)
	}
	if _, err := call[RecipeSampleOut](t, f.a, "recipe.sample", RecipeSampleIn{Run: run.ID, Decision: "approve"}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("people only: %v", err)
	}
	list := must[ops.TodoListOut](t, f.a, "todos.list", ops.TodoListIn{Kind: models.TodoRecipeRun}, false)
	if len(list.Todos) != 1 || len(list.Todos[0].Suggestions) != 1 || list.Todos[0].Suggestions[0].Value != "stop" {
		t.Fatalf("the to-do offers no quiet approval: %+v", list.Todos)
	}
	ok := must[RecipeSampleOut](t, f.a, "recipe.sample", RecipeSampleIn{Run: run.ID, Decision: "approve"}, true)
	a := ok.Run
	if a.Sample.State != models.SampleApproved || a.Sample.By == nil || a.Sample.Rest == nil {
		t.Fatalf("approved, the rest planned: %+v", a.Sample)
	}
	if got := stepIDs(a.Steps); !strings.HasPrefix(got, "inputs,place,sample,approve,assemble") || !strings.Contains(got, "voice") {
		t.Fatalf("the rest's steps follow the sample's: %s", got)
	}
	if a.State != models.RunWaiting || a.WaitingUSD <= 0 || !strings.Contains(ok.Message, "agree to it") {
		t.Fatalf("the rest asks for its own agreement: %s %v %q", a.State, a.WaitingUSD, ok.Message)
	}
	// The rest's estimate: line 1 was made by the sample (free), line 2 is paid.
	var free, paid int
	for _, p := range a.Estimate.Parts {
		if p.Free {
			free++
		} else if p.USD > 0 {
			paid++
		}
	}
	if free != 1 || paid != 1 {
		t.Fatalf("the sample's line is used again: %+v", a.Estimate.Parts)
	}
	more = a.WaitingUSD
	must[RecipeRunOut](t, f.a, "recipe.continue", RecipeContinueIn{Run: run.ID, MoreUSD: &more}, true)
	r = f.advance(run.ID)
	if v := stepOf(r, stepVoice); v == nil || v.State != models.StepDone || !strings.Contains(v.Detail, "1 line reused") {
		t.Fatalf("the voice reuses the sample's line: %+v", v)
	}
	f.finishReview(run.ID)
	if done := f.advance(run.ID); done.State != models.RunDone || done.Spent.USD > done.Agreed.UpTo+0.001 {
		t.Fatalf("done within what was agreed: %s %+v / %+v", done.State, done.Spent, done.Agreed)
	}
}

// "Not quite" stops the run with the person's words; skipping the sample
// needs a reason, which the run keeps.
func TestASampleNotQuiteAndSkipped(t *testing.T) {
	f := setup(t)
	cheapSamples(t)
	f.save(RecipeSaveIn{})
	in := RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: "voice", Text: "Cleo"}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	usd := plan.Sample.Estimate.USD
	advanceOnStart = false
	defer func() { advanceOnStart = true }()
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint, MaxUSD: &usd}, true).Run
	r := f.advance(run.ID)
	f.finishRender(r.Sample.Render)
	f.advance(run.ID)
	if _, err := call[RecipeSampleOut](t, f.a, "recipe.sample", RecipeSampleIn{Run: run.ID, Decision: "not-quite"}, true); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("not quite needs words: %v", err)
	}
	nq := must[RecipeSampleOut](t, f.a, "recipe.sample", RecipeSampleIn{Run: run.ID, Decision: "not-quite", Note: "too bright for this"}, true)
	if nq.Run.State != models.RunStopped || nq.Run.Sample.State != models.SampleNotQuite || !strings.Contains(nq.Run.Problem, "too bright for this") {
		t.Fatalf("stopped with the note: %+v", nq.Run.RecipeRun)
	}
	// Skipping.
	plan = must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	all := plan.Estimate.USD
	skip := RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint, MaxUSD: &all, SkipSample: "ok"}
	if _, err := call[RecipeRunOut](t, f.a, "recipe.run", skip, true); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("a reason is needed: %v", err)
	}
	skip.SkipSample = "We used this voice last week"
	sk := must[RecipeRunOut](t, f.a, "recipe.run", skip, true).Run
	if strings.Contains(stepIDs(sk.Steps), "sample") || sk.Sample == nil || sk.Sample.State != models.SampleSkipped || sk.Sample.Skipped != "We used this voice last week" || sk.Agreed.UpTo < all {
		t.Fatalf("skipped with its reason: %s / %+v / %+v", stepIDs(sk.Steps), sk.Sample, sk.Agreed)
	}
	// Under the floor, no sample at all (the fakes' prices are tiny).
	SampleFloorUSD = 0.10
	if p := must[RecipePlanOut](t, f.a, "recipe.plan", in, false); p.Sample != nil {
		t.Fatalf("cheap work needs no sample: %+v", p.Sample)
	}
}

// A run with a generated shot starts with one shot as its sample (D294): the
// shot is made from the run's words within the sample's price and shown in
// the sample's cut; once a person approves it, the rest is planned without
// that shot (it is the run's shot: not made or paid twice).
func TestASampleOfAShot(t *testing.T) {
	f := setup(t)
	generate.PollEvery, generate.FirstPoll = 40*time.Millisecond, 5*time.Millisecond
	key := "AIza" + strings.Repeat("S", 35)
	fake := fakevideogen.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(videogen.EnvGeminiBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(videogen.GeminiName), "")
	must[providers.Status](t, f.a, "provider.set", providers.SetIn{Name: videogen.GeminiName, Key: key}, true)
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
	spec := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
		Clips: []editspec.Clip{{ID: "boat", AssetID: gv.Clips[0].AssetID, In: 0, Out: 3, Label: "the boat"}, {ID: "hook", AssetID: f.interview, In: 1, Out: 4, Label: "the hook"}}}
	rev := f.apply(editspec.Plan{ProjectID: f.teaser, Branch: "boats", Instruction: "boats", Spec: spec})
	must[ops.RevisionSummary](t, f.a, "revision.approve", ops.RevisionApproveIn{ID: rev}, true)
	must[RecipeSaveOut](t, f.a, "recipe.save", RecipeSaveIn{From: f.teaser, Branch: "boats", Title: "Boats", Name: "boats", NewProjects: true, Vary: []string{"boat-prompt"}}, true)

	in := RecipePlanIn{Recipe: "boats", Inputs: []RecipeInputValue{{Name: "boat-prompt", Text: "a blue paper boat on a pond"}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if plan.Sample == nil || len(plan.Sample.Kinds) != 1 || plan.Sample.Kinds[0].Kind != models.SampleShots || plan.Sample.Kinds[0].Clip != "boat" {
		t.Fatalf("a generated shot is expensive: one shot first: %+v", plan.Sample)
	}
	advanceOnStart = false
	defer func() { advanceOnStart = true }()
	usd := plan.Sample.Estimate.USD
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint, MaxUSD: &usd}, true).Run
	r := f.advance(run.ID)
	st := stepOf(r, stepSample)
	if st == nil || len(st.Jobs) != 1 || r.ProjectID == "" {
		t.Fatalf("the sample asks for one shot in the run's new project: %+v / %s", st, r.ProjectID)
	}
	runJob(st.Jobs[0])
	r = f.advance(run.ID)
	if r.Made["boat"].AssetID == "" || r.Sample.Render == "" || r.Sample.Branch != "sample" {
		t.Fatalf("the shot, the sample's cut and its review copy: %+v / %+v", r.Made, r.Sample)
	}
	if c := f.revision(r.Sample.Revision).Spec.Clips; len(c) != 1 || c[0].AssetID != r.Made["boat"].AssetID {
		t.Fatalf("the sample shows the shot: %+v", c)
	}
	f.finishRender(r.Sample.Render)
	f.advance(run.ID)
	calls := fake.Calls("create")
	ok := must[RecipeSampleOut](t, f.a, "recipe.sample", RecipeSampleIn{Run: run.ID, Decision: "approve"}, true).Run
	if stepOf(&ok.RecipeRun, stepShots) != nil || ok.WaitingUSD != 0 {
		t.Fatalf("the shot isn't planned again, nothing more to agree to: %s / %v %q", stepIDs(ok.Steps), ok.WaitingUSD, ok.Waiting)
	}
	r = f.advance(run.ID)
	f.finishReview(run.ID)
	done := f.advance(run.ID)
	if done.State != models.RunDone || fake.Calls("create") != calls {
		t.Fatalf("done without another shot: %s (calls %d → %d)", done.State, calls, fake.Calls("create"))
	}
	if head := f.revision(done.Revision); head.Spec.Clips[0].AssetID != done.Made["boat"].AssetID {
		t.Fatalf("the run's cut plays the sample's shot: %+v", head.Spec.Clips[0])
	}
}

// startSample starts a run of the teaser in a new voice that makes a sample
// first, agreeing to the sample's price, and returns it after its first look.
func startSample(t *testing.T, f *fx, voice string) *models.RecipeRun {
	t.Helper()
	in := RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: "voice", Text: voice}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if plan.Sample == nil {
		t.Fatalf("no sample: %+v", plan)
	}
	usd := plan.Sample.Estimate.USD
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint, MaxUSD: &usd}, true).Run
	return f.advance(run.ID)
}

// Review of PR #18, item 1: a run again samples the expensive kinds whose
// inputs changed (a new voice), not those a person approved and that didn't
// change; its rest is planned on the cut it makes again.
func TestARunAgainSamplesWhatChanged(t *testing.T) {
	f := setup(t)
	noSamples(t)
	f.save(RecipeSaveIn{})
	p1 := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	advanceOnStart = false
	defer func() { advanceOnStart = true }()
	r1 := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: p1.Fingerprint}, true).Run
	f.advance(r1.ID)
	f.finishReview(r1.ID)
	if done := f.advance(r1.ID); done.State != models.RunDone {
		t.Fatalf("run 1: %s", done.State)
	}
	SampleFloorUSD = 0
	// A new voice: the AI voice is new work, so its sample comes first.
	in := RecipePlanIn{Repeats: "app-teaser#1", Inputs: []RecipeInputValue{{Name: "voice", Text: "Bram"}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if plan.Sample == nil || len(plan.Sample.Kinds) != 1 || plan.Sample.Kinds[0].Kind != models.SampleVoice || !strings.HasPrefix(stepIDs(plan.Steps), "inputs,place,sample,approve,") {
		t.Fatalf("again with a new voice samples it: %+v / %s", plan.Sample, stepIDs(plan.Steps))
	}
	usd := plan.Sample.Estimate.USD
	again := must[RecipeRunOut](t, f.a, "recipe.again", RecipeAgainIn{Run: "app-teaser#1", Inputs: in.Inputs, Fingerprint: plan.Fingerprint, MaxUSD: &usd}, true).Run
	r := f.advance(again.ID)
	if r.Sample == nil || r.Sample.Branch != r1.Branch+"-sample" || r.Sample.Render == "" {
		t.Fatalf("the sample, beside the cut it makes again: %+v", r.Sample)
	}
	f.finishRender(r.Sample.Render)
	f.advance(again.ID)
	ok := must[RecipeSampleOut](t, f.a, "recipe.sample", RecipeSampleIn{Run: again.ID, Decision: "approve"}, true).Run
	if ok.Branch != r1.Branch || !strings.HasPrefix(stepIDs(ok.Steps), "inputs,place,sample,approve,assemble") {
		t.Fatalf("the rest makes a new version of the same cut: %s / %s", ok.Branch, stepIDs(ok.Steps))
	}
	if ok.WaitingUSD > 0 {
		more := ok.WaitingUSD
		must[RecipeRunOut](t, f.a, "recipe.continue", RecipeContinueIn{Run: again.ID, MoreUSD: &more}, true)
	}
	r = f.advance(again.ID)
	if v := stepOf(r, stepVoice); v == nil || v.State != models.StepDone || !strings.Contains(v.Detail, "reused") {
		t.Fatalf("the voice reuses the sample's line: %+v", v)
	}
	f.finishReview(again.ID)
	if done := f.advance(again.ID); done.State != models.RunDone || done.Repeats != r1.ID {
		t.Fatalf("done: %s %q", done.State, done.Waiting)
	}
	// What a person approved and that didn't change needs no new sample.
	approved := approvedKinds(f.ctx, f.a, &models.RecipeRun{Sample: &models.RecipeSample{State: models.SampleApproved, Kinds: []models.RecipeSampleKind{{Kind: models.SampleVoice}}}})
	if !approved[models.SampleVoice] || approved[models.SampleShots] {
		t.Fatalf("approved kinds: %v", approved)
	}
	doc := &models.RecipeDoc{Inputs: []models.RecipeInput{{Name: "voice", Kind: models.RecipeVoice}, {Name: "headline", Kind: models.RecipeWords}},
		Bindings: []models.RecipeBinding{{Input: "voice", To: models.BindVoice}, {Input: "headline", To: "param:end.headline"}}}
	if kindChanged(&planned{Doc: doc, Changed: map[string]bool{"headline": true}}, models.SampleVoice) ||
		!kindChanged(&planned{Doc: doc, Changed: map[string]bool{"voice": true}}, models.SampleVoice) {
		t.Fatal("only the voice's own inputs change the AI voice")
	}
}

// Review of PR #18, items 2 and 3: a run stopped while its sample is made keeps
// what the sample spent (its record and the receipt), and a sample whose run
// stopped can't be approved any more.
func TestAStoppedRunsSampleKeepsItsCost(t *testing.T) {
	f := setup(t)
	cheapSamples(t)
	f.save(RecipeSaveIn{})
	advanceOnStart = false
	defer func() { advanceOnStart = true }()
	r := startSample(t, f, "Bram")
	if r.Spent.USD <= 0 || r.Sample.Spent.USD != r.Spent.USD || r.Sample.State != models.SampleMaking {
		t.Fatalf("what the sample spent so far is the sample's: run %+v, sample %+v (%s)", r.Spent, r.Sample.Spent, r.Sample.State)
	}
	stopped := must[RecipeRunOut](t, f.a, "recipe.stop", RecipeStopIn{Run: r.ID}, true).Run
	if stopped.Sample.State != models.SampleStopped || stopped.Sample.Spent.USD <= 0 || !strings.Contains(stopped.SampleWords, "stopped before anyone decided") {
		t.Fatalf("the sample ends with its run: %+v / %q", stopped.Sample, stopped.SampleWords)
	}
	var p models.Project
	if err := f.a.DB.GetByID(f.ctx, store.CProjects, f.a.WorkspaceID(), r.ProjectID, &p); err != nil {
		t.Fatal(err)
	}
	rc, err := receipt.Receipt(f.ctx, f.a, &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Samples) != 1 || rc.Samples[0].USD <= 0 || rc.Samples[0].State != models.SampleStopped || !strings.Contains(strings.Join(rc.Lines, " "), "stopped before anyone decided") {
		t.Fatalf("the receipt keeps the sample's cost: %+v / %v", rc.Samples, rc.Lines)
	}
	// Stopped while the sample waits: deciding on it is refused, and says why.
	w := startSample(t, f, "Cleo")
	f.finishRender(w.Sample.Render)
	if w = f.advance(w.ID); w.Sample.State != models.SampleWaiting {
		t.Fatalf("waits: %+v", w.Sample)
	}
	must[RecipeRunOut](t, f.a, "recipe.stop", RecipeStopIn{Run: w.ID}, true)
	_, err = call[RecipeSampleOut](t, f.a, "recipe.sample", RecipeSampleIn{Run: w.ID, Decision: "approve"}, true)
	if codeOf(err) != apperr.Conflict || !strings.Contains(apperr.From(err).Message, "already ended") {
		t.Fatalf("a stopped run's sample can't be approved: %v", err)
	}
	if x, _ := loadRun(f.ctx, f.a, w.ID); x.Sample.State != models.SampleStopped {
		t.Fatalf("its sample says it stopped: %+v", x.Sample)
	}
}

// Review of PR #18, item 4: the sample line is the shortest line that needs
// the AI voice, whole, so the run reuses it and pays for no part of it twice.
func TestTheSampleLineIsAWholeLineTheRunReuses(t *testing.T) {
	f := setup(t)
	cheapSamples(t)
	f.save(RecipeSaveIn{})
	show := must[RecipeShowOut](t, f.a, "recipe.show", RecipeRefIn{Recipe: "app-teaser"}, false)
	beats := show.Doc.Cut.Voiceover.Beats
	long := strings.Repeat("Meet the app that reads you a story every night, in a voice you like. ", 4)
	beats[0].Text = strings.TrimSpace(long)
	patch, _ := json.Marshal(map[string]any{"cut": map[string]any{"voiceover": map[string]any{"beats": beats}}})
	must[RecipeUpdateOut](t, f.a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "app-teaser"}, Patch: patch}, true)
	must[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "app-teaser"}, true)
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: "voice", Text: "Bram"}}}, false)
	if plan.Sample == nil || plan.Sample.Kinds[0].Line != "line-2" || plan.Sample.Kinds[0].Text != "Read free tonight." {
		t.Fatalf("the shortest line, whole: %+v", plan.Sample)
	}
	advanceOnStart = false
	defer func() { advanceOnStart = true }()
	r := startSample(t, f, "Bram")
	f.finishRender(r.Sample.Render)
	f.advance(r.ID)
	ok := must[RecipeSampleOut](t, f.a, "recipe.sample", RecipeSampleIn{Run: r.ID, Decision: "approve"}, true).Run
	free := 0
	for _, pt := range ok.Estimate.Parts {
		if pt.Free {
			free++
			if !strings.Contains(pt.Source, "Read free tonight.") {
				t.Fatalf("the free line is the sample's: %+v", pt)
			}
		}
	}
	if free != 1 {
		t.Fatalf("the rest reuses the sample's line: %+v", ok.Estimate.Parts)
	}
}

// Review of PR #18, item 5: the person who approves the sample agrees to the
// rest by their own "estimate costs first" preference, whoever started the run.
func TestTheApproverAgreesToTheRest(t *testing.T) {
	f := setup(t)
	cheapSamples(t)
	f.save(RecipeSaveIn{})
	advanceOnStart = false
	defer func() { advanceOnStart = true }()
	if err := f.a.DB.Insert(f.ctx, store.CUsers, models.User{ID: f.a.Cfg.User.ID, WorkspaceID: f.a.WorkspaceID(), Name: "Ada", Email: "ada@example.com", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	off, on := false, true
	// Started with estimates off; approved with them on: the rest waits for an amount.
	must[prefs.PreferencesOut](t, f.a, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &off}, true)
	in := RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: "voice", Text: "Bram"}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint}, true).Run
	if !run.Agreed.Skipped || run.Sample == nil {
		t.Fatalf("started without an estimate, a sample first: %+v / %+v", run.Agreed, run.Sample)
	}
	r := f.advance(run.ID)
	f.finishRender(r.Sample.Render)
	f.advance(run.ID)
	must[prefs.PreferencesOut](t, f.a, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &on}, true)
	ok := must[RecipeSampleOut](t, f.a, "recipe.sample", RecipeSampleIn{Run: run.ID, Decision: "approve"}, true).Run
	if ok.Agreed.Skipped || ok.State != models.RunWaiting || ok.WaitingUSD <= 0 {
		t.Fatalf("the approver's estimates are on: the rest asks for an amount: %+v / %s %v", ok.Agreed, ok.State, ok.WaitingUSD)
	}
	// Started with estimates on; approved with them off: the rest goes on without a price question.
	r2 := startSample(t, f, "Cleo")
	f.finishRender(r2.Sample.Render)
	f.advance(r2.ID)
	must[prefs.PreferencesOut](t, f.a, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &off}, true)
	ok2 := must[RecipeSampleOut](t, f.a, "recipe.sample", RecipeSampleIn{Run: r2.ID, Decision: "approve"}, true).Run
	if !ok2.Agreed.Skipped || ok2.WaitingUSD != 0 || (ok2.State == models.RunWaiting && ok2.WaitingFor == models.RunWaitsForPerson) {
		t.Fatalf("the approver's estimates are off: no price question: %+v / %s %q", ok2.Agreed, ok2.State, ok2.Waiting)
	}
}

// Review of PR #18, starter content: a range of lengths is said in one unit,
// or each side in its own — never "60–180 min" for a minute to three hours.
func TestLengthRangesSayOneUnit(t *testing.T) {
	for _, c := range []struct {
		lo, hi float64
		want   string
	}{{25, 35, "25–35 s"}, {60, 10800, "1–180 min"}, {45, 90, "45 s–1 min 30 s"}, {30, 59, "30–59 s"}, {90, 150, "1 min 30 s–2 min 30 s"}} {
		if got := secondsRange(c.lo, c.hi); got != c.want {
			t.Errorf("secondsRange(%v, %v) = %q, want %q", c.lo, c.hi, got, c.want)
		}
	}
	sec := callEpisodeStarter().doc.Sections[0]
	if got := sectionNeeds(sec); got != "1–12 moments, 1–180 min in all" {
		t.Fatalf("the call starter's episode: %q", got)
	}
}
