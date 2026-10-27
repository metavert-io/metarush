// Package secretpath decides whether a file may hold secrets. People keep
// MetaRush homes, projects and footage on shared drives and in git
// repositories, so every file-based secret (an env-mode <home>/.env, join
// bundles, CI dotenv files) is checked before it is written or used:
//
//   - never inside a cloud-sync folder (iCloud Drive, Google Drive, OneDrive,
//     Dropbox, Box): those upload the file the moment it is written;
//   - never inside a git work tree where git would commit it; a path git
//     ignores may be used only behind an explicit escape hatch
//     (--allow-unsafe-secret-path) when MetaRush writes it.
//
// The OS keychain is always the default and needs none of this. The package
// runs only `git` with fixed arguments (never a shell) to ask whether a path
// is ignored, and has no dependencies on the rest of MetaRush.
package secretpath

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
)

// Location kinds.
const (
	KindSafe = ""
	KindGit  = "git"
	KindSync = "sync"
)

// Verdict describes where a path lives.
type Verdict struct {
	Path    string `json:"path"`
	Kind    string `json:"kind,omitempty"`    // "" (safe) | git | sync
	Root    string `json:"root,omitempty"`    // the git work tree or the sync folder
	Service string `json:"service,omitempty"` // iCloud Drive | Google Drive | OneDrive | Dropbox | Box | git
	// Ignored reports (for KindGit) that git ignores the path, so it would not
	// be committed. Tracked files are never ignored, whatever .gitignore says.
	Ignored bool `json:"ignored,omitempty"`
	// GitChecked reports that git itself answered the ignore question (false when
	// git is not installed: the path is then treated as not ignored).
	GitChecked bool `json:"gitChecked,omitempty"`
}

// Unsafe reports whether the verdict is anything but a plain local folder.
func (v Verdict) Unsafe() bool { return v.Kind != KindSafe }

// Describe is a one-line human description of the location.
func (v Verdict) Describe() string {
	switch v.Kind {
	case KindSync:
		return fmt.Sprintf("inside %s (%s), which uploads its contents to the cloud", v.Service, tilde(v.Root))
	case KindGit:
		if v.Ignored {
			return fmt.Sprintf("inside the git repository %s (git ignores it, so it isn't committed today)", tilde(v.Root))
		}
		if !v.GitChecked {
			return fmt.Sprintf("inside the git repository %s (git isn't installed, so MetaRush can't confirm that git ignores it)", tilde(v.Root))
		}
		return fmt.Sprintf("inside the git repository %s, and git doesn't ignore it, so it could be committed", tilde(v.Root))
	}
	return "a local folder outside git repositories and sync folders"
}

// Inspector inspects paths. The zero value uses the real home directory, git
// from PATH and the running OS; tests set the fields.
type Inspector struct {
	Home string // the user's home directory (default os.UserHomeDir)
	Git  string // git binary; "" looks it up on PATH, "-" means not installed
	GOOS string // default runtime.GOOS
}

var defaultInspector Inspector

// Inspect classifies path with the default inspector.
func Inspect(path string) Verdict { return defaultInspector.Inspect(path) }

// Inspect classifies path (which need not exist yet).
func (in Inspector) Inspect(path string) Verdict {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.Clean(abs)
	v := Verdict{Path: abs}
	if service, root := in.syncFolder(abs); service != "" {
		v.Kind, v.Service, v.Root = KindSync, service, root
		return v
	}
	// A symlink can point a harmless-looking path into a sync folder.
	if real := resolveExisting(abs); real != abs {
		if service, root := in.syncFolder(real); service != "" {
			v.Kind, v.Service, v.Root = KindSync, service, root
			return v
		}
	}
	if root := in.gitRoot(abs); root != "" {
		v.Kind, v.Service, v.Root = KindGit, "git", root
		v.Ignored, v.GitChecked = in.gitIgnored(root, abs)
	}
	return v
}

func (in Inspector) home() string {
	if in.Home != "" {
		return filepath.Clean(in.Home)
	}
	h, _ := os.UserHomeDir()
	return h
}

func (in Inspector) goos() string {
	if in.GOOS != "" {
		return in.GOOS
	}
	return runtime.GOOS
}

// syncFolder returns the sync service and its root folder when p is inside one.
func (in Inspector) syncFolder(p string) (service, root string) {
	home := in.home()
	fold := in.goos() == "darwin" || in.goos() == "windows" // case-insensitive file systems by default
	norm := func(s string) string {
		if fold {
			return strings.ToLower(s)
		}
		return s
	}
	under := func(base string) (rest string, ok bool) {
		b, q := norm(filepath.Clean(base)), norm(p)
		if q == b {
			return "", true
		}
		if strings.HasPrefix(q, b+string(filepath.Separator)) {
			return p[len(b)+1:], true
		}
		return "", false
	}
	first := func(rest string) string {
		if i := strings.IndexRune(rest, filepath.Separator); i >= 0 {
			return rest[:i]
		}
		return rest
	}
	if home != "" {
		if _, ok := under(filepath.Join(home, "Library", "Mobile Documents")); ok {
			return "iCloud Drive", filepath.Join(home, "Library", "Mobile Documents")
		}
		if rest, ok := under(filepath.Join(home, "Library", "CloudStorage")); ok {
			name := first(rest)
			if name == "" {
				return "a cloud storage provider", filepath.Join(home, "Library", "CloudStorage")
			}
			return cloudStorageService(name), filepath.Join(home, "Library", "CloudStorage", name)
		}
		if rest, ok := under(home); ok && rest != "" {
			if s := homeSyncService(first(rest), fold); s != "" {
				return s, filepath.Join(home, first(rest))
			}
		}
	}
	if in.goos() == "darwin" {
		if rest, ok := under("/Volumes"); ok && rest != "" {
			name := first(rest)
			if l := strings.ToLower(name); strings.HasPrefix(l, "googledrive") || strings.HasPrefix(l, "google drive") {
				return "Google Drive", filepath.Join("/Volumes", name)
			}
		}
	}
	return "", ""
}

