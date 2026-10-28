package handover

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/recipes"
	"github.com/metavert-io/metarush/internal/store"
)

// Pinned work moves by check and set, by the database server's clock (D346):
// a queued job pinned to the old computer, a running one whose lease ended,
// a recipe run no look holds and a recipe's "Planning by itself" are re-pinned
// in one write each, so the old computer's worker, engine and trigger look
// either got there first (it runs it, and it finishes there) or never sees it
// again. Running work finishes where it started, or a person stops it; so do
// link imports and generated shots once they started (their downloads and
// tickets are that computer's), and a move of the media and files being sent
// from a linked folder never move.

// inFlightKinds finish where they started once they have.
var inFlightKinds = []string{jobs.TypeImport, jobs.TypeGenerate}

// stayKinds never move: they act on that computer's own files.
var stayKinds = []string{jobs.TypeStorageMove, jobs.TypeResourceUpload}

// HandoverWork is one piece of work pinned to a computer.
type HandoverWork struct {
	Kind  string `json:"kind" jsonschema:"enum=job,enum=run,enum=trigger,enum=post"`
	ID    string `json:"id"`
	Words string `json:"words" jsonschema:"description=What it is, in plain words."`
	// State is the job's or run's state (queued, running, waiting…).
	State string `json:"state"`
	// Moves: it moves to the new computer; else Why says what happens.
	Moves bool   `json:"moves"`
	Why   string `json:"why,omitempty"`
}

// jobWords names a job's work plainly.
var jobWords = map[string]string{
	jobs.TypeImport: "a link import", jobs.TypeGenerate: "AI work (a generated shot, a voice, a picture…)", jobs.TypeFetch: "a repository or web page being read",
	jobs.TypeDescribe: "a scene description", jobs.TypeStorageMove: "a move of the media", jobs.TypeResourceUpload: "a file from a linked folder being sent",
	jobs.TypeRemotionImport: "a Remotion project being brought in", jobs.TypeRemotionRefresh: "a Remotion refresh", jobs.TypeRemotionPush: "changes sent back to a repository",
	jobs.TypeAbilityRun: "an ability's run", jobs.TypeVoiceover: "an AI voice", jobs.TypeRender: "a render", jobs.TypeTranscribe: "a transcript",
}

func wordsFor(j models.Job) string {
	if w, ok := jobWords[j.Type]; ok {
		return w
	}
	return "background work"
}

func in(s string, list []string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// pinnedWork lists the work pinned to a computer and what a handover does
// with each (gone: the computer is off for good, so nothing runs there again).
func pinnedWork(ctx context.Context, a *app.App, from string, gone bool) ([]HandoverWork, error) {
	var out []HandoverWork
	var js []models.Job
	if err := a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"installation": from, "state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}}, &js,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(500)); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the work pinned to this computer.")
	}
	live := map[string]bool{}
	if len(js) > 0 {
		var leased []models.Job
		f := store.LiveLease()
		f["installation"], f["state"] = from, models.JobRunning
		if err := a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), f, &leased, options.Find().SetProjection(bson.M{"_id": 1})); err == nil {
			for _, j := range leased {
				live[j.ID] = true
			}
		}
	}
	for _, j := range js {
		w := HandoverWork{Kind: "job", ID: j.ID, Words: wordsFor(j), State: j.State}
		started := j.AttemptCount > 0 || len(j.Attempts) > 0
		switch {
		case in(j.Type, stayKinds):
			w.Why = "It acts on that computer's own files, so it finishes there (or a person stops it)."
			if gone {
				w.Why = "It acted on that computer's own files, so it stays where it was; start it again from here if it is still needed."
			}
		case live[j.ID]:
			w.Why = "It is running there now: it finishes there, or a person stops it."
		case gone:
			w.Moves = true
			if started && in(j.Type, inFlightKinds) {
				w.Why = "It starts again here (AI work goes on from the request already sent; nothing is paid for twice)."
			}
		case started && in(j.Type, inFlightKinds):
			w.Why = "It started there, so it finishes there."
		default:
			w.Moves = true
		}
		out = append(out, w)
	}
	var runs []models.RecipeRun
	if err := a.DB.Find(ctx, store.CRecipeRuns, a.WorkspaceID(), bson.M{"installation": from,
		"state": bson.M{"$in": bson.A{models.RunPlanned, models.RunRunning, models.RunWaiting}}}, &runs,
		options.Find().SetProjection(bson.M{"recipeTitle": 1, "number": 1, "state": 1}).SetLimit(200)); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the recipe runs.")
	}
	for _, r := range runs {
		out = append(out, HandoverWork{Kind: "run", ID: r.ID, Words: fmt.Sprintf("run %d of the recipe “%s”", r.Number, r.RecipeTitle), State: r.State, Moves: true})
	}
	var rs []models.Recipe
	if err := a.DB.Find(ctx, store.CRecipes, a.WorkspaceID(), bson.M{"triggers.installation": from, "archived": bson.M{"$ne": true}}, &rs,
		options.Find().SetProjection(bson.M{"title": 1}).SetLimit(200)); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the recipes.")
	}
	for _, r := range rs {
		out = append(out, HandoverWork{Kind: "trigger", ID: r.ID, Words: "“" + r.Title + "” planning by itself", Moves: true})
	}
	// Posts being sent to a video platform: only the computer sending one can
	// go on with it (its upload session is kept there), so it finishes there
	// before that computer steps down.
	var revs []models.Revision
	if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"publications": bson.M{"$elemMatch": bson.M{"state": models.PublicationSending, "installation": from}}}, &revs,
		options.Find().SetProjection(bson.M{"publications": 1, "number": 1}).SetLimit(200)); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the posts being sent.")
	}
	for _, r := range revs {
		for _, x := range r.Publications {
			if x.State != models.PublicationSending || x.Installation != from {
				continue
			}
			where := ops.PublishPlatforms[x.Platform]
			if where == "" {
				where = x.Platform
			}
			w := HandoverWork{Kind: "post", ID: x.ID, Words: fmt.Sprintf("version %d being posted to %s", r.Number, where), State: x.State,
				Why: "It is being sent from there, so it finishes there."}
			if gone {
				w.Why = "It was being sent from there and can't go on elsewhere: after a day MetaRush marks it as not posted. Check " + where + " to see if it got there."
			}
			out = append(out, w)
		}
	}
	return out, nil
}

