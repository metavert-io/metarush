package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Keeping or changing first picks (recipe.picks, D215): Claude — or a person —
// looks at what MetaRush picked and keeps it, or changes it: a chosen
// section's moment (its clip in the run's cut: other footage, other times) or
// a short's stretch (from and to on the conversation's clock), or drops one. A
// run waiting for Claude goes on by itself once every pick is kept or changed.

// RecipePicksIn keeps or changes a run's first picks.
type RecipePicksIn struct {
	Run string `json:"run" jsonschema:"minLength=1,description=The run (its ID or recipe#number)."`
	// Keep keeps every pick and stand-in that is still as MetaRush made it.
	Keep bool `json:"keep,omitempty" jsonschema:"description=Keep every pick still as MetaRush made it (after the changes in set)."`
	// Set changes picks.
	Set []RecipePickChange `json:"set,omitempty" jsonschema:"maxItems=40"`
}

// RecipePickChange changes one pick: a chosen section's moment (its footage,
// in and out) or a short's stretch (from and to on its conversation's clock);
// one more than a part has adds a pick, drop takes one out.
type RecipePickChange struct {
	Part string `json:"part" jsonschema:"minLength=1,description=The chosen section or the pack output (its name)."`
	N    int    `json:"n" jsonschema:"minimum=1,description=Which of its picks (1\\, 2…); one more than it has adds one."`
	// From and To: a moment's in and out in its footage, or a short's stretch
	// on the conversation's clock (seconds into its clip for a short of footage).
	From *float64 `json:"from,omitempty" jsonschema:"minimum=0"`
	To   *float64 `json:"to,omitempty" jsonschema:"minimum=0"`
	// Footage is a moment's clip, or a short of footage's (D295) — its ID or
	// name; default the pick's.
	Footage string `json:"footage,omitempty"`
	Why     string `json:"why,omitempty" jsonschema:"maxLength=300,description=Why this moment (kept with it)."`
	Drop    bool   `json:"drop,omitempty"`
}

// RecipePicksOut is the run after the picks were kept or changed.
type RecipePicksOut struct {
	Run     RecipeRunView `json:"run"`
	Changes []string      `json:"changes"`
	Message string        `json:"message"`
}

func init() {
	ops.Def("recipe.picks", "Keep or change a run's first picks: the moments MetaRush picked for its chosen sections (their clips in the run's cut) and the stretches of its pack's shorts, or drop one; the run goes on once every pick is kept or changed", ops.Flags{Mutating: true}, keepPicks)
}

