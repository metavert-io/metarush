package api

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/remote"
	"github.com/metavert-io/metarush/internal/sessions"
)

// Remote access (D128, D129). MetaRush listens on 127.0.0.1 only.
// `tailscale serve` answers at this computer's tailnet name with HTTPS and
// hands each request to the dashboard from loopback, keeping the Host the
// browser asked for and adding, for people on the tailnet, the
// Tailscale-User-Login and Tailscale-User-Name headers. These checks decide
// what such a request may do (the threat model is in docs/security.md):
//
//   - The public address's Host is accepted only from loopback and only when
//     dashboard.publicURL names it; every other name is still refused (DNS
//     rebinding stays defeated).
//   - Tailscale-* headers are believed only on such a request. Anywhere else
//     (another Host, or a request that didn't come from loopback) they are
//     pretending, and the request is refused.
//   - Tailscale Funnel (visits from the public internet) is refused.
//   - X-Forwarded-Proto is believed only on the public address; https there
//     makes the session cookie Secure (and __Host- prefixed).
//   - With dashboard.tailscaleLogins, a listed account is signed in by itself:
//     its session is bound to that account and checked against the header and
//     the list on every request, so removing someone signs them out at once.
//     An account not on the list gets a plain page that says how the owner
//     adds them. Cross-site requests never sign anyone in.
//   - CSRF (X-MetaRush-Client on changes) and SameSite=Strict apply as for this
//     computer's own browser.

// access is where a request came from.
type access struct {
	// public: through the public address (tailscale serve), from loopback.
	public bool
	// secure: the browser reached the public address over HTTPS.
	secure bool
	// login and name are the Tailscale account (lower case) and its display
	// name, only on the public address and only from a Tailscale address.
	login, name string
	// from is the Tailscale address of the browser's computer.
	from string
}

// person is who a request acts as.
type person struct {
	UserID, UserName, Email string
	// Login is the Tailscale account, when signed in with it.
	Login string
	// Via is how they signed in: link (a one-time sign-in link on this
	// computer), password or tailscale (D183).
	Via string
	// token is the session cookie's value (to see that it still exists).
	token string
}

type accessKey struct{}
type personKey struct{}

func accessOf(r *http.Request) access {
	a, _ := r.Context().Value(accessKey{}).(access)
	return a
}

func personOf(r *http.Request) (person, bool) {
	p, ok := r.Context().Value(personKey{}).(person)
	return p, ok
}

// fromLoopback reports whether the connection came from this computer itself.
func fromLoopback(r *http.Request) bool {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	return ap.Addr().Unmap().IsLoopback()
}

// hasTailscaleHeaders reports any header only `tailscale serve` may set.
func hasTailscaleHeaders(r *http.Request) bool {
	for k := range r.Header {
		if len(k) >= 10 && strings.EqualFold(k[:10], "tailscale-") {
			return true
		}
	}
	return false
}

// tailnetRanges are the addresses Tailscale gives computers (CGNAT range and
// its IPv6 prefix): the only addresses tailscale serve forwards for.
var tailnetRanges = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}

func tailnetAddress(s string) (string, bool) {
	s = strings.TrimSpace(strings.Split(s, ",")[0])
	a, err := netip.ParseAddr(s)
	if err != nil {
		return "", false
	}
	a = a.Unmap()
	for _, p := range tailnetRanges {
		if p.Contains(a) {
			return a.String(), true
		}
	}
	return a.String(), false
}

// localHosts are the Host values this computer's own browser sends.
func (s *Server) localHosts() map[string]bool {
	allowed := map[string]bool{}
	port := strconv.Itoa(s.App.Cfg.Dashboard.Port)
	for _, h := range []string{"127.0.0.1", "localhost", "[::1]", s.App.Cfg.Dashboard.Host} {
		allowed[h+":"+port] = true
	}
	for _, o := range s.App.Cfg.Dashboard.DevOrigins {
		o = strings.TrimPrefix(strings.TrimPrefix(o, "http://"), "https://")
		allowed[strings.TrimSuffix(o, "/")] = true
	}
	return allowed
}

// remoteFile notices config.yaml changing (`metarush remote …` in a terminal)
// so the next request already uses the new remote-access settings; the
// daemon's config poller would take a few seconds.
type remoteFile struct {
	mu   sync.Mutex
	seen time.Time
}

func (s *Server) watchRemoteFile() {
	if st, err := os.Stat(config.Path(s.App.Cfg.Home())); err == nil {
		s.cfgFile.seen = st.ModTime()
	}
}

