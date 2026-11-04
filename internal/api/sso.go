package api

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sso"
)

// Signing in with an organization's account (D425–D434, docs/accounts.md).
// Three steps, all on the dashboard's own address (127.0.0.1 only; other
// computers through tailscale serve, D128):
//
//   - POST /api/auth/sso/start (the sign-in or invite page; the dashboard's
//     header, same-origin, the per-address limit, HTTPS on the public address)
//     keeps a sign-in for this browser and answers with its begin address — on
//     the origin the provider will send the browser back to (127.0.0.1 on this
//     computer, never localhost, which may reach another program on [::1],
//     D451; the public address elsewhere), so the cookie set there comes back
//     with the answer.
//   - GET /auth/sso/begin?t=<ticket> (the ticket works once, within two
//     minutes) adds the sign-in's state to an HttpOnly cookie holding this
//     browser's sign-ins in progress (a few at once: two tabs can sign in,
//     D454; SameSite=Lax: it must ride along the provider's redirect back;
//     __Host- over HTTPS) and sends it to the provider with that state, a
//     nonce and a PKCE challenge.
//   - GET /auth/sso/callback?code&state — only in the browser whose cookie is
//     that state (login CSRF: someone else's answer opened in your browser is
//     refused), once (the sign-in's record is taken), on the address it began
//     at — trades the code with the PKCE verifier, checks the ID token
//     (internal/sso), decides who it is (auth.PersonForSSO) and starts an
//     ordinary session (method sso), or for a test records how it went.

// SSOButton is the organization sign-in a page offers.
type SSOButton struct {
	Provider string `json:"provider" jsonschema:"enum=google,enum=microsoft,enum=oidc"`
	// Title is the provider's name: "Sign in with <Title>".
	Title string `json:"title"`
}

// SSOStart is POST /api/auth/sso/start.
type SSOStart struct {
	// Next is where to go after signing in (a path on this site).
	Next string `json:"next,omitempty"`
	// Invite is the invite link's token of a person joining.
	Invite string `json:"invite,omitempty"`
}

// SSOStartResult says where the browser goes to sign in.
type SSOStartResult struct {
	URL string `json:"url"`
}

const ssoCookie = "mr_sso"

// ssoPending is how many sign-ins in progress one browser keeps at once (the
// oldest gives way).
const ssoPending = 4

// pendingStates reads the states of a browser's sign-ins in progress.
func pendingStates(r *http.Request, acc access) []string {
	c, err := r.Cookie(ssoCookieName(acc))
	if err != nil || c.Value == "" {
		return nil
	}
	var out []string
	for _, st := range strings.Split(c.Value, ".") {
		if st != "" && len(st) <= 100 {
			out = append(out, st)
		}
	}
	return out
}

// setPending writes them back (none: the cookie goes).
func setPending(w http.ResponseWriter, acc access, states []string) {
	if len(states) > ssoPending {
		states = states[:ssoPending]
	}
	c := &http.Cookie{Name: ssoCookieName(acc), Value: strings.Join(states, "."), Path: "/", HttpOnly: true, Secure: acc.public && acc.secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sso.FlowLife / time.Second)}
	if len(states) == 0 {
		c.Value, c.MaxAge = "", -1
	}
	http.SetCookie(w, c)
}

// takePending reports whether state is one of the browser's sign-ins in
// progress (compared in constant time) and the rest of them.
func takePending(states []string, state string) (bool, []string) {
	found := false
	var rest []string
	for _, st := range states {
		if subtle.ConstantTimeCompare([]byte(st), []byte(state)) == 1 {
			found = true
			continue
		}
		rest = append(rest, st)
	}
	return found, rest
}

func ssoCookieName(acc access) string {
	if acc.public && acc.secure {
		return "__Host-" + ssoCookie
	}
	return ssoCookie
}

func ssoButton(st models.AuthSettings) *SSOButton {
	if st.Method != models.AuthSSO || st.SSO == nil {
		return nil
	}
	return &SSOButton{Provider: st.SSO.Provider, Title: sso.Title(st.SSO.Provider, st.SSO.Label)}
}

// nextPath keeps a "next" that is a path on this site, else "/" (never
// another site, a protocol-relative address, the API or a sign-in step).
func nextPath(next string) string {
	if next == "" || len(next) > 512 || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n\t\x00") {
		return "/"
	}
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || strings.HasPrefix(u.Path, "//") {
		return "/"
	}
	for _, p := range []string{"/api/", "/auth", "/media/", "/ext/", "/signin", "/welcome/"} {
		if strings.HasPrefix(u.Path, p) {
			return "/"
		}
	}
	return next
}

