package sound

import "math"

// Noise is how far the background sits below the voice in a stretch of speech
// (a signal-to-noise estimate without a clean reference): the voice level is
// the mean power of the loudest 30% of 20 ms frames, the background the mean
// power of the quietest 15% (the pauses between words), and the voice level
// has the background taken out before the two are compared. It is an
// estimate, good for before/after comparisons of the same recording.
type Noise struct {
	SNR     float64 `json:"snrDb" jsonschema:"description=How far the background sits below the voice (dB; higher = cleaner)."`
	VoiceDB float64 `json:"voiceDb" jsonschema:"description=Voice level (dBFS)."`
	NoiseDB float64 `json:"noiseDb" jsonschema:"description=Background level in the pauses (dBFS)."`
}

// maxSNR caps the estimate: past it the background is inaudible anyway.
const maxSNR = 60

// EstimateNoise measures a mono signal.
func EstimateNoise(x []float32, rate int) (Noise, bool) {
	frame, hop := max(1, rate/50), max(1, rate/100)
	e := frameEnergies(x, frame, hop)
	if len(e) < 20 {
		return Noise{}, false
	}
	loud := percentile(e, 0.70)
	quiet := percentile(e, 0.15)
	var sv, sn float64
	var nv, nn int
	for _, v := range e {
		if v >= loud {
			sv += v
			nv++
		}
		if v <= quiet {
			sn += v
			nn++
		}
	}
	if nv == 0 || nn == 0 {
		return Noise{}, false
	}
	voice, noise := sv/float64(nv), sn/float64(nn)
	clean := math.Max(voice-noise, voice*1e-6)
	snr := maxSNR
	if noise > 1e-14 {
		snr = int(math.Round(math.Min(maxSNR, 10*math.Log10(clean/noise))))
	}
	return Noise{SNR: float64(snr), VoiceDB: math.Round(db(voice)*10) / 10, NoiseDB: math.Round(db(noise)*10) / 10}, true
}
