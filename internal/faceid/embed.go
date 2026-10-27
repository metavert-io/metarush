package faceid

import (
	"errors"
	"fmt"
	"image"
	"math"
	"sync"

	"github.com/metavert-io/metarush/internal/onnx"
)

// ModelName identifies the fingerprint model stored next to every face
// fingerprint; fingerprints from different models are never compared.
const ModelName = "sface-2021dec"

// Dim is the size of a face fingerprint.
const Dim = 128

// MinEyes is the smallest distance between the eyes (pixels of the frame
// looked at) a face needs for a fingerprint: smaller faces are mostly blur.
const MinEyes = 18.0

// ErrUnavailable means onnxruntime or the face model is not installed here.
var ErrUnavailable = errors.New("recognizing faces is not set up on this computer")

// Embedder makes face fingerprints. It is safe for concurrent use.
type Embedder struct {
	m *onnx.Model
}

var (
	embMu    sync.Mutex
	embCache = map[string]*Embedder{}
)

// Open loads the model with onnxruntime (lib) once per process.
func Open(lib, model string) (*Embedder, error) {
	if lib == "" || model == "" {
		return nil, ErrUnavailable
	}
	embMu.Lock()
	defer embMu.Unlock()
	key := lib + "\x00" + model
	if e, ok := embCache[key]; ok {
		return e, nil
	}
	m, err := onnx.Open(lib, model, 2)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	e := &Embedder{m: m}
	embCache[key] = e
	return e, nil
}

// Embed returns the unit-length fingerprint of the face whose five points
// (pixels of img) are pts.
func (e *Embedder) Embed(img image.Image, pts [5][2]float64) ([]float32, error) {
	if e == nil || e.m == nil {
		return nil, ErrUnavailable
	}
	in := AlignedTensor(img, pts)
	out, err := e.m.Run(in, []int64{1, 3, Size, Size})
	if err != nil {
		return nil, err
	}
	if len(out) == 0 || len(out[0].Data) < Dim {
		return nil, errors.New("the face model gave no answer")
	}
	v := append([]float32(nil), out[0].Data[:Dim]...)
	Normalize(v)
	return v, nil
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

// Cosine is the cosine similarity of two fingerprints (0 when either is
// empty or their sizes differ).
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

// Mean is the weighted, normalized mean of fingerprints (weights ≤ 0 count as 1).
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
