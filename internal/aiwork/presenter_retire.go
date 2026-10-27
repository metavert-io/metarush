package aiwork

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Retiring a presenter (D277), by a person or because the consent it relied
// on was withdrawn: from that moment nothing more is made with it; it and the
// service's consent record are deleted at the service here and now when this
// computer has the key, else by a job (a generate job with action
// remove-presenter) any computer with the key takes, tried until done; clips
// still waiting are stopped (being sent: never cut off, and not kept); and
// the cuts that use its clips are listed. A presenter whose answer was lost
// is looked up at the service by the tag in its name.

// ActionRemovePresenter is a generate job's action that deletes a retired
// presenter at its service.
const ActionRemovePresenter = "remove-presenter"

func init() {
	ops.Def("presenter.stock", "List an AI service's stock presenters with their licence — only those whose licence covers commercial use and client work are offered — and whether it covers ads", ops.Flags{},
		func(ctx context.Context, a *app.App, in *PresenterStockIn) (*PresenterStockOut, error) {
			return stockPresenters(ctx, a, in)
		})
	ops.Def("presenter.optout", "Say whether an AI service trains its models on what it is sent, and whether its training opt-out is recorded as set for the key on this computer", ops.Flags{},
		func(ctx context.Context, a *app.App, in *PresenterOptOutIn) (*PresenterOptOutOut, error) {
			return optOutView(ctx, a, in.Provider)
		})
	ops.Def("presenter.optout.set", "Record that an AI service's training opt-out is set for the account whose key this computer uses (or take that back): needed before a person's presenter is made at a service that trains on what it is sent (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *PresenterOptOutIn) (*PresenterOptOutOut, error) {
			return setOptOut(ctx, a, in)
		})
	generate.OnConsentRevoked(func(ctx context.Context, a *app.App, c *models.Consent) []string {
		var ps []models.Presenter
		_ = a.DB.Find(ctx, store.CPresenters, a.WorkspaceID(), bson.M{"consentId": c.ID, "state": bson.M{"$ne": models.PresenterRetired}}, &ps)
		var out []string
		for i := range ps {
			if res, err := retirePresenter(ctx, a, &ps[i], "The consent it relied on was withdrawn."); err == nil {
				out = append(out, res.Summary)
			}
		}
		return out
	})
}

