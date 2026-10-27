package visual

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/onnx/onnxtest"
)

// faceModel is the face model for tests: METARUSH_FACE_MODEL, else a copy kept
// in the temp folder by an earlier run; tests that need it skip without it
// (it is a 39 MB download that tests never make by themselves).
func faceModel(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("METARUSH_FACE_MODEL"); p != "" {
		return p
	}
	p := filepath.Join(os.TempDir(), "metarush-faceid-test-cache", faceid.ModelFile)
	if (faceid.Setup{Home: filepath.Dir(filepath.Dir(filepath.Dir(p)))}).Model() != "" {
		return p
	}
	if _, err := os.Stat(p); err == nil {
		return p
	}
	t.Skip("the face model isn't here (set METARUSH_FACE_MODEL to " + faceid.ModelFile + ")")
	return ""
}

// The fixtures measured end to end (D114, D115): visual-a's talking head and
// the person who walks in are recognized in visual-b, another recording made
// from other photos of them; the stranger in visual-b looks like neither; the
// face that walks across visual-b moves, and the cuts are kept.
func TestRecognizesTheFixtureFaces(t *testing.T) {
	ts := helper(t)
	lib := onnxtest.Library(t)
	model := faceModel(t)
	a := filepath.Join("..", "..", "tests", "fixtures", "gen", "visual-a.mp4")
	b := filepath.Join("..", "..", "tests", "fixtures", "gen", "visual-b.mp4")
	for _, p := range []string{a, b} {
		if _, err := os.Stat(p); err != nil {
			t.Skip("fixtures not generated (scripts/make-fixtures.sh)")
		}
	}
	e, err := faceid.Open(lib, model)
	if err != nil {
		t.Fatal(err)
	}
	ia, err := Run(context.Background(), ts, a, 30, []float64{6.4, 9.7, 13.3, 14.4, 17.3, 21.4, 25.4}, []float64{6, 14, 21, 25}, "original", t.TempDir(), nil, RunOptions{Prints: e})
	if err != nil {
		t.Fatal(err)
	}
	ib, err := Run(context.Background(), ts, b, 24, nil, []float64{8, 14, 20}, "original", t.TempDir(), nil, RunOptions{Prints: e})
	if err != nil {
		t.Fatal(err)
	}
	vec := func(tr Track) []float32 {
		var vs [][]float32
		var ws []float64
		for _, p := range tr.Prints {
			vs, ws = append(vs, p.Vec), append(ws, p.Quality)
		}
		return faceid.Mean(vs, ws)
	}
	trackAt := func(idx *Index, at float64) *Track {
		for i := range idx.Tracks {
			if idx.Tracks[i].From <= at && at < idx.Tracks[i].To && len(idx.Tracks[i].Prints) > 0 {
				return &idx.Tracks[i]
			}
		}
		return nil
	}
	head, walkIn := trackAt(ia, 3), trackAt(ia, 19)
	walking, edge, low, stranger := trackAt(ib, 4), trackAt(ib, 11), trackAt(ib, 17), trackAt(ib, 22)
	for name, tr := range map[string]*Track{"talking head": head, "person walking in": walkIn, "walking": walking, "edge": edge, "low": low, "stranger": stranger} {
		if tr == nil {
			t.Fatalf("no fingerprinted face for %s: a %+v / b %+v", name, ia.Tracks, ib.Tracks)
		}
	}
	same := map[string]float64{
		"talking head ~ walking": faceid.Cosine(vec(*head), vec(*walking)),
		"talking head ~ low":     faceid.Cosine(vec(*head), vec(*low)),
		"walking in ~ edge":      faceid.Cosine(vec(*walkIn), vec(*edge)),
	}
	diff := map[string]float64{
		"talking head ~ stranger": faceid.Cosine(vec(*head), vec(*stranger)),
		"walking in ~ stranger":   faceid.Cosine(vec(*walkIn), vec(*stranger)),
		"talking head ~ edge":     faceid.Cosine(vec(*head), vec(*edge)),
		"walking in ~ walking":    faceid.Cosine(vec(*walkIn), vec(*walking)),
	}
	t.Logf("same people: %v", same)
	t.Logf("different people: %v", diff)
	th := faceid.DefaultThresholds
	for k, v := range same {
		if v < th.FewAuto {
			t.Errorf("%s: %.3f, below the bar for naming automatically with one person known (%.2f)", k, v, th.FewAuto)
		}
	}
	for k, v := range diff {
		if v >= th.Suggest {
			t.Errorf("%s: %.3f, would be asked about (%.2f)", k, v, th.Suggest)
		}
	}
	// Following: the walking face moves left to right, and the index keeps the cuts.
	s, cuts, ok := Subject(ib, 0, 8)
	if !ok || len(cuts) != 0 || Spread(s) < 0.4 || s[0].X > s[len(s)-1].X {
		t.Fatalf("the walk: %v %v spread %.2f %+v", ok, cuts, Spread(s), s)
	}
	if len(ib.Cuts) != 3 || len(ib.Attention) == 0 {
		t.Fatalf("cuts %v, attention %d", ib.Cuts, len(ib.Attention))
	}
	found := false
	for _, x := range ib.Text {
		found = found || x.Text == "SALE ENDS SUNDAY"
	}
	if !found {
		t.Errorf("the sign is read: %+v", ib.Text)
	}
}
