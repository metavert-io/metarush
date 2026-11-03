// Package fakeoidc is a stand-in for an OpenID Connect provider on this
// computer, for tests of signing in with an organization's account (D432):
// a discovery document, a key set made at run time (rotated on request), an
// authorization page and a token endpoint that checks what a real provider
// checks — the client and its secret, the redirect address registered, PKCE
// (S256), a code used once — and signs ID tokens a test can bend (another
// audience, an old time, a key nobody published). Nothing ever reaches a
// real provider; nothing here is a real key.
package fakeoidc

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Person is who signs in at the stand-in's page.
type Person struct {
	Sub           string
	Email         string
	EmailVerified *bool // nil: the claim is left out
	Name          string
	// Extra claims (hd, tid, oid, xms_edov…).
	Extra map[string]any
}

// Key is one signing key.
type Key struct {
	Kid string
	Alg string // RS256 or ES256
	rsa *rsa.PrivateKey
	ec  *ecdsa.PrivateKey
}

// Server is the stand-in.
type Server struct {
	*httptest.Server
	mu sync.Mutex
	// Issuer is what the discovery document and tokens name (the server's own
	// address unless a test changes it).
	Issuer string
	// ClientID and Secret are the registered app ("" secret: a public client).
	ClientID, Secret string
	// Redirects are the registered redirect addresses (empty: any).
	Redirects []string
	// Next is who signs in next at the authorization page (the browser run's
	// page asks instead).
	Next *Person
	// Ask makes the authorization page a form a person fills in (browser runs).
	Ask bool
	// Deny answers the next sign-in with access_denied.
	Deny bool
	// Bend changes the next ID token's claims (a test's wrong audience…).
	Bend func(claims map[string]any)
	// SignWith signs ID tokens with a key the key set doesn't publish.
	SignWith *Key
	// AllowNoPKCE lets a sign-in without a code challenge through (to see that
	// MetaRush always sends one, a test leaves it off).
	AllowNoPKCE bool
	// PostOnly lists only client_secret_post for the token endpoint.
	PostOnly bool
	// MaxAge is the Cache-Control max-age of the key set (0: none).
	MaxAge int

	keys     []*Key // published; the first signs
	codes    map[string]*grant
	Requests map[string]int
	// Seen are the authorization requests' parameters, in order.
	Seen []url.Values
}

type grant struct {
	person            Person
	redirect, nonce   string
	challenge, method string
	used              bool
	issued            time.Time
}

// New starts a stand-in with one RSA key.
func New(clientID, secret string) *Server {
	s := &Server{ClientID: clientID, Secret: secret, codes: map[string]*grant{}, Requests: map[string]int{}}
	s.keys = []*Key{NewKey("RS256")}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	s.Issuer = s.URL
	return s
}

// NewKey makes a signing key (RS256 or ES256) at run time.
func NewKey(alg string) *Key {
	k := &Key{Kid: "k-" + random(6), Alg: alg}
	var err error
	switch alg {
	case "ES256":
		k.ec, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	default:
		k.Alg = "RS256"
		k.rsa, err = rsa.GenerateKey(rand.Reader, 2048)
	}
	if err != nil {
		panic(err)
	}
	return k
}

// Rotate starts signing with a new key; keep says whether the old one stays
// published.
func (s *Server) Rotate(alg string, keep bool) *Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := NewKey(alg)
	if keep {
		s.keys = append([]*Key{k}, s.keys...)
	} else {
		s.keys = []*Key{k}
	}
	return k
}

// Signer is the key signing ID tokens now.
func (s *Server) Signer() *Key {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keys[0]
}

// Count is how many requests reached a path.
func (s *Server) Count(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Requests[path]
}

func random(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.Requests[r.URL.Path]++
	s.mu.Unlock()
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		s.discovery(w)
	case "/jwks":
		s.jwks(w)
	case "/authorize":
		s.authorize(w, r)
	case "/token":
		s.token(w, r)
	default:
		http.NotFound(w, r)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) discovery(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	methods := []string{"client_secret_basic", "client_secret_post"}
	if s.PostOnly {
		methods = []string{"client_secret_post"}
	}
	writeJSON(w, 200, map[string]any{
		"issuer": s.Issuer, "authorization_endpoint": s.URL + "/authorize", "token_endpoint": s.URL + "/token", "jwks_uri": s.URL + "/jwks",
		"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256", "ES256"}, "token_endpoint_auth_methods_supported": methods,
		"code_challenge_methods_supported": []string{"S256"}, "scopes_supported": []string{"openid", "email", "profile"},
	})
}

