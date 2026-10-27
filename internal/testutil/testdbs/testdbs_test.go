package testdbs_test

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testdbs"
)

func TestOnlyDatabasesWhoseRunIsGoneAreStale(t *testing.T) {
	now := time.Now()
	alive := func(pid int) bool { return pid == 42 }
	judge := func(f testdbs.Facts, legacy bool) (string, bool) {
		return testdbs.Judge(f, "here", now, time.Hour, legacy, alive)
	}
	run := func(pid int, host string, age time.Duration) *store.TestRun {
		return &store.TestRun{PID: pid, Host: host, StartedAt: now.Add(-age)}
	}
	if why, ok := judge(testdbs.Facts{Name: "metarush-test-x", Run: run(42, "here", 48*time.Hour)}, true); ok {
		t.Fatalf("a running test's database is stale: %s", why)
	}
	if why, ok := judge(testdbs.Facts{Name: "metarush-test-x", Run: run(7, "here", time.Minute)}, false); !ok || !strings.Contains(why, "ended") {
		t.Fatalf("a run that ended: %q %v", why, ok)
	}
	if _, ok := judge(testdbs.Facts{Name: "metarush-test-x", Run: run(7, "elsewhere", time.Minute)}, false); ok {
		t.Fatal("a recent run on another computer is stale")
	}
	if _, ok := judge(testdbs.Facts{Name: "metarush-test-x", Run: run(7, "elsewhere", 2*time.Hour)}, false); !ok {
		t.Fatal("an old run on another computer is kept")
	}
	if _, ok := judge(testdbs.Facts{Name: "metarush-test-x", Unknown: true}, true); ok {
		t.Fatal("a database whose marker could not be read is stale")
	}
	// Unmarked testutil databases: kept in their first moments, stale once a
	// late write brought one back.
	fresh := store.TestDBPrefix + strings.TrimPrefix(ids.New("t"), "t_")
	if _, ok := judge(testdbs.Facts{Name: fresh}, true); ok {
		t.Fatal("a testutil database made a moment ago is stale")
	}
	if why, ok := testdbs.Judge(testdbs.Facts{Name: fresh}, "here", now.Add(time.Hour), time.Hour, false, alive); !ok || !strings.Contains(why, "late write") {
		t.Fatalf("an unmarked testutil database an hour on: %q %v", why, ok)
	}
	// Scripts' databases say when they were made.
	script := store.TestDBPrefix + "about" + strconv.FormatInt(now.Add(-2*time.Hour).Unix(), 10)
	if _, ok := judge(testdbs.Facts{Name: script}, false); !ok {
		t.Fatal("a script's database from two hours ago is kept (older than an hour)")
	}
	if _, ok := judge(testdbs.Facts{Name: store.TestDBPrefix + "about" + strconv.FormatInt(now.Unix(), 10)}, false); ok {
		t.Fatal("a script's database from now is stale")
	}
	// Older-style: by the first migration; with no migrations only with legacy.
	if _, ok := judge(testdbs.Facts{Name: store.TestDBPrefix + "abc", FirstMigration: now.Add(-2 * time.Hour)}, false); !ok {
		t.Fatal("an old older-style database is kept")
	}
	if _, ok := judge(testdbs.Facts{Name: store.TestDBPrefix + "abc"}, false); ok {
		t.Fatal("an older-style database with no migrations is stale without legacy")
	}
	if _, ok := judge(testdbs.Facts{Name: store.TestDBPrefix + "abc"}, true); !ok {
		t.Fatal("an older-style database with no migrations is kept with legacy")
	}
}

func TestScriptNamesSayWhenTheyWereMade(t *testing.T) {
	for name, want := range map[string]int64{"acc1792534382": 1792534382, "storage1792504623": 1792504623, "04rmp2y0mqvbh7wy": 0, "about": 0} {
		got, ok := testdbs.ScriptTime(name)
		if (want != 0) != ok || (ok && got.Unix() != want) {
			t.Errorf("%s: %v %v, want %d", name, got, ok, want)
		}
	}
}

// A live database: its own marker names this running process, so it is kept,
// and a sweep (listing only) sees it and keeps it.
func TestTheSweepKeepsARunningTestsDatabase(t *testing.T) {
	ctx := context.Background()
	db := testutil.DB(t)
	host, _ := os.Hostname()
	if why, ok := testdbs.Stale(ctx, db, host, time.Hour, true); ok {
		t.Fatalf("this test's own database is stale: %s", why)
	}
	found, err := testdbs.Sweep(ctx, testutil.URI(), testdbs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, f := range found {
		if f.Name == db.Name {
			seen = true
			if f.Stale || f.Dropped {
				t.Fatalf("the sweep counts this test's database as stale: %+v", f)
			}
		}
		if !strings.HasPrefix(f.Name, store.TestDBPrefix) {
			t.Fatalf("the sweep looked at %s", f.Name)
		}
	}
	if !seen {
		t.Fatalf("the sweep did not see %s", db.Name)
	}
	if _, err := testdbs.Sweep(ctx, "mongodb://db.example.com:27017/", testdbs.Options{}); err == nil {
		t.Fatal("a sweep of a MongoDB elsewhere was not refused")
	}
}
