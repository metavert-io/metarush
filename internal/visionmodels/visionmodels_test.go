package visionmodels

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/vision"
)

// YuNet's answer is read as OpenCV reads it (D406): a cell's score is
// √(class × object), its box sits around the cell offset by the answer and
// sized by exp() of it, its points are offsets from the cell, all in strides;
// cells under the score are dropped.
func TestDecodeYuNet(t *testing.T) {
	const W, H = 64, 64
	outs := map[string][]float32{}
	for _, s := range []int{8, 16, 32} {
		n := (W / s) * (H / s)
		outs[fmt.Sprintf("cls_%d", s)] = make([]float32, n)
		outs[fmt.Sprintf("obj_%d", s)] = make([]float32, n)
		outs[fmt.Sprintf("bbox_%d", s)] = make([]float32, 4*n)
		outs[fmt.Sprintf("kps_%d", s)] = make([]float32, 10*n)
	}
	// One face at stride 8, row 3, column 4.
	i := 3*8 + 4
	outs["cls_8"][i], outs["obj_8"][i] = 0.81, 1
	copy(outs["bbox_8"][4*i:], []float32{0.5, 0.5, float32(math.Log(2)), float32(math.Log(3))})
	copy(outs["kps_8"][10*i:], []float32{-0.5, 0, 0.5, 0, 0, 0.5, -0.4, 1, 0.4, 1})
	// A cell under the score (√(0.3 × 0.3) = 0.3) at stride 16.
	outs["cls_16"][1], outs["obj_16"][1] = 0.3, 0.3
	got, err := decodeYuNet(func(n string) []float32 { return outs[n] }, W, H)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("one face: %+v", got)
	}
	f := got[0]
	if math.Abs(f.score-0.9) > 1e-6 {
		t.Errorf("score %v (√(0.81 × 1))", f.score)
	}
	// Centre (4.5, 3.5) strides, 2 × 3 strides.
	if want := [4]float64{36 - 8, 28 - 12, 16, 24}; !close4(f.box, want) {
		t.Errorf("box %v, want %v", f.box, want)
	}
	if want := [2]float64{(4 - 0.5) * 8, 3 * 8}; f.pts[0] != want {
		t.Errorf("left eye %v, want %v", f.pts[0], want)
	}
	// A short answer is refused, not read past.
	outs["kps_32"] = nil
	if _, err := decodeYuNet(func(n string) []float32 { return outs[n] }, W, H); err == nil {
		t.Error("a short answer was read")
	}
}

func close4(a, b [4]float64) bool {
	for i := range a {
		if math.Abs(a[i]-b[i]) > 1e-6 {
			return false
		}
	}
	return true
}

// Faces found twice (two cells, two sizes) are one face: the surest.
func TestNMSKeepsTheSurest(t *testing.T) {
	fs := []faceAt{{box: [4]float64{10, 10, 40, 40}, score: 0.7}, {box: [4]float64{12, 11, 40, 40}, score: 0.9}, {box: [4]float64{100, 10, 40, 40}, score: 0.8}}
	got := nms(fs, faceNMS, 10)
	if len(got) != 2 || got[0].score != 0.9 || got[1].score != 0.8 {
		t.Fatalf("%+v", got)
	}
	if got := nms(fs, faceNMS, 1); len(got) != 1 {
		t.Fatalf("at most one: %+v", got)
	}
}

// The five points are put in the helper's order whatever way YuNet gives
// them: the eye on the picture's left first, then the mouth corners along
// the eyes' line.
func TestPointsInTheHelpersOrder(t *testing.T) {
	p := [5][2]float64{{60, 40}, {40, 40}, {50, 50}, {58, 60}, {42, 60}}
	got := ordered(p)
	want := [5][2]float64{{40, 40}, {60, 40}, {50, 50}, {42, 60}, {58, 60}}
	if got != want {
		t.Fatalf("%v, want %v", got, want)
	}
	// A face upside down: the mouth corners follow the eyes' direction.
	up := ordered([5][2]float64{{40, 60}, {60, 60}, {50, 50}, {58, 40}, {42, 40}})
	if up[3] != [2]float64{42, 40} || up[4] != [2]float64{58, 40} {
		t.Fatalf("upside down: %v", up)
	}
}

