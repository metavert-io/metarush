// Package renderer gives every MetaRush binary a renderer of its own version
// (D75). The binary carries the renderer package (the Remotion project, the
// schema types it imports and the npm lockfile; see the root package
// metarush). Install unpacks it into <home>/renderer/<id>/, runs `npm ci` there
// with the Node.js MetaRush already uses, fetches Remotion's headless browser
// (shared by every renderer with the same Remotion version) and proves the
// result loads. Renders, stills and motion-graphic checks then run node from
// that folder only, so changing a source checkout, or merging into main, never
// changes what an installed binary renders.
//
// <id> is the MetaRush version plus the first 12 hex digits of the package's
// SHA-256, so two builds that carry different renderer files never share a
// folder, and a rebuild that did not touch them reuses the one already set up.
//
// A developer can point an installation at a live checkout instead
// (tools.videoDir plus tools.devRenderer, `metarush renderer dev <dir>`) for
// Remotion Studio work. A videoDir without the developer flag is an older
// installation's setting: it is ignored, with a notice.
package renderer

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	metarush "github.com/metavert-io/metarush"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/version"
)

// Sources say where renders run.
const (
	SourceBundled  = "bundled"  // this version's own renderer in <home>/renderer/<id>
	SourceCheckout = "checkout" // a developer's live checkout (tools.videoDir + tools.devRenderer)
)

// States of an installation's renderer.
const (
	StateReady       = "ready"
	StateMissing     = "missing"     // never set up for this version here; set up on first use
	StateInstalling  = "installing"  // a MetaRush process is setting it up now
	StateRemoved     = "removed"     // it was set up here and its folder is gone
	StateDamaged     = "damaged"     // its files changed or its packages are incomplete
	StateUnavailable = "unavailable" // Node.js is missing, or the developer checkout cannot render
	// StateLeftOut: a person left the renderer out at setup or removed it in
	// Settings → Components (D325): nothing sets it up by itself; adding the
	// component is the one step.
	StateLeftOut = "left-out"
)

// NeedsNode is Status.Needs when Node.js is what's missing.
const (
	NeedsNode = "node"
)

const (
	manifestName   = ".metarush-renderer.json"
	lockName       = ".lock"
	installingName = ".installing.json"
	browsersDir    = "browsers"
	// DefaultKeep is how many renderers prune keeps, this version's included.
	DefaultKeep = 3
)

// required files every renderer package must carry.
var required = []string{"package.json", "package-lock.json", "video/package.json", "video/render/render.mjs", "video/render/browser.mjs", "schema/templates.json"}

// Package is a renderer package: the files a binary carries.
type Package struct {
	fsys     fs.FS
	files    []string // slash paths, sorted
	digest   string   // hex SHA-256 over every file's name and bytes
	remotion string   // the Remotion version the lockfile pins
}

var (
	embedOnce sync.Once
	embedPkg  *Package
	embedErr  error
)

// Embedded is the renderer package compiled into this binary.
func Embedded() (*Package, error) {
	embedOnce.Do(func() { embedPkg, embedErr = Load(metarush.RendererFiles) })
	return embedPkg, embedErr
}

// Load reads a renderer package (tests load small fake ones).
func Load(fsys fs.FS) (*Package, error) {
	var files []string
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	for _, f := range required {
		if !slices.Contains(files, f) {
			return nil, fmt.Errorf("the renderer package has no %s", f)
		}
	}
	h := sha256.New()
	for _, f := range files {
		b, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		writeEntry(h, f, b)
	}
	lock, _ := fs.ReadFile(fsys, "package-lock.json")
	rv := lockedVersion(lock, "remotion")
	if rv == "" {
		return nil, errors.New("the renderer's lockfile does not pin remotion")
	}
	return &Package{fsys: fsys, files: files, digest: hex.EncodeToString(h.Sum(nil)), remotion: rv}, nil
}

