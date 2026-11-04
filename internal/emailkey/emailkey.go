// Package emailkey is how MetaRush matches emails and sign-in names —
// people's sign-ins, Tailscale accounts, invites, guests and the provider's
// vouched-for email alike: trimmed and in lower case, Unicode's (Émile@… is
// émile@…), except for the two characters whose lower case is an ASCII
// letter: the capital I with a dot above (U+0130, lower case i) and the Kelvin
// sign (U+212A, lower case k). They keep their own key, so an address written
// with one never matches someone else's ("Kate@…" with the Kelvin sign isn't
// kate@…).
package emailkey

import (
	"strings"
	"unicode"
)

// fold is a character's key: its lower case, but never an ASCII letter for a
// character that isn't one.
func fold(r rune) rune {
	if r == 'İ' || r == 'K' {
		return r
	}
	return unicode.ToLower(r)
}

// Of is an email's key.
func Of(email string) string {
	return strings.Map(fold, strings.TrimSpace(email))
}

// Pattern is a regular expression matching exactly the emails whose key is
// key, as written (each letter in any case whose key it is, everything else
// literal): for records written before their key was kept.
func Pattern(key string) string {
	var b strings.Builder
	b.WriteByte('^')
	for _, r := range key {
		forms := spellings(r)
		if len(forms) == 1 {
			literal(&b, r)
			continue
		}
		b.WriteByte('[')
		for _, f := range forms {
			literal(&b, f)
		}
		b.WriteByte(']')
	}
	b.WriteByte('$')
	return b.String()
}

// spellings are the characters whose key is r: r and the other cases of it
// that fold to it (é: é and É; k: k and K, not the Kelvin sign; the long s ſ
// is its own). Every character whose lower case is r is in r's case orbit
// but the capital I with a dot, which keeps its own key anyway.
func spellings(r rune) []rune {
	out := []rune{r}
	for u := unicode.SimpleFold(r); u != r; u = unicode.SimpleFold(u) {
		if fold(u) == r {
			out = append(out, u)
		}
	}
	return out
}

func literal(b *strings.Builder, r rune) {
	if strings.ContainsRune(`\.+*?()|[]{}^$-/#&~ `, r) {
		b.WriteByte('\\')
	}
	b.WriteRune(r)
}
