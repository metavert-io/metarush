package editspec

// Voiceover is the narration of a cut: an ordered script of beats, each placed
// against the cut by a structured anchor that Go resolves to frames. Beats are
// normally anchored conversationally — to a clip ("when the b-roll starts,
// +0.5 s"), to a moment in the footage ("right after she laughs") or to the
// previous beat — and can also sit at an exact timeline clock time. Re-editing
// the cut moves clip- and moment-anchored beats with their clips.
//
// The script is part of the spec, so it is versioned, diffable and approved
// through edit check/apply like everything else. Audio is ordinary assets
// (role voiceover) referenced by each beat's take.
type Voiceover struct {
	Beats    []Beat             `json:"beats" bson:"beats" jsonschema:"maxItems=300"`
	Settings *VoiceoverSettings `json:"settings,omitempty" bson:"settings,omitempty"`
	// Layout is how the audio plays: beats (default) places each beat's take at
	// its own anchor; continuous plays one recording from Continuous.Anchor and
	// uses the beats' takes only to know where each line falls inside it.
	Layout     string        `json:"layout,omitempty" bson:"layout,omitempty" jsonschema:"enum=beats,enum=continuous"`
	Continuous *ContinuousVO `json:"continuous,omitempty" bson:"continuous,omitempty"`
	Locked     bool          `json:"locked,omitempty" bson:"locked,omitempty"`
	// Original is the script exactly as the person pasted or imported it. It is
	// never changed afterwards (editing, recording or fitting lines leaves it as
	// it was); only a new whole script replaces it.
	Original string `json:"original,omitempty" bson:"original,omitempty" jsonschema:"maxLength=200000,description=The script exactly as the person pasted or imported it (never changed afterwards; a new whole script replaces it)."`
	// Fitted is Claude's version of the script fitted to the video (D86): one
	// entry per line of the person's script with Claude's words, start point and
	// reason, and the lines the person kept as they wrote them.
	Fitted *FittedScript `json:"fitted,omitempty" bson:"fitted,omitempty"`
	// Active says which version the lines (Beats) are: original (the person's
	// own script, the default) or fitted (Claude's version). Recording and the
	// AI voice always use the lines in Beats.
	Active string `json:"active,omitempty" bson:"active,omitempty" jsonschema:"enum=original,enum=fitted,description=Which version of the script the lines are (original by default)."`
	// Parked keeps the lines of the version not in use exactly as they were when
	// the person switched (their sound and places included), so switching back
	// loses nothing. They are not part of the cut.
	Parked []Beat `json:"parked,omitempty" bson:"parked,omitempty" jsonschema:"maxItems=300,description=The lines of the script version not in use (not part of the cut)."`
}

// Script versions.
const (
	VersionOriginal = "original"
	VersionFitted   = "fitted"
)

// FittedScript is Claude's proposal for fitting the person's script to the
// video, as the person accepted it line by line.
type FittedScript struct {
	Lines []FittedLine `json:"lines" bson:"lines" jsonschema:"maxItems=300"`
	// Note is Claude's summary of what it changed and why.
	Note string `json:"note,omitempty" bson:"note,omitempty" jsonschema:"maxLength=2000"`
}

// FittedLine is one line of the fitted version: Claude's words and start point
// for one line of the person's script, with the reason.
type FittedLine struct {
	ID       string  `json:"id" bson:"id" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64,description=The person's line this fits (its ID)."`
	Original string  `json:"original" bson:"original" jsonschema:"maxLength=2000,description=The person's words for the line, as Claude was given them."`
	Text     string  `json:"text" bson:"text" jsonschema:"minLength=1,maxLength=2000,description=Claude's words for the line (the same words when only the timing changes)."`
	Anchor   *Anchor `json:"anchor,omitempty" bson:"anchor,omitempty" jsonschema:"description=Where Claude starts the line (none: it follows the previous line)."`
	Reason   string  `json:"reason,omitempty" bson:"reason,omitempty" jsonschema:"maxLength=600,description=Why Claude changed it, in plain words."`
	// Kept is the person's choice: their own line here, not Claude's.
	Kept bool `json:"kept,omitempty" bson:"kept,omitempty" jsonschema:"description=The person kept their own line here (Claude's change turned down)."`
}

// Voiceover layouts.
const (
	LayoutBeats      = "beats"
	LayoutContinuous = "continuous"
)

