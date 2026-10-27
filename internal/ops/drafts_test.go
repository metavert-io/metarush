package ops_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Drafts from AI apps and the review desk (D248–D250).

// agentApp is an AI app connected so that it may only propose.
func agentApp(a *app.App, agent, via string) *app.App {
	g := *a
	g.Actor.Via, g.Actor.Agent, g.ProposeOnly = via, agent, true
	return &g
}

// asPerson is someone at the dashboard.
func asPerson(a *app.App) *app.App {
	p := *a
	p.Actor.Via = "dashboard"
	return &p
}

func clipOutPlan(t *testing.T, base, clipID string, out float64) json.RawMessage {
	t.Helper()
	set, _ := json.Marshal(map[string]any{"out": out})
	plan, _ := json.Marshal(editspec.Plan{BaseRevisionID: base, Instruction: "Shorter " + clipID, Ops: []editspec.Op{{Op: "updateClip", ID: clipID, Set: set}}})
	return plan
}

func draftInput(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func headOf(t *testing.T, a *app.App, projectID string) string {
	t.Helper()
	var p models.Project
	if err := a.DB.GetByID(context.Background(), store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
		t.Fatal(err)
	}
	return p.Branches["main"].Head
}

func revisionOf(t *testing.T, a *app.App, id string) models.Revision {
	t.Helper()
	var r models.Revision
	if err := a.DB.GetByID(context.Background(), store.CRevisions, a.WorkspaceID(), id, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// An app that may only propose reads and checks, and every change it asks for
// waits for a person as a draft: a new version beside the cut, a Content
// Settings change kept until approved. It can't approve, deliver, spend or do
// anything people-only, and what it saved says it made it.
func TestAnAppThatProposesOnlySavesDrafts(t *testing.T) {
	a := testapp.New(t)
	p := project(t, a, "Garden story")
	c1 := clip(t, a, "Garage interview", p.ID)
	v1 := saveCut(t, a, p.ID, "", c1.ID)
	do[ops.RevisionSummary](t, a, "revision.approve", ops.RevisionApproveIn{ID: v1})
	codex := agentApp(a, "codex", "mcp")

	// Changes are refused on its connection; drafts are what it saves.
	for _, c := range []struct {
		op   string
		in   any
		code string
	}{
		{"edit.apply", ops.EditApplyIn{Plan: clipOutPlan(t, v1, "ca", 2)}, apperr.Unauthorized},
		{"revision.approve", ops.RevisionApproveIn{ID: v1}, apperr.HumanOnly},
		{"draft.decide", ops.DraftDecideIn{ID: "drf_x", Decision: ops.DecideApprove}, apperr.HumanOnly},
		{"render.submit", ops.RenderSubmitIn{RevisionID: v1, Quality: "final"}, apperr.Unauthorized},
	} {
		err := doErr(codex, c.op, c.in, person) // even claiming to be a person
		if code(err) != c.code {
			t.Errorf("%s on a propose-only connection: %v", c.op, err)
		}
		if d, _ := apperr.From(err).Details.(map[string]any); d["reason"] != "propose_only" {
			t.Errorf("%s: refused for another reason: %v", c.op, err)
		}
	}
	// Reads and checks run.
	do[ops.EditCheckOut](t, codex, "edit.check", ops.EditCheckIn{Plan: clipOutPlan(t, v1, "ca", 2)})

	ver := do[ops.DraftView](t, codex, ops.OpDraftSave, ops.DraftSaveIn{Op: "edit.apply", Input: draftInput(t, ops.EditApplyIn{Plan: clipOutPlan(t, v1, "ca", 2)}),
		Note: "The intro dragged."})
	if ver.Kind != models.DraftVersion || ver.State != models.DraftWaiting || ver.Revision == nil || ver.Revision.Number != 2 || ver.Revision.IsHead ||
		ver.Agent != "codex" || ver.AgentName != "Codex" || ver.Via != "mcp" || len(ver.Changes) == 0 || ver.Note != "The intro dragged." {
		t.Fatalf("version draft: %+v", ver)
	}
	if h := headOf(t, a, p.ID); h != v1 {
		t.Fatalf("a draft moved the cut's latest version to %s", h)
	}
	if r := revisionOf(t, a, ver.Revision.ID); r.DraftID != ver.ID || r.CreatedBy.Agent != "codex" || r.ParentID != v1 || r.Origin != "draft:"+ver.ID {
		t.Fatalf("the draft's version: %+v", r)
	}
	rule := do[ops.DraftView](t, codex, ops.OpDraftSave, ops.DraftSaveIn{Op: "content.rules.add", Input: draftInput(t, map[string]any{"text": "The name bar never animates"})})
	if rule.Kind != models.DraftChange || !strings.Contains(rule.Title, "“The name bar never animates”") || !strings.Contains(strings.Join(rule.Changes, " "), "the Content Settings of “Garden story”, the default") {
		t.Fatalf("change draft: %+v", rule)
	}
	if rs := do[ops.HouseRulesOut](t, a, "content.rules.list", ops.HouseRulesIn{}); len(rs.Rules) != 0 {
		t.Fatalf("a draft made the change: %+v", rs.Rules)
	}
	for _, c := range []struct {
		op   string
		in   any
		code string
	}{
		{"render.submit", ops.RenderSubmitIn{RevisionID: v1}, apperr.Unauthorized},
		{"generate.shot", map[string]any{"projectId": p.ID}, apperr.Unauthorized},
		{"revision.approve", ops.RevisionApproveIn{ID: v1}, apperr.HumanOnly},
		{"project.list", map[string]any{}, apperr.InvalidInput},
		{"render.follow.set", map[string]any{"on": false}, apperr.Unauthorized}, // owners'
		{"edit.apply", ops.EditApplyIn{Plan: clipOutPlan(t, v1, "ca", 2), AllowUnlock: true}, apperr.Locked},
		{"content.rules.add", map[string]any{"words": "x"}, apperr.InvalidInput},
	} {
		if err := doErr(codex, ops.OpDraftSave, ops.DraftSaveIn{Op: c.op, Input: draftInput(t, c.in)}, ops.Caller{}); code(err) != c.code {
			t.Errorf("a draft of %s: %v", c.op, err)
		}
	}

	// The desk lists both; the app sees its own.
	w := do[ops.ReviewWaitingOut](t, asPerson(a), "review.waiting", ops.ReviewWaitingIn{})
	if len(w.Versions) != 1 || w.Versions[0].Why != ops.WaitingDraft || w.Versions[0].Draft == nil || w.Versions[0].Draft.ID != ver.ID ||
		!strings.HasPrefix(w.Versions[0].Said, "Codex made it, through MetaRush's tools for AI apps") {
		t.Fatalf("waiting versions: %+v", w.Versions)
	}
	if len(w.Changes) != 1 || w.Changes[0].ID != rule.ID {
		t.Fatalf("waiting changes: %+v", w.Changes)
	}
	if l := do[ops.DraftListOut](t, codex, "draft.list", ops.DraftListIn{}); len(l.Drafts) != 2 {
		t.Fatalf("codex's drafts: %+v", l.Drafts)
	}
	if l := do[ops.DraftListOut](t, agentApp(a, "cursor", "mcp"), "draft.list", ops.DraftListIn{}); len(l.Drafts) != 0 {
		t.Fatalf("cursor sees codex's drafts: %+v", l.Drafts)
	}

	// Not quite needs words; they go back to the draft (and its version reads Needs changes).
	pa := asPerson(a)
	if err := doErr(pa, "draft.decide", ops.DraftDecideIn{ID: ver.ID, Decision: ops.DecideNotQuite}, person); code(err) != apperr.InvalidInput {
		t.Fatalf("Not quite without a note: %v", err)
	}
	nq := do[ops.DraftView](t, pa, "draft.decide", ops.DraftDecideIn{ID: ver.ID, Decision: ops.DecideNotQuite, Note: "Keep the first two seconds"})
	if nq.State != models.DraftNotQuite || nq.Decision == nil || nq.Decision.Note != "Keep the first two seconds" || nq.Decision.By.UserName != "tester" {
		t.Fatalf("Not quite: %+v", nq)
	}
	if r := revisionOf(t, a, ver.Revision.ID); r.Approval.State != models.ApprovalRejected || r.Approval.Note != "Keep the first two seconds" {
		t.Fatalf("the draft's version after Not quite: %+v", r.Approval)
	}
	// The app's next draft takes its place.
	ver2 := do[ops.DraftView](t, codex, ops.OpDraftSave, ops.DraftSaveIn{Op: "edit.apply", Input: draftInput(t, ops.EditApplyIn{Plan: clipOutPlan(t, v1, "ca", 2.5)}),
		Note: "Kept the first two seconds.", Replaces: ver.ID})
	if ver2.Replaces != ver.ID || ver2.Revision.Number != 3 {
		t.Fatalf("the next draft: %+v", ver2)
	}
	if l := do[ops.DraftListOut](t, codex, "draft.list", ops.DraftListIn{State: models.DraftReplaced}); len(l.Drafts) != 1 || l.Drafts[0].ReplacedBy != ver2.ID {
		t.Fatalf("the replaced draft: %+v", l.Drafts)
	}
	if err := doErr(pa, "draft.decide", ops.DraftDecideIn{ID: ver.ID, Decision: ops.DecideApprove}, person); code(err) != apperr.Conflict {
		t.Fatalf("approving a replaced draft: %v", err)
	}

	// Approving: the version goes into the cut, approved; the change is made as the person.
	ok := do[ops.DraftView](t, pa, "draft.decide", ops.DraftDecideIn{ID: ver2.ID, Decision: ops.DecideApprove})
	if ok.State != models.DraftApproved || ok.Decision.RevisionID != ver2.Revision.ID || headOf(t, a, p.ID) != ver2.Revision.ID {
		t.Fatalf("approved version draft: %+v (head %s)", ok, headOf(t, a, p.ID))
	}
	if r := revisionOf(t, a, ver2.Revision.ID); r.Approval.State != models.ApprovalApproved || r.Approval.By == nil || r.Approval.By.Via != "dashboard" {
		t.Fatalf("approval: %+v", r.Approval)
	}
	done := do[ops.DraftView](t, pa, "draft.decide", ops.DraftDecideIn{ID: rule.ID, Decision: ops.DecideApprove})
	if done.State != models.DraftApproved || done.Decision.Result == "" {
		t.Fatalf("approved change: %+v", done)
	}
	if rs := do[ops.HouseRulesOut](t, a, "content.rules.list", ops.HouseRulesIn{}); len(rs.Rules) != 1 || rs.Rules[0].Text != "The name bar never animates" {
		t.Fatalf("house rules after approving: %+v", rs.Rules)
	}
	w = do[ops.ReviewWaitingOut](t, pa, "review.waiting", ops.ReviewWaitingIn{})
	if len(w.Versions) != 0 || len(w.Changes) != 0 {
		t.Fatalf("still waiting: %+v %+v", w.Versions, w.Changes)
	}
	var audit int64
	audit, _ = a.DB.C(store.CAudit).CountDocuments(context.Background(), bson.M{"workspaceId": a.WorkspaceID(), "action": bson.M{"$in": bson.A{"draft.save", "draft.approved", "draft.not_quite"}}})
	if audit < 5 {
		t.Errorf("audit trail: %d draft entries", audit)
	}
}

// A draft written on an older version goes in on top of what was saved
// meanwhile when the two don't overlap (a new version, approved), and stays
// waiting, saying why, when they do. revision.approve on a draft's version is
// the same decision.
func TestApprovingADraftKeepsBothChanges(t *testing.T) {
	a := testapp.New(t)
	p := project(t, a, "Two clips")
	c1, c2 := clip(t, a, "First", p.ID), clip(t, a, "Second", p.ID)
	v1 := saveCut(t, a, p.ID, "", c1.ID, c2.ID)
	cursor := agentApp(a, "cursor", "cli")
	d := do[ops.DraftView](t, cursor, ops.OpDraftSave, ops.DraftSaveIn{Op: "edit.apply", Input: draftInput(t, ops.EditApplyIn{Plan: clipOutPlan(t, v1, "ca", 2)})})
	clash := do[ops.DraftView](t, cursor, ops.OpDraftSave, ops.DraftSaveIn{Op: "edit.apply", Input: draftInput(t, ops.EditApplyIn{Plan: clipOutPlan(t, v1, "cb", 1)})})
	// Someone shortens the second clip meanwhile.
	chk := do[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: clipOutPlan(t, v1, "cb", 2.5)})
	v4 := do[ops.EditApplyOut](t, a, "edit.apply", ops.EditApplyIn{Plan: clipOutPlan(t, v1, "cb", 2.5), ConfirmToken: chk.ConfirmToken}).Revision.ID

	pa := asPerson(a)
	if v := do[ops.DraftListOut](t, pa, "draft.list", ops.DraftListIn{All: true, State: models.DraftWaiting}); len(v.Drafts) != 2 || !v.Drafts[0].HeadMoved {
		t.Fatalf("drafts after the cut moved: %+v", v.Drafts)
	}
	s := do[ops.RevisionSummary](t, pa, "revision.approve", ops.RevisionApproveIn{ID: d.Revision.ID})
	if s.ID == d.Revision.ID || !s.IsHead || s.Approval.State != models.ApprovalApproved || s.ParentID != v4 {
		t.Fatalf("approved on top of the newer version: %+v", s)
	}
	r := revisionOf(t, a, s.ID)
	if r.Spec.Clips[0].Out != 2 || r.Spec.Clips[1].Out != 2.5 || r.CreatedBy.Agent != "cursor" || r.Origin != "draft:"+d.ID {
		t.Fatalf("both changes kept, the app's in its name: %+v %+v", r.Spec.Clips, r.CreatedBy)
	}
	if v := do[ops.DraftListOut](t, pa, "draft.list", ops.DraftListIn{Agent: "cursor", State: models.DraftApproved}); len(v.Drafts) != 1 || v.Drafts[0].Decision.RevisionNumber != r.Number {
		t.Fatalf("the approved draft: %+v", v.Drafts)
	}
	// The other draft changed the same clip as the person: it waits, saying why.
	err := doErr(pa, "draft.decide", ops.DraftDecideIn{ID: clash.ID, Decision: ops.DecideApprove}, person)
	if code(err) != apperr.RevisionConflict {
		t.Fatalf("an overlapping draft: %v", err)
	}
	v := do[ops.DraftListOut](t, pa, "draft.list", ops.DraftListIn{All: true, State: models.DraftWaiting})
	if len(v.Drafts) != 1 || v.Drafts[0].Tried == nil || v.Drafts[0].Tried.Code != apperr.RevisionConflict {
		t.Fatalf("the overlapping draft: %+v", v.Drafts)
	}
	// Rejecting its version with a note is Not quite; without one, turned down.
	s = do[ops.RevisionSummary](t, pa, "revision.approve", ops.RevisionApproveIn{ID: clash.Revision.ID, Reject: true, Note: "Leave the second clip"})
	if s.Approval.State != models.ApprovalRejected {
		t.Fatalf("rejected: %+v", s)
	}
	if v := do[ops.DraftListOut](t, pa, "draft.list", ops.DraftListIn{All: true, State: models.DraftNotQuite}); len(v.Drafts) != 1 || v.Drafts[0].Tried != nil {
		t.Fatalf("not quite: %+v", v.Drafts)
	}
}

// A change written on a draft's version (never in the cut) is the draft and
// the change together, against the version the draft was written on.
func TestADraftOnADraftKeepsBoth(t *testing.T) {
	a := testapp.New(t)
	p := project(t, a, "Stacked")
	c1, c2 := clip(t, a, "First", p.ID), clip(t, a, "Second", p.ID)
	v1 := saveCut(t, a, p.ID, "", c1.ID, c2.ID)
	codex := agentApp(a, "codex", "mcp")
	d1 := do[ops.DraftView](t, codex, ops.OpDraftSave, ops.DraftSaveIn{Op: "edit.apply", Input: draftInput(t, ops.EditApplyIn{Plan: clipOutPlan(t, v1, "ca", 2)})})
	d2 := do[ops.DraftView](t, codex, ops.OpDraftSave, ops.DraftSaveIn{Op: "edit.apply", Input: draftInput(t, ops.EditApplyIn{Plan: clipOutPlan(t, d1.Revision.ID, "cb", 1.5)}), Replaces: d1.ID})
	r := revisionOf(t, a, d2.Revision.ID)
	if r.ParentID != v1 || r.Spec.Clips[0].Out != 2 || r.Spec.Clips[1].Out != 1.5 {
		t.Fatalf("a draft on a draft: parent %s, clips %+v", r.ParentID, r.Spec.Clips)
	}
	// Its sheet reads as a version that isn't the latest, and a person's
	// change on it keeps the draft's changes too.
	sheet := do[ops.CutShowOut](t, codex, "cut.show", ops.CutShowIn{ProjectID: p.ID, Revision: d2.Revision.ID})
	if !sheet.ReadOnly {
		t.Fatalf("a draft's sheet reads as the latest: %+v", sheet.Revision)
	}
	title := "Stacked, shorter"
	plan, _ := json.Marshal(editspec.Plan{BaseRevisionID: d2.Revision.ID, Instruction: "A title", Ops: []editspec.Op{{Op: "setTitle", Text: &title}}})
	chk := do[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: plan})
	if !chk.Valid || chk.BaseRevisionID != v1 {
		t.Fatalf("checking a change on the draft: valid %v base %s %+v", chk.Valid, chk.BaseRevisionID, chk.Errors)
	}
	saved := do[ops.EditApplyOut](t, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}).Revision
	if r := revisionOf(t, a, saved.ID); r.ParentID != v1 || r.Spec.Title != title || r.Spec.Clips[0].Out != 2 || r.Spec.Clips[1].Out != 1.5 || headOf(t, a, p.ID) != saved.ID {
		t.Fatalf("a change on the draft: parent %s, %q, %+v", r.ParentID, r.Spec.Title, r.Spec.Clips)
	}
}

