package store_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/dblink"
)

func near(a, b time.Time, within time.Duration) bool {
	d := a.Sub(b)
	return d < within && d > -within
}

// A computer whose clock is ten minutes fast or slow still gets the database
// server's time, and the offset it measured says how far off it is.
func TestNowIsTheServersTimeWhateverThisClockSays(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	for _, skew := range []time.Duration{10 * time.Minute, -10 * time.Minute, 0} {
		restore := testutil.SkewClock(t, skew)
		got, err := db.Now(ctx)
		if err != nil {
			t.Fatal(err)
		}
		server, err := db.ServerNow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !near(got, server, 2*time.Second) {
			t.Fatalf("clock %v off: Now %v, the server %v", skew, got, server)
		}
		if off, ok := db.ClockOffset(); !ok || !near(time.Time{}.Add(off), time.Time{}.Add(-skew), 2*time.Second) {
			t.Fatalf("clock %v off: offset %v %v", skew, off, ok)
		}
		restore()
	}
}

// The server is asked once a while, again as soon as this computer's clock
// jumps (set, or the computer slept), and when it doesn't answer the last
// offset goes on; with no offset yet the error says so.
func TestNowAsksOnceAWhile(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	ahead := 7 * time.Minute
	db.FakeServerTime(func(context.Context) (time.Time, error) { return time.Now().Add(ahead), nil })
	for i := 0; i < 5; i++ {
		got, err := db.Now(ctx)
		if err != nil || !near(got, time.Now().Add(ahead), time.Second) {
			t.Fatalf("a server seven minutes ahead: %v %v", got, err)
		}
	}
	if n := db.ServerTimeAsked(); n != 1 {
		t.Fatalf("asked %d times, want once", n)
	}
	// This computer's clock is set ten minutes on: asked again, and the
	// server's time stays the server's.
	restore := testutil.SkewClock(t, 10*time.Minute)
	if got, _ := db.Now(ctx); !near(got, time.Now().Add(ahead), time.Second) || db.ServerTimeAsked() != 2 {
		t.Fatalf("after the clock jumped: %v (asked %d)", got, db.ServerTimeAsked())
	}
	restore()
	if got, _ := db.Now(ctx); !near(got, time.Now().Add(ahead), time.Second) || db.ServerTimeAsked() != 3 {
		t.Fatalf("after the clock jumped back: %v (asked %d)", got, db.ServerTimeAsked())
	}
	// Measured again once the offset is old, behind the caller.
	shorten(t, &store.ServerTimeEvery, 20*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	if _, err := db.Now(ctx); err != nil {
		t.Fatalf("an old offset: %v", err)
	}
	db.ServerTimeSettled()
	if n := db.ServerTimeAsked(); n != 4 {
		t.Fatalf("an old offset: asked %d times, want 4", n)
	}
	// The server doesn't answer: with no offset yet, the error.
	down := errors.New("no answer")
	db.FakeServerTime(func(context.Context) (time.Time, error) { return time.Time{}, down })
	if _, err := db.Now(ctx); !errors.Is(err, down) {
		t.Fatalf("never measured, no answer: %v", err)
	}
}

// shorten sets one of the helper's timings for a test.
func shorten(t *testing.T, v *time.Duration, to time.Duration) {
	t.Helper()
	old := *v
	*v = to
	t.Cleanup(func() { *v = old })
}

// frozen is a server that takes the question and answers only when told.
type frozen struct {
	release chan struct{}
	err     error
}

func (f *frozen) ask(ctx context.Context) (time.Time, error) {
	select {
	case <-f.release:
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	}
	if f.err != nil {
		return time.Time{}, f.err
	}
	return time.Now(), nil
}

// allAtOnce calls Now from n callers and says how long the slowest took.
func allAtOnce(db *store.DB, n int) (took time.Duration, errs []error) {
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			began := time.Now()
			_, err := db.Now(context.Background())
			mu.Lock()
			defer mu.Unlock()
			errs = append(errs, err)
			took = max(took, time.Since(began))
		}()
	}
	wg.Wait()
	return took, errs
}

