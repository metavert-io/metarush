package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/dbmove"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/publish"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// Something written late (D352–D359, D370, docs/database-move.md): a write a
// computer sent just before it slept for longer than MetaRush trusts a look
// at the move's marker can still land in the old database after the move
// marked it moved — and nobody would ever see it there. So the computer that
// moved keeps, until its last look, the old database's address and the
// fingerprints of every record the move's last look read (dbmove), and looks
// at the old database again: right after it switched, then about 15 minutes,
// an hour and a day after the move (MetaRush in the background), and whenever
// an owner asks (`metarush database late-writes`). A look only reads the old
// database, within bounds, going on next time from where it stopped; "nothing
// was written" is said only once a look read everything. What it finds — records new, changed or gone there
// since — goes into this database (late_looks, late_writes) and owners hear of
// it once, in plain words; what a later look no longer finds settles by
// itself. A person carries a record over (with the rest of its group: the
// same clip's transcript, the same batch of fixes), only while this
// database's own is still the one the move copied (check and set), or leaves
// it. Kinds of record that hold sign-in secrets are only ever reported: no
// copy, nothing shown, never carried over. A record removed from the old
// database is never removed here. Copies are kept for LateCopyKeep, and gone
// once a person decided. After the last look the address and the
// fingerprints are dropped.

func init() {
	Def("database.late.writes", "List what was written into the old database after the workspace moved to this one — records new, changed or gone there since the move — and, on the computer that moved it while it keeps the old database's address, look again now (owners)",
		Flags{OwnerOnly: true}, DatabaseLateWrites)
	Def("database.late.show", "Show a record written into the old database after the move: the old database's version beside this database's, without what MetaRush never shows (owners, people only)",
		Flags{OwnerOnly: true, HumanOnly: true}, DatabaseLateShow)
	Def("database.late.carry", "Carry a record written into the old database after the move (with the rest of its group) over into this database — only while this database's own is still the one the move copied (owners, people only)",
		Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, DatabaseLateCarry)
	Def("database.late.leave", "Leave a record written into the old database after the move (with the rest of its group) as it is here: this database keeps its own (owners, people only)",
		Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, DatabaseLateLeave)
}

// When the computer that moved looks again, after the old database was
// marked moved (the move itself looks right after it switched): a computer
// asleep through the move can wake hours later.
var (
	LateLookAfter = []time.Duration{15 * time.Minute, time.Hour, 24 * time.Hour}
	// LateLookGrace is how long after the last look's time a look that can't
	// reach the old database is tried again; then the looks end.
	LateLookGrace = 24 * time.Hour
	// lateRetryEvery is how often a look that couldn't be made is tried again.
	lateRetryEvery = 10 * time.Minute
	// LateCopyKeep is how long a copy of a record nobody decided is kept to
	// carry over; then it is dropped (the finding stays).
	LateCopyKeep = 30 * 24 * time.Hour
	// A look's bounds: in the background (and right after the move) it reads
	// at most so much of the old database again, for so long, pausing as it
	// goes; one a person asks for may read far more.
	lateBackground = dbmove.LookOptions{MaxBytes: 256 << 20, MaxTime: 5 * time.Minute, Pace: 20 * time.Millisecond}
	lateAsked      = dbmove.LookOptions{MaxBytes: 16 << 30, MaxTime: 2 * time.Hour, Pace: 2 * time.Millisecond}
	// How many records found for the first time one look keeps, and one move
	// in all; the rest are counted (a later look keeps going past them).
	lateKeepPerLook = 2000
	lateKeepPerMove = 20000
)

// lateCarryStale is how long a carry-over that stopped part-way (the process
// went away) holds a record before it waits for a person again.
const lateCarryStale = 10 * time.Minute

// errLookRunning: one look at a time, per move.
var errLookRunning = apperr.New(apperr.Conflict, "MetaRush is looking at the old database right now: try again when that look is done.")

// lateKeepMax is the largest old record kept to carry over (a record can be
// up to 16 MB; the one that keeps it needs room around it).
const lateKeepMax = 8 << 20

// How a look came about.
const (
	LateSoon  = "soon"  // the move itself, right after it switched
	LateLater = "later" // on its schedule, MetaRush in the background
	LateAsked = "asked" // an owner asked (database.late.writes look)
)

// States of a record found.
const (
	LateWaiting  = "waiting"  // a person decides: carry it over or leave it
	LateCarrying = "carrying" // being carried over now
	LateCarried  = "carried"  // carried over into this database
	LateLeft     = "left"     // left as it is here
	LateSettled  = "settled"  // a later look no longer found it different: nothing to carry
)

// lateGroupBy are the kinds of record whose records belong together, and the
// field that says to what: carried over or left all at once.
var lateGroupBy = map[string]struct{ field, what string }{
	store.CSegments:            {"assetId", "the same clip's transcript"},
	store.CTranscriptOriginals: {"assetId", "the same clip's first transcript"},
	store.CInsights:            {"assetId", "what MetaRush learned from the same clip"},
	store.CSpeakers:            {"assetId", "the voices of the same clip"},
	store.CFaces:               {"assetId", "the faces of the same clip"},
	store.CTranscriptFixes:     {"batch", "the same fix"},
}

// lateLookRec is a move's looks at the old database (late_looks, in the
// database the workspace moved to; _id is the move).
type lateLookRec struct {
	ID          string `bson:"_id"`
	WorkspaceID string `bson:"workspaceId"`
	// From is the old database in words, FromHost where it is (hosts only),
	// FromName its name: never a sign-in.
	From     string `bson:"from"`
	FromHost string `bson:"fromHost,omitempty"`
	FromName string `bson:"fromName"`
	// MovedAt: when the old database was marked moved (its server's time).
	MovedAt time.Time `bson:"movedAt"`
	// Computer looks (it keeps the old database's address and the move's
	// fingerprints); Due are its looks to come, by its own clock; Done how
	// many of them a look made.
	Computer     string          `bson:"computer"`
	ComputerName string          `bson:"computerName"`
	Due          []time.Time     `bson:"due"`
	Done         int             `bson:"done"`
	Looks        []LateLookTaken `bson:"looks"`
	Tried        *time.Time      `bson:"tried,omitempty"`
	Problem      string          `bson:"problem,omitempty"`
	Ended        bool            `bson:"ended"`
	EndedAt      *time.Time      `bson:"endedAt,omitempty"`
	EndedWhy     string          `bson:"endedWhy,omitempty"`
	// Found numbers the records kept (the last one's number); More is how
	// many the current pass found but didn't keep; Partial and Stopped: the
	// last look stopped before it saw everything, and why; Unread are the
	// kinds of record not read yet in this pass; ReadsAll: the old database's
	// server doesn't say its own hashes, so every look reads it whole.
	Found    int      `bson:"found"`
	More     int      `bson:"more"`
	Partial  bool     `bson:"partial"`
	Stopped  string   `bson:"stopped,omitempty"`
	Unread   []string `bson:"unread,omitempty"`
	ReadsAll bool     `bson:"readsAll"`
	// Pass is one way through everything, over as many looks as it takes.
	Pass latePass `bson:"pass"`
	// Complete: the latest attempt finished a pass, every kind read and
	// nothing left uncounted (a look that failed since takes it back);
	// CompleteAt says when, CompleteFrom when that pass began. Only then does
	// MetaRush say nothing was written.
	Complete     bool       `bson:"complete"`
	CompleteAt   *time.Time `bson:"completeAt,omitempty"`
	CompleteFrom *time.Time `bson:"completeFrom,omitempty"`
	// Gone: the old database is gone or empty.
	Gone bool `bson:"gone,omitempty"`
	// LookingUntil: a look runs now, at most until then (one at a time).
	LookingUntil *time.Time `bson:"lookingUntil,omitempty"`
}

// latePass is one way through the whole old database: a look that stops at
// its bounds saves where, and the next goes on from there.
type latePass struct {
	ID string `bson:"id"`
	// From is when it began; Position where the last look stopped (none: it
	// finished, so the next look begins a new pass); Done the kinds finished
	// with; More how many records found weren't kept; Looks how many looks it took.
	From     time.Time            `bson:"from"`
	Position *dbmove.LookPosition `bson:"position,omitempty"`
	Done     []string             `bson:"done,omitempty"`
	More     int                  `bson:"more"`
	Looks    int                  `bson:"looks"`
}

// LateLookTaken is one look at the old database after a move.
type LateLookTaken struct {
	At   time.Time `bson:"at" json:"at"`
	When string    `bson:"when" json:"when" jsonschema:"enum=soon,enum=later,enum=asked"`
	// Found is how many records it found for the first time (or changed
	// again); More how many more it found but didn't keep; Settled how many
	// found before it no longer found different.
	Found   int `bson:"found" json:"found"`
	More    int `bson:"more,omitempty" json:"more,omitempty"`
	Settled int `bson:"settled,omitempty" json:"settled,omitempty"`
	// Partial: it stopped before it saw everything (Stopped says why).
	Partial bool   `bson:"partial,omitempty" json:"partial,omitempty"`
	Stopped string `bson:"stopped,omitempty" json:"stopped,omitempty"`
	// Complete: it finished a pass: every kind read, nothing left uncounted.
	Complete bool `bson:"complete,omitempty" json:"complete,omitempty"`
}

// lateWriteRec is one record found written into the old database after the
// move (late_writes).
type lateWriteRec struct {
	ID          string `bson:"_id"`
	WorkspaceID string `bson:"workspaceId"`
	Move        string `bson:"move"`
	N           int    `bson:"n"`
	Collection  string `bson:"collection"`
	// Key is the record's _id: its BSON type, then its value (none when
	// ReportOnly: a sign-in link's ID can be its own secret).
	Key  []byte `bson:"key,omitempty"`
	Kind string `bson:"kind"`
	// ReportOnly: a kind that holds sign-in secrets (or that an archive
	// leaves out): no copy, nothing shown, never carried over.
	ReportOnly bool `bson:"reportOnly,omitempty"`
	// Group says which records belong together (lateGroupBy).
	Group string `bson:"group,omitempty"`
	// Old is the old database's version (none when gone, too large, report
	// only, decided, settled or past LateCopyKeep); Kept says one is kept,
	// since KeptAt.
	Old         bson.Raw   `bson:"old,omitempty"`
	Kept        bool       `bson:"kept"`
	KeptAt      *time.Time `bson:"keptAt,omitempty"`
	CopyDropped bool       `bson:"copyDropped,omitempty"`
	TooLarge    bool       `bson:"tooLarge,omitempty"`
	// Count: how many records a whole kind gone held at the move.
	Count int64 `bson:"count,omitempty"`
	// Print is the old database's version's fingerprint, MovePrint the
	// record's when the workspace moved ("" for a new one): the copy here
	// carries over only while it is still that.
	Print     string `bson:"print,omitempty"`
	MovePrint string `bson:"movePrint,omitempty"`
	// At is when it was written, as the record says (nil when it doesn't).
	At           *time.Time `bson:"at,omitempty"`
	Computer     string     `bson:"computer,omitempty"`
	ComputerName string     `bson:"computerName,omitempty"`
	FoundAt      time.Time  `bson:"foundAt"`
	SeenAt       time.Time  `bson:"seenAt"`
	// SeenBy is the last look that found it (a look that saw everything
	// settles what it didn't find).
	SeenBy string `bson:"seenBy,omitempty"`
	State  string `bson:"state"`
	// CarryingAt: when a person's carry-over claimed it (one that stopped
	// part-way waits again after lateCarryStale).
	CarryingAt *time.Time `bson:"carryingAt,omitempty"`
	Again      bool       `bson:"again,omitempty"`
	DecidedBy  string     `bson:"decidedBy,omitempty"`
	DecidedAt  *time.Time `bson:"decidedAt,omitempty"`
	SettledAt  *time.Time `bson:"settledAt,omitempty"`
	// Mixed (never stored): a group entry whose records were added, changed
	// and removed alike.
	Mixed bool `bson:"-"`
}

/* ---------- the old database's address, kept for the looks ---------- */

func lookAddressName(move string) string { return "database-late-" + move }

func lookAddressFile(home, move string) string {
	return filepath.Join(home, "database", "late-"+move+".secret")
}

// keepLookAddress keeps the old database's address on this computer until
// the move's last look: its keychain, or a private file beside the
// database's other secrets. Never config.yaml, an answer or the log.
func keepLookAddress(cfg *config.Config, move, uri string) error {
	if strings.TrimSpace(uri) == "" {
		return errors.New("no address")
	}
	rememberAddress(uri)
	if cfg.SecretMode() == "env" {
		return localdb.WriteSecretFile(lookAddressFile(cfg.Home(), move), "the old database's address", uri, false)
	}
	_, err := config.StoreSecret(cfg.Home(), lookAddressName(move), uri)
	return err
}

