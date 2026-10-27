package ops_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/qa"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// finalRender stores a finished final render of a version (random bytes).
func finalRender(t *testing.T, a *app.App, revisionID string) models.Render {
	t.Helper()
	ctx := context.Background()
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), revisionID, &r); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4096)
	_, _ = rand.Read(b)
	p := filepath.Join(t.TempDir(), "final.mp4")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(ctx, key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: r.ProjectID, RevisionID: r.ID, RevisionNumber: r.Number, SpecHash: r.SpecHash,
		Preset: r.Spec.Output.Preset, Quality: "final", State: models.RenderAvailable, CreatedAt: now, CompletedAt: &now,
		Output: &models.RenderOutput{StorageKey: key, SHA256: sha, Size: int64(len(b)), Duration: r.Duration, Width: 1080, Height: 1920, FPS: 30}}
	if err := a.DB.Insert(ctx, store.CRenders, rd); err != nil {
		t.Fatal(err)
	}
	return rd
}

// checked records a quality check of a render: its findings and the promises it measured.
func checked(t *testing.T, a *app.App, rd models.Render, findings []models.QAFinding, promises []models.QAPromise) models.QAReport {
	t.Helper()
	ctx := context.Background()
	rep := models.QAReport{ID: ids.New(models.QAReportPrefix), WorkspaceID: a.WorkspaceID(), ProjectID: rd.ProjectID, RevisionID: rd.RevisionID, RevisionNumber: rd.RevisionNumber,
		RenderID: rd.ID, Preset: rd.Preset, Quality: rd.Quality, Version: qa.Version, Verdict: models.QAPass, Findings: findings, Promises: promises,
		Checks: []models.QACheck{}, Frames: []models.QAFrame{}, Limitations: []string{}, CreatedAt: time.Now().UTC()}
	for i := range rep.Findings {
		rep.Findings[i].ID = "f" + string(rune('1'+i))
		switch rep.Findings[i].Severity {
		case models.QAError:
			rep.Counts.Errors++
			rep.Verdict = models.QAFail
		case models.QAWarning:
			rep.Counts.Warnings++
			if rep.Verdict == models.QAPass {
				rep.Verdict = models.QAWarnings
			}
		}
	}
	if err := a.DB.Insert(ctx, store.CQAReports, rep); err != nil {
		t.Fatal(err)
	}
	sum := models.RenderQA{ReportID: rep.ID, Verdict: rep.Verdict, Errors: rep.Counts.Errors, Warnings: rep.Counts.Warnings, At: rep.CreatedAt}
	if err := a.DB.Update(ctx, store.CRenders, a.WorkspaceID(), rd.ID, bson.M{"$set": bson.M{"qa": sum}}); err != nil {
		t.Fatal(err)
	}
	return rep
}

// shownKeys are the keys of the holds a person sees (and sends it out past).
func shownKeys(g *ops.ShipGateOut) []string {
	var out []string
	for _, h := range g.Holds {
		if h.Overridable && !h.Covered {
			out = append(out, h.Key)
		}
	}
	return out
}

func holdKinds(g *ops.ShipGateOut) []string {
	var out []string
	for _, h := range g.Holds {
		if !h.Covered {
			out = append(out, h.Kind)
		}
	}
	return out
}