// A face is told in the helper's terms (D407): Apple Vision's square box
// from YuNet's taller one, the points 0–1 with pixel centres on whole numbers
// (faceid.Points takes them back to the same pixels), its roll from the eyes.
func TestFaceInTheHelpersTerms(t *testing.T) {
	fa := faceAt{box: [4]float64{100, 50, 80, 100}, pts: [5][2]float64{{120, 90}, {160, 90}, {140, 110}, {125, 130}, {155, 130}}, score: 0.93}
	f := toFace(fa, 400, 300, 0.6)
	side := appleSide * 100
	if math.Abs(f.Box[2]*400-side) > 0.5 || math.Abs(f.Box[3]*300-side) > 0.5 {
		t.Errorf("not a square of %.1f pixels: %v", side, f.Box)
	}
	if cy := (f.Box[1] + f.Box[3]/2) * 300; math.Abs(cy-(100+appleCenter*100)) > 0.5 {
		t.Errorf("middle at %.1f, want %.1f", cy, 100+appleCenter*100)
	}
	if f.Conf != 0.93 || f.Quality == nil || *f.Quality != 0.6 || len(f.LM) != 5 || f.Roll == nil || *f.Roll != 0 {
		t.Fatalf("%+v", f)
	}
	if px := f.LM[0][0]*400 - 0.5; math.Abs(px-120) > 0.03 {
		t.Errorf("the left eye comes back at %.2f, not 120", px)
	}
}

// How usable a face is: a sharp, frontal, large face scores more than the
// same face blurred, turned or small, and the score stays inside 0–1.
func TestFaceQuality(t *testing.T) {
	face := func(size float64, blur bool, turn float64) (*picture, faceAt) {
		w, h := 400, 400
		p := &picture{w: w, h: h, px: make([]float32, 3*w*h)}
		cx, cy := 200.0, 200.0
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := float32(60)
				dx, dy := (float64(x)-cx)/size, (float64(y)-cy)/size
				if dx*dx+dy*dy < 1 {
					v = 200
					// Texture: stripes a face has (brows, eyes, mouth).
					if !blur && (int(float64(y)/size*8)%2 == 0) {
						v = 120
					}
				}
				i := 3 * (y*w + x)
				p.px[i], p.px[i+1], p.px[i+2] = v, v, v
			}
		}
		e := size * 0.6
		pts := [5][2]float64{{cx - e/2, cy - e/3}, {cx + e/2, cy - e/3}, {cx + turn*e, cy}, {cx - e/3, cy + e/2}, {cx + e/3, cy + e/2}}
		return p, faceAt{box: [4]float64{cx - size, cy - size, 2 * size, 2 * size}, pts: pts, score: 0.95}
	}
	sharp := faceQuality(face(80, false, 0))
	blurred := faceQuality(face(80, true, 0))
	turned := faceQuality(face(80, false, 0.45))
	small := faceQuality(face(16, false, 0))
	if !(sharp > blurred && sharp > turned && sharp > small) {
		t.Fatalf("sharp %.3f, blurred %.3f, turned %.3f, small %.3f", sharp, blurred, turned, small)
	}
	for _, q := range []float64{sharp, blurred, turned, small} {
		if q < 0 || q > 1 || math.IsNaN(q) {
			t.Fatalf("out of 0–1: %v", q)
		}
	}
	// Two points on top of each other: no face to score.
	if q := faceQuality(&picture{w: 10, h: 10, px: make([]float32, 300)}, faceAt{score: 1}); q != 0 {
		t.Fatalf("no eyes: %v", q)
	}
}

