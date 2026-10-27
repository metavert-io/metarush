// Package interchange writes a cut as a timeline other editing apps open: an
// escape hatch to DaVinci Resolve, Final Cut Pro and Premiere Pro (D92).
//
// The timeline is built from the resolved RenderPlan (D6), so pictures, split
// edits, transitions, sound levels and markers sit where MetaRush plays them.
// Three formats are written from one editor-neutral model:
//
//   - OpenTimelineIO JSON (.otio; Resolve imports it natively),
//   - FCPXML 1.8 (.fcpxml; Final Cut Pro and Resolve),
//   - Final Cut Pro 7 XML, "xmeml" version 5 (.xml; Premiere Pro, Resolve).
//
// What each format keeps and loses is in docs/timeline-export.md; every
// export also returns plain notes for what it could not carry exactly.
package interchange

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Formats.
const (
	FormatOTIO   = "otio"
	FormatFCPXML = "fcpxml"
	FormatXMEML  = "xmeml"
)

// Formats lists the formats in the order people see them.
var Formats = []string{FormatOTIO, FormatFCPXML, FormatXMEML}

// Extension is the file extension of a format.
func Extension(format string) string {
	switch format {
	case FormatOTIO:
		return ".otio"
	case FormatFCPXML:
		return ".fcpxml"
	case FormatXMEML:
		return ".xml"
	}
	return ""
}

// Track kinds and roles.
const (
	KindVideo = "video"
	KindAudio = "audio"

	RolePicture   = "picture"
	RoleClipSound = "clip-sound"
	RoleNarration = "narration"
	RoleMusic     = "music"
	RoleEffects   = "effects"
	RoleDialogue  = "dialogue"
)

// Transition kinds (picture).
const (
	TransDissolve = "dissolve"
	TransDipBlack = "dip-black"
	TransDipWhite = "dip-white"
	TransWipe     = "wipe"
	TransSlide    = "slide"
	TransPush     = "push"
	TransZoom     = "zoom"
	TransWhip     = "whip"
)

// Media is one file the timeline plays.
type Media struct {
	ID         string  // asset ID
	Name       string  // readable file name the editor sees ("Kitchen interview.mov")
	Path       string  // absolute local path of the file the timeline points to
	SHA256     string  // the bytes (content address)
	Duration   float64 // seconds
	FPS        float64 // the picture's frame rate (0 for audio-only files)
	Width      int
	Height     int
	HasVideo   bool
	HasAudio   bool
	Channels   int // audio channels (0 when unknown)
	SampleRate int // Hz (0 when unknown)
}

// Keys of MetaRush's own note on a file in FCPXML and Final Cut Pro 7 XML
// (OpenTimelineIO keeps it under "metarush" metadata).
const (
	NoteAssetID = "metarush.assetId"
	NoteSHA256  = "metarush.sha256"
)

// metaRushNote is what MetaRush notes on a file it exports: its clip and bytes.
func metaRushNote(m *Media) [][2]string {
	var out [][2]string
	if m.ID != "" {
		out = append(out, [2]string{NoteAssetID, m.ID})
	}
	if m.SHA256 != "" {
		out = append(out, [2]string{NoteSHA256, m.SHA256})
	}
	return out
}

// Item is one piece of footage, sound or a nested cut on a track. Start and
// Duration are MetaRush's own window in timeline frames: picture items can
// overlap their neighbour under an overlapping transition, sound items under a
// blend. Writers turn overlaps into the format's transitions or fades.
type Item struct {
	Name     string
	Media    *Media
	Nested   *Timeline // a nested cut instead of a file (compound clip)
	Start    int       // timeline frame
	Duration int       // timeline frames
	// Source is where the item starts in its media, in timeline frames
	// (seconds × the timeline's fps); with Speed the media advances Speed
	// frames per timeline frame.
	Source float64
	Speed  float64
	// FadeIn and FadeOut are frames at the head and tail: from and to black for
	// pictures, from and to silence for sound (Fade.Kind says which picture fades are dips).
	FadeIn, FadeOut int
	// LevelDB is a sound item's level; Keys are level changes inside it
	// (ducking), at item-relative frames, only when the level changes.
	LevelDB float64
	Keys    []LevelKey
	Link    string // the clip: a clip's picture and sound share it
	Role    string
	Muted   bool // kept on the track but silent (a muted bus)
	Framing *editspec.PlanFraming
}

