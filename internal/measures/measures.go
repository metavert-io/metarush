// Package measures is what the quality check can measure for a rule or a
// promise (D166, D300): the measure's name in a rule (`max-length:60@vertical`),
// recognizing it from a person's words ("under 60 seconds for shorts"),
// checking it, and saying it in a plain sentence. The operations (keeping a
// rule, Claude's keep cards) and the quality check read the same names here.
package measures

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Measure names. Measures with a number carry it after a colon; a length may
// name the shape it is for after an @ (max-length:60@vertical).
const (
	CaptionsTop    = "captions-top"
	CaptionsBottom = "captions-bottom"
	FirstNames     = "first-names"
	NoFlashes      = "no-flashes"
	MaxLength      = "max-length" // max-length:<seconds>[@<landscape|vertical|square>]
	Shape          = "shape"      // shape:<landscape|vertical|square>
	// The promises' measures (D300).
	Captions        = "captions"          // captions on wherever someone speaks, large and slow enough to read
	LogoEnd         = "logo-end"          // logo-end:<seconds>: a logo or an end card for the last seconds
	LogoStart       = "logo-start"        // logo-start:<seconds>: a logo within the first seconds
	SpeechOverMusic = "speech-over-music" // speech-over-music:<steps>: the voices that much louder than the music
	Disclosed       = "disclosed"         // every AI-generated clip, picture or voice disclosed
	FacesInFrame    = "faces-in-frame"    // no face cut off by the frame
	MinSize         = "min-size"          // min-size:<pixels>: the picture's shorter side at least
	MinFPS          = "min-fps"           // min-fps:<frames a second> at least
)

// Measure is a measure read apart.
type Measure struct {
	Kind  string  // one of the names above
	Value float64 // its number (seconds, steps, pixels, frames a second)
	Shape string  // a length's shape, or the shape a shape rule wants
	// Burned: captions burned into the footage count (captions:burned-in).
	Burned bool
}

// Parse reads a measure ("" or unknown: ok false).
func Parse(m string) (Measure, bool) {
	m = strings.TrimSpace(m)
	switch m {
	case CaptionsTop, CaptionsBottom, FirstNames, NoFlashes, Captions, Disclosed, FacesInFrame:
		return Measure{Kind: m}, true
	}
	kind, arg, ok := strings.Cut(m, ":")
	if !ok {
		return Measure{}, false
	}
	switch kind {
	case Captions:
		if arg == "burned-in" {
			return Measure{Kind: Captions, Burned: true}, true
		}
		return Measure{}, false
	case Shape:
		if validShape(arg) {
			return Measure{Kind: Shape, Shape: arg}, true
		}
		return Measure{}, false
	case MaxLength:
		num, shape, scoped := strings.Cut(arg, "@")
		if scoped && !validShape(shape) {
			return Measure{}, false
		}
		v, err := strconv.ParseFloat(num, 64)
		if err != nil || v <= 0 || v > 24*3600 {
			return Measure{}, false
		}
		return Measure{Kind: MaxLength, Value: v, Shape: shape}, true
	case LogoEnd, LogoStart:
		v, err := strconv.ParseFloat(arg, 64)
		if err != nil || v <= 0 || v > 600 {
			return Measure{}, false
		}
		return Measure{Kind: kind, Value: v}, true
	case SpeechOverMusic:
		v, err := strconv.ParseFloat(arg, 64)
		if err != nil || v < 0 || v > 40 {
			return Measure{}, false
		}
		return Measure{Kind: kind, Value: v}, true
	case MinSize:
		v, err := strconv.Atoi(arg)
		if err != nil || v < 120 || v > 8640 {
			return Measure{}, false
		}
		return Measure{Kind: kind, Value: float64(v)}, true
	case MinFPS:
		v, err := strconv.ParseFloat(arg, 64)
		if err != nil || v < 1 || v > 240 {
			return Measure{}, false
		}
		return Measure{Kind: kind, Value: v}, true
	}
	return Measure{}, false
}

// Valid reports a measure the quality check knows ("" is valid: nothing measured).
func Valid(m string) bool {
	if strings.TrimSpace(m) == "" {
		return true
	}
	_, ok := Parse(m)
	return ok
}

// String writes a measure back.
func (m Measure) String() string {
	num := func(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }
	switch m.Kind {
	case Shape:
		return Shape + ":" + m.Shape
	case MaxLength:
		s := MaxLength + ":" + num(m.Value)
		if m.Shape != "" {
			s += "@" + m.Shape
		}
		return s
	case LogoEnd, LogoStart, SpeechOverMusic, MinFPS:
		return m.Kind + ":" + num(m.Value)
	case MinSize:
		return fmt.Sprintf("%s:%d", MinSize, int(m.Value))
	case Captions:
		if m.Burned {
			return Captions + ":burned-in"
		}
	}
	return m.Kind
}

