package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Assistant configures the dashboard's prompt box, which runs the person's local
// Claude Code headless (read-only; it proposes, people apply). Conversations are
// multi-turn: one long-lived Claude Code process per active conversation.
type Assistant struct {
	// Disabled turns the prompt box off (the dashboard falls back to copy-a-prompt).
	Disabled bool `yaml:"disabled,omitempty"`
	// Model is passed to `claude --model` when set (default: Claude Code's default).
	// A conversation can override it (`/model` in the dashboard, `--model` on the CLI).
	Model string `yaml:"model,omitempty"`
	// Effort is passed to `claude --effort` when set (low, medium, high, xhigh, max;
	// default: Claude Code's default). A conversation can override it (`/effort`).
	Effort string `yaml:"effort,omitempty"`
	// MaxBudgetUSD caps one conversation (all its messages) in dollars with an
	// API-key sign-in, where tokens are real spend (`claude --max-budget-usd`;
	// default 2.00). A Claude plan sign-in has no dollar cap (D181): its usage
	// counts toward the plan, and TokenLimitM is its limit. A conversation can
	// change it (`/budget`).
	MaxBudgetUSD float64 `yaml:"maxBudgetUsd,omitempty"`
	// TokenLimitM caps one conversation in millions of tokens (D181): everything
	// Claude reads and writes for it as Claude Code reports it — new input,
	// writing and reading its prompt cache (most of it: each step reads the
	// whole conversation again), output and summaries. Default 50. A
	// conversation can change it (`/limit`).
	TokenLimitM float64 `yaml:"tokenLimitMillions,omitempty"`
	// Compact has Claude summarize a conversation (/compact) once its memory
	// (context) passes CompactAtTokens: right after a reply while the cache is
	// warm, or before the next message (D181). Default on.
	Compact *bool `yaml:"compact,omitempty"`
	// CompactAtTokens is when Compact summarizes. Default 200000.
	CompactAtTokens int `yaml:"compactAtTokens,omitempty"`
	// AutoCompactTokens is Claude Code's own summary point inside one long
	// message (`claude --autocompact`, 100000–1000000). Default 300000.
	AutoCompactTokens int `yaml:"autoCompactTokens,omitempty"`
	// CacheHour keeps a conversation in Claude's prompt cache for an hour
	// instead of five minutes (CLAUDE_CODE_PROMPT_CACHE_TTL=1h), so a pause to
	// watch a render doesn't make the next message read everything again at
	// full price. Writing the cache costs a little more. Default on.
	CacheHour *bool `yaml:"cacheHour,omitempty"`
	// HoldNews tells Claude about the person's clicks on its cards (and the work
	// they finished) with their next message instead of as a message of its own
	// (D181). Safe steps that ran by themselves and anything that failed are
	// still told at once: Claude carries on from those. Default on.
	HoldNews *bool `yaml:"holdNews,omitempty"`
	// MaxSteps caps Claude Code's steps for one message (`claude --max-turns`). Default 60.
	MaxSteps int `yaml:"maxSteps,omitempty"`
	// TimeoutMinutes stops one message's work that runs longer. Default 10.
	TimeoutMinutes int `yaml:"timeoutMinutes,omitempty"`
	// IdleMinutes ends a conversation's Claude Code process after this long without
	// a message; the next message resumes the session (`claude --resume`). Default 10.
	IdleMinutes int `yaml:"idleMinutes,omitempty"`
	// SafeSteps lets safe steps Claude asks for start by themselves (D141):
	// checking a motion graphic Claude wrote, a review-quality render, and
	// bringing in a link the person typed in the conversation. Approvals,
	// saving versions, final renders and anything that costs money always wait
	// for a click. Default on; false makes every step a card to click.
	SafeSteps *bool `yaml:"safeSteps,omitempty"`
}

// RunsSafeSteps reports whether safe steps start by themselves (default on).
func (a Assistant) RunsSafeSteps() bool { return a.SafeSteps == nil || *a.SafeSteps }

// Compacts reports whether conversations are summarized at CompactAt (default on).
func (a Assistant) Compacts() bool { return a.Compact == nil || *a.Compact }

// CachesAnHour reports whether the prompt cache lasts an hour (default on).
func (a Assistant) CachesAnHour() bool { return a.CacheHour == nil || *a.CacheHour }

// HoldsNews reports whether card outcomes wait for the next message (default on).
func (a Assistant) HoldsNews() bool { return a.HoldNews == nil || *a.HoldNews }

