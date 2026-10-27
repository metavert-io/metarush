// Package faceid recognizes faces across clips (D114): it turns a face in a
// frame into a face fingerprint with SFace (Zhong et al., "SFace:
// Sigmoid-Constrained Hypersphere Loss for Robust Face Recognition", IEEE TIP
// 2021; the MobileFaceNet model OpenCV Zoo publishes under Apache-2.0), run on
// this machine through onnxruntime (internal/onnx), and matches fingerprints
// against the faces people confirmed, the way voices are matched
// (internal/voice): only confirmed faces enroll, an automatic name needs a high
// score and a clear lead over everyone else, anything in between is a
// question, and every correction re-scores the library (internal/faces).
//
// A face is aligned before it is fingerprinted: five points (the eyes, the tip
// of the nose and the mouth corners, from Apple Vision's landmarks on a Mac or
// YuNet elsewhere) are mapped onto the 112×112 template SFace was trained with,
// by the least-squares similarity transform (rotation, one scale, shift) that
// OpenCV's FaceRecognizerSF.alignCrop uses, and the picture is resampled
// bilinearly.
package faceid

import (
	"image"
	"image/color"
	"math"
)

// Size is the side of the aligned face SFace reads.
const Size = 112

// template is where the five points sit in an aligned face (pixels of the
// 112×112 crop): the eye on the picture's left, the other eye, the nose tip,
// the left and right mouth corners (the ArcFace template OpenCV uses).
var template = [5][2]float64{{38.2946, 51.6963}, {73.5318, 51.5014}, {56.0252, 71.7366}, {41.5493, 92.3655}, {70.7299, 92.2041}}

// Affine is a 2×3 transform: x' = A·x + B·y + C, y' = D·x + E·y + F.
type Affine [6]float64

// Apply maps a point.
func (m Affine) Apply(x, y float64) (float64, float64) {
	return m[0]*x + m[1]*y + m[2], m[3]*x + m[4]*y + m[5]
}

// Invert is the inverse transform (the zero transform when m is singular).
func (m Affine) Invert() Affine {
	det := m[0]*m[4] - m[1]*m[3]
	if math.Abs(det) < 1e-12 {
		return Affine{}
	}
	a, b, c, d, e, f := m[4]/det, -m[1]/det, 0.0, -m[3]/det, m[0]/det, 0.0
	c = -(a*m[2] + b*m[5])
	f = -(d*m[2] + e*m[5])
	return Affine{a, b, c, d, e, f}
}

// Similarity is the rotation, uniform scale and shift that best maps the five
// points src (pixels of the frame) onto the template (least squares, no
// mirroring; the same answer as Umeyama's method OpenCV uses when the points
// are not mirrored).
func Similarity(src [5][2]float64) Affine {
	var mx, my, dx, dy float64
	for i := 0; i < 5; i++ {
		mx += src[i][0]
		my += src[i][1]
		dx += template[i][0]
		dy += template[i][1]
	}
	mx, my, dx, dy = mx/5, my/5, dx/5, dy/5
	var a, b, n float64
	for i := 0; i < 5; i++ {
		px, py := src[i][0]-mx, src[i][1]-my
		qx, qy := template[i][0]-dx, template[i][1]-dy
		a += px*qx + py*qy
		b += px*qy - py*qx
		n += px*px + py*py
	}
	if n < 1e-9 {
		return Affine{}
	}
	sc := math.Hypot(a, b) / n
	th := math.Atan2(b, a)
	c, s := sc*math.Cos(th), sc*math.Sin(th)
	return Affine{c, -s, dx - (c*mx - s*my), s, c, dy - (s*mx + c*my)}
}

// rgbAt reads a pixel as 0–255 floats (black outside the picture).
type sampler struct {
	ycc  *image.YCbCr
	rgba *image.RGBA
	img  image.Image
	b    image.Rectangle
}

func newSampler(img image.Image) *sampler {
	s := &sampler{img: img, b: img.Bounds()}
	switch v := img.(type) {
	case *image.YCbCr:
		s.ycc = v
	case *image.RGBA:
		s.rgba = v
	}
	return s
}

