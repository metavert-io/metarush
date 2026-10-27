package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/skillsinstall"
)

// Dual use: every assistant operation has a dedicated `metarush assistant`
// command (annotated with the ops it runs), so agents and power users can do
// everything the dashboard's Claude Code panel does.
func TestEveryAssistantOpHasACommand(t *testing.T) {
	covered := map[string]string{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, name := range strings.Split(c.Annotations[opsAnnotation], ",") {
			if name != "" {
				covered[name] = c.CommandPath()
			}
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	root := &cobra.Command{Use: "metarush"}
	for _, c := range commands() {
		root.AddCommand(c)
	}
	walk(root)
	n := 0
	for _, op := range ops.All() {
		if !strings.HasPrefix(op.Name, "assistant.") {
			continue
		}
		n++
		path, ok := covered[op.Name]
		if !ok {
			t.Errorf("op %s has no dedicated CLI command", op.Name)
			continue
		}
		if !strings.HasPrefix(path, "metarush assistant ") {
			t.Errorf("op %s is served by %q, outside `metarush assistant`", op.Name, path)
		}
	}
	if n < 15 {
		t.Fatalf("only %d assistant ops registered", n)
	}
	for name := range covered {
		if _, ok := ops.Get(name); !ok {
			t.Errorf("a command claims unknown op %s", name)
		}
	}
}

// The read-only assistant commands are allowlisted for interactive Claude Code
// sessions, but the dashboard's own headless assistant cannot drive
// conversations (nor read other conversations).
func TestAssistantCommandsAndAllowlists(t *testing.T) {
	root := &cobra.Command{Use: "metarush"}
	for _, c := range commands() {
		root.AddCommand(c)
	}
	readOnly := map[string]bool{}
	for _, c := range skillsinstall.ReadOnlyCommands {
		readOnly[c] = true
	}
	for _, path := range []string{"metarush assistant status", "metarush assistant conversations list", "metarush assistant conversations show",
		"metarush assistant proposals", "metarush assistant suggestions", "metarush assistant models"} {
		args := strings.Fields(strings.TrimPrefix(path, "metarush "))
		c, _, err := root.Find(args)
		if err != nil || c.CommandPath() != path {
			t.Errorf("%s: %v (%v)", path, err, c)
		}
		if !readOnly[path] {
			t.Errorf("%s is read-only and should be allowlisted for interactive sessions", path)
		}
		if assistant.AllowsCommand(path) {
			t.Errorf("the headless assistant must not run %s", path)
		}
	}
	for _, path := range []string{"metarush assistant ask", "metarush assistant act", "metarush assistant apply", "metarush assistant continue", "metarush assistant settings"} {
		if readOnly[path] {
			t.Errorf("%s changes things and must not be allowlisted", path)
		}
	}
	// `assistant conversation show` (singular) works too.
	if c, _, err := root.Find([]string{"assistant", "conversation", "show"}); err != nil || c.Name() != "show" {
		t.Errorf("alias: %v", err)
	}
}

func TestBillingWording(t *testing.T) {
	if s := spend("plan", 0.42, false); s != "" {
		t.Errorf("a Claude plan shows no dollars by default: %q", s)
	}
	if s := spend("plan", 0.42, true); !strings.Contains(s, "API-equivalent estimate") || !strings.Contains(s, "not billed") {
		t.Errorf("raw view labels the estimate: %q", s)
	}
	if s := spend("api", 0.42, false); strings.TrimSpace(s) != "$0.42" {
		t.Errorf("API key: %q", s)
	}
	if l := limitLines("plan", 50, 0); len(l) != 1 || l[0] != "limit: 50M tokens per conversation" {
		t.Errorf("plan limit (D181: tokens, no dollars): %q", l)
	}
	if l := limitLines("api", 50, 2); len(l) != 2 || !strings.Contains(l[1], "$2.00") {
		t.Errorf("api limits: %q", l)
	}
	used := assistant.TokenUsage{CacheRead: 1_200_000, Output: 30_000, Total: 1_230_000}
	e := assistant.AssistantEvent{Kind: "turn_end", State: assistant.TurnSucceeded, Result: &assistant.TurnResult{DurationMs: 12300, Steps: 6, CostUSD: 0.08, Tokens: &used}}
	if got := turnFooter(e, "plan", false); strings.Contains(got, "$") || !strings.Contains(got, "6 steps") || !strings.Contains(got, "1.2M tokens") {
		t.Errorf("plan footer: %q", got)
	}
	if got := turnFooter(e, "api", false); !strings.Contains(got, "$0.08") {
		t.Errorf("api footer: %q", got)
	}
}
