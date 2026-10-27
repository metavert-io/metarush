package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// Recipes in the Claude panel (D211, docs/recipes.md): Claude's refused
// `metarush recipe run|again|save|update|publish` — and keeping or changing a
// run's first picks (`recipe picks`, D215), saving a recipe sheet as a draft
// (`recipe check <file> --save`, D213) and saving the changes suggested from
// runs as a draft (`recipe suggestions --save`, D217) — become cards built here
// (never from the browser): a run's plan — where the video goes, its inputs,
// each step and who does it — with its one estimate (D208), opening to the
// services, models and sources; what saving a cut would vary each run and keep;
// what a draft or a version made current changes, in plain words (D212). Every
// one waits for a person: Auto never starts a run (D204) and never runs these
// cards; a run, a run again and making a version current are people-only
// decisions, each decided on its own card (never with Approve all); a save or
// a change is a draft a person makes current. Not quite… sends a card back to
// Claude like any other. A run started from a card is the card's work: Claude
// hears once it waits for Claude (to choose the moments), waits for a person,
// or is ready for review.
//
// The panel reads the Recipes operations by name through the ops registry
// (`metarush recipe …`'s own), never by importing internal/recipes: that
// package's tests read the activity bar, which reads conversations.

// recipeCommands are the commands handled here.
var recipeCommands = map[string]bool{"recipe run": true, "recipe again": true, "recipe save": true, "recipe update": true, "recipe publish": true,
	"recipe archive": true, "recipe continue": true, "recipe stop": true,
	"recipe picks": true, "recipe check": true, "recipe suggestions": true, "recipe triggers": true}

// recipeNever are what a person does on the recipe's or the run's page.
var recipeNever = map[string]string{
	"recipe archive":  "Only a person archives or restores a recipe, on its page.",
	"recipe continue": "Only a person lets a waiting run go on, on its page or in To-dos.",
	"recipe stop":     "A person stops a run on its page.",
	"recipe triggers": "Only a person turns planning by itself on or off (or sets its plan aside), on the recipe's page.",
}

// recipeOps are these cards' operations; recipeAloneOps are decided on their
// own card only (a run's estimate, a version made current).
var (
	recipeOps = map[string]bool{"recipe.run": true, "recipe.again": true, "recipe.save": true, "recipe.update": true, "recipe.publish": true,
		"recipe.picks": true, "recipe.write": true, "recipe.suggestions.save": true}
	recipeAloneOps = map[string]bool{"recipe.run": true, "recipe.again": true, "recipe.publish": true}
)

// Kinds of card (RecipeCardView.Kind).
const (
	recipeCardRun     = "run"
	recipeCardAgain   = "again"
	recipeCardSave    = "save"
	recipeCardUpdate  = "update"
	recipeCardPublish = "publish"
	// A run's first picks kept or changed (D215), a recipe sheet saved as a
	// draft (D213), the changes suggested from runs saved as a draft (D217).
	recipeCardPicks     = "picks"
	recipeCardSheet     = "sheet"
	recipeCardSuggested = "suggested"
)

// maxRecipePatch bounds a change Claude wrote to the proposals folder.
const maxRecipePatch = 1 << 20

// RecipeCardView is what a card about a recipe shows (D211).
type RecipeCardView struct {
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=run,enum=again,enum=save,enum=update,enum=publish,enum=picks,enum=sheet,enum=suggested"`
	// Recipe is its short name ("" for a save not made yet); Title its title.
	Recipe string `bson:"recipe,omitempty" json:"recipe,omitempty"`
	Title  string `bson:"title" json:"title"`
	// Link opens it on the Library's Recipes tab (a run again: the run).
	Link string `bson:"link,omitempty" json:"link,omitempty"`
	// Lands says where the video goes; Makes what it makes.
	Lands string `bson:"lands,omitempty" json:"lands,omitempty"`
	Makes string `bson:"makes,omitempty" json:"makes,omitempty"`
	// A run's plan: its inputs, each step and who does it, what changes and
	// what is used again (a run again), whether it waits for Claude, what the
	// budgets leave.
	Inputs  []RecipeCardInput   `bson:"inputs,omitempty" json:"inputs,omitempty"`
	Steps   []models.RecipeStep `bson:"steps,omitempty" json:"steps,omitempty"`
	Changes []string            `bson:"changes,omitempty" json:"changes,omitempty"`
	Reused  []string            `bson:"reused,omitempty" json:"reused,omitempty"`
	Claude  bool                `bson:"claude,omitempty" json:"claude,omitempty" jsonschema:"description=The run waits for Claude to choose the moments."`
	Budget  []string            `bson:"budget,omitempty" json:"budget,omitempty"`
	// A save: what would change each run and what would stay, each with why.
	Varies []string `bson:"varies,omitempty" json:"varies,omitempty"`
	Stays  []string `bson:"stays,omitempty" json:"stays,omitempty"`
	// A draft (update) or a version made current (publish): what it changes,
	// in plain words, from Version (update: the version it starts from) or
	// from the current version, Against (publish); the rules it no longer keeps.
	Differences  []RecipeCardChange `bson:"differences,omitempty" json:"differences,omitempty"`
	Version      int                `bson:"version,omitempty" json:"version,omitempty"`
	Against      int                `bson:"against,omitempty" json:"against,omitempty"`
	RemovedRules []string           `bson:"removedRules,omitempty" json:"removedRules,omitempty"`
	// Note is Claude's note for a draft, or what a run is for (Claude's words).
	Note string `bson:"note,omitempty" json:"note,omitempty"`
	// Problems keep it from starting or being made current; Warnings are worth a look.
	Problems []string `bson:"problems,omitempty" json:"problems,omitempty"`
	Warnings []string `bson:"warnings,omitempty" json:"warnings,omitempty"`
	// Replanned: the plan changed since Claude asked (another run started, an
	// input or a price changed); the card shows the new one and nothing started.
	Replanned bool `bson:"replanned,omitempty" json:"replanned,omitempty"`
	// A run's picks (picks): the run, what the card keeps or changes, and its
	// picks as they stand.
	Run    string           `bson:"run,omitempty" json:"run,omitempty" jsonschema:"description=The run\\, as people name it (run 4 of “App teaser”)."`
	Change string           `bson:"change,omitempty" json:"change,omitempty" jsonschema:"description=What the card keeps or changes\\, in plain words."`
	Picks  []RecipeCardPick `bson:"picks,omitempty" json:"picks,omitempty"`
	// Suggested changes (suggested): why each, in plain words.
	Reasons []string `bson:"reasons,omitempty" json:"reasons,omitempty"`
	// New: a sheet that makes a new recipe (sheet).
	New bool `bson:"new,omitempty" json:"new,omitempty"`
}

// RecipeCardPick is one of a run's first picks as a picks card shows it.
type RecipeCardPick struct {
	Part  string `bson:"part" json:"part"`
	Kind  string `bson:"kind" json:"kind" jsonschema:"enum=moment,enum=short"`
	N     int    `bson:"n" json:"n"`
	When  string `bson:"when" json:"when"`
	Of    string `bson:"of,omitempty" json:"of,omitempty" jsonschema:"description=The footage or conversation it is from."`
	Why   string `bson:"why,omitempty" json:"why,omitempty"`
	Words string `bson:"words,omitempty" json:"words,omitempty"`
	State string `bson:"state" json:"state" jsonschema:"enum=first,enum=standin,enum=kept,enum=changed,enum=dropped"`
	// Changes: this card keeps or changes it.
	Changes bool `bson:"changes,omitempty" json:"changes,omitempty"`
}

