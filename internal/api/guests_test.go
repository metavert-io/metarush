package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/guests"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeoidc"
)

// The guest listener (D491–D499) attacked from the outside: every member
// route absent, forged Tailscale and forwarded headers ignored, another Host
// refused, changes only from the guest page, the cookie's scope, sign-ins
// that can't be fixed in advance, links used once and ending, the
// organization sign-in's rules for guests (another email, an unverified one,
// Microsoft's guests and other directories), sign-ins ending mid-video,
// per-address limits, and every request recorded. Always on a free loopback
// port of the test's own; never a real provider.

var owner = ops.Caller{Human: true}

// guestRig is a dashboard with guests on and its guest listener answering on
// a free port of 127.0.0.1, a project with two versions and their review
// copies kept on this computer.
type guestRig struct {
	t    *testing.T
	d    *dash
	a    *app.App
	srv  *Server
	port int
	base string // http://127.0.0.1:<port>
	p    string // the project
	revs []string
	rnd  map[string]models.Render
	// other is a version of another project.
	other string
}

func newGuestRig(t *testing.T) *guestRig {
	t.Helper()
	keyring.MockInit()
	sso.Forget()
	t.Cleanup(sso.Forget)
	d, projectID, revisionID := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	d.signIn()
	if err := config.Save(d.a.Cfg.Home(), d.a.Cfg); err != nil { // turning guests on writes this computer's settings
		t.Fatal(err)
	}
	r := &guestRig{t: t, d: d, a: d.a, srv: d.srv, p: projectID, rnd: map[string]models.Render{}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	_, port, err := d.srv.ListenGuests(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	r.port, r.base = port, fmt.Sprintf("http://127.0.0.1:%d", port)
	// A second version, and review copies of both.
	var rev models.Revision
	if err := r.a.DB.GetByID(ctx, store.CRevisions, r.a.WorkspaceID(), revisionID, &rev); err != nil {
		t.Fatal(err)
	}
	second := r.saveVersion(projectID, revisionID, rev.Spec.Clips[0].AssetID)
	r.revs = []string{revisionID, second}
	for i, v := range r.revs {
		r.rnd[v] = r.render(projectID, v, i+1)
	}
	op := call[ops.ProjectOut](t, r.a, "project.create", ops.ProjectCreateIn{Name: "Another film"}, owner)
	if _, err := r.a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": rev.Spec.Clips[0].AssetID}, bson.M{"$addToSet": bson.M{"projectIds": op.Project.ID}}); err != nil {
		t.Fatal(err)
	}
	r.other = r.saveVersion(op.Project.ID, "", rev.Spec.Clips[0].AssetID)
	r.rnd[r.other] = r.render(op.Project.ID, r.other, 1)
	call[guests.GuestsStatusOut](t, r.a, "guests.settings.set", guests.GuestsSettingsSetIn{On: ptr(true)}, owner)
	return r
}

func ptr[T any](v T) *T { return &v }

func call[O any](t *testing.T, a *app.App, op string, in any, c ops.Caller) *O {
	t.Helper()
	raw, _ := json.Marshal(in)
	out, err := ops.Call(context.Background(), a, op, raw, c)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return out.(*O)
}

func (r *guestRig) saveVersion(projectID, base, assetID string) string {
	r.t.Helper()
	plan := fmt.Sprintf(`{"projectId":%q,"baseRevisionId":%q,"spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":0,"out":3}]}}`, projectID, base, assetID)
	if base == "" {
		plan = fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":0,"out":3}]}}`, projectID, assetID)
	}
	return call[ops.EditApplyOut](r.t, r.a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan)}, owner).Revision.ID
}

// render keeps a review copy of a version on this computer: 256 KB.
func (r *guestRig) render(projectID, rev string, n int) models.Render {
	return r.renderSized(projectID, rev, n, 256<<10)
}

func (r *guestRig) renderSized(projectID, rev string, n, size int) models.Render {
	r.t.Helper()
	b := make([]byte, size)
	_, _ = rand.Read(b)
	p := filepath.Join(r.t.TempDir(), "r.mp4")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		r.t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(r.a.WorkspaceID(), sha)
	if _, err := r.a.Media.PutFile(context.Background(), key, p, media.PutOptions{SHA256: sha, ContentType: "video/mp4"}, media.Resume{}, nil); err != nil {
		r.t.Fatal(err)
	}
	now := time.Now().UTC()
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: r.a.WorkspaceID(), ProjectID: projectID, RevisionID: rev, RevisionNumber: n, Preset: "landscape-1080p",
		Quality: "review", State: models.RenderAvailable, Output: &models.RenderOutput{StorageKey: key, SHA256: sha, Size: int64(len(b)), Duration: 3, Width: 1920, Height: 1080, FPS: 30},
		CreatedAt: now, CompletedAt: &now}
	if err := r.a.DB.Insert(context.Background(), store.CRenders, rd); err != nil {
		r.t.Fatal(err)
	}
	return rd
}

