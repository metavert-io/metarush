package fixes

import (
	"context"
	"math"
	"math/cmplx"
	"sort"

	"github.com/metavert-io/metarush/internal/tools"
)

// Sound measurements (D153), from the level every tenth of a second (mono,
// 16 kHz) and, for hum, the spectrum.

// levelStep is the step of the level series.
const levelStep = 0.1

// Span is a stretch of a recording, with who speaks in it ("" unknown).
type Span struct {
	From, To float64
	Speaker  string
}

// SoundFacts is what the level series says about a recording.
type SoundFacts struct {
	// Background is the level of the quietest tenth (the room, the hiss);
	// Speech the level of the speech (or the loudest tenth without a
	// transcript); Clean their difference in dB (25 dB or more: clean).
	Background, Speech, Clean float64
	// Speakers are each person's level (dB) over their stretches (at least
	// 5 s of speech); Spread the difference between the loudest and the
	// quietest person.
	Speakers map[string]float64
	Spread   float64
	Loudest  string
	Quietest string
	// Range is how far the loudness moves (3-second levels, the 10th to the
	// 95th percentile, dB); Level the average level (dB below full scale).
	Range, Level float64
	// Start and End are where the sound starts and stops (seconds), Duration
	// the length of the series.
	Start, End, Duration float64
}

// silentDB is quieter than anything heard: the silence at a recording's ends.
const silentDB = -50.0

// Sound measures a recording's levels. spans are the transcript's stretches
// (nil: none yet).
func Sound(ctx context.Context, ts *tools.Set, src string, spans []Span) (*SoundFacts, error) {
	lv, err := audioLevels(ctx, ts, src, levelStep)
	if err != nil {
		return nil, err
	}
	return soundFacts(lv, spans), nil
}

func soundFacts(lv []float64, spans []Span) *SoundFacts {
	f := &SoundFacts{Speakers: map[string]float64{}, Duration: float64(len(lv)) * levelStep}
	if len(lv) == 0 {
		return f
	}
	var heard []float64
	for _, v := range lv {
		if v > -100 {
			heard = append(heard, v)
		}
	}
	sorted := append([]float64{}, heard...)
	sort.Float64s(sorted)
	pct := func(xs []float64, p float64) float64 {
		if len(xs) == 0 {
			return -120
		}
		return xs[min(len(xs)-1, int(p*float64(len(xs))))]
	}
	f.Background = pct(sorted, 0.10)
	// Speech: the power average over the transcript's stretches.
	inSpeech := make([]bool, len(lv))
	per := map[string][]float64{}
	for _, s := range spans {
		for i := int(s.From / levelStep); i < int(math.Ceil(s.To/levelStep)) && i < len(lv); i++ {
			if i < 0 {
				continue
			}
			inSpeech[i] = true
			if s.Speaker != "" {
				per[s.Speaker] = append(per[s.Speaker], lv[i])
			}
		}
	}
	var sp []float64
	for i, v := range lv {
		if inSpeech[i] {
			sp = append(sp, v)
		}
	}
	if len(sp) > 0 {
		f.Speech = powerMean(sp)
	} else {
		f.Speech = pct(sorted, 0.90)
	}
	f.Clean = f.Speech - f.Background
	first := true
	for who, vs := range per {
		if float64(len(vs))*levelStep < 5 {
			continue
		}
		l := powerMean(vs)
		f.Speakers[who] = l
		if first || l > f.Speakers[f.Loudest] {
			f.Loudest = who
		}
		if first || l < f.Speakers[f.Quietest] {
			f.Quietest = who
		}
		first = false
	}
	if len(f.Speakers) > 1 {
		f.Spread = f.Speakers[f.Loudest] - f.Speakers[f.Quietest]
	}
	// Loudness over 3-second stretches, ignoring the quiet gaps.
	win := int(3 / levelStep)
	var st []float64
	for i := 0; i+win <= len(lv); i += win / 3 {
		st = append(st, powerMean(lv[i:i+win]))
	}
	f.Level = powerMean(heard)
	var loud []float64
	for _, v := range st {
		if v > f.Level-20 {
			loud = append(loud, v)
		}
	}
	sort.Float64s(loud)
	if len(loud) > 2 {
		f.Range = pct(loud, 0.95) - pct(loud, 0.10)
	}
	// The silent ends.
	i := 0
	for i < len(lv) && lv[i] < silentDB {
		i++
	}
	j := len(lv)
	for j > i && lv[j-1] < silentDB {
		j--
	}
	f.Start, f.End = float64(i)*levelStep, float64(j)*levelStep
	return f
}

// powerMean averages dB levels as power.
func powerMean(xs []float64) float64 {
	if len(xs) == 0 {
		return -120
	}
	s := 0.0
	for _, v := range xs {
		s += math.Pow(10, v/10)
	}
	return toDB(s / float64(len(xs)))
}

// Hum is how far a mains hum (50 or 60 Hz and its harmonics) stands out of
// the sound around it (dB), and at which frequency. 15 dB or more is a hum
// people hear.
func Hum(ctx context.Context, ts *tools.Set, src string) (float64, int, error) {
	x, err := audioSamples(ctx, ts, src, 4000, 0, 180)
	if err != nil {
		return 0, 0, err
	}
	db, f0 := humOf(x, 4000)
	return db, f0, nil
}

// humOf scores 50 and 60 Hz in samples at rate.
func humOf(x []float32, rate int) (float64, int) {
	const n = 4096
	if len(x) < n {
		return 0, 0
	}
	psd := make([]float64, n/2)
	win := make([]float64, n)
	for i := range win {
		win[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n-1))
	}
	buf := make([]complex128, n)
	frames := 0
	for off := 0; off+n <= len(x); off += n / 2 {
		for i := 0; i < n; i++ {
			buf[i] = complex(float64(x[off+i])*win[i], 0)
		}
		fft(buf, false)
		for k := range psd {
			psd[k] += real(buf[k] * cmplx.Conj(buf[k]))
		}
		frames++
	}
	if frames == 0 {
		return 0, 0
	}
	hz := float64(rate) / n
	bin := func(f float64) int { return int(math.Round(f / hz)) }
	score := func(f0 float64) float64 {
		var rs []float64
		for k := 1; k <= 6; k++ {
			c := bin(f0 * float64(k))
			if c+15 >= len(psd) {
				break
			}
			peak := 0.0
			for b := c - 1; b <= c+1; b++ {
				peak = math.Max(peak, psd[b])
			}
			var around []float64
			for b := c - 12; b <= c+12; b++ {
				if b > 0 && (b < c-3 || b > c+3) {
					around = append(around, psd[b])
				}
			}
			sort.Float64s(around)
			floor := around[len(around)/2]
			if floor <= 0 {
				floor = 1e-12
			}
			rs = append(rs, 10*math.Log10(peak/floor))
		}
		sort.Sort(sort.Reverse(sort.Float64Slice(rs)))
		if len(rs) > 3 {
			rs = rs[:3]
		}
		s := 0.0
		for _, r := range rs {
			s += r
		}
		if len(rs) == 0 {
			return 0
		}
		return s / float64(len(rs))
	}
	s50, s60 := score(50), score(60)
	if s60 >= s50 {
		return s60, 60
	}
	return s50, 50
}
