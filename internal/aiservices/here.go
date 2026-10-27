package aiservices

import (
	"context"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// Models that run on this computer (D278) are providers like any other: the
// same contract, catalogue, estimate, records and conformance suite. Their
// provider's route is RouteLocal: no key, no money (price tables at $0), and
// nothing leaves this computer. Their adapters need the computer's programs
// and settings, which MetaRush hands each call through the context (WithEnv).

// RouteLocal is a provider whose models run on this computer (D278).
const RouteLocal = "local"

// LocalProvider is the catalogue's provider of models that run on this computer.
const LocalProvider = "local"

// Env is what an adapter that runs on this computer needs for one request:
// the allowlisted programs, the settings, the installation's home (where
// downloaded models live, under components/) and a folder of its own.
type Env struct {
	Tools *tools.Set
	Cfg   *config.Config
	// Home is the installation's home folder.
	Home string
	// Scratch is a folder for this request's files, removed after it.
	Scratch string
	// App is the installation, for an ability that provides a kind of work
	// (D281: it runs through internal/abilities); opaque here.
	App any
}

type envKey struct{}

// WithEnv hands an adapter that runs on this computer what it needs.
func WithEnv(ctx context.Context, e Env) context.Context { return context.WithValue(ctx, envKey{}, e) }

// EnvFrom is the Env a call was given (ok false when none was).
func EnvFrom(ctx context.Context) (Env, bool) {
	e, ok := ctx.Value(envKey{}).(Env)
	return e, ok && e.Tools != nil
}

// Here is an adapter whose models run on this computer: it says whether a
// model can run here now, and if not, what a person does about it.
type Here interface {
	// Ready is nil when the model can run on this computer; otherwise an
	// error in plain words naming the setup step (apperr with a command).
	Ready(e Env, model string) error
}

// IsLocal reports whether a provider's models run on this computer.
func IsLocal(provider string) bool {
	p, err := ProviderOf(provider)
	return err == nil && p.Route == RouteLocal
}

// Local reports whether the provider's models run on this computer.
func (p *ProviderInfo) Local() bool { return p != nil && p.Route == RouteLocal }

// ReadyHere says whether a model can run on this computer: nil for a model
// of a provider elsewhere (its key decides), else its adapter's answer.
func ReadyHere(e Env, provider, kind, model string) error {
	if !RunsHere(provider) {
		return nil
	}
	a, err := Get(provider, kind)
	if err != nil {
		return err
	}
	h, ok := a.(Here)
	if !ok {
		return nil
	}
	return h.Ready(e, model)
}

// localRate is a local model's price in words.
const localRate = "free: it runs on this computer"

// AlternativesWhere is Alternatives with a test per model (one that runs on
// this computer is offered only where it can run).
func AlternativesWhere(chosen *Model, req *Request, usd float64, at time.Time, ok func(m *Model) bool) []Alternative {
	return alternatives(chosen, req, usd, at, ok)
}