// CTC, greedily: the likeliest character at each step, repeats joined,
// blanks dropped (a blank between two of the same keeps both), scored by
// the characters kept.
func TestCTCGreedy(t *testing.T) {
	chars := []string{"", "A", "B", " "}
	steps := [][]float32{
		{0.1, 0.8, 0.05, 0.05},   // A
		{0.1, 0.7, 0.1, 0.1},     // A again: joined
		{0.9, 0.05, 0.03, 0.02},  // blank
		{0.1, 0.6, 0.2, 0.1},     // A: a new one after the blank
		{0.1, 0.1, 0.1, 0.7},     // space
		{0.05, 0.05, 0.85, 0.05}, // B
	}
	var flat []float32
	for _, s := range steps {
		flat = append(flat, s...)
	}
	got, score, err := ctcGreedy(flat, len(steps), 4, chars)
	if err != nil || got != "AA B" {
		t.Fatalf("%q %v", got, err)
	}
	if want := (0.8 + 0.6 + 0.7 + 0.85) / 4; math.Abs(score-want) > 1e-6 {
		t.Fatalf("score %v, want %v", score, want)
	}
	if got, score, _ := ctcGreedy([]float32{1, 0, 0, 0}, 1, 4, chars); got != "" || score != 0 {
		t.Fatalf("only blanks: %q %v", got, score)
	}
}

// The detector's map becomes upright lines: each region over the threshold,
// scored by the map's mean over its box, grown back by DB's unclip, in the
// picture's pixels, top to bottom; faint and tiny regions are dropped.
func TestLinesFromTheMap(t *testing.T) {
	const W, H = 100, 60
	prob := make([]float32, W*H)
	fill := func(x0, y0, x1, y1 int, v float32) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				prob[y*W+x] = v
			}
		}
	}
	fill(10, 40, 60, 46, 0.9)  // a line lower down
	fill(20, 10, 80, 18, 0.95) // a line above it
	fill(85, 30, 95, 34, 0.4)  // over the threshold, under the box's (0.6)
	fill(5, 5, 7, 7, 0.9)      // a speck
	tf := &textFinder{thresh: 0.3, boxThresh: 0.6, unclip: 1.5}
	lines := tf.linesOf(prob, W, H, 2, 2, 200, 120)
	if len(lines) != 2 {
		t.Fatalf("two lines: %+v", lines)
	}
	top := lines[0]
	// 60 × 8 pixels of the map, grown by 60·8·1.5 / (2·68) ≈ 3.53 on every side, then doubled.
	d := 60.0 * 8 * 1.5 / (2 * 68)
	if math.Abs(top.x0-(20-d)*2) > 1e-6 || math.Abs(top.y0-(10-d)*2) > 1e-6 || math.Abs(top.x1-(80+d)*2) > 1e-6 || math.Abs(top.y1-(18+d)*2) > 1e-6 {
		t.Fatalf("top line %+v", top)
	}
	if lines[1].y0 <= top.y0 {
		t.Fatal("top to bottom")
	}
}

// PaddleOCR's score is put on the helper's scale (D407): clean lines read
// as sure as Apple Vision says them, guessed characters fall under the bar
// a name shown needs, and two characters are less certain than they score.
func TestCalibratedText(t *testing.T) {
	for _, c := range []struct {
		score float64
		s     string
		want  float64
	}{{0.99, "Ada Park", 1}, {0.95, "EXIT", 1}, {0.75, "SHLE ERS", 0.5}, {0.5, "ZQ7", 0}, {0.99, "OK", 0.8}} {
		if got := calibratedText(c.score, c.s); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%v %q: %v, want %v", c.score, c.s, got, c.want)
		}
	}
}