// moveWork re-pins the work that moves from one computer to another, each
// piece by check and set; what stays (running now, started link imports
// and generated shots, the old computer's own files) is left where it is.
func moveWork(ctx context.Context, a *app.App, from, to, toName string, gone bool) (HandoverMoved, error) {
	var m HandoverMoved
	now := time.Now().UTC()
	// Jobs: queued, or running with a lease that ended by the server's clock.
	notLive := bson.M{"$or": bson.A{
		bson.M{"state": models.JobQueued},
		bson.M{"state": models.JobRunning, "$expr": bson.M{"$lt": bson.A{"$lease.expiresAt", "$$NOW"}}},
	}}
	filter := bson.M{"workspaceId": a.WorkspaceID(), "installation": from, "type": bson.M{"$nin": stayKinds}, "$and": bson.A{notLive}}
	if !gone {
		// Link imports and generated shots that started finish where they started.
		filter["$and"] = bson.A{notLive, bson.M{"$or": bson.A{
			bson.M{"type": bson.M{"$nin": inFlightKinds}},
			bson.M{"attemptCount": bson.M{"$in": bson.A{0, nil}}},
		}}}
	}
	res, err := a.DB.C(store.CJobs).UpdateMany(ctx, filter, bson.M{"$set": bson.M{"installation": to, "updatedAt": now}})
	if err != nil {
		return m, apperr.Wrap(apperr.Database, err, "MetaRush couldn't move the work pinned to the old computer.")
	}
	m.Jobs = int(res.ModifiedCount)
	// Recipe runs no look holds (by the server's clock): the lease is taken
	// away with the move, so a look that was too slow can't save over it.
	rf := recipes.LeaseFree()
	rf["workspaceId"], rf["installation"] = a.WorkspaceID(), from
	rf["state"] = bson.M{"$in": bson.A{models.RunPlanned, models.RunRunning, models.RunWaiting}}
	res, err = a.DB.C(store.CRecipeRuns).UpdateMany(ctx, rf, bson.M{"$set": bson.M{"installation": to, "updatedAt": now}, "$unset": bson.M{"lease": ""}})
	if err != nil {
		return m, apperr.Wrap(apperr.Database, err, "MetaRush couldn't move the recipe runs.")
	}
	m.Runs = int(res.ModifiedCount)
	// "Planning by itself": the old computer's look writes only while the
	// triggers are still its own.
	res, err = a.DB.C(store.CRecipes).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "triggers.installation": from},
		bson.M{"$set": bson.M{"triggers.installation": to, "triggers.computer": toName}})
	if err != nil {
		return m, apperr.Wrap(apperr.Database, err, "MetaRush couldn't move the recipes that plan by themselves.")
	}
	m.Triggers = int(res.ModifiedCount)
	return m, nil
}

// stillThere lists the work still pinned to a computer that a live one
// finishes before it steps down.
func stillThere(ctx context.Context, a *app.App, from string) ([]HandoverWork, error) {
	all, err := pinnedWork(ctx, a, from, false)
	if err != nil {
		return nil, err
	}
	var out []HandoverWork
	for _, w := range all {
		out = append(out, w) // everything still pinned there (what moves has moved)
	}
	return out, nil
}

func addMoved(a, b HandoverMoved) HandoverMoved {
	return HandoverMoved{Conversations: a.Conversations + b.Conversations, Summaries: a.Summaries + b.Summaries, Runs: a.Runs + b.Runs,
		Triggers: a.Triggers + b.Triggers, Jobs: a.Jobs + b.Jobs, Folders: a.Folders + b.Folders}
}