func retirePresenter(ctx context.Context, a *app.App, pr *models.Presenter, reason string) (*PresenterOut, error) {
	bg := context.WithoutCancel(ctx)
	if pr.State == models.PresenterRetired {
		v := presenterView(ctx, a, pr, true)
		return &PresenterOut{Presenter: v, Summary: "It was retired already. " + v.Summary}, nil
	}
	now := time.Now().UTC()
	actor := a.Actor
	jobID := ids.New(ids.Job)
	res, err := a.DB.C(store.CPresenters).UpdateOne(bg, bson.M{"_id": pr.ID, "workspaceId": a.WorkspaceID(), "state": bson.M{"$ne": models.PresenterRetired}},
		bson.M{"$set": bson.M{"state": models.PresenterRetired, "retiredAt": now, "retiredBy": actor, "retireNote": reason, "retireJobId": jobID, "updatedAt": now}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		fresh, _ := findPresenter(bg, a, pr.ID)
		if fresh != nil {
			v := presenterView(ctx, a, fresh, true)
			return &PresenterOut{Presenter: v, Summary: "It was retired already. " + v.Summary}, nil
		}
	}
	pr.State, pr.RetiredAt, pr.RetiredBy, pr.RetireNote, pr.RetireJobID = models.PresenterRetired, &now, &actor, reason, jobID
	out := &PresenterOut{}
	// Clips waiting to be made with it are stopped; the making of it too.
	var pending []models.Generation
	or := bson.A{}
	if pr.ProviderID != "" {
		or = append(or, bson.M{"kind": aiservices.KindPresenter, "options.presenter": pr.ProviderID})
	}
	if pr.TrainingID != "" {
		or = append(or, bson.M{"_id": pr.TrainingID})
	}
	// The making asked for before the presenter noted it (makePresenter).
	or = append(or, bson.M{"kind": aiservices.KindPresenterMake, "options.presenter": pr.ID})
	if len(or) > 0 {
		_ = a.DB.Find(bg, store.CGenerations, a.WorkspaceID(), bson.M{"$or": or, "outcome": bson.M{"$exists": false}}, &pending)
	}
	for _, g := range pending {
		what := upperFirst(kindWhat(g.Kind))
		if stopped, err := a.Queue.CancelQueued(bg, g.JobID); err == nil && stopped {
			out.Stopped = append(out.Stopped, what+" was stopped before it was sent.")
			continue
		}
		switch {
		case g.Kind == aiservices.KindPresenterMake:
			out.Stopped = append(out.Stopped, fmt.Sprintf("The presenter %s is making is deleted there as soon as it is done.", g.ProviderTitle))
		case g.Ticket != nil:
			if _, err := a.Queue.RequestCancel(bg, g.JobID); err == nil {
				out.Stopped = append(out.Stopped, fmt.Sprintf("%s was stopped at %s.", what, g.ProviderTitle))
			}
		default:
			out.Stopped = append(out.Stopped, what+" being sent with it won't be kept.")
		}
	}
	// Deleted at the service: here when this computer has the key, else by a job.
	removed := false
	if key, err := keyFor(a, pr.Provider); err == nil {
		removed = removePresenterAt(bg, a, pr, key) == nil
	}
	var queueErr error
	if !removed {
		queueErr = ensurePresenterRemoval(bg, a, pr)
	}
	a.Audit(ctx, "presenter.retire", "presenter", pr.ID, "", map[string]any{"name": pr.Name, "person": pr.PersonName, "provider": pr.Provider, "reason": reason,
		"deletedThere": removed, "stopped": len(out.Stopped)})
	if fresh, err := findPresenter(bg, a, pr.ID); err == nil {
		pr = fresh
	}
	out.Presenter = presenterView(ctx, a, pr, true)
	out.Summary = fmt.Sprintf("Retired “%s”: nothing more is made with it.", pr.Title)
	switch {
	case pr.Removed != nil:
		out.Summary += " It was deleted at " + pr.ProviderTitle + "."
	case queueErr != nil:
		out.Summary += " MetaRush couldn't queue deleting it at " + pr.ProviderTitle + " just now and tries again by itself in a few minutes."
	default:
		out.Summary += fmt.Sprintf(" A computer with the %s key deletes it there.", pr.ProviderTitle)
	}
	if n := len(out.Presenter.UsedIn); n > 0 {
		var where []string
		for _, u := range out.Presenter.UsedIn {
			where = append(where, u.Text)
		}
		out.Summary += fmt.Sprintf(" Its clips stay in %s, marked as made with consent since withdrawn: %s.", count(n, "cut", "cuts"), strings.Join(where, "; "))
	}
	for _, s := range out.Stopped {
		out.Summary += " " + s
	}
	if out.Presenter.Erasure != "" {
		out.Summary += " " + out.Presenter.Erasure
	}
	return out, nil
}

// removePresenterAt deletes a retired presenter and the service's consent
// record at the service, once. A presenter whose answer was lost (no ID) is
// looked up by the tag in its name.
func removePresenterAt(ctx context.Context, a *app.App, pr *models.Presenter, key string) error {
	if pr.Removed != nil {
		return nil
	}
	ad, err := aiservices.Get(pr.Provider, aiservices.KindPresenterMake)
	if err != nil {
		return err
	}
	pm, ok := ad.(aiservices.PresenterMaker)
	if !ok {
		return apperr.New(apperr.NotConfigured, "MetaRush can't delete presenters at %s.", pr.ProviderTitle)
	}
	f := models.GenForgotten{At: time.Now().UTC(), OK: true}
	targets := []string{}
	training := trainingOf(ctx, a, pr)
	switch {
	case pr.ProviderID != "":
		targets = append(targets, pr.ProviderID)
	case training != "":
		g, err := loadGen(ctx, a, training)
		if err != nil {
			return err
		}
		if g.Outcome == nil && len(g.Sending) == 0 && g.Ticket == nil {
			break // never sent: nothing of it at the service
		}
		if g.Outcome == nil {
			return errLookAgain // still being made: deleted once it is done
		}
		found, err := pm.FindPresenters(ctx, key, voiceTag(g))
		if err != nil {
			return err
		}
		if len(found) == 0 && pr.RetiredAt != nil && time.Since(*pr.RetiredAt) < LostVoiceLookFor && g.Outcome.State != models.GenDone {
			return errLookAgain
		}
		targets = found
		f.Detail = fmt.Sprintf("No answer had come back; %s found at %s under its name.", count(len(found), "presenter was", "presenters were"), pr.ProviderTitle)
	}
	for _, id := range targets {
		if err := pm.RemovePresenter(ctx, key, id); err != nil && !notFound(err) {
			return err
		}
	}
	if pr.Challenge != nil && pr.Challenge.ID != "" {
		if err := pm.DropConsent(ctx, key, pr.Challenge.ID); err != nil && !notFound(err) {
			return err
		}
	}
	res, err := a.DB.C(store.CPresenters).UpdateOne(ctx, bson.M{"_id": pr.ID, "workspaceId": a.WorkspaceID(), "removed": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"removed": f, "updatedAt": time.Now().UTC()}})
	if err != nil {
		return err
	}
	if res.ModifiedCount > 0 {
		pr.Removed = &f
	}
	// The making's record says so too (the receipt reads it).
	if training != "" {
		w := models.GenWithdrawn{At: f.At, By: a.Actor, Reason: firstNonEmpty(pr.RetireNote, "The presenter was retired.")}
		_ = generate.SetOnce(ctx, a, training, "withdrawn", w)
		_ = generate.SetOnce(ctx, a, training, "removed", f)
	}
	return nil
}

