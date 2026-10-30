package generate

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/videogen"
)

// A generation job (D125) runs where it was asked for (the machine with the
// key) and goes through steps that each happen once, recorded on the
// generation before the next one starts:
//
//  1. send the request — after noting that a send began, so a send cut off
//     by a crash is never repeated blindly (it may have reached the provider);
//  2. save the provider's ticket — before waiting, so a restart resumes;
//  3. wait, download at once, check the file (ffprobe, SHA-256) and add it
//     through ingest.File as an ordinary clip with its provenance;
//  4. write the outcome, settle the money, delete the request and its
//     inputs at the provider, and tell the person.

// Progress stages (models.Progress.Stage) of a generation job.
const (
	StageSending     = "sending"
	StageMaking      = "making"
	StageDownloading = "downloading"
	StageAdding      = "adding"
)

// StageLabels are the stages as people read them.
var StageLabels = map[string]string{StageSending: "Sending it to Google Gemini", StageMaking: "Google Gemini is making it",
	StageDownloading: "Downloading the video", StageAdding: "Adding it to MetaRush"}

// Wait puts the job back in the queue for a while: it is not a failure.
type Wait struct {
	After  time.Duration
	Reason string
}

func (w *Wait) Error() string { return w.Reason }

// Final is a failure that trying again can't fix.
type Final struct{ Err error }

func (f *Final) Error() string { return f.Err.Error() }
func (f *Final) Unwrap() error { return f.Err }

// PollEvery is how long a job waits between checks on a running request.
var PollEvery = 5 * time.Second

// FirstPoll is how long it waits before the first check.
var FirstPoll = 2 * time.Second

// Checks the provider answers only with "too many requests" (D178): after
// PollLimitWait of nothing but 429s (of any kind) the job goes back in the
// queue for PollLimitPause — a wait, not a failure — and after PollLimitPauses
// such waits in a row it stops, asks the provider to stop the request and says
// so. A 429 that says the project can't make video at all (the free tier,
// videogen.KindTier) ends the request at once. Sending keeps its own waits
// (wait429).
var (
	PollLimitWait  = 10 * time.Minute
	PollLimitPause = 10 * time.Minute
)

// PollLimitPauses is how many waits in a row a job takes for 429s before it gives up.
const PollLimitPauses = 2

type runner struct {
	a      *app.App
	g      *models.Generation
	job    *models.Job
	p      videogen.Provider
	report func(models.Progress)
}

func (r *runner) say(stage string, pct float64) {
	if r.report != nil {
		label := StageLabels[stage]
		if r.g.ProviderTitle != "" && r.g.ProviderTitle != videogen.GeminiTitle {
			label = strings.ReplaceAll(label, videogen.GeminiTitle, r.g.ProviderTitle)
		}
		r.report(models.Progress{Stage: stage, Message: label, Pct: pct})
	}
}

// Run does one generation job.
func Run(ctx context.Context, a *app.App, j *models.Job, report func(models.Progress)) (map[string]any, error) {
	id, _ := j.Payload["generationId"].(string)
	g, err := loadGen(ctx, a, id)
	if err != nil {
		return nil, &Final{Err: err}
	}
	p, err := videogen.Get(g.Provider)
	if err != nil {
		return nil, &Final{Err: err}
	}
	// The person who asked is the one adding the clip (notifications, job details).
	ga := *a
	ga.Actor = g.CreatedBy
	if ga.Actor.UserID == "" {
		ga.Actor = a.Actor
	}
	r := &runner{a: &ga, g: g, job: j, p: p, report: report}
	return r.run(ctx)
}

func (r *runner) run(ctx context.Context) (map[string]any, error) {
	g := r.g
	if g.Outcome == nil {
		key, err := keyFor(r.a, g.Provider)
		if err != nil {
			r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.NotConfigured, Message: providerTitle(g) + " isn't set up on this computer any more, so the video couldn't be made.",
				Hint: "Add the key again in Settings, Integrations, then generate it again."}, nil)
			return r.finish(ctx, "")
		}
		if g.Ticket == nil {
			if err := r.submit(ctx, key); err != nil {
				return nil, err
			}
		}
		if g.Outcome == nil {
			if err := r.wait(ctx, key); err != nil {
				return nil, err
			}
		}
		return r.finish(ctx, key)
	}
	key, _ := keyFor(r.a, g.Provider)
	return r.finish(ctx, key)
}

