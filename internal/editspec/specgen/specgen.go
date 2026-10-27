// Package specgen generates random, schema-valid edit specs for property and
// fuzz tests (cut sheet round trips, diff-to-ops). Values are chosen to stress
// formatting: odd float precision, unicode, quotes, line breaks and text that
// looks like Markdown structure.
package specgen

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Assets are fixed asset IDs the generator references.
var Assets = []string{
	"ast_01jabcdefghjkmnpqrstvwxyz0",
	"ast_01jabcdefghjkmnpqrstvwxyz1",
	"ast_01jabcdefghjkmnpqrstvwxyz2",
	"ast_01jabcdefghjkmnpqrstvwxyz3",
}

// Gen produces specs from a seeded source.
type Gen struct {
	R *rand.Rand
	// X draws the fields added with resources and nested cuts (D82, D84), so
	// R's sequence — and every golden made from it — stays as it was.
	X *rand.Rand
	// C draws the conversations (D99), so R's and X's sequences stay as they were.
	C *rand.Rand
	// Plain keeps text simple (letters, digits, spaces, punctuation) so specs
	// read like real ones; the default stresses escaping. Plain specs have no
	// resources, nested cuts or conversations.
	Plain bool
	// Versions also gives narration script versions (D86): the original script
	// as given (multi-line, with fences and markup of its own), Claude's fitted
	// lines, the version in use and the parked lines. Off by default, so the
	// specs other tests and golden files use stay the same.
	Versions bool
}

// New returns a generator for seed.
func New(seed int64) *Gen {
	return &Gen{R: rand.New(rand.NewSource(seed)), X: rand.New(rand.NewSource(seed*7919 + 104729)), C: rand.New(rand.NewSource(seed*6151 + 2971))}
}

// Resources are fixed resource IDs the generator pins.
var Resources = []string{
	"res_01jabcdefghjkmnpqrstvwxyz0",
	"res_01jabcdefghjkmnpqrstvwxyz1",
	"res_01jabcdefghjkmnpqrstvwxyz2",
}

// NestedCuts are fixed nested cut source keys the generator uses.
var NestedCuts = []string{
	"cut:prj_01jabcdefghjkmnpqrstvwxyz5/rev_01jabcdefghjkmnpqrstvwxyz5",
	"cut:prj_01jabcdefghjkmnpqrstvwxyz6/rev_01jabcdefghjkmnpqrstvwxyz6",
}

// baseTemplates are the templates R chooses from (the original six; logo.corner comes from X).
var baseTemplates = []string{editspec.TplLowerThirdClean, editspec.TplLowerThirdCinematic, editspec.TplQuoteKinetic, editspec.TplEndCard, editspec.TplDataCard, editspec.TplTitleCard}

func (g *Gen) chance(p float64) bool { return g.R.Float64() < p }

// unfused rounds a product to float64 before it is added to anything: Go fuses
// a*b+c into one instruction on arm64 but not on amd64, so without it a seed
// made slightly different specs on the two (and golden files written on one
// failed on the other).
func unfused(v float64) float64 { return float64(v) }

func (g *Gen) pick(xs ...string) string { return xs[g.R.Intn(len(xs))] }

// ID returns a valid item ID unique within used.
func (g *Gen) ID(used map[string]bool) string {
	for {
		var b strings.Builder
		b.WriteByte(byte('a' + g.R.Intn(26)))
		n := g.R.Intn(10)
		for i := 0; i < n; i++ {
			b.WriteByte("abcdefghijklmnopqrstuvwxyz0123456789_-"[g.R.Intn(38)])
		}
		id := b.String()
		if id == "captions" || used[id] {
			continue
		}
		used[id] = true
		return id
	}
}

var nasty = []string{
	"", " ", "  ", "\n", "\n\n", "\t", "\"", "'", "“", "”", "→", "->", "↳", "·", "|", "#", "## ", "> ", "- ", "* ", "1. ",
	"{", "}", "[", "]", ":", ": ", " #", "\\", "Why:", "Evidence:", "Notes:", "Rights:", "---", "é", "日本", "😀", string(rune(0x2028)), string(rune(0xFEFF)), "<b>",
	"0:12.5", "true", "null", "yes", "~", "%", "&", "`", "@",
}

var words = []string{"we", "built", "it", "twice", "the", "garage", "question", "watching", "nobody", "pause", "Ada", "light", "a", "of", "and", "keep", "breath", "slow"}

// Text returns a random string up to maxLen runes.
func (g *Gen) Text(maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	n := 1 + g.R.Intn(12)
	var parts []string
	for i := 0; i < n; i++ {
		if !g.Plain && g.chance(0.3) {
			parts = append(parts, nasty[g.R.Intn(len(nasty))])
		} else {
			parts = append(parts, words[g.R.Intn(len(words))])
		}
	}
	sep := " "
	s := strings.Join(parts, sep)
	if g.Plain {
		s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
		if s == "" {
			s = "note"
		}
	}
	r := []rune(s)
	if len(r) > maxLen {
		r = r[:maxLen]
	}
	return string(r)
}

// Float returns a value in [lo, hi] with a random number of decimals (sometimes
// full float64 precision).
func (g *Gen) Float(lo, hi float64) float64 {
	v := lo + unfused(g.R.Float64()*(hi-lo))
	switch g.R.Intn(5) {
	case 0:
		return math.Round(v)
	case 1:
		return math.Round(v*10) / 10
	case 2:
		return math.Round(v*1000) / 1000
	case 3:
		return math.Min(hi, math.Max(lo, v)) // full precision
	default:
		return math.Round(v*100) / 100
	}
}

func (g *Gen) ptr(v float64) *float64 { return &v }

