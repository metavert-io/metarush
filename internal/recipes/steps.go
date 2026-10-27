package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/remotion"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// engine advances one run: it acts as the run's person through the recipe
// channel (models.ViaRecipe).
type engine struct {
	a            *app.App
	run          *models.RecipeRun
	recipe       *models.Recipe
	doc          *models.RecipeDoc
	values       map[string]*models.RecipeRunInput
	changed      map[string]bool
	voice        *editspec.VoiceRef
	defaultVoice string
}

// qaGrace is how long a finished review copy may wait for its quality
// check to appear before the run goes on without it.
var qaGrace = 3 * time.Minute

func newEngine(ctx context.Context, a *app.App, run *models.RecipeRun) (*engine, error) {
	var v models.RecipeVersion
	if err := a.DB.GetByID(ctx, store.CRecipeVersions, a.WorkspaceID(), versionID(a.WorkspaceID(), run.Version), &v); err != nil {
		return nil, err
	}
	var r models.Recipe
	if err := a.DB.GetByID(ctx, store.CRecipes, a.WorkspaceID(), run.RecipeID, &r); err != nil {
		return nil, err
	}
	ra := *a
	ra.Actor = models.Actor{UserID: run.CreatedBy.UserID, UserName: run.CreatedBy.UserName, InstallationID: a.Cfg.Installation.ID, Via: models.ViaRecipe}
	e := &engine{a: &ra, run: run, recipe: &r, doc: &v.Doc, values: map[string]*models.RecipeRunInput{}, changed: map[string]bool{}}
	for i := range run.Inputs {
		in := &run.Inputs[i]
		e.values[in.Name] = in
		e.changed[in.Name] = in.Changed
	}
	for _, x := range v.Doc.Inputs {
		if x.Kind != models.RecipeVoice {
			continue
		}
		if len(x.Default.Items) > 0 {
			e.defaultVoice = x.Default.Items[0]
		}
		if in := e.values[x.Name]; in != nil && len(in.Items) > 0 {
			e.voice = &editspec.VoiceRef{Provider: providers.ElevenLabsName, VoiceID: in.Items[0].ID, Name: in.Items[0].Title}
		}
		if run.Repeats != "" {
			if prev, err := getRun(ctx, a, run.Repeats); err == nil {
				for _, pv := range prev.Inputs {
					if pv.Name == x.Name && len(pv.Items) > 0 {
						e.defaultVoice = pv.Items[0].ID
					}
				}
			}
		}
	}
	return e, nil
}

// step does (or looks at) one step; true when it is done.
func (e *engine) step(ctx context.Context, st *models.RecipeStep) (bool, error) {
	switch st.ID {
	case stepInputs:
		return e.inputs(ctx, st)
	case stepPlace:
		return e.place(ctx, st)
	case stepSample:
		return e.sampleStep(ctx, st)
	case stepApprove:
		return e.approveStep(ctx, st)
	case stepCode:
		return e.code(ctx, st)
	case stepShots:
		return e.shots(ctx, st)
	case stepAssemble:
		return e.assemble(ctx, st)
	case stepVoice:
		return e.voiceStep(ctx, st)
	case stepClaude:
		return e.claude(ctx, st)
	case stepSound:
		return e.soundStep(ctx, st)
	case stepReview:
		return e.review(ctx, st)
	case stepPack:
		return e.packStep(ctx, st)
	case stepFinish:
		return e.finish(ctx, st)
	}
	st.State = models.StepSkipped
	return true, nil
}

// waitFor makes the run wait for a person or for Claude.
func (e *engine) waitFor(st *models.RecipeStep, who, words string) {
	e.run.State, e.run.WaitingFor, e.run.Waiting, e.run.WaitingUSD = models.RunWaiting, who, words, 0
	st.State, st.Detail = models.StepWaiting, words
}

// waitForMore makes the run wait for a person's OK to spend more than they
// agreed to ("the AI voice costs"), saying how much; the amount goes with it
// (RecipeRun.WaitingUSD), so going on agrees to exactly what was asked.
func (e *engine) waitForMore(st *models.RecipeStep, what string, usd float64) {
	more := costs.Round(usd)
	e.waitFor(st, models.RunWaitsForPerson, fmt.Sprintf("Waiting for your OK: %s %s more than you agreed to.", what, costs.Money(more)))
	e.run.WaitingUSD = more
}

// fail ends the run: it couldn't finish, and says why plainly.
func (e *engine) fail(ctx context.Context, why string) {
	now := time.Now().UTC()
	e.run.State, e.run.Problem, e.run.FinishedAt, e.run.Waiting, e.run.WaitingFor, e.run.WaitingUSD = models.RunFailed, why, &now, "", "", 0
	endSample(e.run, now)
	for i := range e.run.Steps {
		if s := &e.run.Steps[i]; s.State == models.StepRunning || s.State == models.StepWaiting {
			s.State, s.Detail, s.Ended = models.StepFailed, why, &now
		}
	}
}

