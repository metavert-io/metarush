package leave_test

import (
	"context"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// RevisionTouchesChecked is the walk a check that must fail closed asks: the
// same projects as RevisionTouches, but an error when a lookup on the way
// didn't answer, where RevisionTouches quietly leaves those projects out. A
// nested cut deleted since isn't an error (there's nothing of it to send).
func TestTheCheckedWalkSaysWhenItCouldntLook(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	clipProject, nestedProject := ids.New(ids.Project), ids.New(ids.Project)
	clip := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: ws, ProjectIDs: []string{clipProject}, Kind: "video", Name: "a.mov", State: models.AssetAvailable,
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, clip); err != nil {
		t.Fatal(err)
	}
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: ws, ProjectID: ids.New(ids.Project), Branch: "main", Number: 1, CreatedAt: now,
		Spec: editspec.Spec{SchemaVersion: 1, Clips: []editspec.Clip{
			{ID: "a", AssetID: clip.ID, In: 0, Out: 2},
			// A cut deleted since: no error, nothing of it to look at.
			{ID: "n", AssetID: editspec.NestedPrefix + nestedProject + "/" + ids.New(ids.Revision), In: 0, Out: 2},
		}}}
	if err := a.DB.Insert(ctx, store.CRevisions, rev); err != nil {
		t.Fatal(err)
	}
	got, err := leave.RevisionTouchesChecked(ctx, a, rev.ID)
	if err != nil {
		t.Fatalf("a whole walk: %v", err)
	}
	want := map[string]bool{rev.ProjectID: true, clipProject: true, nestedProject: true}
	if len(got) != len(want) {
		t.Fatalf("projects: %v", got)
	}
	for _, p := range got {
		if !want[p] {
			t.Fatalf("projects: %v", got)
		}
	}
	// Lookups that don't answer: the unchecked walk says less, the checked
	// one says it couldn't look.
	gone, cancel := context.WithCancel(ctx)
	cancel()
	if ps := leave.RevisionTouches(gone, a, rev.ID); len(ps) != 0 {
		t.Fatalf("the unchecked walk without answers: %v", ps)
	}
	if ps, err := leave.RevisionTouchesChecked(gone, a, rev.ID); err == nil {
		t.Fatalf("the checked walk without answers gave %v and no error", ps)
	}
	// A version that isn't there can't be said to touch nothing.
	if _, err := leave.RevisionTouchesChecked(ctx, a, ids.New(ids.Revision)); err == nil {
		t.Fatal("a missing version touched nothing, with no error")
	}
}
