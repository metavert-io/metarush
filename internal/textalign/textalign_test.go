package textalign

import (
	"strings"
	"testing"
)

func timed(text string, start, per float64, group int) []Word {
	var out []Word
	t := start
	for _, w := range Tokens(text) {
		out = append(out, Word{Text: w, Norm: Normalize(w), From: t, To: t + per*0.8, Group: group})
		t += per
	}
	return out
}

func TestNormalizeAndSimilar(t *testing.T) {
	if Normalize("Twice.") != "twice" || Normalize("“Don’t”") != "don't" || Normalize("—") != "" {
		t.Fatal("normalize")
	}
	for _, c := range [][2]string{{"twice", "twice"}, {"northern", "northen"}, {"recording", "recordin"}, {"cut", "cuts"}, {"maren", "maren's"}, {"lofoten", "lofotin"}} {
		if !Similar(c[0], c[1]) {
			t.Errorf("%s ~ %s should be similar", c[0], c[1])
		}
	}
	for _, c := range [][2]string{{"cat", "hat"}, {"light", "night"}, {"we", "us"}, {"silence", "science"}} {
		if Similar(c[0], c[1]) {
			t.Errorf("%s / %s must differ", c[0], c[1])
		}
	}
}

func TestAlignIgnoresFillersAndNoise(t *testing.T) {
	exp := FromText("We came for the northern lights and stayed for the silence.", 0)
	obs := FromText("we came for um the northen lights and uh stayed for the silence", 0)
	steps := Align(exp, obs, 0)
	m, total := Score(exp, steps)
	if m != total {
		t.Fatalf("expected a full match, got %d/%d: %v", m, total, steps)
	}
	if iss := Continuity(exp, obs, steps, DefaultContinuity()); len(iss) != 0 {
		t.Fatalf("noise must not be reported: %+v", iss)
	}
}

func TestContinuityRepeatedWord(t *testing.T) {
	exp := timed("every morning starts in the dark out here", 10, 0.4, 1)
	obs := timed("every morning starts starts in the dark out here", 10, 0.4, 0)
	steps := Align(exp, obs, 2)
	iss := Continuity(exp, obs, steps, DefaultContinuity())
	if len(iss) != 1 || iss[0].Kind != Repeated || strings.Join(iss[0].Words, " ") != "starts" || iss[0].Phrase {
		t.Fatalf("repeated word: %+v", iss)
	}
	if iss[0].From < 10.7 || iss[0].From > 11.3 {
		t.Fatalf("repeat time %.2f", iss[0].From)
	}
}

func TestContinuityRepeatedPhraseAndFragment(t *testing.T) {
	exp := timed("and stayed for the silence every morning", 0, 0.4, 0)
	obs := timed("and stayed for the and stayed for the silence every morning", 0, 0.4, 0)
	iss := Continuity(exp, obs, Align(exp, obs, 0), DefaultContinuity())
	if len(iss) != 1 || iss[0].Kind != Repeated || !iss[0].Phrase {
		t.Fatalf("repeated phrase: %+v", iss)
	}
	// A clipped word heard twice ("nor northern").
	exp = timed("the northern lights", 0, 0.4, 0)
	obs = timed("the nor northern lights", 0, 0.4, 0)
	iss = Continuity(exp, obs, Align(exp, obs, 0), DefaultContinuity())
	if len(iss) != 1 || iss[0].Kind != Repeated {
		t.Fatalf("fragment repeat: %+v", iss)
	}
}

func TestContinuityMissingAndUnexpected(t *testing.T) {
	exp := append(timed("we came for the northern lights", 0, 0.4, 0), timed("and stayed for the silence", 3, 0.4, 1)...)
	obs := timed("we came for the northern lights", 0, 0.4, 0)
	iss := Continuity(exp, obs, Align(exp, obs, 1.5), DefaultContinuity())
	if len(iss) != 1 || iss[0].Kind != Missing || !iss[0].Phrase || iss[0].Group != 1 {
		t.Fatalf("a dropped phrase: %+v", iss)
	}
	obs = append(timed("we came for the northern lights", 0, 0.4, 0), timed("buy one get one free", 2.6, 0.3, 0)...)
	obs = append(obs, timed("and stayed for the silence", 4.2, 0.4, 0)...)
	exp2 := append(timed("we came for the northern lights", 0, 0.4, 0), timed("and stayed for the silence", 4.2, 0.4, 1)...)
	iss = Continuity(exp2, obs, Align(exp2, obs, 1.5), DefaultContinuity())
	if len(iss) != 1 || iss[0].Kind != Unexpected || len(iss[0].Words) != 5 {
		t.Fatalf("unexpected speech: %+v", iss)
	}
	// One clipped word at a cut is not a finding.
	exp3 := timed("this is where it begins", 0, 0.4, 0)
	obs3 := timed("this is where it", 0, 0.4, 0)
	o := DefaultContinuity()
	o.MinMissing = 1
	o.Edges = []float64{1.6}
	if iss := Continuity(exp3, obs3, Align(exp3, obs3, 1), o); len(iss) != 0 {
		t.Fatalf("clipped word at a cut: %+v", iss)
	}
}

func TestAlignTimeSkewPreventsFarMatches(t *testing.T) {
	exp := timed("hello world", 0, 0.5, 0)
	obs := timed("hello world", 30, 0.5, 0)
	steps := Align(exp, obs, 2)
	for _, s := range steps {
		if s.Kind == Match {
			t.Fatalf("words 30 s apart matched: %v", steps)
		}
	}
}

// Pieces of Japanese, Chinese and Thai join with nothing between them, also
// next to a Latin word or a number; other words keep their space (D244).
func TestJoinWords(t *testing.T) {
	for _, c := range []struct {
		in   []string
		want string
	}{
		{[]string{"Hola", "amigos."}, "Hola amigos."},
		{[]string{"人魚", "は、", "南の", "方の", "海に"}, "人魚は、南の方の海に"},
		{[]string{"LibriVox", "の", "録音"}, "LibriVoxの録音"},
		{[]string{"2020", "年"}, "2020年"},
		{[]string{"ロウソク", "と", "人魚"}, "ロウソクと人魚"},
		{[]string{"我们", "走吧"}, "我们走吧"},
		{[]string{"สวัสดี", "ครับ"}, "สวัสดีครับ"},
		{[]string{"안녕하세요", "여러분"}, "안녕하세요 여러분"},
		{[]string{"", "a", "", "b"}, "a b"},
	} {
		if got := JoinWords(c.in); got != c.want {
			t.Errorf("%q: %q, want %q", c.in, got, c.want)
		}
	}
}
