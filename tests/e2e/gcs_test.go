package e2e

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/gcsemu"
	"github.com/metavert-io/metarush/internal/tools"
)

// An ingest into Google Cloud Storage (in-process emulator, see gcsemu) that is
// interrupted mid-upload resumes after a "restart" — a fresh App and driver —
// from the resumable session recorded in the ingest session, without
// re-sending committed bytes. Abandoned sessions are canceled by reconcile.
func TestGCSIngestResumesAfterRestart(t *testing.T) {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	db := testutil.DB(t)
	emu := gcsemu.Start(t, "media")
	t.Setenv("STORAGE_EMULATOR_HOST", emu.Host) // how the client library targets emulators (no auth)
	ctx := context.Background()
	wsID := ids.New(ids.Workspace)
	if err := db.Insert(ctx, store.CWorkspaces, models.Workspace{ID: wsID, WorkspaceID: wsID, Name: "GCS", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	h := t.TempDir()
	instID := ids.New(ids.Installation)
	open := func() *app.App {
		cfg := config.Default(h)
		cfg.SetHome(h)
		cfg.Workspace = config.Ref{ID: wsID, Name: "GCS"}
		cfg.Installation = config.Ref{ID: instID, Name: "gcs-machine"}
		cfg.User = config.UserRef{ID: "usr_gcs", Name: "gcs-user"}
		cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
		cfg.Media = config.Media{Provider: "gcs", GCS: config.GCS{Bucket: "media", Prefix: "team"}}
		a, err := app.OpenWith(ctx, cfg, "cli")
		if err != nil {
			t.Fatal(err)
		}
		if a.Media.Name() != "gcs" || a.Media.Locator() != "gs://media/team@"+emu.Host {
			t.Fatalf("config wiring: %s %s", a.Media.Name(), a.Media.Locator())
		}
		// Same store, smaller chunks than the 16 MiB default so a short clip spans many.
		g, err := media.NewGCS(media.GCSConfig{Bucket: "media", Prefix: "team", ChunkSize: 256 << 10, RetryBackoff: time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		a.Media = g
		return a
	}
	A := open()
	proj, err := ops.CallTyped[ops.ProjectOut](ctx, A, "project.create", ops.ProjectCreateIn{Name: "GCS film"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	file := clip(t, ts, dir, "take.mp4", "testsrc2=size=1280x720:rate=30:duration=4")
	st, _ := os.Stat(file)
	if st.Size() < 1<<20 {
		t.Fatalf("fixture too small (%d bytes) to interrupt", st.Size())
	}
	interrupt := func(a *app.App, path string) ingest.Result {
		cctx, cancel := context.WithCancel(ctx)
		defer cancel()
		return ingest.File(cctx, a, path, ingest.Options{ProjectID: proj.Project.ID, Via: "cli", Progress: func(_, phase string, done, total int64) {
			if phase == "upload" && done >= 512<<10 && done < total {
				cancel()
			}
		}})
	}
	res := interrupt(A, file)
	if res.Status != "failed" {
		t.Fatalf("interrupted ingest: %+v", res)
	}
	var sess models.IngestSession
	if err := A.DB.C(store.CIngest).FindOne(ctx, bson.M{"workspaceId": wsID, "assetId": res.AssetID}).Decode(&sess); err != nil {
		t.Fatal(err)
	}
	if sess.State != "uploading" || !strings.Contains(sess.Upload.SessionURI, "upload_id=") {
		t.Fatalf("ingest session must keep the GCS session for resume: %+v", sess)
	}
	// The session URI is a write capability: stored, never printed or sent to the dashboard.
	list, err := ops.CallTyped[ingest.SessionsOut](ctx, A, "ingest.sessions", ingest.SessionsIn{}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(list); strings.Contains(string(b), "upload_id") || strings.Contains(string(b), "sessionUri") {
		t.Fatalf("session URI leaked into op output: %s", b)
	}
	A.Close() // the process "exits"

	B := open() // …and a new one starts
	res2 := ingest.File(ctx, B, file, ingest.Options{ProjectID: proj.Project.ID, Via: "cli"})
	if res2.Status != "resumed" || res2.AssetID != res.AssetID {
		t.Fatalf("resume after restart: %+v", res2)
	}
	stats := emu.Stats()
	if stats.Initiations != 1 || stats.StatusQueries == 0 || stats.ChunkBytes != st.Size() {
		t.Fatalf("expected one session resumed via a status query with no byte sent twice: %+v (file %d bytes)", stats, st.Size())
	}
	var as models.Asset
	_ = B.DB.GetByID(ctx, store.CAssets, wsID, res.AssetID, &as)
	if as.State != models.AssetAvailable {
		t.Fatalf("asset %s", as.State)
	}
	rc, _, err := B.Media.Get(ctx, as.StorageKey, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	sum, _ := cacheHash(rc)
	rc.Close()
	if sum != as.SHA256 {
		t.Fatal("stored object differs from the original")
	}

	// An abandoned GCS session (not listable) is canceled by reconcile from the
	// URI recorded in the ingest session.
	file2 := clip(t, ts, dir, "take2.mp4", "mandelbrot=size=1280x720:rate=30")
	res3 := interrupt(B, file2)
	if res3.Status != "failed" || emu.Stats().Open != 1 {
		t.Fatalf("second interrupted ingest: %+v %+v", res3, emu.Stats())
	}
	_, _ = B.DB.C(store.CIngest).UpdateMany(ctx, bson.M{"assetId": res3.AssetID}, bson.M{"$set": bson.M{"updatedAt": time.Now().Add(-3 * time.Hour)}})
	rec, err := ops.CallTyped[ingest.ReconcileOut](ctx, B, "ingest.reconcile", ingest.ReconcileIn{OlderThanHours: 1}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.AbortedUploads) != 1 || len(rec.StaleSessions) != 1 || emu.Stats().Open != 0 {
		t.Fatalf("reconcile: %+v, open sessions %d", rec, emu.Stats().Open)
	}
}

func cacheHash(r io.Reader) (string, error) {
	f, err := os.CreateTemp("", "gcs-e2e-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	return media.HashFile(f.Name())
}
