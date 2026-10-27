package ops

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/dbmove"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/service"
	"github.com/metavert-io/metarush/internal/store"
)

// A move's state on this computer (D338): the new database's address kept
// for the move, each move's record (<state>/database-move/<id>.json, 0600)
// — what the dashboard's card and database.move.status show while the move
// runs, since the old database is frozen and no job can be recorded there —
// and the one-time grant a move started from the dashboard runs with
// (`metarush database move --dashboard-move <id>`, started with tools.Start,
// like the media's move, D112).

/* ---------- the new database's address ---------- */

// moveAddressName is where this computer keeps the new database's address
// for a move or a switch: its keychain, or (a home that keeps its keys in
// files) a private file beside the database's other secrets. Never a
// command line, config.yaml, a request body that is logged, or the log.
const moveAddressName = "database-move"

func moveAddressFile(home string) string { return filepath.Join(home, "database", "move.secret") }

// SaveMoveAddress keeps the new database's address on this computer for the
// move (or a switch). It is checked to be a MongoDB address first.
func SaveMoveAddress(cfg *config.Config, uri string) error {
	uri = strings.TrimSpace(uri)
	if err := checkAddress(uri); err != nil {
		return err
	}
	rememberAddress(uri)
	if cfg.SecretMode() == "env" {
		return localdb.WriteSecretFile(moveAddressFile(cfg.Home()), "the new database's address", uri, false)
	}
	if _, err := config.StoreSecret(cfg.Home(), moveAddressName, uri); err != nil {
		return apperr.New(apperr.NotConfigured, "This computer's keychain didn't take the new database's address.").
			WithDetails(map[string]any{"problem": scrubAddresses(err.Error())})
	}
	return nil
}

// LoadMoveAddress reads the address kept for the move ("" when none is).
func LoadMoveAddress(cfg *config.Config) (string, error) {
	var uri string
	if cfg.SecretMode() == "env" {
		if _, err := os.Stat(moveAddressFile(cfg.Home())); err != nil {
			return "", nil
		}
		v, err := localdb.ReadSecretFile(moveAddressFile(cfg.Home()), "the new database's address")
		if err != nil {
			return "", apperr.New(apperr.NotConfigured, "MetaRush can't read the new database's address it kept: %s.", strings.TrimSuffix(err.Error(), "."))
		}
		uri = v
	} else {
		v, err := config.ResolveSecret(cfg.Home(), config.SecretRef(moveAddressName))
		if err != nil {
			return "", nil
		}
		uri = v
	}
	rememberAddress(uri)
	return strings.TrimSpace(uri), nil
}

// forgetMoveAddress removes the copy kept for the move once this computer
// uses the address (it is kept where MetaRush keeps the database's address).
func forgetMoveAddress(cfg *config.Config) {
	if cfg.SecretMode() == "env" {
		_ = os.Remove(moveAddressFile(cfg.Home()))
		return
	}
	_ = config.DeleteSecret(cfg.Home(), moveAddressName)
}

// checkAddress refuses what isn't a MongoDB address (without repeating it).
func checkAddress(uri string) error {
	u, err := url.Parse(uri)
	if err != nil || (u.Scheme != "mongodb" && u.Scheme != "mongodb+srv") || u.Host == "" || strings.ContainsAny(uri, " \n\r\t") {
		return apperr.New(apperr.InvalidInput, "That isn't a MongoDB address: it starts with mongodb:// or mongodb+srv:// and names the server.").
			WithHint("Copy it from MongoDB Atlas (Connect → Drivers) or from whoever runs the server, with this computer's own database user in it.")
	}
	return nil
}

// rememberAddress keeps an address and its password out of everything the
// activity log writes (logbook redaction).
func rememberAddress(uri string) {
	if uri == "" {
		return
	}
	logbook.RememberSecret(uri)
	if u, err := url.Parse(uri); err == nil && u.User != nil {
		if pw, ok := u.User.Password(); ok {
			logbook.RememberSecret(pw, u.User.String())
		}
	}
}

/* ---------- switching this computer ---------- */

const uriVar = config.EnvPrefix + "MONGODB_URI"

