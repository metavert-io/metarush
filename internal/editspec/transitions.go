package editspec

import (
	"fmt"
	"math"
	"strings"
)

// The transition library. Go plans every transition's timing (overlap windows,
// sequential dips, clamping); Remotion only draws the effect over the planned
// frames. This catalog is the one list of what exists: `metarush edit
// transitions` prints it, `metarush schema export` writes it to
// schema/transitions.json for the dashboard's transition picker, and validation
// uses the same facts (which types overlap, take a direction, and so on).

// TransitionInfo describes one transition type.
type TransitionInfo struct {
	Type             string   `json:"type" jsonschema:"description=The value in a spec (transitionIn.type)."`
	Name             string   `json:"name" jsonschema:"description=What people call it."`
	Summary          string   `json:"summary"`
	WhenToUse        string   `json:"whenToUse"`
	Overlap          bool     `json:"overlap" jsonschema:"description=The two shots are on screen together for the duration (the cut gets that much shorter)."`
	Directions       []string `json:"directions,omitempty"`
	DefaultDirection string   `json:"defaultDirection,omitempty"`
	Easing           bool     `json:"easing" jsonschema:"description=Takes an easing."`
	DefaultDuration  float64  `json:"defaultDuration"`
	MinDuration      float64  `json:"minDuration"`
	MaxDuration      float64  `json:"maxDuration"`
	Sound            string   `json:"sound" jsonschema:"description=What the sound does unless the transition sets audio."`
	Flashy           bool     `json:"flashy" jsonschema:"description=Draws attention to itself: only when the project's transition style asks for it."`
	Styles           []string `json:"styles" jsonschema:"description=Transition styles that use it."`
}

// SoundModeInfo describes one way the sound can change at a cut.
type SoundModeInfo struct {
	Mode            string  `json:"mode"`
	Name            string  `json:"name"`
	Summary         string  `json:"summary"`
	Example         string  `json:"example"`
	TakesDuration   bool    `json:"takesDuration"`
	DefaultDuration float64 `json:"defaultDuration,omitempty"`
	MaxDuration     float64 `json:"maxDuration,omitempty"`
}

// ChoiceInfo is a named option (an easing or a direction).
type ChoiceInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Summary string `json:"summary"`
}

// EdgeInfo describes a fade at the very start or end of the cut.
type EdgeInfo struct {
	Edge        string  `json:"edge" jsonschema:"enum=start,enum=end"`
	Name        string  `json:"name"`
	Summary     string  `json:"summary"`
	How         string  `json:"how" jsonschema:"description=How a spec says it."`
	MaxDuration float64 `json:"maxDuration"`
}

// TransitionStyleInfo is a per-project transition style the editing skills follow.
type TransitionStyleInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Summary    string   `json:"summary"`
	Uses       []string `json:"uses" jsonschema:"description=Transition types this style reaches for."`
	SplitEdits string   `json:"splitEdits" jsonschema:"description=How it uses split edits."`
}

// TransitionsCatalog is schema/transitions.json.
type TransitionsCatalog struct {
	GeneratedBy   string                `json:"generatedBy"`
	Transitions   []TransitionInfo      `json:"transitions"`
	Sound         []SoundModeInfo       `json:"sound"`
	Easings       []ChoiceInfo          `json:"easings"`
	Directions    []ChoiceInfo          `json:"directions"`
	Edges         []EdgeInfo            `json:"edges"`
	Styles        []TransitionStyleInfo `json:"styles"`
	DefaultEasing string                `json:"defaultEasing"`
	CutBlend      float64               `json:"cutBlend" jsonschema:"description=Seconds new cuts blend the sound at straight cuts."`
	SplitBlend    float64               `json:"splitBlend" jsonschema:"description=Seconds a split edit blends the two sounds by default."`
}

// Transition styles (project brief).
const (
	StyleNatural = "natural"
	StyleSnappy  = "snappy"
	StyleSmooth  = "smooth"
	StylePlayful = "playful"
)

