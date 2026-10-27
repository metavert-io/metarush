package editspec

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// Conversations (D99, docs/conversations.md): several recordings of one talk —
// one per person, or per-person sound plus a shared picture such as Zoom's
// gallery view — aligned on one clock. A cut pins what it renders with in
// spec.conversations (each member's place on the clock, drift, picture
// settings, the sound's gate), and a clip whose assetId is conv:<name> plays
// it: its picture follows the clip's shots (one person, a layout, a text card,
// other footage) while its sound is every member's own track, mixed
// continuously. To the rest of the engine the clip looks like footage, as a
// nested cut does (D84): its length is the conversation's, its transcript
// everyone's words on the conversation's clock with exact speakers.

// ConvPrefix starts a conversation clip's source key.
const ConvPrefix = "conv:"

// ConversationNamePattern is a pinned conversation's short name (and a member's).
var ConversationNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

// ConvKey is the source key of a pinned conversation.
func ConvKey(name string) string { return ConvPrefix + name }

// IsConversation reports a conversation clip's source key.
func IsConversation(assetID string) bool { return strings.HasPrefix(assetID, ConvPrefix) }

// ConvName is the pin's name in a conversation source key ("" when it isn't one).
func ConvName(assetID string) string {
	if !IsConversation(assetID) {
		return ""
	}
	return strings.TrimPrefix(assetID, ConvPrefix)
}

// Member uses.
const (
	UseBoth    = "both"    // picture and sound of one person (the default)
	UseSound   = "sound"   // one person's sound only
	UsePicture = "picture" // a shared picture (gallery or speaker view): its sound is not mixed
)

// Gate modes.
const (
	GateSpeaking = "speaking" // each track gets quieter while its person isn't speaking
	GateOff      = "off"
)

// DefaultGateDepth is how much quieter a track gets while its person isn't
// speaking, in new conversation cuts (dB).
const DefaultGateDepth = 18.0

// Layouts (vetted; Go computes their geometry, LayoutTiles).
const (
	LayoutSideBySide = "side-by-side"
	LayoutStacked    = "stacked"
	LayoutPiP        = "pip"
	LayoutGrid       = "grid"
)

// Layouts lists the layouts in the order people see them.
var Layouts = []string{LayoutSideBySide, LayoutStacked, LayoutPiP, LayoutGrid}

// LayoutLabel is a layout's plain name.
func LayoutLabel(l string) string {
	switch l {
	case LayoutSideBySide:
		return "side by side"
	case LayoutStacked:
		return "stacked"
	case LayoutPiP:
		return "picture in picture"
	case LayoutGrid:
		return "grid"
	}
	return l
}

// ConversationPin is the conversation a cut plays, as it renders it.
type ConversationPin struct {
	ID      string               `json:"id" bson:"id" jsonschema:"pattern=^cnv_[0-9a-z]{26}$,description=The conversation in the library."`
	Members []ConversationMember `json:"members" bson:"members" jsonschema:"minItems=1,maxItems=16"`
	// Gate is how each person's track gets quieter while they aren't speaking
	// (absent: it never does).
	Gate *ConversationGate `json:"gate,omitempty" bson:"gate,omitempty"`
	// NameTags off hides the name tags in layouts (on by default).
	NameTags string `json:"nameTags,omitempty" bson:"nameTags,omitempty" jsonschema:"enum=on,enum=off,description=Name tags in layouts: on (default) or off."`
	// SampleAligned places each recording exactly where its start and drift
	// say, to the sample, in the render and the preview (D119). Cuts pinned
	// before it existed leave it out: each recording then starts on the cut's
	// next frame (up to one frame late), as they always rendered.
	SampleAligned bool `json:"sampleAligned,omitempty" bson:"sampleAligned,omitempty" jsonschema:"description=Each recording plays exactly where its start says\\, to the sample (new cuts). Absent: from the cut's next frame\\, as cuts made before it rendered."`
	// LayoutStyle is how its picture-in-picture and side-by-side layouts look
	// (D265): from the layout pack when the cut is made or the pack is used on
	// it. Absent: as they always looked.
	LayoutStyle *LayoutStyle `json:"layoutStyle,omitempty" bson:"layoutStyle,omitempty" jsonschema:"description=How its picture-in-picture and side-by-side layouts look (from the layout pack). Absent: as they always looked."`
}

