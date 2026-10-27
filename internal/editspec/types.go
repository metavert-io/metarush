// Package editspec is the declarative edit specification: the only editable source
// of truth for a cut. Claude Code (via skills) and the dashboard author specs or
// typed patch operations; Go validates them, stores them as immutable revisions and
// resolves them into a frame-accurate RenderPlan that Remotion renders verbatim.
//
// Times in a Spec are seconds (easy and unambiguous to author). The RenderPlan is
// integer frames at the output frame rate, computed only here so preview and render
// can never disagree about timing.
package editspec

import (
	"encoding/json"
	"math"
	"regexp"
)

// SchemaVersion is the current edit-spec version.
const SchemaVersion = 1

// Output presets.
const (
	PresetVertical  = "vertical-1080p"
	PresetLandscape = "landscape-1080p"
	PresetSquare    = "square-1080"
)

// Presets maps preset names to dimensions.
var Presets = map[string][2]int{
	PresetVertical:  {1080, 1920},
	PresetLandscape: {1920, 1080},
	PresetSquare:    {1080, 1080},
}

// PresetLabel is the plain name people see for a preset ("vertical").
func PresetLabel(p string) string {
	switch p {
	case PresetVertical:
		return "vertical"
	case PresetLandscape:
		return "landscape"
	case PresetSquare:
		return "square"
	}
	return p
}

// Spec is one complete cut.
type Spec struct {
	SchemaVersion int          `json:"schemaVersion" bson:"schemaVersion" jsonschema:"enum=1"`
	Title         string       `json:"title,omitempty" bson:"title,omitempty" jsonschema:"maxLength=200"`
	Output        Output       `json:"output" bson:"output"`
	Clips         []Clip       `json:"clips" bson:"clips" jsonschema:"minItems=1,maxItems=500"`
	Audio         []AudioTrack `json:"audio,omitempty" bson:"audio,omitempty" jsonschema:"maxItems=60"`
	Captions      *Captions    `json:"captions,omitempty" bson:"captions,omitempty"`
	Overlays      []Overlay    `json:"overlays,omitempty" bson:"overlays,omitempty" jsonschema:"maxItems=100"`
	Brand         *Brand       `json:"brand,omitempty" bson:"brand,omitempty"`
	Notes         string       `json:"notes,omitempty" bson:"notes,omitempty" jsonschema:"maxLength=4000,description=Editorial treatment summary shown to reviewers."`
	// Voiceover is the narration script (beats anchored to the cut) and its audio.
	Voiceover *Voiceover `json:"voiceover,omitempty" bson:"voiceover,omitempty"`
	// Mix sets bus levels, mute/solo, automatic ducking and the master loudness target.
	Mix *Mix `json:"mix,omitempty" bson:"mix,omitempty"`
	// Resources pins the resources the cut's graphics use (logos, pictures, PDF
	// pages, fonts): a short name → the resource and the exact version. Template
	// params (logo, image) and brand.fontFile refer to them by name (D82).
	Resources map[string]ResourcePin `json:"resources,omitempty" bson:"resources,omitempty" jsonschema:"description=Short name (lowercase letters\\, digits\\, - and _) → the resource and its exact version."`
	// Conversations pins the conversations the cut plays (D99): a short name →
	// the conversation and exactly how this cut renders it (each recording's
	// place on its clock, picture settings, the sound's gate). Clips play one
	// with assetId conv:<name>.
	Conversations map[string]ConversationPin `json:"conversations,omitempty" bson:"conversations,omitempty" jsonschema:"description=Short name → a conversation (several recordings of one talk\\, aligned) and how this cut renders it."`
	// Fixed pins the fixed copies the cut plays (D152): footage (an asset ID)
	// → the copy's SHA-256. Footage without a pin plays its original, so a
	// saved version keeps playing what it was saved with whatever fixes are
	// ticked later; a new version moves to a fixed copy with setFixed.
	Fixed map[string]FixedPin `json:"fixed,omitempty" bson:"fixed,omitempty" jsonschema:"description=Footage (asset ID) → the fixed copy this cut plays instead of the original (steadier picture\\, cleaned-up voices…)."`
}

// FixedPin is the fixed copy one piece of footage plays in a cut.
type FixedPin struct {
	SHA256 string `json:"sha256" bson:"sha256" jsonschema:"pattern=^[0-9a-f]{64}$,description=The fixed copy: the SHA-256 of its file."`
}

// FixedFootagePattern is the footage a fixed pin names: an asset ID.
var FixedFootagePattern = regexp.MustCompile(`^ast_[0-9a-z]{26}$`)

var shaHexRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// FixedSHA is the fixed copy a cut plays for an asset ("" for the original).
func (s *Spec) FixedSHA(assetID string) string {
	if s == nil || s.Fixed == nil {
		return ""
	}
	return s.Fixed[assetID].SHA256
}

// ResourcePin is one resource a cut uses, at one exact version.
type ResourcePin struct {
	ID     string `json:"id" bson:"id" jsonschema:"pattern=^res_[0-9a-z]{26}$"`
	SHA256 string `json:"sha256" bson:"sha256" jsonschema:"pattern=^[0-9a-f]{64}$,description=The exact version: the SHA-256 of its file. Updating to a newer version is an explicit change."`
}

// Output describes the rendered frame.
type Output struct {
	Preset     string `json:"preset,omitempty" bson:"preset,omitempty" jsonschema:"enum=vertical-1080p,enum=landscape-1080p,enum=square-1080"`
	Width      int    `json:"width,omitempty" bson:"width,omitempty" jsonschema:"minimum=64,maximum=4096"`
	Height     int    `json:"height,omitempty" bson:"height,omitempty" jsonschema:"minimum=64,maximum=4096"`
	FPS        int    `json:"fps,omitempty" bson:"fps,omitempty" jsonschema:"enum=24,enum=25,enum=30,enum=50,enum=60"`
	Background string `json:"background,omitempty" bson:"background,omitempty" jsonschema:"pattern=^#[0-9a-fA-F]{6}$"`
	Platform   string `json:"platform,omitempty" bson:"platform,omitempty" jsonschema:"enum=generic,enum=youtube,enum=shorts,enum=tiktok,enum=reels,description=Selects caption/end-card safe margins."`
	// Framing is how clips are framed when they are cropped to this shape (D73,
	// D115): "faces" keeps the main face in frame, from each clip's on-screen
	// index (clips with a focus of their own keep it); "follow" moves the crop
	// with the subject (the main face, or what draws the eye where no face is
	// seen), smoothly and never across the footage's own cuts; "center" keeps
	// the middle. New cuts are written with "faces"; absent (cuts made before it
	// existed) means "center", so an older cut renders the same whether or not
	// its footage is indexed later. "follow" is only ever chosen by a person.
	Framing string `json:"framing,omitempty" bson:"framing,omitempty" jsonschema:"enum=faces,enum=follow,enum=center,description=faces: crops keep the main face in frame (new cuts); follow: crops move with the subject as it moves; center or absent: crops keep the middle (older cuts). A clip's own focus or framing.auto wins."`
}

