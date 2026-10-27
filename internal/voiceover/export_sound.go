package voiceover

import (
	"context"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
)

// Work other packages run the way narration work runs (internal/sound: clearer
// voices, setting up components): in the process that asked, recorded as a job
// leased to this installation once the first real step starts, so the activity
// bar, job details and notifications show it, and the person can stop it.

// Trackable is the output of tracked work: it learns its job and says what the
// job produced.
type Trackable interface {
	SetJobID(id string)
	JobResult() map[string]any
}

// TrackWork runs fn as tracked work under action (a key of ActionTitles). fn
// reports progress with Step; its job starts with the first Step.
func TrackWork[T Trackable](ctx context.Context, a *app.App, action, revisionID, projectID string, fn func(context.Context) (T, error)) (T, error) {
	t := &tracker{a: a, action: action, revisionID: revisionID, projectID: projectID, stop: make(chan struct{})}
	out, err := fn(context.WithValue(ctx, trackKey{}, t))
	var res map[string]any
	if err == nil {
		res = out.JobResult()
	}
	if id := t.finish(ctx, res, err); id != "" && err == nil {
		out.SetJobID(id)
	}
	return out, err
}

// Step reports progress on the tracked work behind ctx (starting its job on
// the first call); it returns E_CANCELED once the person asked to stop. Outside
// tracked work it does nothing.
func Step(ctx context.Context, done, total int, msg string) error { return step(ctx, done, total, msg) }

// MixView is a cut's mix in plain words (what mix.show answers), for a saved
// version or a change previewed before it is saved.
func MixView(revisionID string, s *editspec.Spec, plan *editspec.RenderPlan) *MixOut {
	return mixView(revisionID, s, plan)
}