// The one-page packet: what changed and why, checks, cost, who made it and
// what is waiting, as data and as Markdown; for a version no draft holds too.
func TestThePacket(t *testing.T) {
	a := testapp.New(t)
	p := project(t, a, "Packet")
	c1 := clip(t, a, "Only clip", p.ID)
	v1 := saveCut(t, a, p.ID, "", c1.ID)
	codex := agentApp(a, "codex", "mcp")
	d := do[ops.DraftView](t, codex, ops.OpDraftSave, ops.DraftSaveIn{Op: "edit.apply", Input: draftInput(t, ops.EditApplyIn{Plan: clipOutPlan(t, v1, "ca", 2)}),
		Note: "Tighter for the client."})
	pk := do[ops.Packet](t, a, "review.packet", ops.PacketIn{ID: d.ID, Format: "markdown"})
	if pk.Kind != models.DraftVersion || pk.InCut || !strings.HasPrefix(pk.Status, "Waiting for your review") || pk.Who.AgentName != "Codex" ||
		len(pk.Changes) == 0 || len(pk.Why) == 0 || pk.Why[0] != "Tighter for the client." || !strings.Contains(pk.Receipt, "These changes cost nothing to make") || len(pk.Waiting) == 0 {
		t.Fatalf("packet: %+v", pk)
	}
	for _, want := range []string{"# Version 2 of “Packet”", "## What changes", "## Why", "> Tighter for the client.", "## Checks", "## Cost", "## What's waiting", "/review/" + d.ID} {
		if !strings.Contains(pk.Markdown, want) {
			t.Errorf("the Markdown has no %q:\n%s", want, pk.Markdown)
		}
	}
	// By the version it holds, the same page.
	if byRev := do[ops.Packet](t, a, "review.packet", ops.PacketIn{ID: d.Revision.ID}); byRev.ID != d.ID || byRev.Markdown != "" {
		t.Fatalf("packet by the draft's version: %+v", byRev)
	}
	vp := do[ops.Packet](t, a, "review.packet", ops.PacketIn{ID: v1, Format: "markdown"})
	if vp.ID != v1 || !vp.InCut || !strings.HasPrefix(vp.Status, "Draft (the latest version)") || vp.Who.AgentName != "Claude" || !strings.Contains(vp.Markdown, "# Version 1 of “Packet”") {
		t.Fatalf("version packet: %+v", vp)
	}
	if err := doErr(a, "review.packet", ops.PacketIn{ID: "drf_nothing"}, person); code(err) != apperr.NotFound {
		t.Fatalf("an unknown draft: %v", err)
	}
}

