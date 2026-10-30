// Package api is the local dashboard server: the React console, the ops endpoint,
// media streaming, resumable browser uploads and live events. It binds to
// 127.0.0.1 by default and never hands database or storage credentials to the
// browser — Go mediates every operation.
package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/recorder"
	"github.com/metavert-io/metarush/internal/sessions"
	"github.com/metavert-io/metarush/internal/version"
	"github.com/metavert-io/metarush/web"
)

const (
	cookieName   = "mr_session"
	clientHeader = "X-MetaRush-Client"
)

// Status is the local daemon status shown in the dashboard.
type Status struct {
	Version      string              `json:"version"`
	Installation string              `json:"installation"`
	Hostname     string              `json:"hostname"`
	Workspace    string              `json:"workspace"`
	User         string              `json:"user"`
	MediaStore   string              `json:"mediaStore"`
	MediaShared  bool                `json:"mediaShared"`
	WorkerTypes  []string            `json:"workerTypes"`
	WorkerOn     bool                `json:"workerOn"`
	Watch        *ingest.WatchStatus `json:"watch,omitempty"`
	Transcriber  string              `json:"transcriber,omitempty"`
	StartedAt    time.Time           `json:"startedAt"`
	DBOK         bool                `json:"dbOk"`
	DBError      string              `json:"dbError,omitempty"`
}

// Session is GET /api/session: who this dashboard acts as.
type Session struct {
	User         config.UserRef `json:"user"`
	Installation config.Ref     `json:"installation"`
	Workspace    config.Ref     `json:"workspace"`
	Version      string         `json:"version"`
	Access       Access         `json:"access"`
	// Avatar is the signed-in person's picture (D184; absent: their initials).
	Avatar string `json:"avatar,omitempty"`
	// Owner: the signed-in person may change this computer's settings (D191).
	// With "just me" sign-in, the person is the owner.
	Owner bool `json:"owner"`
	// Owners are the names of the people who may ("Owners: Theo Hartley.").
	Owners []string `json:"owners"`
}

// Access says where this browser is: on the computer that runs MetaRush, or
// on another one, through the public address (remote access, D128).
type Access struct {
	// Remote: the browser is on another computer, so actions on files and
	// windows happen on Machine, not in front of the person.
	Remote bool `json:"remote"`
	// Machine is the computer MetaRush runs on, as people call it ("Sam's MacBook").
	Machine string `json:"machine"`
	// SignedInWith is how this browser signed in.
	SignedInWith string `json:"signedInWith" jsonschema:"enum=link,enum=password,enum=tailscale"`
	// Login is the Tailscale account, when signed in with it.
	Login string `json:"login,omitempty"`
	// Secure: the page is HTTPS (or this computer's own address, which
	// browsers treat as secure): the microphone works.
	Secure bool `json:"secure"`
	// PublicURL is the address for other computers, when remote access is on.
	PublicURL string `json:"publicUrl,omitempty"`
}

// Server is the dashboard server.
type Server struct {
	App       *app.App
	Assistant *assistant.Manager
	Watch     *ingest.WatchManager
	WorkerFn  func() (types []string, on bool, transcriber string)
	Log       func(format string, a ...any)
	startedAt time.Time
	broker    *Broker
	uploads   *uploadAPI
	cfgFile   remoteFile
	// mediaDown: the latest media lookup found the database not answering (D180).
	mediaDown atomic.Bool
	// signInLimit caps sign-in and link tries per address (D183).
	signInLimit *rateLimit
	// held: a move keeps changes out of the workspace's database (D335);
	// this server reads only and starts no background work (NewHeld).
	held bool
	// remoteOff: this computer handed its role to another one (D346): its
	// dashboard answers only this computer's own browser, whatever
	// config.yaml says of remote access (RemoteOff).
	remoteOff atomic.Bool
}

