package calls

import (
	"context"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
)

// JobType finds a footage's switches (D160): ffmpeg only, any machine.
const JobType = jobs.TypeSwitches

// Queue asks for a footage's switches: once per evidence version and on-screen
// index version (so the index arriving later refines them again with its
// candidates). It returns the job and whether it was new.
func Queue(ctx context.Context, a *app.App, as *models.Asset, indexVersion string, actor models.Actor) (string, bool, error) {
	key := JobType + ":" + as.ID + ":" + evidence.Version + ":" + indexVersion
	j, created, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: JobType, IdempotencyKey: key, Priority: 4, AssetID: as.ID,
		Payload: map[string]any{"assetId": as.ID}, MaxAttempts: 2, Actor: actor})
	if err != nil {
		return "", false, err
	}
	return j.ID, created, nil
}
