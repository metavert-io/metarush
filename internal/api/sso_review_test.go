package api

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/testutil/fakeoidc"
)

// Signing in with an organization's account, from an independent review's
// scenarios (D450–D454): a new app never breaks the sign-in in use; sign-ins
// on this computer come back to 127.0.0.1 only; moving to another provider
// keeps the people; two sign-ins in one browser both finish.

func masked(secret string) string { return "••••" + secret[len(secret)-4:] }

func reasonIn(env envelope) any { return env.Error.Details["reason"] }

// signsIn signs Maya in from a new browser on this computer with the sign-in
// in use, and checks the provider saw the app wantApp.
func (r *ssoRig) signsIn(what, wantApp, mayaID string) {
	r.t.Helper()
	b := r.here()
	res := r.signInWith(b, maya(true), nil)
	if res.StatusCode != http.StatusFound || strings.Contains(res.Header.Get("Location"), "signin") {
		r.t.Fatalf("%s: Maya's sign-in went to %s", what, res.Header.Get("Location"))
	}
	if s, code := sessionOf(r.t, b); code != 200 || s.User.ID != mayaID || s.Access.SignedInWith != "sso" {
		r.t.Fatalf("%s: Maya's session: %d %+v", what, code, s)
	}
	if got := r.p.Seen[len(r.p.Seen)-1].Get("client_id"); got != wantApp {
		r.t.Fatalf("%s: the provider saw the app %s, not %s", what, got, wantApp)
	}
}

// testWith runs the owner's test sign-in of the setup waiting and says
// whether it worked.
func (r *ssoRig) testWith(owner fakeoidc.Person) (bool, string) {
	r.t.Helper()
	var test auth.SSOTestOut
	r.op("auth.sso.test", map[string]any{}, &test)
	res, body := r.testIn(r.here(), test.URL, owner)
	page := html.UnescapeString(string(body))
	return res.StatusCode == 200 && strings.Contains(page, "The test sign-in worked"), page
}

// statusAfter runs an op that answers with the sign-in's status (a fresh
// value: decoding into an old one would keep fields the answer leaves out).
func (r *ssoRig) statusAfter(name string, in any) auth.SSOStatusOut {
	r.t.Helper()
	var out auth.SSOStatusOut
	r.op(name, in, &out)
	return out
}