func (r *guestRig) share(email, role string, versions ...string) *guests.GuestsShareOut {
	r.t.Helper()
	return call[guests.GuestsShareOut](r.t, r.a, "guests.share", guests.GuestsShareIn{Email: email, Name: "Dana", ProjectID: r.p, Versions: versions, Role: role, Days: 14}, owner)
}

// link makes a one-time link and returns its token.
func (r *guestRig) link(email string) string {
	r.t.Helper()
	out := call[guests.GuestsLinkOut](r.t, r.a, "guests.link", guests.GuestsLinkIn{Guest: email}, owner)
	u, err := url.Parse(out.URL)
	if err != nil || u.Host != fmt.Sprintf("127.0.0.1:%d", r.a.Cfg.GuestListener().PortOrDefault()) {
		r.t.Fatalf("the link opens %s", out.URL)
	}
	return u.Fragment
}

// gb is a browser at the guest address (cookies kept by hand).
type gb struct {
	r       *guestRig
	host    string
	hdr     map[string]string
	cookies map[string]string
	// tls: the requests go to a TLS stand-in of the listener.
	client *http.Client
	base   string
}

func (r *guestRig) browser() *gb {
	return &gb{r: r, host: fmt.Sprintf("127.0.0.1:%d", r.port), hdr: map[string]string{}, cookies: map[string]string{}, client: &http.Client{Transport: http.DefaultTransport}, base: r.base}
}

type genv struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error *apperr.Error   `json:"error"`
}

// do sends a request; guestPage adds the guest page's own header.
func (b *gb) do(method, path string, body any, guestPage bool) (*http.Response, genv, []byte) {
	b.r.t.Helper()
	var rd io.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, b.base+path, rd)
	req.Host = b.host
	for k, v := range b.hdr {
		req.Header.Set(k, v)
	}
	if guestPage {
		req.Header.Set(guestHeader, "1")
		req.Header.Set("Sec-Fetch-Site", "same-origin")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for n, v := range b.cookies {
		req.AddCookie(&http.Cookie{Name: n, Value: v})
	}
	res, err := b.client.Transport.RoundTrip(req)
	if err != nil {
		b.r.t.Fatal(err)
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
	var env genv
	_ = json.Unmarshal(raw, &env)
	return res, env, raw
}

func (b *gb) get(path string) (*http.Response, genv, []byte) { return b.do("GET", path, nil, false) }

// signIn uses a one-time link in this browser.
func (b *gb) signIn(token string) *http.Response {
	b.r.t.Helper()
	res, _, raw := b.do("POST", "/guest/api/link", map[string]string{"token": token}, true)
	if res.StatusCode != 200 {
		b.r.t.Fatalf("link: %d %s", res.StatusCode, raw)
	}
	return res
}

func guestReason(env genv) string {
	if env.Error == nil {
		return ""
	}
	d, _ := env.Error.Details.(map[string]any)
	s, _ := d["reason"].(string)
	return s
}

func TestEveryMemberRouteIsAbsentOnTheGuestListener(t *testing.T) {
	r := newGuestRig(t)
	r.share("dana@client.example", models.GuestCommenter)
	b := r.browser()
	b.signIn(r.link("dana@client.example"))
	// The member's own cookie, in the same browser (cookies don't keep to a
	// port): the guest listener doesn't know it.
	for _, c := range r.d.client.Jar.Cookies(mustURL(r.d.base)) {
		b.cookies[c.Name] = c.Value
	}
	rd := r.rnd[r.revs[0]]
	paths := []string{
		"/api/ops/project.list", "/api/ops", "/api/session", "/api/status", "/api/health", "/api/events", "/api/uploads",
		"/api/auth/state", "/api/auth/login", "/api/auth/sso/start", "/auth/sso/begin?t=x", "/auth/sso/callback?state=x&code=y", "/auth?t=" + strings.Repeat("a", 48),
		"/media/renders/" + rd.ID, "/media/assets/x/original", "/media/avatars/x", "/ext/hello", "/ext/upload",
		"/settings/people", "/library", "/assets/index.js", "/index.html", "/projects/" + r.p, "/signin",
		"/guest/api/ops/guest.shared", "/guest/api/../api/session", "/guest/media/" + r.revs[0], "/guest/api",
	}
	for _, p := range paths {
		for _, m := range []string{"GET", "POST"} {
			res, _, raw := b.do(m, p, nil, true)
			if res.StatusCode != http.StatusNotFound && res.StatusCode != http.StatusMethodNotAllowed && res.StatusCode != http.StatusBadRequest {
				t.Errorf("%s %s answered %d: %s", m, p, res.StatusCode, firstBytes(raw))
			}
			if bytes.Contains(raw, []byte(`"user"`)) || bytes.Contains(raw, []byte("workspace")) || bytes.Contains(raw, []byte("<div id=\"root\"")) {
				t.Errorf("%s %s showed something of the dashboard: %s", m, p, firstBytes(raw))
			}
		}
	}
	// The guest page is its own: never the dashboard's.
	res, _, raw := b.get("/")
	if res.StatusCode != 200 || !bytes.Contains(raw, []byte("for guests")) || bytes.Contains(raw, []byte(`id="root"`)) {
		t.Fatalf("the guest page: %d %s", res.StatusCode, firstBytes(raw))
	}
	csp := res.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "script-src 'sha256-") || strings.Contains(csp, "unsafe-inline") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("the page's policy: %s", csp)
	}
}

