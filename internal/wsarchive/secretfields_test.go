package wsarchive

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// An archive of a database not brought up to date yet (migration 71) takes
// the passwords an older version wrote into an operation's log entry out the
// same way: the fields in place, their values out of the summary and error,
// nothing else changed; other entries pass as they are.
func TestArchivedLogEntriesLoseTheirPasswords(t *testing.T) {
	const current, next, hidden = "tomato garden at dusk", "rosemary under the window", "[redacted]"
	entry := func(op, cur, nw, summary, msg string) bson.D {
		return bson.D{{Key: "_id", Value: "log_1"}, {Key: "kind", Value: "op"}, {Key: "summary", Value: summary},
			{Key: "error", Value: bson.D{{Key: "code", Value: "E_INVALID_INPUT"}, {Key: "message", Value: msg}}},
			{Key: "op", Value: bson.D{{Key: "name", Value: op}, {Key: "ok", Value: false},
				{Key: "input", Value: bson.D{{Key: "current", Value: cur}, {Key: "new", Value: nw}, {Key: "note", Value: "kept"}}}}}}
	}
	got, changed := cleanSecretFields(entry("account.password", current, next, "account.password failed: "+next+" is too common", next+" is too common"), time.Now())
	want := entry("account.password", hidden, hidden, "account.password failed: "+hidden+" is too common", hidden+" is too common")
	g, _ := bson.Marshal(got)
	w, _ := bson.Marshal(want)
	if !changed || bson.Raw(g).String() != bson.Raw(w).String() {
		t.Fatalf("changed %v:\n got %s\nwant %s", changed, bson.Raw(g), bson.Raw(w))
	}
	for _, d := range []bson.D{
		want, // already clean
		entry("transcript.fix", current, next, "transcript.fix", "x"), // another operation's fields of the same names
		{{Key: "_id", Value: "log_2"}, {Key: "kind", Value: "event"}, {Key: "summary", Value: "account.password " + next}},
	} {
		before, _ := bson.Marshal(d)
		out, changed := cleanSecretFields(d, time.Now())
		after, _ := bson.Marshal(out)
		if changed || bson.Raw(before).String() != bson.Raw(after).String() {
			t.Fatalf("changed %s", bson.Raw(before))
		}
	}
}
