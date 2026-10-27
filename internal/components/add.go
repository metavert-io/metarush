package components

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/tools"
)

// applies reports whether a way can be used on this computer.
func (w Way) applies(e *Env) bool {
	if len(w.Systems) > 0 && !slices.Contains(w.Systems, e.goos()) {
		return false
	}
	if w.When != nil && !w.When(e) {
		return false
	}
	if (len(w.Brew) > 0 || w.Cask != "") && !e.Tools.Has(tools.Brew) {
		return false
	}
	if len(w.Pins) > 0 && !e.havePins(w.Pins) {
		return false
	}
	if w.Step != "" && !e.stepHere(w.Step) {
		return false
	}
	return true
}

// automatic: MetaRush can add it this way by itself.
func (w Way) automatic() bool {
	return len(w.Brew) > 0 || w.Cask != "" || len(w.Pins) > 0 || w.Step != ""
}

func (w Way) how(e *Env) string {
	switch {
	case len(w.Brew) > 0 || w.Cask != "":
		return HowHomebrew
	case len(w.Pins) > 0:
		return HowDownload
	case w.Step != "":
		return HowMetaRush
	case w.Says != "":
		return HowYourself
	}
	return HowNone
}

// wayFor is how a component is added here: the first automatic way that
// applies, else the first way a person can follow.
func (e *Env) wayFor(c *Component) Way {
	for _, w := range c.ways {
		if w.automatic() && w.applies(e) {
			return w
		}
	}
	for _, w := range c.ways {
		if !w.automatic() && w.applies(e) {
			return w
		}
	}
	return Way{}
}

// AddOptions are how one add runs.
type AddOptions struct {
	// By is who asked (recorded with the choice).
	By string
	// Say hears each stage in plain words.
	Say func(name, msg string)
	// Changed is told after something was added (the running MetaRush looks
	// for its programs again).
	Changed func()
}

// Added is what one add did.
type ComponentAdded struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	// Done: added now; Already: it was there; NeedsYou: only a person can.
	Done     bool `json:"done"`
	Already  bool `json:"already,omitempty"`
	NeedsYou bool `json:"needsYou,omitempty"`
	// How it was added (homebrew, download, metarush) or how to (yourself).
	How string `json:"how"`
	// Unlocks is what it adds; Licence what a person must know.
	Unlocks string `json:"unlocks"`
	Licence string `json:"licence,omitempty"`
	// Says is one plain line about what happened.
	Says string `json:"says"`
	// Also are components it needed that were added first.
	Also []string `json:"also,omitempty"`
}

var addMu sync.Mutex

// Add adds a component (and what it needs first) to this computer. Only a
// person asks for it (the ops are HumanOnly, D327); nothing runs sudo.
func (e *Env) Add(ctx context.Context, name string, o AddOptions) (*ComponentAdded, error) {
	c, ok := Get(name)
	if !ok {
		return nil, unknown(name)
	}
	if c.Required {
		return nil, apperr.New(apperr.InvalidInput, "%s comes with MetaRush's setup, so it isn't added on its own.", c.Title).WithCommand("metarush setup")
	}
	addMu.Lock()
	defer addMu.Unlock()
	return e.add(ctx, c, o, map[string]bool{})
}

