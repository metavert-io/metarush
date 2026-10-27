package models

import "time"

// Descriptive names, info cards and fixes (D150–D153, docs/sources.md).

// Who chose a source's title (Asset.TitleBy) or a cut's (BranchHead.TitleBy).
const (
	TitleByFile     = "file"     // the file name without its extension (the default)
	TitleByMetaRush = "metarush" // named from MetaRush's first look (or a link's title, a prompt)
	TitleByPerson   = "person"   // typed by someone: never replaced
)

// Who wrote a version's note (Revision.NoteBy).
const (
	NoteByMetaRush = "metarush"
	NoteByClaude   = "claude"
	NoteByPerson   = "person"
	// NoteByAgent: another AI app wrote it, in a draft (D248).
	NoteByAgent = "agent"
)

// NameSuggestion is MetaRush's descriptive name for a source (D150).
type NameSuggestion struct {
	Title string `bson:"title" json:"title"`
	// Why says what it is based on, in plain words ("Lena and Maya talk
	// in it, a call of 32 minutes").
	Why string `bson:"why,omitempty" json:"why,omitempty"`
	// Basis lists what was used: link, people, topic, screen-text, tags,
	// prompt, date, kind.
	Basis []string  `bson:"basis,omitempty" json:"basis,omitempty"`
	At    time.Time `bson:"at" json:"at"`
}

// Kinds of source (D152): they decide which fixes a source is offered.
const (
	SourceCamera  = "camera"  // camera video
	SourceCall    = "call"    // a call or a screen recording
	SourceVoice   = "voice"   // a voice or other audio recording
	SourceMusic   = "music"   // music
	SourcePicture = "picture" // a picture or logo (a resource)
)

// Fixes (D152, D153).
const (
	FixSteady     = "steady"     // Steady the camera
	FixColour     = "colour"     // Balance colour and brightness
	FixGrain      = "grain"      // Reduce grain
	FixVolumes    = "volumes"    // Even out each person's volume
	FixVoices     = "voices"     // Clean up voices
	FixBlocks     = "blocks"     // Reduce compression blockiness
	FixLoudness   = "loudness"   // Even out loudness
	FixHum        = "hum"        // Remove hum
	FixEnds       = "ends"       // Trim silence at the ends
	FixBackground = "background" // Remove the background
	FixEdges      = "edges"      // Trim empty edges
	FixLarger     = "larger"     // Make it larger and sharper
)

// What a fix changes (FixInfo.Affects): the picture, the sound, or only where
// the source starts and stops playing (no copy is made).
const (
	AffectsPicture = "picture"
	AffectsSound   = "sound"
	AffectsTiming  = "timing"
)

// FixInfo describes one fix for people and for the code that runs it.
type FixInfo struct {
	ID string `json:"id"`
	// Label is the checkbox ("Steady the camera"); Result says what the copy
	// has ("steadier picture"); Mark is the reason it fits ("The camera
	// shakes a lot").
	Label   string   `json:"label"`
	Result  string   `json:"result"`
	Mark    string   `json:"mark"`
	Affects string   `json:"affects" jsonschema:"enum=picture,enum=sound,enum=timing"`
	Kinds   []string `json:"kinds"`
}

