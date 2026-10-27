package assistant

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
)

// Tokens, not dollars (D181). A conversation is limited by the tokens Claude
// Code reports for it: new input, writing and reading the prompt cache, and
// output (its thinking included), summaries too. On a Claude plan nothing is
// billed per token, so the plan's own usage (PlanUsage) is what matters; with
// an API key the dollar cap stays as well.

// TokenUsage counts tokens by kind.
type TokenUsage struct {
	Input      int64 `bson:"input" json:"input" jsonschema:"description=New input tokens (not from the prompt cache)."`
	CacheWrite int64 `bson:"cacheWrite" json:"cacheWrite" jsonschema:"description=Tokens written to the prompt cache."`
	CacheRead  int64 `bson:"cacheRead" json:"cacheRead" jsonschema:"description=Tokens read from the prompt cache: every step reads the conversation again."`
	Output     int64 `bson:"output" json:"output" jsonschema:"description=Tokens Claude wrote\\, its thinking included."`
	Total      int64 `bson:"total" json:"total" jsonschema:"description=All of them: what the conversation's limit counts."`
}

func (u TokenUsage) sum() int64 { return u.Input + u.CacheWrite + u.CacheRead + u.Output }

func (u TokenUsage) norm() TokenUsage {
	u.Total = u.sum()
	return u
}

func (u TokenUsage) plus(o TokenUsage) TokenUsage {
	return TokenUsage{Input: u.Input + o.Input, CacheWrite: u.CacheWrite + o.CacheWrite, CacheRead: u.CacheRead + o.CacheRead, Output: u.Output + o.Output}.norm()
}

func (u TokenUsage) minus(o TokenUsage) TokenUsage {
	return TokenUsage{Input: u.Input - o.Input, CacheWrite: u.CacheWrite - o.CacheWrite, CacheRead: u.CacheRead - o.CacheRead, Output: u.Output - o.Output}.norm()
}

// covers reports whether u has at least o of every kind.
func (u TokenUsage) covers(o TokenUsage) bool {
	return u.Input >= o.Input && u.CacheWrite >= o.CacheWrite && u.CacheRead >= o.CacheRead && u.Output >= o.Output
}

// countSince is what a session total adds to the one seen before (D181).
// Claude Code reports each session's running total by model, across resumes;
// one that starts again lower (a new count) is counted from zero. A
// conversation whose tokens weren't counted yet (from before D181) counts from
// this message: fallback is the message's own usage.
func countSince(seen, now, fallback TokenUsage, counting bool) TokenUsage {
	switch {
	case !counting:
		return fallback.norm()
	case now.covers(seen):
		return now.minus(seen)
	}
	return now.norm()
}

// Millions writes a token count for people: "62.5M", "480K", "900".
func Millions(n int64) string {
	switch {
	case n >= 10_000_000:
		return fmt.Sprintf("%.0fM", float64(n)/1e6)
	case n >= 1_000_000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1e6), ".0") + "M"
	case n >= 1_000:
		return fmt.Sprintf("%.0fK", float64(n)/1e3)
	}
	return fmt.Sprintf("%d", n)
}

// PlanUsage is the Claude plan's own usage as Claude Code last reported it
// (its rate_limit_event): how much of each window is used and when it starts
// again. Only a plan sign-in reports it.
type PlanUsage struct {
	FiveHour *PlanWindow `json:"fiveHour,omitempty" jsonschema:"description=The plan's five-hour window."`
	SevenDay *PlanWindow `json:"sevenDay,omitempty" jsonschema:"description=The plan's weekly window."`
	Status   string      `json:"status,omitempty" jsonschema:"description=allowed\\, allowed_warning (getting close) or rejected (used up until it resets)."`
	At       time.Time   `json:"at" jsonschema:"description=When Claude Code said so."`
}

// PlanWindow is one of the plan's usage windows.
type PlanWindow struct {
	Used     float64   `json:"used" jsonschema:"description=Share used\\, 0–1."`
	ResetsAt time.Time `json:"resetsAt"`
}

