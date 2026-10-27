package components

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/providers"
)

// EnvBase points MetaRush's own downloads at a server on this computer
// (loopback only): tests and browser runs, which never reach the internet.
const EnvBase = "METARUSH_COMPONENTS_BASE"

// underTest is testing.Testing (a test turns it off to check a browser
// run's refusal).
var underTest = testing.Testing

// refusesInternet: under go test, or in a browser run (METARUSH_NO_PAID_SERVICES=1).
func refusesInternet() bool {
	return underTest() || strings.TrimSpace(os.Getenv(providers.EnvNoPaidServices)) == "1"
}

// dest is where a pin's files go.
func (e *Env) dest(p Pin) string {
	switch {
	case strings.HasPrefix(p.To, "models/"):
		return filepath.Join(e.Tools.ModelsDir(), filepath.FromSlash(strings.TrimPrefix(p.To, "models/")))
	default:
		return filepath.Join(e.Home(), filepath.FromSlash(p.To))
	}
}

// configPath is what a pin's config setting points at once it is in place.
func (e *Env) configPath(p Pin) string {
	if p.ConfigPath == "" {
		return e.dest(p)
	}
	return filepath.Join(e.dest(p), filepath.FromSlash(p.ConfigPath))
}

// fetch downloads a pin, checks it, unpacks what it keeps and puts it in
// place in one rename (what was there before goes only once the new one is
// whole), then points config.yaml at it.
func (e *Env) fetch(ctx context.Context, p Pin, say func(string)) error {
	src, err := source(p)
	if err != nil {
		return err
	}
	dst := e.dest(p)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), "."+filepath.Base(dst)+"-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	say(fmt.Sprintf("Downloading %s (%s)…", p.Title, mbWords(p.Bytes)))
	file := filepath.Join(tmp, "download")
	if err := e.download(ctx, src, file, p.Bytes, p.Hosts); err != nil {
		return err
	}
	sum, err := fileSHA(file)
	st, serr := os.Stat(file)
	exact := err == nil && sum == p.SHA256 && serr == nil && st.Size() == p.Bytes
	if !exact && (p.ProgramSHA256 == "" || !archive(p.URL)) {
		return apperr.New(apperr.Integrity, "The download of %s isn't the file MetaRush expects, so nothing was added.", p.Title).
			WithDetails(map[string]any{"host": hostOf(src), "url": p.URL})
	}
	keep := file
	if archive(p.URL) {
		say("Unpacking " + p.Title + "…")
		out := filepath.Join(tmp, "files")
		take := filepath.Clean(filepath.FromSlash(p.Take))
		if err := unpackOnly(file, out, p.URL, take, p.Bytes); err != nil {
			return apperr.Wrap(apperr.Integrity, err, "MetaRush couldn't unpack %s.", p.Title)
		}
		keep = filepath.Join(out, take)
		if err := keptInside(out, take); err != nil {
			return apperr.Wrap(apperr.Integrity, err, "%s doesn't hold what MetaRush expects.", p.Title).WithDetails(map[string]any{"missing": p.Take})
		}
	}
	if p.ProgramSHA256 != "" {
		// An unversioned address: the program inside is what's pinned.
		if got, err := fileSHA(keep); err != nil || got != p.ProgramSHA256 {
			return apperr.New(apperr.Integrity, "The download of %s has changed since MetaRush pinned it (its publisher replaced it), so nothing was added.", p.Title).
				WithDetails(map[string]any{"host": hostOf(src), "url": p.URL, "zipChanged": !exact})
		}
	}
	if p.Exec {
		// keep's path holds no links (keptInside), so this changes only it.
		if err := os.Chmod(keep, 0o755); err != nil {
			return err
		}
	}
	old := ""
	if _, err := os.Lstat(dst); err == nil {
		old = filepath.Join(tmp, "old")
		if err := os.Rename(dst, old); err != nil {
			return err
		}
	}
	if err := os.Rename(keep, dst); err != nil {
		if old != "" {
			_ = os.Rename(old, dst)
		}
		return err
	}
	if err := e.record(p); err != nil {
		return err
	}
	if p.Config != "" {
		if err := config.SetField(e.Home(), []string{"tools", p.Config}, e.configPath(p)); err != nil {
			return err
		}
	}
	return nil
}

// unfetch removes what a pin put in place and the setting pointing at it.
func (e *Env) unfetch(p Pin) error {
	dst := e.dest(p)
	if p.Config != "" && toolSetting(e.toolsNow(), p.Config) == e.configPath(p) {
		if err := config.SetField(e.Home(), []string{"tools", p.Config}, ""); err != nil {
			return err
		}
	}
	e.unrecord(p)
	return os.RemoveAll(dst)
}

