//go:build voiceeval

package transcribe

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// TestMultilingual measures the multilingual path (whisper.cpp with the
// language each clip is spoken in, D243–D244) by D10's method: accuracy and
// dropped sentences against the text that was read, word timing around
// pauses (1.5 s of digital silence put into natural pauses, plus word edges
// against ffmpeg's silences), words over silence, noise and music, speed,
// and how well whisper's language detection tells the language from where
// the speech is. Run with -tags voiceeval after scripts/fetch-speech.sh:
//
//	EVAL_SPEECH=tests/fixtures/speech EVAL_LANGS=es go test -tags voiceeval -run TestMultilingual -timeout 60m ./internal/transcribe/
//
// EVAL_LANGS picks languages (comma-separated; "en" only listens for the
// language), EVAL_OUT receives the numbers as JSON. whisper-cli, sherpa-onnx
// and the models come from METARUSH_WHISPER_CLI, METARUSH_SHERPA_DIR and
// METARUSH_MODELS_DIR (or the usual places).
func TestMultilingual(t *testing.T) {
	dir := os.Getenv("EVAL_SPEECH")
	if dir == "" {
		t.Skip("EVAL_SPEECH not set (scripts/fetch-speech.sh)")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join("..", "..", dir)
	}
	want := map[string]bool{}
	for _, l := range strings.Split(os.Getenv("EVAL_LANGS"), ",") {
		if l = strings.TrimSpace(l); l != "" {
			want[l] = true
		}
	}
	ts := tools.New(config.Tools{WhisperCLI: os.Getenv("METARUSH_WHISPER_CLI"), SherpaDir: os.Getenv("METARUSH_SHERPA_DIR"), ModelsDir: os.Getenv("METARUSH_MODELS_DIR")})
	w, _ := newWhisperCPP(ts, "", false).(*whisperCPP)
	det := NewDetector(ts, "")
	if w == nil || det == nil {
		t.Skip("whisper.cpp with a multilingual model is not installed")
	}
	pk, _ := newParakeet(ts, "", false).(*parakeet)
	ctx := context.Background()
	rows := readManifest(t, filepath.Join(dir, "manifest.tsv"))
	report := mlReport{Model: w.Model(), Threads: whisperThreads(), At: time.Now().UTC()}
	langsSeen := map[string]bool{}
	for _, r := range rows {
		if len(want) > 0 && !want[r.lang] {
			continue
		}
		wav := filepath.Join(dir, r.id+".wav")
		if _, err := os.Stat(wav); err != nil {
			t.Fatalf("%s: %v (run scripts/fetch-speech.sh)", r.id, err)
		}
		dur, _ := wavDuration(wav)
		rec := mlRecording{ID: r.id, Language: r.lang, Seconds: round3(dur)}
		rec.Detection = measureDetection(ctx, t, det, wav, r.lang)
		if r.lang != "en" {
			ref := readReference(t, r.id)
			rec.Forced = measureTranscript(ctx, t, w, wav, r.lang, ref)
			rec.Auto = measureTranscript(ctx, t, w, wav, "auto", ref)
			rec.Pauses = measurePauses(ctx, t, w, wav, r.lang)
			if pk != nil {
				tr, err := pk.Transcribe(ctx, wav, t.TempDir(), "en")
				if err != nil {
					t.Fatalf("%s parakeet: %v", r.id, err)
				}
				s := score(ref, transcriptTokens(tr, r.lang), r.lang)
				rec.TodaysDefault = &s
			}
			langsSeen[r.lang] = true
		}
		report.Recordings = append(report.Recordings, rec)
		t.Logf("%s done", r.id)
	}
	for _, l := range sortedLangs(langsSeen) {
		report.NonSpeech = append(report.NonSpeech, measureNonSpeech(ctx, t, w, l)...)
	}
	printReport(t, report)
	if out := os.Getenv("EVAL_OUT"); out != "" {
		b, _ := json.MarshalIndent(report, "", "  ")
		if err := os.WriteFile(out, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

type mlReport struct {
	Model      string        `json:"model"`
	Threads    int           `json:"threads"`
	At         time.Time     `json:"at"`
	Recordings []mlRecording `json:"recordings"`
	NonSpeech  []mlNonSpeech `json:"nonSpeech"`
}

type mlRecording struct {
	ID            string      `json:"id"`
	Language      string      `json:"language"`
	Seconds       float64     `json:"seconds"`
	Detection     mlDetection `json:"detection"`
	Forced        *mlRun      `json:"forced,omitempty"`
	Auto          *mlRun      `json:"auto,omitempty"`
	Pauses        *mlPauses   `json:"pauses,omitempty"`
	TodaysDefault *mlScore    `json:"todaysDefault,omitempty"`
}

type mlRun struct {
	mlScore
	Language      string  `json:"languageReported"`
	Wall          float64 `json:"wall"`
	RTF           float64 `json:"rtf"`
	Pieces        int     `json:"pieces"`
	Segments      int     `json:"segments"`
	OnsetMAE      float64 `json:"onsetMAE"`
	OffsetMAE     float64 `json:"offsetMAE"`
	Spanning      int     `json:"wordsSpanningSilence"`
	SnapOnsetMAE  float64 `json:"tokenOnsetMAE"` // the decoder's token times, as before D244
	SnapOffsetMAE float64 `json:"tokenOffsetMAE"`
	SnapSpanning  int     `json:"tokenWordsSpanningSilence"`
	SilencesAt    float64 `json:"silenceThresholdDB"`
	SilenceN      int     `json:"silences"`
	OutsideText   string  `json:"outsideText,omitempty"`
	// Alone is each dropped sentence's stretch transcribed on its own.
	Alone []mlAlone `json:"alone,omitempty"`
}

type mlAlone struct {
	From, To  float64
	Uncovered float64 // seconds of sound there with no word in the long pass
	Recall    float64 // share of the sentence's words heard when the stretch is transcribed alone
	Heard     string
}

type mlScore struct {
	Unit        string   `json:"unit"` // word | character
	RefTokens   int      `json:"refTokens"`
	Sub         int      `json:"sub"`
	Del         int      `json:"del"`
	Ins         int      `json:"ins"`
	ErrorRate   float64  `json:"errorRate"`
	Outside     int      `json:"outside"` // hypothesis tokens before or after the read text (preamble, outro)
	Sentences   int      `json:"sentences"`
	Dropped     int      `json:"dropped"`
	DroppedText []string `json:"droppedText,omitempty"`
	LongestGap  int      `json:"longestDeletedRun"`
}

type mlPauses struct {
	Inserted   int       `json:"inserted"`
	Measured   int       `json:"measured"`
	GapTruth   float64   `json:"gapTruthMean"`
	GapHeard   float64   `json:"gapHeardMean"`
	EndErr     float64   `json:"endErrMean"`   // end of the word before − start of the pause (positive: into the pause)
	StartErr   float64   `json:"startErrMean"` // start of the word after − end of the pause (negative: inside the pause)
	WorstStart float64   `json:"worstStartErr"`
	Inside     int       `json:"wordsInsideInsertedSilence"`
	Detail     []mlPause `json:"detail"`
	// The same with the decoder's token times as before D244, those with
	// pause edges from the silence, and DTW times alone.
	TokenTimes *mlPauses `json:"tokenTimes,omitempty"`
	TokenEdges *mlPauses `json:"tokenEdges,omitempty"`
	DTWOnly    *mlPauses `json:"dtwOnly,omitempty"`
}

type mlPause struct {
	At, Truth, Heard, EndErr, StartErr float64
}

type mlDetection struct {
	Windows  []mlWindow `json:"windows"`
	Detected Heard      `json:"detected"`
	Wall     float64    `json:"wall"`
	Right    bool       `json:"right"`
}

type mlWindow struct {
	Where    string  `json:"where"`
	Length   float64 `json:"length"`
	Language string  `json:"language"`
	P        float64 `json:"p"`
}

type mlNonSpeech struct {
	Fixture  string `json:"fixture"`
	Language string `json:"language"`
	Raw      string `json:"raw"`
	Kept     string `json:"kept"`
}

type manifestRow struct{ id, lang string }

func readManifest(t *testing.T, path string) []manifestRow {
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("%v (run scripts/fetch-speech.sh)", err)
	}
	defer f.Close()
	var out []manifestRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Split(sc.Text(), "\t")
		if len(p) < 2 || p[0] == "id" {
			continue
		}
		out = append(out, manifestRow{p[0], p[1]})
	}
	return out
}

func readReference(t *testing.T, id string) string {
	b, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "truth", "speech", id+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ---------------------------------------------------------------- scoring ---

// tokens normalizes text the same way for reference and hypothesis: NFKC,
// lower case, hyphens to spaces, apostrophes and other punctuation and
// symbols removed; words split on spaces, or characters for Japanese.
func tokens(s, lang string) []string {
	s = strings.ToLower(norm.NFKC.String(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '-' || r == '‐' || r == '–' || r == '—':
			b.WriteRune(' ')
		case r == '\'' || r == '’' || r == 'ʼ':
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	if lang == "ja" {
		var out []string
		for _, r := range b.String() {
			if !unicode.IsSpace(r) {
				out = append(out, string(r))
			}
		}
		return out
	}
	return strings.Fields(b.String())
}

func transcriptTokens(tr *Transcript, lang string) []string {
	tk, _ := hypTokens(tr, lang)
	return tk
}

// hypTokens is the transcript's tokens with the time of the word each came from.
func hypTokens(tr *Transcript, lang string) ([]string, []tw) {
	var out []string
	var at []tw
	for _, s := range tr.Segments {
		for _, w := range s.Words {
			for _, k := range tokens(w.Text, lang) {
				out = append(out, k)
				at = append(at, tw{w.From, w.To})
			}
		}
	}
	return out, at
}

// alignment says, for each reference token, the hypothesis token it was
// matched to (-1 when deleted), and where each dropped sentence is.
type alignment struct {
	matched []int
	dropped [][2]int // reference token ranges [from, to)
	refTok  []string
}

var reSentence = regexp.MustCompile(`[^.!?;。！？]+[.!?;。！？]*`)

// score aligns the hypothesis to the reference with free hypothesis words
// before and after the read text (a reading's spoken preamble and outro
// aren't in the book) and counts substitutions, deletions and insertions,
// sentences that lost at least half their words, and the longest run of
// reference words lost in a row.
func score(ref string, hyp []string, lang string) mlScore {
	sc, _ := align(ref, hyp, lang)
	return sc
}

func align(ref string, hyp []string, lang string) (mlScore, alignment) {
	var refTok []string
	var sentOf []int
	var sentText []string
	for _, sent := range reSentence.FindAllString(ref, -1) {
		tk := tokens(sent, lang)
		if len(tk) == 0 {
			continue
		}
		sentText = append(sentText, strings.TrimSpace(sent))
		for range tk {
			sentOf = append(sentOf, len(sentText)-1)
		}
		refTok = append(refTok, tk...)
	}
	n, m := len(refTok), len(hyp)
	cost := make([][]int32, n+1)
	for i := range cost {
		cost[i] = make([]int32, m+1)
	}
	for i := 1; i <= n; i++ {
		cost[i][0] = int32(i)
	}
	// cost[0][j] = 0: hypothesis words before the read text are free.
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			sub := cost[i-1][j-1]
			if refTok[i-1] != hyp[j-1] {
				sub++
			}
			cost[i][j] = min(sub, cost[i-1][j]+1, cost[i][j-1]+1)
		}
	}
	endJ := 0
	for j := 0; j <= m; j++ {
		if cost[n][j] < cost[n][endJ] {
			endJ = j
		}
	}
	s := mlScore{Unit: "word", RefTokens: n, Sentences: len(sentText)}
	if lang == "ja" {
		s.Unit = "character"
	}
	s.Outside = m - endJ
	deleted := make([]bool, n)
	al := alignment{matched: make([]int, n), refTok: refTok}
	for k := range al.matched {
		al.matched[k] = -1
	}
	i, j := n, endJ
	for i > 0 {
		switch {
		case j > 0 && cost[i][j] == cost[i-1][j-1]+boolInt(refTok[i-1] != hyp[j-1]):
			if refTok[i-1] != hyp[j-1] {
				s.Sub++
			}
			al.matched[i-1] = j - 1
			i, j = i-1, j-1
		case cost[i][j] == cost[i-1][j]+1:
			s.Del++
			deleted[i-1] = true
			i--
		default:
			s.Ins++
			j--
		}
	}
	s.Outside += j // free words before the text
	if n > 0 {
		s.ErrorRate = round3(float64(s.Sub+s.Del+s.Ins) / float64(n))
	}
	lost := make([]int, len(sentText))
	size := make([]int, len(sentText))
	run := 0
	for k := range refTok {
		size[sentOf[k]]++
		if deleted[k] {
			lost[sentOf[k]]++
			run++
			s.LongestGap = max(s.LongestGap, run)
		} else {
			run = 0
		}
	}
	first := make([]int, len(sentText))
	for k := len(refTok) - 1; k >= 0; k-- {
		first[sentOf[k]] = k
	}
	for k := range sentText {
		if size[k] > 0 && 2*lost[k] >= size[k] && size[k] >= 3 {
			s.Dropped++
			s.DroppedText = append(s.DroppedText, sentText[k])
			al.dropped = append(al.dropped, [2]int{first[k], first[k] + size[k]})
		}
	}
	return s, al
}

func boolInt(b bool) int32 {
	if b {
		return 1
	}
	return 0
}

// ------------------------------------------------------------ transcripts ---

func measureTranscript(ctx context.Context, t *testing.T, w *whisperCPP, wav, lang, ref string) *mlRun {
	dur, _ := wavDuration(wav)
	start := time.Now()
	tr, err := w.Transcribe(ctx, wav, t.TempDir(), lang)
	if err != nil {
		t.Fatalf("%s (%s): %v", wav, lang, err)
	}
	wall := time.Since(start).Seconds()
	want := lang
	if want == "auto" {
		want = strings.SplitN(filepath.Base(wav), "-", 2)[0]
	}
	hyp, hypAt := hypTokens(tr, want)
	sc, al := align(ref, hyp, want)
	r := &mlRun{mlScore: sc, Language: tr.Language, Wall: round3(wall), RTF: round3(wall / dur), Segments: len(tr.Segments)}
	for _, s := range tr.Segments {
		r.Pieces += len(s.Words)
	}
	words := allWords(tr)
	sil, db := silencesFor(ctx, t, w.t, wav, dur)
	r.SilencesAt, r.SilenceN = db, len(sil)
	r.OnsetMAE, r.OffsetMAE, r.Spanning = edgeErrors(words, sil)
	psil, err := (&parakeet{t: w.t}).silences(ctx, wav, dur)
	if err != nil {
		t.Fatal(err)
	}
	if lang != "auto" {
		r.SnapOnsetMAE, r.SnapOffsetMAE, r.SnapSpanning = edgeErrors(tokenWords(t, w, wav, lang, psil), sil)
	}
	if r.Outside > 0 {
		r.OutsideText = outsideText(tr)
	}
	if lang != "auto" {
		r.Alone = checkDropped(ctx, t, w, wav, want, dur, al, hypAt, words, sil)
	}
	return r
}

// checkDropped transcribes the stretch of each dropped sentence on its own
// (from the last word heard before it to the first after it, ±0.5 s): a
// sentence heard there was dropped by the long pass; one not heard either
// way is probably read differently from the text. Uncovered is how much of
// the stretch ffmpeg counts as sound while the long pass has no word there.
func checkDropped(ctx context.Context, t *testing.T, w *whisperCPP, wav, lang string, dur float64, al alignment, hypAt []tw, words []tw, sil []span) []mlAlone {
	var out []mlAlone
	for _, d := range al.dropped {
		from, to := 0.0, dur
		for k := d[0] - 1; k >= 0; k-- {
			if h := al.matched[k]; h >= 0 {
				from = hypAt[h].to
				break
			}
		}
		for k := d[1]; k < len(al.matched); k++ {
			if h := al.matched[k]; h >= 0 {
				to = hypAt[h].from
				break
			}
		}
		from, to = math.Max(0, from-0.5), math.Min(dur, to+0.5)
		a := mlAlone{From: round3(from), To: round3(to)}
		a.Uncovered = round3(uncovered(from, to, words, sil))
		if to-from > 0.5 && to-from <= 90 {
			cut := filepath.Join(t.TempDir(), "alone.wav")
			if _, err := w.t.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Args: []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
				"-ss", ffSec(from), "-t", ffSec(to - from), "-i", wav, "-c:a", "pcm_s16le", cut}}); err != nil {
				t.Fatal(err)
			}
			tr, err := w.Transcribe(ctx, cut, t.TempDir(), lang)
			if err != nil {
				t.Fatal(err)
			}
			heard := map[string]int{}
			for _, k := range transcriptTokens(tr, lang) {
				heard[k]++
			}
			hit := 0
			for _, k := range al.refTok[d[0]:d[1]] {
				if heard[k] > 0 {
					heard[k]--
					hit++
				}
			}
			a.Recall = round3(float64(hit) / float64(d[1]-d[0]))
			a.Heard = segText(tr)
		}
		out = append(out, a)
	}
	return out
}

