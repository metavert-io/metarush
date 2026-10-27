package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/apperr"
)

// The freeze, in the database itself (D335): a database a move keeps changes
// out of refuses every write that goes through this DB — an operation's, a
// background loop's, a handler's or the activity log's alike — except the
// move's own marker. A change that was already running when the move began
// (Admit), or a worker handing its job back (WriteThroughFreeze), writes on
// only while the move knows about it: listed in the marker (busy) before it
// writes, and only until the move starts copying (MoveCopying sets that only
// while nothing is listed) — never once the workspace moved, and never after
// the change ended (work detached from it gets no pass). Every DB looks at the
// marker before a write when its last look is older than MoveLookTrusted (a
// computer waking from sleep); one that watches (WatchMoves, every App opened
// by MetaRush) looks about every MoveLookEvery anyway. When the marker can't
// be read, a write is refused unless the last look, recent, showed no move.

// How often a watching DB looks at the move marker, and how old a look it
// trusts.
var (
	MoveLookEvery   = 750 * time.Millisecond
	MoveLookTrusted = time.Second
	// MoveLookGuess is how recent a look that showed no move must be for a
	// write to go on when the marker can't be read.
	MoveLookGuess = 30 * time.Second
)

// freeze is a DB's last look at the marker and the work it lets through.
type freeze struct {
	mu        sync.Mutex
	m         *MoveMarker
	at        time.Time // wall clock of the last look (no monotonic reading: sleep counts)
	watched   bool
	who       string // the installation that watches (the marker's busy key)
	cancel    context.CancelFunc
	inflight  map[string]int // operations let through the freeze, by name
	published bool           // the marker lists this process's work
}

func (d *DB) fz() *freeze {
	if d == nil {
		return nil
	}
	d.fzOnce.Do(func() { d.freeze = &freeze{inflight: map[string]int{}} })
	return d.freeze
}

// ErrFrozen is a write refused because a move keeps changes out of the
// database (an *ErrDatabaseMoving).
var ErrFrozen = errors.New("the workspace's database is being moved")

// WatchMoves makes the DB look at its move marker about every MoveLookEvery
// until it is closed, and refuse writes while the marker freezes it. who is
// this computer's installation (the work it lets through is listed under it
// in the marker, so the move waits for it).
func (d *DB) WatchMoves(who string) {
	f := d.fz()
	f.mu.Lock()
	if f.watched {
		f.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	f.watched, f.who, f.cancel = true, who, cancel
	f.mu.Unlock()
	lctx, lcancel := context.WithTimeout(ctx, 5*time.Second)
	_, _ = d.lookAtMarker(lctx)
	lcancel()
	go func() {
		t := time.NewTicker(MoveLookEvery)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			lctx, lcancel := context.WithTimeout(ctx, 5*time.Second)
			if _, err := d.lookAtMarker(lctx); err == nil {
				d.publishBusy(lctx)
			}
			lcancel()
		}
	}()
}

// stopWatching ends the watcher (Close).
func (d *DB) stopWatching() {
	if f := d.fz(); f != nil {
		f.mu.Lock()
		if f.cancel != nil {
			f.cancel()
		}
		f.mu.Unlock()
	}
}

// lookAtMarker reads the marker and keeps it as the last look.
func (d *DB) lookAtMarker(ctx context.Context) (*MoveMarker, error) {
	m, err := d.ReadMoveMarker(ctx)
	if err != nil {
		return nil, err
	}
	d.SetMoveMarkerCache(m)
	return m, nil
}

// SetMoveMarkerCache records a marker just read or written as the last look.
func (d *DB) SetMoveMarkerCache(m *MoveMarker) {
	f := d.fz()
	if f == nil {
		return
	}
	f.mu.Lock()
	f.m, f.at = m, time.Now().Round(0)
	f.mu.Unlock()
}

// MoveMarkerNow is the move marker: the last look while it is at most
// MoveLookTrusted old (wall clock), else read again.
func (d *DB) MoveMarkerNow(ctx context.Context) (*MoveMarker, error) {
	f := d.fz()
	f.mu.Lock()
	m, at := f.m, f.at
	f.mu.Unlock()
	if !at.IsZero() && time.Since(at) <= MoveLookTrusted && time.Since(at) >= 0 {
		return m, nil
	}
	return d.lookAtMarker(ctx)
}

