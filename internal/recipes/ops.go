package recipes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// The recipe.* operations (D201–D207): the CLI (`metarush recipe …`), the
// dashboard's Recipes tab and Claude call the same ones. Making a version
// current, archiving, starting a run and letting a waiting one spend more are
// people only; Claude makes drafts and plans.

// RecipeListIn lists recipes.
type RecipeListIn struct {
	Archived  bool   `json:"archived,omitempty" jsonschema:"description=Archived recipes too."`
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Only recipes saved from this project (ID or name)."`
	Query     string `json:"query,omitempty" jsonschema:"maxLength=200,description=Words in a recipe's name or title."`
}

// RecipeListOut lists recipes as cards.
type RecipeListOut struct {
	Recipes []RecipeRow `json:"recipes"`
	Summary string      `json:"summary"`
}

// RecipeRefIn picks a recipe and, optionally, a version.
type RecipeRefIn struct {
	Recipe  string `json:"recipe" jsonschema:"minLength=1,description=Its short name\\, title or ID."`
	Version string `json:"version,omitempty" jsonschema:"description=A version: its number\\, current\\, draft or 7+ characters of its hash (default: the current one\\, else the newest draft)."`
}

// RecipeShowOut is a recipe in full: its versions, inputs with their types,
// defaults and limits, sections, sound, rules, hints, the check and its runs.
type RecipeShowOut struct {
	Recipe   RecipeRow          `json:"recipe"`
	Version  RecipeVersionRow   `json:"version"`
	Versions []RecipeVersionRow `json:"versions"`
	Inputs   []RecipeInputRow   `json:"inputs"`
	Sections []RecipeSectionRow `json:"sections"`
	Rules    []string           `json:"rules"`
	Signoffs []string           `json:"signoffs"`
	Hints    []string           `json:"hints"`
	Lands    string             `json:"lands"`
	// Readable is the recipe's outline in Markdown.
	Readable string           `json:"readable"`
	Doc      models.RecipeDoc `json:"doc"`
	Runs     []RecipeRunBrief `json:"runs"`
	// Imported says where the version came from when it was brought in from a
	// recipe file (D219), and what its code waits for here.
	Imported *RecipeImportView `json:"imported,omitempty"`
}

// RecipeSaveIn saves a project's cut as a recipe.
type RecipeSaveIn struct {
	From    string `json:"from" jsonschema:"minLength=1,description=The project (ID or name)."`
	Branch  string `json:"branch,omitempty" jsonschema:"description=Which cut (default: the main one)."`
	Version string `json:"version,omitempty" jsonschema:"description=Which version of the cut (its number or ID; default: its newest approved one\\, else its newest)."`
	Name    string `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$,description=The recipe's short name (default: from its title)."`
	Title   string `json:"title,omitempty" jsonschema:"maxLength=120,description=The recipe's title (default: the cut's name)."`
	// NewProjects: every run makes a project of its own (else runs are new
	// cuts in this project, D203).
	NewProjects bool     `json:"newProjects,omitempty" jsonschema:"description=Every run makes a project of its own (default: runs are new cuts in this project)."`
	Vary        []string `json:"vary,omitempty" jsonschema:"maxItems=40,description=Proposal items to change each run (keys from the proposal)."`
	Keep        []string `json:"keep,omitempty" jsonschema:"maxItems=40,description=Proposal inputs to keep the same every run."`
	Fixed       []string `json:"fixed,omitempty" jsonschema:"maxItems=40,description=Sections MetaRush builds the same every run (no choosing)."`
	Chosen      []string `json:"chosen,omitempty" jsonschema:"maxItems=40,description=Sections of footage Claude chooses again each run."`
	// Pack adds the proposal's pack outputs (D214): every run also makes them.
	Pack  []string `json:"pack,omitempty" jsonschema:"maxItems=10,description=Pack outputs every run also makes (keys from the proposal: shorts of a conversation the cut plays)."`
	Hints []string `json:"hints,omitempty" jsonschema:"maxItems=20,description=Hints that guide Claude (Name the app in the first two seconds)."`
	Note  string   `json:"note,omitempty" jsonschema:"maxLength=300"`
	// DryRun shows the proposal; nothing is saved.
	DryRun bool `json:"dryRun,omitempty" jsonschema:"description=Show what would vary and what would stay; save nothing."`
	// Draft keeps the first version a draft even when a person saves it.
	Draft bool `json:"draft,omitempty" jsonschema:"description=Keep the first version a draft (a person makes it current later)."`
}

// RecipeSaveOut says what was saved (or would be).
type RecipeSaveOut struct {
	DryRun bool `json:"dryRun"`
	// Title, Makes and Lands say what the recipe is called, what it makes
	// and where its runs go, plainly (the proposal's, before it is saved too).
	Title    string               `json:"title"`
	Makes    string               `json:"makes"`
	Lands    string               `json:"lands"`
	Recipe   *RecipeRow           `json:"recipe,omitempty"`
	Version  *RecipeVersionRow    `json:"version,omitempty"`
	Proposal []RecipeProposalItem `json:"proposal"`
	Check    *models.RecipeCheck  `json:"check,omitempty"`
	Readable string               `json:"readable"`
	Message  string               `json:"message"`
	Next     string               `json:"next,omitempty"`
}

