package resources

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return strings.TrimSpace(string(out))
}

// A folder at a commit (D485): only the repository's own commits (a branch's
// or a tag's, never a copy's that GitHub would hand out under its address),
// and only what the folder needs is fetched, each step within its limit.
func TestFolderAtFetchesOnlyTheRepositorysOwnFolder(t *testing.T) {
	base := t.TempDir()
	makeFixtureRepo(t, base)
	makeFixtureRepo(t, base, "ability")
	t.Setenv("METARUSH_GIT_BASE", base)
	bare := filepath.Join(base, "acme", "widget.git")
	commit := gitOut(t, bare, "rev-parse", "main")
	ctx := context.Background()
	ts := tools.New(config.Tools{})
	lim := FolderLimits{Files: 256, FileBytes: 8 << 20, TotalBytes: 32 << 20, Skip: func(rel string) bool { return strings.HasPrefix(rel, ".") }}
	const repo, folder = "https://github.com/acme/widget", "abilities/title-card"

	work := t.TempDir()
	files, err := FolderAt(ctx, ts, work, repo, commit, folder, lim)
	if err != nil || len(files) != 3 {
		t.Fatalf("the folder: %+v %v", files, err)
	}
	for _, f := range files {
		if f.Size != int64(len(f.Data)) || len(f.Data) == 0 {
			t.Fatalf("%s read as %d bytes (says %d)", f.Path, len(f.Data), f.Size)
		}
	}
	// Nothing else of the repository came: not its large test file.
	big := gitOut(t, bare, "rev-parse", commit+":testdata/big.txt")
	check := exec.Command("git", "-C", filepath.Join(work, "repo.git"), "cat-file", "-e", big)
	check.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	if check.Run() == nil {
		t.Fatal("a file outside the folder was fetched")
	}

	// A commit only a pull request holds (a copy's): refused.
	src := filepath.Join(t.TempDir(), "src")
	if out, err := exec.Command("git", "-c", "protocol.file.allow=always", "clone", "-q", bare, src).CombinedOutput(); err != nil {
		t.Fatalf("clone: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(src, folder, "make.py"), []byte("print('a copy')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"commit", "-qam", "A copy's change"}, {"push", "-q", "origin", "HEAD:refs/pull/7/head"}} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=Ada", "-c", "user.email=ada@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null",
			"-c", "protocol.file.allow=always"}, args...)...)
		cmd.Dir = src
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	imposter := gitOut(t, src, "rev-parse", "HEAD")
	if _, err := FolderAt(ctx, ts, t.TempDir(), repo, imposter, folder, lim); apperr.From(err).Code != apperr.Integrity || !strings.Contains(apperr.From(err).Message, "No branch or tag") {
		t.Fatalf("a copy's commit: %v", err)
	}

	// An older commit of a branch (main moved on): its history holds it.
	makeFixtureRepo(t, base, "second")
	if gitOut(t, bare, "rev-parse", "main") == commit {
		t.Fatal("main didn't move")
	}
	if files, err := FolderAt(ctx, ts, t.TempDir(), repo, commit, folder, lim); err != nil || len(files) != 3 {
		t.Fatalf("an older commit of main: %+v %v", files, err)
	}
	// ... within the history's limit.
	oldHistory := historyMax
	historyMax = 1
	if _, err := FolderAt(ctx, ts, t.TempDir(), repo, commit, folder, lim); err == nil || !strings.Contains(apperr.From(err).Message, "history") {
		t.Fatalf("a history past its limit: %v", err)
	}
	historyMax = oldHistory

	// A tag holds the copy's commit now: it is the repository's.
	if out, err := exec.Command("git", "-C", bare, "tag", "v-copy", imposter).CombinedOutput(); err != nil {
		t.Fatalf("tag: %v %s", err, out)
	}
	if files, err := FolderAt(ctx, ts, t.TempDir(), repo, imposter, folder, lim); err != nil || len(files) != 3 {
		t.Fatalf("a tag's commit: %+v %v", files, err)
	}

	// The commit's folders and the folder's files each have a limit.
	oldFolders := foldersMax
	foldersMax = 1
	if _, err := FolderAt(ctx, ts, t.TempDir(), repo, commit, folder, lim); err == nil || !strings.Contains(apperr.From(err).Message, "more folders") {
		t.Fatalf("folders past their limit: %v", err)
	}
	foldersMax = oldFolders
	small := lim
	small.TotalBytes = 100
	if _, err := FolderAt(ctx, ts, t.TempDir(), repo, commit, folder, small); err == nil || !strings.Contains(apperr.From(err).Message, "add up to more than") {
		t.Fatalf("files past their limit: %v", err)
	}
	// The fetch itself stops at its limit.
	partial := filepath.Join(t.TempDir(), "p.git")
	r, _ := ParseRepoURL(repo)
	if err := partialRepo(ctx, ts, filepath.Dir(partial), partial, r, "blob:none"); err != nil {
		t.Fatal(err)
	}
	if err := fetchWithin(withoutLazyFetch(ctx), ts, partial, 1, nil, "fetch", "--quiet", "--no-tags", "--depth=1", "origin", commit); err != ErrFetchTooLarge {
		t.Fatalf("a fetch past its limit: %v", err)
	}
}

