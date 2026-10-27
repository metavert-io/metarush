package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sessions"
	"github.com/metavert-io/metarush/internal/store"
)

// Remote access (D128, D129) through the whole dashboard stack: requests that
// tailscale serve would make (from loopback, the public Host, forwarded and
// identity headers) and the ones it never makes.

const publicHost = "studio-mac.tail0000.ts.net"

func newRemoteDash(t *testing.T, logins ...string) (*dash, string, string) {
	t.Helper()
	d, projectID, revisionID := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	if err := d.a.Cfg.SetRemote(config.Dashboard{PublicURL: "https://" + publicHost, TailscaleLogins: logins, MachineName: "Sam's MacBook"}); err != nil {
		t.Fatal(err)
	}
	if len(logins) > 0 {
		// Tailscale identity counts only when it is the sign-in method (D183).
		if err := auth.SetMethod(context.Background(), d.a, models.AuthTailscale); err != nil {
			t.Fatal(err)
		}
	}
	return d, projectID, revisionID
}

// browser is one browser on another computer, reaching the dashboard the way
// tailscale serve hands requests over. Cookies are kept by hand: the test
// server is plain http, and the session cookie is Secure.
type browser struct {
	t       *testing.T
	d       *dash
	host    string
	hdr     map[string]string
	cookies map[string]string
}

// tailnet is a browser on the tailnet signed in to Tailscale as login (""
// for a tagged computer, which gets no identity headers).
func (d *dash) tailnet(login, name string) *browser {
	h := map[string]string{"X-Forwarded-Proto": "https", "X-Forwarded-For": "100.101.102.104", "X-Forwarded-Host": publicHost}
	if login != "" {
		h["Tailscale-User-Login"], h["Tailscale-User-Name"], h["Tailscale-Headers-Info"] = login, name, "https://tailscale.com/s/serve-headers"
	}
	return &browser{t: d.t, d: d, host: publicHost, hdr: h, cookies: map[string]string{}}
}

