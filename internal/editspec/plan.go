package editspec

import (
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/textalign"
)

// RenderPlan is the fully resolved, frame-accurate description Remotion renders.
// Both the dashboard preview (proxies) and the worker render (originals) consume
// the same plan shape; only the media URLs differ.
type RenderPlan struct {
	SchemaVersion    int               `json:"schemaVersion"`
	RevisionID       string            `json:"revisionId"`
	ProjectID        string            `json:"projectId"`
	SpecHash         string            `json:"specHash"`
	Media            string            `json:"media" jsonschema:"enum=proxy,enum=original"` // proxy | original
	Width            int               `json:"width"`
	Height           int               `json:"height"`
	FPS              int               `json:"fps"`
	DurationInFrames int               `json:"durationInFrames"`
	Background       string            `json:"background"`
	Brand            Brand             `json:"brand"`
	SafeArea         SafeArea          `json:"safeArea"`
	Clips            []PlanClip        `json:"clips"`
	Audio            []PlanAudio       `json:"audio"`
	DialogueRanges   []FrameRange      `json:"dialogueRanges"`
	Captions         *PlanCaptions     `json:"captions,omitempty"`
	Overlays         []PlanOverlay     `json:"overlays"`
	Sources          map[string]string `json:"sources"` // assetId -> URL (filled by caller)
	// SourceChannels is the audio channel count of each file in Sources, filled
	// with them for previews: the dashboard plays a mono file 3 dB quieter, as
	// the renderer's equal-power upmix does (D64). Absent in renders.
	SourceChannels map[string]int `json:"sourceChannels,omitempty"`
	// MotionSources maps a motion graphic's hash to the URL of its bundle,
	// built from the approved archive and served by the renderer itself
	// (render/render.mjs fills it; nothing else may).
	MotionSources map[string]string `json:"motionSources,omitempty"`
	// MotionStills are, for the dashboard's preview only, the still pictures
	// its motion graphics' checks drew, by hash (D187): the preview shows the
	// one nearest the moment in the graphic's place instead of a placeholder.
	// Filled by the preview's caller like Sources; absent in renders.
	MotionStills map[string]*PlanMotionStills `json:"motionStills,omitempty"`
	// Voiceover is the narration placed on the timeline (for the dashboard and QA;
	// its audio is in Audio with role voiceover).
	Voiceover *PlanVoiceover `json:"voiceover,omitempty"`
	// Mix summarizes the resolved mixer (bus levels, ducking, loudness); the
	// gains themselves are in each clip's and audio element's envelope.
	Mix *PlanMix `json:"mix,omitempty"`
	// Resources are the cut's pinned resources by short name (templates' logo
	// and image, brand.fontFile), at their exact versions (D82).
	Resources map[string]PlanResource `json:"resources,omitempty"`
	// ResourceSources maps a resource file's SHA-256 (a pinned version, or a
	// PDF page picture) to a URL, filled by the caller like Sources: the
	// dashboard's same-origin media route, or the render's loopback server.
	ResourceSources map[string]string `json:"resourceSources,omitempty"`
	// Nested are the cuts from other projects this cut plays (D84), by the
	// clip's source key (cut:<project>/<revision>): resolved at this cut's
	// size and frame rate. Their pictures are drawn from these plans (muted);
	// their sound is already in Audio (flattened, mixed by Go).
	Nested map[string]*NestedPlan `json:"nested,omitempty"`
	// Fixed names the footage this cut plays from a fixed copy (D152):
	// asset ID → the copy's SHA-256. The caller fills Sources with that
	// copy's file (or its preview copy); footage not listed plays its original.
	Fixed map[string]string `json:"fixed,omitempty"`
}

// PlanVoiceover is the resolved narration.
type PlanVoiceover struct {
	Layout          string     `json:"layout" jsonschema:"enum=beats,enum=continuous"`
	SpeakingRateWPM float64    `json:"speakingRateWpm"`
	Beats           []PlanBeat `json:"beats"`
}

// PlanBeat is one resolved beat, in frames.
type PlanBeat struct {
	ID          string `json:"id"`
	Index       int    `json:"index"`
	Text        string `json:"text"`
	AnchorFrame int    `json:"anchorFrame"` // where its anchor resolves
	From        int    `json:"from"`        // where its audio starts
	To          int    `json:"to"`          // where its speech ends (estimated without audio)
	WindowEnd   int    `json:"windowEnd"`   // the next beat's start or the end of the cut
	Words       int    `json:"words"`
	Estimated   bool   `json:"estimated"`
	HasAudio    bool   `json:"hasAudio"`
	Fit         string `json:"fit" jsonschema:"enum=ok,enum=tight,enum=over,enum=unresolved"`
	Problem     string `json:"problem,omitempty"`
}

// PlanMix is the resolved mixer.
type PlanMix struct {
	Buses         map[string]PlanBus `json:"buses"`
	Ducking       PlanDucking        `json:"ducking"`
	VoiceRanges   []FrameRange       `json:"voiceRanges"`
	TargetLUFS    float64            `json:"targetLufs"`
	TruePeakDBTP  float64            `json:"truePeakDbtp"`
	Normalize     bool               `json:"normalize"`
	EstimatedLUFS *float64           `json:"estimatedLufs,omitempty"`
	MasterGainDB  float64            `json:"masterGainDb"`
	PeakLimited   bool               `json:"peakLimited,omitempty"`
	// LevelSpeech: clips with speech are evened out toward SpeechTargetLUFS (each
	// clip's levelDb); CutBlendFrames: how long the sound blends at straight cuts.
	LevelSpeech      bool     `json:"levelSpeech,omitempty"`
	SpeechTargetLUFS *float64 `json:"speechTargetLufs,omitempty"`
	CutBlendFrames   int      `json:"cutBlendFrames,omitempty"`
}

// PlanBus is one resolved mixer bus.
type PlanBus struct {
	GainDB float64 `json:"gainDb"`
	Muted  bool    `json:"muted"` // muted, or silenced because another bus is soloed
}

