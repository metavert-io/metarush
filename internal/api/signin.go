package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sessions"
)

// Signing in with an email and password, and following invite and reset
// links (D183, docs/accounts.md). These endpoints work without a session; the
// Host, cross-site and CSRF-header checks are the same as everywhere.

// AuthState is GET /api/auth/state: how this MetaRush signs people in and
// whether this browser is signed in (the dashboard picks its first screen).
type AuthState struct {
	Method   string `json:"method" jsonschema:"enum=single,enum=password,enum=tailscale"`
	SignedIn bool   `json:"signedIn"`
	Name     string `json:"name,omitempty"`
	Here     bool   `json:"here" jsonschema:"description=This browser is on the computer MetaRush runs on."`
	Secure   bool   `json:"secure" jsonschema:"description=The connection is private enough to type a password (this computer, or HTTPS)."`
	Machine  string `json:"machine"`
}

// SignInRequest is POST /api/auth/login.
type SignInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// SignInResult is what a sign-in (or a followed link) answers.
type SignInResult struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
}

// LinkInfo is GET /api/auth/link/{token}: what an invite or reset link is for.
type LinkInfo struct {
	Kind      string    `json:"kind" jsonschema:"enum=invite,enum=reset"`
	Email     string    `json:"email"`
	Name      string    `json:"name,omitempty"`
	ExpiresAt time.Time `json:"expiresAt"`
	Machine   string    `json:"machine"`
}

// LinkAccept is POST /api/auth/link/{token}.
type LinkAccept struct {
	Name     string `json:"name,omitempty"`
	Password string `json:"password"`
}

func (s *Server) signInRoutes(r *mux.Router) {
	r.HandleFunc("/api/auth/state", s.authState).Methods("GET")
	r.HandleFunc("/api/auth/login", s.authLogin).Methods("POST")
	r.HandleFunc("/api/auth/logout", s.authLogout).Methods("POST")
	r.HandleFunc("/api/auth/link/{token}", s.authLinkInfo).Methods("GET")
	r.HandleFunc("/api/auth/link/{token}", s.authLinkAccept).Methods("POST")
}

func (s *Server) authState(w http.ResponseWriter, r *http.Request) {
	acc := accessOf(r)
	p, ok, _ := s.signedIn(w, r)
	writeOK(w, AuthState{Method: auth.Method(r.Context(), s.App), SignedIn: ok, Name: p.UserName, Here: !acc.public, Secure: !acc.public || acc.secure, Machine: s.App.Cfg.MachineName()})
}

// signInGuard is what every sign-in request passes: the dashboard's own
// header (no cross-site form can send it), the address limit, and a private
// connection for a password.
func (s *Server) signInGuard(w http.ResponseWriter, r *http.Request) bool {
	acc := accessOf(r)
	if r.Header.Get(clientHeader) != "dashboard" || !sameOriginOrDirect(r) {
		writeErr(w, apperr.New(apperr.Unauthorized, "MetaRush only takes sign-ins from its own dashboard.").WithDetails(map[string]any{"reason": "csrf"}))
		return false
	}
	if acc.public && !acc.secure {
		writeErr(w, apperr.New(apperr.Unauthorized, "Passwords are only typed over a private (HTTPS) connection.").
			WithHint("Open MetaRush at its https:// address.").WithDetails(map[string]any{"reason": "not_https"}))
		return false
	}
	if !s.signInLimit.allow(sourceOf(r), time.Now()) {
		writeErr(w, apperr.New(apperr.Unauthorized, "Too many sign-in tries from this browser. Wait a minute, then try again.").WithDetails(map[string]any{"reason": "throttled"}))
		return false
	}
	return true
}

