// Package fakeclaude is a stand-in `claude` executable for tests. A small shell
// wrapper answers the availability probes (--version, --help, auth status); a
// run re-executes the test binary in fake mode (call MaybeRun from TestMain),
// which speaks Claude Code's stream-json protocol: it reads user messages and
// control requests (initialize, interrupt) on stdin, plays a scripted turn per
// message (partial text deltas, tool calls, refused tool calls, proposal files,
// sleeps, crashes), keeps the session on disk so `--resume` works across
// processes, reports a cumulative session cost like Claude Code does, and
// records every spawn's argv.
package fakeclaude

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
	"unicode"
	"unicode/utf16"
)

const scriptEnv = "METARUSH_FAKECLAUDE_SCRIPT"

// Options configure the fake.
type Options struct {
	LoggedOut  bool   // `auth status` reports loggedIn: false
	AuthMethod string // default "claude.ai" (a Claude plan sign-in)
	// Email and Subscription are what `auth status` says of the account
	// (the subscription: pro, max, team, enterprise…).
	Email        string
	Subscription string
	Help         string  // replaces the --help text (default: every flag the assistant uses)
	HelpFails    bool    // --help prints nothing and exits 1
	Body         string  // a shell body run instead of the stream-json fake (crash tests); $RUN is the conversation folder
	Script       *Script // the conversation script (default: reply "ok" to everything)
}

// Script scripts a conversation: each user message plays the first turn whose
// Match is in the message (in order, each used once), else Default.
type Script struct {
	Turns   []Turn `json:"turns"`
	Default *Turn  `json:"default,omitempty"`
	// NoModels makes the initialize control request fail (an older Claude Code).
	NoModels bool `json:"noModels,omitempty"`
	// Sessions "claude" keeps each session where Claude Code does:
	// $CLAUDE_CONFIG_DIR/projects/<the working folder's key>/<session>.jsonl,
	// one line per message, appended on every turn (a resume finds it only
	// there; the fake refuses to run without CLAUDE_CONFIG_DIR, so a test never
	// writes into the real ~/.claude). Replies may then say {{remembered}} (how
	// many earlier messages the session holds) and {{first}} (its first one).
	// Default: a marker file in the conversation folder.
	Sessions string `json:"sessions,omitempty"`
}

// Turn is one scripted reply.
type Turn struct {
	Match     string  `json:"match,omitempty"`
	Steps     []Step  `json:"steps"`
	CostUSD   float64 `json:"costUsd,omitempty"`
	Subtype   string  `json:"subtype,omitempty"` // default success
	IsError   bool    `json:"isError,omitempty"`
	Result    string  `json:"result,omitempty"`
	ExitAfter bool    `json:"exitAfter,omitempty"` // the process exits after this turn
	// Context is how many tokens Claude's memory holds while it replies
	// (default 4420): each reply reads it from the cache.
	Context int `json:"context,omitempty"`
}

// Step is one thing Claude does in a turn.
type Step struct {
	Text    string    `json:"text,omitempty"`    // streamed as partial deltas, then the assistant message
	Tool    *ToolStep `json:"tool,omitempty"`    // a tool call and its result
	Write   *File     `json:"write,omitempty"`   // a file written into this message's proposals folder (with a Write tool row); {{head}} is replaced
	Emit    string    `json:"emit,omitempty"`    // a raw stdout line ({{session}}, {{proposals}} and {{head}} are replaced)
	SleepMs int       `json:"sleepMs,omitempty"` // interruptible
	Die     bool      `json:"die,omitempty"`     // crash mid-turn (exit 3)
}

// ToolStep is a tool call. Denied makes Claude Code's permission mode refuse it.
type ToolStep struct {
	ID      string         `json:"id,omitempty"`
	Name    string         `json:"name"`
	Input   map[string]any `json:"input"`
	Result  string         `json:"result,omitempty"`
	IsError bool           `json:"isError,omitempty"`
	Denied  bool           `json:"denied,omitempty"`
}

// File is a proposal file.
type File struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