// uncovered is the sound (not silence) in [from, to] that no word covers.
func uncovered(from, to float64, words []tw, sil []span) float64 {
	const step = 0.05
	n := 0
	for x := from; x < to; x += step {
		quiet := false
		for _, s := range sil {
			if x >= s.from && x < s.to {
				quiet = true
				break
			}
		}
		if quiet {
			continue
		}
		covered := false
		for _, w := range words {
			if x >= w.from && x < w.to {
				covered = true
				break
			}
		}
		if !covered {
			n++
		}
	}
	return float64(n) * step
}

type tw struct{ from, to float64 }

func allWords(tr *Transcript) []tw {
	var out []tw
	for _, s := range tr.Segments {
		for _, w := range s.Words {
			out = append(out, tw{w.From, w.To})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].from < out[j].from })
	return out
}

// edgeErrors is D10's timing against ffmpeg's silences: for every silence the
// end of the word before it and the start of the word after it, and the
// words whose span crosses a silence of at least 0.3 s.
func edgeErrors(words []tw, sil []span) (onset, offset float64, spanning int) {
	var on, off []float64
	for _, s := range sil {
		mid := (s.from + s.to) / 2
		var before, after *tw
		for i := range words {
			m := (words[i].from + words[i].to) / 2
			if m < mid {
				before = &words[i]
			} else if after == nil {
				after = &words[i]
			}
		}
		if before != nil && after != nil && s.from-before.to < 3 && after.from-s.to < 3 {
			off = append(off, math.Abs(before.to-s.from))
			on = append(on, math.Abs(after.from-s.to))
		}
		if s.to-s.from >= 0.3 {
			for _, w := range words {
				if w.from < s.from-0.05 && w.to > s.to+0.05 {
					spanning++
				}
			}
		}
	}
	return round3(mean(on)), round3(mean(off)), spanning
}

