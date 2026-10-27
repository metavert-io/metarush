package firstrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/tools"
)

// fakeClaude is a stand-in Claude Code that answers --version and
// `auth status --json` the way the real one does, and records what it was
// asked (MetaRush only ever asks those two things).
func fakeClaude(t *testing.T, status string) (*tools.Set, string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "asked")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\ncase \"$1\" in\n--version) echo '2.3.4 (Claude Code)';;\nauth) " + status + ";;\n*) exit 3;;\nesac\n"
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return tools.New(config.Tools{Claude: p}), log
}

func TestClaudeCodeCheck(t *testing.T) {
	cases := []struct {
		name, status   string
		ready, signed  bool
		billing, words string
	}{
		{"plan", `echo '{"loggedIn": true, "authMethod": "claude.ai", "email": "never-read@example.com"}'`, true, true, "plan", "Claude plan"},
		{"api key", `echo '{"loggedIn": true, "authMethod": "api_key"}'`, true, true, "api", "API key"},
		{"signed out", `echo '{"loggedIn": false}'; exit 1`, false, false, "", "not signed in"},
		{"older", `echo 'unknown command' >&2; exit 2`, true, false, "", "didn't say"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ForgetClaude()
			ts, log := fakeClaude(t, c.status)
			got := CheckClaude(context.Background(), ts)
			if !got.Installed || got.Ready != c.ready || got.Version != "2.3.4" || got.Billing != c.billing {
				t.Errorf("%+v", got)
			}
			if (got.SignedIn != nil && *got.SignedIn) != c.signed {
				t.Errorf("signed in: %v", got.SignedIn)
			}
			if !strings.Contains(got.Says, c.words) {
				t.Errorf("says %q", got.Says)
			}
			if strings.Contains(got.Says+got.Next, "never-read") {
				t.Error("something else Claude Code said was passed on")
			}
			asked, _ := os.ReadFile(log)
			for _, line := range strings.Split(strings.TrimSpace(string(asked)), "\n") {
				if line != "--version" && line != "auth status --json" {
					t.Errorf("Claude Code was asked %q", line)
				}
			}
			for _, f := range plainwords.CheckShown(got.Says + " " + got.Next) {
				t.Errorf("not plain: %s", f)
			}
		})
	}
	// Not installed (and the person's own Claude Code is never run in tests).
	ForgetClaude()
	got := CheckClaude(context.Background(), tools.New(config.Tools{}))
	if got.Installed || got.Ready || got.Command != InstallCommand || got.Next == "" {
		t.Errorf("not installed: %+v", got)
	}
}

func TestWhatWorksWithNoKeys(t *testing.T) {
	ForgetClaude()
	cfg := config.Default(t.TempDir())
	cfg.Secrets = "env"
	w := Build(context.Background(), cfg, tools.New(config.Tools{}))
	if w.Summary == "" || len(w.NoKeys) < 5 {
		t.Fatalf("%+v", w)
	}
	for _, x := range w.NoKeys {
		if x.Ready && x.Next != "" {
			t.Errorf("%s is ready but says what to do: %q", x.Key, x.Next)
		}
		if !x.Ready && x.Next == "" && x.Key != "checks" {
			t.Errorf("%s isn't ready and says nothing to do", x.Key)
		}
	}
}

// Claude Code is asked in the person's environment without MetaRush's secrets.
func TestClaudeCodeGetsNoSecrets(t *testing.T) {
	ForgetClaude()
	dir := t.TempDir()
	dump := filepath.Join(dir, "env")
	p := filepath.Join(dir, "claude")
	if err := os.WriteFile(p, []byte("#!/bin/sh\nenv >> "+dump+"\necho '2.0.0 (Claude Code)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METARUSH_MONGODB_URI", "mongodb://metarush:secret-own@127.0.0.1:1/") // secret-scan: allow (a made-up test address)
	t.Setenv("METARUSH_ELEVENLABS_API_KEY", "secret-key")
	t.Setenv("METARUSH_TAILSCALE", "/a/setting")
	CheckClaude(context.Background(), tools.New(config.Tools{Claude: p}))
	b, _ := os.ReadFile(dump)
	if strings.Contains(string(b), "secret-own") || strings.Contains(string(b), "secret-key") {
		t.Error("a MetaRush secret reached Claude Code")
	}
	if !strings.Contains(string(b), "METARUSH_TAILSCALE=/a/setting") {
		t.Error("MetaRush's other settings should still pass")
	}
}
