package prefs

import (
	"context"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// PreferencesIn reads the asking person's preferences.
type PreferencesIn struct{}

// PreferencesSetIn changes them: your own estimate preference, and (owners)
// whether everyone sees estimates first. People only.
type PreferencesSetIn struct {
	Estimates *bool `json:"estimates,omitempty" jsonschema:"description=Estimate costs of outside AI services before edits (true\\, the default) or let paid steps run within your budgets and limits without the estimate step (false)."`
	// RequireEstimates is the owners' choice for everyone.
	RequireEstimates *bool `json:"requireEstimates,omitempty" jsonschema:"description=Owners: everyone sees the estimate before edits that use paid outside AI services\\, whatever their own preference says (true)\\, or each person chooses (false)."`
}

// PreferencesOut is a person's preferences and where each comes from.
type PreferencesOut struct {
	// Estimates: edits that use paid outside AI services show their estimate
	// and wait for your OK first (your choice, or an owner's for everyone).
	Estimates bool `json:"estimates" jsonschema:"description=Edits that use paid outside AI services show their estimate and wait for your OK first."`
	// EstimatesYours is your own choice.
	EstimatesYours bool `json:"estimatesYours" jsonschema:"description=Your own choice (true unless you turned it off)."`
	// EstimatesRequired: an owner asks for estimates for everyone.
	EstimatesRequired bool       `json:"estimatesRequired" jsonschema:"description=An owner asks for estimates for everyone: your own choice waits until they stop."`
	RequiredBy        string     `json:"requiredBy,omitempty" jsonschema:"description=The owner who asked for estimates for everyone."`
	RequiredAt        *time.Time `json:"requiredAt,omitempty"`
	// AssistantMode is your Ask first or Auto choice (D186; chosen with assistant.mode.set).
	AssistantMode string `json:"assistantMode" jsonschema:"enum=ask,enum=auto,description=How Claude's changes for you run: ask (wait for your OK) or auto (changes that can be undone run by themselves)."`
	// AutoOffered: this computer lets Claude's steps run by themselves.
	AutoOffered bool `json:"autoOffered" jsonschema:"description=Auto can be chosen: this computer lets Claude's steps run by themselves (Settings → Claude Code)."`
	// Owner: you may require estimates for everyone.
	Owner   bool   `json:"owner" jsonschema:"description=You are an owner: you may ask for estimates for everyone."`
	Summary string `json:"summary" jsonschema:"description=Your preferences in plain words."`
}

func init() {
	ops.Def("account.preferences", "Show your preferences: whether costs of outside AI services are estimated before edits, Ask first or Auto, and whether an owner asks for estimates for everyone", ops.Flags{},
		func(ctx context.Context, a *app.App, _ *PreferencesIn) (*PreferencesOut, error) {
			return out(ctx, a), nil
		})
	ops.Def("account.preferences.set", "Choose whether costs of outside AI services are estimated before your edits (on by default); owners can ask for estimates for everyone. People only", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *PreferencesSetIn) (*PreferencesOut, error) {
			if in.Estimates == nil && in.RequireEstimates == nil {
				return nil, apperr.New(apperr.InvalidInput, "Say what to change: your estimates (on or off), or, for owners, estimates for everyone.")
			}
			if in.RequireEstimates != nil {
				// The workspace's part is the owners' (D191); your own choice stays yours.
				if err := ops.RequireOwner(ctx, a, "account.preferences.set"); err != nil {
					return nil, err
				}
			}
			uid := Asker(a)
			if in.Estimates != nil {
				if err := SetEstimates(ctx, a, uid, *in.Estimates); err != nil {
					return nil, err
				}
				a.Audit(ctx, "account.preferences", "user", uid, "", map[string]any{"estimates": *in.Estimates})
			}
			if in.RequireEstimates != nil {
				if err := RequireEstimates(ctx, a, *in.RequireEstimates); err != nil {
					return nil, err
				}
				a.Audit(ctx, "workspace.estimates", "workspace", a.WorkspaceID(), "", map[string]any{"required": *in.RequireEstimates})
			}
			return out(ctx, a), nil
		})
}

func out(ctx context.Context, a *app.App) *PreferencesOut {
	uid := Asker(a)
	e := EstimatesFor(ctx, a, uid)
	mode, offered := assistantMode(ctx, a, uid)
	o := &PreferencesOut{Estimates: e.On, EstimatesYours: e.Theirs, EstimatesRequired: e.Required, RequiredBy: e.RequiredBy, RequiredAt: e.RequiredAt,
		AssistantMode: mode, AutoOffered: offered, Owner: ownerCaller(ctx)}
	o.Summary = Words(e)
	return o
}

// Words says the estimate preference in plain words.
func Words(e Estimates) string {
	switch {
	case e.Required && !e.Theirs:
		return "An owner asks for estimates for everyone, so edits that use paid outside AI services show what they cost and wait for your OK first. Your own choice (off) counts again if they stop."
	case e.Required:
		return "Estimates come first, and an owner asks for them for everyone: edits that use paid outside AI services show what they cost and wait for your OK."
	case e.On:
		return "Estimates come first: edits that use paid outside AI services show what they cost and wait for your OK."
	}
	return "Estimates are off for you: edits that use paid outside AI services run within your budgets and daily limits without asking about the price first. Consent, approvals and people-only steps still ask."
}
