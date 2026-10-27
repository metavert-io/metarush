package activity

import (
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
)

// storageMoves lists moving the media to another storage (`metarush storage
// migrate`): it runs in the terminal that asked and reports files and bytes
// ("Copying 120 of 4,000 files").
func (b *builder) storageMoves(other []models.Job) {
	for _, j := range other {
		if j.Type != jobs.TypeStorageMove {
			continue
		}
		to, _ := j.Payload["storage"].(string)
		it := Item{Key: "job:" + j.ID, Category: CatStorage, Kind: "storage", Title: "Moving media to new storage", Subtitle: to, JobID: j.ID,
			Link: links.Job(j.ID), UpdatedAt: j.UpdatedAt}
		switch j.State {
		case models.JobRunning:
			p := j.Progress
			it.State, it.Stage, it.StageLabel, it.Pct = StateActive, "running", capFirst(orStr(p.Message, "Moving media")), ptr(clamp01(p.Pct))
			it.BytesDone, it.BytesTotal, it.Rate = p.Done, p.Total, p.Rate
			if p.Rate > 0 && p.Total > p.Done {
				it.ETASeconds = ptr(float64(p.Total-p.Done) / p.Rate)
			}
			if j.Lease != nil {
				it.Machine = j.Lease.OwnerName
			}
			if n := len(j.Attempts); n > 0 {
				st := j.Attempts[n-1].StartedAt
				it.StartedAt = &st
			}
		case models.JobFailed:
			it.State, it.Stage, it.StageLabel, it.Error = StateFailed, "failed", "Did not finish", firstLine(j.Error)
			b.failed(&it, "Moving the media didn't finish.", j.ID)
		case models.JobSucceeded:
			if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
				continue
			}
			it.State, it.Stage, it.StageLabel, it.Pct = StateDone, "done", "Moved", ptr(1)
		default:
			continue
		}
		b.out.Items = append(b.out.Items, it)
	}
}