// planUsageOf reads a rate_limit_event's rate_limit_info.
func planUsageOf(r *rateLimitInfo, at time.Time) *PlanUsage {
	if r == nil {
		return nil
	}
	pu := &PlanUsage{Status: r.Status, At: at}
	win := func(w *rateLimitWindow) *PlanWindow {
		if w == nil {
			return nil
		}
		return &PlanWindow{Used: w.Utilization, ResetsAt: time.Unix(w.ResetsAt, 0).UTC()}
	}
	if r.Windows != nil {
		pu.FiveHour, pu.SevenDay = win(r.Windows.FiveHour), win(r.Windows.SevenDay)
	}
	if pu.FiveHour == nil && pu.SevenDay == nil && r.Type != "" {
		w := &PlanWindow{Used: r.Utilization, ResetsAt: time.Unix(r.ResetsAt, 0).UTC()}
		switch r.Type {
		case "five_hour":
			pu.FiveHour = w
		case "seven_day":
			pu.SevenDay = w
		}
	}
	return pu
}

// compactInfo is Claude Code's report of a summary (system/compact_boundary).
type compactInfo struct {
	Trigger string `json:"trigger"`
	Pre     int    `json:"pre_tokens"`
	Post    int    `json:"post_tokens"`
}

// rateLimitInfo is a rate_limit_event's payload.
type rateLimitInfo struct {
	Status      string  `json:"status"`
	Type        string  `json:"rateLimitType"`
	Utilization float64 `json:"utilization"`
	ResetsAt    int64   `json:"resetsAt"`
	Windows     *struct {
		FiveHour *rateLimitWindow `json:"five_hour"`
		SevenDay *rateLimitWindow `json:"seven_day"`
	} `json:"unifiedWindows"`
}

type rateLimitWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    int64   `json:"resetsAt"`
}

// ValidTokenLimit checks a per-conversation limit in millions of tokens.
func ValidTokenLimit(m float64) error {
	if m < 1 || m > 1000 {
		return apperr.New(apperr.InvalidInput, "A conversation's limit is between 1 and 1,000 million tokens (you gave %g).", m)
	}
	return nil
}

// limitTokens is the limit that applies to a conversation, in tokens.
func (m *Manager) limitTokens(d *Conversation) int64 {
	lim := d.TokenLimitM
	if lim <= 0 {
		lim = m.App.Cfg.Assistant.TokenLimit()
	}
	return int64(lim * 1e6)
}

// billing is the latest probe's billing mode ("" before the first probe).
func (m *Manager) billing() string {
	b, _ := m.billingMode.Load().(string)
	return b
}

// dollarCap is a conversation's dollar cap: only with an API-key sign-in,
// where tokens are real spend (0: none).
func (m *Manager) dollarCap(d *Conversation) float64 {
	if m.billing() != "api" || d.BudgetUSD <= 0 {
		return 0
	}
	return d.BudgetUSD
}

// overLimitLocked says which limit a conversation reached with extra tokens
// on top of what it used (caller holds c.mu): "tokens", "budget" (an API
// key's dollars) or "" (none).
func (m *Manager) overLimitLocked(c *conv, extra TokenUsage) string {
	d := &c.data
	if lim := m.limitTokens(d); lim > 0 && d.Tokens.Total+extra.sum() >= lim {
		return "tokens"
	}
	if cap := m.dollarCap(d); cap > 0 && d.CostUSD >= cap-0.005 {
		return "budget"
	}
	return ""
}

// limitText says a conversation reached its limit and how to go on.
func (m *Manager) limitText(d *Conversation, which string) string {
	if which == "budget" {
		return fmt.Sprintf("This conversation reached its spending limit ($%.2f of $%.2f). To keep going, type /budget and a higher amount.", d.CostUSD, d.BudgetUSD)
	}
	return fmt.Sprintf("This conversation reached its limit of %s tokens (it used %s). To keep going, type /limit and a higher number of millions, or start a new conversation: it begins with a small memory, so each message uses far fewer tokens.",
		Millions(m.limitTokens(d)), Millions(d.Tokens.Total))
}

