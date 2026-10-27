package recipes

import (
	"math"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sound"
	"github.com/metavert-io/metarush/internal/store"
)

// First picks (D215): the moments a section's new footage starts with are
// whole stretches of speech, scored (a question and its answer first), in the
// footage's order; without words, what moves on screen; with neither, stand-ins.
func TestPickMoments(t *testing.T) {
	a := "ast_01jabcdefghjkmnpqrstvwxyz0"
	speech := func(from, to float64, who, text string) ops.Moment {
		return ops.Moment{AssetID: a, From: from, To: to, Kind: "speech", Detail: who, Text: text, Ref: "seg"}
	}
	ms := []ops.Moment{
		speech(0.5, 2, "Ada", "Hello there."),
		speech(2.6, 4.4, "Ada", "What surprised you the most?"),
		speech(4.8, 6, "Bo", "In a bucket."),
		speech(6.8, 10.6, "Bo", "We grow tomatoes on the roof every summer."),
		speech(11, 11.8, "Ada", "Thanks."),
		{AssetID: a, From: 3, To: 3.5, Kind: "pause"},
	}
	sec := models.RecipeSection{Name: "moments", Kind: models.RecipeChosen, From: "footage", Count: &models.RecipeRange{Min: 1, Max: 3}, Length: &models.RecipeRange{Min: 4, Max: 8},
		Clips: []editspec.Clip{{ID: "hook", AssetID: a, In: 1, Out: 4}, {ID: "answer", AssetID: a, In: 6, Out: 9}}}
	v := &models.RecipeRunInput{Name: "footage", Items: []models.RecipeRunItem{{ID: a, Duration: 12}}}
	picks := firstPicks(sec, v, map[string][]ops.Moment{a: ms})
	if len(picks) != 2 {
		t.Fatalf("two moments, as the section was saved with: %+v", picks)
	}
	var qa *footMoment
	for i := range picks {
		if strings.HasPrefix(picks[i].First, "What surprised") {
			qa = &picks[i]
		}
	}
	if qa == nil || !strings.Contains(strings.Join(qa.Why, ","), "a question and its answer") || qa.From != 2.45 || qa.To != 6.3 || !strings.Contains(qa.Text, "In a bucket.") {
		t.Fatalf("the question and its answer, whole: %+v", picks)
	}
	if picks[0].From > picks[1].From {
		t.Fatalf("in the footage's order: %+v", picks)
	}
	if picks[0].To > picks[1].From && picks[0].From < picks[1].To {
		t.Fatalf("no overlap: %+v", picks)
	}
	// No words: what moves on screen.
	moving := []ops.Moment{{AssetID: a, From: 5, To: 7, Kind: "visual", Change: "motion"}, {AssetID: a, From: 1, To: 1, Kind: "visual", Change: "cut"}}
	vp := firstPicks(sec, v, map[string][]ops.Moment{a: moving})
	if len(vp) == 0 || vp[0].Why[0] == "" || vp[len(vp)-1].To > 12 {
		t.Fatalf("pictures that move: %+v", vp)
	}
	found := false
	for _, p := range vp {
		if p.Why[0] == "movement on screen" && p.From <= 6 && p.To >= 6 {
			found = true
		}
	}
	if !found {
		t.Fatalf("the movement is picked: %+v", vp)
	}
	// Neither: stand-ins, as before.
	if got := firstPicks(sec, v, map[string][]ops.Moment{a: {{AssetID: a, From: 1, To: 2, Kind: "pause"}}}); len(got) != 0 {
		t.Fatalf("nothing to pick from: %+v", got)
	}
	clips, recs := chosenClips(sec, assemblyIn{Values: map[string]*models.RecipeRunInput{"footage": v}, Run: "a test"}, map[string]bool{})
	if len(clips) != 1 || len(recs) != 1 || recs[0].First || !strings.HasPrefix(clips[0].Label, "To choose:") {
		t.Fatalf("stand-ins when there are no moments: %+v %+v", clips, recs)
	}
	// Picks become clips that say why, with the words as evidence.
	pc, pr := pickClips(sec, picks, "run 1 of “T”", map[string]bool{"moments-1": true})
	if pc[0].ID != "moments-1-2" || !strings.HasPrefix(pc[0].Label, "First pick: ") || pc[0].Rationale == "" || !pr[0].First || len(pr[0].Why) == 0 {
		t.Fatalf("pick clips %+v %+v", pc[0], pr[0])
	}
	if q := pc[1]; len(q.Evidence) != 1 || q.Evidence[0].Kind != "transcript" || q.Evidence[0].Quote != "What surprised you the most?" {
		t.Fatalf("evidence %+v", q.Evidence)
	}
}