// The reviewer's run: with app A in use, setting up app B with its own secret
// sent A's next sign-in to bad_client, a remote owner's included, and backing
// out needed A's secret, which providers show once. Each app now has its own
// secret, and a new one waits for its test.
func TestTheSignInInUseKeepsWorkingWhileAnotherAppIsSetUp(t *testing.T) {
	r := newSSODash(t)
	owner := fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true), Name: "Sam Lee"}
	r.setUp(nil)
	r.testAndSwitch(owner)
	var inv auth.UserLinkOut
	r.op("users.invite", map[string]any{"email": "maya@example.com"}, &inv)
	mayaID := inv.Account.ID
	appA := &models.SSOSettings{Provider: sso.OIDC, Issuer: r.p.Issuer, ClientID: "metarush-app"}
	appB := &models.SSOSettings{Provider: sso.OIDC, Issuer: r.p.Issuer, ClientID: "metarush-app-b"}
	r.signsIn("before", "metarush-app", mayaID)

	// App B, registered with the same provider, with its own secret.
	secretB := sso.Random(24)
	r.p.AddClient("metarush-app-b", secretB)
	setB := map[string]any{"provider": "oidc", "issuer": r.p.Issuer, "clientId": "metarush-app-b", "clientSecret": secretB, "label": "Test provider"}
	st := r.statusAfter("auth.sso.set", setB)
	if st.Next == nil || st.Next.ClientID != "metarush-app-b" || !st.InUse || st.Secret.Masked != masked(r.secret) || st.NextSecret == nil || st.NextSecret.Masked != masked(secretB) {
		t.Fatalf("B waiting: %+v", st)
	}
	r.signsIn("while B waits", "metarush-app", mayaID)
	// The owner from another computer too.
	ob := r.tailnet("owner-elsewhere@example.com", "Sam")
	if res := r.signInWith(ob, owner, nil); res.StatusCode != http.StatusFound || strings.Contains(res.Header.Get("Location"), "signin") {
		t.Fatalf("the owner from another computer while B waits: %s", res.Header.Get("Location"))
	}
	if s, code := sessionOf(t, ob); code != 200 || s.User.ID != r.a.Cfg.User.ID {
		t.Fatalf("the owner's session from another computer: %d %+v", code, s)
	}

	// B's test signs in with B's secret; A still signs people in.
	if ok, page := r.testWith(owner); !ok {
		t.Fatalf("B's test: %s", page)
	}
	if got := r.p.Seen[len(r.p.Seen)-1].Get("client_id"); got != "metarush-app-b" {
		t.Fatalf("B's test went to %s", got)
	}
	r.signsIn("after B's test", "metarush-app", mayaID)

	// B forgotten: its secret goes with it, A's stays.
	st = r.statusAfter("auth.sso.discard", map[string]any{})
	if st.Next != nil || st.NextSecret != nil || !st.InUse || st.Secret.Masked != masked(r.secret) {
		t.Fatalf("after forgetting B: %+v", st)
	}
	if s, _, _ := auth.SSONextSecret(r.a.Cfg, appB); s != "" {
		t.Fatal("B's secret stayed on this computer after B was forgotten")
	}
	r.signsIn("after B was forgotten", "metarush-app", mayaID)
	if code, env := r.do("POST", "/api/ops/auth.sso.discard", map[string]any{}, true); code == 200 || reasonIn(env) != "nothing_waiting" {
		t.Fatalf("forgetting nothing: %d %+v", code, env.Error)
	}

	// B again, tested and switched to: people sign in with B.
	r.op("auth.sso.set", setB, nil)
	if ok, page := r.testWith(owner); !ok {
		t.Fatalf("B's second test: %s", page)
	}
	r.op("auth.method.set", map[string]any{"method": "sso"}, nil)
	r.signsIn("with B in use", "metarush-app-b", mayaID)
	// A's secret stays for switching back (a provider shows a secret once).
	if s, _, _ := auth.SSOSecret(r.a.Cfg, appA); s != r.secret {
		t.Fatal("A's secret went when B was switched to")
	}

	// Back to A, without typing its secret again.
	r.change(nil)
	if ok, page := r.testWith(owner); !ok {
		t.Fatalf("A's test: %s", page)
	}
	r.op("auth.method.set", map[string]any{"method": "sso"}, nil)
	r.signsIn("back with A", "metarush-app", mayaID)

	// A new secret for the app in use waits for its test: a mistyped one never
	// reaches a sign-in, and the switch waits for a test that works.
	wrong := sso.Random(24)
	st = r.statusAfter("auth.sso.secret", map[string]any{"clientSecret": wrong})
	if st.Next == nil || st.NextSecret == nil || st.NextSecret.Masked != masked(wrong) || st.Secret.Masked != masked(r.secret) || !st.InUse {
		t.Fatalf("a new secret waiting: %+v", st)
	}
	r.signsIn("while a mistyped secret waits", "metarush-app", mayaID)
	if ok, page := r.testWith(owner); ok || !strings.Contains(page, "didn't work") {
		t.Fatalf("the mistyped secret's test: %s", page)
	}
	if code, env := r.do("POST", "/api/ops/auth.method.set", map[string]any{"method": "sso"}, true); code == 200 || reasonIn(env) != "not_tested" {
		t.Fatalf("switching after a failed test: %d %+v", code, env.Error)
	}
	r.signsIn("after the mistyped secret's test", "metarush-app", mayaID)

	// The provider gives A a new secret: kept here, tested, switched to.
	rotated := sso.Random(24)
	r.p.SetSecret("metarush-app", rotated)
	r.op("auth.sso.secret", map[string]any{"clientSecret": rotated}, nil)
	if ok, page := r.testWith(owner); !ok {
		t.Fatalf("the new secret's test: %s", page)
	}
	r.op("auth.method.set", map[string]any{"method": "sso"}, nil)
	if s, _, _ := auth.SSOSecret(r.a.Cfg, appA); s != rotated {
		t.Fatal("A's new secret isn't the one in use after the switch")
	}
	if s, _, _ := auth.SSONextSecret(r.a.Cfg, appA); s != "" {
		t.Fatal("a secret still waits after the switch")
	}
	r.signsIn("with A's new secret", "metarush-app", mayaID)
	// Only the app in use and the one before it keep a secret here.
	if s, _, _ := auth.SSOSecret(r.a.Cfg, appB); s != "" {
		t.Fatal("B's secret lingers two switches later")
	}
	// The secrets never show up in an answer.
	st = r.statusAfter("auth.sso.status", map[string]any{})
	for _, s := range []string{r.secret, secretB, wrong, rotated} {
		if strings.Contains(ssoJSON(st), s) {
			t.Fatal("a secret in the status")
		}
	}
}

