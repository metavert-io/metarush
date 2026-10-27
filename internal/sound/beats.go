package sound

import (
	"math"
	"sort"
)

// Music beats, for timing music to the video (a track that ends with the
// video ends on a beat near the end). The analysis is the classic one: a
// spectral-flux onset strength envelope, the tempo from its autocorrelation
// (weighted toward moderate tempos), and the beats by dynamic programming
// (Ellis 2007, the method librosa uses). Music without a clear pulse gets its
// strong onsets instead.

// BeatRate is the sample rate the analysis wants (mono).
const BeatRate = 22050

const (
	beatWin = 2048
	beatHop = 512
)

// Beats is what the analysis found.
type Beats struct {
	Tempo      float64   `json:"tempo" jsonschema:"description=Beats per minute (0 when there is no clear pulse)."`
	Confidence float64   `json:"confidence" jsonschema:"description=How clear the pulse is (0–1)."`
	Times      []float64 `json:"times" jsonschema:"description=Beats (or strong onsets when there is no clear pulse)\\, seconds into the file."`
	Onsets     bool      `json:"onsets,omitempty" jsonschema:"description=The times are onsets\\, not beats."`
}

// AnalyzeBeats finds the beats of mono audio at BeatRate.
func AnalyzeBeats(x []float32) Beats {
	env := onsetEnvelope(x)
	if len(env) < 64 {
		return Beats{Times: []float64{}}
	}
	fps := float64(BeatRate) / beatHop
	period, conf := estimateTempo(env, fps)
	out := Beats{Times: []float64{}}
	if period > 0 && conf >= 0.15 {
		out.Confidence = math.Round(conf*100) / 100
		for _, f := range trackBeats(env, period) {
			out.Times = append(out.Times, frameTime(f))
		}
		out.Tempo = math.Round(tempoOf(out.Times, period/fps)*10) / 10
		return out
	}
	out.Onsets = true
	out.Confidence = math.Round(conf*100) / 100
	for _, f := range strongOnsets(env) {
		out.Times = append(out.Times, frameTime(f))
	}
	return out
}

// tempoOf refines the tempo from the tracked beats: the mean beat interval
// near the estimated period (seconds). The autocorrelation's period is whole
// envelope frames (23 ms, about 5% at 120 bpm); the beats between them are not.
func tempoOf(times []float64, period float64) float64 {
	var sum float64
	n := 0
	for i := 1; i < len(times); i++ {
		if d := times[i] - times[i-1]; math.Abs(d-period) < 0.25*period {
			sum += d
			n++
		}
	}
	if n < 4 {
		return 60 / period
	}
	return 60 / (sum / float64(n))
}

// beatLatency is where an onset sits relative to the start of the analysis
// frame whose flux peaks for it: the log-magnitude flux jumps as soon as a
// note's attack enters the Hann window; the frame whose flux peaks starts about
// 0.64 of a window before the attack (measured on clicks at 120 bpm). Frame
// times add it so beats land on the attacks (within half a hop, 12 ms).
const beatLatency = 0.64 * beatWin

// frameTime is the time of an envelope frame's onset (seconds, ms precision).
func frameTime(f int) float64 {
	return math.Round((float64(f*beatHop)+beatLatency)/BeatRate*1000) / 1000
}

// onsetEnvelope is the half-wave rectified spectral flux of log magnitudes,
// with its slow trend removed.
func onsetEnvelope(x []float32) []float64 {
	if len(x) < beatWin {
		return nil
	}
	frames := (len(x)-beatWin)/beatHop + 1
	hann := make([]float64, beatWin)
	for i := range hann {
		hann[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(beatWin))
	}
	bins := beatWin / 2 * 8000 / (BeatRate / 2) // up to 8 kHz
	prev := make([]float64, bins)
	buf := make([]complex128, beatWin)
	env := make([]float64, frames)
	for t := 0; t < frames; t++ {
		off := t * beatHop
		for i := 0; i < beatWin; i++ {
			buf[i] = complex(float64(x[off+i])*hann[i], 0)
		}
		fft(buf, false)
		var flux float64
		for k := 1; k < bins; k++ {
			m := math.Log1p(10 * cabs(buf[k]))
			if d := m - prev[k]; d > 0 && t > 0 {
				flux += d
			}
			prev[k] = m
		}
		env[t] = flux
	}
	// Remove the local mean (about 0.5 s) so loudness changes do not look like onsets.
	w := 21
	out := make([]float64, frames)
	var sum float64
	for t := 0; t < frames; t++ {
		sum += env[t]
		if t >= w {
			sum -= env[t-w]
		}
		mean := sum / float64(min(t+1, w))
		out[t] = math.Max(0, env[t]-mean)
	}
	// Normalize.
	if sd := stddev(out); sd > 0 {
		for i := range out {
			out[i] /= sd
		}
	}
	return out
}

