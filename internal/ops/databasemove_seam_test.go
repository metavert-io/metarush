package ops

import (
	"context"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/store"
)

// SetSwitchFails makes a move's own switch fail after the old database was
// marked moved (a switch that doesn't hold); the returned func undoes it.
func SetSwitchFails(err error) func() {
	old := switchFails
	switchFails = func() error { return err }
	return func() { switchFails = old }
}

// KeepLookAddressForTest replaces the old database's address kept for a
// move's looks (a test points it at a server that doesn't answer).
func KeepLookAddressForTest(cfg *config.Config, move, uri string) error {
	return keepLookAddress(cfg, move, uri)
}

// SetLateKeepPerLook lowers how many records found for the first time one
// look keeps; the returned func undoes it.
func SetLateKeepPerLook(n int) func() {
	old := lateKeepPerLook
	lateKeepPerLook = n
	return func() { lateKeepPerLook = old }
}

// SetLateBackgroundBytes lowers how much a look in the background reads; the
// returned func undoes it.
func SetLateBackgroundBytes(n int64) func() {
	old := lateBackground.MaxBytes
	lateBackground.MaxBytes = n
	return func() { lateBackground.MaxBytes = old }
}

// SetLateCarryHook runs f after a carry-over read a record and before it
// claims it (a look racing it); the returned func undoes it.
func SetLateCarryHook(f func(id string)) func() {
	old := lateCarryHook
	lateCarryHook = func(r lateWriteRec) { f(r.ID) }
	return func() { lateCarryHook = old }
}

// SetServerHashesWork plays a database server that does (or doesn't) say its
// own hashes; the returned func undoes it.
func SetServerHashesWork(ok bool) func() {
	old := lateServerHashesWork
	lateServerHashesWork = func(context.Context, *store.DB) bool { return ok }
	return func() { lateServerHashesWork = old }
}

// SetLateKeepPerMove lowers how many records one move keeps; the returned
// func undoes it.
func SetLateKeepPerMove(n int) func() {
	old := lateKeepPerMove
	lateKeepPerMove = n
	return func() { lateKeepPerMove = old }
}

// SetLateLookAfter replaces when the looks after a move are due; the returned
// func undoes it.
func SetLateLookAfter(d []time.Duration) func() {
	old := LateLookAfter
	LateLookAfter = d
	return func() { LateLookAfter = old }
}