func mustURL(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}

func firstBytes(b []byte) string {
	if len(b) > 200 {
		return string(b[:200])
	}
	return string(b)
}

func TestOnlyTheGuestAddressIsAnswered(t *testing.T) {
	r := newGuestRig(t)
	for _, host := range []string{fmt.Sprintf("localhost:%d", r.port), fmt.Sprintf("[::1]:%d", r.port), "127.0.0.1", fmt.Sprintf("127.0.0.1:%d", r.a.Cfg.Dashboard.Port),
		"guests.example.com", fmt.Sprintf("evil.example:%d", r.port), fmt.Sprintf("127.0.0.1.:%d", r.port)} {
		b := r.browser()
		b.host = host
		res, env, _ := b.get("/guest/api/state")
		if res.StatusCode != http.StatusForbidden || env.Error == nil || !strings.Contains(env.Error.Message, "only at their own address") {
			t.Errorf("Host %q answered %d %+v", host, res.StatusCode, env.Error)
		}
	}
	// The guest address itself, any case.
	b := r.browser()
	if res, _, _ := b.get("/guest/api/state"); res.StatusCode != 200 {
		t.Fatalf("the guest address: %d", res.StatusCode)
	}
}

func TestForgedTailscaleAndForwardedHeadersAreIgnored(t *testing.T) {
	r := newGuestRig(t)
	r.share("dana@client.example", models.GuestViewer)
	ctx := context.Background()
	// Someone claiming to be a tailnet's member, an owner's account, or another
	// address: nobody is signed in by it, and what's recorded is the connection.
	b := r.browser()
	b.hdr = map[string]string{"Tailscale-User-Login": "sam@example.com", "Tailscale-User-Name": "Sam", "Tailscale-Headers-Info": "x",
		"X-Forwarded-For": "203.0.113.9", "X-Real-IP": "203.0.113.9", "Forwarded": "for=203.0.113.9", "X-Forwarded-Proto": "https", "X-Forwarded-Host": "guests.example.com",
		"Fly-Client-IP": "203.0.113.9", "CF-Connecting-IP": "203.0.113.9"}
	res, env, _ := b.get("/guest/api/state")
	var st GuestState
	_ = json.Unmarshal(env.Data, &st)
	if res.StatusCode != 200 || st.SignedIn {
		t.Fatalf("a forged identity signed in: %d %+v", res.StatusCode, st)
	}
	if res, _, _ := b.get("/guest/api/shared"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("forged headers reached what was shared: %d", res.StatusCode)
	}
	// "https" forwarded isn't believed: the cookie is the plain one.
	b.signIn(r.link("dana@client.example"))
	if _, ok := b.cookies["__Host-"+guestCookieName]; ok || b.cookies[guestCookieName] == "" {
		t.Fatalf("cookies: %v", b.cookies)
	}
	var rows []models.GuestActivity
	if err := r.a.DB.Find(ctx, store.CGuestActivity, r.a.WorkspaceID(), bson.M{}, &rows); err != nil || len(rows) == 0 {
		t.Fatalf("nothing recorded: %v", err)
	}
	for _, row := range rows {
		if row.From != "127.0.0.1" {
			t.Fatalf("a forwarded address was believed: %+v", row)
		}
	}
	// And the per-address limit counts the connection, whatever it says.
	defer func(n int) { guestRequestsPerMinute = n }(guestRequestsPerMinute)
	guestRequestsPerMinute = 5
	r2 := r.srv.GuestHandler(r.port)
	limited := 0
	for i := 0; i < 8; i++ {
		req := httptest.NewRequest("GET", "/guest/api/state", nil)
		req.Host, req.RemoteAddr = fmt.Sprintf("127.0.0.1:%d", r.port), "127.0.0.1:50000"
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		w := httptest.NewRecorder()
		r2.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 3 {
		t.Fatalf("%d of 8 limited: a forwarded address escaped the limit", limited)
	}
}

func TestGuestChangesComeOnlyFromTheGuestPage(t *testing.T) {
	r := newGuestRig(t)
	r.share("dana@client.example", models.GuestCommenter)
	tok := r.link("dana@client.example")
	b := r.browser()
	body := map[string]string{"token": tok}
	// No header (a cross-site form), or a cross-site fetch: refused, and the
	// link isn't used up.
	if res, env, _ := b.do("POST", "/guest/api/link", body, false); res.StatusCode != http.StatusForbidden || guestReason(env) != guests.ReasonCSRF {
		t.Fatalf("no header: %d %+v", res.StatusCode, env.Error)
	}
	b.hdr = map[string]string{guestHeader: "1", "Sec-Fetch-Site": "cross-site"}
	if res, env, _ := b.do("POST", "/guest/api/link", body, false); res.StatusCode != http.StatusForbidden || guestReason(env) != guests.ReasonCSRF {
		t.Fatalf("cross-site: %d %+v", res.StatusCode, env.Error)
	}
	b.hdr = map[string]string{guestHeader: "1", "Sec-Fetch-Site": "same-site"}
	if res, _, _ := b.do("POST", "/guest/api/link", body, false); res.StatusCode != http.StatusForbidden {
		t.Fatalf("same-site (the dashboard's port): %d", res.StatusCode)
	}
	b.hdr = map[string]string{}
	b.signIn(tok) // still unused
	at := 1.0
	comment := map[string]any{"body": "Shorter intro", "at": at}
	if res, _, _ := b.do("POST", "/guest/api/versions/"+r.revs[0]+"/comments", comment, false); res.StatusCode != http.StatusForbidden {
		t.Fatalf("a comment without the page's header: %d", res.StatusCode)
	}
	if res, _, raw := b.do("POST", "/guest/api/versions/"+r.revs[0]+"/comments", comment, true); res.StatusCode != 200 {
		t.Fatalf("a comment from the page: %d %s", res.StatusCode, raw)
	}
	// Strictly typed: unknown fields, two values, oversized bodies, a query.
	for name, raw := range map[string]string{
		"unknown field": `{"body":"x","author":"Owner"}`, "two values": `{"body":"x"}{"body":"y"}`, "not JSON": `body=x`,
		"too large": `{"body":"` + strings.Repeat("a", 20<<10) + `"}`,
	} {
		req, _ := http.NewRequest("POST", r.base+"/guest/api/versions/"+r.revs[0]+"/comments", strings.NewReader(raw))
		req.Host = b.host
		req.Header.Set(guestHeader, "1")
		req.AddCookie(&http.Cookie{Name: guestCookieName, Value: b.cookies[guestCookieName]})
		res, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == 200 {
			t.Errorf("%s: kept", name)
		}
	}
	if res, _, _ := b.get("/guest/api/shared?as=owner@example.com"); res.StatusCode != http.StatusBadRequest {
		t.Fatalf("a query on the API: %d", res.StatusCode)
	}
}

func TestTheGuestCookieKeepsToTheGuestAddress(t *testing.T) {
	r := newGuestRig(t)
	r.share("dana@client.example", models.GuestViewer)
	b := r.browser()
	res := b.signIn(r.link("dana@client.example"))
	var c *http.Cookie
	for _, x := range res.Cookies() {
		if x.Name == guestCookieName {
			c = x
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.Domain != "" || c.Secure || c.MaxAge <= 0 || c.MaxAge > int(guests.SessionLife/time.Second) {
		t.Fatalf("the cookie: %+v", c)
	}
	if len(c.Value) != 43 {
		t.Fatalf("a cookie of %d characters", len(c.Value))
	}
	// The dashboard doesn't know it, at any of its addresses.
	req, _ := http.NewRequest("GET", r.d.base+"/api/session", nil)
	req.AddCookie(c)
	dres, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	dres.Body.Close()
	if dres.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the guest's cookie opened the dashboard: %d", dres.StatusCode)
	}
	// Served over HTTPS (as the later relay's address will be): __Host- and Secure.
	ts := httptest.NewUnstartedServer(r.srv.GuestHandler(r.port))
	ts.StartTLS()
	t.Cleanup(ts.Close)
	sb := r.browser()
	sb.base = ts.URL
	sb.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // the test's own server
	sres := sb.signIn(r.link("dana@client.example"))
	var sc *http.Cookie
	for _, x := range sres.Cookies() {
		if x.Name == "__Host-"+guestCookieName {
			sc = x
		}
	}
	if sc == nil || !sc.Secure || sc.Domain != "" || sc.Path != "/" || sc.SameSite != http.SameSiteStrictMode {
		t.Fatalf("over HTTPS: %+v", sres.Cookies())
	}
}

func TestASignInCantBeFixedInAdvance(t *testing.T) {
	r := newGuestRig(t)
	r.share("dana@client.example", models.GuestViewer)
	b := r.browser()
	// A cookie someone else chose, planted before the guest signs in.
	planted := strings.Repeat("A", 43)
	b.cookies[guestCookieName] = planted
	if res, _, _ := b.get("/guest/api/shared"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a planted cookie: %d", res.StatusCode)
	}
	b.cookies[guestCookieName] = planted
	b.signIn(r.link("dana@client.example"))
	got := b.cookies[guestCookieName]
	if got == "" || got == planted {
		t.Fatal("the sign-in kept the planted cookie")
	}
	attacker := r.browser()
	attacker.cookies[guestCookieName] = planted
	if res, _, _ := attacker.get("/guest/api/shared"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the planted cookie works: %d", res.StatusCode)
	}
	// Signing in again ends the sign-in the browser had.
	first := got
	b.signIn(r.link("dana@client.example"))
	old := r.browser()
	old.cookies[guestCookieName] = first
	if res, _, _ := old.get("/guest/api/shared"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the earlier sign-in still works: %d", res.StatusCode)
	}
	if res, _, _ := b.get("/guest/api/shared"); res.StatusCode != 200 {
		t.Fatalf("the new sign-in: %d", res.StatusCode)
	}
}

func TestOneTimeLinksOverTheListener(t *testing.T) {
	r := newGuestRig(t)
	r.share("dana@client.example", models.GuestViewer)
	tok := r.link("dana@client.example")
	b := r.browser()
	b.signIn(tok)
	// Used: a second browser gets nothing.
	other := r.browser()
	res, env, _ := other.do("POST", "/guest/api/link", map[string]string{"token": tok}, true)
	if res.StatusCode != http.StatusUnauthorized || guestReason(env) != guests.ReasonLinkGone || other.cookies[guestCookieName] != "" {
		t.Fatalf("a used link: %d %+v", res.StatusCode, env.Error)
	}
	// Ended: the same answer.
	tok2 := r.link("dana@client.example")
	ctx := context.Background()
	if _, err := r.a.DB.C(store.CGuestLinks).UpdateMany(ctx, bson.M{"workspaceId": r.a.WorkspaceID(), "usedAt": nil}, bson.M{"$set": bson.M{"expiresAt": time.Now().Add(-time.Second).UTC()}}); err != nil {
		t.Fatal(err)
	}
	if res, env, _ := other.do("POST", "/guest/api/link", map[string]string{"token": tok2}, true); res.StatusCode != http.StatusUnauthorized || guestReason(env) != guests.ReasonLinkGone {
		t.Fatalf("an ended link: %d %+v", res.StatusCode, env.Error)
	}
	// Opening the page (as a mail scanner would) uses nothing: the token is
	// in the fragment, which never reaches the server.
	tok3 := r.link("dana@client.example")
	page, _, raw := other.get("/welcome")
	if page.StatusCode != 200 || bytes.Contains(raw, []byte(tok3)) {
		t.Fatalf("the welcome page: %d", page.StatusCode)
	}
	other.signIn(tok3)
}

func TestSigningInLimitsEachAddress(t *testing.T) {
	r := newGuestRig(t)
	defer func(n int) { guestSignInsPerMinute = n }(guestSignInsPerMinute)
	guestSignInsPerMinute = 3
	h := r.srv.GuestHandler(r.port)
	codes := []int{}
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("POST", "/guest/api/link", strings.NewReader(`{"token":"`+strings.Repeat("B", 43)+`"}`))
		req.Host, req.RemoteAddr = fmt.Sprintf("127.0.0.1:%d", r.port), "127.0.0.1:40000"
		req.Header.Set(guestHeader, "1")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		codes = append(codes, w.Code)
	}
	if fmt.Sprint(codes) != "[401 401 401 429 429]" {
		t.Fatalf("codes: %v", codes)
	}
}

func TestTakingThingsBackSignsTheGuestOutMidVideo(t *testing.T) {
	r := newGuestRig(t)
	defer func(d time.Duration) { guestRecheck = d }(guestRecheck)
	guestRecheck = 50 * time.Millisecond
	out := r.share("dana@client.example", models.GuestViewer)
	b := r.browser()
	b.signIn(r.link("dana@client.example"))
	// What was shared, and the version's review copy through the listener (ranges).
	res, env, _ := b.get("/guest/api/versions/" + r.revs[1])
	var w guests.GuestWatchOut
	_ = json.Unmarshal(env.Data, &w)
	if res.StatusCode != 200 || w.URL != guests.MediaPath(r.revs[1], r.rnd[r.revs[1]].ID) {
		t.Fatalf("watch: %d %+v", res.StatusCode, env.Error)
	}
	req, _ := http.NewRequest("GET", r.base+w.URL, nil)
	req.Host = b.host
	req.Header.Set("Range", "bytes=100-199")
	req.AddCookie(&http.Cookie{Name: guestCookieName, Value: b.cookies[guestCookieName]})
	pres, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(pres.Body)
	pres.Body.Close()
	if pres.StatusCode != http.StatusPartialContent || len(part) != 100 || pres.Header.Get("Content-Type") != "video/mp4" {
		t.Fatalf("a range: %d, %d bytes, %s", pres.StatusCode, len(part), pres.Header.Get("Content-Type"))
	}
	// Another version's review copy at a shared version's address: refused.
	if res, _, _ := b.get(guests.MediaPath(r.revs[1], r.rnd[r.revs[0]].ID)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("another version's render: %d", res.StatusCode)
	}
	if res, _, _ := b.get(guests.MediaPath(r.other, r.rnd[r.other].ID)); res.StatusCode != http.StatusNotFound {
		t.Fatalf("another project's render: %d", res.StatusCode)
	}
	// A long answer stops soon after the grant is taken back: a bigger review
	// copy (the newest, so the one watched), read a little, then the owner
	// takes it back while the player waits.
	big := r.renderSized(r.p, r.revs[1], 2, 16<<20)
	res, env, _ = b.get("/guest/api/versions/" + r.revs[1])
	_ = json.Unmarshal(env.Data, &w)
	if w.URL != guests.MediaPath(r.revs[1], big.ID) {
		t.Fatalf("watching %s", w.URL)
	}
	sreq, _ := http.NewRequest("GET", r.base+w.URL, nil)
	sreq.Host = b.host
	sreq.AddCookie(&http.Cookie{Name: guestCookieName, Value: b.cookies[guestCookieName]})
	sres, err := http.DefaultTransport.RoundTrip(sreq)
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 4096)
	if _, err := io.ReadFull(sres.Body, first); err != nil {
		t.Fatal(err)
	}
	call[guests.GuestsRevokeOut](t, r.a, "guests.revoke", guests.GuestsRevokeIn{Grant: out.Grant.ID}, owner)
	time.Sleep(10 * guestRecheck)
	rest, _ := io.Copy(io.Discard, sres.Body)
	sres.Body.Close()
	if total := int64(len(first)) + rest; total >= big.Output.Size {
		t.Fatalf("the whole video went out after the grant was taken back (%d bytes)", total)
	}
	// And the next request is refused, in plain words.
	res, env, _ = b.get("/guest/api/shared")
	if res.StatusCode != http.StatusUnauthorized || env.Error == nil || !strings.Contains(env.Error.Message, "signed out") && !strings.Contains(env.Error.Message, "not signed in") {
		t.Fatalf("after taking it back: %d %+v", res.StatusCode, env.Error)
	}
}