// databaseOverrides are settings in this computer's environment that would
// keep MetaRush on another database than config.yaml names after a switch.
// want is the new address ("" in a plan: any value that would override).
func databaseOverrides(cfg *config.Config, want, name string) []string {
	var out []string
	if v := os.Getenv(uriVar); v != "" && v != want {
		if cfg.SecretMode() != "env" {
			out = append(out, uriVar)
		} else if file, ok := config.DotEnvValue(cfg.Home(), uriVar); !ok || file != v {
			out = append(out, uriVar) // from the shell, not the home's .env (which the switch rewrites)
		}
	}
	if v := os.Getenv(config.EnvPrefix + "DATABASE_NAME"); v != "" && (name == "" || v != name) {
		out = append(out, config.EnvPrefix+"DATABASE_NAME")
	}
	return out
}

// databaseSection is config.yaml's database section after a switch.
type databaseSection struct {
	URI   string                `yaml:"uri"`
	Name  string                `yaml:"name"`
	Local *config.LocalDatabase `yaml:"local,omitempty"`
}

// switchDatabase points this computer at the new database: its address
// where MetaRush keeps the database's (the keychain, or the home's .env), the
// previous one kept aside, and config.yaml's database section (the
// background MetaRush starts again with it). It checks the switch the way
// MetaRush loads its settings and puts everything back when it doesn't hold.
// It returns how to put it back.
func switchDatabase(ctx context.Context, a *app.App, uri, name string, local *config.LocalDatabase) (func(), error) {
	home := a.Cfg.Home()
	env := a.Cfg.SecretMode() == "env"
	if ov := databaseOverrides(a.Cfg, uri, name); len(ov) > 0 {
		return nil, apperr.New(apperr.Conflict, "Settings in this computer's environment (%s) override MetaRush's own, so this computer couldn't switch to the new database.", strings.Join(ov, ", ")).
			WithHint("Remove them from your shell or from the .env file in MetaRush's folder, then run it again: the copy stays as it is.")
	}
	oldNode, err := config.GetField(home, []string{"database"})
	if err != nil {
		return nil, err
	}
	oldURI := a.Cfg.Database.URI
	rememberAddress(oldURI)
	// The old address is kept aside, never thrown away.
	if env {
		if err := localdb.WriteSecretFile(filepath.Join(home, "database", "previous.secret"), "the earlier database address", oldURI, false); err != nil {
			return nil, apperr.New(apperr.NotConfigured, "MetaRush couldn't keep the old database's address aside, so this computer didn't switch.").WithDetails(map[string]any{"problem": scrubAddresses(err.Error())})
		}
	} else if _, err := config.StoreSecret(home, "mongodb-uri-previous", oldURI); err != nil {
		return nil, apperr.New(apperr.NotConfigured, "MetaRush couldn't keep the old database's address aside in the keychain, so this computer didn't switch.")
	}
	ref := config.SecretRef("mongodb-uri")
	if env {
		ref = "${" + uriVar + "}"
	}
	putBack := func() {
		if env {
			_ = config.SetDotEnvVar(home, uriVar, oldURI, false)
		} else {
			_, _ = config.StoreSecret(home, "mongodb-uri", oldURI)
		}
		if oldNode != nil {
			_ = config.SetField(home, []string{"database"}, oldNode)
		}
	}
	if env {
		err = config.SetDotEnvVar(home, uriVar, uri, false)
	} else {
		_, err = config.StoreSecret(home, "mongodb-uri", uri)
	}
	if err != nil {
		putBack()
		return nil, apperr.New(apperr.NotConfigured, "MetaRush couldn't keep the new database's address, so this computer didn't switch.").WithDetails(map[string]any{"problem": scrubAddresses(err.Error())})
	}
	if err := config.SetField(home, []string{"database"}, databaseSection{URI: ref, Name: name, Local: local}); err != nil {
		putBack()
		return nil, apperr.New(apperr.NotConfigured, "MetaRush couldn't change its settings, so this computer didn't switch.").WithDetails(map[string]any{"problem": err.Error()})
	}
	// Checked the way MetaRush loads its settings.
	cfg, err := config.Load(home)
	if err != nil || cfg.Database.URI != uri || cfg.Database.Name != name {
		putBack()
		return nil, apperr.New(apperr.NotConfigured, "With the new settings MetaRush wouldn't use the new database, so this computer kept the old one.").
			WithHint("Check that nothing in this computer's environment names another database, then run it again.")
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	db, err := store.Connect(cctx, cfg.Database.URI, cfg.Database.Name)
	if err == nil {
		_, err = db.ReadRestoreLock(cctx)
		_ = db.Close(context.Background())
	}
	if err != nil {
		putBack()
		return nil, apperr.New(apperr.Database, "With the new settings MetaRush couldn't open the new database, so this computer kept the old one.").
			WithDetails(map[string]any{"problem": scrubAddresses(err.Error())})
	}
	return putBack, nil
}

/* ---------- a move's record on this computer ---------- */

// Move run states.
const (
	DBMovePlanned  = "planned"
	DBMoveStarting = "starting"
	DBMoveRunning  = "running"
	DBMoveDone     = "done"
	DBMoveFailed   = "failed"
	// DBMoveStopped is only said: the process ended without saying how.
	DBMoveStopped = "stopped"
)

// DatabaseMoveRun is one move on this computer: where it goes, how far it is
// and how it went (and, for one started from the dashboard, its grant).
type DatabaseMoveRun struct {
	ID      string           `json:"id"`
	Target  DatabaseTargetIn `json:"target"`
	To      DatabasePlace    `json:"to"`
	Move    string           `json:"move,omitempty"`
	Planned time.Time        `json:"planned"`
	By      string           `json:"by,omitempty"`
	// The grant (the dashboard's): only the token's SHA-256 is kept.
	Granted   *time.Time `json:"granted,omitempty"`
	TokenHash string     `json:"tokenHash,omitempty"`
	PID       int        `json:"pid,omitempty"`
	Parent    int        `json:"parent,omitempty"`
	Started   *time.Time `json:"started,omitempty"`
	Finished  *time.Time `json:"finished,omitempty"`
	// State: planned → starting → running → done | failed.
	State string `json:"state"`
	// Phase: setting-up, pausing, copying, switching; Says what it does now.
	Phase    string          `json:"phase,omitempty"`
	Says     string          `json:"says,omitempty"`
	Progress dbmove.CopyProgress `json:"progress"`
	// Waiting names the computers it waits for to pause.
	Waiting []string `json:"waiting,omitempty"`
	Summary string   `json:"summary,omitempty"`
	Next    []string `json:"next,omitempty"`
	Error   string   `json:"error,omitempty"`
	Code    string   `json:"code,omitempty"`
}

func dbMoveDir(a *app.App) string { return filepath.Join(a.Cfg.StateDir(), "database-move") }

var dbMoveIDRe = regexp.MustCompile(`^[0-9a-f]{24}$`)

func dbMoveFile(a *app.App, id, ext string) (string, error) {
	if !dbMoveIDRe.MatchString(id) {
		return "", apperr.New(apperr.InvalidInput, "That move isn't one this computer planned.")
	}
	return filepath.Join(dbMoveDir(a), id+ext), nil
}

// DatabaseMoveLog is where a move started from the dashboard writes what it says.
func DatabaseMoveLog(a *app.App, id string) (string, error) { return dbMoveFile(a, id, ".log") }

func loadDBMove(a *app.App, id string) (*DatabaseMoveRun, error) {
	p, err := dbMoveFile(a, id, ".json")
	if err != nil {
		return nil, err
	}
	var r DatabaseMoveRun
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &r) != nil || r.ID != id {
		return nil, apperr.New(apperr.NotFound, "That move isn't one this computer planned.").WithHint("Check the new database again.")
	}
	return &r, nil
}

