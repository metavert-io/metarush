package visionmodels

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/onnx"
)

// A picture's subject is cut out with BiRefNet (Zheng et al., "Bilateral
// Reference for High-Resolution Dichotomous Image Segmentation", CAAI AIR
// 2024; MIT), the general model on its lighter backbone: it reads the picture
// at 1024×1024 and answers how sure it is, per pixel, that the pixel belongs
// to the subject — people, animals and things alike, as Apple Vision's
// subject lifting does (D414). Masker is made to be run frame after frame
// (background removal for video builds on it): the model is loaded once, and
// each call takes a picture and returns its mask at the picture's size.

const maskSide = 1024

// maskThreads: a cut-out is one big picture; it may use more of the computer.
const maskThreads = 4

// Masker cuts subjects out with one loaded model.
type Masker struct {
	m  *onnx.Model
	mu sync.Mutex
}

var (
	maskMu    sync.Mutex
	maskCache = map[string]*Masker{}
)

// OpenMasker loads the subject model with onnxruntime (lib) once per process.
func OpenMasker(lib, model string) (*Masker, error) {
	if lib == "" {
		return nil, ErrNoRuntime
	}
	maskMu.Lock()
	defer maskMu.Unlock()
	key := lib + "\x00" + model
	if m, ok := maskCache[key]; ok {
		return m, nil
	}
	om, err := onnx.Open(lib, model, maskThreads)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(model), err)
	}
	m := &Masker{m: om}
	maskCache[key] = m
	return m, nil
}

// Mask is how sure the model is that each pixel of img is the subject, 0–255,
// at img's own size.
func (k *Masker) Mask(img image.Image) (*image.Gray, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	p := toPicture(img)
	if p.w == 0 || p.h == 0 {
		return nil, errors.New("the picture is empty")
	}
	q := p.resized(maskSide, maskSide)
	outs, err := k.m.Run(q.tensor(imageNet, maskSide, maskSide), []int64{1, 3, maskSide, maskSide})
	if err != nil {
		return nil, err
	}
	if len(outs) == 0 || len(outs[0].Data) < maskSide*maskSide {
		return nil, errors.New("the subject model answered in an unexpected shape")
	}
	// The answer is a score per pixel; its sigmoid is how sure.
	sure := &picture{w: maskSide, h: maskSide, px: make([]float32, 3*maskSide*maskSide)}
	for i, v := range outs[0].Data[:maskSide*maskSide] {
		s := sigmoid(v) * 255
		sure.px[3*i], sure.px[3*i+1], sure.px[3*i+2] = s, s, s
	}
	back := sure.resized(p.w, p.h)
	out := image.NewGray(image.Rect(0, 0, p.w, p.h))
	for i := 0; i < p.w*p.h; i++ {
		out.Pix[i] = uint8(math.Max(0, math.Min(255, math.Round(float64(back.px[3*i])))))
	}
	return out, nil
}

// subjectMin: a subject covers at least this share of the picture, or there
// is none (the model answers faint guesses on an empty wall).
const subjectMin = 0.002

// Found reports whether a mask holds a subject.
func Found(mask *image.Gray) bool {
	n := 0
	for _, v := range mask.Pix {
		if v >= 128 {
			n++
		}
	}
	return float64(n) >= subjectMin*float64(len(mask.Pix))
}

// CutOut writes the picture at in with its background see-through to out (a
// PNG the picture's size), as the helper's mask command does; found is false
// when there is no subject (nothing is written).
func (k *Masker) CutOut(in, out string) (bool, error) {
	for _, p := range []string{in, out} {
		if !filepath.IsAbs(p) || strings.HasPrefix(filepath.Base(p), "-") {
			return false, fmt.Errorf("refusing picture path %q", p)
		}
	}
	fh, err := os.Open(in)
	if err != nil {
		return false, err
	}
	img, _, err := image.Decode(fh)
	fh.Close()
	if err != nil {
		return false, fmt.Errorf("the picture can't be read: %w", err)
	}
	mask, err := k.Mask(img)
	if err != nil {
		return false, err
	}
	if !Found(mask) {
		return false, nil
	}
	b := img.Bounds()
	cut := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			a := mask.Pix[y*mask.Stride+x]
			c.A = uint8(uint16(c.A) * uint16(a) / 255)
			cut.SetNRGBA(x, y, c)
		}
	}
	f, err := os.CreateTemp(filepath.Dir(out), "."+filepath.Base(out)+".*")
	if err != nil {
		return false, err
	}
	tmp := f.Name()
	err = png.Encode(f, cut)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, out)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	return true, nil
}
