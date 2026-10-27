// Package components is the one list of what MetaRush needs on a computer
// and what it may add (D324): the program itself, a place for projects and a
// storage folder are required; everything else — FFmpeg, Node.js, the
// Remotion renderer, local transcription, link imports, PDF pages and the
// rest — is optional and unlocks named features. Each component says, in
// video makers' words, what it unlocks, how big it is and how long it takes,
// how MetaRush finds it (one detection, which setup, doctor, Home's Getting
// started and Settings → Components all read) and how it is added on a Mac
// or on Linux: with Homebrew where it is there, else MetaRush's own pinned
// download checked before use into <home>/tools (models into the models
// folder), else plain words for doing it yourself. Nothing here ever runs
// sudo, and adding or removing a component is a person's decision (the ops
// are HumanOnly and OwnerOnly, D327).
package components

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"slices"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// Component is one part MetaRush uses on a computer.
type Component struct {
	// Name is what commands call it (metarush components add renderer).
	Name string
	// Title is what people call it ("The Remotion renderer").
	Title string
	// Thing is what it is, inside a sentence ("the Remotion renderer").
	Thing string
	// Feature names what needs it, to start a sentence: "Rendering needs …".
	Feature string
	// Unlocks says what it adds, in video makers' words.
	Unlocks string
	// Features are the features it unlocks, a few words each.
	Features []string
	// Required: MetaRush needs it to start (setup provides it).
	Required bool
	// Suggested: setup ticks it unless told otherwise.
	Suggested bool
	// Systems it exists on (GOOS; empty: every one).
	Systems []string
	// DownloadMB and DiskMB are rough sizes; Minutes how long adding it
	// usually takes on a home connection.
	DownloadMB, DiskMB, Minutes int
	// Licence is what a person must know when it is added ("" nothing).
	Licence string
	// Needs are components it needs first (added along with it).
	Needs []string
	// Tools are the programs it brings (a missing one points here).
	Tools []string

	detect func(ctx context.Context, e *Env) Found
	ways   []Way
	// remove takes it off this computer; nil when MetaRush can't (Homebrew's,
	// the system's own, or part of setup).
	remove func(ctx context.Context, e *Env) error
	// removeSays is how a person removes it themselves when MetaRush doesn't.
	removeSays string
}

// Way is one way of adding a component on some computers.
type Way struct {
	// Systems it applies to (GOOS; empty: every one).
	Systems []string
	// When, if set, must hold (the program a model needs is already there).
	When func(e *Env) bool
	// Brew are Homebrew formulas to install; Cask a Homebrew cask (macOS).
	Brew []string
	Cask string
	// Pins are MetaRush's own pinned downloads (pins.go).
	Pins []string
	// Step is MetaRush setting it up itself (steps.go).
	Step string
	// Says is how a person adds it themselves (no automatic way), with the
	// Command behind Details.
	Says    string
	Command string
}

// How a way adds a component.
const (
	HowHomebrew = "homebrew"
	HowDownload = "download"
	HowMetaRush = "metarush"
	HowYourself = "yourself"
	HowNone     = "none"
)

// Found is what detection saw.
type Found struct {
	Ready bool
	// NotHere says why it can't be on this computer (a Mac-only part on Linux).
	NotHere string
	// Partly says, plainly, what is there but not enough.
	Partly string
	// Version, Path and Detail are for Details.
	Version, Path, Detail string
}

// Env is the computer components are found and added on.
type Env struct {
	Cfg   *config.Config
	Tools *tools.Set
	// App is set when the components act for a running MetaRush (the
	// dashboard): steps that need it use it.
	App *app.App
	// Pins replace the pinned downloads, Steps the steps, Client the
	// downloader (tests).
	Pins   []Pin
	Steps  map[string]StepFunc
	Client *http.Client
	// GOOS and GOARCH stand in for this computer's (tests); empty: runtime's.
	GOOS, GOARCH string
}

// For is the computer a running MetaRush sees.
func For(a *app.App) *Env { return &Env{Cfg: a.Cfg, Tools: a.Tools, App: a} }

