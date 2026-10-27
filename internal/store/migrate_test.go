package store_test

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// Builds merge in any order: a version below the highest recorded one that a
// database has not seen yet (here: one whose record is missing) still runs.
func TestMigrateAppliesEveryVersionNotRecorded(t *testing.T) {
	db := testutil.DB(t) // migrated to the latest version
	ctx := context.Background()
	if pending, err := db.PendingMigrations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("fresh database: pending %v %v", pending, err)
	}
	if _, err := db.C(store.CMigrations).DeleteOne(ctx, bson.M{"_id": 2}); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.AppliedVersion(ctx); v != store.SchemaVersion() {
		t.Fatalf("highest recorded version %d, want %d", v, store.SchemaVersion())
	}
	pending, err := db.PendingMigrations(ctx)
	if err != nil || len(pending) != 1 || pending[0] != 2 {
		t.Fatalf("pending %v %v", pending, err)
	}
	applied, err := db.Migrate(ctx)
	if err != nil || len(applied) != 1 || !strings.HasPrefix(applied[0], "2 ") {
		t.Fatalf("applied %v %v", applied, err)
	}
	if pending, _ := db.PendingMigrations(ctx); len(pending) != 0 {
		t.Fatalf("still pending: %v", pending)
	}
}

// What Auto spent is read from the generations its jobs made (ops.AutoSpent,
// D241): they are found by their job through an index, not by reading every
// generation of the workspace.
func TestGenerationsAreFoundByTheirJob(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	cur, err := db.C(store.CGenerations).Indexes().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ixs []struct {
		Key bson.D `bson:"key"`
	}
	if err := cur.All(ctx, &ixs); err != nil {
		t.Fatal(err)
	}
	for _, ix := range ixs {
		if len(ix.Key) == 2 && ix.Key[0].Key == "workspaceId" && ix.Key[1].Key == "jobId" {
			return
		}
	}
	t.Fatalf("no (workspaceId, jobId) index on generations: %+v", ixs)
}
