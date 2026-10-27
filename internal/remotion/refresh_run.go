package remotion

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	rnd "github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Running a refresh (D174, D175): a fresh copy of the version, a minimal
// environment with only the keys picked, every step in the operating
// system's sandbox, then what the steps wrote in the outputs becomes a new
// version that goes through the normal check and waits for a person.

// Refresh limits.
var (
	refreshStepLimit  = 30 * time.Minute
	refreshTotalLimit = 2 * time.Hour
)

const (
	refreshLogHead = 256 << 10 // the log keeps its start…
	refreshLogTail = 768 << 10 // …and its end
	maxLeftOutRows = 60
)

// refresher runs one refresh; its seams let tests stand in for the package
// environment and the check (both need this version's renderer).
type refresher struct {
	a *app.App
	// modules gives the folder of the version's installed packages
	// (node_modules), installing them when this computer doesn't have them.
	modules func(ctx context.Context, m *Manifest, say func(string)) (string, error)
	// check checks the new version (D137) and returns its summary.
	check func(ctx context.Context, s *models.RemotionSource, v *models.RemotionVersion, say func(string)) (string, error)
}

func newRefresher(a *app.App) *refresher {
	r := &refresher{a: a}
	r.modules = func(ctx context.Context, m *Manifest, say func(string)) (string, error) {
		videoDir, release, err := rendererDir(a)
		if err != nil {
			return "", err
		}
		defer release()
		pi, err := versionPackages(ctx, a, m)
		if err != nil {
			return "", err
		}
		env, err := ensureEnv(ctx, a, pi, videoDir, say)
		if err != nil {
			return "", err
		}
		return filepath.Join(env, "node_modules"), nil
	}
	r.check = func(ctx context.Context, s *models.RemotionSource, v *models.RemotionVersion, say func(string)) (string, error) {
		rep, err := checkVersion(ctx, a, s, v, checkOptions{Message: v.Message, Say: say})
		if err != nil {
			return "", err
		}
		if rep.Check != nil {
			return rep.Check.Summary, nil
		}
		return "", nil
	}
	return r
}

// RunRefreshJob runs a remotion-refresh job (the worker's handler).
func RunRefreshJob(ctx context.Context, a *app.App, j *models.Job, report func(pct float64, msg string)) (map[string]any, error) {
	if report == nil {
		report = func(float64, string) {}
	}
	ra := *a
	ra.Actor = j.CreatedBy // the person who started it, not the worker
	id, _ := j.Payload["refresh"].(string)
	return newRefresher(&ra).run(ctx, id, j.ID, report)
}

