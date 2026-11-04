package visionmodels

import (
	"image"
	"image/color"
	"math"
)

// picture is an image as 0–255 floats, red, green and blue per pixel, row by
// row: what every model here reads, made once per frame.
type picture struct {
	w, h int
	px   []float32
}

// toPicture copies an image into a picture (fast paths for what JPEG and PNG
// decode to).
func toPicture(img image.Image) *picture {
	b := img.Bounds()
	p := &picture{w: b.Dx(), h: b.Dy()}
	p.px = make([]float32, 3*p.w*p.h)
	i := 0
	switch v := img.(type) {
	case *image.YCbCr:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				yi, ci := v.YOffset(x, y), v.COffset(x, y)
				r, g, bb := color.YCbCrToRGB(v.Y[yi], v.Cb[ci], v.Cr[ci])
				p.px[i], p.px[i+1], p.px[i+2] = float32(r), float32(g), float32(bb)
				i += 3
			}
		}
	case *image.RGBA:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			o := v.PixOffset(b.Min.X, y)
			for x := 0; x < p.w; x++ {
				p.px[i], p.px[i+1], p.px[i+2] = float32(v.Pix[o]), float32(v.Pix[o+1]), float32(v.Pix[o+2])
				i, o = i+3, o+4
			}
		}
	case *image.NRGBA:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			o := v.PixOffset(b.Min.X, y)
			for x := 0; x < p.w; x++ {
				// A see-through pixel is read over white, as a picture is shown.
				a := float32(v.Pix[o+3]) / 255
				p.px[i] = float32(v.Pix[o])*a + 255*(1-a)
				p.px[i+1] = float32(v.Pix[o+1])*a + 255*(1-a)
				p.px[i+2] = float32(v.Pix[o+2])*a + 255*(1-a)
				i, o = i+3, o+4
			}
		}
	case *image.Gray:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			o := v.PixOffset(b.Min.X, y)
			for x := 0; x < p.w; x++ {
				g := float32(v.Pix[o])
				p.px[i], p.px[i+1], p.px[i+2] = g, g, g
				i, o = i+3, o+1
			}
		}
	default:
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
				a := float32(c.A) / 255
				p.px[i] = float32(c.R)*a + 255*(1-a)
				p.px[i+1] = float32(c.G)*a + 255*(1-a)
				p.px[i+2] = float32(c.B)*a + 255*(1-a)
				i += 3
			}
		}
	}
	return p
}

// resized is the picture at w×h: area-averaged when it shrinks (every source
// pixel counts, so small text and faces don't alias away), bilinear with
// pixel centres aligned when it grows (OpenCV's INTER_AREA and INTER_LINEAR).
func (p *picture) resized(w, h int) *picture {
	if w == p.w && h == p.h {
		return p
	}
	return p.resizeV(h).resizeH(w)
}

// resizeH changes the width only.
func (p *picture) resizeH(w int) *picture {
	if w == p.w {
		return p
	}
	out := &picture{w: w, h: p.h, px: make([]float32, 3*w*p.h)}
	taps := weights(p.w, w)
	for y := 0; y < p.h; y++ {
		src := p.px[3*y*p.w : 3*(y+1)*p.w]
		dst := out.px[3*y*w : 3*(y+1)*w]
		for x, tp := range taps {
			var r, g, b float32
			for _, t := range tp {
				s := src[3*t.i:]
				r += s[0] * t.w
				g += s[1] * t.w
				b += s[2] * t.w
			}
			dst[3*x], dst[3*x+1], dst[3*x+2] = r, g, b
		}
	}
	return out
}

// resizeV changes the height only.
func (p *picture) resizeV(h int) *picture {
	if h == p.h {
		return p
	}
	out := &picture{w: p.w, h: h, px: make([]float32, 3*p.w*h)}
	taps := weights(p.h, h)
	row := 3 * p.w
	for y, tp := range taps {
		dst := out.px[y*row : (y+1)*row]
		for _, t := range tp {
			src := p.px[t.i*row : (t.i+1)*row]
			for k := range dst {
				dst[k] += src[k] * t.w
			}
		}
	}
	return out
}

type tap struct {
	i int
	w float32
}

