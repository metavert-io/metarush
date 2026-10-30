package aiwork

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/videogen"
)

// An AI work job (D226) runs where it was asked for (the machine with the
// key), through the steps of generated video, each recorded on the
// generation before the next one starts:
//
//  1. send the request, after noting that a send began, so a send cut off by
//     a crash is never repeated blindly (it may have reached the provider);
//  2. save the provider's ticket before waiting (a one-shot call's ticket
//     too: it names what to delete at the provider);
//  3. wait when the provider works a while (dubbing), then download at once
//     and keep each result with how it was made;
//  4. write the outcome, settle the money, delete what the provider kept, and
//     tell the person.

// Progress stages of an AI work job.
const (
	StageSending     = generate.StageSending
	StageMaking      = generate.StageMaking
	StageDownloading = generate.StageDownloading
	StageAdding      = generate.StageAdding
)

// Handles reports whether a generate job is AI work of another kind than video.
func Handles(j *models.Job) bool {
	k, _ := j.Payload["kind"].(string)
	return k != "" && k != models.GenKindVideo
}

type runner struct {
	a      *app.App
	g      *models.Generation
	job    *models.Job
	ad     aiservices.Adapter
	kind   aiservices.KindInfo
	report func(models.Progress)
}

func (r *runner) say(stage, label string, pct float64) {
	if r.report != nil {
		r.report(models.Progress{Stage: stage, Message: label, Pct: pct})
	}
}

// Run does one AI work job.
func Run(ctx context.Context, a *app.App, j *models.Job, report func(models.Progress)) (map[string]any, error) {
	if action, _ := j.Payload["action"].(string); action == ActionRemovePresenter {
		return runRemovePresenter(ctx, a, j) // deleting a retired presenter (D277)
	}
	id, _ := j.Payload["generationId"].(string)
	g, err := loadGen(ctx, a, id)
	if err != nil {
		return nil, &generate.Final{Err: err}
	}
	if action, _ := j.Payload["action"].(string); action == ActionRemoveVoice {
		return runRemoveVoice(ctx, a, g) // deleting a withdrawn voice copy (D227)
	}
	aiservices.Refresh(ctx, a) // abilities that provide a kind of work (D281)
	ad, err := aiservices.Get(g.Provider, g.Kind)
	if err != nil {
		return nil, &generate.Final{Err: err}
	}
	k, _ := aiservices.KindOf(g.Kind)
	// The person who asked is the one adding the result (notifications, job details).
	ga := *a
	ga.Actor = g.CreatedBy
	if ga.Actor.UserID == "" {
		ga.Actor = a.Actor
	}
	r := &runner{a: &ga, g: g, job: j, ad: ad, kind: k, report: report}
	return r.run(ctx)
}

func (r *runner) run(ctx context.Context) (map[string]any, error) {
	g := r.g
	// A model on this computer is handed the computer for the call (D278).
	ctx, done, err := withEnv(ctx, r.a, g.Provider)
	if err != nil {
		return nil, err
	}
	defer done()
	key, keyErr := keyFor(r.a, g.Provider)
	if g.Outcome == nil {
		if keyErr != nil {
			r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.NotConfigured, Message: g.ProviderTitle + " isn't set up on this computer any more, so it couldn't be made.",
				Hint: "Add the key again in Settings → Integrations, then ask again."}, &models.GenCost{Basis: "none"})
			return r.finish(ctx, "")
		}
		var ready *aiservices.Status
		if g.Ticket == nil {
			st, err := r.submit(ctx, key)
			if err != nil {
				return nil, err
			}
			ready = st
		}
		if ready != nil || ((g.Kind == aiservices.KindVoiceCopy || g.Kind == aiservices.KindPresenterMake) && len(g.Sending) > 0) {
			// The answer is here (and paid for), or a voice copy was sent: it is
			// kept and dealt with whatever happens to the job now, so a stop asked
			// for meanwhile never loses it (D226, D227).
			ctx = context.WithoutCancel(ctx)
		}
		if g.Outcome == nil {
			if err := r.wait(ctx, key, ready); err != nil {
				return nil, err
			}
		}
	}
	if keyErr != nil {
		key = ""
	}
	return r.finish(ctx, key)
}

