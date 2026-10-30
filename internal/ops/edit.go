package ops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// EditCheckIn is a dry run of a plan.
type EditCheckIn struct {
	Plan        json.RawMessage `json:"plan" jsonschema:"type=object,description=An edit plan (see metarush schema show edit-plan)."`
	AllowUnlock bool            `json:"allowUnlock,omitempty" jsonschema:"description=Permit changes to locked items. Only when the user explicitly asked."`
}

// EditCheckOut reports what applying the plan would do.
type EditCheckOut struct {
	Valid          bool             `json:"valid"`
	ProjectID      string           `json:"projectId"`
	Branch         string           `json:"branch"`
	BaseRevisionID string           `json:"baseRevisionId,omitempty"`
	HeadRevisionID string           `json:"headRevisionId,omitempty"`
	Rebased        bool             `json:"rebased"`
	Conflicts      []string         `json:"conflicts,omitempty"`
	Errors         []editspec.Issue `json:"errors"`
	Warnings       []editspec.Issue `json:"warnings"`
	Diff           *editspec.Diff   `json:"diff,omitempty"`
	Duration       float64          `json:"duration"`
	SpecHash       string           `json:"specHash,omitempty"`
	ConfirmToken   string           `json:"confirmToken,omitempty"`
	ExpiresAt      *time.Time       `json:"expiresAt,omitempty"`
	Next           string           `json:"next"`
}

// EditApplyIn applies a plan, creating an immutable revision.
type EditApplyIn struct {
	Plan         json.RawMessage `json:"plan" jsonschema:"type=object"`
	ConfirmToken string          `json:"confirmToken,omitempty" jsonschema:"description=Token from edit check for this exact plan."`
	AllowUnlock  bool            `json:"allowUnlock,omitempty"`
}

// EditApplyOut is the new revision.
type EditApplyOut struct {
	Revision RevisionSummary  `json:"revision"`
	Diff     editspec.Diff    `json:"diff"`
	Warnings []editspec.Issue `json:"warnings"`
	Rebased  bool             `json:"rebased"`
	URL      string           `json:"url"`
}

// RevisionSummary is a revision without its full spec.
type RevisionSummary struct {
	ID          string           `json:"id"`
	ProjectID   string           `json:"projectId"`
	Branch      string           `json:"branch"`
	Number      int              `json:"number"`
	ParentID    string           `json:"parentId,omitempty"`
	RebasedFrom string           `json:"rebasedFrom,omitempty"`
	Title       string           `json:"title,omitempty"`
	Instruction string           `json:"instruction,omitempty"`
	Rationale   string           `json:"rationale,omitempty"`
	Origin      string           `json:"origin,omitempty"`
	Duration    float64          `json:"duration"`
	Clips       int              `json:"clips"`
	Overlays    int              `json:"overlays"`
	Output      editspec.Output  `json:"output"`
	SpecHash    string           `json:"specHash"`
	Approval    models.Approval  `json:"approval"`
	Warnings    []editspec.Issue `json:"warnings,omitempty"`
	IsHead      bool             `json:"isHead"`
	CreatedBy   models.Actor     `json:"createdBy"`
	CreatedAt   time.Time        `json:"createdAt"`
	URL         string           `json:"url"`
	// Waiting are the sign-offs still waiting when a version is approved
	// (D166): a reminder, never a lock.
	Waiting  []models.Signoff `json:"waiting,omitempty"`
	Reminder string           `json:"reminder,omitempty"`
	// Note says in a few words what the version changed (D150); NoteBy who
	// wrote it (metarush, claude, person).
	Note   string `json:"note,omitempty"`
	NoteBy string `json:"noteBy,omitempty"`
	// ToldClaude is the conversation a rejection's note went to (D186): the
	// one that made the version; absent when none did.
	ToldClaude *NoteSent `json:"toldClaude,omitempty" jsonschema:"description=Rejected with a note: the Claude Code conversation the note went to (the one that made the version)\\, if any."`
	// ByClaude: Claude made this version (D188); ReviewAsked: someone asked to
	// have it reviewed. Either puts a draft in "Awaiting your review".
	ByClaude    bool              `json:"byClaude,omitempty"`
	ReviewAsked *models.ReviewAsk `json:"reviewAsked,omitempty"`
	// ByAgent names the AI app that made it (claude, codex, cursor…, D249)
	// and AgentName says it for people ("Codex").
	ByAgent   string `json:"byAgent,omitempty"`
	AgentName string `json:"agentName,omitempty"`
	// DraftID: an AI app's draft holds this version; it isn't in the cut
	// unless a person approved the draft (D248).
	DraftID string `json:"draftId,omitempty"`
}

