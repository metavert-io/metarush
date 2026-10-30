package ops

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/ids"
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

// The worker list, sync status and a job's details say the same when this
// computer's clock isn't the server's (D368): heartbeats, when a job may run
// and an upload's session are the database server's time, so a computer a few
// minutes off doesn't call itself offline, an upload stopped, or a retry's wait
// minutes long. The server's clock really differs here (ServerOff), so reading
// time.Now() fails this.
func TestWhatPeopleSeeGoesByTheServersTime(t *testing.T) {
	for _, off := range []time.Duration{time.Minute, 3 * time.Minute, -time.Minute, -3 * time.Minute} {
		t.Run(off.String(), func(t *testing.T) {
			a := testapp.New(t)
			ctx := context.Background()
			testutil.ServerOff(t, a.DB, off)
			server, err := a.DB.Now(ctx)
			if err != nil {
				t.Fatal(err)
			}
			w := models.WorkerDevice{ID: a.Cfg.Installation.ID, WorkspaceID: a.WorkspaceID(), Name: "studio-mac", JobTypes: []string{jobs.TypeProxy}, Concurrency: 1,
				LastHeartbeat: server.Add(-5 * time.Second)}
			if _, err := a.DB.C(store.CWorkers).ReplaceOne(ctx, bson.M{"_id": w.ID}, w, options.Replace().SetUpsert(true)); err != nil {
				t.Fatal(err)
			}
			j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeProxy, IdempotencyKey: "proxy:seen", MaxAttempts: 3})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID}, bson.M{"$set": bson.M{"nextRunAt": server.Add(20 * time.Second)}}); err != nil {
				t.Fatal(err)
			}
			session := models.IngestSession{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), InstallationID: w.ID, SourceName: "pier.mov", AssetID: ids.New(ids.Asset),
				State: "uploading", Size: 100, BytesDone: 40, Via: "cli", CreatedAt: server.Add(-time.Minute), UpdatedAt: server.Add(-5 * time.Second)}
			if err := a.DB.Insert(ctx, store.CIngest, session); err != nil {
				t.Fatal(err)
			}
			list, err := CallTyped[WorkerListOut](ctx, a, "worker.list", WorkerListIn{}, Caller{})
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Workers) != 1 || !list.Workers[0].Online {
				t.Fatalf("worker.list says %+v", list.Workers)
			}
			sync, err := CallTyped[SyncStatusOut](ctx, a, "sync.status", SyncStatusIn{}, Caller{})
			if err != nil {
				t.Fatal(err)
			}
			if !sync.ThisWorkerOnline || len(sync.Workers) != 1 || !sync.Workers[0].Online {
				t.Fatalf("sync.status says offline: %+v", sync.Workers)
			}
			if len(sync.Ingest) != 1 || sync.Ingest[0].Status != "active" {
				t.Fatalf("an upload that just moved: %+v", sync.Ingest)
			}
			show, err := CallTyped[JobShowOut](ctx, a, "job.show", JobIDIn{ID: j.ID}, Caller{})
			if err != nil {
				t.Fatal(err)
			}
			if show.Waiting == nil || show.Waiting.Code != jobs.WaitRetry || !strings.Contains(show.Waiting.Text, "seconds") {
				t.Fatalf("a retry's wait of 20 seconds: %+v", show.Waiting)
			}
		})
	}
}
