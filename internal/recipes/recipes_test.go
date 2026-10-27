package recipes

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// fx is a workspace with a cut that worked: a brand intro from a linked
// project, two moments of an interview, a title card, an end card over a
// picture, two narration lines in an AI voice (a fake ElevenLabs).
type fx struct {
	t                            *testing.T
	a                            *app.App
	ctx                          context.Context
	teaser, brand                string
	interview, interview2, intro string
	hero, villain                *models.Resource
	eleven                       *fakeeleven.Server
	rev                          string // the approved version
}

var _ = intake.RouteLink // resources.add is registered there

func ffmpeg(t *testing.T, args ...string) {
	t.Helper()
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	if out, err := exec.Command(ts.Path(tools.FFmpeg), append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
}

func setup(t *testing.T) *fx {
	t.Helper()
	keyring.MockInit()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	ws := ids.New(ids.Workspace)
	cfg.Workspace = config.Ref{ID: ws, Name: "T"}
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
	f := &fx{t: t, a: a, ctx: ctx}
	now := time.Now().UTC()
	_ = a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now})
	f.teaser = ids.New(ids.Project)
	_ = a.DB.Insert(ctx, store.CProjects, models.Project{ID: f.teaser, WorkspaceID: ws, Name: "App teaser", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now,
		Rules: []models.ProjectRule{{ID: "rul_1", Text: "Under 35 seconds", Measure: "max-length:35", AddedAt: now}}})
	if _, err := store.MoveContentSettings(ctx, a.DB, ws); err != nil {
		t.Fatal(err)
	}
	f.brand = ids.New(ids.Project)
	_ = a.DB.Insert(ctx, store.CProjects, models.Project{ID: f.brand, WorkspaceID: ws, Name: "Brand", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now})
	dir := t.TempDir()
	video := func(name, src string, secs string) string {
		p := filepath.Join(dir, name)
		ffmpeg(t, "-f", "lavfi", "-i", src, "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", secs,
			"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", p)
		return p
	}
	add := func(path, project string) string {
		r := ingest.File(ctx, a, path, ingest.Options{ProjectID: project, Via: "cli"})
		if r.AssetID == "" {
			t.Fatalf("ingest %s: %+v", path, r)
		}
		return r.AssetID
	}
	f.interview = add(video("interview.mp4", "testsrc2=size=640x360:rate=30", "12"), f.teaser)
	f.interview2 = add(video("interview-2.mp4", "smptebars=size=640x360:rate=30", "12"), f.teaser)
	f.intro = add(video("intro.mp4", "color=c=blue:size=640x360:rate=30", "5"), f.brand)
	if _, err := ops.CallTyped[ops.ProjectLinkOut](ctx, a, "project.link", ops.ProjectLinkIn{ProjectID: f.teaser, From: f.brand}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	picture := func(name, color string) *models.Resource {
		p := filepath.Join(dir, name+".png")
		ffmpeg(t, "-f", "lavfi", "-i", "color=c="+color+":size=320x240", "-frames:v", "1", p)
		if _, err := ops.CallTyped[intake.AddPathsOut](ctx, a, "resources.add", intake.ResourcesAddIn{Paths: []string{p}, ProjectID: f.teaser, Name: name}, ops.Caller{}); err != nil {
			t.Fatal(err)
		}
		e, err := resources.Find(ctx, a, f.teaser, nil, name)
		if err != nil {
			t.Fatal(err)
		}
		return &e.Resource
	}
	f.hero, f.villain = picture("hero", "red"), picture("villain", "green")
	key := "sk" + "_" + strings.Repeat("cd34", 10) + "beef"
	f.eleven = fakeeleven.New(key)
	t.Cleanup(f.eleven.Close)
	t.Setenv(providers.EnvElevenLabsBaseURL, f.eleven.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	if _, err := ops.CallTyped[providers.Status](ctx, a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[providers.SettingsOut](ctx, a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, Voice: "Ada"}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	// The cut that worked.
	spec := &editspec.Spec{SchemaVersion: 1, Title: "App teaser", Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
		Clips: []editspec.Clip{{ID: "brand", AssetID: f.intro, In: 0, Out: 3, Label: "Brand intro"},
			{ID: "hook", AssetID: f.interview, In: 1, Out: 4, Label: "the hook"},
			{ID: "answer", AssetID: f.interview, In: 6, Out: 9, Label: "the answer"}},
		Overlays: []editspec.Overlay{
			{ID: "title", Template: editspec.TplTitleCard, At: 3, Duration: 2, Params: json.RawMessage(`{"title":"Stories for tonight"}`)},
			{ID: "end", Template: editspec.TplEndCard, At: 6, Duration: 3, Params: json.RawMessage(`{"headline":"Read free","cta":"Start now","image":"hero"}`)}},
		Resources: map[string]editspec.ResourcePin{"hero": {ID: f.hero.ID, SHA256: f.hero.Latest().SHA256}},
		Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{
			{ID: "line-1", Text: "Meet the app.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "hook", Offset: 0.2}},
			{ID: "line-2", Text: "Read free tonight.", Anchor: editspec.Anchor{Kind: editspec.AnchorAfter}}}}}
	rev := f.apply(editspec.Plan{ProjectID: f.teaser, Instruction: "the teaser", Spec: spec})
	tts, err := ops.CallTyped[voiceover.GenerateOut](ctx, a, "voiceover.tts", voiceover.TTSIn{RevisionID: rev, Apply: true}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if tts.Applied == nil || tts.Applied.RevisionID == "" {
		t.Fatalf("tts not applied: %+v", tts)
	}
	f.rev = tts.Applied.RevisionID
	if _, err := ops.CallTyped[ops.RevisionSummary](ctx, a, "revision.approve", ops.RevisionApproveIn{ID: f.rev}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fx) apply(plan editspec.Plan) string {
	f.t.Helper()
	raw, _ := json.Marshal(plan)
	chk, err := ops.CallTyped[ops.EditCheckOut](f.ctx, f.a, "edit.check", ops.EditCheckIn{Plan: raw}, ops.Caller{})
	if err != nil {
		f.t.Fatal(err)
	}
	if !chk.Valid {
		f.t.Fatalf("plan invalid: %+v", chk.Errors)
	}
	out, err := ops.CallTyped[ops.EditApplyOut](f.ctx, f.a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: chk.ConfirmToken}, ops.Caller{})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.Revision.ID
}

func call[O any](t *testing.T, a *app.App, name string, in any, human bool) (*O, error) {
	t.Helper()
	return ops.CallTyped[O](context.Background(), a, name, in, ops.Caller{Human: human})
}

func must[O any](t *testing.T, a *app.App, name string, in any, human bool) *O {
	t.Helper()
	out, err := call[O](t, a, name, in, human)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func codeOf(err error) string {
	if err == nil {
		return ""
	}
	return apperr.From(err).Code
}

// save saves the teaser as a recipe (a person, so version 1 is current).
func (f *fx) save(in RecipeSaveIn) *RecipeSaveOut {
	f.t.Helper()
	in.From = f.teaser
	return must[RecipeSaveOut](f.t, f.a, "recipe.save", in, true)
}

// finishReview plays a worker making a run's review copy and its quality check.
func (f *fx) finishReview(runID string) {
	f.t.Helper()
	run, err := getRun(f.ctx, f.a, runID)
	if err != nil {
		f.t.Fatal(err)
	}
	if run.Render == "" {
		f.t.Fatalf("no review copy asked for yet: %+v", run.Steps)
	}
	f.finishRender(run.Render)
}

// finishRender plays a worker making a review copy and its quality check.
func (f *fx) finishRender(renderID string) {
	f.t.Helper()
	var rd models.Render
	if err := f.a.DB.GetByID(f.ctx, store.CRenders, f.a.WorkspaceID(), renderID, &rd); err != nil {
		f.t.Fatal(err)
	}
	now := time.Now().UTC()
	rep := models.QAReport{ID: ids.New(models.QAReportPrefix), WorkspaceID: f.a.WorkspaceID(), ProjectID: rd.ProjectID, RevisionID: rd.RevisionID,
		RevisionNumber: rd.RevisionNumber, RenderID: rd.ID, Quality: rd.Quality, Verdict: models.QAPass, Findings: []models.QAFinding{}, Checks: []models.QACheck{},
		Frames: []models.QAFrame{}, Limitations: []string{}, CreatedAt: now}
	if err := f.a.DB.Insert(f.ctx, store.CQAReports, rep); err != nil {
		f.t.Fatal(err)
	}
	if err := f.a.DB.Update(f.ctx, store.CRenders, f.a.WorkspaceID(), rd.ID, bson.M{"$set": bson.M{"state": models.RenderAvailable, "completedAt": now,
		"output": models.RenderOutput{StorageKey: "renders/x.mp4", SHA256: "ab", Size: 1000, Duration: 9, Width: 960, Height: 540, FPS: 30},
		"qa":     models.RenderQA{ReportID: rep.ID, Verdict: models.QAPass, At: now}}}); err != nil {
		f.t.Fatal(err)
	}
	j := models.Job{ID: ids.New(ids.Job), WorkspaceID: f.a.WorkspaceID(), Type: jobs.TypeQA, State: models.JobSucceeded, ProjectID: rd.ProjectID,
		Payload: map[string]any{"renderId": rd.ID, "revisionId": rd.RevisionID, "auto": true}, IdempotencyKey: "qa:" + rd.ID + ":test",
		Result: map[string]any{"reportId": rep.ID, "verdict": models.QAPass}, Attempts: []models.Attempt{}, CreatedAt: now, UpdatedAt: now, FinishedAt: &now}
	if err := f.a.DB.Insert(f.ctx, store.CJobs, j); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fx) advance(id string) *models.RecipeRun {
	f.t.Helper()
	run, err := Advance(f.ctx, f.a, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return run
}

func (f *fx) revision(id string) *models.Revision {
	f.t.Helper()
	var r models.Revision
	if err := f.a.DB.GetByID(f.ctx, store.CRevisions, f.a.WorkspaceID(), id, &r); err != nil {
		f.t.Fatal(err)
	}
	return &r
}

func stepOf(run *models.RecipeRun, id string) *models.RecipeStep {
	for i := range run.Steps {
		if run.Steps[i].ID == id {
			return &run.Steps[i]
		}
	}
	return nil
}

func inputOf(d *models.RecipeDoc, kind string) *models.RecipeInput {
	for i := range d.Inputs {
		if d.Inputs[i].Kind == kind {
			return &d.Inputs[i]
		}
	}
	return nil
}

// Saving a cut proposes what varies (its own footage, words in graphics, a
// picture that isn't the brand's, the AI voice) and what stays; a person's
// save is current at once, the same recipe twice is refused.
func TestSaveFromProjectProposesWhatVaries(t *testing.T) {
	f := setup(t)
	dry := f.save(RecipeSaveIn{DryRun: true})
	if !dry.DryRun || dry.Recipe != nil {
		t.Fatalf("a dry run saves nothing: %+v", dry)
	}
	varies := map[string]bool{}
	keys := map[string]bool{}
	for _, it := range dry.Proposal {
		keys[it.Key] = true
		if it.Varies {
			varies[it.Key] = true
		}
	}
	for _, k := range []string{"footage", "title", "headline", "hero", "voice", "moments"} {
		if !varies[k] {
			t.Errorf("%s should vary: %+v", k, dry.Proposal)
		}
	}
	for _, k := range []string{"cta", "line-1", "brand-intro"} {
		if !keys[k] || varies[k] {
			t.Errorf("%s should be offered and stay: %+v", k, dry.Proposal)
		}
	}
	if dry.Check == nil || !dry.Check.OK {
		t.Fatalf("check: %+v", dry.Check)
	}
	if !strings.Contains(dry.Readable, "## Inputs") || !strings.Contains(dry.Readable, "## Sections") {
		t.Fatalf("readable: %s", dry.Readable)
	}
	out := f.save(RecipeSaveIn{Hints: []string{"Name the app in the first two seconds."}})
	if out.Recipe == nil || out.Recipe.Name != "app-teaser" || out.Recipe.Current != 1 || out.Version.State != models.RecipeCurrent {
		t.Fatalf("saved %+v / %+v", out.Recipe, out.Version)
	}
	r, err := loadRecipe(f.ctx, f.a, "app-teaser")
	if err != nil {
		t.Fatal(err)
	}
	v, err := loadVersion(f.ctx, f.a, r, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, h := Canonical(&v.Doc); h != v.Hash || v.ID != f.a.WorkspaceID()+"/"+h {
		t.Fatalf("a version is its document's hash: %s %s", v.Hash, h)
	}
	d := &v.Doc
	if d.Lands.Kind != models.RecipeLandsProject || d.Lands.ProjectID != f.teaser || d.From == nil || d.From.RevisionID != f.rev || !d.From.Approved {
		t.Fatalf("lands %+v from %+v", d.Lands, d.From)
	}
	if len(d.Sections) != 2 || d.Sections[0].Kind != models.RecipeFixed || d.Sections[1].Kind != models.RecipeChosen || d.Sections[1].From != "footage" {
		t.Fatalf("sections %+v", d.Sections)
	}
	for _, o := range d.Cut.Overlays {
		if o.Span == nil {
			t.Fatalf("graphics follow their clips: %+v", o)
		}
	}
	if len(d.Rules) != 1 || d.Rules[0].Measure != "max-length:35" || len(d.Hints) != 1 {
		t.Fatalf("rules %+v hints %+v", d.Rules, d.Hints)
	}
	if vi := inputOf(d, models.RecipeVoice); vi == nil || vi.Default.Text != "Ada" {
		t.Fatalf("voice %+v", vi)
	}
	if r.Example != nil {
		t.Fatalf("no render of the version yet, so no example: %+v", r.Example)
	}
	// The same recipe again is refused; a different name doesn't make it another.
	if _, err := call[RecipeSaveOut](t, f.a, "recipe.save", RecipeSaveIn{From: f.teaser, Name: "teaser-two", Hints: []string{"Name the app in the first two seconds."}}, true); codeOf(err) != apperr.Conflict {
		t.Fatalf("same recipe twice: %v", err)
	}
	// Claude's save is a draft a person makes current.
	draft := must[RecipeSaveOut](t, f.a, "recipe.save", RecipeSaveIn{From: f.teaser, Name: "teaser-draft", Keep: []string{"footage"}}, false)
	if draft.Recipe.Current != 0 || draft.Version.State != models.RecipeDraft {
		t.Fatalf("Claude's save: %+v", draft.Version)
	}
	if _, err := call[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "teaser-draft"}, false); err != nil {
		t.Fatal(err)
	} else if p := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "teaser-draft"}, false); p.Ready {
		t.Fatalf("a recipe with no current version can't run: %+v", p)
	}
	if _, err := call[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "teaser-draft"}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("only a person makes a version current: %v", err)
	}
	pub := must[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "teaser-draft"}, true)
	if pub.Recipe.Current != 1 {
		t.Fatalf("published %+v", pub)
	}
	// Unknown keys say what can move.
	if _, err := call[RecipeSaveOut](t, f.a, "recipe.save", RecipeSaveIn{From: f.teaser, DryRun: true, Vary: []string{"nothing"}}, true); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("unknown key: %v", err)
	}
}

