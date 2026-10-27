package wsarchive_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// A restore into a database elsewhere on the internet keeps the archive's
// clients' rules, as a move does: when a client's Content Settings keep
// their faces from cloud storage (the database holds their face prints), it
// is refused before any record goes in — the plan says so too — and the
// database holds nothing at all. A database on this computer is fine.
func TestRestoreKeepsTheArchivesClientsRules(t *testing.T) {
	ctx := context.Background()
	src := testutil.DB(t)
	st := fsStore(t)
	s := seed(t, src, st)
	if _, err := src.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": s.project}, bson.M{"$set": bson.M{"content": bson.M{"uses": "own"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := src.C(store.CContentSettings).InsertOne(ctx, bson.M{"_id": ids.New("cst"), "workspaceId": s.ws, "projectId": s.project,
		"leave": bson.M{"places": bson.M{"storage": bson.M{"keep": bson.A{"faces"}}}}}); err != nil {
		t.Fatal(err)
	}
	res := exportTo(t, src, st, s.ws, wsarchive.MediaReferenced, false)
	cloud := config.Database{URI: "mongodb+srv://cluster0.example.mongodb.net/", Name: "studio"}
	for _, plan := range []bool{true, false} {
		dst := emptyDB(t)
		dir := t.TempDir()
		_, err := wsarchive.Restore(ctx, wsarchive.RestoreOptions{Archive: res.Path, DB: dst, Store: st, StoreWords: "the test storage",
			StateDir: filepath.Join(dir, "state"), ScratchDir: filepath.Join(dir, "scratch"), Plan: plan,
			MayHold: func(ctx context.Context, a *wsarchive.Opened) error { return leave.ArchiveDatabaseCheck(cloud, a.EachDoc) }})
		if e := apperr.From(err); e == nil || e.Code != apperr.KeptHere || !strings.Contains(e.Message, "face prints") {
			t.Fatalf("restoring (plan %v) into a database in the cloud: %v", plan, err)
		}
		names, _ := dst.DB.ListCollectionNames(ctx, bson.M{})
		for _, n := range names {
			if n != store.CTestRun {
				t.Fatalf("the refused restore wrote %s", n)
			}
		}
	}
	dst := emptyDB(t)
	dir := t.TempDir()
	here := config.Database{URI: testutil.URI(), Name: dst.Name}
	if _, err := wsarchive.Restore(ctx, wsarchive.RestoreOptions{Archive: res.Path, DB: dst, Store: st, StoreWords: "the test storage",
		StateDir: filepath.Join(dir, "state"), ScratchDir: filepath.Join(dir, "scratch"),
		MayHold: func(ctx context.Context, a *wsarchive.Opened) error { return leave.ArchiveDatabaseCheck(here, a.EachDoc) }}); err != nil {
		t.Fatalf("restoring into a database on this computer: %v", err)
	}
}