// A long stretch of speech (a recognizer that writes long segments) is split
// at its sentences, from its words, so a pick starts and ends with one.
func TestLongSpeechSplitsAtSentences(t *testing.T) {
	a := "ast_01jabcdefghjkmnpqrstvwxyz0"
	w := func(text string, from, to float64) models.Word { return models.Word{Text: text, From: from, To: to} }
	seg := models.TranscriptSegment{ID: "seg_1", Words: []models.Word{w("I", 0.5, 0.6), w("burned", 0.7, 1), w("three", 1.1, 1.3), w("motors.", 1.4, 1.9),
		w("What", 2.2, 2.4), w("would", 2.5, 2.7), w("you", 2.8, 2.9), w("build?", 3, 3.5), w("That", 3.8, 4), w("changed", 4.1, 4.5), w("everything.", 4.6, 7.4)}}
	ms := []ops.Moment{{AssetID: a, From: 0.5, To: 7.4, Kind: "speech", Text: "I burned three motors. What would you build? That changed everything.", Ref: "seg_1", Detail: "Ada"},
		{AssetID: a, From: 8, To: 9, Kind: "speech", Text: "Short.", Ref: "seg_2", Detail: "Ada"}}
	got := splitSentences(ms, map[string]models.TranscriptSegment{"seg_1": seg})
	if len(got) != 4 || got[0].Text != "I burned three motors." || got[1].From != 2.2 || got[1].To != 3.5 || got[1].Text != "What would you build?" || got[3].Text != "Short." {
		t.Fatalf("sentences %+v", got)
	}
	// Each sentence can start a moment now (one voice, no pause between them).
	cands := candidates(a, 10, got, 1, 3, 2)
	starts := map[float64]bool{}
	for _, c := range cands {
		starts[c.From] = true
	}
	if !starts[round3(2.2-0.15)] {
		t.Fatalf("a sentence after another starts a moment: %+v", cands)
	}
}

// transcript gives a clip a transcript (segments with who says them).
func (f *fx) transcript(assetID string, segs ...models.TranscriptSegment) {
	f.t.Helper()
	for i := range segs {
		s := segs[i]
		s.ID, s.WorkspaceID, s.AssetID, s.Index, s.Provider, s.Model, s.CreatedAt = ids.New("seg"), f.a.WorkspaceID(), assetID, i, "test", "truth", time.Now().UTC()
		if err := f.a.DB.Insert(f.ctx, store.CSegments, s); err != nil {
			f.t.Fatal(err)
		}
	}
	st := models.TranscriptStatus{State: "available", Provider: "test", Precision: "segment", Language: "en", Segments: len(segs), Speakers: 2, UpdatedAt: time.Now().UTC()}
	if err := f.a.DB.Update(f.ctx, store.CAssets, f.a.WorkspaceID(), assetID, bson.M{"$set": bson.M{"transcript": st}}); err != nil {
		f.t.Fatal(err)
	}
}

