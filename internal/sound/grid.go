package sound

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"

	"github.com/metavert-io/metarush/internal/tools"
)

// The music grid (evidence beats-2, D120): the beats of a whole track at its
// own, changing tempo, where its bars start (downbeats) and where its phrases
// start, so music timed to the video can end where the music itself ends a
// phrase. AnalyzeBeats (beats-1) assumed one tempo and read the first quarter
// hour; cuts made with it keep using what it found.
//
// One pass over the track at BeatRate, streamed from ffmpeg (a track of any
// length is never held in memory), gives features every 512 samples: the
// onset strength (beats-1's spectral flux), the onsets of the low end (kick,
// bass) and of the top (cymbals, fills), a pitch-class profile and the energy.
// Then:
//   - tempo over time: the onset strength's autocorrelation in 8-second
//     windows every second, scored at the beat and its double with beats-1's
//     prior around 120 bpm, and the best path through the windows (small
//     changes are cheap, a jump costs a fixed amount);
//   - beats: beats-1's dynamic programming, with the period of each moment;
//   - bars: at each beat, how hard the low end hits and how much the harmony
//     changes; the meter (3 or 4 beats) and where each bar starts, by the best
//     path through the positions in a bar, which may slip at a cost (a pickup,
//     a short bar);
//   - phrases: at each bar line, how much the harmony and the energy change
//     around it and how loud the top hits there; groups of 4 (or 8) bars, by
//     the same kind of path.
// Measured on generated music with known tempo changes and bars (grid_test.go,
// docs/sound.md).

// GridVersion is the analysis version (stored as the insight's model).
const GridVersion = "beats-2"

// TempoSpan is a stretch of a track at one tempo, or changing smoothly from
// one to another.
type TempoSpan struct {
	From  float64 `json:"from" jsonschema:"description=Seconds into the file."`
	To    float64 `json:"to"`
	Start float64 `json:"start" jsonschema:"description=Beats per minute where it starts."`
	End   float64 `json:"end" jsonschema:"description=Beats per minute where it ends (the same when steady)."`
}

// Grid is what the analysis found in a music file.
type Grid struct {
	Tempo      float64     `json:"tempo" jsonschema:"description=The typical tempo (beats per minute; 0 when there is no clear pulse)."`
	Tempos     []TempoSpan `json:"tempos,omitempty" jsonschema:"description=The tempo over the track: steady stretches and smooth changes."`
	Confidence float64     `json:"confidence" jsonschema:"description=How clear the pulse is (0–1)."`
	Beats      []float64   `json:"beats" jsonschema:"description=Beats (or strong onsets when there is no clear pulse)\\, seconds into the file."`
	Onsets     bool        `json:"onsets,omitempty" jsonschema:"description=The beats are onsets: there is no clear pulse."`
	Meter      int         `json:"meter,omitempty" jsonschema:"description=Beats in a bar (3 or 4; 0 when the bars are not clear)."`
	Downbeats  []float64   `json:"downbeats,omitempty" jsonschema:"description=Where bars start (seconds)."`
	Bars       float64     `json:"bars,omitempty" jsonschema:"description=How clear the bars are (0–1)."`
	PhraseBars int         `json:"phraseBars,omitempty" jsonschema:"description=Bars in a phrase (4 or 8)."`
	Phrases    []float64   `json:"phrases,omitempty" jsonschema:"description=Where phrases start (seconds): bar lines where the music begins its next group of bars."`
	PhraseConf float64     `json:"phraseConfidence,omitempty" jsonschema:"description=How clear the phrases are (0–1)."`
	Seconds    float64     `json:"seconds" jsonschema:"description=How much of the track was analysed (all of it)."`
}

// gridFrames are the per-frame features.
type gridFrames struct {
	flux, low, high, energy []float32
	bands                   [][gridBands]float32 // power in six bands (texture)
	chroma                  [][12]float32
}

// gridBands are the texture bands' upper edges (Hz); the last runs to the top.
const gridBands = 6

var gridBandTops = [gridBands - 1]float64{150, 400, 1000, 2500, 6000}

