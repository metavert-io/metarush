//go:build voiceeval

package sound

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// TestStudioSpeedAcrossRecordings measures D121's studio change (run with
// -tags voiceeval): EVAL_RECORDINGS recordings (default 3) of EVAL_SECONDS
// seconds (default 8) of a speech-like voice, made clearer with studio quality
// one recording at a time (one model load each, as before) and all at once
// (one model load), EVAL_ROUNDS times each (default 2), interleaved. The
// components come from METARUSH_TEST_SOUND_HOME (METARUSH_TEST_SOUND_INSTALL=1
// sets them up there). It prints the median wall times and the speedup.
func TestStudioSpeedAcrossRecordings(t *testing.T) {
	home := os.Getenv("METARUSH_TEST_SOUND_HOME")
	if home == "" {
		t.Skip("METARUSH_TEST_SOUND_HOME not set")
	}
	num := func(name string, def int) int {
		if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
			return n
		}
		return def
	}
	recs, secs, rounds := num("EVAL_RECORDINGS", 3), num("EVAL_SECONDS", 8), num("EVAL_ROUNDS", 2)
	ctx := context.Background()
	c := &Components{Home: home, Tools: tools.New(config.Tools{})}
	none := func(d, n int, msg string) error { t.Logf("setup %d/%d %s", d, n, msg); return nil }
	if !c.CleanInstalled() || !c.studioStatus().Installed {
		if os.Getenv("METARUSH_TEST_SOUND_INSTALL") != "1" {
			t.Skipf("clean and studio are not set up in %s (METARUSH_TEST_SOUND_INSTALL=1 sets them up)", home)
		}
		if err := c.InstallClean(ctx, none); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if _, err := c.InstallStudio(ctx, none); err != nil {
			t.Fatal(err)
		}
		t.Logf("studio setup took %s", time.Since(start).Round(time.Second))
	}
	st := c.studioStatus()
	eng := &Engine{Tools: c.Tools, Comp: c, Scratch: t.TempDir()}
	var parts []StudioPart
	for i := 0; i < recs; i++ {
		p := filepath.Join(t.TempDir(), fmt.Sprintf("rec-%d.wav", i))
		voice := testVoice(float64(secs), WetRate)
		for j := range voice { // a different voice level for each recording
			voice[j] *= float32(0.6 + 0.2*float64(i))
		}
		if err := WriteWAV(p, &Audio{Rate: WetRate, Channels: 1, Data: voice}, true); err != nil {
			t.Fatal(err)
		}
		parts = append(parts, StudioPart{Src: p, From: 0, To: float64(secs)})
	}
	var each, once []float64
	for r := 0; r < rounds; r++ {
		start := time.Now()
		for _, p := range parts {
			if _, err := eng.StudioMany(ctx, []StudioPart{p}, nil); err != nil {
				t.Fatal(err)
			}
		}
		each = append(each, time.Since(start).Seconds())
		start = time.Now()
		ws, err := eng.StudioMany(ctx, parts, nil)
		if err != nil {
			t.Fatal(err)
		}
		once = append(once, time.Since(start).Seconds())
		if len(ws) != recs {
			t.Fatalf("%d results for %d recordings", len(ws), recs)
		}
	}
	med := func(v []float64) float64 {
		s := append([]float64(nil), v...)
		sort.Float64s(s)
		if len(s)%2 == 1 {
			return s[len(s)/2]
		}
		return (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	a, b := med(each), med(once)
	t.Logf("studio on %s (speed %.2f s of work per s at setup): %d recordings of %d s: one at a time %.1f s (%v), all at once %.1f s (%v): %.2fx; about %.1f s saved per recording after the first",
		st.Device, st.Speed, recs, secs, a, each, b, once, a/b, (a-b)/float64(max(1, recs-1)))
}
