package store

import (
	"context"
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Collections for the activity log and the improvement pipeline.
const (
	CLogEntries         = "log_entries"         // everything MetaRush did (internal/logbook)
	CImprovementItems   = "improvement_items"   // feature gaps and friction found in the log (internal/improve)
	CImprovementReports = "improvement_reports" // the reports they came from
)

// migrateLog adds the indexes for the activity log and the improvement items
// (migration 7, indexes only).
func migrateLog(ctx context.Context, d *DB) error {
	byTime := func(field string) mongo.IndexModel {
		return idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: field, Value: 1}, {Key: "at", Value: -1}})
	}
	steps := []struct {
		coll   string
		models []mongo.IndexModel
	}{
		// Few indexes: every operation writes an entry, so each index costs on
		// every write. Pages are read newest first from (workspaceId, at) with the
		// other filters applied on the way; only the selective lookups (problems,
		// a job, a conversation, an operation's children) get their own.
		{CLogEntries, []mongo.IndexModel{
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "at", Value: -1}}),
			byTime("level"), byTime("refs.jobId"), byTime("refs.conversationId"),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "parentId", Value: 1}}),
			// Word search over what people read: summaries, errors, prompts and responses.
			idx(bson.D{{Key: "summary", Value: "text"}, {Key: "error.message", Value: "text"}, {Key: "claude.prompt", Value: "text"},
				{Key: "claude.response", Value: "text"}, {Key: "claude.title", Value: "text"}, {Key: "tool.stderrTail", Value: "text"},
				{Key: "event.action", Value: "text"}, {Key: "op.name", Value: "text"}},
				options.Index().SetName("log_text").SetDefaultLanguage("none").
					SetWeights(bson.D{{Key: "summary", Value: 4}, {Key: "claude.prompt", Value: 3}, {Key: "error.message", Value: 3}, {Key: "claude.response", Value: 2}})),
		}},
		{CImprovementItems, []mongo.IndexModel{
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "key", Value: 1}}, options.Index().SetUnique(true)),
			idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "state", Value: 1}, {Key: "score", Value: -1}}),
		}},
		{CImprovementReports, []mongo.IndexModel{idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "createdAt", Value: -1}})}},
	}
	for _, s := range steps {
		if err := createIndexes(ctx, d, s.coll, s.models...); err != nil {
			return fmt.Errorf("%s indexes: %w", s.coll, err)
		}
	}
	return nil
}
