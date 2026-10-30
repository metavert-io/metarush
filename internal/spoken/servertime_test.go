package spoken_test

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/spoken"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

const skew = 10 * time.Minute

func serverNow(t *testing.T, a *app.App) time.Time {
	t.Helper()
	now, err := a.DB.ServerNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return now
}

func near(t *testing.T, what string, at, server time.Time) {
	t.Helper()
	if d := at.Sub(server); d < -time.Minute || d > time.Minute {
		t.Fatalf("%s is %v, %v from the server's time", what, at, d)
	}
}

func change(t *testing.T, a *app.App, id string) models.SpokenChange {
	t.Helper()
	var c models.SpokenChange
	if err := a.DB.GetByID(context.Background(), store.CSpokenChanges, a.WorkspaceID(), id, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// Every daemon sweeps changes left being made, by the database server's
// clock (D360): a change two minutes without news by the server is failed,
// one a few seconds short of it is left, whatever the sweeping computer's
// clock says; two computers sweeping at once fail it once; a change's times
// are the server's, whichever computer wrote them. The consent the new words
// need is in force by the same clock.
func TestChangesBeingMadeGoByTheServersClock(t *testing.T) {
	e := setup(t)
	e.ready(t, []string{models.ConsentUseNewWords})
	ctx := context.Background()
	ask := func() spoken.SayMakeOut {
		in := twelve(e)
		est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", in, false)
		in.MaxUSD = usd(est.USD)
		return *mustCall[spoken.SayMakeOut](t, e.a, "cut.say.make", in, true)
	}
	// Asked on a computer ten minutes behind: its times are the server's.
	behind := testutil.SkewClock(t, -skew)
	first := ask()
	behind()
	c := change(t, e.a, first.Change.ID)
	near(t, "createdAt", c.CreatedAt, serverNow(t, e.a))
	near(t, "updatedAt", c.UpdatedAt, serverNow(t, e.a))

	// Its job and record are gone: nothing will report.
	_, _ = e.a.DB.C(store.CJobs).DeleteOne(ctx, bson.M{"_id": first.JobID})
	_, _ = e.a.DB.C(store.CGenerations).DeleteOne(ctx, bson.M{"_id": first.Change.GenerationID})
	quiet := func(d time.Duration) {
		t.Helper()
		if err := e.a.DB.Update(ctx, store.CSpokenChanges, e.a.WorkspaceID(), first.Change.ID, bson.M{"$set": bson.M{"updatedAt": serverNow(t, e.a).Add(-d)}}); err != nil {
			t.Fatal(err)
		}
	}
	// 1 min 55 s without news: a computer ahead leaves it.
	quiet(115 * time.Second)
	ahead := testutil.SkewClock(t, skew)
	n := spoken.Sweep(ctx, e.a)
	ahead()
	if n != 0 || change(t, e.a, first.Change.ID).State != models.SpokenMaking {
		t.Fatalf("a computer ahead failed a change 1 min 55 s without news (%d)", n)
	}
	// 2 min 5 s: a computer behind fails it while one ahead sweeps from the same reading; it is failed once.
	quiet(125 * time.Second)
	behind = testutil.SkewClock(t, -skew)
	other, inner := 0, false
	undo := spoken.SetSweepPause(func() {
		if inner {
			return
		}
		inner = true
		behind()
		ahead := testutil.SkewClock(t, skew)
		other = spoken.Sweep(ctx, e.a)
		ahead()
	})
	n = spoken.Sweep(ctx, e.a)
	undo()
	behind()
	c = change(t, e.a, first.Change.ID)
	if n+other != 1 || c.State != models.SpokenFailed {
		t.Fatalf("swept %d and %d: %s", n, other, c.State)
	}
	near(t, "updatedAt once failed", c.UpdatedAt, serverNow(t, e.a))

	// Made and kept on a computer behind: its times are the server's.
	e.heard.context, e.heard.words = mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", twelve(e), false).Context, "fifteen"
	behind = testutil.SkewClock(t, -skew)
	second := ask()
	e.runJob(t, second.JobID)
	behind()
	if c = change(t, e.a, second.Change.ID); c.State != models.SpokenReady {
		t.Fatalf("made on a computer behind: %s %s", c.State, c.Why)
	}
	near(t, "updatedAt once kept", c.UpdatedAt, serverNow(t, e.a))

	// The consent for new words: ended 30 s ago by the server's clock for a
	// computer behind, 30 s left for one ahead.
	ends := func(in time.Duration) {
		t.Helper()
		if _, err := e.a.DB.C(store.CConsents).UpdateOne(ctx, bson.M{"_id": e.consent.ID}, bson.M{"$set": bson.M{"expiresAt": serverNow(t, e.a).Add(in)}}); err != nil {
			t.Fatal(err)
		}
	}
	ends(-30 * time.Second)
	behind = testutil.SkewClock(t, -skew)
	est := mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", twelve(e), false)
	behind()
	if est.Allowed || est.Code != "consent" {
		t.Fatalf("a computer behind took an ended consent as in force: %+v", est)
	}
	ends(30 * time.Second)
	ahead = testutil.SkewClock(t, skew)
	est = mustCall[spoken.SayEstimate](t, e.a, "cut.say.estimate", twelve(e), false)
	ahead()
	if est.Code == "consent" {
		t.Fatalf("a computer ahead took a consent with 30 s left as ended: %+v", est.Problems)
	}
}