// loadLookAddress reads the old database's address kept for a move's looks
// ("" when none is kept).
func loadLookAddress(cfg *config.Config, move string) string {
	var uri string
	if cfg.SecretMode() == "env" {
		if _, err := os.Stat(lookAddressFile(cfg.Home(), move)); err != nil {
			return ""
		}
		v, err := localdb.ReadSecretFile(lookAddressFile(cfg.Home(), move), "the old database's address")
		if err != nil {
			return ""
		}
		uri = v
	} else {
		v, err := config.ResolveSecret(cfg.Home(), config.SecretRef(lookAddressName(move)))
		if err != nil {
			return ""
		}
		uri = v
	}
	rememberAddress(uri)
	return strings.TrimSpace(uri)
}

// KeepsLookAddress reports whether this computer keeps the old database's
// address for a move's looks.
func KeepsLookAddress(cfg *config.Config, move string) bool { return loadLookAddress(cfg, move) != "" }

// forgetLookAddress drops the old database's address and the move's
// fingerprints once the looks are over.
func forgetLookAddress(a *app.App, move string) {
	if a.Cfg.SecretMode() == "env" {
		_ = os.Remove(lookAddressFile(a.Cfg.Home(), move))
	} else {
		_ = config.DeleteSecret(a.Cfg.Home(), lookAddressName(move))
	}
	_ = os.Remove(latePrintsFile(a, move))
}

// latePrintsFile is where a move keeps its fingerprints until its last look.
func latePrintsFile(a *app.App, move string) string {
	return filepath.Join(dbMoveDir(a), move+".prints")
}

// openLatePrints opens the file the move's last look writes its
// fingerprints into (nil when it can't: then there are no looks afterwards,
// and the move says so).
func openLatePrints(a *app.App, move string) *os.File {
	if err := os.MkdirAll(dbMoveDir(a), 0o700); err != nil {
		return nil
	}
	p := latePrintsFile(a, move)
	_ = os.Remove(p) // a move run again takes its last look again
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil
	}
	return f
}

/* ---------- starting the looks: the move ---------- */

// startLateLooks records the move's looks in the new database and takes the
// first one, right after this computer switched (old is the old database,
// still open). nil when the looks can't be kept.
func startLateLooks(ctx context.Context, a *app.App, newDB, old *store.DB, move string, from DatabasePlace, movedAt time.Time) *LateLookView {
	ctx = context.WithoutCancel(ctx)
	now := time.Now().UTC()
	rec := lateLookRec{ID: move, WorkspaceID: a.WorkspaceID(), From: from.Words, FromHost: from.Host, FromName: old.Name, MovedAt: movedAt,
		Computer: a.Cfg.Installation.ID, ComputerName: a.Cfg.MachineName(), Due: []time.Time{}, Looks: []LateLookTaken{}}
	for _, d := range LateLookAfter {
		rec.Due = append(rec.Due, now.Add(d))
	}
	if _, err := newDB.C(store.CLateLooks).ReplaceOne(ctx, bson.M{"_id": move}, rec, options.Replace().SetUpsert(true)); err != nil {
		forgetLookAddress(a, move)
		return nil
	}
	_, _ = lookAndRecord(ctx, a, newDB, &rec, old, LateSoon, now)
	return lateLookView(ctx, a, newDB, rec.ID)
}

/* ---------- a look ---------- */

// lookOutcome is what one look did.
type lookOutcome struct {
	found, more, settled int
	partial              bool
	stopped              string
	readsAll             bool
	complete, gone       bool
	unread               []string
	pass                 latePass
}

// lookAndRecord takes one look for a move and keeps what it found in db (the
// database the workspace moved to); owners hear of what is new. A look made
// (even one that stopped at its bounds: it isn't tried again) counts for
// every look due by now; after the last one the address and the
// fingerprints are dropped. old nil: opened with the address kept.
func lookAndRecord(ctx context.Context, a *app.App, db *store.DB, rec *lateLookRec, old *store.DB, when string, now time.Time) (*lookOutcome, error) {
	// One look at a time: a person's beside MetaRush's own would each settle
	// what only the other just saw. A look that went away holds it no longer
	// than a look may take.
	wall := time.Now().UTC()
	until := wall.Add(lateAsked.MaxTime + 2*time.Minute)
	if when != LateAsked {
		until = wall.Add(lateBackground.MaxTime + 2*time.Minute)
	}
	claim, err := db.C(store.CLateLooks).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID(),
		"$or": bson.A{bson.M{"lookingUntil": nil}, bson.M{"lookingUntil": bson.M{"$lt": wall}}}}, bson.M{"$set": bson.M{"lookingUntil": until}})
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't keep what it found in the old database.")
	}
	if claim.MatchedCount == 0 {
		return nil, errLookRunning
	}
	defer func() {
		_, _ = db.C(store.CLateLooks).UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$unset": bson.M{"lookingUntil": ""}})
	}()
	o, err := lookLate(ctx, a, db, rec, old, when, now)
	if err != nil {
		_, _ = db.C(store.CLateLooks).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID()},
			bson.M{"$set": bson.M{"tried": now, "problem": lateProblemWords(err), "complete": false}})
		rec.Complete = false
		if n := len(rec.Due); n > 0 && !now.Before(rec.Due[n-1]) {
			tellOwnersIncomplete(ctx, a, db, rec.ID)
		}
		return nil, err
	}
	done := 0
	for _, d := range rec.Due {
		if !d.After(now) {
			done++
		}
	}
	set := bson.M{"tried": nil, "problem": "", "more": o.more, "partial": o.partial, "stopped": o.stopped, "readsAll": o.readsAll,
		"complete": o.complete, "pass": o.pass, "unread": o.unread, "gone": o.gone}
	if o.complete {
		set["completeAt"], set["completeFrom"] = now, o.pass.From
	}
	if done > rec.Done {
		set["done"] = done
		rec.Done = done
	}
	done = rec.Done
	rec.Partial, rec.Complete, rec.More = o.partial, o.complete, o.more
	_, _ = db.C(store.CLateLooks).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$set": set, "$push": bson.M{"looks": LateLookTaken{At: now, When: when, Found: o.found, More: o.more, Settled: o.settled, Partial: o.partial, Stopped: o.stopped, Complete: o.complete}}})
	switch {
	case o.gone:
		endLateLooks(ctx, a, db, rec.ID, "The old database is gone or empty, so there is nothing left to look at.", true)
	case o.complete:
		quietLateBell(ctx, a, db, "late_incomplete:"+rec.ID, "info", "MetaRush checked everything in the old database after all",
			"A later look read all of the old database.")
		if done >= len(rec.Due) && len(rec.Due) > 0 {
			endLateLooks(ctx, a, db, rec.ID, "MetaRush made its last look at the old database.", true)
		}
	case done >= len(rec.Due) && len(rec.Due) > 0:
		// The last look didn't see everything: the address stays a while
		// (LookDueLate ends it), and owners hear of it while they can act.
		tellOwnersIncomplete(ctx, a, db, rec.ID)
	}
	return o, nil
}

// lateServerHashesWork asks whether a database's server says its own hash of
// each kind of record (replaced in tests).
var lateServerHashesWork = dbmove.ServerHashesWork

// lateReadsAllNote is what a plan says when it doesn't: every look after the
// move reads the whole database over the network, within a look's bounds.
func lateReadsAllNote() string {
	return fmt.Sprintf("This database's server doesn't say by itself what changed in it (MongoDB Atlas's smaller clusters don't), so each look at it after the move reads it whole again over the network. A look MetaRush makes by itself stops after %s or %d minutes and says so; you can then look again yourself.",
		wsarchive.HumanBytes(lateBackground.MaxBytes), int(lateBackground.MaxTime.Minutes()))
}

// lateProblemWords says why a look couldn't be made.
func lateProblemWords(err error) string {
	if e := apperr.From(err); e != nil && e.Code != apperr.Internal {
		return e.Message
	}
	return "MetaRush couldn't reach the old database to look at it again."
}

// endLateLooks ends a move's looks: the record says so and this computer
// drops the old database's address and the fingerprints. settled false: they
// ended without checking everything, and owners hear of it.
func endLateLooks(ctx context.Context, a *app.App, db *store.DB, move, why string, settled bool) {
	now := time.Now().UTC()
	set := bson.M{"ended": true, "endedAt": now, "endedWhy": why}
	if !settled {
		set["complete"] = false
	}
	_, _ = db.C(store.CLateLooks).UpdateOne(ctx, bson.M{"_id": move, "workspaceId": a.WorkspaceID(), "ended": false}, bson.M{"$set": set})
	forgetLookAddress(a, move)
	if !settled {
		tellOwnersIncomplete(ctx, a, db, move)
	}
}

// lookLate compares the old database with the move's fingerprints (reading
// it only, within the bounds of a look of its kind), keeps each record found
// in db, and — when it saw everything — settles what it found before that
// it no longer finds.
func lookLate(ctx context.Context, a *app.App, db *store.DB, rec *lateLookRec, old *store.DB, when string, now time.Time) (*lookOutcome, error) {
	prints := latePrintsFile(a, rec.ID)
	if _, err := os.Stat(prints); err != nil {
		return nil, apperr.New(apperr.NotConfigured, "This computer no longer keeps what it needs to look at the old database again.")
	}
	if old == nil {
		uri := loadLookAddress(a.Cfg, rec.ID)
		if uri == "" {
			return nil, apperr.New(apperr.NotConfigured, "This computer no longer keeps the old database's address.")
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		db2, err := store.Connect(cctx, uri, rec.FromName)
		cancel()
		if err != nil {
			return nil, apperr.New(apperr.Database, "MetaRush couldn't reach the old database to look at it again.").
				WithDetails(map[string]any{"problem": scrubAddresses(err.Error())})
		}
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = db2.Close(c)
			cancel()
		}()
		old = db2
	}
	opts := lateBackground
	if when == LateAsked {
		opts = lateAsked
	}
	var cur lateLookRec
	if err := db.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID()}).Decode(&cur); err == nil {
		rec.Found, rec.Pass = cur.Found, cur.Pass
	}
	// Going on with a pass that stopped part-way, or beginning a new one.
	pass := rec.Pass
	if pass.Position == nil {
		pass = latePass{ID: ids.New("lok"), From: now}
	}
	opts.From = pass.Position
	k := &keeper{ctx: ctx, a: a, db: db, look: rec, now: now, id: pass.ID, names: computerNames(ctx, a, db), room: min(lateKeepPerLook, lateKeepPerMove-rec.Found)}
	opts.Each = k.keep
	lctx, cancel := context.WithTimeout(ctx, opts.MaxTime+time.Minute)
	defer cancel()
	res, err := dbmove.Look(lctx, old, prints, opts)
	if err != nil {
		if errors.Is(err, dbmove.ErrPrints) {
			return nil, apperr.New(apperr.NotConfigured, "This computer's fingerprints of the move can't be read any more, so it can't look at the old database again.")
		}
		if k.err != nil {
			return nil, apperr.Wrap(apperr.Database, k.err, "MetaRush couldn't keep what it found in the old database.")
		}
		return nil, apperr.New(apperr.Database, "MetaRush couldn't read the old database to look at it again.").
			WithDetails(map[string]any{"problem": scrubAddresses(err.Error())})
	}
	if res.Emptied {
		// One finding, in place of one per record; what was found before stays
		// (its copies may be all that is left of it).
		if err := k.keep(dbmove.Late{Kind: dbmove.LateEmptied}); err != nil {
			return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't keep what it found in the old database.")
		}
		if k.newly > 0 {
			tellOwnersLate(ctx, a, db, rec, true)
		}
		return &lookOutcome{found: k.newly, gone: true, pass: pass, readsAll: rec.ReadsAll}, nil
	}
	pass.Done = append(pass.Done, res.Done...)
	pass.More += k.more
	pass.Looks++
	o := &lookOutcome{found: k.newly, more: pass.More, partial: res.Partial, readsAll: !res.ServerHashes}
	if res.Partial {
		pass.Position = res.Position
		o.stopped = res.Stopped
		done := map[string]bool{}
		for _, n := range pass.Done {
			done[n] = true
		}
		for _, n := range res.Names {
			if !done[n] {
				o.unread = append(o.unread, n)
			}
		}
	} else {
		pass.Position = nil
		o.complete = pass.More == 0
		// The pass saw everything: what was found before and not in it is as
		// it was at the move again (or gone again): nothing to carry over.
		r, err := db.C(store.CLateWrites).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "move": rec.ID, "state": LateWaiting, "seenBy": bson.M{"$ne": pass.ID}},
			bson.M{"$set": bson.M{"state": LateSettled, "settledAt": now, "kept": false}, "$unset": bson.M{"old": ""}})
		if err == nil {
			o.settled = int(r.ModifiedCount)
		}
	}
	o.pass = pass
	rec.More = pass.More
	switch {
	case k.newly > 0:
		tellOwnersLate(ctx, a, db, rec, true)
	case o.settled > 0:
		tellOwnersLate(ctx, a, db, rec, false)
	}
	return o, nil
}

