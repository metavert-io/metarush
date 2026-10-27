package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LowWater is the fraction of the limit the quota enforcer prunes down to once
// the cache is over the limit, so it does not evict one object per check.
const LowWater = 0.9

// QuotaStatus is the state of automatic quota enforcement. The daemon (or a
// headless worker) writes it to <cache>/quota.json after every check; `cache
// status` and `sync status` read it.
type QuotaStatus struct {
	Enforced        bool         `json:"enforced"` // a live process checked recently
	EnforcerPID     int          `json:"enforcerPid,omitempty"`
	IntervalSeconds int          `json:"intervalSeconds,omitempty"`
	LastCheckAt     time.Time    `json:"lastCheckAt"`
	Bytes           int64        `json:"bytes"`
	MaxBytes        int64        `json:"maxBytes"`
	OverQuota       bool         `json:"overQuota"`
	Note            string       `json:"note,omitempty"`
	Error           string       `json:"error,omitempty"`
	LastPrune       *PruneResult `json:"lastPrune,omitempty"` // most recent automatic pass that evicted something

	prunedNow bool // this check evicted something (for logging)
}

func (c *Cache) quotaFile() string { return filepath.Join(c.Dir, "quota.json") }

// ReadQuotaStatus returns the last recorded enforcement state (nil if no
// enforcer has ever run for this cache).
func (c *Cache) ReadQuotaStatus() *QuotaStatus {
	b, err := os.ReadFile(c.quotaFile())
	if err != nil {
		return nil
	}
	var q QuotaStatus
	if json.Unmarshal(b, &q) != nil {
		return nil
	}
	iv := time.Duration(q.IntervalSeconds) * time.Second
	q.Enforced = q.EnforcerPID > 0 && ProcessAlive(q.EnforcerPID) && time.Since(q.LastCheckAt) < 2*iv+time.Minute
	if !q.Enforced && q.Note == "" {
		q.Note = "nothing keeps this machine's copies under the size limit right now: MetaRush isn't running in the background (opening the dashboard starts it)"
	}
	return &q
}

func (c *Cache) writeQuotaStatus(q QuotaStatus) {
	b, err := json.MarshalIndent(q, "", "  ")
	if err != nil {
		return
	}
	tmp := c.quotaFile() + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, c.quotaFile())
	}
}

// QuotaCheck runs one enforcement pass: when the cache is above MaxBytes it
// evicts least-recently-used objects (never pinned or in-use ones) down to
// LowWater × MaxBytes. The result is recorded in quota.json.
func (c *Cache) QuotaCheck(interval time.Duration) (QuotaStatus, error) {
	q := QuotaStatus{EnforcerPID: os.Getpid(), IntervalSeconds: int(interval / time.Second), LastCheckAt: time.Now().UTC(), MaxBytes: c.MaxBytes, Enforced: true}
	if prev := c.ReadQuotaStatus(); prev != nil {
		q.LastPrune = prev.LastPrune
	}
	st, err := c.Status()
	if err != nil {
		q.Error = err.Error()
		c.writeQuotaStatus(q)
		return q, err
	}
	q.Bytes = st.Bytes
	if c.MaxBytes > 0 && st.Bytes > c.MaxBytes {
		r, err := c.PruneTo(int64(float64(c.MaxBytes)*LowWater), false)
		r.Trigger = "quota"
		if err != nil {
			q.Error = err.Error()
		}
		if r.Removed > 0 {
			q.LastPrune = &r
			q.prunedNow = true
		}
		q.Bytes = r.AfterBytes
		if r.AfterBytes > c.MaxBytes {
			q.Note = fmt.Sprintf("still over the limit: %s kept for offline work and %s in use can't be cleared", humanBytes(r.PinnedBytes), humanBytes(r.InUseBytes))
		}
	}
	q.OverQuota = c.MaxBytes > 0 && q.Bytes > c.MaxBytes
	c.writeQuotaStatus(q)
	return q, err
}

// EnforceQuota checks the quota every interval (and shortly after downloads
// add to the cache) until ctx ends. logf receives one line per eviction pass
// and per change of the over-quota state.
func (c *Cache) EnforceQuota(ctx context.Context, interval time.Duration, logf func(format string, a ...any)) {
	if interval <= 0 {
		return
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	const minGap = 30 * time.Second
	var last time.Time
	wasOver := false
	check := func() {
		last = time.Now()
		q, err := c.QuotaCheck(interval)
		if err != nil {
			logf("cache quota check failed: %v", err)
			return
		}
		if q.prunedNow {
			r := q.LastPrune
			logf("cache over its %s limit (%s): evicted %d least-recently-used object(s), freed %s; kept %d pinned and %d in-use",
				humanBytes(c.MaxBytes), humanBytes(r.BeforeBytes), r.Removed, humanBytes(r.FreedBytes), r.SkippedPinned, r.SkippedInUse)
		}
		if q.OverQuota != wasOver {
			if q.OverQuota {
				logf("cache is over its limit (%s of %s): %s", humanBytes(q.Bytes), humanBytes(c.MaxBytes), q.Note)
			} else if wasOver {
				logf("cache is back under its limit (%s of %s)", humanBytes(q.Bytes), humanBytes(c.MaxBytes))
			}
			wasOver = q.OverQuota
		}
	}
	check()
	t := time.NewTicker(interval)
	defer t.Stop()
	var soon <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			check()
		case <-c.kick:
			if soon == nil {
				wait := minGap - time.Since(last)
				if wait < 0 {
					wait = 0
				}
				soon = time.After(wait)
			}
		case <-soon:
			soon = nil
			check()
		}
	}
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for x := n / u; x >= u; x /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
