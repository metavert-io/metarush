//go:build darwin || linux

package transcribe

import (
	"testing"
	"unsafe"
)

// The Go mirror of SherpaOnnxOfflineRecognizerConfig must match the C layout
// of every version in residentVersions byte for byte. These offsets were
// printed with offsetof() by a C program compiled against sherpa-onnx
// 1.13.8's c-api.h (arm64; the same on amd64: only pointers, int32 and float).
func TestResidentConfigLayout(t *testing.T) {
	var c cOfflineRecognizerConfig
	if got := unsafe.Sizeof(c); got != 608 {
		t.Fatalf("sizeof = %d, want 608", got)
	}
	for _, f := range []struct {
		name      string
		got, want uintptr
	}{
		{"feat_config.feature_dim", unsafe.Offsetof(c.featureDim), 4},
		{"model_config.transducer.encoder", unsafe.Offsetof(c.transducerEncoder), 8},
		{"model_config.transducer.joiner", unsafe.Offsetof(c.transducerJoiner), 24},
		{"model_config.whisper.enable_segment_timestamps", unsafe.Offsetof(c.whisperSegmentTimestamps), 88},
		{"model_config.tdnn.model", unsafe.Offsetof(c.tdnnModel), 96},
		{"model_config.tokens", unsafe.Offsetof(c.tokens), 104},
		{"model_config.num_threads", unsafe.Offsetof(c.numThreads), 112},
		{"model_config.debug", unsafe.Offsetof(c.debug), 116},
		{"model_config.provider", unsafe.Offsetof(c.provider), 120},
		{"model_config.model_type", unsafe.Offsetof(c.modelType), 128},
		{"model_config.modeling_unit", unsafe.Offsetof(c.modelingUnit), 136},
		{"model_config.sense_voice.use_itn", unsafe.Offsetof(c.senseVoiceUseITN), 176},
		{"model_config.moonshine.merged_decoder", unsafe.Offsetof(c.moonshine) + 4*8, 216},
		{"model_config.canary.use_pnc", unsafe.Offsetof(c.canaryUsePNC), 288},
		{"model_config.funasr_nano.hotwords", unsafe.Offsetof(c.funASRNano) + unsafe.Offsetof(c.funASRNano.hotwords), 400},
		{"model_config.qwen3_asr.hotwords", unsafe.Offsetof(c.qwen3ASR) + unsafe.Offsetof(c.qwen3ASR.hotwords), 472},
		{"model_config.cohere_transcribe.use_itn", unsafe.Offsetof(c.cohereUseITN), 508},
		{"lm_config.model", unsafe.Offsetof(c.lmModel), 512},
		{"lm_config.scale", unsafe.Offsetof(c.lmScale), 520},
		{"decoding_method", unsafe.Offsetof(c.decodingMethod), 528},
		{"max_active_paths", unsafe.Offsetof(c.maxActivePaths), 536},
		{"hotwords_file", unsafe.Offsetof(c.hotwordsFile), 544},
		{"hotwords_score", unsafe.Offsetof(c.hotwordsScore), 552},
		{"rule_fsts", unsafe.Offsetof(c.ruleFSTs), 560},
		{"rule_fars", unsafe.Offsetof(c.ruleFARs), 568},
		{"blank_penalty", unsafe.Offsetof(c.blankPenalty), 576},
		{"hr.dict_dir", unsafe.Offsetof(c.hrDictDir), 584},
		{"hr.rule_fsts", unsafe.Offsetof(c.hrRuleFSTs), 600},
	} {
		if f.got != f.want {
			t.Errorf("offsetof(%s) = %d, want %d", f.name, f.got, f.want)
		}
	}
}

// chunkSamples reproduces ffmpeg's cut: a millisecond is 16 samples at 16 kHz,
// and the times are the %.3f strings the CLI path passes to ffmpeg.
func TestChunkSamplesMatchesTheFFmpegCut(t *testing.T) {
	for _, c := range []struct {
		from, to float64
		n        int
		lo, hi   int
	}{
		{0, 30, 1_000_000, 0, 480_000},
		{28.5, 45.5, 1_000_000, 456_000, 728_000},
		{12.3455, 20.0004, 1_000_000, 197_520, 320_000}, // %.3f prints 12.3455 as 12.345 (it is 12.34549999… in binary)
		{60, 70.123, 1_000_000, 960_000, 1_000_000},     // clipped to the samples there are
		{70, 80, 1_000_000, 1_000_000, 1_000_000},
	} {
		lo, hi := chunkSamples(span{c.from, c.to}, c.n)
		if lo != c.lo || hi != c.hi {
			t.Errorf("chunkSamples(%v, %v) = [%d, %d), want [%d, %d)", c.from, c.to, lo, hi, c.lo, c.hi)
		}
	}
}