// run runs the refresh with this ID.
func (rf *refresher) run(ctx context.Context, id, jobID string, report func(float64, string)) (_ map[string]any, retErr error) {
	a := rf.a
	if report == nil {
		report = func(float64, string) {}
	}
	r, err := getRefresh(ctx, a, id)
	if err != nil {
		return nil, &Final{Err: errors.New("that refresh isn't recorded any more")}
	}
	recorded := false // the record says how it ended
	defer func() {
		if retErr != nil && !recorded && r.State == models.RemotionRunQueued {
			_ = setRefresh(context.WithoutCancel(ctx), a, r.ID, bson.M{"state": models.RemotionRunFailed, "error": "It stopped: " + clipText(apperrMessage(retErr), 300), "finishedAt": time.Now().UTC()})
		}
	}()
	fail := func(msg string, steps []models.RemotionStepRun, logObj *models.RemotionObject) error {
		now := time.Now().UTC()
		set := bson.M{"state": models.RemotionRunFailed, "error": msg, "finishedAt": now}
		if steps != nil {
			set["steps"] = steps
		}
		if logObj != nil {
			set["log"] = logObj
		}
		_ = setRefresh(context.WithoutCancel(ctx), a, r.ID, set)
		recorded = true
		return &Final{Err: errors.New(msg)}
	}
	switch {
	case r.Ran:
		// Its steps began once and were cut short: never run again by itself (keys cost money).
		return nil, fail(fmt.Sprintf("Refresh %d stopped before it finished (MetaRush stopped on %s), so nothing was kept. Start it again if you want it.", r.Number, r.Machine), nil, nil)
	case r.State != models.RemotionRunQueued:
		return nil, &Final{Err: fmt.Errorf("refresh %d already ran", r.Number)}
	case r.Installation != "" && r.Installation != a.Cfg.Installation.ID:
		return nil, fail(fmt.Sprintf("Refresh %d was started for %s, which has its keys; it runs only there.", r.Number, r.Machine), nil, nil)
	}
	// A private key never goes into the steps, whatever the plan said (D282).
	for _, k := range r.RefreshSteps.Keys {
		if p, err := providers.Get(k.Provider); err == nil {
			if err := providers.NotLent(p, refreshSteps); err != nil {
				return nil, fail(apperrMessage(err), nil, nil)
			}
		}
	}
	// What may leave (D290), asked again before its steps run.
	if err := leave.Check(ctx, a, leave.Refresh(r.ProjectID, fmt.Sprintf("refresh %d", r.Number))); err != nil {
		return nil, fail(apperrMessage(err), nil, nil)
	}
	s, err := getSource(ctx, a, r.SourceID)
	if err != nil {
		return nil, fail("The Remotion project was removed.", nil, nil)
	}
	from, err := getVersion(ctx, a, r.From)
	if err != nil {
		return nil, fail("The version it started from isn't there any more.", nil, nil)
	}
	iso := isolationHere(a)
	if iso.Kind == "" {
		return nil, fail(iso.Problem, nil, nil)
	}
	// One refresh of a source at a time (two started at once: the second waits for a person to start it again).
	if other := runningRefresh(ctx, a, s.ID, r.ID); other != nil {
		return nil, fail(fmt.Sprintf("Refresh %d of “%s” is still running; start this one again when it is done.", other.Number, s.Title), nil, nil)
	}
	if iso.Kind != r.Isolation && r.Isolation != "" {
		return nil, fail("This computer keeps refreshes apart in another way than the plan said; look at the plan again and start it again.", nil, nil)
	}
	started := time.Now().UTC()
	_ = setRefresh(ctx, a, r.ID, bson.M{"state": models.RemotionRunRunning, "startedAt": started})
	ctx, cancel := context.WithTimeout(ctx, refreshTotalLimit)
	defer cancel()
	say := func(pct float64) func(string) { return func(m string) { report(pct, m) } }

	// 1. The copy and its packages.
	report(0.02, "making a fresh copy of version "+fmt.Sprint(from.Number))
	m, err := loadManifest(ctx, a, from)
	if err != nil {
		return nil, fail("MetaRush couldn't read the version's files: "+apperrMessage(err), nil, nil)
	}
	root, err := os.MkdirTemp("", "metarush-refresh-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(root)
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real // the sandbox matches real paths (/private/var on a Mac)
	}
	work, home, tmp := filepath.Join(root, "work"), filepath.Join(root, "home"), filepath.Join(root, "tmp")
	for _, d := range []string{work, home, tmp} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	for _, f := range m.Files {
		src, err := localObject(ctx, a, f.SHA256, f.Size)
		if err != nil {
			return nil, fail("MetaRush couldn't get the version's files: "+apperrMessage(err), nil, nil)
		}
		if err := copyNoLink(src, filepath.Join(work, filepath.FromSlash(f.Path))); err != nil {
			return nil, err
		}
	}
	report(0.06, "getting its packages ready")
	modules, err := rf.modules(ctx, m, say(0.08))
	if err != nil {
		return nil, fail("Its packages couldn't be set up: "+apperrMessage(err), nil, nil)
	}
	cache := filepath.Join(a.Cfg.Cache.Dir, "remotion", "refresh", s.ID)
	for _, d := range []string{"npm", "browsers", "xdg"} {
		if err := os.MkdirAll(filepath.Join(cache, d), 0o700); err != nil {
			return nil, err
		}
	}
	if real, err := filepath.EvalSymlinks(cache); err == nil {
		cache = real
	}
	ap := &apart{Root: root, Cache: cache, Work: work, Hide: hiddenFor(a, originalFolder(a, s))}
	if modules != "" {
		if iso.Kind == isoLinux {
			ap.Modules = modules
			_ = os.MkdirAll(filepath.Join(work, "node_modules"), 0o755)
		} else if err := cloneModules(modules, filepath.Join(work, "node_modules")); err != nil {
			return nil, fail("Its packages couldn't be copied for the refresh: "+err.Error(), nil, nil)
		}
	}
	node := a.Tools.Path(tools.Node)
	npm := rnd.New(a.Cfg, a.Tools).NPM()
	if node == "" {
		return nil, fail("A refresh needs Node.js, which isn't installed on this computer.", nil, nil)
	}
	ap.Read = readableFor(node)
	if npm != "" {
		ap.Read = dedupeStrings(append(ap.Read, filepath.Dir(filepath.Dir(npm))))
	}

	// 2. The keys picked, and nothing else.
	red := logbook.NewRedactor()
	red.FromConfig(a.Cfg)
	var keyEnv []string
	for _, k := range r.RefreshSteps.Keys {
		v, err := providers.MustKey(a.Cfg, k.Provider)
		if err != nil {
			return nil, fail(apperrMessage(err), nil, nil)
		}
		red.Add(v)
		keyEnv = append(keyEnv, k.Env+"="+v)
	}
	env := refreshEnv(node, work, home, tmp, cache, keyEnv)
	ap.Ports = ownPorts(a)
	if iso.Kind == isoLinux {
		// Linux can't close this computer's loopback to a program that shares
		// its network, so the steps get one of their own and reach the
		// internet through MetaRush's proxy: any public host over HTTP(S),
		// never this computer or a local network (D235's note on D174).
		proxy, err := sandbox.StartProxy(sandbox.ProxyConfig{AnyHost: true, Token: randomToken(), MaxBytes: 64 << 30, MaxRequests: 1 << 20})
		if err != nil {
			return nil, fail("MetaRush couldn't open the way out for the refresh.", nil, nil)
		}
		defer proxy.Close()
		helpers := filepath.Join(root, "proxy")
		ca, preload, pyDir, err := sandbox.WriteHelpers(helpers, proxy.CAPEM())
		if err == nil {
			ap.ProxySocket = filepath.Join(helpers, "proxy.sock")
			err = proxy.ListenUnix(ap.ProxySocket)
		}
		if err != nil {
			return nil, fail("MetaRush couldn't open the way out for the refresh.", nil, nil)
		}
		ap.ProxyPort = proxy.Port()
		ap.Self, _ = os.Executable()
		env = append(env, sandbox.ProxyEnv(proxy.URL(), ca, preload, pyDir)...)
	}

	// 3. The steps, in order, each in the sandbox.
	_ = setRefresh(ctx, a, r.ID, bson.M{"ran": true})
	lg := newRefreshLog(red)
	var runs []models.RemotionStepRun
	n := len(r.RefreshSteps.Steps)
	for i, st := range r.RefreshSteps.Steps {
		pct := 0.1 + 0.6*float64(i)/float64(max(n, 1))
		report(pct, fmt.Sprintf("step %d of %d: %s", i+1, n, stepLabel(st)))
		lg.header(fmt.Sprintf("step %d of %d: %s", i+1, n, st.Run))
		run, err := rf.step(ctx, ap, iso.Kind, node, npm, env, st, lg)
		runs = append(runs, run)
		if err != nil || !run.OK {
			logObj := lg.save(ctx, a)
			if ctx.Err() != nil {
				state := models.RemotionRunCanceled
				msg := fmt.Sprintf("Refresh %d was stopped during step %d (%s); nothing was kept.", r.Number, i+1, st.Run)
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					state, msg = models.RemotionRunFailed, fmt.Sprintf("Refresh %d took longer than %s and was stopped during step %d (%s); nothing was kept.", r.Number, refreshTotalLimit, i+1, st.Run)
				}
				_ = setRefresh(context.WithoutCancel(ctx), a, r.ID, bson.M{"state": state, "error": msg, "steps": runs, "log": logObj, "finishedAt": time.Now().UTC()})
				recorded = true
				return nil, &Final{Err: errors.New(msg)}
			}
			msg := fmt.Sprintf("Step %d of %d (%s) didn't work", i+1, n, st.Run)
			if run.Last != "" {
				msg += ": " + lastLineOf(run.Last)
			}
			if err != nil {
				msg += " (" + plainStepError(err) + ")"
			}
			return nil, fail(msg+". Nothing was kept.", runs, logObj)
		}
	}
	logObj := lg.save(ctx, a)

	// 4. What it wrote.
	report(0.72, "looking at what the steps wrote")
	take, err := os.MkdirTemp("", "metarush-refresh-take-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(take)
	taken, leftOut, err := collectOutputs(work, take, m, r.RefreshSteps.Outputs)
	if err != nil {
		return nil, fail("MetaRush couldn't read what the steps wrote: "+err.Error(), runs, logObj)
	}
	done := func(set bson.M) {
		set["state"], set["steps"], set["log"], set["finishedAt"] = models.RemotionRunDone, runs, logObj, time.Now().UTC()
		set["leftOut"] = leftOut
		_ = setRefresh(context.WithoutCancel(ctx), a, r.ID, set)
		recorded = true
	}
	if len(taken) == 0 {
		msg := fmt.Sprintf("Refresh %d of “%s” ran its steps, but they changed nothing in %s, so there is no new version.", r.Number, s.Title, joinAnd(r.RefreshSteps.Outputs))
		if len(leftOut) > 0 {
			msg += fmt.Sprintf(" %s they changed elsewhere %s left out.", upperFirst(plural(len(leftOut), "file", "files")), map[bool]string{true: "was", false: "were"}[len(leftOut) == 1])
		}
		done(bson.M{"summary": msg, "changes": []models.RemotionFileChange{}})
		report(1, msg)
		return map[string]any{"message": msg, "refresh": r.ID, "number": r.Number, "sourceId": s.ID, "projectId": s.ProjectID}, nil
	}

	// 5. The new version, on the newest one, through the normal check.
	report(0.76, "saving the new version")
	nv, changes, err := rf.saveVersion(ctx, s, r, taken)
	if err != nil {
		return nil, fail("MetaRush couldn't save what the steps wrote: "+apperrMessage(err), runs, logObj)
	}
	report(0.8, "checking the new version")
	summary, err := rf.check(ctx, s, nv, say(0.85))
	if err != nil {
		summary = "Its check didn't run: " + apperrMessage(err)
	}
	if fresh, err := getVersion(ctx, a, nv.Hash); err == nil {
		nv = fresh
	}
	msg := fmt.Sprintf("Refresh %d of “%s” made version %d (%s).", r.Number, s.Title, nv.Number, plural(len(changes), "file changed", "files changed"))
	switch {
	case nv.Check != nil && !nv.Check.OK:
		msg += " It didn't pass its check: " + strings.TrimSpace(nv.Check.Summary)
	case nv.State == models.RemotionWaiting:
		msg += " It waits for a person to look at it and approve it."
	}
	done(bson.M{"version": nv.Hash, "versionNumber": nv.Number, "changes": changes, "summary": firstNonEmpty(summary, msg)})
	a.Audit(context.WithoutCancel(ctx), "remotion.refreshed", "remotion", s.ID, s.ProjectID, map[string]any{"refresh": r.ID, "number": r.Number, "version": nv.Hash, "changed": len(changes), "leftOut": len(leftOut)})
	report(1, msg)
	return map[string]any{"message": msg, "refresh": r.ID, "number": r.Number, "version": nv.Hash, "versionNumber": nv.Number, "sourceId": s.ID, "projectId": s.ProjectID}, nil
}

