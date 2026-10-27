package visual

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

func q(v float64) *float64 { return &v }

// frame builds a synthetic helper result.
func frame(labels []string, faces []vision.Box, text ...string) vision.Frame {
	f := vision.Frame{W: 1280, H: 720}
	for _, l := range labels {
		f.Labels = append(f.Labels, vision.Label{ID: l, Conf: 0.9, Precise: true})
	}
	for _, b := range faces {
		f.Faces = append(f.Faces, vision.Face{Conf: 0.99, Box: b, Quality: q(0.5)})
	}
	for _, t := range text {
		f.Text = append(f.Text, vision.Text{S: t, Conf: 1, Box: vision.Box{0.3, 0.4, 0.4, 0.2}})
	}
	return f
}

func synthetic() []Sample {
	var s []Sample
	for i := 0; i < 30; i++ {
		t := float64(i) + 0.5
		var f vision.Frame
		switch {
		case t < 6: // talking head, close-up
			f = frame([]string{"people", "interior_room"}, []vision.Box{{0.4, 0.2, 0.2, 0.45}})
		case t < 14: // beach, nobody
			f = frame([]string{"outdoor", "beach", "palm_tree"}, nil)
		case t < 21: // a person in a medium shot from 17 s
			if t >= 17 {
				f = frame([]string{"interior_room"}, []vision.Box{{0.55, 0.25, 0.08, 0.15}})
			} else {
				f = frame([]string{"interior_room"}, nil)
			}
		case t < 25: // a sign
			f = frame([]string{"sign"}, nil, "EXIT")
		default: // a dog, and a clock that keeps changing
			f = frame(nil, nil, "00:00:0"+string(rune('0'+i%10)))
			f.Animals = []vision.Detection{{ID: "dog", Conf: 0.8, Box: vision.Box{0.3, 0.1, 0.4, 0.8}}}
		}
		s = append(s, Sample{At: t, Frame: f})
	}
	return s
}

func TestBuildIndex(t *testing.T) {
	idx := Build(synthetic(), 1, 30, []float64{6, 14, 21, 25}, "proxy")
	has := func(label string, from, to float64) bool {
		for _, l := range idx.Labels {
			if l.Label == label && l.From <= from && l.To >= to {
				return true
			}
		}
		return false
	}
	if !has("beach", 6, 14) || !has("dog", 25, 30) || !has("person", 0, 6) || !has("palm tree", 6, 14) {
		t.Fatalf("labels: %+v", idx.Labels)
	}
	var exit, clock *TextSpan
	for i, x := range idx.Text {
		switch {
		case x.Text == "EXIT":
			exit = &idx.Text[i]
		case strings.HasPrefix(x.Text, "00:00"):
			clock = &idx.Text[i]
		}
	}
	if exit == nil || exit.From != 21 || exit.To != 25 || exit.Changing {
		t.Fatalf("EXIT 21–25: %+v", idx.Text)
	}
	if clock == nil || !clock.Changing {
		t.Fatalf("a changing clock is one span: %+v", idx.Text)
	}
	want := []struct {
		typ      string
		from, to float64
	}{{CloseUp, 0, 6}, {NoOne, 6, 14}, {NoOne, 14, 17}, {Medium, 17, 21}, {NoOne, 21, 30}}
	if len(idx.Shots) < len(want) {
		t.Fatalf("shots: %+v", idx.Shots)
	}
	for i, w := range want[:4] {
		if s := idx.Shots[i]; s.Type != w.typ || s.From != w.from || s.To != w.to {
			t.Fatalf("shot %d: want %+v, got %+v (all %+v)", i, w, s, idx.Shots)
		}
	}
	if len(idx.Tracks) != 2 || idx.Tracks[0].Samples != 6 {
		t.Fatalf("two faces followed (one per shot): %+v", idx.Tracks)
	}
	for _, w := range []string{"beach", "palm", "exit", "dog", "animal", "face", "outdoors", "indoors", "text"} {
		found := false
		for _, term := range idx.Terms {
			if term == w {
				found = true
			}
		}
		if !found {
			t.Fatalf("term %q missing: %v", w, idx.Terms)
		}
	}
	if !strings.Contains(idx.Summary, "text “EXIT”") || !strings.Contains(idx.Summary, "dog") {
		t.Fatalf("summary: %q", idx.Summary)
	}
}