// silencesFor finds a reading's pauses with D10's silencedetect settings
// (-45 dB, 0.2 s), raising the threshold for recordings whose quiet is louder
// than that until at least one pause per 20 s shows.
func silencesFor(ctx context.Context, t *testing.T, ts *tools.Set, wav string, dur float64) ([]span, float64) {
	for _, db := range []float64{-45, -40, -35, -30} {
		res, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Args: []string{"-nostdin", "-hide_banner", "-i", wav,
			"-af", fmt.Sprintf("silencedetect=noise=%gdB:d=0.2", db), "-f", "null", "-"}})
		if err != nil {
			t.Fatal(err)
		}
		var out []span
		start := -1.0
		for _, l := range strings.Split(string(res.Stderr), "\n") {
			if m := reSilStart.FindStringSubmatch(l); m != nil {
				fmt.Sscan(m[1], &start)
				start = math.Max(0, start)
			} else if m := reSilEnd.FindStringSubmatch(l); m != nil && start >= 0 {
				var end float64
				fmt.Sscan(m[1], &end)
				out = append(out, span{start, end})
				start = -1
			}
		}
		if float64(len(out)) >= dur/20 || db == -30 {
			return out, db
		}
	}
	return nil, 0
}

// outsideText shows the transcript's start and end, where a reading's
// preamble and outro are.
func outsideText(tr *Transcript) string {
	var all []string
	for _, s := range tr.Segments {
		all = append(all, s.Text)
	}
	text := strings.Join(all, " ")
	if len([]rune(text)) > 160 {
		r := []rune(text)
		return string(r[:80]) + " … " + string(r[len(r)-80:])
	}
	return text
}