// runningRefresh is another refresh of the source whose steps are running now (nil when none).
func runningRefresh(ctx context.Context, a *app.App, sourceID, except string) *models.RemotionRefresh {
	var rs []models.RemotionRefresh
	if err := a.DB.Find(ctx, store.CRemotionRefreshes, a.WorkspaceID(), bson.M{"sourceId": sourceID, "state": models.RemotionRunRunning, "_id": bson.M{"$ne": except},
		"startedAt": bson.M{"$gte": time.Now().UTC().Add(-refreshTotalLimit)}}, &rs, options.Find().SetLimit(1)); err != nil || len(rs) == 0 {
		return nil
	}
	return &rs[0]
}

// stepLabel names a step in progress lines: “vo”, scripts/make.mjs.
func stepLabel(st models.RemotionStep) string {
	if st.Kind == models.RemotionStepScript {
		return "“" + st.Name + "”"
	}
	return st.Name
}

// originalFolder is the source's folder on this computer ("" when it came
// from GitHub or another computer).
func originalFolder(a *app.App, s *models.RemotionSource) string {
	if s.Origin.Kind != models.RemotionFromFolder || s.Origin.Machine != a.Cfg.Installation.Name || s.Origin.Path == "" {
		return ""
	}
	return expandHome(s.Origin.Path)
}

