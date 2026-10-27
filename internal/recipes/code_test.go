package recipes

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
	"github.com/metavert-io/metarush/internal/videogen"
)

// A clip made from a Remotion composition whose words are settings (D168):
// the recipe offers its words as an input, and a run with new words draws the
// same approved version again with them — no new code to approve — and puts
// the new clip in the fixed section.
func TestNewWordsDrawTheCompositionAgain(t *testing.T) {
	f := setup(t)
	ws := f.a.WorkspaceID()
	now := time.Now().UTC()
	src := models.RemotionSource{ID: ids.New(ids.RemotionSource), WorkspaceID: ws, ProjectID: f.brand, Name: "teaser", Title: "Teaser", Versions: 1, CreatedAt: now, UpdatedAt: now}
	hash := strings.Repeat("ab", 32)
	ver := models.RemotionVersion{ID: ws + "/" + hash, WorkspaceID: ws, SourceID: src.ID, ProjectID: f.brand, Hash: hash, Number: 1, Kind: "import", State: models.RemotionApproved,
		PackagesAllowed: true, At: now, CreatedAt: now, UpdatedAt: now,
		Compositions: []models.RemotionComposition{{ID: "Open", Width: 640, Height: 360, FPS: 30, DurationInFrames: 150, Duration: 5, Words: []string{"OPEN"}}},
		Settings:     []models.RemotionWordSetting{{Key: "OPEN.title", Text: "Hello there", OnScreen: true}, {Key: "OPEN.color", Text: "#ff0000"}}}
	src.Head, src.HeadNumber, src.Approved, src.ApprovedNumber = hash, 1, hash, 1
	if err := f.a.DB.Insert(f.ctx, store.CRemotionSources, src); err != nil {
		t.Fatal(err)
	}
	if err := f.a.DB.Insert(f.ctx, store.CRemotionVersions, ver); err != nil {
		t.Fatal(err)
	}
	// The brand intro is a render of that composition.
	if err := f.a.DB.Update(f.ctx, store.CAssets, ws, f.intro, bson.M{"$set": bson.M{"source": models.AssetSource{Platform: models.PlatformRemotion,
		Remotion: &models.RemotionClipSource{SourceID: src.ID, SourceName: "teaser", SourceTitle: "Teaser", Version: hash, VersionNumber: 1, Kind: models.SourceRemotionRender, Composition: "Open"}}}}); err != nil {
		t.Fatal(err)
	}
	saved := f.save(RecipeSaveIn{})
	var words *RecipeProposalItem
	for i, it := range saved.Proposal {
		if it.Key == "open-title" {
			words = &saved.Proposal[i]
		}
	}
	if words == nil || !words.Varies {
		t.Fatalf("the composition's words are an input: %+v", saved.Proposal)
	}
	in := RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: "open-title", Text: "Stories at bedtime"}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if !plan.Ready || plan.Steps[2].ID != stepCode || plan.Estimate.About != "nothing" {
		t.Fatalf("plan %+v", plan)
	}
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint}, true).Run
	code := stepOf(&run.RecipeRun, stepCode)
	if code.State != models.StepRunning || code.Work["brand"] == "" || len(code.Jobs) != 1 {
		t.Fatalf("drawing it: %+v", code)
	}
	var rr models.RemotionRender
	if err := f.a.DB.GetByID(f.ctx, store.CRemotionRenders, ws, ws+"/"+code.Work["brand"], &rr); err != nil {
		t.Fatal(err)
	}
	if rr.Words != `{"OPEN.title":"Stories at bedtime"}` || rr.Version != hash || rr.Composition != "Open" {
		t.Fatalf("render %+v", rr)
	}
	// The source's own words don't change (other cuts keep theirs).
	var after models.RemotionSource
	_ = f.a.DB.GetByID(f.ctx, store.CRemotionSources, ws, src.ID, &after)
	if after.Words != nil {
		t.Fatalf("the source's settings changed: %+v", after.Words)
	}
	// A worker draws it (here: the clip it made is ready).
	if _, err := f.a.DB.C(store.CRemotionRenders).UpdateOne(f.ctx, bson.M{"_id": ws + "/" + code.Work["brand"]}, bson.M{"$set": bson.M{"state": models.RemotionRenderDone, "assetId": f.interview2, "duration": 5.0}}); err != nil {
		t.Fatal(err)
	}
	moved := f.advance(run.ID)
	if stepOf(moved, stepCode).State != models.StepDone || moved.Made["brand"].AssetID != f.interview2 {
		t.Fatalf("made %+v", moved.Made)
	}
	rev := f.revision(moved.Revision)
	if c := rev.Spec.Clips[0]; c.ID != "brand" || c.AssetID != f.interview2 || !c.Locked || c.Out != 3 {
		t.Fatalf("the fixed section shows the new clip: %+v", c)
	}
	f.finishReview(run.ID)
	if done := f.advance(run.ID); done.State != models.RunDone {
		t.Fatalf("done %+v", done.Steps)
	}
}