// RecipeUpdateIn makes a draft from a version with changes: a JSON merge
// patch (RFC 7386) over its document, and simple additions.
type RecipeUpdateIn struct {
	RecipeRefIn
	Patch    json.RawMessage `json:"patch,omitempty" jsonschema:"type=object,description=A merge patch over the version's document (fields to change; null removes; arrays are replaced whole)."`
	Title    string          `json:"title,omitempty" jsonschema:"maxLength=120"`
	AddHints []string        `json:"addHints,omitempty" jsonschema:"maxItems=20,description=Hints to add."`
	AddRules []string        `json:"addRules,omitempty" jsonschema:"maxItems=20,description=Rules to add."`
	// FromDrive points pictures or footage inputs at Google Drive folders read
	// through the person's own rclone (D220): input name → remote:path ("" takes
	// the folder away again).
	FromDrive map[string]string `json:"fromDrive,omitempty" jsonschema:"description=Inputs that take their pictures or footage from a Google Drive folder through rclone: input name → remote:path (empty takes it away)."`
	// FromDropbox does the same with Dropbox folders (D306).
	FromDropbox map[string]string `json:"fromDropbox,omitempty" jsonschema:"description=Inputs that take their pictures or footage from a Dropbox folder through rclone: input name → remote:path (empty takes it away)."`
	Note        string            `json:"note,omitempty" jsonschema:"maxLength=300,description=What the draft changes\\, in a few words."`
	// DryRun says what the draft would change; nothing is saved (D212).
	DryRun bool `json:"dryRun,omitempty" jsonschema:"description=Say what the draft would change from the version it starts from; save nothing."`
}

// RecipeUpdateOut is the draft made.
type RecipeUpdateOut struct {
	Version RecipeVersionRow    `json:"version"`
	Check   *models.RecipeCheck `json:"check,omitempty"`
	// Same: nothing changed (the version it came from is returned).
	Same    bool   `json:"same"`
	Message string `json:"message"`
	Next    string `json:"next,omitempty"`
	// Recipe and Title name the recipe; Changes are what the draft changes from
	// the version it starts from, in plain words (with a dry run: would change).
	Recipe       string             `json:"recipe"`
	Title        string             `json:"title"`
	Changes      []RecipeChangePart `json:"changes,omitempty"`
	RemovedRules []string           `json:"removedRules,omitempty"`
	DryRun       bool               `json:"dryRun,omitempty"`
}

// RecipeCheckIn checks a version, a document not saved, or a recipe sheet
// (D213: what it changes, and its problems by line).
type RecipeCheckIn struct {
	Recipe  string          `json:"recipe,omitempty"`
	Version string          `json:"version,omitempty"`
	Doc     json.RawMessage `json:"doc,omitempty" jsonschema:"type=object,description=A recipe document to check without saving it."`
	Sheet   string          `json:"sheet,omitempty" jsonschema:"maxLength=4000000,description=A recipe sheet (recipe checkout writes one) to check without saving it."`
}

// RecipeCheckOut is what a check found.
type RecipeCheckOut struct {
	Check   models.RecipeCheck `json:"check"`
	Message string             `json:"message"`
	// For a sheet: whether it reads (Valid) and its problems by line, the
	// recipe it names and the version it builds on (Base; New: a new recipe),
	// and what it changes in plain words (Same: nothing).
	Valid    bool               `json:"valid,omitempty"`
	Problems []cutsheet.Problem `json:"problems,omitempty"`
	Name     string             `json:"name,omitempty"`
	Base     *RecipeVersionRow  `json:"base,omitempty"`
	New      bool               `json:"new,omitempty"`
	Same     bool               `json:"same,omitempty"`
	Changes  []string           `json:"changes,omitempty"`
	// Title is the sheet's recipe title; Parts what it changes from its base
	// part by part, as recipe diff says it (D212), and RemovedRules the base's
	// rules it no longer keeps (what a card about saving it shows).
	Title        string             `json:"title,omitempty"`
	Parts        []RecipeChangePart `json:"parts,omitempty"`
	RemovedRules []string           `json:"removedRules,omitempty"`
}

// RecipePublishOut says which version runs make now.
type RecipePublishOut struct {
	Recipe  RecipeRow        `json:"recipe"`
	Version RecipeVersionRow `json:"version"`
	// RemovedRules are rules of the version before that this one doesn't keep.
	RemovedRules []string `json:"removedRules,omitempty"`
	Message      string   `json:"message"`
}

// RecipeArchiveIn archives (or restores) a recipe.
type RecipeArchiveIn struct {
	Recipe  string `json:"recipe" jsonschema:"minLength=1"`
	Restore bool   `json:"restore,omitempty"`
}

// RecipeArchiveOut is the recipe after.
type RecipeArchiveOut struct {
	Recipe  RecipeRow `json:"recipe"`
	Message string    `json:"message"`
}