// The reviewer's run: Chromium and curl sent localhost:<port>/auth/sso/callback
// with the code, the state and the sign-in's cookie to a second program on
// [::1]:<port>, and replaying them gave an owner session. Sign-ins on this
// computer now come back to 127.0.0.1, which only the dashboard holds; a
// sign-in begun or answered at localhost is refused.
func TestThisComputersSignInsComeBackTo127001(t *testing.T) {
	r := newSSODash(t)
	owner := fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true)}
	st := r.setUp(nil)
	port := strconv.Itoa(r.a.Cfg.Dashboard.Port)
	if st.Callbacks.Local != "http://127.0.0.1:"+port+"/auth/sso/callback" {
		t.Fatalf("the address to register: %s", st.Callbacks.Local)
	}
	// The owner's test comes back to 127.0.0.1 as well.
	var test auth.SSOTestOut
	r.op("auth.sso.test", map[string]any{}, &test)
	if !strings.HasPrefix(test.URL, "http://127.0.0.1:"+port+"/auth/sso/begin?") {
		t.Fatalf("the test's address: %s", test.URL)
	}
	if res, _ := r.atLocalhost().get("http://localhost:" + port + "/auth/sso/begin?" + mustQuery(test.URL)); res.StatusCode != http.StatusForbidden {
		t.Fatalf("the test begun at localhost: %d", res.StatusCode)
	}
	r.op("auth.sso.test", map[string]any{}, &test)
	r.testAndSwitchWith(test.URL, owner)

	// The sign-in page opened at localhost: the sign-in goes on at 127.0.0.1.
	lb := r.atLocalhost()
	res, env, raw := lb.do("POST", "/api/auth/sso/start", map[string]any{}, true, nil)
	if res.StatusCode != 200 {
		t.Fatalf("start at localhost: %s", raw)
	}
	var out SSOStartResult
	_ = json.Unmarshal(env.Data, &out)
	if !strings.HasPrefix(out.URL, "http://127.0.0.1:"+port+"/auth/sso/begin?t=") {
		t.Fatalf("begin address: %s", out.URL)
	}
	// The begin address opened at localhost: refused, no cookie, no provider.
	if res, _ := lb.get("http://localhost:" + port + "/auth/sso/begin?" + mustQuery(out.URL)); res.StatusCode != http.StatusForbidden || len(res.Cookies()) != 0 {
		t.Fatalf("begin at localhost: %d %v", res.StatusCode, res.Cookies())
	}
	// At 127.0.0.1 it goes to the provider, which is told to answer there.
	b := r.here()
	res, env, _ = b.do("POST", "/api/auth/sso/start", map[string]any{}, true, nil)
	_ = json.Unmarshal(env.Data, &out)
	cb, ok := r.toProvider(b, out.URL, maya(true))
	if !ok {
		t.Fatal("begin at 127.0.0.1 didn't reach the provider")
	}
	if got := r.p.Seen[len(r.p.Seen)-1].Get("redirect_uri"); got != "http://127.0.0.1:"+port+"/auth/sso/callback" || !strings.HasPrefix(cb, got+"?") {
		t.Fatalf("the provider answers at %s (%s)", got, cb)
	}
	// The answer, the state and the cookie replayed at localhost (where a
	// program on [::1] would have caught them): refused, nobody signed in.
	thief := r.atLocalhost()
	for n, v := range b.cookies {
		thief.cookies[n] = v
	}
	tres, _ := thief.get("http://localhost:" + port + "/auth/sso/callback?" + mustQuery(cb))
	if tres.StatusCode != http.StatusForbidden {
		t.Fatalf("the answer replayed at localhost: %d", tres.StatusCode)
	}
	if _, code := sessionOf(t, thief); code != 401 {
		t.Fatal("the replay signed someone in")
	}
	// The browser it belongs to still finishes it, once.
	var inv auth.UserLinkOut
	r.op("users.invite", map[string]any{"email": "maya@example.com"}, &inv)
	if res, _ := b.get(cb); res.StatusCode != http.StatusFound || strings.Contains(res.Header.Get("Location"), "signin") {
		t.Fatalf("the sign-in's own browser: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if s, code := sessionOf(t, b); code != 200 || s.User.ID != inv.Account.ID {
		t.Fatalf("Maya's session: %d %+v", code, s)
	}
}