// submit sends the request once and saves the ticket.
func (r *runner) submit(ctx context.Context, key string) error {
	g := r.g
	if len(g.Sending) > len(g.Sends) {
		// A send began and nothing was recorded after it: MetaRush stopped
		// while sending. It may have reached the provider, so it isn't sent
		// again (that could pay twice); the money counts as spent.
		r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider,
			Message: "MetaRush stopped while it was sending this to " + providerTitle(g) + ", so it can't tell whether the video was started. To be safe it wasn't sent again.",
			Hint:    "Generate it again if you still want it. The estimate counts as spent, in case it was made."}, &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"})
		return nil
	}
	// What allowed it when it was asked for may have changed while it waited:
	// a project's Content Settings, a consent (D225, D126). Nothing is sent then.
	if stop := stillAllowed(ctx, r.a, g); stop != nil {
		if Unreadable(stop) {
			return stop // a record couldn't be read just now: tried again, never taken as a refusal
		}
		ae := apperr.From(stop)
		r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: ae.Code, Message: ae.Message + " Nothing was sent.", Hint: ae.Hint}, &models.GenCost{Basis: "none"})
		return nil
	}
	r.say(StageSending, 0.02)
	req, cleanup, err := r.request(ctx)
	if err != nil {
		return err
	}
	defer cleanup()
	now := time.Now().UTC()
	if err := appendTo(ctx, r.a, g.ID, "sending", now); err != nil {
		return err
	}
	g.Sending = append(g.Sending, now)
	t, err := r.p.Submit(ctx, key, req)
	if err != nil {
		var ve *videogen.Error
		if !errors.As(err, &ve) {
			// Not an answer from the provider (a file, the context): nothing was sent.
			_ = r.sent(ctx, models.GenSend{At: time.Now().UTC(), Result: "failed", Detail: firstLine(err.Error())})
			if errors.Is(err, context.Canceled) {
				return err
			}
			return err
		}
		send := models.GenSend{At: time.Now().UTC(), Result: "rejected", Code: ve.Kind, Detail: firstLine(ve.Detail)}
		if ve.Kind == videogen.KindUncertain {
			send.Result = "failed"
		}
		if err := r.sent(ctx, send); err != nil {
			return err
		}
		switch ve.Kind {
		case videogen.KindUncertain:
			r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider, Message: ve.Message + " To be safe it wasn't sent again.",
				Hint: "Generate it again if you still want it. The estimate counts as spent, in case it was made.", Detail: ve.Detail}, &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"})
			return nil
		case videogen.KindTier:
			return r.noVideo(ctx, key, nil, ve) // refused when sent: nothing at the provider to stop
		case videogen.KindSafety:
			r.end(ctx, &models.GenOutcome{State: models.GenRefused, Code: apperr.SafetyBlocked, Message: ve.Message, Hint: ve.Hint, Detail: ve.Detail, Safety: safetyModel(ve.Safety)}, &models.GenCost{Basis: "none"})
			return nil
		case videogen.KindRate, videogen.KindSpend:
			return r.wait429(ctx, ve)
		case videogen.KindServer, videogen.KindNetwork:
			return err // retried with a backoff
		}
		code := ve.AppError().Code
		r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: code, Message: ve.Message, Hint: ve.Hint, Detail: ve.Detail}, &models.GenCost{Basis: "none"})
		return nil
	}
	tk := models.GenTicket{ID: t.ID, Files: t.Files, SubmittedAt: t.SubmittedAt}
	// The ticket first: a crash after this resumes waiting instead of sending again.
	if err := setOnce(context.WithoutCancel(ctx), r.a, g.ID, "ticket", tk); err != nil {
		return err
	}
	g.Ticket = &tk
	_ = r.sent(ctx, models.GenSend{At: time.Now().UTC(), Result: "accepted"})
	r.say(StageMaking, 0.05)
	return nil
}