func keepPicks(ctx context.Context, a *app.App, in *RecipePicksIn) (*RecipePicksOut, error) {
	if !in.Keep && len(in.Set) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "Say which picks to keep or change.").WithHint("Keep them all, or change one: its part, its number and its new times.")
	}
	ref, err := loadRun(ctx, a, in.Run)
	if err != nil {
		return nil, err
	}
	switch ref.State {
	case models.RunDone, models.RunStopped, models.RunFailed:
		return nil, apperr.New(apperr.Conflict, "%s already ended (%s); change its cut or its shorts' own cuts instead.", upperFirst(runName(ref)), strings.ToLower(stateWords(ref)))
	}
	// The run's lease, so the engine and this change never cross.
	var run *models.RecipeRun
	for i := 0; i < 20; i++ {
		if run, err = claim(ctx, a, ref.ID); err != nil {
			return nil, err
		}
		if run != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if run == nil {
		return nil, apperr.New(apperr.Conflict, "%s is busy right now; try again in a moment.", upperFirst(runName(ref)))
	}
	defer release(ctx, a, run)
	var v models.RecipeVersion
	if err := a.DB.GetByID(ctx, store.CRecipeVersions, a.WorkspaceID(), versionID(a.WorkspaceID(), run.Version), &v); err != nil {
		return nil, err
	}
	var changes []string
	var editOps []editspec.Op
	var head *models.Revision
	loadHead := func() (*models.Revision, error) {
		if head != nil {
			return head, nil
		}
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), run.ProjectID, &p); err != nil {
			return nil, apperr.New(apperr.NotFound, "The project this run makes its video in is gone.")
		}
		var rev models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), p.Branches[run.Branch].Head, &rev); err != nil {
			return nil, apperr.New(apperr.NotFound, "The cut this run made is gone.")
		}
		head = &rev
		return head, nil
	}
	for _, ch := range in.Set {
		if sec := sectionOf(&v.Doc, ch.Part); sec != nil {
			rev, err := loadHead()
			if err != nil {
				return nil, err
			}
			op, words, err := momentChange(ctx, a, run, sec, rev, ch)
			if err != nil {
				return nil, err
			}
			editOps = append(editOps, op...)
			changes = append(changes, words)
			continue
		}
		if outputOf(&v.Doc, ch.Part) == nil {
			return nil, apperr.New(apperr.NotFound, "The recipe has no chosen section or pack output called “%s”.", ch.Part)
		}
		words, err := shortChange(ctx, a, run, outputOf(&v.Doc, ch.Part), ch)
		if err != nil {
			return nil, err
		}
		changes = append(changes, words)
	}
	if len(editOps) > 0 {
		rev, _ := loadHead()
		plan := editspec.Plan{BaseRevisionID: rev.ID, Branch: run.Branch, Instruction: "Choose the moments for " + runName(run), Origin: "recipe:" + run.ID,
			Note: shorten("Moments chosen for "+runName(run), 80), Ops: editOps}
		saved, err := saveCut(ctx, a, plan)
		if err != nil {
			return nil, err
		}
		run.Revision, run.RevisionNumber = saved.Revision.ID, saved.Revision.Number
		head = nil
	}
	if in.Keep {
		kept := 0
		if len(run.StandIns) > 0 {
			rev, err := loadHead()
			if err != nil {
				return nil, err
			}
			for i := range run.StandIns {
				si := &run.StandIns[i]
				if !si.Kept && standInAsPut(*si, &rev.Spec) {
					si.Kept, si.KeptBy = true, &a.Actor
					kept++
				}
			}
		}
		for i := range run.Outputs {
			o := &run.Outputs[i]
			if o.Pick == models.PickFirst && o.State == models.OutputPlanned {
				o.Pick, o.PickBy = models.PickKept, &a.Actor
				kept++
			}
		}
		if kept > 0 {
			changes = append(changes, "Kept "+plural(kept, "pick")+" as MetaRush made them.")
		}
	}
	if len(changes) == 0 {
		changes = append(changes, "Every pick was already kept or changed.")
	}
	if err := save(ctx, a, run); err != nil {
		return nil, err
	}
	a.Audit(ctx, "recipe.picks", "recipe", run.RecipeID, run.ProjectID, map[string]any{"run": run.Number, "keep": in.Keep, "changes": len(in.Set)})
	release(ctx, a, run)
	fresh, err := getRun(ctx, a, run.ID)
	if err != nil {
		return nil, err
	}
	if fresh.State == models.RunWaiting && fresh.WaitingFor == models.RunWaitsForClaude {
		fresh = advanceNow(ctx, a, fresh)
	}
	view := runView(ctx, a, fresh)
	msg := upperFirst(runName(fresh)) + ": " + strings.Join(changes, " ")
	if fresh.State != models.RunWaiting || fresh.WaitingFor != models.RunWaitsForClaude {
		msg += " The run goes on."
	}
	return &RecipePicksOut{Run: *view, Changes: changes, Message: msg}, nil
}

// sectionOf finds a chosen section of a document by name.
func sectionOf(d *models.RecipeDoc, name string) *models.RecipeSection {
	for i := range d.Sections {
		if d.Sections[i].Name == name && d.Sections[i].Kind == models.RecipeChosen {
			return &d.Sections[i]
		}
	}
	return nil
}

// momentChange is the change to the run's cut that changes, adds or drops one
// of a chosen section's moments.
func momentChange(ctx context.Context, a *app.App, run *models.RecipeRun, sec *models.RecipeSection, rev *models.Revision, ch RecipePickChange) ([]editspec.Op, string, error) {
	var mine []models.RecipeStandIn
	for _, si := range run.StandIns {
		if si.Section == sec.Name {
			mine = append(mine, si)
		}
	}
	clipAt := func(id string) *editspec.Clip {
		for i := range rev.Spec.Clips {
			if rev.Spec.Clips[i].ID == id {
				return &rev.Spec.Clips[i]
			}
		}
		return nil
	}
	switch {
	case ch.N >= 1 && ch.N <= len(mine):
		si := mine[ch.N-1]
		cl := clipAt(si.ClipID)
		if cl == nil {
			return nil, "", apperr.New(apperr.Conflict, "Moment %d of “%s” isn't in the cut any more.", ch.N, sec.Name)
		}
		if ch.Drop {
			return []editspec.Op{{Op: "removeClip", ID: cl.ID}}, fmt.Sprintf("Took moment %d out of “%s”.", ch.N, sec.Name), nil
		}
		set, words, err := momentSet(ctx, a, run, cl.AssetID, ch)
		if err != nil {
			return nil, "", err
		}
		return []editspec.Op{{Op: "updateClip", ID: cl.ID, Set: set}}, fmt.Sprintf("Moment %d of “%s” is now %s.", ch.N, sec.Name, words), nil
	case ch.N == len(mine)+1 && !ch.Drop:
		after := ""
		for _, si := range mine {
			if clipAt(si.ClipID) != nil {
				after = si.ClipID
			}
		}
		if after == "" {
			return nil, "", apperr.New(apperr.Conflict, "“%s” has no moment left in the cut to add one after.", sec.Name)
		}
		footage := ""
		if len(mine) > 0 {
			footage = mine[len(mine)-1].AssetID
		}
		set, words, err := momentSet(ctx, a, run, footage, ch)
		if err != nil {
			return nil, "", err
		}
		var c editspec.Clip
		_ = json.Unmarshal(set, &c)
		taken := map[string]bool{}
		for _, x := range rev.Spec.Clips {
			taken[x.ID] = true
		}
		c.ID = fmt.Sprintf("%s-%d", sec.Name, ch.N)
		for x := 2; taken[c.ID]; x++ {
			c.ID = fmt.Sprintf("%s-%d-%d", sec.Name, ch.N, x)
		}
		return []editspec.Op{{Op: "insertClip", After: after, Clip: &c}}, fmt.Sprintf("Added moment %d to “%s”: %s.", ch.N, sec.Name, words), nil
	}
	return nil, "", apperr.New(apperr.InvalidInput, "“%s” has %s; say one of them, or %d to add one.", sec.Name, plural(len(mine), "pick"), len(mine)+1)
}

