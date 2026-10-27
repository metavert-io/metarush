package resources

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NamePattern is what a resource name looks like: the short name cut sheets
// and template parameters use.
var NamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

// Slug makes a resource name from a title: "Acme Logo (dark).svg" → "acme-logo-dark".
func Slug(title string) string {
	t := norm.NFKD.String(strings.ToLower(title))
	var b strings.Builder
	dash := false
	for _, r := range t {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue // accents
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if b.Len() > 0 && !dash {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "r-" + s
		s = strings.TrimRight(s, "-")
		if s == "r" {
			s = "resource"
		}
	}
	return s
}

// numbered is name-2, name-3… (an existing -N is replaced, not extended).
func numbered(name string, n int) string {
	base := name
	if i := strings.LastIndexByte(base, '-'); i > 0 {
		if rest := base[i+1:]; rest != "" && strings.Trim(rest, "0123456789") == "" {
			base = base[:i]
		}
	}
	suffix := "-" + itoa(n)
	if len(base)+len(suffix) > 48 {
		base = strings.TrimRight(base[:48-len(suffix)], "-")
	}
	return base + suffix
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