func writeEntry(h hash.Hash, name string, b []byte) {
	fmt.Fprintf(h, "%s\x00%d\x00", name, len(b))
	h.Write(b)
}

// lockedVersion is the version package-lock.json installs for a top-level package.
func lockedVersion(lock []byte, name string) string {
	var l struct {
		Packages map[string]struct {
			Version string `json:"version"`
		} `json:"packages"`
	}
	if json.Unmarshal(lock, &l) != nil {
		return ""
	}
	return l.Packages["node_modules/"+name].Version
}

// Digest is the package's SHA-256 (hex).
func (p *Package) Digest() string { return p.digest }

// Remotion is the Remotion version the package's lockfile pins.
func (p *Package) Remotion() string { return p.remotion }

// Files lists the package's files (slash paths, sorted).
func (p *Package) Files() []string { return append([]string(nil), p.files...) }

// ID names this binary's renderer folder: <release>-<first 12 hex of the digest>.
// The release, not the exact build (D130): builds past a release that leave
// video/ alone (same digest) share one renderer instead of setting up a new one
// for every commit.
func (p *Package) ID() string { return Sanitize(version.Current().Release) + "-" + p.digest[:12] }

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Sanitize makes a version string safe as a folder name ("0.1.0+dirty" → "0.1.0-dirty").
func Sanitize(v string) string {
	s := strings.Trim(unsafeName.ReplaceAllString(v, "-"), "-.")
	if s == "" {
		return "unknown"
	}
	return s
}