// ----------------------------------------------------------------- pauses ---

// measurePauses puts 1.5 s of digital silence into up to eight natural pauses
// (at least 0.3 s, 15 s apart) and checks where the words around them land.
func measurePauses(ctx context.Context, t *testing.T, w *whisperCPP, wav, lang string) *mlPauses {
	dur, _ := wavDuration(wav)
	sil, _ := silencesFor(ctx, t, w.t, wav, dur)
	var picked []span
	last := -100.0
	for _, s := range sil {
		if s.to-s.from >= 0.3 && s.from > 5 && s.to < dur-5 && s.from-last >= 15 {
			picked = append(picked, s)
			last = s.from
			if len(picked) == 8 {
				break
			}
		}
	}
	const gap = 1.5
	out := filepath.Join(t.TempDir(), "paused.wav")
	var mids []float64
	for _, s := range picked {
		mids = append(mids, (s.from+s.to)/2)
	}
	if err := insertSilence(wav, out, mids, gap); err != nil {
		t.Fatal(err)
	}
	// What MetaRush does now: DTW times, pause edges from the silence (D244).
	tr, err := w.Transcribe(ctx, out, t.TempDir(), lang)
	if err != nil {
		t.Fatal(err)
	}
	p := pauseErrors(allWords(tr), picked, mids, gap)
	odur, _ := wavDuration(out)
	osil, err := (&parakeet{t: w.t}).silences(ctx, out, odur)
	if err != nil {
		t.Fatal(err)
	}
	tok := tokenWords(t, w, out, lang, osil)
	p.TokenTimes = pauseErrors(tok, picked, mids, gap)
	p.TokenEdges = pauseErrors(allWords(&Transcript{Segments: snapToSilences([]Segment{asSegment(tok)}, osil, 0.25)}), picked, mids, gap)
	p.DTWOnly = pauseErrors(dtwWords(t, w, out, lang), picked, mids, gap)
	return p
}