// trainingOf is the generation that made (or is making) a presenter: its
// trainingId, else the latest asked for it before that was noted ("" none).
func trainingOf(ctx context.Context, a *app.App, pr *models.Presenter) string {
	if pr.TrainingID != "" {
		return pr.TrainingID
	}
	var g models.Generation
	if err := a.DB.C(store.CGenerations).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "kind": aiservices.KindPresenterMake, "options.presenter": pr.ID},
		options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}})).Decode(&g); err != nil {
		return ""
	}
	return g.ID
}

func notFound(err error) bool {
	var ve *videogen.Error
	return errors.As(err, &ve) && ve.Kind == videogen.KindNotFound
}

// ensurePresenterRemoval makes sure a retired presenter not yet deleted at
// its service has a deletion job waiting or running (no project, no
// computer: any computer with the key takes it; tried until done).
func ensurePresenterRemoval(ctx context.Context, a *app.App, pr *models.Presenter) error {
	if pr.State != models.PresenterRetired || pr.Removed != nil {
		return nil
	}
	mine := bson.M{"workspaceId": a.WorkspaceID(), "type": jobs.TypeGenerate, "payload.presenterId": pr.ID, "payload.action": ActionRemovePresenter}
	live := bson.M{"state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}}
	for k, v := range mine {
		live[k] = v
	}
	if n, err := a.DB.C(store.CJobs).CountDocuments(ctx, live); err != nil || n > 0 {
		return err
	}
	before, err := a.DB.C(store.CJobs).CountDocuments(ctx, mine)
	if err != nil {
		return err
	}
	id, key := pr.RetireJobID, "remove-presenter:"+pr.ID
	if before > 0 || id == "" {
		id, key = ids.New(ids.Job), fmt.Sprintf("remove-presenter:%s:%d", pr.ID, before+1)
	}
	_, _, err = a.Queue.Enqueue(ctx, jobs.EnqueueOptions{ID: id, Type: jobs.TypeGenerate, IdempotencyKey: key, Priority: 8, MaxAttempts: 1000, Actor: a.Actor,
		Payload: map[string]any{"presenterId": pr.ID, "kind": aiservices.KindPresenterMake, "action": ActionRemovePresenter, "what": "deleting a presenter", "provider": pr.ProviderTitle}})
	return err
}

// runRemovePresenter is the deletion job.
func runRemovePresenter(ctx context.Context, a *app.App, j *models.Job) (map[string]any, error) {
	id, _ := j.Payload["presenterId"].(string)
	res := map[string]any{"presenterId": id, "action": ActionRemovePresenter}
	pr, err := findPresenter(ctx, a, id)
	if err != nil {
		return nil, &generate.Final{Err: err}
	}
	if pr.Removed != nil {
		res["removed"] = true
		return res, nil
	}
	key, err := keyFor(a, pr.Provider)
	if err != nil {
		return nil, &generate.Wait{After: time.Hour, Reason: fmt.Sprintf("This computer doesn't have the %s key: a computer that has it deletes the presenter “%s”.", pr.ProviderTitle, pr.Title)}
	}
	if err := removePresenterAt(ctx, a, pr, key); err != nil {
		if errors.Is(err, errLookAgain) {
			return nil, &generate.Wait{After: 2 * time.Minute, Reason: fmt.Sprintf("MetaRush looks for the presenter “%s” at %s again in 2 minutes.", pr.Title, pr.ProviderTitle)}
		}
		return nil, err
	}
	res["removed"] = true
	return res, nil
}

// requeuePresenterRemovals queues the deletion again for every retired
// presenter still at its service whose job is gone (the daemon's sweep).
func requeuePresenterRemovals(ctx context.Context, a *app.App) int {
	var ps []models.Presenter
	_ = a.DB.Find(ctx, store.CPresenters, a.WorkspaceID(), bson.M{"state": models.PresenterRetired, "removed": bson.M{"$exists": false}}, &ps)
	n := 0
	for i := range ps {
		before, _ := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "payload.presenterId": ps[i].ID, "payload.action": ActionRemovePresenter})
		if ensurePresenterRemoval(ctx, a, &ps[i]) == nil {
			if after, _ := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "payload.presenterId": ps[i].ID, "payload.action": ActionRemovePresenter}); after > before {
				n++
			}
		}
	}
	return n
}

