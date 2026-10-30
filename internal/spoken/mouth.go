package spoken

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// The mouth matches the new words (D312): only when a person asks, after
// the new words passed their check; through AI work's lip sync (every face
// in the stretch named by a person, everyone seen or heard there consenting
// to lip sync with that service, children and public figures never), priced
// first. The clip that comes back plays in place of the recording's picture
// over the stretch when the change is put in the cut.

// MouthOrigin is the prefix of the lip sync a change asks for.
const MouthOrigin = "say-mouth:"

// MouthIn asks for the mouth to match a change's new words.
type MouthIn struct {
	ID     string   `json:"id" jsonschema:"pattern=^swc_[0-9a-z]{26}$,description=The change (ready: made and checked)."`
	Model  string   `json:"model,omitempty" jsonschema:"maxLength=160,description=provider/model of the lip sync (default: the usual one)."`
	MaxUSD *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0,description=The most you agree to pay\\, in US dollars (from the estimate). Required unless your estimates are off."`
	DryRun bool     `json:"dryRun,omitempty" jsonschema:"description=Price and check it only; nothing is sent or spent."`
}

// MouthOut is the lip sync priced (a dry run) or asked for.
type MouthOut struct {
	Change   ops.SpokenView     `json:"change"`
	Estimate *aiwork.AIEstimate `json:"estimate,omitempty"`
	JobID    string             `json:"jobId,omitempty"`
	Summary  string             `json:"summary"`
}

func init() {
	ops.Def("cut.say.mouth", "Match the mouth to new words made in a speaker's own voice copy (lip sync over the stretch, only when asked): every face there named by a person and consenting to lip sync with the service; priced first, people only; a dry run prices it", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *MouthIn) (*MouthOut, error) { return askMouth(ctx, a, in) })
	aiwork.RegisterKeeper(MouthOrigin, nil, endedMouth)
}

func askMouth(ctx context.Context, a *app.App, in *MouthIn) (*MouthOut, error) {
	var c models.SpokenChange
	if err := a.DB.GetByID(ctx, store.CSpokenChanges, a.WorkspaceID(), strings.TrimSpace(in.ID), &c); err != nil {
		return nil, apperr.New(apperr.NotFound, "There's no such change of words.")
	}
	switch {
	case c.State != models.SpokenReady && c.State != models.SpokenUndone && c.State != models.SpokenPlaced:
		return nil, apperr.New(apperr.Conflict, "The mouth can be matched once the new words are made and have passed their check.")
	case c.Sound == "" || c.Fit == nil:
		return nil, apperr.New(apperr.Conflict, "These new words have no sound to match the mouth to.")
	case c.Mouth != nil && c.Mouth.State == models.SpokenMaking:
		return nil, apperr.New(apperr.Conflict, "The mouth is already being matched.")
	case c.Mouth != nil && c.Mouth.State == models.SpokenReady:
		return nil, apperr.New(apperr.Conflict, "The mouth already matches these new words.")
	}
	from, to := c.Fit.From, c.Fit.To
	mk := aiwork.MakeIn{Kind: aiservices.KindLipSync, ProjectID: c.ProjectID, Model: in.Model, Clip: c.AssetID, From: &from, To: &to, Sound: c.Sound,
		MaxUSD: in.MaxUSD, DryRun: in.DryRun, Feature: MouthOrigin + c.ID, ID: ids.New(ids.Generation)}
	if in.DryRun {
		est, err := aiwork.EstimateFor(ctx, a, &mk)
		if err != nil {
			return nil, err
		}
		v := view(ctx, a, c.ID)
		out := &MouthOut{Change: v, Estimate: est, Summary: "Matching the mouth to “" + c.Words + "”: " + lowerFirst(est.Summary)}
		if !est.Allowed && len(est.Problems) > 0 {
			out.Summary += " " + est.Problems[0]
		}
		return out, nil
	}
	// The change knows its lip sync before any worker can finish it (its ended hook checks the generation).
	// updatedAt is the server's (D360): the sweep on any computer looks at a mouth being made by it.
	m := models.SpokenMouth{GenerationID: mk.ID, State: models.SpokenMaking, By: a.Actor, At: time.Now().UTC()}
	if err := a.DB.Update(ctx, store.CSpokenChanges, a.WorkspaceID(), c.ID, bson.M{"$set": bson.M{"mouth": m}, "$currentDate": bson.M{"updatedAt": true}}); err != nil {
		return nil, err
	}
	mo, err := aiwork.MakeFor(ctx, a, &mk)
	if err != nil {
		_ = a.DB.Update(context.WithoutCancel(ctx), store.CSpokenChanges, a.WorkspaceID(), c.ID, bson.M{"$set": bson.M{"mouth": c.Mouth}, "$currentDate": bson.M{"updatedAt": true}}) // nothing was sent
		return nil, err
	}
	m.JobID = mo.JobID
	_ = a.DB.Update(ctx, store.CSpokenChanges, a.WorkspaceID(), c.ID, bson.M{"$set": bson.M{"mouth.jobId": mo.JobID}})
	a.Audit(ctx, "cut.say.mouth", "spoken_change", c.ID, c.ProjectID, map[string]any{"generation": m.GenerationID, "estimateUsd": mo.Estimate.USD, "aiGenerated": true})
	return &MouthOut{Change: view(ctx, a, c.ID), Estimate: &mo.Estimate, JobID: mo.JobID,
		Summary: fmt.Sprintf("Matching the mouth to “%s” (about %s). It plays in place of the picture there once the change is in the cut.", c.Words, costs.Money(mo.Estimate.USD))}, nil
}