// A run with the recipe's own inputs: nothing to pay, a new cut in the
// project with the fixed section locked and the moments chosen then, a
// review copy and its quality check, and the version waiting for review.
func TestRunMakesANewCut(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	if !plan.Ready || plan.Estimate.About != "nothing" || len(plan.Estimate.Parts) != 0 || plan.Claude || plan.NewProject || plan.Branch != "app-teaser-1" {
		t.Fatalf("plan %+v", plan)
	}
	var steps []string
	for _, s := range plan.Steps {
		steps = append(steps, s.ID)
	}
	if strings.Join(steps, ",") != "inputs,place,assemble,review,finish" {
		t.Fatalf("steps %v", steps)
	}
	// Only a person starts a run, and only the plan they saw.
	in := RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: plan.Fingerprint}
	if _, err := call[RecipeRunOut](t, f.a, "recipe.run", in, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("not a person: %v", err)
	}
	bad := in
	bad.Fingerprint = strings.Repeat("0", 32)
	if _, err := call[RecipeRunOut](t, f.a, "recipe.run", bad, true); codeOf(err) != apperr.Conflict {
		t.Fatalf("another plan: %v", err)
	}
	out := must[RecipeRunOut](t, f.a, "recipe.run", in, true)
	run := out.Run
	if run.Number != 1 || run.ProjectID != f.teaser || run.Branch != "app-teaser-1" || run.Render == "" || run.State != models.RunRunning {
		t.Fatalf("run %+v", run.RecipeRun)
	}
	rev := f.revision(run.Revision)
	if rev.Origin != "recipe:"+run.ID || rev.Branch != "app-teaser-1" {
		t.Fatalf("version %s %s", rev.Origin, rev.Branch)
	}
	for _, c := range rev.Spec.Clips {
		if (c.ID == "brand") != c.Locked {
			t.Fatalf("only the fixed section is locked: %+v", rev.Spec.Clips)
		}
	}
	if len(rev.Spec.Clips) != 3 || rev.Spec.Clips[1].AssetID != f.interview || rev.Spec.Clips[1].In != 1 {
		t.Fatalf("the moments chosen then: %+v", rev.Spec.Clips)
	}
	if b := rev.Spec.Voiceover.Beats; b[0].Take == nil || b[1].Take == nil {
		t.Fatalf("the voice is reused: %+v", b)
	}
	if f.eleven.Calls("tts") != 2 {
		t.Fatalf("nothing new to read: %d", f.eleven.Calls("tts"))
	}
	f.finishReview(run.ID)
	done := f.advance(run.ID)
	if done.State != models.RunDone || done.QA != "passed" || done.FinishedAt == nil {
		t.Fatalf("done %+v", done)
	}
	head := f.revision(done.Revision)
	if head.ReviewAsked == nil {
		t.Fatal("the version waits for review")
	}
	var n models.Notification
	if err := f.a.DB.C(store.CNotifications).FindOne(f.ctx, bson.M{"groupKey": "recipe-run:" + run.ID}).Decode(&n); err != nil || n.Tone != "success" {
		t.Fatalf("bell %+v %v", n, err)
	}
	r, _ := loadRecipe(f.ctx, f.a, "app-teaser")
	if r.LastRun == nil || r.LastRun.State != models.RunDone || r.Example == nil || r.Example.RunID != run.ID {
		t.Fatalf("recipe after %+v", r)
	}
	// The project keeps the recipe's rule once.
	var p models.Project
	_ = f.a.DB.GetByID(f.ctx, store.CProjects, f.a.WorkspaceID(), f.teaser, &p)
	if len(p.Rules) != 1 {
		t.Fatalf("rules %+v", p.Rules)
	}
	runs := must[RecipeRunsOut](t, f.a, "recipe.runs", RecipeRunsIn{Recipe: "app-teaser"}, false)
	if len(runs.Runs) != 1 || runs.Runs[0].Ref != "app-teaser#1" {
		t.Fatalf("runs %+v", runs)
	}
	one := must[RecipeRunsOut](t, f.a, "recipe.runs", RecipeRunsIn{Run: "app-teaser#1"}, false)
	if one.Run == nil || one.Run.StateLabel != "Ready for your review" {
		t.Fatalf("one %+v", one.Run)
	}
}

