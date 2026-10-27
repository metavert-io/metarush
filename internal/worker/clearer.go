package worker

import (
	"context"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
)

// ClearerVoices says, for QA, which voices a plan plays made clearer and what
// that did, in plain words. internal/sound sets it (the worker cannot import
// it); nil: QA says nothing about it.
var ClearerVoices func(ctx context.Context, a *app.App, plan *editspec.RenderPlan) []string

func clearerVoices(ctx context.Context, a *app.App, plan *editspec.RenderPlan) []string {
	if ClearerVoices == nil || plan == nil {
		return nil
	}
	return ClearerVoices(ctx, a, plan)
}
