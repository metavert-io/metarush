package ops

import (
	"context"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
)

// What MetaRush knows about a source changed (D150, D152): a first analysis
// finished, someone named a voice or a face, a fix switched its media.
// internal/sources refreshes its descriptive name and internal/fixes its
// measurements; they register here because they build on this package.

// Why a source's facts changed (SourceFactsChanged's why).
const (
	FactsIngested = "ingested" // the file was added
	FactsAnalysis = "analysis" // an analysis job finished (preview copy, transcript, voices, on screen, evidence)
	FactsPeople   = "people"   // someone named (or unnamed) a voice or a face
	FactsFixed    = "fixed"    // a fix switched the media it uses
)

var (
	factsMu    sync.Mutex
	factsHooks []func(ctx context.Context, a *app.App, assetID, why string)
)

// OnSourceFacts registers a function told when a source's facts change.
func OnSourceFacts(f func(ctx context.Context, a *app.App, assetID, why string)) {
	factsMu.Lock()
	defer factsMu.Unlock()
	factsHooks = append(factsHooks, f)
}

// SourceFactsChanged tells the registered functions; best effort (they log
// nothing to the caller and never fail it).
func SourceFactsChanged(ctx context.Context, a *app.App, assetID, why string) {
	if assetID == "" || a == nil {
		return
	}
	factsMu.Lock()
	hooks := append([]func(context.Context, *app.App, string, string){}, factsHooks...)
	factsMu.Unlock()
	ctx = context.WithoutCancel(ctx)
	for _, f := range hooks {
		f(ctx, a, assetID, why)
	}
}
