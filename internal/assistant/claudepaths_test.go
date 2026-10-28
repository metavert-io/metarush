package assistant

import (
	"path/filepath"
	"strings"
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
