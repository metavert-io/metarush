package recipes

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

const (
	astA = "ast_01aaaaaaaaaaaaaaaaaaaaaaaa"
	astB = "ast_01bbbbbbbbbbbbbbbbbbbbbbbb"
	astC = "ast_01cccccccccccccccccccccccc"
	resA = "res_01aaaaaaaaaaaaaaaaaaaaaaaa"
	resB = "res_01bbbbbbbbbbbbbbbbbbbbbbbb"
)

// doc is a recipe: an intro (fixed), moments (chosen), an end (fixed), a
// title card over the moments, an end card over the end, two lines.
func doc() *models.RecipeDoc {
	return &models.RecipeDoc{Format: models.RecipeFormat, Title: "Event recap", Lands: models.RecipeLanding{Kind: models.RecipeLandsNewProject},
		Inputs: []models.RecipeInput{
			{Name: "footage", Kind: models.RecipeFootage, Default: models.RecipeValue{Items: []string{astB}}},
			{Name: "headline", Kind: models.RecipeWords, Default: models.RecipeValue{Text: "Gala night"}, MaxLength: 80},
			{Name: "art", Kind: models.RecipePictures, Default: models.RecipeValue{Items: []string{resA}}},
			{Name: "voice", Kind: models.RecipeVoice, Default: models.RecipeValue{Text: "Ada", Items: []string{"voice_ada"}}},
		},
		Sections: []models.RecipeSection{
			{Name: "intro", Kind: models.RecipeFixed, Clips: []editspec.Clip{{ID: "intro", AssetID: astA, In: 0, Out: 3}}},
			{Name: "moments", Kind: models.RecipeChosen, From: "footage", Purpose: "lively moments", Count: &models.RecipeRange{Min: 2, Max: 4}, Length: &models.RecipeRange{Min: 8, Max: 12},
				Clips: []editspec.Clip{{ID: "m1", AssetID: astB, In: 1, Out: 4}, {ID: "m2", AssetID: astB, In: 6, Out: 9}, {ID: "m3", AssetID: astB, In: 10, Out: 12}}},
			{Name: "end", Kind: models.RecipeFixed, Clips: []editspec.Clip{{ID: "outro", AssetID: astA, In: 3, Out: 5}}},
		},
		Cut: models.RecipeCut{Output: editspec.Output{Preset: editspec.PresetVertical, FPS: 30},
			Overlays: []editspec.Overlay{
				{ID: "title", Template: editspec.TplTitleCard, At: 0.5, Duration: 2, Params: json.RawMessage(`{"title":"Gala night"}`), Span: &editspec.OverlaySpan{From: "m2", To: "m2"}},
				{ID: "end", Template: editspec.TplEndCard, At: 0, Duration: 2, Params: json.RawMessage(`{"headline":"Gala night","image":"art"}`), Span: &editspec.OverlaySpan{From: "outro", To: "outro"}}},
			Resources: map[string]editspec.ResourcePin{"art": {ID: resA, SHA256: strings.Repeat("a", 64)}},
			Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{
				{ID: "l1", Text: "What a night.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "m1"}, Take: &editspec.TakeRef{AssetID: astC, Out: 2, Source: "tts"}},
				{ID: "l2", Text: "See you next year.", Anchor: editspec.Anchor{Kind: editspec.AnchorAfter}, Voice: &editspec.VoiceRef{Provider: "elevenlabs", VoiceID: "voice_cleo", Name: "Cleo"},
					Take: &editspec.TakeRef{AssetID: astC, Out: 2, Source: "tts"}}}}},
		Bindings: []models.RecipeBinding{{Input: "headline", To: "param:title.title"}, {Input: "headline", To: "param:end.headline"}, {Input: "art", To: "resource:art"}, {Input: "voice", To: "voice"}},
	}
}

func values(changed map[string]bool, items map[string][]models.RecipeRunItem, texts map[string]string) map[string]*models.RecipeRunInput {
	out := map[string]*models.RecipeRunInput{}
	for _, in := range doc().Inputs {
		v := &models.RecipeRunInput{Name: in.Name, Kind: in.Kind, Text: in.Default.Text, Changed: changed[in.Name]}
		for _, id := range in.Default.Items {
			v.Items = append(v.Items, models.RecipeRunItem{ID: id, Duration: 12, SHA256: strings.Repeat("a", 64)})
		}
		if x, ok := items[in.Name]; ok {
			v.Items = x
		}
		if t, ok := texts[in.Name]; ok {
			v.Text = t
		}
		out[in.Name] = v
	}
	return out
}