// refreshEnv is the whole environment a step gets: built from nothing.
func refreshEnv(node, work, home, tmp, cache string, keys []string) []string {
	bin := filepath.Dir(node)
	path := strings.Join([]string{bin, filepath.Join(work, "node_modules", ".bin"), "/usr/bin", "/bin", "/usr/sbin", "/sbin"}, string(os.PathListSeparator))
	who := "metarush"
	if u, err := user.Current(); err == nil && u.Username != "" {
		who = u.Username
	}
	env := []string{"PATH=" + path, "HOME=" + home, "TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp, "USER=" + who, "LOGNAME=" + who, "SHELL=/bin/sh",
		"LANG=en_US.UTF-8", "TERM=dumb", "CI=1", "FORCE_COLOR=0", "NO_UPDATE_NOTIFIER=1",
		"XDG_CACHE_HOME=" + filepath.Join(cache, "xdg"), "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"npm_config_cache=" + filepath.Join(cache, "npm"), "npm_config_userconfig=" + filepath.Join(home, ".npmrc"), "npm_config_update_notifier=false",
		"npm_config_fund=false", "npm_config_audit=false", "npm_config_yes=false", "PLAYWRIGHT_BROWSERS_PATH=" + filepath.Join(cache, "browsers")}
	return append(env, keys...)
}