// Clip is one shot on the primary storyline. Clips play in order.
type Clip struct {
	ID string `json:"id" bson:"id" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64"`
	// AssetID is the footage the clip shows — or another project's cut, nested
	// (D84): cut:<projectId>/<revisionId>, that exact version. In and out are
	// then seconds of that cut.
	AssetID    string      `json:"assetId" bson:"assetId" jsonschema:"pattern=^(ast_[0-9a-z]{26}|cut:prj_[0-9a-z]{26}/rev_[0-9a-z]{26}|conv:[a-z][a-z0-9_-]{0\\,47})$,description=An asset ID\\, cut:<projectId>/<revisionId> for a cut from another project (nested\\, pinned to that version)\\, or conv:<name> for a conversation the cut pins (its in and out are then seconds on the conversation's clock)."`
	In         float64     `json:"in" bson:"in" jsonschema:"minimum=0,description=Source in-point in seconds."`
	Out        float64     `json:"out" bson:"out" jsonschema:"minimum=0,description=Source out-point in seconds (exclusive)."`
	Speed      float64     `json:"speed,omitempty" bson:"speed,omitempty" jsonschema:"minimum=0.25,maximum=4"`
	Volume     *float64    `json:"volume,omitempty" bson:"volume,omitempty" jsonschema:"minimum=0,maximum=2,description=Linear gain for the clip's own audio (1 = unchanged)."`
	Muted      bool        `json:"muted,omitempty" bson:"muted,omitempty"`
	Framing    *Framing    `json:"framing,omitempty" bson:"framing,omitempty"`
	Transition *Transition `json:"transitionIn,omitempty" bson:"transitionIn,omitempty"`
	FadeOut    float64     `json:"fadeOut,omitempty" bson:"fadeOut,omitempty" jsonschema:"minimum=0,maximum=5"`
	Label      string      `json:"label,omitempty" bson:"label,omitempty" jsonschema:"maxLength=120"`
	Rationale  string      `json:"rationale,omitempty" bson:"rationale,omitempty" jsonschema:"maxLength=2000"`
	Evidence   []Evidence  `json:"evidence,omitempty" bson:"evidence,omitempty" jsonschema:"maxItems=20"`
	Locked     bool        `json:"locked,omitempty" bson:"locked,omitempty"`
	Notes      string      `json:"notes,omitempty" bson:"notes,omitempty" jsonschema:"maxLength=4000,description=A person's notes about this clip (kept with the cut; not shown in the video)."`
	// Shots are a conversation clip's picture over time (D99): who or what is
	// shown from each moment on the conversation's clock until the next.
	Shots []Shot `json:"shots,omitempty" bson:"shots,omitempty" jsonschema:"maxItems=3000,description=A conversation clip's picture over time: one person\\, a layout\\, a text card or other footage\\, each until the next."`
	// MutedMembers are members of the conversation whose sound this clip
	// leaves out (an off-camera interviewer under a text card).
	MutedMembers []string `json:"mutedMembers,omitempty" bson:"mutedMembers,omitempty" jsonschema:"maxItems=16,description=A conversation clip: members whose sound is left out of this clip."`
}

// Framing values (clip framing.auto and output.framing).
const (
	// FramingFaces keeps the main face in frame: the focus point comes from where
	// faces are in the clip's on-screen index (D73).
	FramingFaces = "faces"
	// FramingFollow moves the crop with the subject as it moves (D115): the
	// main face of each shot, else what draws the eye, smoothed, jumping only
	// at the footage's own cuts.
	FramingFollow = "follow"
	// FramingCenter keeps the middle of the picture (output.framing only; the
	// same as absent, written to say so).
	FramingCenter = "center"
)

// Framing controls aspect-ratio reframing.
type Framing struct {
	Mode string `json:"mode,omitempty" bson:"mode,omitempty" jsonschema:"enum=fill,enum=fit"`
	// Auto "faces" keeps the main face of the clip's source range in frame when
	// cropping, from the on-screen index; focusX/focusY are used where no face
	// was seen. (output.framing "faces" does the same for every clip without a
	// focus of its own.)
	Auto   string   `json:"auto,omitempty" bson:"auto,omitempty" jsonschema:"enum=faces,enum=follow,description=faces: keep the main face in frame (from the clip's on-screen index); follow: move the crop with the subject as it moves."`
	FocusX *float64 `json:"focusX,omitempty" bson:"focusX,omitempty" jsonschema:"minimum=0,maximum=1,description=Horizontal focal point kept in frame when cropping (0 left … 1 right)."`
	FocusY *float64 `json:"focusY,omitempty" bson:"focusY,omitempty" jsonschema:"minimum=0,maximum=1"`
	Zoom   float64  `json:"zoom,omitempty" bson:"zoom,omitempty" jsonschema:"minimum=1,maximum=3"`
}

// Transition types. The JSON values of the first four are kept from the first
// schema version; people see them as Cut, Fade in, Dissolve and Fade through black.
const (
	TransitionCut       = "cut"
	TransitionFade      = "fade"         // the clip fades in from the background
	TransitionCrossfade = "crossfade"    // "Dissolve"
	TransitionDip       = "dip-to-black" // "Fade through black"
	TransitionDipWhite  = "dip-to-white" // "Fade through white"
	TransitionWipe      = "wipe"         // an edge sweeps across and reveals the next shot
	TransitionSlide     = "slide"        // the next shot slides in over this one
	TransitionPush      = "push"         // the next shot pushes this one out of frame
	TransitionZoom      = "zoom"         // zoom through into the next shot
	TransitionWhip      = "whip"         // a fast blurred pan into the next shot
)

// Transition directions: the way the picture moves ("left": the next shot comes
// in from the right and travels left).
const (
	DirLeft  = "left"
	DirRight = "right"
	DirUp    = "up"
	DirDown  = "down"
)

// Transition easings (the vetted set).
const (
	EaseInOut  = "ease-in-out" // "Smooth" (default)
	EaseLinear = "linear"      // "Even speed"
	EaseIn     = "ease-in"     // "Speeds up"
	EaseOut    = "ease-out"    // "Slows down"
)

// Transition is how a clip enters: the picture (type, duration, direction,
// easing) and, optionally, the sound (a split edit).
type Transition struct {
	Type      string  `json:"type" bson:"type" jsonschema:"enum=cut,enum=fade,enum=crossfade,enum=dip-to-black,enum=dip-to-white,enum=wipe,enum=slide,enum=push,enum=zoom,enum=whip,description=cut; fade (in from the background); crossfade (dissolve); dip-to-black / dip-to-white (fade through black or white); wipe\\, slide\\, push\\, zoom\\, whip (see metarush edit transitions)."`
	Duration  float64 `json:"duration,omitempty" bson:"duration,omitempty" jsonschema:"minimum=0,maximum=5"`
	Direction string  `json:"direction,omitempty" bson:"direction,omitempty" jsonschema:"enum=left,enum=right,enum=up,enum=down,description=wipe/slide/push/whip: the way the picture moves (left = the next shot comes in from the right). Default left."`
	Easing    string  `json:"easing,omitempty" bson:"easing,omitempty" jsonschema:"enum=ease-in-out,enum=linear,enum=ease-in,enum=ease-out,description=How the movement or blend is paced (default ease-in-out)."`
	// Audio is how the sound changes at this cut: a split edit (the next clip's
	// sound starts early, or this one's carries over) or a pause-seeking switch.
	// Absent: the sound changes with the picture.
	Audio *SoundTransition `json:"audio,omitempty" bson:"audio,omitempty"`
}

// Sound transition modes.
const (
	SoundCut   = "cut"   // the sound changes with the picture
	SoundLead  = "lead"  // the incoming clip's sound starts before we see it (J-cut)
	SoundCarry = "carry" // the outgoing clip's sound carries on over the next shot (L-cut)
	SoundAuto  = "auto"  // move the sound change into the nearest pause so no word is cut
)

// SoundTransition is the sound side of a cut (a split edit).
type SoundTransition struct {
	Mode     string   `json:"mode" bson:"mode" jsonschema:"enum=cut,enum=lead,enum=carry,enum=auto,description=cut: the sound changes with the picture; lead: this clip's sound starts early\\, before we see it (J-cut); carry: the previous clip's sound carries on over this clip (L-cut); auto: MetaRush moves the sound change into the nearest pause so no word is cut."`
	Duration float64  `json:"duration,omitempty" bson:"duration,omitempty" jsonschema:"minimum=0,maximum=3,description=lead/carry: seconds. auto: the most it may move the sound (default 1)."`
	Blend    *float64 `json:"blend,omitempty" bson:"blend,omitempty" jsonschema:"minimum=0,maximum=2,description=Seconds over which one clip's sound fades out while the other's fades in (default: the mix's cutBlend\\, else 0.1)."`
}