// Home is the installation's folder.
func (e *Env) Home() string { return e.Cfg.Home() }

func (e *Env) goos() string {
	if e.GOOS != "" {
		return e.GOOS
	}
	return runtime.GOOS
}

func (e *Env) goarch() string {
	if e.GOARCH != "" {
		return e.GOARCH
	}
	return runtime.GOARCH
}

// All is every component, the required ones first.
func All() []*Component { return registry }

// Get is a component by name.
func Get(name string) (*Component, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, c := range registry {
		if c.Name == name {
			return c, true
		}
	}
	for alias, n := range aliases {
		if alias == name {
			return Get(n)
		}
	}
	return nil, false
}

// aliases are other names people type for a component.
var aliases = map[string]string{
	"remotion": "renderer", "nodejs": "node", "node.js": "node", "yt-dlp": "link-import", "ytdlp": "link-import",
	"poppler": "pdf-pages", "whisper": "other-languages", "whisper.cpp": "other-languages", "parakeet": "transcription",
	"rclone": "google-drive", "d2": "diagrams", "gh": "github", "bubblewrap": "sandbox", "espeak-ng": "stand-in-voice",
	"claude": "claude-code", "vision": "screen-index", "metarush-vision": "screen-index", "deepfilternet": "clean-voice",
}

// Optional lists the optional components for this computer's system.
func (e *Env) Optional() []*Component {
	var out []*Component
	for _, c := range registry {
		if !c.Required && c.on(e.goos()) {
			out = append(out, c)
		}
	}
	return out
}

// on reports whether the component exists on a system.
func (c *Component) on(goos string) bool {
	return len(c.Systems) == 0 || slices.Contains(c.Systems, goos)
}

// SuggestedHere: setup ticks it on this computer.
func (c *Component) SuggestedHere(e *Env) bool { return c.Suggested && c.on(e.goos()) }

// Size says how big it is in words ("about 400 MB", "about 1.6 GB").
func (c *Component) Size() string {
	mb := c.DownloadMB
	if mb == 0 {
		mb = c.DiskMB
	}
	switch {
	case mb <= 0:
		return "nothing to download"
	case mb >= 1000:
		return fmt.Sprintf("about %s GB", strings.TrimSuffix(fmt.Sprintf("%.1f", float64(mb)/1000), ".0"))
	default:
		return fmt.Sprintf("about %d MB", roundMB(mb))
	}
}

func roundMB(mb int) int {
	switch {
	case mb < 10:
		return mb
	case mb < 100:
		return (mb + 4) / 5 * 5
	default:
		return (mb + 25) / 50 * 50
	}
}

// Time says how long adding it usually takes.
func (c *Component) Time() string {
	switch {
	case c.Minutes <= 0:
		return "a moment"
	case c.Minutes == 1:
		return "about a minute"
	case c.Minutes < 10:
		return fmt.Sprintf("a few minutes (about %d)", c.Minutes)
	default:
		return fmt.Sprintf("about %d minutes", c.Minutes)
	}
}

// Needed is the plain sentence a feature says when this component is
// missing: "Rendering needs the Remotion renderer (about 400 MB). Add it in
// Settings → Components."
func (c *Component) Needed() string {
	if c.Required {
		return fmt.Sprintf("%s needs %s, which MetaRush's setup provides.", c.Feature, c.Thing)
	}
	if strings.HasSuffix(c.Thing, ")") { // "the link downloader (yt-dlp), about 40 MB"
		return fmt.Sprintf("%s needs %s, %s. Add it in Settings → Components.", c.Feature, c.Thing, c.Size())
	}
	return fmt.Sprintf("%s needs %s (%s). Add it in Settings → Components.", c.Feature, c.Thing, c.Size())
}

// lowerFirst lowers "The" at the start of a title that follows other words.
func lowerFirst(s string) string {
	if strings.HasPrefix(s, "The ") || strings.HasPrefix(s, "A ") {
		return strings.ToLower(s[:1]) + s[1:]
	}
	return s
}
