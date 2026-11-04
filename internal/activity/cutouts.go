package activity

import (
	"fmt"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
)

// cutOuts shows clips' cut-outs being made (D505): "Cutting the people out
// of “Kitchen chat”", with how far along, the computer and why it waits; a
// finished one stays a moment ("The cut-out of “Kitchen chat” is ready").
func (b *builder) cutOuts(other []models.Job) {
	latest := map[string]models.Job{}
	for _, j := range other {
		if j.Type != jobs.TypeMatte {
			continue
		}
		k := strOf(j.Payload["matteId"])
		if cur, ok := latest[k]; !ok || j.CreatedAt.After(cur.CreatedAt) {
			latest[k] = j
		}
	}
	for _, j := range latest {
		title, projectID := "the clip", j.ProjectID
		if as := b.assets[j.AssetID]; as != nil {
			title = as.DisplayTitle()
			if projectID == "" && len(as.ProjectIDs) > 0 {
				projectID = as.ProjectIDs[0]
			}
		}
		it := Item{Key: "job:" + j.ID, Category: CatProcessing, Kind: "cut-out", Title: fmt.Sprintf("Cutting the people out of “%s”", title),
			Subtitle: b.where(projectID), JobID: j.ID, AssetID: j.AssetID, ProjectID: projectID, Link: links.Asset(projectID, j.AssetID), UpdatedAt: j.UpdatedAt}
		switch j.State {
		case models.JobRunning:
			it.State, it.Stage, it.StageLabel, it.Pct = StateActive, "running", capFirst(orStr(j.Progress.Message, "Working")), ptr(clamp01(j.Progress.Pct))
			if j.Lease != nil {
				it.Machine = j.Lease.OwnerName
			}
			if n := len(j.Attempts); n > 0 {
				st := j.Attempts[n-1].StartedAt
				it.StartedAt = &st
				it.ETASeconds = eta(j.Progress.Pct, st, b.now)
			}
		case models.JobQueued:
			it.State, it.Stage, it.StageLabel = StateActive, "queued", "Waiting to start"
			it.Waiting = jobs.Why(j, b.workers, b.now)
			if it.Waiting != nil && it.Waiting.Code != jobs.WaitStarting {
				it.State, it.Stage, it.StageLabel = StateWaiting, "waiting", "Waiting"
				it.CanProcessNow, it.ProcessJobIDs = it.Waiting.CanProcessNow, []string{j.ID}
			}
			if it.Waiting != nil {
				it.Machine = it.Waiting.Machine
			}
		case models.JobFailed:
			it.State, it.Stage, it.StageLabel, it.Error = StateFailed, "failed", "The cut-out couldn't be made", firstLine(j.Error)
			it.CanRetry, it.RetryJobIDs = true, []string{j.ID}
		case models.JobSucceeded:
			if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
				continue
			}
			it.State, it.Stage, it.Pct = StateDone, "done", ptr(1)
			it.Title = fmt.Sprintf("The cut-out of “%s” is ready", title)
			it.StageLabel = "Cuts can put something else behind the people now"
		default:
			continue
		}
		b.out.Items = append(b.out.Items, it)
	}
}
