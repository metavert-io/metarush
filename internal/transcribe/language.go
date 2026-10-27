package transcribe

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/tools"
)

// Heard is what whisper.cpp's language detection made of a clip's speech
// (D243): each clip is transcribed in the language it is spoken in.
type Heard struct {
	// Language is the code whisper.cpp uses ("es", "ja"); "" when the clip
	// has too little speech to tell.
	Language string `json:"language,omitempty"`
	// Score is how sure it was: whisper's probability for Language averaged
	// over the stretches it listened to (a stretch that heard another
	// language counts as 0), from 0 to 1.
	Score float64 `json:"score,omitempty"`
	// Listened is how many seconds of the clip it listened to.
	Listened float64 `json:"listened,omitempty"`
}

const (
	// DetectWindow is how long one listen is: whisper.cpp's language
	// detection reads the first 30 s it is given.
	DetectWindow = 30.0
	// DetectMinSpeech is the least speech a clip needs before its language is
	// guessed; below it the workspace's language is used.
	DetectMinSpeech = 2.0
	// DetectSure is the score from which a detection is taken without
	// question; below it the clip still gets the language heard, and the
	// dashboard says MetaRush wasn't sure.
	DetectSure = 0.75
)

// Detector tells a clip's spoken language with whisper.cpp's language
// detection (`-dl`), listening where the clip's speech is rather than to its
// first 30 seconds (which may be music or silence).
type Detector struct {
	t     *tools.Set
	model string
}

// NewDetector returns nil when whisper.cpp or a multilingual model is not
// installed (an English-only model can't tell languages apart).
func NewDetector(t *tools.Set, model string) *Detector {
	w, _ := newWhisperCPP(t, model, false).(*whisperCPP)
	if w == nil || strings.HasSuffix(w.model, ".en.bin") || !t.Has(tools.FFmpeg) {
		return nil
	}
	return &Detector{t: t, model: w.model}
}

// Model names the model that listens (for job records).
func (d *Detector) Model() string {
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Base(d.model), "ggml-"), ".bin")
}

// Detect listens to the clip's speech (a 16 kHz mono WAV): up to two 30 s
// stretches, one from where speech starts and, in a clip with more speech
// than that, one from the middle of its speech, and adds whisper's
// probabilities per language.
func (d *Detector) Detect(ctx context.Context, wav, scratch string) (Heard, error) {
	dur, err := wavDuration(wav)
	if err != nil {
		return Heard{}, err
	}
	sil, err := (&parakeet{t: d.t}).silences(ctx, wav, dur)
	if err != nil {
		return Heard{}, err
	}
	return d.detect(ctx, wav, scratch, speechWindows(dur, sil, DetectWindow, 2))
}

func (d *Detector) detect(ctx context.Context, wav, scratch string, windows []span) (Heard, error) {
	if len(windows) == 0 {
		return Heard{}, nil
	}
	votes := map[string]float64{}
	listened := 0.0
	for i, w := range windows {
		lang, p, err := d.listen(ctx, wav, filepath.Join(scratch, fmt.Sprintf("listen-%d.wav", i)), w)
		if err != nil {
			return Heard{}, err
		}
		listened += w.to - w.from
		if lang != "" {
			votes[lang] += p
		}
	}
	best, total := "", 0.0
	for _, l := range sortedKeys(votes) {
		if votes[l] > total {
			best, total = l, votes[l]
		}
	}
	return Heard{Language: best, Score: round3(total / float64(len(windows))), Listened: round3(listened)}, nil
}

var reDetected = regexp.MustCompile(`auto-detected language:\s*([a-z]{2,3})\s*\(p\s*=\s*([\d.]+)\)`)

// listen cuts one stretch out of the clip and asks whisper.cpp which language
// it is ("" when whisper printed none).
func (d *Detector) listen(ctx context.Context, wav, cut string, w span) (string, float64, error) {
	if _, err := d.t.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Args: []string{
		"-nostdin", "-hide_banner", "-loglevel", "error", "-y", "-ss", ffSec(w.from), "-t", ffSec(w.to - w.from),
		"-i", wav, "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", cut}}); err != nil {
		return "", 0, err
	}
	res, err := d.t.Run(ctx, tools.Spec{Tool: tools.Whisper, Background: true, Args: []string{
		"-m", d.model, "-f", cut, "-l", "auto", "-dl", "-t", fmt.Sprint(whisperThreads())}})
	if err != nil {
		return "", 0, err
	}
	return parseDetected(string(res.Stderr) + string(res.Stdout))
}

func parseDetected(out string) (string, float64, error) {
	m := reDetected.FindStringSubmatch(out)
	if m == nil {
		return "", 0, nil
	}
	p, _ := strconv.ParseFloat(m[2], 64)
	return m[1], p, nil
}

// speechWindows picks up to n stretches of at most length seconds where the
// clip has speech (the gaps between ffmpeg's silences): the first from where
// speech starts, the others from the middle of the speech onward, never
// overlapping. A clip with less than DetectMinSpeech of speech gets none.
func speechWindows(dur float64, sil []span, length float64, n int) []span {
	speech := speechSpans(dur, sil)
	total := 0.0
	for _, s := range speech {
		total += s.to - s.from
	}
	if total < DetectMinSpeech || n <= 0 {
		return nil
	}
	// at maps "seconds of speech so far" to a time in the clip.
	at := func(k float64) float64 {
		for _, s := range speech {
			if k <= s.to-s.from {
				return s.from + k
			}
			k -= s.to - s.from
		}
		return speech[len(speech)-1].to
	}
	var out []span
	starts := []float64{speech[0].from}
	for i := 1; i < n; i++ {
		starts = append(starts, at(total*float64(i)/float64(n)))
	}
	for _, from := range starts {
		if len(out) > 0 && from < out[len(out)-1].to {
			continue
		}
		to := math.Min(dur, from+length)
		if to-from >= DetectMinSpeech {
			out = append(out, span{from, to})
		}
	}
	return out
}

// speechSpans is the complement of the silences within [0, dur].
func speechSpans(dur float64, sil []span) []span {
	sort.Slice(sil, func(i, j int) bool { return sil[i].from < sil[j].from })
	var out []span
	at := 0.0
	for _, s := range sil {
		if s.from > at {
			out = append(out, span{at, math.Min(s.from, dur)})
		}
		at = math.Max(at, s.to)
	}
	if at < dur {
		out = append(out, span{at, dur})
	}
	return out
}

func ffSec(v float64) string { return strconv.FormatFloat(math.Max(0, v), 'f', 3, 64) }

func sortedKeys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