func (r *runner) sent(ctx context.Context, s models.GenSend) error {
	if err := appendTo(context.WithoutCancel(ctx), r.a, r.g.ID, "sends", s); err != nil {
		return err
	}
	r.g.Sends = append(r.g.Sends, s)
	return nil
}

// wait429 postpones the job after a rate or spending limit, at most a few times.
func (r *runner) wait429(ctx context.Context, ve *videogen.Error) error {
	n := 0
	for _, s := range r.g.Sends {
		if s.Code == videogen.KindRate || s.Code == videogen.KindSpend {
			n++
		}
	}
	if n > 6 {
		r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider, Message: ve.Message + " MetaRush tried several times and stopped.", Hint: ve.Hint, Detail: ve.Detail}, &models.GenCost{Basis: "none"})
		return nil
	}
	after := ve.RetryAfter
	if after <= 0 {
		after = time.Minute
	}
	reason := "Google is busy with this key's requests; MetaRush tries again in a minute."
	if ve.Kind == videogen.KindSpend {
		reason = "Google's spending limit for this key was reached (it counts 10 minutes at a time); MetaRush tries again in 10 minutes."
	}
	return &Wait{After: after, Reason: reason}
}

// request materializes the request: the exact bytes of every picture and clip.
func (r *runner) request(ctx context.Context) (*videogen.Request, func(), error) {
	g := r.g
	req := &videogen.Request{Model: g.Model, Task: g.Params.Task, Prompt: firstNonEmpty(g.SentPrompt, g.Prompt), Seconds: g.Params.Seconds, AspectRatio: g.Params.AspectRatio, Resolution: g.Params.Resolution}
	for _, ref := range g.References {
		path, err := local(ctx, r.a, ref.StorageKey, ref.SHA256, ref.Size)
		if err != nil {
			return nil, func() {}, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get a picture for this shot from its storage.")
		}
		m := videogen.Media{Role: ref.Role, MIME: ref.MIME, Path: path, SHA256: ref.SHA256, Size: ref.Size}
		if strings.HasPrefix(ref.MIME, "video/") {
			m.Seconds = math.Max(0.1, ref.To-ref.From)
			req.Videos = append(req.Videos, m)
		} else {
			req.Images = append(req.Images, m)
		}
	}
	return req, func() {}, nil
}