// TransitionStyles lists the style IDs.
var TransitionStyles = []string{StyleNatural, StyleSnappy, StyleSmooth, StylePlayful}

var moveDirections = []string{DirLeft, DirRight, DirUp, DirDown}

// Transitions is the transition library, in the order people should consider them.
var Transitions = []TransitionInfo{
	{Type: TransitionCut, Name: "Cut", Summary: "Straight from one shot to the next.", WhenToUse: "Almost always, especially inside a conversation. Let a voice start a moment early or carry over to keep the talk flowing.",
		DefaultDuration: 0, MinDuration: 0, MaxDuration: 0, Sound: "Changes with the picture (new cuts blend it over 0.1 s so it never clicks).", Styles: []string{StyleNatural, StyleSnappy, StyleSmooth, StylePlayful}},
	{Type: TransitionCrossfade, Name: "Dissolve", Summary: "The next shot slowly fades in over this one.", WhenToUse: "Time passing, a change of place, a gentle mood. Not between two people talking.",
		Overlap: true, Easing: true, DefaultDuration: 0.5, MinDuration: 0.1, MaxDuration: 5, Sound: "Blends from one to the other at an even loudness.", Styles: []string{StyleNatural, StyleSmooth}},
	{Type: TransitionDip, Name: "Fade through black", Summary: "This shot fades to black, then the next fades up.", WhenToUse: "Endings, chapter breaks, a long jump in time.",
		DefaultDuration: 0.8, MinDuration: 0.2, MaxDuration: 5, Sound: "Fades out with the picture, then the next shot's sound fades in.", Styles: []string{StyleNatural, StyleSnappy, StyleSmooth, StylePlayful}},
	{Type: TransitionDipWhite, Name: "Fade through white", Summary: "This shot fades to white, then the next fades up.", WhenToUse: "A memory, a dream, a bright upbeat break. Use sparingly.",
		DefaultDuration: 0.8, MinDuration: 0.2, MaxDuration: 5, Sound: "Fades out with the picture, then the next shot's sound fades in.", Styles: []string{StyleSmooth, StylePlayful}},
	{Type: TransitionFade, Name: "Fade in", Summary: "The shot fades in from the background color (black unless the cut sets another).", WhenToUse: "The very first shot, or coming back after a pause.",
		DefaultDuration: 0.6, MinDuration: 0.1, MaxDuration: 5, Sound: "Fades in with the picture.", Styles: []string{StyleNatural, StyleSnappy, StyleSmooth, StylePlayful}},
	{Type: TransitionWipe, Name: "Wipe", Summary: "An edge sweeps across the frame and reveals the next shot.", WhenToUse: "Playful or retro pieces, or a clear \"meanwhile\". Flashy: only when the project's transition style asks for it.",
		Overlap: true, Directions: moveDirections, DefaultDirection: DirLeft, Easing: true, DefaultDuration: 0.5, MinDuration: 0.15, MaxDuration: 3, Sound: "Blends from one to the other over the wipe.", Flashy: true, Styles: []string{StylePlayful}},
	{Type: TransitionSlide, Name: "Slide", Summary: "The next shot slides in over this one.", WhenToUse: "Lists, steps, before/after. Flashy: only when the project's transition style asks for it.",
		Overlap: true, Directions: moveDirections, DefaultDirection: DirLeft, Easing: true, DefaultDuration: 0.4, MinDuration: 0.15, MaxDuration: 3, Sound: "Blends from one to the other over the slide.", Flashy: true, Styles: []string{StylePlayful}},
	{Type: TransitionPush, Name: "Push", Summary: "The next shot pushes this one out of the frame.", WhenToUse: "Energetic sequences, moving on to the next thing. Flashy: only when the project's transition style asks for it.",
		Overlap: true, Directions: moveDirections, DefaultDirection: DirLeft, Easing: true, DefaultDuration: 0.4, MinDuration: 0.15, MaxDuration: 3, Sound: "Blends from one to the other over the push.", Flashy: true, Styles: []string{StyleSnappy, StylePlayful}},
	{Type: TransitionZoom, Name: "Zoom", Summary: "The picture zooms in and through into the next shot.", WhenToUse: "Punchy reveals, hype moments. Flashy: only when the project's transition style asks for it.",
		Overlap: true, Easing: true, DefaultDuration: 0.4, MinDuration: 0.15, MaxDuration: 2, Sound: "Blends from one to the other over the zoom.", Flashy: true, Styles: []string{StylePlayful}},
	{Type: TransitionWhip, Name: "Whip", Summary: "A fast blurred swish, as if the camera whipped over to the next shot.", WhenToUse: "Fast energetic pieces, on a beat. Flashy: only when the project's transition style asks for it.",
		Overlap: true, Directions: moveDirections, DefaultDirection: DirLeft, Easing: true, DefaultDuration: 0.3, MinDuration: 0.1, MaxDuration: 1, Sound: "Blends quickly from one to the other.", Flashy: true, Styles: []string{StyleSnappy, StylePlayful}},
}