// left is what the person agreed to that is not spent yet, in US dollars;
// limited is false when their preference skips the estimate step (D208:
// budgets, daily limits and project caps still hold).
func (e *engine) left() (float64, bool) {
	ag := e.run.Agreed
	if ag == nil || ag.Skipped {
		return 0, false
	}
	return costs.Round(ag.UpTo - e.run.Spent.USD), true
}

// leftFor is what a step may spend of the agreement: what is left, less what
// the plan said the steps after it cost — their share stays theirs, so a step
// that costs more than planned (or wasn't priced, like a shot for a project
// the run makes) asks for its own part, and the later steps don't ask again.
func (e *engine) leftFor(st *models.RecipeStep) (float64, bool) {
	left, limited := e.left()
	if !limited {
		return 0, false
	}
	later, after := 0.0, false
	for i := range e.run.Steps {
		s := &e.run.Steps[i]
		if s.ID == st.ID {
			after = true
			continue
		}
		if after && s.State != models.StepDone && s.State != models.StepSkipped && s.State != models.StepFailed {
			later += s.Estimate
		}
	}
	return costs.Round(math.Max(0, left-later)), true
}

// spend records money a step spent.
func (e *engine) spend(st *models.RecipeStep, c models.RecipeSpent) {
	st.Spent = addSpent(st.Spent, c)
	e.run.Spent = addSpent(e.run.Spent, c)
}

// plainFailure is an error in plain words.
func plainFailure(err error) string {
	ae := apperr.From(err)
	if ae.Code == apperr.Internal && ae.Unwrap() != nil && ae.Message == ae.Unwrap().Error() {
		return "Something went wrong, and MetaRush couldn't finish the run."
	}
	msg := strings.TrimSpace(ae.Message)
	if msg != "" && !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	if h := strings.TrimSpace(ae.Hint); h != "" {
		msg += " " + h
	}
	return msg
}

// ---- the steps ----

// inputs fetches what the run takes from Google Drive folders (D220), then
// checks that what the run was given is still there.
func (e *engine) inputs(ctx context.Context, st *models.RecipeStep) (bool, error) {
	fetched, err := e.fetchDrive(ctx, st)
	if err != nil {
		return false, err
	}
	var gone []string
	for _, in := range e.run.Inputs {
		for _, it := range in.Items {
			switch in.Kind {
			case models.RecipeFootage:
				var as models.Asset
				if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), it.ID, &as); err != nil || as.Trash != nil || as.State != models.AssetAvailable {
					gone = append(gone, "“"+it.Title+"”")
				}
			case models.RecipePictures:
				var r models.Resource
				if err := e.a.DB.GetByID(ctx, store.CResources, e.a.WorkspaceID(), it.ID, &r); err != nil || r.Version(it.SHA256) == nil {
					gone = append(gone, "“"+it.Title+"”")
				}
			}
		}
	}
	if len(gone) > 0 {
		return false, apperr.New(apperr.Conflict, "%s %s gone since the run was planned.", joinAnd(gone), map[bool]string{true: "is", false: "are"}[len(gone) == 1])
	}
	st.Detail = fmt.Sprintf("%s recorded.", plural(len(e.run.Inputs), "input"))
	if fetched > 0 {
		st.Detail = fmt.Sprintf("%s recorded; %s fetched from %s.", plural(len(e.run.Inputs), "input"), plural(fetched, "file"), pendingServices(e.run.Inputs))
	}
	return true, nil
}