// Review copies follow AI apps' drafts of versions too (D187, D249), and a
// draft's copy never replaces the cut's own.
func TestReviewCopiesFollowDrafts(t *testing.T) {
	a := testapp.New(t)
	p := project(t, a, "Follow drafts")
	c1 := clip(t, a, "A clip", p.ID)
	v1 := saveCut(t, a, p.ID, "", c1.ID)
	d := do[ops.DraftView](t, agentApp(a, "codex", "mcp"), ops.OpDraftSave, ops.DraftSaveIn{Op: "edit.apply", Input: draftInput(t, ops.EditApplyIn{Plan: clipOutPlan(t, v1, "ca", 2)})})
	res := follow(t, a, time.Now().Add(ops.ReviewFollowQuiet+time.Second))
	if len(res.Superseded) != 0 || len(followRenders(t, a, v1)) != 1 || len(followRenders(t, a, d.Revision.ID)) != 1 {
		t.Fatalf("review copies: %+v (cut %d, draft %d)", res, len(followRenders(t, a, v1)), len(followRenders(t, a, d.Revision.ID)))
	}
	if v := do[ops.DraftListOut](t, a, "draft.list", ops.DraftListIn{All: true}); len(v.Drafts) != 1 || v.Drafts[0].ReviewCopy == nil || v.Drafts[0].ReviewCopy.Said != "Being made" {
		t.Fatalf("the draft's review copy: %+v", v.Drafts[0].ReviewCopy)
	}
}