// cloudStorageService names the provider of a ~/Library/CloudStorage/<name> folder
// (macOS File Provider locations: GoogleDrive-<account>, OneDrive-<org>, Dropbox, Box-Box…).
func cloudStorageService(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.HasPrefix(l, "googledrive"):
		return "Google Drive"
	case strings.HasPrefix(l, "onedrive"):
		return "OneDrive"
	case strings.HasPrefix(l, "dropbox"):
		return "Dropbox"
	case strings.HasPrefix(l, "box"):
		return "Box"
	case strings.HasPrefix(l, "icloud"):
		return "iCloud Drive"
	}
	return name + " (cloud storage)"
}

// homeSyncService recognizes the classic sync folders directly in the home directory.
func homeSyncService(dir string, fold bool) string {
	eq := func(a, b string) bool {
		if fold {
			return strings.EqualFold(a, b)
		}
		return a == b
	}
	prefix := func(s, p string) bool { return len(s) >= len(p) && eq(s[:len(p)], p) }
	switch {
	case eq(dir, "Dropbox") || prefix(dir, "Dropbox ("):
		return "Dropbox"
	case eq(dir, "Google Drive") || eq(dir, "GoogleDrive") || eq(dir, "My Drive"):
		return "Google Drive"
	case eq(dir, "OneDrive") || prefix(dir, "OneDrive -") || prefix(dir, "OneDrive-") || prefix(dir, "OneDrive "):
		return "OneDrive"
	case eq(dir, "Box") || eq(dir, "Box Sync") || eq(dir, "Box Drive"):
		return "Box"
	case eq(dir, "iCloud Drive") || eq(dir, "iCloudDrive"):
		return "iCloud Drive"
	}
	return ""
}