// What a frame taken at a cut sees starts at the cut, not half a step before
// it, and nothing seen in one shot runs on into the next.
func TestSpansStopAtCuts(t *testing.T) {
	head := frame([]string{"people", "interior_room"}, []vision.Box{{0.4, 0.2, 0.2, 0.45}})
	beach := frame([]string{"outdoor", "beach"}, nil)
	samples := []Sample{{At: 4.5, Frame: head}, {At: 5.5, Frame: head}, {At: 6.0, Frame: beach}} // the extra frame at the cut
	for at := 6.5; at < 14; at++ {
		samples = append(samples, Sample{At: at, Frame: beach})
	}
	samples = append(samples, Sample{At: 14.5, Frame: frame([]string{"interior_room"}, nil)}, Sample{At: 15.5, Frame: frame([]string{"interior_room"}, nil)})
	idx := Build(samples, 1, 16, []float64{6, 14}, "proxy")
	span := func(label string, from float64) *LabelSpan {
		for i, l := range idx.Labels {
			if l.Label == label && l.From == from {
				return &idx.Labels[i]
			}
		}
		return nil
	}
	if b := span("beach", 6); b == nil || b.To != 14 {
		t.Fatalf("the beach is seen from the cut at 6 to the cut at 14: %+v", idx.Labels)
	}
	if r := span("interior room", 4); r == nil || r.To != 6 {
		t.Fatalf("the room before the cut stops at it: %+v", idx.Labels)
	}
	if r := span("interior room", 14); r == nil || r.To != 16 {
		t.Fatalf("the room after the second cut starts at it: %+v", idx.Labels)
	}
	if len(idx.Tracks) != 1 || idx.Tracks[0].From != 4 || idx.Tracks[0].To != 6 {
		t.Fatalf("the face stops at the cut: %+v", idx.Tracks)
	}
	if s := idx.Shots; len(s) < 2 || s[0].Type != CloseUp || s[0].To != 6 || s[1].Type != NoOne || s[1].From != 6 {
		t.Fatalf("shots meet at the cut: %+v", s)
	}
}

func TestReadout(t *testing.T) {
	idx := Build(synthetic(), 1, 30, []float64{6, 14, 21, 25}, "proxy")
	names := func(track int) (string, bool) {
		if track == 1 {
			return "Theo", true
		}
		return "Sam", false
	}
	lines := Readout(idx, []float64{6, 14, 21, 25}, 0, 30, names)
	var got []string
	for _, l := range lines {
		got = append(got, l.Line)
	}
	want := []string{
		"0:00–0:06 close-up · 1 face (Theo) · indoors · no text",
		"0:06–0:14 no people · outdoors, beach · palm tree · no text",
		"0:14–0:17 no people · indoors · no text",
		"0:17–0:21 medium shot · 1 face (maybe Sam) · indoors · no text",
		"0:21–0:25 no people · sign · text “EXIT”",
	}
	for i, w := range want {
		if i >= len(got) || got[i] != w {
			t.Fatalf("line %d:\nwant %s\ngot  %v", i, w, strings.Join(got, "\n     "))
		}
	}
	if !strings.Contains(got[len(got)-1], "dog") {
		t.Fatalf("the dog: %v", got)
	}
	// A range.
	part := Readout(idx, []float64{6, 14, 21, 25}, 7, 9, nil)
	if len(part) != 1 || part[0].Line != "0:07–0:09 no people · outdoors, beach · palm tree · no text" {
		t.Fatalf("range: %+v", part)
	}
}

func TestFaceFocusAndFind(t *testing.T) {
	idx := Build(synthetic(), 1, 30, []float64{6, 14, 21, 25}, "proxy")
	x, y, n := FaceFocus(idx, 0, 6)
	if n != 6 || x != 0.5 || y != 0.425 {
		t.Fatalf("focus on the face: %v %v %d", x, y, n)
	}
	if _, _, n := FaceFocus(idx, 6, 14); n != 0 {
		t.Fatalf("no faces on the beach")
	}
	hits := Find(idx, []string{"exit"})
	if len(hits) != 1 || hits[0].What != "text" || hits[0].From != 21 {
		t.Fatalf("exit: %+v", hits)
	}
	if hits := Find(idx, []string{"exit", "sign"}); len(hits) != 1 || hits[0].From != 21 {
		t.Fatalf("EXIT sign: %+v", hits)
	}
	if hits := Find(idx, []string{"dog"}); len(hits) != 1 || hits[0].What != "label" {
		t.Fatalf("dog: %+v", hits)
	}
	if hits := Find(idx, []string{"giraffe"}); len(hits) != 0 {
		t.Fatalf("giraffe: %+v", hits)
	}
}