// MoveMarkerSeen is the last look at the marker (no read).
func (d *DB) MoveMarkerSeen() *MoveMarker {
	f := d.fz()
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.m
}

// FrozenNow reports whether a move keeps changes out of the database now
// (looked at again when the last look is too old).
func (d *DB) FrozenNow(ctx context.Context) bool {
	m, err := d.MoveMarkerNow(ctx)
	if err != nil {
		return d.MoveMarkerSeen().Frozen()
	}
	return m.Frozen()
}

type throughKey struct{}

// passage is a context's way through a freeze that begins while it runs. It
// ends with its change (Admit's done): work detached from the change
// (context.WithoutCancel keeps the value) gets no pass once it ended.
type passage struct {
	op    string
	ended atomic.Bool
}

// WriteThroughFreeze lets a context's writes through a freeze while the
// move knows about them (a worker handing its job back as the move begins):
// listed in the marker first, and only until the move copies.
func WriteThroughFreeze(ctx context.Context) context.Context {
	return context.WithValue(ctx, throughKey{}, &passage{op: "handing back a job"})
}

func passageOf(ctx context.Context) *passage {
	p, _ := ctx.Value(throughKey{}).(*passage)
	if p == nil || p.ended.Load() {
		return nil
	}
	return p
}

// WritesThrough reports whether a context belongs to a change that may write
// through a freeze that began while it runs (it still runs).
func WritesThrough(ctx context.Context) bool { return passageOf(ctx) != nil }

// Admit lets an operation that passed the freeze check write through a
// freeze that begins while it runs, the way guard says, and counts it as
// running (the marker lists it while the database is being moved, and the
// move waits for it). done ends it, and its pass.
func (d *DB) Admit(ctx context.Context, op string) (context.Context, func()) {
	f := d.fz()
	if f == nil {
		return ctx, func() {}
	}
	f.mu.Lock()
	f.inflight[op]++
	f.mu.Unlock()
	p := &passage{op: op}
	var once sync.Once
	return context.WithValue(ctx, throughKey{}, p), func() {
		once.Do(func() {
			p.ended.Store(true)
			f.mu.Lock()
			if f.inflight[op]--; f.inflight[op] <= 0 {
				delete(f.inflight, op)
			}
			idle := len(f.inflight) == 0 && f.published
			f.mu.Unlock()
			if idle {
				c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				d.publishBusy(c)
				cancel()
			}
		})
	}
}

// busyKey is this process's entry in the marker's list of running work.
func (f *freeze) busyKey() string { return fmt.Sprintf("%s-%d", f.who, os.Getpid()) }

// publishBusy lists the work this process let through the freeze in the
// marker while the database is being moved, and takes it off once it ended.
func (d *DB) publishBusy(ctx context.Context) {
	f := d.fz()
	f.mu.Lock()
	m := f.m
	var ops []string
	for op := range f.inflight {
		ops = append(ops, op)
	}
	published, key := f.published, f.busyKey()
	f.mu.Unlock()
	sort.Strings(ops)
	switch {
	case m == nil || m.State != MoveMoving || f.who == "":
		return
	case len(ops) > 0:
		_, err := d.C(CDatabaseMove).UpdateOne(ctx, bson.M{"_id": moveMarkerID, "move": m.Move, "state": MoveMoving, "copying": bson.M{"$ne": true}},
			bson.M{"$set": bson.M{"busy." + key + ".installation": f.who, "busy." + key + ".ops": ops},
				"$currentDate": bson.M{"busy." + key + ".at": true}})
		if err == nil {
			f.mu.Lock()
			f.published = true
			f.mu.Unlock()
		}
	case published:
		_, err := d.C(CDatabaseMove).UpdateOne(ctx, bson.M{"_id": moveMarkerID}, bson.M{"$unset": bson.M{"busy." + key: ""}})
		if err == nil {
			f.mu.Lock()
			f.published = false
			f.mu.Unlock()
		}
	}
}

