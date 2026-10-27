// Package localmodels sets up the files that models on this computer need
// (D278, D280): each is a pinned download — an archive at a fixed address,
// checked against its SHA-256 before anything is unpacked — made only when a
// person asks for it (metarush ai local setup, or the dashboard's Set up),
// never by itself and never under go test, unpacked into
// <home>/components/local-models/<id> without following links or leaving its
// folder. A program in it is pinned too: its SHA-256 is written down at
// setup and checked before every run, so a changed program is refused.
package localmodels

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/providers"
)

// Pin is one download MetaRush may make for a model on this computer.
type Pin struct {
	// ID names it in commands (metarush ai local setup <id>).
	ID string `json:"id"`
	// Title is what people call it.
	Title string `json:"title"`
	// What says what it is for, in a few words.
	What string `json:"what"`
	// URL and SHA256 are the archive and its checksum, as its publisher gives them.
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	// Bytes is the download's size.
	Bytes int64 `json:"bytes"`
	// Root is the folder the archive holds.
	Root string `json:"-"`
	// Needs are files (or folders) under Root that must be there after unpacking.
	Needs []string `json:"-"`
	// Program is a program under Root that MetaRush runs, pinned at setup.
	Program string `json:"-"`
	// Licence names the licence, with where it is published.
	Licence    string `json:"licence"`
	LicenceURL string `json:"licenceUrl"`
	// Platforms are the GOOS/GOARCH it is for (empty: any).
	Platforms []string `json:"platforms,omitempty"`
}

// For reports whether a pin is for this computer.
func (p Pin) For(goos, goarch string) bool {
	if len(p.Platforms) == 0 {
		return true
	}
	for _, x := range p.Platforms {
		if x == goos+"/"+goarch {
			return true
		}
	}
	return false
}

// EnvBase points downloads at a test server on this computer (loopback only).
const EnvBase = "METARUSH_LOCAL_MODELS_BASE"

// Store is one installation's set-up local model files.
type Store struct {
	Home string
	// Client downloads (default: a client with a 60-minute timeout).
	Client *http.Client
	// Pins override the pinned downloads (tests).
	Pins []Pin
}

// Dir is where a pin's files live.
func (s Store) Dir(p Pin) string { return filepath.Join(s.Home, "components", "local-models", p.ID) }

// Path is a file of a pin's archive, once it is set up.
func (s Store) Path(p Pin, rel string) string {
	return filepath.Join(s.Dir(p), p.Root, filepath.FromSlash(rel))
}

// Find is a pin for this computer by ID.
func (s Store) Find(id string) (Pin, bool) {
	for _, p := range s.all() {
		if p.ID == id && p.For(runtime.GOOS, runtime.GOARCH) {
			return p, true
		}
	}
	return Pin{}, false
}

func (s Store) all() []Pin {
	if s.Pins != nil {
		return s.Pins
	}
	return pins
}

// Here lists the pins for this computer.
func (s Store) Here() []Pin {
	var out []Pin
	seen := map[string]bool{}
	for _, p := range s.all() {
		if p.For(runtime.GOOS, runtime.GOARCH) && !seen[p.ID] {
			out = append(out, p)
			seen[p.ID] = true
		}
	}
	return out
}

// installed is what setup wrote down.
type installed struct {
	Archive string    `json:"archiveSha256"`
	Program string    `json:"programSha256,omitempty"`
	At      time.Time `json:"at"`
}

func (s Store) marker(p Pin) string { return filepath.Join(s.Dir(p), "installed.json") }

var checked sync.Map // program path|size|mtime → bool

