package activity

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A refresh from a source's own steps (D173) and a push back (D176) show in the
// activity bar in plain words: the step it is on, how it ended, where to look.
func TestRemotionRefreshAndPushInActivity(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Teaser film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	src := ids.New(ids.RemotionSource)
	run := addJob(t, a, models.Job{Type: jobs.TypeRemotionRefresh, State: models.JobRunning, ProjectID: p.ID,
		Payload:  map[string]any{"refresh": ids.New(ids.RemotionRefresh), "source": src, "title": "Refreshing “Teaser” from its own steps (refresh 1)"},
		Progress: models.Progress{Pct: 0.4, Message: "step 2 of 3: “vo”"}})
	fin := now.Add(-10 * time.Second)
	sent := addJob(t, a, models.Job{Type: jobs.TypeRemotionPush, State: models.JobSucceeded, ProjectID: p.ID, FinishedAt: &fin,
		Payload: map[string]any{"push": ids.New(ids.RemotionPush), "source": src, "title": "Sending version 2 of “Teaser” back to github.com/acme/teaser"},
		Result:  map[string]any{"message": "Sent version 2 of “Teaser” to github.com/acme/teaser as the branch metarush/teaser-v2."}})
	failed := addJob(t, a, models.Job{Type: jobs.TypeRemotionRefresh, State: models.JobFailed, ProjectID: p.ID, FinishedAt: &fin,
		Error:   "Step 1 of 1 (npm run vo) didn't work: ELEVENLABS_API_KEY is not set. Nothing was kept.",
		Payload: map[string]any{"refresh": ids.New(ids.RemotionRefresh), "source": src, "title": "Refreshing “Teaser” from its own steps (refresh 2)"}})
	s := build(t, a)
	it := item(s, "job:"+run.ID)
	if it == nil || it.Category != CatProcessing || it.Kind != "remotion-refresh" || it.Title != "Refreshing “Teaser” from its own steps (refresh 1)" ||
		it.StageLabel != "Step 2 of 3: “vo”" || it.Pct == nil || *it.Pct != 0.4 || it.Subtitle != "Teaser film" || !strings.Contains(it.Link, src) {
		t.Fatalf("refresh item %+v", it)
	}
	if done := item(s, "job:"+sent.ID); done == nil || done.State != StateDone || !strings.Contains(done.StageLabel, "metarush/teaser-v2") {
		t.Fatalf("push item %+v", done)
	}
	if f := item(s, "job:"+failed.ID); f == nil || f.State != StateFailed || !strings.Contains(f.Problem, "didn't work") || f.CanRetry {
		t.Fatalf("failed refresh %+v", f)
	}
	if s.Counts.Processing < 1 {
		t.Errorf("counts %+v", s.Counts)
	}
}