// MoveMarkerForChange is the marker a change goes by: looked at again when
// the last look is too old. When it can't be read, the last look stands in
// only when it is recent and showed no move; otherwise the change is refused
// (an *apperr.Error, E_DATABASE): MetaRush never guesses during a move.
func (d *DB) MoveMarkerForChange(ctx context.Context) (*MoveMarker, error) {
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	m, err := d.MoveMarkerNow(lctx)
	if err == nil {
		return m, nil
	}
	f := d.fz()
	f.mu.Lock()
	seen, at := f.m, f.at
	f.mu.Unlock()
	if !at.IsZero() && time.Since(at) <= MoveLookGuess && (seen == nil || seen.State == MoveAbandoned) {
		return seen, nil
	}
	return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't check whether the workspace's database is being moved, so it changed nothing.").
		WithHint("Try again in a moment.")
}

// guard refuses a write while a move keeps changes out of the database
// (every collection but the marker's): see the top of this file.
func (d *DB) guard(ctx context.Context, coll string) error {
	if coll == CDatabaseMove {
		return nil
	}
	if d.fz() == nil {
		return nil
	}
	m, err := d.MoveMarkerForChange(ctx)
	if err != nil {
		return err
	}
	if !m.Frozen() {
		d.sayFirst(ctx)
		return nil
	}
	if p := passageOf(ctx); p != nil && m.State == MoveMoving && !m.Copying {
		return d.listPassage(ctx, m, p)
	}
	return &ErrDatabaseMoving{Marker: *m}
}

// listPassage lets a running change's write through a move that is still
// waiting: its process is listed in the marker first (so the move waits for
// it) — in one step that fails once the move copies, or the marker changed.
func (d *DB) listPassage(ctx context.Context, m *MoveMarker, p *passage) error {
	f := d.fz()
	f.mu.Lock()
	who, key := f.who, f.busyKey()
	ops := []string{p.op}
	for op := range f.inflight {
		if op != p.op {
			ops = append(ops, op)
		}
	}
	f.mu.Unlock()
	sort.Strings(ops)
	if who == "" {
		return &ErrDatabaseMoving{Marker: *m} // a connection of no computer: nothing to list
	}
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	res, err := d.C(CDatabaseMove).UpdateOne(lctx, bson.M{"_id": moveMarkerID, "move": m.Move, "state": MoveMoving, "copying": bson.M{"$ne": true}},
		bson.M{"$set": bson.M{"busy." + key + ".installation": who, "busy." + key + ".ops": ops},
			"$currentDate": bson.M{"busy." + key + ".at": true}})
	if err != nil {
		return apperr.Wrap(apperr.Database, err, "MetaRush couldn't check whether the workspace's database is being moved, so it changed nothing.").
			WithHint("Try again in a moment.")
	}
	if res.MatchedCount == 0 {
		cur, err := d.lookAtMarker(lctx)
		switch {
		case err == nil && !cur.Frozen():
			return nil // abandoned meanwhile: changes go on
		case err == nil:
			return &ErrDatabaseMoving{Marker: *cur}
		}
		return &ErrDatabaseMoving{Marker: *m}
	}
	f.mu.Lock()
	f.published = true
	f.mu.Unlock()
	return nil
}

// Coll is a collection whose writes go through the freeze (C). Reads are
// the driver's own.
type Coll struct {
	*mongo.Collection
	db *DB
}

func (c *Coll) refuse(ctx context.Context) error { return c.db.guard(ctx, c.Name()) }

func (c *Coll) InsertOne(ctx context.Context, document any, opts ...options.Lister[options.InsertOneOptions]) (*mongo.InsertOneResult, error) {
	if err := c.refuse(ctx); err != nil {
		return nil, err
	}
	return c.Collection.InsertOne(ctx, document, opts...)
}

func (c *Coll) InsertMany(ctx context.Context, documents any, opts ...options.Lister[options.InsertManyOptions]) (*mongo.InsertManyResult, error) {
	if err := c.refuse(ctx); err != nil {
		return nil, err
	}
	return c.Collection.InsertMany(ctx, documents, opts...)
}

