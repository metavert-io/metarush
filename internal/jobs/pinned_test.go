package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil"
)

// A job pinned to one installation (a link import) is leased only by that
// installation's worker, also inline (LeaseByID), and waits for it by name;
// unpinned jobs are leased by anyone, as before.
func TestPinnedJobsRunOnlyWhereAsked(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	q := &Queue{DB: db, WorkspaceID: "ws_test"}
	pinned, _, err := q.Enqueue(ctx, EnqueueOptions{Type: TypeImport, IdempotencyKey: "import:a", Installation: "ins_here"})
	if err != nil {
		t.Fatal(err)
	}
	free, _, err := q.Enqueue(ctx, EnqueueOptions{Type: TypeProxy, IdempotencyKey: "proxy:free"})
	if err != nil {
		t.Fatal(err)
	}
	other := Worker{ID: "ins_there", Name: "there", JobTypes: []string{TypeImport, TypeProxy}, AcceptsHeavy: true}
	got, err := q.Lease(ctx, other)
	if err != nil || got == nil || got.ID != free.ID {
		t.Fatalf("another machine leases only the unpinned job: %+v %v", got, err)
	}
	if again, _ := q.Lease(ctx, other); again != nil {
		t.Fatalf("another machine leased a pinned import: %+v", again)
	}
	if byID, _ := q.LeaseByID(ctx, other, pinned.ID); byID != nil {
		t.Fatalf("inline on another machine leased a pinned import: %+v", byID)
	}
	workers := []models.WorkerDevice{{ID: "ins_there", Name: "there", JobTypes: []string{TypeImport}, LastHeartbeat: time.Now(), Concurrency: 2},
		{ID: "ins_here", Name: "studio-mac", JobTypes: []string{TypeImport}, LastHeartbeat: time.Now().Add(-time.Hour)}}
	if w := Why(*pinned, workers, time.Now()); w == nil || w.Code != WaitNoWorker || !strings.Contains(w.Text, "studio-mac") {
		t.Fatalf("the pinned job waits for its own machine: %+v", w)
	}
	here := Worker{ID: "ins_here", Name: "studio-mac", JobTypes: []string{TypeImport}}
	got, err = q.Lease(ctx, here)
	if err != nil || got == nil || got.ID != pinned.ID || got.Lease.Owner != "ins_here" {
		t.Fatalf("its own machine leases it: %+v %v", got, err)
	}
	if Label(TypeImport) != "link import" {
		t.Fatal("label")
	}
}
