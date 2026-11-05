package store

import (
	"context"
	"fmt"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/secretfields"
)

// scrubBatch is how many activity log entries migration 71 reads at a time.
var scrubBatch = int64(500)

// scrubLogSecretFields (migration 71) takes out of the activity log what older
// versions wrote there as typed: the values of operations' input fields that
// hold a secret though their names don't say so (internal/secretfields:
// account.password's current and new password). Each such entry gets the
// marker in their place, and the values are taken out of its summary, error
// and command line too; nothing else of it changes. Entries are read in
// batches by _id and each is changed only while it is still as read, so a
// second run (or one racing it) finds nothing left to change.
func scrubLogSecretFields(ctx context.Context, d *DB) error {
	all := secretfields.All()
	if len(all) == 0 {
		return nil
	}
	ops := make([]string, 0, len(all))
	held := bson.A{}
	seen := map[string]bool{}
	for op, fields := range all {
		ops = append(ops, op)
		for _, f := range fields {
			if !seen[f] {
				seen[f] = true
				held = append(held, bson.M{"op.input." + f: bson.M{"$exists": true, "$nin": bson.A{nil, "", secretfields.Hidden}}})
			}
		}
	}
	sort.Strings(ops)
	after := ""
	for {
		filter := bson.M{"kind": "op", "op.name": bson.M{"$in": ops}, "$or": held}
		if after != "" {
			filter["_id"] = bson.M{"$gt": after}
		}
		cur, err := d.C(CLogEntries).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(scrubBatch).
			SetProjection(bson.M{"summary": 1, "op.name": 1, "op.input": 1, "op.command": 1, "error.message": 1, "error.hint": 1, "error.advice": 1}))
		if err != nil {
			return fmt.Errorf("activity log: %w", err)
		}
		var rows []loggedCall
		if err := cur.All(ctx, &rows); err != nil {
			return fmt.Errorf("activity log: %w", err)
		}
		for i := range rows {
			if err := rows[i].scrub(ctx, d, all[rows[i].Op.Name]); err != nil {
				return fmt.Errorf("activity log: %w", err)
			}
		}
		if int64(len(rows)) < scrubBatch {
			return nil
		}
		after = rows[len(rows)-1].ID
	}
}

// loggedCall is what migration 71 reads of an operation's log entry.
type loggedCall struct {
	ID      string `bson:"_id"`
	Summary string `bson:"summary"`
	Op      struct {
		Name    string         `bson:"name"`
		Input   map[string]any `bson:"input"`
		Command string         `bson:"command"`
	} `bson:"op"`
	Error *struct {
		Message string `bson:"message"`
		Hint    string `bson:"hint"`
		Advice  string `bson:"advice"`
	} `bson:"error"`
}

// scrub writes a call's entry back without its secret fields' values, only
// while the entry is still as it was read.
func (c *loggedCall) scrub(ctx context.Context, d *DB, fields []string) error {
	was := map[string]any{}
	for _, f := range fields {
		if v, ok := c.Op.Input[f]; ok {
			was[f] = v
		}
	}
	texts := map[string]*string{"summary": &c.Summary, "op.command": &c.Op.Command}
	if c.Error != nil {
		texts["error.message"], texts["error.hint"], texts["error.advice"] = &c.Error.Message, &c.Error.Hint, &c.Error.Advice
	}
	before := make(map[string]string, len(texts))
	ptrs := make([]*string, 0, len(texts))
	for path, t := range texts {
		before[path] = *t
		ptrs = append(ptrs, t)
	}
	if !secretfields.Scrub(c.Op.Name, c.Op.Input, ptrs...) {
		return nil
	}
	match, set := bson.M{"_id": c.ID}, bson.M{}
	for f, v := range was {
		if s, _ := v.(string); v == nil || s == secretfields.Hidden {
			continue
		}
		if s, _ := c.Op.Input[f].(string); s == secretfields.Hidden {
			match["op.input."+f], set["op.input."+f] = v, secretfields.Hidden
		}
	}
	for path, t := range texts {
		if *t != before[path] {
			match[path], set[path] = before[path], *t
		}
	}
	if len(set) == 0 {
		return nil
	}
	_, err := d.C(CLogEntries).UpdateOne(ctx, match, bson.M{"$set": set})
	return err
}