// RecipeCardInput is one input of a run as its card shows it.
type RecipeCardInput struct {
	Name  string `bson:"name" json:"name"`
	Label string `bson:"label" json:"label"`
	Words string `bson:"words" json:"words"`
	// Given: this run gives it (a run again: it differs from the run made again).
	Given bool `bson:"given,omitempty" json:"given,omitempty"`
}

// RecipeCardChange is one part of a recipe and what changed in it (`recipe
// diff`'s parts, D212).
type RecipeCardChange struct {
	Part    string   `bson:"part" json:"part"`
	Label   string   `bson:"label" json:"label"`
	Changes []string `bson:"changes" json:"changes"`
}

// What the panel sends the Recipes operations and reads back (by name: see above).
type (
	recipeInputValue struct {
		Name  string   `json:"name"`
		Text  string   `json:"text,omitempty"`
		Items []string `json:"items,omitempty"`
	}
	recipePlanIn struct {
		Recipe  string             `json:"recipe,omitempty"`
		Inputs  []recipeInputValue `json:"inputs,omitempty"`
		Repeats string             `json:"repeats,omitempty"`
		Note    string             `json:"note,omitempty"`
	}
	recipeRunIn struct {
		recipePlanIn
		Fingerprint string   `json:"fingerprint"`
		MaxUSD      *float64 `json:"maxUsd,omitempty"`
	}
	recipeAgainIn struct {
		Run         string             `json:"run"`
		Inputs      []recipeInputValue `json:"inputs,omitempty"`
		Note        string             `json:"note,omitempty"`
		Fingerprint string             `json:"fingerprint"`
		MaxUSD      *float64           `json:"maxUsd,omitempty"`
	}
	recipePlanSeen struct {
		Ready    bool     `json:"ready"`
		Problems []string `json:"problems"`
		Warnings []string `json:"warnings"`
		Recipe   string   `json:"recipe"`
		Title    string   `json:"title"`
		Version  int      `json:"version"`
		Lands    string   `json:"lands"`
		Repeats  *struct {
			ID     string `json:"id"`
			Number int    `json:"number"`
		} `json:"repeats"`
		Inputs      []models.RecipeRunInput `json:"inputs"`
		Steps       []models.RecipeStep     `json:"steps"`
		Estimate    costs.Estimate          `json:"estimate"`
		Budget      []string                `json:"budget"`
		Changes     []string                `json:"changes"`
		Reused      []string                `json:"reused"`
		Claude      bool                    `json:"claude"`
		Fingerprint string                  `json:"fingerprint"`
		// Sample is the sample the run makes first (D294): what is agreed when
		// the card is decided is its price; the rest after a person approves it.
		Sample *struct {
			Estimate costs.Estimate `json:"estimate"`
			Summary  string         `json:"summary"`
		} `json:"sample"`
	}
	recipeRefIn struct {
		Recipe  string `json:"recipe"`
		Version string `json:"version,omitempty"`
	}
	recipeVersionSeen struct {
		Number int    `json:"number"`
		State  string `json:"state"`
	}
	recipeSeen struct {
		Name     string `json:"name"`
		Title    string `json:"title"`
		Makes    string `json:"makes"`
		Current  int    `json:"current"`
		Archived bool   `json:"archived"`
	}
	recipeShowSeen struct {
		Recipe  recipeSeen        `json:"recipe"`
		Version recipeVersionSeen `json:"version"`
		Lands   string            `json:"lands"`
		Inputs  []struct {
			Name  string `json:"name"`
			Label string `json:"label"`
		} `json:"inputs"`
	}
	recipeSaveIn struct {
		From        string   `json:"from"`
		Branch      string   `json:"branch,omitempty"`
		Version     string   `json:"version,omitempty"`
		Name        string   `json:"name,omitempty"`
		Title       string   `json:"title,omitempty"`
		NewProjects bool     `json:"newProjects,omitempty"`
		Vary        []string `json:"vary,omitempty"`
		Keep        []string `json:"keep,omitempty"`
		Fixed       []string `json:"fixed,omitempty"`
		Chosen      []string `json:"chosen,omitempty"`
		Hints       []string `json:"hints,omitempty"`
		Note        string   `json:"note,omitempty"`
		Draft       bool     `json:"draft,omitempty"`
	}
	recipeCheckSeen struct {
		Problems []struct {
			Message string `json:"message"`
		} `json:"problems"`
	}
	recipeSaveSeen struct {
		Title    string `json:"title"`
		Makes    string `json:"makes"`
		Lands    string `json:"lands"`
		Proposal []struct {
			What   string `json:"what"`
			Why    string `json:"why"`
			Varies bool   `json:"varies"`
		} `json:"proposal"`
		Check   *recipeCheckSeen   `json:"check"`
		Recipe  *recipeSeen        `json:"recipe"`
		Version *recipeVersionSeen `json:"version"`
		Message string             `json:"message"`
	}
	recipeUpdateIn struct {
		Recipe   string          `json:"recipe"`
		Version  string          `json:"version,omitempty"`
		Patch    json.RawMessage `json:"patch,omitempty"`
		Title    string          `json:"title,omitempty"`
		AddHints []string        `json:"addHints,omitempty"`
		AddRules []string        `json:"addRules,omitempty"`
		// FromDrive: inputs that take their files from a Google Drive folder (D220).
		FromDrive map[string]string `json:"fromDrive,omitempty"`
		// FromDropbox: the same with Dropbox folders (D306).
		FromDropbox map[string]string `json:"fromDropbox,omitempty"`
		Note        string            `json:"note,omitempty"`
		DryRun      bool              `json:"dryRun,omitempty"`
	}
	recipeUpdateSeen struct {
		Recipe       string             `json:"recipe"`
		Title        string             `json:"title"`
		Version      recipeVersionSeen  `json:"version"`
		Check        *recipeCheckSeen   `json:"check"`
		Same         bool               `json:"same"`
		Changes      []RecipeCardChange `json:"changes"`
		RemovedRules []string           `json:"removedRules"`
		Message      string             `json:"message"`
	}
	recipeDiffSeen struct {
		Parts        []RecipeCardChange `json:"parts"`
		RemovedRules []string           `json:"removedRules"`
	}
	recipeCheckOutSeen struct {
		Check recipeCheckSeen `json:"check"`
	}
	recipePublishSeen struct {
		Recipe  recipeSeen        `json:"recipe"`
		Version recipeVersionSeen `json:"version"`
		Message string            `json:"message"`
	}
	recipeRunSeen struct {
		ID             string `json:"id"`
		Ref            string `json:"ref"`
		Number         int    `json:"number"`
		RecipeTitle    string `json:"recipeTitle"`
		State          string `json:"state"`
		StateLabel     string `json:"stateLabel"`
		WaitingFor     string `json:"waitingFor"`
		Waiting        string `json:"waiting"`
		Claude         string `json:"claude"`
		Problem        string `json:"problem"`
		Branch         string `json:"branch"`
		Revision       string `json:"revision"`
		RevisionNumber int    `json:"revisionNumber"`
		Render         string `json:"render"`
		QA             string `json:"qa"`
		SpentWords     string `json:"spentWords"`
	}
	recipeRunOutSeen struct {
		Run     recipeRunSeen `json:"run"`
		Message string        `json:"message"`
	}
	// A run's picks (recipe picks, D215).
	recipePickChange struct {
		Part    string   `json:"part"`
		N       int      `json:"n"`
		From    *float64 `json:"from,omitempty"`
		To      *float64 `json:"to,omitempty"`
		Footage string   `json:"footage,omitempty"`
		Why     string   `json:"why,omitempty"`
		Drop    bool     `json:"drop,omitempty"`
	}
	recipePicksIn struct {
		Run  string             `json:"run"`
		Keep bool               `json:"keep,omitempty"`
		Set  []recipePickChange `json:"set,omitempty"`
	}
	recipePickSeen struct {
		Part         string   `json:"part"`
		Kind         string   `json:"kind"`
		N            int      `json:"n"`
		FootageTitle string   `json:"footageTitle"`
		When         string   `json:"when"`
		Why          []string `json:"why"`
		Words        string   `json:"words"`
		State        string   `json:"state"`
	}
	recipeRunPicksSeen struct {
		Run *struct {
			recipeRunSeen
			RecipeName string           `json:"recipeName"`
			Picks      []recipePickSeen `json:"picks"`
		} `json:"run"`
	}
	// A recipe sheet saved as a draft (recipe check --save, D213).
	recipeSheetCheckSeen struct {
		Valid    bool `json:"valid"`
		Problems []struct {
			Severity string `json:"severity"`
			Line     int    `json:"line"`
			Message  string `json:"message"`
		} `json:"problems"`
		Name         string             `json:"name"`
		Title        string             `json:"title"`
		Base         *recipeVersionSeen `json:"base"`
		New          bool               `json:"new"`
		Same         bool               `json:"same"`
		Parts        []RecipeCardChange `json:"parts"`
		RemovedRules []string           `json:"removedRules"`
		Check        recipeCheckSeen    `json:"check"`
	}
	recipeWriteIn struct {
		Sheet string `json:"sheet"`
		Note  string `json:"note,omitempty"`
	}
	recipeWriteSeen struct {
		Recipe  recipeSeen        `json:"recipe"`
		Version recipeVersionSeen `json:"version"`
		New     bool              `json:"new"`
		Message string            `json:"message"`
	}
	// The changes suggested from runs, saved as a draft (recipe suggestions --save, D217).
	recipeSuggestionsIn struct {
		Recipe string   `json:"recipe"`
		Only   []string `json:"only,omitempty"`
		Note   string   `json:"note,omitempty"`
	}
	recipeSuggestionsSeen struct {
		Recipe      string `json:"recipe"`
		Title       string `json:"title"`
		Looked      int    `json:"looked"`
		Suggestions []struct {
			Reason  string `json:"reason"`
			InDraft bool   `json:"inDraft"`
		} `json:"suggestions"`
		Base    *recipeVersionSeen `json:"base"`
		Changes []RecipeCardChange `json:"changes"`
		Check   *recipeCheckSeen   `json:"check"`
	}
)

