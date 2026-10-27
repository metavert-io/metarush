package transcripts

import (
	"reflect"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
)

func seg(words ...models.Word) models.TranscriptSegment {
	s := models.TranscriptSegment{Words: words}
	if len(words) > 0 {
		s.From, s.To = words[0].From, words[len(words)-1].To
		s.Text = JoinWords(words)
	}
	return s
}

func w(text string, from, to float64) models.Word {
	return models.Word{Text: text, From: from, To: to, Score: 0.9}
}

func TestSplit(t *testing.T) {
	for _, c := range []struct{ in, lead, core, trail string }{
		{"Brook,", "", "Brook", ","},
		{`"E.`, `"`, "E", "."},
		{"don't", "", "don't", ""},
		{"Fern-Brook!", "", "Fern-Brook", "!"},
		{"...", "...", "", ""},
		{"(Elmwood's)", "(", "Elmwood's", ")"},
	} {
		l, c2, tr := Split(c.in)
		if l != c.lead || c2 != c.core || tr != c.trail {
			t.Errorf("Split(%q) = %q %q %q", c.in, l, c2, tr)
		}
	}
	if Norm("Fern-Brook,") != "fernbrook" || Norm("Maple Grove") != "maplegrove" || Norm("Brook’s") != "brooks" {
		t.Error("Norm")
	}
}

func TestMatchWholeWords(t *testing.T) {
	segs := []models.TranscriptSegment{
		seg(w("We", 0, 0.2), w("met", 0.25, 0.4), w("Fern.", 0.5, 0.7), w("Brook,", 0.75, 1.0), w("and", 1.1, 1.2)),
		seg(w("fern", 2, 2.1), w("brook", 2.15, 2.4), w("again.", 2.5, 2.9)),
		seg(w("Fernbrook", 4, 4.5), w("flying", 4.6, 5)),
	}
	u := Units(segs)
	got := Match(u, "Fern Brook", MatchOptions{})
	if len(got) != 2 {
		t.Fatalf("matches %+v", got)
	}
	if got[0].I != 2 || got[0].J != 3 || got[0].Pre != "" || got[0].Post != "," {
		t.Errorf("first %+v", got[0])
	}
	if got[1].I != 5 || got[1].J != 6 {
		t.Errorf("second %+v", got[1])
	}
	if m := Match(u, "Fern Brook", MatchOptions{MatchCase: true}); len(m) != 1 {
		t.Errorf("match case: %+v", m)
	}
	// A match spans timed lines (the name was cut in two between lines).
	segs2 := []models.TranscriptSegment{seg(w("met", 0, 0.2), w("Fern", 0.3, 0.4)), seg(w("Brook", 0.45, 0.8))}
	if m := Match(Units(segs2), "fern brook", MatchOptions{}); len(m) != 1 || m[0].I != 1 || m[0].J != 2 {
		t.Errorf("across lines: %+v", m)
	}
	// Whole words don't find parts of words.
	if m := Match(u, "brook", MatchOptions{}); len(m) != 2 {
		t.Errorf("whole word brook: %+v", m)
	}
}

func TestMatchParts(t *testing.T) {
	u := Units([]models.TranscriptSegment{seg(w("Fernbrook", 4, 4.5), w("brooking", 4.6, 5), w("Fern", 5.1, 5.2), w("Brook.", 5.3, 5.6))})
	got := Match(u, "brook", MatchOptions{Parts: true})
	if len(got) != 3 {
		t.Fatalf("parts %+v", got)
	}
	if got[0].Pre != "Fern" || got[0].Post != "" || got[1].Pre != "" || got[1].Post != "ing" || got[2].Post != "." {
		t.Errorf("pre/post %+v", got)
	}
	// Across a space.
	if m := Match(u, "fern b", MatchOptions{Parts: true}); len(m) != 1 || m[0].I != 2 || m[0].J != 3 || m[0].Post != "rook." {
		t.Errorf("across words %+v", m)
	}
}

func TestSpreadKeepsTheOuterTimes(t *testing.T) {
	// Join: one word over both.
	j := Spread([]models.Word{w("Fern", 12.3, 12.52), w("Brook", 12.55, 12.9)}, []string{"FernBrook"})
	if len(j) != 1 || j[0].From != 12.3 || j[0].To != 12.9 || !j[0].Fixed || j[0].Score != 0 {
		t.Fatalf("join %+v", j)
	}
	// Split: the letters say where.
	s := Spread([]models.Word{w("Maplegrove", 5, 5.8)}, []string{"Maple", "Grove"})
	if len(s) != 2 || s[0].From != 5 || s[0].To != 5.4 || s[1].From != 5.4 || s[1].To != 5.8 {
		t.Fatalf("split %+v", s)
	}
	// Same count: one for one.
	o := Spread([]models.Word{w("a", 1, 1.2), w("b", 1.5, 1.7)}, []string{"A", "B"})
	if o[0].From != 1 || o[0].To != 1.2 || o[1].From != 1.5 || o[1].To != 1.7 {
		t.Fatalf("one for one %+v", o)
	}
	// Three into two: never backwards, ends exact.
	x := Spread([]models.Word{w("one", 0, 0.3), w("two", 0.4, 0.7), w("three", 0.8, 1.3)}, []string{"onetwo", "three"})
	if x[0].From != 0 || x[1].To != 1.3 || x[1].From < x[0].To {
		t.Fatalf("three into two %+v", x)
	}
	if Spread([]models.Word{w("x", 0, 1)}, nil) != nil {
		t.Error("nothing")
	}
}

