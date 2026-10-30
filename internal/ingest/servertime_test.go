package ingest

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// An upload's session says when it last moved in the database server's time,
// and reconcile judges it abandoned by the server's time (D360): a computer
// whose clock is ten minutes fast never fails an upload another computer is
// still sending, and one ten minutes slow finds one that stopped.
func TestReconcileGoesByTheServersTime(t *testing.T) {
	a := testapp.New(t)
	needProbe(t, a)
	ctx := context.Background()

	// Sent from a computer whose clock is ten minutes slow.
	restore := testutil.SkewClock(t, -10*time.Minute)
	res := File(ctx, a, tone(t, t.TempDir(), "harbour.wav", 1, 330), Options{Via: "cli"})
	restore()
	var sess models.IngestSession
	if err := a.DB.GetByID(ctx, store.CIngest, a.WorkspaceID(), res.SessionID, &sess); err != nil {
		t.Fatal(err)
	}
	server, err := a.DB.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d := server.Sub(sess.UpdatedAt); d < 0 || d > time.Minute {
		t.Fatalf("the session moved %v before the server's now", d)
	}

	mk := func(ago time.Duration) models.IngestSession {
		t.Helper()
		s := models.IngestSession{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), SourceName: "pier.mov", AssetID: ids.New(ids.Asset), State: "uploading", Via: "cli",
			Upload: models.UploadState{Key: "ws/pier"}, CreatedAt: server.Add(-ago), UpdatedAt: server.Add(-ago)}
		if err := a.DB.Insert(ctx, store.CIngest, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	moving := mk(time.Hour - 5*time.Minute)
	stopped := mk(time.Hour + 5*time.Minute)
	for _, skew := range []time.Duration{10 * time.Minute, -10 * time.Minute} {
		restore := testutil.SkewClock(t, skew)
		out, err := ops.CallTyped[ReconcileOut](ctx, a, "ingest.reconcile", ReconcileIn{OlderThanHours: 1, DryRun: true}, ops.Caller{})
		restore()
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(out.StaleSessions, moving.ID) || !slices.Contains(out.StaleSessions, stopped.ID) {
			t.Fatalf("a computer %v off: abandoned %v (moving %s, stopped %s)", skew, out.StaleSessions, moving.ID, stopped.ID)
		}
	}
}
