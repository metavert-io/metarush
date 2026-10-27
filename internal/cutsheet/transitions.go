package cutsheet

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
)

// A ↳ line says how the next clip comes in:
//
//	↳ <picture transition> [<direction>] [<length>] [· <phrase>]… [· {block}]
//
// The picture transition is one of a fixed set of plain words (TransitionNames),
// followed for the moving ones by the way the picture moves ("slide left").
// After it, `·`-separated phrases (TransitionClauses) describe the rest, the way
// an editor would say it:
//
//	↳ cut · Ada's voice starts 0.4 s early     (J cut: the next clip's sound leads)
//	↳ dissolve 0.5 s · voice carries 0.3 s     (L cut: this clip's sound runs on)
//	↳ cut · sound changes in a pause           (MetaRush finds the pause; "auto")
//	↳ slide left 0.4 s · slows down            (the pace of the movement)
//
// Each clause covers some Transition JSON fields: the printer writes the phrase
// and the parser reads it back (the cut sheet completeness test fails until a
// new field has a spelling or is accepted in the {block}). A combination no
// phrase can say travels in the {block}, so nothing is ever lost; an unknown
// phrase is an error.

// TransitionName maps a plain-words picture transition to the spec type.
type TransitionName struct {
	Type    string   // editspec transition type
	Words   string   // canonical spelling
	Aliases []string // also accepted on import
}

// TransitionNames is the picture-transition vocabulary (the same set as
// `metarush edit transitions`). The spec type is accepted too.
var TransitionNames = []TransitionName{
	{Type: editspec.TransitionCut, Words: "cut", Aliases: []string{"hard cut", "straight cut"}},
	{Type: editspec.TransitionFade, Words: "fade in", Aliases: []string{"fade", "fade up", "fades in"}},
	{Type: editspec.TransitionCrossfade, Words: "dissolve", Aliases: []string{"crossfade", "cross fade", "cross-fade", "cross dissolve", "cross-dissolve"}},
	{Type: editspec.TransitionDip, Words: "fade through black", Aliases: []string{"dip to black", "dip-to-black", "dip through black", "through black"}},
	{Type: editspec.TransitionDipWhite, Words: "fade through white", Aliases: []string{"dip to white", "dip-to-white", "dip through white", "through white", "flash to white"}},
	{Type: editspec.TransitionWipe, Words: "wipe"},
	{Type: editspec.TransitionSlide, Words: "slide", Aliases: []string{"slide in"}},
	{Type: editspec.TransitionPush, Words: "push"},
	{Type: editspec.TransitionZoom, Words: "zoom", Aliases: []string{"zoom through", "zoom in"}},
	{Type: editspec.TransitionWhip, Words: "whip", Aliases: []string{"whip pan", "whip-pan", "swish pan"}},
}

// CutContext is what a phrase may need to word a cut: the clips on either side
// and the named person who talks most in each ("" when nobody named does, or
// when the sheet is printed without footage info).
type CutContext struct {
	Prev, Next           *editspec.Clip
	PrevVoice, NextVoice string
}

// TransitionClause is one extra phrase on a ↳ line.
type TransitionClause struct {
	// Example is shown in hints, e.g. "voice starts 0.4 s early".
	Example string
	// Keys are the Transition JSON fields this phrase covers.
	Keys []string
	// Parse reads the phrase (its words joined by single spaces) and returns
	// the fields it sets, or false if the phrase is not this clause.
	Parse func(text string) (map[string]json.RawMessage, bool)
	// Print returns the phrase for t at a cut, or false when t does not use it
	// (or uses it in a way no phrase can say: the fields then go in the block).
	Print func(t *editspec.Transition, at CutContext) (string, bool)
}

// TransitionClauses is the registry: the pace of the movement and the sound at
// the cut (split edits), printed in that order.
var TransitionClauses = []TransitionClause{
	{Example: "smooth, even speed, speeds up or slows down", Keys: []string{"easing"}, Parse: parseEasing, Print: printEasing},
	{Example: "Sam's voice starts 0.4 s early; voice carries 0.3 s; sound changes in a pause (or auto); sound changes at the cut; … with a 0.2 s blend",
		Keys: []string{"audio"}, Parse: parseSound, Print: printSound},
}

