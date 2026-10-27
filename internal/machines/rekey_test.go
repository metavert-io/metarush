package machines

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

const envSecret = config.EnvPrefix + "S3_SECRET_ACCESS_KEY"

// rekeyFixture is newFixture's admin machine with its settings on disk, the
// way init writes them: the storage secret in the (mock) keychain or in
// <home>/.env. Another machine, laptop, shares its key.
func rekeyFixture(t *testing.T, preset, secrets string) *fixture {
	t.Helper()
	keyring.MockInit() // never the real keychain
	t.Setenv(config.EnvPrefix+"S3_ACCESS_KEY_ID", "")
	_ = os.Unsetenv(config.EnvPrefix + "S3_ACCESS_KEY_ID")
	t.Setenv(envSecret, "")
	_ = os.Unsetenv(envSecret)
	f := newFixture(t, preset)
	saveSettings(t, f.app.Cfg, secrets, "tsec_admin")
	now := time.Now().UTC()
	if err := f.db.Insert(context.Background(), store.CInstallations, models.Installation{ID: ids.New(ids.Installation), WorkspaceID: f.app.WorkspaceID(), Name: "laptop",
		StorageKeyID: f.app.Cfg.Media.S3.AccessKeyID, CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	return f
}

// saveSettings writes cfg to its home with the storage secret kept as secrets says.
func saveSettings(t *testing.T, cfg *config.Config, secrets, secret string) {
	t.Helper()
	h := cfg.Home()
	c := *cfg
	c.Secrets = secrets
	switch secrets {
	case "keychain":
		ref, err := config.StoreSecret(h, "s3-secret-access-key", secret)
		if err != nil {
			t.Fatal(err)
		}
		c.Media.S3.SecretAccessKey = ref
	case "env":
		if err := config.SetDotEnvVar(h, envSecret, secret, false); err != nil {
			t.Fatal(err)
		}
		c.Media.S3.SecretAccessKey = "${" + envSecret + "}"
	}
	if err := config.Save(h, &c); err != nil {
		t.Fatal(err)
	}
	if got, err := config.Load(h); err != nil || got.Media.S3.SecretAccessKey != secret {
		t.Fatalf("settings do not load: %v", err)
	}
}

// reopen opens the machine's MetaRush again from its settings on disk (as
// the next command would).
func reopen(t *testing.T, home string) *app.App {
	t.Helper()
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.OpenWith(context.Background(), cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

func audits(t *testing.T, f *fixture, action string) int64 {
	t.Helper()
	n, _ := f.db.C(store.CAudit).CountDocuments(context.Background(), map[string]any{"workspaceId": f.app.WorkspaceID(), "action": action})
	return n
}

// A machine on the shared key gets its own, in place: the dry run changes
// nothing and calls nothing; the rekey mints a key limited to the workspace's
// footage, keeps its secret in the keychain, switches config.yaml, records
// and audits it, removes this computer's copy of the old secret and never
// deletes the old key; the result says who still shares it, and the last
// machine off the old key is told it can be deleted.
func TestRekeyKeychain(t *testing.T) {
	f := rekeyFixture(t, "tigris", "keychain")
	ctx := context.Background()
	h := f.app.Cfg.Home()
	before, _ := os.ReadFile(config.Path(h))

	plan, err := Rekey(ctx, f.app, RekeyOptions{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.DryRun || plan.AlreadyOwn || len(plan.Steps) != 2 || !slices.Equal(plan.SharedWith, []string{"laptop"}) || plan.SecretsIn != "keychain" ||
		!strings.Contains(plan.Steps[0], "media/team/") || plan.KeyID != "" {
		t.Fatalf("plan %+v", plan)
	}
	after, _ := os.ReadFile(config.Path(h))
	if len(f.iam.Calls) != 0 || !bytes.Equal(before, after) {
		t.Fatalf("a dry run changed something (IAM calls %v)", f.iam.Calls)
	}

	res, err := Rekey(ctx, f.app, RekeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.DryRun || !res.KeyVerified || res.KeyScope != ScopePrefix || res.KeyLabel != "metarush-admin" || !res.OldSecretRemoved || !slices.Equal(res.SharedWith, []string{"laptop"}) {
		t.Fatalf("rekey %+v", res)
	}
	inst := f.inst(t, f.app.Cfg.Installation.ID)
	key := f.iam.Key(inst.StorageKeyID)
	if key == nil || key.Scope != 1 || len(key.Policies) != 1 || len(key.Roles) != 0 || inst.StorageKey == nil || !inst.StorageKey.Managed || inst.StorageKey.KeyID != key.ID {
		t.Fatalf("the new key %+v, record %+v", key, inst)
	}
	if res.KeyID != MaskKeyID(key.ID) || res.OldKeyID != MaskKeyID("tid_adminkey000000") {
		t.Fatalf("masked ids: %s %s", res.KeyID, res.OldKeyID)
	}
	// config.yaml names the new key and a new keychain entry; the old entry is gone.
	cfg, err := config.Load(h)
	if err != nil || cfg.Media.S3.AccessKeyID != key.ID || cfg.Media.S3.SecretAccessKey != key.Secret {
		t.Fatalf("settings after the rekey: %v", err)
	}
	ref, _ := config.GetField(h, []string{"media", "s3", "secretAccessKey"})
	if ref == nil || ref.Value != "keychain:s3-secret-access-key-"+keySuffix(key.ID) {
		t.Fatalf("secret reference %+v", ref)
	}
	if _, err := config.ResolveSecret(h, "keychain:s3-secret-access-key"); err == nil {
		t.Fatal("this computer's copy of the old secret is still in the keychain")
	}
	// The old key is never deleted; nothing secret is in the result.
	for _, c := range f.iam.Calls {
		if c == "DeleteAccessKey" || c == "ForceDeletePolicy" {
			t.Fatalf("the rekey deleted something: %v", f.iam.Calls)
		}
	}
	raw, _ := json.Marshal(res)
	if strings.Contains(string(raw), key.Secret) || strings.Contains(string(raw), "tsec_admin") {
		t.Fatal("the result shows a secret")
	}
	next := strings.Join(res.NextSteps, " ")
	if !strings.Contains(next, "laptop still uses the old key") || !strings.Contains(next, "never deletes it") || !strings.Contains(next, EnvAdminKeyID) {
		t.Fatalf("next steps: %v", res.NextSteps)
	}
	if audits(t, f, "machine.rekey") != 1 {
		t.Fatal("no audit event")
	}

	// The next command on this machine: nothing to do.
	again, err := Rekey(ctx, reopen(t, h), RekeyOptions{})
	if err != nil || !again.AlreadyOwn || len(f.iam.Live()) != 1 {
		t.Fatalf("second rekey: %+v %v (keys %d)", again, err, len(f.iam.Live()))
	}

	// laptop rekeys on its own computer: nobody else uses the old key now.
	lh := t.TempDir()
	lc := *f.app.Cfg
	lc.SetHome(lh)
	var laptops []models.Installation
	if err := f.db.Find(ctx, store.CInstallations, f.app.WorkspaceID(), bson.M{"name": "laptop"}, &laptops); err != nil || len(laptops) != 1 {
		t.Fatalf("laptop: %v", err)
	}
	lc.Installation = config.Ref{ID: laptops[0].ID, Name: "laptop"}
	lc.Media.S3.SecretAccessKey = "tsec_admin"
	saveSettings(t, &lc, "keychain", "tsec_admin")
	lr, err := Rekey(ctx, reopen(t, lh), RekeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(lr.SharedWith) != 0 || !strings.Contains(strings.Join(lr.NextSteps, " "), "No other machine uses the old key") {
		t.Fatalf("laptop: %+v", lr)
	}
	l, _ := List(ctx, reopen(t, h), false)
	for _, m := range l.Machines {
		if len(m.Warnings) > 0 {
			t.Fatalf("after both rekeys: %+v", m)
		}
	}
}

// Secrets kept in <home>/.env: the new secret replaces the old one there, and
// a restart that forgets what it read from .env (the daemon's) gets it.
func TestRekeyEnv(t *testing.T) {
	f := rekeyFixture(t, "tigris", "env")
	ctx := context.Background()
	h := f.app.Cfg.Home()
	res, err := Rekey(ctx, f.app, RekeyOptions{})
	if err != nil {
		t.Fatal(err)
	}
	key := f.iam.Key(f.inst(t, f.app.Cfg.Installation.ID).StorageKeyID)
	if res.SecretsIn != "env" || res.OldSecretRemoved || key == nil {
		t.Fatalf("rekey %+v", res)
	}
	b, _ := os.ReadFile(config.DotEnvPath(h))
	if !strings.Contains(string(b), key.Secret) || strings.Contains(string(b), "tsec_admin") {
		t.Fatal(".env does not hold the new secret alone")
	}
	config.ForgetDotEnv() // the daemon's restart
	if _, ok := os.LookupEnv(envSecret); ok {
		t.Fatal("ForgetDotEnv kept a value read from .env")
	}
	cfg, err := config.Load(h)
	if err != nil || cfg.Media.S3.AccessKeyID != key.ID || cfg.Media.S3.SecretAccessKey != key.Secret {
		t.Fatalf("after a restart: %v", err)
	}
	if mode, err := os.Stat(config.DotEnvPath(h)); err != nil || mode.Mode().Perm() != 0o600 {
		t.Fatalf(".env mode: %v %v", mode, err)
	}
}

// A key set in the environment would win over config.yaml: refused before
// anything is made. When the last check fails, everything is put back:
// config.yaml byte for byte, the keychain, the record, and the new key is
// deleted. A machine that keeps its footage on a drive has no key to replace.
func TestRekeyRefusesAndPutsEverythingBack(t *testing.T) {
	f := rekeyFixture(t, "tigris", "keychain")
	ctx := context.Background()
	h := f.app.Cfg.Home()
	before, _ := os.ReadFile(config.Path(h))

	t.Setenv(config.EnvPrefix+"S3_ACCESS_KEY_ID", "tid_fromtheshell")
	if _, err := Rekey(ctx, f.app, RekeyOptions{}); !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "S3_ACCESS_KEY_ID") {
		t.Fatalf("a key from the environment: %v", err)
	}
	if len(f.iam.Calls) != 0 {
		t.Fatalf("calls %v", f.iam.Calls)
	}
	_ = os.Unsetenv(config.EnvPrefix + "S3_ACCESS_KEY_ID")

	old := checkRekey
	checkRekey = func(context.Context, string, string, string, string) error { return errors.New("the storage said no") }
	t.Cleanup(func() { checkRekey = old })
	if _, err := Rekey(ctx, f.app, RekeyOptions{}); err == nil {
		t.Fatal("expected the rekey to fail")
	}
	after, _ := os.ReadFile(config.Path(h))
	if !bytes.Equal(before, after) {
		t.Fatalf("config.yaml changed:\n%s", after)
	}
	if s, err := config.ResolveSecret(h, "keychain:s3-secret-access-key"); err != nil || s != "tsec_admin" {
		t.Fatalf("the old secret: %q %v", s, err)
	}
	if n := len(f.iam.Live()); n != 0 || f.iam.PolicyCount() != 0 {
		t.Fatalf("the new key was left behind (%d keys, %d policies)", n, f.iam.PolicyCount())
	}
	if inst := f.inst(t, f.app.Cfg.Installation.ID); inst.StorageKey != nil || inst.StorageKeyID != "tid_adminkey000000" {
		t.Fatalf("record changed: %+v", inst)
	}
	if audits(t, f, "machine.rekey") != 0 {
		t.Fatal("a failed rekey was audited as done")
	}

	f.app.Cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(t.TempDir(), "media"), Shared: true}}
	if _, err := Rekey(ctx, f.app, RekeyOptions{DryRun: true}); !apperr.Is(err, apperr.NotConfigured) {
		t.Fatalf("footage on a drive: %v", err)
	}
}

// Providers without a key API: the dry run lists the steps, the rekey takes a
// key made by hand (its secret from the environment) and never calls Tigris.
func TestRekeyManual(t *testing.T) {
	f := rekeyFixture(t, "r2", "keychain")
	ctx := context.Background()
	plan, err := Rekey(ctx, f.app, RekeyOptions{DryRun: true})
	if err != nil || len(plan.ManualSteps) < 3 || !strings.Contains(strings.Join(plan.ManualSteps, " "), "R2 API Tokens") {
		t.Fatalf("plan %+v %v", plan, err)
	}
	if _, err := Rekey(ctx, f.app, RekeyOptions{}); !apperr.Is(err, apperr.NotConfigured) {
		t.Fatalf("without a key: %v", err)
	}
	if _, err := Rekey(ctx, f.app, RekeyOptions{AccessKeyID: "handmade"}); !apperr.Is(err, apperr.InvalidInput) {
		t.Fatalf("without its secret: %v", err)
	}
	res, err := Rekey(ctx, f.app, RekeyOptions{AccessKeyID: "handmadekey1", ManualSecret: "handmadesecret1"})
	if err != nil {
		t.Fatal(err)
	}
	inst := f.inst(t, f.app.Cfg.Installation.ID)
	if res.KeyProvider != "manual" || inst.StorageKey == nil || inst.StorageKey.Managed || inst.StorageKey.Provider != "manual" || inst.StorageKeyID != "handmadekey1" {
		t.Fatalf("manual rekey %+v / %+v", res, inst)
	}
	if cfg, err := config.Load(f.app.Cfg.Home()); err != nil || cfg.Media.S3.AccessKeyID != "handmadekey1" || cfg.Media.S3.SecretAccessKey != "handmadesecret1" {
		t.Fatalf("settings: %v", err)
	}
	if len(f.iam.Calls) != 0 {
		t.Fatalf("a manual rekey called Tigris: %v", f.iam.Calls)
	}
	if !strings.Contains(strings.Join(res.NextSteps, " "), "your storage provider's console") {
		t.Fatalf("next %v", res.NextSteps)
	}
}