// New footage for a chosen section: stand-ins in its place, the run waits
// for Claude (said plainly, a to-do), and goes on once they are replaced.
func TestNewFootageWaitsForClaude(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	in := RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: "footage", Items: []string{f.interview2}}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if !plan.Ready || !plan.Claude {
		t.Fatalf("plan %+v", plan)
	}
	out := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint}, true)
	run := out.Run
	if run.State != models.RunWaiting || run.WaitingFor != models.RunWaitsForClaude || !strings.Contains(run.Waiting, "Waiting for Claude to choose the moments") {
		t.Fatalf("waits %+v", run.RecipeRun)
	}
	if len(run.StandIns) != 1 || run.StandIns[0].ClipID != "moments-1" || run.StandIns[0].AssetID != f.interview2 || out.Run.Claude == "" {
		t.Fatalf("stand-ins %+v (%q)", run.StandIns, out.Run.Claude)
	}
	var todo models.Todo
	if err := f.a.DB.C(store.CTodos).FindOne(f.ctx, bson.M{"groupKey": "recipe-run:" + run.ID + ":claude", "state": models.TodoOpen}).Decode(&todo); err != nil {
		t.Fatalf("to-do: %v", err)
	}
	if todo.Kind != models.TodoRecipeRun || todo.Context.Recipe == nil || todo.Context.Recipe.WaitingFor != models.RunWaitsForClaude {
		t.Fatalf("to-do %+v", todo)
	}
	// Claude (here, an ordinary change) chooses the moment; the fixed section stays locked.
	set, _ := json.Marshal(map[string]any{"in": 2.5, "out": 6.5})
	f.apply(editspec.Plan{BaseRevisionID: run.Revision, Branch: run.Branch, Instruction: "the moments", Ops: []editspec.Op{{Op: "updateClip", ID: "moments-1", Set: set}}})
	moved := f.advance(run.ID)
	if moved.State != models.RunRunning || stepOf(moved, stepClaude).State != models.StepDone || moved.Render == "" {
		t.Fatalf("goes on: %+v", moved.Steps)
	}
	if err := f.a.DB.C(store.CTodos).FindOne(f.ctx, bson.M{"groupKey": "recipe-run:" + run.ID + ":claude", "state": models.TodoOpen}).Decode(&todo); err == nil {
		t.Fatal("the to-do closes once the moments are chosen")
	}
	f.finishReview(run.ID)
	if done := f.advance(run.ID); done.State != models.RunDone {
		t.Fatalf("done %+v", done.Steps)
	}
}

