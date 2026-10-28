package assistant

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"golang.org/x/text/unicode/norm"

	"github.com/metavert-io/metarush/internal/app"
)

// Where Claude Code keeps a conversation's session (item 34, D345): its
// configuration folder is $CLAUDE_CONFIG_DIR when that is set, else .claude
// in the home folder ($HOME); sessions are
// <config>/projects/<the working folder's key>/<session>.jsonl, and a
// session's own files (tool results, helpers' transcripts) sit in a folder of
// the same name beside it. MetaRush computes these exactly as Claude Code
// does, from the same environment it gives Claude Code (childEnv passes HOME
// and CLAUDE_CONFIG_DIR through), so a session put there is the one
// `claude --resume` finds.

// claudeKeyMax is how long a working folder's key may be before Claude Code
// shortens it and adds a hash.
const claudeKeyMax = 200

// ClaudeConfigDir is Claude Code's configuration folder for an environment
// (getenv: os.Getenv, or a stand-in): CLAUDE_CONFIG_DIR when set (Claude
// Code ignores it when it is blank), else <HOME>/.claude; Unicode NFC like
// Claude Code. Under go test it must come from CLAUDE_CONFIG_DIR, so no test
// ever reaches the real ~/.claude.
func ClaudeConfigDir(getenv func(string) string) (string, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	if d := strings.TrimSpace(getenv("CLAUDE_CONFIG_DIR")); d != "" {
		if !filepath.IsAbs(d) {
			return "", errors.New("CLAUDE_CONFIG_DIR is not a full path")
		}
		return norm.NFC.String(d), nil
	}
	if testing.Testing() {
		return "", errors.New("tests set CLAUDE_CONFIG_DIR: Claude Code's own folder in the home folder is never used under go test")
	}
	h := getenv("HOME")
	if h == "" {
		var err error
		if h, err = os.UserHomeDir(); err != nil {
			return "", err
		}
	}
	return norm.NFC.String(filepath.Join(h, ".claude")), nil
}

// ClaudeProjectKey is the name Claude Code gives a working folder's sessions
// folder: every UTF-16 unit that isn't an ASCII letter or digit becomes "-";
// a key longer than 200 is cut there and gets "-" and the base-36 of the
// absolute 32-bit string hash (Java's) of the whole path.
func ClaudeProjectKey(cwd string) string {
	units := utf16.Encode([]rune(cwd))
	var b strings.Builder
	b.Grow(len(units))
	for _, u := range units {
		if u < 128 && (u >= 'a' && u <= 'z' || u >= 'A' && u <= 'Z' || u >= '0' && u <= '9') {
			b.WriteByte(byte(u))
		} else {
			b.WriteByte('-')
		}
	}
	key := b.String()
	if len(key) <= claudeKeyMax {
		return key
	}
	var h int32
	for _, u := range units {
		h = (h << 5) - h + int32(u)
	}
	abs := int64(h)
	if abs < 0 {
		abs = -abs
	}
	return key[:claudeKeyMax] + "-" + strconv.FormatInt(abs, 36)
}

// SessionPlace is where Claude Code keeps one session of a working folder.
type SessionPlace struct {
	// File is the session's history (<session>.jsonl); Dir its own folder of
	// files beside it (often absent).
	File string
	Dir  string
}

// ClaudeSessionPlace is where Claude Code keeps session id of working folder
// cwd (a real path: Claude Code reads its working folder from the system).
func ClaudeSessionPlace(getenv func(string) string, cwd, id string) (SessionPlace, error) {
	if !validSessionID(id) {
		return SessionPlace{}, errors.New("not a session ID")
	}
	cfg, err := ClaudeConfigDir(getenv)
	if err != nil {
		return SessionPlace{}, err
	}
	dir := filepath.Join(cfg, "projects", ClaudeProjectKey(cwd))
	return SessionPlace{File: filepath.Join(dir, id+".jsonl"), Dir: filepath.Join(dir, id)}, nil
}

