package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/dbmove"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/version"
)

// Moving the database (D334–D338, docs/database-move.md): `metarush database
// plan|move`, designed like the media's move (storage.migrate). The plan says
// what moves, how long it takes, what every computer needs and what waits
// meanwhile, and checks the new database (it answers, holds nothing, takes
// writes) and the clients' What may leave rules. The move:
//
//  1. freezes the workspace: a "being moved" marker in the old database
//     (store.CDatabaseMove); every computer's operations refuse changes
//     (E_MOVING) and MetaRush running in the background pauses (the daemon
//     starts again holding: reading only, no background work) and says so in
//     the marker; the move waits until every computer running MetaRush paused
//     and no work is running;
//  2. copies everything with the workspace export's engine (internal/dbmove
//     on internal/wsarchive): the new database locked first, every
//     collection checked by hash on both sides, the migration level last;
//  3. switches this computer (its address kept in the keychain, or the home's
//     .env when it keeps its keys there);
//  4. marks the old database "moved" (where in words, never a sign-in): other
//     computers keep refusing changes there and say how to switch, each with
//     its own sign-in (database.switch);
//  5. never deletes the old database.
//
// A stopped move is run again to continue it, or abandoned (the freeze is
// lifted; the copy keeps its lock, so nothing uses it). Owners, and people
// only: never Auto, Claude, an AI app or a review-desk draft.

func init() {
	Def("database.plan", "Check what moving the workspace's database would do — to the database on this computer, or to another MongoDB (Atlas or your own server) whose address this computer keeps for the move: what moves, how long it takes, what each computer needs, what waits meanwhile, and whether the new database is empty, reachable and writable; nothing is changed (owners)",
		Flags{OwnerOnly: true}, DatabasePlan)
	Def("database.move", "Move the workspace's database to another one: changes pause on every computer, everything is copied and checked, this computer switches, and the old database is kept, marked as moved, never deleted (owners, people only; plan first)",
		Flags{Mutating: true, HumanOnly: true, OwnerOnly: true, LocalOnly: true}, DatabaseMove)
	Def("database.switch", "Switch this computer to the database the workspace moved to, with this computer's own sign-in for it (owners, people only, at this computer)",
		Flags{Mutating: true, HumanOnly: true, OwnerOnly: true, LocalOnly: true}, DatabaseSwitch)
	Def("database.abandon", "Abandon a move of the database that stopped: changes go on in the old database, and the copy keeps its lock so nothing uses it (owners, people only)",
		Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, DatabaseAbandon)
	Def("database.move.status", "Say which database this computer uses and whether the workspace is being moved to another one or moved (then switch this computer too), and how the last move started on this computer is going",
		Flags{}, DatabaseMoveStatus)
}

// Where the database goes.
const (
	MoveToHere  = "here"  // the database on this computer (item 8's, set up like setup does)
	MoveToSaved = "saved" // another MongoDB whose address this computer keeps for the move
)

// DatabaseTargetIn names the new database: never its address, which this
// computer keeps (SaveMoveAddress) and no command line, request or log holds.
type DatabaseTargetIn struct {
	To string `json:"to" jsonschema:"enum=here,enum=saved,description=here: the database on this computer (set up the way setup does it); saved: another MongoDB (Atlas or your own server) whose address this computer keeps for the move (given once to the plan in a terminal or in Settings → Storage; never on a command line)"`
	// Name is the new database's name (default metarush).
	Name string `json:"name,omitempty" jsonschema:"maxLength=63,description=The new database's name (default metarush)."`
	// OnlyThisComputer: moving to the database on this computer while other
	// computers use the workspace, which couldn't reach it.
	OnlyThisComputer bool `json:"onlyThisComputer,omitempty" jsonschema:"description=The database on this computer is reached by this computer only: say this computer will be the only one using the workspace."`
}

// DatabasePlanIn plans a move.
type DatabasePlanIn struct {
	DatabaseTargetIn
}

// DatabaseMoveIn moves the database.
type DatabaseMoveIn struct {
	DatabaseTargetIn
}

// DatabasePlace is a database in words, never with a sign-in.
type DatabasePlace struct {
	Kind  string `json:"kind" jsonschema:"enum=here,enum=atlas,enum=server"`
	Words string `json:"words" jsonschema:"description=The database in words: the database on this computer\\, MongoDB Atlas\\, a MongoDB server on your network."`
	// Host is where it is (hosts and ports only); Name the database's name.
	Host string `json:"host,omitempty"`
	Name string `json:"name"`
	// Reach: this-computer (only this computer reaches it) or every-computer.
	Reach string `json:"reach" jsonschema:"enum=this-computer,enum=every-computer"`
	// Cloud: a place elsewhere on the internet (What may leave counts it as
	// cloud storage).
	Cloud bool `json:"cloud"`
}

// DatabaseComputer is one computer of the workspace in a plan.
type DatabaseComputer struct {
	Name string `json:"name"`
	// This is the computer the move runs on.
	This bool `json:"this"`
	// Running: MetaRush ran there in the last few minutes (it pauses by itself).
	Running bool   `json:"running"`
	Version string `json:"version,omitempty"`
	// Older: it runs a MetaRush that doesn't know about moving the database
	// (older than the first that does, or one that hasn't said which it
	// runs, or invited and not joined yet): started after the move, it would
	// go on changing the old database, so the move waits until it's updated
	// or removed from the workspace.
	Older bool `json:"older"`
	// Needs says what it needs to use the new database.
	Needs string `json:"needs"`

	why string // the problem it is for the move, in plain words ("" for none)
}

// FirstMoveVersion is the first MetaRush release that knows about moving the
// database (D334). A computer whose record says an older one, or none, could
// go on changing the old database after a move: the plan names it, and the
// move waits until it's updated or removed. A build that knows also says so
// in its record (app.MovesMark), which covers builds from source of an older
// release number.
const FirstMoveVersion = "0.15.0"

// olderBuildLogged is the version of a MetaRush that doesn't know about
// moves which wrote to the activity log from a computer after the build its
// record names did ("" for none). An older MetaRush used only from a command
// line (or an AI app's connection) never rewrites the record, but logs what
// it does with its version. Entries are ordered by their own time, the
// computer's own clock for both; a computer whose record's build never wrote
// an entry tells nothing here.
func olderBuildLogged(ctx context.Context, a *app.App, in models.Installation) string {
	var newest struct {
		At time.Time `bson:"at"`
	}
	opts := options.FindOne().SetSort(bson.D{{Key: "at", Value: -1}}).SetProjection(bson.M{"at": 1})
	if err := a.DB.C(store.CLogEntries).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "installationId": in.ID, "version": in.Version}, opts).Decode(&newest); err != nil {
		return ""
	}
	var after []struct {
		Version string `bson:"version"`
	}
	cur, err := a.DB.C(store.CLogEntries).Find(ctx, bson.M{"workspaceId": a.WorkspaceID(), "installationId": in.ID, "at": bson.M{"$gt": newest.At},
		"version": bson.M{"$nin": bson.A{in.Version, ""}}}, options.Find().SetSort(bson.D{{Key: "at", Value: -1}}).SetLimit(200).SetProjection(bson.M{"version": 1}))
	if err != nil {
		return ""
	}
	if err := cur.All(ctx, &after); err != nil {
		return ""
	}
	for _, e := range after {
		if n, ok := version.Compare(e.Version, FirstMoveVersion); !ok || n < 0 {
			return e.Version
		}
	}
	return ""
}

// knowsMoves says whether a computer's record shows a MetaRush that knows
// about moving the database, and if not, why in plain words.
func knowsMoves(in models.Installation, name string) (bool, string) {
	revoke := fmt.Sprintf("metarush machine revoke %q", name)
	switch {
	case in.State == models.InstallationInvited:
		return false, fmt.Sprintf("%s was invited but hasn't joined yet, and could join with a MetaRush that doesn't know about moving the database. Take the invitation back first (%s) and invite it again after the move, or let it join with this MetaRush now.", name, revoke)
	case in.KnowsMoves != "" && in.KnowsMoves == app.MovesMark(in.Version, in.Commit):
		return true, ""
	case in.Version == "":
		return false, fmt.Sprintf("MetaRush can't tell which MetaRush %s runs. One that doesn't know about moving the database would go on changing the old database when it starts. Update MetaRush there and open it once (it then says which it runs), or remove that computer from the workspace (%s), first.", name, revoke)
	}
	if n, ok := version.Compare(in.Version, FirstMoveVersion); ok && n >= 0 {
		return true, ""
	}
	return false, fmt.Sprintf("%s runs MetaRush %s, which doesn't know about moving the database: started after the move, even if it's off now, it would go on changing the old database. Update MetaRush there and open it once, or remove that computer from the workspace (%s), first.", name, in.Version, revoke)
}

