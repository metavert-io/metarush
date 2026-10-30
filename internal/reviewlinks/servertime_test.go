package reviewlinks_test

import (
	"context"
	"net/url"
	"strconv"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/reviewlinks"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// Every time compared across computers is the database server's (D360):
// these tests run looks on computers whose clocks are ten minutes fast or
// slow, with the stored times a few minutes from the line, so a look that
// went by its own clock would decide the other way.

// serverNow is the database server's own time.
func serverNow(t *testing.T, a *app.App) time.Time {
	t.Helper()
	now, err := a.DB.ServerNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return now
}

func near(a, b time.Time, within time.Duration) bool {
	d := a.Sub(b)
	return d < within && d > -within
}

// setLink changes a link's record as another computer's look would have.
func setLink(t *testing.T, a *app.App, id string, set bson.M) {
	t.Helper()
	if err := a.DB.Update(context.Background(), store.CReviewLinks, a.WorkspaceID(), id, bson.M{"$set": set}); err != nil {
		t.Fatal(err)
	}
}

func getLink(t *testing.T, a *app.App, id string) models.ReviewLink {
	t.Helper()
	var l models.ReviewLink
	if err := a.DB.GetByID(context.Background(), store.CReviewLinks, a.WorkspaceID(), id, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

// A link's end is the database server's time, and every look judges it by
// the server's clock: on a computer ten minutes fast or slow, a link with
// five minutes left still works — listed, sent again for those five minutes,
// not ended, and once taken back not removed for good before its signature
// is over — and a link five minutes past its end has ended, here or in a
// storage this computer no longer uses.
func TestALinksEndIsTheServers(t *testing.T) {
	for _, skew := range []time.Duration{10 * time.Minute, -10 * time.Minute} {
		w := newWorld(t)
		a := w.a
		restore := testutil.SkewClock(t, skew)
		newLink := func() *reviewlinks.ShareOut {
			return call[reviewlinks.ShareOut](t, a, "share.create", reviewlinks.ShareCreateIn{ProjectID: w.p.ID, Days: 1, AsDraft: true}, person)
		}
		soon := newLink()
		server := serverNow(t, a)
		if !near(soon.Link.CreatedAt, server, time.Minute) || soon.Link.ExpiresAt.Sub(soon.Link.CreatedAt) != 24*time.Hour {
			t.Fatalf("clock %v off: made %v, ends %v; the server's time %v", skew, soon.Link.CreatedAt, soon.Link.ExpiresAt, server)
		}
		past, awaySoon, awayPast := newLink(), newLink(), newLink()
		setLink(t, a, soon.Link.ID, bson.M{"expiresAt": server.Add(5 * time.Minute)})
		setLink(t, a, past.Link.ID, bson.M{"expiresAt": server.Add(-5 * time.Minute)})
		setLink(t, a, awaySoon.Link.ID, bson.M{"store": "s3://elsewhere", "expiresAt": server.Add(5 * time.Minute)})
		setLink(t, a, awayPast.Link.ID, bson.M{"store": "s3://elsewhere", "expiresAt": server.Add(-5 * time.Minute)})

		ls := call[reviewlinks.ShareListOut](t, a, "share.list", reviewlinks.ShareListIn{}, ops.Caller{})
		listed := map[string]bool{}
		for _, v := range ls.Links {
			listed[v.Link.ID] = v.Live
		}
		if len(listed) != 2 || !listed[soon.Link.ID] || !listed[awaySoon.Link.ID] || ls.Live != 2 {
			t.Fatalf("clock %v off: the links that work now: %+v", skew, ls)
		}
		// Sent again for the five minutes it has left.
		again := call[reviewlinks.ShareOut](t, a, "share.link", reviewlinks.ShareIDIn{ID: soon.Link.ID}, person)
		u, err := url.Parse(again.URL)
		if err != nil {
			t.Fatal(err)
		}
		if secs, _ := strconv.Atoi(u.Query().Get("X-Amz-Expires")); !again.Live || secs < 240 || secs > 300 {
			t.Fatalf("clock %v off: sent again live %v for %d s, want the 5 minutes it has left", skew, again.Live, secs)
		}
		if err := callErr(a, "share.link", reviewlinks.ShareIDIn{ID: past.Link.ID}, person); err == nil || code(err) != apperr.Conflict {
			t.Fatalf("clock %v off: a link past its end sent again: %v", skew, err)
		}

		s := call[reviewlinks.ShareSyncOut](t, a, "share.sync", reviewlinks.ShareSyncIn{}, ops.Caller{})
		if s.Ended != 1 {
			t.Fatalf("clock %v off: ended %d, want the one past its end: %+v", skew, s.Ended, s)
		}
		for _, c := range []struct {
			id      string
			state   string
			removed bool
		}{{soon.Link.ID, models.ReviewLinkLive, false}, {past.Link.ID, models.ReviewLinkExpired, true},
			{awaySoon.Link.ID, models.ReviewLinkLive, false}, {awayPast.Link.ID, models.ReviewLinkExpired, false}} {
			if l := getLink(t, a, c.id); l.State != c.state || l.Removed != c.removed {
				t.Fatalf("clock %v off: link ending %v is %s (removed %v), want %s (removed %v)", skew, l.ExpiresAt, l.State, l.Removed, c.state, c.removed)
			}
		}

		// Taken back with five minutes of its signature left: its files go,
		// but it isn't removed for good until those minutes are over (a page
		// still open could write a slot again), at this look or the next.
		run(t, a, "share.revoke", reviewlinks.ShareIDIn{ID: soon.Link.ID}, person)
		run(t, a, "share.sync", reviewlinks.ShareSyncIn{}, ops.Caller{})
		if l := getLink(t, a, soon.Link.ID); l.State != models.ReviewLinkRevoked || l.Removed {
			t.Fatalf("clock %v off: taken back with minutes left: %s, removed %v", skew, l.State, l.Removed)
		}
		restore()
	}
}

// One computer at a time reads a link's comment slots, whatever their
// clocks say: the lease ends by the database server's clock and is judged
// by it. A lease with a minute left holds against a computer ten minutes
// fast; one that ended a minute ago is free to a computer ten minutes slow;
// and while a look on the slow computer holds it, a look on the fast one
// doesn't take it over.
func TestOneComputerReadsALinkAtATime(t *testing.T) {
	w := newWorld(t)
	a := w.a
	ctx := context.Background()
	out := call[reviewlinks.ShareOut](t, a, "share.create", reviewlinks.ShareCreateIn{ProjectID: w.p.ID}, person)
	pg := openPage(t, out.URL)
	pg.comment(t, 0, dana, "Dana", map[string]any{"n": 1, "text": "One."})

	setLink(t, a, out.Link.ID, bson.M{"syncUntil": serverNow(t, a).Add(time.Minute), "syncBy": "the other computer"})
	fast := testutil.SkewClock(t, 10*time.Minute)
	if s := call[reviewlinks.ShareSyncOut](t, a, "share.sync", reviewlinks.ShareSyncIn{ID: out.Link.ID}, ops.Caller{}); s.Comments != 0 {
		t.Fatalf("a computer ten minutes fast read through a lease with a minute left: %+v", s)
	}
	if l := getLink(t, a, out.Link.ID); l.SyncBy != "the other computer" {
		t.Fatalf("the other computer's lease was taken: %q", l.SyncBy)
	}
	fast()

	setLink(t, a, out.Link.ID, bson.M{"syncUntil": serverNow(t, a).Add(-time.Minute), "syncBy": "the other computer"})
	slow := testutil.SkewClock(t, -10*time.Minute)
	if s := call[reviewlinks.ShareSyncOut](t, a, "share.sync", reviewlinks.ShareSyncIn{ID: out.Link.ID}, ops.Caller{}); s.Comments != 1 {
		t.Fatalf("a computer ten minutes slow didn't take a lease that ended a minute ago: %+v", s)
	}
	slow()

	// Two computers at once: the slow one's look holds the lease while the
	// fast one looks.
	pg.comment(t, 0, dana, "Dana", map[string]any{"n": 1, "text": "One."}, map[string]any{"n": 2, "text": "Two."})
	slow = testutil.SkewClock(t, -10*time.Minute)
	started := false
	var during *reviewlinks.ShareSyncOut
	var heldBy, after string
	reviewlinks.AfterInserts = func(ctx context.Context, a *app.App, id string) {
		if started {
			return
		}
		started = true
		heldBy = getLink(t, a, id).SyncBy
		slow()
		fast := testutil.SkewClock(t, 10*time.Minute)
		during = call[reviewlinks.ShareSyncOut](t, a, "share.sync", reviewlinks.ShareSyncIn{ID: id}, ops.Caller{})
		fast()
		slow = testutil.SkewClock(t, -10*time.Minute)
		after = getLink(t, a, id).SyncBy
	}
	defer func() { reviewlinks.AfterInserts = nil }()
	s := call[reviewlinks.ShareSyncOut](t, a, "share.sync", reviewlinks.ShareSyncIn{ID: out.Link.ID}, ops.Caller{})
	reviewlinks.AfterInserts = nil
	slow()
	if during == nil || during.Comments != 0 || heldBy == "" || after != heldBy {
		t.Fatalf("both computers held the lease: the fast one read %+v; held by %q, then %q", during, heldBy, after)
	}
	if s.Comments != 1 {
		t.Fatalf("the slow computer's look: %+v", s)
	}
	if l := getLink(t, a, out.Link.ID); l.Comments != 2 || l.SyncBy != "" {
		t.Fatalf("link: %d comments, lease %q", l.Comments, l.SyncBy)
	}
	if n, _ := a.DB.C(store.CComments).CountDocuments(ctx, bson.M{"outside.linkId": out.Link.ID}); n != 2 {
		t.Fatalf("comments: %d", n)
	}
}

// A move holds every link it deals with by the database server's clock, as
// looks do: a look's lease with two minutes left stops a move on a computer
// ten minutes fast; one that ended two minutes ago doesn't stop a move on a
// computer ten minutes slow; and while that move holds the links, a look on
// a computer ten minutes fast reads none of them.
func TestAMoveHoldsLinksByTheServersClock(t *testing.T) {
	w := newWorld(t)
	a := w.a
	ctx := context.Background()
	one := call[reviewlinks.ShareOut](t, a, "share.create", reviewlinks.ShareCreateIn{ProjectID: w.p.ID}, person)
	time.Sleep(5 * time.Millisecond)
	two := call[reviewlinks.ShareOut](t, a, "share.create", reviewlinks.ShareCreateIn{ProjectID: w.p.ID}, person)
	for _, l := range []*reviewlinks.ShareOut{one, two} {
		openPage(t, l.URL).comment(t, 0, dana, "Dana", map[string]any{"n": 1, "text": "Before the move."})
	}

	setLink(t, a, two.Link.ID, bson.M{"syncUntil": serverNow(t, a).Add(2 * time.Minute), "syncBy": "daemon"})
	fast := testutil.SkewClock(t, 10*time.Minute)
	if _, err := ops.ReviewLinksOnMove(ctx, a, a.Media, false); err == nil || code(err) != apperr.Conflict {
		t.Fatalf("a move on a computer ten minutes fast went through a look's lease: %v", err)
	}
	fast()
	if l := getLink(t, a, one.Link.ID); l.State != models.ReviewLinkLive {
		t.Fatalf("a refused move ended a link: %s", l.State)
	}

	setLink(t, a, two.Link.ID, bson.M{"syncUntil": serverNow(t, a).Add(-2 * time.Minute), "syncBy": "daemon"})
	slow := testutil.SkewClock(t, -10*time.Minute)
	started := false
	var during *reviewlinks.ShareSyncOut
	reviewlinks.AfterInserts = func(ctx context.Context, a *app.App, id string) {
		if started || id != one.Link.ID {
			return
		}
		started = true
		slow()
		fast := testutil.SkewClock(t, 10*time.Minute)
		during = call[reviewlinks.ShareSyncOut](t, a, "share.sync", reviewlinks.ShareSyncIn{ID: two.Link.ID}, ops.Caller{})
		fast()
		slow = testutil.SkewClock(t, -10*time.Minute)
	}
	defer func() { reviewlinks.AfterInserts = nil }()
	lines, err := ops.ReviewLinksOnMove(ctx, a, a.Media, false)
	reviewlinks.AfterInserts = nil
	slow()
	if err != nil || len(lines) != 2 {
		t.Fatalf("a move on a computer ten minutes slow, past a lease that ended: %v %v", lines, err)
	}
	if during == nil || during.Comments != 0 {
		t.Fatalf("a look on a computer ten minutes fast read through the move's hold: %+v", during)
	}
	if c := call[ops.CommentListOut](t, a, "comment.list", ops.CommentListIn{ProjectID: w.p.ID}, ops.Caller{}); len(c.Comments) != 2 {
		t.Fatalf("the last comments: %d", len(c.Comments))
	}
}

// A link takes in at most PerHour comments an hour, and the hour is the
// database server's: one computer starts it, another judges it. On a
// computer ten minutes fast an hour that began 59 minutes ago hasn't ended;
// on one ten minutes slow an hour that began 61 minutes ago has, and the
// new one begins at the server's time.
func TestTheHourIsTheServers(t *testing.T) {
	w := newWorld(t)
	a := w.a
	out := call[reviewlinks.ShareOut](t, a, "share.create", reviewlinks.ShareCreateIn{ProjectID: w.p.ID}, person)
	openPage(t, out.URL).comment(t, 0, dana, "Dana", map[string]any{"n": 1, "text": "One."}, map[string]any{"n": 2, "text": "Two."})

	setLink(t, a, out.Link.ID, bson.M{"hour": serverNow(t, a).Add(-59 * time.Minute), "inHour": reviewlinks.PerHour})
	fast := testutil.SkewClock(t, 10*time.Minute)
	if s := call[reviewlinks.ShareSyncOut](t, a, "share.sync", reviewlinks.ShareSyncIn{ID: out.Link.ID}, ops.Caller{}); s.Comments != 0 || s.Waiting != 2 {
		t.Fatalf("an hour with a minute left ended on a computer ten minutes fast: %+v", s)
	}
	fast()

	setLink(t, a, out.Link.ID, bson.M{"hour": serverNow(t, a).Add(-61 * time.Minute), "inHour": reviewlinks.PerHour})
	slow := testutil.SkewClock(t, -10*time.Minute)
	s := call[reviewlinks.ShareSyncOut](t, a, "share.sync", reviewlinks.ShareSyncIn{ID: out.Link.ID}, ops.Caller{})
	slow()
	if s.Comments != 2 || s.Waiting != 0 {
		t.Fatalf("an hour that ended a minute ago didn't on a computer ten minutes slow: %+v", s)
	}
	if l := getLink(t, a, out.Link.ID); l.Hour == nil || !near(*l.Hour, serverNow(t, a), time.Minute) || l.InHour != 2 {
		t.Fatalf("the new hour: began %v (the server's time %v), %d in it", l.Hour, serverNow(t, a), l.InHour)
	}
}