func (s *Server) jwks(w http.ResponseWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []map[string]any
	for _, k := range s.keys {
		keys = append(keys, k.public())
	}
	if s.MaxAge > 0 {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", s.MaxAge))
	}
	writeJSON(w, 200, map[string]any{"keys": keys})
}

func (k *Key) public() map[string]any {
	if k.ec != nil {
		size := 32
		x, y := make([]byte, size), make([]byte, size)
		k.ec.X.FillBytes(x)
		k.ec.Y.FillBytes(y)
		return map[string]any{"kty": "EC", "crv": "P-256", "kid": k.Kid, "use": "sig", "alg": "ES256", "x": b64(x), "y": b64(y)}
	}
	return map[string]any{"kty": "RSA", "kid": k.Kid, "use": "sig", "alg": "RS256", "n": b64(k.rsa.N.Bytes()), "e": b64(big.NewInt(int64(k.rsa.E)).Bytes())}
}

// Sign makes a token over claims with key k (tests make their own too).
func (k *Key) Sign(claims map[string]any) string {
	h, _ := json.Marshal(map[string]any{"alg": k.Alg, "kid": k.Kid, "typ": "JWT"})
	p, _ := json.Marshal(claims)
	signed := b64(h) + "." + b64(p)
	sum := sha256.Sum256([]byte(signed))
	var sig []byte
	if k.ec != nil {
		r, sv, err := ecdsa.Sign(rand.Reader, k.ec, sum[:])
		if err != nil {
			panic(err)
		}
		sig = make([]byte, 64)
		r.FillBytes(sig[:32])
		sv.FillBytes(sig[32:])
	} else {
		var err error
		if sig, err = rsa.SignPKCS1v15(rand.Reader, k.rsa, crypto.SHA256, sum[:]); err != nil {
			panic(err)
		}
	}
	return signed + "." + b64(sig)
}

func redirectWith(w http.ResponseWriter, r *http.Request, to string, q url.Values) {
	u, _ := url.Parse(to)
	v := u.Query()
	for k, vs := range q {
		v[k] = vs
	}
	u.RawQuery = v.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		_ = r.ParseForm()
		q = r.Form
	}
	s.mu.Lock()
	s.Seen = append(s.Seen, q)
	clientOK := q.Get("client_id") == s.ClientID
	redirectOK := len(s.Redirects) == 0
	for _, u := range s.Redirects {
		redirectOK = redirectOK || u == q.Get("redirect_uri")
	}
	deny, ask, next, allowNoPKCE := s.Deny, s.Ask, s.Next, s.AllowNoPKCE
	s.Deny = false
	s.mu.Unlock()
	if !clientOK || !redirectOK {
		// Like a real provider: never back to an address it doesn't know.
		http.Error(w, "unknown client or redirect address", http.StatusBadRequest)
		return
	}
	redirect, state := q.Get("redirect_uri"), q.Get("state")
	if q.Get("response_type") != "code" || !strings.Contains(" "+q.Get("scope")+" ", " openid ") {
		redirectWith(w, r, redirect, url.Values{"error": {"unsupported_response_type"}, "state": {state}})
		return
	}
	if !allowNoPKCE && (q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256") {
		redirectWith(w, r, redirect, url.Values{"error": {"invalid_request"}, "error_description": {"PKCE required"}, "state": {state}})
		return
	}
	if deny {
		redirectWith(w, r, redirect, url.Values{"error": {"access_denied"}, "state": {state}})
		return
	}
	var p Person
	switch {
	case ask && r.Method == http.MethodGet:
		s.page(w, q)
		return
	case ask:
		email := strings.TrimSpace(q.Get("fake_email"))
		yes := q.Get("fake_verified") != ""
		p = Person{Sub: "sub-" + strings.ToLower(email), Email: email, EmailVerified: &yes, Name: strings.TrimSpace(q.Get("fake_name"))}
		if hd := strings.TrimSpace(q.Get("fake_hd")); hd != "" {
			p.Extra = map[string]any{"hd": hd}
		}
	case next != nil:
		p = *next
	default:
		http.Error(w, "nobody to sign in", http.StatusBadRequest)
		return
	}
	code := random(24)
	s.mu.Lock()
	s.codes[code] = &grant{person: p, redirect: redirect, nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), method: q.Get("code_challenge_method"), issued: time.Now()}
	s.mu.Unlock()
	redirectWith(w, r, redirect, url.Values{"code": {code}, "state": {state}})
}

