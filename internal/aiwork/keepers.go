package aiwork

import (
	"context"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
)

// Keeper keeps what came back for work another feature asked for (its
// origin's prefix, e.g. "say:" for new words in a speaker's own voice, D310)
// instead of the usual clip in Sources: path is the downloaded file (removed
// after the call), mime what it is. It returns the output the generation
// records (a clip it made, or the stored file alone); an error fails the work
// like any answer that couldn't be used.
type Keeper func(ctx context.Context, a *app.App, g *models.Generation, path, mime string) (*models.GenOutput, error)

// Ended hears how work another feature asked for ended (done, failed,
// refused or stopped), after it is settled; the feature tells the person
// (MetaRush's own "ready" note isn't sent for such work).
type Ended func(ctx context.Context, a *app.App, g *models.Generation)

var (
	keepersMu sync.RWMutex
	keepers   = map[string]Keeper{}
	enders    = map[string]Ended{}
)

var sweepers []func(ctx context.Context, a *app.App) int

// RegisterSweep adds a feature's own sweep to AI work's (the daemon's, when it
// starts and every five minutes): it closes what the feature's stopped or
// lost work left behind and says how many.
func RegisterSweep(fn func(ctx context.Context, a *app.App) int) {
	keepersMu.Lock()
	defer keepersMu.Unlock()
	sweepers = append(sweepers, fn)
}

func sweepFeatures(ctx context.Context, a *app.App) int {
	keepersMu.RLock()
	fns := append([]func(context.Context, *app.App) int(nil), sweepers...)
	keepersMu.RUnlock()
	n := 0
	for _, fn := range fns {
		n += fn(ctx, a)
	}
	return n
}

// RegisterKeeper hands results of work whose origin starts with prefix to
// keep (nil: kept the usual way), and tells ended how it ended.
func RegisterKeeper(prefix string, keep Keeper, ended Ended) {
	keepersMu.Lock()
	defer keepersMu.Unlock()
	keepers[prefix] = keep
	enders[prefix] = ended
}

func endedFor(origin string) Ended {
	if origin == "" {
		return nil
	}
	keepersMu.RLock()
	defer keepersMu.RUnlock()
	for p, fn := range enders {
		if strings.HasPrefix(origin, p) {
			return fn
		}
	}
	return nil
}

func keeperFor(origin string) Keeper {
	if origin == "" {
		return nil
	}
	keepersMu.RLock()
	defer keepersMu.RUnlock()
	for p, fn := range keepers {
		if fn != nil && strings.HasPrefix(origin, p) {
			return fn
		}
	}
	return nil
}

// keptOrigin is the origin a generation record keeps: only a registered
// feature's, asked for from Go (MakeIn.Feature; a panel card's origin stays
// the channel's business).
func keptOrigin(origin string) string {
	if keeperFor(origin) == nil && endedFor(origin) == nil {
		return ""
	}
	return origin
}

// EstimateFor prices and checks AI work for another feature, as ai.estimate
// does (quietly: a refusal is said, not recorded), with the Go-only fields
// (Use) honoured.
func EstimateFor(ctx context.Context, a *app.App, in *MakeIn) (*AIEstimate, error) {
	p, err := planMake(leave.Quiet(ctx), a, in)
	if err != nil {
		return nil, err
	}
	e := p.estimate(ctx, a)
	return &e, nil
}

// MakeFor asks for AI work for another feature, as ai.make does: priced first
// (maxUsd, unless the asker's estimates are off), consent, budgets, rules and
// What may leave all apply. A registered origin's result goes to its Keeper.
func MakeFor(ctx context.Context, a *app.App, in *MakeIn) (*MakeOut, error) {
	pctx := ctx
	if in.DryRun {
		pctx = leave.Quiet(ctx)
	}
	p, err := planMake(pctx, a, in)
	if err != nil {
		return nil, err
	}
	return start(ctx, a, p, in.MaxUSD, in.DryRun, in.Origin)
}
