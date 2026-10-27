package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// The cut's editing verbs are refused for Claude and become cards checked when
// they are made; a click saves them (in the dashboard directly, elsewhere with
// the confirm token the card asks for).
func TestCutEditVerbsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a) // hook 1–6, broll 10–13 of one 30 s recording
	ctx := context.Background()
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		denied("metarush cut trim " + projectID + " hook --end -0.5 --json"),
		denied("metarush cut move " + projectID + " broll start --json"),
		denied("metarush cut split " + projectID + " 0:02 --json"),
		denied("metarush cut trim " + projectID + " nope --end -1 --json"),
		text("Asked.")}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "tighten the hook and open on the b-roll", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	if len(cv.Actions) != 3 {
		t.Fatalf("expected three cards (an unknown clip is no card), got %+v", cv.Actions)
	}
	trim, move, split := cv.Actions[0], cv.Actions[1], cv.Actions[2]
	if trim.Op != "cut.trim" || trim.Title != "Trim a clip" || trim.Check == nil || !trim.Check.Valid || trim.Check.ConfirmToken != "" ||
		!strings.Contains(strings.Join(trim.Details, "|"), "now ends 0.5 s earlier") {
		t.Fatalf("trim card: %+v", trim)
	}
	if move.Op != "cut.move" || split.Op != "cut.split" || !strings.Contains(split.Details[0], "Splits") {
		t.Fatalf("move/split cards: %+v / %+v", move, split)
	}
	var in map[string]any
	if err := json.Unmarshal([]byte(trim.Input), &in); err != nil || in["clip"] != "hook" || in["end"] != "-0.5" || in["dryRun"] != nil {
		t.Fatalf("trim input %s (%v)", trim.Input, err)
	}
	for _, act := range cv.Actions {
		for _, s := range append([]string{act.Title}, act.Details...) {
			for _, bad := range []string{"rev_", "ast_", "--", "trimClip", "moveClip"} {
				if strings.Contains(s, bad) {
					t.Fatalf("card text %q has %q", s, bad)
				}
			}
		}
	}
	head := func() *models.Revision {
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
			t.Fatal(err)
		}
		var r models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), p.Branches["main"].Head, &r); err != nil {
			t.Fatal(err)
		}
		return &r
	}
	// In the dashboard: one click saves it.
	dash := *a
	dash.Actor.Via = "dashboard"
	b, _ := json.Marshal(ConversationActIn{ID: cv.ID, ActionID: trim.ID, Quiet: true})
	res, err := ops.Call(ctx, &dash, "assistant.act", b, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	if done := res.(*ConversationActOut).Action; done.State != ActionDone || !strings.Contains(done.Result, "Saved as version 2") {
		t.Fatalf("trim card ran: %+v", done)
	}
	if h := head(); h.Spec.Clips[0].Out != 5.5 || !strings.HasPrefix(h.Origin, "cut-trim:studio") {
		t.Fatalf("after the trim card: %+v (%s)", h.Spec.Clips[0], h.Origin)
	}
	// At a terminal (a person, not the dashboard): the card checks and uses the token.
	b, _ = json.Marshal(ConversationActIn{ID: cv.ID, ActionID: move.ID, Quiet: true})
	res, err = ops.Call(ctx, a, "assistant.act", b, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if done := res.(*ConversationActOut).Action; done.State != ActionDone {
		t.Fatalf("move card ran: %+v", done)
	}
	if h := head(); h.Spec.Clips[0].ID != "broll" {
		t.Fatalf("after the move card: %v", h.Spec.Clips)
	}
	// The label in the conversation says what Claude asked for; never a read-only command.
	p2 := newParser("/ws", "/as/conversations/asc_01jabcdefghjkmnpqrstvwxyz0", "/scratch", "/as", func(string) string { return "" })
	for cmd, want := range map[string]string{
		"metarush cut trim prj_x hook --end -0.5 --json":    "Asked to trim a clip",
		"metarush cut remove-words prj_x \"um\" --all":      "Asked to take words out",
		"metarush cut delete-range prj_x 0:01 0:02 --json":  "Asked to cut out part of the video",
		"metarush cut add prj_x interview 0:01 0:03 --json": "Asked to add footage to the cut",
	} {
		words, _ := splitCommand(cmd)
		call, _ := parseMetarush(words)
		if got := p2.bashLabel(&call, cmd); got != want {
			t.Errorf("label for %q: %q", cmd, got)
		}
		if readOnlyCall(call) {
			t.Errorf("%q changes the cut: never a read-only command", cmd)
		}
	}
}
