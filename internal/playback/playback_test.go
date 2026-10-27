package playback

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// The memory's rules (D180) with a fake database and clock; internal/api's
// playback tests play a real outage through the dashboard.

var errDown = errors.New("server selection error: ReplicaSetNoPrimary")

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func testMemory() (*memory, *clock) {
	c := &clock{t: time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)}
	m := newMemory()
	m.now = c.now
	m.lag = time.Millisecond
	return m, c
}

// db is a fake database: what it answers, how often it was asked, and the
// deadline each lookup gave it.
type db struct {
	mu      sync.Mutex
	rec     *Asset
	keep    bool
	err     error
	asked   int
	waits   []time.Duration
	clock   *clock
	block   chan struct{} // when set, reads wait for it
	reading chan struct{}
}

func (d *db) read(ctx context.Context) (any, time.Time, bool, error) {
	d.mu.Lock()
	d.asked++
	if dl, ok := ctx.Deadline(); ok {
		d.waits = append(d.waits, time.Until(dl))
	}
	rec, keep, err, block, reading := d.rec, d.keep, d.err, d.block, d.reading
	d.mu.Unlock()
	if reading != nil {
		reading <- struct{}{}
	}
	if block != nil {
		<-block
	}
	if err != nil {
		return nil, time.Time{}, false, err
	}
	return rec, rec.Version, keep, nil
}

func (d *db) set(rec *Asset, keep bool, err error) {
	d.mu.Lock()
	d.rec, d.keep, d.err = rec, keep, err
	d.mu.Unlock()
}

func (d *db) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.asked
}

func clipAt(version time.Time, proxyKey string) *Asset {
	return &Asset{ID: "ast_1", Title: "Harbour walk", State: models.AssetAvailable, Version: version,
		Variants: map[string]Object{models.VariantProxy: {Key: proxyKey, SHA256: proxyKey, MIME: "video/mp4", Size: 10, Kind: models.VariantProxy}}}
}

var k1 = key{kindAsset, "ast_1"}

func TestMemoryAnswersOnlyWhenTheDatabaseDoesnt(t *testing.T) {
	m, c := testMemory()
	d := &db{clock: c}
	first := clipAt(c.now(), "k1")
	d.set(first, true, nil)
	rec, from, err := m.lookup(context.Background(), k1, d.read)
	if err != nil || from != FromDatabase || rec.(*Asset) != first {
		t.Fatalf("the database answered: %v %v %v", rec, from, err)
	}
	d.set(nil, false, errDown)
	rec, from, err = m.lookup(context.Background(), k1, d.read)
	if err != nil || from != FromMemory || rec.(*Asset) != first {
		t.Fatalf("the database failed: want the remembered record, got %v %v %v", rec, from, err)
	}
	// It came back and says the clip is gone: forgotten at once.
	d.set(nil, false, store.ErrNotFound)
	c.add(Quiet + time.Second)
	if _, _, err := m.lookup(context.Background(), k1, d.read); !errors.Is(err, ErrNotFound) {
		t.Fatalf("gone: %v", err)
	}
	d.set(nil, false, errDown)
	if _, from, err := m.lookup(context.Background(), k1, d.read); !apperr.Is(err, apperr.Database) || from != FromDatabase {
		t.Fatalf("a forgotten clip in an outage: %v %v, want E_DATABASE", from, err)
	}
}

func TestMemoryWaitsLittleAndOneAsksForAll(t *testing.T) {
	m, c := testMemory()
	d := &db{clock: c}
	d.set(clipAt(c.now(), "k1"), true, nil)
	other := key{kindAsset, "ast_2"}
	_, _, _ = m.lookup(context.Background(), k1, d.read) // nothing remembered: Wait
	_, _, _ = m.lookup(context.Background(), k1, d.read) // remembered: QuickWait
	d.set(nil, false, errDown)
	_, _, _ = m.lookup(context.Background(), k1, d.read) // fails: falls back on the memory
	_, _, _ = m.lookup(context.Background(), other, d.read)
	d.mu.Lock()
	waits := append([]time.Duration(nil), d.waits...)
	d.mu.Unlock()
	want := []time.Duration{Wait, QuickWait, QuickWait, DownWait}
	if len(waits) != len(want) {
		t.Fatalf("deadlines %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] > want[i] || waits[i] < want[i]-250*time.Millisecond {
			t.Errorf("lookup %d waited up to %s, want %s", i+1, waits[i], want[i])
		}
	}
	// Within the quiet time the memory answers without asking.
	n := d.count()
	for range 5 {
		if _, from, err := m.lookup(context.Background(), k1, d.read); err != nil || from != FromMemory {
			t.Fatalf("quiet time: %v %v", from, err)
		}
	}
	if d.count() != n {
		t.Fatalf("asked the database %d times during the quiet time", d.count()-n)
	}
	// Then one lookup asks for everyone; the others keep using the memory.
	c.add(Quiet)
	block := make(chan struct{})
	reading := make(chan struct{}, 1)
	d.mu.Lock()
	d.block, d.reading = block, reading
	d.mu.Unlock()
	done := make(chan struct{})
	go func() {
		_, _, _ = m.lookup(context.Background(), k1, d.read)
		close(done)
	}()
	<-reading
	d.mu.Lock()
	d.block, d.reading = nil, nil
	d.mu.Unlock()
	for range 3 {
		if _, from, err := m.lookup(context.Background(), k1, d.read); err != nil || from != FromMemory {
			t.Fatalf("while one asks: %v %v", from, err)
		}
	}
	close(block)
	<-done
	if got := d.count() - n; got != 1 {
		t.Fatalf("after the quiet time the database was asked %d times, want once", got)
	}
}