// place makes the run's project, or a new cut in the recipe's project.
func (e *engine) place(ctx context.Context, st *models.RecipeStep) (bool, error) {
	run := e.run
	if run.NewProject && run.ProjectID == "" {
		name := nameWithInputs(run.ProjectName, e.values)
		out, err := ops.CallTyped[ops.ProjectOut](ctx, e.a, "project.create", ops.ProjectCreateIn{Name: name}, ops.Caller{})
		if err != nil {
			return false, err
		}
		run.ProjectID, run.ProjectName, run.Branch = out.Project.ID, out.Project.Name, "main"
		if err := save(ctx, e.a, run); err != nil { // never two projects for one run
			return false, err
		}
		if e.doc.Settings != "" {
			if _, owner, err := content.For(ctx, e.a, run.ProjectID); err == nil && owner != e.doc.Settings {
				if _, err := content.Follow(ctx, e.a, run.ProjectID, e.doc.Settings); err != nil {
					st.Detail = "The recipe's Content Settings couldn't be followed; the project uses the default ones."
				}
			}
		}
		if home := homeProject(e.doc); home != "" {
			if _, err := ops.Call(ctx, e.a, "project.link", mustJSON(ops.ProjectLinkIn{ProjectID: run.ProjectID, From: home}), ops.Caller{}); err != nil {
				return false, err
			}
		}
	}
	var p models.Project
	if err := e.a.DB.GetByID(ctx, store.CProjects, e.a.WorkspaceID(), run.ProjectID, &p); err != nil {
		return false, apperr.New(apperr.NotFound, "The project this run makes its video in is gone.")
	}
	if p.Archived {
		return false, apperr.New(apperr.Conflict, "“%s” was archived while the run was going on.", p.Name)
	}
	if run.Repeats == "" && !run.NewProject {
		if _, taken := p.Branches[run.Branch]; taken {
			run.Branch = freeBranch(&p, e.recipe.Name, run.Number)
		}
	}
	// Footage a run gives joins its project (Claude sees it in Sources).
	for _, in := range run.Inputs {
		if in.Kind != models.RecipeFootage {
			continue
		}
		for _, it := range in.Items {
			if _, err := ops.Call(ctx, e.a, "asset.link", mustJSON(ops.AssetLinkIn{AssetID: it.ID, ProjectID: run.ProjectID}), ops.Caller{}); err != nil {
				return false, err
			}
		}
	}
	// The recipe's rules and sign-offs (D207).
	for _, r := range e.doc.Rules {
		if _, err := ops.AddRule(ctx, e.a, &p, ops.RuleAddIn{ProjectID: p.ID, Text: r.Text, Check: r.Check, Measure: r.Measure, From: "the recipe “" + e.recipe.Title + "”"}); err != nil {
			return false, err
		}
		_ = e.a.DB.GetByID(ctx, store.CProjects, e.a.WorkspaceID(), p.ID, &p)
	}
	signoffs := append([]string{}, e.doc.Signoffs...)
	for _, r := range e.doc.Rules {
		if r.Measure == "" && ops.MeasureFor(r.Text) == "" {
			// A rule the quality check can't measure: a person ticks it (D207).
			signoffs = append(signoffs, shorten("Checked: "+r.Text, 200))
		}
	}
	for _, s := range signoffs {
		if _, _, err := ops.AddSignoff(ctx, e.a, &p, run.Branch, s); err != nil {
			return false, err
		}
		_ = e.a.DB.GetByID(ctx, store.CProjects, e.a.WorkspaceID(), p.ID, &p)
	}
	switch {
	case run.Repeats != "":
		st.Detail = fmt.Sprintf("A new version of the same cut in “%s”.", p.Name)
	case run.NewProject:
		st.Detail = fmt.Sprintf("A new project, “%s”.", p.Name)
	default:
		st.Detail = fmt.Sprintf("A new cut in “%s”.", p.Name)
	}
	run.ProjectName = p.Name
	return true, nil
}

// homeProject is the project a recipe was saved from ("" none).
func homeProject(d *models.RecipeDoc) string {
	if d.From != nil {
		return d.From.ProjectID
	}
	if d.Lands.Kind == models.RecipeLandsProject {
		return d.Lands.ProjectID
	}
	return ""
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// code draws Remotion clips again with the run's words and pictures (D168,
// D209): the pictures as the clip's own, the way remotion.use takes them — no
// new code, no new approval; a render made before with the same words and
// pictures is reused.
func (e *engine) code(ctx context.Context, st *models.RecipeStep) (bool, error) {
	todo := codeClips(e.doc, e.values, e.changed)
	if st.Work == nil {
		st.Work = map[string]string{}
	}
	if e.run.Made == nil {
		e.run.Made = map[string]models.RecipeMadeClip{}
	}
	waiting := 0
	for _, id := range sortedKeys(todo) {
		if _, ok := e.run.Made[id]; ok {
			continue
		}
		cl, _ := clipByID(e.doc, id)
		if cl == nil {
			continue
		}
		cc := todo[id]
		key := st.Work[id]
		if key == "" {
			var pics []remotion.RemotionPictureSet
			for _, name := range sortedKeys(cc.Pictures) {
				pics = append(pics, remotion.RemotionPictureSet{Name: name, Resource: cc.Pictures[name].ID})
			}
			r, err := remotion.ClipWith(ctx, e.a, cl.AssetID, remotion.ClipChange{Words: cc.Words, Pictures: pics})
			if err != nil {
				return false, err
			}
			st.Work[id] = r.Key
			key = r.Key
			if r.JobID != "" {
				st.Jobs = append(st.Jobs, r.JobID)
			}
		}
		r, err := remotion.RenderState(ctx, e.a, key)
		if err != nil {
			return false, err
		}
		switch r.State {
		case models.RemotionRenderDone:
			if r.AssetID == "" {
				waiting++
				continue
			}
			whole := false
			var orig models.Asset
			if e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), cl.AssetID, &orig) == nil {
				whole = cl.In < 0.05 && cl.Out >= orig.Media.Duration-0.1
			}
			e.run.Made[id] = models.RecipeMadeClip{AssetID: r.AssetID, Duration: r.Duration, Whole: whole}
			st.Made = append(st.Made, r.AssetID)
		case models.RemotionRenderFailed:
			return false, apperr.New(apperr.Conflict, "The clip “%s” couldn't be drawn with %s: %s", id, codeWhat(map[string]*codeClip{id: cc}), strings.TrimSuffix(r.Error, ".")+".")
		default:
			waiting++
		}
	}
	if waiting > 0 {
		st.Detail = fmt.Sprintf("Drawing %s with %s.", plural(waiting, "clip"), codeWhat(todo))
		return false, nil
	}
	st.Detail = fmt.Sprintf("%s drawn with %s.", plural(len(todo), "clip"), codeWhat(todo))
	return true, nil
}

