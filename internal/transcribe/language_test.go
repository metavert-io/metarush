package transcribe

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// whisper.cpp's detection line, as whisper-cli prints it on stderr.
func TestParseDetected(t *testing.T) {
	l, p, err := parseDetected("whisper_full_with_state: auto-detected language: ja (p = 0.998473)\nother lines")
	if err != nil || l != "ja" || p != 0.998473 {
		t.Fatalf("%q %v %v", l, p, err)
	}
	if l, _, _ := parseDetected("no language here"); l != "" {
		t.Fatalf("nothing printed is no language: %q", l)
	}
	if l, _, _ := parseDetected("auto-detected language: haw (p = 0.41)"); l != "haw" {
		t.Fatalf("three-letter codes: %q", l)
	}
}

// The detection listens from where speech starts and from the middle of the
// speech, never to silence, and not at all to a clip with too little speech.
func TestSpeechWindows(t *testing.T) {
	// 0–10 s of music-free silence, then speech to 100 s with a pause at 50–52.
	w := speechWindows(100, []span{{0, 10}, {50, 52}}, DetectWindow, 2)
	if len(w) != 2 || w[0].from != 10 || w[0].to != 40 {
		t.Fatalf("first window from where speech starts: %+v", w)
	}
	// Half of the 88 s of speech is 44 s in: 40 s before the pause, 4 after it.
	if w[1].from != 56 || w[1].to != 86 {
		t.Fatalf("second window from the middle of the speech: %+v", w)
	}
	// A short clip gets one window, cut to the clip.
	if w := speechWindows(12, nil, DetectWindow, 2); len(w) != 1 || w[0].to != 12 {
		t.Fatalf("short clip: %+v", w)
	}
	// Under two seconds of speech: none.
	if w := speechWindows(10, []span{{0, 9}}, DetectWindow, 2); len(w) != 0 {
		t.Fatalf("too little speech: %+v", w)
	}
	if got := speechSpans(10, []span{{2, 3}, {0, 1}}); len(got) != 2 || got[0] != (span{1, 2}) || got[1] != (span{3, 10}) {
		t.Fatalf("speech spans %+v", got)
	}
}

// English goes to the English recognizer, other languages to whisper.cpp; a
// workspace language turns the detection off; an English-only model can't
// detect.
func TestLanguagesPickARecognizerPerLanguage(t *testing.T) {
	ts, _ := fakeRecognizers(t, true, true)
	l := NewLanguages(config.Transcription{}, ts)
	if l == nil || l.Default != "en" || l.Detect == nil {
		t.Fatalf("defaults: %+v", l)
	}
	if p := l.For("en"); p == nil || p.Name() != "parakeet" {
		t.Fatalf("English on Parakeet: %v", p)
	}
	if p := l.For("en-GB"); p == nil || p.Name() != "parakeet" {
		t.Fatalf("English variants too: %v", p)
	}
	if p := l.For("es"); p == nil || p.Name() != "whisper.cpp" {
		t.Fatalf("Spanish on whisper.cpp: %v", p)
	}
	l = NewLanguages(config.Transcription{Language: "fr"}, ts)
	if l.Default != "fr" || l.Detect != nil {
		t.Fatalf("a workspace language: %+v", l)
	}
	l = NewLanguages(config.Transcription{Provider: "whispercpp"}, ts)
	if p := l.For("en"); p == nil || p.Name() != "whisper.cpp" {
		t.Fatalf("whisper.cpp named for everything: %v", p)
	}
	if NewLanguages(config.Transcription{Provider: "none"}, ts) != nil {
		t.Fatal("none transcribes nothing")
	}
	// An English-only whisper.cpp model writes English only: no other
	// languages, no detection.
	tsEn, modelsEn := fakeRecognizers(t, false, false)
	touch(t, modelsEn+"/whisper/ggml-base.en.bin")
	tsEn = tools.New(config.Tools{ModelsDir: modelsEn, FFmpeg: tsEn.Path(tools.FFmpeg), WhisperCLI: tsEn.Path(tools.FFmpeg), SherpaDir: modelsEn + "/none"})
	l = NewLanguages(config.Transcription{}, tsEn)
	if l == nil || l.For("es") != nil || l.Detect != nil || l.Others() {
		t.Fatalf("an English-only model: %+v", l)
	}
	if p := l.For("en"); p == nil || p.Name() != "whisper.cpp" {
		t.Fatalf("it still writes English: %v", p)
	}
	// Parakeet only: English, and nothing for other languages or detection.
	ts, _ = fakeRecognizers(t, true, false)
	l = NewLanguages(config.Transcription{}, ts)
	if l.For("es") != nil || l.Detect != nil || l.Others() {
		t.Fatalf("without whisper.cpp: %+v", l)
	}
}

func TestSpokenLanguageNames(t *testing.T) {
	for code, want := range map[string]string{"es": "Spanish", "ht": "Haitian Creole", "jw": "Javanese", "yue": "Cantonese", "xx": "xx"} {
		if got := SpokenLanguageName(code); got != want {
			t.Errorf("%s: %q, want %q", code, got, want)
		}
	}
	list := SpokenLanguages()
	if list[0].Code != "en" || list[1].Code != "es" || len(list) < 90 {
		t.Fatalf("most spoken first, then the rest: %d, %+v", len(list), list[:3])
	}
	seen := map[string]bool{}
	for _, l := range list {
		if seen[l.Code] {
			t.Fatalf("%s twice", l.Code)
		}
		seen[l.Code] = true
	}
	if !KnownLanguage("pt") || KnownLanguage("xx") {
		t.Fatal("known languages")
	}
}

// fakeRecognizers is a tools.Set whose whisper-cli, sherpa-onnx and ffmpeg
// are stand-ins (never run here), with model files of their own.
func fakeRecognizers(t *testing.T, parakeet, whisper bool) (*tools.Set, string) {
	t.Helper()
	dir := t.TempDir()
	bin := writeExec(t, dir, "bin")
	// An explicit path that isn't a program keeps the real ones on this
	// machine out of the test.
	cfg := config.Tools{ModelsDir: dir + "/models", FFmpeg: bin, WhisperCLI: dir + "/none", SherpaDir: dir + "/none"}
	if whisper {
		cfg.WhisperCLI = bin
		touch(t, dir+"/models/whisper/ggml-large-v3-turbo.bin")
	}
	if parakeet {
		cfg.SherpaDir = dir + "/sherpa"
		writeExec(t, dir+"/sherpa/bin", "sherpa-onnx-offline")
		for _, f := range []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"} {
			touch(t, dir+"/models/parakeet/"+ParakeetModelDir+"/"+f)
		}
	}
	return tools.New(cfg), dir + "/models"
}

func writeExec(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
