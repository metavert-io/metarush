package intake_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"image/color"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// tone writes a short WAV (16-bit mono, 16 kHz) of a quiet tone: footage
// that every computer's ffprobe reads, made without ffmpeg.
func tone(t *testing.T, dir, name string, seconds, hz float64) string {
	t.Helper()
	n := int(16000 * seconds)
	data := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := int16(6000 * math.Sin(2*math.Pi*hz*float64(i)/16000))
		binary.LittleEndian.PutUint16(data[2*i:], uint16(v))
	}
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	w(uint32(36 + len(data)))
	b.WriteString("WAVEfmt ")
	w(uint32(16))
	w(uint16(1)) // PCM
	w(uint16(1)) // mono
	w(uint32(16000))
	w(uint32(32000))
	w(uint16(2))
	w(uint16(16))
	b.WriteString("data")
	w(uint32(len(data)))
	b.Write(data)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// needProbe skips a test that adds footage where ffprobe isn't installed
// (CI has it).
func needProbe(t *testing.T, a *app.App) {
	t.Helper()
	if !a.Tools.Has(tools.FFprobe) {
		t.Skip("needs ffprobe")
	}
}

func fileNamed(files []intake.AddedFile, name string) *intake.AddedFile {
	for i := range files {
		if files[i].Name == name {
			return &files[i]
		}
	}
	return nil
}

// The Add box says where dropped files and pasted links would go before
// anything is sent.
func TestAddRouteSaysWhereThingsWouldGo(t *testing.T) {
	a := testapp.New(t)
	out := call[intake.RouteOut](t, a, "add.route", intake.RouteIn{Names: []string{"interview.mov", "logo.png", "deck.pdf", "photo.HEIC"},
		Text: "the repo https://github.com/acme/widget and the page <https://example.com/pricing>"})
	if len(out.Files) != 4 || out.Files[0].Route != resources.RouteFootage || out.Files[1].Kind != "image" || out.Files[2].Kind != "pdf" || !out.Files[3].Convert {
		t.Fatalf("files %+v", out.Files)
	}
	if len(out.Links) != 2 || out.Links[0].Route != resources.LinkRepo || !out.Links[0].Ready || out.Links[1].Route != resources.LinkAmbiguous {
		t.Fatalf("links %+v", out.Links)
	}
	if out.Summary != "1 video or sound file and 3 pictures, PDFs or fonts and 2 links" {
		t.Fatalf("summary %q", out.Summary)
	}
	links := call[intake.RouteOut](t, a, "add.route", intake.RouteIn{Text: "https://youtu.be/dQw4w9WgXcQ"})
	if links.Summary != "1 link" || len(links.Files) != 0 || links.Links[0].Platform != models.PlatformYouTube {
		t.Fatalf("one link: %+v", links)
	}
	empty := call[intake.RouteOut](t, a, "add.route", intake.RouteIn{})
	if empty.Summary != "" || empty.Files == nil || empty.Links == nil {
		t.Fatalf("nothing: %+v", empty)
	}
}

