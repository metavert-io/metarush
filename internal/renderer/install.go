package renderer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/version"
)

// InstallOptions configure a setup.
type InstallOptions struct {
	// Force sets the renderer up again even when it is ready.
	Force bool
	// Progress hears every step in plain words (optional).
	Progress func(Step)
	// Keep is how many renderers to keep afterwards, this one included
	// (0 = DefaultKeep; negative = remove none).
	Keep int
}

// Step is one stage of a setup.
type Step struct {
	Stage    string  `json:"stage"` // waiting | unpack | packages | browser | check | done
	Message  string  `json:"message"`
	Progress float64 `json:"progress,omitempty"` // within the stage, when known (0..1)
}

// Timeouts for the two long steps.
var (
	packagesTimeout = 20 * time.Minute
	browserTimeout  = 20 * time.Minute
)

// minNodeMajor is the oldest Node.js the renderer's packages run on.
const minNodeMajor = 18

// Install sets this version's renderer up (or repairs it): unpack the package
// into a temporary folder, `npm ci` exactly the locked packages, link the
// shared headless browser and download it if needed, prove the renderer
// loads, then move the folder into place in one rename. Only one process sets
// renderers up at a time; others wait. A ready renderer is left alone unless
// Force.
func (e *Env) Install(ctx context.Context, o InstallOptions) (Status, error) {
	say := func(stage, msg string, p float64) {
		if o.Progress != nil {
			o.Progress(Step{Stage: stage, Message: msg, Progress: p})
		}
	}
	if e.Pkg == nil {
		st := e.Check(false)
		return st, &NotReady{Status: st}
	}
	if e.Cfg.DevRenderer {
		say("check", "This installation renders from a developer checkout; setting up this version's renderer anyway (it is used after `metarush renderer dev --off`).", 0)
	}
	if e.Tools == nil || !e.Tools.Has(tools.Node) {
		return e.Check(false), apperr.New(apperr.ToolMissing, "Rendering needs Node.js 22 or newer, which isn't on this computer yet.").
			WithHint("Add Node.js in Settings → Components (about 50 MB), then set up the renderer again.").WithCommand("metarush components add node && metarush renderer install")
	}
	if major := nodeMajor(e.Tools.Version(ctx, tools.Node)); major > 0 && major < minNodeMajor {
		return e.Check(false), apperr.New(apperr.ToolMissing, "Rendering needs Node.js 22 or newer, and this computer has an older one (%d).", major).
			WithHint("Install a newer Node.js, then set up the renderer again.").WithCommand("metarush components add node && metarush renderer install")
	}
	npm := e.npmCLI()
	if npm == "" {
		return e.Check(false), apperr.New(apperr.ToolMissing, "MetaRush couldn't find npm, which comes with Node.js.").
			WithHint("Reinstall Node.js (it brings npm), then set up the renderer again.").
			WithDetails(map[string]any{"node": e.Tools.Path(tools.Node), "setting": "tools.npm in config.yaml can point at npm's npm-cli.js"}).
			WithCommand("metarush renderer install")
	}
	root := Root(e.Home)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return e.Check(false), err
	}
	lk, ok, err := tryLock(filepath.Join(root, lockName), syscall.LOCK_EX)
	// A status check holds the lock shared for a moment: retry briefly before
	// saying that another process is setting the renderer up.
	for i := 0; err == nil && !ok && i < 10; i++ {
		time.Sleep(20 * time.Millisecond)
		lk, ok, err = tryLock(filepath.Join(root, lockName), syscall.LOCK_EX)
	}
	if err != nil {
		return e.Check(false), err
	}
	if !ok {
		say("waiting", "Another MetaRush is setting up the renderer; waiting for it to finish…", 0)
		if lk, err = waitLock(ctx, filepath.Join(root, lockName), syscall.LOCK_EX); err != nil {
			return e.Check(false), err
		}
	}
	defer lk.release()
	if st := e.check(true, false); st.Ready && !o.Force {
		return st, nil
	}
	id := e.Pkg.ID()
	started := e.now().UTC()
	progress := func(stage, msg string) {
		b, _ := json.Marshal(Progress{PID: os.Getpid(), ID: id, StartedAt: started, Stage: stage, Message: msg})
		_ = writeAtomic(filepath.Join(root, installingName), b, 0o644)
	}
	defer os.Remove(filepath.Join(root, installingName))
	fail := func(err error, details string) (Status, error) {
		msg := plainError(err)
		rec := e.loadRecord()
		rec.LastError = &lastError{ID: id, At: e.now().UTC(), Message: msg}
		e.saveRecord(rec)
		d := map[string]any{"reason": msg}
		if details != "" {
			d["output"] = details
		}
		ae := apperr.Wrap(apperr.ToolFailed, err, "Setting up the renderer failed, so this machine can't make videos yet.").
			WithHint("Check the internet connection, then set up the renderer again.").WithDetails(d).WithCommand("metarush renderer install")
		return e.check(false, false), ae
	}

	tmp, err := os.MkdirTemp(root, ".tmp-"+id+"-")
	if err != nil {
		return fail(err, "")
	}
	moved := false
	defer func() {
		if !moved {
			_ = os.RemoveAll(tmp)
		}
	}()
	say("unpack", "Unpacking the renderer for this version…", 0)
	progress("unpack", "unpacking")
	if err := e.Pkg.Extract(tmp); err != nil {
		return fail(err, "")
	}

	say("packages", "Downloading what the renderer needs (about 100 MB the first time)…", 0)
	progress("packages", "npm ci")
	if out, err := e.npmCI(ctx, npm, tmp); err != nil {
		return fail(fmt.Errorf("npm ci did not finish: %w", err), out)
	}
	if err := e.linkBrowser(tmp); err != nil {
		return fail(err, "")
	}

	say("browser", "Getting the browser the renderer draws with (about 180 MB, downloaded once)…", 0)
	progress("browser", "browser")
	rv, out, err := e.runSetupScript(ctx, tmp, func(p float64) {
		say("browser", fmt.Sprintf("Downloading the renderer's browser: %.0f%%", p*100), p)
	})
	if err != nil {
		return fail(err, out)
	}
	if rv != e.Pkg.remotion {
		return fail(fmt.Errorf("the renderer loaded Remotion %s, expected %s", rv, e.Pkg.remotion), out)
	}

	say("check", "Checking the renderer…", 0)
	progress("check", "checking")
	if problem := e.verifyPackages(tmp); problem != "" {
		return fail(errors.New(problem), "")
	}
	m := Manifest{ID: id, Version: version.Version, Commit: version.Commit, Digest: e.Pkg.digest, Remotion: e.Pkg.remotion,
		Node: e.Tools.Version(ctx, tools.Node), Files: len(e.Pkg.files), InstalledAt: e.now().UTC()}
	mb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(filepath.Join(tmp, manifestName), mb, 0o644); err != nil {
		return fail(err, "")
	}
	final := e.dir(id)
	if exists(final) {
		// A damaged copy, or --force: move it aside first (a render that still
		// has it open keeps working), then delete it.
		old := filepath.Join(root, fmt.Sprintf(".old-%s-%d", id, time.Now().UnixNano()))
		if err := os.Rename(final, old); err != nil {
			return fail(err, "")
		}
		defer os.RemoveAll(old)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fail(err, "")
	}
	moved = true
	rec := e.loadRecord()
	rec.Installed[id] = m.InstalledAt
	if rec.LastError != nil && rec.LastError.ID == id {
		rec.LastError = nil
	}
	e.saveRecord(rec)
	keep := o.Keep
	if keep == 0 {
		keep = DefaultKeep
	}
	if keep > 0 {
		_, _ = e.prune(keep)
	}
	say("done", "The renderer is ready (Remotion "+e.Pkg.remotion+").", 1)
	return e.check(true, false), nil
}

