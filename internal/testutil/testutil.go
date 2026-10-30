// Package testutil provides isolated test databases. Tests only ever run against
// localhost and only drop databases they created (metarush-test-*).
//
// Each test gets a fresh database that looks exactly like a newly migrated one
// (D194). Migrating takes seconds (36 migrations, each building indexes), so a
// test process migrates only its first database the usual way and remembers
// the result — every collection with its options and indexes and every
// document (the migration records) — and makes each later database from that
// template. METARUSH_TEST_DB_FRESH=1 migrates every database instead.
// TestTemplateCopiesMatchAFreshMigration keeps the two the same.
//
// Before its first database, a test process drops the test databases of runs
// that are gone (internal/testutil/testdbs), so a stopped run's leftovers never
// pile up.
package testutil

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testdbs"
)

// DefaultURI is the dev replica set started by scripts/dev-mongo.sh.
const DefaultURI = "mongodb://127.0.0.1:27018/?replicaSet=rs0&directConnection=true"

// URI returns the test MongoDB URI.
func URI() string {
	if u := os.Getenv("METARUSH_TEST_MONGODB_URI"); u != "" {
		return u
	}
	return DefaultURI
}

// FreshEnv names the variable that makes every test database migrate the usual
// way instead of copying this process's template.
const FreshEnv = "METARUSH_TEST_DB_FRESH"

// DB returns a fresh, migrated, throwaway database that is dropped after the test.
func DB(t testing.TB) *store.DB {
	t.Helper()
	uri := URI()
	if !strings.Contains(uri, "127.0.0.1") && !strings.Contains(uri, "localhost") {
		t.Fatalf("refusing to run tests against non-local MongoDB %q", uri)
	}
	sweepOnce.Do(func() { sweep(uri) })
	// The whole ULID: its first ten letters say when it was made (testdbs).
	name := store.TestDBPrefix + strings.TrimPrefix(ids.New("t"), "t_")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := store.Connect(ctx, uri, name)
	if err != nil {
		if os.Getenv("METARUSH_REQUIRE_MONGO") == "1" {
			t.Fatalf("test MongoDB unavailable: %v", err)
		}
		t.Skipf("test MongoDB unavailable (run scripts/dev-mongo.sh): %v", err)
	}
	// Which process made it, so a run that never got to clean up is found
	// (testdbs).
	if err := db.MarkTestRun(ctx); err != nil {
		t.Fatalf("mark test db: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := db.DropTestDatabase(c, uri); err != nil {
			t.Errorf("drop test db: %v", err)
		}
		_ = db.Close(c)
	})
	// Index builds queue up on the single-node replica set while the whole suite
	// creates its databases at once (and parallel builds share the same mongod):
	// give the migrations far more room than the connect.
	mctx, mcancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer mcancel()
	start := time.Now()
	how, err := prepare(mctx, db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	stat(how, time.Since(start))
	return db
}

// StatsEnv names a file each test database's setup is noted in, one line
// "<how> <milliseconds>" each (how: migrated or copied) — how docs/testing.md
// measures what setting databases up costs a run.
const StatsEnv = "METARUSH_TEST_DB_STATS"

func stat(how string, took time.Duration) {
	path := os.Getenv(StatsEnv)
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintf(f, "%s %d\n", how, took.Milliseconds())
	_ = f.Close()
}

var (
	sweepOnce sync.Once
	tplOnce   sync.Once
	tpl       *Template
	tplErr    error
)

// sweep drops the test databases of runs that are gone. It never fails a test:
// a sweep that cannot finish is tried again by the next test process.
func sweep(uri string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = testdbs.Sweep(ctx, uri, testdbs.Options{Drop: true})
}

// prepare migrates a new test database: the first of the process the usual
// way (it becomes the template), every later one by copying the template.
func prepare(ctx context.Context, db *store.DB) (how string, err error) {
	if os.Getenv(FreshEnv) == "1" {
		_, err := db.Migrate(ctx)
		return "migrated", err
	}
	first := false
	tplOnce.Do(func() {
		first = true
		if _, tplErr = db.Migrate(ctx); tplErr != nil {
			return
		}
		tpl, tplErr = ReadTemplate(ctx, db)
	})
	switch {
	case first:
		return "migrated", tplErr
	case tplErr != nil:
		// No template: migrate this one the usual way.
		_, err := db.Migrate(ctx)
		return "migrated", err
	}
	return "copied", tpl.CopyInto(ctx, db)
}

// Template is what a newly migrated database holds: its collections with their
// options and indexes, and their documents. The test marker is left out.
type Template struct {
	Collections []TemplateCollection
}

// TemplateCollection is one collection of a Template.
type TemplateCollection struct {
	Name    string
	Type    string   // "collection", "view" or "timeseries"
	Options bson.Raw // listCollections' options (validators, views' pipelines…)
	Indexes []bson.Raw
	Docs    []bson.Raw
}