// Pasted links: a video is imported, a repository or a page is fetched on
// this machine, a link that could be either asks, and anything else is
// refused — each with a plain line, and one summary for all of them.
func TestAddLinksBringsEachLinkInItsOwnWay(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := newProject(t, a, "Launch")
	out := call[intake.AddLinksOut](t, a, "add.links", intake.AddLinksIn{ProjectID: "Launch",
		URLs: []string{"not a link", "https://example.com/pricing", "https://github.com/acme/widget", "https://www.youtube.com/watch?v=dQw4w9WgXcQ"}})
	if len(out.Links) != 4 {
		t.Fatalf("links %+v", out.Links)
	}
	refused, ask, repo, video := out.Links[0], out.Links[1], out.Links[2], out.Links[3]
	if refused.Status != "refused" || refused.Route != "invalid" || refused.Message != "That isn't a web address MetaRush can use." {
		t.Fatalf("refused %+v", refused)
	}
	if ask.Status != "ask" || !slices.Equal(ask.Choices, []string{"video", "page"}) || ask.Message != "Is this a video to import, or a web page to use in graphics?" {
		t.Fatalf("ask %+v", ask)
	}
	if repo.Status != "fetching" || repo.JobID == "" || repo.Message != "Getting acme/widget from GitHub." {
		t.Fatalf("repository %+v", repo)
	}
	if video.Status != "importing" || video.Route != resources.LinkVideo || out.Imports == nil || out.Imports.Queued != 1 {
		t.Fatalf("video %+v imports %+v", video, out.Imports)
	}
	if out.Summary != "1 video is being imported; 1 repository or page is being brought in; 1 link needs an answer; 1 link can't be used." {
		t.Fatalf("summary %q", out.Summary)
	}
	// The repository is fetched by a job pinned to this machine, into the project.
	j, err := a.Queue.Get(ctx, repo.JobID)
	if err != nil || j.Type != jobs.TypeFetch || j.Installation != a.Cfg.Installation.ID || j.ProjectID != p.ID || j.Payload["kind"] != resources.FetchRepo {
		t.Fatalf("fetch job %+v %v", j, err)
	}
	n, err := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "type": jobs.TypeImport, "projectId": p.ID})
	if err != nil || n != 1 {
		t.Fatalf("import jobs: %d %v", n, err)
	}

	// Answered: as a page it becomes a snapshot of the workspace library; the
	// same answer again is the fetch already on its way.
	page := call[intake.AddLinksOut](t, a, "add.links", intake.AddLinksIn{URLs: []string{"https://example.com/pricing"}, As: "page"})
	if len(page.Links) != 1 || page.Links[0].Status != "fetching" || page.Links[0].Message != "Getting a snapshot of example.com/pricing." || page.Summary != "1 repository or page is being brought in." {
		t.Fatalf("as a page: %+v", page)
	}
	again := call[intake.AddLinksOut](t, a, "add.links", intake.AddLinksIn{URLs: []string{"https://example.com/pricing"}, As: "page"})
	if again.Links[0].JobID != page.Links[0].JobID || !strings.HasPrefix(again.Links[0].Message, "Already getting") {
		t.Fatalf("the same page again: %+v", again.Links)
	}
	asVideo := call[intake.AddLinksOut](t, a, "add.links", intake.AddLinksIn{URLs: []string{"https://example.com/pricing"}, As: "video"})
	if len(asVideo.Links) != 1 || asVideo.Links[0].Status != "importing" || asVideo.Imports == nil {
		t.Fatalf("as a video: %+v", asVideo)
	}

	// A project that isn't there is an error, not a link added somewhere else.
	err = callErr(t, a, "add.links", intake.AddLinksIn{URLs: []string{"https://github.com/acme/widget"}, ProjectID: "No such project"}, person)
	if apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("unknown project: %v", err)
	}
}

// fakeSource is a URL source standing in for one that isn't built yet, one
// that refuses, or one that adds at once.
type fakeSource struct {
	name, route string
	nomatch     bool
	out         *resources.URLAddOut
	err         error
}

func (f fakeSource) Name() string        { return f.name }
func (f fakeSource) Route() string       { return f.route }
func (f fakeSource) Match(*url.URL) bool { return !f.nomatch }
func (f fakeSource) Add(context.Context, *app.App, resources.URLAddIn) (*resources.URLAddOut, error) {
	return f.out, f.err
}

// useSource swaps a registered URL source (by name) for one test.
func useSource(t *testing.T, s fakeSource, probe string) {
	t.Helper()
	u, _ := url.Parse(probe)
	orig := resources.URLSourceFor(s.route, u)
	if orig == nil || orig.Name() != s.name {
		t.Fatalf("no %s source to replace", s.name)
	}
	resources.RegisterURLSource(s)
	t.Cleanup(func() { resources.RegisterURLSource(orig) })
}