// FirstUse sets this version's renderer up when it has never been set up in
// this installation (an upgrade, a new machine), retrying every 30 minutes
// while that fails (for example, offline). A renderer that was set up and was
// later removed or damaged is not set up again by itself: doctor, the render's
// error and this log say to run `metarush renderer install`.
func (e *Env) FirstUse(ctx context.Context, logf func(string, ...any)) {
	st := e.Check(false)
	if st.Source != SourceBundled {
		return
	}
	if st.State == StateLeftOut || choice.Declined(e.Home, Component) {
		// A person left the renderer out at setup or removed it (D325).
		if st.State == StateLeftOut {
			logf("The renderer isn't added on this computer, so it doesn't render; add it in Settings → Components to render here.")
		}
		return
	}
	if st.State == StateMissing && !e.SetsItselfUp() {
		// Nobody chose it and none was set up here before: a person adds it (D327).
		logf("The renderer for this version isn't set up here; a person adds it in Settings → Components (it downloads about 400 MB).")
		return
	}
	switch st.State {
	case StateMissing:
	case StateRemoved, StateDamaged:
		logf("%s %s", st.Problem, st.Fix)
		return
	default:
		return
	}
	logf("Setting up the renderer for this version of MetaRush (one time; it needs the internet)…")
	for {
		last := ""
		st, err := e.Install(ctx, InstallOptions{Progress: func(s Step) {
			if s.Stage != last && s.Stage != "done" {
				last = s.Stage
				logf("renderer: %s", s.Message)
			}
		}})
		if err == nil {
			logf("The renderer is ready (Remotion %s).", st.Remotion)
			return
		}
		// The activity log line keeps what went wrong (a person reads the error's own words).
		why := strings.TrimSuffix(plainError(err), ".")
		if d, ok := apperr.From(err).Details.(map[string]any); ok {
			if r, _ := d["reason"].(string); r != "" {
				why += " (" + r + ")"
			}
		}
		logf("%s. MetaRush tries again in 30 minutes (or run `metarush renderer install`).", why)
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Minute):
		}
		if e.Check(false).State != StateMissing {
			return
		}
	}
}

