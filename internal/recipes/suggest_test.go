package recipes

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// suggestDoc is a recipe as the suggestions read it: a fixed intro, chosen
// moments, a title card, an end card whose headline is an input, music, two
// narration lines and captions at the bottom.
func suggestDoc() *models.RecipeDoc {
	gain := -12.0
	return &models.RecipeDoc{Format: models.RecipeFormat, Title: "Teaser",
		Inputs: []models.RecipeInput{{Name: "headline", Kind: models.RecipeWords, Default: models.RecipeValue{Text: "Read free"}},
			{Name: "footage", Kind: models.RecipeFootage, Default: models.RecipeValue{Items: []string{"ast_b"}}}},
		Sections: []models.RecipeSection{
			{Name: "intro", Kind: models.RecipeFixed, Clips: []editspec.Clip{{ID: "intro", AssetID: "ast_a", In: 0, Out: 3}}},
			{Name: "moments", Kind: models.RecipeChosen, From: "footage", Clips: []editspec.Clip{{ID: "hook", AssetID: "ast_b", In: 1, Out: 5}, {ID: "answer", AssetID: "ast_b", In: 6, Out: 10}}},
		},
		Cut: models.RecipeCut{Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
			Overlays: []editspec.Overlay{
				{ID: "title", Template: editspec.TplTitleCard, At: 0.5, Duration: 2, Params: json.RawMessage(`{"title":"Stories for tonight"}`)},
				{ID: "end", Template: editspec.TplEndCard, At: 8, Duration: 3, Params: json.RawMessage(`{"headline":"Read free","cta":"Start now"}`)}},
			Audio:     []editspec.AudioTrack{{ID: "music", AssetID: "ast_m", Role: editspec.RoleMusic, In: 0, Out: 11, GainDB: &gain}},
			Captions:  &editspec.Captions{Enabled: true, Position: "bottom"},
			Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{{ID: "line-1", Text: "Meet the app."}, {ID: "line-2", Text: "Read free tonight."}}}},
		Bindings: []models.RecipeBinding{{Input: "headline", To: "param:end.headline"}}}
}

// madeOf is the cut a run makes of a recipe (its clips, then the rest).
func madeOf(d *models.RecipeDoc) *editspec.Spec {
	s := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Output: d.Cut.Output, Overlays: cloneOverlays(d.Cut.Overlays), Audio: cloneAudio(d.Cut.Audio),
		Captions: cloneCaptions(d.Cut.Captions), Voiceover: cloneVoiceover(d.Cut.Voiceover)}
	for _, sec := range d.Sections {
		for _, c := range sec.Clips {
			c := cloneClip(c)
			c.Locked = sec.Kind == models.RecipeFixed
			s.Clips = append(s.Clips, c)
		}
	}
	return s
}

func evidenceRun(n int, d *models.RecipeDoc, change func(s *editspec.Spec)) *runEvidence {
	made := madeOf(d)
	now := editspec.Clone(made)
	if change != nil {
		change(now)
	}
	return &runEvidence{run: &models.RecipeRun{RecipeName: "teaser", Number: n}, doc: d, made: made, approved: now}
}

func clipOf(s *editspec.Spec, id string) *editspec.Clip {
	for i := range s.Clips {
		if s.Clips[i].ID == id {
			return &s.Clips[i]
		}
	}
	return nil
}

