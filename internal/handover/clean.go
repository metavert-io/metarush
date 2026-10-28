package handover

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/store"
)

// Nothing is deleted on the old computer until a person says so (D348):
// `handover clean`, there, after the new computer confirmed it received the
// conversations, removes this computer's copies of their Claude Code
// sessions and folders — only of conversations that live elsewhere now.

// HandoverCleanIn removes the old computer's copies of handed-over conversations.
type HandoverCleanIn struct {
	// ID is the handover (default: the latest one from this computer that
	// was received).
	ID string `json:"id,omitempty" jsonschema:"pattern=^hov_[0-9a-z]{26}$"`
	// Yes removes them; without it, it only says what it would remove.
	Yes bool `json:"yes,omitempty"`
}

// HandoverCleanOut says what was (or would be) removed.
type HandoverCleanOut struct {
	Handover HandoverRecord `json:"handover"`
	Removed  bool           `json:"removed"`
	// Files and Bytes are this computer's copies (sessions and folders).
	Files   int      `json:"files"`
	Bytes   int64    `json:"bytes"`
	Kept    []string `json:"kept,omitempty" jsonschema:"description=Conversations still held here, whose files stay."`
	Summary string   `json:"summary"`
}

func cleanHandover(ctx context.Context, a *app.App, in *HandoverCleanIn) (*HandoverCleanOut, error) {
	if err := peopleOnly(a); err != nil {
		return nil, err
	}
	me := a.Cfg.Installation.ID
	var rec *HandoverRecord
	if in.ID != "" {
		r, err := getRecord(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		rec = r
	} else {
		var r HandoverRecord
		err := a.DB.C(store.CHandovers).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "from": me, "kind": KindHandover,
			"state": bson.M{"$in": bson.A{StateReceived, StateDone}}}, options.FindOne().SetSort(bson.D{{Key: "settledAt", Value: -1}})).Decode(&r)
		if err != nil {
			return nil, apperr.New(apperr.NotFound, "No handover from this computer has been received yet, so nothing is removed.").
				WithCommand("metarush machine handover status")
		}
		rec = &r
	}
	if rec.From != me {
		return nil, apperr.New(apperr.InvalidInput, "That handover wasn't made on this computer: clean runs on the computer that handed its role over.")
	}
	if rec.State != StateReceived && rec.State != StateDone {
		return nil, apperr.New(apperr.Conflict, "“%s” hasn't received this handover yet: nothing is removed before it has.", rec.ToName).
			WithCommand("metarush machine handover status")
	}
	out := &HandoverCleanOut{}
	var paths []string
	for _, id := range rec.Conversations {
		var d struct {
			InstallationID string `bson:"installationId"`
			Title          string `bson:"title"`
		}
		err := a.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()},
			options.FindOne().SetProjection(bson.M{"installationId": 1, "title": 1})).Decode(&d)
		if err == nil && d.InstallationID == me {
			out.Kept = append(out.Kept, d.Title) // it came back here: its files stay
			continue
		}
		if sid := rec.Sessions[id]; sid != "" {
			if place, ok := assistant.FindSession(a, sid); ok {
				paths = append(paths, place.File, place.Dir)
			}
		}
		paths = append(paths, assistant.ConversationFolder(a, id))
	}
	for _, p := range paths {
		n, b := sizeOf(p)
		out.Files += n
		out.Bytes += b
	}
	if in.Yes {
		for _, p := range paths {
			if err := os.RemoveAll(p); err != nil {
				return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't remove a copy.").WithDetails(map[string]any{"path": p})
			}
		}
		now := time.Now().UTC()
		_, _ = a.DB.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"cleanedAt": now}})
		rec.CleanedAt = &now
		a.Audit(ctx, "machine.handover.clean", "handover", rec.ID, "", map[string]any{"files": out.Files, "bytes": out.Bytes})
		out.Removed = true
		out.Summary = fmt.Sprintf("Removed this computer's copies of %s handed to “%s” (%s).", plural(len(rec.Conversations)-len(out.Kept), "conversation"), rec.ToName, plural(out.Files, "file"))
	} else {
		out.Summary = fmt.Sprintf("Cleaning up would remove this computer's copies of %s handed to “%s”: %s. They live on “%s” now; nothing else is touched.",
			plural(len(rec.Conversations)-len(out.Kept), "conversation"), rec.ToName, plural(out.Files, "file"), rec.ToName)
	}
	out.Handover = *rec
	return out, nil
}

