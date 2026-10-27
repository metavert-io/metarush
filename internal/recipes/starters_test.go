package recipes

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/receipt"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

// freshApp is a fresh workspace: nothing in it but one empty project, whose
// Content Settings name a narration voice (Ada) of a fake ElevenLabs.
func freshApp(t *testing.T) *fx {
	t.Helper()
	keyring.MockInit()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	ws := ids.New(ids.Workspace)
	cfg.Workspace = config.Ref{ID: ws, Name: "Fresh"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "studio"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "Ada"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")}}
	cfg.Secrets = "env"
	ctx := context.Background()
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	now := time.Now().UTC()
	_ = a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "Fresh", CreatedAt: now})
	// One project, whose Content Settings new projects use (the narration voice).
	_ = a.DB.Insert(ctx, store.CProjects, models.Project{ID: ids.New(ids.Project), WorkspaceID: ws, Name: "House", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now})
	if _, err := store.MoveContentSettings(ctx, a.DB, ws); err != nil {
		t.Fatal(err)
	}
	f := &fx{t: t, a: a, ctx: ctx}
	key := "sk" + "_" + strings.Repeat("ef56", 10) + "cafe"
	f.eleven = fakeeleven.New(key)
	t.Cleanup(f.eleven.Close)
	t.Setenv(providers.EnvElevenLabsBaseURL, f.eleven.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	must[providers.Status](t, a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}, true)
	must[providers.SettingsOut](t, a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, Voice: "Ada"}, true)
	return f
}

// The starters (D293): two, each saying what it is and the first-party
// abilities its runs use (every one in the first-party catalogue); added, each
// comes in as a draft — never current by itself — that passes recipe.check on a
// fresh workspace, every graphic a vetted template; adding again brings nothing;
// a person makes one current, and it plans.
func TestStartersComeInAsDraftsAndCheckOut(t *testing.T) {
	f := freshApp(t)
	b, err := os.ReadFile(filepath.Join(repoRoot(), "internal", "catalog", "firstparty.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cat struct {
		Abilities []struct {
			Name string `json:"name"`
		} `json:"abilities"`
	}
	if err := json.Unmarshal(b, &cat); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{}
	for _, x := range cat.Abilities {
		known[x.Name] = true
	}
	list := must[RecipeStartersOut](t, f.a, "recipe.starters", RecipeStartersIn{}, false)
	if len(list.Starters) != 2 || list.Starters[0].Name != "call-episode" || list.Starters[1].Name != "release-video" {
		t.Fatalf("two starters: %+v", list.Starters)
	}
	for _, s := range list.Starters {
		if s.State != "new" || s.Here != nil || s.Version < 1 || len(s.Uses) == 0 || len(s.Gives) == 0 {
			t.Fatalf("not added yet: %+v", s)
		}
		for _, u := range s.Uses {
			if !known[u] {
				t.Errorf("%s uses %q, which isn't in the first-party catalogue", s.Name, u)
			}
		}
	}
	// Claude may add them: they are drafts.
	added := must[RecipeStartersAddOut](t, f.a, "recipe.starters.add", RecipeStartersAddIn{}, false)
	if len(added.Added) != 2 || added.Project != StarterProject {
		t.Fatalf("added %+v", added)
	}
	for _, x := range added.Added {
		if !x.New || x.Recipe == nil || x.Recipe.Current != 0 || x.Version == nil || x.Version.State != models.RecipeDraft || !x.Version.Imported {
			t.Fatalf("a draft, not current: %+v / %+v", x.Recipe, x.Version)
		}
		chk := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Recipe: x.Name}, false)
		if !chk.Check.OK {
			t.Fatalf("%s checks out on a fresh workspace: %+v", x.Name, chk.Check.Problems)
		}
		r, _ := loadRecipe(f.ctx, f.a, x.Name)
		v, _ := loadVersion(f.ctx, f.a, r, "")
		for _, o := range v.Doc.Cut.Overlays {
			if _, ok := editspec.TemplateByID(o.Template); !ok || o.Motion != nil {
				t.Fatalf("%s: every graphic is a vetted template: %+v", x.Name, o)
			}
		}
	}
	// The release starter's example code slides say where they came from, as a code
	// slide made from a repository does: repo@commit:path:lines.
	home, err := starterHome(f.ctx, f.a)
	if err != nil {
		t.Fatal(err)
	}
	e, err := resources.Find(f.ctx, f.a, home.ID, nil, "example-code-1")
	if err != nil {
		t.Fatal(err)
	}
	if got := e.Resource.Latest().Source.Provenance(); got != "github.com/acme/widget@1a2b3c4d:queue/worker.go:18-26" {
		t.Fatalf("the example code slide's provenance: %q", got)
	}
	again := must[RecipeStartersAddOut](t, f.a, "recipe.starters.add", RecipeStartersAddIn{Only: []string{"release-video"}}, false)
	if len(again.Added) != 1 || !again.Added[0].Same || !strings.Contains(again.Message, "nothing new") {
		t.Fatalf("again: %+v", again)
	}
	if _, err := call[RecipeStartersAddOut](t, f.a, "recipe.starters.add", RecipeStartersAddIn{Only: []string{"nope"}}, false); codeOf(err) != "E_NOT_FOUND" {
		t.Fatalf("an unknown starter: %v", err)
	}
	list = must[RecipeStartersOut](t, f.a, "recipe.starters", RecipeStartersIn{}, false)
	for _, s := range list.Starters {
		if s.State != "here" || s.Here == nil {
			t.Fatalf("here now: %+v", s)
		}
	}
	// Not current: it can't run until a person makes it current.
	if p := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "release-video"}, false); p.Ready {
		t.Fatalf("a draft doesn't run: %+v", p)
	}
	must[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "release-video"}, true)
	p := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "release-video"}, false)
	if !p.Ready || !p.NewProject || p.Project != "Widget 2.4: release video" {
		t.Fatalf("it plans: %+v", p)
	}
	voice := false
	for _, in := range p.Inputs {
		voice = voice || (in.Kind == models.RecipeVoice && len(in.Items) == 1 && in.Items[0].Title == "Ada")
	}
	if !voice || stepOf(&models.RecipeRun{Steps: p.Steps}, stepVoice) == nil || p.Estimate.USD <= 0 {
		t.Fatalf("the narration reads in the Content Settings' voice, priced first: %+v / %+v", p.Inputs, p.Estimate)
	}
}

