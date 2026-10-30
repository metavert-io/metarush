package wsarchive

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// The export's note about computers running MetaRush goes by the database
// server's time, as lastSeenAt is written (D360), whatever this computer's
// clock says.
func TestRunningNowGoesByTheServersTime(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	server, err := db.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range []models.Installation{
		{ID: "ins_here", WorkspaceID: "ws_1", Name: "Studio Mac", State: models.InstallationActive, LastSeenAt: server},
		{ID: "ins_desk", WorkspaceID: "ws_1", Name: "Desk", State: models.InstallationActive, LastSeenAt: server.Add(-2 * time.Minute)},
		{ID: "ins_attic", WorkspaceID: "ws_1", Name: "Attic", State: models.InstallationActive, LastSeenAt: server.Add(-4 * time.Minute)},
	} {
		if err := db.Insert(ctx, store.CInstallations, in); err != nil {
			t.Fatal(err)
		}
	}
	for _, skew := range []time.Duration{10 * time.Minute, -10 * time.Minute} {
		restore := testutil.SkewClock(t, skew)
		got := runningNow(ctx, db, "ws_1", "ins_here")
		restore()
		if !slices.Equal(got, []string{"Desk"}) {
			t.Fatalf("a computer %v off: running %v, want Desk", skew, got)
		}
	}
}