// The same with real connections: a program listening on [::1] at the
// dashboard's port never sees any step of a sign-in on this computer.
func TestAProgramOnIPv6LoopbackNeverSeesASignIn(t *testing.T) {
	r := newSSODash(t)
	owner := fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true)}
	r.setUp(nil)
	r.testAndSwitch(owner)
	var inv auth.UserLinkOut
	r.op("users.invite", map[string]any{"email": "maya@example.com"}, &inv)
	port := strconv.Itoa(r.a.Cfg.Dashboard.Port)
	ln, err := net.Listen("tcp", "[::1]:"+port)
	if err != nil {
		t.Skipf("no IPv6 loopback here: %v", err)
	}
	var seen atomic.Int32
	other := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		seen.Add(1)
		http.Error(w, "not MetaRush", http.StatusTeapot)
	})}
	go func() { _ = other.Serve(ln) }()
	t.Cleanup(func() { _ = other.Close() })

	jar, _ := cookiejar.New(nil)
	browser := &http.Client{Jar: jar}
	req, _ := http.NewRequest("POST", "http://127.0.0.1:"+port+"/api/auth/sso/start", strings.NewReader(`{}`))
	req.Header.Set(clientHeader, "dashboard")
	req.Header.Set("Content-Type", "application/json")
	res, err := browser.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var env envelope
	_ = json.NewDecoder(res.Body).Decode(&env)
	res.Body.Close()
	var out SSOStartResult
	_ = json.Unmarshal(env.Data, &out)
	r.p.Next = func() *fakeoidc.Person { p := maya(true); return &p }()
	// The browser follows every redirect: the provider, then the answer.
	res, err = browser.Get(out.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if seen.Load() != 0 {
		t.Fatalf("the program on [::1] saw %d requests", seen.Load())
	}
	u, _ := url.Parse("http://127.0.0.1:" + port + "/")
	got := false
	for _, c := range jar.Cookies(u) {
		got = got || c.Name == "mr_session"
	}
	if !got {
		t.Fatalf("no session after the sign-in (ended at %s, %d)", res.Request.URL, res.StatusCode)
	}
	sreq, _ := http.NewRequest("GET", "http://127.0.0.1:"+port+"/api/session", nil)
	sres, err := browser.Do(sreq)
	if err != nil {
		t.Fatal(err)
	}
	var senv envelope
	_ = json.NewDecoder(sres.Body).Decode(&senv)
	sres.Body.Close()
	var s Session
	_ = json.Unmarshal(senv.Data, &s)
	if sres.StatusCode != 200 || s.User.ID != inv.Account.ID {
		t.Fatalf("Maya's session: %d %+v", sres.StatusCode, s)
	}
}

