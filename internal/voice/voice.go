// Package voice turns speaker-diarization output into stable, nameable speakers:
// it computes voice fingerprints (TitaNet-Small embeddings through the
// sherpa-onnx C library, on this machine), repairs over-segmented diarization
// (tiny clusters, one person split in two), picks short listenable samples per
// speaker, and matches speakers against the people a workspace has named.
//
// Everything here is local and deterministic for a given input; nothing leaves
// the machine. Thresholds were tuned on real speech (docs/speakers.md).
package voice

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
)

// SampleRate is the only rate fingerprints are computed at (ExtractAudioWAV).
const SampleRate = 16000

// MinSamples is the shortest input the extractor accepts (0.5 s).
const MinSamples = SampleRate / 2

// ModelName identifies the embedding model stored next to every fingerprint;
// fingerprints from different models are never compared.
const ModelName = "nemo-titanet-small"

// ModelFile is the embedding model's file name under <models>/diarization.
const ModelFile = "nemo_en_titanet_small.onnx"

var (
	// ErrUnavailable means the embedding library or model is not installed.
	ErrUnavailable = errors.New("speaker embeddings are unavailable (sherpa-onnx C library or TitaNet model missing)")
	// ErrTooShort means the audio is too short for a reliable embedding.
	ErrTooShort = errors.New("audio too short for a speaker embedding")
)

// Library locates libsherpa-onnx-c-api next to a sherpa-onnx CLI binary
// (<prefix>/bin/sherpa-onnx-… → <prefix>/lib/libsherpa-onnx-c-api.{dylib,so}).
func Library(sherpaBin string) string {
	if sherpaBin == "" {
		return ""
	}
	p, err := filepath.EvalSymlinks(sherpaBin)
	if err != nil {
		p = sherpaBin
	}
	name := "libsherpa-onnx-c-api.so"
	if runtime.GOOS == "darwin" {
		name = "libsherpa-onnx-c-api.dylib"
	}
	lib := filepath.Join(filepath.Dir(filepath.Dir(p)), "lib", name)
	if _, err := os.Stat(lib); err != nil {
		return ""
	}
	return lib
}

// ReadWAV reads 16 kHz mono 16-bit PCM (the format ExtractAudioWAV writes).
func ReadWAV(path string) ([]float32, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, fmt.Errorf("%s is not a WAV file", path)
	}
	var rate, channels, bits int
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4:]))
		body := b[off+8 : min(len(b), off+8+size)]
		switch id {
		case "fmt ":
			if len(body) >= 16 {
				channels = int(binary.LittleEndian.Uint16(body[2:]))
				rate = int(binary.LittleEndian.Uint32(body[4:]))
				bits = int(binary.LittleEndian.Uint16(body[14:]))
			}
		case "data":
			if rate != SampleRate || channels != 1 || bits != 16 {
				return nil, fmt.Errorf("%s: want 16 kHz mono 16-bit PCM, got %d Hz, %d ch, %d bit", path, rate, channels, bits)
			}
			out := make([]float32, len(body)/2)
			for i := range out {
				out[i] = float32(int16(binary.LittleEndian.Uint16(body[2*i:]))) / 32768
			}
			return out, nil
		}
		off += 8 + size + size%2
	}
	return nil, fmt.Errorf("%s has no audio data", path)
}

// Normalize scales v to unit length in place.
func Normalize(v []float32) {
	s := 0.0
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	if s == 0 {
		return
	}
	s = math.Sqrt(s)
	for i := range v {
		v[i] = float32(float64(v[i]) / s)
	}
}

// Cosine is the cosine similarity of two vectors (−1…1; 0 when either is empty
// or the sizes differ).
func Cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / math.Sqrt(na*nb)
}

// Mean is the weighted, normalized mean of unit vectors (weights ≤ 0 count as 1).
func Mean(vs [][]float32, weights []float64) []float32 {
	var out []float32
	for i, v := range vs {
		if len(v) == 0 {
			continue
		}
		if out == nil {
			out = make([]float32, len(v))
		}
		if len(v) != len(out) {
			continue
		}
		w := 1.0
		if i < len(weights) && weights[i] > 0 {
			w = weights[i]
		}
		for k := range v {
			out[k] += float32(w) * v[k]
		}
	}
	Normalize(out)
	return out
}

// Span is a time range in source seconds.
type Span struct {
	From float64 `bson:"from" json:"from"`
	To   float64 `bson:"to" json:"to"`
}

// Len is the span's duration.
func (s Span) Len() float64 { return s.To - s.From }

func slice(pcm []float32, s Span) []float32 {
	a := max(0, int(s.From*SampleRate))
	b := min(len(pcm), int(s.To*SampleRate))
	if b <= a {
		return nil
	}
	return pcm[a:b]
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
