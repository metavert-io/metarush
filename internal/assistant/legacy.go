package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/store"
)

// legacyRun is the run.json format of the single-request assistant (before
// conversations). Each one is imported once as a read-only, one-turn conversation.
type legacyRun struct {
	ID            string        `json:"id"`
	State         string        `json:"state"`
	Prompt        string        `json:"prompt"`
	Context       TurnContext   `json:"context"`
	User          string        `json:"user"`
	Model         string        `json:"model"`
	ClaudeVersion string        `json:"claudeVersion"`
	BudgetUSD     float64       `json:"budgetUsd"`
	CreatedAt     time.Time     `json:"createdAt"`
	EndedAt       *time.Time    `json:"endedAt"`
	Result        *legacyResult `json:"result"`
	Error         string        `json:"error"`
	Proposals     int           `json:"proposals"`
	Events        []legacyEvent `json:"events"`
}

type legacyResult struct {
	Subtype    string  `json:"subtype"`
	IsError    bool    `json:"isError"`
	Text       string  `json:"text"`
	CostUSD    float64 `json:"costUsd"`
	DurationMs int64   `json:"durationMs"`
	Turns      int     `json:"turns"`
	Denials    int     `json:"denials"`
}

type legacyEvent struct {
	At     time.Time     `json:"at"`
	Kind   string        `json:"kind"`
	State  string        `json:"state"`
	Text   string        `json:"text"`
	Tool   *ToolCall     `json:"tool"`
	Result *legacyResult `json:"result"`
	Count  int           `json:"count"`
}

func legacyTurnState(s string) string {
	switch s {
	case "succeeded":
		return TurnSucceeded
	case "failed":
		return TurnFailed
	case "canceled":
		return TurnStopped
	case "timed_out":
		return TurnTimedOut
	}
	return TurnLost
}

// importLegacy turns this machine's runs/<asr_…>/run.json into conversations.
// It only inserts (never overwrites a conversation someone renamed or archived).
func (m *Manager) importLegacy(ctx context.Context) {
	entries, err := os.ReadDir(filepath.Join(m.dir(), "runs"))
	if err != nil {
		return
	}
	me, machine := m.machine()
	for _, e := range entries {
		if !e.IsDir() || !ids.Valid(RunPrefix, e.Name()) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.legacyDir(e.Name()), "run.json"))
		if err != nil {
			continue
		}
		var r legacyRun
		if json.Unmarshal(b, &r) != nil || r.ID != e.Name() {
			continue
		}
		d := legacyConversation(r, m.App.WorkspaceID(), m.App.Cfg.User.ID, me, machine)
		ctx2, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, _ = m.App.DB.C(store.CAssistantConversations).UpdateOne(ctx2, bson.M{"_id": d.ID, "workspaceId": d.WorkspaceID},
			bson.M{"$setOnInsert": d}, options.UpdateOne().SetUpsert(true))
		cancel()
	}
}

func legacyConversation(r legacyRun, workspaceID, userID, installationID, machine string) Conversation {
	state := legacyTurnState(r.State)
	t := Turn{N: 1, Prompt: r.Prompt, Context: r.Context, User: r.User, State: state, StartedAt: r.CreatedAt, EndedAt: r.EndedAt,
		Error: r.Error, Proposals: r.Proposals, Model: r.Model, Folder: "runs/" + r.ID + "/proposals"}
	if r.Result != nil {
		t.CostUSD, t.DurationMs, t.Steps, t.Denials = r.Result.CostUSD, r.Result.DurationMs, r.Result.Turns, r.Result.Denials
	}
	updated := r.CreatedAt
	if r.EndedAt != nil {
		updated = *r.EndedAt
	}
	ctx := r.Context
	d := Conversation{ID: r.ID, WorkspaceID: workspaceID, Title: deriveTitle(r.Prompt), TitleSource: "prompt", UserID: userID, UserName: r.User,
		InstallationID: installationID, InstallationName: machine, ProjectID: r.Context.ProjectID, ProjectName: r.Context.ProjectName,
		State: ConvIdle, Legacy: true, BudgetUSD: r.BudgetUSD, CostUSD: t.CostUSD, ModelUsed: r.Model, ClaudeVersion: r.ClaudeVersion,
		TurnCount: 1, FirstPrompt: clip(r.Prompt, 500), LastPrompt: clip(r.Prompt, 500), ProposalCount: r.Proposals,
		Turns: []Turn{t}, Queue: []QueuedMessage{}, Actions: []AssistantAction{}, CreatedAt: r.CreatedAt, UpdatedAt: updated}
	seq := 0
	add := func(e AssistantEvent) {
		seq++
		e.Seq, e.Turn = seq, 1
		d.Events = append(d.Events, e)
	}
	add(AssistantEvent{At: r.CreatedAt, Kind: "turn", Text: r.Prompt, User: r.User, Context: &ctx})
	var res *legacyResult
	for _, e := range r.Events {
		switch e.Kind {
		case "text":
			add(AssistantEvent{At: e.At, Kind: "text", Text: e.Text})
		case "tool":
			if e.Tool != nil {
				tc := *e.Tool
				if tc.Label == "" {
					tc.Label = tc.Name
				}
				add(AssistantEvent{At: e.At, Kind: "tool", Tool: &tc})
			}
		case "tool_result":
			if e.Tool != nil {
				tc := *e.Tool
				add(AssistantEvent{At: e.At, Kind: "tool_result", Tool: &tc})
			}
		case "proposals":
			add(AssistantEvent{At: e.At, Kind: "proposals", Count: e.Count})
		case "error":
			add(AssistantEvent{At: e.At, Kind: "error", Text: e.Text})
		case "result":
			if e.Result != nil {
				res = e.Result
			}
		}
	}
	end := AssistantEvent{At: updated, Kind: "turn_end", State: state}
	if res != nil {
		end.Result = &TurnResult{Subtype: res.Subtype, IsError: res.IsError, CostUSD: res.CostUSD, TotalCostUSD: res.CostUSD,
			DurationMs: res.DurationMs, Steps: res.Turns, Denials: res.Denials}
	}
	add(end)
	return d
}
