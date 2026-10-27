package conversation

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// truth is tests/fixtures/truth/conversation.json.
type truth struct {
	Length  float64 `json:"length_s"`
	Delay   float64 `json:"delay_s"`
	Members []struct {
		File  string  `json:"file"`
		Name  string  `json:"name"`
		Start float64 `json:"start_s"`
		Drift float64 `json:"drift_ppm"`
		Use   string  `json:"use"`
	} `json:"members"`
	Zoom struct {
		Folder   string  `json:"folder"`
		SamDelay float64 `json:"sam_delay_s"`
	} `json:"zoom"`
	Long struct {
		Files    []string `json:"files"`
		Length   float64  `json:"length_s"`
		SamStart float64  `json:"sam_start_s"`
		SamDrift float64  `json:"sam_drift_ppm"`
	} `json:"long"`
	Dropout []float64 `json:"sam_dropout_s"`
	Freeze  []float64 `json:"sam_freeze_s"`
	Turns   []struct {
		Speaker string  `json:"speaker"`
		Kind    string  `json:"kind"`
		Start   float64 `json:"start_s"`
		End     float64 `json:"end_s"`
		Text    string  `json:"text"`
	} `json:"turns"`
}

func repoRoot(t testing.TB) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// fixtures makes the generated call (macOS: it needs `say`) and reads its truth.
func fixtures(t testing.TB) (string, truth) {
	t.Helper()
	root := repoRoot(t)
	gen := filepath.Join(root, "tests", "fixtures", "gen", "conversation")
	if _, err := os.Stat(filepath.Join(gen, "long", "sam.wav")); err != nil {
		if _, err := exec.LookPath("say"); err != nil {
			t.Skip("the conversation fixtures need macOS `say`")
		}
		if out, err := exec.Command(filepath.Join(root, "scripts", "make-conversation-fixtures.sh")).CombinedOutput(); err != nil {
			t.Fatalf("making fixtures: %v\n%s", err, out)
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "tests", "fixtures", "truth", "conversation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tr truth
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatal(err)
	}
	return gen, tr
}

func testTools(t testing.TB) *tools.Set {
	t.Helper()
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	return ts
}

func loadSound(t testing.TB, ts *tools.Set, path string) *sound {
	t.Helper()
	s, err := decodeSound(context.Background(), ts, path, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const frame = 1.0 / 30

// Two people's own recordings, each with the other picked up from the
// speakers: the offset comes out within a frame, and the call's delay too.
func TestSyncTwoWay(t *testing.T) {
	ts := testTools(t)
	gen, tr := fixtures(t)
	j := loadSound(t, ts, filepath.Join(gen, "maya.mov"))
	s := loadSound(t, ts, filepath.Join(gen, "sam.mov"))
	r := align(j, s, syncOptions{search: 60})
	start, rate := place(0, 1, r.lag, r.slope)
	t.Logf("two-way: method %s start %.4f (truth %.3f) drift %.1f ppm delay %.3f z %.1f windows %d/%d confidence %.2f",
		r.method, start, tr.Members[1].Start, (rate-1)*1e6, r.delay, r.z, r.agree, r.windows, r.confidence)
	if r.method != "two-way" {
		t.Fatalf("expected two-way pick-up, got %s", r.method)
	}
	if math.Abs(start-tr.Members[1].Start) > frame {
		t.Fatalf("start %.4f, truth %.3f (more than a frame off)", start, tr.Members[1].Start)
	}
	if math.Abs(r.delay-tr.Delay) > 0.02 {
		t.Errorf("delay %.3f, truth %.3f", r.delay, tr.Delay)
	}
	if r.confidence < 0.6 {
		t.Errorf("confidence %.2f", r.confidence)
	}
}

// Against the call's mix (a gallery view), each person lines up with their
// voice as the mix heard it.
func TestSyncAgainstMix(t *testing.T) {
	ts := testTools(t)
	gen, tr := fixtures(t)
	g := loadSound(t, ts, filepath.Join(gen, "gallery.mp4"))
	j := loadSound(t, ts, filepath.Join(gen, "maya.mov"))
	s := loadSound(t, ts, filepath.Join(gen, "sam.mov"))
	gallery := tr.Members[2].Start
	rj := align(g, j, syncOptions{search: 60, mix: true})
	sj, _ := place(0, 1, rj.lag, rj.slope)
	rs := align(g, s, syncOptions{search: 60, mix: true})
	ss, _ := place(0, 1, rs.lag, rs.slope)
	t.Logf("mix: maya %.4f (truth %.3f, z %.1f) sam %.4f (truth %.3f, z %.1f)", sj, tr.Members[0].Start-gallery, rj.z, ss, tr.Members[1].Start+tr.Delay-gallery, rs.z)
	if math.Abs(sj-(tr.Members[0].Start-gallery)) > frame {
		t.Fatalf("maya vs the mix: %.4f", sj)
	}
	// Sam's voice reached the mix the call's delay late.
	if math.Abs(ss-(tr.Members[1].Start+tr.Delay-gallery)) > frame {
		t.Fatalf("sam vs the mix: %.4f", ss)
	}
}

// Five minutes on a clock 208 ppm fast: the drift is found and the offset
// holds within a frame at the start and at the end.
func TestSyncDrift(t *testing.T) {
	ts := testTools(t)
	gen, tr := fixtures(t)
	j := loadSound(t, ts, filepath.Join(gen, tr.Long.Files[0]))
	s := loadSound(t, ts, filepath.Join(gen, tr.Long.Files[1]))
	r := align(j, s, syncOptions{search: 60})
	start, rate := place(0, 1, r.lag, r.slope)
	drift := (rate - 1) * 1e6
	end := tr.Long.Length - 5
	wantEnd := (end - tr.Long.SamStart) * (1 + tr.Long.SamDrift/1e6)
	gotEnd := (end - start) * rate
	t.Logf("drift: method %s start %.4f (truth %.3f) drift %.1f ppm (truth %.1f) windows %d agree %d residual %.1f ms; at %.0f s off by %.1f ms",
		r.method, start, tr.Long.SamStart, drift, tr.Long.SamDrift, r.windows, r.agree, r.residual*1000, end, (gotEnd-wantEnd)*1000)
	if math.Abs(start-tr.Long.SamStart) > frame {
		t.Fatalf("start %.4f, truth %.3f", start, tr.Long.SamStart)
	}
	if math.Abs(drift-tr.Long.SamDrift) > 40 {
		t.Fatalf("drift %.1f ppm, truth %.1f", drift, tr.Long.SamDrift)
	}
	if math.Abs(gotEnd-wantEnd) > frame {
		t.Fatalf("at the end it is %.1f ms off", (gotEnd-wantEnd)*1000)
	}
}
