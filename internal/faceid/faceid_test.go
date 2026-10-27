package faceid

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image"
	"image/color"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/metavert-io/metarush/internal/onnx/onnxtest"
)

// Points moved by a known rotation, scale and shift come back to the template.
func TestSimilarityRecoversTheTransform(t *testing.T) {
	th, sc, dx, dy := 0.3, 2.5, 140.0, 60.0
	var src [5][2]float64
	for i, p := range template {
		x, y := p[0]*sc, p[1]*sc
		src[i] = [2]float64{x*math.Cos(th) - y*math.Sin(th) + dx, x*math.Sin(th) + y*math.Cos(th) + dy}
	}
	m := Similarity(src)
	for i, p := range src {
		x, y := m.Apply(p[0], p[1])
		if math.Abs(x-template[i][0]) > 1e-6 || math.Abs(y-template[i][1]) > 1e-6 {
			t.Fatalf("point %d: %.4f,%.4f want %v", i, x, y, template[i])
		}
	}
	inv := m.Invert()
	x, y := inv.Apply(template[2][0], template[2][1])
	if math.Abs(x-src[2][0]) > 1e-6 || math.Abs(y-src[2][1]) > 1e-6 {
		t.Fatalf("inverse: %.4f,%.4f want %v", x, y, src[2])
	}
	if (Similarity([5][2]float64{})) != (Affine{}) {
		t.Fatal("points on top of each other have no transform")
	}
}

// pattern is a picture whose every pixel says where it is.
func pattern(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), uint8((x + y) / 2), 255})
		}
	}
	return img
}

