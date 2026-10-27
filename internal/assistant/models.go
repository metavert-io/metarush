package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// EffortLevels are the effort levels Claude Code accepts (claude --effort).
var EffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// AssistantModel is one model the installed Claude Code offers.
type AssistantModel struct {
	Value         string   `json:"value" jsonschema:"description=What to pass to claude --model (an alias or a full model name)."`
	DisplayName   string   `json:"displayName"`
	Description   string   `json:"description,omitempty"`
	ResolvedModel string   `json:"resolvedModel,omitempty"`
	EffortLevels  []string `json:"effortLevels,omitempty" jsonschema:"description=Effort levels this model supports (empty: effort cannot be set)."`
	Unavailable   string   `json:"unavailable,omitempty" jsonschema:"description=Why it cannot be used yet (e.g. update Claude Code)."`
}

// AssistantModelsOut is the model catalog of the installed Claude Code.
type AssistantModelsOut struct {
	Models      []AssistantModel `json:"models" jsonschema:"description=Usable models; the first (value default) is Claude Code's own choice."`
	Unavailable []AssistantModel `json:"unavailable,omitempty"`
	Source      string           `json:"source" jsonschema:"enum=claude-code,enum=fallback,description=claude-code: reported by the installed Claude Code; fallback: it could not be asked, only the documented aliases are offered."`
	Note        string           `json:"note,omitempty"`
	CheckedAt   time.Time        `json:"checkedAt"`
}

// AssistantSettingsOut are the assistant's installation defaults.
type AssistantSettingsOut struct {
	Model     string  `json:"model,omitempty" jsonschema:"description=Default model (empty: Claude Code's default)."`
	Effort    string  `json:"effort,omitempty" jsonschema:"description=Default effort (empty: Claude Code's default)."`
	BudgetUSD float64 `json:"budgetUsd" jsonschema:"description=Default dollar cap per conversation with an API key; 0 on a Claude plan (its limit is tokens)."`
	BudgetSet bool    `json:"budgetSet" jsonschema:"description=The dollar cap is set in config.yaml (otherwise the default applies)."`
	// Usage and memory (D181).
	TokenLimitM       float64    `json:"tokenLimitMillions" jsonschema:"description=Limit per conversation in millions of tokens."`
	Compact           bool       `json:"compact" jsonschema:"description=Claude summarizes a conversation once its memory passes compactAtTokens."`
	CompactAtTokens   int        `json:"compactAtTokens"`
	AutoCompactTokens int        `json:"autoCompactTokens" jsonschema:"description=Claude Code's own summary point inside one long message."`
	CacheHour         bool       `json:"cacheHour" jsonschema:"description=The prompt cache lasts an hour instead of five minutes."`
	HoldNews          bool       `json:"holdNews" jsonschema:"description=Your clicks on Claude's cards are told with your next message (safe steps and failures right away)."`
	PlanUsage         *PlanUsage `json:"planUsage,omitempty"`
	MaxSteps          int        `json:"maxSteps"`
	TimeoutMinutes    int        `json:"timeoutMinutes"`
	IdleMinutes       int        `json:"idleMinutes"`
	Billing           string     `json:"billing" jsonschema:"enum=plan,enum=api,enum=unknown"`
	BillingLabel      string     `json:"billingLabel"`
	SafeSteps         bool       `json:"safeSteps" jsonschema:"description=The computer's master switch for steps that run by themselves: on\\, safe steps Claude asks for start without a click (checking a motion graphic it wrote\\, a review copy\\, bringing in a link you typed in the conversation) and each person may turn on Auto in the Claude Code panel; off\\, nothing runs by itself and Auto isn't offered."`
}

// AssistantSettingsIn changes the installation defaults (only the fields given).
type AssistantSettingsIn struct {
	Model       *string  `json:"model,omitempty" jsonschema:"maxLength=80,description=Empty or default restores Claude Code's default."`
	Effort      *string  `json:"effort,omitempty" jsonschema:"description=low/medium/high/xhigh/max; empty or default restores Claude Code's default."`
	SafetyLimit *float64 `json:"safetyLimitUsd,omitempty" jsonschema:"minimum=0,maximum=100,description=Per-conversation cap in dollars with an API key (0 restores the default)."`
	SafeSteps   *bool    `json:"safeSteps,omitempty" jsonschema:"description=Let safe steps start by themselves (true\\, the default) or wait for a click (false)."`
	// Usage and memory (D181); 0 restores a number's default.
	TokenLimitM       *float64 `json:"tokenLimitMillions,omitempty" jsonschema:"minimum=0,maximum=1000,description=Limit per conversation in millions of tokens (0: the default\\, 50)."`
	Compact           *bool    `json:"compact,omitempty"`
	CompactAtTokens   *int     `json:"compactAtTokens,omitempty" jsonschema:"minimum=0,maximum=900000,description=Summarize when Claude's memory passes this many tokens (0: the default\\, 200000)."`
	AutoCompactTokens *int     `json:"autoCompactTokens,omitempty" jsonschema:"minimum=0,maximum=1000000,description=Claude Code's own summary point (0: the default\\, 300000)."`
	CacheHour         *bool    `json:"cacheHour,omitempty"`
	HoldNews          *bool    `json:"holdNews,omitempty"`
}

