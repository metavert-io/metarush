package handover_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/handover"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/store"
)

// The role handed back before the old computer had stepped down: the first
// handover ends then (not "received" and in progress for good), the old
// computer is the host again, and it can hand its role over once more.
func TestRoleHandedBackBeforeSteppingDown(t *testing.T) {
	w := newWorkspace(t)
	a, b := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	pctx := handover.WithPassphrase(ctx, pass)
	id, _ := talk(t, a.at(t, remembers()), "", "Hello.")
	// Something of the laptop's own still to finish there: it can't step down yet.
	if _, _, err := a.a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeStorageMove, Actor: a.a.Actor, Installation: a.a.Cfg.Installation.ID,
		IdempotencyKey: ids.New("tst"), MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	first := call[handover.HandoverExportOut](t, pctx, a.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: filepath.Join(t.TempDir(), "1.age")})
	b.at(t, remembers())
	call[handover.HandoverReceiveOut](t, pctx, b.a, "machine.handover.receive", handover.HandoverReceiveIn{File: first.File})
	if role, waiting, err := handover.Look(ctx, a.a); err != nil || role.Role != handover.RoleSteppingDown || len(waiting) != 1 {
		t.Fatalf("the laptop waits to step down: %+v %d %v", role, len(waiting), err)
	}

	// The desk hands it straight back.
	back := filepath.Join(t.TempDir(), "2.age")
	second := call[handover.HandoverExportOut](t, pctx, b.a, "machine.handover.export", handover.HandoverExportIn{To: "laptop", Out: back})
	t.Setenv("CLAUDE_CONFIG_DIR", a.claude)
	call[handover.HandoverReceiveOut](t, pctx, a.a, "machine.handover.receive", handover.HandoverReceiveIn{File: back})

	var r1 handover.HandoverRecord
	if err := w.db.GetByID(ctx, store.CHandovers, w.id, first.Handover.ID, &r1); err != nil {
		t.Fatal(err)
	}
	if r1.State != handover.StateDone || r1.Active || r1.OvertakenBy != second.Handover.ID || r1.SteppedDownAt == nil {
		t.Fatalf("the first handover %+v", r1)
	}
	if role, _, err := handover.Look(ctx, a.a); err != nil || role.Role != handover.RoleHost {
		t.Fatalf("the laptop is the host again: %+v %v", role, err)
	}
	st := call[handover.HandoverStatusOut](t, ctx, a.a, "machine.handover.status", handover.HandoverStatusIn{})
	for _, r := range st.InProgress {
		if r.ID == first.Handover.ID {
			t.Fatal("the first handover is still in progress")
		}
	}
	if st.Clean != nil {
		t.Fatalf("clean is offered for a handover whose conversations came back: %+v", st.Clean)
	}
	// And it can hand over again.
	t.Setenv("CLAUDE_CONFIG_DIR", a.claude)
	if d := conv(t, a.a, id); d.InstallationID != a.a.Cfg.Installation.ID {
		t.Fatal("the conversation came back")
	}
	again := call[handover.HandoverExportOut](t, pctx, a.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: filepath.Join(t.TempDir(), "3.age")})
	if again.Handover.ID == first.Handover.ID || again.Conversations != 1 {
		t.Fatalf("a new handover %+v", again)
	}
}
