package motion

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
)

// drawTestFrame draws a test frame the way the check does: the backdrop, a white
// bar, and a pixel half-way between the two (an antialiased edge).
func drawTestFrame(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, testBG)
		}
	}
	for y := 12; y < 18; y++ {
		for x := 4; x < 36; x++ {
			img.Set(x, y, color.RGBA{0xff, 0xff, 0xff, 0xff})
		}
	}
	img.Set(2, 2, color.RGBA{0x10 + 15, 0x10 + 15, 0x14 + 15, 0xff}) // a soft edge
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The preview's still keeps what the graphic drew and drops the check's
// backdrop (D187): the backdrop is transparent, the bar opaque white, an edge
// partly see-through with the graphic's own colour back.
func TestStillDropsTheBackdrop(t *testing.T) {
	b, err := Still(bytes.NewReader(drawTestFrame(t)))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	at := func(x, y int) color.NRGBA { return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA) }
	if c := at(0, 0); c.A != 0 {
		t.Errorf("the backdrop: %+v, want transparent", c)
	}
	if c := at(10, 14); c.A != 255 || c.R != 255 || c.G != 255 || c.B != 255 {
		t.Errorf("the bar: %+v, want opaque white", c)
	}
	if c := at(2, 2); c.A == 0 || c.A == 255 {
		t.Errorf("a soft edge: %+v, want partly see-through", c)
	}
	if img.Bounds().Dx() != 40 || img.Bounds().Dy() != 20 {
		t.Errorf("size %v: the still keeps the frame's size, so it sits in the graphic's place", img.Bounds())
	}
	if _, err := Still(bytes.NewReader([]byte("not a picture"))); err == nil {
		t.Error("a file that isn't a picture made a still")
	}
}