// limitError is the error for a message to a conversation at its limit.
func (m *Manager) limitError(d *Conversation, which string) error {
	if which == "budget" {
		return budgetError(d)
	}
	lim := m.limitTokens(d)
	return apperr.New(apperr.Conflict, "This conversation reached its limit of %s tokens (it used %s).", Millions(lim), Millions(d.Tokens.Total)).
		WithHint("To keep going, type /limit and a higher number of millions in the message box (for example /limit 80), or start a new conversation.").
		WithDetails(map[string]any{"reason": "limit", "tokens": d.Tokens.Total, "limitTokens": lim}).
		WithCommand("metarush assistant configure " + d.ID + " --limit <millions>")
}

// countLocked adds what a message used to it and to its conversation.
func (c *conv) countLocked(t *Turn, used TokenUsage) {
	if used.sum() <= 0 {
		return
	}
	u := used.norm()
	if t.Tokens != nil {
		u = t.Tokens.plus(u)
	}
	t.Tokens = &u
	c.data.Tokens = c.data.Tokens.plus(used)
}

// tokensUsed is what the conversation has used.
func (c *conv) tokensUsed() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.data.Tokens.Total
}

// compactPrompt is MetaRush's request that Claude summarize a conversation
// (Claude Code's /compact with what to keep for a video edit, D181).
const compactPrompt = "/compact This is a video-editing conversation in MetaRush. Keep: the project and the cuts with their IDs, " +
	"the latest version of each and what every version changed, what the person asked for and liked or didn't, the decisions made, " +
	"open requests and questions, the cards waiting for the person, the motion graphics and their state, and what you were about to do next. " +
	"Leave out raw command output, frame descriptions and transcripts: metarush commands read them again when needed."

// compactMessage is a summary request for a conversation (caller holds c.mu).
func (m *Manager) compactMessage(c *conv) *message {
	return &message{prompt: compactPrompt, compact: true, rc: c.data.lastContextLocked(), shown: "Summarizing the conversation so far, to keep it quick."}
}

// shouldCompactLocked says whether to summarize a conversation now: its
// memory passed the setting, it isn't straight after a summary, and it grew
// by half the setting since the last summary (one that couldn't get it small
// enough isn't tried after every message).
func (m *Manager) shouldCompactLocked(c *conv) bool {
	cfg := m.App.Cfg.Assistant
	d := &c.data
	at := cfg.CompactAt()
	switch {
	case !cfg.Compacts(), d.Legacy, d.SessionLost, c.stopping, c.respawn, m.terminal:
		return false
	case d.ContextTokens < at:
		return false
	case d.CompactedTo > 0 && d.ContextTokens < d.CompactedTo+at/2:
		return false
	case len(d.Turns) > 0 && d.Turns[len(d.Turns)-1].Compact:
		return false
	}
	return true
}

// planFile keeps the plan's usage Claude Code last reported, so terminal
// commands and a restarted daemon show it too.
func (m *Manager) planFile() string { return filepath.Join(m.dir(), "plan-usage.json") }

// setPlanUsage keeps the plan's usage Claude Code last reported.
func (m *Manager) setPlanUsage(p *PlanUsage) {
	m.planMu.Lock()
	m.planUsage = p
	m.planMu.Unlock()
	if b, err := json.Marshal(p); err == nil {
		tmp := m.planFile() + ".tmp"
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, m.planFile())
		}
	}
}

// PlanUsage is the Claude plan's usage Claude Code last reported on this
// machine (nil: none yet).
func (m *Manager) PlanUsage() *PlanUsage {
	m.planMu.Lock()
	defer m.planMu.Unlock()
	if m.planUsage == nil {
		var p PlanUsage
		if b, err := os.ReadFile(m.planFile()); err != nil || json.Unmarshal(b, &p) != nil || p.At.IsZero() {
			return nil
		}
		return &p
	}
	p := *m.planUsage
	return &p
}
