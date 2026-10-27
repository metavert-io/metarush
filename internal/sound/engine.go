package sound

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/textalign"
	"github.com/metavert-io/metarush/internal/tools"
)

// WetRate is the rate processed voices and copies are made at.
const WetRate = 48000

// The clean pipeline after DeepFilterNet (noise and room echo): rumble and
// pops (a high-pass and a dynamic low shelf that only acts on bursts), a
// gentle presence EQ (less mud around 300 Hz, a little presence around 4 kHz,
// a touch of air), softer harsh s sounds, smooth compression and a slow
// leveler. Tuned on noisy test speech with the clarity score (D89); the version
// below goes into every cache key, so changing it never reuses old results.
const (
	cleanChainVersion = "clean-chain-1"
	cleanChain        = "highpass=f=75:poles=2," +
		"adynamicequalizer=dfrequency=110:dqfactor=0.8:tfrequency=110:tqfactor=0.7:dftype=lowpass:tftype=lowshelf:mode=cutabove:auto=on:ratio=4:range=10:attack=1:release=80," +
		"equalizer=f=320:t=q:w=1.1:g=-2.5,equalizer=f=4200:t=q:w=0.9:g=2.5,highshelf=f=11000:g=1.5," +
		"deesser=i=0.35:m=0.5:f=0.5:s=o," +
		"acompressor=threshold=-24dB:ratio=2.5:attack=8:release=140:knee=6:makeup=1," +
		"dynaudnorm=f=250:g=15:p=0.9:m=4:t=0.002"
)

// Engine makes processed voices on this machine.
type Engine struct {
	Tools   *tools.Set
	Comp    *Components
	Scratch string // where temporary files go (deleted after use)
	// Cloud isolates a voice through ElevenLabs (nil: cloud mode is unavailable).
	Cloud func(ctx context.Context, wavPath string, seconds float64) ([]byte, error)
	// Hear transcribes a 16 kHz mono WAV into timed words with the local
	// recognizer (nil: none here); studio's word check uses it.
	Hear func(ctx context.Context, wav16 string) ([]textalign.Word, error)
	// HearID names the recognizer (it goes into studio's cache keys).
	HearID string
	// Whose are the projects whose voices the cloud step sends (What may
	// leave, D290): the cut's and every project its clips belong to.
	Whose []string
}

// EngineVersion is what a mode's results depend on (it goes into cache keys).
// Clean includes ffmpeg's version because its polish runs in ffmpeg; studio and
// cloud only use ffmpeg to read and resample, so an ffmpeg upgrade never makes
// them run (or charge) again.
func (e *Engine) EngineVersion(ctx context.Context, mode string) string {
	switch mode {
	case "clean":
		return fmt.Sprintf("deepfilternet-%s+%s+ffmpeg-%s", e.Tools.Version(ctx, tools.DeepFilter), cleanChainVersion, e.Tools.Version(ctx, tools.FFmpeg))
	case "studio":
		// The word check is part of the result: its version and the recognizer.
		hear := e.HearID
		if e.Hear == nil {
			hear = "none"
		}
		return fmt.Sprintf("deepfilternet-%s+%s+%s+guard-2:%s", e.Tools.Version(ctx, tools.DeepFilter), StudioVersion, strings.Join(studioArgs, ""), hear)
	case "cloud":
		return "elevenlabs-audio-isolation-v1"
	}
	return mode
}

func (e *Engine) tempDir() (string, func(), error) {
	if err := os.MkdirAll(e.Scratch, 0o755); err != nil {
		return "", nil, err
	}
	d, err := os.MkdirTemp(e.Scratch, "sound-")
	if err != nil {
		return "", nil, err
	}
	return d, func() { os.RemoveAll(d) }, nil
}

