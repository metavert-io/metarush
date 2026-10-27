package evidence

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Music beats (internal/sound analyses them when music is added) are kept as
// an insight of the music file, so the preview and the render time music the
// same way (editspec.MusicFacts) without analysing it again.

// BeatsKind is the insight that keeps a music file's beats.
const BeatsKind = "beats"

// BeatsVersion is the analysis version (a new one is analysed again).
const BeatsVersion = "beats-1"

// SaveBeats records a music file's beats (seconds) and tempo.
func SaveBeats(ctx context.Context, db *store.DB, workspaceID, assetID, sha string, tempo, confidence float64, times []float64, onsets bool) error {
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: workspaceID, AssetID: assetID, Kind: BeatsKind, Provider: "metarush", Model: BeatsVersion,
		Digest: sha + ":" + BeatsVersion, CreatedAt: time.Now().UTC(),
		Data: map[string]any{"tempo": tempo, "confidence": confidence, "times": times, "onsets": onsets}}
	return db.Insert(ctx, store.CInsights, ins)
}

// BeatsOf is what was found in one file.
type BeatsOf struct {
	Tempo  float64
	Times  []float64
	Onsets bool
}

// GridVersion is the whole-track analysis (D120): beats at a changing tempo,
// bars and phrases. Kept as an insight of the same kind; LoadBeats never reads it.
const GridVersion = "beats-2"

// TempoSpan is a stretch of a track at one tempo, or changing smoothly.
type TempoSpan struct {
	From  float64 `bson:"from" json:"from"`
	To    float64 `bson:"to" json:"to"`
	Start float64 `bson:"start" json:"start"`
	End   float64 `bson:"end" json:"end"`
}

// GridOf is a music file's whole-track analysis.
type GridOf struct {
	Tempo      float64     `bson:"tempo"`
	Tempos     []TempoSpan `bson:"tempos,omitempty"`
	Confidence float64     `bson:"confidence"`
	Beats      []float64   `bson:"beats"`
	Onsets     bool        `bson:"onsets,omitempty"`
	Meter      int         `bson:"meter,omitempty"`
	Downbeats  []float64   `bson:"downbeats,omitempty"`
	Bars       float64     `bson:"bars,omitempty"`
	PhraseBars int         `bson:"phraseBars,omitempty"`
	Phrases    []float64   `bson:"phrases,omitempty"`
	PhraseConf float64     `bson:"phraseConfidence,omitempty"`
	Seconds    float64     `bson:"seconds"`
}

// SaveGrid records a music file's whole-track analysis.
func SaveGrid(ctx context.Context, db *store.DB, workspaceID, assetID, sha string, g GridOf) error {
	raw, err := bson.Marshal(g)
	if err != nil {
		return err
	}
	data := map[string]any{}
	if err := bson.Unmarshal(raw, &data); err != nil {
		return err
	}
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: workspaceID, AssetID: assetID, Kind: BeatsKind, Provider: "metarush", Model: GridVersion,
		Digest: sha + ":" + GridVersion, CreatedAt: time.Now().UTC(), Data: data}
	return db.Insert(ctx, store.CInsights, ins)
}

// LoadGrids reads the latest whole-track analysis of each asset that has one.
func LoadGrids(ctx context.Context, db *store.DB, workspaceID string, assetIDs []string, sel ...store.InsightMedia) map[string]GridOf {
	out := map[string]GridOf{}
	if db == nil || len(assetIDs) == 0 {
		return out
	}
	var docs []struct {
		AssetID string `bson:"assetId"`
		Data    GridOf `bson:"data"`
	}
	f := store.MediaFilter(assetIDs, db.MediaFor(ctx, workspaceID, assetIDs, sel))
	f["kind"], f["model"] = BeatsKind, GridVersion
	if err := db.Find(ctx, store.CInsights, workspaceID, f, &docs, options.Find().SetSort(store.MediaSort)); err != nil {
		return out
	}
	for _, d := range docs { // ascending: the latest wins
		out[d.AssetID] = d.Data
	}
	return out
}

// LoadBeats reads the latest beats of each asset that has them.
func LoadBeats(ctx context.Context, db *store.DB, workspaceID string, assetIDs []string, sel ...store.InsightMedia) map[string]BeatsOf {
	out := map[string]BeatsOf{}
	if db == nil || len(assetIDs) == 0 {
		return out
	}
	var docs []struct {
		AssetID string `bson:"assetId"`
		Model   string `bson:"model"`
		Data    struct {
			Tempo  float64   `bson:"tempo"`
			Times  []float64 `bson:"times"`
			Onsets bool      `bson:"onsets"`
		} `bson:"data"`
	}
	f := store.MediaFilter(assetIDs, db.MediaFor(ctx, workspaceID, assetIDs, sel))
	f["kind"] = BeatsKind
	if err := db.Find(ctx, store.CInsights, workspaceID, f, &docs, options.Find().SetSort(store.MediaSort)); err != nil {
		return out
	}
	for _, d := range docs { // ascending: the latest wins
		if d.Model != BeatsVersion {
			continue
		}
		out[d.AssetID] = BeatsOf{Tempo: d.Data.Tempo, Times: d.Data.Times, Onsets: d.Data.Onsets}
	}
	return out
}
