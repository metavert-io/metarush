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
	"github.com/metavert-io/metarush/internal/localdb"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/store"
)

// Something written late (D352–D358, docs/database-move.md): a write a
// computer sent just before it slept for longer than MetaRush trusts a look
// at the move's marker can still land in the old database after the move
// marked it moved — and nobody would ever see it there. So the computer that
// moved keeps, until its last look, the old database's address and the
// fingerprints of every record the move's last look read (dbmove), and looks
// at the old database again: right after it switched, then about 15 minutes,
// an hour and a day after the move (MetaRush in the background), and whenever
// an owner asks (`metarush database late-writes`). A look only reads the old
// database. What it finds — records new, changed or gone there since — goes
// into this database (late_looks, late_writes) and owners hear of it once,
// in plain words; a person carries a record over (only while this database's
// own record is still the one the move copied: check and set; otherwise both
// versions are shown and nothing changes) or leaves it. A record gone from
// the old database is never removed here by MetaRush. After the last look the
// address and the fingerprints are dropped.

func init() {
	Def("database.late.writes", "List what was written into the old database after the workspace moved to this one — records new, changed or gone there since the move — and, on the computer that moved it while it keeps the old database's address, look again now (owners)",
		Flags{OwnerOnly: true}, DatabaseLateWrites)
	Def("database.late.show", "Show a record written into the old database after the move: the old database's version beside this database's (owners, people only)",
		Flags{OwnerOnly: true, HumanOnly: true}, DatabaseLateShow)
	Def("database.late.carry", "Carry a record written into the old database after the move over into this database — only while this database's own record is still the one the move copied (owners, people only)",
		Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, DatabaseLateCarry)
	Def("database.late.leave", "Leave a record written into the old database after the move as it is here: this database keeps its own (owners, people only)",
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
)

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
	LateWaiting = "waiting" // a person decides: carry it over or leave it
	LateCarried = "carried" // carried over into this database
	LateLeft    = "left"    // left as it is here
)

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
	// Found numbers the records found (the last one's number).
	Found int `bson:"found"`
}

// LateLookTaken is one look at the old database after a move.
type LateLookTaken struct {
	At   time.Time `bson:"at" json:"at"`
	When string    `bson:"when" json:"when" jsonschema:"enum=soon,enum=later,enum=asked"`
	// Found is how many records it found for the first time (or changed
	// again since a person decided).
	Found int `bson:"found" json:"found"`
}

// lateWriteRec is one record found written into the old database after the
// move (late_writes).
type lateWriteRec struct {
	ID          string `bson:"_id"`
	WorkspaceID string `bson:"workspaceId"`
	Move        string `bson:"move"`
	N           int    `bson:"n"`
	Collection  string `bson:"collection"`
	// Key is the record's _id: its BSON type, then its value.
	Key  []byte `bson:"key"`
	Kind string `bson:"kind"`
	// Old is the old database's version (none when gone, or too large to keep).
	Old      bson.Raw `bson:"old,omitempty"`
	TooLarge bool     `bson:"tooLarge,omitempty"`
	// Print is Old's fingerprint, MovePrint the record's when the workspace
	// moved ("" for a new one): the copy here carries over only while it is
	// still that.
	Print     string `bson:"print,omitempty"`
	MovePrint string `bson:"movePrint,omitempty"`
	// At is when it was written, as the record says (nil when it doesn't).
	At           *time.Time `bson:"at,omitempty"`
	Computer     string     `bson:"computer,omitempty"`
	ComputerName string     `bson:"computerName,omitempty"`
	FoundAt      time.Time  `bson:"foundAt"`
	SeenAt       time.Time  `bson:"seenAt"`
	State        string     `bson:"state"`
	Again        bool       `bson:"again,omitempty"`
	DecidedBy    string     `bson:"decidedBy,omitempty"`
	DecidedAt    *time.Time `bson:"decidedAt,omitempty"`
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

// lookAndRecord takes one look for a move and keeps what it found in db (the
// database the workspace moved to); owners hear of what is new. A look that
// worked counts for every look due by now; after the last one the address
// and the fingerprints are dropped. old nil: opened with the address kept.
func lookAndRecord(ctx context.Context, a *app.App, db *store.DB, rec *lateLookRec, old *store.DB, when string, now time.Time) (int, error) {
	found, err := lookLate(ctx, a, db, rec, old, now)
	if err != nil {
		_, _ = db.C(store.CLateLooks).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID()},
			bson.M{"$set": bson.M{"tried": now, "problem": lateProblemWords(err)}})
		return 0, err
	}
	done := 0
	for _, d := range rec.Due {
		if !d.After(now) {
			done++
		}
	}
	set := bson.M{"tried": nil, "problem": ""}
	if done > rec.Done {
		set["done"] = done
	}
	_, _ = db.C(store.CLateLooks).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$set": set, "$push": bson.M{"looks": LateLookTaken{At: now, When: when, Found: found}}})
	if done >= len(rec.Due) && len(rec.Due) > 0 {
		endLateLooks(ctx, a, db, rec.ID, "MetaRush made its last look at the old database.")
	}
	return found, nil
}

