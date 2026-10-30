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
// the computer slept. A test in clockguard_test.go keeps new comparisons with
// this computer's clock out.

// ServerTimeEvery is how long a measured offset is used before the server's
// time is asked again; ServerTimeRetry how long after a measurement that
// failed; ServerTimeJumpWait how long callers wait for the measurement after
// this computer's clock jumped (the last offset may be off by the jump, or
// right, after a sleep) before they go on with the last offset.
var (
	ServerTimeEvery    = time.Minute
	ServerTimeRetry    = 10 * time.Second
	ServerTimeJumpWait = 2 * time.Second
)

const (
	// clockJump is how far this computer's clock may move apart from the time
	// that really passed before the offset is measured again (the clock was
	// set, or the computer slept).
	clockJump = time.Second
	// slowAnswer is the longest round trip whose middle is trusted without
	// asking again: an answer delayed one way puts the middle off by half of it.
	slowAnswer = 500 * time.Millisecond
	// askFor bounds one measurement (a server that takes the connection and
	// never answers).
	askFor = 15 * time.Second
)

// serverClock is a DB's last measurement of the server's time. One caller's
// need starts a measurement and it runs by itself, so a database that went
// away costs no caller anything once an offset is known: only the very first
// measurement, and the moment after this computer's clock jumped, are waited
// for.
type serverClock struct {
	mu     sync.Mutex
	ok     bool
	offset time.Duration // the server's time − this computer's clock
	rtt    time.Duration // the round trip it was measured with
	at     time.Time     // when it was measured (monotonic; zero: measure again)
	wall   time.Time     // this computer's clock at the last mark (no monotonic reading)
	mono   time.Time     // the same moment by the monotonic clock
	// failed is when a measurement last failed, or a caller stopped waiting
	// for one (monotonic); lastErr what the last one said.
	failed  time.Time
	lastErr error
	// running is closed when the measurement under way ends (nil: none).
	running chan struct{}
	// ask is where the server's time comes from (nil: the server; tests give
	// a fake).
	ask func(context.Context) (time.Time, error)
	// asked counts the round trips (tests).
	asked int
}

// Now is the database server's time now, by this computer's clock and the
// measured offset. Before the first measurement works it returns the error;
// after that it never fails and never waits for a database that doesn't
// answer: an offset gone old is measured again behind the caller, and when
// that fails the last one goes on (clocks drift slowly) and the next try waits
// ServerTimeRetry. Only a clock that jumped is waited for, ServerTimeJumpWait
// at most and whatever the caller's deadline, because the last offset may be
// off by the jump.
func (d *DB) Now(ctx context.Context) (time.Time, error) {
	c := &d.clock
	local := Clock
	var giveUp time.Time
	for {
		wall, mono := local().Round(0), time.Now()
		c.mu.Lock()
		if !c.ok {
			wait := c.start(d, local)
			c.mu.Unlock()
			select {
			case <-wait:
			case <-ctx.Done():
				return time.Time{}, ctx.Err()
			}
			c.mu.Lock()
			ok, err := c.ok, c.lastErr
			c.mu.Unlock()
			if !ok {
				return time.Time{}, err
			}
			continue
		}
		now := wall.Add(c.offset).UTC()
		jumped := jumpedSince(c, wall, mono)
		waiting := !c.failed.IsZero() && mono.Sub(c.failed) < ServerTimeRetry
		if waiting || (!jumped && mono.Sub(c.at) < ServerTimeEvery) {
			c.mu.Unlock()
			return now, nil
		}
		wait := c.start(d, local)
		if !jumped {
			c.mu.Unlock()
			return now, nil
		}
		if giveUp.IsZero() {
			giveUp = mono.Add(ServerTimeJumpWait)
		}
		c.mu.Unlock()
		select {
		case <-wait:
		case <-time.After(giveUp.Sub(mono)):
			// The others go on at once from here.
			c.mu.Lock()
			c.failed = time.Now()
			c.mu.Unlock()
			return now, nil
		}
	}
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

// jumpedSince reports whether this computer's clock moved apart from the time
// that really passed since the last mark (c.mu held).
func jumpedSince(c *serverClock, wall, mono time.Time) bool {
	return absDuration(wall.Sub(c.wall)-mono.Sub(c.mono)) >= clockJump
}

// start returns the measurement under way, or starts one (c.mu held).
func (c *serverClock) start(d *DB, local func() time.Time) chan struct{} {
	if c.running != nil {
		return c.running
	}
	done := make(chan struct{})
	c.running = done
	ask := c.ask
	if ask == nil {
		ask = d.ServerNow
	}
	go func() {
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), askFor)
		defer cancel()
		m, err := c.measure(ctx, ask, local)
		c.finish(m, err, local)
	}()
	return done
}

