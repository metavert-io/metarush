package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// ActionSync names tracked sync work (the activity bar's "Lining up recordings").
const ActionSync = "conversation-sync"

func init() {
	voiceover.ActionTitles[ActionSync] = "Lining up recordings"
	ops.Def("conversation.create", "Make a conversation from several recordings of one talk (one per person, or per-person sound and a shared gallery view): lines them up by their sound on one clock, with clock drift, and says how sure it is", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallCreateIn) (*CallOut, error) { return create(ctx, a, in) })
	ops.Def("conversation.list", "List the conversations (in a project, or all of them) with their people, length and how sure their sync is", ops.Flags{},
		func(ctx context.Context, a *app.App, in *CallListIn) (*CallListOut, error) { return list(ctx, a, in) })
	ops.Def("conversation.show", "Show a conversation: its recordings, where each starts on its clock, how each was lined up and how sure, clean-up suggestions and the cuts that play it", ops.Flags{},
		func(ctx context.Context, a *app.App, in *CallRefIn) (*CallOut, error) {
			c, err := load(ctx, a, in.Conversation, in.ProjectID)
			if err != nil {
				return nil, err
			}
			v := view(ctx, a, c)
			return &CallOut{Conversation: v, Summary: v.Text}, nil
		})
	ops.Def("conversation.sync", "Line a conversation's recordings up again: by their sound (default), as files that start together, on a clap in the first minute, or on moments a person marked in each", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallSyncIn) (*CallOut, error) { return resync(ctx, a, in) })
	ops.Def("conversation.nudge", "Move one recording of a conversation earlier or later on its clock, by seconds or frames (when lips and voice don't quite match)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallNudgeIn) (*CallOut, error) { return nudge(ctx, a, in) })
	ops.Def("conversation.update", "Rename a conversation, or change a member: their name, name tag, what their recording gives (picture and sound, sound only, picture only) and who they are in People", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallUpdateIn) (*CallOut, error) { return update(ctx, a, in) })
	ops.Def("conversation.pin", "Use a conversation's current alignment and member settings in a project's cut (checks the change; apply makes it a new version)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallPinIn) (*CallPlanOut, error) { return pinIn(ctx, a, in) })
}

// CallMemberIn is one recording to put in a conversation.
type CallMemberIn struct {
	AssetID string `json:"assetId" jsonschema:"minLength=1,description=The recording: its ID\\, title or file name."`
	Name    string `json:"name,omitempty" jsonschema:"maxLength=80,description=The person's name (default: the voice named in it\\, else the file's title)."`
	Use     string `json:"use,omitempty" jsonschema:"enum=both,enum=sound,enum=picture,description=both (picture and sound of one person\\, the default)\\, sound (one person's audio)\\, picture (a shared gallery or speaker view: its sound is the call's mix\\, used to line the others up but not mixed)."`
	ID      string `json:"id,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]{0\\,47}$,description=A short name for shots (default from the name)."`
}

