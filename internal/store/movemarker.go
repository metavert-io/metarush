package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/apperr"
)

// CDatabaseMove holds the marker a move of the database keeps in the
// database it moves from (D335): while the workspace is being copied to
// another database every computer refuses changes, and once it moved the
// marker stays for good, so no computer still using the old database ever
// changes it (the two can never split). It names where the workspace went in
// words — the kind of place, its host and the database's name — never a
// sign-in.
const CDatabaseMove = "database_move"

// A move's states, as the old database's marker says them.
const (
	MoveMoving    = "moving"    // the copy is being made: changes wait
	MoveMoved     = "moved"     // the workspace lives in another database now: this one is kept, never changed
	MoveAbandoned = "abandoned" // a person abandoned the move: changes go on here
)

const moveMarkerID = "move"

// Clock is this computer's clock (tests make it run slow or fast).
var Clock = time.Now

// MoveTarget is where a move takes the workspace, in words and without any
// sign-in.
type MoveTarget struct {
	// Kind is here (the database on the computer that moved it), atlas
	// (MongoDB Atlas) or server (another MongoDB server).
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=here,enum=atlas,enum=server"`
	// Words say it for people: "MongoDB Atlas", "a MongoDB server on your network".
	Words string `bson:"words" json:"words"`
	// Host is where it is (hosts and ports only); Name the database's name.
	Host string `bson:"host,omitempty" json:"host,omitempty"`
	Name string `bson:"name" json:"name"`
	// Reach: this-computer (only the computer that moved it reaches it) or
	// every-computer.
	Reach string `bson:"reach" json:"reach" jsonschema:"enum=this-computer,enum=every-computer"`
	// Computer is the computer whose own database it is (Kind here).
	Computer string `bson:"computer,omitempty" json:"computer,omitempty"`
}

// MoveMarker is the marker in the database a move leaves.
type MoveMarker struct {
	ID string `bson:"_id"`
	// Move is the move's identity: the new database's lock names it, so a
	// switch on another computer checks it reached this move's copy.
	Move      string     `bson:"move"`
	Workspace string     `bson:"workspace"`
	State     string     `bson:"state"`
	To        MoveTarget `bson:"to"`
	// By is the person who started it, From the computer it runs on.
	By        string    `bson:"by"`
	ByUserID  string    `bson:"byUserId,omitempty"`
	From      string    `bson:"from"`
	FromName  string    `bson:"fromName"`
	StartedAt time.Time `bson:"startedAt"`
	UpdatedAt time.Time `bson:"updatedAt"`
	// MovedAt is when the copy was whole and checked and the computer that
	// moved it switched; AbandonedAt/By when a person abandoned it.
	MovedAt     *time.Time `bson:"movedAt,omitempty"`
	AbandonedAt *time.Time `bson:"abandonedAt,omitempty"`
	AbandonedBy string     `bson:"abandonedBy,omitempty"`
	// Holding: computers whose MetaRush paused its changes for the move
	// (installation → when it last said so). Switched: computers that use the
	// new database since.
	Holding  map[string]time.Time `bson:"holding,omitempty"`
	Switched map[string]time.Time `bson:"switched,omitempty"`
	// Busy is work let through the freeze — changes already running when
	// the move began — per process (guard.go): the move waits for it.
	Busy map[string]MoveBusy `bson:"busy,omitempty"`
	// Copying: the move stopped waiting and copies (MoveCopying). From then
	// on no running change writes, listed or not.
	Copying bool `bson:"copying,omitempty"`
}

// MoveBusy is one process's work running through the freeze.
type MoveBusy struct {
	Installation string    `bson:"installation"`
	Ops          []string  `bson:"ops"`
	At           time.Time `bson:"at"`
}

// Frozen reports whether the marker keeps changes out of the database: a
// move is copying it, or it moved.
func (m *MoveMarker) Frozen() bool {
	return m != nil && (m.State == MoveMoving || m.State == MoveMoved)
}