func TestGuestsTurnedOffAreSignedOut(t *testing.T) {
	r := newGuestRig(t)
	r.share("dana@client.example", models.GuestViewer)
	b := r.browser()
	b.signIn(r.link("dana@client.example"))
	call[guests.GuestsStatusOut](t, r.a, "guests.settings.set", guests.GuestsSettingsSetIn{On: ptr(false)}, owner)
	res, env, _ := b.get("/guest/api/shared")
	if res.StatusCode != http.StatusUnauthorized || guestReason(env) != guests.ReasonGuestsOff {
		t.Fatalf("guests off: %d %+v", res.StatusCode, env.Error)
	}
}

func TestEveryGuestRequestIsRecorded(t *testing.T) {
	r := newGuestRig(t)
	ctx := context.Background()
	r.share("dana@client.example", models.GuestCommenter, r.revs[0])
	b := r.browser()
	b.get("/")
	b.signIn(r.link("dana@client.example"))
	b.get("/guest/api/shared")
	b.get("/guest/api/versions/" + r.revs[0])
	b.do("POST", "/guest/api/versions/"+r.revs[0]+"/comments", map[string]any{"body": "Brighter", "at": 1.0}, true)
	b.get("/guest/api/versions/" + r.revs[1]) // not shared with them
	b.do("POST", "/guest/api/signout", map[string]any{}, true)
	var rows []models.GuestActivity
	if err := r.a.DB.Find(ctx, store.CGuestActivity, r.a.WorkspaceID(), bson.M{}, &rows, options.Find().SetSort(bson.D{{Key: "at", Value: 1}})); err != nil {
		t.Fatal(err)
	}
	var whats []string
	for _, row := range rows {
		w := row.What
		if row.Refused {
			w += "!" + row.Reason
		}
		whats = append(whats, w)
		if row.From != "127.0.0.1" || row.At.IsZero() {
			t.Errorf("row: %+v", row)
		}
	}
	got := strings.Join(whats, " ")
	for _, want := range []string{"guest.page", "guest.signin", "guest.shared", "guest.watch", "guest.comment", "guest.watch!" + guests.ReasonNotShared, "guest.signout"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	// The activity list says them plainly.
	act := call[guests.GuestsActivityOut](t, r.a, "guests.activity", guests.GuestsActivityIn{}, owner)
	all := ""
	for _, row := range act.Rows {
		all += row.Who + " " + row.Says + "\n"
	}
	for _, want := range []string{"Dana (guest) commented on version 1 of “API film”, from 127.0.0.1", "Dana (guest) signed in with a sign-in link", "turned away (not shared with them)"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in:\n%s", want, all)
		}
	}
}

