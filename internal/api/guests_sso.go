package api

import (
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/guests"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sso"
)

// Guests signing in with an organization's account (D496): the members'
// sign-in (internal/sso's strict checks, the app an owner set up and tested)
// on the guest listener's own steps — /guest/api/sso/start, then
// /guest/sso/begin, then /guest/sso/callback, all at the guest address
// (127.0.0.1, never localhost, as for the dashboard, D451). A guest sign-in
// is its own purpose (sso.ForGuest): only this callback finishes it, and the
// dashboard's refuses it. Its state rides a cookie of its own (mr_guest_sso;
// SameSite=Lax, which the provider's redirect back needs). Who it is: the
// guest whose email the provider vouches for (guests.ForSSO), never a member.

func (g *guestGate) ssoStart(w http.ResponseWriter, r *http.Request) {
	q := reqOf(r)
	q.what = "guest.signin.sso"
	if !g.signIn.allow(q.from, time.Now()) {
		q.reason = guests.ReasonThrottled
		guestError(w, http.StatusTooManyRequests, apperr.New(apperr.Unauthorized, "Too many sign-in tries from this browser. Wait a minute, then try again.").WithDetails(map[string]any{"reason": guests.ReasonThrottled}))
		return
	}
	var in struct{}
	if err := readBody(r, &in); err != nil {
		q.reason = "bad_request"
		guestErr(w, err)
		return
	}
	ctx := r.Context()
	if !guests.On(ctx, g.s.App) {
		q.reason = guests.ReasonGuestsOff
		guestError(w, http.StatusUnauthorized, apperr.New(apperr.Unauthorized, "Guests can't open MetaRush here right now: its owners turned guests off.").WithDetails(map[string]any{"reason": guests.ReasonGuestsOff}))
		return
	}
	st := auth.Settings(ctx, g.s.App)
	if st.Method != models.AuthSSO || st.SSO == nil {
		q.reason = guests.ReasonNoSSO
		guestError(w, http.StatusConflict, apperr.New(apperr.Conflict, "Guests don't sign in with an organization's account here: use the sign-in link you were sent.").WithDetails(map[string]any{"reason": guests.ReasonNoSSO}))
		return
	}
	f := sso.Flow{Purpose: sso.ForGuest, Setup: auth.LiveUse(g.s.App.Cfg, st.SSO).Fingerprint(), Host: g.host(), Redirect: g.origin() + sso.GuestCallbackPath, Next: "/"}
	ticket, err := sso.FlowsIn(g.s.App.Cfg.StateDir()).NewTicket(f)
	if err != nil {
		guestErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't start signing you in. Try again."))
		return
	}
	g.s.App.Log.Secret(ticket)
	q.what = "guest.page" // the sign-in itself is recorded when it finishes
	writeOK(w, GuestSSOStartOut{URL: g.origin() + sso.GuestBeginPath + "?t=" + url.QueryEscape(ticket)})
}

func (g *guestGate) ssoCookieName(secure bool) string {
	if secure {
		return "__Host-" + guestSSOCookie
	}
	return guestSSOCookie
}

func (g *guestGate) pending(r *http.Request) []string {
	c, err := r.Cookie(g.ssoCookieName(reqOf(r).secure))
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

func (g *guestGate) setPending(w http.ResponseWriter, secure bool, states []string) {
	if len(states) > ssoPending {
		states = states[:ssoPending]
	}
	c := &http.Cookie{Name: g.ssoCookieName(secure), Value: strings.Join(states, "."), Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: int(sso.FlowLife / time.Second)}
	if len(states) == 0 {
		c.Value, c.MaxAge = "", -1
	}
	http.SetCookie(w, c)
}

func (g *guestGate) ssoBegin(w http.ResponseWriter, r *http.Request) {
	q := reqOf(r)
	q.what = "guest.page"
	vals := r.URL.Query()
	if len(vals) != 1 || len(vals["t"]) != 1 {
		q.reason = "bad_request"
		g.ssoPage(w, http.StatusBadRequest, "This sign-in link isn't complete", "Start signing in again from your guest page.", true)
		return
	}
	ticket := vals.Get("t")
	f, state, err := sso.FlowsIn(g.s.App.Cfg.StateDir()).Begin(ticket)
	if err != nil {
		q.reason = guests.ReasonLinkGone
		g.ssoPage(w, http.StatusGone, "This sign-in link doesn't work any more", "It was opened already, or more than two minutes went by. Start signing in again from your guest page.", true)
		return
	}
	g.s.App.Log.Secret(ticket, state) // ours: a made-up ticket never is
	if f.Purpose != sso.ForGuest || !strings.EqualFold(r.Host, f.Host) {
		q.reason = guests.ReasonOther
		g.ssoPage(w, http.StatusForbidden, "Open this link where you started", "This sign-in began somewhere else. Start again from your guest page.", true)
		return
	}
	ctx := r.Context()
	use := auth.SetupFor(ctx, g.s.App, f.Setup)
	st := auth.Settings(ctx, g.s.App)
	if use == nil || st.Method != models.AuthSSO || st.SSO == nil || auth.LiveUse(g.s.App.Cfg, st.SSO).Fingerprint() != f.Setup {
		q.reason = "setup_changed"
		g.ssoPage(w, http.StatusConflict, "The sign-in changed", "How people sign in here changed while you started. Start again from your guest page.", true)
		return
	}
	domain := ""
	if use.Setup.Provider == sso.Google {
		domain = use.Setup.Domain
	}
	authURL, err := use.Client().AuthURL(ctx, sso.Request{Redirect: f.Redirect, State: state, Nonce: f.Nonce, Challenge: sso.Challenge(f.Verifier), Domain: domain})
	if err != nil {
		q.reason = "provider"
		g.ssoPage(w, http.StatusBadGateway, "MetaRush couldn't reach the sign-in provider", "Try again in a moment.", true)
		return
	}
	g.setPending(w, q.secure, append([]string{state}, g.pending(r)...))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, authURL, http.StatusFound)
}

