package editspec

import (
	"math"
	"slices"
)

// PlanConversation is a conversation clip's picture in the RenderPlan: its
// shots in clip-relative frames, each with its tiles. Its sound is in the
// plan's audio (one element per member, conversation: <clip>).
type PlanConversation struct {
	Name  string     `json:"name"`
	Shots []PlanShot `json:"shots"`
}

// PlanShot is one shot of a conversation clip.
type PlanShot struct {
	From             int        `json:"from" jsonschema:"description=Clip-relative frame where it starts."`
	DurationInFrames int        `json:"durationInFrames"`
	Kind             string     `json:"kind" jsonschema:"enum=member,enum=layout,enum=card,enum=broll"`
	Layout           string     `json:"layout,omitempty" jsonschema:"enum=side-by-side,enum=stacked,enum=pip,enum=grid"`
	Tiles            []PlanTile `json:"tiles"`
	Card             *PlanCard  `json:"card,omitempty"`
	// Backdrop is the colour between a layout's pictures when its style says
	// so (D265; absent: the cut's background, as always).
	Backdrop string `json:"backdrop,omitempty"`
	// DissolveFrames: the shot fades in over the one before for this long
	// (that one keeps playing underneath).
	DissolveFrames int `json:"dissolveFrames,omitempty"`
}

// PlanTile is one picture in a shot.
type PlanTile struct {
	Member  string   `json:"member,omitempty"`
	AssetID string   `json:"assetId"`
	Rect    PlanRect `json:"rect" jsonschema:"description=The tile in the frame (output pixels)."`
	// Video is where the whole source picture is drawn, relative to the tile;
	// Window is the visible part when the picture doesn't fill the tile.
	Video      PlanRect  `json:"video"`
	Window     *PlanRect `json:"window,omitempty"`
	TrimBefore int       `json:"trimBefore" jsonschema:"description=Frames (at the output rate) into the recording at the shot's first frame."`
	// TrimFraction is the rest of the trim below a frame, when the cut places
	// its recordings to the sample (D119): the picture follows the same exact
	// time as the person's sound.
	TrimFraction *float64 `json:"trimFraction,omitempty" jsonschema:"description=The rest of the trim below a frame (−0.5 to 0.5): trimBefore + trimFraction is the exact place in the recording."`
	PlaybackRate float64  `json:"playbackRate"`
	// Delay is how many frames into the shot the recording has no picture yet.
	Delay  int       `json:"delay,omitempty"`
	Color  []float64 `json:"color,omitempty" jsonschema:"description=An sRGB colour matrix (feColorMatrix values) that matches this webcam to the others."`
	Radius float64   `json:"radius,omitempty"`
	// Border is a line around the picture in one of the brand's colours (D265).
	Border *PlanBorder `json:"border,omitempty"`
	Tag    *PlanTag    `json:"tag,omitempty"`
	// Soft: drawn softened, behind a card.
	Soft bool `json:"soft,omitempty"`
}

// PlanBorder is a line around a tile's picture.
type PlanBorder struct {
	Width float64 `json:"width" jsonschema:"description=Output pixels."`
	Color string  `json:"color"`
}

// PlanTag is a name tag in a tile.
type PlanTag struct {
	Name  string `json:"name"`
	Title string `json:"title,omitempty"`
	// Cover is where the tile's recording shows a name a call app printed
	// (tile pixels): the tag sits over it, opaque, when the cut covers printed
	// names (it has a name bar, D182).
	Cover *PlanRect `json:"cover,omitempty"`
	// Box is where the tag sits when the tile isn't a rectangle (a round
	// inset, D265; tile pixels): centred at its foot, inside the circle.
	// Absent: the bottom-left corner, as always.
	Box *PlanRect `json:"box,omitempty"`
}

// PlanCard is a text card.
type PlanCard struct {
	Text string `json:"text"`
}