// recipeOp calls a Recipes operation by name and reads its answer into out.
func recipeOp[T any](ctx context.Context, m *Manager, name string, in any) (*T, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	res, err := ops.Call(ctx, m.App, name, raw, ops.Caller{})
	if err != nil {
		return nil, err
	}
	return asRecipe[T](res)
}

// asRecipe reads an operation's answer as this package sees it.
func asRecipe[T any](res any) (*T, error) {
	b, err := json.Marshal(res)
	if err != nil {
		return nil, err
	}
	var out T
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// recipeInputs reads what a run gives its inputs, written name=value
// (`--input headline="Read free"`; a name given again adds to its clips or
// pictures, commas separate several), the way `metarush recipe run` does
// (recipes.InputsFromFlags; a test keeps the two the same).
func recipeInputs(raw []string) ([]recipeInputValue, error) {
	var out []recipeInputValue
	at := map[string]int{}
	for _, kv := range raw {
		k, v, ok := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		if !ok || k == "" {
			return nil, fmt.Errorf("give each input as name=value, like headline=\"Read free\"")
		}
		i, seen := at[k]
		if !seen {
			at[k] = len(out)
			out = append(out, recipeInputValue{Name: k})
			i = len(out) - 1
		}
		out[i].Text = v
		for _, item := range strings.Split(v, ",") {
			if item = strings.TrimSpace(item); item != "" {
				out[i].Items = append(out[i].Items, item)
			}
		}
	}
	return out, nil
}

// recipeCard reports whether a card is one of these: it never starts by itself.
func recipeCard(a *AssistantAction) bool { return a.Recipe != nil || recipeOps[a.Op] }

// recipeAlone refuses deciding a run, a run again or making a version current
// together with other cards: each is agreed to on its own card.
func recipeAlone(cards []AssistantAction, in ConversationActIn) error {
	if len(cards) < 2 || in.Dismiss || in.NotQuite != "" {
		return nil
	}
	for _, a := range cards {
		if recipeAloneOps[a.Op] {
			return apperr.New(apperr.Conflict, "“%s” is decided on its own card, after you look at its plan.", strings.TrimSuffix(a.Title, ".")).
				WithDetails(map[string]any{"actionId": a.ID, "reason": "decide_alone"})
		}
	}
	return nil
}

func (m *Manager) describeRecipe(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	if why, never := recipeNever[call.Path]; never {
		incomplete(a, "", why)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 90*time.Second)
	defer cancel()
	switch call.Path {
	case "recipe run", "recipe again":
		m.describeRecipeRun(ctx, call, a)
	case "recipe save":
		m.describeRecipeSave(ctx, c, call, a)
	case "recipe update":
		m.describeRecipeUpdate(ctx, c, call, a)
	case "recipe publish":
		m.describeRecipePublish(ctx, call, a)
	case "recipe picks":
		m.describeRecipePicks(ctx, call, a)
	case "recipe check":
		if !hasBool(call, "save") {
			incomplete(a, "Save a recipe sheet", "Checking a sheet only reads; saving it as a draft is --save.")
			return
		}
		m.describeRecipeSheet(ctx, c, call, a)
	case "recipe suggestions":
		m.describeRecipeSuggestions(ctx, call, a)
	}
}

// describeRecipeRun is a run's card (or a run again's): the plan, its estimate
// and its fingerprint, so what starts is exactly what the person saw.
func (m *Manager) describeRecipeRun(ctx context.Context, call *cliCall, a *AssistantAction) {
	again := call.Path == "recipe again"
	ref := strings.TrimSpace(first(call.Args))
	what := "Run a recipe"
	if again {
		what = "Make a run again"
	}
	if ref == "" {
		incomplete(a, what, "Claude did not say which recipe (or, to make one again, which run).")
		return
	}
	inputs, err := recipeInputs(call.Multi["input"])
	if err != nil {
		incomplete(a, what, err.Error())
		return
	}
	plan := recipePlanIn{Recipe: ref, Inputs: inputs, Note: clip(strings.TrimSpace(call.Flags["note"]), 500)}
	if again {
		plan = recipePlanIn{Repeats: ref, Inputs: inputs, Note: plan.Note}
	}
	m.recipeRunCard(ctx, a, plan)
}

// recipeRunCard plans a run and fills its card (again, when the plan changed).
func (m *Manager) recipeRunCard(ctx context.Context, a *AssistantAction, plan recipePlanIn) {
	again := plan.Repeats != ""
	p, err := recipeOp[recipePlanSeen](ctx, m, "recipe.plan", plan)
	if err != nil {
		title := "Run a recipe"
		if again {
			title = "Make a run again"
		}
		unavailable(a, title, "MetaRush couldn't plan it: "+firstLine(apperr.From(err).Message))
		return
	}
	view := &RecipeCardView{Kind: recipeCardRun, Recipe: p.Recipe, Title: p.Title, Link: links.Recipe(p.Recipe), Lands: p.Lands, Steps: p.Steps,
		Claude: p.Claude, Budget: p.Budget, Problems: p.Problems, Warnings: p.Warnings, Note: plan.Note}
	title := "Run “" + p.Title + "”"
	if again && p.Repeats != nil {
		view.Kind, view.Link, view.Changes, view.Reused = recipeCardAgain, links.RecipeRun(p.Repeats.ID), p.Changes, p.Reused
		title = fmt.Sprintf("Make run %d of “%s” again", p.Repeats.Number, p.Title)
	}
	sample := p.Sample != nil // a run again samples what changed too (D294)
	switch {
	case sample:
		title += " (a sample first: " + p.Sample.Estimate.About + ")"
	case p.Estimate.USD > 0:
		title += " (" + p.Estimate.About + ")"
	}
	labels := m.recipeInputLabels(ctx, p.Recipe, p.Version)
	var given []string
	for _, in := range p.Inputs {
		ci := RecipeCardInput{Name: in.Name, Label: labels[in.Name], Words: in.Words, Given: !in.Default}
		if again {
			ci.Given = in.Changed
		}
		if ci.Label == "" {
			ci.Label = upperFirst(in.Name)
		}
		view.Inputs = append(view.Inputs, ci)
		if ci.Given {
			given = append(given, ci.Label+": "+in.Words)
		}
	}
	a.Recipe, a.HumanOnly = view, true
	a.Details = []string{upperFirst("makes "+p.Lands) + "."}
	if len(given) > 0 {
		lead := "This run: "
		if again {
			lead = "What changes: "
		}
		a.Details = append(a.Details, lead+clip(strings.Join(given, "; "), 400))
	}
	if p.Claude {
		a.Details = append(a.Details, "It waits for Claude to choose the moments.")
	}
	if sample {
		a.Details = append(a.Details, p.Sample.Summary)
	}
	a.Details = append(a.Details, "Only people start a run: Claude asked, the decision is yours.")
	est := p.Estimate
	if sample {
		est = p.Sample.Estimate // what starting agrees to: the sample (D294)
	}
	if est.USD > 0 || len(est.Parts) > 0 {
		a.Estimate = &est
	} else {
		a.Estimate = nil
	}
	if !p.Ready {
		why := "It can't start yet."
		if len(p.Problems) > 0 {
			why = "It can't start yet: " + strings.TrimSuffix(p.Problems[0], ".") + "."
		}
		unavailable(a, clip(title, 160), why)
		return
	}
	// One agreement covers the run (D204): the estimate the person sees — the
	// sample's when the run starts with one (D294); the rest is agreed after it.
	var maxUSD *float64
	if est.USD > 0 {
		v := est.USD
		maxUSD = &v
	}
	a.Title = clip(title, 160)
	if again && p.Repeats != nil {
		setInput(a, "recipe.again", recipeAgainIn{Run: p.Repeats.ID, Inputs: plan.Inputs, Note: plan.Note, Fingerprint: p.Fingerprint, MaxUSD: maxUSD})
		return
	}
	setInput(a, "recipe.run", recipeRunIn{recipePlanIn: recipePlanIn{Recipe: p.Recipe, Inputs: plan.Inputs, Note: plan.Note}, Fingerprint: p.Fingerprint, MaxUSD: maxUSD})
}

// recipeInputLabels are a recipe version's inputs as people name them.
func (m *Manager) recipeInputLabels(ctx context.Context, name string, version int) map[string]string {
	out := map[string]string{}
	show, err := recipeOp[recipeShowSeen](ctx, m, "recipe.show", recipeRefIn{Recipe: name, Version: strconv.Itoa(version)})
	if err != nil {
		return out
	}
	for _, in := range show.Inputs {
		out[in.Name] = in.Label
	}
	return out
}

// describeRecipeSave is saving a project's cut as a recipe: the proposal —
// what would change each run and what would stay — saved as a draft.
func (m *Manager) describeRecipeSave(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	if hasBool(call, "dry-run") {
		incomplete(a, "Save a recipe", "To see what saving would vary and keep, use recipe propose: it only reads.")
		return
	}
	in := recipeSaveIn{From: strings.TrimSpace(call.Flags["from"]), Branch: call.Flags["branch"], Version: call.Flags["version"], Name: clip(call.Flags["name"], 40),
		Title: clip(strings.TrimSpace(call.Flags["title"]), 120), NewProjects: hasBool(call, "new-projects"), Vary: listFlag(call, "vary"), Keep: listFlag(call, "keep"),
		Fixed: listFlag(call, "fixed"), Chosen: listFlag(call, "chosen"), Note: clip(strings.TrimSpace(call.Flags["note"]), 300), Draft: true}
	for _, h := range call.Multi["hint"] {
		if h = strings.TrimSpace(h); h != "" && len(in.Hints) < 20 {
			in.Hints = append(in.Hints, clip(h, 300))
		}
	}
	if in.From == "" && c != nil {
		in.From = c.projectID()
	}
	if in.From == "" {
		incomplete(a, "Save a recipe", "Claude did not say which project's cut to save.")
		return
	}
	prop, err := recipeOp[recipeSaveSeen](ctx, m, "recipe.propose", in)
	if err != nil {
		unavailable(a, "Save the cut as a recipe", "MetaRush couldn't read the cut: "+firstLine(apperr.From(err).Message))
		return
	}
	view := &RecipeCardView{Kind: recipeCardSave, Title: prop.Title, Makes: prop.Makes, Lands: prop.Lands, Note: in.Note}
	for _, it := range prop.Proposal {
		line := it.What
		if it.Why != "" {
			line += " — " + it.Why
		}
		if it.Varies {
			view.Varies = append(view.Varies, line)
		} else {
			view.Stays = append(view.Stays, line)
		}
	}
	view.Problems = checkProblems(prop.Check)
	a.Recipe = view
	a.Title = clip("Save “"+prop.Title+"” as a recipe", 160)
	a.Details = []string{upperFirst(fmt.Sprintf("makes %s; each run makes %s.", prop.Makes, prop.Lands)),
		fmt.Sprintf("Changes each run: %s; stays the same: %s.", pluralize(len(view.Varies), "thing"), pluralize(len(view.Stays), "thing")),
		"Saved as a draft: a person makes it current before it runs."}
	if n := len(view.Problems); n > 0 {
		a.Details = append(a.Details, "Its check finds "+pluralize(n, "problem")+" to fix before it can be made current.")
	}
	setInput(a, "recipe.save", in)
}

// checkProblems are a recipe check's problems, as people read them: what keeps
// a version from being made current. Its warnings are about the cut (how a clip
// is cropped) and are read on the recipe's page, not on a card.
func checkProblems(chk *recipeCheckSeen) []string {
	if chk == nil {
		return nil
	}
	var out []string
	for _, p := range chk.Problems {
		out = append(out, p.Message)
	}
	return out
}

// describeRecipeUpdate is a draft of a recipe with Claude's change: what it
// changes from the version it starts from, in plain words.
func (m *Manager) describeRecipeUpdate(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	ref := strings.TrimSpace(first(call.Args))
	if ref == "" {
		incomplete(a, "Change a recipe", "Claude did not say which recipe.")
		return
	}
	in := recipeUpdateIn{Recipe: ref, Version: strings.TrimSpace(call.Flags["version"]), Title: clip(strings.TrimSpace(call.Flags["title"]), 120),
		Note: clip(strings.TrimSpace(call.Flags["note"]), 300)}
	for _, h := range call.Multi["hint"] {
		if h = strings.TrimSpace(h); h != "" && len(in.AddHints) < 20 {
			in.AddHints = append(in.AddHints, clip(h, 300))
		}
	}
	for _, r := range call.Multi["rule"] {
		if r = strings.TrimSpace(r); r != "" && len(in.AddRules) < 20 {
			in.AddRules = append(in.AddRules, clip(r, 300))
		}
	}
	// An input that takes its files from a Google Drive folder (D220):
	// input=remote:path, empty to take it away; the folder is only read when a
	// run is planned, never for the card.
	for _, kv := range call.Multi["from-drive"] {
		k, v, ok := strings.Cut(kv, "=")
		if k = strings.TrimSpace(k); !ok || k == "" || len(in.FromDrive) >= 20 {
			incomplete(a, "Change a recipe", "Give --from-drive as input=remote:path, like art=gdrive:Teaser/Art.")
			return
		}
		if in.FromDrive == nil {
			in.FromDrive = map[string]string{}
		}
		in.FromDrive[clip(k, 64)] = clip(strings.TrimSpace(v), 500)
	}
	for _, kv := range call.Multi["from-dropbox"] {
		k, v, ok := strings.Cut(kv, "=")
		if k = strings.TrimSpace(k); !ok || k == "" || len(in.FromDropbox) >= 20 {
			incomplete(a, "Change a recipe", "Give --from-dropbox as input=remote:path, like art=dropbox:Teaser/Art.")
			return
		}
		if in.FromDropbox == nil {
			in.FromDropbox = map[string]string{}
		}
		in.FromDropbox[clip(k, 64)] = clip(strings.TrimSpace(v), 500)
	}
	if p := strings.TrimSpace(call.Flags["patch"]); p != "" {
		raw, why := m.recipePatch(c, p)
		if why != "" {
			incomplete(a, "Change a recipe", why)
			return
		}
		in.Patch = raw
	}
	if len(in.Patch) == 0 && in.Title == "" && len(in.AddHints) == 0 && len(in.AddRules) == 0 && len(in.FromDrive) == 0 && len(in.FromDropbox) == 0 {
		incomplete(a, "Change a recipe", "Claude did not say what to change.")
		return
	}
	dry := in
	dry.DryRun = true
	prev, err := recipeOp[recipeUpdateSeen](ctx, m, "recipe.update", dry)
	if err != nil {
		unavailable(a, "Change a recipe", "MetaRush couldn't make the draft: "+firstLine(apperr.From(err).Message))
		return
	}
	title := clip(fmt.Sprintf("Make a draft of “%s” with this change", prev.Title), 160)
	if prev.Same {
		unavailable(a, title, fmt.Sprintf("Nothing would change: it is the same as version %d.", prev.Version.Number))
		return
	}
	view := &RecipeCardView{Kind: recipeCardUpdate, Recipe: prev.Recipe, Title: prev.Title, Link: links.Recipe(prev.Recipe), Differences: prev.Changes,
		Version: prev.Version.Number, RemovedRules: prev.RemovedRules, Note: in.Note}
	view.Problems = checkProblems(prev.Check)
	a.Recipe, a.Title = view, title
	a.Details = []string{changeCount(prev.Changes, prev.Version.Number)}
	if in.Note != "" {
		a.Details = append(a.Details, "Claude's note: "+theirs(in.Note))
	}
	if len(prev.RemovedRules) > 0 {
		a.Details = append(a.Details, "It no longer keeps "+theirsList(prev.RemovedRules)+".")
	}
	a.Details = append(a.Details, "A draft: runs keep making the current version until a person makes it current.")
	if n := len(view.Problems); n > 0 {
		a.Details = append(a.Details, "Its check finds "+pluralize(n, "problem")+" to fix before it can be made current.")
	}
	setInput(a, "recipe.update", in)
}

// changeCount says how much a version changes from another.
func changeCount(parts []RecipeCardChange, from int) string {
	n := 0
	var where []string
	for _, p := range parts {
		n += len(p.Changes)
		where = append(where, lowerFirst(p.Label))
	}
	if n == 0 {
		return fmt.Sprintf("The same video as version %d.", from)
	}
	return fmt.Sprintf("From version %d, it changes %s: %s.", from, pluralize(n, "thing"), strings.Join(where, ", "))
}

// recipePatch reads a change Claude wrote as a merge patch: a JSON file in
// this conversation's proposals folder, and nowhere else. why says why it
// can't be used.
func (m *Manager) recipePatch(c *conv, from string) (json.RawMessage, string) {
	if c == nil || from == "-" {
		return nil, "Claude's change is not in this conversation's proposals folder."
	}
	p := from
	if !filepath.IsAbs(p) {
		p = filepath.Join(m.workspaceDir(), p)
	}
	real := func(s string) string {
		if rs, err := filepath.EvalSymlinks(s); err == nil {
			return rs
		}
		return s
	}
	root, file := real(filepath.Join(m.convDir(c.id), "proposals")), real(p)
	rel, err := filepath.Rel(root, file)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return nil, "Claude's change is not in this conversation's proposals folder."
	}
	st, err := os.Stat(file)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxRecipePatch {
		return nil, "MetaRush can't read Claude's change (a file of at most 1 MB in the proposals folder)."
	}
	b, err := os.ReadFile(file)
	if err != nil || !json.Valid(b) {
		return nil, "Claude's change isn't a merge patch MetaRush can read."
	}
	return json.RawMessage(b), ""
}

