package editspec

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// House style (D199): what new cuts of every project using a set of Content
// Settings start with — captions, the name bar, the lower thirds' look,
// framing, sound at cuts, and an intro and an end card. It is kept in Content
// Settings (internal/content) and read here, where the timing and the cut's
// defaults live: NewCutDefaults fills a new cut's unset settings from it, and
// ConformStyle/StyleOps make an existing cut follow it as one checked change
// ("Use the house style on the latest cut"). With nothing set, nothing changes:
// new cuts get exactly what they got before.

// HouseStyle is a house's style. Empty fields leave MetaRush's own choices.
type HouseStyle struct {
	// Captions: on or off, their look, size and position.
	Captions *StyleCaptions `json:"captions,omitempty" bson:"captions,omitempty" jsonschema:"description=Captions: on or off\\, their look\\, size and position."`
	// NameBar is how the name bar over names a call app printed looks (D162).
	NameBar *StyleNameBar `json:"nameBar,omitempty" bson:"nameBar,omitempty" jsonschema:"description=The name bar: its look\\, height and whether it animates."`
	// LowerThird is the look of name titles: lower-third.clean or lower-third.cinematic.
	LowerThird string `json:"lowerThird,omitempty" bson:"lowerThird,omitempty" jsonschema:"enum=,enum=lower-third.clean,enum=lower-third.cinematic,description=The look of name titles at the bottom: lower-third.clean or lower-third.cinematic."`
	// Framing is how crops are framed (output.framing): faces, follow or center (fixed).
	Framing string `json:"framing,omitempty" bson:"framing,omitempty" jsonschema:"enum=,enum=faces,enum=follow,enum=center,description=How crops are framed: faces (keep faces in frame)\\, follow (move with the subject) or center (fixed: the middle)."`
	// Sound is sound at cuts (D54, D55): the blend at straight cuts and whether speech is evened out.
	Sound *StyleSound `json:"sound,omitempty" bson:"sound,omitempty" jsonschema:"description=Sound at cuts: the blend at straight cuts and whether voices are evened out."`
	// Intro is a card new cuts open with; EndCard one they end with.
	Intro   *StyleCard `json:"intro,omitempty" bson:"intro,omitempty" jsonschema:"description=A card new cuts open with: title.card or slide.title\\, showing the cut's title unless it has its own words."`
	EndCard *StyleCard `json:"endCard,omitempty" bson:"endCard,omitempty" jsonschema:"description=A card new cuts end with: endcard.cta (headline\\, subline\\, call to action\\, handle) or slide.title."`
}

// StyleCaptions are captions in a house style.
type StyleCaptions struct {
	// On: new cuts have captions (true) or not (false); absent leaves it to the cut.
	On       *bool  `json:"on,omitempty" bson:"on,omitempty" jsonschema:"description=Captions on (true) or off (false); absent leaves it to each cut."`
	Look     string `json:"look,omitempty" bson:"look,omitempty" jsonschema:"enum=,enum=clean,enum=emphasis,enum=boxed,enum=bold,enum=highlight,enum=subtitle,description=clean\\, emphasis (the spoken word lights up)\\, boxed\\, bold (big capitals for phones)\\, highlight (a box follows the spoken word) or subtitle (plain subtitles)."`
	Size     string `json:"size,omitempty" bson:"size,omitempty" jsonschema:"enum=,enum=small,enum=medium,enum=large"`
	Position string `json:"position,omitempty" bson:"position,omitempty" jsonschema:"enum=,enum=bottom,enum=lower,enum=middle,enum=top"`
}

// StyleNameBar is the name bar in a house style.
type StyleNameBar struct {
	Look    string `json:"look,omitempty" bson:"look,omitempty" jsonschema:"enum=,enum=band,enum=tab,description=band: across the picture; tab: only as wide as the names."`
	Height  string `json:"height,omitempty" bson:"height,omitempty" jsonschema:"enum=,enum=full,enum=half,description=full or half: about half as tall (it still covers the printed names)."`
	Animate string `json:"animate,omitempty" bson:"animate,omitempty" jsonschema:"enum=,enum=fade,enum=slide,enum=never,description=How a name comes in: fade\\, slide or never (nothing moves)."`
}