// Evidence returns random evidence entries.
func (g *Gen) Evidence() []editspec.Evidence {
	if !g.chance(0.5) {
		return nil
	}
	n := 1 + g.R.Intn(3)
	out := make([]editspec.Evidence, 0, n)
	for i := 0; i < n; i++ {
		e := editspec.Evidence{Kind: g.pick("transcript", "insight", "frame", "scene", "user", "analysis")}
		if g.chance(0.8) {
			e.AssetID = Assets[g.R.Intn(len(Assets))]
		}
		if g.chance(0.7) {
			e.From = g.ptr(g.Float(0, 60))
		}
		if g.chance(0.6) {
			e.To = g.ptr(g.Float(0, 90))
		}
		if g.chance(0.3) {
			e.Ref = g.Text(40)
		}
		if g.chance(0.5) {
			e.Quote = g.Text(120)
		}
		if g.chance(0.4) {
			e.Note = g.Text(120)
		}
		out = append(out, e)
	}
	return out
}

// Clip returns a random clip with the given ID.
func (g *Gen) Clip(id string) editspec.Clip {
	in := g.Float(0, 50)
	c := editspec.Clip{ID: id, AssetID: Assets[g.R.Intn(len(Assets))], In: in, Out: in + 0.2 + g.Float(0, 20)}
	if g.chance(0.3) {
		c.Speed = g.Float(0.25, 4)
	}
	if g.chance(0.3) {
		c.Volume = g.ptr(g.Float(0, 2))
	}
	c.Muted = g.chance(0.2)
	if g.chance(0.4) {
		f := &editspec.Framing{}
		if g.chance(0.7) {
			f.Mode = g.pick("fill", "fit")
		}
		if g.chance(0.6) {
			f.FocusX = g.ptr(g.Float(0, 1))
		}
		if g.chance(0.5) {
			f.FocusY = g.ptr(g.Float(0, 1))
		}
		if g.chance(0.4) {
			f.Zoom = g.Float(1, 3)
		}
		c.Framing = f
	}
	if g.chance(0.5) {
		t := &editspec.Transition{Type: g.pick(editspec.TransitionCut, editspec.TransitionFade, editspec.TransitionCrossfade, editspec.TransitionDip,
			editspec.TransitionDipWhite, editspec.TransitionWipe, editspec.TransitionSlide, editspec.TransitionPush, editspec.TransitionZoom, editspec.TransitionWhip)}
		if g.chance(0.8) {
			t.Duration = g.Float(0, 5)
		}
		if g.chance(0.3) {
			t.Direction = g.pick(editspec.DirLeft, editspec.DirRight, editspec.DirUp, editspec.DirDown)
		}
		if g.chance(0.3) {
			t.Easing = g.pick(editspec.EaseInOut, editspec.EaseLinear, editspec.EaseIn, editspec.EaseOut)
		}
		if g.chance(0.4) {
			a := &editspec.SoundTransition{Mode: g.pick(editspec.SoundCut, editspec.SoundLead, editspec.SoundCarry, editspec.SoundAuto)}
			if g.chance(0.7) {
				a.Duration = g.Float(0, 3)
			}
			if g.chance(0.4) {
				a.Blend = g.ptr(g.Float(0, 2))
			}
			t.Audio = a
		}
		c.Transition = t
	}
	if g.chance(0.2) {
		c.FadeOut = g.Float(0, 5)
	}
	if g.chance(0.3) {
		c.Label = g.Text(120)
	}
	if g.chance(0.5) {
		c.Rationale = g.Text(300)
	}
	c.Evidence = g.Evidence()
	c.Locked = g.chance(0.2)
	if g.chance(0.4) {
		c.Notes = g.Text(400)
	}
	return c
}

// Audio returns a random audio track.
func (g *Gen) Audio(id string) editspec.AudioTrack {
	in := g.Float(0, 20)
	a := editspec.AudioTrack{ID: id, AssetID: Assets[g.R.Intn(len(Assets))], Role: g.pick("voiceover", "music", "sfx", "dialogue"), In: in, Out: in + 0.5 + g.Float(0, 30), At: g.Float(0, 20)}
	if g.chance(0.6) {
		a.GainDB = g.ptr(g.Float(-60, 12))
	}
	if g.chance(0.5) {
		a.FadeIn = g.Float(0, 10)
	}
	if g.chance(0.5) {
		a.FadeOut = g.Float(0, 10)
	}
	a.DuckUnderDialogue = g.chance(0.5)
	if g.chance(0.3) {
		a.DuckDB = g.ptr(g.Float(-30, 0))
	}
	if g.chance(0.5) {
		r := &editspec.Rights{}
		if g.chance(0.7) {
			r.License = g.Text(80)
		}
		if g.chance(0.5) {
			r.Attribution = g.Text(80)
		}
		if g.chance(0.5) {
			r.Source = g.Text(80)
		}
		a.Rights = r
	}
	if g.chance(0.4) {
		a.Rationale = g.Text(300)
	}
	a.Locked = g.chance(0.2)
	if g.chance(0.3) {
		a.Notes = g.Text(300)
	}
	if g.chance(0.3) {
		a.Fit = g.pick(editspec.TrackFitAuto, editspec.TrackFitManual)
		if a.Fit == editspec.TrackFitAuto {
			a.Loop = g.chance(0.4)
			if g.chance(0.6) {
				a.Ending = g.pick(editspec.EndingPhrase, editspec.EndingBeat)
			}
		}
	}
	return a
}