// Extract writes the package's files under dir.
func (p *Package) Extract(dir string) error {
	for _, f := range p.files {
		b, err := fs.ReadFile(p.fsys, f)
		if err != nil {
			return err
		}
		dst := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// Component is the renderer's name among MetaRush's components (D324), and
// SizeWords what adding it downloads.
const (
	Component = "renderer"
	SizeWords = "about 400 MB"
)

// SetsItselfUp reports whether this version's renderer may be set up without
// a person asking (the daemon's first start, an AI app's renderer install):
// when a person wants it, or — nobody having chosen — when a renderer was
// set up here before (an upgrade). Left out, waiting for a person, or never
// set up: no (D325, D327).
func (e *Env) SetsItselfUp() bool {
	if ch, ok := choice.Of(e.Home, Component); ok {
		return ch.Wanted
	}
	return len(e.loadRecord().Installed) > 0
}

// Root is <home>/renderer, where every renderer of an installation lives.
func Root(home string) string { return filepath.Join(home, "renderer") }

// Manifest is written into a renderer folder when its setup succeeded.
type Manifest struct {
	ID          string    `json:"id"`
	Version     string    `json:"version"`
	Commit      string    `json:"commit,omitempty"`
	Digest      string    `json:"digest"`
	Remotion    string    `json:"remotion"`
	Node        string    `json:"node,omitempty"`
	Files       int       `json:"files"`
	InstalledAt time.Time `json:"installedAt"`
}

// Status is an installation's renderer, as doctor, `renderer status` and the
// worker see it.
type Status struct {
	Source string `json:"source" jsonschema:"enum=bundled,enum=checkout"`
	State  string `json:"state" jsonschema:"enum=ready,enum=missing,enum=installing,enum=removed,enum=damaged,enum=unavailable,enum=left-out"`
	// Needs names what this computer lacks for it ("node": Node.js), so
	// nothing branches on the words of Problem.
	Needs string `json:"needs,omitempty"`
	Ready bool   `json:"ready"`
	// ID is this binary's renderer (release + digest); Version is the MetaRush
	// version it belongs to and Remotion the Remotion version it renders with.
	ID       string `json:"id"`
	Version  string `json:"version"`
	Remotion string `json:"remotion,omitempty"`
	Digest   string `json:"digest,omitempty"`
	// Dir is the renderer folder (or the developer checkout); VideoDir is where
	// node runs render/render.mjs.
	Dir         string     `json:"dir"`
	VideoDir    string     `json:"videoDir"`
	Browser     bool       `json:"browser"` // Remotion's headless browser is downloaded
	InstalledAt *time.Time `json:"installedAt,omitempty"`
	// Problem and Fix say, in plain words, what is wrong and what to do.
	Problem    string    `json:"problem,omitempty"`
	Fix        string    `json:"fix,omitempty"`
	Installing *Progress `json:"installing,omitempty"`
	LastError  string    `json:"lastError,omitempty"` // the last failed setup of this version
	// Notice tells an installation that rendered from a checkout before that it
	// now uses this version's renderer (or that a developer checkout is in use).
	Notice   string      `json:"notice,omitempty"`
	Versions []Installed `json:"versions"` // every renderer on this machine, newest first
}

// Installed is one renderer folder on this machine.
type Installed struct {
	ID          string     `json:"id"`
	Version     string     `json:"version"`
	Remotion    string     `json:"remotion,omitempty"`
	InstalledAt *time.Time `json:"installedAt,omitempty"`
	Current     bool       `json:"current"` // this binary's renderer
	Bytes       int64      `json:"bytes,omitempty"`
}

// Progress is a setup in progress (written by the process doing it).
type Progress struct {
	PID       int       `json:"pid"`
	ID        string    `json:"id"`
	StartedAt time.Time `json:"startedAt"`
	Stage     string    `json:"stage,omitempty"`
	Message   string    `json:"message,omitempty"`
}

// Env is one installation's view of the renderer.
type Env struct {
	Home  string
	Cfg   config.Tools // videoDir, devRenderer, npm
	Tools *tools.Set   // node, which runs npm and the renderer's own scripts
	Pkg   *Package     // the renderer package this binary carries
	// Now is the clock (tests).
	Now func() time.Time
}

// New is the renderer of an installation, as this binary carries it.
func New(cfg *config.Config, ts *tools.Set) *Env {
	pkg, _ := Embedded()
	return &Env{Home: cfg.Home(), Cfg: cfg.Tools, Tools: ts, Pkg: pkg}
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Env) dir(id string) string { return filepath.Join(Root(e.Home), id) }

// Check reports the renderer this installation renders with. verify also
// re-reads the installed sources against the package's digest.
func (e *Env) Check(verify bool) Status {
	return e.check(verify, true)
}

func (e *Env) check(verify, probeLock bool) Status {
	st := Status{Source: SourceBundled, Version: version.Version, Versions: []Installed{}}
	if e.Pkg == nil {
		st.State, st.Problem = StateUnavailable, "This copy of MetaRush carries no usable renderer."
		st.Fix = "Reinstall MetaRush (a build from source must include video/ and schema/)."
		return st
	}
	st.ID, st.Remotion, st.Digest = e.Pkg.ID(), e.Pkg.remotion, e.Pkg.digest
	if e.Cfg.DevRenderer {
		return e.checkCheckout(st)
	}
	st.Notice = e.legacyNotice()
	st.Dir = e.dir(st.ID)
	st.VideoDir = filepath.Join(st.Dir, "video")
	st.Versions = e.versions(st.ID)
	rec := e.loadRecord()
	if le := rec.LastError; le != nil && le.ID == st.ID {
		st.LastError = le.Message
	}
	if e.Tools == nil || !e.Tools.Has(tools.Node) {
		st.State, st.Needs = StateUnavailable, NeedsNode
		st.Problem = "Rendering needs Node.js 22 or newer, which isn't on this computer yet."
		st.Fix = "Add Node.js in Settings → Components (about 50 MB), then the renderer."
		return st
	}
	if probeLock {
		if p := e.installing(); p != nil {
			st.State, st.Installing = StateInstalling, p
			st.Problem = "The renderer for this version is being set up."
			st.Fix = "Renders start when it is ready (usually a few minutes)."
			return st
		}
	}
	damaged := func(what string) Status {
		st.State = StateDamaged
		st.Problem = "The renderer for this version is damaged (" + what + ")."
		st.Fix = "Set the renderer up again."
		return st
	}
	m, err := readManifest(st.Dir)
	switch {
	case err != nil && !exists(st.Dir):
		_, was := rec.Installed[st.ID]
		switch {
		case choice.Declined(e.Home, Component):
			// Left out at setup, removed in Settings → Components (D325), or
			// waiting for a person after setup by an AI app or a script
			// (D327): nothing sets it up by itself.
			st.State = StateLeftOut
			st.Problem = "Rendering needs the Remotion renderer, which isn't added on this computer."
			st.Fix = "Add it in Settings → Components (" + SizeWords + ")."
		case was:
			st.State = StateRemoved
			st.Problem = "The renderer for this version was removed from this machine."
			st.Fix = "Set the renderer up again."
		case !e.SetsItselfUp():
			// Nobody chose it and none was ever set up here (D327): a person adds it.
			st.State = StateMissing
			st.Problem = "The renderer for this version isn't set up on this computer yet."
			st.Fix = "Add it in Settings → Components (" + SizeWords + ")."
		default:
			st.State = StateMissing
			st.Problem = "The renderer for this version isn't set up on this machine yet."
			st.Fix = "MetaRush sets it up by itself when it starts (it needs the internet), or you can set it up now."
		}
		if st.LastError != "" {
			st.Problem += " The last try failed." // what went wrong is in LastError
		}
		return st
	case err != nil:
		return damaged("its setup record is missing")
	case m.ID != st.ID || m.Digest != e.Pkg.digest:
		return damaged("its setup record belongs to another version")
	}
	at := m.InstalledAt
	st.InstalledAt = &at
	if problem := e.verifyPackages(st.Dir); problem != "" {
		return damaged(problem)
	}
	if verify {
		if problem := e.verifySources(st.Dir); problem != "" {
			return damaged(problem)
		}
	}
	st.Browser = browserPresent(st.VideoDir)
	st.State, st.Ready = StateReady, true
	return st
}

func (e *Env) checkCheckout(st Status) Status {
	st.Source = SourceCheckout
	dir := e.Cfg.VideoDir
	st.Dir, st.VideoDir = dir, dir
	st.Notice = "Renders use the developer checkout at " + tildePath(dir) + " instead of this version's own renderer (tools.devRenderer). Run `metarush renderer dev --off` to go back."
	switch {
	case dir == "":
		st.State, st.Problem = StateUnavailable, "The developer renderer setting is on, but it does not say which checkout to use."
		st.Fix = "Run `metarush renderer dev <path to the checkout's video folder>`, or `metarush renderer dev --off`."
		st.Notice = ""
		return st
	case e.Tools == nil || !e.Tools.Has(tools.Node):
		st.State, st.Needs, st.Problem = StateUnavailable, NeedsNode, "Node.js is not installed, so this machine cannot render."
		st.Fix = "Install Node.js 22 or newer (for example `brew install node`)."
		return st
	case !exists(filepath.Join(dir, "render", "render.mjs")):
		st.State, st.Problem = StateUnavailable, "The developer checkout at "+tildePath(dir)+" has no renderer (render/render.mjs)."
		st.Fix = "Point `metarush renderer dev` at a checkout's video folder, or run `metarush renderer dev --off`."
		return st
	case !exists(filepath.Join(dir, "node_modules")) && !exists(filepath.Join(dir, "..", "node_modules", "@remotion", "renderer")):
		st.State, st.Problem = StateUnavailable, "The developer checkout's packages are not installed."
		st.Fix = "Run `npm ci` in " + tildePath(filepath.Dir(dir)) + "."
		return st
	}
	for _, p := range []string{filepath.Join(dir, "node_modules", "remotion", "package.json"), filepath.Join(dir, "..", "node_modules", "remotion", "package.json")} {
		if v := packageVersion(p); v != "" {
			st.Remotion = v
			break
		}
	}
	st.Browser = browserPresent(dir)
	st.State, st.Ready = StateReady, true
	return st
}

// verifyPackages checks that npm installed what renders need.
func (e *Env) verifyPackages(dir string) string {
	if v := packageVersion(filepath.Join(dir, "node_modules", "remotion", "package.json")); v != e.Pkg.remotion {
		if v == "" {
			return "Remotion is not installed in it"
		}
		return "it has Remotion " + v + ", not " + e.Pkg.remotion
	}
	for _, p := range []string{"node_modules/@remotion/renderer/package.json", "node_modules/@remotion/bundler/package.json", "node_modules/@metarush/schema/package.json", "video/render/render.mjs"} {
		if !exists(filepath.Join(dir, filepath.FromSlash(p))) {
			return strings.TrimSuffix(strings.TrimPrefix(p, "node_modules/"), "/package.json") + " is missing"
		}
	}
	return ""
}

// verifySources re-reads the installed copy of every package file.
func (e *Env) verifySources(dir string) string {
	h := sha256.New()
	for _, f := range e.Pkg.files {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			return f + " is missing"
		}
		want, _ := fs.ReadFile(e.Pkg.fsys, f)
		if string(b) != string(want) {
			return f + " was changed"
		}
		writeEntry(h, f, b)
	}
	if hex.EncodeToString(h.Sum(nil)) != e.Pkg.digest {
		return "its files do not match this version"
	}
	return ""
}

// Usable reports whether renders can run here now or once a setup in
// progress finishes (the worker advertises rendering only then).
func (e *Env) Usable() bool {
	st := e.Check(false)
	return st.Ready || st.State == StateInstalling
}

// NotReady is the error renders get when the renderer cannot run: its text is
// the plain problem and what to do.
type NotReady struct{ Status Status }

// Error is the technical record (a job's error): the plain words and the
// command that fixes it.
func (n *NotReady) Error() string {
	s := n.Plain()
	if cmd := n.Status.Command(); cmd != "" {
		s += " (" + cmd + ")"
	}
	return s
}

// Plain is what went wrong and what to do, in plain words only.
func (n *NotReady) Plain() string { return strings.TrimSpace(n.Status.Problem + " " + n.Status.Fix) }

// AppError gives the stable error code (skills branch on it); the command
// that fixes it is in Details (--details, the dashboard's Details).
func (n *NotReady) AppError() *apperr.Error {
	code := apperr.NotConfigured
	if n.Status.Needs == NeedsNode || n.Status.State == StateLeftOut {
		code = apperr.ToolMissing
	}
	e := apperr.New(code, "%s", n.Status.Problem).WithHint(n.Status.Fix).WithDetails(map[string]any{"reason": "renderer_" + n.Status.State, "state": n.Status.State})
	if cmd := n.Status.Command(); cmd != "" {
		e = e.WithCommand(cmd)
	}
	return e
}

// Command is the command that fixes the renderer's problem, for people at a
// terminal and for Claude Code ("" when there is nothing to run). Problem and
// Fix say it in plain words.
func (s Status) Command() string {
	if s.Source != SourceBundled {
		return ""
	}
	switch s.State {
	case StateLeftOut:
		return "metarush components add renderer"
	case StateMissing, StateRemoved, StateDamaged:
		return "metarush renderer install"
	case StateUnavailable:
		if s.Needs == NeedsNode {
			return "metarush components add renderer"
		}
	}
	return ""
}

// Ready returns the folder renders run node in, or a *NotReady error.
func (e *Env) Ready(verify bool) (string, Status, error) {
	st := e.Check(verify)
	if !st.Ready {
		return "", st, &NotReady{Status: st}
	}
	return st.VideoDir, st, nil
}

// Use marks a renderer in use while a render runs (prune skips it). The
// release function must be called when the render ends.
func Use(st Status) (func(), error) {
	if st.Source != SourceBundled || st.Dir == "" {
		return func() {}, nil
	}
	l, err := lockShared(filepath.Join(st.Dir, manifestName))
	if err != nil {
		return nil, err
	}
	return l.release, nil
}

// legacyNotice explains the move from a checkout to the per-version renderer.
func (e *Env) legacyNotice() string {
	if e.Cfg.DevRenderer || e.Cfg.VideoDir == "" {
		return ""
	}
	d := tildePath(e.Cfg.VideoDir)
	return "Renders now use the renderer that comes with this version of MetaRush, not the video folder this installation used before (" + d +
		"), so changes there no longer change your renders. To render from that folder again for template work, run `metarush renderer dev " + d + "`."
}

// NoticeOnce returns the move notice the first time it applies to this
// installation (and records that it was shown), else "".
func (e *Env) NoticeOnce() string {
	n := e.legacyNotice()
	if n == "" {
		return ""
	}
	rec := e.loadRecord()
	if rec.NoticeAt != nil && rec.LegacyVideoDir == e.Cfg.VideoDir {
		return ""
	}
	now := e.now().UTC()
	rec.NoticeAt, rec.LegacyVideoDir = &now, e.Cfg.VideoDir
	e.saveRecord(rec)
	return n
}

// versions lists the renderer folders on this machine, newest first.
func (e *Env) versions(current string) []Installed {
	out := []Installed{}
	entries, err := os.ReadDir(Root(e.Home))
	if err != nil {
		return out
	}
	for _, d := range entries {
		if !d.IsDir() || strings.HasPrefix(d.Name(), ".") || d.Name() == browsersDir {
			continue
		}
		in := Installed{ID: d.Name(), Current: d.Name() == current}
		if m, err := readManifest(filepath.Join(Root(e.Home), d.Name())); err == nil {
			at := m.InstalledAt
			in.Version, in.Remotion, in.InstalledAt = m.Version, m.Remotion, &at
		}
		out = append(out, in)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].InstalledAt, out[j].InstalledAt
		switch {
		case a == nil:
			return false
		case b == nil:
			return true
		}
		return a.After(*b)
	})
	return out
}