// End is the item's last frame + 1.
func (it *Item) End() int { return it.Start + it.Duration }

// LevelKey is a level at an item-relative frame.
type LevelKey struct {
	Frame int
	DB    float64
}

// Cut describes how a picture item enters from the one before it.
type Cut struct {
	Kind      string // a Trans* kind; "" for a straight cut
	Overlap   int    // frames both pictures play (overlapping transitions); 0 for dips
	DipOut    int    // dips: frames the outgoing picture fades to the colour
	DipIn     int    // dips: frames the incoming picture fades up
	Direction string
	Easing    string
	From      string // the MetaRush transition type as written in the cut
}

// Track is a lane of items in time order.
type Track struct {
	Name  string
	Kind  string
	Role  string
	Items []*Item
	// Cuts[i] is how Items[i] enters from Items[i-1] (picture tracks only).
	Cuts []Cut
}

// Marker marks a moment or a range on the timeline.
type Marker struct {
	Start    int
	Duration int
	Kind     string // caption | graphic | narration
	Name     string
	Comment  string
}

// Timeline is an editor-neutral cut.
type Timeline struct {
	Name     string
	FPS      int
	Width    int
	Height   int
	Duration int // frames
	Video    []*Track
	Audio    []*Track
	Markers  []Marker
	Media    []*Media
	// Notes say, in plain words, what the timeline could not carry exactly.
	Notes []string
	// Captions are the burned-in caption lines (also written as an SRT file).
	Captions []Caption
}

// Caption is one caption line in timeline frames.
type Caption struct {
	From, To int
	Text     string
	Speaker  string
}

// BuildOptions supply what the plan does not carry.
type BuildOptions struct {
	Name string
	// Media returns the file for an asset (nil: the asset is left out with a note).
	Media func(assetID string) *Media
	// Spec is the cut the plan was resolved from (names of clips and sounds).
	Spec *editspec.Spec
	// Names are readable names of clips and sounds by their spec IDs ("Sam's answer").
	Names map[string]string
}

