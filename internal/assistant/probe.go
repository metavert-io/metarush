package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/tools"
)

// claudeCaps are optional Claude Code flags detected from `claude --help`, so the
// assistant uses the strictest settings the installed version supports.
type claudeCaps struct {
	dontAsk   bool // --permission-mode dontAsk: deny anything not allowlisted
	tools     bool // --tools: offer only the built-in tools the assistant needs
	strictMCP bool // --strict-mcp-config: no MCP servers
	partial   bool // --include-partial-messages: stream text as it is written
	sessionID bool // --session-id: choose the conversation's session ID up front
	effort    bool // --effort <level>
	// autoCompact: --autocompact <tokens>, Claude Code's own summary point (D181)
	autoCompact bool
}

type probeResult struct {
	status AssistantStatus
	caps   claudeCaps
}

// requiredFlags must all appear in `claude --help` for the assistant to work.
// Conversations need stream-json input (follow-ups on stdin) and --resume (a
// conversation outlives its process).
var requiredFlags = []string{"--output-format", "--input-format", "--resume", "--max-budget-usd", "--setting-sources", "--allowedTools", "--disallowedTools", "--append-system-prompt"}

const (
	probeTTL     = 60 * time.Second // a working setup is re-checked every minute
	probeFailTTL = 10 * time.Second // a failure is re-checked soon (it may be transient)
)

// probe checks that Claude Code is installed, recent enough and signed in. It
// never sends a prompt (no cost). It runs detached from the caller's context: a
// browser tab that navigates away must not kill the checks half way and leave a
// wrong verdict in the cache.
func (m *Manager) probe(ctx context.Context, refresh bool) probeResult {
	m.probeMu.Lock()
	defer m.probeMu.Unlock()
	if !refresh && m.probed != nil {
		ttl := probeTTL
		if !m.probed.status.Available {
			ttl = probeFailTTL
		}
		if time.Since(m.probedAt) < ttl {
			return *m.probed
		}
	}
	r := m.runProbe(context.WithoutCancel(ctx))
	m.probed, m.probedAt = &r, time.Now()
	m.billingMode.Store(r.status.Billing)
	return r
}