// CallCreateIn makes a conversation.
type CallCreateIn struct {
	ProjectID string         `json:"projectId" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	Name      string         `json:"name,omitempty" jsonschema:"maxLength=120,description=Default: the people's names."`
	Members   []CallMemberIn `json:"members" jsonschema:"minItems=1,maxItems=16"`
	// Align: audio (by their sound, the default), together (the files start
	// together), none (not now).
	Align  string  `json:"align,omitempty" jsonschema:"enum=audio,enum=together,enum=none"`
	Search float64 `json:"search,omitempty" jsonschema:"minimum=0,maximum=7200,description=How far apart the recordings may start (seconds either way; default 600)."`
	// Source is filled by imports.
	Source *models.ConversationSource `json:"source,omitempty"`
}

// CallOut is a conversation after a change (or as it is).
type CallOut struct {
	Conversation CallInfo `json:"conversation"`
	Summary      string   `json:"summary"`
	JobID        string   `json:"jobId,omitempty"`
}

// SetJobID implements voiceover.Trackable.
func (o *CallOut) SetJobID(id string) { o.JobID = id }

// JobResult implements voiceover.Trackable.
func (o *CallOut) JobResult() map[string]any {
	return map[string]any{"conversationId": o.Conversation.ID, "summary": o.Summary}
}

// CallRefIn names a conversation.
type CallRefIn struct {
	Conversation string `json:"conversation" jsonschema:"minLength=1,description=The conversation: its ID or name."`
	ProjectID    string `json:"projectId,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$,description=Where to look for it by name."`
}

// CallListIn lists conversations.
type CallListIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
}

// CallListOut lists conversations.
type CallListOut struct {
	Conversations []CallInfo `json:"conversations"`
}

// CallSyncIn lines a conversation up again.
type CallSyncIn struct {
	CallRefIn
	Method string `json:"method,omitempty" jsonschema:"enum=audio,enum=together,enum=clap,enum=mark"`
	// Marks are the same moment in two or more recordings (member → seconds into its recording).
	Marks  map[string]float64 `json:"marks,omitempty"`
	Search float64            `json:"search,omitempty" jsonschema:"minimum=0,maximum=7200"`
}

// CallNudgeIn moves one member on the clock.
type CallNudgeIn struct {
	CallRefIn
	Member string `json:"member" jsonschema:"minLength=1,description=The member (short name or name)."`
	// By is seconds (positive: later on the conversation's clock); Frames
	// counts frames at the project's frame rate instead.
	By     float64 `json:"by,omitempty" jsonschema:"minimum=-60,maximum=60"`
	Frames int     `json:"frames,omitempty" jsonschema:"minimum=-300,maximum=300"`
	// To places it exactly (seconds on the clock).
	To *float64 `json:"to,omitempty" jsonschema:"minimum=-36000,maximum=36000"`
}

// CallUpdateIn changes a conversation or one member.
type CallUpdateIn struct {
	CallRefIn
	Name   string `json:"name,omitempty" jsonschema:"maxLength=120"`
	Member string `json:"member,omitempty"`
	// Member fields (with member).
	MemberName string  `json:"memberName,omitempty" jsonschema:"maxLength=80"`
	Tag        *string `json:"tag,omitempty" jsonschema:"maxLength=80"`
	Use        string  `json:"use,omitempty" jsonschema:"enum=both,enum=sound,enum=picture"`
	Person     string  `json:"person,omitempty" jsonschema:"maxLength=120,description=Who they are in People: a person's ID or name (- unlinks)."`
	Muted      *bool   `json:"muted,omitempty"`
	Style      string  `json:"style,omitempty"`
	Layout     string  `json:"layout,omitempty" jsonschema:"enum=side-by-side,enum=stacked,enum=pip,enum=grid"`
}

func create(ctx context.Context, a *app.App, in *CallCreateIn) (*CallOut, error) {
	if !ids.Valid(ids.Project, in.ProjectID) {
		return nil, invalid("say which project the conversation is in (projectId)")
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "There's no project %s in this workspace.", in.ProjectID)
	}
	now := time.Now().UTC()
	c := &models.Conversation{ID: ids.New(ids.Conversation), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.ID}, CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now, Source: in.Source}
	if err := resolveMembers(ctx, a, c, p.ID, in.Members); err != nil {
		return nil, err
	}
	c.Name = strings.TrimSpace(in.Name)
	if c.Name == "" {
		var people []string
		for _, m := range c.Members {
			if m.UseOf() != editspec.UsePicture {
				people = append(people, m.Name)
			}
		}
		c.Name = "Conversation with " + joinAnd(people)
		if len(people) == 0 {
			c.Name = "Conversation"
		}
	}
	run := func(ctx context.Context) (*CallOut, error) {
		switch in.Align {
		case "", MethodAudio:
			if err := syncRun(ctx, a, c, MethodAudio, nil, in.Search, true); err != nil {
				return nil, err
			}
		case MethodTogether:
			if err := syncRun(ctx, a, c, MethodTogether, nil, 0, true); err != nil {
				return nil, err
			}
		case "none":
			as, _ := assets(ctx, a, c)
			c.Length = length(c, as)
		default:
			return nil, invalid("align is audio, together or none, not %q", in.Align)
		}
		if err := a.DB.Insert(ctx, store.CConversations, c); err != nil {
			return nil, err
		}
		a.Audit(ctx, "conversation.create", "conversation", c.ID, p.ID, map[string]any{"members": len(c.Members)})
		v := view(ctx, a, c)
		return &CallOut{Conversation: v, Summary: "Made " + v.Text}, nil
	}
	if in.Align == "none" {
		return run(ctx)
	}
	return voiceover.TrackWork(ctx, a, ActionSync, "", p.ID, run)
}

func list(ctx context.Context, a *app.App, in *CallListIn) (*CallListOut, error) {
	filter := bson.M{}
	if in.ProjectID != "" {
		filter["projectIds"] = in.ProjectID
	}
	var all []models.Conversation
	if err := a.DB.Find(ctx, store.CConversations, a.WorkspaceID(), filter, &all, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(500)); err != nil {
		return nil, err
	}
	out := &CallListOut{Conversations: []CallInfo{}}
	for i := range all {
		out.Conversations = append(out.Conversations, view(ctx, a, &all[i]))
	}
	return out, nil
}

func resync(ctx context.Context, a *app.App, in *CallSyncIn) (*CallOut, error) {
	c, err := load(ctx, a, in.Conversation, in.ProjectID)
	if err != nil {
		return nil, err
	}
	run := func(ctx context.Context) (*CallOut, error) {
		if err := syncRun(ctx, a, c, in.Method, in.Marks, in.Search, false); err != nil {
			return nil, err
		}
		if err := save(ctx, a, c); err != nil {
			return nil, err
		}
		a.Audit(ctx, "conversation.sync", "conversation", c.ID, first(c.ProjectIDs), map[string]any{"method": c.Sync.Method})
		v := view(ctx, a, c)
		return &CallOut{Conversation: v, Summary: "Lined up again: " + v.Text + useHint(v)}, nil
	}
	if in.Method == MethodMark || in.Method == MethodTogether {
		return run(ctx)
	}
	return voiceover.TrackWork(ctx, a, ActionSync, "", first(c.ProjectIDs), run)
}

// useHint says when the project's cut still plays an older alignment.
func useHint(v CallInfo) string {
	for _, cu := range v.Cuts {
		if !cu.Current {
			return " The cut still plays the earlier alignment: use the new one in the cut when you're ready."
		}
	}
	return ""
}

func first(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[0]
}

// findMember finds a member by short name or name.
func findMember(c *models.Conversation, ref string) (*models.ConversationMember, error) {
	ref = strings.TrimSpace(ref)
	if m, _ := c.Member(ref); m != nil {
		return m, nil
	}
	for i := range c.Members {
		if strings.EqualFold(c.Members[i].Name, ref) {
			return &c.Members[i], nil
		}
	}
	return nil, invalid("there's no member %q in %s (the members are %s)", ref, c.Name, memberList(c))
}

func nudge(ctx context.Context, a *app.App, in *CallNudgeIn) (*CallOut, error) {
	c, err := load(ctx, a, in.Conversation, in.ProjectID)
	if err != nil {
		return nil, err
	}
	m, err := findMember(c, in.Member)
	if err != nil {
		return nil, err
	}
	by := in.By
	if in.Frames != 0 {
		fps := projectFPS(ctx, a, first(c.ProjectIDs))
		by = float64(in.Frames) / float64(fps)
	}
	if in.To != nil {
		by = *in.To - m.Start
	}
	if by == 0 {
		return nil, invalid("say how far to move it: by seconds (by), frames or to a moment (to)")
	}
	m.Start = round3(m.Start + by)
	if m.Align == nil {
		m.Align = &models.MemberAlign{Method: "manual", Confidence: 1}
	}
	m.Align.Nudged = round3(m.Align.Nudged + by)
	m.Align.At = time.Now().UTC()
	as, _ := assets(ctx, a, c)
	c.Length = length(c, as)
	if err := save(ctx, a, c); err != nil {
		return nil, err
	}
	a.Audit(ctx, "conversation.nudge", "conversation", c.ID, first(c.ProjectIDs), map[string]any{"member": m.ID, "by": by})
	v := view(ctx, a, c)
	way := "later"
	if by < 0 {
		way = "earlier"
	}
	return &CallOut{Conversation: v, Summary: fmt.Sprintf("Moved %s recording %s %s.%s", possessive(m.Name), secs(math.Abs(by)), way, useHint(v))}, nil
}

func update(ctx context.Context, a *app.App, in *CallUpdateIn) (*CallOut, error) {
	c, err := load(ctx, a, in.Conversation, in.ProjectID)
	if err != nil {
		return nil, err
	}
	var changed []string
	if n := strings.TrimSpace(in.Name); n != "" {
		c.Name = n
		changed = append(changed, "named it "+n)
	}
	if in.Style != "" {
		if _, ok := Styles[in.Style]; !ok {
			return nil, invalid("style is %s, not %q", strings.Join(styleNames(), ", "), in.Style)
		}
		c.Style = in.Style
		changed = append(changed, "style "+in.Style)
	}
	if in.Layout != "" {
		c.Layout = in.Layout
		changed = append(changed, "layout "+editspec.LayoutLabel(in.Layout))
	}
	if in.Member != "" {
		m, err := findMember(c, in.Member)
		if err != nil {
			return nil, err
		}
		if n := strings.TrimSpace(in.MemberName); n != "" {
			m.Name = n
			changed = append(changed, "renamed "+m.ID+" to "+n)
		}
		if in.Tag != nil {
			m.Tag = strings.TrimSpace(*in.Tag)
			changed = append(changed, possessive(m.Name)+" name tag")
		}
		switch in.Use {
		case "":
		case editspec.UseBoth:
			m.Use = ""
			changed = append(changed, possessive(m.Name)+" picture and sound")
		default:
			m.Use = in.Use
			changed = append(changed, possessive(m.Name)+" "+in.Use+" only")
		}
		if in.Muted != nil {
			m.Muted = *in.Muted
			changed = append(changed, possessive(m.Name)+" sound "+map[bool]string{true: "off", false: "on"}[*in.Muted])
		}
		switch p := strings.TrimSpace(in.Person); {
		case p == "":
		case p == "-":
			m.PersonID = ""
			changed = append(changed, "unlinked "+m.Name+" from People")
		default:
			var pr models.Person
			if ids.Valid(ids.Person, p) {
				if err := a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), p, &pr); err != nil {
					return nil, apperr.New(apperr.NotFound, "There's no person %s.", p)
				}
			} else if found := person(ctx, a, p); found != nil {
				pr = *found
			} else {
				return nil, apperr.New(apperr.NotFound, "There's nobody called %q in People.", p).WithHint("People lists everyone you have named.").WithCommand("metarush people list")
			}
			m.PersonID = pr.ID
			if m.Tag == "" {
				m.Tag = tagFrom(pr.Notes)
			}
			changed = append(changed, m.Name+" is "+pr.Name+" in People")
		}
	}
	if len(changed) == 0 {
		return nil, invalid("nothing to change")
	}
	if err := save(ctx, a, c); err != nil {
		return nil, err
	}
	a.Audit(ctx, "conversation.update", "conversation", c.ID, first(c.ProjectIDs), map[string]any{"changes": changed})
	v := view(ctx, a, c)
	return &CallOut{Conversation: v, Summary: "Changed " + strings.Join(changed, ", ") + "."}, nil
}

// ---- plans against a project's cut ----

// CallPinIn uses a conversation's current alignment in a cut.
type CallPinIn struct {
	CallRefIn
	Project string `json:"project,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$,description=The project whose latest cut gets it (default: the conversation's project)."`
	Branch  string `json:"branch,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]*$"`
	Apply   bool   `json:"apply,omitempty" jsonschema:"description=Put it in the cut as a new version."`
}

// CallPlanOut is a change to a cut in plain words with its checked plan.
type CallPlanOut struct {
	Changes []string              `json:"changes"`
	Result  *voiceover.PlanResult `json:"result,omitempty"`
	Applied *voiceover.Applied    `json:"applied,omitempty"`
	Summary string                `json:"summary"`
	// Why are the reasons for an automatic cut's choices (one per shot).
	Why []string `json:"why,omitempty"`
}

func pinIn(ctx context.Context, a *app.App, in *CallPinIn) (*CallPlanOut, error) {
	c, err := load(ctx, a, in.Conversation, in.ProjectID)
	if err != nil {
		return nil, err
	}
	pid := in.Project
	if pid == "" {
		pid = first(c.ProjectIDs)
	}
	rev, err := latest(ctx, a, pid, in.Branch)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		return nil, invalid("the project has no cut yet: make a cut of the conversation first")
	}
	var eops []editspec.Op
	var changes []string
	for _, name := range sortedNames(rev.Spec.Conversations) {
		old := rev.Spec.Conversations[name]
		if old.ID != c.ID {
			continue
		}
		pin := mergePin(old, c)
		if string(mustJSON(pin)) == string(mustJSON(old)) {
			continue
		}
		p := pin
		eops = append(eops, editspec.Op{Op: "setConversation", ID: name, Conversation: &p})
		change := "the cut plays " + c.Name + " with its current alignment"
		if !old.SampleAligned {
			change += ", each recording placed exactly where it starts (to the sample, no longer on the next frame)"
		}
		changes = append(changes, change)
	}
	out := &CallPlanOut{Changes: changes}
	if len(eops) == 0 {
		out.Summary = "The cut already plays " + c.Name + " as it is now."
		if len(rev.Spec.Conversations) == 0 {
			out.Summary = "The cut doesn't play " + c.Name + " yet: make a cut of it first."
		}
		return out, nil
	}
	if out.Result, err = planCheck(ctx, a, rev, "Use the current alignment of "+c.Name, eops); err != nil {
		return nil, err
	}
	out.Summary = "Ready: " + strings.Join(changes, "; ") + "."
	if in.Apply {
		out.Applied = applyResult(ctx, a, out.Result)
		out.Summary = settle(out.Summary, out.Applied)
	}
	return out, nil
}

// mergePin is a cut's pin with the conversation's current alignment and
// member settings; what the cut set for itself (framing, crop, look, gate,
// name tags) is kept for members it already had. Using the current alignment
// also places every recording to the sample (D119): a cut made before that
// existed starts doing so when a person chooses its alignment again.
func mergePin(old editspec.ConversationPin, c *models.Conversation) editspec.ConversationPin {
	pin := old
	pin.SampleAligned = true
	byAsset := map[string]editspec.ConversationMember{}
	for _, m := range old.Members {
		byAsset[m.AssetID] = m
	}
	pin.Members = nil
	for _, m := range c.Members {
		nm := m.ConversationMember
		if om, ok := byAsset[m.AssetID]; ok {
			nm.ID = om.ID // shots name members by the cut's short names
			if om.Framing != nil {
				nm.Framing = om.Framing
			}
			if om.Crop != nil {
				nm.Crop = om.Crop
			}
			if om.Look != nil {
				nm.Look = om.Look
			}
		}
		pin.Members = append(pin.Members, nm)
	}
	return pin
}

func sortedNames(m map[string]editspec.ConversationPin) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// planCheck builds an ops plan against the revision and dry-runs it with
// edit.check (diff, warnings, confirm token). Nothing changes until edit.apply.
func planCheck(ctx context.Context, a *app.App, rev *models.Revision, instruction string, eops []editspec.Op) (*voiceover.PlanResult, error) {
	if len(eops) == 0 {
		return nil, nil
	}
	if r := []rune(instruction); len(r) > 4000 {
		instruction = string(r[:3999]) + "…"
	}
	plan := editspec.Plan{BaseRevisionID: rev.ID, Branch: rev.Branch, Instruction: instruction, Ops: eops}
	return checkPlan(ctx, a, plan)
}

func checkPlan(ctx context.Context, a *app.App, plan editspec.Plan) (*voiceover.PlanResult, error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	out, err := ops.CallTyped[ops.EditCheckOut](ctx, a, "edit.check", ops.EditCheckIn{Plan: raw}, ops.Caller{})
	if err != nil {
		return nil, err
	}
	return &voiceover.PlanResult{Plan: plan, Check: out}, nil
}

// applyResult saves a checked plan as a new version (with its own token).
func applyResult(ctx context.Context, a *app.App, res *voiceover.PlanResult) *voiceover.Applied {
	if res == nil {
		return nil
	}
	if res.Check == nil || !res.Check.Valid {
		msg := "the change did not pass its check"
		if res.Check != nil && len(res.Check.Errors) > 0 {
			msg = res.Check.Errors[0].Message
		}
		return &voiceover.Applied{Problem: msg}
	}
	raw, err := json.Marshal(res.Plan)
	if err != nil {
		return &voiceover.Applied{Problem: err.Error()}
	}
	out, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: res.Check.ConfirmToken}, ops.Caller{})
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return &voiceover.Applied{Problem: ae.Message}
		}
		return &voiceover.Applied{Problem: err.Error()}
	}
	return &voiceover.Applied{RevisionID: out.Revision.ID, ProjectID: out.Revision.ProjectID, Number: out.Revision.Number}
}

// settle says where an applied change went.
func settle(summary string, ap *voiceover.Applied) string {
	switch {
	case ap == nil:
		return summary
	case ap.Problem != "":
		return summary + " It is not in the cut yet: " + strings.TrimRight(ap.Problem, ".") + "."
	}
	return strings.TrimSuffix(strings.TrimPrefix(summary, "Ready: "), " Apply the change to put it in the cut.") + fmt.Sprintf(" It is in the cut now as version %d.", ap.Number)
}
