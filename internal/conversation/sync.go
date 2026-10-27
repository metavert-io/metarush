package conversation

import (
	"math"
	"math/cmplx"
	"sort"
)

// Sync (D100): each member's onsets are cross-correlated against the
// reference's. Against a recording with everyone in it (a call mix) a
// person's own voice gives one sharp peak. Between two people's own tracks,
// only what each microphone picked up from the other is shared, and an online
// call delivers it a network delay late in each direction, so the voice of
// the reference in the member's microphone and the member's voice in the
// reference's give two peaks: the recordings' true offset is halfway between.
// Drift is the slope of the offset measured in 30-second windows.

// fft is an in-place iterative radix-2 FFT (len(x) a power of two).
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
		w := complex(math.Cos(ang), math.Sin(ang))
		for start := 0; start < n; start += size {
			wk := complex(1, 0)
			half := size / 2
			for k := 0; k < half; k++ {
				u := x[start+k]
				v := x[start+k+half] * wk
				x[start+k] = u + v
				x[start+k+half] = u - v
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

// corr is a normalized cross-correlation: at lag L (in 10 ms steps), how much
// a at step k matches b at step k+L.
type corr struct {
	maxLag int
	v      []float64 // index L+maxLag
}

func (c corr) at(l int) float64 {
	i := l + c.maxLag
	if i < 0 || i >= len(c.v) {
		return 0
	}
	return c.v[i]
}

// xcorr cross-correlates a and b for lags in [-maxLag, maxLag].
func xcorr(a, b []float32, maxLag int) corr {
	n := 1
	for n < len(a)+len(b)+1 {
		n <<= 1
	}
	fa := make([]complex128, n)
	fb := make([]complex128, n)
	var ea, eb float64
	for i, v := range a {
		fa[i] = complex(float64(v), 0)
		ea += float64(v) * float64(v)
	}
	for i, v := range b {
		fb[i] = complex(float64(v), 0)
		eb += float64(v) * float64(v)
	}
	fft(fa, false)
	fft(fb, false)
	for i := range fa {
		fa[i] = cmplx.Conj(fa[i]) * fb[i]
	}
	fft(fa, true)
	norm := math.Sqrt(ea * eb)
	if norm == 0 {
		norm = 1
	}
	c := corr{maxLag: maxLag, v: make([]float64, 2*maxLag+1)}
	for l := -maxLag; l <= maxLag; l++ {
		i := l
		if i < 0 {
			i += n
		}
		if i >= 0 && i < n {
			c.v[l+maxLag] = real(fa[i]) / norm
		}
	}
	return c
}

// peak is the best lag of a correlation: the lag (sub-step, parabolic), its
// height and how far it stands out of the rest (a z-score, the ±0.2 s around
// it left out).
type peak struct {
	lag    float64 // steps
	height float64
	z      float64
}

func (c corr) best(lo, hi int) peak {
	lo, hi = max(lo, -c.maxLag), min(hi, c.maxLag)
	bi, bv := lo, math.Inf(-1)
	for l := lo; l <= hi; l++ {
		if v := c.at(l); v > bv {
			bi, bv = l, v
		}
	}
	p := peak{lag: float64(bi), height: bv}
	if bi > lo && bi < hi {
		y0, y1, y2 := c.at(bi-1), bv, c.at(bi+1)
		if d := y0 - 2*y1 + y2; d < 0 {
			p.lag += 0.5 * (y0 - y2) / d
		}
	}
	var s, ss float64
	n := 0
	for l := lo; l <= hi; l++ {
		if l > bi-20 && l < bi+20 {
			continue
		}
		v := c.at(l)
		s += v
		ss += v * v
		n++
	}
	if n > 10 {
		mean := s / float64(n)
		sd := math.Sqrt(math.Max(1e-12, ss/float64(n)-mean*mean))
		p.z = (bv - mean) / sd
	}
	return p
}

// pairResult is how a member lines up with the reference.
type pairResult struct {
	method string
	// lag is where the reference's moment t is in the member's recording,
	// t + lag (seconds), at the start; slope its change per second (drift).
	lag, slope float64
	delay      float64
	z          float64
	windows    int
	agree      int
	residual   float64 // seconds (RMS)
	confidence float64
}

// syncOptions bound the search.
type syncOptions struct {
	search float64 // seconds either way
	mix    bool    // the reference is a call mix (everyone is in it)
}

// zGood is a peak that stands out clearly (z-score); zSome one that may be real.
const (
	zGood = 9.0
	zSome = 5.0
)

// align lines a member's sound up with the reference's.
func align(ref, mem *sound, o syncOptions) pairResult {
	fr, fm := ref.features(), mem.features()
	maxLag := int(math.Round(o.search * envRate))
	if l := len(ref.db) + len(mem.db); maxLag > l {
		maxLag = l
	}
	var res pairResult
	if o.mix {
		// The member's own voice against everything in the mix.
		c := xcorr(fr.all, fm.own, maxLag)
		p := c.best(-maxLag, maxLag)
		res = pairResult{method: "mix", lag: p.lag / envRate, z: p.z}
		res.windows, res.agree, res.residual, res.lag, res.slope = drift(fr.all, fm.own, res.lag, nil)
	} else {
		c1 := xcorr(fr.own, fm.quiet, maxLag) // the reference's voice in the member's microphone
		c2 := xcorr(fr.quiet, fm.own, maxLag) // the member's voice in the reference's
		p1, p2 := c1.best(-maxLag, maxLag), c2.best(-maxLag, maxLag)
		switch {
		case p1.z >= zSome && p2.z >= zSome && math.Abs(p1.lag-p2.lag) < 1.5*envRate:
			// Two-way: halfway between; the call's delay is half the distance.
			res = pairResult{method: "two-way", lag: (p1.lag + p2.lag) / 2 / envRate, delay: (p1.lag - p2.lag) / 2 / envRate, z: math.Min(p1.z, p2.z)}
			d := res.delay
			res.windows, res.agree, res.residual, res.lag, res.slope = drift2(fr, fm, res.lag, d)
		default:
			// One way only (or neither): the stronger peak, everything against everything as a check.
			best := p1
			a, b := fr.own, fm.quiet
			if p2.z > p1.z {
				best, a, b = p2, fr.quiet, fm.own
			}
			c0 := xcorr(fr.all, fm.all, maxLag)
			p0 := c0.best(-maxLag, maxLag)
			if p0.z > best.z+2 {
				best, a, b = p0, fr.all, fm.all
			}
			res = pairResult{method: "one-way", lag: best.lag / envRate, z: best.z}
			res.windows, res.agree, res.residual, res.lag, res.slope = drift(a, b, res.lag, nil)
		}
	}
	res.confidence = confidence(res)
	return res
}

// confidence combines how far the peak stands out with how well the windows
// agree (0–1).
func confidence(r pairResult) float64 {
	prom := math.Max(0, math.Min(1, (r.z-zSome)/(2*zGood-zSome)))
	if r.z >= zGood {
		prom = math.Max(prom, 0.75)
	}
	agree := 0.5
	if r.windows >= 3 {
		agree = float64(r.agree) / float64(r.windows)
	}
	c := 0.6*prom + 0.4*agree
	if r.method == "one-way" {
		c *= 0.75 // it may be off by the call's delay
	}
	return math.Round(math.Max(0, math.Min(1, c))*100) / 100
}

// Drift windows.
const (
	winLen    = 30.0 // seconds
	winStep   = 15.0
	winSearch = 0.25 // seconds either way of the expected offset
	frameTol  = 1.0 / 30
)

// window is one measured offset: at reference time t (its middle), the
// member's offset lag (seconds) and the peak's height.
type window struct {
	t, lag, height float64
}

// windowLag measures the offset of one window around want (steps).
func windowLag(a, b []float32, k0, k1 int, want float64) (float64, float64, bool) {
	R := int(winSearch * envRate)
	center := int(math.Round(want))
	var ea float64
	for k := k0; k < k1; k++ {
		ea += float64(a[k]) * float64(a[k])
	}
	if ea == 0 {
		return 0, 0, false
	}
	vals := make([]float64, 2*R+1)
	bestI, bestV := 0, math.Inf(-1)
	for i := range vals {
		l := center - R + i
		var s, eb float64
		for k := k0; k < k1; k++ {
			j := k + l
			if j < 0 || j >= len(b) {
				continue
			}
			s += float64(a[k]) * float64(b[j])
			eb += float64(b[j]) * float64(b[j])
		}
		if eb > 0 {
			s /= math.Sqrt(ea * eb)
		}
		vals[i] = s
		if s > bestV {
			bestI, bestV = i, s
		}
	}
	if bestV <= 0 {
		return 0, 0, false
	}
	// It must stand out of the window's other lags.
	sorted := append([]float64(nil), vals...)
	sort.Float64s(sorted)
	med := sorted[len(sorted)/2]
	if bestV < med*2.5+0.02 {
		return 0, 0, false
	}
	lag := float64(center - R + bestI)
	if bestI > 0 && bestI < len(vals)-1 {
		y0, y1, y2 := vals[bestI-1], vals[bestI], vals[bestI+1]
		if d := y0 - 2*y1 + y2; d < 0 {
			lag += 0.5 * (y0 - y2) / d
		}
	}
	return lag, bestV, true
}

// drift measures the offset in windows along the reference and fits a line:
// windows measured, how many agree within a frame, their spread, and the
// fitted offset at the start and its slope.
func drift(a, b []float32, lag float64, delay *float64) (int, int, float64, float64, float64) {
	ws := measureWindows(a, b, lag, func(k0, k1 int, want float64) (float64, float64, bool) {
		return windowLag(a, b, k0, k1, want)
	})
	return fitWindows(ws, lag)
}

// drift2 is drift for two-way pick-up: each window takes the midpoint of
// both directions (or one direction corrected by the call's delay).
func drift2(fr, fm features, lag, delay float64) (int, int, float64, float64, float64) {
	d := delay * envRate
	ws := measureWindows(fr.own, fm.quiet, lag, func(k0, k1 int, want float64) (float64, float64, bool) {
		l1, h1, ok1 := windowLag(fr.own, fm.quiet, k0, k1, want+d)
		l2, h2, ok2 := windowLag(fr.quiet, fm.own, k0, k1, want-d)
		switch {
		case ok1 && ok2:
			return (l1 + l2) / 2, math.Min(h1, h2), true
		case ok1:
			return l1 - d, h1 / 2, true
		case ok2:
			return l2 + d, h2 / 2, true
		}
		return 0, 0, false
	})
	return fitWindows(ws, lag)
}

func measureWindows(a, b []float32, lag float64, measure func(k0, k1 int, want float64) (float64, float64, bool)) []window {
	n := len(a)
	W, S := int(winLen*envRate), int(winStep*envRate)
	if n < W {
		W = n
	}
	var ws []window
	slope := 0.0
	for k0 := 0; k0+W <= n; k0 += S {
		mid := float64(k0+W/2) / envRate
		want := (lag + slope*mid) * envRate
		l, h, ok := measure(k0, k0+W, want)
		if !ok {
			continue
		}
		ws = append(ws, window{t: mid, lag: l / envRate, height: h})
		// Follow the drift as it shows, so long recordings stay within the search.
		if len(ws) >= 3 {
			_, sl, _ := lineFit(ws)
			if math.Abs(sl) < 0.002 {
				slope = sl
			}
		}
	}
	return ws
}

// fitWindows fits offset = lag + slope·t to the windows, dropping outliers.
func fitWindows(ws []window, lag float64) (int, int, float64, float64, float64) {
	if len(ws) < 3 {
		agree := 0
		for _, w := range ws {
			if math.Abs(w.lag-lag) <= frameTol {
				agree++
			}
		}
		return len(ws), agree, 0, lag, 0
	}
	keep := ws
	var a, b float64
	for it := 0; it < 4; it++ {
		a, b, _ = lineFit(keep)
		res := make([]float64, len(keep))
		for i, w := range keep {
			res[i] = math.Abs(w.lag - (a + b*w.t))
		}
		s := append([]float64(nil), res...)
		sort.Float64s(s)
		mad := s[len(s)/2]
		lim := math.Max(3*1.4826*mad, 0.02)
		var next []window
		for i, w := range keep {
			if res[i] <= lim {
				next = append(next, w)
			}
		}
		if len(next) < 3 || len(next) == len(keep) {
			break
		}
		keep = next
	}
	span := keep[len(keep)-1].t - keep[0].t
	if span < 60 || math.Abs(b) > 0.002 {
		// Too short to see a drift (or not a believable one): a constant offset.
		b = 0
		s := make([]float64, len(keep))
		for i, w := range keep {
			s[i] = w.lag
		}
		sort.Float64s(s)
		a = s[len(s)/2]
	}
	agree := 0
	var ss float64
	for _, w := range ws {
		r := w.lag - (a + b*w.t)
		if math.Abs(r) <= frameTol {
			agree++
		}
		ss += r * r
	}
	return len(ws), agree, math.Sqrt(ss / float64(len(ws))), a, b
}

// lineFit is the weighted least-squares line through the windows (weights:
// their peak heights).
func lineFit(ws []window) (a, b, r float64) {
	var sw, sx, sy, sxx, sxy float64
	for _, w := range ws {
		h := math.Max(w.height, 1e-3)
		sw += h
		sx += h * w.t
		sy += h * w.lag
		sxx += h * w.t * w.t
		sxy += h * w.t * w.lag
	}
	d := sw*sxx - sx*sx
	if d == 0 {
		return sy / sw, 0, 0
	}
	b = (sw*sxy - sx*sy) / d
	a = (sy - b*sx) / sw
	return a, b, 0
}

// Placement: with the reference at start sRef (rate rRef), a member whose
// moment t+lag(t) lines up with the reference's t, lag(t) = lag + slope·t in
// the reference's recording time, starts at and drifts by:
//
//	member time τ_m = (c − s_m)·r_m, reference time τ_r = (c − s_r)·r_r,
//	τ_m = τ_r + lag + slope·τ_r  ⇒  r_m = r_r·(1+slope), s_m = s_r − lag/r_m·… (below)
func place(sRef, rRef, lag, slope float64) (start, rate float64) {
	rate = rRef * (1 + slope)
	// τ_m = (1+slope)·τ_r + lag with τ_r = (c − sRef)·rRef:
	// τ_m = rate·(c − sRef) + lag = rate·(c − (sRef − lag/rate)).
	start = sRef - lag/rate
	return start, rate
}

// ---- the clap assist ----

// clap finds the sharpest loud transient in the first seconds of a sound: a
// rise of at least 15 dB within 20 ms to near the loudest level, falling
// back quickly. ok is false when there is none.
func clapIn(s *sound, within float64) (float64, bool) {
	n := min(len(s.db), int(within*envRate))
	if n < 10 {
		return 0, false
	}
	_, speech := s.levels()
	top := percentile(s.db[:n], 99.5)
	best, bestScore := -1, 0.0
	for k := 2; k < n-10; k++ {
		rise := float64(s.db[k] - s.db[k-2])
		if rise < 15 || float64(s.db[k]) < math.Max(speech, top)-8 {
			continue
		}
		// A clap dies within 100 ms; speech holds.
		fall := float64(s.db[k] - s.db[min(n-1, k+10)])
		if fall < 10 {
			continue
		}
		if sc := rise + fall; sc > bestScore {
			best, bestScore = k, sc
		}
	}
	if best < 0 {
		return 0, false
	}
	return float64(best) / envRate, true
}

// refineClap finds the loudest sample around a clap found at t (seconds),
// in samples read from t−0.05 s.
func refineClap(samples []float32, from float64) float64 {
	bi, bv := 0, float32(0)
	for i, v := range samples {
		if v < 0 {
			v = -v
		}
		if v > bv {
			bi, bv = i, v
		}
	}
	return from + float64(bi)/sampleRate
}
