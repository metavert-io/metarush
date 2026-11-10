package plainwords

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Hidden characters are the ones that change what a person sees without
// showing themselves: terminal codes (a line erased, the clipboard written),
// characters that reorder text (bidi overrides and isolates), invisible ones
// (zero-width characters, fillers, the blank braille pattern, a run of
// variation selectors that can carry a hidden message) and bytes that aren't
// text at all. Words from outside MetaRush — an AI app's, a reviewer's, a
// catalogue's — lose them before a person reads them, and an approval screen
// says when it took some out.
//
// The rule is shared: the dashboard's (shownText in web/src/lib/hidden.ts)
// follows the same cases (testdata/hidden.json; web/scripts/hidden-test.mjs).

// fillers are letters and symbols that draw nothing.
var fillers = map[rune]bool{
	0x034F: true, // combining grapheme joiner
	0x115F: true, // Hangul choseong filler
	0x1160: true, // Hangul jungseong filler
	0x17B4: true, // Khmer vowel inherent aq
	0x17B5: true, // Khmer vowel inherent aa
	0x180B: true, // Mongolian free variation selectors
	0x180C: true,
	0x180D: true,
	0x180F: true,
	0x2800: true, // braille pattern blank
	0x3164: true, // Hangul filler
	0xFFA0: true, // halfwidth Hangul filler
}

// selector reports a variation selector: VS1–VS16 and the supplement.
func selector(r rune) bool {
	return (r >= 0xFE00 && r <= 0xFE0F) || (r >= 0xE0100 && r <= 0xE01EF)
}

// hiddenRune reports a character Clean always takes out (variation selectors
// are decided by what comes before them).
func hiddenRune(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == 0x2028 || r == 0x2029 ||
		fillers[r] || (r >= 0xFDD0 && r <= 0xFDEF) || r&0xFFFE == 0xFFFE
}

// Clean takes hidden characters out of words, keeping newlines and tabs. An
// emoji keeps its one text-or-picture selector (U+FE0E, U+FE0F) right after
// it; every other variation selector goes. A zero-width joiner stays between
// two emoji (a family, a flag, someone at a computer), and a joiner or
// non-joiner between two letters of a script that isn't ASCII (Persian,
// Hindi), where it changes how the letters join; anywhere else they go.
func Clean(s string) string {
	out, _ := clean(s)
	return out
}

// CleanCount is Clean and how many characters it took out (a run of bytes
// that aren't text counts as one).
func CleanCount(s string) (string, int) { return clean(s) }

// HasHidden reports whether Clean would change s.
func HasHidden(s string) bool {
	_, n := clean(s)
	return n > 0
}

func clean(s string) (string, int) {
	if !needsClean(s) {
		return s, 0
	}
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	prev := rune(-1) // the last character kept
	for i := 0; i < len(s); {
		r, w := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && w <= 1 {
			// Bytes that aren't text, one run at a time.
			for i < len(s) {
				r, w = utf8.DecodeRuneInString(s[i:])
				if r != utf8.RuneError || w > 1 {
					break
				}
				i++
			}
			n++
			continue
		}
		i += w
		switch {
		case r == zwj || r == zwnj:
			next, _ := utf8.DecodeRuneInString(s[i:])
			if joins(r, prev, next) {
				b.WriteRune(r)
				prev = r
			} else {
				n++
			}
		case hiddenRune(r):
			n++
		case selector(r):
			if (r == 0xFE0E || r == 0xFE0F) && prev >= 0 && !selector(prev) && !unicode.IsSpace(prev) {
				b.WriteRune(r)
				prev = r
			} else {
				n++
			}
		default:
			b.WriteRune(r)
			prev = r
		}
	}
	return b.String(), n
}

const (
	zwnj = 0x200C
	zwj  = 0x200D
)

// joins reports a joiner (or non-joiner) that does a joiner's work: between
// two emoji (a joiner only), or two letters of a script that isn't ASCII.
func joins(r, prev, next rune) bool {
	if prev < 0 || next == utf8.RuneError {
		return false
	}
	if r == zwj && (unicode.Is(unicode.So, prev) || unicode.Is(unicode.Sk, prev) || prev == 0xFE0F) && unicode.Is(unicode.So, next) {
		return true
	}
	letter := func(c rune) bool {
		return c > 0x7F && (unicode.IsLetter(c) || unicode.Is(unicode.Mn, c)) && !fillers[c]
	}
	return letter(prev) && letter(next)
}

// needsClean is the quick look: nothing hidden and no selector.
func needsClean(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		if hiddenRune(r) || selector(r) {
			return true
		}
	}
	return false
}