// The call starter end to end (D293–D296): a call recording with words, a
// sample first — a stretch made clearer in the cloud — approved by a person,
// then the rest: the episode (the call cleaned up, its clearer voice following
// the new footage), shorts of the footage as new cuts of the run's project,
// and the delivery notes left to write before it goes out.
func TestCallStarterRunsWithASampleFirst(t *testing.T) {
	f := freshApp(t)
	must[RecipeStartersAddOut](t, f.a, "recipe.starters.add", RecipeStartersAddIn{Only: []string{"call-episode"}}, true)
	must[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "call-episode"}, true)
	gallery := filepath.Join(repoRoot(), "tests", "fixtures", "gen", "conversation", "gallery.mp4")
	if _, err := os.Stat(gallery); err != nil {
		t.Skip("the call fixture is made by scripts/make-conversation-fixtures.sh (macOS `say`)")
	}
	b, err := os.ReadFile(filepath.Join(repoRoot(), "tests", "fixtures", "truth", "conversation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tr callTruth
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatal(err)
	}
	pr := must[ops.ProjectOut](t, f.a, "project.create", ops.ProjectCreateIn{Name: "Calls"}, true)
	r := ingest.File(f.ctx, f.a, gallery, ingest.Options{ProjectID: pr.Project.ID, Via: "cli"})
	if r.AssetID == "" {
		t.Fatalf("ingest %+v", r)
	}
	var turns []models.TranscriptSegment
	for _, x := range tr.Turns {
		turns = append(turns, models.TranscriptSegment{From: math.Max(0, x.Start-0.5), To: x.End - 0.5, Text: x.Text, Speaker: "speaker_0"})
	}
	f.wordTranscript(r.AssetID, turns)

	in := RecipePlanIn{Recipe: "call-episode", Inputs: []RecipeInputValue{{Name: "call", Items: []string{r.AssetID}}, {Name: "episode", Text: "Episode 7: the garden"}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if !plan.Ready || plan.Project != "Episode 7: the garden" || plan.Sample == nil || len(plan.Sample.Kinds) != 1 || plan.Sample.Kinds[0].Kind != models.SampleCleanUp {
		t.Fatalf("a sample of the clean-up first: %+v / %+v", plan.Sample, plan.Problems)
	}
	k := plan.Sample.Kinds[0]
	if k.AssetID != r.AssetID || k.To-k.From > 30.001 || k.USD >= k.RunUSD || len(plan.Pack) == 0 {
		t.Fatalf("one stretch of the call, cheaper than the whole; shorts of the footage: %+v / %+v", k, plan.Pack)
	}
	for _, o := range plan.Pack {
		if o.AssetID != r.AssetID || o.To-o.From < 20-0.5 || o.To-o.From > 60.5 {
			t.Fatalf("a short of the call: %+v", o)
		}
	}
	advanceOnStart = false
	defer func() { advanceOnStart = true }()
	usd := plan.Sample.Estimate.USD
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint, MaxUSD: &usd}, true).Run
	st := f.advance(run.ID)
	if st.Sample == nil || st.Sample.Render == "" {
		t.Fatalf("the sample and its review copy: %+v / %+v", st.Sample, st.Steps)
	}
	sample := f.revision(st.Sample.Revision)
	if sample.Spec.Mix == nil || len(sample.Spec.Mix.Enhanced) == 0 || sample.Spec.Clips[0].AssetID != r.AssetID {
		t.Fatalf("the stretch made clearer: %+v", sample.Spec.Mix)
	}
	f.finishRender(st.Sample.Render)
	st = f.advance(run.ID)
	if st.Sample.State != models.SampleWaiting || st.Sample.Spent.USD <= 0 {
		t.Fatalf("the sample waits for a person, its cost recorded: %+v", st.Sample)
	}
	ok := must[RecipeSampleOut](t, f.a, "recipe.sample", RecipeSampleIn{Run: run.ID, Decision: "approve", Note: "Clear enough"}, true).Run
	more := ok.WaitingUSD
	if more <= 0 {
		t.Fatalf("the rest asks for its agreement: %+v", ok.RecipeRun)
	}
	must[RecipeRunOut](t, f.a, "recipe.continue", RecipeContinueIn{Run: run.ID, MoreUSD: &more}, true)
	st = f.advance(run.ID)
	if st.State != models.RunWaiting || st.WaitingFor != models.RunWaitsForClaude {
		t.Fatalf("Claude chooses the moments and keeps the shorts: %s %q", st.State, st.Waiting)
	}
	head := f.revision(st.Revision)
	follows := false
	if head.Spec.Mix != nil {
		for _, v := range head.Spec.Mix.Voices {
			follows = follows || (v.AssetID == r.AssetID && v.Enhance != nil && v.Enhance.Mode == editspec.EnhanceCloud)
		}
	}
	if !follows || head.Spec.Captions == nil || !head.Spec.Captions.Enabled {
		t.Fatalf("the clearer voice follows the call, captions on: %+v / %+v", head.Spec.Mix, head.Spec.Captions)
	}
	must[RecipePicksOut](t, f.a, "recipe.picks", RecipePicksIn{Run: run.ID, Keep: true}, false)
	st = f.advance(run.ID)
	f.finishReview(run.ID)
	st = f.advance(run.ID)
	for _, o := range st.Outputs {
		if o.State == models.OutputMaking && o.Render != "" {
			f.finishRender(o.Render)
		}
	}
	done := f.advance(run.ID)
	if done.State != models.RunDone {
		t.Fatalf("done: %s %q / %+v", done.State, done.Waiting, done.Steps)
	}
	shorts := 0
	for _, o := range done.Outputs {
		if o.State == models.OutputReady {
			shorts++
			if o.ProjectID != done.ProjectID || !strings.HasPrefix(o.Branch, "shorts-") {
				t.Fatalf("a short is a new cut of the run's project: %+v", o)
			}
		}
	}
	if shorts == 0 || done.Spent.USD > done.Agreed.UpTo+0.001 {
		t.Fatalf("shorts made, within what was agreed: %d / %+v of %v", shorts, done.Spent, done.Agreed.UpTo)
	}
	var p models.Project
	_ = f.a.DB.GetByID(f.ctx, store.CProjects, f.a.WorkspaceID(), done.ProjectID, &p)
	var open []string
	for _, so := range ops.OpenSignoffs(f.ctx, f.a, &p, done.Branch) {
		open = append(open, so.Text)
	}
	if !strings.Contains(strings.Join(open, " | "), "delivery notes") {
		t.Fatalf("the delivery notes wait to be written before it goes out: %v", open)
	}
	// The sample's cost is on the project's receipt, said as the sample's.
	rc, err := receipt.Receipt(f.ctx, f.a, &p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rc.Samples) != 1 || rc.Samples[0].State != models.SampleApproved || rc.Samples[0].USD <= 0 || rc.Samples[0].By != "Ada" ||
		!strings.Contains(strings.Join(rc.Lines, " "), "A sample came first in run 1") {
		t.Fatalf("the receipt: %+v / %v", rc.Samples, rc.Lines)
	}
}
