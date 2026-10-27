package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// testWatcher is a watcher ready to scan by hand (Run's setup without its loop).
func testWatcher(a *app.App, dir string) (*Watcher, *[]string) {
	logs := &[]string{}
	w := &Watcher{App: a, Dir: dir, StableFor: 2 * time.Second, Log: func(f string, args ...any) { *logs = append(*logs, fmt.Sprintf(f, args...)) }}
	w.seen = map[string]obs{}
	w.load()
	w.Status = WatchStatus{Dir: dir, Enabled: true, Pending: []string{}, Errors: []string{}}
	return w, logs
}

// settleSeen makes every file the watcher has seen look unchanged for a
// minute (what waiting StableFor would do).
func settleSeen(w *Watcher) {
	for f, o := range w.seen {
		o.stable = time.Now().Add(-time.Minute)
		w.seen[f] = o
	}
}

// The watched folder takes a file once it has stopped changing, moves it
// into Ingested when asked, says what became of it, and remembers what it
// did across restarts; files that aren't media are left alone for good.
func TestWatcherAddsSettledFilesAndMovesThem(t *testing.T) {
	a := testapp.New(t)
	needProbe(t, a)
	ctx := context.Background()
	p := newProject(t, a, "Garden film")
	dir := t.TempDir()
	wav := tone(t, dir, "birdsong.wav", 1, 440)
	writeAt(t, filepath.Join(dir, "notes.mov"), []byte("not a video"), time.Minute)
	tone(t, filepath.Join(dir, "Ingested"), "earlier.wav", 0.5, 220) // already moved: never again

	w, logs := testWatcher(a, dir)
	w.Move, w.ProjectID = true, p.ID
	w.scan(ctx)
	if s := w.Snapshot(); !slices.Equal(s.Pending, []string{"birdsong.wav", "notes.mov"}) || s.Missing {
		t.Fatalf("first look: %+v", s)
	}
	settleSeen(w)
	w.scan(ctx)
	s := w.Snapshot()
	if s.Ingested != 1 || s.LastIngest != "birdsong.wav (as “birdsong”)" || s.LastIngestName != "birdsong.wav" || s.LastIngestState != "ingested" ||
		s.LastIngestAsset == "" || s.LastIngestAt == nil || len(s.Pending) != 0 || len(s.Errors) != 0 {
		t.Fatalf("added: %+v", s)
	}
	if _, err := os.Stat(filepath.Join(dir, "Ingested", "birdsong.wav")); err != nil {
		t.Fatalf("not moved: %v", err)
	}
	if !slices.ContainsFunc(*logs, func(l string) bool { return strings.HasPrefix(l, "watch: skipped") && strings.Contains(l, "notes.mov") }) {
		t.Fatalf("logs %q", *logs)
	}
	b, err := os.ReadFile(StatusFile(a.Cfg.StateDir()))
	var pub WatchStatus
	if err != nil || json.Unmarshal(b, &pub) != nil || pub.Ingested != 1 || pub.Dir != dir {
		t.Fatalf("published status %s %v", b, err)
	}

	// After a restart nothing is taken twice.
	w2, _ := testWatcher(a, dir)
	w2.Move, w2.ProjectID = true, p.ID
	w2.scan(ctx)
	if s := w2.Snapshot(); len(s.Pending) != 0 {
		t.Fatalf("after a restart: %+v", s)
	}

	// The same bytes under another name: both names kept, stored once. The
	// same file again: already there; it is moved next to the first one.
	copyFile(t, filepath.Join(dir, "Ingested", "birdsong.wav"), filepath.Join(dir, "birdsong-take2.wav"))
	w2.scan(ctx)
	settleSeen(w2)
	w2.scan(ctx)
	if s := w2.Snapshot(); s.LastIngest != "birdsong-take2.wav (the same as “birdsong”; both names are kept, the file is stored once)" || s.LastIngestState != "deduped" {
		t.Fatalf("kept both: %+v", s)
	}
	copyFile(t, filepath.Join(dir, "Ingested", "birdsong.wav"), wav)
	w2.scan(ctx)
	settleSeen(w2)
	w2.scan(ctx)
	if s := w2.Snapshot(); s.LastIngest != "birdsong.wav (already in MetaRush as “birdsong”)" || s.Ingested != 2 {
		t.Fatalf("the same file again: %+v", s)
	}
	moved, _ := filepath.Glob(filepath.Join(dir, "Ingested", "*-birdsong.wav"))
	if len(moved) != 1 {
		t.Fatalf("the second copy keeps the first: %v", moved)
	}
}

