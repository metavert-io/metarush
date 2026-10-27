// Package testdbs finds throwaway test databases whose run is gone and drops
// them (D194). Test runs drop their own databases; a run that was stopped (a
// timeout, Ctrl-C, a crash) leaves its database behind, and the shared test
// mongod collects them. internal/testutil sweeps once per test process before
// it makes its first database; scripts/sweeptestdbs is the same sweep by hand.
//
// Only local metarush-test-* databases are looked at. One is stale when:
//   - its marker (store.CTestRun, written by internal/testutil as it makes the
//     database) names a process on this computer that is no longer running,
//     or a process elsewhere and is older than Older;
//   - it has no marker but internal/testutil's name (metarush-test-<ULID>,
//     which says when it was made) and is over ten minutes old: testutil marks
//     every database it makes at once, so its run dropped it and a late write
//     (a log flush, a heartbeat) brought it back;
//   - it is a script's (metarush-test-<name><unix seconds>, scripts/*.sh) and
//     older than Older;
//   - it is older-style (made before markers) and its first migration ran
//     longer ago than Older; with Legacy also when it has none (a late write
//     brought it back). Only use Legacy when no test from before markers is
//     running.
//
// Everything else is kept. Drops go through store.DropTestDatabase, which
// refuses anything but a local test database.
package testdbs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/oklog/ulid/v2"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/store"
)

// DefaultOlder is how old a test database from elsewhere, a script's or an
// older-style one must be before it counts as stale.
const DefaultOlder = 24 * time.Hour

// Resurrected is how old an unmarked testutil database must be to count as
// brought back by a late write (it is marked within milliseconds of being made).
const Resurrected = 10 * time.Minute

// Options say what Sweep counts as stale and whether it drops.
type Options struct {
	// Older: an unmarked or foreign test database made longer ago than this is
	// stale (DefaultOlder when zero).
	Older time.Duration
	// Legacy also counts older-style test databases with no migrations as stale.
	Legacy bool
	// Drop drops the stale ones (without it, Sweep only lists them).
	Drop bool
}

// Found is one test database Sweep looked at.
type Found struct {
	Name    string
	Stale   bool
	Why     string // why it is stale
	Dropped bool
	Err     error // why a stale one could not be dropped
}

// Sweep looks at every metarush-test-* database on a local MongoDB and, with
// Drop, drops the ones whose run is gone. It refuses a MongoDB elsewhere.
func Sweep(ctx context.Context, uri string, o Options) ([]Found, error) {
	if !store.IsLocalURI(uri) {
		return nil, fmt.Errorf("refusing to sweep %q: test databases are only swept on this computer", uri)
	}
	if o.Older <= 0 {
		o.Older = DefaultOlder
	}
	admin, err := store.Connect(ctx, uri, "admin")
	if err != nil {
		return nil, err
	}
	defer admin.Close(context.Background())
	names, err := admin.Client.ListDatabaseNames(ctx, bson.M{"name": bson.M{"$regex": "^" + store.TestDBPrefix}})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	host, _ := os.Hostname()
	out := make([]Found, 0, len(names))
	for _, name := range names {
		db := &store.DB{Client: admin.Client, DB: admin.Client.Database(name), Name: name}
		f := Found{Name: name}
		f.Why, f.Stale = Stale(ctx, db, host, o.Older, o.Legacy)
		if f.Stale && o.Drop {
			if f.Err = db.DropTestDatabase(ctx, uri); f.Err == nil {
				f.Dropped = true
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// Facts are what a test database says about its run.
type Facts struct {
	Name string
	// Run is its marker (nil when it has none).
	Run *store.TestRun
	// FirstMigration is when its first migration ran (zero: none, or not looked up).
	FirstMigration time.Time
	// Unknown: the marker could not be read (kept).
	Unknown bool
}

// Stale says whether a test database's run is gone, and why.
func Stale(ctx context.Context, db *store.DB, host string, older time.Duration, legacy bool) (string, bool) {
	f := Facts{Name: db.Name}
	var run store.TestRun
	switch err := db.C(store.CTestRun).FindOne(ctx, bson.M{}).Decode(&run); {
	case err == nil:
		f.Run = &run
	case !errors.Is(err, mongo.ErrNoDocuments):
		f.Unknown = true
	case namedByTime(db.Name):
		// Unmarked, but its name says when it was made.
	default:
		// Older-style: when its first migration ran.
		var m struct {
			AppliedAt time.Time `bson:"appliedAt"`
		}
		err := db.C(store.CMigrations).FindOne(ctx, bson.M{}, options.FindOne().SetSort(bson.D{{Key: "appliedAt", Value: 1}})).Decode(&m)
		if err == nil {
			f.FirstMigration = m.AppliedAt
		} else if !errors.Is(err, mongo.ErrNoDocuments) {
			f.Unknown = true
		}
	}
	return Judge(f, host, time.Now(), older, legacy, Running)
}

// Judge decides from a database's facts whether its run is gone (alive says
// whether a process on this computer still runs).
func Judge(f Facts, host string, now time.Time, older time.Duration, legacy bool, alive func(pid int) bool) (string, bool) {
	since := func(t time.Time) time.Duration { return now.Sub(t) }
	ago := func(t time.Time) string {
		d := since(t).Round(time.Minute)
		return strings.TrimSuffix(d.String(), "0s") + " ago"
	}
	switch {
	case f.Unknown:
		return "", false
	case f.Run != nil && f.Run.Host == host && f.Run.PID > 0:
		if alive(f.Run.PID) {
			return "", false
		}
		return fmt.Sprintf("its test process %d ended; made %s", f.Run.PID, ago(f.Run.StartedAt)), true
	case f.Run != nil:
		if since(f.Run.StartedAt) > older {
			return fmt.Sprintf("made on %s %s", f.Run.Host, ago(f.Run.StartedAt)), true
		}
		return "", false
	}
	rest := strings.TrimPrefix(f.Name, store.TestDBPrefix)
	if id, err := ulid.ParseStrict(strings.ToUpper(rest)); err == nil {
		made := ulid.Time(id.Time())
		if since(made) > Resurrected {
			return "no marker: a late write brought it back after its test dropped it; made " + ago(made), true
		}
		return "", false
	}
	if made, ok := ScriptTime(rest); ok {
		if since(made) > older {
			return "a script's, made " + ago(made), true
		}
		return "", false
	}
	// Older-style: when its first migration ran.
	switch {
	case !f.FirstMigration.IsZero():
		if since(f.FirstMigration) > older {
			return "older-style, made " + ago(f.FirstMigration), true
		}
	case legacy:
		return "older-style with no migrations: a late write brought it back", true
	}
	return "", false
}

// namedByTime: the name says when the database was made (testutil's ULID or a
// script's unix seconds).
func namedByTime(name string) bool {
	rest := strings.TrimPrefix(name, store.TestDBPrefix)
	if _, err := ulid.ParseStrict(strings.ToUpper(rest)); err == nil {
		return true
	}
	_, ok := ScriptTime(rest)
	return ok
}

// scriptTime is the unix time at the end of a script's database name.
var scriptTime = regexp.MustCompile(`^[a-z-]*?(\d{9,11})$`)

// ScriptTime is when a script's database was made, from the unix seconds at
// the end of its name (after the metarush-test- prefix).
func ScriptTime(rest string) (time.Time, bool) {
	m := scriptTime.FindStringSubmatch(rest)
	if m == nil {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// Running reports whether a process with this ID exists on this computer.
func Running(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
