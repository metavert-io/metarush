package editspec

import "math"

// Layouts and framing for conversation shots (D102). Go computes every tile's
// rectangle and where each person's picture sits inside it, so the dashboard
// preview and the render draw exactly the same frame.

// PlanRect is a rectangle in output pixels.
type PlanRect struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	W float64 `json:"w"`
	H float64 `json:"h"`
}

// Framing targets for people in tiles: the face's height as a share of the
// tile's, the eyes' height from the top, and how far past covering the tile
// the picture may be enlarged (webcams have few pixels to spare).
const (
	tileFace      = 0.34
	tileEyes      = 0.40
	tileMaxZoom   = 1.8
	singleFace    = 0.30
	singleEyes    = 0.38
	singleMaxZoom = 1.25
	eyesInFace    = 0.40 // the eyes sit 40% down a face box
)

// LayoutCount is how many people a layout shows: min and max.
func LayoutCount(layout string) (int, int) {
	switch layout {
	case LayoutSideBySide, LayoutStacked:
		return 2, 3
	case LayoutPiP:
		return 2, 2
	case LayoutGrid:
		return 2, 9
	}
	return 0, 0
}

// gutter is the gap between tiles.
func gutter(w, h int) float64 { return math.Round(0.006 * float64(min(w, h))) }

// LayoutTiles is where each of n people goes in a layout on a w×h frame.
func LayoutTiles(layout string, n, w, h int) []PlanRect {
	return LayoutTilesStyled(layout, n, w, h, nil)
}

// LayoutTilesStyled is LayoutTiles in a layout style (D265): the inset's
// corner, size and shape in picture in picture; the gap (and, when wide, the
// same margin around) in side by side and stacked. A nil or empty style is
// exactly LayoutTiles.
func LayoutTilesStyled(layout string, n, w, h int, st *LayoutStyle) []PlanRect {
	if n <= 0 || w <= 0 || h <= 0 {
		return nil
	}
	W, H := float64(w), float64(h)
	g := gutter(w, h)
	var sb SideBySideStyle
	var pip PiPStyle
	if st != nil && st.SideBySide != nil {
		sb = *st.SideBySide
	}
	if st != nil && st.PiP != nil {
		pip = *st.PiP
	}
	edge := 0.0 // a margin around the pictures (a wide gap)
	if layout == LayoutSideBySide || layout == LayoutStacked {
		switch sb.Gap {
		case "none":
			g = 0
		case "wide":
			g = math.Round(0.03 * math.Min(W, H))
			edge = g
		}
	}
	var out []PlanRect
	switch layout {
	case LayoutSideBySide:
		cw := (W - 2*edge - g*float64(n-1)) / float64(n)
		for i := 0; i < n; i++ {
			out = append(out, PlanRect{X: round1(edge + float64(i)*(cw+g)), Y: edge, W: round1(cw), H: H - 2*edge})
		}
	case LayoutStacked:
		rh := (H - 2*edge - g*float64(n-1)) / float64(n)
		for i := 0; i < n; i++ {
			out = append(out, PlanRect{X: edge, Y: round1(edge + float64(i)*(rh+g)), W: W - 2*edge, H: round1(rh)})
		}
	case LayoutPiP:
		out = append(out, PlanRect{X: 0, Y: 0, W: W, H: H})
		scale := map[string]float64{"small": 0.75, "large": 1.3}[pip.Size]
		if scale == 0 {
			scale = 1
		}
		left := pip.Corner == "bottom-left" || pip.Corner == "top-left"
		top := pip.Corner == "top-right" || pip.Corner == "top-left"
		for i := 1; i < n; i++ {
			iw := 0.28 * W
			ih := iw * 3 / 4
			if h > w { // vertical: a taller inset, higher up, clear of captions
				iw = 0.38 * W
				ih = iw * 4 / 3
			}
			iw, ih = iw*scale, ih*scale
			if pip.Shape == "circle" { // a round camera bubble: as wide as it is tall
				d := math.Min(iw, ih)
				iw, ih = d, d
			}
			m := math.Round(0.04 * math.Min(W, H))
			y := H - m - ih
			if h > w {
				y = H*0.62 - ih
			}
			step := -(ih + m)
			if top {
				y = m
				if h > w {
					y = math.Round(0.1 * H) // below the apps' top bar
				}
				step = ih + m
			}
			x := W - m - iw
			if left {
				x = m
			}
			out = append(out, PlanRect{X: round1(x), Y: round1(y + float64(i-1)*step), W: round1(iw), H: round1(ih)})
		}
	case LayoutGrid:
		cols, rows := gridShape(n, W, H)
		cw := (W - g*float64(cols-1)) / float64(cols)
		rh := (H - g*float64(rows-1)) / float64(rows)
		for i := 0; i < n; i++ {
			r, c := i/cols, i%cols
			inRow := cols
			if r == rows-1 && n%cols != 0 {
				inRow = n % cols
			}
			x0 := (W - (float64(inRow)*cw + float64(inRow-1)*g)) / 2 // the last row is centered
			out = append(out, PlanRect{X: round1(x0 + float64(c)*(cw+g)), Y: round1(float64(r) * (rh + g)), W: round1(cw), H: round1(rh)})
		}
	}
	return out
}