// Again with a new picture and another voice: the plan says what changes
// and what it costs (D208's estimate: dollars at ElevenLabs' prices, opening
// to each line; agreed once); the new version of the same cut has the new
// picture and the lines read again; compare names both.
func TestAgainWithANewPictureAndAnotherVoice(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	p1 := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	r1 := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: p1.Fingerprint}, true).Run
	f.finishReview(r1.ID)
	f.advance(r1.ID)
	in := RecipePlanIn{Repeats: "app-teaser#1", Inputs: []RecipeInputValue{{Name: "hero", Items: []string{"villain"}}, {Name: "voice", Text: "Bram"}}, Note: "a warmer voice"}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if !plan.Ready || plan.Repeats == nil || plan.Branch != "app-teaser-1" {
		t.Fatalf("plan %+v", plan)
	}
	chars := len("Meet the app.") + len("Read free tonight.")
	if plan.Estimate.USD <= 0 || plan.Estimate.About != "less than a cent" || len(plan.Estimate.Parts) != 2 || !plan.AskForEstimate {
		t.Fatalf("estimate %+v", plan.Estimate)
	}
	credits := 0.0
	for _, pt := range plan.Estimate.Parts {
		credits += pt.Amount
		if pt.Service != "ElevenLabs" || pt.Model == "" || pt.Step != "Make the AI voice" || pt.Unit != "credits" || !strings.Contains(pt.What, "Bram") || pt.Rate == "" {
			t.Fatalf("part %+v", pt)
		}
	}
	if credits != float64(chars) || !strings.Contains(plan.Estimate.Parts[0].Source, "Meet the app.") || stepOf(&models.RecipeRun{Steps: plan.Steps}, stepVoice).Estimate != plan.Estimate.USD {
		t.Fatalf("parts %+v steps %+v", plan.Estimate.Parts, plan.Steps)
	}
	if len(plan.Changes) < 2 {
		t.Fatalf("changes %v", plan.Changes)
	}
	again := RecipeAgainIn{Run: "app-teaser#1", Inputs: in.Inputs, Note: in.Note, Fingerprint: plan.Fingerprint}
	if _, err := call[RecipeRunOut](t, f.a, "recipe.again", again, true); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("the estimate must be agreed: %v", err)
	}
	few := plan.Estimate.USD / 2
	again.MaxUSD = &few
	if _, err := call[RecipeRunOut](t, f.a, "recipe.again", again, true); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("too little: %v", err)
	}
	enough := plan.Estimate.USD
	again.MaxUSD = &enough
	r2 := must[RecipeRunOut](t, f.a, "recipe.again", again, true).Run
	if r2.Repeats != r1.ID || r2.Branch != r1.Branch || r2.Agreed == nil || r2.Agreed.UpTo != enough || r2.Estimate.USD != plan.Estimate.USD {
		t.Fatalf("run %+v", r2.RecipeRun)
	}
	if f.eleven.Calls("tts") != 4 || r2.Spent.Credits != float64(chars) || r2.Spent.USD <= 0 || r2.Spent.USD > enough+0.0005 {
		t.Fatalf("two lines read again: %d calls, spent %+v", f.eleven.Calls("tts"), r2.Spent)
	}
	head := f.revision(r2.Revision)
	if head.Spec.Resources["hero"].ID != f.villain.ID {
		t.Fatalf("the new picture: %+v", head.Spec.Resources)
	}
	for _, b := range head.Spec.Voiceover.Beats {
		if b.Voice == nil || b.Voice.Name != "Bram" || b.Take == nil {
			t.Fatalf("lines in the new voice: %+v", b)
		}
	}
	if head.Branch != r1.Branch || head.Number <= f.revision(r1.Revision).Number {
		t.Fatal("a new version of the same cut")
	}
	f.finishReview(r2.ID)
	if done := f.advance(r2.ID); done.State != models.RunDone {
		t.Fatalf("done %+v", done.Steps)
	}
	cmp := must[RecipeCompareOut](t, f.a, "recipe.compare", RecipeCompareIn{Left: "app-teaser#1", Right: "app-teaser#2"}, false)
	changed := map[string]bool{}
	for _, row := range cmp.Inputs {
		if row.Changed {
			changed[row.Name] = true
		}
	}
	if !changed["hero"] || !changed["voice"] || changed["footage"] || cmp.Cut == nil || len(cmp.Notes) != 1 {
		t.Fatalf("compare %+v", cmp)
	}
}

