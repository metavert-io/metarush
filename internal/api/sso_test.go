package api

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sessions"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeoidc"
)

// Signing in with an organization's account (D425–D434) through the whole
// dashboard stack, against a stand-in provider on this computer (keys made at
// run time; never a real provider): the setup, the test sign-in that must pass
// before the switch, who may come in, every check on the way back, sessions,
// and the same over the address for other computers.

type ssoRig struct {
	*dash
	p      *fakeoidc.Server
	secret string
}

func newSSODash(t *testing.T) *ssoRig {
	t.Helper()
	keyring.MockInit() // never this computer's keychain
	sso.Forget()
	t.Cleanup(sso.Forget)
	d, _, _ := newRemoteDash(t)
	d.signIn()                               // the owner, at this computer, with a one-time link
	d.srv.signInLimit = newRateLimit(100000) // every test request comes from 127.0.0.1
	secret := sso.Random(24)
	p := fakeoidc.New("metarush-app", secret)
	t.Cleanup(p.Close)
	local, public := auth.SSOCallbacks(d.a.Cfg)
	p.Redirects = []string{local, public}
	if !strings.HasPrefix(local, "http://localhost:") || public != "https://"+publicHost+"/auth/sso/callback" {
		t.Fatalf("callbacks: %s %s", local, public)
	}
	return &ssoRig{dash: d, p: p, secret: secret}
}

// here is a browser on this computer at localhost, where organization
// sign-ins come back.
func (d *dash) here() *browser {
	u, _ := url.Parse(d.base)
	return &browser{t: d.t, d: d, host: "localhost:" + u.Port(), hdr: map[string]string{}, cookies: map[string]string{}}
}

// get opens an address of this dashboard (any of its names) in a browser.
func (b *browser) get(raw string) (*http.Response, []byte) {
	b.t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		b.t.Fatal(err)
	}
	res, _, body := b.do("GET", u.RequestURI(), nil, false, nil)
	return res, body
}

// signInWith runs a sign-in in b as who: the sign-in page's start, the begin
// address, the provider's page (answering at once), the callback. It returns
// the callback's answer.
func (r *ssoRig) signInWith(b *browser, who fakeoidc.Person, start map[string]any) *http.Response {
	r.t.Helper()
	res, env, raw := b.do("POST", "/api/auth/sso/start", start, true, nil)
	if res.StatusCode != 200 {
		r.t.Fatalf("start: %d %s", res.StatusCode, raw)
	}
	var out SSOStartResult
	_ = json.Unmarshal(env.Data, &out)
	return r.follow(b, out.URL, who)
}

// follow opens a begin address in b and goes through the provider.
func (r *ssoRig) follow(b *browser, begin string, who fakeoidc.Person) *http.Response {
	r.t.Helper()
	cb, ok := r.toProvider(b, begin, who)
	if !ok {
		r.t.Fatalf("begin didn't reach the provider")
	}
	res, _ := b.get(cb)
	return res
}

// toProvider opens a begin address and returns where the provider sends the
// browser back (the callback with code and state).
func (r *ssoRig) toProvider(b *browser, begin string, who fakeoidc.Person) (string, bool) {
	r.t.Helper()
	bu, _ := url.Parse(begin)
	if b.host != bu.Host {
		r.t.Fatalf("begin address %s for a browser at %s", begin, b.host)
	}
	res, _ := b.get(begin)
	if res.StatusCode != http.StatusFound {
		return "", false
	}
	r.p.Next = &who
	pres, err := http.DefaultTransport.RoundTrip(mustGet(res.Header.Get("Location")))
	if err != nil {
		r.t.Fatal(err)
	}
	pres.Body.Close()
	if pres.StatusCode != http.StatusFound {
		r.t.Fatalf("the provider's page: %d", pres.StatusCode)
	}
	return pres.Header.Get("Location"), true
}

func mustGet(u string) *http.Request {
	req, _ := http.NewRequest("GET", u, nil)
	return req
}

func maya(verified bool) fakeoidc.Person {
	return fakeoidc.Person{Sub: "sub-maya", Email: "maya@example.com", EmailVerified: fakeoidc.Verified(verified), Name: "Maya Lindqvist"}
}

func (r *ssoRig) setUp(extra map[string]any) auth.SSOStatusOut {
	r.t.Helper()
	in := map[string]any{"provider": "oidc", "issuer": r.p.Issuer, "clientId": "metarush-app", "clientSecret": r.secret, "label": "Test provider"}
	for k, v := range extra {
		in[k] = v
	}
	var st auth.SSOStatusOut
	r.op("auth.sso.set", in, &st)
	return st
}

