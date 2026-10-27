package api

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// downloadName is a file name every computer people download to can keep
// (D247): Windows refuses \ / : * ? " < > | and control characters, names
// ending in a dot or a space, and the device names CON, PRN, AUX, NUL,
// COM1–COM9 and LPT1–LPT9 (with any extension), so those characters become
// "-", the end is trimmed and a device name gets "_" in front. The extension
// stays; a name longer than 180 characters is cut before it, at a whole
// character.
func downloadName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f || unicode.IsControl(r):
		case strings.ContainsRune(`\/:*?"<>|`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	n := strings.TrimSpace(b.String())
	ext := path.Ext(n)
	if len(ext) > 12 || strings.TrimRight(ext, ". ") == "" {
		ext = ""
	}
	stem := strings.TrimRight(strings.TrimSuffix(n, ext), ". ")
	if utf8.RuneCountInString(stem) > 180 {
		stem = strings.TrimRight(string([]rune(stem)[:180]), ". ")
	}
	if stem == "" {
		stem = "download"
	}
	base := strings.ToUpper(stem)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		stem = "_" + stem
	}
	return stem + ext
}

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
