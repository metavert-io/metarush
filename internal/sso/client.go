// Package sso signs people in with their organization's account through
// OpenID Connect (D425–D434): Google, Microsoft (Entra ID) or any provider
// that publishes a discovery document. It is the protocol only — discovery,
// the provider's signing keys, the address the browser is sent to, the
// exchange of the answer's code (PKCE, RFC 7636) and the checks on the
// signed ID token (OpenID Connect Core 3.1.3.7) — plus the short-lived
// records of sign-ins in progress on this computer (flows.go). Who may sign
// in, sessions and the endpoints live in internal/auth and internal/api.
//
// Nothing from the provider is kept but the person's identity (who signed in:
// the provider's account ID and email). The code, the tokens, the state, the
// nonce and the PKCE verifier are told to the activity log's redaction and
// never written to the database, config.yaml or a log; tokens are dropped as
// soon as they are checked.
package sso

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/providers"
)

// Kinds of provider.
const (
	Google    = "google"
	Microsoft = "microsoft"
	OIDC      = "oidc"
)

// Fixed addresses of the providers MetaRush knows.
const (
	GoogleIssuer    = "https://accounts.google.com"
	microsoftHost   = "login.microsoftonline.com"
	microsoftPrefix = "https://" + microsoftHost + "/"
	// MicrosoftPersonal is the directory of personal Microsoft accounts
	// (outlook.com, hotmail.com…).
	MicrosoftPersonal = "9188040d-6c67-4c5b-b112-36a304b66dad"
)

// MicrosoftIssuer is the issuer of a Microsoft directory (a tenant ID, or
// organizations / common / consumers).
func MicrosoftIssuer(tenant string) string {
	return microsoftPrefix + strings.ToLower(strings.TrimSpace(tenant)) + "/v2.0"
}

// Title is a provider's name on a button ("Sign in with Google").
func Title(kind, label string) string {
	switch kind {
	case Google:
		return "Google"
	case Microsoft:
		return "Microsoft"
	}
	if l := strings.TrimSpace(label); l != "" {
		return l
	}
	return "your organization's account"
}

// Client talks to one provider for one registered app (its client).
type Client struct {
	Kind     string
	Issuer   string // as set up; Google's and Microsoft's are fixed by Kind
	ClientID string
	// Secret is the app's client secret ("" for an app registered without
	// one, a public client).
	Secret string
	// Directory is the Microsoft directory (tenant ID) the setup is for:
	// only that directory's own people's emails can count (D452).
	Directory string
	// Now is the time ID tokens are checked against (this computer's clock
	// by default; tests move it).
	Now func() time.Time
	// HTTP makes the requests (a 15-second client that follows no redirects
	// by default).
	HTTP *http.Client
}

var defaultHTTP = &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return defaultHTTP
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Problem is a sign-in setup or answer problem in plain words; Reason is a
// stable code (bad_issuer, no_answer, refused, bad_token…).
type Problem struct {
	Reason string
	Msg    string
	Hint   string
	// Detail is for the details behind the plain words (never a token).
	Detail string
}

func (p *Problem) Error() string { return p.Msg }

func problem(reason, msg, hint string) *Problem {
	return &Problem{Reason: reason, Msg: msg, Hint: hint}
}

// Loopback reports an address on this computer (a provider MetaRush may reach
// over plain http: a test's stand-in, or one the owner runs here).
func Loopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}
	h := u.Hostname()
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// CheckIssuer says whether an issuer address can be used: https (or http on
// this computer), no user, query or fragment. Under go test only a provider
// on this computer is ever reached (D432: never a real provider in tests).
func CheckIssuer(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(raw, " \t\r\n") {
		return problem("bad_issuer", "That isn't a sign-in provider's address MetaRush can use.", "Give the address the provider calls its issuer, like https://login.example.com.")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && Loopback(raw)) {
		return problem("bad_issuer", "A sign-in provider's address must start with https://.", "")
	}
	if realRefused() && !Loopback(raw) {
		return problem("bad_issuer", "Tests never reach a real sign-in provider: use a stand-in on this computer.", "")
	}
	return nil
}

// realRefused: only a provider on this computer is ever reached — under go
// test, and in a browser run (METARUSH_NO_PAID_SERVICES=1, scripts/lib/e2e.sh).
func realRefused() bool {
	return testing.Testing() || strings.TrimSpace(os.Getenv(providers.EnvNoPaidServices)) == "1"
}