// Params returns random params for a template.
func (g *Gen) Params(tpl string) json.RawMessage {
	m := map[string]any{}
	switch tpl {
	case editspec.TplLowerThirdClean, editspec.TplLowerThirdCinematic:
		m["name"] = g.nonEmpty(60)
		if g.chance(0.6) {
			m["title"] = g.Text(80)
		}
		if g.chance(0.3) {
			m["align"] = g.pick("left", "center", "right")
		}
	case editspec.TplQuoteKinetic:
		m["text"] = g.nonEmpty(240)
		if g.chance(0.5) {
			m["attribution"] = g.Text(80)
		}
		if g.chance(0.4) {
			m["emphasis"] = []string{g.Text(20), g.Text(20)}
		}
		if g.chance(0.3) {
			m["motion"] = g.pick("restrained", "none")
		}
	case editspec.TplEndCard:
		m["headline"] = g.nonEmpty(80)
		if g.chance(0.5) {
			m["cta"] = g.Text(40)
		}
	case editspec.TplDataCard:
		m["value"] = g.nonEmpty(16)
		m["label"] = g.nonEmpty(80)
		m["source"] = g.nonEmpty(200)
		if g.chance(0.4) {
			m["trend"] = g.pick("up", "down", "flat")
		}
	case editspec.TplTitleCard:
		m["title"] = g.nonEmpty(80)
		if g.chance(0.5) {
			m["subtitle"] = g.Text(120)
		}
	}
	b, _ := json.Marshal(m)
	return b
}

func (g *Gen) nonEmpty(n int) string {
	s := g.Text(n)
	if strings.TrimSpace(s) == "" {
		return "x"
	}
	return s
}

// Overlay returns a random overlay.
func (g *Gen) Overlay(id string) editspec.Overlay {
	tpl := baseTemplates[g.R.Intn(len(baseTemplates))]
	o := editspec.Overlay{ID: id, Template: tpl, At: g.Float(0, 30), Duration: g.Float(0.2, 60), Params: g.Params(tpl)}
	if g.chance(0.5) {
		o.Rationale = g.Text(300)
	}
	o.Evidence = g.Evidence()
	o.Locked = g.chance(0.2)
	if g.chance(0.3) {
		o.Notes = g.Text(300)
	}
	if g.chance(0.25) {
		// A project motion graphic instead of a template (D68): a few names, so
		// one name is sometimes used with two versions in a cut.
		o.Template = ""
		o.Motion = &editspec.MotionRef{Name: g.pick("route-map", "logo-sting", "count-up"), Hash: g.hex(64)}
		m := map[string]any{"from": g.Text(40), "steps": g.R.Intn(20)}
		if g.chance(0.5) {
			m["accent"] = g.chance(0.5)
		}
		o.Params, _ = json.Marshal(m)
	}
	return o
}

// hex returns n lowercase hex digits; half the time one of four fixed
// versions, so overlays in one cut share versions.
func (g *Gen) hex(n int) string {
	const digits = "0123456789abcdef"
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[g.R.Intn(16)]
	}
	// A few fixed versions make collisions between overlays likely.
	if g.chance(0.5) {
		return strings.Repeat(string(digits[g.R.Intn(4)]), n)
	}
	return string(b)
}

// Captions returns random caption settings.
func (g *Gen) Captions() *editspec.Captions {
	c := &editspec.Captions{Enabled: g.chance(0.7)}
	if g.chance(0.6) {
		c.Style = g.pick("clean", "emphasis", "boxed", "bold", "highlight", "subtitle")
	}
	if g.chance(0.6) {
		c.Position = g.pick("bottom", "lower", "middle", "top")
	}
	if g.chance(0.5) {
		c.MaxWordsPerLine = 1 + g.R.Intn(12)
	}
	c.Uppercase = g.chance(0.3)
	if g.chance(0.5) {
		c.Source = g.pick("transcript", "dialogue", "voiceover", "both", "manual")
	}
	c.ShowSpeakers = g.chance(0.3)
	if g.chance(0.4) {
		n := 1 + g.R.Intn(4)
		for i := 0; i < n; i++ {
			from := g.Float(0, 30)
			s := editspec.CaptionSegment{From: from, To: from + g.Float(0.1, 5), Text: g.Text(200)}
			if g.chance(0.5) {
				s.Speaker = g.Text(60)
			}
			c.Segments = append(c.Segments, s)
		}
	}
	if g.chance(0.4) {
		c.Emphasis = []string{g.Text(20), g.Text(20)}
	}
	c.Locked = g.chance(0.2)
	return c
}

func (g *Gen) boolPtr() *bool { v := g.chance(0.5); return &v }

func (g *Gen) word(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteByte("abcdefghijklmnopqrstuvwxyz0123456789"[g.R.Intn(36)])
	}
	return b.String()
}

// Anchor returns a random start point for a narration line (clipIDs may be
// empty, then no clip anchors are made).
func (g *Gen) Anchor(clipIDs []string) editspec.Anchor {
	var a editspec.Anchor
	kind := g.R.Intn(4)
	if kind == 0 && len(clipIDs) == 0 {
		kind = 2
	}
	switch kind {
	case 0:
		a.Kind, a.ClipID = editspec.AnchorClip, clipIDs[g.R.Intn(len(clipIDs))]
		switch {
		case g.chance(0.4):
			a.Edge = "end"
		case g.chance(0.1):
			a.Edge = "start"
		}
		if g.chance(0.5) {
			a.Offset = g.Float(-60, 60)
		}
	case 1:
		a.Kind, a.AssetID, a.At = editspec.AnchorMoment, Assets[g.R.Intn(len(Assets))], g.ptr(g.Float(0, 60))
		if g.chance(0.4) && len(clipIDs) > 0 {
			a.ClipID = clipIDs[g.R.Intn(len(clipIDs))]
		}
		if g.chance(0.5) {
			a.Quote = g.Text(300)
		}
		if g.chance(0.3) {
			a.Ref = g.Text(200)
		}
		if g.chance(0.2) {
			a.Offset = g.Float(-5, 5)
		}
	case 2:
		a.Kind = editspec.AnchorAfter
		switch {
		case g.chance(0.4):
			a.Gap = g.ptr(g.Float(0, 10))
		case g.chance(0.3):
			a.Offset = -g.Float(0.001, 5)
		}
	default:
		a.Kind, a.At = editspec.AnchorTime, g.ptr(g.Float(0, 60))
		if g.chance(0.1) {
			a.Offset = g.Float(-2, 2) // unusual, still valid
		}
	}
	return a
}