// SoundModes lists the ways the sound can change at a cut.
var SoundModes = []SoundModeInfo{
	{Mode: SoundCut, Name: "With the picture", Summary: "The sound changes at the same moment as the picture.", Example: "Sam's voice starts when we see him."},
	{Mode: SoundLead, Name: "Voice starts early", Summary: "The next shot's sound starts a moment before we see it (an editor would call this a J-cut). The shot before it gets quieter as it starts.", Example: "Sam's voice starts 0.4 s before we see him.", TakesDuration: true, DefaultDuration: 0.4, MaxDuration: 3},
	{Mode: SoundCarry, Name: "Voice carries over", Summary: "This shot's sound keeps going for a moment over the next shot (an editor would call this an L-cut).", Example: "Maya's voice carries 0.3 s over the next shot.", TakesDuration: true, DefaultDuration: 0.3, MaxDuration: 3},
	{Mode: SoundAuto, Name: "Into the nearest pause", Summary: "MetaRush moves the sound change into the nearest pause, using the transcript, so no word is cut off.", Example: "The voices change in the pause after \"…and that's how it started.\"", TakesDuration: true, DefaultDuration: DefaultAutoSplit, MaxDuration: 3},
}

// Easings lists the vetted easings.
var Easings = []ChoiceInfo{
	{ID: EaseInOut, Name: "Smooth", Summary: "Starts and ends gently (default)."},
	{ID: EaseLinear, Name: "Even speed", Summary: "The same speed all the way through."},
	{ID: EaseIn, Name: "Speeds up", Summary: "Starts slowly and finishes fast."},
	{ID: EaseOut, Name: "Slows down", Summary: "Starts fast and settles gently."},
}

// Directions lists the movement directions.
var Directions = []ChoiceInfo{
	{ID: DirLeft, Name: "Left", Summary: "The next shot comes in from the right and moves left."},
	{ID: DirRight, Name: "Right", Summary: "The next shot comes in from the left and moves right."},
	{ID: DirUp, Name: "Up", Summary: "The next shot comes in from the bottom and moves up."},
	{ID: DirDown, Name: "Down", Summary: "The next shot comes in from the top and moves down."},
}

// Edges lists the fades at the very start and end of a cut.
var Edges = []EdgeInfo{
	{Edge: "start", Name: "Fade in at the start", Summary: "The video fades in from black.", How: `the first clip's transitionIn {"type":"fade","duration":1} (or "dip-to-white" to fade in from white)`, MaxDuration: 5},
	{Edge: "end", Name: "Fade out at the end", Summary: "The video fades out to black, picture and sound together.", How: `the last clip's "fadeOut": 1.5 (or setTransition with edge "end")`, MaxDuration: 5},
}