// Going past the estimate asks again; a person lets it go on with more. A
// person whose estimates are off (Settings → Preferences, D208) starts a run
// without the estimate step: budgets still hold.
func TestPastTheEstimateAsksAgain(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	in := RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: "voice", Text: "Cleo"}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	usd := plan.Estimate.USD
	advanceOnStart = false
	defer func() { advanceOnStart = true }()
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint, MaxUSD: &usd}, true).Run
	// Something costs more than planned: the run has less agreed than the voice needs.
	if _, err := f.a.DB.C(store.CRecipeRuns).UpdateOne(f.ctx, bson.M{"_id": run.ID}, bson.M{"$set": bson.M{"agreed.upTo": 0}}); err != nil {
		t.Fatal(err)
	}
	waiting := f.advance(run.ID)
	if waiting.State != models.RunWaiting || waiting.WaitingFor != models.RunWaitsForPerson || !strings.Contains(waiting.Waiting, "more than you agreed to") {
		t.Fatalf("asks again: %+v", waiting)
	}
	if f.eleven.Calls("tts") != 2 {
		t.Fatal("nothing is spent past the agreement")
	}
	// The daemon's loop leaves it alone; only a person lets it go on.
	if again := f.advance(run.ID); again.State != models.RunWaiting {
		t.Fatalf("still waiting: %+v", again.State)
	}
	more := usd
	if _, err := call[RecipeRunOut](t, f.a, "recipe.continue", RecipeContinueIn{Run: run.ID, MoreUSD: &more}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("people only: %v", err)
	}
	cont := must[RecipeRunOut](t, f.a, "recipe.continue", RecipeContinueIn{Run: run.ID, MoreUSD: &more}, true).Run
	if cont.State != models.RunRunning || stepOf(&cont.RecipeRun, stepVoice).State != models.StepDone || cont.Agreed.UpTo != more || len(cont.Agreed.More) != 1 {
		t.Fatalf("went on: %+v / %+v", cont.Steps, cont.Agreed)
	}
	// The person turns "Estimate costs of outside AI services before edits"
	// off: the plan says so, and a run starts without an agreed amount.
	who := prefs.Asker(f.a)
	_ = f.a.DB.Insert(f.ctx, store.CUsers, models.User{ID: who, WorkspaceID: f.a.WorkspaceID(), Name: "Ada", Email: "ada@example.com", CreatedAt: time.Now().UTC()})
	if err := prefs.SetEstimates(f.ctx, f.a, who, false); err != nil {
		t.Fatal(err)
	}
	in2 := RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: "voice", Text: "Bram"}}}
	p2 := must[RecipePlanOut](t, f.a, "recipe.plan", in2, false)
	if p2.AskForEstimate || p2.Estimate.USD <= 0 || !strings.Contains(p2.Summary, "Estimates are off for you") {
		t.Fatalf("the preference decides: %+v", p2)
	}
	r2 := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in2, Fingerprint: p2.Fingerprint}, true).Run
	if r2.Agreed == nil || !r2.Agreed.Skipped || r2.AgreedWords != "no estimate asked for (estimates are off): budgets and limits hold" {
		t.Fatalf("agreement %+v", r2.Agreed)
	}
	// Starting is still a person's.
	if _, err := call[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in2, Fingerprint: p2.Fingerprint}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("people only: %v", err)
	}
}

