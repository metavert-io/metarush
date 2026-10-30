package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// Sessions start at the database server's time and "sign out everywhere" is
// the server's time too (D360), so neither the dashboard's clock nor the
// clock of the computer where someone signed out decides. A browser that
// signed in on a dashboard ten minutes fast is signed out by a sign-out a
// moment later, one signed out on a computer ten minutes slow is signed out
// too, and one that signs in on a dashboard ten minutes slow right after a
// sign-out on a computer ten minutes fast stays signed in. The owner's
// one-time link sessions on this computer follow the same clock.
func TestSessionsAndSignOutsFollowTheServersClock(t *testing.T) {
	d, _, _ := newRemoteDash(t)
	d.signIn()
	ctx := context.Background()
	d.op("auth.method.set", map[string]any{"method": "password", "email": "owner@example.com", "password": "tomato garden at dusk"}, nil)
	var inv auth.UserLinkOut
	d.op("users.invite", map[string]any{"email": "maya@example.com", "name": "Maya Lindqvist"}, &inv)
	token := inv.Link[strings.LastIndex(inv.Link, "/")+1:]
	if res, _, raw := d.tailnet("", "").do("POST", "/api/auth/link/"+token, map[string]any{"password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("accept: %s", raw)
	}
	maya := inv.Account.ID
	tenFast, tenSlow := 10*time.Minute, -10*time.Minute

	// skewed runs fn on a computer whose clock is off by skew.
	skewed := func(skew time.Duration, fn func()) {
		restore := testutil.SkewClock(t, skew)
		defer restore()
		fn()
	}
	login := func(skew time.Duration) *browser {
		t.Helper()
		b := d.tailnet("", "")
		skewed(skew, func() {
			if res, _, raw := b.do("POST", "/api/auth/login", map[string]any{"email": "maya@example.com", "password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
				t.Fatalf("sign in on a dashboard %v off: %s", skew, raw)
			}
		})
		return b
	}
	signedIn := func(b *browser) bool {
		t.Helper()
		res, _, _ := b.do("GET", "/api/session", nil, false, nil)
		return res.StatusCode == http.StatusOK
	}
	signOut := func(skew time.Duration, userID string) {
		t.Helper()
		skewed(skew, func() {
			if err := auth.SignOutEverywhere(ctx, d.a, userID); err != nil {
				t.Fatal(err)
			}
		})
	}

	b := login(tenFast)
	if !signedIn(b) {
		t.Fatal("not signed in on a dashboard ten minutes fast")
	}
	signOut(0, maya)
	if signedIn(b) {
		t.Fatal("a sign-out a moment after signing in on a dashboard ten minutes fast left the browser signed in")
	}
	b = login(0)
	signOut(tenSlow, maya)
	if signedIn(b) {
		t.Fatal("a sign-out on a computer ten minutes slow left the browser signed in")
	}
	signOut(tenFast, maya)
	b = login(tenSlow)
	if !signedIn(b) {
		t.Fatal("signing in on a dashboard ten minutes slow right after a sign-out on a computer ten minutes fast didn't last")
	}

	// The owner, signed in on this computer with one-time links.
	owner := d.a.Cfg.User.ID
	skewed(tenFast, d.signIn)
	signOut(tenSlow, owner)
	if code, _ := d.do("GET", "/api/session", nil, false); code != http.StatusUnauthorized {
		t.Fatalf("the owner's link session on a computer ten minutes fast survived a sign-out on one ten minutes slow: %d", code)
	}
	signOut(tenFast, owner)
	skewed(tenSlow, d.signIn)
	if code, _ := d.do("GET", "/api/session", nil, false); code != http.StatusOK {
		t.Fatalf("the owner's link session made right after a sign-out on a computer ten minutes fast: %d", code)
	}
}

// Where change streams aren't available the dashboard polls, and changes carry
// one of two clocks: the database server's for what other computers judge
// (a heartbeat), the writer's for plain records (a project's updatedAt). A
// computer whose clock is off misses neither (D368).
func TestPollingFindsChangesOfEitherClock(t *testing.T) {
	d, projectID, _ := newDash(t, noClaude)
	ctx := context.Background()
	b := d.srv.Broker()
	ch := make(chan Event, 1024)
	b.mu.Lock()
	b.subs[ch] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}()
	w := models.WorkerDevice{ID: d.a.Cfg.Installation.ID, WorkspaceID: d.a.WorkspaceID(), Name: "studio-mac"}
	if _, err := d.a.DB.C(store.CWorkers).ReplaceOne(ctx, bson.M{"_id": w.ID}, w, options.Replace().SetUpsert(true)); err != nil {
		t.Fatal(err)
	}
	for _, skew := range []time.Duration{time.Minute, -time.Minute} {
		restore := testutil.SkewClock(t, skew)
		for len(ch) > 0 {
			<-ch
		}
		server, err := d.a.DB.ServerNow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.a.DB.C(store.CWorkers).UpdateOne(ctx, bson.M{"_id": w.ID}, bson.M{"$set": bson.M{"lastHeartbeat": server}}); err != nil {
			t.Fatal(err)
		}
		if _, err := d.a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": projectID}, bson.M{"$set": bson.M{"updatedAt": store.Clock().UTC()}}); err != nil {
			t.Fatal(err)
		}
		pctx, cancel := context.WithTimeout(ctx, 3500*time.Millisecond)
		b.poll(pctx, 3500*time.Millisecond)
		cancel()
		restore()
		heartbeat, project := false, false
		for len(ch) > 0 {
			e := <-ch
			heartbeat = heartbeat || (e.Type == store.CWorkers && e.ID == w.ID)
			project = project || (e.Type == store.CProjects && e.ID == projectID)
		}
		if !heartbeat {
			t.Errorf("%v off: polling missed a change in the server's time", skew)
		}
		if !project {
			t.Errorf("%v off: polling missed a change by this computer's clock", skew)
		}
	}
}
