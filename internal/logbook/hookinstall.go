package logbook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude Code hooks (opt-in): MetaRush adds command hooks to Claude Code's user
// settings so terminal sessions that use MetaRush are recorded. The hooks run in
// every Claude Code session on the machine, but only sessions that ran a
// metarush command or a MetaRush skill are recorded; the others are discarded at
// the end of each turn. Verified against Claude Code 2.1 (hooks documentation
// and the installed binary): settings.json "hooks" → event → [{matcher, hooks:
// [{type: "command", command, timeout}]}]; input on stdin with session_id,
// transcript_path, cwd, hook_event_name and per event prompt (UserPromptSubmit),
// tool_name/tool_input (PostToolUse, PostToolUseFailure with error),
// last_assistant_message (Stop) and reason (SessionEnd).

// HookEvent is one Claude Code event MetaRush listens to.
type HookEvent struct {
	Event   string
	Matcher string // tool names for PostToolUse; "" = every occurrence
}

// HookEvents are the events the installer adds.
var HookEvents = []HookEvent{
	{Event: "UserPromptSubmit"},
	{Event: "PostToolUse", Matcher: "Bash|Skill"},
	{Event: "PostToolUseFailure", Matcher: "Bash|Skill"},
	{Event: "Stop"},
	{Event: "SessionEnd"},
}

// hookTimeout is the per-hook limit given to Claude Code (seconds). The hook
// itself finishes in milliseconds and gives up on its own after two seconds.
const hookTimeout = 10

// ClaudeSettingsPath is Claude Code's user settings file:
// $CLAUDE_CONFIG_DIR/settings.json, else ~/.claude/settings.json.
func ClaudeSettingsPath() (string, error) {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Join(d, "settings.json"), nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".claude", "settings.json"), nil
}

// shQuote quotes s for a POSIX shell (Claude Code runs command hooks with the
// user's shell).
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'" }

// HookCommand is the command a hook runs for event.
func HookCommand(exe, home, event string) string {
	return shQuote(exe) + " --home " + shQuote(home) + " log hook " + event
}

// isOurs recognizes a MetaRush hook command (any binary path or home).
func isOurs(cmd string) bool {
	return strings.Contains(cmd, " log hook ") && strings.Contains(cmd, "metarush")
}

// HookChange reports what enable or disable did.
type HookChange struct {
	SettingsFile string   `json:"settingsFile"`
	Backup       string   `json:"backup,omitempty" jsonschema:"description=Copy of the settings file before the change."`
	Added        []string `json:"added"`
	Updated      []string `json:"updated"`
	Removed      []string `json:"removed"`
	Changed      bool     `json:"changed"`
}

// HookStatus says whether MetaRush's hooks are installed.
type HookStatus struct {
	Enabled      bool     `json:"enabled" jsonschema:"description=Every MetaRush hook is installed."`
	Partial      bool     `json:"partial" jsonschema:"description=Some hooks are installed\\, not all."`
	Events       []string `json:"events"`
	SettingsFile string   `json:"settingsFile"`
	Binary       string   `json:"binary,omitempty" jsonschema:"description=The metarush program the hooks run."`
	BinaryOK     bool     `json:"binaryOk" jsonschema:"description=That program exists and can run."`
	Home         string   `json:"home,omitempty" jsonschema:"description=The installation the hooks record into."`
	OtherHome    bool     `json:"otherHome" jsonschema:"description=The hooks record into a different installation than this one."`
	Waiting      int      `json:"waiting" jsonschema:"description=Finished turns waiting to be written to the log."`
	Error        string   `json:"error,omitempty"`
}

// ordered is a JSON object that keeps its keys in order and its values as raw
// JSON, so editing one key leaves every other entry exactly as it was.
type ordered struct {
	keys []string
	vals map[string]json.RawMessage
}

func parseOrdered(b []byte) (*ordered, error) {
	o := &ordered{vals: map[string]json.RawMessage{}}
	if len(bytes.TrimSpace(b)) == 0 {
		return o, nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, errors.New("not a JSON object")
	}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, ok := kt.(string)
		if !ok {
			return nil, errors.New("bad key")
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if _, dup := o.vals[k]; !dup {
			o.keys = append(o.keys, k)
		}
		o.vals[k] = raw
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *ordered) get(k string) (json.RawMessage, bool) { v, ok := o.vals[k]; return v, ok }

func (o *ordered) set(k string, v json.RawMessage) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

func (o *ordered) del(k string) {
	if _, ok := o.vals[k]; !ok {
		return
	}
	delete(o.vals, k)
	for i, x := range o.keys {
		if x == k {
			o.keys = append(o.keys[:i], o.keys[i+1:]...)
			break
		}
	}
}

func (o *ordered) marshal() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := json.Marshal(k)
		b.Write(kb)
		b.WriteByte(':')
		b.Write(o.vals[k])
	}
	b.WriteByte('}')
	return b.Bytes()
}

