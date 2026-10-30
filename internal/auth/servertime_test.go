package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/dblink"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Several computers share one database and their clocks differ, so every
// time one computer writes and another compares is the database server's
// (D360). These tests play a computer whose clock is ten minutes slow or fast
// with testutil.SkewClock; the test MongoDB's clock is the right one.

func near(a, b time.Time, within time.Duration) bool {
	d := a.Sub(b)
	return d < within && d > -within
}

func serverTime(t *testing.T, a *app.App) time.Time {
	t.Helper()
	now, err := a.DB.ServerNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return now
}

// newPerson adds an active member with an email.
func newPerson(t *testing.T, a *app.App, name, email string) string {
	t.Helper()
	id := ids.New("usr")
	if err := a.DB.Insert(context.Background(), store.CUsers, models.User{ID: id, WorkspaceID: a.WorkspaceID(), Name: name, Email: email, EmailKey: EmailKey(email),
		Role: models.RoleMember, State: models.UserActive, Source: "invite", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return id
}

func validAfter(t *testing.T, a *app.App, id string) time.Time {
	t.Helper()
	u, err := GetUser(context.Background(), a, id)
	if err != nil || u.SessionsValidAfter == nil {
		t.Fatalf("no sign-out recorded: %v %v", u, err)
	}
	return *u.SessionsValidAfter
}

// "Sign out everywhere" ends sessions made before it by the server's clock:
// one made five minutes before is refused even when the sign-out was pressed
// on a computer ten minutes slow, and one made just after on a computer ten
// minutes slow (or fast) is good, whichever clock is wrong.
func TestSignOutEverywhereFollowsTheServersClock(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	maya := newPerson(t, a, "Maya Lindqvist", "maya@example.com")
	for _, skew := range []time.Duration{-10 * time.Minute, 10 * time.Minute} {
		before := serverTime(t, a).Add(-5 * time.Minute)
		restore := testutil.SkewClock(t, skew) // the computer where she signs out
		if err := SignOutEverywhere(ctx, a, maya); err != nil {
			t.Fatal(err)
		}
		restore()
		if at := validAfter(t, a, maya); !near(at, serverTime(t, a), time.Minute) {
			t.Fatalf("signed out on a computer %v off: recorded %v, the server says %v", skew, at, serverTime(t, a))
		}
		if SessionValid(ctx, a, maya, before) {
			t.Fatalf("a session made five minutes before signing out on a computer %v off still works", skew)
		}
		// A moment later: a start time is measured, to within half a round trip,
		// and two this close could fall in either order.
		time.Sleep(time.Second)
		restore = testutil.SkewClock(t, -skew) // the dashboard's computer, off the other way
		made := SessionStart(ctx, a, nil)
		restore()
		if !near(made, serverTime(t, a), time.Minute) {
			t.Fatalf("a session made on a computer %v off starts at %v, the server says %v", -skew, made, serverTime(t, a))
		}
		if !SessionValid(ctx, a, maya, made) {
			t.Fatalf("a session made after signing out (on a computer %v off, the sign-out %v off) was refused", -skew, skew)
		}
	}
}

// Every other way of signing a person out everywhere — a new password, an
// account turned off or removed, going back to just the owner, a reset — is
// recorded at the server's time too, so a session made five minutes earlier
// ends whichever computer's clock did it.
func TestEverySignOutIsTheServersTime(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	writers := []struct {
		name string
		do   func(id string) error
	}{
		{"a new password", func(id string) error { return setPassword(ctx, a, id, "sunflowers by the gate", true) }},
		{"turned off", func(id string) error { _, err := SetState(ctx, a, id, models.UserDisabled); return err }},
		{"removed", func(id string) error { _, err := SetState(ctx, a, id, models.UserEarlier); return err }},
		{"just the owner", func(string) error { return SetUpSingle(ctx, a, a.Cfg.User.ID) }},
		{"a reset", func(string) error { _, err := Reset(ctx, a); return err }},
	}
	for _, w := range writers {
		for _, skew := range []time.Duration{-10 * time.Minute, 10 * time.Minute} {
			id := newPerson(t, a, "Theo Hartley", ids.New("e")+"@example.com")
			before := serverTime(t, a).Add(-5 * time.Minute)
			restore := testutil.SkewClock(t, skew)
			err := w.do(id)
			restore()
			if err != nil {
				t.Fatalf("%s: %v", w.name, err)
			}
			forgetAll()
			if at := validAfter(t, a, id); !near(at, serverTime(t, a), time.Minute) {
				t.Fatalf("%s on a computer %v off: recorded %v, the server says %v", w.name, skew, at, serverTime(t, a))
			}
			if SessionValid(ctx, a, id, before) {
				t.Fatalf("%s on a computer %v off: a session made five minutes before still works", w.name, skew)
			}
		}
	}
}

// A new session starts after a sign-out the sign-in already read, even when
// the measured time is a moment behind the server's (it keeps milliseconds),
// but a sign-out an older MetaRush wrote ten minutes ahead doesn't carry the
// session past a later sign-out.
func TestSessionStartsAfterTheSignOutItRead(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	server := serverNow(ctx, a)
	justNow := server.Add(300 * time.Millisecond)
	if got := SessionStart(ctx, a, &models.User{SessionsValidAfter: &justNow}); !got.After(justNow) || got.Sub(justNow) > 10*time.Millisecond {
		t.Fatalf("a sign-out a moment ahead: the session starts at %v, the sign-out %v", got, justNow)
	}
	ahead := server.Add(10 * time.Minute)
	if got := SessionStart(ctx, a, &models.User{SessionsValidAfter: &ahead}); !near(got, server, time.Minute) {
		t.Fatalf("a sign-out ten minutes ahead moved the session's start to %v", got)
	}
	earlier := server.Add(-time.Hour)
	if got := SessionStart(ctx, a, &models.User{SessionsValidAfter: &earlier}); !near(got, server, time.Minute) {
		t.Fatalf("an old sign-out: %v", got)
	}
}

// When the database doesn't answer, a sign-in on this computer (a one-time
// link) waits for its time no longer than a sign-in check does and uses this
// computer's clock (D180).
func TestSessionStartWhileTheDatabaseIsAway(t *testing.T) {
	link := dblink.Start(t)
	a := testapp.NewWith(t, testapp.Options{DatabaseURI: link.URI()})
	old := store.ServerTimeEvery
	store.ServerTimeEvery = time.Millisecond // asked again every time
	t.Cleanup(func() { store.ServerTimeEvery = old })
	link.Cut()
	defer link.Restore()
	began := time.Now()
	got := SessionStart(context.Background(), a, nil)
	if took := time.Since(began); took > lookupWait+500*time.Millisecond {
		t.Fatalf("waited %v for the database", took)
	}
	if !near(got, time.Now(), time.Minute) {
		t.Fatalf("started at %v", got)
	}
}

// Wrong passwords are counted, slowed and locked by the server's clock, which
// every computer shares: a lock set on a computer ten minutes slow still holds
// on one ten minutes fast and ends when the server says; a wait and the day a
// count is remembered work the same way.
func TestSignInThrottlingFollowsTheServersClock(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	key := "nobody@example.com"
	set := func(count int, lastAt time.Time, lockedUntil *time.Time) {
		t.Helper()
		f := failures{ID: failureID(a, key), WorkspaceID: a.WorkspaceID(), EmailKey: key, Count: count, LastAt: lastAt, LockedUntil: lockedUntil}
		if _, err := a.DB.C(store.CAuthFailures).ReplaceOne(ctx, bson.M{"_id": f.ID}, f, options.Replace().SetUpsert(true)); err != nil {
			t.Fatal(err)
		}
	}
	get := func() failures {
		t.Helper()
		var f failures
		if err := a.DB.C(store.CAuthFailures).FindOne(ctx, bson.M{"_id": failureID(a, key)}).Decode(&f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	try := func(skew time.Duration) error {
		t.Helper()
		restore := testutil.SkewClock(t, skew)
		defer restore()
		_, err := Authenticate(ctx, a, key, "not the right words at all")
		return err
	}
	waits := func(err error) (secs int, locked, ok bool) {
		e := apperr.From(err)
		d, _ := e.Details.(map[string]any)
		if d["reason"] != "throttled" {
			return 0, false, false
		}
		s, _ := d["retryAfterSeconds"].(int)
		l, _ := d["locked"].(bool)
		return s, l, true
	}
	slow, fast := -10*time.Minute, 10*time.Minute

	// The tenth wrong password, on a computer ten minutes slow, locks the
	// email for fifteen minutes by the server's clock.
	set(lockAfter-1, serverTime(t, a).Add(-20*time.Minute), nil)
	if err := try(slow); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("the tenth try: %v", err)
	}
	f := get()
	if server := serverTime(t, a); f.Count != lockAfter || !near(f.LastAt, server, time.Minute) || f.LockedUntil == nil || !near(*f.LockedUntil, server.Add(lockFor), time.Minute) {
		t.Fatalf("locked on a computer ten minutes slow: %+v (the server says %v)", f, server)
	}
	// A computer ten minutes fast still waits the fifteen minutes.
	if secs, locked, ok := waits(try(fast)); !ok || !locked || secs < 14*60 || secs > 15*60 {
		t.Fatalf("a computer ten minutes fast: wait %ds, locked %v, throttled %v", secs, locked, ok)
	}
	// A lock that ends in five minutes holds on a computer ten minutes fast;
	// one that ended five minutes ago is over on a computer ten minutes slow.
	until := serverTime(t, a).Add(5 * time.Minute)
	set(lockAfter, serverTime(t, a).Add(-10*time.Minute), &until)
	if secs, locked, ok := waits(try(fast)); !ok || !locked || secs < 4*60 || secs > 5*60 {
		t.Fatalf("a lock ending in five minutes, ten minutes fast: wait %ds, locked %v, throttled %v", secs, locked, ok)
	}
	until = serverTime(t, a).Add(-5 * time.Minute)
	set(lockAfter, serverTime(t, a).Add(-20*time.Minute), &until)
	if err := try(slow); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a lock over five minutes ago, ten minutes slow: %v", err)
	}

	// After the fifth, each try waits a few seconds after the last one: a
	// wait that ends in four seconds holds on a computer ten minutes fast, one
	// that ended twelve seconds ago is over on a computer ten minutes slow.
	set(7, serverTime(t, a).Add(-4*time.Second), nil) // the seventh waits 8 s
	if _, locked, ok := waits(try(fast)); !ok || locked {
		t.Fatalf("a wait ending in four seconds, ten minutes fast: throttled %v, locked %v", ok, locked)
	}
	set(7, serverTime(t, a).Add(-20*time.Second), nil)
	if err := try(slow); !errors.Is(err, ErrBadCredentials) {
		t.Fatalf("a wait over twelve seconds ago, ten minutes slow: %v", err)
	}

	// A count is remembered for a day by the server's clock.
	set(4, serverTime(t, a).Add(-failureMemory+5*time.Minute), nil)
	if err := try(fast); !errors.Is(err, ErrBadCredentials) || get().Count != 5 {
		t.Fatalf("a count from 23 h 55 min ago, ten minutes fast: %v, count %d", err, get().Count)
	}
	set(lockAfter-1, serverTime(t, a).Add(-failureMemory-5*time.Minute), nil)
	if err := try(slow); !errors.Is(err, ErrBadCredentials) || get().Count != 1 || get().LockedUntil != nil {
		t.Fatalf("a count from 24 h 5 min ago, ten minutes slow: %v, %+v", err, get())
	}
}

// Invite and reset links end by the server's clock: made on a computer ten
// minutes slow or fast they last their hour by the server's time; one that
// ends in five minutes still works on a computer ten minutes fast, and one
// that ended five minutes ago doesn't on a computer ten minutes slow.
func TestLinksEndByTheServersClock(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	expire := func(token string, at time.Time) {
		t.Helper()
		if _, err := a.DB.C(store.CAuthTokens).UpdateOne(ctx, bson.M{"_id": tokenHash(token)}, bson.M{"$set": bson.M{"expiresAt": at}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, skew := range []time.Duration{-10 * time.Minute, 10 * time.Minute} {
		maya := newPerson(t, a, "Maya Lindqvist", ids.New("m")+"@example.com")
		restore := testutil.SkewClock(t, skew)
		token, exp, err := newLink(ctx, a, LinkReset, maya, ResetTTL)
		restore()
		if err != nil {
			t.Fatal(err)
		}
		if !near(exp, serverTime(t, a).Add(ResetTTL), time.Minute) {
			t.Fatalf("made on a computer %v off, the link ends at %v", skew, exp)
		}
		var rec LinkRecord
		if err := a.DB.C(store.CAuthTokens).FindOne(ctx, bson.M{"_id": tokenHash(token)}).Decode(&rec); err != nil || !near(rec.ExpiresAt, exp, time.Millisecond) {
			t.Fatalf("stored: %+v %v", rec, err)
		}
	}

	// Ends in five minutes: works on a computer ten minutes fast, once.
	maya := newPerson(t, a, "Maya Lindqvist", "maya@example.com")
	token, _, err := newLink(ctx, a, LinkReset, maya, ResetTTL)
	if err != nil {
		t.Fatal(err)
	}
	expire(token, serverTime(t, a).Add(5*time.Minute))
	restore := testutil.SkewClock(t, 10*time.Minute)
	if _, _, err := LookupLink(ctx, a, token); err != nil {
		t.Fatalf("a link ending in five minutes, ten minutes fast: %v", err)
	}
	if _, err := UseLink(ctx, a, token, "", "a brand new garden path"); err != nil {
		t.Fatalf("following it: %v", err)
	}
	if _, err := UseLink(ctx, a, token, "", "a brand new garden path"); !errors.Is(err, ErrLinkGone) {
		t.Fatalf("followed twice: %v", err)
	}
	restore()

	// Ended five minutes ago: gone on a computer ten minutes slow.
	theo := newPerson(t, a, "Theo Hartley", "theo@example.com")
	token, _, err = newLink(ctx, a, LinkReset, theo, ResetTTL)
	if err != nil {
		t.Fatal(err)
	}
	expire(token, serverTime(t, a).Add(-5*time.Minute))
	restore = testutil.SkewClock(t, -10*time.Minute)
	defer restore()
	if _, _, err := LookupLink(ctx, a, token); !errors.Is(err, ErrLinkGone) {
		t.Fatalf("a link that ended five minutes ago, ten minutes slow: %v", err)
	}
	if _, err := UseLink(ctx, a, token, "", "a brand new garden path"); !errors.Is(err, ErrLinkGone) {
		t.Fatalf("following it, ten minutes slow: %v", err)
	}
	if HasPassword(ctx, a, theo) {
		t.Fatal("an ended link set a password")
	}
}

// A stored sign-out keeps milliseconds and a session's start keeps more: a
// session made in the millisecond of a sign-out is one it ends, and one a
// millisecond after it stands.
func TestASignOutCountsToTheMillisecond(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	id := newPerson(t, a, "Theo Hartley", ids.New("e")+"@example.com")
	if err := SignOutEverywhere(ctx, a, id); err != nil {
		t.Fatal(err)
	}
	forgetAll()
	at := validAfter(t, a, id)
	if SessionValid(ctx, a, id, at.Add(70*time.Microsecond)) {
		t.Fatal("a session made in the millisecond of a sign-out outlived it")
	}
	if !SessionValid(ctx, a, id, at.Add(time.Millisecond)) {
		t.Fatal("a session made a millisecond after a sign-out didn't last")
	}
}
