package transcribe

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/voice"
)

// parakeet runs NVIDIA Parakeet TDT 0.6B v2 (int8) through sherpa-onnx on the CPU,
// following the contract measured on a development Mac: silence-aligned
// chunks of ≤30 s, decoded ≤16 at a time, and SentencePiece word assembly.
// English only. Speakers come from the provider-independent Diarizer.
//
// The model is kept in memory (D93): the sherpa-onnx C library next to the CLI
// is loaded with purego, the recognizer stays loaded between batches and jobs
// (released after ResidentIdle), and chunks are cut from the WAV in memory.
// Each batch is the same DecodeStreams call the CLI makes, with the same
// samples and settings, so transcripts are the CLI's. Without the library, with
// a library version whose C structs MetaRush does not know, or with
// transcription.resident: false, it runs the CLI once per batch as before.
type parakeet struct {
	t   *tools.Set
	dir string
	// lib is the sherpa-onnx C library next to the CLI ("" when absent).
	lib string
	// resident keeps the model in memory (transcription.resident, default on).
	resident bool
	// fallback says why the last transcription ran the CLI instead ("" when
	// the resident model was used, or never tried).
	fallback string
}

// ParakeetModelDir is the model folder name under <models>/parakeet.
const ParakeetModelDir = "sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8"

func newParakeet(t *tools.Set, model string, resident bool) Provider {
	if !t.Has(tools.Sherpa) || !t.Has(tools.FFmpeg) {
		return nil
	}
	dir := filepath.Join(t.ModelsDir(), "parakeet", ParakeetModelDir)
	if model != "" && filepath.IsAbs(model) {
		dir = model
	}
	for _, f := range []string{"encoder.int8.onnx", "decoder.int8.onnx", "joiner.int8.onnx", "tokens.txt"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return nil
		}
	}
	return &parakeet{t: t, dir: dir, lib: voice.Library(t.Path(tools.Sherpa)), resident: resident}
}

func (p *parakeet) Name() string  { return "parakeet" }
func (p *parakeet) Model() string { return "tdt-0.6b-v2-int8" }

// Mode says how the model runs: ModeResident when it can be kept in memory on
// this machine, else ModePerBatch (one CLI process per batch).
func (p *parakeet) Mode() string {
	if !p.resident {
		return ModePerBatch
	}
	s, err := serverFor(p.t, p.lib, p.dir, parakeetThreads())
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

// wavSamples is the number of samples in a 16 kHz mono 16-bit WAV's data chunk.
func wavSamples(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return 0, fmt.Errorf("%s is not a WAV file", path)
	}
	for {
		var ch [8]byte
		if _, err := io.ReadFull(f, ch[:]); err != nil {
			return 0, fmt.Errorf("%s has no audio data", path)
		}
		size := int64(binary.LittleEndian.Uint32(ch[4:]))
		if string(ch[0:4]) == "data" {
			st, err := f.Stat()
			if err != nil {
				return 0, err
			}
			here, _ := f.Seek(0, io.SeekCurrent)
			return int(min(size, st.Size()-here) / 2), nil // the same bound voice.ReadWAV applies
		}
		if _, err := f.Seek(size+size%2, io.SeekCurrent); err != nil {
			return 0, err
		}
	}
}

// Fallback is why the last transcription ran the CLI instead of the resident
// model ("" when it did not).
func (p *parakeet) Fallback() string { return p.fallback }

// parakeetThreads is the decoder's thread count (two thirds of the cores, at
// least two), the same for both paths.
func parakeetThreads() int {
	return max(2, runtime.NumCPU()*2/3)
}

type span struct{ from, to float64 }

var (
	reSilStart = regexp.MustCompile(`silence_start:\s*(-?[\d.]+)`)
	reSilEnd   = regexp.MustCompile(`silence_end:\s*([\d.]+)`)
	reDiar     = regexp.MustCompile(`^(\d+\.\d+) -- (\d+\.\d+) (speaker_\d+)$`)
)

func wavDuration(path string) (float64, error) {
	st, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return float64(st.Size()-44) / (16000 * 2), nil // 16 kHz mono s16 (ExtractAudioWAV)
}

func (p *parakeet) silences(ctx context.Context, wav string, dur float64) ([]span, error) {
	return silencesAt(ctx, p.t, wav, dur, -45)
}