func transitionByWords(words string) (string, bool) {
	w := strings.ToLower(strings.Join(strings.Fields(words), " "))
	for _, n := range TransitionNames {
		if w == n.Words || w == n.Type {
			return n.Type, true
		}
		for _, a := range n.Aliases {
			if w == a {
				return n.Type, true
			}
		}
	}
	return "", false
}

// TransitionType reads a transition's plain name ("dissolve", "fade through
// black") or spec type ("crossfade"), for commands that take one.
func TransitionType(words string) (string, bool) { return transitionByWords(words) }

// TransitionWords is the plain name of a transition type ("dissolve").
func TransitionWords(typ string) string {
	for _, n := range TransitionNames {
		if n.Type == typ {
			return n.Words
		}
	}
	return typ
}

func transitionVocabulary() string {
	var ws []string
	for _, n := range TransitionNames {
		ws = append(ws, n.Words)
	}
	return orList(ws)
}

// moves reports transitions that move the picture in a direction.
func moves(typ string) bool {
	info, ok := editspec.TransitionByType(typ)
	return ok && len(info.Directions) > 0
}

func isDirection(w string) bool {
	return w == editspec.DirLeft || w == editspec.DirRight || w == editspec.DirUp || w == editspec.DirDown
}

func movingVocabulary() string {
	var ws []string
	for _, n := range TransitionNames {
		if moves(n.Type) {
			ws = append(ws, n.Words)
		}
	}
	return orList(ws)
}

// TransitionPhrase is a transition in the cut sheet's words, as on its ↳ line
// without the arrow: "dissolve 0.5 s · voice carries 0.3 s". Fields no phrase
// can say are left out (a sheet keeps them in a {block}).
func TransitionPhrase(t *editspec.Transition, at CutContext) string {
	if t == nil {
		return "cut"
	}
	l := TransitionWords(t.Type)
	if t.Direction != "" && moves(t.Type) && isDirection(t.Direction) {
		l += " " + t.Direction
	}
	if t.Duration != 0 {
		l += " " + fmtDur(t.Duration)
	}
	for _, cl := range TransitionClauses {
		if phrase, ok := cl.Print(t, at); ok {
			l += " · " + phrase
		}
	}
	return l
}

// ---- the sound at the cut (transitionIn.audio) ----

const durPat = `(\d+(?:\.\d+)?|\.\d+) ?(?:s|sec|secs|second|seconds)`

var (
	reBlendTail = regexp.MustCompile(`(?i)^(.*?) (?:with an? ` + durPat + ` blend|with no blend|blended over ` + durPat + `)$`)
	reNameOK    = regexp.MustCompile(`^[\p{L}\p{M}][\p{L}\p{M}\p{N} .'’-]{0,39}$`)
	// soundForms are the ways to say the sound at a cut; the first group, when
	// the form has one, is the length.
	soundForms = []struct {
		re   *regexp.Regexp
		mode string
	}{
		{regexp.MustCompile(`(?i)^(?:(?:the|his|her|their|my|your|its|our) )?(?:.+?['’]s )?(?:voice|sound) (?:starts|comes in|begins) ` + durPat + ` (?:early|before)$`), editspec.SoundLead},
		{regexp.MustCompile(`(?i)^j[- ]?cut ` + durPat + `$`), editspec.SoundLead},
		{regexp.MustCompile(`(?i)^(?:(?:the|his|her|their|my|your|its|our) )?(?:.+?['’]s )?(?:voice|sound) (?:carries(?: over| on)?|runs on|continues) ` + durPat + `(?: over| longer)?$`), editspec.SoundCarry},
		{regexp.MustCompile(`(?i)^l[- ]?cut ` + durPat + `$`), editspec.SoundCarry},
		{regexp.MustCompile(`(?i)^(?:auto|sound auto|auto sound|(?:the )?(?:sound|voices?) (?:changes?|switch(?:es)?) in (?:a|the|the nearest|the next) pause)(?: within ` + durPat + `)?$`), editspec.SoundAuto},
		{regexp.MustCompile(`(?i)^(?:the )?sound (?:changes |switches )?(?:at the cut|with the picture)$`), editspec.SoundCut},
	}
)