func (s *Server) ssoStart(w http.ResponseWriter, r *http.Request) {
	if !s.signInGuard(w, r) {
		return
	}
	ctx := r.Context()
	st := auth.Settings(ctx, s.App)
	if st.Method != models.AuthSSO || st.SSO == nil {
		writeErr(w, apperr.New(apperr.Conflict, "People don't sign in with an organization's account here.").WithDetails(map[string]any{"reason": "no_sso"}))
		return
	}
	var in SSOStart
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	acc := accessOf(r)
	f := sso.Flow{Purpose: sso.ForSignIn, Public: acc.public, Setup: auth.LiveUse(s.App.Cfg, st.SSO).Fingerprint(), Next: nextPath(in.Next)}
	if in.Invite != "" {
		s.App.Log.Secret(in.Invite)
		rec, u, err := auth.LookupLink(ctx, s.App, in.Invite)
		if err != nil || rec.Kind != auth.LinkInvite {
			writeErr(w, auth.ErrLinkGone)
			return
		}
		f.Purpose, f.Invite, f.Hint, f.Next = sso.ForInvite, rec.ID, u.Email, "/"
	}
	origin := auth.SSOLocalOrigin(s.App.Cfg)
	f.Host, f.Redirect = auth.SSOLocalHost(s.App.Cfg), origin+sso.CallbackPath
	if acc.public {
		rm := s.App.Cfg.Remote()
		origin, f.Host, f.Redirect = rm.PublicURL, rm.Host, rm.PublicURL+sso.CallbackPath
	}
	ticket, err := sso.FlowsIn(s.App.Cfg.StateDir()).NewTicket(f)
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't start signing you in. Try again."))
		return
	}
	s.App.Log.Secret(ticket)
	writeOK(w, SSOStartResult{URL: origin + sso.BeginPath + "?t=" + url.QueryEscape(ticket)})
}

// sameHost reports the Host a sign-in began at.
func (s *Server) sameHost(r *http.Request, f *sso.Flow) bool {
	if f.Public {
		return s.App.Cfg.Remote().MatchesHost(r.Host)
	}
	return strings.EqualFold(r.Host, f.Host)
}

// errElsewhere: an answer that came back at another address than its sign-in began at.
var errElsewhere = errors.New("elsewhere")

