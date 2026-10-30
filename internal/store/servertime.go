package store

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// The database server's time (D335, D360). Computers' clocks differ, so every
// time compared across computers is the server's: a lease, a heartbeat, a
// claim, a due time, a retry's wait, a stale marker, an expiry. Where the
// comparison fits in the query or the update it goes there ($$NOW,
// $currentDate: SeenWithin, LiveLease, DueBy, StaleBefore). Where it doesn't —
// a time the Go side works out before it writes it, or compares with records
// it already read — DB.Now gives the server's time: this computer's clock
// (Clock) corrected by an offset measured with one round trip and measured
// again every ServerTimeEvery, or as soon as this computer's clock jumps or
// the computer slept.

// ServerTimeEvery is how long a measured offset is used before the server's
// time is asked again.
var ServerTimeEvery = time.Minute

// serverClock is a DB's last measurement of the server's time.
type serverClock struct {
	mu     sync.Mutex
	ok     bool
	offset time.Duration // the server's time − this computer's clock
	wall   time.Time     // this computer's clock when measured (no monotonic reading)
	mono   time.Time     // the same moment by the monotonic clock
	// ask is where the server's time comes from (nil: the server; tests give
	// a fake).
	ask func(context.Context) (time.Time, error)
	// asked counts the round trips (tests).
	asked int
}

// clockJump is how far this computer's clock may move apart from the time
// that really passed before the offset is measured again (the clock was set,
// or the computer slept).
const clockJump = time.Second

// Now is the database server's time now, by this computer's clock and the
// measured offset. Before the first measurement works it returns the error;
// when a later one fails it goes on with the last offset (clocks drift
// slowly).
func (d *DB) Now(ctx context.Context) (time.Time, error) {
	c := &d.clock
	wall, mono := Clock().Round(0), time.Now()
	c.mu.Lock()
	ok, offset := c.ok, c.offset
	fresh := ok && mono.Sub(c.mono) < ServerTimeEvery && absDuration(wall.Sub(c.wall)-mono.Sub(c.mono)) < clockJump
	c.mu.Unlock()
	if fresh {
		return wall.Add(offset).UTC(), nil
	}
	off, err := d.measure(ctx)
	if err != nil {
		if ok {
			return wall.Add(offset).UTC(), nil
		}
		return time.Time{}, err
	}
	return Clock().Round(0).Add(off).UTC(), nil
}

// NowOrLocal is Now, or this computer's clock when the server never
// answered (nothing that needs the server's time can be decided then
// anyway).
func (d *DB) NowOrLocal(ctx context.Context) time.Time {
	if now, err := d.Now(ctx); err == nil {
		return now
	}
	return Clock().UTC()
}

// ClockOffset is how far the database server's time was from this
// computer's clock when last measured (positive: the server is ahead); false
// before the first measurement.
func (d *DB) ClockOffset() (time.Duration, bool) {
	d.clock.mu.Lock()
	defer d.clock.mu.Unlock()
	return d.clock.offset, d.clock.ok
}

// measure asks the server its time once and keeps the offset, taken at the
// middle of the round trip.
func (d *DB) measure(ctx context.Context) (time.Duration, error) {
	c := &d.clock
	c.mu.Lock()
	ask := c.ask
	c.asked++
	c.mu.Unlock()
	if ask == nil {
		ask = d.ServerNow
	}
	before, mono := Clock().Round(0), time.Now()
	server, err := ask(ctx)
	if err != nil {
		return 0, err
	}
	half := time.Since(mono) / 2
	wall := before.Add(half)
	offset := server.Sub(wall)
	c.mu.Lock()
	c.ok, c.offset, c.wall, c.mono = true, offset, wall, mono.Add(half)
	c.mu.Unlock()
	return offset, nil
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

// ServerNow is the database server's own time (computers' clocks differ),
// asked now: one round trip. DB.Now is the same without asking every time.
func (d *DB) ServerNow(ctx context.Context) (time.Time, error) {
	var h struct {
		LocalTime time.Time `bson:"localTime"`
	}
	if err := d.DB.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&h); err != nil {
		return time.Time{}, err
	}
	if h.LocalTime.IsZero() {
		return time.Time{}, errors.New("the database didn't say its time")
	}
	return h.LocalTime.UTC(), nil
}

// DueBy is a filter for records whose time field has come by the database
// server's clock (a due time, a retry's wait, an expiry: field ≤ $$NOW).
func DueBy(field string) bson.M {
	return bson.M{"$expr": bson.M{"$lte": bson.A{"$" + field, "$$NOW"}}}
}

// StaleBefore is a filter for records whose time field is older than the
// last while by the database server's clock (a claim or a marker nobody
// renewed: field < $$NOW − within). Records without the field don't match.
func StaleBefore(field string, within time.Duration) bson.M {
	return bson.M{"$expr": bson.M{"$and": bson.A{
		bson.M{"$eq": bson.A{bson.M{"$type": "$" + field}, "date"}},
		bson.M{"$lt": bson.A{"$" + field, bson.M{"$subtract": bson.A{"$$NOW", within.Milliseconds()}}}},
	}}}
}

// ServerTimeIn is a pipeline expression for the database server's time plus
// d (a due time or an expiry written in an update pipeline).
func ServerTimeIn(d time.Duration) bson.D {
	return bson.D{{Key: "$add", Value: bson.A{"$$NOW", d.Milliseconds()}}}
}
