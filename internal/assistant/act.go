package assistant

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// maxActAtOnce bounds one decision (Approve all).
const maxActAtOnce = 30

// Act runs, dismisses or answers cards for a person: each op runs with the
// caller's own app (its channel and actor), never on the assistant's channel.
// Several cards decided at once (Approve all) run in order, approvals first,
// and Claude hears about all of them in one note — once the work they started
// (a render, a snapshot, an import) has finished (D142, D144).
func (m *Manager) Act(ctx context.Context, a *app.App, caller ops.Caller, in ConversationActIn) (*ConversationActOut, error) {
	c, err := m.live(ctx, in.ID, true)
	if err != nil {
		return nil, err
	}
	want := actIDs(in)
	switch {
	case len(want) == 0:
		return nil, apperr.New(apperr.InvalidInput, "Say which card.")
	case len(want) > maxActAtOnce:
		return nil, apperr.New(apperr.InvalidInput, "That's more cards than one decision can take (%d at most).", maxActAtOnce)
	}
	cards, err := c.cardsFor(want)
	if err != nil {
		return nil, err
	}
	for _, act := range cards {
		if act.State != ActionPending {
			return nil, apperr.New(apperr.Conflict, "%s", decidedAlready(act.State)).WithDetails(map[string]any{"state": act.State, "actionId": act.ID})
		}
	}
	if in.NotQuite != "" {
		note, err := checkNotQuite(in, cards)
		if err != nil {
			return nil, err
		}
		return m.notQuite(ctx, a, c, cards, note)
	}
	if in.Answer != nil {
		if len(cards) != 1 || cards[0].Question == nil {
			return nil, apperr.New(apperr.InvalidInput, "Only a question card takes an answer.")
		}
		return m.answerQuestion(ctx, a, c, cards[0], *in.Answer)
	}
	if !in.Dismiss {
		for _, act := range cards {
			switch {
			case act.Question != nil:
				return nil, apperr.New(apperr.Conflict, "That's a question: answer it on its card (pick an answer or write one), or say Not now.")
			case act.Op == "":
				return nil, apperr.New(apperr.Conflict, "The dashboard can't do this one for Claude. %s", act.Unavailable)
			}
		}
	}
	if err := recipeAlone(cards, in); err != nil { // Recipes: a run is agreed to on its own card (D211)
		return nil, err
	}
	orderCards(cards)
	release := func() {}
	if !in.Dismiss {
		// Claimed first (D345): run once, on the computer holding the
		// conversation, never while it moves to another one.
		claim := make([]string, 0, len(cards))
		for _, act := range cards {
			claim = append(claim, act.ID)
		}
		if release, err = m.claimCards(ctx, c, claim); err != nil {
			return nil, err
		}
	}
	batch := shortID("b")
	out := make([]AssistantAction, 0, len(cards))
	for _, act := range cards {
		done, err := m.runCard(ctx, a, caller, c, act, in)
		if err != nil {
			if len(cards) == 1 {
				release()                        // a single card stays as it was (try again, or Not now)
				m.replanRecipe(ctx, c, act, err) // Recipes: a plan that changed shows again on its card (D211)
				return nil, err
			}
			done = act
			done.State, done.Error = ActionFailed, apperr.From(err).Message
		}
		now := time.Now().UTC()
		// Whoever decides it, whoever the card was for (D179).
		done.By, done.ByID, done.DecidedAt, done.Batch = a.Actor.UserName, a.Actor.UserID, &now, batch
		p := c.cardFor(done.Turn)
		done.ForID, done.ForName = p.id, p.name
		if in.Quiet {
			done.Told = true
		}
		c.putCard(done)
		out = append(out, done)
		m.auditAs(ctx, "assistant.action", "assistant_conversation", c.id, c.projectID(),
			map[string]any{"action": done.Kind, "op": done.Op, "state": done.State, "batch": len(cards) > 1, "reviewCopy": in.ReviewCopy && done.SavesVersion})
		m.logAction(ctx, c, done, a.Actor.UserID, a.Actor.UserName)
	}
	m.save(c, true)
	m.changed(c.id)
	if !in.Quiet && c.here(m) {
		m.tellClaude(c)
	}
	m.ensureWatching()
	snap := m.snapshot(ctx, c)
	return &ConversationActOut{Action: out[0], Actions: out, Conversation: snap}, nil
}

