package voiceover

import (
	"context"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// PriceLines prices AI voice for narration lines that no saved cut has yet
// (an estimate made before anything is saved, D204): the same voices, live
// models, cache and budget checks as voiceover.estimate. Lines that already
// have an AI voice or a recording are left out, as there; beats picks lines
// (IDs or 1-based numbers), none means every line that needs a voice.
func PriceLines(ctx context.Context, a *app.App, projectID string, beats []editspec.Beat, in TTSIn) (*EstimateOut, error) {
	rc := &revCtx{rev: &models.Revision{ProjectID: projectID}, spec: &editspec.Spec{Voiceover: &editspec.Voiceover{Beats: beats}}}
	_, est, _, err := planTTS(ctx, a, rc, &in)
	return est, err
}
