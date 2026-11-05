package matting_test

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/matting"
	"github.com/metavert-io/metarush/internal/matting/mattingtest"
	"github.com/metavert-io/metarush/internal/tools"
)

// A box moving 8 pixels a frame, 320×180 at 30 fps for two seconds, written
// as a WebM (VP9, Matroska's millisecond time base: frame times just before
// the grid), an MP4 (H.264, exact times), a Matroska file of variable rate
// (every third frame left out, the rest at their own times) and an MP4 whose
// sound runs on 0.3 s past its picture.
func movingBox(t *testing.T, dir, name string) string {
	t.Helper()
	src := "color=c=black:s=320x180:rate=30:d=2[bg];color=c=white:s=40x60:rate=30:d=2[sq];[bg][sq]overlay=x='10+n*8':y=60:shortest=1,format=yuv420p"
	out := filepath.Join(dir, name)
	args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi"}
	switch name {
	case "box.webm":
		args = append(args, "-i", src+"[out0]", "-c:v", "libvpx-vp9", "-b:v", "0", "-crf", "20", "-deadline", "realtime", "-cpu-used", "8", out)
	case "box.mp4":
		args = append(args, "-i", src+"[out0]", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", out)
	case "box-vfr.mkv":
		args = append(args, "-i", src+",select='mod(n\\,3)'[out0]", "-fps_mode", "vfr", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", out)
	case "box-long-sound.mp4":
		args = append(args, "-i", src+"[out0]", "-f", "lavfi", "-i", "sine=f=440:d=2.3", "-map", "0:v", "-map", "1:a",
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", out)
	default:
		t.Fatalf("no clip called %s", name)
	}
	if b, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("making %s: %v %s", name, err, b)
	}
	return out
}

// grey decodes every frame of a cut-out as grey bytes: its picture, or its alpha.
func grey(t *testing.T, path string, alpha bool) [][]byte {
	t.Helper()
	vf := "format=gray"
	if alpha {
		vf = "alphaextract,format=gray"
	}
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-c:v", "libvpx-vp9", "-i", path, "-vf", vf, "-f", "rawvideo", "pipe:1").Output()
	if err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	var frames [][]byte
	for len(out) >= 320*180 {
		frames = append(frames, out[:320*180])
		out = out[320*180:]
	}
	return frames
}

// span is where row y is bright: its first and last bright column, or -1.
func span(f []byte, y int) (int, int) {
	first, last := -1, -1
	for x := 0; x < 320; x++ {
		if f[y*320+x] > 128 {
			if first < 0 {
				first = x
			}
			last = x
		}
	}
	return first, last
}

// Each frame of a cut-out carries its own frame's matte (D509), whatever the
// times the clip's file gives its frames: a millisecond time base puts them
// just before the grid, where pairing picture and matte by time gave every
// third frame the frame before's matte; a variable rate has gaps, where the
// matte's decode had fewer frames than the stretch; a file whose sound runs
// on has fewer frames than its length says. Every stretch gets the frames
// Frames counts, so the job's count check holds.
func TestEveryFrameCarriesItsOwnMatte(t *testing.T) {
	ts := toolSet(t)
	dir := t.TempDir()
	for _, name := range []string{"box.webm", "box.mp4", "box-vfr.mkv", "box-long-sound.mp4"} {
		t.Run(name, func(t *testing.T) {
			src := movingBox(t, dir, name)
			v, err := matting.Probe(context.Background(), ts, src)
			if err != nil {
				t.Fatal(err)
			}
			for _, part := range []struct{ from, to float64 }{{0, 0}, {0.3, 1.7}} {
				matte := filepath.Join(dir, fmt.Sprintf("%s-%.1f-matte.mp4", name, part.from))
				m := mattingtest.New()
				m.Width, m.Height = 320, 180
				res, err := matting.Run(context.Background(), matting.Job{Tools: ts, Src: src, From: part.from, To: part.to, Model: m, Matte: matte})
				if err != nil {
					t.Fatal(err)
				}
				first, count, err := matting.Frames(v, part.from, part.to)
				if err != nil {
					t.Fatal(err)
				}
				if res.First != first || res.Frames != count {
					t.Fatalf("from %.1f s: the matte has %d frames from frame %d, the stretch %d from %d", part.from, res.Frames, res.First, count, first)
				}
				out := filepath.Join(dir, fmt.Sprintf("%s-%.1f-cut-out.webm", name, part.from))
				args := append(matting.CutOutArgs(v, res.First, res.Frames, src, matte, 320, 180), out)
				if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
					t.Fatal(err)
				}
				pic, alpha := grey(t, out, false), grey(t, out, true)
				if len(pic) != res.Frames || len(alpha) != res.Frames {
					t.Fatalf("%d frames of picture, %d of alpha, %d of matte", len(pic), len(alpha), res.Frames)
				}
				for k := range pic {
					a0, a1 := span(alpha[k], 90)
					p0, p1 := span(pic[k], 90)
					if abs(a0-p0) > 2 || abs(a1-p1) > 2 {
						t.Fatalf("from %.1f s, frame %d: the box is at %d–%d, its matte at %d–%d", part.from, k, p0, p1, a0, a1)
					}
				}
			}
		})
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
