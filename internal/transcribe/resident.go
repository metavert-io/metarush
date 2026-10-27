//go:build darwin || linux

package transcribe

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"github.com/ebitengine/purego"
)

// The sherpa-onnx C API for the offline (non-streaming) recognizer, loaded at
// run time with purego like internal/voice does for TitaNet. Only what a
// resident Parakeet model needs is bound: create/destroy the recognizer,
// streams, batch decoding and the result as the same JSON line the
// sherpa-onnx-offline CLI prints (so the bake-off's word rules apply
// unchanged). The recognizer runs in the model server process (server.go),
// never in the daemon: ONNX Runtime's memory arena grows across batches of
// different shapes (measured: 9.4 GB after a 31-minute file, 5.8 GB still
// resident after freeing the recognizer), so it lives in a process MetaRush
// can stop.
//
// cOfflineRecognizerConfig mirrors SherpaOnnxOfflineRecognizerConfig of the
// versions in residentVersions byte for byte (608 bytes on 64-bit; the offsets
// are pinned by TestResidentConfigLayout, measured with offsetof against the
// 1.13.8 header). A library of another version could lay the struct out
// differently, so it is never handed this struct: the provider then runs the
// CLI once per batch as before.
type cOfflineRecognizerConfig struct {
	// feat_config
	sampleRate, featureDim int32
	// model_config
	transducerEncoder, transducerDecoder, transducerJoiner *byte
	paraformerModel                                        *byte
	nemoCTCModel                                           *byte
	whisperEncoder, whisperDecoder                         *byte
	whisperLanguage, whisperTask                           *byte
	whisperTailPaddings                                    int32
	whisperTokenTimestamps                                 int32
	whisperSegmentTimestamps                               int32
	tdnnModel                                              *byte
	tokens                                                 *byte
	numThreads, debug                                      int32
	provider, modelType, modelingUnit, bpeVocab            *byte
	telespeechCTC                                          *byte
	senseVoiceModel, senseVoiceLanguage                    *byte
	senseVoiceUseITN                                       int32
	moonshine                                              [5]*byte
	fireRedASR                                             [2]*byte
	dolphinModel, zipformerCTCModel                        *byte
	canaryEncoder, canaryDecoder, canarySrc, canaryTgt     *byte
	canaryUsePNC                                           int32
	wenetCTCModel, omnilingualModel, medASRModel           *byte
	funASRNano                                             cFunASRNano
	fireRedASRCTCModel                                     *byte
	qwen3ASR                                               cQwen3ASR
	cohereEncoder, cohereDecoder, cohereLanguage           *byte
	cohereUsePunct, cohereUseITN                           int32
	// lm_config
	lmModel *byte
	lmScale float32
	// decoding
	decodingMethod *byte
	maxActivePaths int32
	hotwordsFile   *byte
	hotwordsScore  float32
	ruleFSTs       *byte
	ruleFARs       *byte
	blankPenalty   float32
	// hr (homophone replacer)
	hrDictDir, hrLexicon, hrRuleFSTs *byte
}

type cFunASRNano struct {
	encoderAdaptor, llm, embedding, tokenizer, systemPrompt, userPrompt *byte
	maxNewTokens                                                        int32
	temperature, topP                                                   float32
	seed                                                                int32
	language                                                            *byte
	itn                                                                 int32
	hotwords                                                            *byte
}

type cQwen3ASR struct {
	convFrontend, encoder, decoder, tokenizer *byte
	maxTotalLen, maxNewTokens                 int32
	temperature, topP                         float32
	seed                                      int32
	hotwords                                  *byte
}

// residentVersions are the sherpa-onnx library versions whose
// SherpaOnnxOfflineRecognizerConfig layout cOfflineRecognizerConfig matches
// (bootstrap.sh installs 1.13.8). Add a version only after comparing its
// c-api.h struct definitions (TestResidentConfigLayout documents the offsets).
var residentVersions = map[string]bool{"1.13.8": true}

var (
	rOnce       sync.Once
	rErr        error
	rLib        string
	rVersion    string
	rVersionStr func() string
	rCreate     func(cfg *cOfflineRecognizerConfig) uintptr
	rDestroy    func(p uintptr)
	rNewStream  func(p uintptr) uintptr
	rFreeStream func(s uintptr)
	rAccept     func(s uintptr, sampleRate int32, samples *float32, n int32)
	rDecodeMany func(p uintptr, streams *uintptr, n int32)
	rResultJSON func(s uintptr) *byte
	rFreeJSON   func(s *byte)
)

