package handover_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/handover"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// stalledReceive is a handover the receiving computer claimed, took one
// conversation of and then stopped (it went away): what a receive cut off
// part-way leaves.
type stalledReceive struct {
	w           *workspace
	old, nu     *computer
	mOld        *assistant.Manager
	taken, left string
	rec         string
}

func stallReceive(t *testing.T) *stalledReceive {
	t.Helper()
	w := newWorkspace(t)
	s := &stalledReceive{w: w, old: w.computer(t, "laptop"), nu: w.computer(t, "desk")}
	ctx := context.Background()
	s.mOld = s.old.at(t, remembers())
	s.taken, _ = talk(t, s.mOld, "", "First conversation.")
	s.left, _ = talk(t, s.mOld, "", "Second conversation.")
	ex := call[handover.HandoverExportOut](t, handover.WithPassphrase(ctx, pass), s.old.a, "machine.handover.export",
		handover.HandoverExportIn{To: "desk", Out: filepath.Join(t.TempDir(), "h.age")})
	s.rec = ex.Handover.ID
	if _, err := w.db.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": s.rec}, bson.M{"$set": bson.M{"state": handover.StateReceiving}}); err != nil {
		t.Fatal(err)
	}
	if _, err := assistant.AdoptConversation(ctx, s.nu.a, assistant.Adopt{ID: s.taken, Handover: s.rec, From: s.old.a.Cfg.Installation.ID, FromName: "laptop", Marked: true}); err != nil {
		t.Fatal(err)
	}
	return s
}

// quiet makes the desk unseen for two hours, by the server's clock.
func (s *stalledReceive) quiet(t *testing.T) {
	t.Helper()
	ago := bson.D{{Key: "$subtract", Value: bson.A{"$$NOW", int64(2 * time.Hour / time.Millisecond)}}}
	if _, err := s.w.db.C(store.CInstallations).UpdateOne(context.Background(), bson.M{"_id": s.nu.a.Cfg.Installation.ID},
		mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "lastSeenAt", Value: ago}, {Key: "lastUsedAt", Value: ago}}}}}); err != nil {
		t.Fatal(err)
	}
}

func (s *stalledReceive) record(t *testing.T) handover.HandoverRecord {
	t.Helper()
	var r handover.HandoverRecord
	if err := s.w.db.GetByID(context.Background(), store.CHandovers, s.w.id, s.rec, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// While the desk may still be receiving, nothing on the laptop cuts across it.
func (s *stalledReceive) refusedWhileRecent(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	_, err := ops.CallTyped[handover.HandoverAbandonOut](ctx, s.old.a, "machine.handover.abandon", handover.HandoverAbandonIn{}, person())
	refused(t, err, apperr.Conflict, "receiving")
	_, err = ops.CallTyped[handover.HandoverExportOut](handover.WithPassphrase(ctx, pass), s.old.a, "machine.handover.export",
		handover.HandoverExportIn{To: "desk", Out: filepath.Join(t.TempDir(), "again.age")}, person())
	refused(t, err, apperr.Conflict, "")
}

// The receiving computer went away part-way: taking it over ends the
// handover; the conversation it never took goes on where it was, the one it
// took comes with the rest of its work, and the laptop can hand over again.
func TestAStalledReceiveEndsWithATakeOver(t *testing.T) {
	s := stallReceive(t)
	ctx := context.Background()
	s.refusedWhileRecent(t)
	s.quiet(t)
	done := call[handover.HandoverTakeoverOut](t, ctx, s.old.a, "machine.takeover", handover.HandoverTakeoverIn{From: "desk", ConfirmedOff: "desk",
		Folders: []handover.HandoverFolderChoice{}})
	if done.Moved.Conversations != 1 || !strings.Contains(done.Summary, "never finished receiving") {
		t.Fatalf("take-over %+v %q", done.Moved, done.Summary)
	}
	if r := s.record(t); r.State != handover.StateAbandoned || r.Active {
		t.Fatalf("the stalled handover %s active=%v", r.State, r.Active)
	}
	for _, id := range []string{s.taken, s.left} {
		if d := conv(t, s.old.a, id); d.InstallationID != s.old.a.Cfg.Installation.ID || d.HandingOver != nil {
			t.Fatalf("%s isn't the laptop's, unmarked: %s %+v", id, d.InstallationName, d.HandingOver)
		}
	}
	if _, reply := talk(t, s.mOld, s.left, "Still mine?"); reply != "I remember 1 messages." {
		t.Fatalf("the one never taken goes on in its own session: %q", reply)
	}
	talk(t, s.mOld, s.taken, "And this one?")
	call[handover.HandoverExportOut](t, handover.WithPassphrase(ctx, pass), s.old.a, "machine.handover.export",
		handover.HandoverExportIn{To: "desk", Out: filepath.Join(t.TempDir(), "next.age")})
}

// Or, once the receiving computer has been quiet long enough, a person
// abandons it: what it took stays there, the rest goes on here.
func TestAStalledReceiveIsAbandonedOnceQuiet(t *testing.T) {
	s := stallReceive(t)
	ctx := context.Background()
	s.refusedWhileRecent(t)
	s.quiet(t)
	ab := call[handover.HandoverAbandonOut](t, ctx, s.old.a, "machine.handover.abandon", handover.HandoverAbandonIn{})
	if ab.Conversations != 1 || !strings.Contains(ab.Summary, "had already gone to “desk”") || ab.Handover.State != handover.StateAbandoned {
		t.Fatalf("abandon %+v", ab)
	}
	if d := conv(t, s.old.a, s.taken); d.InstallationID != s.nu.a.Cfg.Installation.ID {
		t.Fatal("what the desk took stays there")
	}
	if _, reply := talk(t, s.mOld, s.left, "Still mine?"); reply != "I remember 1 messages." {
		t.Fatalf("the rest goes on here: %q", reply)
	}
	call[handover.HandoverExportOut](t, handover.WithPassphrase(ctx, pass), s.old.a, "machine.handover.export",
		handover.HandoverExportIn{To: "desk", Out: filepath.Join(t.TempDir(), "next.age")})
}

// A mark an export put on after the handover was abandoned never strands a
// conversation: abandoning again takes it off, and so does MetaRush's look in
// the background.
func TestAMarkAfterAnAbandonIsTakenOff(t *testing.T) {
	w := newWorkspace(t)
	old, _ := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	m := old.at(t, remembers())
	id, _ := talk(t, m, "", "A conversation.")
	pctx := handover.WithPassphrase(ctx, pass)
	out := filepath.Join(t.TempDir(), "h.age")
	ex := call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	call[handover.HandoverAbandonOut](t, ctx, old.a, "machine.handover.abandon", handover.HandoverAbandonIn{})
	mark := assistant.HandingOverMark{Handover: ex.Handover.ID, To: ex.Handover.To, ToName: "desk"}
	// An export that read the record before the abandon marks it after.
	if mk, _, err := assistant.MarkHandingOver(ctx, old.a, []string{id}, mark); err != nil || len(mk) != 1 {
		t.Fatalf("mark %v %v", mk, err)
	}
	ab := call[handover.HandoverAbandonOut](t, ctx, old.a, "machine.handover.abandon", handover.HandoverAbandonIn{ID: ex.Handover.ID})
	if ab.Conversations != 1 || !strings.Contains(ab.Summary, "abandoned already") {
		t.Fatalf("abandon again %+v", ab)
	}
	if _, reply := talk(t, m, id, "Still mine?"); reply != "I remember 1 messages." {
		t.Fatalf("it goes on here: %q", reply)
	}
	// Left marked once more, the background look repairs it by itself.
	if mk, _, err := assistant.MarkHandingOver(ctx, old.a, []string{id}, mark); err != nil || len(mk) != 1 {
		t.Fatalf("mark %v %v", mk, err)
	}
	if role, _, err := handover.Look(ctx, old.a); err != nil || role.Role != handover.RoleHost {
		t.Fatalf("look %+v %v", role, err)
	}
	if d := conv(t, old.a, id); d.HandingOver != nil {
		t.Fatal("the look left a mark of an abandoned handover")
	}
	call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out + "2"})
}

