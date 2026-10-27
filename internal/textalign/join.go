package textalign

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Unspaced reports whether r belongs to a script written without spaces
// between words (Chinese characters, Japanese kana, Thai, Lao, Khmer,
// Burmese): a transcript's words in it are pieces of a line, joined with
// nothing between them (D244).
func Unspaced(r rune) bool {
	return unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Thai, unicode.Lao, unicode.Khmer, unicode.Myanmar) ||
		r == 'ー' || r == '々' || (r >= 0x3000 && r <= 0x303F) || (r >= 0xFF00 && r <= 0xFFEF)
}

// SpaceBetween reports whether two words read with a space between them: not
// when either side of the join is a script written without spaces
// ("LibriVoxの録音", "2020年").
func SpaceBetween(left, right string) bool {
	l, _ := utf8.DecodeLastRuneInString(left)
	r, _ := utf8.DecodeRuneInString(right)
	if l == utf8.RuneError || r == utf8.RuneError {
		return true
	}
	return !Unspaced(l) && !Unspaced(r)
}

// JoinWords joins a line's words the way its language writes them: with
// spaces, except between pieces of a script written without them.
func JoinWords(words []string) string {
	var b strings.Builder
	prev := ""
	for _, w := range words {
		if w == "" {
			continue
		}
		if prev != "" && SpaceBetween(prev, w) {
			b.WriteByte(' ')
		}
		b.WriteString(w)
		prev = w
	}
	return b.String()
}