// Voice returns a random synthetic voice choice.
func (g *Gen) Voice() *editspec.VoiceRef {
	v := &editspec.VoiceRef{Provider: g.pick("local", "elevenlabs")}
	if g.chance(0.6) {
		v.VoiceID = g.word(20)
	}
	if g.chance(0.6) {
		v.Name = g.Text(120)
	}
	if g.chance(0.3) {
		v.ModelID = g.pick("eleven_multilingual_v2", "eleven_turbo_v2_5", g.word(12))
	}
	if g.chance(0.2) {
		st := &editspec.TTSVoiceSettings{}
		if g.chance(0.6) {
			st.Stability = g.ptr(g.Float(0, 1))
		}
		if g.chance(0.5) {
			st.SimilarityBoost = g.ptr(g.Float(0, 1))
		}
		if g.chance(0.4) {
			st.Style = g.ptr(g.Float(0, 1))
		}
		if g.chance(0.4) {
			st.Speed = g.ptr(g.Float(0.7, 1.2))
		}
		if g.chance(0.3) {
			st.SpeakerBoost = g.boolPtr()
		}
		if g.chance(0.3) {
			st.OutputFormat = "mp3_44100_128"
		}
		v.Settings = st
	}
	return v
}

func (g *Gen) processed() *editspec.ProcessedRef {
	if !g.chance(0.3) {
		return nil
	}
	pr := &editspec.ProcessedRef{AssetID: Assets[g.R.Intn(len(Assets))], Preset: g.Text(40)}
	if g.chance(0.4) {
		pr.Offset = g.Float(0, 2)
	}
	return pr
}

// Take returns a random take (a range of an audio asset) for a line.
func (g *Gen) Take() *editspec.TakeRef {
	in := g.Float(0, 30)
	t := &editspec.TakeRef{AssetID: Assets[g.R.Intn(len(Assets))], In: in, Out: in + 0.2 + g.Float(0, 20)}
	if g.chance(0.5) {
		t.TakeID = "tk_" + g.word(26)
	}
	if g.chance(0.3) {
		t.Speed = g.Float(0.5, 2)
	}
	if g.chance(0.6) {
		t.Source = g.pick("scratch", "tts", "recorded", "imported")
	}
	t.AIVoice = g.chance(0.3)
	t.Processed = g.processed()
	t.UseProcessed = g.chance(0.3)
	return t
}

// Beat returns a random narration line.
func (g *Gen) Beat(id string, clipIDs []string) editspec.Beat {
	b := editspec.Beat{ID: id, Text: g.nonEmpty(400), Anchor: g.Anchor(clipIDs)}
	if g.chance(0.4) {
		b.Delivery = g.Text(300)
	}
	if g.chance(0.3) {
		b.Rationale = g.Text(300)
	}
	if g.chance(0.2) {
		b.Evidence = g.Evidence()
	}
	if g.chance(0.3) {
		b.Voice = g.Voice()
	}
	if g.chance(0.4) {
		b.Take = g.Take()
	}
	if g.chance(0.3) {
		b.GainDB = g.ptr(g.Float(-30, 12))
	}
	if g.chance(0.4) {
		b.State = g.pick("draft", "scratch", "generated", "recorded", "imported", "final")
	}
	b.Locked = g.chance(0.15)
	return b
}

// Voiceover returns a random narration script whose lines anchor to clipIDs.
func (g *Gen) Voiceover(clipIDs []string, used map[string]bool) *editspec.Voiceover {
	v := &editspec.Voiceover{Beats: []editspec.Beat{}}
	n := g.R.Intn(5)
	for i := 0; i < n; i++ {
		v.Beats = append(v.Beats, g.Beat(g.ID(used), clipIDs))
	}
	if g.chance(0.4) {
		st := &editspec.VoiceoverSettings{}
		if g.chance(0.6) {
			st.SpeakingRateWPM = g.Float(60, 320)
		}
		if g.chance(0.4) {
			st.RateSource = g.pick("default", "measured", "user")
		}
		if g.chance(0.5) {
			st.FitPolicy = g.pick("auto", "ask", "shorten")
		}
		if g.chance(0.4) {
			st.Gap = g.Float(0, 5)
		}
		v.Settings = st
	}
	if g.chance(0.4) {
		v.Layout = g.pick("beats", "continuous")
	}
	if g.chance(0.3) {
		in := g.Float(0, 30)
		c := &editspec.ContinuousVO{AssetID: Assets[g.R.Intn(len(Assets))], In: in, Out: in + 0.5 + g.Float(0, 60), Anchor: g.Anchor(clipIDs)}
		if c.Anchor.Kind == editspec.AnchorAfter {
			c.Anchor = editspec.Anchor{Kind: editspec.AnchorTime, At: g.ptr(g.Float(0, 10))}
		}
		if g.chance(0.4) {
			c.GainDB = g.ptr(g.Float(-30, 12))
		}
		if g.chance(0.5) {
			c.Source = g.pick("scratch", "tts", "recorded", "imported")
		}
		c.AIVoice = g.chance(0.3)
		c.Processed = g.processed()
		c.UseProcessed = g.chance(0.3)
		v.Continuous = c
	}
	v.Locked = g.chance(0.15)
	if g.Versions {
		g.versions(v, clipIDs, used)
	}
	return v
}