// request rebuilds the request from the record: the exact bytes of every file.
func (r *runner) request(ctx context.Context) (*aiservices.Request, error) {
	g := r.g
	req := &aiservices.Request{Kind: g.Kind, Task: g.Params.Task, Model: g.Model, Prompt: firstNonEmpty(g.SentPrompt, g.Prompt)}
	switch g.Kind {
	case aiservices.KindVoiceCopy, aiservices.KindPresenterMake, aiservices.KindPresenter:
		req.Tag = voiceTag(g) // in its name at the provider, to find it if the answer is lost
	}
	if byAbility(g) {
		req.Tag = g.ID // the ability's run names the work it did (D281)
	}
	var linkRoles []string
	if lt, ok := r.ad.(aiservices.LinkTaker); ok {
		linkRoles = lt.LinkRoles()
	}
	if g.Options != nil {
		req.Options = *g.Options
	}
	for i, ref := range g.References {
		path, err := generate.Local(ctx, r.a, ref.StorageKey, ref.SHA256, ref.Size)
		if err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get a file for this request from its storage.")
		}
		m := aiservices.Media{Role: ref.Role, MIME: ref.MIME, Path: path, SHA256: ref.SHA256, Size: ref.Size,
			Name: fmt.Sprintf("%s-%d%s", ref.Role, i+1, aiservices.ExtFor(ref.MIME))}
		if ref.AssetID != "" {
			m.Seconds = math.Max(0.1, ref.To-ref.From)
		}
		if slices.Contains(linkRoles, ref.Role) {
			// The provider downloads it itself: a short-lived link to the same bytes (D273).
			if m.URL, err = linkFor(ctx, r.a, r.ad, ref, path); err != nil {
				return nil, &generate.Final{Err: err}
			}
		}
		req.Inputs = append(req.Inputs, m)
	}
	return req, nil
}

