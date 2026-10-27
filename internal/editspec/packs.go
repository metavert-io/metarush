package editspec

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// Layout packs (D263–D266): a client's title cards, lower thirds, end
// screens, quote cards, chapter cards and the picture-in-picture and
// side-by-side layouts of call recordings, as one named, versioned set kept
// in Content Settings. Each role is played by a vetted template or by a
// project motion graphic a person approved (D68), and the house style
// restyles them: the vetted templates draw in the brand's colours and font,
// show the logo made for their background (PickLogo, D198), and the lower
// thirds and cards follow the house style's choices in MetaRush's own
// starter pack.
//
// A graphic in a cut names its role (Overlay.Role) beside the template or
// motion graphic that draws it, so a saved version renders the same whatever
// the pack becomes (D73). A role without a template is filled from the pack
// when the change is checked (FillRoles); "Use the layout pack on the latest
// cut" makes the graphics that name a role follow the pack as one checked
// change (PackOps). The pack decides how a graphic looks (its choices among
// fixed options: alignment, motion, framing, the logo); the cut decides what
// it says (titles, names, quotes) and when it shows.

// The roles a layout pack fills.
const (
	PackTitleCard   = "title-card"
	PackLowerThird  = "lower-third"
	PackEndScreen   = "end-screen"
	PackQuoteCard   = "quote-card"
	PackChapterCard = "chapter-card"
	// PackPiP and PackSideBySide are how a call recording's picture-in-picture
	// and side-by-side layouts look (D265), not graphics.
	PackPiP        = "picture-in-picture"
	PackSideBySide = "side-by-side"
)

// PackRole is one role a layout pack fills.
type PackRole struct {
	ID string `json:"id"`
	// Words is how people say it ("title card").
	Words   string `json:"words"`
	Summary string `json:"summary"`
	// Graphic: a graphic in the cut (else a call recording's layout).
	Graphic bool `json:"graphic"`
	// Templates are the vetted templates that can play it, MetaRush's own
	// first; a project motion graphic a person approved can play any graphic role.
	Templates []string `json:"templates,omitempty"`
}

// PackRoles lists the roles in the order people see them.
var PackRoles = []PackRole{
	{ID: PackTitleCard, Words: "title card", Graphic: true, Templates: []string{TplTitleCard, TplSlideTitle},
		Summary: "Opens a video or a part of it: a title (and a line under it) with the logo."},
	{ID: PackLowerThird, Words: "lower third", Graphic: true, Templates: []string{TplLowerThirdClean, TplLowerThirdCinematic},
		Summary: "Names who is speaking at the bottom of the picture."},
	{ID: PackEndScreen, Words: "end screen", Graphic: true, Templates: []string{TplEndCard, TplEndScreen, TplSlideTitle},
		Summary: "Ends the video: a headline, a call to action and the handle, or room for the next videos."},
	{ID: PackQuoteCard, Words: "quote card", Graphic: true, Templates: []string{TplSlideQuote, TplQuoteKinetic},
		Summary: "A quote with who said it."},
	{ID: PackChapterCard, Words: "chapter card", Graphic: true, Templates: []string{TplSlideTitle, TplTitleCard},
		Summary: "Starts a chapter: a short label (Part 2) above its title."},
	{ID: PackPiP, Words: "picture in picture",
		Summary: "A call recording with one person full frame and another in a corner."},
	{ID: PackSideBySide, Words: "side by side",
		Summary: "A call recording with people next to each other."},
}

// PackRoleByID finds a role.
func PackRoleByID(id string) (PackRole, bool) {
	for _, r := range PackRoles {
		if r.ID == id {
			return r, true
		}
	}
	return PackRole{}, false
}

// RoleKeyPattern is a role as a cut or a pack names it: the role, or the role
// and one of its variants (lower-third:guest), so a pack can hold more than
// one look for a role.
var RoleKeyPattern = regexp.MustCompile(`^[a-z][a-z-]{0,23}(:[a-z][a-z0-9-]{0,23})?$`)

// SplitRole splits a role key into the role and its variant ("" for none).
func SplitRole(key string) (role, variant string) {
	role, variant, _ = strings.Cut(key, ":")
	return role, variant
}

// RoleWords says a role key the way people do: "lower third", "lower third: guest".
func RoleWords(key string) string {
	role, variant := SplitRole(key)
	w := role
	if r, ok := PackRoleByID(role); ok {
		w = r.Words
	}
	if variant != "" {
		w += ": " + variant
	}
	return w
}

// ParseRoleWords reads a role the way people write it ("title card", "lower
// third: guest", "lower-third:guest", "Quote card"); ok is false when it
// names no graphic role.
func ParseRoleWords(s string) (key string, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	head, variant, hasVariant := strings.Cut(s, ":")
	head = strings.Join(strings.Fields(strings.ReplaceAll(head, "-", " ")), " ")
	variant = strings.TrimSpace(variant)
	for _, r := range PackRoles {
		if r.Graphic && (head == r.Words || head == r.ID) {
			key = r.ID
			if hasVariant {
				if variant == "" {
					return "", false
				}
				key += ":" + variant
			}
			return key, RoleKeyPattern.MatchString(key)
		}
	}
	return "", false
}

// GraphicRole reports a role key that names a graphic role (with or without a variant).
func GraphicRole(key string) bool {
	if !RoleKeyPattern.MatchString(key) {
		return false
	}
	role, _ := SplitRole(key)
	r, ok := PackRoleByID(role)
	return ok && r.Graphic
}