// placed reports whether a pin's files are in place.
func (e *Env) placed(p Pin) bool {
	_, err := os.Stat(e.dest(p))
	return err == nil
}

// toolsNow is config.yaml's tools settings as they are now (a component
// added or removed changes them; the running MetaRush's own copy is left
// alone and its programs are looked for again with these).
func (e *Env) toolsNow() config.Tools {
	if c, err := config.Load(e.Home()); err == nil {
		return c.Tools
	}
	return e.Cfg.Tools
}

func toolSetting(t config.Tools, key string) string {
	switch key {
	case "node":
		return t.Node
	case "ffmpeg":
		return t.FFmpeg
	case "ffprobe":
		return t.FFprobe
	case "ytDlp":
		return t.YtDlp
	case "sherpaDir":
		return t.SherpaDir
	}
	return ""
}

// source is a pin's address, or the stand-in server on this computer.
func source(p Pin) (string, error) {
	if base := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvBase)), "/"); base != "" {
		if !loopback(base) {
			return "", apperr.New(apperr.NotConfigured, "%s must be a server on this computer.", EnvBase)
		}
		return base + "/" + filepath.Base(p.URL), nil
	}
	if refusesInternet() && !loopback(p.URL) {
		return "", apperr.New(apperr.NotConfigured, "Tests never download anything (%s).", hostOf(p.URL))
	}
	return p.URL, nil
}

func loopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

func hostOf(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

// redirects is a download's redirect policy: HTTPS to the address's own
// host or one its publisher uses; a stand-in server on this computer only to
// itself.
func redirects(src string, hosts []string) func(*http.Request, []*http.Request) error {
	from, _ := url.Parse(src)
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		to := req.URL
		switch {
		case from == nil:
		case loopback(src) && to.Host == from.Host:
			return nil
		case loopback(src):
		case to.Scheme == "https" && (to.Hostname() == from.Hostname() || slices.Contains(hosts, to.Hostname())):
			return nil
		}
		return errRedirect{to.Hostname()}
	}
}

// errRedirect is a redirect to a place the publisher doesn't use.
type errRedirect struct{ host string }

func (e errRedirect) Error() string { return "the download was sent to " + e.host }

func (e *Env) download(ctx context.Context, src, dst string, want int64, hosts []string) error {
	base := e.Client
	if base == nil {
		base = &http.Client{Timeout: 90 * time.Minute}
	}
	c := *base
	c.CheckRedirect = redirects(src, hosts)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var r errRedirect
		if errors.As(err, &r) {
			return apperr.New(apperr.Integrity, "%s sent the download elsewhere (%s), so MetaRush didn't take it.", hostOf(src), r.host)
		}
		return apperr.New(apperr.Provider, "MetaRush couldn't reach %s to download it. Check the internet connection and try again.", hostOf(src))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apperr.New(apperr.Provider, "%s didn't send the file (it answered %d). Try again later.", hostOf(src), resp.StatusCode)
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

func archive(u string) bool {
	for _, s := range []string{".zip", ".tar.gz", ".tgz", ".tar.bz2"} {
		if strings.HasSuffix(u, s) {
			return true
		}
	}
	return false
}

// Limits on what an archive may hold.
const (
	maxTotal = 8 << 30
	maxFiles = 40000
)

// unpack writes an archive's files, folders and links under dst: every
// write goes through an os.Root opened on dst (nothing can be created
// outside it, whatever links the archive holds), no entry may sit below a
// link the archive made, links may only point inside the archive, and no
// devices; sizes are bounded.
func unpack(file, dst, u string) error { return unpackOnly(file, dst, u, "", 0) }

// keptInside checks what fetch keeps from an unpacked archive: no link on its
// way (it is moved with plain calls afterwards), itself not a link, and every
// link inside it pointing inside it.
func keptInside(out, take string) error {
	root, err := os.OpenRoot(out)
	if err != nil {
		return err
	}
	defer root.Close()
	if throughLink(root, take) {
		return errors.New("a link is on the way to what it keeps")
	}
	fi, err := root.Lstat(take)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("what it keeps is a link")
	}
	if !fi.IsDir() {
		return nil
	}
	kr, err := root.OpenRoot(take)
	if err != nil {
		return err
	}
	defer kr.Close()
	return fs.WalkDir(kr.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 {
			if _, err := kr.Stat(path); err != nil {
				return fmt.Errorf("a link inside points outside it (%s)", path)
			}
		}
		return nil
	})
}

