package cache

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// In-use registry.
//
// A cached object that a running job, render or operation is reading must never
// be evicted or "repaired" out from under it. Callers take a lease with Acquire
// (or implicitly: Ensure leases what it returns for the lifetime of a scope
// created with Hold). Leases are reference-counted in memory and mirrored as
// empty files under <cache>/leases/<pid>/<sha256>, so the daemon's quota pruner
// also respects objects used by other processes on this machine (a CLI
// `render submit --wait --local`, a headless `worker start`, `render fetch`…).
// A lease directory whose process has exited is stale and is removed.
//
// Cross-process protection is best effort: there is a sub-millisecond window
// between another process creating its lease file and this process re-checking
// it before a delete. The consequence is a failed (retried) job, never data loss:
// the canonical store is authoritative and the object is fetched again.

func (c *Cache) leaseDir(pid int) string {
	return filepath.Join(c.Dir, "leases", strconv.Itoa(pid))
}

// Acquire marks an object as in use until the returned release function is
// called (calling it more than once is harmless). It is safe on a nil Cache.
func (c *Cache) Acquire(sha string) (release func()) {
	if c == nil || !validSHA(sha) {
		return func() {}
	}
	c.lmu.Lock()
	if c.refs == nil {
		c.refs = map[string]int{}
	}
	c.refs[sha]++
	if c.refs[sha] == 1 {
		dir := c.leaseDir(os.Getpid())
		if err := os.MkdirAll(dir, 0o755); err == nil {
			_ = os.WriteFile(filepath.Join(dir, sha), nil, 0o644)
		}
	}
	c.lmu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			c.lmu.Lock()
			defer c.lmu.Unlock()
			c.refs[sha]--
			if c.refs[sha] <= 0 {
				delete(c.refs, sha)
				_ = os.Remove(filepath.Join(c.leaseDir(os.Getpid()), sha))
			}
		})
	}
}

// InUse returns every object currently leased by a live process on this
// machine (including this one). Stale lease directories are cleaned up.
func (c *Cache) InUse() map[string]bool {
	out := map[string]bool{}
	if c == nil {
		return out
	}
	c.lmu.Lock()
	for sha := range c.refs {
		out[sha] = true
	}
	c.lmu.Unlock()
	entries, err := os.ReadDir(filepath.Join(c.Dir, "leases"))
	if err != nil {
		return out
	}
	self := os.Getpid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || !e.IsDir() || pid == self {
			continue
		}
		dir := c.leaseDir(pid)
		if !ProcessAlive(pid) {
			_ = os.RemoveAll(dir)
			continue
		}
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			if validSHA(f.Name()) {
				out[f.Name()] = true
			}
		}
	}
	return out
}

// leasedNow re-checks one object right before it is deleted.
func (c *Cache) leasedNow(sha string) bool {
	c.lmu.Lock()
	n := c.refs[sha]
	c.lmu.Unlock()
	if n > 0 {
		return true
	}
	entries, err := os.ReadDir(filepath.Join(c.Dir, "leases"))
	if err != nil {
		return false
	}
	self := os.Getpid()
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == self {
			continue
		}
		if _, err := os.Stat(filepath.Join(c.leaseDir(pid), sha)); err == nil && ProcessAlive(pid) {
			return true
		}
	}
	return false
}

type scopeKey struct{}

// scope collects the leases taken by Ensure calls made with its context.
type scope struct {
	mu   sync.Mutex
	held map[*Cache]map[string]func()
	done bool
}

// Hold returns a context under which every Ensure call keeps its object leased
// until release is called. Workers wrap each job in one, so nothing a job is
// rendering from can be pruned mid-job; the ops registry wraps each operation.
// It is safe on a nil Cache.
func (c *Cache) Hold(ctx context.Context) (context.Context, func()) {
	s := &scope{held: map[*Cache]map[string]func(){}}
	return context.WithValue(ctx, scopeKey{}, s), s.release
}

func (s *scope) hold(c *Cache, sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	m := s.held[c]
	if m == nil {
		m = map[string]func(){}
		s.held[c] = m
	}
	if _, ok := m[sha]; !ok {
		m[sha] = c.Acquire(sha)
	}
}

func (s *scope) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = true
	for _, m := range s.held {
		for _, rel := range m {
			rel()
		}
	}
	s.held = map[*Cache]map[string]func(){}
}

func scopeFrom(ctx context.Context) *scope {
	s, _ := ctx.Value(scopeKey{}).(*scope)
	return s
}