// shots makes generated shots again from the run's words, within what the
// person agreed to (D204); budgets and today's limit still stop spending. A
// shot the run's sample made (D294) is used as it is.
func (e *engine) shots(ctx context.Context, st *models.RecipeStep) (bool, error) {
	if st.Work == nil {
		st.Work = map[string]string{}
	}
	if e.run.Made == nil {
		e.run.Made = map[string]models.RecipeMadeClip{}
	}
	waiting := 0
	for _, b := range e.doc.Bindings {
		pb, ok := ParseBinding(b.To)
		if !ok || pb.Kind != models.BindPrompt || !e.changed[b.Input] {
			continue
		}
		if _, ok := e.run.Made[pb.Where]; ok {
			continue
		}
		made, err := e.makeShot(ctx, st, pb.Where, e.values[b.Input].Text)
		if err != nil {
			return false, err
		}
		if e.run.State == models.RunWaiting {
			return false, nil
		}
		if !made {
			waiting++
		}
	}
	if waiting > 0 {
		st.Detail = fmt.Sprintf("Making %s.", plural(waiting, "generated shot"))
		return false, nil
	}
	st.Detail = "The generated shots are made: " + spentWords(st.Spent) + "."
	return true, nil
}

// makeShot makes the generated shot of one of the recipe's clips again from
// words: priced, within what the step may spend, sent once (the ticket is
// saved before it is waited on); true once it is made (run.Made has it). A
// shot that would spend more than agreed, or that a budget refuses, makes the
// run wait for a person.
func (e *engine) makeShot(ctx context.Context, st *models.RecipeStep, id, prompt string) (bool, error) {
	cl, _ := clipByID(e.doc, id)
	if cl == nil {
		return true, nil
	}
	gen := st.Work[id]
	if gen == "" {
		was, err := ops.CallTyped[generate.GenView](ctx, e.a, "generate.show", generate.GenIDIn{ID: cl.AssetID}, ops.Caller{})
		if err != nil {
			return false, err
		}
		g := was.Generation
		req := generate.GenShotIn{ProjectID: e.run.ProjectID, Prompt: prompt, Seconds: g.Params.Seconds, AspectRatio: g.Params.AspectRatio,
			Quality: g.Params.Quality, Resolution: g.Params.Resolution, Model: g.Model, NoPeople: g.NoPeople, Origin: "recipe:" + e.run.ID}
		if g.Look != nil {
			req.Look = g.Look.Name
		}
		est, err := ops.CallTyped[generate.GenEstimate](ctx, e.a, "generate.estimate", req, ops.Caller{})
		if err != nil {
			return false, err
		}
		max := 1e9
		if left, limited := e.leftFor(st); limited {
			if est.USD > left+0.0005 {
				e.waitForMore(st, "the generated shot costs", est.USD-left)
				return false, nil
			}
			max = left
		}
		req.MaxUSD = &max
		out, err := ops.CallTyped[generate.GenRunOut](ctx, e.a, "generate.shot", req, ops.Caller{})
		if err != nil {
			if w := personFix(err); w != "" {
				e.waitFor(st, models.RunWaitsForPerson, w)
				return false, nil
			}
			return false, err
		}
		if len(out.Generations) == 0 {
			return false, apperr.New(apperr.Conflict, "The generated shot for “%s” wasn't started.", id)
		}
		gen = out.Generations[0].ID
		st.Work[id] = gen
		st.Jobs = append(st.Jobs, out.JobIDs...)
		if err := save(ctx, e.a, e.run); err != nil { // never sent twice
			return false, err
		}
	}
	gv, err := ops.CallTyped[generate.GenView](ctx, e.a, "generate.show", generate.GenIDIn{ID: gen}, ops.Caller{})
	if err != nil {
		return false, err
	}
	switch gv.State {
	case "done":
		if len(gv.Clips) == 0 {
			return false, apperr.New(apperr.Conflict, "The generated shot for “%s” came back without a clip.", id)
		}
		if e.run.Made == nil {
			e.run.Made = map[string]models.RecipeMadeClip{}
		}
		e.run.Made[id] = models.RecipeMadeClip{AssetID: gv.Clips[0].AssetID, Duration: gv.Clips[0].Duration, Whole: true}
		e.spend(st, models.RecipeSpent{USD: costs.Round(gv.CostUSD)})
		st.Made = append(st.Made, gv.Clips[0].AssetID)
		return true, nil
	case "failed", "refused", "canceled":
		e.spend(st, models.RecipeSpent{USD: costs.Round(gv.CostUSD)})
		return false, apperr.New(apperr.Conflict, "The generated shot for “%s” couldn't be made: %s", id, strings.TrimSuffix(gv.Summary, ".")+".")
	}
	return false, nil
}

