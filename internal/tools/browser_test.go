package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/wsl"
)

// Only https, or http at 127.0.0.1 on this computer, with characters no
// opener reads as its own, is ever handed to a Windows opener: never
// localhost or [::1], which Windows' browser tries first and any program
// there may listen on.
func TestBrowserAddress(t *testing.T) {
	for _, ok := range []string{
		"http://127.0.0.1:4845/auth?t=0123456789abcdef&next=/p/prj_01",
		"http://127.0.0.1:4845/auth?t=abc&next=/%3Fwelcome%3D1",
		"https://accounts.example.com/o/oauth2/v2/auth?client_id=c&state=s",
	} {
		if _, err := BrowserAddress(ok); err != nil {
			t.Errorf("%q refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"http://localhost:4845/auth?t=abc&next=/%3Fwelcome%3D1", "http://LOCALHOST:4845/", "http://[::1]:4845/", "http://127.0.0.2:4845/",
		"", "-n http://127.0.0.1:4845/", "http://example.com:4845/", "file:///etc/passwd", "javascript:alert(1)",
		"http://127.0.0.1:4845/auth?t=a b", `http://127.0.0.1:4845/"&calc`, "http://127.0.0.1:4845/$(id)", "http://127.0.0.1:4845/`id`",
		"http://127.0.0.1:4845/a;b", "http://127.0.0.1:4845/a,b", "http://127.0.0.1:4845/a^b", "http://127.0.0.1:4845/a|b", "http://127.0.0.1:4845/(a)",
		"http://127.0.0.1:4845/a!b", "http://user:pw@127.0.0.1:4845/", "http://127.0.0.1:4845/#x", "http://127.0.0.1:4845/\nx",
	} {
		if _, err := BrowserAddress(bad); err == nil {
			t.Errorf("%q passed", bad)
		}
	}
}

// recorder writes a stand-in program that writes its folder and arguments,
// one per line, into out.
func recorder(t *testing.T, dir, name, out string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func recorded(t *testing.T, out string) []string {
	t.Helper()
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// windowsDrive plays Windows' C: drive in a temporary folder, with a
// stand-in rundll32.exe where Windows keeps it that records what it got into
// out; it returns the drive's folder and System32's.
func windowsDrive(t *testing.T, tmp, out string) (c, sys32 string) {
	t.Helper()
	c = filepath.Join(tmp, "mnt", "c")
	sys32 = filepath.Join(c, "Windows", "System32")
	if err := os.MkdirAll(sys32, 0o755); err != nil {
		t.Fatal(err)
	}
	recorder(t, sys32, Rundll32, out)
	return c, sys32
}

// Inside WSL the dashboard opens in Windows' browser: wslview when it is
// there, else Windows' own rundll32.exe from System32, which gets the address
// as it is (`url.dll,FileProtocolHandler <address>`: no command interpreter
// reads it), run from the C: drive; nothing opens a browser inside Ubuntu,
// and without interop nothing runs at all.
func TestOpenInBrowserInsideWSL(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	out := filepath.Join(tmp, "out")
	c, sys32 := windowsDrive(t, tmp, out)
	bin := filepath.Join(tmp, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	recorder(t, bin, XDGOpen, out+".xdg")
	t.Setenv(EnvToolDirs, bin+string(os.PathListSeparator)+sys32)
	w := wsl.Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Interop: true, DriveRoot: "/mnt/", Drives: map[string]string{c: "c"}}
	t.Cleanup(wsl.Pretend(w))

	u := "http://127.0.0.1:4845/auth?t=0123abcd&next=/%3Fwelcome%3D1&pct=%25PATH%25"
	s := New(config.Tools{})
	if got := s.BrowserOpener(); got != Rundll32 {
		t.Fatalf("opener %q", got)
	}
	how, err := s.OpenInBrowser(context.Background(), u)
	if err != nil || how != Rundll32 {
		t.Fatalf("%q %v", how, err)
	}
	if got := recorded(t, out); strings.Join(got, "|") != c+"|url.dll,FileProtocolHandler|"+u {
		t.Fatalf("rundll32.exe got %q", got)
	}
	if _, err := os.Stat(out + ".xdg"); err == nil {
		t.Fatal("a browser inside Ubuntu was opened")
	}

	// wslview, when it is there, takes the address as it is.
	recorder(t, bin, WSLView, out)
	s = New(config.Tools{})
	if how, err := s.OpenInBrowser(context.Background(), u); err != nil || how != WSLView {
		t.Fatalf("%q %v", how, err)
	}
	if got := recorded(t, out); len(got) != 2 || got[1] != u {
		t.Fatalf("wslview got %q", got)
	}

	// An address with a character a shell reads, or on this computer at
	// anything but 127.0.0.1, is never handed on.
	_ = os.Remove(out)
	for _, bad := range []string{"http://127.0.0.1:4845/auth?t=a&next=$(id)", "http://127.0.0.1:4845/auth?t=a&next=/x;calc", "http://127.0.0.1:4845/a#b",
		"http://localhost:4845/auth?t=a", "http://[::1]:4845/auth?t=a"} {
		if _, err := s.OpenInBrowser(context.Background(), bad); err == nil {
			t.Fatalf("opened %q", bad)
		}
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("the opener ran")
	}

	// Without interop nothing can reach Windows.
	w.Interop = false
	t.Cleanup(wsl.Pretend(w))
	s = New(config.Tools{})
	if how, err := s.OpenInBrowser(context.Background(), u); !errors.Is(err, ErrNoOpener) || how != "" {
		t.Fatalf("no interop: %q %v", how, err)
	}
}

// Sign-in pages (OpenURL) inside WSL: the page's Windows address goes to
// rundll32.exe, never the sign-in address itself, and the page is kept in
// /tmp whatever TMPDIR says, so its path holds only plain characters.
func TestOpenURLInsideWSL(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	out := filepath.Join(tmp, "out")
	c, sys32 := windowsDrive(t, tmp, out)
	odd := filepath.Join(tmp, "tmp;a,b=c&d")
	if err := os.MkdirAll(odd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", odd)
	t.Setenv(EnvToolDirs, sys32)
	t.Cleanup(wsl.Pretend(wsl.Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Interop: true, DriveRoot: "/mnt/", Drives: map[string]string{c: "c"}}))
	s := New(config.Tools{})
	done, err := s.OpenURL(context.Background(), "https://accounts.example.com/o/oauth2/v2/auth?state=SECRETSTATE")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	got := recorded(t, out)
	if len(got) != 3 || got[0] != c || got[1] != "url.dll,FileProtocolHandler" || !strings.HasPrefix(got[2], `\\wsl.localhost\Ubuntu\tmp\metarush-open-`) ||
		!strings.HasSuffix(got[2], `\open.html`) || !plainPagePath(got[2]) || strings.Contains(strings.Join(got, " "), "SECRETSTATE") {
		t.Fatalf("rundll32.exe got %q", got)
	}
	page := "/tmp/" + strings.ReplaceAll(strings.TrimPrefix(got[2], `\\wsl.localhost\Ubuntu\tmp\`), `\`, "/")
	if b, err := os.ReadFile(page); err != nil || !strings.Contains(string(b), "SECRETSTATE") {
		t.Fatalf("the page %s: %v", page, err)
	}
	done()
	if _, err := os.Stat(filepath.Dir(page)); !os.IsNotExist(err) {
		t.Fatalf("the page's folder is still there: %v", err)
	}

	// Without the distribution's name Windows can't be told where the page is.
	t.Cleanup(wsl.Pretend(wsl.Info{Version: 2, Distro: "Ubuntu", Interop: true, DriveRoot: "/mnt/", Drives: map[string]string{c: "c"}}))
	if _, err := New(config.Tools{}).OpenURL(context.Background(), "https://accounts.example.com/o/oauth2/v2/auth?state=s"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("no distribution name: %v", err)
	}
}

// Inside WSL, PATH also lists Windows' folders: programs there are Windows'
// and are left alone (only named, for doctor). Windows' own rundll32.exe is
// taken only from System32, never from PATH — not from a Linux folder that
// comes first (~/.local/bin), a Windows folder a program can write to
// (%APPDATA%\npm), METARUSH_TOOL_DIRS or config — and without it nothing
// opens Windows' browser.
func TestWindowsProgramsAreLeftAlone(t *testing.T) {
	tmp, _ := filepath.EvalSymlinks(t.TempDir())
	c := filepath.Join(tmp, "mnt", "c")
	linuxBin := filepath.Join(tmp, "usr", "bin")
	localBin := filepath.Join(tmp, "home", "you", ".local", "bin")
	npm := filepath.Join(c, "Users", "Sam", "AppData", "Roaming", "npm")
	sys32 := filepath.Join(c, "Windows", "System32")
	for _, d := range []string{linuxBin, localBin, npm, sys32} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// deep-filter stands for any program both sides have (one no computer
	// running these tests has in Homebrew's folder, which a Mac prefers).
	for _, p := range []string{filepath.Join(npm, Claude), filepath.Join(npm, DeepFilter), filepath.Join(linuxBin, DeepFilter), filepath.Join(sys32, Rundll32),
		filepath.Join(npm, Rundll32), filepath.Join(localBin, Rundll32)} {
		if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(EnvToolDirs, "")
	os.Unsetenv(EnvToolDirs) // restored after the test (t.Setenv)
	sep := string(os.PathListSeparator)
	t.Setenv("PATH", localBin+sep+npm+sep+linuxBin+sep+sys32)
	t.Cleanup(wsl.Pretend(wsl.Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Interop: true, DriveRoot: "/mnt/", Drives: map[string]string{c: "c"}}))

	s := New(config.Tools{})
	if p := s.Path(DeepFilter); p != filepath.Join(linuxBin, DeepFilter) {
		t.Fatalf("deep-filter: %q (Ubuntu's own, even after a Windows folder on PATH)", p)
	}
	if s.Path(Claude) == filepath.Join(npm, Claude) {
		t.Fatal("Windows' Claude Code was taken")
	}
	if s.Path(Claude) == "" && s.WindowsCopy(Claude) != filepath.Join(npm, Claude) {
		t.Fatalf("the Windows copy isn't named: %q", s.WindowsCopy(Claude))
	}
	if p := s.Path(Rundll32); p != filepath.Join(sys32, Rundll32) {
		t.Fatalf("rundll32.exe from %q, not System32", p)
	}
	if s.Use(Rundll32, filepath.Join(localBin, Rundll32)) || s.Path(Rundll32) != filepath.Join(sys32, Rundll32) {
		t.Fatalf("rundll32.exe pointed elsewhere: %q", s.Path(Rundll32))
	}
	for _, d := range SearchDirs() {
		if strings.HasPrefix(d, c) {
			t.Fatalf("a Windows folder is watched for programs: %s", d)
		}
	}
	// Only these folders (tests, browser runs): still only where Windows keeps it.
	t.Setenv(EnvToolDirs, localBin)
	if p := New(config.Tools{}).Path(Rundll32); p != filepath.Join(sys32, Rundll32) {
		t.Fatalf("rundll32.exe with only %s: %q", localBin, p)
	}
	t.Setenv(EnvToolDirs, "")
	os.Unsetenv(EnvToolDirs)
	// Windows' PATH not passed on: still where Windows keeps it.
	t.Setenv("PATH", linuxBin)
	if p := New(config.Tools{}).Path(Rundll32); p != filepath.Join(sys32, Rundll32) {
		t.Fatalf("rundll32.exe without Windows' PATH: %q", p)
	}
	// Not in System32: no opener, whatever PATH has.
	if err := os.Remove(filepath.Join(sys32, Rundll32)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", localBin+sep+npm)
	if s := New(config.Tools{}); s.Has(Rundll32) || s.BrowserOpener() == Rundll32 {
		t.Fatalf("an opener from PATH: %q", s.Path(Rundll32))
	}
	// Outside WSL, Windows' openers mean nothing.
	t.Cleanup(wsl.Pretend(wsl.Info{}))
	t.Setenv("PATH", localBin)
	if New(config.Tools{}).Has(Rundll32) {
		t.Fatal("rundll32.exe outside WSL")
	}
}

// On a Mac and on Linux (outside WSL) the opener takes any checked address as
// one argument (a dashboard page's #part too), never one that isn't https or
// this computer's, or that could pass for an option.
func TestOpenerOutsideWSL(t *testing.T) {
	t.Cleanup(wsl.Pretend(wsl.Info{}))
	opener := Open
	if runtime.GOOS == "linux" {
		opener = XDGOpen
	} else if runtime.GOOS != "darwin" {
		t.Skip("no opener here")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	recorder(t, dir, "opener", out)
	s := New(config.Tools{})
	s.Use(opener, filepath.Join(dir, "opener"))
	how, err := s.OpenInBrowser(context.Background(), "http://127.0.0.1:4845/auth?t=ab&next=/settings/footage#watched-folder")
	if err != nil || how != opener {
		t.Fatalf("%q %v", how, err)
	}
	if opener == XDGOpen {
		// left running on its own: wait for it to write
		for i := 0; i < 50; i++ {
			if _, err := os.Stat(out); err == nil {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	if got := recorded(t, out); len(got) != 2 || got[1] != "http://127.0.0.1:4845/auth?t=ab&next=/settings/footage#watched-folder" {
		t.Fatalf("the opener got %q", got)
	}
	for _, bad := range []string{"-a http://127.0.0.1:4845/", "http://example.com/", "file:///etc/passwd", "http://127.0.0.1:4845/a b"} {
		if _, err := s.OpenInBrowser(context.Background(), bad); err == nil {
			t.Errorf("opened %q", bad)
		}
	}
}

// Under go test a real opener never runs: only stand-ins a test points at.
func TestRealOpenerIsRefusedInTests(t *testing.T) {
	t.Cleanup(wsl.Pretend(wsl.Info{}))
	t.Setenv(EnvToolDirs, "")
	os.Unsetenv(EnvToolDirs) // restored after the test (t.Setenv)
	s := New(config.Tools{})
	if s.BrowserOpener() == "" {
		t.Skip("no opener on this computer")
	}
	if _, err := s.OpenInBrowser(context.Background(), "http://127.0.0.1:4845/"); err == nil || !strings.Contains(err.Error(), "refusing to run the real") {
		t.Fatalf("a real browser opener ran: %v", err)
	}
}
