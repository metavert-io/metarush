package local

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"runtime"
	"sync"

	_ "golang.org/x/image/webp"
	_ "image/jpeg" // pictures people keep

	"golang.org/x/image/draw"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/localmodels"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Pictures made larger on this computer (D279): Real-ESRGAN general x4v3
// (BSD-3-Clause, 4.5 MB), run in-process through the onnxruntime that comes
// with the transcriber's sherpa-onnx (internal/onnx), in 128-pixel tiles that
// overlap so no seams show. 2× is the 4× result halved; a picture's
// see-through parts are carried over, made larger smoothly.

// ModelUpscale is the model's ID; UpscalePin its download.
const (
	ModelUpscale = "real-esrgan-x4v3"
	UpscalePin   = "real-esrgan"
)

const (
	tile    = 128 // the model's input, fixed
	overlap = 8   // context each side of a tile, never kept
	factor  = 4   // the model's scale
)

// Runner runs the model on one 1×3×128×128 tile (values 0–1) and returns
// 1×3×512×512. Tests stand one in.
type Runner func(in []float32) ([]float32, error)

// Library finds the onnxruntime that runs the model (tests stand one in).
var Library = func(e aiservices.Env) string {
	if e.Tools == nil {
		return ""
	}
	return onnx.Library(e.Tools.Path(tools.Sherpa))
}

// OpenUpscaler loads the model for a computer (tests stand one in).
var OpenUpscaler = func(e aiservices.Env, s localmodels.Store, p localmodels.Pin) (Runner, error) {
	lib := Library(e)
	if lib == "" {
		return nil, apperr.New(apperr.NotConfigured, "Making pictures larger here needs the transcriber's sherpa-onnx, which brings the program that runs the model.").
			WithHint("Add transcription in Settings → Components.").WithCommand("metarush components add transcription")
	}
	return openModel(lib, s.Path(p, "real_esrgan_general_x4v3.onnx"))
}

var (
	modelsMu sync.Mutex
	opened   = map[string]*onnx.Model{}
)

// openModel loads a model once per process.
func openModel(lib, path string) (Runner, error) {
	modelsMu.Lock()
	defer modelsMu.Unlock()
	m := opened[path]
	if m == nil {
		var err error
		if m, err = onnx.Open(lib, path, min(4, max(1, runtime.NumCPU()/2))); err != nil {
			return nil, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't load the model that makes pictures larger.")
		}
		opened[path] = m
	}
	return func(in []float32) ([]float32, error) {
		out, err := m.Run(in, []int64{1, 3, tile, tile})
		if err != nil {
			return nil, err
		}
		if len(out) == 0 || len(out[0].Data) != 3*tile*factor*tile*factor {
			return nil, fmt.Errorf("the model answered an unexpected shape")
		}
		return out[0].Data, nil
	}, nil
}

type upscale struct{ base }

func init() { aiservices.Register(&upscale{base{aiservices.KindUpscale}}) }

// Ready says whether the model is set up here.
func (a *upscale) Ready(e aiservices.Env, _ string) error {
	s := Stores(e)
	p, ok := s.Find(UpscalePin)
	if !ok {
		return apperr.New(apperr.NotConfigured, "Making pictures larger doesn't run on this kind of computer yet.")
	}
	if err := s.Ready(p); err != nil {
		return err
	}
	if Library(e) == "" {
		return apperr.New(apperr.NotConfigured, "Making pictures larger here needs the transcriber's sherpa-onnx, which brings the program that runs the model.").
			WithHint("Add transcription in Settings → Components.").WithCommand("metarush components add transcription")
	}
	return nil
}

