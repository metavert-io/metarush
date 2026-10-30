package logbook

import (
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
)

// How long an attempt ran comes from the job's own times, the database
// server's (D368): this computer's clock being off doesn't change it.
func TestAnAttemptRanForItsOwnTimes(t *testing.T) {
	start := time.Now().Add(-time.Hour)
	end := start.Add(3 * time.Second)
	j := &models.Job{Attempts: []models.Attempt{{StartedAt: start}}, FinishedAt: &end}
	if got := ranFor(j, 0); got != 3*time.Second {
		t.Fatalf("ran for %v, want 3s", got)
	}
}
