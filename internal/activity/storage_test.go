package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Moving the media shows in the activity bar in plain words, with files and
// bytes, and a stopped one says so.
func TestSummaryStorageMove(t *testing.T) {
	a := testapp.New(t)
	run := addJob(t, a, models.Job{Type: jobs.TypeStorageMove, State: models.JobRunning, Installation: a.Cfg.Installation.ID,
		Payload:  map[string]any{"storage": "Google Cloud Storage bucket team-media", "from": "the folder media on this computer"},
		Progress: models.Progress{Pct: 0.25, Message: "Copying 120 of 4,000 files", Stage: "copying", Done: 2 << 30, Total: 8 << 30, Rate: 64 << 20}})
	s := build(t, a)
	it := item(s, "job:"+run.ID)
	if it == nil {
		t.Fatalf("no storage item in %+v", s.Items)
	}
	if it.Category != CatStorage || it.Title != "Moving media to new storage" || it.Subtitle != "Google Cloud Storage bucket team-media" ||
		it.StageLabel != "Copying 120 of 4,000 files" || it.Pct == nil || *it.Pct != 0.25 || it.BytesTotal != 8<<30 || it.ETASeconds == nil {
		t.Fatalf("storage item %+v", it)
	}
	if s.Counts.Storage != 1 || !strings.Contains(strings.ToLower(s.Headline), "moving media to new storage") {
		t.Fatalf("counts %+v headline %q", s.Counts, s.Headline)
	}
	fin := time.Now().UTC().Add(-5 * time.Second)
	failed := addJob(t, a, models.Job{Type: jobs.TypeStorageMove, State: models.JobFailed, Installation: a.Cfg.Installation.ID, FinishedAt: &fin,
		Error:   "MetaRush stopped while moving the media. Start the move again: it continues where it stopped, and the old storage is unchanged.",
		Payload: map[string]any{"storage": "Google Cloud Storage bucket team-media"}})
	s = build(t, a)
	if f := item(s, "job:"+failed.ID); f == nil || f.State != StateFailed || f.CanRetry || !strings.HasPrefix(f.Error, "MetaRush stopped while moving") {
		t.Fatalf("failed move %+v", f)
	}
}
