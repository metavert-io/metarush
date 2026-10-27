package fontface

import (
	"image"
	"image/color"
	"sync"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gomonobold"
	"golang.org/x/image/math/fixed"
)

// Sheets drawn at the same time each get their own face (run with -race to see
// a shared one fail): this is what two contact sheets finishing together do.
func TestFacesDrawAtTheSameTime(t *testing.T) {
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fc := New("gomonobold", gomonobold.TTF, 15)
			if fc == nil {
				t.Error("no face")
				return
			}
			defer fc.Close()
			img := image.NewRGBA(image.Rect(0, 0, 320, 40))
			for i := 0; i < 50; i++ {
				d := &font.Drawer{Dst: img, Src: &image.Uniform{color.White}, Face: fc, Dot: fixed.P(2, 30)}
				d.DrawString("#12  0:42.500 · test")
			}
		}()
	}
	wg.Wait()
	if New("broken", []byte("not a font"), 12) != nil {
		t.Fatal("a font that can't be read makes no face")
	}
}