// endpointOK says whether an address from the discovery document may be
// used: https, or http when the issuer itself is on this computer.
func (c *Client) endpointOK(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if realRefused() && !Loopback(raw) {
		return false
	}
	return u.Scheme == "https" || (u.Scheme == "http" && Loopback(raw) && Loopback(c.Issuer))
}

// get reads a provider's address (discovery, keys), at most 1 MB.
func (c *Client) get(ctx context.Context, raw, what string) ([]byte, http.Header, error) {
	if !c.endpointOK(raw) && !(raw == c.discoveryURL() && CheckIssuer(c.Issuer) == nil) {
		return nil, nil, problem("bad_issuer", "The sign-in provider named an address MetaRush won't use.", "")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, nil, problem("bad_issuer", "The sign-in provider named an address MetaRush can't use.", "")
	}
	req.Header.Set("Accept", "application/json")
	start := time.Now()
	res, err := c.http().Do(req)
	call := logbook.ProviderCall{Provider: "sso-" + c.Kind, Title: Title(c.Kind, "") + " sign-in", What: what, Method: http.MethodGet, Endpoint: pathOf(raw)}
	if err != nil {
		logbook.ProviderDone(ctx, call, errors.New("no answer"))
		return nil, nil, &Problem{Reason: "no_answer", Msg: "MetaRush couldn't reach the sign-in provider.", Hint: "Check this computer's internet connection, then try again.", Detail: netWords(err)}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	call.Status, call.Duration, call.Bytes = res.StatusCode, time.Since(start), len(b)
	if res.StatusCode != http.StatusOK {
		logbook.ProviderDone(ctx, call, errors.New(res.Status))
		return nil, nil, &Problem{Reason: "no_answer", Msg: "The sign-in provider didn't answer as expected.", Hint: "Check the provider's address, then try again.", Detail: res.Status}
	}
	logbook.ProviderDone(ctx, call, nil)
	return b, res.Header, nil
}

func pathOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host + u.Path
}

// netWords says how a request failed without its address (which a log keeps).
func netWords(err error) string {
	var ne net.Error
	switch {
	case errors.As(err, &ne) && ne.Timeout():
		return "no answer in time"
	case errors.Is(err, context.Canceled):
		return "stopped"
	}
	return "no connection"
}

/* ---------- Discovery ---------- */

// Discovery is what MetaRush reads from a provider's discovery document.
type Discovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	ResponseTypes         []string `json:"response_types_supported"`
	SigningAlgs           []string `json:"id_token_signing_alg_values_supported"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported"`
	// tenantTemplate: Microsoft's documents for common and organizations name
	// the issuer with {tenantid} in it; each token's tid fills it in.
	tenantTemplate bool
}

type cachedDiscovery struct {
	doc   *Discovery
	at    time.Time
	tried time.Time
}

var discoveries sync.Map // discovery URL → *cachedDiscovery
var discoveryMu sync.Mutex

const (
	discoveryLife  = 24 * time.Hour
	discoveryRetry = time.Minute
)

func (c *Client) discoveryURL() string {
	return strings.TrimSuffix(strings.TrimSpace(c.Issuer), "/") + "/.well-known/openid-configuration"
}

// Discover reads (and keeps for a day) the provider's discovery document and
// checks it: the issuer it names is the one set up, and every address MetaRush
// will use is https.
func (c *Client) Discover(ctx context.Context) (*Discovery, error) {
	if err := CheckIssuer(c.Issuer); err != nil {
		return nil, err
	}
	u := c.discoveryURL()
	discoveryMu.Lock()
	defer discoveryMu.Unlock()
	if v, ok := discoveries.Load(u); ok {
		cd := v.(*cachedDiscovery)
		if time.Since(cd.at) < discoveryLife || (time.Since(cd.tried) < discoveryRetry && cd.doc != nil) {
			return cd.doc, nil
		}
	}
	b, _, err := c.get(ctx, u, "read its sign-in settings")
	if err != nil {
		if v, ok := discoveries.Load(u); ok && v.(*cachedDiscovery).doc != nil {
			cd := v.(*cachedDiscovery)
			discoveries.Store(u, &cachedDiscovery{doc: cd.doc, at: cd.at, tried: time.Now()})
			return cd.doc, nil // what it said yesterday still holds while it doesn't answer
		}
		return nil, err
	}
	var d Discovery
	if json.Unmarshal(b, &d) != nil {
		return nil, problem("bad_discovery", "The sign-in provider's settings aren't in a form MetaRush can read.", "Check the provider's address.")
	}
	if err := c.checkDiscovery(&d); err != nil {
		return nil, err
	}
	discoveries.Store(u, &cachedDiscovery{doc: &d, at: time.Now(), tried: time.Now()})
	return &d, nil
}

