package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Watch states.
const (
	WatchWatching      = "watching"
	WatchOff           = "off"
	WatchMissing       = "missing"
	WatchNotConfigured = "not_configured"
	WatchNotRunning    = "not_running" // configured and on, but no daemon is running it
)

// WatchStatusIn has no fields.
type WatchStatusIn struct{}

// WatchStatusOut describes the watched folder: configuration, live state and a suggestion.
type WatchStatusOut struct {
	State            string       `json:"state" jsonschema:"enum=watching,enum=off,enum=missing,enum=not_configured,enum=not_running"`
	Enabled          bool         `json:"enabled"`
	Dir              string       `json:"dir,omitempty"`
	DisplayDir       string       `json:"displayDir,omitempty" jsonschema:"description=The folder with the home directory shortened to ~."`
	Exists           bool         `json:"exists"`
	ProjectID        string       `json:"projectId,omitempty"`
	ProjectName      string       `json:"projectName,omitempty"`
	MoveProcessed    bool         `json:"moveProcessed"`
	StableSeconds    int          `json:"stableSeconds"`
	Live             *WatchStatus `json:"live,omitempty"` // the running watcher's status (scan time, waiting files, last ingest, errors)
	DaemonRunning    bool         `json:"daemonRunning"`
	Suggested        string       `json:"suggested"`
	SuggestedDisplay string       `json:"suggestedDisplay"`
	ConfigPath       string       `json:"configPath"`
	Skipped          int          `json:"skipped" jsonschema:"description=Files that were already in the folder and are being left alone (Import adds them)."`
	CanChoose        bool         `json:"canChoose" jsonschema:"description=This machine has a native folder picker (macOS)."`
	CanReveal        bool         `json:"canReveal" jsonschema:"description=The folder can be opened in Finder or the file manager."`
	Note             string       `json:"note,omitempty"`
}

// WatchImportIn has no fields.
type WatchImportIn struct{}

// WatchChooseIn opens the native folder picker on this machine.
type WatchChooseIn struct {
	Start string `json:"start,omitempty" jsonschema:"description=Folder the picker starts in (default: the watched folder or the suggestion)."`
}

// WatchChooseOut is the picked folder.
type WatchChooseOut struct {
	Path     string `json:"path,omitempty"`
	Display  string `json:"display,omitempty"`
	Canceled bool   `json:"canceled"`
	Blocked  string `json:"blocked,omitempty" jsonschema:"description=Why the chosen folder cannot be watched."`
	Files    int    `json:"files" jsonschema:"description=Files already in the folder."`
}

// WatchRevealIn opens a folder in Finder.
type WatchRevealIn struct {
	Dir string `json:"dir,omitempty" jsonschema:"description=Default: the watched folder."`
}

// WatchRevealOut confirms.
type WatchRevealOut struct {
	Opened string `json:"opened"`
}

// ChooseFolder shows the native folder picker on this machine (macOS).
func ChooseFolder(ctx context.Context, a *app.App, in *WatchChooseIn) (*WatchChooseOut, error) {
	start := expandHome(in.Start)
	if start == "" {
		start = currentWatch(a).Dir
	}
	if start == "" || !isDir(start) {
		start = SuggestedWatchDir()
		if !isDir(start) {
			start = filepath.Dir(start)
		}
	}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	p, err := a.Tools.ChooseFolder(pctx, "Choose the folder MetaRush should watch", start)
	if errors.Is(err, tools.ErrUnsupported) {
		return nil, apperr.New(apperr.NotConfigured, "This computer can't open a folder picker.").WithHint("Type where the folder is, or choose it in Settings > Watched folder.").WithDetails(map[string]any{"reason": "unsupported"})
	}
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "The folder picker didn't open.")
	}
	if p == "" {
		return &WatchChooseOut{Canceled: true}, nil
	}
	out := &WatchChooseOut{Path: p, Display: DisplayPath(p), Blocked: blockedReason(a, p)}
	files, _ := Collect([]string{p}, true)
	out.Files = len(files)
	return out, nil
}