// Usage lists the measures for a refusal's hint.
const Usage = "captions-top, captions-bottom, first-names, no-flashes, max-length:<seconds>[@<shape>], shape:<landscape|vertical|square>, " +
	"captions[:burned-in], logo-end:<seconds>, logo-start:<seconds>, speech-over-music:<steps>, disclosed, faces-in-frame, min-size:<pixels>, min-fps:<frames a second>"

func validShape(s string) bool { return s == "landscape" || s == "vertical" || s == "square" }

var (
	reMinutes    = regexp.MustCompile(`(?i)\b(?:under|at most|no (?:more|longer) than|shorter than|max(?:imum)?(?: of)?|less than|within|up to)\s+(\d+(?:\.\d+)?)\s*(minutes?|mins?|seconds?|secs?|s)\b`)
	reShape      = regexp.MustCompile(`(?i)\b(widescreen|landscape|horizontal|vertical|portrait|square)\b`)
	reForShape   = regexp.MustCompile(`(?i)\b(shorts?|reels?|tiktoks?|stories|vertical|portrait|widescreen|landscape|horizontal|square)\b`)
	reSeconds    = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s*(?:s|secs?|seconds?)\b`)
	reLevel      = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)\s*(?:db|decibels?|steps?|lu|loudness steps?)\b`)
	reLines      = regexp.MustCompile(`(?i)\b(\d{3,4})\s*p\b`)
	reFPS        = regexp.MustCompile(`(?i)\b(\d{2,3}(?:\.\d+)?)\s*(?:fps|frames? (?:a|per) second)\b`)
	reCaptionsOn = regexp.MustCompile(`(?i)\b(?:captions?|subtitles?)\s+(?:on|always|everywhere|throughout|are on|must be on|in every|on every|on all|for every|for all|in all)\b|` +
		`\b(?:with|has|have|include|includes|including|always|readable)\s+(?:burned-in\s+|burnt-in\s+|open\s+)?(?:captions|subtitles)\b|\b(?:captioned|subtitled)\b`)
	reFootageCaptions = regexp.MustCompile(`(?i)\b(?:captions?|subtitles?)\b[^.]*\b(?:already\s+)?(?:in|on|into)\s+(?:the\s+)?(?:footage|recordings?|source|clips)\b|\b(?:footage|recordings?|clips)\b[^.]*\b(?:has|have|carries|carry|comes? with)\b[^.]*\b(?:captions|subtitles)\b`)
	reLogo            = regexp.MustCompile(`(?i)\b(?:logo|end ?card|end ?screen|outro card)s?\b`)
	reEnd             = regexp.MustCompile(`(?i)\b(?:last|final|closing|end|ends|ending|at the end)\b`)
	reStart           = regexp.MustCompile(`(?i)\b(?:first|opening|start|starts|beginning|intro)\b`)
	reEndCard         = regexp.MustCompile(`(?i)\b(?:end ?card|end ?screen|outro card)s?\b`)
)

// For recognizes what the quality check can measure from a rule's words
// ("" when it can't). The first match wins, in the order below.
func For(text string) string {
	t := strings.ToLower(text)
	has := func(xs ...string) bool {
		for _, x := range xs {
			if strings.Contains(t, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("caption", "subtitle") && (has("top") || has("above")):
		return CaptionsTop
	case has("caption", "subtitle") && (has("bottom") || has("below")):
		return CaptionsBottom
	case has("first name"):
		return FirstNames
	case has("flash", "flip", "split-second", "split second"):
		return NoFlashes
	case has("disclos") || (has(" ai", "ai-", "ai ", "generated", "synthetic") && has("label", "say so", "marked", "tell", "flag")):
		return Disclosed
	case has("face") && has("cut off", "cut-off", "cropped", "in frame", "in the frame", "in shot", "chopped"):
		return FacesInFrame
	case has("music") && has("speech", "voice", "talk", "dialogue", "narration", "words", "speaker") &&
		has("louder", "quieter", "over", "above", "under", "below", "drown", "softer"):
		m := Measure{Kind: SpeechOverMusic}
		if x := reLevel.FindStringSubmatch(t); x != nil {
			m.Value, _ = strconv.ParseFloat(x[1], 64)
		}
		if m.Value > 40 {
			m.Value = 40
		}
		return m.String()
	case reLogo.MatchString(t) && (reEnd.MatchString(t) || reEndCard.MatchString(t)) && !reStart.MatchString(t):
		return Measure{Kind: LogoEnd, Value: windowSeconds(t)}.String()
	case reLogo.MatchString(t) && reStart.MatchString(t):
		return Measure{Kind: LogoStart, Value: windowSeconds(t)}.String()
	case reFPS.MatchString(t):
		v, _ := strconv.ParseFloat(reFPS.FindStringSubmatch(t)[1], 64)
		if m := (Measure{Kind: MinFPS, Value: v}); Valid(m.String()) {
			return m.String()
		}
	case reCaptionsOn.MatchString(t) || reFootageCaptions.MatchString(t):
		// "Burned-in" or "open" captions are MetaRush's own, drawn into the
		// video: plain captions. Only captions already in the footage are
		// burned-in (a person says the footage carries them).
		if reFootageCaptions.MatchString(t) {
			return Captions + ":burned-in"
		}
		return Captions
	}
	if m := reMinutes.FindStringSubmatch(t); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		if strings.HasPrefix(m[2], "min") {
			v *= 60
		}
		if v > 0 {
			ms := Measure{Kind: MaxLength, Value: v}
			if s := reForShape.FindStringSubmatch(t); s != nil {
				ms.Shape = shapeOf(s[1])
			}
			return ms.String()
		}
	}
	if px := resolution(t); px > 0 {
		return Measure{Kind: MinSize, Value: float64(px)}.String()
	}
	if m := reShape.FindStringSubmatch(t); m != nil && (has("video", "cut", "format") || len(strings.Fields(t)) <= 3) {
		return Measure{Kind: Shape, Shape: shapeOf(m[1])}.String()
	}
	return ""
}

// windowSeconds is the seconds a logo rule names (3 when it names none).
func windowSeconds(t string) float64 {
	if m := reSeconds.FindStringSubmatch(t); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 0 && v <= 600 {
			return v
		}
	}
	return 3
}

