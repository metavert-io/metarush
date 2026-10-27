package qa

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/math/fixed"

	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/fontface"
	"github.com/metavert-io/metarush/internal/models"
)

// moment is a timeline instant worth looking at.
type moment struct {
	frame    int
	labels   []string
	reason   string
	findings []string
}

// moments lists every cut, overlay start, finding and the first/last frame.
func (r *run) moments() []moment {
	p := r.plan
	last := max(0, p.DurationInFrames-1)
	clampF := func(f int) int { return max(0, min(last, f)) }
	var ms []moment
	addM := func(f int, label, reason string, finding string) {
		f = clampF(f)
		for i := range ms {
			if absI(ms[i].frame-f) <= 1 {
				if !contains(ms[i].labels, label) {
					ms[i].labels = append(ms[i].labels, label)
				}
				if finding != "" {
					ms[i].findings = append(ms[i].findings, finding)
				}
				return
			}
		}
		m := moment{frame: f, labels: []string{label}, reason: reason}
		if finding != "" {
			m.findings = []string{finding}
		}
		ms = append(ms, m)
	}
	for i, c := range p.Clips {
		settle := 2
		switch c.TransitionIn.Type {
		case editspec.TransitionCrossfade:
			settle = c.TransitionIn.DurationInFrames / 2
		case editspec.TransitionFade, editspec.TransitionDip, editspec.TransitionDipWhite,
			editspec.TransitionWipe, editspec.TransitionSlide, editspec.TransitionPush, editspec.TransitionZoom, editspec.TransitionWhip:
			settle = c.TransitionIn.DurationInFrames + 1
		}
		settle = min(settle, max(0, c.DurationInFrames/2))
		if i == 0 {
			addM(c.From+settle, "start "+c.ID, "start", "")
			continue
		}
		addM(c.From+settle, "cut→"+c.ID, "cut", "")
	}
	for _, o := range p.Overlays {
		addM(o.From+min(int(math.Round(0.6*r.fps)), o.DurationInFrames/2), "ov "+o.ID, "overlay", "")
	}
	for _, f := range r.pend {
		if f.To <= f.From && f.From == 0 {
			continue // whole-file findings without a moment
		}
		if f.To-f.From >= r.total()-0.01 {
			continue
		}
		mid := int(math.Round((f.From + f.To) / 2 * r.fps))
		addM(mid, f.ID+" "+f.Kind, "finding", f.ID)
	}
	addM(last-1, "end", "end", "")
	sort.Slice(ms, func(i, j int) bool { return ms[i].frame < ms[j].frame })
	if len(ms) > r.in.MaxFrames {
		var keep, cuts []moment
		for _, m := range ms {
			if m.reason == "cut" {
				cuts = append(cuts, m)
			} else {
				keep = append(keep, m)
			}
		}
		room := r.in.MaxFrames - len(keep)
		if room > 0 && len(cuts) > 0 {
			step := float64(len(cuts)) / float64(room)
			for k := 0; k < room && int(float64(k)*step) < len(cuts); k++ {
				keep = append(keep, cuts[int(float64(k)*step)])
			}
			r.rep.Limitations = append(r.rep.Limitations, fmt.Sprintf("The video has %d cuts, and frames were taken at %d of them, spread evenly.", len(cuts), min(room, len(cuts))))
		}
		ms = keep
		if len(ms) > r.in.MaxFrames {
			ms = ms[:r.in.MaxFrames]
		}
		sort.Slice(ms, func(i, j int) bool { return ms[i].frame < ms[j].frame })
	}
	return ms
}