// RevealFolder opens a folder in Finder (macOS) or the file manager (Linux).
func RevealFolder(ctx context.Context, a *app.App, in *WatchRevealIn) (*WatchRevealOut, error) {
	dir := expandHome(in.Dir)
	if dir == "" {
		dir = currentWatch(a).Dir
	}
	if dir == "" || !filepath.IsAbs(dir) || !isDir(dir) {
		return nil, apperr.New(apperr.InvalidInput, "There's no folder to open.")
	}
	if err := a.Tools.RevealFolder(ctx, filepath.Clean(dir)); err != nil {
		if errors.Is(err, tools.ErrUnsupported) {
			return nil, apperr.New(apperr.NotConfigured, "This computer can't open folders for you.").WithDetails(map[string]any{"reason": "unsupported"})
		}
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't open the folder.")
	}
	return &WatchRevealOut{Opened: DisplayPath(dir)}, nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// WatchSetIn configures the watched folder.
type WatchSetIn struct {
	Dir           string  `json:"dir" jsonschema:"minLength=1,description=Folder to watch (absolute or ~/…)."`
	ProjectID     *string `json:"projectId,omitempty" jsonschema:"description=Project for ingested files; empty string = the inbox."`
	MoveProcessed *bool   `json:"moveProcessed,omitempty" jsonschema:"description=Move ingested files into an Ingested subfolder (never deleted)."`
	StableSeconds *int    `json:"stableSeconds,omitempty" jsonschema:"minimum=2,maximum=600,description=How long a file must stop changing before it is ingested."`
	Create        bool    `json:"create,omitempty" jsonschema:"description=Create the folder if it does not exist."`
	Enabled       *bool   `json:"enabled,omitempty" jsonschema:"description=Default true."`
	Existing      string  `json:"existing,omitempty" jsonschema:"enum=import,enum=skip,description=Files already in the folder: import them (default) or leave them alone and only take new ones."`
}

// WatchDisableIn has no fields.
type WatchDisableIn struct{}

// BrowseIn lists folders under the home directory.
type BrowseIn struct {
	Path   string `json:"path,omitempty" jsonschema:"description=Folder to list (absolute or ~/…); default the home folder."`
	Hidden bool   `json:"hidden,omitempty" jsonschema:"description=Include hidden folders."`
}

// BrowseEntry is one folder.
type BrowseEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Display string `json:"display"`
	Hidden  bool   `json:"hidden,omitempty"`
	// Blocked explains why the folder cannot be watched (it holds MetaRush's own files).
	Blocked string `json:"blocked,omitempty"`
}

// BrowseOut is a folder listing.
type BrowseOut struct {
	Path      string        `json:"path"`
	Display   string        `json:"display"`
	Parent    string        `json:"parent,omitempty" jsonschema:"description=Absent at the home folder (browsing never leaves it)."`
	Home      string        `json:"home"`
	Entries   []BrowseEntry `json:"entries"`
	Truncated bool          `json:"truncated,omitempty"`
	Blocked   string        `json:"blocked,omitempty" jsonschema:"description=Why this folder itself cannot be watched."`
	Suggested string        `json:"suggested"`
}

// MkdirIn creates a folder under the home directory (for the folder picker).
type MkdirIn struct {
	Parent string `json:"parent" jsonschema:"description=Existing folder under the home folder."`
	Name   string `json:"name" jsonschema:"minLength=1,maxLength=255"`
}

func userHome() string {
	h, _ := os.UserHomeDir()
	return filepath.Clean(h)
}

// DisplayPath shortens the home directory to ~.
func DisplayPath(p string) string {
	h := userHome()
	switch {
	case p == "":
		return ""
	case p == h:
		return "~"
	case strings.HasPrefix(p, h+string(filepath.Separator)):
		return "~" + p[len(h):]
	}
	return p
}

