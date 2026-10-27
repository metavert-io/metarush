package motion

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
)

// Still turns one test frame (drawn over TestBackground in the render jail)
// into the picture the dashboard's live preview shows in the graphic's place
// (D187): the backdrop becomes transparent, edges blend softly, and what the
// graphic drew keeps its colours. Only pixels are read and written; nothing a
// graphic made runs anywhere but the jail.
func Still(r io.Reader) ([]byte, error) {
	src, err := png.Decode(r)
	if err != nil {
		return nil, err
	}
	out := KeyBackdrop(src)
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Thresholds of the backdrop key: a pixel this close to TestBackground (the
// largest channel difference) is backdrop; this far or more is the graphic.
// drawnBox calls 24 levels "drawn"; the soft edge spans it.
const (
	keyBackdrop = 10
	keyDrawn    = 40
)

// KeyBackdrop makes TestBackground transparent: alpha follows the distance
// from the backdrop colour, and partly covered pixels get the colour the
// graphic drew there back (their mix with the backdrop undone).
func KeyBackdrop(src image.Image) *image.NRGBA {
	b := src.Bounds()
	out := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	bg := [3]float64{float64(testBG.R), float64(testBG.G), float64(testBG.B)}
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := src.At(x, y).RGBA()
			c := [3]float64{float64(r >> 8), float64(g >> 8), float64(bl >> 8)}
			d := 0.0
			for i := range c {
				d = max(d, absF(c[i]-bg[i]))
			}
			alpha := 1.0
			switch {
			case d <= keyBackdrop:
				alpha = 0
			case d < keyDrawn:
				alpha = (d - keyBackdrop) / (keyDrawn - keyBackdrop)
			}
			alpha *= float64(a>>8) / 255
			if alpha <= 0 {
				continue // transparent (the zero value)
			}
			var px [3]uint8
			for i := range c {
				v := c[i]
				if alpha < 1 {
					v = (c[i] - (1-alpha)*bg[i]) / alpha
				}
				px[i] = uint8(min(255, max(0, v+0.5)))
			}
			out.SetNRGBA(x-b.Min.X, y-b.Min.Y, color.NRGBA{R: px[0], G: px[1], B: px[2], A: uint8(min(255, alpha*255+0.5))})
		}
	}
	return out
}

func absF(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
