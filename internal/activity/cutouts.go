package activity

import (
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
)

// cutOuts shows clips' cut-outs being made (D505): "Cutting the people out
// of “Kitchen chat”", with how far along, the computer and why it waits — one
// put back for another computer (room for it, the model) says so in its own
// words, as generated video's rows say why they wait, never "it ran into a
// problem"; a finished one stays a moment ("The cut-out of “Kitchen chat” is
// ready").
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
				if why := putBackFor(j); why != "" && it.Waiting.Code == jobs.WaitRetry {
					w := *it.Waiting
					w.Text = why + ". " + putBackText + " " + jobs.PlainIn(j.NextRunAt.Sub(b.now)) + "."
					it.Waiting, it.StageLabel = &w, "Waiting for another computer"
				}
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

// putBackText ends the wait line of work put back for another computer ("…;
// this one hasn't. It looks again in about 4 minutes."): its waiting group says
// it waits for another computer, not that it ran into a problem.
const putBackText = "It looks again"

// putBackFor is why a cut-out's job was put back for another computer — its
// last try was postponed, with the reason as its progress (mattes.Wait):
// "Waiting for a computer of this workspace with room to make the cut-out;
// this one hasn't" — or "" when it wasn't (a try that failed waits to try
// again after a problem).
func putBackFor(j models.Job) string {
	if jobs.Postponed(&j) == 0 {
		return ""
	}
	return capFirst(strings.TrimSuffix(strings.TrimSpace(j.Progress.Message), "."))
}
