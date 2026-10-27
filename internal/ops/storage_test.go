package ops

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/storagemove"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/gcsemu"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// moveApp is a test app whose workspace record names its folder store (as
// init writes it), with two clips in the store.
func moveApp(t *testing.T) (*app.App, []models.Asset) {
	t.Helper()
	keyring.MockInit() // a ${VAR} secret goes to the keychain on switch: never the real one
	a := testapp.New(t)
	insert(t, a, store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T", CreatedAt: time.Now().UTC(),
		MediaStore: models.StoreIdentity{Provider: a.Media.Name(), Locator: a.Media.Locator()}})
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Move me"})
	v := models.MediaInfo{Duration: 3, HasVideo: true, Width: 1280, Height: 720, FPS: 30}
	return a, []models.Asset{storedAsset(t, a, p.Project.ID, "One.mov", "video", v), storedAsset(t, a, p.Project.ID, "Two.mov", "video", v)}
}

func writeTo(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "new-storage.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func mediaText(t *testing.T, home string) string {
	t.Helper()
	n, err := config.GetField(home, []string{"media"})
	if err != nil || n == nil {
		t.Fatalf("media section: %v", err)
	}
	s, _ := config.NodeYAML(n)
	return s
}

func storedSum(t *testing.T, st media.Store, key string) string {
	t.Helper()
	rc, _, err := st.Get(context.Background(), key, 0, -1)
	if err != nil {
		t.Fatalf("%s: %v", key, err)
	}
	defer rc.Close()
	h := sha256.New()
	_, _ = io.Copy(h, rc)
	return hex.EncodeToString(h.Sum(nil))
}

func migrate(ctx context.Context, a *app.App, to string) (*StorageMigrateOut, error) {
	return CallTyped[StorageMigrateOut](ctx, a, "storage.migrate", StorageMigrateIn{To: to}, Caller{Human: true})
}

