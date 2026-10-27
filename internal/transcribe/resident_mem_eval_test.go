//go:build voiceeval

package transcribe

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// TestResidentMemory transcribes EVAL_WAVS with the resident model only and
// logs the model server's peak memory and restarts after each file
// (measurement aid for MaxServerMemory).
func TestResidentMemory(t *testing.T) {
	list := os.Getenv("EVAL_WAVS")
	if list == "" {
		t.Skip("EVAL_WAVS not set")
	}
	res, _ := newParakeet(tools.New(config.Tools{}), "", true).(*parakeet)
	if res == nil {
		t.Skip("parakeet not installed")
	}
	defer ReleaseResident()
	for _, wav := range strings.Split(list, ",") {
		start := time.Now()
		tr, err := res.Transcribe(context.Background(), wav, t.TempDir(), "en")
		if err != nil {
			t.Fatal(err)
		}
		st := ResidentStates()
		if len(st) != 1 {
			t.Fatalf("servers %+v", st)
		}
		t.Logf("%s: %.2f s (%s), server peak %.2f GB, %d start(s), %d batch(es), running %v",
			wav[strings.LastIndex(wav, "/")+1:], time.Since(start).Seconds(), tr.Engine, float64(st[0].PeakMemory)/1e9, st[0].Starts, st[0].Batches, st[0].Running)
	}
}