// StyleSound is sound at cuts in a house style.
type StyleSound struct {
	// CutBlend is the blend at straight cuts in seconds (mix.cutBlend).
	CutBlend *float64 `json:"cutBlend,omitempty" bson:"cutBlend,omitempty" jsonschema:"minimum=0,maximum=0.5,description=Seconds the sound blends over at a straight cut (MetaRush's own: 0.1)."`
	// LevelSpeech evens out how loud people speak from clip to clip (mix.levelSpeech).
	LevelSpeech *bool `json:"levelSpeech,omitempty" bson:"levelSpeech,omitempty" jsonschema:"description=Even out how loud people speak from clip to clip (MetaRush's own: on)."`
}

// StyleCard is a card new cuts open or end with, and its words.
type StyleCard struct {
	// Template is title.card or slide.title (intro), endcard.cta or slide.title (end card).
	Template string `json:"template" bson:"template" jsonschema:"enum=title.card,enum=slide.title,enum=endcard.cta,enum=,enum=none,description=The card's template. When changing the house style\\, empty or none takes the card away."`
	// Seconds it stays (default 3 for an intro, 4 for an end card).
	Seconds float64 `json:"seconds,omitempty" bson:"seconds,omitempty" jsonschema:"minimum=1.5,maximum=10"`
	// Title: an intro's title (default: the cut's title); an end card's headline.
	Title string `json:"title,omitempty" bson:"title,omitempty" jsonschema:"maxLength=80,description=An intro's title (default: the cut's title); an end card's headline."`
	// Subtitle: a line under it (an end card's subline).
	Subtitle string `json:"subtitle,omitempty" bson:"subtitle,omitempty" jsonschema:"maxLength=120"`
	// CTA and Handle: an end card's call to action and handle (endcard.cta).
	CTA    string `json:"cta,omitempty" bson:"cta,omitempty" jsonschema:"maxLength=40"`
	Handle string `json:"handle,omitempty" bson:"handle,omitempty" jsonschema:"maxLength=40"`
}

// Defaults for the house's cards.
const (
	DefaultIntroSeconds   = 3.0
	DefaultEndCardSeconds = 4.0
)

// Empty reports a style with nothing chosen.
func (h *HouseStyle) Empty() bool {
	if h == nil {
		return true
	}
	c := h.Captions
	nb := h.NameBar
	so := h.Sound
	return (c == nil || (c.On == nil && c.Look == "" && c.Size == "" && c.Position == "")) &&
		(nb == nil || (nb.Look == "" && nb.Height == "" && nb.Animate == "")) &&
		h.LowerThird == "" && h.Framing == "" && (so == nil || (so.CutBlend == nil && so.LevelSpeech == nil)) &&
		(h.Intro == nil || h.Intro.Template == "") && (h.EndCard == nil || h.EndCard.Template == "")
}

// Clean drops empty groups, so an emptied style is stored as nothing.
func (h *HouseStyle) Clean() *HouseStyle {
	if h == nil {
		return nil
	}
	x := *h
	if c := x.Captions; c != nil && c.On == nil && c.Look == "" && c.Size == "" && c.Position == "" {
		x.Captions = nil
	}
	if nb := x.NameBar; nb != nil && nb.Look == "" && nb.Height == "" && nb.Animate == "" {
		x.NameBar = nil
	}
	if so := x.Sound; so != nil && so.CutBlend == nil && so.LevelSpeech == nil {
		x.Sound = nil
	}
	if x.Intro != nil && x.Intro.Template == "" {
		x.Intro = nil
	}
	if x.EndCard != nil && x.EndCard.Template == "" {
		x.EndCard = nil
	}
	if x.Empty() {
		return nil
	}
	return &x
}

