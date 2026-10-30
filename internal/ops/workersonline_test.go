package ops

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

// Whether a Mac in the workspace can look at clips (its worker said so in the
// last 30 days) goes by the database server's time, as heartbeats are written
// (D360), whatever this computer's clock says.
func TestVisualsPossibleGoesByTheServersTime(t *testing.T) {
	a := testapp.New(t)
	// This computer can't look at clips itself (no FFmpeg for it).
	a.Cfg.Tools.FFmpeg = filepath.Join(t.TempDir(), "no-ffmpeg")
	a.Tools = tools.New(a.Cfg.Tools)
	if vision.CanRun(a.Tools) {
		t.Skip("this computer looks at clips itself")
	}
	ctx := context.Background()
	possible := func(skew, ago time.Duration) bool {
		t.Helper()
		server, err := a.DB.ServerNow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		w := models.WorkerDevice{ID: "ins_mac", WorkspaceID: a.WorkspaceID(), Name: "studio-mac", JobTypes: []string{jobs.TypeVisual}, LastHeartbeat: server.Add(-ago)}
		if _, err := a.DB.C(store.CWorkers).ReplaceOne(ctx, bson.M{"_id": w.ID}, w, options.Replace().SetUpsert(true)); err != nil {
			t.Fatal(err)
		}
		restore := testutil.SkewClock(t, skew)
		defer restore()
		return visualsPossible(ctx, a)
	}
	month := 30 * 24 * time.Hour
	if !possible(10*time.Minute, month-5*time.Minute) {
		t.Fatal("a computer ten minutes fast forgot a Mac seen 5 minutes inside the 30 days")
	}
	if possible(-10*time.Minute, month+5*time.Minute) {
		t.Fatal("a computer ten minutes slow counted a Mac last seen 5 minutes past the 30 days")
	}
}
