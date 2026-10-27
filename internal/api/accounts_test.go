package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Accounts and sign-in (D183) through the whole dashboard stack: a fresh
// install is just the owner on this computer; the owner chooses email and
// password with their own sign-in; invites and reset links work once; a wrong
// email and a wrong password answer the same; a person from before accounts
// keeps their history when invited; only owners manage people.
func TestAccountsAndSignIn(t *testing.T) {
	d, _, _ := newRemoteDash(t)
	d.signIn() // the owner, on this computer, with a one-time link
	ctx := context.Background()
	elsewhere := d.tailnet("", "") // a browser on another computer (no Tailscale identity)

	// A fresh install: just the owner; other computers can't sign in.
	_, env, _ := elsewhere.do("GET", "/api/auth/state", nil, false, nil)
	var state AuthState
	_ = json.Unmarshal(env.Data, &state)
	if state.Method != models.AuthSingle || state.SignedIn || state.Here {
		t.Fatalf("fresh install: %+v", state)
	}
	if res, env, _ := elsewhere.do("GET", "/api/session", nil, false, nil); res.StatusCode != 401 || env.Error.Details["method"] != "single" {
		t.Fatalf("another computer before sign-in is set up: %d %+v", res.StatusCode, env.Error)
	}
	if res, _, _ := elsewhere.do("GET", "/auth?t="+strings.Repeat("a", 48), nil, false, nil); res.StatusCode == http.StatusFound {
		t.Fatal("a one-time link let another computer in")
	}
	// An earlier person: Maya signed in with Tailscale before accounts.
	mayaID := ids.New("usr")
	if err := d.a.DB.Insert(ctx, store.CUsers, models.User{ID: mayaID, WorkspaceID: d.a.WorkspaceID(), Name: "Maya", Logins: []string{"maya@example.com"}, Source: "tailscale", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	d.a.Audit(ctx, "project.create", "project", "prj_x", "", nil) // the owner did something
	mayaApp := *d.a
	mayaApp.Actor.UserID = mayaID
	mayaApp.Audit(ctx, "assistant.turn", "assistant_conversation", "asc_x", "", nil) // and Maya

	// Inviting needs a sign-in method first.
	if code, env := d.do("POST", "/api/ops/users.invite", map[string]any{"email": "maya@example.com"}, true); code == 200 || env.Error.Details["reason"] != "no_method" {
		t.Fatalf("invite before a method: %d %+v", code, env.Error)
	}
	// The owner chooses email and password, giving their own sign-in.
	if code, env := d.do("POST", "/api/ops/auth.method.set", map[string]any{"method": "password", "email": "owner@example.com", "password": "short"}, true); code == 200 || env.Error.Details["reason"] != "weak_password" {
		t.Fatalf("a weak owner password: %d %+v", code, env.Error)
	}
	d.op("auth.method.set", map[string]any{"method": "password", "email": "Owner@Example.com", "password": "tomato garden at dusk"}, nil)
	if auth.Method(ctx, d.a) != models.AuthPassword {
		t.Fatal("method not set")
	}
	// The owner's session says the email they just gave, not the one from setup.
	var ownerSess Session
	_, env = d.do("GET", "/api/session", nil, false)
	_ = json.Unmarshal(env.Data, &ownerSess)
	if !strings.EqualFold(ownerSess.User.Email, "owner@example.com") {
		t.Fatalf("owner's session email: %+v", ownerSess.User)
	}

	// Invite Maya by email: her earlier record becomes the account.
	var inv auth.UserLinkOut
	d.op("users.invite", map[string]any{"email": "Maya@example.com", "name": "Maya Lindqvist"}, &inv)
	if inv.Account.ID != mayaID || !inv.Earlier || inv.Account.State != models.UserInvited || !strings.Contains(inv.Link, "/welcome/") {
		t.Fatalf("invite: %+v", inv)
	}
	token := inv.Link[strings.LastIndex(inv.Link, "/")+1:]
	maya := d.tailnet("", "")
	_, env, _ = maya.do("GET", "/api/auth/link/"+token, nil, false, nil)
	var info LinkInfo
	_ = json.Unmarshal(env.Data, &info)
	if info.Kind != "invite" || info.Email != "Maya@example.com" {
		t.Fatalf("link info: %+v", info)
	}
	// A weak password doesn't use up the link; a good one signs her in once.
	if res, env, _ := maya.do("POST", "/api/auth/link/"+token, map[string]any{"password": "password12345"}, true, nil); res.StatusCode == 200 || env.Error.Details["reason"] != "weak_password" {
		t.Fatalf("weak: %d %+v", res.StatusCode, env.Error)
	}
	if res, _, raw := maya.do("POST", "/api/auth/link/"+token, map[string]any{"password": "sunflowers by the gate"}, false, nil); res.StatusCode == 200 {
		t.Fatalf("a link followed without the dashboard's header: %s", raw)
	}
	if res, _, raw := maya.do("POST", "/api/auth/link/"+token, map[string]any{"password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("accept: %s", raw)
	}
	var sess Session
	_, env, _ = maya.do("GET", "/api/session", nil, false, nil)
	_ = json.Unmarshal(env.Data, &sess)
	if sess.User.ID != mayaID || sess.User.Name != "Maya Lindqvist" || sess.Access.SignedInWith != "password" {
		t.Fatalf("Maya signed in as her earlier self: %+v", sess)
	}
	if res, _, _ := d.tailnet("", "").do("POST", "/api/auth/link/"+token, map[string]any{"password": "sunflowers by the gate"}, true, nil); res.StatusCode == 200 {
		t.Fatal("an invite link worked twice")
	}

	// Members don't manage people.
	if res, env, _ := maya.do("POST", "/api/ops/users.invite", map[string]any{"email": "bob@example.com"}, true, nil); res.StatusCode == 200 || env.Error.Details["reason"] != "owner_only" {
		t.Fatalf("a member inviting: %d %+v", res.StatusCode, env.Error)
	}
	if res, env, _ := maya.do("POST", "/api/ops/auth.method.set", map[string]any{"method": "single"}, true, nil); res.StatusCode == 200 || env.Error.Details["reason"] != "owner_only" {
		t.Fatalf("a member changing the method: %d %+v", res.StatusCode, env.Error)
	}

	// Signing in: a wrong email and a wrong password answer the same.
	other := d.tailnet("", "")
	login := func(b *browser, email, pw string) (int, envelope) {
		res, env, _ := b.do("POST", "/api/auth/login", map[string]any{"email": email, "password": pw}, true, nil)
		return res.StatusCode, env
	}
	c1, e1 := login(other, "maya@example.com", "not her password at all")
	c2, e2 := login(other, "nobody@example.com", "not her password at all")
	if c1 != 401 || c2 != 401 || e1.Error.Message != e2.Error.Message || e1.Error.Details["reason"] != "bad_credentials" {
		t.Fatalf("wrong password %d %+v / unknown email %d %+v", c1, e1.Error, c2, e2.Error)
	}
	if c, _ := login(other, "MAYA@example.com", "sunflowers by the gate"); c != 200 {
		t.Fatalf("sign in: %d", c)
	}
	if res, _, _ := other.do("POST", "/api/auth/logout", nil, true, nil); res.StatusCode != 200 {
		t.Fatal("logout")
	}
	if res, _, _ := other.do("GET", "/api/session", nil, false, nil); res.StatusCode != 401 {
		t.Fatal("still signed in after signing out")
	}
	// Plain http on another computer: no passwords.
	if res, env, _ := d.tailnet("", "").do("POST", "/api/auth/login", map[string]any{"email": "maya@example.com", "password": "x"}, true, map[string]string{"X-Forwarded-Proto": "http"}); res.StatusCode == 200 || env.Error.Details["reason"] != "not_https" {
		t.Fatalf("http: %d %+v", res.StatusCode, env.Error)
	}

	// Throttling: after five wrong passwords each try waits.
	_, _ = d.a.DB.C(store.CAuthFailures).DeleteMany(ctx, bson.M{})
	var last envelope
	for i := 0; i < 7; i++ {
		_, last = login(d.tailnet("", ""), "maya@example.com", "wrong guess number one")
	}
	if last.Error.Details["reason"] != "throttled" {
		t.Fatalf("throttled: %+v", last.Error)
	}
	if c, env := login(d.tailnet("", ""), "maya@example.com", "sunflowers by the gate"); c == 200 || env.Error.Details["reason"] != "throttled" {
		t.Fatalf("even the right password waits: %d", c)
	}
	_, _ = d.a.DB.C(store.CAuthFailures).DeleteMany(ctx, bson.M{})

	// The owner makes a reset link: it works once, and signs her out everywhere else.
	var reset auth.UserLinkOut
	d.op("users.resetLink", map[string]any{"user": "maya@example.com"}, &reset)
	if reset.Kind != "reset" || reset.ExpiresAt.Sub(time.Now()) > time.Hour+time.Minute {
		t.Fatalf("reset link: %+v", reset)
	}
	rt := reset.Link[strings.LastIndex(reset.Link, "/")+1:]
	time.Sleep(5 * time.Millisecond)
	fresh := d.tailnet("", "")
	if res, _, raw := fresh.do("POST", "/api/auth/link/"+rt, map[string]any{"password": "a brand new garden path"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("reset: %s", raw)
	}
	if res, _, _ := maya.do("GET", "/api/session", nil, false, nil); res.StatusCode != 401 {
		t.Fatal("her other browser is still signed in after the reset")
	}
	if c, _ := login(d.tailnet("", ""), "maya@example.com", "a brand new garden path"); c != 200 {
		t.Fatal("the new password doesn't work")
	}
	// Changing her own password keeps this browser signed in and signs out the others.
	me, other2 := d.tailnet("", ""), d.tailnet("", "")
	login(me, "maya@example.com", "a brand new garden path")
	login(other2, "maya@example.com", "a brand new garden path")
	time.Sleep(5 * time.Millisecond)
	me.op("account.password", map[string]any{"current": "a brand new garden path", "new": "rosemary under the window"}, nil)
	if res, _, _ := me.do("GET", "/api/session", nil, false, nil); res.StatusCode != 200 {
		t.Fatal("changing your password signed this browser out")
	}
	if res, _, _ := other2.do("GET", "/api/session", nil, false, nil); res.StatusCode != 401 {
		t.Fatal("her other browser is still signed in after the change")
	}
	// Changing how people sign in always asks for the owner's current password.
	if code, env := d.do("POST", "/api/ops/auth.method.set", map[string]any{"method": "password", "email": "owner@example.com", "password": "an entirely new phrase"}, true); code == 200 || env.Error.Details["reason"] != "bad_current" {
		t.Fatalf("method change without the current password: %d %+v", code, env.Error)
	}
	// Turning her off signs her out; the last owner can't be turned off.
	d.op("users.disable", map[string]any{"user": mayaID}, nil)
	if res, _, _ := me.do("GET", "/api/session", nil, false, nil); res.StatusCode != 401 {
		t.Fatal("a turned-off account is still signed in")
	}
	if code, env := d.do("POST", "/api/ops/users.disable", map[string]any{"user": d.a.Cfg.User.ID}, true); code == 200 || env.Error.Details["reason"] != "last_owner" {
		t.Fatalf("the last owner: %d %+v", code, env.Error)
	}
	// The password is stored hashed, never in users.
	var cred bson.M
	_ = d.a.DB.C(store.CUserCredentials).FindOne(ctx, bson.M{"_id": mayaID}).Decode(&cred)
	if h, _ := cred["hash"].(string); !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("credential: %v", cred)
	}
	var raw bson.M
	_ = d.a.DB.C(store.CUsers).FindOne(ctx, bson.M{"_id": mayaID}).Decode(&raw)
	for k := range raw {
		if strings.Contains(strings.ToLower(k), "password") && k != "passwordSetAt" || k == "hash" {
			t.Fatalf("a password field in users: %s", k)
		}
	}
}