func oneOf(v string, allowed ...string) bool {
	if v == "" {
		return true
	}
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// Problem says, in plain words, what is wrong with a style ("" nothing).
func (h *HouseStyle) Problem() string {
	if h == nil {
		return ""
	}
	if c := h.Captions; c != nil {
		switch {
		case c.Look != "" && !IsCaptionLook(c.Look):
			return fmt.Sprintf("Captions look clean, emphasis, boxed, bold, highlight or subtitle, not %q.", c.Look)
		case !oneOf(c.Size, CaptionSmall, CaptionMedium, CaptionLarge):
			return fmt.Sprintf("Captions are small, medium or large, not %q.", c.Size)
		case !oneOf(c.Position, "bottom", "lower", "middle", "top"):
			return fmt.Sprintf("Captions sit at the bottom, lower, in the middle or at the top, not %q.", c.Position)
		}
	}
	if nb := h.NameBar; nb != nil {
		switch {
		case !oneOf(nb.Look, NameBarBand, NameBarTab):
			return fmt.Sprintf("The name bar is a band or a tab, not %q.", nb.Look)
		case !oneOf(nb.Height, NameBarFull, NameBarHalf):
			return fmt.Sprintf("The name bar is full height or half, not %q.", nb.Height)
		case !oneOf(nb.Animate, NameBarFade, NameBarSlide, NameBarStill):
			return fmt.Sprintf("The name comes in with a fade, a slide or never moves, not %q.", nb.Animate)
		}
	}
	if !oneOf(h.LowerThird, TplLowerThirdClean, TplLowerThirdCinematic) {
		return fmt.Sprintf("Name titles are clean or cinematic, not %q.", h.LowerThird)
	}
	if !oneOf(h.Framing, FramingFaces, FramingFollow, FramingCenter) {
		return fmt.Sprintf("Framing keeps faces in frame, follows movement or stays fixed, not %q.", h.Framing)
	}
	if so := h.Sound; so != nil && so.CutBlend != nil && (*so.CutBlend < 0 || *so.CutBlend > 0.5) {
		return "The blend at straight cuts is between 0 and 0.5 seconds."
	}
	for _, c := range []struct {
		card *StyleCard
		what string
		ok   []string
	}{{h.Intro, "The intro", []string{TplTitleCard, TplSlideTitle}}, {h.EndCard, "The end card", []string{TplEndCard, TplSlideTitle}}} {
		k := c.card
		if k == nil || k.Template == "" {
			continue
		}
		if !oneOf(k.Template, c.ok...) {
			return fmt.Sprintf("%s is a %s, not %q.", c.what, strings.Join(templateWords(c.ok), " or a "), k.Template)
		}
		if k.Seconds != 0 && (k.Seconds < 1.5 || k.Seconds > 10) {
			return c.what + " stays between 1.5 and 10 seconds."
		}
		if len([]rune(k.Title)) > 80 || len([]rune(k.Subtitle)) > 120 || len([]rune(k.CTA)) > 40 || len([]rune(k.Handle)) > 40 {
			return c.what + "'s words are too long (a title or headline 80 letters, the line under it 120, a call to action or handle 40)."
		}
		if c.card == h.EndCard && strings.TrimSpace(k.Title) == "" {
			return "The end card needs its words: a headline (and, if you like, a line under it, a call to action and a handle)."
		}
	}
	return ""
}

func templateWords(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = TemplateWords(id)
	}
	return out
}

// TemplateWords names a card or title template the way people say it.
func TemplateWords(id string) string {
	switch id {
	case TplTitleCard:
		return "title card"
	case TplSlideTitle:
		return "title slide"
	case TplEndCard:
		return "end card"
	case TplLowerThirdClean:
		return "clean name title"
	case TplLowerThirdCinematic:
		return "cinematic name title"
	case TplEndScreen:
		return "end screen"
	case TplSlideQuote:
		return "quote slide"
	case TplQuoteKinetic:
		return "word-by-word quote"
	}
	return id
}

