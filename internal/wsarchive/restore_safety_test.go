package wsarchive_test

import (
	"archive/tar"
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
	"github.com/metavert-io/metarush/internal/media"
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

// addCollection adds a collection's file to an archive folder, with its
// manifest entry and matching checksums (an archive made up by hand).
func addCollection(t *testing.T, dir, name, lines string, options any) {
	t.Helper()
	rel := "collections/" + name + ".jsonl"
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(rel)), []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(lines))
	mp := filepath.Join(dir, wsarchive.ManifestName)
	var m map[string]any
	mb, _ := os.ReadFile(mp)
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{"name": name, "how": "as-is", "file": rel, "count": bytes.Count([]byte(lines), []byte("\n")), "bytes": len(lines), "sha256": hex.EncodeToString(sum[:]), "type": "collection"}
	if options != nil {
		entry["options"] = options
	}
	m["collections"] = append(m["collections"].([]any), entry)
	m["files"] = append(m["files"].([]any), map[string]any{"path": rel, "bytes": len(lines), "sha256": hex.EncodeToString(sum[:])})
	out, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(mp, out, 0o600); err != nil {
		t.Fatal(err)
	}
}

// An archive is restored only as an export makes it: a collection an export
// never holds (password hashes, say) or one made as a view is refused before
// anything is written, whatever its checksums say.
func TestRestoreRefusesCollectionsAnExportNeverHolds(t *testing.T) {
	for _, tc := range []struct {
		what, name, lines string
		options           any
	}{
		{"passwords", store.CUserCredentials, `{"_id":"usr_x","hash":"$argon2id$v=19$m=1,t=1,p=1$c2FsdA$aGFzaA"}` + "\n", nil},
		{"a collection MetaRush doesn't know", "notes_from_elsewhere", `{"_id":"n1"}` + "\n", nil},
		{"a view", store.CPeople + "_view", "", map[string]any{"viewOn": "users", "pipeline": []any{}}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			_, res, _ := restoreSeed(t)
			addCollection(t, res.Path, tc.name, tc.lines, tc.options)
			dst := emptyDB(t)
			_, err := restoreInto(t, res.Path, dst, fsStore(t), false)
			if code(err) != apperr.Integrity {
				t.Fatalf("restored: %v", err)
			}
			if names, _ := wsarchive.ListCollections(context.Background(), dst); len(names) != 1 {
				t.Fatalf("something was written: %v", names)
			}
		})
	}
	// A view's options on a collection an export does hold are refused too.
	_, res, _ := restoreSeed(t)
	mp := filepath.Join(res.Path, wsarchive.ManifestName)
	var m map[string]any
	mb, _ := os.ReadFile(mp)
	if err := json.Unmarshal(mb, &m); err != nil {
		t.Fatal(err)
	}
	for _, c := range m["collections"].([]any) {
		if c.(map[string]any)["name"] == store.CComments {
			c.(map[string]any)["options"] = map[string]any{"viewOn": "users", "pipeline": []any{}}
		}
	}
	mb, _ = json.Marshal(m)
	_ = os.WriteFile(mp, mb, 0o600)
	if _, err := restoreInto(t, res.Path, emptyDB(t), fsStore(t), false); code(err) != apperr.Integrity {
		t.Fatalf("a view's options: %v", err)
	}
}

// tarNames lists the files a tar holds.
func tarNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr := tar.NewReader(f)
	var out []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		out = append(out, h.Name)
	}
	return out
}

