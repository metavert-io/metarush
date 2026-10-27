package cache

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/media"
)

func newCache(t *testing.T, maxBytes int64) *Cache {
	t.Helper()
	c, err := New(t.TempDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	c.MaxBytes = maxBytes
	return c
}

func randomFile(t *testing.T, size int) (string, string) {
	t.Helper()
	b := make([]byte, size)
	_, _ = rand.Read(b)
	p := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	return p, sha
}

// addObject adopts a random object and backdates its last use.
func addObject(t *testing.T, c *Cache, size int, lastUse time.Time) string {
	t.Helper()
	p, sha := randomFile(t, size)
	if _, err := c.Adopt(p, sha); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(c.Path(sha), lastUse, lastUse); err != nil {
		t.Fatal(err)
	}
	return sha
}

func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Skip("cannot spawn a short-lived process")
	}
	return cmd.Process.Pid
}

func TestPruneIsLRUAndKeepsPinnedAndInUse(t *testing.T) {
	c := newCache(t, 0)
	base := time.Now().Add(-10 * time.Hour)
	var shas []string
	for i := 0; i < 5; i++ { // shas[0] is the least recently used
		shas = append(shas, addObject(t, c, 1000, base.Add(time.Duration(i)*time.Hour)))
	}
	if err := c.Pin(shas[0]); err != nil {
		t.Fatal(err)
	}
	release := c.Acquire(shas[1])
	// 5000 bytes cached; prune to 3000 must evict the two oldest evictable: 2 and 3.
	r, err := c.PruneTo(3000, true)
	if err != nil || r.Removed != 2 || r.SkippedPinned != 1 || r.SkippedInUse != 1 {
		t.Fatalf("dry run: %+v %v", r, err)
	}
	for _, s := range shas {
		if !c.Has(s, 1000) {
			t.Fatal("dry run removed something")
		}
	}
	r, _ = c.PruneTo(3000, false)
	if r.Removed != 2 || r.FreedBytes != 2000 || r.AfterBytes != 3000 || r.BeforeBytes != 5000 {
		t.Fatalf("prune: %+v", r)
	}
	for i, want := range []bool{true, true, false, false, true} {
		if c.Has(shas[i], 1000) != want {
			t.Fatalf("object %d present=%v, want %v (LRU order, pins, leases)", i, !want, want)
		}
	}
	// Once released, the in-use object is evictable; the pinned one never is.
	release()
	release() // idempotent
	r, _ = c.PruneTo(0, false)
	if c.Has(shas[1], 1000) || !c.Has(shas[0], 1000) || r.SkippedPinned != 1 {
		t.Fatalf("after release: %+v", r)
	}
	st, _ := c.Status()
	if st.Objects != 1 || st.Pinned != 1 || st.PinnedBytes != 1000 || st.InUse != 0 {
		t.Fatalf("status %+v", st)
	}
}

func TestUseUpdatesLRU(t *testing.T) {
	c := newCache(t, 0)
	old := time.Now().Add(-5 * time.Hour)
	a := addObject(t, c, 500, old)
	b := addObject(t, c, 500, old.Add(time.Hour))
	fsStore, _ := media.NewFS(t.TempDir(), false)
	// Ensure on a cache hit (what a worker does) makes a the most recently used.
	if _, err := c.Ensure(context.Background(), fsStore, media.ObjectKey("ws", a), a, 500, nil); err != nil {
		t.Fatal(err)
	}
	r, _ := c.PruneTo(500, false)
	if r.Removed != 1 || c.Has(b, 500) || !c.Has(a, 500) {
		t.Fatalf("Ensure did not count as a use: %+v", r)
	}
	c2 := newCache(t, 0)
	x := addObject(t, c2, 100, old)
	y := addObject(t, c2, 100, old.Add(time.Hour))
	c2.Touch(x) // e.g. the dashboard served it
	_, _ = c2.PruneTo(100, false)
	if !c2.Has(x, 100) || c2.Has(y, 100) {
		t.Fatal("Touch did not count as a use")
	}
}

func TestLeasesFromOtherProcesses(t *testing.T) {
	c := newCache(t, 0)
	old := time.Now().Add(-time.Hour)
	live := addObject(t, c, 100, old)
	stale := addObject(t, c, 100, old)
	mk := func(pid int, sha string) string {
		d := c.leaseDir(pid)
		_ = os.MkdirAll(d, 0o755)
		_ = os.WriteFile(filepath.Join(d, sha), nil, 0o644)
		return d
	}
	mk(os.Getppid(), live) // the test runner: alive
	staleDir := mk(deadPID(t), stale)
	inUse := c.InUse()
	if !inUse[live] || inUse[stale] {
		t.Fatalf("in use %v", inUse)
	}
	if _, err := os.Stat(staleDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("lease directory of an exited process must be removed")
	}
	r, _ := c.PruneTo(0, false)
	if !c.Has(live, 100) || c.Has(stale, 100) || r.SkippedInUse != 1 {
		t.Fatalf("prune with another process's lease: %+v", r)
	}
}