func (e *Env) add(ctx context.Context, c *Component, o AddOptions, seen map[string]bool) (*ComponentAdded, error) {
	if seen[c.Name] {
		return &ComponentAdded{Name: c.Name, Title: c.Title, Already: true}, nil
	}
	seen[c.Name] = true
	say := func(msg string) {
		if o.Say != nil {
			o.Say(c.Name, msg)
		}
	}
	out := &ComponentAdded{Name: c.Name, Title: c.Title, Unlocks: c.Unlocks, Licence: c.Licence}
	f := e.Find(ctx, c)
	if f.NotHere != "" {
		return nil, apperr.New(apperr.NotConfigured, "%s can't be added on this computer: %s", c.Title, lowerStart(f.NotHere)).WithDetails(map[string]any{"component": c.Name})
	}
	if f.Ready {
		out.Already, out.How, out.Says = true, HowNone, c.Title+" was already on this computer."
		_ = choice.Set(e.Home(), map[string]bool{c.Name: true}, o.By)
		return out, nil
	}
	if p := e.adding(c.Name); p != nil && p.PID != os.Getpid() {
		return nil, apperr.New(apperr.Conflict, "%s is being added already.", c.Title).WithHint("It's ready when that finishes; Settings → Components shows how far along it is.")
	}
	for _, n := range c.Needs {
		need, ok := Get(n)
		if !ok || !need.on(e.goos()) || e.Find(ctx, need).Ready {
			continue
		}
		sub, err := e.add(ctx, need, o, seen)
		if err != nil {
			return nil, apperr.Wrap(apperr.From(err).Code, err, "%s needs %s first, and that couldn't be added: %s", c.Title, need.Thing, lowerStart(apperr.From(err).Message))
		}
		if sub.NeedsYou {
			out.NeedsYou, out.How = true, HowYourself
			out.Says = fmt.Sprintf("%s needs %s first, which you add yourself: %s", c.Title, need.Thing, sub.Says)
			return out, nil
		}
		if sub.Done {
			out.Also = append(out.Also, need.Name)
		}
	}
	w := e.wayFor(c)
	out.How = w.how(e)
	if !w.automatic() {
		out.NeedsYou = true
		out.Says = firstNonEmpty(w.Says, "MetaRush can't add it on this computer.")
		return out, nil
	}
	// The person's choice comes first: what sets itself up only when wanted
	// (the face model, the voice cleaner…) sees it during the steps below.
	_ = choice.Set(e.Home(), map[string]bool{c.Name: true}, o.By)
	e.writeJSON(e.progressFile(c.Name), ComponentProgress{PID: os.Getpid(), StartedAt: time.Now().UTC(), Message: "Starting…"})
	defer os.Remove(e.progressFile(c.Name))
	progress := func(msg string) {
		e.writeJSON(e.progressFile(c.Name), ComponentProgress{PID: os.Getpid(), StartedAt: time.Now().UTC(), Message: msg})
		say(msg)
	}
	if err := e.run(ctx, c, w, progress); err != nil {
		e.writeJSON(e.errorFile(c.Name), lastError{At: time.Now().UTC(), Message: apperr.From(err).Message})
		return nil, failed(c, err)
	}
	e.rescan()
	if o.Changed != nil {
		o.Changed()
	}
	if after := e.Find(ctx, c); !after.Ready {
		why := firstNonEmpty(after.Partly, "MetaRush still can't find it")
		e.writeJSON(e.errorFile(c.Name), lastError{At: time.Now().UTC(), Message: why})
		return nil, apperr.New(apperr.ToolFailed, "%s was set up, but MetaRush still can't use it: %s", c.Title, lowerStart(why)).
			WithDetails(map[string]any{"component": c.Name, "detail": after.Detail}).WithCommand("metarush doctor --details")
	}
	_ = os.Remove(e.errorFile(c.Name))
	_ = choice.Set(e.Home(), map[string]bool{c.Name: true}, o.By)
	out.Done, out.Says = true, c.Title+" is added: "+lowerStart(c.Unlocks)
	return out, nil
}

// run adds a component one way: Homebrew, then MetaRush's downloads, then
// MetaRush's own step.
func (e *Env) run(ctx context.Context, c *Component, w Way, say func(string)) error {
	if len(w.Brew) > 0 {
		say("Installing " + strings.Join(w.Brew, ", ") + " with Homebrew (this can take a few minutes)…")
		if err := e.brew(ctx, append([]string{"install", "--formula"}, w.Brew...)); err != nil {
			return err
		}
	}
	if w.Cask != "" {
		say("Installing " + c.Title + " with Homebrew…")
		if err := e.brew(ctx, []string{"install", "--cask", w.Cask}); err != nil {
			return err
		}
	}
	for _, id := range w.Pins {
		for _, p := range e.pinsFor(id) {
			if err := e.fetch(ctx, p, say); err != nil {
				return err
			}
		}
	}
	if w.Step != "" {
		e.rescan() // what the brew and downloads above brought (the renderer needs Node.js)
		f, err := e.stepFor(w.Step)
		if err != nil {
			return err
		}
		if err := f(ctx, e, say); err != nil {
			return err
		}
	}
	return nil
}

// brewEnv keeps Homebrew's output plain and leaves its clean-up to the person.
var brewEnv = []string{"HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_INSTALL_CLEANUP=1"}

// brew runs Homebrew with typed arguments. Under go test only a stand-in
// Homebrew runs (tools.brew / METARUSH_BREW).
func (e *Env) brew(ctx context.Context, args []string) error {
	if underTest() && !e.Tools.Explicit(tools.Brew) {
		return apperr.New(apperr.NotConfigured, "Tests never run the real Homebrew.")
	}
	res, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.Brew, Args: args, Env: brewEnv})
	if err != nil {
		said := ""
		var tf *tools.ErrFailed
		if errors.As(err, &tf) {
			said = strings.TrimSpace(tf.Stderr)
		} else if res != nil {
			said = strings.TrimSpace(string(res.Stderr))
		}
		return apperr.Wrap(apperr.ToolFailed, err, "Homebrew couldn't install it.").WithHint("Homebrew's own message is behind Details.").
			WithDetails(map[string]any{"homebrew": lastLines(said, 12), "args": args})
	}
	return nil
}

