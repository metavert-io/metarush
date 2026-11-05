package activity

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
)

// What the activity bar says about clips' cut-outs (D505), read only from
// what Build gathered: "Cutting the people out of “Kitchen chat”" in the
// clip's project, linked to the clip — with how far along, the computer and
// the time left while it is made; why it waits (to start, for a computer that
// can make it, for a component to be added, put back for another computer);
// a failure in its first line with Try again; a finished one for a moment
// ("The cut-out of “Kitchen chat” is ready"), then nothing; a stopped one
// nothing. Only the newest try of each cut-out shows.
func TestCutOutRows(t *testing.T) {
	const pid, ast = "prj_garden", "ast_kitchen"
	setup := func(t *testing.T) *builder {
		b := testBuilder(t)
		b.projs[pid] = "Garden film"
		b.assets[ast] = &models.Asset{ID: ast, Title: "Kitchen chat", Name: "kitchen.mov", ProjectIDs: []string{pid}}
		return b
	}
	matte := func(id, state string, change ...func(*models.Job)) models.Job {
		base := []func(*models.Job){payload("matteId", id), func(j *models.Job) { j.AssetID, j.ProjectID, j.Heavy = ast, pid, true }}
		return job(jobs.TypeMatte, state, append(base, change...)...)
	}
	one := func(t *testing.T, b *builder, j models.Job) *Item {
		t.Helper()
		b.cutOuts([]models.Job{j})
		if len(b.out.Items) != 1 {
			t.Fatalf("rows: %+v", b.out.Items)
		}
		return rowFor(b, "job:"+j.ID)
	}
	link := "/p/prj_garden?asset=ast_kitchen&tab=sources"
	if links.Asset(pid, ast) != link {
		t.Fatalf("the clip's link: %s", links.Asset(pid, ast))
	}

	t.Run("being made", func(t *testing.T) {
		b := setup(t)
		j := matte("mte_1", models.JobRunning, running(0.25, "found the people in 30 of 120 frames"))
		it := one(t, b, j)
		if it.Title != "Cutting the people out of “Kitchen chat”" || it.Subtitle != "Garden film" || it.Kind != "cut-out" || it.Category != CatProcessing {
			t.Fatalf("what and where: %+v", it)
		}
		if it.State != StateActive || it.Stage != "running" || it.StageLabel != "Found the people in 30 of 120 frames" || !near(it.Pct, 0.25) {
			t.Fatalf("how far along: %+v", it)
		}
		if it.Machine != "studio-mac" || it.StartedAt == nil || it.ETASeconds == nil || math.Abs(*it.ETASeconds-120) > 3 {
			t.Fatalf("where and how long: %+v (eta %v)", it, it.ETASeconds)
		}
		if it.Link != link || it.JobID != j.ID || it.AssetID != ast || it.ProjectID != pid || it.CanRetry || it.Waiting != nil {
			t.Fatalf("its links: %+v", it)
		}
		// Before its first words, and past the end: "Working", at most all of it.
		quiet := one(t, setup(t), matte("mte_1", models.JobRunning, running(1.5, "")))
		if quiet.StageLabel != "Working" || !near(quiet.Pct, 1) || quiet.ETASeconds != nil {
			t.Fatalf("no words yet: %+v", quiet)
		}
	})

	t.Run("about to start", func(t *testing.T) {
		b := setup(t)
		b.workers = []models.WorkerDevice{onlineWorker("studio-mac", jobs.TypeMatte)}
		it := one(t, b, matte("mte_1", models.JobQueued))
		if it.State != StateActive || it.Stage != "queued" || it.StageLabel != "Waiting to start" || it.Waiting == nil ||
			it.Waiting.Code != jobs.WaitStarting || it.Waiting.Text != "Starting on studio-mac." || it.Machine != "studio-mac" || it.CanProcessNow {
			t.Fatalf("about to start: %+v %+v", it, it.Waiting)
		}
	})

	t.Run("waiting for a computer that makes cut-outs", func(t *testing.T) {
		b := setup(t)
		j := matte("mte_1", models.JobQueued)
		it := one(t, b, j)
		if it.State != StateWaiting || it.Stage != "waiting" || it.StageLabel != "Waiting" || it.Waiting == nil || it.Waiting.Code != jobs.WaitNoWorker ||
			it.Waiting.Text != "No machine running MetaRush can cut the people out of footage right now. It starts as soon as one is." {
			t.Fatalf("no computer: %+v %+v", it, it.Waiting)
		}
		if it.CanProcessNow || len(it.ProcessJobIDs) != 1 || it.ProcessJobIDs[0] != j.ID || it.Machine != "" {
			t.Fatalf("nothing to start it now: %+v", it)
		}
	})

	t.Run("waiting for a component", func(t *testing.T) {
		b := setup(t)
		w := onlineWorker("studio-mac", jobs.TypeVisual)
		w.Lacks = []models.WorkerLack{{JobType: jobs.TypeMatte, Component: "video-cut-outs",
			Says: "studio-mac can cut the people out of footage once “Replacing a video's background” is added in Settings → Components."}}
		b.workers = []models.WorkerDevice{w}
		it := one(t, b, matte("mte_1", models.JobQueued))
		if it.State != StateWaiting || it.Waiting == nil || it.Waiting.Code != jobs.WaitComponent || it.Waiting.Component != "video-cut-outs" ||
			it.Machine != "studio-mac" || !strings.Contains(it.Waiting.Text, "“Replacing a video's background” is added in Settings → Components") {
			t.Fatalf("a component to add: %+v %+v", it, it.Waiting)
		}
	})

	t.Run("put back for another computer", func(t *testing.T) {
		// Waiting for a computer with room for it, or with the model: the job was
		// put back (its last try postponed, the reason as its progress). The row
		// says that reason, in its wait line, and the waiting group says it waits
		// for another computer — never that it ran into a problem.
		for _, reason := range []string{
			"Waiting for a computer of this workspace with room to make the cut-out; this one hasn't.",
			"Waiting for a computer of this workspace that makes cut-outs with Apple Vision person segmentation; this one can't.",
		} {
			b := setup(t)
			b.workers = []models.WorkerDevice{onlineWorker("studio-mac", jobs.TypeMatte)}
			j := matte("mte_1", models.JobQueued, func(j *models.Job) {
				ended := b.now.Add(-time.Minute)
				j.NextRunAt = b.now.Add(4 * time.Minute)
				j.Progress = models.Progress{Message: reason}
				j.Attempts = []models.Attempt{{Worker: here, StartedAt: b.now.Add(-2 * time.Minute), EndedAt: &ended, Outcome: jobs.OutcomePostponed, Error: reason}}
			})
			it := one(t, b, j)
			want := strings.TrimSuffix(reason, ".") + ". It looks again in about 4 minutes."
			if it.State != StateWaiting || it.StageLabel != "Waiting for another computer" || it.Waiting == nil || it.Waiting.Code != jobs.WaitRetry ||
				it.Waiting.Text != want || !it.CanProcessNow || len(it.ProcessJobIDs) != 1 || it.ProcessJobIDs[0] != j.ID {
				t.Fatalf("put back: %+v %+v", it, it.Waiting)
			}
			b.finish()
			if len(b.out.Waiting) != 1 || b.out.Waiting[0].Text != "1 cut-out waiting for another computer" || !b.out.Waiting[0].CanProcessNow {
				t.Fatalf("the waiting group: %+v", b.out.Waiting)
			}
			for _, said := range []string{it.StageLabel, it.Waiting.Text, b.out.Waiting[0].Text, b.out.Headline} {
				if strings.Contains(said, "problem") {
					t.Fatalf("put back isn't a problem: %q", said)
				}
			}
		}
		// A try that failed waits to try again after a problem, and says so.
		b := setup(t)
		b.workers = []models.WorkerDevice{onlineWorker("studio-mac", jobs.TypeMatte)}
		j := matte("mte_1", models.JobQueued, func(j *models.Job) {
			ended := b.now.Add(-time.Minute)
			j.NextRunAt = b.now.Add(4 * time.Minute)
			j.Progress = models.Progress{Message: "Found the people in 30 of 120 frames"}
			j.Attempts = []models.Attempt{{Worker: here, StartedAt: b.now.Add(-2 * time.Minute), EndedAt: &ended, Outcome: "failed", Error: "the model stopped"}}
		})
		it := one(t, b, j)
		if it.StageLabel != "Waiting" || it.Waiting == nil || it.Waiting.Code != jobs.WaitRetry ||
			it.Waiting.Text != "It ran into a problem and tries again in about 4 minutes." || !it.CanProcessNow {
			t.Fatalf("a try that failed: %+v %+v", it, it.Waiting)
		}
		b.finish()
		if len(b.out.Waiting) != 1 || b.out.Waiting[0].Text != "1 cut-out trying again after a problem" {
			t.Fatalf("the waiting group after a problem: %+v", b.out.Waiting)
		}
	})

	t.Run("failed", func(t *testing.T) {
		b := setup(t)
		j := matte("mte_1", models.JobFailed, func(j *models.Job) {
			j.Error = "There isn't room on this computer to make the cut-out: it needs about 1.4 GB free, and 300 MB is.\nfolder: /scratch"
		})
		it := one(t, b, j)
		if it.State != StateFailed || it.Stage != "failed" || it.StageLabel != "The cut-out couldn't be made" ||
			it.Error != "There isn't room on this computer to make the cut-out: it needs about 1.4 GB free, and 300 MB is." {
			t.Fatalf("failed: %+v", it)
		}
		if !it.CanRetry || len(it.RetryJobIDs) != 1 || it.RetryJobIDs[0] != j.ID || it.Link != link {
			t.Fatalf("try again: %+v", it)
		}
	})

	t.Run("made", func(t *testing.T) {
		b := setup(t)
		it := one(t, b, matte("mte_1", models.JobSucceeded))
		if it.State != StateDone || it.Stage != "done" || !near(it.Pct, 1) || it.Title != "The cut-out of “Kitchen chat” is ready" ||
			it.StageLabel != "Cuts can put something else behind the people now" || it.Link != link || it.CanRetry {
			t.Fatalf("made: %+v", it)
		}
		// A while ago, or without a time it finished: nothing to say any more.
		for name, j := range map[string]models.Job{
			"long ago":  matte("mte_1", models.JobSucceeded, finishedAgo(time.Hour)),
			"no finish": matte("mte_1", models.JobSucceeded, func(j *models.Job) { j.FinishedAt = nil }),
			"stopped":   matte("mte_1", models.JobCanceled),
		} {
			b := setup(t)
			b.cutOuts([]models.Job{j})
			if len(b.out.Items) != 0 {
				t.Fatalf("%s: %+v", name, b.out.Items)
			}
		}
	})

	t.Run("the newest try of each cut-out", func(t *testing.T) {
		b := setup(t)
		old := matte("mte_1", models.JobFailed, func(j *models.Job) { j.CreatedAt = j.CreatedAt.Add(-time.Hour); j.Error = "lost" })
		again := matte("mte_1", models.JobRunning, running(0.5, "keeping the people with their background see-through"))
		other := matte("mte_2", models.JobQueued)
		look := job(jobs.TypeVisual, models.JobRunning, running(0.5, "looking"), func(j *models.Job) { j.AssetID = ast })
		b.cutOuts([]models.Job{again, old, other, look})
		if len(b.out.Items) != 2 || rowFor(b, "job:"+old.ID) != nil || rowFor(b, "job:"+look.ID) != nil {
			t.Fatalf("rows: %+v", b.out.Items)
		}
		if it := rowFor(b, "job:"+again.ID); it == nil || it.StageLabel != "Keeping the people with their background see-through" {
			t.Fatalf("the newest try: %+v", it)
		}
		if rowFor(b, "job:"+other.ID) == nil {
			t.Fatal("another cut-out has its own row")
		}
	})

	t.Run("where the clip is", func(t *testing.T) {
		// The job doesn't say its project: the clip's first one.
		b := setup(t)
		it := one(t, b, matte("mte_1", models.JobRunning, running(0.5, "x"), func(j *models.Job) { j.ProjectID = "" }))
		if it.ProjectID != pid || it.Subtitle != "Garden film" || it.Link != link {
			t.Fatalf("from the clip's project: %+v", it)
		}
		// A clip MetaRush didn't read, in no project: "the clip", in the Inbox.
		b = setup(t)
		it = one(t, b, matte("mte_1", models.JobRunning, running(0.5, "x"), func(j *models.Job) { j.AssetID, j.ProjectID = "ast_gone", "" }))
		if it.Title != "Cutting the people out of “the clip”" || it.Subtitle != "Inbox" || it.ProjectID != "" || it.Link != "/?asset=ast_gone" {
			t.Fatalf("a clip not read: %+v", it)
		}
		// A project whose name wasn't read: "a project".
		b = setup(t)
		delete(b.projs, pid)
		if it := one(t, b, matte("mte_1", models.JobRunning, running(0.5, "x"))); it.Subtitle != "a project" {
			t.Fatalf("a project not read: %+v", it)
		}
	})
}