// randomToken is the refresh proxy's password.
func randomToken() string {
	b := make([]byte, 18)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

// step runs one step and says how it went.
func (rf *refresher) step(ctx context.Context, ap *apart, iso, node, npm string, env []string, st models.RemotionStep, lg *refreshLog) (models.RemotionStepRun, error) {
	run := models.RemotionStepRun{Run: st.Run}
	var args []string
	switch st.Kind {
	case models.RemotionStepScript:
		if npm == "" {
			return run, errors.New("npm isn't installed next to Node.js")
		}
		args = append([]string{npm, "run", st.Name}, withDashes(st.Args)...)
	case models.RemotionStepNPX:
		if npm == "" {
			return run, errors.New("npm isn't installed next to Node.js")
		}
		args = append([]string{npm, "exec", "--no", "--", st.Name}, st.Args...)
	case models.RemotionStepNode:
		args = append([]string{st.Name}, st.Args...)
	default:
		return run, errors.New("an unknown kind of step")
	}
	tool, argv, err := ap.command(iso, node, args)
	if err != nil {
		return run, err
	}
	sctx, cancel := context.WithTimeout(ctx, refreshStepLimit)
	defer cancel()
	start := time.Now()
	lg.startStep()
	out, errw := lg.stream(), lg.stream()
	_, err = rf.a.Tools.Run(sctx, tools.Spec{Tool: tool, Args: argv, Dir: ap.Work, Env: env, CleanEnv: true, Background: true, Stdout: out, Stderr: errw, Contained: true})
	out.flush()
	errw.flush()
	run.Seconds = float64(time.Since(start).Milliseconds()) / 1000
	if err == nil {
		run.OK = true
		return run, nil
	}
	var ef *tools.ErrFailed
	if errors.As(err, &ef) {
		var ee interface{ ExitCode() int }
		if errors.As(ef.Err, &ee) {
			run.Exit = ee.ExitCode()
		}
		run.Last = lg.lastLines(8)
		if errors.Is(sctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return run, fmt.Errorf("it ran for longer than %s", refreshStepLimit)
		}
		return run, nil
	}
	run.Last = lg.lastLines(8)
	if errors.Is(sctx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
		return run, fmt.Errorf("it ran for longer than %s", refreshStepLimit)
	}
	return run, err
}

func plainStepError(err error) string {
	msg := err.Error()
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

func lastLineOf(s string) string {
	ls := strings.Split(strings.TrimSpace(s), "\n")
	l := strings.TrimSpace(ls[len(ls)-1])
	if len(l) > 240 {
		l = l[:240] + "…"
	}
	return l
}

// ---- files ----

// copyNoLink copies a file for a refresh: an APFS clone or a real copy,
// never a link (a step writing in place must never reach MetaRush's cache).
func copyNoLink(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	if cloneOS(src, dst) == nil {
		return os.Chmod(dst, 0o644)
	}
	return copyFile(src, dst)
}

// cloneModules gives the refresh its own copy of the installed packages (a
// clone of the folder on APFS, else file by file; never links into the
// package environment), without the link to Remotion's shared browser.
func cloneModules(src, dst string) error {
	_ = os.RemoveAll(dst)
	if cloneOS(src, dst) != nil {
		err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(src, p)
			target := filepath.Join(dst, rel)
			switch {
			case d.IsDir():
				return os.MkdirAll(target, 0o755)
			case d.Type()&os.ModeSymlink != 0:
				l, err := os.Readlink(p)
				if err != nil {
					return err
				}
				return os.Symlink(l, target)
			}
			if err := copyFile(p, target); err != nil {
				return err
			}
			if info, err := d.Info(); err == nil && info.Mode()&0o111 != 0 {
				return os.Chmod(target, 0o755)
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	_ = os.RemoveAll(filepath.Join(dst, ".remotion"))
	return nil
}

// outputFile is a file the steps wrote that becomes part of the new version
// (local: its bytes; empty: removed).
type outputFile struct {
	ManifestFile
	local   string
	removed bool
	change  string
}

// collectOutputs compares the copy with the version it started from: changes
// in the outputs are taken under the import's rules, the rest is listed. The
// copy is read through an os.Root (no way out through a link or ".."), each
// file opened without following a link, and every file that may be taken is
// copied into take, a folder of MetaRush's, as it is read: the new version is
// made from those copies, never from the folder the steps wrote.
func collectOutputs(work, take string, base *Manifest, outputs []string) ([]outputFile, []models.RemotionFileLeftOut, error) {
	om, err := newOutputMatcher(outputs)
	if err != nil {
		return nil, nil, err
	}
	root, err := os.OpenRoot(work)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	var taken []outputFile
	var left []models.RemotionFileLeftOut
	leave := func(rel, why string) {
		if len(left) < maxLeftOutRows {
			left = append(left, models.RemotionFileLeftOut{Path: rel, Why: why})
		}
	}
	seen := map[string]bool{}
	copies := 0
	err = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if why := skipDir(rel); why != "" {
				// The packages, git, caches: never part of a version (no line for the packages).
				if why != whyPackages && why != whyGit && dirChanged(base, rel) {
					leave(rel+"/", why)
				}
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			if om.match(rel) {
				leave(rel, whyLink)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if err != nil {
			if om.match(rel) {
				leave(rel, whyLink) // it became a link, or can't be read
			}
			return nil
		}
		defer f.Close()
		if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
			return nil
		}
		seen[rel] = true
		var dst string
		var cp *os.File
		if om.match(rel) {
			copies++
			dst = filepath.Join(take, fmt.Sprintf("%06d", copies))
			if cp, err = os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600); err != nil {
				return err
			}
		}
		var also io.Writer
		if cp != nil {
			also = cp
		}
		sha, size, secret, err := hashReader(f, rel, also)
		if cp != nil {
			if cerr := cp.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			return err
		}
		keep := false
		defer func() {
			if dst != "" && !keep {
				_ = os.Remove(dst)
			}
		}()
		old, had := base.File(rel)
		if had && old.SHA256 == sha {
			return nil // unchanged
		}
		change := "added"
		if had {
			change = "changed"
		}
		switch {
		case !om.match(rel):
			leave(rel, "written outside the places this refresh may write")
		case skipFile(rel) != "":
			leave(rel, skipFile(rel))
		case secret != "":
			leave(rel, whySecretText)
		case isPublic(rel) && size > maxPublicFileBytes, !isPublic(rel) && size > maxCodeFileBytes:
			leave(rel, whyTooBig)
		default:
			keep = true
			taken = append(taken, outputFile{ManifestFile: ManifestFile{Path: rel, Size: size, SHA256: sha}, local: dst, change: change})
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for _, f := range base.Files {
		if seen[f.Path] {
			continue
		}
		if om.match(f.Path) {
			taken = append(taken, outputFile{ManifestFile: f, removed: true, change: "removed"})
		} else {
			leave(f.Path, "removed outside the places this refresh may write (kept)")
		}
	}
	sort.Slice(taken, func(i, j int) bool { return taken[i].Path < taken[j].Path })
	return taken, left, nil
}

// dirChanged reports a skipped folder the version had files in (worth saying it was left out).
func dirChanged(base *Manifest, rel string) bool {
	for _, f := range base.Files {
		if strings.HasPrefix(f.Path, rel+"/") {
			return true
		}
	}
	return false
}

// saveVersion puts the outputs on the source's newest version (the one the
// refresh started from, or one made meanwhile) and records the new version.
func (rf *refresher) saveVersion(ctx context.Context, s *models.RemotionSource, r *models.RemotionRefresh, taken []outputFile) (*models.RemotionVersion, []models.RemotionFileChange, error) {
	a := rf.a
	fresh, err := getSource(ctx, a, s.ID)
	if err != nil {
		return nil, nil, err
	}
	head, err := getVersion(ctx, a, fresh.Head)
	if err != nil {
		return nil, nil, err
	}
	hm, err := loadManifest(ctx, a, head)
	if err != nil {
		return nil, nil, err
	}
	byPath := map[string]ManifestFile{}
	for _, f := range hm.Files {
		byPath[f.Path] = f
	}
	var send []snapFile
	for _, t := range taken {
		if t.removed {
			delete(byPath, t.Path)
			continue
		}
		byPath[t.Path] = t.ManifestFile
		send = append(send, snapFile{ManifestFile: t.ManifestFile, local: t.local})
	}
	if err := uploadFiles(ctx, a, send, nil); err != nil {
		return nil, nil, err
	}
	var list []ManifestFile
	for _, f := range byPath {
		list = append(list, f)
	}
	nm := NewManifest(list)
	if nm.Hash() == head.Hash {
		return head, nil, nil
	}
	mobj, err := putBytes(ctx, a, nm.Bytes(), "application/json")
	if err != nil {
		return nil, nil, err
	}
	nf, nb, pf, pb := totals(nm)
	var runs []string
	for _, st := range r.RefreshSteps.Steps {
		runs = append(runs, st.Run)
	}
	msg := clipText(fmt.Sprintf("Refreshed with its own steps (%s)", joinAnd(runs)), 2000)
	nv := &models.RemotionVersion{Hash: nm.Hash(), Parent: head.Hash, Kind: models.RemotionKindRefresh, Message: msg, By: a.Actor, At: time.Now().UTC(),
		Machine: a.Cfg.Installation.Name, Manifest: mobj, Files: nf, Bytes: nb, PublicFiles: pf, PublicBytes: pb}
	// The same package files are the same packages a person allowed (the check says so again).
	same := func(p string) bool {
		x, okx := hm.File(p)
		y, oky := nm.File(p)
		return okx == oky && x.SHA256 == y.SHA256
	}
	if same("package.json") && same("package-lock.json") {
		nv.PackagesAllowed, nv.Lock, nv.Remotion, nv.Packages = head.PackagesAllowed, head.Lock, head.Remotion, head.Packages
	}
	nv, _, err = insertVersion(ctx, a, fresh, nv)
	if err != nil {
		return nil, nil, err
	}
	changes, _ := fileChanges(Diff(hm, nm), func(f ManifestFile) ([]byte, bool) {
		if f.Size > 4<<20 {
			return nil, false
		}
		b, err := readObject(ctx, a, f.SHA256, f.Size)
		return b, err == nil
	})
	if changes == nil {
		changes = []models.RemotionFileChange{}
	}
	return nv, changes, nil
}

// ---- the log ----

// refreshLog keeps what the steps print, secrets hidden: its start and its
// end (at most refreshLogHead + refreshLogTail bytes).
type refreshLog struct {
	mu        sync.Mutex
	red       *logbook.Redactor
	head      bytes.Buffer
	tail      []string
	tailBytes int
	dropped   int
	step      []string // the current step's last lines
}

func newRefreshLog(red *logbook.Redactor) *refreshLog { return &refreshLog{red: red} }

func (l *refreshLog) line(s string) {
	s = l.red.String(strings.TrimRight(s, "\r"))
	l.mu.Lock()
	defer l.mu.Unlock()
	l.step = append(l.step, s)
	if len(l.step) > 40 {
		l.step = l.step[len(l.step)-40:]
	}
	if l.head.Len()+len(s)+1 <= refreshLogHead && l.dropped == 0 && len(l.tail) == 0 {
		l.head.WriteString(s + "\n")
		return
	}
	l.tail = append(l.tail, s)
	l.tailBytes += len(s) + 1
	for l.tailBytes > refreshLogTail && len(l.tail) > 1 {
		l.tailBytes -= len(l.tail[0]) + 1
		l.tail = l.tail[1:]
		l.dropped++
	}
}

func (l *refreshLog) header(s string) {
	l.line("")
	l.line("── " + s + " ──")
}

func (l *refreshLog) startStep() {
	l.mu.Lock()
	l.step = nil
	l.mu.Unlock()
}

func (l *refreshLog) lastLines(n int) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	ls := l.step
	for len(ls) > 0 && strings.TrimSpace(ls[len(ls)-1]) == "" {
		ls = ls[:len(ls)-1]
	}
	if len(ls) > n {
		ls = ls[len(ls)-n:]
	}
	return strings.Join(ls, "\n")
}

func (l *refreshLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var b strings.Builder
	b.WriteString(l.head.String())
	if l.dropped > 0 {
		fmt.Fprintf(&b, "… %d lines left out …\n", l.dropped)
	}
	for _, s := range l.tail {
		b.WriteString(s + "\n")
	}
	return b.String()
}

// save keeps the log as an object (nil when it can't be saved).
func (l *refreshLog) save(ctx context.Context, a *app.App) *models.RemotionObject {
	obj, err := putBytes(context.WithoutCancel(ctx), a, []byte(l.text()), "text/plain; charset=utf-8")
	if err != nil {
		return nil
	}
	return &obj
}

// stream is one output stream of a step, cut into lines.
func (l *refreshLog) stream() *logStream { return &logStream{l: l} }

type logStream struct {
	l   *refreshLog
	buf []byte
}

func (s *logStream) Write(p []byte) (int, error) {
	s.buf = append(s.buf, p...)
	for {
		i := bytes.IndexByte(s.buf, '\n')
		if i < 0 {
			break
		}
		s.l.line(string(s.buf[:i]))
		s.buf = s.buf[i+1:]
	}
	if len(s.buf) > 64<<10 { // a very long line: keep it as one
		s.l.line(string(s.buf))
		s.buf = nil
	}
	return len(p), nil
}

func (s *logStream) flush() {
	if len(s.buf) > 0 {
		s.l.line(string(s.buf))
		s.buf = nil
	}
}
