package aiwork

import (
	"context"
	"errors"
	"fmt"
	"strings"
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
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Voice copies (D227) are kept at the provider only as long as the consent
// they rely on holds. A person can withdraw one at any time, and withdrawing
// the consent withdraws every copy made with it: from that moment nothing more
// is said with it, and deleting it at the provider is a job (a generate job
// with action remove-voice) any computer with the provider's key takes, tried
// again until it is done; a computer without the key passes it on. Work still
// waiting or running when its consent is withdrawn is stopped, and checked
// again just before it is sent.

// ActionRemoveVoice is a generate job's action that deletes a voice copy at the provider.
const ActionRemoveVoice = "remove-voice"

func init() {
	generate.OnConsentRevoked(func(ctx context.Context, a *app.App, c *models.Consent) []string {
		var out []string
		// Voice copies made with it: withdrawn, and deleted at the provider.
		var gs []models.Generation
		_ = a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"kind": aiservices.KindVoiceCopy, "consentIds": c.ID, "withdrawn": bson.M{"$exists": false},
			"outcome.state": models.GenDone}, &gs)
		for i := range gs {
			if v, err := withdraw(ctx, a, &gs[i], "The consent it relied on was withdrawn."); err == nil {
				out = append(out, v.Summary)
			}
		}
		// AI work relying on it that hasn't ended. Still waiting: stopped. Waiting
		// at the provider (its ticket saved): stopped there. Being sent: never cut
		// off (the answer would be lost, and a voice copy left at the provider);
		// it is checked again just before it is sent, what comes back isn't kept,
		// and a voice copy is withdrawn and deleted as soon as it is done.
		var pending []models.Generation
		_ = a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"kind": bson.M{"$nin": models.VideoKinds}, "consentIds": c.ID, "outcome": bson.M{"$exists": false}}, &pending)
		for _, g := range pending {
			what := upperFirst(kindWhat(g.Kind))
			if stopped, err := a.Queue.CancelQueued(ctx, g.JobID); err == nil && stopped {
				out = append(out, what+" that relied on it was stopped before it was sent.")
				continue
			}
			switch {
			case g.Kind == aiservices.KindVoiceCopy || g.Kind == aiservices.KindPresenterMake:
				out = append(out, fmt.Sprintf("%s being made with it is withdrawn and deleted at %s as soon as it is done.", what, g.ProviderTitle))
			case g.Ticket != nil:
				if _, err := a.Queue.RequestCancel(ctx, g.JobID); err == nil {
					out = append(out, fmt.Sprintf("%s that relied on it was stopped at %s.", what, g.ProviderTitle))
				}
			default:
				out = append(out, what+" being sent with it won't be kept.")
			}
		}
		return out
	})
}

func voiceView(ctx context.Context, a *app.App, g *models.Generation) VoiceView {
	v := VoiceView{GenerationID: g.ID, Provider: g.Provider, ProviderTitle: g.ProviderTitle, Model: g.Model, Name: voiceName(g), State: "ready"}
	if g.Outcome != nil {
		for _, o := range g.Outcome.Outputs {
			if o.VoiceID != "" {
				v.VoiceID = o.VoiceID
			}
		}
	}
	for _, id := range g.ConsentIDs {
		if c, err := generate.LoadConsent(ctx, a, id); err == nil {
			v.Person = c.PersonName
		}
	}
	switch {
	case g.Removed != nil:
		v.State = "withdrawn"
		v.Summary = fmt.Sprintf("“%s” (%s's voice) was withdrawn and deleted at %s.", v.Name, v.Person, g.ProviderTitle)
	case g.Withdrawn != nil:
		v.State = "withdrawing"
		v.Summary = fmt.Sprintf("“%s” (%s's voice) was withdrawn: nothing more is said with it, and MetaRush is deleting it at %s (a computer with the %s key does it).",
			v.Name, v.Person, g.ProviderTitle, g.ProviderTitle)
	default:
		v.Summary = fmt.Sprintf("“%s”, a copy of %s's voice at %s.", v.Name, v.Person, g.ProviderTitle)
	}
	return v
}

func listVoices(ctx context.Context, a *app.App, in *VoicesIn) (*VoicesOut, error) {
	filter := bson.M{"kind": aiservices.KindVoiceCopy, "outcome.state": models.GenDone}
	if strings.TrimSpace(in.Person) != "" {
		p, err := ops.ResolvePerson(ctx, a, in.Person)
		if err != nil {
			return nil, err
		}
		var cs []models.Consent
		_ = a.DB.Find(ctx, store.CConsents, a.WorkspaceID(), bson.M{"personId": p.ID}, &cs)
		ids := bson.A{}
		for _, c := range cs {
			ids = append(ids, c.ID)
		}
		filter["consentIds"] = bson.M{"$in": ids}
	}
	var gs []models.Generation
	if err := a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), filter, &gs, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(200)); err != nil {
		return nil, err
	}
	out := &VoicesOut{Voices: []VoiceView{}}
	ready := 0
	for i := range gs {
		v := voiceView(ctx, a, &gs[i])
		if v.State == "ready" {
			ready++
		}
		out.Voices = append(out.Voices, v)
	}
	out.Summary = fmt.Sprintf("%s ready to use.", count(ready, "voice copy", "voice copies"))
	if ready == 0 {
		out.Summary = "No voice copies ready to use."
	}
	return out, nil
}