func (s *Server) ssoBegin(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("t")
	s.App.Log.Secret(ticket)
	acc := accessOf(r)
	f, state, err := sso.FlowsIn(s.App.Cfg.StateDir()).Begin(ticket)
	if err != nil {
		s.ssoPage(w, http.StatusGone, "This sign-in link doesn't work any more", "It was opened already, or more than two minutes went by. Start signing in again from MetaRush.", "/signin")
		return
	}
	s.App.Log.Secret(state)
	if acc.public != f.Public || !s.sameHost(r, f) {
		s.ssoPage(w, http.StatusForbidden, "Open this link where you started", "This sign-in began at another address of MetaRush. Start again there.", "")
		return
	}
	if acc.public && !acc.secure {
		s.ssoPage(w, http.StatusForbidden, "Sign in over a private connection", "Open MetaRush at its https:// address to sign in.", "")
		return
	}
	ctx := r.Context()
	use := auth.SetupFor(ctx, s.App, f.Setup)
	if use == nil {
		s.ssoPage(w, http.StatusConflict, "The sign-in changed", "An owner changed how people sign in while you started. Start again.", "/signin")
		return
	}
	domain := ""
	if use.Setup.Provider == sso.Google {
		domain = use.Setup.Domain
	}
	authURL, err := use.Client().AuthURL(ctx, sso.Request{Redirect: f.Redirect, State: state, Nonce: f.Nonce, Challenge: sso.Challenge(f.Verifier), Hint: f.Hint, Domain: domain})
	if err != nil {
		s.ssoProblemPage(w, err)
		return
	}
	setPending(w, acc, append([]string{state}, pendingStates(r, acc)...))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) ssoCallback(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.signInLimit.allow(sourceOf(r), time.Now()) {
		s.ssoPage(w, http.StatusTooManyRequests, "Too many sign-ins from this browser", "Wait a minute, then sign in again.", "")
		return
	}
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	s.App.Log.Secret(state)
	s.App.Log.Secret(code)
	acc := accessOf(r)
	mine, rest := takePending(pendingStates(r, acc), state)
	if state == "" || !mine {
		// Not this browser's sign-in (someone else's answer, a link sent to
		// you): nothing happens.
		s.ssoPage(w, http.StatusForbidden, "This sign-in wasn't started in this browser", "MetaRush finishes a sign-in only in the browser that started it. Start signing in again here.", "/signin")
		return
	}
	setPending(w, acc, rest)
	store := sso.FlowsIn(s.App.Cfg.StateDir())
	f, err := store.Finish(state, func(f *sso.Flow) error {
		if acc.public != f.Public || !s.sameHost(r, f) {
			return errElsewhere
		}
		return nil
	})
	switch {
	case errors.Is(err, errElsewhere):
		s.ssoPage(w, http.StatusForbidden, "Open this link where you started", "This sign-in began at another address of MetaRush. Start again there.", "")
		return
	case err != nil:
		words := "It was finished already."
		if errors.Is(err, sso.ErrExpired) {
			words = "It took more than ten minutes."
		}
		s.ssoPage(w, http.StatusGone, "This sign-in can't be finished", words+" Sign in again.", "/signin")
		return
	}
	ctx := r.Context()
	fail := func(reason string, err error) {
		s.ssoFailed(w, r, f, reason, err)
	}
	if e := q.Get("error"); e != "" {
		if e == "access_denied" {
			fail("cancelled", apperr.New(apperr.Canceled, "You didn't finish signing in at the provider, so nothing changed."))
		} else {
			fail("provider_error", apperr.New(apperr.Unauthorized, "The sign-in provider didn't sign you in.").WithDetails(map[string]any{"reason": "provider_error", "error": html.EscapeString(trim(e, 64))}))
		}
		return
	}
	use := auth.SetupFor(ctx, s.App, f.Setup)
	if use == nil {
		fail("setup_changed", apperr.New(apperr.Conflict, "An owner changed how people sign in while you were at the provider. Sign in again."))
		return
	}
	setup, client := use.Setup, use.Client()
	if iss := q.Get("iss"); iss != "" {
		// The provider names itself in its answer (RFC 9207): it must be the one asked.
		d, derr := client.Discover(ctx)
		if derr == nil && !strings.Contains(d.Issuer, "{tenantid}") && iss != d.Issuer {
			fail("bad_issuer", apperr.New(apperr.Unauthorized, "The answer came from another sign-in provider than the one MetaRush asked, so nobody was signed in."))
			return
		}
	}
	if code == "" || len(code) > 4096 {
		fail("provider_error", apperr.New(apperr.Unauthorized, "The sign-in provider's answer was incomplete. Sign in again."))
		return
	}
	id, err := client.Exchange(ctx, code, f.Verifier, f.Redirect, f.Nonce)
	if err != nil {
		reason := "bad_token"
		var p *sso.Problem
		if errors.As(err, &p) {
			reason = p.Reason
		}
		fail(reason, err)
		return
	}
	if f.Purpose == sso.ForTest {
		s.ssoTested(w, r, f, setup, id)
		return
	}
	if st := auth.Settings(ctx, s.App); st.Method != models.AuthSSO || st.SSO == nil || auth.LiveUse(s.App.Cfg, st.SSO).Fingerprint() != f.Setup {
		fail("setup_changed", apperr.New(apperr.Conflict, "An owner changed how people sign in while you were at the provider. Sign in again."))
		return
	}
	var invite *auth.LinkRecord
	if f.Purpose == sso.ForInvite {
		if invite = auth.LinkByHash(ctx, s.App, f.Invite); invite == nil {
			fail(auth.SSOInviteGone, apperr.New(apperr.NotFound, "This invite doesn't work any more: it was used, it ran out or it was taken back.").WithDetails(map[string]any{"reason": auth.SSOInviteGone}))
			return
		}
	}
	u, joined, err := auth.PersonForSSO(ctx, s.App, setup, id, invite)
	if err != nil {
		fail("", err)
		return
	}
	s.startSession(w, r, u, models.AuthSSO, setup.Provider)
	next := nextPath(f.Next)
	if joined && next == "/" {
		next = "/?welcome=1"
	}
	http.Redirect(w, r, next, http.StatusFound)
}