// StyleWords says a style in plain words, one line per part that is set.
func StyleWords(h *HouseStyle) []string {
	if h.Empty() {
		return nil
	}
	var out []string
	if c := h.Captions; c != nil {
		var bits []string
		switch {
		case c.On != nil && *c.On:
			bits = append(bits, "on")
		case c.On != nil:
			bits = append(bits, "off")
		}
		if c.Look != "" {
			bits = append(bits, c.Look)
		}
		if c.Size != "" {
			bits = append(bits, c.Size)
		}
		if c.Position != "" {
			bits = append(bits, map[string]string{"bottom": "at the bottom", "lower": "low on the picture", "middle": "in the middle", "top": "at the top"}[c.Position])
		}
		if len(bits) > 0 {
			out = append(out, "Captions: "+strings.Join(bits, ", "))
		}
	}
	if nb := h.NameBar; nb != nil {
		var bits []string
		switch nb.Look {
		case NameBarBand:
			bits = append(bits, "a band across the picture")
		case NameBarTab:
			bits = append(bits, "a tab only as wide as the names")
		}
		switch nb.Height {
		case NameBarHalf:
			bits = append(bits, "about half as tall (still covering the printed names)")
		case NameBarFull:
			bits = append(bits, "full height")
		}
		switch nb.Animate {
		case NameBarStill:
			bits = append(bits, "never animates")
		case NameBarSlide:
			bits = append(bits, "names slide in")
		case NameBarFade:
			bits = append(bits, "names fade in")
		}
		if len(bits) > 0 {
			out = append(out, "Name bar: "+strings.Join(bits, ", "))
		}
	}
	if h.LowerThird != "" {
		out = append(out, "Name titles: "+strings.TrimSuffix(TemplateWords(h.LowerThird), " name title"))
	}
	switch h.Framing {
	case FramingFaces:
		out = append(out, "Framing: keeps faces in frame")
	case FramingFollow:
		out = append(out, "Framing: follows movement")
	case FramingCenter:
		out = append(out, "Framing: fixed (the middle of the picture)")
	}
	if so := h.Sound; so != nil {
		var bits []string
		if so.CutBlend != nil {
			if *so.CutBlend == 0 {
				bits = append(bits, "no blend at straight cuts")
			} else {
				bits = append(bits, fmt.Sprintf("a %s s blend at straight cuts", trimNum(*so.CutBlend)))
			}
		}
		if so.LevelSpeech != nil {
			if *so.LevelSpeech {
				bits = append(bits, "voices evened out")
			} else {
				bits = append(bits, "voices left as recorded")
			}
		}
		if len(bits) > 0 {
			out = append(out, "Sound at cuts: "+strings.Join(bits, ", "))
		}
	}
	if k := h.Intro; k != nil && k.Template != "" {
		words := "the cut's title"
		if k.Title != "" {
			words = "“" + k.Title + "”"
		}
		out = append(out, fmt.Sprintf("Intro: %s with %s for %s s", aWord(TemplateWords(k.Template)), words, trimNum(cardSeconds(k, DefaultIntroSeconds))))
	}
	if k := h.EndCard; k != nil && k.Template != "" {
		out = append(out, fmt.Sprintf("End card: %s saying “%s” for %s s", aWord(TemplateWords(k.Template)), k.Title, trimNum(cardSeconds(k, DefaultEndCardSeconds))))
	}
	return out
}

// captionLook, captionSize and captionPosition are a cut's caption settings
// as they render (absent: clean, medium, lower).
func captionLook(v string) string {
	if v == "" {
		return CaptionClean
	}
	return v
}

func captionSize(v string) string {
	if v == "" {
		return CaptionMedium
	}
	return v
}

func captionPosition(v string) string {
	if v == "" {
		return "lower"
	}
	return v
}

// captionSizeValue is a size as a cut stores it: medium is absent.
func captionSizeValue(v string) string {
	if v == CaptionMedium {
		return ""
	}
	return v
}

// aWord puts "a" or "an" before a word.
func aWord(w string) string {
	if w != "" && strings.ContainsRune("aeiou", rune(w[0])) {
		return "an " + w
	}
	return "a " + w
}

func trimNum(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
}

func cardSeconds(k *StyleCard, def float64) float64 {
	if k.Seconds > 0 {
		return k.Seconds
	}
	return def
}

