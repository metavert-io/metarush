package sound

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/tools"
)

// Generated music with a known grid (D120): drums, bass and chords in bars,
// phrases of four bars (a crash where each starts, a snare fill in its last
// bar, the chords' texture changing every phrase), at tempos that hold, jump
// or glide, in 4/4 or 3/4, with a pickup and a short bar where asked.

type musicSection struct {
	bars           int
	meter          int
	bpmFrom, bpmTo float64 // linear over the section's beats
	short          int     // a bar of this many beats first (0: none)
}

type musicTruth struct {
	beats, downbeats, phrases []float64
	ibi                       map[int]float64 // true beat interval by beat index
	seconds                   float64
}

// synthMusic renders sections at rate (mono); pickup beats come before the
// first bar. Phrases are counted in whole bars of the meter (a short bar
// starts a new count).
func synthMusic(rate int, sections []musicSection, pickup int, seed int64) ([]float32, musicTruth) {
	rng := rand.New(rand.NewSource(seed))
	type beat struct {
		t                  float64
		inBar, meter, bar  int
		phraseStart, fill  bool
		chord              int
		down               bool
		interval           float64
		phraseIdx, barOfPh int
	}
	var beats []beat
	t := 0.6
	// The pickup: beats at the first section's tempo, before bar 1.
	first := sections[0]
	for k := 0; k < pickup; k++ {
		iv := 60 / first.bpmFrom
		beats = append(beats, beat{t: t, inBar: first.meter - pickup + k, meter: first.meter, bar: -1, interval: iv})
		t += iv
	}
	barCount := 0 // bars in the current phrase count
	phrase := 0
	for _, s := range sections {
		total := s.bars * s.meter
		if s.short > 0 {
			total += s.short
		}
		k := 0
		layout := []int{}
		if s.short > 0 {
			layout = append(layout, s.short)
		}
		for b := 0; b < s.bars; b++ {
			layout = append(layout, s.meter)
		}
		for bi, n := range layout {
			if n != s.meter && bi == 0 {
				barCount = 0 // a short bar: the phrase count starts again after it
			}
			startsPhrase := n == s.meter && barCount%4 == 0
			if startsPhrase {
				phrase++
			}
			lastOfPhrase := n == s.meter && barCount%4 == 3
			for i := 0; i < n; i++ {
				bpm := s.bpmFrom + (s.bpmTo-s.bpmFrom)*float64(k)/float64(max(1, total-1))
				iv := 60 / bpm
				beats = append(beats, beat{t: t, inBar: i, meter: n, bar: bi, down: i == 0, phraseStart: startsPhrase && i == 0,
					fill: lastOfPhrase && i >= n-2, chord: barCount % 4, interval: iv, phraseIdx: phrase})
				t += iv
				k++
			}
			if n == s.meter {
				barCount++
			}
		}
	}
	end := t + 1.5
	x := make([]float32, int(end*float64(rate)))
	sr := float64(rate)
	add := func(at float64, dur float64, f func(u float64) float64) {
		i0 := int(at * sr)
		for i := i0; i < min(len(x), i0+int(dur*sr)); i++ {
			x[i] += float32(f(float64(i-i0) / sr))
		}
	}
	noise := func() float64 { return 2*rng.Float64() - 1 }
	kick := func(amp float64) func(u float64) float64 {
		return func(u float64) float64 {
			f := 45 + 65*math.Exp(-u/0.03)
			return amp * math.Exp(-u/0.12) * math.Sin(2*math.Pi*f*u)
		}
	}
	snare := func(amp float64) func(u float64) float64 {
		return func(u float64) float64 {
			return amp * (0.7*noise()*math.Exp(-u/0.07) + 0.3*math.Sin(2*math.Pi*185*u)*math.Exp(-u/0.05))
		}
	}
	var lastHat float64
	hat := func(u float64) float64 {
		v := noise()
		hp := v - lastHat
		lastHat = v
		return 0.06 * hp * math.Exp(-u/0.018)
	}
	crash := func(u float64) float64 {
		v := noise()
		hp := v - lastHat
		lastHat = v
		return 0.16 * hp * math.Exp(-u/0.5)
	}
	roots := []float64{65.41, 49.0, 55.0, 43.65} // C, G, A, F (bass)
	chords := [][]float64{{261.6, 329.6, 392.0}, {246.9, 293.7, 392.0}, {220.0, 261.6, 329.6}, {220.0, 261.6, 349.2}}
	var truth musicTruth
	truth.ibi = map[int]float64{}
	for i, b := range beats {
		truth.beats = append(truth.beats, b.t)
		truth.ibi[i] = b.interval
		if b.down && b.bar >= 0 {
			truth.downbeats = append(truth.downbeats, b.t)
		}
		if b.phraseStart {
			truth.phrases = append(truth.phrases, b.t)
		}
		strong := b.meter == 4 && (b.inBar == 0 || b.inBar == 2) || b.meter == 3 && b.inBar == 0 || b.meter == 2 && b.inBar == 0
		backbeat := b.meter == 4 && (b.inBar == 1 || b.inBar == 3) || b.meter == 2 && b.inBar == 1
		if b.bar < 0 { // the pickup: hats and a snare only
			add(b.t, 0.08, hat)
			continue
		}
		switch {
		case b.down:
			add(b.t, 0.4, kick(0.9))
		case strong:
			add(b.t, 0.4, kick(0.55))
		}
		if backbeat {
			add(b.t, 0.3, snare(0.32))
		}
		if b.meter == 3 && b.inBar > 0 {
			add(b.t, 0.3, snare(0.2))
		}
		add(b.t, 0.08, hat)
		add(b.t+b.interval/2, 0.08, hat)
		if b.fill {
			for q := 1; q < 4; q++ {
				add(b.t+b.interval*float64(q)/4, 0.2, snare(0.22))
			}
		}
		if b.phraseStart {
			add(b.t, 2.5, crash)
		}
		if b.down {
			barLen := 0.0
			for j := i; j < len(beats) && (j == i || !beats[j].down); j++ {
				barLen += beats[j].interval
			}
			root := roots[b.chord]
			chord := chords[b.chord]
			pluck := b.phraseIdx%2 == 0 // every other phrase: short bright chords instead of a pad
			add(b.t, barLen, func(u float64) float64 {
				env := math.Min(1, u/0.01) * math.Exp(-u/(barLen*0.8))
				return 0.22 * env * (math.Sin(2*math.Pi*root*u) + 0.4*math.Sin(2*math.Pi*2*root*u))
			})
			add(b.t, barLen, func(u float64) float64 {
				var v float64
				if pluck {
					env := math.Exp(-math.Mod(u, barLen/2) / 0.12)
					for _, f := range chord {
						v += 0.05 * env * (math.Sin(2*math.Pi*2*f*u) + 0.5*math.Sin(2*math.Pi*4*f*u))
					}
					return v
				}
				env := math.Min(1, u/0.05)
				for _, f := range chord {
					v += 0.045 * env * math.Sin(2*math.Pi*f*u)
				}
				return v
			})
		}
	}
	truth.seconds = end
	return x, truth
}

