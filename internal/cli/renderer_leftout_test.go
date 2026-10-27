package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/fakenode"
	"github.com/metavert-io/metarush/internal/tools"
)

// rendererHome is a home whose Node.js is the stand-in (its npm logs runs).
func rendererHome(t *testing.T) (string, *fakenode.Fake) {
	t.Helper()
	pkg, err := renderer.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	fake := fakenode.Write(t, pkg.Remotion())
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Tools.Node, cfg.Tools.NPM = fake.Node, fake.NPM
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	return h, fake
}

func npmRan(fake *fakenode.Fake) bool {
	b, _ := os.ReadFile(filepath.Join(fake.Dir, "runs.log"))
	return strings.Contains(string(b), "npm")
}

// A render asked for in a terminal never sets up a renderer a person left
// out, nor one nobody chose when an AI app asks (D325, D327); it says how to
// add it instead.
func TestRenderDoesntSetUpALeftOutRenderer(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	os.Unsetenv("CLAUDECODE")
	h, fake := rendererHome(t)
	cfg, _ := config.Load(h)
	a := &app.App{Cfg: cfg, Tools: tools.New(cfg.Tools)}
	c := &cobra.Command{}
	c.SetContext(context.Background())
	c.Flags().Bool("json", true, "")
	_ = choice.Set(h, map[string]bool{renderer.Component: false}, "setup")
	err := setUpRendererIfNew(c, a, true)
	if err == nil || !strings.Contains(err.Error(), "Rendering needs the Remotion renderer") || npmRan(fake) {
		t.Fatalf("left out: %v (npm ran: %v)", err, npmRan(fake))
	}
	// Nobody chose, and Claude Code asks: no setup either.
	_ = os.Remove(choice.Path(h))
	t.Setenv("CLAUDECODE", "1")
	if err := setUpRendererIfNew(c, a, true); err == nil || npmRan(fake) {
		t.Fatalf("an AI app set the renderer up: %v", err)
	}
}

// metarush renderer install fits a deploy (scripts/deploy.sh runs it under
// set -e, maybe from Claude Code, maybe over SSH with no terminal): a ready
// renderer answers "already set up"; a renderer nobody chose, or one a
// person left out, is never set up without a person — it says so and exits
// 0; a person at their terminal adds a left-out one, and the choice records
// what really happened.
func TestRendererInstallFitsDeploy(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	os.Unsetenv("CLAUDECODE")
	h, fake := rendererHome(t)
	// Claude Code on a home where nobody chose the renderer: no download, exit 0, a note.
	t.Setenv("CLAUDECODE", "1")
	r := runCLI(t, h, "renderer", "install")
	if r.code != 0 || npmRan(fake) || !strings.Contains(r.stdout+r.stderr, "a person") {
		t.Fatalf("Claude Code, nobody chose: %d %s %s (npm ran: %v)", r.code, r.stdout, r.stderr, npmRan(fake))
	}
	// A person sets it up at their terminal: the choice says so.
	t.Setenv("CLAUDECODE", "")
	os.Unsetenv("CLAUDECODE")
	old := isInteractive
	isInteractive = func() bool { return true }
	r = runCLI(t, h, "renderer", "install")
	isInteractive = old
	if r.code != 0 || !npmRan(fake) {
		t.Fatalf("a person: %d %s %s", r.code, r.stdout, r.stderr)
	}
	if ch, _ := choice.Of(h, renderer.Component); !ch.Wanted || ch.By != "a person at a terminal" {
		t.Fatalf("choice: %+v", ch)
	}
	// Ready, and Claude Code runs deploy's step: already set up, exit 0.
	t.Setenv("CLAUDECODE", "1")
	_ = os.Remove(filepath.Join(fake.Dir, "runs.log"))
	r = runCLI(t, h, "renderer", "install")
	if r.code != 0 || npmRan(fake) || !strings.Contains(r.stdout, "already set up") {
		t.Fatalf("ready, under Claude Code: %d %s %s", r.code, r.stdout, r.stderr)
	}
	// Over SSH (no terminal) to a computer whose renderer was left out: exit 0, a note.
	t.Setenv("CLAUDECODE", "")
	os.Unsetenv("CLAUDECODE")
	h2, fake2 := rendererHome(t)
	_ = choice.Set(h2, map[string]bool{renderer.Component: false}, "setup")
	r = runCLI(t, h2, "renderer", "install")
	if r.code != 0 || npmRan(fake2) || !strings.Contains(r.stdout+r.stderr, "left out") {
		t.Fatalf("left out, no terminal: %d %s %s", r.code, r.stdout, r.stderr)
	}
}

// deploy.sh's renderer step itself, as the script runs it (set -euo
// pipefail, stdin not a terminal), under Claude Code and on a left-out
// renderer: the script goes on to the next step.
func TestDeployRendererStepGoesOn(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	bin := filepath.Join(t.TempDir(), "metarush")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", bin, "github.com/metavert-io/metarush/cmd/metarush")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	step := func(h string, env ...string) (string, error) {
		c := exec.Command("bash", "-c", `set -euo pipefail; MR() { "$BIN" --home "$H" "$@"; }; MR renderer install; echo "next step: migrate"`)
		c.Env = append(append(os.Environ(), "BIN="+bin, "H="+h), env...)
		c.Stdin = nil // not a terminal
		out, err := c.CombinedOutput()
		return string(out), err
	}
	h, fake := rendererHome(t)
	isInteractive = func() bool { return true }
	if r := runCLI(t, h, "renderer", "install"); r.code != 0 {
		t.Fatalf("set it up: %s %s", r.stdout, r.stderr)
	}
	isInteractive = interactive
	if out, err := step(h, "CLAUDECODE=1"); err != nil || !strings.Contains(out, "next step: migrate") {
		t.Fatalf("a ready renderer under Claude Code stopped deploy: %v\n%s", err, out)
	}
	h2, fake2 := rendererHome(t)
	_ = choice.Set(h2, map[string]bool{renderer.Component: false}, "setup")
	if out, err := step(h2); err != nil || !strings.Contains(out, "next step: migrate") || npmRan(fake2) {
		t.Fatalf("a left-out renderer with no terminal stopped deploy: %v\n%s", err, out)
	}
	_ = fake
}