// Extract decodes [from, to) seconds of a media file's first audio stream at
// rate, with channels channels (1 or 2).
func (e *Engine) Extract(ctx context.Context, src string, from, to float64, rate, channels int) (*Audio, error) {
	dir, done, err := e.tempDir()
	if err != nil {
		return nil, err
	}
	defer done()
	out := filepath.Join(dir, "x.wav")
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-ss", ffSec(math.Max(0, from)), "-i", src}
	if to > from {
		args = append(args, "-t", ffSec(to-math.Max(0, from)))
	}
	args = append(args, "-vn", "-map", "0:a:0", "-ar", strconv.Itoa(rate), "-ac", strconv.Itoa(channels), "-c:a", "pcm_f32le", "-f", "wav", out)
	if _, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args, Background: true}); err != nil {
		return nil, fmt.Errorf("could not read the recording's sound: %w", err)
	}
	return ReadWAV(out)
}

func ffSec(v float64) string { return strconv.FormatFloat(v, 'f', 6, 64) }

// Wet is a processed voice: mono at WetRate for [From, From+len) of a source.
type Wet struct {
	Audio   *Audio
	From    float64
	Took    time.Duration
	Device  string  // studio: mps | cpu
	Credits float64 // cloud: what ElevenLabs charged (estimated)
	Guard   *Guard  // studio: the word check
}

// Process makes the processed voice for [from, to) of a source file.
func (e *Engine) Process(ctx context.Context, mode, src string, from, to float64) (*Wet, error) {
	start := time.Now()
	var (
		w   *Wet
		err error
	)
	switch mode {
	case "clean":
		w, err = e.clean(ctx, src, from, to)
	case "studio":
		w, err = e.studio(ctx, src, from, to)
	case "cloud":
		w, err = e.cloud(ctx, src, from, to)
	default:
		return nil, fmt.Errorf("unknown way to make a voice clearer: %q", mode)
	}
	if err != nil {
		return nil, err
	}
	w.From, w.Took = from, time.Since(start)
	return w, nil
}

// monoInput writes [from, to) of src as a mono float WAV at rate.
func (e *Engine) monoInput(ctx context.Context, src, path string, from, to float64, rate int) (*Audio, error) {
	a, err := e.Extract(ctx, src, from, to, rate, 2)
	if err != nil {
		return nil, err
	}
	m := &Audio{Rate: rate, Channels: 1, Data: a.Mono()}
	return m, WriteWAV(path, m, true)
}

// denoise runs the voice cleaner (DeepFilterNet) on [from, to) of a source
// into dir and returns its output (mono, WetRate, lined up with the input).
func (e *Engine) denoise(ctx context.Context, src string, from, to float64, dir, name string) (string, error) {
	if ok, why := e.Comp.Ready("clean"); !ok {
		return "", errors.New(why)
	}
	in := filepath.Join(dir, name+".wav")
	if _, err := e.monoInput(ctx, src, in, from, to, WetRate); err != nil {
		return "", err
	}
	outDir := filepath.Join(dir, name+"-out")
	// -D: compensate the model's look-ahead so the result lines up with the input.
	if _, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.DeepFilter, Args: []string{"-D", "-o", outDir, in}, Background: true}); err != nil {
		return "", fmt.Errorf("the voice cleaner failed: %w", err)
	}
	return filepath.Join(outDir, name+".wav"), nil
}

func (e *Engine) clean(ctx context.Context, src string, from, to float64) (*Wet, error) {
	dir, done, err := e.tempDir()
	if err != nil {
		return nil, err
	}
	defer done()
	den, err := e.denoise(ctx, src, from, to, dir, "voice")
	if err != nil {
		return nil, err
	}
	post := filepath.Join(dir, "post.wav")
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-i", den, "-af", cleanChain,
		"-ar", strconv.Itoa(WetRate), "-ac", "1", "-c:a", "pcm_f32le", post}
	if _, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args, Background: true}); err != nil {
		return nil, fmt.Errorf("the voice polish failed: %w", err)
	}
	a, err := ReadWAV(post)
	if err != nil {
		return nil, err
	}
	return &Wet{Audio: a}, nil
}