const modelsTTL = 10 * time.Minute

// Models asks the installed Claude Code which models (and effort levels) it
// supports, through the stream-json `initialize` control request: no prompt is
// sent, nothing is billed. Cached for ten minutes.
func (m *Manager) Models(ctx context.Context, refresh bool) (*AssistantModelsOut, error) {
	m.modelsMu.Lock()
	defer m.modelsMu.Unlock()
	if !refresh && m.models != nil && time.Since(m.models.CheckedAt) < modelsTTL {
		return m.models, nil
	}
	if !m.App.Tools.Has(tools.Claude) {
		return nil, apperr.New(apperr.ToolMissing, "Claude Code is not installed on this machine, so MetaRush can't list its models.").
			WithHint("Install Claude Code and sign in to it once, then restart MetaRush.")
	}
	out, err := m.askModels(context.WithoutCancel(ctx))
	if err != nil {
		out = &AssistantModelsOut{Source: "fallback", Note: "Claude Code didn't list its models, so only the usual choices are offered: Default, Opus, Sonnet and Haiku.",
			Models: []AssistantModel{{Value: "default", DisplayName: "Default", Description: "Claude Code's own choice"},
				{Value: "opus", DisplayName: "Opus"}, {Value: "sonnet", DisplayName: "Sonnet"}, {Value: "haiku", DisplayName: "Haiku"}}}
	}
	out.CheckedAt = time.Now().UTC()
	m.models = out
	return out, nil
}

// askModels runs `claude -p --input-format stream-json …` with no tools, sends
// the initialize control request and reads the model list from its response.
func (m *Manager) askModels(ctx context.Context) (*AssistantModelsOut, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer w.Close()
	if _, err := w.Write([]byte(`{"type":"control_request","request_id":"mr_models","request":{"subtype":"initialize"}}` + "\n")); err != nil {
		r.Close()
		return nil, err
	}
	var found *AssistantModelsOut
	var failure error
	dir := filepath.Join(m.dir(), "models-probe")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		r.Close()
		return nil, err
	}
	_, runErr := m.App.Tools.Run(ctx, tools.Spec{Tool: tools.Claude, Dir: dir, CleanEnv: true, Stdin: r, MaxLine: 8 << 20,
		Env: childEnv(os.Environ(), m.binDir(), m.App.Cfg.Home(), dir),
		Args: []string{"-p", "--input-format", "stream-json", "--output-format", "stream-json", "--verbose",
			"--setting-sources", "project,local", "--strict-mcp-config", "--tools", "", "--no-session-persistence"},
		OnStart: func(int) { _ = r.Close() },
		OnLine: func(line string) {
			if found != nil || failure != nil {
				return
			}
			found, failure = parseModels(line)
			if found != nil || failure != nil {
				cancel() // done: end the process
			}
		}})
	_ = r.Close()
	switch {
	case found != nil:
		return found, nil
	case failure != nil:
		return nil, failure
	case runErr != nil:
		return nil, runErr
	}
	return nil, apperr.New(apperr.ToolFailed, "Claude Code stopped without answering.")
}

// parseModels reads the initialize control response (nil, nil for other lines).
func parseModels(raw string) (*AssistantModelsOut, error) {
	type entry struct {
		Value                 string   `json:"value"`
		ResolvedModel         string   `json:"resolvedModel"`
		DisplayName           string   `json:"displayName"`
		Description           string   `json:"description"`
		SupportsEffort        bool     `json:"supportsEffort"`
		SupportedEffortLevels []string `json:"supportedEffortLevels"`
	}
	var line struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Error     string `json:"error"`
			Response  struct {
				Models      []entry `json:"models"`
				Unavailable []entry `json:"unavailable_models"`
			} `json:"response"`
		} `json:"response"`
	}
	if json.Unmarshal([]byte(raw), &line) != nil || line.Type != "control_response" || line.Response.RequestID != "mr_models" {
		return nil, nil
	}
	if line.Response.Subtype != "success" || len(line.Response.Response.Models) == 0 {
		return nil, apperr.New(apperr.ToolFailed, "Claude Code didn't list its models%s", suffix(" (", line.Response.Error, ")"))
	}
	conv := func(e entry) AssistantModel {
		mi := AssistantModel{Value: e.Value, DisplayName: e.DisplayName, Description: e.Description, ResolvedModel: e.ResolvedModel}
		if e.SupportsEffort {
			mi.EffortLevels = e.SupportedEffortLevels
		}
		return mi
	}
	out := &AssistantModelsOut{Source: "claude-code"}
	for _, e := range line.Response.Response.Models {
		out.Models = append(out.Models, conv(e))
	}
	for _, e := range line.Response.Response.Unavailable {
		mi := conv(e)
		mi.Unavailable = e.Description
		out.Unavailable = append(out.Unavailable, mi)
	}
	return out, nil
}