// pauses finds a recording's pauses for word edges (D244): ffmpeg's silences
// at -45 dB, or at -40 or -35 dB when the room is louder than that and fewer
// than one pause in 20 seconds shows; base is what -45 dB found (true quiet).
func pauses(ctx context.Context, t *tools.Set, wav string, dur float64) (pause, base []span, err error) {
	for _, db := range []float64{-45, -40, -35} {
		sil, err := silencesAt(ctx, t, wav, dur, db)
		if err != nil {
			return nil, nil, err
		}
		if db == -45 {
			base = sil
		}
		n := 0
		for _, s := range sil {
			if s.to-s.from >= snapPause {
				n++
			}
		}
		if float64(n) >= dur/20 || db == -35 {
			return sil, base, nil
		}
	}
	return base, base, nil
}

func silencesAt(ctx context.Context, t *tools.Set, wav string, dur, db float64) ([]span, error) {
	res, err := t.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-nostdin", "-hide_banner", "-i", wav, "-af", fmt.Sprintf("silencedetect=noise=%gdB:d=0.2", db), "-f", "null", "-"}, Background: true})
	if err != nil {
		return nil, err
	}
	var out []span
	start := -1.0
	for _, l := range strings.Split(string(res.Stderr), "\n") {
		if m := reSilStart.FindStringSubmatch(l); m != nil {
			start, _ = strconv.ParseFloat(m[1], 64)
			start = math.Max(0, start)
		} else if m := reSilEnd.FindStringSubmatch(l); m != nil && start >= 0 {
			end, _ := strconv.ParseFloat(m[1], 64)
			out = append(out, span{start, end})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, span{start, dur})
	}
	return out, nil
}

// chunkPlan cuts at the last silence midpoint within 30 s, else hard-cuts at 30 s.
func chunkPlan(dur float64, sil []span) []span {
	var mids []float64
	for _, s := range sil {
		mids = append(mids, (s.from+s.to)/2)
	}
	var out []span
	start := 0.0
	for start < dur-0.01 {
		if dur-start <= 30 {
			out = append(out, span{start, dur})
			break
		}
		cut := -1.0
		for _, m := range mids {
			if m > start+1 && m <= start+30 {
				cut = m
			}
		}
		if cut < 0 {
			cut = start + 30
		}
		out = append(out, span{start, cut})
		start = cut
	}
	return out
}

type sherpaLine struct {
	Text       string    `json:"text"`
	Timestamps []float64 `json:"timestamps"`
	Durations  []float64 `json:"durations"`
	Tokens     []string  `json:"tokens"`
	LogProbs   []float64 `json:"ys_log_probs"`
}

func (p *parakeet) Transcribe(ctx context.Context, wav, scratch, language string) (*Transcript, error) {
	return p.transcribe(ctx, wav, scratch, language, nil)
}

// TranscribeHinted transcribes with the spelling lists' words as hotwords
// (D147): beam search with a moderate boost for them (2.0; measured: every
// listed name came out as listed, timing and speed nearly unchanged, while 3
// and more made noise into names). A clip with hints decodes on the CLI path;
// the model kept in memory stays on greedy decoding, so clips without hints
// transcribe exactly as before.
func (p *parakeet) TranscribeHinted(ctx context.Context, wav, scratch, language string, hints []string) (*Transcript, error) {
	return p.transcribe(ctx, wav, scratch, language, hints)
}

// hotwordScore is the boost hint words get (see TranscribeHinted).
const hotwordScore = 2.0

// hotwordLines are hint words as sherpa-onnx's hotwords file takes them: one
// per line, no colon (a colon starts a per-line score), at most 200.
func hotwordLines(hints []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range hints {
		h = strings.Join(strings.Fields(strings.ReplaceAll(h, ":", " ")), " ")
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		out = append(out, h)
		if len(out) == 200 {
			break
		}
	}
	return out
}

// hintArgs writes the hotwords file and the BPE vocabulary sherpa-onnx needs
// to encode them (the model folder ships only tokens.txt: each piece scored by
// minus its ID, the merge order sentencepiece writes into .vocab files) and
// returns the decoding flags.
func (p *parakeet) hintArgs(scratch string, hints []string) ([]string, int, error) {
	lines := hotwordLines(hints)
	if len(lines) == 0 {
		return nil, 0, nil
	}
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return nil, 0, err
	}
	toks, err := os.ReadFile(filepath.Join(p.dir, "tokens.txt"))
	if err != nil {
		return nil, 0, err
	}
	var vocab strings.Builder
	for _, l := range strings.Split(strings.TrimRight(string(toks), "\n"), "\n") {
		i := strings.LastIndexByte(l, ' ')
		if i <= 0 {
			continue
		}
		piece, id := l[:i], l[i+1:]
		n, err := strconv.Atoi(strings.TrimSpace(id))
		if err != nil || piece == "<blk>" {
			continue
		}
		fmt.Fprintf(&vocab, "%s %s\n", piece, strconv.FormatFloat(-float64(n), 'f', 1, 64))
	}
	vf, hf := filepath.Join(scratch, "bpe.vocab"), filepath.Join(scratch, "hotwords.txt")
	if err := os.WriteFile(vf, []byte(vocab.String()), 0o600); err != nil {
		return nil, 0, err
	}
	if err := os.WriteFile(hf, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		return nil, 0, err
	}
	return []string{"--decoding-method=modified_beam_search", "--max-active-paths=4", "--modeling-unit=bpe", "--bpe-vocab=" + vf,
		"--hotwords-file=" + hf, "--hotwords-score=" + strconv.FormatFloat(hotwordScore, 'f', 1, 64)}, len(lines), nil
}

