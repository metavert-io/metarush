package generate_test

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// Computers sharing a database have clocks minutes apart: what generated
// video decides across them goes by the database server's clock (D360). Each
// test plays a computer ten minutes ahead and one ten minutes behind against
// records near a limit by the server's clock, where a wrong clock would decide
// the other way.

const skew = 10 * time.Minute

func serverNow(t *testing.T, a *app.App) time.Time {
	t.Helper()
	now, err := a.DB.ServerNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return now
}

// near fails unless at is the server's time now (within a few seconds), not
// a clock ten minutes off.
func near(t *testing.T, what string, at time.Time, server time.Time) {
	t.Helper()
	if d := at.Sub(server); d < -time.Minute || d > time.Minute {
		t.Fatalf("%s is %v, %v from the server's time", what, at, d)
	}
}

// A generation left without an outcome is closed by the sweep once it is a
// minute old by the server's clock; its record and its outcome carry the
// server's time, whichever computer wrote them.
func TestTheSweepGoesByTheServersClock(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ws := e.a.WorkspaceID()
	restore := testutil.SkewClock(t, -skew)
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "a heron on a post", Seconds: 3, Resolution: "360p", MaxUSD: usd(1)}, false)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	g := out.Generations[0]
	near(t, "createdAt", g.CreatedAt, serverNow(t, e.a))
	if _, err := e.a.Queue.RequestCancel(ctx, g.JobID); err != nil {
		t.Fatal(err)
	}
	age := func(d time.Duration) {
		t.Helper()
		if err := e.a.DB.Update(ctx, store.CGenerations, ws, g.ID, bson.M{"$set": bson.M{"createdAt": serverNow(t, e.a).Add(-d)}}); err != nil {
			t.Fatal(err)
		}
	}
	show := func() generate.GenView {
		t.Helper()
		v, err := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: g.ID}, false)
		if err != nil {
			t.Fatal(err)
		}
		return *v
	}
	// 55 s old: a computer ahead leaves it for the next sweep.
	age(55 * time.Second)
	restore = testutil.SkewClock(t, skew)
	generate.Sweep(ctx, e.a)
	restore()
	if v := show(); v.Outcome != nil {
		t.Fatalf("a computer ahead closed a generation 55 s old: %+v", v.Outcome)
	}
	// 65 s old: a computer behind closes it, at the server's time.
	age(65 * time.Second)
	restore = testutil.SkewClock(t, -skew)
	generate.Sweep(ctx, e.a)
	restore()
	v := show()
	if v.Outcome == nil || v.State != generate.StateCanceled {
		t.Fatalf("a computer behind left a generation 65 s old: %+v", v.Outcome)
	}
	near(t, "the outcome's time", v.Outcome.At, serverNow(t, e.a))
}

// holdAt is when a generation's money was held in a ledger.
func holdAt(t *testing.T, a *app.App, scope, key, genID string) time.Time {
	t.Helper()
	var l struct {
		Holds []struct {
			ID string    `bson:"id"`
			At time.Time `bson:"at"`
		} `bson:"holds"`
	}
	_ = a.DB.C(store.CGenSpend).FindOne(context.Background(), bson.M{"_id": a.WorkspaceID() + ":" + scope + ":" + key}).Decode(&l)
	for _, h := range l.Holds {
		if h.ID == genID {
			return h.At
		}
	}
	t.Fatalf("no hold for %s in %s %s", genID, scope, key)
	return time.Time{}
}

// Money held for a generation that was never saved is let go after an hour
// by the server's clock: a computer ahead doesn't let go of a hold another
// computer took 59 minutes ago, and a hold's time is the server's.
func TestHeldMoneyIsLetGoByTheServersClock(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	orphan := ids.New(ids.Generation)
	restore := testutil.SkewClock(t, -skew)
	b, err := generate.Reserve(ctx, e.a, e.project, orphan, 0.4, serverNow(t, e.a))
	restore()
	if err != nil {
		t.Fatal(err)
	}
	near(t, "the hold's time", holdAt(t, e.a, "project", e.project, orphan), serverNow(t, e.a))
	near(t, "the hold's time", holdAt(t, e.a, "day", b.Day, orphan), serverNow(t, e.a))
	held := func(d time.Duration) {
		t.Helper()
		if _, err := e.a.DB.C(store.CGenSpend).UpdateMany(ctx, bson.M{"workspaceId": e.a.WorkspaceID(), "holds.id": orphan},
			bson.M{"$set": bson.M{"holds.$.at": serverNow(t, e.a).Add(-d)}}); err != nil {
			t.Fatal(err)
		}
	}
	held(59 * time.Minute)
	restore = testutil.SkewClock(t, skew)
	n, err := generate.ReleaseOrphans(ctx, e.a)
	restore()
	if err != nil || n != 0 {
		t.Fatalf("a computer ahead let go of a hold 59 minutes old: %d %v", n, err)
	}
	held(61 * time.Minute)
	restore = testutil.SkewClock(t, -skew)
	n, err = generate.ReleaseOrphans(ctx, e.a)
	restore()
	if err != nil || n != 2 {
		t.Fatalf("a computer behind kept a hold 61 minutes old: %d %v", n, err)
	}
}

