package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
)

// A conversation is limited in tokens, not dollars, on a Claude plan (D181):
// counted per reply as it goes and from the session's totals at the end, a
// message that reaches the limit stops there, and raising the limit needs no
// restart. Claude Code gets no dollar cap, an hour-long cache and its own
// summary point.
func TestTokenLimitStopsTheConversation(t *testing.T) {
	a := testApp(t, "")
	a.Cfg.Assistant.TokenLimitM = 0.01 // 10K tokens; each fake reply uses 4,470
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "small", Steps: []fakeclaude.Step{text("a short answer")}},
		{Match: "long job", Steps: []fakeclaude.Step{
			text("step one"), {SleepMs: 150}, text("step two"), {SleepMs: 150}, text("step three"), {SleepMs: 400}, text("step four"), text("never reached"),
		}},
	}, Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{text("fine")}}}})
	ctx := context.Background()
	if st := m.Status(ctx, true); st.Billing != "plan" || st.BudgetUSD != 0 || st.TokenLimitM != 0.01 {
		t.Fatalf("status: %+v", st)
	}
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "a small question"}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	if tk := cv.Turns[0].Tokens; tk == nil || tk.Total != 4470 || tk.CacheRead != 4000 || tk.Output != 50 || cv.Tokens.Total != 4470 || cv.LimitTokens != 10_000 {
		t.Fatalf("the first message's tokens: %+v %+v limit %d", cv.Turns[0].Tokens, cv.Tokens, cv.LimitTokens)
	}
	argv := fakeclaude.Spawns(t, m.convDir(id))[0]
	if has(argv, "--max-budget-usd") || after(argv, "--autocompact") != "300000" {
		t.Fatalf("a Claude plan has no dollar cap, and Claude Code's own summary point is set: %q", argv)
	}
	env, _ := os.ReadFile(filepath.Join(m.convDir(id), "env.txt"))
	if !strings.Contains(string(env), "CLAUDE_CODE_PROMPT_CACHE_TTL=1h") {
		t.Fatal("the prompt cache doesn't last an hour")
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "now the long job"}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, id)
	last := cv.Turns[1]
	if last.State != TurnBudget || !strings.Contains(last.Error, "limit of 10K tokens") || !strings.Contains(last.Error, "/limit") {
		t.Fatalf("stopped at the limit: %+v", last)
	}
	if cv.Tokens.Total < 10_000 || cv.Tokens.Total > 4470*3 || last.Tokens == nil || last.Tokens.Total != cv.Tokens.Total-4470 {
		t.Fatalf("counted once each, the stopped message included: %+v (message %+v)", cv.Tokens, last.Tokens)
	}
	_, err = m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "more"}})
	if code(err) != apperr.Conflict || reason(err) != "limit" {
		t.Fatalf("at the limit: %v", err)
	}
	spawned := len(fakeclaude.Spawns(t, m.convDir(id)))
	raise := 1.0
	if _, err := m.Configure(ctx, ConversationConfigureIn{ID: id, TokenLimitM: &raise}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "more"}}); err != nil {
		t.Fatalf("after raising the limit: %v", err)
	}
	cv = waitIdle(t, m, id)
	if cv.Turns[2].State != TurnSucceeded || cv.LimitTokens != 1_000_000 || len(fakeclaude.Spawns(t, m.convDir(id))) != spawned {
		t.Fatalf("raised without restarting Claude Code: %+v limit %d", cv.Turns[2], cv.LimitTokens)
	}
	// The plan's usage Claude Code reports is kept for terminal commands and restarts.
	m.setPlanUsage(&PlanUsage{SevenDay: &PlanWindow{Used: 0.85}, Status: "allowed_warning", At: timeNow()})
	other := NewTerminal(a, "/usr/local/bin/metarush-test")
	defer other.Close()
	if p := other.PlanUsage(); p == nil || p.SevenDay == nil || p.SevenDay.Used != 0.85 {
		t.Fatalf("another manager reads the plan's usage: %+v", p)
	}
	if bad := 0.5; ValidTokenLimit(bad) == nil {
		t.Fatal("a limit under a million tokens is refused")
	}
}