// momentSet is what a changed moment is: its footage (the run's, by ID or
// name), its in and out, and why.
func momentSet(ctx context.Context, a *app.App, run *models.RecipeRun, footage string, ch RecipePickChange) (json.RawMessage, string, error) {
	if ch.From == nil || ch.To == nil || *ch.To-*ch.From < 0.5 {
		return nil, "", apperr.New(apperr.InvalidInput, "Give the moment's start and end (from and to, seconds into the footage), at least half a second apart.")
	}
	id := footage
	if ch.Footage != "" {
		id = ""
		for _, in := range run.Inputs {
			for _, it := range in.Items {
				if it.ID == ch.Footage || strings.EqualFold(it.Title, ch.Footage) {
					id = it.ID
				}
			}
		}
		if id == "" {
			as, err := ops.ResolveFootage(ctx, a, ch.Footage, run.ProjectID)
			if err != nil {
				return nil, "", apperr.New(apperr.NotFound, "There's no clip “%s” the run can use.", ch.Footage)
			}
			id = as.ID
		}
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
		return nil, "", apperr.New(apperr.NotFound, "There's no clip “%s” the run can use.", ch.Footage)
	}
	if as.Media.Duration > 0 && *ch.To > as.Media.Duration+0.05 {
		return nil, "", apperr.New(apperr.InvalidInput, "“%s” is only %s long.", as.DisplayTitle(), seconds(as.Media.Duration))
	}
	// The first pick's reason, words and note go with it (a merge patch: null takes a field out).
	m := map[string]any{"assetId": id, "in": round3(*ch.From), "out": round3(*ch.To), "label": "Chosen", "rationale": nil, "evidence": nil, "notes": nil}
	if w := strings.TrimSpace(ch.Why); w != "" {
		m["rationale"], m["label"] = shorten(w, 2000), shorten("Chosen: "+w, 120)
	}
	b, _ := json.Marshal(m)
	return b, fmt.Sprintf("%s–%s of “%s”", editspec.Clock(*ch.From), editspec.Clock(*ch.To), as.DisplayTitle()), nil
}

