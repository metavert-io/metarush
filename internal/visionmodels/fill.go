package visionmodels

import (
	"math"
	"sort"
)

// A fill is a picture laid over a blurred, enlarged copy of itself: a
// landscape shot made vertical (9:16), square or 4:5, or a portrait shot made
// landscape, with the copy filling the bands the picture leaves (D445). The
// copy shows the same faces, larger and blurred, which Apple Vision doesn't
// take for faces and YuNet often does (it sees the eyes and mouth through the
// blur, and the picture's sharp edge through the face). Faces in the copy
// aren't faces of the frame.
//
// A fill is found by what it shows: the picture in the middle of the frame
// (centred, as editors place it), bands on both sides of it (above and below,
// or left and right) with little detail next to the picture's (the second
// derivative of the brightness, which blur flattens), and those bands showing
// the picture itself, enlarged around the frame's middle and blurred: their
// brightness follows the picture's, enlarged and blurred the same way, and
// varies about as much. The picture's size is searched for; the one whose
// copy fits best is kept. Letterbox bars, plain walls and studio backdrops
// don't follow the picture, so they aren't fills.

const (
	// fillSide is the long side the frame is looked at in (pixels).
	fillSide = 320
	// A row or column's detail is its sharpest stretch: the 95th percentile
	// of the absolute Laplacian (0–255 brightness), over a few rows. The
	// picture's rows have at least fillDetail (their median), and the bands'
	// (their 90th percentile) at most fillFlat of the picture's.
	fillDetail = 12.0
	fillFlat   = 0.4
	// fillBandsQ: the share of the bands' rows that must be flat (the rest
	// may hold something laid over the copy: a face cam, a picture in
	// picture, a title).
	fillBandsQ = 0.5
	// fillOverlay: a place in the bands with more detail than this (the
	// absolute Laplacian, averaged nearby) is laid over the copy, and left
	// out of comparing the copy with the picture.
	fillOverlay = 6.0
	// The picture covers between fillMin and fillMax of the frame's side.
	fillMin = 0.25
	fillMax = 0.88
	// fillCopy: how alike the bands must be to the picture, enlarged and
	// blurred (Pearson's correlation of brightness), and fillSpread how
	// much their brightness varies against the picture's there (at least
	// this share, at most its inverse).
	fillCopy   = 0.75
	fillSpread = 0.4
	// fillSharpFace: a face outside the picture is laid over the copy (a
	// face cam, a picture in picture) when at least this share of it there
	// has detail.
	fillSharpFace = 0.5
	// fillEdge: a fill's picture ends in straight lines, where the
	// brightness changes this many times more than across the rows near
	// them (the geometric mean of both ends).
	fillEdge = 1.9
)

// gray is a small luminance image.
type gray struct {
	w, h int
	v    []float64
}

func (g gray) at(x, y int) float64 {
	x, y = min(max(x, 0), g.w-1), min(max(y, 0), g.h-1)
	return g.v[y*g.w+x]
}

// sample is the brightness at (x, y) in pixels, bilinear.
func (g gray) sample(x, y float64) float64 {
	x0, y0 := math.Floor(x), math.Floor(y)
	fx, fy := x-x0, y-y0
	ix, iy := int(x0), int(y0)
	return g.at(ix, iy)*(1-fx)*(1-fy) + g.at(ix+1, iy)*fx*(1-fy) + g.at(ix, iy+1)*(1-fx)*fy + g.at(ix+1, iy+1)*fx*fy
}

// smallGray is the picture's luminance with its long side at most side.
func smallGray(p *picture, side int) gray {
	k := float64(side) / float64(max(p.w, p.h))
	if k > 1 {
		k = 1
	}
	w, h := max(1, int(math.Round(float64(p.w)*k))), max(1, int(math.Round(float64(p.h)*k)))
	q := p.resized(w, h)
	g := gray{w: w, h: h, v: make([]float64, w*h)}
	for i := range g.v {
		g.v[i] = 0.299*float64(q.px[3*i]) + 0.587*float64(q.px[3*i+1]) + 0.114*float64(q.px[3*i+2])
	}
	return g
}

