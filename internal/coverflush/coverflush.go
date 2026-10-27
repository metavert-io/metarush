// Package coverflush writes a coverage build's counters as soon as the
// program is asked to stop (D195). A binary built with `go build -cover`
// writes them to GOCOVERDIR when it exits normally; a daemon stopped by a
// signal may never get that far (a second signal, or a script that stops
// waiting and kills it), and the browser runs' coverage of the dashboard's
// server would be lost. In ordinary builds, or without GOCOVERDIR, nothing
// happens.
//
// A coverage build must instrument its main package too (-coverpkg
// ./internal/...,./cmd/...): without it the binary never sets up its
// coverage and writes nothing at all.
package coverflush

import (
	"os"
	"os/signal"
	"runtime/coverage"
	"sync"
)

// Dir is where a coverage build writes its counters ("" when it doesn't).
func Dir() string { return os.Getenv("GOCOVERDIR") }

// Flush writes the counters now. It does nothing outside a coverage build.
func Flush() {
	dir := Dir()
	if dir == "" {
		return
	}
	_ = coverage.WriteMetaDir(dir)
	_ = coverage.WriteCountersDir(dir)
}

// OnSignal writes the counters when one of sigs arrives, until stop is
// called. It only watches: whatever the program does with the signal stays
// the same (call it where the program already handles sigs, so they never
// fall back to the default of ending it at once).
func OnSignal(sigs ...os.Signal) (stop func()) {
	if Dir() == "" {
		return func() {}
	}
	c := make(chan os.Signal, 1)
	signal.Notify(c, sigs...)
	done := make(chan struct{})
	go func() {
		select {
		case <-c:
			Flush()
		case <-done:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(c)
			close(done)
		})
	}
}