// SuggestedWatchDir is the default folder offered when none is configured.
func SuggestedWatchDir() string {
	h := userHome()
	for _, parent := range []string{"Movies", "Videos"} {
		if runtime.GOOS != "darwin" && parent == "Movies" {
			continue
		}
		if st, err := os.Stat(filepath.Join(h, parent)); err == nil && st.IsDir() {
			return filepath.Join(h, parent, "MetaRush Inbox")
		}
	}
	return filepath.Join(h, "MetaRush Inbox")
}

func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" {
		return userHome()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(userHome(), p[2:])
	}
	return p
}

// within reports whether p is root or inside it.
func within(p, root string) bool {
	if root == "" {
		return false
	}
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// ownDirs are folders MetaRush writes to; watching them (or a folder that
// contains them) would ingest MetaRush's own files.
func ownDirs(a *app.App) map[string]string {
	out := map[string]string{
		filepath.Clean(a.Cfg.Home()):      "MetaRush's own folder",
		filepath.Clean(a.Cfg.Cache.Dir):   "the folder where MetaRush keeps this machine's copies",
		filepath.Clean(a.Cfg.Scratch.Dir): "MetaRush's working folder",
	}
	if a.Cfg.Media.Provider == "" || a.Cfg.Media.Provider == "filesystem" {
		out[filepath.Clean(a.Cfg.Media.Filesystem.Root)] = "MetaRush's storage"
	}
	return out
}

// blockedReason explains why dir cannot be watched ("" when it can).
func blockedReason(a *app.App, dir string) string {
	home := userHome()
	switch {
	case dir == "/" || dir == filepath.VolumeName(dir)+string(filepath.Separator):
		return "Watching the whole disk is not allowed. Choose a folder just for new videos."
	case dir == home || realPath(dir) == realPath(home):
		return "Watching your whole home folder would add everything in it. Choose a folder just for new videos, for example " + DisplayPath(SuggestedWatchDir()) + "."
	}
	// Being inside one of MetaRush's folders is checked before containing one (the
	// installation folder contains the cache), in a fixed order, so the same folder
	// always gets the same explanation.
	own := ownDirs(a)
	dirs := make([]string, 0, len(own))
	for d := range own {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	rdir := realPath(dir)
	for _, d := range dirs {
		if within(rdir, realPath(d)) {
			return "This folder is inside " + own[d] + ". Choose a folder outside MetaRush's own files."
		}
	}
	for _, d := range dirs {
		if within(realPath(d), rdir) {
			return "This folder contains " + own[d] + ", so MetaRush would add its own files. Choose a more specific folder."
		}
	}
	return ""
}

// ValidateWatchDir checks a folder for watching; homeOnly restricts it to the
// home folder (the dashboard's folder picker).
func ValidateWatchDir(a *app.App, dir string, create, homeOnly bool) (string, error) {
	dir = expandHome(dir)
	if dir == "" || !filepath.IsAbs(dir) {
		return "", apperr.New(apperr.InvalidInput, "Give the folder's full location, from the top of the disk or your home folder.")
	}
	dir = filepath.Clean(dir)
	if homeOnly && !within(realPath(dir), realPath(userHome())) {
		return "", apperr.New(apperr.InvalidInput, "Choose a folder inside your home folder.").WithDetails(map[string]any{"reason": "outside_home"})
	}
	if r := blockedReason(a, dir); r != "" {
		return "", apperr.New(apperr.InvalidInput, "%s", r).WithDetails(map[string]any{"reason": "blocked"})
	}
	st, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist) && create:
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't make the folder %s.", DisplayPath(dir))
		}
	case errors.Is(err, os.ErrNotExist):
		return "", apperr.New(apperr.InvalidInput, "%s doesn't exist.", DisplayPath(dir)).WithHint("Choose an existing folder, or let MetaRush create it.").
			WithDetails(map[string]any{"reason": "missing"}).WithCommand(fmt.Sprintf("metarush watch set %q --create", DisplayPath(dir)))
	case err != nil:
		return "", apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't read %s.", DisplayPath(dir))
	case !st.IsDir():
		return "", apperr.New(apperr.InvalidInput, "%s is a file, not a folder.", DisplayPath(dir))
	}
	if _, err := os.ReadDir(dir); err != nil {
		return "", apperr.Wrap(apperr.InvalidInput, err, "MetaRush isn't allowed to look inside %s.", DisplayPath(dir)).
			WithHint("Check the folder's permissions.").WithDetails(map[string]any{"reason": "unreadable"})
	}
	return dir, nil
}

