package visual

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/store"
)

// ToMap converts an index for storage in an Insight document.
func ToMap(idx *Index) map[string]any {
	b, err := bson.Marshal(idx)
	if err != nil {
		return map[string]any{}
	}
	var m bson.M
	if err := bson.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// Stored is a clip's index with when it was made.
type Stored struct {
	AssetID   string
	CreatedAt time.Time
	Model     string
	Index     *Index
}

// LoadMany reads the latest index of each asset (assets without one are absent).
func LoadMany(ctx context.Context, db *store.DB, workspaceID string, assetIDs []string, sel ...store.InsightMedia) map[string]*Stored {
	out := map[string]*Stored{}
	if db == nil || len(assetIDs) == 0 {
		return out
	}
	var docs []struct {
		AssetID   string    `bson:"assetId"`
		Model     string    `bson:"model"`
		CreatedAt time.Time `bson:"createdAt"`
		Data      Index     `bson:"data"`
	}
	f := store.MediaFilter(assetIDs, db.MediaFor(ctx, workspaceID, assetIDs, sel))
	f["kind"] = InsightKind
	if err := db.Find(ctx, store.CInsights, workspaceID, f, &docs, options.Find().SetSort(store.MediaSort)); err != nil {
		return out
	}
	for i := range docs {
		d := &docs[i]
		out[d.AssetID] = &Stored{AssetID: d.AssetID, CreatedAt: d.CreatedAt, Model: d.Model, Index: &d.Data}
	}
	return out
}

// Load reads one asset's latest index (nil when it has none).
func Load(ctx context.Context, db *store.DB, workspaceID, assetID string) *Stored {
	return LoadMany(ctx, db, workspaceID, []string{assetID})[assetID]
}

// LoadFaces reads only where the faces are in each asset's latest index, and
// where the eye is drawn and the footage's cuts (planning: framing that keeps
// faces in frame or follows movement).
func LoadFaces(ctx context.Context, db *store.DB, workspaceID string, assetIDs []string, sel ...store.InsightMedia) map[string]*Index {
	out := map[string]*Index{}
	if db == nil || len(assetIDs) == 0 {
		return out
	}
	var docs []struct {
		AssetID string `bson:"assetId"`
		Data    Index  `bson:"data"`
	}
	f := store.MediaFilter(assetIDs, db.MediaFor(ctx, workspaceID, assetIDs, sel))
	f["kind"] = InsightKind
	if err := db.Find(ctx, store.CInsights, workspaceID, f, &docs,
		options.Find().SetSort(store.MediaSort).SetProjection(bson.M{"assetId": 1, "media": 1, "data.faces": 1, "data.duration": 1, "data.cuts": 1, "data.attention": 1})); err != nil {
		return out
	}
	for i := range docs {
		out[docs[i].AssetID] = &docs[i].Data
	}
	return out
}