// Where the eye is drawn, from a heat map: its weighted middle, its peak, and
// its hottest regions, biggest first.
func TestAttentionFromAHeatMap(t *testing.T) {
	const w, h = 40, 20
	heat := make([]float32, w*h)
	for y := 5; y < 15; y++ {
		for x := 24; x < 34; x++ {
			heat[y*w+x] = 0.9
		}
	}
	heat[2*w+2] = 0.95 // a speck: hot, but not a region
	a := attentionOf(heat, w, h)
	if math.Abs(a.At[0]-0.725) > 0.02 || math.Abs(a.At[1]-0.5) > 0.03 || a.Peak != 0.95 {
		t.Fatalf("%+v", a)
	}
	if len(a.Objects) != 1 || a.Objects[0].Box != (vision.Box{0.6, 0.25, 0.25, 0.5}) || a.Objects[0].Conf != 0.9 {
		t.Fatalf("objects %+v", a.Objects)
	}
	cold := attentionOf(make([]float32, w*h), w, h)
	if cold.Peak != 0 || len(cold.Objects) != 0 {
		t.Fatalf("nothing stands out: %+v", cold)
	}
}

// Where faces are seen, they are where the eye is drawn: the biggest
// weighs most, as strong as the surest face.
func TestAttentionFromFaces(t *testing.T) {
	a := faceAttention([]vision.Face{{Conf: 0.9, Box: vision.Box{0.1, 0.1, 0.1, 0.1}}, {Conf: 0.95, Box: vision.Box{0.6, 0.4, 0.3, 0.4}}})
	if a.At[0] < 0.7 || a.At[0] > 0.76 || a.Peak != 0.95 || len(a.Objects) != 2 || a.Objects[0].Box[0] != 0.6 {
		t.Fatalf("%+v", a)
	}
}

// Shrinking averages every pixel it covers; growing is bilinear with the
// pixels' middles lined up; a flat picture stays flat either way.
func TestResize(t *testing.T) {
	p := &picture{w: 4, h: 2, px: []float32{
		0, 0, 0, 100, 100, 100, 50, 50, 50, 50, 50, 50,
		0, 0, 0, 100, 100, 100, 50, 50, 50, 50, 50, 50}}
	half := p.resized(2, 1)
	if half.px[0] != 50 || half.px[3] != 50 {
		t.Fatalf("shrunk %v", half.px)
	}
	flat := &picture{w: 3, h: 3, px: make([]float32, 27)}
	for i := range flat.px {
		flat.px[i] = 77
	}
	for _, size := range [][2]int{{7, 5}, {2, 2}, {9, 1}} {
		q := flat.resized(size[0], size[1])
		for _, v := range q.px {
			if math.Abs(float64(v)-77) > 1e-3 {
				t.Fatalf("%v: %v", size, q.px)
			}
		}
	}
	grow := (&picture{w: 2, h: 1, px: []float32{0, 0, 0, 100, 100, 100}}).resized(4, 1)
	if grow.px[0] != 0 || grow.px[3] != 25 || grow.px[6] != 75 || grow.px[9] != 100 {
		t.Fatalf("grown %v", grow.px)
	}
	if c := p.crop(1, 0, 3, 2); c.w != 2 || c.h != 2 || c.px[0] != 100 {
		t.Fatalf("crop %+v", c)
	}
	if c := p.crop(5, 5, 9, 9); c.w != 0 {
		t.Fatalf("outside: %+v", c)
	}
	turned := (&picture{w: 2, h: 1, px: []float32{1, 1, 1, 2, 2, 2}}).turned()
	if turned.w != 1 || turned.h != 2 || turned.px[0] != 2 || turned.px[3] != 1 {
		t.Fatalf("turned %+v", turned)
	}
}

// The models read pixels in their own order and scale.
func TestTensorLayout(t *testing.T) {
	p := &picture{w: 2, h: 1, px: []float32{255, 0, 51, 0, 255, 0}}
	raw := p.tensor(rawBGR, 3, 2)
	// Blue first: pixel 0's blue is 51.
	if len(raw) != 18 || raw[0] != 51 || raw[1] != 0 || raw[2] != 0 || raw[6+0] != 0 || raw[6+1] != 255 || raw[12] != 255 {
		t.Fatalf("raw %v", raw)
	}
	rec := p.tensor(paddleRec, 2, 1)
	if math.Abs(float64(rec[0])-(51.0/255-0.5)/0.5) > 1e-5 || math.Abs(float64(rec[4])-1) > 1e-5 {
		t.Fatalf("rec %v", rec)
	}
}