// gridShape picks columns and rows for n people: the arrangement whose tiles
// hold the largest 16:9 picture.
func gridShape(n int, W, H float64) (int, int) {
	bestC, bestR, best := 1, n, -1.0
	for c := 1; c <= n; c++ {
		r := (n + c - 1) / c
		tw, th := W/float64(c), H/float64(r)
		s := math.Min(tw/16, th/9)
		if s > best+1e-9 {
			bestC, bestR, best = c, r, s
		}
	}
	return bestC, bestR
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// tileFrame is how a person's picture fills a tile.
type tileFrame struct {
	faceShare, eyes, maxZoom float64
}

// placePicture puts a source picture (sw×sh pixels) into a tile (tw×th): the
// rectangle the whole source is drawn at, relative to the tile, and the visible
// window when the picture doesn't fill it (fit). The crop's edges never show:
// the visible part always lies inside the cropped source. With a face box
// (source fractions) the face is scaled to the tile's face share and its eyes
// put on the tile's eye line (matching headroom across tiles); otherwise the
// focus point is centered.
func placePicture(tw, th, sw, sh float64, crop *Crop, fr *Framing, face *[4]float64, tf tileFrame) (PlanRect, *PlanRect) {
	if sw <= 0 || sh <= 0 {
		sw, sh = 1280, 720
	}
	var cl, ct, cr, cb float64
	if crop != nil {
		cl, ct, cr, cb = crop.Left, crop.Top, crop.Right, crop.Bottom
	}
	cx0, cy0 := cl*sw, ct*sh
	cw, ch := sw*math.Max(0.1, 1-cl-cr), sh*math.Max(0.1, 1-ct-cb)
	zoom := 1.0
	if fr != nil && fr.Zoom > 1 {
		zoom = fr.Zoom
	}
	if fr != nil && fr.Mode == "fit" {
		s := math.Min(tw/cw, th/ch) * zoom
		if s*cw > tw || s*ch > th { // zoomed past fitting: fill instead
			s = math.Max(tw/cw, th/ch) * zoom
		}
		vw, vh := math.Min(tw, cw*s), math.Min(th, ch*s)
		win := PlanRect{X: round1((tw - vw) / 2), Y: round1((th - vh) / 2), W: round1(vw), H: round1(vh)}
		// The crop region's center at the window's center.
		x := win.X + vw/2 - (cx0+cw/2)*s
		y := win.Y + vh/2 - (cy0+ch/2)*s
		return PlanRect{X: round1(x), Y: round1(y), W: round1(sw * s), H: round1(sh * s)}, &win
	}
	base := math.Max(tw/cw, th/ch)
	s := base
	var x, y float64
	if face != nil && face[3] > 0 {
		want := tf.faceShare * th / (face[3] * sh)
		s = math.Max(base, math.Min(want, base*tf.maxZoom)) * zoom
		fcx := (face[0] + face[2]/2) * sw
		eye := (face[1] + face[3]*eyesInFace) * sh
		x = tw/2 - fcx*s
		y = tf.eyes*th - eye*s
	} else {
		s = base * zoom
		fx, fy := 0.5, 0.5
		if fr != nil && fr.FocusX != nil {
			fx = *fr.FocusX
		}
		if fr != nil && fr.FocusY != nil {
			fy = *fr.FocusY
		}
		x = tw/2 - fx*sw*s
		y = th/2 - fy*sh*s
	}
	// Keep the tile inside the cropped picture.
	x = math.Min(-cx0*s, math.Max(tw-(cx0+cw)*s, x))
	y = math.Min(-cy0*s, math.Max(th-(cy0+ch)*s, y))
	return PlanRect{X: round1(x), Y: round1(y), W: round1(sw * s), H: round1(sh * s)}, nil
}

// tileIn is one person's picture to place in a tile of a layout.
type tileIn struct {
	tw, th, sw, sh float64
	crop           *Crop
	fr             *Framing
	face           *[4]float64
}

// placed is where a picture goes in its tile (see placePicture).
type placed struct {
	video  PlanRect
	window *PlanRect
}

// placeMatched places the pictures of a layout's tiles so the faces match:
// one face height and one eye line for every tile that shows a face (as close
// to the layout's targets as every tile allows: a webcam can't be shrunk below
// covering its tile, nor enlarged past tileMaxZoom), each face centered
// across its tile. Tiles without a face, or shown whole (fit), are placed on
// their own.
func placeMatched(ts []tileIn, tf tileFrame) []placed {
	out := make([]placed, len(ts))
	type geo struct {
		i                  int
		cx0, cy0, cw, ch   float64
		smin, smax         float64
		faceSrc, eyeSrc, x float64
	}
	var gs []geo
	for i, t := range ts {
		sw, sh := t.sw, t.sh
		if sw <= 0 || sh <= 0 {
			sw, sh = 1280, 720
			ts[i].sw, ts[i].sh = sw, sh
		}
		fit := t.fr != nil && t.fr.Mode == "fit"
		if t.face == nil || t.face[3] <= 0 || fit {
			v, w := placePicture(t.tw, t.th, sw, sh, t.crop, t.fr, t.face, tf)
			out[i] = placed{v, w}
			continue
		}
		var cl, ct, cr, cb float64
		if t.crop != nil {
			cl, ct, cr, cb = t.crop.Left, t.crop.Top, t.crop.Right, t.crop.Bottom
		}
		g := geo{i: i, cx0: cl * sw, cy0: ct * sh, cw: sw * math.Max(0.1, 1-cl-cr), ch: sh * math.Max(0.1, 1-ct-cb)}
		zoom := 1.0
		if t.fr != nil && t.fr.Zoom > 1 {
			zoom = t.fr.Zoom
		}
		base := math.Max(t.tw/g.cw, t.th/g.ch)
		g.smin, g.smax = base*zoom, base*tf.maxZoom*zoom
		g.faceSrc = t.face[3] * sh
		g.eyeSrc = (t.face[1] + t.face[3]*eyesInFace) * sh
		g.x = (t.face[0] + t.face[2]/2) * sw
		gs = append(gs, g)
	}
	if len(gs) == 0 {
		return out
	}
	// One face height: the target, within what every tile allows.
	th := ts[gs[0].i].th
	lo, hi := 0.0, math.Inf(1)
	for _, g := range gs {
		lo = math.Max(lo, g.smin*g.faceSrc)
		hi = math.Min(hi, g.smax*g.faceSrc)
	}
	F := tf.faceShare * th
	switch {
	case lo > hi:
		F = lo // someone can't be enlarged that far: the others come as close as they can
	default:
		F = math.Max(lo, math.Min(hi, F))
	}
	scales := make([]float64, len(gs))
	elo, ehi := math.Inf(-1), math.Inf(1)
	for k, g := range gs {
		s := math.Max(g.smin, math.Min(g.smax, F/g.faceSrc))
		scales[k] = s
		t := ts[g.i]
		// Where the eyes can go: the picture must keep covering the tile.
		yMin, yMax := t.th-(g.cy0+g.ch)*s, -g.cy0*s
		elo = math.Max(elo, yMin+g.eyeSrc*s)
		ehi = math.Min(ehi, yMax+g.eyeSrc*s)
	}
	E := tf.eyes * th
	if elo > ehi {
		E = (elo + ehi) / 2
	} else {
		E = math.Max(elo, math.Min(ehi, E))
	}
	for k, g := range gs {
		t, s := ts[g.i], scales[k]
		x := t.tw/2 - g.x*s
		y := E - g.eyeSrc*s
		x = math.Min(-g.cx0*s, math.Max(t.tw-(g.cx0+g.cw)*s, x))
		y = math.Min(-g.cy0*s, math.Max(t.th-(g.cy0+g.ch)*s, y))
		out[g.i] = placed{video: PlanRect{X: round1(x), Y: round1(y), W: round1(t.sw * s), H: round1(t.sh * s)}}
	}
	return out
}

// LookMatrix is the colour correction as one sRGB colour matrix (the 20 values
// of an SVG feColorMatrix: four rows of R, G, B, A, offset), nil when neutral.
// Exposure scales, warmth and tint shift the channel balance, contrast pivots
// around mid grey and saturation mixes toward each pixel's luma (BT.709).
func LookMatrix(l *Look) []float64 {
	if l == nil {
		return nil
	}
	one := func(v float64) float64 {
		if v == 0 {
			return 1 // absent: neutral
		}
		return v
	}
	e, k, s := one(l.Exposure), one(l.Contrast), one(l.Saturation)
	w, t := l.Warmth, l.Tint
	if e == 1 && k == 1 && s == 1 && w == 0 && t == 0 {
		return nil
	}
	d := [3]float64{e * (1 + w) * (1 - t/2), e * (1 + t), e * (1 - w) * (1 - t/2)}
	luma := [3]float64{0.2126, 0.7152, 0.0722}
	var m [3][3]float64
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			sat := (1 - s) * luma[j]
			if i == j {
				sat += s
			}
			m[i][j] = sat * k * d[j]
		}
	}
	off := 0.5 * (1 - k)
	out := make([]float64, 20)
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			out[i*5+j] = round5(m[i][j])
		}
		out[i*5+4] = round5(off)
	}
	out[18] = 1 // alpha unchanged
	return out
}

// ApplyLook applies a look to an sRGB colour (0–1 per channel), as the
// renderer's matrix does (clamped).
func ApplyLook(l *Look, rgb [3]float64) [3]float64 {
	m := LookMatrix(l)
	if m == nil {
		return rgb
	}
	var out [3]float64
	for i := 0; i < 3; i++ {
		v := m[i*5]*rgb[0] + m[i*5+1]*rgb[1] + m[i*5+2]*rgb[2] + m[i*5+4]
		out[i] = math.Max(0, math.Min(1, v))
	}
	return out
}