func sizeOf(p string) (int, int64) {
	n, b := 0, int64(0)
	_ = filepath.WalkDir(p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n++
				b += info.Size()
			}
		}
		return nil
	})
	return n, b
}

/* ---------- Abandoning ---------- */

// HandoverAbandonIn abandons a handover that wasn't received.
type HandoverAbandonIn struct {
	ID string `json:"id,omitempty" jsonschema:"pattern=^hov_[0-9a-z]{26}$,description=Default: the one in progress from this computer."`
}

// HandoverAbandonOut says what went back.
type HandoverAbandonOut struct {
	Handover      HandoverRecord `json:"handover"`
	Conversations int            `json:"conversations"`
	Summary       string         `json:"summary"`
}

func abandonHandover(ctx context.Context, a *app.App, in *HandoverAbandonIn) (*HandoverAbandonOut, error) {
	if err := peopleOnly(a); err != nil {
		return nil, err
	}
	var rec *HandoverRecord
	var err error
	if in.ID != "" {
		rec, err = getRecord(ctx, a, in.ID)
	} else {
		rec, err = activeFrom(ctx, a, a.Cfg.Installation.ID)
		if err == nil && rec == nil {
			err = apperr.New(apperr.NotFound, "No handover from this computer is in progress.").WithCommand("metarush machine handover status")
		}
	}
	if err != nil {
		return nil, err
	}
	switch rec.State {
	case StateAbandoned:
		return &HandoverAbandonOut{Handover: *rec, Summary: "That handover was abandoned already."}, nil
	case StateReceiving:
		return nil, apperr.New(apperr.Conflict, "“%s” is receiving this handover now, so it can't be abandoned.", rec.ToName).
			WithHint("Let it finish there; to give the role back after, hand it over from “" + rec.ToName + "” to “" + rec.FromName + "”.").
			WithDetails(map[string]any{"reason": "receiving"})
	case StateReceived, StateDone:
		return nil, apperr.New(apperr.Conflict, "“%s” has received this handover already, so it can't be abandoned.", rec.ToName).
			WithHint("To give the role back, hand it over from “" + rec.ToName + "” to “" + rec.FromName + "”.")
	}
	now := time.Now().UTC()
	res, err := a.DB.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID(), "state": bson.M{"$in": bson.A{StateExporting, StateExported}}},
		bson.M{"$set": bson.M{"state": StateAbandoned, "abandonedAt": now, "abandonedBy": a.Actor}, "$unset": bson.M{"active": ""}})
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't abandon the handover.")
	}
	if res.MatchedCount == 0 {
		return nil, apperr.New(apperr.Conflict, "The handover changed meanwhile: “%s” may be receiving it.", rec.ToName).
			WithCommand("metarush machine handover status").WithDetails(map[string]any{"reason": "receiving"})
	}
	n, err := assistant.UnmarkHandingOver(ctx, a, rec.ID, rec.From)
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "machine.handover.abandon", "handover", rec.ID, "", map[string]any{"to": rec.ToName, "conversations": n})
	rec, err = getRecord(ctx, a, rec.ID)
	if err != nil {
		return nil, err
	}
	return &HandoverAbandonOut{Handover: *rec, Conversations: n, Summary: fmt.Sprintf("Abandoned the handover to “%s”: %s go on on “%s” as before. Delete the handover file if you made one.",
		rec.ToName, plural(n, "conversation"), rec.FromName)}, nil
}
