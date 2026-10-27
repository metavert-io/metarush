package intake_test

import (
	"bytes"
	"context"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func mkdir(t *testing.T, parts ...string) string {
	t.Helper()
	p := filepath.Join(parts...)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeFile(t *testing.T, p string, b []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Linked folders are found by name or ID; two with the same name ask which
// one; a folder can stop being kept in step here only, or everywhere, and be
// linked here again.
func TestLinkedFoldersByNameOrIDAndUnlinking(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := newProject(t, a, "Docs")
	dir1, dir2 := mkdir(t, t.TempDir(), "Logos"), mkdir(t, t.TempDir(), "Logos")
	settle(t, logoPNG(t, dir1, "mark.png", color.RGBA{220, 30, 120, 255}), time.Minute)

	one, err := intake.LinkFolder(ctx, a, &intake.LinkedFolderLinkIn{Path: dir1, ProjectID: "Docs", Upload: models.UploadAll})
	if err != nil {
		t.Fatal(err)
	}
	if one.Folder.Upload != models.UploadAll || one.Folder.ProjectName != "Docs" || one.Folder.Scope != models.ScopeProject ||
		one.Message != "Linked Logos to this project: new and changed files come in by themselves. Added 1 resource now." {
		t.Fatalf("first link: %+v", one)
	}
	two, err := intake.LinkFolder(ctx, a, &intake.LinkedFolderLinkIn{Path: dir2})
	if err != nil {
		t.Fatal(err)
	}
	if two.Folder.Scope != models.ScopeWorkspace || two.Message != "Linked Logos to the workspace library: new and changed files come in by themselves. Added 0 resources now." {
		t.Fatalf("second link: %+v", two)
	}
	again, err := intake.LinkFolder(ctx, a, &intake.LinkedFolderLinkIn{Path: dir1 + string(filepath.Separator)})
	if err != nil || again.Folder.ID != one.Folder.ID || again.Message != "Logos is already linked." || again.Scan != nil {
		t.Fatalf("the same folder again: %+v %v", again, err)
	}

	// Finding a folder: two share a name, one isn't there, an ID that isn't there.
	unlink := func(in intake.LinkedFolderUnlinkIn) error {
		t.Helper()
		return callErr(t, a, "resources.unlink", in, person)
	}
	if e := apperr.From(unlink(intake.LinkedFolderUnlinkIn{Folder: "logos"})); e.Code != apperr.InvalidInput || e.Message != `2 linked folders are called "logos".` {
		t.Fatalf("two of that name: %+v", e)
	}
	if e := apperr.From(unlink(intake.LinkedFolderUnlinkIn{Folder: "Fonts"})); e.Code != apperr.NotFound || e.Message != `There's no linked folder called "Fonts".` {
		t.Fatalf("no such name: %+v", e)
	}
	if e := apperr.From(unlink(intake.LinkedFolderUnlinkIn{Folder: ids.New(ids.LinkedFolder)})); e.Code != apperr.NotFound || !strings.HasPrefix(e.Message, "That linked folder was not found.") {
		t.Fatalf("no such ID: %+v", e)
	}

	// Only here: the record stays, this machine no longer has it.
	here := call[intake.LinkedFolderOut](t, a, "resources.unlink", intake.LinkedFolderUnlinkIn{Folder: two.Folder.ID, Here: true})
	if here.Message != "Logos is no longer kept in step on this machine." || here.Folder.Unlinked || here.Folder.HerePath != "" ||
		here.Folder.Text != "Linked on another machine. Choose where it is on this one to keep it in step here too." {
		t.Fatalf("unlinked here: %+v", here)
	}
	back := call[intake.LinkedFolderOut](t, a, "resources.folder", intake.LinkedFolderSetIn{Folder: two.Folder.ID, Path: dir2})
	if back.Message != "Saved Logos." || back.Scan == nil || back.Folder.HerePath == "" || back.Folder.Here == nil {
		t.Fatalf("linked here again: %+v", back)
	}

	// Everywhere: it stops, its resources stay.
	gone := call[intake.LinkedFolderOut](t, a, "resources.unlink", intake.LinkedFolderUnlinkIn{Folder: one.Folder.ID})
	if gone.Message != "Logos is no longer kept in step. Its resources stay where they are." || !gone.Folder.Unlinked || gone.Folder.Text != "No longer kept in step; its resources stay." {
		t.Fatalf("unlinked: %+v", gone)
	}
	n, err := a.DB.C(store.CResources).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "folder.folderId": one.Folder.ID, "removed": bson.M{"$ne": true}})
	if err != nil || n != 1 {
		t.Fatalf("its resource stays: %d %v", n, err)
	}
	all := call[intake.LinkedFolderListOut](t, a, "resources.folders", intake.LinkedFolderListIn{})
	if len(all.Folders) != 2 || all.Folders[0].ID != one.Folder.ID || all.Folders[1].ID != two.Folder.ID {
		t.Fatalf("all folders: %+v", all.Folders)
	}
	docs := call[intake.LinkedFolderListOut](t, a, "resources.folders", intake.LinkedFolderListIn{ProjectID: p.ID})
	if len(docs.Folders) != 1 || docs.Folders[0].ID != one.Folder.ID {
		t.Fatalf("the project's folders: %+v", docs.Folders)
	}
	if err := callErr(t, a, "resources.folders", intake.LinkedFolderListIn{ProjectID: "No such project"}, person); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("unknown project: %v", err)
	}
	// Linking an unlinked folder here again brings it back.
	re := call[intake.LinkedFolderOut](t, a, "resources.folder", intake.LinkedFolderSetIn{Folder: one.Folder.ID, Path: dir1})
	if re.Folder.Unlinked || re.Folder.HerePath == "" {
		t.Fatalf("linked again: %+v", re.Folder)
	}
}