// Build turns a resolved RenderPlan into a timeline.
func Build(plan *editspec.RenderPlan, o BuildOptions) (*Timeline, error) {
	if plan == nil || plan.FPS <= 0 {
		return nil, fmt.Errorf("the cut couldn't be worked out frame by frame")
	}
	if o.Media == nil {
		o.Media = func(string) *Media { return nil }
	}
	tl := &Timeline{Name: o.Name, FPS: plan.FPS, Width: plan.Width, Height: plan.Height, Duration: plan.DurationInFrames}
	seen := map[string]bool{}
	media := func(id string) *Media {
		m := o.Media(id)
		if m != nil && !seen[m.ID] {
			seen[m.ID] = true
			tl.Media = append(tl.Media, m)
		}
		return m
	}
	name := func(id, fallback string) string {
		if n := o.Names[id]; n != "" {
			return n
		}
		return fallback
	}

	// Picture.
	pic := &Track{Name: "Picture", Kind: KindVideo, Role: RolePicture}
	missing := map[string]bool{}
	for i, pc := range plan.Clips {
		if pc.Conversation != nil {
			// A conversation (D99): one picture item per shot from the recording
			// it shows; layouts and cards become markers (their sound is every
			// person's own track, among the sounds below).
			conversationPicture(tl, pic, pc, media, name)
			continue
		}
		m := media(pc.AssetID)
		if m == nil {
			missing[pc.AssetID] = true
			continue
		}
		fr := pc.Framing
		it := &Item{Name: name(pc.ID, mediaTitle(m)), Media: m, Start: pc.From, Duration: pc.DurationInFrames, Source: float64(pc.TrimBefore),
			Speed: speed(pc.PlaybackRate), Link: "clip:" + pc.ID, Role: RolePicture, Framing: &fr}
		cut := Cut{}
		tr := pc.TransitionIn
		switch {
		case tr.Type == "" || tr.Type == editspec.TransitionCut || tr.DurationInFrames <= 0:
		case editspec.IsOverlapTransition(tr.Type) && i > 0 && len(pic.Items) > 0:
			prev := pic.Items[len(pic.Items)-1]
			cut = Cut{Kind: pictureKind(tr.Type), Overlap: max(0, prev.End()-pc.From), Direction: tr.Direction, Easing: tr.Easing, From: tr.Type}
			if cut.Overlap == 0 {
				cut.Kind = ""
			}
		case editspec.IsDipTransition(tr.Type) && i > 0 && len(pic.Items) > 0:
			kind := TransDipBlack
			if tr.Type == editspec.TransitionDipWhite {
				kind = TransDipWhite
			}
			out := tr.DurationInFrames / 2
			cut = Cut{Kind: kind, DipOut: out, DipIn: tr.DurationInFrames - out, From: tr.Type}
		default: // a fade in from the background (also an overlapping type on the first clip)
			it.FadeIn = tr.DurationInFrames
		}
		pic.Items = append(pic.Items, it)
		pic.Cuts = append(pic.Cuts, cut)
		if pc.FadeOutFrames > 0 {
			it.FadeOut = pc.FadeOutFrames
		}
	}
	// A dip's first half is the outgoing clip's fade-out (Timeline sets it):
	// keep only a fade-out longer than the dip asks for.
	for i := 1; i < len(pic.Items); i++ {
		if c := pic.Cuts[i]; c.Kind == TransDipBlack || c.Kind == TransDipWhite {
			if pic.Items[i-1].FadeOut <= c.DipOut {
				pic.Items[i-1].FadeOut = 0
			}
		}
	}
	tl.Video = append(tl.Video, pic)

	// Sound: each clip's own sound over its sound window.
	var clipSound []*Item
	for _, pc := range plan.Clips {
		if pc.Muted || !pc.HasAudio {
			continue
		}
		m := media(pc.AssetID)
		if m == nil || !m.HasAudio {
			continue
		}
		from, dur, gain := pc.Sound()
		trim := pc.TrimBefore
		if pc.Audio != nil {
			trim = pc.Audio.TrimBefore
			if v := pc.Audio.Variant; v != nil { // a clearer voice plays instead (D88)
				if mv := media(v.AssetID); mv != nil {
					m, trim = mv, v.TrimBefore
				}
			}
		}
		it := &Item{Name: name(pc.ID, mediaTitle(m)), Media: m, Start: from, Duration: dur, Source: float64(trim), Speed: speed(pc.PlaybackRate), Link: "clip:" + pc.ID, Role: RoleClipSound}
		shapeLevels(it, gain, fallbackGain(pc.Volume), -1, -1)
		clipSound = append(clipSound, it)
	}
	tl.Audio = append(tl.Audio, layAudio("Clip sound", RoleClipSound, clipSound)...)

	// Narration, music, effects and added dialogue.
	byRole := map[string][]*Item{}
	var roles []string
	for _, a := range plan.Audio {
		var m *Media
		if v := a.Variant; v != nil { // a clearer voice plays instead (D88)
			if m = media(v.AssetID); m != nil {
				a.TrimBefore = v.TrimBefore
			}
		}
		if m == nil {
			m = media(a.AssetID)
		}
		if m == nil {
			missing[a.AssetID] = true
			continue
		}
		role := audioRole(a.Role)
		id, link := a.ID, "audio:"+a.ID
		if a.BeatID != "" {
			id, link = a.BeatID, "beat:"+a.BeatID+":"+a.ID
		}
		it := &Item{Name: name(id, mediaTitle(m)), Media: m, Start: a.From, Duration: a.DurationInFrames, Source: float64(a.TrimBefore), Speed: speed(a.PlaybackRate), Link: link, Role: role}
		if len(a.Gain) > 0 {
			shapeLevels(it, a.Gain, 1, a.FadeInFrames, a.FadeOutFrames)
		} else {
			it.LevelDB = round1(a.GainDB)
			it.FadeIn, it.FadeOut = a.FadeInFrames, a.FadeOutFrames
		}
		if byRole[role] == nil {
			roles = append(roles, role)
		}
		byRole[role] = append(byRole[role], it)
	}
	sort.SliceStable(roles, func(i, j int) bool { return roleOrder(roles[i]) < roleOrder(roles[j]) })
	for _, r := range roles {
		tl.Audio = append(tl.Audio, layAudio(roleTrackName(r), r, byRole[r])...)
	}

	// Markers: captions, graphics, narration lines without a recording.
	if c := plan.Captions; c != nil {
		for _, l := range c.Lines {
			text := l.Text
			if c.Uppercase {
				text = strings.ToUpper(text)
			}
			tl.Captions = append(tl.Captions, Caption{From: l.From, To: l.To, Text: text, Speaker: l.Speaker})
			label := text
			if l.Speaker != "" && c.ShowSpeakers {
				label = l.Speaker + ": " + text
			}
			tl.Markers = append(tl.Markers, Marker{Start: l.From, Duration: max(1, l.To-l.From), Kind: "caption", Name: "Caption: " + label})
		}
	}
	for _, ov := range plan.Overlays {
		tl.Markers = append(tl.Markers, Marker{Start: ov.From, Duration: max(1, ov.DurationInFrames), Kind: "graphic", Name: "Graphic: " + describeOverlay(ov), Comment: string(ov.Params)})
	}
	if v := plan.Voiceover; v != nil {
		for _, b := range v.Beats {
			if b.HasAudio || b.Fit == "unresolved" {
				continue
			}
			tl.Markers = append(tl.Markers, Marker{Start: b.From, Duration: max(1, b.To-b.From), Kind: "narration", Name: fmt.Sprintf("Narration line %d, not recorded yet: %s", b.Index+1, b.Text)})
		}
	}
	sort.SliceStable(tl.Markers, func(i, j int) bool { return tl.Markers[i].Start < tl.Markers[j].Start })

	if len(missing) > 0 {
		tl.Notes = append(tl.Notes, fmt.Sprintf("%d file(s) could not be found and were left out", len(missing)))
	}
	if len(pic.Items) == 0 {
		return nil, fmt.Errorf("the cut has no footage to export")
	}
	return tl, nil
}

