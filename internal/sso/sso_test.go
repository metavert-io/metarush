package sso

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/testutil/fakeoidc"
)

// Every check of the ID token and the exchange against the stand-in
// provider (internal/testutil/fakeoidc): keys made at run time, never a real
// provider (D432).

const redirect = "http://localhost:5799/auth/sso/callback"

type rig struct {
	t  *testing.T
	p  *fakeoidc.Server
	c  *Client
	at time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	secret := Random(24) // made at run time: never a committed key
	p := fakeoidc.New("metarush-test-app", secret)
	t.Cleanup(p.Close)
	t.Cleanup(Forget)
	r := &rig{t: t, p: p}
	r.c = &Client{Kind: OIDC, Issuer: p.Issuer, ClientID: "metarush-test-app", Secret: secret, Now: func() time.Time {
		if r.at.IsZero() {
			return time.Now()
		}
		return r.at
	}}
	return r
}

// signIn runs a sign-in as the browser would (the provider's answer read from
// its redirect) and returns what Exchange says.
func (r *rig) signIn(who fakeoidc.Person, change func(req *Request, verifier *string)) (*Identity, error) {
	r.t.Helper()
	r.p.Next = &who
	verifier := Random(48)
	req := Request{Redirect: redirect, State: Random(32), Nonce: Random(32), Challenge: Challenge(verifier)}
	exchangeVerifier, nonce := verifier, req.Nonce
	if change != nil {
		change(&req, &exchangeVerifier)
	}
	u, err := r.c.AuthURL(context.Background(), req)
	if err != nil {
		return nil, err
	}
	code, err := r.code(u, req.State)
	if err != nil {
		return nil, err
	}
	return r.c.Exchange(context.Background(), code, exchangeVerifier, redirect, nonce)
}

// code follows the authorization address without following its redirect.
func (r *rig) code(authURL, state string) (string, error) {
	r.t.Helper()
	res, err := http.DefaultTransport.RoundTrip(mustReq(authURL))
	if err != nil {
		r.t.Fatal(err)
	}
	res.Body.Close()
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil || res.StatusCode != http.StatusFound {
		return "", errors.New("no answer from the provider's page")
	}
	q := loc.Query()
	if e := q.Get("error"); e != "" {
		return "", errors.New(e)
	}
	if q.Get("state") != state {
		r.t.Fatalf("the provider's answer carried another state: %q", q.Get("state"))
	}
	return q.Get("code"), nil
}

func mustReq(u string) *http.Request {
	req, _ := http.NewRequest("GET", u, nil)
	return req
}

func person(email string, verified bool) fakeoidc.Person {
	return fakeoidc.Person{Sub: "sub-" + email, Email: email, EmailVerified: fakeoidc.Verified(verified), Name: "Maya Lindqvist"}
}

func reasonOf(err error) string {
	var p *Problem
	if errors.As(err, &p) {
		return p.Reason
	}
	return ""
}

func TestASignInPassesEveryCheck(t *testing.T) {
	r := newRig(t)
	id, err := r.signIn(person("maya@example.com", true), nil)
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject != "sub-maya@example.com" || id.Email != "maya@example.com" || !id.EmailTrusted || id.Domain != "example.com" || id.Name != "Maya Lindqvist" || id.Key == "" {
		t.Fatalf("%+v", id)
	}
	// What the browser was sent with: PKCE (S256), state, nonce, the openid scope.
	seen := r.p.Seen[len(r.p.Seen)-1]
	if seen.Get("code_challenge_method") != "S256" || seen.Get("code_challenge") == "" || seen.Get("nonce") == "" || seen.Get("state") == "" ||
		!strings.Contains(seen.Get("scope"), "openid") || seen.Get("redirect_uri") != redirect {
		t.Fatalf("authorization request: %v", seen)
	}
	// The secret went in the form (the provider lists client_secret_post), and
	// basic works when it's the only way.
	r.p.PostOnly = true
	Forget()
	if _, err := r.signIn(person("maya@example.com", true), nil); err != nil {
		t.Fatalf("client_secret_post: %v", err)
	}
}