func (p *parakeet) transcribe(ctx context.Context, wav, scratch, language string, hints []string) (*Transcript, error) {
	if language != "" && language != "auto" && !strings.HasPrefix(language, "en") {
		return nil, fmt.Errorf("the built-in transcriber only understands English; for %q, switch transcription to whisper.cpp in config.yaml (transcription.provider: whispercpp)", language)
	}
	dur, err := wavDuration(wav)
	if err != nil {
		return nil, err
	}
	sil, err := p.silences(ctx, wav, dur)
	if err != nil {
		return nil, err
	}
	plan := chunkPlan(dur, sil)
	threads := parakeetThreads()
	p.fallback = ""
	extra, n, err := p.hintArgs(filepath.Join(scratch, "hints"), hints)
	if err != nil {
		return nil, err
	}
	if p.resident && n == 0 {
		words, err := p.decodeResident(ctx, wav, plan, threads)
		if err == nil {
			return &Transcript{Language: "en", Segments: segmentWords(words, dur), Engine: ModeResident}, nil
		}
		if !errors.Is(err, ErrResidentUnavailable) {
			return nil, err
		}
		p.fallback = err.Error()
	}
	words, err := p.decodeCLI(ctx, wav, scratch, plan, threads, extra...)
	if err != nil {
		return nil, err
	}
	return &Transcript{Language: "en", Segments: segmentWords(words, dur), Engine: ModePerBatch, Hints: n}, nil
}

// batchSize is how many chunks one decode takes (the bake-off measured 16 as
// the best speed for its memory: about 4.8 GB at the peak with 30 s chunks).
const batchSize = 16

// decodeResident decodes the chunks with the model kept in memory by the
// model server. The chunks are the very samples ffmpeg would cut
// (chunkSamples), so each batch decodes exactly as the CLI decodes its files.
func (p *parakeet) decodeResident(ctx context.Context, wav string, plan []span, threads int) ([]models.Word, error) {
	s, err := serverFor(p.t, p.lib, p.dir, threads)
	if err != nil {
		return nil, err
	}
	n, err := wavSamples(wav)
	if err != nil {
		return nil, err
	}
	var words []models.Word
	for b := 0; b < len(plan); b += batchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch := plan[b:min(b+batchSize, len(plan))]
		ranges := make([][2]int, len(batch))
		for i, c := range batch {
			lo, hi := chunkSamples(c, n)
			ranges[i] = [2]int{lo, hi}
		}
		lines, err := s.decode(ctx, wav, ranges)
		if err != nil {
			return nil, err
		}
		for i, l := range lines {
			var sl sherpaLine
			if err := json.Unmarshal([]byte(l), &sl); err != nil {
				return nil, fmt.Errorf("parse sherpa-onnx output: %w", err)
			}
			words = append(words, assembleWords(sl, batch[i].from)...)
		}
	}
	return words, nil
}

// chunkSamples is the sample range [lo, hi) that `ffmpeg -ss <from> -to <to>`
// (both printed with %.3f, as the CLI path passes them) cuts from a 16 kHz
// WAV of n samples: ffmpeg trims audio sample-accurately, and a millisecond is
// exactly 16 samples.
func chunkSamples(c span, n int) (lo, hi int) {
	ms := func(t float64) int {
		v, _ := strconv.ParseFloat(fmt.Sprintf("%.3f", t), 64)
		return int(math.Round(v * 1000))
	}
	lo, hi = ms(c.from)*16, ms(c.to)*16
	lo, hi = max(0, min(lo, n)), max(0, min(hi, n))
	return lo, max(lo, hi)
}

