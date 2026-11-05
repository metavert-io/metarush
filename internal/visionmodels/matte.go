package visionmodels

import (
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"sync"

	"github.com/metavert-io/metarush/internal/onnx"
)

// A person is told from their background in footage with MODNet (Ke et al.,
// "MODNet: Real-Time Trimap-Free Portrait Matting via Objective
// Decomposition", AAAI 2022; Apache-2.0, code and models): it reads a frame
// with its short side at 512 and answers, per pixel, how much of the pixel is
// the person (a matte: hair and soft edges come out part see-through, not cut
// hard). It is small (26 MB) and quick enough to read every frame of a long
// clip, which BiRefNet at 1024² is not (D506). Video background removal reads
// one matte per frame and smooths them over time (internal/mattes).

// matteShort is the short side MODNet reads frames at (its own reference size).
const matteShort = 512

// matteThreads: a matte is made frame after frame, one model at a time.
const matteThreads = 4

// MatteSize is the size MODNet reads a w×h frame at, as its own inference
// does: frames bigger than 512 on their short side, or smaller than 512 on
// their long side, are scaled so the short side is 512; then each side is
// rounded down to a multiple of 32.
func MatteSize(w, h int) (int, int) {
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	rw, rh := w, h
	if max(w, h) < matteShort || min(w, h) > matteShort {
		if w >= h {
			rh, rw = matteShort, int(float64(w)/float64(h)*matteShort)
		} else {
			rw, rh = matteShort, int(float64(h)/float64(w)*matteShort)
		}
	}
	rw -= rw % 32
	rh -= rh % 32
	return max(rw, 32), max(rh, 32)
}

// Matter makes mattes with one loaded model.
type Matter struct {
	m  *onnx.Model
	mu sync.Mutex
}

var (
	matterMu    sync.Mutex
	matterCache = map[string]*Matter{}
)

// OpenMatter loads the matting model with onnxruntime (lib) once per process.
func OpenMatter(lib, model string) (*Matter, error) {
	if lib == "" {
		return nil, ErrNoRuntime
	}
	matterMu.Lock()
	defer matterMu.Unlock()
	key := lib + "\x00" + model
	if m, ok := matterCache[key]; ok {
		return m, nil
	}
	om, err := onnx.Open(lib, model, matteThreads)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(model), err)
	}
	m := &Matter{m: om}
	matterCache[key] = m
	return m, nil
}

// MatteRGB is the matte of one frame: rgb holds w×h pixels, red, green and
// blue bytes row by row, at MatteSize; the answer is w×h bytes, 0 for the
// background and 255 for the person.
func (k *Matter) MatteRGB(rgb []byte, w, h int) ([]byte, error) {
	if w <= 0 || h <= 0 || len(rgb) < 3*w*h {
		return nil, errors.New("the frame is empty or cut short")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	// MODNet's input: red first, (v/255 − 0.5) / 0.5.
	plane := w * h
	in := make([]float32, 3*plane)
	for i := 0; i < plane; i++ {
		in[i] = float32(rgb[3*i])/127.5 - 1
		in[plane+i] = float32(rgb[3*i+1])/127.5 - 1
		in[2*plane+i] = float32(rgb[3*i+2])/127.5 - 1
	}
	outs, err := k.m.Run(in, []int64{1, 3, int64(h), int64(w)})
	if err != nil {
		return nil, err
	}
	if len(outs) == 0 || len(outs[0].Data) < plane {
		return nil, errors.New("the matting model answered in an unexpected shape")
	}
	out := make([]byte, plane)
	for i, v := range outs[0].Data[:plane] {
		out[i] = uint8(math.Max(0, math.Min(255, math.Round(float64(v)*255))))
	}
	return out, nil
}