// personFix says what a person must do when spending is refused (a budget,
// today's limit, consent, a key); "" when it isn't that kind of refusal.
func personFix(err error) string {
	ae := apperr.From(err)
	switch ae.Code {
	case apperr.Budget:
		return "Waiting for you: " + strings.TrimSuffix(ae.Message, ".") + ". Raise the budget or the limit in Settings → Integrations, then let the run go on."
	case apperr.ConsentRequired:
		return "Waiting for you: " + strings.TrimSuffix(ae.Message, ".") + ". Record the consent, then let the run go on."
	case apperr.NotConfigured:
		return "Waiting for you: " + strings.TrimSuffix(ae.Message, ".") + ". Set it up in Settings → Integrations, then let the run go on."
	}
	return ""
}

// assemble builds the cut (or makes it again) and saves it as a version.
func (e *engine) assemble(ctx context.Context, st *models.RecipeStep) (bool, error) {
	run := e.run
	var p models.Project
	if err := e.a.DB.GetByID(ctx, store.CProjects, e.a.WorkspaceID(), run.ProjectID, &p); err != nil {
		return false, apperr.New(apperr.NotFound, "The project this run makes its video in is gone.")
	}
	in := assemblyIn{Doc: e.doc, Values: e.values, Changed: e.changed, Made: run.Made, Voice: e.voice, DefaultVoice: e.defaultVoice, Run: runName(run),
		Moments: momentsOf(ctx, e.a, e.doc, e.values, e.changed)}
	head := p.Branches[run.Branch].Head
	var base *models.Revision
	if run.Repeats != "" && head != "" {
		var rev models.Revision
		if err := e.a.DB.GetByID(ctx, store.CRevisions, e.a.WorkspaceID(), head, &rev); err != nil {
			return false, err
		}
		base = &rev
		in.Base = &rev.Spec
	}
	as := assemble(in)
	if len(as.Notes) > 0 {
		st.Detail = strings.Join(as.Notes, " ")
	}
	if run.NewProject {
		// A run's own project sees the recipe's project through a link, but not
		// what that project uses through its own links (links aren't
		// transitive): the footage the cut plays joins the run's project.
		if err := e.joinFootage(ctx, as.Spec); err != nil {
			return false, err
		}
	}
	note := shorten(fmt.Sprintf("Made with the recipe “%s”, run %d", e.recipe.Title, run.Number), 80)
	plan := editspec.Plan{ProjectID: run.ProjectID, Branch: run.Branch, Instruction: fmt.Sprintf("Make %s", runName(run)), Origin: "recipe:" + run.ID, Note: note, Spec: as.Spec}
	if base != nil {
		plan.ProjectID, plan.BaseRevisionID = "", base.ID
	} else if head != "" {
		// A cut of that name exists (made by this run before it was saved): build on it.
		plan.ProjectID, plan.BaseRevisionID = "", head
	}
	saved, err := saveCut(ctx, e.a, plan)
	if err != nil {
		return false, err
	}
	run.Revision, run.RevisionNumber, run.StandIns = saved.Revision.ID, saved.Revision.Number, as.StandIns
	st.Made = append(st.Made, saved.Revision.ID)
	if base == nil {
		title := shorten(fmt.Sprintf("%s — run %d", e.recipe.Title, run.Number), 120)
		if run.NewProject {
			title = e.recipe.Title
		}
		_, _ = ops.Call(ctx, e.a, "cut.rename", mustJSON(ops.CutRenameIn{ProjectID: run.ProjectID, Branch: run.Branch, Title: title}), ops.Caller{})
	}
	if st.Detail == "" {
		st.Detail = fmt.Sprintf("Version %d of the cut: the fixed sections built and locked.", saved.Revision.Number)
	}
	return true, nil
}

// joinFootage adds the footage a cut plays (clips, sound tracks, narration
// takes) to the run's project.
func (e *engine) joinFootage(ctx context.Context, s *editspec.Spec) error {
	seen := map[string]bool{}
	var ids []string
	add := func(id string) {
		if strings.HasPrefix(id, "ast_") && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, c := range s.Clips {
		add(c.AssetID)
	}
	for _, t := range s.Audio {
		add(t.AssetID)
	}
	if v := s.Voiceover; v != nil {
		for _, b := range v.Beats {
			if b.Take != nil {
				add(b.Take.AssetID)
			}
		}
	}
	var have []models.Asset
	if err := e.a.DB.Find(ctx, store.CAssets, e.a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}, "projectIds": e.run.ProjectID}, &have,
		options.Find().SetProjection(bson.M{"_id": 1})); err != nil {
		return err
	}
	in := map[string]bool{}
	for _, as := range have {
		in[as.ID] = true
	}
	for _, id := range ids {
		if in[id] {
			continue
		}
		if _, err := ops.Call(ctx, e.a, "asset.link", mustJSON(ops.AssetLinkIn{AssetID: id, ProjectID: e.run.ProjectID}), ops.Caller{}); err != nil {
			return err
		}
	}
	return nil
}