// Drafts from Claude, people make them current (removed rules named), archive.
func TestUpdatePublishArchive(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	up := must[RecipeUpdateOut](t, f.a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "app-teaser"}, AddHints: []string{"End on the logo."},
		Patch: json.RawMessage(`{"rules":null}`), Note: "no length rule"}, false)
	if up.Same || up.Version.Number != 2 || up.Version.State != models.RecipeDraft || up.Version.Parent != 1 {
		t.Fatalf("draft %+v", up)
	}
	same := must[RecipeUpdateOut](t, f.a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "app-teaser"}, AddHints: []string{"End on the logo."}}, false)
	if !same.Same {
		t.Fatalf("nothing changed: %+v", same)
	}
	if _, err := call[RecipeUpdateOut](t, f.a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "app-teaser"}, Patch: json.RawMessage(`{"nope":1}`)}, false); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("unknown field: %v", err)
	}
	pub := must[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "app-teaser"}, true)
	if pub.Version.Number != 2 || len(pub.RemovedRules) != 1 || !strings.Contains(pub.Message, "no longer keeps") {
		t.Fatalf("publish %+v", pub)
	}
	show := must[RecipeShowOut](t, f.a, "recipe.show", RecipeRefIn{Recipe: "app-teaser"}, false)
	if show.Version.Number != 2 || len(show.Versions) != 2 || show.Versions[1].State != models.RecipeEarlier || len(show.Hints) != 0 && show.Hints[0] == "" {
		t.Fatalf("show %+v", show.Versions)
	}
	if _, err := call[RecipeArchiveOut](t, f.a, "recipe.archive", RecipeArchiveIn{Recipe: "app-teaser"}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("people only: %v", err)
	}
	must[RecipeArchiveOut](t, f.a, "recipe.archive", RecipeArchiveIn{Recipe: "app-teaser"}, true)
	if l := must[RecipeListOut](t, f.a, "recipe.list", RecipeListIn{}, false); len(l.Recipes) != 0 {
		t.Fatalf("archived recipes leave the list: %+v", l)
	}
	if p := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false); p.Ready {
		t.Fatal("an archived recipe doesn't run")
	}
	must[RecipeArchiveOut](t, f.a, "recipe.archive", RecipeArchiveIn{Recipe: "app-teaser", Restore: true}, true)
	if l := must[RecipeListOut](t, f.a, "recipe.list", RecipeListIn{}, false); len(l.Recipes) != 1 || len(l.Recipes[0].Inputs) == 0 {
		t.Fatalf("restored %+v", l)
	}
}

