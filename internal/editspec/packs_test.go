package editspec

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// Layout packs (D263–D265).

const packMotionHash = "3fa9c2d1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// packFacts knows one checked motion graphic, acme-title (approved or not).
type packFacts struct {
	selftestFacts
	state string
}

func (f packFacts) Motion(hash string) (MotionInfo, bool) {
	if hash != packMotionHash {
		return MotionInfo{}, false
	}
	return MotionInfo{Name: "acme-title", Hash: hash, State: f.state, Region: "full", MinDuration: 2, MaxDuration: 6,
		Schema: json.RawMessage(`{"type":"object","properties":{"headline":{"type":"string"},"tagline":{"type":"string"},"logo":{"type":"string","format":"metarush-resource"},"wipe":{"type":"string","enum":["left","right"]}},"required":["headline"],"additionalProperties":false}`)}, true
}

func packSpec(overlays ...Overlay) *Spec {
	return logoSpec(&Brand{Logo: "logo", LogoDark: "logo-dark"}, overlays...)
}

func overlayByID(t *testing.T, s *Spec, id string) Overlay {
	t.Helper()
	for _, o := range s.Overlays {
		if o.ID == id {
			return o
		}
	}
	t.Fatalf("no graphic %s", id)
	return Overlay{}
}

func paramsOf(t *testing.T, o Overlay) map[string]any {
	t.Helper()
	m := map[string]any{}
	if err := json.Unmarshal(o.Params, &m); err != nil {
		t.Fatalf("params of %s: %v", o.ID, err)
	}
	return m
}