func summarizeRev(a *app.App, r models.Revision, head string) RevisionSummary {
	s := RevisionSummary{ID: r.ID, ProjectID: r.ProjectID, Branch: r.Branch, Number: r.Number, ParentID: r.ParentID, RebasedFrom: r.RebasedFrom, Note: r.Note, NoteBy: r.NoteBy,
		Title: r.Spec.Title, Instruction: r.Instruction, Rationale: r.Rationale, Origin: r.Origin, Duration: r.Duration, Clips: len(r.Spec.Clips), Overlays: len(r.Spec.Overlays),
		Output: r.Spec.Output, SpecHash: r.SpecHash, Approval: r.Approval, Warnings: r.Warnings, IsHead: r.ID == head, CreatedBy: r.CreatedBy, CreatedAt: r.CreatedAt,
		URL: dashboardURL(a, fmt.Sprintf("/p/%s/r/%s", r.ProjectID, r.ID)), ByClaude: madeByClaude(&r), ReviewAsked: r.ReviewAsked, DraftID: r.DraftID}
	if s.ByAgent = madeByAgent(&r); s.ByAgent != "" {
		s.AgentName = AgentWords(s.ByAgent)
	}
	return s
}

// rewordWarnings says a version's saved warnings in today's words: the list
// stays exactly as it was saved (the same codes, places and severities), and
// a message is replaced only where checking the same cut again finds the same
// problem in the same place. Versions saved before the wording changed (D109)
// then read like new ones on the version page.
func rewordWarnings(ctx context.Context, a *app.App, r *models.Revision) []editspec.Issue {
	if len(r.Warnings) == 0 {
		return r.Warnings
	}
	spec := editspec.Clone(&r.Spec)
	editspec.Normalize(spec)
	facts, err := loadFacts(ctx, a, r.ProjectID, spec)
	if err != nil {
		return r.Warnings
	}
	now := map[string]string{}
	for _, is := range editspec.Validate(spec, facts, nil, true) {
		now[is.Code+"\x00"+is.Path] = is.Message
	}
	out := make([]editspec.Issue, len(r.Warnings))
	for i, w := range r.Warnings {
		out[i] = w
		if m, ok := now[w.Code+"\x00"+w.Path]; ok {
			out[i].Message = m
		} else {
			// Fixed since (the music's rights recorded, a transcript made): keep
			// the saved words, without the commands older versions put in them.
			out[i].Message = withoutCommands(w.Message)
		}
	}
	return out
}

var (
	savedParenCmd  = regexp.MustCompile(`\s*\([^()]*` + "`" + `[^()]*\)`)
	savedClauseCmd = regexp.MustCompile(`\s*;[^;.]*` + "`" + `[^;.]*`)
	savedTicks     = regexp.MustCompile("`[^`]*`")
)

// withoutCommands takes the commands out of a saved message ("…recorded (set
// them on the file with `metarush asset rights`)" → "…recorded."), with the
// parenthesis or clause that held them, and makes it read as a sentence.
func withoutCommands(m string) string {
	if !strings.Contains(m, "`") {
		return m
	}
	m = savedParenCmd.ReplaceAllString(m, "")
	m = savedClauseCmd.ReplaceAllString(m, "")
	m = strings.TrimSpace(savedTicks.ReplaceAllString(m, ""))
	if m == "" {
		return m
	}
	if r := []rune(m); r[0] >= 'a' && r[0] <= 'z' {
		m = strings.ToUpper(string(r[0])) + string(r[1:])
	}
	if !strings.ContainsAny(m[len(m)-1:], ".?!") {
		m += "."
	}
	return m
}

func listRevisions(ctx context.Context, a *app.App, projectID, branch string, limit int64) ([]RevisionSummary, error) {
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "That project isn't in this workspace.").WithDetails(map[string]any{"projectId": projectID})
	}
	filter := bson.M{"projectId": projectID}
	if branch != "" {
		filter["branch"] = branch
	}
	var rs []models.Revision
	if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), filter, &rs, options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(limit)); err != nil {
		return nil, err
	}
	heads := map[string]bool{}
	for _, h := range p.Branches {
		heads[h.Head] = true
	}
	out := make([]RevisionSummary, 0, len(rs))
	for _, r := range rs {
		s := summarizeRev(a, r, "")
		s.IsHead = heads[r.ID]
		out = append(out, s)
	}
	return out, nil
}

// prepared is a fully evaluated plan.
type prepared struct {
	plan      editspec.Plan
	planHash  string
	project   models.Project
	branch    string
	head      string
	base      *models.Revision
	parentID  string
	spec      *editspec.Spec
	rebased   bool
	conflicts []string
	errors    []editspec.Issue
	warnings  []editspec.Issue
	diff      *editspec.Diff
	facts     *dbFacts
	// includes: the drafts whose changes this change carries, because it was
	// written on their versions (D252).
	includes []string
}