// Styles lists the per-project transition styles.
var Styles = []TransitionStyleInfo{
	{ID: StyleNatural, Name: "Natural", Summary: "Straight cuts, with voices that start early or carry over; a dissolve only when time passes; fade through black for endings.", Uses: []string{TransitionCut, TransitionCrossfade, TransitionDip, TransitionFade},
		SplitEdits: "Often: voices carry over a reaction, the next speaker starts a moment early (0.3–0.8 s)."},
	{ID: StyleSnappy, Name: "Snappy", Summary: "Tight straight cuts, with voices that start a moment early; the odd push or whip on a beat.", Uses: []string{TransitionCut, TransitionPush, TransitionWhip, TransitionDip},
		SplitEdits: "Short ones (0.2–0.4 s), mostly the next speaker starting early."},
	{ID: StyleSmooth, Name: "Smooth", Summary: "Dissolves between scenes, longer fades, gentle sound blends.", Uses: []string{TransitionCut, TransitionCrossfade, TransitionDip, TransitionDipWhite, TransitionFade},
		SplitEdits: "Longer ones (0.5–1 s) with longer blends (0.2–0.3 s)."},
	{ID: StylePlayful, Name: "Playful", Summary: "Slides, pushes, zooms, wipes and whips are welcome, still with clean cuts inside a conversation.", Uses: []string{TransitionCut, TransitionSlide, TransitionPush, TransitionZoom, TransitionWipe, TransitionWhip, TransitionDipWhite},
		SplitEdits: "As in Natural inside conversations."},
}

// TransitionCatalog returns schema/transitions.json.
func TransitionCatalog() TransitionsCatalog {
	return TransitionsCatalog{GeneratedBy: "metarush schema export (internal/editspec Transitions); do not edit", Transitions: Transitions, Sound: SoundModes,
		Easings: Easings, Directions: Directions, Edges: Edges, Styles: Styles, DefaultEasing: EaseInOut, CutBlend: DefaultCutBlend, SplitBlend: DefaultSplitBlend}
}

// TransitionByType finds a transition in the library.
func TransitionByType(t string) (TransitionInfo, bool) {
	for _, x := range Transitions {
		if x.Type == t {
			return x, true
		}
	}
	return TransitionInfo{}, false
}

// IsOverlapTransition reports whether the two shots share the screen (the
// incoming clip starts before the outgoing one ends).
func IsOverlapTransition(t string) bool {
	switch t {
	case TransitionCrossfade, TransitionWipe, TransitionSlide, TransitionPush, TransitionZoom, TransitionWhip:
		return true
	}
	return false
}

// IsDipTransition reports whether the transition dips through a color.
func IsDipTransition(t string) bool { return t == TransitionDip || t == TransitionDipWhite }

func transitionHasDirection(t string) bool {
	switch t {
	case TransitionWipe, TransitionSlide, TransitionPush, TransitionWhip:
		return true
	}
	return false
}

// newTransitionType reports types added with the transition library (their
// plans carry an explicit easing and direction; older types keep theirs empty).
func newTransitionType(t string) bool {
	switch t {
	case TransitionDipWhite, TransitionWipe, TransitionSlide, TransitionPush, TransitionZoom, TransitionWhip:
		return true
	}
	return false
}

// movesOutgoing reports transitions that move the outgoing shot too (it gets a
// transitionOut in the plan).
func movesOutgoing(t string) bool {
	return t == TransitionPush || t == TransitionZoom || t == TransitionWhip
}

// TransitionName is how people see a transition: "Dissolve 0.5 s", "Slide left 0.4 s".
func TransitionName(t *Transition) string {
	if t == nil || t.Type == "" || t.Type == TransitionCut {
		return "Cut"
	}
	info, ok := TransitionByType(t.Type)
	name := t.Type
	if ok {
		name = info.Name
	}
	if transitionHasDirection(t.Type) {
		d := t.Direction
		if d == "" {
			d = info.DefaultDirection
		}
		name += " " + d
	}
	if t.Duration > 0 {
		name += " " + seconds(t.Duration)
	}
	return name
}

// seconds formats a duration for people: "0.4 s", "1 s".
func seconds(v float64) string {
	v = math.Round(v*100) / 100
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
	return s + " s"
}