// presenterMade records what making a presenter came to (the job's end):
// made (a draft to test), or not; a presenter retired meanwhile, or whose
// consent was withdrawn while it was made, is deleted at once. It writes
// trainingId with the outcome, so makePresenter, if it notes the making only
// now, leaves the outcome alone.
func (r *runner) presenterMade(ctx context.Context) error {
	g := r.g
	found, _ := r.presenterOf(ctx)
	if found == nil {
		return nil
	}
	pr := *found
	oc := g.Outcome
	now := time.Now().UTC()
	id := ""
	if oc != nil {
		for _, o := range oc.Outputs {
			if o.PresenterID != "" {
				id = o.PresenterID
			}
		}
	}
	coll := r.a.DB.C(store.CPresenters)
	mine := func(extra bson.M) bson.M {
		f := bson.M{"_id": pr.ID, "workspaceId": r.a.WorkspaceID()}
		for k, v := range extra {
			f[k] = v
		}
		return f
	}
	switch {
	case oc != nil && oc.State == models.GenDone && id != "":
		// Its ID is kept whatever happened meanwhile: a retired one is deleted by it.
		_, _ = coll.UpdateOne(ctx, mine(bson.M{"providerId": bson.M{"$exists": false}}), bson.M{"$set": bson.M{"providerId": id, "updatedAt": now}})
		_, _ = coll.UpdateOne(ctx, mine(bson.M{"state": bson.M{"$in": bson.A{models.PresenterWaiting, models.PresenterTraining, models.PresenterFailed}}}),
			bson.M{"$set": bson.M{"state": models.PresenterDraft, "trainingId": g.ID, "updatedAt": now}, "$unset": bson.M{"problem": ""}})
	case oc != nil:
		msg := strings.TrimSpace(oc.Message + " " + oc.Hint)
		_, _ = coll.UpdateOne(ctx, mine(bson.M{"state": bson.M{"$in": bson.A{models.PresenterWaiting, models.PresenterTraining, models.PresenterFailed}}}),
			bson.M{"$set": bson.M{"state": models.PresenterFailed, "problem": firstNonEmpty(msg, "It couldn't be made."), "trainingId": g.ID, "updatedAt": now}})
	}
	if fresh, err := findPresenter(ctx, r.a, pr.ID); err == nil {
		pr = *fresh
	}
	lost := lostAnswer(g) || (oc != nil && oc.State != models.GenDone && len(g.Sending) > len(g.Sends))
	if pr.State != models.PresenterRetired {
		err := generate.ConsentsHold(ctx, r.a, g)
		if generate.Unreadable(err) {
			return err // a consent that couldn't be read just now isn't a withdrawn one: tried again
		}
		if err != nil {
			_, _ = retirePresenter(ctx, r.a, &pr, "The consent it relied on was withdrawn while it was being made.")
			return nil
		}
		if lost {
			_, _ = retirePresenter(ctx, r.a, &pr, "No answer came back when it was made, so whatever was made is deleted.")
		}
		return nil
	}
	// Retired while it was being made: delete what was made — again, when it
	// was counted as deleted before the service had made it.
	if pr.Removed != nil && (id != "" || lost) && oc != nil && pr.Removed.At.Before(oc.At) {
		if res, err := coll.UpdateOne(ctx, mine(nil), bson.M{"$unset": bson.M{"removed": ""}, "$set": bson.M{"updatedAt": now}}); err == nil && res.ModifiedCount > 0 {
			pr.Removed = nil
		}
	}
	if key, err := keyFor(r.a, pr.Provider); err == nil && removePresenterAt(ctx, r.a, &pr, key) == nil {
		return nil
	}
	_ = ensurePresenterRemoval(ctx, r.a, &pr)
	return nil
}