func TestRoleWords(t *testing.T) {
	for in, want := range map[string]string{"title card": "title-card", "Lower third: guest": "lower-third:guest", "lower-third:guest": "lower-third:guest",
		"end screen": "end-screen", "quote-card": "quote-card", "chapter  card": "chapter-card"} {
		got, ok := ParseRoleWords(in)
		if !ok || got != want {
			t.Errorf("ParseRoleWords(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "picture in picture", "logo", "lower third:", "title card: two words"} {
		if k, ok := ParseRoleWords(bad); ok {
			t.Errorf("ParseRoleWords(%q) = %q, want no role", bad, k)
		}
	}
	if RoleWords("lower-third:guest") != "lower third: guest" || RoleWords(PackEndScreen) != "end screen" {
		t.Errorf("RoleWords: %q, %q", RoleWords("lower-third:guest"), RoleWords(PackEndScreen))
	}
}

func TestStarterPackFollowsTheHouseStyle(t *testing.T) {
	p := StarterPack(nil)
	if p.Pieces[PackTitleCard].Template != TplTitleCard || p.Pieces[PackLowerThird].Template != TplLowerThirdClean || p.Pieces[PackEndScreen].Template != TplEndCard {
		t.Fatalf("MetaRush's own pack: %+v", p.Pieces)
	}
	if why := p.Problem(nil, true); why != "" {
		t.Fatalf("the starter pack has a problem: %s", why)
	}
	h := fullStyle()
	p = StarterPack(h)
	if p.Pieces[PackLowerThird].Template != h.LowerThird {
		t.Errorf("the lower third follows the house's name titles: %s", p.Pieces[PackLowerThird].Template)
	}
	var f map[string]string
	_ = json.Unmarshal(p.Pieces[PackEndScreen].Fields, &f)
	if f["headline"] != h.EndCard.Title || f["handle"] != h.EndCard.Handle {
		t.Errorf("the end screen shows the house end card's words when a cut gives none: %v", f)
	}
	if why := p.Problem(nil, true); why != "" {
		t.Fatalf("the starter pack in a house style has a problem: %s", why)
	}
}

func TestPackProblems(t *testing.T) {
	look := packFacts{state: MotionWaiting}.Motion
	for _, c := range []struct {
		pack LayoutPack
		want string
	}{
		{LayoutPack{Pieces: map[string]PackPiece{"logo": {Template: TplLogoCorner}}}, "not \"logo\""},
		{LayoutPack{Pieces: map[string]PackPiece{PackTitleCard: {Template: TplEndCard}}}, "Title card can be a title card or a title slide"},
		{LayoutPack{Pieces: map[string]PackPiece{PackLowerThird: {Template: TplLowerThirdClean, Fields: json.RawMessage(`{"colour":"red"}`)}}}, "has no field \"colour\""},
		{LayoutPack{Pieces: map[string]PackPiece{PackLowerThird: {Template: TplLowerThirdClean, Fields: json.RawMessage(`{"align":"sideways"}`)}}}, "align"},
		{LayoutPack{Pieces: map[string]PackPiece{PackTitleCard: {}}}, "needs a template or a motion graphic"},
		{LayoutPack{Pieces: map[string]PackPiece{PackTitleCard: {Motion: &MotionRef{Name: "nope", Hash: strings.Repeat("0", 64)}}}}, "hasn't been checked"},
		{LayoutPack{Layouts: &LayoutStyle{PiP: &PiPStyle{Corner: "middle"}}}, "sits in a corner"},
	} {
		if got := c.pack.Problem(look, false); !strings.Contains(got, c.want) {
			t.Errorf("%+v: got %q, want %q", c.pack.Pieces, got, c.want)
		}
	}
	ok := LayoutPack{Pieces: map[string]PackPiece{PackTitleCard: {Motion: &MotionRef{Name: "acme-title", Hash: packMotionHash}, Fields: json.RawMessage(`{"wipe":"left"}`)},
		"lower-third:guest": {Template: TplLowerThirdCinematic, Fields: json.RawMessage(`{"align":"center"}`)}}}
	if why := ok.Problem(look, false); why != "" {
		t.Fatalf("a draft may wait for approvals: %s", why)
	}
	if why := ok.Problem(look, true); !strings.Contains(why, "Nobody has approved") {
		t.Fatalf("a current pack needs its graphics approved: %q", why)
	}
	if w := ok.WaitingMotion(look); len(w) != 1 || w[0].Role != PackTitleCard || w[0].Ref.Name != "acme-title" {
		t.Fatalf("waiting: %+v", w)
	}
	if w := ok.WaitingMotion(packFacts{state: MotionApproved}.Motion); len(w) != 0 {
		t.Fatalf("approved graphics don't wait: %+v", w)
	}
	words := PackWords(&ok)
	if len(words) != 2 || !strings.HasPrefix(words[0], "Title card: the motion graphic acme-title") || !strings.HasPrefix(words[1], "Lower third: guest: the cinematic name title template") {
		t.Fatalf("words: %q", words)
	}
}

func TestFillRolesFromThePack(t *testing.T) {
	own := &LayoutPack{Name: "Acme", Version: 3, Pieces: map[string]PackPiece{
		PackTitleCard:  {Motion: &MotionRef{Name: "acme-title", Hash: packMotionHash}, Fields: json.RawMessage(`{"wipe":"left","tagline":"Made in Ohio"}`), Logo: true, Seconds: 4},
		PackLowerThird: {Template: TplLowerThirdCinematic, Fields: json.RawMessage(`{"align":"center"}`)},
	}}
	ps := Packs{Own: own, Starter: StarterPack(nil), Of: "Acme"}
	f := packFacts{state: MotionApproved}
	s := packSpec(
		Overlay{ID: "intro", Role: PackTitleCard, At: 0, Params: json.RawMessage(`{"title":"Spring launch"}`)},
		Overlay{ID: "ada", Role: PackLowerThird, At: 3, Duration: 3, Params: json.RawMessage(`{"name":"Ada","align":"left"}`)},
		Overlay{ID: "quote", Role: PackQuoteCard, At: 6, Duration: 3, Params: json.RawMessage(`{"text":"It works.","attribution":"Ada"}`)},
		Overlay{ID: "kept", Role: PackLowerThird, Template: TplLowerThirdClean, At: 8, Duration: 1.5, Params: json.RawMessage(`{"name":"Bo"}`)},
	)
	base := Clone(s) // an existing cut: only graphics with nothing drawing them are filled
	if !FillRoles(s, base, ps, f) {
		t.Fatal("nothing filled")
	}
	intro := overlayByID(t, s, "intro")
	if intro.Motion == nil || intro.Motion.Hash != packMotionHash || intro.Template != "" || intro.Duration != 4 {
		t.Fatalf("the title card is the pack's motion graphic for its seconds: %+v", intro)
	}
	if p := paramsOf(t, intro); p["headline"] != "Spring launch" || p["wipe"] != "left" || p["tagline"] != "Made in Ohio" || p["logo"] != "logo" {
		t.Fatalf("words carried over (title → headline), the pack's look and words, the brand's logo: %v", p)
	}
	ada := overlayByID(t, s, "ada")
	if ada.Template != TplLowerThirdCinematic || paramsOf(t, ada)["align"] != "center" || paramsOf(t, ada)["name"] != "Ada" {
		t.Fatalf("the lower third takes the pack's look: %+v %s", ada, ada.Params)
	}
	quote := overlayByID(t, s, "quote")
	if quote.Template != TplSlideQuote || paramsOf(t, quote)["text"] != "It works." {
		t.Fatalf("a role the pack leaves out is filled by MetaRush's own: %+v", quote)
	}
	kept := overlayByID(t, s, "kept")
	if kept.Template != TplLowerThirdClean {
		t.Fatalf("a saved graphic's own template stays on later versions: %+v", kept)
	}
	for _, is := range Validate(s, f, base, false) {
		if is.Severity == "error" && strings.HasPrefix(is.Path, "/overlays") {
			t.Fatalf("the filled graphics have problems: %v", is)
		}
	}

	// A new cut follows the pack in every graphic that names a role.
	n := packSpec(Overlay{ID: "kept", Role: PackLowerThird, Template: TplLowerThirdClean, At: 8, Duration: 1.5, Params: json.RawMessage(`{"name":"Bo"}`)})
	FillRoles(n, nil, ps, f)
	if k := overlayByID(t, n, "kept"); k.Template != TplLowerThirdCinematic || k.Duration != 2.5 {
		t.Fatalf("a new cut's lower third follows the pack (and lasts as long as it must): %+v", k)
	}

	// An unfilled role is a plain error at check.
	u := packSpec(Overlay{ID: "chap", Role: PackChapterCard, At: 1, Duration: 3, Params: json.RawMessage(`{"title":"Part 2"}`)})
	if is := Validate(u, f, nil, false); !hasIssue(is, "role_unfilled") {
		t.Fatalf("expected role_unfilled, got %v", is)
	}
	bad := packSpec(Overlay{ID: "x", Role: "logo", Template: TplTitleCard, At: 1, Duration: 3, Params: json.RawMessage(`{"title":"x"}`)})
	if is := Validate(bad, f, nil, false); !hasIssue(is, "unknown_role") {
		t.Fatalf("expected unknown_role, got %v", is)
	}
}

func TestNoPackNoChange(t *testing.T) {
	// MetaRush's own pack on a cut whose graphics name no role changes nothing,
	// and new cuts without a house style are byte for byte as before.
	s := packSpec(Overlay{ID: "t", Template: TplTitleCard, At: 0, Duration: 3, Params: json.RawMessage(`{"title":"x"}`)})
	before := Hash(s)
	if FillRoles(s, nil, Packs{Starter: StarterPack(nil)}, nil) || Hash(s) != before {
		t.Fatal("a cut without roles changed")
	}
	ops, err := PackOps(s, Packs{Starter: StarterPack(nil)}, nil)
	if err != nil || len(ops) != 0 {
		t.Fatalf("ops: %v %v", ops, err)
	}
}

func TestHouseCardsNameTheirRoles(t *testing.T) {
	s := houseSpec()
	NewCutDefaults(s, nil, fullStyle())
	roles := map[string]string{}
	for _, o := range s.Overlays {
		roles[o.Template] = o.Role
	}
	if roles[TplTitleCard] != PackTitleCard || roles[TplEndCard] != PackEndScreen {
		t.Fatalf("the house's cards name their roles: %v", roles)
	}
	// With MetaRush's own pack in that house style, filling a new cut keeps them as they are.
	before := Hash(s)
	FillRoles(s, nil, Packs{Starter: StarterPack(fullStyle())}, nil)
	if Hash(s) != before {
		b, _ := json.Marshal(s.Overlays)
		t.Fatalf("the starter pack changed the house's cards: %s", b)
	}
}

func TestPackOpsAndDrifts(t *testing.T) {
	own := &LayoutPack{Pieces: map[string]PackPiece{
		PackTitleCard:  {Template: TplSlideTitle, Logo: true},
		PackLowerThird: {Template: TplLowerThirdClean, Fields: json.RawMessage(`{"align":"right"}`)},
	}}
	ps := Packs{Own: own, Starter: StarterPack(nil)}
	s := packSpec(
		Overlay{ID: "intro", Role: PackTitleCard, Template: TplTitleCard, At: 0, Duration: 3, Params: json.RawMessage(`{"title":"Hello","subtitle":"World"}`)},
		Overlay{ID: "ada", Role: PackLowerThird, Template: TplLowerThirdClean, At: 4, Duration: 3, Params: json.RawMessage(`{"name":"Ada","align":"left"}`)},
		Overlay{ID: "q", Role: PackQuoteCard, Template: TplSlideQuote, At: 8, Duration: 1, Params: json.RawMessage(`{"text":"Yes"}`), Locked: true},
		Overlay{ID: "free", Template: TplTitleCard, At: 8.5, Duration: 1, Params: json.RawMessage(`{"title":"Mine"}`)},
	)
	drifts := PackDrifts(s, ps, nil)
	got := map[string]PackDrift{}
	for _, d := range drifts {
		got[d.Overlay] = d
	}
	if d := got["intro"]; !d.Other || len(d.Ops) != 1 {
		t.Errorf("intro is another template than the pack's: %+v", d)
	}
	if d := got["ada"]; d.Other || len(d.Fields) != 1 || d.Fields[0] != "align" || len(d.Ops) != 1 {
		t.Errorf("ada changed the pack's alignment: %+v", d)
	}
	if d := got["q"]; !d.Outside || d.Other || len(d.Ops) != 0 {
		t.Errorf("the quote card is outside the pack (and locked): %+v", d)
	}
	if _, ok := got["free"]; ok {
		t.Errorf("a graphic that names no role is its own")
	}
	ops, err := PackOps(s, ps, nil)
	if err != nil || len(ops) != 2 {
		t.Fatalf("ops: %+v %v", ops, err)
	}
	next, _, err := ApplyOps(s, ops, false)
	if err != nil {
		t.Fatal(err)
	}
	intro := overlayByID(t, next, "intro")
	if p := paramsOf(t, intro); intro.Template != TplSlideTitle || p["title"] != "Hello" || p["subtitle"] != "World" || p["logo"] != "logo" {
		t.Fatalf("the intro became the pack's title slide with its words and the logo: %+v %v", intro, p)
	}
	if p := paramsOf(t, overlayByID(t, next, "ada")); p["align"] != "right" || p["name"] != "Ada" {
		t.Fatalf("ada follows the pack's alignment: %v", p)
	}
	if len(PackDrifts(next, ps, nil)) != 1 { // only the locked quote card, outside the pack
		t.Fatalf("after following the pack: %+v", PackDrifts(next, ps, nil))
	}
	// An end card's words become an end screen's.
	es := Overlay{ID: "end", Role: PackEndScreen, Template: TplEndCard, At: 0, Duration: 4, Params: json.RawMessage(`{"headline":"Bye","subline":"See you","cta":"Subscribe"}`)}
	followPiece(&es, PackPiece{Template: TplEndScreen, Fields: json.RawMessage(`{"slots":"one-video"}`)}, "", nil, 0)
	if p := paramsOf(t, es); es.Template != TplEndScreen || es.Duration != 5 || p["headline"] != "Bye" || p["subline"] != "See you" || p["slots"] != "one-video" || p["cta"] != nil {
		t.Fatalf("end card → end screen: %+v %v", es, p)
	}
}

func TestLayoutStyleGeometry(t *testing.T) {
	plain := LayoutTiles(LayoutPiP, 2, 1920, 1080)
	if got := LayoutTilesStyled(LayoutPiP, 2, 1920, 1080, &LayoutStyle{}); got[1] != plain[1] {
		t.Fatalf("an empty style is the layout as always: %+v vs %+v", got[1], plain[1])
	}
	st := &LayoutStyle{PiP: &PiPStyle{Corner: "top-left", Size: "small", Shape: "circle"}}
	in := LayoutTilesStyled(LayoutPiP, 2, 1920, 1080, st)[1]
	if in.W != in.H || in.X > 100 || in.Y > 100 || in.W >= plain[1].H {
		t.Fatalf("a small round inset top left: %+v (plain %+v)", in, plain[1])
	}
	wide := LayoutTilesStyled(LayoutSideBySide, 2, 1920, 1080, &LayoutStyle{SideBySide: &SideBySideStyle{Gap: "wide"}})
	if wide[0].X <= 0 || wide[0].Y <= 0 || wide[1].X-(wide[0].X+wide[0].W) < 30 {
		t.Fatalf("a wide gap with a margin: %+v", wide)
	}
	none := LayoutTilesStyled(LayoutSideBySide, 2, 1920, 1080, &LayoutStyle{SideBySide: &SideBySideStyle{Gap: "none"}})
	if none[0].W+none[1].W != 1920 {
		t.Fatalf("no gap: %+v", none)
	}

	// In the plan: the circle, a frame in the brand's accent, the brand's backdrop.
	f, s := convFixture()
	s.Clips[0].Shots = append(s.Clips[0].Shots, Shot{At: 6.5, Layout: LayoutPiP})
	pin := s.Conversations["call"]
	pin.LayoutStyle = &LayoutStyle{PiP: &PiPStyle{Shape: "circle", Frame: "accent"}, SideBySide: &SideBySideStyle{Backdrop: "brand", Rounded: true, Frame: "text"}}
	s.Conversations["call"] = pin
	s.Brand = &Brand{Accent: "#FF0066", Background: "#102030"}
	if is := Validate(s, f, nil, false); HasErrors(is) {
		t.Fatalf("problems: %v", is)
	}
	plan := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	shots := plan.Clips[0].Conversation.Shots
	side, pip := shots[2], shots[3]
	if side.Backdrop != "#102030" || side.Tiles[0].Border == nil || side.Tiles[0].Border.Color != DefaultBrand.Text || side.Tiles[0].Radius == 0 {
		t.Fatalf("side by side: %+v", side)
	}
	inset := pip.Tiles[1]
	if inset.Rect.W != inset.Rect.H || math.Abs(inset.Radius-inset.Rect.W/2) > 1 || inset.Border == nil || inset.Border.Color != "#FF0066" || pip.Backdrop != "" {
		t.Fatalf("the inset: %+v", inset)
	}
	pin.LayoutStyle = &LayoutStyle{PiP: &PiPStyle{Shape: "oval"}}
	s.Conversations["call"] = pin
	if is := Validate(s, f, nil, false); !hasIssue(is, "layout_style") {
		t.Fatalf("expected layout_style: %v", is)
	}
}

func TestPackLayoutsReachConversations(t *testing.T) {
	_, s := convFixture()
	ps := Packs{Own: &LayoutPack{Layouts: &LayoutStyle{PiP: &PiPStyle{Corner: "bottom-left"}}}, Starter: StarterPack(nil)}
	base := Clone(s)
	if FillRoles(s, base, ps, nil) {
		t.Fatal("a conversation the cut had keeps its look until the pack is used on it")
	}
	if !FillRoles(s, nil, ps, nil) || s.Conversations["call"].LayoutStyle == nil || s.Conversations["call"].LayoutStyle.PiP.Corner != "bottom-left" {
		t.Fatalf("a new cut's call gets the pack's layouts: %+v", s.Conversations["call"].LayoutStyle)
	}
	ops, err := PackOps(s, Packs{Starter: StarterPack(nil)}, nil)
	if err != nil || len(ops) != 1 || ops[0].Op != "setConversation" {
		t.Fatalf("following MetaRush's own pack takes the look away: %+v %v", ops, err)
	}
}

// A fix that moves a graphic to the pack's motion graphic carries its words
// when the facts know the graphic (the quality check adds the pack's).
func TestPackDriftFixCarriesWordsIntoMotion(t *testing.T) {
	own := &LayoutPack{Pieces: map[string]PackPiece{PackTitleCard: {Motion: &MotionRef{Name: "acme-title", Hash: packMotionHash}}}}
	ps := Packs{Own: own, Starter: StarterPack(nil)}
	s := packSpec(Overlay{ID: "intro", Role: PackTitleCard, Template: TplTitleCard, At: 0, Duration: 3, Params: json.RawMessage(`{"title":"Hello"}`)})
	f := packFacts{state: MotionApproved}
	if hs := ps.MotionHashes(); len(hs) != 1 || hs[0] != packMotionHash {
		t.Fatalf("hashes: %v", hs)
	}
	d := PackDrifts(s, ps, WithPackMotion(selftestFacts{}, map[string]MotionInfo{packMotionHash: func() MotionInfo { m, _ := f.Motion(packMotionHash); return m }()}))
	if len(d) != 1 || !d[0].Other || len(d[0].Ops) != 1 || !strings.Contains(string(d[0].Ops[0].Set), `"headline":"Hello"`) {
		t.Fatalf("drift: %+v", d)
	}
	next, _, err := ApplyOps(s, d[0].Ops, false)
	if err != nil {
		t.Fatal(err)
	}
	o := overlayByID(t, next, "intro")
	if p := paramsOf(t, o); o.Template != "" || o.Motion == nil || p["headline"] != "Hello" || p["title"] != nil {
		t.Fatalf("the fix applied: %+v %v", o, p)
	}
}

// Review fixes: the side-by-side gap leaves grids alone; the house style
// leaves lower thirds that name their role to the pack; a graphic that grows
// past the cut's end starts earlier so it still ends with the cut.
func TestPackAndStyleBoundaries(t *testing.T) {
	st := &LayoutStyle{SideBySide: &SideBySideStyle{Gap: "wide"}}
	if a, b := LayoutTiles(LayoutGrid, 4, 1920, 1080), LayoutTilesStyled(LayoutGrid, 4, 1920, 1080, st); a[1] != b[1] || a[3] != b[3] {
		t.Fatalf("a grid keeps its gaps: %+v vs %+v", a, b)
	}

	h := &HouseStyle{LowerThird: TplLowerThirdCinematic}
	s := packSpec(
		Overlay{ID: "ada", Role: PackLowerThird, Template: TplLowerThirdClean, At: 1, Duration: 3, Params: json.RawMessage(`{"name":"Ada"}`)},
		Overlay{ID: "bo", Template: TplLowerThirdClean, At: 5, Duration: 3, Params: json.RawMessage(`{"name":"Bo"}`)},
	)
	ConformStyle(s, h)
	if overlayByID(t, s, "ada").Template != TplLowerThirdClean || overlayByID(t, s, "bo").Template != TplLowerThirdCinematic {
		t.Fatalf("the house style switches only lower thirds that name no role: %+v", s.Overlays)
	}

	// The cut plays 9 s (0.5–9.5 of a 6 s file is still 9 s on its timeline); an end card for its last 4 s.
	end := packSpec(Overlay{ID: "end", Role: PackEndScreen, Template: TplEndCard, At: 5, Duration: 4, Params: json.RawMessage(`{"headline":"Bye"}`)})
	ps := Packs{Own: &LayoutPack{Pieces: map[string]PackPiece{PackEndScreen: {Template: TplEndScreen}}}, Starter: StarterPack(nil)}
	if !FollowPack(end, ps, nil) {
		t.Fatal("nothing changed")
	}
	if o := overlayByID(t, end, "end"); o.Template != TplEndScreen || o.Duration != 5 || o.At != 4 {
		t.Fatalf("the end screen lasts 5 s and still ends with the cut: %+v", o)
	}
}

// A name tag in a round inset sits inside the circle (in its corner it would
// be cut off): Go plans the box, every corner of which lies in the circle.
func TestRoundInsetNameTag(t *testing.T) {
	f, s := convFixture()
	s.Clips[0].Shots = []Shot{{At: 0, Layout: LayoutPiP, Members: []string{"maya", "sam"}}}
	pin := s.Conversations["call"]
	for _, size := range []string{"", "small", "large"} {
		pin.LayoutStyle = &LayoutStyle{PiP: &PiPStyle{Shape: "circle", Size: size}}
		s.Conversations["call"] = pin
		plan := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
		inset := plan.Clips[0].Conversation.Shots[0].Tiles[1]
		b := inset.Tag.Box
		if b == nil || plan.Clips[0].Conversation.Shots[0].Tiles[0].Tag.Box != nil {
			t.Fatalf("%s: only the round inset's tag gets a box: %+v", size, inset.Tag)
		}
		r := inset.Rect.W / 2
		for _, c := range [][2]float64{{b.X, b.Y}, {b.X + b.W, b.Y}, {b.X, b.Y + b.H}, {b.X + b.W, b.Y + b.H}} {
			if math.Hypot(c[0]-r, c[1]-r) > r {
				t.Fatalf("%s: the tag's box %+v leaves the circle at %v (radius %.1f)", size, b, c, r)
			}
		}
	}
	pin.LayoutStyle = &LayoutStyle{PiP: &PiPStyle{Shape: "square"}}
	s.Conversations["call"] = pin
	if tag := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s)).Clips[0].Conversation.Shots[0].Tiles[1].Tag; tag.Box != nil {
		t.Fatalf("a square inset keeps the corner: %+v", tag)
	}
}

