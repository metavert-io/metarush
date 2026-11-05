package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/wsl"
)

// playWSL makes this test run "inside WSL" (D515): Ubuntu on WSL version v,
// its C: drive a folder of the test's.
func playWSL(t *testing.T, v int, systemd, interop bool) wsl.Info {
	t.Helper()
	c, _ := filepath.EvalSymlinks(t.TempDir())
	i := wsl.Info{Version: v, Distro: "Ubuntu", DistroKnown: true, Kernel: "6.6.87.2-microsoft-standard-WSL2", Interop: interop, Systemd: systemd,
		DriveRoot: "/mnt/", Drives: map[string]string{c: "c"}}
	if v == 1 {
		i.Kernel = "4.4.0-19041-Microsoft"
	}
	t.Cleanup(wsl.Pretend(i))
	return i
}

func errorOf(t *testing.T, r cliRun) (code string, details map[string]any) {
	t.Helper()
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil || env.OK {
		t.Fatalf("%v: not an error (%d): %s %s", r.args, r.code, r.stdout, r.stderr)
	}
	return env.Error.Code, env.Error.Details
}

// WSL 1 is refused before anything is set up, with how to move to WSL 2
// (D516); the plain words have no command in them.
func TestSetupAndInitRefuseWSL1(t *testing.T) {
	setupEnv(t)
	playWSL(t, 1, false, true)
	h := filepath.Join(t.TempDir(), "home")
	for _, args := range [][]string{
		{"setup", "--yes", "--json", "--service=false", "--skills=false"},
		{"init", "--yes", "--json"},
	} {
		r := runCLI(t, h, args...)
		code, d := errorOf(t, r)
		if code != "E_NOT_CONFIGURED" || d["reason"] != "wsl1" || !strings.Contains(d["command"].(string), "wsl --set-version Ubuntu 2") {
			t.Fatalf("%v: %s %+v", args, code, d)
		}
	}
	r := runCLI(t, h, "setup", "--yes", "--service=false", "--skills=false")
	if r.code == 0 || !strings.Contains(r.stderr, "WSL 1") || !strings.Contains(r.stderr, "WSL 2") {
		t.Fatalf("plain: %d %s", r.code, r.stderr)
	}
	setupPlain(t, "wsl1", r.stderr)
	if config := filepath.Join(h, "config.yaml"); fileExists(config) {
		t.Fatal("a refused setup left settings behind")
	}
	// doctor fails the check with the same words.
	found := false
	for _, ch := range doctorChecks(t, runCLI(t, h, "doctor", "--json")) {
		if ch.Area == "windows" && ch.Name == "wsl" {
			found = ch.Status == "fail" && strings.Contains(ch.Fix, "wsl --set-version")
		}
	}
	if !found {
		t.Fatal("doctor doesn't fail WSL 1")
	}
}