// actIDs is the cards a decision is about, each once, in the order given.
func actIDs(in ConversationActIn) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range append([]string{in.ActionID}, in.ActionIDs...) {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// cardsFor copies the cards with these IDs (in that order).
func (c *conv) cardsFor(idsList []string) ([]AssistantAction, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]AssistantAction, 0, len(idsList))
	for _, id := range idsList {
		found := false
		for _, a := range c.data.Actions {
			if a.ID == id {
				out = append(out, a)
				found = true
				break
			}
		}
		if !found {
			return nil, apperr.New(apperr.NotFound, "That card isn't in this conversation any more.").WithDetails(map[string]any{"actionId": id})
		}
	}
	return out, nil
}

// putCard replaces a card by its ID.
func (c *conv) putCard(act AssistantAction) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range c.data.Actions {
		if c.data.Actions[i].ID == act.ID {
			c.data.Actions[i] = act
		}
	}
	c.touch()
}

// decisionOps are decisions a later card may need (a save that uses an
// approved graphic): they run first when several cards are decided at once.
var decisionOps = map[string]bool{"motion.approve": true, "motion.revoke": true, "revision.approve": true, "todos.answer": true, "todos.dismiss": true}

func orderCards(cards []AssistantAction) {
	var first, rest []AssistantAction
	for _, a := range cards {
		if decisionOps[a.Op] {
			first = append(first, a)
		} else {
			rest = append(rest, a)
		}
	}
	copy(cards, append(first, rest...))
}

// runCard runs or dismisses one card and returns it decided. An error is a
// check before the change that failed (the card itself is unchanged).
func (m *Manager) runCard(ctx context.Context, a *app.App, caller ops.Caller, c *conv, act AssistantAction, in ConversationActIn) (AssistantAction, error) {
	if in.Dismiss {
		act.State = ActionDismissed
		declineKept(ctx, a, act) // Not now: never offered again in the project (D166)
		return act, nil
	}
	input, err := m.cardInput(ctx, a, caller, act, in)
	if err != nil {
		return act, err
	}
	var res any
	if act.Op == "revision.approve" || act.Op == "motion.approve" {
		// Claude's own "turn it down" card: it hears the card's outcome, not its note back (D186).
		ctx = ops.QuietTurnedDown(ctx)
	}
	if len(act.SetUp) > 0 {
		// Studio quality first (people only; the person confirmed this card).
		err = setUpFor(ctx, a, caller, act.SetUp)
	}
	if err == nil {
		res, err = ops.Call(ctx, a, act.Op, input, recipeCaller(act, caller)) // Recipes: the daemon's engine takes a card's run on (D211)
	}
	if staleRecipe(act, err) { // Recipes: the plan changed, so nothing started; Act plans the card again (D211)
		return act, err
	}
	m.finishCard(ctx, &act, res, err)
	if act.State == ActionDone && in.ReviewCopy && act.SavesVersion {
		m.reviewCopy(ctx, a, caller, &act, res)
	}
	return act, nil
}