// tokenWords is whisper.cpp as MetaRush ran it before D244: the decoder's
// token times, the filler gate, no DTW and no pause edges.
func tokenWords(t *testing.T, w *whisperCPP, wav, lang string, sil []span) []tw {
	prefix := filepath.Join(t.TempDir(), "tok")
	args := []string{"-m", w.model, "-f", wav, "-l", lang, "-mc", "0", "-ojf", "-of", prefix, "-np", "-t", fmt.Sprint(whisperThreads())}
	if _, err := w.t.Run(context.Background(), tools.Spec{Tool: tools.Whisper, Args: args, Background: true}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(prefix + ".json")
	if err != nil {
		t.Fatal(err)
	}
	tr, err := parseWhisperJSON(b, false)
	if err != nil {
		t.Fatal(err)
	}
	dur, _ := wavDuration(wav)
	tr.Segments = gateWhisper(tr.Segments, sil, nil, dur)
	return allWords(tr)
}

func asSegment(ws []tw) Segment {
	var s Segment
	for _, w := range ws {
		s.Words = append(s.Words, models.Word{From: w.from, To: w.to})
	}
	return s
}

// dtwWords transcribes with whisper.cpp's DTW token times (-dtw
// large.v3.turbo -nfa; D10 tried them on English) and gives each word the DTW
// time of its first token, ending where the next word starts (at most where
// its last token ends).
func dtwWords(t *testing.T, w *whisperCPP, wav, lang string) []tw {
	prefix := filepath.Join(t.TempDir(), "dtw")
	args := []string{"-m", w.model, "-f", wav, "-l", lang, "-mc", "0", "-dtw", "large.v3.turbo", "-nfa", "-ojf", "-of", prefix, "-np", "-t", fmt.Sprint(whisperThreads())}
	if _, err := w.t.Run(context.Background(), tools.Spec{Tool: tools.Whisper, Args: args, Background: true}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(prefix + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var out wcOut
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	type word struct{ start, end float64 }
	var ws []word
	for _, seg := range out.Transcription {
		if text := strings.TrimSpace(seg.Text); text == "" || isNonSpeech(text) {
			continue
		}
		open := false
		for _, tk := range seg.Tokens {
			if strings.HasPrefix(tk.Text, "[_") || strings.HasPrefix(tk.Text, "<|") {
				continue
			}
			punct := isPunct(strings.TrimSpace(tk.Text))
			if (strings.HasPrefix(tk.Text, " ") || !open) && !punct {
				ws = append(ws, word{float64(tk.TDTW) / 100, ms(tk.Offsets.To)})
				open = true
				continue
			}
			if open && !punct {
				ws[len(ws)-1].end = ms(tk.Offsets.To)
			}
		}
	}
	var res []tw
	for i, x := range ws {
		end := x.end
		if i+1 < len(ws) && ws[i+1].start < end {
			end = math.Max(x.start, ws[i+1].start)
		}
		res = append(res, tw{x.start, end})
	}
	sort.Slice(res, func(i, j int) bool { return res[i].from < res[j].from })
	return res
}

func pauseErrors(words []tw, picked []span, mids []float64, gap float64) *mlPauses {
	p := &mlPauses{Inserted: len(picked)}
	var gaps, truths, ends, starts []float64
	for k, s := range picked {
		shift := gap * float64(k)
		from, to := s.from+shift, s.to+shift+gap // the whole pause in the new file
		dFrom := mids[k] + shift                 // the inserted digital silence
		dTo := dFrom + gap
		centre := (dFrom + dTo) / 2
		var before, after *tw
		for i := range words {
			m := (words[i].from + words[i].to) / 2
			if m < centre {
				before = &words[i]
			} else if after == nil {
				after = &words[i]
			}
			if math.Min(words[i].to, dTo)-math.Max(words[i].from, dFrom) > 0.2 {
				p.Inside++ // more than 0.2 s of a word inside the digital silence
			}
		}
		if before == nil || after == nil {
			continue
		}
		e := pauseRow(from, to, before, after)
		p.Detail = append(p.Detail, e)
		gaps, truths, ends, starts = append(gaps, e.Heard), append(truths, e.Truth), append(ends, e.EndErr), append(starts, e.StartErr)
		p.WorstStart = math.Min(p.WorstStart, e.StartErr)
	}
	p.Measured = len(p.Detail)
	p.GapHeard, p.GapTruth, p.EndErr, p.StartErr = round3(mean(gaps)), round3(mean(truths)), round3(mean(ends)), round3(mean(starts))
	p.WorstStart = round3(p.WorstStart)
	return p
}

func pauseRow(from, to float64, before, after *tw) mlPause {
	return mlPause{At: round3(from), Truth: round3(to - from), Heard: round3(after.from - before.to), EndErr: round3(before.to - from), StartErr: round3(after.from - to)}
}

// insertSilence copies a 16 kHz mono s16 WAV with `gap` seconds of zeros at
// each of the given times.
func insertSilence(in, out string, at []float64, gap float64) error {
	b, err := os.ReadFile(in)
	if err != nil {
		return err
	}
	pcm := b[44:]
	zeros := make([]byte, int(gap*16000)*2)
	var body []byte
	prev := 0
	for _, a := range at {
		k := int(a*16000) * 2
		body = append(body, pcm[prev:k]...)
		body = append(body, zeros...)
		prev = k
	}
	body = append(body, pcm[prev:]...)
	return os.WriteFile(out, wavFile(body), 0o600)
}

func wavFile(pcm []byte) []byte {
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(pcm)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1)
	binary.LittleEndian.PutUint16(h[22:], 1)
	binary.LittleEndian.PutUint32(h[24:], 16000)
	binary.LittleEndian.PutUint32(h[28:], 32000)
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(pcm)))
	return append(h, pcm...)
}