func TestTheTokenIsRefusedWhenAnyClaimIsWrong(t *testing.T) {
	r := newRig(t)
	cases := []struct {
		name, reason string
		bend         func(map[string]any)
	}{
		{"another app's token", "bad_audience", func(c map[string]any) { c["aud"] = "someone-else" }},
		{"several audiences", "bad_audience", func(c map[string]any) {
			c["aud"] = []string{"metarush-test-app", "someone-else"}
			c["azp"] = "metarush-test-app"
		}},
		{"a prefix of the app", "bad_audience", func(c map[string]any) { c["aud"] = "metarush-test" }},
		{"azp names another app", "bad_audience", func(c map[string]any) { c["azp"] = "someone-else" }},
		{"another issuer", "bad_issuer", func(c map[string]any) { c["iss"] = "https://login.example.net" }},
		{"the issuer with a slash", "bad_issuer", func(c map[string]any) { c["iss"] = c["iss"].(string) + "/" }},
		{"expired", "expired", func(c map[string]any) { c["exp"] = time.Now().Add(-2 * time.Minute).Unix() }},
		{"no expiry", "expired", func(c map[string]any) { delete(c, "exp") }},
		{"issued long ago", "expired", func(c map[string]any) { c["iat"] = time.Now().Add(-time.Hour).Unix() }},
		{"issued in the future", "bad_time", func(c map[string]any) { c["iat"] = time.Now().Add(5 * time.Minute).Unix() }},
		{"not valid yet", "bad_time", func(c map[string]any) { c["nbf"] = time.Now().Add(5 * time.Minute).Unix() }},
		{"no nonce", "bad_nonce", func(c map[string]any) { delete(c, "nonce") }},
		{"another sign-in's nonce", "bad_nonce", func(c map[string]any) { c["nonce"] = Random(32) }},
		{"no subject", "bad_token", func(c map[string]any) { c["sub"] = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r.p.Bend = tc.bend
			_, err := r.signIn(person("maya@example.com", true), nil)
			if reasonOf(err) != tc.reason {
				t.Fatalf("want %s, got %v (%s)", tc.reason, err, reasonOf(err))
			}
		})
	}
	// A minute's difference between the clocks is allowed, not more.
	r.at = time.Now().Add(-50 * time.Second)
	if _, err := r.signIn(person("maya@example.com", true), nil); err != nil {
		t.Fatalf("a slow clock within the allowance: %v", err)
	}
	r.at = time.Now().Add(-3 * time.Minute)
	if _, err := r.signIn(person("maya@example.com", true), nil); reasonOf(err) != "bad_time" {
		t.Fatalf("a clock 3 minutes slow: %v", err)
	}
}

