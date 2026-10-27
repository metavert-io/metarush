package editspec

import (
	"math"
	"strings"
	"testing"
)

// A synthetic read: words at known times, an envelope that is loud during
// words and quiet in the pauses.
func readFixture(spoken [][3]any, dur float64) ([]TranscriptWord, *Envelope) {
	var words []TranscriptWord
	for _, w := range spoken {
		words = append(words, TranscriptWord{Text: w[0].(string), From: w[1].(float64), To: w[2].(float64)})
	}
	env := &Envelope{Step: 0.01}
	for t := 0.0; t < dur; t += env.Step {
		v := 0.0005 // room noise
		for _, w := range words {
			if t >= w.From && t < w.To {
				v = 0.2
			}
		}
		env.RMS = append(env.RMS, v)
	}
	return words, env
}

func beatsOf(texts ...string) []Beat {
	var out []Beat
	for i, t := range texts {
		out = append(out, Beat{ID: "line-" + string(rune('1'+i)), Text: t, Anchor: Anchor{Kind: AnchorAfter}})
	}
	return out
}

// Every line found gets a piece that starts in the pause before it, just
// before its first word; the pieces follow each other without a gap, and no
// cut falls inside a word.
func TestSplitReadCutsOnlyInPauses(t *testing.T) {
	beats := beatsOf("Every morning starts in the kitchen.", "Then the garden wakes up.", "And by noon, everything is green.")
	words, env := readFixture([][3]any{
		{"Every", 2.00, 2.30}, {"morning", 2.32, 2.70}, {"starts", 2.74, 3.05}, {"in", 3.07, 3.15}, {"the", 3.17, 3.25}, {"kitchen.", 3.27, 3.80},
		{"Then", 5.30, 5.50}, {"the", 5.52, 5.60}, {"garden", 5.62, 6.00}, {"wakes", 6.02, 6.30}, {"up.", 6.32, 6.60},
		{"And", 8.10, 8.25}, {"by", 8.27, 8.40}, {"noon,", 8.42, 8.80}, {"everything", 9.00, 9.50}, {"is", 9.52, 9.60}, {"green.", 9.62, 10.10},
	}, 12)
	sp := SplitRead(beats, words, env, 12)
	if len(sp.Lines) != 3 {
		t.Fatalf("lines: %+v", sp.Lines)
	}
	starts := []float64{2.00, 5.30, 8.10}
	for i, l := range sp.Lines {
		if !l.Found {
			t.Fatalf("line %d not found: %+v", i+1, l)
		}
		// The piece starts in the pause, at most a breath (60 ms) before the first word.
		if d := starts[i] - l.In; d < 0 || d > readLead+0.011 {
			t.Errorf("line %d starts at %.3f, %.3f s before its first word (want 0–%.2f)", i+1, l.In, d, readLead)
		}
		if i > 0 && math.Abs(sp.Lines[i-1].Out-l.In) > 1e-9 {
			t.Errorf("a gap between lines %d and %d: %.3f → %.3f", i, i+1, sp.Lines[i-1].Out, l.In)
		}
		// Never inside a word.
		for _, w := range words {
			if l.In > w.From+1e-9 && l.In < w.To-1e-9 {
				t.Errorf("line %d's cut %.3f is inside %q", i+1, l.In, w.Text)
			}
		}
	}
	if last := sp.Lines[2]; math.Abs(last.Out-(10.10+readTail)) > 0.011 {
		t.Errorf("the read ends at %.3f, want just after the last word (%.2f)", last.Out, 10.10+readTail)
	}
	if sp.WPM < 100 || sp.WPM > 300 || sp.Coverage < 0.99 {
		t.Errorf("pace %.0f, coverage %.2f", sp.WPM, sp.Coverage)
	}
}

// The transcript's word times can be off by a few hundredths: the cut goes
// where the sound really starts (the envelope), not inside the word.
func TestSplitReadFollowsTheSound(t *testing.T) {
	beats := beatsOf("One line here.", "Another line there.")
	words, env := readFixture([][3]any{{"One", 1.0, 1.3}, {"line", 1.32, 1.6}, {"here.", 1.62, 2.0}, {"Another", 3.0, 3.4}, {"line", 3.42, 3.7}, {"there.", 3.72, 4.1}}, 6)
	// The transcript says the second line starts 70 ms later than it does.
	words[3].From = 3.07
	sp := SplitRead(beats, words, env, 6)
	if in := sp.Lines[1].In; in > 3.0 || in < 3.0-readLead-0.011 {
		t.Errorf("the cut is at %.3f: the sound starts at 3.000", in)
	}
}

// A line that isn't heard has no piece: its words (if any were said) stay in
// the line before; the lines around it keep theirs.
func TestSplitReadLineNotHeard(t *testing.T) {
	beats := beatsOf("The light arrives slowly.", "Something completely different is said here.", "And then it is everywhere.")
	words, env := readFixture([][3]any{{"The", 1.0, 1.2}, {"light", 1.22, 1.5}, {"arrives", 1.52, 1.9}, {"slowly.", 1.92, 2.4},
		{"And", 4.0, 4.2}, {"then", 4.22, 4.4}, {"it", 4.42, 4.5}, {"is", 4.52, 4.6}, {"everywhere.", 4.62, 5.3}}, 7)
	sp := SplitRead(beats, words, env, 7)
	if !sp.Lines[0].Found || sp.Lines[1].Found || !sp.Lines[2].Found {
		t.Fatalf("found: %v %v %v", sp.Lines[0].Found, sp.Lines[1].Found, sp.Lines[2].Found)
	}
	if sp.Lines[0].Out != sp.Lines[2].In {
		t.Errorf("lines 1 and 3 should meet in the pause: %.3f / %.3f", sp.Lines[0].Out, sp.Lines[2].In)
	}
}