func (s *Server) syncRemote() {
	if s.App.Cfg.Home() == "" {
		return
	}
	st, err := os.Stat(config.Path(s.App.Cfg.Home()))
	if err != nil {
		return
	}
	f := &s.cfgFile
	f.mu.Lock()
	defer f.mu.Unlock()
	if st.ModTime().Equal(f.seen) {
		return
	}
	d, err := config.LoadRemote(s.App.Cfg.Home())
	if err != nil {
		return // a broken file keeps the settings in effect (the config poller says why)
	}
	f.seen = st.ModTime()
	_ = s.App.Cfg.SetRemote(d)
}

// hostGuard rejects requests whose Host is not this dashboard: this defeats DNS
// rebinding, where a hostile web page resolves its own domain to 127.0.0.1.
// The public address (remote access) is accepted from loopback only, which is
// how tailscale serve connects; Tailscale headers anywhere else are refused.
func (s *Server) hostGuard(next http.Handler) http.Handler {
	local := s.localHosts()
	s.watchRemoteFile()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.syncRemote()
		rm := s.App.Cfg.Remote()
		loop := fromLoopback(r)
		public := rm.On() && rm.MatchesHost(r.Host)
		switch {
		case public && !loop:
			// The public name, but not from tailscale serve on this computer.
			s.refuse(w, r, refusedSpoofed, "")
			return
		case public:
		case !local[r.Host] && loop && hasTailscaleHeaders(r):
			// tailscale serve, but remote access isn't turned on, or the visit
			// used another of this computer's tailnet names (the short one, say).
			if rm.On() {
				s.refuse(w, r, refusedOtherName, rm.PublicURL)
			} else {
				s.refuse(w, r, refusedOff, "")
			}
			return
		case !local[r.Host]:
			http.Error(w, "MetaRush only answers on this machine, at 127.0.0.1 or localhost.", http.StatusForbidden)
			return
		case hasTailscaleHeaders(r):
			// Only tailscale serve sends these, and only on the public address.
			s.refuse(w, r, refusedSpoofed, "")
			return
		default:
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessKey{}, access{})))
			return
		}
		if r.Header.Get("Tailscale-Funnel-Request") != "" {
			s.refuse(w, r, refusedFunnel, "")
			return
		}
		acc := access{public: true, secure: strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")}
		from, onTailnet := tailnetAddress(r.Header.Get("X-Forwarded-For"))
		acc.from = from
		if login := strings.ToLower(remote.DecodeHeader(r.Header.Get("Tailscale-User-Login"))); login != "" && onTailnet {
			acc.login, acc.name = login, remote.DecodeHeader(r.Header.Get("Tailscale-User-Name"))
		}
		if rm.IdentitySignIn() && acc.login != "" && !rm.Allowed(acc.login) && auth.Method(r.Context(), s.App) == models.AuthTailscale {
			// Tailscale identity counts only when it is the sign-in method (D183).
			s.refuse(w, r, refusedNotListed, acc.login)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), accessKey{}, acc)))
	})
}

const (
	refusedSpoofed   = "spoofed"
	refusedFunnel    = "funnel"
	refusedNotListed = "not_listed"
	refusedOff       = "remote_off"
	refusedOtherName = "other_name"
)

// refuse turns a visit away: a plain page for a browser opening a page, the
// usual error answer for the dashboard's own requests and the extension.
// subject is the Tailscale account (not_listed) or the right address (other_name).
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, reason, subject string) {
	login := subject
	machine := s.App.Cfg.MachineName()
	var ae *apperr.Error
	var title, owner string
	switch reason {
	case refusedNotListed:
		title = "You can't open MetaRush here yet"
		ae = apperr.New(apperr.Unauthorized, "MetaRush on %s lets in only the Tailscale accounts its owner added, and %s isn't one of them yet.", machine, login).
			WithHint("Ask the owner to add you: in MetaRush on that computer, Settings, Remote access, Add a person.").
			WithDetails(map[string]any{"reason": reason, "login": login}).WithCommand("metarush remote allow " + login)
		owner = fmt.Sprintf("To let you in, the owner adds your account (%s) on %s: in MetaRush there, Settings, then Remote access, then Add a person.", login, machine)
	case refusedFunnel:
		title = "MetaRush isn't open to the internet"
		ae = apperr.New(apperr.Unauthorized, "This visit came from the public internet through Tailscale Funnel, and MetaRush only lets in people on its owner's tailnet.").
			WithHint("Turn Funnel off for this address in Tailscale.").WithDetails(map[string]any{"reason": reason})
		owner = "If you run this computer: turn Tailscale Funnel off for this address."
	case refusedOtherName:
		title = "MetaRush has another address"
		ae = apperr.New(apperr.Unauthorized, "MetaRush on %s answers at %s, not at this address.", machine, subject).
			WithHint("Open that address instead.").WithDetails(map[string]any{"reason": reason, "url": subject})
		owner = "Open " + subject + " instead (bookmark it)."
	case refusedOff:
		title = "MetaRush isn't open to other computers"
		ae = apperr.New(apperr.Unauthorized, "MetaRush on %s answers only on that computer: remote access isn't turned on there.", machine).
			WithHint("Its owner turns remote access on in a terminal on that computer.").WithDetails(map[string]any{"reason": reason}).WithCommand("metarush remote enable")
		owner = "Its owner can turn remote access on in a terminal on that computer."
	default:
		title = "MetaRush turned this visit away"
		ae = apperr.New(apperr.Unauthorized, "This visit said it came through Tailscale, but it didn't come through Tailscale's secure address on this computer, so MetaRush doesn't believe it.").
			WithDetails(map[string]any{"reason": refusedSpoofed})
		owner = "Open MetaRush at its Tailscale address, or on the computer that runs it."
	}
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cache-Control", "no-store")
	if !wantsPage(r) {
		writeJSON(w, http.StatusForbidden, map[string]any{"ok": false, "error": ae})
		return
	}
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(http.StatusForbidden)
	cmd := ""
	if c := ae.Command(); c != "" && reason == refusedNotListed {
		cmd = `<details><summary>For the owner: the command for a terminal</summary><pre>` + html.EscapeString(c) + `</pre></details>`
	}
	fmt.Fprintf(w, refusalPage, html.EscapeString(title), html.EscapeString(title), html.EscapeString(ae.Message), html.EscapeString(owner), cmd)
}

