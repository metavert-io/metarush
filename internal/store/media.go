package store

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Analysis per media (D152). A source's analysis (loudness, music beats,
// what's on screen) is stored with the media it was made from
// (footage_insights.media: a fixed copy's SHA-256, empty for the original),
// so a version that plays the original keeps reading the original's while a
// fixed copy's analysis arrives.

// InsightMedia picks, per asset, whose analysis to read: asset ID → the
// SHA-256 of the fixed copy, or "" for the original.
type InsightMedia map[string]string

// CurrentMedia is the media each asset uses now (its fixed copy, else the
// original). Assets that aren't found read the original.
func (d *DB) CurrentMedia(ctx context.Context, workspaceID string, assetIDs []string) InsightMedia {
	out := InsightMedia{}
	if d == nil || len(assetIDs) == 0 {
		return out
	}
	var docs []struct {
		ID    string `bson:"_id"`
		Fixes *struct {
			Copy *struct {
				SHA256 string `bson:"sha256"`
			} `bson:"copy"`
		} `bson:"fixes"`
	}
	if err := d.Find(ctx, CAssets, workspaceID, bson.M{"_id": bson.M{"$in": assetIDs}}, &docs, options.Find().SetProjection(bson.M{"fixes.copy.sha256": 1})); err != nil {
		return out
	}
	for _, x := range docs {
		if x.Fixes != nil && x.Fixes.Copy != nil {
			out[x.ID] = x.Fixes.Copy.SHA256
		}
	}
	return out
}

// MediaFilter is the query that picks each asset's insights of the chosen
// media (sel; missing assets read the original). Add the kind to it:
// f := MediaFilter(ids, sel); f["kind"] = …, and read with MediaSort, keeping
// the last document per asset: a fixed copy's analysis when it has one, else
// the original's (fixed copies keep the original's timing, so its music beats
// hold and its loudness and faces are close until the copy's own arrive). An
// asset that plays its original never reads a copy's.
func MediaFilter(assetIDs []string, sel InsightMedia) bson.M {
	or := bson.A{}
	var originals []string
	for _, id := range assetIDs {
		if m := sel[id]; m != "" {
			or = append(or, bson.M{"assetId": id, "media": bson.M{"$in": bson.A{m, nil, ""}}})
		} else {
			originals = append(originals, id)
		}
	}
	if len(originals) > 0 {
		or = append(or, bson.M{"assetId": bson.M{"$in": originals}, "media": bson.M{"$in": bson.A{nil, ""}}})
	}
	if len(or) == 0 {
		return bson.M{"assetId": bson.M{"$in": bson.A{}}}
	}
	return bson.M{"$or": or}
}

// MediaSort orders insights so that, read in order with the last one per
// asset kept, a fixed copy's latest wins over the original's (an empty or
// missing media sorts first), and within one media the latest wins.
var MediaSort = bson.D{{Key: "media", Value: 1}, {Key: "createdAt", Value: 1}}

// MediaFor is the selection a reader uses: the one it was given, else each
// asset's current media.
func (d *DB) MediaFor(ctx context.Context, workspaceID string, assetIDs []string, sel []InsightMedia) InsightMedia {
	if len(sel) > 0 && sel[0] != nil {
		return sel[0]
	}
	return d.CurrentMedia(ctx, workspaceID, assetIDs)
}

// SameMedia is the filter for an asset's insights of one media, for writers
// that replace what they made before (media "" is the original).
func SameMedia(assetID, media string) bson.M {
	if media == "" {
		return bson.M{"assetId": assetID, "media": bson.M{"$in": bson.A{nil, ""}}}
	}
	return bson.M{"assetId": assetID, "media": media}
}