// cardInput is the op input a card runs with: as built when Claude asked,
// plus the person's choices, and outside the dashboard the confirm token of
// this exact change (the person confirmed this card, so it is checked and used).
func (m *Manager) cardInput(ctx context.Context, a *app.App, caller ops.Caller, act AssistantAction, in ConversationActIn) (json.RawMessage, error) {
	input := json.RawMessage(act.Input)
	if act.Op == "voiceover.fitted" && len(in.Keep) > 0 {
		input = withKeep(input, in.Keep)
	}
	if act.Op == "keep.add" && in.KeepWhere != "" {
		var ki ops.KeepAddIn
		if json.Unmarshal(input, &ki) == nil {
			if in.KeepWhere != "project" && in.KeepWhere != "settings" {
				return nil, apperr.New(apperr.InvalidInput, "Keep it for this project or for every project using its settings.")
			}
			ki.Where = in.KeepWhere
			input, _ = json.Marshal(ki)
		}
	}
	// Only the dashboard channel saves without a confirm token; a card that
	// runs by itself (Auto, D186) checks its change first like a terminal.
	dashboard := a.Actor.Via == "dashboard"
	switch {
	case dashboard:
	case act.Op == "cut.apply":
		var ci ops.CutApplyIn
		_ = json.Unmarshal(input, &ci)
		chk, err := ops.CallTyped[ops.CutCheckOut](ctx, a, "cut.check", ops.CutCheckIn{Sheet: ci.Sheet, Instruction: ci.Instruction, Origin: ci.Origin, AllowUnlock: ci.AllowUnlock}, caller)
		if err != nil {
			return nil, err
		}
		ci.ConfirmToken = chk.ConfirmToken
		input, _ = json.Marshal(ci)
	case act.Op == "cut.transition":
		var ti ops.CutTransitionIn
		_ = json.Unmarshal(input, &ti)
		dry := ti
		dry.DryRun = true
		chk, err := ops.CallTyped[ops.CutTransitionOut](ctx, a, "cut.transition", dry, caller)
		if err != nil {
			return nil, err
		}
		if chk.Check != nil {
			ti.ConfirmToken = chk.Check.ConfirmToken
		}
		input, _ = json.Marshal(ti)
	case act.Op == "footage.short":
		var si ops.FootageShortIn
		_ = json.Unmarshal(input, &si)
		dry := si
		dry.DryRun = true
		chk, err := ops.CallTyped[ops.FootageShortOut](ctx, a, "footage.short", dry, caller)
		if err != nil {
			return nil, err
		}
		if chk.Check != nil {
			si.ConfirmToken = chk.Check.ConfirmToken
		}
		input, _ = json.Marshal(si)
	case act.Op == "cut.captions":
		var ci ops.CutCaptionsIn
		_ = json.Unmarshal(input, &ci)
		dry := ci
		dry.DryRun = true
		chk, err := ops.CallTyped[ops.CutCaptionsOut](ctx, a, "cut.captions", dry, caller)
		if err != nil {
			return nil, err
		}
		if chk.Check != nil {
			ci.ConfirmToken = chk.Check.ConfirmToken
		}
		input, _ = json.Marshal(ci)
	case isCutEditOp(act.Op):
		withTok, err := cutEditToken(input, func(raw json.RawMessage) (*ops.CutEditOut, error) {
			res, err := ops.Call(ctx, a, act.Op, raw, caller)
			if err != nil {
				return nil, err
			}
			return res.(interface{ Edit() *ops.CutEditOut }).Edit(), nil
		})
		if err != nil {
			return nil, err
		}
		input = withTok
	case act.Op == "edit.apply":
		var ai ops.EditApplyIn
		_ = json.Unmarshal(input, &ai)
		chk, err := ops.CallTyped[ops.EditCheckOut](ctx, a, "edit.check", ops.EditCheckIn{Plan: ai.Plan, AllowUnlock: ai.AllowUnlock}, caller)
		if err != nil {
			return nil, err
		}
		ai.ConfirmToken = chk.ConfirmToken
		input, _ = json.Marshal(ai)
	}
	return input, nil
}

// finishCard records what a card's op did: done (with its plain result, the
// pictures to look at and any work it started) or failed.
func (m *Manager) finishCard(ctx context.Context, act *AssistantAction, res any, err error) {
	switch {
	case err != nil:
		act.State, act.Error = ActionFailed, apperr.From(err).Message
	case appliedProblem(res) != "":
		// It ran, but the change did not get into the cut.
		act.State = ActionFailed
		act.Error, _, _ = actionResult(act.Op, res)
	default:
		act.State = ActionDone
		act.Result, act.Link, act.ForClaude = actionResult(act.Op, res)
		if o, ok := res.(*ops.MotionCheckOut); ok && o.Hash != "" {
			m.motionPictures(ctx, act, o.Hash)
		}
		act.Work = m.workFor(ctx, res)
	}
}

// savedRevision is the version a card's op saved, if any.
func savedRevision(res any) *ops.RevisionSummary {
	if e, ok := res.(interface{ Edit() *ops.CutEditOut }); ok {
		res = e.Edit()
	}
	switch o := res.(type) {
	case *ops.EditApplyOut:
		return &o.Revision
	case *ops.CutApplyOut:
		return &o.Revision
	case *ops.CutTransitionOut:
		if o.Applied != nil {
			return &o.Applied.Revision
		}
	case *ops.CutEditOut:
		if o.Applied != nil {
			return &o.Applied.Revision
		}
	}
	return nil
}

// reviewCopy renders a review copy of the version a card just saved (Save
// and render a review copy): the render is the card's work, so Claude hears
// once, when the copy and its quality check are done.
func (m *Manager) reviewCopy(ctx context.Context, a *app.App, caller ops.Caller, act *AssistantAction, res any) {
	rev := savedRevision(res)
	if rev == nil || rev.ID == "" {
		return
	}
	out, err := ops.CallTyped[ops.RenderOut](ctx, a, "render.submit", ops.RenderSubmitIn{RevisionID: rev.ID, Quality: "review"}, caller)
	if err != nil {
		act.Result += ". The review copy didn't start: " + firstLine(apperr.From(err).Message)
		act.ForClaude += " The review copy the person asked for didn't start: " + firstLine(apperr.From(err).Message)
		return
	}
	act.Result = strings.TrimSuffix(act.Result, ".") + ". Rendering a review copy"
	act.ForClaude += " The person also asked for a review copy: render " + out.Render.ID + "."
	act.Work = renderWork(out, "the review copy of "+versionWords(rev.Number))
}