// submit sends the request once and saves the ticket; a one-shot call's
// result comes back with it.
func (r *runner) submit(ctx context.Context, key string) (*aiservices.Status, error) {
	g := r.g
	if len(g.Sending) > len(g.Sends) && !onThisComputer(g) {
		// A send began and nothing was recorded after it: it may have reached
		// the provider, so it isn't sent again (that could pay twice). Work on
		// this computer reached no one and costs nothing: it simply runs again.
		r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider,
			Message: "MetaRush stopped while it was sending this to " + g.ProviderTitle + ", so it can't tell whether it was made. To be safe it wasn't sent again." + r.lostVoiceWords(),
			Hint:    "Ask again if you still want it. The estimate counts as spent, in case it was made."}, &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"})
		r.lost(ctx, key)
		return nil, nil
	}
	// What allowed it when it was asked for may have changed while it waited:
	// a project's Content Settings, a consent, a presenter, a training opt-out
	// (D225, D227, D276, D277). Nothing is sent then.
	if stop := r.stillAllowed(ctx); stop != nil {
		if generate.Unreadable(stop) {
			return nil, stop // a record couldn't be read just now: tried again, never taken as a refusal
		}
		ae := apperr.From(stop)
		r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: ae.Code, Message: ae.Message + " Nothing was sent.", Hint: ae.Hint}, &models.GenCost{Basis: "none"})
		return nil, nil
	}
	if onThisComputer(g) {
		r.say(StageMaking, "Making it on this computer", 0.05)
	} else {
		r.say(StageSending, "Sending it to "+g.ProviderTitle, 0.02)
	}
	req, err := r.request(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := generate.AppendTo(context.WithoutCancel(ctx), r.a, g.ID, "sending", now); err != nil {
		return nil, err
	}
	g.Sending = append(g.Sending, now)
	if err := ctx.Err(); err != nil {
		// Stopped before anything left this computer: it may be sent later.
		_ = r.sent(ctx, models.GenSend{At: time.Now().UTC(), Result: "failed", Detail: "stopped before it was sent"})
		return nil, err
	}
	// Once it is on its way, a stop asked for meanwhile (a person, a consent
	// withdrawn, this computer stopping) doesn't cut the request off: its
	// answer, which may be paid for or leave something at the provider, is
	// waited for (each adapter bounds the call) and dealt with.
	t, err := r.ad.Submit(context.WithoutCancel(ctx), key, req)
	if err != nil {
		var ve *videogen.Error
		if !errors.As(err, &ve) {
			_ = r.sent(ctx, models.GenSend{At: time.Now().UTC(), Result: "failed", Detail: firstLine(err.Error())})
			return nil, err
		}
		send := models.GenSend{At: time.Now().UTC(), Result: "rejected", Code: ve.Kind, Detail: firstLine(ve.Detail)}
		switch ve.Kind {
		case videogen.KindUncertain:
			send.Result, send.Files = "failed", ve.Files
		case videogen.KindUnusable:
			send.Result = "accepted" // it answered; the answer was of no use
		}
		if err := r.sent(ctx, send); err != nil {
			return nil, err
		}
		switch ve.Kind {
		case videogen.KindUncertain:
			r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider, Message: ve.Message + " To be safe it wasn't sent again." + r.lostVoiceWords(),
				Hint: "Ask again if you still want it. The estimate counts as spent, in case it was made.", Detail: ve.Detail}, &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"})
			r.lost(ctx, key)
		case videogen.KindSafety:
			r.end(ctx, &models.GenOutcome{State: models.GenRefused, Code: apperr.SafetyBlocked, Message: ve.Message, Hint: firstNonEmpty(ve.Hint, "Change the words or the files and try again."),
				Detail: ve.Detail, Safety: safetyModel(ve.Safety)}, &models.GenCost{Basis: "none"})
		case videogen.KindUnusable:
			// The provider answered, so it may have charged: never sent again (D226).
			r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider, Message: ve.Message,
				Hint:   firstNonEmpty(ve.Hint, "Ask again if you still want it. The estimate counts as spent, in case it was charged."),
				Detail: ve.Detail}, &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"})
		case videogen.KindRate, videogen.KindSpend:
			return nil, r.wait429(ctx, ve)
		case videogen.KindServer, videogen.KindNetwork:
			return nil, err // no answer was used: retried with a backoff
		default:
			r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: ve.AppError().Code, Message: ve.Message, Hint: ve.Hint, Detail: ve.Detail}, &models.GenCost{Basis: "none"})
		}
		return nil, nil
	}
	tk := models.GenTicket{ID: t.ID, Files: t.Files, Extra: t.Extra, SubmittedAt: t.SubmittedAt}
	if tk.SubmittedAt.IsZero() {
		tk.SubmittedAt = time.Now().UTC()
	}
	// The ticket first: a crash after this resumes waiting instead of sending again.
	if err := generate.SetOnce(context.WithoutCancel(ctx), r.a, g.ID, "ticket", tk); err != nil {
		return nil, err
	}
	g.Ticket = &tk
	_ = r.sent(ctx, models.GenSend{At: time.Now().UTC(), Result: "accepted"})
	return t.Ready, nil
}

// stillAllowed checks again, just before sending, what the plan checked:
// the Content Settings of every project the work touches — the services and
// the kinds of work they allow (D225, D273) — the consents it relies on and
// the voice copy it speaks with (D227), the presenter it makes or reads with
// (D277) and the training opt-out of a service that trains on what it is
// sent (D276).
func (r *runner) stillAllowed(ctx context.Context) error {
	g := r.g
	// An ability approved again (or taken back) since the plan: the version
	// it planned with is the one asked for, and only it runs (D281).
	if v := aiservices.VersionOf(r.ad, g.Model); v != g.ModelVersion {
		return apperr.New(apperr.Conflict, "“%s” changed after this was asked for (another version of it was approved, or that one was taken back), so it wasn't run.", firstNonEmpty(g.ModelTitle, g.ProviderTitle)).
			WithHint("Ask again to see what the version it has now would do.")
	}
	touched := generate.TouchedBy(ctx, r.a, g.ProjectID, g.References)
	if err := generate.RulesAllow(ctx, r.a, g.Provider, touched); err != nil {
		return err
	}
	if err := kindsAllow(ctx, r.a, g.Kind, touched); err != nil {
		return err
	}
	if aiservices.NothingLeaves(g.Provider) {
		// Nothing leaves this computer (D278, D281): no What may leave rule applies.
	} else if err := leave.CheckGeneration(ctx, r.a, g, touched); err != nil { // D290
		return err
	}
	if err := generate.ConsentsHold(ctx, r.a, g); err != nil {
		return err
	}
	if err := r.presenterHolds(ctx); err != nil {
		return err
	}
	if err := r.trainingHolds(ctx); err != nil {
		return err
	}
	// Lip sync: every face in the part named, looked at every quarter second (D279).
	if err := lipFacesNamed(ctx, r.a, g); err != nil {
		return err
	}
	if g.Options != nil && strings.TrimSpace(g.Options.Voice) != "" {
		var vc models.Generation
		err := r.a.DB.C(store.CGenerations).FindOne(ctx, bson.M{"workspaceId": r.a.WorkspaceID(), "kind": aiservices.KindVoiceCopy, "outcome.outputs.voiceId": g.Options.Voice}).Decode(&vc)
		if err == nil && vc.Withdrawn != nil {
			return apperr.New(apperr.ConsentRequired, "The voice “%s” was withdrawn, so it can't be used any more.", firstNonEmpty(voiceName(&vc), g.Options.Voice))
		}
	}
	return nil
}