// Submit makes the picture larger.
func (a *upscale) Submit(ctx context.Context, _ string, req *aiservices.Request) (*aiservices.Ticket, error) {
	in := req.Input(aiservices.RolePicture)
	if in == nil {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "There's no picture to make larger."}
	}
	e, done, err := env(ctx, "a larger picture")
	if err != nil {
		return nil, err
	}
	defer done()
	s := Stores(e)
	p, ok := s.Find(UpscalePin)
	if !ok || s.Ready(p) != nil {
		return nil, notHere(a.Ready(e, req.Model))
	}
	run, err := OpenUpscaler(e, s, p)
	if err != nil {
		return nil, notHere(err)
	}
	f, err := os.Open(in.Path)
	if err != nil {
		return nil, err
	}
	src, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "MetaRush couldn't read the picture."}
	}
	scale := req.Options.Scale
	if scale == 0 {
		scale = 2
	}
	if scale != 2 && scale != 4 {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "Pictures are made 2 or 4 times larger."}
	}
	big, err := Upscale(ctx, run, src, scale)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&b, big); err != nil {
		return nil, err
	}
	st := &aiservices.Status{State: aiservices.StateDone, Outputs: []aiservices.Output{{MIME: "image/png", Data: b.Bytes()}}, Usage: aiservices.Usage{Known: true}}
	return ready(st), nil
}

// Upscale makes a picture scale (2 or 4) times larger with the model, tile
// by tile: each 128-pixel tile overlaps its neighbours by 8 pixels, and only
// its middle is kept, so tiles meet without seams. See-through parts are
// made larger smoothly and put back.
func Upscale(ctx context.Context, run Runner, src image.Image, scale int) (*image.NRGBA, error) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil, fmt.Errorf("the picture is empty")
	}
	// The picture, padded by repeating its edges to at least one tile.
	pw, ph := max(w, tile), max(h, tile)
	rgb := make([]float32, 3*pw*ph)
	alpha := false
	for y := 0; y < ph; y++ {
		for x := 0; x < pw; x++ {
			c := color.NRGBAModel.Convert(src.At(b.Min.X+min(x, w-1), b.Min.Y+min(y, h-1))).(color.NRGBA)
			if c.A != 255 {
				alpha = true
			}
			i := y*pw + x
			rgb[i], rgb[pw*ph+i], rgb[2*pw*ph+i] = float32(c.R)/255, float32(c.G)/255, float32(c.B)/255
		}
	}
	out := image.NewNRGBA(image.Rect(0, 0, w*factor, h*factor))
	step := tile - 2*overlap
	in := make([]float32, 3*tile*tile)
	for ty := 0; ty < h; ty += step {
		for tx := 0; tx < w; tx += step {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// The window: the step's pixels with context around them, kept inside the picture.
			sx := min(max(tx-overlap, 0), pw-tile)
			sy := min(max(ty-overlap, 0), ph-tile)
			for c := 0; c < 3; c++ {
				for y := 0; y < tile; y++ {
					copy(in[c*tile*tile+y*tile:c*tile*tile+(y+1)*tile], rgb[c*pw*ph+(sy+y)*pw+sx:c*pw*ph+(sy+y)*pw+sx+tile])
				}
			}
			res, err := run(in)
			if err != nil {
				return nil, err
			}
			const T = tile * factor
			for y := ty; y < min(ty+step, h); y++ {
				for x := tx; x < min(tx+step, w); x++ {
					for dy := 0; dy < factor; dy++ {
						oy := (y-sy)*factor + dy
						row := out.Pix[(y*factor+dy)*out.Stride:]
						for dx := 0; dx < factor; dx++ {
							ox := (x-sx)*factor + dx
							o := (x*factor + dx) * 4
							row[o] = byteOf(res[oy*T+ox])
							row[o+1] = byteOf(res[T*T+oy*T+ox])
							row[o+2] = byteOf(res[2*T*T+oy*T+ox])
							row[o+3] = 255
						}
					}
				}
			}
		}
	}
	if alpha {
		// The see-through parts, made larger smoothly.
		a := image.NewAlpha(image.Rect(0, 0, w, h))
		draw.Draw(a, a.Rect, src, b.Min, draw.Src)
		big := image.NewAlpha(out.Rect)
		draw.CatmullRom.Scale(big, big.Rect, a, a.Rect, draw.Src, nil)
		for i := 0; i < len(big.Pix); i++ {
			out.Pix[i*4+3] = big.Pix[i]
		}
	}
	if scale == factor {
		return out, nil
	}
	half := image.NewNRGBA(image.Rect(0, 0, w*scale, h*scale))
	draw.CatmullRom.Scale(half, half.Rect, out, out.Rect, draw.Src, nil)
	return half, nil
}

func byteOf(v float32) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 1:
		return 255
	}
	return uint8(v*255 + 0.5)
}