// A conversation whose memory passes the setting is summarized right after a
// reply (D181): MetaRush sends Claude Code's /compact with what to keep, the
// memory and the count follow Claude Code's report, the summary's tokens are
// counted, and it isn't summarized again until it grows.
func TestLongConversationsAreSummarized(t *testing.T) {
	a := testApp(t, "")
	a.Cfg.Assistant.CompactAtTokens = 5000
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Context: 6000, Steps: []fakeclaude.Step{text("done")}}}})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "a big job"}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	var cv *Conversation
	eventually(t, "the summary", func() bool {
		cv, _ = m.Get(ctx, id, true, false)
		return cv.State == ConvIdle && len(cv.Turns) == 2
	})
	sum := cv.Turns[1]
	if !sum.Compact || !sum.Note || sum.State != TurnSucceeded || cv.Compactions != 1 || cv.CompactedTo != 1500 || cv.ContextTokens != 1500 {
		t.Fatalf("summarized: %+v compactions %d to %d context %d", sum, cv.Compactions, cv.CompactedTo, cv.ContextTokens)
	}
	if sum.Tokens == nil || sum.Tokens.CacheRead != 6000 || sum.Tokens.Output != 500 {
		t.Fatalf("the summary's tokens are counted: %+v", sum.Tokens)
	}
	if got := fakeclaude.Received(t, m.convDir(id)); !strings.Contains(strings.Join(got, "\n"), "/compact This is a video-editing conversation") {
		t.Fatalf("Claude Code got /compact with what to keep: %q", got)
	}
	notice := false
	for _, e := range cv.Events {
		if e.Kind == "notice" && strings.Contains(e.Text, "from 6K to 2K tokens") {
			notice = true
		}
	}
	if !notice {
		t.Fatalf("the person reads that it was summarized: %+v", cv.Events)
	}
	// The next reply (memory 6,000 again) is under 1,500 + half the setting: no summary yet.
	m.App.Cfg.Assistant.CompactAtTokens = 9000
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "again"}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, id)
	if len(cv.Turns) != 3 || cv.Turns[2].Compact {
		t.Fatalf("summarized again too soon: %+v", cv.Turns)
	}
}

// With holdNews (D181) a person's decision on a card waits for their next
// message instead of starting one of its own; it isn't lost when that
// message can't go, and safe steps and failures still go at once.
func TestNewsWaitsForTheNextMessage(t *testing.T) {
	a := testApp(t, "")
	on := true
	a.Cfg.Assistant.HoldNews = &on
	projectID, _, head := fixture(t, a)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "approve it", Steps: []fakeclaude.Step{
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush revision approve " + head}}},
			text("Approving is up to you."),
		}},
	}, Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{text("ok")}}}})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "approve it", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	if len(cv.Actions) != 1 {
		t.Fatalf("cards: %+v", cv.Actions)
	}
	if _, err := actIn(t, m, ConversationActIn{ID: id, ActionID: cv.Actions[0].ID, Dismiss: true}); err != nil {
		t.Fatal(err)
	}
	m.tellClaude(m.convs[id]) // what the timer would do
	cv = waitIdle(t, m, id)
	if len(cv.Turns) != 1 {
		t.Fatalf("a decision started a message of its own: %+v", cv.Turns)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "what next?"}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, id)
	if len(cv.Turns) != 2 || cv.Turns[1].Note || !strings.Contains(cv.Turns[1].Sent, "Not now: ") {
		t.Fatalf("the next message takes the news along: %+v", cv.Turns)
	}
	// Which outcomes go at once.
	c := newConv(Conversation{Actions: []AssistantAction{
		{ID: "act_a", Title: "Set aside", State: ActionDismissed, Batch: "b1"},
		{ID: "act_b", Title: "Check the graphic", State: ActionDone, Result: "It passed", Batch: "b2", Auto: true},
	}})
	c.mu.Lock()
	lines, _, _, told := c.takeReadyLocked(true)
	c.mu.Unlock()
	if len(lines) != 2 || len(told) != 2 {
		t.Fatalf("a safe step that ran by itself goes at once, with the rest: %q", lines)
	}
	c.mu.Lock()
	c.untellLocked(told)
	c.data.Actions[1].Auto = false
	lines, _, _, _ = c.takeReadyLocked(true)
	c.mu.Unlock()
	if len(lines) != 0 || untoldCount(c.data.Actions) != 2 {
		t.Fatalf("decisions alone wait, and untold news stays untold: %q", lines)
	}
}