func prepare(ctx context.Context, a *app.App, raw json.RawMessage, allowUnlock bool) (*prepared, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, invalid("There's no change to check (the plan is empty).")
	}
	p := &prepared{errors: []editspec.Issue{}, warnings: []editspec.Issue{}}
	if issues := editspec.ValidatePlanJSON(raw); len(issues) > 0 {
		p.errors = issues
		return p, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p.plan); err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, err, "The change isn't written the way MetaRush reads it (a field is unknown or has the wrong kind of value).").
			WithDetails(map[string]any{"error": err.Error()})
	}
	canon, _ := json.Marshal(p.plan)
	sum := sha256.Sum256(canon)
	p.planHash = hex.EncodeToString(sum[:])
	if p.plan.Instruction == "" && len(p.plan.Ops) > 0 && strings.HasPrefix(p.plan.Origin, "timeline:") {
		// Timeline saves have no request of their own: say what they do.
		p.plan.Instruction = editspec.OpsSummary(p.plan.Ops)
	}
	if (p.plan.Spec == nil) == (len(p.plan.Ops) == 0) {
		p.errors = append(p.errors, editspec.Issue{Severity: "error", Code: "plan_shape", Message: "A change is either a whole cut (spec) or a list of changes (ops), not both or neither."})
		return p, nil
	}
	p.branch = p.plan.Branch
	if p.branch == "" {
		p.branch = "main"
	}
	projectID := p.plan.ProjectID
	if p.plan.BaseRevisionID != "" {
		var base models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), p.plan.BaseRevisionID, &base); err != nil {
			return nil, apperr.New(apperr.NotFound, "The version this change builds on isn't in this workspace (baseRevisionId).").
				WithDetails(map[string]any{"baseRevisionId": p.plan.BaseRevisionID})
		}
		if projectID != "" && projectID != base.ProjectID {
			return nil, apperr.New(apperr.InvalidInput, "The version this change builds on belongs to another project (baseRevisionId and projectId don't match).").
				WithDetails(map[string]any{"baseRevisionId": base.ID, "baseProjectId": base.ProjectID, "projectId": projectID})
		}
		projectID = base.ProjectID
		p.base = &base
		if base.DraftID != "" {
			// A change written on an AI app's draft (D248), a version that
			// isn't in the cut: the draft and the change together, against
			// the version the draft was written on.
			if err := unfoldDraftBase(ctx, a, p, allowUnlock); err != nil {
				var ae *apperr.Error
				if errors.As(err, &ae) {
					return nil, ae
				}
				return p.opError(err)
			}
			if p.base == nil {
				projectID = base.ProjectID
			}
		}
	}
	if projectID == "" {
		return nil, invalid("This change doesn't say which project it's for (projectId, or baseRevisionId for a change to an existing cut).")
	}
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p.project); err != nil {
		return nil, apperr.New(apperr.NotFound, "The project this change is for isn't in this workspace (projectId).").WithDetails(map[string]any{"projectId": projectID})
	}
	if h, ok := p.project.Branches[p.branch]; ok {
		p.head = h.Head
	} else if p.branch != "main" && p.base != nil {
		p.head = "" // new branch forks from base
	}
	branchExists := p.project.Branches != nil && p.project.Branches[p.branch].Head != ""
	switch {
	case p.base == nil && branchExists:
		return nil, apperr.New(apperr.RevisionConflict, "%s already has a cut, so this change has to say which version it builds on (baseRevisionId).", upperFirst(branchWords(p.project.Name, p.branch))).
			WithDetails(map[string]any{"headRevisionId": p.head}).WithHint("Start from the latest version and write the change against it.").
			WithCommand("metarush revision show " + p.head + " --json")
	case p.base == nil && len(p.plan.Ops) > 0:
		p.errors = append(p.errors, editspec.Issue{Severity: "error", Code: "ops_without_base", Message: "This change doesn't say which version it builds on (baseRevisionId); a project's first cut has to be a whole cut (spec)."})
		return p, nil
	}
	var parentSpec *editspec.Spec
	switch {
	case p.base == nil: // first revision on this branch
		p.spec = editspec.Clone(p.plan.Spec)
	case !branchExists || p.base.ID == p.head: // forking a new branch, or up to date
		p.parentID = p.base.ID
		parentSpec = &p.base.Spec
		if p.plan.Spec != nil {
			p.spec = editspec.Clone(p.plan.Spec)
		} else {
			s, _, err := editspec.ApplyOps(&p.base.Spec, p.plan.Ops, allowUnlock)
			if err != nil {
				return p.opError(err)
			}
			p.spec = s
		}
	default: // the branch moved since the plan was written
		var head models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), p.head, &head); err != nil {
			return nil, err
		}
		if p.plan.Spec != nil {
			return nil, apperr.New(apperr.RevisionConflict, "While this change was being written, %s saved version %d; this change replaces the whole cut, so it can't be combined with theirs and nothing was saved.", whoSaved(a, &head), head.Number).
				WithDetails(map[string]any{"headRevisionId": head.ID, "baseRevisionId": p.base.ID}).
				WithHint("Make the change again on the latest version, or save it as a separate version (a new branch).")
		}
		s, conflicts, err := editspec.Rebase(&p.base.Spec, &head.Spec, p.plan.Ops, allowUnlock)
		if err != nil {
			return p.opError(err)
		}
		if len(conflicts) > 0 {
			return nil, apperr.New(apperr.RevisionConflict, "While this change was being written, %s saved version %d, which also changes %s; nothing was saved.", whoSaved(a, &head), head.Number, joinAnd(plainItems(conflicts, &head.Spec))).
				WithDetails(map[string]any{"headRevisionId": head.ID, "baseRevisionId": p.base.ID, "conflicts": conflicts}).
				WithHint("Compare the two versions and make the change again on the latest one, or save it as a separate version (a new branch).")
		}
		p.spec, p.rebased, p.parentID, parentSpec = s, true, head.ID, &head.Spec
	}
	if p.plan.Spec != nil {
		var style *editspec.HouseStyle
		if parentSpec == nil {
			// A brand-new cut starts with the brand of the Content Settings
			// its project uses (D82, D165, D193) and their house style (D199).
			applyKitToNewCut(ctx, a, p.spec, p.project.ID)
			style, _, _ = content.StyleFor(ctx, a, p.project.ID)
		}
		// A new cut gets the current sound at cuts (speech leveling, click-free
		// straight cuts) and framing, or the house style's; a full rewrite
		// keeps the settings of the cut it replaces.
		editspec.NewCutDefaults(p.spec, parentSpec, style)
	}
	// Graphics that name a role are drawn with the project's layout pack
	// (D264): roles without a template, every role of a new cut, and the call
	// layouts of conversations this change brings in.
	if print := fillPackRoles(ctx, a, p.project.ID, p.spec, parentSpec); print != "" {
		// The confirmation binds the pack the change was filled from too.
		sum := sha256.Sum256([]byte(p.planHash + "|pack:" + print))
		p.planHash = hex.EncodeToString(sum[:])
	}
	editspec.Normalize(p.spec)
	// Footage this change puts into the cut for the first time plays its fixed
	// copy when it has one; saved versions keep what they play (D152).
	pinNewFootage(ctx, a, parentSpec, p.spec, editspec.ExplicitFixed(p.plan.Ops))
	// Changed words (D308): only Put it in the cut adds them, or a person whose
	// change still holds. A new cut made from an older version has only the
	// ones its cut still plays (a person may have taken the others out).
	saidBase := parentSpec
	if p.base != nil && parentSpec == &p.base.Spec {
		still, err := saidStillIn(ctx, a, &p.project, p.base)
		if err != nil {
			return nil, err
		}
		saidBase = still
	}
	if err := saidAllowed(ctx, a, p.project.ID, saidBase, p.spec); err != nil {
		return nil, err
	}
	if len(p.plan.Ops) > 0 {
		// Ops are checked one by one, not against the schema; check the cut they
		// produce (e.g. updateClip {"speed": 9} is outside 0.25–4).
		b, _ := json.Marshal(map[string]any{"spec": p.spec})
		p.errors = append(p.errors, editspec.ValidatePlanJSON(b)...)
	}
	facts, err := loadFacts(ctx, a, p.project.ID, p.spec)
	if err != nil {
		return nil, err
	}
	p.facts = facts
	for _, is := range editspec.Validate(p.spec, facts, parentSpec, allowUnlock) {
		if is.Severity == "error" {
			p.errors = append(p.errors, is)
		} else {
			p.warnings = append(p.warnings, is)
		}
	}
	if parentSpec != nil {
		d := editspec.Compare(parentSpec, p.spec)
		p.diff = &d
	} else {
		empty := &editspec.Spec{Output: p.spec.Output}
		d := editspec.Compare(empty, p.spec)
		p.diff = &d
	}
	return p, nil
}