// The reviewer's case: the account key includes the issuer, so after an owner
// moved to another provider every member's sign-in was refused as "signs in
// with another account". A person's next sign-in now links their account at
// the new provider by an email it vouches for — the rule of a first sign-in —
// while a look-alike at the same provider still can't take theirs over.
func TestMovingToAnotherProviderKeepsThePeople(t *testing.T) {
	r := newSSODash(t)
	ctx := context.Background()
	owner := fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true)}
	r.setUp(nil)
	r.testAndSwitch(owner)
	var inv auth.UserLinkOut
	r.op("users.invite", map[string]any{"email": "maya@example.com"}, &inv)
	r.signsIn("with the first provider", "metarush-app", inv.Account.ID)

	// Another provider (its own address), set up, tested and switched to.
	secret2 := sso.Random(24)
	p2 := fakeoidc.New("metarush-app-2", secret2)
	t.Cleanup(p2.Close)
	local, public := auth.SSOCallbacks(r.a.Cfg)
	p2.Redirects = []string{local, public}
	first := r.p
	r.p = p2
	r.op("auth.sso.set", map[string]any{"provider": "oidc", "issuer": p2.Issuer, "clientId": "metarush-app-2", "clientSecret": secret2, "label": "New provider"}, nil)
	ownerThere := fakeoidc.Person{Sub: "sam-at-2", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true)}
	if ok, page := r.testWith(ownerThere); !ok {
		t.Fatalf("the new provider's test: %s", page)
	}
	r.op("auth.method.set", map[string]any{"method": "sso"}, nil)
	st := r.statusAfter("auth.sso.status", map[string]any{})
	if st.Earlier != 1 || !strings.Contains(st.Summary, "earlier provider") {
		t.Fatalf("people of the earlier provider: %d %q", st.Earlier, st.Summary)
	}

	// An account there whose email it doesn't vouch for links nobody.
	b := r.here()
	if res := r.signInWith(b, fakeoidc.Person{Sub: "maya-unchecked", Email: "maya@example.com", EmailVerified: fakeoidc.Verified(false)}, nil); !strings.Contains(res.Header.Get("Location"), "sso=not_member") {
		t.Fatalf("an unchecked email: %s", res.Header.Get("Location"))
	}
	// Maya's account there, its email vouched for: linked to her.
	mayaThere := fakeoidc.Person{Sub: "maya-at-2", Email: "maya@example.com", EmailVerified: fakeoidc.Verified(true)}
	b = r.here()
	if res := r.signInWith(b, mayaThere, nil); res.StatusCode != http.StatusFound || strings.Contains(res.Header.Get("Location"), "signin") {
		t.Fatalf("Maya at the new provider: %s", res.Header.Get("Location"))
	}
	if s, code := sessionOf(t, b); code != 200 || s.User.ID != inv.Account.ID {
		t.Fatalf("Maya's session: %d %+v", code, s)
	}
	st = r.statusAfter("auth.sso.status", map[string]any{})
	if st.Earlier != 0 {
		t.Fatalf("still %d of the earlier provider", st.Earlier)
	}
	// Another account at the new provider showing her email doesn't take hers over.
	b = r.here()
	if res := r.signInWith(b, fakeoidc.Person{Sub: "look-alike", Email: "maya@example.com", EmailVerified: fakeoidc.Verified(true)}, nil); !strings.Contains(res.Header.Get("Location"), "sso=linked_elsewhere") {
		t.Fatalf("a look-alike at the new provider: %s", res.Header.Get("Location"))
	}
	u, _ := auth.GetUser(ctx, r.a, inv.Account.ID)
	if len(u.SSO) != 2 {
		t.Fatalf("Maya's accounts: %+v", u.SSO)
	}

	// Back to the first provider: her first account still signs her in.
	r.p = first
	r.change(nil)
	if ok, page := r.testWith(owner); !ok {
		t.Fatalf("the first provider's test: %s", page)
	}
	r.op("auth.method.set", map[string]any{"method": "sso"}, nil)
	r.signsIn("back with the first provider", "metarush-app", inv.Account.ID)
}

