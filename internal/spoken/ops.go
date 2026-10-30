package spoken

import (
	"context"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// SayMakeOut is a change asked for: its record and the speech being made.
type SayMakeOut struct {
	Change   ops.SpokenView `json:"change"`
	Estimate SayEstimate    `json:"estimate"`
	JobID    string         `json:"jobId,omitempty"`
	Summary  string         `json:"summary"`
}

func init() {
	ops.Def("cut.say.estimate", "Price changing a word or short phrase someone said into new words in their own voice copy, and check it may be made: whose voice (named by a person), their voice copy and the consent that covers new words with its service, the cut that plays it; nothing is sent or spent", ops.Flags{},
		func(ctx context.Context, a *app.App, in *SayIn) (*SayEstimate, error) {
			p, err := planSay(ctx, a, in)
			if err != nil {
				return nil, err
			}
			return &p.est, nil
		})
	ops.Def("cut.say.make", "Make new words in a speaker's own voice copy, with their consent, to replace a word or short phrase they said: priced first (maxUsd), people only, never automatic; the new words are checked by the recognizer and the voice's fingerprint before anyone can put them in the cut", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *SayIn) (*SayMakeOut, error) { return makeSay(ctx, a, in) })
}

// makeSay asks for the speech (D310): the change is recorded first, so the
// worker's keeper finds it; nothing is kept when the request is refused.
func makeSay(ctx context.Context, a *app.App, in *SayIn) (*SayMakeOut, error) {
	p, err := planSay(ctx, a, in)
	if err != nil {
		return nil, err
	}
	if !p.est.Allowed {
		code := apperr.Conflict
		if p.est.Code == "consent" {
			code = apperr.ConsentRequired
		}
		return nil, apperr.New(code, "%s", p.est.Problems[0]).WithDetails(map[string]any{"problems": p.est.Problems, "reason": p.est.Code})
	}
	// The database server's time (D360): the sweep on any computer compares
	// updatedAt while the change is being made.
	now, err := a.DB.Now(ctx)
	if err != nil {
		return nil, err
	}
	c := models.SpokenChange{ID: ids.New(ids.SpokenChange), GenerationID: ids.New(ids.Generation), WorkspaceID: a.WorkspaceID(), ProjectID: p.project.ID, Branch: p.est.Branch,
		AssetID: p.asset.ID, From: p.est.From, To: p.est.To, Before: p.est.Before, Words: p.est.Words, Context: p.est.Context,
		Speaker: *p.est.Speaker, VoiceGeneration: p.copyGen.ID, VoiceName: p.est.Voice.Name, ConsentID: p.est.ConsentID,
		Provider: p.copyGen.Provider, ProviderTitle: p.copyGen.ProviderTitle, Model: p.est.Model, EstimateUSD: p.est.USD,
		State: models.SpokenMaking, CreatedBy: a.Actor, CreatedAt: now, UpdatedAt: now}
	if p.head != nil {
		c.BaseRevision = p.head.ID
	}
	if err := a.DB.Insert(ctx, store.CSpokenChanges, c); err != nil {
		return nil, err
	}
	mk := p.make
	mk.MaxUSD, mk.Feature, mk.ID = in.MaxUSD, Origin+c.ID, c.GenerationID
	mo, err := aiwork.MakeFor(ctx, a, &mk)
	if err != nil {
		_, _ = a.DB.C(store.CSpokenChanges).DeleteOne(context.WithoutCancel(ctx), bson.M{"_id": c.ID, "workspaceId": a.WorkspaceID()}) // nothing was sent
		return nil, err
	}
	set := bson.M{"jobId": mo.JobID, "estimateUsd": mo.Estimate.USD}
	c.JobID, c.EstimateUSD = mo.JobID, mo.Estimate.USD
	_ = a.DB.Update(ctx, store.CSpokenChanges, a.WorkspaceID(), c.ID, bson.M{"$set": set})
	a.Audit(ctx, "cut.say.make", "spoken_change", c.ID, c.ProjectID, map[string]any{"asset": c.AssetID, "before": c.Before, "words": c.Words,
		"person": c.Speaker.PersonID, "voice": c.VoiceGeneration, "consent": c.ConsentID, "generation": c.GenerationID, "estimateUsd": c.EstimateUSD, "aiGenerated": true})
	v, err := ops.CallTyped[ops.SpokenView](ctx, a, "cut.say.show", ops.CutSayIDIn{ID: c.ID}, ops.Caller{Human: true})
	if err != nil {
		v = &ops.SpokenView{SpokenChange: c}
	}
	out := &SayMakeOut{Change: *v, Estimate: p.est, JobID: mo.JobID}
	out.Summary = "Making “" + c.Words + "” in " + c.Speaker.Name + "'s own voice copy (about " + costs.Money(mo.Estimate.USD) + "). It's checked before it can go in the cut."
	return out, nil
}

func init() {
	ops.Offer("cut.say.make", ops.Offering{ID: "change-a-spoken-word", Group: "fix", Title: "Change a spoken word",
		Does:  "Says a word or short phrase differently in the speaker's own voice copy, with their consent, checked on this computer and marked as changed everywhere; priced first.",
		Words: []string{"say it differently", "they said the wrong number", "replace a word someone said"},
		Needs: ops.Needs{Works: []string{"transcripts", "editing"}, Keys: copyKeys(), Priced: true,
			Leaves: "the new words, with the speaker's words around them, leave this computer for the service that holds the voice copy"}})
}

// copyKeys are the keys of the services that keep voice copies.
func copyKeys() []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range aiservices.ModelsOf(aiservices.KindVoiceCopy) {
		p, err := aiservices.ProviderOf(m.Provider)
		if err != nil || p.Key == "" || seen[p.Key] {
			continue
		}
		seen[p.Key] = true
		out = append(out, p.Key)
	}
	return out
}