// lateProblemWords says why a look couldn't be made.
func lateProblemWords(err error) string {
	if e := apperr.From(err); e != nil && e.Code != apperr.Internal {
		return e.Message
	}
	return "MetaRush couldn't reach the old database to look at it again."
}

// endLateLooks ends a move's looks: the record says so and this computer
// drops the old database's address and the fingerprints.
func endLateLooks(ctx context.Context, a *app.App, db *store.DB, move, why string) {
	now := time.Now().UTC()
	_, _ = db.C(store.CLateLooks).UpdateOne(ctx, bson.M{"_id": move, "workspaceId": a.WorkspaceID(), "ended": false},
		bson.M{"$set": bson.M{"ended": true, "endedAt": now, "endedWhy": why}})
	forgetLookAddress(a, move)
}

// lookLate compares the old database with the move's fingerprints (reading
// it only) and keeps each record found in db; it says how many it found for
// the first time.
func lookLate(ctx context.Context, a *app.App, db *store.DB, rec *lateLookRec, old *store.DB, now time.Time) (int, error) {
	prints := latePrintsFile(a, rec.ID)
	if _, err := os.Stat(prints); err != nil {
		return 0, apperr.New(apperr.NotConfigured, "This computer no longer keeps what it needs to look at the old database again.")
	}
	if old == nil {
		uri := loadLookAddress(a.Cfg, rec.ID)
		if uri == "" {
			return 0, apperr.New(apperr.NotConfigured, "This computer no longer keeps the old database's address.")
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		db2, err := store.Connect(cctx, uri, rec.FromName)
		cancel()
		if err != nil {
			return 0, apperr.New(apperr.Database, "MetaRush couldn't reach the old database to look at it again.").
				WithDetails(map[string]any{"problem": scrubAddresses(err.Error())})
		}
		defer func() {
			c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = db2.Close(c)
			cancel()
		}()
		old = db2
	}
	lctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	res, err := dbmove.Look(lctx, old, prints)
	if err != nil {
		if errors.Is(err, dbmove.ErrPrints) {
			return 0, apperr.New(apperr.NotConfigured, "This computer's fingerprints of the move can't be read any more, so it can't look at the old database again.")
		}
		return 0, apperr.New(apperr.Database, "MetaRush couldn't read the old database to look at it again.").
			WithDetails(map[string]any{"problem": scrubAddresses(err.Error())})
	}
	names := computerNames(ctx, a, db)
	newly := 0
	for _, x := range res.Found {
		_, isNew, err := keepLate(ctx, a, db, rec, x, now, names)
		if err != nil {
			return newly, apperr.Wrap(apperr.Database, err, "MetaRush couldn't keep what it found in the old database.")
		}
		if isNew {
			newly++
		}
	}
	if newly > 0 {
		tellOwnersLate(ctx, a, db, rec)
	}
	return newly, nil
}

// lateWriteID names a record found for a move (the same record found again
// keeps its name).
func lateWriteID(move, coll string, key []byte) string {
	h := sha256.Sum256([]byte(move + "\x00" + coll + "\x00" + string(key)))
	return "lwr_" + hex.EncodeToString(h[:])[:26]
}

// keepLate keeps one record a look found: new ones numbered in order, one
// found before updated when the old database's version changed again (and
// waiting for a person again when one had decided).
func keepLate(ctx context.Context, a *app.App, db *store.DB, look *lateLookRec, x dbmove.Late, now time.Time, names map[string]string) (*lateWriteRec, bool, error) {
	id := lateWriteID(look.ID, x.Collection, x.Key)
	r := lateWriteRec{ID: id, WorkspaceID: a.WorkspaceID(), Move: look.ID, Collection: x.Collection, Key: x.Key, Kind: x.Kind,
		Print: x.Print, MovePrint: x.MovePrint, FoundAt: now, SeenAt: now, State: LateWaiting}
	if len(x.Doc) > lateKeepMax {
		r.TooLarge = true
	} else if x.Doc != nil {
		r.Old = x.Doc
	}
	r.At = lateWhen(x, look.MovedAt)
	r.Computer, r.ComputerName = lateComputer(x.Doc, names)
	var cur lateWriteRec
	err := db.C(store.CLateWrites).FindOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}).Decode(&cur)
	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		var lk lateLookRec
		if err := db.C(store.CLateLooks).FindOneAndUpdate(ctx, bson.M{"_id": look.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$inc": bson.M{"found": 1}},
			options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&lk); err != nil {
			return nil, false, err
		}
		r.N = lk.Found
		if _, err := db.C(store.CLateWrites).InsertOne(ctx, r); err != nil {
			if store.IsDuplicateKey(err) { // another look kept it first
				return &r, false, nil
			}
			return nil, false, err
		}
		return &r, true, nil
	case err != nil:
		return nil, false, err
	}
	if cur.Kind == r.Kind && cur.Print == r.Print {
		_, err := db.C(store.CLateWrites).UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"seenAt": now}})
		return &cur, false, err
	}
	set := bson.M{"kind": r.Kind, "print": r.Print, "seenAt": now, "at": r.At, "computer": r.Computer, "computerName": r.ComputerName, "tooLarge": r.TooLarge}
	unset := bson.M{}
	if r.Old != nil {
		set["old"] = r.Old
	} else {
		unset["old"] = ""
	}
	again := cur.State != LateWaiting
	if again {
		set["state"], set["again"] = LateWaiting, true
	}
	upd := bson.M{"$set": set}
	if len(unset) > 0 {
		upd["$unset"] = unset
	}
	if _, err := db.C(store.CLateWrites).UpdateOne(ctx, bson.M{"_id": id}, upd); err != nil {
		return nil, false, err
	}
	cur.Kind, cur.Print, cur.Old, cur.At = r.Kind, r.Print, r.Old, r.At
	return &cur, again, nil
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

