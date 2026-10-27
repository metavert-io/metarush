package logbook

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/skills"
)

// The hook side (`metarush log hook <event>`, run by Claude Code) must be fast
// and must never get in the way: it reads its input, appends one line to the
// session's buffer under a file lock and exits 0, printing nothing (Claude Code
// would show UserPromptSubmit output to the model). It needs no database, no
// keychain and no network. At the end of each turn (Stop), a session that used
// MetaRush becomes a finished turn in <home>/state/claude-hooks/ready, which
// the next metarush process (or the daemon within seconds) writes to the log
// with the full redaction; other sessions' buffers are discarded.

const (
	maxHookInput    = 64 << 20
	maxHookPrompt   = 256 << 10
	transcriptBytes = 512 << 10
	lockWait        = 2 * time.Second
	staleSession    = 7 * 24 * time.Hour
)

// hookInput is the part of Claude Code's hook input MetaRush reads.
type hookInput struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	HookEventName  string          `json:"hook_event_name"`
	Prompt         string          `json:"prompt"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	DurationMs     float64         `json:"duration_ms"`
	Error          string          `json:"error"`
	LastMessage    string          `json:"last_assistant_message"`
	Reason         string          `json:"reason"`
	AgentID        string          `json:"agent_id"`
}

// bufLine is one buffered event of a session's current turn.
type bufLine struct {
	Type       string    `json:"type"` // prompt | command
	At         time.Time `json:"at"`
	Prompt     string    `json:"prompt,omitempty"`
	Chars      int       `json:"chars,omitempty"`
	Tool       string    `json:"tool,omitempty"`
	Summary    string    `json:"summary,omitempty"`
	OK         bool      `json:"ok"`
	Error      string    `json:"error,omitempty"`
	DurationMs float64   `json:"durationMs,omitempty"`
}

type sessionMeta struct {
	Relevant bool      `json:"relevant"`
	Turns    int       `json:"turns"`
	Started  time.Time `json:"started"`
}

// HookTurn is a finished terminal turn waiting to be written to the log.
type HookTurn struct {
	SessionID string    `json:"sessionId"`
	Turn      int       `json:"turn"`
	Prompt    string    `json:"prompt,omitempty"`
	Chars     int       `json:"chars"`
	Response  string    `json:"response,omitempty"`
	Commands  []bufLine `json:"commands"`
	ToolUses  int       `json:"toolUses"`
	StartedAt time.Time `json:"startedAt"`
	EndedAt   time.Time `json:"endedAt"`
	Reason    string    `json:"reason"`
}

func hooksDirFor(home string) string { return filepath.Join(home, "state", "claude-hooks") }

var sidRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

func sessionName(sid string) string {
	if sidRe.MatchString(sid) {
		return sid
	}
	h := sha256.Sum256([]byte(sid))
	return "s" + hex.EncodeToString(h[:12])
}

// RunHook handles one Claude Code hook event for the installation at home. It
// never returns an error: problems go to <home>/state/claude-hooks/errors.log.
func RunHook(home, event string, stdin io.Reader) {
	dir := hooksDirFor(home)
	defer func() {
		if r := recover(); r != nil {
			hookError(dir, fmt.Errorf("panic: %v", r))
		}
	}()
	raw, err := io.ReadAll(io.LimitReader(stdin, maxHookInput))
	_, _ = io.Copy(io.Discard, stdin)
	if err != nil {
		hookError(dir, err)
		return
	}
	var in hookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		hookError(dir, fmt.Errorf("%s: unreadable input: %v", event, err))
		return
	}
	if in.SessionID == "" {
		return
	}
	if _, err := os.Stat(dir); err != nil {
		return // hooks were turned off for this installation (or never on)
	}
	if err := handleHook(home, dir, event, &in); err != nil {
		hookError(dir, fmt.Errorf("%s: %v", event, err))
	}
}

func hookError(dir string, err error) {
	p := filepath.Join(dir, "errors.log")
	if st, e := os.Stat(p); e == nil && st.Size() > 256<<10 {
		_ = os.Truncate(p, 0)
	}
	f, e := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if e != nil {
		return
	}
	fmt.Fprintf(f, "%s %v\n", time.Now().UTC().Format(time.RFC3339), err)
	f.Close()
}

// hookRedactor hides secrets with what is available without the keychain:
// patterns, field names, this process's environment, the installation's .env
// and the fingerprints of the configured secrets.
type hookRedactor struct {
	r  *Redactor
	fp *Fingerprints
}

func newHookRedactor(home, dir string) *hookRedactor {
	r := NewRedactor()
	r.FromEnvironment()
	if b, err := os.ReadFile(filepath.Join(home, ".env")); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok || strings.HasPrefix(k, "#") {
				continue
			}
			k = strings.TrimSpace(strings.TrimPrefix(k, "export "))
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if pw := urlPassword(v); pw != "" {
				r.Add(pw)
			}
			if SecretEnvName(k) {
				r.Add(v)
			}
		}
	}
	return &hookRedactor{r: r, fp: LoadFingerprints(dir)}
}

func (h *hookRedactor) String(s string) string { return h.r.String(h.fp.Redact(s)) }

// hookPrivacy reads log.prompts from config.yaml without loading the whole
// configuration (no keychain, no secrets).
func hookPrivacy(home string) string {
	b, err := os.ReadFile(filepath.Join(home, "config.yaml"))
	if err != nil {
		return PrivacyWorkspace
	}
	var c struct {
		Log config.Log `yaml:"log"`
	}
	if yaml.Unmarshal([]byte(config.Expand(string(b))), &c) != nil {
		return PrivacyWorkspace
	}
	return c.Log.PromptsMode()
}

func handleHook(home, dir, event string, in *hookInput) error {
	if in.AgentID != "" && event != "SessionEnd" {
		return nil // a subagent's events: the main thread's turn covers it
	}
	sdir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sdir, 0o700); err != nil {
		return err
	}
	name := sessionName(in.SessionID)
	unlock, err := lockSession(filepath.Join(sdir, name+".lock"))
	if err != nil {
		return err
	}
	defer unlock()
	buf := filepath.Join(sdir, name+".jsonl")
	now := time.Now().UTC()
	switch event {
	case "UserPromptSubmit":
		line := bufLine{Type: "prompt", At: now, Chars: utf8.RuneCountInString(in.Prompt)}
		if hookPrivacy(home) != PrivacyOff {
			p := in.Prompt
			if len(p) > maxHookPrompt {
				p = p[:maxHookPrompt] + "\n… (cut)"
			}
			line.Prompt = newHookRedactor(home, dir).String(p)
		}
		return appendLine(buf, line)
	case "PostToolUse", "PostToolUseFailure":
		summary, ok := metarushUse(in.ToolName, in.ToolInput)
		if !ok {
			return nil // not MetaRush: nothing is kept
		}
		red := newHookRedactor(home, dir)
		line := bufLine{Type: "command", At: now, Tool: in.ToolName, Summary: Clip(red.String(summary), 500), OK: event == "PostToolUse", DurationMs: in.DurationMs}
		if in.Error != "" {
			line.Error = Clip(red.String(in.Error), 600)
		}
		return appendLine(buf, line)
	case "Stop", "SessionEnd":
		err := finishTurn(home, dir, sdir, name, buf, in, event)
		if event == "SessionEnd" {
			for _, ext := range []string{".jsonl", ".meta.json", ".relevant"} {
				_ = os.Remove(filepath.Join(sdir, name+ext))
			}
		}
		return err
	}
	return nil
}

var metarushCmd = regexp.MustCompile(`(?:^|[\s;&|()'"` + "`" + `])(?:[^\s;&|()'"` + "`" + `]*/)?metarush(?:$|[\s;&|()'"` + "`" + `])`)

// metarushUse reports whether a tool call used MetaRush: a shell command that
// runs the metarush CLI, or one of MetaRush's skills.
func metarushUse(tool string, input json.RawMessage) (string, bool) {
	var in struct {
		Command string `json:"command"`
		Skill   string `json:"skill"`
	}
	_ = json.Unmarshal(input, &in)
	switch tool {
	case "Bash":
		if metarushCmd.MatchString(in.Command) {
			return in.Command, true
		}
	case "Skill":
		s := in.Skill
		if s == "" {
			s = in.Command
		}
		base := s[strings.LastIndex(s, ":")+1:]
		for _, n := range skills.Names {
			if base == n {
				return s, true
			}
		}
		if strings.HasPrefix(base, "video-") || strings.HasPrefix(base, "metarush-") {
			return s, true
		}
	}
	return "", false
}

func finishTurn(home, dir, sdir, name, buf string, in *hookInput, event string) error {
	lines, err := readBuffer(buf)
	if err != nil {
		return err
	}
	metaPath := filepath.Join(sdir, name+".meta.json")
	var meta sessionMeta
	if b, err := os.ReadFile(metaPath); err == nil {
		_ = json.Unmarshal(b, &meta)
	}
	relevant := meta.Relevant
	if _, err := os.Stat(filepath.Join(sdir, name+".relevant")); err == nil {
		relevant = true // the metarush CLI ran inside this session
	}
	var prompts []string
	var commands []bufLine
	var chars int
	var started time.Time
	for _, l := range lines {
		switch l.Type {
		case "prompt":
			if started.IsZero() {
				started = l.At
			}
			chars += l.Chars
			if l.Prompt != "" {
				prompts = append(prompts, l.Prompt)
			}
		case "command":
			commands = append(commands, l)
			relevant = true
		}
	}
	_ = os.Remove(buf) // the turn is over either way
	if !relevant || (len(prompts) == 0 && chars == 0 && len(commands) == 0) {
		return nil // not a MetaRush session (yet): its prompts are discarded
	}
	if meta.Started.IsZero() {
		meta.Started = firstTime(started, time.Now().UTC())
	}
	meta.Relevant = true
	meta.Turns++
	mb, _ := json.Marshal(meta)
	if err := os.WriteFile(metaPath, mb, 0o600); err != nil {
		return err
	}
	red := newHookRedactor(home, dir)
	resp, tools := in.LastMessage, 0
	if t, n := transcriptTail(in.TranscriptPath); t != "" || n > 0 {
		if resp == "" {
			resp = t
		}
		tools = n
	}
	turn := HookTurn{SessionID: in.SessionID, Turn: meta.Turns, Prompt: strings.Join(prompts, "\n\n"), Chars: chars, Commands: commands,
		ToolUses: tools, StartedAt: firstTime(started, time.Now().UTC()), EndedAt: time.Now().UTC(), Reason: strings.ToLower(event)}
	if hookPrivacy(home) != PrivacyOff && resp != "" {
		if len(resp) > maxHookPrompt {
			resp = resp[:maxHookPrompt] + "\n… (cut)"
		}
		turn.Response = red.String(resp)
	}
	if turn.Commands == nil {
		turn.Commands = []bufLine{}
	}
	rdir := filepath.Join(dir, "ready")
	if err := os.MkdirAll(rdir, 0o700); err != nil {
		return err
	}
	tb, _ := json.Marshal(turn)
	p := filepath.Join(rdir, fmt.Sprintf("%s-%04d-%d.json", name, turn.Turn, time.Now().UnixNano()))
	if err := os.WriteFile(p+".tmp", tb, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

func firstTime(a, b time.Time) time.Time {
	if !a.IsZero() {
		return a
	}
	return b
}

func appendLine(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func readBuffer(path string) ([]bufLine, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []bufLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 4<<20)
	for sc.Scan() {
		var l bufLine
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			out = append(out, l)
		}
	}
	return out, nil
}

// lockSession takes an exclusive lock on a session (parallel tool calls fire
// hooks at the same time), waiting at most two seconds.
func lockSession(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(lockWait)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("session is busy: %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// transcriptTail reads the end of a Claude Code transcript: the last assistant
// text and how many tools were used since the last prompt. Only transcripts
// inside Claude Code's projects folder are read.
func transcriptTail(path string) (string, int) {
	if !trustedTranscript(path) {
		return "", 0
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0
	}
	off := st.Size() - transcriptBytes
	if off < 0 {
		off = 0
	}
	b := make([]byte, st.Size()-off)
	if _, err := f.ReadAt(b, off); err != nil && err != io.EOF {
		return "", 0
	}
	lines := bytes.Split(b, []byte("\n"))
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // the first line is cut
	}
	type block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	type line struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	last, tools := "", 0
	for _, raw := range lines {
		var l line
		if len(bytes.TrimSpace(raw)) == 0 || json.Unmarshal(raw, &l) != nil {
			continue
		}
		var blocks []block
		var text string
		if json.Unmarshal(l.Message.Content, &text) != nil {
			_ = json.Unmarshal(l.Message.Content, &blocks)
		}
		switch l.Type {
		case "user":
			prompt := text != ""
			for _, bl := range blocks {
				if bl.Type == "text" {
					prompt = true
				}
			}
			if prompt {
				tools, last = 0, ""
			}
		case "assistant":
			var parts []string
			for _, bl := range blocks {
				switch bl.Type {
				case "tool_use":
					tools++
				case "text":
					if strings.TrimSpace(bl.Text) != "" {
						parts = append(parts, bl.Text)
					}
				}
			}
			if len(parts) > 0 {
				last = strings.Join(parts, "\n")
			}
		}
	}
	return last, tools
}

func trustedTranscript(path string) bool {
	if path == "" || !filepath.IsAbs(path) || !strings.HasSuffix(path, ".jsonl") || strings.Contains(path, "..") {
		return false
	}
	root := os.Getenv("CLAUDE_CONFIG_DIR")
	if root == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		root = filepath.Join(h, ".claude")
	}
	rel, err := filepath.Rel(filepath.Join(root, "projects"), path)
	return err == nil && !strings.HasPrefix(rel, "..")
}

// markSessionRelevant tells the hook that the metarush CLI ran inside a Claude
// Code session (so its turns are recorded even when the hook could not tell
// from the command, e.g. a wrapper script). Nothing happens unless the hooks
// were turned on for this installation.
func markSessionRelevant(hooksDir, sid string) {
	sdir := filepath.Join(hooksDir, "sessions")
	if st, err := os.Stat(sdir); err != nil || !st.IsDir() {
		return
	}
	p := filepath.Join(sdir, sessionName(sid)+".relevant")
	if _, err := os.Stat(p); err == nil {
		return
	}
	_ = os.WriteFile(p, nil, 0o600)
}

// IngestHookTurns writes the terminal turns the hook finished to the log.
func (l *Logger) IngestHookTurns(ctx context.Context) int {
	if l == nil || l.cfg == nil {
		return 0
	}
	dir := filepath.Join(l.cfg.ClaudeHooksDir(), "ready")
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	n := 0
	for _, f := range files {
		if ctx.Err() != nil {
			break
		}
		claimed := fmt.Sprintf("%s.claim-%d", f, os.Getpid())
		if os.Rename(f, claimed) != nil {
			continue
		}
		b, err := os.ReadFile(claimed)
		_ = os.Remove(claimed)
		if err != nil {
			continue
		}
		var t HookTurn
		if json.Unmarshal(b, &t) != nil {
			continue
		}
		turn := Turn{Source: SourceClaudeTerminal, ConversationID: t.SessionID, Turn: t.Turn, Prompt: t.Prompt, Response: t.Response,
			StartedAt: t.StartedAt, Duration: t.EndedAt.Sub(t.StartedAt), State: "succeeded", ToolUses: t.ToolUses, PromptChars: t.Chars}
		for _, c := range t.Commands {
			ct := ClaudeTool{Name: c.Tool, Summary: c.Summary, IsError: !c.OK}
			switch {
			case c.Error != "":
				ct.Result = c.Error
			case c.OK:
				ct.Result = "ran"
			default:
				ct.Result = "failed"
			}
			turn.Tools = append(turn.Tools, ct)
		}
		l.Claude(ctx, turn)
		n++
	}
	if l.daemon {
		cleanStaleSessions(filepath.Join(l.cfg.ClaudeHooksDir(), "sessions"))
	}
	return n
}

// cleanStaleSessions removes buffers of sessions that ended without telling us.
func cleanStaleSessions(sdir string) {
	ents, err := os.ReadDir(sdir)
	if err != nil {
		return
	}
	for _, e := range ents {
		info, err := e.Info()
		if err == nil && time.Since(info.ModTime()) > staleSession {
			_ = os.Remove(filepath.Join(sdir, e.Name()))
		}
	}
}
