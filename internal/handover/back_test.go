package handover_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/handover"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
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

// A post being sent to YouTube from the old computer is work still finishing
// there: it steps down only once the post is sent (or has failed), never in
// the middle of it.
func TestStepsDownOnlyAfterAPostIsSent(t *testing.T) {
	w := newWorkspace(t)
	a, b := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	pctx := handover.WithPassphrase(ctx, pass)
	talk(t, a.at(t, remembers()), "", "Hello.")
	now := time.Now().UTC()
	rev := ids.New("rev")
	if err := w.db.Insert(ctx, store.CRevisions, models.Revision{ID: rev, WorkspaceID: w.id, ProjectID: "prj_teaser", Branch: "main", Number: 3, CreatedAt: now,
		Publications: []models.Publication{
			{ID: "pub_sending", Platform: "youtube", State: models.PublicationSending, Installation: a.a.Cfg.Installation.ID, At: now, Beat: &now},
			{ID: "pub_elsewhere", Platform: "youtube", State: models.PublicationSending, Installation: b.a.Cfg.Installation.ID, At: now, Beat: &now},
			{ID: "pub_posted", Platform: "youtube", State: models.PublicationPosted, Installation: a.a.Cfg.Installation.ID, At: now},
		}}); err != nil {
		t.Fatal(err)
	}
	plan := call[handover.HandoverPlan](t, ctx, a.a, "machine.handover.plan", handover.HandoverPlanIn{To: "desk"})
	var posts []handover.HandoverWork
	for _, wk := range plan.Work {
		if wk.Kind == "post" {
			posts = append(posts, wk)
		}
	}
	if len(posts) != 1 || posts[0].ID != "pub_sending" || posts[0].Moves || !strings.Contains(posts[0].Words, "version 3 being posted to YouTube") {
		t.Fatalf("the plan's posts %+v", posts)
	}
	ex := call[handover.HandoverExportOut](t, pctx, a.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: filepath.Join(t.TempDir(), "h.age")})
	b.at(t, remembers())
	r := call[handover.HandoverReceiveOut](t, pctx, b.a, "machine.handover.receive", handover.HandoverReceiveIn{File: ex.File})
	if len(r.Waiting) != 1 || r.Waiting[0].Kind != "post" {
		t.Fatalf("waiting %+v", r.Waiting)
	}
	if role, waiting, err := handover.Look(ctx, a.a); err != nil || role.Role != handover.RoleSteppingDown || len(waiting) != 1 {
		t.Fatalf("the laptop steps down while the post is sending: %+v %+v %v", role, waiting, err)
	}
	if _, err := w.db.C(store.CRevisions).UpdateOne(ctx, bson.M{"_id": rev, "publications.id": "pub_sending"},
		bson.M{"$set": bson.M{"publications.$.state": models.PublicationPosted}}); err != nil {
		t.Fatal(err)
	}
	if role, _, err := handover.Look(ctx, a.a); err != nil || role.Role != handover.RoleClient {
		t.Fatalf("the laptop steps down once it is sent: %+v %v", role, err)
	}
}
