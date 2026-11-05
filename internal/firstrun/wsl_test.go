package firstrun

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/renderer"
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

// playWindowsPC plays MetaRush inside WSL on a Windows PC whose C: drive is a
// folder of the test's, holding Sam's user folder and, when given, Windows'
// own WSL settings there (.wslconfig).
func playWindowsPC(t *testing.T, i wsl.Info, wslconfig string) (c string) {
	t.Helper()
	c, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sam := filepath.Join(c, "Users", "Sam")
	if err := os.MkdirAll(sam, 0o755); err != nil {
		t.Fatal(err)
	}
	if wslconfig != "" {
		if err := os.WriteFile(filepath.Join(sam, ".wslconfig"), []byte(wslconfig), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("USERPROFILE", `C:\Users\Sam`)
	i.DriveRoot, i.Drives = "/mnt/", map[string]string{c: "c"}
	t.Cleanup(wsl.Pretend(i))
	return c
}

// What works inside WSL says what to know on Windows, word for word: WSL 1
// refused with how to move on; systemd running, turned on but not restarted,
// or off; Windows programs reachable or not; WSL's idle shutdown as Windows'
// settings say; mirrored networking and localhost forwarding; MetaRush's
// folders on the Windows side; and Claude Code, the one in the distribution
// MetaRush runs in, called what every other line calls it.
func TestWhatWorksSaysWhatToKnowOnWindows(t *testing.T) {
	type note struct {
		ready bool
		says  string
	}
	ubuntu := wsl.Info{Version: 2, Distro: "Ubuntu-24.04", DistroKnown: true, Name: "Ubuntu"}
	for _, c := range []struct {
		name      string
		info      func(wsl.Info) wsl.Info
		wslconfig string
		storage   bool // the storage folder in Sam's folder on C:
		says      string
		notes     map[string]note
		ready     bool
	}{
		{name: "WSL 1", info: func(i wsl.Info) wsl.Info { i.Version = 1; return i },
			says:  "MetaRush is in Ubuntu on Windows with WSL 1, which it can't run on: it needs WSL 2.",
			notes: map[string]note{"wsl-version": {false, "Ubuntu runs on WSL 1 here, where MetaRush's database and many of its tools don't work."}}},
		{name: "everything set", info: func(i wsl.Info) wsl.Info { i.Systemd, i.SystemdAsked, i.Interop = true, true, true; return i },
			wslconfig: "[general]\ninstanceIdleTimeout = -1\n[wsl2]\nnetworkingMode=mirrored\n",
			says:      "MetaRush runs in Ubuntu on Windows (WSL 2).", ready: true,
			notes: map[string]note{
				"background":   {true, "Ubuntu runs systemd, so MetaRush can start by itself in the background."},
				"keep-running": {true, "WSL keeps Ubuntu running after its last window closes, so MetaRush keeps running too."},
				"browser":      {true, "The dashboard opens in your Windows browser, at an address only this PC can open."},
				"network":      {true, "WSL shares Windows' network here (mirrored), and MetaRush still listens only on this PC: other computers can't open it."},
			}},
		{name: "no systemd, no interop, WSL's own settings", info: func(i wsl.Info) wsl.Info { return i },
			says: "MetaRush runs in Ubuntu on Windows (WSL 2).",
			notes: map[string]note{
				"background":   {false, "Ubuntu runs without systemd here, so MetaRush can't start by itself in the background: it starts when you open the dashboard."},
				"keep-running": {false, "Windows stops Ubuntu about 15 seconds after its last window closes, and MetaRush stops with it."},
				"browser":      {false, "Ubuntu can't start Windows programs here, so MetaRush can't open your Windows browser for you."},
			}},
		{name: "systemd asked, a minute's idle, localhost forwarding off", info: func(i wsl.Info) wsl.Info { i.SystemdAsked, i.Interop = true, true; return i },
			wslconfig: "[general]\ninstanceIdleTimeout=60000\n[wsl2]\nlocalhostForwarding=false\n", storage: true,
			says: "MetaRush runs in Ubuntu on Windows (WSL 2).",
			notes: map[string]note{
				"background":   {false, "Ubuntu is set to run systemd, but WSL hasn't restarted since, so MetaRush can't start by itself in the background yet."},
				"keep-running": {false, "Windows stops Ubuntu about 60 seconds after its last window closes, and MetaRush stops with it."},
				"browser":      {true, "The dashboard opens in your Windows browser, at an address only this PC can open."},
				"network":      {false, "WSL doesn't pass this PC's own addresses on to Ubuntu, so your Windows browser can't reach the dashboard."},
				"files":        {false, "MetaRush keeps your footage and finished videos on the Windows side (C:), where Ubuntu reads and writes them slowly."},
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			drive := playWindowsPC(t, c.info(ubuntu), c.wslconfig)
			t.Setenv(tools.EnvToolDirs, t.TempDir()) // no Claude Code anywhere
			ForgetClaude()
			defer ForgetClaude()
			h := t.TempDir()
			cfg := config.Default(h)
			cfg.SetHome(h)
			if c.storage {
				cfg.Media.Filesystem.Root = filepath.Join(drive, "Users", "Sam", "MetaRush", "media")
			}
			w := Build(context.Background(), cfg, tools.New(config.Tools{}))
			r := w.Windows
			if r == nil || r.Says != c.says || r.Ready() != c.ready || len(r.Notes) != len(c.notes) {
				t.Fatalf("%+v", r)
			}
			for key, want := range c.notes {
				n, ok := r.Note(key)
				if !ok || n.Says != want.says || n.Ready != want.ready || !n.Ready && n.Next == "" {
					t.Errorf("%s: %+v", key, n)
				}
				for _, f := range plainwords.Check(n.Says + " " + n.Next) {
					t.Errorf("%s not plain: %s", key, f)
				}
			}
			if w.Claude.Installed || w.Claude.Says != "Claude Code isn't installed in Ubuntu. MetaRush works without it — the dashboard edits, makes transcripts and renders — and with it you edit by asking in plain words." ||
				w.Claude.Next != "Install Claude Code in Ubuntu, then open it there once and sign in." {
				t.Errorf("Claude Code: %+v", w.Claude)
			}
		})
	}
}

// Claude Code installed only on Windows is named, never run, with the
// distribution called what every other line calls it.
func TestClaudeCodeOnlyOnWindows(t *testing.T) {
	c := playWindowsPC(t, wsl.Info{Version: 2, Distro: "Ubuntu-24.04", DistroKnown: true, Name: "Ubuntu", Interop: true}, "")
	npm := filepath.Join(c, "Users", "Sam", "AppData", "Roaming", "npm")
	if err := os.MkdirAll(npm, 0o755); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(filepath.Join(npm, "claude"), []byte("#!/bin/sh\ntouch '"+ran+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", npm)
	t.Setenv(tools.EnvToolDirs, "")
	os.Unsetenv(tools.EnvToolDirs) // restored after the test (t.Setenv)
	t.Setenv("HOME", t.TempDir())
	ts := tools.New(config.Tools{})
	if ts.Has(tools.Claude) {
		t.Skip("this computer has Claude Code where MetaRush looks besides PATH")
	}
	ForgetClaude()
	defer ForgetClaude()
	cc := CheckClaude(context.Background(), ts)
	if cc.Installed || cc.Says != "Claude Code is installed on Windows, but MetaRush runs in Ubuntu and needs its own copy there. MetaRush works without it meanwhile." ||
		cc.Next != "Install Claude Code in Ubuntu, then open it there once and sign in." || cc.Command != InstallCommand {
		t.Fatalf("%+v", cc)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("Windows' Claude Code was run")
	}
}

// The renderer's browser lacking some of this computer's libraries (a fresh
// Ubuntu in WSL) makes finished videos not ready, with the one command that
// adds them; once they are there, finished videos are ready.
func TestRendersSayWhatLibrariesTheyNeed(t *testing.T) {
	root := t.TempDir()
	old := renderer.LibraryRoot
	renderer.LibraryRoot = root
	t.Cleanup(func() { renderer.LibraryRoot = old })
	if err := os.MkdirAll(filepath.Join(root, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "etc", "os-release"), []byte("NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=\"24.04\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A developer checkout of the renderer (ready without setting anything up)
	// whose browser needs libnss3.
	video := filepath.Join(t.TempDir(), "video")
	for _, d := range []string{filepath.Join(video, "render"), filepath.Join(video, "node_modules")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(video, "render", "render.mjs"), []byte("// stand-in\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeELF(t, filepath.Join(video, "node_modules", ".remotion", "chrome-headless-shell", "linux64", "chrome-headless-shell-linux64", "chrome-headless-shell"), "libnss3.so")
	bin := standIns(t, "node")
	t.Setenv(tools.EnvToolDirs, bin)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Tools.DevRenderer, cfg.Tools.VideoDir = true, video
	ts := tools.New(config.Tools{})

	rd := Works(cfg, ts)["renders"]
	if rd.Ready || rd.Says != renderer.LibrariesSay+"." || rd.Next != renderer.LibrariesNext || rd.Needs != "a few system libraries" ||
		!strings.HasPrefix(rd.Command, "sudo apt install -y ") || !strings.Contains(rd.Command, " libnss3 ") {
		t.Fatalf("renders: %+v", rd)
	}
	for _, f := range plainwords.Check(rd.Says + " " + rd.Next) {
		t.Errorf("not plain: %s", f)
	}
	for _, d := range []string{"lib/x86_64-linux-gnu", "lib/aarch64-linux-gnu"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, d, "libnss3.so"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if rd := Works(cfg, ts)["renders"]; !rd.Ready || rd.Next != "" || rd.Command != "" {
		t.Fatalf("with the libraries: %+v", rd)
	}
}