func TestBindingsAndShape(t *testing.T) {
	for _, s := range []string{"clip:intro", "param:end.headline", "resource:art", "words:intro.OPEN.title", "pictures:intro.ART", "line:l1", "voice", "title", "prompt:boat"} {
		b, ok := ParseBinding(s)
		if !ok || b.String() != s {
			t.Errorf("%s → %+v", s, b)
		}
	}
	for _, s := range []string{"", "param:end", "clip:", "nope:x", "words:intro"} {
		if _, ok := ParseBinding(s); ok {
			t.Errorf("%q should not parse", s)
		}
	}
	if is := shapeIssues(doc()); len(is) != 0 {
		t.Fatalf("a good recipe: %+v", is)
	}
	d := doc()
	d.Inputs[1].Name = "Bad Name"
	d.Bindings = append(d.Bindings, models.RecipeBinding{Input: "art", To: "param:title.title"}, models.RecipeBinding{Input: "ghost", To: "title"})
	d.Sections[1].From = "headline"
	codes := map[string]bool{}
	for _, is := range shapeIssues(d) {
		codes[is.Code] = true
	}
	for _, c := range []string{"input_name", "binding_kind", "binding_input", "section_from"} {
		if !codes[c] {
			t.Errorf("missing %s: %v", c, codes)
		}
	}
	// The version is the document, whatever the spacing in a graphic's fields.
	a, b := doc(), doc()
	b.Cut.Overlays[0].Params = json.RawMessage(`{ "title" : "Gala night" }`)
	if _, ha := Canonical(a); func() string { _, hb := Canonical(b); return hb }() != ha {
		t.Fatal("canonical JSON must not depend on spacing")
	}
	if MakesWords(a) != "one vertical video, about 15 s" {
		t.Fatalf("makes %q", MakesWords(a))
	}
	a.Length = &models.RecipeRange{Min: 25, Max: 35}
	if MakesWords(a) != "one vertical video, 25–35 s" {
		t.Fatalf("makes %q", MakesWords(a))
	}
	// What a run spent: dollars (ElevenLabs at its pay-as-you-go prices), and
	// the credits the project's budget counts.
	for c, want := range map[models.RecipeSpent]string{{}: "nothing", {USD: 1.8}: "$1.80", {USD: 0.025, Credits: 312}: "$0.03 (312 ElevenLabs credits)",
		{USD: 0.002, Credits: 1}: "less than a cent (1 ElevenLabs credit)", {USD: 12, Credits: 12400}: "$12.00 (12,400 ElevenLabs credits)"} {
		if got := spentWords(c); got != want {
			t.Errorf("%+v: %q", c, got)
		}
	}
}

// With the recipe's own footage the moments chosen then are reused; fixed
// sections and their graphics are locked; words and pictures go where bound.
func TestBuildWithItsOwnInputs(t *testing.T) {
	d := doc()
	as := build(assemblyIn{Doc: d, Values: values(nil, nil, map[string]string{"headline": "Gala night"}), Changed: map[string]bool{}, Run: "run 1"})
	s := as.Spec
	if len(as.StandIns) != 0 || len(s.Clips) != 5 || s.Clips[1].ID != "m1" || s.Clips[1].Locked || !s.Clips[0].Locked || !s.Clips[4].Locked {
		t.Fatalf("clips %+v", s.Clips)
	}
	if s.Overlays[0].Locked || !s.Overlays[1].Locked {
		t.Fatalf("only graphics in fixed sections are locked: %+v", s.Overlays)
	}
	if s.Voiceover.Beats[0].Take == nil || s.Voiceover.Beats[0].Voice != nil {
		t.Fatalf("lines unchanged: %+v", s.Voiceover.Beats[0])
	}
}