// keeper keeps what a look finds, one record at a time.
type keeper struct {
	ctx   context.Context
	a     *app.App
	db    *store.DB
	look  *lateLookRec
	now   time.Time
	id    string // this look's mark on what it finds
	names map[string]string
	// room is how many records found for the first time it may still keep;
	// newly how many it kept, more how many it couldn't.
	room, newly, more int
	err               error
}

func (k *keeper) keep(x dbmove.Late) error {
	isNew, err := keepLate(k.ctx, k.a, k.db, x, k)
	if err != nil {
		k.err = err
		return err
	}
	if isNew {
		k.newly++
	}
	return nil
}

// lateWriteID names a record found for a move (the same record found again
// keeps its name).
func lateWriteID(move, coll string, key []byte) string {
	h := sha256.Sum256([]byte(move + "\x00" + coll + "\x00" + string(key)))
	return "lwr_" + hex.EncodeToString(h[:])[:26]
}

// lateGroupOf says which group a record belongs to ("" none): from the old
// database's version, or, for one gone from it, this database's.
func lateGroupOf(ctx context.Context, db *store.DB, coll string, doc bson.Raw, key []byte) string {
	g, ok := lateGroupBy[coll]
	if !ok {
		return ""
	}
	if doc == nil {
		var here bson.Raw
		if db.C(coll).FindOne(ctx, bson.M{"_id": dbmove.IDOf(key)}, options.FindOne().SetProjection(bson.M{g.field: 1})).Decode(&here) != nil {
			return ""
		}
		doc = here
	}
	v, err := doc.LookupErr(g.field)
	if err != nil {
		return ""
	}
	s, ok := v.StringValueOK()
	if !ok {
		s = v.String()
	}
	return coll + "|" + g.field + "=" + s
}

// keepLate keeps one record a look found: new ones numbered in order (while
// the look and the move have room), one found before updated when the old
// database's version changed again (and waiting for a person again when one
// had decided, or it had settled). It says whether it kept one for the first
// time.
func keepLate(ctx context.Context, a *app.App, db *store.DB, x dbmove.Late, k *keeper) (bool, error) {
	look, now := k.look, k.now
	id := lateWriteID(look.ID, x.Collection, x.Key)
	r := lateWriteRec{ID: id, WorkspaceID: a.WorkspaceID(), Move: look.ID, Collection: x.Collection, Key: x.Key, Kind: x.Kind, Count: x.Count,
		Print: x.Print, MovePrint: x.MovePrint, FoundAt: now, SeenAt: now, SeenBy: k.id, State: LateWaiting,
		ReportOnly: wsarchive.ReportOnly(x.Collection) || x.Kind == dbmove.LateKindGone || x.Kind == dbmove.LateEmptied}
	switch {
	case r.ReportOnly:
		r.Key = nil // sign-in secrets: no copy, and not even its ID
	case len(x.Doc) > lateKeepMax:
		r.TooLarge = true
	case x.Doc != nil:
		r.Old, r.Kept, r.KeptAt = x.Doc, true, &now
	}
	r.At = lateWhen(x, look.MovedAt)
	r.Computer, r.ComputerName = lateComputer(x.Doc, k.names)
	var cur lateWriteRec
	err := db.C(store.CLateWrites).FindOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}).Decode(&cur)
	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		if k.room <= 0 && x.Kind != dbmove.LateEmptied && x.Kind != dbmove.LateKindGone {
			k.more++
			return false, nil
		}
		r.Group = lateGroupOf(ctx, db, x.Collection, x.Doc, x.Key)
		var lk lateLookRec
		if err := db.C(store.CLateLooks).FindOneAndUpdate(ctx, bson.M{"_id": look.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$inc": bson.M{"found": 1}},
			options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&lk); err != nil {
			return false, err
		}
		r.N = lk.Found
		if _, err := db.C(store.CLateWrites).InsertOne(ctx, r); err != nil {
			if store.IsDuplicateKey(err) { // another look kept it first
				return false, nil
			}
			return false, err
		}
		k.room--
		return true, nil
	case err != nil:
		return false, err
	}
	unsetCarrying := false
	same := cur.Kind == r.Kind && cur.Print == r.Print
	if same && cur.State != LateSettled {
		// As found before: waiting still, being carried over, or decided on
		// this very version.
		_, err := db.C(store.CLateWrites).UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"seenAt": now, "seenBy": k.id}})
		return false, err
	}
	if cur.State == LateCarrying {
		// A person is carrying the version found before over right now: the
		// carry-over's own check (on the fingerprint) can't finish it, so it
		// waits again with this version.
		cur.State = LateWaiting
		unsetCarrying = true
	}
	set := bson.M{"kind": r.Kind, "print": r.Print, "seenAt": now, "seenBy": k.id, "at": r.At, "computer": r.Computer, "computerName": r.ComputerName,
		"tooLarge": r.TooLarge, "kept": r.Kept, "keptAt": r.KeptAt, "copyDropped": false, "state": LateWaiting}
	unset := bson.M{"settledAt": ""}
	if unsetCarrying {
		unset["carryingAt"] = ""
	}
	if r.Old != nil {
		set["old"] = r.Old
	} else {
		unset["old"] = ""
	}
	again := cur.State != LateWaiting
	if again {
		set["again"] = true
	}
	if _, err := db.C(store.CLateWrites).UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": set, "$unset": unset}); err != nil {
		return false, err
	}
	return again, nil
}

// lateWhen is when a record was written, as it says itself: its newest time
// of change (updatedAt, at, createdAt) after the move, else, for a new one,
// the time in its ID; nil when it doesn't say.
func lateWhen(x dbmove.Late, movedAt time.Time) *time.Time {
	if x.Doc == nil {
		return nil
	}
	var best time.Time
	for _, f := range []string{"updatedAt", "at", "createdAt", "lastUsedAt", "lastSeenAt"} {
		if v, err := x.Doc.LookupErr(f); err == nil && v.Type == bson.TypeDateTime {
			if t := v.Time().UTC(); t.After(best) {
				best = t
			}
		}
	}
	if !best.IsZero() && !best.Before(movedAt.Add(-2*time.Minute)) {
		return &best
	}
	if x.Kind == dbmove.LateNew {
		if s, ok := x.ID.StringValueOK(); ok {
			if i := strings.LastIndexByte(s, '_'); i >= 0 && len(s)-i-1 == 26 {
				if u, err := ulid.ParseStrict(strings.ToUpper(s[i+1:])); err == nil {
					t := ulid.Time(u.Time()).UTC()
					if !t.Before(movedAt.Add(-2 * time.Minute)) {
						return &t
					}
				}
			}
		}
	}
	return nil
}

// lateComputer is the computer a record says it was written on ("" when it
// doesn't): its own installation, a lease's or an actor's.
func lateComputer(doc bson.Raw, names map[string]string) (string, string) {
	if doc == nil {
		return "", ""
	}
	for _, path := range [][]string{{"installationId"}, {"installation"}, {"actor", "installationId"}, {"lease", "owner"}, {"by", "installationId"}} {
		v, err := doc.LookupErr(path...)
		if err != nil {
			continue
		}
		if s, ok := v.StringValueOK(); ok && s != "" {
			return s, firstNonEmpty(names[s], "")
		}
	}
	if v, err := doc.LookupErr("lease", "ownerName"); err == nil {
		if s, ok := v.StringValueOK(); ok && s != "" {
			return "", s
		}
	}
	return "", ""
}

// computerNames are the workspace's computers by ID, as db knows them.
func computerNames(ctx context.Context, a *app.App, db *store.DB) map[string]string {
	var insts []models.Installation
	_ = db.Find(ctx, store.CInstallations, a.WorkspaceID(), bson.M{}, &insts)
	out := map[string]string{}
	for _, in := range insts {
		out[in.ID] = firstNonEmpty(in.Name, in.Hostname, in.ID)
	}
	return out
}

// tellOwnersLate says what was written late on every owner's bell, one note
// per move: loud when something is new (it rings again), else quietly brought
// up to date (settled or decided: it doesn't ring, and it stops asking).
func tellOwnersLate(ctx context.Context, a *app.App, db *store.DB, look *lateLookRec, loud bool) {
	var waiting []lateWriteRec
	_ = db.Find(ctx, store.CLateWrites, a.WorkspaceID(), bson.M{"move": look.ID, "state": LateWaiting}, &waiting,
		options.Find().SetSort(bson.D{{Key: "n", Value: 1}}).SetProjection(bson.M{"old": 0}))
	entries := collapse(waiting)
	group := "late_writes:" + look.ID
	title := "Something was written into the old database after the move"
	if len(entries) == 0 {
		quietLateBell(ctx, a, db, group, "info", "Nothing from the old database waits for you any more",
			"What MetaRush found in the old database after the move was decided, or a later look found it as it was at the move again.")
		return
	}
	var says []string
	for i, e := range entries {
		if i == 3 {
			says = append(says, plural(len(entries)-3, "more thing"))
			break
		}
		says = append(says, lateShort(e.rec, e.size))
	}
	body := fmt.Sprintf("After the workspace moved from %s, %s written there: %s.", look.From,
		map[bool]string{true: "something was", false: plural(len(entries), "thing") + " were"}[len(entries) == 1], strings.Join(says, "; "))
	if look.More > 0 {
		body += " " + lateMoreWords(look, look.Found >= lateKeepPerMove)
	}
	body += " Nobody would see it in this database: carry it over, or leave it, in Settings → Storage → Move the database."
	if !loud {
		quietLateBell(ctx, a, db, group, "warning", title, body)
		return
	}
	tellLateOwners(ctx, a, db, models.Notification{Kind: models.NotifyLateWrites, GroupKey: group,
		Tone: "warning", Title: title, Body: body, Link: "/settings/storage#database",
		Details: map[string]any{"move": look.ID, "waiting": len(entries)}})
}

// tellOwnersIncomplete says on every owner's bell, one note per move, that
// the looks couldn't check everything: what wasn't read and what to do.
func tellOwnersIncomplete(ctx context.Context, a *app.App, db *store.DB, move string) {
	v := lateLookView(ctx, a, db, move)
	if v == nil || v.Checked == LateCheckedEverything || v.Checked == LateCheckedGone {
		return
	}
	tellLateOwners(ctx, a, db, models.Notification{Kind: models.NotifyLateWrites, GroupKey: "late_incomplete:" + move,
		Tone: "warning", Title: "MetaRush couldn't check everything in the old database", Body: v.Summary, Link: "/settings/storage#database",
		Details: map[string]any{"move": move, "unread": len(v.Unread)}})
}

func tellLateOwners(ctx context.Context, a *app.App, db *store.DB, n models.Notification) {
	b := *a
	b.DB = db
	var owners []models.User
	_ = db.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"role": models.RoleOwner}, &owners)
	if len(owners) == 0 && a.Cfg.User.ID != "" {
		owners = append(owners, models.User{ID: a.Cfg.User.ID})
	}
	for _, o := range owners {
		n.UserID = o.ID
		_, _ = notify.Upsert(ctx, &b, n, notify.Refresh)
	}
}

// quietLateBell brings a note up to date without ringing again (nothing when
// there is none).
func quietLateBell(ctx context.Context, a *app.App, db *store.DB, group, tone, title, body string) {
	_, _ = db.C(store.CNotifications).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "groupKey": group, "kind": models.NotifyLateWrites},
		bson.M{"$set": bson.M{"title": title, "body": body, "tone": tone, "updatedAt": time.Now().UTC()}})
}

// collapsed is one entry of the list: a record, or the first of a group with
// how many records the group has in that state.
type collapsed struct {
	rec  lateWriteRec
	size int
}