// VoiceoverSettings tune timing and fitting.
type VoiceoverSettings struct {
	SpeakingRateWPM float64 `json:"speakingRateWpm,omitempty" bson:"speakingRateWpm,omitempty" jsonschema:"minimum=60,maximum=320,description=Words per minute for fit checks of beats without audio (default 155; a recording sets the measured rate)."`
	RateSource      string  `json:"rateSource,omitempty" bson:"rateSource,omitempty" jsonschema:"enum=default,enum=measured,enum=user"`
	FitPolicy       string  `json:"fitPolicy,omitempty" bson:"fitPolicy,omitempty" jsonschema:"enum=auto,enum=ask,enum=shorten,description=auto: recorded or imported audio extends the shot and TTS speeds up at most 8%; ask: warn and wait; shorten: rewrite the line. Warnings are always shown."`
	Gap             float64 `json:"gap,omitempty" bson:"gap,omitempty" jsonschema:"minimum=0,maximum=5,description=Default pause (s) before a beat anchored after the previous one (default 0.3)."`
}

// Fit policies.
const (
	FitAuto    = "auto"
	FitAsk     = "ask"
	FitShorten = "shorten"
)

// DefaultWPM is the speaking rate assumed for fit checks until a recording
// measures the narrator's own rate.
const DefaultWPM = 155.0

// MaxTTSSpeedUp is how much the auto fit policy may speed up generated speech.
const MaxTTSSpeedUp = 1.08

// Beat is one line of narration.
type Beat struct {
	ID        string     `json:"id" bson:"id" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64"`
	Text      string     `json:"text" bson:"text" jsonschema:"minLength=1,maxLength=2000,description=The words to speak."`
	Anchor    Anchor     `json:"anchor" bson:"anchor"`
	Delivery  string     `json:"delivery,omitempty" bson:"delivery,omitempty" jsonschema:"maxLength=300,description=How to say it (warm\\, slower\\, smiling): shown to whoever records and sent to TTS as context\\, never spoken."`
	Rationale string     `json:"rationale,omitempty" bson:"rationale,omitempty" jsonschema:"maxLength=2000"`
	Evidence  []Evidence `json:"evidence,omitempty" bson:"evidence,omitempty" jsonschema:"maxItems=10"`
	// Voice overrides the project/workspace TTS voice for this beat (e.g. a
	// second narrator). Resolution: beat → project → workspace.
	Voice  *VoiceRef `json:"voice,omitempty" bson:"voice,omitempty"`
	Take   *TakeRef  `json:"take,omitempty" bson:"take,omitempty"`
	GainDB *float64  `json:"gainDb,omitempty" bson:"gainDb,omitempty" jsonschema:"minimum=-30,maximum=12"`
	State  string    `json:"state,omitempty" bson:"state,omitempty" jsonschema:"enum=draft,enum=scratch,enum=generated,enum=recorded,enum=imported,enum=final,description=Where this beat's audio stands (informational)."`
	Locked bool      `json:"locked,omitempty" bson:"locked,omitempty"`
}

// Anchor kinds.
const (
	AnchorClip   = "clip"   // a clip's start or end (+offset): moves with the clip
	AnchorMoment = "moment" // a source moment (asset + source time): moves with the clip that shows it
	AnchorAfter  = "after"  // after the previous beat's audio (+gap)
	AnchorTime   = "time"   // an exact timeline clock time
)

// Anchor says where a beat (or a continuous recording) starts.
type Anchor struct {
	Kind    string   `json:"kind" bson:"kind" jsonschema:"enum=clip,enum=moment,enum=after,enum=time"`
	ClipID  string   `json:"clipId,omitempty" bson:"clipId,omitempty" jsonschema:"maxLength=64,description=clip: the clip; moment: optional\\, the clip that shows the moment when several do."`
	Edge    string   `json:"edge,omitempty" bson:"edge,omitempty" jsonschema:"enum=start,enum=end,description=clip: its start (default) or end."`
	Offset  float64  `json:"offset,omitempty" bson:"offset,omitempty" jsonschema:"minimum=-60,maximum=60,description=Seconds added to the anchor point (negative = earlier)."`
	AssetID string   `json:"assetId,omitempty" bson:"assetId,omitempty" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=moment: the source asset."`
	At      *float64 `json:"at,omitempty" bson:"at,omitempty" jsonschema:"minimum=0,description=moment: source seconds in assetId; time: timeline seconds."`
	Quote   string   `json:"quote,omitempty" bson:"quote,omitempty" jsonschema:"maxLength=300,description=What happens at the moment\\, in words (e.g. 'right after she laughs')."`
	Ref     string   `json:"ref,omitempty" bson:"ref,omitempty" jsonschema:"maxLength=200,description=Transcript segment or insight ID behind a moment."`
	Gap     *float64 `json:"gap,omitempty" bson:"gap,omitempty" jsonschema:"minimum=0,maximum=10,description=after: pause after the previous beat (default settings.gap)."`
}