// Review 11: a pack uses one version of each shared library; a graphic's (or
// library's) recorded name must be the one the pack gives; a piece's words
// say all it holds.
var theme2SHA = strings.Repeat("c3", 32) // a second approved version of film-theme

type twoThemes struct{ libFacts }

func (f twoThemes) Motion(hash string) (MotionInfo, bool) {
	if hash == theme2SHA {
		return MotionInfo{Name: "film-theme", Hash: hash, State: MotionApproved, Kind: MotionKindLibrary}, true
	}
	return f.libFacts.Motion(hash)
}

func TestPackLibrariesNamesAndWords(t *testing.T) {
	look := twoThemes{}.Motion
	mixed := &LayoutPack{Pieces: map[string]PackPiece{
		PackTitleCard: {Motion: &MotionRef{Name: "end-card", Hash: liftedSHA, Libs: map[string]string{"film-theme": themeSHA}}},
		PackEndScreen: {Motion: &MotionRef{Name: "end-card", Hash: liftedSHA, Libs: map[string]string{"film-theme": theme2SHA}}},
	}}
	if p := mixed.Problem(look, true); !strings.Contains(p, "different versions of the shared library film-theme") {
		t.Fatalf("two versions of one library in a pack: %q", p)
	}
	same := &LayoutPack{Pieces: map[string]PackPiece{
		PackTitleCard: {Motion: &MotionRef{Name: "end-card", Hash: liftedSHA, Libs: map[string]string{"film-theme": themeSHA}}},
		PackEndScreen: {Motion: &MotionRef{Name: "end-card", Hash: liftedSHA, Libs: map[string]string{"film-theme": themeSHA}}},
	}}
	if p := same.Problem(look, true); p != "" {
		t.Fatalf("one version: %q", p)
	}
	if pins := same.LibraryPins(); pins["film-theme"] != themeSHA {
		t.Fatalf("pins: %v", pins)
	}
	// A cut drawn with that pack has one version of the library.
	s := slideSpec(Overlay{ID: "intro", Role: PackTitleCard, At: 0, Duration: 2, Params: json.RawMessage(`{"tagline":"a"}`)},
		Overlay{ID: "end", Role: PackEndScreen, At: 3, Duration: 2, Params: json.RawMessage(`{"tagline":"b"}`)})
	s.Resources["brandfont"] = ResourcePin{ID: "res_selftestfontzzzzzzzzzzzzzz", SHA256: selftestFontSHA}
	FillRoles(s, nil, Packs{Own: same, Starter: StarterPack(nil)}, twoThemes{})
	for _, c := range codesOf(Validate(s, twoThemes{}, nil, false)) {
		if strings.Contains(c, "motion_library") {
			t.Fatalf("the filled cut: %v", c)
		}
	}

	renamed := &LayoutPack{Pieces: map[string]PackPiece{PackTitleCard: {Motion: &MotionRef{Name: "acme-title", Hash: liftedSHA, Libs: map[string]string{"film-theme": themeSHA}}}}}
	if p := renamed.Problem(look, true); !strings.Contains(p, "names the motion graphic acme-title, but version a1a1a1a1 is end-card") {
		t.Fatalf("a name that isn't the version's: %q", p)
	}
	wrongLib := &LayoutPack{Pieces: map[string]PackPiece{PackTitleCard: {Motion: &MotionRef{Name: "end-card", Hash: liftedSHA, Libs: map[string]string{"film-theme": liftedSHA}}}}}
	if p := wrongLib.Problem(look, true); !strings.Contains(p, "isn't the shared library film-theme") {
		t.Fatalf("a library pin that isn't the library: %q", p)
	}

	end := &LayoutPack{Pieces: map[string]PackPiece{PackEndScreen: {Template: TplEndCard, Fields: json.RawMessage(`{"cta":"Buy at shop.example","handle":"@someone-else","align":"left"}`)}}}
	w := PackWords(end)[0]
	for _, want := range []string{"call to action “Buy at shop.example”", "handle “@someone-else”"} {
		if !strings.Contains(w, want) {
			t.Fatalf("the words say every choice: %q (want %q)", w, want)
		}
	}
	if w := PieceWords(same.Pieces[PackTitleCard]); !strings.Contains(w, "with the shared library film-theme version b2b2b2b2") {
		t.Fatalf("the words say the library's version: %q", w)
	}
}
