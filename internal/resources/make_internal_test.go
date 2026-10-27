package resources

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// widget fetches the fixture repository acme/widget into a project.
func widget(t *testing.T) (*app.App, models.Project, *models.Resource, string) {
	t.Helper()
	base := t.TempDir()
	makeFixtureRepo(t, base)
	t.Setenv("METARUSH_GIT_BASE", base)
	a := testapp.New(t)
	p := project(t, a, "Widget tour")
	res, err := FetchRepository(context.Background(), a, FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", ProjectID: p.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return a, p, res.Resources[0], base
}

// A repository's files are read from its snapshot; what isn't a file there
// says so.
func TestRepoFilesAndGit(t *testing.T) {
	a, _, repo, base := widget(t)
	ctx := context.Background()
	b, v, err := RepoFile(ctx, a, repo, "", "./internal//queue/queue.go")
	if err != nil || !bytes.Contains(b, []byte("func Run(")) || v.SHA256 != repo.Current {
		t.Fatalf("a file: %v", err)
	}
	if _, _, err := RepoFile(ctx, a, repo, "", ".git/config"); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("git's own files: %v", err)
	}
	if _, _, err := RepoFile(ctx, a, repo, "", "assets/logo.png"); apperr.From(err).Code != apperr.NotFound || !strings.Contains(err.Error(), "it may have been left out") {
		t.Fatalf("a picture the snapshot left out: %v", err)
	}
	if _, _, err := RepoFile(ctx, a, repo, "nope", "README.md"); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("a version it doesn't have: %v", err)
	}

	// The hardened git, for other packages.
	r := RepoRef{Host: "github.com", Owner: "acme", Name: "widget"}
	commit, branch, err := ResolveCommit(ctx, a.Tools, t.TempDir(), r)
	if err != nil || !FullSHA(commit) || branch != "main" || commit != repo.Latest().Repo.Commit {
		t.Fatalf("resolve: %s %s %v", commit, branch, err)
	}
	if c, _, err := ResolveCommit(ctx, a.Tools, t.TempDir(), RepoRef{Host: "github.com", Owner: "acme", Name: "widget", Ref: commit}); err != nil || c != commit {
		t.Fatalf("a full commit resolves to itself: %s %v", c, err)
	}
	bare := filepath.Join(base, "acme", "widget.git")
	entries, err := ListTree(ctx, a.Tools, bare, commit)
	if err != nil || !slices.ContainsFunc(entries, func(e TreeEntry) bool { return e.Path == "README.md" && e.Type == "blob" && e.Size > 0 }) {
		t.Fatalf("tree: %+v %v", entries, err)
	}
	readme := entries[slices.IndexFunc(entries, func(e TreeEntry) bool { return e.Path == "README.md" })]
	var got []byte
	if err := ReadBlobs(ctx, a.Tools, bare, []string{readme.SHA}, func(_ string, data []byte) error { got = data; return nil }); err != nil || !bytes.Contains(got, []byte("Widget")) {
		t.Fatalf("blob: %q %v", got, err)
	}
	var out bytes.Buffer
	if _, err := Git(ctx, a.Tools, bare, nil, &out, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(out.String()) != commit {
		t.Fatalf("git: %q %v", out.String(), err)
	}
	if !strings.HasPrefix(CloneURL(r), "file://") {
		t.Fatalf("the test base: %s", CloneURL(r))
	}
}