// tellOwnersLate rings every owner's bell once per move, refreshed as more is
// found: what was written late, in words, and where to decide.
func tellOwnersLate(ctx context.Context, a *app.App, db *store.DB, look *lateLookRec) {
	var waiting []lateWriteRec
	_ = db.Find(ctx, store.CLateWrites, a.WorkspaceID(), bson.M{"move": look.ID, "state": LateWaiting}, &waiting, options.Find().SetSort(bson.D{{Key: "n", Value: 1}}))
	if len(waiting) == 0 {
		return
	}
	var says []string
	for i, w := range waiting {
		if i == 3 {
			says = append(says, plural(len(waiting)-3, "more thing"))
			break
		}
		says = append(says, lateShort(w))
	}
	body := fmt.Sprintf("After the workspace moved from %s, %s written there: %s. Nobody would see it in this database: carry it over, or leave it, in Settings → Storage → Move the database.",
		look.From, map[bool]string{true: "something was", false: plural(len(waiting), "thing") + " were"}[len(waiting) == 1], strings.Join(says, "; "))
	b := *a
	b.DB = db
	var owners []models.User
	_ = db.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"role": models.RoleOwner}, &owners)
	if len(owners) == 0 && a.Cfg.User.ID != "" {
		owners = append(owners, models.User{ID: a.Cfg.User.ID})
	}
	for _, o := range owners {
		_, _ = notify.Upsert(ctx, &b, models.Notification{UserID: o.ID, Kind: models.NotifyLateWrites, GroupKey: "late_writes:" + look.ID,
			Tone: "warning", Title: "Something was written into the old database after the move", Body: body, Link: "/settings/storage#database",
			Details: map[string]any{"move": look.ID, "waiting": len(waiting)}}, notify.Refresh)
	}
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
	store.CAuthTokens: "an invitation or password-reset link", store.CTranscriptFixes: "a fix to a transcript",
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

