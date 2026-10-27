package remotion

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// Pushing back (D176), against local bare repositories (file://) and a fake
// gh: nothing is ever sent to GitHub.

// tgit runs git for a test (never on a real remote).
func tgit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Origin", "-c", "user.email=origin@example.invalid", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main", "-c", "protocol.file.allow=always"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "HOME="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

var originFiles = map[string]string{
	"package.json":             `{"name": "teaser", "private": true}`,
	"package-lock.json":        `{"lockfileVersion": 3, "packages": {"": {"name": "teaser"}}}`,
	"src/story.ts":             "export const HEADLINE = \"Hello\";\n",
	"src/old.ts":               "export const OLD = 1;\n",
	".github/workflows/ci.yml": "name: ci\n",
	"CLAUDE.md":                "Instructions for another project's assistant.\n",
}

// broughtIn is what an import keeps of originFiles (no tool or assistant files).
func broughtIn() map[string]string {
	out := map[string]string{}
	for p, b := range originFiles {
		if !strings.HasPrefix(p, ".github/") && p != "CLAUDE.md" {
			out[p] = b
		}
	}
	return out
}

// originRepo makes a work repository with the original files, and returns it with its commit.
func originRepo(t *testing.T) (string, string) {
	t.Helper()
	work := t.TempDir()
	tgit(t, work, "init", "--quiet")
	for p, b := range originFiles {
		full := filepath.Join(work, filepath.FromSlash(p))
		_ = os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tgit(t, work, "add", "--all")
	tgit(t, work, "commit", "--quiet", "-m", "the original")
	return work, tgit(t, work, "rev-parse", "HEAD")
}

func fakeGH(t *testing.T, a *app.App) string {
	t.Helper()
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "args.txt")
	script := "#!/bin/sh\necho \"$@\" >> " + argsFile + "\ncat > /dev/null\necho 'Creating pull request for metarush into main'\necho 'https://github.com/acme/teaser/pull/7'\n"
	p := filepath.Join(dir, "gh")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if !a.Tools.Use(tools.GH, p) {
		t.Fatal("fake gh")
	}
	return argsFile
}

func approve(t *testing.T, a *app.App, s *models.RemotionSource, v *models.RemotionVersion) {
	t.Helper()
	fresh, _ := getSource(context.Background(), a, s.ID)
	if _, err := decide(context.Background(), a, fresh, v, verbApprove, a.Actor, ""); err != nil {
		t.Fatal(err)
	}
}

