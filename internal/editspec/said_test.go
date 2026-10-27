package editspec

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

const astSaid = "ast_01jabcdefghjkmnpqrstvwxyz9"

// saidFacts is facts() with a new sound for "built" (1.3–1.8 s of astA):
// the stretch runs from 1.2 to 1.9 with its edges, 0.7 s long.
func saidFacts() fakeFacts {
	f := facts()
	f.assets[astSaid] = AssetFacts{ID: astSaid, Name: "new words", Kind: "audio", State: "available", Duration: 0.7, HasAudio: true}
	return f
}

func saidSpec() *Spec {
	s := baseSpec()
	s.Captions = &Captions{Enabled: true}
	s.Mix = &Mix{Said: []SaidSound{{ID: "swc_01jabcdefghjkmnpqrstvwxyz0", AssetID: astA, From: 1.2, To: 1.9, Sound: astSaid,
		WordsFrom: 1.32, WordsTo: 1.76, Words: "rebuilt", Before: "built", Who: "Ada"}}}
	Normalize(s)
	return s
}

func TestSaidWordsPlayInPlaceOfTheRecordingsOwn(t *testing.T) {
	s := saidSpec()
	f := saidFacts()
	plan := Resolve(s, f, "rev_x", "prj_x", "h")
	var said *PlanAudio
	for i := range plan.Audio {
		if plan.Audio[i].AssetID == astSaid {
			said = &plan.Audio[i]
		}
	}
	if said == nil {
		t.Fatalf("no element plays the new words: %+v", plan.Audio)
	}
	if !said.AIVoice || said.Role != RoleDialogue || said.Bus != BusSource {
		t.Fatalf("the new words must be marked AI speech on the source bus: %+v", said)
	}
	// The hook clip plays astA from 1 s at frame 0 (30 fps): 1.2 s is frame 6, 1.9 s frame 27.
	if said.From != 6 || said.DurationInFrames != 21 || said.TrimBefore != 0 {
		t.Fatalf("new words placed at %d for %d frames (trim %d), want 6 for 21 (trim 0)", said.From, said.DurationInFrames, said.TrimBefore)
	}
	hook := plan.Clips[0]
	_, _, clipGain := hook.Sound()
	if g := GainAt(clipGain, 15); g > 1e-3 {
		t.Fatalf("the recording's own sound still plays under the new words: gain %.3f at frame 15", g)
	}
	if g := GainAt(clipGain, 2); g < 0.5 {
		t.Fatalf("the recording's own sound is gone before the change: gain %.3f at frame 2", g)
	}
	if g := GainAt(said.Gain, 9); g < 0.5 {
		t.Fatalf("the new words are quiet inside their stretch: gain %.3f", g)
	}
	// Over each edge the two sum to the clip's own gain (the same samples play in both).
	for _, f := range []int{6, 7, 8, 25, 26} {
		sum := GainAt(clipGain, float64(f)) + GainAt(said.Gain, float64(f-said.From))
		if math.Abs(sum-GainAt(clipGain, 1)) > 0.05 {
			t.Fatalf("frame %d: the crossfade doesn't add up (%.3f)", f, sum)
		}
	}
	// Captions and the speech check hear the new words.
	var caption []string
	for _, l := range plan.Captions.Lines {
		caption = append(caption, l.Text)
	}
	if got := strings.Join(caption, " "); !strings.Contains(got, "We rebuilt it") || strings.Contains(got, " built") {
		t.Fatalf("captions = %q, want the new words in place of the old", got)
	}
	marked := false
	for _, l := range plan.Captions.Lines {
		for _, w := range l.Words {
			if w.Text == "rebuilt" && w.Src != nil && w.Src.Said {
				marked = true
			}
		}
	}
	if !marked {
		t.Fatal("the new word isn't marked as said in a voice copy")
	}
	words, _, _ := ExpectedSpeech(s, f)
	var heard []string
	for _, w := range words {
		heard = append(heard, w.Text)
	}
	if got := strings.Join(heard, " "); !strings.Contains(got, "We rebuilt it twice.") {
		t.Fatalf("the speech check expects %q", got)
	}
	// The new sound is one of the things the cut plays.
	found := false
	for _, id := range AssetIDs(s) {
		found = found || id == astSaid
	}
	if !found {
		t.Fatal("AssetIDs leaves out the new words")
	}
	if issues := Validate(s, f, nil, false); HasErrors(issues) {
		t.Fatalf("unexpected problems: %v", issues)
	}
}

