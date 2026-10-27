package components

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/tools"
)

// ListIn lists the components (one when Name is set).
type ComponentsIn struct {
	Name string `json:"name,omitempty" jsonschema:"maxLength=40,description=One component (ffmpeg\\, renderer\\, transcription…); empty: all."`
}

// ListOut is every component on this computer.
type ComponentsOut struct {
	Components []ComponentState `json:"components"`
	// Missing counts the optional components that could be added here and aren't.
	Missing int `json:"missing"`
	// Homebrew: Homebrew is on this computer (what MetaRush adds most with).
	Homebrew bool   `json:"homebrew"`
	Summary  string `json:"summary"`
}

// AddIn adds a component to this computer.
type ComponentAddIn struct {
	Name string `json:"name" jsonschema:"minLength=1,maxLength=40"`
	// Background starts it and answers at once (the dashboard); the list
	// shows how far along it is.
	Background bool `json:"background,omitempty"`
}

// AddOut is a component after adding it.
type ComponentAddOut struct {
	Added     *ComponentAdded `json:"added,omitempty"`
	Component ComponentState  `json:"component"`
	// Started: it is being added in the background.
	Started bool   `json:"started,omitempty"`
	Summary string `json:"summary"`
}

// RemoveIn removes a component from this computer.
type ComponentRemoveIn struct {
	Name string `json:"name" jsonschema:"minLength=1,maxLength=40"`
}

// RemoveOut is what removing did.
type ComponentRemoveOut struct {
	Removed   ComponentRemoved `json:"removed"`
	Component ComponentState   `json:"component"`
	Summary   string           `json:"summary"`
}

type sayKey struct{}

// WithSay makes an add tell each stage (a terminal prints them).
func WithSay(ctx context.Context, say func(name, msg string)) context.Context {
	return context.WithValue(ctx, sayKey{}, say)
}

func sayFrom(ctx context.Context) func(name, msg string) {
	f, _ := ctx.Value(sayKey{}).(func(name, msg string))
	return f
}

// OnChanged, when set (the daemon), is told after a component was added
// or removed: the background work looks again at what it can do.
var OnChanged func(a *app.App)

func init() {
	ops.Def("components.list", "List MetaRush's components on this computer: what each unlocks, whether it is here, its size and how it is added", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ComponentsIn) (*ComponentsOut, error) {
			return list(ctx, For(a), in.Name)
		})
	ops.Def("components.add", "Add an optional component to this computer (FFmpeg, the Remotion renderer, transcription…) with Homebrew or MetaRush's own checked download — people only", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *ComponentAddIn) (*ComponentAddOut, error) {
			return addOp(ctx, a, in)
		})
	ops.Def("components.remove", "Remove an optional component MetaRush added to this computer, and stop it being set up by itself — people only", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *ComponentRemoveIn) (*ComponentRemoveOut, error) {
			return removeOp(ctx, a, in)
		})
}

func list(ctx context.Context, e *Env, name string) (*ComponentsOut, error) {
	// What was added in a terminal or by Homebrew since: look again (D324).
	e.rescan()
	out := &ComponentsOut{Homebrew: e.Tools.Has(tools.Brew)}
	if name != "" {
		c, ok := Get(name)
		if !ok {
			return nil, unknown(name)
		}
		out.Components = []ComponentState{e.StateOf(ctx, c)}
	} else {
		out.Components = e.States(ctx)
	}
	ready, optional := 0, 0
	for _, s := range out.Components {
		if s.Required || s.Status == StatusNotHere {
			continue
		}
		optional++
		if s.Ready {
			ready++
		} else {
			out.Missing++
		}
	}
	switch {
	case name != "":
		out.Summary = out.Components[0].Says
	case out.Missing == 0:
		out.Summary = "Everything MetaRush can use is on this computer."
	default:
		out.Summary = fmt.Sprintf("%d of %d optional components are on this computer; each one you add unlocks more.", ready, optional)
	}
	return out, nil
}

func by(a *app.App) string {
	if a.Actor.UserName != "" {
		return a.Actor.UserName
	}
	if a.Actor.UserID != "" {
		return a.Actor.UserID
	}
	return "a person"
}

func changed(a *app.App) func() {
	return func() {
		if OnChanged != nil {
			OnChanged(a)
		}
		if ops.OnSettingsChanged != nil {
			ops.OnSettingsChanged()
		}
	}
}

func addOp(ctx context.Context, a *app.App, in *ComponentAddIn) (*ComponentAddOut, error) {
	e := For(a)
	c, ok := Get(in.Name)
	if !ok {
		return nil, unknown(in.Name)
	}
	o := AddOptions{By: by(a), Changed: changed(a), Say: sayFrom(ctx)}
	run := func(ctx context.Context) (*ComponentAdded, error) {
		res, err := e.Add(ctx, c.Name, o)
		if err == nil && (res.Done || res.Already) {
			a.Audit(ctx, "components.add", "component", c.Name, "", map[string]any{"how": res.How, "also": res.Also, "already": res.Already})
		}
		return res, err
	}
	if in.Background {
		if p := e.adding(c.Name); p != nil {
			return nil, apperr.New(apperr.Conflict, "%s is being added already.", c.Title)
		}
		if e.Find(ctx, c).Ready {
			res, err := run(ctx)
			if err != nil {
				return nil, err
			}
			return &ComponentAddOut{Added: res, Component: e.StateOf(ctx, c), Summary: res.Says}, nil
		}
		if w := e.wayFor(c); !w.automatic() {
			res, err := run(ctx)
			if err != nil {
				return nil, err
			}
			return &ComponentAddOut{Added: res, Component: e.StateOf(ctx, c), Summary: res.Says}, nil
		}
		// The first stage (or the end) lets the request answer; the add goes
		// on after it (the person may close the page).
		first := make(chan struct{})
		var once sync.Once
		o.Say = func(string, string) { once.Do(func() { close(first) }) }
		go func() {
			defer once.Do(func() { close(first) })
			_, _ = run(context.WithoutCancel(ctx))
		}()
		select {
		case <-first:
		case <-time.After(10 * time.Second):
		}
		st := e.StateOf(ctx, c)
		summary := "Adding " + c.Title + " now; Settings → Components shows how far along it is."
		if c.Licence != "" {
			summary += " " + c.Licence
		}
		return &ComponentAddOut{Component: st, Started: true, Summary: summary}, nil
	}
	res, err := run(ctx)
	if err != nil {
		return nil, err
	}
	out := &ComponentAddOut{Added: res, Component: e.StateOf(ctx, c), Summary: res.Says}
	if res.Done && c.Licence != "" {
		out.Summary += " " + c.Licence
	}
	return out, nil
}

func removeOp(ctx context.Context, a *app.App, in *ComponentRemoveIn) (*ComponentRemoveOut, error) {
	e := For(a)
	res, err := e.Remove(ctx, in.Name, by(a))
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "components.remove", "component", res.Name, "", map[string]any{"removed": res.Removed, "stillHere": res.StillHere})
	changed(a)()
	c, _ := Get(res.Name)
	return &ComponentRemoveOut{Removed: *res, Component: e.StateOf(ctx, c), Summary: res.Says}, nil
}