// measurement is one round trip's answer: the offset at its middle.
type measurement struct {
	offset time.Duration
	rtt    time.Duration
	wall   time.Time
	mono   time.Time
}

// measure asks the server its time, and once more at once when the answer was
// slow (the connection is warm by then); the quicker answer counts.
func (c *serverClock) measure(ctx context.Context, ask func(context.Context) (time.Time, error), local func() time.Time) (measurement, error) {
	var best measurement
	for try := 0; try < 2; try++ {
		c.mu.Lock()
		c.asked++
		c.mu.Unlock()
		before, mono := local().Round(0), time.Now()
		server, err := ask(ctx)
		if err != nil {
			if try > 0 {
				return best, nil
			}
			return best, err
		}
		rtt := time.Since(mono)
		middle := before.Add(rtt / 2)
		if try == 0 || rtt < best.rtt {
			best = measurement{offset: server.Sub(middle), rtt: rtt, wall: middle, mono: mono.Add(rtt / 2)}
		}
		if rtt <= slowAnswer {
			break
		}
	}
	return best, nil
}

// finish keeps what a measurement found. A slow answer doesn't replace an
// offset measured at least as well, unless this computer's clock jumped (that
// one may be off by the jump). A failure keeps the offset there is, and takes
// the jump as it stands, so it is waited for once.
func (c *serverClock) finish(m measurement, err error, local func() time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.running = nil
	if err != nil {
		c.failed, c.lastErr = time.Now(), err
		c.at = time.Time{}
		c.wall, c.mono = local().Round(0), time.Now()
		return
	}
	c.failed, c.lastErr = time.Time{}, nil
	if !c.ok || m.rtt <= slowAnswer || m.rtt <= c.rtt || jumpedSince(c, m.wall, m.mono) {
		c.ok, c.offset, c.rtt = true, m.offset, m.rtt
	}
	c.at, c.wall, c.mono = m.mono, m.wall, m.mono
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

// isDate is an expression that holds when a field is a date (a comparison in
// $expr would otherwise take a missing field for one earlier than any date).
func isDate(field string) bson.M {
	return bson.M{"$eq": bson.A{bson.M{"$type": "$" + field}, "date"}}
}

// DueBy is a filter for records whose time field has come by the database
// server's clock (a due time, a retry's wait, an expiry: field ≤ $$NOW).
// Records without the field don't match.
func DueBy(field string) bson.M {
	return bson.M{"$expr": bson.M{"$and": bson.A{isDate(field), bson.M{"$lte": bson.A{"$" + field, "$$NOW"}}}}}
}

// StaleBefore is a filter for records whose time field is older than the
// last while by the database server's clock (a claim or a marker nobody
// renewed: field < $$NOW − within). Records without the field don't match.
func StaleBefore(field string, within time.Duration) bson.M {
	return bson.M{"$expr": bson.M{"$and": bson.A{isDate(field),
		bson.M{"$lt": bson.A{"$" + field, bson.M{"$subtract": bson.A{"$$NOW", within.Milliseconds()}}}},
	}}}
}

// ServerTimeIn is a pipeline expression for the database server's time plus
// d (a due time or an expiry written in an update pipeline).
func ServerTimeIn(d time.Duration) bson.D {
	return bson.D{{Key: "$add", Value: bson.A{"$$NOW", d.Milliseconds()}}}
}
