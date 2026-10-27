package assistant

import (
	"context"
	"time"

	"github.com/metavert-io/metarush/internal/logbook"
)

// The activity log records every conversation turn — the prompt the person
// typed, Claude's final reply in full, the tool calls with their short
// outcomes and the action cards they produced — what people did with those
// cards, and the conversation's lifecycle (launch, resume, interrupt, stop,
// budget). log.prompts decides where the prompt and reply text are kept.

// decisionOf maps an action card's state to the log's decision words.
func decisionOf(state string) string {
	switch state {
	case ActionDone:
		return logbook.DecisionApplied
	case ActionDismissed:
		return logbook.DecisionDismissed
	case ActionFailed:
		return logbook.DecisionFailed
	case ActionUnavailable:
		return logbook.DecisionUnavailable
	}
	return logbook.DecisionProposed
}

func actionInfo(a AssistantAction) logbook.ActionInfo {
	return logbook.ActionInfo{ID: a.ID, Turn: a.Turn, Kind: a.Kind, Op: a.Op, Title: a.Title, Decision: decisionOf(a.State), Result: a.Result,
		Error: firstNonEmpty(a.Error, a.Unavailable), By: a.By, Auto: a.Auto}
}

// logTurn records a finished turn. response is Claude's final reply in full
// (the turn's last prose block when the stream had no result).
func (m *Manager) logTurn(c *conv, n int, response string) {
	l := m.App.Log
	if l == nil || n <= 0 {
		return
	}
	c.mu.Lock()
	if n > len(c.data.Turns) {
		c.mu.Unlock()
		return
	}
	t := c.data.Turns[n-1]
	// The turn is its sender's (D179), whoever started the conversation.
	from := c.data.sender(n)
	title, convID, userID, userName, model := c.data.Title, c.id, from.id, firstNonEmpty(t.User, from.name), firstNonEmpty(t.Model, c.data.ModelUsed)
	var tools []logbook.ClaudeTool
	byID := map[string]int{}
	lastText := ""
	for _, e := range c.data.Events {
		if e.Turn != n {
			continue
		}
		switch e.Kind {
		case "text":
			lastText = e.Text
		case "tool":
			if e.Tool != nil {
				byID[e.Tool.ID] = len(tools)
				tools = append(tools, logbook.ClaudeTool{Name: e.Tool.Name, Summary: firstNonEmpty(e.Tool.Label, e.Tool.Summary)})
			}
		case "tool_result":
			if e.Tool != nil {
				if i, ok := byID[e.Tool.ID]; ok {
					tools[i].Result, tools[i].IsError, tools[i].Denied = e.Tool.Outcome, e.Tool.IsError, e.Tool.Denied
				}
			}
		}
	}
	var actions []logbook.ActionInfo
	for _, a := range c.data.Actions {
		if a.Turn == n {
			ai := actionInfo(a)
			// The turn records what Claude asked for; what a person did with it is
			// an entry of its own (and may already have happened by now).
			if ai.Decision != logbook.DecisionUnavailable {
				ai.Decision, ai.Result, ai.Error, ai.By = logbook.DecisionProposed, "", "", ""
			}
			actions = append(actions, ai)
		}
	}
	c.mu.Unlock()
	if response == "" {
		response = lastText
	}
	started := t.StartedAt
	dur := time.Duration(t.DurationMs) * time.Millisecond
	l.Claude(context.Background(), logbook.Turn{Source: logbook.SourceAssistant, ConversationID: convID, Turn: n, Title: title, Prompt: t.Prompt,
		Response: response, Tools: tools, Actions: actions, Model: model, Effort: t.Effort, State: t.State, Error: t.Error, CostUSD: t.CostUSD,
		Steps: t.Steps, Denials: t.Denials, StartedAt: started, Duration: dur, UserID: userID, UserName: userName, Note: t.Note,
		Refs: logbook.Refs{ProjectID: t.Context.ProjectID, AssetID: t.Context.AssetID, RevisionID: t.Context.RevisionID}})
}

// logLifecycle records a conversation lifecycle event.
func (m *Manager) logLifecycle(c *conv, action string, details map[string]any) {
	if m.App.Log == nil {
		return
	}
	m.App.Log.ConversationEvent(context.Background(), logbook.SourceAssistant, c.id, action, logbook.Refs{ProjectID: c.projectID()}, details)
}

// logAction records what a person did with an action card.
func (m *Manager) logAction(ctx context.Context, c *conv, act AssistantAction, userID, userName string) {
	if m.App.Log == nil {
		return
	}
	m.App.Log.ActionDecision(ctx, logbook.ActionOutcome{Source: logbook.SourceAssistant, ConversationID: c.id, Action: actionInfo(act),
		Refs: logbook.Refs{ProjectID: c.projectID()}, UserID: userID, UserName: userName})
}
