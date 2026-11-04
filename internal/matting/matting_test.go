package matting_test

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/matting"
	"github.com/metavert-io/metarush/internal/matting/mattingtest"
	"github.com/metavert-io/metarush/internal/onnx/onnxtest"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

func toolSet(t *testing.T) *tools.Set {
	t.Helper()
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) || !ts.Has(tools.FFprobe) {
		t.Skip("ffmpeg isn't installed here")
	}
	return ts
}

// frames counts a video's frames.
func frames(t *testing.T, path string) (n int, size, rate string) {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-count_frames", "-select_streams", "v:0", "-show_entries",
		"stream=nb_read_frames,width,height,r_frame_rate", "-of", "default=nw=1", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	var w, h string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		k, v, _ := strings.Cut(l, "=")
		switch k {
		case "nb_read_frames":
			for _, c := range v {
				n = n*10 + int(c-'0')
			}
		case "width":
			w = v
		case "height":
			h = v
		case "r_frame_rate":
			rate = v
		}
	}
	return n, w + "x" + h, rate
}

// A stretch of a clip gets one matte per frame at the clip's own rate, in
// a video the model's size; how far along it is is reported as it goes.
func TestRunMakesOneMattePerFrame(t *testing.T) {
	ts := toolSet(t)
	dir := t.TempDir()
	src := mattingtest.Footage(t, dir, 2, 320, 180, 25)
	m := mattingtest.New()
	var last, total int
	res, err := matting.Run(context.Background(), matting.Job{Tools: ts, Src: src, From: 0.4, To: 1.6, Model: m, Matte: filepath.Join(dir, "matte.mp4"),
		Progress: func(d, n int) { last, total = d, n }})
	if err != nil {
		t.Fatal(err)
	}
	if res.Frames != 30 || last != 30 || total != 30 || m.Opened.Load() != 1 || m.Frames.Load() != 30 {
		t.Fatalf("frames %d, progress %d of %d, opened %d, read %d", res.Frames, last, total, m.Opened.Load(), m.Frames.Load())
	}
	n, size, rate := frames(t, filepath.Join(dir, "matte.mp4"))
	if n != 30 || size != "64x36" || rate != "25/1" || res.Video.Rate != "25/1" || res.Video.Width != 320 || res.MatteWidth != 64 {
		t.Fatalf("matte video: %d frames, %s, %s; %+v", n, size, rate, res)
	}
	if res.Measures.People != 1 || res.Measures.MsPerFrame < 0 || res.Measures.Seconds <= 0 {
		t.Fatalf("measures %+v", res.Measures)
	}
	// To 0 is the whole clip.
	whole, err := matting.Run(context.Background(), matting.Job{Tools: ts, Src: src, Model: mattingtest.New(), Matte: filepath.Join(dir, "whole.mp4")})
	if err != nil || whole.Frames != 50 {
		t.Fatalf("the whole clip: %+v %v", whole, err)
	}
	// Nobody in the footage: still a matte, nobody counted.
	none := mattingtest.New()
	none.Nobody = true
	empty, err := matting.Run(context.Background(), matting.Job{Tools: ts, Src: src, Model: none, Matte: filepath.Join(dir, "none.mp4")})
	if err != nil || empty.Measures.People != 0 {
		t.Fatalf("nobody: %+v %v", empty, err)
	}
}

