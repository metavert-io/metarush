package editspec

import (
	"encoding/json"
	"strings"
	"testing"
)

// Logos made for their background (D198) and the house style (D199).

const logoRes = "res_selftestlogozzzzzzzzzzzzzz"

// logoSpec pins the self-test logo under the brand's three logo names.
func logoSpec(brand *Brand, overlays ...Overlay) *Spec {
	s := &Spec{Output: Output{Width: 1280, Height: 720, FPS: 30, Platform: "youtube"}, Clips: []Clip{{ID: "a", AssetID: "blue", In: 0.5, Out: 9.5}},
		Overlays: overlays, Brand: brand,
		Resources: map[string]ResourcePin{
			"logo":      {ID: logoRes, SHA256: selftestLogoSHA},
			"logo-dark": {ID: logoRes, SHA256: selftestLogoSHA},
			"logo-icon": {ID: logoRes, SHA256: selftestLogoSHA},
		}}
	Normalize(s)
	return s
}

func planLogo(t *testing.T, p *RenderPlan, id string) string {
	t.Helper()
	for _, o := range p.Overlays {
		if o.ID == id {
			var m map[string]any
			_ = json.Unmarshal(o.Params, &m)
			s, _ := m["logo"].(string)
			return s
		}
	}
	t.Fatalf("no graphic %s in the plan", id)
	return ""
}

