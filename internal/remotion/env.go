package remotion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	rnd "github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/tools"
)

// Package environments (D133): the packages a source's lockfile pins,
// installed once per machine and lockfile into <home>/remotion/envs/<key>
// with `npm ci --ignore-scripts` from the public registry (nothing a package
// says to run at install time runs), with Remotion's headless browser next to
// them. Nothing here is shared between machines; an environment can be
// deleted at any time and is installed again when needed.

const envManifest = ".metarush-env.json"

// envInfo is written into an environment when it is ready.
type envInfo struct {
	Key         string    `json:"key"`
	Lock        string    `json:"lock"`
	Remotion    string    `json:"remotion"`
	InstalledAt time.Time `json:"installedAt"`
}

// envsRoot is where the environments of this installation live.
func envsRoot(a *app.App) string { return filepath.Join(a.Cfg.Home(), "remotion", "envs") }

// browsersRoot holds Remotion's headless browsers, one per Remotion version.
func browsersRoot(a *app.App) string { return filepath.Join(a.Cfg.Home(), "remotion", "browsers") }

var envMu sync.Mutex // one setup per process at a time (the file lock covers processes)

// rendererDir is the folder of this version's own renderer (its scripts run
// node from there; render/source/*.mjs are MetaRush's code).
func rendererDir(a *app.App) (string, func(), error) {
	dir, st, err := rnd.New(a.Cfg, a.Tools).Ready(false)
	if err != nil {
		return "", nil, err
	}
	release, err := rnd.Use(st)
	if err != nil {
		return "", nil, err
	}
	return dir, release, nil
}

// ensureEnv returns a ready environment for a version's packages, installing
// it when this machine doesn't have it yet.
func ensureEnv(ctx context.Context, a *app.App, pi *pkgInfo, videoDir string, say func(string)) (string, error) {
	if say == nil {
		say = func(string) {}
	}
	dir := filepath.Join(envsRoot(a), pi.envKey)
	if envReady(dir, pi) {
		return dir, nil
	}
	if !a.Tools.Has(tools.Node) {
		return "", apperr.New(apperr.ToolMissing, "Reading and rendering a Remotion project needs Node.js, which isn't installed on this computer.").
			WithHint("Install Node.js 22 or newer, then try again.").WithCommand("brew install node")
	}
	envMu.Lock()
	defer envMu.Unlock()
	lk, err := lockFile(filepath.Join(envsRoot(a), ".lock-"+pi.envKey))
	if err != nil {
		return "", err
	}
	defer lk.release()
	if envReady(dir, pi) {
		return dir, nil
	}
	r := rnd.New(a.Cfg, a.Tools)
	npm := r.NPM()
	if npm == "" {
		return "", apperr.New(apperr.ToolMissing, "MetaRush couldn't find npm, which comes with Node.js.").WithHint("Reinstall Node.js (it brings npm), then try again.")
	}
	tmp, err := os.MkdirTemp(envsRoot(a), ".tmp-"+pi.envKey+"-")
	if err != nil {
		return "", err
	}
	moved := false
	defer func() {
		if !moved {
			_ = os.RemoveAll(tmp)
		}
	}()
	if err := os.WriteFile(filepath.Join(tmp, "package.json"), pi.envPkg, 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(tmp, "package-lock.json"), pi.lockBytes, 0o644); err != nil {
		return "", err
	}
	say(fmt.Sprintf("installing the project's packages (Remotion %s, once on this computer)", pi.remotion))
	cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	var tail strings.Builder
	env := append(r.ChildEnv(), "npm_config_ignore_scripts=true", "npm_config_registry="+Registry, "npm_config_audit=false", "npm_config_fund=false", "npm_config_update_notifier=false")
	if os.Getenv("METARUSH_REMOTION_NPM_OFFLINE") == "1" {
		env = append(env, "npm_config_prefer_offline=true")
	}
	_, err = a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: tmp, Env: env,
		Args: []string{npm, "ci", "--ignore-scripts", "--include=dev", "--no-audit", "--no-fund", "--no-update-notifier", "--loglevel=error", "--registry=" + Registry},
		OnLine: func(l string) {
			if tail.Len() < 4000 {
				tail.WriteString(l + "\n")
			}
		}})
	if err != nil {
		var ef *tools.ErrFailed
		msg := err.Error()
		if errors.As(err, &ef) {
			msg = ef.Stderr
		}
		return "", apperr.Wrap(apperr.ToolFailed, err, "Installing the project's packages didn't work, so MetaRush can't read or render it on this computer yet.").
			WithHint("Check the internet connection and try again.").WithDetails(map[string]any{"output": lastLines(msg+"\n"+tail.String(), 20)})
	}
	if v := packageVersion(filepath.Join(tmp, "node_modules", "remotion", "package.json")); v != pi.remotion {
		return "", fmt.Errorf("installing the project's packages gave Remotion %q instead of %s", v, pi.remotion)
	}
	if err := linkBrowser(a, tmp, pi.remotion); err != nil {
		return "", err
	}
	say("getting Remotion's browser for this version (once per computer)")
	if err := runSetup(ctx, a, videoDir, tmp); err != nil {
		return "", err
	}
	b, _ := json.MarshalIndent(envInfo{Key: pi.envKey, Lock: pi.lockHash, Remotion: pi.remotion, InstalledAt: time.Now().UTC()}, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, envManifest), b, 0o644); err != nil {
		return "", err
	}
	if exists(dir) {
		_ = os.RemoveAll(dir)
	}
	if err := os.Rename(tmp, dir); err != nil {
		return "", err
	}
	moved = true
	return dir, nil
}