// PlanDucking is the effective automatic ducking.
type PlanDucking struct {
	Auto                   bool    `json:"auto"`
	MusicUnderVoiceoverDB  float64 `json:"musicUnderVoiceoverDb"`
	SourceUnderVoiceoverDB float64 `json:"sourceUnderVoiceoverDb"`
	MusicUnderDialogueDB   float64 `json:"musicUnderDialogueDb"`
	AttackFrames           int     `json:"attackFrames"`
	ReleaseFrames          int     `json:"releaseFrames"`
}

// SafeArea is the margin (px) graphics and captions must stay inside.
type SafeArea struct {
	Top    int `json:"top"`
	Bottom int `json:"bottom"`
	Left   int `json:"left"`
	Right  int `json:"right"`
}

// FrameRange is a [From, To) frame interval on the timeline.
type FrameRange struct {
	From int `json:"from"`
	To   int `json:"to"`
}

// PlanFraming is resolved framing.
type PlanFraming struct {
	Mode   string  `json:"mode" jsonschema:"enum=fill,enum=fit"`
	FocusX float64 `json:"focusX"`
	FocusY float64 `json:"focusY"`
	Zoom   float64 `json:"zoom"`
	// From is "faces" when the focus came from the clip's on-screen index, and
	// "follow" when it moves with the subject (Path).
	From string `json:"from,omitempty" jsonschema:"enum=faces,enum=follow"`
	// Path moves the focus over the clip (D115): keys in output frames from the
	// clip's start, straight lines between them (FocusX/FocusY are its start).
	Path []FocusKey `json:"path,omitempty"`
}

// PlanTransition is a resolved transition.
type PlanTransition struct {
	Type             string `json:"type" jsonschema:"enum=cut,enum=fade,enum=crossfade,enum=dip-to-black,enum=dip-to-white,enum=wipe,enum=slide,enum=push,enum=zoom,enum=whip"`
	DurationInFrames int    `json:"durationInFrames"`
	// Direction and Easing are set for the transitions that use them (types added
	// with the transition library always carry an easing; crossfades only when asked).
	Direction string `json:"direction,omitempty" jsonschema:"enum=left,enum=right,enum=up,enum=down"`
	Easing    string `json:"easing,omitempty" jsonschema:"enum=ease-in-out,enum=linear,enum=ease-in,enum=ease-out"`
	// Audio is how the sound changes at this cut when it does not simply follow
	// the picture (split edits, blends at straight cuts). Absent: with the picture.
	Audio *PlanCutAudio `json:"audio,omitempty"`
}

// PlanCutAudio is the resolved sound change at a cut: the incoming clip's sound
// starts at EditFrame and the outgoing clip's sound fades out over
// [EditFrame, EditFrame+BlendFrames).
type PlanCutAudio struct {
	Mode            string `json:"mode" jsonschema:"enum=cut,enum=lead,enum=carry,enum=auto"`
	Result          string `json:"result" jsonschema:"enum=cut,enum=lead,enum=carry,description=What the sound does (auto resolves to one of these)."`
	CutFrame        int    `json:"cutFrame" jsonschema:"description=Timeline frame of the picture cut (the middle of an overlapping transition)."`
	EditFrame       int    `json:"editFrame" jsonschema:"description=Timeline frame where the incoming clip's sound starts."`
	BlendFrames     int    `json:"blendFrames"`
	RequestedFrames int    `json:"requestedFrames,omitempty" jsonschema:"description=lead/carry as asked\\, when the footage or the neighbouring cuts allowed less."`
	Limited         string `json:"limited,omitempty" jsonschema:"enum=footage,enum=neighbours"`
	NoPause         bool   `json:"noPause,omitempty" jsonschema:"description=auto: no pause within reach\\, so a word may be cut."`
}

// PlanClipAudio is a clip's sound window when it does not match its picture
// window (a split edit or a blend at a cut): its sound plays on the timeline over
// [From, From+DurationInFrames), starting TrimBefore output frames into the
// source, while its picture plays silently.
type PlanClipAudio struct {
	From             int `json:"from"`
	DurationInFrames int `json:"durationInFrames"`
	TrimBefore       int `json:"trimBefore"`
	BlendInFrames    int `json:"blendInFrames"`
	BlendOutFrames   int `json:"blendOutFrames"`
	// Gain is the sound's gain envelope, relative to From.
	Gain []GainPoint `json:"gain,omitempty"`
	// Variant is a processed copy (a clearer voice) that plays instead of the
	// clip's own sound, with the same timing; TrimBefore above stays in the
	// clip's own source.
	Variant *PlanVariant `json:"variant,omitempty"`
}

// PlanVariant is a processed copy of a recording that plays in place of its
// own sound (mix.enhanced): the file and how far into it the sound starts.
// Timing and gain are those of the element it stands in for.
type PlanVariant struct {
	AssetID    string `json:"assetId"`
	TrimBefore int    `json:"trimBefore" jsonschema:"description=Output frames into the copy."`
	// TrimFraction is the rest of the trim below a frame, as on the element.
	TrimFraction *float64 `json:"trimFraction,omitempty" jsonschema:"description=The rest of the trim below a frame (−0.5 to 0.5): trimBefore + trimFraction is the exact place in the copy."`
}

// SplitTrim splits an exact trim (output frames into a source, at least 0)
// into the nearest whole frame, which frame-based readers such as timeline
// exports use, and the rest below a frame (−0.5 to 0.5, to a millionth of a
// frame), which the renderer and the preview add to place the sound to the
// sample (D119).
func SplitTrim(exact float64) (int, *float64) {
	exact = math.Max(0, exact)
	n := math.Round(exact)
	f := math.Round((exact-n)*1e6) / 1e6
	if f == 0 {
		f = 0 // never -0
	}
	return int(n), &f
}

