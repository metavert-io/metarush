package activity

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/uploads"
)

// A computer whose clock isn't the server's says the same as any other,
// working alone or not (D368): heartbeats, when a job may run and an upload's
// session are the database server's time. A minute or a few apart, its worker
// is online, a job waits for what it really waits for, a retry's wait reads
// right, and an upload still moving is neither stopped nor gone; an upload
// through this computer's dashboard goes by this computer's clock, whose file
// it is. The server's clock really differs here (ServerOff), so a summary that
// reads time.Now() fails this.
func TestASummaryIsRightWhenTheServersClockDiffers(t *testing.T) {
	for _, off := range []time.Duration{time.Minute, 3 * time.Minute, -time.Minute, -3 * time.Minute} {
		t.Run(off.String(), func(t *testing.T) {
			a := testapp.New(t)
			ctx := context.Background()
			testutil.ServerOff(t, a.DB, off)
			server, err := a.DB.Now(ctx)
			if err != nil {
				t.Fatal(err)
			}
			as := clipIn(t, a, "Harbour walk")
			retried := clipIn(t, a, "Pier walk")
			setWorker(t, a, models.WorkerDevice{Name: "studio-mac", JobTypes: []string{jobs.TypeTranscribe}, Concurrency: 1, AcceptsHeavy: true, HeavyEnabled: true,
				Running: 1, LastHeartbeat: server.Add(-5 * time.Second)})
			due := addJob(t, a, models.Job{Type: jobs.TypeTranscribe, State: models.JobQueued, Heavy: true, AssetID: as.ID})
			backoff := addJob(t, a, models.Job{Type: jobs.TypeTranscribe, State: models.JobQueued, Heavy: true, AssetID: retried.ID})
			for id, next := range map[string]time.Time{due.ID: server.Add(-time.Second), backoff.ID: server.Add(20 * time.Second)} {
				if _, err := a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"nextRunAt": next}}); err != nil {
					t.Fatal(err)
				}
			}
			moving := models.IngestSession{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), InstallationID: a.Cfg.Installation.ID, SourceName: "harbour.mov", AssetID: ids.New(ids.Asset),
				State: "uploading", BytesDone: 40, Size: 100, Via: "cli", CreatedAt: server.Add(-time.Minute), UpdatedAt: server.Add(-5 * time.Second)}
			insert(t, a, store.CIngest, moving)
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
			if it := item(s, "asset:"+as.ID); it == nil || it.Waiting == nil || it.Waiting.Code == jobs.WaitNoWorker || it.Waiting.Code == jobs.WaitRetry {
				t.Fatalf("a due job with a worker online: %+v", it)
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

// How long a shot has been making reads right: the ticket is stamped in the
// server's time, like the summary's now, so a computer a minute off doesn't
// say "1 minute so far" the moment it is sent (D360, D371+).
func TestAShotsTimeSoFarIsTheServers(t *testing.T) {
	for _, off := range []time.Duration{time.Minute, -time.Minute} {
		t.Run(off.String(), func(t *testing.T) {
			a := testapp.New(t)
			ctx := context.Background()
			testutil.ServerOff(t, a.DB, off)
			now, err := a.DB.Now(ctx)
			if err != nil {
				t.Fatal(err)
			}
			g := models.Generation{ID: ids.New(ids.Generation), WorkspaceID: a.WorkspaceID(), Provider: "gemini", Model: "m", Prompt: "a pier", CreatedAt: now,
				Ticket: &models.GenTicket{ID: "t1", SubmittedAt: now}}
			insert(t, a, store.CGenerations, g)
			j := addJob(t, a, models.Job{Type: jobs.TypeGenerate, State: models.JobRunning, Progress: models.Progress{Stage: "making"},
				Payload: map[string]any{"generationId": g.ID}, Lease: &models.Lease{Owner: "ins_x", OwnerName: "laptop", ExpiresAt: now.Add(time.Minute)}})
			s := build(t, a)
			it := item(s, "generate:"+j.ID)
			if it == nil {
				t.Fatalf("no row for the shot: %+v", s.Items)
			}
			if strings.Contains(it.StageLabel, "so far") {
				t.Fatalf("just sent, and it says: %q", it.StageLabel)
			}
		})
	}
}