// testAndSwitch is the owner's test sign-in on this computer, then the switch.
func (r *ssoRig) testAndSwitch(owner fakeoidc.Person) {
	r.t.Helper()
	var test auth.SSOTestOut
	r.op("auth.sso.test", map[string]any{}, &test)
	res, body := r.testIn(r.here(), test.URL, owner)
	if res.StatusCode != 200 || !strings.Contains(string(body), "The test sign-in worked") {
		r.t.Fatalf("test sign-in: %d %s", res.StatusCode, body)
	}
	r.op("auth.method.set", map[string]any{"method": "sso"}, nil)
}

func (r *ssoRig) testIn(b *browser, begin string, who fakeoidc.Person) (*http.Response, []byte) {
	r.t.Helper()
	cb, ok := r.toProvider(b, begin, who)
	if !ok {
		r.t.Fatal("the test's begin address didn't reach the provider")
	}
	return b.get(cb)
}

func sessionOf(t *testing.T, b *browser) (Session, int) {
	t.Helper()
	res, env, _ := b.do("GET", "/api/session", nil, false, nil)
	var s Session
	_ = json.Unmarshal(env.Data, &s)
	return s, res.StatusCode
}

func TestOrganizationSignInIsTestedBeforeTheSwitch(t *testing.T) {
	r := newSSODash(t)
	ctx := context.Background()
	owner := fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true), Name: "Sam Lee"}

	// Members never set it up; another computer's owner neither (only here).
	st := r.setUp(nil)
	if st.Next == nil || st.InUse || st.CanSwitch || !st.Secret.Set || strings.Contains(ssoJSON(st), r.secret) || st.Secret.Masked != "••••"+r.secret[len(r.secret)-4:] {
		t.Fatalf("after set: %+v", st)
	}
	if !strings.HasPrefix(st.Callbacks.Local, "http://localhost:") || st.Callbacks.Public == "" {
		t.Fatalf("callbacks: %+v", st.Callbacks)
	}
	// A bad provider address keeps nothing.
	if code, env := r.do("POST", "/api/ops/auth.sso.set", map[string]any{"provider": "oidc", "issuer": "http://127.0.0.1:1", "clientId": "x"}, true); code == 200 {
		t.Fatalf("an unreachable provider was kept: %+v", env)
	}
	// Switching before a test sign-in is refused.
	if code, env := r.do("POST", "/api/ops/auth.method.set", map[string]any{"method": "sso"}, true); code == 200 || env.Error.Details["reason"] != "not_tested" {
		t.Fatalf("switch without a test: %d %+v", code, env.Error)
	}
	// A test that fails (the person says no at the provider) is recorded and
	// still doesn't allow the switch.
	var test auth.SSOTestOut
	r.op("auth.sso.test", map[string]any{}, &test)
	r.p.Deny = true
	if res, body := r.testIn(r.here(), test.URL, owner); res.StatusCode != 200 || !strings.Contains(html.UnescapeString(string(body)), "didn't work") {
		t.Fatalf("a denied test: %d %s", res.StatusCode, body)
	}
	if code, env := r.do("POST", "/api/ops/auth.method.set", map[string]any{"method": "sso"}, true); code == 200 || env.Error.Details["reason"] != "not_tested" {
		t.Fatalf("switch after a failed test: %d %+v", code, env.Error)
	}
	// The test's begin address works once, in a browser on this computer.
	r.op("auth.sso.test", map[string]any{}, &test)
	if res, _ := r.tailnet("", "").get("https://" + publicHost + "/auth/sso/begin?" + mustQuery(test.URL)); res.StatusCode == http.StatusFound {
		t.Fatal("a test began from another computer")
	}
	r.op("auth.sso.test", map[string]any{}, &test)
	r.testAndSwitchWith(test.URL, owner)
	if _, err := os.Stat(filepath.Join(r.a.Cfg.StateDir(), "sso", "test.json")); err == nil {
		t.Fatal("a test switched twice")
	}
	if auth.Method(ctx, r.a) != models.AuthSSO {
		t.Fatal("not switched")
	}
	// The owner's account is linked to the account they tested with.
	ownerRec, _ := auth.GetUser(ctx, r.a, r.a.Cfg.User.ID)
	if len(ownerRec.SSO) != 1 || ownerRec.Role != models.RoleOwner || ownerRec.EmailKey != "sam@example.com" {
		t.Fatalf("owner: %+v", ownerRec)
	}
	// A new browser signs the owner in with the provider.
	b := r.here()
	res := r.signInWith(b, owner, map[string]any{"next": "/settings/people"})
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/settings/people" {
		t.Fatalf("owner sign-in: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	sess, code := sessionOf(t, b)
	if code != 200 || sess.User.ID != r.a.Cfg.User.ID || sess.Access.SignedInWith != "sso" || sess.Access.Provider != "Test provider" || !sess.Owner {
		t.Fatalf("owner session: %d %+v", code, sess)
	}
	_, env, _ := b.do("GET", "/api/auth/state", nil, false, nil)
	var as AuthState
	_ = json.Unmarshal(env.Data, &as)
	if as.Method != "sso" || as.SSO == nil || as.SSO.Title != "Test provider" || as.Password {
		t.Fatalf("auth state: %+v", as)
	}

	// Lockout safety: whatever the provider does, the one-time link at this
	// computer still opens MetaRush as the owner, and auth reset goes back to
	// just the owner.
	r.p.Close()
	sso.Forget()
	r.signIn()
	if code, _ := r.do("GET", "/api/session", nil, false); code != 200 {
		t.Fatal("the one-time link stopped working with the provider gone")
	}
	if _, err := auth.Reset(ctx, r.a); err != nil {
		t.Fatal(err)
	}
	if auth.Method(ctx, r.a) != models.AuthSingle {
		t.Fatal("reset")
	}
	if _, code := sessionOf(t, b); code != 401 {
		t.Fatal("an organization session outlived the reset")
	}
}