// saveCut checks a cut and saves it as a new version with its own confirm
// token; the run may change the parts it locked itself.
func saveCut(ctx context.Context, a *app.App, plan editspec.Plan) (*ops.EditApplyOut, error) {
	raw := mustJSON(plan)
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, a, "edit.check", ops.EditCheckIn{Plan: raw, AllowUnlock: true}, ops.Caller{})
	if err != nil {
		return nil, err
	}
	if !chk.Valid {
		msg := "The cut the recipe makes didn't pass its check."
		if len(chk.Errors) > 0 {
			msg = "The cut the recipe makes didn't pass its check: " + strings.TrimSuffix(chk.Errors[0].Message, ".") + "."
		}
		return nil, apperr.New(apperr.Validation, "%s", msg).WithDetails(map[string]any{"errors": chk.Errors})
	}
	return ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: chk.ConfirmToken, AllowUnlock: true}, ops.Caller{})
}

// headOf is the newest version of the run's cut.
func (e *engine) headOf(ctx context.Context) (*models.Revision, error) {
	var p models.Project
	if err := e.a.DB.GetByID(ctx, store.CProjects, e.a.WorkspaceID(), e.run.ProjectID, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "The project this run makes its video in is gone.")
	}
	head := p.Branches[e.run.Branch].Head
	if head == "" {
		return nil, apperr.New(apperr.NotFound, "The cut this run made is gone.")
	}
	var rev models.Revision
	if err := e.a.DB.GetByID(ctx, store.CRevisions, e.a.WorkspaceID(), head, &rev); err != nil {
		return nil, err
	}
	return &rev, nil
}

// voiceStep makes the AI voice for the lines that need it, within what the
// person agreed to; unchanged lines are reused for free (the line a sample
// made, D294, among them).
func (e *engine) voiceStep(ctx context.Context, st *models.RecipeStep) (bool, error) {
	rev, err := e.headOf(ctx)
	if err != nil {
		return false, err
	}
	out, done, err := e.makeVoice(ctx, st, rev)
	if err != nil || !done {
		return false, err
	}
	if out == nil {
		st.Detail = "Every line already has its voice."
		return true, nil
	}
	e.run.Revision, e.run.RevisionNumber = out.Applied.RevisionID, out.Applied.Number
	st.Made = append(st.Made, out.Applied.RevisionID)
	st.Detail = fmt.Sprintf("%s made, %s reused: %s.", plural(out.Generated, "line"), plural(out.Reused, "line"), spentWords(st.Spent))
	return true, nil
}

// makeVoice makes the AI voice of a version's lines that need it, priced
// first and within what the step may spend, and puts it in the cut (a new
// version, out.Applied). It reports done false while the run waits for a
// person (a budget, the agreement), and a nil out when every line has its voice.
func (e *engine) makeVoice(ctx context.Context, st *models.RecipeStep, rev *models.Revision) (*voiceover.GenerateOut, bool, error) {
	need := false
	if v := rev.Spec.Voiceover; v != nil {
		for _, b := range v.Beats {
			if b.Take == nil || b.Take.Source == models.TakeScratch {
				need = true
			}
		}
	}
	if !need {
		return nil, true, nil
	}
	est, err := ops.CallTyped[voiceover.EstimateOut](ctx, e.a, "voiceover.estimate", voiceover.TTSIn{RevisionID: rev.ID}, ops.Caller{})
	if err != nil {
		if w := personFix(err); w != "" {
			e.waitFor(st, models.RunWaitsForPerson, w)
			return nil, false, nil
		}
		return nil, false, err
	}
	if !est.Allowed {
		e.waitFor(st, models.RunWaitsForPerson, "Waiting for you: "+strings.Join(est.Warnings, " ")+" Raise the budget in Settings → Integrations, then let the run go on.")
		return nil, false, nil
	}
	if left, limited := e.leftFor(st); limited && est.Cost.USD > left+0.0005 {
		e.waitForMore(st, "the AI voice costs", est.Cost.USD-left)
		return nil, false, nil
	}
	out, err := ops.CallTyped[voiceover.GenerateOut](ctx, e.a, "voiceover.tts", voiceover.TTSIn{RevisionID: rev.ID, Apply: true}, ops.Caller{})
	if err != nil {
		if w := personFix(err); w != "" {
			e.waitFor(st, models.RunWaitsForPerson, w)
			return nil, false, nil
		}
		return nil, false, err
	}
	if out.Credits > 0 {
		e.spend(st, models.RecipeSpent{USD: providers.SpeechUSD(out.Credits), Credits: roundCredits(out.Credits)})
	}
	if out.JobID != "" {
		st.Jobs = append(st.Jobs, out.JobID)
	}
	if out.Applied == nil || out.Applied.RevisionID == "" {
		why := "the new voice couldn't be put in the cut"
		if out.Applied != nil && out.Applied.Problem != "" {
			why = strings.TrimSuffix(out.Applied.Problem, ".")
		}
		return nil, false, apperr.New(apperr.Conflict, "The AI voice was made, but %s (the audio is kept).", why)
	}
	return out, true, nil
}