// wait polls until the request ends, then downloads and adds the result.
func (r *runner) wait(ctx context.Context, key string) error {
	g := r.g
	t := &videogen.Ticket{ID: g.Ticket.ID, Files: g.Ticket.Files, SubmittedAt: g.Ticket.SubmittedAt}
	delay := FirstPoll
	if time.Since(g.Ticket.SubmittedAt) > FirstPoll {
		delay = 0
	}
	errorsInARow := 0
	var limitedSince time.Time // the first of the 429s since the provider last answered properly
	for {
		select {
		case <-ctx.Done():
			return r.stopped(ctx, key, t)
		case <-time.After(delay):
		}
		delay = PollEvery
		st, err := r.p.Poll(ctx, key, t)
		if err != nil {
			if ctx.Err() != nil {
				return r.stopped(ctx, key, t)
			}
			var ve *videogen.Error
			if errors.As(err, &ve) {
				switch ve.Kind {
				case videogen.KindTier:
					// The project can't make video at all (the free tier): no wait helps (D178).
					return r.noVideo(ctx, key, t, ve)
				case videogen.KindExpired, videogen.KindKey, videogen.KindPermission:
					r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: ve.AppError().Code, Message: ve.Message, Hint: ve.Hint, Detail: ve.Detail}, &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"})
					return nil
				case videogen.KindRate, videogen.KindSpend:
					if limitedSince.IsZero() {
						limitedSince = time.Now()
					}
					waited := time.Since(limitedSince)
					if waited >= PollLimitWait {
						return r.limited(ctx, key, t, ve)
					}
					r.sayLimited()
					if ve.RetryAfter > delay {
						delay = ve.RetryAfter
					}
					if left := PollLimitWait - waited; delay > left {
						delay = left // ask once more when the time is up, then decide
					}
					continue
				}
			}
			errorsInARow++
			if errorsInARow >= 5 {
				return &Wait{After: 2 * time.Minute, Reason: "MetaRush couldn't reach Google for a while; it checks on the video again in 2 minutes."}
			}
			continue
		}
		errorsInARow, limitedSince = 0, time.Time{}
		switch st.State {
		case videogen.StateRunning:
			made := time.Since(g.Ticket.SubmittedAt).Seconds()
			r.say(StageMaking, math.Min(0.8, 0.05+made/180*0.75))
			continue
		case videogen.StateCanceled:
			r.end(ctx, &models.GenOutcome{State: models.GenCanceled, Message: "It was stopped at " + providerTitle(g) + "."}, r.cost(st.Usage, "none"))
			return nil
		case videogen.StateFailed:
			e := st.Err
			if e == nil {
				e = &videogen.Error{Kind: videogen.KindServer, Message: providerTitle(g) + " couldn't make this video."}
			}
			if e.Kind == videogen.KindTier {
				return r.noVideo(ctx, key, nil, e) // it failed at the provider already: nothing to stop
			}
			if e.Kind == videogen.KindSafety {
				r.end(ctx, &models.GenOutcome{State: models.GenRefused, Code: apperr.SafetyBlocked, Message: e.Message,
					Hint:   "Change the prompt or the pictures and try again. Google doesn't make videos of certain recognizable people, and MetaRush never sends a child's likeness.",
					Detail: e.Detail, Safety: safetyModel(st.Safety)}, r.cost(st.Usage, "none"))
				return nil
			}
			r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider, Message: e.Message, Hint: "Generate it again; if it keeps failing, change the prompt.", Detail: e.Detail}, r.cost(st.Usage, "none"))
			return nil
		}
		return r.collect(ctx, key, t, st)
	}
}

// stopped handles the job's context ending: a person asked to stop it
// (cancel at the provider), or this machine is stopping (resume later).
func (r *runner) stopped(ctx context.Context, key string, t *videogen.Ticket) error {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	j, err := r.a.Queue.Get(bg, r.job.ID)
	if err == nil && j.CancelRequested {
		_ = r.p.Cancel(bg, key, t)
		st, _ := r.p.Poll(bg, key, t)
		usage := videogen.Usage{}
		if st != nil {
			usage = st.Usage
		}
		r.end(bg, &models.GenOutcome{State: models.GenCanceled, Message: "It was stopped."}, r.cost(usage, "none"))
		_, _ = r.finish(bg, key)
	}
	return ctx.Err()
}

// noVideo ends a request the provider will never make for this key's project
// (videogen.KindTier: Gemini's free tier, D178): when it had been accepted (t)
// the provider is asked to stop it; nothing was made or charged ($0, basis
// "none"), and finish lets go of the money held for it, deletes what the
// provider kept and tells the person what to do.
func (r *runner) noVideo(ctx context.Context, key string, t *videogen.Ticket, ve *videogen.Error) error {
	r.cancelAt(ctx, key, t)
	r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: ve.AppError().Code, Message: ve.Message, Hint: ve.Hint, Detail: ve.Detail}, &models.GenCost{Basis: "none"})
	return nil
}

