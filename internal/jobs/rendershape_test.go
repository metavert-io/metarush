package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil"
)

// An older MetaRush never takes a copy in another shape (D396): it would draw
// it with the size changed only, and a newer computer's quality check would
// judge a plan the file doesn't have. A computer that renders but is too old
// leaves it queued, and why it waits says so plainly; a newer one takes it.
func TestAnOlderMetaRushNeverTakesACopyInAnotherShape(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	q := &Queue{DB: db, WorkspaceID: "ws_test"}
	j, _, err := q.Enqueue(ctx, EnqueueOptions{Type: TypeRenderShape, IdempotencyKey: "render:rev_x:landscape-1080p:review:shape-1", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	older := Worker{ID: "ins_old", Name: "old-mac", JobTypes: []string{TypeProxy, TypeQA, TypeRender}, AcceptsHeavy: true} // 0.15.0's
	if got, err := q.Lease(ctx, older); err != nil || got != nil {
		t.Fatalf("an older MetaRush took a copy in another shape: %+v %v", got, err)
	}
	now := time.Now()
	r := Why(*j, []models.WorkerDevice{{ID: "ins_old", Name: "old-mac", JobTypes: older.JobTypes, LastHeartbeat: now}}, now)
	if r == nil || r.Code != WaitNewer || r.Machine != "old-mac" || !strings.HasPrefix(r.Text, "It waits for a computer with a newer MetaRush: old-mac can render, but not a copy in another shape.") {
		t.Fatalf("why it waits: %+v", r)
	}
	if r := Why(*j, nil, now); r == nil || r.Code != WaitNoWorker || !strings.Contains(r.Text, "render copies in another shape") {
		t.Fatalf("nothing running: %+v", r)
	}
	newer := Worker{ID: "ins_new", Name: "new-mac", JobTypes: []string{TypeQA, TypeRender, TypeRenderShape}, AcceptsHeavy: true}
	got, err := q.Lease(ctx, newer)
	if err != nil || got == nil || got.ID != j.ID {
		t.Fatalf("a newer MetaRush takes it: %+v %v", got, err)
	}
	if !IsRender(TypeRenderShape) || !IsRender(TypeRender) || IsRender(TypeQA) || Kind(TypeRenderShape) != TypeRender || Label(TypeRenderShape) != "render" {
		t.Fatal("both kinds read as renders")
	}
}