// Files that pack small but are large are refused by their sizes, which git
// tells before anything of them is read: memory stays small (a fetch's limit
// counts packed bytes, so it can't tell).
func TestFolderAtSizesFilesBeforeReadingThem(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	base := t.TempDir()
	bare := filepath.Join(base, "acme", "widget.git")
	g := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=Ada", "-c", "user.email=ada@example.invalid", "-c", "commit.gpgsign=false",
			"-c", "core.hooksPath=/dev/null", "-c", "protocol.file.allow=always", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	if err := os.MkdirAll(filepath.Dir(bare), 0o755); err != nil {
		t.Fatal(err)
	}
	g(base, "init", "-q", "--bare", bare)
	g(bare, "config", "uploadpack.allowFilter", "true")
	g(bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	work := filepath.Join(t.TempDir(), "w")
	g(filepath.Dir(work), "init", "-q", work)
	dir := filepath.Join(work, "abilities", "card")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ability.yaml"), []byte("ability: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	zeros := make([]byte, 6<<20)
	for i := 0; i < 3; i++ { // 3 × 6 MB of zeros: a few KB each, packed
		zeros[0] = byte('a' + i)
		if err := os.WriteFile(filepath.Join(dir, "data"+string(rune('a'+i))+".bin"), zeros, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	g(work, "add", "-A")
	g(work, "commit", "-qm", "large files that pack small")
	g(work, "push", "-q", bare, "HEAD:refs/heads/main")
	commit := g(work, "rev-parse", "HEAD")
	t.Setenv("METARUSH_GIT_BASE", base)
	ts := tools.New(config.Tools{})
	ctx := context.Background()
	for what, c := range map[string]struct {
		lim  FolderLimits
		want string
	}{
		"one file past its limit": {FolderLimits{Files: 256, FileBytes: 1 << 20, TotalBytes: 64 << 20}, "is 6.0 MB"},
		"the files past theirs":   {FolderLimits{Files: 256, FileBytes: 8 << 20, TotalBytes: 10 << 20}, "add up to more than 10.0 MB"},
	} {
		runtime.GC()
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		_, err := FolderAt(ctx, ts, t.TempDir(), "https://github.com/acme/widget", commit, "abilities/card", c.lim)
		runtime.ReadMemStats(&m1)
		if err == nil || !strings.Contains(apperr.From(err).Message, c.want) {
			t.Errorf("%s: %v", what, err)
		}
		if grew := int64(m1.TotalAlloc - m0.TotalAlloc); grew > 4<<20 {
			t.Errorf("%s: %d MB read into memory (a file is 6 MB)", what, grew>>20)
		}
	}
}