// limited is the end of PollLimitWait of nothing but "too many requests"
// while checking on a request (D178): the job waits in the queue for
// PollLimitPause (not a failure, the attempt doesn't count), and after
// PollLimitPauses such waits in a row MetaRush gives up: it asks the provider
// to stop the request and says so; the estimate counts, in case it was made.
func (r *runner) limited(ctx context.Context, key string, t *videogen.Ticket, ve *videogen.Error) error {
	who := providerTitle(r.g)
	if r.job == nil || jobs.Postponed(r.job) < PollLimitPauses {
		after := PollLimitPause
		if ve.RetryAfter > after {
			after = min(ve.RetryAfter, time.Hour)
		}
		return &Wait{After: after, Reason: fmt.Sprintf("%s has been too busy with this key's requests to say how the video is going for %s; MetaRush asks again in %s.",
			who, minutesWords(PollLimitWait), minutesWords(after))}
	}
	r.cancelAt(ctx, key, t)
	hint := "Generate it again later. The estimate counts as spent, in case it was made."
	if r.g.Provider == videogen.GeminiName {
		hint += " Google AI Studio shows this key's limits: " + videogen.TierLink + "."
	}
	r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider,
		Message: who + " kept turning away MetaRush's checks on this video (too many requests from this key), so MetaRush stopped waiting and asked " + who + " to stop making it.",
		Hint:    hint, Detail: ve.Detail}, &models.GenCost{USD: r.g.Estimate.USD, Basis: "assumed"})
	return nil
}

// sayLimited says why there is no news while the provider answers checks with
// "too many requests" (the activity bar and job details read it).
func (r *runner) sayLimited() {
	if r.report == nil || r.g.Ticket == nil {
		return
	}
	made := time.Since(r.g.Ticket.SubmittedAt).Seconds()
	r.report(models.Progress{Stage: StageMaking, Message: providerTitle(r.g) + " is too busy with this key's requests to say how it's going",
		Pct: math.Min(0.8, 0.05+made/180*0.75)})
}

// cancelAt asks the provider to stop a request it accepted (best effort: it may turn that away too).
func (r *runner) cancelAt(ctx context.Context, key string, t *videogen.Ticket) {
	if t == nil || t.ID == "" || key == "" {
		return
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_ = r.p.Cancel(bg, key, t)
}

// minutesWords says a wait the way people do: "a minute", "10 minutes".
func minutesWords(d time.Duration) string {
	if d < 90*time.Second {
		return "a minute"
	}
	return fmt.Sprintf("%d minutes", int(d.Round(time.Minute).Minutes()))
}

// cost is what the provider reported, priced with the generation's table.
func (r *runner) cost(u videogen.Usage, fallback string) *models.GenCost {
	g := r.g
	if !u.Known {
		if fallback == "assumed" {
			return &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"}
		}
		return &models.GenCost{Basis: fallback}
	}
	pt, err := videogen.PriceAt(g.Provider, g.Model, g.CreatedAt)
	if err != nil {
		return &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"}
	}
	c := pt.Actual(u)
	return &models.GenCost{USD: c.USD, VideoTokens: c.VideoTokens, InputTokens: c.InputTokens, ThinkingTokens: c.ThinkingTokens, TextTokens: c.TextTokens, Basis: "usage"}
}

// collect downloads each video at once, checks it and adds it as a clip.
func (r *runner) collect(ctx context.Context, key string, t *videogen.Ticket, st *videogen.Status) error {
	g := r.g
	var outs []models.GenOutput
	for i := range st.Outputs {
		o := st.Outputs[i]
		r.say(StageDownloading, 0.82)
		path, done, err := scratch(r.a, fmt.Sprintf("generated-%d.mp4", i+1))
		if err != nil {
			return err
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			done()
			return err
		}
		n, err := r.p.Fetch(ctx, key, t, &o, f)
		_ = f.Close()
		if err != nil {
			done()
			var ve *videogen.Error
			if errors.As(err, &ve) && ve.Kind == videogen.KindExpired {
				r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider, Message: ve.Message, Hint: ve.Hint, Detail: ve.Detail}, r.cost(st.Usage, "assumed"))
				return nil
			}
			return err // retried: the provider keeps the result until MetaRush deletes it
		}
		r.say(StageAdding, 0.9)
		out, err := r.add(ctx, path, n, &o, i, len(st.Outputs))
		done()
		if err != nil {
			return err
		}
		outs = append(outs, *out)
	}
	oc := &models.GenOutcome{State: models.GenDone, Outputs: outs, Partial: st.Partial}
	if len(outs) > 0 && outs[0].Duration < float64(g.Params.Seconds)-0.75 {
		oc.Partial = true
	}
	if oc.Partial {
		oc.Message = fmt.Sprintf("%s sent back %s instead of the %d seconds asked for.", providerTitle(g), secondsWords(outs[0].Duration), g.Params.Seconds)
	}
	r.end(ctx, oc, r.cost(st.Usage, "assumed"))
	return nil
}

