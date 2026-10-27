// Package dbmove copies a workspace's whole database into another, empty one
// and checks the copy (`metarush database move`, D334–D338): built on the
// workspace export's engine (internal/wsarchive) — the source is only ever
// read; the new database gets MetaRush's own collections and indexes (its
// migrations up to the source's level, as a restore does) and every
// collection is streamed in _id order; the copy is locked from before
// its first write until it is whole and checked (the restore's lock, kind
// move); every collection's documents are hashed on both sides and must
// match; the migration level goes in last. Nothing is ever deleted, in either
// database.
//
// What switches, what is frozen meanwhile and who may do it is
// internal/ops's (databasemove.go): this package only plans, copies and
// checks.
package dbmove

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// LeftOut are the collections a move doesn't carry, each with why: the old
// database's own state, and what MetaRush writes again by itself. Everything
// else — every collection MetaRush keeps, and any other one the database
// holds — moves as it is, credentials included (it is the same workspace in
// another place, not an archive that leaves).
var LeftOut = map[string]string{
	store.CDatabaseMove: "the move's own marker: it stays in the old database",
	store.CRestoreLock:  "a restore's or a move's lock: the new database gets its own",
	store.CTestRun:      "a test database's marker",
	store.CWorkers:      "each computer's live record of what it can do now: written again when MetaRush starts",
	store.CAuthFailures: "counts of wrong passwords, kept a short while to slow guessing: about sign-ins to the old database only",
}

// Carried reports whether a move copies a collection.
func Carried(name string) bool {
	_, out := LeftOut[name]
	return !out && !strings.HasPrefix(name, "system.")
}

