package catchup

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

func jobsOf(t *testing.T, ctx context.Context, a interface {
	WorkspaceID() string
}, db *store.DB, typ string) []models.Job {
	t.Helper()
	var js []models.Job
	if err := db.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"type": typ}, &js); err != nil {
		t.Fatal(err)
	}
	return js
}

// Evidence from before visual change detection is analyzed again once, for
// clips with a picture only; up-to-date evidence is left alone.
func TestCatchUpFindsVisualChangesInOlderClips(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Old clips"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	old := clip(t, a, p.Project.ID, "old.mov", nil, 2)
	fresh := clip(t, a, p.Project.ID, "fresh.mov", nil, 2)
	now := time.Now().UTC()
	for id, version := range map[string]string{old: "ffmpeg-detectors-1", fresh: evidence.Version} {
		if err := a.DB.Insert(ctx, store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: id, Kind: "technical",
			PromptVersion: version, Digest: "d", Data: map[string]any{"version": version}, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	steps := []Step{{Name: "visual-changes", Version: 1, Run: visualChanges}}
	outs, err := RunSteps(ctx, a, steps)
	if err != nil || !outs[0].Ran {
		t.Fatalf("ran: %+v %v", outs, err)
	}
	js := jobsOf(t, ctx, a, a.DB, jobs.TypeEvidence)
	if len(js) != 1 || js[0].AssetID != old {
		t.Fatalf("one evidence job, for the old clip: %+v", js)
	}
	if outs[0].Summary != "finding cuts, slow changes and movement in 1 clip" {
		t.Fatalf("summary: %q", outs[0].Summary)
	}
	// Once per version.
	if outs, _ := RunSteps(ctx, a, steps); outs[0].Ran {
		t.Fatal("ran twice")
	}
}

// The on-screen index needs a Mac: elsewhere the step waits (not recorded, no
// problem reported); on a Mac it queues every clip with a picture not looked at yet.
func TestCatchUpLooksAtOlderClipsOnAMac(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Old clips"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	old := clip(t, a, p.Project.ID, "old.mov", nil, 2)
	seen := clip(t, a, p.Project.ID, "seen.mov", nil, 2)
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), seen, bson.M{"$set": bson.M{"analysis.visual": "available"}}); err != nil {
		t.Fatal(err)
	}
	steps := []Step{{Name: "visual-index", Version: 1, Run: visualIndex}}

	// A machine that cannot look (no helper, no swiftc, or not a Mac).
	defer func(f func(*tools.Set) bool) { canLook = f }(canLook)
	canLook = func(*tools.Set) bool { return false }
	outs, err := RunSteps(ctx, a, steps)
	if err != nil || outs[0].Ran || !outs[0].NotHere || outs[0].Error != "" {
		t.Fatalf("not here: %+v %v", outs, err)
	}
	if Describe(outs) != "" {
		t.Fatalf("nothing to report: %q", Describe(outs))
	}
	if n, _ := a.DB.C(Collection).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "name": "visual-index"}); n != 0 {
		t.Fatal("recorded although it did not run")
	}
	// A Mac: the clips not looked at yet are queued once (heavy: battery rules apply).
	canLook = func(*tools.Set) bool { return true }
	outs, err = RunSteps(ctx, a, steps)
	if err != nil || !outs[0].Ran || outs[0].Summary != "looking at what's on screen in 1 clip" {
		t.Fatalf("on a Mac: %+v %v", outs, err)
	}
	js := jobsOf(t, ctx, a, a.DB, jobs.TypeVisual)
	if len(js) != 1 || js[0].AssetID != old || !js[0].Heavy {
		t.Fatalf("one heavy visual job, for the clip not looked at: %+v", js)
	}
	if outs, _ := RunSteps(ctx, a, steps); outs[0].Ran {
		t.Fatal("ran twice")
	}
}