// Evidence cites what in the footage justifies an editorial decision.
type Evidence struct {
	Kind    string   `json:"kind" bson:"kind" jsonschema:"enum=transcript,enum=insight,enum=frame,enum=scene,enum=user,enum=analysis"`
	AssetID string   `json:"assetId,omitempty" bson:"assetId,omitempty" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	From    *float64 `json:"from,omitempty" bson:"from,omitempty" jsonschema:"minimum=0"`
	To      *float64 `json:"to,omitempty" bson:"to,omitempty" jsonschema:"minimum=0"`
	Ref     string   `json:"ref,omitempty" bson:"ref,omitempty" jsonschema:"maxLength=200,description=ID of a transcript segment or insight."`
	Quote   string   `json:"quote,omitempty" bson:"quote,omitempty" jsonschema:"maxLength=500"`
	Note    string   `json:"note,omitempty" bson:"note,omitempty" jsonschema:"maxLength=500"`
}

// Audio roles.
const (
	RoleVoiceover = "voiceover"
	RoleMusic     = "music"
	RoleSFX       = "sfx"
	RoleDialogue  = "dialogue"
)

// AudioTrack is an additional audio layer placed on the timeline.
type AudioTrack struct {
	ID                string   `json:"id" bson:"id" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64"`
	AssetID           string   `json:"assetId" bson:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	Role              string   `json:"role" bson:"role" jsonschema:"enum=voiceover,enum=music,enum=sfx,enum=dialogue"`
	In                float64  `json:"in" bson:"in" jsonschema:"minimum=0"`
	Out               float64  `json:"out" bson:"out" jsonschema:"minimum=0"`
	At                float64  `json:"at" bson:"at" jsonschema:"minimum=0,description=Timeline position in seconds."`
	GainDB            *float64 `json:"gainDb,omitempty" bson:"gainDb,omitempty" jsonschema:"minimum=-60,maximum=12"`
	FadeIn            float64  `json:"fadeIn,omitempty" bson:"fadeIn,omitempty" jsonschema:"minimum=0,maximum=10"`
	FadeOut           float64  `json:"fadeOut,omitempty" bson:"fadeOut,omitempty" jsonschema:"minimum=0,maximum=10"`
	DuckUnderDialogue bool     `json:"duckUnderDialogue,omitempty" bson:"duckUnderDialogue,omitempty"`
	DuckDB            *float64 `json:"duckDb,omitempty" bson:"duckDb,omitempty" jsonschema:"minimum=-30,maximum=0"`
	Rights            *Rights  `json:"rights,omitempty" bson:"rights,omitempty"`
	Rationale         string   `json:"rationale,omitempty" bson:"rationale,omitempty" jsonschema:"maxLength=2000"`
	Locked            bool     `json:"locked,omitempty" bson:"locked,omitempty"`
	Notes             string   `json:"notes,omitempty" bson:"notes,omitempty" jsonschema:"maxLength=4000,description=A person's notes about this sound (kept with the cut; not heard in the video)."`
	// Fit is how the track is timed: auto = it starts with the video (from at)
	// and ends with it, with a smooth fade (a longer track ends cleanly, on a
	// beat near the end when it has one; a shorter one fades out early, or
	// loops). Absent or manual = exactly in → out from at.
	Fit string `json:"fit,omitempty" bson:"fit,omitempty" jsonschema:"enum=auto,enum=manual,description=auto: starts with the video and ends with it (smooth fade; a longer track ends on a beat near the end when it has one\\, a shorter one fades out early or loops); manual (default): exactly in → out from at."`
	// Loop repeats a shorter track until the video ends (fit auto).
	Loop bool `json:"loop,omitempty" bson:"loop,omitempty" jsonschema:"description=fit auto: repeat a shorter track until the video ends."`
	// Ending is where a track fitted to the video ends (D120): phrase (music
	// added since) at the start of the music's next phrase, else of its next
	// bar, else on a beat, near the video's end, from the whole track's
	// analysis (bars and phrases, a tempo that may change); beat on the last
	// beat near the end from that analysis. Absent: on a beat found in the
	// track's first quarter hour at one tempo, as music added before.
	Ending string `json:"ending,omitempty" bson:"ending,omitempty" jsonschema:"enum=phrase,enum=beat,description=fit auto: where it ends near the video's end. phrase: where the music starts its next phrase (else its next bar\\, else a beat); beat: on a beat. Absent: on a beat (music added before phrases were found)."`
	// Duck says whether the track gets quieter while someone talks (narration
	// or on camera): true = always (by duckDb, or the mix's depths), false =
	// never; absent = as the mix decides (automatic ducking, or duckUnderDialogue).
	Duck *bool `json:"duck,omitempty" bson:"duck,omitempty" jsonschema:"description=true: gets quieter while someone talks (by duckDb or the mix's depths); false: never; absent: as the mix decides."`
}

// Audio track fits (AudioTrack.Fit).
const (
	TrackFitAuto   = "auto"
	TrackFitManual = "manual"
)

// Endings of a track fitted to the video (AudioTrack.Ending).
const (
	EndingPhrase = "phrase"
	EndingBeat   = "beat"
)

// Rights records licensing for third-party audio.
type Rights struct {
	License     string `json:"license,omitempty" bson:"license,omitempty" jsonschema:"maxLength=200"`
	Attribution string `json:"attribution,omitempty" bson:"attribution,omitempty" jsonschema:"maxLength=500"`
	Source      string `json:"source,omitempty" bson:"source,omitempty" jsonschema:"maxLength=500"`
}

// Caption styles and positions.
const (
	CaptionClean    = "clean"
	CaptionEmphasis = "emphasis"
	CaptionBoxed    = "boxed"
)

// Caption sizes (Captions.Size, D199): absent is medium, the size captions
// always had.
const (
	CaptionSmall  = "small"
	CaptionMedium = "medium"
	CaptionLarge  = "large"
)

// CaptionSizeScale is how much larger (or smaller) a caption size draws than
// medium; the renderer (video/src/captions/CaptionLayer.tsx) and the quality
// check's geometry use the same numbers.
func CaptionSizeScale(size string) float64 {
	switch size {
	case CaptionSmall:
		return 0.82
	case CaptionLarge:
		return 1.2
	}
	return 1
}

// Captions configures burned-in subtitles.
type Captions struct {
	Enabled  bool   `json:"enabled" bson:"enabled"`
	Style    string `json:"style,omitempty" bson:"style,omitempty" jsonschema:"enum=clean,enum=emphasis,enum=boxed,enum=bold,enum=highlight,enum=subtitle,description=The look: clean\\, emphasis (the spoken word lights up)\\, boxed\\, bold (big capitals for phones)\\, highlight (a box follows the spoken word) or subtitle (plain subtitles)."`
	Position string `json:"position,omitempty" bson:"position,omitempty" jsonschema:"enum=bottom,enum=lower,enum=middle,enum=top"`
	// Size is how large the words are (D199): small, medium (absent: the size
	// captions always had) or large.
	Size            string           `json:"size,omitempty" bson:"size,omitempty" jsonschema:"enum=small,enum=medium,enum=large,description=How large the words are: small\\, medium (the default) or large."`
	MaxWordsPerLine int              `json:"maxWordsPerLine,omitempty" bson:"maxWordsPerLine,omitempty" jsonschema:"minimum=1,maximum=12"`
	Uppercase       bool             `json:"uppercase,omitempty" bson:"uppercase,omitempty"`
	Source          string           `json:"source,omitempty" bson:"source,omitempty" jsonschema:"enum=transcript,enum=dialogue,enum=voiceover,enum=both,enum=manual,description=transcript or dialogue = the clips' transcripts (default); voiceover = the narration (take word timings or the script); both = dialogue and narration; manual = segments."`
	Segments        []CaptionSegment `json:"segments,omitempty" bson:"segments,omitempty" jsonschema:"maxItems=2000"`
	Emphasis        []string         `json:"emphasis,omitempty" bson:"emphasis,omitempty" jsonschema:"maxItems=50,description=Words to emphasize (emphasis style)."`
	// ShowSpeakers puts the speaker's name above each caption line (names come
	// from the voices someone named; unnamed voices show as Speaker N).
	ShowSpeakers bool `json:"showSpeakers,omitempty" bson:"showSpeakers,omitempty" jsonschema:"description=Show who is speaking above each caption line."`
	Locked       bool `json:"locked,omitempty" bson:"locked,omitempty"`
}

// CaptionSegment is a manually timed caption (timeline seconds).
type CaptionSegment struct {
	From    float64 `json:"from" bson:"from" jsonschema:"minimum=0"`
	To      float64 `json:"to" bson:"to" jsonschema:"minimum=0"`
	Text    string  `json:"text" bson:"text" jsonschema:"maxLength=300"`
	Speaker string  `json:"speaker,omitempty" bson:"speaker,omitempty" jsonschema:"maxLength=60"`
}

// Template IDs: vetted Remotion components that ship with MetaRush. The AI
// supplies only an ID and validated parameters. Project motion graphics
// (Overlay.Motion, D68) are the one place component code comes from a project,
// behind content addressing, an import allowlist, browser-only rendering and
// a person's approval of each version.
const (
	TplLowerThirdClean     = "lower-third.clean"
	TplLowerThirdCinematic = "lower-third.cinematic"
	TplQuoteKinetic        = "quote.kinetic"
	TplEndCard             = "endcard.cta"
	TplDataCard            = "datacard.stat"
	TplTitleCard           = "title.card"
	// TplLogoCorner keeps a logo in a corner of the frame ("put the logo top-right").
	TplLogoCorner = "logo.corner"
	// Slides (D108): full-frame slides in the brand's colours and font for
	// videos that explain software — a title, a list, code, a diagram, a
	// screenshot of a web page, a quote. They draw resources (code slides and
	// diagrams MetaRush made from a repository, page snapshots) by name.
	TplSlideTitle        = "slide.title"
	TplSlideBullets      = "slide.bullets"
	TplSlideCode         = "slide.code"
	TplSlideArchitecture = "slide.architecture"
	TplSlideScreenshot   = "slide.screenshot"
	TplSlideQuote        = "slide.quote"
	// TplNameBar is a bar over the names a call app printed in the picture,
	// with our own name that can follow who is on screen (D162); it can stay
	// up for the whole video (D163).
	TplNameBar = "name.bar"
	// TplEndScreen is an end screen with room for the next videos and the
	// subscribe button a video platform places over the last seconds (D263).
	TplEndScreen = "end.screen"
)

// Overlay places a motion-graphics template — or a project motion graphic a
// person approved (Motion) — on the timeline. Exactly one of Template and
// Motion is set.
type Overlay struct {
	ID       string `json:"id" bson:"id" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64"`
	Template string `json:"template,omitempty" bson:"template,omitempty" jsonschema:"enum=lower-third.clean,enum=lower-third.cinematic,enum=quote.kinetic,enum=endcard.cta,enum=datacard.stat,enum=title.card,enum=logo.corner,enum=slide.title,enum=slide.bullets,enum=slide.code,enum=slide.architecture,enum=slide.screenshot,enum=slide.quote,enum=name.bar,enum=end.screen,description=A vetted template (the default). Leave it out for a project motion graphic (or for a role the layout pack fills)."`
	// Motion is a project motion graphic instead of a template: its name and
	// the exact version (hash) a person approved. Params are validated
	// against the parameter schema its check recorded.
	Motion *MotionRef `json:"motion,omitempty" bson:"motion,omitempty" jsonschema:"description=A project motion graphic (motion/<name> in the project folder) by name and approved version\\, instead of a template."`
	// Role is the part the graphic plays in the project's layout pack (D264):
	// title-card, lower-third, end-screen, quote-card or chapter-card, with a
	// variant after a colon (lower-third:guest). A graphic that names a role
	// and no template is filled from the pack when the change is checked;
	// the template or motion graphic it is drawn with is kept in the cut, so a
	// saved version renders the same whatever the pack becomes.
	Role string `json:"role,omitempty" bson:"role,omitempty" jsonschema:"pattern=^[a-z][a-z-]{0\\,23}(:[a-z][a-z0-9-]{0\\,23})?$,description=The part it plays in the layout pack: title-card\\, lower-third\\, end-screen\\, quote-card or chapter-card (a variant after a colon: lower-third:guest). Without a template the pack fills it."`
	// At and Duration are seconds on the finished video; with a Span they are
	// measured inside the span instead (Duration 0: to the span's end, D163).
	At        float64         `json:"at" bson:"at" jsonschema:"minimum=0,description=When the graphic starts (seconds on the finished video; with a span: seconds after the span starts)."`
	Duration  float64         `json:"duration" bson:"duration" jsonschema:"minimum=0,maximum=7200,description=How long it stays (seconds; at least 0.2). With a span: 0 = to the span's end."`
	Params    json.RawMessage `json:"params" bson:"params" jsonschema:"type=object"`
	Rationale string          `json:"rationale,omitempty" bson:"rationale,omitempty" jsonschema:"maxLength=2000"`
	Evidence  []Evidence      `json:"evidence,omitempty" bson:"evidence,omitempty" jsonschema:"maxItems=20"`
	Locked    bool            `json:"locked,omitempty" bson:"locked,omitempty"`
	Notes     string          `json:"notes,omitempty" bson:"notes,omitempty" jsonschema:"maxLength=4000,description=A person's notes about this graphic (kept with the cut; not shown in the video)."`
	// Span makes the graphic last a section of the video or the whole of it
	// (D163): from the start of one clip to the end of another, following the
	// clips when the cut changes.
	Span *OverlaySpan `json:"span,omitempty" bson:"span,omitempty" jsonschema:"description=Last a section ({from\\, to}: clip names) or the whole video ({}); at and duration are then measured inside it."`
	// Changes change the graphic's fields while it stays on screen (D163): each
	// applies from a moment of a footage until that footage's next change.
	Changes []OverlayChange `json:"changes,omitempty" bson:"changes,omitempty" jsonschema:"maxItems=3000,description=Moments in the footage from which the graphic's fields change while it stays on screen (a name bar's name follows who is on screen)."`
}

