package editspec

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// moverFacts: astA's subject walks from the left (x 0.2) to the right (x 0.8)
// over 0–8 s, then the footage cuts at 8 s to a shot with the subject at the
// left (x 0.25), still; a face is seen in every frame looked at (1 a second).
type moverFacts struct{ fakeFacts }

func moverAt(t float64) float64 {
	if t < 8 {
		return 0.2 + 0.6*t/8
	}
	return 0.25
}

func (f moverFacts) FaceFocus(id string, from, to float64) (float64, float64, bool) {
	if id != astA {
		return 0, 0, false
	}
	return moverAt((from + to) / 2), 0.4, true
}

func (f moverFacts) Subject(id string, from, to float64) ([]SubjectSample, []float64, bool) {
	if id != astA {
		return nil, nil, false
	}
	var out []SubjectSample
	for t := 0.5; t < 30; t++ {
		if t >= from && t < to {
			out = append(out, SubjectSample{At: t, X: moverAt(t), Y: 0.4, Face: true})
		}
	}
	var cuts []float64
	if from < 8 && to > 8 {
		cuts = []float64{8}
	}
	return out, cuts, len(out) > 0
}

func followSpec(framing string) *Spec {
	s := &Spec{SchemaVersion: 1, Output: Output{Preset: "vertical-1080p", FPS: 30, Framing: framing}, Clips: []Clip{{ID: "walk", AssetID: astA, In: 0, Out: 12}}}
	Normalize(s)
	return s
}

// Following movement is only what the spec says (D115): faces holds one point,
// follow moves the crop with the subject, and a cut that says neither keeps
// the middle whatever the index knows.
func TestFollowOnlyWhereTheSpecSaysSo(t *testing.T) {
	mf := moverFacts{facts()}
	if fr := plan(t, followSpec(FramingFaces), mf).Clips[0].Framing; fr.From != FramingFaces || len(fr.Path) != 0 {
		t.Fatalf("faces: one point: %+v", fr)
	}
	fr := plan(t, followSpec(FramingFollow), mf).Clips[0].Framing
	if fr.From != FramingFollow || len(fr.Path) < 5 || fr.FocusX != fr.Path[0].X {
		t.Fatalf("follow: a path: %+v", fr)
	}
	for _, f := range []string{"", FramingCenter} {
		before, _ := json.Marshal(plan(t, followSpec(f), facts()))
		after, _ := json.Marshal(plan(t, followSpec(f), mf))
		if string(before) != string(after) {
			t.Fatalf("%q: the index changed a cut that doesn't ask to follow:\n%s\n%s", f, before, after)
		}
	}
	// A clip can ask on its own, in any cut; a focus of its own wins over the cut's follow.
	own := followSpec("")
	own.Clips[0].Framing = &Framing{Auto: FramingFollow}
	if fr := plan(t, own, mf).Clips[0].Framing; fr.From != FramingFollow {
		t.Fatalf("framing.auto follow: %+v", fr)
	}
	x := 0.3
	fixed := followSpec(FramingFollow)
	fixed.Clips[0].Framing = &Framing{FocusX: &x}
	if fr := plan(t, fixed, mf).Clips[0].Framing; fr.From != "" || len(fr.Path) != 0 || fr.FocusX != 0.3 {
		t.Fatalf("its own focus: %+v", fr)
	}
	faces := followSpec(FramingFollow)
	faces.Clips[0].Framing = &Framing{Auto: FramingFaces}
	if fr := plan(t, faces, mf).Clips[0].Framing; fr.From != FramingFaces || len(fr.Path) != 0 {
		t.Fatalf("a clip that asks for faces holds still: %+v", fr)
	}
	// Shown whole: nothing to follow.
	fit := followSpec(FramingFollow)
	fit.Clips[0].Framing = &Framing{Mode: "fit"}
	if fr := plan(t, fit, mf).Clips[0].Framing; len(fr.Path) != 0 {
		t.Fatalf("fit: %+v", fr)
	}
	same := followSpec(FramingFollow)
	same.Output = Output{Preset: "landscape-1080p", FPS: 30, Framing: FramingFollow}
	Normalize(same)
	if fr := plan(t, same, mf).Clips[0].Framing; len(fr.Path) != 0 {
		t.Fatalf("not cropped: %+v", fr)
	}
}