// RecipePlanIn plans a run: the recipe, what the run gives its inputs, and
// for a run again the run it makes again.
type RecipePlanIn struct {
	Recipe  string             `json:"recipe,omitempty" jsonschema:"description=The recipe (short name\\, title or ID); with repeats\\, the run's."`
	Version string             `json:"version,omitempty" jsonschema:"description=A version to plan (default: the current one). Runs make the current one."`
	Inputs  []RecipeInputValue `json:"inputs,omitempty" jsonschema:"maxItems=40"`
	Repeats string             `json:"repeats,omitempty" jsonschema:"description=A run to make again (its ID or recipe#number): a new version of the same cut."`
	Note    string             `json:"note,omitempty" jsonschema:"maxLength=500,description=What this run is for or changes (a warmer voice)."`
}

// RecipePlanOut is a run's plan: inputs as they will be, where the video
// goes, each step, the estimate and a fingerprint.
type RecipePlanOut struct {
	Ready      bool                    `json:"ready"`
	Problems   []string                `json:"problems"`
	Warnings   []string                `json:"warnings"`
	Recipe     string                  `json:"recipe"`
	Title      string                  `json:"title"`
	Version    int                     `json:"version"`
	Lands      string                  `json:"lands"`
	ProjectID  string                  `json:"projectId,omitempty"`
	Project    string                  `json:"project"`
	Branch     string                  `json:"branch,omitempty"`
	NewProject bool                    `json:"newProject,omitempty"`
	Repeats    *RecipeRunBrief         `json:"repeats,omitempty"`
	Inputs     []models.RecipeRunInput `json:"inputs"`
	Steps      []models.RecipeStep     `json:"steps"`
	// Estimate is the run's (D208): "about $0.40", opening to each priced
	// step's parts (the step's label on each part).
	Estimate costs.Estimate            `json:"estimate"`
	Budget   []string                  `json:"budget"`
	Changes  []string                  `json:"changes"`
	Reused   []string                  `json:"reused"`
	Claude   bool                      `json:"claude" jsonschema:"description=The run waits for Claude to choose moments\\, or to keep or change first picks."`
	Settings *models.RecipeRunSettings `json:"settings,omitempty"`
	// Pack are the shorts the run makes too (D214), each with its first pick
	// (D215): the stretch of the conversation, why, its first words.
	Pack []models.RecipeRunOutput `json:"pack,omitempty"`
	// AskForEstimate: the person agrees to the estimate before it starts —
	// their "Estimate costs of outside AI services before edits" (Settings →
	// Preferences, D208); budgets and limits hold either way.
	AskForEstimate bool `json:"askForEstimate"`
	// Sample is the sample the run makes first (D294): one priced sample of
	// each expensive kind of work, approved by a person before the rest is
	// planned; what is agreed at the start is its estimate.
	Sample      *RecipeSamplePlan `json:"sample,omitempty"`
	Fingerprint string            `json:"fingerprint"`
	Summary     string            `json:"summary"`
	Next        string            `json:"next,omitempty"`
}

// RecipeRunIn starts a run a person agreed to (people only).
type RecipeRunIn struct {
	RecipePlanIn
	Fingerprint string `json:"fingerprint" jsonschema:"minLength=16,description=The plan's fingerprint (recipe.plan): what starts is exactly what the person saw."`
	// MaxUSD is what the person agrees to spend (at least the estimate, or the
	// sample's when the run starts with one); left out when the estimate is
	// nothing or their preference skips it.
	MaxUSD *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0,description=The most it may spend in US dollars (at least the estimate's; when the run starts with a sample\\, at least the sample's)."`
	// SkipSample starts the whole run without its sample (D294), for the reason given.
	SkipSample string `json:"skipSample,omitempty" jsonschema:"maxLength=500,description=Start the whole run without the sample its plan asks for first; why (kept with the run)."`
}

// RecipeAgainIn makes a run's video again with changes (people only): a new
// version of the same cut.
type RecipeAgainIn struct {
	Run         string             `json:"run" jsonschema:"minLength=1,description=The run to make again (its ID or recipe#number)."`
	Inputs      []RecipeInputValue `json:"inputs,omitempty" jsonschema:"maxItems=40"`
	Note        string             `json:"note,omitempty" jsonschema:"maxLength=500"`
	Fingerprint string             `json:"fingerprint" jsonschema:"minLength=16"`
	MaxUSD      *float64           `json:"maxUsd,omitempty" jsonschema:"minimum=0,description=The most it may spend in US dollars (at least the estimate's; when it starts with a sample\\, at least the sample's)."`
	// SkipSample makes it again without the sample its plan asks for first (D294), for the reason given.
	SkipSample string `json:"skipSample,omitempty" jsonschema:"maxLength=500,description=Make it again without the sample its plan asks for first; why (kept with the run)."`
}

// RecipeRunOut is a run after an action.
type RecipeRunOut struct {
	Run     RecipeRunView `json:"run"`
	Message string        `json:"message"`
}