// describeRecipePublish is making a version current (people only): what it
// changes from the version runs make now and the rules it no longer keeps.
func (m *Manager) describeRecipePublish(ctx context.Context, call *cliCall, a *AssistantAction) {
	ref := strings.TrimSpace(first(call.Args))
	if ref == "" {
		incomplete(a, "Make a version current", "Claude did not say which recipe.")
		return
	}
	version := strings.TrimSpace(call.Flags["version"])
	if version == "" {
		version = "draft"
	}
	show, err := recipeOp[recipeShowSeen](ctx, m, "recipe.show", recipeRefIn{Recipe: ref, Version: version})
	if err != nil {
		unavailable(a, "Make a version current", "MetaRush couldn't find it: "+firstLine(apperr.From(err).Message))
		return
	}
	r, v := show.Recipe, show.Version
	title := clip(fmt.Sprintf("Make version %d of “%s” current", v.Number, r.Title), 160)
	view := &RecipeCardView{Kind: recipeCardPublish, Recipe: r.Name, Title: r.Title, Link: links.Recipe(r.Name) + "&version=" + strconv.Itoa(v.Number),
		Makes: r.Makes, Lands: show.Lands, Version: v.Number, Against: r.Current}
	a.Recipe, a.HumanOnly = view, true
	switch {
	case v.State == models.RecipeCurrent:
		unavailable(a, title, fmt.Sprintf("Version %d is the current one already.", v.Number))
		return
	case r.Archived:
		unavailable(a, title, "“"+r.Title+"” is archived; a person restores it first.")
		return
	}
	if r.Current > 0 {
		if d, err := recipeOp[recipeDiffSeen](ctx, m, "recipe.diff", map[string]string{"recipe": r.Name, "to": strconv.Itoa(v.Number), "from": "current"}); err == nil {
			view.Differences, view.RemovedRules = d.Parts, d.RemovedRules
		}
	}
	if chk, err := recipeOp[recipeCheckOutSeen](ctx, m, "recipe.check", recipeRefIn{Recipe: r.Name, Version: strconv.Itoa(v.Number)}); err == nil {
		view.Problems = checkProblems(&chk.Check)
	}
	a.Details = []string{"Runs make it from now on; runs already made keep the version they made."}
	if r.Current > 0 {
		a.Details = append(a.Details, changeCount(view.Differences, r.Current))
	}
	if len(view.RemovedRules) > 0 {
		a.Details = append(a.Details, "It no longer keeps "+theirsList(view.RemovedRules)+".")
	}
	a.Details = append(a.Details, "Only people make a version current: Claude suggested it, the decision is yours.")
	if len(view.Problems) > 0 {
		unavailable(a, title, "It can't be made current yet: "+strings.TrimSuffix(view.Problems[0], ".")+".")
		return
	}
	a.Title = title
	setInput(a, "recipe.publish", recipeRefIn{Recipe: r.Name, Version: strconv.Itoa(v.Number)})
}