// A file that can't be added is reported (the latest ten) and tried again;
// a folder that goes away is reported once and watched again when it's back.
func TestWatcherReportsProblemsAndAMissingFolder(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	for i := 0; i < 11; i++ {
		writeAt(t, filepath.Join(dir, fmt.Sprintf("clip-%02d.wav", i)), []byte("RIFF"), time.Minute)
	}
	w, logs := testWatcher(a, dir)
	w.ProjectID = ids.New(ids.Project) // a project cleared out meanwhile
	w.scan(ctx)
	settleSeen(w)
	w.scan(ctx)
	s := w.Snapshot()
	if len(s.Errors) != 10 || s.Errors[0] != "clip-10.wav: The project it was meant for was not found. It may have been deleted." || s.Ingested != 0 {
		t.Fatalf("errors %q", s.Errors)
	}
	if !slices.ContainsFunc(*logs, func(l string) bool { return strings.Contains(l, "failed (will retry)") }) {
		t.Fatalf("logs %q", *logs)
	}
	w.scan(ctx)
	if s := w.Snapshot(); len(s.Pending) != 11 {
		t.Fatalf("they are tried again: %+v", s.Pending)
	}

	gone := filepath.Join(t.TempDir(), "Card")
	w2, logs2 := testWatcher(a, gone)
	w2.scan(ctx)
	w2.scan(ctx)
	if s := w2.Snapshot(); !s.Missing || len(*logs2) != 1 || !strings.Contains((*logs2)[0], "is missing; waiting for it to reappear") {
		t.Fatalf("missing: %+v %q", s, *logs2)
	}
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	w2.scan(ctx)
	if s := w2.Snapshot(); s.Missing {
		t.Fatal("back")
	}
}

// "Only new files": what is there when watching starts is left alone until
// a person asks to import it too.
func TestSkippedFilesAreLeftAlone(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeAt(t, filepath.Join(dir, "old.wav"), []byte("RIFF old"), time.Hour)
	writeAt(t, filepath.Join(dir, "Ingested", "older.wav"), []byte("RIFF older"), time.Hour)
	state := a.Cfg.StateDir()
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	n, err := MarkExistingSkipped(state, dir)
	if err != nil || n != 1 || CountSkipped(state, dir) != 1 || CountSkipped(state, t.TempDir()) != 0 {
		t.Fatalf("marked %d %v", n, err)
	}
	w, _ := testWatcher(a, dir)
	w.scan(ctx)
	if s := w.Snapshot(); len(s.Pending) != 0 {
		t.Fatalf("a skipped file is pending: %+v", s)
	}
	if ClearSkipped(state) != 1 || CountSkipped(state, dir) != 0 || ClearSkipped(state) != 0 {
		t.Fatal("clear")
	}
}

func TestLastAddedText(t *testing.T) {
	cases := []struct {
		res  Result
		want string
	}{
		{Result{Title: "Kitchen chat"}, "kitchen.mov (as “Kitchen chat”)"},
		{Result{}, "kitchen.mov"},
		{Result{Duplicate: &models.DuplicateOf{Action: models.DuplicateKeptBoth, Title: "Kitchen chat"}}, "kitchen.mov (the same as “Kitchen chat”; both names are kept, the file is stored once)"},
		{Result{Duplicate: &models.DuplicateOf{Action: models.DuplicateSameFile, Title: "Kitchen chat"}}, "kitchen.mov (already in MetaRush as “Kitchen chat”)"},
		{Result{Duplicate: &models.DuplicateOf{Action: models.DuplicateSkipped, Title: "Kitchen chat"}}, "kitchen.mov (the same as “Kitchen chat”, so it was skipped)"},
	}
	for _, c := range cases {
		if got := lastAddedText("kitchen.mov", c.res); got != c.want {
			t.Errorf("%q, want %q", got, c.want)
		}
	}
}

