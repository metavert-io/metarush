package wsarchive_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// emptyDB is a new test database with nothing in it but the test run's marker
// (dropped after the test).
func emptyDB(t *testing.T) *store.DB {
	t.Helper()
	uri := testutil.URI()
	name := store.TestDBPrefix + strings.TrimPrefix(ids.New("t"), "t_")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := store.Connect(ctx, uri, name)
	if err != nil {
		if os.Getenv("METARUSH_REQUIRE_MONGO") == "1" {
			t.Fatalf("test MongoDB unavailable: %v", err)
		}
		t.Skipf("test MongoDB unavailable (run scripts/dev-mongo.sh): %v", err)
	}
	if err := db.MarkTestRun(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := db.DropTestDatabase(c, uri); err != nil {
			t.Errorf("drop test db: %v", err)
		}
		_ = db.Close(c)
	})
	return db
}

// migrateTo brings a database to a migration level the way MetaRush did then:
// every migration up to that version, each recorded.
func migrateTo(t *testing.T, db *store.DB, level int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, m := range store.Migrations {
		if m.Version > level {
			break
		}
		if err := m.Up(ctx, db); err != nil {
			t.Fatalf("migration %d: %v", m.Version, err)
		}
		if _, err := db.C(store.CMigrations).InsertOne(ctx, bson.M{"_id": m.Version, "name": m.Name, "appliedAt": time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
}

func fsStore(t *testing.T) *media.FS {
	t.Helper()
	st, err := media.NewFS(filepath.Join(t.TempDir(), "media"), false)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// putObject stores bytes by content address.
func putObject(t *testing.T, st media.Store, ws string, b []byte) string {
	t.Helper()
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])
	p := filepath.Join(t.TempDir(), "obj")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PutFile(context.Background(), media.ObjectKey(ws, sha), p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	return sha
}

// putLogBody stores a long log text the way the activity log does (gzipped, by the text's SHA-256).
func putLogBody(t *testing.T, st media.Store, ws, text string) {
	t.Helper()
	sum := sha256.Sum256([]byte(text))
	sha := hex.EncodeToString(sum[:])
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write([]byte(text))
	_ = zw.Close()
	p := filepath.Join(t.TempDir(), "body")
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	key := "ws/" + ws + "/logs/" + sha[:2] + "/" + sha + ".gz"
	if _, err := st.PutFile(context.Background(), key, p, media.PutOptions{}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "images", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Made-up credentials seeded into a workspace: none may reach an archive.
// (Built at run time so the repository holds no key-shaped text.)
var (
	fakeS3Secret   = "tsec_" + strings.Repeat("Q7", 16)
	fakeOpenAIKey  = "sk-" + "proj" + strings.Repeat("x9", 14)
	fakeDBPassword = "maya" + "Hunter2" + "Pass"
	fakePwHash     = "$argon2id$v=19$m=65536,t=3,p=2$" + strings.Repeat("c2FsdA", 4)
	fakeTokenHash  = strings.Repeat("ab12", 16)
	fakeSession    = "https://storage.googleapis.com/upload/storage/v1/b/bkt/o?uploadType=resumable&upload_id=" + strings.Repeat("ADPycd", 8)
	fakeBearer     = "eyJhbGciOiJIUzI1NiJ9." + strings.Repeat("eyJzdWIiOiIxIn0", 2) + "." + strings.Repeat("sig_", 6)
)

// seeded is a workspace with something in nearly every collection.
type seeded struct {
	ws      string
	project string
	objects []string
	secrets []string
}

func seed(t *testing.T, db *store.DB, st media.Store) seeded {
	t.Helper()
	ctx := context.Background()
	ws := ids.New(ids.Workspace)
	now := time.Now().UTC().Truncate(time.Millisecond)
	owner := ids.New(ids.User)
	inst := ids.New(ids.Installation)
	prj := ids.New(ids.Project)
	s := seeded{ws: ws, project: prj, secrets: []string{fakeS3Secret, fakeOpenAIKey, fakeDBPassword, fakePwHash, fakeTokenHash, "upload_id=" + strings.Repeat("ADPycd", 8), fakeBearer}}
	for _, name := range []string{"beach.jpg", "dog.jpg", "portrait.jpg"} {
		s.objects = append(s.objects, putObject(t, st, ws, fixture(t, name)))
	}
	putLogBody(t, st, ws, "a long Claude answer kept in the media store")
	ins := func(coll string, docs ...bson.M) {
		t.Helper()
		for _, d := range docs {
			if _, ok := d["workspaceId"]; !ok {
				d["workspaceId"] = ws
			}
			if _, err := db.C(coll).InsertOne(ctx, d); err != nil {
				t.Fatalf("%s: %v", coll, err)
			}
		}
	}
	actor := bson.M{"userId": owner, "userName": "Maya Lin", "installationId": inst, "via": "cli"}
	ins(store.CWorkspaces, bson.M{"_id": ws, "name": "FernBrook Studio", "createdAt": now, "createdBy": actor,
		"mediaStore": bson.M{"provider": "filesystem", "locator": st.Locator(), "shared": false}})
	ins(store.CUsers,
		bson.M{"_id": owner, "name": "Maya Lin", "email": "maya@example.com", "emailKey": "maya@example.com", "role": "owner", "state": "active", "createdAt": now, "passwordSetAt": now},
		bson.M{"_id": ids.New(ids.User), "name": "Sam Ortiz", "email": "sam@example.com", "role": "member", "state": "active", "createdAt": now})
	ins(store.CUserCredentials, bson.M{"_id": owner, "hash": fakePwHash, "setAt": now})
	ins(store.CAuthTokens, bson.M{"_id": fakeTokenHash, "kind": "reset", "userId": owner, "expiresAt": now.Add(time.Hour)})
	ins(store.CAuthFailures, bson.M{"_id": owner, "count": 2})
	ins(store.CInstallations, bson.M{"_id": inst, "name": "Maya's MacBook", "hostname": "mayas-mbp", "os": "darwin", "arch": "arm64", "version": "0.14.0",
		"userId": owner, "createdAt": now, "lastSeenAt": now.Add(-48 * time.Hour), "storageKeyId": "tid_" + "A1B2C3D4E5F6"})
	ins(store.CWorkers, bson.M{"_id": inst, "name": "Maya's MacBook", "lastHeartbeat": now})
	ins(store.CAbilitySetups, bson.M{"_id": ids.New("abs"), "installationId": inst, "programs": bson.A{bson.M{"name": "ffmpeg", "path": "/opt/homebrew/bin/ffmpeg"}}})
	ins(store.CProjects, bson.M{"_id": prj, "name": "Spring launch", "archived": false, "createdAt": now, "updatedAt": now, "members": bson.A{owner}})
	ins(store.CAssets, bson.M{"_id": ids.New(ids.Asset), "sha256": s.objects[0], "storageKey": media.ObjectKey(ws, s.objects[0]), "size": int64(len(fixture(t, "beach.jpg"))),
		"projectIds": bson.A{prj}, "createdAt": now, "state": "available", "source": bson.M{"path": "~/Movies/beach.jpg", "machine": "Maya's MacBook"}})
	ins(store.CSegments, bson.M{"_id": ids.New(ids.Segment), "assetId": "ast_x", "idx": int32(0), "text": "Welcome to the spring launch", "start": 0.0, "end": 2.5})
	ins(store.CJobs,
		bson.M{"_id": ids.New(ids.Job), "type": "transcribe", "state": "succeeded", "idempotencyKey": "t1", "attemptCount": int32(1), "finishedAt": now},
		bson.M{"_id": "job_running", "type": "render", "state": "running", "idempotencyKey": "r1", "attemptCount": int32(2),
			"lease":    bson.M{"owner": inst, "expiresAt": now.Add(time.Minute)},
			"attempts": bson.A{bson.M{"worker": inst, "startedAt": now.Add(-time.Hour), "endedAt": now.Add(-50 * time.Minute), "outcome": "failed"}, bson.M{"worker": inst, "startedAt": now}},
			"error":    "upload refused: secret_access_key=" + fakeS3Secret},
		bson.M{"_id": ids.New(ids.Job), "type": "import", "state": "queued", "idempotencyKey": "i1", "installation": inst})
	ins(store.CAssistantConversations, bson.M{"_id": "asc_1", "title": "Tighter opening", "userId": owner, "installationId": inst, "installationName": "Maya's MacBook",
		"state": "working", "holderPid": int32(4242), "archived": false, "turns": bson.A{bson.M{"prompt": "use this key " + fakeOpenAIKey + " for the voice"}}})
	ins(store.CComments,
		bson.M{"_id": ids.New(ids.Comment), "projectId": prj, "text": "the old database was mongodb+srv://maya:" + fakeDBPassword + "@cluster0.example.net/x", "createdAt": now},
		bson.M{"_id": ids.New(ids.Comment), "projectId": prj, "text": "header was Bearer " + fakeBearer, "createdAt": now},
		bson.M{"_id": ids.New(ids.Comment), "projectId": prj, "text": "Lena: trim the pause at 0:42", "createdAt": now})
	ins(store.CIngest, bson.M{"_id": ids.New(ids.Ingest), "installationId": inst, "sourceName": "dog.jpg", "state": "uploading",
		"upload": bson.M{"key": "k", "sessionUri": fakeSession, "offset": int64(10)}})
	ins(store.CConsents, bson.M{"_id": ids.New(ids.Consent), "person": "Lena Park", "covers": bson.A{"voice"}, "recordedAt": now})
	ins(store.CLogEntries, bson.M{"_id": ids.New("log"), "kind": "op", "summary": "made a project", "at": now})
	// Someone else's workspace in the same database, a record of no
	// workspace, and a collection MetaRush doesn't know.
	ins(store.CProjects, bson.M{"_id": ids.New(ids.Project), "workspaceId": "ws_someoneelse", "name": "Not ours"})
	if _, err := db.C(store.CComments).InsertOne(ctx, bson.M{"_id": "cmt_orphan", "text": "no workspace"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.C("scratch_notes").InsertOne(ctx, bson.M{"_id": "n1", "workspaceId": ws, "text": "unknown"}); err != nil {
		t.Fatal(err)
	}
	return s
}

// asMap reads a nested document whichever way the driver decoded it.
func asMap(v any) map[string]any {
	switch d := v.(type) {
	case bson.M:
		return d
	case bson.D:
		m := map[string]any{}
		for _, e := range d {
			m[e.Key] = e.Value
		}
		return m
	}
	return nil
}

func code(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func reason(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		if d, ok := ae.Details.(map[string]any); ok {
			r, _ := d["reason"].(string)
			return r
		}
	}
	return ""
}

// fingerprint is everything a database holds: per collection its documents
// (MongoDB's dbHash), its indexes and options — to show an export wrote nothing.
func fingerprint(t *testing.T, db *store.DB) string {
	t.Helper()
	ctx := context.Background()
	var h bson.M
	if err := db.DB.RunCommand(ctx, bson.D{{Key: "dbHash", Value: 1}}).Decode(&h); err != nil {
		t.Fatal(err)
	}
	colls, err := wsarchive.ListCollections(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString(h["md5"].(string))
	for _, c := range colls {
		b.WriteString("|" + c.Name + ":" + c.Options.String())
		ixs, err := wsarchive.ListIndexes(ctx, db, c.Name)
		if err != nil {
			t.Fatal(err)
		}
		for _, ix := range ixs {
			b.WriteString(ix.String())
		}
	}
	return b.String()
}

// grepTree reports any of the strings found in any file under root.
func grepTree(t *testing.T, root string, needles []string) []string {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range needles {
			if bytes.Contains(b, []byte(n)) {
				found = append(found, filepath.Base(p)+": "+n)
			}
		}
		return nil
	})
	return found
}

func exportTo(t *testing.T, db *store.DB, st media.Store, ws, mode string, tar bool) *wsarchive.Result {
	t.Helper()
	to := filepath.Join(t.TempDir(), "archive")
	res, err := wsarchive.Export(context.Background(), wsarchive.ExportOptions{DB: db, Workspace: ws, To: to, Tar: tar, Media: mode, Store: st,
		StoreWords: "the folder media on this computer", Scrub: wsarchive.NewScrubber(nil, fakeS3Secret),
		MadeBy: wsarchive.ArchiveMadeBy{MetaRush: "test", Machine: "test-machine", Person: "Maya Lin"}})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	return res
}

func restoreInto(t *testing.T, archive string, db *store.DB, st media.Store, plan bool) (*wsarchive.RestoreReport, error) {
	t.Helper()
	dir := t.TempDir()
	return wsarchive.Restore(context.Background(), wsarchive.RestoreOptions{Archive: archive, DB: db, Store: st, StoreWords: "the test storage",
		StateDir: filepath.Join(dir, "state"), ScratchDir: filepath.Join(dir, "scratch"), Plan: plan})
}

func collection(m *wsarchive.Manifest, name string) *wsarchive.CollectionFile {
	for i := range m.Collections {
		if m.Collections[i].Name == name {
			return &m.Collections[i]
		}
	}
	return nil
}

func leftOut(m *wsarchive.Manifest, name string) *wsarchive.LeftOutCollection {
	for i := range m.LeftOut {
		if m.LeftOut[i].Name == name {
			return &m.LeftOut[i]
		}
	}
	return nil
}

// A workspace goes out and comes back whole: the archive is private, holds
// no credential, says what it left out and why, and a restore into an empty
// database holds exactly the same documents, its media found where it was.
func TestRoundTripReferenced(t *testing.T) {
	src := testutil.DB(t)
	st := fsStore(t)
	s := seed(t, src, st)
	before := fingerprint(t, src)
	res := exportTo(t, src, st, s.ws, wsarchive.MediaReferenced, false)
	if after := fingerprint(t, src); after != before {
		t.Fatal("the export changed the database it read")
	}
	m := res.Manifest
	if m.Format != wsarchive.Format || m.Workspace.ID != s.ws || m.Workspace.Name != "FernBrook Studio" || m.MadeBy.MetaRush != "test" {
		t.Fatalf("manifest: %+v", m)
	}
	if m.Level.Highest != store.SchemaVersion() || m.Level.Older || len(m.Level.Applied) != len(store.Migrations) {
		t.Fatalf("migration level: %+v", m.Level)
	}
	// Private: folders 0700, files 0600.
	_ = filepath.WalkDir(res.Path, func(p string, d fs.DirEntry, err error) error {
		info, _ := d.Info()
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s is %v, want %v", p, info.Mode().Perm(), want)
		}
		return nil
	})
	// What went in and how.
	for name, want := range map[string]int64{store.CUsers: 2, store.CProjects: 1, store.CJobs: 3, store.CComments: 3, store.CInstallations: 1, store.CWorkspaces: 1} {
		if c := collection(m, name); c == nil || c.Count != want {
			t.Errorf("%s: %+v, want %d documents", name, c, want)
		}
	}
	for name, n := range map[string]int64{store.CUsers: 1, store.CJobs: 1, store.CAssistantConversations: 1, store.CIngest: 1} {
		if c := collection(m, name); c == nil || c.Transformed != n {
			t.Errorf("%s transformed: %+v", name, c)
		}
	}
	if c := collection(m, store.CComments); c.Scrubbed != 2 {
		t.Errorf("comments scrubbed: %+v", c)
	}
	for _, name := range []string{store.CUserCredentials, store.CAuthTokens, store.CAuthFailures, store.CWorkers, store.CAbilitySetups} {
		if lo := leftOut(m, name); lo == nil || lo.Count != 1 || lo.Why == "" {
			t.Errorf("%s should be left out with why: %+v", name, lo)
		}
		if collection(m, name) != nil {
			t.Errorf("%s went in", name)
		}
	}
	if lo := leftOut(m, "scratch_notes"); lo == nil || !lo.Unknown {
		t.Errorf("an unknown collection must be named: %+v", m.LeftOut)
	}
	if collection(m, store.CMigrations) != nil || collection(m, store.CTestRun) != nil {
		t.Error("the migration records and the test marker are not documents of the archive")
	}
	notes := strings.Join(m.Notes, " ")
	if !strings.Contains(notes, "other workspaces") || !strings.Contains(notes, "belonging to no workspace") {
		t.Errorf("notes: %v", m.Notes)
	}
	if m.Media.Mode != wsarchive.MediaReferenced || m.Media.Files != 4 {
		t.Errorf("media: %+v", m.Media)
	}
	if _, err := os.Stat(filepath.Join(res.Path, wsarchive.MediaDir)); !errors.Is(err, os.ErrNotExist) {
		t.Error("referenced media copies no files")
	}
	// No credential anywhere in the archive.
	if found := grepTree(t, res.Path, s.secrets); len(found) > 0 {
		t.Fatalf("credentials in the archive: %v", found)
	}
	// The restore.
	dst := emptyDB(t)
	plan, err := restoreInto(t, res.Path, dst, st, true)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.Plan || plan.Media.Present != 4 || plan.Media.Missing != 0 || len(plan.Migrated) != 0 || plan.Documents == 0 {
		t.Fatalf("plan: %+v", plan)
	}
	if colls, _ := wsarchive.ListCollections(context.Background(), dst); len(colls) != 1 {
		t.Fatalf("a plan wrote something: %v", colls)
	}
	rep, err := restoreInto(t, res.Path, dst, st, false)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !rep.Verified || rep.Media.Present != 4 || len(rep.Migrated) != 0 {
		t.Fatalf("report: %+v", rep)
	}
	ctx := context.Background()
	for _, c := range m.Collections {
		d, err := wsarchive.HashCollection(ctx, dst, c.Name, s.ws)
		if err != nil || d.SHA256 != c.SHA256 || d.Count != c.Count {
			t.Errorf("%s: %+v %v", c.Name, d, err)
		}
	}
	// Indexes came along (a unique one refuses a duplicate).
	if _, err := dst.C(store.CAssets).InsertOne(ctx, bson.M{"_id": "ast_dup", "workspaceId": s.ws, "sha256": s.objects[0]}); !store.IsDuplicateKey(err) {
		t.Errorf("the unique index on footage didn't come along: %v", err)
	}
	var job bson.M
	if err := dst.C(store.CJobs).FindOne(ctx, bson.M{"_id": "job_running"}).Decode(&job); err != nil {
		t.Fatal(err)
	}
	if job["state"] != "queued" || job["lease"] != nil || job["attemptCount"] != int32(1) {
		t.Errorf("a running job comes back waiting, its attempt not counted: %v", job)
	}
	if atts := job["attempts"].(bson.A); asMap(atts[1])["outcome"] != "released" || asMap(atts[0])["outcome"] != "failed" {
		t.Errorf("its last attempt is released, the one before as it was: %v", atts)
	}
	var conv bson.M
	_ = dst.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": "asc_1"}).Decode(&conv)
	if conv["state"] != "idle" || conv["holderPid"] != int32(0) {
		t.Errorf("conversation: %v", conv)
	}
	var user bson.M
	_ = dst.C(store.CUsers).FindOne(ctx, bson.M{"role": "owner"}).Decode(&user)
	if _, ok := user["passwordSetAt"]; ok || user["email"] != "maya@example.com" {
		t.Errorf("owner: %v", user)
	}
	if n, _ := dst.C(store.CUserCredentials).CountDocuments(ctx, bson.M{}); n != 0 {
		t.Error("passwords came back")
	}
	if n, _ := dst.C(store.CMigrations).CountDocuments(ctx, bson.M{}); n != int64(len(store.Migrations)) {
		t.Errorf("migration level: %d", n)
	}
	if len(rep.Elsewhere) != 1 || rep.Elsewhere[0].Computer != "Maya's MacBook" || rep.Elsewhere[0].Jobs != 1 || rep.Elsewhere[0].Conversations != 1 {
		t.Errorf("what stays the old computer's: %+v", rep.Elsewhere)
	}
	// A second restore into the same database is refused: it isn't empty.
	if _, err := restoreInto(t, res.Path, dst, st, false); code(err) != apperr.Conflict || reason(err) != "not_empty" {
		t.Fatalf("restore into a database that isn't empty: %v", err)
	}
}

// Included media travels in the archive (one file with --tar) and lands in
// another storage by content address, checked; the copy's workspace says it
// keeps its footage there.
func TestRoundTripIncludedTar(t *testing.T) {
	src := testutil.DB(t)
	st := fsStore(t)
	s := seed(t, src, st)
	res := exportTo(t, src, st, s.ws, wsarchive.MediaIncluded, true)
	if !res.Tar || !strings.HasSuffix(res.Path, ".tar") {
		t.Fatalf("result: %+v", res)
	}
	if info, err := os.Stat(res.Path); err != nil || info.Mode().Perm() != 0o600 || info.IsDir() {
		t.Fatalf("the archive is one private file: %v %v", info, err)
	}
	if _, err := os.Stat(res.Path + ".partial"); !errors.Is(err, os.ErrNotExist) {
		t.Error("the folder it was made in is left behind")
	}
	if b, _ := os.ReadFile(res.Path); bytes.Contains(b, []byte(fakeS3Secret)) || bytes.Contains(b, []byte(fakePwHash)) {
		t.Fatal("a credential in the archive")
	}
	m := res.Manifest
	if m.Media.Mode != wsarchive.MediaIncluded || m.Media.Files != 4 {
		t.Fatalf("media: %+v", m.Media)
	}
	target := fsStore(t)
	dst := emptyDB(t)
	rep, err := restoreInto(t, res.Path, dst, target, false)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !rep.Verified || rep.Media.Copied != 4 {
		t.Fatalf("report: %+v", rep.Media)
	}
	for _, sha := range s.objects {
		rc, _, err := target.Get(context.Background(), media.ObjectKey(s.ws, sha), 0, -1)
		if err != nil {
			t.Fatalf("object %s not in the new storage: %v", sha, err)
		}
		rc.Close()
	}
	ws, _ := wsarchive.FindWorkspace(context.Background(), dst, s.ws)
	if ws == nil || ws.MediaStore.Locator != target.Locator() {
		t.Fatalf("the copy's workspace should name its new storage: %+v", ws)
	}
}

// A changed byte anywhere — a collection, the media list, a media file — is
// refused before anything is written.
func TestTamperedArchivesAreRefused(t *testing.T) {
	src := testutil.DB(t)
	st := fsStore(t)
	s := seed(t, src, st)
	res := exportTo(t, src, st, s.ws, wsarchive.MediaIncluded, false)
	flip := func(p string) func() {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		orig := append([]byte{}, b...)
		b[len(b)/2] ^= 0x01
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return func() { _ = os.WriteFile(p, orig, 0o600) }
	}
	obj := filepath.Join(res.Path, wsarchive.MediaDir, filepath.FromSlash(media.ObjectKey(s.ws, s.objects[1])))
	for _, p := range []string{
		filepath.Join(res.Path, "collections", store.CProjects+".jsonl"),
		filepath.Join(res.Path, wsarchive.MediaIndexName),
		obj,
	} {
		undo := flip(p)
		dst := emptyDB(t)
		_, err := restoreInto(t, res.Path, dst, fsStore(t), false)
		if code(err) != apperr.Integrity {
			t.Fatalf("%s changed: %v", filepath.Base(p), err)
		}
		if colls, _ := wsarchive.ListCollections(context.Background(), dst); len(colls) != 1 {
			t.Fatalf("%s changed, yet the restore wrote: %v", filepath.Base(p), colls)
		}
		undo()
	}
	// A manifest whose checksum was changed to match nothing.
	mp := filepath.Join(res.Path, wsarchive.ManifestName)
	b, _ := os.ReadFile(mp)
	c := collection(res.Manifest, store.CUsers)
	if err := os.WriteFile(mp, bytes.Replace(b, []byte(c.SHA256), []byte(strings.Repeat("0", 64)), -1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := restoreInto(t, res.Path, emptyDB(t), fsStore(t), false); code(err) != apperr.Integrity {
		t.Fatalf("a changed checksum: %v", err)
	}
	_ = os.WriteFile(mp, b, 0o600)
	// Untouched again, it restores.
	if _, err := restoreInto(t, res.Path, emptyDB(t), fsStore(t), false); err != nil {
		t.Fatalf("the untouched archive: %v", err)
	}
}

// A database a newer MetaRush migrated is never read; an archive from one is
// never restored.
func TestNewerIsRefused(t *testing.T) {
	src := testutil.DB(t)
	st := fsStore(t)
	s := seed(t, src, st)
	res := exportTo(t, src, st, s.ws, wsarchive.MediaReferenced, false)
	ctx := context.Background()
	if _, err := src.C(store.CMigrations).InsertOne(ctx, bson.M{"_id": 9999, "name": "from the future", "appliedAt": time.Now()}); err != nil {
		t.Fatal(err)
	}
	_, err := wsarchive.Export(ctx, wsarchive.ExportOptions{DB: src, Workspace: s.ws, To: filepath.Join(t.TempDir(), "a"), Store: st})
	if code(err) != apperr.Conflict || reason(err) != "newer_database" {
		t.Fatalf("export of a newer database: %v", err)
	}
	if _, err := wsarchive.MakePlan(ctx, wsarchive.ExportOptions{DB: src, Workspace: s.ws, Store: st}); reason(err) != "newer_database" {
		t.Fatalf("plan of a newer database: %v", err)
	}
	mp := filepath.Join(res.Path, wsarchive.ManifestName)
	b, _ := os.ReadFile(mp)
	b = bytes.Replace(b, []byte(`"applied": [`), []byte(`"applied": [{"version": 9999, "name": "from the future"},`), 1)
	_ = os.WriteFile(mp, b, 0o600)
	if _, err := restoreInto(t, res.Path, emptyDB(t), st, false); reason(err) != "newer_database" {
		t.Fatalf("restore of a newer archive: %v", err)
	}
}