func TestTheGuestListenerListensOnThisComputerOnly(t *testing.T) {
	ln, err := guestListen(0)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	a := ln.Addr().(*net.TCPAddr)
	if !a.IP.IsLoopback() || a.IP.String() != "127.0.0.1" {
		t.Fatalf("bound %s", a)
	}
	for _, p := range []int{-1, 70000} {
		if _, err := guestListen(p); err == nil {
			t.Errorf("port %d", p)
		}
	}
	// Nothing else on this computer answers at its port by another address.
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("[::1]:%d", a.Port), time.Second); err == nil {
		c.Close()
		t.Fatal("the guest listener answers on [::1]")
	}
}

/* ---------- Signing in with an organization's account ---------- */

// guestSSO puts an organization sign-in in use (a stand-in provider at
// loopback, its app's secret in a stand-in keychain) and registers the
// guest listener's callback with it.
func (r *guestRig) guestSSO(provider, tenant string) *fakeoidc.Server {
	r.t.Helper()
	ctx := context.Background()
	secret := sso.Random(24)
	p := fakeoidc.New("guest-app", secret)
	r.t.Cleanup(p.Close)
	st := &models.SSOSettings{Provider: provider, Issuer: p.Issuer, ClientID: "guest-app", Label: "Test provider", Rev: "r1", SetAt: time.Now().UTC().Truncate(time.Millisecond)}
	if provider == sso.Microsoft {
		p.Issuer = p.URL + "/" + tenant + "/v2.0"
		st.Issuer, st.Tenant, st.Label = p.Issuer, tenant, ""
	}
	if _, err := providers.StoreKey(r.a.Cfg, "sso-"+auth.AppID(st), secret, false); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.a.DB.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": r.a.WorkspaceID()}, bson.M{"$set": bson.M{"auth.method": models.AuthSSO, "auth.sso": st},
		"$setOnInsert": bson.M{"workspaceId": r.a.WorkspaceID(), "name": "T", "createdAt": time.Now().UTC()}}, options.UpdateOne().SetUpsert(true)); err != nil {
		r.t.Fatal(err)
	}
	auth.ForgetSettings(r.a)
	p.Redirects = []string{fmt.Sprintf("http://127.0.0.1:%d%s", r.port, sso.GuestCallbackPath)}
	return p
}