func suffix(pre, v, post string) string {
	if v == "" {
		return ""
	}
	return pre + v + post
}

// resolveModel validates a model and effort against the catalog; it returns
// the model value to pass ("" for Claude Code's default) and the effort.
func (m *Manager) resolveModel(ctx context.Context, model, effort string) (string, string, error) {
	model, effort = strings.TrimSpace(model), strings.ToLower(strings.TrimSpace(effort))
	if effort == "default" {
		effort = ""
	}
	if effort != "" && !contains(EffortLevels, effort) {
		return "", "", apperr.New(apperr.InvalidInput, "Choose an effort level: %s.", strings.Join(EffortLevels, ", "))
	}
	if model == "" || model == "default" {
		model = ""
	}
	if model == "" && effort == "" {
		return "", "", nil
	}
	if _, err := ValidModel(model); err != nil {
		return "", "", err
	}
	cat, err := m.Models(ctx, false)
	if err != nil {
		return "", "", err
	}
	look := model
	if look == "" {
		look = "default"
	}
	var found *AssistantModel
	for i := range cat.Models {
		mi := &cat.Models[i]
		if strings.EqualFold(mi.Value, look) || (mi.ResolvedModel != "" && strings.EqualFold(mi.ResolvedModel, look)) {
			found = mi
			break
		}
	}
	if found == nil {
		for _, mi := range cat.Unavailable {
			if strings.EqualFold(mi.Value, look) {
				return "", "", apperr.New(apperr.InvalidInput, "%s isn't available in this Claude Code: %s", mi.DisplayName, mi.Unavailable).WithHint("Update Claude Code, or choose another model.")
			}
		}
		names := make([]string, 0, len(cat.Models))
		for _, mi := range cat.Models {
			names = append(names, mi.Value)
		}
		return "", "", apperr.New(apperr.InvalidInput, "Claude Code doesn't offer the model “%s”.", model).WithHint("Choose one of: " + strings.Join(names, ", ") + ".")
	}
	if effort != "" && cat.Source == "claude-code" && !contains(found.EffortLevels, effort) {
		if len(found.EffortLevels) == 0 {
			return "", "", apperr.New(apperr.InvalidInput, "%s doesn't take an effort level.", found.DisplayName).WithHint("Choose Default effort, or another model.")
		}
		return "", "", apperr.New(apperr.InvalidInput, "%s takes these effort levels: %s.", found.DisplayName, strings.Join(found.EffortLevels, ", "))
	}
	return model, effort, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Settings returns the installation defaults.
func (m *Manager) Settings(ctx context.Context) *AssistantSettingsOut {
	st := m.probe(ctx, false).status
	cfg := m.App.Cfg.Assistant
	return &AssistantSettingsOut{Model: cfg.Model, Effort: cfg.Effort, BudgetUSD: cfg.BudgetFor(st.Billing == "plan"), BudgetSet: cfg.MaxBudgetUSD > 0,
		MaxSteps: cfg.Steps(), TimeoutMinutes: cfg.Timeout(), IdleMinutes: cfg.Idle(), Billing: st.Billing, BillingLabel: st.BillingLabel,
		SafeSteps: cfg.RunsSafeSteps(), TokenLimitM: cfg.TokenLimit(), Compact: cfg.Compacts(), CompactAtTokens: cfg.CompactAt(),
		AutoCompactTokens: cfg.AutoCompact(), CacheHour: cfg.CachesAnHour(), HoldNews: cfg.HoldsNews(), PlanUsage: m.PlanUsage()}
}

// SetSettings changes the installation defaults in config.yaml (and in this
// process). Conversations without their own override use them from their next
// message on.
func (m *Manager) SetSettings(ctx context.Context, in AssistantSettingsIn) (*AssistantSettingsOut, error) {
	cfg := &m.App.Cfg.Assistant
	model, effort := cfg.Model, cfg.Effort
	if in.Model != nil {
		model = *in.Model
	}
	if in.Effort != nil {
		effort = *in.Effort
	}
	model, effort, err := m.resolveModel(ctx, model, effort)
	if err != nil {
		return nil, err
	}
	set := map[string]any{}
	if in.Model != nil {
		set["model"] = nilIfEmpty(model)
	}
	if in.Effort != nil {
		set["effort"] = nilIfEmpty(effort)
	}
	budget := cfg.MaxBudgetUSD
	if in.SafetyLimit != nil {
		v := *in.SafetyLimit
		switch {
		case v == 0:
			set["maxBudgetUsd"], budget = nil, 0
		default:
			if err := ValidBudget(v); err != nil {
				return nil, err
			}
			set["maxBudgetUsd"], budget = v, v
		}
	}
	safe := cfg.SafeSteps
	if in.SafeSteps != nil {
		v := *in.SafeSteps
		safe = &v
		if v {
			set["safeSteps"] = nil // the default
		} else {
			set["safeSteps"] = false
		}
	}
	usage, err := usageSettings(in, set)
	if err != nil {
		return nil, err
	}
	if len(set) == 0 {
		return m.Settings(ctx), nil
	}
	if err := config.UpdateAssistant(m.App.Cfg.Home(), set); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't save the setting in this machine's config.yaml.")
	}
	cfg.Model, cfg.Effort, cfg.MaxBudgetUSD, cfg.SafeSteps = model, effort, budget, safe
	usage.apply(cfg)
	m.probeMu.Lock()
	m.probed = nil // the status repeats the defaults
	m.probeMu.Unlock()
	m.auditAs(ctx, "assistant.settings", "installation", m.App.Cfg.Installation.ID, "", map[string]any{"model": model, "effort": effort, "maxBudgetUsd": budget,
		"safeSteps": cfg.RunsSafeSteps(), "tokenLimitMillions": cfg.TokenLimit(), "compact": cfg.Compacts(), "compactAtTokens": cfg.CompactAt(),
		"autoCompactTokens": cfg.AutoCompact(), "cacheHour": cfg.CachesAnHour(), "holdNews": cfg.HoldsNews()})
	if in.Model != nil || in.Effort != nil || in.SafetyLimit != nil || in.AutoCompactTokens != nil || in.CacheHour != nil {
		m.respawnAll() // the model and limits apply from the next message on
	}
	return m.Settings(ctx), nil
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// respawnAll makes every live process restart (resuming its session) before its
// next message, so new defaults apply.
func (m *Manager) respawnAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.convs {
		c.mu.Lock()
		if c.proc != nil {
			c.respawn = true
		}
		c.mu.Unlock()
	}
}

