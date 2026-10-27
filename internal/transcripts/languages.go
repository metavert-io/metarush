package transcripts

import "strings"

// Languages captions are translated into (D238), by their codes (BCP 47:
// es, pt-BR, zh-Hans). Any well-formed tag is accepted; these have names.
var languageNames = map[string]string{
	"ar": "Arabic", "bg": "Bulgarian", "bn": "Bengali", "ca": "Catalan", "cs": "Czech", "da": "Danish", "de": "German",
	"el": "Greek", "en": "English", "en-GB": "British English", "en-US": "American English", "es": "Spanish",
	"es-419": "Latin American Spanish", "es-ES": "Spanish (Spain)", "es-MX": "Mexican Spanish", "et": "Estonian",
	"fa": "Persian", "fi": "Finnish", "fil": "Filipino", "fr": "French", "fr-CA": "Canadian French", "ga": "Irish",
	"gu": "Gujarati", "he": "Hebrew", "hi": "Hindi", "hr": "Croatian", "hu": "Hungarian", "id": "Indonesian",
	"is": "Icelandic", "it": "Italian", "ja": "Japanese", "kn": "Kannada", "ko": "Korean", "lt": "Lithuanian",
	"lv": "Latvian", "ml": "Malayalam", "mr": "Marathi", "ms": "Malay", "nl": "Dutch", "no": "Norwegian", "nb": "Norwegian Bokmål",
	"pa": "Punjabi", "pl": "Polish", "pt": "Portuguese", "pt-BR": "Brazilian Portuguese", "pt-PT": "European Portuguese",
	"ro": "Romanian", "ru": "Russian", "sk": "Slovak", "sl": "Slovenian", "sr": "Serbian", "sv": "Swedish", "sw": "Swahili",
	"ta": "Tamil", "te": "Telugu", "th": "Thai", "tr": "Turkish", "uk": "Ukrainian", "ur": "Urdu", "vi": "Vietnamese",
	"zh": "Chinese", "zh-Hans": "Chinese (Simplified)", "zh-Hant": "Chinese (Traditional)", "zh-CN": "Chinese (China)",
	"zh-TW": "Chinese (Taiwan)",
}

// Language reads a language code people typed (ES, pt_br, es-419,
// ZH-YUE-hk, SL-ROZAJ) as BCP 47 writes it (es, pt-BR, es-419, zh-yue-HK,
// sl-rozaj); ok is false when it isn't one. A language's name (Spanish)
// gives its code.
func Language(code string) (string, bool) {
	typed := strings.TrimSpace(code)
	if len(typed) > MaxLanguageTag {
		return "", false
	}
	if c, ok := languageTag(strings.ReplaceAll(typed, "_", "-")); ok {
		return c, true
	}
	// Names people type: Spanish, french.
	if c, ok := languageByName[strings.ToLower(typed)]; ok {
		return c, true
	}
	return "", false
}

// MaxLanguageTag is the longest language code MetaRush takes: the 35
// characters BCP 47 asks every implementation to handle (RFC 5646, 4.4.1),
// which also keeps a captions file's name (<base>.<lang>.srt) short.
const MaxLanguageTag = 35

// languageByName finds a language's code by its name, in lower case (each
// name is one language's: TestLanguageNamesAreOneLanguagesEach).
var languageByName = func() map[string]string {
	out := map[string]string{}
	for c, n := range languageNames {
		if !strings.Contains(c, "-") {
			out[strings.ToLower(n)] = c
		}
	}
	return out
}()

