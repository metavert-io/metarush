package dbmove

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/store"
)

// movedWithPrints copies a workspace's database, takes the last look with
// fingerprints (as a move does right before it marks the old database moved)
// and returns the old database, the copy and the fingerprints' file.
func movedWithPrints(t *testing.T) (src, dst *store.DB, ws, prints string) {
	t.Helper()
	src, ws = workspaceDB(t)
	dst = emptyDB(t)
	ctx := context.Background()
	later := time.Now().UTC().Add(48 * time.Hour)
	if _, err := src.C(store.CAuthTokens).InsertOne(ctx, bson.M{"_id": "tok_live", "workspaceId": ws, "kind": "invite", "purgeAt": later}); err != nil {
		t.Fatal(err)
	}
	res, err := Copy(ctx, Options{From: src, To: dst, Workspace: ws, Move: "mvL"})
	if err != nil {
		t.Fatal(err)
	}
	prints = filepath.Join(t.TempDir(), "mvL.prints")
	f, err := os.OpenFile(prints, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecheckPrinting(ctx, src, dst, res, "mvL", f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return src, dst, ws, prints
}

func foundWords(r *LookResult) []string {
	var out []string
	for _, x := range r.Found {
		id := x.ID.String()
		if s, ok := x.ID.StringValueOK(); ok {
			id = s
		} else if n, ok := x.ID.Int32OK(); ok {
			id = fmt.Sprint(n)
		} else if x.ID.Type == bson.TypeObjectID {
			id = "objectid"
		}
		out = append(out, x.Collection+" "+id+" "+x.Kind)
	}
	sort.Strings(out)
	return out
}

// Right after the move nothing is new, changed or gone, and the server's own
// hashes say so without reading the records again (but the sign-in links',
// which end by themselves). Then records written straight into the old
// database — changed, new before, between and after the fingerprints' ranges,
// of another type of _id, gone, a kind made since, a sign-in link changed —
// are each found once, with the old database's version and what it was at the
// move; one ending by itself is never "gone". The looks never change the old
// database.
func TestLookFindsWhatWasWrittenLate(t *testing.T) {
	src, _, ws, prints := movedWithPrints(t)
	ctx := context.Background()
	old := printChunk
	printChunk = 100 // many ranges over the 1,203 projects
	t.Cleanup(func() { printChunk = old })

	h, err := ReadPrintsHeader(prints)
	if err != nil || h.Move != "mvL" || h.Database != src.Name || len(h.Kinds) == 0 || h.At.IsZero() {
		t.Fatalf("header: %+v %v", h, err)
	}
	r, err := Look(ctx, src, prints)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Found) != 0 || r.More != 0 {
		t.Fatalf("found right after the move: %v", foundWords(r))
	}
	if strings.Join(r.Read, ",") != store.CAuthTokens {
		t.Fatalf("read again with nothing changed: %v", r.Read)
	}

	must := func(_ any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	raw := func(coll string) *mongo.Collection { return src.DB.Collection(coll) } // past the freeze, as a computer that slept would
	must(raw(store.CProjects).UpdateOne(ctx, bson.M{"_id": "prj_00500"}, bson.M{"$set": bson.M{"name": "Episode 500, recut"}}))
	must(raw(store.CProjects).InsertOne(ctx, bson.M{"_id": "prj_00500a", "workspaceId": ws, "name": "Between"}))
	must(raw(store.CProjects).InsertOne(ctx, bson.M{"_id": "prj_", "workspaceId": ws, "name": "Before all"}))
	must(raw(store.CProjects).InsertOne(ctx, bson.M{"_id": "prj_99999", "workspaceId": ws, "name": "After all"}))
	must(raw(store.CProjects).InsertOne(ctx, bson.M{"_id": bson.NewObjectID(), "workspaceId": ws, "name": "Another kind of ID"}))
	must(raw(store.CProjects).DeleteOne(ctx, bson.M{"_id": "prj_01000"}))
	must(raw("studio_notes").InsertOne(ctx, bson.M{"_id": int32(3), "text": "a number before 7"}))
	must(raw("late_kind").InsertOne(ctx, bson.M{"_id": "lk1", "text": "a kind made since"}))
	must(raw(store.CAuthTokens).UpdateOne(ctx, bson.M{"_id": "tok_live"}, bson.M{"$set": bson.M{"kind": "reset"}}))
	before := dbHash(t, src)

	r, err = Look(ctx, src, prints)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"auth_tokens tok_live changed",
		"late_kind lk1 new",
		"projects objectid new",
		"projects prj_ new",
		"projects prj_00500 changed",
		"projects prj_00500a new",
		"projects prj_01000 gone",
		"projects prj_99999 new",
		"studio_notes 3 new",
	}
	if got := foundWords(r); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("found:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, x := range r.Found {
		switch x.Kind {
		case LateChanged:
			if x.Doc == nil || x.Print == "" || x.MovePrint == "" || x.Print == x.MovePrint {
				t.Fatalf("changed: %+v", x)
			}
			if p, _ := PrintOf(x.Doc); p != x.Print {
				t.Fatal("the fingerprint isn't the record's")
			}
		case LateNew:
			if x.Doc == nil || x.MovePrint != "" {
				t.Fatalf("new: %+v", x)
			}
		case LateGone:
			if x.Doc != nil || x.MovePrint == "" {
				t.Fatalf("gone: %+v", x)
			}
		}
	}
	// A sign-in link that ended (deleted by its time limit) is never "gone".
	must(raw(store.CAuthTokens).DeleteOne(ctx, bson.M{"_id": "tok_live"}))
	before = dbHash(t, src)
	r, err = Look(ctx, src, prints)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range foundWords(r) {
		if strings.HasPrefix(w, "auth_tokens") {
			t.Fatalf("an ended sign-in link reported: %s", w)
		}
	}
	if dbHash(t, src) != before {
		t.Fatal("a look changed the old database")
	}
	// Past MaxFound the rest are counted.
	oldMax := MaxFound
	MaxFound = 3
	t.Cleanup(func() { MaxFound = oldMax })
	r, err = Look(ctx, src, prints)
	if err != nil || len(r.Found) != 3 || r.More != 5 {
		t.Fatalf("over the limit: %d kept, %d more, %v", len(r.Found), r.More, err)
	}
}

// A file of fingerprints cut short is never trusted.
func TestLookRefusesPrintsCutShort(t *testing.T) {
	src, _, _, prints := movedWithPrints(t)
	b, err := os.ReadFile(prints)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prints, b[:len(b)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Look(context.Background(), src, prints); err == nil {
		t.Fatal("looked with fingerprints cut short")
	}
}
