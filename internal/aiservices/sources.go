package aiservices

import (
	"context"
	"sync"
)

// Abilities that provide a kind of work (D281) are providers whose models
// live in a workspace's data, not in catalog.json: an approved ability that
// declares `provides: [<kind>]`. Their provider's route is RouteAbility —
// the ability runs on this computer in MetaRush's sandbox, reaching only
// its declared hosts through MetaRush's proxy with its own keys — and a
// Source puts them into the catalogue and the registry before work is
// planned, listed or run.

// RouteAbility is a provider that is an approved ability (D281).
const RouteAbility = "ability"

// Source adds providers kept in a workspace's data (the app is opaque here:
// internal/abilities knows its own).
type Source func(ctx context.Context, app any) error

var (
	sourcesMu sync.Mutex
	sources   []Source
)

// RegisterSource adds a source (from init).
func RegisterSource(s Source) {
	sourcesMu.Lock()
	defer sourcesMu.Unlock()
	sources = append(sources, s)
}

// Refresh asks every source to bring its providers up to date.
func Refresh(ctx context.Context, app any) {
	sourcesMu.Lock()
	ss := append([]Source(nil), sources...)
	sourcesMu.Unlock()
	for _, s := range ss {
		_ = s(ctx, app)
	}
}

// Versioned is an adapter whose model can change under the same name (an
// ability's newest approved version, D281): the plan records the version and
// the run refuses another.
type Versioned interface {
	Version(model string) string
}

// VersionOf is the version of an adapter's model ("" when it has none).
func VersionOf(ad Adapter, model string) string {
	if v, ok := ad.(Versioned); ok {
		return v.Version(model)
	}
	return ""
}

// PersonOnly is an adapter some of whose models only a person may start
// (an ability that sends pictures or sound away, uses a key or costs money,
// D234): Why is the reason, "" when anyone may.
type PersonOnly interface {
	PersonOnly(model string) string
}

// RunsHere reports whether a provider's work runs on this computer (a
// model here, or an ability in the sandbox): it needs no key of MetaRush's
// own and is handed the computer for each call.
func RunsHere(provider string) bool {
	p, err := ProviderOf(provider)
	return err == nil && (p.Route == RouteLocal || p.Route == RouteAbility)
}

// NothingLeaves reports whether nothing sent to a provider leaves this
// computer: a model here (D278), or an ability whose network is off (D281).
func NothingLeaves(provider string) bool {
	p, err := ProviderOf(provider)
	return err == nil && (p.Route == RouteLocal || p.Stays)
}

// ReplaceProvider puts a provider into the catalogue in place of one a
// source added before (never one from catalog.json).
func ReplaceProvider(p ProviderInfo) (remove func(), err error) {
	catMu.Lock()
	if added[p.Name] {
		out := catalog.Providers[:0]
		for _, q := range catalog.Providers {
			if q.Name != p.Name {
				out = append(out, q)
			}
		}
		catalog.Providers = out
		delete(added, p.Name)
	}
	catMu.Unlock()
	return AddProvider(p)
}

// AddedProviders are the providers sources and tests put in while the
// program runs.
func AddedProviders() []string {
	catMu.RLock()
	defer catMu.RUnlock()
	var out []string
	for n := range added {
		out = append(out, n)
	}
	return out
}