// recipeCaller is how a card's run starts: like a run started from the
// dashboard, its steps are the daemon's engine's (woken at once), never taken
// inside the person's decision, where they would race the engine for the
// run's first steps. Every other card runs with the caller as it is.
func recipeCaller(act AssistantAction, c ops.Caller) ops.Caller {
	if act.Op == "recipe.run" || act.Op == "recipe.again" {
		c.Remote = true
	}
	return c
}

// staleRecipe reports whether a run card's plan changed since Claude asked
// (recipe.run and recipe.again refuse a plan that isn't the one shown).
func staleRecipe(act AssistantAction, err error) bool {
	return err != nil && (act.Op == "recipe.run" || act.Op == "recipe.again") && apperr.From(err).Code == apperr.Conflict
}

// replanRecipe plans a run card again when its plan changed since Claude
// asked (another run started, an input or a price changed): nothing started,
// and the card shows the new plan for the person to look at.
func (m *Manager) replanRecipe(ctx context.Context, c *conv, act AssistantAction, err error) {
	if !staleRecipe(act, err) {
		return
	}
	var plan recipePlanIn
	if act.Op == "recipe.run" {
		var in recipeRunIn
		if json.Unmarshal([]byte(act.Input), &in) != nil {
			return
		}
		plan = in.recipePlanIn
	} else {
		var in recipeAgainIn
		if json.Unmarshal([]byte(act.Input), &in) != nil {
			return
		}
		plan = recipePlanIn{Repeats: in.Run, Inputs: in.Inputs, Note: in.Note}
	}
	fresh := act
	fresh.Details, fresh.Estimate = nil, nil
	m.recipeRunCard(ctx, &fresh, plan)
	if fresh.Recipe != nil {
		fresh.Recipe.Replanned = true
	}
	if fresh.State == ActionUnavailable {
		fresh.Op, fresh.Input = "", "" // it can't start any more: the card says why
	} else {
		fresh.State = ActionPending
	}
	c.putCard(fresh)
	m.save(c, true)
	m.changed(c.id)
}