// rescan makes the running MetaRush look for its programs again, with
// config.yaml as it is now.
func (e *Env) rescan() { e.Tools.Rescan(e.toolsNow()) }

// Remove takes a component off this computer when MetaRush put it there,
// and records that the person doesn't want it (nothing sets it up by itself
// afterwards). Homebrew's and the system's own are left alone, and the
// person is told how to remove them.
func (e *Env) Remove(ctx context.Context, name, by string) (*ComponentRemoved, error) {
	c, ok := Get(name)
	if !ok {
		return nil, unknown(name)
	}
	if c.Required {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush needs %s to run, so it can't be removed.", c.Thing)
	}
	addMu.Lock()
	defer addMu.Unlock()
	if p := e.adding(c.Name); p != nil {
		return nil, apperr.New(apperr.Conflict, "%s is being added right now; remove it once that finishes.", c.Title)
	}
	f := e.Find(ctx, c)
	out := &ComponentRemoved{Name: c.Name, Title: c.Title}
	if e.removable(c, f) {
		remove := c.remove
		if p, ok := providedFor(c.Name); ok && p.Remove != nil {
			remove = p.Remove
		}
		if err := remove(ctx, e); err != nil {
			if ae := apperr.From(err); ae.Code != apperr.Internal {
				return nil, ae
			}
			return nil, apperr.Wrap(apperr.Internal, err, "%s couldn't be removed.", c.Title)
		}
		out.Removed = true
		e.rescan()
	}
	if err := choice.Set(e.Home(), map[string]bool{c.Name: false}, by); err != nil {
		return nil, err
	}
	_ = os.Remove(e.errorFile(c.Name))
	still := e.Find(ctx, c).Ready
	switch {
	case out.Removed && !still:
		out.Says = c.Title + " is removed from this computer; what it unlocks says it is missing until you add it again."
	case out.Removed:
		out.Says = "MetaRush removed its own copy of " + c.Thing + ", but another is still on this computer, so it is still used. " + c.removeSays
	case still:
		out.Says = "MetaRush didn't add " + lowerFirst(c.Title) + ", so it leaves it alone. " + c.removeSays
	default:
		out.Says = c.Title + " isn't on this computer; MetaRush won't set it up by itself."
	}
	out.Says = strings.TrimSpace(out.Says)
	out.StillHere = still
	return out, nil
}

// Removed is what one remove did.
type ComponentRemoved struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Removed bool   `json:"removed"`
	// StillHere: it still works on this computer (another copy, Homebrew's).
	StillHere bool   `json:"stillHere"`
	Says      string `json:"says"`
}

func unknown(name string) error {
	names := make([]string, 0, len(registry))
	for _, c := range registry {
		if !c.Required {
			names = append(names, c.Name)
		}
	}
	return apperr.New(apperr.NotFound, "MetaRush has no component called “%s”.", name).WithHint("Settings → Components lists them.").
		WithDetails(map[string]any{"components": names}).WithCommand("metarush components")
}

// failed is a component that couldn't be added, in plain words.
func failed(c *Component, err error) error {
	ae := apperr.From(err)
	msg := strings.TrimSuffix(ae.Message, ".")
	if ae.Code == apperr.Internal {
		msg = "something went wrong"
	}
	out := apperr.Wrap(ae.Code, err, "%s couldn't be added: %s.", c.Title, lowerStart(msg)).WithHint(firstNonEmpty(ae.Hint, "Try again; Settings → Components shows what happened."))
	d := map[string]any{"component": c.Name}
	if m, ok := ae.Details.(map[string]any); ok {
		for k, v := range m {
			d[k] = v
		}
	}
	return out.WithDetails(d).WithCommand("metarush components add " + c.Name)
}

func lowerStart(s string) string {
	if s == "" {
		return s
	}
	w := strings.SplitN(s, " ", 2)[0]
	// Keep names (FFmpeg, MetaRush, Node.js) as they are.
	if strings.ToUpper(w[:1]) == w[:1] && strings.ToLower(w[1:]) == w[1:] && !strings.ContainsAny(w, ".") {
		return strings.ToLower(s[:1]) + s[1:]
	}
	return s
}

func lastLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
