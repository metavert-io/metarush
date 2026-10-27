package fixes

import (
	"context"
	"math"
	"math/cmplx"
	"sort"

	"github.com/metavert-io/metarush/internal/tools"
)

// Picture measurements (D153). Each returns a number that is also measured on
// the fixed copy, so a person sees before and after.

// shakeWidth is the width the shake is measured at.
const shakeWidth = 320

// Shake is how much a camera shakes: the frame-to-frame motion (phase
// correlation of 256×128 centres of small frames) minus its own smooth trend,
// as a percentage of the picture's width. A steady camera that pans smoothly
// measures near 0; a hand-held phone 0.2–0.5; a walking shake 1 or more.
func Shake(ctx context.Context, ts *tools.Set, src string, duration float64) (float64, error) {
	return ShakeBetween(ctx, ts, src, duration, 0, nil)
}

// ShakeBetween is Shake for a recording whose picture switches (a call,
// D190): the motion is measured within each stretch between two switches
// (cuts, in seconds, at fps frames a second) and never across one — a switch
// would read as one huge jump — and the frames where the picture changes
// completely (a switch the list doesn't have) are left out the same way.
func ShakeBetween(ctx context.Context, ts *tools.Set, src string, duration, fps float64, cuts []float64) (float64, error) {
	windows := sampleWindows(duration, 4, 3)
	var all []float64
	for _, w := range windows {
		frames, fw, fh, err := grayRun(ctx, ts, src, w[0], w[1], shakeWidth)
		if err != nil {
			return 0, err
		}
		if len(frames) < 8 {
			continue
		}
		// Where the series breaks: before frame i when a switch falls between
		// frames i-1 and i (a frame either side, the switch may be a frame off),
		// or when the picture changes completely from one frame to the next.
		breaks := map[int]bool{}
		if fps > 0 {
			for _, c := range cuts {
				if c < w[0] || c > w[0]+w[1] {
					continue
				}
				i := int(math.Round((c - w[0]) * fps))
				for k := i - 1; k <= i+1; k++ {
					breaks[k] = true
				}
			}
		}
		if cuts != nil {
			diffs := make([]float64, len(frames))
			for i := 1; i < len(frames); i++ {
				diffs[i] = meanAbsDiff(frames[i-1], frames[i])
			}
			sorted := append([]float64(nil), diffs[1:]...)
			sort.Float64s(sorted)
			typical := sorted[len(sorted)/2]
			for i := 1; i < len(frames); i++ {
				if diffs[i] > 3*typical+12 {
					breaks[i] = true
				}
			}
		}
		var xs, ys []float64
		flush := func() {
			if len(xs) >= 6 {
				hx, hy := highPass(xs, 4), highPass(ys, 4)
				for i := range hx {
					all = append(all, hx[i]*hx[i]+hy[i]*hy[i])
				}
			}
			xs, ys = nil, nil
		}
		var prev []complex128
		for i, f := range frames {
			cur := spectrumOf(f, fw, fh)
			switch {
			case breaks[i]:
				flush()
			case prev != nil:
				dx, dy := phaseShift(prev, cur)
				xs = append(xs, dx)
				ys = append(ys, dy)
			}
			prev = cur
		}
		flush()
	}
	if len(all) == 0 {
		return 0, nil
	}
	sum := 0.0
	for _, v := range all {
		sum += v
	}
	return math.Sqrt(sum/float64(len(all))) / shakeWidth * 100, nil
}

// meanAbsDiff is how different two gray frames are, on average (0–255).
func meanAbsDiff(a, b []byte) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	s := 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		s += d
	}
	return float64(s) / float64(len(a))
}

// sampleWindows spreads n stretches of up to dur seconds over a clip: [from, dur].
func sampleWindows(duration float64, n int, dur float64) [][2]float64 {
	if duration <= 0 {
		return nil
	}
	if duration <= dur*float64(n) {
		return [][2]float64{{0, duration}}
	}
	var out [][2]float64
	for i := 0; i < n; i++ {
		from := (duration - dur) * (float64(i) + 0.5) / float64(n)
		out = append(out, [2]float64{from, dur})
	}
	return out
}

const pcW, pcH = 256, 128

// spectrumOf is the windowed 2-D spectrum of a frame's centre (pcW×pcH).
func spectrumOf(f []byte, w, h int) []complex128 {
	x0, y0 := (w-pcW)/2, (h-pcH)/2
	out := make([]complex128, pcW*pcH)
	for y := 0; y < pcH; y++ {
		sy := y0 + y
		wy := 0.5 - 0.5*math.Cos(2*math.Pi*float64(y)/float64(pcH-1))
		for x := 0; x < pcW; x++ {
			sx := x0 + x
			v := 0.0
			if sx >= 0 && sy >= 0 && sx < w && sy < h {
				v = float64(f[sy*w+sx])
			}
			wx := 0.5 - 0.5*math.Cos(2*math.Pi*float64(x)/float64(pcW-1))
			out[y*pcW+x] = complex(v*wx*wy, 0)
		}
	}
	fft2(out, pcW, pcH, false)
	return out
}