// newCutStyle fills a new cut's unset settings from the house style (only for
// a project's first cut on a branch: NewCutDefaults with no base). It returns
// whether anything changed. Sound at cuts and framing are newCutSound's and
// newCutFraming's; this does the rest.
func newCutStyle(s *Spec, h *HouseStyle) bool {
	if h.Empty() {
		return false
	}
	changed := false
	if c := h.Captions; c != nil {
		switch {
		case s.Captions == nil && c.On != nil && *c.On:
			s.Captions = &Captions{Enabled: true, Style: c.Look, Size: c.Size, Position: c.Position}
			changed = true
		case s.Captions != nil:
			if s.Captions.Style == "" && c.Look != "" {
				s.Captions.Style, changed = c.Look, true
			}
			if s.Captions.Size == "" && captionSizeValue(c.Size) != "" {
				s.Captions.Size, changed = c.Size, true
			}
			if s.Captions.Position == "" && c.Position != "" {
				s.Captions.Position, changed = c.Position, true
			}
		}
		if s.Captions != nil && s.Captions.Size == CaptionMedium {
			s.Captions.Size = "" // medium is the size captions always had
		}
	}
	for i := range s.Overlays {
		if styleOverlay(&s.Overlays[i], h, false) {
			changed = true
		}
	}
	if addHouseCards(s, h) {
		changed = true
	}
	return changed
}

// ConformStyle makes a cut follow the house style (the latest cut's offer,
// "Use the house style on the latest cut"): captions, name bars, name titles,
// framing and sound at cuts take the house's choices, and the house's intro
// and end card are added when the cut has none. Locked parts stay as they are.
// It returns whether anything changed.
func ConformStyle(s *Spec, h *HouseStyle) bool {
	if h.Empty() {
		return false
	}
	changed := false
	if c := h.Captions; c != nil && (s.Captions == nil || !s.Captions.Locked) {
		on := s.Captions != nil && s.Captions.Enabled
		switch {
		case c.On != nil && *c.On && s.Captions == nil:
			s.Captions = &Captions{Enabled: true}
			changed = true
		case c.On != nil && *c.On != on && s.Captions != nil:
			s.Captions.Enabled, changed = *c.On, true
		}
		if s.Captions != nil {
			if c.Look != "" && captionLook(s.Captions.Style) != c.Look {
				SetCaptionLook(s.Captions, c.Look)
				changed = true
			}
			if c.Size != "" && captionSize(s.Captions.Size) != captionSize(c.Size) {
				s.Captions.Size, changed = captionSizeValue(c.Size), true
			}
			if c.Position != "" && captionPosition(s.Captions.Position) != c.Position {
				s.Captions.Position, changed = c.Position, true
			}
		}
	}
	for i := range s.Overlays {
		if !s.Overlays[i].Locked && styleOverlay(&s.Overlays[i], h, true) {
			changed = true
		}
	}
	if h.Framing != "" && s.Output.Framing != h.Framing && !(h.Framing == FramingCenter && s.Output.Framing == "") {
		s.Output.Framing, changed = h.Framing, true
	}
	if so := h.Sound; so != nil && (s.Mix == nil || !s.Mix.Locked) {
		m := Mix{}
		if s.Mix != nil {
			m = *s.Mix
		}
		touched := false
		// Absent is what cuts made before these settings do: no leveling, no blend.
		if so.LevelSpeech != nil && (m.LevelSpeech != nil && *m.LevelSpeech) != *so.LevelSpeech {
			v := *so.LevelSpeech
			m.LevelSpeech, touched = &v, true
		}
		blend := 0.0
		if m.CutBlend != nil {
			blend = *m.CutBlend
		}
		if so.CutBlend != nil && math.Abs(blend-*so.CutBlend) > 1e-9 {
			v := *so.CutBlend
			m.CutBlend, touched = &v, true
		}
		if touched {
			s.Mix, changed = &m, true
		}
	}
	if addHouseCards(s, h) {
		changed = true
	}
	return changed
}

