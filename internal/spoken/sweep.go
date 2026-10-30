package spoken

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A change whose making stopped without a word — its job failed, was
// stopped or is gone, or its speech ended without the change hearing — is
// failed with why, so the person can try again (D310). AI work's sweep runs
// it (the daemon's, when it starts and every five minutes).

// sweepPause runs between the sweep's reading and its writing (tests only).
var sweepPause = func() {}

// staleAfter is how long a change may be "being made" before its job is
// looked at, by the database server's clock (D360): every daemon sweeps, and a
// computer whose clock runs ahead must not look at a change another computer
// is just making.
const staleAfter = 2 * time.Minute

func init() {
	aiwork.RegisterSweep(Sweep)
}

// Sweep fails the changes left "being made" whose making has ended or is gone,
// and the lip sync left being made the same way; it says how many.
func Sweep(ctx context.Context, a *app.App) int {
	var cs []models.SpokenChange
	_ = a.DB.Find(ctx, store.CSpokenChanges, a.WorkspaceID(), bson.M{"$or": bson.A{
		bson.M{"state": models.SpokenMaking}, bson.M{"mouth.state": models.SpokenMaking}},
		"$and": bson.A{store.StaleBefore("updatedAt", staleAfter)}}, &cs)
	sweepPause()
	n := 0
	for i := range cs {
		c := &cs[i]
		if c.State == models.SpokenMaking {
			if why := stopped(ctx, a, c.GenerationID, c.JobID, c.Sound != ""); why != "" {
				// Only as it was read: another computer may have finished it meanwhile.
				res, err := a.DB.C(store.CSpokenChanges).UpdateOne(ctx, bson.M{"_id": c.ID, "workspaceId": a.WorkspaceID(), "state": models.SpokenMaking, "updatedAt": c.UpdatedAt},
					bson.M{"$set": bson.M{"state": models.SpokenFailed, "why": why}, "$currentDate": bson.M{"updatedAt": true}})
				if err == nil && res.MatchedCount == 1 {
					tell(ctx, a, c, false, why)
					n++
				}
			}
		}
		if m := c.Mouth; m != nil && m.State == models.SpokenMaking {
			if why := stopped(ctx, a, m.GenerationID, m.JobID, m.AssetID != ""); why != "" {
				res, err := a.DB.C(store.CSpokenChanges).UpdateOne(ctx, bson.M{"_id": c.ID, "workspaceId": a.WorkspaceID(), "mouth.state": models.SpokenMaking, "mouth.generationId": m.GenerationID},
					bson.M{"$set": bson.M{"mouth.state": models.SpokenFailed, "mouth.why": why}, "$currentDate": bson.M{"updatedAt": true}})
				if err == nil && res.MatchedCount == 1 {
					n++
				}
			}
		}
	}
	return n
}

// stopped says why work that should have reported has stopped ("" while it
// may still be going): its generation ended without the change hearing, or
// its job ended or is gone.
func stopped(ctx context.Context, a *app.App, genID, jobID string, kept bool) string {
	const again = " Nothing was put in the cut; try again."
	if genID != "" {
		var g models.Generation
		err := a.DB.GetByID(ctx, store.CGenerations, a.WorkspaceID(), genID, &g)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return "" // couldn't be read just now: looked at again on the next sweep
		}
		if err == nil && g.Outcome != nil {
			switch {
			case g.Outcome.State == models.GenDone && !kept:
				return "The speech was made, but MetaRush stopped before it was checked." + again
			case g.Outcome.State == models.GenCanceled:
				return "It was stopped before it was made." + again
			case g.Outcome.State != models.GenDone:
				return strings.TrimSpace(firstNonEmpty(g.Outcome.Message, "It couldn't be made.") + again)
			}
			return ""
		}
	}
	if jobID == "" {
		return "MetaRush lost track of it before it was asked for." + again
	}
	j, err := a.Queue.Get(ctx, jobID)
	switch {
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return "" // couldn't be read just now: it may still be running
	case err != nil:
		return "The work making it is gone." + again
	case j.State == models.JobFailed:
		return strings.TrimSpace("It couldn't be made: "+firstNonEmpty(firstLineOf(j.Error), "the work stopped.")) + again
	case j.State == models.JobCanceled:
		return "It was stopped before it was made." + again
	case j.State == models.JobSucceeded && !kept:
		return "The speech was made, but MetaRush stopped before it was checked." + again
	}
	return ""
}

func firstLineOf(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
