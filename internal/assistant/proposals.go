package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
)

var proposalName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,80}\.((plan|render)\.json|cut\.md)$`)

const (
	maxProposalBytes = 2 << 20
	maxProposals     = 12 // per kind and turn
)

// listProposalFiles returns plan, render-request and cut sheet files in dir.
// Only regular files with a safe name are considered (no symlinks, no
// subfolders).
func listProposalFiles(dir string) (plans, renders, sheets, problems []string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, nil, nil
	}
	for _, e := range entries {
		name := e.Name()
		proposal := strings.HasSuffix(name, ".plan.json") || strings.HasSuffix(name, ".render.json") || strings.HasSuffix(name, ".cut.md")
		switch {
		case strings.HasSuffix(name, ".tmp") || strings.HasPrefix(name, "."), strings.HasSuffix(name, DescribeSuffix):
			continue // scene descriptions are read by `footage describe`, not shown as proposals
		case !proposal:
			// Everything else Claude writes here has a card or a use of its own:
			// questions, motion graphics (a folder), diagrams, scripts.
			continue
		case !e.Type().IsRegular():
			problems = append(problems, "A change Claude wrote was skipped: MetaRush only reads files Claude wrote itself.")
			continue
		case !proposalName.MatchString(name):
			problems = append(problems, "A change Claude wrote was skipped: its name isn't one MetaRush reads.")
			continue
		}
		switch {
		case strings.HasSuffix(name, ".plan.json"):
			plans = append(plans, name)
		case strings.HasSuffix(name, ".cut.md"):
			sheets = append(sheets, name)
		default:
			renders = append(renders, name)
		}
	}
	sort.Strings(plans)
	sort.Strings(renders)
	sort.Strings(sheets)
	if len(sheets) > maxProposals {
		problems = append(problems, fmt.Sprintf("Only the first %d cut sheets are shown.", maxProposals))
		sheets = sheets[:maxProposals]
	}
	if len(plans) > maxProposals {
		problems = append(problems, fmt.Sprintf("Only the first %d changes are shown.", maxProposals))
		plans = plans[:maxProposals]
	}
	if len(renders) > maxProposals {
		problems = append(problems, fmt.Sprintf("Only the first %d render requests are shown.", maxProposals))
		renders = renders[:maxProposals]
	}
	return plans, renders, sheets, problems
}

func countProposals(dir string) int {
	p, r, s, _ := listProposalFiles(dir)
	return len(p) + len(r) + len(s)
}

// readProposal reads a proposal without following symlinks and with a size cap.
func readProposal(dir, name string) ([]byte, error) {
	p := filepath.Join(dir, name)
	st, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	if st.Size() > maxProposalBytes {
		return nil, fmt.Errorf("larger than %d MB", maxProposalBytes>>20)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxProposalBytes+1))
}

// Origin is the label recorded on a revision created from a proposal: the
// conversation and the proposal's ref (t<turn>/<file>; just <file> for the
// single requests of earlier versions).
func Origin(conversationID, ref string) string { return "assistant:" + conversationID + "/" + ref }

// Proposals lists every turn's plans (each re-checked with edit.check against
// the current head, or shown as applied) and render requests. remote callers
// (the dashboard) never get confirm tokens; the CLI does, for `assistant apply`.
func (m *Manager) Proposals(ctx context.Context, id string, remote bool) (*ConversationProposals, error) {
	d, err := m.Get(ctx, id, false, false)
	if err != nil {
		return nil, err
	}
	out := &ConversationProposals{ConversationID: id, Here: d.Here, Machine: d.InstallationName, Turns: []TurnProposals{}, CheckedAt: time.Now().UTC()}
	if !d.Here {
		return out, nil
	}
	for _, t := range d.Turns {
		if t.Folder == "" || t.Proposals == 0 && t.State != TurnRunning {
			continue
		}
		dir := filepath.Join(m.dir(), filepath.FromSlash(t.Folder))
		plans, renders, _, problems := listProposalFiles(dir) // cut sheets are action cards
		if len(plans)+len(renders)+len(problems) == 0 {
			continue
		}
		tp := TurnProposals{Turn: t.N, Plans: []PlanProposal{}, Renders: []RenderProposal{}, Problems: problems}
		byFile := map[string]*PlanProposal{}
		for _, f := range plans {
			tp.Plans = append(tp.Plans, m.checkPlan(ctx, id, t.RefPrefix, dir, f, remote))
		}
		for i := range tp.Plans {
			byFile[tp.Plans[i].File] = &tp.Plans[i]
		}
		for _, f := range renders {
			tp.Renders = append(tp.Renders, m.checkRender(ctx, t.RefPrefix, dir, f, byFile))
		}
		out.Turns = append(out.Turns, tp)
	}
	return out, nil
}

func (m *Manager) checkPlan(ctx context.Context, convID, refPrefix, dir, file string, remote bool) PlanProposal {
	ref := refPrefix + file
	pp := PlanProposal{File: file, Ref: ref, Origin: Origin(convID, ref), Kind: "invalid"}
	raw, err := readProposal(dir, file)
	if err != nil {
		pp.Error = apperr.New(apperr.InvalidInput, "MetaRush can't read the change %s.", quoted(planName(file))).
			WithHint("Ask Claude to write it again.").WithDetails(map[string]any{"file": file, "error": err.Error()})
		return pp
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		pp.Error = apperr.New(apperr.InvalidInput, "The change %s isn't written the way MetaRush expects.", quoted(planName(file))).
			WithHint("Ask Claude to write it again.").WithDetails(map[string]any{"file": file})
		return pp
	}
	// The server, not the model, labels where the plan came from.
	obj["origin"], _ = json.Marshal(pp.Origin)
	plan, _ := json.Marshal(obj)
	pp.Plan = plan
	var peek struct {
		Instruction string            `json:"instruction"`
		Rationale   string            `json:"rationale"`
		Spec        json.RawMessage   `json:"spec"`
		Ops         []json.RawMessage `json:"ops"`
	}
	_ = json.Unmarshal(plan, &peek)
	pp.Instruction, pp.Rationale, pp.OpCount = clip(peek.Instruction, 4000), clip(peek.Rationale, 8000), len(peek.Ops)
	switch {
	case len(peek.Ops) > 0:
		pp.Kind = "ops"
	case len(bytes.TrimSpace(peek.Spec)) > 0 && string(bytes.TrimSpace(peek.Spec)) != "null":
		pp.Kind = "spec"
	}
	a := m.App
	var rev models.Revision
	if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "origin": pp.Origin}).Decode(&rev); err == nil {
		pp.Applied = &AppliedProposal{RevisionID: rev.ID, Number: rev.Number, ProjectID: rev.ProjectID, By: rev.CreatedBy.UserName, At: rev.CreatedAt}
		return pp
	}
	in, _ := json.Marshal(ops.EditCheckIn{Plan: plan})
	res, err := ops.Call(ctx, a, "edit.check", in, ops.Caller{})
	if err != nil {
		pp.Error = apperr.From(err)
		return pp
	}
	co := *res.(*ops.EditCheckOut)
	if remote {
		// The dashboard applies through the human channel; the CLI token and its
		// instructions do not apply there.
		co.ConfirmToken, co.ExpiresAt, co.Next = "", nil, ""
	} else {
		co.Next = "metarush assistant apply " + convID + " " + ref + " --confirm " + co.ConfirmToken
	}
	pp.Check = &co
	return pp
}

func (m *Manager) checkRender(ctx context.Context, refPrefix, dir, file string, plans map[string]*PlanProposal) RenderProposal {
	rp := RenderProposal{File: file, Ref: refPrefix + file, Quality: "review"}
	raw, err := readProposal(dir, file)
	if err != nil {
		rp.Error = "MetaRush can't read this render request. Ask Claude to write it again."
		return rp
	}
	var rr RenderRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rr); err != nil {
		rp.Error = "This render request isn't written the way MetaRush expects. Ask Claude to write it again."
		return rp
	}
	rp.Plan, rp.Preset, rp.Reason = rr.Plan, rr.Preset, clip(rr.Reason, 500)
	if rr.Quality != "" {
		rp.Quality = rr.Quality
	}
	switch {
	case rp.Quality != "review" && rp.Quality != "final":
		rp.Error = "This render request asks for a quality MetaRush doesn't have (it is review or final). Ask Claude to write it again."
		return rp
	case rr.Preset != "" && !knownPreset(rr.Preset):
		rp.Error = fmt.Sprintf("This render request asks for a size MetaRush doesn't have (%s). Ask Claude to write it again.", quoted(rr.Preset))
		return rp
	case (rr.RevisionID == "") == (rr.Plan == ""):
		rp.Error = "This render request should name one version of the cut or one change. Ask Claude to write it again."
		return rp
	}
	revID := rr.RevisionID
	if rr.Plan != "" {
		pp, ok := plans[rr.Plan]
		switch {
		case !ok:
			rp.Error = "The change " + quoted(planName(rr.Plan)) + " this render request needs isn't in this message."
			return rp
		case pp.Applied == nil:
			rp.Waiting = "Apply the change " + quoted(planName(rr.Plan)) + " first"
			return rp
		}
		revID = pp.Applied.RevisionID
	}
	if !ids.Valid(ids.Revision, revID) {
		rp.Error = "This render request names a version of the cut MetaRush can't find. Ask Claude to write it again."
		return rp
	}
	a := m.App
	var rev models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), revID, &rev); err != nil {
		rp.Error = "MetaRush can't find the version of the cut this render request names."
		return rp
	}
	rp.RevisionID, rp.RevisionNumber, rp.ProjectID, rp.Ready = rev.ID, rev.Number, rev.ProjectID, true
	preset := rr.Preset
	if preset == "" {
		preset = rev.Spec.Output.Preset
	}
	var existing []models.Render
	_ = a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"revisionId": rev.ID, "quality": rp.Quality, "preset": preset,
		"state": bson.M{"$nin": bson.A{models.RenderFailed, models.RenderCanceled}}}, &existing,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(1))
	if len(existing) > 0 {
		rp.RenderID, rp.RenderState = existing[0].ID, existing[0].State
	}
	return rp
}

func knownPreset(p string) bool { _, ok := editspec.Presets[p]; return ok }

// Apply applies a conversation's plan proposal through edit.apply on the
// caller's channel (from the CLI that needs the confirm token `assistant
// proposals` printed — the same gate as `edit apply`).
func (m *Manager) Apply(ctx context.Context, a *app.App, caller ops.Caller, in ConversationApplyIn) (*ops.EditApplyOut, error) {
	props, err := m.Proposals(ctx, in.ID, true)
	if err != nil {
		return nil, err
	}
	if !props.Here {
		return nil, apperr.New(apperr.Conflict, "The changes Claude wrote in this conversation are on %s.", props.Machine).
			WithHint("Apply it there, from the dashboard or a terminal on that machine.").WithDetails(map[string]any{"reason": "other_machine"})
	}
	ref := strings.TrimSpace(in.Proposal)
	var found *PlanProposal
	for ti := len(props.Turns) - 1; ti >= 0 && found == nil; ti-- {
		for i := range props.Turns[ti].Plans {
			p := &props.Turns[ti].Plans[i]
			if p.Ref == ref || (!strings.Contains(ref, "/") && p.File == ref) {
				found = p
				break
			}
		}
	}
	switch {
	case found == nil:
		return nil, apperr.New(apperr.NotFound, "There's no change %s in this conversation.", quoted(planName(ref))).
			WithHint("The Claude Code panel shows the changes Claude wrote.").WithCommand("metarush assistant proposals " + in.ID)
	case found.Applied != nil:
		return nil, apperr.New(apperr.Conflict, "The change %s was already applied as version %d.", quoted(planName(found.File)), found.Applied.Number)
	case found.Plan == nil:
		return nil, apperr.New(apperr.InvalidInput, "MetaRush can't read the change %s.", quoted(planName(found.File)))
	}
	return ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: found.Plan, ConfirmToken: in.ConfirmToken, AllowUnlock: in.AllowUnlock}, caller)
}
