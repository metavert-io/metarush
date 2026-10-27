package components

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/tools"
)

// Component states.
const (
	StatusReady    = "ready"
	StatusMissing  = "missing"
	StatusAdding   = "adding"
	StatusFailed   = "failed"
	StatusNeedsYou = "needs-you"
	StatusNotHere  = "not-here"
)

// State is one component on this computer, as every surface shows it.
type ComponentState struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	Unlocks  string   `json:"unlocks"`
	Features []string `json:"features"`
	Required bool     `json:"required"`
	// Suggested: setup ticks it on this computer.
	Suggested bool `json:"suggested"`
	Ready     bool `json:"ready"`
	// Status: ready, missing, adding (being added now), failed (the last try
	// didn't work), needs-you (only a person can add it here) or not-here.
	Status string `json:"status" jsonschema:"enum=ready,enum=missing,enum=adding,enum=failed,enum=needs-you,enum=not-here"`
	// Says is one plain line: what's there, or what adding it does.
	Says string `json:"says"`
	// Needed is what a feature that needs it says while it's missing.
	Needed string `json:"needed,omitempty"`
	Size   string `json:"size"`
	Time   string `json:"time"`
	// Licence is what a person must know when it is added.
	Licence string `json:"licence,omitempty"`
	// How it is added here: homebrew, download (MetaRush's own pinned
	// download), metarush (MetaRush sets it up), yourself, or none.
	How string `json:"how" jsonschema:"enum=homebrew,enum=download,enum=metarush,enum=yourself,enum=none"`
	// Yourself says how a person adds it when MetaRush can't.
	Yourself string `json:"yourself,omitempty"`
	// Command adds it (for Details and terminals).
	Command string   `json:"command,omitempty"`
	Needs   []string `json:"needs,omitempty"`
	// Version, Path and Detail are what detection saw (Details).
	Version string `json:"version,omitempty"`
	Path    string `json:"path,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Adding is a setup in progress; LastError the last one that failed.
	Adding    *ComponentProgress `json:"adding,omitempty"`
	LastError string             `json:"lastError,omitempty"`
	// Chosen is what a person chose: added, left-out, or "" (nobody yet).
	Chosen string `json:"chosen,omitempty" jsonschema:"enum=,enum=added,enum=left-out,enum=waiting"`
	// CanRemove: MetaRush can take it off this computer; otherwise
	// RemoveSays how a person does.
	CanRemove  bool   `json:"canRemove"`
	RemoveSays string `json:"removeSays,omitempty"`
}

// Progress is a component being added (written by the process adding it).
type ComponentProgress struct {
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"startedAt"`
	Message   string    `json:"message"`
}

func (e *Env) stateDir() string { return filepath.Join(e.Home(), "state", "components") }

func (e *Env) progressFile(name string) string {
	return filepath.Join(e.stateDir(), name+".adding.json")
}
func (e *Env) errorFile(name string) string { return filepath.Join(e.stateDir(), name+".error.json") }

func (e *Env) writeJSON(p string, v any) {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return
	}
	b, _ := json.Marshal(v)
	tmp := p + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p)
	}
}

// adding is the setup in progress for a component (nil when none, or when
// the process doing it is gone).
func (e *Env) adding(name string) *ComponentProgress {
	b, err := os.ReadFile(e.progressFile(name))
	if err != nil {
		return nil
	}
	var p ComponentProgress
	if json.Unmarshal(b, &p) != nil || !alive(p.PID) {
		return nil
	}
	return &p
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if pid == os.Getpid() {
		return true
	}
	return syscall.Kill(pid, 0) == nil
}

type lastError struct {
	At      time.Time `json:"at"`
	Message string    `json:"message"`
}

func (e *Env) lastError(name string) string {
	b, err := os.ReadFile(e.errorFile(name))
	if err != nil {
		return ""
	}
	var l lastError
	_ = json.Unmarshal(b, &l)
	return l.Message
}

