package activity

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// recipeRuns adds the runs of recipes (D204): the ones going on, the ones
// waiting for a person or for Claude (said plainly: "Waiting for your OK: …",
// "Waiting for Claude to choose the moments …") and the ones that ended recently.
func (b *builder) recipeRuns(projectID string) {
	filter := bson.M{"workspaceId": b.a.WorkspaceID(),
		"$or": bson.A{bson.M{"state": bson.M{"$in": bson.A{models.RunRunning, models.RunWaiting}}}, bson.M{"finishedAt": bson.M{"$gte": b.now.Add(-b.recent)}}}}
	if projectID != "" {
		filter["projectId"] = projectID
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var runs []models.RecipeRun
	if err := b.a.DB.Find(ctx, store.CRecipeRuns, b.a.WorkspaceID(), filter, &runs, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(20)); err != nil {
		return
	}
	for _, r := range runs {
		it := Item{Key: "recipe:" + r.ID, Category: CatRecipe, Kind: "recipe", Title: fmt.Sprintf("Run %d of “%s”", r.Number, r.RecipeTitle),
			Subtitle: r.ProjectName, RunID: r.ID, ProjectID: r.ProjectID, UpdatedAt: r.UpdatedAt, Link: links.RecipeRun(r.ID)}
		started := r.CreatedAt
		it.StartedAt = &started
		done := 0
		for _, s := range r.Steps {
			if s.State == models.StepDone || s.State == models.StepSkipped {
				done++
			}
		}
		if n := len(r.Steps); n > 0 {
			it.Pct = ptr(float64(done) / float64(n))
		}
		switch r.State {
		case models.RunRunning:
			it.State, it.Stage, it.StageLabel = StateActive, "running", "Making it"
			for _, s := range r.Steps {
				if s.State == models.StepRunning {
					it.Stage, it.StageLabel = s.ID, s.Label
					break
				}
			}
		case models.RunWaiting:
			it.State, it.Stage, it.StageLabel = StateWaiting, "waiting", r.Waiting
			if r.WaitingFor == models.RunWaitsForClaude {
				it.Stage = "claude"
			}
		case models.RunDone:
			it.State, it.Stage, it.StageLabel, it.Pct = StateDone, "done", "Ready for your review", ptr(1)
		case models.RunFailed:
			it.State, it.Stage, it.StageLabel, it.Problem, it.Pct = StateFailed, "failed", "Didn't finish", r.Problem, ptr(1)
		default:
			continue
		}
		b.out.Items = append(b.out.Items, it)
	}
}