// envReady reports an environment that finished setting up for these packages.
func envReady(dir string, pi *pkgInfo) bool {
	b, err := os.ReadFile(filepath.Join(dir, envManifest))
	if err != nil {
		return false
	}
	var m envInfo
	if json.Unmarshal(b, &m) != nil || m.Key != pi.envKey || m.Remotion != pi.remotion {
		return false
	}
	if packageVersion(filepath.Join(dir, "node_modules", "@remotion", "renderer", "package.json")) != pi.remotion {
		return false
	}
	entries, err := os.ReadDir(filepath.Join(dir, "node_modules", ".remotion", "chrome-headless-shell"))
	return err == nil && len(entries) > 0
}

// linkBrowser points the environment's browser folder at the one shared by
// every environment of the same Remotion version; a new one starts as a
// clone of the renderer's own browser (the same Chrome when the versions
// agree, so nothing is downloaded; Remotion replaces it when they don't).
func linkBrowser(a *app.App, env, remotion string) error {
	shared := filepath.Join(browsersRoot(a), "remotion-"+rnd.Sanitize(remotion))
	if !exists(filepath.Join(shared, "chrome-headless-shell")) {
		if err := os.MkdirAll(shared, 0o755); err != nil {
			return err
		}
		if pkg, err := rnd.Embedded(); err == nil {
			seed := filepath.Join(rnd.BrowserDir(a.Cfg.Home(), pkg.Remotion()), "chrome-headless-shell")
			if exists(seed) {
				_ = cloneTree(seed, filepath.Join(shared, "chrome-headless-shell"))
			}
		}
		if seed := os.Getenv("METARUSH_REMOTION_BROWSER_SEED"); seed != "" && !exists(filepath.Join(shared, "chrome-headless-shell")) {
			_ = cloneTree(filepath.Join(seed, "chrome-headless-shell"), filepath.Join(shared, "chrome-headless-shell"))
		}
	}
	link := filepath.Join(env, "node_modules", ".remotion")
	_ = os.RemoveAll(link)
	rel, err := filepath.Rel(filepath.Dir(link), shared)
	if err != nil {
		rel = shared
	}
	return os.Symlink(rel, link)
}

// runSetup makes sure Remotion's browser is there (downloading it outside the
// jail when it isn't: no project code runs here).
func runSetup(ctx context.Context, a *app.App, videoDir, env string) error {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Minute)
	defer cancel()
	var failMsg string
	_, err := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Background: true, Args: []string{"render/source/run.mjs", "setup", "--env", env},
		OnLine: func(l string) {
			var ev struct {
				Type    string `json:"type"`
				Message string `json:"message"`
			}
			if json.Unmarshal([]byte(l), &ev) == nil && ev.Type == "error" {
				failMsg = ev.Message
			}
		}})
	if err != nil || failMsg != "" {
		if failMsg == "" {
			failMsg = err.Error()
		}
		return apperr.New(apperr.ToolFailed, "Getting Remotion's browser for this project didn't work, so MetaRush can't read or render it on this computer yet.").
			WithHint("Check the internet connection and try again.").WithDetails(map[string]any{"reason": firstLine(failMsg)})
	}
	return nil
}

func packageVersion(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	var pj struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &pj) != nil {
		return ""
	}
	return pj.Version
}

func lastLines(s string, n int) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}
