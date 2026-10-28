package assistant

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
)

// The keys are Claude Code's own (2.1.303: replace(/[^a-zA-Z0-9]/g,"-"),
// then past 200 the cut and the base-36 of the string hash), worked out by
// running its functions in Node; the first is a MetaRush home's own folder.
func TestClaudeProjectKeyIsClaudeCodes(t *testing.T) {
	for _, c := range []struct{ cwd, key string }{
		{"/Users/you/.metarush/assistant/workspace", "-Users-you--metarush-assistant-workspace"},
		{"/private/var/folders/x1/abc_def/T/TestHandover123/001/assistant/workspace", "-private-var-folders-x1-abc-def-T-TestHandover123-001-assistant-workspace"},
		{"/Users/you/Movies/MetaRush é/assistant/workspace", "-Users-you-Movies-MetaRush---assistant-workspace"},
		{"/Users/you/🎬 work/assistant/workspace", "-Users-you----work-assistant-workspace"},
		{"/tmp/" + strings.Repeat("a", 230) + "/assistant/workspace", "-tmp-" + strings.Repeat("a", 195) + "-kbjql8"},
		{"/Users/you/" + strings.Repeat("Ünïcode-", 30) + "/workspace", "-Users-you-" + strings.Repeat("-n-code-", 23) + "-n-co-v2smbe"},
	} {
		if got := ClaudeProjectKey(c.cwd); got != c.key {
			t.Errorf("key of %q\n got %q\nwant %q", c.cwd, got, c.key)
		}
	}
}

func TestClaudeConfigDirFollowsClaudeCode(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	d, err := ClaudeConfigDir(env(map[string]string{"CLAUDE_CONFIG_DIR": "/srv/claude-config", "HOME": "/Users/you"}))
	if err != nil || d != "/srv/claude-config" {
		t.Fatalf("CLAUDE_CONFIG_DIR wins: %q %v", d, err)
	}
	if _, err := ClaudeConfigDir(env(map[string]string{"CLAUDE_CONFIG_DIR": "relative/dir"})); err == nil {
		t.Fatal("a relative CLAUDE_CONFIG_DIR is refused")
	}
	// Under go test the home folder's own .claude is never used.
	if _, err := ClaudeConfigDir(env(map[string]string{"HOME": "/Users/you"})); err == nil {
		t.Fatal("tests must set CLAUDE_CONFIG_DIR")
	}
	p, err := ClaudeSessionPlace(env(map[string]string{"CLAUDE_CONFIG_DIR": "/c"}), "/Users/you/.metarush/assistant/workspace", "0b5c2d1e-1f2a-4b3c-8d4e-5f6a7b8c9d0e")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/c", "projects", "-Users-you--metarush-assistant-workspace", "0b5c2d1e-1f2a-4b3c-8d4e-5f6a7b8c9d0e")
	if p.File != want+".jsonl" || p.Dir != want {
		t.Fatalf("place %+v", p)
	}
	if _, err := ClaudeSessionPlace(env(map[string]string{"CLAUDE_CONFIG_DIR": "/c"}), "/w", "../../etc/passwd"); err == nil {
		t.Fatal("a session ID is never a path")
	}
}

// A handover run from a terminal places and finds sessions where the
// background MetaRush's Claude Code keeps them (it recorded where), whatever
// the terminal's own environment says; a session is found by its ID in any
// projects folder of a configuration folder this computer uses.
func TestSessionsFollowTheBackgroundClaudeCode(t *testing.T) {
	service, terminal := t.TempDir(), t.TempDir()
	a := testApp(t, "")
	t.Setenv("CLAUDE_CONFIG_DIR", service)
	RecordClaudeConfig(a) // what the background MetaRush does when it starts
	t.Setenv("CLAUDE_CONFIG_DIR", terminal)
	id := "0b5c2d1e-1f2a-4b3c-8d4e-5f6a7b8c9d0e"
	place, err := SessionPlaceFor(a, id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(place.File, filepath.Join(service, "projects")+string(filepath.Separator)) {
		t.Fatalf("placed by the terminal's environment: %s", place.File)
	}
	if _, ok := FindSession(a, id); ok {
		t.Fatal("found a session that isn't there")
	}
	// Kept under another spelling of the working folder, in the terminal's folder.
	other := filepath.Join(terminal, "projects", "-somewhere-else", id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(other), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte(`{"type":"user"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := FindSession(a, id); !ok || got.File != other {
		t.Fatalf("found %v %v", got, ok)
	}
}

// Claude Code names a sessions folder from its getcwd(): the file system's own
// spelling. A folder made in decomposed form and opened by its composed
// spelling comes back decomposed, and the key MetaRush computes follows.
func TestWorkingFolderKeyIsTheOnDiskSpelling(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("only a Mac's file system gives back its own Unicode form")
	}
	base := t.TempDir()
	nfd := filepath.Join(base, "Café work") // "é" as e + U+0301
	if err := os.Mkdir(nfd, 0o700); err != nil {
		t.Fatal(err)
	}
	nfc := filepath.Join(base, "Café work")
	t.Chdir(nfc)
	cwd, err := syscall.Getwd() // the system's getcwd(), as Claude Code reads it (os.Getwd may give $PWD back)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ClaudeProjectKey(onDiskPath(nfc)), ClaudeProjectKey(cwd); got != want {
		t.Fatalf("key from the spelling typed %q, Claude Code's %q", got, want)
	}
	// The spelling typed alone would have named another folder.
	if ClaudeProjectKey(realPath(nfc)) == ClaudeProjectKey(cwd) {
		t.Fatalf("this file system gave back the composed spelling: %q", cwd)
	}
}