// An abandon that lands while an export marks its conversations: the export
// looks at its handover again after marking, takes the marks off and says so.
func TestAnExportAbandonedWhileMarkingPutsItsMarksBack(t *testing.T) {
	w := newWorkspace(t)
	old, _ := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	m := old.at(t, remembers())
	id, _ := talk(t, m, "", "A conversation.")
	pctx := handover.WithPassphrase(ctx, pass)
	out := filepath.Join(t.TempDir(), "h.age")
	defer handover.AfterMarking(func(hov string) {
		// The abandon, and then the export's mark landing after it.
		call[handover.HandoverAbandonOut](t, ctx, old.a, "machine.handover.abandon", handover.HandoverAbandonIn{ID: hov})
		if mk, _, err := assistant.MarkHandingOver(ctx, old.a, []string{id}, assistant.HandingOverMark{Handover: hov, To: "x", ToName: "desk"}); err != nil || len(mk) != 1 {
			t.Errorf("mark %v %v", mk, err)
		}
	})()
	_, err := ops.CallTyped[handover.HandoverExportOut](pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out}, person())
	refused(t, err, apperr.Conflict, "abandoned")
	if !strings.Contains(err.Error(), "Nothing moved") {
		t.Fatalf("says so: %v", err)
	}
	if _, serr := os.Stat(out); !errors.Is(serr, os.ErrNotExist) {
		t.Fatal("a file was made")
	}
	if d := conv(t, old.a, id); d.HandingOver != nil {
		t.Fatal("the conversation was left marked")
	}
	if _, reply := talk(t, m, id, "Still mine?"); reply != "I remember 1 messages." {
		t.Fatalf("it goes on here: %q", reply)
	}
}

// Making the file again for a handover whose file exists, and failing part
// way: the conversations the first file carries stay on their way; one
// marked only by this run goes on here.
func TestAFailedSecondFileKeepsOnlyTheFirstFilesConversations(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads anything")
	}
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	m := old.at(t, remembers())
	first, _ := talk(t, m, "", "In the first file.")
	pctx := handover.WithPassphrase(ctx, pass)
	out := filepath.Join(t.TempDir(), "h.age")
	call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	later, _ := talk(t, m, "", "Started after the file was made.")
	bad := filepath.Join(assistant.ConversationFolder(old.a, later), "unreadable.txt")
	if err := os.WriteFile(bad, []byte("x"), 0o000); err != nil {
		t.Fatal(err)
	}
	_, err := ops.CallTyped[handover.HandoverExportOut](pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out + "2"}, person())
	if err == nil || !strings.Contains(err.Error(), "goes on here") {
		t.Fatalf("the second file: %v", err)
	}
	if d := conv(t, old.a, first); d.HandingOver == nil {
		t.Fatal("the first file's conversation was put back")
	}
	if d := conv(t, old.a, later); d.HandingOver != nil {
		t.Fatal("the conversation only the failed run marked stays marked")
	}
	nu.at(t, remembers())
	r := call[handover.HandoverReceiveOut](t, pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out})
	if r.Moved.Conversations != 1 || conv(t, old.a, later).InstallationID != old.a.Cfg.Installation.ID {
		t.Fatalf("receive %+v", r.Moved)
	}
}

