package machines

import (
	"context"
	"encoding/json"
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
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/tools"
)

type fixture struct {
	app *app.App
	iam *testutil.FakeIAM
	db  *store.DB
	s3  string
}

func newFixture(t *testing.T, preset string) *fixture {
	t.Helper()
	db := testutil.DB(t)
	backend := s3mem.New()
	_ = backend.CreateBucket("media")
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	iam := testutil.FakeTigrisIAM(t)
	t.Setenv(EnvTigrisIAM, iam.URL)
	t.Setenv(EnvAdminKeyID, "")
	t.Setenv(EnvAdminSecret, "")
	ctx := context.Background()
	wsID := ids.New(ids.Workspace)
	if err := db.Insert(ctx, store.CWorkspaces, models.Workspace{ID: wsID, WorkspaceID: wsID, Name: "T", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: wsID, Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "admin"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "Admin"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "s3", S3: config.S3{Preset: preset, Endpoint: srv.URL, Region: "us-east-1", Bucket: "media", Prefix: "team",
		AccessKeyID: "tid_adminkey000000", SecretAccessKey: "tsec_admin", ForcePathStyle: true}}
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	now := time.Now().UTC()
	if err := db.Insert(ctx, store.CInstallations, models.Installation{ID: cfg.Installation.ID, WorkspaceID: wsID, Name: "admin", StorageKeyID: cfg.Media.S3.AccessKeyID, CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	return &fixture{app: a, iam: iam, db: db, s3: srv.URL}
}

func (f *fixture) inst(t *testing.T, id string) models.Installation {
	t.Helper()
	var i models.Installation
	if err := f.db.GetByID(context.Background(), store.CInstallations, f.app.WorkspaceID(), id, &i); err != nil {
		t.Fatal(err)
	}
	return i
}

func TestInviteListRevoke(t *testing.T) {
	f := newFixture(t, "tigris")
	ctx := context.Background()
	dir := t.TempDir()

	// Default scope on a prefixed store: a policy on media/team/*, no bucket role,
	// no default organization operations.
	inv, err := Invite(ctx, f.app, InviteOptions{Name: "laptop", Out: filepath.Join(dir, "laptop.json")})
	if err != nil {
		t.Fatal(err)
	}
	if inv.KeyScope != ScopePrefix || !inv.KeyVerified || inv.KeyLabel != "metarush-laptop" || strings.Contains(inv.KeyID, "secret") {
		t.Fatalf("invite result: %+v", inv)
	}
	st, _ := os.Stat(inv.Bundle)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("bundle mode %v, want 0600", st.Mode().Perm())
	}
	b, warns, err := ReadBundle(inv.Bundle)
	if err != nil || len(warns) != 0 {
		t.Fatalf("read bundle: %v %v", err, warns)
	}
	inst := f.inst(t, inv.InstallationID)
	key := f.iam.Key(inst.StorageKey.KeyID)
	if key == nil || key.Scope != 1 || len(key.Roles) != 0 || len(key.Policies) != 1 {
		t.Fatalf("minted key: %+v", key)
	}
	if b.Media.S3.SecretAccessKey != key.Secret || b.Media.S3.AccessKeyID != key.ID || b.Installation.ID != inv.InstallationID || b.Database.URI != "" {
		t.Fatal("bundle does not carry the new machine's own key")
	}
	if inst.State != models.InstallationInvited || !inst.StorageKey.Managed || inst.StorageKey.PolicyARN == "" || inst.StorageKeyID != key.ID {
		t.Fatalf("installation record: %+v", inst)
	}
	var pol string
	for _, p := range f.iam.Policies {
		pol = p.Document
	}
	if !strings.Contains(pol, `arn:aws:s3:::media/team/*`) || strings.Contains(pol, `"arn:aws:s3:::media/*"`) {
		t.Fatalf("policy is not limited to the prefix: %s", pol)
	}
	// The secret never appears in the op result.
	raw, _ := json.Marshal(inv)
	if strings.Contains(string(raw), key.Secret) {
		t.Fatal("invite output leaks the secret")
	}

	// Same name again: refused before anything is minted.
	before := len(f.iam.Live())
	if _, err := Invite(ctx, f.app, InviteOptions{Name: "laptop", Out: filepath.Join(dir, "again.json")}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("duplicate name: %v", err)
	}
	// Existing bundle path: refused before anything is minted.
	if _, err := Invite(ctx, f.app, InviteOptions{Name: "other", Out: inv.Bundle}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("existing bundle path: %v", err)
	}
	if len(f.iam.Live()) != before {
		t.Fatal("a refused invite minted a key")
	}

	// Bucket scope uses a bucket role (ReadWrite by default).
	rw, err := Invite(ctx, f.app, InviteOptions{Name: "mini", KeyScope: ScopeBucket, Out: filepath.Join(dir, "mini.json")})
	if err != nil {
		t.Fatal(err)
	}
	mk := f.iam.Key(f.inst(t, rw.InstallationID).StorageKey.KeyID)
	if len(mk.Roles) != 1 || mk.Roles[0]["role"] != RoleReadWrite || mk.Roles[0]["bucket"] != "media" || len(mk.Policies) != 0 {
		t.Fatalf("bucket-scoped key: %+v", mk)
	}

	// List: every machine with labels, key status and the current marker.
	l, err := List(ctx, f.app, true)
	if err != nil {
		t.Fatal(err)
	}
	rows := map[string]MachineRow{}
	for _, m := range l.Machines {
		rows[m.Name] = m
	}
	if !rows["admin"].Current || rows["laptop"].State != models.InstallationInvited || rows["laptop"].KeyStatus != "exists" || rows["laptop"].KeyLabel != "metarush-laptop" {
		t.Fatalf("list rows: %+v", rows)
	}
	if !l.KeysChecked || rows["mini"].KeyScope != "bucket (ReadWrite)" {
		t.Fatalf("list: %+v", l)
	}

	// Guards: never cut off the installation running the command.
	if _, err := Revoke(ctx, f.app, RevokeOptions{Name: "admin"}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("revoking self: %v", err)
	}
	// A key shared with another live machine is refused without --force.
	now := time.Now().UTC()
	_ = f.db.Insert(ctx, store.CInstallations, models.Installation{ID: ids.New(ids.Installation), WorkspaceID: f.app.WorkspaceID(), Name: "twin", StorageKeyID: mk.ID, CreatedAt: now})
	if _, err := Revoke(ctx, f.app, RevokeOptions{Name: "mini"}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("shared key: %v", err)
	}

	// Revoke deletes the key and its policy and marks the machine revoked.
	r, err := Revoke(ctx, f.app, RevokeOptions{Name: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	if !r.KeyDeleted || f.iam.Key(key.ID) != nil || f.iam.PolicyCount() != 0 {
		t.Fatalf("revoke: %+v (key still there: %v, policies %d)", r, f.iam.Key(key.ID) != nil, f.iam.PolicyCount())
	}
	got := f.inst(t, inv.InstallationID)
	if got.State != models.InstallationRevoked || got.RevokedAt == nil || got.StorageKey.DeletedAt == nil {
		t.Fatalf("revoked record: %+v", got)
	}
	again, err := Revoke(ctx, f.app, RevokeOptions{Name: "laptop"})
	if err != nil || !again.AlreadyRevoked {
		t.Fatalf("revoke is not idempotent: %+v %v", again, err)
	}
	// The name is free again once revoked.
	if _, err := Invite(ctx, f.app, InviteOptions{Name: "laptop", Out: filepath.Join(dir, "laptop2.json")}); err != nil {
		t.Fatalf("re-invite after revoke: %v", err)
	}
	var audits int64
	audits, _ = f.db.C(store.CAudit).CountDocuments(ctx, map[string]any{"workspaceId": f.app.WorkspaceID(), "action": map[string]any{"$in": []string{"machine.invite", "machine.revoke"}}})
	if audits < 4 {
		t.Fatalf("expected audit events, got %d", audits)
	}
}

func TestInviteRollsBackOnFailure(t *testing.T) {
	f := newFixture(t, "tigris")
	// The bundle cannot be written (parent is a file): the minted key and its
	// policy must be removed and no installation recorded.
	blocker := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(blocker, []byte("x"), 0o600)
	if _, err := Invite(context.Background(), f.app, InviteOptions{Name: "broken", Out: filepath.Join(blocker, "b.json")}); err == nil {
		t.Fatal("expected failure")
	}
	if n := len(f.iam.Live()); n != 0 || f.iam.PolicyCount() != 0 {
		t.Fatalf("rollback left %d key(s) and %d policies", n, f.iam.PolicyCount())
	}
	n, _ := f.db.C(store.CInstallations).CountDocuments(context.Background(), map[string]any{"name": "broken"})
	if n != 0 {
		t.Fatal("rollback left an installation record")
	}
}

func TestManualProvider(t *testing.T) {
	f := newFixture(t, "r2")
	ctx := context.Background()
	_, err := Invite(ctx, f.app, InviteOptions{Name: "laptop", Out: filepath.Join(t.TempDir(), "l.json")})
	if !apperr.Is(err, apperr.NotConfigured) {
		t.Fatalf("expected manual steps, got %v", err)
	}
	ae := apperr.From(err)
	steps, _ := ae.Details.(map[string]any)["steps"].([]string)
	if len(steps) < 3 || !strings.Contains(strings.Join(steps, "\n"), "R2 API Tokens") {
		t.Fatalf("manual steps: %v", steps)
	}
	// A key created by hand is carried in the bundle; its secret comes from the env.
	inv, err := Invite(ctx, f.app, InviteOptions{Name: "laptop", Out: filepath.Join(t.TempDir(), "l.json"), AccessKeyID: "manualkey", ManualSecret: "manualsecret"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.KeyProvider != "manual" || len(inv.ManualSteps) == 0 {
		t.Fatalf("manual invite: %+v", inv)
	}
	r, err := Revoke(ctx, f.app, RevokeOptions{Name: "laptop"})
	if err != nil || r.KeyDeleted || !strings.Contains(strings.Join(r.ManualSteps, " "), "console") {
		t.Fatalf("manual revoke: %+v %v", r, err)
	}
	if len(f.iam.Calls) != 0 {
		t.Fatalf("manual provider called the Tigris API: %v", f.iam.Calls)
	}
}

func TestBundleValidation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "b.json")
	b := &Bundle{Kind: BundleKind, Version: 1, Workspace: config.Ref{ID: "ws_x"}, Installation: config.Ref{ID: "ins_x", Name: "x"}, Database: BundleDB{Name: "db"},
		Media: BundleMedia{Provider: "s3", S3: &BundleS3{Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"}}}
	if err := WriteBundle(p, b); err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(p, b); err == nil {
		t.Fatal("bundles must never be overwritten")
	}
	_ = os.Chmod(p, 0o644)
	if _, warns, err := ReadBundle(p); err != nil || len(warns) != 1 {
		t.Fatalf("world-readable bundle should warn: %v %v", warns, err)
	}
	bad := filepath.Join(dir, "bad.json")
	_ = os.WriteFile(bad, []byte(`{"kind":"something-else","version":1}`), 0o600)
	if _, _, err := ReadBundle(bad); err == nil {
		t.Fatal("foreign JSON accepted as a bundle")
	}
	if err := ValidName("Bad_Name"); err == nil {
		t.Fatal("invalid name accepted")
	}
}

func TestTigrisClient(t *testing.T) {
	iam := testutil.FakeTigrisIAM(t)
	c := &Tigris{Endpoint: iam.URL, AccessKeyID: "tid_admin", SecretAccessKey: "tsec_admin"}
	ctx := context.Background()
	k, err := c.CreateKey(ctx, "metarush-x", []BucketRole{{Bucket: "media", Role: RoleReadWrite}}, ScopeNoDefaultAllow)
	if err != nil || k.Secret == "" {
		t.Fatal(err)
	}
	got, err := c.GetKey(ctx, k.ID)
	if err != nil || got.Name != "metarush-x" || got.Scope != ScopeNoDefaultAllow || len(got.Roles) != 1 || got.CreatedAt.IsZero() {
		t.Fatalf("get key: %+v %v", got, err)
	}
	if ks, _ := c.ListKeys(ctx); len(ks) != 1 {
		t.Fatalf("list: %v", ks)
	}
	if err := c.DeleteKey(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetKey(ctx, k.ID); err != ErrKeyNotFound {
		t.Fatalf("deleted key still found: %v", err)
	}
	if MaskKeyID("tid_abcdefghijklmnop") != "tid_abcd…mnop" {
		t.Fatal(MaskKeyID("tid_abcdefghijklmnop"))
	}
}

// A fake Atlas CLI proves the database password reaches the CLI on stdin only.
func TestAtlasPasswordViaStdin(t *testing.T) {
	dir := t.TempDir()
	log, in := filepath.Join(dir, "args.log"), filepath.Join(dir, "stdin.json")
	script := `#!/bin/sh
echo "$@" >> "` + log + `"
case "$1 $2" in
  "auth whoami") echo "Logged in"; exit 0;;
  "clusters list") echo '{"results":[{"name":"Cluster0","connectionStrings":{"standardSrv":"mongodb+srv://cluster0.abcde.mongodb.net"}}]}'; exit 0;;
  "dbusers describe") echo "Error: GET ... 404 (request \"USERNAME_NOT_FOUND\")" >&2; exit 1;;
  "api databaseUsers") cat > "` + in + `"; echo '{}'; exit 0;;
  "dbusers delete") exit 0;;
esac
exit 2
`
	if err := os.WriteFile(filepath.Join(dir, "atlas"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	ts := tools.New(config.Tools{})
	if ts.Path(tools.Atlas) != filepath.Join(dir, "atlas") {
		t.Skipf("fake atlas not selected (%s)", ts.Path(tools.Atlas))
	}
	at := &Atlas{Tools: ts, Profile: "p", ProjectID: "proj"}
	ctx := context.Background()
	if err := at.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	c, err := at.ClusterForHost(ctx, "cluster0.abcde.mongodb.net")
	if err != nil || c != "Cluster0" {
		t.Fatalf("cluster: %q %v", c, err)
	}
	if ok, err := at.UserExists(ctx, "metarush-x"); ok || err != nil {
		t.Fatalf("exists: %v %v", ok, err)
	}
	pw, err := at.CreateUser(ctx, "metarush-x", "mydb", c, "test")
	if err != nil || len(pw) != 32 {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(log)
	if strings.Contains(string(args), pw) {
		t.Fatal("password appeared in argv")
	}
	var body map[string]any
	raw, _ := os.ReadFile(in)
	if err := json.Unmarshal(raw, &body); err != nil || body["password"] != pw || body["username"] != "metarush-x" {
		t.Fatalf("stdin body: %s %v", raw, err)
	}
	roles, _ := json.Marshal(body["roles"])
	scopes, _ := json.Marshal(body["scopes"])
	if string(roles) != `[{"databaseName":"mydb","roleName":"readWrite"}]` || !strings.Contains(string(scopes), "Cluster0") {
		t.Fatalf("least privilege: roles %s scopes %s", roles, scopes)
	}
	if !strings.Contains(string(args), "-P p") {
		t.Fatal("profile not passed")
	}
	if err := at.DeleteUser(ctx, "metarush-x"); err != nil {
		t.Fatal(err)
	}
	u, _ := WithUser("mongodb+srv://old:pw@cluster0.abcde.mongodb.net/?retryWrites=true", "metarush-x", pw)
	if !strings.HasPrefix(u, "mongodb+srv://metarush-x:"+pw+"@cluster0.abcde.mongodb.net/") {
		t.Fatalf("uri: %s", config.RedactURI(u))
	}
}
