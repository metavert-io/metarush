package ops

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Drafts from AI apps (D248, D249). An AI app connected so that it may only
// propose (MCP's --propose, the command line's --propose) reads and checks
// like any other, and every change it asks for becomes a draft: a version not
// in the cut, or a change not yet made. Drafts wait on the review desk
// ("Awaiting your review") until a person approves one (a version goes into
// the cut, keeping both changes when someone saved meanwhile; a change is made
// as that person), says Not quite (with what should change, for the app's next
// draft) or turns it down. Approving, delivering, spending money and
// everything people-only stay a person's: the registry refuses them on a
// propose-only connection (ops.go), and draft.save refuses to keep them.

// OpDraftSave saves a draft: the one change an AI app that may only propose makes.
const OpDraftSave = "draft.save"

// versionDraftOps save a new version of a cut, and say how a draft gets the
// change's plan without making it: an edit plan or a cut sheet's check
// (own), the operation's dry run (dryRun: the cut verbs, the brand or house
// style on the cut), or its check without apply (result: the sound mix,
// levels, a narration line). Each of these only checks; a test keeps the list
// to operations that do. As a draft the plan is saved as a version beside the
// cut. Every other change to a cut is refused as a draft (D248).
var versionDraftOps = map[string]string{
	"edit.apply": planOwn, "cut.apply": planOwn,
	"cut.add": planDry, "cut.move": planDry, "cut.trim": planDry, "cut.split": planDry, "cut.delete": planDry, "cut.deleteRange": planDry,
	"cut.removeWords": planDry, "cut.transition": planDry, "cut.framing": planDry, "cut.flashes": planDry, "cut.names": planDry,
	"cut.update": planDry, "cut.useFixed": planDry, "brand.apply": planDry, "content.style.apply": planDry, "content.pack.apply": planDry,
	"mix.set": planResult, "sound.levels": planResult, "voiceover.line": planResult,
}

// How a version draft's operation gives its plan.
const (
	planOwn    = "own"
	planDry    = "dryRun"
	planResult = "result"
)

