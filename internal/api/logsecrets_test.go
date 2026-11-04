package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeoidc"
)

// A value someone sends MetaRush is taken for a secret — hidden wherever it
// appears in the activity log — only once MetaRush finds it is real: a
// sign-in's ticket, state or code, an invite or reset link, a browser
// extension's pairing code or token, a password that signs someone in.
// Before, a made-up value was taken for one as soon as it arrived, so one
// equal to an ordinary word (here a person's email) blanked that word out of
// every later entry.

// madeUp is a person's email that a visitor sends as a ticket, state, code,
// link, pairing or password; the log says it later, when they're invited.
const madeUp = "rowan.vale@example.com"

// hidden reports a value the dashboard's activity log hides (beyond the
// shapes every log hides).
func hidden(d *dash, v string) bool {
	return d.srv.App.Log.Redactor().String(v) != logbook.NewRedactor().String(v)
}

// exportSays reports whether a workspace export made now — by the owner at
// this computer, as the daemon makes one — keeps v as written in a
// collection's records (an export scrubs every secret this process knows).
func exportSays(t *testing.T, d *dash, coll, v string) bool {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "archive")
	if _, err := ops.CallTyped[ops.WorkspaceExportOut](context.Background(), d.a, "workspace.export", ops.WorkspaceExportIn{To: dir}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "collections", coll+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Contains(string(b), v)
}

// logSays reports whether the activity log, written out, says v.
func logSays(t *testing.T, d *dash, v string) bool {
	t.Helper()
	ctx := context.Background()
	d.a.Log.Flush(ctx)
	var entries []bson.M
	cur, err := d.a.DB.C(store.CLogEntries).Find(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.All(ctx, &entries); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(entries)
	return strings.Contains(string(b), v)
}

func TestMadeUpSignInValuesLeaveTheLogAlone(t *testing.T) {
	r := newSSODash(t)
	owner := fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true), Name: "Sam Lee"}
	r.setUp(nil)
	r.testAndSwitch(owner)
	b := r.here()

	// A ticket, a state and a code nobody made, and an invite nobody sent.
	b.get(sso.BeginPath + "?t=" + url.QueryEscape(madeUp))
	b.get(sso.CallbackPath + "?state=" + url.QueryEscape(madeUp) + "&code=" + url.QueryEscape(madeUp))
	if res, _, _ := b.do("POST", "/api/auth/sso/start", map[string]any{"invite": madeUp}, true, nil); res.StatusCode == 200 {
		t.Fatal("a made-up invite started a sign-in")
	}
	// This browser's own sign-in, answered with a made-up code: the provider
	// refuses it, so it is nobody's code.
	res, env, raw := b.do("POST", "/api/auth/sso/start", map[string]any{}, true, nil)
	if res.StatusCode != 200 {
		t.Fatalf("start: %d %s", res.StatusCode, raw)
	}
	var start SSOStartResult
	_ = json.Unmarshal(env.Data, &start)
	cb, ok := r.toProvider(b, start.URL, owner)
	if !ok {
		t.Fatal("begin didn't reach the provider")
	}
	cu, _ := url.Parse(cb)
	q := cu.Query()
	q.Set("code", madeUp)
	cu.RawQuery = q.Encode()
	if res, _ := b.get(cu.String()); !strings.HasPrefix(res.Header.Get("Location"), "/signin?sso=") {
		t.Fatalf("a made-up code: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if _, code := sessionOf(t, b); code == http.StatusOK {
		t.Fatal("a made-up code signed someone in")
	}
	// A browser extension's made-up pairing code and token.
	pair, _ := json.Marshal(ExtPairIn{Code: madeUp, Name: "Chrome"})
	if st, _, _ := r.ext("POST", "/ext/pair", extOrigin, "", pair, nil); st == http.StatusOK {
		t.Fatal("a made-up pairing code paired")
	}
	if st, _, _ := r.ext("POST", "/ext/hello", extOrigin, madeUp, []byte("{}"), nil); st == http.StatusOK {
		t.Fatal("a made-up token was let in")
	}

	if hidden(r.dash, madeUp) || logbook.Remembered(madeUp) {
		t.Fatalf("%q was taken for a secret", madeUp)
	}
	// So the log keeps saying it when that person is invited, and a workspace
	// export keeps a project named after them as it is (an export scrubs
	// every secret this process knows from what it writes).
	r.op("users.invite", map[string]any{"email": madeUp}, nil)
	if !logSays(t, r.dash, madeUp) {
		t.Fatalf("the activity log lost %q", madeUp)
	}
	r.op("project.create", map[string]any{"name": "Interview with " + madeUp}, nil)
	if !exportSays(t, r.dash, store.CProjects, madeUp) {
		t.Fatalf("the workspace export lost %q", madeUp)
	}

	// A real sign-in's ticket, state and code are still kept out of the log.
	ob := r.here()
	res, env, raw = ob.do("POST", "/api/auth/sso/start", map[string]any{}, true, nil)
	if res.StatusCode != 200 {
		t.Fatalf("start: %d %s", res.StatusCode, raw)
	}
	_ = json.Unmarshal(env.Data, &start)
	su, _ := url.Parse(start.URL)
	ticket := su.Query().Get("t")
	cb, ok = r.toProvider(ob, start.URL, owner)
	if !ok {
		t.Fatal("begin didn't reach the provider")
	}
	cu, _ = url.Parse(cb)
	state, code := cu.Query().Get("state"), cu.Query().Get("code")
	if res, _ := ob.get(cb); res.StatusCode != http.StatusFound {
		t.Fatalf("the owner's sign-in: %d", res.StatusCode)
	}
	if s, code := sessionOf(t, ob); code != http.StatusOK || s.User.ID == "" {
		t.Fatalf("the owner isn't signed in: %d", code)
	}
	for name, v := range map[string]string{"ticket": ticket, "state": state, "code": code} {
		if v == "" || !hidden(r.dash, v) {
			t.Errorf("a real sign-in's %s isn't kept out of the log", name)
		}
	}
}

