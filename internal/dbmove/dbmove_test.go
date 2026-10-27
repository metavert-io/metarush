package dbmove

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// emptyDB is a new, empty test database (only the test run's own marker),
// dropped after the test.
func emptyDB(t *testing.T) *store.DB {
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

// dbHash is MongoDB's own hash of a database's collections but some (the
// move's marker), so "untouched" is measured by the server itself.
func dbHash(t *testing.T, db *store.DB, except ...string) string {
	t.Helper()
	ctx := context.Background()
	names, err := db.DB.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	skip := map[string]bool{}
	for _, e := range except {
		skip[e] = true
	}
	var colls bson.A
	for _, n := range names {
		if !skip[n] {
			colls = append(colls, n)
		}
	}
	var h bson.M
	if err := db.DB.RunCommand(ctx, bson.D{{Key: "dbHash", Value: 1}, {Key: "collections", Value: colls}}).Decode(&h); err != nil {
		t.Fatal(err)
	}
	return h["md5"].(string)
}

// workspaceDB is a migrated test database holding a workspace worth moving:
// more projects than one batch, a password, an activity log, a kind of
// record MetaRush doesn't know (with an index of its own), and what a move
// leaves behind.
func workspaceDB(t *testing.T) (*store.DB, string) {
	t.Helper()
	db := testutil.DB(t)
	ctx := context.Background()
	ws := ids.New(ids.Workspace)
	now := time.Now().UTC().Truncate(time.Millisecond)
	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.C(store.CWorkspaces).InsertOne(ctx, bson.M{"_id": ws, "workspaceId": ws, "name": "FernBrook Studio", "createdAt": now}))
	must(db.C(store.CUsers).InsertOne(ctx, bson.M{"_id": ids.New(ids.User), "workspaceId": ws, "name": "Maya Okafor", "email": "maya@example.com", "role": "owner"}))
	must(db.C(store.CUserCredentials).InsertOne(ctx, bson.M{"_id": ids.New("ucr"), "workspaceId": ws, "hash": "$argon2id$v=19$m=65536,t=1,p=4$c2FsdA$aGFzaA"}))
	var projects []any
	for i := 0; i < 1203; i++ {
		projects = append(projects, bson.M{"_id": fmt.Sprintf("prj_%05d", i), "workspaceId": ws, "name": fmt.Sprintf("Episode %d", i), "n": int64(i), "at": now, "tags": bson.A{"call", i % 7}})
	}
	must(db.C(store.CProjects).InsertMany(ctx, projects))
	must(db.C(store.CLogEntries).InsertOne(ctx, bson.M{"_id": ids.New("log"), "workspaceId": ws, "summary": "Lena added a clip", "at": now}))
	must(db.C("studio_notes").InsertOne(ctx, bson.M{"_id": 7, "text": "Sam's note", "f": 1.5}))
	must(db.C("studio_notes").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "text", Value: 1}}, Options: options.Index().SetName("by_text")}))
	must(db.C(store.CWorkers).InsertOne(ctx, bson.M{"_id": "wrk", "workspaceId": ws}))
	must(db.C(store.CAuthFailures).InsertOne(ctx, bson.M{"_id": "f1", "n": 2}))
	return db, ws
}

