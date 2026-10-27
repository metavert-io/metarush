package calls

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/visual"
)

// ToMap is a footage's switches as an insight's data.
func (st *Stored) ToMap() map[string]any {
	b, err := bson.Marshal(st)
	if err != nil {
		return map[string]any{}
	}
	var m map[string]any
	if err := bson.Unmarshal(b, &m); err != nil {
		return map[string]any{}
	}
	return m
}

// Save stores a footage's switches, replacing the ones made before.
func Save(ctx context.Context, db *store.DB, workspaceID, assetID string, st *Stored, model string) (string, error) {
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: workspaceID, AssetID: assetID, Kind: InsightKind, Provider: "ffmpeg", Model: model,
		PromptVersion: Version, Data: st.ToMap(), CreatedAt: time.Now().UTC()}
	if _, err := db.C(store.CInsights).DeleteMany(ctx, bson.M{"workspaceId": workspaceID, "assetId": assetID, "kind": InsightKind}); err != nil {
		return "", err
	}
	if err := db.Insert(ctx, store.CInsights, ins); err != nil {
		return "", err
	}
	return ins.ID, nil
}

// LoadMany reads the switches of assets (assets without them are absent).
func LoadMany(ctx context.Context, db *store.DB, workspaceID string, assetIDs []string) map[string]*Stored {
	out := map[string]*Stored{}
	if db == nil || len(assetIDs) == 0 {
		return out
	}
	var docs []struct {
		AssetID string `bson:"assetId"`
		Data    Stored `bson:"data"`
	}
	if err := db.Find(ctx, store.CInsights, workspaceID, bson.M{"assetId": bson.M{"$in": assetIDs}, "kind": InsightKind}, &docs,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}})); err != nil {
		return out
	}
	for i := range docs {
		out[docs[i].AssetID] = &docs[i].Data
	}
	return out
}

// Load reads one asset's switches (nil when it has none).
func Load(ctx context.Context, db *store.DB, workspaceID, assetID string) *Stored {
	return LoadMany(ctx, db, workspaceID, []string{assetID})[assetID]
}

// faceName is a face's name and whether a person confirmed it.
type faceName struct {
	name      string
	confirmed bool
}

// faceNames reads every face's name per asset and track: the confirmed name,
// else the one recognized by looks.
func faceNames(ctx context.Context, db *store.DB, workspaceID string, assetIDs []string) map[string]map[int]faceName {
	out := map[string]map[int]faceName{}
	var fs []models.FaceTrack
	if err := db.Find(ctx, store.CFaces, workspaceID, bson.M{"assetId": bson.M{"$in": assetIDs}}, &fs,
		options.Find().SetProjection(bson.M{"assetId": 1, "track": 1, "name": 1, "recognized": 1})); err != nil {
		return out
	}
	for _, f := range fs {
		n := faceName{name: f.Name, confirmed: f.Name != ""}
		if n.name == "" && f.Recognized != nil {
			n.name = f.Recognized.Name
		}
		if n.name == "" {
			continue
		}
		if out[f.AssetID] == nil {
			out[f.AssetID] = map[int]faceName{}
		}
		out[f.AssetID][f.Track] = n
	}
	return out
}

// PeopleNames lists the names of the workspace's people.
func PeopleNames(ctx context.Context, db *store.DB, workspaceID string) []string {
	var ps []struct {
		Name       string `bson:"name"`
		MergedInto string `bson:"mergedInto"`
	}
	_ = db.Find(ctx, store.CPeople, workspaceID, bson.M{}, &ps, options.Find().SetProjection(bson.M{"name": 1, "mergedInto": 1}))
	var out []string
	for _, p := range ps {
		if p.Name != "" && p.MergedInto == "" {
			out = append(out, p.Name)
		}
	}
	return out
}

// Footage builds what editspec knows about assets' picture switches (D160):
// the switches, who is on screen in each shot, and the printed names. tx gives
// an asset's transcript (who talks); assets without switches are absent.
func Footage(ctx context.Context, db *store.DB, workspaceID string, assetIDs []string, tx func(string) []editspec.TranscriptSegment) map[string]*editspec.FootageShots {
	out := map[string]*editspec.FootageShots{}
	sw := LoadMany(ctx, db, workspaceID, assetIDs)
	if len(sw) == 0 {
		return out
	}
	var with []string
	for id := range sw {
		with = append(with, id)
	}
	idx := visual.LoadMany(ctx, db, workspaceID, with)
	names := faceNames(ctx, db, workspaceID, with)
	people := PeopleNames(ctx, db, workspaceID)
	for id, st := range sw {
		var ix *visual.Index
		if s := idx[id]; s != nil {
			ix = s.Index
		}
		var segs []editspec.TranscriptSegment
		if tx != nil {
			segs = tx(id)
		}
		fn := names[id]
		out[id] = FootageOf(st, ix, func(track int) (string, bool) { return fn[track].name, fn[track].confirmed }, segs, people)
	}
	return out
}

// FootageOf is one asset's editspec.FootageShots from its switches and index.
func FootageOf(st *Stored, ix *visual.Index, names FaceNames, segs []editspec.TranscriptSegment, people []string) *editspec.FootageShots {
	fs := &editspec.FootageShots{Precision: st.Precision, Switches: make([]editspec.PictureSwitch, 0, len(st.Switches))}
	for _, s := range st.Switches {
		fs.Switches = append(fs.Switches, editspec.PictureSwitch{At: s.At, Last: s.Last})
	}
	fs.Shots = Shots(st, ix, names, segs, people, st.Duration)
	fs.Names = NameTiles(st, ix)
	return fs
}