// unpackOnly unpacks what take names (and the folders on its way; "" the
// whole archive), at most a size its download allows (download×10 + 64 MB;
// 0: the general limit), then checks that every link it made points inside.
func unpackOnly(file, dst, u, take string, download int64) error {
	limit := int64(maxTotal)
	if download > 0 {
		limit = 10*download + 64<<20
	}
	wanted := func(name string) bool {
		sep := string(filepath.Separator)
		return take == "" || name == take || strings.HasPrefix(name, take+sep) || strings.HasPrefix(take, name+sep)
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer root.Close()
	if strings.HasSuffix(u, ".zip") {
		return unzip(file, root, wanted, limit)
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader
	if strings.HasSuffix(u, ".tar.bz2") {
		r = bzip2.NewReader(f)
	} else {
		zr, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer zr.Close()
		r = zr
	}
	tr := tar.NewReader(r)
	var total int64
	files := 0
	var links []string
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			// Each link must lead somewhere inside, followed for real (a chain
			// of links can point out though each looks inside by its words).
			for _, l := range links {
				if _, err := root.Stat(l); err != nil {
					return fmt.Errorf("the archive's links point outside its folder (%s)", l)
				}
			}
			return nil
		}
		if err != nil {
			return err
		}
		name, ok := inside(h.Name)
		if !ok {
			return fmt.Errorf("the archive names a file outside its folder (%s)", h.Name)
		}
		if name == "" || strings.HasPrefix(filepath.Base(name), "._") || !wanted(name) {
			continue
		}
		if throughLink(root, name) {
			return fmt.Errorf("the archive puts something below one of its own links (%s)", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			files++
			total += h.Size
			if files > maxFiles || total > limit {
				return errors.New("the archive is larger than anything MetaRush adds")
			}
			if err := writeFile(root, name, tr, h.Size, os.FileMode(h.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			// Node.js's archive links npm and npx to files inside it.
			if filepath.IsAbs(h.Linkname) {
				return fmt.Errorf("the archive links outside its folder (%s)", h.Name)
			}
			if to, ok := inside(filepath.Join(filepath.Dir(name), h.Linkname)); !ok || to == "" {
				return fmt.Errorf("the archive links outside its folder (%s)", h.Name)
			}
			if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
				return err
			}
			if err := root.Symlink(h.Linkname, name); err != nil {
				return err
			}
			links = append(links, name)
		case tar.TypeXGlobalHeader, tar.TypeXHeader:
		default:
			return fmt.Errorf("the archive holds something other than files, folders and links (%s)", h.Name)
		}
	}
}

// throughLink reports whether a path's folders pass through a link already
// in the archive's folder.
func throughLink(root *os.Root, name string) bool {
	for dir := filepath.Dir(name); dir != "." && dir != string(filepath.Separator); dir = filepath.Dir(dir) {
		if fi, err := root.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return true
		}
	}
	return false
}

// inside cleans an archive path; ok is false when it leaves the archive.
func inside(name string) (string, bool) {
	n := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(n) || n == ".." || strings.HasPrefix(n, ".."+string(filepath.Separator)) {
		return "", false
	}
	if n == "." {
		return "", true
	}
	return n, true
}

func writeFile(root *os.Root, name string, r io.Reader, size int64, mode os.FileMode) error {
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if mode&0o111 != 0 {
		perm = 0o755
	}
	w, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, io.LimitReader(r, size))
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	return err
}

func unzip(file string, root *os.Root, wanted func(string) bool, limit int64) error {
	zr, err := zip.OpenReader(file)
	if err != nil {
		return err
	}
	defer zr.Close()
	if len(zr.File) > maxFiles {
		return errors.New("the archive is larger than anything MetaRush adds")
	}
	var total uint64
	for _, f := range zr.File {
		name, ok := inside(f.Name)
		if !ok {
			return fmt.Errorf("the archive names a file outside its folder (%s)", f.Name)
		}
		if name == "" || strings.HasPrefix(name, "__MACOSX") || strings.HasPrefix(filepath.Base(name), "._") || !wanted(name) {
			continue
		}
		mode := f.Mode()
		switch {
		case mode.IsDir():
			if err := root.MkdirAll(name, 0o755); err != nil {
				return err
			}
			continue
		case !mode.IsRegular():
			return fmt.Errorf("the archive holds something other than files and folders (%s)", f.Name)
		}
		if total += f.UncompressedSize64; total > uint64(limit) {
			return errors.New("the archive is larger than anything MetaRush adds")
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeFile(root, name, rc, int64(f.UncompressedSize64), mode)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// mbWords says a size in words.
func mbWords(b int64) string {
	mb := int((b + 500_000) / 1_000_000)
	if mb >= 1000 {
		return fmt.Sprintf("%.1f GB", float64(mb)/1000)
	}
	if mb < 1 {
		mb = 1
	}
	return fmt.Sprintf("%d MB", mb)
}