// sourceOf is where a sign-in comes from, for the per-address limit: the
// tailnet address tailscale serve forwards, else the connection's.
func sourceOf(r *http.Request) string {
	if acc := accessOf(r); acc.from != "" {
		return acc.from
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return apperr.New(apperr.InvalidInput, "MetaRush couldn't read the request.")
	}
	return nil
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	if !s.signInGuard(w, r) {
		return
	}
	if auth.Method(r.Context(), s.App) != models.AuthPassword {
		writeErr(w, apperr.New(apperr.Conflict, "People don't sign in with a password here.").WithDetails(map[string]any{"reason": "no_passwords"}))
		return
	}
	var in SignInRequest
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	s.App.Log.Secret(in.Password) // never in the activity log
	u, err := auth.Authenticate(r.Context(), s.App, in.Email, in.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.startSession(w, r, u)
	writeOK(w, SignInResult{UserID: u.ID, Name: u.Name})
}

// startSession signs a browser in as a person with a new session (any session
// it had ends: no fixation) and records it.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u *models.User) {
	acc := accessOf(r)
	st := sessions.For(s.App.Cfg.StateDir())
	if c, err := r.Cookie(sessionCookie(acc)); err == nil && c.Value != "" {
		_ = st.Remove(c.Value)
	}
	val, err := st.New(sessions.Session{UserID: u.ID, UserName: u.Name, Public: acc.public, From: acc.from, Method: models.AuthPassword,
		ExpiresAt: time.Now().Add(auth.SessionMax).UTC()}, time.Now())
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't sign you in. Try again."))
		return
	}
	s.App.Log.Secret(val)
	s.setSession(w, acc, val)
	a := s.appAs(person{UserID: u.ID, UserName: u.Name}, "dashboard")
	a.Audit(r.Context(), "auth.signin", "user", u.ID, "", map[string]any{"remote": acc.public, "from": acc.from})
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(clientHeader) != "dashboard" {
		writeErr(w, apperr.New(apperr.Unauthorized, "MetaRush only takes changes from its own dashboard.").WithDetails(map[string]any{"reason": "csrf"}))
		return
	}
	acc := accessOf(r)
	if c, err := r.Cookie(sessionCookie(acc)); err == nil && c.Value != "" {
		_ = sessions.For(s.App.Cfg.StateDir()).Remove(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie(acc), Value: "", Path: "/", HttpOnly: true, Secure: acc.public && acc.secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	writeOK(w, map[string]bool{"signedOut": true})
}

func (s *Server) authLinkInfo(w http.ResponseWriter, r *http.Request) {
	token := mux.Vars(r)["token"]
	s.App.Log.Secret(token)
	rec, u, err := auth.LookupLink(r.Context(), s.App, token)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, LinkInfo{Kind: rec.Kind, Email: u.Email, Name: u.Name, ExpiresAt: rec.ExpiresAt, Machine: s.App.Cfg.MachineName()})
}

func (s *Server) authLinkAccept(w http.ResponseWriter, r *http.Request) {
	if !s.signInGuard(w, r) {
		return
	}
	token := mux.Vars(r)["token"]
	s.App.Log.Secret(token)
	var in LinkAccept
	if err := readJSON(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	s.App.Log.Secret(in.Password)
	u, err := auth.UseLink(r.Context(), s.App, token, in.Name, in.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	s.startSession(w, r, u)
	writeOK(w, SignInResult{UserID: u.ID, Name: u.Name})
}

// rateLimit allows a number of tries per source per minute (sign-ins and
// links); the per-account throttling is in internal/auth.
type rateLimit struct {
	mu    sync.Mutex
	per   int
	seen  map[string][]time.Time
	clean time.Time
}

func newRateLimit(perMinute int) *rateLimit {
	return &rateLimit{per: perMinute, seen: map[string][]time.Time{}}
}

func (l *rateLimit) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if now.Sub(l.clean) > 5*time.Minute {
		for k, ts := range l.seen {
			if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > time.Minute {
				delete(l.seen, k)
			}
		}
		l.clean = now
	}
	var keep []time.Time
	for _, t := range l.seen[key] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= l.per {
		l.seen[key] = keep
		return false
	}
	l.seen[key] = append(keep, now)
	return true
}