// actionResult says what an op produced, in plain words for the card, and
// links to it. forClaude is what Claude needs to carry on (the new version's
// or render's ID, a motion graphic's exact version and its first problem): it
// goes only into the note Claude reads after the card, never on the card.
func actionResult(op string, res any) (result, link, forClaude string) {
	if e, ok := res.(interface{ Edit() *ops.CutEditOut }); ok {
		res = e.Edit() // cut flashes, cut names: an edit that says more
	}
	if result, link, forClaude, ok := conversationResult(res); ok {
		return result, link, forClaude
	}
	if result, link, forClaude, ok := soundResult(res); ok {
		return result, link, forClaude
	}
	if result, link, forClaude, ok := sourceResult(op, res); ok {
		return result, link, forClaude
	}
	if result, link, forClaude, ok := recipeResult(op, res); ok { // Recipes (recipe_actions.go)
		return result, link, forClaude
	}
	switch o := res.(type) {
	case *ops.EditApplyOut:
		return "Saved as " + versionWords(o.Revision.Number), "/p/" + o.Revision.ProjectID + "/r/" + o.Revision.ID, " (" + o.Revision.ID + ")"
	case *ops.CutApplyOut:
		return "Saved as " + versionWords(o.Revision.Number), links.CutSheet(o.Revision.ProjectID, o.Revision.ID), " (" + o.Revision.ID + ")"
	case *ops.CutTransitionOut:
		if o.Applied == nil {
			return plainSummary(o.Summary), "", ""
		}
		return savedAs(o.Summary, o.Applied.Revision.Number), "/p/" + o.Applied.Revision.ProjectID + "/r/" + o.Applied.Revision.ID, " (" + o.Applied.Revision.ID + ")"
	case *ops.CutEditOut:
		if o.Applied == nil {
			return plainSummary(o.Summary), "", ""
		}
		return savedAs(o.Summary, o.Applied.Revision.Number), "/p/" + o.Applied.Revision.ProjectID + "/r/" + o.Applied.Revision.ID, " (" + o.Applied.Revision.ID + ")"
	case *ops.RenderOut:
		what := renderWhat(o.Render)
		if o.Created {
			return upperFirst(what) + " is waiting to start", "/renders/" + o.Render.ID, " (" + o.Render.ID + ")"
		}
		return "There is already " + strings.Replace(what, "the ", "a ", 1) + " (" + renderStateWords(o.Render.State) + ")", "/renders/" + o.Render.ID, " (" + o.Render.ID + ")"
	case *ops.QARunOut:
		if o.Report != nil {
			return qaWords(o.Report.Counts, o.Report.Verdict), links.QA(o.Report.ProjectID, o.Report.RenderID, o.Report.ID), " (report " + o.Report.ID + ")"
		}
		return "The quality check is waiting to start", "/renders/" + o.RenderID, " (" + o.RenderID + ")"
	case *ops.ProjectOut:
		return "Created the project “" + o.Project.Name + "”", "/p/" + o.Project.ID, " (" + o.Project.ID + ")"
	case *models.Comment:
		return "Added the note", "/p/" + o.ProjectID, " (" + o.ID + ")"
	case *ops.SpeakerRenameOut:
		return plainSummary(o.Summary), "", ""
	case *ops.SpeakerHintOut:
		return plainSummary(o.Note), "", ""
	case *ops.PersonOut:
		return plainSummary(o.Summary), "", ""
	case *ops.TodoAnswerOut:
		return plainSummary(o.Done), "", ""
	case *ops.MotionCheckOut:
		link := ""
		if o.State != "" {
			link = links.Motion(o.Hash)
		}
		return motionCheckWords(o), link, motionCheckForClaude(o)
	case *ops.MotionDecideOut:
		return motionDecideWords(o), links.Motion(o.Row.Hash), " (" + o.Row.Version + ")"
	case *ops.ResourceMakeOut:
		return plainSummary(o.Message), links.Resource(o.Resource.ProjectID, o.Resource.ID), ""
	case *ops.ResourceFetchOut:
		return plainSummary(o.Message), links.Resource(o.ProjectID, ""), " (job " + o.JobID + ")"
	case *ops.KeepAddOut:
		link = ""
		if o.Item.ProjectID != "" {
			link = "/p/" + o.Item.ProjectID + "?tab=settings"
		}
		forClaude = " (kept as " + o.Item.ID
		if o.RevisionID != "" {
			forClaude += "; saved version " + o.RevisionID
		}
		if len(o.Jobs) > 0 {
			forClaude += "; jobs " + strings.Join(o.Jobs, ", ")
		}
		return plainSummary(o.Summary), link, forClaude + ")"
	case *ops.ResourceOut:
		return plainSummary(o.Message), links.Resource(o.Resource.ProjectID, o.Resource.ID), ""
	case *linkimport.ImportOut:
		return importWords(o), "/library", " (jobs " + strings.Join(o.JobIDs, ", ") + ")"
	case *voiceover.GenerateOut:
		result, link = narrationResult(o.Summary, o.Applied)
		return result, link, ""
	case *voiceover.CleanupOut:
		result, link = narrationResult(o.Summary, o.Applied)
		return result, link, ""
	case *voiceover.AlignOut:
		result, link = narrationResult(o.Summary, o.Applied)
		return result, link, ""
	case *voiceover.FittedOut:
		result, link = narrationResult(o.Summary, o.Applied)
		return result, link, ""
	case *voiceover.UseOut:
		result, link = narrationResult(o.Summary, o.Applied)
		return result, link, ""
	case *voiceover.PlaceOut:
		result, link = narrationResult(o.Summary, o.Applied)
		return result, link, ""
	case *generate.GenRunOut:
		var genIDs []string
		for _, g := range o.Generations {
			genIDs = append(genIDs, g.ID)
		}
		if len(o.Generations) > 0 && o.Generations[0].ProjectID != "" {
			link = "/p/" + o.Generations[0].ProjectID + "?tab=sources&generate=1"
		}
		return plainSummary(o.Summary), link, " (" + strings.Join(genIDs, ", ") + ")"
	}
	// Anything else: its own summary line when it has one, never raw JSON (the
	// person reads this on the card).
	var fields map[string]json.RawMessage
	if b, err := json.Marshal(res); err == nil && json.Unmarshal(b, &fields) == nil {
		for _, k := range []string{"summary", "done", "note", "message"} {
			var s string
			if json.Unmarshal(fields[k], &s) == nil && strings.TrimSpace(s) != "" {
				return plainSummary(s), "", ""
			}
		}
	}
	return "Done", "", ""
}