// New footage with words: the section starts with MetaRush's first picks, the
// run waits for Claude to keep or change them (said plainly, the picks with
// why), keeping them lets the run go on; changing one changes the cut.
func TestFirstPicksWaitForClaude(t *testing.T) {
	f := setup(t)
	f.transcript(f.interview2,
		models.TranscriptSegment{From: 0.5, To: 2, Text: "Hello there.", SpeakerName: "Ada"},
		models.TranscriptSegment{From: 2.6, To: 4.4, Text: "What surprised you the most?", SpeakerName: "Ada"},
		models.TranscriptSegment{From: 4.8, To: 6, Text: "In a bucket.", SpeakerName: "Bo"},
		models.TranscriptSegment{From: 6.8, To: 10.6, Text: "We grow tomatoes on the roof every summer.", SpeakerName: "Bo"},
		models.TranscriptSegment{From: 11, To: 11.8, Text: "Thanks.", SpeakerName: "Ada"})
	f.save(RecipeSaveIn{})
	in := RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: "footage", Items: []string{f.interview2}}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if !plan.Ready || !plan.Claude || !strings.Contains(stepOf(&models.RecipeRun{Steps: plan.Steps}, stepClaude).Detail, "first picks for “moments”") {
		t.Fatalf("plan %+v", plan.Steps)
	}
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint}, true).Run
	if run.State != models.RunWaiting || run.WaitingFor != models.RunWaitsForClaude || !strings.Contains(run.Waiting, "look at the first picks for “moments” (keep or change them)") {
		t.Fatalf("waits %q", run.Waiting)
	}
	if len(run.StandIns) != 2 || !run.StandIns[0].First || len(run.Picks) != 2 || run.Picks[0].State != models.PickFirst {
		t.Fatalf("picks %+v %+v", run.StandIns, run.Picks)
	}
	// In the footage's order: "Hello there.", then the question and its answer.
	qa, other := run.Picks[1], run.Picks[0]
	if !strings.Contains(strings.Join(qa.Why, ","), "a question and its answer") || qa.FootageTitle == "" || !strings.Contains(run.Claude, "first picks") ||
		!strings.Contains(run.Claude, qa.ClipID) || other.Words != "Hello there." {
		t.Fatalf("the first picks and Claude's task: %+v\n%s", run.Picks, run.Claude)
	}
	rev := f.revision(run.Revision)
	var pick editspec.Clip
	for _, c := range rev.Spec.Clips {
		if c.ID == qa.ClipID {
			pick = c
		}
	}
	if pick.AssetID != f.interview2 || pick.In != 2.45 || pick.Out != 6.3 || !strings.HasPrefix(pick.Label, "First pick:") || pick.Locked {
		t.Fatalf("the pick in the cut: %+v", pick)
	}
	// Change the first pick (Claude, or a person), then keep the other.
	from, to := 6.6, 10.8
	if _, err := call[RecipePicksOut](t, f.a, "recipe.picks", RecipePicksIn{Run: run.ID, Set: []RecipePickChange{{Part: "moments", N: 9, From: &from, To: &to}}}, false); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("no such pick: %v", err)
	}
	ch := must[RecipePicksOut](t, f.a, "recipe.picks", RecipePicksIn{Run: run.ID, Set: []RecipePickChange{{Part: "moments", N: 1, From: &from, To: &to, Why: "the roof garden"}}}, false)
	if ch.Run.State != models.RunWaiting || ch.Run.Picks[0].State != models.PickChanged || ch.Run.Picks[0].From != 6.6 || ch.Run.Picks[1].State != models.PickFirst {
		t.Fatalf("one changed, one still to look at: %+v", ch.Run.Picks)
	}
	head := f.revision(ch.Run.Revision)
	if head.Origin != "recipe:"+run.ID || head.Number <= rev.Number {
		t.Fatalf("a new version of the run's cut: %s %d", head.Origin, head.Number)
	}
	for _, c := range head.Spec.Clips {
		if c.ID == other.ClipID && (c.Label != "Chosen: the roof garden" || c.Rationale != "the roof garden" || len(c.Evidence) != 0 || c.Notes != "" || c.In != 6.6) {
			t.Fatalf("the changed pick keeps nothing of the first one: %+v", c)
		}
	}
	kept := must[RecipePicksOut](t, f.a, "recipe.picks", RecipePicksIn{Run: run.ID, Keep: true}, false)
	if kept.Run.Picks[1].State != models.PickKept || stepOf(&kept.Run.RecipeRun, stepClaude).State != models.StepDone || kept.Run.Render == "" {
		t.Fatalf("kept: the run goes on: %+v / %+v", kept.Run.Picks, kept.Run.Steps)
	}
	f.finishReview(run.ID)
	if done := f.advance(run.ID); done.State != models.RunDone {
		t.Fatalf("done %+v", done.Steps)
	}
	// A run that ended takes no more picks.
	if _, err := call[RecipePicksOut](t, f.a, "recipe.picks", RecipePicksIn{Run: run.ID, Keep: true}, false); codeOf(err) != apperr.Conflict {
		t.Fatalf("ended: %v", err)
	}
}

// The shorts of a pack are priced like the main video's clearer voices: the
// voices it makes clearer with ElevenLabs, over the shorts' stretches as sound
// processes them (padded to whole seconds, and a second for the sound at the
// end: an upper bound), under the pack's step (one estimate for the whole run).
func TestPackClearerVoicesArePriced(t *testing.T) {
	d := doc()
	p := &planned{Doc: d, Changed: map[string]bool{}, Estimate: costs.Of(), stepUSD: map[string]float64{},
		Assembly: &assembly{Spec: &editspec.Spec{Mix: &editspec.Mix{Voices: []editspec.VoiceSound{{Name: "Sam", Enhance: &editspec.Enhance{Mode: editspec.EnhanceCloud}},
			{Name: "Maya", Enhance: &editspec.Enhance{Mode: editspec.EnhanceClean}}}}}},
		Outputs: []models.RecipeRunOutput{{From: 10, To: 22}, {From: 30, To: 36}}}
	planPackSound(p)
	if len(p.Estimate.Parts) != 1 {
		t.Fatalf("parts %+v", p.Estimate.Parts)
	}
	l := p.Estimate.Parts[0]
	if l.Step != "Make the shorts" || l.Service != "ElevenLabs" || !strings.Contains(l.Source, "Sam") || l.Units != "up to 28 s of sound (467 credits)" ||
		math.Abs(l.USD-providers.IsolationUSD(sound.CloudCredits(28))) > 1e-6 || p.stepUSD[stepPack] != costs.Round(l.USD) {
		t.Fatalf("part %+v", l)
	}
	steps := planSteps(&planned{Doc: d, Assembly: &assembly{Spec: &editspec.Spec{}}, Outputs: p.Outputs, stepUSD: p.stepUSD, Project: &models.Project{Name: "P"}})
	var ids []string
	for _, s := range steps {
		ids = append(ids, s.ID)
	}
	if strings.Join(ids, ",") != "inputs,place,assemble,claude,review,pack,finish" || stepOf(&models.RecipeRun{Steps: steps}, stepPack).Estimate != p.stepUSD[stepPack] {
		t.Fatalf("steps %v", ids)
	}
}