// ConversationGate is the sound's gating by who is speaking (D101).
type ConversationGate struct {
	Mode    string   `json:"mode" bson:"mode" jsonschema:"enum=speaking,enum=off,description=speaking: each track gets quieter while its person isn't speaking (less echo and bleed when nobody wore headphones); off: every track plays at its level throughout."`
	DepthDB *float64 `json:"depthDb,omitempty" bson:"depthDb,omitempty" jsonschema:"minimum=6,maximum=40,description=How much quieter (dB; default 18)."`
}

// ConversationMember is one recording of the conversation.
type ConversationMember struct {
	ID      string `json:"id" bson:"id" jsonschema:"pattern=^[a-z][a-z0-9_-]{0\\,47}$,description=The short name shots use."`
	Name    string `json:"name" bson:"name" jsonschema:"minLength=1,maxLength=80,description=The person's name (name tags\\, captions; per-person sound settings find them by it)."`
	AssetID string `json:"assetId" bson:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	// Use is what the recording gives: both (default), sound or picture.
	Use string `json:"use,omitempty" bson:"use,omitempty" jsonschema:"enum=both,enum=sound,enum=picture,description=both: picture and sound of one person (default); sound: one person's audio; picture: a shared picture such as a gallery view (its sound is not mixed)."`
	// Start is where the recording starts on the conversation's clock (seconds).
	Start float64 `json:"start" bson:"start" jsonschema:"minimum=-36000,maximum=36000,description=Where the recording starts on the conversation's clock (seconds; negative when it started earlier)."`
	// Drift is how much faster the recording's clock runs (parts per million).
	Drift    float64 `json:"drift,omitempty" bson:"drift,omitempty" jsonschema:"minimum=-5000,maximum=5000,description=Clock drift in parts per million: recording time = (t − start) × (1 + drift/10⁶)."`
	PersonID string  `json:"personId,omitempty" bson:"personId,omitempty" jsonschema:"pattern=^per_[0-9a-z]{26}$"`
	Tag      string  `json:"tag,omitempty" bson:"tag,omitempty" jsonschema:"maxLength=80,description=The name tag's second line (a role\\, from People's notes)."`
	// Crop removes app chrome (name labels, banners) from the picture.
	Crop    *Crop    `json:"crop,omitempty" bson:"crop,omitempty"`
	Framing *Framing `json:"framing,omitempty" bson:"framing,omitempty" jsonschema:"description=How this person's picture is framed (fill or fit\\, faces\\, focus\\, zoom)."`
	Look    *Look    `json:"look,omitempty" bson:"look,omitempty"`
	Muted   bool     `json:"muted,omitempty" bson:"muted,omitempty" jsonschema:"description=Leave this recording's sound out."`
}

// Crop is how much of each edge of a picture is removed (fractions).
type Crop struct {
	Left   float64 `json:"left,omitempty" bson:"left,omitempty" jsonschema:"minimum=0,maximum=0.45"`
	Top    float64 `json:"top,omitempty" bson:"top,omitempty" jsonschema:"minimum=0,maximum=0.45"`
	Right  float64 `json:"right,omitempty" bson:"right,omitempty" jsonschema:"minimum=0,maximum=0.45"`
	Bottom float64 `json:"bottom,omitempty" bson:"bottom,omitempty" jsonschema:"minimum=0,maximum=0.45"`
}

// Look is a simple colour and brightness correction (webcams that match, D103).
// Absent fields are neutral.
type Look struct {
	Exposure   float64 `json:"exposure,omitempty" bson:"exposure,omitempty" jsonschema:"minimum=0.5,maximum=2,description=Brightness as a gain (1 unchanged)."`
	Contrast   float64 `json:"contrast,omitempty" bson:"contrast,omitempty" jsonschema:"minimum=0.5,maximum=1.5,description=Around mid grey (1 unchanged)."`
	Saturation float64 `json:"saturation,omitempty" bson:"saturation,omitempty" jsonschema:"minimum=0.2,maximum=2,description=1 unchanged."`
	Warmth     float64 `json:"warmth,omitempty" bson:"warmth,omitempty" jsonschema:"minimum=-0.3,maximum=0.3,description=Positive: warmer (more red\\, less blue)."`
	Tint       float64 `json:"tint,omitempty" bson:"tint,omitempty" jsonschema:"minimum=-0.3,maximum=0.3,description=Positive: greener; negative: more magenta."`
}

// Shot is what a conversation clip shows from At (conversation seconds) until
// the next shot: exactly one of Member, Layout, Card and BRoll.
type Shot struct {
	At     float64 `json:"at" bson:"at" jsonschema:"minimum=0,description=Where it starts on the conversation's clock (seconds)."`
	Member string  `json:"member,omitempty" bson:"member,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]{0\\,47}$,description=One person full frame."`
	Layout string  `json:"layout,omitempty" bson:"layout,omitempty" jsonschema:"enum=side-by-side,enum=stacked,enum=pip,enum=grid"`
	// Members are a layout's people in order (default: everyone with a
	// picture); a card's first member is seen softened behind it.
	Members []string   `json:"members,omitempty" bson:"members,omitempty" jsonschema:"maxItems=9"`
	Card    string     `json:"card,omitempty" bson:"card,omitempty" jsonschema:"maxLength=240,description=A text card (an off-camera interviewer's question)."`
	BRoll   *ShotBRoll `json:"broll,omitempty" bson:"broll,omitempty"`
	// Dissolve is how long the switch into this shot blends (seconds; 0 cuts).
	Dissolve  float64 `json:"dissolve,omitempty" bson:"dissolve,omitempty" jsonschema:"minimum=0,maximum=2"`
	Rationale string  `json:"rationale,omitempty" bson:"rationale,omitempty" jsonschema:"maxLength=1000"`
}

// ShotBRoll is other footage shown while the conversation's sound continues.
type ShotBRoll struct {
	AssetID string  `json:"assetId" bson:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	In      float64 `json:"in" bson:"in" jsonschema:"minimum=0,description=Where in that footage it starts (seconds)."`
}