// recipeResult says what a card's run, save, draft or version made current did.
func recipeResult(op string, res any) (result, link, forClaude string, ok bool) {
	switch op {
	case "recipe.run", "recipe.again":
		o, err := asRecipe[recipeRunOutSeen](res)
		if err != nil {
			return "", "", "", false
		}
		return plainSummary(o.Message), links.RecipeRun(o.Run.ID),
			fmt.Sprintf(" (run %s, %s; follow it with `metarush recipe runs --run %s --json`)", o.Run.Ref, o.Run.ID, o.Run.Ref), true
	case "recipe.save":
		o, err := asRecipe[recipeSaveSeen](res)
		if err != nil {
			return "", "", "", false
		}
		if o.Recipe == nil {
			return plainSummary(o.Message), "", "", true
		}
		forClaude = " (recipe " + o.Recipe.Name
		if o.Version != nil {
			forClaude += fmt.Sprintf(", version %d, %s", o.Version.Number, o.Version.State)
		}
		return plainSummary(o.Message), links.Recipe(o.Recipe.Name), forClaude + ")", true
	case "recipe.update", "recipe.suggestions.save":
		o, err := asRecipe[recipeUpdateSeen](res)
		if err != nil {
			return "", "", "", false
		}
		return plainSummary(o.Message), links.Recipe(o.Recipe) + "&version=" + strconv.Itoa(o.Version.Number),
			fmt.Sprintf(" (recipe %s, version %d, %s)", o.Recipe, o.Version.Number, o.Version.State), true
	case "recipe.publish":
		o, err := asRecipe[recipePublishSeen](res)
		if err != nil {
			return "", "", "", false
		}
		return plainSummary(o.Message), links.Recipe(o.Recipe.Name), fmt.Sprintf(" (recipe %s: version %d is current)", o.Recipe.Name, o.Version.Number), true
	case "recipe.picks":
		o, err := asRecipe[recipeRunOutSeen](res)
		if err != nil {
			return "", "", "", false
		}
		return plainSummary(o.Message), links.RecipeRun(o.Run.ID), fmt.Sprintf(" (run %s, %s: %s)", o.Run.Ref, o.Run.ID, o.Run.StateLabel), true
	case "recipe.write":
		o, err := asRecipe[recipeWriteSeen](res)
		if err != nil {
			return "", "", "", false
		}
		return plainSummary(o.Message), links.Recipe(o.Recipe.Name) + "&version=" + strconv.Itoa(o.Version.Number),
			fmt.Sprintf(" (recipe %s, version %d, %s)", o.Recipe.Name, o.Version.Number, o.Version.State), true
	}
	return "", "", "", false
}

// recipeWork is the run a card started (a run's answer: a run, by its ID):
// Claude hears once it waits for Claude or a person, or is ready for review.
func recipeWork(res any) *ActionWork {
	if res == nil {
		return nil
	}
	o, err := asRecipe[recipeRunOutSeen](res)
	if err != nil || !ids.Valid(ids.RecipeRun, o.Run.ID) {
		return nil
	}
	return &ActionWork{Kind: WorkRun, RunID: o.Run.ID, What: fmt.Sprintf("run %d of “%s”", o.Run.Number, o.Run.RecipeTitle), State: WorkRunning,
		StartedAt: time.Now().UTC(), Link: links.RecipeRun(o.Run.ID)}
}