// versions adds random script versions to a narration.
func (g *Gen) versions(v *editspec.Voiceover, clipIDs []string, used map[string]bool) {
	if g.chance(0.6) {
		var lines []string
		for i := 0; i < 1+g.R.Intn(5); i++ {
			switch g.R.Intn(6) {
			case 0:
				lines = append(lines, "")
			case 1:
				lines = append(lines, strings.Repeat("`", 3+g.R.Intn(3))+g.pick("", "text", "md"))
			case 2:
				lines = append(lines, "  "+g.Text(80)+"  ")
			case 3:
				lines = append(lines, "## "+g.Text(40))
			default:
				lines = append(lines, g.Text(120))
			}
		}
		v.Original = strings.Join(lines, "\n")
		if g.chance(0.4) {
			v.Original += "\n"
		}
		if g.chance(0.1) {
			v.Original += "\r\n\x01" // a fence can't hold these: JSON in the settings instead
		}
	}
	if g.chance(0.5) {
		f := &editspec.FittedScript{}
		if g.chance(0.5) {
			f.Note = g.Text(200)
		}
		for i, b := range v.Beats {
			if i > 3 {
				break
			}
			fl := editspec.FittedLine{ID: b.ID, Original: b.Text, Text: g.Text(100), Kept: g.chance(0.3)}
			if fl.Text == "" {
				fl.Text = "words"
			}
			if g.chance(0.6) {
				a := g.Anchor(clipIDs)
				fl.Anchor = &a
			}
			if g.chance(0.6) {
				fl.Reason = g.Text(120)
			}
			f.Lines = append(f.Lines, fl)
		}
		v.Fitted = f
	}
	if g.chance(0.5) {
		v.Active = g.pick("original", "fitted", "")
	}
	if g.chance(0.4) {
		for i := 0; i < 1+g.R.Intn(3); i++ {
			v.Parked = append(v.Parked, g.Beat(g.ID(used), clipIDs))
		}
	}
}

// Mix returns random mixer settings.
func (g *Gen) Mix() *editspec.Mix {
	bus := func() *editspec.Bus {
		if !g.chance(0.5) {
			return nil
		}
		b := &editspec.Bus{}
		if g.chance(0.6) {
			b.GainDB = g.ptr(g.Float(-60, 12))
		}
		b.Mute = g.chance(0.2)
		b.Solo = g.chance(0.1)
		return b
	}
	m := &editspec.Mix{Source: bus(), Voiceover: bus(), Music: bus(), SFX: bus(), MuteSource: g.chance(0.3)}
	if g.chance(0.5) {
		d := &editspec.Ducking{}
		if g.chance(0.5) {
			d.Enabled = g.boolPtr()
		}
		if g.chance(0.4) {
			d.MusicUnderVoiceoverDB = g.ptr(g.Float(-40, 0))
		}
		if g.chance(0.3) {
			d.SourceUnderVoiceoverDB = g.ptr(g.Float(-60, 0))
		}
		if g.chance(0.3) {
			d.MusicUnderDialogueDB = g.ptr(g.Float(-40, 0))
		}
		if g.chance(0.3) {
			d.Attack = g.ptr(g.Float(0.02, 2))
		}
		if g.chance(0.3) {
			d.Release = g.ptr(g.Float(0.02, 3))
		}
		m.Ducking = d
	}
	if g.chance(0.5) {
		l := &editspec.MasterLoudness{}
		if g.chance(0.6) {
			l.TargetLUFS = g.ptr(g.Float(-31, -8))
		}
		if g.chance(0.4) {
			l.TruePeakDBTP = g.ptr(g.Float(-9, 0))
		}
		if g.chance(0.4) {
			l.Normalize = g.boolPtr()
		}
		m.Loudness = l
	}
	if g.chance(0.4) {
		m.LevelSpeech = g.boolPtr()
	}
	if g.chance(0.4) {
		m.CutBlend = g.ptr(g.Float(0, 0.5))
	}
	m.Locked = g.chance(0.15)
	return m
}

func clipIDsOf(s *editspec.Spec) []string {
	ids := make([]string, 0, len(s.Clips))
	for _, c := range s.Clips {
		ids = append(ids, c.ID)
	}
	return ids
}