// Once an offset is known nobody waits for a database that went away: an old
// offset is measured again behind the callers, one measurement however many
// ask, and after it fails nobody asks for a while. Every caller goes on with
// the last offset at once.
func TestNobodyWaitsForADatabaseThatWentAway(t *testing.T) {
	db := testutil.DB(t)
	db.FakeServerTime(func(context.Context) (time.Time, error) { return time.Now(), nil })
	if _, err := db.Now(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Frozen: 20 callers, none waits, one question.
	f := &frozen{release: make(chan struct{}), err: errors.New("no answer")}
	db.SetServerTimeAsk(f.ask)
	asked := db.ServerTimeAsked()
	shorten(t, &store.ServerTimeEvery, time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	took, errs := allAtOnce(db, 20)
	if took > 500*time.Millisecond {
		t.Fatalf("20 callers with a frozen database: the slowest took %v", took)
	}
	for _, err := range errs {
		if err != nil {
			t.Fatalf("with an offset known: %v", err)
		}
	}
	if n := db.ServerTimeAsked() - asked; n != 1 {
		t.Fatalf("20 callers asked %d times, want once", n)
	}

	// It fails: for a while nobody asks.
	shorten(t, &store.ServerTimeRetry, 300*time.Millisecond)
	close(f.release)
	db.ServerTimeSettled()
	asked = db.ServerTimeAsked()
	if took, _ := allAtOnce(db, 20); took > 100*time.Millisecond || db.ServerTimeAsked() != asked {
		t.Fatalf("after a failure: the slowest took %v, asked %d more", took, db.ServerTimeAsked()-asked)
	}
	time.Sleep(350 * time.Millisecond)
	if _, err := db.Now(context.Background()); err != nil {
		t.Fatal(err)
	}
	db.ServerTimeSettled()
	if db.ServerTimeAsked() != asked+1 {
		t.Fatalf("after the wait: asked %d more, want one", db.ServerTimeAsked()-asked)
	}
}

// The very first measurement is the one thing to wait for: everybody waits
// for the same one and gets its answer; one caller's deadline ends that
// caller's wait only.
func TestEveryoneWaitsForTheSameFirstMeasurement(t *testing.T) {
	db := testutil.DB(t)
	f := &frozen{release: make(chan struct{})}
	db.FakeServerTime(f.ask)
	short, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := db.Now(short); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a caller's own deadline: %v", err)
	}
	time.AfterFunc(100*time.Millisecond, func() { close(f.release) })
	took, errs := allAtOnce(db, 20)
	if took < 50*time.Millisecond {
		t.Fatalf("the callers didn't wait for the first measurement (%v)", took)
	}
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := db.ServerTimeAsked(); n != 1 {
		t.Fatalf("asked %d times, want once", n)
	}

	// It fails: each one that waited hears why, and the next tries again.
	down := errors.New("no answer")
	f = &frozen{release: make(chan struct{}), err: down}
	db.FakeServerTime(f.ask)
	asked := db.ServerTimeAsked()
	time.AfterFunc(50*time.Millisecond, func() { close(f.release) })
	_, errs = allAtOnce(db, 20)
	for _, err := range errs {
		if !errors.Is(err, down) {
			t.Fatalf("never measured, no answer: %v", err)
		}
	}
	if n := db.ServerTimeAsked() - asked; n > 2 {
		t.Fatalf("20 callers asked %d times", n)
	}
	db.FakeServerTime(func(context.Context) (time.Time, error) { return time.Now(), nil })
	if _, err := db.Now(context.Background()); err != nil {
		t.Fatalf("the next call tries again: %v", err)
	}
}

// A slow answer's middle can be off by half its round trip, so it is asked
// again at once on the warm connection; if that is slow too it doesn't replace
// an offset measured better, unless this computer's clock jumped.
func TestASlowAnswerIsAskedAgain(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	// The answer's delay is all on the way back: its time is taken at once.
	slowFor := func(tries *int, slow int, by time.Duration) func(context.Context) (time.Time, error) {
		return func(context.Context) (time.Time, error) {
			*tries++
			at := time.Now()
			if *tries <= slow {
				time.Sleep(by)
			}
			return at, nil
		}
	}
	offset := func() time.Duration {
		t.Helper()
		off, ok := db.ClockOffset()
		if !ok {
			t.Fatal("no offset")
		}
		return off
	}
	within := func(what string, d, limit time.Duration) {
		t.Helper()
		if d > limit || d < -limit {
			t.Fatalf("%s: off by %v", what, d)
		}
	}
	const by = 700 * time.Millisecond
	// A cold connection's slow answer, then a quick one.
	tries := 0
	db.FakeServerTime(slowFor(&tries, 1, by))
	if _, err := db.Now(ctx); err != nil {
		t.Fatal(err)
	}
	if tries != 2 {
		t.Fatalf("asked %d times, want again after a slow answer", tries)
	}
	within("a slow answer, then a quick one", offset(), 100*time.Millisecond)

	// Later both are slow: the offset measured well stays.
	shorten(t, &store.ServerTimeEvery, 20*time.Millisecond)
	time.Sleep(30 * time.Millisecond)
	tries = 0
	db.SetServerTimeAsk(slowFor(&tries, 2, by))
	if _, err := db.Now(ctx); err != nil {
		t.Fatal(err)
	}
	db.ServerTimeSettled()
	if tries != 2 {
		t.Fatalf("asked %d times, want 2", tries)
	}
	within("two slow answers and a better offset", offset(), 100*time.Millisecond)

	// Never measured, both slow: the quicker is better than nothing.
	store.ServerTimeEvery = time.Minute
	tries = 0
	db.FakeServerTime(slowFor(&tries, 2, by))
	if _, err := db.Now(ctx); err != nil {
		t.Fatal(err)
	}
	if off := offset(); off > -by/4 {
		t.Fatalf("two slow answers and no offset yet: %v, want about %v", off, -by/2)
	}
}

// After this computer's clock jumped, the last offset may be off by the jump:
// the measurement is waited for, a moment at most; with a database that
// doesn't answer, one caller waits that moment and the rest go on at once.
func TestAJumpIsWaitedForAMomentAtMost(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	db.FakeServerTime(func(context.Context) (time.Time, error) { return time.Now(), nil })
	if _, err := db.Now(ctx); err != nil {
		t.Fatal(err)
	}
	f := &frozen{release: make(chan struct{}), err: errors.New("no answer")}
	db.SetServerTimeAsk(f.ask)
	testutil.SkewClock(t, 10*time.Minute)
	began := time.Now()
	if _, err := db.Now(ctx); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(began); took < time.Second || took > 3*time.Second {
		t.Fatalf("the wait after a jump took %v", took)
	}
	if took, _ := allAtOnce(db, 20); took > 100*time.Millisecond {
		t.Fatalf("after one caller's wait the rest took %v", took)
	}
	close(f.release)
	db.ServerTimeSettled()
	if took, _ := allAtOnce(db, 20); took > 100*time.Millisecond {
		t.Fatalf("after the measurement failed: %v", took)
	}
}

// A real database cut in the middle: the helper's callers don't notice, and it
// picks up again when the database is back.
func TestNowThroughACutDatabase(t *testing.T) {
	name := testutil.DB(t).Name
	link := dblink.Start(t)
	ctx := context.Background()
	db, err := store.Connect(ctx, link.URI(), name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close(ctx) })
	testutil.SkewClock(t, 10*time.Minute)
	if _, err := db.Now(ctx); err != nil {
		t.Fatal(err)
	}
	shorten(t, &store.ServerTimeEvery, 10*time.Millisecond)
	shorten(t, &store.ServerTimeRetry, 50*time.Millisecond)
	link.Cut()
	time.Sleep(20 * time.Millisecond)
	for i := 0; i < 50; i++ {
		began := time.Now()
		got, err := db.Now(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if took := time.Since(began); took > 200*time.Millisecond {
			t.Fatalf("a cut database made a caller wait %v", took)
		}
		if !near(got, time.Now(), 2*time.Second) {
			t.Fatalf("through the cut: %v, want about %v", got, time.Now())
		}
		time.Sleep(10 * time.Millisecond)
	}
	link.Restore()
}

// The query helpers compare with the server's own clock: due, stale and a
// time written as the server's now plus a while.
func TestServerTimeFilters(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	restore := testutil.SkewClock(t, -10*time.Minute) // this computer's clock means nothing here
	defer restore()
	server, err := db.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := db.C("servertime_test")
	docs := []any{
		bson.M{"_id": "past", "at": server.Add(-time.Minute)},
		bson.M{"_id": "soon", "at": server.Add(time.Minute)},
		bson.M{"_id": "old", "at": server.Add(-time.Hour)},
		bson.M{"_id": "none"},
	}
	if _, err := c.InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}
	ids := func(filter bson.M) []string {
		t.Helper()
		cur, err := c.Find(ctx, filter)
		if err != nil {
			t.Fatal(err)
		}
		var out []struct {
			ID string `bson:"_id"`
		}
		if err := cur.All(ctx, &out); err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, o := range out {
			s = append(s, o.ID)
		}
		return s
	}
	if got := ids(store.DueBy("at")); len(got) != 2 || got[0] != "past" || got[1] != "old" {
		t.Fatalf("due, and only records that have the time: %v", got)
	}
	if got := ids(store.StaleBefore("at", 30*time.Minute)); len(got) != 1 || got[0] != "old" {
		t.Fatalf("stale: %v", got)
	}
	if _, err := c.UpdateOne(ctx, bson.M{"_id": "none"}, mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "at", Value: store.ServerTimeIn(5 * time.Minute)}}}}}); err != nil {
		t.Fatal(err)
	}
	var d struct {
		At time.Time `bson:"at"`
	}
	if err := c.FindOne(ctx, bson.M{"_id": "none"}).Decode(&d); err != nil || !near(d.At, server.Add(5*time.Minute), 3*time.Second) {
		t.Fatalf("written as the server's now plus five minutes: %v %v", d.At, err)
	}
}
