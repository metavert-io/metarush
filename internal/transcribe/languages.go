package transcribe

import (
	"context"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// Languages is what this machine writes each language down with (D243):
// English on the default recognizer (Parakeet v2 when it is installed, D10),
// every other language on whisper.cpp with the language set (never Parakeet
// v3, which drops sentences), and whisper.cpp's language detection for clips
// nobody set a language for.
type Languages struct {
	// Default is the language of a clip nobody set and none was heard in:
	// transcription.language when it names one, else English.
	Default string
	// Detect listens for each clip's language; nil when
	// transcription.language names one language (every clip is that) or
	// whisper.cpp with a multilingual model isn't installed.
	Detect  LanguageDetector
	english Provider
	other   Provider
}

// LanguageDetector tells which language a clip (a 16 kHz mono WAV) is spoken in.
type LanguageDetector interface {
	Detect(ctx context.Context, wav, scratch string) (Heard, error)
}

// LanguagesWith is a Languages of given recognizers (tests stand them in).
func LanguagesWith(def string, english, other Provider, detect LanguageDetector) *Languages {
	return &Languages{Default: def, english: english, other: other, Detect: detect}
}

// NewLanguages reads transcription.provider and transcription.language: an
// explicit provider is used for English (and for everything, when it is
// whisper.cpp); "" or "auto" as the language means each clip's language is
// heard. It returns nil when nothing can transcribe.
func NewLanguages(cfg config.Transcription, t *tools.Set) *Languages {
	if cfg.Provider == "none" {
		return nil
	}
	l := &Languages{Default: "en"}
	fixed := cfg.Language != "" && cfg.Language != "auto"
	if fixed {
		l.Default = strings.ToLower(cfg.Language)
	}
	en := cfg
	en.Language = "en"
	l.english = Select(en, t)
	if cfg.Provider == "" || cfg.Provider == "auto" || cfg.Provider == "whispercpp" {
		// An English-only model (ggml-*.en.bin) can't write other languages: it
		// would write a Spanish clip down in English.
		if w, ok := newWhisperCPP(t, whisperModel(cfg), cfg.KeepModelLoaded()).(*whisperCPP); ok && !strings.HasSuffix(w.model, ".en.bin") {
			l.other = w
		}
	}
	if d := NewDetector(t, whisperModel(cfg)); !fixed && d != nil {
		l.Detect = d // a nil *Detector must not become a non-nil interface
	}
	if l.english == nil && l.other == nil {
		return nil
	}
	return l
}

// Recognizer is one of this machine's recognizers by name — "parakeet" or
// "whispercpp" — set up as transcription.* says, or nil when it isn't
// installed (D278: the local recognizers as a transcription provider).
func Recognizer(cfg config.Transcription, t *tools.Set, name string) Provider {
	switch name {
	case "parakeet":
		return newParakeet(t, cfg.Model, cfg.KeepModelLoaded())
	case "whispercpp":
		return newWhisperCPP(t, whisperModel(cfg), cfg.KeepModelLoaded())
	}
	return nil
}

// Multilingual reports whether a recognizer writes languages other than
// English (whisper.cpp with a model that isn't English-only).
func Multilingual(p Provider) bool {
	w, ok := p.(*whisperCPP)
	return ok && !strings.HasSuffix(w.model, ".en.bin")
}

// whisperModel is transcription.model when it names a whisper.cpp model.
func whisperModel(cfg config.Transcription) string {
	if strings.HasPrefix(strings.ToLower(cfg.Model), "ggml-") || cfg.Provider == "whispercpp" {
		return cfg.Model
	}
	return ""
}

// For is the recognizer for a language; nil when this machine has none for
// it (another language without whisper.cpp).
func (l *Languages) For(language string) Provider {
	if l == nil {
		return nil
	}
	if IsEnglish(language) && l.english != nil {
		return l.english
	}
	if l.other != nil {
		return l.other
	}
	return nil
}

// Any is this machine's first recognizer (for status lines).
func (l *Languages) Any() Provider {
	if l == nil {
		return nil
	}
	if l.english != nil {
		return l.english
	}
	return l.other
}

// Others says whether languages other than English can be written down here.
func (l *Languages) Others() bool { return l != nil && l.other != nil }

// IsEnglish reports whether a code is English (en, en-GB).
func IsEnglish(code string) bool {
	code = strings.ToLower(code)
	return code == "en" || strings.HasPrefix(code, "en-")
}

// LanguageChoice is a language whisper.cpp knows, by its code and English name.
type LanguageChoice struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// SpokenLanguages lists the languages a clip can be set to: whisper.cpp's,
// the most spoken first, then the rest by name.
func SpokenLanguages() []LanguageChoice {
	first := []string{"en", "es", "fr", "de", "pt", "it", "nl", "ja", "zh", "ko", "hi", "ar", "ru", "pl", "tr", "uk", "sv", "id", "vi"}
	seen := map[string]bool{}
	var out []LanguageChoice
	for _, c := range first {
		out = append(out, LanguageChoice{Code: c, Name: SpokenLanguageName(c)})
		seen[c] = true
	}
	var rest []LanguageChoice
	for _, c := range whisperLangs {
		if !seen[c] {
			rest = append(rest, LanguageChoice{Code: c, Name: SpokenLanguageName(c)})
			seen[c] = true
		}
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	return append(out, rest...)
}

// SpokenLanguageName names one of whisper.cpp's languages in English
// ("Haitian Creole"); a code it doesn't know stays as it is.
func SpokenLanguageName(code string) string {
	for name, c := range whisperLangs {
		if c == code {
			if code == "jw" {
				return "Javanese"
			}
			words := strings.Fields(name)
			for i, w := range words {
				words[i] = strings.ToUpper(w[:1]) + w[1:]
			}
			return strings.Join(words, " ")
		}
	}
	return code
}

// KnownLanguage reports whether whisper.cpp knows a language code.
func KnownLanguage(code string) bool {
	for _, c := range whisperLangs {
		if c == code {
			return true
		}
	}
	return false
}
