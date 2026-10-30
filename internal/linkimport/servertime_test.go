package linkimport_test

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Whether this computer's MetaRush runs imports goes by its heartbeat in the
// database server's time (D360), whatever this computer's clock says: a
// terminal starts its own worker for an import only when it doesn't.
func TestImportsRunningGoesByTheServersTime(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	runs := func(skew, ago time.Duration) bool {
		t.Helper()
		server, err := a.DB.ServerNow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		w := models.WorkerDevice{ID: a.Cfg.Installation.ID, WorkspaceID: a.WorkspaceID(), Name: "studio-mac", JobTypes: []string{jobs.TypeImport},
			LastHeartbeat: server.Add(-ago)}
		if _, err := a.DB.C(store.CWorkers).ReplaceOne(ctx, bson.M{"_id": w.ID}, w, options.Replace().SetUpsert(true)); err != nil {
			t.Fatal(err)
		}
		restore := testutil.SkewClock(t, skew)
		defer restore()
		return linkimport.Running(ctx, a)
	}
	if !runs(10*time.Minute, 30*time.Second) {
		t.Fatal("a computer ten minutes fast took a worker that heartbeated 30 s ago for stopped")
	}
	if runs(-10*time.Minute, time.Minute) {
		t.Fatal("a computer ten minutes slow took a worker silent for a minute for running")
	}
}