// lateTime says a time the way people read it on this computer.
func lateTime(t time.Time) string { return t.Local().Format("2 Jan 15:04") }

// lateShort is a record found, in a few words ("a comment, changed about 2 Nov 14:05 on Edit laptop").
func lateShort(r lateWriteRec) string {
	s := lateNoun(r.Collection) + ", " + map[string]string{dbmove.LateNew: "added", dbmove.LateChanged: "changed", dbmove.LateGone: "removed"}[r.Kind]
	if r.At != nil {
		s += " about " + lateTime(*r.At)
	}
	if r.ComputerName != "" {
		s += " on " + r.ComputerName
	}
	return s
}

// lateSays is a record found, as a sentence.
func lateSays(r lateWriteRec, copyChanged bool) string {
	what := lateNoun(r.Collection)
	var s string
	switch r.Kind {
	case dbmove.LateNew:
		s = upperFirstWord(what) + " was added to the old database after the move"
	case dbmove.LateChanged:
		s = upperFirstWord(what) + " was changed in the old database after the move"
	default:
		s = upperFirstWord(what) + " was removed from the old database after the move"
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
	case r.Kind == dbmove.LateGone:
		s += " It is still here: MetaRush never removes it by itself."
	case r.TooLarge:
		s += " It is too large for MetaRush to keep a copy to carry over."
	case copyChanged && r.Kind == dbmove.LateNew:
		s += " This database has a different record in its place: compare both before you decide."
	case copyChanged:
		s += " It was changed here too since the move: compare both before you decide."
	}
	return s
}

/* ---------- the views ---------- */

// LateWriteView is a record written into the old database after the move.
type LateWriteView struct {
	// N is its number in the list (carry it over or leave it by number).
	N    int    `json:"n"`
	ID   string `json:"id"`
	What string `json:"what" jsonschema:"description=The kind of record\\, in words: a comment\\, a clip\\, a version of a cut."`
	Kind string `json:"kind" jsonschema:"enum=new,enum=changed,enum=gone"`
	// At is when it was written, as the record says.
	At       *time.Time `json:"at,omitempty"`
	Computer string     `json:"computer,omitempty" jsonschema:"description=The computer it was written on\\, when the record says."`
	FoundAt  time.Time  `json:"foundAt"`
	State    string     `json:"state" jsonschema:"enum=waiting,enum=carried,enum=left"`
	// CopyChanged: this database's record changed since the move too (or,
	// for a new one, this database has one in its place): carrying over is
	// refused and both versions are shown.
	CopyChanged bool `json:"copyChanged"`
	// CanCarry: carrying it over would go ahead now.
	CanCarry bool   `json:"canCarry"`
	Says     string `json:"says"`
	// Collection and Record say where it is (Details).
	Collection string     `json:"collection"`
	Record     string     `json:"record"`
	Move       string     `json:"move"`
	DecidedBy  string     `json:"decidedBy,omitempty"`
	DecidedAt  *time.Time `json:"decidedAt,omitempty"`
}

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
	Summary  string          `json:"summary"`
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
		Looks: append([]LateLookTaken{}, r.Looks...), Ended: r.Ended, EndedWhy: r.EndedWhy, Problem: r.Problem, Found: r.Found}
	if n, err := db.C(store.CLateWrites).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "move": r.ID, "state": LateWaiting}); err == nil {
		v.Waiting = int(n)
	}
	if len(r.Due) > 0 {
		last := r.Due[len(r.Due)-1]
		v.Last = &last
		if !r.Ended && r.Done < len(r.Due) {
			next := r.Due[r.Done]
			v.Next = &next
		}
	}
	who := firstNonEmpty(r.ComputerName, "the computer that moved it")
	switch {
	case v.Waiting == 1:
		v.Summary = fmt.Sprintf("Something was written into the old database (%s) after the move: carry it over, or leave it as it is here.", r.From)
	case v.Waiting > 1:
		v.Summary = fmt.Sprintf("%s were written into the old database (%s) after the move: carry each over, or leave it as it is here.", upperFirstWord(plural(v.Waiting, "record")), r.From)
	case r.Ended && r.Found == 0:
		v.Summary = fmt.Sprintf("Nothing was written into the old database (%s) after the move. MetaRush looked for the last time and no longer keeps its address.", r.From)
	case r.Ended:
		v.Summary = fmt.Sprintf("MetaRush looked at the old database (%s) for the last time; everything it found was decided.", r.From)
	case r.Found == 0 && len(r.Looks) > 0:
		v.Summary = fmt.Sprintf("Nothing was written into the old database (%s) after the move so far. %s looks again %s.", r.From, who, nextWords(v.Next))
	case r.Found == 0:
		v.Summary = fmt.Sprintf("%s looks at the old database (%s) again %s, for anything written there after the move.", who, r.From, nextWords(v.Next))
	default:
		v.Summary = fmt.Sprintf("Everything found in the old database (%s) after the move was decided. %s looks again %s.", r.From, who, nextWords(v.Next))
	}
	return v
}