// collapse lists records found, a group (in one state) as one entry, in the
// order they were found.
func collapse(recs []lateWriteRec) []collapsed {
	var out []collapsed
	at := map[string]int{}
	for _, r := range recs {
		if r.Group != "" {
			key := r.Move + "\x00" + r.Group + "\x00" + r.State
			if i, ok := at[key]; ok {
				out[i].size++
				if out[i].rec.Kind != r.Kind {
					out[i].rec.Mixed = true
				}
				continue
			}
			at[key] = len(out)
		}
		out = append(out, collapsed{rec: r, size: 1})
	}
	return out
}

/* ---------- in words ---------- */

// lateNouns say each kind of record the way people do (a record of a kind
// not listed is "a record").
var lateNouns = map[string]string{
	store.CWorkspaces: "the workspace's own settings", store.CInstallations: "a computer's record", store.CUsers: "a person",
	store.CProjects: "a project", store.CAssets: "a clip", store.CIngest: "an upload", store.CSegments: "a line of a transcript",
	store.CInsights: "what MetaRush learned from a clip", store.CRevisions: "a version of a cut", store.CJobs: "a piece of work",
	store.CRenders: "a render", store.CComments: "a comment", store.CAudit: "an entry of the change history",
	store.CMigrations: "an update of the database", store.CQAReports: "a quality check", store.CVoiceTakes: "a narration take",
	store.CNotifications: "a notification", store.CAssistantConversations: "a Claude Code conversation", store.CMotion: "a motion graphic",
	store.CResources: "a picture or another resource", store.CLinkedFolders: "a linked folder", store.CProjectLinks: "a link between projects",
	store.CCollections: "a collection", store.CConversations: "a call recording", store.CGenerations: "AI work",
	store.CLooks: "a look", store.CConsents: "a consent record", store.CGenSpend: "what was spent on AI work",
	store.CRemotionSources: "a Remotion project", store.CRemotionVersions: "a version of a Remotion project", store.CRemotionRenders: "a Remotion render",
	store.CRemotionRefreshes: "a Remotion refresh", store.CRemotionPushes: "a Remotion push", store.CAbilities: "an ability",
	store.CAbilitySetups: "an ability's setup", store.CAbilityRuns: "a run of an ability", store.CUserCredentials: "a password",
	store.CAuthTokens: "an invitation or password-reset link", store.CAuthFailures: "a count of wrong passwords", store.CTranscriptFixes: "a fix to a transcript",
	store.CTranscriptOriginals: "a transcript as the machine wrote it", store.CTranscriptSnapshots: "the words of a saved version",
	store.CSpelling: "a spelling list entry", store.CCaptionTranslations: "a caption in another language", store.CAutoSpending: "what Auto spent",
	store.CKeep: "something a project kept", store.CSourceCopies: "a fixed copy of footage", store.CContentSettings: "Content Settings",
	store.CContentDefaults: "which Content Settings new projects use",
	store.CRecipes:         "a recipe (Recipes)",
	store.CRecipeVersions:  "a version of a recipe (Recipes)",
	store.CRecipeRuns:      "a run of a recipe (Recipes)",
	store.CDrafts:          "a draft from an AI app", store.CPresenters: "a presenter", store.CTrainingOptOuts: "a training opt-out",
	store.CRecordings: "a screen recording", store.CSpokenChanges: "a changed spoken word", store.CReviewLinks: "a review link",
	store.CHandovers: "a handover between computers", store.CLateLooks: "a look at an old database", store.CLateWrites: "a record found written late",
	store.CLogEntries: "an activity log entry", store.CImprovementItems: "an item of the weekly report", store.CImprovementReports: "a weekly report",
	store.CSpeakers: "the voices heard in a clip", store.CPeople: "a named person", store.CTodos: "a to-do", store.CFaces: "a face",
	"catch_ups": "a catch-up step", "sound_cache": "a clearer voice",
}

func lateNoun(coll string) string { return firstNonEmpty(lateNouns[coll], "a record") }

// lateReportWords say what to do about a record of a kind that is only
// reported (sign-in secrets, and what MetaRush doesn't carry over).
var lateReportWords = map[string]string{
	store.CUserCredentials: "MetaRush never carries passwords over: whoever set or changed theirs then sets it again here (Settings → People).",
	store.CAuthTokens:      "MetaRush never carries sign-in links over, and it doesn't work here: send the invitation or the reset again here (Settings → People).",
	store.CIngest:          "An upload's sign-in to the storage is never carried over: add the file again here.",
}

func lateReportAdvice(coll string) string {
	return firstNonEmpty(lateReportWords[coll], "MetaRush doesn't carry this kind of record over: it is about the old database only. Do it again here if it still matters.")
}

// lateTime says a time the way people read it on this computer.
func lateTime(t time.Time) string { return t.Local().Format("2 Jan 15:04") }

// lateShort is a record found (or its group), in a few words ("a comment,
// changed about 2 Nov 14:05 on Edit laptop").
func lateShort(r lateWriteRec, size int) string {
	switch r.Kind {
	case dbmove.LateEmptied:
		return "the old database itself, gone or empty"
	case dbmove.LateKindGone:
		return fmt.Sprintf("all %s records of one kind (%s), removed", thousands(int(r.Count)), lateNoun(r.Collection))
	}
	what := lateNoun(r.Collection)
	if size > 1 {
		what = fmt.Sprintf("%d records of %s", size, lateGroupBy[r.Collection].what)
	}
	kind := r.Kind
	if r.Mixed {
		kind = dbmove.LateChanged
	}
	s := what + ", " + map[string]string{dbmove.LateNew: "added", dbmove.LateChanged: "changed", dbmove.LateGone: "removed"}[kind]
	if r.At != nil {
		s += " about " + lateTime(*r.At)
	}
	if r.ComputerName != "" {
		s += " on " + r.ComputerName
	}
	return s
}

// lateSays is a record found (or its group of size records), as a sentence.
func lateSays(r lateWriteRec, size int, copyChanged bool) string {
	switch r.Kind {
	case dbmove.LateEmptied:
		s := "The old database is gone or empty: every record it held is no longer there. If you removed it yourself, there is nothing to do; MetaRush removes nothing here because of it."
		return s + lateDecidedWords(r)
	case dbmove.LateKindGone:
		s := fmt.Sprintf("All %s records of one kind (%s) were removed from the old database after the move. They are still here: MetaRush never removes them by itself.", thousands(int(r.Count)), lateNoun(r.Collection))
		return s + lateDecidedWords(r)
	}
	what, verb := upperFirstWord(lateNoun(r.Collection)), "was"
	if size > 1 {
		what, verb = fmt.Sprintf("%d records of %s", size, lateGroupBy[r.Collection].what), "were"
	}
	kind := r.Kind
	if r.Mixed {
		kind = dbmove.LateChanged
	}
	var s string
	switch kind {
	case dbmove.LateNew:
		s = what + " " + verb + " added to the old database after the move"
	case dbmove.LateChanged:
		s = what + " " + verb + " changed in the old database after the move"
	default:
		s = what + " " + verb + " removed from the old database after the move"
	}
	if r.At != nil {
		s += ", about " + lateTime(*r.At)
	}
	if r.ComputerName != "" {
		s += ", on " + r.ComputerName
	}
	s += "."
	switch {
	case r.State == LateCarried:
		s += " It was carried over."
	case r.State == LateLeft:
		s += " It was left as it is here."
	case r.State == LateSettled:
		s += " A later look found it as it was at the move again: there is nothing to carry over."
	case r.ReportOnly:
		s += " " + lateReportAdvice(r.Collection)
	case r.Kind == dbmove.LateGone:
		s += " It is still here: MetaRush never removes it by itself."
	case r.CopyDropped:
		s += fmt.Sprintf(" MetaRush kept a copy for %d days for someone to decide, then let it go: it can't be carried over any more.", lateKeepDays())
	case r.TooLarge:
		s += " It is too large for MetaRush to keep a copy to carry over."
	case copyChanged && r.Kind == dbmove.LateNew:
		s += " This database has a different record in its place: compare both before you decide."
	case copyChanged:
		s += " It was changed here too since the move: compare both before you decide."
	}
	return s
}

func lateKeepDays() int { return int(LateCopyKeep / (24 * time.Hour)) }

// lateDecidedWords says what became of a finding that is only reported.
func lateDecidedWords(r lateWriteRec) string {
	switch r.State {
	case LateLeft:
		return " Seen."
	case LateSettled:
		return " A later look found otherwise."
	}
	return ""
}

// lateKindWords says which kinds of record weren't read ("3 kinds of record,
// such as a project and a comment").
func lateKindWords(colls []string) string {
	if len(colls) == 0 {
		return "everything"
	}
	var eg []string
	for _, c := range colls {
		if len(eg) == 3 {
			break
		}
		eg = append(eg, lateNoun(c))
	}
	if len(colls) == 1 {
		return "one kind of record (" + eg[0] + ")"
	}
	return fmt.Sprintf("%d kinds of record, such as %s", len(colls), joinAnd(eg))
}

// lateMoreWords says what became of records found but not kept: never that a
// next look lists them when none can.
func lateMoreWords(r *lateLookRec, atCap bool) string {
	n := upperFirstWord(plural(r.More, "more record"))
	switch {
	case r.Ended:
		return n + " found were only counted: MetaRush no longer looks, so they stay only in the old database."
	case atCap:
		return fmt.Sprintf("%s found were only counted: MetaRush keeps at most %s for one move, so they stay only in the old database.", n, thousands(lateKeepPerMove))
	}
	return n + " found but not listed yet: the next look lists them."
}

/* ---------- the views ---------- */

// LateWriteView is a record written into the old database after the move,
// or a group of them (the same clip's transcript…) as one.
type LateWriteView struct {
	// N is its number in the list (carry it over or leave it by number).
	N    int    `json:"n"`
	ID   string `json:"id"`
	What string `json:"what" jsonschema:"description=The kind of record\\, in words: a comment\\, a clip\\, a version of a cut."`
	Kind string `json:"kind" jsonschema:"enum=new,enum=changed,enum=gone,enum=kind-gone,enum=emptied"`
	// GroupSize is how many records it stands for (1: just itself);
	// GroupWhat says what they have in common.
	GroupSize int    `json:"groupSize"`
	GroupWhat string `json:"groupWhat,omitempty"`
	// At is when it was written, as the record says.
	At       *time.Time `json:"at,omitempty"`
	Computer string     `json:"computer,omitempty" jsonschema:"description=The computer it was written on\\, when the record says."`
	FoundAt  time.Time  `json:"foundAt"`
	State    string     `json:"state" jsonschema:"enum=waiting,enum=carrying,enum=carried,enum=left,enum=settled"`
	// ReportOnly: a kind that holds sign-in secrets (or that MetaRush
	// doesn't carry over): never shown or carried over, only reported.
	ReportOnly bool `json:"reportOnly"`
	// CopyChanged: this database's record changed since the move too (or,
	// for a new one, this database has one in its place): carrying over is
	// refused and both versions are shown.
	CopyChanged bool `json:"copyChanged"`
	// CanCarry: carrying it over would go ahead now (for a group: its first
	// record; carrying it checks every one).
	CanCarry bool `json:"canCarry"`
	// CopyUntil: when MetaRush lets its copy go if nobody decided;
	// CopyDropped: it did.
	CopyUntil   *time.Time `json:"copyUntil,omitempty"`
	CopyDropped bool       `json:"copyDropped"`
	Says        string     `json:"says"`
	// Collection and Record say where it is (Details).
	Collection string     `json:"collection"`
	Record     string     `json:"record"`
	Move       string     `json:"move"`
	DecidedBy  string     `json:"decidedBy,omitempty"`
	DecidedAt  *time.Time `json:"decidedAt,omitempty"`
}

// How far the looks got (LateLookView.Checked).
const (
	LateCheckedEverything = "everything" // the latest attempt read every kind, and nothing is left uncounted
	LateCheckedPart       = "part"       // some was read; the latest attempt stopped, failed or left some uncounted
	LateCheckedNothing    = "nothing"    // no look could be made
	LateCheckedGone       = "gone"       // the old database is gone or empty
)