// FixCatalog is every fix, in the order they run on a copy (D152, D153).
var FixCatalog = []FixInfo{
	{ID: FixBlocks, Label: "Reduce compression blockiness", Result: "less blocky picture", Mark: "The picture looks blocky", Affects: AffectsPicture, Kinds: []string{SourceCall}},
	{ID: FixGrain, Label: "Reduce grain", Result: "less grain", Mark: "The picture is grainy", Affects: AffectsPicture, Kinds: []string{SourceCamera}},
	{ID: FixSteady, Label: "Steady the camera", Result: "steadier picture", Mark: "The camera shakes a lot", Affects: AffectsPicture, Kinds: []string{SourceCamera, SourceCall}},
	{ID: FixColour, Label: "Balance colour and brightness", Result: "balanced colour and brightness", Mark: "The colour or brightness is off", Affects: AffectsPicture, Kinds: []string{SourceCamera}},
	{ID: FixHum, Label: "Remove hum", Result: "no hum", Mark: "There's a hum", Affects: AffectsSound, Kinds: []string{SourceVoice}},
	{ID: FixVoices, Label: "Clean up voices", Result: "cleaned-up voices", Mark: "There's background noise", Affects: AffectsSound, Kinds: []string{SourceCall, SourceVoice}},
	{ID: FixVolumes, Label: "Even out each person's volume", Result: "everyone equally loud", Mark: "Some people are much quieter than others", Affects: AffectsSound, Kinds: []string{SourceCall}},
	{ID: FixLoudness, Label: "Even out loudness", Result: "even loudness", Mark: "The loudness jumps around or is off", Affects: AffectsSound, Kinds: []string{SourceVoice, SourceMusic}},
	{ID: FixEnds, Label: "Trim silence at the ends", Result: "silent ends left out", Mark: "There's silence at the start or end", Affects: AffectsTiming, Kinds: []string{SourceMusic}},
	{ID: FixBackground, Label: "Remove the background", Result: "background removed", Mark: "It has a plain background", Affects: AffectsPicture, Kinds: []string{SourcePicture}},
	{ID: FixEdges, Label: "Trim empty edges", Result: "empty edges trimmed", Mark: "It has empty space around it", Affects: AffectsPicture, Kinds: []string{SourcePicture}},
	{ID: FixLarger, Label: "Make it larger and sharper", Result: "larger and sharper", Mark: "It's small", Affects: AffectsPicture, Kinds: []string{SourcePicture}},
}

// Fix looks a fix up by its ID.
func Fix(id string) (FixInfo, bool) {
	for _, f := range FixCatalog {
		if f.ID == id {
			return f, true
		}
	}
	return FixInfo{}, false
}

// fixOrder is the order a kind's fixes are shown in (the catalog's is the
// order they run in). A call's webcams can shake too (D190): a call is
// steadied one stretch between the picture's switches at a time.
var fixOrder = map[string][]string{
	SourceCamera:  {FixSteady, FixColour, FixGrain},
	SourceCall:    {FixVolumes, FixVoices, FixBlocks, FixSteady},
	SourceVoice:   {FixVoices, FixLoudness, FixHum},
	SourceMusic:   {FixLoudness, FixEnds},
	SourcePicture: {FixBackground, FixEdges, FixLarger},
}

// FixesFor lists the fixes a kind of source is offered, in the order shown.
func FixesFor(kind string) []FixInfo {
	var out []FixInfo
	for _, id := range fixOrder[kind] {
		if f, ok := Fix(id); ok {
			out = append(out, f)
		}
	}
	return out
}

// RunOrder sorts fix IDs into the order they run on a copy (unknown IDs are
// dropped, duplicates kept once).
func RunOrder(ids []string) []string {
	var out []string
	for _, f := range FixCatalog {
		for _, id := range ids {
			if id == f.ID {
				out = append(out, f.ID)
				break
			}
		}
	}
	return out
}