// conversationPicture lays a conversation clip's shots on the picture track:
// a person or b-roll is that recording from where the shot starts; a layout
// is its first person's picture with a marker naming the layout; a card is a
// marker with its text (D99).
func conversationPicture(tl *Timeline, pic *Track, pc editspec.PlanClip, media func(string) *Media, name func(id, fallback string) string) {
	noted := false
	for k, sh := range pc.Conversation.Shots {
		start := pc.From + sh.From
		if sh.Kind == "layout" || sh.Kind == "card" {
			label := "Card: “" + func() string {
				if sh.Card != nil {
					return sh.Card.Text
				}
				return ""
			}() + "”"
			if sh.Kind == "layout" {
				var who []string
				for _, t := range sh.Tiles {
					who = append(who, t.Member)
				}
				label = "Layout: " + editspec.LayoutLabel(sh.Layout) + " (" + strings.Join(who, ", ") + ")"
			}
			tl.Markers = append(tl.Markers, Marker{Start: start, Duration: max(1, sh.DurationInFrames), Kind: "graphic", Name: label})
			if !noted {
				tl.Notes = append(tl.Notes, "Conversation layouts and text cards are MetaRush's own: the timeline shows the first person's picture there, with a marker naming the layout or the card's text.")
				noted = true
			}
		}
		if len(sh.Tiles) == 0 {
			continue
		}
		t := sh.Tiles[0]
		m := media(t.AssetID)
		if m == nil {
			continue
		}
		fr := editspec.PlanFraming{Mode: "fill", FocusX: 0.5, FocusY: 0.5, Zoom: 1}
		it := &Item{Name: name(pc.ID, mediaTitle(m)) + " · " + mediaTitle(m), Media: m, Start: start + t.Delay, Duration: max(1, sh.DurationInFrames-t.Delay),
			Source: float64(t.TrimBefore), Speed: speed(t.PlaybackRate), Link: fmt.Sprintf("clip:%s/%d", pc.ID, k), Role: RolePicture, Framing: &fr}
		cut := Cut{}
		if sh.DissolveFrames > 0 && len(pic.Items) > 0 {
			// The shot before plays on under the dissolve.
			prev := pic.Items[len(pic.Items)-1]
			prev.Duration += sh.DissolveFrames
			cut = Cut{Kind: TransDissolve, Overlap: sh.DissolveFrames, From: editspec.TransitionCrossfade}
		}
		pic.Items = append(pic.Items, it)
		pic.Cuts = append(pic.Cuts, cut)
	}
}