// OverlaySpan is the stretch of the cut a graphic lasts (D163): from the start
// of clip From to the end of clip To. Either may be left out: the first or the
// last clip. {} is the whole video.
type OverlaySpan struct {
	From string `json:"from,omitempty" bson:"from,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64,description=The clip the graphic starts with (default: the first)."`
	To   string `json:"to,omitempty" bson:"to,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64,description=The clip it ends with (default: the last)."`
}

// OverlayChange is a moment from which a graphic shows other fields (D163):
// wherever the cut shows that footage at or after At (until the footage's next
// change), Params are merged over the graphic's own. Frames showing footage
// without changes use the graphic's own fields.
type OverlayChange struct {
	Footage string          `json:"footage" bson:"footage" jsonschema:"pattern=^(ast_[0-9a-z]{26}|cut:prj_[0-9a-z]{26}/rev_[0-9a-z]{26}|conv:[a-z][a-z0-9_-]{0\\,47})$,description=The footage the moment is in (a clip's assetId)."`
	At      float64         `json:"at" bson:"at" jsonschema:"minimum=0,description=Seconds in that footage from which the new fields apply."`
	Params  json.RawMessage `json:"params" bson:"params" jsonschema:"type=object,description=The fields that change (merged over the graphic's own; a picture field can't change)."`
}

