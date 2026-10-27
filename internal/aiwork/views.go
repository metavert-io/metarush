package aiwork

import (
	"context"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

func loadGen(ctx context.Context, a *app.App, id string) (*models.Generation, error) {
	var g models.Generation
	if err := a.DB.GetByID(ctx, store.CGenerations, a.WorkspaceID(), id, &g); err != nil {
		return nil, apperr.New(apperr.NotFound, "That AI work isn't in this workspace.").WithDetails(map[string]any{"id": id})
	}
	return &g, nil
}

// resolve finds AI work by its ID, or by a clip or picture it made (video
// has its own: metarush generate show).
func resolve(ctx context.Context, a *app.App, id string) (*models.Generation, error) {
	id = strings.TrimSpace(id)
	var g models.Generation
	var err error
	switch {
	case ids.Valid(ids.Generation, id):
		p, e := loadGen(ctx, a, id)
		if e != nil {
			return nil, e
		}
		g, err = *p, nil
	case ids.Valid(ids.Asset, id):
		err = a.DB.C(store.CGenerations).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "outcome.outputs.assetId": id, "kind": bson.M{"$nin": append(models.VideoKinds, aiservices.KindTranscription)}}).Decode(&g)
	case strings.HasPrefix(id, "res_"):
		err = a.DB.C(store.CGenerations).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "outcome.outputs.resourceId": id}).Decode(&g)
	default:
		return nil, apperr.New(apperr.InvalidInput, "Say which AI work: its record, or a clip or picture it made.").WithDetails(map[string]any{"id": id})
	}
	if err != nil {
		return nil, apperr.New(apperr.NotFound, "Nothing in this workspace was made by that AI work.").WithDetails(map[string]any{"id": id})
	}
	if g.KindOf() == models.GenKindVideo {
		return nil, apperr.New(apperr.NotFound, "That is a generated video: it has its own details.").WithCommand("metarush generate show " + g.ID)
	}
	return &g, nil
}

func stateOf(g *models.Generation, j *models.Job) string {
	switch {
	case g.Outcome != nil:
		return g.Outcome.State
	case j != nil && j.State == models.JobCanceled:
		return StateCanceled
	case j != nil && j.State == models.JobFailed:
		return StateFailed
	case g.Ticket != nil:
		return StateMaking
	case len(g.Sending) > len(g.Sends):
		return StateSending
	}
	return StateWaiting
}

func costOf(g *models.Generation) float64 {
	switch {
	case g.Settled != nil:
		return g.Settled.USD
	case g.Outcome != nil && g.Outcome.Actual != nil:
		return g.Outcome.Actual.USD
	}
	return g.Estimate.USD
}

// voiceName is a voice copy's name.
func voiceName(g *models.Generation) string {
	if g.Options != nil {
		return g.Options.Name
	}
	return ""
}

func kindWhat(kind string) string {
	if k, err := aiservices.KindOf(kind); err == nil {
		return k.What
	}
	return "AI work"
}

func summaryOf(g *models.Generation, state string, outs []WorkOutput) string {
	who, thing := g.ProviderTitle, kindWhat(g.Kind)
	switch state {
	case StateWaiting:
		return upperFirst(thing) + ": waiting to be sent to " + who + "."
	case StateSending:
		return upperFirst(thing) + ": being sent to " + who + "."
	case StateMaking:
		return upperFirst(thing) + ": " + who + " is making it."
	case StateDone:
		if len(outs) > 0 && outs[0].Title != "" {
			return fmt.Sprintf("Ready: “%s”.", outs[0].Title)
		}
		return upperFirst(thing) + " is ready."
	case StateRefused:
		return who + " declined " + thing + ": its safety filters blocked it."
	case StateCanceled:
		return upperFirst(thing) + " was stopped."
	}
	if g.Outcome != nil && g.Outcome.Message != "" {
		return g.Outcome.Message
	}
	return upperFirst(thing) + " couldn't be made."
}

