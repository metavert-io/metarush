package activity

import (
	"fmt"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
)

// importedAsset is the clip a finished link import made (or found).
func importedAsset(j models.Job) string {
	if id, _ := j.Result["assetId"].(string); id != "" {
		return id
	}
	return j.AssetID
}

// importItem is one link import: "Looking up the video", "Downloading" (with
// percent, rate and time left), "Adding to MetaRush", then the clip's own
// processing until it is ready. A failure says what happened and what to do
// next, and offers the browser sign-in when that may get through.
func (b *builder) importItem(j models.Job, byAsset map[string][]models.Job) {
	p := linkimport.PayloadOf(&j)
	site := models.PlatformName(p.Platform)
	title := p.Title
	if title == "" {
		title = linkimport.DefaultTitle(p.Platform, p.Who)
		if p.Kind == linkimport.KindPlaylist || p.Kind == linkimport.KindChannel {
			title = "Videos from " + site
		}
	}
	sub := b.where(j.ProjectID)
	if p.Platform != "" && p.Platform != models.PlatformWeb {
		sub += ", from " + site
	}
	it := Item{Key: "import:" + j.ID, Category: CatImport, Kind: "import", Title: title, Subtitle: sub, JobID: j.ID, ProjectID: j.ProjectID,
		URL: p.URL, Platform: p.Platform, Link: links.Job(j.ID), UpdatedAt: j.UpdatedAt}
	if n := len(j.Attempts); n > 0 {
		st := j.Attempts[n-1].StartedAt
		it.StartedAt = &st
	}
	switch j.State {
	case models.JobRunning:
		if j.Lease != nil {
			it.Machine = j.Lease.OwnerName
		}
		pr := j.Progress
		it.State, it.Stage = StateActive, pr.Stage
		it.StageLabel = linkimport.StageLabels[pr.Stage]
		if pr.Message != "" && pr.Stage != "" {
			it.StageLabel = pr.Message
		}
		sp := clamp01(pr.Pct)
		switch pr.Stage {
		case linkimport.StageDownload:
			it.Pct, it.StagePct = ptr(0.04+0.86*sp), ptr(sp)
			it.BytesDone, it.BytesTotal, it.Rate = pr.Done, pr.Total, pr.Rate
			if pr.Rate > 0 && pr.Total > pr.Done {
				it.ETASeconds = ptr(float64(pr.Total-pr.Done) / pr.Rate)
			}
		case linkimport.StageAdding:
			it.Pct, it.StagePct = ptr(0.9+0.1*sp), ptr(sp)
			it.BytesDone, it.BytesTotal = pr.Done, pr.Total
		default:
			it.Stage, it.StageLabel = linkimport.StageLookup, linkimport.StageLabels[linkimport.StageLookup]
			it.Pct = ptr(0.02)
		}
	case models.JobQueued:
		it.Stage, it.StageLabel = "queued", "Waiting to start"
		it.State = StateActive
		it.Waiting = jobs.Why(j, b.workers, b.now)
		if it.Waiting != nil && it.Waiting.Code != jobs.WaitStarting {
			it.State = StateWaiting
			if it.Waiting.Code == jobs.WaitRetry {
				it.StageLabel = "Trying again after a problem"
				it.CanProcessNow, it.ProcessJobIDs = true, []string{j.ID}
			}
		}
		if it.Waiting != nil {
			it.Machine = it.Waiting.Machine
		}
	case models.JobFailed:
		it.State, it.Stage, it.StageLabel, it.Error = StateFailed, "failed", "Couldn't import", firstLine(j.Error)
		if adv := linkimport.AdviceOf(&j); adv != nil {
			it.Problem, it.Hint, it.SignInHelps = adv.Message, adv.Next, adv.SignIn
			if adv.Retry || adv.SignIn {
				it.CanRetry, it.RetryJobIDs = true, []string{j.ID}
			}
		} else {
			it.CanRetry, it.RetryJobIDs = true, []string{j.ID}
			b.failed(&it, "The video couldn't be imported.", j.ID)
		}
		it.Pct = ptr(1)
	case models.JobSucceeded:
		status, _ := j.Result["status"].(string)
		if status == "expanded" {
			if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
				return
			}
			n := toInt(j.Result["queued"])
			it.State, it.Stage, it.Pct = StateDone, "done", ptr(1)
			it.StageLabel = fmt.Sprintf("Found %s; each one is imported on its own", plural(n, "video"))
			break
		}
		id := importedAsset(j)
		as := b.assets[id]
		it.AssetID = id
		if as != nil {
			it.Title, it.Name, it.Link = as.DisplayTitle(), as.Name, notify.AssetLink(as)
		}
		if status == "already" {
			if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
				return
			}
			it.State, it.Stage, it.StageLabel, it.Pct = StateDone, "duplicate", "Already in MetaRush", ptr(1)
			break
		}
		js := byAsset[id]
		last := lastChange(js)
		if j.FinishedAt != nil && j.FinishedAt.After(last) {
			last = *j.FinishedAt
		}
		if !b.applySteps(&it, b.steps(js), last) {
			if len(js) == 0 && j.FinishedAt != nil && b.now.Sub(*j.FinishedAt) <= b.recent {
				it.Stage, it.StageLabel, it.State, it.Pct = "ready", "Added", StateDone, ptr(1)
			} else {
				return
			}
		}
		if it.Pct != nil && it.State != StateDone {
			it.Pct = ptr(0.95 + 0.05**it.Pct)
		}
		if it.State == StateDone {
			it.Pct = ptr(1)
		}
	default:
		return // canceled
	}
	b.out.Items = append(b.out.Items, it)
}