func (p *prepared) opError(err error) (*prepared, error) {
	var le *editspec.LockedError
	if errors.As(err, &le) {
		what := editspec.LockedWords(le.ID)
		if p.base != nil {
			if items := plainItems([]string{le.ID}, &p.base.Spec); len(items) > 0 && le.ID != "mix" && le.ID != "voiceover" && le.ID != "captions" {
				what = joinAnd(items)
			}
		}
		return nil, apperr.New(apperr.Locked, "This change touches %s, which is locked.", what).WithDetails(map[string]any{"locked": le.ID, "change": le.Op + 1}).
			WithHint("Locked parts stay as they are unless the person asks to unlock them.")
	}
	msg := err.Error()
	if !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	p.errors = append(p.errors, editspec.Issue{Severity: "error", Code: "invalid_op", Message: msg})
	return p, nil
}

// branchWords names a branch for people, inside a sentence: the project
// ("Garden story"), or its branch ("the short-version branch of Garden story").
func branchWords(project, branch string) string {
	if project == "" {
		project = "this project"
	}
	if branch == "" || branch == "main" {
		return project
	}
	return "the " + branch + " branch of " + project
}

// validationSummary is the first problem that stops a save, and how many more
// there are (all of them are in the error's details).
func validationSummary(errs []editspec.Issue) string {
	if len(errs) == 0 {
		return ""
	}
	msg := strings.TrimSpace(errs[0].Message)
	if msg != "" && !strings.HasSuffix(msg, ".") {
		msg += "."
	}
	switch n := len(errs) - 1; {
	case n == 1:
		msg += " There is 1 more problem."
	case n > 1:
		msg += fmt.Sprintf(" There are %d more problems.", n)
	}
	return msg
}

