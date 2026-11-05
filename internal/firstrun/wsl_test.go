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
	"github.com/metavert-io/metarush/internal/wsl"
)

// Inside WSL (D515, D518) what works says what to know on Windows, and
// Claude Code is the one in Ubuntu: a copy installed only on Windows is
// named, never run.
func TestWhatWorksInsideWSL(t *testing.T) {
	c, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	npm := filepath.Join(c, "Users", "You", "AppData", "Roaming", "npm")
	if err := os.MkdirAll(npm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(npm, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(wsl.Pretend(wsl.Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Interop: true, Systemd: true, DriveRoot: "/mnt/", Drives: map[string]string{c: "c"}}))
	t.Setenv("PATH", npm)
	t.Setenv("HOME", t.TempDir())
	t.Setenv(tools.EnvToolDirs, "")
	os.Unsetenv(tools.EnvToolDirs) // restored after the test (t.Setenv)
	ts := tools.New(config.Tools{})
	ForgetClaude()
	defer ForgetClaude()
	cc := CheckClaude(context.Background(), ts)
	if ts.Has(tools.Claude) {
		t.Skip("this computer has Claude Code where MetaRush looks besides PATH")
	}
	if cc.Installed || !strings.Contains(cc.Says, "installed on Windows") || !strings.Contains(cc.Next, "in Ubuntu") {
		t.Fatalf("Claude Code: %+v", cc)
	}
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	w := Build(context.Background(), cfg, ts)
	if w.Windows == nil || w.Windows.Says != "MetaRush runs in Ubuntu on Windows (WSL 2)." {
		t.Fatalf("windows: %+v", w.Windows)
	}
	for _, s := range []string{cc.Says, cc.Next, w.Windows.Says} {
		for _, f := range plainwords.Check(s) {
			t.Errorf("not plain: %s", f)
		}
	}
	t.Cleanup(wsl.Pretend(wsl.Info{}))
	if Build(context.Background(), cfg, ts).Windows != nil {
		t.Fatal("outside WSL nothing is said about Windows")
	}
}
