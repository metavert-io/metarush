package intake

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// picture writes a small PNG dated a minute ago (a file that stopped changing).
func picture(t *testing.T, dir, name string, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for y := 4; y < 28; y++ {
		for x := 4; x < 60; x++ {
			img.Set(x, y, c)
		}
	}
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	at := time.Now().Add(-time.Minute)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
	return p
}

// useLive makes m this process's running folder manager for one test.
func useLive(t *testing.T, m *FolderManager) {
	t.Helper()
	prev := LiveFolders
	LiveFolders = m
	t.Cleanup(func() { LiveFolders = prev })
}

func newManager(a *app.App, log func(string, ...any)) *FolderManager {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &FolderManager{a: a, log: log, scanners: map[string]*scanner{}, kick: make(chan struct{}, 1)}
}

// The daemon's folder manager follows this machine's list: it looks through
// each linked folder, takes a file once it has stopped changing, says what
// it added, and lets go of folders that are paused, unlinked or gone.
func TestFolderManagerKeepsThisMachinesFoldersInStep(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "Brand")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	picture(t, dir, "mark.png", color.RGBA{200, 40, 40, 255})
	out, err := LinkFolder(ctx, a, &LinkedFolderLinkIn{Path: dir})
	if err != nil || out.Scan == nil || out.Scan.Added != 1 {
		t.Fatalf("link: %+v %v", out, err)
	}
	id := out.Folder.ID

	var logged []string
	changes := 0
	m := newManager(a, func(f string, args ...any) { logged = append(logged, fmt.Sprintf(f, args...)) })
	m.OnChange = func() { changes++ }
	m.Apply(localFolders(a))
	m.pass(ctx)
	snap, ok := m.Snapshot(id)
	if !ok || snap.State != "watching" || snap.Resources != 1 || snap.Files != 1 {
		t.Fatalf("first look: %+v %v", snap, ok)
	}
	if changes != 0 {
		t.Fatal("nothing new was added, so nothing changed")
	}

	// A new file is seen first, then taken once it has stopped changing.
	p := picture(t, dir, "badge.png", color.RGBA{20, 90, 220, 255})
	m.pass(ctx)
	if snap, _ := m.Snapshot(id); len(snap.Pending) != 1 || snap.Pending[0] != "badge.png" {
		t.Fatalf("arriving: %+v", snap)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	m.scanners[id].seen["badge.png"] = seenFile{key: fileKey(st), since: time.Now().Add(-time.Minute)} // seen a minute ago, unchanged since
	m.pass(ctx)
	if snap, _ := m.Snapshot(id); snap.Resources != 2 || len(snap.Pending) != 0 || changes != 1 {
		t.Fatalf("taken: %+v (changes %d)", snap, changes)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "linked folder Brand: 1 added") {
		t.Fatalf("log %q", logged)
	}

	// The folders list shows the live state while the manager runs.
	useLive(t, m)
	rows, err := folderRows(ctx, a)
	if err != nil || len(rows) != 1 || rows[0].Here == nil || rows[0].Here.Resources != 2 || !strings.HasPrefix(rows[0].Text, "Kept in step: 2 resources") {
		t.Fatalf("rows %+v %v", rows, err)
	}

	// The same folder found at another path is looked through there.
	moved := filepath.Join(t.TempDir(), "Brand")
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	m.Apply([]config.FolderPath{{ID: id, Path: moved}})
	m.pass(ctx)
	if s := m.scanners[id]; s == nil || s.path != moved {
		t.Fatalf("scanner %+v", s)
	}

	// Paused, unlinked or unknown entries are let go.
	m.Apply([]config.FolderPath{{ID: id, Path: moved, Off: true}, {ID: ids.New(ids.LinkedFolder), Path: moved}, {ID: "", Path: moved}, {ID: id}})
	m.pass(ctx)
	if _, ok := m.Snapshot(id); ok {
		t.Fatal("a paused folder is no longer looked through")
	}
	m.Apply([]config.FolderPath{{ID: id, Path: moved}})
	m.pass(ctx)
	if _, ok := m.Snapshot(id); !ok {
		t.Fatal("resumed")
	}
	if err := a.DB.Update(ctx, store.CLinkedFolders, a.WorkspaceID(), id, map[string]any{"$set": map[string]any{"unlinked": true}}); err != nil {
		t.Fatal(err)
	}
	m.pass(ctx)
	if _, ok := m.Snapshot(id); ok {
		t.Fatal("an unlinked folder is no longer looked through")
	}
}