// claude waits until Claude (or a person) has chosen the moments: the
// stand-ins of the chosen sections replaced through a cut sheet, and the first
// picks (D215) of the sections and of the pack's shorts kept or changed.
func (e *engine) claude(ctx context.Context, st *models.RecipeStep) (bool, error) {
	shorts := picksLeft(e.run.Outputs)
	if len(e.run.StandIns) == 0 && len(e.run.Outputs) == 0 {
		st.State, st.Detail = models.StepSkipped, "Nothing for Claude to choose: the same footage as before."
		return true, nil
	}
	var left []models.RecipeStandIn
	if len(e.run.StandIns) > 0 {
		rev, err := e.headOf(ctx)
		if err != nil {
			return false, err
		}
		left = standInsLeft(e.run.StandIns, &rev.Spec)
		if len(left) == 0 {
			e.run.Revision, e.run.RevisionNumber = rev.ID, rev.Number
		}
	}
	if len(left) == 0 && shorts == 0 {
		switch {
		case len(e.run.StandIns) == 0:
			st.Detail = "The shorts' stretches are chosen."
		case len(e.run.Outputs) > 0:
			st.Detail = fmt.Sprintf("The moments are chosen (version %d), and the shorts' stretches.", e.run.RevisionNumber)
		default:
			st.Detail = fmt.Sprintf("The moments are chosen (version %d).", e.run.RevisionNumber)
		}
		return true, nil
	}
	e.waitFor(st, models.RunWaitsForClaude, claudeWaiting(left, shorts))
	return false, nil
}

// claudeWaiting says what a run waits for Claude to do: choose the moments of
// a section (stand-ins), look at the first picks of one (keep or change them),
// look at the shorts' first picks.
func claudeWaiting(left []models.RecipeStandIn, shorts int) string {
	var choose, look []string
	seen := map[string]bool{}
	for _, si := range left {
		if seen[si.Section] {
			continue
		}
		seen[si.Section] = true
		if si.First {
			look = append(look, "“"+si.Section+"”")
		} else {
			choose = append(choose, "“"+si.Section+"”")
		}
	}
	var parts []string
	if len(choose) > 0 {
		parts = append(parts, "choose the moments of "+joinAnd(choose))
	}
	if len(look) > 0 || shorts > 0 {
		what := "the first picks for " + joinAnd(look)
		switch {
		case len(look) == 0:
			what = "the shorts' first picks"
		case shorts > 0:
			what += " and the shorts"
		}
		parts = append(parts, "look at "+what+" (keep or change them)")
	}
	return "Waiting for Claude to " + joinAnd(parts) + "."
}

// picksLeft counts the shorts whose stretch is still MetaRush's first pick.
func picksLeft(outs []models.RecipeRunOutput) int {
	n := 0
	for _, o := range outs {
		if o.Pick == models.PickFirst && o.State == models.OutputPlanned {
			n++
		}
	}
	return n
}

// standInsLeft are the stand-ins and first picks still in a cut as MetaRush
// put them and not kept by anyone.
func standInsLeft(ins []models.RecipeStandIn, s *editspec.Spec) []models.RecipeStandIn {
	var out []models.RecipeStandIn
	for _, si := range ins {
		if si.Kept {
			continue
		}
		if standInAsPut(si, s) {
			out = append(out, si)
		}
	}
	return out
}

// standInAsPut reports whether a stand-in's clip is still in a cut as MetaRush put it.
func standInAsPut(si models.RecipeStandIn, s *editspec.Spec) bool {
	for _, c := range s.Clips {
		if c.ID == si.ClipID && c.AssetID == si.AssetID && math.Abs(c.In-si.In) < 0.001 && math.Abs(c.Out-si.Out) < 0.001 {
			return true
		}
	}
	return false
}

// review makes a review copy of the newest version and waits for its quality check.
func (e *engine) review(ctx context.Context, st *models.RecipeStep) (bool, error) {
	if st.Work == nil {
		st.Work = map[string]string{}
	}
	if st.Work["render"] == "" {
		rev, err := e.headOf(ctx)
		if err != nil {
			return false, err
		}
		e.run.Revision, e.run.RevisionNumber = rev.ID, rev.Number
		out, err := ops.CallTyped[ops.RenderOut](ctx, e.a, "render.submit", ops.RenderSubmitIn{RevisionID: rev.ID, Quality: "review"}, ops.Caller{})
		if err != nil {
			return false, err
		}
		st.Work["render"] = out.Render.ID
		e.run.Render = out.Render.ID
		if out.Job != nil {
			st.Jobs = append(st.Jobs, out.Job.ID)
		} else if out.Render.JobID != "" {
			st.Jobs = append(st.Jobs, out.Render.JobID)
		}
	}
	rc, err := e.renderChecked(ctx, st, st.Work["render"])
	if err != nil {
		return false, err
	}
	st.Detail = rc.detail
	if !rc.done {
		return false, nil
	}
	e.run.QA = rc.qa
	return true, nil
}