// LayoutPack is a client's set of graphics and call layouts (D263).
type LayoutPack struct {
	// Name is what people call it ("Acme"): shown in Settings and on cards.
	Name string `json:"name,omitempty" bson:"name,omitempty" jsonschema:"maxLength=80,description=What people call the pack (Acme)."`
	// Version counts the pack's saved changes (MetaRush keeps it): a draft
	// remembers the version it was made from, so it can't silently undo a
	// change made since.
	Version int `json:"version,omitempty" bson:"version,omitempty" jsonschema:"minimum=0,description=How many times the pack was saved (MetaRush keeps it)."`
	// Pieces are the graphics, by role ("title-card") or role and variant
	// ("lower-third:guest"). A role the pack leaves out uses MetaRush's own.
	Pieces map[string]PackPiece `json:"pieces,omitempty" bson:"pieces,omitempty" jsonschema:"description=The graphics by role (title-card\\, lower-third\\, end-screen\\, quote-card\\, chapter-card; a variant after a colon: lower-third:guest)."`
	// Layouts are how call recordings' picture-in-picture and side-by-side
	// layouts look (D265).
	Layouts *LayoutStyle `json:"layouts,omitempty" bson:"layouts,omitempty" jsonschema:"description=How call recordings' picture-in-picture and side-by-side layouts look."`
	// Note says what the pack is for, in a person's or Claude's words.
	Note string `json:"note,omitempty" bson:"note,omitempty" jsonschema:"maxLength=500"`
}

// PackPiece is the graphic that plays one role: a vetted template or a
// project motion graphic a person approved, and the pack's choices for it.
type PackPiece struct {
	Template string     `json:"template,omitempty" bson:"template,omitempty" jsonschema:"description=A vetted template (title.card\\, lower-third.clean…)."`
	Motion   *MotionRef `json:"motion,omitempty" bson:"motion,omitempty" jsonschema:"description=A project motion graphic a person approved\\, instead of a template."`
	// Fields are the pack's choices for the graphic's fields: how it looks
	// (alignment, motion, frame — always the pack's) and words or pictures a
	// graphic shows when the cut gives none (an end screen's call to action).
	Fields json.RawMessage `json:"fields,omitempty" bson:"fields,omitempty" jsonschema:"type=object,description=The pack's choices for the graphic: how it looks (always) and words it shows when the cut gives none."`
	// Seconds a graphic that names the role stays when the cut doesn't say.
	Seconds float64 `json:"seconds,omitempty" bson:"seconds,omitempty" jsonschema:"minimum=0.5,maximum=60,description=How long the graphic stays when the cut doesn't say."`
	// Logo: the graphic shows the brand's logo (the one made for its background).
	Logo bool `json:"logo,omitempty" bson:"logo,omitempty" jsonschema:"description=Show the brand's logo (the one made for the graphic's background)."`
}

// IsMotion reports a piece drawn by a project motion graphic.
func (p PackPiece) IsMotion() bool { return p.Motion != nil }

// What names the piece's graphic: a template ID or "motion name@version".
func (p PackPiece) What() string {
	if p.Motion != nil {
		return "motion " + p.Motion.Name + "@" + shortHash(p.Motion.Hash)
	}
	return p.Template
}

// LayoutStyle is how call recordings' layouts look (D265). Empty fields are
// MetaRush's own look (what cuts always had).
type LayoutStyle struct {
	PiP        *PiPStyle        `json:"pip,omitempty" bson:"pip,omitempty" jsonschema:"description=Picture in picture: where the inset sits\\, its size\\, shape and frame."`
	SideBySide *SideBySideStyle `json:"sideBySide,omitempty" bson:"sideBySide,omitempty" jsonschema:"description=Side by side (and stacked): the gap\\, frame and what shows between the pictures."`
}

// PiPStyle is how a picture-in-picture layout looks.
type PiPStyle struct {
	Corner string `json:"corner,omitempty" bson:"corner,omitempty" jsonschema:"enum=,enum=bottom-right,enum=bottom-left,enum=top-right,enum=top-left,description=Where the inset sits (default bottom-right)."`
	Size   string `json:"size,omitempty" bson:"size,omitempty" jsonschema:"enum=,enum=small,enum=medium,enum=large,description=How large the inset is (default medium)."`
	Shape  string `json:"shape,omitempty" bson:"shape,omitempty" jsonschema:"enum=,enum=rounded,enum=square,enum=circle,description=rounded (default)\\, square or circle (a round camera bubble)."`
	Frame  string `json:"frame,omitempty" bson:"frame,omitempty" jsonschema:"enum=,enum=none,enum=accent,enum=primary,enum=text,description=A line around the inset in one of the brand's colours (default none)."`
}

// SideBySideStyle is how side-by-side (and stacked) layouts look.
type SideBySideStyle struct {
	Gap      string `json:"gap,omitempty" bson:"gap,omitempty" jsonschema:"enum=,enum=none,enum=thin,enum=wide,description=The gap between the pictures (default thin)."`
	Frame    string `json:"frame,omitempty" bson:"frame,omitempty" jsonschema:"enum=,enum=none,enum=accent,enum=primary,enum=text,description=A line around each picture in one of the brand's colours (default none)."`
	Backdrop string `json:"backdrop,omitempty" bson:"backdrop,omitempty" jsonschema:"enum=,enum=black,enum=brand,description=What shows between the pictures: black (default) or the brand's background colour."`
	Rounded  bool   `json:"rounded,omitempty" bson:"rounded,omitempty" jsonschema:"description=Round the pictures' corners."`
}

// Empty reports a layout style that changes nothing.
func (l *LayoutStyle) Empty() bool {
	return l == nil || (l.PiP == nil || *l.PiP == PiPStyle{}) && (l.SideBySide == nil || *l.SideBySide == SideBySideStyle{})
}

// Clean drops empty parts, so an emptied style is stored as nothing.
func (l *LayoutStyle) Clean() *LayoutStyle {
	if l.Empty() {
		return nil
	}
	x := *l
	if x.PiP != nil && *x.PiP == (PiPStyle{}) {
		x.PiP = nil
	}
	if x.SideBySide != nil && *x.SideBySide == (SideBySideStyle{}) {
		x.SideBySide = nil
	}
	return &x
}

