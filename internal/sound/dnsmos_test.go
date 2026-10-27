package sound

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The score needs onnxruntime (from the sherpa-onnx install) and the DNSMOS
// model; tests skip without them. METARUSH_DNSMOS_LIB / METARUSH_DNSMOS_MODEL
// point at them; METARUSH_DNSMOS_FILES (comma-separated 16 kHz WAVs) prints
// scores for a comparison with Microsoft's dnsmos_local.py.
func testScorer(t *testing.T) *Scorer {
	t.Helper()
	lib, model := os.Getenv("METARUSH_DNSMOS_LIB"), os.Getenv("METARUSH_DNSMOS_MODEL")
	if lib == "" {
		home, _ := os.UserHomeDir()
		lib = filepath.Join(home, ".local", "opt", "sherpa-onnx", "current", "lib", "libonnxruntime.dylib")
	}
	if model == "" {
		home, _ := os.UserHomeDir()
		model = filepath.Join(home, ".local", "share", "metarush-models", "dnsmos", "sig_bak_ovr.onnx")
	}
	for _, p := range []string{lib, model} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("voice clarity score not installed here (%s)", p)
		}
	}
	s, err := OpenScorer(lib, model)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDNSMOSFiles(t *testing.T) {
	files := os.Getenv("METARUSH_DNSMOS_FILES")
	if files == "" {
		t.Skip("set METARUSH_DNSMOS_FILES")
	}
	s := testScorer(t)
	for _, f := range strings.Split(files, ",") {
		a, err := ReadWAV(f)
		if err != nil {
			t.Fatal(err)
		}
		c, err := s.Score(a.Mono())
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: OVRL %.4f SIG %.4f BAK %.4f (%d windows)", filepath.Base(f), c.Overall, c.Speech, c.Background, c.Windows)
	}
}