// A copy holds exactly what the old database does — every kind of record,
// the unknown kind with its index, the password and the migration level —
// and nothing it leaves behind; it is locked until checked, then marked
// whole; the old database isn't changed at all.
func TestCopyIsWholeAndChecked(t *testing.T) {
	src, ws := workspaceDB(t)
	dst := emptyDB(t)
	ctx := context.Background()
	before := dbHash(t, src)
	var phases []string
	res, err := Copy(ctx, Options{From: src, To: dst, Workspace: ws, WorkspaceName: "FernBrook Studio", Move: "mv1",
		Progress: func(p CopyProgress) {
			if len(phases) == 0 || phases[len(phases)-1] != p.Phase {
				phases = append(phases, p.Phase)
			}
		}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Verified || res.Resumed {
		t.Fatalf("result: %+v", res)
	}
	if strings.Join(phases, ",") != "copying,checking" {
		t.Fatalf("phases: %v", phases)
	}
	if got := dbHash(t, src); got != before {
		t.Fatal("the old database changed")
	}
	lock, _ := dst.ReadRestoreLock(ctx)
	if lock == nil || lock.Kind != store.LockMove || lock.Archive != "mv1" || lock.State != store.RestoreDone || lock.Workspace != ws {
		t.Fatalf("lock: %+v", lock)
	}
	infos, _ := wsarchive.ListCollections(ctx, src)
	for _, info := range infos {
		if !Carried(info.Name) {
			if n, _ := dst.C(info.Name).CountDocuments(ctx, bson.M{}); n != 0 && info.Name != store.CTestRun {
				t.Errorf("%s was copied: %d", info.Name, n)
			}
			continue
		}
		a, _ := wsarchive.HashWhere(ctx, src, info.Name, nil)
		b, _ := wsarchive.HashWhere(ctx, dst, info.Name, nil)
		if a != b {
			t.Errorf("%s differs: %+v vs %+v", info.Name, a, b)
		}
	}
	if n, _ := dst.C(store.CUserCredentials).CountDocuments(ctx, bson.M{}); n != 1 {
		t.Fatal("the password didn't move with the workspace")
	}
	// The new database has MetaRush's own indexes, every one the old database
	// has, and none of a kind MetaRush doesn't know: that one is said.
	if ix, _ := wsarchive.ListIndexes(ctx, dst, "studio_notes"); len(ix) != 0 {
		t.Fatalf("the unknown kind's index was made: %v", ix)
	}
	if len(res.IndexesLeft) != 1 || res.IndexesLeft[0] != "studio_notes by_text" {
		t.Fatalf("indexes not made: %v", res.IndexesLeft)
	}
	lvlSrc, _ := wsarchive.ReadLevel(ctx, src)
	lvlDst, _ := wsarchive.ReadLevel(ctx, dst)
	if lvlSrc.Highest != lvlDst.Highest || len(lvlSrc.Applied) != len(lvlDst.Applied) || lvlDst.Highest == 0 {
		t.Fatalf("level: %+v vs %+v", lvlSrc, lvlDst)
	}
	// A database that holds something isn't copied into.
	if _, err := Copy(ctx, Options{From: src, To: src, Workspace: ws, Move: "mv2"}); err == nil {
		t.Fatal("copied into a database that isn't empty")
	}
}

// A copy that stopped part-way goes on where it stopped (the documents that
// are in stay, nothing is copied twice), and is checked whole.
func TestCopyContinuesAfterAStop(t *testing.T) {
	src, ws := workspaceDB(t)
	dst := emptyDB(t)
	ctx := context.Background()
	stopNow := false
	stopped := errors.New("stopped by the test")
	_, err := Copy(ctx, Options{From: src, To: dst, Workspace: ws, Move: "mv1",
		Progress: func(p CopyProgress) {
			if p.Collection == store.CProjects && p.DocumentsDone > 0 {
				if n, _ := dst.C(store.CProjects).CountDocuments(ctx, bson.M{}); n >= 500 {
					stopNow = true
				}
			}
		},
		Stop: func(context.Context) error {
			if stopNow {
				return stopped
			}
			return nil
		}})
	var p *Partial
	if !errors.As(err, &p) || !errors.Is(err, stopped) {
		t.Fatalf("stop: %v", err)
	}
	n, _ := dst.C(store.CProjects).CountDocuments(ctx, bson.M{})
	if n == 0 || n >= 1203 {
		t.Fatalf("part-way: %d projects", n)
	}
	if lock, _ := dst.ReadRestoreLock(ctx); !lock.Unfinished() {
		t.Fatalf("the half copy isn't locked: %+v", lock)
	}
	if err := dst.CheckNoUnfinishedRestore(ctx); err == nil || !strings.Contains(err.Error(), "move") {
		t.Fatalf("the half copy is usable: %v", err)
	}
	if tgt := CheckTarget(ctx, dst, "mv1"); !tgt.Resume || tgt.Problem != "" {
		t.Fatalf("own half copy: %+v", tgt)
	}
	if tgt := CheckTarget(ctx, dst, "someone-else"); tgt.Problem == "" || tgt.Empty {
		t.Fatalf("another move's half copy: %+v", tgt)
	}
	res, err := Copy(ctx, Options{From: src, To: dst, Workspace: ws, Move: "mv1"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Verified || !res.Resumed {
		t.Fatalf("result: %+v", res)
	}
	if n, _ := dst.C(store.CProjects).CountDocuments(ctx, bson.M{}); n != 1203 {
		t.Fatalf("projects: %d", n)
	}
	// Run once more after it finished: checked again, nothing added.
	if res, err := Copy(ctx, Options{From: src, To: dst, Workspace: ws, Move: "mv1"}); err != nil || !res.Verified {
		t.Fatalf("again: %v %+v", err, res)
	}
}

// Something written into the old database during the copy is noticed: the
// copy isn't marked whole and stays locked.
func TestCopyNoticesAChangedDatabase(t *testing.T) {
	src, ws := workspaceDB(t)
	dst := emptyDB(t)
	ctx := context.Background()
	wrote := false
	_, err := Copy(ctx, Options{From: src, To: dst, Workspace: ws, Move: "mv1",
		Progress: func(p CopyProgress) {
			if p.Phase == "copying" && p.Collection == "studio_notes" && !wrote {
				wrote = true // a computer that didn't pause changes a project already copied
				_, _ = src.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": "prj_00003"}, bson.M{"$set": bson.M{"name": "Renamed by Lena"}})
			}
		}})
	if !wrote {
		t.Fatal("the change wasn't made")
	}
	if !errors.Is(err, ErrChanged) {
		t.Fatalf("not noticed: %v", err)
	}
	if lock, _ := dst.ReadRestoreLock(ctx); lock == nil || lock.State == store.RestoreDone {
		t.Fatalf("a copy that differs was marked whole: %+v", lock)
	}
}

// The new database is checked without writing to it: empty and writable
// here (a test server signs nobody in), refused when it holds something.
func TestCheckTarget(t *testing.T) {
	dst := emptyDB(t)
	ctx := context.Background()
	before := dbHash(t, dst)
	tgt := CheckTarget(ctx, dst, "mv1")
	if !tgt.Reached || !tgt.Empty || !tgt.Writable || tgt.Problem != "" || tgt.Version == "" {
		t.Fatalf("empty: %+v", tgt)
	}
	if dbHash(t, dst) != before {
		t.Fatal("the check wrote into the new database")
	}
	if _, err := dst.C("projects").InsertOne(ctx, bson.M{"_id": "p"}); err != nil {
		t.Fatal(err)
	}
	if tgt := CheckTarget(ctx, dst, "mv1"); tgt.Empty || tgt.Problem == "" || len(tgt.Holds) != 1 {
		t.Fatalf("not empty: %+v", tgt)
	}
}

// What a plan reads: counts and sizes per kind of record, what stays behind
// and why, and a database of more than one workspace refused.
func TestSurvey(t *testing.T) {
	src, ws := workspaceDB(t)
	ctx := context.Background()
	s, err := SurveySource(ctx, src, ws)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]MoveCollection{}
	for _, c := range s.Collections {
		by[c.Name] = c
	}
	if p := by[store.CProjects]; p.Count != 1203 || !p.Carried || p.Bytes == 0 {
		t.Fatalf("projects: %+v", p)
	}
	if w := by[store.CWorkers]; w.Carried || w.Why == "" {
		t.Fatalf("live records: %+v", w)
	}
	if len(s.Problems) != 0 || s.Documents < 1203 || s.Level.Highest == 0 {
		t.Fatalf("survey: %+v", s)
	}
	if _, err := src.C(store.CWorkspaces).InsertOne(ctx, bson.M{"_id": "ws_other", "name": "Lena's own"}); err != nil {
		t.Fatal(err)
	}
	s, _ = SurveySource(ctx, src, ws)
	if len(s.Problems) != 1 || !strings.Contains(s.Problems[0], "Lena's own") {
		t.Fatalf("two workspaces: %v", s.Problems)
	}
}

// A kind of record made in the old database while it was copied (by a
// computer that didn't pause) stops the copy: it never says the copy is
// whole while the old database holds something the new one lacks.
func TestCopyNoticesALateCollection(t *testing.T) {
	src, ws := workspaceDB(t)
	dst := emptyDB(t)
	ctx := context.Background()
	calls := 0
	res, err := Copy(ctx, Options{From: src, To: dst, Workspace: ws, Move: "mvL",
		Stop: func(ctx context.Context) error {
			calls++
			if calls == 2 {
				_, _ = src.C("late_kind").InsertOne(ctx, bson.M{"_id": "x", "workspaceId": ws, "v": 1})
			}
			return nil
		}})
	if !errors.Is(err, ErrChanged) || (res != nil && res.Verified) {
		t.Fatalf("a collection made during the copy: err %v, verified %v", err, res != nil && res.Verified)
	}
	if lock, _ := dst.ReadRestoreLock(ctx); lock == nil || lock.State == store.RestoreDone {
		t.Fatalf("the copy was marked whole: %+v", lock)
	}
}

// A record written into the old database after its kind was checked is
// noticed by the last look, right before the old database is marked moved.
func TestRecheckNoticesAWriteAfterTheCheck(t *testing.T) {
	src, ws := workspaceDB(t)
	dst := emptyDB(t)
	ctx := context.Background()
	first, done := "", false
	res, err := Copy(ctx, Options{From: src, To: dst, Workspace: ws, Move: "mvH",
		Progress: func(p CopyProgress) {
			if p.Phase != "checking" {
				return
			}
			if p.CollectionsDone == 0 && p.Collection != "" {
				first = p.Collection
			}
			if p.CollectionsDone == 1 && !done && first != "" {
				done = true
				_, _ = src.C(first).InsertOne(ctx, bson.M{"_id": "zz_late", "workspaceId": ws})
			}
		}})
	if err != nil || !done {
		t.Fatalf("copy: %v (wrote: %v)", err, done)
	}
	if err := Recheck(ctx, src, res); !errors.Is(err, ErrChanged) {
		t.Fatalf("a write after %s was checked: %v", first, err)
	}
	// Nothing changed: the last look agrees.
	src2, ws2 := workspaceDB(t)
	dst2 := emptyDB(t)
	res2, err := Copy(ctx, Options{From: src2, To: dst2, Workspace: ws2, Move: "mvH2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := Recheck(ctx, src2, res2); err != nil {
		t.Fatalf("an unchanged database: %v", err)
	}
}