// gridAnalyzer computes the features from a stream of mono float32 samples
// at BeatRate (an io.Writer of little-endian float32s, as ffmpeg's f32le).
type gridAnalyzer struct {
	buf     []float32
	pending []byte
	hann    []float64
	prev    []float64
	fbuf    []complex128
	pcOf    []int8
	bandOf  []int8
	f       gridFrames
	samples int
}

const (
	gridBins8k  = beatWin / 2 * 8000 / (BeatRate / 2) // beats-1's onset band (up to 8 kHz)
	gridLowTop  = beatWin * 180 / BeatRate            // the low end: up to 180 Hz
	gridHighBot = beatWin * 5000 / BeatRate           // the top: from 5 kHz
)

func newGridAnalyzer() *gridAnalyzer {
	g := &gridAnalyzer{hann: make([]float64, beatWin), prev: make([]float64, beatWin/2), fbuf: make([]complex128, beatWin), pcOf: make([]int8, beatWin/2), bandOf: make([]int8, beatWin/2)}
	for i := range g.hann {
		g.hann[i] = 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(beatWin))
	}
	for k := range g.pcOf {
		g.pcOf[k] = -1
		f := float64(k) * BeatRate / beatWin
		if f >= 55 && f <= 4000 {
			g.pcOf[k] = int8((int(math.Round(12*math.Log2(f/440)))%12 + 12) % 12)
		}
		b := 0
		for b < len(gridBandTops) && f >= gridBandTops[b] {
			b++
		}
		g.bandOf[k] = int8(b)
	}
	return g
}

func (g *gridAnalyzer) Write(p []byte) (int, error) {
	n := len(p)
	if len(g.pending) > 0 {
		p = append(g.pending, p...)
		g.pending = nil
	}
	whole := len(p) / 4 * 4
	for i := 0; i < whole; i += 4 {
		g.buf = append(g.buf, math.Float32frombits(binary.LittleEndian.Uint32(p[i:])))
	}
	if whole < len(p) {
		g.pending = append([]byte(nil), p[whole:]...)
	}
	g.samples += whole / 4
	g.drain()
	return n, nil
}

// push feeds samples directly (tests).
func (g *gridAnalyzer) push(x []float32) {
	g.buf = append(g.buf, x...)
	g.samples += len(x)
	g.drain()
}

func (g *gridAnalyzer) drain() {
	off := 0
	for len(g.buf)-off >= beatWin {
		g.frame(g.buf[off : off+beatWin])
		off += beatHop
	}
	if off > 0 {
		g.buf = append(g.buf[:0], g.buf[off:]...)
	}
}

func (g *gridAnalyzer) frame(x []float32) {
	for i := 0; i < beatWin; i++ {
		g.fbuf[i] = complex(float64(x[i])*g.hann[i], 0)
	}
	fft(g.fbuf, false)
	first := len(g.f.flux) == 0
	var flux, low, high, energy float64
	var chroma [12]float64
	var bands [gridBands]float32
	for k := 1; k < beatWin/2; k++ {
		mag := cabs(g.fbuf[k])
		m := math.Log1p(10 * mag)
		if d := m - g.prev[k]; d > 0 && !first {
			if k < gridBins8k {
				flux += d
			}
			if k <= gridLowTop {
				low += d
			}
			if k >= gridHighBot {
				high += d
			}
		}
		g.prev[k] = m
		p := mag * mag
		energy += p
		bands[g.bandOf[k]] += float32(p)
		if pc := g.pcOf[k]; pc >= 0 {
			chroma[pc] += p
		}
	}
	g.f.bands = append(g.f.bands, bands)
	var c [12]float32
	var norm float64
	for _, v := range chroma {
		norm += v * v
	}
	if norm > 0 {
		norm = math.Sqrt(norm)
		for i, v := range chroma {
			c[i] = float32(v / norm)
		}
	}
	g.f.flux = append(g.f.flux, float32(flux))
	g.f.low = append(g.f.low, float32(low))
	g.f.high = append(g.f.high, float32(high))
	g.f.energy = append(g.f.energy, float32(energy))
	g.f.chroma = append(g.f.chroma, c)
}

// AnalyzeGrid finds the grid of mono audio at BeatRate held in memory (tests,
// short files); Engine.MusicGrid streams a file through the same analysis.
func AnalyzeGrid(x []float32) Grid {
	g := newGridAnalyzer()
	g.push(x)
	return g.grid()
}