// A face already where the template wants it is cut out unchanged, red,
// green and blue planes in that order.
func TestAlignedTensorIsTheFaceAligned(t *testing.T) {
	img := pattern(112, 112)
	tensor := AlignedTensor(img, template)
	if len(tensor) != 3*Size*Size {
		t.Fatalf("size %d", len(tensor))
	}
	for _, at := range [][2]int{{0, 0}, {50, 60}, {111, 111}} {
		x, y := at[0], at[1]
		i := y*Size + x
		r, g, b := tensor[i], tensor[Size*Size+i], tensor[2*Size*Size+i]
		if math.Abs(float64(r)-float64(x)) > 0.01 || math.Abs(float64(g)-float64(y)) > 0.01 || math.Abs(float64(b)-float64((x+y)/2)) > 0.6 {
			t.Fatalf("pixel %v: %v %v %v", at, r, g, b)
		}
	}
	// Twice the size, shifted: the same face comes out.
	big := image.NewRGBA(image.Rect(0, 0, 400, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 400; x++ {
			sx, sy := float64(x-100)/2, float64(y-80)/2
			if sx >= 0 && sy >= 0 && sx < 112 && sy < 112 {
				big.Set(x, y, img.At(int(sx), int(sy)))
			}
		}
	}
	var pts [5][2]float64
	for i, p := range template {
		pts[i] = [2]float64{p[0]*2 + 100, p[1]*2 + 80}
	}
	out := AlignedImage(big, pts)
	for _, at := range [][2]int{{20, 20}, {56, 70}, {90, 100}} {
		got, want := out.RGBAAt(at[0], at[1]), img.RGBAAt(at[0], at[1])
		if absDiff(got.R, want.R) > 2 || absDiff(got.G, want.G) > 2 {
			t.Fatalf("scaled face at %v: %v want %v", at, got, want)
		}
	}
	if p, ok := Points([][2]float64{{0.5, 0.5}, {0.6, 0.5}, {0.55, 0.6}, {0.5, 0.7}, {0.6, 0.7}}, 200, 100); !ok || p[0] != [2]float64{99.5, 49.5} || math.Abs(EyeDistance(p)-20) > 1e-9 {
		t.Fatalf("points: %v %v", p, ok)
	}
	if _, ok := Points(nil, 10, 10); ok {
		t.Fatal("no landmarks, no points")
	}
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// Embed feeds the aligned face to the model and answers its first 128 values,
// unit length (a stand-in model with the face model's names and shapes).
func TestEmbedRunsTheModel(t *testing.T) {
	lib := onnxtest.Library(t)
	e, err := Open(lib, onnxtest.FirstValues(t, t.TempDir(), Dim))
	if err != nil {
		t.Fatal(err)
	}
	img := pattern(112, 112)
	v, err := e.Embed(img, template)
	if err != nil {
		t.Fatal(err)
	}
	want := AlignedTensor(img, template)[:Dim]
	w := append([]float32(nil), want...)
	Normalize(w)
	if len(v) != Dim || Cosine(v, w) < 0.9999 {
		t.Fatalf("embedding is not the model's answer: cos %.5f", Cosine(v, w))
	}
	if _, err := Open("", ""); err == nil {
		t.Fatal("no library, no model: unavailable")
	}
}

// unitVec is a fingerprint mostly along axis a, with some of axis b.
func unitVec(a int, b int, mix float64) []float32 {
	v := make([]float32, Dim)
	v[a] = float32(math.Sqrt(1 - mix*mix))
	v[b] += float32(mix)
	Normalize(v)
	return v
}

// The voices' rules (D60), for faces: a clear, high match names the face; a
// middling one asks; a runner-up close behind asks; people said not to be it
// are left out; with one person known the bar is higher.
func TestDecide(t *testing.T) {
	th := Thresholds{Auto: 0.55, Margin: 0.15, FewAuto: 0.62, FewOthers: 2, Suggest: 0.38}
	ada := NewProfile("ada", "Ada", []Exemplar{{Source: "f1", Vec: unitVec(0, 10, 0.1), Weight: 1}, {Source: "f2", Vec: unitVec(0, 11, 0.2), Weight: 1}})
	bo := NewProfile("bo", "Bo", []Exemplar{{Source: "f3", Vec: unitVec(1, 12, 0.1), Weight: 1}})
	cy := NewProfile("cy", "Cy", []Exemplar{{Source: "f4", Vec: unitVec(2, 13, 0.1), Weight: 1}})
	people := []Profile{ada, bo, cy}
	if d := Decide(unitVec(0, 20, 0.5), people, nil, th); d.Auto == nil || d.Auto.PersonID != "ada" || d.Auto.Score < 0.8 {
		t.Fatalf("a close match is named: %+v", d)
	}
	if d := Decide(unitVec(0, 20, 0.9), people, nil, th); d.Auto != nil || len(d.Suggestions) != 1 || d.Suggestions[0].PersonID != "ada" {
		t.Fatalf("a middling match is asked about: %+v", d)
	}
	if d := Decide(unitVec(5, 20, 0.5), people, nil, th); d.Auto != nil || len(d.Suggestions) != 0 {
		t.Fatalf("a stranger is left alone: %+v", d)
	}
	// Halfway between Ada and Bo: no clear lead, so it is asked about.
	between := make([]float32, Dim)
	between[0], between[1] = 0.7, 0.7
	Normalize(between)
	if d := Decide(between, people, nil, th); d.Auto != nil || len(d.Suggestions) != 2 {
		t.Fatalf("no clear lead: %+v", d)
	}
	if d := Decide(unitVec(0, 20, 0.5), people, map[string]bool{"ada": true}, th); d.Auto != nil || len(d.Suggestions) != 0 {
		t.Fatalf("not Ada: %+v", d)
	}
	// One person known: a match that would do with several needs more.
	m := Score(unitVec(0, 20, 0.80), ada)
	if m.Score < 0.55 || m.Score >= 0.62 {
		t.Fatalf("test vector should score between Auto and FewAuto: %v", m.Score)
	}
	if d := Decide(unitVec(0, 20, 0.80), []Profile{ada}, nil, th); d.Auto != nil || len(d.Suggestions) != 1 {
		t.Fatalf("one person known, stricter: %+v", d)
	}
	if d := Decide(unitVec(0, 20, 0.80), people, nil, th); d.Auto == nil {
		t.Fatalf("several people known: %+v", d)
	}
	if th.AutoNeed(1) != 0.62 || th.AutoNeed(3) != 0.55 {
		t.Fatal("auto need")
	}
}

// The model is downloaded once, checked against its checksum, and refused
// when it doesn't match.
func TestSetupDownloadsAndChecks(t *testing.T) {
	body := []byte("not really a model, but bytes with a known checksum")
	sum := sha256.Sum256(body)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	s := Setup{Home: t.TempDir(), URL: srv.URL + "/model.onnx", SHA256: hex.EncodeToString(sum[:])}
	if s.Model() != "" {
		t.Fatal("nothing is there yet")
	}
	p, err := s.Ensure(context.Background())
	if err != nil || p != s.Path() {
		t.Fatalf("ensure: %q %v", p, err)
	}
	if b, _ := os.ReadFile(p); string(b) != string(body) {
		t.Fatal("contents")
	}
	if _, err := s.Ensure(context.Background()); err != nil || hits != 1 {
		t.Fatalf("downloaded again: %d %v", hits, err)
	}
	bad := Setup{Home: t.TempDir(), URL: srv.URL + "/model.onnx", SHA256: hex.EncodeToString(make([]byte, 32))}
	if _, err := bad.Ensure(context.Background()); err == nil {
		t.Fatal("a file that doesn't match its checksum must be refused")
	}
	if _, err := os.Stat(bad.Path()); err == nil {
		t.Fatal("and never kept")
	}
}