// TakeRef is the audio a beat plays: a range of an ordinary audio asset. A
// cleanup preset produces a second asset; UseProcessed switches between them
// (A/B) without losing either.
type TakeRef struct {
	TakeID       string        `json:"takeId,omitempty" bson:"takeId,omitempty" jsonschema:"pattern=^tk_[0-9a-z]{26}$"`
	AssetID      string        `json:"assetId" bson:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	In           float64       `json:"in" bson:"in" jsonschema:"minimum=0"`
	Out          float64       `json:"out" bson:"out" jsonschema:"minimum=0"`
	Speed        float64       `json:"speed,omitempty" bson:"speed,omitempty" jsonschema:"minimum=0.5,maximum=2,description=Playback rate (pitch preserved). Fitting may speed TTS up by at most 8%."`
	Source       string        `json:"source,omitempty" bson:"source,omitempty" jsonschema:"enum=scratch,enum=tts,enum=recorded,enum=imported"`
	AIVoice      bool          `json:"aiVoice,omitempty" bson:"aiVoice,omitempty" jsonschema:"description=Generated by a synthetic voice (TTS or a voice clone); carried into delivery sidecars."`
	Processed    *ProcessedRef `json:"processed,omitempty" bson:"processed,omitempty"`
	UseProcessed bool          `json:"useProcessed,omitempty" bson:"useProcessed,omitempty" jsonschema:"description=Play the processed (cleanup) version instead of the original (A/B)."`
}

// ProcessedRef is a derived version of a take (voice cleanup), same timing.
type ProcessedRef struct {
	AssetID string `json:"assetId" bson:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	Preset  string `json:"preset" bson:"preset" jsonschema:"maxLength=40"`
	// Offset is how far the processed file is shifted against the original
	// (silence trim); In/Out of the take are given in the original's time.
	Offset float64 `json:"offset,omitempty" bson:"offset,omitempty"`
}

// PlayAsset is the asset this take plays and the time offset to apply to In/Out.
func (t *TakeRef) PlayAsset() (assetID string, offset float64) {
	if t.UseProcessed && t.Processed != nil && t.Processed.AssetID != "" {
		return t.Processed.AssetID, t.Processed.Offset
	}
	return t.AssetID, 0
}

// Rate returns the take's playback rate (1 when unset).
func (t *TakeRef) Rate() float64 {
	if t.Speed <= 0 {
		return 1
	}
	return t.Speed
}

