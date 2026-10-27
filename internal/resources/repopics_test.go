package resources

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func TestCleanPicturePath(t *testing.T) {
	for in, want := range map[string]string{"art/characters": "art/characters", "./art/characters/": "art/characters", " /brand ": "brand",
		"": WholeRepo, ".": WholeRepo, "/": WholeRepo, `art\icons`: "art/icons", "logo.svg": "logo.svg"} {
		got, err := CleanPicturePath(in)
		if err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"../secrets", "art/../..", ".git/config", "art/*.png", "a//b", "art/\x01x", strings.Repeat("a/", 300)} {
		if _, err := CleanPicturePath(bad); err == nil || apperr.From(err).Code != apperr.InvalidInput {
			t.Errorf("%q should be refused: %v", bad, err)
		}
	}
	if !UnderPicturePath("art/characters/alice.png", "art") || !UnderPicturePath("art", "art") || UnderPicturePath("artwork/x.png", "art") || !UnderPicturePath("x.png", WholeRepo) {
		t.Fatal("under")
	}
	if got := PicturePathOf("art/characters/alice.png", []string{"art", "art/characters", "brand"}); got != "art/characters" {
		t.Fatalf("the narrowest path: %q", got)
	}
}

func TestChangePicturePaths(t *testing.T) {
	ch, err := ChangePicturePaths([]string{"art/characters", "brand"}, []string{"art", "brand/logo.svg", "icons/"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// "art" holds art/characters (it takes its place); brand/logo.svg is in brand already.
	if strings.Join(ch.Paths, ",") != "art,brand,icons" || strings.Join(ch.Added, ",") != "art,icons" || len(ch.Notes) != 1 || !strings.Contains(ch.Notes[0], "already kept: it is in “brand”") {
		t.Fatalf("%+v", ch)
	}
	ch, err = ChangePicturePaths(ch.Paths, nil, []string{"brand"})
	if err != nil || strings.Join(ch.Paths, ",") != "art,icons" || strings.Join(ch.Dropped, ",") != "brand" {
		t.Fatalf("drop: %+v %v", ch, err)
	}
	if _, err := ChangePicturePaths(ch.Paths, nil, []string{"elsewhere"}); err == nil || !strings.Contains(err.Error(), "doesn't keep pictures from “elsewhere”") {
		t.Fatalf("dropping a path it doesn't keep: %v", err)
	}
	var many []string
	for i := 0; i <= MaxPicturePaths; i++ {
		many = append(many, "art/"+string(rune('a'+i)))
	}
	if _, err := ChangePicturePaths(nil, many, nil); err == nil {
		t.Fatal("too many paths")
	}
	if !SamePicturePaths([]string{"b", "a"}, []string{"a", "b"}) || SamePicturePaths([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("same paths")
	}
}

// Pictures are told by their bytes, never by their names.
func TestClassifyPicture(t *testing.T) {
	art := filepath.Join("..", "..", "tests", "fixtures", "repo-art")
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join(art, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := []struct {
		name  string
		data  []byte
		ext   string
		about string
	}{
		{"alice.png", read("alice.png"), "png", ""},
		{"hero", read("alice.png"), "png", ""}, // no extension: its bytes say PNG
		{"photo.dat", read("bob.jpg"), "jpg", ""},
		{"dot.gif", read("dot.gif"), "gif", ""},
		{"carol.svg", read("carol.svg"), "svg", ""},
		{"riff.webp", append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), make([]byte, 600)...), "webp", ""},
		{"blink.gif", read("blink.gif"), "", "animated GIF"},
		{"eve.png", []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 12\n"), "", "Git LFS"},
		{"notes.png", []byte("not a picture\n"), "", "named like a picture"},
		{"logo.png", read("carol.svg"), "svg", ""}, // an SVG named .png is an SVG
		{"README.md", []byte("# The cast\n"), "", ""},
		{"deck.pdf", []byte("%PDF-1.7\n" + strings.Repeat("x", 600)), "", "a PDF"},
		{"face.heic", append([]byte{0, 0, 0, 0x18, 'f', 't', 'y', 'p', 'h', 'e', 'i', 'c'}, make([]byte, 600)...), "", "HEIC or AVIF"},
		{"tool.bin", append([]byte{0x7f, 'E', 'L', 'F', 0}, make([]byte, 600)...), "", "not a PNG, JPEG, WebP, GIF or SVG picture"},
	}
	for _, c := range cases {
		ext, why := classifyPicture(c.name, c.data)
		if ext != c.ext || (c.about == "" && why != "") || (c.about != "" && !strings.Contains(why, c.about)) {
			t.Errorf("%s: %q %q, want %q %q", c.name, ext, why, c.ext, c.about)
		}
	}
}

// A repository keeps the pictures in its folders as pictures of their own,
// pinned to the commit; a newer commit makes a new version of a changed one,
// adds new files and marks the ones that went away; the same commit again
// keeps nothing new.
func TestRepositoryPictures(t *testing.T) {
	base := t.TempDir()
	makeFixtureRepo(t, base)
	makeFixtureRepo(t, base, "art")
	t.Setenv("METARUSH_GIT_BASE", base)
	a := testapp.New(t)
	a.Cfg.Resources.Repos.PictureMB = 1
	ctx := context.Background()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Teaser", CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	req := FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", ProjectID: p.ID, Pictures: []string{"./art/characters/"}}
	res, err := FetchRepository(ctx, a, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := res.Resources[0]
	v := repo.Latest()
	rs := v.Repo
	if res.Statuses[0] != StatusAdded || strings.Join(repo.PicturePaths, ",") != "art/characters" || strings.Join(rs.PicturePaths, ",") != "art/characters" || res.Pictures != 5 {
		t.Fatalf("repository %+v / %+v / %d", repo.PicturePaths, rs.PicturePaths, res.Pictures)
	}
	var kept []string
	for _, k := range rs.Pictures {
		kept = append(kept, strings.TrimPrefix(k.Path, "art/characters/"))
	}
	if strings.Join(kept, ",") != "alice.png,bob.jpg,carol.svg,hero,more/dot.gif" {
		t.Fatalf("kept %v", kept)
	}
	why := map[string]string{}
	for _, l := range rs.PicturesLeftOut {
		why[strings.TrimPrefix(l.Path, "art/characters/")] = l.Why
	}
	if len(why) != 5 || rs.PicturesSkipped != 5 || !strings.Contains(why["alias.png"], "link") || !strings.Contains(why["big.png"], "larger than 1.0 MB") ||
		!strings.Contains(why["blink.gif"], "animated GIF") || !strings.Contains(why["eve.png"], "Git LFS") || !strings.Contains(why["notes.png"], "named like a picture") {
		t.Fatalf("left out %v (%v)", why, rs.PicturesSkippedWhy)
	}
	notes := strings.Join(res.Notes, " ")
	if !strings.Contains(notes, "Kept 5 pictures from “art/characters”: 5 new.") || !strings.Contains(notes, "5 files under them left out") {
		t.Fatalf("notes %q", notes)
	}
	// Each a picture of its own, next to the repository, that says where it came from.
	pic := func(file string) *models.Resource {
		t.Helper()
		var r models.Resource
		if err := a.DB.C(store.CResources).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "repoFile.repoId": repo.ID, "repoFile.path": "art/characters/" + file}).Decode(&r); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		return &r
	}
	alice := pic("alice.png")
	av := alice.Latest()
	if alice.Kind != models.ResourceImage || alice.Scope != models.ScopeProject || alice.ProjectID != p.ID || alice.Name != "widget-alice" || alice.Title != "alice" ||
		av.Width != 32 || av.Height != 24 || av.Source.Kind != models.FromRepo || av.Source.Provenance() != "github.com/acme/widget@"+rs.Commit[:8]+":art/characters/alice.png" ||
		av.Source.License != "MIT" || len(av.Source.Lineage) != 1 || av.Source.Lineage[0].ResourceID != repo.ID || rs.Pictures[0].SHA256 != av.SHA256 {
		t.Fatalf("alice %+v / %+v", alice, av)
	}
	// The same bytes under another name are a picture of their own.
	if hero := pic("hero"); hero.ID == alice.ID || hero.Latest().SHA256 != av.SHA256 || hero.Name != "widget-hero" {
		t.Fatalf("hero %+v", hero)
	}
	carol := pic("carol.svg")
	cv := carol.Latest()
	stored, err := os.ReadFile(a.Cache.Path(cv.SHA256))
	if err != nil {
		t.Fatal(err)
	}
	if carol.Kind != models.ResourceSVG || cv.OriginalSHA256 == "" || cv.OriginalSHA256 == cv.SHA256 || bytes.Contains(stored, []byte("<script")) || bytes.Contains(stored, []byte("onload")) {
		t.Fatalf("the SVG is cleaned: %+v\n%s", cv, stored)
	}
	// The same commit again: nothing new.
	again, err := FetchRepository(ctx, a, FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", ProjectID: p.ID}, nil)
	if err != nil || again.Statuses[0] != StatusSame || again.Pictures != 0 || len(again.Notes) != 0 {
		t.Fatalf("again %+v %v", again, err)
	}
	// A newer commit: Alice changed, Frank joined, Bob left.
	makeFixtureRepo(t, base, "art-second")
	newer, err := FetchRepository(ctx, a, FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", ProjectID: p.ID, ResourceID: repo.ID}, nil)
	if err != nil || newer.Statuses[0] != StatusVersion {
		t.Fatalf("newer %+v %v", newer, err)
	}
	nv := newer.Resources[0].Latest().Repo
	kept = nil
	for _, k := range nv.Pictures {
		kept = append(kept, strings.TrimPrefix(k.Path, "art/characters/"))
	}
	if strings.Join(kept, ",") != "alice.png,carol.svg,frank.png,hero,more/dot.gif" || nv.Commit == rs.Commit {
		t.Fatalf("kept at the newer commit %v", kept)
	}
	alice2 := pic("alice.png")
	if alice2.ID != alice.ID || len(alice2.Versions) != 2 || alice2.Current == av.SHA256 || alice2.Latest().Source.Commit != nv.Commit || nv.Pictures[0].SHA256 != alice2.Current {
		t.Fatalf("a new version of the same picture: %+v", alice2)
	}
	if bob := pic("bob.jpg"); !bob.Missing || bob.MissingSince == nil || len(bob.Versions) != 1 {
		t.Fatalf("bob is marked, never removed: %+v", bob)
	}
	if hero := pic("hero"); len(hero.Versions) != 1 || hero.Missing {
		t.Fatalf("hero is the same: %+v", hero)
	}
	notes = strings.Join(newer.Notes, " ")
	if !strings.Contains(notes, "1 new, 1 changed, 3 the same") || !strings.Contains(notes, "1 picture no longer in the repository (“bob”)") {
		t.Fatalf("notes %q", notes)
	}
	// What a later look keeps: only the paths still kept.
	if got := KeptPictures(newer.Resources[0], newer.Resources[0].Latest(), "art/characters/more"); len(got) != 1 || got[0].Path != "art/characters/more/dot.gif" {
		t.Fatalf("under a folder %+v", got)
	}
	r2 := *newer.Resources[0]
	r2.PicturePaths = []string{"art/characters/more"}
	if got := KeptPictures(&r2, r2.Latest(), ""); len(got) != 1 {
		t.Fatalf("only what it keeps %+v", got)
	}
}

// One fetch keeps at most so many pictures: the rest are left out, and said so.
func TestPicturesPastTheLimit(t *testing.T) {
	base := t.TempDir()
	makeFixtureRepo(t, base)
	makeFixtureRepo(t, base, "art")
	t.Setenv("METARUSH_GIT_BASE", base)
	a := testapp.New(t)
	a.Cfg.Resources.Repos.Pictures = 2
	res, err := FetchRepository(context.Background(), a, FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", Workspace: true, Pictures: []string{"art"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	rs := res.Resources[0].Latest().Repo
	past := 0
	for _, l := range rs.PicturesLeftOut {
		if strings.HasPrefix(l.Why, "past what one fetch keeps (2 pictures") {
			past++
		}
	}
	// In path order: alice and bob (hero has alice's bytes, but comes later).
	if res.Pictures != 2 || len(rs.Pictures) != 2 || rs.Pictures[0].Path != "art/characters/alice.png" || rs.Pictures[1].Path != "art/characters/bob.jpg" || past != 3 {
		t.Fatalf("kept %d %+v, left out %+v", res.Pictures, rs.Pictures, rs.PicturesLeftOut)
	}
}

// One picture per repository file, even when two fetches keep it at once:
// the second insert finds the file's picture (and AddFile adds a version to
// it); another file of the same name takes the next name.
func TestOnePicturePerRepositoryFile(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	file := &models.ResourceRepoFile{RepoID: "res_x", Path: "art/a.png"}
	first := models.Resource{ID: ids.New(ids.Resource), WorkspaceID: a.WorkspaceID(), Name: "widget-a", Title: "a", Kind: models.ResourceImage, Scope: models.ScopeWorkspace,
		RepoFile: file, Versions: []models.ResourceVersion{}, CreatedAt: now, UpdatedAt: now}
	if err := insertUnique(ctx, a, &first, false); err != nil {
		t.Fatal(err)
	}
	kept := func(f *models.ResourceRepoFile) func() bool {
		return func() bool { return repoFileResource(ctx, a, f.RepoID, f.Path) != nil }
	}
	second := first
	second.ID = ids.New(ids.Resource)
	if err := insertUniqueOr(ctx, a, &second, false, kept(file)); !errors.Is(err, errFileKept) {
		t.Fatalf("the same file twice: %v", err)
	}
	third := first
	third.ID, third.RepoFile = ids.New(ids.Resource), &models.ResourceRepoFile{RepoID: "res_x", Path: "art/more/a.png"}
	if err := insertUniqueOr(ctx, a, &third, false, kept(third.RepoFile)); err != nil || third.Name != "widget-a-2" {
		t.Fatalf("another file of that name: %v %q", err, third.Name)
	}
}

// Keeping another folder's pictures fetches the commit the repository shows
// and keeps them, without a new version of its files.
func TestPicturesOnlyAtTheCommitItShows(t *testing.T) {
	base := t.TempDir()
	makeFixtureRepo(t, base)
	makeFixtureRepo(t, base, "art")
	t.Setenv("METARUSH_GIT_BASE", base)
	a := testapp.New(t)
	ctx := context.Background()
	res, err := FetchRepository(ctx, a, FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", Workspace: true}, nil)
	if err != nil || res.Pictures != 0 {
		t.Fatalf("%+v %v", res, err)
	}
	repo := res.Resources[0]
	commit := repo.Latest().Repo.Commit
	if err := a.DB.Update(ctx, store.CResources, a.WorkspaceID(), repo.ID, bson.M{"$set": bson.M{"picturePaths": []string{"art/characters/more"}}}); err != nil {
		t.Fatal(err)
	}
	q, err := QueueFetch(ctx, a, FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", Ref: commit, ResourceID: repo.ID, Workspace: true, PicturesOnly: true})
	if err != nil || q.Status != "fetching" || !strings.Contains(q.Message, "the pictures of acme/widget") {
		t.Fatalf("queued %+v %v", q, err)
	}
	var j models.Job
	if err := a.DB.GetByID(ctx, store.CJobs, a.WorkspaceID(), q.JobID, &j); err != nil {
		t.Fatal(err)
	}
	// The same request again is the same job; a refresh is not.
	if again, _ := QueueFetch(ctx, a, FetchPayload(&j)); again.JobID != j.ID {
		t.Fatalf("the same fetch %+v", again)
	}
	if other, _ := QueueFetch(ctx, a, FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", ResourceID: repo.ID, Workspace: true}); other.JobID == j.ID {
		t.Fatal("a refresh is another fetch")
	}
	out, err := RunFetch(ctx, a, &j, nil)
	if err != nil {
		t.Fatal(err)
	}
	var after models.Resource
	_ = a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), repo.ID, &after)
	v := after.Latest()
	if out.Pictures != 1 || len(after.Versions) != 1 || len(v.Repo.Pictures) != 1 || v.Repo.Pictures[0].Path != "art/characters/more/dot.gif" ||
		!strings.Contains(out.Message, "Kept the pictures of acme/widget") {
		t.Fatalf("%+v / %+v", out, v.Repo)
	}
	var dot models.Resource
	_ = a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), v.Repo.Pictures[0].ResourceID, &dot)
	if dot.Scope != models.ScopeWorkspace || dot.Kind != models.ResourceImage || dot.RepoFile == nil {
		t.Fatalf("next to the repository, in the workspace library: %+v", dot)
	}
	// Only a repository keeps pictures; a pictures-only fetch needs the commit.
	if _, err := QueueFetch(ctx, a, FetchRequest{Kind: FetchPage, URL: "https://example.com", Workspace: true, Pictures: []string{"art"}}); err == nil {
		t.Fatal("a page keeps no folders' pictures")
	}
	if _, err := QueueFetch(ctx, a, FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", ResourceID: repo.ID, Workspace: true, PicturesOnly: true, Ref: "main"}); err == nil {
		t.Fatal("pictures only, at a commit")
	}
}
