package version

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// scriptRepo is a throwaway git repository with scripts/version.sh and
// scripts/bump-version.sh, a VERSION and a CHANGELOG.md, and a bare "origin"
// cloned from it (never pushed to: the test checks that nothing reaches it).
type scriptRepo struct {
	t          *testing.T
	dir, plain string
	env        []string
}

func newScriptRepo(t *testing.T) *scriptRepo {
	t.Helper()
	for _, tool := range []string{"bash", "git", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not installed", tool)
		}
	}
	base := t.TempDir()
	r := &scriptRepo{t: t, dir: filepath.Join(base, "repo"), plain: filepath.Join(base, "origin.git"),
		// The person's own git settings (signing, hooks, identity) stay out of it.
		env: append(os.Environ(), "GIT_CONFIG_GLOBAL="+filepath.Join(base, "gitconfig"), "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C")}
	if err := os.MkdirAll(filepath.Join(r.dir, "scripts"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"version.sh", "bump-version.sh"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "scripts", s))
		if err != nil {
			t.Fatal(err)
		}
		r.write("scripts/"+s, string(b))
		if err := os.Chmod(filepath.Join(r.dir, "scripts", s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.write("VERSION", "0.2.0\n")
	r.write("CHANGELOG.md", "# Changelog\n\n## Unreleased\n- **Faces are recognized across clips** (D114). Name a face once.\n- For developers: `internal/onnx`.\n\n## 0.1.0 — 2026-10-17 (Tier A: local vertical slice)\n- Single Go binary.\n")
	r.write("main.go", "package main\n")
	r.git("init", "-q", "-b", "main")
	r.git("config", "user.email", "release@example.com")
	r.git("config", "user.name", "Release Test")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "start")
	cmd := exec.Command("git", "clone", "-q", "--bare", r.dir, r.plain)
	cmd.Env = r.env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --bare: %v\n%s", err, out)
	}
	r.git("remote", "add", "origin", r.plain)
	return r
}