// Problem says what is wrong with a layout style ("" nothing).
func (l *LayoutStyle) Problem() string {
	if l == nil {
		return ""
	}
	if p := l.PiP; p != nil {
		switch {
		case !oneOf(p.Corner, "bottom-right", "bottom-left", "top-right", "top-left"):
			return fmt.Sprintf("The picture in picture sits in a corner (bottom-right, bottom-left, top-right or top-left), not %q.", p.Corner)
		case !oneOf(p.Size, "small", "medium", "large"):
			return fmt.Sprintf("The picture in picture is small, medium or large, not %q.", p.Size)
		case !oneOf(p.Shape, "rounded", "square", "circle"):
			return fmt.Sprintf("The picture in picture is rounded, square or a circle, not %q.", p.Shape)
		case !oneOf(p.Frame, "none", "accent", "primary", "text"):
			return fmt.Sprintf("The frame is none or one of the brand's colours (accent, primary, text), not %q.", p.Frame)
		}
	}
	if s := l.SideBySide; s != nil {
		switch {
		case !oneOf(s.Gap, "none", "thin", "wide"):
			return fmt.Sprintf("The gap between pictures is none, thin or wide, not %q.", s.Gap)
		case !oneOf(s.Frame, "none", "accent", "primary", "text"):
			return fmt.Sprintf("The frame is none or one of the brand's colours (accent, primary, text), not %q.", s.Frame)
		case !oneOf(s.Backdrop, "black", "brand"):
			return fmt.Sprintf("Between the pictures shows black or the brand's background, not %q.", s.Backdrop)
		}
	}
	return ""
}

// LayoutStyleWords says a layout style in plain words, a line per layout.
func LayoutStyleWords(l *LayoutStyle) []string {
	if l.Empty() {
		return nil
	}
	var out []string
	frame := func(f string) string {
		if f == "" || f == "none" {
			return ""
		}
		return "a line in the brand's " + f + " colour"
	}
	if p := l.PiP; p != nil && *p != (PiPStyle{}) {
		var bits []string
		if p.Size != "" {
			bits = append(bits, p.Size)
		}
		switch p.Shape {
		case "circle":
			bits = append(bits, "a circle")
		case "square":
			bits = append(bits, "square corners")
		case "rounded":
			bits = append(bits, "rounded corners")
		}
		if p.Corner != "" {
			bits = append(bits, strings.ReplaceAll(p.Corner, "-", " "))
		}
		if f := frame(p.Frame); f != "" {
			bits = append(bits, f)
		}
		out = append(out, "Picture in picture: "+strings.Join(bits, ", "))
	}
	if s := l.SideBySide; s != nil && *s != (SideBySideStyle{}) {
		var bits []string
		switch s.Gap {
		case "none":
			bits = append(bits, "no gap")
		case "thin":
			bits = append(bits, "a thin gap")
		case "wide":
			bits = append(bits, "a wide gap")
		}
		if s.Backdrop == "brand" {
			bits = append(bits, "the brand's background between the pictures")
		}
		if s.Rounded {
			bits = append(bits, "rounded corners")
		}
		if f := frame(s.Frame); f != "" {
			bits = append(bits, f)
		}
		out = append(out, "Side by side: "+strings.Join(bits, ", "))
	}
	return out
}

// StarterPack is MetaRush's own layout pack in a house style (D263): the
// vetted templates, the lower thirds in the house's look, the house's intro
// and end card templates for the title card and end screen (with the end
// card's words as the words an end screen shows when a cut gives none), and
// call layouts as they always looked. It is what a project uses until its
// settings have a pack, and what fills a role a pack leaves out.
func StarterPack(h *HouseStyle) *LayoutPack {
	title := TplTitleCard
	if h != nil && h.Intro != nil && h.Intro.Template == TplSlideTitle {
		title = TplSlideTitle
	}
	lower := TplLowerThirdClean
	if h != nil && h.LowerThird != "" {
		lower = h.LowerThird
	}
	end := PackPiece{Template: TplEndCard, Seconds: DefaultEndCardSeconds, Logo: true}
	if h != nil && h.EndCard != nil && h.EndCard.Template != "" {
		k := h.EndCard
		if k.Template == TplSlideTitle {
			end.Template = TplSlideTitle
		}
		words := map[string]string{}
		if end.Template == TplEndCard {
			words = map[string]string{"headline": k.Title, "subline": k.Subtitle, "cta": k.CTA, "handle": k.Handle}
		} else {
			words = map[string]string{"title": k.Title, "subtitle": k.Subtitle}
		}
		f := map[string]string{}
		for key, v := range words {
			if strings.TrimSpace(v) != "" {
				f[key] = v
			}
		}
		if len(f) > 0 {
			end.Fields, _ = json.Marshal(f)
		}
		if k.Seconds > 0 {
			end.Seconds = k.Seconds
		}
	}
	intro := PackPiece{Template: title, Seconds: DefaultIntroSeconds, Logo: true}
	if h != nil && h.Intro != nil && h.Intro.Seconds > 0 {
		intro.Seconds = h.Intro.Seconds
	}
	return &LayoutPack{Name: "MetaRush's own", Pieces: map[string]PackPiece{
		PackTitleCard:   intro,
		PackLowerThird:  {Template: lower, Seconds: 4},
		PackEndScreen:   end,
		PackQuoteCard:   {Template: TplSlideQuote, Seconds: 5},
		PackChapterCard: {Template: TplSlideTitle, Seconds: 3},
	}}
}

// Packs is the layout pack a project's cuts follow: the pack of the Content
// Settings it uses (Own, nil when they have none) over MetaRush's starter
// pack in the house style, which fills the roles Own leaves out.
type Packs struct {
	Own     *LayoutPack
	Starter *LayoutPack
	// Of names whose settings hold Own ("" when it is MetaRush's own).
	Of string
}

// Current is the pack in use: the settings' own, else the starter.
func (ps Packs) Current() *LayoutPack {
	if ps.Own != nil {
		return ps.Own
	}
	return ps.Starter
}