// MusicGrid analyses a whole music file, streamed from ffmpeg.
func (e *Engine) MusicGrid(ctx context.Context, src string) (Grid, error) {
	g := newGridAnalyzer()
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", src, "-vn", "-map", "0:a:0", "-ac", "1", "-ar", strconv.Itoa(BeatRate), "-f", "f32le", "-"}
	if _, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args, Stdout: g, Background: true}); err != nil {
		return Grid{}, fmt.Errorf("could not read the music: %w", err)
	}
	if g.samples == 0 {
		return Grid{}, errors.New("the music has no sound")
	}
	return g.grid(), nil
}

// grid runs the analysis on the features.
func (g *gridAnalyzer) grid() Grid {
	out := Grid{Beats: []float64{}, Seconds: math.Round(float64(g.samples)/BeatRate*1000) / 1000}
	env := smoothEnvelope(g.f.flux)
	if len(env) < 64 {
		return out
	}
	fps := float64(BeatRate) / beatHop
	period, conf := tempoCurve(env, fps)
	out.Confidence = math.Round(conf*100) / 100
	if period == nil || conf < 0.15 {
		out.Onsets = true
		for _, f := range strongOnsets(env) {
			out.Beats = append(out.Beats, frameTime(f))
		}
		return out
	}
	beats := trackBeatsVar(env, period)
	for _, f := range beats {
		out.Beats = append(out.Beats, refinedTime(env, f))
	}
	out.Tempo, out.Tempos = tempoSummary(out.Beats)
	meter, downs, barConf := findBars(beats, &g.f)
	if meter == 0 {
		return out
	}
	out.Meter, out.Bars = meter, math.Round(barConf*100)/100
	for _, i := range downs {
		out.Downbeats = append(out.Downbeats, frameTime(beats[i]))
	}
	q, starts, phraseConf := findPhrases(beats, downs, &g.f)
	if q == 0 {
		return out
	}
	out.PhraseBars, out.PhraseConf = q, math.Round(phraseConf*100)/100
	for _, b := range starts {
		out.Phrases = append(out.Phrases, frameTime(beats[downs[b]]))
	}
	return out
}

// refinedTime is a beat's time between envelope frames: the onset peak next
// to the beat's frame, placed by a parabola through it and its neighbours
// (frames are 23 ms apart; beats-1 kept whole frames), with beats-1's latency.
func refinedTime(env []float64, f int) float64 {
	m := f
	for _, c := range []int{f - 1, f + 1} {
		if c >= 0 && c < len(env) && env[c] > env[m] {
			m = c
		}
	}
	d := 0.0
	if m > 0 && m+1 < len(env) {
		a, b, c := env[m-1], env[m], env[m+1]
		if den := a - 2*b + c; den < 0 {
			d = math.Max(-0.5, math.Min(0.5, 0.5*(a-c)/den))
		}
	}
	return math.Round(((float64(m)+d)*beatHop+beatLatency)/BeatRate*1000) / 1000
}

// smoothEnvelope is beats-1's onset strength: the flux with its local mean
// (about 0.5 s) removed, normalized.
func smoothEnvelope(flux []float32) []float64 {
	const w = 21
	out := make([]float64, len(flux))
	var sum float64
	for t := range flux {
		sum += float64(flux[t])
		if t >= w {
			sum -= float64(flux[t-w])
		}
		mean := sum / float64(min(t+1, w))
		out[t] = math.Max(0, float64(flux[t])-mean)
	}
	if sd := stddev(out); sd > 0 {
		for i := range out {
			out[i] /= sd
		}
	}
	return out
}

// Tempo states: 50–220 bpm in 1% steps.
const (
	gridMinBPM  = 50.0
	gridMaxBPM  = 220.0
	gridStep    = 1.01
	gridWinSec  = 8.0
	gridHopSec  = 1.0
	gridSlip    = 0.02 // the cost of each 1% step between windows
	gridMaxSlip = 6    // steps a tempo may glide per window before it counts as a jump
	gridJump    = 0.5  // the cost of a jump (an abrupt tempo change)
)

