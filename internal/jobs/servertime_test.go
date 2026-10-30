package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// Computers' clocks differ: when a job may run (a new job, a retry's wait, a
// postponement, a job put back or tried again, one a person expedites) is
// written and compared in the database server's time (D360), so a worker
// whose clock is ten minutes fast never skips a wait, and one ten minutes
// slow never leaves a due job waiting.
func TestWhenAJobMayRunIsTheServersTime(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	q := &Queue{DB: db, WorkspaceID: "ws_test"}
	fast, slow := 10*time.Minute, -10*time.Minute
	on := func(skew time.Duration, f func()) {
		t.Helper()
		restore := testutil.SkewClock(t, skew)
		defer restore()
		f()
	}
	worker := func(id string) Worker { return Worker{ID: id, JobTypes: []string{TypeProxy}, AcceptsHeavy: true} }
	lease := func(skew time.Duration, w Worker) (j *models.Job) {
		t.Helper()
		on(skew, func() {
			var err error
			if j, err = q.Lease(ctx, w); err != nil {
				t.Fatal(err)
			}
		})
		return j
	}
	nextRunAt := func(id string) time.Time {
		t.Helper()
		j, err := q.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return j.NextRunAt
	}
	server := func() time.Time {
		t.Helper()
		now, err := db.ServerNow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return now
	}

	// Queued on a computer ten minutes fast: a slow computer's worker takes
	// it at once (by this computer's clock it would be due in ten minutes).
	var j *models.Job
	on(fast, func() {
		var err error
		if j, _, err = q.Enqueue(ctx, EnqueueOptions{Type: TypeProxy, IdempotencyKey: "proxy:fast", MaxAttempts: 5}); err != nil {
			t.Fatal(err)
		}
	})
	if d := nextRunAt(j.ID).Sub(server()); d > time.Minute || d < -time.Minute {
		t.Fatalf("a new job's nextRunAt is the server's now: off by %v", d)
	}
	got := lease(slow, worker("ins_slow"))
	if got == nil || got.ID != j.ID {
		t.Fatalf("a slow computer's worker didn't take a new job: %+v", got)
	}

	// Its attempt fails on the slow computer: a worker ten minutes fast waits
	// out the retry's whole wait, by the server's time.
	const wait = 4 * time.Second // long enough for a loaded machine to look within it
	old := RetryBase
	RetryBase = wait
	t.Cleanup(func() { RetryBase = old })
	before := server()
	on(slow, func() {
		if err := q.Fail(ctx, got, "ins_slow", errors.New("boom"), true); err != nil {
			t.Fatal(err)
		}
	})
	if d := nextRunAt(j.ID).Sub(before); d < wait-time.Second || d > wait+time.Minute {
		t.Fatalf("the retry's wait is the server's now plus the backoff: %v", d)
	}
	if got := lease(fast, worker("ins_fast")); got != nil {
		t.Fatalf("a fast computer skipped the retry's wait: %+v", got)
	}
	time.Sleep(wait + 200*time.Millisecond)
	got = lease(slow, worker("ins_slow"))
	if got == nil || got.ID != j.ID {
		t.Fatalf("a slow computer left a due retry waiting: %+v", got)
	}

	// Postponed for a while on a slow computer: the same.
	on(slow, func() {
		if err := q.Postpone(ctx, got, "ins_slow", wait, "Waiting for the render's record."); err != nil {
			t.Fatal(err)
		}
	})
	if got := lease(fast, worker("ins_fast")); got != nil {
		t.Fatalf("a fast computer skipped a postponement: %+v", got)
	}
	time.Sleep(wait + 200*time.Millisecond)
	got = lease(slow, worker("ins_slow"))
	if got == nil || got.ID != j.ID {
		t.Fatalf("a slow computer left a postponed job waiting: %+v", got)
	}

	// Put back by a stopping worker on a fast computer: due at once for a
	// slow one.
	on(fast, func() {
		if err := q.Release(ctx, got, "ins_slow"); err != nil {
			t.Fatal(err)
		}
	})
	if got = lease(slow, worker("ins_slow")); got == nil || got.ID != j.ID {
		t.Fatalf("a released job waited on a slow computer: %+v", got)
	}

	// Failed for good, then tried again on a fast computer: due at once for
	// a slow one.
	on(fast, func() {
		if err := q.Fail(ctx, got, "ins_slow", errors.New("bad input"), false); err != nil {
			t.Fatal(err)
		}
		if _, err := q.Retry(ctx, j.ID); err != nil {
			t.Fatal(err)
		}
	})
	if got = lease(slow, worker("ins_slow")); got == nil || got.ID != j.ID {
		t.Fatalf("a job tried again waited on a slow computer: %+v", got)
	}

	// Waiting out a long retry, expedited on a slow computer: due at once by
	// the server's time; one whose wait is over already keeps its time.
	RetryBase = time.Hour
	on(slow, func() {
		if err := q.Fail(ctx, got, "ins_slow", errors.New("boom"), true); err != nil {
			t.Fatal(err)
		}
	})
	if got := lease(fast, worker("ins_fast")); got != nil {
		t.Fatalf("an hour's wait skipped: %+v", got)
	}
	on(slow, func() {
		if ids, err := q.Expedite(ctx, []string{j.ID}); err != nil || len(ids) != 1 {
			t.Fatalf("expedite: %v %v", ids, err)
		}
	})
	if d := nextRunAt(j.ID).Sub(server()); d > time.Minute || d < -time.Minute {
		t.Fatalf("expedited to the server's now: off by %v", d)
	}
	past := server().Add(-time.Hour).Truncate(time.Millisecond)
	if _, err := db.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID}, bson.M{"$set": bson.M{"nextRunAt": past}}); err != nil {
		t.Fatal(err)
	}
	on(fast, func() {
		if _, err := q.Expedite(ctx, []string{j.ID}); err != nil {
			t.Fatal(err)
		}
	})
	if at := nextRunAt(j.ID); !at.Equal(past) {
		t.Fatalf("a job already due was moved: %v, want %v", at, past)
	}
	if got := lease(slow, worker("ins_slow")); got == nil || got.ID != j.ID {
		t.Fatalf("an expedited job waited on a slow computer: %+v", got)
	}
}

