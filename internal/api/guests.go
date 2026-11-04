package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/guests"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sso"
	"github.com/metavert-io/metarush/internal/store"
)

// The guest listener (D491–D499, docs/guests.md): a second web server, for
// guests only — people outside the workspace an owner shared versions with.
//
//   - It listens on 127.0.0.1 only (config.GuestHost), never 0.0.0.0, :: or a
//     network's address: this computer may sit on a shared network. It is
//     off unless a person turned guests on at this computer, at guests.port
//     (4846 by default); tests use a free port of their own.
//   - It has its own router: only the guest page, the guest sign-in steps and
//     the guest API exist there. The dashboard, /api/ops, /media, /ext, the
//     one-time /auth link, the members' sign-in and every setting don't — a
//     route missing from it doesn't exist there, rather than being refused by
//     a check that could be wrong.
//   - Its own Host check: the guest address's exact host and port only (DNS
//     rebinding, and anything sent to it under another name, is refused).
//   - Untrusted requests: Tailscale's identity headers and every forwarded
//     header are taken off before anything reads them, so none is believed;
//     the visitor's address is the connection's (visitor: the one place the
//     relay of a later release changes, to believe its header only from its
//     own port, D499); every input is typed and bounded (bodies of a few KB,
//     no query on the API, IDs by their shape).
//   - Its own cookie on the guest address only (mr_guest; __Host-mr_guest and
//     Secure when served over HTTPS), HttpOnly and SameSite=Strict, and the
//     guest page's own header (X-MetaRush-Guest) on every change, same-origin.
//   - Per-address limits like the members' sign-in's: every request, and the
//     sign-in steps more tightly.
//   - Every request is recorded (guests.Record).

const (
	guestCookieName = "mr_guest"
	guestSSOCookie  = "mr_guest_sso"
	guestHeader     = "X-MetaRush-Guest"
	// guestBodyLimit bounds what a guest's request may carry.
	guestBodyLimit = 16 << 10
	// guestURLLimit bounds an address (the provider's answer carries a code
	// and a state).
	guestURLLimit = 8 << 10
)

// guestRecheck is how often a video being sent checks that its guest is still
// signed in (a variable for tests).
var guestRecheck = 5 * time.Second

// Per-address limits on the guest listener (a minute's tries).
var (
	guestRequestsPerMinute = 300
	guestSignInsPerMinute  = 20
)

// guestGate is the guest listener of a dashboard server.
type guestGate struct {
	s *Server
	// port is the port its guest address names (127.0.0.1:port).
	port int
	// limit counts every request per address; signIn the sign-in steps.
	limit, signIn *rateLimit
	// believeForwarded says whether a request's forwarded address may be
	// believed: nowhere in this release (D499). The relay of a later release
	// sets it to "only from its own port".
	believeForwarded func(*http.Request) bool
	// bucket is the media store's address for signed links (the page's
	// policy lets video and stills come from there); "" for media kept here.
	bucketOnce sync.Once
	bucket     string
}

func newGuestGate(s *Server, port int) *guestGate {
	return &guestGate{s: s, port: port, limit: newRateLimit(guestRequestsPerMinute), signIn: newRateLimit(guestSignInsPerMinute)}
}

// host is the Host the guest address names.
func (g *guestGate) host() string { return config.GuestHost + ":" + strconv.Itoa(g.port) }

// origin is the guest address.
func (g *guestGate) origin() string { return "http://" + g.host() }

// GuestHandler is the guest listener's handler for a listener at
// 127.0.0.1:port (its own router, Host check and guards).
func (s *Server) GuestHandler(port int) http.Handler {
	return newGuestGate(s, port).handler()
}