// laplacian is the absolute Laplacian of g (its borders zero).
func laplacian(g gray) gray {
	out := gray{w: g.w, h: g.h, v: make([]float64, len(g.v))}
	for y := 1; y < g.h-1; y++ {
		for x := 1; x < g.w-1; x++ {
			i := y*g.w + x
			out.v[i] = math.Abs(4*g.v[i] - g.v[i-1] - g.v[i+1] - g.v[i-g.w] - g.v[i+g.w])
		}
	}
	return out
}

// percentile is the q-quantile of xs (sorted in place).
func percentile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	return xs[min(len(xs)-1, int(q*float64(len(xs))))]
}

// detailProfile is, for each row (across) or column, how much detail its
// sharpest stretch has (a running median over five, so one stray row
// doesn't count).
func detailProfile(l gray, across bool) []float64 {
	n, m := l.h, l.w
	if !across {
		n, m = l.w, l.h
	}
	out := make([]float64, n)
	line := make([]float64, 0, m)
	for i := 1; i < n-1; i++ {
		line = line[:0]
		for j := 1; j < m-1; j++ {
			if across {
				line = append(line, l.v[i*l.w+j])
			} else {
				line = append(line, l.v[j*l.w+i])
			}
		}
		out[i] = percentile(line, 0.95)
	}
	if n > 2 {
		out[0], out[n-1] = out[1], out[n-2]
	}
	return runningMedian(out, 5)
}

// runningMedian is xs's median over windows of w around each place.
func runningMedian(xs []float64, w int) []float64 {
	out := make([]float64, len(xs))
	win := make([]float64, 0, w)
	for i := range xs {
		win = win[:0]
		for j := max(0, i-w/2); j <= min(len(xs)-1, i+w/2); j++ {
			win = append(win, xs[j])
		}
		out[i] = percentile(win, 0.5)
	}
	return out
}

// flatBands reports a picture from..to (rows or columns) whose bands on both
// sides have little detail next to it.
func flatBands(prof []float64, from, to int) bool {
	inside := append([]float64{}, prof[from:to]...)
	before, after := append([]float64{}, prof[:from]...), append([]float64{}, prof[to:]...)
	outside := append(append([]float64{}, before...), after...)
	if len(before) == 0 || len(after) == 0 {
		return false
	}
	pic := percentile(inside, 0.5)
	// Most of the bands' rows are flat, and all of one band's (whatever is
	// laid over the copy sits in one).
	flat := fillFlat * pic
	return pic >= fillDetail && percentile(outside, fillBandsQ) <= flat &&
		math.Min(percentile(before, 0.9), percentile(after, 0.9)) <= flat
}

// boxBlur averages g over a square of radius r.
func boxBlur(g gray, r int) gray {
	if r < 1 {
		return g
	}
	tmp := gray{w: g.w, h: g.h, v: make([]float64, len(g.v))}
	out := gray{w: g.w, h: g.h, v: make([]float64, len(g.v))}
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			s := 0.0
			for k := -r; k <= r; k++ {
				s += g.at(x+k, y)
			}
			tmp.v[y*g.w+x] = s / float64(2*r+1)
		}
	}
	for y := 0; y < g.h; y++ {
		for x := 0; x < g.w; x++ {
			s := 0.0
			for k := -r; k <= r; k++ {
				s += tmp.at(x, y+k)
			}
			out.v[y*g.w+x] = s / float64(2*r+1)
		}
	}
	return out
}

// copyLikeness is how much the bands outside the picture (x0..x1, y0..y1 in
// g) look like the picture enlarged to cover the frame around its middle and
// blurred (blurs: the picture blurred a little, more and a lot, as the
// copy's blur is unknown; the closest is kept). It is zero where the bands
// don't vary like the picture does there.
func copyLikeness(g, overlay gray, blurs []gray, x0, y0, x1, y1 int) float64 {
	pw, ph := float64(x1-x0), float64(y1-y0)
	if pw < 4 || ph < 4 {
		return 0
	}
	// The copy covers the frame: enlarged by s around the frame's middle.
	s := math.Max(float64(g.w)/pw, float64(g.h)/ph)
	cx, cy := float64(x0)+pw/2, float64(y0)+ph/2
	step := max(1, min(g.w, g.h)/40)
	var pts [][2]float64
	var ys []float64
	for y := 0; y < g.h; y += step {
		for x := 0; x < g.w; x += step {
			if x >= x0 && x < x1 && y >= y0 && y < y1 {
				continue
			}
			// The picture point the copy shows here.
			px, py := cx+(float64(x)-float64(g.w)/2)/s, cy+(float64(y)-float64(g.h)/2)/s
			if px < float64(x0) || px > float64(x1-1) || py < float64(y0) || py > float64(y1-1) || overlay.at(x, y) > fillOverlay {
				continue
			}
			pts = append(pts, [2]float64{px, py})
			ys = append(ys, blurs[0].at(x, y))
		}
	}
	if len(pts) < 20 {
		return 0
	}
	best := 0.0
	xs := make([]float64, len(pts))
	for _, b := range blurs {
		for i, p := range pts {
			xs[i] = b.sample(p[0], p[1])
		}
		best = math.Max(best, likeCopy(xs, ys))
	}
	return best
}