// Brand carries visual tokens for templates.
type Brand struct {
	Primary    string `json:"primary,omitempty" bson:"primary,omitempty" jsonschema:"pattern=^#[0-9a-fA-F]{6}$"`
	Accent     string `json:"accent,omitempty" bson:"accent,omitempty" jsonschema:"pattern=^#[0-9a-fA-F]{6}$"`
	Text       string `json:"text,omitempty" bson:"text,omitempty" jsonschema:"pattern=^#[0-9a-fA-F]{6}$"`
	Background string `json:"background,omitempty" bson:"background,omitempty" jsonschema:"pattern=^#[0-9a-fA-F]{6}$"`
	Font       string `json:"font,omitempty" bson:"font,omitempty" jsonschema:"enum=Inter,enum=Fraunces,enum=JetBrains Mono"`
	// FontFile is a font from the cut's resources (by name): the templates
	// draw with it instead of Font (the brand kit's font file).
	FontFile string `json:"fontFile,omitempty" bson:"fontFile,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]{0\\,47}$,description=A font from the cut's resources (its short name); used instead of font."`
	// Logo names the brand's main logo among the cut's resources (D198), and
	// LogoDark and LogoLight the logos made for dark and for light
	// backgrounds. A template whose logo is the main one shows the one made
	// for its background instead — chosen by contrast with the background
	// colour — else the main one. Cuts without Logo show exactly the resource
	// each graphic names.
	Logo      string `json:"logo,omitempty" bson:"logo,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]{0\\,47}$,description=The brand's main logo (a resource name\\, usually logo): a graphic showing it shows the logo made for its background instead when the cut has one."`
	LogoDark  string `json:"logoDark,omitempty" bson:"logoDark,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]{0\\,47}$,description=The logo for dark backgrounds (a resource name\\, usually logo-dark)."`
	LogoLight string `json:"logoLight,omitempty" bson:"logoLight,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]{0\\,47}$,description=The logo for light backgrounds (a resource name\\, usually logo-light)."`
}

// ResourceFormat marks a template parameter that names one of the cut's
// resources ("logo", or "deck#3" for page 3 of a PDF). Validation checks the
// name is pinned in spec.resources and is the right kind; the renderer draws
// the pinned version. Project motion graphics (part 2) mark theirs the same way.
const ResourceFormat = "metarush-resource"

// LowerThirdParams for lower-third.clean and lower-third.cinematic.
type LowerThirdParams struct {
	Name  string `json:"name" jsonschema:"minLength=1,maxLength=60"`
	Title string `json:"title,omitempty" jsonschema:"maxLength=80"`
	Align string `json:"align,omitempty" jsonschema:"enum=left,enum=center,enum=right"`
	// Logo is a small logo at the start of the strap (a resource name).
	Logo string `json:"logo,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A logo from the cut's resources\\, drawn at the start of the strap."`
}

// QuoteParams for quote.kinetic.
type QuoteParams struct {
	Text        string   `json:"text" jsonschema:"minLength=1,maxLength=240"`
	Attribution string   `json:"attribution,omitempty" jsonschema:"maxLength=80"`
	Emphasis    []string `json:"emphasis,omitempty" jsonschema:"maxItems=8"`
	Motion      string   `json:"motion,omitempty" jsonschema:"enum=restrained,enum=none"`
}

// EndCardParams for endcard.cta.
type EndCardParams struct {
	Headline string `json:"headline" jsonschema:"minLength=1,maxLength=80"`
	Subline  string `json:"subline,omitempty" jsonschema:"maxLength=120"`
	CTA      string `json:"cta,omitempty" jsonschema:"maxLength=40"`
	Handle   string `json:"handle,omitempty" jsonschema:"maxLength=40"`
	// Logo sits above the headline; Image fills the card behind a soft scrim.
	Logo  string `json:"logo,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A logo from the cut's resources\\, above the headline."`
	Image string `json:"image,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A picture from the cut's resources behind the card (a PDF page: name#3)."`
}

// EndScreenParams for end.screen: an end screen that leaves room for the
// elements a video platform places over a video's last 5–20 seconds (the next
// videos and the subscribe button), with a headline, a line and the logo.
type EndScreenParams struct {
	Headline string `json:"headline" jsonschema:"minLength=1,maxLength=80"`
	Subline  string `json:"subline,omitempty" jsonschema:"maxLength=120"`
	// Slots is the room left for the platform's elements: two videos and the
	// subscribe button (default), one video and the button, or two videos.
	Slots string `json:"slots,omitempty" jsonschema:"enum=two-videos,enum=one-video,enum=videos-only,description=The room left for the platform's elements: two-videos (two videos and the subscribe button\\, the default)\\, one-video (one video and the button) or videos-only (two videos)."`
	// Outline draws a faint outline where each element goes, as a guide for
	// placing them (off by default: the platform draws them).
	Outline bool   `json:"outline,omitempty" jsonschema:"description=Draw a faint outline where each element goes (a guide while placing them)."`
	Logo    string `json:"logo,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A logo from the cut's resources\\, above the headline."`
	Image   string `json:"image,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A picture from the cut's resources behind the screen\\, darkened."`
}

// End screen slot layouts (EndScreenParams.Slots).
const (
	EndScreenTwoVideos  = "two-videos"
	EndScreenOneVideo   = "one-video"
	EndScreenVideosOnly = "videos-only"
)

// DataCardParams for datacard.stat. A source is mandatory: factual claims must be
// grounded in user-approved sources.
type DataCardParams struct {
	Value   string `json:"value" jsonschema:"minLength=1,maxLength=16"`
	Label   string `json:"label" jsonschema:"minLength=1,maxLength=80"`
	Source  string `json:"source" jsonschema:"minLength=1,maxLength=200"`
	Context string `json:"context,omitempty" jsonschema:"maxLength=120"`
	Trend   string `json:"trend,omitempty" jsonschema:"enum=up,enum=down,enum=flat"`
}

// TitleCardParams for title.card.
type TitleCardParams struct {
	Title    string `json:"title" jsonschema:"minLength=1,maxLength=80"`
	Subtitle string `json:"subtitle,omitempty" jsonschema:"maxLength=120"`
	// Logo sits above the title; Image replaces the softened frame behind it.
	Logo  string `json:"logo,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A logo from the cut's resources\\, above the title."`
	Image string `json:"image,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A picture from the cut's resources behind the title (a PDF page: name#3)."`
}

// LogoCornerParams for logo.corner: a logo kept in one corner of the frame.
type LogoCornerParams struct {
	Logo   string `json:"logo" jsonschema:"minLength=1,format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A logo from the cut's resources."`
	Corner string `json:"corner,omitempty" jsonschema:"enum=top-right,enum=top-left,enum=bottom-right,enum=bottom-left,description=Default top-right."`
	Size   string `json:"size,omitempty" jsonschema:"enum=small,enum=medium,enum=large,description=Default small (about a tenth of the frame's width)."`
	// Opacity: 1 is solid; lower lets the picture show through.
	Opacity float64 `json:"opacity,omitempty" jsonschema:"minimum=0.2,maximum=1,description=Default 0.9."`
}

// SlideTitleParams for slide.title: a title slide in the brand's colours.
type SlideTitleParams struct {
	Title    string `json:"title" jsonschema:"minLength=1,maxLength=90"`
	Subtitle string `json:"subtitle,omitempty" jsonschema:"maxLength=140"`
	// Kicker is a short label above the title ("Part 2", "How it works").
	Kicker string `json:"kicker,omitempty" jsonschema:"maxLength=40,description=A short label above the title (Part 2\\, How it works)."`
	Logo   string `json:"logo,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A logo from the cut's resources\\, above the title."`
	Image  string `json:"image,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A picture behind the title\\, darkened (a page snapshot\\, a photo)."`
}

