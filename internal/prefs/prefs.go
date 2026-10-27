// Package prefs is a person's own preferences, Settings → Preferences (D208):
// "Estimate costs of outside AI services before edits" (on by default) and
// the owners' "require estimates for everyone", beside the person's Ask first
// or Auto choice (D186, users.assistantMode) and light or dark (kept in each
// browser).
//
// EstimatesFirst is the hook everything that spends money on an outside AI
// service asks before it does: generated shots, the AI voice, cloud voice
// clean-up, Claude's priced cards under Auto, and Recipes runs (the run's
// estimate agreement). On (the
// default), the person sees the estimate and agrees to it first. Off, paid
// steps run within budgets, daily limits and project caps without the
// estimate step; consent, approvals and people-only actions don't change.
package prefs

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Estimates is where a person's estimate preference comes from.
type Estimates struct {
	// On: edits that use paid outside AI services show their estimate and
	// wait for the person's OK first.
	On bool
	// Theirs is the person's own choice (true unless they turned it off).
	Theirs bool
	// Required: an owner asks for estimates for everyone.
	Required bool
	// RequiredBy names who did, and when.
	RequiredBy string
	RequiredAt *time.Time
}

// readTimeout keeps a slow database from holding up a price check: when the
// preference can't be read, estimates come first (the safe default).
const readTimeout = 3 * time.Second

// EstimatesFor reads userID's estimate preference and the owners' word.
func EstimatesFor(ctx context.Context, a *app.App, userID string) Estimates {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	defer cancel()
	out := Estimates{On: true, Theirs: true}
	if userID != "" {
		var u struct {
			Preferences *models.UserPreferences `bson:"preferences"`
		}
		err := a.DB.C(store.CUsers).FindOne(ctx, bson.M{"_id": userID, "workspaceId": a.WorkspaceID()},
			options.FindOne().SetProjection(bson.M{"preferences": 1})).Decode(&u)
		if err == nil && u.Preferences != nil && u.Preferences.EstimatesOff {
			out.Theirs = false
		}
	}
	if s := workspaceEstimates(ctx, a); s != nil && s.Required {
		out.Required, out.RequiredBy = true, s.UpdatedBy.UserName
		at := s.UpdatedAt
		out.RequiredAt = &at
	}
	out.On = out.Theirs || out.Required
	return out
}

// EstimatesFirst reports whether edits that use paid outside AI services
// show userID their estimate and wait for their OK before anything is sent
// (D208): true unless they turned "Estimate costs of outside AI services
// before edits" off and no owner requires estimates for everyone. This is
// the hook generated shots, the AI voice, cloud voice clean-up, Claude's
// priced cards under Auto and Recipes runs call; false means a paid step runs
// within budgets, daily limits and project caps without the estimate step.
func EstimatesFirst(ctx context.Context, a *app.App, userID string) bool {
	return EstimatesFor(ctx, a, userID).On
}

// Asker is the person behind a call: the signed-in person (or the card's,
// for steps that run by themselves), else this computer's own user.
func Asker(a *app.App) string {
	if a.Actor.UserID != "" {
		return a.Actor.UserID
	}
	return a.Cfg.User.ID
}

// EstimatesFirstForAsker is EstimatesFirst for the person behind a call.
func EstimatesFirstForAsker(ctx context.Context, a *app.App) bool {
	return EstimatesFirst(ctx, a, Asker(a))
}

func workspaceEstimates(ctx context.Context, a *app.App) *models.EstimateSettings {
	var ws struct {
		Estimates *models.EstimateSettings `bson:"estimates"`
	}
	err := a.DB.C(store.CWorkspaces).FindOne(ctx, bson.M{"_id": a.WorkspaceID()}, options.FindOne().SetProjection(bson.M{"estimates": 1})).Decode(&ws)
	if err != nil {
		return nil
	}
	return ws.Estimates
}

// SetEstimates turns a person's own estimate preference on or off.
func SetEstimates(ctx context.Context, a *app.App, userID string, on bool) error {
	if userID == "" {
		return apperr.New(apperr.Unauthorized, "MetaRush doesn't know who you are, so it can't keep your choice.")
	}
	now := time.Now().UTC()
	set := bson.M{"preferences.updatedAt": now, "updatedAt": now}
	var update bson.M
	if on {
		update = bson.M{"$set": set, "$unset": bson.M{"preferences.estimatesOff": ""}}
	} else {
		set["preferences.estimatesOff"] = true
		update = bson.M{"$set": set}
	}
	if err := a.DB.Update(ctx, store.CUsers, a.WorkspaceID(), userID, update); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return apperr.New(apperr.NotFound, "MetaRush can't find your account, so it can't keep your choice.").WithDetails(map[string]any{"userId": userID})
		}
		return apperr.Wrap(apperr.Database, err, "MetaRush couldn't save your choice.")
	}
	return nil
}

// RequireEstimates is the owners' choice for everyone (owner-only: the caller
// checks ops.RequireOwner first).
func RequireEstimates(ctx context.Context, a *app.App, required bool) error {
	set := models.EstimateSettings{Required: required, UpdatedAt: time.Now().UTC(), UpdatedBy: a.Actor}
	if err := a.DB.Update(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), bson.M{"$set": bson.M{"estimates": set}}); err != nil {
		return apperr.Wrap(apperr.Database, err, "MetaRush couldn't save the choice for everyone.")
	}
	return nil
}

// assistantMode is a person's Ask first or Auto choice (D186), as the
// assistant reads it: Auto only when chosen and this computer lets steps run
// by themselves (the owner's master switch).
func assistantMode(ctx context.Context, a *app.App, userID string) (mode string, offered bool) {
	offered = a.Cfg.Assistant.RunsSafeSteps()
	if userID == "" || !offered {
		return "ask", offered
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readTimeout)
	defer cancel()
	var u struct {
		Mode string `bson:"assistantMode"`
	}
	err := a.DB.C(store.CUsers).FindOne(ctx, bson.M{"_id": userID, "workspaceId": a.WorkspaceID()},
		options.FindOne().SetProjection(bson.M{"assistantMode": 1})).Decode(&u)
	if err != nil || u.Mode != "auto" {
		return "ask", offered
	}
	return "auto", offered
}

// ownerCaller reports whether the caller may change the owners' part.
func ownerCaller(ctx context.Context) bool { return ops.IsOwnerCaller(ctx) }
