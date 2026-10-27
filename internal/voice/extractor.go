//go:build darwin || linux

package voice

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The sherpa-onnx C API, loaded at run time with purego (no cgo, no headers at
// build time). Only the speaker-embedding entry points are bound.
type cExtractorConfig struct {
	model      *byte
	numThreads int32
	debug      int32
	provider   *byte
}

var (
	cOnce        sync.Once
	cErr         error
	cLib         string
	cCreate      func(cfg *cExtractorConfig) uintptr
	cDestroy     func(p uintptr)
	cDim         func(p uintptr) int32
	cNewStream   func(p uintptr) uintptr
	cIsReady     func(p, s uintptr) int32
	cCompute     func(p, s uintptr) *float32
	cFreeVector  func(v *float32)
	cAccept      func(s uintptr, sampleRate int32, samples *float32, n int32)
	cFinished    func(s uintptr)
	cDestroyStrm func(s uintptr)
)

func bind(lib string) error {
	cOnce.Do(func() {
		cLib = lib
		h, err := purego.Dlopen(lib, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			cErr = fmt.Errorf("load %s: %w", lib, err)
			return
		}
		defer func() {
			if r := recover(); r != nil { // RegisterLibFunc panics on a missing symbol
				cErr = fmt.Errorf("%s lacks the speaker-embedding API: %v", lib, r)
			}
		}()
		purego.RegisterLibFunc(&cCreate, h, "SherpaOnnxCreateSpeakerEmbeddingExtractor")
		purego.RegisterLibFunc(&cDestroy, h, "SherpaOnnxDestroySpeakerEmbeddingExtractor")
		purego.RegisterLibFunc(&cDim, h, "SherpaOnnxSpeakerEmbeddingExtractorDim")
		purego.RegisterLibFunc(&cNewStream, h, "SherpaOnnxSpeakerEmbeddingExtractorCreateStream")
		purego.RegisterLibFunc(&cIsReady, h, "SherpaOnnxSpeakerEmbeddingExtractorIsReady")
		purego.RegisterLibFunc(&cCompute, h, "SherpaOnnxSpeakerEmbeddingExtractorComputeEmbedding")
		purego.RegisterLibFunc(&cFreeVector, h, "SherpaOnnxSpeakerEmbeddingExtractorDestroyEmbedding")
		purego.RegisterLibFunc(&cAccept, h, "SherpaOnnxOnlineStreamAcceptWaveform")
		purego.RegisterLibFunc(&cFinished, h, "SherpaOnnxOnlineStreamInputFinished")
		purego.RegisterLibFunc(&cDestroyStrm, h, "SherpaOnnxDestroyOnlineStream")
	})
	if cErr == nil && lib != cLib {
		return fmt.Errorf("speaker-embedding library already loaded from %s", cLib)
	}
	return cErr
}

// Extractor computes speaker embeddings with the NeMo TitaNet-Small model that
// diarization uses, through the sherpa-onnx C library. It is safe for
// concurrent use (calls are serialized).
type Extractor struct {
	mu  sync.Mutex
	p   uintptr
	dim int
}

func cString(s string) []byte { return append([]byte(s), 0) }

// Open loads the library (once per process) and the embedding model.
func Open(lib, model string, threads int) (*Extractor, error) {
	if _, err := os.Stat(lib); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, lib)
	}
	if _, err := os.Stat(model); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, model)
	}
	if err := bind(lib); err != nil {
		return nil, err
	}
	if threads < 1 {
		threads = 1
	}
	m, prov := cString(model), cString("cpu")
	cfg := cExtractorConfig{model: &m[0], numThreads: int32(threads), provider: &prov[0]}
	p := cCreate(&cfg)
	runtime.KeepAlive(m)
	runtime.KeepAlive(prov)
	if p == 0 {
		return nil, fmt.Errorf("sherpa-onnx could not load the speaker-embedding model %s", model)
	}
	return &Extractor{p: p, dim: int(cDim(p))}, nil
}

// Dim is the embedding size (192 for TitaNet-Small).
func (e *Extractor) Dim() int { return e.dim }

// Embed returns the L2-normalized embedding of 16 kHz mono samples.
func (e *Extractor) Embed(samples []float32) ([]float32, error) {
	if len(samples) < MinSamples {
		return nil, ErrTooShort
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.p == 0 {
		return nil, errors.New("extractor is closed")
	}
	s := cNewStream(e.p)
	if s == 0 {
		return nil, errors.New("sherpa-onnx could not create a stream")
	}
	defer cDestroyStrm(s)
	cAccept(s, SampleRate, &samples[0], int32(len(samples)))
	cFinished(s)
	runtime.KeepAlive(samples)
	if cIsReady(e.p, s) == 0 {
		return nil, ErrTooShort
	}
	v := cCompute(e.p, s)
	if v == nil {
		return nil, errors.New("sherpa-onnx returned no embedding")
	}
	out := make([]float32, e.dim)
	copy(out, unsafe.Slice(v, e.dim))
	cFreeVector(v)
	Normalize(out)
	return out, nil
}

// Close frees the model.
func (e *Extractor) Close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.p != 0 {
		cDestroy(e.p)
		e.p = 0
	}
}