// tempoState is a tempo state's bpm.
func tempoState(j int) float64 { return gridMinBPM * math.Pow(gridStep, float64(j)) }

func tempoStates() int {
	return int(math.Floor(math.Log(gridMaxBPM/gridMinBPM)/math.Log(gridStep))) + 1
}

// tempoCurve is the beat period (frames) at every frame and how clear the
// pulse is.
func tempoCurve(env []float64, fps float64) ([]float64, float64) {
	win := int(math.Round(gridWinSec * fps))
	hop := int(math.Round(gridHopSec * fps))
	maxLag := int(math.Ceil(60 * fps / gridMinBPM))
	if len(env) < win {
		win = len(env)
	}
	if win <= 2*maxLag+2 {
		// Too short for windows: one window over everything, as beats-1.
		p, c := estimateTempo(env, fps)
		if p <= 0 {
			return nil, c
		}
		per := make([]float64, len(env))
		for i := range per {
			per[i] = p
		}
		return per, c
	}
	var starts []int
	for s := 0; s+win <= len(env); s += hop {
		starts = append(starts, s)
	}
	if last := len(env) - win; starts[len(starts)-1] != last {
		starts = append(starts, last)
	}
	J := tempoStates()
	emit := make([][]float64, len(starts))
	acf := make([]float64, 2*maxLag+2)
	var clear float64
	for w, s := range starts {
		x := env[s : s+win]
		var e0 float64
		for _, v := range x {
			e0 += v * v
		}
		for lag := range acf {
			if lag >= len(x) || e0 == 0 {
				acf[lag] = 0
				continue
			}
			var c float64
			for t := lag; t < len(x); t++ {
				c += x[t] * x[t-lag]
			}
			acf[lag] = c / (e0 * float64(len(x)-lag) / float64(len(x)))
		}
		at := func(lag float64) float64 {
			i := int(lag)
			if i < 0 || i+1 >= len(acf) {
				return 0
			}
			f := lag - float64(i)
			return acf[i]*(1-f) + acf[i+1]*f
		}
		emit[w] = make([]float64, J)
		best := 0.0
		for j := 0; j < J; j++ {
			bpm := tempoState(j)
			lag := 60 * fps / bpm
			prior := math.Exp(-0.5 * math.Pow(math.Log2(bpm/120), 2))
			v := (math.Max(0, at(lag)) + 0.5*math.Max(0, at(2*lag))) * prior
			emit[w][j] = v
			best = math.Max(best, math.Max(0, at(lag)))
		}
		clear += best
	}
	clear /= float64(len(starts))
	// The best path through the windows (Viterbi).
	score := append([]float64(nil), emit[0]...)
	back := make([][]int, len(starts))
	next := make([]float64, J)
	for w := 1; w < len(starts); w++ {
		back[w] = make([]int, J)
		for j := 0; j < J; j++ {
			bi, bv := 0, math.Inf(-1)
			for i := 0; i < J; i++ {
				d := absInt(i - j)
				c := gridSlip * float64(d)
				if d > gridMaxSlip {
					c = gridJump + gridSlip*gridMaxSlip
				}
				if v := score[i] - c; v > bv {
					bi, bv = i, v
				}
			}
			next[j] = bv + emit[w][j]
			back[w][j] = bi
		}
		copy(score, next)
	}
	j := 0
	for i := range score {
		if score[i] > score[j] {
			j = i
		}
	}
	path := make([]int, len(starts))
	for w := len(starts) - 1; w >= 0; w-- {
		path[w] = j
		if w > 0 {
			j = back[w][j]
		}
	}
	// The period at every frame: between the windows' middles, linear in tempo.
	per := make([]float64, len(env))
	mid := func(w int) float64 { return float64(starts[w]) + float64(win)/2 }
	w := 0
	for t := range per {
		for w+1 < len(starts) && mid(w+1) <= float64(t) {
			w++
		}
		bpm := tempoState(path[w])
		if w+1 < len(starts) && float64(t) > mid(w) {
			a, b := tempoState(path[w]), tempoState(path[w+1])
			if absInt(path[w+1]-path[w]) <= gridMaxSlip { // a glide: follow it; a jump happens at the next window's middle
				f := (float64(t) - mid(w)) / (mid(w+1) - mid(w))
				bpm = a + (b-a)*f
			}
		}
		per[t] = 60 * fps / bpm
	}
	return per, math.Max(0, math.Min(1, clear))
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// trackBeatsVar places beats by beats-1's dynamic programming with the
// period of each moment: each beat rewards a strong onset and is penalized
// for straying from the period there.
func trackBeatsVar(env []float64, period []float64) []int {
	n := len(env)
	score := make([]float64, n)
	back := make([]int, n)
	const tightness = 100
	for t := 0; t < n; t++ {
		p := period[t]
		lo := t - int(math.Round(2*p))
		hi := t - int(math.Round(p/2))
		best, bestP := 0.0, -1
		for q := max(0, lo); q <= hi && q < t; q++ {
			d := math.Log(float64(t-q) / p)
			v := score[q] - tightness*d*d
			if bestP < 0 || v > best {
				best, bestP = v, q
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
	end, endV := n-1, math.Inf(-1)
	for t := max(0, n-int(math.Ceil(period[n-1]))); t < n; t++ {
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

// tempoSummary is the typical tempo (the median beat interval) and the tempo
// spans, from the tracked beats: the local tempo at each beat (the median
// interval over nine beats), cut where it changes by more than 5% over eight
// beats (a jump; a glide changes far more slowly), each span steady when its
// ends are within 3% of each other.
func tempoSummary(beats []float64) (float64, []TempoSpan) {
	n := len(beats)
	if n < 2 {
		return 0, nil
	}
	var ibis []float64
	for i := 1; i < n; i++ {
		ibis = append(ibis, beats[i]-beats[i-1])
	}
	typical := 60 / median(ibis)
	local := make([]float64, n) // bpm at each beat
	for i := range local {
		var w []float64
		for j := max(1, i-4); j <= min(n-1, i+4); j++ {
			w = append(w, beats[j]-beats[j-1])
		}
		local[i] = 60 / median(w)
	}
	change := func(i int) float64 {
		a, b := max(0, i-4), min(n-1, i+4)
		return math.Abs(local[b]-local[a]) / local[i]
	}
	cuts := []int{0}
	for i := 1; i < n-1; i++ {
		c := change(i)
		if c > 0.05 && c >= change(i-1) && c > change(i+1) && i-cuts[len(cuts)-1] >= 8 {
			cuts = append(cuts, i)
		}
	}
	at := func(from, to int) float64 { // the tempo over beats [from, to)
		var d []float64
		for i := max(1, from+1); i < to; i++ {
			d = append(d, beats[i]-beats[i-1])
		}
		if len(d) == 0 {
			return 0
		}
		return 60 / median(d)
	}
	var spans []TempoSpan
	for k, from := range cuts {
		to := n
		if k+1 < len(cuts) {
			to = cuts[k+1]
		}
		s, e := at(from, min(to, from+9)), at(max(from, to-9), to)
		if s == 0 || e == 0 {
			continue
		}
		if math.Abs(e-s)/s < 0.03 {
			s = at(from, to)
			e = s
		}
		end := beats[min(to, n-1)]
		spans = append(spans, TempoSpan{From: round3(beats[from]), To: round3(end), Start: math.Round(s*10) / 10, End: math.Round(e*10) / 10})
	}
	if len(spans) > 0 {
		spans[0].From = 0
	}
	return math.Round(typical*10) / 10, spans
}

func median(xs []float64) float64 {
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func medianIn(beats []float64, from, to float64) float64 {
	var d []float64
	for i := 1; i < len(beats); i++ {
		if beats[i] >= from && beats[i] < to {
			d = append(d, beats[i]-beats[i-1])
		}
	}
	return median(d)
}

// zscores normalizes values by their median and spread (MAD), so one loud
// moment doesn't swamp the rest.
func zscores(x []float64) []float64 {
	out := make([]float64, len(x))
	if len(x) == 0 {
		return out
	}
	m := median(x)
	dev := make([]float64, len(x))
	for i, v := range x {
		dev[i] = math.Abs(v - m)
	}
	mad := median(dev) * 1.4826
	if mad < 1e-9 {
		mad = stddev(x)
	}
	if mad < 1e-9 {
		return out
	}
	for i, v := range x {
		out[i] = math.Max(-4, math.Min(4, (v-m)/mad))
	}
	return out
}

func meanChroma(f *gridFrames, from, to int) [12]float64 {
	var c [12]float64
	from, to = max(0, from), min(len(f.chroma), to)
	for t := from; t < to; t++ {
		for i, v := range f.chroma[t] {
			c[i] += float64(v)
		}
	}
	return c
}

// meanBands is the mean log power of each texture band over frames [from, to).
func meanBands(f *gridFrames, from, to int) [gridBands]float64 {
	var out [gridBands]float64
	from, to = max(0, from), min(len(f.bands), to)
	if to <= from {
		for i := range out {
			out[i] = math.Log(1e-9)
		}
		return out
	}
	for t := from; t < to; t++ {
		for i, v := range f.bands[t] {
			out[i] += math.Log(1e-9 + float64(v))
		}
	}
	for i := range out {
		out[i] /= float64(to - from)
	}
	return out
}

func bandDistance(a, b [gridBands]float64) float64 {
	var s float64
	for i := range a {
		s += (a[i] - b[i]) * (a[i] - b[i])
	}
	return math.Sqrt(s)
}

func chromaChange(a, b [12]float64) float64 {
	var ab, aa, bb float64
	for i := range a {
		ab += a[i] * b[i]
		aa += a[i] * a[i]
		bb += b[i] * b[i]
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return 1 - ab/math.Sqrt(aa*bb)
}

func peakNear(x []float32, t, before, after int) float64 {
	best := 0.0
	for i := max(0, t-before); i <= min(len(x)-1, t+after); i++ {
		best = math.Max(best, float64(x[i]))
	}
	return best
}

func meanOver(x []float32, from, to int) float64 {
	from, to = max(0, from), min(len(x), to)
	if to <= from {
		return 0
	}
	var s float64
	for t := from; t < to; t++ {
		s += float64(x[t])
	}
	return s / float64(to-from)
}

// cyclePath finds where a cycle of n positions falls on a sequence (beats in
// a bar, bars in a phrase): position 0 earns the strength there, the others
// lose an n−1th of it; moving to the next position is free, any other move
// (a slip) costs slip. It returns the position of every element and the
// path's score per element (the mean separation, used as confidence).
func cyclePath(strength []float64, n int, slip float64) ([]int, float64) {
	m := len(strength)
	if m == 0 || n < 2 {
		return nil, 0
	}
	emit := func(i, k int) float64 {
		if k == 0 {
			return strength[i]
		}
		return -strength[i] / float64(n-1)
	}
	score := make([]float64, n)
	for k := 0; k < n; k++ {
		score[k] = emit(0, k)
	}
	back := make([][]int, m)
	next := make([]float64, n)
	for i := 1; i < m; i++ {
		back[i] = make([]int, n)
		for k := 0; k < n; k++ {
			prev := (k - 1 + n) % n
			bi, bv := prev, score[prev]
			for j := 0; j < n; j++ {
				if j != prev {
					if v := score[j] - slip; v > bv {
						bi, bv = j, v
					}
				}
			}
			next[k] = bv + emit(i, k)
			back[i][k] = bi
		}
		copy(score, next)
	}
	k := 0
	for j := range score {
		if score[j] > score[k] {
			k = j
		}
	}
	total := score[k]
	pos := make([]int, m)
	for i := m - 1; i >= 0; i-- {
		pos[i] = k
		if i > 0 {
			k = back[i][k]
		}
	}
	return pos, total / float64(m)
}

// Bars and phrases: how much a slip costs and how clear they must be.
const (
	barSlip       = 6.0
	phraseSlip    = 4.0
	minBarConf    = 0.25
	minPhraseConf = 0.2
)

// findBars is the meter (3 or 4; 0 when unclear), the indexes of the beats
// that start bars, and how clear the bars are (0–1).
func findBars(beats []int, f *gridFrames) (int, []int, float64) {
	if len(beats) < 16 {
		return 0, nil, 0
	}
	low := make([]float64, len(beats))
	chg := make([]float64, len(beats))
	for i, t := range beats {
		low[i] = peakNear(f.low, t, 1, 2)
		chg[i] = chromaChange(meanChroma(f, prevBeat(beats, i), t), meanChroma(f, t, nextBeat(beats, i, len(f.flux))))
	}
	zl, zc := zscores(low), zscores(chg)
	d := make([]float64, len(beats))
	for i := range d {
		d[i] = zl[i] + zc[i]
	}
	best, bestM, bestPos := math.Inf(-1), 0, []int(nil)
	scores := map[int]float64{}
	for _, m := range []int{4, 3} {
		pos, s := cyclePath(d, m, barSlip)
		scores[m] = s
		// Four beats to the bar is far more common: three must clearly win.
		if m == 3 && s < scores[4]*1.15 {
			continue
		}
		if s > best {
			best, bestM, bestPos = s, m, pos
		}
	}
	conf := math.Max(0, math.Min(1, best/2))
	if conf < minBarConf {
		return 0, nil, conf
	}
	var downs []int
	for i, k := range bestPos {
		if k == 0 {
			downs = append(downs, i)
		}
	}
	return bestM, downs, conf
}

func prevBeat(beats []int, i int) int {
	if i > 0 {
		return beats[i-1]
	}
	if len(beats) > 1 {
		return beats[0] - (beats[1] - beats[0])
	}
	return beats[0]
}

func nextBeat(beats []int, i, n int) int {
	if i+1 < len(beats) {
		return beats[i+1]
	}
	if i > 0 {
		return min(n, beats[i]+(beats[i]-beats[i-1]))
	}
	return n
}

// findPhrases is the bars in a phrase (4 or 8; 0 when unclear), the indexes
// (into downs) of the bars that start phrases, and how clear they are.
func findPhrases(beats, downs []int, f *gridFrames) (int, []int, float64) {
	if len(downs) < 12 {
		return 0, nil, 0
	}
	n := len(downs)
	at := func(b int) int { // the bar line's frame (b may run past the ends)
		switch {
		case b < 0:
			return beats[downs[0]] + b*(beats[downs[min(1, n-1)]]-beats[downs[0]])
		case b >= n:
			last := beats[downs[n-1]]
			bar := last - beats[downs[max(0, n-2)]]
			return min(len(f.flux), last+(b-n+1)*bar)
		}
		return beats[downs[b]]
	}
	// At each bar line: how much the texture (six bands) and the harmony
	// change from the two bars before to the two after, how loud the top
	// rings just after it (a crash), and how busy the beat before it is (a fill).
	tex := make([]float64, n)
	chg := make([]float64, n)
	crash := make([]float64, n)
	fill := make([]float64, n)
	for b := 0; b < n; b++ {
		t := at(b)
		beat := max(1, (at(b+1)-t)/4)
		tex[b] = bandDistance(meanBands(f, at(b-2), t), meanBands(f, t, at(b+2)))
		chg[b] = chromaChange(meanChroma(f, at(b-2), t), meanChroma(f, t, at(b+2)))
		crash[b] = meanBands(f, t, t+max(1, beat/2))[gridBands-1]
		fill[b] = meanOver(f.flux, t-beat, t)
	}
	zx, zc, zr, zf := zscores(tex), zscores(chg), zscores(crash), zscores(fill)
	s := make([]float64, n)
	for b := range s {
		s[b] = zx[b] + zc[b] + zr[b] + zf[b]
	}
	best, bestQ, bestPos := math.Inf(-1), 0, []int(nil)
	scores := map[int]float64{}
	for _, q := range []int{4, 8} {
		if n < 3*q {
			continue
		}
		pos, sc := cyclePath(s, q, phraseSlip)
		scores[q] = sc
		// Phrases of four bars are the ones an ending needs; eight must clearly win.
		if q == 8 && sc < scores[4]*1.25 {
			continue
		}
		if sc > best {
			best, bestQ, bestPos = sc, q, pos
		}
	}
	if bestQ == 0 {
		return 0, nil, 0
	}
	conf := math.Max(0, math.Min(1, best/3))
	if conf < minPhraseConf {
		return 0, nil, conf
	}
	var starts []int
	for b, k := range bestPos {
		if k == 0 {
			starts = append(starts, b)
		}
	}
	return bestQ, starts, conf
}