func TestAnAnswerFromBeforeAChangeIsNotKept(t *testing.T) {
	m, c := testMemory()
	d := &db{clock: c}
	d.set(clipAt(c.now(), "old"), true, nil)
	block := make(chan struct{})
	reading := make(chan struct{}, 1)
	d.block, d.reading = block, reading
	done := make(chan struct{})
	go func() {
		_, _, _ = m.lookup(context.Background(), k1, d.read)
		close(done)
	}()
	<-reading
	c.add(time.Millisecond)
	m.forget(k1) // moved to the Trash while the old record was on its way
	c.add(time.Millisecond)
	close(block)
	<-done
	if _, ok := m.recall(k1); ok {
		t.Fatal("an answer read before the change was remembered")
	}
}

func TestAnOlderVersionDoesntReplaceANewerOne(t *testing.T) {
	m, c := testMemory()
	d := &db{clock: c}
	newer := clipAt(c.now(), "new")
	d.set(newer, true, nil)
	_, _, _ = m.lookup(context.Background(), k1, d.read)
	c.add(time.Second)
	d.set(clipAt(c.now().Add(-time.Hour), "old"), true, nil) // a member that lags behind
	rec, _, err := m.lookup(context.Background(), k1, d.read)
	if err != nil || rec.(*Asset) != newer {
		t.Fatalf("a lagging member's older record was used: %v %v", rec, err)
	}
	if r, _ := m.recall(k1); r.(*Asset) != newer {
		t.Fatal("the older record replaced the newer one")
	}
}

func TestOldMemoriesAndTheTrashAreNotUsed(t *testing.T) {
	m, c := testMemory()
	d := &db{clock: c}
	d.set(clipAt(c.now(), "k1"), true, nil)
	_, _, _ = m.lookup(context.Background(), k1, d.read)
	c.add(MaxAge + time.Second)
	if _, ok := m.recall(k1); ok {
		t.Fatal("what the database said over an hour ago was used")
	}
	// In the Trash: served as the database says, never remembered.
	d.set(clipAt(c.now(), "k1"), false, nil)
	if _, from, err := m.lookup(context.Background(), k1, d.read); err != nil || from != FromDatabase {
		t.Fatalf("a clip in the Trash: %v %v", from, err)
	}
	if _, ok := m.recall(k1); ok {
		t.Fatal("a clip in the Trash was remembered")
	}
}

func TestMemoryHoldsTheMostRecentlyUsed(t *testing.T) {
	m, c := testMemory()
	m.capacity = 3
	for _, id := range []string{"a", "b", "c"} {
		m.keep(key{kindAsset, id}, clipAt(c.now(), id), c.now(), c.now().Add(-time.Second))
	}
	m.recall(key{kindAsset, "a"}) // used: b is now the oldest
	m.keep(key{kindAsset, "d"}, clipAt(c.now(), "d"), c.now(), c.now().Add(-time.Second))
	for id, want := range map[string]bool{"a": true, "b": false, "c": true, "d": true} {
		if _, ok := m.recall(key{kindAsset, id}); ok != want {
			t.Errorf("%s remembered %v, want %v", id, ok, want)
		}
	}
}

func TestAChangeIsReadAgain(t *testing.T) {
	m, c := testMemory()
	var asked atomic.Int32
	newer := clipAt(c.now().Add(time.Minute), "new")
	answer := func(rec *Asset, keep bool, err error) reader {
		return func(context.Context) (any, time.Time, bool, error) {
			asked.Add(1)
			if err != nil {
				return nil, time.Time{}, false, err
			}
			return rec, rec.Version, keep, nil
		}
	}
	m.keep(k1, clipAt(c.now(), "old"), c.now(), c.now().Add(-time.Second))
	c.add(time.Second)
	if !m.forget(k1) {
		t.Fatal("forget: it was remembered")
	}
	c.add(time.Second)
	m.rereadSoon(k1, answer(newer, true, nil))
	waitFor(t, func() bool { r, ok := m.recall(k1); return ok && r.(*Asset) == newer })
	// Deleted (or moved to the Trash, or the database didn't answer): it stays forgotten.
	for _, rd := range []reader{answer(nil, false, store.ErrNotFound), answer(newer, false, nil), answer(nil, false, errDown)} {
		m.forget(k1)
		c.add(time.Second)
		before := asked.Load()
		m.rereadSoon(k1, rd)
		waitFor(t, func() bool { return asked.Load() > before && !m.rereading(k1) })
		if _, ok := m.recall(k1); ok {
			t.Fatal("a change that wasn't confirmed brought the old record back")
		}
	}
}

func TestTheOneAskingWentAwayIsNoOutage(t *testing.T) {
	m, c := testMemory()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := m.lookup(ctx, k1, func(ctx context.Context) (any, time.Time, bool, error) {
		return nil, time.Time{}, false, ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	m.mu.Lock()
	failed := !m.failedAt.IsZero()
	m.mu.Unlock()
	if failed {
		t.Fatal("a browser that stopped asking was taken for a database outage")
	}
	_ = c
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
