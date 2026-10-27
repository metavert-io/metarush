package evidence

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

type visualTruth struct {
	Duration float64              `json:"duration_s"`
	Cuts     []float64            `json:"cuts_s"`
	Pan      [2]float64           `json:"pan_s"`
	Enter    [2]float64           `json:"enter_s"`
	Quiet    [][2]float64         `json:"quiet_s"`
	Text     map[string][]float64 `json:"text"`
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "tests", "fixtures", "gen", name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("%s not generated (scripts/make-fixtures.sh)", name)
	}
	return p
}

func overlaps(c VisualChange, a, b float64) bool { return c.To >= a && c.From <= b }

// The measured acceptance for change-aware sampling: on visual-a (a hard cut,
// a slow pan, a person walking in, a talking head, a sign and a dog), every
// cut, the pan and the entrance are found, and the still parts (the talking
// head among them) have no events.
func TestVisualFixtureDetectsCutsPanAndEntrance(t *testing.T) {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	p := fixture(t, "visual-a.mp4")
	var truth visualTruth
	b, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "truth", "visual-a.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &truth); err != nil {
		t.Fatal(err)
	}
	mi := &models.MediaInfo{Duration: truth.Duration, HasVideo: true, HasAudio: true}
	rep, err := Analyze(context.Background(), ts, p, "original", t.TempDir(), mi)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range rep.VisualChanges {
		t.Logf("%-7s at %6.2f  %6.2f–%6.2f  score %.2f  frames %v", c.Kind, c.At, c.From, c.To, c.Score, c.Frames)
	}
	// Every cut, at the right time, and nothing else counted as a cut.
	var cuts []VisualChange
	for _, c := range rep.VisualChanges {
		if c.Kind == ChangeCut {
			cuts = append(cuts, c)
		}
	}
	if len(cuts) != len(truth.Cuts) {
		t.Errorf("cuts: want %v, got %d", truth.Cuts, len(cuts))
	}
	for _, want := range truth.Cuts {
		found := false
		for _, c := range cuts {
			if math.Abs(c.At-want) <= 0.2 {
				found = true
			}
		}
		if !found {
			t.Errorf("no cut found near %.1f s", want)
		}
	}
	found := func(a, b float64, kinds ...string) bool {
		for _, c := range rep.VisualChanges {
			for _, k := range kinds {
				if c.Kind == k && overlaps(c, a, b) {
					return true
				}
			}
		}
		return false
	}
	if !found(truth.Pan[0]+0.3, truth.Pan[1]-0.3, ChangeGradual, ChangeMotion) {
		t.Errorf("the slow pan %v was not found", truth.Pan)
	}
	if !found(truth.Enter[0], truth.Enter[1], ChangeMotion, ChangeGradual) {
		t.Errorf("the person walking in %v was not found", truth.Enter)
	}
	for _, q := range truth.Quiet {
		for _, c := range rep.VisualChanges {
			if c.Kind != ChangeCut && overlaps(c, q[0], q[1]) {
				t.Errorf("false event in the still part %v: %+v", q, c)
			}
			if c.Kind == ChangeCut && c.At > q[0] && c.At < q[1] {
				t.Errorf("false cut in the still part %v: %+v", q, c)
			}
		}
	}
	if rep.Activity == nil || len(rep.Activity.Values) != int(math.Ceil(truth.Duration)) {
		t.Fatalf("activity per second: %+v", rep.Activity)
	}
	if rep.Activity.Values[16] <= rep.Activity.Values[3] {
		t.Errorf("activity while someone walks in (%.3f) should exceed the talking head (%.3f)", rep.Activity.Values[16], rep.Activity.Values[3])
	}
}

// The other fixtures: steady motion (a bouncing block, a cellular automaton) is
// that shot's normal, not a stream of events.
func TestVisualFixturesSteadyMotion(t *testing.T) {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	for _, f := range []struct {
		name string
		dur  float64
		max  int
	}{{"interview-a.mov", 27, 1}, {"broll-c.mp4", 10, 1}, {"long-silence.mp4", 8, 0}} {
		p := fixture(t, f.name)
		ch, _, err := detectVisual(context.Background(), ts, p, &models.MediaInfo{Duration: f.dur, HasVideo: true}, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range ch {
			t.Logf("%s: %-7s at %6.2f %6.2f–%6.2f score %.2f", f.name, c.Kind, c.At, c.From, c.To, c.Score)
		}
		if len(ch) > f.max {
			t.Errorf("%s: %d events, want at most %d", f.name, len(ch), f.max)
		}
	}
}