// phaseShift is how far b moved from a (subpixel), from their spectra.
func phaseShift(a, b []complex128) (float64, float64) {
	r := make([]complex128, len(a))
	for i := range a {
		c := b[i] * cmplx.Conj(a[i])
		if m := cmplx.Abs(c); m > 1e-9 {
			c /= complex(m, 0)
		}
		r[i] = c
	}
	fft2(r, pcW, pcH, true)
	best, bx, by := -math.MaxFloat64, 0, 0
	for y := 0; y < pcH; y++ {
		for x := 0; x < pcW; x++ {
			if v := real(r[y*pcW+x]); v > best {
				best, bx, by = v, x, y
			}
		}
	}
	at := func(x, y int) float64 { return real(r[((y+pcH)%pcH)*pcW+(x+pcW)%pcW]) }
	sub := func(l, c, rr float64) float64 {
		d := l - 2*c + rr
		if d == 0 {
			return 0
		}
		return 0.5 * (l - rr) / d
	}
	fx := float64(bx) + sub(at(bx-1, by), best, at(bx+1, by))
	fy := float64(by) + sub(at(bx, by-1), best, at(bx, by+1))
	if fx > pcW/2 {
		fx -= pcW
	}
	if fy > pcH/2 {
		fy -= pcH
	}
	return fx, fy
}

func fft(x []complex128, inverse bool) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for l := 2; l <= n; l <<= 1 {
		ang := -2 * math.Pi / float64(l)
		if inverse {
			ang = -ang
		}
		wl := cmplx.Rect(1, ang)
		for i := 0; i < n; i += l {
			w := complex(1, 0)
			for k := 0; k < l/2; k++ {
				u, v := x[i+k], x[i+k+l/2]*w
				x[i+k], x[i+k+l/2] = u+v, u-v
				w *= wl
			}
		}
	}
}

func fft2(x []complex128, w, h int, inverse bool) {
	row := make([]complex128, w)
	for y := 0; y < h; y++ {
		copy(row, x[y*w:(y+1)*w])
		fft(row, inverse)
		copy(x[y*w:], row)
	}
	col := make([]complex128, h)
	for c := 0; c < w; c++ {
		for y := 0; y < h; y++ {
			col[y] = x[y*w+c]
		}
		fft(col, inverse)
		for y := 0; y < h; y++ {
			x[y*w+c] = col[y]
		}
	}
}

// highPass is a series minus its Gaussian-smoothed trend (sigma in samples).
func highPass(v []float64, sigma float64) []float64 {
	n := len(v)
	out := make([]float64, n)
	r := int(math.Ceil(3 * sigma))
	for i := 0; i < n; i++ {
		sw, s := 0.0, 0.0
		for k := -r; k <= r; k++ {
			j := i + k
			if j < 0 || j >= n {
				continue
			}
			wt := math.Exp(-float64(k*k) / (2 * sigma * sigma))
			sw += wt
			s += wt * v[j]
		}
		out[i] = v[i] - s/sw
	}
	return out
}

// sampleTimes spreads n moments over a clip, away from its very ends.
func sampleTimes(duration float64, n int) []float64 {
	if duration <= 0 {
		return []float64{0}
	}
	out := make([]float64, n)
	for i := range out {
		out[i] = duration * (float64(i) + 0.5) / float64(n)
	}
	return out
}

// ColourFacts are the colour and brightness of a clip's picture.
type ColourFacts struct {
	// Cast is how far the average colour is from grey, relative to the
	// brightness (0: neutral; 0.06 or more: a visible cast).
	Cast float64
	// Brightness is the average brightness (0–1); Range the spread between
	// the darkest and brightest 1% (0–1).
	Brightness, Range float64
	// Low and High are each colour's darkest and brightest 0.5% (0–1), and
	// Mean its average over the mid-tones: what balancing works from.
	Low, High, Mean [3]float64
}