// DefaultHelp lists every flag the assistant probes for.
const DefaultHelp = `Usage: claude [options] [prompt]
  --output-format <format>  --input-format <format>  --include-partial-messages  --resume [value]  --session-id <uuid>
  --max-budget-usd <amount>  --setting-sources <sources>  --effort <level>  --autocompact <auto|tokens>
  --allowedTools <tools...>  --disallowedTools <tools...>  --append-system-prompt <prompt>
  --permission-mode <mode> (choices: "acceptEdits", "dontAsk", "plan")  --tools <tools...>
  --strict-mcp-config  --no-session-persistence  --model <model>`

// Write creates the fake in a temp dir and returns its path. Script-driven runs
// re-execute the current test binary: its TestMain must call MaybeRun first.
func Write(t testing.TB, o Options) string {
	t.Helper()
	dir := t.TempDir()
	help := o.Help
	if help == "" {
		help = DefaultHelp
	}
	helpCase := "  --help) cat <<'MRFAKE_HELP'\n" + help + "\nMRFAKE_HELP\n  exit 0;;\n"
	if o.HelpFails {
		helpCase = "  --help) exit 1;;\n"
	}
	method := o.AuthMethod
	if method == "" {
		method = "claude.ai"
	}
	login := `{"loggedIn": true, "authMethod": "` + method + `"`
	if o.Email != "" {
		login += `, "email": "` + o.Email + `"`
	}
	if o.Subscription != "" {
		login += `, "subscriptionType": "` + o.Subscription + `"`
	}
	login += `}`
	if o.LoggedOut {
		login = `{"loggedIn": false, "authMethod": "none"}`
	}
	run := ""
	if o.Body != "" {
		run = "RUN=\"$METARUSH_ASSISTANT_RUN_DIR\"\n" +
			"for a in \"$@\"; do printf '%s\\0' \"$a\"; done > \"$RUN/argv.txt\"\n" +
			"env > \"$RUN/env.txt\"\n" + o.Body + "\n"
	} else {
		sc := o.Script
		if sc == nil {
			sc = &Script{}
		}
		b, _ := json.Marshal(sc)
		sp := filepath.Join(dir, "script.json")
		if err := os.WriteFile(sp, b, 0o644); err != nil {
			t.Fatal(err)
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		run = "exec env " + scriptEnv + "=" + shq(sp) + " " + shq(exe) + " \"$@\"\n"
	}
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  --version) echo '9.9.9 (Claude Code)'; exit 0;;\n" +
		helpCase +
		"  auth) echo '" + login + "'; exit 0;;\n" +
		"esac\n" + run
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Argv reads the arguments of the latest spawn (argv.txt, NUL-separated).
func Argv(t testing.TB, runDir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir, "argv.txt"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
}

// Spawns reads the argv of every spawn of a conversation, in order.
func Spawns(t testing.TB, runDir string) [][]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir, "argv.log"))
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var argv []string
		if json.Unmarshal([]byte(line), &argv) == nil {
			out = append(out, argv)
		}
	}
	return out
}

// Spawn is one start of the fake: its arguments, the folder it worked in
// and where Claude Code's own folder was.
type Spawn struct {
	Argv      []string `json:"argv"`
	Cwd       string   `json:"cwd"`
	ConfigDir string   `json:"configDir,omitempty"`
	Home      string   `json:"home,omitempty"`
}

// SpawnLog reads every start of a conversation's fake, in order.
func SpawnLog(t testing.TB, runDir string) []Spawn {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(runDir, "spawns.log"))
	if err != nil {
		t.Fatal(err)
	}
	var out []Spawn
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var s Spawn
		if json.Unmarshal([]byte(line), &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// Resumed is the session a spawn resumed ("" when it started a new one).
func (s Spawn) Resumed() string {
	for i, a := range s.Argv {
		if a == "--resume" && i+1 < len(s.Argv) {
			return s.Argv[i+1]
		}
	}
	return ""
}

// ProjectKey is Claude Code's name for a working folder's sessions folder
// (the fake's own copy of the rule, so a test catches MetaRush getting it
// wrong): every UTF-16 unit but ASCII letters and digits becomes "-"; past
// 200 the key is cut and gets the base-36 of the string's hash.
func ProjectKey(cwd string) string {
	units := utf16.Encode([]rune(cwd))
	b := make([]byte, len(units))
	var h int32
	for i, u := range units {
		h = h*31 + int32(u)
		if u < 128 && (unicode.IsLetter(rune(u)) || unicode.IsDigit(rune(u))) {
			b[i] = byte(u)
		} else {
			b[i] = '-'
		}
	}
	if len(b) <= 200 {
		return string(b)
	}
	n := int64(h)
	if n < 0 {
		n = -n
	}
	return string(b[:200]) + "-" + strconv.FormatInt(n, 36)
}

// SessionLine is one line of a session file the fake keeps (claude mode).
type SessionLine struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Text      string `json:"text"`
}