// Without the loudness, the cut is still between the words.
func TestSplitReadWithoutEnvelope(t *testing.T) {
	beats := beatsOf("First thing.", "Second thing.")
	words, _ := readFixture([][3]any{{"First", 1.0, 1.4}, {"thing.", 1.42, 1.9}, {"Second", 2.5, 2.9}, {"thing.", 2.92, 3.4}}, 4)
	sp := SplitRead(beats, words, nil, 4)
	if in := sp.Lines[1].In; in < 1.9 || in > 2.5 {
		t.Errorf("cut at %.3f, want inside the pause 1.9–2.5", in)
	}
}

// As-is scripts (D86): paragraphs, else one line per row, else sentences; no
// timing assumed (every line follows the one before), markup still places a line.
func TestParseScriptAsIs(t *testing.T) {
	s := &Spec{Output: Output{FPS: 30}, Clips: []Clip{{ID: "kitchen", Label: "Kitchen"}, {ID: "garden"}}}
	cases := []struct {
		text string
		want []string
	}{
		{"First paragraph.\nStill the first.\n\nSecond paragraph.", []string{"First paragraph. Still the first.", "Second paragraph."}},
		{"One line.\nAnother line.\nA third line.", []string{"One line.", "Another line.", "A third line."}},
		{"A long sentence that was wrapped\nat a fixed width by an email client\nand continues here. Then another one.", []string{"A long sentence that was wrapped at a fixed width by an email client and continues here.", "Then another one."}},
		{"We met Dr. Smith at 9. Then we left! Did it work? Yes.", []string{"We met Dr. Smith at 9.", "Then we left!", "Did it work?", "Yes."}},
		{"# a comment\nOnly this.", []string{"Only this."}},
	}
	for _, c := range cases {
		beats, issues := ParseScriptAsIs(c.text, s)
		if len(issues) > 0 {
			t.Fatalf("%q: %v", c.text, issues)
		}
		var got []string
		for _, b := range beats {
			got = append(got, b.Text)
			if b.Anchor.Kind != AnchorAfter {
				t.Errorf("%q: line %q is placed (%s); as-is lines follow each other", c.text, b.Text, b.Anchor.Kind)
			}
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%q:\n got %q\nwant %q", c.text, got, c.want)
		}
	}
	beats, _ := ParseScriptAsIs("[@garden+0.5] (warm) The garden.\n\nNext.", s)
	if beats[0].Anchor.Kind != AnchorClip || beats[0].Anchor.ClipID != "garden" || beats[0].Delivery != "warm" || beats[1].Anchor.Kind != AnchorAfter {
		t.Errorf("markup: %+v", beats)
	}
}

// A live read placed where it was spoken plays as one audio element while its
// pieces still follow each other; a line moved by an edit plays on its own.
func TestLiveReadPlaysAsOnePiece(t *testing.T) {
	asset := "ast_01jabcdefghjkmnpqrstvwxyz3"
	s := &Spec{SchemaVersion: 1, Output: Output{Preset: PresetLandscape, FPS: 30}, Clips: []Clip{
		{ID: "a", AssetID: "ast_01jabcdefghjkmnpqrstvwxyz0", In: 0, Out: 6}, {ID: "b", AssetID: "ast_01jabcdefghjkmnpqrstvwxyz1", In: 0, Out: 6}}}
	Normalize(s)
	clips, _ := Timeline(s)
	take := func(in, out float64) *TakeRef {
		return &TakeRef{TakeID: "tk_01jabcdefghjkmnpqrstvwxyz5", AssetID: asset, In: in, Out: out, Source: "recorded"}
	}
	// Recorded from 1.0 s on the timeline: pieces 0.9–3.4, 3.4–6.25, 6.25–8.0.
	pins := []float64{1.0 + 0.9, 1.0 + 3.4, 1.0 + 6.25}
	s.Voiceover = &Voiceover{Beats: []Beat{
		{ID: "l1", Text: "one", Anchor: PinAnchor(s, clips, pins[0]), Take: take(0.9, 3.4)},
		{ID: "l2", Text: "two", Anchor: PinAnchor(s, clips, pins[1]), Take: take(3.4, 6.25)},
		{ID: "l3", Text: "three", Anchor: PinAnchor(s, clips, pins[2]), Take: take(6.25, 8.0)},
	}}
	if a := s.Voiceover.Beats[2].Anchor; a.Kind != AnchorClip || a.ClipID != "b" || math.Abs(a.Offset-1.25) > 1e-9 {
		t.Fatalf("line 3's pin: %+v (want 1.25 s after shot b starts)", a)
	}
	plan := Resolve(Clone(s), nil, "rev", "prj", "")
	var vo []PlanAudio
	for _, a := range plan.Audio {
		if a.Role == RoleVoiceover {
			vo = append(vo, a)
		}
	}
	if len(vo) != 1 || len(vo[0].BeatIDs) != 3 || vo[0].From != 57 || vo[0].TrimBefore != 27 {
		t.Fatalf("one element for the whole read: %+v", vo)
	}
	// An edit lengthens shot a: line 3 (tied to shot b) moves later and plays on its own.
	s.Clips[0].Out = 7
	plan = Resolve(Clone(s), nil, "rev", "prj", "")
	vo = vo[:0]
	for _, a := range plan.Audio {
		if a.Role == RoleVoiceover {
			vo = append(vo, a)
		}
	}
	if len(vo) != 2 || len(vo[0].BeatIDs) != 2 || vo[1].BeatID != "l3" || vo[1].From != int(math.Round((pins[2]+1)*30)) {
		t.Fatalf("after the edit: %+v", vo)
	}
}