// lostAnswer reports work whose answer was lost on the way back: something
// may have been made at the service all the same.
func lostAnswer(g *models.Generation) bool {
	for _, s := range g.Sends {
		if s.Code == videogen.KindUncertain {
			return true
		}
	}
	return false
}

// --- training opt-outs (D276) ---

// trainingAllowed refuses to send a person's face or voice (who: whose, in
// words) to a service that trains on what it is sent, unless the account can
// opt out and the opt-out is recorded for the key this computer uses (D276).
func trainingAllowed(ctx context.Context, a *app.App, p *aiservices.ProviderInfo, key, who string) error {
	who = upperFirst(firstNonEmpty(who, "a person's face or voice"))
	switch p.Data.TrainsOnUploads {
	case aiservices.TrainsYes:
		return apperr.New(apperr.Conflict, "%s would go to %s, which trains its models on what it is sent with no way to opt out, so MetaRush doesn't send it there.", who, p.Title).
			WithDetails(map[string]any{"reason": "training", "provider": p.Name})
	case aiservices.TrainsUnlessOptedOut:
		if optOutFor(ctx, a, p.Name, key) == nil {
			e := apperr.New(apperr.Conflict, "%s would go to %s, which trains its models on what it is sent unless the account opts out, and no opt-out is recorded for the key this computer uses. Set the opt-out for that account, then record that you did.", who, p.Title).
				WithDetails(map[string]any{"reason": "training_opt_out", "provider": p.Name}).WithCommand("metarush presenter opt-out " + p.Name + " --how \"<what you did>\"")
			if p.Data.OptOut != "" {
				e = e.WithHint("How: " + p.Data.OptOut)
			}
			return e
		}
	}
	return nil
}

// optOutFor is the opt-out recorded for a service and this key (nil: none).
func optOutFor(ctx context.Context, a *app.App, provider, key string) *models.TrainingOptOut {
	var os []models.TrainingOptOut
	_ = a.DB.Find(ctx, store.CTrainingOptOuts, a.WorkspaceID(), bson.M{"provider": provider, "removedAt": bson.M{"$exists": false}}, &os,
		options.Find().SetSort(bson.D{{Key: "recordedAt", Value: -1}}))
	ending := providers.Mask(key)
	for i := range os {
		if os[i].KeyEnding == ending {
			return &os[i]
		}
	}
	return nil
}