// Received reads every stdin line the fake got (all spawns).
func Received(t testing.TB, runDir string) []string {
	t.Helper()
	b, _ := os.ReadFile(filepath.Join(runDir, "stdin.log"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// MaybeRun runs the fake when this process was started as one. Call it first
// in TestMain: `if fakeclaude.MaybeRun() { return }` is not enough — it exits.
func MaybeRun() {
	sp := os.Getenv(scriptEnv)
	if sp == "" {
		return
	}
	os.Exit(run(sp))
}

// Run plays the fake with the conversation script at scriptPath. The
// fakeclaude command (cmd/fakeclaude, for end-to-end runs of the dashboard)
// calls it.
func Run(scriptPath string) int { return run(scriptPath) }

// Emit is a shell snippet printing lines verbatim (Body mode).
func Emit(lines ...string) string {
	return "cat <<'MRFAKE_EOF'\n" + strings.Join(lines, "\n") + "\nMRFAKE_EOF\n"
}

/* ---------- The fake itself ---------- */

type fake struct {
	run       string // conversation folder ($METARUSH_ASSISTANT_RUN_DIR)
	script    Script
	used      map[int]bool
	session   string
	budget    float64
	spent     float64 // this process
	out       *bufio.Writer
	mu        sync.Mutex
	lines     chan string
	interrupt chan struct{}
	turnBusy  bool
	// sessionFile is where the session is kept in claude mode; cwd the
	// folder the fake works in.
	sessionFile string
	cwd         string
}

type state struct {
	Cost float64 `json:"cost"`
	// The session's running token totals (Claude Code reports them by model
	// in each result, across resumes) and its memory after the last reply.
	Input, CacheWrite, CacheRead, Output int64
	Context                              int
	Msgs                                 int
}

func (f *fake) emit(v any) {
	b, _ := json.Marshal(v)
	f.mu.Lock()
	f.out.Write(append(b, '\n'))
	f.out.Flush()
	f.mu.Unlock()
}

func (f *fake) emitRaw(s string) {
	f.mu.Lock()
	f.out.WriteString(s + "\n")
	f.out.Flush()
	f.mu.Unlock()
}

func (f *fake) loadState() state {
	var st state
	b, _ := os.ReadFile(filepath.Join(f.run, "fake-state.json"))
	_ = json.Unmarshal(b, &st)
	return st
}

func (f *fake) saveState(st state) {
	b, _ := json.Marshal(st)
	_ = os.WriteFile(filepath.Join(f.run, "fake-state.json"), b, 0o600)
}

func run(scriptPath string) int {
	f := &fake{run: os.Getenv("METARUSH_ASSISTANT_RUN_DIR"), used: map[int]bool{}, out: bufio.NewWriter(os.Stdout),
		lines: make(chan string, 64), interrupt: make(chan struct{}, 1)}
	b, err := os.ReadFile(scriptPath)
	if err == nil {
		_ = json.Unmarshal(b, &f.script)
	}
	args := os.Args[1:]
	_ = os.MkdirAll(f.run, 0o700)
	var nul strings.Builder
	for _, a := range args {
		nul.WriteString(a + "\x00")
	}
	_ = os.WriteFile(filepath.Join(f.run, "argv.txt"), []byte(nul.String()), 0o600)
	if lf, err := os.OpenFile(filepath.Join(f.run, "argv.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		ab, _ := json.Marshal(args)
		lf.Write(append(ab, '\n'))
		lf.Close()
	}
	_ = os.WriteFile(filepath.Join(f.run, "env.txt"), []byte(strings.Join(os.Environ(), "\n")+"\n"), 0o600)
	f.cwd, _ = os.Getwd()
	if lf, err := os.OpenFile(filepath.Join(f.run, "spawns.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		sb, _ := json.Marshal(Spawn{Argv: args, Cwd: f.cwd, ConfigDir: os.Getenv("CLAUDE_CONFIG_DIR"), Home: os.Getenv("HOME")})
		lf.Write(append(sb, '\n'))
		lf.Close()
	}
	resume := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--session-id":
			if i+1 < len(args) {
				f.session = args[i+1]
			}
		case "--resume":
			if i+1 < len(args) {
				resume = args[i+1]
			}
		case "--max-budget-usd":
			if i+1 < len(args) {
				f.budget, _ = strconv.ParseFloat(args[i+1], 64)
			}
		}
	}
	sessions := filepath.Join(f.run, "fake-sessions")
	_ = os.MkdirAll(sessions, 0o700)
	if f.script.Sessions == "claude" {
		cfg := os.Getenv("CLAUDE_CONFIG_DIR")
		if cfg == "" {
			fmt.Fprintln(os.Stderr, "fake claude: claude sessions need CLAUDE_CONFIG_DIR (never the real home folder)")
			return 2
		}
		id := f.session
		if resume != "" {
			id = resume
		}
		if id != "" {
			f.sessionFile = filepath.Join(cfg, "projects", ProjectKey(f.cwd), id+".jsonl")
		}
		if resume != "" {
			if _, err := os.Stat(f.sessionFile); err != nil {
				fmt.Fprintf(os.Stderr, "No conversation found with session ID: %s\n", resume)
				return 1
			}
			f.session = resume
		}
	} else if resume != "" {
		if _, err := os.Stat(filepath.Join(sessions, resume)); err != nil {
			fmt.Fprintf(os.Stderr, "No conversation found with session ID: %s\n", resume)
			return 1
		}
		f.session = resume
	}
	if f.session == "" {
		f.session = "fake-session-" + strconv.Itoa(os.Getpid())
	}
	if f.script.Sessions == "claude" && f.sessionFile == "" {
		f.sessionFile = filepath.Join(os.Getenv("CLAUDE_CONFIG_DIR"), "projects", ProjectKey(f.cwd), f.session+".jsonl")
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT)
	go func() {
		<-sig
		// Like Claude Code: SIGINT ends the turn (reported as interrupted) and the process.
		f.mu.Lock()
		busy := f.turnBusy
		f.mu.Unlock()
		if busy {
			f.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": "[Request interrupted by user]"}}}, "parent_tool_use_id": nil, "session_id": f.session})
			f.emit(f.interruptedResult())
		}
		os.Exit(0)
	}()
	go func() {
		sc := bufio.NewScanner(os.Stdin)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		logf, _ := os.OpenFile(filepath.Join(f.run, "stdin.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		for sc.Scan() {
			line := sc.Text()
			if logf != nil {
				logf.WriteString(line + "\n")
			}
			var msg struct {
				Type    string `json:"type"`
				Request struct {
					Subtype string `json:"subtype"`
				} `json:"request"`
			}
			if json.Unmarshal([]byte(line), &msg) == nil && msg.Type == "control_request" && msg.Request.Subtype == "interrupt" {
				select {
				case f.interrupt <- struct{}{}:
				default:
				}
			}
			f.lines <- line
		}
		close(f.lines)
	}()
	for line := range f.lines {
		var msg struct {
			Type      string `json:"type"`
			RequestID string `json:"request_id"`
			Request   struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue
		}
		switch msg.Type {
		case "control_request":
			switch msg.Request.Subtype {
			case "initialize":
				if f.script.NoModels {
					f.emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "error", "request_id": msg.RequestID, "error": "unsupported"}})
				} else {
					f.emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": msg.RequestID, "response": map[string]any{"models": fakeModels}}})
				}
			case "interrupt":
				// Interrupt while idle: drain the signal and acknowledge.
				select {
				case <-f.interrupt:
				default:
				}
				f.emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": msg.RequestID, "response": map[string]any{}}})
			}
		case "user":
			text := ""
			for _, c := range msg.Message.Content {
				text += c.Text
			}
			if f.turn(text) {
				return 0
			}
		}
	}
	return 0
}

var fakeModels = []map[string]any{
	{"value": "default", "resolvedModel": "claude-fake-default", "displayName": "Default (recommended)", "description": "Fake default", "supportsEffort": true, "supportedEffortLevels": []string{"low", "medium", "high", "xhigh", "max"}},
	{"value": "opus", "resolvedModel": "claude-fake-opus", "displayName": "Fake Opus", "supportsEffort": true, "supportedEffortLevels": []string{"low", "medium", "high", "xhigh", "max"}},
	{"value": "sonnet", "resolvedModel": "claude-fake-sonnet", "displayName": "Fake Sonnet", "supportsEffort": true, "supportedEffortLevels": []string{"low", "medium", "high", "max"}},
	{"value": "haiku", "resolvedModel": "claude-fake-haiku", "displayName": "Fake Haiku"},
}

var proposalsLine = regexp.MustCompile(`Proposals folder for this message: (\S+)`)

// headLine is the head revision in a message's context; {{head}} in a script
// becomes its ID, so scripted plans stay valid as the cut moves.
var headLine = regexp.MustCompile(`Current head of main: r\d+ \((rev_[0-9a-z]+)\)`)

func (f *fake) pick(text string) Turn {
	for i, t := range f.script.Turns {
		if !f.used[i] && t.Match != "" && strings.Contains(text, t.Match) {
			f.used[i] = true
			return t
		}
	}
	if f.script.Default != nil {
		return *f.script.Default
	}
	return Turn{Steps: []Step{{Text: "ok"}}, CostUSD: 0.001}
}

func (f *fake) result(t Turn, total float64, steps int, st state, turn map[string]int64) map[string]any {
	sub := t.Subtype
	if sub == "" {
		sub = "success"
	}
	return map[string]any{"type": "result", "subtype": sub, "is_error": t.IsError, "result": t.Result, "total_cost_usd": total, "duration_ms": 1234,
		"num_turns": steps, "session_id": f.session, "permission_denials": []any{},
		"usage": map[string]any{"input_tokens": turn["input"], "cache_creation_input_tokens": turn["write"], "cache_read_input_tokens": turn["read"], "output_tokens": turn["output"]},
		"modelUsage": map[string]any{"claude-fake": map[string]any{"contextWindow": 200000, "inputTokens": st.Input, "cacheCreationInputTokens": st.CacheWrite,
			"cacheReadInputTokens": st.CacheRead, "outputTokens": st.Output}}}
}

// compact plays Claude Code's /compact: a summary of the conversation, told
// with system/compact_boundary (the memory before and after) and a result
// whose own usage is empty; the summary's tokens show only in the totals.
func (f *fake) compact() {
	st := f.loadState()
	pre := st.Context
	if pre == 0 {
		pre = 4420
	}
	f.emit(map[string]any{"type": "system", "subtype": "status", "status": "compacting", "session_id": f.session})
	st.CacheRead += int64(pre)
	st.Output += 500
	st.Context = 1500
	f.saveState(st)
	f.emit(map[string]any{"type": "system", "subtype": "status", "status": nil, "compact_result": "success", "session_id": f.session})
	f.emit(map[string]any{"type": "system", "subtype": "compact_boundary", "session_id": f.session, "compact_metadata": map[string]any{"trigger": "manual", "pre_tokens": pre, "post_tokens": 1500}})
	f.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": "<local-command-stdout>Compacted </local-command-stdout>"}, "parent_tool_use_id": nil, "session_id": f.session})
	f.emit(f.result(Turn{}, st.Cost, 0, st, map[string]int64{}))
}

// turn plays one scripted reply; it returns true when the process should exit.
func (f *fake) turn(text string) bool {
	if strings.HasPrefix(strings.TrimSpace(text), "/compact") {
		f.compact()
		return false
	}
	t := f.pick(text)
	proposals := ""
	if m := proposalsLine.FindStringSubmatch(text); m != nil {
		proposals = m[1]
	}
	_ = os.WriteFile(filepath.Join(f.run, "fake-sessions", f.session), []byte("1"), 0o600)
	remembered, first := f.remember(text)
	f.mu.Lock()
	f.turnBusy = true
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.turnBusy = false
		f.mu.Unlock()
	}()
	f.emit(map[string]any{"type": "system", "subtype": "init", "session_id": f.session, "model": "claude-fake", "claude_code_version": "9.9.9", "permissionMode": "dontAsk"})
	interrupted := func() bool {
		select {
		case <-f.interrupt:
			return true
		default:
			return false
		}
	}
	sleep := func(ms int) bool {
		end := time.Now().Add(time.Duration(ms) * time.Millisecond)
		for time.Now().Before(end) {
			if interrupted() {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return interrupted()
	}
	head := ""
	if m := headLine.FindStringSubmatch(text); m != nil {
		head = m[1]
	}
	repl := func(s string) string {
		return strings.NewReplacer("{{session}}", f.session, "{{proposals}}", proposals, "{{head}}", head,
			"{{remembered}}", strconv.Itoa(remembered), "{{first}}", first).Replace(s)
	}
	stt := f.loadState()
	ctxTokens := t.Context
	if ctxTokens == 0 {
		ctxTokens = 4420
	}
	stt.Context = ctxTokens
	usage := map[string]any{"input_tokens": 120, "cache_read_input_tokens": ctxTokens - 420, "cache_creation_input_tokens": 300, "output_tokens": 50}
	turnUse := map[string]int64{}
	// msg is one reply's envelope: an ID (a reply's usage is counted once) and its usage.
	msg := func(content []map[string]any) map[string]any {
		stt.Msgs++
		turnUse["input"] += 120
		turnUse["read"] += int64(ctxTokens - 420)
		turnUse["write"] += 300
		turnUse["output"] += 50
		// The session's totals grow as it goes (an interrupted turn's replies count too).
		stt.Input, stt.CacheRead, stt.CacheWrite, stt.Output = stt.Input+120, stt.CacheRead+int64(ctxTokens-420), stt.CacheWrite+300, stt.Output+50
		f.saveState(stt)
		return map[string]any{"id": fmt.Sprintf("msg_fake_%d", stt.Msgs), "content": content, "usage": usage}
	}
	n := 0
	for i, st := range t.Steps {
		if interrupted() {
			return f.interrupted()
		}
		switch {
		case st.Die:
			fmt.Fprintln(os.Stderr, "fake claude crashed mid-turn")
			os.Exit(3)
		case st.SleepMs > 0:
			if sleep(st.SleepMs) {
				return f.interrupted()
			}
		case st.Emit != "":
			f.emitRaw(repl(st.Emit))
		case st.Text != "":
			n++
			f.emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_start", "index": i, "content_block": map[string]any{"type": "text"}}, "parent_tool_use_id": nil, "session_id": f.session})
			parts := chunks(repl(st.Text), 3)
			for _, p := range parts {
				f.emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_delta", "index": i, "delta": map[string]any{"type": "text_delta", "text": p}}, "parent_tool_use_id": nil, "session_id": f.session})
				if sleep(15) {
					return f.interrupted()
				}
			}
			text := repl(st.Text)
			f.emit(map[string]any{"type": "assistant", "message": msg([]map[string]any{{"type": "text", "text": text}}), "parent_tool_use_id": nil, "session_id": f.session})
			f.emit(map[string]any{"type": "stream_event", "event": map[string]any{"type": "content_block_stop", "index": i}, "parent_tool_use_id": nil, "session_id": f.session})
			f.keep("assistant", text)
		case st.Write != nil:
			n++
			path := filepath.Join(proposals, st.Write.Name) // may name a subfolder (motion/<name>/index.tsx)
			_ = os.MkdirAll(filepath.Dir(path), 0o700)
			content := repl(st.Write.Content)
			_ = os.WriteFile(path, []byte(content), 0o600)
			id := fmt.Sprintf("toolu_w%d", i)
			f.emit(map[string]any{"type": "assistant", "message": msg([]map[string]any{{"type": "tool_use", "id": id, "name": "Write", "input": map[string]any{"file_path": path, "content": content}}}), "parent_tool_use_id": nil, "session_id": f.session})
			f.emit(map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "content": "File created successfully"}}}, "parent_tool_use_id": nil, "session_id": f.session})
		case st.Tool != nil:
			n++
			id := st.Tool.ID
			if id == "" {
				id = fmt.Sprintf("toolu_%d_%d", os.Getpid(), i)
			}
			in := map[string]any{}
			for k, v := range st.Tool.Input {
				if s, ok := v.(string); ok {
					v = repl(s)
				}
				in[k] = v
			}
			f.emit(map[string]any{"type": "assistant", "message": msg([]map[string]any{{"type": "tool_use", "id": id, "name": st.Tool.Name, "input": in}}), "parent_tool_use_id": nil, "session_id": f.session})
			if st.Tool.Denied {
				f.emit(map[string]any{"type": "system", "subtype": "permission_denied", "tool_name": st.Tool.Name, "tool_use_id": id, "decision_reason_type": "mode", "message": "Permission to use " + st.Tool.Name + " has been denied"})
				f.emit(map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "is_error": true,
					"content": "Permission to use " + st.Tool.Name + " has been denied because Claude Code is running in don't ask mode.\nIMPORTANT: do not retry."}}}, "parent_tool_use_id": nil, "session_id": f.session})
			} else {
				f.emit(map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{{"type": "tool_result", "tool_use_id": id, "is_error": st.Tool.IsError, "content": repl(st.Tool.Result)}}}, "parent_tool_use_id": nil, "session_id": f.session})
			}
		}
	}
	if interrupted() {
		return f.interrupted()
	}
	f.spent += t.CostUSD
	stt.Cost += t.CostUSD
	f.saveState(stt)
	if f.budget > 0 && f.spent > f.budget+1e-9 && t.Subtype == "" {
		t.Subtype, t.IsError = "error_max_budget_usd", true
	}
	f.emit(f.result(t, stt.Cost, n, stt, turnUse))
	return t.ExitAfter
}

