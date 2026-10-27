package tools

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
)

func writeExec(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// whisper-server comes from the same build as whisper-cli: the one next to
// it, or the one config names — never another found on PATH.
func TestWhisperServerIsTheOneNextToWhisperCLI(t *testing.T) {
	build, other := t.TempDir(), t.TempDir()
	cli := writeExec(t, build, Whisper)
	server := writeExec(t, build, WhisperServer)
	writeExec(t, other, WhisperServer)
	t.Setenv("PATH", other+string(os.PathListSeparator)+os.Getenv("PATH"))

	if got := New(config.Tools{WhisperCLI: cli}).Path(WhisperServer); got != server {
		t.Fatalf("next to whisper-cli: got %q, want %q", got, server)
	}
	lone := writeExec(t, t.TempDir(), Whisper)
	if got := New(config.Tools{WhisperCLI: lone}).Path(WhisperServer); got != "" {
		t.Fatalf("a whisper-server from PATH must not be paired with another build's whisper-cli: %q", got)
	}
	named := writeExec(t, t.TempDir(), "my-whisper-server")
	if got := New(config.Tools{WhisperCLI: cli, WhisperServer: named}).Path(WhisperServer); got != named {
		t.Fatalf("config names it: got %q", got)
	}
}

// A program kept in this process's group is still killed when its run is
// canceled (the whisper-server of the transcription model server).
func TestSameGroupProgramIsKilledOnCancel(t *testing.T) {
	s := New(config.Tools{})
	if !s.Use(FFmpeg, "/bin/sleep") {
		t.Skip("no /bin/sleep")
	}
	pid, group := make(chan int, 1), make(chan int, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Run(ctx, Spec{Tool: FFmpeg, Args: []string{"30"}, SameGroup: true, Background: true, OnStart: func(p int) {
		pg, _ := syscall.Getpgid(p)
		pid <- p
		group <- pg
	}})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("got %v after %v", err, time.Since(start))
	}
	p := <-pid
	if pg := <-group; pg != syscall.Getpgrp() {
		t.Fatalf("the program is in group %d, not this process's %d", pg, syscall.Getpgrp())
	}
	if syscall.Kill(p, 0) == nil {
		t.Fatal("the program is still running")
	}
}

// A contained program (an ability, a Remotion refresh's step) takes what it
// left running with it when it ends, even a child that kept its output open,
// and has no controlling terminal (D232, the review of 25 Oct).
func TestContainedProgramLeavesNothingBehind(t *testing.T) {
	s := New(config.Tools{})
	if !s.Use(FFmpeg, "/bin/sh") {
		t.Skip("no /bin/sh")
	}
	dir := t.TempDir()
	late := dir + "/late.txt"
	start := time.Now()
	var out bytes.Buffer
	// The child keeps stdout open and writes a file two seconds later.
	_, err := s.Run(context.Background(), Spec{Tool: FFmpeg, Contained: true, Stdout: &out,
		Args: []string{"-c", "(sleep 2; echo late > " + late + "; echo late) & echo started"}})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("Run waited for the child it left behind (%v)", d)
	}
	time.Sleep(3 * time.Second)
	if _, err := os.Stat(late); err == nil {
		t.Fatal("a child outlived the run and wrote a file")
	}
	if !strings.Contains(out.String(), "started") || strings.Contains(out.String(), "late") {
		t.Fatalf("its output: %q", out.String())
	}
	// No controlling terminal: /dev/tty can't be opened.
	var tty bytes.Buffer
	_, _ = s.Run(context.Background(), Spec{Tool: FFmpeg, Contained: true, Stdout: &tty,
		Args: []string{"-c", "if (exec 3<>/dev/tty) 2>/dev/null; then echo TTY; else echo none; fi"}})
	if strings.TrimSpace(tty.String()) != "none" {
		t.Fatalf("a contained program has no terminal: %q", tty.String())
	}
	// Canceled or out of time: the whole group goes.
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	late2 := dir + "/late2.txt"
	_, err = s.Run(ctx, Spec{Tool: FFmpeg, Contained: true, Args: []string{"-c", "(sleep 1; echo x > " + late2 + ") & sleep 30"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("out of time: %v", err)
	}
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(late2); err == nil {
		t.Fatal("a child outlived a run that ran out of time")
	}
	if _, err := s.Run(context.Background(), Spec{Tool: FFmpeg, Contained: true, OnLine: func(string) {}}); err == nil {
		t.Fatal("OnLine isn't available to a contained program")
	}
}

// No program MetaRush starts (rclone, yt-dlp, ffmpeg…) inherits a video
// platform's client or a person's sign-in (D305), or the database
// administrator's address, even when this process has them.
func TestProgramsNeverInheritSignIns(t *testing.T) {
	s := New(config.Tools{})
	if !s.Use(Rclone, "/usr/bin/env") {
		t.Skip("no /usr/bin/env")
	}
	t.Setenv("METARUSH_PUBLISH_YOUTUBE_CLIENT", `{"id":"i","secret":"s"}`)
	t.Setenv("METARUSH_PUBLISH_YOUTUBE_PERSON_USR_ABC", `{"refresh":"r"}`)
	t.Setenv("METARUSH_MONGODB_ADMIN_URI", "mongodb://admin@127.0.0.1:1/") // secret-scan: allow (a made-up test address)
	t.Setenv("METARUSH_SOMETHING_PLAIN", "kept")
	res, err := s.Run(context.Background(), Spec{Tool: Rclone})
	if err != nil {
		t.Fatal(err)
	}
	out := string(res.Stdout)
	if strings.Contains(out, "METARUSH_PUBLISH_") || strings.Contains(out, "METARUSH_MONGODB_ADMIN_URI") {
		t.Fatalf("the program got:\n%s", out)
	}
	if !strings.Contains(out, "METARUSH_SOMETHING_PLAIN=kept") {
		t.Fatal("other variables should still reach it")
	}
}

// A sign-in address (its state and code challenge) never goes on the
// opener's command line, which other programs see and the activity log
// records: a private page sends the browser on — opened in the app that
// opens web addresses, never whatever opens .html files — and it is removed
// when the sign-in is over (or a minute later).
func TestOpenURLKeepsTheAddressOffTheCommandLine(t *testing.T) {
	opener := Open
	if runtime.GOOS == "linux" {
		opener = XDGOpen
	} else if runtime.GOOS != "darwin" {
		t.Skip("no opener here")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "args")
	script := filepath.Join(dir, "opener")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$METARUSH_TEST_OPEN_OUT\"\nfor a in \"$@\"; do last=\"$a\"; done\ncat \"$last\" >> \"$METARUSH_TEST_OPEN_OUT\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	browser := filepath.Join(dir, "browser")
	if err := os.WriteFile(browser, []byte("#!/bin/sh\necho /Applications/Firefox.app\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("METARUSH_TEST_OPEN_OUT", out)
	old := OpenPageKept
	OpenPageKept = time.Hour
	t.Cleanup(func() { OpenPageKept = old })
	s := New(config.Tools{})
	s.Use(opener, script)
	s.Use(OSAScript, browser)
	u := "https://accounts.example.com/o/oauth2/v2/auth?client_id=c&state=SECRETSTATE&code_challenge=CHALLENGE&redirect_uri=http%3A%2F%2F127.0.0.1%3A5%2Fcallback"
	done, err := s.OpenURL(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(b), "\n")
	args := lines[:1]
	if opener == Open {
		args = lines[:3]
		if args[0] != "-a" || args[1] != "/Applications/Firefox.app" {
			t.Fatalf("not opened in the web browser: %q", args)
		}
	}
	page := args[len(args)-1]
	if strings.Contains(strings.Join(args, " "), "SECRETSTATE") || strings.Contains(strings.Join(args, " "), "CHALLENGE") || filepath.Base(page) != "open.html" {
		t.Fatalf("the opener was given %q", args)
	}
	if !strings.Contains(string(b), `url=https://accounts.example.com/o/oauth2/v2/auth?client_id=c&amp;state=SECRETSTATE`) {
		t.Fatalf("the page doesn't send the browser on:\n%s", b)
	}
	if st, err := os.Stat(filepath.Dir(page)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("folder %v %v", st, err)
	}
	if st, err := os.Stat(page); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("page %v %v", st, err)
	}
	done() // the sign-in is over
	if _, err := os.Stat(filepath.Dir(page)); !os.IsNotExist(err) {
		t.Fatal("the page outlived the sign-in")
	}
	if _, err := s.OpenURL(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("a file address was opened")
	}
}