// npmCI installs exactly the locked packages of the video workspace (its
// development packages included: motion-graphic type checks need React's
// types). The environment puts this Node.js first on PATH so npm's scripts
// run with it.
func (e *Env) npmCI(ctx context.Context, npm, dir string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, packagesTimeout)
	defer cancel()
	var tail lineTail
	_, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.Node, Dir: dir, Env: e.childEnv(),
		Args:   []string{npm, "ci", "--workspace", "video", "--include=dev", "--no-audit", "--no-fund", "--no-update-notifier", "--loglevel=error"},
		OnLine: tail.add})
	if err != nil {
		var ef *tools.ErrFailed
		if errors.As(err, &ef) {
			return strings.TrimSpace(tail.String() + "\n" + ef.Stderr), err
		}
		return tail.String(), err
	}
	return tail.String(), nil
}

// runSetupScript runs render/browser.mjs: it downloads the headless browser
// when missing and reports the Remotion version the package loads.
func (e *Env) runSetupScript(ctx context.Context, dir string, onProgress func(float64)) (string, string, error) {
	ctx, cancel := context.WithTimeout(ctx, browserTimeout)
	defer cancel()
	var mu sync.Mutex
	remotion, scriptErr, done := "", "", false
	var tail lineTail
	_, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.Node, Dir: filepath.Join(dir, "video"), Env: e.childEnv(), Background: true,
		Args: []string{"render/browser.mjs"},
		OnLine: func(line string) {
			tail.add(line)
			var ev struct {
				Type     string  `json:"type"`
				Remotion string  `json:"remotion"`
				Progress float64 `json:"progress"`
				Message  string  `json:"message"`
			}
			if json.Unmarshal([]byte(line), &ev) != nil {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			switch ev.Type {
			case "versions":
				remotion = ev.Remotion
			case "progress":
				if onProgress != nil {
					onProgress(ev.Progress)
				}
			case "error":
				scriptErr = ev.Message
			case "done":
				done = true
			}
		}})
	mu.Lock()
	defer mu.Unlock()
	switch {
	case scriptErr != "":
		return remotion, tail.String(), errors.New(scriptErr)
	case err != nil:
		var ef *tools.ErrFailed
		if errors.As(err, &ef) {
			return remotion, strings.TrimSpace(tail.String() + "\n" + ef.Stderr), fmt.Errorf("the renderer did not start: %w", err)
		}
		return remotion, tail.String(), err
	case !done:
		return remotion, tail.String(), errors.New("the renderer's setup script stopped early")
	}
	return remotion, tail.String(), nil
}

