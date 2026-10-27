package wsarchive_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// rewriteFile changes a file of an archive folder and fixes the manifest's
// checksums to match (an archive made that way, not one damaged on the way).
func rewriteFile(t *testing.T, dir, rel string, change func([]byte) []byte) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	nb := change(b)
	if err := os.WriteFile(p, nb, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(nb)
	mp := filepath.Join(dir, wsarchive.ManifestName)
	var m map[string]any
	mb, _ := os.ReadFile(mp)
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatal(err)
	}
	for _, list := range []string{"files", "collections"} {
		for _, it := range m[list].([]any) {
			f := it.(map[string]any)
			if f["path"] == rel || f["file"] == rel {
				f["sha256"], f["bytes"] = hex.EncodeToString(sum[:]), len(nb)
				if c, ok := f["count"].(float64); ok {
					f["count"] = c + float64(bytes.Count(nb, []byte("\n"))-bytes.Count(b, []byte("\n")))
				}
			}
		}
	}
	out, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(mp, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

func restoreSeed(t *testing.T) (*store.DB, *wsarchive.Result, string) {
	t.Helper()
	src := testutil.DB(t)
	st := fsStore(t)
	s := seed(t, src, st)
	if _, err := src.C(store.CRecipes).InsertOne(context.Background(), bson.M{"_id": ids.New(ids.Recipe), "workspaceId": s.ws, "name": "Basic explainer"}); err != nil {
		t.Fatal(err)
	}
	return src, exportTo(t, src, st, s.ws, wsarchive.MediaReferenced, false), s.ws
}

// Something written into the database while the footage is put in place is
// never merged with the copy: the database is looked at again right before
// the records go in, and the restore stops.
func TestRestoreRefusesADatabaseWrittenMeanwhile(t *testing.T) {
	_, res, _ := restoreSeed(t)
	dst := emptyDB(t)
	dir := t.TempDir()
	written := false
	_, err := wsarchive.Restore(context.Background(), wsarchive.RestoreOptions{Archive: res.Path, DB: dst, Store: fsStore(t), StateDir: filepath.Join(dir, "s"), ScratchDir: filepath.Join(dir, "x"),
		Progress: func(p wsarchive.ArchiveProgress) {
			if p.Phase == "media" && !written {
				written = true
				_, _ = dst.C(store.CProjects).InsertOne(context.Background(), bson.M{"_id": "prj_meanwhile", "workspaceId": "ws_other", "name": "Made meanwhile"})
			}
		}})
	if c, r := code(err), reason(err); c != apperr.Conflict || r != "not_empty" {
		t.Fatalf("a database written meanwhile: %v", err)
	}
	if n, _ := dst.C(store.CWorkspaces).CountDocuments(context.Background(), bson.M{}); n != 0 {
		t.Fatal("the copy was merged into it")
	}
}

// A restore that stops halfway — a record its collection's unique index
// refuses, a record that doesn't read back the same — leaves its lock: the
// migration level is never written, MetaRush refuses to bring the database up
// to date or work in it, and the words say it holds part of the copy and how
// to start over.
func TestRestoreStoppedHalfwayIsLocked(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		what   string
		rel    string
		change func([]byte) []byte
	}{
		{"a duplicate name", "collections/recipes.jsonl", func(b []byte) []byte {
			return append(b, []byte(`{"_id":"rcp_dup","workspaceId":`+strings.Split(strings.Split(string(b), `"workspaceId":`)[1], ",")[0]+`,"name":"Basic explainer"}`+"\n")...)
		}},
		{"a record that reads back otherwise", "collections/projects.jsonl", func(b []byte) []byte {
			line := strings.SplitN(string(b), "\n", 2)[0]
			// _id last: MongoDB stores it first, so the copy reads back otherwise.
			var d bson.D
			if err := bson.UnmarshalExtJSON([]byte(line), true, &d); err != nil {
				t.Fatal(err)
			}
			id := d[0]
			d = append(d[1:], id)
			raw, _ := bson.Marshal(d)
			nl, _ := wsarchive.Line(raw)
			return append(nl, b[len(line)+1:]...)
		}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			_, res, _ := restoreSeed(t)
			rewriteFile(t, res.Path, tc.rel, tc.change)
			dst := emptyDB(t)
			_, err := restoreInto(t, res.Path, dst, fsStore(t), false)
			if err == nil {
				t.Fatal("the restore went through")
			}
			var ae *apperr.Error
			if !errorsAs(err, &ae) || !strings.Contains(ae.Message+" "+ae.Hint, "part of the copy") || !strings.Contains(ae.Hint, "new, empty database") {
				t.Fatalf("the words: %v", err)
			}
			if n, _ := dst.C(store.CMigrations).CountDocuments(ctx, bson.M{}); n != 0 {
				t.Errorf("the migration level was written: %d", n)
			}
			if _, err := dst.Migrate(ctx); err == nil || !strings.Contains(err.Error(), "part of a restore") {
				t.Errorf("bringing a half-restored database up to date: %v", err)
			}
		})
	}
}

func errorsAs(err error, ae **apperr.Error) bool {
	for e := err; e != nil; {
		if x, ok := e.(*apperr.Error); ok {
			*ae = x
			return true
		}
		u, ok := e.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		e = u.Unwrap()
	}
	return false
}