func (g *guestGate) handler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/", g.page).Methods("GET", "HEAD")
	r.HandleFunc(guests.WelcomePath, g.page).Methods("GET", "HEAD")
	r.HandleFunc("/guest/api/health", g.health).Methods("GET")
	r.HandleFunc("/guest/api/state", g.state).Methods("GET")
	r.HandleFunc("/guest/api/link", g.useLink).Methods("POST")
	r.HandleFunc("/guest/api/signout", g.signOut).Methods("POST")
	r.HandleFunc("/guest/api/sso/start", g.ssoStart).Methods("POST")
	r.HandleFunc(sso.GuestBeginPath, g.ssoBegin).Methods("GET")
	r.HandleFunc(sso.GuestCallbackPath, g.ssoCallback).Methods("GET")
	r.HandleFunc("/guest/api/shared", g.shared).Methods("GET")
	r.HandleFunc("/guest/api/versions/{version}", g.watch).Methods("GET")
	r.HandleFunc("/guest/api/versions/{version}/comments", g.comments).Methods("GET")
	r.HandleFunc("/guest/api/versions/{version}/comments", g.comment).Methods("POST")
	r.HandleFunc("/guest/media/{version}/{render}", g.media).Methods("GET", "HEAD")
	r.HandleFunc("/guest/media/{version}/{render}/still", g.still).Methods("GET", "HEAD")
	r.NotFoundHandler = http.HandlerFunc(g.notFound)
	r.MethodNotAllowedHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		guestError(w, http.StatusMethodNotAllowed, apperr.New(apperr.InvalidInput, "The guest page doesn't ask for that."))
	})
	return g.guard(r)
}

// untrustedHeaders are taken off every request before anything reads them:
// identity a tailnet's serve would add, and addresses something in front
// would forward. None is believed here (D491, D499).
func untrusted(name string) bool {
	n := strings.ToLower(name)
	switch {
	case strings.HasPrefix(n, "tailscale-"), strings.HasPrefix(n, "x-forwarded-"):
		return true
	}
	switch n {
	case "forwarded", "x-real-ip", "x-client-ip", "true-client-ip", "cf-connecting-ip", "fly-client-ip", "x-original-forwarded-for":
		return true
	}
	return false
}

// visitor is the address a request came from: the connection's. A forwarded
// address is believed only where believeForwarded says so — nowhere yet.
func (g *guestGate) visitor(r *http.Request, forwarded string) string {
	if g.believeForwarded != nil && forwarded != "" && g.believeForwarded(r) {
		if a, err := netip.ParseAddr(strings.TrimSpace(strings.Split(forwarded, ",")[0])); err == nil {
			return a.Unmap().String()
		}
	}
	if ap, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
		return ap.Addr().Unmap().String()
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// secure reports a request that came over HTTPS (never, in this release: the
// guest address is this computer's own, http).
func (g *guestGate) secure(r *http.Request) bool { return r.TLS != nil }

type guestCtxKey struct{}

// guestReq is what the guard learned about a request.
type guestReq struct {
	from   string
	secure bool
	// signed is the guest's checked sign-in, once a handler looked.
	signed *guests.Signed
	// what, projectID and revisionID say what it did, for its record.
	what, projectID, revisionID, reason string
	media, session                      string
	quiet                               bool // nothing to record (health)
}

func reqOf(r *http.Request) *guestReq {
	q, _ := r.Context().Value(guestCtxKey{}).(*guestReq)
	if q == nil {
		return &guestReq{}
	}
	return q
}

// statusWriter keeps an answer's status for its record.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// guard runs before every route: headers that aren't believed go, the Host
// is the guest address's, the address limit, the size bounds, the page's
// own header on changes; then the request is recorded.
func (g *guestGate) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded := r.Header.Get("X-Forwarded-For")
		for k := range r.Header {
			if untrusted(k) {
				r.Header.Del(k)
			}
		}
		q := &guestReq{from: g.visitor(r, forwarded), secure: g.secure(r)}
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		sw := &statusWriter{ResponseWriter: w}
		refuse := func(status int, reason string, e *apperr.Error) {
			q.what, q.reason = "guest.refused", reason
			guestError(sw, status, e)
			g.record(r, q, sw.status)
		}
		if !strings.EqualFold(r.Host, g.host()) {
			// Only the guest address: another name for this computer (or a page
			// that pointed its own name here) gets nothing.
			refuse(http.StatusForbidden, guests.ReasonOther, apperr.New(apperr.Unauthorized, "MetaRush answers guests only at their own address."))
			return
		}
		if len(r.URL.RequestURI()) > guestURLLimit {
			refuse(http.StatusRequestURITooLong, "too_long", apperr.New(apperr.InvalidInput, "That address is too long."))
			return
		}
		if !g.limit.allow(q.from, time.Now()) {
			h.Set("Retry-After", "60")
			refuse(http.StatusTooManyRequests, guests.ReasonThrottled, apperr.New(apperr.Unauthorized, "Too many requests from this browser. Wait a minute, then try again.").
				WithDetails(map[string]any{"reason": guests.ReasonThrottled}))
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			// Changes come from the guest page itself: its own header (no
			// cross-site form can send one) and a same-origin request.
			if r.Header.Get(guestHeader) != "1" || !sameOriginOrDirect(r) {
				refuse(http.StatusForbidden, guests.ReasonCSRF, apperr.New(apperr.Unauthorized, "MetaRush takes changes only from the guest page.").WithDetails(map[string]any{"reason": guests.ReasonCSRF}))
				return
			}
			r.Body = http.MaxBytesReader(sw, r.Body, guestBodyLimit)
		}
		if strings.HasPrefix(r.URL.Path, "/guest/api/") && r.URL.RawQuery != "" {
			refuse(http.StatusBadRequest, "query", apperr.New(apperr.InvalidInput, "The guest page doesn't ask for that."))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/guest/api/") {
			h.Set("Cache-Control", "no-store")
		}
		ctx := context.WithValue(r.Context(), guestCtxKey{}, q)
		next.ServeHTTP(sw, r.WithContext(ctx))
		if !q.quiet {
			g.record(r, q, sw.status)
		}
	})
}

