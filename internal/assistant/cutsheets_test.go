package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// A cut sheet Claude writes in its proposals folder becomes an action card
// that says what changes and carries the changed lines; asking to save one
// (refused for Claude) makes the same card, never a second one. The person
// saves it on the human channel with the proposal's origin, and Claude hears
// the outcome.
func TestCutSheetProposalsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	ctx := context.Background()
	show, err := ops.CallTyped[ops.CutShowOut](ctx, a, "cut.show", ops.CutShowIn{ProjectID: projectID}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	shorter := strings.Replace(show.Sheet, "0:10 → 0:13", "0:10 → 0:12", 1)
	hook := strings.Replace(show.Sheet, "0:01 → 0:06", "0:01 → 0:05", 1)
	if shorter == show.Sheet || hook == show.Sheet {
		t.Fatalf("unexpected sheet:\n%s", show.Sheet)
	}
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "tighten", Steps: []fakeclaude.Step{
			{Write: &fakeclaude.File{Name: "shorter.cut.md", Content: shorter}},
			{Write: &fakeclaude.File{Name: "hook.cut.md", Content: hook}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": `metarush cut apply {{proposals}}/hook.cut.md -m "end the hook sooner." --json`}}},
			text("Two sheets to look at."),
		}},
		{Match: "Done from the dashboard", Steps: []fakeclaude.Step{text("Saved.")}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "tighten it", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	// Sheet cards are added as the turn wraps up, just after it goes idle.
	eventually(t, "a card for each sheet", func() bool {
		c, err := m.Get(ctx, id, true, true)
		if err != nil {
			t.Fatal(err)
		}
		cv = c
		return len(c.Actions) >= 2
	})
	if len(cv.Actions) != 2 {
		t.Fatalf("one card per sheet: %+v", cv.Actions)
	}
	cards := map[string]AssistantAction{}
	for _, c := range cv.Actions {
		cards[c.Proposal] = c
	}
	h, s := cards["t1/hook.cut.md"], cards["t1/shorter.cut.md"]
	if h.Op != "cut.apply" || h.Title != "Save the cut: end the hook sooner" || h.SheetCheck == nil || !h.SheetCheck.Valid || h.SheetCheck.ConfirmToken != "" ||
		!strings.Contains(strings.Join(h.Details, " | "), "hook now ends at 0:05") {
		t.Fatalf("card for the sheet Claude asked to save: %+v %+v", h, h.SheetCheck)
	}
	if s.Op != "cut.apply" || s.Title != "Save the cut: Shorter" || !strings.Contains(strings.Join(s.Details, " | "), "broll now ends at 0:12") {
		t.Fatalf("card for the sheet Claude only wrote: %+v", s)
	}
	changed := false
	for _, l := range s.SheetLines {
		changed = changed || l.Op != "same"
	}
	if !changed {
		t.Fatalf("the card carries the changed lines: %+v", s.SheetLines)
	}

	dash := *a
	dash.Actor.Via = "dashboard"
	in, _ := json.Marshal(ConversationActIn{ID: id, ActionID: s.ID})
	res, err := ops.Call(ctx, &dash, "assistant.act", in, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	done := res.(*ConversationActOut).Action
	if done.State != ActionDone || done.Result != "Saved as version 2" || !strings.Contains(done.Link, "tab=script") {
		t.Fatalf("saved: %+v", done)
	}
	var rev models.Revision
	if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "origin": Origin(id, "t1/shorter.cut.md")}).Decode(&rev); err != nil || rev.Number != 2 || rev.CreatedBy.Via != "dashboard" {
		t.Fatalf("the revision records where it came from: %+v %v", rev, err)
	}
	if rev.Instruction != "tighten it" {
		t.Fatalf("a sheet that names no change records what the person asked for: %q", rev.Instruction)
	}
	cv = waitIdle(t, m, id)
	if len(cv.Turns) != 2 || !cv.Turns[1].Note {
		t.Fatalf("Claude is told the outcome: %+v", cv.Turns)
	}

	// From the CLI the card is checked again for its confirm token; the other
	// sheet changed a different clip, so it goes on top of r2.
	cli := *a
	cli.Actor.Via = "cli"
	in, _ = json.Marshal(ConversationActIn{ID: id, ActionID: h.ID, Quiet: true})
	res, err = ops.Call(ctx, &cli, "assistant.act", in, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if done := res.(*ConversationActOut).Action; done.State != ActionDone || !strings.Contains(done.Result, "version 3") {
		t.Fatalf("saved from the CLI: %+v", done)
	}
	var head models.Revision
	if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "origin": Origin(id, "t1/hook.cut.md")}).Decode(&head); err != nil {
		t.Fatal(err)
	}
	// The card's title drops the message's final period; the revision keeps the message as written.
	if head.Number != 3 || head.Spec.Clips[0].Out != 5 || head.Spec.Clips[1].Out != 12 || head.Instruction != "end the hook sooner." {
		t.Fatalf("both changes are in r3: %+v", head.Spec.Clips)
	}

	// The activity log has both sheet cards on the turn that proposed them, and
	// each decision as its own entry, like any other card.
	var turn1 []logbook.Entry
	eventually(t, "the first turn in the log", func() bool {
		turn1 = logEntries(t, ctx, m, bson.M{"kind": logbook.KindClaude, "refs.conversationId": id, "claude.turn": 1})
		return len(turn1) == 1
	})
	proposed := 0
	for _, ai := range turn1[0].Claude.Actions {
		if ai.Op == "cut.apply" && ai.Decision == logbook.DecisionProposed && strings.HasPrefix(ai.Title, "Save the cut: ") {
			proposed++
		}
	}
	if proposed != 2 {
		t.Fatalf("the turn's sheet cards in the log: %+v", turn1[0].Claude.Actions)
	}
	var decided []logbook.Entry
	eventually(t, "both decisions in the log", func() bool {
		decided = logEntries(t, ctx, m, bson.M{"kind": logbook.KindAction, "refs.conversationId": id, "action.op": "cut.apply"})
		return len(decided) == 2
	})
	for _, e := range decided {
		if e.Action.Decision != logbook.DecisionApplied || !strings.Contains(e.Action.Result, "Saved as version") {
			t.Fatalf("a sheet card's decision in the log: %+v", e.Action)
		}
	}
	saves := logEntries(t, ctx, m, bson.M{"kind": logbook.KindOp, "op.name": "cut.apply"})
	if len(saves) != 2 {
		t.Fatalf("the saves themselves are logged as operations: %d", len(saves))
	}
	if text := logbook.Describe(&saves[0], logbook.LoadNames(ctx, a.DB, a.WorkspaceID(), saves), ""); !strings.HasPrefix(text, "Saved a new version of ") || !strings.Contains(text, "from a cut sheet") {
		t.Fatalf("a save reads in plain words: %q", text)
	}
}