// ExactTrim is a trim with its fraction (trimBefore when there is none).
func ExactTrim(trim int, frac *float64) float64 {
	if frac == nil {
		return float64(trim)
	}
	return float64(trim) + *frac
}

// PlanClip is a clip placed on the timeline in frames.
type PlanClip struct {
	ID               string         `json:"id"`
	AssetID          string         `json:"assetId"`
	From             int            `json:"from"`
	DurationInFrames int            `json:"durationInFrames"`
	TrimBefore       int            `json:"trimBefore"` // source offset in output frames
	SourceIn         float64        `json:"sourceIn"`
	SourceOut        float64        `json:"sourceOut"`
	PlaybackRate     float64        `json:"playbackRate"`
	Volume           float64        `json:"volume"`
	Muted            bool           `json:"muted"`
	HasAudio         bool           `json:"hasAudio"`
	SourceWidth      int            `json:"sourceWidth"`
	SourceHeight     int            `json:"sourceHeight"`
	Framing          PlanFraming    `json:"framing"`
	TransitionIn     PlanTransition `json:"transitionIn"`
	FadeOutFrames    int            `json:"fadeOutFrames"`
	Label            string         `json:"label,omitempty"`
	// Gain is the clip audio's gain envelope (volume, transitions, mix, ducking,
	// master), clip-relative frames. Absent when the clip is muted or silent, or
	// when the clip's sound has its own window (Audio).
	Gain []GainPoint `json:"gain,omitempty"`
	// TransitionOut is how the clip leaves when the next clip's transition moves
	// it too (push, zoom, whip): the last DurationInFrames frames.
	TransitionOut *PlanTransition `json:"transitionOut,omitempty"`
	// Audio is the clip's sound window when it differs from the picture (split
	// edits, blends at cuts): the picture plays silently and the sound plays here.
	Audio *PlanClipAudio `json:"audio,omitempty"`
	// SpeechLUFS is the clip's measured speech loudness and LevelDB the gain that
	// evens it out with the other clips (mix.levelSpeech); both are in the gain.
	SpeechLUFS *float64 `json:"speechLufs,omitempty"`
	LevelDB    *float64 `json:"levelDb,omitempty"`
	// Conversation is a conversation clip's picture (D99): its shots and
	// tiles. The clip itself plays silently; its sound is the members'
	// elements in the plan's audio.
	Conversation *PlanConversation `json:"conversation,omitempty"`
}

// Sound returns where a clip's sound plays: its sound window, or its picture
// window when the two match, and the matching gain envelope.
func (pc *PlanClip) Sound() (from, dur int, gain []GainPoint) {
	if a := pc.Audio; a != nil {
		return a.From, a.DurationInFrames, a.Gain
	}
	return pc.From, pc.DurationInFrames, pc.Gain
}

// PlanAudio is an audio layer in frames.
type PlanAudio struct {
	ID               string `json:"id"`
	AssetID          string `json:"assetId"`
	Role             string `json:"role" jsonschema:"enum=voiceover,enum=music,enum=sfx,enum=dialogue"`
	From             int    `json:"from"`
	DurationInFrames int    `json:"durationInFrames"`
	TrimBefore       int    `json:"trimBefore"`
	// TrimFraction is the rest of the trim below a frame: trimBefore +
	// trimFraction is exactly where in the file the element starts (a
	// conversation member in a cut that places recordings to the sample, D119).
	// Absent: trimBefore is exact (every other element, and older cuts).
	TrimFraction      *float64 `json:"trimFraction,omitempty" jsonschema:"description=The rest of the trim below a frame (−0.5 to 0.5): trimBefore + trimFraction is the exact place in the file (recordings placed to the sample)."`
	GainDB            float64  `json:"gainDb"`
	FadeInFrames      int      `json:"fadeInFrames"`
	FadeOutFrames     int      `json:"fadeOutFrames"`
	DuckUnderDialogue bool     `json:"duckUnderDialogue"`
	DuckDB            float64  `json:"duckDb"`
	// Bus is the mixer bus (source | voiceover | music | sfx).
	Bus string `json:"bus,omitempty" jsonschema:"enum=source,enum=voiceover,enum=music,enum=sfx"`
	// BeatID names the voiceover beat this element plays (the first one when
	// it plays several: the pieces of one live read that still follow each
	// other, D85).
	BeatID string `json:"beatId,omitempty"`
	// BeatIDs lists every line the element plays when it plays more than one.
	BeatIDs []string `json:"beatIds,omitempty"`
	// PlaybackRate (pitch preserved); 1 when absent.
	PlaybackRate float64 `json:"playbackRate,omitempty"`
	// AIVoice marks synthetic speech (TTS or a voice clone).
	AIVoice bool `json:"aiVoice,omitempty"`
	// Gain is the element's gain envelope (all of gainDb, fades, bus, ducking and
	// master), element-relative frames. When present it replaces gainDb/fades/duck.
	Gain []GainPoint `json:"gain,omitempty"`
	// Variant is a processed copy (a clearer voice) that plays instead of the
	// track's own file (dialogue tracks), with the same timing.
	Variant *PlanVariant `json:"variant,omitempty"`
	// Fit is "auto" when MetaRush timed the track to the video (audio.fit):
	// Piece numbers the repeats of a looped track (0 = the first play) and
	// EndsOnBeat says the last piece ends on a beat found in the music.
	Fit        string `json:"fit,omitempty" jsonschema:"enum=auto"`
	Piece      int    `json:"piece,omitempty"`
	EndsOnBeat bool   `json:"endsOnBeat,omitempty"`
	// EndsOn says what a track with an ending ends on (D120): the start of a
	// phrase, a bar line or a beat ("" when none was within reach).
	EndsOn string `json:"endsOn,omitempty" jsonschema:"enum=phrase,enum=bar,enum=beat"`
	// Nested names the clip whose nested cut this sound belongs to (D84): the
	// nested cut's own sounds, mixed with that clip's envelope.
	Nested string `json:"nested,omitempty"`
	// Conversation names the conversation clip this sound belongs to and
	// Member whose track it is (D101).
	Conversation string `json:"conversation,omitempty"`
	Member       string `json:"member,omitempty"`
}

