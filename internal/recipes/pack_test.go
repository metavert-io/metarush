package recipes

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/conversation"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/worker"
)

// callTruth is what tests/fixtures/truth/conversation.json says about the
// generated call: who is in it, where each recording starts, who says what when.
type callTruth struct {
	Delay   float64 `json:"delay_s"`
	Members []struct {
		File  string  `json:"file"`
		Name  string  `json:"name"`
		Start float64 `json:"start_s"`
		Drift float64 `json:"drift_ppm"`
	} `json:"members"`
	Turns []struct {
		Speaker string  `json:"speaker"`
		Start   float64 `json:"start_s"`
		End     float64 `json:"end_s"`
		Text    string  `json:"text"`
	} `json:"turns"`
}

func repoRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// episode brings the generated call (Maya and Sam, each recorded on their
// own machine; scripts/make-conversation-fixtures.sh, macOS `say`) into a
// project: each recording measured, its words from the truth, lined up as a
// conversation and cut automatically — the episode, approved.
func (f *fx) episode() (project, conv string) {
	f.t.Helper()
	root := repoRoot()
	gen := filepath.Join(root, "tests", "fixtures", "gen", "conversation")
	if _, err := os.Stat(filepath.Join(gen, "sam.mov")); err != nil {
		if _, err := exec.LookPath("say"); err != nil {
			f.t.Skip("the conversation fixtures need macOS `say`")
		}
		if out, err := exec.Command(filepath.Join(root, "scripts", "make-conversation-fixtures.sh")).CombinedOutput(); err != nil {
			f.t.Fatalf("making fixtures: %v\n%s", err, out)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "tests", "fixtures", "truth", "conversation.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	var tr callTruth
	if err := json.Unmarshal(b, &tr); err != nil {
		f.t.Fatal(err)
	}
	pr := must[ops.ProjectOut](f.t, f.a, "project.create", ops.ProjectCreateIn{Name: "Garden show"}, true)
	project = pr.Project.ID
	add := func(file string) string {
		r := ingest.File(f.ctx, f.a, filepath.Join(gen, file), ingest.Options{ProjectID: project, Via: "cli"})
		if r.AssetID == "" {
			f.t.Fatalf("ingest %s: %+v", file, r)
		}
		js, err := f.a.Queue.List(f.ctx, bson.M{"assetId": r.AssetID, "type": "evidence"}, 10)
		if err != nil {
			f.t.Fatal(err)
		}
		for _, j := range js {
			w := worker.New(f.a)
			w.OnlyJobID, w.IgnoreBattery, w.AcceptsHeavy = j.ID, true, true
			w.Log = func(string, ...any) {}
			if err := w.Run(f.ctx); err != nil {
				f.t.Fatal(err)
			}
		}
		return r.AssetID
	}
	maya, sam := add("maya.mov"), add("sam.mov")
	// Each person's words on their own recording's clock.
	samClock := func(t float64) float64 { return (t - tr.Members[1].Start) * (1 + tr.Members[1].Drift/1e6) }
	var jt, st []models.TranscriptSegment
	for _, x := range tr.Turns {
		switch x.Speaker {
		case "Maya":
			jt = append(jt, models.TranscriptSegment{From: x.Start, To: x.End, Text: x.Text, Speaker: "speaker_0"})
		case "Sam":
			st = append(st, models.TranscriptSegment{From: samClock(x.Start), To: samClock(x.End), Text: x.Text, Speaker: "speaker_0"})
		}
	}
	f.wordTranscript(maya, jt)
	f.wordTranscript(sam, st)
	cv := must[conversation.CallOut](f.t, f.a, "conversation.create", conversation.CallCreateIn{ProjectID: project, Name: "Garden call",
		Members: []conversation.CallMemberIn{{AssetID: maya, Name: "Maya"}, {AssetID: sam, Name: "Sam"}}}, true)
	ep := must[conversation.CallPlanOut](f.t, f.a, "conversation.cut", conversation.CallCutIn{CallRefIn: conversation.CallRefIn{Conversation: cv.Conversation.ID}, Style: "podcast", Apply: true}, true)
	if ep.Applied == nil || ep.Applied.Problem != "" {
		f.t.Fatalf("episode: %s", ep.Summary)
	}
	must[ops.RevisionSummary](f.t, f.a, "revision.approve", ops.RevisionApproveIn{ID: ep.Applied.RevisionID}, true)
	return project, cv.Conversation.ID
}

// wordTranscript stores turns as segments with their words spread over each turn.
func (f *fx) wordTranscript(assetID string, turns []models.TranscriptSegment) {
	f.t.Helper()
	words := 0
	for i, tu := range turns {
		toks := strings.Fields(tu.Text)
		total := 0
		for _, w := range toks {
			total += len(w) + 1
		}
		acc := 0
		for _, w := range toks {
			a := tu.From + (tu.To-tu.From)*float64(acc)/float64(total)
			acc += len(w) + 1
			z := tu.From + (tu.To-tu.From)*float64(acc)/float64(total) - 0.03
			tu.Words = append(tu.Words, models.Word{Text: w, From: math.Round(a*1000) / 1000, To: math.Round(z*1000) / 1000})
		}
		words += len(tu.Words)
		tu.ID, tu.WorkspaceID, tu.AssetID, tu.Index, tu.Provider, tu.Model, tu.CreatedAt = ids.New("seg"), f.a.WorkspaceID(), assetID, i, "test", "truth", time.Now().UTC()
		if err := f.a.DB.Insert(f.ctx, store.CSegments, tu); err != nil {
			f.t.Fatal(err)
		}
	}
	st := models.TranscriptStatus{State: "available", Provider: "test", Precision: "word", Language: "en", Segments: len(turns), Words: words, Speakers: 1, UpdatedAt: time.Now().UTC()}
	if err := f.a.DB.Update(f.ctx, store.CAssets, f.a.WorkspaceID(), assetID, bson.M{"$set": bson.M{"transcript": st}}); err != nil {
		f.t.Fatal(err)
	}
}

// A pack (D214): an episode saved as a recipe with its shorts, the shorts
// changed in its sheet (D213) — a draft a person makes current — the plan
// showing the shorts' first picks (D215) under one estimate, the run waiting
// for Claude to keep or change them, one changed and one kept, then each
// short made as its own project (origin recipe:<run>), with its review copy
// and quality check, waiting for review with the main video; the runs of a
// short's project list the run.
func TestPackOfShorts(t *testing.T) {
	f := setup(t)
	project, conv := f.episode()
	dry := must[RecipeSaveOut](t, f.a, "recipe.propose", RecipeSaveIn{From: project}, false)
	offered := false
	for _, it := range dry.Proposal {
		if it.Key == "shorts" && it.Kind == "pack" && !it.Varies && strings.Contains(it.What, "Garden call") {
			offered = true
		}
	}
	if !offered {
		t.Fatalf("shorts are offered for a cut that plays a conversation: %+v", dry.Proposal)
	}
	saved := must[RecipeSaveOut](t, f.a, "recipe.save", RecipeSaveIn{From: project, Name: "garden-show", Pack: []string{"shorts"}}, true)
	if saved.Version == nil || saved.Version.State != models.RecipeCurrent || !strings.Contains(saved.Readable, "") {
		t.Fatalf("saved %+v", saved.Version)
	}
	// Claude changes the shorts in the recipe's sheet: two of 5–20 s, named.
	co := must[RecipeCheckoutOut](t, f.a, "recipe.checkout", RecipeRefIn{Recipe: "garden-show"}, false)
	if !strings.Contains(co.Sheet, "## Pack") || !strings.Contains(co.Sheet, "- shorts: shorts · count 1–3 · length 15–60 s") || co.File != "garden-show.recipe.md" {
		t.Fatalf("sheet:\n%s", co.Sheet)
	}
	edited := strings.Replace(co.Sheet, "- shorts: shorts · count 1–3 · length 15–60 s", "- shorts: shorts · count 2 · length 5–20 s · named \"{title}: short {k}\"", 1)
	chk := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Sheet: edited}, false)
	if !chk.Valid || chk.Same || len(chk.Changes) != 1 || !strings.Contains(chk.Changes[0], "pack's “shorts” changed") || !chk.Check.OK {
		t.Fatalf("check %+v", chk)
	}
	w := must[RecipeWriteOut](t, f.a, "recipe.write", RecipeWriteIn{Sheet: edited, Note: "two shorter shorts"}, false)
	if w.New || w.Same || w.Version.Number != 2 || w.Version.State != models.RecipeDraft || !strings.Contains(w.Sheet, "count 2 · length 5–20 s") {
		t.Fatalf("draft %+v", w)
	}
	again := must[RecipeWriteOut](t, f.a, "recipe.write", RecipeWriteIn{Sheet: edited}, false)
	if !again.Same {
		t.Fatalf("the same sheet twice is the same version: %+v", again.Version)
	}
	must[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "garden-show"}, true)

	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "garden-show"}, false)
	if !plan.Ready || len(plan.Pack) != 2 || !plan.Claude || plan.Estimate.About != "nothing" || !strings.Contains(plan.Summary, "2 shorts") {
		t.Fatalf("plan %+v", plan)
	}
	var steps []string
	for _, s := range plan.Steps {
		steps = append(steps, s.ID)
	}
	if strings.Join(steps, ",") != "inputs,place,assemble,claude,review,pack,finish" {
		t.Fatalf("steps %v", steps)
	}
	for _, o := range plan.Pack {
		if o.Conversation != conv || o.Pick != models.PickFirst || len(o.Why) == 0 || o.To-o.From < 5 || o.To-o.From > 20.5 || o.ConversationName != "Garden call" {
			t.Fatalf("a short's first pick %+v", o)
		}
	}
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "garden-show"}, Fingerprint: plan.Fingerprint}, true).Run
	if run.State != models.RunWaiting || run.WaitingFor != models.RunWaitsForClaude || !strings.Contains(run.Waiting, "the shorts' first picks") ||
		len(run.Outputs) != 2 || !strings.Contains(run.Claude, "The shorts' first picks from “Garden call”") {
		t.Fatalf("waits for Claude: %q %+v\n%s", run.Waiting, run.Outputs, run.Claude)
	}
	// Claude changes the second short's stretch and keeps the first.
	from, to := 36.3, 44.3
	ch := must[RecipePicksOut](t, f.a, "recipe.picks", RecipePicksIn{Run: run.Ref, Keep: true, Set: []RecipePickChange{{Part: "shorts", N: 2, From: &from, To: &to, Why: "the roof garden"}}}, false)
	got := ch.Run
	if got.Outputs[0].Pick != models.PickKept || got.Outputs[1].Pick != models.PickChanged || got.Outputs[1].From != 36.3 || stepOf(&got.RecipeRun, stepClaude).State != models.StepDone {
		t.Fatalf("picks %+v / %+v", got.Outputs, got.Steps)
	}
	f.finishReview(run.ID)
	made := f.advance(run.ID)
	if st := stepOf(made, stepPack); st.State != models.StepRunning || len(made.Outputs) != 2 {
		t.Fatalf("making the shorts: %+v", made.Steps)
	}
	for _, o := range made.Outputs {
		// Named by the pack's pattern: the recipe's title (the episode's, “Garden call”) and the short's number.
		if o.ProjectID == "" || o.Revision == "" || o.Render == "" || o.State != models.OutputMaking || o.ProjectName != "Garden call: short "+fmt.Sprint(o.N) {
			t.Fatalf("a short's own project: %+v", o)
		}
		rev := f.revision(o.Revision)
		if rev.Origin != "recipe:"+run.ID || rev.ProjectID != o.ProjectID || rev.Spec.Output.Preset != "vertical-1080p" {
			t.Fatalf("its version: %s %s %s", rev.Origin, rev.ProjectID, rev.Spec.Output.Preset)
		}
		f.finishRender(o.Render)
	}
	done := f.advance(run.ID)
	if done.State != models.RunDone || stepOf(done, stepPack).State != models.StepDone {
		t.Fatalf("done %+v", done.Steps)
	}
	for _, o := range done.Outputs {
		if o.State != models.OutputReady || o.QA != "passed" || f.revision(o.Revision).ReviewAsked == nil {
			t.Fatalf("ready for review: %+v", o)
		}
	}
	var bell models.Notification
	if err := f.a.DB.C(store.CNotifications).FindOne(f.ctx, bson.M{"groupKey": "recipe-run:" + run.ID}).Decode(&bell); err != nil || !strings.Contains(bell.Body, "2 shorts") {
		t.Fatalf("bell %+v %v", bell, err)
	}
	// A short's project lists the run that made it (Made with…).
	runs := must[RecipeRunsOut](t, f.a, "recipe.runs", RecipeRunsIn{ProjectID: done.Outputs[1].ProjectID}, false)
	if len(runs.Runs) != 1 || runs.Runs[0].ID != run.ID || len(runs.Runs[0].Outputs) != 2 {
		t.Fatalf("runs of a short's project %+v", runs.Runs)
	}
	// Making the run again keeps its shorts.
	ap := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Repeats: run.ID}, false)
	if len(ap.Pack) != 0 || !strings.Contains(strings.Join(ap.Reused, ";"), "shorts") {
		t.Fatalf("again %+v", ap)
	}
}