// nextWords says when the next look is.
func nextWords(t *time.Time) string {
	if t == nil {
		return "when asked"
	}
	d := time.Until(*t)
	switch {
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
	var cur bson.Raw
	err := a.DB.C(r.Collection).FindOne(ctx, bson.M{"_id": dbmove.IDOf(r.Key)}).Decode(&cur)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, r.Kind != dbmove.LateNew, nil // gone here: changed since the move (but for a new one)
	}
	if err != nil {
		return nil, false, err
	}
	if r.Kind == dbmove.LateNew {
		return cur, true, nil
	}
	p, err := dbmove.PrintOf(cur)
	if err != nil {
		return cur, true, nil
	}
	return cur, p != r.MovePrint, nil
}

func viewOfLate(ctx context.Context, a *app.App, r lateWriteRec) LateWriteView {
	v := LateWriteView{N: r.N, ID: r.ID, What: lateNoun(r.Collection), Kind: r.Kind, At: r.At, Computer: r.ComputerName, FoundAt: r.FoundAt,
		State: r.State, Collection: r.Collection, Record: lateRecordWords(r.Key), Move: r.Move, DecidedBy: r.DecidedBy, DecidedAt: r.DecidedAt}
	if r.State == LateWaiting {
		_, changed, err := copyState(ctx, a, r)
		v.CopyChanged = err == nil && changed
		v.CanCarry = err == nil && !changed && r.Kind != dbmove.LateGone && !r.TooLarge && r.Old != nil
	}
	v.Says = lateSays(r, v.CopyChanged)
	return v
}

/* ---------- database.late.writes ---------- */

// DatabaseLateWritesIn asks for what was written late.
type DatabaseLateWritesIn struct {
	// Look again now: on the computer that moved it, while it keeps the old
	// database's address.
	Look bool `json:"look,omitempty" jsonschema:"description=Look at the old database again now (on the computer that moved it\\, while it keeps the old database's address)."`
}

// DatabaseLateWritesOut is what was written into the old database after a
// move into this one.
type DatabaseLateWritesOut struct {
	// Looked: a look was made now; Problem says why one couldn't be.
	Looked  bool            `json:"looked"`
	Problem string          `json:"problem,omitempty"`
	Moves   []LateLookView  `json:"moves"`
	Found   []LateWriteView `json:"found"`
	Waiting int             `json:"waiting"`
	Summary string          `json:"summary"`
	Steps   []string        `json:"steps"`
}

