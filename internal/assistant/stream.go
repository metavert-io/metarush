package assistant

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// timeNow is time.Now (tests replace it).
var timeNow = time.Now

// streamLine is the subset of Claude Code's stream-json output the assistant reads.
type streamLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	// system/init and every line
	Model     string `json:"model"`
	Version   string `json:"claude_code_version"`
	SessionID string `json:"session_id"`
	// system/permission_denied
	ToolUseID string `json:"tool_use_id"`
	ToolName  string `json:"tool_name"`
	// assistant / user (an object), system/permission_denied (a string)
	ParentToolUseID *string         `json:"parent_tool_use_id"`
	RawMessage      json.RawMessage `json:"message"`
	Message         struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
		Usage   *usage          `json:"usage"`
	} `json:"-"`
	// system/compact_boundary
	Compact *compactInfo `json:"compact_metadata"`
	// rate_limit_event
	RateLimit *rateLimitInfo `json:"rate_limit_info"`
	// result: this message's usage (Usage) and the session's running total by model
	Usage *usage `json:"usage"`
	// stream_event (--include-partial-messages)
	Event *streamEvent `json:"event"`
	// result
	IsError           bool                       `json:"is_error"`
	Result            string                     `json:"result"`
	TotalCostUSD      float64                    `json:"total_cost_usd"`
	DurationMs        int64                      `json:"duration_ms"`
	NumTurns          int                        `json:"num_turns"`
	PermissionDenials json.RawMessage            `json:"permission_denials"`
	ModelUsage        map[string]modelUsageEntry `json:"modelUsage"`
	// control_response
	Response *struct {
		RequestID string `json:"request_id"`
	} `json:"response"`
}

type usage struct {
	InputTokens              int `json:"input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

func (u *usage) tokens() TokenUsage {
	if u == nil {
		return TokenUsage{}
	}
	return TokenUsage{Input: int64(u.InputTokens), CacheWrite: int64(u.CacheCreationInputTokens), CacheRead: int64(u.CacheReadInputTokens), Output: int64(u.OutputTokens)}.norm()
}

type modelUsageEntry struct {
	ContextWindow            int   `json:"contextWindow"`
	InputTokens              int64 `json:"inputTokens"`
	OutputTokens             int64 `json:"outputTokens"`
	CacheReadInputTokens     int64 `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64 `json:"cacheCreationInputTokens"`
}

type streamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock *struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"content_block"`
	Delta *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
}

type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// partialOp tells the conversation how its live (unfinished) block changed.
type partialOp struct {
	start bool   // a new content block began (phase says which kind)
	phase string // text | thinking | tool
	delta string // text appended to the current text block
	clear bool   // the block is complete (its final event follows)
}

// lineOut is everything one stdout line contributed.
type lineOut struct {
	events  []AssistantEvent
	partial *partialOp
	result  *TurnResult
	init    bool
	denied  []deniedCall // tool calls Claude Code's permission mode refused
	ackID   string       // a control_response for this request ID
	// used is what a reply this line finished used (counted once per reply,
	// a helper's replies too): the conversation's limit is checked as it goes.
	used      TokenUsage
	compacted *compactInfo // Claude Code summarized the conversation
	plan      *PlanUsage   // the Claude plan's usage (rate_limit_event)
}

// deniedCall is a tool call the read-only allowlist refused (with its input).
type deniedCall struct {
	ToolUseID string
	Name      string
	Input     json.RawMessage
}

// callInfo remembers a tool call until its result arrives.
type callInfo struct {
	name  string
	input json.RawMessage
	cmd   *cliCall
	label string
}

// parser turns stream-json lines into condensed transcript events. One parser
// lives as long as one Claude Code process.
type parser struct {
	workspace string      // Claude Code's working directory
	convDir   string      // the conversation folder (proposals live under it)
	scratch   string      // frames and contact sheets
	assistant string      // the assistant folder (workspace + conversations)
	aliases   [][2]string // the same roots as configured (before resolving symlinks)
	names     func(id string) string

	model, version, sessionID string
	denied                    map[string]bool
	calls                     map[string]*callInfo
	seen                      map[string]string // image path → the footage it shows
	contextTokens             int
	contextWindow             int
	counted                   map[string]bool // replies whose usage was counted (one reply spans several lines)
	title                     string          // from the first reply's [title: …] line
	final                     string          // the turn's final reply in full (the activity log keeps it)
}

func newParser(workspace, convDir, scratch, assistantDir string, names func(string) string) *parser {
	if names == nil {
		names = func(string) string { return "" }
	}
	return &parser{workspace: workspace, convDir: convDir, scratch: scratch, assistant: assistantDir, names: names,
		denied: map[string]bool{}, calls: map[string]*callInfo{}, seen: map[string]string{}, counted: map[string]bool{}}
}

var titleLine = regexp.MustCompile(`(?s)\n?[ \t]*\[title:\s*([^\]\n]{1,80})\][ \t]*\n?\s*$`)

// stripTitle removes the trailing [title: …] line and returns it.
func stripTitle(s string) (string, string) {
	m := titleLine.FindStringSubmatchIndex(s)
	if m == nil {
		return s, ""
	}
	return strings.TrimRight(s[:m[0]], " \t\n"), strings.TrimSpace(s[m[2]:m[3]])
}

// line parses one stdout line; unknown or malformed lines yield nothing.
func (p *parser) line(raw string) lineOut {
	var out lineOut
	raw = strings.TrimSpace(raw)
	if raw == "" || raw[0] != '{' {
		return out
	}
	var l streamLine
	if json.Unmarshal([]byte(raw), &l) != nil {
		return out
	}
	if l.Type == "assistant" || l.Type == "user" {
		_ = json.Unmarshal(l.RawMessage, &l.Message)
	}
	if l.SessionID != "" {
		p.sessionID = l.SessionID
	}
	switch l.Type {
	case "system":
		switch l.Subtype {
		case "init":
			if l.Model != "" {
				p.model = l.Model
			}
			if l.Version != "" {
				p.version = l.Version
			}
			out.init = true
		case "compact_boundary":
			if c := l.Compact; c != nil {
				out.compacted = c
				if c.Post > 0 {
					p.contextTokens = c.Post
				}
				if c.Pre > 0 && c.Post > 0 {
					out.events = append(out.events, AssistantEvent{Kind: "notice", Text: fmt.Sprintf("Claude summarized the conversation to keep it quick: its memory went from %s to %s tokens.", Millions(int64(c.Pre)), Millions(int64(c.Post)))})
				}
			}
		case "permission_denied":
			p.denied[l.ToolUseID] = true
			if c := p.calls[l.ToolUseID]; c != nil {
				out.denied = append(out.denied, deniedCall{ToolUseID: l.ToolUseID, Name: c.name, Input: c.input})
			}
		}
	case "stream_event":
		if l.ParentToolUseID != nil && *l.ParentToolUseID != "" || l.Event == nil {
			return out
		}
		switch l.Event.Type {
		case "content_block_start":
			phase := "text"
			if cb := l.Event.ContentBlock; cb != nil {
				switch cb.Type {
				case "thinking", "redacted_thinking":
					phase = "thinking"
				case "tool_use", "server_tool_use":
					phase = "tool"
				}
			}
			out.partial = &partialOp{start: true, phase: phase}
		case "content_block_delta":
			if d := l.Event.Delta; d != nil && d.Type == "text_delta" && d.Text != "" {
				out.partial = &partialOp{delta: d.Text}
			}
		case "message_stop":
			out.partial = &partialOp{clear: true}
		}
	case "rate_limit_event":
		out.plan = planUsageOf(l.RateLimit, timeNow())
	case "assistant":
		if u := l.Message.Usage; u != nil && l.Message.ID != "" && !p.counted[l.Message.ID] {
			p.counted[l.Message.ID] = true
			out.used = u.tokens()
		}
		if l.ParentToolUseID != nil && *l.ParentToolUseID != "" {
			return out // inside a subagent: the parent's tool row covers it
		}
		if u := l.Message.Usage; u != nil {
			if n := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens; n > 0 {
				p.contextTokens = n
			}
		}
		var blocks []contentBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return out
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				text, title := stripTitle(b.Text)
				if title != "" && p.title == "" {
					p.title = clip(title, 80)
				}
				if t := strings.TrimSpace(text); t != "" {
					out.events = append(out.events, AssistantEvent{Kind: "text", Text: clip(t, 32<<10)})
				}
				out.partial = &partialOp{clear: true}
			case "tool_use":
				call := p.describe(b.ID, b.Name, b.Input)
				out.events = append(out.events, AssistantEvent{Kind: "tool", Tool: call})
				out.partial = &partialOp{clear: true}
			}
		}
	case "user":
		if l.ParentToolUseID != nil && *l.ParentToolUseID != "" {
			return out
		}
		var blocks []contentBlock
		if json.Unmarshal(l.Message.Content, &blocks) != nil {
			return out
		}
		for _, b := range blocks {
			if b.Type != "tool_result" {
				continue
			}
			text, hasImage := resultText(b.Content)
			denied := p.denied[b.ToolUseID] || (b.IsError && strings.HasPrefix(text, "Permission to use"))
			tc := &ToolCall{ID: b.ToolUseID, IsError: b.IsError, Denied: denied}
			if denied {
				// Not "blocked": a refused change is already a card, and a
				// refused look-up is one Claude does another way.
				tc.Output = firstLine(text)
			} else {
				tc.Output = clip(text, 4000)
				tc.Outcome = p.outcome(p.calls[b.ToolUseID], text, b.IsError, hasImage)
				if !b.IsError {
					p.rememberImages(p.calls[b.ToolUseID], text)
				}
			}
			out.events = append(out.events, AssistantEvent{Kind: "tool_result", Tool: tc})
		}
	case "result":
		var denials []json.RawMessage
		_ = json.Unmarshal(l.PermissionDenials, &denials)
		var session TokenUsage
		for _, mu := range l.ModelUsage {
			if mu.ContextWindow > p.contextWindow {
				p.contextWindow = mu.ContextWindow
			}
			session = session.plus(TokenUsage{Input: mu.InputTokens, CacheWrite: mu.CacheCreationInputTokens, CacheRead: mu.CacheReadInputTokens, Output: mu.OutputTokens})
		}
		text, title := stripTitle(strings.TrimSpace(l.Result))
		if title != "" && p.title == "" {
			p.title = clip(title, 80)
		}
		p.final = clip(text, 1<<20)
		out.result = &TurnResult{Subtype: l.Subtype, IsError: l.IsError, Text: clip(text, 8000), TotalCostUSD: l.TotalCostUSD,
			DurationMs: l.DurationMs, Steps: l.NumTurns, Denials: len(denials), ContextTokens: p.contextTokens, ContextWindow: p.contextWindow,
			session: session, message: l.Usage.tokens()}
		out.partial = &partialOp{clear: true}
	case "control_response":
		if l.Response != nil {
			out.ackID = l.Response.RequestID
		}
	}
	return out
}

// describe condenses a tool call to a row: a readable label, the raw command or
// file (summary), the raw input (clipped), images it looks at and the checklist.
func (p *parser) describe(id, name string, input json.RawMessage) *ToolCall {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(k string) string { s, _ := in[k].(string); return s }
	info := &callInfo{name: name, input: input}
	p.calls[id] = info
	tc := &ToolCall{ID: id, Name: name, Input: clip(string(input), 4000)}
	switch name {
	case "Bash":
		cmd := strings.Join(strings.Fields(str("command")), " ")
		tc.Summary = clip(p.short(cmd), 300)
		words, err := splitCommand(str("command"))
		if err != nil {
			// "metarush … --json 2>&1 | head": the metarush part, for labels only.
			words, err = splitCommand(leadingCommand(str("command")))
		}
		if err == nil {
			if call, ok := parseMetarush(words); ok {
				info.cmd = &call
			}
		}
		tc.Label = p.bashLabel(info.cmd, str("command"))
	case "Read":
		path := str("file_path")
		tc.Summary = p.rel(path)
		st := p.readLabel(path)
		tc.Label, tc.Group, tc.Many, tc.Images = st.label, st.group, st.many, st.images
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		path := str("file_path")
		tc.Summary = p.rel(path)
		if name == "Write" {
			if c, ok := in["content"].(string); ok {
				tc.Summary += fmt.Sprintf(" (%d bytes)", len(c))
			}
		}
		var st step
		st, tc.Proposal = p.writeLabel(name, path)
		tc.Label, tc.Group, tc.Many = st.label, st.group, st.many
	case "Skill":
		s := str("skill")
		if s == "" {
			s = str("command")
		}
		tc.Summary = s
		tc.Label = skillLabels[s]
		if tc.Label == "" {
			tc.Label = "Looked up how to do this"
		}
	case "Glob":
		tc.Summary = strings.TrimSpace(str("pattern") + " " + p.rel(str("path")))
		tc.Label, tc.Group, tc.Many = "Looked through MetaRush's notes", "notes", "Read %d of MetaRush's notes"
	case "Grep":
		tc.Summary = clip(strings.TrimSpace(str("pattern")+" "+p.rel(str("path"))), 200)
		tc.Label, tc.Group, tc.Many = "Searched the notes", "search", "Searched the notes %d times"
		if pat := strings.TrimSpace(str("pattern")); plainSearch.MatchString(pat) {
			tc.Label += " for “" + clip(pat, 60) + "”" // words, not a pattern full of symbols
		}
	case "TodoWrite":
		if todos, ok := in["todos"].([]any); ok {
			for _, t := range todos {
				m, _ := t.(map[string]any)
				c, _ := m["content"].(string)
				st, _ := m["status"].(string)
				af, _ := m["activeForm"].(string)
				if c != "" {
					tc.Todos = append(tc.Todos, TodoItem{Content: clip(c, 300), Status: st, ActiveForm: clip(af, 300)})
				}
			}
			tc.Summary = fmt.Sprintf("%d steps", len(tc.Todos))
		}
		tc.Label = "Updated the checklist"
	case "Task", "Agent":
		tc.Summary = clip(str("description"), 200)
		tc.Label = "Asked a helper to " + clip(strings.ToLower(str("description")), 80)
	case "WebFetch":
		tc.Label = "Tried to open a web page"
	case "WebSearch":
		tc.Label = "Tried to search the web"
	default:
		tc.Label = "Worked on it" // a tool MetaRush doesn't describe: never its name
	}
	info.label = tc.Label
	return tc
}

// plainSearch is a search made of words (shown in the label), not a pattern.
var plainSearch = regexp.MustCompile(`^[\p{L}\p{N}' -]{1,60}$`)

func (p *parser) rel(path string) string {
	if path == "" {
		return ""
	}
	for _, base := range []string{p.workspace, p.assistant} {
		if base == "" {
			continue
		}
		if r, err := filepath.Rel(base, path); err == nil && !strings.HasPrefix(r, "..") {
			return p.short(filepath.ToSlash(r))
		}
	}
	return path
}

var proposalsPathRe = regexp.MustCompile(`(?:[^\s'"]*/)?conversations/` + ConvPrefix + `_[0-9a-z]{26}/proposals/`)
var legacyProposalsRe = regexp.MustCompile(`(?:[^\s'"]*/)?runs/` + RunPrefix + `_[0-9a-z]{26}/proposals/`)

// short drops the workspace prefix and the conversation folder from paths in a
// summary, so "…/conversations/asc_…/proposals/t1/x.plan.json" reads "proposals/t1/x.plan.json".
func (p *parser) short(s string) string {
	for _, base := range []string{p.workspace, p.assistant} {
		if base != "" {
			s = strings.ReplaceAll(s, base+string(filepath.Separator), "")
		}
	}
	s = proposalsPathRe.ReplaceAllString(s, "proposals/")
	return legacyProposalsRe.ReplaceAllString(s, "proposals/")
}

// resultText flattens a tool_result content (a string or a list of blocks).
func resultText(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, false
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		image := false
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, b.Text)
			case "image":
				image = true
				parts = append(parts, "[image]")
			}
		}
		return strings.Join(parts, "\n"), image
	}
	return "", false
}

// clip shortens s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