// A folder's settings and how it stands, in plain words: only new files,
// files that wait to upload until a cut uses them, paused, and gone.
func TestLinkedFolderSettingsAndStates(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	dir := mkdir(t, t.TempDir(), "Stills")
	settle(t, logoPNG(t, dir, "old.png", color.RGBA{200, 30, 30, 255}), time.Minute)
	// Hidden and system files are left alone; a text file is nothing MetaRush uses.
	writeFile(t, filepath.Join(dir, ".hidden.png"), []byte("\x89PNG\r\n\x1a\n"))
	writeFile(t, filepath.Join(dir, "Thumbs.db"), []byte("x"))
	writeFile(t, filepath.Join(dir, "desktop.ini"), []byte("x"))
	writeFile(t, filepath.Join(dir, ".git", "config"), []byte("[core]"))
	settle(t, writeFile(t, filepath.Join(dir, "notes.txt"), []byte("call sheet")), time.Minute)

	if _, err := intake.LinkFolder(ctx, a, &intake.LinkedFolderLinkIn{Path: dir, Upload: "sometimes"}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("an upload choice that isn't one: %v", err)
	}
	if _, err := intake.LinkFolder(ctx, a, &intake.LinkedFolderLinkIn{Path: "Stills"}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("a relative path: %v", err)
	}
	if _, err := intake.LinkFolder(ctx, a, &intake.LinkedFolderLinkIn{Path: dir, ProjectID: "No such project"}); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("an unknown project: %v", err)
	}

	out, err := intake.LinkFolder(ctx, a, &intake.LinkedFolderLinkIn{Path: dir, Existing: "skip", Upload: models.UploadOnUse})
	if err != nil {
		t.Fatal(err)
	}
	if out.Scan == nil || out.Scan.Added != 0 || out.Folder.Upload != models.UploadOnUse {
		t.Fatalf("only new files: %+v", out)
	}
	id := out.Folder.ID
	set := func(in intake.LinkedFolderSetIn) *intake.LinkedFolderOut {
		t.Helper()
		in.Folder = id
		return call[intake.LinkedFolderOut](t, a, "resources.folder", in)
	}
	settle(t, logoPNG(t, dir, "new.png", color.RGBA{20, 90, 220, 255}), time.Minute)
	r := set(intake.LinkedFolderSetIn{Rescan: true})
	if r.Scan == nil || r.Scan.Added != 1 || r.Scan.Waiting != 1 || r.Folder.Here == nil || r.Folder.Here.Files != 3 {
		t.Fatalf("a new file: %+v here %+v", r.Scan, r.Folder.Here)
	}
	if r.Folder.Text != "Kept in step: 1 resource from 3 files; 1 large file uploads when a cut first uses it." {
		t.Fatalf("text: %q", r.Folder.Text)
	}
	settle(t, logoPNG(t, mkdir(t, dir, "more"), "newer.png", color.RGBA{20, 190, 120, 255}), time.Minute)
	r = set(intake.LinkedFolderSetIn{Rescan: true})
	if r.Folder.Text != "Kept in step: 2 resources from 4 files; 2 large files upload when a cut first uses them." {
		t.Fatalf("text with two waiting: %q", r.Folder.Text)
	}

	// Settings.
	if err := callErr(t, a, "resources.folder", intake.LinkedFolderSetIn{Folder: id, Upload: "sometimes"}, person); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("an upload choice that isn't one: %v", err)
	}
	if s := set(intake.LinkedFolderSetIn{Upload: models.UploadSmall, SmallMB: 3}); s.Folder.Upload != models.UploadSmall || s.Folder.SmallMB != 3 || s.Scan != nil || s.Message != "Saved Stills." {
		t.Fatalf("upload settings: %+v", s)
	}
	paused := set(intake.LinkedFolderSetIn{Pause: true, Rescan: true})
	if !paused.Folder.Paused || paused.Folder.Text != "Paused on this machine." || paused.Scan != nil {
		t.Fatalf("paused: %+v", paused)
	}
	resumed := set(intake.LinkedFolderSetIn{Resume: true})
	if resumed.Folder.Paused {
		t.Fatalf("resumed: %+v", resumed.Folder)
	}

	// The folder goes away (an external drive unplugged): it says so.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	missing := set(intake.LinkedFolderSetIn{Rescan: true})
	if missing.Folder.Here == nil || missing.Folder.Here.State != "missing" ||
		missing.Folder.Text != "The folder can't be found. If it is on an external drive, plug it in and keeping in step carries on." {
		t.Fatalf("missing: %+v", missing.Folder)
	}
}

