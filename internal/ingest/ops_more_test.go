package ingest

import (
	"context"
	"image/color"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// `metarush ingest`: a dry run lists what would be added; a real run adds
// footage and pictures, counts what happened and says the originals are safe.
func TestIngestFilesOp(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	newProject(t, a, "Garden film")
	dir := t.TempDir()
	wav := tone(t, dir, "birdsong.wav", 1, 440)
	pic := logo(t, dir, "logo.png", color.RGBA{30, 140, 60, 255})
	notes := writeAt(t, filepath.Join(dir, "notes.mov"), []byte("a shot list, not a video"), time.Minute)
	missing := filepath.Join(dir, "gone.wav")
	files := func(in FilesIn) (*FilesOut, error) {
		return ops.CallTyped[FilesOut](ctx, a, "ingest.files", in, ops.Caller{})
	}

	dry, err := files(FilesIn{Paths: []string{dir, missing}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || len(dry.Results) != 4 || dry.Results[0].Status != "failed" || dry.Results[0].Name != "gone.wav" {
		t.Fatalf("dry run: %+v", dry.Results)
	}
	for _, r := range dry.Results[1:] {
		if r.Status != "would-ingest" {
			t.Fatalf("dry run: %+v", dry.Results)
		}
	}
	if n, err := a.DB.C(store.CAssets).CountDocuments(ctx, map[string]any{"workspaceId": a.WorkspaceID()}); err != nil || n != 0 {
		t.Fatalf("a dry run adds nothing: %d %v", n, err)
	}
	if empty, err := files(FilesIn{Paths: []string{t.TempDir()}, DryRun: true}); err != nil || empty.Results == nil || len(empty.Results) != 0 {
		t.Fatalf("an empty folder: %+v %v", empty, err)
	}
	if _, err := files(FilesIn{Paths: []string{wav}, Duplicates: "maybe"}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("a duplicates choice that isn't one: %v", err)
	}
	if _, err := files(FilesIn{Paths: []string{wav}, ProjectID: "No such project"}); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("an unknown project: %v", err)
	}

	needProbe(t, a)
	out, err := files(FilesIn{Paths: []string{wav, pic, notes, missing}, ProjectID: "Garden film"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Ingested != 2 || out.Skipped != 1 || out.Failed != 1 || out.Note != "The originals are safely stored. Preview copies, transcripts and the rest are being prepared." {
		t.Fatalf("ingest: %+v", out)
	}
	no := false
	again, err := files(FilesIn{Paths: []string{wav, copyFile(t, wav, filepath.Join(t.TempDir(), "copy.wav"))}, Transcribe: &no})
	if err != nil || again.Deduped != 1 || again.Duplicates != 1 || again.Note != "" {
		t.Fatalf("again: %+v %v", again, err)
	}
}

// Uploads that were left unfinished are cleaned up (never anything stored):
// a dry run lists them, a real run aborts them and marks their sessions and
// clips as failed, with a plain reason.
func TestIngestSessionsAndReconcile(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(state string, age time.Duration, uri string) models.IngestSession {
		t.Helper()
		as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: state + ".mov", SHA256: ids.New("sha"), State: models.AssetStaging,
			ProjectIDs: []string{}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			t.Fatal(err)
		}
		s := models.IngestSession{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), SourceName: as.Name, AssetID: as.ID, State: state, Via: "cli",
			Upload: models.UploadState{Key: "ws/" + as.ID, SessionURI: uri}, CreatedAt: now.Add(-age), UpdatedAt: now.Add(-age)}
		if err := a.DB.Insert(ctx, store.CIngest, s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	stale := mk("uploading", 48*time.Hour, "")
	checking := mk("verifying", 30*time.Hour, "")
	finished := mk("available", 72*time.Hour, "")
	recent := mk("uploading", 2*time.Hour, "")
	gcs := mk("uploading", 50*time.Hour, "https://storage.example/upload?upload_id=abc")
	staging := filepath.Join(a.Cfg.Media.Filesystem.Root, ".staging")
	oldPart := writeAt(t, filepath.Join(staging, "0123abcd.part"), []byte("half a file"), 48*time.Hour)
	newPart := writeAt(t, filepath.Join(staging, "4567ef01.part"), []byte("still arriving"), time.Second)

	sessions := func(in SessionsIn) []models.IngestSession {
		t.Helper()
		out, err := ops.CallTyped[SessionsOut](ctx, a, "ingest.sessions", in, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		return out.Sessions
	}
	if all := sessions(SessionsIn{}); len(all) != 5 || all[0].ID != recent.ID || all[4].ID != finished.ID {
		t.Fatalf("all sessions, newest first: %+v", all)
	}
	if up := sessions(SessionsIn{State: "uploading", Limit: 2}); len(up) != 2 || up[0].ID != recent.ID {
		t.Fatalf("uploading: %+v", up)
	}
	if none := sessions(SessionsIn{State: "failed"}); none == nil || len(none) != 0 {
		t.Fatalf("none failed: %+v", none)
	}

	reconcile := func(in ReconcileIn) *ReconcileOut {
		t.Helper()
		out, err := ops.CallTyped[ReconcileOut](ctx, a, "ingest.reconcile", in, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	idsOf := func(ss ...models.IngestSession) []string {
		var out []string
		for _, s := range ss {
			out = append(out, s.ID)
		}
		slices.Sort(out)
		return out
	}
	dry := reconcile(ReconcileIn{DryRun: true})
	got := append([]string{}, dry.StaleSessions...)
	slices.Sort(got)
	if !dry.DryRun || !slices.Equal(got, idsOf(stale, checking, gcs)) || !slices.Contains(dry.AbortedUploads, "0123abcd.part") ||
		!slices.Contains(dry.AbortedUploads, gcs.Upload.Key) || slices.Contains(dry.AbortedUploads, "4567ef01.part") {
		t.Fatalf("dry run: %+v", dry)
	}
	if _, err := os.Stat(oldPart); err != nil {
		t.Fatal("a dry run removed an unfinished upload")
	}
	var s models.IngestSession
	if err := a.DB.GetByID(ctx, store.CIngest, a.WorkspaceID(), stale.ID, &s); err != nil || s.State != "uploading" {
		t.Fatalf("a dry run changed a session: %+v %v", s, err)
	}

	real := reconcile(ReconcileIn{})
	if len(real.StaleSessions) != 3 {
		t.Fatalf("reconcile: %+v", real)
	}
	if _, err := os.Stat(oldPart); !os.IsNotExist(err) {
		t.Fatal("the abandoned upload is still there")
	}
	if _, err := os.Stat(newPart); err != nil {
		t.Fatal("an upload still arriving was removed")
	}
	for _, x := range []models.IngestSession{stale, checking, gcs} {
		if err := a.DB.GetByID(ctx, store.CIngest, a.WorkspaceID(), x.ID, &s); err != nil || s.State != "failed" || s.Error != "abandoned (reconciled)" {
			t.Fatalf("session %+v %v", s, err)
		}
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), x.AssetID, &as); err != nil || as.State != models.AssetFailed ||
			as.StateReason != "The upload was left unfinished. Add the file again to finish it." {
			t.Fatalf("clip %+v %v", as, err)
		}
	}
	for _, x := range []models.IngestSession{finished, recent} {
		if err := a.DB.GetByID(ctx, store.CIngest, a.WorkspaceID(), x.ID, &s); err != nil || s.State != x.State {
			t.Fatalf("left alone: %+v %v", s, err)
		}
	}
	// Younger uploads count once the age is lowered.
	if out := reconcile(ReconcileIn{OlderThanHours: 1, DryRun: true}); !slices.Equal(out.StaleSessions, []string{recent.ID}) {
		t.Fatalf("an hour: %+v", out)
	}
}