// lost: what was asked for may have been made although its answer was lost.
// A voice copy is withdrawn, looked up at the provider by the tag in its name
// and deleted — here when this computer can, else by the deletion job (D227);
// a presenter is retired and deleted the same way; a presenter clip is looked
// up by its tag and deleted with the files the request uploaded (D277).
func (r *runner) lost(ctx context.Context, key string) {
	switch r.g.Kind {
	case aiservices.KindPresenterMake:
		_ = r.presenterMade(context.WithoutCancel(ctx)) // again in finish, which tries again on a failed read
	case aiservices.KindVoiceCopy:
		_, _ = withdraw(context.WithoutCancel(ctx), r.a, r.g, "No answer came back when it was made, so whatever was made is deleted.")
	case aiservices.KindPresenter:
		// finish looks for the clip by its tag, again for a while (lostClip)
	}
}

func (r *runner) lostVoiceWords() string {
	switch r.g.Kind {
	case aiservices.KindVoiceCopy:
		return " MetaRush looks for the voice at " + r.g.ProviderTitle + " and deletes it if it was made."
	case aiservices.KindPresenterMake:
		return " MetaRush looks for the presenter at " + r.g.ProviderTitle + " and deletes it if it was made."
	case aiservices.KindPresenter:
		return " MetaRush looks for the clip at " + r.g.ProviderTitle + " and deletes it if it was made."
	}
	return ""
}

func (r *runner) sent(ctx context.Context, s models.GenSend) error {
	if err := generate.AppendTo(context.WithoutCancel(ctx), r.a, r.g.ID, "sends", s); err != nil {
		return err
	}
	r.g.Sends = append(r.g.Sends, s)
	return nil
}

// wait429 postpones the job after a rate or spending limit, a few times.
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
	return &generate.Wait{After: after, Reason: r.g.ProviderTitle + " is busy with this key's requests; MetaRush tries again in a minute."}
}

func (r *runner) ticket() *aiservices.Ticket {
	g := r.g
	if g.Ticket == nil {
		return nil
	}
	return &aiservices.Ticket{ID: g.Ticket.ID, Files: g.Ticket.Files, Extra: g.Ticket.Extra, SubmittedAt: g.Ticket.SubmittedAt}
}

