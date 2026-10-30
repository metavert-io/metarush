package assistant

import (
	"context"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil"
)

// A card following a review copy waits qaGrace after the render finished for
// its quality check, by the database server's time (the render's completion
// is written in it on the computer that rendered it: D360).
func TestACardWaitsForTheQualityCheckByTheServersTime(t *testing.T) {
	a := testApp(t, "")
	m := NewTerminal(a, "/usr/local/bin/metarush-test")
	ctx := context.Background()
	gone := func(skew, ago time.Duration) bool {
		t.Helper()
		now, err := a.DB.ServerNow(ctx)
		if err != nil {
			t.Fatal(err)
		}
		done := now.Add(-ago)
		restore := testutil.SkewClock(t, skew)
		defer restore()
		ended := false
		w := ActionWork{RenderID: "rnd_none", RenderDoneAt: &done}
		m.checkQA(ctx, w, &models.Render{ID: "rnd_none"}, nil, func(state, outcome, forClaude string) (ActionWork, bool) {
			ended = state == WorkGone
			return w, true
		})
		return ended
	}
	if gone(10*time.Minute, qaGrace-30*time.Second) {
		t.Fatal("a computer ten minutes fast stopped waiting for the quality check early")
	}
	if !gone(-10*time.Minute, qaGrace+30*time.Second) {
		t.Fatal("a computer ten minutes slow waited on for a quality check that never came")
	}
}