// LateLookView is a move's looks at the old database.
type LateLookView struct {
	Move    string    `json:"move"`
	From    string    `json:"from" jsonschema:"description=The old database\\, in words."`
	MovedAt time.Time `json:"movedAt"`
	// Computer looks; Here: this computer (it keeps the old database's address).
	Computer string          `json:"computer"`
	Here     bool            `json:"here"`
	Looks    []LateLookTaken `json:"looks"`
	Next     *time.Time      `json:"next,omitempty"`
	Last     *time.Time      `json:"last,omitempty" jsonschema:"description=When the last look is due."`
	Ended    bool            `json:"ended"`
	EndedWhy string          `json:"endedWhy,omitempty"`
	Problem  string          `json:"problem,omitempty"`
	Found    int             `json:"found"`
	Waiting  int             `json:"waiting"`
	// Checked says how far the looks got; AllClear is the one a script can
	// trust: the looks are over, their last one read everything, nothing was
	// left uncounted and nothing waits for a person. Anything else is not an
	// all-clear, however little was found.
	Checked  string `json:"checked" jsonschema:"enum=everything,enum=part,enum=nothing,enum=gone"`
	AllClear bool   `json:"allClear"`
	// CompleteAt is when a look last finished reading everything (CompleteFrom
	// when that way through began: it can take several looks).
	CompleteAt   *time.Time `json:"completeAt,omitempty"`
	CompleteFrom *time.Time `json:"completeFrom,omitempty"`
	// Unread are the kinds of record not read yet (Details).
	Unread []string `json:"unread"`
	// CanLook: a look can be made now, from where this is asked: this computer
	// keeps the old database's address and the move's fingerprints.
	CanLook bool `json:"canLook"`
	// More: found and not kept; AtLimit: the move keeps no more, so they stay
	// only counted. Partial: the last look stopped before it saw everything
	// (Stopped says why); ReadsAll: every look reads the old database whole
	// (its server doesn't say what changed).
	More     int    `json:"more"`
	AtLimit  bool   `json:"atLimit"`
	Partial  bool   `json:"partial"`
	Stopped  string `json:"stopped,omitempty"`
	ReadsAll bool   `json:"readsAll"`
	// CopyDays is how long a copy of a record nobody decided is kept.
	CopyDays int    `json:"copyDays"`
	Summary  string `json:"summary"`
}

func readLateLook(ctx context.Context, a *app.App, db *store.DB, move string) (*lateLookRec, error) {
	var r lateLookRec
	if err := db.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move, "workspaceId": a.WorkspaceID()}).Decode(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

func lateLookView(ctx context.Context, a *app.App, db *store.DB, move string) *LateLookView {
	r, err := readLateLook(ctx, a, db, move)
	if err != nil {
		return nil
	}
	return viewOfLook(ctx, a, db, r)
}

func viewOfLook(ctx context.Context, a *app.App, db *store.DB, r *lateLookRec) *LateLookView {
	v := &LateLookView{Move: r.ID, From: r.From, MovedAt: r.MovedAt, Computer: r.ComputerName, Here: r.Computer == a.Cfg.Installation.ID,
		Looks: append([]LateLookTaken{}, r.Looks...), Ended: r.Ended, EndedWhy: r.EndedWhy, Problem: r.Problem, Found: r.Found,
		More: r.More, AtLimit: r.Found >= lateKeepPerMove, Partial: r.Partial, Stopped: r.Stopped, ReadsAll: r.ReadsAll, CopyDays: lateKeepDays(),
		CompleteAt: r.CompleteAt, CompleteFrom: r.CompleteFrom, Unread: append([]string{}, r.Unread...)}
	var waiting []lateWriteRec
	_ = db.Find(ctx, store.CLateWrites, a.WorkspaceID(), bson.M{"move": r.ID, "state": LateWaiting}, &waiting,
		options.Find().SetProjection(bson.M{"old": 0, "key": 0}))
	v.Waiting = len(collapse(waiting))
	if len(r.Due) > 0 {
		last := r.Due[len(r.Due)-1]
		v.Last = &last
		if !r.Ended && r.Done < len(r.Due) {
			next := r.Due[r.Done]
			v.Next = &next
		}
	}
	switch {
	case r.Gone:
		v.Checked = LateCheckedGone
	case r.Complete:
		v.Checked = LateCheckedEverything
	case len(r.Looks) > 0:
		v.Checked = LateCheckedPart
	default:
		v.Checked = LateCheckedNothing
	}
	v.AllClear = r.Ended && v.Checked == LateCheckedEverything && v.Waiting == 0 && r.More == 0
	if v.Here && !r.Ended && KeepsLookAddress(a.Cfg, r.ID) {
		if _, err := os.Stat(latePrintsFile(a, r.ID)); err == nil {
			v.CanLook = true
		}
	}
	v.Summary = lateSummary(r, v)
	return v
}

// lateSummary says where the looks stand. "Nothing was written" comes only
// after a look that read everything.
func lateSummary(r *lateLookRec, v *LateLookView) string {
	who := firstNonEmpty(r.ComputerName, "the computer that moved it")
	var s string
	switch {
	case r.Gone:
		s = fmt.Sprintf("The old database (%s) is gone or empty, so MetaRush stopped looking at it.", r.From)
		if r.CompleteAt != nil {
			s += " It last read all of it " + lateTime(*r.CompleteAt) + "."
		}
	case v.Waiting == 1:
		s = fmt.Sprintf("Something was written into the old database (%s) after the move: carry it over, or leave it as it is here.", r.From)
	case v.Waiting > 1:
		s = fmt.Sprintf("%s were written into the old database (%s) after the move: carry each over, or leave it as it is here.", upperFirstWord(plural(v.Waiting, "thing")), r.From)
	case v.Checked != LateCheckedEverything:
	case r.Ended && r.Found == 0:
		s = fmt.Sprintf("Nothing was written into the old database (%s) after the move: MetaRush read all of it for the last time and no longer keeps its address.", r.From)
	case r.Ended:
		s = fmt.Sprintf("MetaRush read all of the old database (%s) for the last time; everything it found was decided or settled.", r.From)
	case r.Found == 0:
		s = fmt.Sprintf("Nothing was written into the old database (%s) after the move so far: MetaRush read all of it. %s looks again %s.", r.From, who, nextWords(v.Next))
	default:
		s = fmt.Sprintf("Everything found in the old database (%s) after the move was decided or settled. %s looks again %s.", r.From, who, nextWords(v.Next))
	}
	if !r.Gone && v.Checked != LateCheckedEverything {
		s = strings.TrimSpace(s + " " + lateIncompleteWords(r, v, who))
	}
	if r.More > 0 {
		s += " " + lateMoreWords(r, v.AtLimit)
	}
	if r.ReadsAll && !r.Ended {
		s += " The old database's server doesn't say by itself what changed, so each look reads all of it again."
	}
	if v.CompleteAt != nil && !r.Gone && v.CompleteFrom != nil && v.CompleteAt.Sub(*v.CompleteFrom) > time.Minute && v.Checked == LateCheckedEverything {
		s += " It took several looks: what it read first was read " + lateTime(*v.CompleteFrom) + "."
	}
	return s
}

// lateIncompleteWords says that the looks didn't check everything, what they
// didn't read, and what can be done: never to look again where nobody can.
func lateIncompleteWords(r *lateLookRec, v *LateLookView, who string) string {
	s := "MetaRush couldn't check everything in the old database (" + r.From + ")"
	if !r.Ended && v.Next != nil {
		s = "MetaRush hasn't been able to check everything in the old database (" + r.From + ") yet"
	}
	switch {
	case r.Problem != "" && len(r.Looks) == 0:
		s += ": no look could be made (" + lowerFirst(strings.TrimSuffix(r.Problem, ".")) + ")"
	case r.Problem != "":
		s += ": its last look couldn't be made (" + lowerFirst(strings.TrimSuffix(r.Problem, ".")) + ")"
	case r.Partial:
		s += ": the last look stopped part-way, " + r.Stopped + ", and didn't get to " + lateKindWords(r.Unread)
	case r.More > 0:
		s += ": it found more than it could keep"
	case len(r.Looks) == 0:
		s += ": it hasn't looked yet"
	}
	s += "."
	if r.CompleteAt != nil {
		s += " It last read all of it " + lateTime(*r.CompleteAt) + "."
	}
	switch {
	case r.Ended:
		s += " MetaRush no longer keeps its address, so it can't look again: if something may have been written there, keep the old database and look in it yourself before you remove it."
	case v.CanLook && r.Partial:
		s += " Look again to go on from where it stopped."
	case v.CanLook && r.More > 0 && v.AtLimit:
	case v.CanLook && r.More > 0:
		s += " Look again to list more."
	case v.CanLook:
		s += " Look again once it can be reached."
	case !v.Here:
		s += " " + who + " keeps its address: look again there."
	default:
		s += " This computer no longer keeps what it needs to look: keep the old database and look in it yourself before you remove it."
	}
	if v.Next != nil {
		s += " " + who + " looks again " + nextWords(v.Next) + "."
	}
	return s
}

// nextWords says when the next look is.
func nextWords(t *time.Time) string {
	if t == nil {
		return "when asked"
	}
	d := time.Until(*t)
	switch {
	case d < -time.Hour:
		return "when MetaRush runs there"
	case d <= time.Minute:
		return "in a moment"
	case d < 90*time.Minute:
		return fmt.Sprintf("in about %s", plural(int(d.Minutes()+0.5), "minute"))
	case d < 36*time.Hour:
		return fmt.Sprintf("in about %s", plural(int(d.Hours()+0.5), "hour"))
	}
	return "at " + lateTime(*t)
}

// lateRecordWords is a record's _id for Details.
func lateRecordWords(key []byte) string {
	if len(key) == 0 {
		return ""
	}
	v := dbmove.IDOf(key)
	if s, ok := v.StringValueOK(); ok {
		return s
	}
	b, err := bson.MarshalExtJSON(bson.D{{Key: "_id", Value: v}}, false, false)
	if err != nil {
		return v.String()
	}
	return strings.TrimSuffix(strings.TrimPrefix(string(b), `{"_id":`), "}")
}

// copyState says whether this database's record is still the one the move
// copied: for a new record, whether one with its ID is here; for a changed
// or gone one, whether the one here still has its fingerprint from the move.
// It returns the record here (nil when there is none).
func copyState(ctx context.Context, a *app.App, r lateWriteRec) (bson.Raw, bool, error) {
	if r.ReportOnly {
		return nil, false, nil // never read: only reported
	}
	var cur bson.Raw
	err := a.DB.C(r.Collection).FindOne(ctx, bson.M{"_id": dbmove.IDOf(r.Key)}).Decode(&cur)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, r.Kind != dbmove.LateNew, nil // gone here: changed since the move (but for a new one)
	}
	if err != nil {
		return nil, false, err
	}
	return cur, copyChangedFrom(r, cur), nil
}

func copyChangedFrom(r lateWriteRec, cur bson.Raw) bool {
	if cur == nil {
		return r.Kind != dbmove.LateNew
	}
	if r.Kind == dbmove.LateNew {
		p, err := dbmove.PrintOf(cur)
		return err != nil || p != r.Print
	}
	p, err := dbmove.PrintOf(cur)
	if err != nil {
		return true
	}
	return p != r.MovePrint && p != r.Print // the very version found is no conflict: it went over already
}

// copiesChanged says, for many records at once (a page of the list), whether
// this database's record changed since the move: one query per kind.
func copiesChanged(ctx context.Context, a *app.App, recs []lateWriteRec) map[string]bool {
	out := map[string]bool{}
	byColl := map[string][]lateWriteRec{}
	for _, r := range recs {
		byColl[r.Collection] = append(byColl[r.Collection], r)
	}
	for coll, rs := range byColl {
		ids := bson.A{}
		for _, r := range rs {
			ids = append(ids, dbmove.IDOf(r.Key))
		}
		here := map[string]bson.Raw{}
		cur, err := a.DB.C(coll).Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
		if err != nil {
			continue
		}
		for cur.Next(ctx) {
			if k, err := cur.Current.LookupErr("_id"); err == nil {
				here[string(append([]byte{byte(k.Type)}, k.Value...))] = bson.Raw(append([]byte(nil), cur.Current...))
			}
		}
		_ = cur.Close(ctx)
		for _, r := range rs {
			out[r.ID] = copyChangedFrom(r, here[string(r.Key)])
		}
	}
	return out
}

func viewOfLate(r lateWriteRec, size int, changed bool) LateWriteView {
	kind := r.Kind
	if r.Mixed {
		kind = dbmove.LateChanged
	}
	v := LateWriteView{N: r.N, ID: r.ID, What: lateNoun(r.Collection), Kind: kind, GroupSize: size, At: r.At, Computer: r.ComputerName, FoundAt: r.FoundAt,
		State: r.State, ReportOnly: r.ReportOnly, CopyDropped: r.CopyDropped, Collection: r.Collection, Record: lateRecordWords(r.Key), Move: r.Move,
		DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt}
	if v.GroupSize < 1 {
		v.GroupSize = 1
	}
	if r.Kind == dbmove.LateEmptied {
		v.What = "the old database"
	}
	if r.Group != "" {
		v.GroupWhat = lateGroupBy[r.Collection].what
	}
	if r.State == LateWaiting && !r.ReportOnly {
		v.CopyChanged = changed
		v.CanCarry = !changed && r.Kind != dbmove.LateGone && r.Kept && !r.TooLarge
		if r.Kept && r.KeptAt != nil {
			until := r.KeptAt.Add(LateCopyKeep)
			v.CopyUntil = &until
		}
	}
	v.Says = lateSays(r, v.GroupSize, v.CopyChanged)
	return v
}

