package intake_test

import (
	"context"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakerclone"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Files from a Google Drive folder (D220) through the person's own rclone:
// footage and a picture listed for what they are (a Google document, which
// has no file to fetch, isn't listed); the picture goes to the project's
// resources and the footage to the project, each saying it came from Google
// Drive with Drive's address and checksum; a file that isn't footage or a
// picture is said plainly, never added; a file that can't be fetched here is
// said plainly too.
func TestImportFromGoogleDrive(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	root := t.TempDir()
	art := filepath.Join(root, "Teaser", "Art")
	_ = os.MkdirAll(art, 0o755)
	tone(t, art, "Room tone.wav", 1.2, 330)
	logoPNG(t, art, "Logo.png", color.RGBA{R: 200, G: 40, B: 40, A: 255})
	_ = os.WriteFile(filepath.Join(art, "notes.txt"), []byte("call sheet"), 0o644)
	tool, _ := fakerclone.Write(t, fakerclone.State{Remotes: []fakerclone.Remote{{Name: "gdrive", Type: "drive", Root: root}}, Docs: []string{"Script"}})
	useRclone(a, tool)
	p := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Teaser"})

	list := call[intake.CloudListOut](t, a, "add.cloud.list", intake.CloudListIn{Folder: "gdrive:Teaser/Art"})
	what := map[string]string{}
	for _, f := range list.Files {
		what[f.Name] = f.What
	}
	if list.Service != "Google Drive" || what["Room tone.wav"] != "footage" || what["Logo.png"] != "picture" || what["notes.txt"] != "other" || what["Script"] != "" ||
		!strings.Contains(list.Summary, "1 video or sound file and 1 picture") {
		t.Fatalf("list %+v", list)
	}

	// All of it: the footage and the picture, nothing else.
	out := call[intake.AddPathsOut](t, a, "add.cloud", intake.CloudAddIn{Folder: "gdrive:Teaser/Art", All: true, ProjectID: p.Project.ID})
	byName := map[string]intake.AddedFile{}
	for _, f := range out.Files {
		byName[f.Name] = f
	}
	if len(out.Files) != 2 || byName["Room tone.wav"].AssetID == "" || byName["Logo.png"].Route != resources.RouteResource || byName["Logo.png"].ResourceID == "" {
		t.Fatalf("all: %+v", out.Files)
	}
	if !strings.HasPrefix(byName["Logo.png"].Path, "gdrive:Teaser/Art/") {
		t.Fatalf("where the picture came from: %+v", byName["Logo.png"])
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), byName["Room tone.wav"].AssetID, &as); err != nil {
		t.Fatal(err)
	}
	if as.Source == nil || as.Source.Platform != models.PlatformDrive || !strings.HasPrefix(as.Source.URL, "https://drive.google.com/file/d/") || as.Source.Checksum == "" {
		t.Fatalf("provenance %+v", as.Source)
	}
	if !strings.Contains(byName["Room tone.wav"].Message, "from Google Drive") {
		t.Fatalf("said %q", byName["Room tone.wav"].Message)
	}
	// Again: already there.
	again := call[intake.AddPathsOut](t, a, "add.cloud", intake.CloudAddIn{Folder: "gdrive:Teaser/Art", Names: []string{"room tone.wav"}, ProjectID: p.Project.ID})
	if len(again.Files) != 1 || !strings.Contains(again.Files[0].Message, "was already there") {
		t.Fatalf("again: %+v", again.Files)
	}

	// Named on purpose: a text file isn't added.
	odd := call[intake.AddPathsOut](t, a, "add.cloud", intake.CloudAddIn{Folder: "gdrive:Teaser/Art", Names: []string{"notes.txt"}})
	byName = map[string]intake.AddedFile{}
	for _, f := range odd.Files {
		byName[f.Name] = f
		if f.AssetID != "" || f.ResourceID != "" {
			t.Fatalf("%s was added: %+v", f.Name, f)
		}
	}
	if m := byName["notes.txt"].Message; !strings.Contains(m, "isn't a video, sound, picture, PDF or font") && byName["notes.txt"].Status != "failed" {
		t.Fatalf("notes.txt: %+v", byName["notes.txt"])
	}
	// A scratch folder that can't take a file (its path has a colon): said plainly, nothing added.
	a.Cfg.Scratch.Dir = filepath.Join(t.TempDir(), "scratch:here")
	stuck := call[intake.AddPathsOut](t, a, "add.cloud", intake.CloudAddIn{Folder: "gdrive:Teaser/Art", Names: []string{"Logo.png"}})
	if len(stuck.Files) != 1 || stuck.Files[0].Status != "failed" || !strings.Contains(stuck.Files[0].Message, "scratch folder can't take a file from Google Drive") {
		t.Fatalf("a scratch folder with a colon: %+v", stuck.Files)
	}
	odd.Files = append(odd.Files, stuck.Files...)
	for _, f := range odd.Files {
		if strings.Contains(f.Message, "rclone:") || strings.Contains(f.Message, "Failed to") {
			t.Fatalf("not plain: %q", f.Message)
		}
	}
}