// A check finds what a recipe can't do: a field too short for its input, a
// graphic that isn't there, an archived project it adds cuts to.
func TestCheckFindsProblems(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	show := must[RecipeShowOut](t, f.a, "recipe.show", RecipeRefIn{Recipe: "app-teaser"}, false)
	doc := show.Doc
	for i := range doc.Inputs {
		if doc.Inputs[i].Name == "headline" {
			doc.Inputs[i].MaxLength = 500
		}
	}
	doc.Bindings = append(doc.Bindings, models.RecipeBinding{Input: "title", To: "param:gone.title"})
	raw, _ := json.Marshal(doc)
	chk := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Doc: raw}, false)
	if chk.Check.OK {
		t.Fatalf("problems expected: %+v", chk.Check)
	}
	if !strings.Contains(chk.Check.Problems[0].Message, "gone") {
		t.Fatalf("the missing graphic: %+v", chk.Check.Problems)
	}
	ok := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Recipe: "app-teaser"}, false)
	if !ok.Check.OK || ok.Check.Duration < 8 || len(ok.Check.Stills) != 2 || !strings.HasPrefix(ok.Check.Stills[0].URL, "/media/assets/") {
		t.Fatalf("check %+v", ok.Check)
	}
	if _, err := f.a.DB.C(store.CProjects).UpdateOne(f.ctx, bson.M{"_id": f.teaser}, bson.M{"$set": bson.M{"archived": true}}); err != nil {
		t.Fatal(err)
	}
	gone := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Recipe: "app-teaser"}, false)
	if gone.Check.OK || !strings.Contains(gone.Check.Problems[0].Message, "archived") {
		t.Fatalf("archived project: %+v", gone.Check)
	}
}

// Clearing a project out for good keeps its runs' records (marked), stops
// one still going and drops an example that was in it.
func TestProjectGoneKeepsRuns(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: plan.Fingerprint}, true).Run
	_, _ = f.a.DB.C(store.CRecipes).UpdateOne(f.ctx, bson.M{"name": "app-teaser"}, bson.M{"$set": bson.M{"example": models.RecipeExample{RenderID: run.Render, ProjectID: f.teaser}}})
	if _, err := projectGone(f.ctx, f.a, f.teaser); err != nil {
		t.Fatal(err)
	}
	after, _ := getRun(f.ctx, f.a, run.ID)
	if after.State != models.RunStopped || !after.ProjectGone || after.Problem == "" {
		t.Fatalf("run after %+v", after)
	}
	r, _ := loadRecipe(f.ctx, f.a, "app-teaser")
	if r.Example != nil {
		t.Fatal("the example went with the project")
	}
	if _, err := call[RecipeRunOut](t, f.a, "recipe.stop", RecipeStopIn{Run: run.ID}, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("a stopped run can't stop again: %v", err)
	}
}
