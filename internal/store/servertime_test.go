package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
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
	// Measured again once the offset is old.
	old := store.ServerTimeEvery
	store.ServerTimeEvery = 20 * time.Millisecond
	t.Cleanup(func() { store.ServerTimeEvery = old })
	time.Sleep(30 * time.Millisecond)
	if _, err := db.Now(ctx); err != nil || db.ServerTimeAsked() != 4 {
		t.Fatalf("an old offset: %v (asked %d)", err, db.ServerTimeAsked())
	}
	// The server doesn't answer: with no offset yet, the error; with one, it
	// goes on.
	down := errors.New("no answer")
	db.FakeServerTime(func(context.Context) (time.Time, error) { return time.Time{}, down })
	if _, err := db.Now(ctx); !errors.Is(err, down) {
		t.Fatalf("never measured, no answer: %v", err)
	}
	db.FakeServerTime(func(context.Context) (time.Time, error) { return time.Now().Add(ahead), nil })
	if _, err := db.Now(ctx); err != nil {
		t.Fatal(err)
	}
	failing := false
	db.FakeServerTime(func(context.Context) (time.Time, error) {
		if failing {
			return time.Time{}, down
		}
		return time.Now().Add(ahead), nil
	})
	if _, err := db.Now(ctx); err != nil {
		t.Fatal(err)
	}
	failing = true
	time.Sleep(30 * time.Millisecond)
	if got, err := db.Now(ctx); err != nil || !near(got, time.Now().Add(ahead), time.Second) {
		t.Fatalf("no answer after a measurement: %v %v", got, err)
	}
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
	if got := ids(store.DueBy("at")); len(got) != 3 || got[0] != "past" || got[1] != "old" || got[2] != "none" {
		t.Fatalf("due: %v", got)
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