// page is the browser run's sign-in page: an email, verified or not, and a name.
func (s *Server) page(w http.ResponseWriter, q url.Values) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	var hidden strings.Builder
	for k, vs := range q {
		for _, v := range vs {
			fmt.Fprintf(&hidden, `<input type="hidden" name="%s" value="%s">`, html.EscapeString(k), html.EscapeString(v))
		}
	}
	fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>Test provider</title>
<style>body{font:15px system-ui;margin:3em;max-width:30em}label{display:block;margin:.8em 0}input[type=email],input[type=text]{width:100%%;padding:.4em}</style></head>
<body><h1>Sign in to the test provider</h1><p data-fake-provider>A stand-in for an organization's sign-in. Nothing here is real.</p>
<form method="post" action="/authorize" data-fake-form>%s
<label>Email <input type="email" name="fake_email" value="%s" data-fake-email></label>
<label>Name <input type="text" name="fake_name" value="" data-fake-name></label>
<label>Workspace domain <input type="text" name="fake_hd" value="" data-fake-hd></label>
<label><input type="checkbox" name="fake_verified" value="1" checked data-fake-verified> The provider has checked this email</label>
<button type="submit" data-fake-continue>Continue</button></form></body></html>`, hidden.String(), html.EscapeString(q.Get("login_hint")))
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]any{"error": "invalid_request"})
		return
	}
	_ = r.ParseForm()
	id, secret, basic := r.BasicAuth()
	if basic {
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if id != s.ClientID || (s.Secret != "" && subtle.ConstantTimeCompare([]byte(secret), []byte(s.Secret)) != 1) || (basic && s.PostOnly) {
		writeJSON(w, 401, map[string]any{"error": "invalid_client"})
		return
	}
	if r.PostForm.Get("grant_type") != "authorization_code" {
		writeJSON(w, 400, map[string]any{"error": "unsupported_grant_type"})
		return
	}
	g := s.codes[r.PostForm.Get("code")]
	if g == nil || g.used || time.Since(g.issued) > 5*time.Minute || g.redirect != r.PostForm.Get("redirect_uri") {
		if g != nil {
			g.used = true // a code tried twice is spent (RFC 6749 4.1.2)
		}
		writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
		return
	}
	g.used = true
	if g.challenge != "" || !s.AllowNoPKCE {
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if g.method != "S256" || subtle.ConstantTimeCompare([]byte(b64(sum[:])), []byte(g.challenge)) != 1 {
			writeJSON(w, 400, map[string]any{"error": "invalid_grant", "error_description": "PKCE verification failed"})
			return
		}
	}
	now := time.Now()
	claims := map[string]any{"iss": s.Issuer, "sub": g.person.Sub, "aud": s.ClientID, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "nonce": g.nonce}
	if g.person.Email != "" {
		claims["email"] = g.person.Email
	}
	if g.person.EmailVerified != nil {
		claims["email_verified"] = *g.person.EmailVerified
	}
	if g.person.Name != "" {
		claims["name"] = g.person.Name
	}
	for k, v := range g.person.Extra {
		claims[k] = v
	}
	if s.Bend != nil {
		s.Bend(claims)
		s.Bend = nil
	}
	signer := s.keys[0]
	if s.SignWith != nil {
		signer = s.SignWith
	}
	writeJSON(w, 200, map[string]any{"access_token": "at-" + random(24), "token_type": "Bearer", "expires_in": 3600, "id_token": signer.Sign(claims)})
}

// Verified is a pointer to true or false, for Person.EmailVerified.
func Verified(v bool) *bool { return &v }