// wait polls until the request ends (a one-shot call has ended already),
// then downloads and keeps the results.
func (r *runner) wait(ctx context.Context, key string, ready *aiservices.Status) error {
	g := r.g
	t := r.ticket()
	if t == nil {
		return nil
	}
	if ready != nil {
		return r.ended(ctx, key, t, ready)
	}
	delay := generate.FirstPoll
	if time.Since(t.SubmittedAt) > delay {
		delay = 0
	}
	errorsInARow := 0
	for {
		select {
		case <-ctx.Done():
			return r.stopped(ctx, key, t)
		case <-time.After(delay):
		}
		delay = generate.PollEvery
		st, err := r.ad.Poll(ctx, key, t)
		if err != nil {
			if ctx.Err() != nil {
				return r.stopped(ctx, key, t)
			}
			var ve *videogen.Error
			if errors.As(err, &ve) {
				switch ve.Kind {
				case videogen.KindExpired, videogen.KindKey, videogen.KindPermission, videogen.KindNotFound:
					r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: ve.AppError().Code, Message: ve.Message + r.lostVoiceWords(), Hint: ve.Hint, Detail: ve.Detail},
						&models.GenCost{USD: g.Estimate.USD, Basis: "assumed"})
					// A voice copy's one-shot answer isn't kept anywhere to ask about
					// again (MetaRush stopped after saving its ticket): the voice may be
					// at the provider, so it is looked up by its tag and deleted (D227).
					r.lost(ctx, key)
					return nil
				case videogen.KindRate, videogen.KindSpend:
					if ve.RetryAfter > delay {
						delay = ve.RetryAfter
					}
					continue
				}
			}
			errorsInARow++
			if errorsInARow >= 5 {
				return &generate.Wait{After: 2 * time.Minute, Reason: "MetaRush couldn't reach " + g.ProviderTitle + " for a while; it checks again in 2 minutes."}
			}
			continue
		}
		errorsInARow = 0
		if st.State == aiservices.StateRunning {
			pct := 0.05 + math.Min(0.75, time.Since(t.SubmittedAt).Seconds()/600*0.75)
			if st.Progress > 0 {
				pct = 0.05 + st.Progress*0.75
			}
			r.say(StageMaking, g.ProviderTitle+" is making it", pct)
			continue
		}
		return r.ended(ctx, key, t, st)
	}
}

// ended handles a finished request: kept results, a refusal, a failure.
func (r *runner) ended(ctx context.Context, key string, t *aiservices.Ticket, st *aiservices.Status) error {
	g := r.g
	switch st.State {
	case aiservices.StateCanceled:
		r.end(ctx, &models.GenOutcome{State: models.GenCanceled, Message: "It was stopped at " + g.ProviderTitle + "."}, r.cost(st.Usage, "none"))
		return nil
	case aiservices.StateFailed:
		e := st.Err
		if e == nil {
			e = &videogen.Error{Kind: videogen.KindServer, Message: g.ProviderTitle + " couldn't make it."}
		}
		if e.Kind == videogen.KindSafety {
			r.end(ctx, &models.GenOutcome{State: models.GenRefused, Code: apperr.SafetyBlocked, Message: e.Message, Hint: "Change the words or the files and try again.",
				Detail: e.Detail, Safety: safetyModel(firstSafety(st.Safety, e.Safety))}, r.cost(st.Usage, "none"))
			return nil
		}
		r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider, Message: e.Message, Hint: firstNonEmpty(e.Hint, "Ask again; if it keeps failing, change the words or the files."),
			Detail: e.Detail}, r.cost(st.Usage, "none"))
		return nil
	}
	if len(st.Outputs) == 0 {
		r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider, Message: g.ProviderTitle + " finished without sending anything back.", Detail: firstLine(st.Text)},
			r.cost(st.Usage, "assumed"))
		return nil
	}
	// A consent it relies on withdrawn, or the presenter reading it retired,
	// while it was being made: what came back isn't kept, and is deleted at
	// the provider (finish; D227, D277). A voice copy or a presenter is kept
	// long enough to be withdrawn and deleted by its ID (finish).
	if g.Kind != aiservices.KindVoiceCopy && g.Kind != aiservices.KindPresenterMake {
		err := generate.ConsentsHold(context.WithoutCancel(ctx), r.a, g)
		if err == nil {
			err = r.presenterHolds(context.WithoutCancel(ctx))
		}
		if err != nil && generate.Unreadable(err) {
			return err // checked again when the job is tried again; nothing is thrown away on a read that failed
		}
		if err != nil {
			ae := apperr.From(err)
			r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: ae.Code, Message: ae.Message + " What came back wasn't kept.", Hint: ae.Hint}, r.cost(st.Usage, "assumed"))
			return nil
		}
	}
	var outs []models.GenOutput
	for i := range st.Outputs {
		o := st.Outputs[i]
		out, err := r.keep(ctx, key, t, &o, i, len(st.Outputs))
		if err != nil {
			var ve *videogen.Error
			if errors.As(err, &ve) && ve.Kind == videogen.KindExpired {
				r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider, Message: ve.Message, Hint: ve.Hint, Detail: ve.Detail}, r.cost(st.Usage, "assumed"))
				return nil
			}
			var fe *generate.Final
			if errors.As(err, &fe) {
				r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.From(fe.Err).Code, Message: apperr.From(fe.Err).Message}, r.cost(st.Usage, "assumed"))
				return nil
			}
			return err
		}
		outs = append(outs, *out)
	}
	r.end(ctx, &models.GenOutcome{State: models.GenDone, Outputs: outs}, r.cost(st.Usage, "assumed"))
	return nil
}