func TestSaidWordsKeepTheMixAsItWas(t *testing.T) {
	// Adding changed words to a cut without a mix keeps the original mixer
	// (no automatic ducking or loudness): only the stretch changes.
	plain := baseSpec()
	pp := Resolve(plain, saidFacts(), "r", "p", "h")
	s := saidSpec()
	ps := Resolve(s, saidFacts(), "r", "p", "h")
	if (pp.Mix == nil) != (ps.Mix == nil) || (ps.Mix != nil && ps.Mix.Normalize != pp.Mix.Normalize) {
		t.Fatalf("changed words switched the mixer: %+v vs %+v", pp.Mix, ps.Mix)
	}
	_, _, gp := pp.Clips[0].Sound()
	_, _, gs := ps.Clips[0].Sound()
	for _, f := range []float64{0, 3, 40, 80} {
		if math.Abs(GainAt(gp, f)-GainAt(gs, f)) > 1e-6 {
			t.Fatalf("frame %v outside the change sounds different: %v vs %v", f, GainAt(gp, f), GainAt(gs, f))
		}
	}
	// Older cuts keep their exact JSON (D73): the field is left out when empty.
	b, _ := json.Marshal(plain)
	if strings.Contains(string(b), "said") {
		t.Fatalf("a cut without changed words mentions them: %s", b)
	}
}

func TestSaidWordsAreChecked(t *testing.T) {
	f := saidFacts()
	s := saidSpec()
	s.Mix.Said[0].To = 2.5 // the sound is 0.7 s long
	issues := Validate(s, f, nil, false)
	if !hasCode(issues, "bad_said") {
		t.Fatalf("a new sound that doesn't fit its stretch must be refused: %v", issues)
	}
	s = saidSpec()
	s.Clips[0].Out = 1.5 // the cut ends inside the new words
	if issues := Validate(s, f, nil, false); !hasCode(issues, "said_cut") {
		t.Fatalf("a cut inside the new words must be said: %v", issues)
	}
	s = saidSpec()
	s.Clips[0].In, s.Clips[0].Out = 2.5, 4
	if issues := Validate(s, f, nil, false); !hasCode(issues, "said_unused") {
		t.Fatalf("changed words the cut doesn't play must be said: %v", issues)
	}
	s = saidSpec()
	delete(f.assets, astSaid)
	if issues := Validate(s, f, nil, false); !hasCode(issues, "missing_asset") {
		t.Fatalf("missing new words must be refused: %v", issues)
	}
	// Without the sound the clip keeps its own (nothing is silenced).
	plan := Resolve(s, f, "r", "p", "h")
	_, _, g := plan.Clips[0].Sound()
	if GainAt(g, 15) < 0.5 {
		t.Fatal("the recording went quiet although the new words are missing")
	}
}

func TestSaidWordsSpreadByLetters(t *testing.T) {
	ws := spreadWords("a hundred", 1, 2)
	if len(ws) != 2 || !ws[0].Said || ws[0].From != 1 || ws[1].To != 2 || ws[0].To >= ws[1].To || ws[0].To > 1.2 {
		t.Fatalf("spread = %+v", ws)
	}
}

func hasCode(issues []Issue, code string) bool {
	for _, i := range issues {
		if i.Code == code {
			return true
		}
	}
	return false
}
