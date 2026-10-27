package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/browserext"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// extPair pairs an extension (origin) with a code and returns its token and ID.
func (d *dash) extPair(code, origin string) ExtPairOut {
	d.t.Helper()
	in, _ := json.Marshal(ExtPairIn{Code: code, Name: "Chrome", Version: "0.3.0"})
	st, out, _ := d.ext("POST", "/ext/pair", origin, "", in, nil)
	if st != http.StatusOK {
		d.t.Fatalf("pair %s: %d %+v", origin, st, out.Error)
	}
	var p ExtPairOut
	_ = json.Unmarshal(out.Data, &p)
	return p
}

func (d *dash) extUpload(token, origin string) string {
	d.t.Helper()
	body, _ := json.Marshal(map[string]any{"name": "clip.mp4", "size": 10, "method": "browser", "source": map[string]any{"url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ"}})
	st, out, _ := d.ext("POST", "/ext/uploads", origin, token, body, nil)
	if st != http.StatusOK {
		d.t.Fatalf("upload: %d %+v", st, out.Error)
	}
	var up ExtUploadOut
	_ = json.Unmarshal(out.Data, &up)
	return up.ID
}

const mayaOrigin = "chrome-extension://cccccccccccccccccccccccccccccccc"

// Browser extension pairings belong to a person (D197): a member pairs her own
// browser, what it sends (videos, page snapshots) is hers, she sees only her
// own browsers and unpairs them, owners see and unpair everyone's, and a
// turned-off account's browser stops at once (and works again once it is on).
func TestExtensionPairingsArePeoples(t *testing.T) {
	d, _, _ := newRemoteDash(t)
	d.signIn() // the owner, on this computer
	ctx := context.Background()
	d.op("auth.method.set", map[string]any{"method": "password", "email": "owner@example.com", "password": "tomato garden at dusk"}, nil)
	var inv auth.UserLinkOut
	d.op("users.invite", map[string]any{"email": "maya@example.com", "name": "Maya"}, &inv)
	maya := d.tailnet("", "")
	if res, _, raw := maya.do("POST", "/api/auth/link/"+inv.Link[strings.LastIndex(inv.Link, "/")+1:], map[string]any{"password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("accept: %s", raw)
	}
	var u models.User
	if err := d.a.DB.C(store.CUsers).FindOne(ctx, bson.M{"workspaceId": d.a.WorkspaceID(), "emailKey": "maya@example.com"}).Decode(&u); err != nil {
		t.Fatal(err)
	}

	// A member pairs her own browser: no owner needed.
	var pair browserext.ExtensionPairOut
	maya.op("extension.pair", map[string]any{}, &pair)
	if pair.For != "Maya" || pair.Code == "" {
		t.Fatalf("her code: %+v", pair)
	}
	// The owner's code waits too: several people's codes at once.
	ownerCode := pairCode(t, d)
	mine := d.extPair(pair.Code, mayaOrigin)
	if mine.For != "Maya" {
		t.Fatalf("paired for %q", mine.For)
	}
	theirs := d.extPair(ownerCode, extOrigin)
	if theirs.For != "Tester" {
		t.Fatalf("the owner's browser is paired for %q", theirs.For)
	}

	// What her browser sends is hers: a video, a page snapshot.
	rec, _ := d.srv.uploads.st.Get(d.extUpload(mine.Token, mayaOrigin))
	if rec.UserID != u.ID || rec.UserName != "Maya" {
		t.Fatalf("her upload is credited to %q %q", rec.UserID, rec.UserName)
	}
	page, _ := json.Marshal(ExtPageIn{URL: "https://example.com/pricing", Title: "Widget pricing", Screenshot: pagePNG(t)})
	if st, out, _ := d.ext("POST", "/ext/pages", mayaOrigin, mine.Token, page, nil); st != http.StatusOK {
		t.Fatalf("her page: %d %+v", st, out.Error)
	}
	var r models.Resource
	if err := d.a.DB.C(store.CResources).FindOne(ctx, bson.M{"workspaceId": d.a.WorkspaceID(), "kind": models.ResourceWebsite}).Decode(&r); err != nil {
		t.Fatal(err)
	}
	if r.CreatedBy.UserID != u.ID || r.CreatedBy.Via != models.ViaExtension {
		t.Fatalf("her snapshot is credited to %+v", r.CreatedBy)
	}
	// The owner's browser sends as the owner.
	if rec, _ := d.srv.uploads.st.Get(d.extUpload(theirs.Token, extOrigin)); rec.UserID != d.a.Cfg.User.ID {
		t.Fatalf("the owner's upload is credited to %q", rec.UserID)
	}

	// She sees only her own browsers; the owner sees everyone's, with whose each is.
	var js browserext.ExtensionStatusOut
	maya.op("extension.status", map[string]any{}, &js)
	if len(js.Browsers) != 1 || js.Browsers[0].ID != mine.ID || js.Owner || len(js.Everyone) != 0 {
		t.Fatalf("her list: %+v", js)
	}
	var os1 browserext.ExtensionStatusOut
	d.op("extension.status", map[string]any{}, &os1)
	if len(os1.Browsers) != 1 || os1.Browsers[0].ID != theirs.ID || !os1.Owner || len(os1.Everyone) != 2 {
		t.Fatalf("the owner's list: %+v", os1)
	}
	people := map[string]string{}
	for _, b := range os1.Everyone {
		people[b.ID] = b.Person
	}
	if people[mine.ID] != "Maya" || people[theirs.ID] != "Tester" {
		t.Fatalf("whose: %v", people)
	}

	// She can't unpair someone else's browser, or everyone's; she unpairs her own.
	for _, in := range []map[string]any{{"id": theirs.ID}, {"everyone": true}} {
		res, env, _ := maya.do("POST", "/api/ops/extension.revoke", in, true, nil)
		if res.StatusCode != 401 || env.Error.Details["reason"] != "owner_only" {
			t.Fatalf("a member unpairing others' %v: %d %+v", in, res.StatusCode, env.Error)
		}
	}

	// Turned off: her browser stops at once (plainly), and works again when she is back.
	d.op("users.disable", map[string]any{"user": u.ID}, nil)
	st, out, _ := d.ext("POST", "/ext/hello", mayaOrigin, mine.Token, []byte(`{}`), nil)
	if st != http.StatusForbidden || !strings.Contains(out.Error.Message, "turned off") {
		t.Fatalf("a turned-off account's browser: %d %+v", st, out.Error)
	}
	if st, _, _ := d.ext("POST", "/ext/uploads", mayaOrigin, mine.Token, []byte(`{"name":"x.mp4","size":10}`), nil); st != http.StatusForbidden {
		t.Fatalf("a turned-off account's browser sends: %d", st)
	}
	var everyone browserext.ExtensionStatusOut
	d.op("extension.status", map[string]any{}, &everyone)
	for _, b := range everyone.Everyone {
		if b.ID == mine.ID && !strings.Contains(b.Stopped, "turned off") {
			t.Fatalf("the owner's list doesn't say hers stopped: %+v", b)
		}
	}
	d.op("users.enable", map[string]any{"user": u.ID}, nil)
	st, out, _ = d.ext("POST", "/ext/hello", mayaOrigin, mine.Token, []byte(`{}`), nil)
	var hello ExtHelloOut
	_ = json.Unmarshal(out.Data, &hello)
	if st != http.StatusOK || hello.For != "Maya" {
		t.Fatalf("back on, and the extension hears whose it is: %d %+v %+v", st, hello, out.Error)
	}
	// Removed: it stops too.
	d.op("users.remove", map[string]any{"user": u.ID}, nil)
	if st, out, _ := d.ext("POST", "/ext/hello", mayaOrigin, mine.Token, []byte(`{}`), nil); st != http.StatusForbidden || out.Error.Message == "" {
		t.Fatalf("a removed account's browser: %d %+v", st, out.Error)
	}

	// An owner unpairs anyone's browser.
	var gone browserext.ExtensionRevokeOut
	d.op("extension.revoke", map[string]any{"id": mine.ID}, &gone)
	if gone.Revoked != 1 || len(gone.Everyone) != 1 {
		t.Fatalf("the owner unpairs hers: %+v", gone)
	}
	if st, _, _ := d.ext("POST", "/ext/hello", mayaOrigin, mine.Token, []byte(`{}`), nil); st != http.StatusUnauthorized {
		t.Fatalf("an unpaired browser: %d", st)
	}
	// Anyone unpairs all of their own (here the owner's one).
	var all browserext.ExtensionRevokeOut
	d.op("extension.revoke", map[string]any{"all": true}, &all)
	if all.Revoked != 1 || len(all.Browsers) != 0 {
		t.Fatalf("all of mine: %+v", all)
	}
}

// A member signed in unpairs her own browser (Remove in Your browsers).
func TestExtensionMemberUnpairsHerOwn(t *testing.T) {
	d, _, _ := newRemoteDash(t)
	d.signIn()
	d.op("auth.method.set", map[string]any{"method": "password", "email": "owner@example.com", "password": "tomato garden at dusk"}, nil)
	var inv auth.UserLinkOut
	d.op("users.invite", map[string]any{"email": "maya@example.com", "name": "Maya"}, &inv)
	maya := d.tailnet("", "")
	if res, _, raw := maya.do("POST", "/api/auth/link/"+inv.Link[strings.LastIndex(inv.Link, "/")+1:], map[string]any{"password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("accept: %s", raw)
	}
	var pair browserext.ExtensionPairOut
	maya.op("extension.pair", map[string]any{}, &pair)
	mine := d.extPair(pair.Code, mayaOrigin)
	var out browserext.ExtensionRevokeOut
	maya.op("extension.revoke", map[string]any{"id": mine.ID}, &out)
	if out.Revoked != 1 || len(out.Browsers) != 0 {
		t.Fatalf("she unpairs hers: %+v", out)
	}
	if st, _, _ := d.ext("POST", "/ext/hello", mayaOrigin, mine.Token, []byte(`{}`), nil); st != http.StatusUnauthorized {
		t.Fatalf("her unpaired browser: %d", st)
	}
}

// Pairings made before they had a person become the owner's (the person this
// installation was set up for): the file moves over, the old token keeps
// working and what it sends is the owner's; a code waiting from before pairs
// for the owner.
func TestExtensionPairingsFromBeforeAreTheOwners(t *testing.T) {
	d, _, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = byte(i + 7)
	}
	tok := browserext.TokenPrefix + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(tok))
	code := "K7QD3MXA"
	csum := sha256.Sum256([]byte(code))
	old := map[string]any{
		"browsers": []map[string]any{{"id": "ext_01jold00000000000000000000", "name": "Chrome", "origin": extOrigin, "pairedAt": time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339),
			"sent": 3, "scopes": []string{"footage", "pages"}, "tokenHash": hex.EncodeToString(sum[:])}},
		"code": map[string]any{"hash": hex.EncodeToString(csum[:]), "expiresAt": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339), "tries": 0},
	}
	b, _ := json.Marshal(old)
	path := filepath.Join(d.a.Cfg.StateDir(), "browser-extension.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := ops.CallTyped[browserext.ExtensionStatusOut](context.Background(), d.a, "extension.status", browserext.ExtensionStatusIn{}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Browsers) != 1 || st.Browsers[0].UserID != d.a.Cfg.User.ID || !st.CodeWaiting {
		t.Fatalf("the owner's after the move: %+v", st)
	}
	after, _ := os.ReadFile(path)
	var moved struct {
		Version  int `json:"version"`
		Browsers []struct {
			UserID string `json:"userId"`
		} `json:"browsers"`
		Codes []struct {
			UserID string `json:"userId"`
		} `json:"codes"`
		Code any `json:"code"`
	}
	_ = json.Unmarshal(after, &moved)
	if moved.Version != 2 || len(moved.Browsers) != 1 || moved.Browsers[0].UserID != d.a.Cfg.User.ID || len(moved.Codes) != 1 || moved.Codes[0].UserID != d.a.Cfg.User.ID || moved.Code != nil {
		t.Fatalf("the file after the move: %s", after)
	}
	// The old token works and sends as the owner.
	if rec, _ := d.srv.uploads.st.Get(d.extUpload(tok, extOrigin)); rec.UserID != d.a.Cfg.User.ID {
		t.Fatalf("an older pairing's upload is credited to %q", rec.UserID)
	}
	// The code from before pairs another browser for the owner.
	if p := d.extPair("K7QD-3MXA", "chrome-extension://dddddddddddddddddddddddddddddddd"); p.For != "Tester" {
		t.Fatalf("the waiting code pairs for %q", p.For)
	}
}