// Find is what detection sees for one component (nothing changes).
func (e *Env) Find(ctx context.Context, c *Component) Found {
	if !c.on(e.goos()) {
		return Found{NotHere: notHereWords(c)}
	}
	detect := c.detect
	if detect == nil {
		if p, ok := providedFor(c.Name); ok && p.Detect != nil {
			detect = p.Detect
		}
	}
	if detect == nil {
		return Found{Detail: "this MetaRush can't tell"}
	}
	f := detect(tools.Quiet(ctx), e)
	if f.Ready {
		if why := e.changed(c); why != "" {
			f.Ready, f.Partly = false, why
		}
	}
	return f
}

func notHereWords(c *Component) string {
	if slices.Equal(c.Systems, []string{mac}) {
		return "It needs a Mac."
	}
	return "It isn't made for this kind of computer."
}

// Ready reports whether a component works on this computer now.
func (e *Env) Ready(ctx context.Context, name string) bool {
	c, ok := Get(name)
	return ok && e.Find(ctx, c).Ready
}

// StateOf is one component's state.
func (e *Env) StateOf(ctx context.Context, c *Component) ComponentState {
	f := e.Find(ctx, c)
	st := ComponentState{Name: c.Name, Title: c.Title, Unlocks: c.Unlocks, Features: append([]string{}, c.Features...), Required: c.Required,
		Suggested: c.SuggestedHere(e), Ready: f.Ready, Size: c.Size(), Time: c.Time(), Licence: c.Licence, Needs: c.Needs,
		Version: f.Version, Path: f.Path, Detail: f.Detail}
	if st.Features == nil {
		st.Features = []string{}
	}
	if ch, ok := choice.Of(e.Home(), c.Name); ok {
		st.Chosen = map[bool]string{true: "added", false: "left-out"}[ch.Wanted]
		if ch.Waiting {
			st.Chosen = "waiting"
		}
	}
	w := e.wayFor(c)
	st.How = w.how(e)
	if st.How == HowYourself {
		st.Yourself, st.Command = w.Says, w.Command
	} else if !c.Required {
		st.Command = "metarush components add " + c.Name
	}
	st.CanRemove = !c.Required && e.removable(c, f)
	if !st.CanRemove && !c.Required {
		st.RemoveSays = c.removeSays
	}
	switch {
	case f.NotHere != "":
		st.Status, st.Says, st.How, st.Command = StatusNotHere, f.NotHere, HowNone, ""
		st.CanRemove = false
	case f.Ready:
		st.Status, st.Says = StatusReady, readyWords(c, f)
	case e.adding(c.Name) != nil:
		st.Adding = e.adding(c.Name)
		st.Status, st.Says = StatusAdding, "Being added now: "+strings.TrimSuffix(st.Adding.Message, "…")+"…"
	default:
		st.Needed = c.Needed()
		st.LastError = e.lastError(c.Name)
		switch {
		case st.How == HowYourself:
			st.Status, st.Says = StatusNeedsYou, firstNonEmpty(f.Partly, "MetaRush can't add it on this computer by itself.")+" "+w.Says
		case st.LastError != "":
			st.Status, st.Says = StatusFailed, "The last try to add it didn't work: "+strings.TrimSuffix(st.LastError, ".")+"."
		default:
			st.Status, st.Says = StatusMissing, firstNonEmpty(f.Partly, "Not on this computer yet.")
		}
	}
	return st
}

func readyWords(c *Component, f Found) string {
	if f.Partly != "" {
		return "Ready. " + f.Partly
	}
	if c.Required {
		return "Ready."
	}
	return "Ready on this computer."
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// States is every component's state, the required ones first.
func (e *Env) States(ctx context.Context) []ComponentState {
	out := make([]ComponentState, 0, len(registry))
	for _, c := range registry {
		out = append(out, e.StateOf(ctx, c))
	}
	return out
}

// removable: MetaRush put it there (a pinned download or something it set
// up), so it can take it away again.
func (e *Env) removable(c *Component, f Found) bool {
	if c.Required || f.NotHere != "" {
		return false
	}
	if p, ok := providedFor(c.Name); ok && p.Remove != nil {
		return f.Ready || f.Partly != ""
	}
	if c.remove == nil {
		return false
	}
	for _, w := range c.ways {
		for _, id := range w.Pins {
			for _, p := range e.pinsFor(id) {
				if e.placed(p) {
					return true
				}
			}
		}
		if w.Step != "" && (f.Ready || f.Partly != "") {
			return true
		}
	}
	return false
}