// confirm tokens bind the exact plan hash + base + expiry with an installation secret.
func confirmKey(a *app.App) ([]byte, error) {
	p := filepath.Join(a.Cfg.StateDir(), "confirm.key")
	if b, err := os.ReadFile(p); err == nil && len(b) == 32 {
		return b, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, b, 0o600); err != nil {
		return nil, err
	}
	return b, nil
}

func mintToken(a *app.App, planHash, base string, exp time.Time) (string, error) {
	k, err := confirmKey(a)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, k)
	fmt.Fprintf(m, "%s|%s|%d", planHash, base, exp.Unix())
	return fmt.Sprintf("ct_%d_%s", exp.Unix(), hex.EncodeToString(m.Sum(nil))[:32]), nil
}

// checkToken refuses a save without a matching check. The messages say what
// happened in words (sheetTokenMessage keys on "expired" and "has changed");
// the command that checks is kept in Details.
func checkToken(a *app.App, tok, planHash, base string) error {
	check := "metarush edit check --plan <file>"
	parts := strings.Split(tok, "_")
	if len(parts) != 3 || parts[0] != "ct" {
		return apperr.New(apperr.ConfirmRequired, "This change hasn't been checked yet.").
			WithHint("Check it first and look at what it changes, then save it.").WithCommand(check)
	}
	unix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return apperr.New(apperr.ConfirmRequired, "This change hasn't been checked yet.").
			WithHint("Check it first and look at what it changes, then save it.").WithCommand(check)
	}
	exp := time.Unix(unix, 0)
	if time.Now().After(exp) {
		return apperr.New(apperr.ConfirmRequired, "The check of this change has expired (a check lasts 30 minutes).").
			WithHint("Check it again, then save it.").WithCommand(check)
	}
	want, err := mintToken(a, planHash, base, exp)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(want), []byte(tok)) {
		return apperr.New(apperr.ConfirmRequired, "This change has changed since it was checked, so it wasn't saved.").
			WithHint("Check the change you want to save, then save that one.").WithCommand(check)
	}
	return nil
}

// checkPlan is edit check: validation, the diff against the base and a
// confirm token for exactly this plan.
func checkPlan(ctx context.Context, a *app.App, plan json.RawMessage, allowUnlock bool) (*EditCheckOut, error) {
	return checkPlanBinding(ctx, a, plan, allowUnlock, "")
}

// bindPlan makes a plan's confirmation also cover what a change does outside
// the plan (extra: the files an import adds).
func bindPlan(p *prepared, extra string) {
	if extra != "" {
		sum := sha256.Sum256([]byte(p.planHash + "|also:" + extra))
		p.planHash = hex.EncodeToString(sum[:])
	}
}

// checkPlanBinding is checkPlan whose token also covers extra.
func checkPlanBinding(ctx context.Context, a *app.App, plan json.RawMessage, allowUnlock bool, extra string) (*EditCheckOut, error) {
	p, err := prepare(ctx, a, plan, allowUnlock)
	if err != nil {
		return nil, err
	}
	bindPlan(p, extra)
	out := &EditCheckOut{Valid: len(p.errors) == 0, ProjectID: p.project.ID, Branch: p.branch, HeadRevisionID: p.head, Rebased: p.rebased,
		Errors: p.errors, Warnings: p.warnings, Diff: p.diff}
	if p.plan.Spec != nil && p.project.ID != "" {
		reconcileBriefTodos(ctx, a, &p.project) // a story edit was requested: ask for a missing goal/audience/tone
	}
	if p.base != nil {
		out.BaseRevisionID = p.base.ID
	}
	if p.spec != nil {
		_, total := editspec.Timeline(p.spec)
		out.Duration = float64(total) / float64(p.spec.Output.FPS)
		out.SpecHash = editspec.Hash(p.spec)
	}
	if out.Valid {
		exp := time.Now().Add(30 * time.Minute).UTC()
		tok, err := mintToken(a, p.planHash, out.BaseRevisionID, exp)
		if err != nil {
			return nil, err
		}
		out.ConfirmToken, out.ExpiresAt = tok, &exp
		out.Next = "Show the person what changes and the warnings; if they agree, save the change."
	} else {
		out.Next = "Fix the problems, then check the change again."
	}
	return out, nil
}

