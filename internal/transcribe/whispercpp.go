package transcribe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/textalign"
	"github.com/metavert-io/metarush/internal/tools"
)

// whisperCPP runs whisper.cpp (Metal-accelerated on Apple Silicon). With
// transcription.resident (the default), whisper-server from the same build
// next to whisper-cli and a whisper.cpp version MetaRush checked, the model
// stays loaded between clips in the model server (D121, whisperserver.go);
// otherwise, or when that fails for a clip, whisper-cli runs once per clip.
// Both give the same JSON to the same parser, so transcripts are identical.
type whisperCPP struct {
	t     *tools.Set
	model string
	// resident keeps the model in memory (transcription.resident, default on).
	resident bool
	// fallback says why the last transcription ran whisper-cli instead ("" when
	// the loaded model was used, or never tried).
	fallback string
}

func newWhisperCPP(t *tools.Set, model string, resident bool) Provider {
	if !t.Has(tools.Whisper) {
		return nil
	}
	dir := filepath.Join(t.ModelsDir(), "whisper")
	if model == "" {
		for _, m := range []string{"ggml-large-v3-turbo.bin", "ggml-large-v3.bin", "ggml-medium.en.bin", "ggml-base.en.bin"} {
			if _, err := os.Stat(filepath.Join(dir, m)); err == nil {
				model = filepath.Join(dir, m)
				break
			}
		}
	} else if !filepath.IsAbs(model) {
		model = filepath.Join(dir, model)
	}
	if model == "" {
		return nil
	}
	if _, err := os.Stat(model); err != nil {
		return nil
	}
	return &whisperCPP{t: t, model: model, resident: resident}
}

func (w *whisperCPP) Name() string { return "whisper.cpp" }
func (w *whisperCPP) Model() string {
	return strings.TrimSuffix(strings.TrimPrefix(filepath.Base(w.model), "ggml-"), ".bin")
}

// Mode says how the model runs: ModeResident when it can stay loaded on this
// machine, else ModePerBatch (one whisper-cli process per clip).
func (w *whisperCPP) Mode() string {
	if !w.resident {
		return ModePerBatch
	}
	s, err := whisperServerFor(w.t, w.model, whisperThreads())
	if err != nil {
		return ModePerBatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.disabled != nil {
		return ModePerBatch
	}
	return ModeResident
}

// Fallback is why the last transcription ran whisper-cli instead of the
// loaded model ("" when it did not).
func (w *whisperCPP) Fallback() string { return w.fallback }

// whisperThreads is the decoder's thread count (half the cores, at least
// two), the same for both ways.
func whisperThreads() int { return max(2, runtime.NumCPU()/2) }

type wcToken struct {
	Text    string `json:"text"`
	Offsets struct {
		From int `json:"from"`
		To   int `json:"to"`
	} `json:"offsets"`
	P    float64 `json:"p"`
	TDTW int     `json:"t_dtw"`
}

type wcOut struct {
	Result struct {
		Language string `json:"language"`
	} `json:"result"`
	Transcription []wcSegment `json:"transcription"`
}

type wcSegment struct {
	Offsets struct {
		From int `json:"from"`
		To   int `json:"to"`
	} `json:"offsets"`
	Text   string    `json:"text"`
	Tokens []wcToken `json:"tokens"`
}

func (w *whisperCPP) Transcribe(ctx context.Context, wav, scratch, language string) (*Transcript, error) {
	if language == "" {
		language = "auto"
	}
	threads := whisperThreads()
	w.fallback = ""
	var b []byte
	engine := ModePerBatch
	if w.resident {
		out, err := w.decodeResident(ctx, wav, language, threads)
		switch {
		case err == nil:
			b, engine = out, ModeResident
		case errors.Is(err, ErrResidentUnavailable):
			w.fallback = err.Error()
		default:
			return nil, err
		}
	}
	if b == nil {
		out, err := w.decodeCLI(ctx, wav, scratch, language, threads)
		if err != nil {
			return nil, err
		}
		b = out
	}
	tr, err := parseWhisperJSON(b, dtwPreset(w.model) != "")
	if err != nil {
		return nil, err
	}
	tr.Engine = engine
	dur, _ := wavDuration(wav)
	var sil, quiet []span
	if w.t.Has(tools.FFmpeg) {
		sil, quiet, _ = pauses(ctx, w.t, wav, dur)
	}
	tr.Segments = snapToSilences(gateWhisper(tr.Segments, sil, quiet, dur), sil, snapPause)
	return tr, nil
}

// snapPause is the shortest pause word edges are moved out of (D244).
const snapPause = 0.25

// dtwPreset is whisper.cpp's alignment-heads preset for a model (D244): with
// it, token times come from lining the sound up with what was written
// (dynamic time warping over the model's attention) instead of the decoder's
// timestamps, which start the word after a pause up to a second early, inside
// the pause. "" for a model without one (its token times are used as before).
func dtwPreset(model string) string {
	name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(model), "ggml-"), ".bin")
	p := strings.ReplaceAll(name, "-", ".")
	switch p {
	case "tiny", "tiny.en", "base", "base.en", "small", "small.en", "medium", "medium.en", "large.v1", "large.v2", "large.v3", "large.v3.turbo":
		return p
	}
	return ""
}

