package ingest

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// fakeHome points the home folder at a temp dir so browsing, creating and the
// suggested folder never touch the real one.
func fakeHome(t *testing.T) string {
	t.Helper()
	h, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", h)
	return h
}

func reason(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		if m, ok := ae.Details.(map[string]any); ok {
			if r, ok := m["reason"].(string); ok {
				return r
			}
		}
	}
	return ""
}

func TestValidateWatchDir(t *testing.T) {
	a := testapp.New(t)
	home := fakeHome(t)

	if _, err := ValidateWatchDir(a, "relative/folder", false, false); err == nil {
		t.Fatal("relative path accepted")
	}
	blocked := map[string]string{
		"/":                                  "whole disk",
		home:                                 "whole home folder",
		a.Cfg.Home():                         "inside MetaRush's own folder",
		filepath.Join(a.Cfg.Home(), "cache"): "This folder is inside",
		filepath.Dir(a.Cfg.Home()):           "This folder contains",
	}
	for dir, want := range blocked {
		_, err := ValidateWatchDir(a, dir, false, false)
		if err == nil || reason(err) != "blocked" || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v (reason %q), want blocked with %q", dir, err, reason(err), want)
		}
	}

	inbox := filepath.Join(home, "Movies", "MetaRush Inbox")
	if _, err := ValidateWatchDir(a, "~/Movies/MetaRush Inbox", false, false); reason(err) != "missing" {
		t.Fatalf("missing folder: %v", err)
	}
	if _, err := os.Stat(inbox); !os.IsNotExist(err) {
		t.Fatal("validation created the folder without being asked")
	}
	got, err := ValidateWatchDir(a, "~/Movies/MetaRush Inbox", true, true)
	if err != nil || got != inbox {
		t.Fatalf("create: %q %v", got, err)
	}
	if st, err := os.Stat(inbox); err != nil || !st.IsDir() {
		t.Fatalf("not created: %v", err)
	}
	outside := t.TempDir()
	if _, err := ValidateWatchDir(a, outside, false, true); reason(err) != "outside_home" {
		t.Fatalf("dashboard choice outside home: %v", err)
	}
	if _, err := ValidateWatchDir(a, outside, false, false); err != nil {
		t.Fatalf("the CLI may watch any readable folder: %v", err)
	}
	file := filepath.Join(home, "notes.txt")
	_ = os.WriteFile(file, []byte("x"), 0o644)
	if _, err := ValidateWatchDir(a, file, false, false); err == nil || !strings.Contains(err.Error(), "is a file") {
		t.Fatalf("file: %v", err)
	}
}

func TestBrowseAndMkdirStayInsideHome(t *testing.T) {
	a := testapp.New(t)
	home := fakeHome(t)
	ctx := context.Background()
	for _, d := range []string{"Movies", "Documents", ".secret"} {
		_ = os.Mkdir(filepath.Join(home, d), 0o755)
	}
	_ = os.WriteFile(filepath.Join(home, "file.txt"), []byte("x"), 0o644)

	out, err := ops.CallTyped[BrowseOut](ctx, a, "watch.browse", BrowseIn{}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range out.Entries {
		names = append(names, e.Name)
	}
	if strings.Join(names, ",") != "Documents,Movies" || out.Parent != "" || out.Display != "~" || out.Blocked == "" {
		t.Fatalf("home listing: names %v parent %q display %q blocked %q", names, out.Parent, out.Display, out.Blocked)
	}
	if out.Suggested != filepath.Join(home, "Movies", "MetaRush Inbox") && out.Suggested != filepath.Join(home, "MetaRush Inbox") {
		t.Fatalf("suggested %q", out.Suggested)
	}
	sub, err := ops.CallTyped[BrowseOut](ctx, a, "watch.browse", BrowseIn{Path: "~/Movies"}, ops.Caller{})
	if err != nil || sub.Parent != home || sub.Display != "~/Movies" {
		t.Fatalf("subfolder: %+v %v", sub, err)
	}
	for _, p := range []string{"/", filepath.Dir(home), filepath.Join(home, "..")} {
		if _, err := ops.CallTyped[BrowseOut](ctx, a, "watch.browse", BrowseIn{Path: p}, ops.Caller{}); reason(err) != "outside_home" {
			t.Errorf("browse %s: %v", p, err)
		}
	}

	made, err := ops.CallTyped[BrowseEntry](ctx, a, "watch.mkdir", MkdirIn{Parent: "~/Movies", Name: "MetaRush Inbox"}, ops.Caller{})
	if err != nil || made.Display != "~/Movies/MetaRush Inbox" {
		t.Fatalf("mkdir: %+v %v", made, err)
	}
	if _, err := ops.CallTyped[BrowseEntry](ctx, a, "watch.mkdir", MkdirIn{Parent: "~/Movies", Name: "MetaRush Inbox"}, ops.Caller{}); err == nil {
		t.Fatal("creating an existing folder should be refused")
	}
	for _, in := range []MkdirIn{{Parent: filepath.Dir(home), Name: "x"}, {Parent: home, Name: "../escape"}, {Parent: home, Name: ".."}} {
		if _, err := ops.CallTyped[BrowseEntry](ctx, a, "watch.mkdir", in, ops.Caller{}); err == nil {
			t.Errorf("mkdir %+v accepted", in)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(home), "x")); err == nil {
		t.Fatal("a folder was created outside the home folder")
	}
}

func TestWatchManagerHotReload(t *testing.T) {
	a := testapp.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := 0
	m := NewWatchManager(ctx, a, nil)
	m.OnChange = func() { changes++ }
	dirA, dirB := t.TempDir(), t.TempDir()

	m.Apply(config.Watch{Enabled: true, Dir: dirA, StableSeconds: 2})
	if !m.Running() || m.Config().Dir != dirA {
		t.Fatal("not watching after Apply")
	}
	waitScan(t, m)
	m.Apply(config.Watch{Enabled: true, Dir: dirA, StableSeconds: 2}) // unchanged: no restart
	if changes != 1 {
		t.Fatalf("an unchanged config restarted the watcher (%d changes)", changes)
	}
	m.Apply(config.Watch{Enabled: true, Dir: dirB, StableSeconds: 3})
	if !m.Running() || m.Config().Dir != dirB || changes != 2 {
		t.Fatalf("switching folders: running %v dir %q changes %d", m.Running(), m.Config().Dir, changes)
	}
	m.Apply(config.Watch{Enabled: false, Dir: dirB, StableSeconds: 3})
	if m.Running() {
		t.Fatal("still watching after turning it off")
	}
	if _, ok := m.Snapshot(); ok {
		t.Fatal("snapshot while stopped")
	}
	m.Disabled = true // serve --no-watch: tracks config, runs nothing
	m.Apply(config.Watch{Enabled: true, Dir: dirA, StableSeconds: 2})
	if m.Running() || m.Config().Dir != dirA {
		t.Fatal("--no-watch must not start a watcher")
	}
}

func waitScan(t *testing.T, m *WatchManager) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s, ok := m.Snapshot(); ok && !s.LastScan.IsZero() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the watcher never scanned its folder")
}
