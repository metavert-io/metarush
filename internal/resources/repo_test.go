package resources

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func TestParseRepoURL(t *testing.T) {
	cases := []struct {
		in                string
		ok                bool
		owner, name, ref  string
		path, canonicalTo string
	}{
		{in: "https://github.com/acme/widget", ok: true, owner: "acme", name: "widget"},
		{in: "github.com/acme/widget.git", ok: true, owner: "acme", name: "widget"},
		{in: "https://www.github.com/Acme-Inc/my.repo_1/", ok: true, owner: "Acme-Inc", name: "my.repo_1"},
		{in: "https://github.com/acme/widget/tree/v2.0/cmd/server", ok: true, owner: "acme", name: "widget", ref: "v2.0", path: "cmd/server"},
		{in: "https://github.com/acme/widget/blob/main/internal/queue/queue.go", ok: true, owner: "acme", name: "widget", ref: "main", path: "internal/queue/queue.go"},
		{in: "https://github.com/acme/widget/commit/" + strings.Repeat("ab", 20), ok: true, owner: "acme", name: "widget", ref: strings.Repeat("ab", 20)},
		{in: "https://github.com/acme/widget/releases/tag/v1.2.3", ok: true, owner: "acme", name: "widget", ref: "v1.2.3"},
		{in: "https://github.com/orgs/acme", ok: false},
		{in: "https://github.com/settings/profile", ok: false},
		{in: "https://github.com/acme", ok: false},
		{in: "https://gitlab.com/acme/widget", ok: false},
		{in: "https://user:pw@github.com/acme/widget", ok: false},
		{in: "ftp://github.com/acme/widget", ok: false},
		{in: "https://github.com/-acme/widget", ok: false},
	}
	for _, c := range cases {
		r, ok := ParseRepoURL(c.in)
		if ok != c.ok {
			t.Errorf("%s: ok=%v, want %v", c.in, ok, c.ok)
			continue
		}
		if !ok {
			continue
		}
		if r.Owner != c.owner || r.Name != c.name || r.Ref != c.ref || r.Path != c.path {
			t.Errorf("%s: got %+v", c.in, r)
		}
		if r.URL() != "https://github.com/"+c.owner+"/"+c.name {
			t.Errorf("%s: canonical %s", c.in, r.URL())
		}
	}
}

func TestLocateSnippet(t *testing.T) {
	file := "a\nb\nfunc Run() {\n\tfor {\n\t}\n}\nz\n"
	snip := "func Run() {\n\tfor {\n\t}\n}"
	if from, to, ok := locateSnippet(file, snip, 3); !ok || from != 3 || to != 6 {
		t.Fatalf("same place: %d-%d %v", from, to, ok)
	}
	moved := "// header\n" + file
	if from, to, ok := locateSnippet(moved, snip, 3); !ok || from != 4 || to != 7 {
		t.Fatalf("moved down: %d-%d %v", from, to, ok)
	}
	if _, _, ok := locateSnippet(strings.Replace(file, "for {", "for i := range 3 {", 1), snip, 3); ok {
		t.Fatal("changed code must not be found")
	}
	if got := shiftRanges("12-14,20", 1); got != "13-15,21" {
		t.Fatalf("shift: %s", got)
	}
	if f, l, err := ParseLines("L40-L72"); err != nil || f != 40 || l != 72 {
		t.Fatalf("lines: %d %d %v", f, l, err)
	}
	if _, _, err := ParseLines("72-40"); err == nil {
		t.Fatal("a backwards range is refused")
	}
	if hl, err := parseHighlight("44-46,60", 40, 72); err != nil || len(hl) != 2 || hl[1] != [2]int{60, 60} {
		t.Fatalf("highlight: %v %v", hl, err)
	}
	if _, err := parseHighlight("10", 40, 72); err == nil {
		t.Fatal("a highlight outside the lines shown is refused")
	}
}

func TestFilesAndLicences(t *testing.T) {
	for p, want := range map[string]string{"cmd/main.go": "Go", "web/App.tsx": "TypeScript", "Dockerfile": "Dockerfile", "Makefile": "Makefile", "notes.txt": ""} {
		if got, _ := LanguageOf(p); got != want {
			t.Errorf("%s: %q, want %q", p, got, want)
		}
	}
	if !isBinaryName("assets/logo.PNG") || isBinaryName("main.go") {
		t.Fatal("binary names")
	}
	if !isBinaryData([]byte("abc\x00def")) || isBinaryData([]byte("plain text\n")) {
		t.Fatal("binary data")
	}
	if !isLFSPointer([]byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 12\n")) {
		t.Fatal("LFS pointer")
	}
	mit, _ := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "repo", "LICENSE"))
	if LicenseFromText(string(mit)) != "MIT" || LicenseFromText("Apache License, Version 2.0") != "Apache-2.0" || LicenseFromText("all rights reserved") != "" {
		t.Fatal("licences")
	}
	for _, bad := range []string{"../x", "a/../b", "/abs", ".git/config", "a//b", ""} {
		if safeRepoPath(bad) {
			t.Errorf("%q should be refused", bad)
		}
	}
	if DiagramFormat("flowchart LR\n A --> B") != "mermaid" || DiagramFormat("%% a note\nsequenceDiagram\n A->>B: hi") != "mermaid" || DiagramFormat("a -> b: hi") != "d2" {
		t.Fatal("diagram formats")
	}
}

