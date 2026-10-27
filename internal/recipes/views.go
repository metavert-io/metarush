package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// What the Recipes tab, `metarush recipe …` and Claude read: recipes as
// cards (what each makes, its inputs, the last runs), versions, inputs with
// their limits, sections, and runs with their steps in plain words.

// RecipeRow is a recipe as its card shows it.
type RecipeRow struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Title string `json:"title"`
	Makes string `json:"makes"`
	// Current and Draft are version numbers (0 none).
	Current int `json:"current"`
	Draft   int `json:"draft,omitempty"`
	// Lands says where runs go, plainly.
	Lands     string             `json:"lands"`
	ProjectID string             `json:"projectId,omitempty" jsonschema:"description=The project it was saved from (runs add cuts to it when it lands there)."`
	Inputs    []RecipeInputRow   `json:"inputs"`
	Example   *RecipeExampleView `json:"example,omitempty"`
	LastRuns  []RecipeRunBrief   `json:"lastRuns"`
	Runs      int                `json:"runs"`
	Archived  bool               `json:"archived,omitempty"`
	// PlansByItself: it plans runs by itself (new files, a weekly time, D218);
	// Planned: a plan it made waits for a person to run it.
	PlansByItself bool      `json:"plansByItself,omitempty"`
	Planned       bool      `json:"planned,omitempty"`
	URL           string    `json:"url"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// RecipeExampleView is the render a recipe's card plays.
type RecipeExampleView struct {
	RenderID   string `json:"renderId"`
	ProjectID  string `json:"projectId"`
	RevisionID string `json:"revisionId"`
	VideoURL   string `json:"videoUrl" jsonschema:"description=The dashboard's address of the video."`
	ThumbURL   string `json:"thumbUrl"`
}

// RecipeInputRow is one input: its type, default and limits, from which the
// Run with… form and the CLI's --input flags are built.
type RecipeInputRow struct {
	Name     string `json:"name"`
	Kind     string `json:"kind" jsonschema:"enum=words,enum=choice,enum=footage,enum=pictures,enum=voice"`
	Label    string `json:"label"`
	Required bool   `json:"required,omitempty"`
	// Default is what a run uses unless it gives another, plainly; Value the
	// same as data (words, or clips and pictures by ID).
	Default string             `json:"default"`
	Value   models.RecipeValue `json:"value"`
	// Limits say what it takes ("up to 40 characters", "1–8 pictures").
	Limits    string   `json:"limits,omitempty"`
	MaxLength int      `json:"maxLength,omitempty"`
	Choices   []string `json:"choices,omitempty"`
	MinCount  int      `json:"minCount,omitempty"`
	MaxCount  int      `json:"maxCount,omitempty"`
	// From says where a run takes it from by default ("the newest in the linked folder “Art”").
	From string `json:"from,omitempty"`
	// Goes says where it goes ("the end card's headline").
	Goes []string `json:"goes"`
}

// RecipeSectionRow is one section of the video, plainly.
type RecipeSectionRow struct {
	Name    string `json:"name"`
	Kind    string `json:"kind" jsonschema:"enum=fixed,enum=chosen"`
	Purpose string `json:"purpose,omitempty"`
	Length  string `json:"length"`
	// What it shows ("2 clips: “Open” from the Remotion project “Teaser”").
	What string `json:"what"`
}

// RecipeVersionRow is one version of a recipe.
type RecipeVersionRow struct {
	Number int                 `json:"number"`
	Hash   string              `json:"hash"`
	Short  string              `json:"short"`
	Parent int                 `json:"parent,omitempty"`
	State  string              `json:"state" jsonschema:"enum=draft,enum=current,enum=earlier"`
	Note   string              `json:"note,omitempty"`
	By     string              `json:"by"`
	At     time.Time           `json:"at"`
	OK     bool                `json:"ok" jsonschema:"description=Its last check found no problems."`
	Check  *models.RecipeCheck `json:"check,omitempty"`
	// Imported: it was brought in from a recipe file another workspace exported (D219).
	Imported bool `json:"imported,omitempty"`
}

// RecipeRunBrief is a run in a list.
type RecipeRunBrief struct {
	ID         string             `json:"id"`
	Ref        string             `json:"ref" jsonschema:"description=How to name it in a command (app-teaser#4)."`
	Recipe     string             `json:"recipe"`
	Title      string             `json:"title"`
	Number     int                `json:"number"`
	State      string             `json:"state"`
	StateLabel string             `json:"stateLabel"`
	WaitingFor string             `json:"waitingFor,omitempty"`
	At         time.Time          `json:"at"`
	By         string             `json:"by"`
	ProjectID  string             `json:"projectId,omitempty"`
	Project    string             `json:"project,omitempty"`
	Branch     string             `json:"branch,omitempty" jsonschema:"description=The cut it made (its name in the project)."`
	Version    int                `json:"version,omitempty" jsonschema:"description=The cut's version number it made."`
	Approved   bool               `json:"approved" jsonschema:"description=A person approved the version it made."`
	Spent      models.RecipeSpent `json:"spent"`
	SpentWords string             `json:"spentWords"`
	Repeats    string             `json:"repeats,omitempty" jsonschema:"description=The run it makes again (its ref)."`
	URL        string             `json:"url"`
	// Outputs are the shorts of its pack (D214), each its own project.
	Outputs []RecipeOutputBrief `json:"outputs,omitempty"`
}

// RecipePick is one of a run's first picks (D215) or stand-ins, as Claude and
// the run's page show them: a chosen section's moment (its clip in the run's
// cut) or a short's stretch of a conversation or of footage (D295), why it was picked and where it
// stands.
type RecipePick struct {
	// Part is the chosen section or the pack output it is in; N its number there.
	Part string `json:"part"`
	Kind string `json:"kind" jsonschema:"enum=moment,enum=short"`
	N    int    `json:"n"`
	// ClipID is a moment's clip in the run's cut.
	ClipID string `json:"clipId,omitempty"`
	// Footage is the clip's footage (a moment, or a short of footage) or the conversation (a short),
	// FootageTitle its name.
	Footage      string `json:"footage"`
	FootageTitle string `json:"footageTitle,omitempty"`
	// From and To are seconds into the footage (a moment) or on the
	// conversation's clock (a short of a conversation); When says them plainly.
	From  float64  `json:"from"`
	To    float64  `json:"to"`
	When  string   `json:"when"`
	Why   []string `json:"why,omitempty"`
	Words string   `json:"words,omitempty" jsonschema:"description=What is said in it (the start)."`
	// State: first (MetaRush's first pick, not looked at yet), standin (spread
	// over the footage for Claude to replace), kept, changed or dropped.
	State string `json:"state" jsonschema:"enum=first,enum=standin,enum=kept,enum=changed,enum=dropped"`
	// Made is the short's own project once it is made.
	ProjectID string `json:"projectId,omitempty"`
}

// RecipeRunView is a run with its steps and where everything is.
type RecipeRunView struct {
	models.RecipeRun
	Ref         string `json:"ref"`
	StateLabel  string `json:"stateLabel"`
	SpentWords  string `json:"spentWords"`
	AgreedWords string `json:"agreedWords"`
	Approved    bool   `json:"approved"`
	URL         string `json:"url"`
	ProjectURL  string `json:"projectUrl,omitempty"`
	VersionURL  string `json:"versionUrl,omitempty"`
	RenderURL   string `json:"renderUrl,omitempty"`
	// Claude says what Claude is to do when the run waits for it.
	Claude string `json:"claude,omitempty"`
	// Picks are its first picks and stand-ins (D215), section by section, then
	// the shorts of its pack: what keep and change (recipe.picks) act on.
	Picks []RecipePick `json:"picks,omitempty"`
	// Shorts are the pack's videos (D214) with links to their projects.
	Shorts []RecipeOutputBrief `json:"shorts,omitempty"`
	// Pending are the jobs its current step waits on.
	Pending []string `json:"pending,omitempty"`
	// SampleWords says where its sample stands (D294); SampleURL opens the
	// sample's version and SampleRenderURL its review copy.
	SampleWords     string `json:"sampleWords,omitempty"`
	SampleURL       string `json:"sampleUrl,omitempty"`
	SampleRenderURL string `json:"sampleRenderUrl,omitempty"`
	// SampleWaits: the run waits for a person to approve its sample.
	SampleWaits bool `json:"sampleWaits,omitempty"`
}

// landsWords says where runs go.
func landsWords(ctx context.Context, a *app.App, d *models.RecipeDoc) string {
	if d.Lands.Kind == models.RecipeLandsProject {
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), d.Lands.ProjectID, &p) == nil {
			return "a new cut in “" + p.Name + "”"
		}
		return "a new cut in a project that was cleared out"
	}
	return "a new project each time"
}

// recipeRow makes a recipe's card.
func recipeRow(ctx context.Context, a *app.App, r *models.Recipe, v *models.RecipeVersion) RecipeRow {
	row := RecipeRow{ID: r.ID, Name: r.Name, Title: r.Title, Makes: r.Makes, Current: r.CurrentNumber, Draft: r.DraftNumber, ProjectID: r.ProjectID,
		Runs: r.Runs, Archived: r.Archived, URL: ops.DashboardURL(a, links.Recipe(r.Name)), UpdatedAt: r.UpdatedAt, Inputs: []RecipeInputRow{}, LastRuns: []RecipeRunBrief{},
		PlansByItself: triggersOn(r.Triggers), Planned: r.Prepared != nil}
	if v != nil {
		row.Lands = landsWords(ctx, a, &v.Doc)
		row.Inputs = inputRows(ctx, a, &v.Doc)
	}
	if ex := r.Example; ex != nil {
		row.Example = &RecipeExampleView{RenderID: ex.RenderID, ProjectID: ex.ProjectID, RevisionID: ex.RevisionID,
			VideoURL: "/media/renders/" + ex.RenderID, ThumbURL: "/media/renders/" + ex.RenderID + "/thumbnail"}
	}
	runs, _ := runsOf(ctx, a, bson.M{"recipeId": r.ID}, 3)
	for i := range runs {
		row.LastRuns = append(row.LastRuns, runBrief(ctx, a, &runs[i]))
	}
	return row
}

// inputRows lists a recipe's inputs for people.
func inputRows(ctx context.Context, a *app.App, d *models.RecipeDoc) []RecipeInputRow {
	out := []RecipeInputRow{}
	for _, in := range d.Inputs {
		row := RecipeInputRow{Name: in.Name, Kind: in.Kind, Label: in.Label, Required: in.Required, Value: in.Default, MaxLength: in.MaxLength, Choices: in.Choices, Goes: []string{}}
		if row.Label == "" {
			row.Label = upperFirst(inputLabel(in))
		}
		switch in.Kind {
		case models.RecipeWords, models.RecipeChoice:
			row.Default = "“" + shorten(in.Default.Text, 80) + "”"
		case models.RecipeVoice:
			row.Default = "the voice “" + in.Default.Text + "”"
			if len(in.Default.Items) == 0 {
				row.Default = "the narration voice the Content Settings name" // D295
			}
		case models.RecipeFootage:
			row.Default = plural(len(in.Default.Items), "clip")
		case models.RecipePictures:
			row.Default = plural(len(in.Default.Items), "picture")
		}
		var limits []string
		if in.MaxLength > 0 {
			limits = append(limits, fmt.Sprintf("up to %d characters", in.MaxLength))
		}
		if len(in.Choices) > 0 {
			limits = append(limits, "one of "+joinAnd(quoteAll(in.Choices)))
		}
		if c := in.Count; c != nil {
			row.MinCount, row.MaxCount = int(c.Min), int(c.Max)
			what := "clip"
			if in.Kind == models.RecipePictures {
				what = "picture"
			}
			switch {
			case c.Min > 0 && c.Max > 0 && c.Min != c.Max:
				limits = append(limits, fmt.Sprintf("%d–%d %ss", int(c.Min), int(c.Max), what))
			case c.Max > 0:
				limits = append(limits, plural(int(c.Max), what))
			case c.Min > 0:
				limits = append(limits, "at least "+plural(int(c.Min), what))
			}
		}
		row.Limits = strings.Join(limits, ", ")
		if f := in.From; f != nil {
			switch {
			case f.Folder != "":
				var lf models.LinkedFolder
				if a.DB.GetByID(ctx, store.CLinkedFolders, a.WorkspaceID(), f.Folder, &lf) == nil {
					row.From = "the newest in the linked folder “" + lf.Name + "”"
				} else {
					row.From = "the newest in a linked folder"
				}
			case f.Collection != "":
				row.From = "the newest on a collection"
			case f.Project != "":
				row.From = "the newest footage of a project"
			case f.Repo != "":
				row.From = repoSourceWords(ctx, a, f) // D216
			case f.Drive != "" || f.Dropbox != "":
				if df, _, err := driveFolder(in); err == nil {
					row.From = "the newest files of " + df.Words() // D220, D306
				} else {
					row.From = "the newest in a cloud folder"
				}
			}
		}
		for _, b := range bindingsOf(d, in.Name) {
			row.Goes = append(row.Goes, targetWords(d, b))
		}
		for _, s := range d.Sections {
			if s.Kind == models.RecipeChosen && s.From == in.Name {
				row.Goes = append(row.Goes, "the moments Claude chooses for “"+s.Name+"”")
			}
		}
		out = append(out, row)
	}
	return out
}

// sectionRows lists a recipe's sections for people.
func sectionRows(ctx context.Context, a *app.App, d *models.RecipeDoc) []RecipeSectionRow {
	out := []RecipeSectionRow{}
	for _, s := range d.Sections {
		row := RecipeSectionRow{Name: s.Name, Kind: s.Kind, Purpose: s.Purpose, Length: seconds(sectionSeconds(s))}
		if s.Kind == models.RecipeChosen {
			if s.Length != nil && s.Length.Max > 0 && s.Length.Min > 0 {
				row.Length = secondsRange(s.Length.Min, s.Length.Max)
			}
			from := s.From
			for _, in := range d.Inputs {
				if in.Name == s.From {
					from = inputLabel(in)
				}
			}
			row.What = "Claude chooses " + sectionNeeds(s) + " from " + from
		} else {
			row.What = clipsWords(ctx, a, s.Clips)
		}
		out = append(out, row)
	}
	return out
}

// clipsWords says what clips show ("2 clips: “Open” from “Teaser”, “intro.mov”").
func clipsWords(ctx context.Context, a *app.App, cs []editspec.Clip) string {
	var names []string
	for i, c := range cs {
		if i == 3 {
			names = append(names, fmt.Sprintf("%d more", len(cs)-3))
			break
		}
		var as models.Asset
		switch {
		case strings.HasPrefix(c.AssetID, "cut:"):
			names = append(names, "a cut of another project")
		case strings.HasPrefix(c.AssetID, "conv:"):
			names = append(names, "a call recording")
		case a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), c.AssetID, &as) == nil:
			if as.Source.IsRemotion() && as.Source.Remotion.Composition != "" {
				src := as.Source.Remotion.SourceTitle
				if src == "" {
					src = as.Source.Remotion.SourceName
				}
				names = append(names, "“"+as.Source.Remotion.Composition+"” from the Remotion project “"+src+"”")
			} else {
				names = append(names, "“"+shorten(as.DisplayTitle(), 40)+"”")
			}
		default:
			names = append(names, "a clip that is gone")
		}
	}
	return plural(len(cs), "clip") + ": " + joinAnd(names)
}

// versionRow is a version in a list.
func versionRow(v *models.RecipeVersion, numbers map[string]int) RecipeVersionRow {
	by := v.By.UserName
	if by == "" {
		by = "someone"
	}
	if v.By.Via == models.ViaAssistant {
		by = "Claude, for " + by
	}
	row := RecipeVersionRow{Number: v.Number, Hash: v.Hash, Short: models.ShortHash(v.Hash), Parent: numbers[v.Parent], State: v.State, Note: v.Note, By: by, At: v.At, Check: v.Check}
	row.OK = v.Check == nil || v.Check.OK
	row.Imported = v.Imported != nil
	return row
}

// runBrief is a run in a list.
func runBrief(ctx context.Context, a *app.App, r *models.RecipeRun) RecipeRunBrief {
	by := r.CreatedBy.UserName
	if by == "" {
		by = "someone"
	}
	b := RecipeRunBrief{ID: r.ID, Ref: runRef(r), Recipe: r.RecipeName, Title: r.RecipeTitle, Number: r.Number, State: r.State, StateLabel: stateWords(r),
		WaitingFor: r.WaitingFor, At: r.CreatedAt, By: by, ProjectID: r.ProjectID, Project: r.ProjectName, Branch: r.Branch, Version: r.RevisionNumber, Spent: r.Spent,
		SpentWords: spentWords(r.Spent), URL: ops.DashboardURL(a, links.RecipeRun(r.ID))}
	if r.Repeats != "" {
		if p, err := getRun(ctx, a, r.Repeats); err == nil {
			b.Repeats = runRef(p)
		}
	}
	if r.Revision != "" {
		var rev models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), r.Revision, &rev) == nil {
			b.Approved = rev.Approval.State == models.ApprovalApproved
		}
	}
	b.Outputs = outputBriefs(a, r)
	return b
}

// outputBriefs are a run's shorts with links to their projects.
func outputBriefs(a *app.App, r *models.RecipeRun) []RecipeOutputBrief {
	var out []RecipeOutputBrief
	for _, o := range r.Outputs {
		b := RecipeOutputBrief{Output: o.Output, N: o.N, State: o.State, ProjectID: o.ProjectID, Project: o.ProjectName, Branch: o.Branch, Version: o.RevisionNumber}
		if o.ProjectID != "" && !o.ProjectGone {
			b.URL = ops.DashboardURL(a, links.Project(o.ProjectID))
		}
		out = append(out, b)
	}
	return out
}

// runPicks are a run's first picks and stand-ins, as they stand in its cut,
// then its shorts' stretches.
func runPicks(ctx context.Context, a *app.App, r *models.RecipeRun) []RecipePick {
	var out []RecipePick
	titles := newSheetContext(ctx, a)
	if len(r.StandIns) > 0 && r.ProjectID != "" && !r.ProjectGone {
		var spec *editspec.Spec
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), r.ProjectID, &p) == nil {
			if head := p.Branches[r.Branch].Head; head != "" {
				var rev models.Revision
				if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &rev) == nil {
					spec = &rev.Spec
				}
			}
		}
		n := map[string]int{}
		for _, si := range r.StandIns {
			n[si.Section]++
			pk := RecipePick{Part: si.Section, Kind: "moment", N: n[si.Section], ClipID: si.ClipID, Footage: si.AssetID, From: si.In, To: si.Out, Why: si.Why, Words: si.Words}
			switch {
			case si.Kept:
				pk.State = models.PickKept
			case spec == nil || standInAsPut(si, spec):
				pk.State = "standin"
				if si.First {
					pk.State = models.PickFirst
				}
			default:
				pk.State = models.PickDropped
				for _, c := range spec.Clips {
					if c.ID == si.ClipID {
						pk.State, pk.Footage, pk.From, pk.To = models.PickChanged, c.AssetID, c.In, c.Out
					}
				}
			}
			pk.FootageTitle = titles.title(pk.Footage)
			pk.When = editspec.Clock(pk.From) + "–" + editspec.Clock(pk.To)
			out = append(out, pk)
		}
	}
	for _, o := range r.Outputs {
		footage := o.Conversation
		if o.AssetID != "" {
			footage = o.AssetID // a short of footage (D295)
		}
		out = append(out, RecipePick{Part: o.Output, Kind: "short", N: o.N, Footage: footage, FootageTitle: outputSourceName(o), From: o.From, To: o.To,
			When: editspec.Clock(o.From) + "–" + editspec.Clock(o.To), Why: o.Why, Words: o.First, State: o.Pick, ProjectID: o.ProjectID})
	}
	return out
}

// runView is a run with its steps and links.
func runView(ctx context.Context, a *app.App, r *models.RecipeRun) *RecipeRunView {
	v := &RecipeRunView{RecipeRun: *r, Ref: runRef(r), StateLabel: stateWords(r), SpentWords: spentWords(r.Spent), URL: ops.DashboardURL(a, links.RecipeRun(r.ID))}
	v.Lease = nil
	if r.Agreed != nil {
		switch {
		case r.Agreed.Skipped:
			v.AgreedWords = "no estimate asked for (estimates are off): budgets and limits hold"
		case r.Agreed.UpTo <= 0:
			v.AgreedWords = "nothing to pay"
		case r.Agreed.UpTo < 0.01:
			v.AgreedWords = "less than a cent"
		default:
			v.AgreedWords = "up to " + costs.Money(r.Agreed.UpTo)
		}
	}
	if r.ProjectID != "" && !r.ProjectGone {
		v.ProjectURL = ops.DashboardURL(a, links.Project(r.ProjectID))
		if r.Revision != "" {
			v.VersionURL = ops.DashboardURL(a, links.Revision(r.ProjectID, r.Revision))
			var rev models.Revision
			if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), r.Revision, &rev) == nil {
				v.Approved = rev.Approval.State == models.ApprovalApproved
			}
		}
		if r.Render != "" {
			v.RenderURL = ops.DashboardURL(a, links.Render(r.ProjectID, r.Render))
		}
	}
	v.Picks = runPicks(ctx, a, r)
	v.Shorts = outputBriefs(a, r)
	if r.State == models.RunWaiting && r.WaitingFor == models.RunWaitsForClaude {
		v.Claude = claudeTask(ctx, a, r, v.Picks)
	}
	v.Pending = PendingJobs(ctx, a, r)
	if sm := r.Sample; sm != nil {
		v.SampleWords = sampleWords(sm)
		v.SampleWaits = sm.State == models.SampleWaiting && r.State == models.RunWaiting
		if r.ProjectID != "" && !r.ProjectGone {
			if sm.Revision != "" {
				v.SampleURL = ops.DashboardURL(a, links.Revision(r.ProjectID, sm.Revision))
			}
			if sm.Render != "" {
				v.SampleRenderURL = ops.DashboardURL(a, links.Render(r.ProjectID, sm.Render))
			}
		}
	}
	return v
}

// claudeTask says what Claude is to do for a run waiting on it: which
// stand-ins to replace in which cut, with what; which first picks (D215) of
// the sections and of the shorts (D214) to keep or change.
func claudeTask(ctx context.Context, a *app.App, r *models.RecipeRun, picks []RecipePick) string {
	var v models.RecipeVersion
	if a.DB.GetByID(ctx, store.CRecipeVersions, a.WorkspaceID(), versionID(a.WorkspaceID(), r.Version), &v) != nil {
		return ""
	}
	var parts []string
	sections := false
	seen := map[string]bool{}
	for _, si := range r.StandIns {
		if seen[si.Section] {
			continue
		}
		seen[si.Section] = true
		for _, s := range v.Doc.Sections {
			if s.Name != si.Section {
				continue
			}
			from := s.From
			for _, in := range r.Inputs {
				if in.Name == s.From {
					from = in.Words
				}
			}
			purpose := s.Purpose
			if purpose == "" {
				purpose = "the strongest moments"
			}
			var replace, look []string
			for _, pk := range picks {
				if pk.Kind != "moment" || pk.Part != s.Name {
					continue
				}
				switch pk.State {
				case "standin":
					replace = append(replace, pk.ClipID)
				case models.PickFirst:
					w := fmt.Sprintf("%s (%s of “%s”: %s", pk.ClipID, pk.When, pk.FootageTitle, joinAnd(pk.Why))
					if pk.Words != "" {
						w += " — “" + shorten(pk.Words, 60) + "”"
					}
					look = append(look, w+")")
				}
			}
			if len(replace) > 0 {
				sections = true
				parts = append(parts, fmt.Sprintf("In the section “%s”, replace the stand-in clips %s with %s (%s) chosen from %s.", s.Name, strings.Join(replace, ", "), purpose, sectionNeeds(s), from))
			}
			if len(look) > 0 {
				sections = true
				parts = append(parts, fmt.Sprintf("In the section “%s” (%s, %s), MetaRush's first picks from %s are %s: keep the ones that fit and change the others.", s.Name, purpose, sectionNeeds(s), from, joinAnd(look)))
			}
		}
	}
	var shorts []string
	conv := ""
	for _, pk := range picks {
		if pk.Kind == "short" && pk.State == models.PickFirst {
			conv = pk.FootageTitle
			w := fmt.Sprintf("%d. %s (%s)", pk.N, pk.When, joinAnd(pk.Why))
			if pk.Words != "" {
				w = fmt.Sprintf("%d. %s “%s” (%s)", pk.N, pk.When, shorten(pk.Words, 60), joinAnd(pk.Why))
			}
			shorts = append(shorts, w)
		}
	}
	if len(shorts) > 0 {
		purpose := ""
		for _, o := range v.Doc.Pack {
			if o.Purpose != "" {
				purpose = " Look for " + strings.TrimSuffix(o.Purpose, ".") + "."
			}
		}
		parts = append(parts, fmt.Sprintf("The shorts' first picks from “%s” are %s: keep the stretches that stand on their own, change or drop the others.%s", conv, strings.Join(shorts, "; "), purpose))
	}
	if len(parts) == 0 {
		return ""
	}
	end := " Keep picks as they are, or change them"
	if sections {
		end += fmt.Sprintf(" (a section's moments through the sheet of version %d of the cut in “%s”; change only those clips — the fixed sections are locked)", r.RevisionNumber, r.ProjectName)
	}
	return strings.Join(parts, " ") + end + "; the run goes on by itself once every pick is kept or changed."
}

// readable is a recipe's outline in Markdown (the plan's form; not a sheet to edit).
func readable(ctx context.Context, a *app.App, d *models.RecipeDoc) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — %s · runs: %s", d.Title, MakesWords(d), landsWords(ctx, a, d))
	if d.Settings != "" {
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), d.Settings, &p) == nil {
			fmt.Fprintf(&b, " · settings: “%s”'s", p.Name)
		}
	}
	b.WriteString("\n## Inputs\n")
	for _, in := range inputRows(ctx, a, d) {
		line := fmt.Sprintf("- %s: %s", in.Name, kindWords(in.Kind))
		if in.Limits != "" {
			line += " · " + in.Limits
		}
		if in.From != "" {
			line += " · from " + in.From
		}
		line += " · now " + in.Default
		b.WriteString(line + "\n")
	}
	b.WriteString("## Sections\n")
	for i, s := range sectionRows(ctx, a, d) {
		fmt.Fprintf(&b, "%d. %s · %s · %s · %s\n", i+1, s.Name, s.Kind, s.Length, s.What)
	}
	var sound []string
	if v := d.Cut.Voiceover; v != nil && len(v.Beats) > 0 {
		sound = append(sound, "narration: "+plural(len(v.Beats), "line"))
	}
	for _, t := range d.Cut.Audio {
		if t.Role == editspec.RoleMusic {
			sound = append(sound, "music: “"+t.ID+"”")
		}
	}
	if len(sound) > 0 {
		b.WriteString("## Sound\n")
		for _, s := range sound {
			b.WriteString("- " + s + "\n")
		}
	}
	if len(d.Pack) > 0 {
		b.WriteString("## Pack\n")
		for _, o := range d.Pack {
			b.WriteString("- " + o.Name + ": " + outputWords(o) + "\n")
		}
	}
	if len(d.Rules) > 0 || len(d.Signoffs) > 0 {
		b.WriteString("## Rules\n")
		for _, r := range d.Rules {
			b.WriteString("- " + r.Text + "\n")
		}
		for _, s := range d.Signoffs {
			b.WriteString("- Sign-off: " + s + "\n")
		}
	}
	if len(d.Hints) > 0 {
		b.WriteString("## Hints\n")
		for _, h := range d.Hints {
			b.WriteString("- " + h + "\n")
		}
	}
	return b.String()
}

// docJSON is a document as indented JSON (for --details and update patches).
func docJSON(d *models.RecipeDoc) string {
	b, _ := json.MarshalIndent(d, "", "  ")
	return string(b)
}
