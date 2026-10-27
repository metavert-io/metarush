package activity

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
)

// Generated video (D125): one row per generation job — "Sending it to Google
// Gemini", "Google Gemini is making it" (how long so far), "Downloading the
// video", "Adding it to MetaRush", then the clip's own preparation until it
// is ready. A failure or a refusal says what happened and what to do next.

// generatedAsset is the clip a finished generation job made.
func generatedAsset(j models.Job) string {
	if id, _ := j.Result["assetId"].(string); id != "" {
		return id
	}
	return ""
}

var genStageLabels = map[string]string{"sending": "Sending it to Google Gemini", "making": "Google Gemini is making it",
	"downloading": "Downloading the video", "adding": "Adding it to MetaRush"}

func (b *builder) generations(ctx context.Context, gens []models.Job, byAsset map[string][]models.Job) {
	if len(gens) == 0 {
		return
	}
	idsIn := bson.A{}
	for _, j := range gens {
		if id, _ := j.Payload["generationId"].(string); id != "" {
			idsIn = append(idsIn, id)
		}
	}
	recs := map[string]models.Generation{}
	var gs []models.Generation
	_ = b.a.DB.Find(ctx, store.CGenerations, b.a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idsIn}}, &gs)
	for _, g := range gs {
		recs[g.ID] = g
	}
	for _, j := range gens {
		id, _ := j.Payload["generationId"].(string)
		g, ok := recs[id]
		if !ok {
			continue
		}
		b.generationItem(j, g, byAsset)
	}
}

// aiWhat says what other kinds of AI work make (D226).
var aiWhat = map[string]string{"picture": "A picture", "voice": "AI speech", "voice-copy": "A voice copy", "voice-change": "A recording in another voice",
	"sound-effect": "A sound effect", "music": "A piece of music", "dubbing": "A dubbed copy", "transcription": "A transcript in the cloud"}

func genWhat(g models.Generation) string {
	if w := aiWhat[g.Kind]; w != "" {
		return w
	}
	switch g.Operation {
	case models.GenExtend:
		return fmt.Sprintf("Continuing a clip by %d seconds", g.Params.Seconds)
	case models.GenEdit:
		return "Changing a clip"
	case models.GenLookTest:
		return "A test shot of a look"
	}
	// "An 8-second shot", "A 6-second shot": the article follows how the number is said.
	t := capFirst(fmt.Sprintf("%s %d-second shot", plainwords.AOrAn(g.Params.Seconds), g.Params.Seconds))
	if g.Take > 1 {
		t += fmt.Sprintf(" (take %d)", g.Take)
	}
	return t
}

func (b *builder) generationItem(j models.Job, g models.Generation, byAsset map[string][]models.Job) {
	who := firstNonEmptyStr(g.ProviderTitle, "the video service")
	title := genWhat(g)
	if p := strings.TrimSpace(g.Prompt); p != "" {
		title += ": " + clip(p, 70)
	}
	sub := b.where(g.ProjectID) + ", made with " + who + ", marked as AI-generated"
	if g.Kind == "transcription" {
		sub = b.where(g.ProjectID) + ", written down by " + who
	}
	it := Item{Key: "generate:" + j.ID, Category: CatGenerate, Kind: "generate", Title: title, Subtitle: sub,
		JobID: j.ID, ProjectID: g.ProjectID, Link: links.Job(j.ID), UpdatedAt: j.UpdatedAt}
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
		it.State, it.Stage = StateActive, firstNonEmptyStr(pr.Stage, "sending")
		it.StageLabel = firstNonEmptyStr(pr.Message, genStageLabels[it.Stage], "Working")
		if it.Stage == "making" && g.Ticket != nil {
			if d := b.now.Sub(g.Ticket.SubmittedAt); d > 20*time.Second {
				it.StageLabel += fmt.Sprintf(" (%s so far)", minutesWords(d))
			}
		}
		it.Pct = ptr(clamp01(pr.Pct))
	case models.JobQueued:
		it.Stage, it.StageLabel, it.State = "queued", "Waiting to start", StateActive
		it.Waiting = jobs.Why(j, b.workers, b.now)
		if it.Waiting != nil && it.Waiting.Code != jobs.WaitStarting {
			it.State = StateWaiting
			if it.Waiting.Code == jobs.WaitRetry {
				it.StageLabel = "Waiting before it asks Google again"
				if j.Progress.Message != "" {
					it.StageLabel = j.Progress.Message
				}
			}
			it.Machine = it.Waiting.Machine
		}
	case models.JobFailed:
		it.State, it.Stage, it.StageLabel, it.Error, it.Pct = StateFailed, "failed", "Couldn't make it", firstLine(j.Error), ptr(1)
		if g.Outcome != nil {
			if g.Outcome.State == models.GenRefused {
				it.StageLabel = who + " declined it"
			}
			it.Problem, it.Hint = g.Outcome.Message, g.Outcome.Hint
		} else {
			b.failed(&it, "The video couldn't be made.", j.ID)
		}
	case models.JobCanceled:
		if j.FinishedAt == nil || b.now.Sub(*j.FinishedAt) > b.recent {
			return
		}
		it.State, it.Stage, it.StageLabel, it.Pct = StateDone, "done", "Stopped", ptr(1)
	case models.JobSucceeded:
		id := generatedAsset(j)
		as := b.assets[id]
		it.AssetID = id
		if as != nil {
			it.Name, it.Link = as.Name, notify.AssetLink(as)
		}
		js := byAsset[id]
		last := lastChange(js)
		if j.FinishedAt != nil && j.FinishedAt.After(last) {
			last = *j.FinishedAt
		}
		if !b.applySteps(&it, b.steps(js), last) {
			if j.FinishedAt != nil && b.now.Sub(*j.FinishedAt) <= b.recent {
				it.Stage, it.StageLabel, it.State, it.Pct = "ready", readyWhere(g.Kind), StateDone, ptr(1)
			} else {
				return
			}
		}
		if it.State == StateDone {
			it.Pct = ptr(1)
			it.StageLabel = readyWhere(g.Kind)
		}
	default:
		return
	}
	b.out.Items = append(b.out.Items, it)
}

// readyWhere says where finished AI work is.
func readyWhere(kind string) string {
	switch kind {
	case "picture":
		return "Ready in the resources library"
	case "transcription":
		return "The clip's words are being replaced"
	case "voice-copy":
		return "Ready with the narration voices"
	}
	return "Ready in Sources"
}

func minutesWords(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < 2*time.Minute:
		return "about a minute"
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}

func firstNonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