// fMeasure matches estimates to references within tol seconds (each used once).
func fMeasure(est, ref []float64, tol float64) float64 {
	if len(est) == 0 || len(ref) == 0 {
		return 0
	}
	used := make([]bool, len(est))
	hits := 0
	for _, r := range ref {
		i := sort.SearchFloat64s(est, r-tol)
		for ; i < len(est) && est[i] <= r+tol; i++ {
			if !used[i] {
				used[i] = true
				hits++
				break
			}
		}
	}
	p, rc := float64(hits)/float64(len(est)), float64(hits)/float64(len(ref))
	if p+rc == 0 {
		return 0
	}
	return 2 * p * rc / (p + rc)
}

// tempoError is the median relative error of the tracked beats' local tempo
// against the true one, at the true beats.
func tempoError(est []float64, tr musicTruth) float64 {
	if len(est) < 2 {
		return 1
	}
	var errs []float64
	for i, t := range tr.beats {
		k := sort.SearchFloat64s(est, t)
		if k <= 0 || k >= len(est) {
			continue
		}
		e := est[k] - est[k-1]
		errs = append(errs, math.Abs(e-tr.ibi[i])/tr.ibi[i])
	}
	return median(errs)
}

type gridCase struct {
	name     string
	sections []musicSection
	pickup   int
}

