package components

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/tools"
)

// What MetaRush downloaded into <home>/tools is written down when it is put
// in place (<home>/tools/pinned.json): every file of it (all of Node.js, npm
// included; the transcriber's folder; each single program) by its SHA-256
// and every link by where it points. Before a program there runs
// (tools.Trusted), and when components are looked at, the whole download it
// belongs to is checked against that record — and a single program also
// against the checksum built into its pin. A download with no record, or a
// record that no longer matches, isn't run: "add it again". yt-dlp updates
// itself through MetaRush (linkimport calls tools.Recorded afterwards).
//
// This catches accidental changes and other programs changing these files;
// it doesn't stop someone who can act as this user, who could change the
// record (or MetaRush) as well.

// pinnedFile is the record, beside the programs.
const pinnedFile = "pinned.json"

func (e *Env) toolsDir() string { return filepath.Join(e.Home(), "tools") }

// managedTo are the places in a home's tools folder MetaRush's pins put
// things (from every pin, every platform).
var managedTo = func() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range pins {
		if strings.HasPrefix(p.To, "tools/") && !seen[p.To] {
			seen[p.To] = true
			out = append(out, filepath.FromSlash(p.To))
		}
	}
	sort.Strings(out)
	return out
}()

var pinnedMu sync.Mutex

type manifest map[string]string // file → SHA-256, or "link:<target>"

func readPinned(dir string) map[string]manifest {
	m := map[string]manifest{}
	if b, err := os.ReadFile(filepath.Join(dir, pinnedFile)); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

func writePinned(dir string, m map[string]manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := filepath.Join(dir, pinnedFile+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, pinnedFile))
}

// manifestOf reads what is at dest now: a file, or every file and link of a
// folder (links not followed).
func manifestOf(dest string) (manifest, error) {
	m := manifest{}
	fi, err := os.Lstat(dest)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		sum, err := fileSHA(dest)
		if err != nil {
			return nil, err
		}
		m[dest] = sum
		return m, nil
	}
	err = filepath.WalkDir(dest, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			to, err := os.Readlink(path)
			if err != nil {
				return err
			}
			m[path] = "link:" + to
		case d.Type().IsRegular():
			sum, err := cachedSHA(path)
			if err != nil {
				return err
			}
			m[path] = sum
		}
		return nil
	})
	return m, err
}

var shaCache sync.Map // path|size|mtime → SHA-256

func cachedSHA(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	key := fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
	if v, ok := shaCache.Load(key); ok {
		return v.(string), nil
	}
	sum, err := fileSHA(path)
	if err != nil {
		return "", err
	}
	shaCache.Store(key, sum)
	return sum, nil
}

// record writes down what a placed pin put in place.
func (e *Env) record(p Pin) error {
	if !strings.HasPrefix(p.To, "tools/") {
		return nil
	}
	placedPins.Store(e.dest(p), p)
	return recordDest(e.toolsDir(), e.dest(p))
}

// placedPins are the pins this MetaRush put in place (by destination): the
// built-in ones, or a test's stand-ins.
var placedPins sync.Map

func recordDest(toolsDir, dest string) error {
	m, err := manifestOf(dest)
	if err != nil {
		return err
	}
	pinnedMu.Lock()
	defer pinnedMu.Unlock()
	all := readPinned(toolsDir)
	all[dest] = m
	return writePinned(toolsDir, all)
}

// unrecord forgets a removed pin's files.
func (e *Env) unrecord(p Pin) {
	pinnedMu.Lock()
	defer pinnedMu.Unlock()
	all := readPinned(e.toolsDir())
	delete(all, e.dest(p))
	_ = writePinned(e.toolsDir(), all)
}

// managedDest finds the download a path belongs to: the home's tools folder
// (a folder named tools beside a config.yaml) and the place a pin puts
// things that holds the path ("" when the path isn't one).
func managedDest(path string) (toolsDir, dest string) {
	for d := filepath.Dir(path); d != filepath.Dir(d); d = filepath.Dir(d) {
		if filepath.Base(d) != "tools" {
			continue
		}
		home := filepath.Dir(d)
		if _, err := os.Stat(filepath.Join(home, "config.yaml")); err != nil {
			continue
		}
		for _, to := range managedTo {
			dst := filepath.Join(home, to)
			if path == dst || strings.HasPrefix(path, dst+string(filepath.Separator)) {
				return d, dst
			}
		}
		return "", ""
	}
	return "", ""
}

// builtIn is the checksum a pin carries for a single program at dest ("" when
// it carries none: an archive's folder, or yt-dlp, which updates itself).
func builtIn(home, dest string) string {
	of := func(p Pin) string {
		switch {
		case p.Config == "ytDlp":
			return ""
		case p.ProgramSHA256 != "":
			return p.ProgramSHA256
		case !archive(p.URL):
			return p.SHA256
		}
		return ""
	}
	if v, ok := placedPins.Load(dest); ok {
		return of(v.(Pin))
	}
	for _, p := range pins {
		if strings.HasPrefix(p.To, "tools/") && filepath.Join(home, filepath.FromSlash(p.To)) == dest && p.For(runtime.GOOS, runtime.GOARCH) {
			return of(p)
		}
	}
	return ""
}

// trusted is nil for a program MetaRush didn't download, or one whose whole
// download is as MetaRush put it in place; otherwise it says, plainly, that
// it changed (or can't be told apart from a changed one).
func trusted(path string) error {
	toolsDir, dest := managedDest(path)
	if dest == "" {
		return nil
	}
	return checkDest(toolsDir, dest)
}

func checkDest(toolsDir, dest string) error {
	changed := func() error {
		name := filepath.Base(dest)
		if c := ForTool(name); c != nil {
			name = c.Thing
		}
		return apperr.New(apperr.Integrity, "%s was changed after MetaRush added it, so MetaRush won't run it. Add it again in Settings → Components.", upperFirst(name)).
			WithDetails(map[string]any{"path": dest})
	}
	want, ok := readPinned(toolsDir)[dest]
	now, err := manifestOf(dest)
	if err != nil {
		return nil // gone: whoever runs it finds that out
	}
	if b := builtIn(filepath.Dir(toolsDir), dest); b != "" && now[dest] != b {
		return changed()
	}
	if !ok || len(want) != len(now) {
		return changed()
	}
	for f, sum := range want {
		if now[f] != sum {
			return changed()
		}
	}
	return nil
}

// recorded writes a download down again after MetaRush itself changed it
// (yt-dlp's update through MetaRush).
func recorded(path string) {
	if toolsDir, dest := managedDest(path); dest != "" {
		_ = recordDest(toolsDir, dest)
	}
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// changed says whether a component's placed downloads were changed since
// MetaRush put them in place ("" when they weren't).
func (e *Env) changed(c *Component) string {
	for _, w := range c.ways {
		for _, id := range w.Pins {
			for _, p := range e.pinsFor(id) {
				if !strings.HasPrefix(p.To, "tools/") || !e.placed(p) {
					continue
				}
				if err := checkDest(e.toolsDir(), e.dest(p)); err != nil {
					return apperr.From(err).Message
				}
			}
		}
	}
	return ""
}

func init() {
	tools.Trusted = trusted
	tools.Recorded = recorded
}