/* ---------- database.late.writes ---------- */

// DatabaseLateWritesIn asks for what was written late.
type DatabaseLateWritesIn struct {
	// Look again now: on the computer that moved it, while it keeps the old
	// database's address.
	Look bool `json:"look,omitempty" jsonschema:"description=Look at the old database again now (on the computer that moved it\\, while it keeps the old database's address)."`
	// Skip and Limit page through a long list (Limit 0: 200).
	Skip  int `json:"skip,omitempty" jsonschema:"minimum=0"`
	Limit int `json:"limit,omitempty" jsonschema:"minimum=0,maximum=1000"`
}

// DatabaseLateWritesOut is what was written into the old database after a
// move into this one.
type DatabaseLateWritesOut struct {
	// Looked: a look was made now; Problem says why one couldn't be.
	Looked  bool           `json:"looked"`
	Problem string         `json:"problem,omitempty"`
	Moves   []LateLookView `json:"moves"`
	// Found is a page of the list (Total entries in all; a group counts once).
	Found   []LateWriteView `json:"found"`
	Total   int             `json:"total"`
	Waiting int             `json:"waiting"`
	// AllClear: for every move into this database, the looks are over, their
	// last one read everything, nothing is uncounted and nothing waits (false
	// with no move: nothing was looked at).
	AllClear bool     `json:"allClear"`
	Summary  string   `json:"summary"`
	Steps    []string `json:"steps"`
}

// lateLooks are the looks recorded in this database, oldest move first.
func lateLooks(ctx context.Context, a *app.App) []lateLookRec {
	var recs []lateLookRec
	_ = a.DB.Find(ctx, store.CLateLooks, a.WorkspaceID(), bson.M{}, &recs, options.Find().SetSort(bson.D{{Key: "movedAt", Value: 1}}))
	return recs
}

// lateList is every entry of the list, numbered: moves oldest first, records
// in the order found, a group (in one state) as one entry. Copies aren't read.
func lateList(ctx context.Context, a *app.App, recs []lateLookRec) []collapsed {
	// A carry-over that stopped part-way lets go of what it claimed.
	_, _ = a.DB.C(store.CLateWrites).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "state": LateCarrying, "carryingAt": bson.M{"$lt": time.Now().UTC().Add(-lateCarryStale)}},
		bson.M{"$set": bson.M{"state": LateWaiting}, "$unset": bson.M{"carryingAt": ""}})
	var found []lateWriteRec
	_ = a.DB.Find(ctx, store.CLateWrites, a.WorkspaceID(), bson.M{}, &found, options.Find().SetProjection(bson.M{"old": 0}))
	movedAt := map[string]time.Time{}
	for _, r := range recs {
		movedAt[r.ID] = r.MovedAt
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Move != found[j].Move {
			return movedAt[found[i].Move].Before(movedAt[found[j].Move])
		}
		return found[i].N < found[j].N
	})
	return collapse(found)
}

// DatabaseLateWrites is database.late.writes.
func DatabaseLateWrites(ctx context.Context, a *app.App, in *DatabaseLateWritesIn) (*DatabaseLateWritesOut, error) {
	out := &DatabaseLateWritesOut{Moves: []LateLookView{}, Found: []LateWriteView{}, Steps: []string{}}
	recs := lateLooks(ctx, a)
	if in.Look {
		var elsewhere []string
		for i := range recs {
			r := &recs[i]
			if r.Ended {
				continue
			}
			if r.Computer != a.Cfg.Installation.ID {
				elsewhere = append(elsewhere, firstNonEmpty(r.ComputerName, "the computer that moved it"))
				continue
			}
			if a.ReadOnly {
				continue
			}
			if _, err := lookAndRecord(ctx, a, a.DB, r, nil, LateAsked, time.Now().UTC()); err != nil {
				out.Problem = lateProblemWords(err)
				if e := apperr.From(err); e != nil && e.Code == apperr.NotConfigured {
					// The address or the fingerprints are gone from this computer:
					// no look could ever be made again.
					endLateLooks(ctx, a, a.DB, r.ID, out.Problem+" MetaRush stopped looking at the old database.", false)
				}
				continue
			}
			out.Looked = true
		}
		switch {
		case out.Looked || out.Problem != "":
		case len(elsewhere) > 0:
			out.Problem = fmt.Sprintf("Only %s looks at the old database: it keeps its address. Ask there.", joinAnd(dedupe(elsewhere)))
		case len(recs) > 0:
			out.Problem = "MetaRush no longer looks at the old database: it made its last look and doesn't keep its address any more."
		default:
			out.Problem = "No move into this database is being looked after."
		}
		recs = lateLooks(ctx, a)
	}
	for i := range recs {
		if v := viewOfLook(ctx, a, a.DB, &recs[i]); v != nil {
			out.Moves = append(out.Moves, *v)
		}
	}
	out.AllClear = len(out.Moves) > 0
	for _, m := range out.Moves {
		out.AllClear = out.AllClear && m.AllClear
	}
	entries := lateList(ctx, a, recs)
	out.Total = len(entries)
	for _, e := range entries {
		if e.rec.State == LateWaiting {
			out.Waiting++
		}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	skip := max(in.Skip, 0)
	var page []lateWriteRec
	var nums []int
	var sizes []int
	for i := skip; i < len(entries) && len(page) < limit; i++ {
		page, nums, sizes = append(page, entries[i].rec), append(nums, i+1), append(sizes, entries[i].size)
	}
	var waitingPage []lateWriteRec
	for _, r := range page {
		if r.State == LateWaiting && !r.ReportOnly {
			waitingPage = append(waitingPage, r)
		}
	}
	changed := copiesChanged(ctx, a, waitingPage)
	for i, r := range page {
		v := viewOfLate(r, sizes[i], changed[r.ID])
		v.N = nums[i]
		out.Found = append(out.Found, v)
	}
	switch {
	case len(out.Moves) == 0:
		out.Summary = "No move into this database is being looked after: nothing was written late."
	case out.Waiting > 0:
		out.Summary = fmt.Sprintf("%s written into the old database after the move %s for a person.", upperFirstWord(plural(out.Waiting, "thing")),
			map[bool]string{true: "waits", false: "wait"}[out.Waiting == 1])
		out.Steps = append(out.Steps, "Carry each over into this database (only while this database's own record is still the one the move copied; the records of one clip go over together: if any changed here, none does), or leave it as it is here.",
			"A record removed from the old database is never removed here by MetaRush: if it should go, remove it here yourself, then leave it.",
			fmt.Sprintf("MetaRush keeps its copy of each for %d days for someone to decide, then lets it go.", lateKeepDays()))
	default:
		out.Summary = out.Moves[len(out.Moves)-1].Summary
	}
	if len(out.Moves) > 0 {
		m := out.Moves[len(out.Moves)-1]
		if out.Waiting > 0 && m.Checked != LateCheckedEverything && m.Checked != LateCheckedGone {
			// The list's summary says what waits: this says the check isn't done.
			out.Summary += " " + m.Summary
		}
		switch {
		case !m.Partial && m.More == 0 || m.Ended:
		case m.AtLimit:
		case m.CanLook:
			out.Steps = append(out.Steps, "Not everything written late is listed yet: look again.")
		default:
			out.Steps = append(out.Steps, "Not everything written late is listed yet: look again on "+firstNonEmpty(m.Computer, "the computer that moved it")+".")
		}
	}
	if skip+len(out.Found) < out.Total {
		out.Steps = append(out.Steps, fmt.Sprintf("The list goes on: %d more after these.", out.Total-skip-len(out.Found)))
	}
	return out, nil
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func readLateWrite(ctx context.Context, a *app.App, id string) (*lateWriteRec, error) {
	var r lateWriteRec
	if err := a.DB.C(store.CLateWrites).FindOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}).Decode(&r); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperr.New(apperr.NotFound, "That record found in the old database isn't here.").
				WithHint("List what was found again.").WithCommand("metarush database late-writes")
		}
		return nil, err
	}
	return &r, nil
}

// lateRef is one entry a person named: a record (or a group's first) and its
// number in the list.
type lateRef struct {
	rec lateWriteRec
	n   int
}

// lateByRef finds entries by their numbers in the list or their IDs, or all
// that wait.
func lateByRef(ctx context.Context, a *app.App, refs []string, all bool) ([]lateRef, error) {
	entries := lateList(ctx, a, lateLooks(ctx, a))
	number := map[string]int{}
	for i, e := range entries {
		number[e.rec.ID] = i + 1
	}
	var ids []string
	if all {
		for _, e := range entries {
			if e.rec.State == LateWaiting {
				ids = append(ids, e.rec.ID)
			}
		}
	}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if n, err := strconv.Atoi(ref); err == nil {
			if n < 1 || n > len(entries) {
				return nil, apperr.New(apperr.NotFound, "There is no number %d in the list of what was written late.", n).WithCommand("metarush database late-writes")
			}
			ids = append(ids, entries[n-1].rec.ID)
			continue
		}
		ids = append(ids, ref)
	}
	if len(ids) == 0 && !all {
		return nil, invalid("Say which records found in the old database: their numbers in the list, or all of them.")
	}
	var out []lateRef
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		r, err := readLateWrite(ctx, a, id)
		if err != nil {
			return nil, err
		}
		out = append(out, lateRef{rec: *r, n: number[id]})
	}
	return out, nil
}

// lateMembers are the records an entry stands for: itself, or every record
// of its group in the same state.
func lateMembers(ctx context.Context, a *app.App, r lateWriteRec) ([]lateWriteRec, error) {
	if r.Group == "" {
		return []lateWriteRec{r}, nil
	}
	var all []lateWriteRec
	// Without their copies: a clip's whole transcript is held only one line at a time.
	if err := a.DB.Find(ctx, store.CLateWrites, a.WorkspaceID(), bson.M{"move": r.Move, "group": r.Group, "state": r.State}, &all,
		options.Find().SetSort(bson.D{{Key: "n", Value: 1}}).SetProjection(bson.M{"old": 0})); err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return []lateWriteRec{r}, nil
	}
	return all, nil
}

/* ---------- database.late.show ---------- */

// DatabaseLateShowIn names a record found.
type DatabaseLateShowIn struct {
	// ID is its ID or its number in the list.
	ID string `json:"id" jsonschema:"maxLength=64"`
}

// DatabaseLateShowOut is a record found, both versions side by side —
// without what MetaRush never shows (sign-in state, capabilities, voice and
// face prints…) and with anything that looks like a key taken out.
type DatabaseLateShowOut struct {
	Found LateWriteView `json:"found"`
	// Old is the old database's version, Here this database's (Extended
	// JSON; none when there is none, nothing kept, or a kind that holds
	// sign-in secrets).
	Old  json.RawMessage `json:"old,omitempty" jsonschema:"type=object"`
	Here json.RawMessage `json:"here,omitempty" jsonschema:"type=object"`
	// Differs names the fields that differ; Hidden the fields left out.
	Differs []string `json:"differs"`
	Hidden  []string `json:"hidden"`
	Summary string   `json:"summary"`
}

// DatabaseLateShow is database.late.show.
func DatabaseLateShow(ctx context.Context, a *app.App, in *DatabaseLateShowIn) (*DatabaseLateShowOut, error) {
	refs, err := lateByRef(ctx, a, []string{in.ID}, false)
	if err != nil {
		return nil, err
	}
	r := refs[0].rec
	members, _ := lateMembers(ctx, a, r)
	here, changed, err := copyState(ctx, a, r)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read this database's record.")
	}
	out := &DatabaseLateShowOut{Found: viewOfLate(r, len(members), changed), Differs: []string{}, Hidden: []string{}}
	out.Found.N = refs[0].n
	out.Summary = out.Found.Says
	if r.ReportOnly {
		return out, nil // sign-in secrets: nothing of either version is shown
	}
	scrub := wsarchive.NewScrubber(append(logbook.KnownSecrets(a.Cfg, abilityKeyNames(ctx, a)...), publish.Secrets(a.Cfg, workspacePeople(ctx, a)...)...)...)
	hidden := map[string]bool{}
	oldShown := lateShowable(r.Old, scrub, hidden)
	hereShown := lateShowable(here, scrub, hidden)
	if oldShown != nil {
		if b, err := bson.MarshalExtJSON(oldShown, false, false); err == nil {
			out.Old = b
		}
	}
	if hereShown != nil {
		if b, err := bson.MarshalExtJSON(hereShown, false, false); err == nil {
			out.Here = b
		}
	}
	out.Differs = differingFields(oldShown, hereShown)
	for h := range hidden {
		out.Hidden = append(out.Hidden, h)
	}
	sort.Strings(out.Hidden)
	return out, nil
}

