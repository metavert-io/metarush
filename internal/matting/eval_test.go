//go:build matteeval

package matting

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// TestCutOutEval measures the models that find the people in footage (D506,
// docs/background-removal.md#how-well-it-works), one after the other:
//
//   - on real footage (a man outdoors, a woman with curly hair, several
//     people in a busy room, an astronaut before a busy background, and an
//     animation with nobody in it): how long a frame takes, how often
//     someone is found, and how much the matte's edges flicker where the
//     picture holds still, before and after smoothing;
//   - on a composite whose matte is known: a woman filmed against a green
//     screen, keyed (the reference), placed over the outdoor clip: how much
//     of each model's matte is right — its mean error, its overlap with the
//     reference (IoU at half) and its error along the edges.
//
// Fetch the clips with scripts/fetch-cutout-clips.sh, then on a Mac (Apple
// Vision, METARUSH_VISION_HELPER the helper when it isn't on PATH) and with
// METARUSH_ORT_LIB and METARUSH_MODNET for MODNet:
//
//	METARUSH_MATTE_CLIPS=tests/fixtures/cutouts go test -tags matteeval -run TestCutOutEval -timeout 30m -v ./internal/matting/
func TestCutOutEval(t *testing.T) {
	dir := os.Getenv("METARUSH_MATTE_CLIPS")
	if dir == "" {
		t.Skip("METARUSH_MATTE_CLIPS names scripts/fetch-cutout-clips.sh's folder")
	}
	ctx := context.Background()
	ts := tools.New(config.Tools{Vision: os.Getenv("METARUSH_VISION_HELPER")})
	var ms []struct {
		name string
		m    Model
	}
	if runtime.GOOS == "darwin" {
		ms = append(ms, struct {
			name string
			m    Model
		}{"Apple Vision (accurate)", Apple{Tools: ts}})
	}
	if lib, path := os.Getenv("METARUSH_ORT_LIB"), os.Getenv("METARUSH_MODNET"); lib != "" && path != "" {
		ms = append(ms, struct {
			name string
			m    Model
		}{"MODNet", MODNet{Lib: lib, Path: path}})
	}
	if len(ms) == 0 {
		t.Skip("no model to measure: Apple Vision needs a Mac, MODNet METARUSH_ORT_LIB and METARUSH_MODNET")
	}
	out := t.TempDir()
	t.Log("| clip | model | matte size | per frame | someone found | edge flicker (before → after smoothing) |")
	// Each stretch is one shot (ffmpeg's scene score finds no cut inside it).
	for _, c := range []struct {
		id, what      string
		from, seconds float64
	}{{"cbp", "a man outdoors, 1080p", 8, 4}, {"spectro", "curly hair, vertical 1080p", 20, 4}, {"artemis", "several people in a busy room, 1080p", 180, 4},
		{"artemis", "an astronaut, a busy background, 1080p", 236.8, 3.3}, {"artemis", "an animation with nobody in it, 1080p", 30, 4}} {
		src := filepath.Join(dir, c.id+".webm")
		if _, err := os.Stat(src); err != nil {
			t.Fatalf("%s: run scripts/fetch-cutout-clips.sh first (%v)", c.id, err)
		}
		for _, m := range ms {
			res, err := Run(ctx, Job{Tools: ts, Src: src, From: c.from, To: c.from + c.seconds, Model: m.m, Matte: filepath.Join(out, fmt.Sprintf("%s-%.0f-%s.mp4", c.id, c.from, slug(m.name)))})
			if err != nil {
				t.Fatalf("%s with %s: %v", c.id, m.name, err)
			}
			me := res.Measures
			t.Logf("| %s | %s | %d×%d | %.0f ms | %.0f%% | %.1f → %.1f |", c.what, m.name, res.MatteWidth, res.MatteHeight, me.MsPerFrame, me.People*100, me.FlickerBefore, me.FlickerAfter)
		}
	}
	// A longer stretch, made whole: the matte, then the cut-out file (what a
	// person waits for), in seconds per minute of footage.
	t.Log("| model | stretch | the matte | the cut-out file | in all, per minute of footage |")
	for _, m := range ms {
		secs := 60.0
		if _, ok := m.m.(MODNet); ok {
			secs = 20 // about a minute and a half of work on a busy Mac mini
		}
		src := filepath.Join(dir, "cbp.webm")
		matte := filepath.Join(out, "long-"+slug(m.name)+".mp4")
		t0 := time.Now()
		res, err := Run(ctx, Job{Tools: ts, Src: src, From: 4, To: 4 + secs, Model: m.m, Matte: matte})
		if err != nil {
			t.Fatalf("the long stretch with %s: %v", m.name, err)
		}
		matting := time.Since(t0).Seconds()
		cw, ch := CutOutSize(res.Video.Width, res.Video.Height, 1920)
		t1 := time.Now()
		args := append(CutOutArgs(res.Video, res.First, res.Frames, src, matte, cw, ch), filepath.Join(out, "long-"+slug(m.name)+".webm"))
		if _, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
			t.Fatalf("the cut-out file: %v", err)
		}
		file := time.Since(t1).Seconds()
		long := float64(res.Frames) / res.Video.FPS // the clip may end sooner
		t.Logf("| %s | %.1f s, %d frames | %.0f s | %.0f s | %.1f min |", m.name, long, res.Frames, matting, file, (matting+file)/long)
	}
	comp, truth, w, h := composite(t, ts, dir, out)
	t.Log("| model | mean error | IoU at half | error along the edges | per frame |")
	for _, m := range ms {
		matte := filepath.Join(out, "composite-"+slug(m.name)+".mp4")
		res, err := Run(ctx, Job{Tools: ts, Src: comp, Model: m.m, Matte: matte})
		if err != nil {
			t.Fatalf("the composite with %s: %v", m.name, err)
		}
		mae, iou, edge := compare(t, ts, matte, truth, w, h)
		t.Logf("| %s | %.1f%% | %.3f | %.1f%% | %.0f ms |", m.name, mae*100, iou, edge*100, res.Measures.MsPerFrame)
	}
}