// attachConversations draws each conversation clip's shots and turns its
// sound into one audio element per member: their own track over the clip's
// sound window, at their offset and drift, with the clip's envelope (volume,
// ends, ducking, master) times theirs (the gate, their level, D101).
//
// Each element starts on a frame of the cut. A pin that places recordings to
// the sample (SampleAligned, D119) carries the exact place in the recording at
// that frame (trimBefore + trimFraction), so every person's track lines up
// with the others to the sample; older pins start each recording at the
// nearest frame of its own (up to one frame late), exactly as they rendered.
func attachConversations(s *Spec, plan *RenderPlan, cf *convFacts) {
	if cf == nil {
		return
	}
	fps := float64(s.Output.FPS)
	for i := range plan.Clips {
		pc := &plan.Clips[i]
		name := ConvName(pc.AssetID)
		cs := cf.srcs[name]
		if name == "" || cs == nil || i >= len(s.Clips) {
			continue
		}
		c := s.Clips[i]
		from, dur, env := pc.Sound()
		hadSound := !pc.Muted && pc.HasAudio
		pc.Gain, pc.Audio = nil, nil // the picture plays silently: the sound is the members' elements
		pc.Conversation = cf.planShots(s, c, *pc, cs, fps)
		if !hadSound || dur <= 0 {
			continue
		}
		act := cf.activity(cs)
		depth := cs.pin.GateDepth()
		rate := pc.PlaybackRate
		if rate <= 0 {
			rate = 1
		}
		clock := func(f float64) float64 { return pc.SourceIn + (f-float64(pc.From))/fps*rate }
		for _, m := range cs.pin.Members {
			if !m.HasSound() || slices.Contains(c.MutedMembers, m.ID) {
				continue
			}
			a, ok := cf.base.Asset(m.AssetID)
			if !ok || !a.HasAudio {
				continue
			}
			f0, f1 := from, from+dur
			// The recording may start later or end earlier than the window.
			if first := float64(pc.From) + (m.Start-pc.SourceIn)*fps/rate; first > float64(f0) {
				f0 = int(math.Ceil(first - 1e-9))
			}
			if a.Duration > 0 {
				if last := float64(pc.From) + (m.ClockAt(a.Duration)-pc.SourceIn)*fps/rate; last < float64(f1) {
					f1 = int(math.Floor(last + 1e-9))
				}
			}
			if f1-f0 < 1 {
				continue
			}
			tau0 := math.Max(0, m.SourceAt(clock(float64(f0))))
			trim := int(math.Round(tau0 * fps))
			var frac *float64
			if cs.pin.SampleAligned {
				trim, frac = SplitTrim(tau0 * fps)
			}
			g := newGate(act[m.ID], depth)
			lvl := cs.levels[m.ID]
			if s.Mix != nil {
				if v := s.Mix.VoiceFor(m.Name); v != nil && v.GainDB != nil {
					lvl += *v.GainDB
				}
			}
			lin := dbToLin(lvl)
			off := f0 - from
			envl := envelope(f1-f0, func(f int) float64 {
				return GainAt(env, float64(off+f)) * g.at(clock(float64(f0+f))) * lin
			})
			pa := PlanAudio{ID: c.ID + "/" + m.ID, AssetID: m.AssetID, Role: RoleDialogue, Bus: BusSource, From: f0, DurationInFrames: f1 - f0,
				TrimBefore: trim, TrimFraction: frac, PlaybackRate: round6(m.Rate()), Gain: envl, Conversation: c.ID, Member: m.ID}
			if s.Mix != nil {
				tau1 := m.SourceAt(clock(float64(f1)))
				if v := s.Mix.variantFor(m.AssetID, tau0, tau1, cf.base); v != nil {
					if frac != nil {
						pa.Variant = &PlanVariant{AssetID: v.Variant}
						pa.Variant.TrimBefore, pa.Variant.TrimFraction = SplitTrim((tau0 - v.Start) * fps)
					} else {
						pa.Variant = planVariant(v, trim, fps)
					}
				}
			}
			plan.Audio = append(plan.Audio, pa)
		}
	}
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// defaultShot is what a conversation clip without shots shows: its one
// picture, or everyone with a picture in the layout that suits the frame.
func defaultShot(pin ConversationPin, out Output) Shot {
	pics := pin.PictureMembers()
	switch {
	case len(pics) == 1:
		return Shot{Member: pics[0]}
	case len(pics) > 3:
		return Shot{Layout: LayoutGrid}
	case out.Height > out.Width:
		return Shot{Layout: LayoutStacked}
	}
	return Shot{Layout: LayoutSideBySide}
}

// planShots resolves a conversation clip's shots into frames and tiles.
func (cf *convFacts) planShots(s *Spec, c Clip, pc PlanClip, cs *convSource, fps float64) *PlanConversation {
	shots := c.Shots
	if len(shots) == 0 {
		shots = []Shot{defaultShot(cs.pin, s.Output)}
	}
	rate := pc.PlaybackRate
	if rate <= 0 {
		rate = 1
	}
	frameOf := func(t float64) int { return int(math.Round((t - pc.SourceIn) / rate * fps)) }
	first := max(0, ShotAt(shots, pc.SourceIn))
	type seg struct {
		sh   Shot
		a, b int
	}
	var segs []seg
	cur := seg{sh: shots[first], a: 0}
	for _, sh := range shots[first+1:] {
		f := frameOf(sh.At)
		if f <= cur.a {
			cur.sh = sh // starts at (or before) the clip's start
			continue
		}
		if f >= pc.DurationInFrames {
			break
		}
		cur.b = f
		segs = append(segs, cur)
		cur = seg{sh: sh, a: f}
	}
	cur.b = pc.DurationInFrames
	segs = append(segs, cur)
	out := &PlanConversation{Name: cs.name, Shots: []PlanShot{}}
	for k, sg := range segs {
		t0 := pc.SourceIn + float64(sg.a)/fps*rate
		t1 := pc.SourceIn + float64(sg.b)/fps*rate
		ps := cf.planShot(s, sg.sh, cs, t0, t1, fps)
		ps.From, ps.DurationInFrames = sg.a, sg.b-sg.a
		if k > 0 && sg.sh.Dissolve > 0 {
			d := int(math.Round(sg.sh.Dissolve * fps))
			d = min(d, ps.DurationInFrames/2, (segs[k-1].b-segs[k-1].a)/2)
			if d > 0 {
				ps.DissolveFrames = d
			}
		}
		out.Shots = append(out.Shots, ps)
	}
	return out
}

// planShot lays out one shot that shows [t0, t1) of the conversation.
func (cf *convFacts) planShot(s *Spec, sh Shot, cs *convSource, t0, t1, fps float64) PlanShot {
	W, H := s.Output.Width, s.Output.Height
	full := PlanRect{X: 0, Y: 0, W: float64(W), H: float64(H)}
	single := tileFrame{faceShare: singleFace, eyes: singleEyes, maxZoom: singleMaxZoom}
	tiled := tileFrame{faceShare: tileFace, eyes: tileEyes, maxZoom: tileMaxZoom}
	tags := cs.pin.NameTags != "off"
	switch sh.Kind() {
	case "member":
		m, ok := cs.pin.Member(sh.Member)
		if !ok || !m.HasPicture() {
			// A voice without a picture: their name on a card.
			name := sh.Member
			if ok {
				name = m.Name
			}
			return PlanShot{Kind: "card", Tiles: []PlanTile{}, Card: &PlanCard{Text: name}}
		}
		return PlanShot{Kind: "member", Tiles: []PlanTile{cf.memberTile(s, m, full, t0, t1, fps, single, false, cs.pin.SampleAligned)}}
	case "layout":
		ids := sh.Members
		if len(ids) == 0 {
			ids = cs.pin.PictureMembers()
		}
		var ms []ConversationMember
		for _, id := range ids {
			if m, ok := cs.pin.Member(id); ok && m.HasPicture() {
				ms = append(ms, m)
			}
		}
		lo, hi := LayoutCount(sh.Layout)
		if len(ms) > hi {
			ms = ms[:hi]
		}
		if len(ms) < lo {
			if len(ms) == 1 {
				return PlanShot{Kind: "member", Tiles: []PlanTile{cf.memberTile(s, ms[0], full, t0, t1, fps, single, false, cs.pin.SampleAligned)}}
			}
			return PlanShot{Kind: "card", Tiles: []PlanTile{}, Card: &PlanCard{Text: ""}}
		}
		st := cs.pin.LayoutStyle
		rects := LayoutTilesStyled(sh.Layout, len(ms), W, H, st)
		ps := PlanShot{Kind: "layout", Layout: sh.Layout, Tiles: []PlanTile{}}
		ins := make([]tileIn, len(ms))
		for i, m := range ms {
			ins[i] = cf.tileInput(s, m, rects[i], t0, t1)
		}
		var places []placed
		if sh.Layout == LayoutPiP {
			// The main picture and the inset differ in size: each is framed on its own.
			for i, in := range ins {
				tf := tiled
				if i == 0 {
					tf = single
				}
				v, w := placePicture(in.tw, in.th, in.sw, in.sh, in.crop, in.fr, in.face, tf)
				places = append(places, placed{v, w})
			}
		} else {
			places = placeMatched(ins, tiled) // matching face sizes and eye lines
		}
		for i, m := range ms {
			t := cf.tileOf(m, rects[i], places[i], t0, fps, false, cs.pin.SampleAligned)
			if sh.Layout == LayoutPiP && i > 0 {
				t.Radius = math.Round(0.012 * float64(min(W, H)))
			}
			styleTile(&t, s, st, sh.Layout, i)
			if tags {
				t.Tag = &PlanTag{Name: m.Name, Title: m.Tag}
			}
			if cv := tileTagCover(s, cf.base, t); cv != nil {
				// The app's printed name is covered by the tile's own tag (D182).
				name := m.Name
				if bar := nameBarOf(s); bar != nil && firstNamesOf(*bar) {
					name = firstName(name)
				}
				t.Tag = &PlanTag{Name: name, Title: m.Tag, Cover: cv}
			}
			if t.Tag != nil && t.Tag.Cover == nil && roundInset(st, sh.Layout, i) {
				t.Tag.Box = circleTagBox(t.Rect.W)
			}
			ps.Tiles = append(ps.Tiles, t)
		}
		ps.Backdrop = layoutBackdrop(s, st, sh.Layout)
		return ps
	case "card":
		ps := PlanShot{Kind: "card", Tiles: []PlanTile{}, Card: &PlanCard{Text: sh.Card}}
		for _, id := range sh.Members {
			if m, ok := cs.pin.Member(id); ok && m.HasPicture() {
				ps.Tiles = append(ps.Tiles, cf.memberTile(s, m, full, t0, t1, fps, single, true, cs.pin.SampleAligned))
				break
			}
		}
		return ps
	case "broll":
		b := sh.BRoll
		a, _ := cf.base.Asset(b.AssetID)
		at := b.In + math.Max(0, t0-sh.At)
		v, win := placePicture(full.W, full.H, float64(a.Width), float64(a.Height), nil, &Framing{}, nil, single)
		if ff, ok := cf.base.(FaceBoxFacts); ok && FacesFramed(s.Output) {
			if box, ok := ff.FaceBox(b.AssetID, at, at+(t1-t0)); ok {
				v, win = placePicture(full.W, full.H, float64(a.Width), float64(a.Height), nil, &Framing{}, &box, single)
			}
		}
		return PlanShot{Kind: "broll", Tiles: []PlanTile{{AssetID: b.AssetID, Rect: full, Video: v, Window: win, TrimBefore: int(math.Round(at * fps)), PlaybackRate: 1}}}
	}
	return PlanShot{Kind: "card", Tiles: []PlanTile{}, Card: &PlanCard{Text: ""}}
}

// memberTile is one member's picture in a tile, showing [t0, t1) of the
// conversation: framed by their face when the spec says so (D73), cropped,
// colour matched, at their offset and drift.
func (cf *convFacts) memberTile(s *Spec, m ConversationMember, r PlanRect, t0, t1, fps float64, tf tileFrame, soft, exact bool) PlanTile {
	in := cf.tileInput(s, m, r, t0, t1)
	v, win := placePicture(in.tw, in.th, in.sw, in.sh, in.crop, in.fr, in.face, tf)
	return cf.tileOf(m, r, placed{v, win}, t0, fps, soft, exact)
}

// tileInput is what placing a member's picture in a tile needs: the tile, the
// picture's size, the crop and framing, and the face over [t0, t1) when the
// spec frames by faces.
func (cf *convFacts) tileInput(s *Spec, m ConversationMember, r PlanRect, t0, t1 float64) tileIn {
	a, _ := cf.base.Asset(m.AssetID)
	tau0, tau1 := m.SourceAt(t0), m.SourceAt(t1)
	var face *[4]float64
	if FollowsFaces(Clip{Framing: m.Framing}, s.Output) {
		if ff, ok := cf.base.(FaceBoxFacts); ok {
			if box, ok := ff.FaceBox(m.AssetID, math.Max(0, tau0), math.Max(0, tau1)); ok {
				face = &box
			} else if box, ok := ff.FaceBox(m.AssetID, 0, math.Max(a.Duration, tau1)); ok {
				face = &box // nobody seen in this stretch: frame them as elsewhere in the recording
			}
		}
	}
	sw, sh := float64(a.Width), float64(a.Height)
	if sw <= 0 || sh <= 0 {
		sw, sh = 1280, 720
	}
	return tileIn{tw: r.W, th: r.H, sw: sw, sh: sh, crop: m.Crop, fr: m.Framing, face: face}
}

// tileOf is a member's tile with its picture placed, at their offset and
// drift. With exact placement (D119) the picture follows the very time the
// person's sound plays: the exact place in the recording at the shot's first
// frame, or, when the recording starts inside the shot, at the frame it
// first shows (it began part of a frame earlier).
func (cf *convFacts) tileOf(m ConversationMember, r PlanRect, p placed, t0, fps float64, soft, exact bool) PlanTile {
	t := PlanTile{Member: m.ID, AssetID: m.AssetID, Rect: r, Video: p.video, Window: p.window, PlaybackRate: round6(m.Rate()), Color: LookMatrix(m.Look), Soft: soft}
	if tau0 := m.SourceAt(t0); tau0 < 0 {
		t.Delay = int(math.Ceil(-tau0 / m.Rate() * fps))
		if exact {
			t.TrimBefore, t.TrimFraction = SplitTrim((tau0 + float64(t.Delay)/fps*m.Rate()) * fps)
		}
	} else {
		t.TrimBefore = int(math.Round(tau0 * fps))
		if exact {
			t.TrimBefore, t.TrimFraction = SplitTrim(tau0 * fps)
		}
	}
	return t
}

// ConversationTiles lists every recording a plan's conversation shots draw
// (for the media sources).
func conversationTileAssets(p *RenderPlan, add func(string)) {
	for _, c := range p.Clips {
		if c.Conversation == nil {
			continue
		}
		for _, sh := range c.Conversation.Shots {
			for _, t := range sh.Tiles {
				add(t.AssetID)
			}
		}
	}
}

// styleTile gives a layout's picture the shape and frame of its layout style
// (D265): the inset's corners (rounded, square, a circle) and a line in one
// of the brand's colours; side by side, rounded corners and the line. With no
// style the tile is as it always was.
func styleTile(t *PlanTile, s *Spec, st *LayoutStyle, layout string, i int) {
	if st == nil {
		return
	}
	short := math.Min(t.Rect.W, t.Rect.H)
	frame := ""
	switch layout {
	case LayoutPiP:
		p := st.PiP
		if p == nil || i == 0 {
			return
		}
		switch p.Shape {
		case "square":
			t.Radius = 0
		case "circle":
			t.Radius = math.Round(short / 2)
		}
		frame = p.Frame
	case LayoutSideBySide, LayoutStacked:
		sb := st.SideBySide
		if sb == nil {
			return
		}
		if sb.Rounded {
			t.Radius = math.Round(0.012 * float64(min(s.Output.Width, s.Output.Height)))
		}
		frame = sb.Frame
	default:
		return
	}
	if c := brandColor(s, frame); c != "" {
		t.Border = &PlanBorder{Width: math.Max(2, math.Round(0.004*float64(min(s.Output.Width, s.Output.Height)))), Color: c}
	}
}

// layoutBackdrop is the colour between a side-by-side layout's pictures when
// its style asks for the brand's background ("" otherwise).
func layoutBackdrop(s *Spec, st *LayoutStyle, layout string) string {
	if st == nil || st.SideBySide == nil || st.SideBySide.Backdrop != "brand" || (layout != LayoutSideBySide && layout != LayoutStacked) {
		return ""
	}
	return brandColor(s, "background")
}

// brandColor is one of the cut's brand colours (accent, primary, text,
// background) over MetaRush's defaults; "" for none.
func brandColor(s *Spec, which string) string {
	b := DefaultBrand
	if s.Brand != nil {
		mergeBrand(&b, *s.Brand)
	}
	switch which {
	case "accent":
		return b.Accent
	case "primary":
		return b.Primary
	case "text":
		return b.Text
	case "background":
		return b.Background
	}
	return ""
}

// roundInset reports picture in picture's inset drawn as a circle.
func roundInset(st *LayoutStyle, layout string, i int) bool {
	return st != nil && st.PiP != nil && st.PiP.Shape == "circle" && layout == LayoutPiP && i > 0
}

// circleTagBox is where a name tag fits in a round inset of diameter d: a
// band centred at its foot whose corners all lie inside the circle (a tag in
// the bottom-left corner would be cut off by its curve).
func circleTagBox(d float64) *PlanRect {
	return &PlanRect{X: round1(0.17 * d), Y: round1(0.68 * d), W: round1(0.66 * d), H: round1(0.18 * d)}
}