// record writes the request (never failing it).
func (g *guestGate) record(r *http.Request, q *guestReq, status int) {
	what := q.what
	if what == "" {
		what = "guest.refused"
	}
	h := guests.Hit{What: what, ProjectID: q.projectID, RevisionID: q.revisionID, From: q.from, Status: status, Reason: q.reason, Media: q.media, Session: q.session}
	h.Refused = status >= 400
	if h.Refused && h.Reason == "" {
		h.Reason = reasonForStatus(status)
	}
	if q.signed != nil {
		h.Guest = &q.signed.Guest
	}
	guests.Record(r.Context(), g.s.App, h)
}

func reasonForStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return guests.ReasonSignedOut
	case http.StatusNotFound:
		return guests.ReasonNotShared
	case http.StatusTooManyRequests:
		return guests.ReasonThrottled
	}
	return "refused"
}

// guestError answers a guest with an error, as the dashboard's API does.
func guestError(w http.ResponseWriter, status int, err error) {
	ae := apperr.From(err)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]any{"ok": false, "error": ae})
}

// guestErr answers with an operation's error at its own status.
func guestErr(w http.ResponseWriter, err error) {
	guestError(w, apperr.From(err).HTTPStatus(), err)
}

func (g *guestGate) notFound(w http.ResponseWriter, r *http.Request) {
	q := reqOf(r)
	q.what, q.reason = "guest.refused", "no_such_page"
	guestError(w, http.StatusNotFound, apperr.New(apperr.NotFound, "There's nothing like that on the guest page."))
}

func (g *guestGate) health(w http.ResponseWriter, r *http.Request) {
	reqOf(r).quiet = true
	writeOK(w, map[string]bool{"guests": true})
}

/* ---------- Who is signed in ---------- */

func guestCookie(secure bool) string {
	if secure {
		return "__Host-" + guestCookieName
	}
	return guestCookieName
}

