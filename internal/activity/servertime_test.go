package activity

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/uploads"
)

func serverNow(t *testing.T, a *app.App) time.Time {
	t.Helper()
	now, err := a.DB.ServerNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return now
}

// A computer whose clock is off says the same as any other, working alone or
// not (D368): heartbeats, when a job may run and an upload's session are the
// database server's time. Off by a minute or by a few, its worker is online,
// a job waits for what it really waits for, a retry's wait reads right, and an
// upload still moving is neither stopped nor gone; an upload through this
// computer's dashboard goes by this computer's clock, whose file it is.
func TestASummaryIsRightOnAComputerWhoseClockIsOff(t *testing.T) {
	for _, skew := range []time.Duration{time.Minute, 3 * time.Minute, -time.Minute, -3 * time.Minute} {
		t.Run(skew.String(), func(t *testing.T) {
			a := testapp.New(t)
			ctx := context.Background()
			as := clipIn(t, a, "Harbour walk")
			retried := clipIn(t, a, "Pier walk")
			server := serverNow(t, a)
			setWorker(t, a, models.WorkerDevice{Name: "studio-mac", JobTypes: []string{jobs.TypeTranscribe}, Concurrency: 1, AcceptsHeavy: true, HeavyEnabled: true,
				Running: 1, LastHeartbeat: server.Add(-5 * time.Second)})
			addJob(t, a, models.Job{Type: jobs.TypeTranscribe, State: models.JobQueued, Heavy: true, AssetID: as.ID})
			backoff := addJob(t, a, models.Job{Type: jobs.TypeTranscribe, State: models.JobQueued, Heavy: true, AssetID: retried.ID})
			if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": backoff.ID}, bson.M{"$set": bson.M{"nextRunAt": server.Add(20 * time.Second)}}); err != nil {
				t.Fatal(err)
			}
			moving := models.IngestSession{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), InstallationID: a.Cfg.Installation.ID, SourceName: "harbour.mov", AssetID: ids.New(ids.Asset),
				State: "uploading", BytesDone: 40, Size: 100, Via: "cli", CreatedAt: server.Add(-time.Minute), UpdatedAt: server.Add(-5 * time.Second)}
			insert(t, a, store.CIngest, moving)

			testutil.SkewClock(t, skew)
			st := uploads.For(a.Cfg.StateDir())
			upload, err := st.Create("roses.mov", 1000, "", "")
			if err != nil {
				t.Fatal(err)
			}
			st.Update(upload.ID, true, func(r *uploads.Record) { r.Offset, r.Rate = 500, 100 })

			s := build(t, a)
			if len(s.Workers) != 1 {
				t.Fatalf("workers online: %+v", s.Workers)
			}
			if it := item(s, "asset:"+as.ID); it == nil || it.Waiting == nil || it.Waiting.Code == jobs.WaitNoWorker {
				t.Fatalf("a job with a worker online waits for one: %+v", it)
			}
			it := item(s, "asset:"+retried.ID)
			if it == nil || it.Waiting == nil || it.Waiting.Code != jobs.WaitRetry || !strings.Contains(it.Waiting.Text, "seconds") {
				t.Fatalf("a retry's wait of 20 seconds: %+v", it)
			}
			if it := item(s, "ingest:"+moving.ID); it == nil || it.State != StateActive || strings.Contains(it.StageLabel, "Stopped") {
				t.Fatalf("an upload that just moved: %+v", it)
			}
			if it := item(s, "upload:"+upload.ID); it == nil || it.State != StateActive {
				t.Fatalf("an upload through this computer's dashboard: %+v", it)
			}
		})
	}
}