func TestWatchManagerRestart(t *testing.T) {
	a := testapp.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := NewWatchManager(ctx, a, nil)
	changes := 0
	m.OnChange = func() { changes++ }
	cfg := config.Watch{Enabled: true, Dir: t.TempDir(), StableSeconds: 2}
	m.Apply(cfg)
	m.Restart()
	if !m.Running() || m.Config() != cfg || changes != 2 {
		t.Fatalf("restart: running %v config %+v changes %d", m.Running(), m.Config(), changes)
	}
	m.Apply(config.Watch{})
	if m.Running() {
		t.Fatal("stopped")
	}
}

func watchCall[O any](t *testing.T, a *app.App, op string, in any) *O {
	t.Helper()
	out, err := ops.CallTyped[O](context.Background(), a, op, in, ops.Caller{})
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return out
}

func watchErr(a *app.App, op string, in any) error {
	_, err := ops.CallTyped[WatchStatusOut](context.Background(), a, op, in, ops.Caller{})
	return err
}

func ptr[T any](v T) *T { return &v }

// The watched folder's settings and how it stands, in plain words: not set
// up, set up but nothing running, watching (a daemon's published status),
// missing, off; only new files and importing the rest.
func TestWatchOpsSetUpAndSayHowItStands(t *testing.T) {
	a := testapp.New(t)
	home := fakeHome(t)
	status := func() *WatchStatusOut { return watchCall[WatchStatusOut](t, a, "watch.status", WatchStatusIn{}) }

	s := status()
	if s.State != WatchNotConfigured || s.StableSeconds != 6 || s.Suggested != SuggestedWatchDir() || s.ConfigPath != config.Path(a.Cfg.Home()) ||
		!strings.HasPrefix(s.Note, "No watched folder yet.") {
		t.Fatalf("not set up: %+v", s)
	}
	if e := apperr.From(watchErr(a, "watch.import", WatchImportIn{})); e.Code != apperr.InvalidInput || e.Message != "There's no watched folder yet." {
		t.Fatalf("import with no folder: %+v", e)
	}

	inbox := filepath.Join(home, "Movies", "MetaRush Inbox")
	set := watchCall[WatchStatusOut](t, a, "watch.set", WatchSetIn{Dir: "~/Movies/MetaRush Inbox", Create: true})
	if set.State != WatchNotRunning || !set.Enabled || set.Dir != inbox || set.DisplayDir != "~/Movies/MetaRush Inbox" || !set.Exists ||
		set.Note != "Saved. MetaRush on this machine starts watching within a few seconds." {
		t.Fatalf("set: %+v", set)
	}
	saved, err := config.Load(a.Cfg.Home())
	if err != nil || saved.Worker.Watch.Dir != inbox || !saved.Worker.Watch.Enabled || saved.Worker.Watch.StableSeconds != 6 {
		t.Fatalf("config.yaml: %+v %v", saved.Worker.Watch, err)
	}
	if s := status(); s.State != WatchNotRunning || !strings.HasPrefix(s.Note, "Set up, but MetaRush is not running on this machine") {
		t.Fatalf("not running: %+v", s)
	}

	// A daemon publishes its status: watching while it is fresh.
	statusFile := StatusFile(a.Cfg.StateDir())
	b, _ := json.Marshal(WatchStatus{Dir: inbox, Enabled: true, LastScan: time.Now().UTC(), Pending: []string{}, Errors: []string{}})
	writeAt(t, statusFile, b, 0)
	if s := status(); s.State != WatchWatching || !s.DaemonRunning || s.Live == nil || s.Live.Dir != inbox {
		t.Fatalf("watching: %+v", s)
	}
	writeAt(t, statusFile, b, time.Minute)
	if s := status(); s.State != WatchNotRunning || s.DaemonRunning {
		t.Fatalf("an old status: %+v", s)
	}
	writeAt(t, statusFile, []byte("{not json"), 0)
	if s := status(); s.State != WatchNotRunning {
		t.Fatalf("an unreadable status: %+v", s)
	}

	// Settings: a project (or the inbox), how long files must settle, moving.
	p := newProject(t, a, "Garden film")
	withProject := watchCall[WatchStatusOut](t, a, "watch.set", WatchSetIn{Dir: inbox, ProjectID: ptr(p.ID), StableSeconds: ptr(30), MoveProcessed: ptr(true)})
	if withProject.ProjectID != p.ID || withProject.ProjectName != "Garden film" || withProject.StableSeconds != 30 || !withProject.MoveProcessed {
		t.Fatalf("with a project: %+v", withProject)
	}
	if e := apperr.From(watchErr(a, "watch.set", WatchSetIn{Dir: inbox, ProjectID: ptr(ids.New(ids.Project))})); e.Code != apperr.NotFound {
		t.Fatalf("unknown project: %+v", e)
	}
	if e := apperr.From(watchErr(a, "watch.set", WatchSetIn{Dir: inbox, StableSeconds: ptr(1)})); e.Message != "A file can wait between 2 and 600 seconds to stop changing (not 1)." {
		t.Fatalf("too short: %+v", e)
	}
	if e := apperr.From(watchErr(a, "watch.set", WatchSetIn{Dir: inbox, Existing: "maybe"})); e.Code != apperr.InvalidInput {
		t.Fatalf("existing: %+v", e)
	}
	if inboxOnly := watchCall[WatchStatusOut](t, a, "watch.set", WatchSetIn{Dir: inbox, ProjectID: ptr("")}); inboxOnly.ProjectID != "" || inboxOnly.StableSeconds != 30 {
		t.Fatalf("back to the inbox: %+v", inboxOnly)
	}

	// Only new files, then importing what was there.
	writeAt(t, filepath.Join(inbox, "a.mov"), []byte("x"), time.Minute)
	writeAt(t, filepath.Join(inbox, "b.mov"), []byte("y"), time.Minute)
	if s := watchCall[WatchStatusOut](t, a, "watch.set", WatchSetIn{Dir: inbox, Existing: "skip"}); s.Skipped != 2 {
		t.Fatalf("skip: %+v", s)
	}
	imp := watchCall[WatchStatusOut](t, a, "watch.import", WatchImportIn{})
	if imp.Note != "2 files that were already there will be added. MetaRush on this machine picks them up within a few seconds." || imp.Skipped != 0 {
		t.Fatalf("import: %+v", imp)
	}
	if err := os.Remove(filepath.Join(inbox, "b.mov")); err != nil {
		t.Fatal(err)
	}
	watchCall[WatchStatusOut](t, a, "watch.set", WatchSetIn{Dir: inbox, Existing: "skip"})
	if imp := watchCall[WatchStatusOut](t, a, "watch.import", WatchImportIn{}); !strings.HasPrefix(imp.Note, "1 file that was already there will be added.") {
		t.Fatalf("import one: %q", imp.Note)
	}

	// Off, and a folder that went away.
	if off := watchCall[WatchStatusOut](t, a, "watch.disable", WatchDisableIn{}); off.State != WatchOff || off.Enabled || off.Dir != inbox {
		t.Fatalf("off: %+v", off)
	}
	if off := watchCall[WatchStatusOut](t, a, "watch.set", WatchSetIn{Dir: inbox, Enabled: ptr(false)}); off.State != WatchOff {
		t.Fatalf("set off: %+v", off)
	}
	watchCall[WatchStatusOut](t, a, "watch.set", WatchSetIn{Dir: inbox})
	if err := os.RemoveAll(inbox); err != nil {
		t.Fatal(err)
	}
	if s := status(); s.State != WatchMissing || !strings.HasPrefix(s.Note, "The folder cannot be found.") {
		t.Fatalf("missing: %+v", s)
	}

	// From the dashboard, only folders inside the home folder.
	via := a.Actor.Via
	a.Actor.Via = "dashboard"
	err = watchErr(a, "watch.set", WatchSetIn{Dir: t.TempDir()})
	a.Actor.Via = via
	if reason(err) != "outside_home" {
		t.Fatalf("dashboard outside home: %v", err)
	}
}