// ssoIn signs in at the guest listener as who: start, begin, the provider's
// page, the callback. It returns the callback's answer.
func (b *gb) ssoIn(p *fakeoidc.Server, who fakeoidc.Person) (*http.Response, []byte) {
	b.r.t.Helper()
	res, env, raw := b.do("POST", "/guest/api/sso/start", map[string]any{}, true)
	if res.StatusCode != 200 {
		b.r.t.Fatalf("start: %d %s", res.StatusCode, raw)
	}
	var out GuestSSOStartOut
	_ = json.Unmarshal(env.Data, &out)
	bu, _ := url.Parse(out.URL)
	if bu.Host != b.host || bu.Path != sso.GuestBeginPath {
		b.r.t.Fatalf("begins at %s", out.URL)
	}
	res, _, _ = b.get(bu.RequestURI())
	if res.StatusCode != http.StatusFound {
		b.r.t.Fatalf("begin: %d", res.StatusCode)
	}
	p.Next = &who
	pres, err := http.DefaultTransport.RoundTrip(mustGet(res.Header.Get("Location")))
	if err != nil {
		b.r.t.Fatal(err)
	}
	pres.Body.Close()
	cb, _ := url.Parse(pres.Header.Get("Location"))
	if cb.Host != b.host || cb.Path != sso.GuestCallbackPath {
		b.r.t.Fatalf("the provider sent the browser to %s", cb)
	}
	res, _, raw = b.get(cb.RequestURI())
	return res, raw
}

