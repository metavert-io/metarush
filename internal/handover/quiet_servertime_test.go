package handover

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// When a computer was last seen is the database server's time, and so is how
// long ago it says that was, whatever this computer's clock says (D368).
func TestLastSeenWordsGoByTheServersTime(t *testing.T) {
	for _, off := range []time.Duration{10 * time.Minute, -10 * time.Minute} {
		a := testapp.New(t)
		ctx := context.Background()
		testutil.ServerOff(t, a.DB, off)
		server, err := a.DB.Now(ctx)
		if err != nil {
			t.Fatal(err)
		}
		gone := models.Installation{ID: "ins_gone", WorkspaceID: a.WorkspaceID(), Name: "Old laptop", State: models.InstallationActive, LastSeenAt: server.Add(-5*time.Minute - 10*time.Second)}
		if err := a.DB.Insert(ctx, store.CInstallations, gone); err != nil {
			t.Fatal(err)
		}
		ok, why, err := quietEnough(ctx, a, &gone)
		if err != nil || ok {
			t.Fatalf("seen five minutes ago: %v %v", ok, err)
		}
		if !strings.Contains(why, "was seen 5 minutes ago") {
			t.Fatalf("a server %v from this computer says: %s", off, why)
		}
	}
}