// add checks a downloaded video (it must be one, with a picture) and adds it
// through the ordinary pipeline, with where it came from.
func (r *runner) add(ctx context.Context, path string, size int64, o *videogen.Output, i, n int) (*models.GenOutput, error) {
	g := r.g
	mi, err := probe.Probe(ctx, r.a.Tools, path)
	if err != nil || !mi.HasVideo || mi.Duration < 0.3 {
		return nil, &Final{Err: apperr.New(apperr.Provider, "%s sent something that isn't a video MetaRush can read.", providerTitle(g))}
	}
	sha, err := media.HashFile(path)
	if err != nil {
		return nil, err
	}
	title := r.title(i, n)
	src := &models.AssetSource{Platform: models.PlatformGenerated, Title: clip(g.Prompt, 200), Duration: mi.Duration, DownloadedAt: time.Now().UTC(),
		Tool: providerTitle(g), ToolVersion: g.Model, Method: models.SourceGenerated,
		Generated: &models.GeneratedSource{GenerationID: g.ID, Provider: g.Provider, ProviderTitle: providerTitle(g), Model: g.Model, ModelTitle: g.ModelTitle,
			Operation: g.Operation, Take: g.Take, Prompt: clip(g.Prompt, 300), Likeness: len(g.ConsentIDs) > 0}}
	if c, err := videogen.ModelInfo(g.Provider, g.Model); err == nil {
		src.Generated.Watermark = c.Watermark
	}
	if g.Look != nil {
		src.Generated.Look = g.Look.Title
	}
	via := g.CreatedBy.Via
	if via == "" {
		via = "cli"
	}
	res := ingest.File(ctx, r.a, path, ingest.Options{ProjectID: g.ProjectID, DisplayName: fileName(title), Title: title, Source: src, JobID: r.job.ID, Via: via,
		Tags: []string{models.TagAIGenerated}, Transcribe: true, FootageOnly: true, Duplicates: ingest.DuplicatesKeep})
	switch res.Status {
	case "failed", "skipped":
		return nil, errors.New("adding the generated video didn't work: " + res.Error)
	}
	id := res.AssetID
	if id == "" && res.Duplicate != nil {
		id = res.Duplicate.AssetID
	}
	if g.Operation == models.GenLookTest {
		// A look's test shots stay out of the Inbox: they are on the look's test sheet.
		_ = r.a.DB.Update(ctx, store.CAssets, r.a.WorkspaceID(), id, bson.M{"$set": bson.M{"archivedAt": time.Now().UTC(), "archivedBy": r.a.Actor}})
	}
	return &models.GenOutput{AssetID: id, SHA256: sha, Size: size, Duration: mi.Duration, Width: mi.Width, Height: mi.Height, FPS: mi.FPS, HasAudio: mi.HasAudio,
		MIME: firstNonEmpty(o.MIME, "video/mp4"), Delivery: o.Delivery(), ProviderFileID: o.FileID, Status: res.Status}, nil
}

func (r *runner) title(i, n int) string {
	g := r.g
	t := clip(g.Prompt, 60)
	switch g.Operation {
	case models.GenExtend, models.GenEdit:
		if g.Template != nil && g.Template.Vars["title"] != "" {
			t = g.Template.Vars["title"]
		}
	}
	if g.Take > 1 {
		t += fmt.Sprintf(" (take %d)", g.Take)
	}
	if n > 1 {
		t += fmt.Sprintf(" (%d of %d)", i+1, n)
	}
	return t
}

var fileUnsafe = strings.NewReplacer("/", "-", "\\", "-", ":", "-", "\x00", "", "\n", " ", "\r", " ")