func speed(r float64) float64 {
	if r <= 0 {
		return 1
	}
	return r
}

func fallbackGain(volume float64) float64 {
	if volume <= 0 {
		return 1
	}
	return volume
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func pictureKind(t string) string {
	switch t {
	case editspec.TransitionWipe:
		return TransWipe
	case editspec.TransitionSlide:
		return TransSlide
	case editspec.TransitionPush:
		return TransPush
	case editspec.TransitionZoom:
		return TransZoom
	case editspec.TransitionWhip:
		return TransWhip
	}
	return TransDissolve
}

func audioRole(r string) string {
	switch r {
	case editspec.RoleVoiceover:
		return RoleNarration
	case editspec.RoleMusic:
		return RoleMusic
	case editspec.RoleSFX:
		return RoleEffects
	}
	return RoleDialogue
}

func roleOrder(r string) int {
	switch r {
	case RoleDialogue:
		return 0
	case RoleNarration:
		return 1
	case RoleMusic:
		return 2
	}
	return 3
}

func roleTrackName(r string) string {
	switch r {
	case RoleNarration:
		return "Narration"
	case RoleMusic:
		return "Music"
	case RoleEffects:
		return "Effects"
	}
	return "Dialogue"
}

// mediaTitle is a file's name without its extension.
func mediaTitle(m *Media) string {
	n := m.Name
	if i := strings.LastIndex(n, "."); i > 0 {
		n = n[:i]
	}
	return n
}

// layAudio places sound items on as few tracks as possible: an item follows
// the previous one on its track when they do not overlap, or overlap by a
// blend short enough for a crossfade (at most half of either item); otherwise
// it goes to the next track ("Music 2").
func layAudio(name, role string, items []*Item) []*Track {
	sort.SliceStable(items, func(i, j int) bool { return items[i].Start < items[j].Start })
	var tracks []*Track
	for _, it := range items {
		var dst *Track
		for _, t := range tracks {
			last := t.Items[len(t.Items)-1]
			ov := last.End() - it.Start
			if ov <= 0 || (ov*2 <= last.Duration && ov*2 <= it.Duration) {
				dst = t
				break
			}
		}
		if dst == nil {
			n := name
			if len(tracks) > 0 {
				n = fmt.Sprintf("%s %d", name, len(tracks)+1)
			}
			dst = &Track{Name: n, Kind: KindAudio, Role: role}
			tracks = append(tracks, dst)
		}
		dst.Items = append(dst.Items, it)
	}
	return tracks
}

// describeOverlay names a graphic for a marker ("Lower third: Sam Lee, Host").
func describeOverlay(ov editspec.PlanOverlay) string {
	if ov.Motion != nil {
		return "motion graphic " + ov.Motion.Name
	}
	p := map[string]any{}
	_ = jsonUnmarshal(ov.Params, &p)
	s := func(k string) string { v, _ := p[k].(string); return v }
	join := func(parts ...string) string {
		var out []string
		for _, x := range parts {
			if strings.TrimSpace(x) != "" {
				out = append(out, x)
			}
		}
		return strings.Join(out, ", ")
	}
	switch ov.Template {
	case editspec.TplLowerThirdClean, editspec.TplLowerThirdCinematic:
		return "Lower third: " + join(s("name"), s("title"))
	case editspec.TplQuoteKinetic:
		q := "“" + s("text") + "”"
		if a := s("attribution"); a != "" {
			q += " (" + a + ")"
		}
		return "Quote: " + q
	case editspec.TplEndCard:
		return "End card: " + join(s("headline"), s("subline"), s("cta"), s("handle"))
	case editspec.TplDataCard:
		return "Statistic: " + join(s("value")+" "+s("label"), "source: "+s("source"))
	case editspec.TplTitleCard:
		return "Title: " + join(s("title"), s("subtitle"))
	}
	return ov.Template
}
