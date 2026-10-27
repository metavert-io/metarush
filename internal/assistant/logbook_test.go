package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

func logEntries(t *testing.T, ctx context.Context, m *Manager, filter bson.M) []logbook.Entry {
	t.Helper()
	m.App.Log.Flush(ctx)
	filter["workspaceId"] = m.App.WorkspaceID()
	var out []logbook.Entry
	cur, err := m.App.DB.C(store.CLogEntries).Find(ctx, filter)
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.All(ctx, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every turn of a dashboard conversation is in the activity log: the prompt,
// the full final reply, tool calls with their outcomes and the action cards,
// then what a person did with a card, and the conversation's lifecycle.
func TestConversationTurnsAreLogged(t *testing.T) {
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	long := strings.Repeat("A long and careful answer. ", 400) // > 4 KB: stored gzipped in the media store
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "render it", Steps: []fakeclaude.Step{
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Input: map[string]any{"command": "metarush project list --json"}, Result: `{"ok":true,"data":{"projects":[]}}`}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush render submit --revision " + head + " --quality review --json"}}},
			text("I asked for a review render."),
		}, Result: "I asked for a review render."},
		{Match: "explain", Steps: []fakeclaude.Step{text(long)}, Result: long},
	}}})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "render it please", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	if len(cv.Actions) != 1 {
		t.Fatalf("actions: %+v", cv.Actions)
	}
	dash := *a
	dash.Actor.Via = "dashboard"
	in, _ := json.Marshal(ConversationActIn{ID: id, ActionID: cv.Actions[0].ID, Quiet: true})
	if _, err := ops.Call(ctx, &dash, "assistant.act", in, ops.Caller{Human: true, Remote: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "explain what you did"}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, id)

	var turns []logbook.Entry
	eventually(t, "two turn entries", func() bool { // the entry is written just after the turn goes idle
		turns = logEntries(t, ctx, m, bson.M{"kind": logbook.KindClaude, "refs.conversationId": id})
		return len(turns) == 2
	})
	var first, second logbook.Entry
	for _, e := range turns {
		if e.Claude.Turn == 1 {
			first = e
		} else {
			second = e
		}
	}
	c := first.Claude
	if first.Channel != logbook.ChannelAssistant || c.Source != logbook.SourceAssistant || c.Prompt != "render it please" ||
		c.Response != "I asked for a review render." || c.Privacy != logbook.PrivacyWorkspace || first.Refs.ProjectID != projectID {
		t.Fatalf("first turn: %+v %+v", first, c)
	}
	if len(c.Tools) != 2 || c.Tools[0].Name != "Bash" || c.Tools[0].Summary == "" || c.Tools[0].Result == "" || !c.Tools[1].Denied {
		t.Fatalf("tools: %+v", c.Tools)
	}
	if len(c.Actions) != 1 || c.Actions[0].Decision != logbook.DecisionProposed || c.Actions[0].Op != "render.submit" {
		t.Fatalf("action cards of the turn: %+v", c.Actions)
	}
	// The long reply went to the media store, gzipped; reading it back gives the full text.
	if second.Claude.Response != "" || second.Claude.ResponseRef == nil || second.Claude.ResponseRef.Store != logbook.StoreWorkspace {
		t.Fatalf("long reply: %+v", second.Claude)
	}
	body, err := a.Log.ReadBody(ctx, second.Claude.ResponseRef)
	if err != nil || body != strings.TrimSpace(long) {
		t.Fatalf("read body: %v (%d chars)", err, len(body))
	}
	decisions := logEntries(t, ctx, m, bson.M{"kind": logbook.KindAction, "refs.conversationId": id})
	if len(decisions) != 1 || decisions[0].Action.Decision != logbook.DecisionApplied || decisions[0].Action.Op != "render.submit" {
		t.Fatalf("decision: %+v", decisions)
	}
	launches := logEntries(t, ctx, m, bson.M{"kind": logbook.KindEvent, "event.action": "conversation.launch", "refs.conversationId": id})
	if len(launches) != 1 {
		t.Fatalf("launch events: %+v", launches)
	}
	// The op that started the conversation is logged as a dashboard-channel change.
	asks := logEntries(t, ctx, m, bson.M{"kind": logbook.KindOp, "op.name": "assistant.act"})
	if len(asks) != 1 || !asks[0].Op.Mutating || !asks[0].Op.Human {
		t.Fatalf("assistant.act op entry: %+v", asks)
	}
}

// log.prompts: local keeps the words in this machine's home; off keeps none.
func TestConversationPrivacyModes(t *testing.T) {
	for _, mode := range []string{config.PromptsLocal, config.PromptsOff} {
		t.Run(mode, func(t *testing.T) {
			a := testApp(t, "")
			a.Cfg.Log.Prompts = mode
			a.Log.SetSettings(a.Cfg.Log)
			projectID, _, _ := fixture(t, a)
			m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{text("secret plans")}, Result: "secret plans"}}})
			ctx := context.Background()
			dash := *a
			dash.Actor.Via = "dashboard"
			in, _ := json.Marshal(AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "my private prompt", ProjectID: projectID}})
			res, err := ops.Call(ctx, &dash, "assistant.ask", in, ops.Caller{Human: true, Remote: true})
			if err != nil {
				t.Fatal(err)
			}
			waitIdle(t, m, res.(*ConversationSendOut).Conversation.ID)
			var turns []logbook.Entry
			eventually(t, "the turn entry", func() bool {
				turns = logEntries(t, ctx, m, bson.M{"kind": logbook.KindClaude})
				return len(turns) == 1
			})
			e := turns[0]
			if e.Claude.Prompt != "" || e.Claude.Response != "" || strings.Contains(e.Summary, "private") || e.Claude.Privacy != mode {
				t.Fatalf("text in the database under %s: %+v", mode, e)
			}
			if e.Claude.PromptChars != len("my private prompt") {
				t.Fatalf("prompt length not recorded: %+v", e.Claude)
			}
			// The ask op's input does not carry the prompt either.
			ops := logEntries(t, ctx, m, bson.M{"op.name": "assistant.ask"})
			if len(ops) == 0 {
				t.Fatal("no assistant.ask entry")
			}
			if b, _ := json.Marshal(ops[0].Op.Input); strings.Contains(string(b), "private prompt") {
				t.Fatalf("prompt in the op input under %s: %s", mode, b)
			}
			switch mode {
			case config.PromptsLocal:
				ref := e.Claude.PromptRef
				if ref == nil || ref.Store != logbook.StoreLocal || ref.InstallationID != a.Cfg.Installation.ID {
					t.Fatalf("local prompt ref: %+v", ref)
				}
				if _, err := os.Stat(filepath.Join(a.Cfg.LogBodiesDir(), filepath.FromSlash(ref.Key))); err != nil {
					t.Fatalf("local body file: %v", err)
				}
				if body, err := a.Log.ReadBody(ctx, ref); err != nil || body != "my private prompt" {
					t.Fatalf("read local body: %q %v", body, err)
				}
			case config.PromptsOff:
				if e.Claude.PromptRef != nil || e.Claude.ResponseRef != nil {
					t.Fatalf("bodies kept with prompts off: %+v", e.Claude)
				}
			}
		})
	}
}