// A snapshot can only hold plain files with clean names.
func TestUnpackRefusesUnsafeEntries(t *testing.T) {
	for _, h := range []*tar.Header{
		{Name: "../escape.txt", Mode: 0o644, Size: 1, Typeflag: tar.TypeReg},
		{Name: "link", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink},
		{Name: "dev", Typeflag: tar.TypeChar},
	} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		_ = tw.WriteHeader(h)
		if h.Size > 0 {
			_, _ = tw.Write([]byte("x"))
		}
		_ = tw.Close()
		_ = gz.Close()
		dir := t.TempDir()
		f := filepath.Join(dir, "s.tar.gz")
		_ = os.WriteFile(f, buf.Bytes(), 0o644)
		out := filepath.Join(dir, "out")
		_ = os.MkdirAll(out, 0o755)
		if err := unpackSnapshot(f, out); err == nil {
			t.Errorf("%s (%c) was unpacked", h.Name, h.Typeflag)
		}
	}
}

// makeFixtureRepo builds acme/widget (scripts/make-fixture-repo.sh) under dir.
func makeFixtureRepo(t *testing.T, dir string, step ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	args := append([]string{filepath.Join("..", "..", "scripts", "make-fixture-repo.sh"), dir}, step...)
	if out, err := exec.Command("bash", args...).CombinedOutput(); err != nil {
		t.Fatalf("fixture repo: %v %s", err, out)
	}
}

func TestFetchRepositoryFromFixture(t *testing.T) {
	base := t.TempDir()
	makeFixtureRepo(t, base)
	t.Setenv("METARUSH_GIT_BASE", base)
	a := testapp.New(t)
	ctx := context.Background()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Widget tour", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := a.DB.Insert(ctx, store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	req := FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", ProjectID: p.ID}
	res, err := FetchRepository(ctx, a, req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Resources) != 1 || res.Statuses[0] != StatusAdded {
		t.Fatalf("result: %+v", res)
	}
	r := res.Resources[0]
	v := r.Latest()
	rs := v.Repo
	if r.Kind != models.ResourceRepo || r.Name != "widget" || r.Title != "acme/widget" || r.Scope != models.ScopeProject {
		t.Fatalf("resource: %+v", r)
	}
	if rs.Files != 6 || rs.Skipped != 3 || rs.License != "MIT" || len(rs.Recent) != 2 || rs.Recent[0].Author != "Ada Park" || rs.History != "git" {
		t.Fatalf("snapshot: files %d skipped %d (%v) licence %q recent %+v history %q", rs.Files, rs.Skipped, rs.SkippedWhy, rs.License, rs.Recent, rs.History)
	}
	if len(rs.Languages) == 0 || rs.Languages[0].Name != "Go" {
		t.Fatalf("languages: %+v", rs.Languages)
	}
	if !strings.Contains(rs.Readme, "Widget turns a folder of clips") || v.Source.Commit != rs.Commit || v.Source.Provenance() != "github.com/acme/widget@"+rs.Commit[:8] {
		t.Fatalf("readme %q / provenance %q", rs.Readme, v.Source.Provenance())
	}
	// A read-only copy with the text files only.
	dir, _, err := RepoCheckout(ctx, a, r, "")
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "internal", "queue", "queue.go")); err != nil || !strings.Contains(string(b), "func Run(") {
		t.Fatalf("queue.go: %v", err)
	}
	for _, gone := range []string{"assets/logo.png", "testdata/big.txt", "docs-link.md"} {
		if _, err := os.Lstat(filepath.Join(dir, filepath.FromSlash(gone))); err == nil {
			t.Errorf("%s should have been left out", gone)
		}
	}
	if st, _ := os.Stat(filepath.Join(dir, "README.md")); st == nil || st.Mode().Perm()&0o222 != 0 {
		t.Fatalf("the copy must be read-only: %v", st)
	}
	ov, err := ReadOverview(ctx, a, v)
	if err != nil || len(ov.Files) != 6 || len(ov.Skipped) != 3 {
		t.Fatalf("overview: %+v %v", ov, err)
	}
	// The same commit again: nothing new (the snapshot's bytes are the same).
	again, err := FetchRepository(ctx, a, req, nil)
	if err != nil || again.Statuses[0] != StatusSame || again.Resources[0].ID != r.ID {
		t.Fatalf("again: %+v %v", again, err)
	}
	// A newer commit: a new version of the same resource.
	makeFixtureRepo(t, base, "second")
	req.ResourceID = r.ID
	newer, err := FetchRepository(ctx, a, req, nil)
	if err != nil || newer.Statuses[0] != StatusVersion || len(newer.Resources[0].Versions) != 2 || newer.Resources[0].Latest().Repo.Commit == rs.Commit {
		t.Fatalf("newer: %+v %v", newer, err)
	}
	// A branch that isn't there is a plain refusal.
	_, err = FetchRepository(ctx, a, FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", Ref: "no-such-branch", ProjectID: p.ID}, nil)
	var perm *Permanent
	if err == nil || !errors.As(err, &perm) || !strings.Contains(err.Error(), "no branch or tag") {
		t.Fatalf("missing branch: %v", err)
	}
}