// checkRunWork follows a run a card started.
func (m *Manager) checkRunWork(ctx context.Context, w ActionWork, end ender) (ActionWork, bool) {
	out, err := recipeOp[struct {
		Run *recipeRunSeen `json:"run"`
	}](ctx, m, "recipe.runs", map[string]string{"run": w.RunID})
	if err != nil {
		if apperr.From(err).Code == apperr.NotFound {
			return end(WorkGone, upperFirst(w.What)+" is gone", "The run "+w.RunID+" is gone.")
		}
		return w, false
	}
	v := out.Run
	if v == nil {
		return w, false
	}
	follow := fmt.Sprintf(" Read it with `metarush recipe runs --run %s --json`.", v.Ref)
	switch v.State {
	case models.RunWaiting:
		if v.WaitingFor == models.RunWaitsForClaude {
			task := v.Claude
			if task == "" {
				task = v.Waiting + "."
			}
			return end(WorkFinished, upperFirst(w.What)+" waits for Claude to choose the moments",
				fmt.Sprintf("Run %s (%s) waits for you: %s Choose them through the cut's sheet (cut show, cut check, cut apply); the run goes on by itself once the stand-ins are replaced.%s", v.Ref, v.ID, task, follow))
		}
		return end(WorkFinished, upperFirst(w.What)+": "+lowerFirst(v.StateLabel),
			fmt.Sprintf("Run %s (%s) waits for a person: %s Only they let it go on (its page or To-dos); say so plainly and don't start anything.%s", v.Ref, v.ID, v.Waiting, follow))
	case models.RunDone:
		shown := upperFirst(w.What) + " is ready for your review"
		if v.RevisionNumber > 0 {
			shown += fmt.Sprintf(": version %d", v.RevisionNumber)
		}
		if v.QA != "" {
			shown += "; the quality check " + v.QA
		}
		return end(WorkFinished, shown,
			fmt.Sprintf("Run %s (%s) is done: version %d (%s) of the cut %s, review copy %s; the quality check %s; it spent %s.%s", v.Ref, v.ID, v.RevisionNumber, v.Revision,
				v.Branch, orText(v.Render, "none"), orText(v.QA, "had nothing to say"), v.SpentWords, follow))
	case models.RunFailed, models.RunStopped:
		shown := upperFirst(w.What) + " didn't finish"
		if v.State == models.RunStopped {
			shown = upperFirst(w.What) + " was stopped"
		}
		if v.Problem != "" {
			shown += ": " + strings.TrimSuffix(v.Problem, ".")
		}
		return end(WorkFailed, shown, fmt.Sprintf("Run %s (%s) %s: %s%s", v.Ref, v.ID, v.State, v.Problem, follow))
	}
	return w, false
}

func orText(s, none string) string {
	if strings.TrimSpace(s) == "" {
		return none
	}
	return s
}

// recipeLabel says what Claude did with a recipe command, plainly.
func recipeLabel(c *cliCall) string {
	arg := first(c.Args)
	switch c.Path {
	case "recipe", "recipe list":
		return "Looked at the recipes"
	case "recipe show":
		return "Read the recipe " + quoted(clip(arg, 60))
	case "recipe propose":
		return "Looked at what a recipe from the cut would keep"
	case "recipe check":
		if hasBool(c, "save") {
			return "Asked to save a recipe sheet as a draft"
		}
		return "Checked a recipe"
	case "recipe picks":
		return "Asked to keep or change a run's first picks"
	case "recipe suggestions":
		if hasBool(c, "save") {
			return "Asked to save the changes suggested for a recipe as a draft"
		}
		return "Looked at the changes suggested for a recipe"
	case "recipe triggers":
		if _, ok := c.Flags["new-files"]; ok || c.Flags["weekly"] != "" || hasBool(c, "dismiss") {
			return "Asked for something only a person does with a recipe"
		}
		return "Looked at when recipes plan runs by themselves"
	case "recipe plan":
		if c.Flags["again"] != "" {
			return "Planned making a run again"
		}
		return "Planned a run of " + quoted(clip(arg, 60))
	case "recipe runs":
		if c.Flags["run"] != "" {
			return "Looked at a run"
		}
		return "Looked at the runs"
	case "recipe compare":
		return "Compared two runs"
	case "recipe diff":
		return "Compared two versions of a recipe"
	case "recipe run":
		return "Asked to start a run of " + quoted(clip(arg, 60))
	case "recipe again":
		return "Asked to make a run again"
	case "recipe save":
		return "Asked to save the cut as a recipe"
	case "recipe update":
		return "Asked to change a recipe"
	case "recipe publish":
		return "Asked to make a version of a recipe current"
	case "recipe archive", "recipe continue", "recipe stop":
		return "Asked for something only a person does with a recipe"
	case "recipe export": // D219: a person exports and brings in recipe files
		return "Asked to export a recipe to a file"
	case "recipe import":
		return "Asked to bring in a recipe file"
	}
	return ""
}

// recipeOutcome sums up what a recipe command answered: a plan's estimate.
func recipeOutcome(path string, d map[string]json.RawMessage) string {
	switch path {
	case "recipe plan":
		var est struct {
			About string `json:"about"`
		}
		var ready bool
		_ = json.Unmarshal(d["ready"], &ready)
		if json.Unmarshal(d["estimate"], &est) != nil || est.About == "" {
			return ""
		}
		if !ready {
			return "it can't start yet"
		}
		return est.About
	case "recipe list":
		var xs []json.RawMessage
		_ = json.Unmarshal(d["recipes"], &xs)
		return pluralize(len(xs), "recipe")
	case "recipe diff":
		var n int
		if json.Unmarshal(d["changes"], &n) == nil && n > 0 {
			return pluralize(n, "change")
		}
	case "recipe suggestions":
		var xs []json.RawMessage
		if json.Unmarshal(d["suggestions"], &xs) == nil {
			return pluralize(len(xs), "suggestion")
		}
	}
	return ""
}

// describeRecipePicks keeps or changes a run's first picks (D215): what the
// card keeps or changes, and the run's picks as they stand. The run goes on
// by itself once every pick is kept or changed; the card never runs by itself.
func (m *Manager) describeRecipePicks(ctx context.Context, call *cliCall, a *AssistantAction) {
	what := "Keep or change a run's first picks"
	ref := strings.TrimSpace(first(call.Args))
	if ref == "" {
		incomplete(a, what, "Claude did not say which run.")
		return
	}
	in := recipePicksIn{Run: ref, Keep: hasBool(call, "keep")}
	var ch *recipePickChange
	if part := strings.TrimSpace(call.Flags["part"]); part != "" {
		n, err := strconv.Atoi(strings.TrimSpace(call.Flags["n"]))
		if err != nil || n < 1 {
			incomplete(a, what, "Claude did not say which pick (its number).")
			return
		}
		ch = &recipePickChange{Part: clip(part, 40), N: n, Footage: clip(strings.TrimSpace(call.Flags["footage"]), 200), Why: clip(strings.TrimSpace(call.Flags["why"]), 300), Drop: hasBool(call, "drop")}
		if !ch.Drop {
			from, ok1 := editspec.ParseClock(call.Flags["from"])
			to, ok2 := editspec.ParseClock(call.Flags["to"])
			if !ok1 || !ok2 || to <= from {
				incomplete(a, what, "Claude did not give where the pick starts and ends (from and to).")
				return
			}
			ch.From, ch.To = &from, &to
		}
		in.Set = []recipePickChange{*ch}
	}
	if !in.Keep && ch == nil {
		incomplete(a, what, "Claude did not say which picks to keep or change.")
		return
	}
	seen, err := recipeOp[recipeRunPicksSeen](ctx, m, "recipe.runs", map[string]string{"run": ref})
	if err != nil || seen.Run == nil {
		why := "MetaRush couldn't find that run."
		if err != nil {
			why = "MetaRush couldn't find that run: " + firstLine(apperr.From(err).Message)
		}
		unavailable(a, what, why)
		return
	}
	run := seen.Run
	runWords := fmt.Sprintf("run %d of “%s”", run.Number, run.RecipeTitle)
	view := &RecipeCardView{Kind: recipeCardPicks, Recipe: run.RecipeName, Title: run.RecipeTitle, Link: links.RecipeRun(run.ID), Run: runWords}
	kind := "moment"
	count := 0
	for _, p := range run.Picks {
		cp := RecipeCardPick{Part: p.Part, Kind: p.Kind, N: p.N, When: p.When, Of: p.FootageTitle, Why: strings.Join(p.Why, ", "), Words: clip(p.Words, 120), State: p.State}
		if ch != nil && p.Part == ch.Part {
			kind = p.Kind
			count++
			cp.Changes = p.N == ch.N
		}
		if in.Keep && (p.State == models.PickFirst || p.State == "standin") {
			cp.Changes = true
		}
		view.Picks = append(view.Picks, cp)
	}
	var parts []string
	if ch != nil {
		pick := fmt.Sprintf("%s %d of “%s”", kind, ch.N, ch.Part)
		if kind == "short" {
			pick = fmt.Sprintf("short %d", ch.N)
		}
		switch {
		case ch.Drop:
			parts = append(parts, "drop "+pick)
		case ch.N == count+1:
			parts = append(parts, fmt.Sprintf("add %s %d to “%s”: %s–%s", kind, ch.N, ch.Part, editspec.Clock(*ch.From), editspec.Clock(*ch.To)))
		default:
			parts = append(parts, fmt.Sprintf("make %s %s–%s", pick, editspec.Clock(*ch.From), editspec.Clock(*ch.To)))
		}
		if ch.Footage != "" && kind == "moment" && !ch.Drop {
			parts[0] += " of “" + ch.Footage + "”"
		}
	}
	if in.Keep {
		if ch != nil {
			parts = append(parts, "keep the others as MetaRush made them")
		} else {
			parts = append(parts, "keep MetaRush's first picks")
		}
	}
	view.Change = upperFirst(joinWords(parts)) + "."
	a.Recipe = view
	a.Title = clip(upperFirst(joinWords(parts))+" in "+runWords, 160)
	a.Details = []string{view.Change}
	if ch != nil && ch.Why != "" {
		a.Details = append(a.Details, "Claude's reason: "+theirs(ch.Why))
	}
	a.Details = append(a.Details, "The run goes on by itself once every pick is kept or changed.")
	switch run.State {
	case models.RunDone, models.RunStopped, models.RunFailed:
		unavailable(a, a.Title, upperFirst(runWords)+" already ended, so its picks can't change; change its cut instead.")
		return
	}
	setInput(a, "recipe.picks", in)
}

