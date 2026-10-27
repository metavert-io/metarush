package editspec

import (
	"encoding/json"
	"strings"
	"testing"
)

// faceFacts adds where the faces are: astA's face sits right of center from 0–8 s.
type faceFacts struct{ fakeFacts }

func (f faceFacts) FaceFocus(id string, from, to float64) (float64, float64, bool) {
	if id == astA && from < 8 {
		return 0.72, 0.31, true
	}
	return 0, 0, false
}

func plan(t *testing.T, s *Spec, f Facts) *RenderPlan {
	t.Helper()
	Normalize(s)
	return Resolve(s, f, "rev_1", "prj_1", "h")
}

func hasWarning(issues []Issue, code string) bool {
	for _, i := range issues {
		if i.Code == code {
			return true
		}
	}
	return false
}

func warningAt(issues []Issue, code, path string) *Issue {
	for i := range issues {
		if issues[i].Code == code && issues[i].Path == path {
			return &issues[i]
		}
	}
	return nil
}

// A cut that does not say how to frame its crops keeps the middle, whether or
// not its footage has been indexed: its plan is the same before and after.
func TestOlderCutIgnoresTheIndex(t *testing.T) {
	s := baseSpec() // vertical output; hook and end are landscape astA, heavily cropped, no focus
	before, _ := json.Marshal(plan(t, Clone(s), facts()))
	after, _ := json.Marshal(plan(t, Clone(s), faceFacts{facts()}))
	if string(before) != string(after) {
		t.Fatalf("indexing the footage changed an older cut's plan:\n before %s\n after  %s", before, after)
	}
	if fr := plan(t, Clone(s), faceFacts{facts()}).Clips[0].Framing; fr.FocusX != 0.5 || fr.FocusY != 0.5 || fr.From != "" {
		t.Fatalf("an older cut keeps the middle: %+v", fr)
	}
	// It still warns about the crop, and says how to keep faces in frame.
	w := warningAt(Validate(s, faceFacts{facts()}, nil, false), "heavy_crop", "/clips/0/framing")
	if w == nil || !strings.Contains(w.Message, "keep faces in frame") || strings.Contains(w.Message, "output.framing") {
		t.Fatalf("crop warning with the way to opt in: %+v", w)
	}
}

// Reframing only where the spec says so: the cut (output.framing faces) or the
// clip (framing.auto faces).
func TestFramingFollowsFacesWhenTheSpecSaysSo(t *testing.T) {
	ff := faceFacts{facts()}
	s := baseSpec()
	s.Output.Framing = FramingFaces
	p := plan(t, Clone(s), ff)
	if fr := p.Clips[0].Framing; fr.FocusX != 0.72 || fr.FocusY != 0.31 || fr.From != FramingFaces {
		t.Fatalf("the cut keeps faces in frame: %+v", fr)
	}
	if fr := p.Clips[2].Framing; fr.FocusX != 0.5 || fr.From != "" {
		t.Fatalf("no face in that range: the middle: %+v", fr)
	}
	if fr := p.Clips[1].Framing; fr.From != "" {
		t.Fatalf("a vertical clip in a vertical output is not cropped: nothing to follow: %+v", fr)
	}
	if w := warningAt(Validate(s, ff, nil, false), "heavy_crop", "/clips/0/framing"); w != nil {
		t.Fatalf("the clip follows the face: no crop warning (%+v)", w)
	}
	// Not indexed yet: the middle, and a warning that no face was found.
	if fr := plan(t, Clone(s), facts()).Clips[0].Framing; fr.FocusX != 0.5 || fr.From != "" {
		t.Fatalf("not indexed: the middle: %+v", fr)
	}
	if w := warningAt(Validate(s, facts(), nil, false), "heavy_crop", "/clips/0/framing"); w == nil || !strings.Contains(w.Message, "no face has been found in it yet") {
		t.Fatalf("not indexed: %+v", w)
	}
	// A clip with a focus of its own keeps it…
	x := 0.2
	own := Clone(s)
	own.Clips[0].Framing = &Framing{FocusX: &x}
	if fr := plan(t, own, ff).Clips[0].Framing; fr.FocusX != 0.2 || fr.From != "" {
		t.Fatalf("its own focus: %+v", fr)
	}
	// …unless the clip itself asks to follow the faces, in any cut.
	clip := baseSpec()
	clip.Clips[0].Framing = &Framing{FocusX: &x, Auto: FramingFaces}
	if fr := plan(t, clip, ff).Clips[0].Framing; fr.FocusX != 0.72 || fr.From != FramingFaces {
		t.Fatalf("framing.auto faces: %+v", fr)
	}
	// center says no, like absent.
	center := baseSpec()
	center.Output.Framing = FramingCenter
	if fr := plan(t, center, ff).Clips[0].Framing; fr.FocusX != 0.5 || fr.From != "" {
		t.Fatalf("center: %+v", fr)
	}
	// Fit shows the whole picture: nothing to follow.
	fit := Clone(s)
	fit.Clips[0].Framing = &Framing{Mode: "fit", Auto: FramingFaces}
	if fr := plan(t, fit, ff).Clips[0].Framing; fr.From != "" || fr.FocusX != 0.5 {
		t.Fatalf("fit: %+v", fr)
	}
}