func (s *sampler) px(x, y int) (r, g, b float64) {
	if x < s.b.Min.X || y < s.b.Min.Y || x >= s.b.Max.X || y >= s.b.Max.Y {
		return 0, 0, 0
	}
	switch {
	case s.ycc != nil:
		yi, ci := s.ycc.YOffset(x, y), s.ycc.COffset(x, y)
		R, G, B := color.YCbCrToRGB(s.ycc.Y[yi], s.ycc.Cb[ci], s.ycc.Cr[ci])
		return float64(R), float64(G), float64(B)
	case s.rgba != nil:
		i := s.rgba.PixOffset(x, y)
		return float64(s.rgba.Pix[i]), float64(s.rgba.Pix[i+1]), float64(s.rgba.Pix[i+2])
	}
	R, G, B, _ := s.img.At(x, y).RGBA()
	return float64(R >> 8), float64(G >> 8), float64(B >> 8)
}

// bilinear samples at (x, y) with pixel centers on whole numbers (OpenCV's convention).
func (s *sampler) bilinear(x, y float64) (float64, float64, float64) {
	x0, y0 := math.Floor(x), math.Floor(y)
	fx, fy := x-x0, y-y0
	ix, iy := int(x0), int(y0)
	r00, g00, b00 := s.px(ix, iy)
	r10, g10, b10 := s.px(ix+1, iy)
	r01, g01, b01 := s.px(ix, iy+1)
	r11, g11, b11 := s.px(ix+1, iy+1)
	w00, w10, w01, w11 := (1-fx)*(1-fy), fx*(1-fy), (1-fx)*fy, fx*fy
	return r00*w00 + r10*w10 + r01*w01 + r11*w11, g00*w00 + g10*w10 + g01*w01 + g11*w11, b00*w00 + b10*w10 + b01*w01 + b11*w11
}

// Points turns five landmarks given 0–1 from the top-left of a w×h picture
// into pixel positions (pixel centers on whole numbers).
func Points(lm [][2]float64, w, h int) ([5][2]float64, bool) {
	var out [5][2]float64
	if len(lm) != 5 || w <= 0 || h <= 0 {
		return out, false
	}
	for i, p := range lm {
		out[i] = [2]float64{p[0]*float64(w) - 0.5, p[1]*float64(h) - 0.5}
	}
	return out, true
}

// AlignedTensor cuts the face out of img by its five points (pixels) into
// SFace's input: 1×3×112×112, red, green, blue, 0–255 (no mean, no scaling).
func AlignedTensor(img image.Image, pts [5][2]float64) []float32 {
	inv := Similarity(pts).Invert()
	s := newSampler(img)
	out := make([]float32, 3*Size*Size)
	for v := 0; v < Size; v++ {
		for u := 0; u < Size; u++ {
			x, y := inv.Apply(float64(u), float64(v))
			r, g, b := s.bilinear(x, y)
			i := v*Size + u
			out[i] = float32(r)
			out[Size*Size+i] = float32(g)
			out[2*Size*Size+i] = float32(b)
		}
	}
	return out
}

// AlignedImage is the aligned face as a picture (debugging and tests).
func AlignedImage(img image.Image, pts [5][2]float64) *image.RGBA {
	t := AlignedTensor(img, pts)
	out := image.NewRGBA(image.Rect(0, 0, Size, Size))
	for i := 0; i < Size*Size; i++ {
		out.Pix[4*i] = clamp8(t[i])
		out.Pix[4*i+1] = clamp8(t[Size*Size+i])
		out.Pix[4*i+2] = clamp8(t[2*Size*Size+i])
		out.Pix[4*i+3] = 255
	}
	return out
}

func clamp8(v float32) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}

// EyeDistance is the distance between the eyes in pixels: below about 20 the
// face is too small to recognize reliably.
func EyeDistance(pts [5][2]float64) float64 {
	return math.Hypot(pts[1][0]-pts[0][0], pts[1][1]-pts[0][1])
}
