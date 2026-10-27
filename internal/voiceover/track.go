package voiceover

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/store"
)

// Narration work runs where it was asked for — this machine has the provider
// key, the recording and the tools — but it is recorded as a running job
// leased to this installation, so the activity bar ("Making the AI voice for
// line 3"), job details and notifications show it like any other job. The job
// starts with the first piece of real work (input mistakes never create one)
// and the person can stop it between lines.

// Narration actions (job payload "action").
const (
	ActionScratch = "scratch"
	ActionTTS     = "tts"
	ActionCleanup = "cleanup"
	ActionAlign   = "align"
	ActionClone   = "clone"
)

// ActionTitles name narration work for people.
var ActionTitles = map[string]string{
	ActionScratch: "Scratch narration",
	ActionTTS:     "AI voice",
	ActionCleanup: "Narration cleanup",
	ActionAlign:   "Placing your recording",
	ActionClone:   "Voice copy",
}

type trackKey struct{}

type tracker struct {
	a          *app.App
	action     string
	revisionID string
	projectID  string

	mu       sync.Mutex
	job      *models.Job
	prog     models.Progress
	lastBeat time.Time
	stop     chan struct{}
	canceled atomic.Bool
	lost     atomic.Bool
}

// trackable outputs carry the job that recorded them.
type trackable interface {
	setJobID(string)
	jobResult() map[string]any
}

func (o *GenerateOut) setJobID(id string) { o.JobID = id }
func (o *GenerateOut) jobResult() map[string]any {
	return withApplied(map[string]any{"summary": o.Summary, "lines": len(o.Takes), "generated": o.Generated, "reused": o.Reused, "credits": o.Credits}, o.Applied)
}
func (o *CleanupOut) setJobID(id string) { o.JobID = id }
func (o *CleanupOut) jobResult() map[string]any {
	return withApplied(map[string]any{"summary": o.Summary, "lines": len(o.Lines), "preset": o.Preset.Label}, o.Applied)
}
func (o *AlignOut) setJobID(id string) { o.JobID = id }
func (o *AlignOut) jobResult() map[string]any {
	return withApplied(map[string]any{"summary": o.Summary, "lines": len(o.Takes), "coverage": o.Coverage, "measuredWpm": o.MeasuredWPM}, o.Applied)
}

// withApplied records the version the work was put in (apply), so the
// notification says it is in the cut rather than asking to apply it.
func withApplied(r map[string]any, ap *Applied) map[string]any {
	if ap != nil && ap.Problem == "" && ap.RevisionID != "" {
		r["appliedRevisionId"], r["appliedNumber"] = ap.RevisionID, ap.Number
	}
	return r
}
func (o *CloneOut) setJobID(id string) { o.JobID = id }
func (o *CloneOut) jobResult() map[string]any {
	return map[string]any{"summary": o.Summary, "voice": o.Name, "lines": 0}
}

// track runs narration work as a recorded job (see the package comment above).
func track[T any, PT interface {
	*T
	trackable
}](ctx context.Context, a *app.App, action, revisionID, projectID string, fn func(context.Context) (PT, error)) (PT, error) {
	t := &tracker{a: a, action: action, revisionID: revisionID, projectID: projectID, stop: make(chan struct{})}
	out, err := fn(context.WithValue(ctx, trackKey{}, t))
	var res map[string]any
	if err == nil && out != nil {
		res = out.jobResult()
	}
	if id := t.finish(ctx, res, err); id != "" && out != nil {
		out.setJobID(id)
	}
	return out, err
}

// step reports progress on the narration job behind ctx, starting it on the
// first call. It returns E_CANCELED once the person asked to stop.
func step(ctx context.Context, done, total int, msg string) error {
	t, _ := ctx.Value(trackKey{}).(*tracker)
	if t == nil {
		return nil
	}
	return t.step(ctx, done, total, msg)
}

var errStopped = apperr.New(apperr.Canceled, "Stopped, as you asked.")

