package ops

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
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A move started from the dashboard (D112): the storage described in the form
// becomes a description with the typed secret only as a keychain reference;
// the plan says what it would copy; the person's confirmation is a one-time
// grant that only the process it was made for can use, once and soon; the
// move then runs as storage.migrate does and its outcome is kept for the
// Settings page.
func TestDashboardMove(t *testing.T) {
	a, clips := moveApp(t) // keychain mode, with the test keychain
	ctx := context.Background()
	backend := s3mem.New()
	srv := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(srv.Close)
	if err := backend.CreateBucket("footage"); err != nil {
		t.Fatal(err)
	}
	const secret = "dash-secret-value-1234"
	target := StorageTarget{Provider: "s3", Preset: "custom", Endpoint: srv.URL, Region: "us-east-1", Bucket: "footage", Prefix: "team",
		AccessKeyID: "test", SecretAccessKey: secret, ForcePathStyle: true}

	// Planning: the description keeps a reference, never the secret.
	mv, plan, err := PlanDashboardMove(ctx, a, target)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Nothing || plan.ToCopy != len(clips) || plan.To.Name != "S3 storage bucket footage" || mv.State != MovePlanned || mv.To.Locator != plan.To.Locator {
		t.Fatalf("plan %+v move %+v", plan, mv)
	}
	desc, _ := moveFile(a, mv.ID, ".yaml")
	b, err := os.ReadFile(desc)
	st, _ := os.Stat(desc)
	if err != nil || strings.Contains(string(b), secret) || !strings.Contains(string(b), "keychain:s3-secret-access-key-") || st.Mode().Perm() != 0o600 {
		t.Fatalf("description (%v, %v):\n%s", st.Mode(), err, b)
	}
	rec, _ := os.ReadFile(filepath.Join(dashboardDir(a), mv.ID+".json"))
	if strings.Contains(string(rec), secret) {
		t.Fatal("the secret is in the move's record")
	}
	// Planned moves are not shown; nothing runs.
	if s, err := StorageStatus(ctx, a, &StorageStatusIn{}); err != nil || s.Move != nil || s.Moving || s.SwitchMeToo || !strings.HasPrefix(s.Summary, "This computer keeps the media in the folder") {
		t.Fatalf("status before %+v %v", s, err)
	}

	// The grant: only for a move this computer planned, once.
	if _, _, err := GrantDashboardMove(a, strings.Repeat("0", 24), false); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("unknown move: %v", err)
	}
	if _, _, err := GrantDashboardMove(a, "../../config", false); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("not an ID: %v", err)
	}
	token, granted, err := GrantDashboardMove(a, mv.ID, false)
	if err != nil || len(token) != 64 || granted.State != MoveStarting {
		t.Fatalf("grant %q %+v %v", token, granted, err)
	}
	if rec, _ := os.ReadFile(filepath.Join(dashboardDir(a), mv.ID+".json")); strings.Contains(string(rec), token) {
		t.Fatal("the token itself is kept")
	}
	if _, _, err := GrantDashboardMove(a, mv.ID, false); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("granted twice: %v", err)
	}
	if s, _ := StorageStatus(ctx, a, &StorageStatusIn{}); !s.Moving || s.Move == nil || s.Move.State != MoveStarting || !MoveRunningHere(ctx, a) {
		t.Fatalf("status while starting %+v", s)
	}
	// A wrong token uses the grant up: the right one is refused after it.
	if _, _, err := ClaimDashboardMove(a, mv.ID, "not-the-token"); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("wrong token: %v", err)
	}
	if _, _, err := ClaimDashboardMove(a, mv.ID, token); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("a used grant: %v", err)
	}
	ageGrant(t, a, mv.ID, 5*time.Minute) // long gone, for what follows

	// A grant older than two minutes is refused.
	mv2, _, err := PlanDashboardMove(ctx, a, target)
	if err != nil {
		t.Fatal(err)
	}
	token2, _, err := GrantDashboardMove(a, mv2.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	old := ageGrant(t, a, mv2.ID, 3*time.Minute)
	if _, _, err := ClaimDashboardMove(a, mv2.ID, token2); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("an old grant: %v", err)
	}
	if s, _ := StorageStatus(ctx, a, &StorageStatusIn{}); s.Moving || s.Move == nil || s.Move.State != MoveStopped || len(s.Move.Next) == 0 || old.State != MoveStarting {
		t.Fatalf("a grant nobody took %+v", s.Move)
	}

	// The real thing: claimed by its process, moved, recorded.
	mv3, _, err := PlanDashboardMove(ctx, a, target)
	if err != nil {
		t.Fatal(err)
	}
	token3, _, err := GrantDashboardMove(a, mv3.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	claimed, path, err := ClaimDashboardMove(a, mv3.ID, token3+"\n")
	if err != nil || claimed.State != MoveRunning || claimed.PID != os.Getpid() || !claimed.ReadBack || path != filepath.Join(dashboardDir(a), mv3.ID+".yaml") {
		t.Fatalf("claim %+v %q %v", claimed, path, err)
	}
	if s, _ := StorageStatus(ctx, a, &StorageStatusIn{}); !s.Moving || s.Move.State != MoveRunning || s.SwitchMeToo {
		t.Fatalf("status while moving %+v", s)
	}
	out, err := CallTyped[StorageMigrateOut](ctx, a, "storage.migrate", StorageMigrateIn{To: path, ReadBack: claimed.ReadBack}, Caller{Human: true})
	FinishDashboardMove(a, mv3.ID, out, err)
	if err != nil || !out.Verified || !out.Switched || !strings.Contains(out.Summary, "read back") {
		t.Fatalf("move %+v %v", out, err)
	}
	text := mediaText(t, a.Cfg.Home())
	if !strings.Contains(text, "keychain:s3-secret-access-key-") || strings.Contains(text, secret) {
		t.Fatalf("config.yaml media:\n%s", text)
	}
	s, err := StorageStatus(ctx, a, &StorageStatusIn{})
	if err != nil || s.Moving || s.Move == nil || s.Move.State != MoveDone || !strings.HasPrefix(s.Move.Summary, "Moved ") || s.Move.Finished == nil {
		t.Fatalf("status after %+v %v", s, err)
	}
	// The old folder still has every clip: nothing is ever deleted.
	for _, c := range clips {
		if storedSum(t, a.Media, c.StorageKey) != c.SHA256 {
			t.Fatalf("%s changed in the old store", c.Name)
		}
	}
}

