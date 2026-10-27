package names

import (
	"regexp"
	"strings"
	"unicode"
)

// Names in the picture (D156). A call app prints each person's name under
// their picture, a lower third says who is talking, a title introduces a
// guest: FromScreen reads such text as a person's name when it looks like
// one. It is deliberately strict — the result is only ever a suggestion a
// person confirms, and app labels, titles and slides are not people.

var (
	bracketed = regexp.MustCompile(`\s*[\(\[\{][^\)\]\}]*[\)\]\}]`)
	// "Lena's iPhone", "Ben’s MacBook Pro": the device, not the name.
	device = regexp.MustCompile(`(?i)['’]s\s+(iphone|ipad|ipod|macbook(\s+(pro|air))?|mac(\s+mini)?|imac|android|phone|pc|laptop|computer|desktop|galaxy\b.*|pixel\b.*|surface\b.*)$`)
	// A lower third: "Jane Doe, CEO", "Jane Doe | Founder", "Jane Doe — host".
	separator = regexp.MustCompile(`\s*(,|\||•|·|:|\s[—–-]\s)\s*`)
	// One word of a name: a capital first (any script with case), then letters,
	// apostrophes, hyphens or a closing period (an initial: "J.").
	nameWord = regexp.MustCompile(`^\p{Lu}[\p{L}\p{M}'’\-]*\.?$`)
)

// particles may sit lowercase inside a name ("Ana de Souza", "Vincent van Gogh").
var particles = map[string]bool{"de": true, "da": true, "di": true, "du": true, "del": true, "della": true, "der": true, "den": true, "van": true,
	"von": true, "le": true, "la": true, "bin": true, "binti": true, "al": true, "el": true, "y": true, "e": true, "dos": true, "das": true, "ter": true}

// notNames are words that make text something other than a person's name:
// call-app labels and controls, recording banners, and words titles and
// captions are made of.
var notNames = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`mute muted unmute video stop start participants participant chat share sharing screen record recording recorded
		reactions reaction leave end meeting meetings gallery speaker speakers view security apps app whiteboard whiteboards more raise raised hand
		hands talking host co-host cohost you me guest guests live waiting room rooms breakout join joined joining audio captions caption transcript
		transcription settings people everyone everybody presenting presenter present now camera microphone mic off on connecting reconnecting
		network unstable poor connection internet zoom teams meet google microsoft webex skype facetime riverside squadcast streamyard loom
		welcome thanks thank hello hi hey bye goodbye the and of a an to in at for with by from is are was were this that it its our your my
		their his her club call calls weekly daily monthly episode season part chapter intro introduction outro interview podcast show news
		breaking update updates today tonight tomorrow yesterday morning evening night week weekend month year minute minutes second seconds
		hour hours am pm subscribe like follow comment comments link bio click here watch listen more less new old best top next previous
		slide slides agenda summary questions question answer answers notes note title subtitle office studio
		team company inc llc ltd corp corporation official channel group webinar conference keynote panel session workshop class lesson
		course tutorial demo demonstration preview trailer teaser clip clips scene scenes take takes cam closeup
		exit entrance open closed sale sold free buy shop store menu stop caution danger warning private parking toilet toilets restroom`) {
		notNames[w] = true
	}
}

// FromScreen reads on-screen text as a person's name: "Lena Okoye",
// "Lena's iPhone" → "Lena", "Jane Doe, CEO" → "Jane Doe", "JANE DOE" →
// "Jane Doe". It reports false for app labels ("Mute", "● Recording…"),
// titles ("Garden club · weekly call"), numbers and anything that is not one
// to four capitalized words.
func FromScreen(text string) (string, bool) {
	s := strings.Join(strings.Fields(text), " ")
	s = bracketed.ReplaceAllString(s, "")
	s = strings.TrimSpace(device.ReplaceAllString(s, ""))
	if parts := separator.Split(s, 2); len(parts) > 0 {
		s = parts[0]
	}
	s = strings.TrimFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && r != '.' })
	s = strings.TrimSuffix(s, ".")
	if s == "" || len([]rune(s)) > 40 {
		return "", false
	}
	for _, r := range s {
		if unicode.IsDigit(r) {
			return "", false
		}
	}
	words := strings.Fields(s)
	if len(words) == 0 || len(words) > 4 {
		return "", false
	}
	letters := 0
	allCaps := true
	for i, w := range words {
		lw := strings.ToLower(strings.Trim(w, ".'’"))
		if notNames[lw] {
			return "", false
		}
		if particles[w] && i > 0 && i < len(words)-1 {
			continue
		}
		if !nameWord.MatchString(w) {
			return "", false
		}
		for _, r := range w {
			if unicode.IsLetter(r) {
				letters++
				if unicode.IsLower(r) {
					allCaps = false
				}
			}
		}
	}
	if letters < 2 {
		return "", false
	}
	// A single capital letter or two ("AB", "J.") is not a name on its own.
	if len(words) == 1 && letters < 3 {
		return "", false
	}
	if allCaps {
		for i, w := range words {
			words[i] = titleWord(w)
		}
	}
	return strings.Join(words, " "), true
}

// titleWord writes an all-capitals word the way names are written ("O'BRIEN"
// → "O'Brien", "ANNE-MARIE" → "Anne-Marie").
func titleWord(w string) string {
	var b strings.Builder
	start := true
	for _, r := range w {
		switch {
		case unicode.IsLetter(r) && start:
			b.WriteRune(unicode.ToUpper(r))
			start = false
		case unicode.IsLetter(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
			start = r == '-' || r == '\'' || r == '’'
		}
	}
	return b.String()
}

// SameName reports whether two names can mean the same person: equal when
// folded, or one is the start of the other word by word ("Lena" and
// "Lena Okoye", "L Okoye" and "Lena Okoye" do; "Len" and "Lena
// Okoye" do not).
func SameName(a, b string) bool {
	fa, fb := Fold(a), Fold(b)
	if fa == "" || fb == "" {
		return false
	}
	if fa == fb {
		return true
	}
	return wordStarts(fa, fb) || wordStarts(fb, fa)
}

// wordStarts: every word of q is a whole word of name, or (one letter) its
// initial, in order, starting with name's first word.
func wordStarts(q, name string) bool {
	qs, ws := strings.Fields(q), strings.Fields(name)
	if len(qs) > len(ws) {
		return false
	}
	j := 0
	for i, w := range ws {
		if j == len(qs) {
			break
		}
		if i == 0 && !(qs[0] == w || (len([]rune(qs[0])) == 1 && strings.HasPrefix(w, qs[0]))) {
			return false
		}
		if qs[j] == w || (len([]rune(qs[j])) == 1 && strings.HasPrefix(w, qs[j])) {
			j++
		}
	}
	return j == len(qs)
}
