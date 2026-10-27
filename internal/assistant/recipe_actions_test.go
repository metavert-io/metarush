package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/recipes"
	"github.com/metavert-io/metarush/internal/store"
)

// recipeFixture saves the fixture project's cut as a recipe, as a person
// (version 1 is current): "film", whose runs are new cuts in the project.
func recipeFixture(t *testing.T, a *app.App) (projectID string) {
	t.Helper()
	ctx := context.Background()
	projectID, _, _ = fixture(t, a)
	saved, err := ops.CallTyped[recipes.RecipeSaveOut](ctx, a, "recipe.save", recipes.RecipeSaveIn{From: projectID, Name: "film", Title: "Assistant film"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Version == nil || saved.Version.State != models.RecipeCurrent {
		t.Fatalf("a person's save is current: %+v", saved)
	}
	return projectID
}

// decide runs (or sets aside, or sends back) cards as a person in the dashboard.
func decide(t *testing.T, a *app.App, in ConversationActIn) (*ConversationActOut, error) {
	t.Helper()
	dash := *a
	dash.Actor.Via = "dashboard"
	in.Quiet = true
	res, err := ops.Call(context.Background(), &dash, "assistant.act", mustJSON(t, in), ops.Caller{Human: true, Remote: true})
	if err != nil {
		return nil, err
	}
	return res.(*ConversationActOut), nil
}

func cardOfKind(t *testing.T, cv *Conversation, op string, n int) AssistantAction {
	t.Helper()
	seen := 0
	for _, a := range cv.Actions {
		if a.Kind == op {
			if seen == n {
				return a
			}
			seen++
		}
	}
	t.Fatalf("no card %d for %q in %+v", n, op, cv.Actions)
	return AssistantAction{}
}

func partChanges(parts []RecipeCardChange, part string) string {
	for _, p := range parts {
		if p.Part == part {
			return strings.Join(p.Changes, " | ")
		}
	}
	return ""
}

// Claude's recipe requests in the panel become cards built by the server
// (D211): a run with its plan and estimate (people only), a save of the cut
// as a draft with what varies and stays, a draft of a change with what it
// changes (from Claude's hints, or a change it wrote in the proposals
// folder), making a version current with what it changes (people only).
// Archiving and dry runs never become cards; a version already current can't
// be made current. None runs by itself, not even under Auto with estimates off;
// runs and making a version current are decided on their own card; Not quite
// sends a card back. A run started from a card is its work: Claude hears when
// it waits for Claude. A plan that changed is planned again on its card.
func TestRecipeRequestsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	on, off := true, false
	projectID := recipeFixture(t, a)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CUsers, models.User{ID: a.Cfg.User.ID, WorkspaceID: a.WorkspaceID(), Name: "Tester", Email: "tester@example.com", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	ws := a.WorkspaceID()
	if n, _ := a.DB.C(store.CWorkspaces).CountDocuments(ctx, bson.M{"_id": ws}); n == 0 {
		if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "make it again", Steps: []fakeclaude.Step{
			// Reading and planning run: no cards.
			tool("Bash", map[string]any{"command": "metarush recipe list --json"}, `{"ok":true,"data":{"recipes":[{"name":"film"}],"summary":"1 recipe."}}`),
			tool("Bash", map[string]any{"command": "metarush recipe plan film --note 'the spring one' --json"}, `{"ok":true,"data":{"ready":true,"estimate":{"about":"nothing"}}}`),
			denied(`metarush recipe run film --note 'the spring one' --max-cost 99 --json`),
			denied(`metarush recipe save --from ` + projectID + ` --title 'Film again' --hint 'Open on the hook.' --json`),
			denied(`metarush recipe update film --hint 'End on the hook.' --note 'a stronger ending' --json`),
			{Write: &fakeclaude.File{Name: "rules.json", Content: `{"rules":[{"text":"First names only"}]}`}},
			denied(`metarush recipe update film --patch {{proposals}}/rules.json --json`),
			denied(`metarush recipe publish film --version 1 --json`),
			// Never cards: archiving is a person's, a dry run only reads.
			denied(`metarush recipe archive film --json`),
			denied(`metarush recipe save --from ` + projectID + ` --dry-run --json`),
			denied(`metarush recipe update film --patch /etc/hosts --json`),
			text("I put a run, a save and two changes on cards for you."),
		}},
		{Match: "make version 2 current", Steps: []fakeclaude.Step{denied(`metarush recipe publish film --version 2 --json`), text("It's on a card.")}},
		{Match: "run it once more", Steps: []fakeclaude.Step{denied(`metarush recipe run film --json`), text("A run is on a card.")}},
		{Match: "Not quite:", Steps: []fakeclaude.Step{text("I'll plan it again with that.")}},
		{Match: "make run 1 again", Steps: []fakeclaude.Step{denied(`metarush recipe again film#1 --input 'voice=Bram' --json`), text("It's on a card.")}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "make it again", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	if len(cv.Actions) != 5 {
		t.Fatalf("five cards (archiving, a dry run and a change from outside the proposals folder are never cards): %+v", cv.Actions)
	}
	for _, x := range cv.Actions {
		for _, line := range append([]string{x.Title}, x.Details...) {
			if f := plainwords.CheckPanel(line); len(f) > 0 {
				t.Errorf("not plain on a card: %q %+v", line, f)
			}
		}
	}

	// The run: its plan, people only, the fingerprint of what the person sees.
	run := cardOfKind(t, cv, "recipe run", 0)
	if run.Op != "recipe.run" || !run.HumanOnly || run.State != ActionPending || run.Recipe == nil || run.Recipe.Kind != "run" || run.Recipe.Recipe != "film" ||
		len(run.Recipe.Steps) == 0 || run.Recipe.Note != "the spring one" || !strings.HasPrefix(run.Title, "Run “") || run.Estimate != nil {
		t.Fatalf("the run's card: %+v %+v", run, run.Recipe)
	}
	if !strings.Contains(strings.Join(run.Details, "|"), "Only people start a run") || !strings.Contains(run.Details[0], "a new cut in") {
		t.Errorf("the run's lines: %v", run.Details)
	}
	var runIn recipeRunIn
	if err := json.Unmarshal([]byte(run.Input), &runIn); err != nil || len(runIn.Fingerprint) < 16 || runIn.MaxUSD != nil || runIn.Recipe != "film" || runIn.Note != "the spring one" {
		t.Fatalf("the run's input (Claude's --max-cost is never taken): %+v %v", runIn, err)
	}
	// The save: a draft, with what varies and what stays.
	save := cardOfKind(t, cv, "recipe save", 0)
	var saveIn recipeSaveIn
	_ = json.Unmarshal([]byte(save.Input), &saveIn)
	if save.Op != "recipe.save" || save.HumanOnly || save.Recipe == nil || save.Recipe.Kind != "save" || len(save.Recipe.Varies)+len(save.Recipe.Stays) == 0 ||
		!saveIn.Draft || saveIn.Title != "Film again" || !reflect.DeepEqual(saveIn.Hints, []string{"Open on the hook."}) || save.Title != "Save “Film again” as a recipe" {
		t.Fatalf("the save's card: %+v %+v %+v", save, save.Recipe, saveIn)
	}
	// The changes: what each draft changes, in plain words.
	hint, patch := cardOfKind(t, cv, "recipe update", 0), cardOfKind(t, cv, "recipe update", 1)
	if hint.Op != "recipe.update" || hint.Recipe == nil || hint.Recipe.Version != 1 || hint.Recipe.Note != "a stronger ending" ||
		partChanges(hint.Recipe.Differences, "hints") != "New hint: “End on the hook.”" || !strings.HasPrefix(hint.Details[0], "From version 1, it changes 1 thing: hints for Claude") {
		t.Fatalf("the hint's card: %+v %+v", hint, hint.Recipe)
	}
	if patch.Recipe == nil || !strings.Contains(partChanges(patch.Recipe.Differences, "rules"), "New rule: “First names only”") {
		t.Fatalf("the change Claude wrote: %+v %+v", patch, patch.Recipe)
	}
	// Making the current version current again: nothing to do.
	pub := cardOfKind(t, cv, "recipe publish", 0)
	if pub.State != ActionUnavailable || !strings.Contains(pub.Unavailable, "current one already") || pub.Op != "" {
		t.Fatalf("version 1 is current already: %+v", pub)
	}

	// None runs by itself: not under Auto, not with estimates off.
	a.Cfg.Assistant.SafeSteps = &on
	if _, err := ops.CallTyped[AssistantModeOut](ctx, a, "assistant.mode.set", AssistantModeSetIn{Mode: ModeAuto}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[prefs.PreferencesOut](ctx, a, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &off}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	c, _ := m.live(ctx, id, false)
	for _, x := range []AssistantAction{run, save, hint, patch} {
		x := x
		if m.startAuto(c, &x) || x.State != ActionPending || x.Auto {
			t.Errorf("Auto never runs a recipe card: %s %+v", x.Op, x)
		}
	}
	a.Cfg.Assistant.SafeSteps = &off

	// A run is agreed to on its own card (Approve all refuses it); setting cards aside together is fine.
	if _, err := decide(t, a, ConversationActIn{ID: id, ActionIDs: []string{run.ID, save.ID}}); code(err) != "E_CONFLICT" || reason(err) != "decide_alone" {
		t.Fatalf("a run with another card: %v", err)
	}
	// The hint's draft, saved by a person: a draft, with its link.
	done, err := decide(t, a, ConversationActIn{ID: id, ActionID: hint.ID})
	if err != nil {
		t.Fatal(err)
	}
	if d := done.Action; d.State != ActionDone || !strings.Contains(d.Result, "Draft version 2") || !strings.Contains(d.Link, "recipe=film&version=2") || !strings.Contains(d.ForClaude, "version 2, draft") {
		t.Fatalf("the draft saved: %+v", d)
	}
	// Making it current: what it changes, people only; then version 2 is current.
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "make version 2 current", ProjectID: projectID}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, id)
	pub2 := cardOfKind(t, cv, "recipe publish", 1)
	if pub2.Op != "recipe.publish" || !pub2.HumanOnly || pub2.State != ActionPending || pub2.Recipe == nil || pub2.Recipe.Version != 2 || pub2.Recipe.Against != 1 ||
		partChanges(pub2.Recipe.Differences, "hints") != "New hint: “End on the hook.”" || pub2.Title != "Make version 2 of “Assistant film” current" {
		t.Fatalf("making version 2 current: %+v %+v", pub2, pub2.Recipe)
	}
	if _, err := decide(t, a, ConversationActIn{ID: id, ActionID: pub2.ID}); err != nil {
		t.Fatal(err)
	}
	show, err := ops.CallTyped[recipes.RecipeShowOut](ctx, a, "recipe.show", recipes.RecipeRefIn{Recipe: "film"}, ops.Caller{})
	if err != nil || show.Recipe.Current != 2 {
		t.Fatalf("version 2 is current: %+v %v", show, err)
	}

	// Not quite sends the run back to Claude with the note.
	nq, err := decide(t, a, ConversationActIn{ID: id, ActionID: run.ID, NotQuite: "Use the headline Spring is here"})
	if err != nil || nq.Action.State != ActionNotQuite {
		t.Fatalf("not quite: %+v %v", nq, err)
	}
	cv = waitIdle(t, m, id)
	if last := cv.Turns[len(cv.Turns)-1]; !strings.Contains(last.Prompt, "Not quite: Run “") || !strings.Contains(last.Prompt, "Use the headline Spring is here") {
		t.Fatalf("Claude hears the note: %q", last.Prompt)
	}

	// A new run card; meanwhile someone else starts a run, so its plan changed.
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "run it once more", ProjectID: projectID}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, id)
	again := cardOfKind(t, cv, "recipe run", 1)
	plan, err := ops.CallTyped[recipes.RecipePlanOut](ctx, a, "recipe.plan", recipes.RecipePlanIn{Recipe: "film"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[recipes.RecipeRunOut](ctx, a, "recipe.run", recipes.RecipeRunIn{RecipePlanIn: recipes.RecipePlanIn{Recipe: "film"}, Fingerprint: plan.Fingerprint},
		ops.Caller{Human: true, Remote: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := decide(t, a, ConversationActIn{ID: id, ActionID: again.ID}); code(err) != "E_CONFLICT" {
		t.Fatalf("the plan changed: %v", err)
	}
	cv, _ = m.Get(ctx, id, false, false)
	fresh := cardOfKind(t, cv, "recipe run", 1)
	var freshIn recipeRunIn
	_ = json.Unmarshal([]byte(fresh.Input), &freshIn)
	if fresh.State != ActionPending || fresh.Recipe == nil || !fresh.Recipe.Replanned || freshIn.Fingerprint == "" || fresh.Input == again.Input {
		t.Fatalf("planned again on its card: %+v %+v", fresh, fresh.Recipe)
	}
	// Run as planned again: it starts, and the run is the card's work.
	started, err := decide(t, a, ConversationActIn{ID: id, ActionID: fresh.ID})
	if err != nil {
		t.Fatal(err)
	}
	d := started.Action
	if d.State != ActionDone || !strings.HasPrefix(d.Result, "Started run 2 of “Assistant film”") || !strings.Contains(d.Link, "/library/recipes?run=rrn_") ||
		d.Work == nil || d.Work.Kind != WorkRun || d.Work.State != WorkRunning || d.Work.What != "run 2 of “Assistant film”" {
		t.Fatalf("the run started: %+v %+v", d, d.Work)
	}
	// It waits for Claude: the card's work ends, and Claude is told what to do.
	if _, err := a.DB.C(store.CRecipeRuns).UpdateOne(ctx, bson.M{"_id": d.Work.RunID}, bson.M{"$set": bson.M{"state": models.RunWaiting, "waitingFor": models.RunWaitsForClaude,
		"waiting": "Waiting for Claude to choose the moments of “moments”"}}); err != nil {
		t.Fatal(err)
	}
	// The manager watches the card's work (as it does a render's) and ends it.
	var w *ActionWork
	eventually(t, "the run's wait to end the card's work", func() bool {
		cv, _ = m.Get(ctx, id, false, false)
		w = cardOfKind(t, cv, "recipe run", 1).Work
		return w != nil && w.State != WorkRunning
	})
	if w == nil || w.State != WorkFinished || w.Outcome != "Run 2 of “Assistant film” waits for Claude to choose the moments" {
		t.Fatalf("the work's outcome: %+v", w)
	}
	c.mu.Lock()
	forClaude := ""
	for _, x := range c.data.Actions {
		if x.ID == fresh.ID && x.Work != nil {
			forClaude = x.Work.ForClaude
		}
	}
	c.mu.Unlock()
	if !strings.Contains(forClaude, "waits for you") || !strings.Contains(forClaude, "film#2") {
		t.Fatalf("what Claude is told: %q", forClaude)
	}
	// Making a run again names the run (film#1): run 1 never made its cut, so it can't.
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "make run 1 again", ProjectID: projectID}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, id)
	ag := cardOfKind(t, cv, "recipe again", 0)
	if ag.Recipe == nil || ag.Recipe.Kind != "again" || ag.State != ActionUnavailable || !strings.Contains(ag.Unavailable, "It can't start yet") || !strings.HasPrefix(ag.Title, "Make run 1 of “Assistant film” again") {
		t.Fatalf("making run 1 again: %+v %+v", ag, ag.Recipe)
	}
}

// The panel reads --input like `metarush recipe run`; its lines name what
// Claude did plainly.
func TestRecipeCardWords(t *testing.T) {
	for _, raw := range [][]string{{`headline=Read free`, "art=hero, villain", "art=sidekick"}, {"voice=Bram"}, {}} {
		mine, err1 := recipeInputs(raw)
		theirs, err2 := recipes.InputsFromFlags(raw)
		a, _ := json.Marshal(mine)
		b, _ := json.Marshal(theirs)
		if (err1 == nil) != (err2 == nil) || string(a) != string(b) {
			t.Errorf("%v: %s vs %s", raw, a, b)
		}
	}
	if _, err := recipeInputs([]string{"nothing"}); err == nil {
		t.Error("an input without a value is refused")
	}
	// A card's run is the daemon engine's to advance (as one started from the dashboard); other cards keep their caller.
	if c := recipeCaller(AssistantAction{Op: "recipe.run"}, ops.Caller{Human: true}); !c.Remote || !c.Human {
		t.Errorf("a run card's caller: %+v", c)
	}
	if c := recipeCaller(AssistantAction{Op: "recipe.save"}, ops.Caller{Human: true}); c.Remote {
		t.Errorf("a save card's caller: %+v", c)
	}
	// A run is named recipe#N: inside a word a # is itself, a word starting with one is a comment.
	for in, ok := range map[string]bool{"metarush recipe again film#2 --json": true, "metarush recipe list # all of them": false} {
		words, err := splitCommand(in)
		if (err == nil) != ok || (ok && words[3] != "film#2") {
			t.Errorf("splitCommand(%q): %v %v", in, words, err)
		}
	}
	for cmd, want := range map[string]string{
		"metarush recipe list --json":                      "Looked at the recipes",
		"metarush recipe show film --json":                 "Read the recipe “film”",
		"metarush recipe plan film --input a=b --json":     "Planned a run of “film”",
		"metarush recipe plan --again film#2 --json":       "Planned making a run again",
		"metarush recipe runs --run film#2 --json":         "Looked at a run",
		"metarush recipe diff film --json":                 "Compared two versions of a recipe",
		"metarush recipe run film --json":                  "Asked to start a run of “film”",
		"metarush recipe publish film --version 2":         "Asked to make a version of a recipe current",
		"metarush recipe save --from prj_x --new-projects": "Asked to save the cut as a recipe",
	} {
		words, err := splitCommand(cmd)
		if err != nil {
			t.Fatal(err)
		}
		call, ok := parseMetarush(words)
		if !ok {
			t.Fatalf("%s: not parsed", cmd)
		}
		if got := recipeLabel(&call); got != want {
			t.Errorf("%s: %q, want %q", cmd, got, want)
		}
	}
	// Reading commands stay reading; the changes are refused and become cards.
	for cmd, readOnly := range map[string]bool{"metarush recipe list": true, "metarush recipe diff film --to 2": true, "metarush recipe runs --run film#2": true,
		"metarush recipe runs --run film#2 --wait": false, "metarush recipe run film": false, "metarush recipe update film --hint x": false} {
		words, _ := splitCommand(cmd)
		call, _ := parseMetarush(words)
		if readOnlyCall(call) != readOnly {
			t.Errorf("%s: read only %v, want %v", cmd, !readOnly, readOnly)
		}
	}
	var d map[string]json.RawMessage
	_ = json.Unmarshal([]byte(`{"ready":true,"estimate":{"about":"about $0.13"}}`), &d)
	if got := recipeOutcome("recipe plan", d); got != "about $0.13" {
		t.Errorf("a plan's outcome: %q", got)
	}
	_ = json.Unmarshal([]byte(`{"ready":false,"estimate":{"about":"nothing"}}`), &d)
	if got := recipeOutcome("recipe plan", d); got != "it can't start yet" {
		t.Errorf("a plan that can't start: %q", got)
	}
}

// The rest of Claude's recipe changes are cards too (D211 extended): keeping
// or changing a waiting run's first picks (D215), saving a recipe sheet it
// wrote as a draft (D213) and saving the changes suggested from runs as a
// draft (D217, unavailable while nothing repeats). Turning planning by itself
// on is never a card (a person does it, D218); reading suggestions and triggers
// stays reading. None runs by itself under Auto; Not quite sends one back; the
// picks card goes on with the run (Claude hears when it waits or is ready); the
// sheet saves a draft, never current.
func TestRecipePicksSheetAndSuggestionCards(t *testing.T) {
	a := testApp(t, "")
	on, off := true, false
	projectID := recipeFixture(t, a)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CUsers, models.User{ID: a.Cfg.User.ID, WorkspaceID: a.WorkspaceID(), Name: "Tester", Email: "tester@example.com", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// New footage: the run waits for Claude with a stand-in in “moments”.
	other := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{projectID}, Kind: "video", Name: "second.mov",
		SHA256: strings.Repeat("cd", 32), Size: 1000, MIME: "video/quicktime", StorageKey: "k2", State: models.AssetAvailable,
		Media:    models.MediaInfo{Container: "mov", Duration: 20, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, other); err != nil {
		t.Fatal(err)
	}
	plan := recipes.RecipePlanIn{Recipe: "film", Inputs: []recipes.RecipeInputValue{{Name: "footage", Items: []string{other.ID}}}}
	p, err := ops.CallTyped[recipes.RecipePlanOut](ctx, a, "recipe.plan", plan, ops.Caller{})
	if err != nil || !p.Ready || !p.Claude {
		t.Fatalf("plan %+v %v", p, err)
	}
	started, err := ops.CallTyped[recipes.RecipeRunOut](ctx, a, "recipe.run", recipes.RecipeRunIn{RecipePlanIn: plan, Fingerprint: p.Fingerprint}, ops.Caller{Human: true})
	if err != nil || started.Run.State != models.RunWaiting || started.Run.WaitingFor != models.RunWaitsForClaude {
		t.Fatalf("the run waits for Claude: %+v %v", started, err)
	}
	co, err := ops.CallTyped[recipes.RecipeCheckoutOut](ctx, a, "recipe.checkout", recipes.RecipeRefIn{Recipe: "film"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	sheet := strings.Replace(co.Sheet, "## The cut", "## Hints\n- End on the hook.\n\n## The cut", 1)
	if sheet == co.Sheet {
		t.Fatalf("the sheet has no cut: %s", co.Sheet)
	}
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "look at the picks", Steps: []fakeclaude.Step{
			tool("Bash", map[string]any{"command": "metarush recipe runs --run film#1 --json"}, `{"ok":true,"data":{}}`),
			tool("Bash", map[string]any{"command": "metarush recipe suggestions film --json"}, `{"ok":true,"data":{"suggestions":[]}}`),
			tool("Bash", map[string]any{"command": "metarush recipe triggers film --json"}, `{"ok":true,"data":{}}`),
			denied(`metarush recipe picks film#1 --keep --json`),
			denied(`metarush recipe picks film#1 --part moments --n 1 --from 0:02 --to 0:06.5 --why 'the strongest line' --json`),
			{Write: &fakeclaude.File{Name: "film.recipe.md", Content: sheet}},
			denied(`metarush recipe check {{proposals}}/film.recipe.md --save --note 'a stronger ending' --json`),
			denied(`metarush recipe check {{proposals}}/film.recipe.md --save --note 'another ending' --json`),
			denied(`metarush recipe suggestions film --save --json`),
			// Never cards: planning by itself is a person's to turn on; a sheet outside the proposals folder.
			denied(`metarush recipe triggers film --new-files on --json`),
			denied(`metarush recipe check /etc/hosts --save --json`),
			text("The picks, a sheet and the suggestions are on cards for you."),
		}},
		{Match: "Not quite:", Steps: []fakeclaude.Step{text("I'll pick another moment.")}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "look at the picks and tidy the recipe", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	if len(cv.Actions) != 5 {
		t.Fatalf("five cards (triggers and a sheet outside the proposals folder are never cards; reading is no card): %+v", cv.Actions)
	}
	for _, x := range cv.Actions {
		for _, line := range append([]string{x.Title, x.Unavailable}, x.Details...) {
			if f := plainwords.CheckPanel(line); len(f) > 0 {
				t.Errorf("not plain on a card: %q %+v", line, f)
			}
		}
	}
	keep, change := cardOfKind(t, cv, "recipe picks", 0), cardOfKind(t, cv, "recipe picks", 1)
	if keep.Op != "recipe.picks" || keep.HumanOnly || keep.State != ActionPending || keep.Recipe == nil || keep.Recipe.Kind != "picks" ||
		keep.Title != "Keep MetaRush's first picks in run 1 of “Assistant film”" || len(keep.Recipe.Picks) != 1 || !keep.Recipe.Picks[0].Changes || keep.Recipe.Picks[0].State != "standin" {
		t.Fatalf("the keep card: %+v %+v", keep, keep.Recipe)
	}
	var changeIn recipePicksIn
	_ = json.Unmarshal([]byte(change.Input), &changeIn)
	if change.Title != "Make moment 1 of “moments” 0:02.0–0:06.5 in run 1 of “Assistant film”" || len(changeIn.Set) != 1 || *changeIn.Set[0].From != 2 || *changeIn.Set[0].To != 6.5 ||
		changeIn.Set[0].Why != "the strongest line" || changeIn.Keep {
		t.Fatalf("the change card: %q %+v", change.Title, changeIn)
	}
	sheetCard := cardOfKind(t, cv, "recipe check", 0)
	if sheetCard.Op != "recipe.write" || sheetCard.Recipe == nil || sheetCard.Recipe.Kind != "sheet" || sheetCard.Recipe.Version != 1 || sheetCard.Recipe.Note != "a stronger ending" ||
		partChanges(sheetCard.Recipe.Differences, "hints") != "New hint: “End on the hook.”" || sheetCard.Title != "Save the changed sheet of “Assistant film” as a draft" {
		t.Fatalf("the sheet's card: %+v %+v", sheetCard, sheetCard.Recipe)
	}
	sug := cardOfKind(t, cv, "recipe suggestions", 0)
	if sug.State != ActionUnavailable || !strings.Contains(sug.Unavailable, "Nothing people changed") || sug.Op != "" || sug.Recipe == nil || sug.Recipe.Kind != "suggested" {
		t.Fatalf("no suggestion yet: %+v", sug)
	}
	// None runs by itself, under Auto with estimates off.
	a.Cfg.Assistant.SafeSteps = &on
	if _, err := ops.CallTyped[AssistantModeOut](ctx, a, "assistant.mode.set", AssistantModeSetIn{Mode: ModeAuto}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[prefs.PreferencesOut](ctx, a, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &off}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	c, _ := m.live(ctx, id, false)
	for _, x := range []AssistantAction{keep, change, sheetCard, cardOfKind(t, cv, "recipe check", 1)} {
		x := x
		if m.startAuto(c, &x) || x.State != ActionPending || x.Auto {
			t.Errorf("Auto never runs it: %s %+v", x.Op, x)
		}
	}
	a.Cfg.Assistant.SafeSteps = &off
	// Not quite sends the change back to Claude, and a sheet too.
	nq, err := decide(t, a, ConversationActIn{ID: id, ActionID: change.ID, NotQuite: "A later moment"})
	if err != nil || nq.Action.State != ActionNotQuite {
		t.Fatalf("not quite: %+v %v", nq, err)
	}
	cv = waitIdle(t, m, id)
	if last := cv.Turns[len(cv.Turns)-1]; !strings.Contains(last.Prompt, "Not quite: Make moment 1") || !strings.Contains(last.Prompt, "A later moment") {
		t.Fatalf("Claude hears the note: %q", last.Prompt)
	}
	other2 := cardOfKind(t, cv, "recipe check", 1)
	nq, err = decide(t, a, ConversationActIn{ID: id, ActionID: other2.ID, NotQuite: "Keep the old ending"})
	if err != nil || nq.Action.State != ActionNotQuite {
		t.Fatalf("not quite on a sheet: %+v %v", nq, err)
	}
	cv = waitIdle(t, m, id)
	if last := cv.Turns[len(cv.Turns)-1]; !strings.Contains(last.Prompt, "Not quite: Save the changed sheet") || !strings.Contains(last.Prompt, "Keep the old ending") {
		t.Fatalf("Claude hears the note on the sheet: %q", last.Prompt)
	}
	// Keeping the picks: the run goes on, and is the card's work.
	kept, err := decide(t, a, ConversationActIn{ID: id, ActionID: keep.ID})
	if err != nil {
		t.Fatal(err)
	}
	if d := kept.Action; d.State != ActionDone || !strings.Contains(d.Result, "Kept 1 pick") || !strings.Contains(d.Link, "/library/recipes?run=rrn_") || d.Work == nil || d.Work.Kind != WorkRun {
		t.Fatalf("kept: %+v %+v", d, d.Work)
	}
	runs, err := ops.CallTyped[recipes.RecipeRunsOut](ctx, a, "recipe.runs", recipes.RecipeRunsIn{Run: "film#1"}, ops.Caller{})
	if err != nil || runs.Run == nil || (runs.Run.State == models.RunWaiting && runs.Run.WaitingFor == models.RunWaitsForClaude) {
		t.Fatalf("the run goes on: %+v %v", runs.Run, err)
	}
	// The sheet: a draft, never current.
	saved, err := decide(t, a, ConversationActIn{ID: id, ActionID: sheetCard.ID})
	if err != nil {
		t.Fatal(err)
	}
	if d := saved.Action; d.State != ActionDone || !strings.Contains(d.Result, "Draft version 2") || !strings.Contains(d.Link, "recipe=film&version=2") || !strings.Contains(d.ForClaude, "version 2, draft") {
		t.Fatalf("the sheet saved: %+v", d)
	}
	show, err := ops.CallTyped[recipes.RecipeShowOut](ctx, a, "recipe.show", recipes.RecipeRefIn{Recipe: "film", Version: "2"}, ops.Caller{})
	if err != nil || show.Version.State != models.RecipeDraft || show.Recipe.Current != 1 || len(show.Hints) != 1 {
		t.Fatalf("a draft with the hint: %+v %v", show, err)
	}
}

// The panel reads the new commands' flags the way the CLI does: --keep and
// --drop take no value in recipe picks (recipe save --keep does), --save in
// recipe check and recipe suggestions; reading stays reading.
// Claude pointing an input at a Google Drive folder (D220) is a card like any
// other change: the draft says the input takes the folder's newest files (the
// folder is read only when a run is planned), and a malformed one is sent back.
func TestRecipeUpdateCardTakesADriveFolder(t *testing.T) {
	a := testApp(t, "")
	recipeFixture(t, a)
	ctx := context.Background()
	show, err := ops.CallTyped[recipes.RecipeShowOut](ctx, a, "recipe.show", recipes.RecipeRefIn{Recipe: "film"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	input := ""
	for _, in := range show.Inputs {
		if in.Kind == "footage" || in.Kind == "pictures" {
			input = in.Name
			break
		}
	}
	if input == "" {
		t.Fatalf("the fixture's recipe has no footage or pictures input: %+v", show.Inputs)
	}
	m := withFake(t, a, fakeclaude.Options{})
	card := func(cmd string) AssistantAction {
		t.Helper()
		words, err := splitCommand(cmd)
		if err != nil {
			t.Fatal(err)
		}
		call, ok := parseMetarush(words)
		if !ok {
			t.Fatalf("not a metarush command: %s", cmd)
		}
		var act AssistantAction
		m.describeRecipeUpdate(ctx, nil, &call, &act)
		return act
	}
	act := card(`metarush recipe update film --from-drive '` + input + `=gdrive:Shoots/Spring 2026' --note 'the shared Drive' --json`)
	var in recipeUpdateIn
	if act.Op != "recipe.update" || act.Recipe == nil || json.Unmarshal([]byte(act.Input), &in) != nil || in.FromDrive[input] != "gdrive:Shoots/Spring 2026" || in.DryRun {
		t.Fatalf("the card: %+v %+v", act, in)
	}
	if got := partChanges(act.Recipe.Differences, "inputs"); !strings.Contains(got, "the newest files of “Shoots/Spring 2026” in the Google Drive “gdrive”") {
		t.Fatalf("what the draft changes: %+v", act.Recipe.Differences)
	}
	for _, line := range append([]string{act.Title}, act.Details...) {
		if f := plainwords.CheckPanel(line); len(f) > 0 {
			t.Errorf("not plain on a card: %q %+v", line, f)
		}
	}
	if bad := card(`metarush recipe update film --from-drive gdrive:Shoots --json`); bad.Op != "" || !strings.Contains(bad.Unavailable, "input=remote:path") {
		t.Fatalf("a malformed folder: %+v", bad)
	}
}

func TestRecipeCardFlags(t *testing.T) {
	for cmd, want := range map[string]map[string]string{
		"metarush recipe picks film#2 --keep --json":                          {"keep": ""},
		"metarush recipe picks film#2 --drop --part moments --n 2":            {"drop": "", "part": "moments", "n": "2"},
		"metarush recipe save --from prj_x --keep footage":                    {"keep": "footage"},
		"metarush recipe check --save film.recipe.md":                         {"save": ""},
		"metarush recipe suggestions --save film":                             {"save": ""},
		"metarush recipe triggers --dismiss film":                             {"dismiss": ""},
		"metarush recipe triggers film --weekly 'monday 09:00'":               {"weekly": "monday 09:00"},
		"metarush recipe suggestions film --only length:intro:shorter --save": {"only": "length:intro:shorter", "save": ""},
	} {
		words, err := splitCommand(cmd)
		if err != nil {
			t.Fatal(err)
		}
		call, _ := parseMetarush(words)
		for k, v := range want {
			if got, ok := call.Flags[k]; !ok || got != v {
				t.Errorf("%s: --%s = %q, want %q (args %v)", cmd, k, got, v, call.Args)
			}
		}
	}
	for cmd, readOnly := range map[string]bool{"metarush recipe suggestions film": true, "metarush recipe suggestions film --save": false,
		"metarush recipe triggers": true, "metarush recipe triggers film --new-files on": false, "metarush recipe triggers film --dismiss": false,
		"metarush recipe picks film#2 --keep": false, "metarush recipe check film.recipe.md --save": false} {
		words, _ := splitCommand(cmd)
		call, _ := parseMetarush(words)
		if readOnlyCall(call) != readOnly {
			t.Errorf("%s: read only %v, want %v", cmd, !readOnly, readOnly)
		}
	}
	for cmd, want := range map[string]string{
		"metarush recipe picks film#2 --keep":        "Asked to keep or change a run's first picks",
		"metarush recipe check a.recipe.md --save":   "Asked to save a recipe sheet as a draft",
		"metarush recipe suggestions film":           "Looked at the changes suggested for a recipe",
		"metarush recipe suggestions film --save":    "Asked to save the changes suggested for a recipe as a draft",
		"metarush recipe triggers":                   "Looked at when recipes plan runs by themselves",
		"metarush recipe triggers film --weekly off": "Asked for something only a person does with a recipe",
		"metarush recipe export film":                "Asked to export a recipe to a file",
		"metarush recipe import film.recipe.zip":     "Asked to bring in a recipe file",
	} {
		words, _ := splitCommand(cmd)
		call, _ := parseMetarush(words)
		if got := recipeLabel(&call); got != want {
			t.Errorf("%s: %q, want %q", cmd, got, want)
		}
	}
}