// likeCopy is how much ys (the bands) look like xs (the picture, enlarged
// and blurred): their correlation, when the bands vary about as much as the
// picture does there (a darkened or faded copy still varies; a backdrop that
// only darkens toward the edges doesn't), else zero.
func likeCopy(xs, ys []float64) float64 {
	sx, sy := stddev(xs), stddev(ys)
	if sx < 2 || sy < 2 || sy/sx < fillSpread || sy/sx > 1/fillSpread {
		return 0
	}
	return correlation(xs, ys)
}

func stddev(xs []float64) float64 {
	var m float64
	for _, x := range xs {
		m += x
	}
	m /= float64(len(xs))
	var v float64
	for _, x := range xs {
		v += (x - m) * (x - m)
	}
	return math.Sqrt(v / float64(len(xs)))
}

// correlation is Pearson's correlation of xs and ys.
func correlation(xs, ys []float64) float64 {
	n := float64(len(xs))
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx, my = mx/n, my/n
	var sxy, sxx, syy float64
	for i := range xs {
		dx, dy := xs[i]-mx, ys[i]-my
		sxy += dx * dy
		sxx += dx * dx
		syy += dy * dy
	}
	if sxx == 0 || syy == 0 {
		return 0
	}
	return sxy / math.Sqrt(sxx*syy)
}

// fillFrame is a fill found in a frame: its sharp picture (frame pixels
// [x, y, w, h]) and what tells the copy's faces from faces laid over it.
type fillFrame struct {
	pic [4]float64
	// detail is the frame's detail at each place (the absolute Laplacian,
	// averaged nearby; small) and k frame pixels per small pixel.
	detail gray
	k      float64
}

// sharpPicture finds the sharp picture of a fill, as a box in the frame's
// pixels [x, y, w, h]; ok is false for a frame that isn't one.
func sharpPicture(p *picture) (box [4]float64, ok bool) {
	f, ok := findFill(p)
	return f.pic, ok
}

// findFill finds a fill in a frame (ok false: it isn't one).
func findFill(p *picture) (f fillFrame, ok bool) {
	if p.w < 32 || p.h < 32 {
		return f, false
	}
	g := smallGray(p, fillSide)
	l := laplacian(g)
	var blurs []gray
	var overlay gray
	best, bestFrom, bestTo, bestAcross := fillCopy, 0, 0, false
	// A picture fills a frame's width when the frame is taller than it
	// (bands above and below), its height when the frame is wider (bands
	// at the sides); a square frame may have either.
	ways := []bool{true, false}
	switch {
	case p.h > p.w*11/10:
		ways = []bool{true}
	case p.w > p.h*11/10:
		ways = []bool{false}
	}
	for _, across := range ways {
		prof := detailProfile(l, across)
		n := len(prof)
		for size := int(math.Ceil(fillMin * float64(n))); size <= int(fillMax*float64(n)); size++ {
			from := (n - size) / 2
			to := from + size
			if !flatBands(prof, from, to) || math.Sqrt(edgeStrength(g, from, across)*edgeStrength(g, to, across)) < fillEdge {
				continue
			}
			if blurs == nil {
				r := float64(min(g.w, g.h))
				blurs = []gray{boxBlur(g, 1), boxBlur(g, max(1, int(r/60))), boxBlur(g, max(1, int(r/24)))}
				overlay = boxBlur(l, 2)
			}
			var c float64
			if across {
				c = copyLikeness(g, overlay, blurs, 0, from, g.w, to)
			} else {
				c = copyLikeness(g, overlay, blurs, from, 0, to, g.h)
			}
			if c > best {
				best, bestFrom, bestTo, bestAcross = c, from, to, across
			}
		}
	}
	if bestTo == 0 {
		return f, false
	}
	k := float64(p.w) / float64(g.w)
	f = fillFrame{detail: overlay, k: k}
	if bestAcross {
		f.pic = [4]float64{0, float64(bestFrom) * k, float64(p.w), float64(bestTo-bestFrom) * k}
	} else {
		f.pic = [4]float64{float64(bestFrom) * k, 0, float64(bestTo-bestFrom) * k, float64(p.h)}
	}
	return f, true
}