// RecipeRunsIn lists runs, or shows one.
type RecipeRunsIn struct {
	Recipe    string `json:"recipe,omitempty" jsonschema:"description=One recipe's runs."`
	Run       string `json:"run,omitempty" jsonschema:"description=One run in full (its ID or recipe#number)."`
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Runs whose video (or one of whose shorts) is in this project."`
	Limit     int    `json:"limit,omitempty" jsonschema:"minimum=0,maximum=200"`
}

// RecipeRunsOut lists runs (and one in full).
type RecipeRunsOut struct {
	Runs    []RecipeRunBrief `json:"runs"`
	Run     *RecipeRunView   `json:"run,omitempty"`
	Summary string           `json:"summary"`
}

// RecipeCompareIn picks two runs.
type RecipeCompareIn struct {
	Left  string `json:"left" jsonschema:"minLength=1,description=A run (its ID or recipe#number)."`
	Right string `json:"right" jsonschema:"minLength=1"`
}

// RecipeContinueIn lets a waiting run go on (people only).
type RecipeContinueIn struct {
	Run     string   `json:"run" jsonschema:"minLength=1"`
	MoreUSD *float64 `json:"moreUsd,omitempty" jsonschema:"minimum=0,description=Agree to spend this much more in US dollars."`
}

// RecipeStopIn stops a run.
type RecipeStopIn struct {
	Run string `json:"run" jsonschema:"minLength=1"`
}

func init() {
	ops.Def("recipe.list", "List the workspace's recipes: what each makes, its inputs, its example and its last runs (archived ones with archived)", ops.Flags{}, listRecipes)
	ops.Def("recipe.show", "Show a recipe: its versions, inputs (type, default and limits), sections, sound, rules, hints, its last check and its runs", ops.Flags{}, showRecipe)
	ops.Def("recipe.propose", "Show what saving a project's cut as a recipe would vary each run and keep (nothing is saved): the proposal, with the keys vary, keep, fixed and chosen take, the recipe in plain words and its check", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RecipeSaveIn) (*RecipeSaveOut, error) {
			cp := *in
			cp.DryRun = true
			return saveRecipe(ctx, a, &cp)
		})
	ops.Def("recipe.save", "Save a project's cut as a recipe: MetaRush proposes what varies (the project's own footage, words in graphics, word settings, pictures, the voice) and what stays; vary, keep, fixed and chosen move items; a person's save is current, Claude's a draft", ops.Flags{Mutating: true}, saveRecipe)
	ops.Def("recipe.update", "Make a draft version of a recipe from a version with changes (a merge patch over its document, a title, hints or rules to add); a person makes it current", ops.Flags{Mutating: true}, updateRecipe)
	ops.Def("recipe.check", "Check a recipe version (or a document): its inputs and where they go, against the graphics' fields, the word settings and approvals, then a dry assembly with its own inputs and stills of each section", ops.Flags{}, checkRecipe)
	ops.Def("recipe.publish", "Make a version of a recipe current: the one runs make (removed rules are named) — people only", ops.Flags{Mutating: true, HumanOnly: true}, publishRecipe)
	ops.Def("recipe.archive", "Archive a recipe (or restore it): it leaves the Recipes tab and can't run; its versions and runs stay — people only", ops.Flags{Mutating: true, HumanOnly: true}, archiveRecipe)
	ops.Def("recipe.plan", "Plan a run of a recipe (nothing starts): the inputs as they will be, where the video goes, each step, one estimate in dollars that opens to each step's outside service, model, source and price, whether the person is asked to agree to it, and a fingerprint", ops.Flags{}, planRecipe)
	ops.Def("recipe.run", "Start a run of a recipe a person agreed to (the plan's fingerprint and an amount at least its estimate): a new project or a new cut, made through the ordinary steps — people only", ops.Flags{Mutating: true, HumanOnly: true}, runRecipe)
	ops.Def("recipe.again", "Make a run's video again with changes (new inputs, another voice): a new version of the same cut, the plan saying what changed and what is reused — people only", ops.Flags{Mutating: true, HumanOnly: true}, againRecipe)
	ops.Def("recipe.runs", "List runs of recipes (when, who, where, cost, approved), or show one with its steps and what it waits for", ops.Flags{}, listRuns)
	ops.Def("recipe.compare", "Compare two runs of a recipe: changed inputs, notes, recipe versions, cost, and the cut-sheet difference of the versions they made", ops.Flags{}, func(ctx context.Context, a *app.App, in *RecipeCompareIn) (*RecipeCompareOut, error) {
		return compareRuns(ctx, a, in.Left, in.Right)
	})
	ops.Def("recipe.continue", "Let a run that waits for a person go on (after a budget was raised or consent recorded), agreeing to spend more when it asks — people only", ops.Flags{Mutating: true, HumanOnly: true}, func(ctx context.Context, a *app.App, in *RecipeContinueIn) (*RecipeRunOut, error) {
		run, err := continueRun(ctx, a, in.Run, in.MoreUSD)
		if err != nil {
			return nil, err
		}
		v := runView(ctx, a, run)
		return &RecipeRunOut{Run: *v, Message: upperFirst(runName(run)) + ": " + strings.ToLower(v.StateLabel[:1]) + v.StateLabel[1:] + "."}, nil
	})
	ops.Def("recipe.stop", "Stop a run: work it asked for that hasn't started is called off; what it made stays", ops.Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *RecipeStopIn) (*RecipeRunOut, error) {
		run, err := stopRun(ctx, a, in.Run)
		if err != nil {
			return nil, err
		}
		return &RecipeRunOut{Run: *runView(ctx, a, run), Message: upperFirst(runName(run)) + " is stopped; what it made stays."}, nil
	})
}

