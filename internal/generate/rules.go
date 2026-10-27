package generate

import (
	"context"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/videogen"
)

// RulesAllow checks the Content Settings of every project a request touches
// (D225): one that keeps its work away from the provider refuses it, whichever
// project the request was made from. "" is footage or a picture in no
// project: the default's Content Settings apply.
func RulesAllow(ctx context.Context, a *app.App, provider string, projects []string) error {
	seen := map[string]bool{}
	for _, pid := range projects {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if err := content.AIServiceAllowed(ctx, a, pid, provider); err != nil {
			return err
		}
	}
	return nil
}

// TouchedBy are the projects a request touches now: the one it is for, those
// holding the clips it sends (frames, parts, a look's stills and clips) and
// the owners of the pictures it sends.
func TouchedBy(ctx context.Context, a *app.App, projectID string, refs []models.GenReference) []string {
	out := []string{projectID}
	for _, ref := range refs {
		if ref.AssetID != "" {
			var as models.Asset
			if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ref.AssetID, &as) == nil {
				if len(as.ProjectIDs) == 0 {
					out = append(out, "")
				}
				out = append(out, as.ProjectIDs...)
			}
		}
		if ref.ResourceID != "" {
			var r models.Resource
			if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), ref.ResourceID, &r) == nil {
				out = append(out, r.ProjectID)
			}
		}
	}
	return out
}

// ConsentsHold reports whether every consent a request relies on is still in
// force for its project (D126, D227).
func ConsentsHold(ctx context.Context, a *app.App, g *models.Generation) error {
	now := time.Now().UTC()
	for _, id := range g.ConsentIDs {
		c, err := loadConsent(ctx, a, id)
		if err != nil && !ConsentGone(err) {
			return err // couldn't read it just now: checked again later, not a refusal
		}
		if err != nil {
			return apperr.New(apperr.ConsentRequired, "A consent it relied on is gone.")
		}
		if !c.Active(now) || (g.ProjectID != "" && !c.Covers(g.ProjectID, now)) {
			return apperr.New(apperr.ConsentRequired, "%s's consent, which this relied on, was withdrawn or has ended.", c.PersonName).
				WithHint("Record their consent again on the People page if they agree, then ask again.")
		}
	}
	return nil
}

// stillAllowed checks again, just before a video request is sent, what its
// plan checked: the Content Settings of every project it touches and the
// consents it relies on. What changed while it waited stops it at $0.
func stillAllowed(ctx context.Context, a *app.App, g *models.Generation) error {
	touched := TouchedBy(ctx, a, g.ProjectID, g.References)
	if err := RulesAllow(ctx, a, g.Provider, touched); err != nil {
		return err
	}
	if err := leave.CheckGeneration(ctx, a, g, touched); err != nil { // D290
		return err
	}
	return ConsentsHold(ctx, a, g)
}

// mayLeave asks the What may leave part of every project a shot touches
// (D290) whether its words and pictures may go to the video service.
func mayLeave(ctx context.Context, a *app.App, p *plan) error {
	provider := firstNonEmpty(p.provider, Provider) // the shot's own: Gemini, or Vertex AI (D282)
	title := provider
	if info, err := videogen.Info(provider); err == nil && info.Title != "" {
		title = info.Title
	}
	kinds := leave.GenerationKinds(&models.Generation{Prompt: p.prompt, References: p.refs})
	return leave.Check(ctx, a, leave.AI(provider, title, kinds, TouchedBy(ctx, a, p.projectID, p.refs), "a generated shot"))
}
