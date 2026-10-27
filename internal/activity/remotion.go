package activity

import (
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
)

// remotionWork shows a Remotion source's refresh (its own steps, D173) and a
// push of its changes back (D176) in plain words: what it does, the step it
// is on, how it ended.
func (b *builder) remotionWork(other []models.Job) {
	for _, j := range other {
		if j.Type != jobs.TypeRemotionRefresh && j.Type != jobs.TypeRemotionPush && j.Type != jobs.TypeAbilityRun {
			continue
		}
		kind, fallback := "remotion-refresh", "Refreshing a Remotion project from its own steps"
		source, _ := j.Payload["source"].(string)
		link := links.Remotion(j.ProjectID, source, "")
		switch j.Type {
		case jobs.TypeRemotionPush:
			kind, fallback = "remotion-push", "Sending a Remotion project's changes back"
		case jobs.TypeAbilityRun:
			// A run of an ability (D234), pinned to the computer that asked.
			kind, fallback = "ability-run", "Running an ability"
			link = links.AbilityRun(strOf(j.Payload["ability"]), strOf(j.Payload["run"]))
		}
		title := orStr(strOf(j.Payload["title"]), fallback)
		it := Item{Key: "job:" + j.ID, Category: CatProcessing, Kind: kind, Title: title, Subtitle: b.where(j.ProjectID), JobID: j.ID, ProjectID: j.ProjectID,
			Link: link, UpdatedAt: j.UpdatedAt}
		switch j.State {
		case models.JobRunning:
			it.State, it.Stage, it.StageLabel, it.Pct = StateActive, "running", capFirst(orStr(j.Progress.Message, "Starting")), ptr(clamp01(j.Progress.Pct))
			if j.Lease != nil {
				it.Machine = j.Lease.OwnerName
			}
			if n := len(j.Attempts); n > 0 {
				st := j.Attempts[n-1].StartedAt
				it.StartedAt = &st
			}
		case models.JobQueued:
			it.State, it.Stage, it.StageLabel = StateActive, "queued", "Waiting to start"
			it.Waiting = jobs.Why(j, b.workers, b.now)
			if it.Waiting != nil && it.Waiting.Code != jobs.WaitStarting {
				it.State, it.Stage, it.StageLabel = StateWaiting, "waiting", "Waiting"
			}
			if it.Waiting != nil {
				it.Machine = it.Waiting.Machine
			}
		case models.JobFailed, models.JobCanceled:
			if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
				continue
			}
			it.State, it.Stage, it.StageLabel, it.Error = StateFailed, "failed", "Didn't finish", firstLine(j.Error)
			// Never retried from here: a person looks at the plan and starts it again.
			b.failed(&it, firstLine(orStr(j.Error, "It didn't finish.")), j.ID)
		case models.JobSucceeded:
			if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
				continue
			}
			it.State, it.Stage, it.Pct = StateDone, "done", ptr(1)
			it.StageLabel = capFirst(orStr(strOf(j.Result["message"]), "Done"))
			if v := strOf(j.Result["version"]); v != "" && j.Type != jobs.TypeAbilityRun {
				it.Link = links.Remotion(j.ProjectID, source, v)
			}
		default:
			continue
		}
		b.out.Items = append(b.out.Items, it)
	}
}
