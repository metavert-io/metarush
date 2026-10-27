// Package firstrun says what MetaRush can do on this computer right after it
// is set up (D256): what works with no keys and costs nothing, what a key
// adds, and whether Claude Code is installed and signed in (D255). `metarush
// setup`, doctor and the dashboard's first run (op setup.status) all read it,
// so every surface says the same thing.
package firstrun

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/tools"
)

// ClaudeCode is Claude Code on this computer, as Claude Code itself reports it.
// MetaRush only asks (`claude --version`, `claude auth status --json`): it
// never signs anyone in and never reads Claude Code's credentials.
type ClaudeCode struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// SignedIn is Claude Code's own answer; absent when it couldn't say.
	SignedIn *bool `json:"signedIn,omitempty"`
	// Billing is how its use is paid for: plan (a Claude subscription, usage
	// counts toward the plan's limits), api (a key billed per use) or unknown.
	Billing string `json:"billing,omitempty" jsonschema:"enum=,enum=plan,enum=api,enum=unknown"`
	// Ready: installed and signed in, so editing by asking works.
	Ready bool `json:"ready"`
	// Says is what it means, Next what to do (plain words); Command is the
	// command that does it (for --details).
	Says    string `json:"says"`
	Next    string `json:"next,omitempty"`
	Command string `json:"command,omitempty"`
}

// InstallCommand installs Claude Code (Node.js is already one of MetaRush's needs).
const InstallCommand = "npm install -g @anthropic-ai/claude-code"

var (
	claudeMu   sync.Mutex
	claudeLast ClaudeCode
	claudeAt   time.Time
	claudePath string
)

// CheckClaude asks Claude Code whether it is installed and signed in; the
// answer is kept for a minute (the dashboard asks often).
func CheckClaude(ctx context.Context, ts *tools.Set) ClaudeCode {
	claudeMu.Lock()
	defer claudeMu.Unlock()
	path := ts.Path(tools.Claude)
	if path == claudePath && !claudeAt.IsZero() && time.Since(claudeAt) < time.Minute {
		return claudeLast
	}
	c := checkClaude(ctx, ts)
	claudeLast, claudeAt, claudePath = c, time.Now(), path
	return c
}

// ForgetClaude drops the kept answer (after setup, or a person pressing Check again).
func ForgetClaude() {
	claudeMu.Lock()
	claudeAt = time.Time{}
	claudeMu.Unlock()
}

func checkClaude(ctx context.Context, ts *tools.Set) ClaudeCode {
	if !ts.Has(tools.Claude) || testing.Testing() && !ts.Explicit(tools.Claude) {
		// Tests never run the person's own Claude Code (tools.claude names a fake).
		return ClaudeCode{Says: "Claude Code isn't installed on this computer. MetaRush works without it — the dashboard edits, makes transcripts and renders — and with it you edit by asking in plain words.",
			Next: "Install Claude Code, then open it once and sign in.", Command: InstallCommand}
	}
	run := func(args ...string) (string, error) {
		c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
		defer cancel()
		// The person's own environment, without MetaRush's secrets.
		res, err := ts.Run(tools.Quiet(c), tools.Spec{Tool: tools.Claude, Args: args, Dir: tempDir(), CleanEnv: true, Env: secretpath.WithoutSecrets(os.Environ())})
		out := ""
		if res != nil {
			out = string(res.Stdout) + string(res.Stderr)
		}
		return out, err
	}
	ver, err := run("--version")
	if err != nil {
		return ClaudeCode{Installed: true, Says: "Claude Code is on this computer but doesn't start.", Next: "Open Claude Code once in a terminal to see what's wrong.", Command: "claude"}
	}
	c := ClaudeCode{Installed: true, Version: strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(firstLine(ver)), "(Claude Code)"))}
	// Only the two answers MetaRush needs are read from Claude Code's report.
	if out, err := run("auth", "status", "--json"); err == nil || out != "" {
		var as struct {
			LoggedIn   *bool  `json:"loggedIn"`
			AuthMethod string `json:"authMethod"`
		}
		if i := strings.Index(out, "{"); i >= 0 && json.Unmarshal([]byte(out[i:]), &as) == nil && as.LoggedIn != nil {
			c.SignedIn = as.LoggedIn
			if *as.LoggedIn {
				c.Billing = billing(as.AuthMethod)
			}
		}
	}
	switch {
	case c.SignedIn == nil:
		c.Ready = true // it couldn't say; it asks for a sign-in itself when it needs one
		c.Says = "Claude Code is installed. It didn't say whether it is signed in; if it asks, sign in once."
		c.Next, c.Command = "", "claude"
	case !*c.SignedIn:
		c.Says = "Claude Code is installed but not signed in, so it can't edit for you yet."
		c.Next = "Open Claude Code once in a terminal and sign in. MetaRush never signs in for you."
		c.Command = "claude"
	case c.Billing == "plan":
		c.Ready = true
		c.Says = "Claude Code is installed and signed in with a Claude plan: editing by asking counts toward your plan's limits."
	case c.Billing == "api":
		c.Ready = true
		c.Says = "Claude Code is installed and signed in with an API key: you pay for what each message uses."
	default:
		c.Ready = true
		c.Says = "Claude Code is installed and signed in."
	}
	return c
}

// billing classifies Claude Code's sign-in like the dashboard's panel does.
func billing(authMethod string) string {
	a := strings.ToLower(authMethod)
	switch {
	case strings.Contains(a, "claude.ai") || strings.Contains(a, "subscription") || strings.Contains(a, "oauth"):
		return "plan"
	case strings.Contains(a, "api") || strings.Contains(a, "key") || strings.Contains(a, "bedrock") || strings.Contains(a, "vertex") || strings.Contains(a, "foundry"):
		return "api"
	}
	return "unknown"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}