// SlideBulletsParams for slide.bullets: a title and up to six points.
type SlideBulletsParams struct {
	Title   string   `json:"title" jsonschema:"minLength=1,maxLength=80"`
	Bullets []string `json:"bullets" jsonschema:"minItems=1,maxItems=6,description=Up to six short points (a line or two each)."`
	// Image is a picture beside the points (a screenshot, a diagram, a logo).
	Image string `json:"image,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A picture beside the points."`
	// Reveal: one-by-one (default: each point appears in turn) or all.
	Reveal string `json:"reveal,omitempty" jsonschema:"enum=one-by-one,enum=all,description=one-by-one (default) or all at once."`
}

// SlideCodeParams for slide.code: a code slide MetaRush made from a
// repository (metarush resources make code), with a title.
type SlideCodeParams struct {
	Title string `json:"title,omitempty" jsonschema:"maxLength=80"`
	Code  string `json:"code" jsonschema:"minLength=1,format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=The code picture (a resource made with metarush resources make code)."`
	// Caption is a line under the code (what to notice).
	Caption string `json:"caption,omitempty" jsonschema:"maxLength=140"`
}

// SlideArchitectureParams for slide.architecture: a diagram (made with
// metarush resources make diagram, or any picture), with a title.
type SlideArchitectureParams struct {
	Title   string `json:"title,omitempty" jsonschema:"maxLength=80"`
	Diagram string `json:"diagram" jsonschema:"minLength=1,format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=The diagram (a resource made with metarush resources make diagram\\, or any picture)."`
	Caption string `json:"caption,omitempty" jsonschema:"maxLength=140"`
}

// SlideScreenshotParams for slide.screenshot: a web page snapshot (or any
// picture) in a browser or phone frame; a tall one scrolls.
type SlideScreenshotParams struct {
	Title string `json:"title,omitempty" jsonschema:"maxLength=80"`
	Image string `json:"image" jsonschema:"minLength=1,format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A web page snapshot (name: for a computer; name#2: a phone; name#3: the whole page) or any picture."`
	// Frame: browser, phone or none (default: phone for a tall picture, else browser).
	Frame string `json:"frame,omitempty" jsonschema:"enum=browser,enum=phone,enum=none"`
	// URL is the address shown in the browser frame (default: the page's own).
	URL     string `json:"url,omitempty" jsonschema:"maxLength=120"`
	Caption string `json:"caption,omitempty" jsonschema:"maxLength=140"`
	// Scroll moves down a picture taller than the frame over the slide's time
	// (default: on for a whole-page snapshot).
	Scroll *bool `json:"scroll,omitempty"`
}

// SlideQuoteParams for slide.quote: a quote as a full slide.
type SlideQuoteParams struct {
	Text        string `json:"text" jsonschema:"minLength=1,maxLength=280"`
	Attribution string `json:"attribution,omitempty" jsonschema:"maxLength=80"`
	Role        string `json:"role,omitempty" jsonschema:"maxLength=80,description=Who they are (CTO\\, Acme)."`
	Image       string `json:"image,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A portrait or logo beside the quote."`
}

// NameBarParams for name.bar: an opaque bar over the names a call app printed
// in the picture, showing our own name (D162). It is solid from its first
// frame; when the name changes (a graphic's changes, D163) the words cross-fade
// and the bar stays put.
type NameBarParams struct {
	// Name is who is on screen; empty shows the bar without a name.
	Name  string `json:"name,omitempty" jsonschema:"maxLength=60,description=Who is on screen (empty: the bar shows no name)."`
	Title string `json:"title,omitempty" jsonschema:"maxLength=80,description=A short line under the name."`
	// Cover is the area the bar must hide, in fractions of the picture.
	Cover *CoverBox `json:"cover,omitempty" jsonschema:"description=The area to hide (the printed names)\\, in fractions of the picture from its top-left. Default: the bottom-left corner where call apps print names."`
	// Style: band (default) runs across the picture; tab is only as wide as
	// the area and the name.
	Style string `json:"style,omitempty" jsonschema:"enum=band,enum=tab,description=band (default): across the whole picture; tab: a box only as wide as the area and the name."`
	// Hide takes the bar away until a later change brings it back (footage
	// with nothing to cover).
	Hide bool   `json:"hide,omitempty" jsonschema:"description=Take the bar away (footage with nothing to cover); a later change brings it back."`
	Logo string `json:"logo,omitempty" jsonschema:"format=metarush-resource,pattern=^[a-z][a-z0-9_-]{0\\,47}(#[1-9][0-9]{0\\,2})?$,description=A small logo at the start of the bar."`
	// FirstNames says the names are first names only, so a conversation's
	// tile tags that cover printed names use them too (D182).
	FirstNames bool `json:"firstNames,omitempty" jsonschema:"description=The names are first names only (a conversation's tile tags follow it)."`
	// Height is how tall the bar is (D199): full (absent) or half — about half
	// as tall, never shorter than the printed names it covers.
	Height string `json:"height,omitempty" jsonschema:"enum=full,enum=half,description=full (default) or half: about half as tall\\, never too short to cover the printed names."`
	// Animate is how a name comes in (D199): fade (absent: the name fades in
	// quickly), slide (it slides in) or never (nothing moves). The bar itself
	// is always solid from its first frame.
	Animate string `json:"animate,omitempty" jsonschema:"enum=fade,enum=slide,enum=never,description=How a name comes in: fade (default)\\, slide or never (nothing moves). The bar is always solid from its first frame."`
}

// Name bar looks, heights and animations (NameBarParams.Style, Height,
// Animate; D162, D199).
const (
	NameBarBand  = "band"
	NameBarTab   = "tab"
	NameBarFull  = "full"
	NameBarHalf  = "half"
	NameBarFade  = "fade"
	NameBarSlide = "slide"
	NameBarStill = "never"
)

// CoverBox is an area of the picture in fractions (0–1) from its top-left.
type CoverBox struct {
	X float64 `json:"x" jsonschema:"minimum=0,maximum=1"`
	Y float64 `json:"y" jsonschema:"minimum=0,maximum=1"`
	W float64 `json:"w" jsonschema:"minimum=0,maximum=1"`
	H float64 `json:"h" jsonschema:"minimum=0,maximum=1"`
}

// DefaultNameCover is where call apps print names when nothing says otherwise:
// the bottom-left corner.
var DefaultNameCover = CoverBox{X: 0, Y: 0.9, W: 0.3, H: 0.1}

// TemplateInfo describes a vetted template for catalogs and skills.
type TemplateInfo struct {
	ID          string  `json:"id"`
	Version     string  `json:"version"`
	Summary     string  `json:"summary"`
	MinDuration float64 `json:"minDuration"`
	MaxDuration float64 `json:"maxDuration"`
	Region      string  `json:"region"` // lower | center | full | corner — used for collision checks
	// Persistent: made to stay on screen for a section or the whole video
	// (a span, D163), however long that is.
	Persistent bool `json:"persistent,omitempty"`
	// ResourceParams are the parameters that name one of the cut's resources.
	ResourceParams []string `json:"resourceParams,omitempty"`
	params         any
}