// The ship gate (D298, D299): an older workspace is asked once and nothing
// changes until an owner answers; with the gate on a version waits while its
// final render hasn't been checked, the check found problems or a sign-off
// waits — delivery refuses it with what holds it — until those are done or a
// person sends it out anyway with a note (never an agent, Auto or a draft),
// which covers only what it saw and is written into the delivery record, its
// notes file and the receipt. Loosening steps are a person's while it is on.
func TestShipGate(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T"}); err != nil {
		t.Fatal(err)
	}
	p := project(t, a, "Garden story")
	as := txClip(t, a, "Interview", p.ID, []models.Word{wd("We", 0.1, 0.3, 0.9), wd("met", 0.35, 0.6, 0.9), wd("in", 0.7, 0.9, 0.9), wd("Elmwood.", 1.0, 2.2, 0.9)})
	rid := captionedCut(t, a, p.ID, as.ID)
	ready := func() *ops.ShipGateOut {
		t.Helper()
		return do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: p.ID})
	}

	// 1. Nobody decided: it only reminds, and owners are asked once.
	g := ready()
	if g.On || g.Decided || g.Ready || !strings.Contains(strings.Join(holdKinds(g), ","), "approval") {
		t.Fatalf("undecided: %+v", g)
	}
	if asked, err := ops.AskAboutShipGate(ctx, a); err != nil || !asked {
		t.Fatalf("asked %v %v", asked, err)
	}
	var todo models.Todo
	if err := a.DB.C(store.CTodos).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "kind": models.TodoShipGate, "state": models.TodoOpen}).Decode(&todo); err != nil {
		t.Fatalf("no question: %v", err)
	}
	if err := doErr(a, "todos.answer", ops.TodoAnswerIn{ID: todo.ID, Suggestion: 1}, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an agent answered: %v", err)
	}
	do[ops.TodoAnswerOut](t, a, "todos.answer", ops.TodoAnswerIn{ID: todo.ID, Suggestion: 1})
	if g := ready(); !g.On || !g.Decided {
		t.Fatalf("on after the answer: %+v", g)
	}
	if asked, _ := ops.AskAboutShipGate(ctx, a); asked {
		t.Fatal("asked again after the workspace decided")
	}

	// 2. Approved, but no final render yet: nothing to send (not overridable).
	do[ops.RevisionSummary](t, a, "revision.approve", ops.RevisionApproveIn{ID: rid})
	if g := ready(); g.Ready || strings.Join(holdKinds(g), ",") != models.HoldRender || g.Holds[0].Overridable || g.Holds[0].Paths[0].Op != "render.submit" {
		t.Fatalf("no render: %+v", g)
	}

	// 3. A final render nobody checked: held, and delivery refuses it.
	rd := finalRender(t, a, rid)
	g = ready()
	if g.Ready || g.State != ops.GateHeld || strings.Join(holdKinds(g), ",") != models.HoldQAMissing || g.Holds[0].Paths[0].Op != "qa.run" || g.Headline != "Held by 1 thing" {
		t.Fatalf("unchecked: %+v", g)
	}
	dir := t.TempDir()
	err := doErr(a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: dir}, person)
	if code(err) != "E_HELD" || !strings.Contains(err.Error(), "quality check hasn't looked at its final render") {
		t.Fatalf("delivered unchecked: %v", err)
	}

	// 4. Checked: a problem (a broken promise) holds it, a warning never does.
	checked(t, a, rd, []models.QAFinding{
		{Severity: models.QAError, Kind: "promise_broken", From: 0, To: 3, Message: "The promise “Captions on every video” isn't kept: it has no captions.",
			Fix: &models.QAFix{Summary: "Turn the captions on."}},
		{Severity: models.QAWarning, Kind: "codec", Message: "Worth a look."},
	}, nil)
	g = ready()
	if len(g.Holds) != 1 || g.Holds[0].Kind != models.HoldQAErrors || len(g.Holds[0].Problems) != 1 || !g.Holds[0].Problems[0].Promise || g.Holds[0].Problems[0].Fix != "Turn the captions on." ||
		g.Renders[0].Check != "problems" {
		t.Fatalf("problems: %+v", g)
	}
	// A sign-off waiting holds it too.
	do[ops.SignoffsOut](t, a, "cut.signoffs.add", ops.SignoffAddIn{SignoffsIn: ops.SignoffsIn{ProjectID: p.ID}, Text: "Lena OKs the final version"})
	g = ready()
	if strings.Join(holdKinds(g), ",") != "qa-errors,signoff" || g.Headline != "Held by 2 things" || !g.Holds[1].Paths[0].People {
		t.Fatalf("a sign-off: %+v", g)
	}
	for _, l := range append(g.Lines, g.Summary, g.Headline, g.Why) {
		if fs := plainwords.CheckShown(l); fs != nil {
			t.Errorf("%q: %v", l, fs)
		}
	}

	// 5. Only a person sends it out anyway: an agent, Claude in the panel, an
	// app that may only propose and a draft are all refused.
	note := ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: p.ID}, Note: "The client needs it today; captions come in version 2.", Holds: shownKeys(ready())}
	if err := doErr(a, "delivery.override", note, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an agent sent it out: %v", err)
	}
	panel := *a
	panel.Actor.Via = models.ViaAssistant
	if err := doErr(&panel, "delivery.override", note, person); err == nil {
		t.Fatal("Claude in the panel sent it out")
	}
	propose := *a
	propose.ProposeOnly = true
	if err := doErr(&propose, "delivery.override", note, ops.Caller{}); err == nil {
		t.Fatal("a propose-only app sent it out")
	}
	in, _ := json.Marshal(note)
	err = doErr(&propose, "draft.save", ops.DraftSaveIn{Op: "delivery.override", Input: in}, ops.Caller{})
	if code(err) != "E_HUMAN_ONLY" || !strings.Contains(err.Error(), "a person's decision, with their own note") {
		t.Fatalf("a draft held an override: %v", err)
	}
	if op, _ := ops.Get("delivery.override"); ops.Draftable(op) {
		t.Fatal("the review desk lists the override as draftable")
	}
	if err := doErr(a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: p.ID}, Note: "ok", Holds: note.Holds}, person); code(err) != "E_INVALID_INPUT" {
		t.Fatalf("no real note: %v", err)
	}
	if err := doErr(a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: p.ID}, Note: note.Note}, person); code(err) != "E_INVALID_INPUT" {
		t.Fatalf("a note that names no holds: %v", err)
	}
	if err := doErr(a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: p.ID}, Note: note.Note, Holds: note.Holds[:1]}, person); code(err) != "E_CONFLICT" {
		t.Fatalf("a note for only some of what holds it now: %v", err)
	}
	// Loosening steps are a person's while the gate is on.
	tick := ops.SignoffDecideIn{SignoffsIn: ops.SignoffsIn{ProjectID: p.ID}, Signoff: "1"}
	if err := doErr(a, "cut.signoffs.done", tick, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an agent ticked a sign-off: %v", err)
	}
	if err := doErr(a, "delivery.gate.set", ops.GateSetIn{On: false}, ops.Caller{Owner: true}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an agent turned the gate off: %v", err)
	}

	sent := do[ops.ShipGateOut](t, a, "delivery.override", note)
	if !sent.Ready || sent.State != ops.GateSentAnyway || sent.Override == nil || sent.Override.Note != note.Note || len(sent.Override.Holds) != 2 || len(holdKinds(sent)) != 0 {
		t.Fatalf("sent anyway: %+v", sent)
	}
	if err := doErr(a, "delivery.override", note, person); code(err) != "E_CONFLICT" {
		t.Fatalf("a second note for nothing: %v", err)
	}

	// 6. Delivery goes ahead and says so: the record, the notes file, the receipt.
	out, err := ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: dir}, person)
	if err != nil || len(out.Files) != 1 || len(out.Files[0].SentAnyway) != 1 || !strings.Contains(out.Note, "sent it out anyway") {
		t.Fatalf("deliver: %+v %v", out, err)
	}
	var sc ops.DeliverySidecar
	b, _ := os.ReadFile(out.Files[0].Sidecar)
	_ = json.Unmarshal(b, &sc)
	if sc.Gate == nil || !sc.Gate.On || len(sc.Gate.Held) != 2 || len(sc.Gate.SentAnyway) != 1 || sc.Gate.SentAnyway[0].Note != note.Note {
		t.Fatalf("the record: %s", b)
	}
	nf, err := os.ReadFile(filepath.Join(dir, sc.NotesFile))
	if err != nil || !strings.Contains(string(nf), "## Sent out anyway") || !strings.Contains(string(nf), "Lena OKs the final version") || !strings.Contains(string(nf), note.Note) {
		t.Fatalf("the notes file: %s %v", nf, err)
	}
	if d, err := ops.ParseDeliveryNotes(nf); err != nil || d.Description != "" {
		t.Fatalf("the notes file reads back as notes: %+v %v", d, err)
	}
	var stored models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &stored)
	if len(stored.ShipOverrides) != 1 || stored.ShipOverrides[0].By.Via == "" || stored.ShipOverrides[0].At.IsZero() {
		t.Fatalf("the version keeps who and when: %+v", stored.ShipOverrides)
	}

	// 7. A note covers only what it saw: a new sign-off holds it again, and
	// a promise made after the check makes the check out of date.
	do[ops.SignoffsOut](t, a, "cut.signoffs.add", ops.SignoffAddIn{SignoffsIn: ops.SignoffsIn{ProjectID: p.ID}, Text: "Legal OKs the claims"})
	do[ops.RuleAddOut](t, a, "project.rules.add", ops.RuleAddIn{ProjectID: p.ID, Text: "Under 60 seconds for shorts", Promise: true})
	g = ready()
	if strings.Join(holdKinds(g), ",") != "qa-stale,signoff" || !strings.Contains(g.Summary, "checked before the promise “Under 60 seconds for shorts”") {
		t.Fatalf("new holds: %+v", g)
	}
	if err := doErr(a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir()}, person); code(err) != "E_HELD" {
		t.Fatalf("delivered past a new hold: %v", err)
	}
	// A promise MetaRush can't measure stays a sign-off; taking one back is a person's.
	if err := doErr(a, "project.rules.add", ops.RuleAddIn{ProjectID: p.ID, Text: "Warm and friendly", Promise: true}, person); code(err) != "E_INVALID_INPUT" {
		t.Fatalf("an unmeasurable promise: %v", err)
	}
	if err := doErr(a, "project.rules.remove", ops.RuleRemoveIn{ProjectID: p.ID, Rule: "Under 60 seconds for shorts"}, ops.Caller{}); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an agent took a promise back: %v", err)
	}

	// 8. Timelines into a delivery folder wait the same way; elsewhere they don't.
	err = doErr(a, "export.timeline", ops.ExportTimelineIn{RevisionID: rid, Format: "otio", Out: filepath.Join(dir, "garden.otio")}, person)
	if code(err) != "E_HELD" {
		t.Fatalf("a timeline into the delivery folder: %v", err)
	}
	if !ops.IsDeliveryFolder(dir) || ops.IsDeliveryFolder(t.TempDir()) {
		t.Fatal("delivery folders")
	}

	// 9. A person turns it off: everything only reminds, and the record says what would have held it.
	do[ops.GateSettingsOut](t, a, "delivery.gate.set", ops.GateSetIn{On: false})
	g = ready()
	if g.On || !g.Ready || g.State != ops.GateReminders {
		t.Fatalf("off: %+v", g)
	}
	out, err = ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir()}, person)
	if err != nil || len(out.Signoffs) != 2 {
		t.Fatalf("deliver with the gate off: %+v %v", out, err)
	}
	b, _ = os.ReadFile(out.Files[0].Sidecar)
	sc = ops.DeliverySidecar{}
	_ = json.Unmarshal(b, &sc)
	if sc.Gate == nil || sc.Gate.On || len(sc.Gate.Held) != 3 || len(sc.Gate.SentAnyway) != 0 {
		t.Fatalf("the record with the gate off: %s", b)
	}

	// 10. A client's settings hold their videos whatever the workspace says (owners).
	if err := doErr(a, "content.gate.set", ops.ContentGateSetIn{ProjectID: p.ID, Mode: "on"}, ops.Caller{Remote: true}); err == nil {
		t.Fatal("a member over the network set the client's gate")
	}
	gs := do[ops.GateSettingsOut](t, a, "content.gate.set", ops.ContentGateSetIn{ProjectID: p.ID, Mode: "on"})
	if !gs.On || gs.Settings == nil || gs.Settings.Mode != models.GateOn {
		t.Fatalf("the settings' gate: %+v", gs)
	}
	if g := ready(); !g.On || g.Ready || !strings.Contains(g.Why, "settings hold every version") {
		t.Fatalf("held by the settings: %+v", g)
	}
	cs := do[ops.ContentShowOut](t, a, "content.show", ops.ContentShowIn{ProjectID: p.ID})
	found := false
	for _, part := range cs.Parts {
		found = found || (part.Name == "gate" && part.Summary == "Videos wait for the quality check and sign-offs")
	}
	if !found {
		t.Fatalf("the part: %+v", cs.Parts)
	}
	// Ticking it as a person clears the sign-off holds.
	do[ops.SignoffsOut](t, a, "cut.signoffs.done", tick)
	do[ops.SignoffsOut](t, a, "cut.signoffs.done", ops.SignoffDecideIn{SignoffsIn: ops.SignoffsIn{ProjectID: p.ID}, Signoff: "Legal OKs the claims"})
	if g := ready(); strings.Join(holdKinds(g), ",") != "qa-stale" {
		t.Fatalf("after the sign-offs: %+v", holdKinds(g))
	}

	// 11. Checked again with today's promises: a broken one holds it, and a
	// person taking that promise back lets it go without another check.
	fps := do[ops.RuleAddOut](t, a, "project.rules.add", ops.RuleAddIn{ProjectID: p.ID, Text: "At least 50 fps", Promise: true})
	var shorts models.ProjectRule
	for _, r := range fps.Rules {
		if r.Text == "Under 60 seconds for shorts" {
			shorts = r
		}
	}
	checked(t, a, rd, []models.QAFinding{{Severity: models.QAError, Kind: "promise_broken", Message: "The promise “At least 50 fps” isn't kept: it plays 30 frames a second."}},
		[]models.QAPromise{{RuleID: shorts.ID, Measure: shorts.Measure, Status: models.PromiseKept}, {RuleID: fps.Rule.ID, Measure: fps.Rule.Measure, Status: models.PromiseBroken, FindingID: "f1"}})
	if g := ready(); strings.Join(holdKinds(g), ",") != "qa-errors" || len(g.Promises) != 2 {
		t.Fatalf("a broken promise: %v %+v", holdKinds(g), g.Promises)
	}
	do[ops.RulesOut](t, a, "project.rules.remove", ops.RuleRemoveIn{ProjectID: p.ID, Rule: "At least 50 fps"})
	if g := ready(); len(holdKinds(g)) != 0 || !g.Ready || g.Renders[0].Errors != 0 {
		t.Fatalf("a promise taken back still holds it: %v %+v", holdKinds(g), g.Renders)
	}
	if g := ready(); g.Headline != "Ready to deliver" || len(g.Promises) != 1 {
		t.Fatalf("ready: %q %+v", g.Headline, g.Promises)
	}
	// A promise MetaRush couldn't measure here never holds it, but "ready" says so.
	checked(t, a, rd, nil, []models.QAPromise{{RuleID: shorts.ID, Measure: shorts.Measure, Status: models.PromiseUnmeasured, Measured: "Not here."}})
	if g := ready(); !g.Ready || g.Unmeasured != 1 || g.Headline != "Ready, but 1 promise couldn't be checked here" {
		t.Fatalf("an unmeasured promise: %q %+v", g.Headline, g)
	}
}