func (g *guestGate) setCookie(w http.ResponseWriter, secure bool, val string) {
	c := &http.Cookie{Name: guestCookie(secure), Value: val, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
		MaxAge: int(guests.SessionLife / time.Second)}
	if val == "" {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// token is the browser's guest cookie ("" none).
func (g *guestGate) token(r *http.Request) string {
	c, err := r.Cookie(guestCookie(reqOf(r).secure))
	if err != nil {
		return ""
	}
	return c.Value
}

// signed checks the request's sign-in; when it doesn't count it answers
// (and takes the cookie away) and reports false.
func (g *guestGate) signed(w http.ResponseWriter, r *http.Request) (*guests.Signed, bool) {
	q := reqOf(r)
	tok := g.token(r)
	if tok != "" {
		g.s.App.Log.Secret(tok) // a guest's cookie never reaches the activity log
	}
	s, err := guests.Check(r.Context(), g.s.App, tok, q.from)
	if err != nil {
		if tok != "" {
			g.setCookie(w, q.secure, "")
		}
		q.reason = guests.ReasonSignedOut
		if d, ok := apperr.From(err).Details.(map[string]any); ok {
			if r, ok := d["reason"].(string); ok {
				q.reason = r
			}
		}
		guestErr(w, err)
		return nil, false
	}
	q.signed, q.session = s, s.Session.ID
	return s, true
}

// appAs is the App as a guest: what they do is recorded as theirs.
func (g *guestGate) appAs(s *guests.Signed) *app.App {
	a := *g.s.App
	a.Actor = guests.Actor(&s.Guest, g.s.App.Cfg.Installation.ID)
	return &a
}

// call runs a guest operation as the signed-in guest: through the registry,
// as a person (the guest pressed something), a guest caller, with the
// guest's checked sign-in in the context — never an owner, never a member.
func (g *guestGate) call(r *http.Request, s *guests.Signed, op string, in any) (any, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return nil, err
	}
	ctx := guests.WithSigned(r.Context(), s)
	return ops.Call(ctx, g.appAs(s), op, raw, ops.Caller{Human: true, Remote: true, Elsewhere: true, Guest: true})
}

// readBody decodes a guest's request strictly: exactly the type's fields,
// one value, within guestBodyLimit.
func readBody(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, guestBodyLimit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apperr.New(apperr.InvalidInput, "MetaRush couldn't read the request.")
	}
	if dec.More() {
		return apperr.New(apperr.InvalidInput, "MetaRush couldn't read the request.")
	}
	return nil
}

/* ---------- The state, signing in by link, signing out ---------- */

// GuestState is GET /guest/api/state on the guest listener: whether this
// browser is signed in as a guest, and how a guest signs in here.
type GuestState struct {
	SignedIn bool `json:"signedIn"`
	// Guest is who is signed in.
	Guest *guests.GuestSelf `json:"guest,omitempty"`
	// SSO is the organization's sign-in guests may use ("" none).
	SSO string `json:"sso,omitempty"`
}

// GuestLinkIn is POST /guest/api/link: the one-time link's token, from the
// address's fragment.
type GuestLinkIn struct {
	Token string `json:"token"`
}

// GuestSignedIn answers a sign-in.
type GuestSignedIn struct {
	Guest guests.GuestSelf `json:"guest"`
}

// GuestSSOStartOut is POST /guest/api/sso/start: where the browser goes.
type GuestSSOStartOut struct {
	URL string `json:"url"`
}

// GuestCommentBody is POST /guest/api/versions/{version}/comments.
type GuestCommentBody struct {
	At   *float64 `json:"at,omitempty"`
	Body string   `json:"body"`
}

func (g *guestGate) state(w http.ResponseWriter, r *http.Request) {
	q := reqOf(r)
	q.what = "guest.page"
	out := GuestState{}
	if btn := ssoButton(auth.Settings(r.Context(), g.s.App)); btn != nil {
		out.SSO = btn.Title
	}
	if tok := g.token(r); tok != "" {
		g.s.App.Log.Secret(tok)
		if s, err := guests.Check(r.Context(), g.s.App, tok, q.from); err == nil {
			q.signed, q.session = s, s.Session.ID
			out.SignedIn, out.Guest = true, &guests.GuestSelf{Name: s.Guest.Name, Email: s.Guest.Email}
		} else {
			g.setCookie(w, q.secure, "")
		}
	}
	writeOK(w, out)
}

// startGuestSession signs a browser in as a guest with a new sign-in (the
// one it had ends: no fixation) and sets its cookie.
func (g *guestGate) startGuestSession(w http.ResponseWriter, r *http.Request, s *guests.Signed, via, provider string) error {
	q := reqOf(r)
	if old := g.token(r); old != "" {
		guests.EndSession(r.Context(), g.s.App, old)
	}
	tok, err := guests.NewSession(r.Context(), g.s.App, &s.Guest, via, provider, q.from)
	if err != nil {
		return apperr.Wrap(apperr.Internal, err, "MetaRush couldn't sign you in. Try again.")
	}
	g.s.App.Log.Secret(tok)
	g.setCookie(w, q.secure, tok)
	q.signed = s
	return nil
}