// What people kept changing across runs becomes suggestions with plain reasons
// — a shorter intro, the same new call to action, quieter music, captions off,
// a narration line rewritten differently each time (something each run gives)
// — while what changed once, what an input gives (the headline) and the
// moments Claude chose don't; a draft with them has each change, and a version
// that has them already gets none of them again.
func TestSuggestionsComeFromWhatRepeats(t *testing.T) {
	d := suggestDoc()
	edit := func(n int) func(s *editspec.Spec) {
		return func(s *editspec.Spec) {
			o := overlayIn(s, "end")
			o.Params = setParam(o.Params, "headline", fmt.Sprintf("Episode %d", n)) // an input: the run's own words
			clipOf(s, "hook").In = 2                                                // Claude's moment, nudged: not the recipe's
			if n == 4 {
				s.Overlays = s.Overlays[1:] // the title card taken out once
				return
			}
			clipOf(s, "intro").Out = 2 + 0.1*float64(n)
			o = overlayIn(s, "end")
			o.Params = setParam(o.Params, "cta", "Start reading")
			g := -18.0
			s.Audio[0].GainDB = &g
			s.Captions.Enabled = false
			beatIn(s, "line-2").Text = fmt.Sprintf("Tonight's story %d.", n)
		}
	}
	ev := []*runEvidence{evidenceRun(1, d, edit(1)), evidenceRun(2, d, edit(2)), evidenceRun(3, d, edit(3)), evidenceRun(4, d, edit(4))}
	got := map[string]*suggestion{}
	var keys []string
	for _, s := range aggregate(ev, nil) {
		got[s.Key] = s
		keys = append(keys, s.Key)
	}
	want := []string{"length:intro:shorter", "words:end.cta", "captions:off", "level:music:quieter", "vary-line:line-2"}
	if len(keys) != len(want) {
		t.Fatalf("suggestions %v, want %v", keys, want)
	}
	for _, k := range want {
		s := got[k]
		if s == nil {
			t.Fatalf("no %s in %v", k, keys)
		}
		if s.Count != 3 || s.Of != 4 || len(s.Runs) != 3 || !strings.Contains(s.Reason, "in 3 of 4 runs") {
			t.Errorf("%s: %d of %d %v %q", k, s.Count, s.Of, s.Runs, s.Reason)
		}
	}
	if r := got["length:intro:shorter"].Reason; r != "You shortened “intro” in 3 of 4 runs: from 3 s to about 2.2 s." {
		t.Errorf("the intro's reason: %q", r)
	}
	if r := got["words:end.cta"].Reason; r != "You changed the end card's call to action to “Start reading” in 3 of 4 runs (it said “Start now”)." {
		t.Errorf("the call to action's reason: %q", r)
	}
	if r := got["level:music:quieter"].Reason; r != "You made the music “music” quieter in 3 of 4 runs (by about 6 dB)." {
		t.Errorf("the music's reason: %q", r)
	}
	if c := got["vary-line:line-2"].Change; !strings.Contains(c, "Narration line 2 is something each run gives") {
		t.Errorf("the line's change: %q", c)
	}
	// The draft has each change.
	draft := Clone(d)
	for _, k := range want {
		if !applies(draft, got[k]) || !apply(draft, got[k]) {
			t.Fatalf("%s doesn't apply", k)
		}
	}
	if c, _ := clipByID(draft, "intro"); c.In != 0 || c.Out != 2.2 {
		t.Errorf("the intro: %+v", c)
	}
	if cta := paramsOf(overlayByID(draft, "end"))["cta"]; cta != "Start reading" {
		t.Errorf("the call to action: %v", cta)
	}
	if g := draft.Cut.Audio[0].GainDB; g == nil || *g != -18 {
		t.Errorf("the music: %v", g)
	}
	if draft.Cut.Captions.Enabled {
		t.Error("captions stay on")
	}
	in := inputByName(draft)["line-2"]
	if in == nil || in.Kind != models.RecipeWords || in.Default.Text != "Read free tonight." || len(bindingsOf(draft, "line-2")) != 1 {
		t.Errorf("line 2 is an input now: %+v %+v", in, draft.Bindings)
	}
	// A version that has them already gets none of them again.
	for _, k := range want {
		if applies(draft, got[k]) {
			t.Errorf("%s again on a version that has it", k)
		}
	}
	// Once is not a pattern; two of five is not more than half.
	if s := aggregate(ev[:1], nil); len(s) != 0 {
		t.Errorf("one run: %+v", s)
	}
	five := append(append([]*runEvidence{}, ev[2:]...), evidenceRun(5, d, nil), evidenceRun(6, d, nil), evidenceRun(7, d, nil))
	for _, s := range aggregate(five, nil) {
		t.Errorf("2 of 5 runs: %s %q", s.Key, s.Reason)
	}
}

