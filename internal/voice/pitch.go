package voice

import (
	"math"
	"sort"
)

// Pitch is a voice's typical pitch: the median fundamental frequency of its
// voiced speech and how many seconds were voiced. It is a cheap sanity check
// next to the fingerprint (two voices an octave apart are not one person),
// never a decision on its own.
type Pitch struct {
	Hz     float64 `json:"hz"`
	Voiced float64 `json:"voiced"`
}

// Pitch analysis settings: 8 kHz analysis (the 16 kHz audio halved), 40 ms
// frames every 20 ms, 60–400 Hz.
const (
	pitchRate     = SampleRate / 2
	pitchFrame    = pitchRate * 40 / 1000
	pitchHop      = pitchRate * 20 / 1000
	pitchMinHz    = 60.0
	pitchMaxHz    = 400.0
	pitchVoicedR  = 0.45 // normalized autocorrelation a frame needs to count as voiced
	pitchMaxSpeak = 60.0 // seconds of speech analyzed per voice at most
)

// MinVoiced is how much voiced speech each side needs before pitches are compared.
const MinVoiced = 1.5

// MeasurePitch estimates the median pitch of the speech in spans of 16 kHz
// mono pcm with a plain autocorrelation pitch tracker (energy gate,
// normalized autocorrelation, the shortest strong period to avoid octave
// errors, parabolic refinement).
func MeasurePitch(pcm []float32, spans []Span) Pitch {
	var x []float32
	total := 0.0
	for _, s := range spans {
		if total >= pitchMaxSpeak {
			break
		}
		seg := slice(pcm, s)
		if len(seg) < 2*pitchFrame {
			continue
		}
		if room := pitchMaxSpeak - total; s.Len() > room {
			seg = seg[:int(room*SampleRate)]
		}
		total += float64(len(seg)) / SampleRate
		// Halve the rate (pairwise mean is a gentle low-pass) and keep span
		// boundaries apart with a short gap of silence.
		for i := 0; i+1 < len(seg); i += 2 {
			x = append(x, (seg[i]+seg[i+1])/2)
		}
		x = append(x, make([]float32, pitchFrame)...)
	}
	if len(x) < pitchFrame*2 {
		return Pitch{}
	}
	nFrames := (len(x) - pitchFrame) / pitchHop
	rms := make([]float64, nFrames)
	for f := 0; f < nFrames; f++ {
		s := 0.0
		for _, v := range x[f*pitchHop : f*pitchHop+pitchFrame] {
			s += float64(v) * float64(v)
		}
		rms[f] = math.Sqrt(s / pitchFrame)
	}
	sorted := append([]float64(nil), rms...)
	sort.Float64s(sorted)
	gate := math.Max(0.002, 0.25*sorted[int(0.95*float64(len(sorted)-1))])
	minLag, maxLag := int(math.Floor(pitchRate/pitchMaxHz)), int(math.Floor(pitchRate/pitchMinHz))
	var f0s []float64
	r := make([]float64, maxLag+2)
	for f := 0; f < nFrames; f++ {
		if rms[f] < gate {
			continue
		}
		start := f * pitchHop
		fr := x[start : start+pitchFrame]
		mean := 0.0
		for _, v := range fr {
			mean += float64(v)
		}
		mean /= pitchFrame
		best := -1.0
		for lag := minLag; lag <= maxLag+1 && lag < pitchFrame; lag++ {
			var num, e0, e1 float64
			for i := 0; i+lag < pitchFrame; i++ {
				a, b := float64(fr[i])-mean, float64(fr[i+lag])-mean
				num += a * b
				e0 += a * a
				e1 += b * b
			}
			if e0 <= 0 || e1 <= 0 {
				r[lag] = 0
				continue
			}
			r[lag] = num / math.Sqrt(e0*e1)
			if lag <= maxLag && r[lag] > best {
				best = r[lag]
			}
		}
		if best < pitchVoicedR {
			continue
		}
		// The shortest period that is nearly as strong as the best one (a
		// period doubled or tripled correlates too: that would halve the pitch).
		pick := -1
		for lag := minLag + 1; lag < maxLag && lag+1 < pitchFrame; lag++ {
			if r[lag] >= 0.9*best && r[lag] >= r[lag-1] && r[lag] >= r[lag+1] {
				pick = lag
				break
			}
		}
		if pick < 0 {
			continue
		}
		t := float64(pick)
		if a, b, c := r[pick-1], r[pick], r[pick+1]; a-2*b+c != 0 {
			t += 0.5 * (a - c) / (a - 2*b + c)
		}
		if hz := pitchRate / t; hz >= pitchMinHz && hz <= pitchMaxHz {
			f0s = append(f0s, hz)
		}
	}
	if len(f0s) == 0 {
		return Pitch{}
	}
	sort.Float64s(f0s)
	return Pitch{Hz: round3(f0s[len(f0s)/2]), Voiced: round3(float64(len(f0s)) * pitchHop / pitchRate)}
}

// PitchDiffers reports whether two voices clearly differ in pitch: both have
// at least MinVoiced seconds of voiced speech and their medians are 60 Hz or
// 0.4 octave apart. (Male and female voices are typically ~100–140 Hz vs.
// ~180–230 Hz; one person's median moves a few Hz between recordings.)
func PitchDiffers(a, b Pitch) bool {
	if a.Hz <= 0 || b.Hz <= 0 || a.Voiced < MinVoiced || b.Voiced < MinVoiced {
		return false
	}
	return math.Abs(a.Hz-b.Hz) >= 60 || math.Abs(math.Log2(a.Hz/b.Hz)) >= 0.4
}

// CombinePitch is the pitch of several voices of one person: the voiced-time
// weighted median.
func CombinePitch(ps []Pitch) Pitch {
	var ok []Pitch
	total := 0.0
	for _, p := range ps {
		if p.Hz > 0 && p.Voiced > 0 {
			ok = append(ok, p)
			total += p.Voiced
		}
	}
	if len(ok) == 0 {
		return Pitch{}
	}
	sort.Slice(ok, func(i, j int) bool { return ok[i].Hz < ok[j].Hz })
	acc := 0.0
	for _, p := range ok {
		acc += p.Voiced
		if acc >= total/2 {
			return Pitch{Hz: p.Hz, Voiced: round3(total)}
		}
	}
	return Pitch{Hz: ok[len(ok)-1].Hz, Voiced: round3(total)}
}