func saveDBMove(a *app.App, r *DatabaseMoveRun) error {
	p, err := dbMoveFile(a, r.ID, ".json")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dbMoveDir(a), 0o700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(r, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// dbMoves are this computer's moves, newest first.
func dbMoves(a *app.App) []*DatabaseMoveRun {
	entries, _ := os.ReadDir(dbMoveDir(a))
	var out []*DatabaseMoveRun
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !dbMoveIDRe.MatchString(id) {
			continue
		}
		if r, err := loadDBMove(a, id); err == nil {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return dbMoveTime(out[i]).After(dbMoveTime(out[j])) })
	return out
}

func dbMoveTime(r *DatabaseMoveRun) time.Time {
	switch {
	case r.Started != nil:
		return *r.Started
	case r.Granted != nil:
		return *r.Granted
	}
	return r.Planned
}

// dbMoveRun keeps a running move's record up to date (at most twice a
// second for its progress).
type dbMoveRun struct {
	mu    sync.Mutex
	r     *DatabaseMoveRun
	saved time.Time
}

type dbMoveRunKey struct{}

// withDBMoveRun hands a move started from the dashboard its record.
func withDBMoveRun(ctx context.Context, r *DatabaseMoveRun) context.Context {
	return context.WithValue(ctx, dbMoveRunKey{}, r)
}

// moveRecordFrom is the record of the move about to run: the dashboard's
// (claimed by this process), else a new one.
func moveRecordFrom(ctx context.Context, a *app.App, in DatabaseTargetIn) *dbMoveRun {
	if r, ok := ctx.Value(dbMoveRunKey{}).(*DatabaseMoveRun); ok && r != nil {
		return &dbMoveRun{r: r}
	}
	now := time.Now().UTC()
	r := &DatabaseMoveRun{ID: newMoveID(), Target: in, Planned: now, Started: &now, By: a.Actor.UserName, State: DBMoveRunning, PID: os.Getpid()}
	run := &dbMoveRun{r: r}
	run.save(a, true)
	return run
}

func (m *dbMoveRun) save(a *app.App, force bool) {
	if !force && time.Since(m.saved) < 500*time.Millisecond {
		return
	}
	m.saved = time.Now()
	_ = saveDBMove(a, m.r)
}

func (m *dbMoveRun) phase(a *app.App, phase, says string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.r.Phase, m.r.Says = phase, says
	m.save(a, true)
}

func (m *dbMoveRun) started(a *app.App, move string, to DatabasePlace) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.r.Move, m.r.To = move, to
	m.save(a, true)
}

