package store

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/oldnames"
)

// migrateClearerNames renames, in place, the stored fields D210 took a word
// from (internal/oldnames): a fixed copy's fingerprint and fix steps, how a
// generated resource was made, and a Remotion refresh's steps. Only names
// change; every value stays as it was, so fingerprints, the fixed copies'
// IDs and every other hash are the same and every copy already made stays in
// use. A document that has both names got the old one from an older build
// later, so the old value wins ($rename's rule, and the readers'). Running it
// again finds nothing to do.
func migrateClearerNames(ctx context.Context, d *DB) error {
	renames := []struct{ coll, from, to string }{
		{CAssets, "fixes." + oldnames.Fingerprint, "fixes.fingerprint"},
		{CAssets, "fixes.copy." + oldnames.Fingerprint, "fixes.copy.fingerprint"},
		{CResources, "fixes." + oldnames.Fingerprint, "fixes.fingerprint"},
		{CResources, "fixes.copy." + oldnames.Fingerprint, "fixes.copy.fingerprint"},
		{CSourceCopies, oldnames.Fingerprint, "fingerprint"},
		{CSourceCopies, oldnames.FixSteps, "fixSteps"},
		{CRemotionRefreshes, oldnames.RefreshSteps, "refreshSteps"},
	}
	for _, r := range renames {
		if _, err := d.C(r.coll).UpdateMany(ctx, bson.M{r.from: bson.M{"$exists": true}}, bson.M{"$rename": bson.M{r.from: r.to}}); err != nil {
			return fmt.Errorf("%s: %s → %s: %w", r.coll, r.from, r.to, err)
		}
	}
	// A resource's versions are an array, which $rename can't reach: each
	// version whose source has the old name gets it under the new one.
	old := "$$v.source." + oldnames.MadeWith
	rest := bson.M{"$arrayToObject": bson.M{"$filter": bson.M{"input": bson.M{"$objectToArray": "$$v.source"}, "cond": bson.M{"$ne": bson.A{"$$this.k", oldnames.MadeWith}}}}}
	renamed := bson.M{"$mergeObjects": bson.A{"$$v", bson.M{"source": bson.M{"$mergeObjects": bson.A{rest, bson.M{"madeWith": old}}}}}}
	version := bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{bson.M{"$type": old}, "missing"}}, "$$v", renamed}}
	if _, err := d.C(CResources).UpdateMany(ctx, bson.M{"versions.source." + oldnames.MadeWith: bson.M{"$exists": true}},
		mongo.Pipeline{{{Key: "$set", Value: bson.M{"versions": bson.M{"$map": bson.M{"input": "$versions", "as": "v", "in": version}}}}}}); err != nil {
		return fmt.Errorf("%s: versions' sources: %w", CResources, err)
	}
	return nil
}