func parseSound(text string) (map[string]json.RawMessage, bool) {
	st := editspec.SoundTransition{}
	main := text
	if m := reBlendTail.FindStringSubmatch(text); m != nil {
		main = m[1]
		b := 0.0
		for _, g := range m[2:] {
			if g != "" {
				v, ok := parseNum(g)
				if !ok {
					return nil, false
				}
				b = v
			}
		}
		st.Blend = &b
	}
	for _, f := range soundForms {
		m := f.re.FindStringSubmatch(main)
		if m == nil {
			continue
		}
		st.Mode = f.mode
		if len(m) > 1 && m[1] != "" {
			v, ok := parseNum(m[1])
			if !ok {
				return nil, false
			}
			st.Duration = v
		}
		raw, err := json.Marshal(st)
		if err != nil {
			return nil, false
		}
		return map[string]json.RawMessage{"audio": raw}, true
	}
	return nil, false
}

// personVoice is "Sam's voice" when the person is known, else "voice".
func personVoice(name string) string {
	if name != "" && reNameOK.MatchString(name) {
		return name + "'s voice"
	}
	return "voice"
}

func printSound(t *editspec.Transition, at CutContext) (string, bool) {
	a := t.Audio
	if a == nil || a.Duration < 0 || (a.Blend != nil && *a.Blend < 0) {
		return "", false
	}
	var s string
	switch a.Mode {
	case editspec.SoundLead:
		s = personVoice(at.NextVoice) + " starts " + fmtDur(a.Duration) + " early"
	case editspec.SoundCarry:
		s = personVoice(at.PrevVoice) + " carries " + fmtDur(a.Duration)
	case editspec.SoundAuto:
		s = "sound changes in a pause"
		if a.Duration != 0 {
			s += " within " + fmtDur(a.Duration)
		}
	case editspec.SoundCut:
		if a.Duration != 0 {
			return "", false
		}
		s = "sound changes at the cut"
	default:
		return "", false
	}
	if a.Blend != nil {
		if *a.Blend == 0 {
			s += " with no blend"
		} else {
			s += " with a " + fmtDur(*a.Blend) + " blend"
		}
	}
	return s, true
}

// ---- the pace of the movement (transitionIn.easing) ----

// easingAliases are other ways to say an easing; the catalog's names in lower
// case ("smooth", "even speed", "speeds up", "slows down") are the spellings.
var easingAliases = map[string][]string{
	editspec.EaseInOut:  {"ease-in-out", "ease in out", "ease in and out", "eases in and out", "smooth pace"},
	editspec.EaseLinear: {"linear", "even", "steady", "constant speed", "even pace"},
	editspec.EaseIn:     {"ease-in", "ease in", "eases in", "accelerates"},
	editspec.EaseOut:    {"ease-out", "ease out", "eases out", "decelerates"},
}

func easingWords(id string) (string, bool) {
	for _, e := range editspec.Easings {
		if e.ID == id {
			return strings.ToLower(e.Name), true
		}
	}
	return "", false
}

// EasingByWords reads an easing's plain name ("slows down") or spec value.
func EasingByWords(text string) (string, bool) {
	w := strings.ToLower(strings.Join(strings.Fields(text), " "))
	for _, e := range editspec.Easings {
		if w == strings.ToLower(e.Name) || w == e.ID || slices.Contains(easingAliases[e.ID], w) {
			return e.ID, true
		}
	}
	return "", false
}

func parseEasing(text string) (map[string]json.RawMessage, bool) {
	id, ok := EasingByWords(text)
	if !ok {
		return nil, false
	}
	return map[string]json.RawMessage{"easing": json.RawMessage(quote(id))}, true
}

func printEasing(t *editspec.Transition, _ CutContext) (string, bool) {
	if t.Easing == "" {
		return "", false
	}
	return easingWords(t.Easing)
}