// ContinuousVO is one recording played from a single anchor.
type ContinuousVO struct {
	AssetID      string        `json:"assetId" bson:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	In           float64       `json:"in" bson:"in" jsonschema:"minimum=0"`
	Out          float64       `json:"out" bson:"out" jsonschema:"minimum=0"`
	Anchor       Anchor        `json:"anchor" bson:"anchor"`
	GainDB       *float64      `json:"gainDb,omitempty" bson:"gainDb,omitempty" jsonschema:"minimum=-30,maximum=12"`
	Source       string        `json:"source,omitempty" bson:"source,omitempty" jsonschema:"enum=scratch,enum=tts,enum=recorded,enum=imported"`
	AIVoice      bool          `json:"aiVoice,omitempty" bson:"aiVoice,omitempty"`
	Processed    *ProcessedRef `json:"processed,omitempty" bson:"processed,omitempty"`
	UseProcessed bool          `json:"useProcessed,omitempty" bson:"useProcessed,omitempty"`
}

// VoiceRef selects a synthetic voice. Provider "local" is the free scratch voice
// (macOS say / espeak-ng); "elevenlabs" uses the configured ElevenLabs key.
type VoiceRef struct {
	Provider string            `json:"provider" bson:"provider" jsonschema:"enum=local,enum=elevenlabs"`
	VoiceID  string            `json:"voiceId,omitempty" bson:"voiceId,omitempty" jsonschema:"maxLength=120"`
	Name     string            `json:"name,omitempty" bson:"name,omitempty" jsonschema:"maxLength=120"`
	ModelID  string            `json:"modelId,omitempty" bson:"modelId,omitempty" jsonschema:"maxLength=120"`
	Settings *TTSVoiceSettings `json:"settings,omitempty" bson:"settings,omitempty"`
}

// TTSVoiceSettings are provider voice settings (ElevenLabs semantics).
type TTSVoiceSettings struct {
	Stability       *float64 `json:"stability,omitempty" bson:"stability,omitempty" jsonschema:"minimum=0,maximum=1"`
	SimilarityBoost *float64 `json:"similarityBoost,omitempty" bson:"similarityBoost,omitempty" jsonschema:"minimum=0,maximum=1"`
	Style           *float64 `json:"style,omitempty" bson:"style,omitempty" jsonschema:"minimum=0,maximum=1"`
	Speed           *float64 `json:"speed,omitempty" bson:"speed,omitempty" jsonschema:"minimum=0.7,maximum=1.2"`
	SpeakerBoost    *bool    `json:"speakerBoost,omitempty" bson:"speakerBoost,omitempty"`
	OutputFormat    string   `json:"outputFormat,omitempty" bson:"outputFormat,omitempty" jsonschema:"maxLength=40,description=Provider output format (e.g. mp3_44100_128)."`
}

// Mix is the audio mix: four buses, automatic ducking and a master loudness
// target. Go resolves it into per-element gain envelopes in the RenderPlan, so
// the dashboard preview and the final render mix identically.
type Mix struct {
	Source    *Bus `json:"source,omitempty" bson:"source,omitempty" jsonschema:"description=The clips' own audio (production sound) and dialogue tracks."`
	Voiceover *Bus `json:"voiceover,omitempty" bson:"voiceover,omitempty"`
	Music     *Bus `json:"music,omitempty" bson:"music,omitempty"`
	SFX       *Bus `json:"sfx,omitempty" bson:"sfx,omitempty"`
	// MuteSource mutes the original audio of every clip (per clip: clip.muted).
	MuteSource bool            `json:"muteSource,omitempty" bson:"muteSource,omitempty"`
	Ducking    *Ducking        `json:"ducking,omitempty" bson:"ducking,omitempty"`
	Loudness   *MasterLoudness `json:"loudness,omitempty" bson:"loudness,omitempty"`
	// LevelSpeech evens out how loud people sound from clip to clip (different
	// cameras, microphones or distances): every clip with speech is turned up or
	// down toward the cut's typical speech level, from the loudness measured when
	// the footage was imported. New cuts start with it on; when it is absent
	// (cuts made before it existed) clips keep their recorded level.
	LevelSpeech *bool `json:"levelSpeech,omitempty" bson:"levelSpeech,omitempty" jsonschema:"description=Even out speech loudness between clips (on for new cuts; absent = off\\, as older cuts were made)."`
	// CutBlend is how long the sound blends at a straight cut so it never clicks
	// (seconds; new cuts start with 0.1). Absent: no blend, as older cuts were made.
	// A transition's own audio.blend wins.
	CutBlend *float64 `json:"cutBlend,omitempty" bson:"cutBlend,omitempty" jsonschema:"minimum=0,maximum=0.5,description=Seconds the sound blends at straight cuts so they never click (new cuts: 0.1; absent = none\\, as older cuts were made)."`
	// Voices adjusts single people's voices in the clips' own sound and in
	// dialogue tracks: a level change and a clearer voice over the stretches
	// where that person speaks (from the speaker data), or over a whole
	// recording (assetId: one person's own track). See voices.go.
	Voices []VoiceSound `json:"voices,omitempty" bson:"voices,omitempty" jsonschema:"maxItems=50,description=Per person (by the name the speaker data shows) or per recording: a level change and a clearer voice over their speech."`
	// Enhanced is the processed sound recordings play instead of their own:
	// copies `sound enhance` made from the voices' settings (recordings are
	// never changed). Each covers one stretch of one recording, with the same
	// timing from Start.
	Enhanced []EnhancedSound `json:"enhanced,omitempty" bson:"enhanced,omitempty" jsonschema:"maxItems=200,description=The processed copies (clearer voices) recordings play instead of their own sound; written by sound enhance."`
	// Said is words changed in the speaker's own voice copy, with their
	// consent (D308): the new sound plays instead of the recording's own over
	// each stretch. Written by cut say place; see said.go.
	Said   []SaidSound `json:"said,omitempty" bson:"said,omitempty" jsonschema:"maxItems=100,description=Words changed in the speaker's own voice copy\\, with their consent: the new sound plays instead of the recording's own over each stretch. Written by cut say place."`
	Locked bool        `json:"locked,omitempty" bson:"locked,omitempty"`
}

// VoiceSound is one person's voice (Name) — or one whole recording's sound
// (AssetID) — in the mix: a level change over their speech and how it is made
// clearer. Exactly one of Name and AssetID is set.
type VoiceSound struct {
	Name    string   `json:"name,omitempty" bson:"name,omitempty" jsonschema:"maxLength=120,description=The person as the speaker data names them (Maya\\, Speaker 2)."`
	AssetID string   `json:"assetId,omitempty" bson:"assetId,omitempty" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=Instead of a person: a whole recording (one person's own track)."`
	GainDB  *float64 `json:"gainDb,omitempty" bson:"gainDb,omitempty" jsonschema:"minimum=-30,maximum=12,description=Level change over their speech in dB (0 = as recorded)."`
	Enhance *Enhance `json:"enhance,omitempty" bson:"enhance,omitempty"`
}

// Enhance modes.
const (
	EnhanceClean  = "clean"  // on this machine, fast: less hiss and room echo, softer harsh s sounds and pops, clearer tone, even level
	EnhanceStudio = "studio" // on this machine, slower and stronger: the voice is rebuilt (an optional component)
	EnhanceCloud  = "cloud"  // ElevenLabs voice isolation (paid per minute, opt-in)
)

// EnhanceModes lists the modes in the order people see them.
var EnhanceModes = []string{EnhanceClean, EnhanceStudio, EnhanceCloud}

// Enhance is how a voice is made clearer. The original is kept: Strength mixes
// the processed voice with it (1 = only the processed voice), and Off plays
// the original while keeping the settings and the processed copy.
type Enhance struct {
	Mode     string   `json:"mode" bson:"mode" jsonschema:"enum=clean,enum=studio,enum=cloud,description=clean: on this machine\\, fast (less hiss and echo\\, softer harsh s sounds\\, clearer tone); studio: on this machine\\, slower and stronger (optional component); cloud: ElevenLabs voice isolation (paid)."`
	Strength *float64 `json:"strength,omitempty" bson:"strength,omitempty" jsonschema:"minimum=0,maximum=1,description=How much of the processed voice is heard (0–1; default 1: all of it)."`
	Off      bool     `json:"off,omitempty" bson:"off,omitempty" jsonschema:"description=Play the original voice (the settings and the processed copy are kept)."`
}

// EnhancedSound is a processed copy of part of a recording: Variant plays in
// place of AssetID's own sound from Start (whole seconds into the recording,
// so it lines up at every frame rate) for the copy's length.
type EnhancedSound struct {
	AssetID  string  `json:"assetId" bson:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=The recording."`
	Start    float64 `json:"start" bson:"start" jsonschema:"minimum=0,description=Where in the recording the copy starts (whole seconds)."`
	Variant  string  `json:"variant" bson:"variant" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=The processed copy (an audio asset with the recording's timing from start)."`
	Settings string  `json:"settings" bson:"settings" jsonschema:"maxLength=2000,description=The voice settings it was made with (who\\, how\\, how strongly), so MetaRush can tell when it is out of date."`
}

// Defaults written into new cuts (see NewCutDefaults).
const (
	DefaultCutBlend   = 0.1 // seconds
	DefaultSplitBlend = 0.1 // seconds, a split edit's blend when neither the transition nor the mix sets one
	DefaultAutoSplit  = 1.0 // seconds the auto split may move the sound at most
	MaxLevelDB        = 10.0
)

// onlyCutSettings reports whether a mix section holds nothing but the sound-at-
// cuts settings (levelSpeech, cutBlend; a lock) and voice settings (voices,
// enhanced): such a cut keeps the original mixer behavior (no automatic ducking
// or normalization), as before the settings existed — making a voice clearer or
// a person louder does not switch the rest of the mixer. An empty mix section
// without them still counts as a mix (unchanged).
func (m *Mix) onlyCutSettings() bool {
	if m == nil || (m.LevelSpeech == nil && m.CutBlend == nil && len(m.Voices) == 0 && len(m.Enhanced) == 0 && len(m.Said) == 0) {
		return false
	}
	c := *m
	c.LevelSpeech, c.CutBlend, c.Locked, c.Voices, c.Enhanced, c.Said = nil, nil, false, nil, nil, nil
	return equalJSON(c, Mix{})
}

// LevelsSpeech reports whether the cut evens out speech loudness between clips.
func (s *Spec) LevelsSpeech() bool {
	return s.Mix != nil && s.Mix.LevelSpeech != nil && *s.Mix.LevelSpeech
}

// NewCutDefaults gives a new cut the current sound-at-cuts settings (speech
// leveling on, 0.1 s blends at straight cuts) and framing (faces kept in frame)
// unless it sets them itself. base is the revision the cut replaces (nil for a
// project's first cut): a full rewrite of an older cut keeps that cut's sound and
// framing (nothing is added), a rewrite of a newer cut keeps its settings. A
// house style (D199; at most one, from the Content Settings the project uses)
// gives a project's first cut its choices instead of MetaRush's: sound at
// cuts, framing, captions, the name bar's look, the name titles' look and the
// intro and end card; with none, or an empty one, nothing changes. It returns
// whether anything was added.
func NewCutDefaults(s *Spec, base *Spec, style ...*HouseStyle) bool {
	var h *HouseStyle
	if len(style) > 0 && base == nil {
		h = style[0]
	}
	sound := newCutSound(s, base, h)
	framing := newCutFraming(s, base, h)
	styled := false
	if h != nil {
		styled = newCutStyle(s, h)
	}
	return sound || framing || styled
}

// newCutFraming writes how a new cut frames its crops: faces kept in frame
// (output.framing "faces"), or the house style's framing. A full rewrite keeps
// the framing of the cut it replaces, and adds nothing to a cut made before
// the setting existed, so an older cut keeps its framing however its footage
// is indexed later (D73).
func newCutFraming(s *Spec, base *Spec, h *HouseStyle) bool {
	if s.Output.Framing != "" {
		return false
	}
	want := FramingFaces
	if h != nil && h.Framing != "" {
		want = h.Framing
	}
	if base != nil {
		want = base.Output.Framing
	}
	if want == "" {
		return false
	}
	s.Output.Framing = want
	return true
}

// newCutSound gives a new cut speech leveling and click-free straight cuts, or
// the house style's sound at cuts (see NewCutDefaults).
func newCutSound(s *Spec, base *Spec, h *HouseStyle) bool {
	level, blend := true, DefaultCutBlend
	if h != nil && h.Sound != nil {
		if h.Sound.LevelSpeech != nil {
			level = *h.Sound.LevelSpeech
		}
		if h.Sound.CutBlend != nil {
			blend = *h.Sound.CutBlend
		}
	}
	var lp *bool = &level
	bp := &blend
	if base != nil {
		if base.Mix == nil {
			return false
		}
		lp, bp = base.Mix.LevelSpeech, base.Mix.CutBlend
		if lp == nil && bp == nil {
			return false
		}
	}
	m := Mix{}
	if s.Mix != nil {
		m = *s.Mix
	}
	changed := false
	if m.LevelSpeech == nil && lp != nil {
		v := *lp
		m.LevelSpeech = &v
		changed = true
	}
	if m.CutBlend == nil && bp != nil {
		v := *bp
		m.CutBlend = &v
		changed = true
	}
	if changed {
		s.Mix = &m
	}
	return changed
}

// Bus names.
const (
	BusSource    = "source"
	BusVoiceover = "voiceover"
	BusMusic     = "music"
	BusSFX       = "sfx"
)

// Buses lists the bus names in mixer order.
var Buses = []string{BusSource, BusVoiceover, BusMusic, BusSFX}

// Bus is one mixer channel.
type Bus struct {
	GainDB *float64 `json:"gainDb,omitempty" bson:"gainDb,omitempty" jsonschema:"minimum=-60,maximum=12"`
	Mute   bool     `json:"mute,omitempty" bson:"mute,omitempty"`
	Solo   bool     `json:"solo,omitempty" bson:"solo,omitempty"`
}

// Ducking configures automatic ducking: music and clip audio under narration,
// music under on-camera speech. Levels are dB changes (negative).
type Ducking struct {
	Enabled                *bool    `json:"enabled,omitempty" bson:"enabled,omitempty" jsonschema:"description=Automatic ducking (default on whenever the cut has a voiceover or a mix section)."`
	MusicUnderVoiceoverDB  *float64 `json:"musicUnderVoiceoverDb,omitempty" bson:"musicUnderVoiceoverDb,omitempty" jsonschema:"minimum=-40,maximum=0,description=Music while narration plays (default -14 dB)."`
	SourceUnderVoiceoverDB *float64 `json:"sourceUnderVoiceoverDb,omitempty" bson:"sourceUnderVoiceoverDb,omitempty" jsonschema:"minimum=-60,maximum=0,description=Clip audio while narration plays (default -18 dB)."`
	MusicUnderDialogueDB   *float64 `json:"musicUnderDialogueDb,omitempty" bson:"musicUnderDialogueDb,omitempty" jsonschema:"minimum=-40,maximum=0,description=Music under on-camera speech (default -12 dB; a track's own duckDb overrides it)."`
	Attack                 *float64 `json:"attack,omitempty" bson:"attack,omitempty" jsonschema:"minimum=0.02,maximum=2,description=Seconds the level ramps down before speech starts (default 0.25)."`
	Release                *float64 `json:"release,omitempty" bson:"release,omitempty" jsonschema:"minimum=0.02,maximum=3,description=Seconds the level ramps back up after speech (default 0.5)."`
}

// MasterLoudness is the delivery loudness target.
type MasterLoudness struct {
	TargetLUFS   *float64 `json:"targetLufs,omitempty" bson:"targetLufs,omitempty" jsonschema:"minimum=-31,maximum=-8,description=Integrated loudness target (default from output.platform: -14 LUFS for social and YouTube\\, -16 generic)."`
	TruePeakDBTP *float64 `json:"truePeakDbtp,omitempty" bson:"truePeakDbtp,omitempty" jsonschema:"minimum=-9,maximum=0,description=True-peak ceiling (default -1 dBTP)."`
	Normalize    *bool    `json:"normalize,omitempty" bson:"normalize,omitempty" jsonschema:"description=Apply one static master gain so the estimated mix meets the target (default on for cuts with a voiceover; needs loudness evidence for the sources). QA measures the real render."`
}

// Mix defaults.
const (
	DefaultMusicUnderVO  = -14.0
	DefaultSourceUnderVO = -18.0
	DefaultMusicUnderDlg = -12.0
	DefaultDuckAttack    = 0.25
	DefaultDuckRelease   = 0.5
	DefaultTruePeak      = -1.0
)

// PlatformLoudness is the integrated loudness convention per output platform.
var PlatformLoudness = map[string]float64{"shorts": -14, "tiktok": -14, "reels": -14, "youtube": -14, "generic": -16}

// AssetIDs lists every asset a spec plays: clips, audio tracks and narration
// (takes, their cleaned-up versions and a continuous recording), in order.
// Nested cuts are not assets (NestedKeys lists them; PlanSourceIDs lists the
// assets a resolved plan plays, theirs included).
func AssetIDs(s *Spec) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !IsNested(id) && !IsConversation(id) && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, c := range s.Clips {
		add(c.AssetID)
	}
	// The recordings of the conversations it plays, and their B-roll (D99).
	for _, id := range ConversationAssetIDs(s) {
		add(id)
	}
	for _, t := range s.Audio {
		add(t.AssetID)
	}
	if v := s.Voiceover; v != nil {
		for _, b := range v.Beats {
			if b.Take != nil {
				add(b.Take.AssetID)
				if b.Take.Processed != nil {
					add(b.Take.Processed.AssetID)
				}
			}
		}
		if c := v.Continuous; c != nil {
			add(c.AssetID)
			if c.Processed != nil {
				add(c.Processed.AssetID)
			}
		}
	}
	if m := s.Mix; m != nil {
		for _, e := range m.Enhanced { // the clearer copies the clips' sound plays
			add(e.Variant)
		}
		for _, c := range m.Said { // new words in a speaker's own voice (D308)
			add(c.Sound)
			add(c.Picture)
		}
	}
	return out
}
