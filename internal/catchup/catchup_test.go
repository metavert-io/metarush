package catchup

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// clip inserts an available clip with sound and a finished transcript (what
// footage imported before speaker detection looks like when speakers is nil).
func clip(t *testing.T, a *app.App, project, name string, sp *models.SpeakersStatus, segments int) string {
	t.Helper()
	now := time.Now().UTC()
	id := ids.New(ids.Asset)
	as := models.Asset{ID: id, WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{project}, Kind: "video", Name: name, SHA256: ids.New("sha"), State: models.AssetAvailable,
		Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 20}, Variants: map[string]models.Variant{},
		Transcript: &models.TranscriptStatus{State: "available", Segments: segments, Words: segments * 5}, Speakers: sp, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(context.Background(), store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	return id
}

func diarizeJobs(t *testing.T, a *app.App) []models.Job {
	t.Helper()
	var js []models.Job
	if err := a.DB.Find(context.Background(), store.CJobs, a.WorkspaceID(), bson.M{"type": jobs.TypeDiarize}, &js); err != nil {
		t.Fatal(err)
	}
	return js
}

// After an upgrade, a clip that has a transcript but never had speaker
// detection gets exactly one detection job; running the catch-up again (or a
// second machine running it at the same time) queues none.
func TestCatchUpQueuesSpeakerDetectionOnce(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Old clips"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	old := clip(t, a, p.Project.ID, "IMG_2391.mov", nil, 4)
	clip(t, a, p.Project.ID, "silent.mov", nil, 0) // a transcript with no speech: nothing to listen for
	clip(t, a, p.Project.ID, "done.mov", &models.SpeakersStatus{State: "available", Count: 2}, 4)
	unavailable := clip(t, a, p.Project.ID, "elsewhere.mov", &models.SpeakersStatus{State: "unavailable"}, 3)

	outs, err := Run(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	js := diarizeJobs(t, a)
	if len(js) != 2 {
		t.Fatalf("want one job for each clip without voices (2), got %d: %+v", len(js), js)
	}
	byAsset := map[string]models.Job{}
	for _, j := range js {
		byAsset[j.AssetID] = j
		if !j.Heavy || j.State != models.JobQueued || j.CreatedBy.UserID != a.Cfg.User.ID {
			t.Fatalf("the job must be heavy (battery policy), queued and the person's (notifications): %+v", j)
		}
	}
	if _, ok := byAsset[old]; !ok {
		t.Fatal("the old clip got no job")
	}
	if _, ok := byAsset[unavailable]; !ok {
		t.Fatal("a clip transcribed where speaker detection was missing got no job")
	}
	if s := Describe(outs); s == "" {
		t.Fatalf("the run should say what it did: %+v", outs)
	}

	// Again: recorded as done, nothing new.
	again, err := Run(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range again {
		if o.Ran {
			t.Fatalf("a completed step ran again: %+v", o)
		}
	}
	if n := len(diarizeJobs(t, a)); n != 2 {
		t.Fatalf("second run queued more jobs: %d", n)
	}
	// Even if the record were missing (two machines at once), the keys keep it to one job per clip.
	if _, err := a.DB.C(Collection).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "name": "speakers"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n := len(diarizeJobs(t, a)); n != 2 {
		t.Fatalf("re-running the step must not duplicate jobs: %d", n)
	}

	// The battery policy applies: a laptop below the threshold leaves it waiting,
	// "Process now" lets it run.
	laptop := jobs.Worker{ID: "ins_laptop", Name: "laptop", JobTypes: []string{jobs.TypeDiarize}, AcceptsHeavy: true, DeferHeavy: true}
	if got, err := a.Queue.Lease(ctx, laptop); err != nil || got != nil {
		t.Fatalf("a worker deferring heavy jobs leased the catch-up job: %+v %v", got, err)
	}
	if _, err := a.Queue.Expedite(ctx, []string{byAsset[old].ID}); err != nil {
		t.Fatal(err)
	}
	if got, err := a.Queue.Lease(ctx, laptop); err != nil || got == nil || got.ID != byAsset[old].ID {
		t.Fatalf("Process now should let it run on battery: %+v %v", got, err)
	}
}

// Steps are versioned per workspace: a new version runs once more.
func TestCatchUpStepsRunOncePerVersion(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	runs := 0
	step := func(v int) []Step {
		return []Step{{Name: "test-step", Version: v, Run: func(context.Context, *app.App) (string, error) { runs++; return "did it", nil }}}
	}
	for i := 0; i < 2; i++ {
		if _, err := RunSteps(ctx, a, step(1)); err != nil {
			t.Fatal(err)
		}
	}
	if runs != 1 {
		t.Fatalf("version 1 ran %d times", runs)
	}
	outs, err := RunSteps(ctx, a, step(2))
	if err != nil || runs != 2 || !outs[0].Ran {
		t.Fatalf("version 2 should run once: %d runs, %+v %v", runs, outs, err)
	}
	var rec Record
	if err := a.DB.C(Collection).FindOne(ctx, bson.M{"_id": a.WorkspaceID() + "/test-step"}).Decode(&rec); err != nil || rec.Version != 2 || rec.Summary != "did it" {
		t.Fatalf("record: %+v %v", rec, err)
	}
	// A failing step is not recorded, so it is tried again.
	fails := []Step{{Name: "flaky", Version: 1, Run: func(context.Context, *app.App) (string, error) { return "", fmt.Errorf("database busy") }}}
	outs, _ = RunSteps(ctx, a, fails)
	if outs[0].Error == "" || outs[0].Ran {
		t.Fatalf("failure: %+v", outs)
	}
	if n, _ := a.DB.C(Collection).CountDocuments(ctx, bson.M{"name": "flaky"}); n != 0 {
		t.Fatal("a failed step must not be recorded")
	}
}

// The questions earlier versions never asked appear: a project that has a cut
// but no brief gets its brief questions; the cut is offered for approval and
// music without rights is asked about.
func TestCatchUpAsksMissingQuestions(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Launch"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	video := clip(t, a, p.Project.ID, "talk.mov", &models.SpeakersStatus{State: "available"}, 3)
	music := clip(t, a, p.Project.ID, "song.m4a", &models.SpeakersStatus{State: "available"}, 0)
	plan := json.RawMessage(fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},
		"clips":[{"id":"a","assetId":%q,"in":0,"out":5}],"audio":[{"id":"bed","assetId":%q,"role":"music","in":0,"out":5,"at":0}]}}`, p.Project.ID, video, music))
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, a, "edit.check", ops.EditCheckIn{Plan: plan}, ops.Caller{})
	if err != nil || !chk.Valid {
		t.Fatalf("check: %v %+v", err, chk)
	}
	if _, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	// As an earlier version left it: none of the questions exist.
	if _, err := a.DB.C(store.CTodos).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID()}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(ctx, a); err != nil {
		t.Fatal(err)
	}
	var open []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"state": models.TodoOpen}, &open); err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, td := range open {
		kinds[td.Kind]++
	}
	if kinds[models.TodoBriefMissing] != 3 || kinds[models.TodoRevisionApproval] != 1 || kinds[models.TodoMusicRights] != 1 {
		t.Fatalf("missing questions after the catch-up: %v", kinds)
	}
}