// A worker whose clock is ten minutes fast never takes a live lease (by any
// way of leasing), and one ten minutes slow takes over a lease that ended by
// the server's time.
func TestAFastClockNeverTakesALiveLease(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	q := &Queue{DB: db, WorkspaceID: "ws_test"}
	w := func(id string) Worker { return Worker{ID: id, JobTypes: []string{TypeProxy}, AcceptsHeavy: true} }
	j, _, err := q.Enqueue(ctx, EnqueueOptions{Type: TypeProxy, IdempotencyKey: "proxy:live", MaxAttempts: 5})
	if err != nil {
		t.Fatal(err)
	}
	restore := testutil.SkewClock(t, -10*time.Minute)
	held, err := q.Lease(ctx, w("ins_slow"))
	restore()
	if err != nil || held == nil {
		t.Fatalf("lease: %+v %v", held, err)
	}
	restore = testutil.SkewClock(t, 10*time.Minute)
	for name, take := range map[string]func() (*models.Job, error){
		"Lease":      func() (*models.Job, error) { return q.Lease(ctx, w("ins_fast")) },
		"LeaseAmong": func() (*models.Job, error) { return q.LeaseAmong(ctx, w("ins_fast"), []string{j.ID}) },
		"LeaseByID":  func() (*models.Job, error) { return q.LeaseByID(ctx, w("ins_fast"), j.ID) },
	} {
		if got, err := take(); err != nil || (got != nil && got.ID != "") {
			t.Fatalf("%s on a fast computer took a live lease: %+v %v", name, got, err)
		}
	}
	// The holder's heartbeat on its slow clock keeps the lease the server's
	// LeaseDuration from now.
	restore()
	restore = testutil.SkewClock(t, -10*time.Minute)
	if _, err := q.Heartbeat(ctx, held, "ins_slow", models.Progress{Message: "working"}); err != nil {
		t.Fatal(err)
	}
	restore()
	cur, _ := q.Get(ctx, j.ID)
	now, _ := db.ServerNow(ctx)
	if d := cur.Lease.ExpiresAt.Sub(now); d < LeaseDuration-30*time.Second || d > LeaseDuration+time.Second {
		t.Fatalf("a heartbeat on a slow clock: the lease ends in %v", d)
	}
	// The lease ends by the server's time: a slow computer takes it over.
	ended := now.Add(-time.Second)
	if _, err := db.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID}, bson.M{"$set": bson.M{"lease.expiresAt": ended}}); err != nil {
		t.Fatal(err)
	}
	restore = testutil.SkewClock(t, -10*time.Minute)
	defer restore()
	if got, err := q.Lease(ctx, w("ins_other")); err != nil || got == nil || got.ID != j.ID {
		t.Fatalf("a slow computer left a lease that ended: %+v %v", got, err)
	}
}

// A job's times are one clock's, the database server's, whichever computer
// wrote them (D368): what people read beside when it may run adds up.
func TestAJobsTimesAreTheServers(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	q := &Queue{DB: db, WorkspaceID: "ws_test"}
	w := Worker{ID: "ins_a", JobTypes: []string{TypeProxy}, AcceptsHeavy: true}
	near := func(what string, at time.Time) {
		t.Helper()
		now, err := db.ServerNow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if d := now.Sub(at); d < -time.Second || d > time.Minute {
			t.Fatalf("%s: %v from the server's now", what, d)
		}
	}
	for _, skew := range []time.Duration{10 * time.Minute, -10 * time.Minute} {
		restore := testutil.SkewClock(t, skew)
		j, _, err := q.Enqueue(ctx, EnqueueOptions{Type: TypeProxy, IdempotencyKey: "proxy:" + skew.String(), MaxAttempts: 3})
		if err != nil {
			t.Fatal(err)
		}
		near("made", j.CreatedAt)
		got, err := q.Lease(ctx, w)
		if err != nil || got == nil {
			t.Fatalf("lease: %+v %v", got, err)
		}
		near("started", got.Attempts[0].StartedAt)
		if err := q.Succeed(ctx, got, w.ID, map[string]any{}); err != nil {
			t.Fatal(err)
		}
		restore()
		done, err := q.Get(ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		if done.FinishedAt == nil {
			t.Fatal("not finished")
		}
		near("ended", *done.FinishedAt)
		near("changed", done.UpdatedAt)
	}
}
