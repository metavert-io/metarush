package qa

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// AI-generated footage gets one information note for the whole video, with
// where it shows and that platforms ask people to say so (D127); never a
// warning or an error.
func TestAIGeneratedIsNoted(t *testing.T) {
	r := &run{in: Input{Generated: map[string]GeneratedClip{"ast_g": {Provider: "Google Gemini"}}}, fps: 30,
		plan: &editspec.RenderPlan{FPS: 30, Clips: []editspec.PlanClip{
			{ID: "me", AssetID: "ast_a", From: 0, DurationInFrames: 90},
			{ID: "boat", AssetID: "ast_g", From: 90, DurationInFrames: 120},
			{ID: "boat2", AssetID: "ast_g", From: 300, DurationInFrames: 60},
		}}}
	r.aiGenerated()
	if len(r.pend) != 1 {
		t.Fatalf("one note: %d", len(r.pend))
	}
	f := r.pend[0]
	if f.Severity != models.QAInfo || f.Kind != "ai_generated" || f.From != 3 || f.To != 12 || len(f.ClipIDs) != 2 ||
		!strings.Contains(f.Message, "2 AI-generated shots") || !strings.Contains(f.Message, "Google Gemini") || !strings.Contains(f.Message, "YouTube") {
		t.Fatalf("%+v", f)
	}
	r2 := &run{in: Input{Generated: map[string]GeneratedClip{"ast_g": {Provider: "Google Gemini", Likeness: true}}}, fps: 30,
		plan: &editspec.RenderPlan{FPS: 30, Clips: []editspec.PlanClip{{ID: "ada", AssetID: "ast_g", DurationInFrames: 90}}}}
	r2.aiGenerated()
	if len(r2.pend) != 1 || !strings.Contains(r2.pend[0].Message, "real person's likeness") {
		t.Fatalf("likeness: %+v", r2.pend)
	}
	r3 := &run{in: Input{}, fps: 30, plan: &editspec.RenderPlan{FPS: 30, Clips: []editspec.PlanClip{{ID: "me", AssetID: "ast_a", DurationInFrames: 90}}}}
	r3.aiGenerated()
	if len(r3.pend) != 0 {
		t.Fatal("no generated footage, no note")
	}
}

// A presenter reading lines is noted as an AI presenter, with the reminder
// that it is never the person speaking live (D277).
func TestAIPresenterIsNoted(t *testing.T) {
	r := &run{in: Input{Generated: map[string]GeneratedClip{"ast_p": {Provider: "D-ID", Likeness: true, Presenter: true}}}, fps: 30,
		plan: &editspec.RenderPlan{FPS: 30, Clips: []editspec.PlanClip{{ID: "ada", AssetID: "ast_p", From: 0, DurationInFrames: 150}}}}
	r.aiGenerated()
	if len(r.pend) != 1 {
		t.Fatalf("%+v", r.pend)
	}
	f := r.pend[0]
	if f.Kind != "ai_generated" || !strings.Contains(f.Message, "an AI presenter reading lines on camera") || !strings.Contains(f.Message, "D-ID") ||
		!strings.Contains(f.Message, "real person's likeness") || !strings.Contains(f.Message, "never the person speaking live") {
		t.Fatalf("%+v", f)
	}
}

// Changed words are a note while the person's consent holds; once it is
// withdrawn (or the consent behind a matched mouth is), they are a problem:
// the version can't go out until they come out of the cut (D309, D311).
func TestChangedWordsWhoseConsentEndedAreAProblem(t *testing.T) {
	r := &run{in: Input{Said: []SaidFacts{
		{ChangeID: "swc_a", From: 2, To: 2.5, Who: "Ada Lovelace", Words: "fifteen", Before: "twelve"},
		{ChangeID: "swc_b", From: 5, To: 5.5, Who: "Ada Lovelace", Words: "seven", Before: "six", Withdrawn: "Ada Lovelace's consent for new words in their voice was withdrawn."},
		{ChangeID: "swc_c", From: 8, To: 8.5, Who: "Ada Lovelace", Words: "red", Before: "blue", Mouth: true, MouthWithdrawn: "Bo's consent for lip sync, which the matched mouth relied on, is no longer in force."},
	}}, fps: 30, plan: &editspec.RenderPlan{FPS: 30}}
	r.changedWords()
	if len(r.pend) != 3 {
		t.Fatalf("one finding per change: %+v", r.pend)
	}
	if f := r.pend[0]; f.Severity != models.QAInfo || f.Kind != "changed_words" || !strings.Contains(f.Message, "with their consent") {
		t.Fatalf("a change whose consent holds is a note: %+v", f)
	}
	for _, f := range r.pend[1:] {
		if f.Severity != models.QAError || f.Kind != "changed_words_withdrawn" || strings.Contains(f.Message, "with their consent") || !strings.Contains(f.Message, "can't go out") {
			t.Fatalf("a change whose consent ended is a problem: %+v", f)
		}
	}
	if !strings.Contains(r.pend[2].Message, "mouth") {
		t.Fatalf("it says it's the matched mouth: %s", r.pend[2].Message)
	}
}