// renderWhat names a render: "the review copy of version 2", "the final render of version 2".
func renderWhat(r models.Render) string {
	if r.Quality == "final" {
		return "the final render of " + versionWords(r.RevisionNumber)
	}
	return "the review copy of " + versionWords(r.RevisionNumber)
}

// importWords says what a link import did, in plain words.
func importWords(o *linkimport.ImportOut) string {
	switch {
	case o.Queued == 1 && len(o.Imports) > 0 && o.Imports[0].Title != "":
		return "Bringing in " + theirs(o.Imports[0].Title)
	case o.Queued > 0:
		return "Bringing in " + pluralize(o.Queued, "video")
	case o.Already > 0:
		return "Already in MetaRush"
	case o.Refused > 0 && len(o.Imports) > 0:
		return plainSummary(o.Imports[0].Message)
	}
	return "Nothing to bring in"
}

// qaWords says what a quality check found, in plain words.
func qaWords(c models.QACounts, verdict string) string {
	switch {
	case c.Errors > 0 && c.Warnings > 0:
		return "The quality check found " + pluralize(c.Errors, "problem") + " and " + pluralize(c.Warnings, "thing") + " worth checking"
	case c.Errors > 0:
		return "The quality check found " + pluralize(c.Errors, "problem")
	case c.Warnings > 0:
		return "The quality check found " + pluralize(c.Warnings, "thing") + " worth checking"
	case verdict == "":
		return "The quality check is done"
	}
	return "The quality check found nothing wrong"
}

// motionPictures puts a motion graphic's test frames, contact sheet and
// preview on a card (same-origin media URLs; shown large on click).
func (m *Manager) motionPictures(ctx context.Context, act *AssistantAction, hash string) {
	comp, err := motionComponent(ctx, m, hash)
	if err != nil || comp == nil {
		return
	}
	act.Frames = nil
	for i, f := range comp.Frames {
		label := strings.TrimSpace(f.Label)
		if label == "" {
			label = "At " + strconv.FormatFloat(f.At, 'f', 1, 64) + " s"
		}
		if len(comp.Shapes) > 1 && f.Shape != "" {
			label = f.Shape + " · " + label // drawn in each shape the cuts need (D164)
		}
		act.Frames = append(act.Frames, CardPicture{URL: "/media/motion/" + hash + "/frame-" + strconv.Itoa(i), Label: label})
	}
	if comp.Sheet != nil {
		act.Image = "/media/motion/" + hash + "/sheet"
	}
	if comp.Video != nil && comp.Video.Key != "" {
		act.Preview = "/media/motion/" + hash + "/preview"
	}
}
