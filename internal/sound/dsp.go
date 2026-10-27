package sound

import (
	"math"
	"math/cmplx"
	"sort"
)

// fft is an in-place iterative radix-2 FFT (len(x) must be a power of two).
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
	for size := 2; size <= n; size <<= 1 {
		ang := 2 * math.Pi / float64(size)
		if !inverse {
			ang = -ang
		}
		w := cmplx.Rect(1, ang)
		half := size / 2
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			for k := 0; k < half; k++ {
				a, b := x[start+k], x[start+k+half]*wk
				x[start+k], x[start+k+half] = a+b, a-b
				wk *= w
			}
		}
	}
	if inverse {
		inv := complex(1/float64(n), 0)
		for i := range x {
			x[i] *= inv
		}
	}
}

func nextPow2(n int) int {
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

// bestLag finds how many samples b is late against a (b[i+lag] ≈ a[i]),
// within ±maxLag, by cross-correlation (FFT). It also returns the normalized
// correlation at that lag (1 = identical shapes).
func bestLag(a, b []float32, maxLag int) (int, float64) {
	n := min(len(a), len(b))
	if n < 16 {
		return 0, 0
	}
	size := nextPow2(2 * n)
	fa, fb := make([]complex128, size), make([]complex128, size)
	var ea, eb float64
	for i := 0; i < n; i++ {
		fa[i] = complex(float64(a[i]), 0)
		fb[i] = complex(float64(b[i]), 0)
		ea += float64(a[i]) * float64(a[i])
		eb += float64(b[i]) * float64(b[i])
	}
	fft(fa, false)
	fft(fb, false)
	for i := range fa {
		fa[i] = fb[i] * cmplx.Conj(fa[i])
	}
	fft(fa, true) // fa[k] = Σ a[i]·b[i+k]
	best, bestV := 0, math.Inf(-1)
	for lag := -maxLag; lag <= maxLag; lag++ {
		k := lag
		if k < 0 {
			k += size
		}
		if v := real(fa[k]); v > bestV {
			best, bestV = lag, v
		}
	}
	if ea == 0 || eb == 0 {
		return 0, 0
	}
	return best, bestV / math.Sqrt(ea*eb)
}

// shift moves a signal later by lag samples (earlier when negative), keeping its length.
func shift(x []float32, lag int) []float32 {
	out := make([]float32, len(x))
	for i := range out {
		j := i + lag
		if j >= 0 && j < len(x) {
			out[i] = x[j]
		}
	}
	return out
}

// frameEnergies is the mean square of each frame (frame and hop in samples).
func frameEnergies(x []float32, frame, hop int) []float64 {
	if frame <= 0 || hop <= 0 || len(x) < frame {
		return nil
	}
	n := (len(x)-frame)/hop + 1
	out := make([]float64, n)
	for k := 0; k < n; k++ {
		var s float64
		for _, v := range x[k*hop : k*hop+frame] {
			s += float64(v) * float64(v)
		}
		out[k] = s / float64(frame)
	}
	return out
}

func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	i := int(math.Round(p * float64(len(s)-1)))
	return s[max(0, min(len(s)-1, i))]
}

func db(power float64) float64 {
	if power <= 1e-20 {
		return -200
	}
	return 10 * math.Log10(power)
}

// activeLevel is the RMS over the frames where a reference signal is loud (its
// loudest 40%): a level for speech, not for the pauses between words. ref
// decides the frames, x is measured on them (the same frames for a dry and a
// processed signal, so the two can be compared).
func activeLevel(ref, x []float32, rate int) float64 {
	frame := max(1, rate/50) // 20 ms
	er := frameEnergies(ref, frame, frame)
	ex := frameEnergies(x, frame, frame)
	if len(er) == 0 || len(ex) == 0 {
		return 0
	}
	thr := percentile(er, 0.6)
	var s float64
	n := 0
	for k := range er {
		if k < len(ex) && er[k] >= thr && er[k] > 1e-12 {
			s += ex[k]
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return math.Sqrt(s / float64(n))
}

func peakAbs(x []float32) float64 {
	p := 0.0
	for _, v := range x {
		if a := math.Abs(float64(v)); a > p {
			p = a
		}
	}
	return p
}