// ageGrant makes a move's grant older, as if its process never came.
func ageGrant(t *testing.T, a *app.App, id string, by time.Duration) DashboardMove {
	t.Helper()
	mv, err := loadDashboardMove(a, id)
	if err != nil || mv.Granted == nil {
		t.Fatalf("%+v %v", mv, err)
	}
	g := mv.Granted.Add(-by)
	mv.Granted = &g
	if err := saveDashboardMove(a, mv); err != nil {
		t.Fatal(err)
	}
	return *mv
}

// What a form may say: the typed text is taken as written (no ${…}, no
// keychain names), folders are full places, a secret typed on an installation
// that keeps its keys in the environment is refused (the variable's name is
// given instead).
func TestDashboardMoveRefuses(t *testing.T) {
	a, _ := moveApp(t)
	ctx := context.Background()
	for name, tg := range map[string]StorageTarget{
		"a variable in a name":   {Provider: "s3", Preset: "custom", Endpoint: "https://s3.example.test", Bucket: "${METARUSH_MONGODB_URI}", AccessKeyID: "k", SecretAccessKey: "s"},
		"a keychain entry":       {Provider: "s3", Preset: "custom", Endpoint: "https://s3.example.test", Bucket: "b", AccessKeyID: "keychain:mongodb-uri", SecretAccessKey: "s"},
		"a folder without place": {Provider: "filesystem", Root: "media"},
		"no provider":            {Root: "/tmp/x"},
		"an address that isn't":  {Provider: "s3", Preset: "custom", Endpoint: "ftp://s3.example.test", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"},
		"credentials in address": {Provider: "s3", Preset: "custom", Endpoint: "https://u:p@s3.example.test", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"},
		"no secret":              {Provider: "s3", Preset: "r2", Endpoint: "https://x.r2.cloudflarestorage.com", Bucket: "b", AccessKeyID: "k"},
		"a key file that isn't":  {Provider: "gcs", Bucket: "b", CredentialsFile: "/etc/hosts"},
	} {
		if _, _, err := PlanDashboardMove(ctx, a, tg); apperr.From(err).Code != apperr.InvalidInput {
			t.Errorf("%s: %v", name, err)
		}
	}
	entries, _ := os.ReadDir(dashboardDir(a))
	if len(entries) != 0 {
		t.Fatalf("refused forms left %d files", len(entries))
	}
	// Environment-mode installations name the variable that holds the secret.
	a.Cfg.Secrets = "env"
	tg := StorageTarget{Provider: "s3", Preset: "custom", Endpoint: "https://s3.example.test", Bucket: "b", AccessKeyID: "k", SecretAccessKey: "s"}
	if _, _, err := PlanDashboardMove(ctx, a, tg); apperr.From(err).Code != apperr.InvalidInput || !strings.Contains(apperr.From(err).Message, "environment file") {
		t.Fatalf("env mode, typed secret: %v", err)
	}
	tg.SecretAccessKey, tg.SecretVariable = "", "MR_TEST_NOT_SET_ANYWHERE"
	if _, _, err := PlanDashboardMove(ctx, a, tg); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("env mode, missing variable: %v", err)
	}
	t.Setenv("MR_TEST_DASH_SECRET", "x")
	id := newMoveID()
	p, err := describeTarget(a, id, StorageTarget{Provider: "s3", Preset: "custom", Endpoint: "https://s3.example.test", Bucket: "b", AccessKeyID: "k", SecretVariable: "MR_TEST_DASH_SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "secretAccessKey: ${MR_TEST_DASH_SECRET}") {
		t.Fatalf("description:\n%s", b)
	}
}

// After another machine moved the media, this one still uses the old
// storage: the status says so and offers the new storage as a form to fill
// with this machine's own keys; moving there switches this machine too.
func TestDashboardMoveSwitchMeToo(t *testing.T) {
	a, clips := moveApp(t)
	ctx := context.Background()
	newRoot := filepath.Join(t.TempDir(), "Studio media")
	// Another machine moved everything there and the workspace names it.
	for _, c := range clips {
		dst := filepath.Join(newRoot, filepath.FromSlash(c.StorageKey))
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		rc, _, err := a.Media.Get(ctx, c.StorageKey, 0, -1)
		if err != nil {
			t.Fatal(err)
		}
		f, _ := os.Create(dst)
		_, _ = f.ReadFrom(rc)
		_ = f.Close()
		_ = rc.Close()
	}
	if err := a.DB.Update(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), bson.M{"$set": bson.M{"mediaStore": models.StoreIdentity{Provider: "filesystem", Locator: "fs:" + newRoot, Shared: true}}}); err != nil {
		t.Fatal(err)
	}
	s, err := StorageStatus(ctx, a, &StorageStatusIn{})
	if err != nil || !s.SwitchMeToo || s.Workspace == nil || s.Workspace.Name != "the shared folder Studio media" || s.Target == nil ||
		s.Target.Provider != "filesystem" || s.Target.Root != newRoot || !s.Target.Shared || !strings.HasPrefix(s.Summary, "This computer still uses") {
		t.Fatalf("status %+v %v", s, err)
	}
	if b, _ := json.Marshal(s); strings.Contains(string(b), "secret") && strings.Contains(string(b), "keychain:") {
		t.Fatalf("a key in the status: %s", b)
	}
	mv, plan, err := PlanDashboardMove(ctx, a, *s.Target)
	if err != nil || plan.ToCopy != 0 || plan.Present != len(clips) {
		t.Fatalf("plan %+v %v", plan, err)
	}
	token, _, _ := GrantDashboardMove(a, mv.ID, false)
	_, path, err := ClaimDashboardMove(a, mv.ID, token)
	if err != nil {
		t.Fatal(err)
	}
	out, err := CallTyped[StorageMigrateOut](ctx, a, "storage.migrate", StorageMigrateIn{To: path}, Caller{Human: true})
	FinishDashboardMove(a, mv.ID, out, err)
	if err != nil || !out.Switched || out.CopiedNow != 0 {
		t.Fatalf("switch %+v %v", out, err)
	}
	if !strings.Contains(mediaText(t, a.Cfg.Home()), newRoot) {
		t.Fatal("config.yaml still names the old folder")
	}
}

// The workspace's storage read back as a form: provider, bucket, folder and
// address, never a key.
func TestTargetFromStore(t *testing.T) {
	for loc, want := range map[string]struct{ provider, preset, endpoint, bucket, prefix, name string }{
		"s3://media/team@fly.storage.tigris.dev":              {"s3", "tigris", "https://fly.storage.tigris.dev", "media", "team", "Tigris bucket media"},
		"s3://media":                                          {"s3", "aws", "", "media", "", "Amazon S3 bucket media"},
		"s3://media@abc.r2.cloudflarestorage.com":             {"s3", "r2", "https://abc.r2.cloudflarestorage.com", "media", "", "Cloudflare R2 bucket media"},
		"s3://media@127.0.0.1:9000":                           {"s3", "custom", "https://127.0.0.1:9000", "media", "", "S3 storage bucket media"},
		"gs://team-media/metarush@storage.googleapis.com:443": {"gcs", "", "", "team-media", "metarush", "Google Cloud Storage bucket team-media"},
		"fs:/Volumes/Studio":                                  {"filesystem", "", "", "", "", "the folder Studio on this computer"},
	} {
		tg, side := TargetFromStore(models.StoreIdentity{Locator: loc})
		if tg.Provider != want.provider || tg.Preset != want.preset || tg.Endpoint != want.endpoint || tg.Bucket != want.bucket || tg.Prefix != want.prefix || side.Name != want.name ||
			tg.SecretAccessKey != "" || tg.AccessKeyID != "" {
			t.Errorf("%s: %+v %q", loc, tg, side.Name)
		}
	}
}
