package sound

import (
	"math"
	"sort"

	"github.com/metavert-io/metarush/internal/editspec"
)

// A processed copy of a recording is assembled from the original and the
// processed voices: inside each person's stretch the copy is the original
// mixed with that person's processed voice at their strength (wet/dry); the
// stretches blend into each other over editspec.VoiceCrossfade; everywhere
// else the copy is the original, sample for sample. Each processed voice is
// first lined up with the original (models can add delay) and brought to the
// original's speech level, so the mixer's loudness facts stay true and A/B
// compares the sound, not the volume.

// Piece is one stretch of a copy and how it sounds.
type Piece struct {
	From, To float64 // source seconds
	Speaker  string
	Mode     string  // "" = the original
	Strength float64 // 0–1
	Wet      string  // the processed voice it uses (a key into the wets)
}

// AlignedWet is a processed voice lined up with the original and level-matched.
type AlignedWet struct {
	*Wet
	Lag       int     // samples the processed voice was late (corrected)
	Coherence float64 // how alike the two waveforms are once lined up (1 = the same shape)
	Gain      float64 // applied to match the original's speech level
}

// alignWet lines a processed voice up with the original (mono, same rate) and
// matches its speech level over [coreFrom, coreTo) (source seconds).
func alignWet(dry []float32, dryFrom float64, w *Wet, coreFrom, coreTo float64) *AlignedWet {
	rate := w.Audio.Rate
	x := w.Audio.Data
	// The original over the same stretch the processed voice covers.
	off := int(math.Round((w.From - dryFrom) * float64(rate)))
	ref := make([]float32, len(x))
	for i := range ref {
		if j := off + i; j >= 0 && j < len(dry) {
			ref[i] = dry[j]
		}
	}
	aw := &AlignedWet{Wet: w, Gain: 1}
	// Coarse: speech envelopes in 10 ms steps (±0.3 s), robust to rebuilt voices.
	hop := rate / 100
	er, ew := envelope(ref, hop), envelope(x, hop)
	coarse, _ := bestLagF(er, ew, 30)
	lag := coarse * hop
	// Fine: the waveforms themselves (±10 ms around the coarse lag), when they
	// still look alike (subtractive cleaners; rebuilt voices do not).
	seg := min(len(x), rate*8)
	start := max(0, min(len(x)-seg, findLoud(ref, hop)))
	if seg > rate/2 {
		a := ref[start : start+seg]
		b := shift(x, lag)[start : start+seg]
		fine, corr := bestLag(a, b, rate/100)
		aw.Coherence = math.Round(corr*100) / 100
		if corr > 0.5 {
			lag += fine
		}
	}
	aw.Lag = lag
	if lag != 0 {
		x = shift(x, lag)
	}
	// Level: the original's speech level over the core stretch.
	cf, ct := int(math.Round((coreFrom-w.From)*float64(rate))), int(math.Round((coreTo-w.From)*float64(rate)))
	cf, ct = max(0, cf), min(len(x), ct)
	if ct-cf > rate/4 {
		d, p := activeLevel(ref[cf:ct], ref[cf:ct], rate), activeLevel(ref[cf:ct], x[cf:ct], rate)
		if d > 0 && p > 0 {
			aw.Gain = d / p
		}
		// Never into clipping: keep the loudest moment under −1 dBFS.
		if pk := peakAbs(x[cf:ct]) * aw.Gain; pk > 0.89 {
			aw.Gain *= 0.89 / pk
		}
	}
	out := make([]float32, len(x))
	for i, v := range x {
		out[i] = v * float32(aw.Gain)
	}
	aw.Wet = &Wet{Audio: &Audio{Rate: rate, Channels: 1, Data: out}, From: w.From, Took: w.Took, Device: w.Device, Credits: w.Credits}
	return aw
}

func envelope(x []float32, hop int) []float32 {
	n := len(x) / hop
	out := make([]float32, n)
	for k := 0; k < n; k++ {
		var s float64
		for _, v := range x[k*hop : (k+1)*hop] {
			s += float64(v) * float64(v)
		}
		out[k] = float32(math.Sqrt(s / float64(hop)))
	}
	// Remove the mean so silence does not dominate the correlation.
	var m float64
	for _, v := range out {
		m += float64(v)
	}
	if n > 0 {
		m /= float64(n)
	}
	for i := range out {
		out[i] -= float32(m)
	}
	return out
}

// bestLagF is bestLag for envelopes (any length).
func bestLagF(a, b []float32, maxLag int) (int, float64) {
	if len(a) < 4 || len(b) < 4 {
		return 0, 0
	}
	return bestLag(a, b, maxLag)
}

