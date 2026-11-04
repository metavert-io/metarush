package matting

import "math"

// Mattes made frame by frame shimmer at the edges: a model answers each
// frame on its own, so a hair's breadth of difference in a still picture
// moves the edge back and forth. The smoother (D508) blends each matte with
// the one before where the picture held still — how much depends, pixel by
// pixel, on how much the picture changed there — and follows the new matte
// where the picture moved, so a moving person keeps up and a still edge
// holds. A cut in the footage (most of the picture changing at once) starts
// afresh.
const (
	// smoothKeep is how much of the previous matte a pixel keeps where the
	// picture didn't change at all.
	smoothKeep = 0.65
	// smoothChange is how much change in brightness (0–255) halves what a
	// pixel keeps: picture movement wins quickly.
	smoothChange = 6.0
	// cutChange: a frame whose brightness changed by this much on average is
	// another shot; nothing is carried over.
	cutChange = 28.0
	// stillChange: a pixel whose brightness changed less than this held still
	// (for measuring flicker).
	stillChange = 3
)

type smoother struct {
	w, h                    int
	prevLuma                []float32
	prevOut                 []float32
	prevRaw                 []byte
	keep                    [256]float32
	flickBefore, flickAfter float64
	flickN                  int
}

func newSmoother(w, h int) *smoother {
	s := &smoother{w: w, h: h}
	for d := range s.keep {
		s.keep[d] = float32(smoothKeep * math.Exp2(-float64(d)/smoothChange))
	}
	return s
}

// luma is a frame's brightness, 0–255, per pixel.
func luma(frame []byte, px int, format string, out []float32) {
	r, g, b := 0, 1, 2
	if format == "bgra" {
		r, b = 2, 0
	}
	for i := range out {
		o := i * px
		out[i] = 0.299*float32(frame[o+r]) + 0.587*float32(frame[o+g]) + 0.114*float32(frame[o+b])
	}
}

// next is the smoothed matte of a frame given the model's raw one.
func (s *smoother) next(raw, frame []byte, px int, format string) []byte {
	n := s.w * s.h
	cur := make([]float32, n)
	luma(frame, px, format, cur)
	out := make([]byte, n)
	if s.prevLuma == nil {
		s.prevOut = make([]float32, n)
		for i, v := range raw {
			s.prevOut[i] = float32(v)
			out[i] = v
		}
		s.prevLuma, s.prevRaw = cur, append([]byte(nil), raw...)
		return out
	}
	// A cut: most of the picture changed.
	total := 0.0
	for i := 0; i < n; i += 7 {
		total += math.Abs(float64(cur[i] - s.prevLuma[i]))
	}
	if total/float64((n+6)/7) > cutChange {
		for i, v := range raw {
			s.prevOut[i] = float32(v)
			out[i] = v
		}
		s.prevLuma, s.prevRaw = cur, append(s.prevRaw[:0], raw...)
		return out
	}
	var before, after float64
	edges := 0
	for i := 0; i < n; i++ {
		d := cur[i] - s.prevLuma[i]
		if d < 0 {
			d = -d
		}
		k := s.keep[min(255, int(d))]
		v := (1-k)*float32(raw[i]) + k*s.prevOut[i]
		o := uint8(v + 0.5)
		if v >= 255 {
			o = 255
		}
		if d < stillChange && (inEdge(raw[i]) || inEdge(s.prevRaw[i])) {
			before += math.Abs(float64(raw[i]) - float64(s.prevRaw[i]))
			after += math.Abs(float64(o) - float64(uint8(s.prevOut[i]+0.5)))
			edges++
		}
		s.prevOut[i] = v
		out[i] = o
	}
	if edges > 0 {
		s.flickBefore += before / float64(edges)
		s.flickAfter += after / float64(edges)
		s.flickN++
	}
	s.prevLuma, s.prevRaw = cur, append(s.prevRaw[:0], raw...)
	return out
}

// inEdge reports a matte value at a soft edge (neither background nor person).
func inEdge(v byte) bool { return v > 8 && v < 247 }

// flicker is the mean frame-to-frame change at still edges, before (false)
// or after (true) smoothing.
func (s *smoother) flicker(smoothed bool) float64 {
	if s.flickN == 0 {
		return 0
	}
	if smoothed {
		return math.Round(s.flickAfter/float64(s.flickN)*100) / 100
	}
	return math.Round(s.flickBefore/float64(s.flickN)*100) / 100
}