// Piece is the graphic that plays a role key: the pack's own piece for the
// key, else for the role without its variant, else the starter's. inPack is
// false when the pack in use doesn't have the role (the starter fills it).
func (ps Packs) Piece(key string) (piece PackPiece, inPack, ok bool) {
	role, _ := SplitRole(key)
	for _, p := range []*LayoutPack{ps.Own, ps.Starter} {
		if p == nil {
			continue
		}
		for _, k := range []string{key, role} {
			if pc, has := p.Pieces[k]; has && (pc.Template != "" || pc.Motion != nil) {
				return pc, p == ps.Current(), true
			}
		}
	}
	return PackPiece{}, false, false
}

// Layouts is how the pack in use draws call layouts (nil: as always).
func (ps Packs) Layouts() *LayoutStyle {
	if c := ps.Current(); c != nil && !c.Layouts.Empty() {
		return c.Layouts
	}
	return nil
}

// PackMotion looks up what a motion graphic version's check recorded (nil:
// unknown).
type PackMotion func(hash string) (MotionInfo, bool)

// Problem says, in plain words, what is wrong with a pack ("" nothing).
// approvedOnly also refuses a motion graphic nobody approved (a pack that
// becomes current); a draft may wait for approvals.
func (p *LayoutPack) Problem(look PackMotion, approvedOnly bool) string {
	if p == nil {
		return ""
	}
	if len([]rune(p.Name)) > 80 {
		return "The pack's name is at most 80 letters."
	}
	if len([]rune(p.Note)) > 500 {
		return "The pack's note is at most 500 letters."
	}
	if len(p.Pieces) > 40 {
		return "A pack holds at most 40 graphics."
	}
	for _, key := range sortedPieceKeys(p.Pieces) {
		if why := p.Pieces[key].problem(key, look, approvedOnly); why != "" {
			return why
		}
	}
	// One version of each shared library across the pack (D171): a cut can't
	// draw two versions of one library, so graphics that share one share its version.
	pinned, by := map[string]string{}, map[string]string{}
	for _, key := range sortedPieceKeys(p.Pieces) {
		m := p.Pieces[key].Motion
		if m == nil {
			continue
		}
		for _, lib := range sortedLibNames(m.Libs) {
			h := m.Libs[lib]
			if prev, ok := pinned[lib]; ok && prev != h {
				return fmt.Sprintf("The %s and the %s use different versions of the shared library %s (%s, %s); a pack uses one version of each.", RoleWords(by[lib]), RoleWords(key), lib, shortHash(prev), shortHash(h))
			}
			pinned[lib], by[lib] = h, key
		}
	}
	return p.Layouts.Problem()
}

// LibraryPins are the shared library versions the pack's motion graphics
// use (name → version), so a graphic added to it uses the same ones.
func (p *LayoutPack) LibraryPins() map[string]string {
	out := map[string]string{}
	if p == nil {
		return out
	}
	for _, key := range sortedPieceKeys(p.Pieces) {
		if m := p.Pieces[key].Motion; m != nil {
			for lib, h := range m.Libs {
				if _, ok := out[lib]; !ok {
					out[lib] = h
				}
			}
		}
	}
	return out
}

func sortedLibNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (pc PackPiece) problem(key string, look PackMotion, approvedOnly bool) string {
	if !GraphicRole(key) {
		return fmt.Sprintf("A pack has graphics for a title card, lower third, end screen, quote card or chapter card (a variant after a colon: lower-third:guest), not %q.", key)
	}
	role, _ := SplitRole(key)
	r, _ := PackRoleByID(role)
	words := upperFirst(RoleWords(key))
	switch {
	case pc.Template != "" && pc.Motion != nil:
		return words + " is drawn by a template or a motion graphic, not both."
	case pc.Template == "" && pc.Motion == nil:
		return words + " needs a template or a motion graphic."
	case pc.Seconds != 0 && (pc.Seconds < 0.5 || pc.Seconds > 60):
		return words + " stays between 0.5 and 60 seconds."
	}
	if pc.Template != "" {
		if !slicesContains(r.Templates, pc.Template) {
			return fmt.Sprintf("%s can be %s, or a motion graphic, not %q.", words, joinOr(templateWords(r.Templates)), pc.Template)
		}
		if why := templateFieldsProblem(pc.Template, pc.Fields); why != "" {
			return words + ": " + why
		}
		return ""
	}
	m := pc.Motion
	if !motionNameRe.MatchString(m.Name) || !shaHexRe.MatchString(m.Hash) {
		return words + " names a motion graphic MetaRush can't read (a name and its full version)."
	}
	if look == nil {
		return ""
	}
	info, ok := look(m.Hash)
	if !ok {
		return fmt.Sprintf("%s's motion graphic %s (version %s) hasn't been checked in this workspace yet; check it first.", words, m.Name, shortHash(m.Hash))
	}
	if info.Kind == MotionKindLibrary {
		return fmt.Sprintf("%s is a shared library, not a graphic.", m.Name)
	}
	if info.Name != "" && info.Name != m.Name {
		return fmt.Sprintf("%s names the motion graphic %s, but version %s is %s.", words, m.Name, shortHash(m.Hash), info.Name)
	}
	if approvedOnly && info.State != MotionApproved {
		_, msg := motionMessage(info, *m)
		return words + ": " + msg
	}
	// The shared libraries it imports (D171): each pinned, and approved before it is the pack's.
	for _, lib := range info.Libraries {
		h := m.Libs[lib]
		if h == "" {
			return fmt.Sprintf("%s's motion graphic %s needs a version of its shared library %s.", words, m.Name, lib)
		}
		li, known := look(h)
		if !known {
			return fmt.Sprintf("%s: the shared library %s (version %s) hasn't been checked in this workspace yet; check it first.", words, lib, shortHash(h))
		}
		if li.Kind != MotionKindLibrary || (li.Name != "" && li.Name != lib) {
			return fmt.Sprintf("%s: version %s isn't the shared library %s.", words, shortHash(h), lib)
		}
		if approvedOnly && li.State != MotionApproved {
			_, msg := motionMessage(li, MotionRef{Name: lib, Hash: h})
			return words + ": " + msg
		}
	}
	if why := motionFieldsProblem(info, pc.Fields); why != "" {
		return words + ": " + why
	}
	return ""
}

var motionNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)

// PackWait is a motion graphic (or a shared library one imports) of a pack
// that waits for a person's approval.
type PackWait struct {
	Role string
	Ref  MotionRef
	// Library: a shared library the role's graphic imports (D171).
	Library bool
}

// WaitingMotion lists the pack's motion graphics, and the shared libraries
// they import, that nobody has approved yet (unknown versions too), in role
// order.
func (p *LayoutPack) WaitingMotion(look PackMotion) []PackWait {
	var out []PackWait
	if p == nil {
		return out
	}
	approved := func(h string) bool {
		if look == nil {
			return false
		}
		info, ok := look(h)
		return ok && info.State == MotionApproved
	}
	for _, key := range sortedPieceKeys(p.Pieces) {
		pc := p.Pieces[key]
		if pc.Motion == nil {
			continue
		}
		if !approved(pc.Motion.Hash) {
			out = append(out, PackWait{Role: key, Ref: *pc.Motion})
		}
		libs := make([]string, 0, len(pc.Motion.Libs))
		for l := range pc.Motion.Libs {
			libs = append(libs, l)
		}
		sort.Strings(libs)
		for _, l := range libs {
			if h := pc.Motion.Libs[l]; !approved(h) {
				out = append(out, PackWait{Role: key, Ref: MotionRef{Name: l, Hash: h}, Library: true})
			}
		}
	}
	return out
}

// PackWords says a pack in plain words: a line per role it has, then its layouts.
func PackWords(p *LayoutPack) []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, key := range sortedPieceKeys(p.Pieces) {
		out = append(out, upperFirst(RoleWords(key))+": "+PieceWords(p.Pieces[key]))
	}
	return append(out, LayoutStyleWords(p.Layouts)...)
}

// PieceWords says what draws a piece ("the title card template, with the logo").
// It says everything the piece holds — its shared libraries' versions and
// each of its choices — so what a person accepts is what they read.
func PieceWords(pc PackPiece) string {
	var w string
	if pc.Motion != nil {
		w = fmt.Sprintf("the motion graphic %s (version %s", pc.Motion.Name, shortHash(pc.Motion.Hash))
		for _, lib := range sortedLibNames(pc.Motion.Libs) {
			w += fmt.Sprintf(", with the shared library %s version %s", lib, shortHash(pc.Motion.Libs[lib]))
		}
		w += ")"
	} else {
		w = "the " + TemplateWords(pc.Template) + " template"
	}
	if pc.Logo {
		w += ", with the logo"
	}
	if pc.Seconds > 0 {
		w += fmt.Sprintf(", %s s", trimNum(pc.Seconds))
	}
	if f := choiceWords(pc.Fields); f != "" {
		w += "; " + f
	}
	return w
}

// fieldNames are how people say a graphic's fields.
var fieldNames = map[string]string{
	"title": "title", "subtitle": "line under it", "headline": "headline", "subline": "line under it", "cta": "call to action",
	"handle": "handle", "align": "alignment", "kicker": "label above", "slots": "room for", "outline": "guide outline",
	"motion": "movement", "text": "quote", "attribution": "who said it", "role": "who they are", "name": "name",
	"logo": "logo", "image": "picture", "reveal": "points appear", "emphasis": "highlighted words",
}

// choiceWords says a piece's choices, every one of them, words in quotes.
func choiceWords(raw json.RawMessage) string {
	m, err := fieldsMap(raw)
	if err != nil || len(m) == 0 {
		return ""
	}
	var parts []string
	for _, k := range sortedRawKeys(m) {
		name := fieldNames[k]
		if name == "" {
			name = k
		}
		var v any
		_ = json.Unmarshal(m[k], &v)
		var val string
		switch x := v.(type) {
		case string:
			val = "“" + x + "”"
		case bool:
			val = map[bool]string{true: "yes", false: "no"}[x]
		case float64:
			val = trimNum(x)
		case []any:
			var items []string
			for _, it := range x {
				items = append(items, fmt.Sprintf("“%v”", it))
			}
			val = strings.Join(items, ", ")
		default:
			b, _ := json.Marshal(v)
			val = string(b)
		}
		parts = append(parts, name+" "+val)
	}
	return strings.Join(parts, ", ")
}

// sortedPieceKeys orders role keys as PackRoles does, variants after their role.
func sortedPieceKeys(m map[string]PackPiece) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	rank := func(k string) int {
		role, _ := SplitRole(k)
		for i, r := range PackRoles {
			if r.ID == role {
				return i
			}
		}
		return len(PackRoles)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := rank(keys[i]), rank(keys[j])
		if a != b {
			return a < b
		}
		return keys[i] < keys[j]
	})
	return keys
}

// ---- the fields of a graphic: how it looks, what it says ----

// Kinds of a graphic's fields: the pack decides how a graphic looks; the cut
// decides what it says and which pictures it shows.
const (
	fieldLook    = "look"    // a choice among fixed options, a number or a yes/no
	fieldWords   = "words"   // text the graphic shows
	fieldPicture = "picture" // a resource the graphic shows
)

// fieldKinds are a graphic's fields and their kinds.
type fieldKinds map[string]string