// What can't be done is said plainly, with what to do: a folder that isn't
// one rclone has, no files named, files the folder doesn't have, a project
// that doesn't exist, a folder that isn't there; the whole of a remote reads
// as one.
func TestCloudFoldersSayWhatIsWrong(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "Shoot"), 0o755)
	tone(t, root, "Top.wav", 1, 440)
	tool, _ := fakerclone.Write(t, fakerclone.State{Remotes: []fakerclone.Remote{{Name: "dropbox", Type: "dropbox", Root: root}}})
	useRclone(a, tool)

	// The whole remote, from its top.
	top := call[intake.CloudListOut](t, a, "add.cloud.list", intake.CloudListIn{Folder: "dropbox:"})
	if !strings.HasPrefix(top.Summary, "The Dropbox “dropbox”: 1 file") {
		t.Fatalf("the top: %q", top.Summary)
	}
	for _, c := range []struct {
		op   string
		in   any
		code string
		says string
	}{
		{"add.cloud.list", intake.CloudListIn{Folder: "nowhere:Shoot"}, "", "nowhere"},
		{"add.cloud", intake.CloudAddIn{Folder: "nowhere:Shoot", All: true}, "", "nowhere"},
		{"add.cloud", intake.CloudAddIn{Folder: "dropbox:"}, apperr.InvalidInput, "Say which files to bring in"},
		{"add.cloud", intake.CloudAddIn{Folder: "dropbox:", IDs: []string{"id:nothere"}}, apperr.NotFound, "id:nothere"},
		{"add.cloud", intake.CloudAddIn{Folder: "dropbox:", Names: []string{"Missing.wav"}}, apperr.NotFound, "“Missing.wav”"},
		{"add.cloud", intake.CloudAddIn{Folder: "dropbox:", All: true, ProjectID: "No such project"}, apperr.NotFound, ""},
		{"add.cloud", intake.CloudAddIn{Folder: "dropbox:Not here", All: true}, "", "Not here"},
		{"add.cloud.list", intake.CloudListIn{Folder: "dropbox:Not here"}, "", "Not here"},
	} {
		_, err := ops.CallTyped[intake.AddPathsOut](ctx, a, c.op, c.in, person)
		if c.op == "add.cloud.list" {
			_, err = ops.CallTyped[intake.CloudListOut](ctx, a, c.op, c.in, person)
		}
		e := apperr.From(err)
		if err == nil || (c.code != "" && e.Code != c.code) || !strings.Contains(e.Message+" "+e.Hint, c.says) || strings.Contains(e.Message, "rclone:") {
			t.Errorf("%s %+v: %v (%s)", c.op, c.in, err, e.Code)
		}
	}
}

// A timeline from another editing app dropped with other files becomes a
// cut, not footage or a picture: the Add box and adding resources both say
// so plainly (an .xml file only when it says it is one).
func TestAddingATimelineSaysMakeACut(t *testing.T) {
	a := testapp.New(t)
	dir := t.TempDir()
	edl := filepath.Join(dir, "Rough cut.edl")
	_ = os.WriteFile(edl, []byte("TITLE: Rough cut\nFCM: NON-DROP FRAME\n"), 0o644)
	xml := filepath.Join(dir, "Rough cut.xml")
	_ = os.WriteFile(xml, []byte(`<?xml version="1.0"?><!DOCTYPE xmeml><xmeml version="5"><sequence/></xmeml>`), 0o644)
	other := filepath.Join(dir, "Settings.xml")
	_ = os.WriteFile(other, []byte(`<?xml version="1.0"?><settings/>`), 0o644)

	out := call[intake.AddPathsOut](t, a, "add.paths", intake.AddPathsIn{Paths: []string{edl, xml, other}})
	byName := map[string]intake.AddedFile{}
	for _, f := range out.Files {
		byName[f.Name] = f
	}
	for _, n := range []string{"Rough cut.edl", "Rough cut.xml"} {
		if f := byName[n]; f.Status != "skipped" || !strings.Contains(f.Message, "is a timeline from another editing app, not footage: make a cut from it instead") {
			t.Fatalf("%s: %+v", n, f)
		}
	}
	if f := byName["Settings.xml"]; strings.Contains(f.Message, "timeline") {
		t.Fatalf("an .xml that isn't a timeline: %+v", f)
	}
	res := call[intake.AddPathsOut](t, a, "resources.add", intake.ResourcesAddIn{Paths: []string{edl}})
	if len(res.Files) != 1 || res.Files[0].Status != "skipped" || !strings.Contains(res.Files[0].Message, "make a cut from it instead") {
		t.Fatalf("resources.add: %+v", res.Files)
	}
}