// Ready is nil when a pin is set up here and unchanged; otherwise a plain
// error saying how to set it up.
func (s Store) Ready(p Pin) error {
	notReady := func() error {
		return apperr.New(apperr.NotConfigured, "%s isn't set up on this computer yet: setting it up downloads %s once.", upper(p.Title), Size(p.Bytes)).
			WithHint("An owner sets it up once; Details has the command.").WithCommand("metarush ai local setup " + p.ID)
	}
	b, err := os.ReadFile(s.marker(p))
	if err != nil {
		return notReady()
	}
	var in installed
	if json.Unmarshal(b, &in) != nil || in.Archive != p.SHA256 {
		return notReady()
	}
	for _, n := range p.Needs {
		if _, err := os.Stat(s.Path(p, n)); err != nil {
			return notReady()
		}
	}
	if p.Program != "" {
		prog := s.Path(p, p.Program)
		st, err := os.Stat(prog)
		if err != nil {
			return notReady()
		}
		key := fmt.Sprintf("%s|%d|%s", prog, st.Size(), st.ModTime())
		ok, seen := checked.Load(key)
		if !seen {
			sum, err := fileSHA(prog)
			ok = err == nil && sum == in.Program
			checked.Store(key, ok)
		}
		if !ok.(bool) {
			return apperr.New(apperr.NotConfigured, "%s was changed after it was set up, so MetaRush won't run it.", upper(p.Title)).
				WithHint("Set it up again.").WithCommand("metarush ai local setup " + p.ID)
		}
	}
	return nil
}

var setupMu sync.Mutex

// Setup downloads a pin, checks its SHA-256, unpacks it and writes down what
// it set up. Only a person asks for it; tests may only reach a server on
// this computer.
func (s Store) Setup(ctx context.Context, p Pin, progress func(string)) error {
	setupMu.Lock()
	defer setupMu.Unlock()
	if s.Ready(p) == nil {
		return nil
	}
	src, err := s.source(p)
	if err != nil {
		return err
	}
	dir := s.Dir(p)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "."+p.ID+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	archive := filepath.Join(tmp, "download")
	if progress != nil {
		progress(fmt.Sprintf("downloading %s (%s)", p.Title, Size(p.Bytes)))
	}
	if err := s.download(ctx, src, archive, p.Bytes); err != nil {
		return err
	}
	if sum, err := fileSHA(archive); err != nil || sum != p.SHA256 {
		return apperr.New(apperr.Integrity, "The download of %s isn't the file MetaRush expects (its checksum differs), so nothing was set up.", p.Title).
			WithDetails(map[string]any{"host": hostOf(src)})
	}
	if progress != nil {
		progress("unpacking " + p.Title)
	}
	unpacked := filepath.Join(tmp, "files")
	unpackFn := func() error {
		if strings.HasSuffix(p.URL, ".zip") {
			return unzip(archive, unpacked)
		}
		return unpack(archive, unpacked, strings.HasSuffix(p.URL, ".tar.gz") || strings.HasSuffix(p.URL, ".tgz"))
	}
	if err := unpackFn(); err != nil {
		return apperr.Wrap(apperr.Integrity, err, "MetaRush couldn't unpack %s.", p.Title)
	}
	_ = os.Remove(archive)
	in := installed{Archive: p.SHA256, At: time.Now().UTC()}
	if p.Program != "" {
		prog := filepath.Join(unpacked, p.Root, filepath.FromSlash(p.Program))
		if in.Program, err = fileSHA(prog); err != nil {
			return apperr.New(apperr.Integrity, "%s has no program %s in it.", p.Title, p.Program)
		}
	}
	for _, n := range p.Needs {
		if _, err := os.Stat(filepath.Join(unpacked, p.Root, filepath.FromSlash(n))); err != nil {
			return apperr.New(apperr.Integrity, "%s lacks %s.", p.Title, n)
		}
	}
	b, _ := json.Marshal(in)
	if err := os.WriteFile(filepath.Join(unpacked, "installed.json"), b, 0o644); err != nil {
		return err
	}
	// The old folder goes only once the new one is whole.
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(unpacked, dir)
}