// Spec returns a random, schema-valid, normalized spec.
func (g *Gen) Spec() *editspec.Spec {
	used := map[string]bool{}
	s := &editspec.Spec{SchemaVersion: 1}
	if g.chance(0.7) {
		s.Title = g.Text(200)
	}
	presets := []string{editspec.PresetVertical, editspec.PresetLandscape, editspec.PresetSquare}
	switch g.R.Intn(3) {
	case 0:
		s.Output.Preset = presets[g.R.Intn(3)]
	case 1:
		s.Output.Width, s.Output.Height = 64+2*g.R.Intn(2000), 64+2*g.R.Intn(2000)
	default:
		s.Output.Preset = presets[g.R.Intn(3)]
		s.Output.Width, s.Output.Height = 64+2*g.R.Intn(2000), 64+2*g.R.Intn(2000)
	}
	if g.chance(0.7) {
		s.Output.FPS = []int{24, 25, 30, 50, 60}[g.R.Intn(5)]
	}
	if g.chance(0.4) {
		s.Output.Background = fmt.Sprintf("#%06X", g.R.Intn(1<<24))
	}
	if g.chance(0.6) {
		s.Output.Platform = g.pick("generic", "youtube", "shorts", "tiktok", "reels")
	}
	n := 1 + g.R.Intn(6)
	for i := 0; i < n; i++ {
		s.Clips = append(s.Clips, g.Clip(g.ID(used)))
	}
	if g.chance(0.5) {
		for i := 0; i < 1+g.R.Intn(3); i++ {
			s.Audio = append(s.Audio, g.Audio(g.ID(used)))
		}
	}
	if g.chance(0.6) {
		s.Captions = g.Captions()
	}
	if g.chance(0.5) {
		for i := 0; i < 1+g.R.Intn(3); i++ {
			s.Overlays = append(s.Overlays, g.Overlay(g.ID(used)))
		}
	}
	if g.chance(0.4) {
		b := &editspec.Brand{}
		if g.chance(0.6) {
			b.Primary = fmt.Sprintf("#%06x", g.R.Intn(1<<24))
		}
		if g.chance(0.5) {
			b.Accent = fmt.Sprintf("#%06X", g.R.Intn(1<<24))
		}
		if g.chance(0.4) {
			b.Text = fmt.Sprintf("#%06x", g.R.Intn(1<<24))
		}
		if g.chance(0.3) {
			b.Background = fmt.Sprintf("#%06x", g.R.Intn(1<<24))
		}
		if g.chance(0.5) {
			b.Font = g.pick("Inter", "Fraunces", "JetBrains Mono")
		}
		s.Brand = b
	}
	if g.chance(0.6) {
		s.Notes = g.Text(4000)
	}
	if g.chance(0.4) {
		s.Voiceover = g.Voiceover(clipIDsOf(s), used)
	}
	if g.chance(0.4) {
		s.Mix = g.Mix()
	}
	if !g.Plain {
		g.extras(s)
	}
	editspec.Normalize(s)
	return s
}

func (g *Gen) xchance(p float64) bool { return g.X.Float64() < p }

func (g *Gen) xhex(n int) string {
	const digits = "0123456789abcdef"
	if g.xchance(0.5) {
		return strings.Repeat(string(digits[g.X.Intn(4)]), n) // a few shared versions
	}
	b := make([]byte, n)
	for i := range b {
		b[i] = digits[g.X.Intn(16)]
	}
	return string(b)
}

// extras adds resource pins, resource parameters, a corner logo, a brand font
// file and nested cuts (drawn from X).
func (g *Gen) extras(s *editspec.Spec) {
	if g.xchance(0.5) {
		s.Resources = map[string]editspec.ResourcePin{}
		names := []string{"logo", "logo-dark", "team_photo", "deck", "brand-font", "x"}
		for i := 0; i < 1+g.X.Intn(4); i++ {
			s.Resources[names[g.X.Intn(len(names))]] = editspec.ResourcePin{ID: Resources[g.X.Intn(len(Resources))], SHA256: g.xhex(64)}
		}
	}
	pinned := editspec.ResourceNames(s)
	ref := func() string {
		if len(pinned) == 0 || g.xchance(0.2) {
			return "unpinned-name"
		}
		n := pinned[g.X.Intn(len(pinned))]
		if g.xchance(0.2) {
			n += fmt.Sprintf("#%d", 1+g.X.Intn(12))
		}
		return n
	}
	for i := range s.Overlays {
		o := &s.Overlays[i]
		if o.Motion != nil {
			continue
		}
		switch {
		case g.xchance(0.12):
			o.Template = editspec.TplLogoCorner
			m := map[string]any{"logo": ref()}
			if g.xchance(0.5) {
				m["corner"] = []string{"top-right", "top-left", "bottom-right", "bottom-left"}[g.X.Intn(4)]
			}
			if g.xchance(0.4) {
				m["size"] = []string{"small", "medium", "large"}[g.X.Intn(3)]
			}
			if g.xchance(0.3) {
				m["opacity"] = math.Round((0.2+unfused(0.8*g.X.Float64()))*100) / 100
			}
			o.Params, _ = json.Marshal(m)
		case g.xchance(0.5):
			t, _ := editspec.TemplateByID(o.Template)
			if len(t.ResourceParams) == 0 {
				continue
			}
			var m map[string]any
			_ = json.Unmarshal(o.Params, &m)
			if m == nil {
				m = map[string]any{}
			}
			for _, p := range t.ResourceParams {
				if g.xchance(0.6) {
					m[p] = ref()
				}
			}
			o.Params, _ = json.Marshal(m)
		}
	}
	if len(pinned) > 0 && g.xchance(0.25) {
		if s.Brand == nil {
			s.Brand = &editspec.Brand{}
		}
		s.Brand.FontFile = pinned[g.X.Intn(len(pinned))]
	}
	if len(s.Clips) > 0 && g.xchance(0.3) {
		c := &s.Clips[g.X.Intn(len(s.Clips))]
		c.AssetID = NestedCuts[g.X.Intn(len(NestedCuts))]
		c.Speed = 0
		c.Framing = nil
	}
	if g.C != nil && g.C.Float64() < 0.35 {
		g.conversations(s)
	}
}

