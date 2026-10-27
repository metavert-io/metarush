package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
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
	now := time.Now().UTC()
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
	return nil
}

// SetMoveState moves a move's marker from one state to another, only while
// it is that move's and in that state. It reports whether it did.
func (d *DB) SetMoveState(ctx context.Context, move, from, to, by string) (bool, error) {
	now := time.Now().UTC()
	set := bson.M{"state": to, "updatedAt": now}
	switch to {
	case MoveMoved:
		set["movedAt"] = now
	case MoveAbandoned:
		set["abandonedAt"], set["abandonedBy"] = now, by
	}
	res, err := d.C(CDatabaseMove).UpdateOne(ctx, bson.M{"_id": moveMarkerID, "move": move, "state": from}, bson.M{"$set": set})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount == 1, nil
}

// MoveHolding records that a computer's MetaRush paused its changes for the
// move (written only into the marker, which no move copies).
func (d *DB) MoveHolding(ctx context.Context, move, installation string) error {
	_, err := d.C(CDatabaseMove).UpdateOne(ctx, bson.M{"_id": moveMarkerID, "move": move},
		bson.M{"$set": bson.M{"holding." + installation: time.Now().UTC()}})
	return err
}

// MoveSwitched records that a computer switched to the new database.
func (d *DB) MoveSwitched(ctx context.Context, move, installation string) error {
	_, err := d.C(CDatabaseMove).UpdateOne(ctx, bson.M{"_id": moveMarkerID, "move": move},
		bson.M{"$set": bson.M{"switched." + installation: time.Now().UTC()}})
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
