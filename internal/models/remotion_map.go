package models

import "time"

// The project map of a Remotion source (D167): for each composition, its
// scenes (where each starts and ends, resolved while the code runs, so
// lengths computed in code are exact), what sets each scene's length, the
// words on screen with where they are written, the pictures, the sounds with
// their timing and level, the fonts and the data files that set the timing,
// with a still of each scene drawn in the render jail. The map is a JSON
// object in the media store (one per version); the version keeps a summary.

// Map states.
const (
	RemotionMapMaking = "making"
	RemotionMapDone   = "done"
	RemotionMapFailed = "failed"
)

// MapFormat names the map document's layout.
const MapFormat = "metarush.remotion-map/1"

// RemotionMapRef is a version's map: whether it is made, and a summary.
type RemotionMapRef struct {
	State        string          `bson:"state" json:"state" jsonschema:"enum=making,enum=done,enum=failed"`
	JobID        string          `bson:"jobId,omitempty" json:"jobId,omitempty"`
	Object       *RemotionObject `bson:"object,omitempty" json:"object,omitempty"`
	At           time.Time       `bson:"at" json:"at"`
	Machine      string          `bson:"machine,omitempty" json:"machine,omitempty"`
	Compositions int             `bson:"compositions" json:"compositions"`
	Scenes       int             `bson:"scenes" json:"scenes"`
	Sounds       int             `bson:"sounds" json:"sounds"`
	Seconds      float64         `bson:"seconds,omitempty" json:"seconds,omitempty"`
	Problem      string          `bson:"problem,omitempty" json:"problem,omitempty"`
	Blocked      []string        `bson:"blocked,omitempty" json:"blocked,omitempty"`
}

// RemotionMap is the map document (format metarush.remotion-map/1).
type RemotionMap struct {
	Format       string                   `json:"format"`
	Source       string                   `json:"source" jsonschema:"description=The source's short name."`
	Version      string                   `json:"version"`
	Number       int                      `json:"number"`
	At           time.Time                `json:"at"`
	Compositions []RemotionMapComposition `json:"compositions"`
	// Data are the data files the code reads (JSON): timings (word timings,
	// lengths) and other data.
	Data []RemotionMapData `json:"data"`
	// Copy are the files that hold the project's on-screen words, when the code
	// keeps them in constants (D168): the place a change of words goes, or
	// what becomes settings.
	Copy []RemotionCopyFile `json:"copy"`
	// Settings: the words are settings (D168) — changed without a new version.
	Settings bool     `json:"settings"`
	Fonts    []string `json:"fonts" jsonschema:"description=Google Fonts the code loads."`
	Problems []string `json:"problems,omitempty"`
}

// RemotionMapComposition is one composition of the map.
type RemotionMapComposition struct {
	ID               string  `json:"id"`
	Width            int     `json:"width"`
	Height           int     `json:"height"`
	FPS              float64 `json:"fps"`
	DurationInFrames int     `json:"durationInFrames"`
	Duration         float64 `json:"duration" jsonschema:"description=Seconds."`
	File             string  `json:"file" jsonschema:"description=Where it is registered."`
	Line             int     `json:"line"`
	Component        string  `json:"component,omitempty"`
	ComponentFile    string  `json:"componentFile,omitempty"`
	// Length is what sets the composition's length.
	Length RemotionMapLength  `json:"length"`
	Scenes []RemotionMapScene `json:"scenes"`
	Sounds []RemotionMapSound `json:"sounds"`
	// Data are the data files its code reads.
	Data    []string `json:"data,omitempty"`
	Problem string   `json:"problem,omitempty" jsonschema:"description=Why it couldn't be mapped (plain words)."`
}

// RemotionMapScene is one scene: a sequence of the composition (a
// TransitionSeries or Series entry, or a Sequence), or the whole composition
// when it has none.
type RemotionMapScene struct {
	N    int    `json:"n" jsonschema:"description=Its place in the composition (1\\, 2\\, 3…)."`
	Name string `json:"name" jsonschema:"description=Its name as the code gives it (or its component's)."`
	// Kind: how the code lays it out.
	Kind         string               `json:"kind" jsonschema:"enum=transition-series,enum=series,enum=sequence,enum=whole"`
	Component    string               `json:"component,omitempty"`
	File         string               `json:"file,omitempty" jsonschema:"description=The file its component is in."`
	Line         int                  `json:"line,omitempty"`
	From         float64              `json:"from" jsonschema:"description=Seconds into the composition."`
	To           float64              `json:"to"`
	FromFrame    int                  `json:"fromFrame"`
	Frames       int                  `json:"frames"`
	Placed       string               `json:"placed,omitempty" jsonschema:"description=Where the code places it (file:line of its sequence)."`
	Length       RemotionMapLength    `json:"length"`
	Words        []RemotionMapWord    `json:"words"`
	Pictures     []RemotionMapPicture `json:"pictures"`
	Sounds       []int                `json:"sounds" jsonschema:"description=The composition's sounds (their n) that start in this scene."`
	Fonts        []string             `json:"fonts"`
	Data         []string             `json:"data,omitempty" jsonschema:"description=Data files its code reads."`
	Still        *RemotionObject      `json:"still,omitempty"`
	StillAt      float64              `json:"stillAt,omitempty"`
	Parts        []RemotionMapPart    `json:"parts,omitempty" jsonschema:"description=Sequences inside it."`
	Overflowing  []string             `json:"overflowing,omitempty" jsonschema:"description=Words that spill out of their box or the frame in its still."`
	FromSettings int                  `json:"fromSettings,omitempty" jsonschema:"description=How many of its words are settings."`
}