func trim(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ssoFailed tells a person why a sign-in didn't work: a test's result is
// recorded and shown; a sign-in goes back to the sign-in page with the
// reason (and the email the provider gave, the person's own).
func (s *Server) ssoFailed(w http.ResponseWriter, r *http.Request, f *sso.Flow, reason string, err error) {
	ae := apperr.From(err)
	details, _ := ae.Details.(map[string]any)
	if reason == "" {
		if d, ok := details["reason"].(string); ok {
			reason = d
		}
	}
	if f.Purpose == sso.ForTest {
		_ = sso.FlowsIn(s.App.Cfg.StateDir()).SaveTest(sso.TestResult{Setup: f.Setup, UserID: f.UserID, OK: false, Reason: reason, Message: strings.TrimSpace(ae.Message + " " + ae.Hint)})
		s.ssoPage(w, http.StatusOK, "The test sign-in didn't work", ae.Message+" "+ae.Hint, "")
		return
	}
	q := url.Values{"sso": {reason}}
	if e, ok := details["email"].(string); ok && e != "" {
		q.Set("as", e)
	}
	http.Redirect(w, r, "/signin?"+q.Encode(), http.StatusFound)
}

// ssoTested records a test sign-in that passed every check, and says who it
// was (the owner switches to the setup with it, D430).
func (s *Server) ssoTested(w http.ResponseWriter, r *http.Request, f *sso.Flow, setup *models.SSOSettings, id *sso.Identity) {
	ctx := r.Context()
	title := sso.Title(setup.Provider, setup.Label)
	who := id.Email
	if who == "" {
		who = firstNonEmpty(id.Name, "an account")
	}
	if other, _ := auth.ByKey(ctx, s.App, id.Key); other != nil && other.ID != f.UserID {
		msg := fmt.Sprintf("You signed in with %s as %s, but that account signs %s in already. Test again with your own account.", title, who, firstNonEmpty(other.Name, other.Email))
		_ = sso.FlowsIn(s.App.Cfg.StateDir()).SaveTest(sso.TestResult{Setup: f.Setup, UserID: f.UserID, OK: false, Reason: auth.SSOLinkedElsewhere, Message: msg, Identity: id})
		s.ssoPage(w, http.StatusOK, "The test sign-in didn't work", msg, "")
		return
	}
	msg := fmt.Sprintf("You signed in with %s as %s, and the answer passed every check. Go back to Settings → People (or the terminal) to switch to it: you will sign in this way from then on. You can close this tab.", title, who)
	if !id.EmailTrusted {
		msg += " " + title + " doesn't vouch for that account's email (" + id.Why + "), so people join with invite links rather than by their email alone."
	}
	if err := sso.FlowsIn(s.App.Cfg.StateDir()).SaveTest(sso.TestResult{Setup: f.Setup, UserID: f.UserID, OK: true, Message: msg, Identity: id}); err != nil {
		s.ssoPage(w, http.StatusInternalServerError, "MetaRush couldn't keep the test's result", "Try the test again.", "")
		return
	}
	s.appAs(person{UserID: f.UserID}, "dashboard").Audit(ctx, "auth.sso.tested", "workspace", s.App.WorkspaceID(), "", map[string]any{"provider": setup.Provider, "ok": true})
	s.ssoPage(w, http.StatusOK, "The test sign-in worked", msg, "")
}

func (s *Server) ssoProblemPage(w http.ResponseWriter, err error) {
	var p *sso.Problem
	if errors.As(err, &p) {
		s.ssoPage(w, http.StatusBadGateway, "MetaRush couldn't reach the sign-in provider", p.Msg+" "+p.Hint, "/signin")
		return
	}
	s.ssoPage(w, http.StatusBadGateway, "MetaRush couldn't reach the sign-in provider", apperr.From(err).Message, "/signin")
}

// ssoPage is a plain page for a sign-in step's outcome (the same look as the
// refusal page; no script).
func (s *Server) ssoPage(w http.ResponseWriter, status int, title, msg, back string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(status)
	link := ""
	if back != "" {
		link = `<p><a href="` + html.EscapeString(back) + `" data-sso-back>Sign in again</a></p>`
	}
	fmt.Fprintf(w, refusalPage, html.EscapeString(title), html.EscapeString(title), html.EscapeString(msg),
		html.EscapeString("MetaRush on "+s.App.Cfg.MachineName()+"."), link)
}