// Kind is the shot's kind: member, layout, card or broll ("" when it is none
// or more than one).
func (sh Shot) Kind() string {
	kinds := 0
	k := ""
	if sh.Member != "" {
		kinds, k = kinds+1, "member"
	}
	if sh.Layout != "" {
		kinds, k = kinds+1, "layout"
	}
	if sh.Card != "" {
		kinds, k = kinds+1, "card"
	}
	if sh.BRoll != nil {
		kinds, k = kinds+1, "broll"
	}
	if kinds != 1 {
		return ""
	}
	return k
}

// UseOf is a member's use (default both).
func (m ConversationMember) UseOf() string {
	if m.Use == "" {
		return UseBoth
	}
	return m.Use
}

// HasPicture reports whether the member's picture can be shown.
func (m ConversationMember) HasPicture() bool { return m.UseOf() != UseSound }

// HasSound reports whether the member's sound is mixed.
func (m ConversationMember) HasSound() bool { return m.UseOf() != UsePicture && !m.Muted }

// Rate is the member's recording seconds per conversation second.
func (m ConversationMember) Rate() float64 { return 1 + m.Drift/1e6 }

// SourceAt is the member's recording time at conversation time t.
func (m ConversationMember) SourceAt(t float64) float64 { return (t - m.Start) * m.Rate() }

// ClockAt is the conversation time at the member's recording time tau.
func (m ConversationMember) ClockAt(tau float64) float64 { return m.Start + tau/m.Rate() }

// Member finds a member by its short name.
func (p *ConversationPin) Member(id string) (ConversationMember, bool) {
	if p == nil {
		return ConversationMember{}, false
	}
	for _, m := range p.Members {
		if m.ID == id {
			return m, true
		}
	}
	return ConversationMember{}, false
}

// PictureMembers lists the members whose picture can be shown, in order.
func (p *ConversationPin) PictureMembers() []string {
	var out []string
	if p == nil {
		return out
	}
	for _, m := range p.Members {
		if m.HasPicture() {
			out = append(out, m.ID)
		}
	}
	return out
}

// PeopleMembers lists the members who are one person (both or sound), in order.
func (p *ConversationPin) PeopleMembers() []string {
	var out []string
	if p == nil {
		return out
	}
	for _, m := range p.Members {
		if m.UseOf() != UsePicture {
			out = append(out, m.ID)
		}
	}
	return out
}

// GateDepth is the gate's depth in dB (0 when the gate is off).
func (p *ConversationPin) GateDepth() float64 {
	if p == nil || p.Gate == nil || p.Gate.Mode != GateSpeaking {
		return 0
	}
	if p.Gate.DepthDB != nil {
		return math.Max(6, math.Min(40, math.Abs(*p.Gate.DepthDB)))
	}
	return DefaultGateDepth
}

// NewGate is the gate new conversation cuts are written with.
func NewGate() *ConversationGate {
	d := DefaultGateDepth
	return &ConversationGate{Mode: GateSpeaking, DepthDB: &d}
}

// ShotAt is the shot showing at conversation time t: the last one starting at
// or before t, else the first (its index; -1 without shots).
func ShotAt(shots []Shot, t float64) int {
	if len(shots) == 0 {
		return -1
	}
	i := sort.Search(len(shots), func(i int) bool { return shots[i].At > t+1e-9 })
	if i == 0 {
		return 0
	}
	return i - 1
}

