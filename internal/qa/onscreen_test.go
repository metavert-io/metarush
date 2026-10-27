package qa

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// walkIndex is astA's on-screen index: a face at the far right (0–3 s), then, after a cut,
// low in the middle of the picture (3–6 s), and a sign at the bottom
// ("SALE ENDS SUNDAY") the whole time.
func walkIndex() *visual.Index {
	var samples []visual.Sample
	for at := 0.5; at < 6; at++ {
		f := vision.Frame{W: 1920, H: 1080, Text: []vision.Text{{S: "SALE ENDS SUNDAY", Conf: 1, Box: vision.Box{0.4, 0.86, 0.2, 0.05}}}}
		if at < 3 {
			f.Faces = []vision.Face{{Conf: 1, Box: vision.Box{0.82, 0.2, 0.12, 0.22}}}
		} else {
			f.Faces = []vision.Face{{Conf: 1, Box: vision.Box{0.45, 0.72, 0.1, 0.2}}}
		}
		samples = append(samples, visual.Sample{At: at, Frame: f})
	}
	return visual.Build(samples, 1, 60, []float64{3}, "proxy") // the footage cuts to the new shot at 3 s
}

// subjectFacts follow the index like the real facts do.
type subjectFacts struct {
	*fakeFacts
	idx *visual.Index
}

func (f subjectFacts) Subject(id string, from, to float64) ([]editspec.SubjectSample, []float64, bool) {
	if id != astA {
		return nil, nil, false
	}
	s, cuts, ok := visual.Subject(f.idx, from, to)
	var out []editspec.SubjectSample
	for _, p := range s {
		out = append(out, editspec.SubjectSample{At: p.At, X: p.X, Y: p.Y, Face: p.Face})
	}
	return out, cuts, ok
}

func walkSpec(framing string) *editspec.Spec {
	return &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetVertical, FPS: 30, Platform: "generic", Framing: framing},
		Clips: []editspec.Clip{{ID: "walk", AssetID: astA, In: 0, Out: 6}},
		Captions: &editspec.Captions{Enabled: true, Source: "manual", Position: "bottom", Segments: []editspec.CaptionSegment{
			{From: 0.2, To: 2.9, Text: "I walk to the end of the garden"}, {From: 3.0, To: 5.9, Text: "and then I sit down by the gate"}}}}
}