func folderRows(ctx context.Context, a *app.App) ([]LinkedFolderRow, error) {
	var fs []models.LinkedFolder
	if err := a.DB.Find(ctx, store.CLinkedFolders, a.WorkspaceID(), map[string]any{}, &fs); err != nil {
		return nil, err
	}
	var rows []LinkedFolderRow
	for _, f := range fs {
		rows = append(rows, folderRow(ctx, a, f))
	}
	return rows, nil
}

// While a daemon runs here, linking and looking again are left to it: the
// command doesn't look through the folder itself.
func TestWithADaemonLinkingLeavesTheLookToIt(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	m := newManager(a, nil)
	useLive(t, m)
	dir := t.TempDir()
	picture(t, dir, "mark.png", color.RGBA{200, 40, 40, 255})
	out, err := LinkFolder(ctx, a, &LinkedFolderLinkIn{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	if out.Scan != nil || strings.Contains(out.Message, "Added") || out.Folder.Text != "Linked here; looking through it…" {
		t.Fatalf("linked with a daemon: %+v", out)
	}
	if len(m.list) != 1 || m.list[0].ID != out.Folder.ID {
		t.Fatalf("the daemon was told: %+v", m.list)
	}
	select {
	case <-m.kick:
	default:
		t.Fatal("the daemon was not woken")
	}
	r, err := runFolderOp(ctx, a, &LinkedFolderSetIn{Folder: out.Folder.ID, Rescan: true})
	if err != nil || r.Scan != nil {
		t.Fatalf("look again with a daemon: %+v %v", r, err)
	}
	select {
	case <-m.kick:
	default:
		t.Fatal("looking again wakes the daemon")
	}
}

func runFolderOp(ctx context.Context, a *app.App, in *LinkedFolderSetIn) (*LinkedFolderOut, error) {
	return ops.CallTyped[LinkedFolderOut](ctx, a, "resources.folder", in, ops.Caller{Human: true})
}

// A folder linked here whose first look nobody has published yet.
func TestFolderRowBeforeTheFirstLook(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	rec := models.LinkedFolder{ID: ids.New(ids.LinkedFolder), WorkspaceID: a.WorkspaceID(), Name: "Fonts", Scope: models.ScopeWorkspace, Upload: models.UploadSmall,
		CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CLinkedFolders, rec); err != nil {
		t.Fatal(err)
	}
	if err := saveLocalFolders(a, []config.FolderPath{{ID: rec.ID, Path: t.TempDir()}}); err != nil {
		t.Fatal(err)
	}
	if row := folderRow(ctx, a, rec); row.Text != "Linked here. MetaRush keeps it in step while it runs on this machine." || row.SmallMB != a.Cfg.Resources.SmallMB() {
		t.Fatalf("no daemon: %+v", row)
	}
	useLive(t, newManager(a, nil))
	if row := folderRow(ctx, a, rec); row.Text != "Linked here; looking through it…" {
		t.Fatalf("with a daemon: %+v", row)
	}
}

// What a machine remembers about a folder survives a damaged state file,
// and a config.yaml that can't be written is a plain error.
func TestFolderStateAndSettingsProblems(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	id := ids.New(ids.LinkedFolder)
	if err := os.MkdirAll(stateDir(a), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir(a), id+".json"), []byte(`{"done":null,"routes":null}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st := loadState(a, id)
	if st.Done == nil || st.Routes == nil {
		t.Fatalf("state %+v", st)
	}
	if err := os.WriteFile(filepath.Join(stateDir(a), id+".json"), []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := loadState(a, id); len(st.Done) != 0 || st.Routes == nil {
		t.Fatalf("damaged state %+v", st)
	}

	// No config.yaml to write: linking says so plainly, and this machine's
	// list falls back to what it had in memory.
	a.Cfg.Resources.Folders = []config.FolderPath{{ID: id, Path: "/somewhere"}}
	if err := os.Remove(config.Path(a.Cfg.Home())); err != nil {
		t.Fatal(err)
	}
	if got := localFolders(a); len(got) != 1 || got[0].ID != id {
		t.Fatalf("fallback list %+v", got)
	}
	_, err := LinkFolder(ctx, a, &LinkedFolderLinkIn{Path: t.TempDir()})
	if e := apperr.From(err); e.Code != apperr.Internal || e.Message != "MetaRush couldn't save this machine's settings." {
		t.Fatalf("no config.yaml: %v", err)
	}
}

// uploadNow follows the folder's upload choice: everything, nothing until a
// cut uses it, or small files (the folder's limit, else this machine's).
func TestUploadNowFollowsTheFoldersChoice(t *testing.T) {
	a := &app.App{Cfg: config.Default(t.TempDir())}
	s := &scanner{a: a}
	cases := []struct {
		upload  string
		smallMB int
		size    int64
		want    bool
	}{
		{models.UploadAll, 0, 900 << 20, true},
		{models.UploadOnUse, 0, 10, false},
		{models.UploadSmall, 0, int64(a.Cfg.Resources.SmallMB()) << 20, true},
		{models.UploadSmall, 0, int64(a.Cfg.Resources.SmallMB())<<20 + 1, false},
		{models.UploadSmall, 2, 3 << 20, false},
		{"", 2, 1 << 20, true},
	}
	for _, c := range cases {
		s.rec = models.LinkedFolder{Upload: c.upload, SmallMB: c.smallMB}
		if got := s.uploadNow(c.size); got != c.want {
			t.Errorf("%q (limit %d MB), %d bytes: %v", c.upload, c.smallMB, c.size, got)
		}
	}
}

// The manager runs until its context ends; NewFolderManager starts it.
func TestFolderManagerRunsUntilStopped(t *testing.T) {
	a := testapp.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	out, err := LinkFolder(context.Background(), a, &LinkedFolderLinkIn{Path: dir})
	if err != nil {
		t.Fatal(err)
	}
	m := newManager(a, nil)
	m.Apply(localFolders(a))
	done := make(chan struct{})
	go func() { m.run(ctx); close(done) }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if _, ok := m.Snapshot(out.Folder.ID); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the manager never looked through the folder")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("the manager didn't stop")
	}

	stopped, stop := context.WithCancel(context.Background())
	stop()
	started := NewFolderManager(stopped, a, nil)
	started.Apply(nil) // nothing to look through: it stops at once
	if started.log == nil || started.scanners == nil {
		t.Fatalf("manager %+v", started)
	}
}

// Files a linked folder can't add are said plainly, each with its own name
// (never the whole path on this computer), and tried again later (a file
// nobody may read); a video file that isn't one is left alone, not added.
func TestLinkedFolderSaysWhatItCouldntAdd(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "Shoot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "broken.mov"), []byte("not really a video"), 0o644)
	locked := filepath.Join(dir, "locked.png")
	_ = os.WriteFile(locked, []byte("\x89PNG\r\n\x1a\n"), 0o644)
	old := time.Now().Add(-time.Hour) // there a while: not still being copied in
	for _, n := range []string{"broken.mov", "locked.png"} {
		_ = os.Chtimes(filepath.Join(dir, n), old, old)
	}
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })
	if f, err := os.Open(locked); err == nil {
		f.Close()
		t.Skip("this user reads every file")
	}
	out, err := LinkFolder(ctx, a, &LinkedFolderLinkIn{Path: dir})
	if err != nil || out.Scan == nil {
		t.Fatalf("link: %+v %v", out, err)
	}
	said := strings.Join(out.Scan.Errors, "\n")
	if out.Scan.Footage != 0 || strings.Contains(said, "broken.mov") || !strings.Contains(said, "locked.png: MetaRush isn't allowed to read this file.") || strings.Contains(said, dir) {
		t.Fatalf("what it couldn't add: %+v", out.Scan)
	}
	for _, e := range out.Scan.Errors {
		if err := plainwords.CheckShown(e); err != nil {
			t.Errorf("%q: %v", e, err)
		}
	}
}