func pushNow(t *testing.T, a *app.App, in RemotionPushIn) (*RemotionPushPlanOut, *models.RemotionPush) {
	t.Helper()
	ctx := context.Background()
	p, _, err := planPush(ctx, a, &in)
	if err != nil {
		t.Fatal(err)
	}
	if !p.Ready {
		t.Fatalf("not ready: %q", p.Problems)
	}
	out, err := startPush(ctx, a, &RemotionPushStartIn{RemotionPushIn: in, Plan: p.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunPushJob(ctx, a, &models.Job{ID: out.JobID, Payload: map[string]any{"push": out.ID}, CreatedBy: a.Actor}, nil); err != nil {
		rec, _ := getPush(ctx, a, out.ID)
		t.Fatalf("push: %v (%+v)", err, rec)
	}
	rec, _ := getPush(ctx, a, out.ID)
	return p, rec
}

func needsGit(t *testing.T, a *app.App) {
	t.Helper()
	if !a.Tools.Has(tools.Git) {
		t.Skip("git isn't installed")
	}
}

func TestPushBackToAGitHubRepository(t *testing.T) {
	a := testapp.New(t)
	needsGit(t, a)
	ctx := context.Background()
	work, commit := originRepo(t)
	base := t.TempDir()
	bare := filepath.Join(base, "acme", "teaser.git")
	_ = os.MkdirAll(filepath.Dir(bare), 0o755)
	tgit(t, base, "clone", "--quiet", "--bare", work, bare)
	t.Setenv("METARUSH_GIT_BASE", base)
	args := fakeGH(t, a)

	s := newSource(t, a, "teaser")
	s.Origin = models.RemotionOrigin{Kind: models.RemotionFromGitHub, URL: "https://github.com/acme/teaser", Repo: "acme/teaser", Commit: commit, Branch: "main"}
	if err := a.DB.Update(ctx, store.CRemotionSources, a.WorkspaceID(), s.ID, map[string]any{"$set": map[string]any{"origin": s.Origin}}); err != nil {
		t.Fatal(err)
	}
	v1 := versionOf(t, a, s, broughtIn(), "", models.RemotionKindImport)
	edit := broughtIn()
	edit["src/story.ts"] = "export const HEADLINE = \"Hello from MetaRush\";\n"
	edit["src/new.ts"] = "export const NEW = 2;\n"
	delete(edit, "src/old.ts")
	v2 := versionOf(t, a, s, edit, v1.Hash, models.RemotionKindEdit)

	// Not approved yet: nothing goes back.
	if p, _, err := planPush(ctx, a, &RemotionPushIn{Source: "teaser@2"}); err != nil || p.Ready || !strings.Contains(strings.Join(p.Problems, " "), "isn't approved") {
		t.Fatalf("unapproved: %v %+v", err, p)
	}
	approve(t, a, s, v2)
	p, rec := pushNow(t, a, RemotionPushIn{Source: "teaser"})
	var cs []string
	for _, c := range p.Changes {
		cs = append(cs, c.Change+" "+c.Path)
	}
	if strings.Join(cs, "|") != "added src/new.ts|removed src/old.ts|changed src/story.ts" || p.Branch != "metarush/teaser-v2" || !p.PR || p.Target.Kind != models.RemotionPushGitHub ||
		p.Target.Default != "main" || p.Target.Where != "github.com/acme/teaser" || len(p.WordChanges) != 1 || p.WordChanges[0].After != "Hello from MetaRush" {
		t.Fatalf("plan: %+v", p)
	}
	if rec.State != models.RemotionRunDone || !strings.HasPrefix(rec.PRURL, "https://github.com/acme/teaser/pull/") || len(rec.Commit) != 40 {
		t.Fatalf("record: %+v", rec)
	}
	// The branch: the original's files with MetaRush's changes; the left-out files untouched; main as it was.
	files := tgit(t, bare, "ls-tree", "-r", "--name-only", "refs/heads/metarush/teaser-v2")
	for _, want := range []string{".github/workflows/ci.yml", "CLAUDE.md", "src/new.ts", "src/story.ts"} {
		if !strings.Contains(files, want) {
			t.Errorf("the branch lacks %s:\n%s", want, files)
		}
	}
	if strings.Contains(files, "src/old.ts") {
		t.Error("the removed file is still there")
	}
	if got := tgit(t, bare, "show", "refs/heads/metarush/teaser-v2:src/story.ts"); !strings.Contains(got, "Hello from MetaRush") {
		t.Errorf("story: %s", got)
	}
	if parent := tgit(t, bare, "rev-parse", "refs/heads/metarush/teaser-v2^"); parent != commit {
		t.Errorf("parent %s, want the original %s", parent, commit)
	}
	if main := tgit(t, bare, "rev-parse", "refs/heads/main"); main != commit {
		t.Error("main moved")
	}
	if msg := tgit(t, bare, "log", "-1", "--format=%B", "refs/heads/metarush/teaser-v2"); !strings.Contains(msg, "Version 2 of teaser, from MetaRush") {
		t.Errorf("message: %s", msg)
	}
	b, _ := os.ReadFile(args)
	if !strings.Contains(string(b), "pr create --repo acme/teaser --head metarush/teaser-v2 --base main") {
		t.Errorf("gh: %s", b)
	}

	// A later version goes on top of the branch MetaRush made: a fast-forward.
	edit3 := broughtIn()
	edit3["src/story.ts"] = "export const HEADLINE = \"Hello again\";\n"
	v3 := versionOf(t, a, s, edit3, v2.Hash, models.RemotionKindEdit)
	approve(t, a, s, v3)
	p3, rec3 := pushNow(t, a, RemotionPushIn{Source: "teaser@3", Branch: "metarush/teaser-v2", NoPR: true})
	if !p3.Extends || p3.Parent != rec.Commit || p3.PR {
		t.Fatalf("extends: %+v", p3)
	}
	if parent := tgit(t, bare, "rev-parse", "refs/heads/metarush/teaser-v2^"); parent != rec.Commit || tgit(t, bare, "rev-parse", "refs/heads/metarush/teaser-v2") != rec3.Commit {
		t.Error("not a fast-forward")
	}
	// Someone else changes the branch: MetaRush never overwrites it.
	other := t.TempDir()
	tgit(t, other, "clone", "--quiet", "--branch", "metarush/teaser-v2", bare, "c")
	_ = os.WriteFile(filepath.Join(other, "c", "README.md"), []byte("theirs\n"), 0o644)
	tgit(t, filepath.Join(other, "c"), "add", "README.md")
	tgit(t, filepath.Join(other, "c"), "commit", "--quiet", "-m", "someone else")
	tgit(t, filepath.Join(other, "c"), "push", "--quiet", "origin", "metarush/teaser-v2")
	edit4 := broughtIn()
	edit4["src/story.ts"] = "export const HEADLINE = \"Hello a fourth time\";\n"
	v4 := versionOf(t, a, s, edit4, v3.Hash, models.RemotionKindEdit)
	approve(t, a, s, v4)
	for _, c := range []struct{ branch, want string }{{"metarush/teaser-v2", "MetaRush never overwrites it"}, {"main", "never sends changes to main"}, {"master", "never sends changes to master"}} {
		q, _, err := planPush(ctx, a, &RemotionPushIn{Source: "teaser@4", Branch: c.branch})
		if err != nil || q.Ready || !strings.Contains(strings.Join(q.Problems, " "), c.want) {
			t.Errorf("branch %s: %v %q", c.branch, err, q.Problems)
		}
	}
	tgit(t, bare, "branch", "theirs/feature", "main")
	if q, _, _ := planPush(ctx, a, &RemotionPushIn{Source: "teaser@4", Branch: "theirs/feature"}); q.Ready || !strings.Contains(strings.Join(q.Problems, " "), "didn't make") {
		t.Errorf("a branch MetaRush didn't make: %q", q.Problems)
	}
	// What was brought in itself: nothing to send.
	approve(t, a, s, v1)
	if q, _, _ := planPush(ctx, a, &RemotionPushIn{Source: "teaser@1"}); q.Ready || !strings.Contains(strings.Join(q.Problems, " "), "nothing to send back") {
		t.Errorf("nothing to send: %q", q.Problems)
	}
	if lst, err := pushesOf(ctx, a, s.ID, 10); err != nil || len(lst) != 2 {
		t.Errorf("records: %d %v", len(lst), err)
	}
}

func TestPushBackFromAFolder(t *testing.T) {
	a := testapp.New(t)
	needsGit(t, a)
	ctx := context.Background()
	for _, withRemote := range []bool{true, false} {
		work, commit := originRepo(t)
		bare := filepath.Join(t.TempDir(), "teaser.git")
		if withRemote {
			tgit(t, filepath.Dir(bare), "clone", "--quiet", "--bare", work, bare)
			tgit(t, work, "remote", "add", "origin", "file://"+bare)
		}
		name := map[bool]string{true: "withremote", false: "ownrepo"}[withRemote]
		s := newSource(t, a, name)
		s.Origin = models.RemotionOrigin{Kind: models.RemotionFromFolder, Folder: filepath.Base(work), Path: work, Machine: a.Cfg.Installation.Name, Commit: commit, Branch: "main"}
		if err := a.DB.Update(ctx, store.CRemotionSources, a.WorkspaceID(), s.ID, map[string]any{"$set": map[string]any{"origin": s.Origin}}); err != nil {
			t.Fatal(err)
		}
		first := broughtIn()
		first["src/name.ts"] = "export const NAME = \"" + name + "\";\n" // each source its own versions
		v1 := versionOf(t, a, s, first, "", models.RemotionKindImport)
		edit := broughtIn()
		edit["src/name.ts"] = first["src/name.ts"]
		edit["src/story.ts"] = "export const HEADLINE = \"From a folder\";\n"
		v2 := versionOf(t, a, s, edit, v1.Hash, models.RemotionKindEdit)
		approve(t, a, s, v2)
		p, rec := pushNow(t, a, RemotionPushIn{Source: name})
		repo := bare
		kind := models.RemotionPushGit
		if !withRemote {
			repo, kind = filepath.Join(work, ".git"), models.RemotionPushFolder
		}
		if p.Target.Kind != kind || p.PR || rec.State != models.RemotionRunDone {
			t.Fatalf("%s: %+v %+v", name, p, rec)
		}
		if got := tgit(t, repo, "--git-dir="+repo, "show", "refs/heads/metarush/"+name+"-v2:src/story.ts"); !strings.Contains(got, "From a folder") {
			t.Errorf("%s: %s", name, got)
		}
		// The person's folder: its files, its branch and its index as they were.
		if st := tgit(t, work, "status", "--porcelain"); st != "" {
			t.Errorf("%s: the folder changed: %s", name, st)
		}
		if head := tgit(t, work, "rev-parse", "--abbrev-ref", "HEAD"); head != "main" || tgit(t, work, "rev-parse", "HEAD") != commit {
			t.Errorf("%s: the folder's branch moved", name)
		}
	}
}

func TestPushIsPeopleOnly(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	s := newSource(t, a, "pfilm")
	versionOf(t, a, s, broughtIn(), "", models.RemotionKindImport)
	in, _ := json.Marshal(RemotionPushStartIn{RemotionPushIn: RemotionPushIn{Source: s.Name}, Plan: strings.Repeat("0", 64)})
	if _, err := ops.Call(ctx, a, "remotion.push.start", in, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Errorf("without a person: %v", err)
	}
	ca := *a
	ca.Actor.Via = models.ViaAssistant
	if _, err := ops.Call(ctx, &ca, "remotion.push.start", in, ops.Caller{Human: true}); apperr.From(err).Code != apperr.Unauthorized {
		t.Errorf("from Claude: %v", err)
	}
	if checkBranch("metarush/teaser-v4", "trunk") != "" || checkBranch("trunk", "trunk") == "" || checkBranch("a..b", "") == "" || checkBranch("-x", "") == "" || checkBranch("x.lock", "") == "" {
		t.Error("branch names")
	}
}
