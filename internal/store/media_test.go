package store_test

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// Analysis per media (D152): a reader of a fixed copy gets the copy's own
// analysis when there is one, else the original's; a reader of the original
// never gets a copy's.
func TestMediaFilterPicksTheMediaAsked(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	ws := "ws_test"
	t0 := time.Now().UTC()
	docs := []any{
		bson.M{"_id": "i1", "workspaceId": ws, "assetId": "A", "kind": "loudness", "value": "A original", "createdAt": t0},
		bson.M{"_id": "i2", "workspaceId": ws, "assetId": "A", "kind": "loudness", "media": "copyA", "value": "A copy", "createdAt": t0.Add(-time.Hour)},
		bson.M{"_id": "i3", "workspaceId": ws, "assetId": "B", "kind": "loudness", "media": "", "value": "B original", "createdAt": t0},
		bson.M{"_id": "i4", "workspaceId": ws, "assetId": "B", "kind": "loudness", "media": "copyB", "value": "B copy", "createdAt": t0},
		bson.M{"_id": "i5", "workspaceId": ws, "assetId": "C", "kind": "loudness", "value": "C original", "createdAt": t0},
	}
	if _, err := db.C(store.CInsights).InsertMany(ctx, docs); err != nil {
		t.Fatal(err)
	}
	read := func(sel store.InsightMedia) map[string]string {
		t.Helper()
		f := store.MediaFilter([]string{"A", "B", "C"}, sel)
		f["kind"] = "loudness"
		var rows []struct {
			AssetID string `bson:"assetId"`
			Value   string `bson:"value"`
		}
		if err := db.Find(ctx, store.CInsights, ws, f, &rows, options.Find().SetSort(store.MediaSort)); err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, r := range rows {
			out[r.AssetID] = r.Value // the last one per asset wins
		}
		return out
	}
	got := read(store.InsightMedia{"A": "copyA", "C": "copyC"})
	if got["A"] != "A copy" || got["B"] != "B original" || got["C"] != "C original" {
		t.Fatalf("A's copy (older than its original's, still wins), B's original, C's original while its copy has none: %v", got)
	}
	if got := read(nil); got["A"] != "A original" || got["B"] != "B original" {
		t.Fatalf("originals never read a copy's: %v", got)
	}
	n, err := db.C(store.CInsights).CountDocuments(ctx, bson.M{"workspaceId": ws, "$and": bson.A{store.SameMedia("B", "")}})
	if err != nil || n != 1 {
		t.Fatalf("same media: %d %v", n, err)
	}
}