// RemoteOff keeps remote access off for this server: a computer that handed
// its role over works as a client of the workspace, its dashboard for its own
// browser only (D346).
func (s *Server) RemoteOff() {
	s.remoteOff.Store(true)
	_ = s.App.Cfg.SetRemote(config.Dashboard{})
}

// NewHeld builds the server MetaRush runs while a move of the database keeps
// changes out of it (D335): the dashboard reads, nothing changes — no
// background work of its own (recovered uploads and recordings, the
// assistant's notes), and changes outside the operations are refused.
func NewHeld(a *app.App) *Server {
	s := &Server{App: a, startedAt: time.Now().UTC(), Log: func(string, ...any) {}, signInLimit: newRateLimit(20), held: true}
	s.broker = NewBroker(a)
	s.uploads = newUploads(s)
	exe, _ := os.Executable()
	s.Assistant = assistant.NewTerminal(a, exe)
	s.Assistant.OnChange = func(id string) { s.broker.Local(Event{Type: "assistant", ID: id}) }
	return s
}

// New builds a server.
func New(a *app.App) *Server {
	s := &Server{App: a, startedAt: time.Now().UTC(), Log: func(string, ...any) {}, signInLimit: newRateLimit(20)}
	s.broker = NewBroker(a)
	s.uploads = newUploads(s)
	exe, _ := os.Executable()
	s.Assistant = assistant.New(a, exe)
	s.Assistant.OnChange = func(id string) { s.broker.Local(Event{Type: "assistant", ID: id}) }
	// A reply that left plans to review or changes to OK (cards, questions)
	// notifies the person who wrote the message (its cards are theirs, D179),
	// whoever started the conversation. A reply to a dashboard update
	// notifies only when it leaves something for them: work Claude asked for
	// may finish long after they looked away (D142).
	s.Assistant.OnTurnEnd = func(c assistant.Conversation, t assistant.Turn) {
		if t.State != assistant.TurnSucceeded {
			return
		}
		asks := 0
		for _, act := range c.Actions {
			if act.Turn == t.N && act.State == assistant.ActionPending {
				asks++
			}
		}
		if t.Note && asks == 0 && t.Proposals == 0 {
			return
		}
		user := t.UserID // older messages are the person's who started the conversation
		if user == "" {
			user = c.UserID
		}
		if user == "" {
			user = a.Cfg.User.ID
		}
		notify.AssistantTurn(context.Background(), a, c.ID, t.N, c.ProjectID, c.ProjectName, t.Proposals, asks, user)
	}
	return s
}

// Broker exposes the event broker (for local events).
func (s *Server) Broker() *Broker { return s.broker }