// PlanWord is a caption word in timeline frames.
type PlanWord struct {
	Text     string `json:"text"`
	From     int    `json:"from"`
	To       int    `json:"to"`
	Emphasis bool   `json:"emphasis"`
	// Src is where the word is said: which footage (or narration take) and
	// when (D145). Go reads it (fixing a caption, the caption check); it is not
	// part of the plan's JSON, so plans render as before.
	Src *WordSource `json:"-"`
}

// WordSource is where a caption word comes from (source seconds). Words of a
// conversation clip have none (they come from several recordings).
type WordSource struct {
	AssetID string
	ClipID  string
	BeatID  string
	From    float64
	To      float64
	// Said: the cut says this word in the speaker's voice copy, in place of
	// what was said (mix.said, D308); captions files can mark it.
	Said bool
}

// PlanCaptionLine is one on-screen caption line.
type PlanCaptionLine struct {
	From    int        `json:"from"`
	To      int        `json:"to"`
	Text    string     `json:"text"`
	Speaker string     `json:"speaker,omitempty"`
	Words   []PlanWord `json:"words"`
}

// PlanCaptions is resolved captions.
type PlanCaptions struct {
	Style     string `json:"style" jsonschema:"enum=clean,enum=emphasis,enum=boxed,enum=bold,enum=highlight,enum=subtitle"`
	Position  string `json:"position" jsonschema:"enum=bottom,enum=lower,enum=middle,enum=top"`
	Uppercase bool   `json:"uppercase"`
	Precision string `json:"precision" jsonschema:"enum=word,enum=segment"` // word | segment
	// Size is small or large (D199); absent: medium, the size captions always had.
	Size string `json:"size,omitempty" jsonschema:"enum=small,enum=large"`
	// ShowSpeakers renders each line's speaker name above it.
	ShowSpeakers bool              `json:"showSpeakers,omitempty"`
	Lines        []PlanCaptionLine `json:"lines"`
}

// PlanOverlay is a template instance (or a project motion graphic) in frames.
type PlanOverlay struct {
	ID               string          `json:"id"`
	Template         string          `json:"template"`
	TemplateVersion  string          `json:"templateVersion"`
	From             int             `json:"from"`
	DurationInFrames int             `json:"durationInFrames"`
	Params           json.RawMessage `json:"params"`
	// Motion is set instead of Template for a project motion graphic.
	Motion *PlanMotion `json:"motion,omitempty"`
	// Segments are the graphic's fields over time when they change while it
	// stays on screen (D163): from (frames after the graphic starts) and the
	// whole fields from then on. The first starts at 0 with Params. Absent when
	// nothing changes.
	Segments []PlanSegment `json:"segments,omitempty"`
}

// PlanSegment is a graphic's fields from one frame on (D163).
type PlanSegment struct {
	From   int             `json:"from" jsonschema:"description=Frames after the graphic starts."`
	Params json.RawMessage `json:"params"`
}

// PlanMotion is a project motion graphic in the plan: which version, and the
// screen region its check recorded (layering, captions, QA).
type PlanMotion struct {
	Name   string `json:"name"`
	Hash   string `json:"hash"`
	Region string `json:"region" jsonschema:"enum=lower,enum=center,enum=full"`
	// Libs are the shared libraries it is bundled with (D171): name → version.
	Libs map[string]string `json:"libs,omitempty"`
}

// SafeAreaFor is the margin graphics and captions keep for a platform at a
// frame size (the same table Resolve uses).
func SafeAreaFor(platform string, width, height int) SafeArea {
	safe, ok := platformSafe[platform]
	if !ok {
		safe = platformSafe["generic"]
	}
	h, w := float64(height), float64(width)
	return SafeArea{Top: int(h * safe[0]), Bottom: int(h * safe[1]), Left: int(w * safe[2]), Right: int(w * safe[3])}
}

// DefaultBrand is the restrained default palette.
var DefaultBrand = Brand{Primary: "#F2EDE4", Accent: "#E0A458", Text: "#F7F5F0", Background: "#0B0B0E", Font: "Inter"}

// platform margins as fractions of height/width.
var platformSafe = map[string][4]float64{ // top, bottom, left, right
	"generic": {0.06, 0.08, 0.06, 0.06},
	"youtube": {0.06, 0.10, 0.06, 0.06},
	"shorts":  {0.10, 0.20, 0.06, 0.12},
	"tiktok":  {0.10, 0.22, 0.06, 0.14},
	"reels":   {0.12, 0.22, 0.06, 0.10},
}

