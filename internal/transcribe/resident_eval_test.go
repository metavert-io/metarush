//go:build voiceeval

package transcribe

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// TestResidentMatchesCLI is the measurement harness for D93 (run with
// -tags voiceeval): every WAV in EVAL_WAVS (comma-separated 16 kHz mono files)
// is transcribed EVAL_ROUNDS times (default 3) by the CLI path (one
// sherpa-onnx-offline process per batch, ffmpeg cuts per chunk) and by the
// resident model, interleaved; the words must be identical. It prints the
// median wall time of each path, one cold resident run (model loaded first),
// and the peak memory of each; EVAL_OUT, when set, receives the numbers as JSON.
func TestResidentMatchesCLI(t *testing.T) {
	list := os.Getenv("EVAL_WAVS")
	if list == "" {
		t.Skip("EVAL_WAVS not set")
	}
	rounds := 3
	if n, err := strconv.Atoi(os.Getenv("EVAL_ROUNDS")); err == nil && n > 0 {
		rounds = n
	}
	ts := tools.New(config.Tools{})
	cli, _ := newParakeet(ts, "", false).(*parakeet)
	res, _ := newParakeet(ts, "", true).(*parakeet)
	if cli == nil || res == nil {
		t.Skip("parakeet not installed")
	}
	if res.Mode() != ModeResident {
		t.Fatalf("the resident model is not available here (library %q)", res.lib)
	}
	ctx := context.Background()
	words := func(p *parakeet, wav string) ([]models.Word, time.Duration, string) {
		start := time.Now()
		tr, err := p.Transcribe(ctx, wav, t.TempDir(), "en")
		if err != nil {
			t.Fatalf("%s: %v", wav, err)
		}
		var ws []models.Word
		for _, s := range tr.Segments {
			ws = append(ws, s.Words...)
		}
		return ws, time.Since(start), tr.Engine
	}
	type row struct {
		File        string    `json:"file"`
		Seconds     float64   `json:"seconds"`
		Words       int       `json:"words"`
		Identical   bool      `json:"identical"`
		CLI         []float64 `json:"cli"`
		Resident    []float64 `json:"resident"`
		Cold        float64   `json:"residentCold"`
		CLIMedian   float64   `json:"cliMedian"`
		ResMedian   float64   `json:"residentMedian"`
		Speedup     float64   `json:"speedup"`
		ColdSpeedup float64   `json:"coldSpeedup"`
	}
	var rows []row
	for _, wav := range strings.Split(list, ",") {
		dur, _ := wavDuration(wav)
		r := row{File: filepath.Base(wav), Seconds: dur, Identical: true}
		// One cold resident run: the model is loaded inside the measurement.
		ReleaseResident()
		ref, d, eng := words(res, wav)
		if eng != ModeResident {
			t.Fatalf("%s ran %s, not resident (%s)", wav, eng, res.Fallback())
		}
		r.Cold, r.Words = d.Seconds(), len(ref)
		for i := 0; i < rounds; i++ {
			cw, cd, ceng := words(cli, wav)
			if ceng != ModePerBatch {
				t.Fatalf("CLI path ran %s", ceng)
			}
			rw, rd, _ := words(res, wav)
			r.CLI, r.Resident = append(r.CLI, cd.Seconds()), append(r.Resident, rd.Seconds())
			if !reflect.DeepEqual(cw, ref) || !reflect.DeepEqual(rw, ref) {
				r.Identical = false
				t.Errorf("%s round %d: transcripts differ (cli %d words, resident %d, first resident %d)\n%s", wav, i+1, len(cw), len(rw), len(ref), firstDiff(cw, rw))
			}
		}
		r.CLIMedian, r.ResMedian = median(r.CLI), median(r.Resident)
		r.Speedup, r.ColdSpeedup = r.CLIMedian/r.ResMedian, r.CLIMedian/r.Cold
		rows = append(rows, r)
	}
	var serverPeak int64
	for _, st := range ResidentStates() {
		serverPeak = max(serverPeak, st.PeakMemory)
	}
	ReleaseResident()
	var self, kids syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &self)
	_ = syscall.Getrusage(syscall.RUSAGE_CHILDREN, &kids)
	t.Logf("%-18s %8s %6s %10s %10s %10s %8s %8s %s", "file", "audio s", "words", "CLI s", "resident s", "cold s", "speedup", "cold x", "identical")
	var sumCLI, sumRes float64
	for _, r := range rows {
		sumCLI += r.CLIMedian
		sumRes += r.ResMedian
		t.Logf("%-18s %8.1f %6d %10.2f %10.2f %10.2f %7.2fx %7.2fx %v", r.File, r.Seconds, r.Words, r.CLIMedian, r.ResMedian, r.Cold, r.Speedup, r.ColdSpeedup, r.Identical)
	}
	t.Logf("all files: CLI %.2f s, resident %.2f s (%.2fx); peak memory: model server %.2f GB, largest child process %.2f GB, this process %.2f GB",
		sumCLI, sumRes, sumCLI/sumRes, float64(serverPeak)/1e9, maxRSSGB(kids.Maxrss), maxRSSGB(self.Maxrss))
	if out := os.Getenv("EVAL_OUT"); out != "" {
		b, _ := json.MarshalIndent(map[string]any{"rows": rows, "rounds": rounds, "threads": parakeetThreads(), "selfMaxRSSGB": maxRSSGB(self.Maxrss),
			"childMaxRSSGB": maxRSSGB(kids.Maxrss), "serverPeakGB": float64(serverPeak) / 1e9, "at": time.Now().UTC()}, "", "  ")
		if err := os.WriteFile(out, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// maxRSSGB converts getrusage's ru_maxrss (bytes on macOS, KiB on Linux).
func maxRSSGB(v int64) float64 {
	if runtime.GOOS == "darwin" {
		return float64(v) / 1e9
	}
	return float64(v) * 1024 / 1e9
}

func median(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s) == 0 {
		return 0
	}
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func firstDiff(a, b []models.Word) string {
	for i := 0; i < min(len(a), len(b)); i++ {
		if a[i] != b[i] {
			return fmt.Sprintf("word %d: %+v vs %+v", i, a[i], b[i])
		}
	}
	return fmt.Sprintf("lengths %d vs %d", len(a), len(b))
}