func (c *Coll) DeleteOne(ctx context.Context, filter any, opts ...options.Lister[options.DeleteOneOptions]) (*mongo.DeleteResult, error) {
	if err := c.refuse(ctx); err != nil {
		return nil, err
	}
	return c.Collection.DeleteOne(ctx, filter, opts...)
}

func (c *Coll) DeleteMany(ctx context.Context, filter any, opts ...options.Lister[options.DeleteManyOptions]) (*mongo.DeleteResult, error) {
	if err := c.refuse(ctx); err != nil {
		return nil, err
	}
	return c.Collection.DeleteMany(ctx, filter, opts...)
}

func (c *Coll) UpdateByID(ctx context.Context, id any, update any, opts ...options.Lister[options.UpdateOneOptions]) (*mongo.UpdateResult, error) {
	if err := c.refuse(ctx); err != nil {
		return nil, err
	}
	return c.Collection.UpdateByID(ctx, id, update, opts...)
}

func (c *Coll) UpdateOne(ctx context.Context, filter any, update any, opts ...options.Lister[options.UpdateOneOptions]) (*mongo.UpdateResult, error) {
	if err := c.refuse(ctx); err != nil {
		return nil, err
	}
	return c.Collection.UpdateOne(ctx, filter, update, opts...)
}

func (c *Coll) UpdateMany(ctx context.Context, filter any, update any, opts ...options.Lister[options.UpdateManyOptions]) (*mongo.UpdateResult, error) {
	if err := c.refuse(ctx); err != nil {
		return nil, err
	}
	return c.Collection.UpdateMany(ctx, filter, update, opts...)
}

func (c *Coll) ReplaceOne(ctx context.Context, filter any, replacement any, opts ...options.Lister[options.ReplaceOptions]) (*mongo.UpdateResult, error) {
	if err := c.refuse(ctx); err != nil {
		return nil, err
	}
	return c.Collection.ReplaceOne(ctx, filter, replacement, opts...)
}

func (c *Coll) BulkWrite(ctx context.Context, models []mongo.WriteModel, opts ...options.Lister[options.BulkWriteOptions]) (*mongo.BulkWriteResult, error) {
	if err := c.refuse(ctx); err != nil {
		return nil, err
	}
	return c.Collection.BulkWrite(ctx, models, opts...)
}

func (c *Coll) FindOneAndDelete(ctx context.Context, filter any, opts ...options.Lister[options.FindOneAndDeleteOptions]) *mongo.SingleResult {
	if err := c.refuse(ctx); err != nil {
		return mongo.NewSingleResultFromDocument(bson.D{}, err, nil)
	}
	return c.Collection.FindOneAndDelete(ctx, filter, opts...)
}

func (c *Coll) FindOneAndReplace(ctx context.Context, filter any, replacement any, opts ...options.Lister[options.FindOneAndReplaceOptions]) *mongo.SingleResult {
	if err := c.refuse(ctx); err != nil {
		return mongo.NewSingleResultFromDocument(bson.D{}, err, nil)
	}
	return c.Collection.FindOneAndReplace(ctx, filter, replacement, opts...)
}

func (c *Coll) FindOneAndUpdate(ctx context.Context, filter any, update any, opts ...options.Lister[options.FindOneAndUpdateOptions]) *mongo.SingleResult {
	if err := c.refuse(ctx); err != nil {
		return mongo.NewSingleResultFromDocument(bson.D{}, err, nil)
	}
	return c.Collection.FindOneAndUpdate(ctx, filter, update, opts...)
}

func (c *Coll) Drop(ctx context.Context, opts ...options.Lister[options.DropCollectionOptions]) error {
	if err := c.refuse(ctx); err != nil {
		return err
	}
	return c.Collection.Drop(ctx, opts...)
}

// ServerNow is the database server's own time (computers' clocks differ).
func (d *DB) ServerNow(ctx context.Context) (time.Time, error) {
	var h struct {
		LocalTime time.Time `bson:"localTime"`
	}
	if err := d.DB.RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&h); err != nil {
		return time.Time{}, err
	}
	if h.LocalTime.IsZero() {
		return time.Time{}, errors.New("the database didn't say its time")
	}
	return h.LocalTime.UTC(), nil
}