func removeVoice(ctx context.Context, a *app.App, in *VoiceRemoveIn) (*VoiceRemoveOut, error) {
	id := strings.TrimSpace(in.ID)
	var g models.Generation
	filter := bson.M{"workspaceId": a.WorkspaceID(), "kind": aiservices.KindVoiceCopy}
	if strings.HasPrefix(id, "gen_") {
		filter["_id"] = id
	} else {
		filter["outcome.outputs.voiceId"] = id
	}
	if err := a.DB.C(store.CGenerations).FindOne(ctx, filter).Decode(&g); err != nil {
		return nil, apperr.New(apperr.NotFound, "MetaRush made no voice copy like that.").WithDetails(map[string]any{"id": id})
	}
	if g.Withdrawn != nil {
		v := voiceView(ctx, a, &g)
		return &VoiceRemoveOut{Voice: v, Summary: "It was withdrawn already. " + v.Summary}, nil
	}
	v, err := withdraw(ctx, a, &g, firstNonEmpty(in.Note, "A person withdrew it."))
	if err != nil {
		return nil, err
	}
	return &VoiceRemoveOut{Voice: *v, Summary: v.Summary}, nil
}

// voiceOf is the provider's ID of the voice a copy made.
func voiceOf(g *models.Generation) string {
	if g.Outcome != nil {
		for _, o := range g.Outcome.Outputs {
			if o.VoiceID != "" {
				return o.VoiceID
			}
		}
	}
	return ""
}

// withdraw records a voice copy withdrawn (once: nothing more is said with
// it) and deletes it at the provider: here and now when this computer has the
// key, else by the deletion job, tried again until it is done. When it was
// withdrawn is the database server's time (D360): the deletion, on any
// computer with the key, looks for a lost copy for a while after it.
func withdraw(ctx context.Context, a *app.App, g *models.Generation, reason string) (*VoiceView, error) {
	bg := context.WithoutCancel(ctx)
	at, err := generate.Now(bg, a)
	if err != nil {
		return nil, err
	}
	w := models.GenWithdrawn{At: at, By: a.Actor, Reason: reason, JobID: ids.New(ids.Job)}
	if err := generate.SetOnce(bg, a, g.ID, "withdrawn", w); err != nil {
		return nil, err
	}
	if fresh, err := loadGen(bg, a, g.ID); err == nil {
		*g = *fresh
	}
	removed := false
	if key, err := keyFor(a, g.Provider); err == nil {
		removed = removeAtProvider(bg, a, g, key) == nil
	}
	var queueErr error
	if !removed {
		queueErr = ensureRemoval(bg, a, g)
	}
	details := map[string]any{"provider": g.Provider, "voiceId": voiceOf(g), "deletedThere": removed, "reason": reason}
	if queueErr != nil {
		details["queueError"] = queueErr.Error()
	}
	a.Audit(ctx, "ai.voices.remove", "generation", g.ID, g.ProjectID, details)
	v := voiceView(ctx, a, g)
	if queueErr != nil {
		// Withdrawn all the same; the sweep queues the deletion (daemon start, every few minutes).
		v.Summary += " MetaRush couldn't queue deleting it just now and tries again by itself in a few minutes."
	}
	return &v, nil
}

// ensureRemoval makes sure a withdrawn voice copy not yet deleted at the
// provider has a deletion job waiting or running. The job belongs to no
// project (clearing a project out never calls it off) and to no computer
// (any computer with the key takes it); one that ended without deleting it
// (stopped by someone, or out of tries) is replaced by a new one.
func ensureRemoval(ctx context.Context, a *app.App, g *models.Generation) error {
	if g.Withdrawn == nil || g.Removed != nil {
		return nil
	}
	mine := bson.M{"workspaceId": a.WorkspaceID(), "type": jobs.TypeGenerate, "payload.generationId": g.ID, "payload.action": ActionRemoveVoice}
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
	id, key := g.Withdrawn.JobID, "remove-voice:"+g.ID
	if before > 0 || id == "" {
		id, key = ids.New(ids.Job), fmt.Sprintf("remove-voice:%s:%d", g.ID, before+1)
	}
	_, _, err = a.Queue.Enqueue(ctx, jobs.EnqueueOptions{ID: id, Type: jobs.TypeGenerate, IdempotencyKey: key, Priority: 8, MaxAttempts: 1000, Actor: a.Actor,
		Payload: map[string]any{"generationId": g.ID, "kind": g.Kind, "action": ActionRemoveVoice, "what": "deleting a voice copy", "provider": g.ProviderTitle}})
	return err
}

