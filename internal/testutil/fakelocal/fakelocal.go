// Package fakelocal stands in for the programs and model files of models
// that run on this computer (D278–D280), so their adapters and the shared
// lifecycle are tested without any download: a speech program that copies a
// WAV MetaRush made and records its arguments, pinned files "set up" in a
// temporary home, a model that repeats each pixel, a subject mask, a
// recognizer that always hears the same words.
package fakelocal

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"bytes"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/local"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/localmodels"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// Pins are the stand-in pins: the speech program, the two voice models and
// the upscaler, each 1 MB.
func Pins() []localmodels.Pin {
	sha := strings.Repeat("ab", 32)
	return []localmodels.Pin{
		{ID: local.ProgramPin, Title: "the offline voices' program", SHA256: sha, Bytes: 1 << 20, Root: "prog", Program: "bin/tts", Needs: []string{"lib"}},
		{ID: local.ModelKokoro, Title: "Kokoro v1.0", SHA256: sha, Bytes: 1 << 20, Root: "kokoro", Needs: []string{"model.onnx"}},
		{ID: local.ModelKitten, Title: "Kitten nano v0.8", SHA256: sha, Bytes: 1 << 20, Root: "kitten", Needs: []string{"model.int8.onnx"}},
		{ID: local.UpscalePin, Title: "Real-ESRGAN", SHA256: sha, Bytes: 1 << 20, Root: "esrgan", Needs: []string{"real_esrgan_general_x4v3.onnx"}},
	}
}

// Install puts a pin's files in place as setup would, without a download.
func Install(t testing.TB, s localmodels.Store, p localmodels.Pin, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		path := s.Path(p, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := os.FileMode(0o644)
		if rel == p.Program {
			mode = 0o755
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	marker := map[string]any{"archiveSha256": p.SHA256}
	if p.Program != "" {
		b, _ := os.ReadFile(s.Path(p, p.Program))
		sum := sha256.Sum256(b)
		marker["programSha256"] = hex.EncodeToString(sum[:])
	}
	b, _ := json.Marshal(marker)
	if err := os.WriteFile(filepath.Join(s.Dir(p), "installed.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Store makes a temporary home the local adapters use until the test ends.
func Store(t testing.TB) localmodels.Store {
	t.Helper()
	s := localmodels.Store{Home: t.TempDir(), Pins: Pins()}
	old := local.Stores
	local.Stores = func(aiservices.Env) localmodels.Store { return s }
	t.Cleanup(func() { local.Stores = old })
	return s
}

// fakeSpeech writes a WAV MetaRush made and remembers its arguments, one per line.
const fakeSpeech = `#!/bin/sh
: > "$FAKE_TTS_ARGS"
for a in "$@"; do
  printf '%s\n' "$a" >> "$FAKE_TTS_ARGS"
  case "$a" in --output-filename=*) cp "$FAKE_TTS_WAV" "${a#--output-filename=}" ;; esac
done
`

// Voices sets the offline voices up with a stand-in speech program; it
// returns the computer and the file the program writes its arguments to.
func Voices(t testing.TB) (aiservices.Env, string) {
	t.Helper()
	s := Store(t)
	ps := Pins()
	Install(t, s, ps[0], map[string]string{"bin/tts": fakeSpeech, "lib/libonnxruntime.dylib": "x"})
	Install(t, s, ps[1], map[string]string{"model.onnx": "x", "voices.bin": "x", "tokens.txt": "x", "lexicon-us-en.txt": "x"})
	Install(t, s, ps[2], map[string]string{"model.int8.onnx": "x", "voices.bin": "x", "tokens.txt": "x"})
	dir := t.TempDir()
	wav := filepath.Join(dir, "said.wav")
	if err := os.WriteFile(wav, aiservices.ToneWAV(1.5, 220), 0o600); err != nil {
		t.Fatal(err)
	}
	args := filepath.Join(dir, "args.txt")
	t.Setenv("FAKE_TTS_WAV", wav)
	t.Setenv("FAKE_TTS_ARGS", args)
	return aiservices.Env{Tools: tools.New(config.Tools{}), Cfg: &config.Config{}, Home: s.Home, Scratch: t.TempDir()}, args
}

// Nearest is a stand-in upscaling model: each pixel four times wider and taller.
func Nearest(in []float32) ([]float32, error) {
	const t, T = 128, 512
	out := make([]float32, 3*T*T)
	for c := 0; c < 3; c++ {
		for y := 0; y < T; y++ {
			for x := 0; x < T; x++ {
				out[c*T*T+y*T+x] = in[c*t*t+(y/4)*t+x/4]
			}
		}
	}
	return out, nil
}

// Upscaler sets the upscaler up with Nearest as its model.
func Upscaler(t testing.TB) aiservices.Env {
	t.Helper()
	s := Store(t)
	Install(t, s, Pins()[3], map[string]string{"real_esrgan_general_x4v3.onnx": "x"})
	oldL, oldO := local.Library, local.OpenUpscaler
	t.Cleanup(func() { local.Library, local.OpenUpscaler = oldL, oldO })
	local.Library = func(aiservices.Env) string { return "libonnxruntime" }
	local.OpenUpscaler = func(aiservices.Env, localmodels.Store, localmodels.Pin) (local.Runner, error) { return Nearest, nil }
	return aiservices.Env{Tools: &tools.Set{}, Cfg: &config.Config{}, Home: s.Home}
}

// Masker stands in for the subject mask: it cuts out a small red dot and
// records the type of each picture it was handed.
func Masker(t testing.TB) *[]string {
	t.Helper()
	oldReady, oldMask := local.MaskerReady, local.Masker
	t.Cleanup(func() { local.MaskerReady, local.Masker = oldReady, oldMask })
	var handed []string
	local.MaskerReady = func(aiservices.Env) error { return nil }
	local.Masker = func(_ context.Context, _ aiservices.Env, in, out string) (bool, error) {
		if !filepath.IsAbs(in) {
			t.Errorf("the helper was handed a relative path %q", in)
		}
		b, _ := os.ReadFile(in)
		handed = append(handed, aiservices.Sniff(b))
		img := image.NewNRGBA(image.Rect(0, 0, 4, 4))
		img.SetNRGBA(1, 1, color.NRGBA{200, 30, 40, 255})
		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		return true, os.WriteFile(out, buf.Bytes(), 0o600)
	}
	return &handed
}

// Recognizer is a stand-in that writes down "water the beans" for any
// recording, named as Parakeet.
type Recognizer struct{}

func (Recognizer) Name() string  { return "parakeet" }
func (Recognizer) Model() string { return "tdt-0.6b-v2-int8" }
func (Recognizer) Transcribe(context.Context, string, string, string) (*transcribe.Transcript, error) {
	return &transcribe.Transcript{Language: "en", Segments: []transcribe.Segment{{From: 0.1, To: 1.2, Text: "water the beans",
		Words: []models.Word{{Text: "water", From: 0.1, To: 0.4}, {Text: "the", From: 0.4, To: 0.5}, {Text: "beans", From: 0.5, To: 1.2}}}}}, nil
}

// Hearing makes Recognizer this computer's recognizer until the test ends.
func Hearing(t testing.TB) {
	t.Helper()
	old := local.Recognizer
	local.Recognizer = func(aiservices.Env, string, string) (transcribe.Provider, error) { return Recognizer{}, nil }
	t.Cleanup(func() { local.Recognizer = old })
}
