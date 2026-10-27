// Package conversation is conversations from online calls (D99–D103,
// docs/conversations.md): aligning several recordings of one talk on one clock
// (sync), the automatic cut between people and layouts, call clean-up, webcam
// colour matching, imports of provider folders and best moments. The edit
// spec side (pins, conversation clips, shots, the sound) is in editspec.
package conversation

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"

	"github.com/metavert-io/metarush/internal/tools"
)

// The sound is analysed at 16 kHz mono in the speech band (300–3400 Hz:
// rumble, hiss and most music stay out), as its energy every 10 ms.
const (
	sampleRate = 16000
	hop        = sampleRate / envRate
	envRate    = 100 // values a second
)

// sound is one recording's energy over time: dB every 10 ms (a 30 ms window
// centered on each step), from its start.
type sound struct {
	db  []float32
	dur float64
}

// envWriter turns streamed float32 samples into 10 ms energies.
type envWriter struct {
	pending []byte
	acc     float64
	n       int
	sums    []float64 // sum of squares per 10 ms
}

func (w *envWriter) Write(p []byte) (int, error) {
	total := len(p)
	if len(w.pending) > 0 {
		p = append(w.pending, p...)
		w.pending = nil
	}
	for len(p) >= 4 {
		v := float64(math.Float32frombits(binary.LittleEndian.Uint32(p)))
		p = p[4:]
		w.acc += v * v
		w.n++
		if w.n == hop {
			w.sums = append(w.sums, w.acc)
			w.acc, w.n = 0, 0
		}
	}
	if len(p) > 0 {
		w.pending = append([]byte(nil), p...)
	}
	return total, nil
}

func (w *envWriter) sound() *sound {
	if w.n > 0 {
		w.sums = append(w.sums, w.acc*float64(hop)/float64(w.n))
	}
	s := &sound{db: make([]float32, len(w.sums)), dur: float64(len(w.sums)) / envRate}
	for k := range w.sums {
		e := w.sums[k]
		n := 1.0
		if k > 0 {
			e += w.sums[k-1]
			n++
		}
		if k+1 < len(w.sums) {
			e += w.sums[k+1]
			n++
		}
		s.db[k] = float32(10 * math.Log10(e/(n*hop)+1e-12))
	}
	return s
}

// decodeSound reads a recording's sound (from seconds in, for dur seconds;
// dur 0: to the end) through ffmpeg into energies.
func decodeSound(ctx context.Context, ts *tools.Set, path string, from, dur float64) (*sound, error) {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if from > 0 {
		args = append(args, "-ss", strconv.FormatFloat(from, 'f', 3, 64))
	}
	args = append(args, "-i", path)
	if dur > 0 {
		args = append(args, "-t", strconv.FormatFloat(dur, 'f', 3, 64))
	}
	args = append(args, "-map", "0:a:0", "-vn", "-af", "highpass=f=300,lowpass=f=3400", "-ac", "1", "-ar", strconv.Itoa(sampleRate), "-f", "f32le", "pipe:1")
	w := &envWriter{}
	if _, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args, Stdout: w}); err != nil {
		return nil, fmt.Errorf("reading the sound: %w", err)
	}
	s := w.sound()
	if len(s.db) == 0 {
		return nil, fmt.Errorf("the recording has no sound")
	}
	return s, nil
}

// decodeSamples reads [from, from+dur) of a recording's sound as 16 kHz mono
// samples (the clap assist refines its moment on them).
func decodeSamples(ctx context.Context, ts *tools.Set, path string, from, dur float64) ([]float32, error) {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-ss", strconv.FormatFloat(math.Max(0, from), 'f', 3, 64), "-i", path,
		"-t", strconv.FormatFloat(dur, 'f', 3, 64), "-map", "0:a:0", "-vn", "-ac", "1", "-ar", strconv.Itoa(sampleRate), "-f", "f32le", "pipe:1"}
	res, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args})
	if err != nil {
		return nil, err
	}
	out := make([]float32, len(res.Stdout)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(res.Stdout[i*4:]))
	}
	return out, nil
}

var _ io.Writer = (*envWriter)(nil)

// percentile of xs (0–100), on a copy.
func percentile(xs []float32, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	c := make([]float64, len(xs))
	for i, x := range xs {
		c[i] = float64(x)
	}
	sort.Float64s(c)
	i := int(math.Round(p / 100 * float64(len(c)-1)))
	return c[max(0, min(len(c)-1, i))]
}

// levels are a recording's background (10th percentile) and speaking level
// (95th percentile) in dB.
func (s *sound) levels() (noise, speech float64) {
	return percentile(s.db, 10), percentile(s.db, 95)
}

// features are a recording's onsets (the rise of its energy above its
// background, square-rooted so loud and quiet events weigh closer), all of
// them and split by level: its own voice (near its speaking level) and the
// quiet ones (well below it but above the background: what the microphone
// picked up from elsewhere).
type features struct {
	all, own, quiet []float32
}

func (s *sound) features() features {
	noise, speech := s.levels()
	floor := float32(noise + 6)
	ownAt := float32(speech - 15)
	n := len(s.db)
	f := features{all: make([]float32, n), own: make([]float32, n), quiet: make([]float32, n)}
	prev := floor
	for k := 0; k < n; k++ {
		v := s.db[k]
		if v < floor {
			v = floor
		}
		d := v - prev
		prev = v
		if d <= 0 {
			continue
		}
		o := float32(math.Sqrt(float64(d)))
		f.all[k] = o
		// The level the onset rises to decides whose it is (a few steps on, as
		// syllables peak after they start).
		peak := v
		for j := k + 1; j < min(n, k+6); j++ {
			if s.db[j] > peak {
				peak = s.db[j]
			}
		}
		if peak >= ownAt {
			f.own[k] = o
		} else {
			f.quiet[k] = o
		}
	}
	return f
}