// `metarush renderer install` is itself a request to set the renderer up,
// like `setup --components renderer`: a script with no terminal (the
// acceptance run in a fresh home, deploy over SSH, the Homebrew cask's
// "metarush init && metarush renderer install") sets up a renderer nobody
// chose or one waiting for a person, and records that a command did. An AI
// app never downloads it, and a renderer a person left out is set up by a
// script only when it says --force.
func TestRendererInstallIsARequestToSetItUp(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	os.Unsetenv("CLAUDECODE")
	t.Setenv("METARUSH_AGENT", "")
	isInteractive = func() bool { return false }
	t.Cleanup(func() { isInteractive = interactive })
	by := func(h string) string {
		ch, ok := choice.Of(h, renderer.Component)
		if !ok || !ch.Wanted || ch.Waiting {
			return ""
		}
		return ch.By
	}

	t.Run("a fresh home, no terminal: set up", func(t *testing.T) {
		h, fake := rendererHome(t)
		r := runCLI(t, h, "renderer", "install", "--json")
		if r.code != 0 || !npmRan(fake) {
			t.Fatalf("acceptance's renderer step didn't set it up: %d %s %s", r.code, r.stdout, r.stderr)
		}
		if got := by(h); got != "a command run without a terminal" {
			t.Errorf("the choice says %q", got)
		}
	})
	t.Run("waiting for a person, no terminal: set up", func(t *testing.T) {
		h, fake := rendererHome(t)
		_ = choice.SetWaiting(h, []string{renderer.Component}, "setup with nobody at a terminal")
		if r := runCLI(t, h, "renderer", "install"); r.code != 0 || !npmRan(fake) {
			t.Fatalf("a waiting renderer wasn't set up: %d %s %s", r.code, r.stdout, r.stderr)
		}
		if got := by(h); got != "a command run without a terminal" {
			t.Errorf("the choice says %q", got)
		}
	})
	t.Run("an AI app: nothing downloaded", func(t *testing.T) {
		h, fake := rendererHome(t)
		t.Setenv("CLAUDECODE", "1")
		r := runCLI(t, h, "renderer", "install")
		if r.code != 0 || npmRan(fake) {
			t.Fatalf("Claude Code set the renderer up: %d %s %s", r.code, r.stdout, r.stderr)
		}
		if _, ok := choice.Of(h, renderer.Component); ok {
			t.Error("a choice was recorded")
		}
	})
	t.Run("left out by a person: only with --force", func(t *testing.T) {
		h, fake := rendererHome(t)
		_ = choice.Set(h, map[string]bool{renderer.Component: false}, "Sam")
		r := runCLI(t, h, "renderer", "install")
		if r.code != 0 || npmRan(fake) || !strings.Contains(r.stdout+r.stderr, "left out") {
			t.Fatalf("a script overrode a person's no: %d %s %s", r.code, r.stdout, r.stderr)
		}
		if ch, _ := choice.Of(h, renderer.Component); ch.Wanted || ch.By != "Sam" {
			t.Errorf("the person's choice changed: %+v", ch)
		}
		r = runCLI(t, h, "renderer", "install", "--force")
		if r.code != 0 || !npmRan(fake) {
			t.Fatalf("--force: %d %s %s", r.code, r.stdout, r.stderr)
		}
		if got := by(h); got != "a command run without a terminal, with --force" {
			t.Errorf("the choice says %q", got)
		}
	})
	t.Run("the Homebrew cask's next step", func(t *testing.T) {
		setupEnv(t)
		db := testutil.DB(t)
		h := filepath.Join(t.TempDir(), "home")
		t.Setenv("METARUSH_MONGODB_URI", testutil.URI())
		r := runCLI(t, h, "init", "--yes", "--test-db", "--secrets", "env", "--db-name", db.Name, "--workspace-name", "Cask home",
			"--media", "filesystem", "--port", strconv.Itoa(freeTCPPort(t)))
		if r.code != 0 {
			t.Fatalf("init: %d %s", r.code, r.stderr)
		}
		pkg, _ := renderer.Embedded()
		fake := fakenode.Write(t, pkg.Remotion())
		cfg, err := config.Load(h)
		if err != nil {
			t.Fatal(err)
		}
		cfg.Tools.Node, cfg.Tools.NPM = fake.Node, fake.NPM // Homebrew's node, as the cask asks for
		if err := config.Save(h, cfg); err != nil {
			t.Fatal(err)
		}
		if r := runCLI(t, h, "renderer", "install"); r.code != 0 || !npmRan(fake) {
			t.Fatalf("metarush init && metarush renderer install did nothing: %d %s %s", r.code, r.stdout, r.stderr)
		}
	})
}