func (g *guestGate) useLink(w http.ResponseWriter, r *http.Request) {
	q := reqOf(r)
	q.what = "guest.signin"
	if !g.signIn.allow(q.from, time.Now()) {
		q.reason = guests.ReasonThrottled
		guestError(w, http.StatusTooManyRequests, apperr.New(apperr.Unauthorized, "Too many sign-in tries from this browser. Wait a minute, then try again.").WithDetails(map[string]any{"reason": guests.ReasonThrottled}))
		return
	}
	var in GuestLinkIn
	if err := readBody(r, &in); err != nil {
		q.what, q.reason = "guest.signin.refused", "bad_request"
		guestErr(w, err)
		return
	}
	g.s.App.Log.Secret(in.Token)
	gu, err := guests.Redeem(r.Context(), g.s.App, in.Token)
	if err != nil {
		q.what, q.reason = "guest.signin.refused", guests.ReasonLinkGone
		guestErr(w, err)
		return
	}
	if err := g.startGuestSession(w, r, &guests.Signed{Guest: *gu}, "link", ""); err != nil {
		guestErr(w, err)
		return
	}
	writeOK(w, GuestSignedIn{Guest: guests.GuestSelf{Name: gu.Name, Email: gu.Email}})
}

func (g *guestGate) signOut(w http.ResponseWriter, r *http.Request) {
	q := reqOf(r)
	q.what = "guest.signout"
	if tok := g.token(r); tok != "" {
		g.s.App.Log.Secret(tok)
		if s, err := guests.Check(r.Context(), g.s.App, tok, q.from); err == nil {
			q.signed = s
		}
		guests.EndSession(r.Context(), g.s.App, tok)
	}
	g.setCookie(w, q.secure, "")
	writeOK(w, map[string]bool{"signedOut": true})
}

/* ---------- The guest API ---------- */

func (g *guestGate) shared(w http.ResponseWriter, r *http.Request) {
	reqOf(r).what = "guest.shared"
	s, ok := g.signed(w, r)
	if !ok {
		return
	}
	out, err := g.call(r, s, "guest.shared", guests.GuestSharedIn{})
	if err != nil {
		guestErr(w, err)
		return
	}
	writeOK(w, out)
}

// versionParam is a version's ID from the address, by its shape (anything
// else is the one "not shared" answer).
func versionParam(r *http.Request) (string, bool) {
	v := mux.Vars(r)["version"]
	return v, ids.Valid(ids.Revision, v)
}

func (g *guestGate) watch(w http.ResponseWriter, r *http.Request) {
	q := reqOf(r)
	q.what = "guest.watch"
	s, ok := g.signed(w, r)
	if !ok {
		return
	}
	v, ok := versionParam(r)
	if !ok {
		guestErr(w, notSharedErr())
		return
	}
	q.revisionID = v
	out, err := g.call(r, s, "guest.watch", guests.GuestWatchIn{Version: v})
	if err != nil {
		q.reason = reasonOf(err)
		guestErr(w, err)
		return
	}
	q.projectID = projectOfVersion(r, g, v)
	writeOK(w, out)
}

func (g *guestGate) comments(w http.ResponseWriter, r *http.Request) {
	q := reqOf(r)
	q.what = "guest.comments"
	s, ok := g.signed(w, r)
	if !ok {
		return
	}
	v, ok := versionParam(r)
	if !ok {
		guestErr(w, notSharedErr())
		return
	}
	q.revisionID = v
	out, err := g.call(r, s, "guest.comments", guests.GuestCommentsIn{Version: v})
	if err != nil {
		q.reason = reasonOf(err)
		guestErr(w, err)
		return
	}
	q.projectID = projectOfVersion(r, g, v)
	writeOK(w, out)
}

func (g *guestGate) comment(w http.ResponseWriter, r *http.Request) {
	q := reqOf(r)
	q.what = "guest.comment"
	s, ok := g.signed(w, r)
	if !ok {
		return
	}
	v, ok := versionParam(r)
	if !ok {
		guestErr(w, notSharedErr())
		return
	}
	q.revisionID = v
	var in GuestCommentBody
	if err := readBody(r, &in); err != nil {
		q.reason = "bad_request"
		guestErr(w, err)
		return
	}
	out, err := g.call(r, s, "guest.comment", guests.GuestCommentIn{Version: v, At: in.At, Body: in.Body})
	if err != nil {
		q.reason = reasonOf(err)
		guestErr(w, err)
		return
	}
	q.projectID = projectOfVersion(r, g, v)
	writeOK(w, out)
}