// verifyPlanToken checks a confirm token against a plan (and extra, as
// checkPlanBinding bound it) without saving it.
func verifyPlanToken(ctx context.Context, a *app.App, plan json.RawMessage, token, extra string) error {
	p, err := prepare(ctx, a, plan, false)
	if err != nil {
		return err
	}
	bindPlan(p, extra)
	base := ""
	if p.base != nil {
		base = p.base.ID
	}
	return checkToken(a, token, p.planHash, base)
}

// applyPlan is edit apply: the plan becomes a new revision once its confirm
// token matches (the dashboard confirms in its own dialog instead).
func applyPlan(ctx context.Context, a *app.App, plan json.RawMessage, token string, allowUnlock bool) (*EditApplyOut, error) {
	p, err := prepare(ctx, a, plan, allowUnlock)
	if err != nil {
		return nil, err
	}
	if len(p.errors) > 0 {
		return nil, apperr.New(apperr.Validation, "This change can't be saved. %s", validationSummary(p.errors)).WithDetails(map[string]any{"errors": p.errors, "warnings": p.warnings})
	}
	base := ""
	if p.base != nil {
		base = p.base.ID
	}
	if a.Actor.Via != "dashboard" {
		if err := checkToken(a, token, p.planHash, base); err != nil {
			return nil, err
		}
	}
	return commitRevision(ctx, a, p)
}

