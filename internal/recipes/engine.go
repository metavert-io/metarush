package recipes

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// The engine (D204): a run's steps advance from the database, through the
// same ops and jobs a person uses, as the person who started it (channel
// recipe in the activity log). Any loop may look at a run, but only the
// computer that started it advances it (its files and keys), one at a time
// (a lease on the run); a terminal's `recipe run --wait` advances its own run
// like the daemon does.

// leaseFor is how long one look at a run may take before another may look.
const leaseFor = 5 * time.Minute

// EngineEvery is how often the daemon looks at running runs.
var EngineEvery = 5 * time.Second

// wake makes the daemon's loop look now (a run started from the dashboard).
var wake = make(chan struct{}, 1)

func nudge() {
	select {
	case wake <- struct{}{}:
	default:
	}
}

// leaseOwner names this process's looks.
func leaseOwner(a *app.App) string {
	return a.Cfg.Installation.ID + ":" + strconv.Itoa(os.Getpid())
}

// lookName names one look at a run: the computer, the process and the look
// itself. A dashboard request and the daemon's loop live in one process (and
// a terminal beside them in another), so every look holds the lease under a
// name of its own: two steppers never work on one run at once, whoever they are.
func lookName(a *app.App) string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return leaseOwner(a) + ":" + hex.EncodeToString(b)
}

// A run's lease ends by the database server's time (D346), like a job's:
// computers' clocks differ, and a handover moving a run to another computer
// takes it only when no look holds it ($$NOW).

// LeaseFree matches a run no look holds now, by the server's time.
func LeaseFree() bson.M {
	return bson.M{"$or": bson.A{bson.M{"lease": nil}, bson.M{"$expr": bson.M{"$lt": bson.A{"$lease.until", "$$NOW"}}}}}
}

// leaseFrom is the update that gives a run's lease to by until leaseFor
// after now on the server's clock.
func leaseFrom(by string) mongo.Pipeline {
	return mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "lease", Value: bson.D{
		{Key: "by", Value: bson.D{{Key: "$literal", Value: by}}},
		{Key: "until", Value: bson.D{{Key: "$add", Value: bson.A{"$$NOW", leaseFor.Milliseconds()}}}},
	}}}}}}
}

// claim takes a run's lease for one look; nil when someone else is looking at
// it, or it isn't going on any more.
func claim(ctx context.Context, a *app.App, id string) (*models.RecipeRun, error) {
	me := lookName(a)
	var run models.RecipeRun
	filter := LeaseFree()
	filter["_id"], filter["workspaceId"], filter["state"] = id, a.WorkspaceID(), bson.M{"$in": bson.A{models.RunRunning, models.RunWaiting}}
	err := a.DB.C(store.CRecipeRuns).FindOneAndUpdate(ctx, filter, leaseFrom(me), options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&run)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if run.Lease == nil || run.Lease.By != me {
		run.Lease = &models.RunLease{By: me, Until: time.Now().UTC().Add(leaseFor)}
	}
	return &run, nil
}

// release gives a run's lease back (the look that claimed it).
func release(ctx context.Context, a *app.App, run *models.RecipeRun) {
	if run == nil || run.Lease == nil || run.Lease.By == "" {
		return
	}
	_, _ = a.DB.C(store.CRecipeRuns).UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": run.ID, "workspaceId": a.WorkspaceID(), "lease.by": run.Lease.By},
		bson.M{"$unset": bson.M{"lease": ""}})
}