// describeRecipeSheet saves a recipe sheet Claude wrote as a draft (D213):
// what it changes from the version it was written from (or that it makes a
// new recipe), its problems; always a draft, which only a person makes current.
func (m *Manager) describeRecipeSheet(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	what := "Save a recipe sheet as a draft"
	path := strings.TrimSpace(first(call.Args))
	if path == "" || path == "-" {
		incomplete(a, what, "Claude did not name the recipe sheet's file (in this conversation's proposals folder).")
		return
	}
	if c == nil {
		incomplete(a, what, "The recipe sheet is not in this conversation's proposals folder.")
		return
	}
	raw, why := m.proposalFile(c, path, "recipe sheet")
	if why != "" {
		incomplete(a, what, why)
		return
	}
	note := clip(strings.TrimSpace(call.Flags["note"]), 300)
	chk, err := recipeOp[recipeSheetCheckSeen](ctx, m, "recipe.check", map[string]string{"sheet": string(raw)})
	if err != nil {
		unavailable(a, what, "MetaRush couldn't read it: "+firstLine(apperr.From(err).Message))
		return
	}
	title := strings.TrimSpace(chk.Title)
	if title == "" {
		title = chk.Name
	}
	view := &RecipeCardView{Kind: recipeCardSheet, Recipe: chk.Name, Title: title, Differences: chk.Parts, RemovedRules: chk.RemovedRules, Note: note, New: chk.New}
	if chk.Name != "" && !chk.New {
		view.Link = links.Recipe(chk.Name)
	}
	if chk.Base != nil {
		view.Version = chk.Base.Number
	}
	a.Recipe = view
	a.Title = clip(fmt.Sprintf("Save the changed sheet of “%s” as a draft", title), 160)
	if chk.New {
		a.Title = clip(fmt.Sprintf("Save “%s” as a new recipe, a draft", title), 160)
	}
	var errs []string
	for _, p := range chk.Problems {
		if p.Severity == "error" {
			errs = append(errs, fmt.Sprintf("line %d: %s", p.Line, strings.TrimSuffix(p.Message, ".")))
		}
	}
	switch {
	case len(errs) > 0 || !chk.Valid:
		first := "it doesn't read as a recipe"
		if len(errs) > 0 {
			first = errs[0]
		}
		view.Problems = errs
		unavailable(a, a.Title, "It can't be saved yet: "+first+".")
		return
	case chk.Same && chk.Base != nil:
		unavailable(a, a.Title, fmt.Sprintf("Nothing would change: the sheet is version %d.", chk.Base.Number))
		return
	}
	view.Problems = checkProblems(&chk.Check)
	if chk.Base != nil {
		a.Details = []string{changeCount(chk.Parts, chk.Base.Number)}
	} else {
		a.Details = []string{"A new recipe."}
	}
	if note != "" {
		a.Details = append(a.Details, "Claude's note: "+theirs(note))
	}
	if len(chk.RemovedRules) > 0 {
		a.Details = append(a.Details, "It no longer keeps "+theirsList(chk.RemovedRules)+".")
	}
	a.Details = append(a.Details, "A draft: runs keep making the current version until a person makes it current.")
	if n := len(view.Problems); n > 0 {
		a.Details = append(a.Details, "Its check finds "+pluralize(n, "problem")+" to fix before it can be made current.")
	}
	setInput(a, "recipe.write", recipeWriteIn{Sheet: string(raw), Note: note})
}

// describeRecipeSuggestions saves the changes suggested from what people
// changed in runs as a draft (D217), with why each and what the draft changes.
func (m *Manager) describeRecipeSuggestions(ctx context.Context, call *cliCall, a *AssistantAction) {
	what := "Save the suggested changes as a draft"
	if !hasBool(call, "save") {
		incomplete(a, what, "Looking at the suggestions only reads; saving them as a draft is --save.")
		return
	}
	ref := strings.TrimSpace(first(call.Args))
	if ref == "" {
		incomplete(a, what, "Claude did not say which recipe.")
		return
	}
	in := recipeSuggestionsIn{Recipe: ref, Only: listFlag(call, "only"), Note: clip(strings.TrimSpace(call.Flags["note"]), 300)}
	sug, err := recipeOp[recipeSuggestionsSeen](ctx, m, "recipe.suggestions", recipeSuggestionsIn{Recipe: in.Recipe, Only: in.Only})
	if err != nil {
		unavailable(a, what, "MetaRush couldn't work them out: "+firstLine(apperr.From(err).Message))
		return
	}
	view := &RecipeCardView{Kind: recipeCardSuggested, Recipe: sug.Recipe, Title: sug.Title, Link: links.Recipe(sug.Recipe), Differences: sug.Changes, Note: in.Note}
	for _, s := range sug.Suggestions {
		if s.InDraft {
			view.Reasons = append(view.Reasons, s.Reason)
		}
	}
	if sug.Base != nil {
		view.Version = sug.Base.Number
	}
	a.Recipe = view
	title := clip(fmt.Sprintf("Make a draft of “%s” with the suggested changes", sug.Title), 160)
	if len(view.Reasons) == 0 {
		unavailable(a, title, "Nothing people changed in its runs repeats now.")
		return
	}
	view.Problems = checkProblems(sug.Check)
	a.Title = title
	a.Details = append(a.Details, view.Reasons...)
	if sug.Base != nil {
		a.Details = append(a.Details, changeCount(sug.Changes, sug.Base.Number))
	}
	a.Details = append(a.Details, "A draft: runs keep making the current version until a person makes it current.")
	if n := len(view.Problems); n > 0 {
		a.Details = append(a.Details, "Its check finds "+pluralize(n, "problem")+" to fix before it can be made current.")
	}
	setInput(a, "recipe.suggestions.save", in)
}