// ssoCallback finishes a guest's sign-in with the provider's answer: only the
// parts it reads count (state, code, error, iss), the rest is ignored
// (bounded by the address's length), and a state or code is kept out of the
// log only once it belongs to a sign-in begun here (internal/sso keeps the
// code it exchanges).
func (g *guestGate) ssoCallback(w http.ResponseWriter, r *http.Request) {
	q := reqOf(r)
	q.what = "guest.signin.refused"
	w.Header().Set("Cache-Control", "no-store")
	if !g.signIn.allow(q.from, time.Now()) {
		q.reason = guests.ReasonThrottled
		g.ssoPage(w, http.StatusTooManyRequests, "Too many sign-ins from this browser", "Wait a minute, then sign in again.", false)
		return
	}
	vals := r.URL.Query()
	state, code := vals.Get("state"), vals.Get("code")
	mine, rest := takePending(g.pending(r), state)
	if state == "" || !mine {
		// Not this browser's sign-in (someone else's answer, a link sent to
		// you): nothing happens.
		q.reason = guests.ReasonCSRF
		g.ssoPage(w, http.StatusForbidden, "This sign-in wasn't started in this browser", "MetaRush finishes a sign-in only in the browser that started it. Start signing in again here.", true)
		return
	}
	g.setPending(w, q.secure, rest)
	f, err := sso.FlowsIn(g.s.App.Cfg.StateDir()).Finish(state, func(f *sso.Flow) error {
		if f.Purpose != sso.ForGuest || !strings.EqualFold(r.Host, f.Host) {
			return errElsewhere
		}
		return nil
	})
	switch {
	case errors.Is(err, errElsewhere):
		q.reason = guests.ReasonOther
		g.ssoPage(w, http.StatusForbidden, "Open this link where you started", "This sign-in began somewhere else. Start again from your guest page.", true)
		return
	case err != nil:
		q.reason = guests.ReasonLinkGone
		words := "It was finished already."
		if errors.Is(err, sso.ErrExpired) {
			words = "It took more than ten minutes."
		}
		g.ssoPage(w, http.StatusGone, "This sign-in can't be finished", words+" Sign in again.", true)
		return
	}
	g.s.App.Log.Secret(state)
	ctx := r.Context()
	if e := vals.Get("error"); e != "" {
		q.reason = "provider_error"
		if e == "access_denied" {
			g.ssoPage(w, http.StatusOK, "You didn't finish signing in", "Nothing changed. Sign in again when you're ready.", true)
		} else {
			g.ssoPage(w, http.StatusUnauthorized, "The sign-in provider didn't sign you in", "Sign in again, or use the sign-in link you were sent.", true)
		}
		return
	}
	st := auth.Settings(ctx, g.s.App)
	use := auth.SetupFor(ctx, g.s.App, f.Setup)
	if use == nil || st.Method != models.AuthSSO || st.SSO == nil || auth.LiveUse(g.s.App.Cfg, st.SSO).Fingerprint() != f.Setup {
		q.reason = "setup_changed"
		g.ssoPage(w, http.StatusConflict, "The sign-in changed", "How people sign in here changed while you were at the provider. Sign in again.", true)
		return
	}
	client := use.Client()
	if iss := vals.Get("iss"); iss != "" {
		if d, derr := client.Discover(ctx); derr == nil && !strings.Contains(d.Issuer, "{tenantid}") && iss != d.Issuer {
			q.reason = "bad_issuer"
			g.ssoPage(w, http.StatusUnauthorized, "The answer came from another provider", "Nobody was signed in. Sign in again.", true)
			return
		}
	}
	if code == "" || len(code) > 4096 {
		q.reason = "provider_error"
		g.ssoPage(w, http.StatusUnauthorized, "The sign-in provider's answer was incomplete", "Sign in again.", true)
		return
	}
	id, err := client.Exchange(ctx, code, f.Verifier, f.Redirect, f.Nonce)
	if err != nil {
		q.reason = "bad_token"
		var p *sso.Problem
		if errors.As(err, &p) {
			q.reason = p.Reason
		}
		g.ssoPage(w, http.StatusUnauthorized, "The sign-in didn't pass MetaRush's checks", "Nobody was signed in. Sign in again, or use the sign-in link you were sent.", true)
		return
	}
	gu, err := guests.ForSSO(ctx, g.s.App, use.Setup.Provider, id)
	if err != nil {
		ae := apperr.From(err)
		q.reason = reasonOf(err)
		g.ssoPage(w, http.StatusForbidden, "You can't open anything here with that account", strings.TrimSpace(ae.Message+" "+ae.Hint), true)
		return
	}
	q.what = "guest.signin.sso"
	if err := g.startGuestSession(w, r, &guests.Signed{Guest: *gu}, "sso", use.Setup.Provider); err != nil {
		g.ssoPage(w, http.StatusInternalServerError, "MetaRush couldn't sign you in", "Try again.", true)
		return
	}
	http.Redirect(w, r, "/", http.StatusFound)
}

// ssoPage is a plain page for a guest sign-in step's outcome (no script).
func (g *guestGate) ssoPage(w http.ResponseWriter, status int, title, msg string, back bool) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	link := ""
	if back {
		link = `<p><a href="/" data-guest-back>Back to your guest page</a></p>`
	}
	fmt.Fprintf(w, refusalPage, html.EscapeString(title), html.EscapeString(title), html.EscapeString(msg), html.EscapeString("MetaRush, for guests."), link)
}