// lateShowable is a record as a person may see it: without the fields
// MetaRush never shows (models.NeverShown, wherever they appear), and with
// anything shaped like a key, a password in an address or a sign-in token
// taken out (the workspace export's scrub). nil stays nil.
func lateShowable(doc bson.Raw, scrub *wsarchive.Scrubber, hidden map[string]bool) bson.Raw {
	if doc == nil {
		return nil
	}
	var d bson.D
	if err := bson.Unmarshal(doc, &d); err != nil {
		return nil
	}
	d = withoutNeverShown(d, "", hidden)
	b, err := bson.Marshal(d)
	if err != nil {
		return nil
	}
	if s, _, err := scrub.Doc(b, nil); err == nil {
		return s
	}
	return b
}

func withoutNeverShown(d bson.D, path string, hidden map[string]bool) bson.D {
	out := bson.D{}
	for _, e := range d {
		p := e.Key
		if path != "" {
			p = path + "." + e.Key
		}
		if models.HiddenField(e.Key, holdsText(e.Value)) {
			hidden[p] = true
			continue
		}
		out = append(out, bson.E{Key: e.Key, Value: hideIn(e.Value, p, hidden)})
	}
	return out
}

// holdsText reports a value a secret can be in (not a number, a yes-or-no or
// a time).
func holdsText(v any) bool {
	switch v.(type) {
	case int32, int64, float64, bool, bson.DateTime, bson.Decimal128, nil:
		return false
	}
	return true
}

func hideIn(v any, path string, hidden map[string]bool) any {
	switch x := v.(type) {
	case bson.D:
		return withoutNeverShown(x, path, hidden)
	case bson.A:
		out := make(bson.A, len(x))
		for i, y := range x {
			out[i] = hideIn(y, path, hidden)
		}
		return out
	}
	return v
}

// differingFields names the top-level fields two records disagree on.
func differingFields(a, b bson.Raw) []string {
	out := []string{}
	fields := map[string]bool{}
	for _, d := range []bson.Raw{a, b} {
		els, _ := d.Elements()
		for _, e := range els {
			fields[e.Key()] = true
		}
	}
	for f := range fields {
		va, ea := a.LookupErr(f)
		vb, eb := b.LookupErr(f)
		if (ea == nil) != (eb == nil) || (ea == nil && !va.Equal(vb)) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
}

/* ---------- carrying over, leaving ---------- */

// DatabaseLateDecideIn names records found: their IDs or numbers, or all
// that wait.
type DatabaseLateDecideIn struct {
	IDs []string `json:"ids,omitempty" jsonschema:"maxItems=500"`
	All bool     `json:"all,omitempty" jsonschema:"description=Every record that waits for a person."`
}

// LateRefused is a record (or a group) not carried over, and why.
type LateRefused struct {
	Found LateWriteView `json:"found"`
	Why   string        `json:"why"`
	// Reason: changed_here (this database's record changed since the move),
	// gone (removed from the old database: never removed here), report_only
	// (sign-in secrets: never carried over), no_copy (too large to keep, or
	// let go of), decided (carried over or left already), clash (another
	// record here takes its place), changed_again (a look found a newer
	// version meanwhile), not_all_listed (part of a group a look hasn't
	// listed whole yet), cant_be_whole (one no look can complete now: it can
	// only be left), being_carried (a carry-over holds it right now).
	Reason string `json:"reason" jsonschema:"enum=changed_here,enum=gone,enum=report_only,enum=no_copy,enum=decided,enum=clash,enum=changed_again,enum=not_all_listed,enum=cant_be_whole,enum=being_carried"`
}

// DatabaseLateCarryOut is what was carried over.
type DatabaseLateCarryOut struct {
	Carried []LateWriteView `json:"carried"`
	Refused []LateRefused   `json:"refused"`
	// Warnings say what went only part of the way (a group of which some
	// records changed here meanwhile).
	Warnings []string `json:"warnings"`
	Summary  string   `json:"summary"`
}

// DatabaseLateLeaveOut is what was left as it is.
type DatabaseLateLeaveOut struct {
	Left    []LateWriteView `json:"left"`
	Summary string          `json:"summary"`
}

// lateBeingCarried is said of a record a carry-over claimed.
const lateBeingCarried = "It is being carried over right now (one that stopped part-way lets go of it within 10 minutes): try again then."

// lateCarryHook, set by tests, runs after a carry-over read what it carries
// and before it claims it (a look racing it).
var lateCarryHook func(r lateWriteRec)

// DatabaseLateCarry is database.late.carry: the old database's version goes
// into this database only while this database's own record is still the one
// the move copied (by check and set: the record matched whole), and a group
// goes over whole or not at all. Otherwise nothing changes and both versions
// are shown (database.late.show). A record removed from the old database is
// never removed here; one of a kind that holds sign-in secrets is never
// carried over.
func DatabaseLateCarry(ctx context.Context, a *app.App, in *DatabaseLateDecideIn) (*DatabaseLateCarryOut, error) {
	if err := databasePeopleOnly(a); err != nil {
		return nil, err
	}
	refs, err := lateByRef(ctx, a, in.IDs, in.All)
	if err != nil {
		return nil, err
	}
	out := &DatabaseLateCarryOut{Carried: []LateWriteView{}, Refused: []LateRefused{}, Warnings: []string{}}
	who := firstNonEmpty(a.Actor.UserName, "a person")
	looks := map[string]*lateLookRec{}
	for _, ref := range refs {
		members, err := lateMembers(ctx, a, ref.rec)
		if err != nil {
			return nil, err
		}
		look := looks[ref.rec.Move]
		if look == nil {
			look, _ = readLateLook(ctx, a, a.DB, ref.rec.Move)
			looks[ref.rec.Move] = look
		}
		reason, why, carried, warn, err := carryEntry(ctx, a, ref.rec, members, look, who)
		if err != nil {
			return nil, err
		}
		cur, _ := readLateWrite(ctx, a, ref.rec.ID)
		if cur == nil {
			cur = &ref.rec
		}
		_, changed, _ := copyState(ctx, a, *cur)
		v := viewOfLate(*cur, len(members), changed)
		v.N = ref.n
		if warn != "" {
			out.Warnings = append(out.Warnings, warn)
		}
		if reason != "" && carried == 0 {
			out.Refused = append(out.Refused, LateRefused{Found: v, Why: why, Reason: reason})
			continue
		}
		v.GroupSize = carried
		out.Carried = append(out.Carried, v)
	}
	for _, look := range looks {
		if look != nil {
			tellOwnersLate(ctx, a, a.DB, look, false)
		}
	}
	if len(refs) == 1 && len(out.Refused) == 1 {
		f := out.Refused[0]
		return nil, apperr.New(apperr.Conflict, "%s", f.Why).
			WithHint("Compare both versions, then leave it, or change this database's record yourself.").
			WithDetails(map[string]any{"reason": f.Reason, "id": f.Found.ID}).WithCommand(fmt.Sprintf("metarush database late-writes show %d", f.Found.N))
	}
	n := 0
	for _, c := range out.Carried {
		n += c.GroupSize
	}
	switch {
	case len(refs) == 0:
		out.Summary = "Nothing was waiting to be carried over."
	case n > 0 && len(out.Refused) == 0:
		out.Summary = fmt.Sprintf("Carried %s over into this database.", plural(n, "record"))
	case n > 0:
		out.Summary = fmt.Sprintf("Carried %s over; %s not: see why, and compare both versions first.", plural(n, "record"), plural(len(out.Refused), "thing"))
	default:
		out.Summary = fmt.Sprintf("Nothing was carried over: %s", lowerFirst(out.Refused[0].Why))
	}
	if len(out.Warnings) > 0 {
		out.Summary += " " + strings.Join(out.Warnings, " ")
	}
	return out, nil
}

// carryEntry carries one entry over — a record, or every record of its group
// (all checked first: a group goes over whole or not at all). It says why
// not (a reason and words), how many records went over, and what went only
// part of the way.
func carryEntry(ctx context.Context, a *app.App, r lateWriteRec, members []lateWriteRec, look *lateLookRec, who string) (string, string, int, string, error) {
	group := r.Group != ""
	what := upperFirstWord(lateNoun(r.Collection))
	if len(members) > 1 {
		what = fmt.Sprintf("The %s of %s", plural(len(members), "record"), lateGroupBy[r.Collection].what)
	}
	if group && look != nil && (look.More > 0 || look.Partial) {
		if look.Ended || look.Found >= lateKeepPerMove {
			return "cant_be_whole", what + " can't go over whole: MetaRush didn't see all of it and can't look any further. Leave it: what it found stays uncarried, and the rest stays only in the old database.", 0, "", nil
		}
		return "not_all_listed", what + " may not all be listed yet (the last look didn't keep or see everything), and it goes over whole: look again first.", 0, "", nil
	}
	gone := "was"
	if len(members) > 1 {
		gone = "has a record that was"
	}
	var check []lateWriteRec
	for _, m := range members {
		switch {
		case m.State == LateCarrying:
			return "being_carried", lateBeingCarried, 0, "", nil
		case m.State != LateWaiting:
			return "decided", "That record was " + map[string]string{LateCarried: "carried over", LateLeft: "left as it is here", LateSettled: "settled by a later look"}[m.State] + " already.", 0, "", nil
		case m.Kind == dbmove.LateEmptied || m.Kind == dbmove.LateKindGone:
			return "gone", "MetaRush never removes anything here because it went from the old database: if you want it gone here too, remove it yourself, then leave this.", 0, "", nil
		case m.ReportOnly:
			return "report_only", upperFirstWord(lateNoun(r.Collection)) + " is a kind of record MetaRush only reports. " + lateReportAdvice(r.Collection), 0, "", nil
		case m.Kind == dbmove.LateGone:
			return "gone", what + " " + gone + " removed from the old database after the move, and MetaRush never removes a record here by itself: if it should go, remove it here yourself, then leave it.", 0, "", nil
		case !m.Kept:
			if m.CopyDropped {
				return "no_copy", fmt.Sprintf("MetaRush kept its copy for %d days for someone to decide, then let it go, so it can't be carried over any more.", lateKeepDays()), 0, "", nil
			}
			return "no_copy", what + " is too large for MetaRush to have kept a copy, so it can't be carried over.", 0, "", nil
		}
		check = append(check, m)
	}
	changedHere := 0
	for len(check) > 0 {
		n := min(len(check), 500)
		for _, c := range copiesChanged(ctx, a, check[:n]) {
			if c {
				changedHere++
			}
		}
		check = check[n:]
	}
	if changedHere > 0 {
		if len(members) > 1 {
			return "changed_here", fmt.Sprintf("%d of %s changed here too since the move (or another record took its place), so nothing was carried over: these go over together.",
				changedHere, lowerFirst(what)), 0, "", nil
		}
		return "changed_here", "This database's " + strings.TrimPrefix(strings.TrimPrefix(lateNoun(r.Collection), "a "), "an ") + " was changed too since the move (or another record took its place), so carrying the old database's version over would lose that change.", 0, "", nil
	}
	carried, raced := 0, 0
	var reason, why string
	for _, m := range members {
		rs, w, err := carryLate(ctx, a, m, who)
		if err != nil {
			return "", "", carried, "", err
		}
		if rs != "" {
			raced++
			reason, why = rs, w
			continue
		}
		carried++
	}
	warn := ""
	if carried > 0 && raced > 0 {
		left := plural(raced, "record") + " of them " + map[bool]string{true: "waits", false: "wait"}[raced == 1]
		warn = fmt.Sprintf("Only %d of the %s of %s went over, as one changed while they went: %s %s.", carried, plural(len(members), "record"), lateGroupBy[r.Collection].what, why, left)
	}
	return reason, why, carried, warn, nil
}

// carryLate carries one record over: claimed first (waiting → carrying, only
// while it still has the version read: a look that found a newer one
// meanwhile wins), then written here by check and set, then marked carried
// and its copy let go. A refusal is a reason and words.
func carryLate(ctx context.Context, a *app.App, r lateWriteRec, who string) (string, string, error) {
	if lateCarryHook != nil {
		lateCarryHook(r)
	}
	lw := a.DB.C(store.CLateWrites)
	claim, err := lw.UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "state": LateWaiting, "print": r.Print},
		bson.M{"$set": bson.M{"state": LateCarrying, "carryingAt": time.Now().UTC()}})
	if err != nil {
		return "", "", apperr.Wrap(apperr.Database, err, "MetaRush couldn't carry it over.")
	}
	if claim.ModifiedCount == 0 {
		return "changed_again", "A look found a newer version of it in the old database meanwhile: see it again, then decide.", nil
	}
	putBack := func() {
		_, _ = lw.UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": r.ID, "state": LateCarrying}, bson.M{"$set": bson.M{"state": LateWaiting}, "$unset": bson.M{"carryingAt": ""}})
	}
	// The copy, read only now: a group's records are held one at a time.
	var kept lateWriteRec
	if err := lw.FindOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()}).Decode(&kept); err != nil || kept.Old == nil {
		putBack()
		if err != nil {
			return "", "", apperr.Wrap(apperr.Database, err, "MetaRush couldn't carry it over.")
		}
		return "no_copy", "MetaRush no longer keeps a copy of it, so it can't be carried over.", nil
	}
	coll := a.DB.C(r.Collection)
	id := dbmove.IDOf(r.Key)
	changedHere := "This database's record was changed too since the move, so carrying the old database's version over would lose that change."
	here, changed, err := copyState(ctx, a, r)
	if err != nil {
		putBack()
		return "", "", apperr.Wrap(apperr.Database, err, "MetaRush couldn't read this database's record.")
	}
	if changed {
		putBack()
		if r.Kind == dbmove.LateNew {
			return "changed_here", "This database already has a record in its place, so it wasn't added.", nil
		}
		return "changed_here", changedHere, nil
	}
	already := false
	if here != nil {
		p, err := dbmove.PrintOf(here)
		already = err == nil && p == r.Print // a carry-over that stopped just after this step
	}
	switch {
	case already:
	case r.Kind == dbmove.LateNew:
		if _, err := coll.InsertOne(ctx, kept.Old); err != nil {
			putBack()
			if store.IsDuplicateKey(err) {
				var other bson.Raw
				if coll.FindOne(ctx, bson.M{"_id": id}).Decode(&other) == nil {
					return "changed_here", "This database already has a record in its place, so it wasn't added.", nil
				}
				return "clash", "Another record in this database takes its place (the same name or key), so it wasn't added.", nil
			}
			return "", "", apperr.Wrap(apperr.Database, err, "MetaRush couldn't carry it over.")
		}
	default:
		if here == nil {
			putBack()
			return "changed_here", changedHere, nil
		}
		// Check and set: replaced only while the record here is still, whole,
		// the one just read (the one the move copied).
		res, err := coll.ReplaceOne(ctx, bson.M{"_id": id, "$expr": bson.M{"$eq": bson.A{"$$ROOT", bson.M{"$literal": here}}}}, kept.Old)
		if err != nil {
			putBack()
			if store.IsDuplicateKey(err) {
				return "clash", "Another record in this database takes its place (the same name or key), so it wasn't carried over.", nil
			}
			return "", "", apperr.Wrap(apperr.Database, err, "MetaRush couldn't carry it over.")
		}
		if res.MatchedCount == 0 {
			putBack()
			return "changed_here", changedHere, nil
		}
	}
	// In the history before it is marked: a process that stops here leaves
	// a history that says so, and the record goes over again without a change.
	a.Audit(ctx, "database.late.carry", "late_write", r.ID, "", map[string]any{"move": r.Move, "collection": r.Collection, "record": lateRecordWords(r.Key), "kind": r.Kind})
	now := time.Now().UTC()
	done, err := lw.UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "state": LateCarrying, "print": r.Print},
		bson.M{"$set": bson.M{"state": LateCarried, "decidedBy": who, "decidedAt": now, "kept": false}, "$unset": bson.M{"old": "", "carryingAt": ""}})
	if err == nil && done.ModifiedCount == 0 {
		// A look kept a newer version while this one went over: it waits
		// again with that one (and this database's record now differs from
		// the move's, so it is shown side by side).
		return "changed_again", "A look found a newer version of it in the old database while this one went over: see it again, then decide.", nil
	}
	return "", "", err
}

