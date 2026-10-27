package logbook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// RunDaemon does the long-running process's share of the log until ctx ends:
// it writes spooled entries (every 30 s), logs finished Claude Code terminal
// turns (every 5 s), writes the dashboard-read counts (every 5 min), asks the
// secret sources (provider keys) for their values and refreshes the hook's
// secret fingerprints and, only when log.retentionDays is set, removes this
// machine's entries older than that (hourly).
func (l *Logger) RunDaemon(ctx context.Context, logf func(string, ...any)) {
	if l == nil {
		return
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	l.mu.Lock()
	l.daemon = true
	l.mu.Unlock()
	l.refreshSecrets()
	l.refreshFingerprints()
	spool := time.NewTicker(30 * time.Second)
	hooks := time.NewTicker(5 * time.Second)
	agg := time.NewTicker(aggregateEvery)
	hourly := time.NewTicker(time.Hour)
	defer spool.Stop()
	defer hooks.Stop()
	defer agg.Stop()
	defer hourly.Stop()
	flushSpool := func() {
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		if n, err := l.FlushSpool(c, 20); n > 0 {
			logf("activity log: wrote %d entries saved while the database was unreachable", n)
		} else if err != nil && l.Spooled().Entries > 0 {
			_ = err // still unreachable: try again later
		}
	}
	flushSpool()
	l.retention(ctx, logf)
	for {
		select {
		case <-ctx.Done():
			return
		case <-spool.C:
			flushSpool()
		case <-hooks.C:
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			l.IngestHookTurns(c)
			cancel()
		case <-agg.C:
			l.flushAggregates()
		case <-hourly.C:
			l.refreshSecrets()
			l.refreshFingerprints()
			l.retention(ctx, logf)
		}
	}
}

// refreshFingerprints keeps the hook's fingerprints of the configured secrets
// current (only when the hooks are turned on for this installation).
func (l *Logger) refreshFingerprints() {
	if l.cfg == nil {
		return
	}
	dir := l.cfg.ClaudeHooksDir()
	if _, err := os.Stat(filepath.Join(dir, "sessions")); err != nil {
		return
	}
	_ = WriteFingerprints(dir, l.red)
}

// retention removes old entries of this machine when log.retentionDays is set
// (never otherwise), and local prompt texts nothing refers to any more.
func (l *Logger) retention(ctx context.Context, logf func(string, ...any)) {
	if l.cfg == nil || l.db == nil {
		return
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if days := l.Settings().RetentionDays; days > 0 {
		before := l.now().AddDate(0, 0, -days)
		c, cancel := context.WithTimeout(ctx, time.Minute)
		res, err := Purge(c, l, PurgeOptions{Before: before, InstallationID: l.inst})
		cancel()
		if err == nil && res.Deleted > 0 {
			logf("activity log: removed %d entries older than %d days (log.retentionDays)", res.Deleted, days)
		}
	}
	l.sweepLocalBodies(ctx)
}

// sweepLocalBodies removes this machine's local prompt texts for days that no
// entry refers to any more (after a purge on any machine).
func (l *Logger) sweepLocalBodies(ctx context.Context) {
	dir := l.cfg.LogBodiesDir()
	days, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, d := range days {
		day, err := time.Parse("2006-01-02", d.Name())
		if err != nil || !d.IsDir() || time.Since(day) < 48*time.Hour {
			continue
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		n, err := l.db.C(Collection).CountDocuments(c, bson.M{"workspaceId": l.ws, "installationId": l.inst, "kind": KindClaude,
			"at": bson.M{"$gte": day, "$lt": day.Add(24 * time.Hour)}})
		cancel()
		if err == nil && n == 0 {
			_ = os.RemoveAll(filepath.Join(dir, d.Name()))
		}
	}
}

// Status is the log's own health on this machine.
type Status struct {
	Prompts       string      `json:"prompts" jsonschema:"enum=workspace,enum=local,enum=off"`
	RetentionDays int         `json:"retentionDays" jsonschema:"description=0 keeps everything until you delete it."`
	Spool         SpoolStatus `json:"spool"`
	Machine       string      `json:"machine"`
	// Problem says why prompts cannot be kept as set (local mode in a folder
	// that is synced or would be committed).
	Problem string `json:"problem,omitempty"`
}

// Status reports the settings and what waits in the spool.
func (l *Logger) Status() Status {
	st := Status{Prompts: l.Privacy(), Spool: l.Spooled()}
	if l != nil && l.cfg != nil {
		st.RetentionDays = l.Settings().RetentionDays
		st.Machine = l.machine
		st.Problem = l.LocalPromptsProblem()
	}
	return st
}

// String describes the status in one line (for doctor).
func (s Status) String() string {
	out := "prompts kept: " + s.Prompts
	if s.RetentionDays > 0 {
		out += fmt.Sprintf(", entries older than %d days removed", s.RetentionDays)
	} else {
		out += ", entries kept until you delete them"
	}
	if s.Spool.Entries > 0 {
		out += fmt.Sprintf(", %d waiting to be saved", s.Spool.Entries)
	}
	if s.Problem != "" {
		out += "; " + s.Problem
	}
	return out
}