// remote reports a dashboard caller (folder choices are limited to the home folder).
func remote(a *app.App) bool { return a.Actor.Via == "dashboard" }

func currentWatch(a *app.App) config.Watch {
	if Live != nil {
		return Live.Config()
	}
	return a.Cfg.Worker.Watch
}

// readStatusFile reads the status a running watcher (in a daemon) publishes.
func readStatusFile(a *app.App) (*WatchStatus, bool) {
	p := StatusFile(a.Cfg.StateDir())
	st, err := os.Stat(p)
	if err != nil {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var s WatchStatus
	if json.Unmarshal(b, &s) != nil {
		return nil, false
	}
	return &s, time.Since(st.ModTime()) < 20*time.Second && s.Enabled
}

func watchStatus(ctx context.Context, a *app.App) *WatchStatusOut {
	w := currentWatch(a)
	sug := SuggestedWatchDir()
	out := &WatchStatusOut{Enabled: w.Enabled, Dir: w.Dir, DisplayDir: DisplayPath(w.Dir), ProjectID: w.ProjectID, MoveProcessed: w.MoveProcessed,
		StableSeconds: w.StableSeconds, Suggested: sug, SuggestedDisplay: DisplayPath(sug), ConfigPath: config.Path(a.Cfg.Home()),
		CanChoose: runtime.GOOS == "darwin" && a.Tools.Has(tools.OSAScript), CanReveal: (runtime.GOOS == "darwin" && a.Tools.Has(tools.Open)) || (runtime.GOOS == "linux" && a.Tools.Has(tools.XDGOpen))}
	if w.Dir != "" {
		out.Skipped = CountSkipped(a.Cfg.StateDir(), w.Dir)
	}
	if out.StableSeconds <= 0 {
		out.StableSeconds = 6
	}
	if w.ProjectID != "" {
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), w.ProjectID, &p) == nil {
			out.ProjectName = p.Name
		}
	}
	if w.Dir != "" {
		if st, err := os.Stat(w.Dir); err == nil && st.IsDir() {
			out.Exists = true
		}
	}
	if Live != nil {
		out.DaemonRunning = true
		if s, ok := Live.Snapshot(); ok {
			out.Live = &s
		}
	} else if s, fresh := readStatusFile(a); s != nil && fresh {
		out.DaemonRunning, out.Live = true, s
	}
	switch {
	case w.Dir == "":
		out.State = WatchNotConfigured
		out.Note = "No watched folder yet. Videos you drop into one are added by themselves once they finish copying."
	case !w.Enabled:
		out.State = WatchOff
	case !out.Exists:
		out.State = WatchMissing
		out.Note = "The folder cannot be found. If it is on an external drive, plug it in and watching carries on."
	case out.Live != nil:
		out.State = WatchWatching
	default:
		out.State = WatchNotRunning
		out.Note = "Set up, but MetaRush is not running on this machine, so nothing is being added. Open MetaRush to start watching."
	}
	return out
}

func saveWatch(ctx context.Context, a *app.App, w config.Watch) error {
	if err := config.SetField(a.Cfg.Home(), []string{"worker", "watch"}, w); err != nil {
		return apperr.Wrap(apperr.Internal, err, "MetaRush couldn't save this machine's settings.").WithDetails(map[string]any{"file": "config.yaml"})
	}
	if Live != nil {
		Live.Apply(w) // hot reload in this daemon; other processes reload from the file
	} else {
		a.Cfg.Worker.Watch = w
	}
	return nil
}