func (t *tracker) step(ctx context.Context, done, total int, msg string) error {
	if t.canceled.Load() {
		return errStopped
	}
	pct := 0.0
	if total > 0 {
		pct = math.Max(0, math.Min(1, float64(done)/float64(total)))
	}
	t.mu.Lock()
	t.prog = models.Progress{Pct: pct, Message: msg}
	started := t.job != nil
	t.mu.Unlock()
	if !started {
		t.start(ctx, total)
	}
	t.beat(ctx, false)
	if t.canceled.Load() {
		return errStopped
	}
	return nil
}

// start records the job (best effort: the work goes on if it cannot be recorded).
func (t *tracker) start(ctx context.Context, total int) {
	a := t.a
	payload := map[string]any{"action": t.action, "title": ActionTitles[t.action], "lines": total}
	if t.revisionID != "" {
		var rev models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), t.revisionID, &rev) == nil {
			payload["revisionId"], payload["revisionNumber"] = rev.ID, rev.Number
			if t.projectID == "" {
				t.projectID = rev.ProjectID
			}
		}
	}
	w := jobs.Worker{ID: a.Cfg.Installation.ID, Name: a.Cfg.Installation.Name}
	// Two attempts: this process is the first; if it stops, a worker takes the
	// second only to close the job plainly (interruptedNarrationHandler).
	j, err := a.Queue.Start(ctx, jobs.EnqueueOptions{Type: jobs.TypeVoiceover, ProjectID: t.projectID, Payload: payload, Actor: a.Actor, MaxAttempts: 2}, w)
	if err != nil {
		return
	}
	t.mu.Lock()
	t.job = j
	t.mu.Unlock()
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-t.stop:
				return
			case <-tick.C:
				t.beat(ctx, true)
			}
		}
	}()
}

// beat keeps the lease alive and publishes progress (at most once a second unless forced).
func (t *tracker) beat(ctx context.Context, force bool) {
	t.mu.Lock()
	j, p := t.job, t.prog
	if j == nil || (!force && time.Since(t.lastBeat) < time.Second) {
		t.mu.Unlock()
		return
	}
	t.lastBeat = time.Now()
	t.mu.Unlock()
	c, err := t.a.Queue.Heartbeat(context.WithoutCancel(ctx), j, t.a.Cfg.Installation.ID, p)
	switch {
	case errors.Is(err, jobs.ErrLostLease):
		t.lost.Store(true)
	case c:
		t.canceled.Store(true)
	}
}

// finish records the outcome and notifies the person. It returns the job ID
// (empty when the work never started a job).
func (t *tracker) finish(ctx context.Context, result map[string]any, err error) string {
	t.mu.Lock()
	j := t.job
	t.mu.Unlock()
	if j == nil {
		return ""
	}
	close(t.stop)
	if t.lost.Load() {
		return j.ID
	}
	a, owner := t.a, t.a.Cfg.Installation.ID
	fctx := context.WithoutCancel(ctx)
	ae := apperr.From(err)
	switch {
	case err != nil && (t.canceled.Load() || ae.Code == apperr.Canceled):
		_ = a.Queue.Canceled(fctx, j, owner)
		return j.ID
	case err != nil:
		// People read the job's error; the stable code stays with it for advice and agents.
		_ = a.DB.Update(fctx, store.CJobs, a.WorkspaceID(), j.ID, bson.M{"$set": bson.M{"payload.errorCode": ae.Code}})
		_ = a.Queue.Fail(fctx, j, owner, errors.New(ae.Message), false)
	default:
		if result == nil {
			result = map[string]any{}
		}
		_ = a.Queue.Succeed(fctx, j, owner, result)
	}
	if done, gerr := a.Queue.Get(fctx, j.ID); gerr == nil {
		notify.NarrationOutcome(fctx, a, done)
	}
	return j.ID
}

// InterruptedMessage is what a narration job says when the process doing it
// stopped (a worker found its lease expired).
const InterruptedMessage = "MetaRush stopped while doing this. Start it again: lines that were already made are reused at no cost."
