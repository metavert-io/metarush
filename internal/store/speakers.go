package store

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Collections for speaker identity and to-dos.
const (
	CSpeakers = "asset_speakers" // voices heard in each asset
	CPeople   = "people"         // named people and their voice profiles
	CTodos    = "todos"          // open questions for people
	CFaces    = "faces"          // faces followed through clips by the on-screen index (D73)
)

// migrateSpeakersTodos adds the indexes for asset speakers, people and to-dos.
// One open to-do per group key is enforced by a partial unique index, so
// concurrent workers can create to-dos idempotently.
func migrateSpeakersTodos(ctx context.Context, d *DB) error {
	steps := []struct {
		coll   string
		models []mongo.IndexModel
	}{
		{CSpeakers, []mongo.IndexModel{
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}, {Key: "label", Value: 1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "personId", Value: 1}}),
		}},
		{CPeople, []mongo.IndexModel{idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "nameKey", Value: 1}})}},
		{CTodos, []mongo.IndexModel{
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "groupKey", Value: 1}},
				options.Index().SetUnique(true).SetPartialFilterExpression(bson.M{"state": "open"}).SetName("open_group_unique")),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}, {Key: "priority", Value: 1}, {Key: "createdAt", Value: -1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "assetId", Value: 1}, {Key: "state", Value: 1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectIds", Value: 1}, {Key: "state", Value: 1}}),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "groupKey", Value: 1}, {Key: "state", Value: 1}}),
		}},
	}
	for _, s := range steps {
		if err := createIndexes(ctx, d, s.coll, s.models...); err != nil {
			return fmt.Errorf("%s indexes: %w", s.coll, err)
		}
	}
	return nil
}