func fileName(title string) string {
	n := strings.TrimSpace(fileUnsafe.Replace(title))
	if r := []rune(n); len(r) > 80 {
		n = string(r[:80])
	}
	if n == "" {
		n = "generated"
	}
	return n + ".mp4"
}

// end writes the outcome once (and the actual cost), at the database
// server's time (D360): other computers compare it.
func (r *runner) end(ctx context.Context, oc *models.GenOutcome, actual *models.GenCost) {
	oc.At = r.a.DB.NowOrLocal(ctx)
	if actual != nil && actual.Basis != "" {
		oc.Actual = actual
	}
	bg := context.WithoutCancel(ctx)
	if err := setOnce(bg, r.a, r.g.ID, "outcome", oc); err == nil {
		if fresh, err := loadGen(bg, r.a, r.g.ID); err == nil {
			*r.g = *fresh
			return
		}
	}
	r.g.Outcome = oc
}

// finish settles the money, deletes everything kept at the provider and tells
// the person — each once — then reports the job's result.
func (r *runner) finish(ctx context.Context, key string) (map[string]any, error) {
	g := r.g
	bg := context.WithoutCancel(ctx)
	oc := g.Outcome
	if oc == nil {
		return nil, errors.New("the generation has no outcome yet")
	}
	if g.Settled == nil {
		usd, note := 0.0, ""
		if oc.Actual != nil {
			usd = oc.Actual.USD
			if oc.Actual.Basis == "assumed" {
				note = "The provider didn't say what it charged, so the estimate counts."
			}
		}
		if err := Settle(bg, r.a, g, usd, note); err != nil {
			return nil, err
		}
	}
	if g.Forgotten == nil && g.Ticket != nil && key != "" {
		var outs []videogen.Output
		for _, o := range oc.Outputs {
			if o.ProviderFileID != "" {
				outs = append(outs, videogen.Output{FileID: o.ProviderFileID})
			}
		}
		t := &videogen.Ticket{ID: g.Ticket.ID, Files: g.Ticket.Files}
		f := models.GenForgotten{At: time.Now().UTC(), OK: true}
		if err := r.p.Forget(bg, key, t, outs); err != nil {
			f.OK, f.Detail = false, firstLine(err.Error())
		}
		if err := setOnce(bg, r.a, g.ID, "forgotten", f); err == nil {
			g.Forgotten = &f
		}
	}
	r.notify(bg)
	res := map[string]any{"generationId": g.ID, "state": oc.State, "costUsd": costOf(g)}
	var assets []string
	for _, o := range oc.Outputs {
		assets = append(assets, o.AssetID)
	}
	if len(assets) > 0 {
		res["assetId"], res["assetIds"] = assets[0], assets
	}
	if oc.State != models.GenDone {
		// The job's own record says why in plain words (activity, job details).
		_ = r.a.DB.Update(bg, store.CJobs, r.a.WorkspaceID(), r.job.ID, bson.M{"$set": bson.M{"payload.code": oc.Code, "payload.advice": strings.TrimSpace(oc.Message + " " + oc.Hint)}})
		if oc.State == models.GenCanceled {
			return res, nil
		}
		return nil, &Final{Err: apperr.New(firstNonEmpty(oc.Code, apperr.Provider), "%s", firstNonEmpty(oc.Message, "The video couldn't be made."))}
	}
	return res, nil
}

