//go:build voiceeval

package transcribe

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// TestWhisperResidentMatchesCLI is the measurement harness for D121 (run with
// -tags voiceeval): every WAV in EVAL_WAVS (comma-separated 16 kHz mono files)
// is transcribed once by the loaded model started cold (the model load is in
// the measurement), then EVAL_ROUNDS times (default 3) by whisper-cli (one
// process per clip) and by the loaded model, interleaved; the transcripts
// must be identical. whisper-cli, whisper-server and the models come from
// METARUSH_WHISPER_CLI, METARUSH_WHISPER_SERVER and METARUSH_MODELS_DIR (or
// the usual places); EVAL_LANGUAGE is the language (default auto). It prints
// the median wall time of each way and whisper-server's peak memory;
// EVAL_OUT, when set, receives the numbers as JSON.
func TestWhisperResidentMatchesCLI(t *testing.T) {
	list := os.Getenv("EVAL_WAVS")
	if list == "" {
		t.Skip("EVAL_WAVS not set")
	}
	rounds := 3
	if n, err := strconv.Atoi(os.Getenv("EVAL_ROUNDS")); err == nil && n > 0 {
		rounds = n
	}
	language := os.Getenv("EVAL_LANGUAGE")
	ts := tools.New(config.Tools{WhisperCLI: os.Getenv("METARUSH_WHISPER_CLI"), WhisperServer: os.Getenv("METARUSH_WHISPER_SERVER"), ModelsDir: os.Getenv("METARUSH_MODELS_DIR")})
	cli, _ := newWhisperCPP(ts, "", false).(*whisperCPP)
	res, _ := newWhisperCPP(ts, "", true).(*whisperCPP)
	if cli == nil || res == nil {
		t.Skip("whisper.cpp not installed")
	}
	if res.Mode() != ModeResident {
		_, err := whisperServerFor(ts, res.model, whisperThreads())
		t.Fatalf("the whisper model cannot stay loaded here: %v", err)
	}
	ctx := context.Background()
	run := func(p *whisperCPP, wav string) (*Transcript, time.Duration) {
		start := time.Now()
		tr, err := p.Transcribe(ctx, wav, t.TempDir(), language)
		if err != nil {
			t.Fatalf("%s: %v", wav, err)
		}
		return tr, time.Since(start)
	}
	type row struct {
		File        string    `json:"file"`
		Seconds     float64   `json:"seconds"`
		Words       int       `json:"words"`
		Language    string    `json:"language"`
		Identical   bool      `json:"identical"`
		CLI         []float64 `json:"cli"`
		Resident    []float64 `json:"resident"`
		Cold        float64   `json:"residentCold"`
		CLIMedian   float64   `json:"cliMedian"`
		ResMedian   float64   `json:"residentMedian"`
		Speedup     float64   `json:"speedup"`
		ColdSpeedup float64   `json:"coldSpeedup"`
	}
	same := func(a, b *Transcript) bool {
		x, y := *a, *b
		x.Engine, y.Engine = "", ""
		return reflect.DeepEqual(x, y)
	}
	var rows []row
	for _, wav := range strings.Split(list, ",") {
		dur, _ := wavDuration(wav)
		r := row{File: filepath.Base(wav), Seconds: dur, Identical: true}
		ReleaseResident() // the cold run loads the model inside the measurement
		ref, d := run(res, wav)
		if ref.Engine != ModeResident {
			t.Fatalf("%s ran %s, not resident (%s)", wav, ref.Engine, res.Fallback())
		}
		r.Cold, r.Language = d.Seconds(), ref.Language
		for _, s := range ref.Segments {
			r.Words += len(s.Words)
		}
		for i := 0; i < rounds; i++ {
			ct, cd := run(cli, wav)
			if ct.Engine != ModePerBatch {
				t.Fatalf("whisper-cli path ran %s", ct.Engine)
			}
			rt, rd := run(res, wav)
			if rt.Engine != ModeResident {
				t.Fatalf("%s round %d ran %s (%s)", wav, i+1, rt.Engine, res.Fallback())
			}
			r.CLI, r.Resident = append(r.CLI, cd.Seconds()), append(r.Resident, rd.Seconds())
			if !same(ct, ref) || !same(rt, ref) {
				r.Identical = false
				t.Errorf("%s round %d: transcripts differ\n%s", wav, i+1, firstDiffTranscript(ct, rt))
			}
		}
		r.CLIMedian, r.ResMedian = median(r.CLI), median(r.Resident)
		r.Speedup, r.ColdSpeedup = r.CLIMedian/r.ResMedian, r.CLIMedian/r.Cold
		rows = append(rows, r)
	}
	var peak int64
	for _, st := range ResidentStates() {
		if st.Kind == EngineWhisper {
			peak = max(peak, st.PeakMemory)
		}
	}
	ReleaseResident()
	t.Logf("%-18s %8s %6s %4s %8s %10s %8s %8s %8s %s", "file", "audio s", "words", "lang", "CLI s", "loaded s", "cold s", "speedup", "cold x", "identical")
	var sumCLI, sumRes, sumAudio float64
	for _, r := range rows {
		sumCLI, sumRes, sumAudio = sumCLI+r.CLIMedian, sumRes+r.ResMedian, sumAudio+r.Seconds
		t.Logf("%-18s %8.1f %6d %4s %8.2f %10.2f %8.2f %7.2fx %7.2fx %v", r.File, r.Seconds, r.Words, r.Language, r.CLIMedian, r.ResMedian, r.Cold, r.Speedup, r.ColdSpeedup, r.Identical)
	}
	t.Logf("all %d files (%.1f min of audio): whisper-cli %.2f s, loaded model %.2f s (%.2fx); whisper-server peak memory %.2f GB; model %s, %d threads",
		len(rows), sumAudio/60, sumCLI, sumRes, sumCLI/sumRes, float64(peak)/1e9, res.Model(), whisperThreads())
	if out := os.Getenv("EVAL_OUT"); out != "" {
		b, _ := json.MarshalIndent(map[string]any{"rows": rows, "rounds": rounds, "threads": whisperThreads(), "model": res.Model(),
			"serverPeakGB": float64(peak) / 1e9, "cliTotal": sumCLI, "residentTotal": sumRes, "at": time.Now().UTC()}, "", "  ")
		if err := os.WriteFile(out, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func firstDiffTranscript(a, b *Transcript) string {
	if a.Language != b.Language || len(a.Segments) != len(b.Segments) {
		return "language " + a.Language + " vs " + b.Language + ", segments " + strconv.Itoa(len(a.Segments)) + " vs " + strconv.Itoa(len(b.Segments))
	}
	for i := range a.Segments {
		if !reflect.DeepEqual(a.Segments[i], b.Segments[i]) {
			ja, _ := json.Marshal(a.Segments[i])
			jb, _ := json.Marshal(b.Segments[i])
			return "segment " + strconv.Itoa(i) + ":\n" + string(ja) + "\n" + string(jb)
		}
	}
	return "same segments"
}