// Taking a fixed clip out drops it with what went only to it; a graphic taken
// out takes its input along; a section's moments made shorter in all set its
// length; a graphic shown for less time; the captions moved; the music taken out.
func TestSuggestionsTakeThingsOut(t *testing.T) {
	d := suggestDoc()
	d.Sections = append([]models.RecipeSection{{Name: "brand", Kind: models.RecipeFixed, Clips: []editspec.Clip{{ID: "brand", AssetID: "ast_c", In: 0, Out: 2}}}}, d.Sections...)
	d.Cut.Overlays[0].Span = &editspec.OverlaySpan{From: "brand", To: "intro"}
	ev := []*runEvidence{}
	for n := 1; n <= 3; n++ {
		ev = append(ev, evidenceRun(n, d, func(s *editspec.Spec) {
			s.Clips = s.Clips[1:] // no brand intro
			s.Overlays = s.Overlays[:1]
			s.Overlays[0].Duration = 1.2
			clipOf(s, "answer").Out = 7 // the moments: 4 + 1 = 5 s instead of 8
			s.Captions.Position = "top"
			s.Audio = nil
		}))
	}
	got := map[string]*suggestion{}
	for _, s := range aggregate(ev, func(string) string { return "Garden bed" }) {
		got[s.Key] = s
	}
	for _, k := range []string{"gone:brand", "graphic-gone:end", "graphic-length:title:shorter", "moments:moments:shorter", "captions:top", "sound-gone:music"} {
		if got[k] == nil || got[k].Count != 3 || !strings.Contains(got[k].Reason, "in all 3 runs") {
			t.Fatalf("%s: %+v (all: %v)", k, got[k], keysOf(got))
		}
	}
	if r := got["sound-gone:music"].Reason; r != "You took the music “Garden bed” out in all 3 runs." {
		t.Errorf("music by its name: %q", r)
	}
	draft := Clone(d)
	for _, s := range got {
		if !apply(draft, s) {
			t.Fatalf("%s doesn't apply", s.Key)
		}
	}
	if len(draft.Sections) != 2 || draft.Sections[0].Name != "intro" {
		t.Errorf("the brand section goes with its clip: %+v", draft.Sections)
	}
	if sp := draft.Cut.Overlays[0].Span; sp == nil || sp.From != "intro" {
		t.Errorf("the title follows the next clip: %+v", sp)
	}
	if overlayByID(draft, "end") != nil || inputByName(draft)["headline"] != nil || len(draft.Bindings) != 0 {
		t.Errorf("the end card goes with its headline input: %+v %+v", draft.Cut.Overlays, draft.Inputs)
	}
	if inputByName(draft)["footage"] == nil {
		t.Error("the moments' footage stays an input")
	}
	if l := draft.Sections[1].Length; l == nil || l.Min != 4.5 || l.Max != 6 {
		t.Errorf("the moments last about 5 s in all: %+v", l)
	}
	if o := overlayByID(draft, "title"); o.Duration != 1.2 {
		t.Errorf("the title card: %+v", o)
	}
	if draft.Cut.Captions.Position != "top" || draft.Cut.Audio != nil {
		t.Errorf("captions %+v music %+v", draft.Cut.Captions, draft.Cut.Audio)
	}
}

