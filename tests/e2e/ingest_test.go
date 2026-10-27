package e2e

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/skillsinstall"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/tools"
)

func setup(t *testing.T) (*install, *tools.Set) {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	db := testutil.DB(t)
	backend := s3mem.New()
	_ = backend.CreateBucket("media")
	s3srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(s3srv.Close)
	wsID := ids.New(ids.Workspace)
	_ = db.Insert(context.Background(), store.CWorkspaces, models.Workspace{ID: wsID, WorkspaceID: wsID, Name: "E2E", CreatedAt: time.Now().UTC()})
	return newInstall(t, db, s3srv.URL, wsID, "machine"), ts
}

// A watched inbox must never ingest a recording that is still being written.
func TestWatchedInboxWaitsForStableFiles(t *testing.T) {
	in, ts := setup(t)
	a := in.app
	full := clip(t, ts, t.TempDir(), "rec.mp4", "testsrc2=size=640x360:rate=30:duration=4")
	data, _ := os.ReadFile(full)
	inbox := t.TempDir()
	w := &ingest.Watcher{App: a, Dir: inbox, StableFor: 3 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()
	// Write the file slowly, like a recorder would.
	dst := filepath.Join(inbox, "recording.mp4")
	f, _ := os.Create(dst)
	chunk := len(data) / 6
	for i := 0; i < 6; i++ {
		end := (i + 1) * chunk
		if i == 5 {
			end = len(data)
		}
		f.Write(data[i*chunk : end])
		f.Sync()
		time.Sleep(900 * time.Millisecond)
		if n, _ := a.DB.C(store.CAssets).CountDocuments(ctx, map[string]any{"workspaceId": a.WorkspaceID()}); n != 0 {
			t.Fatalf("ingested while the file was still growing (after chunk %d)", i)
		}
	}
	f.Close()
	deadline := time.Now().Add(30 * time.Second)
	for {
		n, _ := a.DB.C(store.CAssets).CountDocuments(ctx, map[string]any{"workspaceId": a.WorkspaceID(), "state": models.AssetAvailable})
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stable file was never ingested")
		}
		time.Sleep(300 * time.Millisecond)
	}
	cancel()
	// A restarted watcher does not ingest it again; the original is still in place.
	ctx2, cancel2 := context.WithCancel(context.Background())
	w2 := &ingest.Watcher{App: a, Dir: inbox, StableFor: time.Second}
	go func() { _ = w2.Run(ctx2) }()
	time.Sleep(4 * time.Second)
	cancel2()
	if n, _ := a.DB.C(store.CIngest).CountDocuments(context.Background(), map[string]any{"workspaceId": a.WorkspaceID()}); n != 1 {
		t.Fatalf("expected exactly one ingest session, got %d", n)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatal("watcher removed the original")
	}
}

// An ingest interrupted mid-upload resumes instead of restarting; a missing canonical
// object is reported clearly.
func TestInterruptedIngestResumesAndMissingSourceIsReported(t *testing.T) {
	in, ts := setup(t)
	a := in.app
	big := clip(t, ts, t.TempDir(), "long.mp4", "testsrc2=size=1920x1080:rate=30:duration=4,noise=alls=100:allf=t")
	if st, _ := os.Stat(big); st.Size() < 9<<20 {
		t.Skipf("fixture too small for multipart (%d bytes)", st.Size())
	}
	proj, _ := ops.CallTyped[ops.ProjectOut](context.Background(), a, "project.create", ops.ProjectCreateIn{Name: "resume"}, ops.Caller{})
	ctx, cancel := context.WithCancel(context.Background())
	var uploaded int64
	r1 := ingest.File(ctx, a, big, ingest.Options{ProjectID: proj.Project.ID, Progress: func(_, phase string, done, total int64) {
		if phase == "upload" && done > 0 && done < total {
			uploaded = done
			cancel()
		}
	}})
	if r1.Status != "failed" || uploaded == 0 {
		t.Fatalf("expected an interrupted upload, got %s (%d bytes)", r1.Status, uploaded)
	}
	var sess models.IngestSession
	_ = a.DB.GetByID(context.Background(), store.CIngest, a.WorkspaceID(), r1.SessionID, &sess)
	if sess.State != "uploading" || sess.Upload.UploadID == "" {
		t.Fatalf("session not resumable: %+v", sess)
	}
	var firstUpload int64 = -1
	r2 := ingest.File(context.Background(), a, big, ingest.Options{ProjectID: proj.Project.ID, Progress: func(_, phase string, done, total int64) {
		if phase == "upload" && firstUpload < 0 {
			firstUpload = done
		}
	}})
	if r2.Status != "resumed" || r2.AssetID != r1.AssetID {
		t.Fatalf("expected resume of %s, got %s %s (%s)", r1.AssetID, r2.Status, r2.AssetID, r2.Error)
	}
	if firstUpload <= 0 {
		t.Fatalf("upload restarted from zero")
	}
	var as models.Asset
	_ = a.DB.GetByID(context.Background(), store.CAssets, a.WorkspaceID(), r2.AssetID, &as)
	if as.State != models.AssetAvailable {
		t.Fatalf("asset state %s", as.State)
	}
	// Missing source: remove the canonical object and the local copy.
	_ = a.Media.Delete(context.Background(), as.StorageKey)
	_ = os.Chmod(a.Cache.Path(as.SHA256), 0o644)
	_ = os.Remove(a.Cache.Path(as.SHA256))
	_, err := a.Cache.Ensure(context.Background(), a.Media, as.StorageKey, as.SHA256, as.Size, nil)
	if err == nil || !strings.Contains(err.Error(), "missing from its storage") {
		t.Fatalf("expected a clear missing-source error, got %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("wrong error")
	}
}

// Reinstalling skills never overwrites a file the user edited.
func TestSkillsInstallKeepsUserEdits(t *testing.T) {
	root := t.TempDir()
	o := skillsinstall.Options{Dir: filepath.Join(root, ".claude", "skills"), ProjectRoot: root}
	r1, err := skillsinstall.Install(o)
	if err != nil || len(r1.Installed) == 0 {
		t.Fatalf("install: %v %+v", err, r1)
	}
	p := filepath.Join(o.Dir, "video-render", "SKILL.md")
	_ = os.WriteFile(p, []byte("my own notes"), 0o644)
	r2, err := skillsinstall.Install(o)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "my own notes" || len(r2.Skipped) != 1 {
		t.Fatalf("user edit was not preserved: skipped=%v", r2.Skipped)
	}
	md, _ := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if strings.Count(string(md), "metarush:begin") != 1 {
		t.Fatal("CLAUDE.md block missing or duplicated")
	}
	set, _ := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	if !strings.Contains(string(set), "metarush footage transcript") || strings.Contains(string(set), "metarush edit apply") {
		t.Fatal("permission allowlist must include read-only commands and exclude mutating ones")
	}
	if _, err := skillsinstall.Install(skillsinstall.Options{Dir: o.Dir, ProjectRoot: root, Force: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) == "my own notes" {
		t.Fatal("--force should replace the edited file")
	}
}