// Making a slide, a diagram or a card checks what was asked first, in plain
// words; drawing needs the renderer (not set up in tests), and says so.
func TestMakeChecksWhatWasAsked(t *testing.T) {
	a, p, repo, _ := widget(t)
	ctx := context.Background()
	pic, err := AddFile(ctx, a, writePNG(t, t.TempDir(), "logo.png", testLogo(40, 20)), AddOptions{ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 50001)
	cases := []struct {
		o    MakeOptions
		says string
	}{
		{MakeOptions{Kind: MakeCode, Repo: pic.Resource}, "logo is a picture, not a repository."},
		{MakeOptions{Kind: MakeCode, Repo: repo, RepoSHA: "0000000"}, "acme/widget doesn't have that version any more."},
		{MakeOptions{Kind: MakeCode, Repo: repo, Theme: "sepia"}, "Choose a dark or a light theme."},
		{MakeOptions{Kind: MakeCode}, "A code slide is made from a repository: say which one."},
		{MakeOptions{Kind: MakeCode, Repo: repo, File: "internal/queue/queue.go"}, "internal/queue/queue.go has 81 lines: say which ones to show (at most 80), like lines 40-72."},
		{MakeOptions{Kind: MakeCode, Repo: repo, File: "cmd/widget/main.go", From: 20, To: 40}, "cmd/widget/main.go has 26 lines, so there's no line 40."},
		{MakeOptions{Kind: MakeCode, Repo: repo, File: "internal/queue/queue.go", From: 1, To: 81}, "A code slide shows at most 80 lines (asked for 81): pick the part that matters."},
		{MakeOptions{Kind: MakeCode, Repo: repo, File: "cmd/widget/main.go", From: 3, To: 9, Highlight: "1-2"}, "highlighted lines 1-2 are outside the lines shown (3-9)"},
		{MakeOptions{Kind: MakeCode, Repo: repo, File: "cmd/widget/main.go", Highlight: "two"}, "highlight lines like 12-14,20 (lines of the file)"},
		{MakeOptions{Kind: MakeDiagram, Source: "  "}, "A diagram needs its description, written in Mermaid or D2."},
		{MakeOptions{Kind: MakeDiagram, Source: long}, "That diagram is too long to draw (at most 50,000 characters)."},
		{MakeOptions{Kind: MakeDiagram, Source: "a -> b", Format: "plantuml"}, "A diagram is written in Mermaid or D2."},
		{MakeOptions{Kind: MakeStats}, "A repository card is made from a repository: say which one."},
		{MakeOptions{Kind: "poster"}, "MetaRush makes code slides (code), diagrams (diagram) and repository cards (stats)."},
	}
	for _, c := range cases {
		_, err := Make(ctx, a, c.o)
		if e := apperr.From(err); e.Message != c.says {
			t.Errorf("%+v: %q", c.o.Kind, e.Message)
		}
	}
	// Valid requests reach the drawing, which needs the renderer.
	for _, o := range []MakeOptions{
		{Kind: MakeCode, Repo: repo, File: "cmd/widget/main.go", Highlight: "3-4", Language: "go"},
		{Kind: MakeCode, Repo: repo, RepoSHA: repo.Latest().Repo.Commit[:10], File: "internal/queue/queue.go", From: 5, To: 5},
		{Kind: MakeDiagram, Source: "flowchart LR\n  a --> b", Workspace: true},
		{Kind: MakeStats, Repo: repo},
	} {
		if _, err := Make(ctx, a, o); err == nil || apperr.From(err).Message == "" {
			t.Errorf("%s: %v", o.Kind, err)
		}
	}
	if _, err := Make(ctx, a, MakeOptions{Kind: MakeDiagram, Source: "a -> b: calls", Format: "d2"}); err == nil {
		t.Fatal("d2 can't draw in a test without its program or a renderer")
	}
	if from, to, err := ParseLines(" L40 – L72 "); err != nil || from != 40 || to != 72 {
		t.Fatalf("ParseLines: %d %d %v", from, to, err)
	}
	if _, _, err := ParseLines("9-3"); err == nil {
		t.Fatal("backwards lines")
	}
	if shiftRanges("12-14,20", 2) != "14-16,22" || shiftRanges("3", 0) != "3" || shiftRanges("x", 1) != "" {
		t.Fatal("shiftRanges")
	}
}

// A newer commit makes new versions of what was made from the older one:
// code that moved is followed; code that changed, a file that went and
// anything that can't be drawn now stay as they were, each with a note.
func TestRegenerateFromRepo(t *testing.T) {
	a, p, repo, _ := widget(t)
	ctx := context.Background()
	now := time.Now().UTC()
	made := func(name string, mw models.ResourceMadeWith) {
		t.Helper()
		mw.RepoID = repo.ID
		if mw.RepoSHA == "" {
			mw.RepoSHA = strings.Repeat("0", 64)
		}
		r := models.Resource{ID: ids.New(ids.Resource), WorkspaceID: a.WorkspaceID(), Name: name, Title: name, Kind: models.ResourceImage, Scope: models.ScopeProject,
			ProjectID: p.ID, Current: "sha-" + name, CreatedAt: now, UpdatedAt: now,
			Versions: []models.ResourceVersion{{SHA256: "sha-" + name, Source: models.ResourceSource{Kind: models.FromGenerated, MadeWith: &mw}, CreatedAt: now}}}
		if err := a.DB.Insert(ctx, store.CResources, r); err != nil {
			t.Fatal(err)
		}
	}
	queue, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "repo", "internal", "queue", "queue.go"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(queue), "\n")
	made("moved", models.ResourceMadeWith{Kind: MakeCode, File: "internal/queue/queue.go", From: 40, Snippet: strings.Join(lines[1:4], "\n"), Highlight: "41"})
	made("gone", models.ResourceMadeWith{Kind: MakeCode, File: "internal/old.go", From: 1, Snippet: "package old"})
	made("changed", models.ResourceMadeWith{Kind: MakeCode, File: "internal/queue/queue.go", From: 1, Snippet: "func Removed() {}"})
	made("card", models.ResourceMadeWith{Kind: MakeStats})
	made("diagram", models.ResourceMadeWith{Kind: MakeDiagram})
	made("current", models.ResourceMadeWith{Kind: MakeStats, RepoSHA: repo.Current})
	notes, err := RegenerateFromRepo(ctx, a, repo)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"moved couldn't be made again: ", "gone stays as it was: internal/old.go isn't in the newer commit.",
		"changed stays as it was: its code in internal/queue/queue.go changed. Make it again if you want the new code.", "card couldn't be made again: "}
	for _, w := range want {
		if !slices.ContainsFunc(notes, func(n string) bool { return strings.HasPrefix(n, w) }) {
			t.Errorf("no note %q in %q", w, notes)
		}
	}
	if len(notes) != 4 {
		t.Fatalf("notes %q", notes)
	}
	if n, err := RegenerateFromRepo(ctx, a, &models.Resource{}); err != nil || n != nil {
		t.Fatal("a resource that isn't a repository")
	}
}

// The brand a generated picture uses: the project's Content Settings, else
// MetaRush's defaults; the theme follows its background.
func TestBrandColorsFollowContentSettings(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	if b := brandColors(ctx, a, ""); b != editspec.DefaultBrand {
		t.Fatalf("defaults: %+v", b)
	}
	p := project(t, a, "Launch")
	if _, err := store.MoveContentSettings(ctx, a.DB, a.WorkspaceID()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.C(store.CContentSettings).UpdateOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": p.ID},
		bson.M{"$set": bson.M{"brandKit": models.BrandKit{Primary: "#112233", Background: "#FFFFFF", Font: "Fraunces"}}}); err != nil {
		t.Fatal(err)
	}
	b := brandColors(ctx, a, p.ID)
	if b.Primary != "#112233" || b.Background != "#FFFFFF" || b.Font != "Fraunces" || b.Accent != editspec.DefaultBrand.Accent || themeFor(b.Background) != "light" {
		t.Fatalf("the project's brand: %+v", b)
	}
}