// visual writes the overview contact sheet, the moments sheet and one still per moment.
func (r *run) visual(ctx context.Context) error {
	if !r.mi.HasVideo || r.in.OutDir == "" {
		return nil
	}
	dir := r.in.OutDir
	fdir := filepath.Join(dir, "frames")
	if err := os.MkdirAll(fdir, 0o755); err != nil {
		return err
	}
	d := &derive.Deriver{T: r.t}
	total := r.rep.Measurements.Duration
	if total <= 0 {
		total = r.total()
	}
	vertical := r.mi.Height > r.mi.Width
	tileW := 400
	cols := 4
	if vertical {
		tileW, cols = 240, 6
	}
	// Stills can only come from frames that exist in the file.
	lastT := math.Max(0, total-1/r.fps)
	failed := 0
	// Overview: evenly spaced frames labeled with timecode and the clip on screen.
	n := int(math.Max(8, math.Min(24, math.Round(total/2.5))))
	var tiles []tile
	for i := 0; i < n; i++ {
		t := round3(math.Min(lastT, total*(float64(i)+0.5)/float64(n)))
		img, err := r.grab(ctx, d, t, tileW)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++
			continue
		}
		tiles = append(tiles, tile{img: img, line1: fmt.Sprintf("#%d %s", i+1, derive.Timecode(t)), line2: r.clipLabel(t)})
	}
	if len(tiles) > 0 {
		sheetPath := filepath.Join(dir, "contact-sheet.jpg")
		if err := writeSheet(sheetPath, tiles, cols, nil); err != nil {
			return err
		}
		r.rep.ContactSheet = &models.QAFrame{ID: "sheet", Label: fmt.Sprintf("%d evenly spaced frames, labeled #n, timeline M:SS.mmm and the clip on screen", len(tiles)), Reason: "sheet", File: "contact-sheet.jpg", Path: sheetPath}
	}
	// Moments: cuts, overlays, findings — one full-size still each, plus a sheet.
	ms := r.moments()
	var mtiles []tile
	for i, m := range ms {
		t := round3(math.Min(lastT, float64(m.frame)/r.fps))
		id := fmt.Sprintf("fr%02d", i+1)
		name := fmt.Sprintf("%02d-%s-%s.jpg", i+1, m.reason, strings.ReplaceAll(derive.Timecode(t), ":", "m"))
		p := filepath.Join(fdir, name)
		if err := d.Frame(ctx, r.in.Path, p, t, 960); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			failed++
			continue
		}
		full, err := loadJPEG(p)
		if err != nil {
			failed++
			continue
		}
		label := strings.Join(m.labels, " · ")
		r.rep.Frames = append(r.rep.Frames, models.QAFrame{ID: id, At: t, Label: label, Reason: m.reason, File: "frames/" + name, Path: p})
		for _, fid := range m.findings {
			if f := r.finding(fid); f != nil {
				f.Frames = append(f.Frames, id)
			}
		}
		mtiles = append(mtiles, tile{img: scaleTo(full, tileW), line1: fmt.Sprintf("%s %s", id, derive.Timecode(t)), line2: label, frame: m.frame})
	}
	if len(mtiles) > 0 {
		mp := filepath.Join(dir, "moments-sheet.jpg")
		if err := writeSheet(mp, mtiles, cols, r.guides()); err != nil {
			return err
		}
		r.rep.MomentsSheet = &models.QAFrame{ID: "moments", Label: "a frame at every cut, overlay start and finding; amber = plan safe area, cyan = caption band while a caption is on screen", Reason: "sheet", File: "moments-sheet.jpg", Path: mp}
	}
	if failed > 0 {
		r.rep.Limitations = append(r.rep.Limitations, fmt.Sprintf("%s couldn't be taken from the render.", upperFirst(count(failed, "frame"))))
	}
	return nil
}

func loadJPEG(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return jpeg.Decode(f)
}

// scaleTo resizes so the long side (vertical: height, else width) is n pixels,
// matching derive.Frame's sizing for sheet tiles.
func scaleTo(src image.Image, n int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return src
	}
	var nw, nh int
	if w > h {
		nw, nh = n, int(math.Round(float64(h)*float64(n)/float64(w)))
	} else {
		nw, nh = int(math.Round(float64(w)*float64(n)/float64(h))), n
	}
	nw += nw % 2
	nh += nh % 2
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, b, xdraw.Src, nil)
	return dst
}

