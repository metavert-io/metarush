package renderer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/testutil/fakenode"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/version"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// The binary carries the whole renderer package: every tracked source of
// video/ and of the schema types, the workspace manifests and the lockfile, and
// nothing installed (no node_modules, no caches).
func TestEmbeddedPackageIsComplete(t *testing.T) {
	pkg, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	files := pkg.Files()
	for _, f := range append(required, "web/package.json", "schema/ts/index.ts", "video/src/index.ts", "video/tsconfig.json") {
		if !slices.Contains(files, f) {
			t.Errorf("the package lacks %s", f)
		}
	}
	for _, f := range files {
		if strings.Contains(f, "node_modules/") || strings.Contains(f, ".bundle-cache") || strings.Contains(f, ".selftest") {
			t.Errorf("installed or generated file embedded: %s", f)
		}
	}
	// Named by the release (0.2.0), not the exact build (0.2.0-dev): builds past a release share it (D130).
	rel := version.Current().Release
	if len(pkg.Digest()) != 64 || strings.Contains(pkg.ID(), "dev") || !strings.HasPrefix(pkg.ID(), Sanitize(rel)+"-") || len(pkg.ID()) != len(Sanitize(rel))+13 {
		t.Fatalf("digest %q id %q", pkg.Digest(), pkg.ID())
	}
	var vp struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	b, _ := os.ReadFile(filepath.Join(repoRoot(t), "video", "package.json"))
	_ = json.Unmarshal(b, &vp)
	if pkg.Remotion() == "" || pkg.Remotion() != vp.Dependencies["remotion"] {
		t.Fatalf("lockfile pins remotion %q, video/package.json asks for %q", pkg.Remotion(), vp.Dependencies["remotion"])
	}
	// go:embed skips names starting with "." or "_": none of the tracked
	// renderer sources may be dropped that way (or by a missing pattern).
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	cmd := exec.Command(git, "ls-files", "video/src", "video/render", "video/scripts", "schema/ts", "video/package.json", "video/tsconfig.json", "package.json", "package-lock.json")
	cmd.Dir = repoRoot(t)
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("not a git checkout: %v", err)
	}
	for _, f := range strings.Fields(string(out)) {
		if !slices.Contains(files, f) {
			t.Errorf("tracked renderer file %s is not embedded (rename it, or extend the go:embed patterns in renderer_package.go)", f)
		}
	}
}