// lateLooks are the looks recorded in this database, oldest move first.
func lateLooks(ctx context.Context, a *app.App) []lateLookRec {
	var recs []lateLookRec
	_ = a.DB.Find(ctx, store.CLateLooks, a.WorkspaceID(), bson.M{}, &recs, options.Find().SetSort(bson.D{{Key: "movedAt", Value: 1}}))
	return recs
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
	var found []lateWriteRec
	_ = a.DB.Find(ctx, store.CLateWrites, a.WorkspaceID(), bson.M{}, &found)
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
	for i, r := range found {
		v := viewOfLate(ctx, a, r)
		v.N = i + 1
		out.Found = append(out.Found, v)
		if v.State == LateWaiting {
			out.Waiting++
		}
	}
	switch {
	case len(out.Moves) == 0:
		out.Summary = "No move into this database is being looked after: nothing was written late."
	case out.Waiting > 0:
		out.Summary = fmt.Sprintf("%s written into the old database after the move %s for a person.", upperFirstWord(plural(out.Waiting, "record")),
			map[bool]string{true: "waits", false: "wait"}[out.Waiting == 1])
		out.Steps = append(out.Steps, "Carry each over into this database (only while this database's own record is still the one the move copied), or leave it as it is here.",
			"A record removed from the old database is never removed here by MetaRush: if it should go, remove it here yourself, then leave it.")
	default:
		out.Summary = out.Moves[len(out.Moves)-1].Summary
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

// lateByRef finds records found by their IDs or their numbers in the list,
// with each one's number there.
func lateByRef(ctx context.Context, a *app.App, refs []string, all bool) ([]lateWriteRec, map[string]int, error) {
	list, err := DatabaseLateWrites(ctx, a, &DatabaseLateWritesIn{})
	if err != nil {
		return nil, nil, err
	}
	number := map[string]int{}
	for _, f := range list.Found {
		number[f.ID] = f.N
	}
	var ids []string
	if all {
		for _, f := range list.Found {
			if f.State == LateWaiting {
				ids = append(ids, f.ID)
			}
		}
	}
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if n, err := strconv.Atoi(ref); err == nil {
			if n < 1 || n > len(list.Found) {
				return nil, nil, apperr.New(apperr.NotFound, "There is no number %d in the list of what was written late.", n).WithCommand("metarush database late-writes")
			}
			ids = append(ids, list.Found[n-1].ID)
			continue
		}
		ids = append(ids, ref)
	}
	if len(ids) == 0 {
		if all {
			return nil, number, nil
		}
		return nil, nil, invalid("Say which records found in the old database: their numbers in the list, or all of them.")
	}
	var out []lateWriteRec
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		r, err := readLateWrite(ctx, a, id)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, *r)
	}
	return out, number, nil
}

/* ---------- database.late.show ---------- */

// DatabaseLateShowIn names a record found.
type DatabaseLateShowIn struct {
	// ID is its ID or its number in the list.
	ID string `json:"id" jsonschema:"maxLength=64"`
}

// DatabaseLateShowOut is a record found, both versions side by side.
type DatabaseLateShowOut struct {
	Found LateWriteView `json:"found"`
	// Old is the old database's version, Here this database's (Extended
	// JSON; none when there is none).
	Old  json.RawMessage `json:"old,omitempty" jsonschema:"type=object"`
	Here json.RawMessage `json:"here,omitempty" jsonschema:"type=object"`
	// Differs names the fields that differ.
	Differs []string `json:"differs"`
	Summary string   `json:"summary"`
}

// DatabaseLateShow is database.late.show.
func DatabaseLateShow(ctx context.Context, a *app.App, in *DatabaseLateShowIn) (*DatabaseLateShowOut, error) {
	rs, number, err := lateByRef(ctx, a, []string{in.ID}, false)
	if err != nil {
		return nil, err
	}
	r := rs[0]
	out := &DatabaseLateShowOut{Found: viewOfLate(ctx, a, r), Differs: []string{}}
	out.Found.N = number[r.ID]
	here, _, err := copyState(ctx, a, r)
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read this database's record.")
	}
	if r.Old != nil {
		if b, err := bson.MarshalExtJSON(r.Old, false, false); err == nil {
			out.Old = b
		}
	}
	if here != nil {
		if b, err := bson.MarshalExtJSON(here, false, false); err == nil {
			out.Here = b
		}
	}
	out.Differs = differingFields(r.Old, here)
	out.Summary = out.Found.Says
	return out, nil
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

// LateRefused is a record not carried over, and why.
type LateRefused struct {
	Found LateWriteView `json:"found"`
	Why   string        `json:"why"`
	// Reason: changed_here (this database's record changed since the move),
	// gone (removed from the old database: never removed here), too_large,
	// decided (carried over or left already), clash (another record here
	// takes its place).
	Reason string `json:"reason" jsonschema:"enum=changed_here,enum=gone,enum=too_large,enum=decided,enum=clash"`
}

// DatabaseLateCarryOut is what was carried over.
type DatabaseLateCarryOut struct {
	Carried []LateWriteView `json:"carried"`
	Refused []LateRefused   `json:"refused"`
	Summary string          `json:"summary"`
}

// DatabaseLateLeaveOut is what was left as it is.
type DatabaseLateLeaveOut struct {
	Left    []LateWriteView `json:"left"`
	Summary string          `json:"summary"`
}

