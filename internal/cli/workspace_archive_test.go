package cli

import (
	"context"
	"encoding/json"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// archiveEmptyDB is a new test database with nothing in it but the test
// run's marker (dropped after the test).
func archiveEmptyDB(t *testing.T) *store.DB {
	t.Helper()
	uri := testutil.URI()
	name := store.TestDBPrefix + strings.TrimPrefix(ids.New("t"), "t_")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := store.Connect(ctx, uri, name)
	if err != nil {
		t.Skipf("test MongoDB unavailable (run scripts/dev-mongo.sh): %v", err)
	}
	if err := db.MarkTestRun(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = db.DropTestDatabase(c, uri)
		_ = db.Close(c)
	})
	return db
}

// archiveWorkspace gives a test installation a workspace worth exporting:
// its record, its owner, a project and a picture in its storage.
func archiveWorkspace(t *testing.T) *app.App {
	t.Helper()
	a := testapp.New(t)
	ctx := context.Background()
	ws, now := a.WorkspaceID(), time.Now().UTC()
	b, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "images", "dog.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])
	p := filepath.Join(t.TempDir(), "dog.jpg")
	_ = os.WriteFile(p, b, 0o600)
	if _, err := a.Media.PutFile(ctx, media.ObjectKey(ws, sha), p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	for coll, doc := range map[string]bson.M{
		store.CWorkspaces: {"_id": ws, "workspaceId": ws, "name": "FernBrook Studio", "createdAt": now,
			"mediaStore": bson.M{"provider": a.Media.Name(), "locator": a.Media.Locator()}},
		store.CUsers:    {"_id": a.Cfg.User.ID, "workspaceId": ws, "name": "Lena Park", "email": "lena@example.com", "role": "owner", "createdAt": now},
		store.CProjects: {"_id": ids.New(ids.Project), "workspaceId": ws, "name": "Spring launch", "archived": false, "createdAt": now, "updatedAt": now},
		store.CAssets:   {"_id": ids.New(ids.Asset), "workspaceId": ws, "sha256": sha, "storageKey": media.ObjectKey(ws, sha), "createdAt": now},
	} {
		if _, err := a.DB.C(coll).InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

// asPerson plays a person at their own terminal.
func asPerson(t *testing.T) {
	t.Helper()
	old := atTerminal
	atTerminal = func() bool { return true }
	t.Cleanup(func() { atTerminal = old })
	t.Setenv("CLAUDECODE", "")
	t.Setenv("METARUSH_AGENT", "")
	t.Setenv("METARUSH_VIA", "")
}

func dbHash(t *testing.T, db *store.DB) string {
	t.Helper()
	var h bson.M
	if err := db.DB.RunCommand(context.Background(), bson.D{{Key: "dbHash", Value: 1}}).Decode(&h); err != nil {
		t.Fatal(err)
	}
	return h["md5"].(string)
}

func errorCode(t *testing.T, r cliRun) (string, string) {
	t.Helper()
	var env struct {
		OK    bool `json:"ok"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal([]byte(r.stdout), &env)
	if env.OK || r.code == 0 {
		t.Fatalf("%v should have been refused: %s", r.args, r.stdout)
	}
	return env.Error.Code, env.Error.Message
}

// At a person's terminal the export reads the database without writing to
// it (its own log entry waits in this computer's spool), the plan writes
// nothing, and the restore puts the workspace into an empty database, writes
// its own entry into the copy's log and refuses a database that isn't empty.
// Claude Code, an AI app and an address on the command line are refused.
func TestWorkspaceExportAndRestoreAtATerminal(t *testing.T) {
	a := archiveWorkspace(t)
	asPerson(t)
	h := a.Cfg.Home()
	before := dbHash(t, a.DB)
	plan := runCLI(t, h, "workspace", "export", "--to", filepath.Join(t.TempDir(), "plan"), "--plan", "--json")
	var planned ops.WorkspaceExportOut
	decodeData(t, plan, &planned)
	if planned.Plan == nil || planned.Plan.Media.Files != 1 || planned.Plan.Workspace.Name != "FernBrook Studio" || !strings.Contains(planned.Holds, "consent") {
		t.Fatalf("plan: %+v", planned)
	}
	to := filepath.Join(t.TempDir(), "studio.tar")
	r := runCLI(t, h, "workspace", "export", "--to", to, "--tar", "--json")
	var out ops.WorkspaceExportOut
	decodeData(t, r, &out)
	if out.Export == nil || out.Export.State != "done" || out.Export.Documents < 4 || out.Export.MediaFiles != 1 {
		t.Fatalf("export: %+v", out.Export)
	}
	if info, err := os.Stat(to); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("archive: %v %v", info, err)
	}
	if after := dbHash(t, a.DB); after != before {
		t.Fatal("the export wrote into the database it read")
	}
	spooled := false
	files, _ := filepath.Glob(filepath.Join(a.Cfg.LogSpoolDir(), "*.jsonl"))
	for _, f := range files {
		if b, _ := os.ReadFile(f); strings.Contains(string(b), `"workspace.export"`) {
			spooled = true
		}
	}
	if !spooled {
		t.Error("the export's own entry should wait in this computer's spool")
	}
	// The plain words say where it is and how private it is.
	plain := runCLI(t, h, "workspace", "export", "--to", filepath.Join(t.TempDir(), "plain"))
	if plain.code != 0 || !strings.Contains(plain.stdout, "The archive is ready") || !strings.Contains(plain.stdout, "Only your account on this computer can open it") {
		t.Fatalf("plain words: %s %s", plain.stdout, plain.stderr)
	}
	// Refusals: Claude Code at the keys, an AI app, an address in argv.
	t.Setenv("CLAUDECODE", "1")
	if c, _ := errorCode(t, runCLI(t, h, "workspace", "export", "--to", filepath.Join(t.TempDir(), "x"), "--json")); c != "E_HUMAN_ONLY" {
		t.Fatalf("Claude Code exporting: %s", c)
	}
	if c, _ := errorCode(t, runCLI(t, h, "workspace", "restore", to, "--json")); c != "E_HUMAN_ONLY" {
		t.Fatalf("Claude Code restoring: %s", c)
	}
	t.Setenv("CLAUDECODE", "")
	if c, _ := errorCode(t, runCLI(t, h, "--agent", "codex", "workspace", "export", "--to", filepath.Join(t.TempDir(), "x"), "--json")); c != "E_HUMAN_ONLY" {
		t.Fatalf("an AI app exporting: %s", c)
	}
	if c, m := errorCode(t, runCLI(t, h, "workspace", "export", "--to", filepath.Join(t.TempDir(), "x"), "--db-uri", testutil.URI(), "--json")); c != "E_INVALID_INPUT" || !strings.Contains(m, "history") {
		t.Fatalf("an address in argv: %s %s", c, m)
	}
	old := atTerminal
	atTerminal = func() bool { return false }
	if c, _ := errorCode(t, runCLI(t, h, "workspace", "export", "--to", filepath.Join(t.TempDir(), "x"), "--json")); c != "E_HUMAN_ONLY" {
		t.Fatalf("a script nobody watches: %s", c)
	}
	atTerminal = old
	// The restore, into another database, from a computer that isn't set up.
	dst := archiveEmptyDB(t)
	t.Setenv("ARCHIVE_TEST_DB_URI", testutil.URI())
	bare := filepath.Join(t.TempDir(), "bare")
	restore := []string{"workspace", "restore", to, "--db-uri-env", "ARCHIVE_TEST_DB_URI", "--db-name", dst.Name, "--storage", "folder", "--media-root", a.Cfg.Media.Filesystem.Root, "--json"}
	r = runCLI(t, bare, restore...)
	var rep restoreOut
	decodeData(t, r, &rep)
	if rep.RestoreReport == nil || !rep.Verified || rep.Workspace.ID != a.WorkspaceID() || rep.Media.Present != 1 || rep.Media.Missing != 0 || len(rep.Next) == 0 {
		t.Fatalf("restore: %+v", rep)
	}
	ctx := context.Background()
	if n, _ := dst.C(store.CLogEntries).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "op.name": wsarchive.OpRestore}); n != 1 {
		t.Errorf("the restore's entry in the copy's log: %d", n)
	}
	if n, _ := dst.C(store.CAudit).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "action": wsarchive.OpRestore}); n != 1 {
		t.Errorf("the restore in the copy's change history: %d", n)
	}
	if c, _ := errorCode(t, runCLI(t, bare, restore...)); c != "E_CONFLICT" {
		t.Fatalf("a database that isn't empty: %s", c)
	}
}

// A new computer starts from an export: setup restores it into the new,
// empty database, brings it up to date and joins it as its owner.
func TestSetupRestoresAnExport(t *testing.T) {
	a := archiveWorkspace(t)
	asPerson(t)
	to := filepath.Join(t.TempDir(), "studio.tar")
	decodeData(t, runCLI(t, a.Cfg.Home(), "workspace", "export", "--to", to, "--tar", "--json"), &ops.WorkspaceExportOut{})
	setupEnv(t)
	dst := archiveEmptyDB(t)
	h := filepath.Join(t.TempDir(), "home")
	r := runCLI(t, h, "setup", "--yes", "--json", "--database", "elsewhere", "--db-uri", testutil.URI(), "--db-name", dst.Name, "--test-db",
		"--secrets", "env", "--service=false", "--skills=false", "--no-components", "--port", strconv.Itoa(freeTCPPort(t)),
		"--storage", "folder", "--media-root", a.Cfg.Media.Filesystem.Root, "--restore", to)
	var res SetupResult
	decodeData(t, r, &res)
	if res.Restored == nil || res.Workspace.ID != a.WorkspaceID() || res.Workspace.Name != "FernBrook Studio" || res.Restored.MediaMissing != 0 {
		t.Fatalf("setup: %+v %+v", res, res.Restored)
	}
	if res.User.ID != a.Cfg.User.ID {
		t.Errorf("the computer should work as the workspace's owner: %+v", res.User)
	}
	ctx := context.Background()
	if n, _ := dst.C(store.CInstallations).CountDocuments(ctx, bson.M{"_id": res.Installation.ID, "workspaceId": a.WorkspaceID()}); n != 1 {
		t.Error("this computer didn't join the restored workspace")
	}
	if n, _ := dst.C(store.CUsers).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()}); n != 1 {
		t.Errorf("setup made another person: %d", n)
	}
	if pending, _ := dst.PendingMigrations(ctx); len(pending) != 0 {
		t.Errorf("not up to date: %v", pending)
	}
	// Setting up again never restores over it.
	again := runCLI(t, h, "setup", "--yes", "--json", "--service=false", "--restore", to)
	if again.code == 0 {
		t.Fatalf("setup on a set-up computer restored: %s", again.stdout)
	}
}
