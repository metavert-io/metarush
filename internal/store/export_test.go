package store

import (
	"context"
	"time"
)

// FakeServerTime makes the server-time helper ask f instead of the server
// (tests: a server whose clock differs, or one that doesn't answer), as a DB
// that never measured.
func (d *DB) FakeServerTime(f func(context.Context) (time.Time, error)) {
	d.ServerTimeSettled()
	d.clock.mu.Lock()
	defer d.clock.mu.Unlock()
	d.clock.ask, d.clock.ok, d.clock.failed, d.clock.lastErr = f, false, time.Time{}, nil
}

// SetServerTimeAsk changes what the helper asks and keeps the offset it has
// (a server that stops answering after it did).
func (d *DB) SetServerTimeAsk(f func(context.Context) (time.Time, error)) {
	d.ServerTimeSettled()
	d.clock.mu.Lock()
	defer d.clock.mu.Unlock()
	d.clock.ask = f
}

// ServerTimeAsked counts the helper's round trips.
func (d *DB) ServerTimeAsked() int {
	d.clock.mu.Lock()
	defer d.clock.mu.Unlock()
	return d.clock.asked
}

// ServerTimeSettled waits for the measurement under way, if any.
func (d *DB) ServerTimeSettled() {
	d.clock.mu.Lock()
	running := d.clock.running
	d.clock.mu.Unlock()
	if running != nil {
		<-running
	}
}

// ClockOffset is how far the server's time was from this computer's clock
// when last measured (positive: the server is ahead); false before the first
// measurement.
func (d *DB) ClockOffset() (time.Duration, bool) {
	d.clock.mu.Lock()
	defer d.clock.mu.Unlock()
	return d.clock.offset, d.clock.ok
}