// A stopped export's leftovers never end up in another archive: an export
// with other options starts its folder afresh, and a single file holds only
// what the manifest lists.
func TestExportStartsAfreshAfterAStoppedOneWithOtherOptions(t *testing.T) {
	src := testutil.DB(t)
	st := fsStore(t)
	s := seed(t, src, st)
	for _, tarred := range []bool{true, false} {
		to := filepath.Join(t.TempDir(), "archive")
		dest := to
		if tarred {
			dest = wsarchive.TarPath(to)
		}
		// What a stopped `--media included` export of the same archive left.
		left := filepath.Join(dest+".partial", wsarchive.MediaDir, filepath.FromSlash(media.ObjectKey(s.ws, s.objects[0])))
		if err := os.MkdirAll(filepath.Dir(left), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(left, fixture(t, "beach.jpg"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(dest+".partial", "stray.txt"), []byte("left behind"), 0o600)
		res, err := wsarchive.Export(context.Background(), wsarchive.ExportOptions{DB: src, Workspace: s.ws, To: to, Tar: tarred, Media: wsarchive.MediaReferenced, Store: st})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		if tarred {
			names = tarNames(t, res.Path)
		} else {
			_ = filepath.WalkDir(res.Path, func(p string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					rel, _ := filepath.Rel(res.Path, p)
					names = append(names, filepath.ToSlash(rel))
				}
				return nil
			})
		}
		for _, n := range names {
			if strings.HasPrefix(n, wsarchive.MediaDir+"/") || n == "stray.txt" {
				t.Errorf("tar %v: a referenced archive holds %s", tarred, n)
			}
		}
	}
}

// Work that was running, and paid work that was waiting, is held in a copy
// for a person — never taken by any computer by itself, so a copy beside a
// still-running original never does (or pays for) the same work twice — and
// the restore lists it. Other waiting work waits as before.
func TestRestoreHoldsWorkThatCouldRunTwice(t *testing.T) {
	ctx := context.Background()
	src := testutil.DB(t)
	st := fsStore(t)
	s := seed(t, src, st)
	for _, j := range []bson.M{
		{"_id": "job_paid", "workspaceId": s.ws, "type": "generate", "state": "queued", "idempotencyKey": "g1", "cancelRequested": false},
		{"_id": "job_local", "workspaceId": s.ws, "type": "transcribe", "state": "queued", "idempotencyKey": "t2", "cancelRequested": false},
	} {
		if _, err := src.C(store.CJobs).InsertOne(ctx, j); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := src.C(store.CRenders).InsertOne(ctx, bson.M{"_id": "rnd_running", "workspaceId": s.ws, "state": "rendering", "jobId": "job_running"}); err != nil {
		t.Fatal(err)
	}
	res := exportTo(t, src, st, s.ws, wsarchive.MediaReferenced, false)
	dst := emptyDB(t)
	rep, err := restoreInto(t, res.Path, dst, st, false)
	if err != nil {
		t.Fatal(err)
	}
	state := func(coll, id string) string {
		var d bson.M
		_ = dst.C(coll).FindOne(ctx, bson.M{"_id": id}).Decode(&d)
		s, _ := d["state"].(string)
		return s
	}
	for id, want := range map[string]string{"job_running": "canceled", "job_paid": "canceled", "job_local": "queued"} {
		if got := state(store.CJobs, id); got != want {
			t.Errorf("%s: %s, want %s", id, got, want)
		}
	}
	if got := state(store.CRenders, "rnd_running"); got != "canceled" {
		t.Errorf("the render whose work was held: %s", got)
	}
	// The running render, the paid shot and the seed's import (it downloads).
	if rep.Held != 3 || !strings.Contains(strings.Join(rep.Notes, " "), "held here for a person") {
		t.Errorf("the report should list the held work: %d %v", rep.Held, rep.Notes)
	}
}

// A restore that continues checks the copy again first: the records that went
// in before it stopped must still be exactly the archive's — none added, none
// changed (once migrations ran, which change records and may add the
// workspace's own: none gone, none of another workspace) — or it stops,
// saying so, and the database stays held. A copy
// nobody touched continues and finishes. Exporting a database a restore still
// holds is refused too: there is no whole workspace in it.
func TestRestoreContinuesOnlyOverAnUntouchedCopy(t *testing.T) {
	ctx := context.Background()
	src := testutil.DB(t)
	st := fsStore(t)
	s := seed(t, src, st)
	res := exportTo(t, src, st, s.ws, wsarchive.MediaIncluded, false)
	stopped := func(t *testing.T) *store.DB {
		t.Helper()
		dst := emptyDB(t)
		dir := t.TempDir()
		_, err := wsarchive.Restore(ctx, wsarchive.RestoreOptions{Archive: res.Path, DB: dst, Store: fsStore(t), StoreWords: "the test storage",
			StateDir: filepath.Join(dir, "state"), ScratchDir: filepath.Join(dir, "scratch"),
			MayStore: func(context.Context, *store.DB, string) error { return apperr.New(apperr.Conflict, "Not now.") }})
		if err == nil {
			t.Fatal("the restore didn't stop")
		}
		if l, _ := dst.ReadRestoreLock(ctx); l == nil || l.State != store.RestoreFootage {
			t.Fatalf("not held while its footage waits: %+v", l)
		}
		return dst
	}
	// As the restore leaves it once every record is in and its footage
	// copied: the archive's migration level written, the copy being brought
	// up to date.
	migrating := func(t *testing.T, dst *store.DB) {
		t.Helper()
		l, _ := dst.ReadRestoreLock(ctx)
		mb, _ := os.ReadFile(filepath.Join(res.Path, wsarchive.ManifestName))
		m, err := wsarchive.ReadManifest(mb)
		if err != nil {
			t.Fatal(err)
		}
		for _, ap := range m.Level.Applied {
			if _, err := dst.C(store.CMigrations).InsertOne(ctx, bson.M{"_id": ap.Version, "name": ap.Name, "appliedAt": ap.AppliedAt}); err != nil {
				t.Fatal(err)
			}
		}
		if err := dst.SetRestoreLock(ctx, l.Archive, l.Workspace, l.Name, store.RestoreMigrating); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		what   string
		change func(t *testing.T, dst *store.DB)
	}{
		{"a record added", func(t *testing.T, dst *store.DB) {
			if _, err := dst.C(store.CProjects).InsertOne(ctx, bson.M{"_id": ids.New(ids.Project), "workspaceId": s.ws, "name": "Autumn launch", "archived": false}); err != nil {
				t.Fatal(err)
			}
		}},
		{"a record changed", func(t *testing.T, dst *store.DB) {
			if _, err := dst.C(store.CProjects).UpdateOne(ctx, bson.M{"workspaceId": s.ws}, bson.M{"$set": bson.M{"name": "Renamed meanwhile"}}); err != nil {
				t.Fatal(err)
			}
		}},
		{"another workspace's record", func(t *testing.T, dst *store.DB) {
			if _, err := dst.C(store.CProjects).InsertOne(ctx, bson.M{"_id": ids.New(ids.Project), "workspaceId": ids.New(ids.Workspace), "name": "Elsewhere"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"a record in a collection the archive hasn't", func(t *testing.T, dst *store.DB) {
			if _, err := dst.C(store.CNotifications).InsertOne(ctx, bson.M{"_id": ids.New("ntf"), "workspaceId": s.ws}); err != nil {
				t.Fatal(err)
			}
		}},
		{"a record gone once it was being brought up to date", func(t *testing.T, dst *store.DB) {
			migrating(t, dst)
			if _, err := dst.C(store.CProjects).DeleteOne(ctx, bson.M{"workspaceId": s.ws}); err != nil {
				t.Fatal(err)
			}
		}},
		{"another workspace's record once it was being brought up to date", func(t *testing.T, dst *store.DB) {
			migrating(t, dst)
			if _, err := dst.C(store.CNotifications).InsertOne(ctx, bson.M{"_id": ids.New("ntf"), "workspaceId": ids.New(ids.Workspace)}); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.what, func(t *testing.T) {
			dst := stopped(t)
			tc.change(t, dst)
			_, err := restoreInto(t, res.Path, dst, fsStore(t), false)
			var ae *apperr.Error
			if err == nil || !errorsAs(err, &ae) || !strings.Contains(ae.Hint, "new, empty database") {
				t.Fatalf("continued over a copy changed meanwhile: %v", err)
			}
			if l, _ := dst.ReadRestoreLock(ctx); !l.Unfinished() {
				t.Fatalf("the database was let go: %+v", l)
			}
		})
	}
	t.Run("untouched", func(t *testing.T) {
		dst := stopped(t)
		if _, err := wsarchive.Export(ctx, wsarchive.ExportOptions{DB: dst, Workspace: s.ws, To: filepath.Join(t.TempDir(), "again"), Store: st}); err == nil || !strings.Contains(err.Error(), "didn't finish") {
			t.Fatalf("exported a half-restored database: %v", err)
		}
		rep, err := restoreInto(t, res.Path, dst, fsStore(t), false)
		if err != nil || !rep.Verified {
			t.Fatalf("continuing an untouched copy: %v %+v", err, rep)
		}
	})
	t.Run("untouched while being brought up to date", func(t *testing.T) {
		dst := stopped(t)
		migrating(t, dst)
		rep, err := restoreInto(t, res.Path, dst, fsStore(t), false)
		if err != nil || !rep.Verified {
			t.Fatalf("continuing an untouched copy: %v %+v", err, rep)
		}
	})
}
