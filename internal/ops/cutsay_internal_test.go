package ops

import (
	"context"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A change whose consent was withdrawn while it was being put in stays
// withdrawn: the version that put it in is noted, but its state isn't
// turned back to placed (so nothing can put it in again).
func TestPlacingKeepsAWithdrawnChangeWithdrawn(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	c := models.SpokenChange{ID: ids.New(ids.SpokenChange), WorkspaceID: a.WorkspaceID(), ProjectID: ids.New(ids.Project), Branch: "main",
		State: models.SpokenWithdrawn, Why: "Ada Lovelace's consent for new words in their voice was withdrawn.", CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CSpokenChanges, c); err != nil {
		t.Fatal(err)
	}
	recordPlacing(ctx, a, &c, models.SpokenVersion{RevisionID: ids.New(ids.Revision), Branch: "main", At: now}, false)
	var got models.SpokenChange
	if err := a.DB.GetByID(ctx, store.CSpokenChanges, a.WorkspaceID(), c.ID, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != models.SpokenWithdrawn || len(got.Placed) != 1 {
		t.Fatalf("a withdrawn change was marked placed: %s (%d versions noted)", got.State, len(got.Placed))
	}
}
