// Package cache is the per-installation, content-addressed local copy of media.
// Caches are never authoritative: every entry is verified against the SHA-256
// recorded in shared state, and anything missing is re-fetched from the canonical
// store (resuming partial downloads).
package cache

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/media"
)

// Cache is a content-addressed directory of verified objects.
type Cache struct {
	Dir      string
	MaxBytes int64
	mu       sync.Mutex
	inflight map[string]*sync.Mutex

	lmu  sync.Mutex     // guards refs
	refs map[string]int // in-use leases held by this process
	kick chan struct{}  // wakes the quota enforcer after downloads
}

// New opens (creating) a cache directory.
func New(dir string, maxGB float64) (*Cache, error) {
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "partial"), 0o755); err != nil {
		return nil, err
	}
	return &Cache{Dir: dir, MaxBytes: int64(maxGB * (1 << 30)), inflight: map[string]*sync.Mutex{}, refs: map[string]int{}, kick: make(chan struct{}, 1)}, nil
}

func validSHA(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// Path is where an object with this hash lives in the cache.
func (c *Cache) Path(sha string) string {
	return filepath.Join(c.Dir, "objects", sha[:2], sha)
}

func (c *Cache) partialPath(sha string) string { return filepath.Join(c.Dir, "partial", sha) }

// Has reports whether the object is cached (size-checked, not re-hashed).
func (c *Cache) Has(sha string, size int64) bool {
	if !validSHA(sha) {
		return false
	}
	st, err := os.Stat(c.Path(sha))
	return err == nil && (size <= 0 || st.Size() == size)
}

// PartialSize is how many bytes of an interrupted download are kept for resume.
func (c *Cache) PartialSize(sha string) int64 {
	if !validSHA(sha) {
		return 0
	}
	if st, err := os.Stat(c.partialPath(sha)); err == nil {
		return st.Size()
	}
	return 0
}

// Remove deletes this machine's copy of an object (and any partial
// download). It reports whether a copy was there. The canonical store is not
// touched; only asset.delete calls this, after its reference check.
func (c *Cache) Remove(sha string) (bool, error) {
	if c == nil || !validSHA(sha) {
		return false, nil
	}
	unlock := c.lock(sha)
	defer unlock()
	_ = os.Remove(c.partialPath(sha))
	err := os.Remove(c.Path(sha))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}

// Touch records a use of a cached object (LRU order for pruning). Serving a
// cached file to the dashboard counts as a use.
func (c *Cache) Touch(sha string) {
	if c == nil || !validSHA(sha) {
		return
	}
	p := c.Path(sha)
	if info, err := os.Stat(p); err == nil {
		_ = os.Chtimes(p, time.Now(), info.ModTime()) // atime = last use; mtime untouched
	}
}

func (c *Cache) notify() {
	if c.kick == nil {
		return
	}
	select {
	case c.kick <- struct{}{}:
	default:
	}
}

func (c *Cache) lock(sha string) func() {
	c.mu.Lock()
	if c.inflight == nil {
		c.inflight = map[string]*sync.Mutex{}
	}
	m, ok := c.inflight[sha]
	if !ok {
		m = &sync.Mutex{}
		c.inflight[sha] = m
	}
	c.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// Adopt puts a local file the user already has (e.g. the original they just
// ingested) into the cache. On APFS this is a copy-on-write clone that uses no
// extra space; otherwise it is a copy. The source is never modified or moved.
func (c *Cache) Adopt(src, sha string) (string, error) {
	if !validSHA(sha) {
		return "", fmt.Errorf("invalid sha256 %q", sha)
	}
	defer c.lock(sha)()
	dst := c.Path(sha)
	if _, err := os.Stat(dst); err == nil {
		c.Touch(sha)
		return dst, nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	tmp := dst + ".tmp"
	os.Remove(tmp)
	if err := cloneFile(src, tmp); err != nil {
		if err := copyFile(src, tmp); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	_ = os.Chmod(dst, 0o444)
	c.Touch(sha) // a clone keeps the source's atime; this is a use now
	c.notify()
	return dst, nil
}

// Ensure returns a verified local path for an object, downloading (with resume)
// from the canonical store when needed. The object is leased while Ensure runs
// and, when ctx carries a scope from Hold, until that scope is released.
func (c *Cache) Ensure(ctx context.Context, st media.Store, key, sha string, size int64, progress func(done, total int64)) (string, error) {
	if !validSHA(sha) {
		return "", fmt.Errorf("invalid sha256 %q", sha)
	}
	defer c.Acquire(sha)()
	if s := scopeFrom(ctx); s != nil {
		s.hold(c, sha)
	}
	defer c.lock(sha)()
	dst := c.Path(sha)
	if info, err := os.Stat(dst); err == nil && (size <= 0 || info.Size() == size) {
		now := time.Now()
		_ = os.Chtimes(dst, now, info.ModTime()) // atime = last use, for LRU
		return dst, nil
	}
	part := c.partialPath(sha)
	var offset int64
	if info, err := os.Stat(part); err == nil {
		offset = info.Size()
	}
	if size > 0 && offset > size {
		os.Remove(part)
		offset = 0
	}
	if size <= 0 || offset < size {
		rc, info, err := st.Get(ctx, key, offset, -1)
		if errors.Is(err, media.ErrNotFound) {
			return "", fmt.Errorf("a file MetaRush needs is missing from its storage (%s, in %s)", key, st.Locator())
		}
		if err != nil {
			return "", err
		}
		total := info.Size
		if size > 0 {
			total = size
		}
		f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			rc.Close()
			return "", err
		}
		buf := make([]byte, 1<<20)
		done := offset
		for {
			n, rerr := rc.Read(buf)
			if n > 0 {
				if _, werr := f.Write(buf[:n]); werr != nil {
					rc.Close()
					f.Close()
					return "", werr
				}
				done += int64(n)
				if progress != nil {
					progress(done, total)
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				rc.Close()
				f.Close()
				return "", rerr // partial file kept for resume
			}
			if err := ctx.Err(); err != nil {
				rc.Close()
				f.Close()
				return "", err // partial file kept for resume
			}
		}
		rc.Close()
		if err := f.Close(); err != nil {
			return "", err
		}
	}
	got, err := media.HashFile(part)
	if err != nil {
		return "", err
	}
	if got != sha {
		os.Remove(part)
		return "", fmt.Errorf("the downloaded copy of %s didn't match the original, so it wasn't kept (got %s, expected %s)", key, got, sha)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(part, dst); err != nil {
		return "", err
	}
	_ = os.Chmod(dst, 0o444)
	c.Touch(sha)
	c.notify()
	return dst, nil
}

// Verify re-hashes a cached object; corrupt entries are removed.
func (c *Cache) Verify(sha string) (bool, error) {
	p := c.Path(sha)
	got, err := media.HashFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if got != sha {
		_ = os.Chmod(p, 0o644)
		os.Remove(p)
		return false, nil
	}
	return true, nil
}

// Stats summarizes the cache.
type Stats struct {
	Dir          string       `json:"dir"`
	Objects      int          `json:"objects"`
	Bytes        int64        `json:"bytes"`
	MaxBytes     int64        `json:"maxBytes"`
	PartialBytes int64        `json:"partialBytes"`
	Partials     int          `json:"partials"`
	Pinned       int          `json:"pinned"`
	PinnedBytes  int64        `json:"pinnedBytes"`
	InUse        int          `json:"inUse"`
	InUseBytes   int64        `json:"inUseBytes"`
	Quota        *QuotaStatus `json:"quota,omitempty"`
}

// Status walks the cache.
func (c *Cache) Status() (Stats, error) {
	s := Stats{Dir: c.Dir, MaxBytes: c.MaxBytes}
	pins := c.pins()
	inUse := c.InUse()
	err := filepath.WalkDir(filepath.Join(c.Dir, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !validSHA(d.Name()) {
			return nil
		}
		if info, err := d.Info(); err == nil {
			s.Objects++
			s.Bytes += info.Size()
			if pins[d.Name()] {
				s.Pinned++
				s.PinnedBytes += info.Size()
			}
			if inUse[d.Name()] {
				s.InUse++
				s.InUseBytes += info.Size()
			}
		}
		return nil
	})
	_ = filepath.WalkDir(filepath.Join(c.Dir, "partial"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				s.Partials++
				s.PartialBytes += info.Size()
			}
		}
		return nil
	})
	s.Quota = c.ReadQuotaStatus()
	return s, err
}

func (c *Cache) pinFile() string { return filepath.Join(c.Dir, "pins.txt") }

func (c *Cache) pins() map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(c.pinFile())
	if err != nil {
		return out
	}
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); validSHA(l) {
			out[l] = true
		}
	}
	return out
}

