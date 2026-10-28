package recipes

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// Starting, going on and stopping (D204): a run starts only with a person's
// OK (recipe.run and recipe.again are people only; Auto never starts one),
// with one estimate agreed for the whole run — unless the person turned
// "Estimate costs of outside AI services before edits" off (D208,
// prefs.EstimatesFirst): then it starts without the estimate step, within
// budgets, daily limits and project caps; going past the agreed amount,
// consent and spending refused by a budget wait for a person
// (recipe.continue).

// startRun makes a run from a plan the person agreed to. A plan with an
// expensive kind of work starts with its sample (D294): the person agrees to
// the sample's price, and the rest is planned once they approve it — unless
// they skip the sample with a reason (skipSample), which is recorded.
func startRun(ctx context.Context, a *app.App, in planIn, fingerprint string, maxUSD *float64, skipSample string) (*models.RecipeRun, error) {
	p, err := makePlan(ctx, a, in)
	if err != nil {
		return nil, err
	}
	if len(p.Problems) > 0 {
		return nil, apperr.New(apperr.Validation, "This run can't start: %s", strings.TrimSuffix(p.Problems[0], ".")+".").
			WithDetails(map[string]any{"problems": p.Problems})
	}
	if p.Version.Hash != p.Recipe.Current {
		return nil, apperr.New(apperr.Validation, "Runs make the current version of “%s”; version %d isn't it.", p.Recipe.Title, p.Version.Number).
			WithHint("A person makes it current first.")
	}
	if strings.TrimSpace(fingerprint) != p.Fingerprint {
		return nil, apperr.New(apperr.Conflict, "The plan changed since it was shown (an input, the recipe or a price), so nothing started.").
			WithHint("Look at the plan again, then start it.").WithDetails(map[string]any{"fingerprint": p.Fingerprint})
	}
	var agreed *models.RecipeAgreement
	now := time.Now().UTC()
	skipSample = strings.TrimSpace(skipSample)
	var sample *models.RecipeSample
	steps := p.Steps
	if sp := p.sample; sp != nil {
		sample = &models.RecipeSample{State: models.SampleMaking, Kinds: sp.Kinds, Estimate: sp.Estimate}
		if skipSample != "" {
			if utf8.RuneCountInString(skipSample) < 8 {
				return nil, apperr.New(apperr.InvalidInput, "Say why the run goes without its sample (a few words); the reason is kept with the run.")
			}
			sample.State, sample.Skipped, sample.By, sample.At = models.SampleSkipped, shorten(skipSample, 500), &a.Actor, &now
			steps = withoutSteps(steps, stepSample, stepApprove)
		} else {
			steps = untilStep(steps, stepApprove) // the rest is planned once the sample is approved
		}
	}
	if p.Ask {
		usd, about := p.Estimate.USD, p.Estimate.About
		if sample != nil && sample.State == models.SampleMaking {
			usd, about = sample.Estimate.USD, sample.Estimate.About // what is agreed now: the sample
		}
		up := usd
		if usd > 0 {
			if maxUSD == nil || *maxUSD+0.0005 < usd {
				return nil, confirmCost(p, sample != nil && sample.State == models.SampleMaking, about)
			}
			up = costs.Round(math.Max(usd, *maxUSD))
			if sample != nil && sample.State == models.SampleMaking {
				up = costs.Round(usd) // the rest is agreed after the sample
			}
		}
		agreed = &models.RecipeAgreement{UpTo: up, By: a.Actor, At: now}
	} else {
		agreed = &models.RecipeAgreement{By: a.Actor, At: now, Skipped: true}
	}
	// The run goes on where this computer's background work runs: here, or
	// the computer that has its role now (D349).
	host, err := a.PinWork(ctx)
	if err != nil {
		return nil, err
	}
	var r models.Recipe
	if err := a.DB.C(store.CRecipes).FindOneAndUpdate(ctx, bson.M{"_id": p.Recipe.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$inc": bson.M{"runs": 1}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&r); err != nil {
		return nil, err
	}
	run := &models.RecipeRun{ID: ids.New(ids.RecipeRun), WorkspaceID: a.WorkspaceID(), RecipeID: r.ID, RecipeName: r.Name, RecipeTitle: p.Doc.Title,
		Number: r.Runs, Version: p.Version.Hash, VersionNumber: p.Version.Number, Inputs: p.Inputs, Note: p.Note, Settings: p.Settings,
		Fingerprint: p.Fingerprint, Estimate: p.Estimate, Agreed: agreed, Steps: steps, State: models.RunRunning, Outputs: p.Outputs, Sample: sample,
		Installation: host.ID, CreatedBy: a.Actor, CreatedAt: now, UpdatedAt: now}
	switch {
	case p.Prev != nil:
		run.Repeats, run.ProjectID, run.ProjectName, run.Branch = p.Prev.ID, p.Project.ID, p.Project.Name, p.Branch
	case p.Project != nil:
		run.ProjectID, run.ProjectName = p.Project.ID, p.Project.Name
		run.Branch = freeBranch(p.Project, r.Name, run.Number)
	default:
		run.NewProject, run.ProjectName = true, nameWithInputs(newProjectName(p.Doc, run.Number), p.Values)
	}
	if err := a.DB.Insert(ctx, store.CRecipeRuns, run); err != nil {
		return nil, err
	}
	// A plan the recipe made by itself (D218) is used up by any run that starts.
	_, _ = a.DB.C(store.CRecipes).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$set": bson.M{"lastRun": models.RecipeRunRef{ID: run.ID, Number: run.Number, State: run.State, At: now, By: a.Actor}}, "$unset": bson.M{"prepared": ""}})
	details := map[string]any{"run": run.Number, "version": run.VersionNumber, "estimateUsd": p.Estimate.USD, "estimateFirst": p.Ask}
	if sample != nil {
		details["sample"], details["sampleUsd"] = sample.State, sample.Estimate.USD
		if sample.Skipped != "" {
			details["sampleSkipped"] = sample.Skipped // why a person went without it (D294)
		}
	}
	if run.Repeats != "" {
		details["repeats"] = run.Repeats
	}
	a.Audit(ctx, "recipe.run", "recipe", r.ID, run.ProjectID, details)
	if !advanceOnStart {
		return run, nil
	}
	return advanceNow(ctx, a, run), nil
}

// advanceNow takes a run's first steps at once — or, for a request from the
// dashboard, wakes the daemon's loop so the request returns right away.
func advanceNow(ctx context.Context, a *app.App, run *models.RecipeRun) *models.RecipeRun {
	if ops.CallerFrom(ctx).Remote {
		nudge()
		return run
	}
	if out, err := Advance(ctx, a, run.ID); err == nil && out != nil {
		return out
	}
	return run
}

// advanceOnStart: a run takes its first steps as it starts (tests turn it
// off to look at a run before it moves).
var advanceOnStart = true

// confirmCost refuses a start without the person's OK for the estimate (the
// sample's, when the run starts with its sample, D294).
func confirmCost(p *planned, sample bool, about string) error {
	if sample {
		return apperr.New(apperr.ConfirmRequired, "This run starts with a sample that costs %s; agree to that amount to make it (the rest, %s, is planned once you approve the sample).", about, p.Estimate.About).
			WithHint("Look at the sample's estimate, then start it with an amount at least that large — or skip the sample with a reason.").
			WithDetails(map[string]any{"estimate": p.sample.Estimate, "fingerprint": p.Fingerprint, "sample": true})
	}
	return apperr.New(apperr.ConfirmRequired, "This run costs %s; agree to that amount to start it.", p.Estimate.About).
		WithHint("Look at the estimate, then start it with an amount at least that large.").
		WithDetails(map[string]any{"estimate": p.Estimate, "fingerprint": p.Fingerprint})
}

// untilStep keeps a run's steps up to and including the one called id.
func untilStep(steps []models.RecipeStep, id string) []models.RecipeStep {
	for i, s := range steps {
		if s.ID == id {
			return append([]models.RecipeStep{}, steps[:i+1]...)
		}
	}
	return steps
}

// withoutSteps leaves the steps called ids out.
func withoutSteps(steps []models.RecipeStep, ids ...string) []models.RecipeStep {
	var out []models.RecipeStep
	for _, s := range steps {
		if !containsString(ids, s.ID) {
			out = append(out, s)
		}
	}
	return out
}

// continueRun lets a run that waits for a person go on, optionally agreeing
// to spend more.
func continueRun(ctx context.Context, a *app.App, ref string, moreUSD *float64) (*models.RecipeRun, error) {
	run, err := loadRun(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	if run.State != models.RunWaiting || run.WaitingFor != models.RunWaitsForPerson {
		return nil, apperr.New(apperr.Conflict, "%s isn't waiting for anyone's OK (%s).", upperFirst(runName(run)), strings.ToLower(stateWords(run)))
	}
	if run.Sample != nil && run.Sample.State == models.SampleWaiting {
		return nil, apperr.New(apperr.Conflict, "%s waits for you to approve its sample first (or say what isn't right).", upperFirst(runName(run))).
			WithHint("Watch and listen to the sample on the run's page, then approve it: the rest of the run is planned then.").
			WithCommand("metarush recipe sample " + runRef(run) + " --approve")
	}
	// A run that asks to spend more goes on only with the person's OK for that
	// much (going on without it would only ask again).
	if ask := run.WaitingUSD; ask > 0 && run.Agreed != nil && !run.Agreed.Skipped && (moreUSD == nil || *moreUSD+0.0005 < ask) {
		return nil, apperr.New(apperr.ConfirmRequired, "%s asks to spend %s more than you agreed to; agree to that to let it go on.", upperFirst(runName(run)), costs.Money(ask)).
			WithHint("Go on with an amount at least that large, or stop the run.").WithDetails(map[string]any{"waitingUsd": ask})
	}
	now := time.Now().UTC()
	set := bson.M{"state": models.RunRunning, "waiting": "", "waitingFor": "", "updatedAt": now}
	if run.Agreed != nil && !run.Agreed.Skipped && moreUSD != nil {
		more := costs.Round(math.Max(0, *moreUSD))
		run.Agreed.UpTo = costs.Round(run.Agreed.UpTo + more)
		run.Agreed.More = append(run.Agreed.More, models.RecipeMore{USD: more, By: a.Actor, At: now})
		set["agreed"] = run.Agreed
	}
	for i := range run.Steps {
		if run.Steps[i].State == models.StepWaiting {
			set[fmt.Sprintf("steps.%d.state", i)] = models.StepRunning
			if strings.HasPrefix(run.Steps[i].Detail, "Waiting for") {
				set[fmt.Sprintf("steps.%d.detail", i)] = "" // what it waited for is answered
			}
		}
	}
	res, err := a.DB.C(store.CRecipeRuns).UpdateOne(ctx, bson.M{"_id": run.ID, "workspaceId": a.WorkspaceID(), "state": models.RunWaiting},
		bson.M{"$set": set, "$unset": bson.M{"waitingUsd": ""}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, apperr.New(apperr.Conflict, "%s changed meanwhile; look at it again.", upperFirst(runName(run)))
	}
	details := map[string]any{"run": run.Number}
	if moreUSD != nil {
		details["moreUsd"] = costs.Round(math.Max(0, *moreUSD))
	}
	a.Audit(ctx, "recipe.continue", "recipe", run.RecipeID, run.ProjectID, details)
	fresh, err := getRun(ctx, a, run.ID)
	if err != nil {
		return nil, err
	}
	return advanceNow(ctx, a, fresh), nil
}

// stopRun stops a run: queued work it asked for is called off; what it made stays.
func stopRun(ctx context.Context, a *app.App, ref string) (*models.RecipeRun, error) {
	run, err := loadRun(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	switch run.State {
	case models.RunDone, models.RunStopped, models.RunFailed:
		return nil, apperr.New(apperr.Conflict, "%s already ended (%s).", upperFirst(runName(run)), strings.ToLower(stateWords(run)))
	}
	now := time.Now().UTC()
	who := a.Actor.UserName
	if who == "" {
		who = "someone"
	}
	for i := range run.Steps {
		s := &run.Steps[i]
		if s.State == models.StepRunning || s.State == models.StepWaiting {
			for _, j := range s.Jobs {
				_, _ = a.Queue.CancelQueued(ctx, j)
			}
			s.State, s.Ended = models.StepFailed, &now
		}
	}
	run.State, run.Problem, run.Waiting, run.WaitingFor, run.WaitingUSD, run.FinishedAt = models.RunStopped, "Stopped by "+who+".", "", "", 0, &now
	endSample(run, now) // a sample nobody decided on ends with its run (D294)
	run.UpdatedAt = now
	run.Lease = nil
	res, err := a.DB.C(store.CRecipeRuns).ReplaceOne(ctx, bson.M{"_id": run.ID, "workspaceId": a.WorkspaceID(), "state": bson.M{"$in": bson.A{models.RunRunning, models.RunWaiting, models.RunPlanned}}}, run)
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, apperr.New(apperr.Conflict, "%s changed meanwhile; look at it again.", upperFirst(runName(run)))
	}
	_, _ = todos.CloseStale(ctx, a, "recipe-run:"+run.ID+":", nil)
	a.Audit(ctx, "recipe.stop", "recipe", run.RecipeID, run.ProjectID, map[string]any{"run": run.Number})
	return run, nil
}

// projectGone marks the runs of a project cleared out for good (D196): their
// records stay; one still going is stopped.
func projectGone(ctx context.Context, a *app.App, projectID string) (int, error) {
	now := time.Now().UTC()
	res, err := a.DB.C(store.CRecipeRuns).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID, "state": bson.M{"$in": bson.A{models.RunRunning, models.RunWaiting, models.RunPlanned}}},
		bson.M{"$set": bson.M{"state": models.RunStopped, "problem": "Its project was cleared out.", "waiting": "", "waitingFor": "", "finishedAt": now, "updatedAt": now},
			"$unset": bson.M{"waitingUsd": ""}})
	if err != nil {
		return 0, err
	}
	if _, err := a.DB.C(store.CRecipeRuns).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID}, bson.M{"$set": bson.M{"projectGone": true}}); err != nil {
		return 0, err
	}
	// A short of a run's pack (D214) that was its own project: marked; the run goes on.
	if _, err := a.DB.C(store.CRecipeRuns).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "outputs.projectId": projectID},
		bson.M{"$set": bson.M{"outputs.$[o].projectGone": true}}, options.UpdateMany().SetArrayFilters([]any{bson.M{"o.projectId": projectID}})); err != nil {
		return 0, err
	}
	// A recipe whose example was in it shows none.
	if _, err := a.DB.C(store.CRecipes).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "example.projectId": projectID}, bson.M{"$unset": bson.M{"example": ""}}); err != nil {
		return 0, err
	}
	return int(res.ModifiedCount), nil
}

func init() {
	ops.RegisterPurgeHook("recipes", projectGone)
}

// runsOf lists runs, newest first.
func runsOf(ctx context.Context, a *app.App, filter bson.M, limit int64) ([]models.RecipeRun, error) {
	var out []models.RecipeRun
	err := a.DB.Find(ctx, store.CRecipeRuns, a.WorkspaceID(), filter, &out, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(limit))
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	return out, err
}