// Templates is the catalog of vetted Remotion templates.
var Templates = []TemplateInfo{
	{ID: TplLowerThirdClean, Version: "1", Summary: "A clean name title at the bottom of the screen with a thin accent line, optionally with a small logo. Good for interviews.", MinDuration: 2, MaxDuration: 10, Region: "lower", params: LowerThirdParams{}},
	{ID: TplLowerThirdCinematic, Version: "1", Summary: "An elegant name title at the bottom in widely spaced serif letters that appears slowly, optionally with a small logo. Good for a documentary feel.", MinDuration: 2.5, MaxDuration: 10, Region: "lower", params: LowerThirdParams{}},
	{ID: TplQuoteKinetic, Version: "1", Summary: "A quote that appears calmly, word by word, with optional highlighted words.", MinDuration: 2.5, MaxDuration: 15, Region: "center", params: QuoteParams{}},
	{ID: TplEndCard, Version: "1", Summary: "An end card in your brand's look with a headline, a call to action and your handle, kept clear of the apps' buttons; optional logo and background picture.", MinDuration: 2, MaxDuration: 10, Region: "full", params: EndCardParams{}},
	{ID: TplDataCard, Version: "1", Summary: "An animated number with a label and its source on screen (a source is required).", MinDuration: 2.5, MaxDuration: 10, Region: "center", params: DataCardParams{}},
	{ID: TplTitleCard, Version: "1", Summary: "Opening title over a softened frame; optional logo above it and a picture behind it.", MinDuration: 1.5, MaxDuration: 8, Region: "center", params: TitleCardParams{}},
	{ID: TplLogoCorner, Version: "1", Summary: "A logo in one corner of the frame (top-right by default), for as long as you like.", MinDuration: 1, MaxDuration: 600, Region: "corner", Persistent: true, params: LogoCornerParams{}},
	{ID: TplSlideTitle, Version: "1", Summary: "Slide: a title in the brand's colours, with an optional label above, a logo and a darkened picture behind.", MinDuration: 2, MaxDuration: 60, Region: "full", params: SlideTitleParams{}},
	{ID: TplSlideBullets, Version: "1", Summary: "Slide: a title and up to six points that appear one by one, optionally beside a picture.", MinDuration: 3, MaxDuration: 60, Region: "full", params: SlideBulletsParams{}},
	{ID: TplSlideCode, Version: "1", Summary: "Slide: a picture of code from a repository, under a title; long code scrolls.", MinDuration: 3, MaxDuration: 60, Region: "full", params: SlideCodeParams{}},
	{ID: TplSlideArchitecture, Version: "1", Summary: "Slide: a diagram of how something fits together (or any picture), under a title.", MinDuration: 3, MaxDuration: 60, Region: "full", params: SlideArchitectureParams{}},
	{ID: TplSlideScreenshot, Version: "1", Summary: "Slide: a web page snapshot (or any screenshot) in a browser or phone frame; a whole-page snapshot scrolls.", MinDuration: 2, MaxDuration: 60, Region: "full", params: SlideScreenshotParams{}},
	{ID: TplSlideQuote, Version: "1", Summary: "Slide: a quote with who said it, optionally beside a portrait or logo.", MinDuration: 3, MaxDuration: 60, Region: "full", params: SlideQuoteParams{}},
	{ID: TplNameBar, Version: "1", Summary: "A bar across the bottom that hides the names a call app printed in the picture and shows your own; the name can follow who is on screen, and it can stay up for the whole video.", MinDuration: 1, MaxDuration: 7200, Region: "lower", Persistent: true, params: NameBarParams{}},
	{ID: TplEndScreen, Version: "1", Summary: "An end screen for a video's last 5–20 seconds in your brand's look: a headline, a line and the logo beside empty room where a video platform places the next videos and the subscribe button.", MinDuration: 5, MaxDuration: 20, Region: "full", params: EndScreenParams{}},
}

func init() {
	for i := range Templates {
		Templates[i].ResourceParams = resourceParamsOf(Templates[i].params)
	}
}

// TemplateByID finds a template.
func TemplateByID(id string) (TemplateInfo, bool) {
	for _, t := range Templates {
		if t.ID == id {
			return t, true
		}
	}
	return TemplateInfo{}, false
}

// Op is one typed patch operation against a base revision.
type Op struct {
	Op       string          `json:"op" bson:"op" jsonschema:"enum=insertClip,enum=removeClip,enum=replaceClip,enum=updateClip,enum=moveClip,enum=addOverlay,enum=updateOverlay,enum=removeOverlay,enum=addAudio,enum=updateAudio,enum=removeAudio,enum=setCaptions,enum=setOutput,enum=setBrand,enum=setNotes,enum=setTitle,enum=lock,enum=unlock,enum=setScript,enum=updateVoiceover,enum=addBeat,enum=updateBeat,enum=moveBeat,enum=removeBeat,enum=setMix,enum=setTransition,enum=splitClip,enum=trimClip,enum=slipClip,enum=deleteRange,enum=removeWords,enum=setResource,enum=setConversation,enum=setFixed"`
	ID       string          `json:"id,omitempty" bson:"id,omitempty" jsonschema:"description=Target clip/overlay/audio/beat ID (or 'captions'\\, 'voiceover'\\, 'mix') for update/remove/move/lock ops; setTransition: the clip the transition leads into; splitClip/trimClip/slipClip/removeWords: the clip."`
	After    string          `json:"after,omitempty" bson:"after,omitempty" jsonschema:"description=Insert or move after this clip ID (beat ID for addBeat/moveBeat)."`
	Before   string          `json:"before,omitempty" bson:"before,omitempty" jsonschema:"description=Insert or move before this clip ID (beat ID for addBeat/moveBeat)."`
	Clip     *Clip           `json:"clip,omitempty" bson:"clip,omitempty"`
	Overlay  *Overlay        `json:"overlay,omitempty" bson:"overlay,omitempty"`
	Track    *AudioTrack     `json:"track,omitempty" bson:"track,omitempty"`
	Captions *Captions       `json:"captions,omitempty" bson:"captions,omitempty"`
	Output   *Output         `json:"output,omitempty" bson:"output,omitempty"`
	Brand    *Brand          `json:"brand,omitempty" bson:"brand,omitempty"`
	Text     *string         `json:"text,omitempty" bson:"text,omitempty" jsonschema:"maxLength=4000,description=Value for setNotes/setTitle; removeWords: the words removed (kept for the record)."`
	Set      json.RawMessage `json:"set,omitempty" bson:"set,omitempty" jsonschema:"type=object,description=Fields to change for update ops."`
	// Voiceover ops.
	Voiceover *Voiceover `json:"voiceover,omitempty" bson:"voiceover,omitempty" jsonschema:"description=setScript: the whole voiceover (null removes it)."`
	Beat      *Beat      `json:"beat,omitempty" bson:"beat,omitempty" jsonschema:"description=addBeat: the new beat."`
	Anchor    *Anchor    `json:"anchor,omitempty" bson:"anchor,omitempty" jsonschema:"description=moveBeat: the beat's new anchor (or use after/before to reorder the script)."`
	Mix       *Mix       `json:"mix,omitempty" bson:"mix,omitempty" jsonschema:"description=setMix: the whole mix (null removes it)."`
	// Transition ops.
	Transition *Transition `json:"transition,omitempty" bson:"transition,omitempty" jsonschema:"description=setTransition: how the clip (id) comes in\\, picture and sound (null = a plain cut). With edge end: how it fades out (type fade or dip-to-black\\, duration)."`
	Edge       string      `json:"edge,omitempty" bson:"edge,omitempty" jsonschema:"enum=in,enum=end,description=setTransition: in (default) = the cut into the clip; end = the clip's fade-out (the end of the whole video when it is the last clip). trimClip: in = the clip's start\\, end = its end."`
	// Studio editing ops (splitClip, trimClip, slipClip, deleteRange, removeWords).
	At    *float64 `json:"at,omitempty" bson:"at,omitempty" jsonschema:"minimum=0,description=splitClip: the moment in the clip's footage (source seconds) where it is cut in two."`
	From  *float64 `json:"from,omitempty" bson:"from,omitempty" jsonschema:"minimum=0,description=deleteRange: where the removed stretch starts (seconds on the cut's timeline); removeWords: where it starts in the clip's footage (source seconds)."`
	To    *float64 `json:"to,omitempty" bson:"to,omitempty" jsonschema:"minimum=0,description=deleteRange: where the removed stretch ends (timeline seconds); removeWords: where it ends in the footage; trimClip: the edge's new moment in the clip's footage (source seconds)."`
	By    *float64 `json:"by,omitempty" bson:"by,omitempty" jsonschema:"minimum=-36000,maximum=36000,description=slipClip: seconds to slide the clip's footage (positive: later in the recording); the clip keeps its length and place."`
	NewID string   `json:"newId,omitempty" bson:"newId,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64,description=splitClip\\, deleteRange\\, removeWords: the name of the second part when a clip is cut in two (default: the clip's name with -2\\, -3…)."`
	Mode  string   `json:"mode,omitempty" bson:"mode,omitempty" jsonschema:"enum=ripple,enum=roll,description=trimClip: ripple (default: the rest of the cut moves up or back) or roll (the neighbouring clip's edge moves with it\\, so the cut keeps its length)."`
	// Resource is setResource's pin: the resource and exact version the name
	// (id) stands for in this cut (null removes the name).
	Resource *ResourcePin `json:"resource,omitempty" bson:"resource,omitempty" jsonschema:"description=setResource: the resource and exact version the short name (id) stands for (null removes it)."`
	// Conversation is setConversation's pin: the conversation and how the cut
	// renders it under the short name (id); null removes it.
	Conversation *ConversationPin `json:"conversation,omitempty" bson:"conversation,omitempty" jsonschema:"description=setConversation: the conversation and how this cut renders it\\, under the short name (id) its clips use (null removes it)."`
	// Fixed is setFixed's pin: the fixed copy the footage (id: an asset ID)
	// plays in this cut; null plays its original again (D152).
	Fixed *FixedPin `json:"fixed,omitempty" bson:"fixed,omitempty" jsonschema:"description=setFixed: the fixed copy the footage (id\\, an asset ID) plays in this cut (null: the original again)."`
}

