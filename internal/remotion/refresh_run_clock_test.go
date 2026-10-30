package remotion

import (
	"context"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// One refresh of a source at a time, by the database server's clock (D360):
// a refresh another computer started 1 h 55 min ago still runs for a computer
// ten minutes fast, and one started 2 h 5 min ago is over for a computer ten
// minutes slow.
func TestOneRefreshAtATimeByTheServersClock(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	s := newSource(t, a, "clockfilm")
	server, err := a.DB.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	started := server.Add(-refreshTotalLimit + 5*time.Minute)
	other := models.RemotionRefresh{ID: ids.New(ids.RemotionRefresh), WorkspaceID: a.WorkspaceID(), SourceID: s.ID, ProjectID: s.ProjectID, Number: 1,
		State: models.RemotionRunRunning, Machine: "Studio iMac", Installation: "ins_other", StartedAt: &started, Ran: true, CreatedAt: started, UpdatedAt: started}
	if err := a.DB.Insert(ctx, store.CRemotionRefreshes, other); err != nil {
		t.Fatal(err)
	}
	mine := ids.New(ids.RemotionRefresh)

	restore := testutil.SkewClock(t, 10*time.Minute)
	if got := runningRefresh(ctx, a, s.ID, mine); got == nil || got.ID != other.ID {
		t.Fatalf("a computer ten minutes fast took a refresh started 1 h 55 min ago as over: %+v", got)
	}
	restore()
	if err := setRefresh(ctx, a, other.ID, map[string]any{"startedAt": server.Add(-refreshTotalLimit - 5*time.Minute)}); err != nil {
		t.Fatal(err)
	}
	restore = testutil.SkewClock(t, -10*time.Minute)
	if got := runningRefresh(ctx, a, s.ID, mine); got != nil {
		t.Fatalf("a computer ten minutes slow took a refresh started 2 h 5 min ago as still running: %+v", got)
	}
	restore()
}

// A refresh's start is the server's time, so another computer's look at it
// agrees with this one's.
func TestARefreshStartsAtTheServersTime(t *testing.T) {
	a := testapp.New(t)
	needsSandbox(t, a)
	ctx := context.Background()
	s := newSource(t, a, "startfilm")
	versionOf(t, a, s, refreshProject("console.log(1)\n"), "", models.RemotionKindImport)
	restore := testutil.SkewClock(t, 10*time.Minute)
	defer restore()
	r := startTestRefresh(t, a, s, RemotionRefreshIn{Steps: []string{"npm run make-data"}, Outputs: []string{"public/"}})
	var checked []string
	if _, err := testRefresher(a, &checked).run(ctx, r.ID, r.JobID, nil); err != nil {
		t.Fatal(err)
	}
	got, err := getRefresh(ctx, a, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	server, err := a.DB.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got.StartedAt == nil || got.StartedAt.Sub(server) > time.Minute || server.Sub(*got.StartedAt) > 5*time.Minute {
		t.Fatalf("started at %v by a computer ten minutes fast; the server's time is %v", got.StartedAt, server)
	}
}