// The footage's faces and text, placed through the crop, against the
// captions: a face the crop cuts off, captions over a face, captions over
// the footage's own words; each with a fix that applies to the cut.
func TestOnScreenFindsFacesAndTextUnderCaptions(t *testing.T) {
	ts := toolset(t)
	idx := walkIndex()
	spec := walkSpec(editspec.FramingCenter)
	plan := resolve(t, spec, facts(false))
	path := render(t, ts, "540x960", 6, "", "")
	rep := analyze(t, ts, path, plan, spec, func(in *Input) {
		in.OnScreen = map[string]*visual.Index{astA: idx}
		in.FaceNames = map[string]map[int]string{astA: {1: "Ada"}}
	})
	dump(t, rep)
	cut := find(rep, "face_cut_off")
	if cut == nil || cut.Basis != models.QAHeuristic || cut.From > 0.6 || cut.To < 2.4 || cut.To > 3.6 || cut.Message != "Around 0:00 the crop cuts off Ada's face at the right edge (in the “walk” clip)." {
		t.Fatalf("the face at the right edge is cut off by the middle crop: %+v", cut)
	}
	if cut.Fix == nil || len(cut.Fix.Ops) != 1 || cut.Fix.Ops[0].Op != "updateClip" {
		t.Fatalf("fix: let the crop follow her: %+v", cut.Fix)
	}
	fixed, _, err := editspec.ApplyOps(spec, cut.Fix.Ops, false)
	if err != nil || fixed.Clips[0].Framing == nil || fixed.Clips[0].Framing.Auto != editspec.FramingFollow {
		t.Fatalf("the fix applies: %+v %v", fixed, err)
	}
	covered := find(rep, "faces_covered")
	if covered == nil || covered.From < 2.4 || covered.Fix == nil || len(covered.Fix.Ops) != 1 || covered.Fix.Ops[0].Captions.Position != "top" {
		t.Fatalf("the captions cover the face low in the picture; move them to the top: %+v", covered)
	}
	text := find(rep, "text_under_captions")
	if text == nil || text.Fix == nil || len(text.Fix.Ops) != 1 || text.Fix.Ops[0].Captions.Position != "top" {
		t.Fatalf("the captions sit over the sign: %+v", text)
	}
	if c := checkOf(rep, "on_screen"); c == nil || c.Status != "warning" || c.Basis != models.QAHeuristic {
		t.Fatalf("check: %+v", c)
	}
	// Following movement keeps her in the picture: no face cut off.
	follow := walkSpec(editspec.FramingFollow)
	editspec.Normalize(follow)
	fplan := editspec.Resolve(editspec.Clone(follow), subjectFacts{facts(false), idx}, "rev_test", "prj_test", "hash")
	if len(fplan.Clips[0].Framing.Path) == 0 {
		t.Fatalf("the follow plan has a path: %+v", fplan.Clips[0].Framing)
	}
	rep2 := analyze(t, ts, path, fplan, follow, func(in *Input) { in.OnScreen = map[string]*visual.Index{astA: idx} })
	if f := find(rep2, "face_cut_off"); f != nil {
		t.Fatalf("following movement keeps the face in the picture: %+v", f)
	}
	// Nothing looked at: the check says why it didn't run.
	rep3 := analyze(t, ts, path, plan, spec, nil)
	if c := checkOf(rep3, "on_screen"); c == nil || c.Status != "skipped" {
		t.Fatalf("skipped without an index: %+v", c)
	}
}

// Where a Mac can look, the rendered frames confirm what the geometry says.
func TestOnScreenLooksAtTheRender(t *testing.T) {
	ts := toolset(t)
	idx := walkIndex()
	spec := walkSpec(editspec.FramingCenter)
	plan := resolve(t, spec, facts(false))
	path := render(t, ts, "540x960", 6, "", "")
	looked := 0
	rep := analyze(t, ts, path, plan, spec, func(in *Input) {
		in.OnScreen = map[string]*visual.Index{astA: idx}
		in.Look = func(ctx context.Context, dir string) ([]vision.Frame, error) {
			frames, err := listJPEGs(dir)
			looked += len(frames)
			// What the helper would see: a face cut by the right edge, the
			// caption words over the low face, and the sign under the captions.
			var out []vision.Frame
			for _, name := range frames {
				out = append(out, vision.Frame{File: name, W: 540, H: 960,
					Faces: []vision.Face{{Conf: 0.9, Box: vision.Box{0.93, 0.2, 0.07, 0.12}}, {Conf: 0.9, Box: vision.Box{0.4, 0.66, 0.2, 0.18}}},
					Text:  []vision.Text{{S: "and then I sit down by the gate", Conf: 1, Box: vision.Box{0.1, 0.78, 0.8, 0.04}}, {S: "SALE ENDS SUNDAY", Conf: 0.7, Box: vision.Box{0.3, 0.86, 0.4, 0.04}}}})
			}
			return out, err
		}
	})
	dump(t, rep)
	if looked == 0 {
		t.Fatal("the rendered frames were looked at")
	}
	for _, k := range []string{"face_cut_off", "faces_covered", "text_under_captions"} {
		if f := find(rep, k); f == nil || f.Basis != models.QAObserved {
			t.Fatalf("%s is seen in the rendered picture: %+v", k, f)
		}
	}
	if c := checkOf(rep, "on_screen"); c == nil || c.Basis != models.QAObserved {
		t.Fatalf("check: %+v", c)
	}
}

func listJPEGs(dir string) ([]string, error) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".jpg") {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

func checkOf(rep *models.QAReport, name string) *models.QACheck {
	for i := range rep.Checks {
		if rep.Checks[i].Name == name {
			return &rep.Checks[i]
		}
	}
	return nil
}