// A model that fails, or a stop, ends it plainly and leaves no matte half
// written to be taken for a whole one.
func TestRunStops(t *testing.T) {
	ts := toolSet(t)
	dir := t.TempDir()
	src := mattingtest.Footage(t, dir, 2, 320, 180, 25)
	m := mattingtest.New()
	m.FailAt = 5
	if _, err := matting.Run(context.Background(), matting.Job{Tools: ts, Src: src, Model: m, Matte: filepath.Join(dir, "a.mp4")}); !errors.Is(err, mattingtest.ErrFailed) {
		t.Fatalf("a failing model: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	stop := &stopAt{Model: mattingtest.New(), at: 4, cancel: cancel}
	if _, err := matting.Run(ctx, matting.Job{Tools: ts, Src: src, Model: stop, Matte: filepath.Join(dir, "b.mp4")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("stopped: %v", err)
	}
	if _, err := matting.Run(context.Background(), matting.Job{Tools: ts, Src: src, From: 3, To: 4, Model: mattingtest.New(), Matte: filepath.Join(dir, "c.mp4")}); err == nil {
		t.Fatal("a stretch after the clip's end made a matte")
	}
	if _, err := matting.Run(context.Background(), matting.Job{Tools: ts, Src: filepath.Join(dir, "missing.mp4"), Model: mattingtest.New(), Matte: filepath.Join(dir, "d.mp4")}); err == nil {
		t.Fatal("a missing file made a matte")
	}
}

// stopAt cancels the run when its stream reaches a frame.
type stopAt struct {
	*mattingtest.Model
	at     int
	cancel func()
	n      atomic.Int64
}

func (s *stopAt) Open(ctx context.Context, mw, mh int) (matting.Stream, error) {
	in, err := s.Model.Open(ctx, mw, mh)
	return &stopStream{Stream: in, s: s}, err
}

type stopStream struct {
	matting.Stream
	s *stopAt
}

func (x *stopStream) Next(f []byte) ([]byte, error) {
	if int(x.s.n.Add(1)) == x.s.at {
		x.s.cancel()
	}
	return x.Stream.Next(f)
}

// The file's picture: its size as shown, its rate as a fraction, its colours.
func TestProbe(t *testing.T) {
	ts := toolSet(t)
	src := mattingtest.Footage(t, t.TempDir(), 1, 160, 90, 30)
	v, err := matting.Probe(context.Background(), ts, src)
	if err != nil || v.Width != 160 || v.Height != 90 || v.Rate != "30/1" || v.FPS != 30 || v.Duration < 0.9 {
		t.Fatalf("%+v %v", v, err)
	}
	if _, err := matting.Probe(context.Background(), ts, filepath.Join(t.TempDir(), "nothing.mp4")); err == nil {
		t.Fatal("probed a missing file")
	}
}

// Even sizes for the encoders; four bytes a pixel for the helper.
func TestSizes(t *testing.T) {
	if matting.Even(1281) != 1280 || matting.Even(1) != 2 || matting.Bytes("bgra") != 4 || matting.Bytes("rgb24") != 3 {
		t.Fatal("sizes")
	}
	if w, h := (matting.Apple{}).Size(1920, 1080); w != 1280 || h != 720 {
		t.Fatalf("Apple Vision reads 1080p at %dx%d", w, h)
	}
	if w, h := (matting.Apple{}).Size(1079, 1919); w != 720 || h != 1280 {
		t.Fatalf("a vertical frame at %dx%d", w, h)
	}
	if w, h := (matting.Apple{}).Size(640, 361); w != 640 || h != 360 {
		t.Fatalf("a small frame at %dx%d", w, h)
	}
	if w, h := (matting.MODNet{}).Size(1920, 1080); w != 896 || h != 512 {
		t.Fatalf("MODNet reads 1080p at %dx%d", w, h)
	}
	if (matting.Apple{}).Format() != "bgra" || (matting.MODNet{}).Format() != "rgb24" {
		t.Fatal("formats")
	}
}

// Off a Mac, or without the helper, Apple Vision's stream says why it can't.
func TestAppleStreamSaysWhyItStopped(t *testing.T) {
	ts := tools.New(config.Tools{Vision: filepath.Join(t.TempDir(), "no-helper")})
	s, err := matting.Apple{Tools: ts}.Open(context.Background(), 64, 36)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Next(make([]byte, 64*36*4))
	if err == nil {
		t.Fatal("a frame went through no helper")
	}
	if cerr := s.Close(); cerr == nil {
		t.Fatal("closing hid the helper's failure")
	}
	if runtime.GOOS != "darwin" && !errors.Is(err, vision.ErrNotMac) && !strings.Contains(err.Error(), "Mac") && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("off a Mac: %v", err)
	}
}

// MODNet's runner reads frames at its size and answers a matte per frame:
// with its stand-in (the person is what is bright), the box is the person.
func TestMODNetRunsFrames(t *testing.T) {
	ts := toolSet(t)
	lib := onnxtest.Library(t)
	dir := t.TempDir()
	model := onnxtest.Matte(t, dir)
	src := mattingtest.Footage(t, dir, 1, 640, 360, 10)
	res, err := matting.Run(context.Background(), matting.Job{Tools: ts, Src: src, Model: matting.MODNet{Lib: lib, Path: model}, Matte: filepath.Join(dir, "m.mp4")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Frames != 10 || res.MatteWidth != 640 || res.MatteHeight != 352 || res.Measures.People != 1 {
		t.Fatalf("%+v", res)
	}
	if _, err := (matting.MODNet{Lib: "", Path: model}).Open(context.Background(), 32, 32); err == nil {
		t.Fatal("no onnxruntime, yet it opened")
	}
	if _, err := os.Stat(filepath.Join(dir, "m.mp4")); err != nil {
		t.Fatal(err)
	}
}