func (b *browser) do(method, path string, body any, csrf bool, extra map[string]string) (*http.Response, envelope, []byte) {
	b.t.Helper()
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, b.d.base+path, rd)
	req.Host = b.host
	for k, v := range b.hdr {
		req.Header.Set(k, v)
	}
	for k, v := range extra {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	if csrf {
		req.Header.Set(clientHeader, "dashboard")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for n, v := range b.cookies {
		req.AddCookie(&http.Cookie{Name: n, Value: v})
	}
	res, err := http.DefaultTransport.RoundTrip(req) // redirects are not followed
	if err != nil {
		b.t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	for _, c := range res.Cookies() {
		if c.Value == "" || c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c.Value
		}
	}
	var env envelope
	_ = json.Unmarshal(raw, &env)
	return res, env, raw
}

func (b *browser) op(name string, in, out any) envelope {
	b.t.Helper()
	res, env, raw := b.do("POST", "/api/ops/"+name, in, true, nil)
	if res.StatusCode != 200 {
		b.t.Fatalf("%s: %d %s", name, res.StatusCode, raw)
	}
	if out != nil {
		_ = json.Unmarshal(env.Data, out)
	}
	return env
}

func TestRemoteAllowedLoginSignsInByItself(t *testing.T) {
	d, projectID, revisionID := newRemoteDash(t, "maya@example.com")
	maya := d.tailnet("maya@example.com", "=?utf-8?q?Maya_Lindqvist?=")
	res, env, raw := maya.do("GET", "/api/session", nil, false, map[string]string{"Sec-Fetch-Site": "same-origin"})
	if res.StatusCode != 200 {
		t.Fatalf("an allowed account signs in with no link: %d %s", res.StatusCode, raw)
	}
	var sess Session
	_ = json.Unmarshal(env.Data, &sess)
	if sess.User.Name != "Maya Lindqvist" || sess.User.Email != "maya@example.com" || sess.User.ID == d.a.Cfg.User.ID ||
		!sess.Access.Remote || sess.Access.SignedInWith != "tailscale" || sess.Access.Login != "maya@example.com" || sess.Access.Machine != "Sam's MacBook" || !sess.Access.Secure {
		t.Fatalf("%+v", sess)
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		cookie = c
	}
	if cookie == nil || cookie.Name != "__Host-mr_session" || !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" {
		t.Fatalf("the session cookie over HTTPS: %+v", cookie)
	}
	// The person exists once; the next request reuses the session.
	if res, _, _ = maya.do("GET", "/api/session", nil, false, nil); res.StatusCode != 200 || len(res.Cookies()) != 0 {
		t.Fatalf("second request: %d %v", res.StatusCode, res.Cookies())
	}
	var users []models.User
	_ = d.a.DB.Find(context.Background(), store.CUsers, d.a.WorkspaceID(), bson.M{"logins": "maya@example.com"}, &users)
	if len(users) != 1 || users[0].ID != sess.User.ID || users[0].Source != "tailscale" {
		t.Fatalf("%+v", users)
	}

	// What she does is hers: the audit trail, "Approved by" (approval stays
	// people-only, and counts for her as for anyone signed in).
	var p ops.ProjectOut
	maya.op("project.create", ops.ProjectCreateIn{Name: "From the tailnet"}, &p)
	maya.op("revision.approve", ops.RevisionApproveIn{ID: revisionID}, nil)
	var rev models.Revision
	_ = d.a.DB.GetByID(context.Background(), store.CRevisions, d.a.WorkspaceID(), revisionID, &rev)
	if rev.Approval.State != models.ApprovalApproved || rev.Approval.By == nil || rev.Approval.By.UserID != sess.User.ID || rev.Approval.By.UserName != "Maya Lindqvist" {
		t.Fatalf("approved by: %+v", rev.Approval)
	}
	var ev []models.AuditEvent
	_ = d.a.DB.Find(context.Background(), store.CAudit, d.a.WorkspaceID(), bson.M{"targetId": p.Project.ID}, &ev)
	if len(ev) == 0 || ev[0].Actor.UserID != sess.User.ID || ev[0].Actor.Via != "dashboard" {
		t.Fatalf("audit: %+v", ev)
	}
	ev = nil
	_ = d.a.DB.Find(context.Background(), store.CAudit, d.a.WorkspaceID(), bson.M{"action": "remote.signin"}, &ev)
	if len(ev) != 1 || ev[0].Actor.UserID != sess.User.ID || ev[0].Details["from"] != "100.101.102.104" {
		t.Fatalf("sign-in audit: %+v", ev)
	}
	// The person at this computer approving the same way is recorded as them.
	d.signIn()
	d.op("revision.approve", ops.RevisionApproveIn{ID: revisionID, Note: "local"}, nil)
	_ = d.a.DB.GetByID(context.Background(), store.CRevisions, d.a.WorkspaceID(), revisionID, &rev)
	if rev.Approval.By == nil || rev.Approval.By.UserID != d.a.Cfg.User.ID {
		t.Fatalf("local approval: %+v", rev.Approval)
	}
	_ = projectID
}