func (m *Manager) runProbe(ctx context.Context) probeResult {
	cfg := m.App.Cfg.Assistant
	st := AssistantStatus{BudgetUSD: cfg.Budget(), TokenLimitM: cfg.TokenLimit(), MaxSteps: cfg.Steps(), TimeoutMinutes: cfg.Timeout(), IdleMinutes: cfg.Idle(), Model: cfg.Model,
		Machine: m.App.Cfg.Installation.Name, InstallationID: m.App.Cfg.Installation.ID, Working: []string{}, CheckedAt: time.Now().UTC()}
	st.Billing, st.BillingLabel = billing("")
	r := probeResult{}
	if cfg.Disabled {
		st.Reason = "The Claude Code panel is turned off on this machine."
		st.Hint = "To turn it on, delete the “disabled” line under “assistant” in this machine's config.yaml. You can still copy a prompt into your own Claude Code session."
		r.status = st
		return r
	}
	if !m.App.Tools.Has(tools.Claude) {
		st.Reason = "Claude Code is not installed on this machine, or MetaRush can't find it."
		st.Hint = "Install Claude Code and sign in to it once, then restart MetaRush. Until then, you can copy the prompt into your own Claude Code session."
		r.status = st
		return r
	}
	env := childEnv(os.Environ(), m.binDir(), m.App.Cfg.Home(), m.App.Cfg.AssistantDir())
	run := func(timeout time.Duration, args ...string) (string, error) {
		c, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		res, err := m.App.Tools.Run(c, tools.Spec{Tool: tools.Claude, Args: args, Env: env, CleanEnv: true, Dir: os.TempDir()})
		out := ""
		if res != nil {
			out = string(res.Stdout) + string(res.Stderr)
		}
		return out, err
	}
	ver, err := run(30*time.Second, "--version")
	if err != nil {
		st.Reason = "Claude Code is installed but doesn't start." + saidText(err)
		st.Hint = "Open Claude Code once in a terminal to see what's wrong, then restart MetaRush."
		r.status = st
		return r
	}
	st.ClaudeVersion = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(firstLine(ver)), "(Claude Code)"))
	help, err := run(30*time.Second, "--help")
	if err != nil || strings.TrimSpace(help) == "" {
		st.Reason = "Claude Code did not answer when MetaRush checked it." + saidText(err)
		st.Hint = "Press Check again. If it keeps happening, open Claude Code in a terminal to see why."
		r.status = st
		return r
	}
	for _, f := range requiredFlags {
		if !strings.Contains(help, f) {
			st.Reason = "This version of Claude Code (" + st.ClaudeVersion + ") is too old for the Claude Code panel."
			st.Hint = "Update Claude Code, then restart MetaRush."
			r.status = st
			return r
		}
	}
	r.caps = claudeCaps{dontAsk: strings.Contains(help, "dontAsk"), tools: strings.Contains(help, "--tools "),
		strictMCP: strings.Contains(help, "--strict-mcp-config"), partial: strings.Contains(help, "--include-partial-messages"),
		sessionID: strings.Contains(help, "--session-id"), effort: strings.Contains(help, "--effort"), autoCompact: strings.Contains(help, "--autocompact")}
	var rawAuth map[string]any
	if out, err := run(20*time.Second, "auth", "status", "--json"); err == nil || out != "" {
		var as struct {
			LoggedIn   *bool  `json:"loggedIn"`
			AuthMethod string `json:"authMethod"`
		}
		if i := strings.Index(out, "{"); i >= 0 && json.Unmarshal([]byte(out[i:]), &as) == nil && as.LoggedIn != nil {
			st.LoggedIn, st.AuthMethod = as.LoggedIn, as.AuthMethod
			_ = json.Unmarshal([]byte(out[i:]), &rawAuth)
		}
	}
	st.Billing, st.BillingLabel = billing(st.AuthMethod)
	if st.LoggedIn == nil || *st.LoggedIn {
		// Whose Claude answers here, and its terms (D239).
		st.Account = accountOf(rawAuth, st.AuthMethod, st.Machine)
		st.Account.At = st.CheckedAt
	}
	st.BudgetUSD, st.TokenLimitM = cfg.BudgetFor(st.Billing == "plan"), cfg.TokenLimit()
	if st.LoggedIn != nil && !*st.LoggedIn {
		st.Reason = "Claude Code is installed but not signed in."
		st.Hint = "Open Claude Code once in a terminal and sign in, then press Check again."
		r.status = st
		return r
	}
	st.Available = true
	r.status = st
	return r
}

// billing classifies Claude Code's sign-in. A Claude plan (subscription) sign-in
// is not billed per token: its usage counts toward the plan's limits, and the
// dollar figures Claude Code reports are API-equivalent estimates. An API key or
// a cloud provider is billed per token.
func billing(authMethod string) (mode, label string) {
	a := strings.ToLower(authMethod)
	switch {
	case strings.Contains(a, "claude.ai") || strings.Contains(a, "subscription") || strings.Contains(a, "oauth"):
		return "plan", "Claude plan (subscription): usage counts toward your plan limits"
	case strings.Contains(a, "api") || strings.Contains(a, "key") || strings.Contains(a, "bedrock") || strings.Contains(a, "vertex") || strings.Contains(a, "foundry"):
		return "api", "API key: you pay for what each message uses"
	}
	return "unknown", "Claude Code's sign-in did not say how it is billed"
}

// saidText is what Claude Code itself printed when it failed (" It said: …"),
// or nothing: MetaRush's own wording of the failure stays out of the reason.
func saidText(err error) string {
	var ef *tools.ErrFailed
	if errors.As(err, &ef) {
		if s := firstLine(ef.Stderr); s != "" {
			return " It said: " + s
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 240 {
		s = s[:240] + "…"
	}
	return s
}