// notify tells the person who asked how it went (once per generation).
func (r *runner) notify(ctx context.Context) {
	g := r.g
	oc := g.Outcome
	if oc == nil || g.CreatedBy.UserID == "" {
		return
	}
	n := models.Notification{UserID: g.CreatedBy.UserID, GroupKey: "generation:" + g.ID, JobID: g.JobID, ProjectID: g.ProjectID,
		Details: map[string]any{"generationId": g.ID, "costUsd": costOf(g), "state": oc.State}}
	thing := what(g)
	switch oc.State {
	case models.GenDone:
		n.Kind, n.Tone = models.NotifyGenerationReady, "success"
		n.Title = upperFirst(thing) + " is ready"
		if len(oc.Outputs) > 0 {
			n.AssetID = oc.Outputs[0].AssetID
			n.Link = links.Asset(g.ProjectID, n.AssetID)
		}
		n.Body = fmt.Sprintf("Made with %s for %s. It's in Sources, marked as AI-generated.", providerTitle(g), money(costOf(g)))
		if g.Operation == models.GenLookTest && g.Look != nil {
			n.Title = fmt.Sprintf("A test shot of the look “%s” is ready", g.Look.Title)
			n.Body = fmt.Sprintf("Made with %s for %s. It's on the look's test sheet: check it before approving the look.", providerTitle(g), money(costOf(g)))
			n.Link = lookLink(g.Look)
		}
		if oc.Partial {
			n.Body = oc.Message + " " + n.Body
		}
	case models.GenCanceled:
		return
	default:
		n.Kind, n.Tone = models.NotifyGenerationFailed, "error"
		n.Title = "Couldn't make " + thing
		if oc.State == models.GenRefused {
			n.Title = providerTitle(g) + " declined " + thing
		}
		n.Body = strings.TrimSpace(oc.Message + " " + oc.Hint)
		n.Link = "/?activity=open"
		switch {
		case g.ProjectID != "":
			n.Link = "/p/" + g.ProjectID + "?tab=sources&generate=1"
		case g.Look != nil:
			n.Link = lookLink(g.Look)
		}
	}
	_, _ = notify.Upsert(ctx, r.a, n, notify.Once)
}

// lookLink is where a look is shown: its person on the People page, or the Looks page.
func lookLink(l *models.GenLookRef) string {
	if l.Subject == models.LookPerson && l.PersonID != "" {
		return "/people?person=" + l.PersonID
	}
	return "/settings/integrations#looks"
}

func safetyModel(s *videogen.Safety) *models.GenSafety {
	if s == nil {
		return nil
	}
	return &models.GenSafety{Blocked: s.Blocked, Side: s.Side, FilteredCount: s.FilteredCount, SupportCodes: s.SupportCodes, Category: s.Category, Message: s.Message}
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 600 {
		s = s[:600] + "…"
	}
	return s
}

// Sweep closes generations whose job ended without an outcome (stopped while
// still waiting to start, or given up on) and lets go of money held for
// requests that were never saved. The daemon runs it every few minutes. A
// generation's age is the database server's (D360): every computer sweeps.
func Sweep(ctx context.Context, a *app.App) int {
	var gs []models.Generation
	_ = a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"outcome": bson.M{"$exists": false}, "$and": bson.A{store.StaleBefore("createdAt", time.Minute)},
		"kind": bson.M{"$in": models.VideoKinds}}, &gs)
	n := 0
	for i := range gs {
		g := &gs[i]
		j, err := a.Queue.Get(ctx, g.JobID)
		if err != nil || (j.State != models.JobCanceled && j.State != models.JobFailed) {
			continue
		}
		r := &runner{a: a, g: g, job: j}
		if p, err := videogen.Get(g.Provider); err == nil {
			r.p = p
		}
		switch {
		case j.State == models.JobCanceled && g.Ticket == nil:
			r.end(ctx, &models.GenOutcome{State: models.GenCanceled, Message: "It was stopped before it was sent. Nothing was spent."}, &models.GenCost{Basis: "none"})
		case j.State == models.JobCanceled:
			r.end(ctx, &models.GenOutcome{State: models.GenCanceled, Message: "It was stopped."}, &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"})
		default:
			r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider, Message: "MetaRush gave up on this video: " + firstLine(j.Error)}, &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"})
		}
		_ = Settle(ctx, a, r.g, costOf(r.g), "")
		n++
	}
	if k, err := ReleaseOrphans(ctx, a); err == nil {
		n += k
	}
	return n
}

// RunSweep sweeps when the daemon starts and every five minutes.
func RunSweep(ctx context.Context, a *app.App) {
	for {
		Sweep(ctx, a)
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}
