package visionmodels

import (
	"math"

	"github.com/metavert-io/metarush/internal/faceid"
)

// How usable a face is as a picture of the face (Apple Vision's capture
// quality, which the index uses to pick each face's best frames and to leave
// blurred faces unfingerprinted, D114). Nothing here measures it, so it is
// estimated from what makes a face a good picture (D407): how sure YuNet is,
// how sharp the face is once aligned, how frontal it is, and how big; scaled
// so faces the helper rates as usable come out alike.

const (
	// qualitySharp: the aligned face's sharpness (variance of its Laplacian)
	// at which sharpness stops counting against it.
	qualitySharp = 400.0
	// qualityEyes: the distance between the eyes (pixels) from which size
	// stops counting against it.
	qualityEyes = 48.0
	// qualityTop is what a sharp, frontal, large face YuNet is sure of scores
	// (Apple Vision's capture quality of such faces in the fixtures).
	qualityTop = 0.72
)

// faceQuality estimates how usable a face is as a picture, 0–1.
func faceQuality(p *picture, fa faceAt) float64 {
	pts := ordered(fa.pts)
	eyes := math.Hypot(pts[1][0]-pts[0][0], pts[1][1]-pts[0][1])
	if eyes < 2 {
		return 0
	}
	// Frontal: the nose sits between the eyes; a face turned away has it near one eye.
	mx, my := (pts[0][0]+pts[1][0])/2, (pts[0][1]+pts[1][1])/2
	ux, uy := (pts[1][0]-pts[0][0])/eyes, (pts[1][1]-pts[0][1])/eyes
	turn := math.Abs((pts[2][0]-mx)*ux+(pts[2][1]-my)*uy) / eyes
	pose := clamp01(1 - turn/0.7)
	size := clamp01(0.35 + 0.65*eyes/qualityEyes)
	sharp := clamp01(math.Log10(1+alignedSharpness(p, pts)) / math.Log10(1+qualitySharp))
	return clamp01(qualityTop * math.Sqrt(fa.score) * sharp * pose * size)
}

// alignedSharpness is the variance of the Laplacian of the face aligned onto
// face recognition's 112×112 template (so faces of every size compare), over
// its middle (eyes to mouth).
func alignedSharpness(p *picture, pts [5][2]float64) float64 {
	inv := faceid.Similarity(pts).Invert()
	const n = faceid.Size
	g := make([]float64, n*n)
	for v := 0; v < n; v++ {
		for u := 0; u < n; u++ {
			x, y := inv.Apply(float64(u), float64(v))
			g[v*n+u] = p.grayAt(x, y)
		}
	}
	var sum, sq float64
	cnt := 0
	for v := 30; v < 100; v++ {
		for u := 24; u < 88; u++ {
			l := 4*g[v*n+u] - g[(v-1)*n+u] - g[(v+1)*n+u] - g[v*n+u-1] - g[v*n+u+1]
			sum += l
			sq += l * l
			cnt++
		}
	}
	m := sum / float64(cnt)
	return sq/float64(cnt) - m*m
}

// grayAt is the brightness at (x, y), bilinear, pixel centres on whole numbers
// (black outside the picture, as the aligned face has it).
func (p *picture) grayAt(x, y float64) float64 {
	x0, y0 := math.Floor(x), math.Floor(y)
	fx, fy := x-x0, y-y0
	ix, iy := int(x0), int(y0)
	at := func(x, y int) float64 {
		if x < 0 || y < 0 || x >= p.w || y >= p.h {
			return 0
		}
		i := 3 * (y*p.w + x)
		return 0.299*float64(p.px[i]) + 0.587*float64(p.px[i+1]) + 0.114*float64(p.px[i+2])
	}
	return at(ix, iy)*(1-fx)*(1-fy) + at(ix+1, iy)*fx*(1-fy) + at(ix, iy+1)*(1-fx)*fy + at(ix+1, iy+1)*fx*fy
}
