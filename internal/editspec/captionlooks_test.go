package editspec

import (
	"reflect"
	"strings"
	"testing"
)

// The looks (D245): six, each a value the spec's style takes, the first three
// drawn as before; a look's words per line fill an unset line; a look that
// shows the spoken word falls back without word timings; the plan keeps the
// look; QA's house style difference names what differs.
func TestCaptionLooks(t *testing.T) {
	if len(CaptionLooks) < 4 || len(CaptionLooks) > 6 {
		t.Fatalf("four to six looks, got %d", len(CaptionLooks))
	}
	field, _ := reflect.TypeOf(Captions{}).FieldByName("Style")
	plan, _ := reflect.TypeOf(PlanCaptions{}).FieldByName("Style")
	house, _ := reflect.TypeOf(StyleCaptions{}).FieldByName("Look")
	seen := map[string]bool{}
	for _, l := range CaptionLooks {
		if seen[l.ID] || l.Name == "" || l.Summary == "" || l.LineHeight <= 0 || l.Scale <= 0 || l.WordsPerLine < 1 || l.WordsPerLine > 12 {
			t.Errorf("look %+v", l)
		}
		seen[l.ID] = true
		for _, tag := range []reflect.StructTag{field.Tag, plan.Tag, house.Tag} {
			if !strings.Contains(tag.Get("jsonschema"), "enum="+l.ID) {
				t.Errorf("%s is missing from %s", l.ID, tag)
			}
		}
		if l.WordTimed && (l.Fallback == "" || CaptionLook(l.Fallback).WordTimed) {
			t.Errorf("%s needs a fallback that doesn't need word timings", l.ID)
		}
	}
	// The first three are drawn as they always were (QA's geometry, D73).
	for id, lh := range map[string]float64{CaptionClean: 1.2, CaptionEmphasis: 1.2, CaptionBoxed: 1.38} {
		if l := CaptionLook(id); l.LineHeight != lh || l.Scale != 1 || l.WordsPerLine != 6 {
			t.Errorf("%s changed: %+v", id, l)
		}
	}
	if CaptionLook("").ID != CaptionClean || CaptionLook("nope").ID != CaptionClean || IsCaptionLook("nope") {
		t.Error("unknown looks are clean")
	}

	// Words per line: an unset line takes the look's.
	s := baseSpec()
	s.Captions = &Captions{Enabled: true, Style: CaptionBold}
	Normalize(s)
	if s.Captions.MaxWordsPerLine != 3 {
		t.Fatalf("bold holds 3 words a line: %d", s.Captions.MaxWordsPerLine)
	}
	s.Captions = &Captions{Enabled: true}
	Normalize(s)
	if s.Captions.Style != CaptionClean || s.Captions.MaxWordsPerLine != 6 {
		t.Fatalf("defaults as before: %+v", s.Captions)
	}
	// Changing the look brings its words per line unless someone chose them.
	SetCaptionLook(s.Captions, CaptionSubtitle)
	if s.Captions.Style != CaptionSubtitle || s.Captions.MaxWordsPerLine != 9 {
		t.Fatalf("subtitle: %+v", s.Captions)
	}
	s.Captions.MaxWordsPerLine = 5
	SetCaptionLook(s.Captions, CaptionBold)
	if s.Captions.MaxWordsPerLine != 5 {
		t.Fatalf("a chosen words per line stays: %+v", s.Captions)
	}

	// The plan keeps the look with word timings and falls back without.
	s = baseSpec()
	s.Captions = &Captions{Enabled: true, Style: CaptionHighlight}
	Normalize(s)
	f := facts()
	if p := Resolve(s, f, "", "", ""); p.Captions == nil || p.Captions.Style != CaptionHighlight || p.Captions.Precision != "word" {
		t.Fatalf("word-timed: %+v", p.Captions)
	}
	a := f.assets[astA]
	a.TranscriptPrecision = "segment"
	f.assets[astA] = a
	if p := Resolve(s, f, "", "", ""); p.Captions.Style != CaptionBoxed {
		t.Fatalf("highlight without word timings is boxed: %+v", p.Captions.Style)
	}
	var warned bool
	for _, is := range Validate(s, f, nil, false) {
		if is.Code == "captions_downgraded" && strings.Contains(is.Message, "“A box follows the spoken word”") && strings.Contains(is.Message, "“Boxed”") {
			warned = true
		}
	}
	if !warned {
		t.Error("the check says the look falls back")
	}

	// How captions differ from the house style.
	h := &HouseStyle{Captions: &StyleCaptions{Look: CaptionBold, Position: "top"}}
	nc, says := CaptionStyleDiff(&Captions{Enabled: true, Style: CaptionClean, MaxWordsPerLine: 6, Position: "lower"}, h)
	if nc == nil || nc.Style != CaptionBold || nc.MaxWordsPerLine != 3 || nc.Position != "top" || len(says) != 2 || !strings.Contains(says[0], "“Big and bold”") {
		t.Fatalf("differences: %+v %v", nc, says)
	}
	if nc, _ := CaptionStyleDiff(&Captions{Enabled: true, Style: CaptionBold, Position: "top"}, h); nc != nil {
		t.Fatalf("the same: %+v", nc)
	}
	if nc, _ := CaptionStyleDiff(&Captions{Enabled: false, Style: CaptionClean}, h); nc != nil {
		t.Fatal("captions off are the other check's")
	}
	if h := (&HouseStyle{Captions: &StyleCaptions{Look: "glitter"}}); !strings.Contains(h.Problem(), "subtitle") {
		t.Errorf("an unknown house look: %q", h.Problem())
	}
}