// A generated shot made again from new words (a fake Gemini): priced in
// dollars first, made only within the agreed amount, its clip in the section.
func TestGeneratedShotFromNewWords(t *testing.T) {
	f := setup(t)
	noSamples(t) // about something else: the sample first is TestASampleOfAShot
	generate.PollEvery, generate.FirstPoll = 40*time.Millisecond, 5*time.Millisecond
	key := "AIza" + strings.Repeat("Q", 35)
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
	boat := gv.Clips[0].AssetID
	spec := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
		Clips: []editspec.Clip{{ID: "boat", AssetID: boat, In: 0, Out: 3, Label: "the boat"}, {ID: "hook", AssetID: f.interview, In: 1, Out: 4, Label: "the hook"}}}
	rev := f.apply(editspec.Plan{ProjectID: f.teaser, Branch: "boats", Instruction: "boats", Spec: spec})
	must[ops.RevisionSummary](t, f.a, "revision.approve", ops.RevisionApproveIn{ID: rev}, true)
	saved := must[RecipeSaveOut](t, f.a, "recipe.save", RecipeSaveIn{From: f.teaser, Branch: "boats", Title: "Boats", Vary: []string{"boat-prompt"}}, true)
	if saved.Recipe == nil || saved.Recipe.Current != 1 {
		t.Fatalf("saved %+v", saved)
	}
	in := RecipePlanIn{Recipe: "boats", Inputs: []RecipeInputValue{{Name: "boat-prompt", Text: "a blue paper boat on a pond"}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if !plan.Ready || plan.Estimate.USD <= 0 || len(plan.Estimate.Parts) == 0 || !strings.HasPrefix(plan.Estimate.About, "about $") {
		t.Fatalf("plan %+v", plan)
	}
	for _, pt := range plan.Estimate.Parts {
		if pt.Step != "Make the generated shots" || pt.Service != est.ProviderTitle || pt.Model == "" {
			t.Fatalf("part %+v", pt)
		}
	}
	if stepOf(&models.RecipeRun{Steps: plan.Steps}, stepShots).Estimate != plan.Estimate.USD {
		t.Fatalf("the shot's own estimate is what the run is held to: %v / %+v", plan.Estimate.USD, plan.Steps)
	}
	calls := fake.Calls("create")
	if _, err := call[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint}, true); codeOf(err) == "" {
		t.Fatal("the estimate must be agreed")
	}
	maxUSD := plan.Estimate.USD
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint, MaxUSD: &maxUSD}, true).Run
	shots := stepOf(&run.RecipeRun, stepShots)
	if shots.State != models.StepRunning || len(shots.Jobs) != 1 || fake.Calls("create") != calls {
		t.Fatalf("asked for the shot: %+v (calls %d)", shots, fake.Calls("create"))
	}
	runJob(shots.Jobs[0])
	moved := f.advance(run.ID)
	if moved.Spent.USD <= 0 || moved.Spent.USD > maxUSD+0.001 || moved.Made["boat"].AssetID == "" || moved.Made["boat"].AssetID == boat {
		t.Fatalf("made %+v spent %+v", moved.Made, moved.Spent)
	}
	head := f.revision(moved.Revision)
	if head.Spec.Clips[0].AssetID != moved.Made["boat"].AssetID || !head.Spec.Clips[0].Locked {
		t.Fatalf("the new shot in the fixed section: %+v", head.Spec.Clips[0])
	}
	f.finishReview(run.ID)
	done := f.advance(run.ID)
	if done.State != models.RunDone {
		t.Fatalf("done %+v", done.Steps)
	}
	if !strings.Contains(stepOf(done, stepShots).Detail, "$") {
		t.Fatalf("what it spent: %q", stepOf(done, stepShots).Detail)
	}
}
