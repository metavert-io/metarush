package derive

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/tools"
)

// makeClip renders a 6s 1080p test clip with a 1.5s silent pause at 2.0–3.5s and
// a black section at 4.5–5.0s.
func makeClip(t *testing.T, ts *tools.Set) string {
	t.Helper()
	if !ts.Has(tools.FFmpeg) || !ts.Has(tools.FFprobe) {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	p := filepath.Join(t.TempDir(), "clip.mov")
	_, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=30:duration=6",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=6",
		"-filter_complex", "[0:v]drawbox=enable='between(t,4.5,5.0)':color=black:t=fill[v];[1:a]volume='if(between(t,2,3.5),0,1)':eval=frame[a]",
		"-map", "[v]", "-map", "[a]", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", p}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProbeDeriveEvidence(t *testing.T) {
	ctx := context.Background()
	ts := tools.New(config.Tools{})
	clip := makeClip(t, ts)
	mi, err := probe.Probe(ctx, ts, clip)
	if err != nil {
		t.Fatal(err)
	}
	if !mi.HasVideo || !mi.HasAudio || mi.Width != 1920 || math.Abs(mi.Duration-6) > 0.1 || mi.VFR {
		t.Fatalf("probe: %+v", mi)
	}
	d := &Deriver{T: ts}
	dir := t.TempDir()
	px, err := d.Proxy(ctx, clip, filepath.Join(dir, "proxy.mp4"), mi)
	if err != nil {
		t.Fatal(err)
	}
	if px.Height != 720 || px.Width != 1280 {
		t.Fatalf("proxy dims %dx%d", px.Width, px.Height)
	}
	sheet, times, err := d.ContactSheet(ctx, clip, filepath.Join(dir, "sheet.jpg"), dir, mi, 8)
	if err != nil || len(times) != 8 || sheet.Width == 0 {
		t.Fatalf("contact sheet: %v %v", err, sheet)
	}
	pk, err := d.Peaks(ctx, clip, filepath.Join(dir, "peaks.json"), mi)
	if err != nil {
		t.Fatal(err)
	}
	var peaks struct{ Peaks []float64 }
	b, _ := os.ReadFile(filepath.Join(dir, "peaks.json"))
	_ = json.Unmarshal(b, &peaks)
	if len(peaks.Peaks) < 100 || pk.Size == 0 {
		t.Fatalf("peaks: %d", len(peaks.Peaks))
	}
	rep, err := evidence.Analyze(ctx, ts, clip, "original", dir, mi)
	if err != nil {
		t.Fatal(err)
	}
	foundPause := false
	for _, s := range rep.Silences {
		if math.Abs(s.From-2.0) < 0.15 && math.Abs(s.Duration-1.5) < 0.2 {
			foundPause = true
		}
	}
	if !foundPause {
		t.Errorf("did not find the 1.5s pause: %+v", rep.Silences)
	}
	if len(rep.Black) == 0 || math.Abs(rep.Black[0].From-4.5) > 0.1 {
		t.Errorf("black section not found: %+v", rep.Black)
	}
	if !rep.Loudness.Measured {
		t.Errorf("loudness not measured")
	}
	if len(rep.Scenes) == 0 {
		t.Errorf("expected scene changes at the black section: %+v", rep.Scenes)
	}
	// Not-a-video content is rejected by content, not extension.
	bad := filepath.Join(dir, "fake.mp4")
	_ = os.WriteFile(bad, []byte("this is not a video"), 0o644)
	if _, err := probe.Probe(ctx, ts, bad); err == nil {
		t.Fatal("text file accepted as media")
	}
}
