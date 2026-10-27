package editspec

import (
	"encoding/json"
	"os"
	"testing"
)

// Auto splits on real word timings: Parakeet's transcript of one generation of
// the two-speaker fixture (scripts/make-fixtures.sh;
// testdata/two-speakers-words.json) and that same generation's sample-exact
// turn boundaries (testdata/two-speakers-truth.json, the ground truth written
// with it). The two are kept together: tests/fixtures/truth is rewritten by
// every computer that makes the fixtures, a few milliseconds off (another
// voice, many). The picture cuts inside a word; auto must move the sound
// change into the real silence between the turns and cut no word.
func TestAutoSplitOnFixtureWordTimings(t *testing.T) {
	var words struct {
		Segments []struct {
			From, To float64
			Speaker  string
			Text     string
			Words    []struct {
				Text     string
				From, To float64
			}
		}
	}
	b, err := os.ReadFile("testdata/two-speakers-words.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &words); err != nil {
		t.Fatal(err)
	}
	var truth struct {
		Turns []struct {
			Speaker string  `json:"speaker"`
			Start   float64 `json:"start_s"`
			EndS    float64 `json:"end_s"`
		} `json:"turns"`
	}
	tb, err := os.ReadFile("testdata/two-speakers-truth.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(tb, &truth); err != nil {
		t.Fatal(err)
	}
	const ast = "ast_01jabcdefghjkmnpqrstvwxyz6"
	f := facts()
	f.assets[ast] = AssetFacts{ID: ast, Name: "two-speakers", State: "available", InProject: true, Duration: 29, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, TranscriptPrecision: "word"}
	for _, s := range words.Segments {
		seg := TranscriptSegment{From: s.From, To: s.To, Text: s.Text, Speaker: s.Speaker}
		for _, w := range s.Words {
			seg.Words = append(seg.Words, TranscriptWord{Text: w.Text, From: w.From, To: w.To})
		}
		f.tx[ast] = append(f.tx[ast], seg)
	}
	questionEnd, answerStart := truth.Turns[0].EndS, truth.Turns[1].Start // the silence between them
	if gap := answerStart - questionEnd; gap < 0.5 || gap > 0.7 {
		t.Fatalf("the turns' silence is %.3f s, not the fixture's 0.6 s", gap)
	}
	cases := []struct {
		name      string
		aOut, bIn float64
		want      string
	}{
		// The question's picture holds into the pause; the answer's picture starts inside "It".
		{"answer starts inside a word", questionEnd + 0.36, answerStart - 0.02, SoundLead},
		// The question's picture cuts inside "started."; the answer's starts in the pause.
		{"question cut inside a word", questionEnd - 0.14, questionEnd - 0.04, SoundCarry},
	}
	for _, tc := range cases {
		s := &Spec{Output: Output{Preset: PresetLandscape, FPS: 30}, Clips: []Clip{
			{ID: "question", AssetID: ast, In: 0.3, Out: tc.aOut},
			{ID: "answer", AssetID: ast, In: tc.bIn, Out: 7.0, Transition: &Transition{Type: TransitionCut, Audio: &SoundTransition{Mode: SoundAuto}}},
		}}
		Normalize(s)
		plan := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
		ta := plan.Clips[1].TransitionIn.Audio
		if ta == nil || ta.Result != tc.want || ta.NoPause {
			t.Fatalf("%s: %+v", tc.name, ta)
		}
		q, a := plan.Clips[0], plan.Clips[1]
		qSrc := q.SourceIn + float64(ta.EditFrame-q.From)/30 // the question's sound starts fading here
		aStart := a.SourceIn + float64(ta.EditFrame-a.From)/30
		aFull := a.SourceIn + float64(ta.EditFrame+ta.BlendFrames-a.From)/30 // the answer is at full level here
		if qSrc < questionEnd || aFull > answerStart {
			t.Fatalf("%s: the sound changes at %.3f s (question side) / %.3f–%.3f s (answer side), outside the silence %.2f–%.2f s", tc.name, qSrc, aStart, aFull, questionEnd, answerStart)
		}
		t.Logf("%s: %s %.2f s; the question fades from %.3f s, the answer reaches full level at %.3f s (silence %.2f–%.2f s)", tc.name, ta.Result, float64(ta.EditFrame-ta.CutFrame)/30, qSrc, aFull, questionEnd, answerStart)
		for _, ci := range CutReport(s, f) {
			if len(ci.Clipped) > 0 || len(ci.DroppedPrev) > 0 || len(ci.DroppedNext) > 0 {
				t.Fatalf("%s: words cut or lost: %+v", tc.name, ci)
			}
		}
	}
}