// linkBrowser points the new renderer's browser folder at the one shared by
// every renderer with the same Remotion version (Remotion keeps Chrome
// Headless Shell under video/node_modules/.remotion), so an upgrade that keeps
// Remotion does not download the browser again. The link is relative, so a
// home folder that moves keeps working.
func (e *Env) linkBrowser(dir string) error {
	nm := filepath.Join(dir, "video", "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		return err
	}
	shared := filepath.Join(Root(e.Home), browsersDir, "remotion-"+Sanitize(e.Pkg.remotion))
	if err := os.MkdirAll(shared, 0o755); err != nil {
		return err
	}
	link := filepath.Join(nm, ".remotion")
	if _, err := os.Lstat(link); err == nil {
		if err := os.RemoveAll(link); err != nil {
			return err
		}
	}
	rel, err := filepath.Rel(nm, shared)
	if err != nil {
		return err
	}
	return os.Symlink(rel, link)
}

// childEnv puts the Node.js MetaRush runs first on PATH (npm and its scripts
// spawn `node`) and keeps npm quiet.
func (e *Env) childEnv() []string {
	path := os.Getenv("PATH")
	if n := e.Tools.Path(tools.Node); n != "" {
		path = filepath.Dir(n) + string(os.PathListSeparator) + path
	}
	return []string{"PATH=" + path, "npm_config_update_notifier=false", "npm_config_fund=false", "npm_config_audit=false"}
}

// NPM is npm's command-line script for this Node.js ("" when there is none):
// Remotion sources install their own locked packages with it (D133).
func (e *Env) NPM() string { return e.npmCLI() }

// ChildEnv is the environment npm runs with (this Node.js first on PATH).
func (e *Env) ChildEnv() []string { return e.childEnv() }

// BrowserDir is the shared headless browser folder of a Remotion version.
func BrowserDir(home, remotionVersion string) string {
	return filepath.Join(Root(home), browsersDir, "remotion-"+Sanitize(remotionVersion))
}

// npmCLI finds npm's command-line script for the Node.js MetaRush uses: the
// npm installed next to it (Homebrew, nvm, the official installers, Debian),
// or tools.npm. It is run as `node <npm-cli.js>`, so npm always runs with the
// same Node.js.
func (e *Env) npmCLI() string {
	var cands []string
	if e.Cfg.NPM != "" {
		cands = append(cands, e.Cfg.NPM)
	}
	if node := e.Tools.Path(tools.Node); node != "" {
		dirs := []string{filepath.Dir(node)}
		if r, err := filepath.EvalSymlinks(node); err == nil && filepath.Dir(r) != dirs[0] {
			dirs = append(dirs, filepath.Dir(r))
		}
		for _, d := range dirs {
			cands = append(cands, filepath.Join(d, "npm"),
				filepath.Join(d, "..", "lib", "node_modules", "npm", "bin", "npm-cli.js"),
				filepath.Join(d, "..", "libexec", "lib", "node_modules", "npm", "bin", "npm-cli.js"))
		}
	}
	if p, err := exec.LookPath("npm"); err == nil {
		cands = append(cands, p)
	}
	for _, c := range cands {
		r, err := filepath.EvalSymlinks(c)
		if err != nil {
			continue
		}
		if st, err := os.Stat(r); err != nil || st.IsDir() {
			continue
		}
		if strings.HasSuffix(r, ".js") || strings.HasSuffix(r, ".cjs") {
			return r
		}
		// A launcher script: npm-cli.js sits in the package next to it.
		for _, j := range []string{filepath.Join(filepath.Dir(r), "npm-cli.js"), filepath.Join(filepath.Dir(r), "..", "lib", "node_modules", "npm", "bin", "npm-cli.js")} {
			if exists(j) {
				return j
			}
		}
	}
	return ""
}

