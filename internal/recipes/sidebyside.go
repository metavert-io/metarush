package recipes

import (
	"context"
	"fmt"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Two runs side by side (D212): recipe.compare gives each run's review copy
// to play next to the other's on the recipe's page (`?compare=<run>&with=<run>`),
// above what changed between them.

// RecipeCopyView is a run's review copy, to play next to another's.
type RecipeCopyView struct {
	RenderID string `json:"renderId"`
	// Run is the run's number; Version the version of the cut it is of.
	Run     int `json:"run"`
	Version int `json:"version,omitempty"`
	// Ready: it can play now; Words says where it stands ("ready, 12 s",
	// "still being made").
	Ready bool   `json:"ready"`
	State string `json:"state" jsonschema:"description=The render's state (queued\\, rendering\\, uploading\\, available\\, failed\\, canceled)."`
	Words string `json:"words"`
	// VideoURL and ThumbURL are the dashboard's addresses of the video and its still.
	VideoURL string  `json:"videoUrl,omitempty"`
	ThumbURL string  `json:"thumbUrl,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
}

// reviewCopies are two runs' review copies (nil for a run without one, or
// whose project was cleared out).
func reviewCopies(ctx context.Context, a *app.App, l, r *models.RecipeRun) [2]*RecipeCopyView {
	return [2]*RecipeCopyView{reviewCopy(ctx, a, l), reviewCopy(ctx, a, r)}
}

func reviewCopy(ctx context.Context, a *app.App, run *models.RecipeRun) *RecipeCopyView {
	if run.Render == "" || run.ProjectGone {
		return nil
	}
	var rd models.Render
	if a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), run.Render, &rd) != nil {
		return nil
	}
	v := &RecipeCopyView{RenderID: rd.ID, Run: run.Number, Version: rd.RevisionNumber, State: rd.State}
	switch rd.State {
	case models.RenderAvailable:
		v.Ready = true
		v.VideoURL, v.ThumbURL = "/media/renders/"+rd.ID, "/media/renders/"+rd.ID+"/thumbnail"
		v.Words = "ready"
		if o := rd.Output; o != nil {
			v.Duration, v.Width, v.Height = o.Duration, o.Width, o.Height
			if o.Duration > 0 {
				v.Words = fmt.Sprintf("ready, %s", seconds(o.Duration))
			}
		}
	case models.RenderFailed:
		v.Words = "its review copy didn't finish"
	case models.RenderCanceled:
		v.Words = "its review copy was stopped"
	default:
		v.Words = "its review copy is still being made"
	}
	return v
}