// storage.plan and storage.migrate from the folder store to an S3 bucket
// (emulated): only a person runs the move, only on this machine; everything
// arrives checked; this installation's config.yaml and the workspace switch;
// the old store is unchanged; the activity job records it; a later run
// copies what the old store got after the switch.
func TestStorageMigrate(t *testing.T) {
	a, clips := moveApp(t)
	ctx := context.Background()
	home := a.Cfg.Home()
	backend := s3mem.New()
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	if err := backend.CreateBucket("footage"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MR_TEST_NEW_MEDIA_SECRET", "test-secret-value")
	to := writeTo(t, fmt.Sprintf("provider: s3\ns3:\n  endpoint: %s\n  region: us-east-1\n  bucket: footage\n  prefix: team\n  accessKeyId: test\n  secretAccessKey: ${MR_TEST_NEW_MEDIA_SECRET}\n  forcePathStyle: true\n", srv.URL))

	plan := call[StoragePlanOut](t, a, "storage.plan", StoragePlanIn{To: to})
	if plan.Nothing || plan.CatchUp || plan.ToCopy < 2 || plan.Present != 0 || plan.From.Provider != "filesystem" || plan.To.Provider != "s3" ||
		plan.To.Name != "S3 storage bucket footage" || plan.Local != plan.ToCopy || plan.Download != 0 || !strings.HasPrefix(plan.Summary, "Moving to S3 storage bucket footage would copy") {
		t.Fatalf("plan %+v", plan)
	}
	// People only, and only on this machine.
	if _, err := CallTyped[StorageMigrateOut](ctx, a, "storage.migrate", StorageMigrateIn{To: to}, Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("not a person: %v", err)
	}
	if _, err := CallTyped[StorageMigrateOut](ctx, a, "storage.migrate", StorageMigrateIn{To: to}, Caller{Human: true, Remote: true}); apperr.From(err).Code != apperr.Unauthorized {
		t.Fatalf("over HTTP: %v", err)
	}
	// A move whose process stopped earlier is closed when the next one starts.
	w := jobs.Worker{ID: a.Cfg.Installation.ID, Name: a.Cfg.Installation.Name}
	stale, err := a.Queue.Start(ctx, jobs.EnqueueOptions{Type: jobs.TypeStorageMove, Installation: a.Cfg.Installation.ID, MaxAttempts: 2, Payload: map[string]any{"storage": "elsewhere"}}, w)
	if err != nil {
		t.Fatal(err)
	}
	_ = a.DB.Update(ctx, store.CJobs, a.WorkspaceID(), stale.ID, bson.M{"$set": bson.M{"lease.expiresAt": time.Now().UTC().Add(-time.Minute)}})

	out, err := migrate(ctx, a, to)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Verified || !out.Switched || !out.Workspace || out.CatchUp || out.Copied != plan.Files || out.CopiedNow != plan.Files || len(out.Problems) != 0 ||
		out.Downloaded != 0 || out.JobID == "" || !strings.HasPrefix(out.Summary, "Moved ") || len(out.Next) == 0 {
		t.Fatalf("move %+v", out)
	}
	// config.yaml names the bucket; the secret went to the keychain, never the file.
	text := mediaText(t, home)
	if !strings.Contains(text, "provider: s3") || !strings.Contains(text, "keychain:s3-secret-access-key-") || strings.Contains(text, "test-secret-value") {
		t.Fatalf("config.yaml media:\n%s", text)
	}
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	st, err := app.NewMediaStore(cfg)
	if err != nil || st.Locator() != out.To.Locator {
		t.Fatalf("MetaRush would now open %v (%v), want %s", st, err, out.To.Locator)
	}
	for _, c := range clips {
		if storedSum(t, st, c.StorageKey) != c.SHA256 {
			t.Fatalf("%s arrived damaged", c.Name)
		}
		if storedSum(t, a.Media, c.StorageKey) != c.SHA256 {
			t.Fatalf("%s changed in the old store", c.Name)
		}
	}
	var ws models.Workspace
	if err := a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws); err != nil || ws.MediaStore.Locator != out.To.Locator || ws.MediaStore.Provider != "s3" {
		t.Fatalf("workspace %+v %v", ws.MediaStore, err)
	}
	j, err := a.Queue.Get(ctx, out.JobID)
	if err != nil || j.State != models.JobSucceeded || j.Type != jobs.TypeStorageMove || j.Installation != a.Cfg.Installation.ID || j.Result["summary"] != out.Summary {
		t.Fatalf("job %+v %v", j, err)
	}
	if s, _ := a.Queue.Get(ctx, stale.ID); s.State != models.JobFailed || s.Error != storagemove.Interrupted {
		t.Fatalf("the stopped move: %s %q", s.State, s.Error)
	}
	if d, err := JobDetail(ctx, a, out.JobID); err != nil || d.Title != "Moving media to S3 storage bucket footage" || d.Produced == nil || d.Produced.Summary != out.Summary {
		t.Fatalf("job details %+v %v", d, err)
	}

	// MetaRush starts again with the new storage; meanwhile the old folder got one more file.
	a2, err := app.Open(ctx, home, "cli")
	if err != nil {
		t.Fatal(err)
	}
	defer a2.Close()
	if a2.Media.Locator() != out.To.Locator {
		t.Fatalf("reopened on %s", a2.Media.Locator())
	}
	late := storedAsset(t, a, clips[0].ProjectIDs[0], "Late.mov", "video", clips[0].Media)
	p2 := call[StoragePlanOut](t, a2, "storage.plan", StoragePlanIn{To: to})
	if !p2.CatchUp || p2.ToCopy != 1 || p2.From.Provider != "filesystem" {
		t.Fatalf("catch-up plan %+v", p2)
	}
	o2, err := migrate(ctx, a2, to)
	if err != nil || !o2.CatchUp || o2.CopiedNow != 1 || !o2.Verified || !o2.Switched || !strings.Contains(o2.Summary, "since the switch") {
		t.Fatalf("catch-up %+v %v", o2, err)
	}
	if storedSum(t, a2.Media, late.StorageKey) != late.SHA256 {
		t.Fatal("the late file did not arrive")
	}
	if o3, err := migrate(ctx, a2, to); err != nil || o3.CopiedNow != 0 || !strings.HasPrefix(o3.Summary, "Nothing new") {
		t.Fatalf("nothing new %+v %v", o3, err)
	}
}

// To Google Cloud Storage (emulated): the switch names the bucket and
// MetaRush opens it from config.yaml.
func TestStorageMigrateToGCS(t *testing.T) {
	emu := gcsemu.Start(t, "team-media")
	t.Setenv("STORAGE_EMULATOR_HOST", emu.Host)
	a, clips := moveApp(t)
	ctx := context.Background()
	to := writeTo(t, "media:\n  provider: gcs\n  gcs:\n    bucket: team-media\n    prefix: metarush\n")
	out, err := migrate(ctx, a, to)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Verified || !out.Switched || out.To.Name != "Google Cloud Storage bucket team-media" || !strings.HasPrefix(out.To.Locator, "gs://team-media/metarush") {
		t.Fatalf("%+v", out)
	}
	cfg, _ := config.Load(a.Cfg.Home())
	st, err := app.NewMediaStore(cfg)
	if err != nil || st.Locator() != out.To.Locator || storedSum(t, st, clips[1].StorageKey) != clips[1].SHA256 {
		t.Fatalf("opened %v %v", st, err)
	}
}