// -------------------------------------------------------------- detection ---

// measureDetection asks whisper's language detection about stretches of 5,
// 10 and 30 s from where speech starts and from a quarter, half and three
// quarters into the speech, then runs the detection MetaRush uses.
func measureDetection(ctx context.Context, t *testing.T, det *Detector, wav, lang string) mlDetection {
	dur, _ := wavDuration(wav)
	sil, err := (&parakeet{t: det.t}).silences(ctx, wav, dur)
	if err != nil {
		t.Fatal(err)
	}
	speech := speechSpans(dur, sil)
	total := 0.0
	for _, s := range speech {
		total += s.to - s.from
	}
	at := func(frac float64) float64 {
		k := total * frac
		for _, s := range speech {
			if k <= s.to-s.from {
				return s.from + k
			}
			k -= s.to - s.from
		}
		return speech[len(speech)-1].to
	}
	var d mlDetection
	scratch := t.TempDir()
	for _, where := range []struct {
		name string
		frac float64
	}{{"start", 0}, {"quarter", 0.25}, {"half", 0.5}, {"three quarters", 0.75}} {
		from := at(where.frac)
		for _, length := range []float64{5, 10, 30} {
			if from+length > dur {
				continue
			}
			l, p, err := det.listen(ctx, wav, filepath.Join(scratch, "w.wav"), span{from, from + length})
			if err != nil {
				t.Fatal(err)
			}
			d.Windows = append(d.Windows, mlWindow{Where: where.name, Length: length, Language: l, P: p})
		}
	}
	start := time.Now()
	h, err := det.Detect(ctx, wav, scratch)
	if err != nil {
		t.Fatal(err)
	}
	d.Wall, d.Detected, d.Right = round3(time.Since(start).Seconds()), h, h.Language == lang
	return d
}

// -------------------------------------------------------------- non-speech --