// Listen serves until ctx ends.
func (s *Server) Listen(ctx context.Context) error {
	addr := net.JoinHostPort(s.App.Cfg.Dashboard.Host, strconv.Itoa(s.App.Cfg.Dashboard.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return apperr.Wrap(apperr.Internal, err, "The dashboard can't start: something on this machine is already using port %d. Is MetaRush already running?", s.App.Cfg.Dashboard.Port).
			WithHint("Use the MetaRush that is already running, or stop it first.").WithDetails(map[string]any{"address": addr}).WithCommand("metarush stop")
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 120 * time.Second}
	go s.broker.Run(ctx)
	if !s.held {
		s.uploads.recover()
		go s.uploads.sweep(ctx)
		asPerson := func(userID, userName string) *app.App {
			return s.appAs(person{UserID: userID, UserName: userName}, "dashboard")
		}
		go recorder.Recover(ctx, s.App, asPerson)
		go recorder.Sweep(ctx, s.App, asPerson) // pieces of recordings nobody finished don't stay forever
	}
	defer s.uploads.st.Flush()
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(c)
		s.Assistant.Close() // end Claude Code processes; their conversations resume later
	}()
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	r := mux.NewRouter()
	r.HandleFunc("/api/health", s.health).Methods("GET")
	r.HandleFunc("/auth", s.auth).Methods("GET")
	s.signInRoutes(r) // /api/auth/*: they work without a session (D183)
	api := r.PathPrefix("/api").Subrouter()
	api.Use(s.requireSession)
	api.HandleFunc("/session", s.session).Methods("GET")
	api.HandleFunc("/status", s.status).Methods("GET")
	api.HandleFunc("/ops", s.listOps).Methods("GET")
	api.HandleFunc("/ops/{name}", s.callOp).Methods("POST")
	api.HandleFunc("/events", s.broker.ServeHTTP).Methods("GET")
	api.HandleFunc("/uploads", s.uploads.list).Methods("GET")
	api.HandleFunc("/uploads", s.uploads.create).Methods("POST")
	api.HandleFunc("/uploads/{id}", s.uploads.head).Methods("HEAD")
	api.HandleFunc("/uploads/{id}", s.uploads.get).Methods("GET")
	api.HandleFunc("/uploads/{id}", s.uploads.patch).Methods("PATCH")
	api.HandleFunc("/uploads/{id}", s.uploads.cancel).Methods("DELETE")
	api.HandleFunc("/watch/choose", s.watchChoose).Methods("POST")
	api.HandleFunc("/watch/reveal", s.watchReveal).Methods("POST")
	api.HandleFunc("/export/timeline", s.exportTimeline).Methods("POST")
	api.HandleFunc("/export/reveal", s.exportReveal).Methods("POST")
	api.HandleFunc("/export/download", s.exportDownload).Methods("GET")
	api.HandleFunc("/recipes/export", s.recipeExport).Methods("GET") // D219: recipe files between workspaces
	api.HandleFunc("/recipes/import", s.recipeImport).Methods("POST")
	api.HandleFunc("/abilities/export", s.abilityExport).Methods("GET") // D292: ability files between workspaces
	api.HandleFunc("/abilities/import", s.abilityImport).Methods("POST")
	api.HandleFunc("/extension/download", s.extensionDownload).Methods("GET")
	api.HandleFunc("/resources/folders/choose", s.folderChoose).Methods("POST")
	api.HandleFunc("/resources/folders/set", s.folderSet).Methods("POST")
	api.HandleFunc("/storage/choose", s.storageChoose).Methods("POST")
	api.HandleFunc("/storage/plan", s.storagePlan).Methods("POST")
	api.HandleFunc("/storage/move", s.storageMove).Methods("POST")
	api.HandleFunc("/database/plan", s.databasePlan).Methods("POST") // D338: moving the database
	api.HandleFunc("/database/move", s.databaseMove).Methods("POST")
	api.HandleFunc("/database/switch", s.databaseSwitch).Methods("POST")
	api.HandleFunc("/providers/{name}/voices/{voice}/preview", s.voicePreview).Methods("GET")
	s.assistantRoutes(api)
	s.recordingRoutes(api) // screen recordings made in the dashboard (D258)
	s.extRoutes(r)         // the browser extension: its own origin check and upload-only token, never the session
	med := r.PathPrefix("/media").Subrouter()
	med.Use(s.requireSession)
	med.HandleFunc("/assets/{id}/snippet", s.assetSnippet).Methods("GET", "HEAD") // before {variant}
	med.HandleFunc("/assets/{id}/frame", s.assetFrame).Methods("GET", "HEAD")     // before {variant}
	med.HandleFunc("/assets/{id}/{variant}", s.assetMedia).Methods("GET", "HEAD")
	med.HandleFunc("/renders/{id}", s.renderMedia).Methods("GET", "HEAD")
	med.HandleFunc("/avatars/{id}", s.avatarMedia).Methods("GET", "HEAD")
	med.HandleFunc("/renders/{id}/thumbnail", s.renderThumb).Methods("GET", "HEAD")
	med.HandleFunc("/motion/{hash}/{kind}", s.motionMedia).Methods("GET", "HEAD")
	med.HandleFunc("/remotion/{hash}/{kind}/{n}", s.remotionMedia).Methods("GET", "HEAD")
	med.HandleFunc("/remotion/{hash}/{kind}/{n}/{side}", s.remotionMedia).Methods("GET", "HEAD")
	med.HandleFunc("/resources/file/{sha}", s.resourceFileByHash).Methods("GET", "HEAD") // before {id}/{kind}
	med.HandleFunc("/resources/{id}/{kind}", s.resourceMedia).Methods("GET", "HEAD")
	r.PathPrefix("/").HandlerFunc(s.static)
	return s.hostGuard(securityHeaders(s.heldGuard(r)))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, ok, err := s.signedIn(w, r)
		if err != nil {
			writeErr(w, err)
			return
		}
		if !ok {
			// "not signed in" is what the dashboard looks for (web/src/api/client.ts);
			// "method" says which sign-in page to show (D183).
			method := auth.Method(r.Context(), s.App)
			public := accessOf(r).public
			switch {
			case public && method == models.AuthSingle:
				writeErr(w, apperr.New(apperr.Unauthorized, "This browser is not signed in to MetaRush on %s, and sign-in isn't set up there.", s.App.Cfg.MachineName()).
					WithHint("Ask the owner to choose how people sign in, in Settings → People, and to invite you.").
					WithDetails(map[string]any{"reason": "no_session", "remote": true, "method": method}))
			case method == models.AuthPassword:
				writeErr(w, apperr.New(apperr.Unauthorized, "This browser is not signed in to MetaRush.").
					WithHint("Sign in with your email and password.").WithDetails(map[string]any{"reason": "no_session", "remote": public, "method": method}))
			case public:
				writeErr(w, apperr.New(apperr.Unauthorized, "This browser is not signed in to MetaRush on %s.", s.App.Cfg.MachineName()).
					WithHint("Sign in to Tailscale with the account the owner let in, then open this page again.").
					WithDetails(map[string]any{"reason": "no_session", "remote": true, "method": method}))
			default:
				writeErr(w, apperr.New(apperr.Unauthorized, "This browser is not signed in to MetaRush.").
					WithHint("Open the dashboard again from MetaRush (it gives a one-time sign-in link).").
					WithDetails(map[string]any{"reason": "no_session", "method": method}).WithCommand("metarush dashboard"))
			}
			return
		}
		if cl := r.Header.Get(clientHeader); r.Method != "GET" && r.Method != "HEAD" && cl != "dashboard" && cl != "cli" {
			// Cross-site forms cannot set custom headers: CSRF defense. ("cli" is
			// the local CLI forwarding to this daemon, e.g. `assistant ask`.)
			writeErr(w, apperr.New(apperr.Unauthorized, "MetaRush only takes changes from its own dashboard.").WithDetails(map[string]any{"reason": "csrf", "missingHeader": clientHeader}))
			return
		}
		ctx := context.WithValue(r.Context(), personKey{}, who)
		// Owner-only ops and handlers ask whether this person is an owner (D191):
		// a one-time link's session is the owner at this computer (like its CLI).
		uid := s.appAs(who, "").Actor.UserID
		link := who.Via == "link" || who.Via == "" // as /api/session says
		ctx = ops.OverHTTP(ctx, func() bool { return link || auth.SignedInOwner(ctx, s.App, uid) })
		r = r.WithContext(ctx)
		if strings.HasSuffix(r.URL.Path, "/events") {
			r = s.whileSignedIn(r, who) // live streams end soon after a sign-in stops counting
		}
		next.ServeHTTP(w, r)
	})
}

