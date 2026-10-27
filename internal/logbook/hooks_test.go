package logbook

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// A user's existing Claude Code settings, with hooks of their own.
const userSettings = `{
  "model": "opus",
  "permissions": {"allow": ["Bash(git status)"]},
  "hooks": {
    "PostToolUse": [
      {"matcher": "Edit|Write", "hooks": [{"type": "command", "command": "prettier --write \"$FILE\""}]}
    ],
    "Stop": [
      {"hooks": [{"type": "command", "command": "say done"}]}
    ]
  },
  "statusLine": {"type": "command", "command": "~/bin/status"}
}
`

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("settings are not JSON: %v\n%s", err, b)
	}
	return m
}

// The installer merges into the user's settings without disturbing anything
// else, keeps a backup, is idempotent, and disable removes only its own hooks.
// Everything happens under a temporary CLAUDE_CONFIG_DIR — never the real one.
func TestHookInstaller(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	p, err := ClaudeSettingsPath()
	if err != nil || p != filepath.Join(dir, "settings.json") {
		t.Fatalf("settings path %q %v", p, err)
	}
	if err := os.WriteFile(p, []byte(userSettings), 0o644); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(t.TempDir(), "my installation")
	exe := "/Applications/Meta Rush/bin/metarush" // spaces must survive quoting
	ch, err := EnableHooks(p, exe, home)
	if err != nil {
		t.Fatal(err)
	}
	if !ch.Changed || len(ch.Added) != len(HookEvents) || ch.Backup == "" {
		t.Fatalf("enable: %+v", ch)
	}
	if b, _ := os.ReadFile(ch.Backup); string(b) != userSettings {
		t.Fatal("the backup is not the original file")
	}
	if st, _ := os.Stat(p); st.Mode().Perm() != 0o644 {
		t.Fatalf("mode changed to %v", st.Mode().Perm())
	}
	after, _ := os.ReadFile(p)
	m := decode(t, after)
	orig := decode(t, []byte(userSettings))
	for _, k := range []string{"model", "permissions", "statusLine"} {
		if !reflect.DeepEqual(m[k], orig[k]) {
			t.Fatalf("%s changed: %v", k, m[k])
		}
	}
	if !strings.HasPrefix(string(after), "{\n  \"model\"") {
		t.Fatalf("key order changed:\n%s", after)
	}
	hooks := m["hooks"].(map[string]any)
	post := hooks["PostToolUse"].([]any)
	if len(post) != 2 || !reflect.DeepEqual(post[0], orig["hooks"].(map[string]any)["PostToolUse"].([]any)[0]) {
		t.Fatalf("the user's PostToolUse hook was disturbed: %v", post)
	}
	ours := post[1].(map[string]any)
	h := ours["hooks"].([]any)[0].(map[string]any)
	if ours["matcher"] != "Bash|Skill" || h["type"] != "command" || h["timeout"].(float64) != hookTimeout ||
		h["command"] != `'/Applications/Meta Rush/bin/metarush' --home '`+home+`' log hook PostToolUse` {
		t.Fatalf("our hook: %v", ours)
	}
	if stop := hooks["Stop"].([]any); len(stop) != 2 {
		t.Fatalf("Stop hooks: %v", stop)
	}
	st := HooksStatus(p, home)
	if !st.Enabled || st.OtherHome || st.Binary != exe || st.Home != home || len(st.Events) != len(HookEvents) {
		t.Fatalf("status: %+v", st)
	}
	if st := HooksStatus(p, "/somewhere/else"); !st.OtherHome {
		t.Fatalf("other installation not noticed: %+v", st)
	}
	// Idempotent.
	again, err := EnableHooks(p, exe, home)
	if err != nil || again.Changed || again.Backup != "" {
		t.Fatalf("second enable changed things: %+v %v", again, err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(after) {
		t.Fatal("second enable rewrote the file")
	}
	// A new binary location replaces the old hooks instead of adding more.
	moved, err := EnableHooks(p, "/usr/local/bin/metarush", home)
	if err != nil || len(moved.Updated) != len(HookEvents) || len(moved.Added) != 0 {
		t.Fatalf("moved binary: %+v %v", moved, err)
	}
	if n := strings.Count(mustRead(t, p), " log hook "); n != len(HookEvents) {
		t.Fatalf("%d MetaRush hooks after moving the binary", n)
	}
	// Disable removes only MetaRush's hooks.
	off, err := DisableHooks(p)
	if err != nil || len(off.Removed) != len(HookEvents) {
		t.Fatalf("disable: %+v %v", off, err)
	}
	if !reflect.DeepEqual(decode(t, []byte(mustRead(t, p))), orig) {
		t.Fatalf("disable left the settings different from the original:\n%s", mustRead(t, p))
	}
	if st := HooksStatus(p, home); st.Enabled || st.Partial {
		t.Fatalf("status after disable: %+v", st)
	}
	// Disabling again changes nothing; invalid JSON is never touched.
	if off2, err := DisableHooks(p); err != nil || off2.Changed {
		t.Fatalf("second disable: %+v %v", off2, err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnableHooks(p, exe, home); err == nil || mustRead(t, p) != "{not json" {
		t.Fatalf("invalid settings were changed: %v", err)
	}
	// No settings file yet: one is created.
	fresh := filepath.Join(t.TempDir(), "new", "settings.json")
	if ch, err := EnableHooks(fresh, exe, home); err != nil || !ch.Changed || ch.Backup != "" {
		t.Fatalf("fresh settings: %+v %v", ch, err)
	}
	if st := HooksStatus(fresh, home); !st.Enabled {
		t.Fatalf("fresh status: %+v", st)
	}
}

func mustRead(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hookInputJSON(event, session string, extra map[string]any) string {
	m := map[string]any{"session_id": session, "transcript_path": "", "cwd": "/tmp/x", "hook_event_name": event}
	for k, v := range extra {
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func runHook(home, event, session string, extra map[string]any) {
	RunHook(home, event, strings.NewReader(hookInputJSON(event, session, extra)))
}

// Only sessions that used MetaRush are recorded; the others are discarded at
// the end of each turn. Recorded turns become log entries (claude-terminal).
func TestHookRecordsOnlyMetaRushSessions(t *testing.T) {
	e := newEnv(t, true)
	home := e.home
	if err := os.MkdirAll(filepath.Join(e.cfg.ClaudeHooksDir(), "sessions"), 0o700); err != nil { // hooks turned on
		t.Fatal(err)
	}
	// A session that never touches MetaRush.
	runHook(home, "UserPromptSubmit", "sess-other", map[string]any{"prompt": "refactor my parser"})
	runHook(home, "PostToolUse", "sess-other", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "go test ./..."}})
	runHook(home, "Stop", "sess-other", map[string]any{"last_assistant_message": "done refactoring"})
	// A MetaRush session: prompt, a metarush command, a skill, then the reply.
	runHook(home, "UserPromptSubmit", "sess-mr", map[string]any{"prompt": "list my video projects"})
	runHook(home, "PostToolUse", "sess-mr", map[string]any{"tool_name": "Skill", "tool_input": map[string]any{"skill": "video-project"}})
	runHook(home, "PostToolUse", "sess-mr", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "metarush project list --json"}, "duration_ms": 120})
	runHook(home, "PostToolUseFailure", "sess-mr", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "./bin/metarush render submit --revision rev_x"}, "error": "exit 3"})
	runHook(home, "Stop", "sess-mr", map[string]any{"last_assistant_message": "You have two projects."})
	// Later turns of a MetaRush session are recorded even without commands.
	runHook(home, "UserPromptSubmit", "sess-mr", map[string]any{"prompt": "thanks, and what about my calendar?"})
	runHook(home, "Stop", "sess-mr", map[string]any{"last_assistant_message": "I can't see calendars."})
	// A session marked by the metarush CLI itself (a wrapper script ran it).
	markSessionRelevant(e.cfg.ClaudeHooksDir(), "sess-cli")
	runHook(home, "UserPromptSubmit", "sess-cli", map[string]any{"prompt": "run my script"})
	runHook(home, "Stop", "sess-cli", map[string]any{"last_assistant_message": "ran it"})
	runHook(home, "SessionEnd", "sess-cli", map[string]any{"reason": "prompt_input_exit"})

	ready, _ := filepath.Glob(filepath.Join(e.cfg.ClaudeHooksDir(), "ready", "*.json"))
	if len(ready) != 3 {
		t.Fatalf("finished turns: %d (%v)", len(ready), ready)
	}
	if _, err := os.Stat(filepath.Join(e.cfg.ClaudeHooksDir(), "sessions", "sess-other.jsonl")); !os.IsNotExist(err) {
		t.Fatal("the other session's prompts were kept")
	}
	for _, f := range ready {
		if strings.Contains(mustRead(t, f), "refactor") {
			t.Fatal("a non-MetaRush prompt was recorded")
		}
	}
	if left, _ := filepath.Glob(filepath.Join(e.cfg.ClaudeHooksDir(), "sessions", "sess-cli*")); len(left) > 1 { // the lock file may stay
		t.Fatalf("SessionEnd left files: %v", left)
	}
	ctx := context.Background()
	l := e.open(t, e.ms)
	if n := l.IngestHookTurns(ctx); n != 3 {
		t.Fatalf("ingested %d turns", n)
	}
	l.Flush(ctx)
	turns := e.entries(t, bson.M{"kind": KindClaude})
	if len(turns) != 3 {
		t.Fatalf("turn entries: %d", len(turns))
	}
	var first *Entry
	for i := range turns {
		if turns[i].Refs.ConversationID == "sess-mr" && turns[i].Claude.Turn == 1 {
			first = &turns[i]
		}
	}
	if first == nil {
		t.Fatalf("no first turn of sess-mr: %+v", turns)
	}
	c := first.Claude
	if first.Channel != ChannelClaudeTerminal || c.Source != SourceClaudeTerminal || c.Prompt != "list my video projects" || c.Response != "You have two projects." {
		t.Fatalf("turn: %+v %+v", first, c)
	}
	if len(c.Tools) != 3 || c.Tools[0].Name != "Skill" || !c.Tools[2].IsError || c.Tools[2].Result != "exit 3" {
		t.Fatalf("commands: %+v", c.Tools)
	}
	if ready, _ := filepath.Glob(filepath.Join(e.cfg.ClaudeHooksDir(), "ready", "*")); len(ready) != 0 {
		t.Fatalf("ready files left: %v", ready)
	}
}

// The hook redacts before it writes, never prints, and is fast; parallel
// hooks of one session lose nothing.
// awsID looks like an AWS access key ID (put together at run time for the secret scan).
var awsID = "AKIA" + "IOSFODNN7EXAMPLE"

func TestHookIsSafeFastAndConcurrent(t *testing.T) {
	e := newEnv(t, false)
	home := e.home
	dir := e.cfg.ClaudeHooksDir()
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	r := NewRedactor()
	r.addConfigured("keychain-only-secret-42")
	if err := WriteFingerprints(dir, r); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("METARUSH_ELEVENLABS_API_KEY=dotenv-secret-value-77\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	runHook(home, "UserPromptSubmit", "s1", map[string]any{"prompt": "use keychain-only-secret-42 and dotenv-secret-value-77 and " + awsID})
	if d := time.Since(start); d > 50*time.Millisecond {
		t.Errorf("hook took %v (target < 50 ms)", d)
	}
	buf := mustRead(t, filepath.Join(dir, "sessions", "s1.jsonl"))
	for _, s := range []string{"keychain-only-secret-42", "dotenv-secret-value-77", awsID} {
		if strings.Contains(buf, s) {
			t.Fatalf("the hook buffer holds %s: %s", s, buf)
		}
	}
	// Twenty tool calls finishing at once.
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			runHook(home, "PostToolUse", "s1", map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": fmt.Sprintf("metarush asset show ast_%d --json", i)}})
		}(i)
	}
	wg.Wait()
	lines, err := readBuffer(filepath.Join(dir, "sessions", "s1.jsonl"))
	if err != nil || len(lines) != 21 {
		t.Fatalf("buffer lines: %d %v", len(lines), err)
	}
	// Garbage and unknown events are ignored silently.
	RunHook(home, "UserPromptSubmit", strings.NewReader("not json"))
	RunHook(home, "SomethingNew", strings.NewReader(hookInputJSON("SomethingNew", "s1", nil)))
	if b, err := os.ReadFile(filepath.Join(dir, "errors.log")); err != nil || !strings.Contains(string(b), "unreadable input") {
		t.Fatalf("errors.log: %q %v", b, err)
	}
	// With the hooks turned off for this installation, nothing is written.
	other := t.TempDir()
	runHook(other, "UserPromptSubmit", "s2", map[string]any{"prompt": "hello"})
	if _, err := os.Stat(filepath.Join(other, "state")); !os.IsNotExist(err) {
		t.Fatal("the hook wrote into an installation that did not turn it on")
	}
}

func TestMetarushUseDetection(t *testing.T) {
	for cmd, want := range map[string]bool{"metarush project list --json": true, "./bin/metarush asset show x": true, "cd /tmp && metarush log list": true,
		"/usr/local/bin/metarush version": true, "echo metarush-ish": false, "grep metarushes file": false, "go test ./...": false, "(metarush doctor)": true} {
		in, _ := json.Marshal(map[string]any{"command": cmd})
		if _, got := metarushUse("Bash", in); got != want {
			t.Errorf("%q: %v", cmd, got)
		}
	}
	for skill, want := range map[string]bool{"video-render": true, "metarush:video-qa": true, "metarush-improve": true, "pdf": false} {
		in, _ := json.Marshal(map[string]any{"skill": skill})
		if _, got := metarushUse("Skill", in); got != want {
			t.Errorf("skill %q: %v", skill, got)
		}
	}
}
