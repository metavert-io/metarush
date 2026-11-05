package api

import (
	"strings"

	"github.com/metavert-io/metarush/internal/winname"
)

// downloadName is a file name every computer people download to can keep
// (D247): Windows refuses \ / : * ? " < > | and control characters, names
// ending in a dot or a space, and the device names CON, PRN, AUX, NUL,
// COM1–COM9 and LPT1–LPT9 (with any extension), so those characters become
// "-", the end is trimmed and a device name gets "_" in front. The extension
// stays; a name longer than 180 characters is cut before it, at a whole
// character. The rules are internal/winname's, which names what MetaRush
// writes for Windows too (D523).
func downloadName(name string) string { return winname.Safe(name, "download") }

// attachment is the Content-Disposition of a download named name, the name
// made safe for every computer (downloadName) and written as RFC 6266
// recommends: quoted, and when it has other than ASCII letters also as UTF-8
// (filename*), with an ASCII stand-in for clients that read only the first.
func attachment(name string) string {
	n := downloadName(name)
	ascii, plain := asciiName(n)
	h := `attachment; filename="` + ascii + `"`
	if !plain {
		h += "; filename*=UTF-8''" + pctEncode(n)
	}
	return h
}

// asciiName is n with every non-ASCII character as "_" (and whether there was none).
func asciiName(n string) (string, bool) {
	var b strings.Builder
	plain := true
	for _, r := range n {
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
			plain = false
		}
	}
	return b.String(), plain
}

// pctEncode is RFC 5987's value-chars: attr-chars as they are, other bytes as %XX.
func pctEncode(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z') || ('0' <= c && c <= '9') || strings.IndexByte("!#$&+-.^_`|~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}