// weights are, for each destination position, the source positions it reads
// and how much of each.
func weights(from, to int) [][]tap {
	out := make([][]tap, to)
	scale := float64(from) / float64(to)
	if scale > 1 {
		// Shrinking: each destination pixel is the average of the source
		// stretch it covers, part pixels counted by how much is covered.
		for d := 0; d < to; d++ {
			a, b := float64(d)*scale, float64(d+1)*scale
			var tp []tap
			for s := int(math.Floor(a)); s < int(math.Ceil(b)) && s < from; s++ {
				cover := math.Min(b, float64(s+1)) - math.Max(a, float64(s))
				if cover > 1e-9 {
					tp = append(tp, tap{s, float32(cover / scale)})
				}
			}
			out[d] = tp
		}
		return out
	}
	for d := 0; d < to; d++ {
		x := (float64(d)+0.5)*scale - 0.5
		x0 := math.Floor(x)
		f := x - x0
		i0, i1 := int(x0), int(x0)+1
		i0 = min(max(i0, 0), from-1)
		i1 = min(max(i1, 0), from-1)
		if i0 == i1 {
			out[d] = []tap{{i0, 1}}
			continue
		}
		out[d] = []tap{{i0, float32(1 - f)}, {i1, float32(f)}}
	}
	return out
}

// crop is the part of the picture inside [x0, x1) × [y0, y1), clamped to it.
func (p *picture) crop(x0, y0, x1, y1 int) *picture {
	x0, y0 = max(x0, 0), max(y0, 0)
	x1, y1 = min(x1, p.w), min(y1, p.h)
	if x1 <= x0 || y1 <= y0 {
		return &picture{}
	}
	w, h := x1-x0, y1-y0
	out := &picture{w: w, h: h, px: make([]float32, 3*w*h)}
	for y := 0; y < h; y++ {
		copy(out.px[3*y*w:3*(y+1)*w], p.px[3*((y0+y)*p.w+x0):3*((y0+y)*p.w+x1)])
	}
	return out
}

// norm says how a model reads pixels: which of red, green and blue goes in
// each channel, and (v·scale − mean) / std per channel.
type norm struct {
	order     [3]int // the picture's channel each tensor channel takes (0 red, 1 green, 2 blue)
	scale     float32
	mean, std [3]float32
}

var (
	// rawBGR: 0–255, blue first (YuNet, as OpenCV feeds it).
	rawBGR = norm{order: [3]int{2, 1, 0}, scale: 1, std: [3]float32{1, 1, 1}}
	// paddleDet: PaddleOCR's detector, blue first, with ImageNet's numbers
	// taken in that order (as PaddleOCR does).
	paddleDet = norm{order: [3]int{2, 1, 0}, scale: 1.0 / 255, mean: [3]float32{0.485, 0.456, 0.406}, std: [3]float32{0.229, 0.224, 0.225}}
	// paddleRec: PaddleOCR's recognizer, blue first, to −1…1.
	paddleRec = norm{order: [3]int{2, 1, 0}, scale: 1.0 / 255, mean: [3]float32{0.5, 0.5, 0.5}, std: [3]float32{0.5, 0.5, 0.5}}
	// imageNet: red first, ImageNet's mean and spread (U²-Net-p, BiRefNet).
	imageNet = norm{order: [3]int{0, 1, 2}, scale: 1.0 / 255, mean: [3]float32{0.485, 0.456, 0.406}, std: [3]float32{0.229, 0.224, 0.225}}
)

// tensor writes the picture into a 1×3×H×W tensor of the given size: the
// picture at the top left, the rest zero.
func (p *picture) tensor(n norm, W, H int) []float32 {
	out := make([]float32, 3*W*H)
	plane := W * H
	for c := 0; c < 3; c++ {
		src := n.order[c]
		sc, mean, std := n.scale, n.mean[c], n.std[c]
		dst := out[c*plane : (c+1)*plane]
		for y := 0; y < min(p.h, H); y++ {
			row := p.px[3*y*p.w:]
			d := dst[y*W:]
			for x := 0; x < min(p.w, W); x++ {
				d[x] = (row[3*x+src]*sc - mean) / std
			}
		}
	}
	return out
}

// gray is the picture's brightness (0–255), for measuring sharpness.
func (p *picture) gray() []float32 {
	out := make([]float32, p.w*p.h)
	for i := range out {
		out[i] = 0.299*p.px[3*i] + 0.587*p.px[3*i+1] + 0.114*p.px[3*i+2]
	}
	return out
}

// sigmoid of a value.
func sigmoid(v float32) float32 { return float32(1 / (1 + math.Exp(-float64(v)))) }

// clamp01 keeps a value inside 0…1.
func clamp01(v float64) float64 { return math.Max(0, math.Min(1, v)) }

// r3 rounds to three decimals, like the helper's output.
func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

// r4 rounds to four decimals (landmarks).
func r4(v float64) float64 { return math.Round(v*10000) / 10000 }

// roundUp is n rounded up to a multiple of m.
func roundUp(n, m int) int { return (n + m - 1) / m * m }
