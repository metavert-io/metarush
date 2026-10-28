package handover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/ids"
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
	Files int      `json:"files"`
	Bytes int64    `json:"bytes"`
	Kept  []string `json:"kept,omitempty" jsonschema:"description=Conversations still held here, whose files stay."`
	// HandoverFiles are the handover files this computer made for it that are
	// still where it wrote them (removed too).
	HandoverFiles int    `json:"handoverFiles"`
	Summary       string `json:"summary"`
}

func cleanHandover(ctx context.Context, a *app.App, in *HandoverCleanIn) (*HandoverCleanOut, error) {
	if err := peopleOnly(a); err != nil {
		return nil, err
	}
	me := a.Cfg.Installation.ID
	var recs []HandoverRecord
	if in.ID != "" {
		r, err := getRecord(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		if r.From != me {
			return nil, apperr.New(apperr.InvalidInput, "That handover wasn't made on this computer: clean runs on the computer that handed its role over.")
		}
		if r.State != StateReceived && r.State != StateDone {
			return nil, apperr.New(apperr.Conflict, "“%s” hasn't received this handover yet: nothing is removed before it has.", r.ToName).
				WithCommand("metarush machine handover status")
		}
		recs = []HandoverRecord{*r}
	} else {
		// Every handover from here that was received and not cleaned up yet —
		// one a later handover overtook too (its copies are left here all the
		// same) — or else the latest, to say nothing is left.
		var all []HandoverRecord
		if err := a.DB.Find(ctx, store.CHandovers, a.WorkspaceID(), bson.M{"from": me, "kind": KindHandover, "state": bson.M{"$in": bson.A{StateReceived, StateDone}}}, &all,
			options.Find().SetSort(bson.D{{Key: "settledAt", Value: -1}})); err != nil {
			return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the handovers.")
		}
		if len(all) == 0 {
			return nil, apperr.New(apperr.NotFound, "No handover from this computer has been received yet, so nothing is removed.").
				WithCommand("metarush machine handover status")
		}
		for _, r := range all {
			if r.CleanedAt == nil {
				recs = append(recs, r)
			}
		}
		if len(recs) == 0 {
			recs = all[:1]
		}
	}
	out := &HandoverCleanOut{}
	var paths []string
	seen := map[string]bool{}
	moved, toNames := 0, []string{}
	for _, rec := range recs {
		for _, id := range rec.Conversations {
			var d struct {
				InstallationID string `bson:"installationId"`
				Title          string `bson:"title"`
			}
			err := a.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()},
				options.FindOne().SetProjection(bson.M{"installationId": 1, "title": 1})).Decode(&d)
			if err == nil && d.InstallationID == me {
				// It is held here now (it came back, through any computer):
				// its files are the ones it goes on with.
				if !seen[id] {
					out.Kept = append(out.Kept, d.Title)
				}
				seen[id] = true
				continue
			}
			if err != nil && !errors.Is(err, mongo.ErrNoDocuments) {
				return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read where a conversation is now.")
			}
			if sid := rec.Sessions[id]; sid != "" {
				if place, ok := assistant.FindSession(a, sid); ok {
					paths = append(paths, place.File, place.Dir)
				}
			}
			if !seen[id] {
				paths = append(paths, assistant.ConversationFolder(a, id))
				moved++
			}
			seen[id] = true
		}
		// The handover files this computer made for it, where they still are.
		made := madeFiles(a, rec.ID)
		paths = append(paths, made...)
		out.HandoverFiles += len(made)
		if !slices.Contains(toNames, "“"+rec.ToName+"”") {
			toNames = append(toNames, "“"+rec.ToName+"”")
		}
	}
	for _, p := range paths {
		n, b := sizeOf(p)
		out.Files += n
		out.Bytes += b
	}
	to := strings.Join(toNames, " and ")
	if in.Yes {
		for _, p := range paths {
			if err := os.RemoveAll(p); err != nil {
				return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't remove a copy.").WithDetails(map[string]any{"path": p})
			}
		}
		now := time.Now().UTC()
		for i := range recs {
			_, _ = a.DB.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": recs[i].ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"cleanedAt": now}})
			recs[i].CleanedAt = &now
			a.Audit(ctx, "machine.handover.clean", "handover", recs[i].ID, "", map[string]any{"files": out.Files, "bytes": out.Bytes})
		}
		out.Removed = true
		out.Summary = fmt.Sprintf("Removed this computer's copies of %s handed to %s (%s).", plural(moved, "conversation"), to, plural(out.Files, "file"))
		if out.HandoverFiles > 0 {
			out.Summary += fmt.Sprintf(" The handover %s made here went too.", map[bool]string{true: "file", false: "files"}[out.HandoverFiles == 1])
		}
	} else {
		out.Summary = fmt.Sprintf("Cleaning up would remove this computer's copies of %s handed to %s: %s. They live elsewhere now; nothing else is touched.",
			plural(moved, "conversation"), to, plural(out.Files, "file"))
		if out.HandoverFiles > 0 {
			out.Summary += fmt.Sprintf(" That includes the handover %s made here.", map[bool]string{true: "file", false: "files"}[out.HandoverFiles == 1])
		}
	}
	if len(out.Kept) > 0 {
		out.Summary += fmt.Sprintf(" %s held here now %s as %s.", upperFirst(plural(len(out.Kept), "conversation")), map[bool]string{true: "stays", false: "stay"}[len(out.Kept) == 1],
			map[bool]string{true: "it is", false: "they are"}[len(out.Kept) == 1])
	}
	out.Handover = recs[0]
	return out, nil
}