// New footage for the chosen section: stand-ins (the smallest count, the
// section's length, spread over the footage), graphics and lines that
// followed the old moments follow them; new words, a new picture, another voice.
func TestBuildWithNewInputs(t *testing.T) {
	d := doc()
	shaB := strings.Repeat("b", 64)
	vals := values(map[string]bool{"footage": true, "headline": true, "art": true, "voice": true},
		map[string][]models.RecipeRunItem{"footage": {{ID: astC, Duration: 30}}, "art": {{ID: resB, SHA256: shaB}}, "voice": {{ID: "voice_bram", Title: "Bram"}}},
		map[string]string{"headline": "Spring gala"})
	as := build(assemblyIn{Doc: d, Values: vals, Changed: map[string]bool{"footage": true, "headline": true, "art": true, "voice": true},
		Voice: &editspec.VoiceRef{Provider: "elevenlabs", VoiceID: "voice_bram", Name: "Bram"}, DefaultVoice: "voice_ada", Run: "run 2 of “Event recap”"})
	s := as.Spec
	if len(as.StandIns) != 2 || len(s.Clips) != 4 || s.Clips[1].ID != "moments-1" || s.Clips[2].ID != "moments-2" || s.Clips[1].AssetID != astC {
		t.Fatalf("stand-ins %+v / %+v", as.StandIns, s.Clips)
	}
	total := (s.Clips[1].Out - s.Clips[1].In) + (s.Clips[2].Out - s.Clips[2].In)
	if total < 9.9 || total > 10.1 || s.Clips[1].In >= s.Clips[2].In {
		t.Fatalf("the section's length, spread: %+v", s.Clips)
	}
	if !strings.Contains(s.Clips[1].Notes, "lively moments") || !strings.Contains(s.Clips[1].Notes, "2–4 moments") {
		t.Fatalf("notes %q", s.Clips[1].Notes)
	}
	if sp := s.Overlays[0].Span; sp.From != "moments-2" || sp.To != "moments-2" {
		t.Fatalf("the title card follows its moment: %+v", sp)
	}
	if s.Voiceover.Beats[0].Anchor.ClipID != "moments-1" {
		t.Fatalf("the line follows its moment: %+v", s.Voiceover.Beats[0].Anchor)
	}
	if paramText(s.Overlays[0].Params, "title") != "Spring gala" || paramText(s.Overlays[1].Params, "headline") != "Spring gala" {
		t.Fatalf("words: %s %s", s.Overlays[0].Params, s.Overlays[1].Params)
	}
	if s.Resources["art"].ID != resB || s.Resources["art"].SHA256 != shaB {
		t.Fatalf("picture %+v", s.Resources)
	}
	b := s.Voiceover.Beats
	if b[0].Voice == nil || b[0].Voice.VoiceID != "voice_bram" || b[0].Take != nil {
		t.Fatalf("the recipe's voice lines are read again: %+v", b[0])
	}
	if b[1].Voice.VoiceID != "voice_cleo" || b[1].Take == nil {
		t.Fatalf("a line with a voice of its own keeps it: %+v", b[1])
	}
}

// Again: only what changed changes; what people did to the cut since stays.
func TestRebindKeepsWhatPeopleDid(t *testing.T) {
	d := doc()
	first := build(assemblyIn{Doc: d, Values: values(nil, nil, nil), Changed: map[string]bool{}, Run: "run 1"}).Spec
	// Since the run: a person trimmed a moment and added a quote.
	first.Clips[2].Out = 8
	first.Overlays = append(first.Overlays, editspec.Overlay{ID: "quote", Template: editspec.TplQuoteKinetic, At: 1, Duration: 3, Params: json.RawMessage(`{"text":"Wow"}`)})
	changed := map[string]bool{"headline": true}
	as := rebind(assemblyIn{Doc: d, Base: first, Values: values(changed, nil, map[string]string{"headline": "Gala two"}), Changed: changed, Run: "run 2"})
	s := as.Spec
	if s.Clips[2].Out != 8 || findOverlay(s, "quote") == nil || len(as.StandIns) != 0 {
		t.Fatalf("people's changes stay: %+v", s.Clips)
	}
	if paramText(findOverlay(s, "end").Params, "headline") != "Gala two" {
		t.Fatalf("the new words: %s", findOverlay(s, "end").Params)
	}
	// New footage again: the chosen section's clips (whatever they are named
	// now) become stand-ins between the fixed sections.
	changed = map[string]bool{"footage": true}
	as = rebind(assemblyIn{Doc: d, Base: s, Values: values(changed, map[string][]models.RecipeRunItem{"footage": {{ID: astC, Duration: 20}}}, nil), Changed: changed, Run: "run 3"})
	ids := []string{}
	for _, c := range as.Spec.Clips {
		ids = append(ids, c.ID)
	}
	if strings.Join(ids, ",") != "intro,moments-1,moments-2,outro" || findOverlay(as.Spec, "title").Span.From != "moments-1" {
		t.Fatalf("stand-ins between the fixed sections: %v / %+v", ids, findOverlay(as.Spec, "title").Span)
	}
}