func optOutView(ctx context.Context, a *app.App, provider string) (*PresenterOptOutOut, error) {
	p, err := aiservices.ProviderOf(provider)
	if err != nil {
		return nil, err
	}
	out := &PresenterOptOutOut{Provider: p.Name, ProviderTitle: p.Title, Trains: firstNonEmpty(p.Data.TrainsOnUploads, "unstated")}
	key, kerr := keyFor(a, p.Name)
	if kerr == nil {
		out.OptOut = optOutFor(ctx, a, p.Name, key)
	}
	switch p.Data.TrainsOnUploads {
	case aiservices.TrainsNo:
		out.Summary = fmt.Sprintf("%s's terms say it doesn't train its models on what it is sent; no opt-out is needed.", p.Title)
	case aiservices.TrainsYes:
		out.Summary = fmt.Sprintf("%s trains its models on what it is sent, with no way to opt out: MetaRush doesn't make presenters of people there.", p.Title)
	case aiservices.TrainsUnlessOptedOut:
		switch {
		case kerr != nil:
			out.Summary = fmt.Sprintf("%s trains on what it is sent unless the account opts out. This computer has no %s key yet.", p.Title, p.Title)
		case out.OptOut != nil:
			out.Summary = fmt.Sprintf("%s trains on what it is sent unless the account opts out; %s recorded the opt-out as set for this key on %s.", p.Title,
				firstNonEmpty(out.OptOut.RecordedBy.UserName, "someone"), out.OptOut.RecordedAt.Local().Format("2 Jan 2006"))
		default:
			out.Summary = fmt.Sprintf("%s trains on what it is sent unless the account opts out, and no opt-out is recorded for this key: no presenter of a person is made there until it is.", p.Title)
		}
	default:
		out.Summary = fmt.Sprintf("MetaRush's catalogue doesn't say whether %s trains on what it is sent.", p.Title)
	}
	return out, nil
}

func setOptOut(ctx context.Context, a *app.App, in *PresenterOptOutIn) (*PresenterOptOutOut, error) {
	p, err := aiservices.ProviderOf(in.Provider)
	if err != nil {
		return nil, err
	}
	key, err := keyFor(a, p.Name)
	if err != nil {
		return nil, apperr.New(apperr.NotConfigured, "This computer has no %s key: the opt-out is recorded for the account whose key it uses.", p.Title)
	}
	now := time.Now().UTC()
	if in.Remove {
		actor := a.Actor
		if _, err := a.DB.C(store.CTrainingOptOuts).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "provider": p.Name, "keyEnding": providers.Mask(key),
			"removedAt": bson.M{"$exists": false}}, bson.M{"$set": bson.M{"removedAt": now, "removedBy": actor}}); err != nil {
			return nil, err
		}
		a.Audit(ctx, "presenter.optout.set", "provider", p.Name, "", map[string]any{"removed": true})
		out, err := optOutView(ctx, a, p.Name)
		if err == nil {
			out.Summary = "Took back the opt-out record. " + out.Summary
		}
		return out, err
	}
	how := strings.TrimSpace(in.How)
	if len([]rune(how)) < 8 {
		return nil, apperr.New(apperr.InvalidInput, "Say what you did to opt out, in your own words (for example: “wrote to its privacy team on 25 Oct and got a confirmation”).")
	}
	o := models.TrainingOptOut{ID: ids.New(ids.TrainingOptOut), WorkspaceID: a.WorkspaceID(), Provider: p.Name, ProviderTitle: p.Title, KeyEnding: providers.Mask(key), How: how,
		RecordedBy: a.Actor, RecordedAt: now, Machine: a.Cfg.Installation.Name}
	if err := a.DB.Insert(ctx, store.CTrainingOptOuts, o); err != nil {
		return nil, err
	}
	a.Audit(ctx, "presenter.optout.set", "provider", p.Name, "", map[string]any{"how": how, "keyEnding": o.KeyEnding})
	out, err := optOutView(ctx, a, p.Name)
	if err == nil {
		out.Summary = "Recorded. " + out.Summary
	}
	return out, err
}

// --- stock presenters (D275) ---