func nodeMajor(v string) int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '.'); i > 0 {
		v = v[:i]
	}
	n, _ := strconv.Atoi(v)
	return n
}

// plainError is an error's first line without a tool's prefix noise.
func plainError(err error) string {
	if ae := apperr.From(err); ae != nil && ae.Code != apperr.Internal {
		return ae.Message
	}
	s := strings.TrimSpace(err.Error())
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// lineTail keeps the last lines a program printed (for failure details).
type lineTail struct {
	mu    sync.Mutex
	lines []string
}

func (t *lineTail) add(line string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > 40 {
		t.lines = t.lines[len(t.lines)-40:]
	}
}

func (t *lineTail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

/* ---------- prune ---------- */

// PruneResult says what prune removed and kept.
type PruneResult struct {
	Removed    []Installed `json:"removed"`
	Kept       []Installed `json:"kept"`
	InUse      []Installed `json:"inUse"` // older renderers a running render still uses (kept for now)
	FreedBytes int64       `json:"freedBytes"`
}

// Prune keeps this version's renderer and the most recently set up others,
// keep in all (DefaultKeep when keep <= 0), and removes the rest, never one a
// render is using, plus browsers no remaining renderer uses and leftovers of
// interrupted setups.
func (e *Env) Prune(ctx context.Context, keep int) (PruneResult, error) {
	root := Root(e.Home)
	if !exists(root) {
		return PruneResult{Removed: []Installed{}, Kept: []Installed{}, InUse: []Installed{}}, nil
	}
	lk, err := waitLock(ctx, filepath.Join(root, lockName), syscall.LOCK_EX)
	if err != nil {
		return PruneResult{}, err
	}
	defer lk.release()
	if keep <= 0 {
		keep = DefaultKeep
	}
	return e.prune(keep)
}

// inUse is a renderer a render is using, so it isn't removed.
func inUse() error {
	return apperr.New(apperr.Conflict, "A render is using the renderer right now, so it wasn't removed.").
		WithHint("Remove it once the render finishes.")
}

// RemoveCurrent removes this version's renderer (Settings → Components'
// Remove, D324) the way prune removes older ones: with the setup lock held,
// and only when no render holds it (Use's shared lock on its manifest).
func (e *Env) RemoveCurrent(ctx context.Context) error {
	if e.Pkg == nil || e.Cfg.DevRenderer {
		return nil // a developer checkout is the developer's
	}
	root := Root(e.Home)
	dir := e.dir(e.Pkg.ID())
	if !exists(dir) {
		return nil
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	lk, err := waitLock(wctx, filepath.Join(root, lockName), syscall.LOCK_EX)
	if err != nil {
		return apperr.New(apperr.Conflict, "The renderer is being set up right now, so it wasn't removed.").WithHint("Remove it once that finishes.")
	}
	defer lk.release()
	var l *fileLock
	if exists(filepath.Join(dir, manifestName)) {
		var ok bool
		l, ok, err = tryLock(filepath.Join(dir, manifestName), syscall.LOCK_EX)
		if err != nil {
			return err
		}
		if !ok {
			return inUse()
		}
	}
	err = os.RemoveAll(dir)
	if l != nil {
		l.release()
	}
	if err != nil {
		return err
	}
	rec := e.loadRecord()
	delete(rec.Installed, e.Pkg.ID())
	e.saveRecord(rec)
	return nil
}

// prune runs with the setup lock held.
func (e *Env) prune(keep int) (PruneResult, error) {
	res := PruneResult{Removed: []Installed{}, Kept: []Installed{}, InUse: []Installed{}}
	root := Root(e.Home)
	current := ""
	if e.Pkg != nil {
		current = e.Pkg.ID()
	}
	all := e.versions(current)
	kept := 0
	for _, in := range all {
		if in.Current {
			res.Kept = append(res.Kept, in)
			kept++
		}
	}
	rec := e.loadRecord()
	for _, in := range all {
		if in.Current {
			continue
		}
		dir := filepath.Join(root, in.ID)
		if kept < keep && in.InstalledAt != nil {
			res.Kept = append(res.Kept, in)
			kept++
			continue
		}
		var l *fileLock
		if exists(filepath.Join(dir, manifestName)) {
			var ok bool
			var err error
			l, ok, err = tryLock(filepath.Join(dir, manifestName), syscall.LOCK_EX)
			if err == nil && !ok {
				res.InUse = append(res.InUse, in)
				continue
			}
		}
		in.Bytes = dirSize(dir)
		err := os.RemoveAll(dir)
		if l != nil {
			l.release()
		}
		if err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, in)
		res.FreedBytes += in.Bytes
		delete(rec.Installed, in.ID)
	}
	e.saveRecord(rec)
	// Browsers no kept renderer uses, and leftovers of interrupted setups.
	used := map[string]bool{}
	if e.Pkg != nil {
		used["remotion-"+Sanitize(e.Pkg.remotion)] = true
	}
	for _, in := range append(append([]Installed{}, res.Kept...), res.InUse...) {
		if in.Remotion != "" {
			used["remotion-"+Sanitize(in.Remotion)] = true
		}
	}
	if entries, err := os.ReadDir(filepath.Join(root, browsersDir)); err == nil {
		for _, d := range entries {
			if d.IsDir() && !used[d.Name()] {
				p := filepath.Join(root, browsersDir, d.Name())
				res.FreedBytes += dirSize(p)
				_ = os.RemoveAll(p)
			}
		}
	}
	if entries, err := os.ReadDir(root); err == nil {
		for _, d := range entries {
			if d.IsDir() && (strings.HasPrefix(d.Name(), ".tmp-") || strings.HasPrefix(d.Name(), ".old-")) {
				if info, err := d.Info(); err == nil && time.Since(info.ModTime()) > time.Hour {
					_ = os.RemoveAll(filepath.Join(root, d.Name()))
				}
			}
		}
	}
	return res, nil
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// Export writes the renderer package's files under dir (release archives
// carry them next to the binary, for reading; the binary sets its renderer up
// from its own copy).
func Export(dir string) (*Package, error) {
	pkg, err := Embedded()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return pkg, pkg.Extract(dir)
}

/* ---------- locks (flock: released by the system when a process dies) ---------- */

type fileLock struct{ f *os.File }

func (l *fileLock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}

// tryLock takes a lock without waiting; ok is false when another holder has it.
func tryLock(path string, how int) (*fileLock, bool, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return nil, false, err
	}
	if err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &fileLock{f: f}, true, nil
}

func waitLock(ctx context.Context, path string, how int) (*fileLock, error) {
	for {
		l, ok, err := tryLock(path, how)
		if err != nil || ok {
			return l, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// lockShared marks a renderer in use (waits briefly while prune holds it).
func lockShared(path string) (*fileLock, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return waitLock(ctx, path, syscall.LOCK_SH)
}

// installing reports a setup another holder of the lock is running.
func (e *Env) installing() *Progress {
	root := Root(e.Home)
	p := filepath.Join(root, lockName)
	if !exists(p) {
		return nil
	}
	l, ok, err := tryLock(p, syscall.LOCK_SH)
	if err != nil {
		return nil
	}
	if ok {
		l.release()
		return nil
	}
	var pr Progress
	if b, err := os.ReadFile(filepath.Join(root, installingName)); err == nil && json.Unmarshal(b, &pr) == nil {
		return &pr
	}
	return &Progress{}
}