// With a daemon's watch manager in this process, changes apply at once and
// its live status is what watch.status reports.
func TestWatchOpsWithARunningManager(t *testing.T) {
	a := testapp.New(t)
	home := fakeHome(t)
	ctx, cancel := context.WithCancel(context.Background())
	m := NewWatchManager(ctx, a, nil)
	prev := Live
	Live = m
	t.Cleanup(func() {
		m.Apply(config.Watch{})
		Live = prev
		cancel()
	})
	dir := filepath.Join(home, "Live inbox")
	set := watchCall[WatchStatusOut](t, a, "watch.set", WatchSetIn{Dir: dir, Create: true})
	if set.State != WatchWatching || !set.DaemonRunning || set.Live == nil || set.Note != "" || !m.Running() || m.Config().Dir != dir {
		t.Fatalf("set with a daemon: %+v", set)
	}
	if imp := watchCall[WatchStatusOut](t, a, "watch.import", WatchImportIn{}); imp.Note != "0 files that were already there will be added." {
		t.Fatalf("import: %q", imp.Note)
	}
	off := watchCall[WatchStatusOut](t, a, "watch.disable", WatchDisableIn{})
	if off.State != WatchOff || !off.DaemonRunning || m.Running() {
		t.Fatalf("off: %+v (running %v)", off, m.Running())
	}
}

