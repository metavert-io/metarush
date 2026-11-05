package renderer

import (
	"bufio"
	"debug/elf"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The renderer's browser on Linux (D522's setup on WSL, any minimal Linux):
// Remotion's Chrome Headless Shell links against a few of the system's own
// libraries (NSS, ATK, GBM, ALSA…) that a fresh Ubuntu in WSL, a container or
// a server image often lacks; without them every render stops at once with
// "error while loading shared libraries". BrowserLibraries reads the
// browser's ELF header (its DT_NEEDED entries, with debug/elf: nothing runs)
// and looks for each library where the dynamic loader would, so setup,
// doctor and Getting started can say so before the first render, with the
// one command that adds them (an administrator's: MetaRush never runs sudo).

// Libraries is what the renderer's browser lacks here.
type Libraries struct {
	// Checked: the browser was found and read (Linux, downloaded).
	Checked bool `json:"checked"`
	// Missing are the library files it needs and this computer lacks.
	Missing []string `json:"missing,omitempty"`
	// Command adds them (Debian and Ubuntu; elsewhere the system's packages).
	Command string `json:"command,omitempty"`
}

// OK reports nothing missing (or nothing could be checked).
func (l Libraries) OK() bool { return len(l.Missing) == 0 }

// What setup, doctor and Getting started say when libraries are missing.
const (
	LibrariesSay  = "Finished videos can't be made yet: the renderer's browser needs a few of this computer's own libraries"
	LibrariesNext = "An administrator adds them with one command (Details shows it), then renders work."
)

// LibraryRoot stands for "/" when libraries are looked for (tests).
var LibraryRoot = "/"

var (
	libsMu    sync.Mutex
	libsCache = map[string]libsEntry{}
)

type libsEntry struct {
	mod time.Time
	out Libraries
}

// BrowserLibraries checks the browser of a renderer's video folder (Linux
// only; elsewhere, or before the browser is downloaded, Checked is false).
func BrowserLibraries(videoDir string) Libraries {
	if runtime.GOOS != "linux" && LibraryRoot == "/" {
		return Libraries{}
	}
	bin := findBrowser(filepath.Join(videoDir, "node_modules", ".remotion", "chrome-headless-shell"))
	if bin == "" {
		return Libraries{}
	}
	st, err := os.Stat(bin)
	if err != nil {
		return Libraries{}
	}
	libsMu.Lock()
	if e, ok := libsCache[bin]; ok && e.mod.Equal(st.ModTime()) && len(e.out.Missing) == 0 {
		libsMu.Unlock()
		return e.out // a library added since is looked for again: only a clean answer is kept
	}
	libsMu.Unlock()
	f, err := elf.Open(bin)
	if err != nil {
		return Libraries{}
	}
	needed, err := f.ImportedLibraries()
	f.Close()
	if err != nil {
		return Libraries{}
	}
	out := Libraries{Checked: true, Missing: missingLibraries(needed, libraryDirs(filepath.Dir(bin)))}
	if len(out.Missing) > 0 {
		out.Command = libraryCommand(readFile(filepath.Join(LibraryRoot, "etc", "os-release")), out.Missing)
	}
	libsMu.Lock()
	libsCache[bin] = libsEntry{mod: st.ModTime(), out: out}
	libsMu.Unlock()
	return out
}

// findBrowser is the chrome-headless-shell program under dir (Remotion keeps
// it a folder or two down, per platform), "" when it isn't downloaded.
func findBrowser(dir string) string {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	found := ""
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || found != "" {
			return filepath.SkipDir
		}
		if rel, _ := filepath.Rel(dir, p); strings.Count(rel, string(filepath.Separator)) > 3 {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "chrome-headless-shell" {
			if info, err := d.Info(); err == nil && info.Mode().IsRegular() {
				found = p
			}
		}
		return nil
	})
	return found
}

// libraryDirs are where the dynamic loader looks: the program's own folder
// (Chrome's $ORIGIN), the multiarch and usual folders, then ld.so.conf.d's.
func libraryDirs(own string) []string {
	triplet := map[string]string{"amd64": "x86_64-linux-gnu", "arm64": "aarch64-linux-gnu"}[runtime.GOARCH]
	dirs := []string{own}
	for _, d := range []string{"/lib/" + triplet, "/usr/lib/" + triplet, "/lib64", "/usr/lib64", "/lib", "/usr/lib", "/usr/local/lib"} {
		dirs = append(dirs, filepath.Join(LibraryRoot, d))
	}
	confs, _ := filepath.Glob(filepath.Join(LibraryRoot, "etc", "ld.so.conf.d", "*.conf"))
	sort.Strings(confs)
	for _, c := range confs {
		sc := bufio.NewScanner(strings.NewReader(readFile(c)))
		for sc.Scan() {
			l := strings.TrimSpace(sc.Text())
			if strings.HasPrefix(l, "/") {
				dirs = append(dirs, filepath.Join(LibraryRoot, l))
			}
		}
	}
	return dirs
}

// missingLibraries are the needed library files found in none of dirs.
func missingLibraries(needed, dirs []string) []string {
	var out []string
	for _, n := range needed {
		found := false
		for _, d := range dirs {
			if _, err := os.Stat(filepath.Join(d, n)); err == nil {
				found = true
				break
			}
		}
		if !found {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// browserPackages are Debian's and Ubuntu's packages for what Chrome Headless
// Shell links against (Remotion's list, with the runtime packages rather than
// the -dev ones); t64 names from Ubuntu 24.04 and Debian 13 on.
var browserPackages = []string{"libnss3", "libdbus-1-3", "libatk1.0-0", "libasound2", "libxrandr2", "libxkbcommon0", "libxfixes3", "libxcomposite1",
	"libxdamage1", "libgbm1", "libcups2", "libcairo2", "libpango-1.0-0", "libatk-bridge2.0-0"}

// libraryCommand is the command that adds the libraries: apt on Debian and
// Ubuntu (by the release's package names), else the files to look for.
func libraryCommand(osRelease string, missing []string) string {
	f := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(osRelease))
	for sc.Scan() {
		if k, v, ok := strings.Cut(strings.TrimSpace(sc.Text()), "="); ok {
			f[k] = strings.Trim(v, `"'`)
		}
	}
	id, like := f["ID"], " "+f["ID_LIKE"]+" "
	major, _ := strconv.Atoi(strings.SplitN(f["VERSION_ID"], ".", 2)[0])
	debianLike := id == "debian" || id == "ubuntu" || strings.Contains(like, " debian ") || strings.Contains(like, " ubuntu ")
	if !debianLike {
		return "Install your system's packages that provide: " + strings.Join(missing, ", ")
	}
	t64 := id == "ubuntu" && major >= 24 || id == "debian" && major >= 13
	pkgs := make([]string, 0, len(browserPackages))
	for _, p := range browserPackages {
		if t64 && (p == "libasound2" || p == "libatk1.0-0" || p == "libatk-bridge2.0-0" || p == "libcups2") {
			p += "t64"
		}
		pkgs = append(pkgs, p)
	}
	return "sudo apt install -y " + strings.Join(pkgs, " ")
}

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}
