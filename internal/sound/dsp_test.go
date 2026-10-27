package sound

import (
	"math"
	"math/rand"
	"path/filepath"
	"testing"
)

func TestWAVRoundTrip(t *testing.T) {
	a := &Audio{Rate: 48000, Channels: 2}
	for i := 0; i < 4800; i++ {
		v := float32(0.5 * math.Sin(2*math.Pi*440*float64(i)/48000))
		a.Data = append(a.Data, v, -v)
	}
	dir := t.TempDir()
	for _, float := range []bool{false, true} {
		p := filepath.Join(dir, "x.wav")
		if err := WriteWAV(p, a, float); err != nil {
			t.Fatal(err)
		}
		b, err := ReadWAV(p)
		if err != nil {
			t.Fatal(err)
		}
		if b.Rate != 48000 || b.Channels != 2 || b.Frames() != 4800 {
			t.Fatalf("shape %d Hz %d ch %d frames", b.Rate, b.Channels, b.Frames())
		}
		tol := 1e-6
		if !float {
			tol = 2.0 / 32768
		}
		for i := range a.Data {
			if math.Abs(float64(a.Data[i]-b.Data[i])) > tol {
				t.Fatalf("sample %d: %v vs %v", i, a.Data[i], b.Data[i])
			}
		}
		if m := b.Mono(); len(m) != 4800 || math.Abs(float64(m[100])) > tol {
			t.Fatalf("mono of opposite channels is silent")
		}
	}
}

func TestBestLagFindsTheShift(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	x := make([]float32, 8000)
	for i := range x {
		x[i] = float32(rng.NormFloat64())
	}
	for _, lag := range []int{0, 7, -13, 120} {
		y := shift(x, -lag) // y[i] = x[i-lag]: y is late by lag
		for i := range y {
			y[i] += float32(0.1 * rng.NormFloat64())
		}
		got, corr := bestLag(x, y, 200)
		if got != lag || corr < 0.8 {
			t.Fatalf("lag %d: found %d (correlation %.2f)", lag, got, corr)
		}
	}
}

// 120 bpm clicks over a quiet pad: the tempo and every beat are found.
func TestBeatsOnAClickTrack(t *testing.T) {
	rate := BeatRate
	secs := 20.0
	x := make([]float32, int(secs*float64(rate)))
	rng := rand.New(rand.NewSource(2))
	for i := range x {
		tt := float64(i) / float64(rate)
		x[i] = float32(0.05*math.Sin(2*math.Pi*220*tt) + 0.005*rng.NormFloat64())
	}
	first := 0.25
	for b := first; b < secs; b += 0.5 {
		start := int(b * float64(rate))
		for k := 0; k < rate/20 && start+k < len(x); k++ { // a 50 ms decaying click
			env := math.Exp(-float64(k) / float64(rate/200))
			x[start+k] += float32(0.6 * env * math.Sin(2*math.Pi*1500*float64(k)/float64(rate)))
		}
	}
	bt := AnalyzeBeats(x)
	if bt.Onsets || math.Abs(bt.Tempo-120) > 1 {
		t.Fatalf("tempo %.1f (onsets %v, confidence %.2f)", bt.Tempo, bt.Onsets, bt.Confidence)
	}
	hits := 0
	for _, b := range bt.Times {
		k := math.Round((b - first) / 0.5)
		if math.Abs(b-(first+k*0.5)) <= 0.035 {
			hits++
		}
	}
	if hits < len(bt.Times)*9/10 || len(bt.Times) < 30 {
		t.Fatalf("beats off the clicks: %d of %d within 35 ms (%v)", hits, len(bt.Times), bt.Times)
	}
}

func TestNoiseEstimateRanksCleanerHigher(t *testing.T) {
	rate := 16000
	rng := rand.New(rand.NewSource(3))
	make1 := func(noise float64) []float32 {
		x := make([]float32, rate*8)
		for i := range x {
			tt := float64(i) / float64(rate)
			v := 0.0
			if math.Mod(tt, 1) < 0.6 { // "speech" 60% of the time
				v = 0.3 * math.Sin(2*math.Pi*200*tt) * (0.6 + 0.4*math.Sin(2*math.Pi*3*tt))
			}
			x[i] = float32(v + noise*rng.NormFloat64())
		}
		return x
	}
	noisy, _ := EstimateNoise(make1(0.02), rate)
	clean, _ := EstimateNoise(make1(0.002), rate)
	if !(clean.SNR > noisy.SNR+15) {
		t.Fatalf("cleaner %v vs noisier %v", clean, noisy)
	}
	if noisy.SNR < 10 || noisy.SNR > 30 {
		t.Fatalf("noisy estimate out of range: %+v", noisy)
	}
}