// resolveExisting resolves symlinks in the longest existing prefix of p.
func resolveExisting(p string) string {
	dir, rest := p, ""
	for {
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			if rest == "" {
				return real
			}
			return filepath.Join(real, rest)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return p
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// gitRoot walks up from p looking for a .git directory or file (worktrees and
// submodules use a file); when none is found and git is installed it asks
// `git rev-parse --show-toplevel` (core.worktree and similar layouts).
func (in Inspector) gitRoot(p string) string {
	for dir := p; ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	git := in.gitBin()
	if git == "" {
		return ""
	}
	dir := nearestDir(p)
	out, err := runGit(git, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return filepath.Clean(strings.TrimSpace(string(out)))
}

func nearestDir(p string) string {
	for dir := p; ; {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		dir = parent
	}
}

func (in Inspector) gitBin() string {
	switch in.Git {
	case "-":
		return ""
	case "":
		p, err := exec.LookPath("git")
		if err != nil {
			return ""
		}
		return p
	}
	return in.Git
}

// gitIgnored asks git whether p would be ignored (tracked files never are).
func (in Inspector) gitIgnored(root, p string) (ignored, checked bool) {
	git := in.gitBin()
	if git == "" {
		return false, false
	}
	rel, err := filepath.Rel(resolveExisting(root), resolveExisting(p))
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = p
	}
	_, err = runGit(git, root, "check-ignore", "-q", "--", rel)
	if err == nil {
		return true, true
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return false, true // definitely not ignored
	}
	return false, false // git could not answer (exit 128 and similar)
}

// runGit runs git with fixed arguments in dir, without inheriting GIT_* overrides.
func runGit(git, dir string, args ...string) ([]byte, error) {
	cmd := exec.Command(git, append([]string{"-C", dir}, args...)...)
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = nil
	err := cmd.Run()
	return stdout.Bytes(), err
}

// UnsafeError refuses a secret file location. Message and Hint are written for
// people; the CLI shows both.
type UnsafeError struct {
	Verdict Verdict
	What    string // e.g. "the join bundle"
	// EscapeHatch reports that --allow-unsafe-secret-path would allow this path.
	EscapeHatch bool
	Hint        string
}

func (e *UnsafeError) Error() string {
	return fmt.Sprintf("MetaRush won't keep %s at %s: it is %s.", e.What, tilde(e.Verdict.Path), e.Verdict.Describe())
}

// AppError maps the refusal to the stable E_UNSAFE_SECRET_PATH error. For a
// secrets file the command that moves the secrets into the keychain is kept
// with it (shown with --details), so the hint can stay in words.
func (e *UnsafeError) AppError() *apperr.Error {
	ae := apperr.New(apperr.UnsafeSecretPath, "%s", e.Error()).WithHint(e.Hint).
		WithDetails(map[string]any{"path": e.Verdict.Path, "kind": e.Verdict.Kind, "root": e.Verdict.Root, "service": e.Verdict.Service,
			"ignored": e.Verdict.Ignored, "escapeHatch": e.EscapeHatch})
	if strings.HasPrefix(e.What, "the secrets file") && !e.EscapeHatch {
		ae.WithCommand("metarush init --reconfigure --secrets keychain")
	}
	return ae
}

// tilde shows a path in the home folder as ~/… (the full path stays in Details).
func tilde(p string) string {
	if h, err := os.UserHomeDir(); err == nil && h != "" && strings.HasPrefix(p, h+string(filepath.Separator)) {
		return "~" + p[len(h):]
	}
	return p
}

// Check enforces the rules for writing a secret file at path (what names it in
// the error, e.g. "the join bundle"): never in a sync folder; in a git work tree
// only when git ignores the path and allowUnsafe is set.
func (in Inspector) Check(path, what string, allowUnsafe bool) error {
	v := in.Inspect(path)
	switch v.Kind {
	case KindSync:
		return &UnsafeError{Verdict: v, What: what,
			Hint: "Secrets never go into folders that sync to the cloud (there's no way around this). Keep them in the OS keychain (the default), or choose a place outside " + v.Service + ", for example the " + DefaultDirName + " folder in your home folder."}
	case KindGit:
		if !v.Ignored {
			return &UnsafeError{Verdict: v, What: what,
				Hint: "Keep it in the OS keychain (the default) or outside the repository, for example in the " + DefaultDirName + " folder in your home folder. To keep it in the repository, git must ignore it (.gitignore) and you must add --allow-unsafe-secret-path."}
		}
		if !allowUnsafe {
			return &UnsafeError{Verdict: v, What: what, EscapeHatch: true,
				Hint: "Keep it in the OS keychain (the default) or outside the repository, for example in the " + DefaultDirName + " folder in your home folder. Git ignores this file, so you may keep it there with --allow-unsafe-secret-path."}
		}
	}
	return nil
}

// Check is Inspector.Check with the default inspector.
func Check(path, what string, allowUnsafe bool) error {
	return defaultInspector.Check(path, what, allowUnsafe)
}

// CheckUse enforces the rules for reading an existing secret file: refused in a
// sync folder and where git would commit it; a git-ignored file is accepted
// (doctor still warns about it).
func (in Inspector) CheckUse(path, what string) error {
	v := in.Inspect(path)
	switch {
	case v.Kind == KindSync:
		return &UnsafeError{Verdict: v, What: what,
			Hint: "Move the secrets out of " + v.Service + " (they may already have been uploaded, so change those passwords and keys), or keep them in the OS keychain instead."}
	case v.Kind == KindGit && !v.Ignored:
		return &UnsafeError{Verdict: v, What: what,
			Hint: "Add the file to .gitignore (and check that it was never committed; if it was, change those passwords and keys), move MetaRush's folder out of the repository, or keep the secrets in the OS keychain instead."}
	}
	return nil
}

// CheckUse is Inspector.CheckUse with the default inspector.
func CheckUse(path, what string) error { return defaultInspector.CheckUse(path, what) }

// ProjectFolderMarker is the file every MetaRush project folder carries
// (`metarush project checkout` writes it): <folder>/.metarush/lock.json.
// Project folders are made to be shared (git, a team drive), so nothing that
// could leak (log exports, bug reports) is written inside one.
var ProjectFolderMarker = filepath.Join(".metarush", "lock.json")

// ProjectFolder returns the MetaRush project folder that holds the file at path
// (the nearest folder above it with the marker), or "" when there is none.
// path need not exist.
func ProjectFolder(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	for dir := filepath.Dir(filepath.Clean(abs)); ; {
		if _, err := os.Lstat(filepath.Join(dir, ProjectFolderMarker)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// DefaultDirName is where MetaRush writes credential files it creates for you
// to move elsewhere (join bundles, CI dotenv files): ~/.metarush-bundles.
const DefaultDirName = ".metarush-bundles"

// DefaultDir returns ~/.metarush-bundles, creating it 0700.
func DefaultDir() (string, error) {
	h := defaultInspector.home()
	if h == "" {
		return "", errors.New("cannot determine the home directory for ~/" + DefaultDirName)
	}
	d := filepath.Join(h, DefaultDirName)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	_ = os.Chmod(d, 0o700)
	return d, nil
}

// DefaultDirHint is the display form of the default credential folder.
func DefaultDirHint(home string) string {
	if home == "" {
		return "~/" + DefaultDirName
	}
	return filepath.Join(home, DefaultDirName)
}

// LooseMode reports whether a file is readable or writable by group or others.
func LooseMode(path string) (os.FileMode, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, false
	}
	return st.Mode().Perm(), st.Mode().Perm()&0o077 != 0
}