// Opening a folder and the folder picker use this computer's own tools
// (fakes here); a folder that isn't there is refused before anything runs.
func TestWatchRevealAndChoose(t *testing.T) {
	a := testapp.New(t)
	home := fakeHome(t)
	fake := t.TempDir()
	pick := filepath.Join(home, "Picked")
	if err := os.MkdirAll(pick, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAt(t, filepath.Join(pick, "one.mov"), []byte("x"), time.Minute)
	script := func(name, body string) {
		if err := os.WriteFile(filepath.Join(fake, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script("open", "exit 0")
	script("xdg-open", "exit 0")
	script("osascript", "echo '"+pick+"/'")
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	a.Tools = tools.New(a.Cfg.Tools)
	opener := map[string]string{"darwin": tools.Open, "linux": tools.XDGOpen}[runtime.GOOS]
	if opener == "" || a.Tools.Path(opener) != filepath.Join(fake, opener) {
		t.Skip("the fake folder opener isn't the one MetaRush would use here")
	}

	reveal := func(dir string) (*WatchRevealOut, error) {
		return ops.CallTyped[WatchRevealOut](context.Background(), a, "watch.reveal", WatchRevealIn{Dir: dir}, ops.Caller{})
	}
	if out, err := reveal("~/Picked"); err != nil || out.Opened != "~/Picked" {
		t.Fatalf("reveal: %+v %v", out, err)
	}
	for _, bad := range []string{"", "relative/folder", filepath.Join(home, "missing")} {
		if _, err := reveal(bad); apperr.From(err).Message != "There's no folder to open." {
			t.Errorf("%q: %v", bad, err)
		}
	}
	script(opener, "exit 3")
	if _, err := reveal(pick); apperr.From(err).Code != apperr.Internal || apperr.From(err).Message != "MetaRush couldn't open the folder." {
		t.Fatalf("an opener that fails: %v", err)
	}

	choose := func() (*WatchChooseOut, error) {
		return ops.CallTyped[WatchChooseOut](context.Background(), a, "watch.choose", WatchChooseIn{}, ops.Caller{})
	}
	if runtime.GOOS != "darwin" {
		if _, err := choose(); apperr.From(err).Code != apperr.NotConfigured {
			t.Fatalf("no folder picker here: %v", err)
		}
		return
	}
	if a.Tools.Path(tools.OSAScript) != filepath.Join(fake, "osascript") {
		t.Skip("the fake folder picker isn't the one MetaRush would use here")
	}
	out, err := choose()
	if err != nil || out.Path != pick || out.Display != "~/Picked" || out.Files != 1 || out.Blocked != "" || out.Canceled {
		t.Fatalf("chosen: %+v %v", out, err)
	}
	script("osascript", "echo '"+home+"'")
	if out, err := choose(); err != nil || !strings.HasPrefix(out.Blocked, "Watching your whole home folder") {
		t.Fatalf("the home folder: %+v %v", out, err)
	}
	script("osascript", "echo ''")
	if out, err := choose(); err != nil || !out.Canceled || out.Path != "" {
		t.Fatalf("canceled: %+v %v", out, err)
	}
	script("osascript", "exit 1")
	if _, err := choose(); apperr.From(err).Code != apperr.Internal {
		t.Fatalf("a picker that fails: %v", err)
	}
}