// The reviewer's case: two sign-ins started in one browser shared one cookie,
// so the first tab's answer was "This sign-in wasn't started in this
// browser". A browser now keeps a few sign-ins in progress.
func TestTwoSignInsInOneBrowserBothFinish(t *testing.T) {
	r := newSSODash(t)
	r.setUp(nil)
	r.testAndSwitch(fakeoidc.Person{Sub: "sub-sam", Email: "sam@example.com", EmailVerified: fakeoidc.Verified(true)})
	var inv auth.UserLinkOut
	r.op("users.invite", map[string]any{"email": "maya@example.com"}, &inv)
	b := r.here()
	begin := func() string {
		t.Helper()
		res, env, raw := b.do("POST", "/api/auth/sso/start", map[string]any{}, true, nil)
		if res.StatusCode != 200 {
			t.Fatalf("start: %s", raw)
		}
		var out SSOStartResult
		_ = json.Unmarshal(env.Data, &out)
		cb, ok := r.toProvider(b, out.URL, maya(true))
		if !ok {
			t.Fatal("begin")
		}
		return cb
	}
	first, second := begin(), begin()
	// The first tab's answer comes back first: it finishes.
	if res, _ := b.get(first); res.StatusCode != http.StatusFound || strings.Contains(res.Header.Get("Location"), "signin") {
		t.Fatalf("the first tab: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	if s, code := sessionOf(t, b); code != 200 || s.User.ID != inv.Account.ID {
		t.Fatalf("after the first tab: %d", code)
	}
	// The second one too.
	if res, _ := b.get(second); res.StatusCode != http.StatusFound || strings.Contains(res.Header.Get("Location"), "signin") {
		t.Fatalf("the second tab: %d %s", res.StatusCode, res.Header.Get("Location"))
	}
	// Each answer works once.
	if res, _ := b.get(first); res.StatusCode != http.StatusForbidden {
		t.Fatalf("an answer used twice: %d", res.StatusCode)
	}
	// A few at once: the oldest of more than four gives way, the rest finish.
	var answers []string
	for range 5 {
		answers = append(answers, begin())
	}
	if res, _ := b.get(answers[0]); res.StatusCode != http.StatusForbidden {
		t.Fatalf("the oldest of five: %d", res.StatusCode)
	}
	for i := len(answers) - 1; i >= 1; i-- {
		if res, _ := b.get(answers[i]); res.StatusCode != http.StatusFound || strings.Contains(res.Header.Get("Location"), "signin") {
			t.Fatalf("sign-in %d of five: %d %s", i+1, res.StatusCode, res.Header.Get("Location"))
		}
	}
	// Another browser's answer still finishes nothing here.
	other := r.here()
	cb := func() string {
		res, env, _ := other.do("POST", "/api/auth/sso/start", map[string]any{}, true, nil)
		if res.StatusCode != 200 {
			t.Fatal("start in another browser")
		}
		var out SSOStartResult
		_ = json.Unmarshal(env.Data, &out)
		c, _ := r.toProvider(other, out.URL, maya(true))
		return c
	}()
	if res, _ := b.get(cb); res.StatusCode != http.StatusForbidden {
		t.Fatalf("another browser's answer: %d", res.StatusCode)
	}
	if res, _ := other.get(cb); res.StatusCode != http.StatusFound {
		t.Fatalf("its own browser: %d", res.StatusCode)
	}
}
