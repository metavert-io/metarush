package linkimport

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
)

// keepPartial is how long a failed import's partial download is kept for a retry.
const keepPartial = 7 * 24 * time.Hour

// Sweep removes download folders nothing will use again: imports that finished
// or were canceled, ones that are gone, and failed ones older than a week.
// Imports still queued or running keep theirs (a retry continues the download).
func Sweep(ctx context.Context, a *app.App) int {
	root := filepath.Join(a.Cfg.Scratch.Dir, "imports")
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() || !ids.Valid(ids.Job, e.Name()) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info, _ := e.Info()
		old := info != nil && time.Since(info.ModTime()) > keepPartial
		j, err := a.Queue.Get(ctx, e.Name())
		remove := false
		switch {
		case err != nil:
			remove = ctx.Err() == nil
		case j.State == models.JobSucceeded || j.State == models.JobCanceled:
			remove = true
		case j.State == models.JobFailed && old:
			remove = true
		}
		if remove && os.RemoveAll(dir) == nil {
			n++
		}
	}
	return n
}

// RunSweep sweeps when the daemon starts and once a day.
func RunSweep(ctx context.Context, a *app.App) {
	for {
		Sweep(ctx, a)
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