// bindOffline loads the library once per process and checks its version.
func bindOffline(lib string) error {
	rOnce.Do(func() {
		rLib = lib
		h, err := purego.Dlopen(lib, purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			rErr = fmt.Errorf("load %s: %w", lib, err)
			return
		}
		defer func() {
			if r := recover(); r != nil { // RegisterLibFunc panics on a missing symbol
				rErr = fmt.Errorf("%s lacks the offline recognizer API: %v", lib, r)
			}
		}()
		purego.RegisterLibFunc(&rVersionStr, h, "SherpaOnnxGetVersionStr")
		rVersion = rVersionStr()
		if !residentVersions[rVersion] {
			rErr = fmt.Errorf("sherpa-onnx %s is a version whose model settings this MetaRush does not know (it knows 1.13.8)", rVersion)
			return
		}
		purego.RegisterLibFunc(&rCreate, h, "SherpaOnnxCreateOfflineRecognizer")
		purego.RegisterLibFunc(&rDestroy, h, "SherpaOnnxDestroyOfflineRecognizer")
		purego.RegisterLibFunc(&rNewStream, h, "SherpaOnnxCreateOfflineStream")
		purego.RegisterLibFunc(&rFreeStream, h, "SherpaOnnxDestroyOfflineStream")
		purego.RegisterLibFunc(&rAccept, h, "SherpaOnnxAcceptWaveformOffline")
		purego.RegisterLibFunc(&rDecodeMany, h, "SherpaOnnxDecodeMultipleOfflineStreams")
		purego.RegisterLibFunc(&rResultJSON, h, "SherpaOnnxGetOfflineStreamResultAsJson")
		purego.RegisterLibFunc(&rFreeJSON, h, "SherpaOnnxDestroyOfflineStreamResultJson")
	})
	if rErr == nil && lib != rLib {
		return fmt.Errorf("the offline recognizer library is already loaded from %s", rLib)
	}
	return rErr
}

// residentSupported reports whether this machine's sherpa-onnx library can
// hold the model (it exists and is a known version). It loads the library into
// this process to read its version (internal/voice loads the same library).
func residentSupported(lib string) error {
	if lib == "" {
		return fmt.Errorf("%w: no sherpa-onnx C library next to the command-line tools", ErrResidentUnavailable)
	}
	if _, err := os.Stat(lib); err != nil {
		return fmt.Errorf("%w: %s", ErrResidentUnavailable, lib)
	}
	if err := bindOffline(lib); err != nil {
		return fmt.Errorf("%w: %v", ErrResidentUnavailable, err)
	}
	return nil
}

// goString copies a NUL-terminated C string (memory the library owns).
func goString(p *byte) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*byte)(unsafe.Add(unsafe.Pointer(p), n)) != 0 {
		n++
	}
	return string(unsafe.Slice(p, n))
}

// localModel is a loaded Parakeet recognizer in this process (the model server).
type localModel struct {
	mu sync.Mutex
	p  uintptr
}

// openLocal loads the model with the settings the CLI path runs with
// (sherpa-onnx-offline's defaults plus MetaRush's flags), so a decode here is
// the CLI's decode.
func openLocal(lib, dir string, threads int) (*localModel, error) {
	if err := residentSupported(lib); err != nil {
		return nil, err
	}
	cs := func(s string) *byte { b := append([]byte(s), 0); return &b[0] }
	enc, dec, join, tok := cs(dir+"/encoder.int8.onnx"), cs(dir+"/decoder.int8.onnx"), cs(dir+"/joiner.int8.onnx"), cs(dir+"/tokens.txt")
	prov, typ, method := cs("cpu"), cs("nemo_transducer"), cs("greedy_search")
	cfg := cOfflineRecognizerConfig{sampleRate: 16000, featureDim: 80, transducerEncoder: enc, transducerDecoder: dec, transducerJoiner: join,
		tokens: tok, numThreads: int32(threads), provider: prov, modelType: typ, decodingMethod: method, maxActivePaths: 4, hotwordsScore: 1.5}
	p := rCreate(&cfg)
	runtime.KeepAlive(&cfg)
	for _, b := range []*byte{enc, dec, join, tok, prov, typ, method} {
		runtime.KeepAlive(b)
	}
	if p == 0 {
		return nil, fmt.Errorf("%w: sherpa-onnx could not load the model in %s", ErrResidentUnavailable, dir)
	}
	return &localModel{p: p}, nil
}

// decode runs one batch (≤16 chunks of ≤30 s) and returns each chunk's result
// as sherpa-onnx-offline prints it, in order: the same DecodeStreams call the
// CLI makes for the files on its command line.
func (m *localModel) decode(chunks [][]float32) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.p == 0 {
		return nil, errors.New("the model is closed")
	}
	if len(chunks) == 0 {
		return []string{}, nil
	}
	streams := make([]uintptr, len(chunks))
	defer func() {
		for _, s := range streams {
			if s != 0 {
				rFreeStream(s)
			}
		}
	}()
	var silence [1]float32
	for i, c := range chunks {
		s := rNewStream(m.p)
		if s == 0 {
			return nil, errors.New("sherpa-onnx could not create a stream")
		}
		streams[i] = s
		if len(c) > 0 {
			rAccept(s, 16000, &c[0], int32(len(c)))
		} else {
			rAccept(s, 16000, &silence[0], 0) // an empty chunk decodes like the CLI's empty file: no tokens
		}
		runtime.KeepAlive(c)
	}
	rDecodeMany(m.p, &streams[0], int32(len(streams)))
	out := make([]string, len(streams))
	for i, s := range streams {
		j := rResultJSON(s)
		if j == nil {
			return nil, errors.New("sherpa-onnx returned no result")
		}
		out[i] = goString(j)
		rFreeJSON(j)
	}
	return out, nil
}

func (m *localModel) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.p != 0 {
		rDestroy(m.p)
		m.p = 0
	}
}

// libraryVersion is the loaded library's version ("" before loading).
func libraryVersion() string { return rVersion }

// maxRSS is this process's peak memory in bytes (getrusage reports bytes on
// macOS and KiB on Linux).
func maxRSS() int64 {
	var ru syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &ru) != nil {
		return 0
	}
	if runtime.GOOS == "darwin" {
		return int64(ru.Maxrss)
	}
	return int64(ru.Maxrss) * 1024
}