// MintLaunchNonce writes a one-time sign-in nonce (used by `metarush dashboard`).
func MintLaunchNonce(stateDir string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	n := hex.EncodeToString(b)
	dir := filepath.Join(stateDir, "nonces")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, n), []byte(time.Now().UTC().Format(time.RFC3339)), 0o600); err != nil {
		return "", err
	}
	return n, nil
}

func (s *Server) auth(w http.ResponseWriter, r *http.Request) {
	n := r.URL.Query().Get("t")
	if len(n) != 48 || strings.ContainsAny(n, "./\\") {
		http.Error(w, "This sign-in link isn't complete. Open the dashboard again from MetaRush to get a new one.", http.StatusBadRequest)
		return
	}
	p := filepath.Join(s.App.Cfg.StateDir(), "nonces", n)
	b, err := os.ReadFile(p)
	_ = os.Remove(p) // one-time
	if err != nil {
		http.Error(w, "This sign-in link was already used or has expired. Open the dashboard again from MetaRush to get a new one.", http.StatusUnauthorized)
		return
	}
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err != nil || time.Since(t) > 10*time.Minute {
		http.Error(w, "This sign-in link has expired. Open the dashboard again from MetaRush to get a new one.", http.StatusUnauthorized)
		return
	}
	s.App.Log.Secret(n) // the used nonce is never logged
	acc := accessOf(r)
	method := auth.Method(r.Context(), s.App)
	switch {
	case acc.public && acc.login != "" && s.App.Cfg.Remote().IdentitySignIn() && method == models.AuthTailscale:
		// A listed Tailscale account: the link signs that person in, the same
		// way opening the address does.
		if _, err := s.signInTailscale(r.Context(), w, acc); err != nil {
			http.Error(w, apperr.From(err).Message, http.StatusServiceUnavailable)
			return
		}
	case acc.public:
		// A one-time link signs in the owner on this computer only (D183): it
		// never lets another computer in as them.
		http.Error(w, "One-time sign-in links work only on the computer MetaRush runs on. Sign in with your own account instead.", http.StatusForbidden)
		return
	default:
		u := s.App.Cfg.User
		// Made at the database server's time, like a password session: the
		// owner's "sign out everywhere" is compared with it (D360).
		val, err := sessions.For(s.App.Cfg.StateDir()).New(sessions.Session{UserID: u.ID, UserName: u.Name, Public: false, From: acc.from, Method: "link",
			CreatedAt: auth.SessionStart(r.Context(), s.App, nil)}, time.Now())
		if err != nil {
			http.Error(w, "MetaRush couldn't sign this browser in. Try the link again.", http.StatusInternalServerError)
			return
		}
		s.App.Log.Secret(val) // the new session cookie is never logged
		s.setSession(w, acc, val)
	}
	next := r.URL.Query().Get("next")
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		next = "/"
	}
	http.Redirect(w, r, next, http.StatusFound)
}

