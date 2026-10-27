package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Ability files from the dashboard (D292), like Recipes' files (D219): the
// file comes down from GET /api/abilities/export as a download and goes back
// up as the body of POST /api/abilities/import — a signed-in person, the CSRF
// header on the import, the server choosing every path. A member may bring a
// file in as a draft; approving it stays an owner's, after a check here.

const abilityCardManifest = `ability: 1
name: title-card
title: Title card
does: Makes a plain title card picture from a few words.
runs: python3 make.py
inputs:
  words: {kind: words, max: 80, says: The words on the card}
outputs: [picture]
cost: free
needs: {programs: [python3]}
licence: MIT
test: {inputs: {words: Hello}}
`

const abilityCardScript = "print('a card')\n"

// keptAbility records a version of an ability the way a passing check here
// would — its canonical archive in the media store — in the given state
// (approved: by Tester). Moving its file around needs no sandbox.
func keptAbility(t *testing.T, a *app.App, notes, state string, number int) *abilities.Archive {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	for p, s := range map[string]string{abilities.ManifestFile: abilityCardManifest, "make.py": abilityCardScript, abilities.NotesFile: notes} {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	arc, err := abilities.Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	m, err := arc.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	tmp := filepath.Join(t.TempDir(), "archive.json")
	if err := os.WriteFile(tmp, arc.Bytes, 0o644); err != nil {
		t.Fatal(err)
	}
	key := media.ObjectKey(a.WorkspaceID(), arc.Hash)
	info, err := a.Media.PutFile(ctx, key, tmp, media.PutOptions{ContentType: "application/json", SHA256: arc.Hash}, media.Resume{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	files := []models.AbilityFile{}
	for _, f := range arc.Files {
		b := f.Bytes()
		s := sha256.Sum256(b)
		files = append(files, models.AbilityFile{Path: f.Path, Size: int64(len(b)), SHA256: hex.EncodeToString(s[:]), Program: f.Exec})
	}
	now := time.Now().UTC()
	v := models.AbilityVersion{ID: a.WorkspaceID() + "/" + arc.Hash, WorkspaceID: a.WorkspaceID(), Name: m.Name, Hash: arc.Hash, Number: number, Title: m.Title,
		Does: m.Does, Search: []string{}, Manifest: abilityCardManifest, Notes: notes, Files: files, Archive: models.AbilityObject{Key: key, SHA256: arc.Hash, Size: info.Size},
		Check: models.AbilityCheck{At: now, OK: true, Problems: []string{}, Warnings: []string{}}, State: state, Decisions: []models.AbilityDecision{},
		By: a.Actor, CreatedAt: now, UpdatedAt: now}
	if state == models.AbilityApproved {
		v.Decisions = append(v.Decisions, models.AbilityDecision{State: models.AbilityApproved, By: models.Actor{UserID: a.Actor.UserID, UserName: "Tester"}, At: now})
	}
	if err := a.DB.Insert(ctx, store.CAbilities, v); err != nil {
		t.Fatal(err)
	}
	return arc
}

// sendFile is a browser's upload of a file as a request's body.
func (b *browser) sendFile(path string, body io.Reader, csrf bool) (int, envelope) {
	b.t.Helper()
	req, _ := http.NewRequest("POST", b.d.base+path, body)
	req.Host = b.host
	for k, v := range b.hdr {
		req.Header.Set(k, v)
	}
	if csrf {
		req.Header.Set(clientHeader, "dashboard")
	}
	req.Header.Set("Content-Type", "application/zip")
	for n, v := range b.cookies {
		req.AddCookie(&http.Cookie{Name: n, Value: v})
	}
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer res.Body.Close()
	var env envelope
	_ = json.NewDecoder(res.Body).Decode(&env)
	return res.StatusCode, env
}

func TestAbilityFilesThroughTheDashboard(t *testing.T) {
	// Workspace A: an approved ability to export.
	d, _, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	arc := keptAbility(t, d.a, "Short words read best.\n", models.AbilityApproved, 1)
	get := func(query string) (*http.Response, []byte) {
		t.Helper()
		res, err := d.client.Get(d.base + "/api/abilities/export" + query)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res, b
	}
	if res, _ := get("?ref=title-card"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without a session: %d", res.StatusCode)
	}
	d.signIn()
	res, file := get("?ref=title-card")
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/zip" || !strings.Contains(res.Header.Get("Content-Disposition"), `filename=title-card.ability.zip`) ||
		res.Header.Get("Content-Length") == "" || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the download: %d %v %s", res.StatusCode, res.Header, file)
	}
	// The file: ability.json, the archive as it is, and the files people read.
	zr, err := zip.NewReader(bytes.NewReader(file), int64(len(file)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		got[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	var doc abilities.AbilityFileDoc
	if err := json.Unmarshal(got["ability.json"], &doc); err != nil {
		t.Fatalf("ability.json: %v (%v)", err, len(got))
	}
	if doc.Format != abilities.FileFormat || doc.Ability.Name != "title-card" || doc.Version.Hash != arc.Hash || doc.Version.Number != 1 ||
		doc.Approval.State != models.AbilityApproved || len(doc.Approval.Decisions) != 1 || doc.Approval.Decisions[0].By != "Tester" {
		t.Fatalf("ability.json: %+v", doc)
	}
	if !bytes.Equal(got["archive.json"], arc.Bytes) || string(got[abilities.ManifestFile]) != abilityCardManifest || string(got[abilities.NotesFile]) != "Short words read best.\n" {
		t.Fatalf("the file's contents: %v", len(got))
	}
	// A ref is needed, and must name an ability here.
	if res, b := get(""); res.StatusCode != http.StatusBadRequest || !strings.Contains(string(b), apperr.InvalidInput) || !strings.Contains(string(b), "Say which ability") {
		t.Fatalf("no ref: %d %s", res.StatusCode, b)
	}
	if res, b := get("?ref=no-such-ability"); res.StatusCode != http.StatusNotFound || !strings.Contains(string(b), apperr.NotFound) {
		t.Fatalf("an unknown ability: %d %s", res.StatusCode, b)
	}
	// The operations themselves stay off /api/ops (LocalOnly): the browser
	// never names a path on this computer.
	for _, op := range []string{"ability.export", "ability.import"} {
		if code, _ := d.do("POST", "/api/ops/"+op, map[string]any{"ref": "title-card", "file": "/etc/passwd", "out": "/tmp/x.zip"}, true); code == 200 {
			t.Fatalf("%s over /api/ops", op)
		}
	}

	// Workspace B: an owner, and a member signed in on another computer.
	b, _, _ := newRemoteDash(t)
	b.signIn()
	b.op("auth.method.set", map[string]any{"method": "password", "email": "owner@example.com", "password": "tomato garden at dusk"}, nil)
	var inv auth.UserLinkOut
	b.op("users.invite", map[string]any{"email": "maya@example.com", "name": "Maya"}, &inv)
	token := inv.Link[strings.LastIndex(inv.Link, "/")+1:]
	maya := b.tailnet("", "")
	if res, _, raw := maya.do("POST", "/api/auth/link/"+token, map[string]any{"password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("accept: %s", raw)
	}
	nobody := b.tailnet("", "")
	upload := func(who *browser, query string, body []byte, csrf bool) (int, envelope, abilities.AbilityImportOut) {
		t.Helper()
		code, env := who.sendFile("/api/abilities/import"+query, bytes.NewReader(body), csrf)
		var out abilities.AbilityImportOut
		_ = json.Unmarshal(env.Data, &out)
		return code, env, out
	}
	versions := func() int64 {
		n, _ := b.a.DB.C(store.CAbilities).CountDocuments(context.Background(), bson.M{"workspaceId": b.a.WorkspaceID()})
		return n
	}
	// People only: no session, and no CSRF header, are refused before anything is read.
	if code, env, _ := upload(nobody, "?dryRun=1", file, true); code != http.StatusUnauthorized || env.Error.Details["reason"] != "no_session" {
		t.Fatalf("without a session: %d %+v", code, env.Error)
	}
	if code, env, _ := upload(maya, "?dryRun=1", file, false); code != http.StatusUnauthorized || env.Error.Details["reason"] != "csrf" {
		t.Fatalf("without the CSRF header: %d %+v", code, env.Error)
	}
	// What isn't an ability file: nothing, too much, not a zip.
	if code, env, _ := upload(maya, "", nil, true); code != http.StatusBadRequest || !strings.Contains(env.Error.Message, "Choose an ability file") {
		t.Fatalf("an empty body: %d %+v", code, env.Error)
	}
	old := maxAbilityUpload
	maxAbilityUpload = int64(len(file)) - 1
	code, env, _ := upload(maya, "", file, true)
	maxAbilityUpload = old
	if code != http.StatusBadRequest || env.Error.Code != apperr.InvalidInput || !strings.Contains(env.Error.Message, "larger than an ability file can be") {
		t.Fatalf("too large: %d %+v", code, env.Error)
	}
	if code, env, _ := upload(maya, "", []byte("PK\x03\x04 this is not a zip at all"), true); code < 400 || code >= 500 || env.OK {
		t.Fatalf("a malformed zip: %d %+v", code, env.Error)
	}
	cut := append([]byte{}, file[:len(file)/2]...)
	if code, env, _ := upload(maya, "", cut, true); code < 400 || code >= 500 || env.OK {
		t.Fatalf("half a file: %d %+v", code, env.Error)
	}
	if versions() != 0 {
		t.Fatal("a refused file was kept")
	}
	// A dry run says what would come in and keeps nothing.
	code, env, out := upload(maya, "?dryRun=1", file, true)
	if code != 200 || !out.DryRun || !out.New || out.Of || out.State != models.AbilityImported || out.Version != arc.Hash || out.From == nil || out.From.Number != 1 || out.From.State != models.AbilityApproved || versions() != 0 {
		t.Fatalf("the dry run: %d %+v %+v", code, env.Error, out)
	}
	// An unrelated ability of the same name here: refused unless the person
	// says it is the same one (asVersion).
	keptAbility(t, b.a, "Another card, made here.\n", models.AbilityApproved, 1)
	if code, env, _ := upload(maya, "", file, true); code != http.StatusConflict || env.Error.Code != apperr.Conflict {
		t.Fatalf("the same name, no shared history: %d %+v", code, env.Error)
	}
	if code, _, out := upload(maya, "?dryRun=true&asVersion=true", file, true); code != 200 || !out.DryRun || !out.Of || versions() != 1 {
		t.Fatalf("a dry run as a version: %d %+v", code, out)
	}
	// The member brings it in: a draft, recorded as theirs, never approved here.
	code, env, out = upload(maya, "?asVersion=1", file, true)
	if code != 200 || out.DryRun || !out.New || !out.Of || out.Number != 2 || out.State != models.AbilityImported || versions() != 2 {
		t.Fatalf("brought in: %d %+v %+v", code, env.Error, out)
	}
	var v models.AbilityVersion
	if err := b.a.DB.GetByID(context.Background(), store.CAbilities, b.a.WorkspaceID(), b.a.WorkspaceID()+"/"+arc.Hash, &v); err != nil || v.State != models.AbilityImported ||
		v.Imported == nil || v.Imported.By.UserName != "Maya" || v.Imported.State != models.AbilityApproved || len(v.Decisions) != 0 {
		t.Fatalf("the draft: %v %+v", err, v)
	}
	// Bringing in the version it already is brings nothing new.
	if code, _, again := upload(maya, "", file, true); code != 200 || again.New || again.Number != 2 || versions() != 2 {
		t.Fatalf("again: %d %+v", code, again)
	}
	// Approving it is an owner's: a member is refused; an owner is refused
	// too until it has been checked on a computer here.
	ref := "title-card@" + arc.Hash
	if res, env, _ := maya.do("POST", "/api/ops/ability.approve", map[string]any{"ref": ref}, true, nil); res.StatusCode != http.StatusUnauthorized || env.Error.Details["reason"] != "owner_only" {
		t.Fatalf("a member approving: %d %+v", res.StatusCode, env.Error)
	}
	if code, env := b.do("POST", "/api/ops/ability.approve", map[string]any{"ref": ref}, true); code == 200 || env.Error.Details["reason"] == "owner_only" || !strings.Contains(strings.ToLower(env.Error.Message), "check") {
		t.Fatalf("an owner approving an unchecked import: %d %+v", code, env.Error)
	}
	// The member can take the draft's file down again.
	if res, _, raw := maya.do("GET", "/api/abilities/export?ref="+ref, nil, false, nil); res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/zip" || len(raw) == 0 {
		t.Fatalf("a member's download: %d %v", res.StatusCode, res.Header)
	}
}

// The file endpoints refuse plainly when this computer's scratch folder
// can't be written: ability files and the Recipes feature's files say so
// instead of sending half a file (the Recipes feature's other refusals are
// tested with that feature).
func TestFileEndpointsRefusePlainly(t *testing.T) {
	d, _, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	d.signIn()
	keptAbility(t, d.a, "Short words read best.\n", models.AbilityApproved, 1)
	// A scratch folder that can't be made, then one that can't be written.
	scratch := d.a.Cfg.Scratch.Dir
	t.Cleanup(func() { d.a.Cfg.Scratch.Dir = scratch })
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	for _, dir := range []string{filepath.Join(file, "scratch"), locked} {
		d.a.Cfg.Scratch.Dir = dir
		for _, c := range []struct{ method, path string }{{"GET", "/api/abilities/export?ref=title-card"}, {"POST", "/api/abilities/import"},
			{"GET", "/api/recipes/export?recipe=anything"}, {"POST", "/api/recipes/import"}} {
			code, hdr, b := d.send(c.method, c.path, []byte("PK"), map[string]string{"Content-Type": "application/zip"}, c.method == "POST")
			if code < 500 || hdr.Get("Content-Type") == "application/zip" || envOf(t, b).OK {
				t.Fatalf("%s %s with the scratch folder %s: %d %s", c.method, c.path, dir, code, b)
			}
		}
	}
}

// The recorder's endpoints refuse what they can't read: a start or a clock
// that isn't JSON, a start the recorder refuses, a clock, an offset or a
// piece for a recording that isn't there or without its place in the file.
func TestRecordingEndpointsRefusePlainly(t *testing.T) {
	d, _, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	d.signIn()
	asJSON := map[string]string{"Content-Type": "application/json"}
	for _, c := range []struct {
		method, path string
		body         []byte
		hdr          map[string]string
		want         int
	}{
		{"POST", "/api/recordings", []byte("not json"), asJSON, http.StatusBadRequest},
		{"POST", "/api/recordings", []byte(`{"name":"Demo","tracks":[]}`), asJSON, http.StatusBadRequest},
		{"POST", "/api/recordings/rcd_none/clock", []byte("not json"), asJSON, http.StatusBadRequest},
		{"POST", "/api/recordings/rcd_none/clock", []byte(`{}`), asJSON, http.StatusNotFound},
		{"PATCH", "/api/recordings/rcd_none/tracks/voice", []byte("pcm"), map[string]string{"Content-Type": "application/octet-stream"}, http.StatusBadRequest},
	} {
		if code, _, b := d.send(c.method, c.path, c.body, c.hdr, true); code != c.want || envOf(t, b).OK {
			t.Fatalf("%s %s %q: %d %s", c.method, c.path, c.body, code, b)
		}
	}
	if code, _, _ := d.send("HEAD", "/api/recordings/rcd_none/tracks/voice", nil, nil, false); code != http.StatusNotFound {
		t.Fatalf("the offset of a recording that isn't there: %d", code)
	}
}