func (r *ssoRig) testAndSwitchWith(begin string, owner fakeoidc.Person) {
	r.t.Helper()
	if res, body := r.testIn(r.here(), begin, owner); res.StatusCode != 200 || !strings.Contains(string(body), "The test sign-in worked") {
		r.t.Fatalf("test: %d %s", res.StatusCode, body)
	}
	var st auth.SSOStatusOut
	r.op("auth.sso.status", map[string]any{}, &st)
	if !st.CanSwitch || st.Test == nil || !st.Test.OK || st.Test.Email != owner.Email || st.Test.ForSetup != "next" {
		r.t.Fatalf("status after the test: %+v %+v", st, st.Test)
	}
	r.op("auth.method.set", map[string]any{"method": "sso"}, nil)
}

func mustQuery(raw string) string {
	u, _ := url.Parse(raw)
	return u.RawQuery
}

func TestOrganizationSignInLetsInOnlyPeopleOfTheWorkspace(t *testing.T) {
	r := newSSODash(t)
	ctx := context.Background()
	r.setUp(map[string]any{"domain": "example.org"})
	r.testAndSwitch(fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true)})
	users := func() int64 {
		n, _ := r.a.DB.C(store.CUsers).CountDocuments(ctx, bson.M{"workspaceId": r.a.WorkspaceID()})
		return n
	}
	before := users()

	// Someone with an account at the provider but none here: refused, nobody made.
	res := r.signInWith(r.here(), maya(true), nil)
	if loc := res.Header.Get("Location"); !strings.HasPrefix(loc, "/signin?") || !strings.Contains(loc, "sso=not_member") || !strings.Contains(loc, "as=maya%40example.com") {
		t.Fatalf("a stranger: %s", loc)
	}
	if users() != before {
		t.Fatal("a stranger was made a person")
	}
	// Invited: the first sign-in links the account (an email the provider checked).
	var inv auth.UserLinkOut
	r.op("users.invite", map[string]any{"email": "Maya@Example.com"}, &inv)
	// An unchecked email doesn't count.
	if res := r.signInWith(r.here(), maya(false), nil); !strings.Contains(res.Header.Get("Location"), "sso=not_member") {
		t.Fatalf("an unverified email: %s", res.Header.Get("Location"))
	}
	b := r.here()
	if res := r.signInWith(b, maya(true), nil); res.StatusCode != http.StatusFound || !strings.HasPrefix(res.Header.Get("Location"), "/") || strings.Contains(res.Header.Get("Location"), "signin") {
		t.Fatalf("invited: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	sess, _ := sessionOf(t, b)
	if sess.User.ID != inv.Account.ID || sess.Owner {
		t.Fatalf("Maya's session: %+v", sess)
	}
	u, _ := auth.GetUser(ctx, r.a, inv.Account.ID)
	if u.State != models.UserActive || len(u.SSO) != 1 {
		t.Fatalf("Maya: %+v", u)
	}
	// Another account at the provider with Maya's email doesn't take hers over.
	impostor := maya(true)
	impostor.Sub = "sub-someone-else"
	if res := r.signInWith(r.here(), impostor, nil); !strings.Contains(res.Header.Get("Location"), "sso=linked_elsewhere") {
		t.Fatalf("same email, other account: %s", res.Header.Get("Location"))
	}
	// Linked: her account signs her in even when its email changes.
	moved := maya(true)
	moved.Email = "maya.l@example.net"
	b2 := r.here()
	if res := r.signInWith(b2, moved, nil); strings.Contains(res.Header.Get("Location"), "signin") {
		t.Fatalf("by account: %s", res.Header.Get("Location"))
	}
	// The allowed domain: a new member, and the owners hear of it.
	carol := fakeoidc.Person{Sub: "sub-carol", Email: "carol@example.org", EmailVerified: fakeoidc.Verified(true), Name: "Carol Diaz"}
	b3 := r.here()
	if res := r.signInWith(b3, carol, nil); res.Header.Get("Location") != "/?welcome=1" {
		t.Fatalf("domain join: %s", res.Header.Get("Location"))
	}
	c, _ := auth.FindByEmail(ctx, r.a, "carol@example.org")
	if c == nil || c.Role != models.RoleMember || c.State != models.UserActive || c.Source != "sso" {
		t.Fatalf("Carol: %+v", c)
	}
	if n, _ := r.a.DB.C(store.CNotifications).CountDocuments(ctx, bson.M{"kind": models.NotifyPersonJoined, "userId": r.a.Cfg.User.ID}); n != 1 {
		t.Fatalf("owners told %d times", n)
	}
	// A subdomain, or an unchecked email at the domain, doesn't join.
	for _, p := range []fakeoidc.Person{
		{Sub: "s1", Email: "dan@mail.example.org", EmailVerified: fakeoidc.Verified(true)},
		{Sub: "s2", Email: "erin@example.org", EmailVerified: fakeoidc.Verified(false)},
		{Sub: "s3", Email: "fay@example.org"},
	} {
		if res := r.signInWith(r.here(), p, nil); !strings.Contains(res.Header.Get("Location"), "sso=not_member") {
			t.Errorf("%s joined: %s", p.Email, res.Header.Get("Location"))
		}
	}
	// Turned off: refused, and her session ends.
	r.op("users.disable", map[string]any{"user": inv.Account.ID}, nil)
	if _, code := sessionOf(t, b); code != 401 {
		t.Fatal("a turned-off person kept her session")
	}
	if res := r.signInWith(r.here(), moved, nil); !strings.Contains(res.Header.Get("Location"), "sso=disabled") {
		t.Fatalf("turned off: %s", res.Header.Get("Location"))
	}
	// Removed: refused; invited again, a new account of hers is linked afresh.
	r.op("users.enable", map[string]any{"user": inv.Account.ID}, nil)
	r.op("users.remove", map[string]any{"user": inv.Account.ID}, nil)
	if res := r.signInWith(r.here(), moved, nil); !strings.Contains(res.Header.Get("Location"), "sso=not_member") && !strings.Contains(res.Header.Get("Location"), "sso=removed") {
		t.Fatalf("removed: %s", res.Header.Get("Location"))
	}
	r.op("users.invite", map[string]any{"email": "maya@example.com"}, &inv)
	if res := r.signInWith(r.here(), impostor, nil); strings.Contains(res.Header.Get("Location"), "signin") {
		t.Fatalf("invited again: %s", res.Header.Get("Location"))
	}

	// Joining with an invite link: the provider's email must be the invited one.
	var link auth.UserLinkOut
	r.op("users.invite", map[string]any{"email": "gus@example.com"}, &link)
	token := link.Link[strings.LastIndex(link.Link, "/")+1:]
	_, env, _ := r.here().do("GET", "/api/auth/link/"+token, nil, false, nil)
	var info LinkInfo
	_ = json.Unmarshal(env.Data, &info)
	if info.SSO == nil || info.Password {
		t.Fatalf("link info: %+v", info)
	}
	gusOther := fakeoidc.Person{Sub: "sub-gus", Email: "gus@example.net", EmailVerified: fakeoidc.Verified(true)}
	if res := r.signInWith(r.here(), gusOther, map[string]any{"invite": token}); !strings.Contains(res.Header.Get("Location"), "sso=invite_email") {
		t.Fatalf("an invite with another email: %s", res.Header.Get("Location"))
	}
	gus := fakeoidc.Person{Sub: "sub-gus", Email: "gus@example.com"} // no email_verified at all: the link vouches
	if res := r.signInWith(r.here(), gus, map[string]any{"invite": token}); res.Header.Get("Location") != "/?welcome=1" {
		t.Fatalf("joining with the link: %s", res.Header.Get("Location"))
	}
	if res, _, _ := r.here().do("POST", "/api/auth/sso/start", map[string]any{"invite": token}, true, nil); res.StatusCode == 200 {
		t.Fatal("an invite link worked twice")
	}
	// No passwords here: a link can't be used to choose one.
	if res, env, _ := r.here().do("POST", "/api/auth/link/"+token, map[string]any{"password": "sunflowers by the gate"}, true, nil); res.StatusCode == 200 || env.Error.Details["reason"] == nil {
		t.Fatalf("a password through an invite: %d", res.StatusCode)
	}
}

func TestOrganizationSignInRefusesAnswersThatDontHoldUp(t *testing.T) {
	r := newSSODash(t)
	r.setUp(map[string]any{"domain": "example.com"})
	r.testAndSwitch(fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.org", EmailVerified: fakeoidc.Verified(true)})
	ok := func(b *browser) bool { _, code := sessionOf(t, b); return code == 200 }

	// Every bent answer: no session, back to the sign-in page with the reason.
	for name, bend := range map[string]func(map[string]any){
		"another app":   func(c map[string]any) { c["aud"] = "someone-else" },
		"another issue": func(c map[string]any) { c["iss"] = "https://login.example.net" },
		"expired":       func(c map[string]any) { c["exp"] = time.Now().Add(-time.Hour).Unix() },
		"wrong nonce":   func(c map[string]any) { c["nonce"] = sso.Random(32) },
		"no nonce":      func(c map[string]any) { delete(c, "nonce") },
	} {
		b := r.here()
		r.p.Bend = bend
		res := r.signInWith(b, maya(true), nil)
		if !strings.HasPrefix(res.Header.Get("Location"), "/signin?sso=") || ok(b) {
			t.Errorf("%s: %s", name, res.Header.Get("Location"))
		}
	}
	// A signature by a key the provider never published.
	r.p.SignWith = fakeoidc.NewKey("RS256")
	if b := r.here(); !strings.Contains(r.signInWith(b, maya(true), nil).Header.Get("Location"), "sso=bad_signature") || ok(b) {
		t.Error("a forged signature")
	}
	r.p.SignWith = nil

	// CSRF on the callback: the answer opened in a browser that didn't start
	// the sign-in (no cookie, or another sign-in's) does nothing.
	victim, attacker := r.here(), r.here()
	_, env, _ := attacker.do("POST", "/api/auth/sso/start", map[string]any{}, true, nil)
	var out SSOStartResult
	_ = json.Unmarshal(env.Data, &out)
	cb, _ := r.toProvider(attacker, out.URL, maya(true))
	if res, body := victim.get(cb); res.StatusCode != http.StatusForbidden || !strings.Contains(html.UnescapeString(string(body)), "wasn't started in this browser") || ok(victim) {
		t.Fatalf("someone else's answer: %d", res.StatusCode)
	}
	_, env, _ = victim.do("POST", "/api/auth/sso/start", map[string]any{}, true, nil)
	_ = json.Unmarshal(env.Data, &out)
	if _, ok := r.toProvider(victim, out.URL, maya(true)); !ok {
		t.Fatal("victim's own begin")
	}
	if res, _ := victim.get(cb); res.StatusCode != http.StatusForbidden || ok(victim) {
		t.Fatalf("someone else's answer with a sign-in of one's own going: %d", res.StatusCode)
	}
	// The attacker's own answer still works in the attacker's browser, once.
	if res, _ := attacker.get(cb); res.StatusCode != http.StatusFound || !ok(attacker) {
		t.Fatalf("the right browser: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// A replayed answer: the sign-in is taken, nothing happens.
	replay := r.here()
	replay.cookies["mr_sso"] = mustQueryValue(cb, "state")
	if res, body := replay.get(cb); res.StatusCode != http.StatusGone || !strings.Contains(string(body), "finished already") || ok(replay) {
		t.Fatalf("a replayed answer: %d %s", res.StatusCode, body)
	}

	// A code from another sign-in, put into this one's answer: the provider
	// refuses it, since it was asked for with another PKCE challenge.
	one, two := r.here(), r.here()
	_, env, _ = one.do("POST", "/api/auth/sso/start", map[string]any{}, true, nil)
	_ = json.Unmarshal(env.Data, &out)
	cbOne, _ := r.toProvider(one, out.URL, maya(true))
	_, env, _ = two.do("POST", "/api/auth/sso/start", map[string]any{}, true, nil)
	_ = json.Unmarshal(env.Data, &out)
	cbTwo, _ := r.toProvider(two, out.URL, maya(true))
	injected := strings.Replace(cbTwo, "code="+url.QueryEscape(mustQueryValue(cbTwo, "code")), "code="+url.QueryEscape(mustQueryValue(cbOne, "code")), 1)
	if res, _ := two.get(injected); !strings.Contains(res.Header.Get("Location"), "sso=bad_code") || ok(two) {
		t.Fatalf("an injected code: %s", res.Header.Get("Location"))
	}

	// A begin address works once.
	b := r.here()
	_, env, _ = b.do("POST", "/api/auth/sso/start", map[string]any{}, true, nil)
	_ = json.Unmarshal(env.Data, &out)
	b.get(out.URL)
	if res, _ := r.here().get(out.URL); res.StatusCode != http.StatusGone {
		t.Fatalf("a begin address opened twice: %d", res.StatusCode)
	}
	// Starting needs the dashboard's header (no cross-site form starts one).
	if res, _, _ := r.here().do("POST", "/api/auth/sso/start", map[string]any{}, false, nil); res.StatusCode == 200 {
		t.Fatal("a sign-in started without the dashboard's header")
	}
	if res, _, _ := r.here().do("POST", "/api/auth/sso/start", map[string]any{}, true, map[string]string{"Sec-Fetch-Site": "cross-site"}); res.StatusCode == 200 {
		t.Fatal("a cross-site sign-in started")
	}

	// Open redirects: only a path on this site, never another site or a step of signing in.
	for next, want := range map[string]string{
		"/p/prj_1?tab=cut#x":                   "/p/prj_1?tab=cut#x",
		"//evil.example.com/x":                 "/",
		"https://evil.example.com/":            "/",
		"/\\evil.example.com":                  "/",
		"/api/ops/auth.reset":                  "/",
		"/auth/sso/begin?t=x":                  "/",
		"javascript:alert(1)":                  "/",
		"/%2F%2Fevil.example.com":              "/",
		"/settings/people\r\nSet-Cookie: x=1":  "/",
		"/" + strings.Repeat("a", 600):         "/",
		"":                                     "/",
		"/signin?next=//evil.example.com":      "/",
		"/welcome/abc":                         "/",
		"/library?q=" + url.QueryEscape("//x"): "/library?q=" + url.QueryEscape("//x"),
	} {
		res := r.signInWith(r.here(), fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.org", EmailVerified: fakeoidc.Verified(true)}, map[string]any{"next": next})
		if got := res.Header.Get("Location"); got != want {
			t.Errorf("next %q → %q, want %q", next, got, want)
		}
	}
}

func mustQueryValue(raw, name string) string {
	u, _ := url.Parse(raw)
	return u.Query().Get(name)
}

func TestOrganizationSignInFromAnotherComputer(t *testing.T) {
	r := newSSODash(t)
	ctx := context.Background()
	r.setUp(nil)
	r.testAndSwitch(fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true)})
	var inv auth.UserLinkOut
	r.op("users.invite", map[string]any{"email": "maya@example.com"}, &inv)
	if err := r.a.Cfg.SetRemote(config.Dashboard{PublicURL: "https://" + publicHost, TailscaleLogins: []string{"maya@example.com"}, MachineName: "Sam's MacBook"}); err != nil {
		t.Fatal(err)
	}
	// A browser on the tailnet, signed in to Tailscale as Maya: with an
	// organization's sign-in, Tailscale's identity alone lets nobody in.
	mb := r.tailnet("maya@example.com", "Maya")
	if _, code := sessionOf(t, mb); code != 401 {
		t.Fatal("Tailscale's identity signed Maya in under an organization's sign-in")
	}
	res, env, raw := mb.do("POST", "/api/auth/sso/start", map[string]any{"next": "/library"}, true, nil)
	if res.StatusCode != 200 {
		t.Fatalf("start: %s", raw)
	}
	var out SSOStartResult
	_ = json.Unmarshal(env.Data, &out)
	if !strings.HasPrefix(out.URL, "https://"+publicHost+"/auth/sso/begin?t=") {
		t.Fatalf("begin address: %s", out.URL)
	}
	bres, _ := mb.get(out.URL)
	var setCookie *http.Cookie
	for _, c := range bres.Cookies() {
		if c.Name == "__Host-mr_sso" {
			setCookie = c
		}
	}
	if setCookie == nil || !setCookie.Secure || !setCookie.HttpOnly || setCookie.SameSite != http.SameSiteLaxMode || setCookie.Path != "/" || setCookie.Domain != "" {
		t.Fatalf("the sign-in's cookie: %+v", bres.Cookies())
	}
	r.p.Next = func() *fakeoidc.Person { p := maya(true); return &p }()
	pres, _ := http.DefaultTransport.RoundTrip(mustGet(bres.Header.Get("Location")))
	cb := pres.Header.Get("Location")
	if !strings.HasPrefix(cb, "https://"+publicHost+"/auth/sso/callback?") {
		t.Fatalf("the provider sent the browser to %s", cb)
	}
	// The answer at this computer's own address (not where it began): refused.
	lb := r.here()
	lb.cookies["mr_sso"] = mustQueryValue(cb, "state")
	if res, _ := lb.get("http://" + lb.host + "/auth/sso/callback?" + mustQuery(cb)); res.StatusCode != http.StatusForbidden {
		t.Fatalf("an answer at another address: %d", res.StatusCode)
	}
	if res, _ := mb.get(cb); res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/library" {
		t.Fatalf("callback: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if _, ok := mb.cookies["__Host-mr_session"]; !ok {
		t.Fatalf("session cookie: %v", mb.cookies)
	}
	sess, code := sessionOf(t, mb)
	if code != 200 || sess.User.ID != inv.Account.ID || !sess.Access.Remote || sess.Access.SignedInWith != "sso" {
		t.Fatalf("remote session: %d %+v", code, sess)
	}
	// Plain http on the public address: no sign-ins.
	if res, _, _ := r.tailnet("", "").do("POST", "/api/auth/sso/start", map[string]any{}, true, map[string]string{"X-Forwarded-Proto": "http"}); res.StatusCode == 200 {
		t.Fatal("a sign-in over plain http")
	}
	// Spoofed Tailscale headers on this computer's own address are still refused.
	if res, _, _ := r.here().do("POST", "/api/auth/sso/start", map[string]any{}, true, map[string]string{"Tailscale-User-Login": "maya@example.com"}); res.StatusCode != http.StatusForbidden {
		t.Fatalf("spoofed headers: %d", res.StatusCode)
	}
	// Tailscale accounts kept beside it: the listed account signs in by itself again.
	r.setUp(map[string]any{"alsoTailscale": true})
	waitSettings(ctx, r)
	if s, code := sessionOf(t, r.tailnet("maya@example.com", "Maya")); code != 200 || s.Access.SignedInWith != "tailscale" {
		t.Fatalf("Tailscale kept beside it: %d %+v", code, s)
	}
	// The method changes only at this computer.
	if res, env, _ := mb.do("POST", "/api/ops/auth.sso.test", map[string]any{}, true, nil); res.StatusCode == 200 || env.Error.Details["reason"] == nil {
		t.Fatalf("a member's test from elsewhere: %d", res.StatusCode)
	}
}

// waitSettings lets the cached sign-in settings catch up (5 s at most).
func waitSettings(ctx context.Context, r *ssoRig) {
	auth.ForgetSettings(r.a)
}

func TestOrganizationSessionsAndTheOtherSignIns(t *testing.T) {
	r := newSSODash(t)
	ctx := context.Background()
	// Before: email and password, with Maya signed in by password.
	r.op("auth.method.set", map[string]any{"method": "password", "email": "sam@example.com", "password": "tomato garden at dusk"}, nil)
	var inv auth.UserLinkOut
	r.op("users.invite", map[string]any{"email": "maya@example.com"}, &inv)
	token := inv.Link[strings.LastIndex(inv.Link, "/")+1:]
	pw := r.here()
	if res, _, raw := pw.do("POST", "/api/auth/link/"+token, map[string]any{"name": "Maya", "password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("Maya joins with a password: %s", raw)
	}
	// Switching keeps passwords when the owner says so…
	r.setUp(map[string]any{"alsoPassword": true})
	r.testAndSwitch(fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true)})
	if _, code := sessionOf(t, pw); code != 200 {
		t.Fatal("a password session ended though passwords stay")
	}
	b := r.here()
	if res, _, _ := b.do("POST", "/api/auth/login", map[string]any{"email": "maya@example.com", "password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
		t.Fatal("password sign-in kept beside it")
	}
	// …and ends them when it doesn't (the same provider and app: no new test).
	r.setUp(map[string]any{"alsoPassword": false})
	auth.ForgetSettings(r.a)
	if _, code := sessionOf(t, pw); code != 401 {
		t.Fatal("a password session outlived passwords")
	}
	if res, env, _ := r.here().do("POST", "/api/auth/login", map[string]any{"email": "maya@example.com", "password": "sunflowers by the gate"}, true, nil); res.StatusCode == 200 || env.Error.Details["reason"] != "no_passwords" {
		t.Fatalf("password sign-in after turning it off: %d", res.StatusCode)
	}
	// Maya signs in with the provider (her email is the provider's checked one).
	mb := r.here()
	if res := r.signInWith(mb, maya(true), nil); strings.Contains(res.Header.Get("Location"), "signin") {
		t.Fatalf("Maya: %s", res.Header.Get("Location"))
	}
	// Sign out everywhere ends it on every computer (the database's time).
	r.op("users.disable", map[string]any{"user": inv.Account.ID}, nil)
	r.op("users.enable", map[string]any{"user": inv.Account.ID}, nil)
	if _, code := sessionOf(t, mb); code != 401 {
		t.Fatal("an organization session outlived being turned off")
	}
	mb = r.here()
	r.signInWith(mb, maya(true), nil)
	var you auth.UserOut
	mbRes, mbEnv, _ := mb.do("POST", "/api/ops/account.signoutEverywhere", map[string]any{}, true, nil)
	_ = json.Unmarshal(mbEnv.Data, &you)
	if mbRes.StatusCode != 200 {
		t.Fatalf("sign out everywhere: %d", mbRes.StatusCode)
	}
	if _, code := sessionOf(t, mb); code != 401 {
		t.Fatal("still signed in after signing out everywhere")
	}
	// Going back to email and password ends organization sessions.
	mb = r.here()
	r.signInWith(mb, maya(true), nil)
	r.op("auth.method.set", map[string]any{"method": "password", "email": "sam@example.com", "currentPassword": "tomato garden at dusk"}, nil)
	if _, code := sessionOf(t, mb); code != 401 {
		t.Fatal("an organization session outlived the method")
	}
	// The session file keeps the method and provider, never a token.
	b2, _ := os.ReadFile(filepath.Join(r.a.Cfg.StateDir(), "sessions.json"))
	if strings.Contains(string(b2), "eyJ") {
		t.Fatal("a token in the session file")
	}
	var ss map[string]sessions.Session
	_ = json.Unmarshal(b2, &ss)
	_ = ctx
}

func TestOrganizationSetupIsTheOwnersAtThisComputer(t *testing.T) {
	r := newSSODash(t)
	ctx := context.Background()
	r.setUp(nil)
	r.testAndSwitch(fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true)})
	var inv auth.UserLinkOut
	r.op("users.invite", map[string]any{"email": "maya@example.com"}, &inv)
	mb := r.here()
	r.signInWith(mb, maya(true), nil)
	// A member: reads it, changes nothing.
	if res, env, _ := mb.do("GET", "/api/ops", nil, false, nil); res.StatusCode != 200 || env.OK != true {
		t.Fatal("ops list")
	}
	for name, in := range map[string]any{
		"auth.sso.set":    map[string]any{"provider": "oidc", "issuer": r.p.Issuer, "clientId": "x"},
		"auth.sso.secret": map[string]any{"clientSecret": sso.Random(24)},
		"auth.sso.test":   map[string]any{},
		"auth.method.set": map[string]any{"method": "single"},
	} {
		if res, env, _ := mb.do("POST", "/api/ops/"+name, in, true, nil); res.StatusCode == 200 || env.Error.Details["reason"] != "owner_only" {
			t.Errorf("a member's %s: %d %+v", name, res.StatusCode, env.Error)
		}
	}
	var st auth.SSOStatusOut
	if res, env, _ := mb.do("POST", "/api/ops/auth.sso.status", map[string]any{}, true, nil); res.StatusCode != 200 {
		t.Fatal("a member reading the setup")
	} else {
		_ = json.Unmarshal(env.Data, &st)
	}
	if st.Owner || !st.InUse || strings.Contains(ssoJSON(st), r.secret) {
		t.Fatalf("a member's view: %+v", st)
	}
	// The secret never appears in an answer, the log or the database.
	for _, coll := range []string{store.CWorkspaces, store.CUsers, store.CAudit} {
		var docs []bson.M
		cur, _ := r.a.DB.C(coll).Find(ctx, bson.M{})
		_ = cur.All(ctx, &docs)
		if strings.Contains(ssoJSON(docs), r.secret) {
			t.Errorf("the client secret in %s", coll)
		}
	}
	r.a.Log.Flush(ctx)
	var entries []bson.M
	if cur, err := r.a.DB.C(store.CLogEntries).Find(ctx, bson.M{}); err == nil {
		_ = cur.All(ctx, &entries)
	}
	if s := ssoJSON(entries); strings.Contains(s, r.secret) || strings.Contains(s, "eyJ") {
		t.Error("a secret or a token in the activity log")
	}
}

func ssoJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