func init() {
	Def("edit.check", "Dry-run a plan: schema + preflight validation, diff against the base, and a confirm token", Flags{}, func(ctx context.Context, a *app.App, in *EditCheckIn) (*EditCheckOut, error) {
		return checkPlan(ctx, a, in.Plan, in.AllowUnlock)
	})
	Def("edit.apply", "Apply a checked plan, creating a new immutable revision (optimistic concurrency on the branch head)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *EditApplyIn) (*EditApplyOut, error) {
		return applyPlan(ctx, a, in.Plan, in.ConfirmToken, in.AllowUnlock)
	})
	Def("edit.templates", "List the vetted motion-graphics templates and their parameter schemas", Flags{}, func(ctx context.Context, a *app.App, _ *EmptyIn) (*TemplatesOut, error) {
		out := &TemplatesOut{Templates: []TemplateRow{}}
		schemas := editspec.Schemas()
		for _, t := range editspec.Templates {
			out.Templates = append(out.Templates, TemplateRow{ID: t.ID, Version: t.Version, Summary: t.Summary, MinDuration: t.MinDuration, MaxDuration: t.MaxDuration, Region: t.Region,
				Params: schemas["template."+t.ID+".schema.json"]})
		}
		return out, nil
	})
	Def("edit.transitions", "List the transitions (picture and sound) with plain descriptions, directions, easings, durations and the per-project styles", Flags{}, func(ctx context.Context, a *app.App, _ *EmptyIn) (*editspec.TransitionsCatalog, error) {
		c := editspec.TransitionCatalog()
		return &c, nil
	})
	Def("revision.list", "List a project's revisions (newest first)", Flags{}, func(ctx context.Context, a *app.App, in *RevisionListIn) (*RevisionListOut, error) {
		rs, err := listRevisions(ctx, a, in.ProjectID, in.Branch, 200)
		if err != nil {
			return nil, err
		}
		return &RevisionListOut{Revisions: rs}, nil
	})
	Def("revision.show", "Show a revision including its full spec", Flags{}, func(ctx context.Context, a *app.App, in *RevisionShowIn) (*RevisionShowOut, error) {
		r, p, err := getRevision(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		head := p.Branches[r.Branch].Head
		var renders []models.Render
		_ = a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"revisionId": r.ID}, &renders, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
		if renders == nil {
			renders = []models.Render{}
		}
		s := summarizeRev(a, *r, head)
		s.Warnings = rewordWarnings(ctx, a, r)
		return &RevisionShowOut{Summary: s, Spec: r.Spec, Ops: r.Ops, Renders: renders}, nil
	})
	Def("revision.compare", "Compare two revisions (clips, overlays, audio, captions, duration)", Flags{}, func(ctx context.Context, a *app.App, in *RevisionCompareIn) (*RevisionCompareOut, error) {
		l, _, err := getRevision(ctx, a, in.Left)
		if err != nil {
			return nil, err
		}
		r, _, err := getRevision(ctx, a, in.Right)
		if err != nil {
			return nil, err
		}
		if l.ProjectID != r.ProjectID {
			return nil, invalid("The two versions belong to different projects.")
		}
		d := editspec.Compare(&l.Spec, &r.Spec)
		return &RevisionCompareOut{Left: summarizeRev(a, *l, ""), Right: summarizeRev(a, *r, ""), Diff: d}, nil
	})
	Def("revision.plan", "Resolve a revision into its frame-accurate render plan (proxy URLs for preview)", Flags{}, func(ctx context.Context, a *app.App, in *RevisionPlanIn) (*editspec.RenderPlan, error) {
		r, _, err := getRevision(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		return RevisionPlan(ctx, a, r)
	})
	Def("revision.approve", "Approve (or reject) a revision — people only", Flags{Mutating: true, HumanOnly: true}, func(ctx context.Context, a *app.App, in *RevisionApproveIn) (*RevisionSummary, error) {
		if r, _, err := getRevision(ctx, a, in.ID); err == nil && r.DraftID != "" {
			// An AI app's draft (D249): approving puts it in the cut;
			// rejecting with a note is Not quite, without one turns it down.
			if d := draftForApproval(ctx, a, r); d != nil && d.State != models.DraftApproved && d.State != models.DraftReplaced {
				return approveDraftVersion(ctx, a, d, in)
			}
		}
		state := models.ApprovalApproved
		if in.Reject {
			state = models.ApprovalRejected
		}
		now := time.Now().UTC()
		actor := a.Actor
		if err := a.DB.Update(ctx, store.CRevisions, a.WorkspaceID(), in.ID, bson.M{"$set": bson.M{"approval": models.Approval{State: state, By: &actor, At: &now, Note: in.Note}}}); err != nil {
			return nil, versionNotFound(in.ID)
		}
		r, p, err := getRevision(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		a.Audit(ctx, "revision."+state, "revision", r.ID, r.ProjectID, map[string]any{"note": in.Note})
		reconcileRevisionTodos(ctx, a, r, p, p.Branches[r.Branch].Head == r.ID)
		s := summarizeRev(a, *r, p.Branches[r.Branch].Head)
		if !in.Reject {
			s.Waiting = OpenSignoffs(ctx, a, p, r.Branch)
			s.Reminder = SignoffReminder(s.Waiting)
		} else {
			// The note reaches the conversation that made it (D186).
			s.ToldClaude = tellTurnedDown(ctx, a, TurnedDown{ProjectID: r.ProjectID, RevisionID: r.ID, RevisionNumber: r.Number, Origin: r.Origin, Note: in.Note})
		}
		return &s, nil
	})
}

// previewRenderPlan resolves a spec for the dashboard's player: proxy sources
// (the original for audio and footage without a proxy), served local-first.
func previewRenderPlan(spec *editspec.Spec, facts *dbFacts, revID, projectID, hash string) *editspec.RenderPlan {
	plan := editspec.Resolve(spec, facts, revID, projectID, hash)
	previewSources(plan, facts, spec) // proxies or originals, and each source's audio channels (D64)
	return plan
}

// EmptyIn is the input of ops that take no parameters.
type EmptyIn struct{}

// TemplatesOut lists templates.
type TemplatesOut struct {
	Templates []TemplateRow `json:"templates"`
}

// TemplateRow describes one template.
type TemplateRow struct {
	ID          string  `json:"id"`
	Version     string  `json:"version"`
	Summary     string  `json:"summary"`
	MinDuration float64 `json:"minDuration"`
	MaxDuration float64 `json:"maxDuration"`
	Region      string  `json:"region"`
	Params      any     `json:"params"`
}

// RevisionListIn selects revisions.
type RevisionListIn struct {
	ProjectID string `json:"projectId"`
	Branch    string `json:"branch,omitempty"`
}

// RevisionListOut lists revisions.
type RevisionListOut struct {
	Revisions []RevisionSummary `json:"revisions"`
}

// RevisionShowIn selects a revision.
type RevisionShowIn struct {
	ID string `json:"id"`
}

// RevisionShowOut is a revision with its spec.
type RevisionShowOut struct {
	Summary RevisionSummary `json:"summary"`
	Spec    editspec.Spec   `json:"spec"`
	Ops     []editspec.Op   `json:"ops,omitempty"`
	Renders []models.Render `json:"renders"`
}

// RevisionCompareIn selects two revisions.
type RevisionCompareIn struct {
	Left  string `json:"left"`
	Right string `json:"right"`
}

// RevisionCompareOut is a diff.
type RevisionCompareOut struct {
	Left  RevisionSummary `json:"left"`
	Right RevisionSummary `json:"right"`
	Diff  editspec.Diff   `json:"diff"`
}

// RevisionPlanIn selects a revision.
type RevisionPlanIn struct {
	ID string `json:"id"`
}

// RevisionApproveIn approves or rejects.
type RevisionApproveIn struct {
	ID     string `json:"id"`
	Reject bool   `json:"reject,omitempty"`
	Note   string `json:"note,omitempty" jsonschema:"maxLength=2000"`
}

func getRevision(ctx context.Context, a *app.App, id string) (*models.Revision, *models.Project, error) {
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), id, &r); err != nil {
		return nil, nil, versionNotFound(id)
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), r.ProjectID, &p); err != nil {
		return nil, nil, apperr.New(apperr.NotFound, "The project of that version isn't in this workspace any more.").WithDetails(map[string]any{"revisionId": id, "projectId": r.ProjectID})
	}
	return &r, &p, nil
}