func (m *dbMoveRun) progress(a *app.App, p dbmove.CopyProgress) {
	m.mu.Lock()
	defer m.mu.Unlock()
	newPhase := m.r.Progress.Phase != p.Phase
	m.r.Progress, m.r.Says = p, copyLabel(p)
	if p.Phase == "checking" {
		m.r.Phase = "checking"
	}
	m.save(a, newPhase)
}

func (m *dbMoveRun) waiting(a *app.App, names []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if strings.Join(m.r.Waiting, "\n") == strings.Join(names, "\n") {
		return
	}
	m.r.Waiting = names
	if len(names) > 0 {
		m.r.Says = "Waiting for " + joinAnd(names) + " to pause"
	}
	m.save(a, true)
}

func (m *dbMoveRun) finish(a *app.App, out *DatabaseMoveOut, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	m.r.Finished, m.r.TokenHash, m.r.Waiting = &now, "", nil
	switch {
	case err != nil:
		ae := apperr.From(err)
		m.r.State, m.r.Error, m.r.Code = DBMoveFailed, ae.Message, ae.Code
		m.r.Next = nil
		if ae.Hint != "" {
			m.r.Next = []string{ae.Hint}
		}
	default:
		m.r.State, m.r.Phase = DBMoveDone, ""
		if out != nil {
			m.r.Summary, m.r.Next, m.r.To = out.Summary, out.Next, out.To
		}
	}
	m.save(a, true)
}

// daemonRunsHere reports whether MetaRush runs in the background on this computer.
func daemonRunsHere(a *app.App) bool {
	_, ok := service.ReadDaemon(a.Cfg.Home())
	return ok
}

// moveRunsHere reports whether a move runs on this computer now (its
// process alive, or a dashboard grant not yet taken).
func moveRunsHere(a *app.App, move string) bool {
	for _, r := range dbMoves(a) {
		if move != "" && r.Move != "" && r.Move != move {
			continue
		}
		switch r.State {
		case DBMoveStarting:
			if r.Granted != nil && time.Since(*r.Granted) <= moveGrantFresh {
				return true
			}
		case DBMoveRunning:
			if r.PID != os.Getpid() && alive(r.PID) {
				return true
			}
		}
	}
	return false
}

/* ---------- moves started from the dashboard ---------- */

// DatabaseMoveForm is the new database as Settings → Storage describes it:
// the database on this computer, or another MongoDB with its address typed
// once (kept in this computer's keychain, never returned or logged).
type DatabaseMoveForm struct {
	To string `json:"to" jsonschema:"enum=here,enum=elsewhere"`
	// Address is sent once and never returned: kept on this computer.
	Address          string `json:"address,omitempty" jsonschema:"maxLength=2048,description=Sent once\\, never returned: kept in this computer's keychain."`
	Name             string `json:"name,omitempty" jsonschema:"maxLength=63"`
	OnlyThisComputer bool   `json:"onlyThisComputer,omitempty"`
}