// views builds views of AI work, with what it made and its jobs.
func views(ctx context.Context, a *app.App, gs []models.Generation) []WorkView {
	jobIDs, assetIDs, resIDs := bson.A{}, bson.A{}, bson.A{}
	for _, g := range gs {
		if g.JobID != "" {
			jobIDs = append(jobIDs, g.JobID)
		}
		if g.Outcome != nil {
			for _, o := range g.Outcome.Outputs {
				if o.AssetID != "" {
					assetIDs = append(assetIDs, o.AssetID)
				}
				if o.ResourceID != "" {
					resIDs = append(resIDs, o.ResourceID)
				}
			}
		}
	}
	jobsBy := map[string]*models.Job{}
	if len(jobIDs) > 0 {
		var js []models.Job
		_ = a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": jobIDs}}, &js)
		for i := range js {
			jobsBy[js[i].ID] = &js[i]
		}
	}
	assetsBy := map[string]*models.Asset{}
	if len(assetIDs) > 0 {
		var as []models.Asset
		_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": assetIDs}}, &as, options.Find().SetProjection(bson.M{"name": 1, "title": 1, "media": 1, "projectIds": 1}))
		for i := range as {
			assetsBy[as[i].ID] = &as[i]
		}
	}
	resBy := map[string]*models.Resource{}
	if len(resIDs) > 0 {
		var rs []models.Resource
		_ = a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": resIDs}}, &rs, options.Find().SetProjection(bson.M{"name": 1, "title": 1, "projectId": 1}))
		for i := range rs {
			resBy[rs[i].ID] = &rs[i]
		}
	}
	revoked := generate.RevokedConsents(ctx, a, gs)
	out := make([]WorkView, 0, len(gs))
	for _, g := range gs {
		j := jobsBy[g.JobID]
		v := WorkView{Generation: g, Outputs: []WorkOutput{}, CostUSD: costOf(&g), KindTitle: kindTitleOf(g.Kind)}
		v.State = stateOf(&g, j)
		if j != nil {
			v.Job = &generate.GenJobBrief{ID: j.ID, State: j.State, Progress: j.Progress}
		}
		if g.Outcome != nil {
			for _, o := range g.Outcome.Outputs {
				switch {
				case o.ResourceID != "":
					w := WorkOutput{Kind: "picture", ID: o.ResourceID, Title: "AI picture", Link: links.Resource(g.ProjectID, o.ResourceID)}
					if r := resBy[o.ResourceID]; r != nil {
						w.Title = firstNonEmpty(r.Title, r.Name)
					}
					v.Outputs = append(v.Outputs, w)
				case o.PresenterID != "":
					w := WorkOutput{Kind: "presenter", ID: o.PresenterID, Title: "A presenter"}
					var pr models.Presenter
					if a.DB.C(store.CPresenters).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "trainingId": g.ID}).Decode(&pr) == nil {
						w.ID, w.Title, w.Link = pr.ID, pr.Title, links.Person(pr.PersonID)
					}
					v.Outputs = append(v.Outputs, w)
				case o.VoiceID != "":
					v.Outputs = append(v.Outputs, WorkOutput{Kind: "voice", ID: o.VoiceID, Title: firstNonEmpty(voiceName(&g), "Voice copy")})
				case g.Kind == aiservices.KindTranscription:
					w := WorkOutput{Kind: "transcript", ID: o.AssetID, Title: fmt.Sprintf("%d words", o.Words), Link: links.Asset(g.ProjectID, o.AssetID)}
					if as := assetsBy[o.AssetID]; as != nil {
						w.Title = fmt.Sprintf("The words of “%s”", as.DisplayTitle())
					}
					v.Outputs = append(v.Outputs, w)
				case o.AssetID != "":
					w := WorkOutput{Kind: "clip", ID: o.AssetID, Title: "AI sound", Duration: o.Duration, Link: links.Asset(g.ProjectID, o.AssetID)}
					if as := assetsBy[o.AssetID]; as != nil {
						w.Title = as.DisplayTitle()
						if len(as.ProjectIDs) > 0 {
							w.Link = links.Asset(as.ProjectIDs[0], o.AssetID)
						}
					}
					v.Outputs = append(v.Outputs, w)
				}
			}
		}
		for _, id := range g.ConsentIDs {
			if revoked[id] {
				v.ConsentRevoked = true
			}
		}
		v.Summary = summaryOf(&g, v.State, v.Outputs)
		out = append(out, v)
	}
	return out
}

func kindTitleOf(kind string) string {
	if k, err := aiservices.KindOf(kind); err == nil {
		return k.Title
	}
	return kind
}

func listWork(ctx context.Context, a *app.App, in *ListIn) (*ListOut, error) {
	filter := bson.M{"kind": bson.M{"$nin": models.VideoKinds}}
	if in.Kind != "" {
		if _, err := aiservices.KindOf(in.Kind); err != nil {
			return nil, err
		}
		filter["kind"] = in.Kind
	}
	if in.ProjectID != "" {
		filter["projectId"] = in.ProjectID
	}
	limit := int64(in.Limit)
	if limit <= 0 {
		limit = 100
	}
	var gs []models.Generation
	if err := a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), filter, &gs, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(limit)); err != nil {
		return nil, err
	}
	out := &ListOut{Work: views(ctx, a, gs)}
	done := 0
	for _, v := range out.Work {
		if v.Settled != nil {
			out.SpentUSD += v.Settled.USD
		}
		if v.State == StateDone {
			done++
		}
	}
	out.SpentUSD = costs.Round(out.SpentUSD)
	if len(gs) == 0 {
		out.Summary = "No AI work yet."
	} else {
		out.Summary = fmt.Sprintf("%s, %d ready, %s spent.", count(len(gs), "piece of AI work", "pieces of AI work"), done, costs.Money(out.SpentUSD))
	}
	return out, nil
}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func cancelWork(ctx context.Context, a *app.App, id string) (*CancelOut, error) {
	g, err := resolve(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if g.Outcome != nil {
		v := views(ctx, a, []models.Generation{*g})[0]
		return &CancelOut{Work: v, Summary: "It has already ended: " + strings.ToLower(v.Summary[:1]) + v.Summary[1:]}, nil
	}
	j, err := a.Queue.RequestCancel(ctx, g.JobID)
	if err != nil {
		return nil, err
	}
	summary := "Asked it to stop. MetaRush tells " + g.ProviderTitle + " to stop too."
	if j.State == models.JobCanceled && g.Ticket == nil {
		r := &runner{a: a, g: g, job: j}
		r.end(ctx, &models.GenOutcome{State: models.GenCanceled, Message: "It was stopped before it was sent. Nothing was spent."}, &models.GenCost{Basis: "none"})
		_ = generate.Settle(ctx, a, r.g, 0, "")
		summary = "Stopped before it was sent. Nothing was spent."
	}
	a.Audit(ctx, "ai.cancel", "generation", g.ID, g.ProjectID, map[string]any{"job": g.JobID, "kind": g.Kind})
	g, _ = loadGen(ctx, a, g.ID)
	return &CancelOut{Work: views(ctx, a, []models.Generation{*g})[0], Summary: summary}, nil
}