func stockPresenters(ctx context.Context, a *app.App, in *PresenterStockIn) (*PresenterStockOut, error) {
	out := &PresenterStockOut{Services: []PresenterStockService{}}
	offered := 0
	for _, p := range aiservices.Providers() {
		if p.Presenters == nil || (in.Provider != "" && !strings.EqualFold(in.Provider, p.Name)) {
			continue
		}
		s := PresenterStockService{Provider: p.Name, ProviderTitle: p.Title, Licence: p.Presenters.Stock, Offered: p.Presenters.Stock.Offered(), KeyReady: keyReady(a, p.Name),
			Presenters: []aiservices.StockPresenter{}}
		switch {
		case !s.Offered:
			s.Note = p.Presenters.Stock.Summary
		case !s.KeyReady:
			s.Note = fmt.Sprintf("Add the %s key in Settings → Integrations to see its stock presenters.", p.Title)
		default:
			ad, err := aiservices.Get(p.Name, aiservices.KindPresenter)
			lister, ok := ad.(aiservices.StockLister)
			if err != nil || !ok {
				s.Note = "MetaRush can't list " + p.Title + "'s stock presenters yet."
				break
			}
			key, _ := keyFor(a, p.Name)
			list, err := lister.StockPresenters(ctx, key)
			if err != nil {
				s.Note = apperr.From(providerErr(err)).Message
				break
			}
			s.Presenters = append(s.Presenters, list...)
			offered += len(list)
			s.Note = p.Presenters.Stock.AdsWords(p.Title)
		}
		out.Services = append(out.Services, s)
	}
	if in.Provider != "" && len(out.Services) == 0 {
		return nil, apperr.New(apperr.NotFound, "MetaRush knows no AI service called “%s” that makes presenters.", in.Provider)
	}
	out.Summary = fmt.Sprintf("%s offered.", count(offered, "stock presenter", "stock presenters"))
	if offered == 0 {
		out.Summary = "No stock presenters are offered: a service's licence must cover commercial use and client work."
	}
	return out, nil
}

// --- links for services that download inputs themselves ---

// linksPossible reports whether MetaRush can give a service links to stored
// files: the media store makes short-lived links, or the service is a test
// server on this computer.
func linksPossible(a *app.App, ad aiservices.Adapter) error {
	lt, ok := ad.(aiservices.LinkTaker)
	if !ok || len(lt.LinkRoles()) == 0 {
		return nil
	}
	if a.Media != nil && a.Media.Capabilities().PresignedGet {
		return nil
	}
	if lt.LocalProvider() {
		return nil
	}
	return apperr.New(apperr.NotConfigured, "%s downloads the training take from a link, and this MetaRush keeps its files on this computer, where it can't reach them. Keep the media in cloud storage that makes links (Settings → Storage) to make a presenter there.",
		providerTitle(ad.Provider()))
}

// linkFor is a short-lived link to stored bytes for a service to download.
var linkFor = func(ctx context.Context, a *app.App, ad aiservices.Adapter, ref models.GenReference, localPath string) (string, error) {
	name := ref.Role + aiservices.ExtFor(ref.MIME)
	if a.Media != nil && a.Media.Capabilities().PresignedGet {
		return a.Media.PresignGet(ctx, ref.StorageKey, 2*time.Hour, ref.MIME, name)
	}
	if lt, ok := ad.(aiservices.LinkTaker); ok && lt.LocalProvider() {
		return localLinks.add(localPath, name)
	}
	return "", linksPossible(a, ad)
}

// localLinks serves stored files on this computer to a test service on this
// computer only (go test, browser runs): never started otherwise.
var localLinks = &linkServer{files: map[string]linkFile{}}

type linkFile struct {
	path    string
	expires time.Time
}

type linkServer struct {
	mu    sync.Mutex
	base  string
	files map[string]linkFile
}

func (s *linkServer) add(localPath, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.base == "" {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return "", err
		}
		s.base = "http://" + l.Addr().String()
		go func() { _ = http.Serve(l, http.HandlerFunc(s.serve)) }()
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	p := "/" + hex.EncodeToString(b) + "/" + path.Base(name)
	now := time.Now()
	for k, f := range s.files {
		if now.After(f.expires) {
			delete(s.files, k)
		}
	}
	s.files[p] = linkFile{path: localPath, expires: now.Add(2 * time.Hour)}
	return s.base + p, nil
}

func (s *linkServer) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	f, ok := s.files[r.URL.Path]
	s.mu.Unlock()
	if !ok || time.Now().After(f.expires) || r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, f.path)
}
