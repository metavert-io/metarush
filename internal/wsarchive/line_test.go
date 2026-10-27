package wsarchive

import (
	"bytes"
	"math"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Every BSON type MetaRush could hold goes through a line and back byte for
// byte: the archive is exact, and a copy's hash equals the file's.
func TestLinesRoundTripEveryType(t *testing.T) {
	oid := bson.NewObjectID()
	dec, _ := bson.ParseDecimal128("1234.5678")
	doc := bson.D{
		{Key: "_id", Value: "ast_01abc"},
		{Key: "workspaceId", Value: "ws_1"},
		{Key: "s", Value: "words with \"quotes\", a newline\nand ünïcödé ✓"},
		{Key: "i32", Value: int32(-7)},
		{Key: "i64", Value: int64(1) << 53},
		{Key: "f", Value: 0.1},
		{Key: "fneg0", Value: math.Copysign(0, -1)},
		{Key: "fbig", Value: 1e300},
		{Key: "nan", Value: math.NaN()},
		{Key: "inf", Value: math.Inf(-1)},
		{Key: "dec", Value: dec},
		{Key: "b", Value: true},
		{Key: "null", Value: nil},
		{Key: "date", Value: bson.NewDateTimeFromTime(time.Date(2026, 10, 26, 12, 30, 1, 123e6, time.UTC))},
		{Key: "ts", Value: bson.Timestamp{T: 1793063362, I: 4}},
		{Key: "oid", Value: oid},
		{Key: "bin", Value: bson.Binary{Subtype: 0, Data: []byte{0, 1, 2, 255}}},
		{Key: "uuid", Value: bson.Binary{Subtype: 4, Data: bytes.Repeat([]byte{7}, 16)}},
		{Key: "re", Value: bson.Regex{Pattern: "^a.*", Options: "i"}},
		{Key: "arr", Value: bson.A{int32(1), "two", bson.D{{Key: "three", Value: 3.0}}, bson.A{}}},
		{Key: "nested", Value: bson.D{{Key: "z", Value: 1.5}, {Key: "a", Value: bson.D{}}}},
		{Key: "min", Value: bson.MinKey{}},
		{Key: "max", Value: bson.MaxKey{}},
	}
	raw, err := bson.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	line, err := Line(raw)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(line, []byte("\n")) != 1 || line[len(line)-1] != '\n' {
		t.Fatalf("a document is one line: %q", line)
	}
	back, err := FromLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, raw) {
		t.Fatalf("round trip changed the document:\n%s\n%s", raw, back)
	}
	again, _ := Line(back)
	if !bytes.Equal(again, line) {
		t.Fatalf("the line changed:\n%s\n%s", line, again)
	}
}

// The scrubber changes nothing in a document without credentials (the same
// bytes come out), and takes them out of every text where they are.
func TestScrubberKeepsCleanDocumentsExact(t *testing.T) {
	s := NewScrubber(nil)
	clean, _ := bson.Marshal(bson.D{{Key: "_id", Value: "cmt_1"}, {Key: "text", Value: "Cut the intro at 0:12"}, {Key: "n", Value: int32(3)},
		{Key: "words", Value: bson.A{bson.D{{Key: "w", Value: "hello"}}}}})
	out, changed, err := s.Doc(clean)
	if err != nil || changed != nil || !bytes.Equal(out, clean) {
		t.Fatalf("a clean document changed: %v %v", changed, err)
	}
	key := "sk-" + "ant01234567890abcdefghijklmnop" // secret-scan: allow (a made-up key)
	dirty, _ := bson.Marshal(bson.D{{Key: "_id", Value: "cmt_2"}, {Key: "text", Value: "my key is " + key},
		{Key: "turns", Value: bson.A{bson.D{{Key: "prompt", Value: "use mongodb://maya:hunter2pass@db.example.com/x"}}}},
		{Key: "n", Value: int32(3)}})
	out, changed, err = s.Doc(dirty)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 2 || changed[0] != "text" || changed[1] != "turns.0.prompt" {
		t.Fatalf("changed fields: %v", changed)
	}
	if bytes.Contains(out, []byte(key)) || bytes.Contains(out, []byte("hunter2pass")) {
		t.Fatal("a credential is still there")
	}
	var d bson.D
	if err := bson.Unmarshal(out, &d); err != nil || len(d) != 4 || d[3].Value.(int32) != 3 {
		t.Fatalf("the rest of the document changed: %v %v", d, err)
	}
}
