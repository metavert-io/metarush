package components

import (
	"context"
	"sync"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/localmodels"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

// StepFunc sets a component up the way MetaRush does it itself; say hears
// each stage in plain words.
type StepFunc func(ctx context.Context, e *Env, say func(string)) error

// steps are what MetaRush sets up itself.
var steps = map[string]StepFunc{
	"renderer": func(ctx context.Context, e *Env, say func(string)) error {
		last := ""
		_, err := renderer.New(e.Cfg, e.Tools).Install(ctx, renderer.InstallOptions{Progress: func(s renderer.Step) {
			if s.Message != last {
				last = s.Message
				say(s.Message)
			}
		}})
		return err
	},
	"vision": func(ctx context.Context, e *Env, say func(string)) error {
		say("Building the helper that looks at your clips (a few seconds)…")
		_, _, err := vision.Ensure(ctx, e.Tools, e.Home())
		return err
	},
	"faces": func(ctx context.Context, e *Env, say func(string)) error {
		say("Downloading the face model (about 40 MB)…")
		_, err := faceid.Setup{Home: e.Home()}.Ensure(ctx)
		return err
	},
	"stabilizer": func(ctx context.Context, e *Env, say func(string)) error {
		_, err := (&fixes.Stabilizer{Home: e.Home(), Tools: e.Tools}).Ensure(ctx, say)
		return err
	},
	"ai-models": func(ctx context.Context, e *Env, say func(string)) error {
		s := localmodels.Store{Home: e.Home()}
		for _, p := range s.Here() {
			if s.Ready(p) == nil {
				continue
			}
			if err := s.Setup(ctx, p, say); err != nil {
				return err
			}
		}
		return nil
	},
}

// provided are components other packages find and set up (internal/sound:
// clearer and studio voices), registered with Provide.
var (
	provMu   sync.RWMutex
	provided = map[string]Provided{}
)

// Provided is how another package finds, sets up and removes a component.
type Provided struct {
	Detect func(ctx context.Context, e *Env) Found
	Step   StepFunc
	Remove func(ctx context.Context, e *Env) error
}

// Provide registers how a package finds, sets up and removes one of the
// registry's components (call it from init).
func Provide(name string, p Provided) {
	provMu.Lock()
	defer provMu.Unlock()
	provided[name] = p
}

func providedFor(name string) (Provided, bool) {
	provMu.RLock()
	defer provMu.RUnlock()
	p, ok := provided[name]
	return p, ok
}

// stepFor is the step that sets a component up: the test's own, else
// MetaRush's, else the providing package's. Under go test only a test's own
// runs (and the renderer with a stand-in Node.js): nothing is downloaded.
func (e *Env) stepFor(name string) (StepFunc, error) {
	if f, ok := e.Steps[name]; ok {
		return f, nil
	}
	if underTest() && !(name == "renderer" && e.Tools.Explicit(tools.Node)) {
		return nil, apperr.New(apperr.NotConfigured, "Tests never set components up for real (%s).", name)
	}
	if f, ok := steps[name]; ok {
		return f, nil
	}
	if p, ok := providedFor(name); ok && p.Step != nil {
		return p.Step, nil
	}
	return nil, apperr.New(apperr.NotConfigured, "This MetaRush can't set that up by itself.")
}

// stepHere reports whether a step exists for a component.
func (e *Env) stepHere(name string) bool {
	if _, ok := e.Steps[name]; ok {
		return true
	}
	if _, ok := steps[name]; ok {
		return true
	}
	p, ok := providedFor(name)
	return ok && p.Step != nil
}
