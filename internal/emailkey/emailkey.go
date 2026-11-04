// Package emailkey is how MetaRush matches emails — people's sign-ins,
// invites, guests and the provider's vouched-for email alike: trimmed, with
// the ASCII letters A–Z in lower case and every other character exactly as
// written. Only ASCII is folded: Unicode's lower case maps other characters
// onto ASCII ones (the Kelvin sign K becomes k), so "Kate@…" written with it
// would match kate@… — someone else's address.
package emailkey

import "strings"

// Of is an email's key.
func Of(email string) string {
	b := []byte(strings.TrimSpace(email))
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// Pattern is a regular expression matching exactly the emails whose key is
// key, as written (ASCII letters in either case, everything else literal):
// for records written before their key was kept.
func Pattern(key string) string {
	var b strings.Builder
	b.WriteByte('^')
	for _, r := range key {
		switch {
		case 'a' <= r && r <= 'z':
			b.WriteByte('[')
			b.WriteRune(r)
			b.WriteRune(r - ('a' - 'A'))
			b.WriteByte(']')
		case strings.ContainsRune(`\.+*?()|[]{}^$-/#&~ `, r):
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('$')
	return b.String()
}