// Health is GET /api/health: the build that answers (the same fields as
// `metarush version --json`, D130), so deploy scripts can check what runs.
type Health struct {
	OK bool `json:"ok"`
	version.Info
	Installation string `json:"installation"`
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, Health{OK: true, Info: version.Current(), Installation: s.App.Cfg.Installation.Name})
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	p, _ := personOf(r)
	acc := accessOf(r)
	rm := s.App.Cfg.Remote()
	via := p.Via
	if via == "" {
		via = "link"
	}
	// The person's record is who they are now (a name or email changed in
	// Settings); the config's copy and the session's are from setup or sign-in.
	var avatar string
	user := config.UserRef{ID: p.UserID, Name: p.UserName, Email: p.Email}
	if u, err := auth.GetUser(r.Context(), s.App, p.UserID); err == nil {
		avatar = auth.AvatarURL(u)
		if u.Name != "" {
			user.Name = u.Name
		}
		if u.Email != "" {
			user.Email = u.Email
		}
	}
	owners := auth.OwnerNames(r.Context(), s.App)
	writeOK(w, Session{Avatar: avatar, User: user, Owner: ops.IsOwnerCaller(r.Context()), Owners: owners, Installation: s.App.Cfg.Installation, Workspace: s.App.Cfg.Workspace,
		Version: version.Version, Access: Access{Remote: acc.public, Machine: s.App.Cfg.MachineName(), SignedInWith: via, Login: p.Login,
			Secure: !acc.public || acc.secure, PublicURL: rm.PublicURL}})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	p, _ := personOf(r)
	st := Status{Version: version.Version, Installation: s.App.Cfg.Installation.Name, Hostname: app.Hostname(), Workspace: s.App.Cfg.Workspace.Name,
		User: p.UserName, MediaStore: s.App.Media.Locator(), MediaShared: s.App.Media.Capabilities().Shared, StartedAt: s.startedAt, WorkerTypes: []string{}}
	if s.WorkerFn != nil {
		st.WorkerTypes, st.WorkerOn, st.Transcriber = s.WorkerFn()
	}
	if s.Watch != nil {
		if ws, ok := s.Watch.Snapshot(); ok {
			if ws.Pending == nil {
				ws.Pending = []string{}
			}
			if ws.Errors == nil {
				ws.Errors = []string{}
			}
			st.Watch = &ws
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	if err := s.App.DB.Client.Ping(ctx, nil); err != nil {
		st.DBError = err.Error()
	} else {
		st.DBOK = true
	}
	writeOK(w, st)
}

// OpInfo describes an op for the dashboard.
type OpInfo struct {
	Name      string `json:"name"`
	Summary   string `json:"summary"`
	Mutating  bool   `json:"mutating"`
	HumanOnly bool   `json:"humanOnly"`
	LocalOnly bool   `json:"localOnly"`
}

func (s *Server) listOps(w http.ResponseWriter, r *http.Request) {
	var out []OpInfo
	for _, op := range ops.All() {
		out = append(out, OpInfo{op.Name, op.Summary, op.Mutating, op.HumanOnly, op.LocalOnly})
	}
	writeOK(w, out)
}

func (s *Server) callOp(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	limit := int64(ops.DefaultMaxInput)
	if op, ok := ops.Get(name); ok {
		limit = op.InputLimit()
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't read the request."))
		return
	}
	if int64(len(body)) > limit {
		writeErr(w, apperr.New(apperr.InvalidInput, "That request is too large for MetaRush (over %d MB).", limit>>20))
		return
	}
	via := "dashboard"
	if r.Header.Get(clientHeader) == "cli" {
		via = "cli" // the local CLI forwarding a live-process op to this daemon
	}
	a := s.appFor(r, via)
	// Whether the person is an owner is asked only by owner-only ops (OverHTTP, D191).
	out, err := ops.Call(r.Context(), a, name, body, ops.Caller{Human: true, Remote: true, Elsewhere: accessOf(r).public})
	if err == nil && name == "account.password" {
		// Signed out everywhere else; this browser carries on with a new sign-in.
		if who, ok := personOf(r); ok && auth.Method(r.Context(), s.App) == models.AuthPassword {
			if u, uerr := auth.GetUser(r.Context(), s.App, who.UserID); uerr == nil {
				s.startSession(w, r, u)
			}
		}
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, out)
}

func (s *Server) static(w http.ResponseWriter, r *http.Request) {
	if !web.Built() {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!doctype html><meta charset="utf-8"><title>MetaRush</title><body style="font-family:system-ui;background:#0b0b0e;color:#eee;padding:3rem"><h1>MetaRush</h1><p>This copy of MetaRush was built without its dashboard. If you build MetaRush yourself, run <code>npm run build -w web</code> and build it again, or use the Vite dev server (./start.sh).</p></body>`)
		return
	}
	dist := web.Dist()
	p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	if st, err := fs.Stat(dist, p); err != nil || st.IsDir() {
		p = "index.html" // SPA route
	}
	if strings.HasPrefix(p, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeFileFS(w, r, dist, p)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOK(w http.ResponseWriter, v any) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": v})
}

func writeErr(w http.ResponseWriter, err error) {
	ae := apperr.From(err)
	writeJSON(w, ae.HTTPStatus(), map[string]any{"ok": false, "error": ae})
}