// notSharedErr is the one answer for what isn't shared (as the guest
// operations give it).
func notSharedErr() error {
	return apperr.New(apperr.NotFound, "That isn't shared with you.").WithHint("Open what was shared with you from your guest page.").
		WithDetails(map[string]any{"reason": guests.ReasonNotShared})
}

// reasonOf is an error's reason for the request's record.
func reasonOf(err error) string {
	if d, ok := apperr.From(err).Details.(map[string]any); ok {
		if r, ok := d["reason"].(string); ok {
			return r
		}
	}
	return ""
}

// projectOfVersion is a version's project, for the record of a request that
// was let through (the guest operation found it shared).
func projectOfVersion(r *http.Request, g *guestGate, revisionID string) string {
	var rev struct {
		ProjectID string `bson:"projectId"`
	}
	if err := g.s.App.DB.GetByID(r.Context(), store.CRevisions, g.s.App.WorkspaceID(), revisionID, &rev); err != nil {
		return ""
	}
	return rev.ProjectID
}

/* ---------- Review copies kept on this computer ---------- */

// guestMedia serves a review copy kept on this computer, with ranges, after
// the guest's sign-in and the version's grant are checked; the answer stops
// soon after the sign-in stops counting.
func (g *guestGate) media(w http.ResponseWriter, r *http.Request) { g.serveMedia(w, r, false) }

func (g *guestGate) still(w http.ResponseWriter, r *http.Request) { g.serveMedia(w, r, true) }

func (g *guestGate) serveMedia(w http.ResponseWriter, r *http.Request, still bool) {
	q := reqOf(r)
	q.what = "guest.media"
	s, ok := g.signed(w, r)
	if !ok {
		return
	}
	v := mux.Vars(r)
	if !ids.Valid(ids.Revision, v["version"]) || !ids.Valid(ids.Render, v["render"]) {
		guestErr(w, notSharedErr())
		return
	}
	rd, seen, err := guests.MediaFor(r.Context(), g.s.App, s, v["version"], v["render"])
	if err != nil {
		q.reason = reasonOf(err)
		guestErr(w, err)
		return
	}
	q.projectID, q.revisionID = guests.Seen(seen)
	q.media = rd.ID
	if still {
		q.media += ":still"
	}
	path, ok := g.localFile(rd.Output.StorageKey, rd.Output.SHA256, rd.Output.Size)
	mime := "video/mp4"
	if still {
		if rd.Output.ThumbnailKey == "" {
			guestError(w, http.StatusNotFound, apperr.New(apperr.NotFound, "This version has no still."))
			return
		}
		mime = "image/jpeg"
		path, ok = g.localFile(rd.Output.ThumbnailKey, "", -1)
	}
	if !ok || !isLocalStore(g.s.App) {
		// Media in a bucket is watched through a signed link, never through
		// this computer (D493).
		guestErr(w, notSharedErr())
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "private, no-store")
	ctx, cancel := g.whileGuest(r, s)
	defer cancel()
	serveFileUntil(ctx, w, r.WithContext(ctx), path)
}

// localFile is where a stored file is on this computer: the cache, or the
// media folder when the media is kept here.
func (g *guestGate) localFile(key, sha string, size int64) (string, bool) {
	a := g.s.App
	if sha != "" && a.Cache.Has(sha, size) {
		a.Cache.Touch(sha)
		return a.Cache.Path(sha), true
	}
	if p, ok := localPathOf(a, key); ok {
		return p, true
	}
	return "", false
}