// validSessionID: a session ID MetaRush gave Claude Code (a UUID) or one the
// fake uses; never a path.
func validSessionID(id string) bool {
	if id == "" || len(id) > 80 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// ValidSessionID reports whether id can name a session file.
func ValidSessionID(id string) bool { return validSessionID(id) }

// WorkingFolder is the folder this installation's Claude Code works in, as
// Claude Code sees it — its getcwd(), the file system's own spelling (made
// when missing, so it is known).
func (m *Manager) WorkingFolder() (string, error) {
	if err := os.MkdirAll(m.workspaceDir(), 0o700); err != nil {
		return "", err
	}
	return onDiskPath(m.workspaceDir()), nil
}

// SessionPlaceOf is where Claude Code on this computer keeps a session of
// this installation's working folder, by this process's environment (the
// one it gives the Claude Code it starts).
func (m *Manager) SessionPlaceOf(id string) (SessionPlace, error) {
	cwd, err := m.WorkingFolder()
	if err != nil {
		return SessionPlace{}, err
	}
	return ClaudeSessionPlace(os.Getenv, cwd, id)
}

/* ---------- The background MetaRush's Claude Code ---------- */

// claudeRecord is where the background MetaRush says its Claude Code keeps
// its things (<home>/state/claude-code.json). A terminal's environment may
// differ from the background service's (launchd gives it PATH and little
// else), so a handover places and finds sessions by this record (D345).
type claudeRecord struct {
	ConfigDir string    `json:"configDir"`
	At        time.Time `json:"at"`
}

func claudeRecordPath(a *app.App) string { return filepath.Join(a.Cfg.StateDir(), "claude-code.json") }

// RecordClaudeConfig notes the configuration folder the Claude Code this
// process starts uses; the background MetaRush calls it.
func RecordClaudeConfig(a *app.App) {
	dir, err := ClaudeConfigDir(os.Getenv)
	if err != nil || a.Cfg.Home() == "" {
		return
	}
	p := claudeRecordPath(a)
	var old claudeRecord
	if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &old) == nil && old.ConfigDir == dir {
		return
	}
	b, _ := json.Marshal(claudeRecord{ConfigDir: dir, At: time.Now().UTC()})
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// ClaudeConfigFor is the configuration folder of the Claude Code the
// background MetaRush starts on this computer: as it recorded, else this
// process's own.
func ClaudeConfigFor(a *app.App) (string, error) {
	var r claudeRecord
	if b, err := os.ReadFile(claudeRecordPath(a)); err == nil && json.Unmarshal(b, &r) == nil && filepath.IsAbs(r.ConfigDir) {
		return r.ConfigDir, nil
	}
	return ClaudeConfigDir(os.Getenv)
}

// claudeConfigs are the configuration folders a session of this computer
// may be in, the background MetaRush's first: as recorded, this process's,
// and (outside go test) the home folder's own.
func claudeConfigs(a *app.App) []string {
	var out []string
	add := func(d string, err error) {
		if err == nil && d != "" && !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	add(ClaudeConfigFor(a))
	add(ClaudeConfigDir(os.Getenv))
	if !testing.Testing() {
		if h, err := os.UserHomeDir(); err == nil {
			add(norm.NFC.String(filepath.Join(h, ".claude")), nil)
		}
	}
	return out
}

// findSession looks for a session by its ID in every projects folder of the
// configuration folders this computer's sessions may be in (a working
// folder spelled another way, or another configuration folder).
func findSession(dirs []string, id string) (SessionPlace, bool) {
	if !validSessionID(id) {
		return SessionPlace{}, false
	}
	for _, cfg := range dirs {
		entries, err := os.ReadDir(filepath.Join(cfg, "projects"))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			base := filepath.Join(cfg, "projects", e.Name(), id)
			if st, err := os.Stat(base + ".jsonl"); err == nil && st.Mode().IsRegular() {
				return SessionPlace{File: base + ".jsonl", Dir: base}, true
			}
		}
	}
	return SessionPlace{}, false
}
