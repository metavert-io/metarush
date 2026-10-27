package localdb

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The files kept from MongoDB's archive: the server itself, and the licence
// and notices that come with it (MongoDB's own terms travel with the copy).
var keptFiles = map[string]bool{"bin/mongod": true, "LICENSE-Community.txt": true, "MPL-2": true, "README": true, "THIRD-PARTY-NOTICES": true}

// maxMongod caps the program read from an archive.
const maxMongod = 600 << 20

// EnvArchive names a copy of the pinned archive that MetaRush uses instead
// of downloading it (an offline computer): it is checked against the pinned
// SHA-256 all the same.
const EnvArchive = "METARUSH_MONGODB_ARCHIVE"

// EnvPin names a JSON file ({"version", "url", "sha256"}) that replaces the
// pin, for browser runs that serve an archive from this computer: only a
// loopback address (http://127.0.0.1:…) is accepted, so it can never send
// MetaRush somewhere else for its database.
const EnvPin = "METARUSH_MONGODB_PIN"

// PinFromEnv is the pin for this computer: Current, or the stand-in EnvPin
// names (loopback only).
func PinFromEnv() (Pin, error) {
	p := os.Getenv(EnvPin)
	if p == "" {
		return Current, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return Pin{}, plainErr("the stand-in database download couldn't be read", err)
	}
	var f struct {
		Version string `json:"version"`
		URL     string `json:"url"`
		SHA256  string `json:"sha256"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return Pin{}, plainErr("the stand-in database download isn't readable", err)
	}
	if !loopbackURL(f.URL) || f.Version == "" || len(f.SHA256) != 64 {
		return Pin{}, errors.New("a stand-in database download must name a version, its SHA-256 and an address on this computer")
	}
	t, _ := Target()
	return Pin{Version: f.Version, SizeMB: 1, Archives: map[string]Archive{t: {URL: f.URL, SHA256: f.SHA256}}}, nil
}

func loopbackURL(u string) bool {
	return strings.HasPrefix(u, "http://127.0.0.1:") || strings.HasPrefix(u, "http://localhost:")
}

// ErrNoBuild means MongoDB has no build of its server for this computer.
var ErrNoBuild = errors.New("MongoDB doesn't make its server for this kind of computer")

// Install downloads the pinned archive (or takes the copy EnvArchive names),
// checks its SHA-256, and keeps the server and its licence in
// <home>/database/mongodb/<version>. It does nothing when that version is
// already there and checks out. progress, when set, hears the bytes so far
// and the total (0 when unknown).
func (s *Server) Install(ctx context.Context, progress func(done, total int64)) error {
	pin := s.pin()
	target := s.target()
	arch, ok := pin.For(target)
	if !ok {
		return ErrNoBuild
	}
	dir := s.versionDir(pin.Version)
	if s.verifiedBinary(ctx, pin.Version) {
		return nil
	}
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(s.Dir(), "download-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	file := filepath.Join(tmp, "mongodb.tgz")
	if local := os.Getenv(EnvArchive); local != "" {
		if err := copyFile(local, file); err != nil {
			return plainErr("the copy of MongoDB's download couldn't be read", err)
		}
	} else if err := s.download(ctx, arch.URL, file, progress); err != nil {
		return err
	}
	if sum, err := fileSHA(file); err != nil || sum != arch.SHA256 {
		return fmt.Errorf("the download from %s isn't the one MetaRush expects (its SHA-256 differs), so it wasn't used", hostOf(arch.URL))
	}
	staged := filepath.Join(tmp, "mongodb")
	if err := extract(file, staged); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(staged, "bin", "mongod")); err != nil {
		return fmt.Errorf("the download from %s has no database server in it", hostOf(arch.URL))
	}
	sum, err := fileSHA(filepath.Join(staged, "bin", "mongod"))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(staged, "mongod.sha256"), []byte(sum+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return err
	}
	_ = os.RemoveAll(dir) // a version left half there by an earlier try
	if err := os.Rename(staged, dir); err != nil {
		return err
	}
	if !s.verifiedBinary(ctx, pin.Version) {
		return errors.New("the database server MetaRush downloaded doesn't start on this computer")
	}
	return nil
}

// verifiedBinary reports whether a version's server is in place, unchanged
// since it was unpacked (the SHA-256 recorded then), and answers --version.
func (s *Server) verifiedBinary(ctx context.Context, version string) bool {
	bin := s.binary(version)
	want, err := os.ReadFile(filepath.Join(s.versionDir(version), "mongod.sha256"))
	if err != nil {
		return false
	}
	if got, err := fileSHA(bin); err != nil || got != strings.TrimSpace(string(want)) {
		return false
	}
	if !s.Tools.Use(toolName, bin) {
		return false
	}
	s.Tools.ForgetVersion(toolName)
	return s.Tools.Version(ctx, toolName) == version
}

func (s *Server) download(ctx context.Context, url, dst string, progress func(done, total int64)) error {
	// Tests never reach the internet: only a stand-in server on this computer.
	if testing.Testing() && !loopbackURL(url) {
		return fmt.Errorf("downloads are off in tests (%s)", hostOf(url))
	}
	c := s.Client
	if c == nil {
		c = &http.Client{Timeout: 30 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("couldn't reach %s to download the database: check the internet connection", hostOf(url))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d when MetaRush asked for the database", hostOf(url), resp.StatusCode)
	}
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	var r io.Reader = io.LimitReader(resp.Body, 1<<30)
	if progress != nil {
		r = &counting{r: r, total: resp.ContentLength, fn: progress}
	}
	_, err = io.Copy(f, r)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

type counting struct {
	r     io.Reader
	done  int64
	total int64
	last  time.Time
	fn    func(done, total int64)
}

func (c *counting) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.done += int64(n)
	if time.Since(c.last) > 500*time.Millisecond || err == io.EOF {
		c.last = time.Now()
		c.fn(c.done, c.total)
	}
	return n, err
}

// extract unpacks the kept files of a MongoDB archive into dir. Every entry
// sits in one top folder (mongodb-macos-aarch64-8.0.34/…); only regular files
// MetaRush knows by name are written, each to its own fixed place, so nothing
// in an archive can write anywhere else.
func extract(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return errors.New("the database download isn't a readable archive")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("the database download is damaged")
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			continue
		}
		top, rest, ok := strings.Cut(name, "/")
		if !ok || !strings.HasPrefix(top, "mongodb-") || !keptFiles[rest] {
			continue
		}
		dst := filepath.Join(dir, filepath.FromSlash(rest))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if rest == "bin/mongod" {
			mode = 0o755
		}
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, maxMongod))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
	}
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

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func hostOf(url string) string {
	u := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	if i := strings.IndexByte(u, '/'); i > 0 {
		u = u[:i]
	}
	return u
}