func listRecipes(ctx context.Context, a *app.App, in *RecipeListIn) (*RecipeListOut, error) {
	filter := bson.M{}
	if !in.Archived {
		filter["archived"] = bson.M{"$ne": true}
	}
	if in.ProjectID != "" {
		p, err := ops.ResolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		filter["projectId"] = p.ID
	}
	var rs []models.Recipe
	if err := a.DB.Find(ctx, store.CRecipes, a.WorkspaceID(), filter, &rs, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(500)); err != nil {
		return nil, err
	}
	out := &RecipeListOut{Recipes: []RecipeRow{}}
	q := strings.ToLower(strings.TrimSpace(in.Query))
	for i := range rs {
		r := &rs[i]
		if q != "" && !strings.Contains(strings.ToLower(r.Name+" "+r.Title), q) {
			continue
		}
		v, _ := loadVersion(ctx, a, r, "")
		out.Recipes = append(out.Recipes, recipeRow(ctx, a, r, v))
	}
	switch n := len(out.Recipes); n {
	case 0:
		out.Summary = "No recipes yet. Save a video that worked as a recipe to make it again."
	default:
		out.Summary = plural(n, "recipe") + "."
	}
	return out, nil
}

func showRecipe(ctx context.Context, a *app.App, in *RecipeRefIn) (*RecipeShowOut, error) {
	r, err := loadRecipe(ctx, a, in.Recipe)
	if err != nil {
		return nil, err
	}
	v, err := loadVersion(ctx, a, r, in.Version)
	if err != nil {
		return nil, err
	}
	vs, err := listVersions(ctx, a, r.ID)
	if err != nil {
		return nil, err
	}
	numbers := map[string]int{}
	for _, x := range vs {
		numbers[x.Hash] = x.Number
	}
	out := &RecipeShowOut{Recipe: recipeRow(ctx, a, r, v), Version: versionRow(v, numbers), Inputs: inputRows(ctx, a, &v.Doc), Sections: sectionRows(ctx, a, &v.Doc),
		Rules: []string{}, Signoffs: append([]string{}, v.Doc.Signoffs...), Hints: append([]string{}, v.Doc.Hints...), Lands: landsWords(ctx, a, &v.Doc),
		Readable: readable(ctx, a, &v.Doc), Doc: v.Doc, Runs: []RecipeRunBrief{}}
	for i := range vs {
		out.Versions = append(out.Versions, versionRow(&vs[i], numbers))
	}
	for _, rule := range v.Doc.Rules {
		out.Rules = append(out.Rules, rule.Text)
	}
	runs, _ := runsOf(ctx, a, bson.M{"recipeId": r.ID}, 20)
	for i := range runs {
		out.Runs = append(out.Runs, runBrief(ctx, a, &runs[i]))
	}
	out.Imported = importView(ctx, a, v)
	return out, nil
}

func saveRecipe(ctx context.Context, a *app.App, in *RecipeSaveIn) (*RecipeSaveOut, error) {
	src, err := cutToSave(ctx, a, in.From, in.Branch, in.Version)
	if err != nil {
		return nil, err
	}
	pr, err := propose(ctx, a, src, in)
	if err != nil {
		return nil, err
	}
	out := &RecipeSaveOut{DryRun: in.DryRun, Title: pr.Doc.Title, Makes: pr.Doc.Makes, Lands: landsWords(ctx, a, pr.Doc), Proposal: pr.Items, Readable: readable(ctx, a, pr.Doc)}
	chk := checkDoc(ctx, a, pr.Doc)
	out.Check = chk
	if in.DryRun {
		out.Message = fmt.Sprintf("Here is what a recipe from version %d of the cut would vary and keep; nothing is saved yet.", src.rev.Number)
		out.Next = "Move items between the lists if you like, then save it."
		return out, nil
	}
	r, err := newRecipe(ctx, a, in.Name, pr.Doc.Title, src.project.ID)
	if err != nil {
		return nil, err
	}
	v, _, err := saveVersion(ctx, a, r, pr.Doc, "", in.Note, chk)
	if err != nil {
		_, _ = a.DB.C(store.CRecipes).DeleteOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()}) // never a recipe without a version
		return nil, err
	}
	if ex := exampleOf(ctx, a, src.rev); ex != nil {
		_, _ = a.DB.C(store.CRecipes).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"example": ex}})
		r.Example = ex
	}
	human := ops.CallerFrom(ctx).Human
	current := human && !in.Draft && chk.OK
	if current {
		if err := makeCurrent(ctx, a, r, v); err != nil {
			return nil, err
		}
	}
	a.Audit(ctx, "recipe.save", "recipe", r.ID, src.project.ID, map[string]any{"name": r.Name, "version": v.Number, "fromRevision": src.rev.ID, "current": current})
	row := recipeRow(ctx, a, r, v)
	vr := versionRow(v, nil)
	out.Recipe, out.Version = &row, &vr
	switch {
	case current:
		out.Message = fmt.Sprintf("Saved “%s” as a recipe: version 1 is current, so it can run now.", r.Title)
	case !chk.OK:
		out.Message = fmt.Sprintf("Saved “%s” as a recipe draft; its check found %s to fix before a person makes it current.", r.Title, plural(len(chk.Problems), "problem"))
	default:
		out.Message = fmt.Sprintf("Saved “%s” as a recipe draft; a person makes it current before it runs.", r.Title)
	}
	out.Next = "Plan a run to see what it makes and what it costs."
	return out, nil
}