// TokenLimit returns the default per-conversation limit in millions of tokens.
func (a Assistant) TokenLimit() float64 {
	if a.TokenLimitM > 0 {
		return a.TokenLimitM
	}
	return DefaultAssistantTokenLimitM
}

// CompactAt returns the context size, in tokens, at which a conversation is summarized.
func (a Assistant) CompactAt() int {
	if a.CompactAtTokens > 0 {
		return a.CompactAtTokens
	}
	return DefaultAssistantCompactAt
}

// AutoCompact returns Claude Code's own summary point, in tokens.
func (a Assistant) AutoCompact() int {
	if a.AutoCompactTokens > 0 {
		return a.AutoCompactTokens
	}
	return DefaultAssistantAutoCompact
}

// Assistant defaults.
const (
	DefaultAssistantBudgetUSD      = 2.00 // API-key sign-in: real spend per conversation
	DefaultAssistantTokenLimitM    = 50   // millions of tokens per conversation (D181)
	DefaultAssistantCompactAt      = 200000
	DefaultAssistantAutoCompact    = 300000
	DefaultAssistantMaxSteps       = 60
	DefaultAssistantTimeoutMinutes = 10
	DefaultAssistantIdleMinutes    = 10
)

// Budget returns the default per-conversation dollar cap for API-key billing.
func (a Assistant) Budget() float64 { return a.BudgetFor(false) }

// BudgetFor returns the default per-conversation dollar cap: real dollars for
// API-key billing; none (0) for a Claude plan sign-in, whose limit is tokens.
func (a Assistant) BudgetFor(plan bool) float64 {
	switch {
	case plan:
		return 0
	case a.MaxBudgetUSD > 0:
		return a.MaxBudgetUSD
	}
	return DefaultAssistantBudgetUSD
}

// Steps returns the per-message step limit.
func (a Assistant) Steps() int {
	if a.MaxSteps > 0 {
		return a.MaxSteps
	}
	return DefaultAssistantMaxSteps
}

// Timeout returns the per-turn time limit in minutes.
func (a Assistant) Timeout() int {
	if a.TimeoutMinutes > 0 {
		return a.TimeoutMinutes
	}
	return DefaultAssistantTimeoutMinutes
}

// Idle returns the idle time in minutes after which a conversation's process ends.
func (a Assistant) Idle() int {
	if a.IdleMinutes > 0 {
		return a.IdleMinutes
	}
	return DefaultAssistantIdleMinutes
}

// AssistantDir is the per-installation workspace the headless assistant runs in.
func (c *Config) AssistantDir() string { return filepath.Join(c.home, "assistant") }

// UpdateAssistant edits only the `assistant` section of config.yaml: each key in
// set is written (a nil value removes it). The rest of the file, keychain
// references and ${VAR} placeholders included, is kept as written. (A loaded
// Config is never written back: it holds resolved secrets and env overrides.)
func UpdateAssistant(home string, set map[string]any) error {
	p := Path(home)
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", p, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s is not a YAML mapping", p)
	}
	var section *yaml.Node
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "assistant" {
			section = root.Content[i+1]
		}
	}
	if section == nil || section.Kind != yaml.MappingNode {
		if section == nil {
			section = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "assistant"}, section)
		} else {
			*section = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
	}
	for key, v := range set {
		idx := -1
		for i := 0; i+1 < len(section.Content); i += 2 {
			if section.Content[i].Value == key {
				idx = i
			}
		}
		if v == nil {
			if idx >= 0 {
				section.Content = append(section.Content[:idx], section.Content[idx+2:]...)
			}
			continue
		}
		val := &yaml.Node{Kind: yaml.ScalarNode}
		switch x := v.(type) {
		case string:
			val.Tag, val.Value, val.Style = "!!str", x, yaml.DoubleQuotedStyle
		case float64:
			val.Tag, val.Value = "!!float", strconv.FormatFloat(x, 'f', 2, 64)
		case int:
			val.Tag, val.Value = "!!int", strconv.Itoa(x)
		case bool:
			val.Tag, val.Value = "!!bool", strconv.FormatBool(x)
		default:
			return fmt.Errorf("unsupported value for assistant.%s", key)
		}
		if idx >= 0 {
			section.Content[idx+1] = val
		} else {
			section.Content = append(section.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
		}
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, out, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
