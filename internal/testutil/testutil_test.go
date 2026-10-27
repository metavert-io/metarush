package testutil

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/store"
)

// A database made from the template is the same as one migrated the usual
// way: the same collections with the same options, the same indexes (keys,
// names and every option) and the same documents (D194).
func TestTemplateCopiesMatchAFreshMigration(t *testing.T) {
	ctx := context.Background()
	t.Setenv(FreshEnv, "1")
	start := time.Now()
	fresh := DB(t)
	tookFresh := time.Since(start)
	t.Setenv(FreshEnv, "")

	// DB itself: whichever database of this process came first is the
	// template; a second one is always a copy.
	_ = DB(t)
	start = time.Now()
	copied := DB(t)
	tookCopy := time.Since(start)
	t.Logf("a fresh migration took %v, a copy of the template %v", tookFresh.Round(time.Millisecond), tookCopy.Round(time.Millisecond))
	if tplErr != nil {
		t.Fatalf("no template: %v", tplErr)
	}
	same(t, ctx, fresh, copied, false)

	// And a template read from the fresh one copies it exactly, documents too.
	tp, err := ReadTemplate(ctx, fresh)
	if err != nil {
		t.Fatal(err)
	}
	empty := emptyDB(t)
	if err := tp.CopyInto(ctx, empty); err != nil {
		t.Fatal(err)
	}
	same(t, ctx, fresh, empty, true)

	// Nothing is left for Migrate to do on a copy.
	if pending, err := copied.PendingMigrations(ctx); err != nil || len(pending) != 0 {
		t.Fatalf("a copy has migrations pending: %v %v", pending, err)
	}
	if applied, err := copied.Migrate(ctx); err != nil || len(applied) != 0 {
		t.Fatalf("migrating a copy applied %v %v", applied, err)
	}
}

// A template copies options, views and documents it does not know about.
func TestTemplatesCopyOptionsViewsAndDocuments(t *testing.T) {
	ctx := context.Background()
	src := emptyDB(t)
	cmd := bson.D{{Key: "create", Value: "checked"}, {Key: "validator", Value: bson.M{"n": bson.M{"$type": "int"}}}}
	if err := src.DB.RunCommand(ctx, cmd).Err(); err != nil {
		t.Fatal(err)
	}
	if err := src.DB.RunCommand(ctx, bson.D{{Key: "create", Value: "capped"}, {Key: "capped", Value: true}, {Key: "size", Value: 4096}}).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := src.C("checked").InsertOne(ctx, bson.M{"_id": "a", "n": 1}); err != nil {
		t.Fatal(err)
	}
	if err := src.DB.CreateView(ctx, "ones", "checked", bson.A{bson.M{"$match": bson.M{"n": 1}}}); err != nil {
		t.Fatal(err)
	}
	if err := src.DB.RunCommand(ctx, bson.D{{Key: "createIndexes", Value: "words"}, {Key: "indexes", Value: bson.A{
		bson.M{"key": bson.M{"text": "text"}, "name": "words_text", "default_language": "none", "weights": bson.M{"text": 3}},
		bson.M{"key": bson.M{"at": 1}, "name": "at_ttl", "expireAfterSeconds": 60},
		bson.M{"key": bson.M{"k": 1}, "name": "k_partial", "unique": true, "partialFilterExpression": bson.M{"k": bson.M{"$exists": true}}},
	}}}).Err(); err != nil {
		t.Fatal(err)
	}
	tp, err := ReadTemplate(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	dst := emptyDB(t)
	if err := tp.CopyInto(ctx, dst); err != nil {
		t.Fatal(err)
	}
	same(t, ctx, src, dst, true)
	if _, err := dst.C("checked").InsertOne(ctx, bson.M{"n": "not a number"}); err == nil {
		t.Fatal("the copy lost the validator")
	}
}

// emptyDB is a marked test database with nothing in it, dropped afterwards.
func emptyDB(t *testing.T) *store.DB {
	t.Helper()
	ctx := context.Background()
	db, err := store.Connect(ctx, URI(), store.TestDBPrefix+strings.TrimPrefix(ids.New("t"), "t_"))
	if err != nil {
		t.Skipf("test MongoDB unavailable: %v", err)
	}
	if err := db.MarkTestRun(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.DropTestDatabase(context.Background(), URI())
		_ = db.Close(context.Background())
	})
	return db
}

// same fails the test where two databases differ in collections, options,
// indexes or (with docs) documents. The test marker is left out.
func same(t *testing.T, ctx context.Context, a, b *store.DB, docs bool) {
	t.Helper()
	pa, pb := picture(t, ctx, a, docs), picture(t, ctx, b, docs)
	if len(pa) != len(pb) {
		t.Errorf("%d collections, the copy has %d", len(pa), len(pb))
	}
	for name, want := range pa {
		got, ok := pb[name]
		switch {
		case !ok:
			t.Errorf("the copy lacks %s", name)
		case got != want:
			t.Errorf("%s differs:\n want %s\n  got %s", name, want, got)
		}
	}
	for name := range pb {
		if _, ok := pa[name]; !ok {
			t.Errorf("the copy has %s, which the original lacks", name)
		}
	}
	if len(pa) < 2 {
		t.Fatalf("hardly anything to compare: %v", pa)
	}
}

// picture describes each collection as text: type, options, indexes by name
// and (with docs) documents by _id.
func picture(t *testing.T, ctx context.Context, db *store.DB, docs bool) map[string]string {
	t.Helper()
	cur, err := db.DB.ListCollections(ctx, bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	var specs []struct {
		Name    string   `bson:"name"`
		Type    string   `bson:"type"`
		Options bson.Raw `bson:"options"`
	}
	if err := cur.All(ctx, &specs); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, s := range specs {
		if s.Name == store.CTestRun || strings.HasPrefix(s.Name, "system.") {
			continue
		}
		var b bytes.Buffer
		fmt.Fprintf(&b, "%s %s", s.Type, s.Options)
		if s.Type != "view" {
			icur, err := db.C(s.Name).Indexes().List(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var ixs []bson.Raw
			if err := icur.All(ctx, &ixs); err != nil {
				t.Fatal(err)
			}
			sort.Slice(ixs, func(i, j int) bool {
				return ixs[i].Lookup("name").StringValue() < ixs[j].Lookup("name").StringValue()
			})
			for _, ix := range ixs {
				fmt.Fprintf(&b, "\n  index %s", ix)
			}
		}
		if docs && s.Type != "view" {
			dcur, err := db.C(s.Name).Find(ctx, bson.D{})
			if err != nil {
				t.Fatal(err)
			}
			var ds []bson.Raw
			if err := dcur.All(ctx, &ds); err != nil {
				t.Fatal(err)
			}
			sort.Slice(ds, func(i, j int) bool { return ds[i].Lookup("_id").String() < ds[j].Lookup("_id").String() })
			for _, d := range ds {
				fmt.Fprintf(&b, "\n  doc %s", d)
			}
		}
		out[s.Name] = b.String()
	}
	return out
}