func TestLogoMadeForItsBackground(t *testing.T) {
	title := Overlay{ID: "title", Template: TplTitleCard, At: 0, Duration: 3, Params: json.RawMessage(`{"title":"Hello","logo":"logo"}`)}
	corner := Overlay{ID: "corner", Template: TplLogoCorner, At: 3, Duration: 3, Params: json.RawMessage(`{"logo":"logo"}`)}
	icon := Overlay{ID: "end", Template: TplEndCard, At: 6, Duration: 3, Params: json.RawMessage(`{"headline":"Bye","logo":"logo-icon"}`)}

	// A dark background (MetaRush's own) shows the logo for dark backgrounds.
	b := &Brand{Logo: "logo", LogoDark: "logo-dark"}
	s := logoSpec(b, title, corner, icon)
	p := Resolve(Clone(s), selftestFacts{}, "rev_x", "prj_x", Hash(s))
	if got := planLogo(t, p, "title"); got != "logo-dark" {
		t.Errorf("title card on a dark background shows %q, want logo-dark", got)
	}
	if got := planLogo(t, p, "corner"); got != "logo" {
		t.Errorf("logo.corner sits on the footage and keeps the main logo, got %q", got)
	}
	if got := planLogo(t, p, "end"); got != "logo-icon" {
		t.Errorf("a graphic naming another logo shows it as named, got %q", got)
	}
	if p.Brand.Logo != "logo" || p.Brand.LogoDark != "logo-dark" {
		t.Errorf("the plan's brand carries the logos: %+v", p.Brand)
	}

	// A light background with no logo for light backgrounds: the main one.
	lb := &Brand{Background: "#F7F5F0", Text: "#141414", Logo: "logo", LogoDark: "logo-dark"}
	s = logoSpec(lb, title)
	p = Resolve(Clone(s), selftestFacts{}, "rev_x", "prj_x", Hash(s))
	if got := planLogo(t, p, "title"); got != "logo" {
		t.Errorf("light background without a light logo shows %q, want the main logo", got)
	}
	// …and with one, that one.
	lb.LogoLight = "logo-light"
	s = logoSpec(lb, title)
	s.Resources["logo-light"] = ResourcePin{ID: logoRes, SHA256: selftestLogoSHA}
	p = Resolve(Clone(s), selftestFacts{}, "rev_x", "prj_x", Hash(s))
	if got := planLogo(t, p, "title"); got != "logo-light" {
		t.Errorf("light background shows %q, want logo-light", got)
	}

	// Cuts without brand.logo (every cut made before) show what they name.
	s = logoSpec(&Brand{Background: "#0B0B0E"}, title)
	p = Resolve(Clone(s), selftestFacts{}, "rev_x", "prj_x", Hash(s))
	if got := planLogo(t, p, "title"); got != "logo" {
		t.Errorf("a cut without brand logos changed its logo to %q", got)
	}
	if !strings.Contains(string(mustJSON(t, p.Overlays[0].Params)), `"title":"Hello"`) {
		t.Errorf("params changed: %s", p.Overlays[0].Params)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestLogoChoiceFollowsNameBarSegments(t *testing.T) {
	bar := Overlay{ID: "names", Template: TplNameBar, Span: &OverlaySpan{}, Params: json.RawMessage(`{"name":"Ada","logo":"logo"}`),
		Changes: []OverlayChange{{Footage: "blue", At: 3, Params: json.RawMessage(`{"name":"Grace"}`)}}}
	s := logoSpec(&Brand{Logo: "logo", LogoDark: "logo-dark"}, bar)
	p := Resolve(Clone(s), selftestFacts{}, "rev_x", "prj_x", Hash(s))
	if got := planLogo(t, p, "names"); got != "logo-dark" {
		t.Fatalf("name bar shows %q, want logo-dark", got)
	}
	if len(p.Overlays[0].Segments) < 2 {
		t.Fatalf("expected segments, got %+v", p.Overlays[0].Segments)
	}
	for _, sg := range p.Overlays[0].Segments {
		if !strings.Contains(string(sg.Params), `"logo":"logo-dark"`) {
			t.Errorf("segment keeps the main logo: %s", sg.Params)
		}
	}
}

func TestBrandLogosAreUsedAndChecked(t *testing.T) {
	s := logoSpec(&Brand{Logo: "logo", LogoDark: "logo-dark", LogoLight: "missing-light"})
	var found bool
	for _, is := range validateResources(s, selftestFacts{}) {
		if is.Code == "resource_unpinned" && is.Path == "/brand/logoLight" {
			found = true
			if !strings.Contains(is.Message, "brand's logo") {
				t.Errorf("message: %s", is.Message)
			}
		}
	}
	if !found {
		t.Error("an unpinned brand logo isn't reported")
	}
	s.Brand.LogoLight = ""
	p := Resolve(Clone(s), selftestFacts{}, "rev_x", "prj_x", Hash(s))
	if _, ok := p.ResourceSources[selftestLogoSHA]; !ok {
		t.Errorf("the brand's logos are among the files the plan draws: %v", p.ResourceSources)
	}
}

func TestDarkBackground(t *testing.T) {
	for hex, want := range map[string]bool{"#0B0B0E": true, "#000000": true, "#1B4D3E": true, "#F7F5F0": false, "#FFFFFF": false, "#E0A458": false} {
		got, ok := DarkBackground(hex)
		if !ok || got != want {
			t.Errorf("DarkBackground(%s) = %v, %v; want %v", hex, got, ok, want)
		}
	}
	if _, ok := DarkBackground("teal"); ok {
		t.Error("a word isn't a colour")
	}
}

// houseSpec is a new cut as Claude writes one: two clips, a name bar, a
// person's name title, no captions.
func houseSpec() *Spec {
	return &Spec{Title: "Why we volunteer", Output: Output{Preset: PresetLandscape}, Clips: []Clip{
		{ID: "a", AssetID: astA, In: 0, Out: 8},
		{ID: "b", AssetID: astB, In: 0, Out: 8},
	}, Overlays: []Overlay{
		{ID: "names", Template: TplNameBar, Span: &OverlaySpan{}, Params: json.RawMessage(`{"name":"Ada"}`)},
		{ID: "who", Template: TplLowerThirdClean, At: 1, Duration: 3, Params: json.RawMessage(`{"name":"Ada Lovelace"}`)},
		{ID: "short", Template: TplLowerThirdClean, At: 9, Duration: 2.2, Params: json.RawMessage(`{"name":"Grace"}`)},
	}, Resources: map[string]ResourcePin{"logo": {ID: logoRes, SHA256: selftestLogoSHA}}}
}

func fullStyle() *HouseStyle {
	blend := 0.2
	return &HouseStyle{
		Captions:   &StyleCaptions{On: boolp(true), Look: CaptionBoxed, Size: CaptionLarge, Position: "bottom"},
		NameBar:    &StyleNameBar{Look: NameBarTab, Height: NameBarHalf, Animate: NameBarStill},
		LowerThird: TplLowerThirdCinematic,
		Framing:    FramingFollow,
		Sound:      &StyleSound{CutBlend: &blend, LevelSpeech: boolp(false)},
		Intro:      &StyleCard{Template: TplTitleCard},
		EndCard:    &StyleCard{Template: TplEndCard, Title: "Volunteer with us", CTA: "Sign up", Handle: "@fernbrook"},
	}
}

func TestNewCutDefaultsWithoutHouseStyleAreUnchanged(t *testing.T) {
	plain := houseSpec()
	NewCutDefaults(plain, nil)
	for name, h := range map[string]*HouseStyle{"nil": nil, "empty": {}, "empty groups": {Captions: &StyleCaptions{}, NameBar: &StyleNameBar{}, Sound: &StyleSound{}}} {
		s := houseSpec()
		NewCutDefaults(s, nil, h)
		if Hash(s) != Hash(plain) || !equalJSON(s, plain) {
			t.Errorf("%s house style changed the new cut:\n%s\nvs\n%s", name, mustJSON(t, s), mustJSON(t, plain))
		}
	}
}

func TestNewCutDefaultsWithHouseStyle(t *testing.T) {
	s := houseSpec()
	if !NewCutDefaults(s, nil, fullStyle()) {
		t.Fatal("nothing added")
	}
	if c := s.Captions; c == nil || !c.Enabled || c.Style != CaptionBoxed || c.Size != CaptionLarge || c.Position != "bottom" {
		t.Errorf("captions: %+v", s.Captions)
	}
	if s.Output.Framing != FramingFollow {
		t.Errorf("framing %q", s.Output.Framing)
	}
	if s.Mix == nil || s.Mix.LevelSpeech == nil || *s.Mix.LevelSpeech || s.Mix.CutBlend == nil || *s.Mix.CutBlend != 0.2 {
		t.Errorf("sound at cuts: %+v", s.Mix)
	}
	byID := map[string]Overlay{}
	for _, o := range s.Overlays {
		byID[o.ID] = o
	}
	var nb NameBarParams
	_ = json.Unmarshal(byID["names"].Params, &nb)
	if nb.Style != NameBarTab || nb.Height != NameBarHalf || nb.Animate != NameBarStill || nb.Name != "Ada" {
		t.Errorf("name bar: %+v", nb)
	}
	if byID["who"].Template != TplLowerThirdCinematic {
		t.Errorf("name title look: %s", byID["who"].Template)
	}
	if byID["short"].Template != TplLowerThirdClean {
		t.Errorf("a name title too short for the cinematic look keeps its own: %s", byID["short"].Template)
	}
	intro, ok := byID["intro"]
	if !ok || intro.Template != TplTitleCard || intro.At != 0 || intro.Duration != DefaultIntroSeconds ||
		!strings.Contains(string(intro.Params), `"title":"Why we volunteer"`) || !strings.Contains(string(intro.Params), `"logo":"logo"`) {
		t.Errorf("intro: %+v %s", intro, intro.Params)
	}
	end, ok := byID["end-card"]
	if !ok || end.Template != TplEndCard || end.At != 16-DefaultEndCardSeconds || end.Duration != DefaultEndCardSeconds ||
		!strings.Contains(string(end.Params), `"headline":"Volunteer with us"`) || !strings.Contains(string(end.Params), `"handle":"@fernbrook"`) {
		t.Errorf("end card: %+v %s", end, end.Params)
	}
	// The cut it makes is valid.
	Normalize(s)
	b, _ := json.Marshal(map[string]any{"spec": s})
	if issues := ValidatePlanJSON(b); len(issues) > 0 {
		t.Errorf("schema: %v", issues)
	}
}

func TestNewCutKeepsWhatItSetsItself(t *testing.T) {
	s := houseSpec()
	s.Captions = &Captions{Enabled: true, Style: CaptionEmphasis}
	s.Output.Framing = FramingFaces
	s.Overlays[0].Params = json.RawMessage(`{"name":"Ada","animate":"slide"}`)
	s.Overlays = append(s.Overlays, Overlay{ID: "open", Template: TplSlideTitle, At: 0, Duration: 3, Params: json.RawMessage(`{"title":"Its own"}`)})
	NewCutDefaults(s, nil, fullStyle())
	if s.Captions.Style != CaptionEmphasis || s.Captions.Size != CaptionLarge || s.Captions.Position != "bottom" {
		t.Errorf("captions: %+v (its own look kept, the rest filled)", s.Captions)
	}
	if s.Output.Framing != FramingFaces {
		t.Errorf("framing %q", s.Output.Framing)
	}
	if !strings.Contains(string(s.Overlays[0].Params), `"animate":"slide"`) {
		t.Errorf("name bar's own animation: %s", s.Overlays[0].Params)
	}
	for _, o := range s.Overlays {
		if o.ID == "intro" {
			t.Error("a cut that opens with its own card gets no intro")
		}
	}
	// A rewrite of an existing cut keeps that cut's settings: no house style.
	base := houseSpec()
	r := houseSpec()
	NewCutDefaults(r, base, fullStyle())
	if r.Captions != nil || len(r.Overlays) != 3 {
		t.Errorf("a rewrite took the house style: %+v", r)
	}
}

// The house's cards never land on another card: the intro ends before the
// first one, the end card starts after the last one, or they are left out.
func TestHouseCardsKeepClearOfOtherCards(t *testing.T) {
	s := houseSpec()
	s.Overlays = append(s.Overlays, Overlay{ID: "quote", Template: TplQuoteKinetic, At: 2, Duration: 3, Params: json.RawMessage(`{"text":"We came back every week."}`)},
		Overlay{ID: "stat", Template: TplDataCard, At: 12.5, Duration: 3, Params: json.RawMessage(`{"value":"40%","label":"more volunteers","source":"The 2025 report"}`)})
	NewCutDefaults(s, nil, fullStyle())
	for _, o := range s.Overlays {
		switch o.ID {
		case "intro":
			if o.At != 0 || o.Duration != 2 {
				t.Errorf("the intro ends before the quote: %+v", o)
			}
		case "end-card":
			t.Errorf("no room after the stat card for an end card: %+v", o)
		}
	}
}

func TestConformStyleOnTheLatestCut(t *testing.T) {
	cut := houseSpec()
	NewCutDefaults(cut, nil) // an older cut: MetaRush's own defaults
	Normalize(cut)
	h := fullStyle()
	ops, err := StyleOps(cut, h)
	if err != nil || len(ops) == 0 {
		t.Fatalf("ops %v, %v", ops, err)
	}
	next, _, err := ApplyOps(cut, ops, false)
	if err != nil {
		t.Fatal(err)
	}
	want := Clone(cut)
	ConformStyle(want, h)
	Normalize(next)
	Normalize(want)
	if Hash(next) != Hash(want) {
		t.Errorf("the ops don't make the cut follow the style:\n%s\nvs\n%s", mustJSON(t, next), mustJSON(t, want))
	}
	if again, _ := StyleOps(next, h); len(again) != 0 {
		t.Errorf("a cut that follows the style gets more changes: %+v", again)
	}
	// Locked captions stay as they are; a name bar's own choice is replaced.
	locked := Clone(cut)
	locked.Captions = &Captions{Enabled: false, Locked: true}
	locked.Overlays[0].Params = json.RawMessage(`{"name":"Ada","animate":"slide","height":"full"}`)
	ConformStyle(locked, h)
	if locked.Captions.Enabled {
		t.Error("locked captions were turned on")
	}
	var nb NameBarParams
	_ = json.Unmarshal(locked.Overlays[0].Params, &nb)
	if nb.Animate != NameBarStill || nb.Height != NameBarHalf {
		t.Errorf("the name bar doesn't follow the style: %+v", nb)
	}
	// Captions off in the style turn them off.
	off := Clone(next)
	ConformStyle(off, &HouseStyle{Captions: &StyleCaptions{On: boolp(false)}})
	if off.Captions == nil || off.Captions.Enabled {
		t.Errorf("captions: %+v", off.Captions)
	}
	// Nothing set: no change.
	if ops, _ := StyleOps(cut, &HouseStyle{}); len(ops) != 0 {
		t.Errorf("an empty style changes the cut: %+v", ops)
	}
}

func TestHouseStyleWordsAndProblems(t *testing.T) {
	words := strings.Join(StyleWords(fullStyle()), " | ")
	for _, want := range []string{"Captions: on, boxed, large, at the bottom", "about half as tall", "never animates", "Name titles: cinematic",
		"follows movement", "a 0.2 s blend at straight cuts", "voices left as recorded", "Intro: a title card with the cut's title for 3 s", "“Volunteer with us”"} {
		if !strings.Contains(words, want) {
			t.Errorf("words miss %q: %s", want, words)
		}
	}
	if p := fullStyle().Problem(); p != "" {
		t.Errorf("a good style has a problem: %s", p)
	}
	for _, h := range []*HouseStyle{
		{Captions: &StyleCaptions{Size: "huge"}},
		{NameBar: &StyleNameBar{Animate: "spin"}},
		{LowerThird: "slide.quote"},
		{Framing: "zoom"},
		{EndCard: &StyleCard{Template: TplEndCard}},
		{Intro: &StyleCard{Template: TplEndCard}},
	} {
		if h.Problem() == "" {
			t.Errorf("no problem with %+v", h)
		}
	}
	if (&HouseStyle{Captions: &StyleCaptions{}, Intro: &StyleCard{}}).Clean() != nil {
		t.Error("an emptied style is kept")
	}
}