// cleanOffer is the newest handover from this computer whose copies are
// still here for a conversation held elsewhere now (nil: nothing to clean).
func cleanOffer(ctx context.Context, a *app.App, me string) *HandoverRecord {
	var recs []HandoverRecord
	if a.DB.Find(ctx, store.CHandovers, a.WorkspaceID(), bson.M{"from": me, "kind": KindHandover, "state": bson.M{"$in": bson.A{StateReceived, StateDone}}, "cleanedAt": nil}, &recs,
		options.Find().SetSort(bson.D{{Key: "settledAt", Value: -1}}).SetLimit(20)) != nil {
		return nil
	}
	for i := range recs {
		if len(recs[i].Conversations) == 0 {
			continue
		}
		n, err := a.DB.C(store.CAssistantConversations).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "_id": bson.M{"$in": recs[i].Conversations},
			"installationId": bson.M{"$ne": me}})
		if err == nil && n > 0 {
			return &recs[i]
		}
	}
	return nil
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
	from := bson.A{StateExporting, StateExported}
	switch rec.State {
	case StateAbandoned:
		// Abandoned already: its marks come off again, so running it again
		// repairs a conversation an export marked after the abandon.
		n, err := assistant.UnmarkHandingOver(ctx, a, rec.ID, rec.From)
		if err != nil {
			return nil, err
		}
		out := &HandoverAbandonOut{Handover: *rec, Conversations: n, Summary: "That handover was abandoned already."}
		if n > 0 {
			out.Summary += fmt.Sprintf(" %s still marked as moving %s on on “%s” again.", upperFirst(plural(n, "conversation")), goWord(n), rec.FromName)
		}
		return out, nil
	case StateReceiving:
		// Only once the computer receiving it has been quiet long enough (it
		// stopped part-way, and may be gone): what it took stays there.
		recv, err := findComputer(ctx, a, rec.To)
		if err != nil {
			return nil, err
		}
		ok, _, err := quietEnough(ctx, a, recv)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, apperr.New(apperr.Conflict, "“%s” is receiving this handover now, so it can't be abandoned.", rec.ToName).
				WithHint("Let it finish there. If it stopped part-way and is gone, abandon it once it has been quiet for " + quietWords() + ", or take it over.").
				WithDetails(map[string]any{"reason": "receiving"})
		}
		from = bson.A{StateReceiving}
	case StateReceived, StateDone:
		return nil, apperr.New(apperr.Conflict, "“%s” has received this handover already, so it can't be abandoned.", rec.ToName).
			WithHint("To give the role back, hand it over from “" + rec.ToName + "” to “" + rec.FromName + "”.")
	}
	now := time.Now().UTC()
	res, err := a.DB.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID(), "state": bson.M{"$in": from}},
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
	// What the receiving computer took before it stopped stays there.
	taken, err := a.DB.C(store.CAssistantConversations).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "installationId": rec.To, "moved.handover": rec.ID})
	if err != nil {
		taken = 0
	}
	a.Audit(ctx, "machine.handover.abandon", "handover", rec.ID, "", map[string]any{"to": rec.ToName, "conversations": n, "taken": taken})
	rec, err = getRecord(ctx, a, rec.ID)
	if err != nil {
		return nil, err
	}
	sum := fmt.Sprintf("Abandoned the handover to “%s”: %s %s on on “%s” as before. Delete the handover file if you made one.",
		rec.ToName, plural(n, "conversation"), goWord(n), rec.FromName)
	if taken > 0 {
		sum += fmt.Sprintf(" %s had already gone to “%s” and %s there: take “%s” over to bring %s back.", upperFirst(plural(int(taken), "conversation")), rec.ToName,
			map[bool]string{true: "stays", false: "stay"}[taken == 1], rec.ToName, map[bool]string{true: "it", false: "them"}[taken == 1])
	}
	return &HandoverAbandonOut{Handover: *rec, Conversations: n, Summary: sum}, nil
}