// endedMouth records the clip with the mouth matched, or why there is none.
func endedMouth(ctx context.Context, a *app.App, g *models.Generation) {
	id := strings.TrimPrefix(g.Origin, MouthOrigin)
	var c models.SpokenChange
	if err := a.DB.GetByID(ctx, store.CSpokenChanges, a.WorkspaceID(), id, &c); err != nil || c.Mouth == nil || c.Mouth.GenerationID != g.ID {
		return // not this change's own lip sync
	}
	set := bson.M{}
	ok := g.Outcome != nil && g.Outcome.State == models.GenDone
	asset := ""
	if ok {
		for _, o := range g.Outcome.Outputs {
			if o.AssetID != "" {
				asset = o.AssetID
			}
		}
	}
	n := models.Notification{UserID: c.CreatedBy.UserID, GroupKey: "spoken-mouth:" + c.ID, ProjectID: c.ProjectID, JobID: g.JobID, Details: map[string]any{"changeId": c.ID}}
	switch {
	case ok && asset != "":
		set["mouth.state"], set["mouth.assetId"] = models.SpokenReady, asset
		n.Kind, n.Tone = models.NotifyGenerationReady, "success"
		n.Title = fmt.Sprintf("The mouth matches “%s”", generateClip(c.Words))
		n.Body = "It plays in place of the picture there once the change is in the cut."
	default:
		why := "The mouth couldn't be matched."
		if g.Outcome != nil && strings.TrimSpace(g.Outcome.Message) != "" {
			why = strings.TrimSpace(g.Outcome.Message + " " + g.Outcome.Hint)
		}
		set["mouth.state"], set["mouth.why"] = models.SpokenFailed, why
		n.Kind, n.Tone = models.NotifyGenerationFailed, "error"
		n.Title = fmt.Sprintf("The mouth couldn't be matched to “%s”", generateClip(c.Words))
		n.Body = why
	}
	res, err := a.DB.C(store.CSpokenChanges).UpdateOne(ctx, bson.M{"_id": c.ID, "workspaceId": a.WorkspaceID(), "mouth.state": models.SpokenMaking, "mouth.generationId": g.ID}, bson.M{"$set": set, "$currentDate": bson.M{"updatedAt": true}})
	if err != nil || res.MatchedCount == 0 {
		return // moved on meanwhile (withdrawn, or a sweep): left as it is
	}
	if n.UserID != "" && (g.Outcome == nil || g.Outcome.State != models.GenCanceled) {
		_, _ = notify.Upsert(ctx, a, n, notify.Once)
	}
}

func view(ctx context.Context, a *app.App, id string) ops.SpokenView {
	v, err := ops.CallTyped[ops.SpokenView](ctx, a, "cut.say.show", ops.CutSayIDIn{ID: id}, ops.Caller{Human: true})
	if err != nil || v == nil {
		return ops.SpokenView{}
	}
	return *v
}

func generateClip(s string) string { return clipWords(s, 40) }