// FixResults says what a set of fixes did, in a few words ("steadier picture,
// balanced colour and brightness").
func FixResults(ids []string) string {
	var parts []string
	for _, f := range FixCatalog {
		for _, id := range ids {
			if id == f.ID {
				parts = append(parts, f.Result)
			}
		}
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	out := ""
	for i, p := range parts {
		switch {
		case i == 0:
			out = p
		case i == len(parts)-1:
			out += " and " + p
		default:
			out += ", " + p
		}
	}
	return out
}

// SourceKindWords says a kind of source plainly ("a camera video").
func SourceKindWords(kind string) string {
	switch kind {
	case SourceCamera:
		return "camera video"
	case SourceCall:
		return "call or screen recording"
	case SourceVoice:
		return "voice or audio recording"
	case SourceMusic:
		return "music"
	case SourcePicture:
		return "picture or logo"
	}
	return ""
}

// States of making a fixed copy (SourceFixes.State, SourceCopy.State).
const (
	FixPreparing = "preparing"
	FixReady     = "ready"
	FixFailed    = "failed"
)

// SourceFixes is a source's pre-processing (D152): what kind of source it is,
// what MetaRush measured, what a person ticked and the fixed copy in use.
type SourceFixes struct {
	// Kind is the kind of source; KindBy is "person" when someone chose it
	// (otherwise MetaRush worked it out).
	Kind   string `bson:"kind,omitempty" json:"kind,omitempty" jsonschema:"enum=,enum=camera,enum=call,enum=voice,enum=music,enum=picture"`
	KindBy string `bson:"kindBy,omitempty" json:"kindBy,omitempty"`
	// Marks are MetaRush's measurements and the fixes that fit (the inspect
	// job after the first look, or `asset fixes --look`).
	Marks    []FixMark  `bson:"marks,omitempty" json:"marks,omitempty"`
	MarkedAt *time.Time `bson:"markedAt,omitempty" json:"markedAt,omitempty"`
	// MarksFor is the media the marks were measured on (its SHA-256).
	MarksFor string `bson:"marksFor,omitempty" json:"marksFor,omitempty"`
	// Ticked are the fixes a person turned on, in the order they are applied.
	Ticked []string `bson:"ticked,omitempty" json:"ticked,omitempty"`
	// Fingerprint names the ticked fixes' copy: the hash of its fix steps
	// ("" when nothing that makes a copy is ticked); State is how making it
	// goes, Error why it failed (plain).
	Fingerprint string `bson:"fingerprint,omitempty" json:"fingerprint,omitempty"`
	State       string `bson:"state,omitempty" json:"state,omitempty" jsonschema:"enum=,enum=preparing,enum=ready,enum=failed"`
	Error       string `bson:"error,omitempty" json:"error,omitempty"`
	JobID       string `bson:"jobId,omitempty" json:"jobId,omitempty"`
	// Copy is the fixed copy the source uses now (nil: the original).
	Copy *FixedCopyRef `bson:"copy,omitempty" json:"copy,omitempty"`
	// OriginalVariants keep the original's preview copy, thumbnail, sound wave
	// and steady copy while a fixed copy's are the source's variants.
	OriginalVariants map[string]Variant `bson:"originalVariants,omitempty" json:"originalVariants,omitempty"`
	// Trim is where music starts and stops ("Trim silence at the ends"): the
	// file is not cut; music added from now on plays between these seconds.
	Trim *SoundTrim `bson:"trim,omitempty" json:"trim,omitempty"`
	// From is the version of a picture the fixes were made from (resources).
	From      string     `bson:"from,omitempty" json:"from,omitempty"`
	UpdatedAt *time.Time `bson:"updatedAt,omitempty" json:"updatedAt,omitempty"`
	UpdatedBy *Actor     `bson:"updatedBy,omitempty" json:"updatedBy,omitempty"`
}

// FixMark is one measurement and whether its fix fits.
type FixMark struct {
	Fix  string `bson:"fix" json:"fix"`
	Fits bool   `bson:"fits" json:"fits"`
	// Why is the plain reason when it fits ("The camera shakes a lot").
	Why string `bson:"why,omitempty" json:"why,omitempty"`
	// Value is the measurement in Unit (px a frame, %, levels, dB, s).
	Value float64 `bson:"value" json:"value"`
	Unit  string  `bson:"unit,omitempty" json:"unit,omitempty"`
	// Measured says the measurement in words ("shake: 5.5 pixels a frame").
	Measured string `bson:"measured,omitempty" json:"measured,omitempty"`
	// Pending says why it can't be measured yet ("once the transcript is ready").
	Pending string `bson:"pending,omitempty" json:"pending,omitempty"`
}

// FixedCopyRef points a source at its fixed copy.
type FixedCopyRef struct {
	ID          string    `bson:"id" json:"id"`
	SHA256      string    `bson:"sha256" json:"sha256"`
	StorageKey  string    `bson:"storageKey" json:"storageKey"`
	Size        int64     `bson:"size" json:"size"`
	MIME        string    `bson:"mime" json:"mime"`
	Fingerprint string    `bson:"fingerprint" json:"fingerprint"`
	Fixes       []string  `bson:"fixes" json:"fixes"`
	At          time.Time `bson:"at" json:"at"`
}

// SoundTrim is where a piece of music starts and stops sounding.
type SoundTrim struct {
	Start float64 `bson:"start" json:"start" jsonschema:"description=Seconds of silence at the start."`
	End   float64 `bson:"end" json:"end" jsonschema:"description=Where the last sound ends\\, in seconds."`
	// Duration is the file's length, so the silence at the end is Duration − End.
	Duration float64 `bson:"duration" json:"duration"`
}

// SourceCopy is one fixed copy (collection source_copies, D152): made once
// per original and fix steps on any machine, never changed once ready.
type SourceCopy struct {
	// ID is <workspace>/<original SHA-256>:<fingerprint>.
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	AssetID     string `bson:"assetId,omitempty" json:"assetId,omitempty"`
	ResourceID  string `bson:"resourceId,omitempty" json:"resourceId,omitempty"`
	// SourceSHA256 is the original it was made from.
	SourceSHA256 string `bson:"sourceSha256" json:"sourceSha256"`
	// Fingerprint is the hash of the ticked fixes that make it (fixes.Fingerprint);
	// FixSteps are how it is made, step by step.
	Fingerprint string   `bson:"fingerprint" json:"fingerprint"`
	FixSteps    FixSteps `bson:"fixSteps" json:"fixSteps"`
	State       string   `bson:"state" json:"state" jsonschema:"enum=preparing,enum=ready,enum=failed"`
	Error       string   `bson:"error,omitempty" json:"error,omitempty"`
	// The copy itself (when ready).
	SHA256     string             `bson:"sha256,omitempty" json:"sha256,omitempty"`
	StorageKey string             `bson:"storageKey,omitempty" json:"storageKey,omitempty"`
	Size       int64              `bson:"size,omitempty" json:"size,omitempty"`
	MIME       string             `bson:"mime,omitempty" json:"mime,omitempty"`
	Media      *MediaInfo         `bson:"media,omitempty" json:"media,omitempty"`
	Variants   map[string]Variant `bson:"variants,omitempty" json:"variants,omitempty"`
	// Measures are the same measurements before and after, per fix.
	Measures []FixMeasure `bson:"measures,omitempty" json:"measures,omitempty"`
	// Notes say anything a person should know (a basic method was used).
	Notes     []string  `bson:"notes,omitempty" json:"notes,omitempty"`
	JobID     string    `bson:"jobId,omitempty" json:"jobId,omitempty"`
	MadeOn    string    `bson:"madeOn,omitempty" json:"madeOn,omitempty" jsonschema:"description=The installation that made it."`
	Took      float64   `bson:"took,omitempty" json:"took,omitempty" jsonschema:"description=Seconds it took."`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	CreatedBy Actor     `bson:"createdBy" json:"createdBy"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

// FixSteps are how a fixed copy is made: the fixes in the order they run.
type FixSteps struct {
	Version int       `bson:"version" json:"version"`
	Kind    string    `bson:"kind" json:"kind"`
	Steps   []FixStep `bson:"steps" json:"steps"`
	// Take makes a fresh copy of the same fixes (made again on request).
	Take int `bson:"take,omitempty" json:"take,omitempty"`
}

// FixStep is one of a fixed copy's fix steps: the engine that ran and its
// settings (from the measurements), with the tool and its version.
type FixStep struct {
	Fix    string            `bson:"fix" json:"fix"`
	Engine string            `bson:"engine,omitempty" json:"engine,omitempty"`
	Params map[string]string `bson:"params,omitempty" json:"params,omitempty"`
	Tool   string            `bson:"tool,omitempty" json:"tool,omitempty"`
}

// FixMeasure is a fix's measurement before and after.
type FixMeasure struct {
	Fix    string  `bson:"fix" json:"fix"`
	Unit   string  `bson:"unit,omitempty" json:"unit,omitempty"`
	Before float64 `bson:"before" json:"before"`
	After  float64 `bson:"after" json:"after"`
	// Words says it plainly ("shake: 5.5 → 0.2 pixels a frame").
	Words string `bson:"words,omitempty" json:"words,omitempty"`
}

// CurrentMedia is the file a source uses now: its fixed copy, else the
// original. key, sha and size locate it in the media store.
func (a *Asset) CurrentMedia() (key, sha string, size int64) {
	if a.Fixes != nil && a.Fixes.Copy != nil && a.Fixes.Copy.SHA256 != "" {
		return a.Fixes.Copy.StorageKey, a.Fixes.Copy.SHA256, a.Fixes.Copy.Size
	}
	return a.StorageKey, a.SHA256, a.Size
}

// MediaSHA is the analysis key of the media a source uses now: the fixed
// copy's SHA-256, or "" for the original (Insight.Media).
func (a *Asset) MediaSHA() string {
	if a.Fixes != nil && a.Fixes.Copy != nil {
		return a.Fixes.Copy.SHA256
	}
	return ""
}

// Fixed reports whether the source uses a fixed copy now.
func (a *Asset) Fixed() bool { return a.Fixes != nil && a.Fixes.Copy != nil }

// SourceKind is the kind of source when known ("" otherwise).
func (a *Asset) SourceKind() string {
	if a.Fixes != nil {
		return a.Fixes.Kind
	}
	return ""
}