// targetOf turns the form into the plan's input (keeping a typed address).
func targetOf(a *app.App, f DatabaseMoveForm) (DatabaseTargetIn, error) {
	in := DatabaseTargetIn{Name: strings.TrimSpace(f.Name), OnlyThisComputer: f.OnlyThisComputer}
	switch f.To {
	case "here":
		in.To = MoveToHere
	case "elsewhere":
		in.To = MoveToSaved
		if addr := strings.TrimSpace(f.Address); addr != "" {
			if err := SaveMoveAddress(a.Cfg, addr); err != nil {
				return in, err
			}
		}
	default:
		return in, invalid("Choose where the database goes: the database on this computer, or another MongoDB.")
	}
	return in, nil
}

// PlanDashboardDatabaseMove plans a move a person described in the dashboard;
// the record it returns is what they confirm next.
func PlanDashboardDatabaseMove(ctx context.Context, a *app.App, f DatabaseMoveForm) (*DatabaseMoveRun, *DatabasePlanOut, error) {
	pruneDBMoves(a)
	in, err := targetOf(a, f)
	if err != nil {
		return nil, nil, err
	}
	plan, err := CallTyped[DatabasePlanOut](ctx, a, "database.plan", DatabasePlanIn{DatabaseTargetIn: in}, Caller{Human: true})
	if err != nil {
		return nil, nil, err
	}
	r := &DatabaseMoveRun{ID: newMoveID(), Target: in, To: plan.To, Planned: time.Now().UTC(), By: a.Actor.UserName, State: DBMovePlanned}
	if err := saveDBMove(a, r); err != nil {
		return nil, nil, err
	}
	return r, plan, nil
}

// pruneDBMoves forgets plans nobody confirmed within a day.
func pruneDBMoves(a *app.App) {
	for _, r := range dbMoves(a) {
		if r.State == DBMovePlanned && time.Since(r.Planned) > 24*time.Hour {
			if p, err := dbMoveFile(a, r.ID, ".json"); err == nil {
				_ = os.Remove(p)
			}
		}
	}
}

