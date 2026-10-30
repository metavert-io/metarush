package ops

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

// Putting new words in a cut on a computer whose clock is ten minutes slow
// stamps the change with the database server's time: the daemons' sweep
// judges a lip sync still being made by it, and must not take it for one
// left behind (D360).
func TestPlacingNewWordsStampsTheServersTime(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	server, err := a.DB.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := models.SpokenChange{ID: ids.New("swc"), WorkspaceID: a.WorkspaceID(), State: models.SpokenReady,
		Mouth: &models.SpokenMouth{State: models.SpokenMaking, GenerationID: "gen_lips"}, UpdatedAt: server.Add(-time.Hour)}
	if err := a.DB.Insert(ctx, store.CSpokenChanges, c); err != nil {
		t.Fatal(err)
	}
	testutil.SkewClock(t, -10*time.Minute)
	recordPlacing(ctx, a, &c, models.SpokenVersion{RevisionID: "rev_1", Branch: "main", At: store.Clock().UTC()}, false)
	var got models.SpokenChange
	if err := a.DB.GetByID(ctx, store.CSpokenChanges, a.WorkspaceID(), c.ID, &got); err != nil {
		t.Fatal(err)
	}
	if d := server.Sub(got.UpdatedAt); d > time.Second || d < -time.Minute {
		t.Fatalf("placed on a computer ten minutes slow: updatedAt %v, the server's now %v", got.UpdatedAt, server)
	}
	n, err := a.DB.C(store.CSpokenChanges).CountDocuments(ctx, store.StaleBefore("updatedAt", 2*time.Minute))
	if err != nil || n != 0 {
		t.Fatalf("the sweep would take it for left behind: %d %v", n, err)
	}
}