// dtwArgs are the arguments that turn DTW token times on for a model (flash
// attention off: whisper.cpp fills them only without it).
func dtwArgs(model string) []string {
	if p := dtwPreset(model); p != "" {
		return []string{"-dtw", p, "-nfa"}
	}
	return nil
}

// decodeCLI is the original path: one whisper-cli process for the clip, which
// loads the model, writes its full JSON and exits.
func (w *whisperCPP) decodeCLI(ctx context.Context, wav, scratch, language string, threads int) ([]byte, error) {
	prefix := filepath.Join(scratch, "whisper")
	// -mc 0 avoids quote drift and end-of-file loops; no --vad (it breaks token
	// times) — hallucinated fillers are gated against detected speech instead.
	args := append([]string{"-m", w.model, "-f", wav, "-l", language, "-mc", "0", "-ojf", "-of", prefix, "-np", "-t", fmt.Sprint(threads)}, dtwArgs(w.model)...)
	if _, err := w.t.Run(ctx, tools.Spec{Tool: tools.Whisper, Args: args, Background: true}); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(prefix + ".json")
	if err != nil {
		return nil, fmt.Errorf("whisper.cpp produced no JSON: %w", err)
	}
	return b, nil
}

// fillers are what whisper.cpp writes over silence, room noise and music:
// thanks and goodbyes in the languages measured, compared without case,
// spaces or punctuation at their ends.
var fillers = map[string]bool{"thank you": true, "you": true, "thanks for watching": true, "bye": true,
	"gracias": true, "gracias por ver": true, "gracias por ver el video": true, "muchas gracias": true,
	"merci": true, "merci d'avoir regardé": true, "merci d'avoir regardé cette vidéo": true, "merci beaucoup": true,
	"vielen dank": true, "danke": true, "danke fürs zuschauen": true, "tschüss": true,
	"obrigado": true, "obrigada": true, "obrigado por assistir": true, "obrigada por assistir": true, "muito obrigado": true,
	"ご視聴ありがとうございました": true, "ありがとうございました": true, "おやすみなさい": true}

// subtitleCredits are the credits of subtitle files whisper.cpp learned from
// and writes over silence ("Untertitelung des ZDF, 2020", "Sous-titrage ST'
// 501", "Subtítulos realizados por la comunidad de Amara.org").
var subtitleCredits = regexp.MustCompile(`(?i)(amara\.org|untertitel|sous-titr|subt[íi]tul|legendas?\b|legendado|字幕|radio-canada|\bzdf\b)`)

// gateWhisper drops what whisper writes over silence: filler and credit
// segments that sit mostly on the recording's pauses (it prints "Thank you."
// over silence and music, and the like in other languages), anything that
// sits almost entirely on true quiet (-45 dB; whisper writes whole invented
// sentences there in other languages), and segments with no letters or
// digits at all ("..."); it clamps times to the audio duration.
func gateWhisper(segs []Segment, sil, quiet []span, dur float64) []Segment {
	share := func(s Segment, spans []span) float64 {
		length := s.To - s.From
		if length <= 0 {
			return 1
		}
		silent := 0.0
		for _, x := range spans {
			silent += math.Max(0, math.Min(s.To, x.to)-math.Max(s.From, x.from))
		}
		return silent / length
	}
	out := segs[:0]
	for _, s := range segs {
		if dur > 0 {
			s.To = math.Min(s.To, dur)
			for i := range s.Words {
				s.Words[i].From, s.Words[i].To = math.Min(s.Words[i].From, dur), math.Min(s.Words[i].To, dur)
			}
		}
		if !hasLetters(s.Text) || share(s, quiet) >= 0.9 {
			continue
		}
		if (fillers[fillerKey(s.Text)] || subtitleCredits.MatchString(s.Text)) && share(s, sil) > 0.7 {
			continue
		}
		out = append(out, s)
	}
	return out
}