// decodeCLI is the original path: ffmpeg cuts each chunk into a file and one
// sherpa-onnx-offline process decodes each batch of files.
func (p *parakeet) decodeCLI(ctx context.Context, wav, scratch string, plan []span, threads int, extra ...string) ([]models.Word, error) {
	dir := filepath.Join(scratch, "chunks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	files := make([]string, len(plan))
	for i, c := range plan {
		files[i] = filepath.Join(dir, fmt.Sprintf("chunk_%04d.wav", i))
		if _, err := p.t.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-nostdin", "-loglevel", "error", "-y", "-i", wav, "-ss", fmt.Sprintf("%.3f", c.from), "-to", fmt.Sprintf("%.3f", c.to), "-c:a", "pcm_s16le", files[i]}}); err != nil {
			return nil, err
		}
	}
	var words []models.Word
	for b := 0; b < len(files); b += batchSize {
		batch := files[b:min(b+batchSize, len(files))]
		args := []string{"--encoder=" + filepath.Join(p.dir, "encoder.int8.onnx"), "--decoder=" + filepath.Join(p.dir, "decoder.int8.onnx"),
			"--joiner=" + filepath.Join(p.dir, "joiner.int8.onnx"), "--tokens=" + filepath.Join(p.dir, "tokens.txt"),
			"--model-type=nemo_transducer", fmt.Sprintf("--num-threads=%d", threads)}
		args = append(append(args, extra...), batch...)
		res, err := p.t.Run(ctx, tools.Spec{Tool: tools.Sherpa, Args: args, Background: true})
		if err != nil {
			return nil, err
		}
		var lines []sherpaLine
		sc := bufio.NewScanner(strings.NewReader(string(res.Stdout)))
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			l := strings.TrimSpace(sc.Text())
			if !strings.HasPrefix(l, "{") {
				continue
			}
			var sl sherpaLine
			if err := json.Unmarshal([]byte(l), &sl); err != nil {
				return nil, fmt.Errorf("parse sherpa-onnx output: %w", err)
			}
			lines = append(lines, sl)
		}
		if len(lines) != len(batch) {
			return nil, fmt.Errorf("sherpa-onnx returned %d results for %d chunks", len(lines), len(batch))
		}
		for i, sl := range lines {
			words = append(words, assembleWords(sl, plan[b+i].from)...)
		}
	}
	return words, nil
}

// assembleWords applies the bake-off's word rules to one chunk's tokens.
func assembleWords(sl sherpaLine, offset float64) []models.Word {
	var out []models.Word
	var cur *models.Word
	var lps []float64
	newWord := true
	flush := func() {
		if cur != nil && strings.TrimSpace(cur.Text) != "" {
			if len(lps) > 0 {
				sum := 0.0
				for _, v := range lps {
					sum += v
				}
				cur.Score = round3(math.Exp(sum / float64(len(lps))))
			}
			cur.From, cur.To = round3(cur.From), round3(cur.To)
			out = append(out, *cur)
		}
		cur, lps = nil, nil
	}
	for i, tk := range sl.Tokens {
		if i >= len(sl.Timestamps) {
			break
		}
		start := sl.Timestamps[i] + offset
		end := start
		if i < len(sl.Durations) {
			end += sl.Durations[i]
		}
		lp := 0.0
		if i < len(sl.LogProbs) {
			lp = sl.LogProbs[i]
		}
		if tk == " " { // marks "next token starts a new word"
			newWord = true
			continue
		}
		bare := strings.TrimSpace(tk)
		if isPunct(bare) {
			if cur != nil {
				cur.Text += bare // punctuation does not extend the end time
			}
			continue
		}
		if strings.HasPrefix(tk, " ") || newWord || cur == nil {
			flush()
			cur = &models.Word{Text: bare, From: start, To: end}
			lps = []float64{lp}
			newWord = false
			continue
		}
		cur.Text += tk
		cur.To = end
		lps = append(lps, lp)
	}
	flush()
	return out
}

// segmentWords groups words into caption-friendly segments: split at sentence
// ends and pauses ≥0.8 s; at most ~25 words. AssignSpeakers later splits them
// where the speaker changes.
func segmentWords(words []models.Word, dur float64) []Segment {
	var out []Segment
	var cur *Segment
	flush := func() {
		if cur != nil && len(cur.Words) > 0 {
			texts := make([]string, len(cur.Words))
			for i, w := range cur.Words {
				texts[i] = w.Text
			}
			cur.Text = strings.Join(texts, " ")
			cur.From, cur.To = cur.Words[0].From, math.Min(cur.Words[len(cur.Words)-1].To, dur)
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, w := range words {
		if cur != nil {
			prev := cur.Words[len(cur.Words)-1]
			if w.From-prev.To >= 0.8 || len(cur.Words) >= 25 || endsSentence(prev.Text) {
				flush()
			}
		}
		if cur == nil {
			cur = &Segment{}
		}
		w.To = math.Min(w.To, dur)
		cur.Words = append(cur.Words, w)
	}
	flush()
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

func endsSentence(w string) bool {
	return strings.HasSuffix(w, ".") || strings.HasSuffix(w, "?") || strings.HasSuffix(w, "!")
}