func handlerCommand(raw json.RawMessage) string {
	var h struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	}
	if json.Unmarshal(raw, &h) != nil || h.Type != "command" {
		return ""
	}
	return h.Command
}

func groupMatcher(g *ordered) string {
	raw, ok := g.get("matcher")
	if !ok {
		return ""
	}
	var m string
	_ = json.Unmarshal(raw, &m)
	return m
}

// SettingsError is a Claude Code settings file MetaRush did not change: Plain
// says so for people, Err is the technical reason (for Details).
type SettingsError struct {
	Plain string
	Err   error
}

func (e *SettingsError) Error() string { return e.Plain + " (" + e.Err.Error() + ")" }
func (e *SettingsError) Unwrap() error { return e.Err }

// unreadableSettings is the plain reason for a settings file MetaRush cannot read.
const unreadableSettings = "MetaRush couldn't read Claude Code's settings file, so it left it unchanged. Fix the file or move it aside, then try again."

// editHooks rewrites the MetaRush handlers in a settings document: want maps an
// event to the handler to install (nil removes MetaRush's handlers).
func editHooks(doc []byte, want map[string]json.RawMessage) ([]byte, *HookChange, error) {
	root, err := parseOrdered(doc)
	if err != nil {
		return nil, nil, &SettingsError{Plain: unreadableSettings, Err: fmt.Errorf("the Claude Code settings file is not valid JSON: %w", err)}
	}
	ch := &HookChange{Added: []string{}, Updated: []string{}, Removed: []string{}}
	hooks := &ordered{vals: map[string]json.RawMessage{}}
	if raw, ok := root.get("hooks"); ok && string(bytes.TrimSpace(raw)) != "null" {
		if hooks, err = parseOrdered(raw); err != nil {
			return nil, nil, &SettingsError{Plain: unreadableSettings, Err: fmt.Errorf("the \"hooks\" setting is not a JSON object: %w", err)}
		}
	}
	for _, he := range HookEvents {
		desired := want[he.Event]
		var groups []json.RawMessage
		if raw, ok := hooks.get(he.Event); ok {
			if err := json.Unmarshal(raw, &groups); err != nil {
				return nil, nil, &SettingsError{Plain: unreadableSettings, Err: fmt.Errorf("the %s hooks are not a list: %w", he.Event, err)}
			}
		}
		placed := false
		var out []json.RawMessage
		for _, graw := range groups {
			g, err := parseOrdered(graw)
			if err != nil {
				out = append(out, graw)
				continue
			}
			hraw, ok := g.get("hooks")
			var handlers []json.RawMessage
			if !ok || json.Unmarshal(hraw, &handlers) != nil {
				out = append(out, graw)
				continue
			}
			var keep []json.RawMessage
			hadOurs := false
			for _, h := range handlers {
				if !isOurs(handlerCommand(h)) {
					keep = append(keep, h)
					continue
				}
				hadOurs = true
				switch {
				case desired != nil && !placed && groupMatcher(g) == he.Matcher:
					placed = true
					if !jsonEqual(h, desired) {
						ch.Updated = append(ch.Updated, he.Event)
					}
					keep = append(keep, desired)
				default:
					ch.Removed = append(ch.Removed, he.Event)
				}
			}
			if !hadOurs {
				out = append(out, graw)
				continue
			}
			if len(keep) == 0 {
				continue // the group only held MetaRush's hook
			}
			hb, _ := json.Marshal(keep)
			g.set("hooks", hb)
			out = append(out, g.marshal())
		}
		if desired != nil && !placed {
			g := &ordered{vals: map[string]json.RawMessage{}}
			if he.Matcher != "" {
				mb, _ := json.Marshal(he.Matcher)
				g.set("matcher", mb)
			}
			hb, _ := json.Marshal([]json.RawMessage{desired})
			g.set("hooks", hb)
			out = append(out, g.marshal())
			ch.Added = append(ch.Added, he.Event)
		}
		switch {
		case len(out) > 0:
			b, _ := json.Marshal(out)
			hooks.set(he.Event, b)
		case len(groups) > 0:
			hooks.del(he.Event) // it held only MetaRush's hooks
		}
	}
	ch.Changed = len(ch.Added)+len(ch.Updated)+len(ch.Removed) > 0
	if !ch.Changed {
		return doc, ch, nil
	}
	if len(hooks.keys) == 0 {
		root.del("hooks")
	} else {
		root.set("hooks", hooks.marshal())
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, root.marshal(), "", "  "); err != nil {
		return nil, nil, err
	}
	pretty.WriteByte('\n')
	return pretty.Bytes(), ch, nil
}

func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return bytes.Equal(xa, ya)
}

func handlerJSON(exe, home, event string) json.RawMessage {
	b, _ := json.Marshal(struct {
		Type    string `json:"type"`
		Command string `json:"command"`
		Timeout int    `json:"timeout"`
	}{"command", HookCommand(exe, home, event), hookTimeout})
	return b
}