func TestRetypeChangesOnlyWhatDiffers(t *testing.T) {
	old := []models.Word{w("We", 0, 0.2), w("met", 0.25, 0.4), w("Fern", 0.5, 0.7), w("Brook", 0.75, 1.0), w("today.", 1.1, 1.4)}
	bs := Retype(old, "We met FernBrook today.")
	if len(bs) != 1 || bs[0].I != 2 || bs[0].J != 3 || len(bs[0].Words) != 1 || bs[0].Words[0].From != 0.5 || bs[0].Words[0].To != 1.0 {
		t.Fatalf("join %+v", bs)
	}
	// Two separate changes.
	bs = Retype(old, "We saw Fern Brook yesterday.")
	if len(bs) != 2 || bs[0].I != 1 || bs[0].J != 1 || bs[1].I != 4 || bs[1].J != 4 {
		t.Fatalf("two changes %+v", bs)
	}
	// An added word borrows the next word's time.
	bs = Retype(old, "We met Fern Brook here today.")
	if len(bs) != 1 || bs[0].I != 4 || bs[0].J != 4 || len(bs[0].Words) != 2 || bs[0].Words[0].Text != "here" || bs[0].Words[1].Text != "today." {
		t.Fatalf("added %+v", bs)
	}
	// Added at the end borrows the last word.
	bs = Retype(old, "We met Fern Brook today. Really.")
	if len(bs) != 1 || bs[0].I != 4 || bs[0].Words[0].Text != "today." || bs[0].Words[1].Text != "Really." || bs[0].Words[1].To != 1.4 {
		t.Fatalf("added at end %+v", bs)
	}
	// A word taken out.
	bs = Retype(old, "We met Fern Brook.")
	if len(bs) != 1 || bs[0].I != 3 || bs[0].J != 4 || len(bs[0].Words) != 1 || bs[0].Words[0].Text != "Brook." {
		t.Fatalf("removed %+v", bs)
	}
	// No change.
	if bs := Retype(old, "We met Fern Brook today."); len(bs) != 0 {
		t.Fatalf("unchanged %+v", bs)
	}
	// Insertions on both sides of one word share it once.
	two := []models.Word{w("a", 0, 1), w("b", 1, 2)}
	bs = Retype(two, "a X b Y")
	if len(bs) != 1 || bs[0].I != 1 || bs[0].J != 1 || !reflect.DeepEqual(texts(bs[0].Words), []string{"X", "b", "Y"}) {
		t.Fatalf("both sides %+v", bs)
	}
	for i := 1; i < len(bs[0].Words); i++ {
		if bs[0].Words[i].From < bs[0].Words[i-1].To {
			t.Fatalf("backwards %+v", bs[0].Words)
		}
	}
}

func texts(ws []models.Word) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = w.Text
	}
	return out
}

func TestUnitsOfLinesWithoutWordTimes(t *testing.T) {
	segs := []models.TranscriptSegment{{From: 0, To: 3, Text: "Hello Fern Brook there"}}
	u := Units(segs)
	if len(u) != 4 || u[1].Word != -1 || u[1].WordInText != 1 || u[1].From != 0.75 {
		t.Fatalf("units %+v", u)
	}
	if m := Match(u, "fern brook", MatchOptions{}); len(m) != 1 || m[0].I != 1 || m[0].J != 2 {
		t.Fatalf("match %+v", m)
	}
}

// A stretch is the words whose middle is inside it; a moment (--at) is the
// word said then, or in a short pause the nearest one, and nothing far from words.
func TestRangeOfAStretchOrAMoment(t *testing.T) {
	us := Units([]models.TranscriptSegment{seg(w("We", 0.3, 0.5), w("met", 0.5, 0.8), w("Fern", 1.0, 1.2), w("Brook", 1.2, 1.5)), seg(w("again.", 4.0, 4.4))})
	for _, c := range []struct {
		from, to float64
		i, j     int
		ok       bool
	}{
		{0.3, 0.8, 0, 1, true},    // whole words
		{0.45, 1.3, 1, 2, true},   // by their middles: "met" and "Fern" (the middle of "Brook" is 1.35)
		{0.6, 0.6, 1, 1, true},    // a moment inside "met"
		{0.85, 0.85, 1, 1, true},  // a short pause: the nearest word
		{0.95, 0.95, 2, 2, true},  // nearer to "Fern"
		{1.2, 1.2, 2, 2, true},    // where two words meet: the one whose middle is nearer
		{2.5, 2.5, -1, -1, false}, // far from any word
		{2.0, 3.0, -1, -1, false},
	} {
		i, j, ok := Range(us, c.from, c.to)
		if i != c.i || j != c.j || ok != c.ok {
			t.Errorf("Range(%.2f, %.2f) = %d, %d, %v; want %d, %d, %v", c.from, c.to, i, j, ok, c.i, c.j, c.ok)
		}
	}
}