/* ---------- the installation's record (<home>/state/renderer.json) ---------- */

type record struct {
	// Installed are the renderers set up here (prune forgets the ones it removes).
	Installed      map[string]time.Time `json:"installed,omitempty"`
	LastError      *lastError           `json:"lastError,omitempty"`
	LegacyVideoDir string               `json:"legacyVideoDir,omitempty"`
	NoticeAt       *time.Time           `json:"noticeAt,omitempty"`
}

type lastError struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

func (e *Env) recordPath() string { return filepath.Join(e.Home, "state", "renderer.json") }

func (e *Env) loadRecord() record {
	var r record
	if b, err := os.ReadFile(e.recordPath()); err == nil {
		_ = json.Unmarshal(b, &r)
	}
	if r.Installed == nil {
		r.Installed = map[string]time.Time{}
	}
	return r
}

func (e *Env) saveRecord(r record) {
	b, _ := json.MarshalIndent(r, "", "  ")
	_ = writeAtomic(e.recordPath(), b, 0o600)
}

/* ---------- small helpers ---------- */

func readManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
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

// browserPresent reports whether Remotion's Chrome Headless Shell is downloaded
// for a video package (renders download it on first use otherwise).
func browserPresent(videoDir string) bool {
	entries, err := os.ReadDir(filepath.Join(videoDir, "node_modules", ".remotion", "chrome-headless-shell"))
	return err == nil && len(entries) > 0
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func writeAtomic(p string, b []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", p, os.Getpid())
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// tildePath shortens the user's home folder to ~ for messages.
func tildePath(p string) string {
	if u, err := os.UserHomeDir(); err == nil && u != "" && (p == u || strings.HasPrefix(p, u+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(p, u)
	}
	return p
}
