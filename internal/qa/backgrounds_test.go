package qa

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// A version that replaces clips' backgrounds gets one note saying where and
// what found the people, and a finding for each clip whose cut-out is gone,
// unmade, too short (errors) or from other media than the cut plays (D510).
func TestBackgroundFindings(t *testing.T) {
	ok, gone, soon, short, stale := strings.Repeat("1a", 32), strings.Repeat("2b", 32), strings.Repeat("3c", 32), strings.Repeat("4d", 32), strings.Repeat("5e", 32)
	mattes := map[string]editspec.MatteInfo{
		ok:    {AssetID: "ast_a", From: 0, To: 10, Ready: true, Model: "Apple Vision person segmentation"},
		soon:  {AssetID: "ast_a", From: 0, To: 10},
		short: {AssetID: "ast_a", From: 3, To: 4, Ready: true},
		stale: {AssetID: "ast_b", From: 0, To: 10, Ready: true, Fixed: strings.Repeat("ef", 32)},
	}
	bg := func(sha string) *editspec.ClipBackground {
		return &editspec.ClipBackground{With: editspec.BackgroundColour, Colour: "#00b140", Matte: sha}
	}
	spec := &editspec.Spec{Clips: []editspec.Clip{
		{ID: "a", AssetID: "ast_a", In: 1, Out: 3, Background: bg(ok)},
		{ID: "b", AssetID: "ast_a", In: 1, Out: 3, Background: bg(gone)},
		{ID: "c", AssetID: "ast_a", In: 1, Out: 3, Background: bg(soon)},
		{ID: "d", AssetID: "ast_a", In: 1, Out: 3, Background: bg(short)},
		{ID: "e", AssetID: "ast_b", In: 1, Out: 3, Label: "The garden", Background: bg(stale)},
		{ID: "f", AssetID: "ast_b", In: 1, Out: 3},
	}}
	plan := &editspec.RenderPlan{FPS: 30}
	for i, c := range spec.Clips {
		plan.Clips = append(plan.Clips, editspec.PlanClip{ID: c.ID, From: 60 * i, DurationInFrames: 60})
	}
	r := &run{in: Input{Spec: spec, Mattes: func(sha string) (editspec.MatteInfo, bool) { m, ok := mattes[sha]; return m, ok }}, plan: plan, fps: 30,
		rep: &models.QAReport{}}
	r.backgrounds()
	got := map[string]*models.QAFinding{}
	for _, f := range r.pend {
		got[strings.Join(f.ClipIDs, ",")+" "+f.Kind] = f
	}
	for key, sev := range map[string]string{"b background_matte": models.QAError, "c background_matte": models.QAError, "d background_matte": models.QAError,
		"e background_matte_stale": models.QAWarning} {
		f := got[key]
		if f == nil || f.Severity != sev || !strings.HasPrefix(f.Command, "metarush cut background ") {
			t.Fatalf("%s: %+v (all: %v)", key, f, keysOf(got))
		}
	}
	if f := got["e background_matte_stale"]; !strings.Contains(f.Message, "“The garden”") || !strings.Contains(f.Message, "a fixed copy of the clip") {
		t.Fatalf("stale words: %q", f.Message)
	}
	note := got["a,b,c,d,e background_replaced"]
	if note == nil || note.Severity != models.QAInfo || note.From != 0 || note.To != 10 || !strings.Contains(note.Message, "5 clips") ||
		!strings.Contains(note.Message, "Apple Vision person segmentation") {
		t.Fatalf("the note: %+v", note)
	}
	if len(r.rep.Checks) != 1 || r.rep.Checks[0].Name != "backgrounds" || r.rep.Checks[0].Status != "fail" {
		t.Fatalf("the check: %+v", r.rep.Checks)
	}
	// A version without replaced backgrounds has neither the note nor the check.
	r = &run{in: Input{Spec: &editspec.Spec{Clips: spec.Clips[5:]}}, plan: plan, fps: 30, rep: &models.QAReport{}}
	r.backgrounds()
	if len(r.pend) != 0 || len(r.rep.Checks) != 0 {
		t.Fatalf("no backgrounds: %+v %+v", r.pend, r.rep.Checks)
	}
	for _, f := range got {
		if strings.Contains(f.Message, "--") || strings.Contains(f.Message, "sha") {
			t.Fatalf("not plain: %q", f.Message)
		}
	}
}

func keysOf(m map[string]*models.QAFinding) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The cuts a cut plays (nested, at any depth) are looked at too: a clip of
// theirs whose cut-out is gone or not made is an error at the outer clip that
// plays them, and their replaced backgrounds are part of the video's note,
// at their time on this timeline.
func TestNestedCutsBackgroundsAreChecked(t *testing.T) {
	ok, gone := strings.Repeat("1a", 32), strings.Repeat("2b", 32)
	mattes := map[string]editspec.MatteInfo{ok: {AssetID: "ast_a", From: 0, To: 10, Ready: true, Model: "MODNet"}}
	inner := &editspec.RenderPlan{FPS: 30, Clips: []editspec.PlanClip{
		{ID: "x", AssetID: "ast_a", From: 0, DurationInFrames: 30, Background: &editspec.PlanBackground{With: editspec.BackgroundBlur, CutOut: ok}},
		{ID: "y", AssetID: "ast_a", From: 30, DurationInFrames: 30, Background: &editspec.PlanBackground{With: editspec.BackgroundBlur, CutOut: gone}},
		{ID: "z", AssetID: "ast_a", From: 60, DurationInFrames: 30},
	}}
	key := editspec.NestedKey("prj_inner", "rev_inner")
	plan := &editspec.RenderPlan{FPS: 30, DurationInFrames: 150, Clips: []editspec.PlanClip{
		{ID: "open", AssetID: "ast_b", From: 0, DurationInFrames: 60},
		{ID: "teaser", AssetID: key, From: 60, DurationInFrames: 90},
	}, Nested: map[string]*editspec.NestedPlan{key: {ProjectID: "prj_inner", RevisionID: "rev_inner", Title: "Garden teaser", Plan: inner}}}
	spec := &editspec.Spec{Clips: []editspec.Clip{{ID: "open", AssetID: "ast_b", In: 0, Out: 2}, {ID: "teaser", AssetID: key, In: 0, Out: 3}}}
	r := &run{in: Input{Spec: spec, Mattes: func(sha string) (editspec.MatteInfo, bool) { m, ok := mattes[sha]; return m, ok }}, plan: plan, fps: 30,
		rep: &models.QAReport{}}
	r.backgrounds()
	var errs, notes []*models.QAFinding
	for _, f := range r.pend {
		switch f.Kind {
		case "background_matte":
			errs = append(errs, f)
		case "background_replaced":
			notes = append(notes, f)
		}
	}
	if len(errs) != 1 || errs[0].Severity != models.QAError || strings.Join(errs[0].ClipIDs, ",") != "teaser" || errs[0].From != 3 || errs[0].To != 4 ||
		!strings.Contains(errs[0].Message, "“Garden teaser”") || errs[0].Command != "" {
		t.Fatalf("the nested cut's missing cut-out: %+v", errs)
	}
	if len(notes) != 1 || !strings.Contains(notes[0].Message, "2 clips") || !strings.Contains(notes[0].Message, "MODNet") || notes[0].From != 2 || notes[0].To != 4 {
		t.Fatalf("the note: %+v", notes)
	}
	if len(r.rep.Checks) != 1 || r.rep.Checks[0].Status != "fail" {
		t.Fatalf("the check: %+v", r.rep.Checks)
	}
}