// requeueRemovals queues the deletion again for every withdrawn voice copy
// still at its provider whose job is gone (D227). The daemon runs it with
// the sweep, when it starts and every few minutes.
func requeueRemovals(ctx context.Context, a *app.App) int {
	var gs []models.Generation
	_ = a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"kind": aiservices.KindVoiceCopy, "withdrawn": bson.M{"$exists": true}, "removed": bson.M{"$exists": false}}, &gs)
	n := 0
	for i := range gs {
		before, _ := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "payload.generationId": gs[i].ID, "payload.action": ActionRemoveVoice})
		if ensureRemoval(ctx, a, &gs[i]) == nil {
			if after, _ := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "payload.generationId": gs[i].ID, "payload.action": ActionRemoveVoice}); after > before {
				n++
			}
		}
	}
	return n
}

// LostVoiceLookFor is how long MetaRush keeps looking for a voice copy whose
// answer was lost before it says none was made (a provider may finish making
// it a little after the answer was lost).
var LostVoiceLookFor = 10 * time.Minute

// errLookAgain: a lost voice copy wasn't found yet; it is looked for again.
var errLookAgain = errors.New("not found at the provider yet")

// VoiceTag is the tag MetaRush puts in a voice copy's name at the provider
// (aiservices.TaggedName), to find it again when the answer is lost.
func VoiceTag(g *models.Generation) string { return voiceTag(g) }

func voiceTag(g *models.Generation) string {
	id := strings.ToLower(g.ID)
	if len(id) > 10 {
		id = id[len(id)-10:]
	}
	return "mr-" + id
}

// removeAtProvider deletes a withdrawn voice copy at its provider and records
// it once; a voice the provider no longer has counts as deleted. A copy whose
// answer was lost (no voice ID) is looked up by the tag in its name.
func removeAtProvider(ctx context.Context, a *app.App, g *models.Generation, key string) error {
	if g.Removed != nil {
		return nil
	}
	ad, err := aiservices.Get(g.Provider, g.Kind)
	if err != nil {
		return err
	}
	rm, ok := ad.(aiservices.VoiceRemover)
	if !ok {
		return apperr.New(apperr.NotConfigured, "MetaRush can't delete voices at %s.", g.ProviderTitle)
	}
	f := models.GenForgotten{At: time.Now().UTC(), OK: true}
	opts := models.GenOptions{}
	if g.Options != nil {
		opts = *g.Options
	}
	voices := []string{}
	if voice := voiceOf(g); voice != "" {
		voices = append(voices, voice)
	} else if fd, ok := ad.(aiservices.VoiceFinder); ok {
		found, err := fd.FindVoices(ctx, key, voiceTag(g), opts)
		if err != nil {
			return err
		}
		if len(found) == 0 && g.Withdrawn != nil {
			// By the server's clock, as Withdrawn.At is (withdraw).
			now, err := generate.Now(ctx, a)
			if err != nil {
				return err
			}
			if now.Sub(g.Withdrawn.At) < LostVoiceLookFor {
				return errLookAgain
			}
		}
		voices = found
		f.Detail = fmt.Sprintf("No answer had come back; %s found at %s under its name.", count(len(found), "voice was", "voices were"), g.ProviderTitle)
		if len(found) == 0 {
			f.Detail = "No answer had come back, and " + g.ProviderTitle + " has no voice under its name: none was made."
		}
	}
	for _, voice := range voices {
		if err := rm.RemoveVoice(ctx, key, voice, opts); err != nil {
			var ve *videogen.Error
			if !errors.As(err, &ve) || ve.Kind != videogen.KindNotFound {
				return err
			}
			if f.Detail == "" {
				f.Detail = g.ProviderTitle + " no longer had it."
			}
		}
	}
	if err := generate.SetOnce(ctx, a, g.ID, "removed", f); err != nil {
		return err
	}
	g.Removed = &f
	return nil
}

// runRemoveVoice is the deletion job: a computer with the key deletes the
// voice; one without it passes the job on, and it is tried again until done.
func runRemoveVoice(ctx context.Context, a *app.App, g *models.Generation) (map[string]any, error) {
	res := map[string]any{"generationId": g.ID, "kind": g.Kind, "action": ActionRemoveVoice}
	if g.Removed != nil {
		res["removed"] = true
		return res, nil
	}
	key, err := keyFor(a, g.Provider)
	if err != nil {
		return nil, &generate.Wait{After: time.Hour, Reason: fmt.Sprintf("This computer doesn't have the %s key: a computer that has it deletes the voice copy “%s”.", g.ProviderTitle, voiceName(g))}
	}
	if err := removeAtProvider(ctx, a, g, key); err != nil {
		if errors.Is(err, errLookAgain) {
			return nil, &generate.Wait{After: 2 * time.Minute, Reason: fmt.Sprintf("No answer came back when the voice copy “%s” was made; MetaRush looks for it at %s again in 2 minutes.", voiceName(g), g.ProviderTitle)}
		}
		return nil, err // tried again with a backoff
	}
	res["removed"] = true
	return res, nil
}