// doctorChecks are doctor --json's checks, whether or not all passed.
func doctorChecks(t *testing.T, r cliRun) []Check {
	t.Helper()
	var env struct {
		Data struct {
			Checks []Check `json:"checks"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &env); err != nil {
		t.Fatalf("doctor --json: %v\n%s\n%s", err, r.stdout, r.stderr)
	}
	return env.Data.Checks
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// Inside WSL 2 without systemd there is no background service to set up:
// setup doesn't try (systemctl never runs), says why and what turns it on;
// the result carries Windows' notes; `service install` refuses in the same
// words (D517). The storage folder on the Windows side is said to be slow
// (D521), and the database is refused there (D522).
func TestSetupInsideWSLWithoutSystemd(t *testing.T) {
	user := setupEnv(t)
	w := playWSL(t, 2, false, false)
	var c string
	for m := range w.Drives {
		c = m
	}
	db := testutil.DB(t)
	h := filepath.Join(t.TempDir(), "home")
	media := filepath.Join(c, "Users", "You", "MetaRush media")
	// A systemctl that writes down being run: it must not be.
	ran := filepath.Join(t.TempDir(), "ran")
	fake := filepath.Join(t.TempDir(), "systemctl")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\" >> '"+ran+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METARUSH_SYSTEMCTL", fake)
	args := []string{"setup", "--yes", "--json", "--database", "elsewhere", "--db-uri", testutil.URI(), "--db-name", db.Name, "--test-db",
		"--secrets", "env", "--service", "--skills=false", "--no-components", "--media-root", media, "--port", strconv.Itoa(freeTCPPort(t)),
		"--workspace-name", "Garden club", "--user-name", "Sam", "--user-email", "sam@example.com"}
	r := runCLI(t, h, args...)
	var res SetupResult
	decodeData(t, r, &res)
	if res.Service.Installed || !res.Service.Wanted || !strings.Contains(res.Service.Problem, "systemd") || !strings.Contains(res.Service.Says, "without systemd") {
		t.Fatalf("service: %+v", res.Service)
	}
	next := strings.Join(res.Next, "\n")
	if !strings.Contains(next, "[boot]\nsystemd=true") || !strings.Contains(next, "wsl --shutdown") || strings.Contains(next, "\nmetarush service install\n") {
		t.Fatalf("next: %q", res.Next)
	}
	if fileExists(ran) {
		b, _ := os.ReadFile(ran)
		t.Fatalf("systemctl ran: %s", b)
	}
	win := res.WhatWorks.Windows
	if win == nil || win.Version != 2 || !strings.Contains(win.Says, "Ubuntu on Windows (WSL 2)") {
		t.Fatalf("windows: %+v", win)
	}
	notes := map[string]wsl.Note{}
	for _, n := range win.Notes {
		if _, ok := notes[n.Key]; !ok {
			notes[n.Key] = n
		}
	}
	if notes["background"].Ready || notes["keep-running"].Ready || notes["browser"].Ready || !strings.Contains(notes["files"].Says, "on the Windows side (C:)") {
		t.Fatalf("notes: %+v", win.Notes)
	}
	// The plain end of setup says it too.
	r = runCLI(t, h, "setup", "--yes", "--service", "--skills=false", "--no-components")
	for _, want := range []string{"without systemd", "MetaRush runs in Ubuntu on Windows (WSL 2).", "Windows stops Ubuntu"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("setup again doesn't say %q:\n%s", want, r.stdout)
		}
	}
	// (Setup's own line about the plugin names "this metarush program", in
	// words that depend on whether Claude Code is found: not WSL's.)
	for _, f := range (plainwords.Allow{"flag:--details"}).Filter(plainwords.CheckShown(r.stdout)) {
		if f.Kind == plainwords.KindCommand && strings.HasPrefix(f.Text, "metarush program") {
			continue
		}
		t.Errorf("plain setup inside WSL shows %s", f)
	}
	// `service install` refuses in the same words.
	r = runCLI(t, h, "service", "install", "--json", "--no-wait")
	if code, d := errorOf(t, r); code != "E_NOT_CONFIGURED" || d["reason"] != "wsl_no_systemd" || !strings.Contains(d["command"].(string), "systemd=true") {
		t.Fatalf("service install: %s %+v", code, d)
	}
	// doctor's WINDOWS checks.
	got := map[string]Check{}
	for _, ch := range doctorChecks(t, runCLI(t, h, "doctor", "--json")) {
		if ch.Area == "windows" {
			got[ch.Name] = ch
		}
	}
	for name, status := range map[string]string{"wsl": "ok", "loopback": "ok", "background": "info", "keep-running": "info", "browser": "warn", "on the Windows side": "warn"} {
		if got[name].Status != status || got[name].Say == "" {
			t.Errorf("doctor %s: %+v", name, got[name])
		}
	}
	if !strings.Contains(got["on the Windows side"].Do, "Settings → Storage") {
		t.Errorf("the storage check: %+v", got["on the Windows side"])
	}
	r = runCLI(t, h, "doctor")
	if !strings.Contains(r.stdout, "WINDOWS") || !strings.Contains(r.stdout, "MetaRush runs in Ubuntu on Windows (WSL 2)") {
		t.Errorf("doctor's words:\n%s", r.stdout)
	}
	_ = user
}

// The database is never kept on the Windows side (D522): refused before
// anything downloads.
func TestSetupRefusesTheDatabaseOnTheWindowsSide(t *testing.T) {
	setupEnv(t)
	w := playWSL(t, 2, true, true)
	var c string
	for m := range w.Drives {
		c = m
	}
	h := filepath.Join(c, "Users", "You", "metarush")
	pin := filepath.Join(t.TempDir(), "pin.json")
	if err := os.WriteFile(pin, []byte(`{"version":"9.9.9","url":"http://127.0.0.1:1/never.tgz","sha256":"`+strings.Repeat("a", 64)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(localdb.EnvPin, pin)
	r := runCLI(t, h, "setup", "--yes", "--json", "--secrets", "env", "--service=false", "--skills=false", "--no-components", "--database-port", strconv.Itoa(freeTCPPort(t)))
	code, d := errorOf(t, r)
	if code != "E_INVALID_INPUT" || d["reason"] != "windows_side" {
		t.Fatalf("%s %+v", code, d)
	}
	r = runCLI(t, h, "setup", "--yes", "--secrets", "env", "--service=false", "--skills=false", "--no-components", "--database-port", strconv.Itoa(freeTCPPort(t)))
	if r.code == 0 || !strings.Contains(r.stderr, "can't be kept on the Windows side") || !strings.Contains(r.stderr, "Linux home folder") {
		t.Fatalf("plain: %d %s", r.code, r.stderr)
	}
	setupPlain(t, "windows side", r.stderr)
	if fileExists(filepath.Join(h, "database", "mongodb")) {
		t.Fatal("something was downloaded for it")
	}
}

// Inside WSL the dashboard's link opens in Windows' browser through cmd.exe
// (a stand-in here), escaped; without interop the person is told to open it
// in their Windows browser (D518).
func TestDashboardLinkOpensInWindows(t *testing.T) {
	playWSL(t, 2, true, true)
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "args")
	if err := os.WriteFile(filepath.Join(bin, tools.CmdExe), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+out+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(tools.EnvToolDirs, bin)
	ts := tools.New(config.Tools{})
	how, err := openBrowser(ts, "http://127.0.0.1:4845/auth?t=0123abcd&next=/p/prj_1")
	if err != nil || how != tools.CmdExe {
		t.Fatalf("%q %v", how, err)
	}
	b, _ := os.ReadFile(out)
	if got := strings.Split(strings.TrimSpace(string(b)), "\n"); len(got) != 3 || got[2] != "http://127.0.0.1:4845/auth?t=0123abcd^&next=/p/prj_1" {
		t.Fatalf("cmd.exe got %q", got)
	}
	if w := openYourselfWords(); !strings.Contains(w, "Windows browser") {
		t.Fatalf("words: %q", w)
	}
	for _, f := range plainwords.Check(openYourselfWords()) {
		t.Errorf("not plain: %s", f)
	}
}