// languageTag writes a well-formed BCP 47 tag the way the standard does,
// subtag by subtag: the language in lower case (2 or 3 letters) and up to
// three 3-letter extended languages after it (zh-yue), a 4-letter script
// in title case (Hant), a region as 2 capitals or 3 digits (BR, 419),
// variants of 5 to 8 letters and digits or 4 starting with a digit
// (rozaj, 1901), then extensions (u-co-phonebk) and private use (x-whatever)
// in lower case.
func languageTag(code string) (string, bool) {
	parts := strings.Split(code, "-")
	at := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	if n := len(parts[0]); n < 2 || n > 3 || !letters(parts[0]) {
		return "", false
	}
	out := []string{strings.ToLower(parts[0])}
	i := 1
	for n := 0; n < 3 && len(at(i)) == 3 && letters(at(i)); n++ {
		out = append(out, strings.ToLower(at(i)))
		i++
	}
	if p := at(i); len(p) == 4 && letters(p) {
		out = append(out, strings.ToUpper(p[:1])+strings.ToLower(p[1:]))
		i++
	}
	if p := at(i); (len(p) == 2 && letters(p)) || (len(p) == 3 && digits(p)) {
		out = append(out, strings.ToUpper(p))
		i++
	}
	seen := map[string]bool{}
	for p := at(i); alnum(p) && (len(p) >= 5 && len(p) <= 8 || len(p) == 4 && digits(p[:1])); p = at(i) {
		v := strings.ToLower(p)
		if seen[v] {
			return "", false // a variant twice
		}
		seen[v] = true
		out = append(out, v)
		i++
	}
	singletons := map[string]bool{}
	for i < len(parts) {
		s := strings.ToLower(parts[i])
		if len(s) != 1 || !alnum(s) || singletons[s] {
			return "", false
		}
		singletons[s] = true
		out = append(out, s)
		i++
		min, n := 2, 0 // an extension's subtags have 2 to 8 characters
		if s == "x" {
			min = 1 // private use: 1 to 8, and it runs to the end
		}
		for ; i < len(parts) && len(parts[i]) >= min && len(parts[i]) <= 8 && alnum(parts[i]) && (s == "x" || len(parts[i]) > 1); i++ {
			out = append(out, strings.ToLower(parts[i]))
			n++
		}
		if n == 0 {
			return "", false
		}
	}
	return strings.Join(out, "-"), true
}

func letters(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return false
		}
	}
	return s != ""
}

func digits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

func alnum(s string) bool {
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return s != ""
}

// LanguageName names a language (Spanish, Brazilian Portuguese); a code
// without a name stays as it is.
func LanguageName(code string) string {
	if n := languageNames[code]; n != "" {
		return n
	}
	if i := strings.IndexByte(code, '-'); i > 0 {
		if n := languageNames[code[:i]]; n != "" {
			return n + " (" + code[i+1:] + ")"
		}
	}
	return code
}

// SameLanguage reports whether two codes name the same language (en and en-US do).
func SameLanguage(a, b string) bool {
	base := func(s string) string {
		if i := strings.IndexByte(s, '-'); i > 0 {
			return strings.ToLower(s[:i])
		}
		return strings.ToLower(s)
	}
	return base(a) == base(b)
}

// VTTWithNote writes cues as a WebVTT captions file that starts with a note
// (a machine-translated file says so).
func VTTWithNote(cues []Cue, note string) string {
	v := VTT(cues)
	note = strings.Join(strings.Fields(strings.ReplaceAll(note, "-->", "→")), " ")
	if note == "" {
		return v
	}
	return "WEBVTT\n\nNOTE " + note + "\n\n" + strings.TrimPrefix(v, "WEBVTT\n\n")
}

// RLM is the right-to-left mark: a captions line in Arabic, Hebrew, Persian
// or Urdu starts with it, so players that don't know the language still
// show the line right to left (a line that starts with a number or a name
// in Latin letters otherwise runs the wrong way).
const RLM = "\u200f"

// RightToLeft reports whether a language is written right to left.
func RightToLeft(code string) bool {
	base := strings.ToLower(code)
	if i := strings.IndexByte(base, '-'); i > 0 {
		base = base[:i]
	}
	switch base {
	case "ar", "he", "iw", "fa", "ur":
		return true
	}
	return false
}