// Normalize fills defaults in place (deterministically) so hashes are stable.
func Normalize(s *Spec) {
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	o := &s.Output
	if o.Preset != "" && (o.Width == 0 || o.Height == 0) {
		if d, ok := Presets[o.Preset]; ok {
			o.Width, o.Height = d[0], d[1]
		}
	}
	if o.Width == 0 || o.Height == 0 {
		o.Preset, o.Width, o.Height = PresetVertical, 1080, 1920
	}
	if o.FPS == 0 {
		o.FPS = 30
	}
	if o.Background == "" {
		o.Background = "#000000"
	}
	if o.Platform == "" {
		o.Platform = "generic"
	}
	for i := range s.Clips {
		c := &s.Clips[i]
		if c.Speed == 0 {
			c.Speed = 1
		}
		c.In, c.Out = round3(c.In), round3(c.Out)
	}
	for i := range s.Audio {
		a := &s.Audio[i]
		a.In, a.Out, a.At = round3(a.In), round3(a.Out), round3(a.At)
	}
	if v := s.Voiceover; v != nil {
		if v.Beats == nil {
			v.Beats = []Beat{}
		}
		norm := func(a *Anchor) {
			a.Offset = round3(a.Offset)
			if a.At != nil {
				x := round3(*a.At)
				a.At = &x
			}
		}
		for i := range v.Beats {
			b := &v.Beats[i]
			norm(&b.Anchor)
			if t := b.Take; t != nil {
				t.In, t.Out = round3(t.In), round3(t.Out)
			}
		}
		if c := v.Continuous; c != nil {
			c.In, c.Out = round3(c.In), round3(c.Out)
			norm(&c.Anchor)
		}
	}
	normalizeVoices(s.Mix)
	normalizeSaid(s.Mix)
	normalizeConversations(s)
	if s.Captions != nil {
		if s.Captions.Style == "" {
			s.Captions.Style = CaptionClean
		}
		if s.Captions.Position == "" {
			s.Captions.Position = "lower"
		}
		if s.Captions.Source == "" {
			s.Captions.Source = "transcript"
		}
		if s.Captions.MaxWordsPerLine == 0 {
			s.Captions.MaxWordsPerLine = CaptionLook(s.Captions.Style).WordsPerLine // 6 for the first three looks
		}
	}
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// Timeline computes clip placement in frames: the picture. It is the single
// implementation of picture timing used by validation, planning, preview and
// render (and the anchor of narration lines, which stay tied to the picture).
// Where each clip's sound plays (split edits, blends) is resolved on top of it
// by resolveSound, which also needs the footage's length and transcripts.
func Timeline(s *Spec) (placed []PlanClip, total int) {
	fps := float64(s.Output.FPS)
	pos := 0
	for i, c := range s.Clips {
		dur := int(math.Round((c.Out - c.In) / c.Speed * fps))
		if dur < 1 {
			dur = 1
		}
		tr := PlanTransition{Type: TransitionCut}
		if c.Transition != nil && c.Transition.Type != "" {
			tr.Type = c.Transition.Type
			tr.DurationInFrames = int(math.Round(c.Transition.Duration * fps))
		}
		if i == 0 && IsOverlapTransition(tr.Type) {
			tr.Type = TransitionFade // nothing to cross from
		}
		if c.Transition != nil && tr.Type == c.Transition.Type {
			if transitionHasDirection(tr.Type) {
				tr.Direction = c.Transition.Direction
				if tr.Direction == "" {
					tr.Direction = DirLeft
				}
			}
			if info, ok := TransitionByType(tr.Type); ok && info.Easing {
				tr.Easing = c.Transition.Easing // crossfades keep none unless asked (older plans)
				if tr.Easing == "" && newTransitionType(tr.Type) {
					tr.Easing = EaseInOut
				}
			}
		}
		if IsOverlapTransition(tr.Type) && i > 0 {
			maxOverlap := min(placed[i-1].DurationInFrames, dur) / 2
			if tr.DurationInFrames > maxOverlap {
				tr.DurationInFrames = maxOverlap
			}
			pos -= tr.DurationInFrames
		}
		if IsDipTransition(tr.Type) && i > 0 && placed[i-1].FadeOutFrames < tr.DurationInFrames/2 {
			placed[i-1].FadeOutFrames = tr.DurationInFrames / 2
		}
		if movesOutgoing(tr.Type) && i > 0 && tr.DurationInFrames > 0 {
			out := PlanTransition{Type: tr.Type, DurationInFrames: tr.DurationInFrames, Direction: tr.Direction, Easing: tr.Easing}
			placed[i-1].TransitionOut = &out
		}
		placed = append(placed, PlanClip{
			ID: c.ID, AssetID: c.AssetID, From: pos, DurationInFrames: dur,
			TrimBefore: int(math.Round(c.In * fps)), SourceIn: c.In, SourceOut: c.Out,
			PlaybackRate: c.Speed, Muted: c.Muted, TransitionIn: tr,
			FadeOutFrames: int(math.Round(c.FadeOut * fps)), Label: c.Label,
		})
		pos += dur
	}
	return placed, pos
}

// Resolve turns a normalized spec into a RenderPlan. Sources are left empty for
// the caller to fill with proxy or original URLs (PlanSourceIDs lists them,
// nested cuts' included), and so are ResourceSources' URLs (every file the
// plan draws is a key).
func Resolve(s *Spec, facts Facts, revisionID, projectID, specHash string) *RenderPlan {
	return resolve(s, wrapFacts(s, facts), revisionID, projectID, specHash)
}

func resolve(s *Spec, facts Facts, revisionID, projectID, specHash string) *RenderPlan {
	Normalize(s)
	clips, total := Timeline(s)
	brand := DefaultBrand
	if s.Brand != nil {
		mergeBrand(&brand, *s.Brand)
	}
	safe := platformSafe[s.Output.Platform]
	if safe == ([4]float64{}) {
		safe = platformSafe["generic"]
	}
	h, w := float64(s.Output.Height), float64(s.Output.Width)
	plan := &RenderPlan{
		SchemaVersion: SchemaVersion, RevisionID: revisionID, ProjectID: projectID, SpecHash: specHash,
		Width: s.Output.Width, Height: s.Output.Height, FPS: s.Output.FPS, DurationInFrames: total,
		Background: s.Output.Background, Brand: brand,
		SafeArea: SafeArea{Top: int(h * safe[0]), Bottom: int(h * safe[1]), Left: int(w * safe[2]), Right: int(w * safe[3])},
		Audio:    []PlanAudio{}, Overlays: []PlanOverlay{}, DialogueRanges: []FrameRange{}, Sources: map[string]string{},
	}
	for i := range clips {
		c := s.Clips[i]
		pc := &clips[i]
		vol := 1.0
		if c.Volume != nil {
			vol = *c.Volume
		}
		pc.Volume = vol
		fr := PlanFraming{Mode: "fill", FocusX: 0.5, FocusY: 0.5, Zoom: 1}
		if c.Framing != nil {
			if c.Framing.Mode != "" {
				fr.Mode = c.Framing.Mode
			}
			if c.Framing.FocusX != nil {
				fr.FocusX = *c.Framing.FocusX
			}
			if c.Framing.FocusY != nil {
				fr.FocusY = *c.Framing.FocusY
			}
			if c.Framing.Zoom >= 1 {
				fr.Zoom = c.Framing.Zoom
			}
		}
		if facts != nil {
			if a, ok := facts.Asset(c.AssetID); ok {
				pc.SourceWidth, pc.SourceHeight, pc.HasAudio = a.Width, a.Height, a.HasAudio
				if x, y, ok := FaceFraming(c, a, s.Output, facts); ok {
					fr.FocusX, fr.FocusY, fr.From = x, y, FramingFaces
				}
				if path, ok := FollowPath(c, a, s.Output, pc.DurationInFrames, [2]float64{fr.FocusX, fr.FocusY}, facts); ok {
					fr.Path, fr.From = path, FramingFollow
					fr.FocusX, fr.FocusY = path[0].X, path[0].Y
				}
			}
		}
		pc.Framing = fr
		plan.Clips = append(plan.Clips, *pc)
	}
	beats := ResolveBeats(s, plan.Clips, total, facts)
	sp := resolveSound(s, plan.Clips, facts)
	resolveMix(s, plan, beats, facts, sp) // sets effective clip mutes, dialogue ranges, audio elements and envelopes
	if s.Voiceover != nil {
		plan.Voiceover = planVoiceover(s, beats)
	}
	if s.Captions != nil && s.Captions.Enabled {
		plan.Captions = resolveCaptions(s, plan.Clips, facts, beats, sp)
	}
	plan.Resources = planResources(s, facts)
	for id, pin := range s.Fixed {
		if plan.Fixed == nil {
			plan.Fixed = map[string]string{}
		}
		plan.Fixed[id] = pin.SHA256
	}
	for _, o := range s.Overlays {
		from, dur, ok := OverlayWindow(s, o, plan.Clips, total)
		if !ok || from >= total {
			continue
		}
		if from+dur > total {
			dur = total - from
		}
		ver := "1"
		if t, ok := TemplateByID(o.Template); ok {
			ver = t.Version
		}
		params := o.Params
		if len(params) == 0 {
			params = json.RawMessage(`{}`)
		}
		var segments []PlanSegment
		if len(o.Changes) > 0 {
			segments = OverlaySegments(s, o, plan.Clips, from, dur)
			if len(segments) > 0 {
				params = segments[0].Params
			}
			if len(segments) < 2 {
				segments = nil // the fields never change on screen: Params says it all
			}
		}
		if o.Motion == nil && brand.Logo != "" {
			// The logo made for the graphic's background (D198).
			params = pickPlanLogo(params, brand, o.Template, s.Resources)
			for i := range segments {
				segments[i].Params = pickPlanLogo(segments[i].Params, brand, o.Template, s.Resources)
			}
		}
		po := PlanOverlay{ID: o.ID, Template: o.Template, TemplateVersion: ver, From: from, DurationInFrames: dur, Params: params, Segments: segments}
		if o.Motion != nil {
			region := "center"
			if info, ok := motionInfo(facts, o.Motion.Hash); ok && info.Region != "" {
				region = info.Region
			}
			po.Template, po.TemplateVersion = "", ""
			po.Motion = &PlanMotion{Name: o.Motion.Name, Hash: o.Motion.Hash, Region: region}
			if len(o.Motion.Libs) > 0 {
				po.Motion.Libs = map[string]string{}
				for k, v := range o.Motion.Libs {
					po.Motion.Libs[k] = v
				}
			}
		}
		plan.Overlays = append(plan.Overlays, po)
	}
	if plan.Clips == nil {
		plan.Clips = []PlanClip{}
	}
	if cf := convFactsOf(facts); cf != nil {
		attachConversations(s, plan, cf)
	}
	if nf, ok := facts.(*nestFacts); ok {
		attachNested(s, plan, nf)
	}
	// Every resource file the plan draws, for the caller to give a URL.
	if files := ResourceFiles(plan, s, facts); len(files) > 0 {
		plan.ResourceSources = map[string]string{}
		for _, f := range files {
			plan.ResourceSources[f] = ""
		}
	}
	return plan
}

func mergeBrand(dst *Brand, b Brand) {
	if b.Primary != "" {
		dst.Primary = b.Primary
	}
	if b.Accent != "" {
		dst.Accent = b.Accent
	}
	if b.Text != "" {
		dst.Text = b.Text
	}
	if b.Background != "" {
		dst.Background = b.Background
	}
	if b.Font != "" {
		dst.Font = b.Font
	}
	if b.FontFile != "" {
		dst.FontFile = b.FontFile
	}
	if b.Logo != "" {
		dst.Logo, dst.LogoDark, dst.LogoLight = b.Logo, b.LogoDark, b.LogoLight
	}
}

// mapSourceToTimeline converts a source time inside clip c to a timeline frame.
func mapSourceToTimeline(pc PlanClip, fps float64, t float64) int {
	return pc.From + int(math.Round((t-pc.SourceIn)/pc.PlaybackRate*fps))
}

// dialogueRanges finds timeline spans with speech (from transcripts) for ducking:
// what each clip's sound window lets through.
func dialogueRanges(s *Spec, clips []PlanClip, facts Facts) []FrameRange {
	return dialogueRangesSP(s, clips, facts, resolveSound(s, clips, facts))
}

func dialogueRangesSP(s *Spec, clips []PlanClip, facts Facts, sp *soundPlan) []FrameRange {
	fps := float64(s.Output.FPS)
	var out []FrameRange
	for i, pc := range clips {
		if pc.Muted {
			continue
		}
		var segs []TranscriptSegment
		if facts != nil {
			segs = heardWords(s, i, pc.AssetID, facts)
		}
		in, outT := pc.SourceIn, pc.SourceOut
		if sp != nil && i < len(sp.win) {
			in, outT = sp.win[i].heardIn, sp.win[i].heardOut
		}
		for _, seg := range segs {
			from, to := math.Max(seg.From, in), math.Min(seg.To, outT)
			if to <= from {
				continue
			}
			out = append(out, FrameRange{From: mapSourceToTimeline(pc, fps, from), To: mapSourceToTimeline(pc, fps, to)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	merged := []FrameRange{}
	for _, r := range out {
		if n := len(merged); n > 0 && r.From <= merged[n-1].To+int(fps/2) {
			if r.To > merged[n-1].To {
				merged[n-1].To = r.To
			}
			continue
		}
		merged = append(merged, r)
	}
	return merged
}

// captionSources reports which speech a caption source covers.
func captionSources(c *Captions) (dialogue, narration bool) {
	switch c.Source {
	case "voiceover":
		return false, true
	case "both":
		return true, true
	case "manual":
		return false, false
	}
	return true, false // "", transcript, dialogue
}

// CaptionPrecision reports the timing evidence available for captions:
// "word", "segment" (line-level or estimated) or "" (no evidence).
func CaptionPrecision(s *Spec, facts Facts) string {
	if s.Captions != nil && s.Captions.Source == "manual" {
		return "segment"
	}
	dialogue, narration := true, false
	if s.Captions != nil {
		dialogue, narration = captionSources(s.Captions)
	}
	prec := ""
	if dialogue {
		prec = dialoguePrecision(s, facts)
	}
	if narration {
		np := narrationPrecision(s, facts)
		switch {
		case np == "":
		case prec == "" || prec == "word" && np == "segment":
			prec = np
		}
	}
	return prec
}

func dialoguePrecision(s *Spec, facts Facts) string {
	if facts == nil {
		return ""
	}
	prec := "word"
	any := false
	for _, c := range s.Clips {
		a, ok := facts.Asset(c.AssetID)
		if !ok || c.Muted || !a.HasAudio || (s.Mix != nil && s.Mix.MuteSource) {
			continue
		}
		switch a.TranscriptPrecision {
		case "word":
			any = true
		case "segment":
			any = true
			prec = "segment"
		default:
			return ""
		}
	}
	if !any {
		return ""
	}
	return prec
}

// narrationPrecision is "word" when every beat's take has word-timed speech,
// "segment" when some timing is estimated from the script, "" without beats.
func narrationPrecision(s *Spec, facts Facts) string {
	if s.Voiceover == nil || len(s.Voiceover.Beats) == 0 {
		return ""
	}
	for _, b := range s.Voiceover.Beats {
		if b.Take == nil || facts == nil {
			return "segment"
		}
		a, ok := facts.Asset(b.Take.AssetID)
		if !ok || a.TranscriptPrecision != "word" {
			return "segment"
		}
	}
	return "word"
}

func resolveCaptions(s *Spec, clips []PlanClip, facts Facts, beats []ResolvedBeat, sp *soundPlan) *PlanCaptions {
	fps := float64(s.Output.FPS)
	cap := s.Captions
	pc := &PlanCaptions{Style: cap.Style, Position: cap.Position, Uppercase: cap.Uppercase, ShowSpeakers: cap.ShowSpeakers, Lines: []PlanCaptionLine{}}
	if cap.Size == CaptionSmall || cap.Size == CaptionLarge {
		pc.Size = cap.Size
	}
	emph := map[string]bool{}
	for _, e := range cap.Emphasis {
		emph[normWord(e)] = true
	}
	if cap.Source == "manual" {
		pc.Precision = "segment"
		for _, seg := range cap.Segments {
			pc.Lines = append(pc.Lines, PlanCaptionLine{From: int(math.Round(seg.From * fps)), To: int(math.Round(seg.To * fps)), Text: seg.Text, Speaker: seg.Speaker, Words: []PlanWord{}})
		}
		return pc
	}
	pc.Precision = CaptionPrecision(s, facts)
	if pc.Precision == "" {
		return pc
	}
	if l := CaptionLook(pc.Style); pc.Precision == "segment" && l.WordTimed {
		pc.Style = l.Fallback // a look that shows the spoken word needs word timing evidence
	}
	dialogue, narration := captionSources(cap)
	split := false
	if dialogue && facts != nil && dialoguePrecision(s, facts) != "" {
		var dl []PlanCaptionLine
		dl, split = dialogueLines(s, clips, facts, fps, cap.MaxWordsPerLine, emph, sp)
		pc.Lines = append(pc.Lines, dl...)
	}
	if narration {
		nl := narrationLines(s, beats, facts, fps, cap.MaxWordsPerLine, emph)
		if !dialogue {
			// Only the narration: one voice, nothing to tell apart (as with a single
			// unnamed on-camera voice), so no name above the lines.
			for i := range nl {
				nl[i].Speaker = ""
			}
			pc.ShowSpeakers = false
		}
		pc.Lines = append(pc.Lines, nl...)
	}
	if narration || split {
		sort.SliceStable(pc.Lines, func(i, j int) bool { return pc.Lines[i].From < pc.Lines[j].From })
		// One line at a time: an earlier line ends where the next begins.
		for i := 0; i+1 < len(pc.Lines); i++ {
			if next := pc.Lines[i+1].From; pc.Lines[i].To > next {
				pc.Lines[i].To = max(pc.Lines[i].From+1, next)
			}
		}
	}
	// Keep each line on screen until the next starts (bounded) for readability.
	for i := range pc.Lines {
		if i+1 < len(pc.Lines) {
			next := pc.Lines[i+1].From
			if next > pc.Lines[i].To && next-pc.Lines[i].To < int(fps*0.5) {
				pc.Lines[i].To = next
			}
		}
	}
	return pc
}

// timedWord is a caption word in timeline frames before line grouping.
type timedWord struct {
	text     string
	from, to int
	gapSec   float64 // pause before this word (seconds)
	src      *WordSource
}

func groupLines(words []timedWord, speaker string, maxWords int, emph map[string]bool) []PlanCaptionLine {
	var out []PlanCaptionLine
	var line []PlanWord
	flush := func() {
		if len(line) == 0 {
			return
		}
		texts := make([]string, len(line))
		for i, w := range line {
			texts[i] = w.Text
		}
		out = append(out, PlanCaptionLine{From: line[0].From, To: line[len(line)-1].To, Text: textalign.JoinWords(texts), Speaker: speaker, Words: line})
		line = nil
	}
	for i, w := range words {
		if len(line) > 0 && (len(line) >= maxWords || w.gapSec > 0.6 || endsSentence(words[i-1].text)) {
			flush()
		}
		text := strings.TrimSpace(w.text)
		to := w.to
		if to <= w.from {
			to = w.from + 1
		}
		line = append(line, PlanWord{Text: text, From: w.from, To: to, Emphasis: emph[normWord(text)], Src: w.src})
	}
	flush()
	return out
}

// dialogueLines captions what each clip's sound lets through, timed when it is
// heard: with a split edit a voice that starts before its picture is captioned
// before the shot changes. split reports whether any clip's sound has its own window.
func dialogueLines(s *Spec, clips []PlanClip, facts Facts, fps float64, maxWords int, emph map[string]bool, sp *soundPlan) ([]PlanCaptionLine, bool) {
	var out []PlanCaptionLine
	split := false
	for i, clip := range clips {
		if clip.Muted {
			continue
		}
		in, outT := clip.SourceIn, clip.SourceOut
		if sp != nil && i < len(sp.win) {
			in, outT = sp.win[i].heardIn, sp.win[i].heardOut
			split = split || sp.win[i].separate
		}
		for _, seg := range heardWords(s, i, clip.AssetID, facts) {
			if seg.To <= in || seg.From >= outT {
				continue
			}
			if len(seg.Words) == 0 {
				from, to := math.Max(seg.From, in), math.Min(seg.To, outT)
				out = append(out, PlanCaptionLine{From: mapSourceToTimeline(clip, fps, from), To: mapSourceToTimeline(clip, fps, to), Text: strings.TrimSpace(seg.Text), Speaker: seg.Speaker, Words: []PlanWord{}})
				continue
			}
			var words []timedWord
			for i, w := range seg.Words {
				mid := (w.From + w.To) / 2
				if mid < in || mid >= outT {
					continue
				}
				gap := 0.0
				if i > 0 {
					gap = w.From - seg.Words[i-1].To
				}
				tw := timedWord{text: w.Text, gapSec: gap,
					from: mapSourceToTimeline(clip, fps, math.Max(w.From, in)), to: mapSourceToTimeline(clip, fps, math.Min(w.To, outT))}
				if !IsConversation(clip.AssetID) {
					tw.src = &WordSource{AssetID: clip.AssetID, ClipID: clip.ID, From: w.From, To: w.To, Said: w.Said}
				}
				words = append(words, tw)
			}
			out = append(out, groupLines(words, seg.Speaker, maxWords, emph)...)
		}
	}
	return out, split
}

// NarrationSpeaker names narration caption lines when captions mix dialogue
// and narration (with showSpeakers it appears above them, like the on-camera
// speakers' names). Narration-only captions carry no name: there is one voice.
const NarrationSpeaker = "Narrator"

// narrationLines captions each beat from its take's word timings (transcript of
// a recording, TTS alignment) or, without them, from the script spread over the
// beat's speech in proportion to word length.
func narrationLines(s *Spec, beats []ResolvedBeat, facts Facts, fps float64, maxWords int, emph map[string]bool) []PlanCaptionLine {
	var out []PlanCaptionLine
	for _, rb := range beats {
		if rb.Problem != "" {
			continue
		}
		b := s.Voiceover.Beats[rb.Index]
		var words []timedWord
		if t := b.Take; t != nil && facts != nil {
			rate := rb.Rate
			if rate <= 0 {
				rate = 1
			}
			var prev *TranscriptWord
			for _, seg := range facts.Transcript(t.AssetID) {
				for i := range seg.Words {
					w := seg.Words[i]
					mid := (w.From + w.To) / 2
					if mid < t.In || mid >= t.Out {
						continue
					}
					gap := 0.0
					if prev != nil {
						gap = w.From - prev.To
					}
					from := rb.Start + int(math.Round((math.Max(w.From, t.In)-t.In)/rate*fps))
					to := rb.Start + int(math.Round((math.Min(w.To, t.Out)-t.In)/rate*fps))
					words = append(words, timedWord{text: w.Text, from: from, to: to, gapSec: gap, src: &WordSource{AssetID: t.AssetID, BeatID: b.ID, From: w.From, To: w.To}})
					prev = &seg.Words[i]
				}
			}
		}
		if len(words) == 0 {
			words = estimatedWords(b.Text, rb.Start, rb.Speech)
		}
		out = append(out, groupLines(words, NarrationSpeaker, maxWords, emph)...)
	}
	return out
}

// estimatedWords spreads a line's words over [start, start+frames) in
// proportion to their length (captions for narration that has no word timing).
func estimatedWords(text string, start, frames int) []timedWord {
	toks := textalign.Tokens(text)
	if len(toks) == 0 || frames <= 0 {
		return nil
	}
	total := 0
	for _, t := range toks {
		total += len([]rune(t)) + 1
	}
	var out []timedWord
	acc := 0
	for _, t := range toks {
		n := len([]rune(t)) + 1
		from := start + frames*acc/total
		acc += n
		to := start + frames*acc/total
		out = append(out, timedWord{text: t, from: from, to: max(from+1, to-1)})
	}
	return out
}

func endsSentence(w string) bool {
	w = strings.TrimSpace(w)
	return strings.HasSuffix(w, ".") || strings.HasSuffix(w, "?") || strings.HasSuffix(w, "!")
}

func normWord(w string) string {
	return strings.ToLower(strings.Trim(w, " .,!?;:\"'“”‘’()-"))
}