// Plan is what skills hand to `metarush edit check|apply`: either a full spec or ops.
type Plan struct {
	ProjectID      string `json:"projectId,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	BaseRevisionID string `json:"baseRevisionId,omitempty" jsonschema:"pattern=^rev_[0-9a-z]{26}$,description=Revision this plan was written against. Required unless the project has no revisions yet."`
	Branch         string `json:"branch,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]*$,maxLength=40"`
	Instruction    string `json:"instruction,omitempty" jsonschema:"maxLength=4000,description=The user's request, verbatim when possible."`
	Rationale      string `json:"rationale,omitempty" jsonschema:"maxLength=8000,description=Editorial reasoning for the whole change."`
	Origin         string `json:"origin,omitempty" jsonschema:"maxLength=200,pattern=^[a-z][a-z0-9-]*:[A-Za-z0-9_./-]+$,description=Where the plan came from (set by MetaRush; e.g. assistant:<run>/<file> for a dashboard-assistant proposal). Recorded on the revision."`
	// Note says in a few words what the new version changes ("flicker fixes
	// and headline cards", D150); MetaRush writes one from the instruction or
	// the changes when it is left out.
	Note string `json:"note,omitempty" jsonschema:"maxLength=80,description=A few words saying what this version changes (flicker fixes and headline cards); shown on the version."`
	Spec *Spec  `json:"spec,omitempty"`
	Ops  []Op   `json:"ops,omitempty" jsonschema:"maxItems=200"`
}

// Issue is a validation or preflight finding.
type Issue struct {
	Severity string `json:"severity" bson:"severity"` // error | warning
	Code     string `json:"code" bson:"code"`
	Path     string `json:"path,omitempty" bson:"path,omitempty"`
	Message  string `json:"message" bson:"message"`
}

// AssetFacts is what validation needs to know about a referenced asset.
type AssetFacts struct {
	ID                  string
	Name                string
	Kind                string
	State               string
	InProject           bool
	RightsRecorded      bool // the file itself has music rights recorded (asset rights)
	Duration            float64
	HasVideo, HasAudio  bool
	Width, Height       int
	FPS                 float64
	VFR, HDR            bool
	HasMezzanine        bool
	TranscriptPrecision string // "", "word", "segment"
	// Remotion is set for a clip MetaRush rendered from a Remotion source
	// (D135): the version it came from and where that version stands.
	Remotion *RemotionFacts
}

// RemotionFacts is what validation knows about a rendered composition's version.
type RemotionFacts struct {
	SourceTitle   string
	Composition   string
	VersionNumber int
	State         string // waiting | approved | rejected | revoked
}

// remotionWhy says why a version can't be used.
func remotionWhy(state string) string {
	switch state {
	case MotionRevoked:
		return "its approval was withdrawn"
	case MotionRejected:
		return "a person turned it down"
	}
	return "no person has approved it"
}

// TranscriptWord and TranscriptSegment are source-time transcript data used to
// derive captions and dialogue ranges.
type TranscriptWord struct {
	Text     string
	From, To float64
	// Said marks a word the cut says in the speaker's voice copy in place of
	// what was said (mix.said, D308).
	Said bool
}

// TranscriptSegment mirrors a stored segment.
type TranscriptSegment struct {
	From, To float64
	Text     string
	Speaker  string
	Words    []TranscriptWord
}

// Facts supplies asset and transcript data to validation and planning.
type Facts interface {
	Asset(id string) (AssetFacts, bool)
	Transcript(assetID string) []TranscriptSegment
}

// FaceFacts supplies where the main face is in a source range (the on-screen
// index): x and y from the left and top, 0–1. ok is false when no face was seen.
type FaceFacts interface {
	FaceFocus(assetID string, from, to float64) (x, y float64, ok bool)
}

// HeavyCrop is how much of a source fill framing cuts off for an output shape
// (0 none … 1); above 0.3 the subject easily falls out of frame.
func HeavyCrop(srcW, srcH, outW, outH int) float64 {
	if srcW <= 0 || srcH <= 0 || outW <= 0 || outH <= 0 {
		return 0
	}
	srcAR := float64(srcW) / float64(srcH)
	outAR := float64(outW) / float64(outH)
	return 1 - math.Min(srcAR, outAR)/math.Max(srcAR, outAR)
}

// heavyCropAt is the share above which a clip counts as heavily cropped.
const heavyCropAt = 0.3

// FollowsFaces reports whether the spec asks this clip to keep the main face in
// frame: the clip says so (framing.auto faces), or the cut does (output.framing
// faces) and the clip has no focus of its own. Only what the spec says counts,
// never whether the footage happens to be indexed, so a revision renders the same
// over time (D73). A clip shown whole (fit) has nothing to follow.
func FollowsFaces(c Clip, out Output) bool {
	if c.Framing != nil && c.Framing.Mode == "fit" {
		return false
	}
	if c.Framing != nil && (c.Framing.Auto == FramingFaces || c.Framing.Auto == FramingFollow) {
		return true // following movement keeps the face in frame wherever the path can't say more
	}
	ownFocus := c.Framing != nil && (c.Framing.FocusX != nil || c.Framing.FocusY != nil)
	return FacesFramed(out) && !ownFocus
}

// FaceFraming is the focus a clip gets from the faces in its source range when
// the spec asks for it (FollowsFaces) and the crop or zoom cuts something off.
// ok is false when faces do not decide it (not asked, nothing cut off, or no
// face seen there).
func FaceFraming(c Clip, a AssetFacts, out Output, facts Facts) (x, y float64, ok bool) {
	ff, isFaces := facts.(FaceFacts)
	if !isFaces || !FollowsFaces(c, out) {
		return 0, 0, false
	}
	zoomed := c.Framing != nil && c.Framing.Zoom > 1
	if !zoomed && HeavyCrop(a.Width, a.Height, out.Width, out.Height) < 0.005 {
		return 0, 0, false // the whole picture shows: there is nothing to keep in frame
	}
	return ff.FaceFocus(c.AssetID, c.In, c.Out)
}
