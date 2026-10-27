package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// House sign-offs and the house style (D199, D200) in the Claude panel: a
// "Keep this sign-off?" card asks where — just this project, or every project
// using its settings — like a rule's; kept for every project it is a house
// sign-off of the settings (each project ticks its own), and the next message
// tells Claude the house style and the sign-offs still waiting, the house's
// included.
func TestKeepSignoffAsksWhereAndClaudeHearsTheHouseStyle(t *testing.T) {
	a := testApp(t, "")
	project, _, _ := fixture(t, a)
	ctx := context.Background()
	if _, err := store.MoveContentSettings(ctx, a.DB, a.WorkspaceID()); err != nil {
		t.Fatal(err)
	}
	p := " --project " + project
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "legal", Steps: []fakeclaude.Step{
			denied("metarush keep signoff 'Legal OKs it before export'" + p + " --json"),
			text("I asked to keep that sign-off."),
		}},
		{Match: "Next", Steps: []fakeclaude.Step{text("OK.")}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{ProjectID: project, Prompt: "Our legal team has to OK every video before export."}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitCards(t, m, out.Conversation.ID, 1)
	by, _ := cardsOf(t, cv)
	cards := by["keep signoff"]
	if len(cards) != 1 {
		t.Fatalf("cards: %+v", by)
	}
	if w := cards[0].Keeping.Where; len(w) != 2 || w[0].Value != "project" || w[0].Label != "Just this project" || w[1].Value != "settings" ||
		w[1].Label != "Every project using this project's settings" {
		t.Fatalf("where a sign-off goes: %+v", w)
	}
	done := actWhere(t, a, cv.ID, cards[0].ID, "settings")
	if done.State != ActionDone || !strings.Contains(done.Result, "house sign-off “Legal OKs it before export”") {
		t.Fatalf("kept as a house sign-off: %+v", done)
	}
	if pr := keptProject(t, a, project); len(pr.Signoffs) != 0 {
		t.Fatalf("the project's own sign-offs didn't change: %+v", pr.Signoffs)
	}
	if hs := content.HouseSignoffs(ctx, a, project); len(hs) != 1 || hs[0].Text != "Legal OKs it before export" {
		t.Fatalf("the house sign-offs: %+v", hs)
	}

	// A house style; the next message carries it, and the house sign-off still waiting.
	on, never := "on", "never"
	if _, err := ops.CallTyped[ops.ContentStyleOut](ctx, a, "content.style.set", ops.ContentStyleSetIn{ProjectID: project, Captions: &on, NameBarAnimate: &never}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: cv.ID, AssistantMessageIn: AssistantMessageIn{Prompt: "Next", ProjectID: project}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, cv.ID)
	sent := cv.Turns[1].Sent
	for _, w := range []string{"House style from “", "Captions: on", "never animates", "Sign-offs still waiting", "“Legal OKs it before export”"} {
		if !strings.Contains(sent, w) {
			t.Errorf("the message should say %q:\n%s", w, sent)
		}
	}
	_ = models.KeepSignoff
}