// RemotionMapPart is a sequence inside a scene.
type RemotionMapPart struct {
	Name      string  `json:"name"`
	Component string  `json:"component,omitempty"`
	From      float64 `json:"from"`
	To        float64 `json:"to"`
}

// RemotionMapLength says what sets a length: a number written in the code
// (its file and line), or code that works it out (with the data files and
// settings it reads).
type RemotionMapLength struct {
	Frames   int     `json:"frames"`
	Seconds  float64 `json:"seconds"`
	File     string  `json:"file,omitempty"`
	Line     int     `json:"line,omitempty"`
	Code     string  `json:"code,omitempty" jsonschema:"description=The code that sets it (durationInFrames={…})."`
	Computed bool    `json:"computed,omitempty" jsonschema:"description=Worked out in code (from data or other scenes)."`
	// Overlap: seconds it overlaps the next scene (a transition).
	Overlap float64           `json:"overlap,omitempty"`
	Clues   []RemotionMapClue `json:"clues,omitempty" jsonschema:"description=Where the numbers that work it out are."`
}

// RemotionMapClue is a place in the code or its data that a computed length depends on.
type RemotionMapClue struct {
	Kind string `json:"kind" jsonschema:"enum=data,enum=settings"`
	File string `json:"file"`
	Line int    `json:"line,omitempty"`
	Text string `json:"text,omitempty"`
}

// RemotionMapWord is words seen on screen, with where they are written.
type RemotionMapWord struct {
	Text string `json:"text"`
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	// Key: the constant it is in (END.tagline); when the words are settings,
	// the setting's name.
	Key     string   `json:"key,omitempty"`
	Setting bool     `json:"setting,omitempty" jsonschema:"description=The words are a setting (changed without a new version)."`
	Data    string   `json:"data,omitempty" jsonschema:"description=The data file the words come from."`
	At      float64  `json:"at" jsonschema:"description=Seconds into the composition where it was seen."`
	Font    string   `json:"font,omitempty"`
	Flags   []string `json:"flags,omitempty" jsonschema:"description=outside (past the frame)\\, clipped (cut off by its box)\\, cut short (…)."`
	Lines   int      `json:"lines,omitempty"`
}

// RemotionMapPicture is a picture from public/ a scene shows.
type RemotionMapPicture struct {
	File string `json:"file" jsonschema:"description=Its path in public/."`
	Code string `json:"code,omitempty" jsonschema:"description=Where the code names it (file:line)."`
	Seen bool   `json:"seen,omitempty" jsonschema:"description=Seen in the scene's still."`
}

// RemotionMapSound is one sound the composition plays (an <Audio> element as
// it plays: where, from which point of the file, how loud).
type RemotionMapSound struct {
	N    int    `json:"n"`
	File string `json:"file" jsonschema:"description=Its path in public/ (empty when it isn't a project file)."`
	// From/To: seconds into the composition; Start: seconds into the file.
	From  float64 `json:"from"`
	To    float64 `json:"to"`
	Start float64 `json:"start"`
	Rate  float64 `json:"rate,omitempty" jsonschema:"description=Playback speed (1 when absent)."`
	// Volume is its level (the highest, 0 to 1 or more); Changes: it changes
	// while it plays (fades, dips under the voice).
	Volume  float64 `json:"volume"`
	Changes bool    `json:"changes,omitempty"`
	FadeIn  float64 `json:"fadeIn,omitempty" jsonschema:"description=Seconds it takes to come up at its start."`
	FadeOut float64 `json:"fadeOut,omitempty" jsonschema:"description=Seconds it takes to go down at its end."`
	// Dips: times it gets quieter in between (music under a voice); DipTo how
	// low (a fraction of Volume).
	Dips  int     `json:"dips,omitempty"`
	DipTo float64 `json:"dipTo,omitempty"`
	// Role: what it sounds like it is: music, voiceover or effect (a guess
	// from its length, name and the project's data).
	Role  string `json:"role" jsonschema:"enum=music,enum=voiceover,enum=effect"`
	Words string `json:"words,omitempty" jsonschema:"description=Its words\\, when the project's data says them."`
	Code  string `json:"code,omitempty" jsonschema:"description=Where the code plays it (file:line)."`
	Video bool   `json:"video,omitempty" jsonschema:"description=The sound of a video the composition shows."`
}

// RemotionMapData is a data file the code reads.
type RemotionMapData struct {
	File   string   `json:"file"`
	Kind   string   `json:"kind" jsonschema:"enum=timings,enum=data"`
	Bytes  int      `json:"bytes"`
	Lines  int      `json:"lines,omitempty" jsonschema:"description=Texts it gives for sound files (voiceover lines)."`
	UsedBy []string `json:"usedBy,omitempty" jsonschema:"description=Compositions whose code reads it."`
}

// RemotionCopyFile is a file that holds on-screen words in constants.
type RemotionCopyFile struct {
	File    string   `json:"file"`
	Words   int      `json:"words"`
	Consts  []string `json:"consts"`
	Wrapped bool     `json:"wrapped,omitempty" jsonschema:"description=Its words are settings already."`
}
