package providers

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
)

// Google Cloud's Vertex AI (D282: Veo through a person's own Google Cloud
// project) takes no API key for Veo: it wants a short-lived access token for
// an identity in the project. A person creates a service account with the
// Vertex AI User role, downloads its JSON key and gives it to MetaRush once
// (metarush providers set vertex --key-stdin < key.json; never an argument).
// MetaRush keeps only what it needs of it — the project, the account's
// address and its private key — where it keeps every key (this machine's
// keychain, or the installation's private .env), and signs a token request
// with it for each hour of use; the private key itself never leaves this
// computer, and the token is never written anywhere (the log hides it).

// VertexName is the integration's ID.
const VertexName = "vertex"

// The token endpoint and the API host can be pointed at a server on this
// computer (tests, browser runs); nothing else is accepted.
const (
	EnvVertexBaseURL  = "METARUSH_VERTEX_BASE_URL"
	EnvVertexTokenURL = "METARUSH_VERTEX_TOKEN_URL"
)

// googleTokenURL is the only token endpoint a key's assertion goes to.
const googleTokenURL = "https://oauth2.googleapis.com/token"

// vertexScope is what the token may do: Google Cloud's APIs (Vertex AI
// accepts no narrower scope).
const vertexScope = "https://www.googleapis.com/auth/cloud-platform"