func slug(s string) string {
	return strings.ToLower(strings.NewReplacer(" ", "-", "(", "", ")", "").Replace(s))
}

// The green screen covers the woman's upper body in this stretch: the crop
// keeps only that (the carpet, the studio's lights and the burned-in words
// stay out), keyed by its green for the reference matte and placed over the
// outdoor clip.
const (
	gsFrom, gsSeconds          = 6.0, 4.0
	cropW, cropH, cropX, cropY = 360, 460, 105, 230
	keyColour                  = "0x30c058"
)

// composite makes the clip with a known matte: the keyed woman over the
// outdoor clip, and that matte, both lossless-ish and frame for frame.
func composite(t *testing.T, ts *tools.Set, dir, out string) (string, string, int, int) {
	t.Helper()
	gs, bg := filepath.Join(dir, "greenscreen.webm"), filepath.Join(dir, "cbp.webm")
	comp, truth := filepath.Join(out, "composite.mp4"), filepath.Join(out, "truth.mkv")
	key := fmt.Sprintf("[0:v]crop=%d:%d:%d:%d,chromakey=color=%s:similarity=0.10:blend=0.04,format=yuva444p,setpts=PTS-STARTPTS[fg]", cropW, cropH, cropX, cropY, keyColour)
	run := func(args ...string) {
		if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: append([]string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"}, args...)}); err != nil {
			t.Fatalf("ffmpeg: %v", err)
		}
	}
	run("-ss", fmt.Sprint(gsFrom), "-t", fmt.Sprint(gsSeconds), "-i", gs, "-ss", "20", "-t", fmt.Sprint(gsSeconds), "-i", bg, "-filter_complex",
		key+fmt.Sprintf(";[1:v]scale=-2:%d,crop=%d:%d,setpts=PTS-STARTPTS,fps=30[bg];[bg][fg]overlay=shortest=1,format=yuv420p[o]", cropH, cropW, cropH),
		"-map", "[o]", "-r", "30", "-c:v", "libx264", "-crf", "12", "-preset", "veryfast", "-an", comp)
	run("-ss", fmt.Sprint(gsFrom), "-t", fmt.Sprint(gsSeconds), "-i", gs, "-filter_complex", key+";[fg]alphaextract,format=gray[o]",
		"-map", "[o]", "-r", "30", "-c:v", "ffv1", "-an", truth)
	return comp, truth, cropW, cropH
}

// frames decodes a video's frames as grey bytes at w×h.
func frames(t *testing.T, ts *tools.Set, path string, w, h int) [][]byte {
	t.Helper()
	var buf bytes.Buffer
	if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Stdout: &buf, Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", path,
		"-vf", fmt.Sprintf("scale=%d:%d:flags=bicubic,format=gray", w, h), "-f", "rawvideo", "pipe:1"}}); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
	var out [][]byte
	b := buf.Bytes()
	for len(b) >= w*h {
		out = append(out, b[:w*h])
		b = b[w*h:]
	}
	return out
}

// compare measures a matte against the reference: the mean error (0–1),
// the overlap at half (IoU), and the mean error where the reference has an
// edge within four pixels.
func compare(t *testing.T, ts *tools.Set, matte, truth string, w, h int) (mae, iou, edge float64) {
	t.Helper()
	got, want := frames(t, ts, matte, w, h), frames(t, ts, truth, w, h)
	n := min(len(got), len(want))
	if n == 0 || math.Abs(float64(len(got)-len(want))) > 1 {
		t.Fatalf("%d frames of matte, %d of the reference", len(got), len(want))
	}
	var sum, edgeSum float64
	var px, edgePx, inter, union int
	for f := 0; f < n; f++ {
		g, r := got[f], want[f]
		band := nearEdge(r, w, h, 4)
		for i := range r {
			d := math.Abs(float64(g[i])-float64(r[i])) / 255
			sum += d
			px++
			if band[i] {
				edgeSum += d
				edgePx++
			}
			a, b := g[i] >= 128, r[i] >= 128
			if a && b {
				inter++
			}
			if a || b {
				union++
			}
		}
	}
	mae = sum / float64(px)
	if union > 0 {
		iou = float64(inter) / float64(union)
	}
	if edgePx > 0 {
		edge = edgeSum / float64(edgePx)
	}
	return mae, iou, edge
}

// nearEdge marks the pixels within r of where the reference changes between
// see-through and kept.
func nearEdge(m []byte, w, h, r int) []bool {
	out := make([]bool, len(m))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			on := m[y*w+x] >= 128
			edge := false
			for dy := -1; dy <= 1 && !edge; dy++ {
				for dx := -1; dx <= 1; dx++ {
					yy, xx := y+dy, x+dx
					if yy < 0 || xx < 0 || yy >= h || xx >= w {
						continue
					}
					if (m[yy*w+xx] >= 128) != on {
						edge = true
						break
					}
				}
			}
			if !edge {
				continue
			}
			for yy := max(0, y-r); yy <= min(h-1, y+r); yy++ {
				for xx := max(0, x-r); xx <= min(w-1, x+r); xx++ {
					out[yy*w+xx] = true
				}
			}
		}
	}
	return out
}