func TestSanitize(t *testing.T) {
	for in, want := range map[string]string{"0.1.0": "0.1.0", "0.1.0-rc1": "0.1.0-rc1", "0.1.1-snapshot+abc": "0.1.1-snapshot-abc", "": "unknown", "../x": "x"} {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

// env is a fresh installation home with the fake toolchain.
func env(t *testing.T, mut func(*config.Tools)) (*Env, *fakenode.Fake) {
	t.Helper()
	pkg, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	fake := fakenode.Write(t, pkg.Remotion())
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	tc := config.Tools{Node: fake.Node, NPM: fake.NPM}
	if mut != nil {
		mut(&tc)
	}
	// A person chose the renderer (setup's suggestion): it sets itself up
	// on first start (D325). Tests of other choices change it.
	if err := choice.Set(home, map[string]bool{Component: true}, "setup"); err != nil {
		t.Fatal(err)
	}
	return &Env{Home: home, Cfg: tc, Tools: tools.New(tc), Pkg: pkg}, fake
}

func install(t *testing.T, e *Env) Status {
	t.Helper()
	st, err := e.Install(context.Background(), InstallOptions{})
	if err != nil {
		t.Fatalf("install: %v (%+v)", err, apperr.From(err))
	}
	return st
}

// A setup lands in <home>/renderer/<version>-<digest>, byte for byte this
// binary's package, with the shared browser linked in; a second setup does
// nothing unless forced.
func TestInstallSetsUpThisVersionsRenderer(t *testing.T) {
	e, fake := env(t, nil)
	if st := e.Check(false); st.State != StateMissing || st.Ready || !strings.Contains(st.Fix, "set it up now") || st.Command() != "metarush renderer install" {
		t.Fatalf("before setup: %+v", st)
	}
	var stages []string
	st, err := e.Install(context.Background(), InstallOptions{Progress: func(s Step) {
		if len(stages) == 0 || stages[len(stages)-1] != s.Stage {
			stages = append(stages, s.Stage)
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(e.Home, "renderer", e.Pkg.ID())
	if !st.Ready || st.State != StateReady || st.Dir != want || st.VideoDir != filepath.Join(want, "video") || st.Source != SourceBundled || !st.Browser {
		t.Fatalf("after setup: %+v", st)
	}
	if strings.Join(stages, ",") != "unpack,packages,browser,check,done" {
		t.Fatalf("stages: %v", stages)
	}
	for _, f := range e.Pkg.Files() {
		got, err := os.ReadFile(filepath.Join(want, filepath.FromSlash(f)))
		wantB, _ := fs.ReadFile(e.Pkg.fsys, f)
		if err != nil || !bytes.Equal(got, wantB) {
			t.Fatalf("%s differs from the package: %v", f, err)
		}
	}
	link, err := os.Readlink(filepath.Join(want, "video", "node_modules", ".remotion"))
	if err != nil || filepath.IsAbs(link) || !strings.HasSuffix(link, filepath.Join("browsers", "remotion-"+e.Pkg.Remotion())) {
		t.Fatalf("browser link %q %v", link, err)
	}
	// npm ci ran in the new renderer's own (temporary) folder, the setup check
	// in its video package.
	if runs := fake.Runs(t); len(runs) != 2 || !strings.HasPrefix(runs[0], "npm "+filepath.Join(e.Home, "renderer", ".tmp-"+e.Pkg.ID())) ||
		runs[1] != "browser "+strings.TrimPrefix(runs[0], "npm ")+"/video" {
		t.Fatalf("runs: %v", runs)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(e.Home, "renderer", ".tmp-*")); len(leftovers) > 0 {
		t.Fatalf("temporary folders left: %v", leftovers)
	}
	if _, err := e.Install(context.Background(), InstallOptions{}); err != nil || fake.Count(t, "npm") != 1 {
		t.Fatalf("a ready renderer was set up again: %v, %d npm runs", err, fake.Count(t, "npm"))
	}
	if _, err := e.Install(context.Background(), InstallOptions{Force: true}); err != nil || fake.Count(t, "npm") != 2 {
		t.Fatalf("--force did not set it up again: %v, %d npm runs", err, fake.Count(t, "npm"))
	}
	if !e.Check(true).Ready || len(e.Check(false).Versions) != 1 {
		t.Fatalf("after --force: %+v", e.Check(true))
	}
}

// An installation that rendered from a source checkout moves to this
// version's renderer, says so once, and later edits of the checkout do not
// reach the installed renderer.
func TestCheckoutEditsDoNotChangeTheInstalledRenderer(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "video")
	if err := os.MkdirAll(filepath.Join(checkout, "render"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "render", "render.mjs"), []byte("// the checkout\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, _ := env(t, func(tc *config.Tools) { tc.VideoDir = checkout }) // an older installation's setting, no developer flag
	st := install(t, e)
	if st.Source != SourceBundled || strings.HasPrefix(st.VideoDir, checkout) || !strings.Contains(st.Notice, "renderer dev") {
		t.Fatalf("legacy videoDir: %+v", st)
	}
	if n := e.NoticeOnce(); n == "" || e.NoticeOnce() != "" {
		t.Fatal("the move notice should be given exactly once")
	}
	installed, _ := os.ReadFile(filepath.Join(st.VideoDir, "render", "render.mjs"))
	if err := os.WriteFile(filepath.Join(checkout, "render", "render.mjs"), []byte("// edited after the install\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after := e.Check(true)
	now, _ := os.ReadFile(filepath.Join(after.VideoDir, "render", "render.mjs"))
	embedded, _ := fs.ReadFile(e.Pkg.fsys, "video/render/render.mjs")
	if !after.Ready || after.VideoDir != st.VideoDir || !bytes.Equal(now, installed) || !bytes.Equal(now, embedded) {
		t.Fatalf("the checkout edit reached the installed renderer: %+v", after)
	}
}

// Deleting the renderer gives a clear prompt to set it up again, everywhere
// it is asked about; it is never reinstalled behind the person's back.
func TestRemovedRendererAsksForAReinstall(t *testing.T) {
	e, fake := env(t, nil)
	st := install(t, e)
	if err := os.RemoveAll(st.Dir); err != nil {
		t.Fatal(err)
	}
	got := e.Check(true)
	if got.Ready || got.State != StateRemoved || !strings.Contains(got.Problem, "removed") || got.Fix != "Set the renderer up again." || got.Command() != "metarush renderer install" {
		t.Fatalf("removed: %+v", got)
	}
	_, _, err := e.Ready(true)
	var nr *NotReady
	if !errors.As(err, &nr) || !strings.Contains(err.Error(), "Set the renderer up again") {
		t.Fatalf("Ready: %v", err)
	}
	if ae := apperr.From(err); ae.Code != apperr.NotConfigured || !strings.Contains(ae.Hint, "renderer up again") || ae.Command() != "metarush renderer install" {
		t.Fatalf("app error: %+v", ae)
	}
	if e.Usable() {
		t.Fatal("a removed renderer is not usable")
	}
	var said []string
	e.FirstUse(context.Background(), func(f string, x ...any) { said = append(said, f) })
	if fake.Count(t, "npm") != 1 || len(said) != 1 {
		t.Fatalf("first use must not reinstall a removed renderer: %d npm runs, said %v", fake.Count(t, "npm"), said)
	}
	if st2 := install(t, e); !st2.Ready {
		t.Fatalf("reinstall: %+v", st2)
	}
}

// A renderer whose files were changed, or whose packages went missing, is
// damaged: renders refuse it with the same prompt; setup repairs it.
func TestDamagedRendererIsRefusedAndRepaired(t *testing.T) {
	e, _ := env(t, nil)
	st := install(t, e)
	p := filepath.Join(st.VideoDir, "render", "render.mjs")
	if err := os.WriteFile(p, []byte("// tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := e.Check(true); got.State != StateDamaged || !strings.Contains(got.Problem, "render.mjs was changed") || got.Command() != "metarush renderer install" {
		t.Fatalf("edited: %+v", got)
	}
	repaired := install(t, e)
	if !repaired.Ready {
		t.Fatalf("repair: %+v", repaired)
	}
	if err := os.RemoveAll(filepath.Join(repaired.Dir, "node_modules", "remotion")); err != nil {
		t.Fatal(err)
	}
	if got := e.Check(false); got.State != StateDamaged || !strings.Contains(got.Problem, "Remotion is not installed") {
		t.Fatalf("packages: %+v", got)
	}
}

// The first start after an upgrade sets the renderer up; a failed setup is
// recorded (and cleaned up) and says what to do.
func TestFirstUseAndFailedSetup(t *testing.T) {
	e, fake := env(t, nil)
	fake.FailNPM(t, true)
	_, err := e.Install(context.Background(), InstallOptions{})
	if ae := apperr.From(err); err == nil || ae.Code != apperr.ToolFailed || !strings.Contains(ae.Message, "Setting up the renderer failed") || !strings.Contains(ae.Hint, "set up the renderer again") || ae.Command() != "metarush renderer install" {
		t.Fatalf("failed setup: %v", err)
	}
	if st := e.Check(false); st.State != StateMissing || !strings.Contains(st.LastError, "npm ci") || !strings.Contains(st.Problem, "The last try failed") {
		t.Fatalf("after a failed setup: %+v", st)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(e.Home, "renderer", ".tmp-*")); len(leftovers) > 0 {
		t.Fatalf("a failed setup left %v", leftovers)
	}
	// Offline at the first start: FirstUse says so and waits to try again.
	ctx, cancel := context.WithCancel(context.Background())
	var said []string
	e.FirstUse(ctx, func(f string, x ...any) {
		line := fmt.Sprintf(f, x...)
		said = append(said, line)
		if strings.Contains(line, "tries again") {
			cancel()
		}
	})
	cancel()
	if last := said[len(said)-1]; len(said) < 2 || strings.Count(last, "Setting up the renderer failed") != 1 || !strings.Contains(last, "npm ci") {
		t.Fatalf("first use said %q", said)
	}
	fake.FailNPM(t, false)
	said = nil
	e.FirstUse(context.Background(), func(f string, x ...any) { said = append(said, f) })
	if st := e.Check(true); !st.Ready || st.LastError != "" {
		t.Fatalf("first use did not set it up: %+v (said %v)", st, said)
	}
}

// Prune keeps this version's renderer and the newest others, never one a
// render is using, and removes browsers nothing uses any more.
func TestPruneKeepsCurrentNewestAndInUse(t *testing.T) {
	e, _ := env(t, nil)
	install(t, e)
	root := Root(e.Home)
	old := func(id string, age time.Duration, remotion string) {
		dir := filepath.Join(root, id)
		if err := os.MkdirAll(filepath.Join(dir, "video"), 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(Manifest{ID: id, Version: id, Remotion: remotion, InstalledAt: time.Now().Add(-age)})
		if err := os.WriteFile(filepath.Join(dir, manifestName), b, 0o644); err != nil {
			t.Fatal(err)
		}
		_ = os.MkdirAll(filepath.Join(root, browsersDir, "remotion-"+remotion), 0o755)
	}
	old("0.0.1-aaaaaaaaaaaa", 72*time.Hour, "4.0.1")
	old("0.0.2-bbbbbbbbbbbb", 48*time.Hour, "4.0.2")
	old("0.0.3-cccccccccccc", 24*time.Hour, e.Pkg.Remotion())
	// A render still uses the oldest one.
	use, err := Use(Status{Source: SourceBundled, Dir: filepath.Join(root, "0.0.1-aaaaaaaaaaaa")})
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Prune(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	ids := func(xs []Installed) string {
		var s []string
		for _, x := range xs {
			s = append(s, x.ID)
		}
		return strings.Join(s, ",")
	}
	if ids(res.Kept) != e.Pkg.ID()+",0.0.3-cccccccccccc" || ids(res.Removed) != "0.0.2-bbbbbbbbbbbb" || ids(res.InUse) != "0.0.1-aaaaaaaaaaaa" {
		t.Fatalf("kept %s removed %s in use %s", ids(res.Kept), ids(res.Removed), ids(res.InUse))
	}
	if _, err := os.Stat(filepath.Join(root, browsersDir, "remotion-4.0.2")); !os.IsNotExist(err) {
		t.Fatal("the browser only the removed renderer used is still there")
	}
	if _, err := os.Stat(filepath.Join(root, browsersDir, "remotion-4.0.1")); err != nil {
		t.Fatal("the browser of a renderer in use was removed")
	}
	use()
	res, err = e.Prune(context.Background(), 1)
	if err != nil || ids(res.Removed) != "0.0.3-cccccccccccc,0.0.1-aaaaaaaaaaaa" || !e.Check(true).Ready {
		t.Fatalf("second prune: %+v %v", res, err)
	}
}

// While another process sets the renderer up, renders wait for it.
func TestInstallingStateWhileAnotherProcessSetsUp(t *testing.T) {
	e, _ := env(t, nil)
	root := Root(e.Home)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(root, lockName), os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(Progress{PID: 123, ID: e.Pkg.ID(), StartedAt: time.Now(), Stage: "packages"})
	_ = os.WriteFile(filepath.Join(root, installingName), b, 0o644)
	st := e.Check(false)
	if st.State != StateInstalling || st.Installing == nil || st.Installing.Stage != "packages" || !e.Usable() || st.Ready {
		t.Fatalf("installing: %+v", st)
	}
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	if st := e.Check(false); st.State != StateMissing {
		t.Fatalf("after the other setup ended: %+v", st)
	}
}

// The developer setting renders from a live checkout, and says why not when
// the checkout cannot render.
func TestDeveloperCheckout(t *testing.T) {
	checkout := filepath.Join(t.TempDir(), "video")
	_ = os.MkdirAll(filepath.Join(checkout, "render"), 0o755)
	_ = os.WriteFile(filepath.Join(checkout, "render", "render.mjs"), []byte("//\n"), 0o644)
	e, _ := env(t, func(tc *config.Tools) { tc.VideoDir, tc.DevRenderer = checkout, true })
	if st := e.Check(false); st.Source != SourceCheckout || st.Ready || !strings.Contains(st.Fix, "npm ci") {
		t.Fatalf("no packages: %+v", st)
	}
	_ = os.MkdirAll(filepath.Join(checkout, "node_modules", "remotion"), 0o755)
	_ = os.WriteFile(filepath.Join(checkout, "node_modules", "remotion", "package.json"), []byte(`{"version":"9.9.9"}`), 0o644)
	st := e.Check(false)
	if !st.Ready || st.VideoDir != checkout || st.Remotion != "9.9.9" || !strings.Contains(st.Notice, "developer checkout") {
		t.Fatalf("checkout: %+v", st)
	}
	release, err := Use(st)
	if err != nil {
		t.Fatal(err)
	}
	release()
	e.Cfg.VideoDir = ""
	if st := e.Check(false); st.Ready || st.State != StateUnavailable || !strings.Contains(st.Fix, "renderer dev") {
		t.Fatalf("no checkout: %+v", st)
	}
}

// Without Node.js, the renderer cannot be set up, and both say so plainly.
func TestNoNode(t *testing.T) {
	e, _ := env(t, func(tc *config.Tools) { tc.Node = filepath.Join(t.TempDir(), "missing-node") })
	e.Tools = tools.New(config.Tools{Node: e.Cfg.Node})
	if e.Tools.Has(tools.Node) {
		t.Skip("a node on this machine's usual paths is found anyway")
	}
	if st := e.Check(false); st.State != StateUnavailable || !strings.Contains(st.Problem, "Node.js") {
		t.Fatalf("check: %+v", st)
	}
	if _, err := e.Install(context.Background(), InstallOptions{}); apperr.From(err).Code != apperr.ToolMissing {
		t.Fatalf("install: %v", err)
	}
}
