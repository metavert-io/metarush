package transcripts

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func serverNow(t *testing.T, a *app.App) time.Time {
	t.Helper()
	now, err := a.DB.ServerNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return now
}

func leaseOf(t *testing.T, a *app.App, assetID string) *models.TranscriptLock {
	t.Helper()
	var as models.Asset
	if err := a.DB.GetByID(context.Background(), store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		t.Fatal(err)
	}
	if as.TextFixes == nil {
		return nil
	}
	return as.TextFixes.Lock
}

// tryHold asks for a clip's lease, waiting at most a while (a lease another
// computer holds is waited for up to 15 s otherwise).
func tryHold(a *app.App, assetID string, wait time.Duration) (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	return Hold(ctx, a, assetID)
}

// A clip's lease is written and looked at by the database server's clock
// (D360): a computer whose clock is ten minutes ahead neither takes a lease
// another computer holds nor saves a version as if no fix were under way,
// and one ten minutes behind takes a lease that has ended.
func TestTheClipsLeaseFollowsTheServersClock(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	as := clip(t, a, "", words("We@0-0.2 met@0.25-0.4 Fern@0.5-0.7 Brook@0.75-1"))

	// A computer behind takes the lease: it ends lockFor after the server's now.
	restore := testutil.SkewClock(t, -10*time.Minute)
	before := serverNow(t, a)
	release, err := tryHold(a, as.ID, 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	l := leaseOf(t, a, as.ID)
	if l == nil || l.Until.Before(before.Add(lockFor-time.Second)) || l.Until.After(serverNow(t, a).Add(lockFor+time.Second)) {
		t.Fatalf("the lease ends at %v, want about %v", l, before.Add(lockFor))
	}
	restore()

	// Another computer, ahead, can't take it, and sees the fix under way.
	restore = testutil.SkewClock(t, 10*time.Minute)
	if _, err := tryHold(a, as.ID, time.Second); err == nil {
		t.Fatal("a computer ahead took a lease another computer holds")
	}
	if st, err := textState(ctx, a, as.ID); err != nil || !st.busy {
		t.Fatalf("a fix under way on another computer: %+v %v", st, err)
	}
	restore()
	release()

	// A lease still running for 20 s by the server's clock: still held, for a computer ahead too.
	set := func(until time.Time) {
		t.Helper()
		if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": as.ID}, bson.M{"$set": bson.M{"textFixes.lock": models.TranscriptLock{Token: "elsewhere", Until: until}}}); err != nil {
			t.Fatal(err)
		}
	}
	set(serverNow(t, a).Add(20 * time.Second))
	restore = testutil.SkewClock(t, 10*time.Minute)
	if _, err := tryHold(a, as.ID, time.Second); err == nil {
		t.Fatal("a computer ahead took a lease with 20 s left")
	}
	if st, _ := textState(ctx, a, as.ID); !st.busy {
		t.Fatal("a computer ahead took a lease with 20 s left as ended")
	}
	restore()

	// One that ended 2 s ago by the server's clock: a computer behind takes it.
	set(serverNow(t, a).Add(-2 * time.Second))
	restore = testutil.SkewClock(t, -10*time.Minute)
	if st, _ := textState(ctx, a, as.ID); st.busy {
		t.Fatal("a computer behind took an ended lease as running")
	}
	release, err = tryHold(a, as.ID, 10*time.Second)
	if err != nil {
		t.Fatalf("a computer behind couldn't take an ended lease: %v", err)
	}
	if l := leaseOf(t, a, as.ID); l == nil || l.Token == "elsewhere" {
		t.Fatalf("the lease after taking it: %+v", l)
	}
	release()
	restore()
}