func firstSafety(ss ...*videogen.Safety) *videogen.Safety {
	for _, s := range ss {
		if s != nil {
			return s
		}
	}
	return nil
}

// stopped handles the job's context ending: a person asked to stop it
// (cancel at the provider), or this machine is stopping (resume later).
func (r *runner) stopped(ctx context.Context, key string, t *aiservices.Ticket) error {
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	j, err := r.a.Queue.Get(bg, r.job.ID)
	if err == nil && j.CancelRequested {
		_ = r.ad.Cancel(bg, key, t)
		r.end(bg, &models.GenOutcome{State: models.GenCanceled, Message: "It was stopped."}, &models.GenCost{USD: r.g.Estimate.USD, Basis: "assumed"})
		_, _ = r.finish(bg, key)
	}
	return ctx.Err()
}

// cost is what the provider reported, priced with the record's price table:
// for kinds priced by what is made, the estimate counts unless the provider
// said how much it made.
func (r *runner) cost(u aiservices.Usage, fallback string) *models.GenCost {
	g := r.g
	if fallback == "none" {
		return &models.GenCost{Basis: "none"}
	}
	c := &models.GenCost{USD: g.Estimate.USD, InputTokens: g.Estimate.InputTokens, Unit: g.Estimate.Unit, Amount: g.Estimate.Amount, Basis: "assumed"}
	if !u.Known {
		return c
	}
	pr, err := aiservices.PriceAt(g.Provider, g.Kind, g.Model, g.CreatedAt)
	if err != nil {
		return c
	}
	amount := -1.0
	if pr.RoundSeconds > 0 && u.Seconds > 0 {
		// The provider counts in steps (D-ID: 15 seconds), as the estimate did.
		u.Seconds = math.Ceil(u.Seconds/pr.RoundSeconds-1e-9) * pr.RoundSeconds
	}
	switch pr.Unit {
	case "second":
		if u.Seconds > 0 {
			amount = u.Seconds
		}
	case "minute":
		if u.Seconds > 0 {
			amount = u.Seconds / 60
		}
	case "hour":
		if u.Seconds > 0 {
			amount = u.Seconds / 3600
		}
	case "thousand-characters":
		if u.Characters > 0 {
			amount = float64(u.Characters) / 1000
		}
	}
	if amount < 0 {
		c.Basis = "estimate"
		return c
	}
	usd := amount*pr.USD + inputUSD(g, pr)
	return &models.GenCost{USD: math.Round(usd*1e6) / 1e6, InputTokens: g.Estimate.InputTokens, Unit: pr.Unit, Amount: amount, Basis: "usage"}
}

func inputUSD(g *models.Generation, pr *aiservices.Price) float64 {
	return float64(g.Estimate.InputTokens) * pr.InputPerMillion / 1e6
}

// end writes the outcome once, at the database server's time (D360): how
// long a lost clip is looked for, and whether a presenter was deleted before
// it was made, are read from it on any computer.
func (r *runner) end(ctx context.Context, oc *models.GenOutcome, actual *models.GenCost) {
	oc.At = generate.StampNow(ctx, r.a)
	if actual != nil && actual.Basis != "" {
		oc.Actual = actual
	}
	bg := context.WithoutCancel(ctx)
	if err := generate.SetOnce(bg, r.a, r.g.ID, "outcome", oc); err == nil {
		if fresh, err := loadGen(bg, r.a, r.g.ID); err == nil {
			*r.g = *fresh
			return
		}
	}
	r.g.Outcome = oc
}