// usageChange is a change to the usage and memory settings (D181).
type usageChange struct{ in AssistantSettingsIn }

// usageSettings checks the usage and memory settings and adds the config.yaml
// changes to set (a default writes nothing).
func usageSettings(in AssistantSettingsIn, set map[string]any) (usageChange, error) {
	if v := in.TokenLimitM; v != nil {
		if *v != 0 {
			if err := ValidTokenLimit(*v); err != nil {
				return usageChange{}, err
			}
			set["tokenLimitMillions"] = *v
		} else {
			set["tokenLimitMillions"] = nil
		}
	}
	if v := in.CompactAtTokens; v != nil {
		switch {
		case *v == 0:
			set["compactAtTokens"] = nil
		case *v < 50_000 || *v > 900_000:
			return usageChange{}, apperr.New(apperr.InvalidInput, "Summarize when Claude's memory passes between 50,000 and 900,000 tokens (you gave %d).", *v)
		default:
			set["compactAtTokens"] = *v
		}
	}
	if v := in.AutoCompactTokens; v != nil {
		switch {
		case *v == 0:
			set["autoCompactTokens"] = nil
		case *v < 100_000 || *v > 1_000_000:
			return usageChange{}, apperr.New(apperr.InvalidInput, "Claude Code's own summary point is between 100,000 and 1,000,000 tokens (you gave %d).", *v)
		default:
			set["autoCompactTokens"] = *v
		}
	}
	for key, v := range map[string]*bool{"compact": in.Compact, "cacheHour": in.CacheHour, "holdNews": in.HoldNews} {
		switch {
		case v == nil:
		case *v:
			set[key] = nil // on is the default
		default:
			set[key] = false
		}
	}
	return usageChange{in}, nil
}

// apply puts the checked change into this process's settings.
func (u usageChange) apply(cfg *config.Assistant) {
	in := u.in
	if v := in.TokenLimitM; v != nil {
		cfg.TokenLimitM = *v
	}
	if v := in.CompactAtTokens; v != nil {
		cfg.CompactAtTokens = *v
	}
	if v := in.AutoCompactTokens; v != nil {
		cfg.AutoCompactTokens = *v
	}
	flag := func(v *bool) *bool {
		if v == nil || *v {
			return nil
		}
		f := false
		return &f
	}
	if in.Compact != nil {
		cfg.Compact = flag(in.Compact)
	}
	if in.CacheHour != nil {
		cfg.CacheHour = flag(in.CacheHour)
	}
	if in.HoldNews != nil {
		cfg.HoldNews = flag(in.HoldNews)
	}
}
