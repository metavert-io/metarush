package ingest

import (
	"context"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
)

// WatchManager runs the daemon's watched folder and swaps it when the
// configuration changes (hot reload: no daemon restart).
type WatchManager struct {
	App *app.App
	Log func(format string, a ...any)
	// Disabled is set by `serve --no-watch`: configuration is tracked but nothing runs.
	Disabled bool
	// OnChange is called after the watcher starts, stops or changes.
	OnChange func()

	parent context.Context
	mu     sync.Mutex
	cfg    config.Watch
	w      *Watcher
	cancel context.CancelFunc
	done   chan struct{}
}

// Live is the running daemon's watch manager in this process (nil in the CLI);
// watch ops use it to apply changes at once and report live status.
var Live *WatchManager

// NewWatchManager creates a manager bound to ctx (the daemon's lifetime).
func NewWatchManager(ctx context.Context, a *app.App, log func(string, ...any)) *WatchManager {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &WatchManager{App: a, Log: log, parent: ctx}
}

// Apply starts, stops or restarts the watcher for cfg. Unchanged config is a no-op.
func (m *WatchManager) Apply(cfg config.Watch) {
	m.mu.Lock()
	running := m.w != nil
	same := running && m.cfg == cfg
	m.cfg = cfg
	if same {
		m.mu.Unlock()
		return
	}
	cancel, done := m.cancel, m.done
	m.w, m.cancel, m.done = nil, nil, nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second): // an ingest in progress finishes in the background
		}
	}
	if cfg.Enabled && cfg.Dir != "" && !m.Disabled {
		stable := time.Duration(cfg.StableSeconds) * time.Second
		w := &Watcher{App: m.App, Dir: cfg.Dir, StableFor: stable, Move: cfg.MoveProcessed, ProjectID: cfg.ProjectID, Log: m.Log}
		ctx, c := context.WithCancel(m.parent)
		d := make(chan struct{})
		m.mu.Lock()
		m.w, m.cancel, m.done = w, c, d
		m.mu.Unlock()
		go func() {
			defer close(d)
			_ = w.Run(ctx)
		}()
		m.Log("watch: watching %s", cfg.Dir)
	} else if running {
		m.Log("watch: stopped")
	}
	if m.OnChange != nil {
		m.OnChange()
	}
}

// Restart restarts the watcher with the same configuration (after the skip
// marks changed, for example).
func (m *WatchManager) Restart() {
	m.mu.Lock()
	cfg := m.cfg
	m.cfg = config.Watch{} // force Apply to treat it as a change
	m.mu.Unlock()
	m.Apply(cfg)
}

// Config returns the configuration the manager is applying.
func (m *WatchManager) Config() config.Watch {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// Snapshot returns the running watcher's status (ok=false when none runs).
func (m *WatchManager) Snapshot() (WatchStatus, bool) {
	m.mu.Lock()
	w := m.w
	m.mu.Unlock()
	if w == nil {
		return WatchStatus{}, false
	}
	return w.Snapshot(), true
}

// Running reports whether a watcher is active.
func (m *WatchManager) Running() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.w != nil
}