func TestMadeUpPasswordsAndLinksLeaveTheLogAlone(t *testing.T) {
	d, _, _ := newRemoteDash(t)
	d.signIn() // the owner, at this computer, with a one-time link
	d.srv.signInLimit = newRateLimit(100000)
	d.op("auth.method.set", map[string]any{"method": "password", "email": "owner@example.com", "password": "tomato garden at dusk"}, nil)
	b := d.here()

	// A password that signs nobody in; a link nobody made, and a password for it.
	if res, _, _ := b.do("POST", "/api/auth/login", map[string]any{"email": "owner@example.com", "password": madeUp}, true, nil); res.StatusCode == 200 {
		t.Fatal("a wrong password signed in")
	}
	b.do("GET", "/api/auth/link/"+madeUp, nil, false, nil)
	if res, _, _ := b.do("POST", "/api/auth/link/"+madeUp, map[string]any{"password": madeUp}, true, nil); res.StatusCode == 200 {
		t.Fatal("a made-up link was used")
	}
	if hidden(d, madeUp) || logbook.Remembered(madeUp) {
		t.Fatalf("%q was taken for a secret", madeUp)
	}
	var inv auth.UserLinkOut
	d.op("users.invite", map[string]any{"email": madeUp, "name": "Rowan Vale"}, &inv)
	if !logSays(t, d, madeUp) {
		t.Fatalf("the activity log lost %q", madeUp)
	}

	// A real link, once used, and the password chosen with it are kept out.
	token, pw := inv.Link[strings.LastIndex(inv.Link, "/")+1:], "lanterns over the harbour"
	if res, _, raw := d.here().do("POST", "/api/auth/link/"+token, map[string]any{"password": pw}, true, nil); res.StatusCode != 200 {
		t.Fatalf("accept: %d %s", res.StatusCode, raw)
	}
	if !hidden(d, token) || !hidden(d, pw) {
		t.Error("a real link or the password chosen with it isn't kept out of the log")
	}
}
