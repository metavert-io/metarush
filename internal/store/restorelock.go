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

// CRestoreLock holds the marker a restore of a workspace keeps in the
// database it writes into (D333): from before its first write until the copy
// is checked and up to date. While it isn't done, MetaRush doesn't bring the
// database up to date or set up a computer with it.
const CRestoreLock = "restore_lock"

// A restore's states.
const (
	RestoreCopying   = "copying"   // the records are going in
	RestoreFootage   = "footage"   // every record is in and checked; the archive's footage is being copied into the storage
	RestoreMigrating = "migrating" // every record is in and checked; the copy is being brought up to date
	RestoreDone      = "done"      // the copy is whole, checked and up to date
	RestoreAbandoned = "abandoned" // it stopped before writing any record of the copy
)

const restoreLockID = "restore"

// RestoreLock is a restore's marker.
type RestoreLock struct {
	ID string `bson:"_id"`
	// Archive is the archive's identity (the SHA-256 of its manifest), so the
	// same restore started again continues; Workspace and Name what it holds.
	Archive   string    `bson:"archive"`
	Workspace string    `bson:"workspace"`
	Name      string    `bson:"name"`
	State     string    `bson:"state"`
	StartedAt time.Time `bson:"startedAt"`
	UpdatedAt time.Time `bson:"updatedAt"`
}

// Unfinished reports whether the restore it marks still holds the database.
func (l *RestoreLock) Unfinished() bool {
	return l != nil && (l.State == RestoreCopying || l.State == RestoreFootage || l.State == RestoreMigrating)
}

// ReadRestoreLock reads a database's restore marker (nil when there is none).
func (d *DB) ReadRestoreLock(ctx context.Context) (*RestoreLock, error) {
	var l RestoreLock
	err := d.C(CRestoreLock).FindOne(ctx, bson.M{"_id": restoreLockID}).Decode(&l)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// SetRestoreLock writes the restore marker's state.
func (d *DB) SetRestoreLock(ctx context.Context, archive, workspace, name, state string) error {
	now := time.Now().UTC()
	_, err := d.C(CRestoreLock).UpdateOne(ctx, bson.M{"_id": restoreLockID},
		bson.M{"$set": bson.M{"archive": archive, "workspace": workspace, "name": name, "state": state, "updatedAt": now},
			"$setOnInsert": bson.M{"startedAt": now}}, options.UpdateOne().SetUpsert(true))
	return err
}

// ErrRestoreUnfinished is a database holding part of a restore that didn't finish.
type ErrRestoreUnfinished struct{ Lock RestoreLock }

func (e *ErrRestoreUnfinished) Error() string {
	if e.Lock.State == RestoreFootage {
		return fmt.Sprintf("this database holds a restore of the workspace “%s” that stopped while its footage was copied: run the same restore again to continue it", e.Lock.Name)
	}
	if e.Lock.State == RestoreMigrating {
		return fmt.Sprintf("this database holds a restore of the workspace “%s” that stopped while it was being brought up to date: run the same restore again to finish it", e.Lock.Name)
	}
	return fmt.Sprintf("this database holds part of a restore of the workspace “%s” that didn't finish, so MetaRush doesn't work in it: restore again into a new, empty database (MetaRush never deletes anything)", e.Lock.Name)
}

// CheckNoUnfinishedRestore refuses a database a restore still holds.
func (d *DB) CheckNoUnfinishedRestore(ctx context.Context) error {
	l, err := d.ReadRestoreLock(ctx)
	if err != nil {
		return err
	}
	if l.Unfinished() {
		return &ErrRestoreUnfinished{Lock: *l}
	}
	return nil
}