// VersionDraftOps lists the changes a draft saves as a version (for tests and docs).
func VersionDraftOps() []string {
	out := make([]string, 0, len(versionDraftOps))
	for k := range versionDraftOps {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// notDrafted are changes no draft holds, with why.
var notDrafted = map[string]string{
	"render.submit":   "MetaRush makes a review copy of every draft by itself, and final renders are a person's to start.",
	"render.cancel":   "Renders are a person's to stop.",
	"qa.run":          "MetaRush checks every review copy by itself.",
	"cut.copyIn":      "Copying another project's cut also brings its footage into this project, which a person decides.",
	"cut.nest":        "Playing another project's cut in this one also links that project's footage, which a person decides.",
	"draft.save":      "A draft can't hold another draft.",
	"timeline.import": "A timeline from another editor becomes a cut when a person brings it in, answering for footage it doesn't find: read it (timeline.read) and tell them what would come in.",
	"draft.decide":    "Only a person decides about drafts.",
	"project.update":  "A project's name, brief and place are a person's to change: tell them what you'd suggest.",
	"sound.enhance":   "Clearer voices are made from the recordings first (and in the cloud they cost money), so a person starts them.",
	"voiceover.tts":   "The AI voice costs money, so a person makes it after seeing the price.",
	// Words changed in a speaker's own voice (D311): making them, putting them
	// in and taking them out are a person's steps; making and putting them in
	// are people only anyway.
	"cut.say.undo":    "Changing what someone is heard to say, either way, is a person's step: tell them what you'd suggest.",
	"generate.shot":   "Generated video costs money, so a person asks for it after seeing the price.",
	"generate.again":  "Generated video costs money, so a person asks for it after seeing the price.",
	"generate.extend": "Generated video costs money, so a person asks for it after seeing the price.",
	"generate.edit":   "Generated video costs money, so a person asks for it after seeing the price.",
	// Screen recordings (D258–D260) are their maker's: footage, links and cuts from one are a person's to start.
	"recording.finish": "A screen recording becomes footage when the person who recorded it, or an owner, says so.",
	"recording.link":   "Linking a screen recording brings its footage into a project, which a person decides.",
	"recording.cut":    "A cut from a screen recording links its footage into the project first, which a person decides.",
	// Layout packs (D266): a pack decides every video of the settings, and code reaches one only through a person.
	"content.pack.set":     "A layout pack decides how every video using the settings looks, so a person changes it under Settings → Layout pack: tell them what you'd suggest.",
	"content.pack.propose": "A proposed layout pack already waits for a person's decision on its own, so it isn't kept as a draft too: tell the person what you'd suggest.",
	"content.pack.decline": "Setting a proposed layout pack aside is the person's decision.",
	// The ship gate (D299): sending a version out past what holds it is a person's, with their own note.
	"delivery.override": "Sending a version out past the quality check or a waiting sign-off is a person's decision, with their own note: tell them what holds it.",
	// Exporting a workspace (D332): everything about the work, people and their consent included, in one file.
	"workspace.export": "An export holds everything about the work, people and their consent included, so only an owner makes one, in person: tell them why you'd suggest one.",
	// A restored copy's pause on deleting by itself is lifted by an owner who knows the original is retired.
	"workspace.adopt": "Only an owner says a restored copy is now the workspace, once they know the original is retired or the copy has storage of its own.",
}

// MaxWaitingDrafts is how many drafts one app may have waiting at once.
const MaxWaitingDrafts = 50

// DraftSaveIn proposes one change.
type DraftSaveIn struct {
	Op    string          `json:"op" jsonschema:"minLength=1,maxLength=80,description=The change to propose\\, by its operation's name (find and describe show them): cut.apply with a cut sheet\\, edit.apply with an edit plan\\, a cut verb (cut.trim…)\\, a Content Settings change (content.rules.add\\, transcript.spelling.add\\, content.style.set…) or another change."`
	Input json.RawMessage `json:"input,omitempty" jsonschema:"type=object,description=Its input\\, as describe shows it (no confirm token is needed: nothing changes until a person approves)."`
	// Note is the app's own words: why. A person reads it on the review desk.
	Note string `json:"note,omitempty" jsonschema:"maxLength=2000,description=Why\\, in a sentence or two: the person reads it beside the change."`
	// Replaces: the draft this one takes the place of (after Not quite).
	Replaces string `json:"replaces,omitempty" jsonschema:"description=The draft this one takes the place of (one a person said Not quite about)."`
}

// DraftListIn chooses drafts.
type DraftListIn struct {
	Agent     string `json:"agent,omitempty" jsonschema:"description=Only this AI app's (codex\\, cursor…). Default: the asking app's own when it has a name\\, else everyone's."`
	All       bool   `json:"all,omitempty" jsonschema:"description=Every app's drafts\\, even when the asking app has a name."`
	State     string `json:"state,omitempty" jsonschema:"enum=,enum=waiting,enum=approving,enum=approved,enum=not_quite,enum=turned_down,enum=replaced,enum=in_cut,description=Only drafts in this state."`
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=Only this project's (ID or name)."`
	Limit     int    `json:"limit,omitempty" jsonschema:"minimum=0,maximum=200,description=At most this many\\, newest first (default 50)."`
}

// DraftListOut lists drafts, newest first.
type DraftListOut struct {
	Drafts []DraftView `json:"drafts"`
}

// DraftDecideIn is a person's decision.
type DraftDecideIn struct {
	ID       string `json:"id" jsonschema:"description=The draft (or the version a draft holds)."`
	Decision string `json:"decision" jsonschema:"enum=approve,enum=notQuite,enum=turnDown,description=approve: put it in the cut (a version) or make the change; notQuite: say what should change (note) for the app's next draft; turnDown: no."`
	Note     string `json:"note,omitempty" jsonschema:"maxLength=2000,description=What should change (Not quite needs it)\\, or why."`
}

// Decisions a person makes about a draft.
const (
	DecideApprove  = "approve"
	DecideNotQuite = "notQuite"
	DecideTurnDown = "turnDown"
)

// DraftView is a draft as the review desk and the app that made it see it.
type DraftView struct {
	ID        string       `json:"id"`
	Kind      string       `json:"kind" jsonschema:"enum=version,enum=change"`
	State     string       `json:"state" jsonschema:"enum=waiting,enum=approving,enum=approved,enum=not_quite,enum=turned_down,enum=replaced,enum=in_cut"`
	Op        string       `json:"op"`
	Input     string       `json:"input,omitempty" jsonschema:"description=The change's input as the app gave it (Details)."`
	Title     string       `json:"title" jsonschema:"description=What it does\\, in a few plain words."`
	Changes   []string     `json:"changes" jsonschema:"description=What changes\\, in plain words (a version: what changes in the video)."`
	Warnings  []string     `json:"warnings,omitempty" jsonschema:"description=What the check found that doesn't stop it."`
	Note      string       `json:"note,omitempty" jsonschema:"description=The app's own words: why."`
	Agent     string       `json:"agent"`
	AgentName string       `json:"agentName" jsonschema:"description=The app in people's words: Codex\\, Cursor\\, Claude Code."`
	Via       string       `json:"via" jsonschema:"description=How it reached MetaRush: mcp or cli."`
	ViaWords  string       `json:"viaWords" jsonschema:"description=The same in words: through MetaRush's tools for AI apps\\, or the command line."`
	CreatedBy models.Actor `json:"createdBy"`
	CreatedAt time.Time    `json:"createdAt"`
	// Said is the line the review desk shows: "Codex made it, through MetaRush's tools for AI apps".
	Said        string `json:"said"`
	ProjectID   string `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	CutTitle    string `json:"cutTitle,omitempty"`
	// A version draft's version (not in the cut while it waits), and the
	// version it was written against.
	Revision   *RevisionSummary `json:"revision,omitempty"`
	BaseNumber int              `json:"baseNumber,omitempty"`
	// HeadMoved: someone saved a newer version since; approving keeps both
	// changes when they don't overlap.
	HeadMoved  bool             `json:"headMoved,omitempty"`
	ReviewCopy *DraftReviewCopy `json:"reviewCopy,omitempty"`
	// Cost is MetaRush's estimate of what approving costs at outside
	// services; Receipt says it in one line with what the draft used.
	Cost       *costs.Estimate       `json:"cost,omitempty"`
	Receipt    string                `json:"receipt"`
	Decision   *models.DraftDecision `json:"decision,omitempty"`
	Tried      *models.DraftTry      `json:"tried,omitempty"`
	Replaces   string                `json:"replaces,omitempty"`
	ReplacedBy string                `json:"replacedBy,omitempty"`
	URL        string                `json:"url"`
	// Next says what happens now (for the app that made it).
	Next string `json:"next,omitempty"`
}

// DraftReviewCopy is a version draft's review copy and its quality check.
type DraftReviewCopy struct {
	RenderID string `json:"renderId"`
	State    string `json:"state"`
	// Said: "Ready; the quality check found nothing wrong", "Being made"…
	Said     string  `json:"said"`
	Problems int     `json:"problems"`
	Worth    int     `json:"worthChecking"`
	Machine  string  `json:"machine,omitempty"`
	Seconds  float64 `json:"seconds,omitempty" jsonschema:"description=How long it took to make."`
	Follow   bool    `json:"follow,omitempty" jsonschema:"description=MetaRush made it by itself."`
}

func init() {
	Def(OpDraftSave, "Propose a change for a person to review, without making it: a new version of a cut (a cut sheet, an edit plan, a cut verb) saved as a draft outside the cut, or another change (Content Settings, names, notes) kept until a person approves it", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *DraftSaveIn) (*DraftView, error) {
			return saveDraft(ctx, a, in)
		})
	Def("draft.list", "Drafts AI apps proposed, newest first: what each changes, its state (waiting, approved, Not quite with a person's note, turned down) and the decision", Flags{},
		func(ctx context.Context, a *app.App, in *DraftListIn) (*DraftListOut, error) {
			return listDrafts(ctx, a, in)
		})
	Def("draft.decide", "Decide about an AI app's draft — people only: approve (a version goes into the cut, a change is made), Not quite with what should change, or turn it down", Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *DraftDecideIn) (*DraftView, error) {
			return decideDraft(ctx, a, in)
		})
}

// proposeRefusal is what a propose-only connection hears for a change it
// tried to make (ops.go): save it as a draft, or why no draft can hold it.
func proposeRefusal(op *Op) error {
	details := map[string]any{"reason": "propose_only", "op": op.Name}
	if why := whyNotDrafted(op); why != "" {
		details["draftable"] = false
		if op.HumanOnly {
			return apperr.New(apperr.HumanOnly, "Only a person can do this: an AI app may propose changes, never approve, deliver or decide.").
				WithHint("Ask the person to do it in the dashboard.").WithDetails(details)
		}
		return apperr.New(apperr.Unauthorized, "This AI app can only propose changes, and this one is a person's: %s", lowerFirst(why)).WithDetails(details)
	}
	details["draftable"] = true
	cmd := fmt.Sprintf("metarush draft save %s --input '<the same input>' --note '<why>'", op.Name)
	switch op.Name {
	case "cut.apply":
		cmd = "metarush draft save --sheet <file> --note '<why>'"
	case "edit.apply":
		cmd = "metarush draft save --plan <file> --note '<why>'"
	}
	return apperr.New(apperr.Unauthorized, "This AI app can only propose changes: save this one as a draft, and a person approves it on the review desk.").
		WithHint("Nothing changes until a person approves the draft.").WithDetails(details).WithCommand(cmd)
}

// Draftable reports whether a draft may hold a change made by op: the
// changes saved as versions beside the cut (versionDraftOps) and the change
// drafts' allowlist (changeDrafts) — what a propose-only connection offers in
// a change's place (D248).
func Draftable(op *Op) bool { return op.Mutating && whyNotDrafted(op) == "" }

// whyNotDrafted says why no draft may hold op ("" when one may).
func whyNotDrafted(op *Op) string {
	switch {
	case op.HumanOnly && notDrafted[op.Name] != "":
		return notDrafted[op.Name]
	case op.HumanOnly:
		return "Only a person can do this."
	case !op.Mutating:
		return ""
	case op.OwnerOnly:
		return "Only an owner changes this computer's and the workspace's settings."
	case op.LocalOnly:
		return "It works only on the computer that has the files."
	}
	if why := notDrafted[op.Name]; why != "" {
		return why
	}
	if _, ok := changeDrafts[op.Name]; ok || versionDraftOps[op.Name] != "" {
		return ""
	}
	for _, p := range paidOps {
		if p == op.Name {
			return "It costs money at an outside service, so a person starts it after seeing the price."
		}
	}
	return "MetaRush doesn't keep this kind of change as a draft."
}

// saveDraft is draft.save.
func saveDraft(ctx context.Context, a *app.App, in *DraftSaveIn) (*DraftView, error) {
	name := strings.TrimSpace(in.Op)
	op, ok := Get(name)
	if !ok {
		op, ok = Get(strings.ReplaceAll(name, "_", ".")) // its MCP tool name
	}
	if !ok {
		return nil, apperr.New(apperr.NotFound, "MetaRush has no change by that name.").WithDetails(map[string]any{"op": name}).WithCommand("metarush ops find <what it does>")
	}
	input := map[string]any{}
	if raw := bytes.TrimSpace(in.Input); len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, invalid("Give the change's input as an object with the fields its description lists.")
		}
	}
	if !op.Mutating {
		return nil, apperr.New(apperr.InvalidInput, "That only reads, so there's nothing to propose: run it instead.").WithDetails(map[string]any{"op": op.Name})
	}
	if why := whyNotDrafted(op); why != "" {
		code := apperr.Unauthorized
		if op.HumanOnly {
			code = apperr.HumanOnly
		}
		return nil, apperr.New(code, "No draft can hold this change. %s", why).WithDetails(map[string]any{"op": op.Name, "reason": "not_draftable"}).
			WithHint("Tell the person what you'd like, and they decide.")
	}
	// An app's words are data: control characters and terminal escapes go (D248).
	input = cleanValue(input).(map[string]any)
	// Tokens never matter in a draft: nothing changes until a person approves.
	delete(input, "confirmToken")
	delete(input, "mcpConfirmToken")
	note := cleanText(strings.TrimSpace(in.Note))
	if len([]rune(note)) > 2000 {
		note = string([]rune(note)[:2000])
	}
	d := &models.Draft{ID: ids.New(ids.Draft), WorkspaceID: a.WorkspaceID(), Op: op.Name, Note: note, State: models.DraftWaiting,
		Agent: a.Actor.Agent, CreatedBy: a.Actor, CreatedAt: time.Now().UTC()}
	if n, err := a.DB.C(store.CDrafts).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "agent": d.Agent,
		"state": bson.M{"$in": bson.A{models.DraftWaiting, models.DraftApproving}}}); err == nil && n >= MaxWaitingDrafts {
		return nil, apperr.New(apperr.Conflict, "%s has %d drafts waiting for review already: a person needs to look at them before it saves more.", AgentWords(d.Agent), n).
			WithDetails(map[string]any{"waiting": n, "max": MaxWaitingDrafts})
	}
	var old *models.Draft
	if in.Replaces != "" {
		o, err := getDraft(ctx, a, in.Replaces)
		if err != nil {
			return nil, err
		}
		if o.State != models.DraftWaiting && o.State != models.DraftNotQuite {
			return nil, apperr.New(apperr.Conflict, "That draft isn't waiting any more (%s), so nothing takes its place.", draftStateWords(o.State)).WithDetails(map[string]any{"replaces": o.ID})
		}
		if o.Agent == d.Agent {
			old, d.Replaces = o, o.ID
		} // another app's draft stays: both wait
	}
	var err error
	if versionDraftOps[op.Name] != "" {
		err = saveVersionDraft(ctx, a, op, input, d)
	} else {
		err = saveChangeDraft(ctx, a, op, input, d)
	}
	if err != nil {
		return nil, err
	}
	for i := range d.Changes {
		d.Changes[i] = cleanText(d.Changes[i])
	}
	d.Title = cleanText(d.Title)
	cost := draftPrice(d)
	if cost.USD > 0 {
		return nil, apperr.New(apperr.Unauthorized, "No draft can hold a change that costs money (MetaRush estimates %s): a person starts it after seeing the price.", cost.About)
	}
	d.Cost = &cost
	if err := a.DB.Insert(ctx, store.CDrafts, d); err != nil {
		return nil, err
	}
	if old != nil {
		_, _ = a.DB.C(store.CDrafts).UpdateOne(ctx, bson.M{"_id": old.ID, "workspaceId": a.WorkspaceID(), "state": bson.M{"$in": bson.A{models.DraftWaiting, models.DraftNotQuite}}},
			bson.M{"$set": bson.M{"state": models.DraftReplaced, "replacedBy": d.ID}})
	}
	a.Audit(ctx, "draft.save", "draft", d.ID, d.ProjectID, map[string]any{"op": d.Op, "kind": d.Kind, "agent": d.Agent, "revisionId": d.RevisionID, "replaces": d.Replaces})
	v := viewDraft(ctx, a, d)
	v.Next = "Saved as a draft for a person to review: nothing changes until they approve it. They may say Not quite with what should change; draft list shows their decision."
	if in.Replaces != "" && d.Replaces == "" {
		v.Next += " It doesn't take the place of another app's draft: both wait."
	}
	return v, nil
}

// saveVersionDraft checks a change that saves a version and saves it as a
// version outside the cut.
func saveVersionDraft(ctx context.Context, a *app.App, op *Op, input map[string]any, d *models.Draft) error {
	if unlock, _ := input["allowUnlock"].(bool); unlock {
		return apperr.New(apperr.Locked, "A draft can't unlock locked parts of a cut.").WithHint("Ask the person to unlock them, or leave them as they are.")
	}
	var prep *prepared
	var changes []string
	switch op.Name {
	case "edit.apply":
		var in EditApplyIn
		if err := strictInput(input, &in); err != nil {
			return err
		}
		p, err := prepare(ctx, a, in.Plan, false)
		if err != nil {
			return err
		}
		prep = p
	case "cut.apply":
		var in CutApplyIn
		if err := strictInput(input, &in); err != nil {
			return err
		}
		chk, err := checkSheet(ctx, a, in.Sheet, in.Instruction, in.Rationale, "draft:"+d.ID, false)
		if err != nil {
			return err
		}
		if !chk.Valid {
			return apperr.New(apperr.Validation, "The cut sheet has %d problem(s), so it can't be a draft yet.", countErrors(chk.Problems)).
				WithDetails(map[string]any{"problems": chk.Problems}).WithHint("Check the sheet to see each problem with its line, fix them, then save the draft.")
		}
		if chk.NoChanges {
			return apperr.New(apperr.InvalidInput, "Nothing to propose: this sheet is the same cut as version %d.", chk.BaseNumber)
		}
		prep, changes = chk.prep, chk.Changes
	default:
		// A cut verb, the brand or house style on the cut: its dry run is the
		// check and gives the plan. The sound mix, levels, a narration line:
		// checked without apply, they give it too. A draft never applies.
		delete(input, "apply")
		if versionDraftOps[op.Name] == planDry {
			input["dryRun"] = true
		}
		in := reflect.New(op.InType).Interface()
		if err := strictInput(input, in); err != nil {
			return err
		}
		out, err := Run(ctx, a, op, in)
		if err != nil {
			return err
		}
		plan, check, lines, err := planOf(op, out)
		if err != nil {
			return err
		}
		if check != nil && !check.Valid {
			return apperr.New(apperr.Validation, "This change can't be saved as it stands. %s", validationSummary(check.Errors)).
				WithDetails(map[string]any{"errors": check.Errors})
		}
		p, err := prepare(ctx, a, plan, false)
		if err != nil {
			return err
		}
		prep, changes = p, lines
	}
	if len(prep.errors) > 0 {
		return apperr.New(apperr.Validation, "This change can't be saved as it stands. %s", validationSummary(prep.errors)).
			WithDetails(map[string]any{"errors": prep.errors, "warnings": prep.warnings})
	}
	prep.plan.Origin = "draft:" + d.ID
	if len(changes) == 0 {
		changes = describeVersion(ctx, a, prep)
	}
	rev, err := commitDraftRevision(ctx, a, prep, d.ID)
	if err != nil {
		return err
	}
	d.Kind, d.ProjectID, d.Branch = models.DraftVersion, rev.ProjectID, rev.Branch
	d.RevisionID, d.RevisionNumber, d.BaseRevisionID = rev.ID, rev.Number, rev.ParentID
	d.Changes = changes
	d.Title = draftVersionTitle(prep, rev)
	for _, w := range prep.warnings {
		d.Warnings = append(d.Warnings, withoutCommands(w.Message))
	}
	if d.Note == "" {
		d.Note = strings.TrimSpace(firstNonEmpty(prep.plan.Rationale, prep.plan.Instruction))
	}
	return nil
}

// planOf is the plan a version draft's operation checked without making it:
// a cut verb's dry run (CutEditOut), or a sound or narration change's result.
func planOf(op *Op, out any) (json.RawMessage, *EditCheckOut, []string, error) {
	if e, ok := out.(interface{ Edit() *CutEditOut }); ok {
		ce := e.Edit()
		if ce.NoChange {
			return nil, nil, nil, apperr.New(apperr.InvalidInput, "Nothing to propose: %s", firstNonEmpty(ce.Summary, "the cut wouldn't change."))
		}
		return ce.Plan, ce.Check, ce.Changes, nil
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, nil, nil, err
	}
	var r struct {
		Changes []string `json:"changes"`
		Result  *struct {
			Plan  json.RawMessage `json:"plan"`
			Check *EditCheckOut   `json:"check"`
		} `json:"result"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, nil, nil, err
	}
	if r.Result == nil || len(r.Result.Plan) == 0 {
		return nil, nil, nil, apperr.New(apperr.InvalidInput, "Nothing to propose: the cut wouldn't change.").WithDetails(map[string]any{"op": op.Name})
	}
	return r.Result.Plan, r.Result.Check, r.Changes, nil
}

// describeVersion says what a prepared change does to the video, in words.
func describeVersion(ctx context.Context, a *app.App, p *prepared) []string {
	parent := &editspec.Spec{Output: p.spec.Output}
	if p.parentID != "" {
		var r models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), p.parentID, &r) == nil {
			parent = &r.Spec
		}
	}
	info := newSheetInfo(ctx, a)
	names := cutsheet.NameFunc(p.spec, info, projectAssetIDs(ctx, a, p.project.ID))
	return cutsheet.Describe(parent, p.spec, names)
}