// save writes a run back (only while this look holds its lease).
func save(ctx context.Context, a *app.App, run *models.RecipeRun) error {
	if run.Lease == nil || run.Lease.By == "" {
		return errLost
	}
	run.UpdatedAt = time.Now().UTC()
	lease := run.Lease
	b, err := bson.Marshal(run)
	if err != nil {
		return err
	}
	var doc bson.D
	if err := bson.Unmarshal(b, &doc); err != nil {
		return err
	}
	kept := doc[:0]
	for _, e := range doc {
		if e.Key != "lease" {
			kept = append(kept, e)
		}
	}
	// One write: the run as it is now, its lease going on from now by the
	// server's clock.
	res, err := a.DB.C(store.CRecipeRuns).UpdateOne(ctx, bson.M{"_id": run.ID, "workspaceId": a.WorkspaceID(), "lease.by": lease.By},
		mongo.Pipeline{{{Key: "$replaceWith", Value: bson.D{{Key: "$mergeObjects", Value: bson.A{
			bson.D{{Key: "$literal", Value: kept}},
			bson.D{{Key: "lease", Value: bson.D{
				{Key: "by", Value: bson.D{{Key: "$literal", Value: lease.By}}},
				{Key: "until", Value: bson.D{{Key: "$add", Value: bson.A{"$$NOW", leaseFor.Milliseconds()}}}},
			}}},
		}}}}}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return errLost
	}
	return nil
}

var errLost = errors.New("recipes: another look took the run")

// Advance moves a run on as far as it can now: steps MetaRush does at once
// run, steps waiting on jobs (renders, generated shots) look at them, a run
// waiting for Claude looks at its cut. It returns the run as it stands.
func Advance(ctx context.Context, a *app.App, id string) (*models.RecipeRun, error) {
	run, err := claim(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if run == nil {
		return getRun(ctx, a, id)
	}
	defer release(ctx, a, run)
	if run.Installation != "" && run.Installation != a.Cfg.Installation.ID {
		return run, nil // another computer has its files and keys
	}
	if run.State == models.RunWaiting && run.WaitingFor == models.RunWaitsForPerson {
		return run, nil // a person says when it goes on (recipe.continue)
	}
	e, err := newEngine(ctx, a, run)
	if err != nil {
		run.State, run.Problem = models.RunFailed, "MetaRush couldn't read the recipe version this run makes."
		now := time.Now().UTC()
		run.FinishedAt = &now
		return run, save(ctx, a, run)
	}
	for i := range run.Steps {
		st := &run.Steps[i]
		if st.State == models.StepDone || st.State == models.StepSkipped {
			continue
		}
		if st.State == models.StepFailed {
			break
		}
		if st.State == models.StepPending {
			now := time.Now().UTC()
			st.State, st.Started = models.StepRunning, &now
		}
		run.State, run.Waiting, run.WaitingFor, run.WaitingUSD = models.RunRunning, "", "", 0
		done, err := e.step(ctx, st)
		if err != nil {
			e.fail(ctx, plainFailure(err))
		}
		if done {
			now := time.Now().UTC()
			if st.State != models.StepSkipped {
				st.State = models.StepDone
			}
			st.Ended = &now
			if serr := save(ctx, a, run); serr != nil {
				return run, serr
			}
			continue
		}
		break
	}
	if run.State == models.RunRunning && allDone(run) {
		e.finished(ctx)
	}
	if err := save(ctx, a, run); err != nil {
		return run, err
	}
	e.tell(ctx)
	return run, nil
}

func allDone(run *models.RecipeRun) bool {
	for _, s := range run.Steps {
		if s.State != models.StepDone && s.State != models.StepSkipped {
			return false
		}
	}
	return true
}

// getRun reads a run.
func getRun(ctx context.Context, a *app.App, id string) (*models.RecipeRun, error) {
	var run models.RecipeRun
	if err := a.DB.GetByID(ctx, store.CRecipeRuns, a.WorkspaceID(), id, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// RunEngine is the daemon's loop: it advances the runs this computer started
// that are going on (not those waiting for a person).
func RunEngine(ctx context.Context, a *app.App, logf func(string, ...any)) {
	t := time.NewTimer(EngineEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wake:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}
		if err := advanceAll(ctx, a); err != nil && ctx.Err() == nil && logf != nil {
			logf("recipes: %v", err)
		}
		t.Reset(EngineEvery)
	}
}

// advanceAll advances every run of this computer that is going on.
func advanceAll(ctx context.Context, a *app.App) error {
	var runs []models.RecipeRun
	err := a.DB.Find(ctx, store.CRecipeRuns, a.WorkspaceID(), bson.M{"installation": a.Cfg.Installation.ID,
		"$or": bson.A{bson.M{"state": models.RunRunning}, bson.M{"state": models.RunWaiting, "waitingFor": models.RunWaitsForClaude}}}, &runs,
		options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(50))
	if err != nil {
		return err
	}
	var firstErr error
	for _, r := range runs {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := Advance(ctx, a, r.ID); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("run %s: %w", r.ID, err)
		}
	}
	return firstErr
}

// PendingJobs are the jobs a run's current step waits on (a terminal that
// waits for the run runs them).
func PendingJobs(ctx context.Context, a *app.App, run *models.RecipeRun) []string {
	var out []string
	for _, s := range run.Steps {
		if s.State != models.StepRunning {
			continue
		}
		for _, id := range s.Jobs {
			if j, err := a.Queue.Get(ctx, id); err == nil && (j.State == models.JobQueued || j.State == models.JobRunning) {
				out = append(out, id)
			}
		}
	}
	return out
}