// renderCheck is where a review copy stands: done once it is ready and its
// quality check ran (or never came); qa says what the check found, detail
// what is going on.
type renderCheck struct {
	done   bool
	qa     string
	detail string
}

// renderChecked looks at a review copy and its quality check.
func (e *engine) renderChecked(ctx context.Context, st *models.RecipeStep, renderID string) (renderCheck, error) {
	var r models.Render
	if err := e.a.DB.GetByID(ctx, store.CRenders, e.a.WorkspaceID(), renderID, &r); err != nil {
		return renderCheck{}, apperr.New(apperr.NotFound, "The review copy is gone.")
	}
	switch r.State {
	case models.RenderFailed, models.RenderCanceled:
		why := strings.TrimSpace(r.Error)
		if why == "" {
			why = "it was stopped"
		}
		return renderCheck{}, apperr.New(apperr.Conflict, "The review copy couldn't be made: %s", strings.TrimSuffix(why, ".")+".")
	case models.RenderAvailable:
	default:
		return renderCheck{detail: "Making the review copy."}, nil
	}
	if r.QA != nil {
		q := qaWords(r.QA)
		return renderCheck{done: true, qa: q, detail: "The review copy is ready; the quality check " + q + "."}, nil
	}
	var qa models.Job
	err := e.a.DB.C(store.CJobs).FindOne(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "type": jobs.TypeQA, "payload.renderId": r.ID},
		options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}})).Decode(&qa)
	if err == nil {
		if !containsString(st.Jobs, qa.ID) {
			st.Jobs = append(st.Jobs, qa.ID)
		}
		switch qa.State {
		case models.JobSucceeded:
			q := "ran"
			var again models.Render
			if e.a.DB.GetByID(ctx, store.CRenders, e.a.WorkspaceID(), r.ID, &again) == nil && again.QA != nil {
				q = qaWords(again.QA)
			}
			return renderCheck{done: true, qa: q, detail: "The review copy is ready; the quality check " + q + "."}, nil
		case models.JobFailed, models.JobCanceled:
			return renderCheck{done: true, qa: "couldn't run", detail: "The review copy is ready; its quality check couldn't run."}, nil
		}
		return renderCheck{detail: "Checking the review copy."}, nil
	}
	if r.CompletedAt != nil && time.Since(*r.CompletedAt) > qaGrace {
		return renderCheck{done: true, qa: "didn't run", detail: "The review copy is ready (no quality check ran)."}, nil
	}
	return renderCheck{detail: "Checking the review copy."}, nil
}

// qaWords says what a quality check found ("passed", "found 2 things to look at").
func qaWords(q *models.RenderQA) string {
	switch {
	case q.Errors > 0:
		return fmt.Sprintf("found %s", plural(q.Errors, "problem"))
	case q.Warnings > 0:
		return fmt.Sprintf("found %s to look at", plural(q.Warnings, "thing"))
	}
	return "passed"
}

// finish asks for review: the newest version waits in "Awaiting your review",
// and each short of the pack with it (D214).
func (e *engine) finish(ctx context.Context, st *models.RecipeStep) (bool, error) {
	rev, err := e.headOf(ctx)
	if err != nil {
		return false, err
	}
	if _, err := ops.CallTyped[ops.RevisionSummary](ctx, e.a, "revision.askReview", ops.AskReviewIn{ID: rev.ID}, ops.Caller{}); err != nil {
		return false, err
	}
	e.run.Revision, e.run.RevisionNumber = rev.ID, rev.Number
	st.Detail = fmt.Sprintf("Version %d waits in Awaiting your review.", rev.Number)
	shorts := 0
	for i := range e.run.Outputs {
		o := &e.run.Outputs[i]
		if o.State != models.OutputReady || o.Revision == "" {
			continue
		}
		if _, err := ops.CallTyped[ops.RevisionSummary](ctx, e.a, "revision.askReview", ops.AskReviewIn{ID: o.Revision}, ops.Caller{}); err == nil {
			shorts++
		}
	}
	if shorts > 0 {
		st.Detail = fmt.Sprintf("Version %d and %s wait in Awaiting your review.", rev.Number, plural(shorts, "short"))
	}
	return true, nil
}

// finished ends a run that did every step.
func (e *engine) finished(ctx context.Context) {
	now := time.Now().UTC()
	e.run.State, e.run.FinishedAt, e.run.Waiting, e.run.WaitingFor = models.RunDone, &now, "", ""
	set := bson.M{"lastRun": models.RecipeRunRef{ID: e.run.ID, Number: e.run.Number, State: models.RunDone, At: now, By: e.run.CreatedBy}}
	if e.recipe.Example == nil && e.run.Render != "" {
		set["example"] = models.RecipeExample{RenderID: e.run.Render, ProjectID: e.run.ProjectID, RevisionID: e.run.Revision, RunID: e.run.ID}
	}
	_, _ = e.a.DB.C(store.CRecipes).UpdateOne(ctx, bson.M{"_id": e.recipe.ID, "workspaceId": e.a.WorkspaceID()}, bson.M{"$set": set})
}