func TestABadSignatureOrAlgorithmIsRefused(t *testing.T) {
	r := newRig(t)
	// Signed by a key the provider doesn't publish, under a published kid.
	stray := fakeoidc.NewKey("RS256")
	stray.Kid = r.p.Signer().Kid
	r.p.SignWith = stray
	if _, err := r.signIn(person("maya@example.com", true), nil); reasonOf(err) != "bad_signature" {
		t.Fatalf("a forged signature: %v", err)
	}
	// Under a kid nobody published (even after asking for the keys again).
	refetchGap = 0
	t.Cleanup(func() { refetchGap = 30 * time.Second })
	r.p.SignWith = fakeoidc.NewKey("RS256")
	if _, err := r.signIn(person("maya@example.com", true), nil); reasonOf(err) != "bad_signature" {
		t.Fatalf("an unknown key: %v", err)
	}
	r.p.SignWith = nil

	// Tokens made by hand: none, HS256 with the public key as the secret, crit,
	// an access token's typ, padding.
	d, err := r.c.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": r.p.Issuer, "aud": "metarush-test-app", "sub": "x", "nonce": "n", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
	good := r.p.Signer().Sign(claims)
	parts := strings.Split(good, ".")
	hdr := func(h map[string]any) string { b, _ := json.Marshal(h); return base64.RawURLEncoding.EncodeToString(b) }
	for name, raw := range map[string]string{
		"alg none":     hdr(map[string]any{"alg": "none"}) + "." + parts[1] + ".",
		"alg none sig": hdr(map[string]any{"alg": "none", "kid": r.p.Signer().Kid}) + "." + parts[1] + "." + parts[2],
		"HS256":        hdr(map[string]any{"alg": "HS256", "kid": r.p.Signer().Kid}) + "." + parts[1] + "." + parts[2],
		"crit":         hdr(map[string]any{"alg": "RS256", "kid": r.p.Signer().Kid, "crit": []string{"exp"}}) + "." + parts[1] + "." + parts[2],
		"access token": hdr(map[string]any{"alg": "RS256", "kid": r.p.Signer().Kid, "typ": "at+jwt"}) + "." + parts[1] + "." + parts[2],
		"padding":      parts[0] + "=." + parts[1] + "." + parts[2],
		"two parts":    parts[0] + "." + parts[1],
		"payload swap": parts[0] + "." + base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"x"}`)) + "." + parts[2],
	} {
		if _, err := r.c.Verify(context.Background(), d, raw, "n"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := r.c.Verify(context.Background(), d, good, "n"); err != nil {
		t.Fatalf("the untouched token: %v", err)
	}
}

func TestKeysAreRotatedAndRefetchedSparingly(t *testing.T) {
	r := newRig(t)
	if _, err := r.signIn(person("maya@example.com", true), nil); err != nil {
		t.Fatal(err)
	}
	if n := r.p.Count("/jwks"); n != 1 {
		t.Fatalf("keys read %d times", n)
	}
	if _, err := r.signIn(person("maya@example.com", true), nil); err != nil || r.p.Count("/jwks") != 1 {
		t.Fatalf("kept keys: %v, read %d times", err, r.p.Count("/jwks"))
	}
	// The provider rotates its key: within the gap, a token under the new key
	// isn't trusted yet (no flood of key reads)…
	r.p.Rotate("ES256", false)
	if _, err := r.signIn(person("maya@example.com", true), nil); reasonOf(err) != "bad_signature" {
		t.Fatalf("within the gap: %v", err)
	}
	if n := r.p.Count("/jwks"); n != 1 {
		t.Fatalf("keys read %d times within the gap", n)
	}
	// …after it, the new key is read once and used (an EC key this time).
	refetchGap = 0
	t.Cleanup(func() { refetchGap = 30 * time.Second })
	if _, err := r.signIn(person("maya@example.com", true), nil); err != nil {
		t.Fatalf("after rotation: %v", err)
	}
	if n := r.p.Count("/jwks"); n != 2 {
		t.Fatalf("keys read %d times after rotation", n)
	}
	// Cache-Control max-age is kept within bounds.
	for h, want := range map[string]time.Duration{"public, max-age=23420, must-revalidate": 23420 * time.Second, "max-age=1": keysMin, "max-age=999999": keysMax, "": keysDefault, "no-store": keysDefault} {
		hh := http.Header{}
		hh.Set("Cache-Control", h)
		if got := maxAge(hh); got != want {
			t.Errorf("%q: %v, want %v", h, got, want)
		}
	}
}

func TestTheExchangeChecksPKCEAndUsesACodeOnce(t *testing.T) {
	r := newRig(t)
	// A verifier that isn't the one the challenge came from.
	if _, err := r.signIn(person("maya@example.com", true), func(_ *Request, v *string) { *v = Random(48) }); reasonOf(err) != "bad_code" {
		t.Fatalf("wrong verifier: %v", err)
	}
	// No verifier at all.
	if _, err := r.signIn(person("maya@example.com", true), func(_ *Request, v *string) { *v = "" }); reasonOf(err) != "bad_code" {
		t.Fatalf("no verifier: %v", err)
	}
	// A request without a challenge is refused by a provider that wants one.
	if _, err := r.signIn(person("maya@example.com", true), func(q *Request, _ *string) { q.Challenge = "" }); err == nil {
		t.Fatal("a sign-in without PKCE went through")
	}
	// A code works once.
	r.p.Next = &fakeoidc.Person{Sub: "s", Email: "maya@example.com", EmailVerified: fakeoidc.Verified(true)}
	verifier := Random(48)
	req := Request{Redirect: redirect, State: Random(32), Nonce: Random(32), Challenge: Challenge(verifier)}
	u, _ := r.c.AuthURL(context.Background(), req)
	code, err := r.code(u, req.State)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Exchange(context.Background(), code, verifier, redirect, req.Nonce); err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.Exchange(context.Background(), code, verifier, redirect, req.Nonce); reasonOf(err) != "bad_code" {
		t.Fatalf("a replayed code: %v", err)
	}
	// The wrong client secret.
	r.c.Secret = Random(24)
	if _, err := r.signIn(person("maya@example.com", true), nil); reasonOf(err) != "bad_client" {
		t.Fatalf("wrong secret: %v", err)
	}
	// The person said no at the provider.
	r.c.Secret = r.p.Secret
	r.p.Deny = true
	if _, err := r.signIn(person("maya@example.com", true), nil); err == nil || err.Error() != "access_denied" {
		t.Fatalf("denied: %v", err)
	}
}

func TestDiscoveryIsChecked(t *testing.T) {
	r := newRig(t)
	r.p.Issuer = "http://127.0.0.1:1/somewhere-else"
	if _, err := r.c.Discover(context.Background()); reasonOf(err) != "bad_issuer" {
		t.Fatalf("another issuer: %v", err)
	}
	for _, bad := range []string{"ftp://example.com", "https://user:pw@example.com", "https://example.com/?x=1", "http://example.com", "not a url", "https://example.com#x", "https://accounts.google.com"} {
		if err := CheckIssuer(bad); err == nil {
			t.Errorf("%q accepted (real providers are refused under go test too)", bad)
		}
	}
	if CheckIssuer("http://127.0.0.1:5791") != nil || CheckIssuer("http://localhost:5791/realms/x") != nil {
		t.Fatal("a stand-in on this computer refused")
	}
}

func TestProvidersSayWhoseEmailCounts(t *testing.T) {
	g := &Client{Kind: Google, ClientID: "a"}
	for _, tc := range []struct {
		cl              Claims
		trusted, refute bool
		domain          string
	}{
		{Claims{Email: "maya@gmail.com", EmailVerified: true}, true, false, ""},
		{Claims{Email: "maya@example.com", EmailVerified: true, HD: "example.com"}, true, false, "example.com"},
		{Claims{Email: "maya@example.com", EmailVerified: true}, false, false, ""}, // an outside address Google once checked
		{Claims{Email: "maya@gmail.com", EmailVerified: false}, false, true, ""},
		{Claims{}, false, false, ""},
	} {
		id := g.identity(&tc.cl)
		if id.EmailTrusted != tc.trusted || id.EmailRefuted != tc.refute || id.Domain != tc.domain || (!tc.trusted && id.Why == "") {
			t.Errorf("google %+v → %+v", tc.cl, id)
		}
	}
	m := &Client{Kind: Microsoft, ClientID: "a"}
	tid := "11111111-2222-3333-4444-555555555555"
	if id := m.identity(&Claims{Email: "maya@example.com", TID: tid, OID: "o1", Subject: "s1"}); id.EmailTrusted || id.Domain != tid || id.Why == "" {
		t.Errorf("microsoft without xms_edov: %+v", id)
	}
	if id := m.identity(&Claims{Email: "maya@example.com", TID: tid, OID: "o1", Subject: "s1", EmailDomainVerified: true}); !id.EmailTrusted {
		t.Errorf("microsoft with xms_edov: %+v", id)
	}
	// The same person in another app is the same key (oid), and in another directory isn't.
	a := m.identity(&Claims{TID: tid, OID: "o1", Subject: "s1"})
	b := m.identity(&Claims{TID: tid, OID: "o1", Subject: "s2"})
	c := m.identity(&Claims{TID: "99999999-2222-3333-4444-555555555555", OID: "o1", Subject: "s1"})
	if a.Key != b.Key || a.Key == c.Key {
		t.Errorf("keys: %s %s %s", a.Key, b.Key, c.Key)
	}
	o := &Client{Kind: OIDC, ClientID: "a"}
	if id := o.identity(&Claims{Email: "Maya@Example.com", EmailVerified: true}); !id.EmailTrusted || id.Domain != "example.com" {
		t.Errorf("oidc: %+v", id)
	}
	if id := o.identity(&Claims{Email: "maya@example.com"}); id.EmailTrusted || id.Domain != "" {
		t.Errorf("oidc unverified: %+v", id)
	}
	if id := o.identity(&Claims{Email: "two@@example.com", EmailVerified: true}); id.Email != "" || id.EmailTrusted {
		t.Errorf("oidc odd email: %+v", id)
	}
}

func TestMicrosoftIssuers(t *testing.T) {
	m := &Client{Kind: Microsoft, Issuer: MicrosoftIssuer("organizations")}
	tid := "11111111-2222-3333-4444-555555555555"
	tpl := &Discovery{Issuer: microsoftPrefix + "{tenantid}/v2.0"}
	// The document for organizations names {tenantid}: each token fills it in.
	// (Its addresses here are on this computer: tests never reach Microsoft.)
	doc := &Discovery{Issuer: tpl.Issuer, AuthorizationEndpoint: "https://127.0.0.1:1/a", TokenEndpoint: "https://127.0.0.1:1/t", JWKSURI: "https://127.0.0.1:1/k"}
	if err := m.checkDiscovery(doc); err != nil || !doc.tenantTemplate {
		t.Fatalf("organizations: %v %+v", err, doc)
	}
	// Another provider's document under Microsoft's name is refused.
	if err := m.checkDiscovery(&Discovery{Issuer: "https://login.example.net/v2.0", AuthorizationEndpoint: doc.AuthorizationEndpoint, TokenEndpoint: doc.TokenEndpoint, JWKSURI: doc.JWKSURI}); reasonOf(err) != "bad_issuer" {
		t.Fatalf("another issuer: %v", err)
	}
	tpl.tenantTemplate = true
	for _, tc := range []struct {
		iss, tid string
		ok       bool
	}{
		{microsoftPrefix + tid + "/v2.0", tid, true},
		{microsoftPrefix + tid + "/v2.0", "99999999-2222-3333-4444-555555555555", false}, // another directory's issuer
		{microsoftPrefix + "{tenantid}/v2.0", tid, false},
		{microsoftPrefix + "common/v2.0", "common", false},
		{"https://sts.windows.net/" + tid + "/", tid, false}, // a v1 token
	} {
		if got := m.issuerOK(tpl, &Claims{Issuer: tc.iss, TID: tc.tid}); got != tc.ok {
			t.Errorf("%s %s: %v", tc.iss, tc.tid, got)
		}
	}
	// One directory: exactly its issuer.
	one := &Discovery{Issuer: microsoftPrefix + tid + "/v2.0"}
	if !m.issuerOK(one, &Claims{Issuer: one.Issuer, TID: tid}) || m.issuerOK(one, &Claims{Issuer: microsoftPrefix + "99999999-2222-3333-4444-555555555555/v2.0", TID: "99999999-2222-3333-4444-555555555555"}) {
		t.Error("one directory")
	}
	// A key that names its issuer signs only for it.
	if !keyIssuerOK(microsoftPrefix+"{tenantid}/v2.0", &Claims{Issuer: microsoftPrefix + tid + "/v2.0", TID: tid}) ||
		keyIssuerOK(microsoftPrefix+MicrosoftPersonal+"/v2.0", &Claims{Issuer: microsoftPrefix + tid + "/v2.0", TID: tid}) {
		t.Error("key issuers")
	}
	// Google's two spellings.
	g := &Client{Kind: Google}
	gd := &Discovery{Issuer: GoogleIssuer}
	if !g.issuerOK(gd, &Claims{Issuer: "accounts.google.com"}) || !g.issuerOK(gd, &Claims{Issuer: GoogleIssuer}) || g.issuerOK(gd, &Claims{Issuer: "https://accounts.google.com/"}) {
		t.Error("google issuers")
	}
}

func TestKeysThatCantBeUsedAreLeftOut(t *testing.T) {
	small, _ := rsa.GenerateKey(rand.Reader, 1024)
	b := func(x []byte) string { return base64.RawURLEncoding.EncodeToString(x) }
	set, _ := json.Marshal(map[string]any{"keys": []any{
		map[string]any{"kty": "RSA", "kid": "small", "n": b(small.N.Bytes()), "e": "AQAB"},
		map[string]any{"kty": "RSA", "kid": "enc", "use": "enc", "n": b(make([]byte, 256)), "e": "AQAB"},
		map[string]any{"kty": "EC", "kid": "off-curve", "crv": "P-256", "x": b(make([]byte, 32)), "y": b(make([]byte, 32))},
		map[string]any{"kty": "oct", "kid": "shared", "k": "c2VjcmV0"},
		"not a key",
		fakeoidcPublic(t),
	}})
	keys, err := parseKeys(set)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].kid != "good" {
		t.Fatalf("kept %d keys", len(keys))
	}
}

func fakeoidcPublic(t *testing.T) map[string]any {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	return map[string]any{"kty": "RSA", "kid": "good", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": "AQAB"}
}

func TestSignInsInProgressWorkOnce(t *testing.T) {
	st := FlowsIn(t.TempDir())
	ticket, err := st.NewTicket(Flow{Purpose: ForSignIn, Redirect: redirect, Next: "/p/x", Setup: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	f, state, err := st.Begin(ticket)
	if err != nil || f.Nonce == "" || f.Verifier == "" || state == "" || f.Next != "/p/x" {
		t.Fatalf("begin: %v %+v", err, f)
	}
	if _, _, err := st.Begin(ticket); !errors.Is(err, ErrGone) {
		t.Fatalf("a ticket opened twice: %v", err)
	}
	// Nothing on disk says the ticket, the state, the nonce or the verifier by name.
	_ = filepath.Walk(st.Dir(), func(p string, info os.FileInfo, err error) error {
		if strings.Contains(p, ticket) || strings.Contains(p, state) {
			t.Errorf("a file named by a secret: %s", p)
		}
		if info != nil && !info.IsDir() && info.Mode().Perm() != 0o600 {
			t.Errorf("%s is %v", p, info.Mode().Perm())
		}
		if info != nil && info.IsDir() && info.Mode().Perm() != 0o700 {
			t.Errorf("%s is %v", p, info.Mode().Perm())
		}
		return nil
	})
	got, err := st.Finish(state)
	if err != nil || got.Nonce != f.Nonce || got.Verifier != f.Verifier {
		t.Fatalf("finish: %v", err)
	}
	if _, err := st.Finish(state); !errors.Is(err, ErrGone) {
		t.Fatalf("an answer replayed: %v", err)
	}
	for _, bad := range []string{"", "short", "../../etc/passwd/../../../../x", strings.Repeat("a", 200)} {
		if _, err := st.Finish(bad); err == nil {
			t.Errorf("%q finished", bad)
		}
	}
	// A test result is kept for this computer and goes stale.
	if err := st.SaveTest(TestResult{Setup: "s1", UserID: "usr_1", OK: true}); err != nil {
		t.Fatal(err)
	}
	if r := st.LastTest(); r == nil || !r.OK || !r.Fresh() {
		t.Fatalf("test: %+v", r)
	}
}