/* ---------- The handover files this computer made ---------- */

// madeFile is a handover file this computer wrote, as it wrote it.
type madeFile struct {
	Path  string    `json:"path"`
	Bytes int64     `json:"bytes"`
	At    time.Time `json:"at"`
}

func madeFilesPath(a *app.App, handover string) string {
	return filepath.Join(a.Cfg.StateDir(), "handover", "made", handover+".json")
}

// rememberFile notes, on this computer only, where a handover file was
// written, so clean can remove it once the handover was received (its place
// never goes into the database).
func rememberFile(a *app.App, handover, path string, size int64) {
	if !ids.Valid(ids.Handover, handover) {
		return
	}
	p := madeFilesPath(a, handover)
	var list []madeFile
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &list)
	}
	list = append(list, madeFile{Path: path, Bytes: size, At: time.Now().UTC()})
	b, err := json.Marshal(list)
	if err != nil || os.MkdirAll(filepath.Dir(p), 0o700) != nil {
		return
	}
	_ = os.WriteFile(p, b, 0o600)
}

// madeFiles are the handover files this computer made for a handover that
// are still there as they were written: the same size, still a handover
// file (encrypted, or the gzip'd archive) — never anything else at that path.
func madeFiles(a *app.App, handover string) []string {
	if !ids.Valid(ids.Handover, handover) {
		return nil
	}
	b, err := os.ReadFile(madeFilesPath(a, handover))
	if err != nil {
		return nil
	}
	var list []madeFile
	if json.Unmarshal(b, &list) != nil {
		return nil
	}
	var out []string
	for _, f := range list {
		st, err := os.Lstat(f.Path)
		if err != nil || !st.Mode().IsRegular() || st.Size() != f.Bytes || !looksLikeHandoverFile(f.Path) {
			continue
		}
		out = append(out, f.Path)
	}
	return out
}

func looksLikeHandoverFile(p string) bool {
	if enc, err := Encrypted(p); err == nil && enc {
		return true
	}
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 2)
	n, _ := io.ReadFull(f, head)
	return n == 2 && head[0] == 0x1f && head[1] == 0x8b
}