// The day whose limit a shot counts against is the server's UTC day: a
// computer ten minutes off either way, or one whose clock already says
// tomorrow, asks and shows against the same day as every other.
func TestTheDailyLimitIsTheServersDay(t *testing.T) {
	e := setup(t)
	day := func() string { return serverNow(t, e.a).UTC().Format("2006-01-02") }
	now := serverNow(t, e.a).UTC()
	tomorrow := now.Truncate(24 * time.Hour).Add(24 * time.Hour).Sub(now)
	for _, off := range []time.Duration{-skew, skew, tomorrow + time.Minute} {
		restore := testutil.SkewClock(t, off)
		before := day()
		out, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "a kingfisher diving", Seconds: 3, Resolution: "360p", MaxUSD: usd(1)}, false)
		if err != nil {
			t.Fatal(err)
		}
		st, err := call[generate.GenSettingsOut](t, e.a, "generate.settings", generate.GenSettingsIn{ProjectID: e.project}, false)
		if err != nil {
			t.Fatal(err)
		}
		after := day()
		restore()
		g := out.Generations[0]
		if g.Budget == nil || (g.Budget.Day != before && g.Budget.Day != after) {
			t.Fatalf("clock off by %v: the shot counted against %+v, the server's day is %s", off, g.Budget, before)
		}
		if st.Budget.Today.Day != before && st.Budget.Today.Day != after {
			t.Fatalf("clock off by %v: today's limit shows %s, the server's day is %s", off, st.Budget.Today.Day, before)
		}
	}
}

// A consent runs a year from the server's time, and whether it is in force
// — when a shot is planned, when it is sent, when a look is made or approved
// — is decided by the server's clock.
func TestConsentInForceGoesByTheServersClock(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	wren := e.person(t, "Wren Tolliver")
	porch := e.clipWithFace(t, "porch", wren, "Wren Tolliver", true)
	restore := testutil.SkewClock(t, -skew)
	before := serverNow(t, e.a)
	c, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Wren Tolliver", Recording: porch.ID, Adult: true,
		Statement: "I, Wren Tolliver, agree to my likeness in generated video for the pond film."}, true)
	restore()
	if err != nil {
		t.Fatal(err)
	}
	if c.Consent.ExpiresAt == nil {
		t.Fatal("someone else's consent runs a year")
	}
	near(t, "the consent's end", *c.Consent.ExpiresAt, before.AddDate(1, 0, 0))
	ends := func(in time.Duration) {
		t.Helper()
		if _, err := e.a.DB.C(store.CConsents).UpdateOne(ctx, bson.M{"_id": c.Consent.ID}, bson.M{"$set": bson.M{"expiresAt": serverNow(t, e.a).Add(in)}}); err != nil {
			t.Fatal(err)
		}
	}
	as := func(off time.Duration, fn func()) {
		t.Helper()
		restore := testutil.SkewClock(t, off)
		defer restore()
		fn()
	}
	shot := generate.GenShotIn{ProjectID: e.project, Prompt: "she waves from the porch", From: &generate.FrameRef{AssetID: porch.ID, At: 1}, Seconds: 4}
	g := &models.Generation{ProjectID: e.project, ConsentIDs: []string{c.Consent.ID}}
	look := generate.LookCreateIn{Title: "Wren", Person: "Wren Tolliver", Description: "A woman with a grey braid and a green coat.",
		Stills: []generate.LookStillIn{{AssetID: porch.ID, At: 1}, {AssetID: porch.ID, At: 2}}}

	// What a person reads about it goes by the same clock (D368).
	status := func(off time.Duration) string {
		t.Helper()
		var v *generate.ConsentView
		as(off, func() {
			var err error
			if v, err = call[generate.ConsentView](t, e.a, "consent.show", generate.ConsentIDIn{ID: c.Consent.ID}, false); err != nil {
				t.Fatal(err)
			}
		})
		return v.Status
	}
	ends(-30 * time.Second)
	if st := status(-skew); st != "expired" {
		t.Fatalf("a computer behind shows a consent that ended 30 s ago as %s", st)
	}
	ends(30 * time.Second)
	if st := status(skew); st != "active" {
		t.Fatalf("a computer ahead shows a consent with 30 s left as %s", st)
	}

	// Ended 30 s ago by the server's clock: a computer behind refuses all of it.
	ends(-30 * time.Second)
	as(-skew, func() {
		if est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", shot, false); err != nil || est.Allowed {
			t.Fatalf("a computer behind planned with an ended consent: %+v %v", est, err)
		}
		if err := generate.ConsentsHold(ctx, e.a, g); codeOf(err) != apperr.ConsentRequired {
			t.Fatalf("a computer behind would send with an ended consent: %v", err)
		}
		if _, err := call[generate.LookOut](t, e.a, "looks.create", look, true); codeOf(err) != apperr.ConsentRequired {
			t.Fatalf("a computer behind made a look on an ended consent: %v", err)
		}
	})
	// 30 s left: a computer ahead still takes it as in force.
	ends(30 * time.Second)
	as(skew, func() {
		if est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", shot, false); err != nil || !est.Allowed {
			t.Fatalf("a computer ahead took a consent with 30 s left as ended: %+v %v", est, err)
		}
		if err := generate.ConsentsHold(ctx, e.a, g); err != nil {
			t.Fatalf("a computer ahead would stop a request whose consent has 30 s left: %v", err)
		}
		if _, err := call[generate.LookOut](t, e.a, "looks.create", look, true); err != nil {
			t.Fatalf("a computer ahead refused a look on a consent with 30 s left: %v", err)
		}
	})
	// Approving the look goes by the same clock.
	ends(-30 * time.Second)
	as(-skew, func() {
		if _, err := call[generate.LookOut](t, e.a, "looks.approve", generate.LookDecideIn{Look: "wren"}, true); codeOf(err) != apperr.ConsentRequired {
			t.Fatalf("a computer behind approved a look on an ended consent: %v", err)
		}
	})
	ends(30 * time.Second)
	as(skew, func() {
		if _, err := call[generate.LookOut](t, e.a, "looks.approve", generate.LookDecideIn{Look: "wren"}, true); err != nil {
			t.Fatalf("a computer ahead refused to approve a look whose consent has 30 s left: %v", err)
		}
	})
}
