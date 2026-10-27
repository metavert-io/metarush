package cli

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

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/machines"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

func runMachine(t *testing.T, home string, args ...string) error {
	t.Helper()
	old := homeFlag
	homeFlag = home
	defer func() { homeFlag = old }()
	cmd := machineCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.PersistentFlags().Bool("json", true, "")
	cmd.SetArgs(args)
	cmd.SetContext(context.Background())
	return cmd.Execute()
}

// invite on an admin installation → join on a fresh home → the record becomes
// active with the minted key → revoke → the bundle can no longer be used.
func TestMachineJoinFromBundle(t *testing.T) {
	db := testutil.DB(t)
	backend := s3mem.New()
	_ = backend.CreateBucket("media")
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	defer srv.Close()
	iam := testutil.FakeTigrisIAM(t)
	t.Setenv(machines.EnvTigrisIAM, iam.URL)
	ctx := context.Background()
	wsID := ids.New(ids.Workspace)
	_ = db.Insert(ctx, store.CWorkspaces, models.Workspace{ID: wsID, WorkspaceID: wsID, Name: "T", CreatedAt: time.Now().UTC()})
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: wsID, Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "admin"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "s3", S3: config.S3{Preset: "tigris", Endpoint: srv.URL, Region: "us-east-1", Bucket: "media", Prefix: "team", AccessKeyID: "tid_admin0000000000", SecretAccessKey: "tsec_admin", ForcePathStyle: true}}
	admin, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	inv, err := machines.Invite(ctx, admin, machines.InviteOptions{Name: "studio", Out: filepath.Join(t.TempDir(), "studio.json")})
	if err != nil {
		t.Fatal(err)
	}

	// The bundle has no database credentials (no --atlas): the joining machine
	// supplies its own URI. Secrets stay in env (no keychain in tests).
	t.Setenv("METARUSH_MONGODB_URI", testutil.URI())
	b, _, _ := machines.ReadBundle(inv.Bundle)
	t.Setenv("METARUSH_S3_SECRET_ACCESS_KEY", b.Media.S3.SecretAccessKey)
	newHome := t.TempDir()
	err = runMachine(t, newHome, "join", inv.Bundle, "--secrets", "env", "--port", "4851", "--test-db", "--user-email", "studio@example.com", "--keep-bundle")
	var ee *exitError
	if err != nil && !errors.As(err, &ee) { // doctor may flag unrelated local tools; the join itself must succeed
		t.Fatalf("join: %v", err)
	}
	joined, err := config.Load(newHome)
	if err != nil {
		t.Fatal(err)
	}
	if joined.Installation.ID != inv.InstallationID || joined.Workspace.ID != wsID || joined.Media.S3.AccessKeyID != b.Media.S3.AccessKeyID {
		t.Fatalf("joined config: installation %s workspace %s", joined.Installation.ID, joined.Workspace.ID)
	}
	raw, _ := os.ReadFile(config.Path(newHome))
	if strings.Contains(string(raw), b.Media.S3.SecretAccessKey) {
		t.Fatal("secret written to config.yaml")
	}
	if _, err := os.Stat(inv.Bundle); err != nil {
		t.Fatal("join --keep-bundle must keep the bundle")
	}
	if !config.HomeIgnoreFileOK(newHome) {
		t.Fatal("the joined home has no .gitignore ignoring everything")
	}
	var inst models.Installation
	_ = db.GetByID(ctx, store.CInstallations, wsID, inv.InstallationID, &inst)
	if inst.State != models.InstallationActive || inst.JoinedAt == nil || inst.StorageKey == nil || inst.StorageKeyID != b.Media.S3.AccessKeyID || inst.Hostname == "" {
		t.Fatalf("installation after join: %+v", inst)
	}

	// Revoked: the key is gone and re-running the bundle is refused.
	if _, err := machines.Revoke(ctx, admin, machines.RevokeOptions{Name: "studio"}); err != nil {
		t.Fatal(err)
	}
	if iam.Key(b.Media.S3.AccessKeyID) != nil {
		t.Fatal("key survived revoke")
	}
	_ = db.GetByID(ctx, store.CInstallations, wsID, inv.InstallationID, &inst)
	if inst.State != models.InstallationRevoked || inst.StorageKey == nil || inst.JoinedAt == nil {
		t.Fatalf("revoke lost the record's fields: %+v", inst)
	}
	if err := runMachine(t, t.TempDir(), "join", inv.Bundle, "--secrets", "env", "--port", "4852", "--test-db"); err == nil {
		t.Fatal("a revoked installation's bundle was accepted")
	}
}