// versionNotFound says a version (revision) isn't there; its ID stays in Details.
func versionNotFound(id string) error {
	return apperr.New(apperr.NotFound, "That version isn't in this workspace.").WithDetails(map[string]any{"revisionId": id}).
		WithHint("The project's history lists its versions.")
}

// commitRevision inserts the immutable revision, then moves the branch head with a
// compare-and-swap. If another writer moved the head in between, the revision stays
// as an unreferenced (detached) record and the caller gets E_REVISION_CONFLICT.
func commitRevision(ctx context.Context, a *app.App, p *prepared) (*EditApplyOut, error) {
	var proj models.Project
	err := a.DB.C(store.CProjects).FindOneAndUpdate(ctx, bson.M{"_id": p.project.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$inc": bson.M{"revisionSeq": 1}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&proj)
	if err != nil {
		return nil, err
	}
	_, total := editspec.Timeline(p.spec)
	// The database server's time (D360): the daemons' review copies follow a
	// version once it has been quiet a while by that clock, on whichever
	// computer looks.
	now, err := a.DB.Now(ctx)
	if err != nil {
		return nil, err
	}
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: p.project.ID, Branch: p.branch, Number: proj.RevisionSeq,
		ParentID: p.parentID, Spec: *p.spec, SpecHash: editspec.Hash(p.spec), Ops: p.plan.Ops, Instruction: p.plan.Instruction, Rationale: p.plan.Rationale, Origin: p.plan.Origin,
		Warnings: p.warnings, Duration: float64(total) / float64(p.spec.Output.FPS), Approval: models.Approval{State: models.ApprovalDraft}, CreatedBy: a.Actor, CreatedAt: now,
		Includes: p.includes}
	if p.rebased && p.base != nil {
		rev.RebasedFrom = p.base.ID
	}
	// The words the version is made with, kept so it renders them forever (D146).
	if rev.Transcripts, err = pinTranscripts(ctx, a, p.facts); err != nil {
		return nil, err
	}
	// A few words saying what this version changed (D150): the change's own
	// note, else what was asked, else the changes.
	rev.Note, rev.NoteBy = versionNote(&p.plan, p.diff, a.Actor.Via)
	if err := a.DB.Insert(ctx, store.CRevisions, rev); err != nil {
		return nil, err
	}
	field := "branches." + p.branch
	filter := bson.M{"_id": p.project.ID, "workspaceId": a.WorkspaceID()}
	if expected := p.project.Branches[p.branch].Head; expected != "" {
		filter[field+".head"] = expected
	} else {
		filter[field] = bson.M{"$exists": false}
	}
	// Only the head moves: the cut keeps its name (D150).
	res, err := a.DB.C(store.CProjects).UpdateOne(ctx, filter, bson.M{"$set": bson.M{field + ".head": rev.ID, field + ".updatedAt": now, field + ".updatedBy": a.Actor, "updatedAt": now}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		_, _ = a.DB.C(store.CRevisions).UpdateByID(ctx, rev.ID, bson.M{"$set": bson.M{"detached": true}})
		var cur models.Project
		_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), p.project.ID, &cur)
		return nil, apperr.New(apperr.RevisionConflict, "Someone saved another change to %s at the same moment, so this one wasn't saved; nothing was overwritten.", branchWords(p.project.Name, p.branch)).
			WithDetails(map[string]any{"headRevisionId": cur.Branches[p.branch].Head, "detachedRevisionId": rev.ID}).
			WithHint("Check the change again against the latest version, then save it.")
	}
	details := map[string]any{"number": rev.Number, "parent": rev.ParentID, "rebased": p.rebased, "specHash": rev.SpecHash, "instruction": truncate(rev.Instruction, 300)}
	if rev.Origin != "" {
		details["origin"] = rev.Origin
	}
	a.Audit(ctx, "revision.create", "revision", rev.ID, rev.ProjectID, details)
	closeIncludedDrafts(ctx, a, &rev, "saved on top of it") // drafts this version carries are in the cut now (D252)
	reconcileRevisionTodos(ctx, a, &rev, &proj, true)       // music rights, approval
	_ = faces.MarkCuts(ctx, a, rev.ProjectID)               // questions about faces: the people in the cut first (D157)
	queueResourceUploads(ctx, a, &rev, p.facts)             // linked folders' large files, the first time a cut uses them
	return &EditApplyOut{Revision: summarizeRev(a, rev, rev.ID), Diff: *p.diff, Warnings: p.warnings, Rebased: p.rebased,
		URL: dashboardURL(a, fmt.Sprintf("/p/%s/r/%s", rev.ProjectID, rev.ID))}, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var _ = mongo.ErrNoDocuments