func TestRemoteRefusals(t *testing.T) {
	d, _, _ := newRemoteDash(t, "maya@example.com")
	// An account that isn't on the list: a plain page that says how to get in…
	bob := d.tailnet("bob@example.com", "Bob")
	res, _, raw := bob.do("GET", "/", nil, false, map[string]string{"Accept": "text/html", "Sec-Fetch-Mode": "navigate"})
	page := string(raw)
	if res.StatusCode != 403 || !strings.Contains(page, "bob@example.com") || !strings.Contains(page, "Remote access") || !strings.Contains(page, "metarush remote allow bob@example.com") ||
		!strings.Contains(res.Header.Get("Content-Type"), "text/html") || !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("not on the list: %d %s", res.StatusCode, page)
	}
	// …and the usual error answer for the dashboard's own requests.
	res, env, _ := bob.do("GET", "/api/session", nil, false, nil)
	if res.StatusCode != 403 || env.Error.Details["reason"] != refusedNotListed || len(bob.cookies) != 0 {
		t.Fatalf("api: %d %+v", res.StatusCode, env.Error)
	}
	n, _ := d.a.DB.C(store.CUsers).CountDocuments(context.Background(), bson.M{"logins": "bob@example.com"})
	if n != 0 {
		t.Fatal("no person is made for a refused account")
	}

	// Tailscale headers that didn't come through serve on the public address are pretending:
	// on this computer's own address…
	spoof := d.tailnet("maya@example.com", "Maya")
	spoof.host = "127.0.0.1:" + strconv.Itoa(d.a.Cfg.Dashboard.Port)
	if res, env, _ = spoof.do("GET", "/api/session", nil, false, nil); res.StatusCode != 403 || env.Error.Details["reason"] != refusedSpoofed {
		t.Fatalf("spoofed on the local address: %d %+v", res.StatusCode, env.Error)
	}
	// …even an app-capabilities header alone.
	if res, _, _ = spoof.do("GET", "/api/health", nil, false, map[string]string{"Tailscale-User-Login": "", "Tailscale-User-Name": "", "Tailscale-Headers-Info": "", "Tailscale-App-Capabilities": "{}"}); res.StatusCode != 403 {
		t.Fatalf("any Tailscale header: %d", res.StatusCode)
	}
	// …and from anywhere but this computer, even with the public Host.
	h := d.srv.Handler()
	for _, hdrs := range []map[string]string{{"Tailscale-User-Login": "maya@example.com"}, {}} {
		req := httptest.NewRequest("GET", "/api/session", nil) // RemoteAddr 192.0.2.1:1234
		req.Host = publicHost
		for k, v := range hdrs {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), refusedSpoofed) {
			t.Fatalf("not from loopback %v: %d %s", hdrs, rec.Code, rec.Body)
		}
	}
	// Tailscale Funnel (the public internet) is turned away.
	funnel := d.tailnet("", "")
	if res, env, _ = funnel.do("GET", "/api/health", nil, false, map[string]string{"Tailscale-Funnel-Request": "?1"}); res.StatusCode != 403 || env.Error.Details["reason"] != refusedFunnel {
		t.Fatalf("funnel: %d %+v", res.StatusCode, env.Error)
	}
	// DNS rebinding: any other name is still refused.
	other := d.tailnet("maya@example.com", "Maya")
	other.host = "evil.example.com"
	if res, _, _ = other.do("GET", "/api/health", nil, false, nil); res.StatusCode != 403 {
		t.Fatalf("other names: %d", res.StatusCode)
	}
	// This computer's short tailnet name through serve: refused, with the right address.
	short := d.tailnet("maya@example.com", "Maya")
	short.host = "studio-mac"
	if res, env, _ = short.do("GET", "/api/session", nil, false, nil); res.StatusCode != 403 || env.Error.Details["reason"] != refusedOtherName || env.Error.Details["url"] != "https://"+publicHost {
		t.Fatalf("short name: %d %+v", res.StatusCode, env.Error)
	}
	// Identity headers count only from a Tailscale address.
	outside := d.tailnet("maya@example.com", "Maya")
	if res, env, _ = outside.do("GET", "/api/session", nil, false, map[string]string{"X-Forwarded-For": "203.0.113.9"}); res.StatusCode != 401 || env.Error.Details["reason"] != "no_session" {
		t.Fatalf("not a tailnet address: %d %+v", res.StatusCode, env.Error)
	}
	// Cross-site requests never sign anyone in (SameSite for the ambient identity).
	cross := d.tailnet("maya@example.com", "Maya")
	if res, _, _ = cross.do("GET", "/api/session", nil, false, map[string]string{"Sec-Fetch-Site": "cross-site"}); res.StatusCode != 401 || len(cross.cookies) != 0 {
		t.Fatalf("cross-site: %d %v", res.StatusCode, cross.cookies)
	}
	// CSRF holds on the public address.
	maya := d.tailnet("maya@example.com", "Maya")
	if res, env, _ = maya.do("POST", "/api/ops/project.list", map[string]any{}, false, nil); res.StatusCode != 401 || env.Error.Details["reason"] != "csrf" {
		t.Fatalf("csrf: %d %+v", res.StatusCode, env.Error)
	}
	// What follows is about where, not who: even an owner can't from elsewhere
	// (a member is refused before that, D191, TestOwnerOnlyMachineSettings).
	d.makeOwner("maya@example.com")
	// The folder picker would open on this computer's screen: not from elsewhere.
	if res, env, _ = maya.do("POST", "/api/watch/choose", map[string]any{}, true, nil); res.StatusCode != http.StatusFailedDependency || env.Error.Details["reason"] != "elsewhere" {
		t.Fatalf("picker: %d %+v", res.StatusCode, env.Error)
	}
	if res, env, _ = maya.do("POST", "/api/resources/folders/choose", map[string]any{}, true, nil); env.Error.Details["reason"] != "elsewhere" {
		t.Fatalf("folder picker: %d %+v", res.StatusCode, env.Error)
	}
	// Moving the media (D112) runs on this computer with its keys: started only here.
	for _, path := range []string{"/api/storage/choose", "/api/storage/plan", "/api/storage/move"} {
		if res, env, _ = maya.do("POST", path, map[string]any{}, true, nil); res.StatusCode != http.StatusFailedDependency || env.Error.Details["reason"] != "elsewhere" {
			t.Fatalf("%s from elsewhere: %d %+v", path, res.StatusCode, env.Error)
		}
	}
	// Letting more people in only from this computer; removing works from anywhere.
	if res, env, _ = maya.do("POST", "/api/ops/remote.allow", map[string]any{"add": []string{"bob@example.com"}}, true, nil); res.StatusCode != 401 || env.Error.Details["reason"] != "elsewhere" {
		t.Fatalf("adding from elsewhere: %d %+v", res.StatusCode, env.Error)
	}
}