// studioResult is the runner's line for one finished job.
type studioResult struct {
	Index        int     `json:"index"`
	Device       string  `json:"device"` // cuda | mps | cpu
	Torch        string  `json:"torch"`
	Seconds      float64 `json:"seconds"`
	AudioSeconds float64 `json:"audio_seconds"`
	// Fallback says why a graphics card was not used ("" when it was, or
	// there is none).
	Fallback string `json:"fallback,omitempty"`
}

// studioJob is one file for the runner: its studio voice goes to Out and,
// when Denoised is set, its denoiser-only version (the safe one) there.
type studioJob struct {
	In       string `json:"in"`
	Out      string `json:"out"`
	Denoised string `json:"denoised,omitempty"`
}

// runStudio runs the studio voice on files, loading the model once; onDone
// (optional) hears about each finished file.
func runStudio(ctx context.Context, ts *tools.Set, dir string, jobs []studioJob, onDone func(done int), extra ...string) ([]studioResult, error) {
	// The runner ships inside MetaRush: a newer MetaRush brings a newer one
	// (the Python packages and the model stay).
	runner := filepath.Join(dir, studioRunnerName)
	if cur, err := os.ReadFile(runner); err != nil || !bytes.Equal(cur, studioRunner) {
		if err := os.WriteFile(runner, studioRunner, 0o644); err != nil {
			return nil, err
		}
	}
	tmp, err := os.MkdirTemp("", "metarush-studio-jobs-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	jf := filepath.Join(tmp, "jobs.json")
	b, _ := json.Marshal(jobs)
	if err := os.WriteFile(jf, b, 0o600); err != nil {
		return nil, err
	}
	res := make([]studioResult, len(jobs))
	got := 0
	var lines []string
	_, err = ts.Run(ctx, tools.Spec{Tool: tools.StudioPython, Background: true,
		Args: append([]string{"-I", filepath.Join(dir, studioRunnerName), "--model-dir", filepath.Join(dir, "model"), "--jobs", jf, "--device", "auto"}, extra...),
		Env:  []string{"PYTORCH_ENABLE_MPS_FALLBACK=1", "HF_HUB_OFFLINE=1", "TQDM_DISABLE=1"},
		OnLine: func(line string) {
			lines = append(lines, line)
			var r struct {
				Type string `json:"type"`
				studioResult
			}
			if json.Unmarshal([]byte(line), &r) == nil && r.Type == "done" && r.Index >= 0 && r.Index < len(res) {
				res[r.Index] = r.studioResult
				got++
				if onDone != nil {
					onDone(got)
				}
			}
		}})
	if err != nil {
		return nil, fmt.Errorf("the studio voice failed: %w", err)
	}
	if got != len(jobs) {
		return nil, fmt.Errorf("the studio voice gave no result (%s)", strings.Join(lines, " "))
	}
	return res, nil
}

func (e *Engine) studio(ctx context.Context, src string, from, to float64) (*Wet, error) {
	ws, err := e.StudioMany(ctx, []StudioPart{{Src: src, From: from, To: to}}, nil)
	if err != nil {
		return nil, err
	}
	return ws[0], nil
}

// studioArgs are the studio voice's settings: the enhancer's own denoising is
// off (lambd 0) because it works on the voice cleaner's output.
var studioArgs = []string{"--nfe", "64", "--solver", "midpoint", "--lambd", "0", "--tau", "0.5"}

// StudioPart is one stretch of a recording for the studio voice.
type StudioPart struct {
	Src      string // the recording's file
	From, To float64
}

// StudioMany makes studio voices for stretches of any number of recordings
// with one model load (D121; it used to load once per recording): the voice
// cleaner first (DeepFilterNet, as clean), then Resemble Enhance rebuilds the
// cleaned voice. That generative step can say a word differently
// ("seedlings" as "seaweeds", measured on noisy speech), so the local
// recognizer hears the cleaned voice and the rebuilt one, and wherever a word
// differs the cleaned voice plays (D90). Without a recognizer the cleaned
// voice plays throughout. progress (optional) hears how many are rebuilt.
func (e *Engine) StudioMany(ctx context.Context, parts []StudioPart, progress func(done int)) ([]*Wet, error) {
	if ok, why := e.Comp.Ready("studio"); !ok {
		return nil, errors.New(why)
	}
	dir, done, err := e.tempDir()
	if err != nil {
		return nil, err
	}
	defer done()
	bases := make([]string, len(parts))
	jobs := make([]studioJob, len(parts))
	for i, p := range parts {
		if bases[i], err = e.denoise(ctx, p.Src, p.From, p.To, dir, fmt.Sprintf("clean-%d", i)); err != nil {
			return nil, err
		}
		in := filepath.Join(dir, fmt.Sprintf("in-%d.wav", i))
		if _, err := e.monoInput(ctx, bases[i], in, 0, 0, 44100); err != nil { // the model's own rate
			return nil, err
		}
		jobs[i] = studioJob{In: in, Out: filepath.Join(dir, fmt.Sprintf("out-%d.wav", i))}
	}
	start := time.Now()
	res, err := runStudio(ctx, e.Tools, e.Comp.StudioDir(), jobs, progress, studioArgs...)
	if err != nil {
		return nil, err
	}
	each := time.Since(start) / time.Duration(len(jobs))
	out := make([]*Wet, len(jobs))
	for i, j := range jobs {
		base, err := ReadWAV(bases[i])
		if err != nil {
			return nil, err
		}
		gen, err := e.Extract(ctx, j.Out, 0, 0, WetRate, 1)
		if err != nil {
			return nil, err
		}
		gen.Data = lineUp(base.Data, gen.Data, WetRate)
		w := &Wet{Audio: gen, Device: res[i].Device, Took: each}
		if e.Hear == nil {
			w.Audio, w.Guard = base, &Guard{Checked: false}
		} else if err := e.guard(ctx, w, base); err != nil {
			return nil, err
		}
		out[i] = w
	}
	return out, nil
}

func (e *Engine) cloud(ctx context.Context, src string, from, to float64) (*Wet, error) {
	if e.Cloud == nil {
		return nil, errors.New("cloud voice isolation needs your ElevenLabs key on this computer (Settings → Integrations)")
	}
	dir, done, err := e.tempDir()
	if err != nil {
		return nil, err
	}
	defer done()
	a, err := e.Extract(ctx, src, from, to, WetRate, 2)
	if err != nil {
		return nil, err
	}
	in := filepath.Join(dir, "voice.wav")
	if err := WriteWAV(in, &Audio{Rate: WetRate, Channels: 1, Data: a.Mono()}, false); err != nil {
		return nil, err
	}
	body, err := e.Cloud(ctx, in, to-from)
	if err != nil {
		return nil, err
	}
	got := filepath.Join(dir, "isolated.audio")
	if err := os.WriteFile(got, body, 0o600); err != nil {
		return nil, err
	}
	w, err := e.Extract(ctx, got, 0, 0, WetRate, 1)
	if err != nil {
		return nil, errors.New("ElevenLabs returned sound MetaRush cannot read")
	}
	return &Wet{Audio: w, Credits: CloudCredits(to - from)}, nil
}

// CloudCreditsPerMinute is what ElevenLabs charges for voice isolation: "1000
// characters for every minute of audio" (elevenlabs.io/docs/capabilities/
// voice-isolator, checked 2026-10-19; files up to 500 MB and 1 hour).
const CloudCreditsPerMinute = 1000

// CloudMaxSeconds is the longest piece ElevenLabs takes in one request.
const CloudMaxSeconds = 3600

// CloudCredits estimates the charge for isolating seconds of audio (rounded up
// to a whole credit; ElevenLabs bills per minute of audio at that rate).
func CloudCredits(seconds float64) float64 {
	return math.Ceil(math.Max(0, seconds) / 60 * CloudCreditsPerMinute)
}
