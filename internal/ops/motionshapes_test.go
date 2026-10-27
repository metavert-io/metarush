package ops

import (
	"context"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Test frames are drawn in the shapes of the cuts a graphic is for (D164):
// the cuts that use it, else the project's cuts, else the brief's deliverables.
func TestMotionCheckShapesFollowTheCuts(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Shapes"})
	now := time.Now().UTC()
	as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "talk.mov",
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 20, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, as)
	proj := func() *models.Project {
		pp, err := resolveProject(ctx, a, p.Project.ID)
		if err != nil {
			t.Fatal(err)
		}
		return pp
	}
	labels := func() []string {
		var out []string
		for _, s := range checkShapes(ctx, a, proj(), "name-bar") {
			out = append(out, fmt.Sprintf("%s %dx%d", s.Label, s.Width, s.Height))
		}
		return out
	}
	// No cut yet: the brief's deliverables.
	if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.Project.ID}, bson.M{"$set": bson.M{"brief.deliverables": []models.Deliverable{
		{Name: "by email", Preset: "landscape-1080p"}, {Name: "social shorts", Preset: "vertical-1080p", Platform: "shorts"}}}}); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(labels()); got != "[widescreen 1920x1080 vertical 1080x1920]" {
		t.Fatalf("from the deliverables: %s", got)
	}
	// A widescreen cut: its shape.
	savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":0,"out":5}]}}`, p.Project.ID, as.ID))
	if got := fmt.Sprint(labels()); got != "[widescreen 1920x1080]" {
		t.Fatalf("from the cut: %s", got)
	}
}