// DatabaseCount is one headline count of what moves ("120 clips").
type DatabaseCount struct {
	What  string `json:"what"`
	Count int64  `json:"count"`
}

// DatabasePlanOut is what a move would do.
type DatabasePlanOut struct {
	From DatabasePlace `json:"from"`
	To   DatabasePlace `json:"to"`
	// Nothing: the workspace uses that database already.
	Nothing bool `json:"nothing"`
	// Resume: a move to this database stopped part-way; moving continues it.
	Resume bool `json:"resume"`
	// Collections are every kind of record the old database holds, with
	// counts and sizes, and whether it moves.
	Collections []dbmove.MoveCollection `json:"collections"`
	Documents   int64                   `json:"documents"`
	Bytes       int64                   `json:"bytes"`
	// Counts are the headline counts ("4 projects, 120 clips…").
	Counts []DatabaseCount `json:"counts"`
	// Seconds is about how long it takes; Takes says it ("about 3 minutes").
	Seconds int    `json:"seconds"`
	Takes   string `json:"takes"`
	// Computers are the workspace's computers and what each needs.
	Computers []DatabaseComputer `json:"computers"`
	// Frozen says what waits during the move.
	Frozen []string `json:"frozen"`
	// Target is the new database checked (reached, empty, writable).
	Target dbmove.TargetCheck `json:"target"`
	// KeptHere are clients' What may leave rules that keep their things
	// from the new database.
	KeptHere []string `json:"keptHere"`
	// Problems stop the move; Ready: nothing does.
	Problems []string `json:"problems"`
	Ready    bool     `json:"ready"`
	Notes    []string `json:"notes"`
	// Kept says the old database is kept and how a person removes it later.
	Kept    string `json:"kept"`
	Summary string `json:"summary"`
}

// DatabaseMoveOut is what a move did.
type DatabaseMoveOut struct {
	From        DatabasePlace           `json:"from"`
	To          DatabasePlace           `json:"to"`
	Collections []dbmove.MoveCollection `json:"collections"`
	Documents   int64                   `json:"documents"`
	Resumed     bool                    `json:"resumed"`
	Verified    bool                    `json:"verified"`
	// Switched: this computer uses the new database now; Marked: the old one
	// says the workspace moved.
	Switched bool `json:"switched"`
	Marked   bool `json:"marked"`
	// Restarted: MetaRush running in the background here started again with
	// the new database.
	Restarted bool `json:"restarted"`
	// Notes say what else to know (indexes of the old database MetaRush
	// doesn't make).
	Notes []string `json:"notes"`
	// Others are the other computers, each to switch with its own sign-in.
	Others  []string `json:"others"`
	Seconds float64  `json:"seconds"`
	Kept    string   `json:"kept"`
	Next    []string `json:"next"`
	// Late is the looks at the old database afterwards, for anything written
	// there late (the first one was made right after the switch).
	Late    *LateLookView `json:"late,omitempty"`
	Summary string        `json:"summary"`
}

// databasePeopleOnly: a move, a switch and abandoning one are a person's,
// every time (on top of HumanOnly): never Auto, Claude, an AI app or the
// review desk.
func databasePeopleOnly(a *app.App) error {
	if a.Actor.Via == models.ViaAssistant || a.Actor.Via == models.ViaAssistantAuto || a.Actor.Agent != "" || a.ProposeOnly || a.Actor.Via == "mcp" {
		return apperr.New(apperr.HumanOnly, "Only a person moves the workspace's database: Claude, Auto and AI apps never do.").
			WithDetails(map[string]any{"reason": "people_only"})
	}
	return nil
}