func updateRecipe(ctx context.Context, a *app.App, in *RecipeUpdateIn) (*RecipeUpdateOut, error) {
	r, base, doc, err := draftOf(ctx, a, in)
	if err != nil {
		return nil, err
	}
	chk := checkDoc(ctx, a, doc)
	if in.DryRun {
		return draftPreview(ctx, a, r, base, doc, chk), nil
	}
	v, made, err := saveVersion(ctx, a, r, doc, base.Hash, in.Note, chk)
	if err != nil {
		return nil, err
	}
	numbers := map[string]int{base.Hash: base.Number}
	out := &RecipeUpdateOut{Version: versionRow(v, numbers), Check: chk, Same: !made, Recipe: r.Name, Title: r.Title}
	if made {
		out.Changes, out.RemovedRules = Differences(ctx, a, &base.Doc, doc), removedRules(&base.Doc, doc)
	}
	switch {
	case !made:
		out.Message = fmt.Sprintf("Nothing changed: that is version %d of “%s”.", v.Number, r.Title)
	case !chk.OK:
		out.Message = fmt.Sprintf("Draft version %d of “%s” is saved; its check found %s to fix.", v.Number, r.Title, plural(len(chk.Problems), "problem"))
	default:
		out.Message = fmt.Sprintf("Draft version %d of “%s” is saved; a person makes it current.", v.Number, r.Title)
		out.Next = "A person looks at it and makes it current."
	}
	if made {
		a.Audit(ctx, "recipe.update", "recipe", r.ID, r.ProjectID, map[string]any{"version": v.Number, "from": base.Number})
	}
	return out, nil
}

// mergePatch applies a JSON merge patch (RFC 7386).
func mergePatch(doc, patch []byte) ([]byte, error) {
	var d, p any
	if err := json.Unmarshal(doc, &d); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(patch, &p); err != nil {
		return nil, apperr.New(apperr.InvalidInput, "The change can't be read.")
	}
	return json.Marshal(mergeValue(d, p))
}

func mergeValue(target, patch any) any {
	pm, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	tm, ok := target.(map[string]any)
	if !ok {
		tm = map[string]any{}
	}
	for k, v := range pm {
		if v == nil {
			delete(tm, k)
			continue
		}
		tm[k] = mergeValue(tm[k], v)
	}
	return tm
}

func checkRecipe(ctx context.Context, a *app.App, in *RecipeCheckIn) (*RecipeCheckOut, error) {
	var doc *models.RecipeDoc
	switch {
	case strings.TrimSpace(in.Sheet) != "":
		return checkSheet(ctx, a, in.Sheet)
	case len(bytes.TrimSpace(in.Doc)) > 0:
		var d models.RecipeDoc
		dec := json.NewDecoder(bytes.NewReader(in.Doc))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&d); err != nil {
			return nil, apperr.Wrap(apperr.InvalidInput, err, "That isn't a recipe MetaRush can read (a field is unknown or has the wrong kind of value).").WithDetails(map[string]any{"problem": err.Error()})
		}
		doc = &d
	case in.Recipe != "":
		r, err := loadRecipe(ctx, a, in.Recipe)
		if err != nil {
			return nil, err
		}
		v, err := loadVersion(ctx, a, r, in.Version)
		if err != nil {
			return nil, err
		}
		doc = &v.Doc
	default:
		return nil, apperr.New(apperr.InvalidInput, "Say which recipe to check, or give a recipe to check.")
	}
	chk := checkDoc(ctx, a, doc)
	out := &RecipeCheckOut{Check: *chk}
	switch {
	case chk.OK && len(chk.Warnings) == 0:
		out.Message = fmt.Sprintf("The recipe checks out: with its own inputs it makes %s.", seconds(chk.Duration))
	case chk.OK:
		out.Message = fmt.Sprintf("The recipe checks out, with %s to look at: with its own inputs it makes %s.", plural(len(chk.Warnings), "thing"), seconds(chk.Duration))
	default:
		out.Message = fmt.Sprintf("The recipe has %s to fix: %s", plural(len(chk.Problems), "problem"), chk.Problems[0].Message)
	}
	return out, nil
}

