package visual

import (
	"context"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// Each regular frame is the one on screen at its moment: the frame just before
// a cut shows the shot before it, not the next one (the fps filter's default
// took the frame half a step later, so in a call's speaker view the end of one
// speaker's picture held the next speaker's face).
func TestRegularFramesAreTheirMoments(t *testing.T) {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "cut.mp4")
	// Red, then blue from 2.7 s (30 fps).
	if _, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=red:s=64x64:r=30:d=6", "-f", "lavfi", "-i", "color=c=blue:s=64x64:r=30:d=6",
		"-filter_complex", "[0:v][1:v]overlay=enable='gte(t,2.7)'", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-g", "300", src}}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "frames")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: append([]string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"},
		batchArgs(src, 0.5, 5, 1, "scale=32:-2", filepath.Join(out, "r%05d.jpg"))...)}); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"red", "red", "red", "blue", "blue"} { // 0.5, 1.5, 2.5, 3.5, 4.5 s
		fh, err := os.Open(filepath.Join(out, fmt.Sprintf("r%05d.jpg", i+1)))
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(fh)
		fh.Close()
		if err != nil {
			t.Fatal(err)
		}
		r, _, b, _ := img.At(16, 16).RGBA()
		got := "red"
		if b > r {
			got = "blue"
		}
		if got != want {
			t.Errorf("frame at %.1f s is %s, want %s", 0.5+float64(i), got, want)
		}
	}
}
