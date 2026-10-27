package transcripts

import (
	"strings"
	"testing"
)

// Language codes are written subtag by subtag as BCP 47 does (D238).
func TestLanguageCodesAsBCP47WritesThem(t *testing.T) {
	for typed, want := range map[string]string{
		"ES":                 "es",
		" pt_br ":            "pt-BR",
		"es_419":             "es-419",
		"zh-hant-tw":         "zh-Hant-TW",
		"ZH-yue-HK":          "zh-yue-HK",
		"zh-YUE-hant-hk":     "zh-yue-Hant-HK",
		"SL-ROZAJ":           "sl-rozaj",
		"sl-Latn-IT-Nedis":   "sl-Latn-IT-nedis",
		"de-ch-1901":         "de-CH-1901",
		"FIL":                "fil",
		"de-DE-u-CO-phonebk": "de-DE-u-co-phonebk",
		"en-x-Captions":      "en-x-captions",
		"Spanish":            "es",
		"thai":               "th",
	} {
		if got, ok := Language(typed); !ok || got != want {
			t.Errorf("Language(%q) = %q, %v; want %q", typed, got, ok, want)
		}
	}
	// At most 35 characters (RFC 5646, 4.4.1): a captions file is named after it.
	if c := "en-x-" + strings.Repeat("abcdefgh-", 3) + "abc"; len(c) != MaxLanguageTag {
		t.Fatalf("the longest code is %d characters", len(c))
	} else if got, ok := Language(c); !ok || got != c {
		t.Errorf("a %d-character code: %q %v", len(c), got, ok)
	} else if got, ok := Language(c + "d"); ok {
		t.Errorf("a %d-character code: %q", len(c)+1, got)
	}
	for _, bad := range []string{"", "e", "english-uk", "es-", "es--MX", "es-MX-toolongvariant", "de-1901-1901",
		"de-u", "de-u-co-u-ca", "x-whatever", "en-US!", "es-ÉS", "../es"} {
		if got, ok := Language(bad); ok {
			t.Errorf("Language(%q) = %q; want it refused", bad, got)
		}
	}
}

func TestRightToLeftLanguages(t *testing.T) {
	for code, want := range map[string]bool{"ar": true, "ar-EG": true, "HE": true, "fa-IR": true, "ur": true, "en": false, "fr-CA": false, "arn": false} {
		if RightToLeft(code) != want {
			t.Errorf("RightToLeft(%q) = %v", code, !want)
		}
	}
}

// A right-to-left line's mark comes first in SubRip, before the speaker's
// name, so the whole line runs right to left; WebVTT keeps the name in its
// voice tag.
func TestRightToLeftMarkStartsTheLine(t *testing.T) {
	cues := []Cue{{From: 0, To: 1, Speaker: "Theo", Text: RLM + "مرحبا بكم"}, {From: 1, To: 2, Text: RLM + "3 أيام"}, {From: 2, To: 3, Speaker: "Ann", Text: "Hello"}}
	srt := SRT(cues)
	for _, want := range []string{"\n" + RLM + "Theo: مرحبا بكم\n", "\n" + RLM + "3 أيام\n", "\nAnn: Hello\n"} {
		if !strings.Contains(srt, want) {
			t.Errorf("SubRip lacks %q:\n%s", want, srt)
		}
	}
	if strings.Contains(srt, ": "+RLM) {
		t.Errorf("a mark after a name:\n%s", srt)
	}
	if vtt := VTT(cues); !strings.Contains(vtt, "<v Theo>"+RLM+"مرحبا بكم") {
		t.Errorf("WebVTT:\n%s", vtt)
	}
}

// A language's name gives one code, every time: no two languages share a
// name ("Norwegian" is no, "Norwegian Bokmål" nb).
func TestLanguageNamesAreOneLanguagesEach(t *testing.T) {
	seen := map[string]string{}
	for c, n := range languageNames {
		if other, ok := seen[strings.ToLower(n)]; ok {
			t.Errorf("%s and %s are both named %q", other, c, n)
		}
		seen[strings.ToLower(n)] = c
	}
	for range 50 {
		if c, _ := Language("Norwegian"); c != "no" {
			t.Fatalf("Norwegian: %q", c)
		}
		if c, _ := Language("norwegian bokmål"); c != "nb" {
			t.Fatalf("Norwegian Bokmål: %q", c)
		}
	}
}