// New cuts are written with faces kept in frame; rewrites keep the framing of
// the cut they replace, and add nothing to an older cut.
func TestNewCutsSayFaces(t *testing.T) {
	s := baseSpec()
	if !NewCutDefaults(s, nil) || s.Output.Framing != FramingFaces {
		t.Fatalf("a new cut says faces: %+v", s.Output)
	}
	old := baseSpec()
	old.Mix = nil
	rewrite := baseSpec()
	NewCutDefaults(rewrite, old)
	if rewrite.Output.Framing != "" {
		t.Fatalf("a rewrite of an older cut adds nothing: %+v", rewrite.Output)
	}
	for _, want := range []string{FramingFaces, FramingCenter} {
		base := baseSpec()
		base.Output.Framing = want
		next := baseSpec()
		NewCutDefaults(next, base)
		if next.Output.Framing != want {
			t.Fatalf("a rewrite keeps %s: %+v", want, next.Output)
		}
	}
	explicit := baseSpec()
	explicit.Output.Framing = FramingCenter
	NewCutDefaults(explicit, nil)
	if explicit.Output.Framing != FramingCenter {
		t.Fatalf("an explicit choice is kept: %+v", explicit.Output)
	}
}

// Opting an older cut in is an ordinary edit (setOutput): the new revision says
// faces and follows them; the old one is unchanged.
func TestOptInIsAnEdit(t *testing.T) {
	ff := faceFacts{facts()}
	old := baseSpec()
	beforeOld, _ := json.Marshal(plan(t, Clone(old), ff))
	out := old.Output
	out.Framing = FramingFaces
	next, touched, err := ApplyOps(old, []Op{{Op: "setOutput", Output: &out}}, false)
	if err != nil || !touched["output"] || next.Output.Framing != FramingFaces {
		t.Fatalf("setOutput: %+v %v %v", next.Output, touched, err)
	}
	if fr := plan(t, Clone(next), ff).Clips[0].Framing; fr.From != FramingFaces {
		t.Fatalf("the opted-in revision follows the face: %+v", fr)
	}
	afterOld, _ := json.Marshal(plan(t, Clone(old), ff))
	if string(beforeOld) != string(afterOld) {
		t.Fatal("the older revision's plan changed")
	}
	// The minimal ops between the two cuts are that one change.
	ops, err := DiffOps(old, next)
	if err != nil || len(ops) != 1 || ops[0].Op != "setOutput" || ops[0].Output == nil || ops[0].Output.Framing != FramingFaces {
		t.Fatalf("diff ops: %+v %v", ops, err)
	}
}