func TestSampleTimes(t *testing.T) {
	r, m := SampleTimes(5, 1, []float64{2.5, 3.1, 3.2, 4.9, 9})
	if len(r) != 5 || r[0] != 0.5 || r[4] != 4.5 {
		t.Fatalf("regular: %v", r)
	}
	if len(m) != 2 || m[0] != 3.1 || m[1] != 4.9 {
		t.Fatalf("extra frames not near a regular one, not too close to each other: %v", m)
	}
	if StepFor(1800) != 1 || StepFor(3*3600) != 4 {
		t.Fatalf("steps: %v %v", StepFor(1800), StepFor(3*3600))
	}
}

var (
	helperOnce sync.Once
	helperTS   *tools.Set
	helperErr  error
	// helperDir is where the helper was built for this run (TestMain removes it).
	helperDir string
)

// helper builds metarush-vision once for the tests (macOS with swiftc), or uses METARUSH_VISION.
func helper(t *testing.T) *tools.Set {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("Apple Vision is macOS only")
	}
	helperOnce.Do(func() {
		cfg := config.Tools{Vision: os.Getenv("METARUSH_VISION")}
		helperTS = tools.New(cfg)
		if cfg.Vision != "" {
			return
		}
		dir, err := os.MkdirTemp("", "metarush-vision-test-")
		if err != nil {
			helperErr = err
			return
		}
		helperDir = dir
		_, helperErr = vision.Build(context.Background(), helperTS, dir)
	})
	if helperErr != nil {
		if !helperTS.Has(tools.Swiftc) {
			t.Skip("swiftc not installed")
		}
		t.Fatal(helperErr)
	}
	return helperTS
}

// The measured acceptance for the on-screen index on visual-a: the EXIT sign is
// read, the dog and the beach are recognized, the talking head is a close-up
// and the person who walks in is a medium shot.
func TestIndexVisualFixture(t *testing.T) {
	ts := helper(t)
	src := filepath.Join("..", "..", "tests", "fixtures", "gen", "visual-a.mp4")
	if _, err := os.Stat(src); err != nil {
		t.Skip("visual-a.mp4 not generated (scripts/make-fixtures.sh)")
	}
	idx, err := Run(context.Background(), ts, src, 30, []float64{6.4, 9.7, 13.3, 14.4, 17.3, 21.4, 25.4}, []float64{6, 14, 21, 25}, "original", t.TempDir(), nil, RunOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range Readout(idx, []float64{6, 14, 21, 25}, 0, 30, nil) {
		t.Log(l.Line)
	}
	t.Logf("summary: %s", idx.Summary)
	t.Logf("terms: %v", idx.Terms)
	findIn := func(words []string, from, to float64) bool {
		for _, h := range Find(idx, words) {
			if h.To > from && h.From < to {
				return true
			}
		}
		return false
	}
	if !findIn([]string{"exit"}, 21, 25) {
		t.Errorf("EXIT not read in 21–25: %+v", idx.Text)
	}
	if !findIn([]string{"dog"}, 25, 30) {
		t.Errorf("dog not found in 25–30")
	}
	if !findIn([]string{"beach"}, 6, 14) {
		t.Errorf("beach not found in 6–14")
	}
	shotAt := func(at float64) string {
		for _, s := range idx.Shots {
			if at >= s.From && at < s.To {
				return s.Type
			}
		}
		return ""
	}
	if s := shotAt(3); s != CloseUp {
		t.Errorf("talking head at 3 s: %q", s)
	}
	if s := shotAt(19); s != Medium {
		t.Errorf("the person at 19 s: %q", s)
	}
	if s := shotAt(10); s != NoOne {
		t.Errorf("the beach at 10 s: %q", s)
	}
	if x, _, n := FaceFocus(idx, 18, 21); n == 0 || x < 0.6 || x > 0.8 {
		t.Errorf("focus on the person who walked in (right of center): x=%v from %d frames", x, n)
	}
}