// ReadTemplate reads a migrated database into a Template.
func ReadTemplate(ctx context.Context, db *store.DB) (*Template, error) {
	cur, err := db.DB.ListCollections(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var specs []struct {
		Name    string   `bson:"name"`
		Type    string   `bson:"type"`
		Options bson.Raw `bson:"options"`
	}
	if err := cur.All(ctx, &specs); err != nil {
		return nil, err
	}
	t := &Template{}
	for _, s := range specs {
		if s.Name == store.CTestRun || strings.HasPrefix(s.Name, "system.") {
			continue
		}
		c := TemplateCollection{Name: s.Name, Type: s.Type, Options: s.Options}
		if c.Type != "view" {
			icur, err := db.C(s.Name).Indexes().List(ctx)
			if err != nil {
				return nil, fmt.Errorf("%s indexes: %w", s.Name, err)
			}
			var specs []bson.Raw
			if err := icur.All(ctx, &specs); err != nil {
				return nil, fmt.Errorf("%s indexes: %w", s.Name, err)
			}
			for _, ix := range specs {
				if name, _ := ix.Lookup("name").StringValueOK(); name == "_id_" {
					continue
				}
				c.Indexes = append(c.Indexes, ix)
			}
			dcur, err := db.C(s.Name).Find(ctx, bson.D{})
			if err != nil {
				return nil, fmt.Errorf("%s documents: %w", s.Name, err)
			}
			if err := dcur.All(ctx, &c.Docs); err != nil {
				return nil, fmt.Errorf("%s documents: %w", s.Name, err)
			}
		}
		t.Collections = append(t.Collections, c)
	}
	return t, nil
}

// copyAtOnce is how many collections are made at the same time: making a
// collection and its indexes is mostly mongod creating files, which it does
// side by side (measured under load: one at a time 8.2 s, 8 at once 3.7 s,
// 32 at once 3.1 s; migrating 10.5 s).
const copyAtOnce = 16

// CopyInto makes a new, empty database look like the template: collections
// first (several at once, each with all its indexes in one command), then
// views, which read from them.
func (t *Template) CopyInto(ctx context.Context, db *store.DB) error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		sem  = make(chan struct{}, copyAtOnce)
	)
	for _, c := range t.Collections {
		if c.Type == "view" {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := c.make(ctx, db); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", c.Name, err))
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		return errs[0]
	}
	for _, c := range t.Collections {
		if c.Type == "view" {
			if err := c.make(ctx, db); err != nil {
				return fmt.Errorf("%s: %w", c.Name, err)
			}
		}
	}
	return nil
}

func (c TemplateCollection) make(ctx context.Context, db *store.DB) error {
	opts, _ := c.Options.Elements()
	// A plain collection with indexes is made by its createIndexes; anything
	// with options (or no indexes) is made explicitly first.
	if len(opts) > 0 || len(c.Indexes) == 0 || c.Type != "collection" {
		cmd := bson.D{{Key: "create", Value: c.Name}}
		for _, e := range opts {
			cmd = append(cmd, bson.E{Key: e.Key(), Value: e.Value()})
		}
		if err := db.DB.RunCommand(ctx, cmd).Err(); err != nil {
			return fmt.Errorf("create: %w", err)
		}
	}
	if len(c.Indexes) > 0 {
		ixs := make(bson.A, len(c.Indexes))
		for i, ix := range c.Indexes {
			ixs[i] = ix
		}
		cmd := bson.D{{Key: "createIndexes", Value: c.Name}, {Key: "indexes", Value: ixs}}
		if err := db.DB.RunCommand(ctx, cmd).Err(); err != nil {
			return fmt.Errorf("indexes: %w", err)
		}
	}
	if len(c.Docs) > 0 {
		docs := make([]any, len(c.Docs))
		for i, d := range c.Docs {
			docs[i] = d
		}
		if _, err := db.C(c.Name).InsertMany(ctx, docs); err != nil {
			return fmt.Errorf("documents: %w", err)
		}
	}
	return nil
}

// SkewClock makes this computer's clock (store.Clock, which jobs.Clock and the
// server-time helper read) run off by d — a computer whose clock is wrong —
// until restore is called or the test ends. The database server's time is
// unchanged.
func SkewClock(t testing.TB, d time.Duration) (restore func()) {
	t.Helper()
	// A busy test machine's slow answer isn't a database that went away: what
	// a test skews for, it waits to hear.
	store.ServerTimeJumpWait = 30 * time.Second
	old := store.Clock
	store.Clock = func() time.Time { return old().Add(d) }
	var once sync.Once
	restore = func() { once.Do(func() { store.Clock = old }) }
	t.Cleanup(restore)
	return restore
}

// ServerOff makes db take the database server's time to be off by d from this
// computer's real clock until the test ends: what a display or any Go-side
// check gets from store.DB.Now moves, and a bare time.Now() doesn't, so a test
// sees which one code reads. Stamps a test writes come from db.Now then. It
// doesn't move $$NOW or $currentDate: for those, SkewClock.
func ServerOff(t testing.TB, db *store.DB, d time.Duration) {
	t.Helper()
	db.ShiftServerTime(d)
	t.Cleanup(func() { db.ShiftServerTime(0) })
}
