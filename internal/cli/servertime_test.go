package cli

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// serverNowFor is the database server's time, asked directly.
func serverNowFor(t *testing.T, a *app.App) time.Time {
	t.Helper()
	now, err := a.DB.ServerNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return now
}

// A terminal waiting for a job decides by the database server's time whether
// to start its worker again (D360): on a computer ten minutes fast a lease
// another computer holds is still that computer's and a retry's wait isn't
// over; on one ten minutes slow a lease that ended and a wait that is over
// are.
func TestTheTerminalGoesByTheServersTime(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeProxy, IdempotencyKey: "proxy:skewed", MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	other := jobs.Worker{ID: ids.New(ids.Installation), Name: "studio-mac", JobTypes: []string{jobs.TypeProxy}, AcceptsHeavy: true}
	if l, err := a.Queue.LeaseByID(ctx, other, j.ID); err != nil || l == nil {
		t.Fatalf("the other computer's lease: %+v %v", l, err)
	}
	starts := 0
	term := startTerminal(ctx, func() jobRunner {
		starts++
		return runFunc(func(context.Context) error { return nil })
	})
	for !term.stopped() {
		time.Sleep(10 * time.Millisecond)
	}
	set := func(field string, v any) []models.Job {
		t.Helper()
		if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID}, bson.M{"$set": bson.M{field: v}}); err != nil {
			t.Fatal(err)
		}
		cur, err := a.Queue.Get(ctx, j.ID)
		if err != nil {
			t.Fatal(err)
		}
		return []models.Job{*cur}
	}
	keepGoing := func(skew time.Duration, pending []models.Job) {
		t.Helper()
		restore := testutil.SkewClock(t, skew)
		defer restore()
		if err := term.keepGoing(ctx, pending, func() time.Time { return a.DB.NowOrLocal(ctx) }, t.Logf); err != nil {
			t.Fatal(err)
		}
		for !term.stopped() {
			time.Sleep(10 * time.Millisecond)
		}
	}
	// Running elsewhere, its lease ending in a few seconds by the server.
	keepGoing(10*time.Minute, set("lease.expiresAt", serverNowFor(t, a).Add(5*time.Second)))
	if starts != 1 {
		t.Fatalf("a computer ten minutes fast took a live lease for over: started %d times", starts)
	}
	// Its lease ended a second ago by the server.
	keepGoing(-10*time.Minute, set("lease.expiresAt", serverNowFor(t, a).Add(-time.Second)))
	if starts != 2 {
		t.Fatalf("a computer ten minutes slow left a lease that ended: started %d times", starts)
	}
	// Queued, its retry due in five minutes by the server.
	set("state", models.JobQueued)
	keepGoing(10*time.Minute, set("nextRunAt", serverNowFor(t, a).Add(5*time.Minute)))
	if starts != 2 {
		t.Fatalf("a computer ten minutes fast skipped a retry's wait: started %d times", starts)
	}
	keepGoing(-10*time.Minute, set("nextRunAt", serverNowFor(t, a).Add(-time.Second)))
	if starts != 3 {
		t.Fatalf("a computer ten minutes slow left a due job waiting: started %d times", starts)
	}
}

// A wait gives up on a job no computer can run only once its retry's wait is
// over by the database server's time, and the workers it counts as online
// heartbeated within OnlineWindow by the server's time (D360).
func TestWaitingAndOnlineGoByTheServersTime(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	const nobody = "nobody-runs-this"
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: nobody, IdempotencyKey: "nobody:1"})
	if err != nil {
		t.Fatal(err)
	}
	stuckOn := func(skew time.Duration, next time.Time) bool {
		t.Helper()
		if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID}, bson.M{"$set": bson.M{"nextRunAt": next}}); err != nil {
			t.Fatal(err)
		}
		cur, _ := a.Queue.Get(ctx, j.ID)
		restore := testutil.SkewClock(t, skew)
		defer restore()
		var caps *hereCan
		return len(stuckJobs(ctx, a, []models.Job{*cur}, &caps)) == 1
	}
	if stuckOn(10*time.Minute, serverNowFor(t, a).Add(5*time.Minute)) {
		t.Fatal("a computer ten minutes fast gave up on a job still waiting for its next try")
	}
	if !stuckOn(-10*time.Minute, serverNowFor(t, a).Add(-time.Second)) {
		t.Fatal("a computer ten minutes slow waited on for a job no computer can run")
	}

	// A worker that heartbeated 30 s ago by the server is online; one 60 s
	// ago isn't, whatever this computer's clock says.
	beat := func(skew, ago time.Duration) (online bool, doctor string) {
		t.Helper()
		w := models.WorkerDevice{ID: "ins_desk", WorkspaceID: a.WorkspaceID(), Name: "desk", JobTypes: []string{nobody}, Concurrency: 1,
			LastHeartbeat: serverNowFor(t, a).Add(-ago)}
		if _, err := a.DB.C(store.CWorkers).ReplaceOne(ctx, bson.M{"_id": w.ID}, w, options.Replace().SetUpsert(true)); err != nil {
			t.Fatal(err)
		}
		restore := testutil.SkewClock(t, skew)
		defer restore()
		ws, err := ops.OnlineWorkers(ctx, a, nobody)
		if err != nil {
			t.Fatal(err)
		}
		_, doctor, err = activeWorkers(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		return len(ws) == 1, doctor
	}
	if on, doctor := beat(-10*time.Minute, 30*time.Second); !on || !strings.Contains(doctor, "desk") {
		t.Fatalf("a computer ten minutes slow: a worker that heartbeated 30 s ago is offline (%q)", doctor)
	}
	if on, doctor := beat(10*time.Minute, 30*time.Second); !on || !strings.Contains(doctor, "desk") {
		t.Fatalf("a computer ten minutes fast: a worker that heartbeated 30 s ago is offline (%q)", doctor)
	}
	if on, doctor := beat(-10*time.Minute, time.Minute); on || strings.Contains(doctor, "desk") {
		t.Fatalf("a computer ten minutes slow: a worker silent for a minute is online (%q)", doctor)
	}
}

// init records when the computer was last seen in the database server's time
// (a take-over and a move compare it with other computers': D360).
func TestInitRecordsTheServersTime(t *testing.T) {
	setupEnv(t)
	db := testutil.DB(t)
	h := filepath.Join(t.TempDir(), "home")
	t.Setenv("METARUSH_MONGODB_URI", testutil.URI())
	testutil.SkewClock(t, -10*time.Minute)
	r := runCLI(t, h, "init", "--yes", "--test-db", "--secrets", "env", "--db-name", db.Name, "--workspace-name", "Skewed home",
		"--media", "filesystem", "--port", strconv.Itoa(freeTCPPort(t)))
	if r.code != 0 {
		t.Fatalf("init: %d %s", r.code, r.stderr)
	}
	cur, err := db.C(store.CInstallations).Find(context.Background(), bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	var ins []models.Installation
	if err := cur.All(context.Background(), &ins); err != nil || len(ins) != 1 {
		t.Fatalf("installations: %+v %v", ins, err)
	}
	now, _ := db.ServerNow(context.Background())
	if d := now.Sub(ins[0].LastSeenAt); d < 0 || d > time.Minute {
		t.Fatalf("last seen %v before the server's now (a computer ten minutes slow)", d)
	}
}
