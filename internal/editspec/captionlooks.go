package editspec

import "fmt"

// Caption looks (D245): a small set of vetted looks a cut's captions are
// drawn in (Captions.Style), each drawn in the brand's colours and font.
// Go holds them; `metarush schema export` writes schema/caption-looks.json,
// which the renderer (video/src/captions/CaptionLayer.tsx) and the dashboard
// read, and the quality check measures captions with the same numbers.

// The looks added with D245 (clean, emphasis and boxed came first).
const (
	CaptionBold      = "bold"
	CaptionHighlight = "highlight"
	CaptionSubtitle  = "subtitle"
)

// CaptionLookInfo is one look: what people see it called, what it is for and
// how the renderer draws it.
type CaptionLookInfo struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	// Name is what people see it called; Summary says what it looks like and
	// what it suits, in plain words.
	Name    string `json:"name"`
	Summary string `json:"summary"`
	// WordTimed looks show the word being said; without word timings they are
	// drawn as Fallback.
	WordTimed bool   `json:"wordTimed,omitempty"`
	Fallback  string `json:"fallback,omitempty"`
	// WordsPerLine is how many words a line holds when the cut doesn't say.
	WordsPerLine int `json:"wordsPerLine"`
	// Weight is the brand font's weight it uses: display, strong, label or
	// text (the font's regular weight).
	Weight string `json:"weight" jsonschema:"enum=display,enum=strong,enum=label,enum=text"`
	// LineHeight is the line's height in ems; Scale how much larger than the
	// medium caption size it draws (the cut's size on top of it).
	LineHeight float64 `json:"lineHeight"`
	Scale      float64 `json:"scale"`
	// Uppercase draws it in capitals whatever the cut says.
	Uppercase bool `json:"uppercase,omitempty"`
	// Backdrop is what lies behind the words: a soft shade (scrim), a box
	// behind each line (box) or nothing (none: an outline keeps them legible).
	Backdrop string `json:"backdrop" jsonschema:"enum=scrim,enum=box,enum=none"`
	// Outline draws a stroke in the brand's background colour round the words.
	Outline bool `json:"outline,omitempty"`
	// Spoken is how the word being said shows: in the accent colour (color)
	// or on a box in the accent colour (box); "" not at all.
	Spoken string `json:"spoken,omitempty" jsonschema:"enum=,enum=color,enum=box"`
	// Pop makes each line arrive with a small scale-up.
	Pop bool `json:"pop,omitempty"`
}

// CaptionLooks are the vetted looks, in the order people see them.
var CaptionLooks = []CaptionLookInfo{
	{ID: CaptionClean, Version: "1", Name: "Clean", Summary: "White words on a soft shade, for any video.",
		WordsPerLine: 6, Weight: "display", LineHeight: 1.2, Scale: 1, Backdrop: "scrim"},
	{ID: CaptionEmphasis, Version: "1", Name: "The spoken word lights up", Summary: "Each word turns the accent colour as it is said; good for talking heads.",
		WordTimed: true, Fallback: CaptionClean, WordsPerLine: 6, Weight: "strong", LineHeight: 1.2, Scale: 1, Backdrop: "scrim", Spoken: "color"},
	{ID: CaptionBoxed, Version: "1", Name: "Boxed", Summary: "Words on a solid box, readable over busy footage.",
		WordsPerLine: 6, Weight: "label", LineHeight: 1.38, Scale: 1, Backdrop: "box"},
	{ID: CaptionBold, Version: "1", Name: "Big and bold", Summary: "A few large capitals at a time with an outline, for videos watched on phones.",
		WordsPerLine: 3, Weight: "strong", LineHeight: 1.1, Scale: 1.18, Uppercase: true, Backdrop: "none", Outline: true, Pop: true},
	{ID: CaptionHighlight, Version: "1", Name: "A box follows the spoken word", Summary: "The word being said sits on a box in the accent colour; lively, for short videos.",
		WordTimed: true, Fallback: CaptionBoxed, WordsPerLine: 4, Weight: "strong", LineHeight: 1.32, Scale: 1.08, Backdrop: "none", Outline: true, Spoken: "box"},
	{ID: CaptionSubtitle, Version: "1", Name: "Plain subtitles", Summary: "Smaller words with a thin outline and longer lines, like film subtitles.",
		WordsPerLine: 9, Weight: "text", LineHeight: 1.25, Scale: 0.86, Backdrop: "none", Outline: true},
}

// CaptionLook is a look by its ID; an empty or unknown one is Clean.
func CaptionLook(id string) CaptionLookInfo {
	for _, l := range CaptionLooks {
		if l.ID == id {
			return l
		}
	}
	return CaptionLooks[0]
}

// IsCaptionLook reports whether id names one of the looks.
func IsCaptionLook(id string) bool {
	for _, l := range CaptionLooks {
		if l.ID == id {
			return true
		}
	}
	return false
}

// CaptionLookCatalog is schema/caption-looks.json.
type CaptionLookCatalog struct {
	GeneratedBy string            `json:"generatedBy"`
	Looks       []CaptionLookInfo `json:"looks"`
}

// CaptionLookFile returns the caption look catalog `metarush schema export` writes.
func CaptionLookFile() CaptionLookCatalog {
	return CaptionLookCatalog{GeneratedBy: "metarush schema export (internal/editspec CaptionLooks); do not edit", Looks: CaptionLooks}
}

// SetCaptionLook gives captions another look; the words per line follow the
// new look's when the captions had the old look's (nobody chose them).
func SetCaptionLook(c *Captions, look string) {
	old := CaptionLook(c.Style)
	if c.MaxWordsPerLine == 0 || c.MaxWordsPerLine == old.WordsPerLine {
		c.MaxWordsPerLine = CaptionLook(look).WordsPerLine
	}
	c.Style = look
}

// CaptionStyleDiff is how a cut's captions differ from the house style's
// look, size and position (D245; QA's house_style): the captions as the house
// style would have them, and the differences in plain words; nil when they
// agree, the captions are off or the house style says none of the three.
func CaptionStyleDiff(c *Captions, h *HouseStyle) (*Captions, []string) {
	if c == nil || !c.Enabled || h == nil || h.Captions == nil {
		return nil, nil
	}
	hc, nc := h.Captions, *c
	var says []string
	if hc.Look != "" && captionLook(c.Style) != hc.Look {
		SetCaptionLook(&nc, hc.Look)
		says = append(says, fmt.Sprintf("they look “%s” where the house style has “%s”", CaptionLook(c.Style).Name, CaptionLook(hc.Look).Name))
	}
	if hc.Size != "" && captionSize(c.Size) != captionSize(hc.Size) {
		nc.Size = captionSizeValue(hc.Size)
		says = append(says, fmt.Sprintf("they are %s where it has them %s", captionSize(c.Size), captionSize(hc.Size)))
	}
	if hc.Position != "" && captionPosition(c.Position) != hc.Position {
		nc.Position = hc.Position
		says = append(says, fmt.Sprintf("they sit %s where it has them %s", CaptionPlace(captionPosition(c.Position)), CaptionPlace(hc.Position)))
	}
	if len(says) == 0 {
		return nil, nil
	}
	return &nc, says
}

// CaptionPlace says where captions sit, in plain words.
func CaptionPlace(position string) string {
	switch position {
	case "top":
		return "at the top"
	case "middle":
		return "in the middle"
	case "bottom":
		return "at the very bottom"
	}
	return "low on the screen"
}
