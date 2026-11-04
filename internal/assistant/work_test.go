package assistant

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
)

// finishRender plays a worker finishing a render (5 s long) and its automatic
// quality check with this verdict and number of things worth checking; it
// returns the report's ID.
func finishRender(t *testing.T, a *app.App, renderID, verdict string, warnings int) string {
	t.Helper()
	ctx := context.Background()
	var rd models.Render
	if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), renderID, &rd); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := a.DB.Update(ctx, store.CRenders, a.WorkspaceID(), renderID, bson.M{"$set": bson.M{"state": models.RenderAvailable, "completedAt": now,
		"output": models.RenderOutput{StorageKey: "renders/x.mp4", SHA256: "ab", Size: 1000, Duration: 5, Width: 540, Height: 960, FPS: 30}}}); err != nil {
		t.Fatal(err)
	}
	rep := models.QAReport{ID: ids.New(models.QAReportPrefix), WorkspaceID: a.WorkspaceID(), ProjectID: rd.ProjectID, RevisionID: rd.RevisionID,
		RevisionNumber: rd.RevisionNumber, RenderID: rd.ID, Quality: rd.Quality, Verdict: verdict, Counts: models.QACounts{Warnings: warnings},
		Findings: []models.QAFinding{}, Checks: []models.QACheck{}, Frames: []models.QAFrame{}, Limitations: []string{}, CreatedAt: now}
	for i := 0; i < warnings; i++ {
		rep.Findings = append(rep.Findings, models.QAFinding{ID: "f1", Severity: models.QAWarning, Kind: "silence", Basis: models.QAObserved, From: 1, To: 2,
			Timecode: "0:01", Message: "Around 0:01 nobody speaks for a second."})
	}
	if err := a.DB.Insert(ctx, store.CQAReports, rep); err != nil {
		t.Fatal(err)
	}
	j := models.Job{ID: ids.New(ids.Job), WorkspaceID: a.WorkspaceID(), Type: jobs.TypeQA, State: models.JobSucceeded, ProjectID: rd.ProjectID,
		Payload: map[string]any{"renderId": rd.ID, "revisionId": rd.RevisionID, "auto": true}, IdempotencyKey: "qa:" + rd.ID + ":test",
		Result: map[string]any{"reportId": rep.ID, "verdict": verdict}, Attempts: []models.Attempt{}, CreatedAt: now, UpdatedAt: now, FinishedAt: &now}
	if err := a.DB.Insert(ctx, store.CJobs, j); err != nil {
		t.Fatal(err)
	}
	return rep.ID
}

// A generated shot is watched until its clip lands (Claude hears it then, not
// when it was asked for); a priced-only run has nothing to watch.
func TestGeneratedShotIsWatchedUntilItLands(t *testing.T) {
	m := &Manager{}
	w := m.workFor(context.Background(), &generate.GenRunOut{JobIDs: []string{"job_a"}, Generations: []generate.GenView{{Generation: models.Generation{ProjectID: "prj_x"}}}})
	if w == nil || w.Kind != WorkJobs || len(w.JobIDs) != 1 || w.JobIDs[0] != "job_a" || w.What != "the generated shot" || w.Link != "/p/prj_x?tab=sources" {
		t.Fatalf("watch %+v", w)
	}
	if w := m.workFor(context.Background(), &generate.GenRunOut{DryRun: true, JobIDs: []string{"job_a"}}); w != nil {
		t.Fatalf("a priced-only run is not watched: %+v", w)
	}
}

// A generated shot that couldn't be made says why on its card in the
// generation's own words, without the error code (a free-tier key, D178);
// Claude gets the error, what to tell the person and how to read the record.
func TestGeneratedShotThatCouldntBeMade(t *testing.T) {
	msg := "This Google project is on the free tier, which can't make video. Turn on billing for the project in Google AI Studio, then try again."
	advice := msg + " Google AI Studio shows the project's tier and its limits: https://ai.dev/rate-limit. Nothing was made or charged."
	j := models.Job{ID: "job_a", Type: jobs.TypeGenerate, State: models.JobFailed, Error: "E_PROVIDER: " + msg,
		Payload: map[string]any{"generationId": "gen_a", "advice": advice}}
	shown, forClaude, ok := jobOutcome(j)
	if ok || shown != "The shot couldn't be made: "+strings.TrimSuffix(msg, ".") {
		t.Fatalf("shown %q", shown)
	}
	if f := plainwords.CheckPanel(shown); len(f) > 0 {
		t.Fatalf("not plain: %v", f)
	}
	if !strings.Contains(forClaude, "E_PROVIDER") || !strings.Contains(forClaude, "https://ai.dev/rate-limit") || !strings.Contains(forClaude, "metarush generate show gen_a --json") {
		t.Fatalf("for Claude %q", forClaude)
	}
	j.State, j.Error = models.JobCanceled, "canceled"
	if shown, _, _ := jobOutcome(j); shown != "The shot was stopped" {
		t.Fatalf("stopped %q", shown)
	}
}

// What Claude hears when a cut-out is ready names the background command's
// own flags (its usage line in the command index), never a spelling the
// command doesn't take, and each command in it parses as that command.
func TestTheCutOutNoteSpeaksTheCommandsFlags(t *testing.T) {
	j := models.Job{ID: "job_a", Type: jobs.TypeMatte, AssetID: "ast_a", State: models.JobSucceeded, Result: map[string]any{"sha256": strings.Repeat("ab", 32)}}
	_, forClaude, ok := jobOutcome(j)
	if !ok || !strings.Contains(forClaude, "metarush cut background") {
		t.Fatalf("for Claude %q", forClaude)
	}
	usage, found := catalog.CommandFor("cut.background")
	if !found {
		t.Fatal("cut.background has no command")
	}
	flags := regexp.MustCompile(`--[a-z][a-z-]*`)
	for _, f := range flags.FindAllString(forClaude, -1) {
		if !strings.Contains(usage.Usage, f+" ") && !strings.Contains(usage.Usage, f+"]") {
			t.Errorf("%s isn't a flag of %q", f, usage.Usage)
		}
	}
	if strings.Contains(forClaude, "colour:") || strings.Contains(forClaude, "picture:") {
		t.Errorf("a spelling the command doesn't take: %q", forClaude)
	}
	for _, cmd := range regexp.MustCompile("`(metarush cut background[^`]*)`").FindAllStringSubmatch(forClaude, -1) {
		w, err := splitCommand(strings.NewReplacer("<project>", "prj_x", "<clip id>", "c1", "<name>", "logo", "<clip>", "ast_b", "[--footage-at 0:03]", "--footage-at 0:03").Replace(cmd[1]))
		if err != nil {
			t.Fatal(err)
		}
		if c, ok := parseMetarush(w); !ok || c.Path != "cut background" || c.Flags["clip"] != "c1" {
			t.Errorf("%s: %+v", cmd[1], c)
		}
	}
}
