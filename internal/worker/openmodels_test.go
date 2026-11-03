package worker

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/looker"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakescreen"
	"github.com/metavert-io/metarush/internal/visual"
	"github.com/metavert-io/metarush/internal/visionmodels"
)

// A computer with the open models looks at new footage itself (D405, D408):
// it offers the visual job, says how it looks, and the clip's index says
// the open models made it and what they look for; the insight records them.
func TestOpenModelsLookAtFootageHere(t *testing.T) {
	s := newShop(t)
	staged := fakescreen.Stage(t)
	staged.Apply(&s.a.Cfg.Tools)
	s.a.Tools.Rescan(s.a.Cfg.Tools)
	s.a.Cfg.SetScreenIndexChoice(config.ScreenOpenModels)
	w := s.worker()
	if !w.Can(jobs.TypeVisual) || w.screenIndex() != looker.OpenModels {
		t.Fatalf("the open models: %v %q", w.JobTypes(), w.screenIndex())
	}
	if slices.ContainsFunc(w.Lacks(), func(l models.WorkerLack) bool { return l.JobType == jobs.TypeVisual }) {
		t.Fatalf("lacks %v", w.Lacks())
	}
	ctx := context.Background()
	res := ingest.File(ctx, s.a, s.footage("quay", 4, false, true), ingest.Options{ProjectID: s.project, Via: "cli", Transcribe: false})
	var visualJob, proxy string
	for _, id := range res.JobIDs {
		j, _ := s.a.Queue.Get(ctx, id)
		switch j.Type {
		case jobs.TypeVisual:
			visualJob = id
		case jobs.TypeProxy:
			proxy = id
		}
	}
	if visualJob == "" {
		t.Fatalf("no visual job queued where the open models can look: %v", res.JobIDs)
	}
	for _, j := range runJobs(t, w, proxy, visualJob) {
		if j.State != models.JobSucceeded {
			t.Fatalf("%s: %s %s", j.Type, j.State, j.Error)
		}
		if j.Type == jobs.TypeVisual && j.Result["lookedWith"] != looker.OpenModels {
			t.Fatalf("result %v", j.Result)
		}
	}
	var found []models.Insight
	if err := s.a.DB.Find(ctx, store.CInsights, s.a.WorkspaceID(), bson.M{"assetId": res.AssetID, "kind": visual.InsightKind}, &found); err != nil || len(found) != 1 {
		t.Fatalf("one index: %d %v", len(found), err)
	}
	ins := found[0]
	if ins.Provider != looker.OpenModels || ins.Model != visionmodels.ModelName || ins.PromptVersion != visual.Version {
		t.Fatalf("insight %+v", ins)
	}
	st := visual.Load(ctx, s.a.DB, s.a.WorkspaceID(), res.AssetID)
	if st == nil || !st.Index.ByOpenModels() || st.Index.Looked(looker.SeesLabels) || !st.Index.Looked(looker.SeesFaces) || st.Index.Frames == 0 {
		t.Fatalf("index %+v", st)
	}
	if s.asset(res.AssetID).Analysis[jobs.TypeVisual] != "available" {
		t.Fatalf("analysis %v", s.asset(res.AssetID).Analysis)
	}
}

// A Mac in the workspace sees more in a clip (D410): while one that takes
// big jobs was seen in the last couple of minutes, a computer with the open
// models leaves the clip to it for a while, then looks itself; an Apple
// Vision Mac that went quiet, or this computer's own record, holds nothing.
func TestAMacGetsTheFirstLook(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	w := s.worker()
	mac := func(id string, seen time.Duration, heavy bool) {
		t.Helper()
		_, err := s.a.DB.C(store.CWorkers).UpdateOne(ctx, bson.M{"_id": id},
			bson.M{"$set": bson.M{"workspaceId": s.a.WorkspaceID(), "screenIndex": looker.AppleVision, "acceptsHeavy": heavy, "jobTypes": []string{jobs.TypeVisual}}},
			options.UpdateOne().SetUpsert(true))
		if err != nil {
			t.Fatal(err)
		}
		// The heartbeat in the database server's time, as workers write it.
		if _, err := s.a.DB.C(store.CWorkers).UpdateOne(ctx, bson.M{"_id": id}, bson.A{bson.M{"$set": bson.M{"lastHeartbeat": bson.M{"$subtract": bson.A{"$$NOW", seen.Milliseconds()}}}}}); err != nil {
			t.Fatal(err)
		}
	}
	j := &models.Job{ID: "job_look", Type: jobs.TypeVisual}
	if err := macFirst(ctx, w, j); err != nil {
		t.Fatalf("no Mac about: %v", err)
	}
	mac("ins_quietmac", 10*time.Minute, true)
	if err := macFirst(ctx, w, j); err != nil {
		t.Fatalf("a Mac that went quiet: %v", err)
	}
	mac("ins_busymac", 10*time.Second, false)
	if err := macFirst(ctx, w, j); err != nil {
		t.Fatalf("a Mac not taking big jobs (on battery): %v", err)
	}
	mac(s.a.Cfg.Installation.ID, 5*time.Second, true)
	if err := macFirst(ctx, w, j); err != nil {
		t.Fatalf("this computer's own record: %v", err)
	}
	mac("ins_mac", 5*time.Second, true)
	err := macFirst(ctx, w, j)
	var wait *postponed
	if err == nil || !errors.As(err, &wait) || wait.after != macWait || !strings.Contains(wait.reason, "A Mac in this workspace sees more") {
		t.Fatalf("a Mac about: %v", err)
	}
	// After waiting macWaits times, this computer looks.
	for i := 0; i < macWaits; i++ {
		end := time.Now()
		j.Attempts = append(j.Attempts, models.Attempt{EndedAt: &end, Outcome: jobs.OutcomePostponed})
	}
	j.Attempts = append(j.Attempts, models.Attempt{})
	if err := macFirst(ctx, w, j); err != nil {
		t.Fatalf("after waiting: %v", err)
	}
}