// Footage in a linked folder becomes sources of its project; a picture that
// can't be read is reported by its place in the folder, never by the full
// path on this computer.
func TestLinkedFolderFootageAndProblems(t *testing.T) {
	a := testapp.New(t)
	needProbe(t, a)
	ctx := context.Background()
	p := newProject(t, a, "Garden")
	dir := mkdir(t, t.TempDir(), "Garden shoot")
	settle(t, tone(t, dir, "birds.wav", 1, 440), time.Minute)
	broken := writeFile(t, filepath.Join(dir, "broken.png"), append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{9}, 64)...))
	settle(t, broken, time.Minute)

	out, err := intake.LinkFolder(ctx, a, &intake.LinkedFolderLinkIn{Path: dir, ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.Scan.Footage != 1 || out.Scan.Added != 0 || len(out.Scan.Errors) != 1 {
		t.Fatalf("scan %+v", out.Scan)
	}
	if e := out.Scan.Errors[0]; !strings.HasPrefix(e, "broken.png: ") || strings.Contains(e, dir) {
		t.Fatalf("the problem names the file by its place in the folder: %q", e)
	}
	if !strings.HasSuffix(out.Message, "Added 0 resources now and 1 clip.") {
		t.Fatalf("message %q", out.Message)
	}
	if !strings.HasPrefix(out.Folder.Text, "Kept in step: 0 resources from 2 files, 1 clip") {
		t.Fatalf("text %q", out.Folder.Text)
	}
	var clips []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"projectIds": p.ID}, &clips); err != nil || len(clips) != 1 || clips[0].Via != "folder" {
		t.Fatalf("the clip: %+v %v", clips, err)
	}
	// The same sound again under another name is kept as a second name of the
	// same clip, not a failure.
	if err := os.WriteFile(filepath.Join(dir, "birds-copy.wav"), mustRead(t, filepath.Join(dir, "birds.wav")), 0o644); err != nil {
		t.Fatal(err)
	}
	settle(t, filepath.Join(dir, "birds-copy.wav"), time.Minute)
	r := call[intake.LinkedFolderOut](t, a, "resources.folder", intake.LinkedFolderSetIn{Folder: out.Folder.ID, Rescan: true})
	if r.Scan.Footage != 0 || r.Folder.Here.Footage != 2 {
		t.Fatalf("a copy: %+v (here %+v)", r.Scan, r.Folder.Here)
	}
	for _, e := range r.Scan.Errors {
		if strings.Contains(e, "birds-copy.wav") {
			t.Fatalf("a copy is not a problem: %q", e)
		}
	}
	var clip models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), clips[0].ID, &clip); err != nil || !clip.HasName("birds-copy.wav") {
		t.Fatalf("the copy's name is kept on the clip: %+v %v", clip.Aliases, err)
	}
}

