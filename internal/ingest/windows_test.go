package ingest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/wsl"
)

// playWindows makes the test run inside WSL 2 (D515) with Windows' C: drive
// a folder of the test's, holding a Windows user folder "You".
func playWindows(t *testing.T) (c, you string) {
	t.Helper()
	c, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	you = filepath.Join(c, "Users", "You")
	for _, d := range []string{filepath.Join(you, "Videos", "OBS"), filepath.Join(c, "Users", "Public")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(wsl.Pretend(wsl.Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Interop: true, DriveRoot: "/mnt/", Drives: map[string]string{c: "c"}}))
	return c, you
}

// Paths typed as Windows gives them (D520) are added from where WSL shows
// them; another Linux's files and network folders fail in plain words; files
// from the Windows side say they come in more slowly (D521).
func TestIngestTakesWindowsPaths(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	_, you := playWindows(t)
	wav := tone(t, filepath.Join(you, "Videos", "OBS"), "take 1.wav", 1, 440)
	linux := tone(t, t.TempDir(), "voice.wav", 1, 330)
	dry, err := ops.CallTyped[FilesOut](ctx, a, "ingest.files", FilesIn{DryRun: true, Paths: []string{
		`"C:\Users\You\Videos\OBS\take 1.wav"`, linux, `\\wsl.localhost\Debian\home\you\x.wav`, `\\nas\footage\y.wav`}}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]Result{}
	for _, r := range dry.Results {
		byName[filepath.Base(strings.ReplaceAll(r.Path, `\`, "/"))] = r
	}
	if r := byName["take 1.wav"]; r.Status != "would-ingest" || r.Path != wav {
		t.Fatalf("the Windows path: %+v", r)
	}
	if r := byName["x.wav"]; r.Status != "failed" || !strings.Contains(r.Error, "another Linux on this PC") || r.Name != "x.wav" {
		t.Fatalf("another distribution: %+v", r)
	}
	if r := byName["y.wav"]; r.Status != "failed" || !strings.Contains(r.Error, "network folder") {
		t.Fatalf("a network folder: %+v", r)
	}
	if !strings.Contains(dry.Slow, "on the Windows side") || !strings.Contains(dry.Slow, "copy footage into your Linux home folder") || strings.Contains(dry.Slow, "of these") {
		t.Fatalf("slow: %q", dry.Slow)
	}
	// Nothing from Windows: nothing to say.
	if only, err := ops.CallTyped[FilesOut](ctx, a, "ingest.files", FilesIn{DryRun: true, Paths: []string{linux}}, ops.Caller{}); err != nil || only.Slow != "" {
		t.Fatalf("Linux only: %+v %v", only, err)
	}
	// Outside WSL the words stay out and a Windows path is just a missing file.
	t.Cleanup(wsl.Pretend(wsl.Info{}))
	out, err := ops.CallTyped[FilesOut](ctx, a, "ingest.files", FilesIn{DryRun: true, Paths: []string{`C:\Users\You\Videos\OBS\take 1.wav`}}, ops.Caller{})
	if err != nil || out.Slow != "" || len(out.Results) != 1 || out.Results[0].Status != "failed" {
		t.Fatalf("outside WSL: %+v %v", out, err)
	}
}

// The watched folder inside WSL (D520, D521): typed as Windows gives it; the
// dashboard may choose a folder in the Windows user's own folder (not the
// whole folder, a whole drive or every user's), and the status says it is
// looked through every few seconds, more slowly than Linux's own disk.
func TestWatchedFolderOnTheWindowsSide(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	fakeHome(t)
	c, you := playWindows(t)
	obs := filepath.Join(you, "Videos", "OBS")
	got, err := ValidateWatchDir(a, `C:\Users\You\Videos\OBS`, false, true)
	if err != nil || got != obs {
		t.Fatalf("the dashboard's choice in the Windows user's folder: %q %v", got, err)
	}
	for dir, want := range map[string]string{
		c:                         "whole Windows drive",
		you:                       "whole Windows user folder",
		filepath.Join(c, "Users"): "every Windows user's folder",
	} {
		if _, err := ValidateWatchDir(a, dir, false, false); reason(err) != "blocked" || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", dir, err)
		}
	}
	if _, err := ValidateWatchDir(a, filepath.Join(c, "Users", "Public"), false, true); reason(err) != "outside_home" || !strings.Contains(err.Error(), "your own folder on Windows") {
		t.Fatalf("another user's folder from the dashboard: %v", err)
	}
	if _, err := ValidateWatchDir(a, `E:\Footage`, false, false); reason(err) != "windows_path" || !strings.Contains(err.Error(), "Drive E:") {
		t.Fatalf("a drive WSL doesn't show: %v", err)
	}
	if err := config.SetField(a.Cfg.Home(), []string{"worker", "watch"}, config.Watch{Enabled: true, Dir: obs, StableSeconds: 6}); err != nil {
		t.Fatal(err)
	}
	a.Cfg.Worker.Watch = config.Watch{Enabled: true, Dir: obs, StableSeconds: 6}
	st, err := ops.CallTyped[WatchStatusOut](ctx, a, "watch.status", WatchStatusIn{}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if st.EverySeconds != 5 || !strings.Contains(st.Slow, "on the Windows side (C:)") || st.DisplayDir != `C:\Users\You\Videos\OBS` {
		t.Fatalf("status: %+v", st)
	}
	// The watcher looks every 5 s there.
	w := &Watcher{App: a, Dir: obs, StableFor: time.Second}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	_ = w.Run(cctx)
	if w.Every != wsl.WatchEvery {
		t.Fatalf("every %s", w.Every)
	}
	// Browsing: the Windows user's folder is a second place to start.
	b, err := ops.CallTyped[BrowseOut](ctx, a, "watch.browse", BrowseIn{Path: `C:\Users\You\Videos`}, ops.Caller{})
	if err != nil || b.WindowsHome != you || b.Display != `C:\Users\You\Videos` || b.Parent != you || len(b.Entries) != 1 || b.Entries[0].Display != `C:\Users\You\Videos\OBS` {
		t.Fatalf("browse: %+v %v", b, err)
	}
	top, err := ops.CallTyped[BrowseOut](ctx, a, "watch.browse", BrowseIn{Path: you}, ops.Caller{})
	if err != nil || top.Parent != "" {
		t.Fatalf("browsing stops at the Windows user's folder: %+v %v", top, err)
	}
	if _, err := ops.CallTyped[BrowseOut](ctx, a, "watch.browse", BrowseIn{Path: c}, ops.Caller{}); reason(err) != "outside_home" {
		t.Fatalf("a whole drive: %v", err)
	}
}
