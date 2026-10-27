package derive

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/math/fixed"

	"github.com/metavert-io/metarush/internal/fontface"
	"github.com/metavert-io/metarush/internal/models"
)

// Tile is one frame of a labeled sheet: its source time, a small badge drawn
// on the picture ("#3") and the caption under it ("0:42.5 · Maya: “and
// then the bunny…”"), wrapped to at most two lines.
type Tile struct {
	At      float64
	Badge   string
	Caption string
}

// sheetFaces makes the caption and badge faces for one sheet (fontface: a face
// is never shared between sheets drawn at the same time); close both after it.
func sheetFaces() (font.Face, font.Face) {
	return fontface.New("goregular", goregular.TTF, 14), fontface.New("gobold", gobold.TTF, 13)
}

// wrap breaks text into at most lines lines of at most width pixels; the last
// line ends with "…" when the text does not fit.
func wrap(fc font.Face, text string, width, lines int) []string {
	words := strings.Fields(text)
	var out []string
	cur := ""
	fits := func(s string) bool { return font.MeasureString(fc, s).Ceil() <= width }
	for i := 0; i < len(words); i++ {
		w := words[i]
		next := w
		if cur != "" {
			next = cur + " " + w
		}
		if fits(next) {
			cur = next
			continue
		}
		if cur == "" { // one word wider than the line: cut it
			for len([]rune(w)) > 1 && !fits(w+"…") {
				r := []rune(w)
				w = string(r[:len(r)-1])
			}
			cur = w + "…"
			out = append(out, cur)
			return out
		}
		out = append(out, cur)
		cur = w
		if len(out) == lines {
			// No room left: end the last line with an ellipsis.
			last := out[lines-1]
			for len([]rune(last)) > 1 && !fits(last+"…") {
				r := []rune(last)
				last = strings.TrimRight(string(r[:len(r)-1]), " ")
			}
			if !strings.HasSuffix(last, "…") {
				last += "…"
			}
			out[lines-1] = last
			return out
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// LabeledSheet grabs each tile's frame (a few at a time) and lays them out in a
// grid with badges and captions. It returns the variant description of out.
func (d *Deriver) LabeledSheet(ctx context.Context, in, out, scratch string, mi *models.MediaInfo, tiles []Tile) (*models.Variant, error) {
	if len(tiles) == 0 {
		return nil, fmt.Errorf("no frames to show")
	}
	dir, err := os.MkdirTemp(scratch, "sheet-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	portrait := mi.Height > mi.Width
	tileW, cols := 400, 4
	if portrait {
		tileW, cols = 240, 6
	}
	if len(tiles) < cols {
		cols = len(tiles)
	}
	frames := make([]image.Image, len(tiles))
	errs := make([]error, len(tiles))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, t := range tiles {
		wg.Add(1)
		go func(i int, t Tile) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			p := filepath.Join(dir, fmt.Sprintf("f%03d.jpg", i))
			if err := d.Frame(ctx, in, p, t.At, tileW); err != nil {
				errs[i] = err
				return
			}
			frames[i], errs[i] = loadJPEG(p)
		}(i, t)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	sheet := composeLabeled(frames, tiles, cols)
	f, err := os.Create(out)
	if err != nil {
		return nil, err
	}
	if err := jpeg.Encode(f, sheet, &jpeg.Options{Quality: 82}); err != nil {
		f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	v, err := d.describe(ctx, out, models.VariantContactSheet, "image/jpeg", map[string]string{"frames": fmt.Sprint(len(tiles)), "cols": fmt.Sprint(cols)})
	if v != nil {
		v.Width, v.Height = sheet.Bounds().Dx(), sheet.Bounds().Dy()
	}
	return v, err
}

func composeLabeled(frames []image.Image, tiles []Tile, cols int) *image.RGBA {
	tw, th := frames[0].Bounds().Dx(), frames[0].Bounds().Dy()
	for _, f := range frames { // frames of one source share a size; be safe anyway
		if f.Bounds().Dy() > th {
			th = f.Bounds().Dy()
		}
	}
	capFace, badge := sheetFaces()
	for _, fc := range []font.Face{capFace, badge} {
		if fc != nil {
			defer fc.Close()
		}
	}
	gap, lineH := 6, 17
	label := 2*lineH + 8
	rows := (len(frames) + cols - 1) / cols
	W := cols*tw + (cols+1)*gap
	H := rows*(th+label) + (rows+1)*gap
	img := image.NewRGBA(image.Rect(0, 0, W, H))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{14, 14, 18, 255}}, image.Point{}, draw.Src)
	ink := &image.Uniform{color.RGBA{232, 228, 220, 255}}
	for i, fr := range frames {
		x := gap + (i%cols)*(tw+gap)
		y := gap + (i/cols)*(th+label+gap)
		draw.Draw(img, image.Rect(x, y, x+fr.Bounds().Dx(), y+fr.Bounds().Dy()), fr, fr.Bounds().Min, draw.Src)
		if badge != nil && tiles[i].Badge != "" {
			bw := font.MeasureString(badge, tiles[i].Badge).Ceil() + 10
			draw.Draw(img, image.Rect(x+4, y+4, x+4+bw, y+4+19), &image.Uniform{color.RGBA{0, 0, 0, 170}}, image.Point{}, draw.Over)
			dr := &font.Drawer{Dst: img, Src: ink, Face: badge, Dot: fixed.P(x+9, y+18)}
			dr.DrawString(tiles[i].Badge)
		}
		if capFace != nil {
			for k, line := range wrap(capFace, tiles[i].Caption, tw-4, 2) {
				dr := &font.Drawer{Dst: img, Src: ink, Face: capFace, Dot: fixed.P(x+2, y+th+lineH+k*lineH)}
				dr.DrawString(line)
			}
		}
	}
	return img
}