// DatabaseLateCarry is database.late.carry: the old database's version goes
// into this database only while this database's own record is still the one
// the move copied (by check and set: the record matched whole). Otherwise
// nothing changes and both versions are shown (database.late.show). A record
// removed from the old database is never removed here.
func DatabaseLateCarry(ctx context.Context, a *app.App, in *DatabaseLateDecideIn) (*DatabaseLateCarryOut, error) {
	if err := databasePeopleOnly(a); err != nil {
		return nil, err
	}
	recs, number, err := lateByRef(ctx, a, in.IDs, in.All)
	if err != nil {
		return nil, err
	}
	out := &DatabaseLateCarryOut{Carried: []LateWriteView{}, Refused: []LateRefused{}}
	who := firstNonEmpty(a.Actor.UserName, "a person")
	for _, r := range recs {
		reason, why, err := carryLate(ctx, a, r, who)
		if err != nil {
			return nil, err
		}
		cur, _ := readLateWrite(ctx, a, r.ID)
		if cur == nil {
			cur = &r
		}
		v := viewOfLate(ctx, a, *cur)
		v.N = number[r.ID]
		if reason != "" {
			out.Refused = append(out.Refused, LateRefused{Found: v, Why: why, Reason: reason})
			continue
		}
		out.Carried = append(out.Carried, v)
		a.Audit(ctx, "database.late.carry", "late_write", r.ID, "", map[string]any{"move": r.Move, "collection": r.Collection, "record": lateRecordWords(r.Key), "kind": r.Kind})
	}
	if len(recs) == 1 && len(out.Refused) == 1 {
		f := out.Refused[0]
		return nil, apperr.New(apperr.Conflict, "%s", f.Why).
			WithHint("Compare both versions, then leave it, or change this database's record yourself.").
			WithDetails(map[string]any{"reason": f.Reason, "id": f.Found.ID}).WithCommand(fmt.Sprintf("metarush database late-writes show %d", f.Found.N))
	}
	switch {
	case len(recs) == 0:
		out.Summary = "Nothing was waiting to be carried over."
	case len(out.Carried) > 0 && len(out.Refused) == 0:
		out.Summary = fmt.Sprintf("Carried %s over into this database.", plural(len(out.Carried), "record"))
	case len(out.Carried) > 0:
		out.Summary = fmt.Sprintf("Carried %s over; %s not: compare both versions of %s first.", plural(len(out.Carried), "record"), plural(len(out.Refused), "record"),
			map[bool]string{true: "it", false: "each"}[len(out.Refused) == 1])
	default:
		out.Summary = fmt.Sprintf("Nothing was carried over: %s first.", lowerFirst(strings.TrimSuffix(out.Refused[0].Why, ".")))
	}
	return out, nil
}

// carryLate carries one record over; a refusal is a reason and words.
func carryLate(ctx context.Context, a *app.App, r lateWriteRec, who string) (string, string, error) {
	switch {
	case r.State != LateWaiting:
		return "decided", "That record was " + map[string]string{LateCarried: "carried over", LateLeft: "left as it is here"}[r.State] + " already.", nil
	case r.Kind == dbmove.LateGone:
		return "gone", upperFirstWord(lateNoun(r.Collection)) + " was removed from the old database after the move, and MetaRush never removes it here by itself: if it should go, remove it here yourself, then leave it.", nil
	case r.TooLarge || r.Old == nil:
		return "too_large", upperFirstWord(lateNoun(r.Collection)) + " is too large for MetaRush to have kept a copy, so it can't carry it over.", nil
	}
	coll := a.DB.C(r.Collection)
	id := dbmove.IDOf(r.Key)
	changedHere := "This database's " + strings.TrimPrefix(strings.TrimPrefix(lateNoun(r.Collection), "a "), "an ") + " was changed too since the move, so carrying the old database's version over would lose that change."
	switch r.Kind {
	case dbmove.LateNew:
		if _, err := coll.InsertOne(ctx, r.Old); err != nil {
			if store.IsDuplicateKey(err) {
				if here, _, _ := copyState(ctx, a, r); here != nil {
					return "changed_here", "This database already has a record in its place, so it wasn't added.", nil
				}
				return "clash", "Another record in this database takes its place (the same name or key), so it wasn't added.", nil
			}
			return "", "", apperr.Wrap(apperr.Database, err, "MetaRush couldn't carry it over.")
		}
	default:
		here, changed, err := copyState(ctx, a, r)
		if err != nil {
			return "", "", apperr.Wrap(apperr.Database, err, "MetaRush couldn't read this database's record.")
		}
		if changed || here == nil {
			return "changed_here", changedHere, nil
		}
		// Check and set: replaced only while the record here is still, whole,
		// the one just read (the one the move copied).
		res, err := coll.ReplaceOne(ctx, bson.M{"_id": id, "$expr": bson.M{"$eq": bson.A{"$$ROOT", bson.M{"$literal": here}}}}, r.Old)
		if err != nil {
			if store.IsDuplicateKey(err) {
				return "clash", "Another record in this database takes its place (the same name or key), so it wasn't carried over.", nil
			}
			return "", "", apperr.Wrap(apperr.Database, err, "MetaRush couldn't carry it over.")
		}
		if res.MatchedCount == 0 {
			return "changed_here", changedHere, nil
		}
	}
	now := time.Now().UTC()
	_, err := a.DB.C(store.CLateWrites).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "state": LateWaiting},
		bson.M{"$set": bson.M{"state": LateCarried, "decidedBy": who, "decidedAt": now}})
	return "", "", err
}

