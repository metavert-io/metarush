package visionmodels

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/onnx/onnxtest"
	"github.com/metavert-io/metarush/internal/vision"
)

// The model runners through onnxruntime, on stand-ins with the real models'
// names and shapes that answer from how bright the picture is (D413): they
// run wherever onnxruntime is (METARUSH_ORT_LIB, or the transcriber's
// install), on a Mac and on Linux, and are skipped plainly elsewhere. The
// real models are measured under -tags screeneval.

func standIns(t *testing.T) (lib string, p Paths) {
	t.Helper()
	lib = onnxtest.Library(t)
	dir := t.TempDir()
	return lib, Paths{Faces: onnxtest.FaceFinder(t, dir), TextDet: onnxtest.TextFinder(t, dir), TextRec: onnxtest.TextReader(t, dir),
		TextDict: onnxtest.TextChars(t, dir), Attention: onnxtest.Saliency(t, dir)}
}

func canvas(w, h int, fill color.Gray) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = fill.Y
	}
	return img
}

func rect(img *image.Gray, x0, y0, x1, y1 int, v uint8) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetGray(x, y, color.Gray{Y: v})
		}
	}
}

// A face where the stand-in sees one: one face, in the helper's terms, its
// five points in order; nothing where the picture is dark.
func TestFacesThroughTheRuntime(t *testing.T) {
	lib, p := standIns(t)
	e, err := OpenEngine(lib, p)
	if err != nil {
		t.Fatal(err)
	}
	img := canvas(120, 120, color.Gray{})
	rect(img, 40, 32, 72, 64, 255) // 32 × 32, middle (56, 48)
	f, err := e.Look(img, vision.Options{Text: "off"})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Faces) != 1 || f.W != 120 || f.H != 120 {
		t.Fatalf("one face: %+v", f)
	}
	fc := f.Faces[0]
	cx, cy := (fc.Box[0]+fc.Box[2]/2)*120, (fc.Box[1]+fc.Box[3]/2)*120
	if cx < 40 || cx > 72 || cy < 32 || cy > 80 || fc.Conf < FaceMin || len(fc.LM) != 5 || fc.LM[0][0] >= fc.LM[1][0] || fc.Quality == nil {
		t.Fatalf("the face %+v (middle %.0f, %.0f)", fc, cx, cy)
	}
	if f.Labels == nil || f.Animals == nil || f.Humans == nil || f.Text != nil {
		t.Fatalf("what the models don't look for is empty, not missing; text wasn't asked for: %+v", f)
	}
	dark, err := e.Look(canvas(120, 120, color.Gray{}), vision.Options{Text: "off"})
	if err != nil || len(dark.Faces) != 0 {
		t.Fatalf("a dark picture: %+v %v", dark, err)
	}
	if again, _ := OpenEngine(lib, p); again != e {
		t.Fatal("the models are loaded once per process")
	}
}

// A line of text where the stand-in detector sees one, read by the stand-in
// recognizer (its one character, once per bright stretch).
func TestTextThroughTheRuntime(t *testing.T) {
	lib, p := standIns(t)
	e, err := OpenEngine(lib, p)
	if err != nil {
		t.Fatal(err)
	}
	// Dark grey, so the face stand-in sees nothing; three short white bars on
	// one line read as three characters, and the line's box is around them.
	img := canvas(320, 96, color.Gray{Y: 20})
	for _, x := range []int{40, 104, 168} {
		rect(img, x, 40, x+40, 56, 255)
	}
	rect(img, 80, 40, 104, 56, 255) // the first two bars meet: one stretch
	f, err := e.Look(img, vision.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Text) == 0 {
		t.Fatalf("no text: %+v", f)
	}
	var all []string
	for _, x := range f.Text {
		all = append(all, x.S)
		if x.Box[1] > 40.0/96 || x.Box[1]+x.Box[3] < 56.0/96 {
			t.Errorf("a line's box misses the bars: %v", x.Box)
		}
	}
	if s := strings.Join(all, " "); !strings.Contains(s, "I") {
		t.Fatalf("read %q", s)
	}
}