func shapeOf(w string) string {
	switch strings.ToLower(strings.TrimSuffix(strings.ToLower(w), "s")) {
	case "short", "reel", "tiktok", "storie", "stories", "vertical", "portrait":
		return "vertical"
	case "widescreen", "landscape", "horizontal":
		return "landscape"
	}
	return "square"
}

// resolution is the shorter side a rule asks for (0 none): 1080p, 4K, full HD.
func resolution(t string) int {
	if m := reLines.FindStringSubmatch(t); m != nil {
		if v, _ := strconv.Atoi(m[1]); v >= 240 && v <= 4320 {
			return v
		}
	}
	switch {
	case strings.Contains(t, "4k") || strings.Contains(t, "uhd") || strings.Contains(t, "ultra hd"):
		return 2160
	case strings.Contains(t, "full hd") || strings.Contains(t, "fhd"):
		return 1080
	}
	return 0
}

// Words says what the quality check measures, in plain words ("" none).
func Words(m string) string {
	ms, ok := Parse(m)
	if !ok {
		return ""
	}
	switch ms.Kind {
	case CaptionsTop:
		return "the captions are at the top"
	case CaptionsBottom:
		return "the captions are at the bottom"
	case FirstNames:
		return "names on screen are first names only"
	case NoFlashes:
		return "no split-second flashes at the clips' edges"
	case MaxLength:
		s := "the video is at most " + LengthWords(ms.Value)
		if ms.Shape != "" {
			s += " when it is " + ShapeWords(ms.Shape)
		}
		return s
	case Shape:
		return "the video is " + ShapeWords(ms.Shape)
	case Captions:
		if ms.Burned {
			return "captions are on wherever someone speaks — the cut's own, or the ones already in the footage"
		}
		return "captions are on wherever someone speaks, large enough and slow enough to read"
	case LogoEnd:
		return "a logo or an end card is on screen for the last " + secondsWords(ms.Value)
	case LogoStart:
		return "a logo is on screen within the first " + secondsWords(ms.Value)
	case SpeechOverMusic:
		if ms.Value == 0 {
			return "the music is never louder than the voices"
		}
		return fmt.Sprintf("the voices are at least %s louder than the music", stepsWords(ms.Value))
	case Disclosed:
		return "every AI-generated clip, picture and voice is disclosed, on screen or in the version's description"
	case FacesInFrame:
		return "no face is cut off by the edge of the picture"
	case MinSize:
		return fmt.Sprintf("the picture is at least %d pixels on its shorter side", int(ms.Value))
	case MinFPS:
		return fmt.Sprintf("it plays at least %s frames a second", strconv.FormatFloat(ms.Value, 'f', -1, 64))
	}
	return ""
}

// LengthWords says a length: "a minute", "2 minutes", "45 seconds".
func LengthWords(s float64) string {
	if s >= 60 && int(s)%60 == 0 && s == float64(int(s)) {
		n := int(s) / 60
		if n == 1 {
			return "a minute"
		}
		return fmt.Sprintf("%d minutes", n)
	}
	return secondsWords(s)
}

func secondsWords(s float64) string {
	if s == 1 {
		return "second"
	}
	return strconv.FormatFloat(s, 'f', -1, 64) + " seconds"
}

func stepsWords(v float64) string {
	if v == 1 {
		return "1 loudness step"
	}
	return strconv.FormatFloat(v, 'f', -1, 64) + " loudness steps"
}

// ShapeWords names a shape: widescreen, vertical, square.
func ShapeWords(o string) string {
	return map[string]string{"landscape": "widescreen", "vertical": "vertical", "square": "square"}[o]
}