func (r *run) clipLabel(t float64) string {
	f := int(math.Round(t * r.fps))
	cs := r.clipsAt(f, f+1)
	if len(cs) == 0 {
		return ""
	}
	return strings.Join(ids(cs), "+")
}

func (r *run) grab(ctx context.Context, d *derive.Deriver, t float64, w int) (image.Image, error) {
	f, err := os.CreateTemp(r.in.Scratch, "qa-*.jpg")
	if err != nil {
		return nil, err
	}
	f.Close()
	defer os.Remove(f.Name())
	if err := d.Frame(ctx, r.in.Path, f.Name(), t, w); err != nil {
		return nil, err
	}
	in, err := os.Open(f.Name())
	if err != nil {
		return nil, err
	}
	defer in.Close()
	return jpeg.Decode(in)
}

// guides returns the plan boxes to outline on a tile showing timeline frame f.
func (r *run) guides() func(f int) []guide {
	g := newGeom(r.plan)
	return func(f int) []guide {
		out := []guide{{b: g.safe, c: color.RGBA{240, 180, 60, 255}}}
		if c := r.plan.Captions; c != nil {
			for _, l := range c.Lines {
				if f >= l.From && f < l.To {
					out = append(out, guide{b: g.captionZone(c.Position, c.Style, c.Size), c: color.RGBA{80, 210, 240, 255}})
					break
				}
			}
		}
		for i := range out {
			out[i].sx, out[i].sy = 1/g.W, 1/g.H
		}
		return out
	}
}

type tile struct {
	img          image.Image
	line1, line2 string
	frame        int
}

type guide struct {
	b      box
	c      color.RGBA
	sx, sy float64 // normalize plan px to 0..1
}

// labelFace makes the label face for one sheet (fontface: a face is never
// shared between sheets drawn at the same time).
func labelFace() font.Face { return fontface.New("gomonobold", gomonobold.TTF, 14) }

func writeSheet(path string, tiles []tile, cols int, guides func(int) []guide) error {
	if len(tiles) == 0 {
		return nil
	}
	if len(tiles) < cols {
		cols = len(tiles)
	}
	tw, th := tiles[0].img.Bounds().Dx(), tiles[0].img.Bounds().Dy()
	gap, label := 6, 36
	rows := (len(tiles) + cols - 1) / cols
	W, H := cols*tw+(cols+1)*gap, rows*(th+label)+(rows+1)*gap
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{14, 14, 18, 255}}, image.Point{}, draw.Src)
	fc := labelFace()
	if fc != nil {
		defer fc.Close()
	}
	for i, t := range tiles {
		x := gap + (i%cols)*(tw+gap)
		y := gap + (i/cols)*(th+label+gap)
		draw.Draw(img, image.Rect(x, y, x+tw, y+th), t.img, t.img.Bounds().Min, draw.Src)
		if guides != nil {
			for _, g := range guides(t.frame) {
				outline(img, image.Rect(x+int(g.b.L*g.sx*float64(tw)), y+int(g.b.T*g.sy*float64(th)), x+int(g.b.R*g.sx*float64(tw))-1, y+int(g.b.B*g.sy*float64(th))-1), g.c)
			}
		}
		if fc != nil {
			for k, s := range []string{t.line1, t.line2} {
				dr := &font.Drawer{Dst: img, Src: &image.Uniform{color.RGBA{232, 228, 220, 255}}, Face: fc, Dot: fixed.P(x+2, y+th+15+k*16)}
				for s != "" && dr.MeasureString(s).Ceil() > tw-4 {
					s = string([]rune(s)[:len([]rune(s))-1])
				}
				dr.DrawString(s)
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 82}); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func outline(img *image.RGBA, r image.Rectangle, c color.RGBA) {
	r = r.Intersect(img.Bounds())
	for x := r.Min.X; x <= r.Max.X; x++ {
		img.SetRGBA(x, r.Min.Y, c)
		img.SetRGBA(x, r.Max.Y, c)
	}
	for y := r.Min.Y; y <= r.Max.Y; y++ {
		img.SetRGBA(r.Min.X, y, c)
		img.SetRGBA(r.Max.X, y, c)
	}
}