func (c *Client) checkDiscovery(d *Discovery) error {
	want := strings.TrimSpace(c.Issuer)
	switch {
	case d.Issuer == want, strings.TrimSuffix(d.Issuer, "/") == strings.TrimSuffix(want, "/"):
	case c.Kind == Microsoft && strings.HasPrefix(want, microsoftPrefix) && d.Issuer == microsoftPrefix+"{tenantid}/v2.0":
		d.tenantTemplate = true
	case c.Kind == Microsoft && strings.HasPrefix(d.Issuer, microsoftPrefix) && strings.HasSuffix(d.Issuer, "/v2.0") && isGUID(strings.TrimSuffix(strings.TrimPrefix(d.Issuer, microsoftPrefix), "/v2.0")):
		// A directory named by its domain answers with its ID.
	default:
		return &Problem{Reason: "bad_issuer", Msg: "The sign-in provider says it is another provider than the one set up.", Hint: "Check the provider's address.", Detail: "issuer " + d.Issuer}
	}
	for _, e := range []string{d.AuthorizationEndpoint, d.TokenEndpoint, d.JWKSURI} {
		if e == "" || !c.endpointOK(e) {
			return problem("bad_discovery", "The sign-in provider's settings name an address MetaRush won't use.", "Its addresses must start with https://.")
		}
	}
	if len(d.ResponseTypes) > 0 && !contains(d.ResponseTypes, "code") {
		return problem("bad_discovery", "The sign-in provider doesn't offer the kind of sign-in MetaRush uses.", "")
	}
	if len(d.CodeChallengeMethods) > 0 && !contains(d.CodeChallengeMethods, "S256") {
		return problem("bad_discovery", "The sign-in provider doesn't offer the protection MetaRush asks for (PKCE).", "")
	}
	if len(d.SigningAlgs) > 0 {
		ok := false
		for _, a := range d.SigningAlgs {
			ok = ok || Supported(a)
		}
		if !ok {
			return problem("bad_discovery", "The sign-in provider signs its answers in a way MetaRush doesn't check.", "")
		}
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func isGUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}

// IsDirectoryID reports a Microsoft directory (tenant) ID.
func IsDirectoryID(s string) bool { return isGUID(strings.TrimSpace(s)) }

// forgetDiscoveries drops the remembered documents (tests).
func forgetDiscoveries() {
	discoveries.Range(func(k, _ any) bool { discoveries.Delete(k); return true })
}

// Forget drops everything remembered about providers (tests that change a
// stand-in's keys or settings).
func Forget() { forgetDiscoveries(); forgetKeys() }

/* ---------- The sign-in ---------- */

// Request is one sign-in sent to the provider.
type Request struct {
	Redirect  string // this dashboard's callback address
	State     string
	Nonce     string
	Challenge string // the PKCE challenge (S256 of the verifier)
	// Hint is the email the person is expected to use (the owner's, in a test).
	Hint string
	// Domain is the Google Workspace domain to suggest ("" none).
	Domain string
}

// AuthURL is the provider's address the browser is sent to.
func (c *Client) AuthURL(ctx context.Context, r Request) (string, error) {
	d, err := c.Discover(ctx)
	if err != nil {
		return "", err
	}
	logbook.RememberSecret(r.State)
	logbook.RememberSecret(r.Nonce)
	logbook.RememberSecret(r.Challenge)
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", c.ClientID)
	q.Set("redirect_uri", r.Redirect)
	q.Set("scope", "openid email profile")
	q.Set("state", r.State)
	q.Set("nonce", r.Nonce)
	q.Set("code_challenge", r.Challenge)
	q.Set("code_challenge_method", "S256")
	switch {
	case r.Hint != "":
		// A test suggests the owner's own account (Microsoft takes either a
		// hint or the account chooser, never both).
		q.Set("login_hint", r.Hint)
	case c.Kind == Google || c.Kind == Microsoft:
		q.Set("prompt", "select_account") // people with several accounts choose
	}
	switch c.Kind {
	case Google:
		if r.Domain != "" {
			q.Set("hd", r.Domain) // only a suggestion: the answer's hd is what is checked
		}
	case Microsoft:
		q.Set("response_mode", "query") // a GET to the callback, which the sign-in's cookie reaches
	}
	sep := "?"
	if strings.Contains(d.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return d.AuthorizationEndpoint + sep + q.Encode(), nil
}

// tokenAnswer is the provider's answer to the code exchange.
type tokenAnswer struct {
	IDToken string `json:"id_token"`
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Type    string `json:"token_type"`
	Error   string `json:"error"`
	Desc    string `json:"error_description"`
}

// Exchange trades the answer's code for the ID token (with the PKCE verifier,
// and the client secret when the app has one) and checks the token. The
// access and refresh tokens the provider may also send are dropped unread.
func (c *Client) Exchange(ctx context.Context, code, verifier, redirect, nonce string) (*Identity, error) {
	d, err := c.Discover(ctx)
	if err != nil {
		return nil, err
	}
	logbook.RememberSecret(code)
	logbook.RememberSecret(verifier)
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", redirect)
	form.Set("code_verifier", verifier)
	form.Set("client_id", c.ClientID)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, nil)
	if c.Secret != "" {
		logbook.RememberSecret(c.Secret)
		// In the form when the provider takes it there (Google and Microsoft
		// do): a secret's own characters arrive as they are. Otherwise
		// client_secret_basic, the default (RFC 6749 2.3.1: both parts
		// form-encoded first).
		if contains(d.TokenAuthMethods, "client_secret_post") {
			form.Set("client_secret", c.Secret)
		} else {
			req.SetBasicAuth(url.QueryEscape(c.ClientID), url.QueryEscape(c.Secret))
		}
	}
	req.Body = io.NopCloser(strings.NewReader(form.Encode()))
	req.ContentLength = int64(len(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	start := time.Now()
	call := logbook.ProviderCall{Provider: "sso-" + c.Kind, Title: Title(c.Kind, "") + " sign-in", What: "finish a sign-in", Method: http.MethodPost, Endpoint: pathOf(d.TokenEndpoint)}
	res, err := c.http().Do(req)
	if err != nil {
		logbook.ProviderDone(ctx, call, errors.New("no answer"))
		return nil, &Problem{Reason: "no_answer", Msg: "MetaRush couldn't reach the sign-in provider to finish signing in.", Hint: "Check this computer's internet connection, then sign in again.", Detail: netWords(err)}
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	call.Status, call.Duration, call.Bytes = res.StatusCode, time.Since(start), len(b)
	var t tokenAnswer
	_ = json.Unmarshal(b, &t)
	for _, s := range []string{t.IDToken, t.Access, t.Refresh} {
		if s != "" {
			logbook.RememberSecret(s) // never in the activity log, whatever records it
		}
	}
	if res.StatusCode != http.StatusOK || t.IDToken == "" {
		logbook.ProviderDone(ctx, call, errors.New(firstNonEmpty(t.Error, res.Status)))
		switch t.Error {
		case "invalid_client", "unauthorized_client":
			return nil, &Problem{Reason: "bad_client", Msg: "The sign-in provider doesn't accept this computer's client ID or secret.", Hint: "An owner checks the client ID and sets the client secret again on this computer.", Detail: t.Error}
		case "invalid_grant":
			return nil, &Problem{Reason: "bad_code", Msg: "The sign-in provider didn't accept the answer: it was used already, it ran out, or it was for another sign-in.", Hint: "Sign in again.", Detail: t.Error}
		}
		return nil, &Problem{Reason: "refused", Msg: "The sign-in provider didn't finish the sign-in.", Hint: "Sign in again; if it happens again, an owner checks the setup in Settings → People.", Detail: firstNonEmpty(t.Error, res.Status)}
	}
	logbook.ProviderDone(ctx, call, nil)
	return c.Verify(ctx, d, t.IDToken, nonce)
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// String says which provider a client is for (no secret).
func (c *Client) String() string { return fmt.Sprintf("%s %s %s", c.Kind, c.Issuer, c.ClientID) }
