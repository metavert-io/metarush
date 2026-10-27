package aiwork

import (
	"context"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/vision"
)

// LookAtFrames puts a stand-in for the on-screen helper in place for one test.
func LookAtFrames(t testing.TB, f func(dir string) ([]vision.Frame, error)) {
	was := lookAtFrames
	lookAtFrames = func(_ context.Context, _ *app.App, dir string) ([]vision.Frame, error) { return f(dir) }
	t.Cleanup(func() { lookAtFrames = was })
}

// SetMakeStarted runs fn right after a presenter's making is queued, before
// makePresenter records it (tests: the job ends first).
func SetMakeStarted(fn func(jobID string)) func() {
	old := makeStarted
	makeStarted = fn
	return func() { makeStarted = old }
}
