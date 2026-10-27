package intake

import (
	"context"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A file that couldn't be added waits a minute, five minutes, half an hour,
// then six hours at a time before it is tried again by itself.
func TestRetryWaitsGrowToSixHours(t *testing.T) {
	for tries, want := range map[int]time.Duration{0: time.Minute, 1: time.Minute, 2: 5 * time.Minute, 3: 30 * time.Minute, 4: 6 * time.Hour, 5: 6 * time.Hour, 40: 6 * time.Hour} {
		if got := retryWait(tries); got != want {
			t.Errorf("after %d failures: %s, want %s", tries, got, want)
		}
	}
}

// brokenFolder links a folder here for a project that isn't there, so every
// file in it fails until the project is: what a project cleared out from
// under a linked folder looks like.
func brokenFolder(t *testing.T, a *app.App) (models.LinkedFolder, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Logos")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rec := models.LinkedFolder{ID: ids.New(ids.LinkedFolder), WorkspaceID: a.WorkspaceID(), Name: "Logos", Scope: models.ScopeProject, ProjectID: ids.New(ids.Project),
		Upload: models.UploadSmall, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(context.Background(), store.CLinkedFolders, rec); err != nil {
		t.Fatal(err)
	}
	if err := saveLocalFolders(a, []config.FolderPath{{ID: rec.ID, Path: dir}}); err != nil {
		t.Fatal(err)
	}
	return rec, dir
}

// projectIsBack makes the project a broken folder is for.
func projectIsBack(t *testing.T, a *app.App, rec models.LinkedFolder) {
	t.Helper()
	now := time.Now().UTC()
	p := models.Project{ID: rec.ProjectID, WorkspaceID: a.WorkspaceID(), Name: "Garden", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(context.Background(), store.CProjects, p); err != nil {
		t.Fatal(err)
	}
}

const markProblem = "mark.png: That project was not found. It may have been deleted."

// A file a linked folder couldn't add waits for its next try while it stays
// the same (nothing is read meanwhile), the waits growing with each failure;
// its problem stays in the folder's status until it is added or goes away; a
// change to it is tried at once, and so is everything when a person looks.
func TestAFileThatCouldntBeAddedIsTriedAgainLater(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	rec, dir := brokenFolder(t, a)
	picture(t, dir, "mark.png", color.RGBA{200, 40, 40, 255})
	s := newScanner(a, rec, dir)

	res := s.scan(ctx, true)
	if len(res.Errors) != 1 || res.Errors[0] != markProblem {
		t.Fatalf("first try: %+v", res)
	}
	f := s.state.Failed["mark.png"]
	if f == nil || f.Tries != 1 || f.Next.Sub(f.At) != time.Minute || s.state.Done["mark.png"] != "" {
		t.Fatalf("recorded: %+v, done %q", f, s.state.Done["mark.png"])
	}
	if st := s.status; len(st.Errors) != 1 || st.Errors[0] != markProblem || st.Failed != 1 || st.RetryAt == nil || !st.RetryAt.Equal(f.Next) {
		t.Fatalf("status: %+v", st)
	}

	// Before its next try it isn't tried again, and its problem stays.
	res = s.scan(ctx, false)
	if len(res.Errors) != 0 || len(res.Pending) != 0 || s.state.Failed["mark.png"].Tries != 1 || len(s.status.Errors) != 1 || s.status.Failed != 1 {
		t.Fatalf("waiting for its next try: %+v, status %+v", res, s.status)
	}

	// Each try that fails waits longer: five minutes, half an hour, then six hours.
	for i, wait := range []time.Duration{5 * time.Minute, 30 * time.Minute, 6 * time.Hour, 6 * time.Hour} {
		s.state.Failed["mark.png"].Next = time.Now().Add(-time.Second) // its time has come
		res = s.scan(ctx, false)
		f = s.state.Failed["mark.png"]
		if len(res.Errors) != 1 || f.Tries != i+2 || f.Next.Sub(f.At) != wait || !s.status.RetryAt.Equal(f.Next) {
			t.Fatalf("try %d: %+v, recorded %+v", i+2, res, f)
		}
	}

	// A change is tried as soon as it stops changing, not in six hours, and
	// its waits start over.
	p := picture(t, dir, "mark.png", color.RGBA{20, 90, 220, 255})
	changed, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	res = s.scan(ctx, false)
	if len(res.Pending) != 1 || len(res.Errors) != 0 || len(s.status.Errors) != 1 {
		t.Fatalf("the change is seen arriving, its problem still shown: %+v, status %+v", res, s.status)
	}
	s.seen["mark.png"] = seenFile{key: fileKey(changed), since: time.Now().Add(-time.Minute)} // unchanged for a minute
	res = s.scan(ctx, false)
	f = s.state.Failed["mark.png"]
	if len(res.Errors) != 1 || f.Tries != 1 || f.Key != fileKey(changed) || f.Next.Sub(f.At) != time.Minute {
		t.Fatalf("the change was tried at once: %+v, recorded %+v", res, f)
	}

	// A person's look tries every file that couldn't be added now; a file
	// that goes away takes its problem with it.
	gone := picture(t, dir, "badge.png", color.RGBA{20, 190, 120, 255})
	if res = s.scan(ctx, true); len(res.Errors) != 2 || s.status.Failed != 2 || len(s.status.Errors) != 2 || s.state.Failed["mark.png"].Tries != 2 {
		t.Fatalf("two files: %+v, status %+v", res, s.status)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if res = s.scan(ctx, false); s.status.Failed != 1 || s.state.Failed["badge.png"] != nil || len(s.status.Errors) != 1 || s.status.Errors[0] != markProblem {
		t.Fatalf("a file that went away: %+v, status %+v", res, s.status)
	}

	// Once the problem is gone, a person's look adds it and the folder's
	// status is clear; this machine remembers it.
	projectIsBack(t, a, rec)
	res = s.scan(ctx, true)
	if res.Added != 1 || len(res.Errors) != 0 || len(s.state.Failed) != 0 || s.state.Done["mark.png"] != fileKey(changed) {
		t.Fatalf("added: %+v, failed %+v", res, s.state.Failed)
	}
	if st := s.status; st.Errors != nil || st.Failed != 0 || st.RetryAt != nil || st.Resources != 1 {
		t.Fatalf("status once added: %+v", st)
	}
	if saved := loadState(a, rec.ID); len(saved.Failed) != 0 || saved.Done["mark.png"] != fileKey(changed) {
		t.Fatalf("saved %+v", saved)
	}
}

// The folder's status line says, in plain words, how many files couldn't be
// added and when MetaRush tries them again by itself.
func TestFolderStatusSaysWhatCouldntBeAdded(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	in := func(d time.Duration) *time.Time { at := now.Add(d); return &at }
	for _, c := range []struct {
		m    *models.LinkedFolderMachine
		want string
	}{
		{nil, ""},
		{&models.LinkedFolderMachine{Files: 3}, ""},
		{&models.LinkedFolderMachine{Failed: 1, RetryAt: in(time.Minute)}, "1 file couldn't be added; MetaRush tries it again in about a minute."},
		{&models.LinkedFolderMachine{Failed: 1, RetryAt: in(5 * time.Minute)}, "1 file couldn't be added; MetaRush tries it again in about 5 minutes."},
		{&models.LinkedFolderMachine{Failed: 3, RetryAt: in(6 * time.Hour)}, "3 files couldn't be added; MetaRush tries again in about 6 hours."},
		{&models.LinkedFolderMachine{Failed: 2, RetryAt: in(-time.Minute)}, "2 files couldn't be added; MetaRush tries again soon."},
		{&models.LinkedFolderMachine{Failed: 1}, "1 file couldn't be added; MetaRush tries it again soon."},
	} {
		got := failedText(c.m, now)
		if got != c.want {
			t.Errorf("%+v: %q, want %q", c.m, got, c.want)
		}
		if fs := plainwords.CheckShown(got); len(fs) > 0 {
			t.Errorf("%q isn't plain: %v", got, fs)
		}
	}

	// In the folder's row, after how it stands; which file and why are its errors.
	a := testapp.New(t)
	rec, _ := brokenFolder(t, a)
	retry := time.Now().Add(30 * time.Minute).UTC()
	rec.Machines = map[string]models.LinkedFolderMachine{a.Cfg.Installation.ID: {State: "watching", Files: 3, Resources: 2, Failed: 1, RetryAt: &retry, Errors: []string{markProblem}}}
	row := folderRow(context.Background(), a, rec)
	if row.Text != "Kept in step: 2 resources from 3 files. 1 file couldn't be added; MetaRush tries it again in about 30 minutes." || row.Here == nil || row.Here.Errors[0] != markProblem {
		t.Fatalf("row: %q %+v", row.Text, row.Here)
	}
	if fs := plainwords.CheckShown(row.Text); len(fs) > 0 {
		t.Fatalf("%q isn't plain: %v", row.Text, fs)
	}
}

// While a daemon runs, a file that couldn't be added waits for its next try
// there too; Look again (the dashboard's button, resources.folder with
// rescan) tries it on the daemon's next pass instead.
func TestLookAgainWithADaemonTriesNow(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	rec, dir := brokenFolder(t, a)
	p := picture(t, dir, "mark.png", color.RGBA{200, 40, 40, 255})
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	var logged []string
	m := newManager(a, func(f string, args ...any) { logged = append(logged, fmt.Sprintf(f, args...)) })
	useLive(t, m)
	m.Apply(localFolders(a))
	m.pass(ctx)
	m.scanners[rec.ID].seen["mark.png"] = seenFile{key: fileKey(st), since: time.Now().Add(-time.Minute)} // unchanged for a minute
	m.pass(ctx)
	s := m.scanners[rec.ID]
	if f := s.state.Failed["mark.png"]; f == nil || f.Tries != 1 {
		t.Fatalf("tried once: %+v", f)
	}
	if len(logged) != 1 || logged[0] != "linked folder Logos: couldn't add "+markProblem {
		t.Fatalf("log %q", logged)
	}

	// The problem goes away; the file still waits for its next try (a minute).
	projectIsBack(t, a, rec)
	m.pass(ctx)
	if snap, _ := m.Snapshot(rec.ID); snap.Failed != 1 || snap.Resources != 0 || s.state.Failed["mark.png"].Tries != 1 || len(logged) != 1 {
		t.Fatalf("waiting for its next try: %+v", snap)
	}
	out, err := runFolderOp(ctx, a, &LinkedFolderSetIn{Folder: rec.ID, Rescan: true})
	if err != nil || out.Scan != nil || !m.asked[rec.ID] {
		t.Fatalf("look again: %+v %v (asked %v)", out, err, m.asked)
	}
	m.pass(ctx)
	if snap, _ := m.Snapshot(rec.ID); snap.Failed != 0 || snap.Errors != nil || snap.RetryAt != nil || snap.Resources != 1 || len(m.asked) != 0 {
		t.Fatalf("tried now and added: %+v (asked %v)", snap, m.asked)
	}
}

// A look from the terminal while the daemon runs (each keeps this machine's
// record of the folder): the daemon carries on from what the terminal did, so
// a file the terminal added is neither tried again nor shown as a problem.
func TestTheDaemonCarriesOnFromALookInTheTerminal(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	rec, dir := brokenFolder(t, a)
	picture(t, dir, "mark.png", color.RGBA{200, 40, 40, 255})
	m := newManager(a, nil)
	m.Apply(localFolders(a))
	m.Rescan(rec.ID)
	m.pass(ctx)
	if snap, _ := m.Snapshot(rec.ID); snap.Failed != 1 {
		t.Fatalf("the daemon's try: %+v", snap)
	}

	projectIsBack(t, a, rec)
	if res := newScanner(a, rec, dir).scan(ctx, true); res.Added != 1 {
		t.Fatalf("the terminal's look: %+v", res)
	}
	m.pass(ctx)
	s := m.scanners[rec.ID]
	if snap, _ := m.Snapshot(rec.ID); snap.Failed != 0 || snap.Errors != nil || snap.Resources != 1 || len(s.state.Failed) != 0 || s.state.Done["mark.png"] == "" {
		t.Fatalf("the daemon after the terminal's look: %+v, state %+v", snap, s.state)
	}
	var rs []models.Resource
	if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), map[string]any{"folder.folderId": rec.ID}, &rs); err != nil || len(rs) != 1 || len(rs[0].Versions) != 1 {
		t.Fatalf("one resource, one version: %+v %v", rs, err)
	}
}

// A record of a failure that can't be read is dropped rather than kept as
// nothing; a folder never looked through here has no state yet.
func TestFolderStateDropsUnreadableFailures(t *testing.T) {
	a := &app.App{Cfg: config.Default(t.TempDir())}
	id := ids.New(ids.LinkedFolder)
	if err := os.MkdirAll(stateDir(a), 0o755); err != nil {
		t.Fatal(err)
	}
	b := []byte(`{"done":{},"routes":{},"failed":{"a.png":null,"b.png":{"key":"1|2","problem":"It can't be read.","tries":2}}}`)
	if err := os.WriteFile(statePath(a, id), b, 0o644); err != nil {
		t.Fatal(err)
	}
	st := loadState(a, id)
	if len(st.Failed) != 1 || st.Failed["b.png"] == nil || st.Failed["b.png"].Tries != 2 || !strings.HasPrefix(st.Failed["b.png"].Problem, "It can") {
		t.Fatalf("failed %+v", st.Failed)
	}
	if stateTime(a, id).IsZero() || !stateTime(a, ids.New(ids.LinkedFolder)).IsZero() {
		t.Fatal("state times")
	}
}