func draftVersionTitle(p *prepared, rev *models.Revision) string {
	if rev.Note != "" {
		return upperFirst(rev.Note)
	}
	if p.plan.Instruction != "" {
		return upperFirst(oneLine(p.plan.Instruction))
	}
	return fmt.Sprintf("A new version of %s", q(p.project.Name))
}

// strictInput decodes an input map into an operation's input type, refusing
// fields it doesn't have (as the registry does).
func strictInput(input map[string]any, into any) error {
	b, _ := json.Marshal(input)
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return apperr.Wrap(apperr.InvalidInput, err, "Something in the change's input isn't in a form MetaRush accepts.").WithDetails(map[string]any{"problem": err.Error()})
	}
	return nil
}

// commitDraftRevision saves a prepared change as a version that isn't in the
// cut: numbered and kept like any version (its words pinned), but no branch's
// latest version moves (commitRevision is the one that moves it).
func commitDraftRevision(ctx context.Context, a *app.App, p *prepared, draftID string) (*models.Revision, error) {
	var proj models.Project
	err := a.DB.C(store.CProjects).FindOneAndUpdate(ctx, bson.M{"_id": p.project.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$inc": bson.M{"revisionSeq": 1}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&proj)
	if err != nil {
		return nil, err
	}
	_, total := editspec.Timeline(p.spec)
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: p.project.ID, Branch: p.branch, Number: proj.RevisionSeq,
		ParentID: p.parentID, Spec: *p.spec, SpecHash: editspec.Hash(p.spec), Ops: p.plan.Ops, Instruction: p.plan.Instruction, Rationale: p.plan.Rationale, Origin: p.plan.Origin,
		Warnings: p.warnings, Duration: float64(total) / float64(p.spec.Output.FPS), Approval: models.Approval{State: models.ApprovalDraft}, CreatedBy: a.Actor,
		CreatedAt: time.Now().UTC(), DraftID: draftID, Includes: p.includes}
	if p.rebased && p.base != nil {
		rev.RebasedFrom = p.base.ID
	}
	if rev.Transcripts, err = pinTranscripts(ctx, a, p.facts); err != nil {
		return nil, err
	}
	rev.Note, _ = versionNote(&p.plan, p.diff, a.Actor.Via)
	rev.NoteBy = models.NoteByMetaRush
	if p.plan.Note != "" {
		rev.NoteBy = models.NoteByAgent
	}
	if err := a.DB.Insert(ctx, store.CRevisions, rev); err != nil {
		return nil, err
	}
	// The review copy needs the large files of linked folders it uses (D81).
	queueResourceUploads(ctx, a, &rev, p.facts)
	return &rev, nil
}

// saveChangeDraft checks a change against its allowlist entry, says in full
// what approving does and keeps it (D248).
func saveChangeDraft(ctx context.Context, a *app.App, op *Op, input map[string]any, d *models.Draft) error {
	spec := changeDrafts[op.Name]
	sh, err := describeChange(ctx, a, op, spec, input) // a setting it can't show refuses it first, plainly
	if err != nil {
		return err
	}
	in := reflect.New(op.InType).Interface()
	if err := strictInput(input, in); err != nil {
		return err
	}
	b, _ := json.Marshal(input)
	d.Kind, d.Input, d.ProjectID, d.Title, d.Changes = models.DraftChange, string(b), sh.projectID, sh.title, sh.lines
	d.Target, d.Expect = sh.target, sh.expect
	return nil
}

func clipWords(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}

func getDraft(ctx context.Context, a *app.App, id string) (*models.Draft, error) {
	var d models.Draft
	if err := a.DB.GetByID(ctx, store.CDrafts, a.WorkspaceID(), id, &d); err != nil {
		return nil, apperr.New(apperr.NotFound, "That draft isn't in this workspace.").WithDetails(map[string]any{"draftId": id})
	}
	return &d, nil
}

// draftOf finds a draft by its ID or by the version it holds.
func draftOf(ctx context.Context, a *app.App, id string) (*models.Draft, error) {
	if strings.HasPrefix(id, ids.Revision+"_") {
		var d models.Draft
		if err := a.DB.C(store.CDrafts).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "revisionId": id}).Decode(&d); err != nil {
			return nil, apperr.New(apperr.NotFound, "No AI app's draft holds that version.").WithDetails(map[string]any{"revisionId": id})
		}
		return &d, nil
	}
	return getDraft(ctx, a, id)
}

