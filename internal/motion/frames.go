package motion

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/math/fixed"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/fontface"
	"github.com/metavert-io/metarush/internal/models"
)

// TestBackground is what the test frames are drawn over, so what the
// component draws can be told apart from the frame (layout checks).
const TestBackground = "#101014"

var testBG = color.RGBA{0x10, 0x10, 0x14, 0xff}

// Frame is one test frame on disk.
type Frame struct {
	Label string
	At    float64 // seconds from the graphic's start
	Path  string
	Ms    int    // how long it took to draw
	Shape string // the shape it was drawn in (D164; "" the only one)
}

func loadPNG(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return png.Decode(f)
}

// drawnBox is the bounding box of everything that differs from the test
// background (what the component drew), or empty.
func drawnBox(img image.Image) image.Rectangle {
	b := img.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X-1, b.Min.Y-1
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, _ := img.At(x, y).RGBA()
			if absDiff(r>>8, uint32(testBG.R)) > 24 || absDiff(g>>8, uint32(testBG.G)) > 24 || absDiff(bl>>8, uint32(testBG.B)) > 24 {
				minX, minY, maxX, maxY = min(minX, x), min(minY, y), max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX < minX {
		return image.Rectangle{}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}

func absDiff(a, b uint32) uint32 {
	if a > b {
		return a - b
	}
	return b - a
}

// differs reports whether two renders of the same frame differ beyond
// antialiasing noise (0.2% of pixels by more than 8 levels).
func differs(a, b image.Image) bool {
	if a.Bounds() != b.Bounds() {
		return true
	}
	bad, n := 0, 0
	r := a.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y += 2 {
		for x := r.Min.X; x < r.Max.X; x += 2 {
			n++
			r1, g1, b1, _ := a.At(x, y).RGBA()
			r2, g2, b2, _ := b.At(x, y).RGBA()
			if absDiff(r1>>8, r2>>8) > 8 || absDiff(g1>>8, g2>>8) > 8 || absDiff(b1>>8, b2>>8) > 8 {
				bad++
			}
		}
	}
	return n > 0 && float64(bad)/float64(n) > 0.002
}

// Analyze looks at the test frames: where the component draws compared with
// the safe area and the caption band, whether it draws the same frame the
// same way twice, and how long frames take.
func Analyze(frames []Frame, again string, region string, safe editspec.SafeArea, w, h int) []models.MotionProblem {
	var out []models.MotionProblem
	warn := func(code, msg, hint string) {
		out = append(out, models.MotionProblem{Severity: "warning", Code: code, Message: msg, Hint: hint})
	}
	safeRect := image.Rect(safe.Left, safe.Top, w-safe.Right, h-safe.Bottom)
	outside, empty := map[string]bool{}, 0
	var mid image.Image
	slowest := 0
	for _, f := range frames {
		slowest = max(slowest, f.Ms)
		img, err := loadPNG(f.Path)
		if err != nil {
			continue
		}
		if f.Label == "middle" {
			mid = img
		}
		box := drawnBox(img)
		if box.Empty() {
			empty++
			continue
		}
		if region == "full" {
			continue // full-frame graphics may fill the frame; their text is for the reviewer to judge
		}
		const slack = 4
		if box.Min.X < safeRect.Min.X-slack {
			outside["left"] = true
		}
		if box.Max.X > safeRect.Max.X+slack {
			outside["right"] = true
		}
		if box.Min.Y < safeRect.Min.Y-slack {
			outside["top"] = true
		}
		if box.Max.Y > safeRect.Max.Y+slack {
			outside["bottom"] = true
		}
		if region == "lower" && box.Min.Y < h/2 {
			outside["above the lower band"] = true
		}
	}
	var sides []string
	for _, s := range []string{"top", "bottom", "left", "right"} {
		if outside[s] {
			sides = append(sides, s)
		}
	}
	if len(sides) > 0 {
		warn("outside_safe_area", fmt.Sprintf("It draws outside the safe area (%s), where platform buttons and captions can cover it.", joinWords(sides)),
			"Keep text and shapes inside layout.content (the safe area with a margin).")
	}
	if outside["above the lower band"] {
		warn("region_mismatch", "It's meant for the lower part of the frame (region lower), but it draws in the upper half.", "Set region to center or full, or keep it in the lower band.")
	}
	if empty == len(frames) && len(frames) > 0 {
		warn("draws_nothing", "None of the test frames show anything.", "Check the sample parameters in preview.json and the animation timing.")
	}
	if again != "" && mid != nil {
		if b, err := loadPNG(again); err == nil && differs(mid, b) {
			warn("nondeterministic_frame", "The middle frame came out differently when it was drawn twice; frames render in parallel and in any order, so it may flicker.",
				`Use random("seed") from remotion instead of Math.random(), and useCurrentFrame() instead of the clock.`)
		}
	}
	if slowest > 4000 {
		warn("slow_frames", fmt.Sprintf("A test frame took %.1f s to draw; renders with it will be slow.", float64(slowest)/1000), "Draw less per frame, or compute expensive things once with useMemo.")
	}
	return out
}

func joinWords(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	s := xs[0]
	for i := 1; i < len(xs)-1; i++ {
		s += ", " + xs[i]
	}
	return s + " and " + xs[len(xs)-1]
}

// labelFace makes the label face for one sheet (fontface: a face is never
// shared between sheets drawn at the same time).
func labelFace() font.Face { return fontface.New("gomonobold", gomonobold.TTF, 14) }

// ContactSheet lays the test frames out in a labeled grid (JPEG) with the
// safe area outlined in amber on each tile.
func ContactSheet(frames []Frame, out string, safe editspec.SafeArea, w, h int) (width, height int, err error) {
	var imgs []image.Image
	var labels []string
	for i, f := range frames {
		img, err := loadPNG(f.Path)
		if err != nil {
			return 0, 0, err
		}
		imgs = append(imgs, img)
		labels = append(labels, fmt.Sprintf("#%d %s %s", i+1, f.Label, clock(f.At)))
	}
	if len(imgs) == 0 {
		return 0, 0, fmt.Errorf("no frames")
	}
	tw := 400
	cols := 3
	if h > w {
		tw, cols = 240, len(imgs)
		if cols > 6 {
			cols = 6
		}
	}
	th := int(math.Round(float64(tw) * float64(h) / float64(w)))
	if len(imgs) < cols {
		cols = len(imgs)
	}
	gap, label := 6, 22
	rows := (len(imgs) + cols - 1) / cols
	W, H := cols*tw+(cols+1)*gap, rows*(th+label)+(rows+1)*gap
	sheet := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(sheet, sheet.Bounds(), &image.Uniform{color.RGBA{14, 14, 18, 255}}, image.Point{}, draw.Src)
	sx, sy := float64(tw)/float64(w), float64(th)/float64(h)
	amber := color.RGBA{224, 164, 88, 255}
	fc := labelFace()
	if fc != nil {
		defer fc.Close()
	}
	for i, img := range imgs {
		x := gap + (i%cols)*(tw+gap)
		y := gap + (i/cols)*(th+label+gap)
		dst := image.Rect(x, y, x+tw, y+th)
		xdraw.CatmullRom.Scale(sheet, dst, img, img.Bounds(), xdraw.Src, nil)
		r := image.Rect(x+int(float64(safe.Left)*sx), y+int(float64(safe.Top)*sy), x+tw-int(float64(safe.Right)*sx)-1, y+th-int(float64(safe.Bottom)*sy)-1)
		for px := r.Min.X; px <= r.Max.X; px += 2 {
			sheet.Set(px, r.Min.Y, amber)
			sheet.Set(px, r.Max.Y, amber)
		}
		for py := r.Min.Y; py <= r.Max.Y; py += 2 {
			sheet.Set(r.Min.X, py, amber)
			sheet.Set(r.Max.X, py, amber)
		}
		if fc != nil {
			dr := &font.Drawer{Dst: sheet, Src: &image.Uniform{color.RGBA{232, 228, 220, 255}}, Face: fc, Dot: fixed.P(x+2, y+th+16)}
			s := labels[i]
			for s != "" && dr.MeasureString(s).Ceil() > tw-4 {
				s = string([]rune(s)[:len([]rune(s))-1])
			}
			dr.DrawString(s)
		}
	}
	f, err := os.Create(out)
	if err != nil {
		return 0, 0, err
	}
	if err := jpeg.Encode(f, sheet, &jpeg.Options{Quality: 84}); err != nil {
		f.Close()
		return 0, 0, err
	}
	return W, H, f.Close()
}

// clock formats seconds as M:SS.s.
func clock(t float64) string {
	m := int(t) / 60
	return fmt.Sprintf("%d:%04.1f", m, t-float64(m*60))
}
