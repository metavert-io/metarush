package abilities

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/tools"
)

// gitIn runs git in a folder with a fixed identity and no settings of whoever
// runs the test.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "init.defaultBranch=main", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
		"-c", "protocol.file.allow=always", "-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+t.TempDir(), "GIT_AUTHOR_NAME=Ada Park", "GIT_AUTHOR_EMAIL=ada@example.invalid",
		"GIT_COMMITTER_NAME=Ada Park", "GIT_COMMITTER_EMAIL=ada@example.invalid", "GIT_AUTHOR_DATE=2026-10-20T10:00:00Z", "GIT_COMMITTER_DATE=2026-10-20T10:00:00Z")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	if err := os.MkdirAll(to, 0o755); err != nil {
		t.Fatal(err)
	}
	es, err := os.ReadDir(from)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		b, err := os.ReadFile(filepath.Join(from, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(to, e.Name()), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// A maintainer's repository: abilities/title-card committed, origin on
// GitHub (a bare repository here stands in for it through METARUSH_GIT_BASE).
func maintainerRepo(t *testing.T) (work, base string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	base = t.TempDir()
	work = filepath.Join(t.TempDir(), "widget")
	copyDir(t, filepath.Join("..", "..", "tests", "fixtures", "ability-repo", "title-card"), filepath.Join(work, "abilities", "title-card"))
	if err := os.WriteFile(filepath.Join(work, "abilities", "title-card", ".DS_Store"), []byte("left out"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "init", "-q")
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-qm", "The title card")
	gitIn(t, work, "remote", "add", "origin", "git@github.com:acme/widget.git")
	if err := os.MkdirAll(filepath.Join(base, "acme"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "clone", "-q", "--bare", work, filepath.Join(base, "acme", "widget.git"))
	return work, base
}

// The generator pins each ability folder to its repository's commit and its
// archive's version, only when the commit holds exactly the folder's files,
// and writes what MetaRush reads whole; what it writes imports (D487).
func TestMakeACatalogue(t *testing.T) {
	work, base := maintainerRepo(t)
	ctx := context.Background()
	ts := tools.New(config.Tools{})
	out := filepath.Join(t.TempDir(), "index.json")
	if _, err := MakeIndex(ctx, ts, MakeIn{Folder: filepath.Join(work, "abilities"), Out: out}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("a new index needs a name and a title: %v", err)
	}
	made, err := MakeIndex(ctx, ts, MakeIn{Folder: filepath.Join(work, "abilities"), Out: out, Name: "acme", Title: "Acme abilities", Maintainer: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	head := gitIn(t, work, "rev-parse", "HEAD")
	hash := cardHash(t)
	if !made.Written || made.Version != 1 || len(made.Listed) != 1 || len(made.LeftOut) != 0 {
		t.Fatalf("made: %+v", made)
	}
	e := made.Index.Abilities[0]
	g := e.Source.Git
	if e.Name != "title-card" || e.Archive != hash || g == nil || g.Commit != head || g.Path != "abilities/title-card" || g.Repository != "https://github.com/acme/widget" ||
		e.Publisher.GitHub != "acme" || e.Cost != "free" || e.Network != NetworkOff || len(e.Programs) != 1 || !strings.Contains(e.Notes, "five or fewer") {
		t.Fatalf("the listing: %+v %+v", e, g)
	}
	if len(made.Warnings) != 1 || !strings.Contains(made.Warnings[0], "push it") {
		t.Fatalf("a commit not pushed is said: %v", made.Warnings)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if cp, err := ParseIndex(b); err != nil || len(cp.Abilities) != 1 || len(cp.LeftOut) != 0 {
		t.Fatalf("what it writes MetaRush reads: %+v %v", cp, err)
	}

	// Again with nothing changed: the same version.
	again, err := MakeIndex(ctx, ts, MakeIn{Folder: filepath.Join(work, "abilities"), Out: out})
	if err != nil || again.Version != 1 || again.Changed {
		t.Fatalf("nothing changed: %+v %v", again, err)
	}
	// A change not committed is left out.
	script := filepath.Join(work, "abilities", "title-card", "make.py")
	if err := os.WriteFile(script, []byte("print('changed')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := MakeIndex(ctx, ts, MakeIn{Folder: filepath.Join(work, "abilities"), Out: out, DryRun: true})
	if err != nil || len(dirty.LeftOut) != 1 || !strings.Contains(dirty.LeftOut[0].Why, "commit them first") || len(dirty.Index.Abilities) != 0 {
		t.Fatalf("a change not committed: %+v %v", dirty, err)
	}
	// Committed: a new version of the index.
	gitIn(t, work, "commit", "-qam", "Change the card")
	next, err := MakeIndex(ctx, ts, MakeIn{Folder: filepath.Join(work, "abilities"), Out: out})
	if err != nil || next.Version != 2 || !next.Changed || next.Index.Abilities[0].Archive == hash || next.Index.Name != "acme" {
		t.Fatalf("a new version: %+v %v", next, err)
	}
	// A folder whose manifest has problems is left out, with why.
	bad := filepath.Join(work, "abilities", "broken")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, ManifestFile), []byte("ability: 1\nname: broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withBad, err := MakeIndex(ctx, ts, MakeIn{Folder: filepath.Join(work, "abilities"), Out: out, DryRun: true})
	if err != nil || len(withBad.LeftOut) != 1 || withBad.LeftOut[0].Name != "broken" || len(withBad.Index.Abilities) != 1 {
		t.Fatalf("a broken manifest: %+v %v", withBad, err)
	}
	// Not an index at --out: refused unless forced.
	other := filepath.Join(t.TempDir(), "notes.json")
	if err := os.WriteFile(other, []byte(`{"precious": true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := MakeIndex(ctx, ts, MakeIn{Folder: filepath.Join(work, "abilities"), Out: other, Name: "acme", Title: "Acme"}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("writing over another file: %v", err)
	}

	// What the generator wrote imports: the same version, fetched from the commit.
	if err := os.RemoveAll(bad); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "push", "-q", filepath.Join(base, "acme", "widget.git"), "main")
	t.Setenv("METARUSH_GIT_BASE", base)
	pushed, err := MakeIndex(ctx, ts, MakeIn{Folder: filepath.Join(work, "abilities"), Out: out})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	ib, _ := os.ReadFile(out)
	if err := json.Unmarshal(ib, &m); err != nil {
		t.Fatal(err)
	}
	cat := serveCatalogue(t, m)
	a := newAppWith(t, cat)
	got := mustCall[AbilityImportOut](t, a, "ability.catalogues.import", AbilityCatalogueImportIn{From: "acme:title-card"}, ops.Caller{Human: true})
	if got.Version != pushed.Index.Abilities[0].Archive || got.State != models.AbilityImported {
		t.Fatalf("the generator's index imports: %+v", got)
	}
}

// The sample catalogue of the starter pack (abilities/catalogue.sample.json)
// is read whole, and — where this checkout has the commit it pins — each
// listing is exactly the archive of its folder there and says what its
// manifest says (D487). Regenerate it with
// metarush ability catalogues make abilities --out abilities/catalogue.sample.json --commit <a commit on main>.
func TestTheSampleCatalogueIsSound(t *testing.T) {
	root := filepath.Join("..", "..")
	b, err := os.ReadFile(filepath.Join(root, "abilities", "catalogue.sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	cp, err := ParseIndex(b)
	if err != nil || len(cp.LeftOut) != 0 || len(cp.Abilities) == 0 {
		t.Fatalf("the sample: %+v %v", cp, err)
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	ts := tools.New(config.Tools{})
	for _, e := range cp.Abilities {
		g := e.Source.Git
		if g == nil {
			t.Fatalf("%s: the sample pins this repository's commits", e.Name)
		}
		if err := exec.Command("git", "-C", root, "cat-file", "-e", g.Commit+"^{commit}").Run(); err != nil {
			t.Logf("this checkout hasn't commit %s (a shallow clone): %s's files aren't compared", g.Commit[:12], e.Name)
			continue
		}
		files, err := resources.FolderAtHere(context.Background(), ts, root, g.Commit, g.Path, folderLimits())
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		arc, err := archiveOfFolder(files)
		if err != nil {
			t.Fatalf("%s: %v", e.Name, err)
		}
		if arc.Hash != e.Archive {
			t.Errorf("%s: the sample names %s, its folder at %s is %s", e.Name, Short(e.Archive), g.Commit[:12], Short(arc.Hash))
			continue
		}
		m, _ := arc.Manifest()
		if diffs := listingDiffers(e, m); len(diffs) > 0 {
			t.Errorf("%s: the listing differs from its manifest: %v", e.Name, diffs)
		}
	}
}
