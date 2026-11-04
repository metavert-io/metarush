//go:build matteeval

package visionmodels

import (
	"context"
	"image"
	"image/png"
	"io"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/onnx"
)

func frames(t *testing.T, src string, w, h, n int) [][]byte {
	cmd := exec.CommandContext(context.Background(), "ffmpeg", "-hide_banner", "-loglevel", "error", "-i", src, "-frames:v", strconv.Itoa(n), "-vf", "scale="+strconv.Itoa(w)+":"+strconv.Itoa(h)+":flags=area,format=rgb24", "-f", "rawvideo", "pipe:1")
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var fs [][]byte
	for {
		b := make([]byte, 3*w*h)
		if _, err := io.ReadFull(out, b); err != nil {
			break
		}
		fs = append(fs, b)
	}
	_ = cmd.Wait()
	return fs
}

func TestMatteSpeed(t *testing.T) {
	lib := os.Getenv("METARUSH_ORT_LIB")
	src := os.Getenv("EVAL_SRC")
	dir := os.Getenv("EVAL_MODELS")
	// MODNet
	mw, mh := MatteSize(1920, 1080)
	t.Logf("modnet size %dx%d", mw, mh)
	m, err := OpenMatter(lib, dir+"/modnet/model.onnx")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("modnet in %v out %v", m.m.Inputs, m.m.Outputs)
	fs := frames(t, src, mw, mh, 40)
	start := time.Now()
	var last []byte
	for _, f := range fs {
		if last, err = m.MatteRGB(f, mw, mh); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("modnet: %d frames %.1f ms/frame", len(fs), float64(time.Since(start).Milliseconds())/float64(len(fs)))
	g := image.NewGray(image.Rect(0, 0, mw, mh))
	copy(g.Pix, last)
	fo, _ := os.Create(os.Getenv("EVAL_OUT") + "/modnet.png")
	_ = png.Encode(fo, g)
	fo.Close()
	for _, sz := range [][2]int{{672, 384}, {576, 320}} {
		for _, th := range []int{4, 8} {
			om, err := onnx.Open(lib, dir+"/modnet/model.onnx", th)
			if err != nil {
				t.Fatal(err)
			}
			mm := &Matter{m: om}
			fs := frames(t, src, sz[0], sz[1], 30)
			start := time.Now()
			for _, f := range fs {
				if _, err = mm.MatteRGB(f, sz[0], sz[1]); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("modnet %dx%d threads %d: %.1f ms/frame", sz[0], sz[1], th, float64(time.Since(start).Milliseconds())/float64(len(fs)))
		}
	}
	// PP-HumanSeg 192x192
	pm, err := onnx.Open(lib, dir+"/pphumanseg/model.onnx", 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("pphumanseg in %v out %v", pm.Inputs, pm.Outputs)
	ps := frames(t, src, 192, 192, 40)
	start = time.Now()
	var po []Tensor0
	_ = po
	var lastp []float32
	for _, f := range ps {
		in := make([]float32, 3*192*192)
		for i := 0; i < 192*192; i++ {
			in[i] = (float32(f[3*i])/255 - 0.5) / 0.5
			in[192*192+i] = (float32(f[3*i+1])/255 - 0.5) / 0.5
			in[2*192*192+i] = (float32(f[3*i+2])/255 - 0.5) / 0.5
		}
		outs, err := pm.Run(in, []int64{1, 3, 192, 192})
		if err != nil {
			t.Fatal(err)
		}
		lastp = outs[0].Data
		if len(outs[0].Shape) > 0 && f[0] == 255 {
			t.Logf("shape %v", outs[0].Shape)
		}
	}
	t.Logf("pphumanseg: %d frames %.2f ms/frame (%d values)", len(ps), float64(time.Since(start).Microseconds())/1000/float64(len(ps)), len(lastp))
	// BiRefNet 1024
	bm, err := OpenMasker(lib, dir+"/birefnet-dir/BiRefNet-general-bb_swin_v1_tiny-epoch_232.onnx")
	if err != nil {
		t.Skip(err)
	}
	bs := frames(t, src, 1024, 576, 3)
	start = time.Now()
	for _, f := range bs {
		img := image.NewRGBA(image.Rect(0, 0, 1024, 576))
		for i := 0; i < 1024*576; i++ {
			img.Pix[4*i], img.Pix[4*i+1], img.Pix[4*i+2], img.Pix[4*i+3] = f[3*i], f[3*i+1], f[3*i+2], 255
		}
		if _, err := bm.Mask(img); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("birefnet: %d frames %.1f ms/frame", len(bs), float64(time.Since(start).Milliseconds())/float64(len(bs)))
}

type Tensor0 struct{}