func TestGuestsSignInWithTheOrganizationsAccount(t *testing.T) {
	r := newGuestRig(t)
	b := r.browser()
	// Not how people sign in here: refused plainly.
	if res, env, _ := b.do("POST", "/guest/api/sso/start", map[string]any{}, true); res.StatusCode != http.StatusConflict || guestReason(env) != guests.ReasonNoSSO {
		t.Fatalf("no organization sign-in: %d %+v", res.StatusCode, env.Error)
	}
	p := r.guestSSO(sso.OIDC, "")
	r.share("dana@client.example", models.GuestCommenter)
	verified := func(email string, ok bool) fakeoidc.Person {
		return fakeoidc.Person{Sub: "sub-" + email, Email: email, EmailVerified: fakeoidc.Verified(ok), Name: "Dana"}
	}
	// Another email than the one shared with.
	res, raw := b.ssoIn(p, verified("mallory@client.example", true))
	if res.StatusCode != http.StatusForbidden || !bytes.Contains(raw, []byte("Nothing is shared with mallory@client.example here")) || b.cookies[guestCookieName] != "" {
		t.Fatalf("another email: %d %s", res.StatusCode, raw)
	}
	// The email shared with, but not verified by the provider.
	res, raw = b.ssoIn(p, verified("dana@client.example", false))
	if res.StatusCode != http.StatusForbidden || !bytes.Contains(raw, []byte("vouch")) || b.cookies[guestCookieName] != "" {
		t.Fatalf("an unverified email: %d %s", res.StatusCode, raw)
	}
	// The verified email: signed in as the guest, never as a member.
	res, raw = b.ssoIn(p, verified("dana@client.example", true))
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/" || b.cookies[guestCookieName] == "" {
		t.Fatalf("signed in: %d %s", res.StatusCode, raw)
	}
	if res, _, _ := b.get("/guest/api/shared"); res.StatusCode != 200 {
		t.Fatalf("what was shared: %d", res.StatusCode)
	}
	var sess models.GuestSession
	if err := r.a.DB.C(store.CGuestSessions).FindOne(context.Background(), bson.M{"workspaceId": r.a.WorkspaceID(), "via": "sso"}).Decode(&sess); err != nil {
		t.Fatalf("no sso sign-in recorded: %v", err)
	}
	if n, _ := r.a.DB.C(store.CUsers).CountDocuments(context.Background(), bson.M{"workspaceId": r.a.WorkspaceID(), "emailKey": "dana@client.example"}); n != 0 {
		t.Fatal("the guest became a member")
	}
	// The members' callback never finishes a guest's sign-in, and the
	// guest's never a member's.
	res, env, _ := b.do("POST", "/guest/api/sso/start", map[string]any{}, true)
	var start GuestSSOStartOut
	_ = json.Unmarshal(env.Data, &start)
	bu, _ := url.Parse(start.URL)
	mb := r.d.here()
	mres, _ := mb.get(sso.BeginPath + "?" + bu.RawQuery)
	if mres.StatusCode == http.StatusFound {
		t.Fatal("the dashboard began a guest's sign-in")
	}
	_ = res
}

