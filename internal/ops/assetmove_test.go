package ops

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Moving footage is one write: it joins the new project and leaves the old one
// together, its other projects stay where they were, and moving it into a
// project it is already in only takes it out of the old one.
func TestMovingFootageIsOneChange(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	garden := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Garden story"}).Project
	launch := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Launch film"}).Project
	brand := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Brand"}).Project
	now := time.Now().UTC()
	clip := func(projects ...string) models.Asset {
		as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: projects, Kind: "video", Name: "interview.mp4",
			State: models.AssetAvailable, Media: models.MediaInfo{Duration: 12, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
			Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
		insert(t, a, store.CAssets, as)
		return as
	}
	projectsOf := func(id string) []string {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
			t.Fatal(err)
		}
		return as.ProjectIDs
	}

	// From Brand-and-Garden to Launch: Brand stays first, Garden goes, Launch joins at the end.
	c := clip(brand.ID, garden.ID)
	call[AssetMoveOut](t, a, "asset.move", AssetMoveIn{AssetID: c.ID, From: garden.ID, To: launch.ID})
	if got := projectsOf(c.ID); !slices.Equal(got, []string{brand.ID, launch.ID}) {
		t.Fatalf("after the move the clip is in %v, want Brand then Launch film", got)
	}

	// Already in Launch too: the move only takes it out of Garden, and Launch keeps its place.
	c = clip(launch.ID, garden.ID)
	call[AssetMoveOut](t, a, "asset.move", AssetMoveIn{AssetID: c.ID, From: garden.ID, To: launch.ID})
	if got := projectsOf(c.ID); !slices.Equal(got, []string{launch.ID}) {
		t.Fatalf("a clip already in Launch film is in %v, want only Launch film", got)
	}

	// From the Inbox (no project left): it simply joins.
	c = clip()
	call[AssetMoveOut](t, a, "asset.move", AssetMoveIn{AssetID: c.ID, To: brand.ID})
	if got := projectsOf(c.ID); !slices.Equal(got, []string{brand.ID}) {
		t.Fatalf("a clip from the Inbox is in %v, want Brand", got)
	}
}