// A version on a gated client's settings, approved and rendered with a
// clean check: what the review of D298–D302 tried.
func gatedVersion(t *testing.T, a *app.App, name string) (models.Project, string, models.Render) {
	t.Helper()
	p := project(t, a, name)
	as := txClip(t, a, "Interview", p.ID, []models.Word{wd("We", 0.1, 0.3, 0.9), wd("met", 0.35, 0.6, 0.9)})
	rid := captionedCut(t, a, p.ID, as.ID)
	do[ops.RevisionSummary](t, a, "revision.approve", ops.RevisionApproveIn{ID: rid})
	rd := finalRender(t, a, rid)
	return p, rid, rd
}

// The review of the gate (D299): an agent can't switch a project to settings
// that drop a gate, promises or house sign-offs (an owner as a person can);
// a note covers only the holds the person saw; delivery writes every note
// that covers what holds it, each with its person; notes are never capped;
// only an owner sets the one-time question aside; a folder inside a client's
// delivery folder, or one delivery wrote to, holds timelines; older computers
// are warned about.
func TestShipGateReview(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T"}); err != nil {
		t.Fatal(err)
	}
	agent := ops.Caller{}

	// 1. The client's settings (the default's) hold versions back, with a
	// promise and a house sign-off; an agent can't move a project off them.
	acme := project(t, a, "Acme brand")
	p, rid, rd := gatedVersion(t, a, "Garden story")
	do[ops.GateSettingsOut](t, a, "content.gate.set", ops.ContentGateSetIn{ProjectID: acme.ID, Mode: "on"})
	do[ops.HouseSignoffsOut](t, a, "content.signoffs.add", ops.HouseSignoffAddIn{ProjectID: acme.ID, Text: "Legal OKs it"})
	do[ops.HouseRulesOut](t, a, "content.rules.add", ops.HouseRuleAddIn{ProjectID: acme.ID, Text: "Under 60 seconds", Promise: true})
	checked(t, a, rd, nil, []models.QAPromise{})
	if err := doErr(a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: t.TempDir()}, agent); code(err) != "E_HELD" {
		t.Fatalf("held first: %v", err)
	}
	// An owner, as a person, gives another project empty settings of its own;
	// making those the default is an owner's too.
	other := project(t, a, "Internal tests")
	do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: other.ID})
	for _, sw := range []struct {
		op string
		in any
	}{
		{"content.own", ops.ContentOwnIn{ProjectID: p.ID}},
		{"content.follow", ops.ContentFollowIn{ProjectID: p.ID, Follow: other.ID}},
		{"content.setDefault", ops.ContentDefaultIn{ProjectID: other.ID}},
	} {
		err := doErr(a, sw.op, sw.in, agent)
		if err == nil {
			t.Fatalf("%s by an agent went through", sw.op)
		}
		if code(err) != "E_HUMAN_ONLY" || !strings.Contains(err.Error(), "holding versions back") || !strings.Contains(err.Error(), "“Legal OKs it”") || !strings.Contains(err.Error(), "“Under 60 seconds”") {
			t.Fatalf("%s by an agent: %v", sw.op, err)
		}
	}
	if g := do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: p.ID}); !g.On {
		t.Fatal("an agent's switch dropped the gate")
	}
	// A copy keeps it all, so anyone may make one; an owner, as a person, may switch.
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, a, "content.own", ops.ContentOwnIn{ProjectID: p.ID, Copy: true}, agent); err != nil {
		t.Fatalf("a copy: %v", err)
	}
	if g := do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: p.ID}); !g.On || len(holdKinds(g)) == 0 {
		t.Fatalf("the copy kept the gate: %+v", g)
	}
	do[ops.ContentShowOut](t, a, "content.follow", ops.ContentFollowIn{ProjectID: p.ID, UseDefault: true})

	// 2. A note covers the holds the person saw: one that appeared since is refused.
	seen := do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: p.ID})
	if err := doErr(a, "cut.signoffs.add", ops.SignoffAddIn{SignoffsIn: ops.SignoffsIn{ProjectID: p.ID}, Text: "Client legal OKs the claims"}, agent); err != nil {
		t.Fatal(err)
	}
	err := doErr(a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: p.ID}, Note: "Client needs it today.", Holds: shownKeys(seen)}, person)
	if code(err) != "E_CONFLICT" || !strings.Contains(err.Error(), "changed since you looked") || !strings.Contains(err.Error(), "Client legal OKs the claims") {
		t.Fatalf("a note past a hold nobody saw: %v", err)
	}

	// 3. Two people's notes, each past what they saw: delivery writes both.
	alice := do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: p.ID})
	do[ops.ShipGateOut](t, a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: p.ID}, Note: "ALICE: legal is on holiday, the claims are checked.", Holds: shownKeys(alice)})
	do[ops.SignoffsOut](t, a, "cut.signoffs.add", ops.SignoffAddIn{SignoffsIn: ops.SignoffsIn{ProjectID: p.ID}, Text: "Lena OKs it"})
	bob := *a
	bob.Actor.UserName = "Bob"
	g := do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: p.ID})
	if _, err := ops.CallTyped[ops.ShipGateOut](ctx, &bob, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: p.ID}, Note: "BOB: Lena said yes by phone.", Holds: shownKeys(g)}, person); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	out, err := ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: dir}, person)
	if err != nil || len(out.Files[0].SentAnyway) != 2 || !strings.Contains(out.Note, "tester and Bob") {
		t.Fatalf("both notes: %+v %v", out, err)
	}
	var sc ops.DeliverySidecar
	b, _ := os.ReadFile(out.Files[0].Sidecar)
	_ = json.Unmarshal(b, &sc)
	nf, _ := os.ReadFile(filepath.Join(dir, sc.NotesFile))
	if len(sc.Gate.SentAnyway) != 2 || !strings.Contains(string(nf), "ALICE:") || !strings.Contains(string(nf), "BOB:") || !strings.Contains(string(nf), "Bob sent this version out") {
		t.Fatalf("the record and notes file: %s\n%s", b, nf)
	}
	var stored models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &stored)
	if len(stored.SentOut) != 1 || len(stored.SentOut[0].Overrides) != 2 || stored.SentOut[0].What != "delivery" {
		t.Fatalf("what went out: %+v", stored.SentOut)
	}

	// 4. Notes are never dropped: 22 of them, every one kept.
	for i := 0; i < 20; i++ {
		do[ops.SignoffsOut](t, a, "cut.signoffs.add", ops.SignoffAddIn{SignoffsIn: ops.SignoffsIn{ProjectID: p.ID}, Text: fmt.Sprintf("OK number %d", i)})
		g := do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: p.ID})
		do[ops.ShipGateOut](t, a, "delivery.override", ops.ShipOverrideIn{ShipGateIn: ops.ShipGateIn{ProjectID: p.ID}, Note: fmt.Sprintf("Note %d", i), Holds: shownKeys(g)})
	}
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &stored)
	if len(stored.ShipOverrides) != 22 || !strings.HasPrefix(stored.ShipOverrides[0].Note, "ALICE") {
		t.Fatalf("notes kept: %d, first %q", len(stored.ShipOverrides), stored.ShipOverrides[0].Note)
	}

	// 7. Timelines into a folder inside a client's delivery folder wait too,
	// and so do ones into a folder delivery wrote to (no record needed).
	sub := filepath.Join(dir, "timelines")
	_ = os.MkdirAll(sub, 0o755)
	if !ops.ClientFolder(a, sub) {
		t.Fatal("a folder inside a delivery folder")
	}
	elsewhere := t.TempDir()
	if ops.ClientFolder(a, elsewhere) {
		t.Fatal("any folder is a client's")
	}
	if err := ops.RememberDeliveryFolder(a, elsewhere); err != nil {
		t.Fatal(err)
	}
	if !ops.ClientFolder(a, filepath.Join(elsewhere, "for-the-editor")) {
		t.Fatal("a folder delivery wrote to on this computer")
	}
	// Never the home folder or the top of a disk; the list is private.
	home, _ := os.UserHomeDir()
	for _, d := range []string{home, "/", "/Volumes/Client drive"} {
		if err := ops.RememberDeliveryFolder(a, d); err != nil {
			t.Fatal(err)
		}
		if ops.ClientFolder(a, filepath.Join(d, "anything")) {
			t.Fatalf("%s became a client's delivery folder", d)
		}
	}
	list := filepath.Join(a.Cfg.Home(), "state", "private", "delivery-folders.json")
	if st, err := os.Stat(list); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the remembered folders: %v %v", st, err)
	}
	if st, err := os.Stat(filepath.Dir(list)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("their folder: %v %v", st, err)
	}
	do[ops.SignoffsOut](t, a, "cut.signoffs.add", ops.SignoffAddIn{SignoffsIn: ops.SignoffsIn{ProjectID: p.ID}, Text: "One more OK"})
	if err := doErr(a, "export.timeline", ops.ExportTimelineIn{RevisionID: rid, Format: "otio", Out: filepath.Join(sub, "garden.otio")}, agent); code(err) != "E_HELD" {
		t.Fatalf("a timeline into a folder inside the client's: %v", err)
	}

	// 8. Older computers in the workspace are warned about while the gate is on.
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CInstallations, models.Installation{ID: ids.New(ids.Installation), WorkspaceID: a.WorkspaceID(), Name: "old laptop", Version: "0.1.0", CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	g = do[ops.ShipGateOut](t, a, "delivery.ready", ops.ShipGateIn{ProjectID: p.ID})
	gs := do[ops.GateSettingsOut](t, a, "delivery.gate", ops.GateSettingsIn{ProjectID: p.ID})
	if g.OlderComputers != 1 || len(g.Warnings) != 1 || !strings.Contains(g.Warnings[0], "older MetaRush") || gs.OlderComputers != 1 || len(gs.Warnings) != 1 {
		t.Fatalf("older computers: %+v %+v", g.Warnings, gs.Warnings)
	}

	// 6. Only an owner, as a person, sets the one-time question aside.
	b2 := testapp.New(t)
	if err := b2.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: b2.WorkspaceID(), WorkspaceID: b2.WorkspaceID(), Name: "Old"}); err != nil {
		t.Fatal(err)
	}
	if asked, err := ops.AskAboutShipGate(ctx, b2); !asked || err != nil {
		t.Fatal(asked, err)
	}
	var todo models.Todo
	_ = b2.DB.C(store.CTodos).FindOne(ctx, bson.M{"workspaceId": b2.WorkspaceID(), "kind": models.TodoShipGate}).Decode(&todo)
	if err := doErr(b2, "todos.dismiss", ops.TodoDismissIn{ID: todo.ID}, agent); code(err) != "E_HUMAN_ONLY" {
		t.Fatalf("an agent set the question aside: %v", err)
	}
	if asked, _ := ops.AskAboutShipGate(ctx, b2); asked {
		t.Fatal("asked twice while open")
	}
	set := do[ops.TodoAnswerOut](t, b2, "todos.dismiss", ops.TodoDismissIn{ID: todo.ID})
	gs = do[ops.GateSettingsOut](t, b2, "delivery.gate", ops.GateSettingsIn{})
	if !gs.Decided || gs.On || !strings.Contains(set.Done, "only remind") {
		t.Fatalf("an owner set it aside: %+v %+v", set, gs)
	}
}