// ReadMoveMarker reads a database's move marker (nil when there is none).
func (d *DB) ReadMoveMarker(ctx context.Context) (*MoveMarker, error) {
	var m MoveMarker
	err := d.C(CDatabaseMove).FindOne(ctx, bson.M{"_id": moveMarkerID}).Decode(&m)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ErrMoveUnderway refuses starting a move while another one holds the database.
var ErrMoveUnderway = errors.New("another move of this database is underway or finished")

// StartMoveMarker writes the marker of a move that starts: only when the
// database has none, or one a person abandoned (an older move's). A marker of
// this very move (a move run again to continue it) is left as it is.
func (d *DB) StartMoveMarker(ctx context.Context, m MoveMarker) error {
	now := Clock().UTC() // replaced by the server's time below
	m.ID, m.State, m.UpdatedAt = moveMarkerID, MoveMoving, now
	if m.StartedAt.IsZero() {
		m.StartedAt = now
	}
	cur, err := d.ReadMoveMarker(ctx)
	if err != nil {
		return err
	}
	if cur != nil && cur.State == MoveMoving && cur.Move == m.Move {
		return nil
	}
	if cur.Frozen() {
		return ErrMoveUnderway
	}
	// Replaced only while it is still the abandoned one read above (or none
	// is there): two moves started at once can't both win.
	filter := bson.M{"_id": moveMarkerID}
	if cur != nil {
		filter["state"] = MoveAbandoned
		filter["move"] = cur.Move
	}
	res, err := d.C(CDatabaseMove).ReplaceOne(ctx, filter, m, options.Replace().SetUpsert(cur == nil))
	if IsDuplicateKey(err) {
		return ErrMoveUnderway
	}
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 && res.UpsertedCount == 0 {
		return ErrMoveUnderway
	}
	// The database server's time, never this computer's: every time in the
	// marker is compared with others written on other computers.
	_, err = d.C(CDatabaseMove).UpdateOne(ctx, bson.M{"_id": moveMarkerID, "move": m.Move},
		bson.M{"$currentDate": bson.M{"startedAt": true, "updatedAt": true}})
	return err
}

// SetMoveState moves a move's marker from one state to another, only while
// it is that move's and in that state. It reports whether it did.
func (d *DB) SetMoveState(ctx context.Context, move, from, to, by string) (bool, error) {
	set := bson.M{"state": to}
	when := bson.M{"updatedAt": true}
	switch to {
	case MoveMoved:
		when["movedAt"] = true
	case MoveAbandoned:
		when["abandonedAt"], set["abandonedBy"] = true, by
	}
	res, err := d.C(CDatabaseMove).UpdateOne(ctx, bson.M{"_id": moveMarkerID, "move": move, "state": from}, bson.M{"$set": set, "$currentDate": when})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// MoveBeat says the move still runs (the marker's updatedAt): another
// computer doesn't abandon a move that is running.
// MoveCopying says the move stopped waiting and copies: set only while no
// process listed running work within fresh (server time), in one step, so a
// running change either lists itself first (and the move waits for it) or
// finds the copy started and writes nothing (guard.go). False: some work is
// listed.
func (d *DB) MoveCopying(ctx context.Context, move string, fresh time.Duration) (bool, error) {
	recent := bson.M{"$filter": bson.M{
		"input": bson.M{"$objectToArray": bson.M{"$ifNull": bson.A{"$busy", bson.M{}}}},
		"cond":  bson.M{"$gt": bson.A{"$$this.v.at", bson.M{"$subtract": bson.A{"$$NOW", fresh.Milliseconds()}}}},
	}}
	res, err := d.C(CDatabaseMove).UpdateOne(ctx, bson.M{"_id": moveMarkerID, "move": move, "state": MoveMoving,
		"$expr": bson.M{"$eq": bson.A{bson.M{"$size": recent}, 0}}},
		bson.M{"$set": bson.M{"copying": true}, "$currentDate": bson.M{"updatedAt": true}})
	if err != nil {
		return false, err
	}
	if res.MatchedCount == 1 {
		if m, err := d.ReadMoveMarker(ctx); err == nil {
			d.SetMoveMarkerCache(m)
		}
	}
	return res.MatchedCount == 1, nil
}

func (d *DB) MoveBeat(ctx context.Context, move string) error {
	_, err := d.C(CDatabaseMove).UpdateOne(ctx, bson.M{"_id": moveMarkerID, "move": move, "state": MoveMoving},
		bson.M{"$currentDate": bson.M{"updatedAt": true}})
	return err
}

// MoveAlive reports whether a move's marker said it runs within the last
// while, by the database server's own clock.
func (d *DB) MoveAlive(ctx context.Context, move string, within time.Duration) (bool, error) {
	n, err := d.C(CDatabaseMove).CountDocuments(ctx, bson.M{"_id": moveMarkerID, "move": move, "state": MoveMoving,
		"$expr": bson.M{"$gt": bson.A{"$updatedAt", bson.M{"$subtract": bson.A{"$$NOW", within.Milliseconds()}}}}})
	return n > 0, err
}

// SeenWithin is a filter for records whose field is within the last while
// by the database server's own clock (computers' clocks differ).
func SeenWithin(field string, within time.Duration) bson.M {
	return bson.M{"$expr": bson.M{"$gte": bson.A{"$" + field, bson.M{"$subtract": bson.A{"$$NOW", within.Milliseconds()}}}}}
}

// LiveLease is a filter for jobs whose lease hasn't ended by the database
// server's clock.
func LiveLease() bson.M {
	return bson.M{"$expr": bson.M{"$gt": bson.A{"$lease.expiresAt", "$$NOW"}}}
}

// MoveHolding records that a computer's MetaRush paused its changes for the
// move (written only into the marker, which no move copies).
func (d *DB) MoveHolding(ctx context.Context, move, installation string) error {
	_, err := d.C(CDatabaseMove).UpdateOne(ctx, bson.M{"_id": moveMarkerID, "move": move},
		bson.M{"$currentDate": bson.M{"holding." + installation: true}})
	return err
}

// MoveSwitched records that a computer switched to the new database.
func (d *DB) MoveSwitched(ctx context.Context, move, installation string) error {
	_, err := d.C(CDatabaseMove).UpdateOne(ctx, bson.M{"_id": moveMarkerID, "move": move},
		bson.M{"$currentDate": bson.M{"switched." + installation: true}})
	return err
}

// ErrDatabaseMoving is a database a move keeps changes out of.
type ErrDatabaseMoving struct{ Marker MoveMarker }

func (e *ErrDatabaseMoving) Error() string {
	if e.Marker.State == MoveMoved {
		return fmt.Sprintf("the workspace moved to another database, %s, so MetaRush doesn't change this one: switch this computer too", e.Marker.To.Words)
	}
	return "the workspace is being moved to another database, so MetaRush doesn't change this one until that is done"
}

// AppError says the refusal in plain words (apperr.Coded): every write a
// frozen database refuses, wherever it came from, reads the same.
func (e *ErrDatabaseMoving) AppError() *apperr.Error {
	m := e.Marker
	if m.State == MoveMoved {
		return apperr.New(apperr.Moving, "The workspace moved to another database (%s): switch this computer too.", m.To.Words).
			WithHint("Switch it at this computer with its own sign-in for the new database (Settings → Storage shows how). Changes made in the old database would be lost, so MetaRush refuses them there.").
			WithDetails(map[string]any{"reason": "database_moved", "to": m.To.Words, "name": m.To.Name}).
			WithCommand("metarush database switch --to address --name " + m.To.Name)
	}
	by, from := m.By, m.FromName
	if by == "" {
		by = "A person"
	}
	if from == "" {
		from = "another computer"
	}
	return apperr.New(apperr.Moving, "The workspace is being moved to another database; changes wait until it's done.").
		WithHint(fmt.Sprintf("%s started moving it on %s. Reading and playing keep working.", by, from)).
		WithDetails(map[string]any{"reason": "database_moving", "to": m.To.Words}).WithCommand("metarush database move-status")
}

// CheckNotMoving refuses a database a move keeps changes out of.
func (d *DB) CheckNotMoving(ctx context.Context) error {
	m, err := d.ReadMoveMarker(ctx)
	if err != nil {
		return err
	}
	if m.Frozen() {
		return &ErrDatabaseMoving{Marker: *m}
	}
	return nil
}
