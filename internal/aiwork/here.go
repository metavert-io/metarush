package aiwork

import (
	"context"
	"path/filepath"
	"slices"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
)

// Models that run on this computer (D278) go through the same lifecycle as
// every other provider's: chosen in the open, recorded, kept with how they
// were made. What differs is said here: they need no key and no money, so
// they need no price agreed; they send nothing anywhere, so Content Settings
// that allow no AI services still allow them (unless they name them); and
// whether one can run is this computer's business, not a key's.

// allows is Content Settings' word on a provider (D225), with models on
// this computer (and abilities whose network is off, D281) allowed under "no
// AI services": nothing of the client's leaves. Naming them in the forbidden
// list still forbids them.
func allows(r *models.AIServiceRules, provider string) bool {
	if r == nil {
		return true
	}
	if r.None && aiservices.NothingLeaves(provider) {
		return !slices.Contains(r.Forbid, provider)
	}
	return r.Allows(provider) && ruledOutVia(r, provider) == ""
}

// ruledOutVia is a service the settings rule out that an ability reaches
// (its key, or its hosts): ruling ElevenLabs out rules out an ability that
// calls ElevenLabs too; an open network reaches any service, so any rule
// out rules it out (D281). "" when none.
func ruledOutVia(r *models.AIServiceRules, provider string) string {
	if r == nil || len(r.Forbid) == 0 {
		return ""
	}
	p, err := aiservices.ProviderOf(provider)
	if err != nil {
		return ""
	}
	for _, x := range p.Reaches {
		if x == "*" {
			return "*"
		}
		if slices.Contains(r.Forbid, x) {
			return x
		}
	}
	return ""
}

// envFor is this computer for an adapter whose models run here, with a
// folder of its own for the call.
func envFor(a *app.App) (aiservices.Env, func(), error) {
	p, done, err := generate.Scratch(a, "local")
	if err != nil {
		return aiservices.Env{}, nil, err
	}
	return aiservices.Env{Tools: a.Tools, Cfg: a.Cfg, Home: a.Cfg.Home(), Scratch: filepath.Dir(p), App: a}, done, nil
}

// readyHere says whether a model can run on this computer (nil for a
// provider elsewhere: its key decides).
func readyHere(a *app.App, provider, kind, model string) error {
	if !aiservices.RunsHere(provider) {
		return nil
	}
	return aiservices.ReadyHere(aiservices.Env{Tools: a.Tools, Cfg: a.Cfg, Home: a.Cfg.Home(), App: a}, provider, kind, model)
}

// withEnv hands an adapter whose work runs here (a model on this computer,
// or an ability in the sandbox, D281) this computer for one call; done
// removes the call's folder. Calls to providers elsewhere are left as they are.
func withEnv(ctx context.Context, a *app.App, provider string) (context.Context, func(), error) {
	if !aiservices.RunsHere(provider) {
		return ctx, func() {}, nil
	}
	e, done, err := envFor(a)
	if err != nil {
		return ctx, nil, err
	}
	return aiservices.WithEnv(ctx, e), done, nil
}

// apperrMessage is an error's plain words, with its hint.
func apperrMessage(err error) string {
	ae := apperr.From(err)
	if ae.Hint != "" {
		return ae.Message + " " + ae.Hint
	}
	return ae.Message
}

// onThisComputer reports whether a record's work ran on this computer.
func onThisComputer(g *models.Generation) bool { return aiservices.IsLocal(g.Provider) }

// byAbility reports whether a record's work was done by an ability (D281).
func byAbility(g *models.Generation) bool {
	p, err := aiservices.ProviderOf(g.Provider)
	return err == nil && p.Route == aiservices.RouteAbility
}
