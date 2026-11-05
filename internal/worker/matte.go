package worker

import (
	"context"
	"errors"
	"runtime"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/mattes"
	"github.com/metavert-io/metarush/internal/matting"
	"github.com/metavert-io/metarush/internal/models"
)

// matteHandler makes a clip's cut-out (D505) with a model this computer has:
// a cut-out asked of a model another computer has waits for it, one no
// computer can make ends plainly with the component that adds it.
func matteHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	res, err := mattes.Run(ctx, w.App, j, func(pct float64, msg string) { report(pct, msg) })
	var wait *mattes.Wait
	var final *mattes.Final
	switch {
	case errors.As(err, &wait):
		return nil, &postponed{after: wait.After, reason: wait.Why}
	case errors.As(err, &final):
		return nil, &Permanent{Err: final.Err}
	}
	return res, err
}

// mattes are the models this computer makes cut-outs with now (published
// on its record, so work asked of a model waits for a computer that has it).
func (w *Worker) mattes() []string {
	if !w.Can(jobs.TypeMatte) {
		return nil
	}
	return mattes.LocalModels(w.App)
}

// matteComponent is the component that adds cut-outs to this computer: the
// on-screen helper on a Mac, MODNet elsewhere.
func matteComponent() string {
	if runtime.GOOS == "darwin" {
		return "screen-index"
	}
	return matting.Component
}