func TestRemoteRevokingAndCookieScope(t *testing.T) {
	d, _, _ := newRemoteDash(t, "maya@example.com")
	maya := d.tailnet("maya@example.com", "Maya")
	if res, _, raw := maya.do("GET", "/api/session", nil, false, nil); res.StatusCode != 200 {
		t.Fatalf("%d %s", res.StatusCode, raw)
	}
	tok := maya.cookies["__Host-mr_session"]
	// The cookie alone is nothing: another person on the tailnet (or a tagged
	// computer) with a copy of it isn't signed in…
	thief := d.tailnet("sam@example.com", "Sam")
	thief.cookies = map[string]string{"__Host-mr_session": tok}
	if err := d.a.Cfg.SetRemote(config.Dashboard{PublicURL: "https://" + publicHost, TailscaleLogins: []string{"maya@example.com", "sam@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if res, env, _ := thief.do("GET", "/api/session", nil, false, nil); res.StatusCode != 200 || !strings.Contains(string(env.Data), "sam@example.com") {
		t.Fatalf("the other person is signed in as themselves, never as Maya: %d %s", res.StatusCode, env.Data)
	}
	tagged := d.tailnet("", "")
	tagged.cookies = map[string]string{"__Host-mr_session": tok}
	if res, _, _ := tagged.do("GET", "/api/session", nil, false, nil); res.StatusCode != 401 {
		t.Fatalf("a copied identity cookie without the identity: %d", res.StatusCode)
	}
	// …nor on this computer's own address.
	local := &browser{t: t, d: d, host: "127.0.0.1:" + strconv.Itoa(d.a.Cfg.Dashboard.Port), hdr: map[string]string{}, cookies: map[string]string{"mr_session": tok}}
	if res, _, _ := local.do("GET", "/api/session", nil, false, nil); res.StatusCode != 401 {
		t.Fatalf("a public session on the local address: %d", res.StatusCode)
	}

	// Taking her off the list: turned away at once, everywhere.
	if err := d.a.Cfg.SetRemote(config.Dashboard{PublicURL: "https://" + publicHost, TailscaleLogins: []string{"sam@example.com"}}); err != nil {
		t.Fatal(err)
	}
	if res, _, _ := maya.do("GET", "/api/session", nil, false, nil); res.StatusCode != 403 {
		t.Fatalf("removed: %d", res.StatusCode)
	}
	// Turning Tailscale sign-in off ends the sessions it made.
	if err := d.a.Cfg.SetRemote(config.Dashboard{PublicURL: "https://" + publicHost}); err != nil {
		t.Fatal(err)
	}
	if res, _, _ := thief.do("GET", "/api/session", nil, false, nil); res.StatusCode != 401 {
		t.Fatalf("identity sign-in off: %d", res.StatusCode)
	}
	if _, ok := sessions.For(d.a.Cfg.StateDir()).Get(thief.cookies["__Host-mr_session"], time.Now()); ok {
		t.Fatal("the ended session is gone from the file")
	}
	// Remote access off: the public name is refused like any other.
	if err := d.a.Cfg.SetRemote(config.Dashboard{}); err != nil {
		t.Fatal(err)
	}
	if res, env, _ := maya.do("GET", "/api/session", nil, false, nil); res.StatusCode != 403 || env.Error.Details["reason"] != refusedOff {
		t.Fatalf("remote access off: %d %+v", res.StatusCode, env.Error)
	}
}

// A one-time link works through the public address too (a computer without
// a listed account, or a tagged one): it signs in as the person who runs
// MetaRush here, Secure over HTTPS, good only on that address.
func TestRemoteSignInLink(t *testing.T) {
	d, _, _ := newRemoteDash(t) // no Tailscale accounts listed
	tab := d.tailnet("maya@example.com", "Maya")
	if res, env, _ := tab.do("GET", "/api/session", nil, false, nil); res.StatusCode != 401 || env.Error.Details["remote"] != true {
		t.Fatalf("no sign-in yet: %d %+v", res.StatusCode, env.Error)
	}
	// One-time links sign in the owner on this computer only (D183): never
	// another computer, over HTTPS or not.
	for _, proto := range []string{"https", ""} {
		nonce, err := MintLaunchNonce(d.a.Cfg.StateDir())
		if err != nil {
			t.Fatal(err)
		}
		res, _, _ := tab.do("GET", "/auth?t="+nonce+"&next=/settings", nil, false, map[string]string{"X-Forwarded-Proto": proto})
		if res.StatusCode != http.StatusForbidden || len(res.Cookies()) != 0 {
			t.Fatalf("a link on another computer (%q): %d %v", proto, res.StatusCode, res.Cookies())
		}
	}
	// On this computer it works; X-Forwarded-Proto counts only on the public address.
	local := &browser{t: t, d: d, host: "127.0.0.1:" + strconv.Itoa(d.a.Cfg.Dashboard.Port), hdr: map[string]string{"X-Forwarded-Proto": "https"}, cookies: map[string]string{}}
	nonce, _ := MintLaunchNonce(d.a.Cfg.StateDir())
	res, _, _ := local.do("GET", "/auth?t="+nonce, nil, false, nil)
	if c := res.Cookies()[0]; c.Name != "mr_session" || c.Secure || res.StatusCode != http.StatusFound {
		t.Fatalf("local: %d %+v", res.StatusCode, c)
	}
	var sess Session
	_, env, _ := local.do("GET", "/api/session", nil, false, nil)
	_ = json.Unmarshal(env.Data, &sess)
	if sess.User.ID != d.a.Cfg.User.ID || sess.Access.SignedInWith != "link" || sess.Access.Remote {
		t.Fatalf("the owner on this computer: %+v", sess)
	}
	// Another computer signs in with an account: over HTTPS the cookie is __Host- and Secure.
	local.op("auth.method.set", map[string]any{"method": "password", "email": "sam@example.com", "password": "tomato garden at dusk"}, nil)
	res, _, raw := tab.do("POST", "/api/auth/login", map[string]any{"email": "sam@example.com", "password": "tomato garden at dusk"}, true, nil)
	if res.StatusCode != 200 {
		t.Fatalf("sign in elsewhere: %s", raw)
	}
	if c := res.Cookies()[0]; c.Name != "__Host-mr_session" || !c.Secure || c.SameSite != http.SameSiteStrictMode || !c.HttpOnly {
		t.Fatalf("remote cookie: %+v", c)
	}
	// Sign out everyone elsewhere: the remote browser must sign in again, this computer's stays.
	if n, _ := sessions.For(d.a.Cfg.StateDir()).RemoveWhere(func(s sessions.Session) bool { return s.Public }); n != 1 {
		t.Fatalf("public sessions: %d", n)
	}
	if res, _, _ := tab.do("GET", "/api/session", nil, false, nil); res.StatusCode != 401 {
		t.Fatalf("signed out: %d", res.StatusCode)
	}
	if res, _, _ := local.do("GET", "/api/session", nil, false, nil); res.StatusCode != 200 {
		t.Fatalf("this computer's browser: %d", res.StatusCode)
	}
}

// A real socket from an address that isn't loopback (the link-local address
// macOS puts on lo0, reachable only from this computer): Tailscale headers and
// the public Host are refused.
func TestRemoteNonLoopbackSocket(t *testing.T) {
	ln, err := net.Listen("tcp", "[fe80::1%lo0]:0")
	if err != nil {
		t.Skip("no link-local address on the loopback interface here:", err)
	}
	d, _, _ := newRemoteDash(t, "maya@example.com")
	srv := &http.Server{Handler: d.srv.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	req, err := http.NewRequest("GET", "http://"+strings.Replace(ln.Addr().String(), "%", "%25", 1)+"/api/session", nil) // the zone, escaped in a URL
	if err != nil {
		t.Fatal(err)
	}
	req.Host = publicHost
	req.Header.Set("Tailscale-User-Login", "maya@example.com")
	req.Header.Set("X-Forwarded-For", "100.101.102.104")
	req.Header.Set("X-Forwarded-Proto", "https")
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 403 || !strings.Contains(string(b), refusedSpoofed) || len(res.Cookies()) != 0 {
		t.Fatalf("non-loopback with identity headers: %d %s", res.StatusCode, b)
	}
}

// The extension through the public address: a member pairs her own browser
// (D197) and its uploads are hers.
func TestRemoteExtensionAndDownloads(t *testing.T) {
	d, _, _ := newRemoteDash(t, "maya@example.com")
	maya := d.tailnet("maya@example.com", "Maya")
	maya.op("project.list", map[string]any{}, nil) // signs her in
	var pair struct {
		Code    string `json:"code"`
		Address string `json:"address"`
		Steps   []string
	}
	maya.op("extension.pair", map[string]any{}, &pair)
	if pair.Address != "https://"+publicHost || !strings.Contains(strings.Join(pair.Steps, " "), "Download the extension") {
		t.Fatalf("pairing from another computer: %+v", pair)
	}
	res, _, raw := maya.do("GET", "/api/extension/download", nil, false, nil)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "application/zip" || !bytes.Contains(raw, []byte("metarush-browser-extension/manifest.json")) {
		t.Fatalf("extension download: %d %s", res.StatusCode, res.Header)
	}
	ext := d.tailnet("maya@example.com", "Maya")
	ext.hdr["Origin"] = "chrome-extension://abcdefghijklmnopabcdefghijklmnop"
	res, _, raw = ext.do("POST", "/ext/pair", map[string]any{"code": pair.Code, "name": "Chrome"}, false, map[string]string{clientHeader: "extension"})
	if res.StatusCode != 200 {
		t.Fatalf("ext pair: %d %s", res.StatusCode, raw)
	}
	var out struct {
		Data ExtPairOut `json:"data"`
	}
	_ = json.Unmarshal(raw, &out)
	if out.Data.Machine != "Sam's MacBook" {
		t.Fatalf("%+v", out.Data)
	}
	ext.hdr["Authorization"] = "Bearer " + out.Data.Token
	res, _, raw = ext.do("POST", "/ext/uploads", map[string]any{"name": "clip.mp4", "size": 10, "method": "browser", "source": map[string]any{"url": "https://www.youtube.com/watch?v=dQw4w9WgXcQ"}}, false, map[string]string{clientHeader: "extension"})
	if res.StatusCode != 200 {
		t.Fatalf("ext upload: %d %s", res.StatusCode, raw)
	}
	var up struct {
		Data ExtUploadOut `json:"data"`
	}
	_ = json.Unmarshal(raw, &up)
	rec, _ := d.srv.uploads.st.Get(up.Data.ID)
	var users []models.User
	_ = d.a.DB.Find(context.Background(), store.CUsers, d.a.WorkspaceID(), bson.M{"logins": "maya@example.com"}, &users)
	if len(users) != 1 || rec.UserID != users[0].ID {
		t.Fatalf("the upload is hers: %+v %+v", rec, users)
	}
	// An export folder MetaRush didn't make is never sent.
	if res, _, _ := maya.do("GET", "/api/export/download?folder=/etc", nil, false, nil); res.StatusCode != 401 {
		t.Fatalf("export download outside the exports folder: %d", res.StatusCode)
	}
}

// A change made in a terminal (`metarush remote remove`, `disable`: another
// process writing config.yaml) applies on the very next request.
func TestRemoteSettingsFromTheFileApplyAtOnce(t *testing.T) {
	d, _, _ := newRemoteDash(t)
	if err := auth.SetMethod(context.Background(), d.a, models.AuthTailscale); err != nil {
		t.Fatal(err)
	}
	h := d.a.Cfg.Home()
	if err := config.Save(h, config.Default(h)); err != nil {
		t.Fatal(err)
	}
	set := func(key string, v any) {
		t.Helper()
		if err := config.SetField(h, []string{"dashboard", key}, v); err != nil {
			t.Fatal(err)
		}
	}
	set("publicURL", "https://"+publicHost)
	set("tailscaleLogins", []string{"maya@example.com"})
	maya := d.tailnet("maya@example.com", "Maya")
	if res, _, _ := maya.do("GET", "/api/session", nil, false, nil); res.StatusCode != 200 {
		t.Fatalf("signed in from the file's settings: %d", res.StatusCode)
	}
	time.Sleep(20 * time.Millisecond) // a newer modification time
	set("tailscaleLogins", []string{"sam@example.com"})
	if res, env, _ := maya.do("GET", "/api/session", nil, false, nil); res.StatusCode != 403 || env.Error.Details["reason"] != refusedNotListed {
		t.Fatalf("taken off the list in another process: %d %+v", res.StatusCode, env.Error)
	}
	time.Sleep(20 * time.Millisecond)
	set("publicURL", "")
	if res, _, _ := maya.do("GET", "/api/health", nil, false, nil); res.StatusCode != 403 {
		t.Fatalf("remote access turned off in another process: %d", res.StatusCode)
	}
	// A broken file keeps the settings in effect.
	time.Sleep(20 * time.Millisecond)
	set("publicURL", "https://localhost")
	if d.a.Cfg.Remote().On() {
		t.Fatal("a refused address must not be used")
	}
}

// A live stream that is already open ends soon after its sign-in stops
// counting (revoking a person reaches everything they have open).
func TestRemoteStreamEndsWhenRevoked(t *testing.T) {
	old := streamRecheck
	streamRecheck = 100 * time.Millisecond
	defer func() { streamRecheck = old }()
	d, _, _ := newRemoteDash(t, "maya@example.com")
	maya := d.tailnet("maya@example.com", "Maya")
	if res, _, _ := maya.do("GET", "/api/session", nil, false, nil); res.StatusCode != 200 {
		t.Fatalf("sign-in: %d", res.StatusCode)
	}
	req, _ := http.NewRequest("GET", d.base+"/api/events", nil)
	req.Host = publicHost
	for k, v := range maya.hdr {
		req.Header.Set(k, v)
	}
	for n, v := range maya.cookies {
		req.AddCookie(&http.Cookie{Name: n, Value: v})
	}
	res, err := http.DefaultTransport.RoundTrip(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("stream: %v %v", err, res)
	}
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, res.Body)
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("the stream ended while she was still on the list")
	case <-time.After(400 * time.Millisecond):
	}
	if err := d.a.Cfg.SetRemote(config.Dashboard{PublicURL: "https://" + publicHost, TailscaleLogins: []string{"sam@example.com"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream kept going after she was taken off the list")
	}
}