func fillerKey(text string) string {
	return strings.TrimFunc(strings.ToLower(strings.TrimSpace(text)), func(r rune) bool { return unicode.IsPunct(r) || unicode.IsSpace(r) })
}

func hasLetters(text string) bool {
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// parseWhisperJSON reads whisper-cli's -ojf JSON. With useDTW, a word starts
// at the DTW time of its first token and ends where its last token ends, or
// where the next word starts when that is sooner (D244); a token without a
// DTW time keeps the decoder's.
func parseWhisperJSON(b []byte, useDTW bool) (*Transcript, error) {
	var out wcOut
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("parse whisper.cpp JSON: %w", err)
	}
	tr := &Transcript{Language: out.Result.Language}
	type ref struct{ seg, word int }
	var dtw []ref // words whose first token has a DTW time, in order
	for _, seg := range out.Transcription {
		text := strings.TrimSpace(seg.Text)
		if text == "" || isNonSpeech(text) {
			continue
		}
		s := Segment{From: ms(seg.Offsets.From), To: ms(seg.Offsets.To), Text: text}
		var cur *models.Word
		curDTW := -1
		flush := func() {
			if cur != nil && strings.TrimSpace(cur.Text) != "" {
				cur.Text = strings.TrimSpace(cur.Text)
				if useDTW && curDTW >= 0 {
					cur.From = float64(curDTW) / 100
					dtw = append(dtw, ref{len(tr.Segments), len(s.Words)})
				}
				s.Words = append(s.Words, *cur)
			}
			cur = nil
		}
		for _, tk := range seg.Tokens {
			if strings.HasPrefix(tk.Text, "[_") || strings.HasPrefix(tk.Text, "<|") {
				continue // special/timestamp tokens
			}
			from, to := ms(tk.Offsets.From), ms(tk.Offsets.To)
			// In a script written without spaces (Japanese, Chinese, Thai)
			// every token is a piece of its own: a word there is what can
			// be timed and cut (D244).
			startsWord := strings.HasPrefix(tk.Text, " ") || cur == nil || unspacedJoin(cur.Text, tk.Text)
			punct := isPunct(strings.TrimSpace(tk.Text))
			if startsWord && !punct {
				flush()
				cur = &models.Word{Text: tk.Text, From: from, To: to, Score: round3(tk.P)}
				curDTW = tk.TDTW
				continue
			}
			if cur == nil {
				continue
			}
			cur.Text += tk.Text
			if !punct && to > cur.To {
				cur.To = to
			}
			cur.Score = round3(math.Min(cur.Score, tk.P))
		}
		flush()
		tr.Segments = append(tr.Segments, s)
	}
	for i, r := range dtw {
		w := &tr.Segments[r.seg].Words[r.word]
		if i+1 < len(dtw) {
			if next := tr.Segments[dtw[i+1].seg].Words[dtw[i+1].word].From; next < w.To {
				w.To = math.Max(w.From, next)
			}
		}
		w.To = math.Max(w.To, w.From)
	}
	return tr, nil
}

// unspacedJoin reports whether a token joins a word in a script written
// without spaces (either side of the join).
func unspacedJoin(word, token string) bool {
	l, _ := utf8.DecodeLastRuneInString(word)
	r, _ := utf8.DecodeRuneInString(token)
	return l != utf8.RuneError && r != utf8.RuneError && (textalign.Unspaced(l) || textalign.Unspaced(r)) && !isPunct(strings.TrimSpace(token))
}

func ms(v int) float64 { return float64(v) / 1000 }

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

func isPunct(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsPunct(r) {
			return false
		}
	}
	return true
}

// isNonSpeech drops bracketed annotations like "[MUSIC]" or "(silence)".
func isNonSpeech(s string) bool {
	s = strings.TrimSpace(s)
	return (strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]")) || (strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")")) || s == "♪"
}