// measureNonSpeech transcribes silence, quiet room noise and a chord bed with
// the language set, before and after MetaRush's filler gate.
func measureNonSpeech(ctx context.Context, t *testing.T, w *whisperCPP, lang string) []mlNonSpeech {
	dir := t.TempDir()
	chord := "0.07*(sin(2*PI*F1*t)+sin(2*PI*F2*t)+sin(2*PI*F3*t))*(0.6+0.4*sin(2*PI*2*t))"
	pick := func(a, b, c, d string) string {
		return fmt.Sprintf("if(lt(mod(t,8),2),%s,if(lt(mod(t,8),4),%s,if(lt(mod(t,8),6),%s,%s)))", a, b, c, d)
	}
	chord = strings.NewReplacer("F1", pick("261.63", "220", "174.61", "196"), "F2", pick("329.63", "261.63", "220", "246.94"), "F3", pick("392", "329.63", "261.63", "293.66")).Replace(chord)
	fixtures := []struct{ name, src, length string }{
		{"silence (8 s, digital)", "anullsrc=r=16000:cl=mono", "8"},
		{"room noise (10 s, pink, quiet)", "anoisesrc=color=pink:amplitude=0.01:r=16000:seed=7", "10"},
		{"chord bed (30 s)", "aevalsrc='" + chord + "':s=16000", "30"},
	}
	var out []mlNonSpeech
	for i, f := range fixtures {
		wav := filepath.Join(dir, fmt.Sprintf("ns%d.wav", i))
		if _, err := w.t.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", f.src, "-t", f.length, "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-fflags", "+bitexact", "-flags:a", "+bitexact", "-map_metadata", "-1", wav}}); err != nil {
			t.Fatal(err)
		}
		b, err := w.decodeCLI(ctx, wav, t.TempDir(), lang, whisperThreads())
		if err != nil {
			t.Fatal(err)
		}
		raw, err := parseWhisperJSON(b, false)
		if err != nil {
			t.Fatal(err)
		}
		kept, err := w.Transcribe(ctx, wav, t.TempDir(), lang)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, mlNonSpeech{Fixture: f.name, Language: lang, Raw: segText(raw), Kept: segText(kept)})
	}
	return out
}

func segText(tr *Transcript) string {
	var parts []string
	for _, s := range tr.Segments {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, " ")
}

// ----------------------------------------------------------------- report ---

func printReport(t *testing.T, r mlReport) {
	t.Logf("model %s, %d threads", r.Model, r.Threads)
	t.Logf("| Recording | Audio | Unit | Error rate (S/D/I) | Dropped sentences | Longest run lost | Outside the text | Pieces / segments | Wall (RTF) | -l auto: language, error rate | Parakeet v2 today |")
	for _, x := range r.Recordings {
		if x.Forced == nil {
			continue
		}
		f, a := x.Forced, x.Auto
		pk := "–"
		if x.TodaysDefault != nil {
			pk = pct(x.TodaysDefault.ErrorRate)
		}
		t.Logf("| %s | %.0f s | %s | %s (%d/%d/%d) | %d of %d | %d | %d | %d / %d | %.1f s (%.3f) | %s, %s | %s |", x.ID, x.Seconds, f.Unit, pct(f.ErrorRate), f.Sub, f.Del, f.Ins,
			f.Dropped, f.Sentences, f.LongestGap, f.Outside, f.Pieces, f.Segments, f.Wall, f.RTF, a.Language, pct(a.ErrorRate), pk)
		for i, d := range f.DroppedText {
			if i < len(f.Alone) {
				al := f.Alone[i]
				t.Logf("    dropped at %.1f–%.1f s (%.1f s of sound with no word; alone: %.0f%% of its words): %s", al.From, al.To, al.Uncovered, 100*al.Recall, d)
			} else {
				t.Logf("    dropped: %s", d)
			}
		}
		if f.OutsideText != "" {
			t.Logf("    text: %s", f.OutsideText)
		}
	}
	t.Logf("| Recording | Pauses put in | Truth gap | Heard gap | End error | Start error | Worst start | Words in the inserted silence | Onset / offset MAE vs silences | Words spanning a silence | Threshold |")
	for _, x := range r.Recordings {
		if x.Pauses == nil {
			continue
		}
		p, f := x.Pauses, x.Forced
		t.Logf("| %s now | %d (%d measured) | %.2f | %.2f | %+.2f | %+.2f | %+.2f | %d | %.2f / %.2f | %d | %g dB, %d silences |", x.ID, p.Inserted, p.Measured, p.GapTruth, p.GapHeard, p.EndErr, p.StartErr, p.WorstStart, p.Inside, f.OnsetMAE, f.OffsetMAE, f.Spanning, f.SilencesAt, f.SilenceN)
		t.Logf("| %s before (whole reading) | | | | | | | | %.2f / %.2f | %d | |", x.ID, f.SnapOnsetMAE, f.SnapOffsetMAE, f.SnapSpanning)
		for _, v := range []struct {
			name string
			q    *mlPauses
		}{{"token times (before)", p.TokenTimes}, {"token times + pause edges", p.TokenEdges}, {"DTW alone", p.DTWOnly}} {
			if q := v.q; q != nil {
				t.Logf("| %s %s | | %.2f | %.2f | %+.2f | %+.2f | %+.2f | %d | | | |", x.ID, v.name, q.GapTruth, q.GapHeard, q.EndErr, q.StartErr, q.WorstStart, q.Inside)
			}
		}
	}
	t.Logf("| Recording | Detected (score, listened) | right | wall | windows: where length → language p |")
	for _, x := range r.Recordings {
		var ws []string
		for _, w := range x.Detection.Windows {
			ws = append(ws, fmt.Sprintf("%s %gs→%s %.2f", w.Where, w.Length, w.Language, w.P))
		}
		d := x.Detection.Detected
		t.Logf("| %s | %s (%.2f, %.0f s) | %v | %.1f s | %s |", x.ID, d.Language, d.Score, d.Listened, x.Detection.Right, x.Detection.Wall, strings.Join(ws, "; "))
	}
	for _, n := range r.NonSpeech {
		t.Logf("non-speech %s, -l %s: raw %q, kept %q", n.Fixture, n.Language, n.Raw, n.Kept)
	}
}