// VertexKey is a service account's key, as MetaRush keeps it.
type VertexKey struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`

	rsa *rsa.PrivateKey
}

var projectRe = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

// ParseVertexKey reads a service account's JSON key (as Google gives it, or
// as MetaRush keeps it) and checks it is one: a service account, a project,
// an address and an RSA private key, and Google's own token endpoint.
func ParseVertexKey(raw string) (*VertexKey, error) {
	var k VertexKey
	bad := func(f string, a ...any) error {
		return apperr.New(apperr.InvalidInput, f, a...).WithHint("Give MetaRush the key file Google Cloud made for a service account (its Keys tab → Add key → Create new key).")
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &k); err != nil {
		return nil, bad("That isn't a service account's key file.")
	}
	if k.Type != "service_account" {
		return nil, bad("That key file isn't a service account's (its type is “%s”).", k.Type)
	}
	if !projectRe.MatchString(k.ProjectID) {
		return nil, bad("That key names no Google Cloud project MetaRush can use.")
	}
	if !strings.Contains(k.ClientEmail, "@") || strings.ContainsAny(k.ClientEmail, " \t\r\n") {
		return nil, bad("That key names no service account.")
	}
	if k.TokenURI == "" {
		k.TokenURI = googleTokenURL
	}
	if k.TokenURI != googleTokenURL {
		return nil, bad("That key sends its sign-in somewhere other than Google, so MetaRush won't use it.")
	}
	block, _ := pem.Decode([]byte(k.PrivateKey))
	if block == nil {
		return nil, bad("That key has no private key in it.")
	}
	pk, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		if pk1, err1 := x509.ParsePKCS1PrivateKey(block.Bytes); err1 == nil {
			pk, err = pk1, nil
		}
	}
	rk, ok := pk.(*rsa.PrivateKey)
	if err != nil || !ok {
		return nil, bad("That key's private key isn't one MetaRush can sign with.")
	}
	k.rsa = rk
	return &k, nil
}

// Kept is the key as MetaRush keeps it: its fields on one line, nothing else.
func (k *VertexKey) Kept() string {
	b, _ := json.Marshal(k)
	return string(b)
}

// Account is the service account's address (shown instead of a key's last characters).
func (k *VertexKey) Account() string { return k.ClientEmail }

// NormalizeVertexKey is a JSON key as MetaRush keeps it (one line).
func NormalizeVertexKey(raw string) (string, error) {
	k, err := ParseVertexKey(raw)
	if err != nil {
		return "", err
	}
	return k.Kept(), nil
}

// VertexAuthError is a token request Google refused or couldn't answer.
type VertexAuthError struct {
	Status  int    // 0: the request didn't get through
	Reason  string // Google's error code (invalid_grant…)
	Message string
}

func (e *VertexAuthError) Error() string { return e.Message }

type vertexToken struct {
	value string
	until time.Time
}

var (
	vertexMu     sync.Mutex
	vertexTokens = map[string]vertexToken{}
	// VertexHTTP is the client for token requests (tests may replace it).
	VertexHTTP = &http.Client{Timeout: 30 * time.Second}
)

// VertexAccessToken is an access token for a kept key, minted by signing a
// token request with its private key (RS256) and kept in memory while it is
// good (an hour, less five minutes). It is never written anywhere.
func VertexAccessToken(ctx context.Context, key string) (string, error) {
	k, err := ParseVertexKey(key)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(k.Kept()))
	id := hex.EncodeToString(sum[:])
	vertexMu.Lock()
	if t, ok := vertexTokens[id]; ok && time.Now().Before(t.until) {
		vertexMu.Unlock()
		return t.value, nil
	}
	vertexMu.Unlock()
	tokenURL, err := VertexTokenURL()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	assertion, err := k.sign(now, k.TokenURI)
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	call := logbook.ProviderCall{Provider: VertexName, Title: "Google Cloud", What: "sign in to Google Cloud", Method: http.MethodPost, Endpoint: "/token"}
	start := time.Now()
	res, err := VertexHTTP.Do(req)
	call.Duration = time.Since(start)
	if err != nil {
		logbook.ProviderDone(ctx, call, err)
		return "", &VertexAuthError{Message: "MetaRush couldn't reach Google Cloud to sign in. Check the internet connection; it tries again by itself."}
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	call.Status, call.Bytes = res.StatusCode, len(body)
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &out)
	if out.AccessToken != "" {
		logbook.RememberSecret(out.AccessToken) // the log never shows it
	}
	logbook.ProviderDone(ctx, call, nil)
	if res.StatusCode != http.StatusOK || out.AccessToken == "" {
		msg := "Google Cloud didn't accept the service account's key. It may have been deleted or turned off: make a new key and give it to MetaRush again."
		if res.StatusCode >= 500 {
			msg = "Google Cloud had a problem signing in. MetaRush tries again by itself."
		}
		return "", &VertexAuthError{Status: res.StatusCode, Reason: out.Error, Message: msg}
	}
	life := time.Duration(max(out.ExpiresIn, 600)) * time.Second
	vertexMu.Lock()
	vertexTokens[id] = vertexToken{value: out.AccessToken, until: now.Add(life - 5*time.Minute)}
	vertexMu.Unlock()
	return out.AccessToken, nil
}

// ForgetVertexTokens drops the tokens kept in memory (a key removed or replaced; tests).
func ForgetVertexTokens() {
	vertexMu.Lock()
	vertexTokens = map[string]vertexToken{}
	vertexMu.Unlock()
}

// sign makes the signed token request (a JWT) for an hour of access.
func (k *VertexKey) sign(now time.Time, aud string) (string, error) {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	head := map[string]string{"alg": "RS256", "typ": "JWT"}
	if k.PrivateKeyID != "" {
		head["kid"] = k.PrivateKeyID
	}
	claims := map[string]any{"iss": k.ClientEmail, "scope": vertexScope, "aud": aud, "iat": now.Unix(), "exp": now.Add(time.Hour).Unix()}
	signing := enc(head) + "." + enc(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.rsa, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// VertexBaseURL is the Vertex AI host for a region, or the override. Under
// go test and in browser runs (RealServicesRefused) only a server on this
// computer is accepted, so a test can never reach Google Cloud (and spend).
func VertexBaseURL(region string) (string, error) {
	if !regionRe.MatchString(region) {
		return "", apperr.New(apperr.InvalidInput, "“%s” isn't a Google Cloud region.", region)
	}
	base := "https://" + region + "-aiplatform.googleapis.com"
	if o := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvVertexBaseURL)), "/"); o != "" {
		if !localElevenBase(o) {
			return "", apperr.New(apperr.NotConfigured, "%s must be a server on this computer.", EnvVertexBaseURL)
		}
		base = o
	}
	if RealServicesRefused() && !localElevenBase(base) {
		return "", apperr.New(apperr.NotConfigured, "Tests never call Google Cloud: set %s to a fake on this computer.", EnvVertexBaseURL)
	}
	return base, nil
}

// VertexTokenURL is Google's token endpoint, or the override (on this computer only).
func VertexTokenURL() (string, error) {
	u := googleTokenURL
	if o := strings.TrimSpace(os.Getenv(EnvVertexTokenURL)); o != "" {
		if !localElevenBase(o) {
			return "", apperr.New(apperr.NotConfigured, "%s must be a server on this computer.", EnvVertexTokenURL)
		}
		u = o
	}
	if RealServicesRefused() && !localElevenBase(u) {
		return "", apperr.New(apperr.NotConfigured, "Tests never sign in to Google Cloud: set %s to a fake on this computer.", EnvVertexTokenURL)
	}
	return u, nil
}

var regionRe = regexp.MustCompile(`^[a-z]+-[a-z]+[0-9]$`)

// VertexKeyWords says which key is kept, for people: the service account,
// never the key.
func VertexKeyWords(key string) string {
	k, err := ParseVertexKey(key)
	if err != nil {
		return "a service account key"
	}
	return fmt.Sprintf("the service account %s (project %s)", k.ClientEmail, k.ProjectID)
}

// KeyShaper is an integration whose key isn't a one-line API key (a
// service account's JSON key): it checks and rewrites what a person gives it
// into what MetaRush keeps, and says which key it is without showing it.
type KeyShaper interface {
	NormalizeKey(raw string) (string, error)
	DescribeKey(key string) string
}

// maxShapedKey is the longest key a KeyShaper may keep (what the keychain holds).
const maxShapedKey = 3000