func draftStateWords(s string) string {
	switch s {
	case models.DraftWaiting:
		return "waiting for review"
	case models.DraftApproved:
		return "approved"
	case models.DraftNotQuite:
		return "a person said Not quite"
	case models.DraftTurnedDown:
		return "turned down"
	case models.DraftReplaced:
		return "a newer draft took its place"
	case models.DraftApproving:
		return "being approved"
	case models.DraftInCut:
		return "its change is in the cut"
	}
	return s
}

// listDrafts is draft.list.
func listDrafts(ctx context.Context, a *app.App, in *DraftListIn) (*DraftListOut, error) {
	filter := bson.M{}
	switch {
	case in.Agent != "":
		filter["agent"] = models.AgentSlug(in.Agent)
	case !in.All && a.Actor.Agent != "":
		filter["agent"] = a.Actor.Agent
	}
	if in.State != "" {
		filter["state"] = in.State
	}
	if in.ProjectID != "" {
		p, err := resolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		filter["projectId"] = p.ID
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	var ds []models.Draft
	if err := a.DB.Find(ctx, store.CDrafts, a.WorkspaceID(), filter, &ds, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(int64(limit))); err != nil {
		return nil, err
	}
	out := &DraftListOut{Drafts: make([]DraftView, 0, len(ds))}
	for i := range ds {
		out.Drafts = append(out.Drafts, *viewDraft(ctx, a, &ds[i]))
	}
	return out, nil
}

// AgentWords names who made something for people: "Codex", "Claude", "An AI app".
func AgentWords(agent string) string {
	if n := models.AgentName(agent); n != "" {
		return n
	}
	return "An AI app"
}

// ViaWords says how an AI app reached MetaRush.
func ViaWords(via string) string {
	switch via {
	case "mcp":
		return "through MetaRush's tools for AI apps"
	case "cli":
		return "from the command line"
	case models.ViaAssistant, models.ViaAssistantAuto:
		return "in the dashboard's Claude Code panel"
	case "dashboard":
		return "in the dashboard"
	}
	return ""
}

// viewDraft is a draft as people and apps read it.
func viewDraft(ctx context.Context, a *app.App, d *models.Draft) *DraftView {
	v := &DraftView{ID: d.ID, Kind: d.Kind, State: d.State, Op: d.Op, Input: d.Input, Title: d.Title, Changes: d.Changes, Warnings: d.Warnings,
		Note: d.Note, Agent: d.Agent, AgentName: AgentWords(d.Agent), Via: d.CreatedBy.Via, ViaWords: ViaWords(d.CreatedBy.Via),
		CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt, ProjectID: d.ProjectID, Decision: d.Decision, Tried: d.Tried,
		Replaces: d.Replaces, ReplacedBy: d.ReplacedBy, URL: dashboardURL(a, "/review/"+d.ID)}
	if v.Changes == nil {
		v.Changes = []string{}
	}
	v.Said = v.AgentName + " made it"
	if v.ViaWords != "" {
		v.Said += ", " + v.ViaWords
	}
	var p models.Project
	if d.ProjectID != "" && a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), d.ProjectID, &p) == nil {
		v.ProjectName = p.Name
		if len(p.Branches) > 1 && d.Branch != "" {
			v.CutTitle = p.Branches[d.Branch].Title
		}
	}
	cost := d.Cost
	if cost == nil {
		c := draftPrice(d)
		cost = &c
	}
	v.Cost = cost
	v.Receipt = receiptWords(cost, nil)
	if d.Kind != models.DraftVersion || d.RevisionID == "" {
		return v
	}
	var r models.Revision
	if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"_id": d.RevisionID, "workspaceId": a.WorkspaceID()}, options.FindOne().SetProjection(bson.M{"ops": 0, "transcripts": 0})).Decode(&r); err == nil {
		head := ""
		if p.ID != "" {
			head = p.Branches[r.Branch].Head
		}
		s := summarizeRev(a, r, head)
		v.Revision = &s
		v.HeadMoved = d.State == models.DraftWaiting && head != r.ParentID
	}
	if d.BaseRevisionID != "" {
		var b models.Revision
		if a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"_id": d.BaseRevisionID, "workspaceId": a.WorkspaceID()}, options.FindOne().SetProjection(bson.M{"number": 1})).Decode(&b) == nil {
			v.BaseNumber = b.Number
		}
	}
	v.ReviewCopy = draftReviewCopy(ctx, a, d.RevisionID)
	used := []string{"MetaRush's check of the change"}
	if rc := v.ReviewCopy; rc != nil && rc.State == models.RenderAvailable {
		u := "a review copy"
		if rc.Machine != "" {
			u += " made on " + q(rc.Machine)
		}
		if rc.Seconds > 0 {
			u += " in " + logbook.FormatDuration(rc.Seconds*1000)
		}
		used = append(used, u, "its quality check")
	}
	v.Receipt = receiptWords(cost, used)
	return v
}