// wantsPage reports a browser opening a page (not the dashboard's own API
// calls, media or the extension).
func wantsPage(r *http.Request) bool {
	p := r.URL.Path
	if strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/ext/") || strings.HasPrefix(p, "/media/") || r.Header.Get(clientHeader) != "" {
		return false
	}
	return r.Method == http.MethodGet && (strings.Contains(r.Header.Get("Accept"), "text/html") || r.Header.Get("Sec-Fetch-Mode") == "navigate")
}

const refusalPage = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>%s</title>
<style>
:root{color-scheme:light dark}body{margin:0;min-height:100dvh;display:grid;place-items:center;font:15px/1.55 system-ui,-apple-system,sans-serif;background:#0b0b0e;color:#ece9e4}
@media (prefers-color-scheme: light){body{background:#f6f4f0;color:#1d1b18}main{background:#fff!important;border-color:#e5e0d8!important}p{color:#5c564e!important}}
main{max-width:520px;margin:24px;padding:32px;border-radius:18px;background:#16161b;border:1px solid #2a2a31}
h1{font-size:22px;line-height:1.25;margin:0 0 14px;letter-spacing:-.02em}p{margin:10px 0;color:#b5aca2}
details{margin-top:18px;font-size:13px}pre{white-space:pre-wrap;word-break:break-all;padding:10px 12px;border-radius:10px;background:rgba(127,127,127,.12)}
</style></head><body><main data-metarush-refusal><h1>%s</h1><p>%s</p><p>%s</p>%s</main></body></html>`

/* ---------- who a request acts as ---------- */

// sessionCookie is the cookie's name: __Host- prefixed (only this exact
// address, Secure, never set by a sibling name on the tailnet) when the
// browser came over HTTPS to the public address.
func sessionCookie(acc access) string {
	if acc.public && acc.secure {
		return "__Host-" + cookieName
	}
	return cookieName
}

func (s *Server) setSession(w http.ResponseWriter, acc access, val string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie(acc), Value: val, Path: "/", HttpOnly: true, Secure: acc.public && acc.secure,
		SameSite: http.SameSiteStrictMode, MaxAge: int(sessions.TTL / time.Second)})
}

// sameOriginOrDirect reports a request a person's own browser made from the
// dashboard itself (or a non-browser client): never a cross-site one.
func sameOriginOrDirect(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "", "same-origin", "none":
		return true
	}
	return false
}

// signedIn says who a request acts as (ok false: not signed in). A listed
// Tailscale account on the public address is signed in by itself.
func (s *Server) signedIn(w http.ResponseWriter, r *http.Request) (person, bool, error) {
	acc := accessOf(r)
	rm := s.App.Cfg.Remote()
	ctx := r.Context()
	method := auth.Method(ctx, s.App)
	st := sessions.For(s.App.Cfg.StateDir())
	var sess sessions.Session
	var have bool
	var token string
	if c, err := r.Cookie(sessionCookie(acc)); err == nil && c.Value != "" {
		token = c.Value
		if sess, have = st.Get(token, time.Now()); have {
			s.App.Log.Secret(token) // a session cookie never reaches the activity log
		}
	}
	if have && sess.Public != acc.public {
		have = false // a session is only good where it was made
	}
	if method == models.AuthTailscale && acc.public && rm.IdentitySignIn() && acc.login != "" {
		// hostGuard already turned away accounts that aren't on the list.
		if have && sess.Login == acc.login {
			return person{UserID: sess.UserID, UserName: sess.UserName, Email: emailOf(sess.Login), Login: sess.Login, Via: "tailscale", token: token}, true, nil
		}
		if !sameOriginOrDirect(r) {
			return person{}, false, nil
		}
		p, err := s.signInTailscale(ctx, w, acc)
		if err != nil {
			return person{}, false, err
		}
		return p, true, nil
	}
	if !have {
		return person{}, false, nil
	}
	end := func() (person, bool, error) {
		_ = st.Remove(token)
		return person{}, false, nil
	}
	switch {
	case sess.Login != "" || sess.Method == "tailscale":
		// A Tailscale sign-in whose account is no longer let in, no longer
		// sent, or Tailscale isn't the sign-in method any more: it ends here.
		return end()
	case sess.Method == models.AuthPassword:
		if method != models.AuthPassword || time.Since(sess.LastUsed) > auth.SessionIdle || !auth.SessionValid(ctx, s.App, sess.UserID, sess.CreatedAt) {
			return end()
		}
		u, err := auth.GetUser(ctx, s.App, sess.UserID)
		name, email := sess.UserName, ""
		if err == nil {
			name, email = u.Name, u.Email
		}
		st.Touch(token, time.Now())
		return person{UserID: sess.UserID, UserName: name, Email: email, Via: "password", token: token}, true, nil
	}
	// A one-time link's session: the owner, on this computer only (D183).
	if acc.public {
		return end()
	}
	u := s.App.Cfg.User
	if !auth.SessionValid(ctx, s.App, u.ID, sess.CreatedAt) {
		return end()
	}
	return person{UserID: u.ID, UserName: u.Name, Email: u.Email, Via: "link", token: token}, true, nil
}

// whileSignedIn ends a long-lived request (the live event streams) soon after
// its sign-in stops counting: the person was taken off the list, Tailscale
// sign-in or remote access was turned off, or the session was signed out.
func (s *Server) whileSignedIn(r *http.Request, who person) *http.Request {
	ctx, cancel := context.WithCancel(r.Context())
	acc := accessOf(r)
	still := func() bool {
		rm := s.App.Cfg.Remote()
		if acc.public && (!rm.On() || !rm.MatchesHost(r.Host)) {
			return false
		}
		if who.Via == "tailscale" && (!rm.IdentitySignIn() || !rm.Allowed(who.Login)) {
			return false
		}
		if who.token != "" {
			if _, ok := sessions.For(s.App.Cfg.StateDir()).Get(who.token, time.Now()); !ok {
				return false
			}
		}
		return true
	}
	go func() {
		t := time.NewTicker(streamRecheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !still() {
					cancel()
					return
				}
			}
		}
	}()
	return r.WithContext(ctx)
}

// streamRecheck is how often a live stream checks that its sign-in still counts.
var streamRecheck = 10 * time.Second

func emailOf(login string) string {
	if _, domain, ok := strings.Cut(login, "@"); ok && strings.Contains(domain, ".") {
		return login
	}
	return ""
}

// signInTailscale starts a session for a listed Tailscale account (the person
// is made on their first sign-in) and records it in the audit trail.
func (s *Server) signInTailscale(ctx context.Context, w http.ResponseWriter, acc access) (person, error) {
	u, created, err := remote.PersonFor(ctx, s.App, acc.login, acc.name)
	if err != nil {
		return person{}, err
	}
	val, err := sessions.For(s.App.Cfg.StateDir()).New(sessions.Session{UserID: u.ID, UserName: u.Name, Login: acc.login, Public: true, From: acc.from, Method: "tailscale"}, time.Now())
	if err != nil {
		return person{}, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't sign you in.")
	}
	s.App.Log.Secret(val)
	s.setSession(w, acc, val)
	p := person{UserID: u.ID, UserName: u.Name, Email: emailOf(acc.login), Login: acc.login, Via: "tailscale"}
	p.token = val
	a := s.appAs(p, "dashboard")
	a.Audit(ctx, "remote.signin", "user", u.ID, "", map[string]any{"login": acc.login, "from": acc.from, "firstTime": created})
	return p, nil
}

// appFor is the App as the person a request acts as: what they do is theirs
// in the audit trail, the activity log and "Approved by".
func (s *Server) appFor(r *http.Request, via string) *app.App {
	p, _ := personOf(r)
	return s.appAs(p, via)
}

func (s *Server) appAs(p person, via string) *app.App {
	a := *s.App
	a.Actor.Via = via
	if p.UserID != "" {
		a.Actor.UserID, a.Actor.UserName = p.UserID, p.UserName
	}
	return &a
}