// Colour measures the picture on frames across a clip.
func Colour(ctx context.Context, ts *tools.Set, src string, duration float64) (*ColourFacts, error) {
	var hist [3][256]int
	var luma [256]int
	var sum [3]float64
	n := 0
	for _, t := range sampleTimes(duration, 12) {
		px, w, h, err := frameAt(ctx, ts, src, t, 160, -2, true)
		if err != nil {
			return nil, err
		}
		for i := 0; i < w*h; i++ {
			r, g, b := px[3*i], px[3*i+1], px[3*i+2]
			hist[0][r]++
			hist[1][g]++
			hist[2][b]++
			y := (299*int(r) + 587*int(g) + 114*int(b)) / 1000
			luma[y]++
			if y > 25 && y < 230 {
				sum[0] += float64(r)
				sum[1] += float64(g)
				sum[2] += float64(b)
				n++
			}
		}
	}
	f := &ColourFacts{}
	total := 0
	for _, c := range luma {
		total += c
	}
	if total == 0 {
		return f, nil
	}
	pct := func(hst *[256]int, p float64) float64 {
		want := int(math.Round(p * float64(total)))
		acc := 0
		for i, c := range hst {
			acc += c
			if acc >= want {
				return float64(i) / 255
			}
		}
		return 1
	}
	for c := 0; c < 3; c++ {
		f.Low[c], f.High[c] = pct(&hist[c], 0.005), pct(&hist[c], 0.995)
	}
	ysum := 0.0
	for i, c := range luma {
		ysum += float64(i) * float64(c)
	}
	f.Brightness = ysum / float64(total) / 255
	f.Range = pct(&luma, 0.99) - pct(&luma, 0.01)
	if n > 0 {
		for c := 0; c < 3; c++ {
			f.Mean[c] = sum[c] / float64(n) / 255
		}
		grey := (f.Mean[0] + f.Mean[1] + f.Mean[2]) / 3
		if grey > 0 {
			for c := 0; c < 3; c++ {
				f.Cast = math.Max(f.Cast, math.Abs(f.Mean[c]-grey)/grey)
			}
		}
	}
	return f, nil
}

// Grain is the picture's noise in 8-bit levels (Immerkær's estimate over the
// flattest tenth of 16×16 blocks, so texture doesn't count as grain): clean
// video measures about 1, grainy 3 or more.
func Grain(ctx context.Context, ts *tools.Set, src string, duration float64) (float64, error) {
	var blocks []float64
	for _, t := range sampleTimes(duration, 8) {
		// At the picture's own size (scaling would average the grain away),
		// the centre 960×540 at most.
		px, w, h, err := frameAt(ctx, ts, src, t, 0, 0, false)
		if err != nil {
			return 0, err
		}
		cw, ch := min(w, 960), min(h, 540)
		x0, y0 := (w-cw)/2, (h-ch)/2
		crop := make([]byte, cw*ch)
		for y := 0; y < ch; y++ {
			copy(crop[y*cw:(y+1)*cw], px[(y0+y)*w+x0:(y0+y)*w+x0+cw])
		}
		blocks = append(blocks, noiseBlocks(crop, cw, ch)...)
	}
	if len(blocks) == 0 {
		return 0, nil
	}
	sort.Float64s(blocks)
	return blocks[len(blocks)/10], nil
}

// noiseBlocks is Immerkær's noise estimate of each 16×16 block of a frame.
func noiseBlocks(px []byte, w, h int) []float64 {
	var out []float64
	const bs = 16
	for by := 1; by+bs < h-1; by += bs {
		for bx := 1; bx+bs < w-1; bx += bs {
			s := 0.0
			for y := by; y < by+bs; y++ {
				for x := bx; x < bx+bs; x++ {
					p := func(dx, dy int) float64 { return float64(px[(y+dy)*w+x+dx]) }
					v := p(-1, -1) - 2*p(0, -1) + p(1, -1) - 2*p(-1, 0) + 4*p(0, 0) - 2*p(1, 0) + p(-1, 1) - 2*p(0, 1) + p(1, 1)
					s += math.Abs(v)
				}
			}
			out = append(out, math.Sqrt(math.Pi/2)*s/(6*bs*bs))
		}
	}
	return out
}

// Blockiness compares the steps across 8-pixel block edges with the steps
// inside blocks, on frames at their own size: about 1 for clean video, 1.3 or
// more when compression shows as blocks.
func Blockiness(ctx context.Context, ts *tools.Set, src string, duration float64) (float64, error) {
	var edge, inner float64
	var ne, ni int
	for _, t := range sampleTimes(duration, 6) {
		px, w, h, err := frameAt(ctx, ts, src, t, 0, 0, false)
		if err != nil {
			return 0, err
		}
		for y := 0; y < h; y++ {
			for x := 0; x+1 < w; x++ {
				d := math.Abs(float64(px[y*w+x]) - float64(px[y*w+x+1]))
				if x%8 == 7 {
					edge += d
					ne++
				} else {
					inner += d
					ni++
				}
			}
		}
		for y := 0; y+1 < h; y++ {
			for x := 0; x < w; x++ {
				d := math.Abs(float64(px[y*w+x]) - float64(px[(y+1)*w+x]))
				if y%8 == 7 {
					edge += d
					ne++
				} else {
					inner += d
					ni++
				}
			}
		}
	}
	if ne == 0 || ni == 0 || inner == 0 {
		return 1, nil
	}
	return (edge / float64(ne)) / (inner / float64(ni)), nil
}
