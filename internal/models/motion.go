package models

import "time"

// Motion graphic states (D68). Only approved components render.
const (
	MotionWaiting  = "waiting"  // checked, waiting for a person to look at it
	MotionApproved = "approved" // a person approved this exact version
	MotionRejected = "rejected" // a person turned this version down
	MotionRevoked  = "revoked"  // approval was withdrawn: new renders are refused
)

// MotionComponent is one version of a project motion graphic: a Remotion
// component identified by the SHA-256 of its canonical source archive. The
// archive, contact sheet, test frames and preview video live in the media
// store; this document holds what MetaRush learned when it checked the
// component and what people decided about it. One document per workspace and
// hash (_id = <workspaceId>/<hash>).
type MotionComponent struct {
	ID          string `bson:"_id" json:"-"`
	WorkspaceID string `bson:"workspaceId" json:"-"`
	Hash        string `bson:"hash" json:"hash" jsonschema:"description=SHA-256 of the canonical source archive (the component's identity)."`
	Name        string `bson:"name" json:"name" jsonschema:"description=The folder name it was last checked as (motion/<name>)."`
	ProjectID   string `bson:"projectId,omitempty" json:"projectId,omitempty" jsonschema:"description=The project folder it was checked in\\, when there was one."`
	// Kind: a graphic ("", the default) or a shared library ("library", D171:
	// a folder with library.json that graphics import as @metarush/lib/<name>,
	// approved once per version for every graphic that uses it).
	Kind string `bson:"kind,omitempty" json:"kind,omitempty" jsonschema:"enum=,enum=library"`
	// Libraries are the shared libraries a graphic imports; CheckedWith the
	// versions its check drew it with. A cut pins the versions it renders with.
	Libraries   []string           `bson:"libraries,omitempty" json:"libraries,omitempty"`
	CheckedWith []MotionLibraryPin `bson:"checkedWith,omitempty" json:"checkedWith,omitempty"`
	// UsedBy: a library's check drew these graphics with it (their names).
	UsedBy []string `bson:"usedBy,omitempty" json:"usedBy,omitempty"`
	// What the component says about itself (meta) and its parameters (zod,
	// converted to JSON Schema at check time).
	Summary     string  `bson:"summary,omitempty" json:"summary,omitempty"`
	Region      string  `bson:"region" json:"region" jsonschema:"enum=lower,enum=center,enum=full"`
	MinDuration float64 `bson:"minDuration" json:"minDuration"`
	MaxDuration float64 `bson:"maxDuration" json:"maxDuration"`
	// Persistent: made to stay on screen for a section or the whole video
	// (meta.persistent, D163): no time limit.
	Persistent  bool      `bson:"persistent,omitempty" json:"persistent,omitempty"`
	CheckAt     []float64 `bson:"checkAt,omitempty" json:"checkAt,omitempty" jsonschema:"description=Extra moments (seconds from its start) the check takes frames at."`
	SchemaJSON  string    `bson:"schemaJson" json:"-"` // JSON Schema of the parameters (kept as text: its $-keys are not BSON field names)
	PreviewJSON string    `bson:"previewJson,omitempty" json:"-"`
	// What the check found.
	Files    []MotionFile    `bson:"files" json:"files"`
	Imports  []MotionImport  `bson:"imports" json:"imports"`
	Warnings []MotionProblem `bson:"warnings,omitempty" json:"warnings,omitempty"`
	// Resources are the resources its code draws through useResource (D107):
	// by name (useResource("logo"): a cut that uses the component pins them)
	// or through a parameter marked as a resource (useResource(params.image)).
	Resources []MotionResourceRef `bson:"resources,omitempty" json:"resources,omitempty"`
	Archive   MotionObject        `bson:"archive" json:"archive"`
	Sheet     *MotionObject       `bson:"sheet,omitempty" json:"sheet,omitempty" jsonschema:"description=The contact sheet of the test frames."`
	Frames    []MotionFrame       `bson:"frames,omitempty" json:"frames,omitempty" jsonschema:"description=The test frames of every shape it was checked in (the first shape's first)."`
	Video     *MotionObject       `bson:"video,omitempty" json:"video,omitempty" jsonschema:"description=A short preview render (MP4) in the first shape."`
	// Shapes are the video shapes it was checked in (D164): the cuts it is for.
	// Absent for versions checked before (one shape: Check.Width × Check.Height).
	Shapes []MotionShape   `bson:"shapes,omitempty" json:"shapes,omitempty"`
	Check  MotionCheckInfo `bson:"check" json:"check"`
	// What people decided.
	State      string           `bson:"state" json:"state" jsonschema:"enum=waiting,enum=approved,enum=rejected,enum=revoked"`
	Decisions  []MotionDecision `bson:"decisions,omitempty" json:"decisions,omitempty"`
	ApprovedBy *Actor           `bson:"approvedBy,omitempty" json:"approvedBy,omitempty"`
	ApprovedAt *time.Time       `bson:"approvedAt,omitempty" json:"approvedAt,omitempty"`
	CreatedAt  time.Time        `bson:"createdAt" json:"createdAt"`
	UpdatedAt  time.Time        `bson:"updatedAt" json:"updatedAt"`
}

// MotionKindLibrary is a shared library of motion graphics (D171).
const MotionKindLibrary = "library"

// MotionLibraryPin is one shared library at one version.
type MotionLibraryPin struct {
	Name string `bson:"name" json:"name"`
	Hash string `bson:"hash" json:"hash"`
}