// Pinned returns the set of pinned object hashes.
func (c *Cache) Pinned() map[string]bool { return c.pins() }

// Pin marks objects to keep during pruning.
func (c *Cache) Pin(shas ...string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := c.pins()
	for _, s := range shas {
		if validSHA(s) {
			p[s] = true
		}
	}
	var lines []string
	for s := range p {
		lines = append(lines, s)
	}
	sort.Strings(lines)
	return os.WriteFile(c.pinFile(), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// PruneResult reports one pruning pass.
type PruneResult struct {
	At            time.Time `json:"at"`
	Trigger       string    `json:"trigger"` // manual | quota
	TargetBytes   int64     `json:"targetBytes"`
	BeforeBytes   int64     `json:"beforeBytes"`
	AfterBytes    int64     `json:"afterBytes"`
	Removed       int       `json:"removed"`
	FreedBytes    int64     `json:"freedBytes"`
	SkippedPinned int       `json:"skippedPinned"`
	PinnedBytes   int64     `json:"pinnedBytes"`
	SkippedInUse  int       `json:"skippedInUse"`
	InUseBytes    int64     `json:"inUseBytes"`
	DryRun        bool      `json:"dryRun"`
}

// Prune evicts least-recently-used unpinned objects until under target bytes.
// It never touches anything outside the cache directory.
func (c *Cache) Prune(target int64, dryRun bool) (removed int, freed int64, err error) {
	r, err := c.PruneTo(target, dryRun)
	return r.Removed, r.FreedBytes, err
}

// PruneTo evicts least-recently-used objects until the cache is at or below
// target bytes (target < 0 means the configured limit). Pinned objects and
// objects leased by a live process are never evicted.
func (c *Cache) PruneTo(target int64, dryRun bool) (PruneResult, error) {
	if target < 0 {
		target = c.MaxBytes
	}
	res := PruneResult{At: time.Now().UTC(), Trigger: "manual", TargetBytes: target, DryRun: dryRun}
	type ent struct {
		path string
		size int64
		used time.Time
		sha  string
	}
	var ents []ent
	var total int64
	pins := c.pins()
	inUse := c.InUse()
	_ = filepath.WalkDir(filepath.Join(c.Dir, "objects"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !validSHA(d.Name()) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		total += info.Size()
		switch {
		case pins[d.Name()]:
			res.SkippedPinned++
			res.PinnedBytes += info.Size()
		case inUse[d.Name()]:
			res.SkippedInUse++
			res.InUseBytes += info.Size()
		default:
			ents = append(ents, ent{p, info.Size(), atime(info), d.Name()})
		}
		return nil
	})
	res.BeforeBytes = total
	sort.SliceStable(ents, func(i, j int) bool { return ents[i].used.Before(ents[j].used) })
	for _, e := range ents {
		if total <= target {
			break
		}
		if !dryRun {
			unlock := c.lock(e.sha)
			if c.leasedNow(e.sha) { // leased since the walk began
				unlock()
				res.SkippedInUse++
				res.InUseBytes += e.size
				continue
			}
			_ = os.Chmod(e.path, 0o644)
			err := os.Remove(e.path)
			unlock()
			if err != nil {
				continue
			}
		}
		total -= e.size
		res.FreedBytes += e.size
		res.Removed++
	}
	res.AfterBytes = total
	return res, nil
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

// HashReader hashes a stream (helper for callers verifying downloads).
func HashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