func TestAddLinksWhenASourceCantOrAddsAtOnce(t *testing.T) {
	a := testapp.New(t)
	repo := "https://github.com/acme/widget"
	addOne := func() intake.LinkOutcome {
		t.Helper()
		out := call[intake.AddLinksOut](t, a, "add.links", intake.AddLinksIn{URLs: []string{repo}})
		if len(out.Links) != 1 {
			t.Fatalf("links %+v", out.Links)
		}
		return out.Links[0]
	}
	t.Run("not built yet", func(t *testing.T) {
		useSource(t, fakeSource{name: "github", route: resources.LinkRepo, err: resources.ErrComingSoon}, repo)
		if o := addOne(); o.Status != "later" || o.Message != resources.ComingSoonMessage(resources.LinkRepo) {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("no source for it", func(t *testing.T) {
		useSource(t, fakeSource{name: "github", route: resources.LinkRepo, nomatch: true}, repo)
		out := call[intake.AddLinksOut](t, a, "add.links", intake.AddLinksIn{URLs: []string{repo}})
		if o := out.Links[0]; o.Status != "later" || out.Summary != "1 link can't be added yet." {
			t.Fatalf("%+v %q", o, out.Summary)
		}
	})
	t.Run("refused with a reason", func(t *testing.T) {
		useSource(t, fakeSource{name: "github", route: resources.LinkRepo, err: apperr.New(apperr.InvalidInput, "That repository is too large to keep.")}, repo)
		if o := addOne(); o.Status != "refused" || o.Message != "That repository is too large to keep." {
			t.Fatalf("%+v", o)
		}
	})
	t.Run("added at once", func(t *testing.T) {
		useSource(t, fakeSource{name: "github", route: resources.LinkRepo, out: &resources.URLAddOut{Message: "Added widget to Resources."}}, repo)
		if o := addOne(); o.Status != "added" || o.Message != "Added widget to Resources." || o.JobID != "" {
			t.Fatalf("%+v", o)
		}
		// The summary says what happened when everything was added at once (it was ".").
		one := call[intake.AddLinksOut](t, a, "add.links", intake.AddLinksIn{URLs: []string{repo}})
		if one.Summary != "1 repository or page was added." {
			t.Fatalf("one: %q", one.Summary)
		}
		two := call[intake.AddLinksOut](t, a, "add.links", intake.AddLinksIn{URLs: []string{repo, "https://github.com/acme/gadget", "not a link"}})
		if two.Summary != "2 repositories or pages were added; 1 link can't be used." {
			t.Fatalf("two and a refusal: %q", two.Summary)
		}
	})
}

// Local files and folders through the Add box: pictures to Resources,
// footage to the project, folders linked; what can't be added says why.
func TestAddPathsRoutesFilesAndFolders(t *testing.T) {
	a := testapp.New(t)
	needProbe(t, a)
	ctx := context.Background()
	p := newProject(t, a, "Garden film")
	dir := t.TempDir()
	logo := logoPNG(t, dir, "garden-logo.png", color.RGBA{30, 140, 60, 255})
	wav := tone(t, dir, "birdsong.wav", 1.5, 440)
	notes := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(notes, []byte("shot list: roses, then the pond\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "Brand assets")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	settle(t, logoPNG(t, folder, "badge.png", color.RGBA{200, 40, 40, 255}), time.Minute)

	out := call[intake.AddPathsOut](t, a, "add.paths", intake.AddPathsIn{ProjectID: "Garden film",
		Paths: []string{logo, wav, notes, filepath.Join(dir, "missing.png"), folder, a.Cfg.Home()}})
	if f := fileNamed(out.Files, "garden-logo.png"); f == nil || f.Route != resources.RouteResource || f.Status != resources.StatusAdded || f.ResourceID == "" {
		t.Fatalf("picture: %+v", f)
	}
	clip := fileNamed(out.Files, "birdsong.wav")
	if clip == nil || clip.Route != resources.RouteFootage || clip.Status != "ingested" || clip.AssetID == "" || !strings.HasPrefix(clip.Message, "Added ") {
		t.Fatalf("footage: %+v", clip)
	}
	if f := fileNamed(out.Files, "notes.txt"); f == nil || f.Status != "skipped" || f.Route != resources.RouteUnknown || f.Message != "notes.txt isn't a video, sound, picture, PDF or font." {
		t.Fatalf("text file: %+v", f)
	}
	if f := fileNamed(out.Files, "missing.png"); f == nil || f.Status != "failed" || f.Message != "Can't find missing.png." {
		t.Fatalf("missing: %+v", f)
	}
	if f := fileNamed(out.Files, filepath.Base(a.Cfg.Home())); f == nil || f.Status != "failed" || !strings.Contains(f.Message, "MetaRush's own folder") {
		t.Fatalf("MetaRush's own folder: %+v", f)
	}
	if len(out.Folders) != 1 || out.Folders[0].Folder.Name != "Brand assets" || out.Folders[0].Scan == nil || out.Folders[0].Scan.Added != 1 {
		t.Fatalf("folders %+v", out.Folders)
	}
	if out.Summary != "1 clip added, 1 resource added, 1 folder linked, 2 files couldn't be added." {
		t.Fatalf("summary %q", out.Summary)
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), clip.AssetID, &as); err != nil || !slices.Contains(as.ProjectIDs, p.ID) {
		t.Fatalf("the clip is in the project: %+v %v", as.ProjectIDs, err)
	}

	// Again: the same files are already there; a folder's files are added
	// once when copied rather than linked; Workspace keeps pictures out of
	// the project.
	again := call[intake.AddPathsOut](t, a, "add.paths", intake.AddPathsIn{ProjectID: p.ID, Paths: []string{logo, wav}})
	if f := fileNamed(again.Files, "birdsong.wav"); f == nil || f.Status != "deduped" || f.Message != "birdsong.wav was already there." {
		t.Fatalf("footage again: %+v", f)
	}
	if f := fileNamed(again.Files, "garden-logo.png"); f == nil || f.Status != resources.StatusSame {
		t.Fatalf("picture again: %+v", f)
	}
	if again.Summary != "2 files were already there." {
		t.Fatalf("summary again %q", again.Summary)
	}
	copied := filepath.Join(t.TempDir(), "Stills")
	if err := os.MkdirAll(copied, 0o755); err != nil {
		t.Fatal(err)
	}
	logoPNG(t, copied, "pond.png", color.RGBA{20, 80, 200, 255})
	if err := os.WriteFile(filepath.Join(copied, ".DS_Store"), []byte("finder"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp := call[intake.AddPathsOut](t, a, "add.paths", intake.AddPathsIn{ProjectID: p.ID, Paths: []string{copied}, Folders: "copy", Workspace: true})
	if len(cp.Folders) != 0 || len(cp.Files) != 1 || cp.Files[0].Name != "pond.png" || cp.Files[0].Status != resources.StatusAdded {
		t.Fatalf("copied folder: %+v", cp)
	}
	var pond models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), cp.Files[0].ResourceID, &pond); err != nil || pond.Scope != models.ScopeWorkspace || pond.ProjectID != "" {
		t.Fatalf("a workspace picture: %+v %v", pond, err)
	}

	if err := callErr(t, a, "add.paths", intake.AddPathsIn{ProjectID: "No such project", Paths: []string{logo}}, person); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("unknown project: %v", err)
	}
}

// `metarush resources add`: names, titles and tags for one file, a new
// version of a resource by name, footage turned away to the Add box, a
// picture that can't be read, a folder linked.
func TestResourcesAddNamesVersionsAndRefusals(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := newProject(t, a, "Brand")
	dir := t.TempDir()
	first := logoPNG(t, dir, "mark-v1.png", color.RGBA{220, 30, 120, 255})
	out := call[intake.AddPathsOut](t, a, "resources.add", intake.ResourcesAddIn{ProjectID: "Brand", Paths: []string{first}, Name: "brand-mark", Title: "Brand mark",
		Tags: []string{"Logo"}, Description: "The round mark.", License: "Ours"})
	if len(out.Files) != 1 || out.Files[0].Status != resources.StatusAdded || out.Summary != "1 resource added." {
		t.Fatalf("added %+v", out)
	}
	var r models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), out.Files[0].ResourceID, &r); err != nil {
		t.Fatal(err)
	}
	if r.Name != "brand-mark" || r.Title != "Brand mark" || r.ProjectID != p.ID || r.Description != "The round mark." || r.License != "Ours" || !slices.Contains(r.Tags, "logo") {
		t.Fatalf("resource %+v", r)
	}

	// A new version of it, by its short name.
	second := logoPNG(t, dir, "mark-v2.png", color.RGBA{20, 90, 220, 255})
	v := call[intake.AddPathsOut](t, a, "resources.add", intake.ResourcesAddIn{ProjectID: p.ID, Paths: []string{second}, Version: "brand-mark"})
	if len(v.Files) != 1 || v.Files[0].Status != resources.StatusVersion || v.Files[0].ResourceID != r.ID {
		t.Fatalf("version %+v", v.Files)
	}
	if err := callErr(t, a, "resources.add", intake.ResourcesAddIn{ProjectID: p.ID, Paths: []string{second}, Version: "no-such-resource"}, person); err == nil {
		t.Fatal("a version of a resource that isn't there was accepted")
	}

	// Footage is turned away (it belongs with the footage), a broken picture
	// says why, a missing file says so, and a folder is linked.
	wav := tone(t, dir, "voice.wav", 0.5, 220)
	broken := filepath.Join(dir, "broken.png")
	if err := os.WriteFile(broken, append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 64)...), 0o644); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "Logos")
	if err := os.MkdirAll(folder, 0o755); err != nil {
		t.Fatal(err)
	}
	mixed := call[intake.AddPathsOut](t, a, "resources.add", intake.ResourcesAddIn{ProjectID: p.ID, Workspace: true, Paths: []string{wav, broken, filepath.Join(dir, "gone.png"), folder}})
	if f := fileNamed(mixed.Files, "voice.wav"); f == nil || f.Status != "skipped" || f.Route != resources.RouteFootage ||
		f.Message != "voice.wav is a video or sound file, so it belongs with your footage: drop it on the Add box instead." {
		t.Fatalf("footage: %+v", f)
	}
	if f := fileNamed(mixed.Files, "broken.png"); f == nil || f.Status != "failed" || !strings.HasPrefix(f.Message, "broken.png") || strings.Contains(f.Message, dir) {
		t.Fatalf("broken picture: %+v", f)
	}
	if f := fileNamed(mixed.Files, "gone.png"); f == nil || f.Status != "failed" || f.Message != "Can't find gone.png." {
		t.Fatalf("missing: %+v", f)
	}
	if len(mixed.Folders) != 1 || mixed.Folders[0].Folder.Scope != models.ScopeWorkspace {
		t.Fatalf("a workspace folder: %+v", mixed.Folders)
	}
	if mixed.Summary != "1 folder linked, 2 files couldn't be added." {
		t.Fatalf("summary %q", mixed.Summary)
	}
	// A folder that can't be linked (MetaRush's own) is a failed line, not an error.
	own := call[intake.AddPathsOut](t, a, "resources.add", intake.ResourcesAddIn{Paths: []string{a.Cfg.Home()}})
	if len(own.Files) != 1 || own.Files[0].Status != "failed" || len(own.Folders) != 0 {
		t.Fatalf("own folder: %+v", own)
	}
	if err := callErr(t, a, "resources.add", intake.ResourcesAddIn{ProjectID: "No such project", Paths: []string{first}}, person); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("unknown project: %v", err)
	}
}