// StyleOps are the changes that make a cut (a saved, normalized one) follow
// the house style (ConformStyle), as small ops (DiffOps): none when it follows
// it already.
func StyleOps(s *Spec, h *HouseStyle) ([]Op, error) {
	next := Clone(s)
	if !ConformStyle(next, h) {
		return nil, nil
	}
	Normalize(next)
	return DiffOps(s, next)
}

// styleOverlay gives one graphic the house's name bar or name-title look.
// conform also replaces choices the graphic made itself (a new cut keeps them).
func styleOverlay(o *Overlay, h *HouseStyle, conform bool) bool {
	switch o.Template {
	case TplNameBar:
		nb := h.NameBar
		if nb == nil {
			return false
		}
		var m map[string]json.RawMessage
		if len(o.Params) > 0 && json.Unmarshal(o.Params, &m) != nil {
			return false
		}
		if m == nil {
			m = map[string]json.RawMessage{}
		}
		changed := false
		for _, f := range []struct{ key, v, def string }{{"style", nb.Look, NameBarBand}, {"height", nb.Height, NameBarFull}, {"animate", nb.Animate, NameBarFade}} {
			if f.v == "" {
				continue
			}
			var cur string
			if raw, ok := m[f.key]; ok {
				_ = json.Unmarshal(raw, &cur)
			}
			if cur == f.v || (cur == "" && f.v == f.def) {
				continue
			}
			if cur != "" && !conform {
				continue // the graphic chose its own
			}
			m[f.key], _ = json.Marshal(f.v)
			changed = true
		}
		if changed {
			o.Params, _ = json.Marshal(m)
		}
		return changed
	case TplLowerThirdClean, TplLowerThirdCinematic:
		if GraphicRole(o.Role) {
			return false // a lower third that names its role is the layout pack's (D264)
		}
		want := h.LowerThird
		if want == "" || want == o.Template {
			return false
		}
		t, ok := TemplateByID(want)
		if !ok || o.Span != nil || o.Duration < t.MinDuration || o.Duration > t.MaxDuration {
			return false // the other look can't last this long (or this short)
		}
		o.Template = want
		return true
	}
	return false
}

// addHouseCards adds the house's intro and end card when the cut has none.
func addHouseCards(s *Spec, h *HouseStyle) bool {
	if len(s.Clips) == 0 || ((h.Intro == nil || h.Intro.Template == "") && (h.EndCard == nil || h.EndCard.Template == "")) {
		return false
	}
	c := Clone(s)
	Normalize(c)
	_, total := Timeline(c)
	length := float64(total) / float64(c.Output.FPS)
	logo := ""
	if s.Brand != nil && s.Brand.Logo != "" {
		logo = s.Brand.Logo
	} else if _, ok := s.Resources["logo"]; ok {
		logo = "logo"
	}
	changed := false
	used := 0.0
	// A house card never lands on another card or slide: the intro ends before
	// the first one starts, the end card starts after the last one ends.
	firstCard, lastCard := length, 0.0
	for _, o := range s.Overlays {
		if o.Span != nil || !cardLike(o) {
			continue
		}
		firstCard = math.Min(firstCard, o.At)
		lastCard = math.Max(lastCard, o.At+o.Duration)
	}
	if k := h.Intro; k != nil && k.Template != "" && !hasOpeningCard(s) {
		title := strings.TrimSpace(k.Title)
		if title == "" {
			title = strings.TrimSpace(s.Title)
		}
		if r := []rune(title); len(r) > 80 {
			title = strings.TrimSpace(string(r[:80]))
		}
		secs := fitCard(k, DefaultIntroSeconds, math.Min(length-1, firstCard))
		if title != "" && secs > 0 {
			p := map[string]any{"title": title}
			if k.Subtitle != "" {
				p["subtitle"] = k.Subtitle
			}
			if logo != "" {
				p["logo"] = logo
			}
			raw, _ := json.Marshal(p)
			s.Overlays = append(s.Overlays, Overlay{ID: freeID(s, "intro"), Template: k.Template, Role: PackTitleCard, At: 0, Duration: secs, Params: raw,
				Rationale: "The house style opens every new cut with this card."})
			used, changed = secs, true
		}
	}
	if k := h.EndCard; k != nil && k.Template != "" && strings.TrimSpace(k.Title) != "" && !hasEndCard(s, length) {
		secs := fitCard(k, DefaultEndCardSeconds, math.Min(length-used-1, length-lastCard))
		if secs > 0 {
			p := map[string]any{}
			if k.Template == TplEndCard {
				p["headline"] = k.Title
				if k.Subtitle != "" {
					p["subline"] = k.Subtitle
				}
				if k.CTA != "" {
					p["cta"] = k.CTA
				}
				if k.Handle != "" {
					p["handle"] = k.Handle
				}
			} else {
				p["title"] = k.Title
				if k.Subtitle != "" {
					p["subtitle"] = k.Subtitle
				}
			}
			if logo != "" {
				p["logo"] = logo
			}
			raw, _ := json.Marshal(p)
			at := math.Round((length-secs)*1000) / 1000
			s.Overlays = append(s.Overlays, Overlay{ID: freeID(s, "end-card"), Template: k.Template, Role: PackEndScreen, At: at, Duration: secs, Params: raw,
				Rationale: "The house style ends every new cut with this card."})
			changed = true
		}
	}
	return changed
}