// receiptWords is the receipt line: what approving costs at outside services
// (nothing for every change a draft may hold), and what the draft used.
func receiptWords(cost *costs.Estimate, used []string) string {
	s := "These changes cost nothing to make: no paid outside service is used."
	if cost.USD > 0 {
		s = "Approving would cost " + cost.About + " at outside services."
	}
	if len(used) > 0 {
		s += " Used " + joinAnd(used) + "."
	}
	return s
}

// draftReviewCopy is the newest review copy of a draft's version.
func draftReviewCopy(ctx context.Context, a *app.App, revisionID string) *DraftReviewCopy {
	var rs []models.Render
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"revisionId": revisionID, "quality": "review", "state": bson.M{"$ne": models.RenderCanceled}}, &rs,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(1)); err != nil || len(rs) == 0 {
		return nil
	}
	r := rs[0]
	rc := &DraftReviewCopy{RenderID: r.ID, State: r.State, Machine: r.RenderedByName, Follow: r.Follow}
	if r.CompletedAt != nil {
		rc.Seconds = r.CompletedAt.Sub(r.CreatedAt).Seconds()
	}
	switch r.State {
	case models.RenderAvailable:
		switch {
		case r.QA == nil:
			rc.Said = "Ready; its quality check hasn't finished yet"
		case r.QA.Errors > 0:
			rc.Problems, rc.Worth = r.QA.Errors, r.QA.Warnings
			rc.Said = "Ready; the quality check found " + plural(r.QA.Errors, "problem")
		case r.QA.Warnings > 0:
			rc.Worth = r.QA.Warnings
			rc.Said = "Ready; the quality check found " + plural(r.QA.Warnings, "thing") + " worth checking"
		default:
			rc.Said = "Ready; the quality check found nothing wrong"
		}
	case models.RenderFailed:
		rc.Said = "It couldn't be made"
	default:
		rc.Said = "Being made"
	}
	return rc
}

