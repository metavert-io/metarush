package store_test

import (
	"context"
	"fmt"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// Migration 71 takes the passwords older versions wrote into the activity log
// out of it: an operation's secret fields (internal/secretfields) in its
// entries' input get the marker in place, and their values leave the entry's
// summary and error; nothing else of any entry changes — not entries already
// clean, not another operation's fields of the same names, not other kinds of
// entry. It reads in batches, and running it again changes nothing.
func TestOlderLogEntriesLoseTheirPasswords(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	old := *store.LogScrubBatch
	*store.LogScrubBatch = 2
	t.Cleanup(func() { *store.LogScrubBatch = old })
	const ws, current, next, hidden = "ws_logsecrets", "tomato garden at dusk", "rosemary under the window", "[redacted]"
	opEntry := func(id, op string, input bson.D, summary string, errMsg string) bson.D {
		d := bson.D{{Key: "_id", Value: id}, {Key: "workspaceId", Value: ws}, {Key: "kind", Value: "op"}, {Key: "level", Value: "info"}, {Key: "summary", Value: summary}}
		if errMsg != "" {
			d = append(d, bson.E{Key: "error", Value: bson.D{{Key: "code", Value: "E_INVALID_INPUT"}, {Key: "message", Value: errMsg}}})
		}
		return append(d, bson.E{Key: "op", Value: bson.D{{Key: "name", Value: op}, {Key: "mutating", Value: true}, {Key: "ok", Value: errMsg == ""},
			{Key: "input", Value: input}, {Key: "command", Value: "metarush account password"}}})
	}
	written := []bson.D{
		opEntry("log_a1", "account.password", bson.D{{Key: "current", Value: current}, {Key: "new", Value: next}}, "account.password failed: "+next+" is too common", next+" is too common"),
		opEntry("log_a2", "account.password", bson.D{{Key: "new", Value: next}, {Key: "note", Value: "kept"}}, "account.password", ""),
		opEntry("log_clean", "account.password", bson.D{{Key: "current", Value: hidden}, {Key: "new", Value: hidden}}, "account.password", ""),
		opEntry("log_other", "transcript.fix", bson.D{{Key: "current", Value: current}, {Key: "new", Value: next}}, "transcript.fix", ""),
		{{Key: "_id", Value: "log_event"}, {Key: "workspaceId", Value: ws}, {Key: "kind", Value: "event"}, {Key: "summary", Value: "account.password " + next}},
	}
	for i := 0; i < 5; i++ { // more batches
		written = append(written, opEntry(fmt.Sprintf("log_b%d", i), "account.password", bson.D{{Key: "current", Value: current}}, "account.password", ""))
	}
	for _, d := range written {
		if _, err := db.C(store.CLogEntries).InsertOne(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	// What each entry must read afterwards: only the secret fields' values changed, in place.
	want := map[string]bson.D{
		"log_a1":    opEntry("log_a1", "account.password", bson.D{{Key: "current", Value: hidden}, {Key: "new", Value: hidden}}, "account.password failed: "+hidden+" is too common", hidden+" is too common"),
		"log_a2":    opEntry("log_a2", "account.password", bson.D{{Key: "new", Value: hidden}, {Key: "note", Value: "kept"}}, "account.password", ""),
		"log_clean": written[2],
		"log_other": written[3],
		"log_event": written[4],
	}
	for i := 0; i < 5; i++ {
		want[fmt.Sprintf("log_b%d", i)] = opEntry(fmt.Sprintf("log_b%d", i), "account.password", bson.D{{Key: "current", Value: hidden}}, "account.password", "")
	}
	check := func(round string) {
		t.Helper()
		for id, d := range want {
			var got bson.Raw
			if err := db.C(store.CLogEntries).FindOne(ctx, bson.M{"_id": id}).Decode(&got); err != nil {
				t.Fatal(err)
			}
			b, _ := bson.Marshal(d)
			if got.String() != bson.Raw(b).String() {
				t.Errorf("%s, %s:\n got %s\nwant %s", round, id, got, bson.Raw(b))
			}
		}
	}
	for _, round := range []string{"the migration", "it again"} {
		if _, err := db.C(store.CMigrations).DeleteOne(ctx, bson.M{"_id": 71}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		check(round)
	}
}