// fillMargin: the copy's pixels this close to the picture (small pixels)
// are left out of measuring it (the picture's sharp edge).
const fillMargin = 2

// sharpShare is the share of a box's pixels outside the picture (away from
// its edges) with detail: laid over the copy, not of it (-1: too few of them
// to tell, and the face counts as the copy's, as every face outside the
// picture did before).
func (f fillFrame) sharpShare(box [4]float64) float64 {
	x0, y0 := int(box[0]/f.k), int(box[1]/f.k)
	x1, y1 := int(math.Ceil((box[0]+box[2])/f.k)), int(math.Ceil((box[1]+box[3])/f.k))
	px0, py0 := int(f.pic[0]/f.k)-fillMargin, int(f.pic[1]/f.k)-fillMargin
	px1, py1 := int(math.Ceil((f.pic[0]+f.pic[2])/f.k))+fillMargin, int(math.Ceil((f.pic[1]+f.pic[3])/f.k))+fillMargin
	n, sharp := 0, 0
	for y := max(0, y0); y < min(f.detail.h, y1); y++ {
		for x := max(0, x0); x < min(f.detail.w, x1); x++ {
			if x >= px0 && x < px1 && y >= py0 && y < py1 {
				continue
			}
			n++
			if f.detail.v[y*f.detail.w+x] > fillOverlay {
				sharp++
			}
		}
	}
	if n < 9 {
		return -1
	}
	return float64(sharp) / float64(n)
}

// inCopy says a face found in a fill is the copy's: mostly outside the
// picture and, there, mostly without detail (D465: a face cam or a picture
// in picture laid over the copy is sharp, and stays).
func (f fillFrame) inCopy(face [4]float64) bool {
	if !outsidePicture(face, f.pic) {
		return false
	}
	return f.sharpShare(face) < fillSharpFace
}

// edgeStrength is how much more the brightness changes across row (or
// column) at than across the rows near it: a fill's picture ends in a
// straight line.
func edgeStrength(g gray, at int, across bool) float64 {
	n, m := g.h, g.w
	if !across {
		n, m = g.w, g.h
	}
	step := func(i int) float64 {
		if i < 1 || i >= n {
			return 0
		}
		s := 0.0
		for j := 0; j < m; j++ {
			if across {
				s += math.Abs(g.at(j, i) - g.at(j, i-1))
			} else {
				s += math.Abs(g.at(i, j) - g.at(i-1, j))
			}
		}
		return s / float64(m)
	}
	edge := math.Max(step(at), math.Max(step(at-1), step(at+1)))
	var near []float64
	for d := 3; d <= 10; d++ {
		near = append(near, step(at-d), step(at+d))
	}
	return edge / math.Max(0.5, percentile(near, 0.5))
}

// outsidePicture says a face found in a fill is in its blurred copy: its
// middle is outside the sharp picture, or most of it is.
func outsidePicture(face, pic [4]float64) bool {
	cx, cy := face[0]+face[2]/2, face[1]+face[3]/2
	if cx < pic[0] || cx > pic[0]+pic[2] || cy < pic[1] || cy > pic[1]+pic[3] {
		return true
	}
	ix := math.Max(0, math.Min(face[0]+face[2], pic[0]+pic[2])-math.Max(face[0], pic[0]))
	iy := math.Max(0, math.Min(face[1]+face[3], pic[1]+pic[3])-math.Max(face[1], pic[1]))
	return ix*iy < 0.5*face[2]*face[3]
}