// whileGuest ends a long answer (a video being sent) soon after the guest's
// sign-in stops counting: taken back, signed out, guests off.
func (g *guestGate) whileGuest(r *http.Request, s *guests.Signed) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(r.Context())
	go func() {
		t := time.NewTicker(guestRecheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !guests.StillSignedIn(ctx, g.s.App, s) {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, cancel
}

// errStopped ends a file being sent.
var errStopped = errors.New("stopped")

// stopper reads a file until its context ends.
type stopper struct {
	ctx context.Context
	f   io.ReadSeeker
}

func (s stopper) Read(p []byte) (int, error) {
	if s.ctx.Err() != nil {
		return 0, errStopped
	}
	return s.f.Read(p)
}

func (s stopper) Seek(off int64, whence int) (int64, error) { return s.f.Seek(off, whence) }

func (g *guestGate) page(w http.ResponseWriter, r *http.Request) {
	q := reqOf(r)
	q.what = "guest.page"
	if tok := g.token(r); tok != "" {
		g.s.App.Log.Secret(tok)
		if s, err := guests.Check(r.Context(), g.s.App, tok, q.from); err == nil {
			q.signed, q.session = s, s.Session.ID
		}
	}
	b, csp := guestPageFor(g.bucketOrigin())
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", csp)
	h.Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_, _ = w.Write(b)
}

// bucketOrigin is the media store's address when it signs links ("" when
// the media is kept on this computer).
func (g *guestGate) bucketOrigin() string {
	g.bucketOnce.Do(func() {
		if isLocalStore(g.s.App) {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if u, err := g.s.App.Media.PresignGet(ctx, "guest-page-origin", time.Minute, "", ""); err == nil {
			g.bucket = originOf(u)
		}
	})
	return g.bucket
}

/* ---------- Running it ---------- */

// guestListener runs the guest listener of the background MetaRush: started,
// moved or stopped as this computer's settings say (guests.listen,
// guests.port), on 127.0.0.1 only.
type guestListener struct {
	mu   sync.Mutex
	srv  *http.Server
	port int
}

// ApplyGuests starts, moves or stops the guest listener as this computer's
// settings say now. It reports a listener that couldn't start.
func (s *Server) ApplyGuests(ctx context.Context) error {
	l := s.App.Cfg.GuestListener()
	gl := &s.guestRun
	gl.mu.Lock()
	defer gl.mu.Unlock()
	want := 0
	if l.Listen && !s.held && !s.remoteOff.Load() {
		want = l.PortOrDefault()
	}
	if gl.srv != nil && gl.port == want {
		return nil
	}
	if gl.srv != nil {
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = gl.srv.Shutdown(c)
		cancel()
		gl.srv, gl.port = nil, 0
		guests.SayStopped(s.App.Cfg)
		s.Log("guests: this computer stopped answering guests")
	}
	if want == 0 {
		return nil
	}
	srv, port, err := s.ListenGuests(ctx, want)
	if err != nil {
		s.Log("guests: %s", apperr.From(err).Message)
		return err
	}
	gl.srv, gl.port = srv, port
	guests.SayListening(s.App.Cfg, port)
	s.Log("guests: this computer answers guests at http://%s:%d (on this computer only)", config.GuestHost, port)
	return nil
}

// ListenGuests serves the guest listener at 127.0.0.1:port until ctx ends
// (port 0: a free one, for tests) and returns the server and its port. It
// never binds anything but 127.0.0.1.
func (s *Server) ListenGuests(ctx context.Context, port int) (*http.Server, int, error) {
	ln, err := guestListen(port)
	if err != nil {
		return nil, 0, err
	}
	got := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: s.GuestHandler(got), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: time.Minute, IdleTimeout: 2 * time.Minute,
		MaxHeaderBytes: 32 << 10}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
	}()
	return srv, got, nil
}

// guestListen binds the guest listener: 127.0.0.1 alone, whatever anything
// says (D491).
func guestListen(port int) (net.Listener, error) {
	if port < 0 || port > 65535 {
		return nil, apperr.New(apperr.InvalidInput, "The guest listener needs a port from 1024 to 65535.")
	}
	addr := net.JoinHostPort(config.GuestHost, strconv.Itoa(port))
	ln, err := net.Listen("tcp4", addr)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "The guest listener can't start: something on this computer already uses port %d.", port).
			WithHint("Choose another port for guests in Settings → People → Guests.").WithCommand("metarush guests set --port <another port>")
	}
	if a, ok := ln.Addr().(*net.TCPAddr); !ok || !a.IP.IsLoopback() || a.IP.String() != config.GuestHost {
		_ = ln.Close()
		return nil, fmt.Errorf("the guest listener bound %s, not this computer's own address", ln.Addr())
	}
	return ln, nil
}