// finish settles the money, deletes what the provider kept and tells the
// person — each once — then reports the job's result.
func (r *runner) finish(ctx context.Context, key string) (map[string]any, error) {
	g := r.g
	bg := context.WithoutCancel(ctx)
	oc := g.Outcome
	if oc == nil {
		return nil, errors.New("the work has no outcome yet")
	}
	if g.Settled == nil {
		usd, note := 0.0, ""
		if oc.Actual != nil {
			usd = oc.Actual.USD
			if oc.Actual.Basis == "assumed" {
				note = "The provider didn't say what it counted, so the estimate counts."
			}
		}
		if err := generate.Settle(bg, r.a, g, usd, note); err != nil {
			return nil, err
		}
	}
	if g.Forgotten == nil && g.Ticket != nil && (key != "" || onThisComputer(g)) {
		var outs []aiservices.Output
		for _, o := range oc.Outputs {
			outs = append(outs, aiservices.Output{FileID: o.ProviderFileID, VoiceID: o.VoiceID, PresenterID: o.PresenterID})
		}
		f := models.GenForgotten{At: time.Now().UTC(), OK: true}
		if err := r.ad.Forget(bg, key, r.ticket(), outs); err != nil {
			f.OK, f.Detail, f.Kept = false, firstLine(err.Error()), errors.Is(err, aiservices.ErrKept)
		}
		if err := generate.SetOnce(bg, r.a, g.ID, "forgotten", f); err == nil {
			g.Forgotten = &f
		}
	}
	// A voice copy made while its consent was being withdrawn doesn't stay at the provider (D227).
	if g.Kind == aiservices.KindVoiceCopy && oc.State == models.GenDone && g.Withdrawn == nil {
		err := generate.ConsentsHold(bg, r.a, g)
		if generate.Unreadable(err) {
			return nil, err // tried again: a consent that couldn't be read isn't a withdrawn one
		}
		if err != nil {
			_, _ = withdraw(bg, r.a, g, "The consent it relied on was withdrawn while it was being made.")
		}
	}
	// A presenter clip whose answer was lost: looked for at the service for a
	// while (it may show up late), deleted when found (D277).
	if err := r.lostClip(bg, key); errors.Is(err, errLookAgain) {
		return nil, &generate.Wait{After: 2 * time.Minute, Reason: fmt.Sprintf("No answer came back from %s, and the clip isn't there yet: MetaRush looks for it again in 2 minutes.", g.ProviderTitle)}
	}
	// A presenter made: a draft to test (or deleted, D277).
	if g.Kind == aiservices.KindPresenterMake {
		if err := r.presenterMade(bg); err != nil {
			return nil, err
		}
	}
	if fn := endedFor(g.Origin); fn != nil {
		fn(bg, r.a, g) // the feature that asked tells the person (D310)
	} else {
		r.notify(bg)
	}
	res := map[string]any{"generationId": g.ID, "kind": g.Kind, "state": oc.State, "costUsd": costOf(g)}
	for _, o := range oc.Outputs {
		switch {
		case o.ResourceID != "":
			res["resourceId"] = o.ResourceID
		case o.PresenterID != "":
			res["presenterId"] = o.PresenterID
		case o.VoiceID != "":
			res["voiceId"] = o.VoiceID
		case o.AssetID != "" && g.Kind != aiservices.KindTranscription:
			res["assetId"] = o.AssetID
		}
	}
	if oc.State != models.GenDone {
		_ = r.a.DB.Update(bg, store.CJobs, r.a.WorkspaceID(), r.job.ID, bson.M{"$set": bson.M{"payload.code": oc.Code, "payload.advice": strings.TrimSpace(oc.Message + " " + oc.Hint)}})
		if oc.State == models.GenCanceled {
			return res, nil
		}
		return nil, &generate.Final{Err: apperr.New(firstNonEmpty(oc.Code, apperr.Provider), "%s", firstNonEmpty(oc.Message, "It couldn't be made."))}
	}
	return res, nil
}