func publishRecipe(ctx context.Context, a *app.App, in *RecipeRefIn) (*RecipePublishOut, error) {
	r, err := loadRecipe(ctx, a, in.Recipe)
	if err != nil {
		return nil, err
	}
	ref := in.Version
	if ref == "" {
		ref = "draft"
	}
	v, err := loadVersion(ctx, a, r, ref)
	if err != nil {
		return nil, err
	}
	if r.Archived {
		return nil, apperr.New(apperr.Conflict, "“%s” is archived; restore it first.", r.Title)
	}
	chk := checkDoc(ctx, a, &v.Doc)
	recordCheck(ctx, a, v, chk)
	if !chk.OK {
		return nil, apperr.New(apperr.Validation, "Version %d of “%s” can't be made current: %s", v.Number, r.Title, chk.Problems[0].Message).
			WithDetails(map[string]any{"problems": chk.Problems})
	}
	var removed []string
	if r.Current != "" && r.Current != v.Hash {
		if old, err := loadVersion(ctx, a, r, "current"); err == nil {
			keep := map[string]bool{}
			for _, x := range v.Doc.Rules {
				keep[x.Text] = true
			}
			for _, x := range old.Doc.Rules {
				if !keep[x.Text] {
					removed = append(removed, x.Text)
				}
			}
		}
	}
	if err := makeCurrent(ctx, a, r, v); err != nil {
		return nil, err
	}
	a.Audit(ctx, "recipe.publish", "recipe", r.ID, r.ProjectID, map[string]any{"version": v.Number, "removedRules": len(removed)})
	out := &RecipePublishOut{Recipe: recipeRow(ctx, a, r, v), Version: versionRow(v, nil), RemovedRules: removed,
		Message: fmt.Sprintf("Version %d of “%s” is current: runs make it from now on.", v.Number, r.Title)}
	if len(removed) > 0 {
		out.Message += " It no longer keeps " + plural(len(removed), "rule") + ": " + joinAnd(quoteAll(removed)) + "."
	}
	return out, nil
}

func archiveRecipe(ctx context.Context, a *app.App, in *RecipeArchiveIn) (*RecipeArchiveOut, error) {
	r, err := loadRecipe(ctx, a, in.Recipe)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	update := bson.M{"$set": bson.M{"archived": true, "archivedAt": now, "archivedBy": a.Actor, "updatedAt": now}}
	msg := fmt.Sprintf("“%s” is archived: it left the Recipes tab and can't run; its versions and runs stay.", r.Title)
	if in.Restore {
		update = bson.M{"$set": bson.M{"archived": false, "updatedAt": now}, "$unset": bson.M{"archivedAt": "", "archivedBy": ""}}
		msg = fmt.Sprintf("“%s” is back on the Recipes tab.", r.Title)
	}
	if _, err := a.DB.C(store.CRecipes).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()}, update); err != nil {
		return nil, err
	}
	a.Audit(ctx, "recipe.archive", "recipe", r.ID, r.ProjectID, map[string]any{"restore": in.Restore})
	r.Archived = !in.Restore
	v, _ := loadVersion(ctx, a, r, "")
	return &RecipeArchiveOut{Recipe: recipeRow(ctx, a, r, v), Message: msg}, nil
}

func planRecipe(ctx context.Context, a *app.App, in *RecipePlanIn) (*RecipePlanOut, error) {
	p, err := makePlan(ctx, a, planIn{Recipe: in.Recipe, Version: in.Version, Inputs: in.Inputs, Repeats: in.Repeats, Note: in.Note})
	if err != nil {
		return nil, err
	}
	return planOut(ctx, a, p), nil
}