func pct(v float64) string { return fmt.Sprintf("%.1f%%", 100*v) }

func mean(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func sortedLangs(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestMultilingualDTW compares word edges against ffmpeg's silences over
// whole readings (D10's onset and offset errors, and words across a pause)
// for whisper.cpp's token times and its DTW times, each as they are and
// snapped to pauses; and asks the language detection about silence, room
// noise and music. Same environment as TestMultilingual.
func TestMultilingualDTW(t *testing.T) {
	dir := os.Getenv("EVAL_SPEECH")
	if dir == "" {
		t.Skip("EVAL_SPEECH not set (scripts/fetch-speech.sh)")
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join("..", "..", dir)
	}
	want := map[string]bool{}
	for _, l := range strings.Split(os.Getenv("EVAL_LANGS"), ",") {
		if l = strings.TrimSpace(l); l != "" {
			want[l] = true
		}
	}
	ts := tools.New(config.Tools{WhisperCLI: os.Getenv("METARUSH_WHISPER_CLI"), ModelsDir: os.Getenv("METARUSH_MODELS_DIR")})
	w, _ := newWhisperCPP(ts, "", false).(*whisperCPP)
	det := NewDetector(ts, "")
	if w == nil || det == nil {
		t.Skip("whisper.cpp with a multilingual model is not installed")
	}
	ctx := context.Background()
	type row struct {
		ID                             string
		Plain, PlainSnap, DTW, DTWSnap [3]float64 // onset MAE, offset MAE, words across a pause
		Silences                       int
		Threshold                      float64
	}
	var rows []row
	for _, r := range readManifest(t, filepath.Join(dir, "manifest.tsv")) {
		if len(want) > 0 && !want[r.lang] {
			continue
		}
		wav := filepath.Join(dir, r.id+".wav")
		dur, _ := wavDuration(wav)
		sil, db := silencesFor(ctx, t, ts, wav, dur)
		psil, err := (&parakeet{t: ts}).silences(ctx, wav, dur)
		if err != nil {
			t.Fatal(err)
		}
		edges := func(ws []tw) [3]float64 {
			on, off, sp := edgeErrors(ws, sil)
			return [3]float64{on, off, float64(sp)}
		}
		x := row{ID: r.id, Silences: len(sil), Threshold: db}
		tok := tokenWords(t, w, wav, r.lang, psil)
		x.Plain = edges(tok)
		x.PlainSnap = edges(allWords(&Transcript{Segments: snapToSilences([]Segment{asSegment(tok)}, psil, 0.25)}))
		dw := dtwWords(t, w, wav, r.lang)
		x.DTW = edges(dw)
		x.DTWSnap = edges(allWords(&Transcript{Segments: snapToSilences([]Segment{asSegment(dw)}, psil, 0.25)}))
		rows = append(rows, x)
		t.Logf("%s done", r.id)
	}
	t.Logf("| Recording | Silences | Token times (before): onset / offset MAE, words across a pause | token times + pause edges | DTW alone | DTW + pause edges (now) |")
	f := func(v [3]float64) string { return fmt.Sprintf("%.2f / %.2f, %.0f", v[0], v[1], v[2]) }
	for _, x := range rows {
		t.Logf("| %s | %d (%g dB) | %s | %s | %s | %s |", x.ID, x.Silences, x.Threshold, f(x.Plain), f(x.PlainSnap), f(x.DTW), f(x.DTWSnap))
	}
	// What the detection makes of no speech at all.
	scratch := t.TempDir()
	for i, src := range []struct{ name, lavfi, length string }{
		{"silence", "anullsrc=r=16000:cl=mono", "8"},
		{"room noise", "anoisesrc=color=pink:amplitude=0.01:r=16000:seed=7", "10"},
		{"chord bed", "aevalsrc='0.07*(sin(2*PI*261.63*t)+sin(2*PI*329.63*t)+sin(2*PI*392*t))*(0.6+0.4*sin(2*PI*2*t))':s=16000", "30"},
	} {
		wav := filepath.Join(scratch, fmt.Sprintf("ns%d.wav", i))
		if _, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-nostdin", "-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", src.lavfi, "-t", src.length, "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wav}}); err != nil {
			t.Fatal(err)
		}
		h, err := det.Detect(ctx, wav, scratch)
		if err != nil {
			t.Fatal(err)
		}
		l, p, _ := det.listen(ctx, wav, filepath.Join(scratch, "w.wav"), span{0, mustDur(wav)})
		t.Logf("detection on %s: Detect %q (score %.2f, listened %.0f s); whole file %s p=%.2f", src.name, h.Language, h.Score, h.Listened, l, p)
	}
}

func mustDur(wav string) float64 {
	d, _ := wavDuration(wav)
	return d
}