// decideDraft is draft.decide: a person's decision.
func decideDraft(ctx context.Context, a *app.App, in *DraftDecideIn) (*DraftView, error) {
	d, err := draftOf(ctx, a, in.ID)
	if err != nil {
		return nil, err
	}
	note := strings.TrimSpace(in.Note)
	switch d.State {
	case models.DraftApproved:
		return nil, apperr.New(apperr.Conflict, "This draft was approved already.").WithDetails(map[string]any{"draftId": d.ID})
	case models.DraftInCut:
		return nil, apperr.New(apperr.Conflict, "This draft's change is in the cut already: someone saved a change on top of it.").WithDetails(map[string]any{"draftId": d.ID})
	case models.DraftReplaced:
		return nil, apperr.New(apperr.Conflict, "A newer draft took this one's place: decide about that one.").WithDetails(map[string]any{"draftId": d.ID, "replacedBy": d.ReplacedBy})
	case models.DraftApproving:
		if d.ApprovingAt != nil && time.Since(*d.ApprovingAt) < approvingFor {
			return nil, apperr.New(apperr.Conflict, "Someone is approving this draft right now.").WithDetails(map[string]any{"draftId": d.ID})
		}
	}
	now := time.Now().UTC()
	dec := &models.DraftDecision{By: a.Actor, At: now, Note: note}
	switch in.Decision {
	case DecideApprove:
		dec.State = models.DraftApproved
		return approveDecided(ctx, a, d, dec)
	case DecideNotQuite:
		if note == "" {
			return nil, invalid("Say what should change: the note goes back to the AI app for its next draft.")
		}
		dec.State = models.DraftNotQuite
	case DecideTurnDown:
		dec.State = models.DraftTurnedDown
	default:
		return nil, invalid("Decide with approve, notQuite or turnDown.")
	}
	res, err := a.DB.C(store.CDrafts).UpdateOne(ctx, bson.M{"_id": d.ID, "workspaceId": a.WorkspaceID(), "state": d.State},
		bson.M{"$set": bson.M{"state": dec.State, "decision": dec}, "$unset": bson.M{"tried": "", "approvingAt": ""}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, apperr.New(apperr.Conflict, "Someone decided about this draft at the same moment; look at it again.").WithDetails(map[string]any{"draftId": d.ID})
	}
	d.State, d.Decision, d.Tried = dec.State, dec, nil
	if d.Kind == models.DraftVersion && d.RevisionID != "" {
		// The version itself reads Needs changes, with the person's note.
		ap := models.Approval{State: models.ApprovalRejected, By: &dec.By, At: &now, Note: note}
		_, _ = a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": d.RevisionID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"approval": ap}})
	}
	a.Audit(ctx, "draft."+dec.State, "draft", d.ID, d.ProjectID, map[string]any{"agent": d.Agent, "note": note})
	return viewDraft(ctx, a, d), nil
}

// approvingFor is how long an approval holds a draft before another person
// may approve it again (one left unfinished by a crash).
const approvingFor = 5 * time.Minute

// approveDecided carries out an approval once: the draft moves from its
// state to approving (a check-and-set, so a double click or two people make
// one approval), is priced again, then the change is made and the draft
// marked approved — or it goes back to its state, saying why.
func approveDecided(ctx context.Context, a *app.App, d *models.Draft, dec *models.DraftDecision) (*DraftView, error) {
	now := dec.At
	claim := bson.M{"_id": d.ID, "workspaceId": a.WorkspaceID(), "$or": bson.A{
		bson.M{"state": bson.M{"$in": bson.A{models.DraftWaiting, models.DraftNotQuite, models.DraftTurnedDown}}},
		bson.M{"state": models.DraftApproving, "approvingAt": bson.M{"$lt": now.Add(-approvingFor)}}}}
	var before models.Draft
	err := a.DB.C(store.CDrafts).FindOneAndUpdate(ctx, claim, bson.M{"$set": bson.M{"state": models.DraftApproving, "approvingAt": now}}).Decode(&before)
	if err != nil {
		return nil, apperr.New(apperr.Conflict, "Someone is deciding about this draft right now, or did already; look at it again.").WithDetails(map[string]any{"draftId": d.ID})
	}
	back := before.State
	if back == models.DraftApproving {
		back = models.DraftWaiting
	}
	fail := func(err error) (*DraftView, error) {
		ae := apperr.From(err)
		try := models.DraftTry{By: a.Actor, At: now, Message: ae.Message, Code: ae.Code}
		_, _ = a.DB.C(store.CDrafts).UpdateOne(ctx, bson.M{"_id": d.ID, "workspaceId": a.WorkspaceID(), "state": models.DraftApproving},
			bson.M{"$set": bson.M{"state": back, "tried": try}, "$unset": bson.M{"approvingAt": ""}})
		return nil, err
	}
	// Priced again, with today's facts: nothing that costs money is approved unseen.
	cost := draftPrice(&before)
	if cost.USD > 0 {
		return fail(apperr.New(apperr.Unauthorized, "Approving this would now cost %s at outside services, so it isn't made from a draft: start it yourself after seeing the price.", cost.About))
	}
	inCut, err := approveDraft(ctx, a, &before, dec)
	if err != nil {
		return fail(err)
	}
	state := models.DraftApproved
	if inCut {
		state = models.DraftInCut
		dec.State = models.DraftInCut
	}
	if _, err := a.DB.C(store.CDrafts).UpdateOne(ctx, bson.M{"_id": d.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$set": bson.M{"state": state, "decision": dec, "cost": cost}, "$unset": bson.M{"tried": "", "approvingAt": ""}}); err != nil {
		return nil, err
	}
	before.State, before.Decision, before.Tried, before.ApprovingAt, before.Cost = state, dec, nil, nil, &cost
	a.Audit(ctx, "draft."+state, "draft", d.ID, d.ProjectID, map[string]any{"agent": d.Agent, "note": dec.Note, "revisionId": dec.RevisionID})
	return viewDraft(ctx, a, &before), nil
}

// approveDraft carries out a person's approval: a change is made as them; a
// version is checked again with today's facts (the same check a person's own
// save gets) and goes into the cut — its own version when nothing changed,
// else a new version keeping both changes where they don't overlap —
// approved by them. inCut: its change was in the cut already (D252).
func approveDraft(ctx context.Context, a *app.App, d *models.Draft, dec *models.DraftDecision) (inCut bool, err error) {
	if d.Kind == models.DraftChange {
		op, ok := Get(d.Op)
		if !ok {
			return false, apperr.New(apperr.NotFound, "This version of MetaRush can't make that change any more.").WithDetails(map[string]any{"op": d.Op})
		}
		if why := whyNotDrafted(op); why != "" || versionDraftOps[op.Name] != "" {
			return false, apperr.New(apperr.Unauthorized, "This change can't be made from a draft. %s", why)
		}
		// What it does is measured again: approving does what was shown, or nothing.
		var input map[string]any
		if err := json.Unmarshal([]byte(d.Input), &input); err != nil {
			return false, err
		}
		if err := recheckChange(ctx, a, d, input); err != nil {
			return false, err
		}
		if _, err := Call(ctx, a, op.Name, json.RawMessage(d.Input), CallerFrom(ctx)); err != nil {
			return false, err
		}
		dec.Result = changeDone(d)
		return false, nil
	}
	r, p, err := getRevision(ctx, a, d.RevisionID)
	if err != nil {
		return false, err
	}
	head := p.Branches[r.Branch].Head
	if h, ok := headCarries(ctx, a, head, d); ok && head != r.ID {
		// Someone saved a change on top of the draft's version: it is in already.
		dec.RevisionID, dec.RevisionNumber = h.ID, h.Number
		dec.Result = fmt.Sprintf("Its change is in the cut already, in version %d.", h.Number)
		return true, nil
	}
	now := time.Now().UTC()
	actor := a.Actor
	approval := models.Approval{State: models.ApprovalApproved, By: &actor, At: &now, Note: dec.Note}
	put := r
	if head != r.ID {
		// The change against the version it was written on, checked with today's
		// facts (a clip put in the Trash meanwhile, a graphic withdrawn…) and
		// combined with anything saved since.
		plan := editspec.Plan{Instruction: r.Instruction, Rationale: r.Rationale, Origin: r.Origin, Note: r.Note}
		if r.Branch != "main" {
			plan.Branch = r.Branch
		}
		var parent models.Revision
		switch {
		case r.ParentID == "":
			plan.ProjectID, plan.Spec = r.ProjectID, editspec.Clone(&r.Spec)
		case a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), r.ParentID, &parent) != nil:
			return false, apperr.New(apperr.NotFound, "The version this draft was written on isn't in this workspace any more.")
		default:
			plan.BaseRevisionID = parent.ID
			if ops, err := editspec.DiffOps(&parent.Spec, &r.Spec); err == nil {
				plan.Ops = ops
			} else if head == r.ParentID {
				plan.Spec = editspec.Clone(&r.Spec)
			} else {
				return false, apperr.New(apperr.RevisionConflict, "Someone saved a newer version of %s, and this draft changes the whole cut, so the two can't be combined. The draft stays waiting.", branchWords(p.Name, r.Branch)).
					WithHint("Say Not quite, so the app makes it again on the latest version.")
			}
		}
		raw, _ := json.Marshal(plan)
		prep, err := prepare(ctx, a, raw, false)
		if err != nil {
			return false, approveConflict(ctx, a, err, p, r)
		}
		if len(prep.errors) > 0 {
			return false, apperr.New(apperr.Validation, "This draft can't go in the cut as it stands today. %s The draft stays waiting.", validationSummary(prep.errors)).
				WithDetails(map[string]any{"errors": prep.errors}).WithHint("Fix it on the draft's version, or say Not quite so the app makes it again.")
		}
		if head == r.ParentID && editspec.Hash(prep.spec) == r.SpecHash {
			// Nothing changed since, and it still checks: the draft's own version goes in.
			field := "branches." + r.Branch
			filter := bson.M{"_id": p.ID, "workspaceId": a.WorkspaceID()}
			if head != "" {
				filter[field+".head"] = head
			} else {
				filter[field] = bson.M{"$exists": false}
			}
			res, err := a.DB.C(store.CProjects).UpdateOne(ctx, filter, bson.M{"$set": bson.M{field + ".head": r.ID, field + ".updatedAt": now, field + ".updatedBy": a.Actor, "updatedAt": now}})
			if err != nil {
				return false, err
			}
			if res.MatchedCount == 0 {
				return false, apperr.New(apperr.RevisionConflict, "Someone saved another change to %s at the same moment; the draft stays waiting: approve it again.", branchWords(p.Name, r.Branch))
			}
			a.Audit(ctx, "revision.create", "revision", r.ID, r.ProjectID, map[string]any{"number": r.Number, "parent": r.ParentID, "draft": d.ID, "agent": d.Agent})
			reconcileRevisionTodos(ctx, a, r, p, true)
			_ = faces.MarkCuts(ctx, a, r.ProjectID)
			closeIncludedDrafts(ctx, a, r, "approved")
		} else {
			// The change is the app's: saved in its name, approved in the person's.
			prep.includes = r.Includes
			made := *a
			made.Actor = r.CreatedBy
			saved, err := commitRevision(ctx, &made, prep)
			if err != nil {
				return false, approveConflict(ctx, a, err, p, r)
			}
			nr, _, err := getRevision(ctx, a, saved.Revision.ID)
			if err != nil {
				return false, err
			}
			put = nr
		}
	}
	if _, err := a.DB.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": put.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"approval": approval}}); err != nil {
		return false, err
	}
	a.Audit(ctx, "revision.approved", "revision", put.ID, put.ProjectID, map[string]any{"note": dec.Note, "draft": d.ID})
	dec.RevisionID, dec.RevisionNumber = put.ID, put.Number
	if put.ID == r.ID {
		dec.Result = fmt.Sprintf("Version %d is in the cut and approved.", r.Number)
	} else {
		dec.Result = fmt.Sprintf("Its change is in the cut as version %d, with the newer changes kept, and approved.", put.Number)
	}
	return false, nil
}