// dbNameRe is a database name MongoDB takes on every system.
var dbNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,63}$`)

// databasePlaceOf names a database in words.
func databasePlaceOf(d config.Database) DatabasePlace {
	p := DatabasePlace{Name: d.Name, Reach: "every-computer"}
	if d.Local != nil {
		p.Kind, p.Words, p.Host, p.Reach = MoveToHere, "the database on this computer", fmt.Sprintf("127.0.0.1:%d", d.Local.Port), "this-computer"
		return p
	}
	u, err := url.Parse(strings.TrimSpace(d.URI))
	if err != nil || u.Host == "" {
		p.Kind, p.Words = "server", "a MongoDB server"
		return p
	}
	p.Host = u.Host
	hosts := strings.Split(u.Host, ",")
	first := hosts[0]
	if h, _, err := net.SplitHostPort(first); err == nil {
		first = h
	}
	first = strings.ToLower(first)
	p.Cloud = !nearbyHost(first) || u.Scheme == "mongodb+srv"
	switch {
	case strings.HasSuffix(first, ".mongodb.net"):
		p.Kind, p.Words = "atlas", "MongoDB Atlas"
	case first == "127.0.0.1" || first == "localhost" || first == "::1":
		p.Kind, p.Words = "server", "a MongoDB server on this computer"
	case nearbyHost(first):
		p.Kind, p.Words = "server", "a MongoDB server on your network"
	default:
		p.Kind, p.Words = "server", "a MongoDB server at "+first
	}
	return p
}

// nearbyHost reports a host on this computer or a private network.
func nearbyHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

// markerTarget is a place as the old database's marker keeps it.
func (p DatabasePlace) markerTarget(computer string) store.MoveTarget {
	t := store.MoveTarget{Kind: p.Kind, Words: p.Words, Host: p.Host, Name: p.Name, Reach: p.Reach}
	if p.Kind == MoveToHere {
		t.Words, t.Computer = "the database on "+computer, computer
	}
	return t
}

// dbTarget is the new database, opened.
type dbTarget struct {
	in    DatabaseTargetIn
	place DatabasePlace
	name  string
	// uri is its address with the sign-in: never shown, logged or written
	// anywhere but this computer's keychain (or its .env).
	uri string
	db  *store.DB
	// local is the database on this computer (To here); ready says it is set
	// up and signs in.
	local *localdb.Server
	ready bool
	// fingerprint names the place for continuing a move: its kind, hosts and
	// name, never the sign-in.
	fingerprint string
}

func (t *dbTarget) close() {
	if t != nil && t.db != nil {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = t.db.Close(c)
		cancel()
	}
}

// openTarget reads where the new database is and connects to it. A plan
// (setUp false) sets nothing up: the database on this computer is only
// looked at.
func openTarget(ctx context.Context, a *app.App, in DatabaseTargetIn, setUp bool) (*dbTarget, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "metarush"
	}
	if !dbNameRe.MatchString(name) || name == "admin" || name == "local" || name == "config" {
		return nil, invalid("The new database's name has letters, digits, - and _ only (up to 63), and isn't admin, local or config.")
	}
	t := &dbTarget{in: in, name: name}
	switch strings.TrimSpace(in.To) {
	case MoveToHere:
		return t, openHere(ctx, a, t, setUp)
	case MoveToSaved, "":
	default:
		return nil, invalid("Say where the database goes: here (the database on this computer) or saved (another database whose address this computer keeps).")
	}
	uri, err := LoadMoveAddress(a.Cfg)
	if err != nil {
		return nil, err
	}
	if uri == "" {
		return nil, apperr.New(apperr.NotConfigured, "This computer keeps no address for the new database.").
			WithHint("Give it once to the plan in a terminal (it is read from standard input, never typed on the command line), or in Settings → Storage → Move the database.").
			WithCommand("metarush database plan --to address --name <name>")
	}
	t.uri = uri
	t.place = databasePlaceOf(config.Database{URI: uri, Name: name})
	if strings.HasPrefix(name, store.TestDBPrefix) && !store.IsLocalURI(uri) {
		return nil, invalid("A test database's name is only for a database on this computer.")
	}
	t.fingerprint = fingerprintOf(t.place.Kind, hostsOf(uri), name)
	return t, nil
}

// connect signs in to the new database (once): false when it doesn't answer
// or doesn't take the sign-in (a plan says so plainly; a move refuses).
func (t *dbTarget) connect(ctx context.Context) bool {
	if t.db != nil {
		return true
	}
	if t.uri == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	db, err := store.Connect(cctx, t.uri, t.name)
	if err != nil {
		return false
	}
	t.db = db
	return true
}

// openHere is the database on this computer: looked at for a plan, set up
// (downloaded, started, its sign-ins made) for a move — the way setup does it,
// resuming every step.
func openHere(ctx context.Context, a *app.App, t *dbTarget, setUp bool) error {
	home := a.Cfg.Home()
	srv := localdb.New(home, 0, a.Tools)
	st, have := srv.ReadState()
	if have {
		srv.Port = st.Port
	} else {
		p, err := localdb.PickPort(home)
		if err != nil {
			return apperr.New(apperr.NotConfigured, "MetaRush can't set up a database on this computer: %s.", strings.TrimSuffix(err.Error(), "."))
		}
		srv.Port = p
	}
	t.local = srv
	t.place = DatabasePlace{Kind: MoveToHere, Words: "the database on this computer", Host: fmt.Sprintf("127.0.0.1:%d", srv.Port), Name: t.name, Reach: "this-computer"}
	t.fingerprint = fingerprintOf(MoveToHere, a.Cfg.Installation.ID, t.name)
	saved := localdb.Saved{}
	if uri, _ := LoadMoveAddress(a.Cfg); srv.Ours(uri, localdb.User) {
		saved.URI = uri
	}
	if admin, _ := localAdminAddress(a.Cfg); srv.Ours(admin, localdb.AdminUser) {
		saved.AdminURI = admin
	}
	if !setUp {
		if have && st.Initiated && saved.URI != "" {
			sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if srv.Answers(sctx) {
				if db, err := store.Connect(sctx, saved.URI, t.name); err == nil {
					t.db, t.uri, t.ready = db, saved.URI, true
				}
			}
		}
		return nil
	}
	if !srv.Available() {
		return apperr.New(apperr.NotConfigured, "MongoDB makes no server for this computer, so MetaRush can't keep the database here.")
	}
	uri := saved.URI
	keep := func(user, u string) error {
		if user == localdb.AdminUser {
			return keepLocalAdmin(a.Cfg, u)
		}
		uri = u
		return SaveMoveAddress(a.Cfg, u)
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	if _, err := srv.Prepare(dctx, t.name, saved, nil, keep); err != nil {
		return apperr.New(apperr.Database, "The database on this computer couldn't be set up: %s.", strings.TrimSuffix(err.Error(), ".")).
			WithHint("Nothing was moved and changes go on in the old database. Run the move again: setting it up carries on where it stopped.").
			WithCommand("metarush database status --details")
	}
	if uri == "" {
		return apperr.New(apperr.Database, "MetaRush set up the database on this computer but kept no sign-in for it.").WithHint("Run the move again.")
	}
	t.uri = uri
	cctx, cancel2 := context.WithTimeout(ctx, 20*time.Second)
	defer cancel2()
	db, err := store.Connect(cctx, uri, t.name)
	if err != nil {
		return apperr.New(apperr.Database, "MetaRush couldn't sign in to the database it set up on this computer.").WithHint("Run the move again.")
	}
	t.db, t.ready = db, true
	return nil
}

// keepLocalAdmin keeps the administrator's address of the database on this
// computer where setup keeps it (upgrades only).
func keepLocalAdmin(cfg *config.Config, uri string) error {
	logbook.RememberSecret(uri)
	if cfg.SecretMode() == "env" {
		return localdb.WriteSecretFile(localdb.AdminSecretFile(cfg.Home()), "the database's administrator address", uri, false)
	}
	_, err := config.StoreSecret(cfg.Home(), localdb.AdminSecret, uri)
	return err
}

func localAdminAddress(cfg *config.Config) (string, error) {
	if cfg.SecretMode() == "env" {
		return localdb.ReadSecretFile(localdb.AdminSecretFile(cfg.Home()), "the database's administrator address")
	}
	return config.ResolveSecret(cfg.Home(), config.SecretRef(localdb.AdminSecret))
}

func fingerprintOf(parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:12])
}

// hostsOf is a database address's hosts, sorted and lowercased (never its sign-in).
func hostsOf(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	hs := strings.Split(strings.ToLower(u.Host), ",")
	sort.Strings(hs)
	return u.Scheme + "://" + strings.Join(hs, ",")
}

// sameDatabase reports whether the new database is the one this computer uses.
func sameDatabase(a *app.App, t *dbTarget) bool {
	if t.in.To == MoveToHere {
		return a.Cfg.Database.Local != nil && a.Cfg.Database.Name == t.name
	}
	return hostsOf(a.Cfg.Database.URI) == hostsOf(t.uri) && a.Cfg.Database.Name == t.name
}

/* ---------- the plan ---------- */

// DatabasePlan is database.plan.
func DatabasePlan(ctx context.Context, a *app.App, in *DatabasePlanIn) (*DatabasePlanOut, error) {
	t, err := openTarget(ctx, a, in.DatabaseTargetIn, false)
	if err != nil {
		return nil, err
	}
	defer t.close()
	out, err := planMove(ctx, a, t)
	if err != nil || out.Ready || out.Nothing {
		return out, err
	}
	if forgetUnneededAddress(ctx, a) {
		out.Notes = append(out.Notes, ForgotAddressWords)
	}
	return out, nil
}

// RunningWithin is how recently MetaRush must have run on a computer for a
// move to wait for it to pause (it says so every minute).
var RunningWithin = 130 * time.Second

func planMove(ctx context.Context, a *app.App, t *dbTarget) (*DatabasePlanOut, error) {
	out := &DatabasePlanOut{From: databasePlaceOf(a.Cfg.Database), To: t.place, Collections: []dbmove.MoveCollection{}, Counts: []DatabaseCount{},
		Computers: []DatabaseComputer{}, Frozen: []string{}, KeptHere: []string{}, Problems: []string{}, Notes: []string{}}
	out.Kept = keptWords(out.From)
	if sameDatabase(a, t) {
		out.Nothing = true
		out.Summary = "The workspace already uses " + t.place.Words + ". There is nothing to move."
		return out, nil
	}
	marker, err := a.DB.ReadMoveMarker(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't look into the workspace's database.")
	}
	switch {
	case marker != nil && marker.State == store.MoveMoved:
		return nil, movedError(marker)
	case marker != nil && marker.State == store.MoveMoving && marker.To.Name == t.name && markerMatches(marker, t, a):
		out.Resume = true
	case marker != nil && marker.State == store.MoveMoving:
		out.Problems = append(out.Problems, fmt.Sprintf("A move to %s started on %s and waits to be continued or abandoned.", marker.To.Words, firstNonEmpty(marker.FromName, "another computer")))
	}
	survey, err := dbmove.SurveySource(ctx, a.DB, a.WorkspaceID())
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read what the workspace's database holds.")
	}
	out.Collections, out.Documents, out.Bytes = survey.Collections, survey.Documents, survey.Bytes
	out.Problems = append(out.Problems, survey.Problems...)
	out.Counts = headlineCounts(survey.Collections)
	// Clients' What may leave rules (D337), before anything else is asked of
	// the new database: one elsewhere on the internet is cloud storage for
	// their words.
	kept := false
	if LeaveDatabase != nil {
		if err := LeaveDatabase(ctx, a, config.Database{URI: t.uri, Name: t.name, Local: localOf(t)}, true); err != nil {
			e := apperr.From(err)
			if e.Code != apperr.KeptHere {
				return nil, err
			}
			kept = true
			out.KeptHere = append(out.KeptHere, e.Message)
			out.Problems = append(out.Problems, e.Message)
		}
	}
	// The new database.
	switch {
	case kept:
		out.Target = dbmove.TargetCheck{Problem: "Not looked at: a client's rules keep their words, voices or faces from it."}
	case t.local != nil && !t.ready:
		out.Target = dbmove.TargetCheck{Empty: true, Writable: true}
		if !t.local.Available() {
			out.Problems = append(out.Problems, "MongoDB makes no server for this computer, so MetaRush can't keep the database here.")
		} else if _, have := t.local.ReadState(); !have {
			out.Notes = append(out.Notes, fmt.Sprintf("MetaRush first sets up the database on this computer: it downloads MongoDB from MongoDB's own server (about %d MB) and keeps it in MetaRush's folder, the way setup does.", t.local.DownloadMB()))
			if note := t.local.KernelNote(); note != "" {
				out.Notes = append(out.Notes, note) // MongoDB 7.0 on a kernel MongoDB 8 refuses (D339)
			}
		} else if why := t.local.CantStart(); why != "" {
			out.Problems = append(out.Problems, why)
		}
		if w := t.local.PlaceWarning(); w != "" {
			out.Notes = append(out.Notes, w)
		}
	case !t.connect(ctx):
		out.Target = dbmove.TargetCheck{Problem: "MetaRush couldn't reach the new database, or it didn't take the sign-in."}
		out.Problems = append(out.Problems, out.Target.Problem+" Check the address (and that this computer may reach it), then check again.")
	default:
		move := ""
		if out.Resume {
			move = marker.Move
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		out.Target = dbmove.CheckTarget(cctx, t.db, move)
		cancel()
		if out.Target.Problem != "" {
			out.Problems = append(out.Problems, out.Target.Problem)
		}
		if out.Target.Resume || out.Target.Copied {
			out.Resume = true
		}
	}
	// The workspace's computers.
	others := 0
	for _, c := range workspaceComputers(ctx, a) {
		if !c.This {
			others++
		}
		out.Computers = append(out.Computers, c)
	}
	for i := range out.Computers {
		c := &out.Computers[i]
		switch {
		case c.This:
			c.Needs = "Nothing: it switches by itself once the copy is checked."
		case t.place.Reach == "this-computer":
			c.Needs = "It couldn't reach the new database, which only this computer reaches."
		default:
			c.Needs = "Its own sign-in for the new database (a database user of its own; sign-ins are never copied between computers), then a switch on that computer."
		}
		if c.why != "" {
			out.Problems = append(out.Problems, c.why)
		}
	}
	if t.place.Reach == "this-computer" && others > 0 && !t.in.OnlyThisComputer {
		names := []string{}
		for _, c := range out.Computers {
			if !c.This {
				names = append(names, c.Name)
			}
		}
		out.Problems = append(out.Problems, fmt.Sprintf("Your other computers (%s) couldn't reach the database on this computer, so they couldn't use the workspace any more. Move it here only if this computer will be the only one: say so, and check again.", joinAnd(names)))
	}
	if who := mediaMoveRunning(ctx, a); who != "" {
		out.Problems = append(out.Problems, fmt.Sprintf("The media is being moved on %s: let that finish first, then move the database.", who))
	}
	if ov := databaseOverrides(a.Cfg, t.uri, t.name); len(ov) > 0 {
		out.Problems = append(out.Problems, fmt.Sprintf("Settings in this computer's environment (%s) override MetaRush's own, so this computer couldn't switch to the new database. Remove them from your shell or from the .env file in MetaRush's folder first.", strings.Join(ov, ", ")))
	}
	if t.place.Cloud && !kept {
		out.Notes = append(out.Notes, "The new database is elsewhere on the internet, which clients' What may leave rules count as cloud storage: it holds the workspace's transcripts, names and notes, and the voice and face prints MetaRush knows people by.")
	}
	if !lateServerHashesWork(ctx, a.DB) {
		out.Notes = append(out.Notes, lateReadsAllNote())
	}
	if t.place.Kind == "server" && t.place.Words == "a MongoDB server on this computer" && others > 0 {
		out.Notes = append(out.Notes, "The new database's address is on this computer: your other computers reach it only if the same address works on theirs (a tunnel, say).")
	}
	// How long it takes, and what waits meanwhile.
	out.Seconds = estimateSeconds(out.Bytes, len(out.Collections), t)
	out.Takes = takesWords(out.Seconds)
	out.Frozen = []string{
		"While it moves (" + out.Takes + "), changes wait on every computer: editing, adding footage, renders, Claude's changes and every other background step. Reading the projects and playing footage keep working.",
		"MetaRush on each computer pauses by itself; work that is running stops first and starts again afterwards.",
		"The old database is never changed again: once the copy is checked, every computer still using it keeps refusing changes there until it switches.",
	}
	if out.Resume {
		out.Notes = append(out.Notes, "A move to this database stopped part-way: moving continues it where it stopped, and checks everything again.")
	}
	if survey.Level.Older {
		out.Notes = append(out.Notes, "The workspace's database lacks some of this MetaRush's updates: they are made in the new database when MetaRush first opens it.")
	}
	out.Ready = len(out.Problems) == 0
	switch {
	case out.Ready && out.Resume:
		out.Summary = fmt.Sprintf("Moving would go on copying the workspace's database, %s (%s), to %s, check it and switch this computer.", countWords(out.Documents, "record"), humanBytes(out.Bytes), t.place.Words)
	case out.Ready:
		out.Summary = fmt.Sprintf("Moving would copy the workspace's database, %s (%s), to %s in %s, check it and switch this computer.", countWords(out.Documents, "record"), humanBytes(out.Bytes), t.place.Words, out.Takes)
	default:
		out.Summary = "The database can't be moved to " + t.place.Words + " yet: " + lowerFirst(strings.TrimSuffix(out.Problems[0], "."))
		if len(out.Problems) > 1 {
			out.Summary += fmt.Sprintf(" (and %s more)", plural(len(out.Problems)-1, "thing"))
		}
		out.Summary += "."
	}
	return out, nil
}

func localOf(t *dbTarget) *config.LocalDatabase {
	if t.local == nil {
		return nil
	}
	return &config.LocalDatabase{Port: t.local.Port}
}

// markerMatches reports whether a marker is a move to this very place.
func markerMatches(m *store.MoveMarker, t *dbTarget, a *app.App) bool {
	if m.From != a.Cfg.Installation.ID || m.To.Name != t.name {
		return false
	}
	if t.in.To == MoveToHere {
		return m.To.Kind == MoveToHere
	}
	return m.To.Kind != MoveToHere && m.To.Host == t.place.Host
}

// keptWords says the old database is kept, and how a person removes it.
func keptWords(from DatabasePlace) string {
	where := "where it is"
	switch from.Kind {
	case MoveToHere:
		where = "in MetaRush's folder on this computer (the database folder)"
	case "atlas":
		where = "in MongoDB Atlas"
	}
	return "The old database is kept " + where + ", marked as moved, and nothing is ever deleted. Once every computer uses the new one and everything looks right, you can remove the old database yourself " +
		"(in Atlas, your server's own tools, or by removing the database folder): MetaRush never does."
}

// headlineCounts picks what people count in a workspace.
func headlineCounts(cs []dbmove.MoveCollection) []DatabaseCount {
	words := []struct{ coll, what string }{
		{store.CProjects, "projects"}, {store.CAssets, "clips"}, {store.CRevisions, "versions"}, {store.CUsers, "people"},
		{store.CComments, "comments"}, {store.CSegments, "transcript lines"}, {store.CLogEntries, "activity log entries"},
	}
	by := map[string]int64{}
	for _, c := range cs {
		by[c.Name] = c.Count
	}
	out := []DatabaseCount{}
	for _, w := range words {
		if n := by[w.coll]; n > 0 {
			out = append(out, DatabaseCount{What: w.what, Count: n})
		}
	}
	return out
}

// estimateSeconds is about how long a move takes: the pause, the copy, and
// reading both sides again to check it.
func estimateSeconds(bytes int64, collections int, t *dbTarget) int {
	copyRate, readRate := 4.0*(1<<20), 10.0*(1<<20) // another computer, over the network
	if t.local != nil || t.place.Kind == "server" && t.place.Words == "a MongoDB server on this computer" {
		copyRate, readRate = 25.0*(1<<20), 60.0*(1<<20)
	}
	s := 20.0 + float64(bytes)/copyRate + 2*float64(bytes)/readRate + 0.3*float64(collections)
	if t.local != nil {
		if _, have := t.local.ReadState(); !have {
			s += float64(t.local.DownloadMB())/5 + 30 // the download and setting it up
		}
	}
	return int(s + 0.5)
}

// takesWords says a duration the way people do.
func takesWords(s int) string {
	switch {
	case s < 60:
		return "under a minute"
	case s < 90:
		return "about a minute"
	case s < 90*60:
		return fmt.Sprintf("about %d minutes", (s+30)/60)
	}
	h := float64(s) / 3600
	if h < 1.75 {
		return "about an hour and a half"
	}
	return fmt.Sprintf("about %.0f hours", h)
}

func countWords(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return thousands(int(n)) + " " + noun + "s"
}

// workspaceComputers are the workspace's computers that aren't removed.
func workspaceComputers(ctx context.Context, a *app.App) []DatabaseComputer {
	insts := installationsOf(ctx, a)
	running := runningInstallations(ctx, a)
	out := []DatabaseComputer{}
	seenThis := false
	for _, in := range insts {
		c := DatabaseComputer{Name: firstNonEmpty(in.Name, in.Hostname, in.ID), This: in.ID == a.Cfg.Installation.ID, Version: in.Version,
			Running: running[in.ID]}
		if c.This {
			// This computer runs this build, whatever its record last said.
			seenThis, c.Version = true, version.Current().Version
		} else if knows, why := knowsMoves(in, c.Name); !knows {
			c.Older, c.why = true, why
		} else if v := olderBuildLogged(ctx, a, in); v != "" {
			c.Older, c.Version = true, v
			c.why = fmt.Sprintf("%s ran MetaRush %s after a newer one (its activity log says so), and that one doesn't know about moving the database: started after the move, it would go on changing the old database. Remove it there, or update it, and open MetaRush once; or remove that computer from the workspace (metarush machine revoke %q), first.", c.Name, v, c.Name)
		}
		out = append(out, c)
	}
	if !seenThis {
		out = append(out, DatabaseComputer{Name: a.Cfg.MachineName(), This: true})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].This != out[j].This {
			return out[i].This
		}
		return out[i].Name < out[j].Name
	})
	return out
}

/* ---------- the move ---------- */

// QuietWait is how long a move waits for the workspace's computers to pause
// and for running work to stop; QuietGrace how long it lets the last of them
// settle (longer than MetaRush trusts a look at the marker, store.MoveLookTrusted).
var (
	QuietWait  = 150 * time.Second
	QuietGrace = 2 * time.Second
)

// DatabaseMove is database.move.
func DatabaseMove(ctx context.Context, a *app.App, in *DatabaseMoveIn) (*DatabaseMoveOut, error) {
	if err := databasePeopleOnly(a); err != nil {
		return nil, err
	}
	started := time.Now()
	unlock, err := lockDBMove(a)
	if err != nil {
		return nil, err
	}
	defer unlock()
	rec := moveRecordFrom(ctx, a, in.DatabaseTargetIn)
	out, err := databaseMove(ctx, a, in, rec, started)
	if err != nil && forgetUnneededAddress(context.WithoutCancel(ctx), a) {
		if e := apperr.From(err); e != nil {
			e.Hint = strings.TrimSpace(e.Hint + " " + ForgotAddressWords)
			err = e
		}
	}
	rec.finish(a, out, err)
	return out, err
}

func databaseMove(ctx context.Context, a *app.App, in *DatabaseMoveIn, rec *dbMoveRun, started time.Time) (*DatabaseMoveOut, error) {
	say := progressFrom(ctx)
	marker, err := a.DB.ReadMoveMarker(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't look into the workspace's database.")
	}
	if marker != nil && marker.State == store.MoveMoved {
		return nil, movedError(marker)
	}
	resume := marker != nil && marker.State == store.MoveMoving
	// The plan first, nothing set up: a move that can't go ahead stops here.
	pre, err := openTarget(ctx, a, in.DatabaseTargetIn, false)
	if err != nil {
		return nil, err
	}
	rec.place(a, pre.place)
	if resume && !markerMatches(marker, pre, a) {
		pre.close()
		return nil, apperr.New(apperr.Conflict, "A move to %s started on %s and waits to be continued or abandoned.", marker.To.Words, firstNonEmpty(marker.FromName, "another computer")).
			WithHint("Continue that one (run it again with the same database on that computer), or abandon it first.").
			WithDetails(map[string]any{"reason": "move_waiting"}).WithCommand("metarush database abandon")
	}
	plan, err := planMove(ctx, a, pre)
	pre.close()
	if err != nil {
		return nil, err
	}
	if plan.Nothing {
		return nil, apperr.New(apperr.Conflict, "%s", plan.Summary)
	}
	if len(plan.KeptHere) > 0 && LeaveDatabase != nil {
		// What may leave refuses it (D337): said and recorded, before anything
		// is set up or frozen.
		if err := LeaveDatabase(ctx, a, config.Database{URI: pre.uri, Name: pre.name, Local: localOf(pre)}, false); err != nil {
			return nil, err
		}
	}
	if !plan.Ready {
		return nil, apperr.New(apperr.Conflict, "Nothing was moved: %s", lowerFirst(plan.Problems[0])).
			WithDetails(map[string]any{"reason": "not_ready", "problems": plan.Problems})
	}
	rec.phase(a, "setting-up", "Getting the new database ready")
	say("Getting the new database ready", 0, 0)
	t, err := openTarget(ctx, a, in.DatabaseTargetIn, true)
	if err != nil {
		return nil, err
	}
	defer t.close()
	if !t.connect(ctx) {
		return nil, apperr.New(apperr.Database, "MetaRush couldn't reach the new database, or it didn't take the sign-in. Nothing was moved.").
			WithHint("Check its address (and that this computer may reach it), then plan again.")
	}
	moveID := newMoveID()
	if resume {
		moveID = marker.Move
	}
	rec.started(a, moveID, t.place)
	// 1. The freeze: from now on every computer refuses changes, and this
	// process writes nothing into the old database but the marker.
	m := store.MoveMarker{Move: moveID, Workspace: a.WorkspaceID(), To: t.place.markerTarget(a.Cfg.MachineName()), By: firstNonEmpty(a.Actor.UserName, "a person"),
		ByUserID: a.Actor.UserID, From: a.Cfg.Installation.ID, FromName: a.Cfg.MachineName()}
	if err := a.DB.StartMoveMarker(ctx, m); err != nil {
		if errors.Is(err, store.ErrMoveUnderway) {
			return nil, apperr.New(apperr.Conflict, "Another move of this database started meanwhile.").WithHint("See how it goes in Settings → Storage.")
		}
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't mark the workspace as being moved. Nothing was moved.")
	}
	if cur, err := a.DB.ReadMoveMarker(ctx); err == nil {
		a.SetMoveMarker(cur)
		marker = cur
	}
	// The marker says the move runs while it does (no other computer
	// abandons a running move).
	beatCtx, stopBeat := context.WithCancel(context.WithoutCancel(ctx))
	defer stopBeat()
	_ = a.DB.MoveBeat(beatCtx, moveID)
	go func() {
		tk := time.NewTicker(moveBeatEvery)
		defer tk.Stop()
		for {
			select {
			case <-beatCtx.Done():
				return
			case <-tk.C:
				_ = a.DB.MoveBeat(beatCtx, moveID)
			}
		}
	}()
	// 2. Every computer running MetaRush pauses; work that runs stops.
	rec.phase(a, "pausing", "Waiting for every computer to pause its changes")
	if err := waitQuiet(ctx, a, moveID, rec, say); err != nil {
		return nil, err
	}
	// 3. The copy, checked.
	rec.phase(a, "copying", "Copying the workspace")
	res, err := dbmove.Copy(ctx, dbmove.Options{From: a.DB, To: t.db, Workspace: a.WorkspaceID(), WorkspaceName: a.Cfg.Workspace.Name, Move: moveID,
		Stop: func(ctx context.Context) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			cur, err := a.DB.ReadMoveMarker(ctx)
			if err == nil && (cur == nil || cur.Move != moveID || cur.State != store.MoveMoving) {
				return errAbandoned
			}
			return nil
		},
		Progress: func(p dbmove.CopyProgress) {
			rec.progress(a, p)
			say(copyLabel(p), p.DocumentsDone, p.Documents)
		}})
	if err != nil {
		return nil, copyError(err, t.place)
	}
	// 4. The old database says the workspace moved: from now on every
	// computer still using it refuses changes there for good. This comes
	// before this computer switches: a stop in between leaves this computer
	// on the old database, told to switch — never on the new one while the
	// old one could still be abandoned and changed (D335).
	// The last look, right before: the old database (frozen) still adds up
	// to exactly what the copy holds, every kind of record listed again.
	rec.phase(a, "checking", "Looking at the old database one last time")
	// The same read keeps every record's fingerprint, for the looks at the old
	// database after the move (something written there late, D354).
	printsOK := false
	if pf := openLatePrints(a, moveID); pf != nil {
		err := dbmove.RecheckPrinting(ctx, a.DB, t.db, res, moveID, pf)
		if cerr := pf.Close(); err == nil && cerr == nil {
			printsOK = true
		} else {
			_ = os.Remove(latePrintsFile(a, moveID))
		}
		if err != nil {
			return nil, copyError(err, t.place)
		}
	} else if err := dbmove.Recheck(ctx, a.DB, t.db, res); err != nil {
		return nil, copyError(err, t.place)
	}
	ok, err := a.DB.SetMoveState(ctx, moveID, store.MoveMoving, store.MoveMoved, "")
	if err != nil || !ok {
		_ = os.Remove(latePrintsFile(a, moveID))
		if err == nil {
			return nil, apperr.New(apperr.Conflict, "The move was abandoned while it copied, so this computer didn't switch.").
				WithHint("The new database keeps its lock, so nothing uses that copy. Move again into a new, empty database when you want.")
		}
		return nil, apperr.Wrap(apperr.Database, err, "The copy is whole and checked, but MetaRush couldn't mark the old database as moved, so nothing switched. Run the move again to finish it.")
	}
	movedAt := time.Now().UTC()
	if cur, err := a.DB.ReadMoveMarker(ctx); err == nil {
		a.SetMoveMarker(cur)
		if cur != nil && cur.MovedAt != nil {
			movedAt = cur.MovedAt.UTC()
		}
	}
	// The old database's address stays on this computer until the last look
	// at it (D353); without it, or the fingerprints, there are no looks.
	lateOK := printsOK && !lateKeepFails && keepLookAddress(a.Cfg, moveID, a.Cfg.Database.URI) == nil
	if !lateOK {
		forgetLookAddress(a, moveID)
	}
	// 5. This computer switches (checked the way MetaRush loads its settings;
	// put back when it doesn't work).
	rec.phase(a, "switching", "Switching this computer to the new database")
	switchedAt := time.Now().UTC()
	switchNow := func() error {
		if switchFails != nil { // tests: a switch that doesn't hold
			if err := switchFails(); err != nil {
				return err
			}
		}
		return switchDatabase(ctx, a, t.uri, t.name, localOf(t))
	}
	if err := switchNow(); err != nil {
		e := apperr.From(err)
		return nil, apperr.New(e.Code, "The copy is whole and checked and the workspace moved, but this computer couldn't switch to it: %s", lowerFirst(e.Message)).
			WithHint(strings.TrimSpace(e.Hint + " Then switch this computer (Settings → Storage shows how): the new database's address is still kept here.")).
			WithDetails(map[string]any{"reason": "not_switched", "problem": e.Details}).WithCommand("metarush database switch --to " + map[bool]string{true: "here", false: "saved"}[t.local != nil] + " --name " + t.name)
	}
	_ = a.DB.MoveSwitched(ctx, moveID, a.Cfg.Installation.ID)
	if cur, err := a.DB.ReadMoveMarker(ctx); err == nil {
		a.SetMoveMarker(cur)
	}
	forgetMoveAddress(a.Cfg)
	out := &DatabaseMoveOut{From: databasePlaceOf(a.Cfg.Database), To: t.place, Collections: res.Collections, Documents: res.Documents, Resumed: resume || res.Resumed,
		Verified: res.Verified, Switched: true, Marked: true, Others: []string{}, Next: []string{}, Notes: []string{}, Kept: keptWords(databasePlaceOf(a.Cfg.Database))}
	if n := len(res.IndexesLeft); n > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("The old database had %s MetaRush doesn't make, so the new one doesn't (it has MetaRush's own): %s.",
			map[bool]string{true: "an index", false: fmt.Sprintf("%d indexes", n)}[n == 1], strings.Join(res.IndexesLeft, "; ")))
	}
	for _, c := range plan.Computers {
		if !c.This {
			out.Others = append(out.Others, c.Name)
		}
	}
	// The record goes into the new database, where the workspace lives on.
	recordMove(ctx, t.db, a, "database.move", "moved the workspace's database", map[string]any{"from": out.From.Words, "to": t.place.Words,
		"documents": res.Documents, "collections": len(res.Collections), "resumed": out.Resumed, "seconds": int(time.Since(started).Seconds())})
	// The first look at the old database for anything written there late
	// (D352): right after the switch; later ones by MetaRush in the background.
	if lateOK {
		out.Late = startLateLooks(ctx, a, t.db, a.DB, moveID, out.From, movedAt)
	}
	if out.Late == nil {
		out.Notes = append(out.Notes, "MetaRush couldn't keep what it needs to look at the old database again later, for anything written there after the move.")
	} else if out.Late.Waiting > 0 {
		out.Next = append(out.Next, out.Late.Summary+" Settings → Storage → Move the database shows it (metarush database late-writes).")
	} else {
		out.Next = append(out.Next, lateLooksAgainWords)
	}
	// MetaRush running in the background here starts again with the new
	// database (it follows config.yaml).
	if running, restarted := waitForDaemonRestart(ctx, a.Cfg.Home(), switchedAt); running {
		out.Restarted = restarted
		if !restarted {
			out.Next = append(out.Next, "MetaRush running in the background on this computer didn't start again yet: quit it and start it again.")
		}
	}
	out.Seconds = time.Since(started).Seconds()
	out.Summary = fmt.Sprintf("Moved the workspace's database, %s, to %s, every record checked. This computer uses it now; the old database is kept, marked as moved, and nothing was deleted.",
		countWords(res.Documents, "record"), t.place.Words)
	if len(out.Others) > 0 {
		if t.place.Reach == "this-computer" {
			out.Next = append(out.Next, fmt.Sprintf("Your other computers (%s) can't reach the new database, so they keep refusing changes: this computer is the only one using the workspace now.", joinAnd(out.Others)))
		} else {
			out.Next = append(out.Next, fmt.Sprintf("On each other computer (%s), switch to the new database with that computer's own sign-in: until then it refuses changes. Settings → Storage there shows how.", joinAnd(out.Others)))
		}
	}
	out.Next = append(out.Next, out.Kept)
	return out, nil
}

var errAbandoned = errors.New("the move was abandoned")

// A running move says so in its marker every moveBeatEvery; one that said so
// within MoveAliveFor runs (another computer doesn't abandon it).
var (
	moveBeatEvery = 10 * time.Second
	MoveAliveFor  = 45 * time.Second
)

// moveAlive reports whether a move's marker says it still runs (by the
// database server's clock).
func moveAlive(ctx context.Context, a *app.App, m *store.MoveMarker) bool {
	if m == nil || m.State != store.MoveMoving {
		return false
	}
	alive, err := a.DB.MoveAlive(ctx, m.Move, MoveAliveFor)
	return err != nil || alive // unknown: as running (never abandoned on a guess)
}

// switchFails, set by tests, makes a move's own switch fail after the old
// database was marked moved.
var switchFails func() error

// lateKeepFails, set by tests, plays a move that can't keep what the looks
// afterwards need.
var lateKeepFails bool

func copyLabel(p dbmove.CopyProgress) string {
	if p.Phase == "checking" {
		return "Checking the copy"
	}
	return "Copying the workspace"
}

// copyError says plainly why a copy stopped; the freeze stays (run the move
// again to continue it, or abandon it).
func copyError(err error, to DatabasePlace) error {
	var p *dbmove.Partial
	switch {
	case errors.Is(err, errAbandoned):
		return apperr.New(apperr.Canceled, "The move was abandoned, so this computer didn't switch.").
			WithHint("The new database keeps its lock, so nothing uses that copy. Changes go on in the old database.")
	case errors.Is(err, context.Canceled):
		return apperr.New(apperr.Canceled, "Stopped. Changes still wait on every computer: run the move again to continue where it stopped, or abandon it.").
			WithCommand("metarush database abandon")
	case errors.Is(err, dbmove.ErrChanged):
		where := ""
		if errors.As(err, &p) && p.Collection != "" {
			where = " (" + p.Collection + ")"
		}
		return apperr.New(apperr.Integrity, "Something changed in the old database while it was copied%s, so this computer didn't switch.", where).
			WithHint("A computer may not have paused (an older MetaRush, or one started meanwhile). The new database keeps its lock, so nothing uses that copy: abandon this move, then move again into a new, empty database.").
			WithDetails(map[string]any{"reason": "changed"}).WithCommand("metarush database abandon")
	}
	return apperr.New(apperr.Database, "The copy to %s stopped part-way, so this computer didn't switch. Changes still wait on every computer.", to.Words).
		WithHint("Run the move again: it continues where it stopped and checks everything. Or abandon it to let changes go on in the old database.").
		WithDetails(map[string]any{"problem": scrubAddresses(err.Error())}).WithCommand("metarush database abandon")
}

// BusyFresh is how recently a process must have listed its running work in
// the marker for the move to wait for it (a process that died stops saying so).
var BusyFresh = 15 * time.Second

// waitQuiet waits until every computer running MetaRush paused for the move,
// no work runs (job leases) and no change that was already running when the
// move began still runs anywhere, then lets it all settle and looks again.
func waitQuiet(ctx context.Context, a *app.App, move string, rec *dbMoveRun, say ProgressFunc) error {
	deadline := time.Now().Add(QuietWait)
	settled := false
	for {
		m, err := a.DB.ReadMoveMarker(ctx)
		if err != nil {
			return apperr.Wrap(apperr.Database, err, "MetaRush couldn't look into the workspace's database.")
		}
		if m == nil || m.Move != move || m.State != store.MoveMoving {
			return copyError(errAbandoned, DatabasePlace{})
		}
		waiting := notPaused(ctx, a, m)
		busy := busyComputers(ctx, a)
		running := runningChanges(ctx, a, m)
		rec.waiting(a, append(append(append([]string{}, waiting...), busy...), running...))
		if len(waiting) == 0 && len(busy) == 0 && len(running) == 0 {
			if settled {
				// The copy starts — only while no running change is listed,
				// in one step: from now on none writes (store/guard.go).
				ok, err := a.DB.MoveCopying(ctx, move, BusyFresh)
				if err != nil {
					return apperr.Wrap(apperr.Database, err, "MetaRush couldn't write the move's marker.")
				}
				if ok {
					return nil
				}
				settled = false
				continue
			}
			// Quiet: let what was on its way land, then look once more.
			settled = true
			select {
			case <-ctx.Done():
				return copyError(ctx.Err(), DatabasePlace{})
			case <-time.After(QuietGrace):
			}
			continue
		}
		settled = false
		if time.Now().After(deadline) {
			var parts []string
			if len(waiting) > 0 {
				parts = append(parts, fmt.Sprintf("MetaRush on %s hasn't paused its changes", joinAnd(waiting)))
			}
			if len(busy) > 0 {
				parts = append(parts, fmt.Sprintf("work is still running on %s", joinAnd(busy)))
			}
			if len(running) > 0 {
				parts = append(parts, fmt.Sprintf("a change started before the move is still running on %s", joinAnd(running)))
			}
			return apperr.New(apperr.Conflict, "Nothing was copied yet: %s.", strings.Join(parts, ", and ")).
				WithHint("An older MetaRush doesn't pause by itself: update it, or quit MetaRush on that computer. Let running work finish. Then run the move again to continue, or abandon it to let changes go on.").
				WithDetails(map[string]any{"reason": "not_paused", "computers": waiting, "busy": busy, "changes": running}).WithCommand("metarush database abandon")
		}
		label := "Waiting for every computer to pause its changes"
		switch {
		case len(waiting) > 0:
			label = "Waiting for " + joinAnd(waiting) + " to pause its changes"
		case len(busy) > 0:
			label = "Waiting for the work running on " + joinAnd(busy) + " to stop"
		case len(running) > 0:
			label = "Waiting for a change started on " + joinAnd(running) + " to finish"
		}
		say(label, 0, 0)
		select {
		case <-ctx.Done():
			return copyError(ctx.Err(), DatabasePlace{})
		case <-time.After(time.Second):
		}
	}
}

// runningChanges names the computers with a change still running that began
// before the freeze (listed in the marker by their processes, recently).
func runningChanges(ctx context.Context, a *app.App, m *store.MoveMarker) []string {
	if len(m.Busy) == 0 {
		return nil
	}
	now, err := a.DB.ServerNow(ctx)
	if err != nil {
		now = time.Now().UTC()
	}
	names := map[string]string{}
	for _, in := range installationsOf(ctx, a) {
		names[in.ID] = firstNonEmpty(in.Name, in.Hostname, in.ID)
	}
	seen := map[string]bool{}
	out := []string{}
	for _, b := range m.Busy {
		if len(b.Ops) == 0 || now.Sub(b.At) > BusyFresh {
			continue
		}
		who := firstNonEmpty(names[b.Installation], b.Installation, "another computer")
		if !seen[who] {
			seen[who] = true
			out = append(out, who)
		}
	}
	sort.Strings(out)
	return out
}

// runningInstallations are the computers that ran MetaRush within
// RunningWithin, by the database server's clock.
func runningInstallations(ctx context.Context, a *app.App) map[string]bool {
	f := store.SeenWithin("lastSeenAt", RunningWithin)
	f["state"] = bson.M{"$ne": models.InstallationRevoked}
	var insts []models.Installation
	_ = a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), f, &insts)
	out := map[string]bool{}
	for _, in := range insts {
		out[in.ID] = true
	}
	return out
}

// notPaused names the computers running MetaRush that didn't pause for the
// move yet (this one only when MetaRush runs here in the background); the
// marker's times are all the database server's.
func notPaused(ctx context.Context, a *app.App, m *store.MoveMarker) []string {
	running := runningInstallations(ctx, a)
	out := []string{}
	for _, in := range installationsOf(ctx, a) {
		if !running[in.ID] {
			continue
		}
		if in.ID == a.Cfg.Installation.ID && !daemonRunsHere(a) {
			continue
		}
		if at, ok := m.Holding[in.ID]; ok && !at.Before(m.StartedAt) {
			continue
		}
		out = append(out, firstNonEmpty(in.Name, in.Hostname, in.ID))
	}
	sort.Strings(out)
	return out
}

// mediaMoveRunning names the computer moving the media now ("" none): a
// move of the database doesn't start meanwhile.
func mediaMoveRunning(ctx context.Context, a *app.App) string {
	if MoveRunningHere(ctx, a) {
		return a.Cfg.MachineName()
	}
	var js []models.Job
	f := store.LiveLease()
	f["type"], f["state"] = jobs.TypeStorageMove, models.JobRunning
	_ = a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), f, &js)
	for _, j := range js {
		if j.Lease != nil {
			return firstNonEmpty(j.Lease.OwnerName, j.Lease.Owner, "another computer")
		}
	}
	return ""
}

// busyComputers names the computers with work running (a live lease).
func busyComputers(ctx context.Context, a *app.App) []string {
	var js []models.Job
	f := store.LiveLease()
	f["state"] = models.JobRunning
	_ = a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), f, &js)
	seen := map[string]bool{}
	out := []string{}
	for _, j := range js {
		who := ""
		if j.Lease != nil {
			who = firstNonEmpty(j.Lease.OwnerName, j.Lease.Owner)
		}
		who = firstNonEmpty(who, "another computer")
		if !seen[who] {
			seen[who] = true
			out = append(out, who)
		}
	}
	sort.Strings(out)
	return out
}

// recordMove writes a move's (or a switch's) entry into the new database's
// change history and activity log directly: the old database is frozen, and
// the new one is where the workspace lives on.
func recordMove(ctx context.Context, db *store.DB, a *app.App, op, summary string, facts map[string]any) {
	ctx = context.WithoutCancel(ctx)
	now := time.Now().UTC()
	ev := models.AuditEvent{ID: ids.New(ids.Audit), WorkspaceID: a.WorkspaceID(), At: now, Actor: a.Actor, Action: op, TargetType: "workspace", TargetID: a.WorkspaceID(), Details: facts}
	_, _ = db.C(store.CAudit).InsertOne(ctx, ev)
	ch := logbook.ChannelCLI
	if a.Actor.Via == "dashboard" {
		ch = logbook.ChannelDashboard
	}
	e := logbook.Entry{ID: ids.New(logbook.EntryPrefix), WorkspaceID: a.WorkspaceID(), At: now, Kind: logbook.KindOp, Level: logbook.LevelInfo, Channel: ch,
		UserID: a.Actor.UserID, UserName: a.Actor.UserName, InstallationID: a.Cfg.Installation.ID, Machine: a.Cfg.MachineName(),
		Version: version.Current().Version, Summary: summary,
		Op: &logbook.OpInfo{Name: op, Mutating: true, Human: true, OK: true, Actions: []string{op}, Output: &logbook.OutputInfo{Facts: facts}}}
	_, _ = db.C(store.CLogEntries).InsertOne(ctx, e)
}

// movedError is a change refused because the workspace moved.
func movedError(m *store.MoveMarker) error {
	return (&store.ErrDatabaseMoving{Marker: *m}).AppError()
}

// movingError is a change refused while the workspace is being moved.
func movingError(m *store.MoveMarker) error {
	return (&store.ErrDatabaseMoving{Marker: *m}).AppError()
}

// FrozenError is the refusal of a change while a move keeps changes out of
// the workspace's database (nil when none does).
func FrozenError(m *store.MoveMarker) error {
	switch {
	case m == nil:
		return nil
	case m.State == store.MoveMoved:
		return movedError(m)
	case m.State == store.MoveMoving:
		return movingError(m)
	}
	return nil
}

// frozenExempt are the operations that run while the database is frozen:
// the move's own.
var frozenExempt = map[string]bool{"database.plan": true, "database.move": true, "database.switch": true, "database.abandon": true, "database.move.status": true}

// refuseWhileMoving is dispatch's check (D335): a change while a move keeps
// changes out of the database is refused, plainly.
func refuseWhileMoving(ctx context.Context, a *app.App, op *Op) error {
	if !op.Mutating || frozenExempt[op.Name] || a.ReadOnly || a.DB == nil {
		return nil
	}
	m, err := a.DB.MoveMarkerForChange(ctx)
	if err != nil {
		return err // not read, and no recent look that showed no move: nothing changes
	}
	return FrozenError(m)
}

/* ---------- the switch on another computer ---------- */

// DatabaseSwitchIn switches this computer.
type DatabaseSwitchIn struct {
	// Name is the new database's name (default: the one the move named).
	Name string `json:"name,omitempty" jsonschema:"maxLength=63"`
}

// DatabaseSwitchOut is a switch done.
type DatabaseSwitchOut struct {
	From     DatabasePlace `json:"from"`
	To       DatabasePlace `json:"to"`
	Switched bool          `json:"switched"`
	Next     []string      `json:"next"`
	// Late is the looks at the old database, when this is the computer that
	// moved it (its own switch hadn't held).
	Late    *LateLookView `json:"late,omitempty"`
	Summary string        `json:"summary"`
}

// DatabaseSwitch is database.switch: this computer, still using the old
// database, switches to the one the workspace moved to, with its own
// sign-in (kept on this computer for the switch, SaveMoveAddress).
func DatabaseSwitch(ctx context.Context, a *app.App, in *DatabaseSwitchIn) (*DatabaseSwitchOut, error) {
	if err := databasePeopleOnly(a); err != nil {
		return nil, err
	}
	m, err := a.DB.ReadMoveMarker(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't look into the workspace's database.")
	}
	if m == nil || m.State != store.MoveMoved {
		msg := "The workspace didn't move to another database, so there is nothing to switch to."
		if m != nil && m.State == store.MoveMoving {
			msg = "The workspace is still being moved: switch once the move is done."
		}
		return nil, apperr.New(apperr.Conflict, "%s", msg).WithCommand("metarush database move-status")
	}
	// The database on the computer that moved it is reached by that computer
	// only: it switches there (a move whose own switch didn't hold).
	here := m.To.Kind == MoveToHere
	if here && m.From != a.Cfg.Installation.ID {
		return nil, apperr.New(apperr.Conflict, "The workspace moved to the database on %s, which only that computer reaches.", firstNonEmpty(m.To.Computer, "another computer")).
			WithHint("This computer can't use it. To use the workspace here, export it on that computer and restore it here, or move it to a database every computer reaches.")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = m.To.Name
	}
	target := DatabaseTargetIn{To: MoveToSaved, Name: name}
	if here {
		target.To = MoveToHere
	}
	t, err := openTarget(ctx, a, target, here)
	if err != nil {
		return nil, err
	}
	defer t.close()
	if !t.connect(ctx) {
		return nil, apperr.New(apperr.Database, "MetaRush couldn't reach the new database with this computer's sign-in.").
			WithHint("Check the address and this computer's own database user, then switch again.")
	}
	// It must be this move's copy, whole and checked, of this workspace.
	lock, err := t.db.ReadRestoreLock(ctx)
	if err != nil || lock == nil || lock.Kind != store.LockMove || lock.Archive != m.Move || lock.State != store.RestoreDone || lock.Workspace != a.WorkspaceID() {
		return nil, apperr.New(apperr.Conflict, "That database isn't the one the workspace moved to (or its copy isn't finished), so this computer didn't switch.").
			WithHint(fmt.Sprintf("The workspace moved to %s, as a database called “%s”.", m.To.Words, m.To.Name)).
			WithDetails(map[string]any{"reason": "not_the_copy"})
	}
	from := databasePlaceOf(a.Cfg.Database)
	if err := switchDatabase(ctx, a, t.uri, t.name, localOf(t)); err != nil {
		return nil, err
	}
	_ = a.DB.MoveSwitched(ctx, m.Move, a.Cfg.Installation.ID)
	forgetMoveAddress(a.Cfg)
	recordMove(ctx, t.db, a, "database.switch", "switched this computer to the workspace's new database", map[string]any{"from": from.Words, "to": t.place.Words})
	out := &DatabaseSwitchOut{From: from, To: t.place, Switched: true, Next: []string{}}
	// The computer that moved it, whose own switch didn't hold: its looks at
	// the old database start now (D352).
	if m.From == a.Cfg.Installation.ID && KeepsLookAddress(a.Cfg, m.Move) {
		if _, err := os.Stat(latePrintsFile(a, m.Move)); err == nil && lateLookView(ctx, a, t.db, m.Move) == nil {
			movedAt := time.Now().UTC()
			if m.MovedAt != nil {
				movedAt = m.MovedAt.UTC()
			}
			if v := startLateLooks(ctx, a, t.db, a.DB, m.Move, from, movedAt); v != nil {
				out.Late = v
			}
		}
	}
	out.Summary = "This computer uses " + t.place.Words + " now, with its own sign-in. MetaRush running in the background here starts again by itself to use it."
	out.Next = append(out.Next, keptWords(from))
	return out, nil
}

/* ---------- abandoning a move ---------- */

// DatabaseAbandonIn has no fields.
type DatabaseAbandonIn struct{}

// DatabaseAbandonOut is a move abandoned.
type DatabaseAbandonOut struct {
	Abandoned bool   `json:"abandoned"`
	To        string `json:"to,omitempty"`
	Summary   string `json:"summary"`
}

// DatabaseAbandon is database.abandon: the freeze is lifted; the copy keeps
// its lock, so nothing uses it.
func DatabaseAbandon(ctx context.Context, a *app.App, _ *DatabaseAbandonIn) (*DatabaseAbandonOut, error) {
	if err := databasePeopleOnly(a); err != nil {
		return nil, err
	}
	m, err := a.DB.ReadMoveMarker(ctx)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't look into the workspace's database.")
	}
	switch {
	case m == nil || m.State == store.MoveAbandoned:
		return &DatabaseAbandonOut{Summary: "No move of the database is waiting: changes go on as usual."}, nil
	case m.State == store.MoveMoved:
		return nil, apperr.New(apperr.Conflict, "The move finished: the workspace moved to %s, so it can't be abandoned.", m.To.Words).
			WithHint("Switch this computer too (Settings → Storage shows how).").WithCommand("metarush database switch --to address --name " + m.To.Name)
	}
	if moveRunsHere(a, m.Move) {
		return nil, apperr.New(apperr.Conflict, "The move is still running on this computer.").WithHint("Stop it first (Ctrl-C in its terminal), or wait for it to finish.")
	}
	if m.From != a.Cfg.Installation.ID && moveAlive(ctx, a, m) {
		return nil, apperr.New(apperr.Conflict, "The move is still running on %s.", firstNonEmpty(m.FromName, "another computer")).
			WithHint("Stop it there first, or wait for it to finish; once it has stopped it can be abandoned from any computer.").
			WithDetails(map[string]any{"reason": "move_running"})
	}
	ok, err := a.DB.SetMoveState(ctx, m.Move, store.MoveMoving, store.MoveAbandoned, firstNonEmpty(a.Actor.UserName, "a person"))
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't lift the pause.")
	}
	if !ok {
		return nil, apperr.New(apperr.Conflict, "The move finished or changed meanwhile.").WithCommand("metarush database move-status")
	}
	cur, _ := a.DB.ReadMoveMarker(ctx)
	a.SetMoveMarker(cur)
	a.Audit(ctx, "database.abandon", "workspace", a.WorkspaceID(), "", map[string]any{"to": m.To.Words, "startedBy": m.By, "startedOn": m.FromName})
	forgetUnneededAddress(ctx, a)
	return &DatabaseAbandonOut{Abandoned: true, To: m.To.Words,
		Summary: "Abandoned the move to " + m.To.Words + ": changes go on in this database on every computer. The copy keeps its lock, so nothing uses it; nothing was deleted, and this computer no longer keeps the new database's address."}, nil
}

// scrubAddresses takes database addresses (and their sign-ins) out of text.
func scrubAddresses(s string) string {
	return addressRe.ReplaceAllString(s, "[address]")
}

var addressRe = regexp.MustCompile(`mongodb(?:\+srv)?://[^\s"']+`)
