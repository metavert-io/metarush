package mattes

import (
	"context"
	"fmt"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// ReadyCheck names the check that this computer makes cut-outs
// (ops.Needs.Setup of the abilities that use them).
const ReadyCheck = "cut-outs-video"

// CutoutsIn lists a clip's cut-outs.
type CutoutsIn struct {
	AssetID string `json:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=The clip."`
}

// CutoutsOut is a clip's cut-outs, newest first.
type CutoutsOut struct {
	Cutouts []CutoutView `json:"cutouts"`
	// Here are the models this computer makes cut-outs with now.
	Here    []string `json:"here"`
	Summary string   `json:"summary"`
}

func init() {
	ops.Def("footage.cutout", "Cut the people out of a clip, or a part of it, for background removal in video: the people told from their background frame by frame and kept with the clip's picture, made once on a computer of the workspace that can (free; nothing leaves it) and used by every cut that replaces the clip's background", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CutoutIn) (*CutoutOut, error) { return Make(ctx, a, in) })
	ops.Def("footage.cutouts", "List a clip's cut-outs for background removal in video: the part each covers, how far along it is, the model and the computer that made it", ops.Flags{},
		func(ctx context.Context, a *app.App, in *CutoutsIn) (*CutoutsOut, error) {
			return list(ctx, a, in.AssetID)
		})
	ops.Def("footage.cutout.preview", "Show one moment of a clip with its background replaced by a colour, a blur of the clip, a picture or another clip, as a still on this computer (nothing is kept)", ops.Flags{LocalOnly: true},
		func(ctx context.Context, a *app.App, in *CutoutPreviewIn) (*CutoutPreviewOut, error) {
			return PreviewFile(ctx, a, in)
		})
	ops.RegisterReady(ReadyCheck, func(ctx context.Context, a *app.App) (bool, string) {
		if len(LocalModels(a)) > 0 {
			return true, ""
		}
		return false, "Apple Vision on a Mac, or “Replacing a video's background” added in Settings → Components"
	})
}

func list(ctx context.Context, a *app.App, assetID string) (*CutoutsOut, error) {
	as, err := clipFor(ctx, a, assetID)
	if err != nil {
		return nil, err
	}
	docs, err := OfClip(ctx, a, as.ID)
	if err != nil {
		return nil, err
	}
	out := &CutoutsOut{Cutouts: []CutoutView{}, Here: LocalModels(a)}
	if out.Here == nil {
		out.Here = []string{}
	}
	ready := 0
	for i := range docs {
		job := Refresh(ctx, a, &docs[i])
		out.Cutouts = append(out.Cutouts, ViewOf(&docs[i], as.DisplayTitle(), as.Media.Duration, job))
		if docs[i].State == models.MatteReady {
			ready++
		}
	}
	switch {
	case len(docs) == 0:
		out.Summary = fmt.Sprintf("“%s” has no cut-out yet.", as.DisplayTitle())
	case len(docs) == 1:
		out.Summary = fmt.Sprintf("“%s” has one cut-out: %s", as.DisplayTitle(), out.Cutouts[0].Words)
	default:
		out.Summary = fmt.Sprintf("“%s” has %d cut-outs, %d ready.", as.DisplayTitle(), len(docs), ready)
	}
	return out, nil
}