// slowStore fails a download after limit bytes on its first Get.
type slowStore struct {
	media.Store
	mu      sync.Mutex
	limit   int64
	offsets []int64
}

type cutReader struct {
	io.ReadCloser
	left int64
}

func (r *cutReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, errors.New("connection reset")
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, err := r.ReadCloser.Read(p)
	r.left -= int64(n)
	return n, err
}

func (s *slowStore) Get(ctx context.Context, key string, off, n int64) (io.ReadCloser, media.ObjectInfo, error) {
	rc, info, err := s.Store.Get(ctx, key, off, n)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offsets = append(s.offsets, off)
	if err == nil && len(s.offsets) == 1 && s.limit > 0 {
		return &cutReader{rc, s.limit}, info, nil
	}
	return rc, info, err
}

func storeWith(t *testing.T, size int) (*media.FS, string, string) {
	t.Helper()
	st, _ := media.NewFS(t.TempDir(), false)
	p, sha := randomFile(t, size)
	key := media.ObjectKey("ws_t", sha)
	if _, err := st.PutFile(context.Background(), key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	return st, key, sha
}

func TestEnsureResumesAndHoldLeases(t *testing.T) {
	c := newCache(t, 0)
	fsStore, key, sha := storeWith(t, 3<<20+11)
	st := &slowStore{Store: fsStore, limit: 1 << 20}
	if _, err := c.Ensure(context.Background(), st, key, sha, 3<<20+11, nil); err == nil {
		t.Fatal("expected the cut download to fail")
	}
	if got := c.PartialSize(sha); got != 1<<20 {
		t.Fatalf("partial kept %d bytes", got)
	}
	ctx, release := c.Hold(context.Background())
	if _, err := c.Ensure(ctx, st, key, sha, 3<<20+11, nil); err != nil {
		t.Fatal(err)
	}
	if len(st.offsets) != 2 || st.offsets[1] != 1<<20 {
		t.Fatalf("second download started at %v, want resume at 1 MiB", st.offsets)
	}
	if ok, _ := c.Verify(sha); !ok {
		t.Fatal("resumed object failed verification")
	}
	// Held by the scope: the quota pruner must not evict it.
	if r, _ := c.PruneTo(0, false); r.Removed != 0 || r.SkippedInUse != 1 {
		t.Fatalf("held object pruned: %+v", r)
	}
	if b, _ := os.ReadDir(c.leaseDir(os.Getpid())); len(b) != 1 {
		t.Fatalf("lease file missing (%d entries)", len(b))
	}
	release()
	if r, _ := c.PruneTo(0, false); r.Removed != 1 {
		t.Fatalf("released object not pruned: %+v", r)
	}
	if b, _ := os.ReadDir(c.leaseDir(os.Getpid())); len(b) != 0 {
		t.Fatal("lease file left behind")
	}
}

func TestQuotaCheckAndEnforcer(t *testing.T) {
	c := newCache(t, 10_000)
	base := time.Now().Add(-time.Hour)
	var shas []string
	for i := 0; i < 6; i++ {
		shas = append(shas, addObject(t, c, 2000, base.Add(time.Duration(i)*time.Minute)))
	}
	q, err := c.QuotaCheck(time.Minute) // 12000 > 10000 → prune to 9000
	if err != nil || q.OverQuota || q.LastPrune == nil || q.LastPrune.Removed != 2 || q.Bytes != 8000 {
		t.Fatalf("quota check: %+v %v", q, err)
	}
	if c.Has(shas[0], 2000) || c.Has(shas[1], 2000) || !c.Has(shas[2], 2000) {
		t.Fatal("quota pruning must evict least recently used first")
	}
	st, _ := c.Status()
	if st.Quota == nil || !st.Quota.Enforced || st.Quota.LastPrune == nil || st.Quota.EnforcerPID != os.Getpid() {
		t.Fatalf("status quota %+v", st.Quota)
	}
	// Everything pinned: over quota, nothing evictable, and the status says why.
	_ = c.Pin(shas[2:]...)
	addObjectPinned := addObject(t, c, 4000, time.Now())
	_ = c.Pin(addObjectPinned)
	q, _ = c.QuotaCheck(time.Minute)
	if !q.OverQuota || !strings.Contains(q.Note, "kept for offline work") || q.LastPrune.Removed != 2 {
		t.Fatalf("over-quota status: %+v", q)
	}

	// The enforcer loop prunes in the background and logs.
	c2 := newCache(t, 3000)
	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	var logs []string
	done := make(chan struct{})
	// The enforcer stops before the test's folders are removed (it may be writing to them).
	defer func() { cancel(); <-done }()
	go func() {
		defer close(done)
		c2.EnforceQuota(ctx, 50*time.Millisecond, func(f string, a ...any) {
			mu.Lock()
			logs = append(logs, f)
			mu.Unlock()
		})
	}()
	for i := 0; i < 3; i++ {
		addObject(t, c2, 2000, base.Add(time.Duration(i)*time.Minute))
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, _ := c2.Status()
		if st.Bytes <= 3000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("enforcer did not prune: %d bytes", st.Bytes)
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(logs) == 0 || !strings.Contains(strings.Join(logs, "\n"), "evicted") {
		t.Fatalf("enforcer logs: %v", logs)
	}
}

func TestQuotaStatusNotEnforced(t *testing.T) {
	c := newCache(t, 1000)
	if c.ReadQuotaStatus() != nil {
		t.Fatal("no status before any check")
	}
	q := QuotaStatus{EnforcerPID: deadPID(t), IntervalSeconds: 60, LastCheckAt: time.Now()}
	c.writeQuotaStatus(q)
	got := c.ReadQuotaStatus()
	if got == nil || got.Enforced || got.Note == "" {
		t.Fatalf("exited enforcer must not count: %+v", got)
	}
}

func TestRepair(t *testing.T) {
	c := newCache(t, 0)
	now := time.Now()
	good := addObject(t, c, 3000, now)
	bad := addObject(t, c, 3000, now)
	p := c.Path(bad)
	_ = os.Chmod(p, 0o644)
	_ = os.WriteFile(p, []byte(strings.Repeat("x", 3000)), 0o644) // bit rot
	// Stray temp file (abandoned long ago) and a misplaced file.
	stray := c.Path(good) + ".tmp"
	_ = os.WriteFile(stray, []byte("tmp"), 0o644)
	old := now.Add(-time.Hour)
	_ = os.Chtimes(stray, old, old)
	_ = os.WriteFile(filepath.Join(c.Dir, "objects", good[:2], "notes.txt"), []byte("?"), 0o644)
	// Two partial downloads: one abandoned, one being written by a live process.
	_, abandoned := randomFile(t, 10)
	_, active := randomFile(t, 10)
	_ = os.WriteFile(c.partialPath(abandoned), []byte("half"), 0o644)
	_ = os.WriteFile(c.partialPath(active), []byte("half"), 0o644)
	ld := c.leaseDir(os.Getppid())
	_ = os.MkdirAll(ld, 0o755)
	_ = os.WriteFile(filepath.Join(ld, active), nil, 0o644)
	// A pinned object that is missing locally but exists in the canonical store.
	fsStore, key, missing := storeWith(t, 4000)
	_ = c.Pin(missing, good)
	refetch := func(ctx context.Context, sha string) error {
		_, err := c.Ensure(ctx, fsStore, key, sha, -1, nil)
		return err
	}

	dry, err := c.Repair(context.Background(), true, refetch, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dry.Checked != 2 || dry.OK != 1 || dry.Corrupt != 1 || dry.Strays != 2 || dry.Partials != 1 || dry.SkippedInUse != 1 || dry.PinnedMissing != 1 || dry.Refetched != 0 {
		t.Fatalf("dry run: %+v", dry)
	}
	if _, err := os.Stat(c.Path(bad)); err != nil {
		t.Fatal("dry run removed a corrupt object")
	}
	for _, pr := range dry.Problems {
		if !strings.HasPrefix(pr.Action, "would-") && pr.Action != "skipped-in-use" {
			t.Fatalf("dry run action %q", pr.Action)
		}
	}

	var seen int
	r, err := c.Repair(context.Background(), false, refetch, func(i, n int, sha string, size int64) { seen = n })
	if err != nil {
		t.Fatal(err)
	}
	if r.Corrupt != 1 || r.Strays != 2 || r.Partials != 1 || r.SkippedInUse != 1 || r.Refetched != 1 || r.RefetchFailed != 0 || seen != 2 {
		t.Fatalf("repair: %+v", r)
	}
	if _, err := os.Stat(c.Path(bad)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("corrupt object kept")
	}
	if _, err := os.Stat(stray); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stray temp file kept")
	}
	if c.PartialSize(abandoned) != 0 || c.PartialSize(active) == 0 {
		t.Fatal("partial cleanup must keep downloads in progress and remove abandoned ones")
	}
	if ok, _ := c.Verify(missing); !ok || !c.Has(good, 3000) {
		t.Fatal("pinned object not re-fetched / good object lost")
	}
	// A pinned object missing from the canonical store too is reported, not fatal.
	_, gone := randomFile(t, 10)
	_ = c.Pin(gone)
	r, _ = c.Repair(context.Background(), false, func(ctx context.Context, sha string) error {
		return errors.New("object missing from the canonical store")
	}, nil)
	if r.RefetchFailed != 1 || r.Corrupt != 0 || r.OK != 2 {
		t.Fatalf("second repair: %+v", r)
	}
}
