package ops_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// moveDB is a new, empty test database for a move to go into (dropped after
// the test).
func moveDB(t *testing.T) *store.DB {
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

// hashBut is the server's own hash of a database's collections but the
// move's marker and the activity log (whose entries go in until the move
// freezes it: logQuietSince checks those): "the old database is untouched".
func hashBut(t *testing.T, db *store.DB) string {
	t.Helper()
	ctx := context.Background()
	names, err := db.DB.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	var colls bson.A
	for _, n := range names {
		if n != store.CDatabaseMove && n != store.CLogEntries {
			colls = append(colls, n)
		}
	}
	var h bson.M
	if err := db.DB.RunCommand(ctx, bson.D{{Key: "dbHash", Value: 1}, {Key: "collections", Value: colls}}).Decode(&h); err != nil {
		t.Fatal(err)
	}
	return h["md5"].(string)
}

// logQuietSince fails when the old database's activity log got an entry
// after the move froze it.
func logQuietSince(t *testing.T, db *store.DB) {
	t.Helper()
	ctx := context.Background()
	m, err := db.ReadMoveMarker(ctx)
	if err != nil || m == nil {
		t.Fatalf("no marker: %v", err)
	}
	if n, _ := db.C(store.CLogEntries).CountDocuments(ctx, bson.M{"at": bson.M{"$gte": m.StartedAt}}); n > 0 {
		t.Fatalf("%d activity log entries went into the old database after it froze", n)
	}
}

// databaseExists says whether the server still has a database.
func databaseExists(t *testing.T, db *store.DB) bool {
	t.Helper()
	names, err := db.Client.ListDatabaseNames(context.Background(), bson.M{"name": db.Name})
	if err != nil {
		t.Fatal(err)
	}
	return len(names) == 1
}

// moveWorkspace is a workspace to move: this computer (Studio Mac, its owner
// Maya), a second one (Edit laptop) on the same database, a project with a
// comment, a password. The keychain is a stand-in.
type moveWorkspace struct {
	a, b    *app.App
	project string
}

func newMoveWorkspace(t *testing.T) *moveWorkspace {
	t.Helper()
	keyring.MockInit() // the new database's address and the switch: never the real keychain
	a := testapp.New(t)
	ctx := context.Background()
	ws, now := a.WorkspaceID(), time.Now().UTC()
	a.Cfg.Installation.Name = "Studio Mac"
	b := secondComputer(t, a, "Edit laptop")
	for coll, doc := range map[string]bson.M{
		store.CWorkspaces: {"_id": ws, "workspaceId": ws, "name": "FernBrook Studio", "createdAt": now,
			"mediaStore": bson.M{"provider": a.Media.Name(), "locator": a.Media.Locator()}},
		store.CUsers:           {"_id": a.Cfg.User.ID, "workspaceId": ws, "name": "Maya Okafor", "email": "maya@example.com", "role": "owner", "createdAt": now},
		store.CUserCredentials: {"_id": ids.New("ucr"), "workspaceId": ws, "userId": a.Cfg.User.ID, "hash": "$argon2id$v=19$m=65536,t=1,p=4$c2FsdA$aGFzaA"},
		store.CComments:        {"_id": ids.New("cmt"), "workspaceId": ws, "text": "Lena: tighten the intro", "createdAt": now},
	} {
		if _, err := a.DB.C(coll).InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	for _, in := range []models.Installation{
		{ID: a.Cfg.Installation.ID, WorkspaceID: ws, Name: "Studio Mac", CreatedAt: now},
		{ID: b.Cfg.Installation.ID, WorkspaceID: ws, Name: "Edit laptop", CreatedAt: now, LastSeenAt: now.Add(-time.Hour)},
	} {
		if _, err := a.DB.C(store.CInstallations).InsertOne(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Spring launch"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	return &moveWorkspace{a: a, b: b, project: p.Project.ID}
}

// secondComputer is another installation home of the same workspace, on the
// same database (a computer of its own: its own home, keychain entries and
// installation).
func secondComputer(t *testing.T, a *app.App, name string) *app.App {
	t.Helper()
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = a.Cfg.Workspace
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: name}
	cfg.User = a.Cfg.User
	cfg.Database = a.Cfg.Database
	cfg.Media = a.Cfg.Media
	cfg.Dashboard.Port = 4858
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	b, err := app.OpenWith(context.Background(), cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	return b
}

// secretAddress is the new database's address with a made-up token in it, so
// a test can tell it never shows up where it shouldn't.
func secretAddress(t *testing.T) (uri, token string) {
	token = "mrmove" + strings.ToLower(strings.TrimPrefix(ids.New("t"), "t_"))
	return testutil.URI() + "&appName=" + token, token
}

func codeAndReason(err error) (string, string) {
	ae := apperr.From(err)
	if ae == nil {
		return "", ""
	}
	r := ""
	if d, ok := ae.Details.(map[string]any); ok {
		r, _ = d["reason"].(string)
	}
	return ae.Code, r
}

// The whole move between two test databases: the plan, the copy checked
// collection by collection, this computer switched (its address in the
// keychain, the old one kept aside), the old database untouched apart from
// its "moved" marker and never deleted, the move recorded in the new one.
// The second computer, still on the old database, reads but refuses changes
// (E_MOVING, "switch this computer too") until it switches with its own
// sign-in; then it works in the new database. The address never shows up in
// the marker, the settings file, the log or an answer.
func TestDatabaseMoveBetweenTwoDatabases(t *testing.T) {
	w := newMoveWorkspace(t)
	a, b := w.a, w.b
	ctx := context.Background()
	dst := moveDB(t)
	uri, token := secretAddress(t)
	before := hashBut(t, a.DB)
	if err := ops.SaveMoveAddress(a.Cfg, uri); err != nil {
		t.Fatal(err)
	}
	target := ops.DatabaseTargetIn{To: ops.MoveToSaved, Name: dst.Name}
	plan, err := ops.CallTyped[ops.DatabasePlanOut](ctx, a, "database.plan", ops.DatabasePlanIn{DatabaseTargetIn: target}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Ready || !plan.Target.Empty || !plan.Target.Writable || plan.Documents == 0 || plan.Takes == "" || len(plan.Frozen) == 0 {
		t.Fatalf("plan: %+v", plan)
	}
	if plan.To.Kind != "server" || plan.To.Reach != "every-computer" || plan.To.Cloud {
		t.Fatalf("where: %+v", plan.To)
	}
	var laptop *ops.DatabaseComputer
	for i := range plan.Computers {
		if plan.Computers[i].Name == "Edit laptop" {
			laptop = &plan.Computers[i]
		}
	}
	if laptop == nil || !strings.Contains(laptop.Needs, "own sign-in") || laptop.Running {
		t.Fatalf("computers: %+v", plan.Computers)
	}
	if hashBut(t, a.DB) != before {
		t.Fatal("the plan changed the old database")
	}

	out, err := ops.CallTyped[ops.DatabaseMoveOut](ctx, a, "database.move", ops.DatabaseMoveIn{DatabaseTargetIn: target}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if !out.Verified || !out.Switched || !out.Marked || len(out.Others) != 1 || !strings.Contains(out.Kept, "nothing is ever deleted") {
		t.Fatalf("move: %+v", out)
	}
	// The old database: untouched but its marker, still there.
	if hashBut(t, a.DB) != before {
		t.Fatal("the move changed the old database")
	}
	logQuietSince(t, a.DB)
	if !databaseExists(t, a.DB) {
		t.Fatal("the old database is gone")
	}
	m, _ := a.DB.ReadMoveMarker(ctx)
	if m == nil || m.State != store.MoveMoved || m.To.Name != dst.Name || m.Switched[a.Cfg.Installation.ID].IsZero() {
		t.Fatalf("marker: %+v", m)
	}
	// The new database: every record, its lock marked whole, the move recorded.
	for _, coll := range []string{store.CProjects, store.CComments, store.CUserCredentials, store.CUsers, store.CWorkspaces} {
		n1, _ := a.DB.C(coll).CountDocuments(ctx, bson.M{})
		n2, _ := dst.C(coll).CountDocuments(ctx, bson.M{})
		if n1 == 0 || n1 != n2 {
			t.Fatalf("%s: %d vs %d", coll, n1, n2)
		}
	}
	if lock, _ := dst.ReadRestoreLock(ctx); lock == nil || lock.Kind != store.LockMove || lock.State != store.RestoreDone {
		t.Fatalf("lock: %+v", lock)
	}
	if n, _ := dst.C(store.CAudit).CountDocuments(ctx, bson.M{"action": "database.move"}); n != 1 {
		t.Fatal("the move isn't in the new database's change history")
	}
	if n, _ := dst.C(store.CDatabaseMove).CountDocuments(ctx, bson.M{}); n != 0 {
		t.Fatal("the marker was copied")
	}
	// This computer: switched, the old address kept aside, the move's copy gone.
	home := a.Cfg.Home()
	cfg, err := config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URI != uri || cfg.Database.Name != dst.Name {
		t.Fatalf("this computer uses %s / %s", config.RedactURI(cfg.Database.URI), cfg.Database.Name)
	}
	if prev, _ := config.ResolveSecret(home, config.SecretRef("mongodb-uri-previous")); prev != a.Cfg.Database.URI {
		t.Fatal("the old address wasn't kept aside")
	}
	if left, _ := ops.LoadMoveAddress(cfg); left != "" {
		t.Fatal("the move's own copy of the address stayed")
	}
	// The address never shows up in the marker, the settings, the log or an answer.
	raw, _ := os.ReadFile(config.Path(home))
	mj, _ := json.Marshal(m)
	oj, _ := json.Marshal(out)
	pj, _ := json.Marshal(plan)
	for what, text := range map[string]string{"settings": string(raw), "marker": string(mj), "answer": string(oj), "plan": string(pj)} {
		if strings.Contains(text, token) || strings.Contains(text, "mongodb://") {
			t.Fatalf("the address is in the %s: %s", what, text)
		}
	}
	a.Log.Flush(ctx)
	for _, db := range []*store.DB{a.DB, dst} {
		var hits []bson.M
		cur, _ := db.C(store.CLogEntries).Find(ctx, bson.M{})
		_ = cur.All(ctx, &hits)
		for _, h := range hits {
			if b, _ := json.Marshal(h); strings.Contains(string(b), token) {
				t.Fatalf("the address is in the log: %s", b)
			}
		}
	}
	_ = filepath.WalkDir(home, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), token) {
				t.Errorf("the address is in %s", p)
			}
		}
		return nil
	})

	// The second computer, still on the old database: reads work, changes are refused.
	if _, err := ops.CallTyped[ops.ProjectListOut](ctx, b, "project.list", ops.ProjectListIn{}, ops.Caller{Human: true}); err != nil {
		t.Fatalf("reading on the second computer: %v", err)
	}
	_, err = ops.CallTyped[ops.ProjectOut](ctx, b, "project.create", ops.ProjectCreateIn{Name: "Lost"}, ops.Caller{Human: true})
	if c, r := codeAndReason(err); c != apperr.Moving || r != "database_moved" || !strings.Contains(apperr.From(err).Message, "switch this computer too") {
		t.Fatalf("a change on the second computer: %v", err)
	}
	st, err := ops.CallTyped[ops.DatabaseMoveStatusOut](ctx, b, "database.move.status", ops.DatabaseMoveStatusIn{}, ops.Caller{})
	if err != nil || !st.SwitchMeToo || !st.CanSwitch || !strings.Contains(st.Command, "database switch") || len(st.Steps) == 0 {
		t.Fatalf("status there: %+v %v", st, err)
	}
	// It switches with its own address for the new database.
	if err := ops.SaveMoveAddress(b.Cfg, testutil.URI()); err != nil {
		t.Fatal(err)
	}
	sw, err := ops.CallTyped[ops.DatabaseSwitchOut](ctx, b, "database.switch", ops.DatabaseSwitchIn{}, ops.Caller{Human: true})
	if err != nil || !sw.Switched {
		t.Fatalf("switch: %+v %v", sw, err)
	}
	if m, _ := a.DB.ReadMoveMarker(ctx); m.Switched[b.Cfg.Installation.ID].IsZero() {
		t.Fatal("the marker doesn't know the second computer switched")
	}
	b2, err := app.Open(ctx, b.Cfg.Home(), "cli")
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if b2.DB.Name != dst.Name {
		t.Fatalf("the second computer opens %s", b2.DB.Name)
	}
	if _, err := ops.CallTyped[ops.ProjectOut](ctx, b2, "project.create", ops.ProjectCreateIn{Name: "Summer"}, ops.Caller{Human: true}); err != nil {
		t.Fatalf("a change after switching: %v", err)
	}
	if hashBut(t, a.DB) != before {
		t.Fatal("the switch changed the old database")
	}
	logQuietSince(t, a.DB)
	// Switching again: there is no move in the new database.
	if _, err := ops.CallTyped[ops.DatabaseSwitchOut](ctx, b2, "database.switch", ops.DatabaseSwitchIn{}, ops.Caller{Human: true}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("a second switch: %v", err)
	}
}

// While the move waits for a computer running MetaRush to pause, changes are
// refused on every computer (plainly: "being moved") and reads work; once it
// paused, the move goes on.
func TestDatabaseMoveFreezesChanges(t *testing.T) {
	w := newMoveWorkspace(t)
	a, b := w.a, w.b
	ctx := context.Background()
	// The laptop runs MetaRush now: the move waits for it.
	if _, err := a.DB.C(store.CInstallations).UpdateOne(ctx, bson.M{"_id": b.Cfg.Installation.ID}, bson.M{"$set": bson.M{"lastSeenAt": time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}
	dst := moveDB(t)
	if err := ops.SaveMoveAddress(a.Cfg, testutil.URI()); err != nil {
		t.Fatal(err)
	}
	target := ops.DatabaseTargetIn{To: ops.MoveToSaved, Name: dst.Name}
	var wg sync.WaitGroup
	var out *ops.DatabaseMoveOut
	var moveErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		out, moveErr = ops.CallTyped[ops.DatabaseMoveOut](ctx, a, "database.move", ops.DatabaseMoveIn{DatabaseTargetIn: target}, ops.Caller{Human: true})
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		m, _ := a.DB.ReadMoveMarker(ctx)
		if m != nil && m.State == store.MoveMoving {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the move didn't freeze the workspace")
		}
		time.Sleep(100 * time.Millisecond)
	}
	time.Sleep(app.MoveMarkerTTL + 100*time.Millisecond)
	for name, c := range map[string]*app.App{"this computer": a, "the laptop": b} {
		_, err := ops.CallTyped[ops.ProjectOut](ctx, c, "project.create", ops.ProjectCreateIn{Name: "During"}, ops.Caller{Human: true})
		code, reason := codeAndReason(err)
		if code != apperr.Moving || reason != "database_moving" || !strings.Contains(apperr.From(err).Message, "changes wait until it's done") {
			t.Fatalf("a change on %s during the move: %v", name, err)
		}
		if _, err := ops.CallTyped[ops.ProjectListOut](ctx, c, "project.list", ops.ProjectListIn{}, ops.Caller{Human: true}); err != nil {
			t.Fatalf("reading on %s during the move: %v", name, err)
		}
	}
	st, err := ops.CallTyped[ops.DatabaseMoveStatusOut](ctx, b, "database.move.status", ops.DatabaseMoveStatusIn{}, ops.Caller{})
	if err != nil || st.State != store.MoveMoving || !st.Frozen || st.SwitchMeToo {
		t.Fatalf("status during the move: %+v %v", st, err)
	}
	// The laptop's MetaRush pauses (what its daemon does when it holds).
	if _, err := ops.HoldForMove(ctx, b); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if moveErr != nil || !out.Verified {
		t.Fatalf("move: %+v %v", out, moveErr)
	}
	if m, _ := a.DB.ReadMoveMarker(ctx); m.Holding[b.Cfg.Installation.ID].IsZero() {
		t.Fatal("the laptop's pause isn't in the marker")
	}
}

// A move stopped part-way keeps changes waiting; run again, it continues and
// finishes. Another, abandoned, lifts the freeze: changes go on in the old
// database and the copy keeps its lock, so nothing uses it.
func TestDatabaseMoveResumesOrIsAbandoned(t *testing.T) {
	for _, abandon := range []bool{false, true} {
		w := newMoveWorkspace(t)
		a := w.a
		ctx := context.Background()
		var docs []any
		for i := 0; i < 1500; i++ {
			docs = append(docs, bson.M{"_id": ids.New("log"), "workspaceId": a.WorkspaceID(), "summary": "Sam renamed a clip", "n": i})
		}
		if _, err := a.DB.C(store.CLogEntries).InsertMany(ctx, docs); err != nil {
			t.Fatal(err)
		}
		dst := moveDB(t)
		if err := ops.SaveMoveAddress(a.Cfg, testutil.URI()); err != nil {
			t.Fatal(err)
		}
		target := ops.DatabaseTargetIn{To: ops.MoveToSaved, Name: dst.Name}
		// Stopped while it copies the activity log (Ctrl-C).
		cctx, cancel := context.WithCancel(ctx)
		cctx = ops.WithProgress(cctx, func(label string, done, total int64) {
			if strings.HasPrefix(label, "Copying") {
				if n, _ := dst.C(store.CLogEntries).CountDocuments(ctx, bson.M{}); n >= 500 {
					cancel()
				}
			}
		})
		_, err := ops.CallTyped[ops.DatabaseMoveOut](cctx, a, "database.move", ops.DatabaseMoveIn{DatabaseTargetIn: target}, ops.Caller{Human: true})
		cancel()
		if apperr.From(err).Code != apperr.Canceled {
			t.Fatalf("stopped: %v", err)
		}
		m, _ := a.DB.ReadMoveMarker(ctx)
		if m == nil || m.State != store.MoveMoving {
			t.Fatalf("after the stop: %+v", m)
		}
		time.Sleep(app.MoveMarkerTTL + 100*time.Millisecond)
		if _, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Waiting"}, ops.Caller{Human: true}); apperr.From(err).Code != apperr.Moving {
			t.Fatalf("a change after the stop: %v", err)
		}
		st, _ := ops.CallTyped[ops.DatabaseMoveStatusOut](ctx, a, "database.move.status", ops.DatabaseMoveStatusIn{}, ops.Caller{})
		if st.Run == nil || st.Run.State != ops.DBMoveFailed || st.State != store.MoveMoving {
			t.Fatalf("status after the stop: %+v", st)
		}
		if !abandon {
			out, err := ops.CallTyped[ops.DatabaseMoveOut](ctx, a, "database.move", ops.DatabaseMoveIn{DatabaseTargetIn: target}, ops.Caller{Human: true})
			if err != nil || !out.Resumed || !out.Verified {
				t.Fatalf("continued: %+v %v", out, err)
			}
			if n, _ := dst.C(store.CLogEntries).CountDocuments(ctx, bson.M{}); n < 1500 {
				t.Fatalf("log entries copied: %d", n)
			}
			continue
		}
		ab, err := ops.CallTyped[ops.DatabaseAbandonOut](ctx, a, "database.abandon", ops.DatabaseAbandonIn{}, ops.Caller{Human: true})
		if err != nil || !ab.Abandoned {
			t.Fatalf("abandon: %+v %v", ab, err)
		}
		if _, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Going on"}, ops.Caller{Human: true}); err != nil {
			t.Fatalf("a change after abandoning: %v", err)
		}
		if err := dst.CheckNoUnfinishedRestore(ctx); err == nil {
			t.Fatal("the half copy can be used")
		}
		if _, err := dst.Migrate(ctx); err == nil {
			t.Fatal("the half copy was brought up to date")
		}
		if cfg, _ := config.Load(a.Cfg.Home()); cfg.Database.Name != a.Cfg.Database.Name {
			t.Fatal("this computer switched to a half copy")
		}
		// Abandoning again: nothing waits.
		if ab, err := ops.CallTyped[ops.DatabaseAbandonOut](ctx, a, "database.abandon", ops.DatabaseAbandonIn{}, ops.Caller{Human: true}); err != nil || ab.Abandoned {
			t.Fatalf("again: %+v %v", ab, err)
		}
	}
}

// A client whose rules keep their words from cloud storage stops a move to a
// database on the internet: the plan says so first (nothing recorded), the
// move refuses (E_KEPT_HERE, naming the rule) and records the refusal; a
// database on this computer or the network is fine.
func TestDatabaseMoveKeepsClientsRules(t *testing.T) {
	w := newMoveWorkspace(t)
	a := w.a
	ctx := context.Background()
	if _, err := ops.CallTyped[leave.LeaveOut](ctx, a, "content.leave.set", leave.LeaveSetIn{ProjectID: w.project, Keep: map[string][]string{"storage": {"words"}}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if err := ops.SaveMoveAddress(a.Cfg, "mongodb+srv://cluster0.example.mongodb.net/"); err != nil {
		t.Fatal(err)
	}
	target := ops.DatabaseTargetIn{To: ops.MoveToSaved, Name: "studio"}
	refusals := func() int64 {
		n, _ := a.DB.C(store.CAudit).CountDocuments(ctx, bson.M{"action": leave.AuditRefused})
		return n
	}
	plan, err := ops.CallTyped[ops.DatabasePlanOut](ctx, a, "database.plan", ops.DatabasePlanIn{DatabaseTargetIn: target}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ready || len(plan.KeptHere) != 1 || !plan.To.Cloud || plan.To.Kind != "atlas" || refusals() != 0 {
		t.Fatalf("plan: %+v", plan)
	}
	_, err = ops.CallTyped[ops.DatabaseMoveOut](ctx, a, "database.move", ops.DatabaseMoveIn{DatabaseTargetIn: target}, ops.Caller{Human: true})
	if apperr.From(err).Code != apperr.KeptHere || refusals() != 1 {
		t.Fatalf("move: %v (refusals %d)", err, refusals())
	}
	if m, _ := a.DB.ReadMoveMarker(ctx); m != nil {
		t.Fatalf("frozen anyway: %+v", m)
	}
	// A database on this computer or the network is no cloud storage.
	for _, d := range []config.Database{{URI: "mongodb://10.0.0.5:27017/", Name: "studio"}, {URI: testutil.URI(), Name: "studio"}, {Local: &config.LocalDatabase{Port: 27150}, Name: "studio"}} {
		if err := ops.LeaveDatabase(ctx, a, d, true); err != nil {
			t.Fatalf("%s: %v", config.HostOf(d.URI), err)
		}
	}
}

// Moving, switching and abandoning are an owner's and a person's, every
// time; the plan is an owner's on any surface.
func TestDatabaseMoveIsAnOwnersAndAPersons(t *testing.T) {
	w := newMoveWorkspace(t)
	a := w.a
	ctx := context.Background()
	if err := ops.SaveMoveAddress(a.Cfg, testutil.URI()); err != nil {
		t.Fatal(err)
	}
	target := ops.DatabaseTargetIn{To: ops.MoveToSaved, Name: store.TestDBPrefix + "never-made"}
	member := ops.OverHTTP(ctx, func() bool { return false })
	for _, name := range []string{"database.plan", "database.abandon"} {
		var in any = ops.DatabasePlanIn{DatabaseTargetIn: target}
		if name == "database.abandon" {
			in = ops.DatabaseAbandonIn{}
		}
		_, err := ops.Call(member, a, name, archiveJSON(in), ops.Caller{Human: true, Remote: true})
		if c, r := codeAndReason(err); c != apperr.Unauthorized || r != "owner_only" {
			t.Fatalf("a member's %s: %v", name, err)
		}
	}
	changes := map[string]any{"database.move": ops.DatabaseMoveIn{DatabaseTargetIn: target}, "database.switch": ops.DatabaseSwitchIn{}, "database.abandon": ops.DatabaseAbandonIn{}}
	for name, in := range changes {
		// Not a person.
		if _, err := ops.Call(ctx, a, name, archiveJSON(in), ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
			t.Fatalf("%s without a person: %v", name, err)
		}
		// Claude in the dashboard, Auto, an AI app, MCP, a propose-only app.
		for what, change := range map[string]func(b *appCopy){
			"Claude": func(b *appCopy) { b.Actor.Via = models.ViaAssistant },
			"Auto":   func(b *appCopy) { b.Actor.Via = models.ViaAssistantAuto },
			"an app": func(b *appCopy) { b.Actor.Agent = "codex" },
			"MCP":    func(b *appCopy) { b.Actor.Via = "mcp" },
			"drafts": func(b *appCopy) { b.ProposeOnly = true },
		} {
			b := *a
			change(&b)
			_, err := ops.Call(ctx, &b, name, archiveJSON(in), ops.Caller{Human: true})
			if c := apperr.From(err).Code; c != apperr.HumanOnly && c != apperr.Unauthorized {
				t.Fatalf("%s by %s: %v", name, what, err)
			}
		}
		// A review-desk draft can't hold it.
		d := *a
		d.Actor.Agent = "codex"
		if _, err := ops.CallTyped[ops.DraftView](ctx, &d, "draft.save", ops.DraftSaveIn{Op: name, Input: archiveJSON(in)}, ops.Caller{}); err == nil {
			t.Fatalf("a draft holds %s", name)
		}
	}
	// The plan on an AI app's channel (MCP) is allowed: an owner's reading.
	mcp := *a
	mcp.Actor.Via = "mcp"
	if _, err := ops.CallTyped[ops.DatabasePlanOut](ctx, &mcp, "database.plan", ops.DatabasePlanIn{DatabaseTargetIn: target}, ops.Caller{}); err != nil {
		t.Fatalf("plan over MCP: %v", err)
	}
	// An address on the command line's place (the operation's input) doesn't exist: only here or saved.
	if _, err := ops.Call(ctx, a, "database.plan", json.RawMessage(`{"to":"mongodb://127.0.0.1:27018/"}`), ops.Caller{Human: true}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("an address as input: %v", err)
	}
	// Moving to the database on this computer while the laptop uses the
	// workspace: refused unless this computer will be the only one (the plan
	// sets nothing up).
	here := ops.DatabaseTargetIn{To: ops.MoveToHere, Name: "studio"}
	plan, err := ops.CallTyped[ops.DatabasePlanOut](ctx, a, "database.plan", ops.DatabasePlanIn{DatabaseTargetIn: here}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Ready || plan.To.Reach != "this-computer" || !strings.Contains(strings.Join(plan.Problems, " "), "Edit laptop") {
		t.Fatalf("here, others: %+v", plan)
	}
	here.OnlyThisComputer = true
	plan, _ = ops.CallTyped[ops.DatabasePlanOut](ctx, a, "database.plan", ops.DatabasePlanIn{DatabaseTargetIn: here}, ops.Caller{Human: true})
	if plan == nil || strings.Contains(strings.Join(plan.Problems, " "), "Edit laptop") {
		t.Fatalf("here, only this computer: %+v", plan)
	}
	if _, err := os.Stat(filepath.Join(a.Cfg.Home(), "database")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the plan set up a database")
	}
}
