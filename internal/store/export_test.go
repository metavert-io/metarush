package store

import (
	"context"
	"time"
)

// FakeServerTime makes the server-time helper ask f instead of the server
// (tests: a server whose clock differs, or one that doesn't answer).
func (d *DB) FakeServerTime(f func(context.Context) (time.Time, error)) {
	d.clock.mu.Lock()
	defer d.clock.mu.Unlock()
	d.clock.ask, d.clock.ok = f, false
}

// ServerTimeAsked counts the helper's round trips.
func (d *DB) ServerTimeAsked() int {
	d.clock.mu.Lock()
	defer d.clock.mu.Unlock()
	return d.clock.asked
}