// planOut is a plan as people and the dashboard read it.
func planOut(ctx context.Context, a *app.App, p *planned) *RecipePlanOut {
	out := &RecipePlanOut{Ready: len(p.Problems) == 0, Problems: nonNil(p.Problems), Warnings: nonNil(p.Warnings), Recipe: p.Recipe.Name, Title: p.Doc.Title,
		Version: p.Version.Number, Branch: p.Branch, Inputs: p.Inputs, Steps: p.Steps, Estimate: p.Estimate, Budget: nonNil(p.Budget),
		Changes: nonNil(p.Changes), Reused: nonNil(p.Reused), Claude: len(p.Assembly.StandIns) > 0 || len(p.Outputs) > 0, Settings: p.Settings, AskForEstimate: p.Ask, Fingerprint: p.Fingerprint,
		Pack: p.Outputs, Sample: p.sample}
	switch {
	case p.Prev != nil && p.Project != nil:
		out.ProjectID, out.Project = p.Project.ID, p.Project.Name
		out.Lands = fmt.Sprintf("a new version of the cut %s made, in “%s”", runName(p.Prev), p.Project.Name)
		b := runBrief(ctx, a, p.Prev)
		out.Repeats = &b
	case p.Project != nil:
		out.ProjectID, out.Project = p.Project.ID, p.Project.Name
		out.Lands = fmt.Sprintf("a new cut in “%s”", p.Project.Name)
	default:
		out.NewProject, out.Project = true, nameWithInputs(p.ProjectName, p.Values)
		out.Lands = fmt.Sprintf("a new project, “%s”", out.Project)
	}
	if out.Inputs == nil {
		out.Inputs = []models.RecipeRunInput{}
	}
	what := "Run " + fmt.Sprint(p.Number) + " of “" + p.Doc.Title + "” makes " + out.Lands
	if p.Prev != nil {
		what = "Making " + runName(p.Prev) + " again makes " + out.Lands
	}
	out.Summary = what + "; it costs " + p.Estimate.About + "."
	if !p.Ask && p.Estimate.USD > 0 {
		out.Summary += " Estimates are off for you (Settings → Preferences), so it starts without asking about the price; budgets and limits still hold."
	}
	if n := len(p.Outputs); n > 0 {
		out.Summary += fmt.Sprintf(" It also makes %s, each its own project.", plural(n, "short"))
	}
	if out.Claude {
		out.Summary += " It waits for Claude to choose the moments (or keep or change MetaRush's first picks)."
	}
	if p.sample != nil {
		out.Summary += " " + p.sample.Summary
	}
	switch {
	case !out.Ready:
		out.Next = "Fix the problems, then plan it again."
	case p.sample != nil && p.Ask:
		out.Next = "If the person agrees to " + p.sample.Estimate.About + " for the sample, they start it; or they skip the sample with a reason and agree to " + p.Estimate.About + "."
	case p.Ask && p.Estimate.USD > 0:
		out.Next = "If the person agrees to " + p.Estimate.About + ", they start it."
	default:
		out.Next = "The person starts it when they are ready."
	}
	return out
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func runRecipe(ctx context.Context, a *app.App, in *RecipeRunIn) (*RecipeRunOut, error) {
	if in.Repeats != "" {
		return nil, apperr.New(apperr.InvalidInput, "To make a run again, use recipe again.").WithCommand("metarush recipe again " + in.Repeats)
	}
	run, err := startRun(ctx, a, planIn{Recipe: in.Recipe, Version: in.Version, Inputs: in.Inputs, Note: in.Note}, in.Fingerprint, in.MaxUSD, in.SkipSample)
	if err != nil {
		return nil, err
	}
	v := runView(ctx, a, run)
	return &RecipeRunOut{Run: *v, Message: fmt.Sprintf("Started %s: %s.", runName(run), strings.ToLower(v.StateLabel[:1])+v.StateLabel[1:])}, nil
}

func againRecipe(ctx context.Context, a *app.App, in *RecipeAgainIn) (*RecipeRunOut, error) {
	run, err := startRun(ctx, a, planIn{Repeats: in.Run, Inputs: in.Inputs, Note: in.Note}, in.Fingerprint, in.MaxUSD, in.SkipSample)
	if err != nil {
		return nil, err
	}
	v := runView(ctx, a, run)
	return &RecipeRunOut{Run: *v, Message: fmt.Sprintf("Started %s, making the same cut again: %s.", runName(run), strings.ToLower(v.StateLabel[:1])+v.StateLabel[1:])}, nil
}

func listRuns(ctx context.Context, a *app.App, in *RecipeRunsIn) (*RecipeRunsOut, error) {
	out := &RecipeRunsOut{Runs: []RecipeRunBrief{}}
	if in.Run != "" {
		run, err := loadRun(ctx, a, in.Run)
		if err != nil {
			return nil, err
		}
		out.Run = runView(ctx, a, run)
		out.Runs = append(out.Runs, runBrief(ctx, a, run))
		out.Summary = upperFirst(runName(run)) + ": " + out.Run.StateLabel + "."
		return out, nil
	}
	filter := bson.M{}
	if in.Recipe != "" {
		r, err := loadRecipe(ctx, a, in.Recipe)
		if err != nil {
			return nil, err
		}
		filter["recipeId"] = r.ID
	}
	if in.ProjectID != "" {
		p, err := ops.ResolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		// Its video is there, or one of its shorts is (D214).
		filter["$or"] = bson.A{bson.M{"projectId": p.ID}, bson.M{"outputs.projectId": p.ID}}
	}
	limit := int64(in.Limit)
	if limit <= 0 {
		limit = 50
	}
	runs, err := runsOf(ctx, a, filter, limit)
	if err != nil {
		return nil, err
	}
	going := 0
	for i := range runs {
		out.Runs = append(out.Runs, runBrief(ctx, a, &runs[i]))
		if runs[i].State == models.RunRunning || runs[i].State == models.RunWaiting {
			going++
		}
	}
	switch {
	case len(runs) == 0:
		out.Summary = "No runs yet."
	case going > 0:
		out.Summary = fmt.Sprintf("%s, %d still going.", plural(len(runs), "run"), going)
	default:
		out.Summary = plural(len(runs), "run") + "."
	}
	return out, nil
}