// ShotsFor are the shots of a clip cut down to [in, out): the shot showing at
// in (starting at in) and those that start inside. Splits, trims and removed
// stretches use it so every part keeps its picture (D99).
func ShotsFor(shots []Shot, in, out float64) []Shot {
	if len(shots) == 0 {
		return nil
	}
	var res []Shot
	if i := ShotAt(shots, in); i >= 0 {
		first := shots[i]
		first.At = round3(in)
		res = append(res, first)
	}
	for _, sh := range shots {
		if sh.At > in+1e-9 && sh.At < out-1e-9 {
			res = append(res, sh)
		}
	}
	return res
}

// ConversationAssetIDs lists the recordings and footage the spec's
// conversations play (members and B-roll), in order.
func ConversationAssetIDs(s *Spec) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, name := range sortedConvNames(s) {
		for _, m := range s.Conversations[name].Members {
			add(m.AssetID)
		}
	}
	for _, c := range s.Clips {
		for _, sh := range c.Shots {
			if sh.BRoll != nil {
				add(sh.BRoll.AssetID)
			}
		}
	}
	return out
}

func sortedConvNames(s *Spec) []string {
	names := make([]string, 0, len(s.Conversations))
	for n := range s.Conversations {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// HasConversations reports whether a spec plays a conversation (or pins one).
func HasConversations(s *Spec) bool {
	if s == nil {
		return false
	}
	if len(s.Conversations) > 0 {
		return true
	}
	for _, c := range s.Clips {
		if IsConversation(c.AssetID) {
			return true
		}
	}
	return false
}

// normalizeConversations rounds a spec's conversation numbers (stable hashes,
// lossless cut sheets): times to the millisecond, drift to a tenth of a ppm,
// fractions and looks to three decimals, shots sorted by time.
func normalizeConversations(s *Spec) {
	for name, p := range s.Conversations {
		for i := range p.Members {
			m := &p.Members[i]
			m.Start = round3(m.Start)
			m.Drift = math.Round(m.Drift*10) / 10
			if c := m.Crop; c != nil {
				c.Left, c.Top, c.Right, c.Bottom = round3(c.Left), round3(c.Top), round3(c.Right), round3(c.Bottom)
			}
			if l := m.Look; l != nil {
				l.Exposure, l.Contrast, l.Saturation, l.Warmth, l.Tint = round3(l.Exposure), round3(l.Contrast), round3(l.Saturation), round3(l.Warmth), round3(l.Tint)
			}
		}
		if g := p.Gate; g != nil && g.DepthDB != nil {
			x := math.Round(math.Abs(*g.DepthDB)*10) / 10
			g.DepthDB = &x
		}
		s.Conversations[name] = p
	}
	for i := range s.Clips {
		c := &s.Clips[i]
		for j := range c.Shots {
			c.Shots[j].At = round3(c.Shots[j].At)
			c.Shots[j].Dissolve = round3(c.Shots[j].Dissolve)
			if b := c.Shots[j].BRoll; b != nil {
				b.In = round3(b.In)
			}
		}
		sort.SliceStable(c.Shots, func(a, b int) bool { return c.Shots[a].At < c.Shots[b].At })
	}
}

// CoverShots shows another member over [from, to) of the conversation's clock
// (a frozen picture, say) and then goes back to what was showing at to: shots
// starting inside the stretch give way. The result is sorted.
func CoverShots(shots []Shot, from, to float64, member, why, back string) []Shot {
	from, to = round3(from), round3(to)
	resume := Shot{At: to}
	if k := ShotAt(shots, to); k >= 0 {
		resume = shots[k]
		resume.At = to
	}
	resume.Rationale = back
	out := make([]Shot, 0, len(shots)+2)
	for _, sh := range shots {
		if sh.At < from || sh.At >= to {
			out = append(out, sh)
		}
	}
	out = append(out, Shot{At: from, Member: member, Rationale: why})
	if resume.Kind() != "" {
		out = append(out, resume)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// ClipShots are a conversation clip's shots, or the one shot it shows without
// any (from its start), so edits can start from what is on screen.
func ClipShots(s *Spec, c Clip) []Shot {
	if len(c.Shots) > 0 || !IsConversation(c.AssetID) {
		return append([]Shot(nil), c.Shots...)
	}
	pin, ok := s.Conversations[ConvName(c.AssetID)]
	if !ok {
		return nil
	}
	sh := defaultShot(pin, s.Output)
	sh.At = c.In
	return []Shot{sh}
}