// remember reads what the session file holds before this message (claude
// mode) — how many messages and the first one — and appends the message.
func (f *fake) remember(text string) (int, string) {
	if f.sessionFile == "" {
		return 0, ""
	}
	n, first := 0, ""
	if b, err := os.ReadFile(f.sessionFile); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			var l SessionLine
			if json.Unmarshal([]byte(line), &l) == nil && l.Type == "user" {
				if n == 0 {
					first = l.Text
				}
				n++
			}
		}
	}
	if r := []rune(first); len(r) > 40 {
		first = string(r[:40])
	}
	f.keep("user", text)
	return n, first
}

// keep appends one line to the session file (claude mode).
func (f *fake) keep(kind, text string) {
	if f.sessionFile == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(f.sessionFile), 0o700)
	b, _ := json.Marshal(SessionLine{Type: kind, SessionID: f.session, Cwd: f.cwd, Text: text})
	if lf, err := os.OpenFile(f.sessionFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		lf.Write(append(b, '\n'))
		lf.Close()
	}
}

// interruptedResult is the result of a turn stopped midway: its replies so far are in the totals.
func (f *fake) interruptedResult() map[string]any {
	st := f.loadState()
	return f.result(Turn{Subtype: "error_during_execution", IsError: true}, st.Cost, 0, st, map[string]int64{})
}

func (f *fake) interrupted() bool {
	f.emit(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "request_id": "interrupt", "response": map[string]any{"still_queued": []any{}}}})
	f.emit(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": "[Request interrupted by user]"}}}, "parent_tool_use_id": nil, "session_id": f.session})
	f.emit(f.interruptedResult())
	return false
}

func chunks(s string, n int) []string {
	r := []rune(s)
	if len(r) < n {
		return []string{s}
	}
	var out []string
	size := (len(r) + n - 1) / n
	for i := 0; i < len(r); i += size {
		j := i + size
		if j > len(r) {
			j = len(r)
		}
		out = append(out, string(r[i:j]))
	}
	return out
}