// notify tells the person who asked how it went (once per generation).
func (r *runner) notify(ctx context.Context) {
	g := r.g
	oc := g.Outcome
	if oc == nil || g.CreatedBy.UserID == "" || oc.State == models.GenCanceled {
		return
	}
	n := models.Notification{UserID: g.CreatedBy.UserID, GroupKey: "generation:" + g.ID, JobID: g.JobID, ProjectID: g.ProjectID,
		Details: map[string]any{"generationId": g.ID, "kind": g.Kind, "costUsd": costOf(g), "state": oc.State}}
	thing := r.kind.What
	if oc.State == models.GenDone {
		n.Kind, n.Tone = models.NotifyGenerationReady, "success"
		n.Title = upperFirst(thing) + " is ready"
		n.Body = fmt.Sprintf("Made with %s for %s. %s", g.ProviderTitle, costs.Money(costOf(g)), readyWhere(g.Kind))
		if onThisComputer(g) {
			n.Body = fmt.Sprintf("Made on this computer with %s, free. %s", g.ModelTitle, readyWhere(g.Kind))
		}
		for _, o := range oc.Outputs {
			switch {
			case o.ResourceID != "":
				n.Link = links.Resource(g.ProjectID, o.ResourceID)
			case o.AssetID != "":
				n.AssetID = o.AssetID
				n.Link = links.Asset(g.ProjectID, o.AssetID)
			}
		}
	} else {
		n.Kind, n.Tone = models.NotifyGenerationFailed, "error"
		n.Title = "Couldn't make " + thing
		if oc.State == models.GenRefused {
			n.Title = g.ProviderTitle + " declined " + thing
		}
		n.Body = strings.TrimSpace(oc.Message + " " + oc.Hint)
		n.Link = links.Job(g.JobID)
	}
	_, _ = notify.Upsert(ctx, r.a, n, notify.Once)
}

func readyWhere(kind string) string {
	switch kind {
	case aiservices.KindPicture:
		return "It's in the resources library, marked as AI-generated."
	case aiservices.KindBackground:
		return "It's in the resources library as a new picture; the original is as it was."
	case aiservices.KindUpscale:
		return "It's in the resources library as a new picture, marked as AI-made; the original is as it was."
	case aiservices.KindTranscription:
		return "The clip's words are being replaced with it."
	case aiservices.KindVoiceCopy:
		return "It's with the narration voices; everything said with it is marked as an AI voice."
	case aiservices.KindPresenterMake:
		return "Make a test clip of it, watch it, then approve it."
	}
	return "It's in Sources, marked as AI-generated."
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

// Sweep closes AI work whose job ended without an outcome and lets go of the
// money held for it, and queues again the deletion of withdrawn voice copies
// whose job is gone. The daemon runs it with generated video's sweep, when it
// starts and every five minutes. A record's age is the database server's
// (D360): every computer sweeps.
func Sweep(ctx context.Context, a *app.App) int {
	var gs []models.Generation
	_ = a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"outcome": bson.M{"$exists": false}, "$and": bson.A{store.StaleBefore("createdAt", time.Minute)},
		"kind": bson.M{"$nin": models.VideoKinds}}, &gs)
	n := 0
	for i := range gs {
		g := &gs[i]
		j, err := a.Queue.Get(ctx, g.JobID)
		if err != nil || (j.State != models.JobCanceled && j.State != models.JobFailed) {
			continue
		}
		r := &runner{a: a, g: g, job: j}
		switch {
		case j.State == models.JobCanceled && g.Ticket == nil:
			r.end(ctx, &models.GenOutcome{State: models.GenCanceled, Message: "It was stopped before it was sent. Nothing was spent."}, &models.GenCost{Basis: "none"})
		case j.State == models.JobCanceled:
			r.end(ctx, &models.GenOutcome{State: models.GenCanceled, Message: "It was stopped."}, &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"})
		default:
			r.end(ctx, &models.GenOutcome{State: models.GenFailed, Code: apperr.Provider, Message: "MetaRush gave up on it: " + firstLine(j.Error)}, &models.GenCost{USD: g.Estimate.USD, Basis: "assumed"})
		}
		_ = generate.Settle(ctx, a, r.g, costOf(r.g), "")
		if fn := endedFor(r.g.Origin); fn != nil {
			fn(ctx, a, r.g) // the feature that asked hears it ended (D310)
		}
		n++
	}
	n += sweepFeatures(ctx, a)
	// Presenters whose consent has ended are retired; withdrawn voice copies
	// and retired presenters still at their provider keep a deletion job
	// (D227, D277).
	n += retireLapsed(ctx, a)
	return n + requeueRemovals(ctx, a) + requeuePresenterRemovals(ctx, a)
}