// GrantDashboardDatabaseMove is the person's confirmation: a one-time token
// for the process that moves the database (two minutes, used once; only its
// SHA-256 is kept).
func GrantDashboardDatabaseMove(a *app.App, id string) (string, *DatabaseMoveRun, error) {
	r, err := loadDBMove(a, id)
	if err != nil {
		return "", nil, err
	}
	if r.State != DBMovePlanned {
		return "", nil, apperr.New(apperr.Conflict, "That move was started already.").WithHint("Check the new database again to plan another.")
	}
	if time.Since(r.Planned) > movePlanFresh {
		return "", nil, apperr.New(apperr.Conflict, "That plan is more than an hour old.").WithHint("Check the new database again: the plan says what moving does now.")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token := hex.EncodeToString(b)
	now := time.Now().UTC()
	r.Granted, r.TokenHash, r.State = &now, tokenHash(token), DBMoveStarting
	if err := saveDBMove(a, r); err != nil {
		return "", nil, err
	}
	return token, r, nil
}

// ClaimDashboardDatabaseMove is the moving process taking its grant; the
// context it returns hands the move its record.
func ClaimDashboardDatabaseMove(ctx context.Context, a *app.App, id, token string) (context.Context, *DatabaseMoveRun, error) {
	refused := apperr.New(apperr.HumanOnly, "That move wasn't confirmed by a person in the dashboard, or it was started already.")
	claim, err := dbMoveFile(a, id, ".claim")
	if err != nil {
		return ctx, nil, refused
	}
	f, err := os.OpenFile(claim, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ctx, nil, refused // used once
	}
	_ = f.Close()
	r, err := loadDBMove(a, id)
	if err != nil || r.State != DBMoveStarting || r.Granted == nil || time.Since(*r.Granted) > moveGrantFresh ||
		subtle.ConstantTimeCompare([]byte(tokenHash(strings.TrimSpace(token))), []byte(r.TokenHash)) != 1 {
		return ctx, nil, refused
	}
	now := time.Now().UTC()
	r.PID, r.Parent, r.Started, r.State, r.TokenHash = os.Getpid(), os.Getppid(), &now, DBMoveRunning, ""
	if err := saveDBMove(a, r); err != nil {
		return ctx, nil, err
	}
	return withDBMoveRun(ctx, r), r, nil
}

// FailDashboardDatabaseMove records a dashboard move that couldn't start.
func FailDashboardDatabaseMove(a *app.App, id string, err error) {
	r, lerr := loadDBMove(a, id)
	if lerr != nil {
		return
	}
	run := &dbMoveRun{r: r}
	run.finish(a, nil, err)
}

/* ---------- the status ---------- */

// DatabaseMoveStatusIn has no fields.
type DatabaseMoveStatusIn struct{}

// DatabaseMoveRunView is the last move started on this computer.
type DatabaseMoveRunView struct {
	State    string          `json:"state" jsonschema:"enum=starting,enum=running,enum=done,enum=failed,enum=stopped"`
	Phase    string          `json:"phase,omitempty"`
	To       string          `json:"to" jsonschema:"description=The new database\\, in words."`
	Says     string          `json:"says,omitempty"`
	Progress dbmove.CopyProgress `json:"progress"`
	Waiting  []string        `json:"waiting,omitempty"`
	Started  *time.Time      `json:"started,omitempty"`
	Finished *time.Time      `json:"finished,omitempty"`
	Summary  string          `json:"summary"`
	Next     []string        `json:"next,omitempty"`
}

// DatabaseMoveStatusOut is this computer's database and the workspace's move.
type DatabaseMoveStatusOut struct {
	This DatabasePlace `json:"this" jsonschema:"description=The database this computer uses."`
	// State is the workspace's database's: "" (no move), moving, moved or
	// abandoned (the last move was abandoned).
	State string `json:"state" jsonschema:"enum=,enum=moving,enum=moved,enum=abandoned"`
	// Frozen: changes wait (moving) or are refused for good (moved) here.
	Frozen bool `json:"frozen"`
	// To is where the workspace goes or went, in words; ToName the database's name.
	To     string `json:"to,omitempty"`
	ToName string `json:"toName,omitempty"`
	// By started it, on From.
	By        string     `json:"by,omitempty"`
	From      string     `json:"from,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	MovedAt   *time.Time `json:"movedAt,omitempty"`
	// Paused names the computers that paused for the move.
	Paused []string `json:"paused,omitempty"`
	// SwitchMeToo: the workspace moved and this computer still uses the old
	// database; CanSwitch: the new one is a database other computers reach.
	SwitchMeToo bool `json:"switchMeToo"`
	CanSwitch   bool `json:"canSwitch"`
	// Steps say what to do on this computer, in words; the switch's command
	// is in Command (Details).
	Steps   []string `json:"steps"`
	Command string   `json:"command,omitempty"`
	// Moving: a move runs on this computer now; Run is the last one started here.
	Moving  bool                 `json:"moving"`
	Run     *DatabaseMoveRunView `json:"run,omitempty"`
	Summary string               `json:"summary"`
}

// DatabaseMoveStatus is database.move.status.
func DatabaseMoveStatus(ctx context.Context, a *app.App, _ *DatabaseMoveStatusIn) (*DatabaseMoveStatusOut, error) {
	out := &DatabaseMoveStatusOut{This: databasePlaceOf(a.Cfg.Database), Steps: []string{}}
	m, err := a.DB.ReadMoveMarker(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't look into the workspace's database.")
	}
	a.SetMoveMarker(m)
	if m != nil {
		out.State, out.Frozen, out.To, out.ToName, out.By, out.From = m.State, m.Frozen(), m.To.Words, m.To.Name, m.By, m.FromName
		started := m.StartedAt
		out.StartedAt, out.MovedAt = &started, m.MovedAt
		out.Paused = pausedNames(ctx, a, m)
	}
	for _, r := range dbMoves(a) {
		if r.State == DBMovePlanned {
			continue
		}
		v := &DatabaseMoveRunView{State: r.State, Phase: r.Phase, To: r.To.Words, Says: r.Says, Progress: r.Progress, Waiting: r.Waiting,
			Started: r.Started, Finished: r.Finished, Summary: r.Summary, Next: r.Next}
		switch r.State {
		case DBMoveStarting:
			v.Summary = "Starting to move the database to " + r.To.Words + "."
			if r.Granted != nil && time.Since(*r.Granted) > moveGrantFresh {
				v.State, v.Summary = DBMoveStopped, "Moving the database to "+r.To.Words+" didn't start."
			}
		case DBMoveRunning:
			v.Summary = "Moving the database to " + r.To.Words + "."
			if !alive(r.PID) {
				v.State, v.Summary = DBMoveStopped, "Moving the database to "+r.To.Words+" stopped before it finished."
				v.Next = []string{"Move it again to continue where it stopped, or abandon it to let changes go on in the old database."}
			}
		case DBMoveFailed:
			v.Summary = r.Error
		}
		out.Run = v
		break
	}
	out.Moving = out.Run != nil && (out.Run.State == DBMoveRunning || out.Run.State == DBMoveStarting)
	switch {
	case m != nil && m.State == store.MoveMoved:
		out.SwitchMeToo = true
		out.CanSwitch = m.To.Kind != MoveToHere
		if out.CanSwitch {
			out.Command = "metarush database switch --to address --name " + m.To.Name
			out.Steps = append(out.Steps,
				"Get this computer its own sign-in for the new database: a database user of its own (in Atlas: Database Access → Add New Database User), never another computer's.",
				"Switch this computer with it: in Settings → Storage → Move the database, or in a terminal on this computer (Details shows the command, which reads the address from what you paste, never from the command line).",
				"MetaRush running in the background here starts again by itself with the new database.")
			out.Summary = fmt.Sprintf("The workspace moved to another database (%s): switch this computer too, with its own sign-in. Until then it refuses changes here.", m.To.Words)
		} else {
			out.Steps = append(out.Steps, fmt.Sprintf("The new database is on %s, which only that computer reaches: this computer can't use the workspace any more. To use it here, export it there and restore it here.", firstNonEmpty(m.To.Computer, "another computer")))
			out.Summary = fmt.Sprintf("The workspace moved to the database on %s, which only that computer reaches.", firstNonEmpty(m.To.Computer, "another computer"))
		}
	case m != nil && m.State == store.MoveMoving:
		out.Summary = fmt.Sprintf("The workspace is being moved to another database (%s); changes wait until it's done.", m.To.Words)
		if out.Moving && out.Run != nil && out.Run.Says != "" {
			out.Steps = append(out.Steps, out.Run.Says+".")
		} else if !out.Moving {
			out.Steps = append(out.Steps, fmt.Sprintf("%s started it on %s. When it stopped part-way, run it again there to continue it, or abandon it to let changes go on here.", firstNonEmpty(m.By, "A person"), firstNonEmpty(m.FromName, "another computer")))
		}
	default:
		out.Summary = "This computer keeps the workspace in " + out.This.Words + "."
	}
	return out, nil
}

// pausedNames names the computers that paused for a move.
func pausedNames(ctx context.Context, a *app.App, m *store.MoveMarker) []string {
	out := []string{}
	for _, c := range workspaceComputersByID(ctx, a) {
		if at, ok := m.Holding[c.id]; ok && !at.Before(m.StartedAt) {
			out = append(out, c.name)
		}
	}
	sort.Strings(out)
	return out
}

type computerName struct{ id, name string }

// installationsOf are the workspace's computers that aren't removed.
func installationsOf(ctx context.Context, a *app.App) []models.Installation {
	var insts []models.Installation
	_ = a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), bson.M{"state": bson.M{"$ne": models.InstallationRevoked}}, &insts)
	return insts
}

func workspaceComputersByID(ctx context.Context, a *app.App) []computerName {
	var out []computerName
	for _, in := range installationsOf(ctx, a) {
		out = append(out, computerName{id: in.ID, name: firstNonEmpty(in.Name, in.Hostname, in.ID)})
	}
	return out
}

// HoldForMove is what MetaRush running in the background does while a move
// keeps changes out of the workspace's database (internal/cli's daemon, held):
// it says it paused, in the marker only, so the move goes on.
func HoldForMove(ctx context.Context, a *app.App) (*store.MoveMarker, error) {
	m, err := a.DB.ReadMoveMarker(ctx)
	if err != nil {
		return nil, err
	}
	a.SetMoveMarker(m)
	if m != nil && m.State == store.MoveMoving {
		if err := a.DB.MoveHolding(ctx, m.Move, a.Cfg.Installation.ID); err != nil {
			return m, err
		}
	}
	return m, nil
}

// MovedWords is what a held computer's log says about the marker.
func MovedWords(m *store.MoveMarker) string {
	if m == nil {
		return ""
	}
	if m.State == store.MoveMoved {
		return fmt.Sprintf("the workspace moved to another database (%s): switch this computer too (Settings → Storage)", m.To.Words)
	}
	return fmt.Sprintf("the workspace is being moved to another database (%s): changes wait until it's done", m.To.Words)
}
