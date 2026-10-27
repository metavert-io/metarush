package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// dbHashBut is the server's hash of a database's collections but the move's
// marker.
func dbHashBut(t *testing.T, db *store.DB) string {
	t.Helper()
	ctx := context.Background()
	names, err := db.DB.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	var colls bson.A
	for _, n := range names {
		if n != store.CDatabaseMove {
			colls = append(colls, n)
		}
	}
	var h bson.M
	if err := db.DB.RunCommand(ctx, bson.D{{Key: "dbHash", Value: 1}, {Key: "collections", Value: colls}}).Decode(&h); err != nil {
		t.Fatal(err)
	}
	return h["md5"].(string)
}

// laptopHome is a second installation home of the same workspace on the same
// database: another computer (Edit laptop).
func laptopHome(t *testing.T, cfgA *config.Config) string {
	t.Helper()
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace, cfg.User, cfg.Database, cfg.Media = cfgA.Workspace, cfgA.User, cfgA.Database, cfgA.Media
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "Edit laptop"}
	cfg.Dashboard.Port = 4857
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	return h
}

// The move at a person's terminal, with a second computer's home in the same
// test: the address comes from an environment variable (never the command
// line, which refuses one), the plan and the move answer in plain words and
// --json, the old database is untouched but its marker, and the laptop —
// still on it — sees "moved", refuses changes and switches with its own
// sign-in. The address shows up in no command line, output, settings file or
// log; Claude Code at the keys is refused.
func TestDatabaseMoveAtATerminal(t *testing.T) {
	keyring.MockInit()
	a := archiveWorkspace(t)
	asPerson(t)
	ctx := context.Background()
	h := a.Cfg.Home()
	hb := laptopHome(t, a.Cfg)
	now := time.Now().UTC()
	for _, in := range []models.Installation{
		{ID: a.Cfg.Installation.ID, WorkspaceID: a.WorkspaceID(), Name: "Studio Mac", CreatedAt: now},
		{ID: ids.New(ids.Installation), WorkspaceID: a.WorkspaceID(), Name: "Edit laptop", CreatedAt: now},
	} {
		if _, err := a.DB.C(store.CInstallations).InsertOne(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	dst := archiveEmptyDB(t)
	token := "mrmove" + strings.ToLower(strings.TrimPrefix(ids.New("t"), "t_"))
	uri := testutil.URI() + "&appName=" + token
	t.Setenv("NEW_DATABASE", uri)
	var runs []cliRun
	run := func(home string, args ...string) cliRun {
		r := runCLI(t, home, args...)
		runs = append(runs, r)
		return r
	}

	// An address on the command line is refused, without repeating it.
	r := run(h, "database", "plan", "--to", uri, "--json")
	if code, _ := errorCode(t, r); code != apperr.InvalidInput {
		t.Fatalf("an address as --to: %s", r.stdout)
	}
	r = run(h, "database", "plan", "--to", "env:NEW_DATABASE", "--name", dst.Name)
	if r.code != 0 || !strings.Contains(r.stdout, "Moving would copy the workspace's database") || !strings.Contains(r.stdout, "Edit laptop") {
		t.Fatalf("plan (exit %d):\n%s\n%s", r.code, r.stdout, r.stderr)
	}
	if f := plainwords.Check(r.stdout); len(f) > 0 {
		t.Fatalf("the plan isn't plain: %v\n%s", f, r.stdout)
	}
	// Claude Code at the keys doesn't move it.
	t.Setenv("CLAUDECODE", "1")
	r = run(h, "database", "move", "--to", "saved", "--name", dst.Name, "--yes", "--json")
	if code, _ := errorCode(t, r); code != apperr.HumanOnly {
		t.Fatalf("Claude Code moved it: %s", r.stdout)
	}
	t.Setenv("CLAUDECODE", "")
	before := dbHashBut(t, a.DB) // the plan and the refusals are in its activity log
	r = run(h, "database", "move", "--to", "saved", "--name", dst.Name, "--yes", "--json")
	var out ops.DatabaseMoveOut
	decodeData(t, r, &out)
	if !out.Verified || !out.Switched || !out.Marked {
		t.Fatalf("move: %+v", out)
	}
	if dbHashBut(t, a.DB) != before {
		t.Fatal("the move changed the old database")
	}
	// This computer works in the new database now.
	r = run(h, "project", "create", "Summer", "--json")
	if r.code != 0 {
		t.Fatalf("a change here after the move: %s %s", r.stdout, r.stderr)
	}
	if n, _ := dst.C(store.CProjects).CountDocuments(ctx, bson.M{"name": "Summer"}); n != 1 {
		t.Fatal("the change didn't go into the new database")
	}

	// The laptop, still on the old database.
	r = run(hb, "project", "create", "Lost", "--json")
	if code, msg := errorCode(t, r); code != apperr.Moving || !strings.Contains(msg, "switch this computer too") {
		t.Fatalf("a change on the laptop: %s", r.stdout)
	}
	r = run(hb, "database", "move-status")
	if r.code != 0 || !strings.Contains(r.stdout, "switch this computer too") {
		t.Fatalf("status on the laptop:\n%s", r.stdout)
	}
	if f := plainwords.Check(r.stdout); len(f) > 0 {
		t.Fatalf("the status isn't plain: %v\n%s", f, r.stdout)
	}
	t.Setenv("LAPTOP_DATABASE", testutil.URI())
	r = run(hb, "database", "switch", "--to", "env:LAPTOP_DATABASE", "--json")
	var sw ops.DatabaseSwitchOut
	decodeData(t, r, &sw)
	if !sw.Switched {
		t.Fatalf("switch: %+v", sw)
	}
	r = run(hb, "project", "create", "Autumn", "--json")
	if r.code != 0 {
		t.Fatalf("a change on the laptop after switching: %s %s", r.stdout, r.stderr)
	}
	if dbHashBut(t, a.DB) != before {
		t.Fatal("the old database changed")
	}

	// The address is nowhere it shouldn't be.
	for _, r := range runs {
		for _, arg := range r.args {
			if strings.Contains(arg, token) && arg != uri {
				t.Fatalf("the address is in a command line: %v", r.args)
			}
		}
		if strings.Contains(r.stdout+r.stderr, token) {
			t.Fatalf("the address is in what %v said:\n%s%s", r.args, r.stdout, r.stderr)
		}
	}
	for _, home := range []string{h, hb} {
		_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if b, _ := os.ReadFile(p); strings.Contains(string(b), token) {
					t.Errorf("the address is in %s", p)
				}
			}
			return nil
		})
	}
	m, _ := a.DB.ReadMoveMarker(ctx)
	if b, _ := json.Marshal(m); m == nil || strings.Contains(string(b), token) || strings.Contains(string(b), "mongodb://") {
		t.Fatalf("marker: %s", b)
	}
	for _, db := range []*store.DB{a.DB, dst} {
		cur, err := db.C(store.CLogEntries).Find(ctx, bson.M{})
		if err != nil {
			t.Fatal(err)
		}
		var docs []bson.M
		_ = cur.All(ctx, &docs)
		for _, d := range docs {
			if b, _ := json.Marshal(d); strings.Contains(string(b), token) {
				t.Fatalf("the address is in the log: %s", b)
			}
		}
	}
}