func TestFinishJoinBundle(t *testing.T) {
	mk := func() string {
		p := filepath.Join(t.TempDir(), "metarush-join-x.json")
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
	p := mk()
	if del, err := finishJoinBundle(p, true, false, nil); err != nil || !del || exists(p) {
		t.Fatalf("non-interactive successful join must delete the bundle: %v %v", del, err)
	}
	p = mk()
	if del, _ := finishJoinBundle(p, true, true, nil); del || !exists(p) {
		t.Fatal("--keep-bundle must keep it")
	}
	if del, _ := finishJoinBundle(p, false, false, nil); del || !exists(p) {
		t.Fatal("a join whose checks failed must keep the bundle")
	}
	if del, _ := finishJoinBundle(p, true, false, func() bool { return false }); del || !exists(p) {
		t.Fatal("answering no must keep it")
	}
	if del, _ := finishJoinBundle(p, true, false, func() bool { return true }); !del || exists(p) {
		t.Fatal("answering yes must delete it")
	}
}

// Bundles in a sync folder or where git would commit them are refused before use.
func TestJoinRefusesUnsafeBundleLocation(t *testing.T) {
	fake := t.TempDir()
	t.Setenv("HOME", fake)
	dir := filepath.Join(fake, "Library", "CloudStorage", "GoogleDrive-a@example.com", "My Drive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "metarush-join-x.json")
	if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runMachine(t, t.TempDir(), "join", p, "--secrets", "env", "--test-db")
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("bundle in Google Drive: want exit 2 (E_UNSAFE_SECRET_PATH), got %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatal("a refused bundle must not be deleted")
	}
}

// invite writes bundles to ~/.metarush-bundles by default and refuses unsafe --out paths.
func TestInviteBundleLocation(t *testing.T) {
	fake := t.TempDir()
	t.Setenv("HOME", fake)
	ctx := context.Background()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	wsID := ids.New(ids.Workspace)
	cfg.Workspace = config.Ref{ID: wsID, Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "admin"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media"), Shared: true}}
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	inv, err := machines.Invite(ctx, a, machines.InviteOptions{Name: "mini", AccessKeyID: "manual", ManualSecret: "x"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(fake, ".metarush-bundles", "metarush-join-mini.json")
	if got, _ := filepath.EvalSymlinks(inv.Bundle); got != mustEval(t, want) {
		t.Fatalf("default bundle path %s, want %s", inv.Bundle, want)
	}
	if st, _ := os.Stat(filepath.Dir(want)); st == nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("~/.metarush-bundles must be 0700")
	}
	sync := filepath.Join(fake, "Dropbox", "b.json")
	if _, err := machines.Invite(ctx, a, machines.InviteOptions{Name: "mini2", Out: sync, AllowUnsafeSecretPath: true, AccessKeyID: "manual", ManualSecret: "x"}); err == nil || !strings.Contains(err.Error(), "Dropbox") {
		t.Fatalf("--out in Dropbox must be refused even with the escape hatch: %v", err)
	}
	if _, err := os.Stat(sync); err == nil {
		t.Fatal("refused bundle was written")
	}
}

// machine rekey changes this machine's key: a person in a terminal only
// (without one it stops before opening anything; --dry-run is for anyone).
func TestMachineRekeyNeedsAPerson(t *testing.T) {
	home := t.TempDir()
	err := runMachine(t, home, "rekey")
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != exitCodeFor(apperr.HumanOnly) {
		t.Fatalf("got %v", err)
	}
	if entries, _ := os.ReadDir(home); len(entries) != 0 {
		t.Fatalf("a refused rekey wrote %d files", len(entries))
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, filepath.Base(p))
}
