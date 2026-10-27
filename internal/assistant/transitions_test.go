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

// `metarush cut transition` is refused for Claude and becomes a card that was
// checked when it was made (the change in plain words, its warnings); the
// person's click puts it in the cut.
func TestCutTransitionBecomesACard(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	ctx := context.Background()
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		denied("metarush cut transition " + projectID + " broll dissolve 0.6 --json"),
		denied("metarush cut transition " + projectID + " 2 slide --direction left --easing 'slows down' --auto --json"),
		denied("metarush cut transition " + projectID + " nope dissolve --json"),
		text("Asked.")}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "use a dissolve into the b-roll", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	if len(cv.Actions) != 2 {
		t.Fatalf("expected two cards (an unknown clip is no card), got %+v", cv.Actions)
	}
	card := cv.Actions[0]
	if card.Op != "cut.transition" || card.State != ActionPending || !strings.HasPrefix(card.Title, "Change the transition into ") || card.Check == nil || !card.Check.Valid ||
		!strings.Contains(strings.Join(card.Details, "|"), "now comes in with a dissolve 0.6 s (was a straight cut)") || card.Check.ConfirmToken != "" {
		t.Fatalf("dissolve card: %+v (check %+v)", card, card.Check)
	}
	var in ops.CutTransitionIn
	if err := json.Unmarshal([]byte(card.Input), &in); err != nil || in.Clip != "broll" || in.Type != "dissolve" || in.Duration == nil || *in.Duration != 0.6 || in.DryRun {
		t.Fatalf("card input %s (%v)", card.Input, err)
	}
	slide := cv.Actions[1]
	if !strings.Contains(strings.Join(slide.Details, "|"), "a slide left 0.4 s, slows down, sound changes in a pause") {
		t.Fatalf("slide card: %+v", slide.Details)
	}
	for _, act := range cv.Actions {
		for _, s := range append([]string{act.Title}, act.Details...) {
			for _, bad := range []string{"framing.", "rev_", "ast_", "--", "setTransition"} {
				if strings.Contains(s, bad) {
					t.Fatalf("card text %q has %q", s, bad)
				}
			}
		}
	}
	dash := *a
	dash.Actor.Via = "dashboard"
	b, _ := json.Marshal(ConversationActIn{ID: cv.ID, ActionID: card.ID, Quiet: true})
	res, err := ops.Call(ctx, &dash, "assistant.act", b, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	if done := res.(*ConversationActOut).Action; done.State != ActionDone || !strings.Contains(done.Result, "Saved as version 2") || !strings.HasPrefix(done.Link, "/p/"+projectID+"/r/rev_") {
		t.Fatalf("card ran: %+v", done)
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
		t.Fatal(err)
	}
	var head models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), p.Branches["main"].Head, &head); err != nil {
		t.Fatal(err)
	}
	if tr := head.Spec.Clips[1].Transition; tr == nil || tr.Type != "crossfade" || tr.Duration != 0.6 || head.CreatedBy.Via != "dashboard" {
		t.Fatalf("the cut after the card: %+v by %+v", tr, head.CreatedBy)
	}
	// The label in the conversation says what Claude asked for.
	p2 := newParser("/ws", "/as/conversations/asc_01jabcdefghjkmnpqrstvwxyz0", "/scratch", "/as", func(string) string { return "" })
	words, _ := splitCommand("metarush cut transition prj_x 2 dissolve --json")
	call, _ := parseMetarush(words)
	if got := p2.bashLabel(&call, "metarush cut transition prj_x 2 dissolve --json"); got != "Asked to change a transition" {
		t.Fatalf("label %q", got)
	}
	if readOnlyCall(call) {
		t.Fatal("cut transition changes the cut: never a read-only command")
	}
}