// conversations pins one or two conversations and turns clips into
// conversation clips with shots (drawn from C).
func (g *Gen) conversations(s *editspec.Spec) {
	c := g.C
	ch := func(p float64) bool { return c.Float64() < p }
	num := func(lo, hi float64) float64 {
		v := lo + unfused(c.Float64()*(hi-lo))
		if ch(0.5) {
			return math.Round(v*1000) / 1000
		}
		return v
	}
	text := func(n int) string {
		saved := g.R
		g.R = c
		t := g.Text(n)
		g.R = saved
		if strings.TrimSpace(t) == "" {
			t = "Maya"
		}
		return t
	}
	s.Conversations = map[string]editspec.ConversationPin{}
	names := []string{"call", "interview", "panel-2"}
	memberIDs := []string{"maya", "sam", "gallery", "member", "card", "x_1"}
	n := 1 + c.Intn(2)
	var convs []string
	for k := 0; k < n; k++ {
		name := names[k]
		pin := editspec.ConversationPin{ID: fmt.Sprintf("cnv_01jabcdefghjkmnpqrstvwx%02d", k)}
		for i := 0; i < 1+c.Intn(3); i++ {
			m := editspec.ConversationMember{ID: memberIDs[(i+c.Intn(2)*3)%len(memberIDs)], Name: text(80), AssetID: Assets[c.Intn(len(Assets))], Start: num(-20, 600)}
			if hasMember(pin, m.ID) {
				continue
			}
			if ch(0.3) {
				m.Use = []string{"both", "sound", "picture"}[c.Intn(3)]
			}
			if ch(0.4) {
				m.Drift = num(-500, 500)
			}
			if ch(0.2) {
				m.PersonID = "per_01jabcdefghjkmnpqrstvwxyz0"
			}
			if ch(0.3) {
				m.Tag = text(80)
			}
			if ch(0.3) {
				m.Crop = &editspec.Crop{}
				if ch(0.7) {
					m.Crop.Top = num(0, 0.3)
				}
				if ch(0.5) {
					m.Crop.Left = num(0, 0.3)
				}
			}
			if ch(0.4) {
				m.Framing = &editspec.Framing{}
				if ch(0.6) {
					m.Framing.Auto = editspec.FramingFaces
				}
				if ch(0.3) {
					m.Framing.Mode = []string{"fill", "fit"}[c.Intn(2)]
				}
				if ch(0.3) {
					x := num(0, 1)
					m.Framing.FocusX = &x
				}
				if ch(0.3) {
					m.Framing.Zoom = num(1, 3)
				}
			}
			if ch(0.3) {
				m.Look = &editspec.Look{Exposure: num(0.5, 2), Warmth: num(-0.3, 0.3)}
				if ch(0.5) {
					m.Look.Contrast, m.Look.Saturation, m.Look.Tint = num(0.5, 1.5), num(0.2, 2), num(-0.3, 0.3)
				}
			}
			m.Muted = ch(0.1)
			pin.Members = append(pin.Members, m)
		}
		switch c.Intn(4) {
		case 0:
			pin.Gate = editspec.NewGate()
		case 1:
			pin.Gate = &editspec.ConversationGate{Mode: editspec.GateSpeaking}
		case 2:
			pin.Gate = &editspec.ConversationGate{Mode: editspec.GateOff}
		}
		if ch(0.3) {
			pin.NameTags = []string{"on", "off"}[c.Intn(2)]
		}
		pin.SampleAligned = ch(0.5)
		s.Conversations[name] = pin
		convs = append(convs, name)
	}
	for i := range s.Clips {
		if !ch(0.4) {
			continue
		}
		cl := &s.Clips[i]
		name := convs[c.Intn(len(convs))]
		pin := s.Conversations[name]
		cl.AssetID, cl.Speed, cl.Framing = editspec.ConvKey(name), 0, nil
		at := cl.In
		for k := 0; k < c.Intn(6); k++ {
			sh := editspec.Shot{At: math.Round(at*1000) / 1000}
			m := pin.Members[c.Intn(len(pin.Members))].ID
			switch c.Intn(4) {
			case 0:
				sh.Member = m
			case 1:
				sh.Layout = editspec.Layouts[c.Intn(len(editspec.Layouts))]
				if ch(0.5) {
					sh.Members = []string{m}
				}
			case 2:
				sh.Card = text(240)
				if ch(0.5) {
					sh.Members = []string{m}
				}
			default:
				sh.BRoll = &editspec.ShotBRoll{AssetID: Assets[c.Intn(len(Assets))], In: num(0, 30)}
			}
			if ch(0.3) {
				sh.Dissolve = num(0, 2)
			}
			if ch(0.4) {
				sh.Rationale = text(1000)
			}
			cl.Shots = append(cl.Shots, sh)
			at += 0.001 + num(0, 8)
		}
		if ch(0.25) {
			cl.MutedMembers = []string{pin.Members[0].ID}
		}
	}
}

func hasMember(p editspec.ConversationPin, id string) bool {
	for _, m := range p.Members {
		if m.ID == id {
			return true
		}
	}
	return false
}