func keysOf(m map[string]*suggestion) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The fixture end to end: three runs, two of them with the brand intro
// shortened before a person approved them, the third approved as it was made:
// "You shortened “brand-intro” in 2 of 3 runs"; Claude saves the draft (a draft,
// never current); a person makes it current, and the suggestion is gone.
func TestSuggestionsFromApprovedRuns(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	empty := must[RecipeSuggestionsOut](t, f.a, "recipe.suggestions", RecipeSuggestionsIn{Recipe: "app-teaser"}, false)
	if empty.Looked != 0 || len(empty.Suggestions) != 0 || !strings.Contains(empty.Summary, "No approved runs") {
		t.Fatalf("no runs yet: %+v", empty)
	}
	for n := 1; n <= 3; n++ {
		plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
		run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: plan.Fingerprint}, true).Run
		f.finishReview(run.ID)
		done := f.advance(run.ID)
		if done.State != models.RunDone {
			t.Fatalf("run %d: %+v", n, done.Steps)
		}
		if n == 2 {
			waiting := must[RecipeSuggestionsOut](t, f.a, "recipe.suggestions", RecipeSuggestionsIn{Recipe: "app-teaser"}, false)
			if waiting.Looked != 1 || waiting.Waiting != 1 {
				t.Fatalf("a run not approved yet waits: %+v", waiting)
			}
		}
		rev := done.Revision
		if n < 3 {
			// A person shortens the brand intro (a fixed section: unlocked on purpose), then approves.
			set, _ := json.Marshal(map[string]any{"out": 2})
			plan := editspec.Plan{BaseRevisionID: rev, Branch: done.Branch, Instruction: "a shorter intro", Ops: []editspec.Op{{Op: "updateClip", ID: "brand", Set: set}}}
			raw, _ := json.Marshal(plan)
			chk := must[ops.EditCheckOut](t, f.a, "edit.check", ops.EditCheckIn{Plan: raw, AllowUnlock: true}, true)
			if !chk.Valid {
				t.Fatalf("the shorter intro: %+v", chk.Errors)
			}
			rev = must[ops.EditApplyOut](t, f.a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: chk.ConfirmToken, AllowUnlock: true}, true).Revision.ID
		}
		must[ops.RevisionSummary](t, f.a, "revision.approve", ops.RevisionApproveIn{ID: rev}, true)
	}
	sug := must[RecipeSuggestionsOut](t, f.a, "recipe.suggestions", RecipeSuggestionsIn{Recipe: "app-teaser"}, false)
	if sug.Looked != 3 || sug.Waiting != 0 || len(sug.Suggestions) != 1 {
		t.Fatalf("suggestions %+v", sug)
	}
	s := sug.Suggestions[0]
	if s.Key != "length:brand:shorter" || s.Reason != "You shortened “brand-intro” in 2 of 3 runs: from 3 s to about 2 s." || s.Count != 2 || s.Of != 3 ||
		strings.Join(s.Runs, ",") != "app-teaser#1,app-teaser#2" || !s.InDraft {
		t.Fatalf("the suggestion %+v", s)
	}
	if sug.Base == nil || sug.Base.Number != 1 || len(sug.Changes) == 0 || sug.Check == nil || !sug.Check.OK {
		t.Fatalf("the draft it would make: base %+v changes %+v check %+v", sug.Base, sug.Changes, sug.Check)
	}
	if _, err := call[RecipeSuggestionsOut](t, f.a, "recipe.suggestions", RecipeSuggestionsIn{Recipe: "app-teaser", Only: []string{"words:nothing"}}, false); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("an unknown suggestion: %v", err)
	}
	// Claude saves it: a draft, never current.
	saved := must[RecipeUpdateOut](t, f.a, "recipe.suggestions.save", RecipeSuggestionsSaveIn{RecipeSuggestionsIn: RecipeSuggestionsIn{Recipe: "app-teaser"}}, false)
	if saved.Same || saved.Version.Number != 2 || saved.Version.State != models.RecipeDraft || !strings.Contains(saved.Version.Note, "Suggested from runs") {
		t.Fatalf("saved %+v", saved)
	}
	r, _ := loadRecipe(f.ctx, f.a, "app-teaser")
	if r.CurrentNumber != 1 || r.DraftNumber != 2 {
		t.Fatalf("version 1 stays current: %+v", r)
	}
	v, _ := loadVersion(f.ctx, f.a, r, "2")
	if c, _ := clipByID(&v.Doc, "brand"); c == nil || c.Out != 2 {
		t.Fatalf("the draft's brand intro: %+v", c)
	}
	if _, err := call[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "app-teaser", Version: "2"}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("only a person makes it current: %v", err)
	}
	must[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "app-teaser", Version: "2"}, true)
	after := must[RecipeSuggestionsOut](t, f.a, "recipe.suggestions", RecipeSuggestionsIn{Recipe: "app-teaser"}, false)
	if len(after.Suggestions) != 0 || !strings.Contains(after.Summary, "Nothing people changed repeats") {
		t.Fatalf("the current version has it now: %+v", after)
	}
	if _, err := call[RecipeUpdateOut](t, f.a, "recipe.suggestions.save", RecipeSuggestionsSaveIn{RecipeSuggestionsIn: RecipeSuggestionsIn{Recipe: "app-teaser"}}, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("nothing to save: %v", err)
	}
}
