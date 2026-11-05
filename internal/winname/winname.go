// Package winname makes file names every computer people use can keep, as
// Windows asks (D247, D523): its refused characters (\ / : * ? " < > | and
// control characters) become "-", a name may not end in a dot or a space,
// and the device names CON, PRN, AUX, NUL, COM1–COM9 and LPT1–LPT9 (with any
// extension) get "_" in front. Downloads from the dashboard are named so, and
// so is what MetaRush writes into a folder people open from Windows (inside
// WSL, Windows' own drives). It depends on nothing else in MetaRush.
package winname

import (
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxStem is how long a name's part before the extension may be, in
// characters (longer ones are cut at a whole character).
const MaxStem = 180

// Safe is name as every computer keeps it; fallback when nothing is left
// ("download", "clip"). The extension (up to 12 characters) stays.
func Safe(name, fallback string) string {
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
	if utf8.RuneCountInString(stem) > MaxStem {
		stem = strings.TrimRight(string([]rune(stem)[:MaxStem]), ". ")
	}
	if stem == "" {
		stem = fallback
	}
	if Reserved(stem) {
		stem = "_" + stem
	}
	return stem + ext
}

// Reserved reports a name Windows keeps for a device (CON, nul.txt, COM3.tar.gz),
// in any case and with any extension.
func Reserved(name string) bool {
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	switch base {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return true
	}
	return false
}

// Kept reports whether Windows keeps name exactly as it is (Safe changes nothing).
func Kept(name string) bool { return name != "" && Safe(name, "") == name }