// The recognizer's characters are read from its settings file: CTC's blank
// first, a space last.
func TestReadDict(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.yml")
	os.WriteFile(good, []byte("PostProcess:\n  name: CTCLabelDecode\n  character_dict:\n  - a\n  - b\n"), 0o644)
	chars, err := readDict(good)
	if err != nil || strings.Join(chars, "|") != "|a|b| " {
		t.Fatalf("%q %v", chars, err)
	}
	bad := filepath.Join(dir, "bad.yml")
	os.WriteFile(bad, []byte("PostProcess:\n  name: DBPostProcess\n"), 0o644)
	if _, err := readDict(bad); err == nil {
		t.Fatal("settings without characters")
	}
}

// A model file is used only when it is the pinned file: its SHA-256 is
// checked (once per file and change), a changed or missing one says so.
func TestStoreUsesOnlyPinnedFiles(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) File {
		os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644)
		sum := sha256.Sum256([]byte(body))
		return File{ID: name, Name: name, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body)), Part: PartIndex}
	}
	a, b := write("a.onnx", "model a"), write("b.onnx", "model b")
	s := Store{Dir: dir, Files: []File{a, b, {ID: "c", Name: "c.onnx", SHA256: strings.Repeat("0", 64), Part: PartCutOut}}}
	if err := s.Ready(PartIndex); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(PartCutOut); err == nil || !strings.Contains(err.Error(), "isn't here") || s.Present(PartCutOut) {
		t.Fatalf("missing: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "b.onnx"), []byte("model B, changed"), 0o644)
	if err := s.Ready(PartIndex); err == nil || !strings.Contains(err.Error(), "checksum differs") {
		t.Fatalf("changed: %v", err)
	}
	if s.Bytes(PartIndex) != int64(len("model a")+len("model b")) {
		t.Fatalf("bytes %d", s.Bytes(PartIndex))
	}
}

// Every pinned model is a checked download with its licence said (D406):
// the publisher's HTTPS address, a SHA-256, a size, a licence and where the
// repository says it — and none is GPL or for non-commercial use only.
func TestPinnedModels(t *testing.T) {
	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	seen := map[string]bool{}
	parts := map[string]int{}
	for _, f := range Files {
		if !strings.HasPrefix(f.URL, "https://") || !hex64.MatchString(f.SHA256) || f.Bytes <= 0 || f.Name == "" || f.Title == "" || f.What == "" ||
			!strings.HasPrefix(f.LicenceURL, "https://") {
			t.Errorf("%s isn't a checked download: %+v", f.ID, f)
		}
		l := strings.ToLower(f.Licence)
		if !(strings.Contains(l, "mit") || strings.Contains(l, "apache-2.0")) || strings.Contains(l, "gpl") || strings.Contains(l, "non-commercial") || strings.Contains(l, "noncommercial") {
			t.Errorf("%s: licence %q", f.ID, f.Licence)
		}
		if seen[f.ID] || seen[f.Name] {
			t.Errorf("%s twice", f.ID)
		}
		seen[f.ID], seen[f.Name] = true, true
		parts[f.Part]++
	}
	if parts[PartIndex] != 5 || parts[PartCutOut] != 1 || len(parts) != 2 {
		t.Fatalf("parts %v", parts)
	}
	p := StoreIn("/models").IndexPaths()
	if p.Faces == "" || p.TextDet == "" || p.TextRec == "" || p.TextDict == "" || p.Attention == "" || filepath.Dir(p.Faces) != filepath.Join("/models", Folder) {
		t.Fatalf("paths %+v", p)
	}
}

// A mask holds a subject only when enough of it is sure.
func TestFound(t *testing.T) {
	m := image.NewGray(image.Rect(0, 0, 100, 100))
	if Found(m) {
		t.Fatal("an empty mask")
	}
	for i := 0; i < 30; i++ {
		m.SetGray(i, 0, color.Gray{Y: 200})
	}
	if !Found(m) {
		t.Fatal("30 sure pixels in 10,000 are a subject (more than 0.2%)")
	}
}