// source is where a pin is downloaded from: its own address, or a test
// server on this computer (EnvBase). Tests and browser runs never reach the
// internet.
func (s Store) source(p Pin) (string, error) {
	if base := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvBase)), "/"); base != "" {
		if !loopback(base) {
			return "", apperr.New(apperr.NotConfigured, "%s must be a server on this computer.", EnvBase)
		}
		return base + "/" + filepath.Base(p.URL), nil
	}
	if refusesInternet() && !loopback(p.URL) {
		return "", apperr.New(apperr.NotConfigured, "Tests never download models (%s).", hostOf(p.URL))
	}
	return p.URL, nil
}

// underTest is testing.Testing, which a test turns off to check a browser
// run's refusal.
var underTest = testing.Testing

// refusesInternet: under go test, or in a browser run (METARUSH_NO_PAID_SERVICES=1).
func refusesInternet() bool {
	return underTest() || strings.TrimSpace(os.Getenv(providers.EnvNoPaidServices)) == "1"
}

func loopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

func (s Store) download(ctx context.Context, src, dst string, want int64) error {
	c := s.Client
	if c == nil {
		c = &http.Client{Timeout: 60 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		return apperr.New(apperr.Provider, "MetaRush couldn't reach %s to download it.", hostOf(src))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apperr.New(apperr.Provider, "%s answered %d.", hostOf(src), resp.StatusCode)
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	limit := want + want/10 + 1<<20 // a little over the pinned size, never unbounded
	_, err = io.Copy(f, io.LimitReader(resp.Body, limit))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// unpack writes an archive's regular files and folders under dst: nothing
// outside it, no links, no devices, bounded sizes.
func unpack(archive, dst string, gz bool) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = bzip2.NewReader(f)
	if gz {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer zr.Close()
		r = zr
	}
	tr := tar.NewReader(r)
	const maxTotal, maxFiles = 4 << 30, 20000
	var total int64
	files := 0
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(filepath.FromSlash(h.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("the archive names a file outside its folder (%s)", h.Name)
		}
		if name == "." || strings.HasPrefix(filepath.Base(name), "._") {
			continue // the folder itself; macOS's resource-fork twins
		}
		out := filepath.Join(dst, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			files++
			total += h.Size
			if files > maxFiles || total > maxTotal {
				return errors.New("the archive is larger than any model MetaRush sets up")
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if h.Mode&0o111 != 0 {
				mode = 0o755
			}
			w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
			if err != nil {
				return err
			}
			_, err = io.Copy(w, io.LimitReader(tr, h.Size))
			if cerr := w.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
		default:
			return fmt.Errorf("the archive holds something other than files and folders (%s)", h.Name)
		}
	}
}

// unzip writes a zip archive's regular files and folders under dst, by the
// same rules as unpack.
func unzip(archive, dst string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close()
	const maxTotal, maxFiles = 4 << 30, 20000
	var total uint64
	if len(zr.File) > maxFiles {
		return errors.New("the archive is larger than any model MetaRush sets up")
	}
	for _, f := range zr.File {
		name := filepath.Clean(filepath.FromSlash(f.Name))
		if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
			return fmt.Errorf("the archive names a file outside its folder (%s)", f.Name)
		}
		if name == "." || strings.HasPrefix(name, "__MACOSX") || strings.HasPrefix(filepath.Base(name), "._") {
			continue
		}
		out := filepath.Join(dst, name)
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(out, 0o755); err != nil {
				return err
			}
			continue
		case !mode.IsRegular():
			return fmt.Errorf("the archive holds something other than files and folders (%s)", f.Name)
		}
		if total += f.UncompressedSize64; total > maxTotal {
			return errors.New("the archive is larger than any model MetaRush sets up")
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if mode&0o111 != 0 {
			perm = 0o755
		}
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(w, io.LimitReader(rc, int64(f.UncompressedSize64)))
		rc.Close()
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func fileSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// Size says a download's size as people do: "31 MB", "1.2 GB".
func Size(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%d MB", (b+(1<<19))>>20)
	}
	return fmt.Sprintf("%d KB", (b+512)>>10)
}

func upper(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