// DatabaseLateLeave is database.late.leave: this database keeps its own
// record; the one found in the old database is only kept in the list.
func DatabaseLateLeave(ctx context.Context, a *app.App, in *DatabaseLateDecideIn) (*DatabaseLateLeaveOut, error) {
	if err := databasePeopleOnly(a); err != nil {
		return nil, err
	}
	recs, number, err := lateByRef(ctx, a, in.IDs, in.All)
	if err != nil {
		return nil, err
	}
	out := &DatabaseLateLeaveOut{Left: []LateWriteView{}}
	now := time.Now().UTC()
	who := firstNonEmpty(a.Actor.UserName, "a person")
	for _, r := range recs {
		if r.State != LateWaiting {
			continue
		}
		res, err := a.DB.C(store.CLateWrites).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "state": LateWaiting},
			bson.M{"$set": bson.M{"state": LateLeft, "decidedBy": who, "decidedAt": now}})
		if err != nil {
			return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't note it.")
		}
		if res.ModifiedCount == 0 {
			continue
		}
		r.State, r.DecidedBy, r.DecidedAt = LateLeft, who, &now
		v := viewOfLate(ctx, a, r)
		v.N = number[r.ID]
		out.Left = append(out.Left, v)
		a.Audit(ctx, "database.late.leave", "late_write", r.ID, "", map[string]any{"move": r.Move, "collection": r.Collection, "record": lateRecordWords(r.Key), "kind": r.Kind})
	}
	if len(out.Left) == 0 {
		out.Summary = "Nothing was waiting: those were decided already."
	} else {
		out.Summary = fmt.Sprintf("Left %s as %s here: this database keeps its own.", plural(len(out.Left), "record"), map[bool]string{true: "it is", false: "they are"}[len(out.Left) == 1])
	}
	return out, nil
}

/* ---------- MetaRush in the background ---------- */

// RunLateLooks takes the looks due on this computer (MetaRush in the
// background, every minute).
func RunLateLooks(ctx context.Context, a *app.App, logf func(string, ...any)) {
	t := time.NewTimer(20 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		for _, s := range LookDueLate(ctx, a, time.Now().UTC()) {
			logf("%s", s)
		}
		t.Reset(time.Minute)
	}
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
		if len(r.Due) == 0 || r.Done >= len(r.Due) {
			endLateLooks(ctx, a, a.DB, r.ID, "MetaRush made its last look at the old database.")
			continue
		}
		if now.Before(r.Due[r.Done]) {
			continue
		}
		if r.Tried != nil && now.Sub(*r.Tried) < lateRetryEvery {
			continue
		}
		found, err := lookAndRecord(ctx, a, a.DB, r, nil, LateLater, now)
		switch {
		case err == nil && found > 0:
			said = append(said, fmt.Sprintf("looked at the old database (%s) again: %s written there after the move", r.From, plural(found, "record")))
		case err == nil:
			said = append(said, fmt.Sprintf("looked at the old database (%s) again: nothing new", r.From))
		case now.After(r.Due[len(r.Due)-1].Add(LateLookGrace)):
			endLateLooks(ctx, a, a.DB, r.ID, "MetaRush couldn't reach the old database for its last look, so it stopped looking and no longer keeps its address.")
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
