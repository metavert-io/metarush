package firstrun

import (
	"context"
	"os"
	"strings"

	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// WorksItem is one thing MetaRush does, and whether it works here now.
type WorksItem struct {
	Key  string `json:"key" jsonschema:"enum=transcripts,enum=editing,enum=renders,enum=checks,enum=scratch-voice,enum=generated-shots,enum=ai-voices"`
	Name string `json:"name"` // in plain words ("Transcripts and captions")
	// Ready: it works on this computer now.
	Ready bool `json:"ready"`
	// Says what it does (plain words); Next is what to do when it isn't ready.
	Says string `json:"says"`
	Next string `json:"next,omitempty"`
	// Command sets it up (for --details); Place is where in the dashboard.
	Command string `json:"command,omitempty"`
	Place   string `json:"place,omitempty"`
	// Needs says in a few words what it needs on this computer when it isn't
	// ready ("FFmpeg"): what the catalogue's entries that depend on it say
	// (D270).
	Needs string `json:"needs,omitempty"`
	// Component is the component that makes it work (D324): Getting started
	// offers Add it while it isn't ready.
	Component string `json:"component,omitempty"`
}

// Works is what works on this computer by key (transcripts, editing,
// renders, checks, scratch-voice, generated-shots, ai-voices), without
// asking Claude Code: what the catalogue ties its entries to (D270).
func Works(cfg *config.Config, ts *tools.Set) map[string]WorksItem {
	out := map[string]WorksItem{}
	for _, it := range append(noKeys(cfg, ts), withKeys(cfg)...) {
		out[it.Key] = it
	}
	return out
}

// WhatWorks is what works on this computer: with no keys (free, nothing leaves
// the computer), what a key adds (paid services, priced before they run),
// and Claude Code.
type WhatWorks struct {
	NoKeys   []WorksItem `json:"noKeys"`
	WithKeys []WorksItem `json:"withKeys"`
	Claude   ClaudeCode  `json:"claude"`
	// Summary is one plain line for the top of the first run.
	Summary string `json:"summary"`
}

// Build reads this computer's state (tools, renderer, keys, Claude Code);
// it changes nothing and needs no database.
func Build(ctx context.Context, cfg *config.Config, ts *tools.Set) WhatWorks {
	r := WhatWorks{NoKeys: noKeys(cfg, ts), WithKeys: withKeys(cfg), Claude: CheckClaude(ctx, ts)}
	ready := 0
	for _, c := range r.NoKeys {
		if c.Ready {
			ready++
		}
	}
	switch {
	case ready == len(r.NoKeys):
		r.Summary = "Everything below works now, free and on this computer: no keys or accounts needed."
	default:
		r.Summary = "Most of MetaRush works free on this computer with no keys; what isn't ready yet says what it needs."
	}
	return r
}

func noKeys(cfg *config.Config, ts *tools.Set) []WorksItem {
	// What works reads the components' one detection (D324): a missing
	// part says which component adds it, and Getting started offers Add it.
	e := &components.Env{Cfg: cfg, Tools: ts}
	ctx := tools.Quiet(context.Background())
	has := func(name string) bool { return e.Ready(ctx, name) }
	missing := func(it *WorksItem, name string) {
		c, _ := components.Get(name)
		it.Component, it.Needs = c.Name, c.Thing
		it.Next = "Add " + c.Thing + " in Settings → Components (" + c.Size() + ")."
		it.Command, it.Place = "metarush components add "+c.Name, components.Place
	}
	video := has("ffmpeg")
	var out []WorksItem
	t := WorksItem{Key: "transcripts", Name: "Transcripts and captions"}
	if p := transcribe.Select(cfg.Transcription, ts); p != nil {
		t.Ready = true
		t.Says = "Made on this computer as soon as footage arrives; nothing is uploaded. Search what anyone said, and cut by deleting words."
	} else {
		t.Says = "Made on this computer, free, once transcription is added; nothing is uploaded."
		missing(&t, "transcription")
	}
	out = append(out, t)
	e2 := WorksItem{Key: "editing", Name: "Editing", Ready: video,
		Says: "Cut, trim and reorder clips, add captions, titles and graphics, and keep every version — in the dashboard, or by asking Claude Code."}
	if !video {
		e2.Says = "Editing needs FFmpeg, the free video tool, to bring your footage in."
		missing(&e2, "ffmpeg")
	}
	out = append(out, e2)
	rd := WorksItem{Key: "renders", Name: "Finished videos", Says: "Made on this computer at full quality, in every shape you need (wide, square, vertical)."}
	switch rs := renderer.New(cfg, ts).Check(false); {
	case rs.Ready:
		rd.Ready = true
	case !has("node"):
		rd.Says = "Finished videos need Node.js and the Remotion renderer on this computer."
		missing(&rd, renderer.Component)
	case !video:
		rd.Says = "Finished videos need FFmpeg on this computer."
		missing(&rd, "ffmpeg")
	case rs.State == renderer.StateMissing:
		rd.Ready = true // set up by itself when MetaRush first starts
		rd.Says = "Made on this computer at full quality. The first time, MetaRush takes a few minutes to get ready for it by itself."
		rd.Command, rd.Component = "metarush components add renderer", renderer.Component
	default:
		rd.Says = "Finished videos need the Remotion renderer on this computer."
		missing(&rd, renderer.Component)
	}
	out = append(out, rd)
	ck := WorksItem{Key: "checks", Name: "A check of every finished video", Ready: video,
		Says: "Before a video goes out, MetaRush watches and listens to it: words cut off, captions over faces, sound too quiet."}
	if !video {
		missing(&ck, "ffmpeg")
	}
	out = append(out, ck)
	sv := WorksItem{Key: "scratch-voice", Name: "A stand-in voice for narration", Says: "A free, robotic voice to time narration before you record it or choose an AI voice."}
	if has("stand-in-voice") {
		sv.Ready = true
	} else {
		missing(&sv, "stand-in-voice")
		sv.Next = "Add the stand-in voice in Settings → Components (optional)."
	}
	out = append(out, sv)
	return out
}

// withKeys is what a key adds: one entry per kind of paid service MetaRush
// has, ready when this computer has its key.
func withKeys(cfg *config.Config) []WorksItem {
	var out []WorksItem
	seen := map[string]bool{}
	for _, p := range providers.All() {
		st := providers.StatusFor(cfg, p)
		c := WorksItem{Ready: st.Configured, Place: "/settings/integrations", Command: "metarush providers set " + p.Name()}
		switch p.Category() {
		case "video":
			c.Key, c.Name = "generated-shots", "Generated shots"
			c.Says = "New video clips made from a description, with " + p.Title() + ". You see the price before anything is made, and it's paid to " + p.Title() + " directly."
		case "voice":
			c.Key, c.Name = "ai-voices", "AI voices"
			c.Says = "Natural narration voices and cloud voice clean-up, with " + p.Title() + ". You see the price first, and it's paid to " + p.Title() + " directly."
		default:
			continue
		}
		if seen[c.Key] {
			continue
		}
		seen[c.Key] = true
		if !c.Ready {
			c.Next = "Add " + article(p.Title()) + " " + p.Title() + " key in Settings → Integrations."
			c.Needs = article(p.Title()) + " " + p.Title() + " key"
		}
		out = append(out, c)
	}
	return out
}

func tempDir() string { return os.TempDir() }

// article is "an" before a name said with a vowel first (an ElevenLabs key), else "a".
func article(name string) string {
	if name != "" && strings.ContainsRune("AEIOUaeiou", rune(name[0])) {
		return "an"
	}
	return "a"
}