// A file a linked folder couldn't add (here: its project was cleared out
// meanwhile) is tried again once the problem is gone, and its problem stays
// in the folder's status until then (it used to be marked done when it failed,
// and its problem was gone on the next look).
func TestLinkedFolderRetriesAFileThatFailed(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := newProject(t, a, "Garden")
	dir := mkdir(t, t.TempDir(), "Logos")
	out, err := intake.LinkFolder(ctx, a, &intake.LinkedFolderLinkIn{Path: dir, ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.C(store.CProjects).DeleteOne(ctx, bson.M{"_id": p.ID}); err != nil {
		t.Fatal(err)
	}
	settle(t, logoPNG(t, dir, "mark.png", color.RGBA{200, 30, 30, 255}), time.Minute)
	first := call[intake.LinkedFolderOut](t, a, "resources.folder", intake.LinkedFolderSetIn{Folder: out.Folder.ID, Rescan: true})
	if len(first.Scan.Errors) != 1 || !strings.HasPrefix(first.Scan.Errors[0], "mark.png: ") {
		t.Fatalf("the failure is reported: %+v", first.Scan)
	}
	// Until it is added, the folder's status keeps its problem and says when
	// it is tried again.
	list := call[intake.LinkedFolderListOut](t, a, "resources.folders", intake.LinkedFolderListIn{})
	if h := list.Folders[0].Here; h == nil || len(h.Errors) != 1 || h.Errors[0] != first.Scan.Errors[0] || h.Failed != 1 || h.RetryAt == nil ||
		!strings.Contains(list.Folders[0].Text, ". 1 file couldn't be added; MetaRush tries it again in ") {
		t.Fatalf("the folder's status: %+v %q", h, list.Folders[0].Text)
	}
	if err := a.DB.Insert(ctx, store.CProjects, p); err != nil { // the project is back
		t.Fatal(err)
	}
	second := call[intake.LinkedFolderOut](t, a, "resources.folder", intake.LinkedFolderSetIn{Folder: out.Folder.ID, Rescan: true})
	if second.Scan.Added != 1 || len(second.Scan.Errors) != 0 {
		t.Fatalf("the file was not tried again: %+v", second.Scan)
	}
	if h := second.Folder.Here; h == nil || len(h.Errors) != 0 || h.Failed != 0 || h.RetryAt != nil || strings.Contains(second.Folder.Text, "couldn't") {
		t.Fatalf("added, so its problem is gone: %+v %q", h, second.Folder.Text)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