// EnableHooks adds MetaRush's hooks to a Claude Code settings file (creating it
// if needed), leaving every other setting as it is. It is idempotent, replaces
// MetaRush hooks that point at another binary or installation, and keeps a
// backup of the previous file next to it.
func EnableHooks(settingsFile, exe, home string) (*HookChange, error) {
	if !filepath.IsAbs(exe) || !filepath.IsAbs(home) {
		return nil, errors.New("the program and installation paths must be absolute")
	}
	want := map[string]json.RawMessage{}
	for _, he := range HookEvents {
		want[he.Event] = handlerJSON(exe, home, he.Event)
	}
	ch, err := rewriteSettings(settingsFile, want)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(home, "state", "claude-hooks", "sessions"), 0o700); err != nil {
		return ch, err
	}
	return ch, nil
}

// DisableHooks removes MetaRush's hooks (and nothing else) from a settings file.
func DisableHooks(settingsFile string) (*HookChange, error) {
	return rewriteSettings(settingsFile, map[string]json.RawMessage{})
}

func rewriteSettings(settingsFile string, want map[string]json.RawMessage) (*HookChange, error) {
	cur, err := os.ReadFile(settingsFile)
	exists := err == nil
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	next, ch, err := editHooks(cur, want)
	if err != nil {
		return nil, err
	}
	ch.SettingsFile = settingsFile
	if !ch.Changed {
		return ch, nil
	}
	mode := os.FileMode(0o600)
	if exists {
		if st, err := os.Stat(settingsFile); err == nil {
			mode = st.Mode().Perm()
		}
		backup := settingsFile + ".metarush-backup-" + time.Now().Format("20060102-150405")
		if err := os.WriteFile(backup, cur, mode); err != nil {
			return nil, &SettingsError{Plain: "MetaRush couldn't keep a backup of Claude Code's settings file, so it left the file unchanged.",
				Err: fmt.Errorf("could not back up %s: %w", settingsFile, err)}
		}
		ch.Backup = backup
	} else if err := os.MkdirAll(filepath.Dir(settingsFile), 0o700); err != nil {
		return nil, err
	}
	tmp := settingsFile + ".metarush-tmp"
	if err := os.WriteFile(tmp, next, mode); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, settingsFile); err != nil {
		os.Remove(tmp)
		return nil, err
	}
	return ch, nil
}

// HooksStatus reads which MetaRush hooks a settings file has. exe and home are
// this installation's, to tell whether the hooks record here.
func HooksStatus(settingsFile, home string) HookStatus {
	st := HookStatus{SettingsFile: settingsFile, Events: []string{}}
	b, err := os.ReadFile(settingsFile)
	if err != nil {
		if !os.IsNotExist(err) {
			st.Error = err.Error()
		}
		return st
	}
	root, err := parseOrdered(b)
	if err != nil {
		st.Error = "MetaRush couldn't read Claude Code's settings file."
		return st
	}
	raw, ok := root.get("hooks")
	if !ok {
		return st
	}
	hooks, err := parseOrdered(raw)
	if err != nil {
		return st
	}
	for _, he := range HookEvents {
		var groups []struct {
			Hooks []json.RawMessage `json:"hooks"`
		}
		if r, ok := hooks.get(he.Event); !ok || json.Unmarshal(r, &groups) != nil {
			continue
		}
	found:
		for _, g := range groups {
			for _, h := range g.Hooks {
				cmd := handlerCommand(h)
				if !isOurs(cmd) {
					continue
				}
				st.Events = append(st.Events, he.Event)
				if st.Binary == "" {
					st.Binary, st.Home = parseHookCommand(cmd)
				}
				break found
			}
		}
	}
	st.Enabled = len(st.Events) == len(HookEvents)
	st.Partial = len(st.Events) > 0 && !st.Enabled
	if st.Binary != "" {
		if info, err := os.Stat(st.Binary); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			st.BinaryOK = true
		}
	}
	if st.Home != "" && home != "" {
		a, _ := filepath.Abs(st.Home)
		b, _ := filepath.Abs(home)
		st.OtherHome = a != b
	}
	if home != "" {
		ready, _ := filepath.Glob(filepath.Join(home, "state", "claude-hooks", "ready", "*.json"))
		st.Waiting = len(ready)
	}
	return st
}

// parseHookCommand reads the binary and home back from a hook command.
func parseHookCommand(cmd string) (exe, home string) {
	words := shellWords(cmd)
	if len(words) > 0 {
		exe = words[0]
	}
	for i := 0; i+1 < len(words); i++ {
		if words[i] == "--home" {
			home = words[i+1]
		}
	}
	return exe, home
}

// shellWords splits a command written by HookCommand (single quotes only).
func shellWords(s string) []string {
	var out []string
	var cur strings.Builder
	in, started := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			in = !in
			started = true
		case c == '"' && !in && i+2 < len(s) && s[i+1] == '\'' && s[i+2] == '"':
			cur.WriteByte('\'')
			i += 2
		case (c == ' ' || c == '\t') && !in:
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteByte(c)
			started = true
		}
	}
	if started {
		out = append(out, cur.String())
	}
	return out
}
