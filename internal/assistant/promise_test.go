package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
)

// A promise made to the client becomes a "Keep this promise?" card when
// Claude reports it (D301): a person keeps it, where they choose; one MetaRush
// can't measure makes no card; Claude can't send a version out anyway (D299).
func TestPromiseKeepCards(t *testing.T) {
	a := testApp(t, "")
	project, _, _ := fixture(t, a)
	ctx := context.Background()
	if _, err := store.MoveContentSettings(ctx, a.DB, a.WorkspaceID()); err != nil {
		t.Fatal(err)
	}
	p := " --project " + project
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "promised", Steps: []fakeclaude.Step{
			denied("metarush keep promise 'Captions on every video'" + p + " --json"),
			denied("metarush project rules add " + project + " 'Under 60 seconds for shorts' --promise"),
			denied("metarush keep promise 'Warm and friendly'" + p),
			denied("metarush deliver override " + project + " --note 'they need it today'"),
			text("I asked to keep those promises."),
		}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{ProjectID: project,
		Prompt: "We promised the client captions on every video and shorts under 60 seconds."}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitCards(t, m, out.Conversation.ID, 2)
	cv = waitIdle(t, m, cv.ID)
	var promises []AssistantAction
	for _, act := range cv.Actions {
		if act.Op == "delivery.override" && act.State == ActionPending {
			t.Fatalf("Claude's override became a card that runs: %+v", act)
		}
		if act.Keeping != nil && act.Keeping.Kind == models.KeepPromise && act.State == ActionPending {
			promises = append(promises, act)
		}
	}
	if len(promises) != 2 {
		t.Fatalf("promise cards: %+v", cv.Actions)
	}
	first := promises[0]
	if first.Title != "Keep this as a promise to the client?" || !strings.Contains(first.Keeping.Does, "captions are on wherever someone speaks") ||
		!strings.Contains(first.Keeping.Does, "waits before it goes out") || first.Auto || len(first.Keeping.Where) != 2 {
		t.Fatalf("the card: %+v %+v", first, first.Keeping)
	}
	for _, s := range append([]string{first.Title, first.Keeping.Does}, first.Details...) {
		if fs := plainwords.CheckShown(s); len(fs) > 0 {
			t.Errorf("%q: %v", s, fs)
		}
	}
	// Nothing is kept until a person keeps it.
	if pr := keptProject(t, a, project); len(pr.Rules) != 0 {
		t.Fatalf("a card kept something: %+v", pr.Rules)
	}
	if done := actAsPerson(t, a, cv.ID, first.ID); done.State != ActionDone || !strings.Contains(done.Result, "Kept the promise “Captions on every video”") {
		t.Fatalf("kept: %+v", done)
	}
	if done := actWhere(t, a, cv.ID, promises[1].ID, "settings"); done.State != ActionDone {
		t.Fatalf("kept for the settings: %+v", done)
	}
	pr := keptProject(t, a, project)
	if len(pr.Rules) != 1 || !pr.Rules[0].Promise || pr.Rules[0].Measure != "captions" {
		t.Fatalf("the project's promise: %+v", pr.Rules)
	}
	rules := memoryContext(ctx, a, &pr)
	if !strings.Contains(strings.Join(rules, "\n"), "Promises made to the client") || !strings.Contains(strings.Join(rules, "\n"), "Under 60 seconds for shorts") {
		t.Fatalf("Claude's context: %v", rules)
	}
}
