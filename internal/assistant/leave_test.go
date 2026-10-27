package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/todos"
)

// keepWords makes a project whose Content Settings keep its words from
// Claude (What may leave, D290), and one with no rules.
func keepWords(t *testing.T, ctx context.Context, m *Manager) (kept, plain string) {
	t.Helper()
	t.Setenv("CLAUDECODE", "")
	a := m.App
	person := ops.Caller{Human: true}
	mk := func(name string) string {
		out, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: name}, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		return out.Project.ID
	}
	plain = mk("Base")
	kept = mk("Acme")
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, a, "content.own", ops.ContentOwnIn{ProjectID: kept}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[leave.LeaveOut](ctx, a, "content.leave.set", leave.LeaveSetIn{ProjectID: kept, Keep: map[string][]string{"claude": {"words"}}}, person); err != nil {
		t.Fatal(err)
	}
	return kept, plain
}

// The project so far that MetaRush writes into Claude's first message
// leaves out a client's to-do questions when the client keeps its words.
func TestSoFarLeavesOutAKeptClientsWords(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{})
	ctx := context.Background()
	kept, plain := keepWords(t, ctx, m)
	for _, p := range []string{kept, plain} {
		if _, err := todos.Upsert(ctx, a, models.Todo{Kind: models.TodoSpelling, Scope: "project", ProjectID: p, ProjectIDs: []string{p}, GroupKey: "spelling:" + p,
			Question: "You fixed “zorblaxian” 3 times. Add it to the spelling list?", Priority: 3}); err != nil {
			t.Fatal(err)
		}
	}
	out, err := soFarForClaude(ctx, a, kept, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.Text, "zorblaxian") || len(out.Todos) != 0 {
		t.Fatalf("a kept client's words in Claude's first message: %s", out.Text)
	}
	if out, _ := soFarForClaude(ctx, a, plain, "", ""); !strings.Contains(out.Text, "zorblaxian") {
		t.Fatalf("a project without rules keeps its questions: %s", out.Text)
	}
}

// A conversation already running starts Claude Code again once a client's
// rules close the shared folders, so its read rules follow.
func TestRulesChangeRestartsClaude(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{text("ok")}}}})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "first"}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	waitIdle(t, m, id)
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "second"}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, id)
	if n := len(fakeclaude.Spawns(t, m.convDir(id))); n != 1 {
		t.Fatalf("one process while nothing changed: %d", n)
	}
	keepWords(t, ctx, m)
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "third"}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, id)
	spawns := fakeclaude.Spawns(t, m.convDir(id))
	if len(spawns) != 2 {
		t.Fatalf("the rules changed: started again? %d", len(spawns))
	}
	if args := strings.Join(spawns[1], " "); !strings.Contains(args, "Read(/"+realPath(a.Cfg.Scratch.Dir)+"/**)") {
		t.Fatalf("the shared folders are denied: %s", args)
	}
}