func gridCases() []gridCase {
	return []gridCase{
		{name: "steady 120 bpm, 4/4", sections: []musicSection{{bars: 48, meter: 4, bpmFrom: 120, bpmTo: 120}}},
		{name: "jump 100 → 132 bpm", sections: []musicSection{{bars: 28, meter: 4, bpmFrom: 100, bpmTo: 100}, {bars: 32, meter: 4, bpmFrom: 132, bpmTo: 132}}},
		{name: "glide 90 → 140 bpm", sections: []musicSection{{bars: 40, meter: 4, bpmFrom: 90, bpmTo: 140}, {bars: 16, meter: 4, bpmFrom: 140, bpmTo: 140}}},
		{name: "waltz 150 bpm, 3/4", sections: []musicSection{{bars: 64, meter: 3, bpmFrom: 150, bpmTo: 150}}},
		{name: "pickup and a short bar", pickup: 1, sections: []musicSection{{bars: 24, meter: 4, bpmFrom: 116, bpmTo: 116}, {bars: 24, meter: 4, bpmFrom: 116, bpmTo: 116, short: 2}}},
	}
}

// TestGridOnGeneratedMusic measures the grid against the truth, and beats-1
// on the same music, and holds the grid to it.
func TestGridOnGeneratedMusic(t *testing.T) {
	var report []string
	for i, gc := range gridCases() {
		x, tr := synthMusic(BeatRate, gc.sections, gc.pickup, int64(i+1))
		g := AnalyzeGrid(x)
		v1 := AnalyzeBeats(x)
		bf, v1f := fMeasure(g.Beats, tr.beats, 0.07), fMeasure(v1.Times, tr.beats, 0.07)
		te, v1te := tempoError(g.Beats, tr), tempoError(v1.Times, tr)
		df := fMeasure(g.Downbeats, tr.downbeats, 0.07)
		pf := fMeasure(g.Phrases, tr.phrases, 0.07)
		meter := gc.sections[0].meter
		report = append(report, fmt.Sprintf("%-24s beats F %.3f (beats-1 %.3f), tempo error %.2f%% (beats-1 %.2f%%), meter %d (true %d), bars F %.3f, phrases F %.3f (%d bars), tempo %v",
			gc.name, bf, v1f, te*100, v1te*100, g.Meter, meter, df, pf, g.PhraseBars, g.Tempos))
		if bf < 0.95 || te > 0.02 {
			t.Errorf("%s: beats F %.3f, tempo error %.2f%%", gc.name, bf, te*100)
		}
		if g.Meter != meter || df < 0.9 {
			t.Errorf("%s: meter %d (true %d), bars F %.3f", gc.name, g.Meter, meter, df)
		}
		if pf < 0.85 {
			t.Errorf("%s: phrases F %.3f", gc.name, pf)
		}
	}
	t.Log("\n" + strings.Join(report, "\n"))
}

const (
	fitVideo = "ast_01jfitvideoaaaaaaaaaaaaaa0"
	fitMusic = "ast_01jfitmusicaaaaaaaaaaaaaa0"
)

// fitFacts gives editspec's music fit a generated track: its grid (tracks
// with an ending) and its beats-1 beats (tracks without).
type fitFacts struct {
	seconds float64
	beats   []float64
	grid    *editspec.MusicGrid
}

func (f fitFacts) Asset(id string) (editspec.AssetFacts, bool) {
	switch id {
	case fitVideo:
		return editspec.AssetFacts{ID: id, Name: "talk.mov", Kind: "video", State: "available", InProject: true, Duration: 1800, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30}, true
	case fitMusic:
		return editspec.AssetFacts{ID: id, Name: "bed.wav", Kind: "audio", State: "available", InProject: true, Duration: f.seconds, HasAudio: true}, true
	}
	return editspec.AssetFacts{}, false
}
func (f fitFacts) Transcript(string) []editspec.TranscriptSegment { return nil }
func (f fitFacts) Beats(string) ([]float64, bool)                 { return f.beats, f.beats != nil }
func (f fitFacts) MusicGrid(string) (*editspec.MusicGrid, bool)   { return f.grid, f.grid != nil }

// endingsOn fits the track to videos of many lengths and says how often it
// ends on a true phrase start, a true bar line (one or the other) and a true
// beat (any of them), and how early it ends on average (seconds).
func endingsOn(f fitFacts, tr musicTruth, ending string) (phrase, bar, beat, gap float64) {
	near := func(xs []float64, t float64) bool {
		i := sort.SearchFloat64s(xs, t-0.07)
		return i < len(xs) && xs[i] <= t+0.07
	}
	n := 0
	for L := 20.0; L < f.seconds-5; L += (f.seconds - 25) / 60 {
		s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30}, Clips: []editspec.Clip{{ID: "talk", AssetID: fitVideo, In: 0, Out: math.Round(L*1000) / 1000}},
			Audio: []editspec.AudioTrack{{ID: "bed", AssetID: fitMusic, Role: editspec.RoleMusic, In: 0, Out: f.seconds, Fit: editspec.TrackFitAuto, Ending: ending}}}
		editspec.Normalize(s)
		fi, ok := editspec.MusicFit(s, s.Audio[0], f)
		if !ok {
			continue
		}
		n++
		switch {
		case near(tr.phrases, fi.To):
			phrase++
			bar++
			beat++
		case near(tr.downbeats, fi.To):
			bar++
			beat++
		case near(tr.beats, fi.To):
			beat++
		}
		gap += L - fi.To
	}
	k := float64(n)
	return phrase / k, bar / k, beat / k, gap / k
}