// approveConflict says plainly why a draft's change can't go on top of what
// was saved since: which version, and the clips both change, by their names.
func approveConflict(ctx context.Context, a *app.App, err error, p *models.Project, r *models.Revision) error {
	ae := apperr.From(err)
	if ae.Code != apperr.RevisionConflict {
		return err
	}
	details, _ := ae.Details.(map[string]any)
	var head models.Revision
	headID, _ := details["headRevisionId"].(string)
	if headID == "" || a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), headID, &head) != nil {
		return apperr.New(apperr.RevisionConflict, "Someone saved a newer version of %s that changes the same parts as this draft. The draft stays waiting.", branchWords(p.Name, r.Branch)).
			WithDetails(ae.Details).WithHint("Say Not quite, so the app makes it again on the latest version.")
	}
	var names []string
	if cs, ok := details["conflicts"].([]string); ok {
		names = clipNames(ctx, a, &head.Spec, cs)
	}
	what := "the same parts of the cut"
	if len(names) > 0 {
		what = joinAnd(names)
	}
	return apperr.New(apperr.RevisionConflict, "Version %d, saved after this draft, also changes %s. The draft stays waiting.", head.Number, what).
		WithDetails(ae.Details).WithHint("Say Not quite, so the app makes it again on the latest version.")
}

