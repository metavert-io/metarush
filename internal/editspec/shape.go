package editspec

import "fmt"

// Shape is what shape a video is (D376): vertical, landscape or square, by
// one rule everywhere — Go, the dashboard (web/src/lib/shape.ts, its twin,
// held to the same cases in testdata/shapes.json), QA, motion checks.
type Shape string

// The three shapes.
const (
	ShapeVertical  Shape = "vertical"
	ShapeLandscape Shape = "landscape"
	ShapeSquare    Shape = "square"
)

// The one rule's thresholds (D376) are the renderer's own
// (video/src/core/layout.ts orientationOf, which lays out captions, templates
// and motion graphics): width ÷ height above 1.15 is landscape, below 0.87
// vertical, else square. QA, motion checks and the dashboard use the same, so
// what MetaRush says about a shape is what the renderer draws. The cases in
// testdata/shapes.json hold Go, the dashboard and the renderer to it.
const (
	LandscapeAbove = 1.15
	VerticalBelow  = 0.87
)

// ShapeOf is the shape of a width × height frame ("" when either is missing).
func ShapeOf(w, h int) Shape {
	if w <= 0 || h <= 0 {
		return ""
	}
	switch r := float64(w) / float64(h); {
	case r > LandscapeAbove:
		return ShapeLandscape
	case r < VerticalBelow:
		return ShapeVertical
	}
	return ShapeSquare
}

// Word is the shape as people say it: landscape is "widescreen".
func (s Shape) Word() string {
	if s == ShapeLandscape {
		return "widescreen"
	}
	return string(s)
}

// OutputShape is a video's shape with its size and aspect ratio: what every
// surface reads instead of working the shape out (the dashboard included).
type OutputShape struct {
	Shape  Shape   `json:"shape" jsonschema:"enum=vertical,enum=landscape,enum=square"`
	Width  int     `json:"width"`
	Height int     `json:"height"`
	Aspect float64 `json:"aspect" jsonschema:"description=Width ÷ height (rounded to 4 places)."`
	// Ratio is the aspect ratio in its smallest whole numbers ("16:9",
	// "9:16", "4:5", "1:1"); a size with no small ratio says "1000×1001".
	Ratio string `json:"ratio"`
	// Word is the shape as people say it: widescreen, vertical, square.
	Word string `json:"word"`
}

// ShapeOfSize describes a width × height frame (the zero value when either
// is missing).
func ShapeOfSize(w, h int) OutputShape {
	s := ShapeOf(w, h)
	if s == "" {
		return OutputShape{}
	}
	g := gcd(w, h)
	ratio := fmt.Sprintf("%d:%d", w/g, h/g)
	if w/g > 64 || h/g > 64 {
		ratio = fmt.Sprintf("%d×%d", w, h)
	}
	return OutputShape{Shape: s, Width: w, Height: h, Aspect: float64(int(float64(w)/float64(h)*10000+0.5)) / 10000, Ratio: ratio, Word: s.Word()}
}

// Known reports a size (a shape that isn't the zero value).
func (s OutputShape) Known() bool { return s.Shape != "" }

// Key names the exact size ("1080x1920"): two frames are drawn alike only
// when their keys match, whatever their shape's word.
func (s OutputShape) Key() string {
	if s.Width <= 0 || s.Height <= 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", s.Width, s.Height)
}

// RatioKey is the aspect ratio in smallest whole numbers, always ("16:9",
// "1000:1149"): sizes with the same key are drawn alike, so a graphic drawn
// at 1920×1080 covers a 1280×720 or 3840×2160 cut (D379).
func (s OutputShape) RatioKey() string {
	if s.Width <= 0 || s.Height <= 0 {
		return ""
	}
	g := gcd(s.Width, s.Height)
	return fmt.Sprintf("%d:%d", s.Width/g, s.Height/g)
}

// TestSize is the size a test frame of this ratio is drawn at: the ratio
// with its short side at least 1080 and both sides even (16:9 → 1920×1080,
// 4:5 → 1080×1350), never larger than it needs to be; a ratio that can't
// fit within 4096 keeps its own size, made even (D379).
func TestSize(w, h int) OutputShape {
	if w <= 0 || h <= 0 {
		return OutputShape{}
	}
	g := gcd(w, h)
	a, b := w/g, h/g
	for k := 1; k*max(a, b) <= 4096; k++ {
		x, y := k*a, k*b
		if min(x, y) >= 1080 && x%2 == 0 && y%2 == 0 {
			return ShapeOfSize(x, y)
		}
	}
	x, y := min(w, 4096)&^1, min(h, 4096)&^1
	return ShapeOfSize(max(x, 2), max(y, 2))
}

// Shape is the output's shape: of its width × height, else of its preset's
// size. The zero value when it says neither (Normalize makes such a cut
// vertical; callers that know better decide).
func (o Output) Shape() OutputShape {
	w, h := o.Width, o.Height
	if w <= 0 || h <= 0 {
		d, ok := Presets[o.Preset]
		if !ok {
			return OutputShape{}
		}
		w, h = d[0], d[1]
	}
	return ShapeOfSize(w, h)
}

// PresetFor is the preset of a shape ("" for none).
func PresetFor(s Shape) string {
	switch s {
	case ShapeVertical:
		return PresetVertical
	case ShapeLandscape:
		return PresetLandscape
	case ShapeSquare:
		return PresetSquare
	}
	return ""
}

// ParseShape reads a shape a person or a flag names: vertical, landscape
// (widescreen, horizontal), square, or an exact size like 1080x1350. The
// size is that shape's preset's unless one is given.
func ParseShape(v string) (OutputShape, bool) {
	switch v {
	case "vertical", "portrait":
		v = PresetVertical
	case "landscape", "widescreen", "horizontal":
		v = PresetLandscape
	case "square":
		v = PresetSquare
	}
	if d, ok := Presets[v]; ok {
		return ShapeOfSize(d[0], d[1]), true
	}
	var w, h int
	if n, err := fmt.Sscanf(v, "%dx%d", &w, &h); err == nil && n == 2 && fmt.Sprintf("%dx%d", w, h) == v && w >= 64 && h >= 64 && w <= 4096 && h <= 4096 {
		return ShapeOfSize(w, h), true
	}
	return OutputShape{}, false
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a <= 0 {
		return 1
	}
	return a
}