// Collection is one collection of a move: what the old database holds of it
// and, once moved, what the copy holds.
type MoveCollection struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
	// Bytes is about how much it holds (as stored, before compression).
	Bytes   int64 `json:"bytes"`
	Carried bool  `json:"carried"`
	// Why it stays behind (Carried false).
	Why string `json:"why,omitempty"`
	// Copied and Verified once moved; SHA256 is what the copy's documents
	// add up to (the last look before the old database is marked moved
	// compares the old one with it).
	Copied   int64  `json:"copied,omitempty"`
	Verified bool   `json:"verified,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	// TimeLimited: its records end by themselves (a TTL index); those ending
	// soon aren't compared.
	TimeLimited bool `json:"timeLimited,omitempty"`
}

// Survey is what a database holds, for a move's plan.
type Survey struct {
	Collections []MoveCollection `json:"collections"`
	// Documents and Bytes are what moves.
	Documents int64 `json:"documents"`
	Bytes     int64 `json:"bytes"`
	// Level is the database's migration level (it moves with it, last).
	Level wsarchive.MigrationLevel `json:"level"`
	// Others are workspaces the database holds beside the one moving.
	Others []string `json:"others,omitempty"`
	// Problems stop a move: things a copy can't carry.
	Problems []string `json:"problems,omitempty"`
}

// SurveySource reads what a database holds (read only: listCollections,
// $collStats, count, find on the workspaces).
func SurveySource(ctx context.Context, db *store.DB, workspace string) (*Survey, error) {
	infos, err := wsarchive.ListCollections(ctx, db)
	if err != nil {
		return nil, err
	}
	s := &Survey{Collections: []MoveCollection{}}
	for _, info := range infos {
		c := MoveCollection{Name: info.Name, Carried: Carried(info.Name)}
		if !c.Carried {
			c.Why = LeftOut[info.Name]
		}
		if info.Type != "" && info.Type != "collection" {
			c.Carried, c.Why = false, "a "+info.Type+", which a move can't copy"
			s.Problems = append(s.Problems, fmt.Sprintf("The database holds “%s”, a %s rather than ordinary records, which a move can't copy.", info.Name, info.Type))
		}
		c.Count, c.Bytes = Size(ctx, db, info.Name)
		if c.Carried {
			s.Documents += c.Count
			s.Bytes += c.Bytes
		}
		s.Collections = append(s.Collections, c)
	}
	if s.Level, err = wsarchive.ReadLevel(ctx, db); err != nil {
		return nil, err
	}
	all, err := wsarchive.Workspaces(ctx, db)
	if err != nil {
		return nil, err
	}
	for _, w := range all {
		if w.ID != workspace {
			s.Others = append(s.Others, firstOf(w.Name, w.ID))
		}
	}
	if len(s.Others) > 0 {
		s.Problems = append(s.Problems, fmt.Sprintf("The database holds other workspaces too (%s). Moving the database moves them all, so it is done only for a database of one workspace: export this one instead (Settings → Storage → Export the workspace).", strings.Join(quoted(s.Others), ", ")))
	}
	return s, nil
}

// Size counts a collection's documents and about how much they hold, from the
// server's own figures (no document is read); a count alone when it can't
// say more.
func Size(ctx context.Context, db *store.DB, coll string) (count, bytes int64) {
	cur, err := db.C(coll).Aggregate(ctx, mongo.Pipeline{{{Key: "$collStats", Value: bson.D{{Key: "storageStats", Value: bson.D{}}}}}})
	if err == nil {
		var rows []struct {
			Storage struct {
				Count any `bson:"count"`
				Size  any `bson:"size"`
			} `bson:"storageStats"`
		}
		if cur.All(ctx, &rows) == nil && len(rows) > 0 {
			return toInt(rows[0].Storage.Count), toInt(rows[0].Storage.Size)
		}
	}
	n, _ := db.C(coll).CountDocuments(ctx, bson.M{})
	return n, 0
}

func toInt(v any) int64 {
	switch x := v.(type) {
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case int:
		return int64(x)
	}
	return 0
}

// Target is what the new database is like, checked without writing to it.
type TargetCheck struct {
	// Reached: MetaRush signed in and the database answered.
	Reached bool `json:"reached"`
	// Empty: it holds nothing (a copy may go in); Holds lists what it holds.
	Empty bool     `json:"empty"`
	Holds []string `json:"holds,omitempty"`
	// Resume: it holds this very move's copy, stopped part-way (the move
	// continues it); Copied: this move's copy, whole and checked.
	Resume bool `json:"resume"`
	Copied bool `json:"copied"`
	// Writable: the sign-in may make collections and indexes and write
	// documents there.
	Writable bool `json:"writable"`
	// Version is the MongoDB server's version.
	Version string `json:"version,omitempty"`
	// Problem says, plainly, what stops a move there ("" none).
	Problem string `json:"problem,omitempty"`
}

// CheckTarget looks at the new database without writing anything: whether
// it answers, holds nothing (or this move's own unfinished copy) and lets
// this sign-in write.
func CheckTarget(ctx context.Context, db *store.DB, move string) TargetCheck {
	t := TargetCheck{}
	ver, _, err := db.ServerInfo(ctx)
	if err != nil {
		t.Problem = "MetaRush couldn't reach the new database, or it didn't take the sign-in."
		return t
	}
	t.Reached, t.Version = true, ver
	lock, err := db.ReadRestoreLock(ctx)
	if err != nil {
		t.Problem = "MetaRush couldn't look into the new database: the sign-in may not be allowed to read it."
		return t
	}
	holds, err := wsarchive.Holds(ctx, db)
	if err != nil {
		t.Problem = "MetaRush couldn't look into the new database: the sign-in may not be allowed to read it."
		return t
	}
	t.Holds = holds
	switch {
	case lock != nil && lock.Kind == store.LockMove && lock.Archive == move && lock.State == store.RestoreDone:
		t.Copied = true
	case lock != nil && lock.Kind == store.LockMove && lock.Archive == move && lock.Unfinished():
		t.Resume = true
	case lock != nil && lock.Unfinished():
		t.Problem = "The new database holds part of another copy that didn't finish, and a move goes only into an empty database."
	case len(holds) > 0:
		t.Problem = "The new database already holds things, and a move goes only into an empty one. MetaRush never deletes anything: choose a new database (another name), or empty this one yourself first."
	default:
		t.Empty = true
	}
	t.Writable = CanWrite(ctx, db)
	if !t.Writable && t.Problem == "" {
		t.Problem = "The sign-in for the new database may read it but not write into it. Give it the right to read and write that database (readWrite), then check again."
	}
	return t
}

// writeActions are what a copy does in the new database.
var writeActions = []string{"insert", "createCollection", "createIndex", "update", "find"}

// CanWrite asks the server what the sign-in may do in the database
// (connectionStatus with its privileges): nothing is written to find out. A
// server that signs nobody in (a test server) lets anyone write.
func CanWrite(ctx context.Context, db *store.DB) bool {
	var st struct {
		AuthInfo struct {
			Users      []bson.M `bson:"authenticatedUsers"`
			Privileges []struct {
				Resource struct {
					DB         *string `bson:"db"`
					Collection *string `bson:"collection"`
					Cluster    bool    `bson:"cluster"`
					AnyRes     bool    `bson:"anyResource"`
				} `bson:"resource"`
				Actions []string `bson:"actions"`
			} `bson:"authenticatedUserPrivileges"`
		} `bson:"authInfo"`
	}
	if err := db.DB.RunCommand(ctx, bson.D{{Key: "connectionStatus", Value: 1}, {Key: "showPrivileges", Value: true}}).Decode(&st); err != nil {
		return false
	}
	if len(st.AuthInfo.Users) == 0 {
		return true // no sign-in: a server without access control
	}
	have := map[string]bool{}
	for _, p := range st.AuthInfo.Privileges {
		r := p.Resource
		applies := r.AnyRes
		if r.DB != nil && r.Collection != nil && (*r.DB == db.Name || *r.DB == "") && *r.Collection == "" {
			applies = true
		}
		if !applies {
			continue
		}
		for _, a := range p.Actions {
			have[a] = true
		}
	}
	for _, a := range writeActions {
		if !have[a] {
			return false
		}
	}
	return true
}

// Progress is how far a copy is.
type CopyProgress struct {
	Phase string `json:"phase" jsonschema:"enum=copying,enum=checking"`
	// Collection is the one being copied or checked.
	Collection      string `json:"collection,omitempty"`
	Collections     int    `json:"collections"`
	CollectionsDone int    `json:"collectionsDone"`
	Documents       int64  `json:"documents"`
	DocumentsDone   int64  `json:"documentsDone"`
}

// Options say what to copy where.
type Options struct {
	From, To *store.DB
	// Workspace and WorkspaceName are what the copy holds (the lock names them).
	Workspace, WorkspaceName string
	// Move is the move's identity (the lock names it, so the same move run
	// again continues its own copy and nobody else's).
	Move string
	// Stop is asked between collections and batches: an error stops the copy
	// (a person abandoned the move). The copy keeps its lock.
	Stop     func(ctx context.Context) error
	Progress func(CopyProgress)
}

// Result is what a copy did.
type Result struct {
	Collections []MoveCollection `json:"collections"`
	Documents   int64            `json:"documents"`
	// Resumed: the copy continued one that had stopped.
	Resumed bool `json:"resumed"`
	// Verified: every collection's documents are the same on both sides.
	Verified bool `json:"verified"`
	// IndexesLeft are indexes of the old database MetaRush doesn't make (the
	// new one has MetaRush's own), in words.
	IndexesLeft []string `json:"indexesLeft,omitempty"`
	Seconds     float64
}

// ErrChanged is a copy whose old database changed meanwhile: something wrote
// into it during the move (a computer that didn't pause).
var ErrChanged = errors.New("the old database changed during the move")

// Partial is a copy that stopped once something was in the new database:
// its lock stays, so nothing uses a half copy.
type Partial struct {
	Collection string
	Err        error
}

func (p *Partial) Error() string {
	if p.Collection == "" {
		return p.Err.Error()
	}
	return fmt.Sprintf("%s: %v", p.Collection, p.Err)
}

func (p *Partial) Unwrap() error { return p.Err }

// Copy copies every carried collection of the old database into the new,
// empty one (or continues this move's own copy there), the migration level
// last, then checks every collection's documents on both sides by hash and
// marks the copy whole. It only reads the old database.
func Copy(ctx context.Context, o Options) (*Result, error) {
	started := time.Now()
	res := &Result{Collections: []MoveCollection{}}
	lock, err := o.To.ReadRestoreLock(ctx)
	if err != nil {
		return nil, err
	}
	holds, err := wsarchive.Holds(ctx, o.To)
	if err != nil {
		return nil, err
	}
	mine := lock != nil && lock.Kind == store.LockMove && lock.Archive == o.Move
	switch {
	case mine && lock.State == store.RestoreDone:
		res.Resumed = true // copied and checked before: checked again below
	case mine && lock.Unfinished():
		res.Resumed = true
	case lock != nil && lock.Unfinished():
		return nil, errors.New("the new database holds part of another copy")
	case len(holds) > 0:
		return nil, fmt.Errorf("the new database already holds %s", strings.Join(holds, ", "))
	}
	// The lock first: from now on nothing else works in the new database
	// until the copy is whole and checked.
	if !mine || lock.State != store.RestoreDone {
		if err := o.To.SetMoveLock(ctx, o.Move, o.Workspace, o.WorkspaceName, store.RestoreCopying); err != nil {
			return nil, err
		}
		// MetaRush's own collections and indexes, as its migrations up to the
		// old database's level make them (never the old database's own list,
		// as a restore never takes an archive's), before the first record: a
		// copy that stopped once records went in made them already.
		has, err := holdsRecords(ctx, o.To)
		if err != nil {
			return nil, err
		}
		if !has {
			lvl, err := wsarchive.ReadLevel(ctx, o.From)
			if err != nil {
				return nil, err
			}
			if err := o.To.MakeForRestore(ctx, lvl.Versions()); err != nil {
				return nil, &Partial{Err: err}
			}
		}
	}
	infos, err := wsarchive.ListCollections(ctx, o.From)
	if err != nil {
		return nil, err
	}
	var order []wsarchive.CollectionInfo
	var level *wsarchive.CollectionInfo
	for i := range infos {
		switch {
		case !Carried(infos[i].Name):
		case infos[i].Name == store.CMigrations:
			level = &infos[i]
		default:
			order = append(order, infos[i])
		}
	}
	// The migration level last: a copy that stopped part-way never says it
	// is up to date.
	if level != nil {
		order = append(order, *level)
	}
	total := int64(0)
	for _, info := range order {
		n, _ := Size(ctx, o.From, info.Name)
		total += n
	}
	p := CopyProgress{Phase: "copying", Collections: len(order), Documents: total}
	progress := func() {
		if o.Progress != nil {
			o.Progress(p)
		}
	}
	stop := func() error {
		if o.Stop != nil {
			return o.Stop(ctx)
		}
		return ctx.Err()
	}
	if !mine || lock.State != store.RestoreDone {
		for i, info := range order {
			if err := stop(); err != nil {
				return nil, &Partial{Err: err}
			}
			p.Collection, p.CollectionsDone = info.Name, i
			progress()
			if err := copyCollection(ctx, o, info, &p, progress, stop); err != nil {
				return nil, &Partial{Collection: info.Name, Err: err}
			}
		}
	}
	// The check: every collection's documents, read again on both sides.
	p.Phase, p.DocumentsDone = "checking", 0
	for i, info := range order {
		p.Collection, p.CollectionsDone = info.Name, i
		progress()
		src, dst, err := hashBoth(ctx, o.From, o.To, info.Name)
		if err != nil {
			return nil, &Partial{Collection: info.Name, Err: err}
		}
		p.DocumentsDone += src.Count
		c := MoveCollection{Name: info.Name, Carried: true, Count: src.Count, Bytes: src.Bytes, Copied: dst.Count, Verified: src == dst, SHA256: dst.SHA256,
			TimeLimited: len(timeLimits(ctx, o.From, info.Name)) > 0}
		res.Collections = append(res.Collections, c)
		res.Documents += dst.Count
		if src != dst {
			return res, &Partial{Collection: info.Name, Err: ErrChanged}
		}
	}
	p.CollectionsDone, p.Collection = len(order), ""
	progress()
	// A kind of record made in the old database meanwhile was never copied.
	if late, err := lateCollections(ctx, o.From, order); err != nil {
		return res, &Partial{Err: err}
	} else if late != "" {
		return res, &Partial{Collection: late, Err: ErrChanged}
	}
	names := make([]string, 0, len(order))
	for _, info := range order {
		names = append(names, info.Name)
	}
	if left := wsarchive.IndexesNotMade(ctx, o.From, o.To, names); len(left) > 0 {
		res.IndexesLeft = left
	}
	if err := o.To.SetMoveLock(ctx, o.Move, o.Workspace, o.WorkspaceName, store.RestoreDone); err != nil {
		return res, err
	}
	res.Verified = true
	res.Seconds = time.Since(started).Seconds()
	sort.Slice(res.Collections, func(i, j int) bool { return res.Collections[i].Name < res.Collections[j].Name })
	return res, nil
}

// lateCollections names a carried collection the old database holds records
// in that isn't among those copied ("" none).
func lateCollections(ctx context.Context, from *store.DB, copied []wsarchive.CollectionInfo) (string, error) {
	known := map[string]bool{}
	for _, c := range copied {
		known[c.Name] = true
	}
	infos, err := wsarchive.ListCollections(ctx, from)
	if err != nil {
		return "", err
	}
	for _, info := range infos {
		if !Carried(info.Name) || known[info.Name] {
			continue
		}
		if n, err := from.C(info.Name).CountDocuments(ctx, bson.M{}, options.Count().SetLimit(1)); err != nil {
			return "", err
		} else if n > 0 {
			return info.Name, nil
		}
	}
	return "", nil
}

// Recheck is the last look at the old database, right before it is marked
// moved (it is frozen by then): every kind of record is listed again and read
// again, and must add up to exactly what the copy holds. A kind made since,
// a record written or removed since, stops the move (ErrChanged).
func Recheck(ctx context.Context, from, to *store.DB, res *Result) error {
	if res == nil {
		return errors.New("nothing was copied")
	}
	copied := map[string]MoveCollection{}
	var order []wsarchive.CollectionInfo
	for _, c := range res.Collections {
		copied[c.Name] = c
		order = append(order, wsarchive.CollectionInfo{Name: c.Name})
	}
	if late, err := lateCollections(ctx, from, order); err != nil {
		return &Partial{Err: err}
	} else if late != "" {
		return &Partial{Collection: late, Err: ErrChanged}
	}
	for _, c := range res.Collections {
		if c.TimeLimited {
			// Records that end by themselves are read again on both sides,
			// those ending soon left out (either side may delete them by now).
			a, b, err := hashBoth(ctx, from, to, c.Name)
			if err != nil {
				return &Partial{Collection: c.Name, Err: err}
			}
			if a != b {
				return &Partial{Collection: c.Name, Err: ErrChanged}
			}
			continue
		}
		d, err := wsarchive.HashWhere(ctx, from, c.Name, nil)
		if err != nil {
			return &Partial{Collection: c.Name, Err: err}
		}
		if d.Count != c.Copied || d.SHA256 != c.SHA256 {
			return &Partial{Collection: c.Name, Err: ErrChanged}
		}
	}
	return nil
}

/* ---------- records that end by themselves ---------- */

// endingSoon is how close to its end a time-limited record may be and still
// be compared: one that ends sooner may be deleted on either side by now.
const endingSoon = 15 * time.Minute

// timeLimit is a TTL index: its field and how long after it a record goes.
type timeLimit struct {
	field string
	after int64
}

// timeLimits lists a collection's TTL indexes (one field each).
func timeLimits(ctx context.Context, db *store.DB, coll string) []timeLimit {
	ixs, err := wsarchive.ListIndexes(ctx, db, coll)
	if err != nil {
		return nil
	}
	var out []timeLimit
	for _, ix := range ixs {
		v, err := ix.LookupErr("expireAfterSeconds")
		if err != nil {
			continue
		}
		after, ok := v.AsInt64OK()
		if !ok {
			if f, okf := v.DoubleOK(); okf {
				after, ok = int64(f), true
			}
		}
		key, err := ix.LookupErr("key")
		if err != nil || !ok {
			continue
		}
		elems, err := key.Document().Elements()
		if err != nil || len(elems) != 1 {
			continue
		}
		out = append(out, timeLimit{field: elems[0].Key(), after: after})
	}
	return out
}

// serverNow is the database server's own time (every computer's clock may differ).
func serverNow(ctx context.Context, db *store.DB) time.Time {
	var h struct {
		LocalTime time.Time `bson:"localTime"`
	}
	if err := db.DB.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&h); err != nil || h.LocalTime.IsZero() {
		return time.Now().UTC()
	}
	return h.LocalTime.UTC()
}

// stillLive is the records of a collection with TTL indexes that won't end
// within endingSoon (records whose field isn't a date never end).
func stillLive(limits []timeLimit, now time.Time) bson.M {
	var and bson.A
	for _, l := range limits {
		cutoff := now.Add(endingSoon).Add(-time.Duration(l.after) * time.Second)
		and = append(and, bson.M{"$or": bson.A{
			bson.M{l.field: bson.M{"$not": bson.M{"$type": "date"}}},
			bson.M{l.field: bson.M{"$gt": cutoff}},
		}})
	}
	return bson.M{"$and": and}
}

// hashBoth digests a collection on both sides: every record, or for one with
// TTL indexes (in the old database) the records that don't end soon, by one
// time — the old database server's.
func hashBoth(ctx context.Context, from, to *store.DB, coll string) (wsarchive.Digest, wsarchive.Digest, error) {
	var filter bson.M
	if limits := timeLimits(ctx, from, coll); len(limits) > 0 {
		filter = stillLive(limits, serverNow(ctx, from))
	}
	a, err := wsarchive.HashWhere(ctx, from, coll, filter)
	if err != nil {
		return a, wsarchive.Digest{}, err
	}
	b, err := wsarchive.HashWhere(ctx, to, coll, filter)
	return a, b, err
}

// copyCollection streams a collection's documents into the new database, in
// _id order (MetaRush made its own collections there; any other is made as
// its first document goes in) — or, when this move already copied some,
// continues after the documents that are there (an ordered copy leaves a
// prefix of the stream; the check afterwards proves it).
func copyCollection(ctx context.Context, o Options, info wsarchive.CollectionInfo, p *CopyProgress, progress func(), stop func() error) error {
	if limits := timeLimits(ctx, o.From, info.Name); len(limits) > 0 {
		return copyTimeLimited(ctx, o, info.Name, limits, p, progress, stop)
	}
	have, err := o.To.C(info.Name).CountDocuments(ctx, bson.M{})
	if err != nil {
		return err
	}
	in := wsarchive.NewInserter(ctx, o.To, info.Name)
	var seen int64
	err = wsarchive.EachDocWhere(ctx, o.From, info.Name, nil, func(doc bson.Raw) error {
		seen++
		p.DocumentsDone++
		if seen <= have {
			return nil
		}
		if err := in.Add(doc); err != nil {
			return err
		}
		if seen%wsarchive.InsertBatch == 0 {
			progress()
			if err := stop(); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return in.Flush()
}

// copyTimeLimited copies the records of a collection with TTL indexes (sign-in
// links) that don't end soon, each put in place by its _id. The new database
// may delete ended ones by itself meanwhile (pausing its time limits takes
// more than a database user that may read and write records), so what is
// there says nothing of how far a copy got: a copy that runs again puts each
// in place again. The check compares the same records on both sides.
func copyTimeLimited(ctx context.Context, o Options, coll string, limits []timeLimit, p *CopyProgress, progress func(), stop func() error) error {
	var n int64
	return wsarchive.EachDocWhere(ctx, o.From, coll, stillLive(limits, serverNow(ctx, o.From)), func(doc bson.Raw) error {
		n++
		p.DocumentsDone++
		id, err := doc.LookupErr("_id")
		if err != nil {
			return err
		}
		if _, err := o.To.C(coll).ReplaceOne(ctx, bson.D{{Key: "_id", Value: id}}, doc, options.Replace().SetUpsert(true)); err != nil {
			return err
		}
		if n%wsarchive.InsertBatch == 0 {
			progress()
			return stop()
		}
		return nil
	})
}

// holdsRecords reports whether a database holds any record but a lock's or
// a test database's own marker.
func holdsRecords(ctx context.Context, db *store.DB) (bool, error) {
	names, err := db.DB.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		return false, err
	}
	for _, n := range names {
		if n == store.CRestoreLock || n == store.CTestRun || strings.HasPrefix(n, "system.") {
			continue
		}
		if c, err := db.C(n).CountDocuments(ctx, bson.M{}, options.Count().SetLimit(1)); err != nil {
			return false, err
		} else if c > 0 {
			return true, nil
		}
	}
	return false, nil
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func quoted(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "“" + x + "”"
	}
	return out
}