// When MetaRush could not use the new storage as it would load it, the old
// settings are put back.
func TestStorageSwitchPutsTheOldSettingsBack(t *testing.T) {
	a, _ := moveApp(t)
	ctx := context.Background()
	before := mediaText(t, a.Cfg.Home())
	tg, err := loadStorageTarget(a, writeTo(t, "provider: filesystem\nfilesystem:\n  root: "+filepath.Join(t.TempDir(), "new")+"\n"), false)
	if err != nil {
		t.Fatal(err)
	}
	// A file the new store does not have: the check after switching fails.
	if _, err := switchStorage(ctx, a, tg, media.ObjectKey(a.WorkspaceID(), strings.Repeat("ab", 32))); apperr.From(err).Code != apperr.Storage {
		t.Fatalf("switch: %v", err)
	}
	if after := mediaText(t, a.Cfg.Home()); after != before {
		t.Fatalf("not restored:\n%s\nwas:\n%s", after, before)
	}
}

// A damaged file in the old store stops the switch; settings in the
// environment that would override the new storage stop the move before it
// copies anything.
func TestStorageMigrateRefuses(t *testing.T) {
	a, _ := moveApp(t)
	ctx := context.Background()
	before := mediaText(t, a.Cfg.Home())
	b := make([]byte, 512)
	_, _ = rand.Read(b)
	p := filepath.Join(t.TempDir(), "x")
	_ = os.WriteFile(p, b, 0o600)
	h := sha256.Sum256([]byte("other bytes"))
	if _, err := a.Media.PutFile(ctx, media.ObjectKey(a.WorkspaceID(), hex.EncodeToString(h[:])), p, media.PutOptions{}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	newRoot := filepath.Join(t.TempDir(), "new")
	to := writeTo(t, "provider: filesystem\nfilesystem:\n  root: "+newRoot+"\n")
	_, err := migrate(ctx, a, to)
	ae := apperr.From(err)
	if ae.Code != apperr.Integrity || !strings.Contains(ae.Message, "did not switch") {
		t.Fatalf("damaged: %v", err)
	}
	if mediaText(t, a.Cfg.Home()) != before {
		t.Fatal("config.yaml switched")
	}
	var ws models.Workspace
	_ = a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws)
	if ws.MediaStore.Locator != a.Media.Locator() {
		t.Fatal("the workspace switched")
	}
	js, _ := a.Queue.List(ctx, bson.M{"type": jobs.TypeStorageMove}, 5)
	if len(js) != 1 || js[0].State != models.JobFailed || !strings.Contains(js[0].Error, "did not switch") {
		t.Fatalf("job %+v", js)
	}

	t.Setenv("METARUSH_MEDIA_ROOT", filepath.Join(t.TempDir(), "elsewhere"))
	other := writeTo(t, "provider: filesystem\nfilesystem:\n  root: "+filepath.Join(t.TempDir(), "third")+"\n")
	if _, err := migrate(ctx, a, other); apperr.From(err).Code != apperr.Conflict || !strings.Contains(err.Error(), "METARUSH_MEDIA_ROOT") {
		t.Fatalf("override: %v", err)
	}
	if plan := call[StoragePlanOut](t, a, "storage.plan", StoragePlanIn{To: other}); len(plan.Overrides) != 1 {
		t.Fatalf("plan overrides %+v", plan.Overrides)
	}
	if _, err := migrate(ctx, a, filepath.Join(t.TempDir(), "missing.yaml")); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("missing file: %v", err)
	}
	// The storage this machine already uses: nothing to move, nothing changed.
	t.Setenv("METARUSH_MEDIA_ROOT", "")
	same, err := migrate(ctx, a, writeTo(t, "provider: filesystem\nfilesystem:\n  root: "+a.Cfg.Media.Filesystem.Root+"\n"))
	if err != nil || !same.Nothing || same.Copied != 0 || !same.Workspace || !strings.Contains(same.Summary, "already uses") {
		t.Fatalf("same storage: %+v %v", same, err)
	}
	if mediaText(t, a.Cfg.Home()) != before {
		t.Fatal("config.yaml changed")
	}
}