// Mutate returns a changed copy of s: a few random edits of the kinds people
// make (trim, reorder, insert, remove, retime graphics, toggle locks...).
func (g *Gen) Mutate(s *editspec.Spec) *editspec.Spec {
	out := editspec.Clone(s)
	used := map[string]bool{}
	for _, c := range out.Clips {
		used[c.ID] = true
	}
	for _, a := range out.Audio {
		used[a.ID] = true
	}
	for _, o := range out.Overlays {
		used[o.ID] = true
	}
	if out.Voiceover != nil {
		for _, b := range out.Voiceover.Beats {
			used[b.ID] = true
		}
	}
	if !g.Plain && out.Resources != nil && g.xchance(0.3) {
		for n, p := range out.Resources {
			if g.xchance(0.5) {
				p.SHA256 = g.xhex(64)
				out.Resources[n] = p
			} else if g.xchance(0.3) {
				delete(out.Resources, n)
			}
			break
		}
		if len(out.Resources) == 0 {
			out.Resources = nil
		}
	}
	n := 1 + g.R.Intn(4)
	for k := 0; k < n; k++ {
		switch g.R.Intn(16) {
		case 0: // trim
			i := g.R.Intn(len(out.Clips))
			out.Clips[i].Out = out.Clips[i].In + 0.1 + g.Float(0, 10)
		case 1: // reorder
			if len(out.Clips) > 1 {
				i, j := g.R.Intn(len(out.Clips)), g.R.Intn(len(out.Clips))
				out.Clips[i], out.Clips[j] = out.Clips[j], out.Clips[i]
			}
		case 2: // insert
			c := g.Clip(g.ID(used))
			i := g.R.Intn(len(out.Clips) + 1)
			out.Clips = append(out.Clips[:i], append([]editspec.Clip{c}, out.Clips[i:]...)...)
		case 3: // remove
			if len(out.Clips) > 1 {
				i := g.R.Intn(len(out.Clips))
				out.Clips = append(out.Clips[:i], out.Clips[i+1:]...)
			}
		case 4: // replace attributes
			i := g.R.Intn(len(out.Clips))
			id := out.Clips[i].ID
			out.Clips[i] = g.Clip(id)
		case 5: // lock toggle
			i := g.R.Intn(len(out.Clips))
			out.Clips[i].Locked = !out.Clips[i].Locked
		case 6: // overlays
			switch {
			case len(out.Overlays) > 0 && g.chance(0.5):
				i := g.R.Intn(len(out.Overlays))
				out.Overlays[i].At = g.Float(0, 30)
			case len(out.Overlays) > 1 && g.chance(0.5):
				out.Overlays[0], out.Overlays[1] = out.Overlays[1], out.Overlays[0]
			default:
				out.Overlays = append(out.Overlays, g.Overlay(g.ID(used)))
			}
		case 7: // audio
			switch {
			case len(out.Audio) > 0 && g.chance(0.4):
				out.Audio = out.Audio[1:]
			case len(out.Audio) > 0 && g.chance(0.5):
				i := g.R.Intn(len(out.Audio))
				out.Audio[i].GainDB = g.ptr(g.Float(-30, 0))
			default:
				out.Audio = append(out.Audio, g.Audio(g.ID(used)))
			}
		case 8:
			if g.chance(0.5) {
				out.Captions = g.Captions()
			} else {
				out.Captions = nil
			}
		case 9:
			out.Title = g.Text(200)
			out.Notes = g.Text(500)
		case 10:
			out.Output.FPS = []int{24, 25, 30, 50, 60}[g.R.Intn(5)]
			out.Output.Platform = g.pick("generic", "youtube", "shorts", "tiktok", "reels")
		case 11:
			i := g.R.Intn(len(out.Clips))
			out.Clips[i].Volume = nil
			out.Clips[i].Muted = false
			out.Clips[i].Framing = nil
			out.Clips[i].Notes = g.Text(200)
		case 12: // narration lines: reword, re-place, reorder, add, remove, lock
			v := out.Voiceover
			if v == nil {
				out.Voiceover = g.Voiceover(clipIDsOf(out), used)
				break
			}
			switch {
			case len(v.Beats) > 0 && g.chance(0.3):
				i := g.R.Intn(len(v.Beats))
				v.Beats[i].Text = g.nonEmpty(200)
				v.Beats[i].Anchor = g.Anchor(clipIDsOf(out))
			case len(v.Beats) > 1 && g.chance(0.3):
				i, j := g.R.Intn(len(v.Beats)), g.R.Intn(len(v.Beats))
				v.Beats[i], v.Beats[j] = v.Beats[j], v.Beats[i]
			case len(v.Beats) > 0 && g.chance(0.3):
				i := g.R.Intn(len(v.Beats))
				v.Beats = append(v.Beats[:i], v.Beats[i+1:]...)
			case len(v.Beats) > 0 && g.chance(0.3):
				i := g.R.Intn(len(v.Beats))
				v.Beats[i].Locked = !v.Beats[i].Locked
				v.Beats[i].Delivery = g.Text(100)
			default:
				b := g.Beat(g.ID(used), clipIDsOf(out))
				i := g.R.Intn(len(v.Beats) + 1)
				v.Beats = append(v.Beats[:i], append([]editspec.Beat{b}, v.Beats[i:]...)...)
			}
		case 13: // the whole narration: settings, layout, lock, or none
			switch {
			case out.Voiceover == nil || g.chance(0.2):
				if g.chance(0.5) {
					out.Voiceover = nil
				} else {
					out.Voiceover = g.Voiceover(clipIDsOf(out), used)
				}
			case g.chance(0.4):
				out.Voiceover.Settings = &editspec.VoiceoverSettings{FitPolicy: g.pick("auto", "ask", "shorten"), Gap: g.Float(0, 5)}
			case g.chance(0.5):
				out.Voiceover.Locked = !out.Voiceover.Locked
			default:
				out.Voiceover.Layout = g.pick("", "beats", "continuous")
			}
		case 14: // the mix
			switch {
			case out.Mix == nil || g.chance(0.3):
				out.Mix = g.Mix()
			case g.chance(0.3):
				out.Mix = nil
			case g.chance(0.5):
				out.Mix.Locked = !out.Mix.Locked
			default:
				out.Mix.Music = &editspec.Bus{GainDB: g.ptr(g.Float(-30, 0))}
				out.Mix.MuteSource = !out.Mix.MuteSource
			}
		case 15:
			if out.Captions != nil {
				out.Captions.Source = g.pick("voiceover", "both", "dialogue", "")
				out.Captions.ShowSpeakers = !out.Captions.ShowSpeakers
			}
		}
	}
	editspec.Normalize(out)
	return out
}