func init() {
	ops.Def("watch.status", "Show the watched folder: configuration, state (watching, off, missing), last scan, files waiting to settle, last ingest and errors", ops.Flags{}, func(ctx context.Context, a *app.App, _ *WatchStatusIn) (*WatchStatusOut, error) {
		return watchStatus(ctx, a), nil
	})
	ops.Def("watch.set", "Configure and enable the watched folder (writes only worker.watch in config.yaml; the running daemon picks it up without a restart); owners", ops.Flags{Mutating: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, in *WatchSetIn) (*WatchStatusOut, error) {
		dir, err := ValidateWatchDir(a, in.Dir, in.Create, remote(a))
		if err != nil {
			return nil, err
		}
		w := currentWatch(a)
		w.Dir, w.Enabled = dir, in.Enabled == nil || *in.Enabled
		if in.ProjectID != nil {
			pid := strings.TrimSpace(*in.ProjectID)
			if pid != "" {
				var p models.Project
				if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), pid, &p); err != nil {
					return nil, apperr.New(apperr.NotFound, "That project was not found. It may have been deleted.").WithDetails(map[string]any{"project": pid})
				}
			}
			w.ProjectID = pid
		}
		if in.MoveProcessed != nil {
			w.MoveProcessed = *in.MoveProcessed
		}
		if in.StableSeconds != nil {
			if *in.StableSeconds < 2 || *in.StableSeconds > 600 {
				return nil, apperr.New(apperr.InvalidInput, "A file can wait between 2 and 600 seconds to stop changing (not %d).", *in.StableSeconds)
			}
			w.StableSeconds = *in.StableSeconds
		}
		if w.StableSeconds <= 0 {
			w.StableSeconds = 6
		}
		switch in.Existing {
		case "", "import":
			ClearSkipped(a.Cfg.StateDir())
		case "skip":
			if _, err := MarkExistingSkipped(a.Cfg.StateDir(), dir); err != nil {
				return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't note the files that are already there.")
			}
		default:
			return nil, apperr.New(apperr.InvalidInput, "For the files already in the folder, choose import or skip.")
		}
		if err := saveWatch(ctx, a, w); err != nil {
			return nil, err
		}
		if Live != nil {
			Live.Restart() // picks up the skip marks even when the folder did not change
		}
		a.Audit(ctx, "watch.set", "installation", a.Cfg.Installation.ID, w.ProjectID, map[string]any{"dir": w.Dir, "enabled": w.Enabled, "moveProcessed": w.MoveProcessed, "stableSeconds": w.StableSeconds})
		out := watchStatus(ctx, a)
		if Live == nil {
			out.Note = "Saved. MetaRush on this machine starts watching within a few seconds."
		}
		return out, nil
	})
	ops.Def("watch.disable", "Stop watching the folder (the folder setting is kept); owners", ops.Flags{Mutating: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, _ *WatchDisableIn) (*WatchStatusOut, error) {
		w := currentWatch(a)
		w.Enabled = false
		if err := saveWatch(ctx, a, w); err != nil {
			return nil, err
		}
		a.Audit(ctx, "watch.disable", "installation", a.Cfg.Installation.ID, "", map[string]any{"dir": w.Dir})
		return watchStatus(ctx, a), nil
	})
	ops.Def("watch.import", "Also add the files that were already in the watched folder when it was set to take only new files", ops.Flags{Mutating: true}, func(ctx context.Context, a *app.App, _ *WatchImportIn) (*WatchStatusOut, error) {
		w := currentWatch(a)
		if w.Dir == "" {
			return nil, apperr.New(apperr.InvalidInput, "There's no watched folder yet.").WithHint("Choose one in Settings > Watched folder.")
		}
		n := ClearSkipped(a.Cfg.StateDir())
		if Live != nil {
			Live.Restart()
		}
		a.Audit(ctx, "watch.import", "installation", a.Cfg.Installation.ID, w.ProjectID, map[string]any{"dir": w.Dir, "files": n})
		out := watchStatus(ctx, a)
		out.Note = fmt.Sprintf("%d files that were already there will be added.", n)
		if n == 1 {
			out.Note = "1 file that was already there will be added."
		}
		if Live == nil {
			out.Note += " MetaRush on this machine picks them up within a few seconds."
		}
		return out, nil
	})
	ops.Def("watch.choose", "Open the native folder picker on this machine (macOS) and return the chosen folder", ops.Flags{LocalOnly: true}, ChooseFolder)
	ops.Def("watch.reveal", "Open the watched folder (or another folder) in Finder", ops.Flags{LocalOnly: true}, RevealFolder)
	ops.Def("watch.browse", "List folders under your home folder (the watched-folder picker; never leaves the home folder)", ops.Flags{OwnerOnly: true}, func(ctx context.Context, a *app.App, in *BrowseIn) (*BrowseOut, error) {
		home := realPath(userHome())
		p := expandHome(in.Path)
		if p == "" {
			p = home
		}
		if !filepath.IsAbs(p) {
			return nil, apperr.New(apperr.InvalidInput, "Give the folder's full location, from the top of the disk or your home folder.")
		}
		p = realPath(filepath.Clean(p))
		if !within(p, home) {
			return nil, apperr.New(apperr.InvalidInput, "Only folders inside your home folder can be browsed here.").WithDetails(map[string]any{"reason": "outside_home"})
		}
		ents, err := os.ReadDir(p)
		if err != nil {
			return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't look inside %s.", DisplayPath(p))
		}
		out := &BrowseOut{Path: p, Display: DisplayPath(p), Home: home, Entries: []BrowseEntry{}, Blocked: blockedReason(a, p), Suggested: SuggestedWatchDir()}
		if p != home {
			out.Parent = filepath.Dir(p)
		}
		for _, e := range ents {
			name := e.Name()
			full := filepath.Join(p, name)
			isDir := e.IsDir()
			if e.Type()&os.ModeSymlink != 0 {
				if st, err := os.Stat(full); err == nil && st.IsDir() && within(realPath(full), home) {
					isDir = true
				}
			}
			hidden := strings.HasPrefix(name, ".")
			if !isDir || (hidden && !in.Hidden) {
				continue
			}
			out.Entries = append(out.Entries, BrowseEntry{Name: name, Path: full, Display: DisplayPath(full), Hidden: hidden, Blocked: blockedReason(a, full)})
		}
		sort.Slice(out.Entries, func(i, j int) bool {
			return strings.ToLower(out.Entries[i].Name) < strings.ToLower(out.Entries[j].Name)
		})
		if len(out.Entries) > 500 {
			out.Entries, out.Truncated = out.Entries[:500], true
		}
		return out, nil
	})
	ops.Def("watch.mkdir", "Create a folder under your home folder (for the watched-folder picker); owners", ops.Flags{Mutating: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, in *MkdirIn) (*BrowseEntry, error) {
		name := strings.TrimSpace(in.Name)
		if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.ContainsRune(name, 0) {
			return nil, apperr.New(apperr.InvalidInput, "A folder's name can't contain slashes.")
		}
		home := realPath(userHome())
		parent := realPath(filepath.Clean(expandHome(in.Parent)))
		if !filepath.IsAbs(parent) || !within(parent, home) {
			return nil, apperr.New(apperr.InvalidInput, "Folders can only be made inside your home folder.").WithDetails(map[string]any{"reason": "outside_home"})
		}
		full := filepath.Join(parent, name)
		if err := os.Mkdir(full, 0o755); err != nil {
			if errors.Is(err, os.ErrExist) {
				return nil, apperr.New(apperr.Conflict, "%s already exists.", DisplayPath(full))
			}
			return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't make the folder %s.", DisplayPath(full))
		}
		a.Audit(ctx, "watch.mkdir", "installation", a.Cfg.Installation.ID, "", map[string]any{"dir": full})
		return &BrowseEntry{Name: name, Path: full, Display: DisplayPath(full), Blocked: blockedReason(a, full)}, nil
	})
}