// Where no face is seen, the stand-in saliency map says what stands out; a
// frame with a face has the face as where the eye is drawn.
func TestAttentionThroughTheRuntime(t *testing.T) {
	lib, p := standIns(t)
	e, err := OpenEngine(lib, p)
	if err != nil {
		t.Fatal(err)
	}
	img := canvas(200, 100, color.Gray{})
	rect(img, 130, 30, 170, 70, 128) // mid-grey: not bright enough for the face stand-in
	f, err := e.Look(img, vision.Options{Text: "off", Attention: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Faces) != 0 || f.Attention == nil || f.Attention.At[0] < 0.6 || f.Attention.Peak < 0.5 || len(f.Attention.Objects) == 0 {
		t.Fatalf("what stands out: %+v %+v", f.Faces, f.Attention)
	}
	face := canvas(120, 120, color.Gray{})
	rect(face, 40, 32, 72, 64, 255)
	g, err := e.Look(face, vision.Options{Text: "off", Attention: true})
	if err != nil || len(g.Faces) != 1 || g.Attention == nil || len(g.Attention.Objects) != 1 || g.Attention.Objects[0].Box != g.Faces[0].Box {
		t.Fatalf("a face is where the eye is drawn: %+v %v", g.Attention, err)
	}
}

// Analyze looks at a folder of JPEGs in name order, as the helper does, and
// says which frame it couldn't read.
func TestAnalyzeAFolder(t *testing.T) {
	lib, p := standIns(t)
	e, err := OpenEngine(lib, p)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for i, name := range []string{"f00002.jpg", "f00001.jpg"} {
		img := canvas(120, 120, color.Gray{})
		if i == 0 {
			rect(img, 40, 32, 72, 64, 255)
		}
		fh, _ := os.Create(filepath.Join(dir, name))
		_ = jpeg.Encode(fh, img, &jpeg.Options{Quality: 95})
		fh.Close()
	}
	_ = os.WriteFile(filepath.Join(dir, "f00003.jpg"), []byte("not a picture"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a frame"), 0o644)
	var got []vision.Frame
	if err := e.Analyze(context.Background(), dir, vision.Options{Text: "off"}, func(f vision.Frame) { got = append(got, f) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].File != "f00001.jpg" || got[1].File != "f00002.jpg" || len(got[0].Faces) != 0 || len(got[1].Faces) != 1 || got[2].Error == "" {
		t.Fatalf("%+v", got)
	}
	if err := e.Analyze(context.Background(), "relative", vision.Options{}, func(vision.Frame) {}); err == nil {
		t.Fatal("a relative folder was looked at")
	}
}

// The subject is cut out where the stand-in sees it: the picture's own
// pixels kept there and see-through elsewhere, at its size; an empty
// picture has no subject and nothing is written.
func TestCutOutThroughTheRuntime(t *testing.T) {
	lib := onnxtest.Library(t)
	dir := t.TempDir()
	m, err := OpenMasker(lib, onnxtest.Subject(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 300; x++ {
			c := color.RGBA{10, 10, 10, 255}
			if (x-150)*(x-150)+(y-100)*(y-100) < 60*60 {
				c = color.RGBA{250, 240, 230, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	in, out := filepath.Join(dir, "in.png"), filepath.Join(dir, "out.png")
	fh, _ := os.Create(in)
	_ = png.Encode(fh, img)
	fh.Close()
	found, err := m.CutOut(in, out)
	if err != nil || !found {
		t.Fatalf("%v %v", found, err)
	}
	fh, _ = os.Open(out)
	cut, err := png.Decode(fh)
	fh.Close()
	if err != nil || cut.Bounds().Dx() != 300 || cut.Bounds().Dy() != 200 {
		t.Fatalf("the cut-out %v %v", cut.Bounds(), err)
	}
	_, _, _, inside := cut.At(150, 100).RGBA()
	_, _, _, outside := cut.At(10, 10).RGBA()
	r, _, _, _ := color.NRGBAModel.Convert(cut.At(150, 100)).RGBA()
	if inside < 0xf000 || outside > 0x1000 || r>>8 < 240 {
		t.Fatalf("inside %x, outside %x, red %d", inside, outside, r>>8)
	}
	empty := filepath.Join(dir, "empty.png")
	fh, _ = os.Create(empty)
	_ = png.Encode(fh, canvas(64, 64, color.Gray{}))
	fh.Close()
	if found, err := m.CutOut(empty, filepath.Join(dir, "none.png")); err != nil || found {
		t.Fatalf("no subject: %v %v", found, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "none.png")); err == nil {
		t.Fatal("a cut-out was written for a picture with no subject")
	}
	if _, err := m.CutOut("relative.png", out); err == nil {
		t.Fatal("a relative path")
	}
}

// A shape the face finder is unsure of with no detail in it isn't a face
// (D407): a smooth grey glow, like a call's blurred background, is left out;
// the same glow with detail in it, or one the finder is sure of, is a face.
func TestAFeaturelessShapeIsNotAFace(t *testing.T) {
	lib, p := standIns(t)
	e, err := OpenEngine(lib, p)
	if err != nil {
		t.Fatal(err)
	}
	glow := func(top float64, detail uint8) *image.Gray {
		img := image.NewGray(image.Rect(0, 0, 160, 160))
		for y := 0; y < 160; y++ {
			for x := 0; x < 160; x++ {
				v := top - 0.5*math.Hypot(float64(x)-80, float64(y)-80)
				if detail > 0 && (x+y)%2 == 0 {
					v -= float64(detail)
				} else if detail > 0 {
					v += float64(detail)
				}
				img.SetGray(x, y, color.Gray{Y: uint8(math.Max(0, math.Min(255, v)))})
			}
		}
		return img
	}
	for _, c := range []struct {
		name string
		img  *image.Gray
		face bool
	}{
		{"a smooth grey glow the finder is 0.67 sure of", glow(170, 0), false},
		{"the same glow with detail in it", glow(170, 30), true},
		{"a smooth glow the finder is sure of", glow(250, 0), true},
	} {
		f, err := e.Look(c.img, vision.Options{Text: "off"})
		if err != nil {
			t.Fatal(err)
		}
		if (len(f.Faces) > 0) != c.face {
			t.Errorf("%s: %d faces", c.name, len(f.Faces))
		}
	}
}