func (r *scriptRepo) write(name, s string) {
	r.t.Helper()
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(s), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *scriptRepo) run(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir, cmd.Env = r.dir, r.env
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (r *scriptRepo) git(args ...string) string {
	r.t.Helper()
	out, err := r.run("git", args...)
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func (r *scriptRepo) version(args ...string) string {
	r.t.Helper()
	out, err := r.run("scripts/version.sh", args...)
	if err != nil {
		r.t.Fatalf("version.sh %v: %v\n%s", args, err, out)
	}
	return out
}

func (r *scriptRepo) bump(wantOK bool, wantText string, args ...string) {
	r.t.Helper()
	out, err := r.run("scripts/bump-version.sh", args...)
	if (err == nil) != wantOK || !strings.Contains(out, wantText) {
		r.t.Fatalf("bump-version.sh %v: err %v, want ok=%v and %q in:\n%s", args, err, wantOK, wantText, out)
	}
}

func (r *scriptRepo) read(name string) string {
	r.t.Helper()
	b, err := os.ReadFile(filepath.Join(r.dir, name))
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

func TestVersionScriptsCutARelease(t *testing.T) {
	r := newScriptRepo(t)
	start := r.git("rev-parse", "HEAD")
	if v := r.version(); v != "0.2.0-dev" {
		t.Fatalf("before any tag: %s", v)
	}
	if j := r.version("--json"); !strings.Contains(j, `"version": "0.2.0-dev"`) || !strings.Contains(j, `"devCount": null`) || !strings.Contains(j, `"commit": "`+start[:7]+`"`) {
		t.Errorf("--json: %s", j)
	}

	// Refused: a bump from a VERSION nobody tagged yet, uncommitted changes, an older release.
	r.bump(false, "no tag yet: cut it as it is with scripts/bump-version.sh 0.2.0", "minor")
	r.write("notes.txt", "x")
	r.bump(false, "uncommitted changes", "0.2.0")
	if err := os.Remove(filepath.Join(r.dir, "notes.txt")); err != nil {
		t.Fatal(err)
	}
	r.bump(false, "not newer than VERSION", "0.1.9")
	r.bump(true, "Dry run: nothing changed", "0.2.0", "--dry-run")
	if s := r.git("status", "--porcelain"); s != "" || r.git("tag", "-l") != "" {
		t.Fatalf("the dry run changed something: %q", s)
	}

	// The first release: VERSION already says 0.2.0.
	r.bump(true, "a build of this commit says 0.2.0", "0.2.0")
	day := time.Now().Format("2 Jan 2006")
	cl := r.read("CHANGELOG.md")
	if want := "## Unreleased\n\n## 0.2.0 — " + day + "\n- **Faces are recognized across clips** (D114). Name a face once.\n- For developers: `internal/onnx`.\n\n## 0.1.0 — 2026-10-17"; !strings.Contains(cl, want) {
		t.Fatalf("CHANGELOG.md:\n%s\nwant:\n%s", cl, want)
	}
	n := ParseChangelog(cl)
	if u, s := n.Section(""), n.Section("0.2.0"); u == nil || len(u.Entries) != 0 || s == nil || len(s.Entries) != 2 || s.Date != time.Now().Format("2006-01-02") {
		t.Fatalf("parsed: %+v", n)
	}
	if v := strings.TrimSpace(r.read("VERSION")); v != "0.2.0" {
		t.Errorf("VERSION = %q", v)
	}
	if head, tag := r.git("rev-parse", "HEAD"), r.git("rev-parse", "v0.2.0^{commit}"); head != tag || r.git("log", "-1", "--format=%s") != "MetaRush 0.2.0" {
		t.Errorf("the tag is not on the release commit (%s vs %s)", head, tag)
	}
	if r.git("cat-file", "-t", "v0.2.0") != "tag" {
		t.Error("v0.2.0 is not an annotated tag")
	}
	// Nothing was pushed.
	if out, _ := r.run("git", "--git-dir", r.plain, "rev-parse", "main"); out != start {
		t.Errorf("origin moved to %s", out)
	}
	if out, _ := r.run("git", "--git-dir", r.plain, "tag", "-l"); out != "" {
		t.Errorf("origin has tags: %s", out)
	}

	// Builds: at the tag, with uncommitted changes, and after it.
	if v := r.version(); v != "0.2.0" {
		t.Errorf("at the tag: %s", v)
	}
	if j := r.version("--json"); !strings.Contains(j, `"devCount": null`) {
		t.Errorf("a release has no dev count: %s", j)
	}
	r.write("main.go", "package main\n\nfunc main() {}\n")
	if v, c := r.version(), r.version("--commit"); v != "0.2.0-dev.0" || !strings.HasSuffix(c, "-dirty") {
		t.Errorf("uncommitted changes at the tag: %s %s", v, c)
	}
	r.git("commit", "-q", "-am", "a change")
	if v := r.version(); v != "0.2.0-dev.1" {
		t.Errorf("one commit after the tag: %s", v)
	}
	if l := r.version("--ldflags"); !strings.Contains(l, "internal/version.Version=0.2.0-dev.1 ") || !strings.Contains(l, "internal/version.Commit="+r.git("rev-parse", "--short=7", "HEAD")+" ") || !strings.Contains(l, "internal/version.Date=20") {
		t.Errorf("--ldflags: %s", l)
	}

	// The next release.
	r.bump(false, "released already", "0.2.0")
	r.bump(false, `nothing under "## Unreleased"`, "patch")
	cl = strings.Replace(r.read("CHANGELOG.md"), "## Unreleased\n", "## Unreleased\n- **A fix.**\n", 1)
	r.write("CHANGELOG.md", cl)
	r.git("commit", "-q", "-am", "changelog")
	r.bump(true, "tag v0.2.1", "patch")
	if v := r.version(); v != "0.2.1" {
		t.Errorf("after the patch release: %s", v)
	}
	if s := ParseChangelog(r.read("CHANGELOG.md")).Section("0.2.1"); s == nil || len(s.Entries) != 1 || s.Entries[0].Headline != "A fix" {
		t.Errorf("0.2.1 notes: %+v", s)
	}
	r.bump(false, "released already", "v0.2.1")
	r.git("tag", "v0.3.0")
	r.bump(false, "the tag v0.3.0 exists already", "minor")
}