// TestEndingsOnPhrases measures where fitted music ends on the generated
// tracks, against the truth: with an ending (the grid) and as before (beats-1
// beats, the nearest beat).
func TestEndingsOnPhrases(t *testing.T) {
	var report []string
	for i, gc := range gridCases() {
		x, tr := synthMusic(BeatRate, gc.sections, gc.pickup, int64(i+1))
		g := AnalyzeGrid(x)
		v1 := AnalyzeBeats(x)
		f := fitFacts{seconds: tr.seconds, beats: v1.Times, grid: &editspec.MusicGrid{Beats: g.Beats, Downbeats: g.Downbeats, Phrases: g.Phrases}}
		p, b, bt, gap := endingsOn(f, tr, editspec.EndingPhrase)
		op, ob, obt, ogap := endingsOn(f, tr, "")
		report = append(report, fmt.Sprintf("%-24s ends on a phrase %3.0f%%, on a bar line %3.0f%%, on a beat %3.0f%%, %.2f s before the end — before: %3.0f%%, %3.0f%%, %3.0f%%, %.2f s",
			gc.name, p*100, b*100, bt*100, gap, op*100, ob*100, obt*100, ogap))
		if b < 0.9 || bt < 0.98 {
			t.Errorf("%s: endings on a bar line %.0f%%, on a beat %.0f%%", gc.name, b*100, bt*100)
		}
	}
	t.Log("\n" + strings.Join(report, "\n"))
}

// A 20-minute track whose tempo changes after 16 minutes, read from a file the
// way music is (streamed from ffmpeg): every beat and both tempos are found,
// where beats-1 read the first quarter hour only.
func TestGridOfALongTrack(t *testing.T) {
	if testing.Short() {
		t.Skip("a 20-minute track")
	}
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg is not installed")
	}
	x, tr := synthMusic(BeatRate, []musicSection{{bars: 448, meter: 4, bpmFrom: 112, bpmTo: 112}, {bars: 124, meter: 4, bpmFrom: 124, bpmTo: 124}}, 0, 9)
	dir := t.TempDir()
	file := filepath.Join(dir, "long.wav")
	if err := WriteWAV(file, &Audio{Rate: BeatRate, Channels: 1, Data: x}, false); err != nil {
		t.Fatal(err)
	}
	x = nil
	eng := &Engine{Tools: ts, Scratch: dir}
	g, err := eng.MusicGrid(context.Background(), file)
	if err != nil {
		t.Fatal(err)
	}
	bf := fMeasure(g.Beats, tr.beats, 0.07)
	late := 0
	for _, b := range g.Beats {
		if b > 16*60+5 {
			late++
		}
	}
	// beats-1 as music add ran it: one tempo, the first 15 minutes.
	head, err := eng.Extract(context.Background(), file, 0, 15*60, BeatRate, 1)
	if err != nil {
		t.Fatal(err)
	}
	v1 := AnalyzeBeats(head.Data)
	v1f := fMeasure(v1.Times, tr.beats, 0.07)
	t.Logf("20-minute track: %.0f s read, beats F %.3f (beats-1 %.3f, %d beats after 15:00: 0), %d beats after 16:05, tempo %v, bars F %.3f, phrases F %.3f",
		g.Seconds, bf, v1f, late, late, g.Tempos, fMeasure(g.Downbeats, tr.downbeats, 0.07), fMeasure(g.Phrases, tr.phrases, 0.07))
	if g.Seconds < tr.seconds-1 || bf < 0.95 || len(g.Tempos) != 2 || math.Abs(g.Tempos[0].Start-112) > 1.5 || math.Abs(g.Tempos[1].Start-124) > 1.5 || math.Abs(g.Tempos[1].From-tr.downbeats[448]) > 8 {
		t.Fatalf("long track: %.0f s, beats F %.3f, tempo %v", g.Seconds, bf, g.Tempos)
	}
}