// findLoud is where the loudest stretch of a signal starts (sample index), to
// pick an excerpt with speech for alignment.
func findLoud(x []float32, hop int) int {
	e := frameEnergies(x, hop, hop)
	best, bestV := 0, -1.0
	win := 300 // 3 s of 10 ms frames
	var s float64
	for k := range e {
		s += e[k]
		if k >= win {
			s -= e[k-win]
		}
		if s > bestV {
			bestV, best = s, max(0, k-win+1)
		}
	}
	return best * hop
}

// Assemble builds a copy of dry (the original over [start, …), any channel
// count) from pieces and their aligned processed voices.
func Assemble(dry *Audio, start float64, pieces []Piece, wets map[string]*AlignedWet) *Audio {
	rate, ch := dry.Rate, dry.Channels
	n := dry.Frames()
	out := &Audio{Rate: rate, Channels: ch, Data: append([]float32(nil), dry.Data...)}
	sort.SliceStable(pieces, func(i, j int) bool { return pieces[i].From < pieces[j].From })
	same := func(a, b Piece) bool { return a.Mode == b.Mode && a.Strength == b.Strength && a.Wet == b.Wet }
	half := editspec.VoiceCrossfade / 2
	for k, p := range pieces {
		w, ok := wets[p.Wet]
		if p.Mode == "" || p.Strength <= 0 || !ok || w == nil {
			continue
		}
		// Ramps only where the neighbour sounds different (a person next to an
		// identical setting has no edge to hide).
		rampIn := k == 0 || !same(pieces[k-1], p) || pieces[k-1].To < p.From-1e-6
		rampOut := k == len(pieces)-1 || !same(pieces[k+1], p) || pieces[k+1].From > p.To+1e-6
		a, b := p.From, p.To
		if rampIn {
			a -= half
		}
		if rampOut {
			b += half
		}
		i0 := max(0, int(math.Floor((a-start)*float64(rate))))
		i1 := min(n, int(math.Ceil((b-start)*float64(rate))))
		wd := w.Audio.Data
		woff := int(math.Round((w.From - start) * float64(rate)))
		for i := i0; i < i1; i++ {
			t := start + float64(i)/float64(rate)
			g := 1.0
			if rampIn && t < p.From+half {
				g = math.Max(0, math.Min(1, (t-(p.From-half))/editspec.VoiceCrossfade))
			}
			if rampOut && t > p.To-half {
				g = math.Min(g, math.Max(0, math.Min(1, ((p.To+half)-t)/editspec.VoiceCrossfade)))
			}
			e := float32(g * p.Strength)
			if e <= 0 {
				continue
			}
			j := i - woff
			if j < 0 || j >= len(wd) {
				continue
			}
			v := wd[j]
			for c := 0; c < ch; c++ {
				d := out.Data[i*ch+c]
				out.Data[i*ch+c] = d + e*(v-dry.Data[i*ch+c])
			}
		}
	}
	return out
}

// downsample3 turns 48 kHz into 16 kHz (the clarity score's rate) with a
// windowed-sinc low-pass at 7.2 kHz.
func downsample3(x []float32) []float32 {
	const taps = 63
	h := make([]float64, taps)
	fc := 7200.0 / 48000
	var sum float64
	for i := range h {
		m := float64(i - taps/2)
		v := 2 * fc
		if m != 0 {
			v = math.Sin(2*math.Pi*fc*m) / (math.Pi * m)
		}
		wv := 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/(taps-1)) + 0.08*math.Cos(4*math.Pi*float64(i)/(taps-1)) // Blackman
		h[i] = v * wv
		sum += h[i]
	}
	for i := range h {
		h[i] /= sum
	}
	out := make([]float32, len(x)/3)
	for k := range out {
		c := 3 * k
		var s float64
		for i := 0; i < taps; i++ {
			j := c + i - taps/2
			if j >= 0 && j < len(x) {
				s += h[i] * float64(x[j])
			}
		}
		out[k] = float32(s)
	}
	return out
}

// gather joins the parts of a mono signal (at rate, starting at start seconds)
// that fall in the given stretches.
func gather(x []float32, rate int, start float64, spans [][2]float64) []float32 {
	var out []float32
	for _, sp := range spans {
		i0 := max(0, int(math.Round((sp[0]-start)*float64(rate))))
		i1 := min(len(x), int(math.Round((sp[1]-start)*float64(rate))))
		if i1 > i0 {
			out = append(out, x[i0:i1]...)
		}
	}
	return out
}