// shortChange changes, adds or drops one of the pack's shorts before it is made.
func shortChange(ctx context.Context, a *app.App, run *models.RecipeRun, out *models.RecipeOutput, ch RecipePickChange) (string, error) {
	var mine []int
	for i, o := range run.Outputs {
		if o.Output == ch.Part {
			mine = append(mine, i)
		}
	}
	if ch.N == len(mine)+1 && !ch.Drop {
		var o models.RecipeRunOutput
		switch {
		case len(mine) > 0:
			last := run.Outputs[mine[len(mine)-1]]
			o = models.RecipeRunOutput{Output: ch.Part, N: ch.N, Kind: last.Kind, Conversation: last.Conversation, ConversationName: last.ConversationName,
				AssetID: last.AssetID, FootageName: last.FootageName, State: models.OutputPlanned}
		case out != nil && out.Footage != "":
			// A short of footage (D295) the first picks found none of: the run's clip it names.
			o = models.RecipeRunOutput{Output: ch.Part, N: ch.N, Kind: out.Kind, State: models.OutputPlanned}
			for _, in := range run.Inputs {
				if in.Name == out.Footage && len(in.Items) > 0 {
					o.AssetID, o.FootageName = in.Items[0].ID, in.Items[0].Title
				}
			}
			if o.AssetID == "" {
				return "", apperr.New(apperr.Conflict, "The pack's “%s” has no footage in this run to make a short of.", ch.Part)
			}
		default:
			return "", apperr.New(apperr.Conflict, "The pack's “%s” has no shorts in this run to add to.", ch.Part)
		}
		if err := setStretch(ctx, a, run, &o, ch); err != nil {
			return "", err
		}
		run.Outputs = append(run.Outputs, o)
		sortOutputs(run.Outputs)
		return fmt.Sprintf("Added short %d: %s–%s.", ch.N, editspec.Clock(o.From), editspec.Clock(o.To)), nil
	}
	if ch.N < 1 || ch.N > len(mine) {
		return "", apperr.New(apperr.InvalidInput, "The pack's “%s” has %s in this run; say one of them, or %d to add one.", ch.Part, plural(len(mine), "short"), len(mine)+1)
	}
	o := &run.Outputs[mine[ch.N-1]]
	if o.State != models.OutputPlanned {
		return "", apperr.New(apperr.Conflict, "Short %d is made already (its own project, “%s”); change its cut there.", ch.N, o.ProjectName)
	}
	if ch.Drop {
		o.Pick, o.PickBy, o.State = models.PickDropped, &a.Actor, models.OutputDropped
		return fmt.Sprintf("Dropped short %d: it isn't made.", ch.N), nil
	}
	if err := setStretch(ctx, a, run, o, ch); err != nil {
		return "", err
	}
	return fmt.Sprintf("Short %d is now %s–%s.", ch.N, editspec.Clock(o.From), editspec.Clock(o.To)), nil
}

// setStretch gives a short a new stretch of its conversation — or, for a
// short of footage (D295), of the run's footage (another of its clips with footage).
func setStretch(ctx context.Context, a *app.App, run *models.RecipeRun, o *models.RecipeRunOutput, ch RecipePickChange) error {
	if o.AssetID != "" {
		return setFootageStretch(ctx, a, run, o, ch)
	}
	if ch.From == nil || ch.To == nil || *ch.To-*ch.From < 2 {
		return apperr.New(apperr.InvalidInput, "Give the short's stretch (from and to, on the conversation's clock), at least 2 seconds long.")
	}
	var c models.Conversation
	if err := a.DB.GetByID(ctx, store.CConversations, a.WorkspaceID(), o.Conversation, &c); err != nil {
		return apperr.New(apperr.NotFound, "The conversation the shorts come from is gone.")
	}
	if c.Length > 0 && *ch.To > c.Length+0.5 {
		return apperr.New(apperr.InvalidInput, "The conversation is only %s long.", seconds(c.Length))
	}
	o.From, o.To = round3(*ch.From), round3(*ch.To)
	o.Pick, o.PickBy, o.First = models.PickChanged, &a.Actor, ""
	o.Why = nil
	if w := strings.TrimSpace(ch.Why); w != "" {
		o.Why = []string{shorten(w, 300)}
	}
	return nil
}

// setFootageStretch gives a short of footage (D295) a new stretch: seconds
// into its clip, or into another clip the run was given (footage).
func setFootageStretch(ctx context.Context, a *app.App, run *models.RecipeRun, o *models.RecipeRunOutput, ch RecipePickChange) error {
	if ch.From == nil || ch.To == nil || *ch.To-*ch.From < 2 {
		return apperr.New(apperr.InvalidInput, "Give the short's stretch (from and to, seconds into its clip), at least 2 seconds long.")
	}
	if *ch.To-*ch.From > ops.ShortMaxSeconds {
		return apperr.New(apperr.InvalidInput, "A short is at most 3 minutes long.")
	}
	id, title := o.AssetID, o.FootageName
	if f := strings.TrimSpace(ch.Footage); f != "" {
		id = ""
		for _, in := range run.Inputs {
			if in.Kind != models.RecipeFootage {
				continue
			}
			for _, it := range in.Items {
				if it.ID == f || strings.EqualFold(it.Title, f) {
					id, title = it.ID, it.Title
				}
			}
		}
		if id == "" {
			return apperr.New(apperr.NotFound, "The run has no clip “%s” to make a short of.", f)
		}
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
		return apperr.New(apperr.NotFound, "The clip the short comes from is gone.")
	}
	if as.Media.Duration > 0 && *ch.To > as.Media.Duration+0.05 {
		return apperr.New(apperr.InvalidInput, "“%s” is only %s long.", as.DisplayTitle(), seconds(as.Media.Duration))
	}
	o.AssetID, o.FootageName = id, title
	o.From, o.To = round3(*ch.From), round3(*ch.To)
	o.Pick, o.PickBy, o.First = models.PickChanged, &a.Actor, ""
	o.Why = nil
	if w := strings.TrimSpace(ch.Why); w != "" {
		o.Why = []string{shorten(w, 300)}
	}
	return nil
}
