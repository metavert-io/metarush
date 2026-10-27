package voice

import (
	"math"
	"math/rand"
	"testing"
)

// voiced is a harmonic tone with a little vibrato and noise: a crude vowel at f0.
func voiced(f0, seconds float64, seed int64) []float32 {
	r := rand.New(rand.NewSource(seed))
	n := int(seconds * SampleRate)
	out := make([]float32, n)
	phase := 0.0
	for i := range out {
		t := float64(i) / SampleRate
		f := f0 * (1 + 0.02*math.Sin(2*math.Pi*5*t))
		phase += 2 * math.Pi * f / SampleRate
		v := 0.0
		for h := 1; h <= 6; h++ {
			v += math.Sin(float64(h)*phase) / float64(h)
		}
		out[i] = float32(0.25*v + 0.01*r.NormFloat64())
	}
	return out
}

func TestMeasurePitchFindsTheFundamental(t *testing.T) {
	for _, f0 := range []float64{105, 132, 193, 240} {
		pcm := voiced(f0, 3, int64(f0))
		got := MeasurePitch(pcm, []Span{{0, 3}})
		if math.Abs(got.Hz-f0) > f0*0.04 || got.Voiced < 2 {
			t.Fatalf("f0 %.0f Hz: measured %.1f Hz over %.2f s voiced", f0, got.Hz, got.Voiced)
		}
	}
	// Silence and noise are not voiced.
	quiet := make([]float32, 3*SampleRate)
	r := rand.New(rand.NewSource(1))
	for i := range quiet {
		quiet[i] = float32(0.02 * r.NormFloat64())
	}
	if got := MeasurePitch(quiet, []Span{{0, 3}}); got.Voiced > 0.3 {
		t.Fatalf("noise counted as voiced: %+v", got)
	}
}

func TestPitchDiffers(t *testing.T) {
	owner, guest := Pitch{Hz: 132, Voiced: 8}, Pitch{Hz: 193, Voiced: 6}
	if !PitchDiffers(owner, guest) {
		t.Fatal("132 Hz and 193 Hz voices should differ")
	}
	if PitchDiffers(owner, Pitch{Hz: 140, Voiced: 5}) {
		t.Fatal("the same person a few Hz apart should not differ")
	}
	if PitchDiffers(owner, Pitch{Hz: 210, Voiced: 0.8}) {
		t.Fatal("too little voiced speech to compare")
	}
	c := CombinePitch([]Pitch{{Hz: 130, Voiced: 4}, {Hz: 136, Voiced: 6}, {Hz: 0, Voiced: 0}})
	if c.Hz != 136 || c.Voiced != 10 {
		t.Fatalf("combined pitch %+v", c)
	}
}
