package worker

import (
	"bytes"
	"context"
	"encoding/binary"
	"math"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/tools"
)

// decodeMono decodes a file's sound as mono float32 at 48 kHz.
func decodeMono(t *testing.T, ts *tools.Set, file string) []float32 {
	t.Helper()
	var out bytes.Buffer
	if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Stdout: &out,
		Args: []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-i", file, "-vn", "-ac", "1", "-ar", "48000", "-f", "f32le", "-"}}); err != nil {
		t.Fatal(err)
	}
	x := make([]float32, out.Len()/4)
	if err := binary.Read(bytes.NewReader(out.Bytes()[:len(x)*4]), binary.LittleEndian, x); err != nil {
		t.Fatal(err)
	}
	return x
}

// lagAt is where (seconds; b's time minus a's) the stretch [from, to) of a
// best matches b, searched within ±20 ms of expect (parabolic sub-sample peak).
func lagAt(a, b []float32, from, to, expect float64) float64 {
	const sr = 48000.0
	s0, s1 := int(from*sr), int(to*sr)
	center := int(math.Round(expect * sr))
	const w = 960
	corr := make([]float64, 2*w+1)
	for k := -w; k <= w; k++ {
		var c float64
		for m := s0; m < s1 && m < len(a); m++ {
			if n := m + center + k; n >= 0 && n < len(b) {
				c += float64(a[m]) * float64(b[n])
			}
		}
		corr[k+w] = c
	}
	best := 1
	for i := 1; i < len(corr)-1; i++ {
		if corr[i] > corr[best] {
			best = i
		}
	}
	p, q, r := corr[best-1], corr[best], corr[best+1]
	frac := 0.0
	if d := p - 2*q + r; d != 0 {
		frac = 0.5 * (p - r) / d
	}
	return (float64(center+best-w) + frac) / sr
}

// A drift copy made from compressed footage (AAC, the way recordings usually
// arrive) runs exactly at the recording's rate and starts exactly where the
// plan says: every burst of noise in it is where it belongs, to a fraction of
// a sample, early and late in the copy (D119).
func TestDriftCopyLinesUpToTheSample(t *testing.T) {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg is not installed")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "voice.m4a")
	// Bursts of noise 40 ms long every 0.43 s, from 0.32 s on, coded as AAC.
	if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-y", "-f", "lavfi",
		"-i", "aevalsrc=0.45*(2*random(1)-1)*lt(mod(t+0.11\\,0.43)\\,0.04):s=48000:c=mono", "-t", "14", "-c:a", "aac", "-b:a", "192k", src}}); err != nil {
		t.Fatal(err)
	}
	const fps, rate = 30.0, 1.000208
	exact := 3.2617 * fps // the element starts 3.2617 s into the recording
	trim, frac := editspec.SplitTrim(exact)
	plan := &editspec.RenderPlan{FPS: 30, Audio: []editspec.PlanAudio{{ID: "talk/bo", AssetID: "ast_bo", From: 22, DurationInFrames: 270, TrimBefore: trim, TrimFraction: frac,
		PlaybackRate: rate, Conversation: "talk", Member: "bo"}}}
	copies := editspec.DriftCopies(plan)
	if len(copies) != 1 || plan.Audio[0].AssetID != copies[0].Key || plan.Audio[0].PlaybackRate != 1 || copies[0].Start != 2 {
		t.Fatalf("copies %+v, element %+v", copies, plan.Audio[0])
	}
	out := filepath.Join(dir, "copy.wav")
	if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: copies[0].FFmpegArgs(src, out)}); err != nil {
		t.Fatal(err)
	}
	ref, cp := decodeMono(t, ts, src), decodeMono(t, ts, out)
	// In the copy, recording time tau is at copy time (tau − 2) / rate; the
	// element starts at copy time ExactTrim/fps (recording time 3.2617 s).
	if got := editspec.ExactTrim(plan.Audio[0].TrimBefore, plan.Audio[0].TrimFraction) / fps; math.Abs(got-(3.2617-2)/rate) > 1e-7 {
		t.Fatalf("the element starts at %.7f s into the copy, want %.7f", got, (3.2617-2)/rate)
	}
	for _, k := range []int{6, 9, 15, 21, 25} { // bursts at 0.43k − 0.11 s
		tau := 0.43*float64(k) - 0.11 + 0.02
		want := (tau-2)/rate - tau
		got := lagAt(ref, cp, tau-0.025, tau+0.025, want)
		if e := math.Abs(got - want); e > 1/48000.0 {
			t.Errorf("burst at %.2f s of the recording: %.1f µs from where it belongs in the copy (more than a sample)", tau, e*1e6)
		} else {
			t.Logf("burst at %.2f s of the recording: %+.2f µs from where it belongs in the copy", tau, (got-want)*1e6)
		}
	}
}