func TestMicrosoftGuestsAndOtherDirectoriesDontSignInAsGuests(t *testing.T) {
	r := newGuestRig(t)
	ours := "11111111-2222-3333-4444-555555555555"
	theirs := "99999999-8888-7777-6666-555555555555"
	p := r.guestSSO(sso.Microsoft, ours)
	r.share("dana@client.example", models.GuestViewer)
	ms := func(sub, tid string, extra map[string]any) fakeoidc.Person {
		claims := map[string]any{"tid": tid, "oid": "oid-" + sub, "xms_edov": true}
		for k, v := range extra {
			claims[k] = v
		}
		return fakeoidc.Person{Sub: sub, Email: "dana@client.example", Name: "Dana", Extra: claims}
	}
	b := r.browser()
	for name, who := range map[string]fakeoidc.Person{
		"a guest in our directory":     ms("g1", ours, map[string]any{"acct": 1}),
		"another directory's member":   ms("g2", theirs, nil),
		"a personal Microsoft account": ms("g3", sso.MicrosoftPersonal, nil),
		"another identity provider":    ms("g4", ours, map[string]any{"idp": "https://sts.windows.net/" + theirs + "/"}),
	} {
		// Turned away: by who it is (a guest, a personal account), or, for a
		// directory a single-directory setup isn't for, by the token's checks.
		res, raw := b.ssoIn(p, who)
		if (res.StatusCode != http.StatusForbidden && res.StatusCode != http.StatusUnauthorized) || b.cookies[guestCookieName] != "" {
			t.Errorf("%s: %d %s", name, res.StatusCode, firstBytes(raw))
		}
	}
	// Our directory's own member with that email: in.
	if res, raw := b.ssoIn(p, ms("g5", ours, nil)); res.StatusCode != http.StatusFound || b.cookies[guestCookieName] == "" {
		t.Fatalf("our directory's member: %d %s", res.StatusCode, raw)
	}
}