// cardLike reports a graphic that fills the picture or its middle (cards,
// slides, quotes, stat cards, project motion graphics): a house card keeps
// clear of it.
func cardLike(o Overlay) bool {
	if o.Motion != nil {
		return true
	}
	t, ok := TemplateByID(o.Template)
	return ok && (t.Region == "full" || t.Region == "center")
}

// fitCard is how long a house card stays: its seconds (or the default), inside
// its template's limits, and at most room; 0 when it doesn't fit.
func fitCard(k *StyleCard, def, room float64) float64 {
	secs := cardSeconds(k, def)
	if t, ok := TemplateByID(k.Template); ok {
		secs = math.Max(t.MinDuration, math.Min(t.MaxDuration, secs))
		if room < t.MinDuration {
			return 0
		}
	}
	if secs > room {
		secs = room
	}
	return math.Round(secs*1000) / 1000
}

// hasOpeningCard reports a title card or slide at the very start.
func hasOpeningCard(s *Spec) bool {
	for _, o := range s.Overlays {
		if (o.Template == TplTitleCard || o.Template == TplSlideTitle) && o.Span == nil && o.At <= 0.5 {
			return true
		}
	}
	return false
}

// hasEndCard reports an end card, or a card that ends with the video.
func hasEndCard(s *Spec, length float64) bool {
	for _, o := range s.Overlays {
		if o.Template == TplEndCard {
			return true
		}
		if (o.Template == TplTitleCard || o.Template == TplSlideTitle) && o.Span == nil && o.At+o.Duration >= length-0.5 && o.At > 0.5 {
			return true
		}
	}
	return false
}

// freeID is base, or base-2, base-3… when the cut already has that name.
func freeID(s *Spec, base string) string {
	used := map[string]bool{}
	for _, c := range s.Clips {
		used[c.ID] = true
	}
	for _, o := range s.Overlays {
		used[o.ID] = true
	}
	for _, a := range s.Audio {
		used[a.ID] = true
	}
	if s.Voiceover != nil {
		for _, b := range s.Voiceover.Beats {
			used[b.ID] = true
		}
	}
	id := base
	for k := 2; used[id]; k++ {
		id = fmt.Sprintf("%s-%d", base, k)
	}
	return id
}

// NameBarLookOf is a name bar's own look, height and animation ("" each
// when it leaves them to the template's defaults).
func NameBarLookOf(o Overlay) (look, height, animate string) {
	var p NameBarParams
	_ = json.Unmarshal(o.Params, &p)
	return p.Style, p.Height, p.Animate
}

// NameBarStyle is the house's name bar look for a bar MetaRush writes (cut
// names): its look, height and animation, each "" when the house says nothing.
func NameBarStyle(h *HouseStyle) (look, height, animate string) {
	if h == nil || h.NameBar == nil {
		return "", "", ""
	}
	return h.NameBar.Look, h.NameBar.Height, h.NameBar.Animate
}