// MotionResourceRef is one place a component's code draws a resource.
type MotionResourceRef struct {
	// Name is the resource's short name ("logo", "deck#3") when the code names
	// it; Param the parameter that names it otherwise (params.image).
	Name  string `bson:"name,omitempty" json:"name,omitempty"`
	Param string `bson:"param,omitempty" json:"param,omitempty"`
	File  string `bson:"file" json:"file"`
	Line  int    `bson:"line" json:"line"`
	// Font: a font drawn with useFont("name", …) (D171), not a picture.
	Font bool `bson:"font,omitempty" json:"font,omitempty"`
}

// ResourceNames are the names a component's code uses itself (pictures and fonts).
func (c *MotionComponent) ResourceNames() []string {
	var out []string
	seen := map[string]bool{}
	for _, r := range c.Resources {
		if r.Name != "" && !seen[r.Name] {
			seen[r.Name] = true
			out = append(out, r.Name)
		}
	}
	return out
}

// PictureAndFontNames splits the names its code uses into pictures
// (useResource) and fonts (useFont, D171).
func (c *MotionComponent) PictureAndFontNames() (pictures, fonts []string) {
	seen := map[string]bool{}
	for _, r := range c.Resources {
		if r.Name == "" || seen[r.Name] {
			continue
		}
		seen[r.Name] = true
		if r.Font {
			fonts = append(fonts, r.Name)
		} else {
			pictures = append(pictures, r.Name)
		}
	}
	return pictures, fonts
}

// MotionFile is one source file of a component.
type MotionFile struct {
	Path  string `bson:"path" json:"path"`
	Bytes int    `bson:"bytes" json:"bytes"`
	Lines int    `bson:"lines" json:"lines"`
}

// MotionImport is one import the scan found in the parsed source.
type MotionImport struct {
	Specifier string `bson:"specifier" json:"specifier"`
	File      string `bson:"file" json:"file"`
	Line      int    `bson:"line" json:"line"`
	Kind      string `bson:"kind" json:"kind" jsonschema:"enum=remotion,enum=react,enum=package,enum=metarush,enum=relative,description=remotion and react: the renderer itself; package: one of the curated packages MetaRush ships; metarush: MetaRush's theme and layout helpers; relative: a file in the component's own folder."`
}

// MotionProblem is a finding of the check, located in the source when it can be.
type MotionProblem struct {
	Severity string `bson:"severity" json:"severity" jsonschema:"enum=error,enum=warning"`
	Code     string `bson:"code" json:"code"`
	File     string `bson:"file,omitempty" json:"file,omitempty"`
	Line     int    `bson:"line,omitempty" json:"line,omitempty"`
	Col      int    `bson:"col,omitempty" json:"col,omitempty"`
	Message  string `bson:"message" json:"message"`
	Hint     string `bson:"hint,omitempty" json:"hint,omitempty"`
}

// MotionObject is a file in the media store.
type MotionObject struct {
	Key    string `bson:"key" json:"-"`
	SHA256 string `bson:"sha256" json:"sha256"`
	Size   int64  `bson:"size" json:"size"`
	Width  int    `bson:"width,omitempty" json:"width,omitempty"`
	Height int    `bson:"height,omitempty" json:"height,omitempty"`
}

// MotionFrame is one test frame.
type MotionFrame struct {
	Label  string       `bson:"label" json:"label" jsonschema:"description=What the frame shows (start\\, middle\\, end\\, a moment meta marks)."`
	At     float64      `bson:"at" json:"at" jsonschema:"description=Seconds from the graphic's start."`
	Object MotionObject `bson:"object" json:"object"`
	// Shape is the shape it was drawn in ("widescreen"; "" for a version
	// checked in one shape before D164).
	Shape string `bson:"shape,omitempty" json:"shape,omitempty"`
}

// MotionShape is one video shape a graphic was checked in (D164).
type MotionShape struct {
	Label    string        `bson:"label" json:"label" jsonschema:"description=widescreen\\, vertical or square."`
	Width    int           `bson:"width" json:"width"`
	Height   int           `bson:"height" json:"height"`
	Platform string        `bson:"platform,omitempty" json:"platform,omitempty"`
	Video    *MotionObject `bson:"video,omitempty" json:"video,omitempty" jsonschema:"description=Its preview in this shape."`
	// For is what it is for, in words ("fernbrook main").
	For string `bson:"for,omitempty" json:"for,omitempty"`
}

// MotionCheckInfo records how the component was checked.
type MotionCheckInfo struct {
	At          time.Time `bson:"at" json:"at"`
	By          Actor     `bson:"by" json:"by"`
	Machine     string    `bson:"machine,omitempty" json:"machine,omitempty"`
	Renderer    string    `bson:"renderer,omitempty" json:"renderer,omitempty" jsonschema:"description=The Remotion version that drew the test frames."`
	Width       int       `bson:"width" json:"width"`
	Height      int       `bson:"height" json:"height"`
	FPS         int       `bson:"fps" json:"fps"`
	Duration    float64   `bson:"duration" json:"duration" jsonschema:"description=Seconds the test renders ran the graphic for."`
	BundleBytes int       `bson:"bundleBytes" json:"bundleBytes"`
	Blocked     []string  `bson:"blocked,omitempty" json:"blocked,omitempty" jsonschema:"description=Places the component tried to reach while it was checked (MetaRush blocked them)."`
	SlowestMs   int       `bson:"slowestMs,omitempty" json:"slowestMs,omitempty" jsonschema:"description=The slowest test frame\\, in milliseconds."`
}

// MotionDecision is one person's decision about a version.
type MotionDecision struct {
	State string    `bson:"state" json:"state" jsonschema:"enum=approved,enum=rejected,enum=revoked"`
	By    Actor     `bson:"by" json:"by"`
	At    time.Time `bson:"at" json:"at"`
	Note  string    `bson:"note,omitempty" json:"note,omitempty"`
}