func cabs(c complex128) float64 { return math.Hypot(real(c), imag(c)) }

func stddev(x []float64) float64 {
	if len(x) == 0 {
		return 0
	}
	var m float64
	for _, v := range x {
		m += v
	}
	m /= float64(len(x))
	var s float64
	for _, v := range x {
		s += (v - m) * (v - m)
	}
	return math.Sqrt(s / float64(len(x)))
}

// estimateTempo picks the beat period (frames) from the envelope's
// autocorrelation weighted by a log-normal prior around 120 bpm; conf is the
// normalized autocorrelation at that period.
func estimateTempo(env []float64, fps float64) (float64, float64) {
	minLag := int(math.Floor(60 * fps / 220))
	maxLag := int(math.Ceil(60 * fps / 50))
	if maxLag >= len(env)/2 {
		maxLag = len(env)/2 - 1
	}
	if maxLag <= minLag {
		return 0, 0
	}
	var e0 float64
	for _, v := range env {
		e0 += v * v
	}
	if e0 == 0 {
		return 0, 0
	}
	best, bestScore, bestAC := 0, math.Inf(-1), 0.0
	for lag := minLag; lag <= maxLag; lag++ {
		var ac float64
		for t := lag; t < len(env); t++ {
			ac += env[t] * env[t-lag]
		}
		ac /= e0 * float64(len(env)-lag) / float64(len(env))
		bpm := 60 * fps / float64(lag)
		prior := math.Exp(-0.5 * math.Pow(math.Log2(bpm/120), 2))
		if s := ac * prior; s > bestScore {
			best, bestScore, bestAC = lag, s, ac
		}
	}
	if best == 0 {
		return 0, 0
	}
	// Refine to a fractional period by a parabola through the neighbours.
	period := float64(best)
	return period, math.Max(0, math.Min(1, bestAC))
}

// trackBeats places beats by dynamic programming: each beat rewards a strong
// onset and is penalized for straying from the period.
func trackBeats(env []float64, period float64) []int {
	n := len(env)
	score := make([]float64, n)
	back := make([]int, n)
	const tightness = 100
	for t := 0; t < n; t++ {
		lo := t - int(math.Round(2*period))
		hi := t - int(math.Round(period/2))
		best, bestP := 0.0, -1
		for p := max(0, lo); p <= hi && p < t; p++ {
			d := math.Log(float64(t-p) / period)
			v := score[p] - tightness*d*d
			if bestP < 0 || v > best {
				best, bestP = v, p
			}
		}
		if bestP >= 0 && best > 0 {
			score[t] = env[t] + best
			back[t] = bestP
		} else {
			score[t] = env[t]
			back[t] = -1
		}
	}
	// End on the best-scoring frame within the last period.
	end, endV := n-1, math.Inf(-1)
	for t := max(0, n-int(math.Ceil(period))); t < n; t++ {
		if score[t] > endV {
			end, endV = t, score[t]
		}
	}
	var beats []int
	for t := end; t >= 0; t = back[t] {
		beats = append(beats, t)
		if back[t] < 0 {
			break
		}
	}
	sort.Ints(beats)
	return beats
}

// strongOnsets are the envelope's peaks well above its typical level.
func strongOnsets(env []float64) []int {
	var out []int
	thr := 1.5 // in standard deviations (the envelope is normalized)
	last := -1000
	for t := 1; t+1 < len(env); t++ {
		if env[t] >= thr && env[t] >= env[t-1] && env[t] > env[t+1] && t-last >= 4 {
			out = append(out, t)
			last = t
		}
	}
	return out
}