// templateKinds reads a vetted template's fields from its Go type.
func templateKinds(id string) fieldKinds {
	t, ok := TemplateByID(id)
	if !ok || t.params == nil {
		return nil
	}
	out := fieldKinds{}
	rt := reflect.TypeOf(t.params)
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		tag := f.Tag.Get("jsonschema")
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch {
		case strings.Contains(tag, "format="+ResourceFormat):
			out[name] = fieldPicture
		case ft.Kind() == reflect.String && !strings.Contains(tag, "enum="):
			out[name] = fieldWords
		case ft.Kind() == reflect.Slice && ft.Elem().Kind() == reflect.String:
			out[name] = fieldWords
		default:
			out[name] = fieldLook
		}
	}
	return out
}

// schemaKinds reads a motion graphic's fields from its recorded JSON Schema.
func schemaKinds(schema json.RawMessage) fieldKinds {
	var s struct {
		Properties map[string]struct {
			Type   any    `json:"type"`
			Format string `json:"format"`
			Enum   []any  `json:"enum"`
			Const  any    `json:"const"`
			Items  *struct {
				Type any `json:"type"`
			} `json:"items"`
		} `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return nil
	}
	out := fieldKinds{}
	for name, p := range s.Properties {
		typ, _ := p.Type.(string)
		switch {
		case p.Format == ResourceFormat:
			out[name] = fieldPicture
		case len(p.Enum) > 0 || p.Const != nil:
			out[name] = fieldLook
		case typ == "string":
			out[name] = fieldWords
		case typ == "array" && p.Items != nil && p.Items.Type == "string":
			out[name] = fieldWords
		default:
			out[name] = fieldLook
		}
	}
	return out
}

// pieceKinds are the fields of the graphic that draws a piece (nil: unknown).
func pieceKinds(pc PackPiece, facts Facts) fieldKinds {
	if pc.Motion != nil {
		if info, ok := motionInfo(facts, pc.Motion.Hash); ok {
			return schemaKinds(info.Schema)
		}
		return nil
	}
	return templateKinds(pc.Template)
}

// sameWords are fields that say the same thing under another name, so words
// carry over when a graphic changes template: a card's title is an end
// card's headline.
var sameWords = map[string][]string{
	"title": {"headline"}, "headline": {"title"},
	"subtitle": {"subline"}, "subline": {"subtitle"},
	"text": {"quote"}, "quote": {"text"},
	"attribution": {"name"},
}

// carryName is the field a value goes to in a graphic with the given fields
// ("" when it has nowhere to go).
func carryName(field string, kinds fieldKinds) string {
	if _, ok := kinds[field]; ok {
		return field
	}
	for _, alt := range sameWords[field] {
		if _, ok := kinds[alt]; ok {
			return alt
		}
	}
	return ""
}

// templateFieldsProblem says what is wrong with a pack's choices for a
// vetted template's fields: each must be one of its fields and valid, words
// the cut doesn't give filled in with a sample to check them.
func templateFieldsProblem(template string, fields json.RawMessage) string {
	m, err := fieldsMap(fields)
	if err != nil {
		return "its fields aren't a list of names and values."
	}
	if len(m) == 0 {
		return ""
	}
	kinds := templateKinds(template)
	for k := range m {
		if _, ok := kinds[k]; !ok {
			return fmt.Sprintf("the %s template has no field %q.", TemplateWords(template), k)
		}
	}
	sample := sampleFields(m, kinds, requiredOf(template))
	raw, _ := json.Marshal(sample)
	if is := ValidateTemplateParams(template, raw, ""); len(is) > 0 {
		return strings.TrimSuffix(is[0].Message, ".") + "."
	}
	return ""
}

// motionFieldsProblem is templateFieldsProblem for a motion graphic.
func motionFieldsProblem(info MotionInfo, fields json.RawMessage) string {
	m, err := fieldsMap(fields)
	if err != nil {
		return "its fields aren't a list of names and values."
	}
	if len(m) == 0 {
		return ""
	}
	kinds := schemaKinds(info.Schema)
	for k := range m {
		if _, ok := kinds[k]; !ok {
			return fmt.Sprintf("the motion graphic %s has no field %q.", info.Name, k)
		}
	}
	var s struct {
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(info.Schema, &s)
	raw, _ := json.Marshal(sampleFields(m, kinds, s.Required))
	if is := ValidateMotionParams(info, raw, ""); len(is) > 0 {
		return strings.TrimSuffix(is[0].Message, ".") + "."
	}
	return ""
}

// sampleFields fills the required words a pack leaves to each cut with a
// sample, so the pack's own choices can be checked on their own.
func sampleFields(m map[string]json.RawMessage, kinds fieldKinds, required []string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	for k, v := range m {
		out[k] = v
	}
	for _, r := range required {
		if _, ok := out[r]; ok {
			continue
		}
		switch kinds[r] {
		case fieldWords:
			out[r] = json.RawMessage(`"Sample"`)
		case fieldPicture:
			out[r] = json.RawMessage(`"logo"`)
		}
	}
	return out
}

// requiredOf lists a vetted template's required fields.
func requiredOf(template string) []string {
	t, ok := TemplateByID(template)
	if !ok || t.params == nil {
		return nil
	}
	var out []string
	rt := reflect.TypeOf(t.params)
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		name, opts, _ := strings.Cut(f.Tag.Get("json"), ",")
		if name != "" && name != "-" && !strings.Contains(opts, "omitempty") {
			out = append(out, name)
		}
	}
	return out
}

func fieldsMap(raw json.RawMessage) (map[string]json.RawMessage, error) {
	m := map[string]json.RawMessage{}
	if len(raw) == 0 || string(raw) == "null" {
		return m, nil
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ---- filling and following roles ----

// brandLogoName is the cut's main logo by its pin name ("" none): the brand's
// main logo, else a pin called logo.
func brandLogoName(s *Spec) string {
	if s.Brand != nil && s.Brand.Logo != "" {
		return s.Brand.Logo
	}
	if _, ok := s.Resources["logo"]; ok {
		return "logo"
	}
	return ""
}

// pieceLimits are how short and long the piece's graphic may be (0, 0: unknown).
func pieceLimits(pc PackPiece, facts Facts) (lo, hi float64, persistent bool) {
	if pc.Motion != nil {
		if info, ok := motionInfo(facts, pc.Motion.Hash); ok {
			return info.MinDuration, info.MaxDuration, info.Persistent
		}
		return 0, 0, false
	}
	if t, ok := TemplateByID(pc.Template); ok {
		return t.MinDuration, t.MaxDuration, t.Persistent
	}
	return 0, 0, false
}

// followPiece makes one graphic that names a role drawn by the pack's piece:
// its words and pictures carried over (to the same field, or one that says the
// same thing), fields the new graphic doesn't have dropped, the pack's look
// set, the pack's words used only where the cut gives none, the logo when the
// pack shows it and the cut has none, and its length kept inside what the new
// graphic allows. It reports whether anything changed.
func followPiece(o *Overlay, pc PackPiece, logo string, facts Facts, length float64) bool {
	before, _ := json.Marshal(o)
	kinds := pieceKinds(pc, facts)
	own, err := fieldsMap(o.Params)
	if err != nil {
		return false
	}
	next := map[string]json.RawMessage{}
	sameGraphic := o.Template == pc.Template && ((o.Motion == nil && pc.Motion == nil) || (o.Motion != nil && pc.Motion != nil && o.Motion.Name == pc.Motion.Name))
	for k, v := range own {
		if kinds == nil {
			if sameGraphic {
				next[k] = v
			}
			continue
		}
		if to := carryName(k, kinds); to != "" {
			if _, taken := next[to]; !taken || to == k {
				next[to] = v
			}
		}
	}
	fixed, _ := fieldsMap(pc.Fields)
	for k, v := range fixed {
		if kinds != nil && kinds[k] == fieldLook {
			next[k] = v
			continue
		}
		if cur, has := next[k]; !has || string(cur) == `""` {
			next[k] = v
		}
	}
	if pc.Logo && logo != "" {
		if _, has := kinds["logo"]; has {
			if cur, ok := next["logo"]; !ok || string(cur) == `""` {
				next["logo"], _ = json.Marshal(logo)
			}
		}
	}
	if pc.Motion != nil {
		ref := *pc.Motion
		o.Template, o.Motion = "", &ref
	} else {
		o.Template, o.Motion = pc.Template, nil
	}
	if len(next) == 0 {
		o.Params = json.RawMessage(`{}`)
	} else {
		o.Params, _ = json.Marshal(next)
	}
	if o.Span == nil {
		lo, hi, persistent := pieceLimits(pc, facts)
		if o.Duration <= 0 {
			o.Duration = pc.Seconds
			if o.Duration <= 0 {
				o.Duration = math.Max(lo, 3)
			}
		}
		if lo > 0 && o.Duration < lo {
			o.Duration = lo
		}
		if hi > 0 && !persistent && o.Duration > hi {
			o.Duration = hi
		}
		o.Duration = math.Round(o.Duration*1000) / 1000
		if length > 0 && o.At+o.Duration > length+1e-9 {
			// A graphic that now lasts longer than the cut has left (an end
			// screen that needs 5 s) starts earlier and still ends with it.
			o.At = math.Max(0, math.Round((length-o.Duration)*1000)/1000)
		}
	}
	after, _ := json.Marshal(o)
	return string(before) != string(after)
}

// cutLength is how long a cut plays (seconds; 0 when it has no clips).
func cutLength(s *Spec) float64 {
	if len(s.Clips) == 0 {
		return 0
	}
	c := Clone(s)
	Normalize(c)
	if c.Output.FPS <= 0 {
		return 0
	}
	_, total := Timeline(c)
	return float64(total) / float64(c.Output.FPS)
}

// FillRoles fills the graphics of a change that name a role (D264): one that
// names no template or motion graphic gets the pack's piece. A new cut (base
// nil) also makes every graphic that names a role follow the pack, as the
// house style does with name titles (D199), and gives its call recordings the
// pack's layouts. A conversation the change adds to an existing cut gets the
// pack's layouts too. It reports whether anything changed.
func FillRoles(s *Spec, base *Spec, ps Packs, facts Facts) bool {
	changed := false
	logo := brandLogoName(s)
	length := -1.0 // worked out once, when a graphic needs it
	for i := range s.Overlays {
		o := &s.Overlays[i]
		if o.Role == "" || !GraphicRole(o.Role) {
			continue
		}
		pc, _, ok := ps.Piece(o.Role)
		if !ok {
			continue
		}
		unfilled := o.Template == "" && o.Motion == nil
		if unfilled || (base == nil && !o.Locked) {
			if length < 0 {
				length = cutLength(s)
			}
			if followPiece(o, pc, logo, facts, length) {
				changed = true
			}
		}
	}
	if ls := ps.Layouts(); ls != nil {
		for name, pin := range s.Conversations {
			if pin.LayoutStyle != nil {
				continue
			}
			if base != nil {
				if _, had := base.Conversations[name]; had {
					continue
				}
			}
			st := *ls
			pin.LayoutStyle = &st
			s.Conversations[name] = pin
			changed = true
		}
	}
	return changed
}

// FollowPack makes a cut follow its layout pack (D264, "Use the layout pack
// on the latest cut"): every unlocked graphic that names a role is drawn by
// the pack's piece, and its call recordings take the pack's layouts. It
// reports whether anything changed.
func FollowPack(s *Spec, ps Packs, facts Facts) bool {
	changed := false
	logo := brandLogoName(s)
	length := cutLength(s)
	for i := range s.Overlays {
		o := &s.Overlays[i]
		if o.Role == "" || o.Locked || !GraphicRole(o.Role) {
			continue
		}
		pc, _, ok := ps.Piece(o.Role)
		if ok && followPiece(o, pc, logo, facts, length) {
			changed = true
		}
	}
	want := ps.Layouts()
	for name, pin := range s.Conversations {
		cur, _ := json.Marshal(pin.LayoutStyle)
		next, _ := json.Marshal(want)
		if string(cur) == string(next) {
			continue
		}
		if want == nil {
			pin.LayoutStyle = nil
		} else {
			st := *want
			pin.LayoutStyle = &st
		}
		s.Conversations[name] = pin
		changed = true
	}
	return changed
}

// PackOps are the changes that make a cut (a saved, normalized one) follow
// its layout pack (FollowPack), as small ops: none when it follows it already.
func PackOps(s *Spec, ps Packs, facts Facts) ([]Op, error) {
	next := Clone(s)
	if !FollowPack(next, ps, facts) {
		return nil, nil
	}
	Normalize(next)
	return DiffOps(s, next)
}

// PackDrift is how one graphic that names a role differs from its pack (D267).
type PackDrift struct {
	Overlay string
	Role    string
	// Outside: the pack in use doesn't have the role (MetaRush's own fills it).
	Outside bool
	// Other: drawn by another template or motion graphic than the pack's.
	Other bool
	// Fields are the pack's look the graphic changed (alignment, motion…).
	Fields []string
	// Ops make it follow the pack (empty when it is locked).
	Ops []Op
}

// PackDrifts lists the graphics that name a role and don't follow the pack in
// use: a role the pack doesn't have, another graphic than the pack's, or the
// pack's look changed. Locked graphics are listed without a fix.
func PackDrifts(s *Spec, ps Packs, facts Facts) []PackDrift {
	var out []PackDrift
	logo := brandLogoName(s)
	length := cutLength(s)
	for _, o := range s.Overlays {
		if o.Role == "" || !GraphicRole(o.Role) {
			continue
		}
		pc, inPack, ok := ps.Piece(o.Role)
		if !ok {
			continue
		}
		d := PackDrift{Overlay: o.ID, Role: o.Role, Outside: !inPack}
		d.Other = o.Template != pc.Template || (o.Motion == nil) != (pc.Motion == nil) || (o.Motion != nil && (o.Motion.Name != pc.Motion.Name || o.Motion.Hash != pc.Motion.Hash))
		if !d.Other {
			own, _ := fieldsMap(o.Params)
			fixed, _ := fieldsMap(pc.Fields)
			kinds := pieceKinds(pc, facts)
			for _, k := range sortedRawKeys(fixed) {
				if kinds != nil && kinds[k] != fieldLook {
					continue
				}
				if cur, has := own[k]; !has || !sameJSON(cur, fixed[k]) {
					d.Fields = append(d.Fields, k)
				}
			}
		}
		if !d.Outside && !d.Other && len(d.Fields) == 0 {
			continue
		}
		if !o.Locked && (d.Other || len(d.Fields) > 0) {
			next := o
			if followPiece(&next, pc, logo, facts, length) {
				set, err := overlayPatch(o, next)
				if err == nil {
					d.Ops = []Op{{Op: "updateOverlay", ID: o.ID, Set: set}}
				}
			}
		}
		out = append(out, d)
	}
	return out
}

// overlayPatch is the merge patch from one graphic to another (updateOverlay's set).
func overlayPatch(a, b Overlay) (json.RawMessage, error) {
	p, err := fieldPatch(a, b, "id")
	if err == nil && p == nil {
		return nil, fmt.Errorf("no change")
	}
	return p, err
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return string(a) == string(b)
	}
	return reflect.DeepEqual(x, y)
}

func sortedRawKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func slicesContains(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func joinOr(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return aWord(xs[0])
	}
	words := make([]string, len(xs))
	for i, x := range xs {
		words[i] = aWord(x)
	}
	return strings.Join(words[:len(words)-1], ", ") + " or " + words[len(words)-1]
}

// layoutStyleIssues checks the layout styles of the cut's conversations (D265).
func layoutStyleIssues(s *Spec) []Issue {
	var out []Issue
	names := make([]string, 0, len(s.Conversations))
	for n := range s.Conversations {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if why := s.Conversations[n].LayoutStyle.Problem(); why != "" {
			out = append(out, errI("layout_style", "/conversations/"+n+"/layoutStyle", "The conversation %s: %s", n, why))
		}
	}
	return out
}

// NeedsPack reports a change the layout pack fills (FillRoles): a graphic
// that names a role without a template, a new cut with a role or a call
// recording, or a conversation added to an existing cut. Changes that need
// nothing from the pack don't read it.
func NeedsPack(s, base *Spec) bool {
	for _, o := range s.Overlays {
		if o.Role != "" && (base == nil || (o.Template == "" && o.Motion == nil)) {
			return true
		}
	}
	for name := range s.Conversations {
		if base == nil {
			return true
		}
		if _, had := base.Conversations[name]; !had {
			return true
		}
	}
	return false
}

// MotionHashes are the motion graphics the pack in use (and the starter) draw with.
func (ps Packs) MotionHashes() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range []*LayoutPack{ps.Own, ps.Starter} {
		if p == nil {
			continue
		}
		for _, pc := range p.Pieces {
			if pc.Motion != nil && !seen[pc.Motion.Hash] {
				seen[pc.Motion.Hash] = true
				out = append(out, pc.Motion.Hash)
			}
		}
	}
	sort.Strings(out)
	return out
}

// WithPackMotion is facts that also know a pack's motion graphics (infos, by
// hash), so following the pack carries a graphic's words into a motion graphic
// the cut doesn't use yet. Only the motion half is added; f must not be nil.
func WithPackMotion(f Facts, infos map[string]MotionInfo) Facts {
	return packMotionFacts{Facts: f, infos: infos}
}

type packMotionFacts struct {
	Facts
	infos map[string]MotionInfo
}

func (p packMotionFacts) Motion(hash string) (MotionInfo, bool) {
	if m, ok := p.infos[hash]; ok {
		return m, true
	}
	return motionInfo(p.Facts, hash)
}