// The crop keeps the subject in view as it walks, moves no faster than it
// may, and jumps only at the footage's own cut, exactly there.
func TestFollowPathKeepsTheSubjectAndJumpsOnlyAtCuts(t *testing.T) {
	p := plan(t, followSpec(FramingFollow), moverFacts{facts()})
	c := p.Clips[0]
	path := c.Framing.Path
	vw, _ := visibleShare(1920, 1080, 1080, 1920, 1)
	cutFrame := 8 * 30
	maxStep := followMaxSpeed * vw / 30
	for i := 1; i < len(path); i++ {
		a, b := path[i-1], path[i]
		if b.F <= a.F {
			t.Fatalf("keys out of order: %+v %+v", a, b)
		}
		step := math.Abs(b.X-a.X) / float64(b.F-a.F)
		jump := a.F == cutFrame-1 && b.F == cutFrame
		if !jump && step > maxStep*1.05 {
			t.Fatalf("too fast between frames %d and %d: %.5f per frame (at most %.5f)", a.F, b.F, step, maxStep)
		}
		if jump && math.Abs(b.X-a.X) < 0.1 {
			t.Fatalf("the cut should jump: %+v → %+v", a, b)
		}
	}
	jumped := false
	for i := 1; i < len(path); i++ {
		if path[i-1].F == cutFrame-1 && path[i].F == cutFrame {
			jumped = true
		}
	}
	if !jumped {
		t.Fatalf("no jump at the cut (frame %d): %+v", cutFrame, path)
	}
	// The subject stays inside the crop at every moment it was seen.
	for ts := 0.5; ts < 12; ts++ {
		x, _ := FocusAt(path, ts*30)
		if d := math.Abs(moverAt(ts) - x); d > vw/2 {
			t.Fatalf("at %.1f s the subject (%.2f) is out of the crop (%.2f ± %.2f)", ts, moverAt(ts), x, vw/2)
		}
	}
	// After the cut the subject stands still: so does the crop.
	x1, _ := FocusAt(path, 9*30)
	x2, _ := FocusAt(path, 11.5*30)
	if math.Abs(x1-x2) > 1e-6 {
		t.Fatalf("a still subject, a still crop: %.4f %.4f", x1, x2)
	}
	if path[len(path)-1].F != c.DurationInFrames-1 {
		t.Fatalf("the path runs to the clip's last frame: %+v", path[len(path)-1])
	}
}

// A subject that only fidgets leaves the crop where it is (one focus point).
func TestFollowIgnoresFidgeting(t *testing.T) {
	var samples []SubjectSample
	for i, t := 0, 0.5; t < 10; i, t = i+1, t+1 {
		samples = append(samples, SubjectSample{At: t, X: 0.5 + 0.02*float64(i%2), Y: 0.5, Face: true})
	}
	ff := fixedSubject{facts(), samples}
	fr := plan(t, followSpec(FramingFollow), ff).Clips[0].Framing
	if len(fr.Path) != 0 {
		t.Fatalf("fidgeting moved the crop: %+v", fr.Path)
	}
}

type fixedSubject struct {
	fakeFacts
	s []SubjectSample
}

func (f fixedSubject) Subject(string, float64, float64) ([]SubjectSample, []float64, bool) {
	return f.s, nil, true
}

// Speed: the path is in the clip's output frames, so a clip played twice as
// fast covers the same footage in half the frames.
func TestFollowPathFollowsSpeed(t *testing.T) {
	s := followSpec(FramingFollow)
	s.Clips[0].Out = 8
	s.Clips[0].Speed = 2
	p := plan(t, s, moverFacts{facts()})
	c := p.Clips[0]
	if len(c.Framing.Path) == 0 || c.Framing.Path[len(c.Framing.Path)-1].F != c.DurationInFrames-1 || c.DurationInFrames != 120 {
		t.Fatalf("speed: %d frames, path %+v", c.DurationInFrames, c.Framing.Path)
	}
}

// The resolved paths of a few cuts are frozen (testdata/follow-plans.json):
// a revision that follows movement renders the same over time.
func TestFollowPlansGolden(t *testing.T) {
	specs := map[string]*Spec{"vertical-walk": followSpec(FramingFollow)}
	cut := followSpec(FramingFollow)
	cut.Clips = []Clip{{ID: "a", AssetID: astA, In: 2, Out: 6}, {ID: "b", AssetID: astA, In: 6, Out: 11, Speed: 1.5}}
	Normalize(cut)
	specs["two-clips-speed"] = cut
	square := followSpec(FramingFollow)
	square.Output = Output{Preset: "square-1080", FPS: 30, Framing: FramingFollow}
	Normalize(square)
	specs["square"] = square
	got := map[string]json.RawMessage{}
	for name, s := range specs {
		p := Resolve(Clone(s), moverFacts{facts()}, "rev_follow", "prj_follow", Hash(s))
		var frs []PlanFraming
		for _, c := range p.Clips {
			frs = append(frs, c.Framing)
		}
		b, _ := json.Marshal(frs)
		got[name] = b
	}
	path := filepath.Join("testdata", "follow-plans.json")
	if *updateGolden {
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s missing (go test ./internal/editspec -run TestFollowPlansGolden -update): %v", path, err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for name, w := range want {
		var a, b any
		_ = json.Unmarshal(w, &a)
		_ = json.Unmarshal(got[name], &b)
		if !equalJSON(a, b) {
			t.Errorf("%s: the follow path changed\n want %s\n  got %s", name, w, got[name])
		}
	}
	if len(want) != len(got) {
		t.Fatalf("golden has %d cuts, the test %d", len(want), len(got))
	}
}