// DatabaseLateLeave is database.late.leave: this database keeps its own
// record (or its group's); the copy found in the old database is let go,
// and the record stays in the list as decided.
func DatabaseLateLeave(ctx context.Context, a *app.App, in *DatabaseLateDecideIn) (*DatabaseLateLeaveOut, error) {
	if err := databasePeopleOnly(a); err != nil {
		return nil, err
	}
	refs, err := lateByRef(ctx, a, in.IDs, in.All)
	if err != nil {
		return nil, err
	}
	out := &DatabaseLateLeaveOut{Left: []LateWriteView{}}
	now := time.Now().UTC()
	who := firstNonEmpty(a.Actor.UserName, "a person")
	total, busy := 0, 0
	looks := map[string]*lateLookRec{}
	counted := 0
	for _, ref := range refs {
		if ref.rec.State == LateCarrying {
			busy++
			continue
		}
		if ref.rec.State != LateWaiting {
			continue
		}
		members, err := lateMembers(ctx, a, ref.rec)
		if err != nil {
			return nil, err
		}
		left := 0
		for _, m := range members {
			res, err := a.DB.C(store.CLateWrites).UpdateOne(ctx, bson.M{"_id": m.ID, "workspaceId": a.WorkspaceID(), "state": LateWaiting},
				bson.M{"$set": bson.M{"state": LateLeft, "decidedBy": who, "decidedAt": now, "kept": false}, "$unset": bson.M{"old": ""}})
			if err != nil {
				return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't note it.")
			}
			if res.ModifiedCount == 0 {
				continue
			}
			left++
			a.Audit(ctx, "database.late.leave", "late_write", m.ID, "", map[string]any{"move": m.Move, "collection": m.Collection, "record": lateRecordWords(m.Key), "kind": m.Kind})
		}
		if left == 0 {
			continue
		}
		total += left
		r := ref.rec
		r.State, r.DecidedBy, r.DecidedAt, r.Old, r.Kept = LateLeft, who, &now, nil, false
		v := viewOfLate(r, left, false)
		v.N = ref.n
		out.Left = append(out.Left, v)
		if _, ok := looks[r.Move]; !ok {
			look, _ := readLateLook(ctx, a, a.DB, r.Move)
			looks[r.Move] = look
			if look != nil && look.More > 0 && (look.Ended || look.Found >= lateKeepPerMove) {
				counted += look.More
			}
		}
	}
	for _, look := range looks {
		if look != nil {
			tellOwnersLate(ctx, a, a.DB, look, false)
		}
	}
	switch {
	case total == 0 && busy > 0:
		return nil, apperr.New(apperr.Conflict, "%s", lateBeingCarried).WithDetails(map[string]any{"reason": "being_carried"})
	case total == 0:
		out.Summary = "Nothing was waiting: those were decided already."
	default:
		out.Summary = fmt.Sprintf("Left %s as %s here: this database keeps its own.", plural(total, "record"), map[bool]string{true: "it is", false: "they are"}[total == 1])
		if counted > 0 {
			out.Summary += fmt.Sprintf(" %s MetaRush only counted stay only in the old database.", upperFirstWord(plural(counted, "more record")))
		}
	}
	return out, nil
}

/* ---------- MetaRush in the background ---------- */

// RunLateLooks takes the looks due on this computer and lets go of copies
// nobody decided on in time (MetaRush in the background, every minute).
func RunLateLooks(ctx context.Context, a *app.App, logf func(string, ...any)) {
	t := time.NewTimer(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now().UTC()
		for _, s := range LookDueLate(ctx, a, now) {
			logf("%s", s)
		}
		if n := DropOldLateCopies(ctx, a, now); n > 0 {
			logf("let go of %s of records written into an old database late that nobody decided on in %d days", plural(n, "copy"), lateKeepDays())
		}
		t.Reset(time.Minute)
	}
}

// DropOldLateCopies lets go of the copies of records found written late that
// waited longer than LateCopyKeep for a person (the findings stay, and say
// so). An automatic deletion: never while the workspace's background
// deletions are paused (a restored copy). It says how many it let go.
func DropOldLateCopies(ctx context.Context, a *app.App, now time.Time) int {
	if a.ReadOnly || a.Held() || a.DB.DeletionsPaused(ctx, a.WorkspaceID()) {
		return 0
	}
	res, err := a.DB.C(store.CLateWrites).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "kept": true, "keptAt": bson.M{"$lt": now.Add(-LateCopyKeep)}},
		bson.M{"$set": bson.M{"kept": false, "copyDropped": true}, "$unset": bson.M{"old": ""}})
	if err != nil {
		return 0
	}
	return int(res.ModifiedCount)
}

// LookDueLate takes the looks due on this computer by now, tries again one
// that couldn't be made, and ends the looks whose time is over. It says what
// it did.
func LookDueLate(ctx context.Context, a *app.App, now time.Time) []string {
	if a.ReadOnly || a.Held() {
		return nil
	}
	var recs []lateLookRec
	if err := a.DB.Find(ctx, store.CLateLooks, a.WorkspaceID(), bson.M{"computer": a.Cfg.Installation.ID, "ended": false}, &recs); err != nil {
		return nil
	}
	var said []string
	for i := range recs {
		r := &recs[i]
		if len(r.Due) == 0 {
			endLateLooks(ctx, a, a.DB, r.ID, "MetaRush made its last look at the old database.", true)
			continue
		}
		if r.Done >= len(r.Due) {
			// The last look didn't see everything: a person can still look, for
			// a while.
			switch {
			case r.Complete:
				endLateLooks(ctx, a, a.DB, r.ID, "MetaRush made its last look at the old database.", true)
			case now.After(r.Due[len(r.Due)-1].Add(LateLookGrace)):
				endLateLooks(ctx, a, a.DB, r.ID, "MetaRush's last look didn't see everything and nobody finished it, so it stopped looking and no longer keeps the old database's address.", false)
				said = append(said, fmt.Sprintf("stopped looking at the old database (%s): its last look didn't see everything", r.From))
			}
			continue
		}
		if now.Before(r.Due[r.Done]) {
			continue
		}
		if r.Tried != nil && now.Sub(*r.Tried) < lateRetryEvery {
			continue
		}
		o, err := lookAndRecord(ctx, a, a.DB, r, nil, LateLater, now)
		switch {
		case errors.Is(err, errLookRunning):
			// A person's look runs now: it counts when it's done.
		case err != nil && apperr.From(err).Code == apperr.NotConfigured:
			// The address or the fingerprints are gone from this computer: no
			// later look could be made either.
			endLateLooks(ctx, a, a.DB, r.ID, lateProblemWords(err)+" MetaRush stopped looking at the old database.", false)
			said = append(said, fmt.Sprintf("stopped looking at the old database (%s): %s", r.From, lowerFirst(strings.TrimSuffix(lateProblemWords(err), "."))))
		case err == nil && o.partial:
			said = append(said, fmt.Sprintf("looked at the old database (%s) again, part of it: %s; %s found", r.From, o.stopped, plural(o.found, "record")))
		case err == nil && o.found > 0:
			said = append(said, fmt.Sprintf("looked at the old database (%s) again: %s written there after the move", r.From, plural(o.found, "record")))
		case err == nil:
			said = append(said, fmt.Sprintf("looked at the old database (%s) again: nothing new", r.From))
		case now.After(r.Due[len(r.Due)-1].Add(LateLookGrace)):
			endLateLooks(ctx, a, a.DB, r.ID, "MetaRush couldn't reach the old database for its last look, so it stopped looking and no longer keeps its address.", false)
			said = append(said, fmt.Sprintf("stopped looking at the old database (%s): it couldn't be reached", r.From))
		default:
			said = append(said, fmt.Sprintf("couldn't look at the old database (%s) again: %s", r.From, lowerFirst(strings.TrimSuffix(lateProblemWords(err), "."))))
		}
	}
	return said
}

// LateStatus is the newest move's looks in this database, for
// database.move.status (nil when none).
func LateStatus(ctx context.Context, a *app.App) *LateLookView {
	var r lateLookRec
	err := a.DB.C(store.CLateLooks).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID()}, options.FindOne().SetSort(bson.D{{Key: "movedAt", Value: -1}})).Decode(&r)
	if err != nil {
		return nil
	}
	return viewOfLook(ctx, a, a.DB, &r)
}