// clipNames names a cut's clips (by their IDs in it) by their footage's
// titles: “Garage interview”; other parts by what they are.
func clipNames(ctx context.Context, a *app.App, spec *editspec.Spec, ids []string) []string {
	var out []string
	for _, id := range ids {
		name := ""
		for i, c := range spec.Clips {
			if c.ID == id {
				name = fmt.Sprintf("clip %d", i+1)
				var as models.Asset
				if c.AssetID != "" && a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), c.AssetID, &as) == nil {
					name = fmt.Sprintf("clip %d (%s)", i+1, q(as.DisplayTitle()))
				}
			}
		}
		if name == "" {
			if items := plainItems([]string{id}, spec); len(items) > 0 {
				name = items[0]
			}
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// draftForApproval is the waiting draft a version holds (nil when it holds none).
func draftForApproval(ctx context.Context, a *app.App, r *models.Revision) *models.Draft {
	if r.DraftID == "" {
		return nil
	}
	d, err := getDraft(ctx, a, r.DraftID)
	if err != nil {
		return nil
	}
	return d
}

// unfoldDraftBase turns a change written on a draft's version (never in the
// cut) into the draft and the change together, against the version the draft
// was written on: saving it (or another draft built on it) never loses the
// draft's own changes (D248). A draft of a first cut makes it a whole cut.
func unfoldDraftBase(ctx context.Context, a *app.App, p *prepared, allowUnlock bool) error {
	dr := p.base
	// What is saved carries the draft's change (and what its version carried).
	p.includes = appendNew(append([]string(nil), dr.Includes...), dr.DraftID)
	next := p.plan.Spec
	if next == nil {
		s, _, err := editspec.ApplyOps(&dr.Spec, p.plan.Ops, allowUnlock)
		if err != nil {
			return err
		}
		next = s
	} else {
		next = editspec.Clone(next)
	}
	if p.plan.Branch == "" {
		p.branch = dr.Branch
		if dr.Branch != "main" {
			p.plan.Branch = dr.Branch
		}
	}
	if dr.ParentID == "" {
		p.plan.BaseRevisionID, p.plan.ProjectID, p.plan.Ops, p.plan.Spec = "", dr.ProjectID, nil, next
		p.base = nil
		return nil
	}
	var parent models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), dr.ParentID, &parent); err != nil {
		return apperr.New(apperr.NotFound, "The version this draft was written on isn't in this workspace any more.").WithDetails(map[string]any{"revisionId": dr.ParentID})
	}
	if ops, err := editspec.DiffOps(&parent.Spec, next); err == nil {
		p.plan.Ops, p.plan.Spec = ops, nil
	} else {
		p.plan.Ops, p.plan.Spec = nil, next
	}
	p.plan.BaseRevisionID = parent.ID
	p.base = &parent
	return nil
}

// approveDraftVersion is revision.approve of a version a draft holds.
func approveDraftVersion(ctx context.Context, a *app.App, d *models.Draft, in *RevisionApproveIn) (*RevisionSummary, error) {
	decision := DecideApprove
	switch {
	case in.Reject && strings.TrimSpace(in.Note) != "":
		decision = DecideNotQuite
	case in.Reject:
		decision = DecideTurnDown
	}
	v, err := decideDraft(ctx, a, &DraftDecideIn{ID: d.ID, Decision: decision, Note: in.Note})
	if err != nil {
		return nil, err
	}
	id := d.RevisionID
	if v.Decision != nil && v.Decision.RevisionID != "" {
		id = v.Decision.RevisionID
	}
	r, p, err := getRevision(ctx, a, id)
	if err != nil {
		return nil, err
	}
	s := summarizeRev(a, *r, p.Branches[r.Branch].Head)
	if decision == DecideApprove {
		s.Waiting = OpenSignoffs(ctx, a, p, r.Branch)
		s.Reminder = SignoffReminder(s.Waiting)
	}
	return &s, nil
}

// cleanText takes control and format characters out of an app's words:
// terminal escapes (a clipboard write), bells, carriage returns, bidi
// overrides and isolates, zero-width and other invisible characters
// (Unicode's Cc and Cf, line and paragraph separators). Newlines and tabs stay.
func cleanText(s string) string {
	hidden := func(r rune) bool {
		return r != '\n' && r != '\t' && (unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029)
	}
	if !strings.ContainsFunc(s, hidden) {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if !hidden(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cleanValue is cleanText through every string of a JSON value.
func cleanValue(v any) any {
	switch x := v.(type) {
	case string:
		return cleanText(x)
	case []any:
		for i := range x {
			x[i] = cleanValue(x[i])
		}
		return x
	case map[string]any:
		for k, e := range x {
			x[k] = cleanValue(e)
		}
		return x
	}
	return v
}

func appendNew(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

// closeIncludedDrafts closes the drafts a version just put in the cut carries
// (a person saved a change on top of their versions, D252): their change is
// in the cut, so they leave the desk as "in the cut".
func closeIncludedDrafts(ctx context.Context, a *app.App, rev *models.Revision, how string) {
	if len(rev.Includes) == 0 {
		return
	}
	who := a.Actor.UserName
	if who == "" {
		who = "Someone"
	}
	dec := models.DraftDecision{State: models.DraftInCut, By: a.Actor, At: time.Now().UTC(), RevisionID: rev.ID, RevisionNumber: rev.Number,
		Result: fmt.Sprintf("Its change is in the cut as part of version %d, which %s %s.", rev.Number, who, how)}
	res, _ := a.DB.C(store.CDrafts).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "_id": bson.M{"$in": rev.Includes},
		"state": bson.M{"$in": bson.A{models.DraftWaiting, models.DraftNotQuite, models.DraftTurnedDown}}},
		bson.M{"$set": bson.M{"state": models.DraftInCut, "decision": dec}, "$unset": bson.M{"tried": ""}})
	if res != nil && res.ModifiedCount > 0 {
		a.Audit(ctx, "draft.in_cut", "revision", rev.ID, rev.ProjectID, map[string]any{"drafts": rev.Includes})
	}
}

// headCarries reports whether the cut's latest version (head) is, or was made
// on top of, a draft's version: its change is in the cut already (D252).
func headCarries(ctx context.Context, a *app.App, head string, d *models.Draft) (*models.Revision, bool) {
	id := head
	for i := 0; id != "" && i < 200; i++ {
		var r models.Revision
		if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()},
			options.FindOne().SetProjection(bson.M{"parentId": 1, "includes": 1, "number": 1, "projectId": 1})).Decode(&r); err != nil {
			return nil, false
		}
		if r.ID == d.RevisionID || slicesContains(r.Includes, d.ID) {
			var h models.Revision
			_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &h)
			return &h, true
		}
		id = r.ParentID
	}
	return nil, false
}

func slicesContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
