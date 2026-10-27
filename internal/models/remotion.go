package models

import (
	"strconv"
	"strings"
	"time"
)

// Remotion sources (D131–D138, docs/remotion-sources.md): a whole Remotion
// project kept inside a MetaRush project as content-addressed versions, read
// and rendered in the network jail, edited on MetaRush's own history and
// approved by a person per version.

// Version states: only approved versions render into cuts.
const (
	RemotionWaiting  = "waiting"  // checked, waiting for a person
	RemotionApproved = "approved" // a person approved this exact version
	RemotionRejected = "rejected" // a person turned it down
	RemotionRevoked  = "revoked"  // approval withdrawn: new renders are refused
)

// Where a source came from.
const (
	RemotionFromFolder = "folder"
	RemotionFromGitHub = "github"
	// RemotionFromFile: brought in with a file another workspace exported
	// (D219); Folder is the file's name.
	RemotionFromFile = "file"
)

// Version kinds.
const (
	RemotionKindImport = "import" // the project as it was brought in
	RemotionKindEdit   = "edit"   // a change checked on MetaRush's branch
)

// Render states.
const (
	RemotionRenderQueued    = "queued"
	RemotionRenderRendering = "rendering"
	RemotionRenderDone      = "done"
	RemotionRenderFailed    = "failed"
)

// Clip provenance (AssetSource.Platform / Method) for clips made from a source.
const (
	PlatformRemotion = "remotion"
	// SourceRemotionRender: MetaRush rendered a composition.
	SourceRemotionRender = "render"
	// SourceRemotionExisting: a video the project already had in its out/ folder.
	SourceRemotionExisting = "existing"
	// SourceRemotionSound: a sound a composition plays, brought in as a track (D170).
	SourceRemotionSound = "sound"
)

// RemotionSource is one Remotion project inside a MetaRush project.
type RemotionSource struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"-"`
	ProjectID   string `bson:"projectId" json:"projectId"`
	// Name is its short name in the project (cut sheets: remotion <name> …).
	Name        string         `bson:"name" json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$"`
	Title       string         `bson:"title" json:"title"`
	Description string         `bson:"description,omitempty" json:"description,omitempty"`
	Origin      RemotionOrigin `bson:"origin" json:"origin"`
	// Head is the newest version on MetaRush's branch; Approved the newest a
	// person approved ("" when none).
	Head           string `bson:"head" json:"head"`
	HeadNumber     int    `bson:"headNumber" json:"headNumber"`
	Approved       string `bson:"approved,omitempty" json:"approved,omitempty"`
	ApprovedNumber int    `bson:"approvedNumber,omitempty" json:"approvedNumber,omitempty"`
	Versions       int    `bson:"versions" json:"versions"`
	// Allowed is the package set people allowed: the import's, then each set a
	// person allowed for a version that changed it (D137).
	Allowed []RemotionAllowed `bson:"allowed" json:"allowed"`
	// LeftOut sums up what the import did not copy, and why.
	LeftOut []RemotionLeftOut `bson:"leftOut,omitempty" json:"leftOut,omitempty"`
	// Existing are the videos it already had in out/, brought in as footage.
	Existing []RemotionExisting `bson:"existing,omitempty" json:"existing,omitempty"`
	// Words are the on-screen words people changed as settings (D169): applied
	// to every render of a version that has word settings, without a new
	// version of the code.
	Words *RemotionWords `bson:"words,omitempty" json:"words,omitempty"`
	// WordsPending is the last change of words that was only checked (or
	// that its check stopped): what the dashboard shows next to the form.
	WordsPending *RemotionWordsChange `bson:"wordsPending,omitempty" json:"wordsPending,omitempty"`
	// Pictures are the pictures people put in place of the project's own as
	// settings (D209), like Words; PicturesPending the last change only checked.
	Pictures        *RemotionPictures       `bson:"pictures,omitempty" json:"pictures,omitempty"`
	PicturesPending *RemotionPicturesChange `bson:"picturesPending,omitempty" json:"picturesPending,omitempty"`
	// Refresh are the refresh steps a person last started (D173): the next
	// plan starts from them. Refreshes and Pushes count the runs (their numbers).
	Refresh   *RemotionRefreshSteps `bson:"refresh,omitempty" json:"refresh,omitempty"`
	Refreshes int                   `bson:"refreshes,omitempty" json:"refreshes,omitempty"`
	Pushes    int                   `bson:"pushes,omitempty" json:"pushes,omitempty"`
	Removed   bool                  `bson:"removed,omitempty" json:"removed,omitempty"`
	CreatedAt time.Time             `bson:"createdAt" json:"createdAt"`
	CreatedBy Actor                 `bson:"createdBy" json:"createdBy"`
	UpdatedAt time.Time             `bson:"updatedAt" json:"updatedAt"`
}

// RemotionOrigin is where the project was imported from. The original is
// never changed.
type RemotionOrigin struct {
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=folder,enum=github,enum=file"`
	// GitHub: the repository's address, owner/name, the branch or tag asked for.
	URL  string `bson:"url,omitempty" json:"url,omitempty"`
	Repo string `bson:"repo,omitempty" json:"repo,omitempty"`
	Ref  string `bson:"ref,omitempty" json:"ref,omitempty"`
	// Commit is the original commit (a GitHub import, or a folder that is a
	// git checkout); Dirty: the folder had changes not committed (the version
	// is the folder as it was, not the commit).
	Commit string `bson:"commit,omitempty" json:"commit,omitempty"`
	Branch string `bson:"branch,omitempty" json:"branch,omitempty"`
	Dirty  bool   `bson:"dirty,omitempty" json:"dirty,omitempty"`
	// Folder: its name, the path as given (home shortened to ~), the machine,
	// and its git remote (any password removed).
	Folder  string `bson:"folder,omitempty" json:"folder,omitempty"`
	Path    string `bson:"path,omitempty" json:"path,omitempty"`
	Machine string `bson:"machine,omitempty" json:"machine,omitempty"`
	Remote  string `bson:"remote,omitempty" json:"remote,omitempty"`
}

// Where says where it came from in a few words: "github.com/acme/teaser at
// 5318d00" or "the folder teaser on Theo's Mac".
func (o RemotionOrigin) Where() string {
	if o.Kind == RemotionFromGitHub {
		s := strings.TrimPrefix(strings.TrimPrefix(o.URL, "https://"), "http://")
		if o.Commit != "" {
			s += " at " + ShortSHA(o.Commit)
		}
		return s
	}
	if o.Kind == RemotionFromFile {
		return "the file " + o.Folder + " from another workspace"
	}
	s := "the folder " + o.Folder
	if o.Machine != "" {
		s += " on " + o.Machine
	}
	return s
}

// RemotionAllowed is one package set people allowed for a source.
type RemotionAllowed struct {
	Lock    string    `bson:"lock" json:"lock" jsonschema:"description=SHA-256 of the package lockfile."`
	Version string    `bson:"version" json:"version" jsonschema:"description=The version that brought it."`
	By      Actor     `bson:"by" json:"by"`
	At      time.Time `bson:"at" json:"at"`
	Import  bool      `bson:"import,omitempty" json:"import,omitempty" jsonschema:"description=Allowed by importing the project."`
}

// RemotionLeftOut is a group of files an import did not copy.
type RemotionLeftOut struct {
	Why      string   `bson:"why" json:"why"`
	Count    int      `bson:"count" json:"count"`
	Examples []string `bson:"examples,omitempty" json:"examples,omitempty"`
}

// RemotionExisting is a video the project already had in out/.
type RemotionExisting struct {
	Path        string  `bson:"path" json:"path"`
	AssetID     string  `bson:"assetId" json:"assetId"`
	Composition string  `bson:"composition,omitempty" json:"composition,omitempty"`
	Duration    float64 `bson:"duration,omitempty" json:"duration,omitempty"`
}

// RemotionVersion is one version of a source: a manifest of its files (each
// a content-addressed object) whose SHA-256 is the version, with what
// MetaRush read from it, its check and what people decided. One document per
// workspace and hash (_id = <workspaceId>/<hash>).
type RemotionVersion struct {
	ID          string    `bson:"_id" json:"-"`
	WorkspaceID string    `bson:"workspaceId" json:"-"`
	SourceID    string    `bson:"sourceId" json:"sourceId"`
	ProjectID   string    `bson:"projectId" json:"projectId"`
	Hash        string    `bson:"hash" json:"hash" jsonschema:"description=SHA-256 of the version's canonical manifest (its identity)."`
	Parent      string    `bson:"parent,omitempty" json:"parent,omitempty"`
	Number      int       `bson:"number" json:"number"`
	Kind        string    `bson:"kind" json:"kind" jsonschema:"enum=import,enum=edit,enum=refresh"`
	Message     string    `bson:"message,omitempty" json:"message,omitempty"`
	By          Actor     `bson:"by" json:"by"`
	At          time.Time `bson:"at" json:"at"`
	Machine     string    `bson:"machine,omitempty" json:"machine,omitempty"`
	// The files.
	Manifest    RemotionObject `bson:"manifest" json:"manifest"`
	Files       int            `bson:"files" json:"files"`
	Bytes       int64          `bson:"bytes" json:"bytes"`
	PublicFiles int            `bson:"publicFiles" json:"publicFiles"`
	PublicBytes int64          `bson:"publicBytes" json:"publicBytes"`
	// Packages: the lockfile, the Remotion version it pins and the packages.
	Lock            string                  `bson:"lock" json:"lock"`
	Remotion        string                  `bson:"remotion" json:"remotion"`
	Packages        []RemotionPackage       `bson:"packages" json:"packages"`
	PackagesChanged []RemotionPackageChange `bson:"packagesChanged,omitempty" json:"packagesChanged,omitempty"`
	PackagesAllowed bool                    `bson:"packagesAllowed" json:"packagesAllowed" jsonschema:"description=Its package set is one people allowed."`
	Fonts           []RemotionFont          `bson:"fonts,omitempty" json:"fonts,omitempty"`
	// What MetaRush read: the static read (never runs code) and the
	// compositions as the jailed browser saw them (the truth once it ran).
	Entry        string                `bson:"entry,omitempty" json:"entry,omitempty"`
	Config       []RemotionConfigNote  `bson:"config,omitempty" json:"config,omitempty"`
	Static       RemotionStatic        `bson:"static" json:"static"`
	Compositions []RemotionComposition `bson:"compositions" json:"compositions"`
	Read         *RemotionRead         `bson:"read,omitempty" json:"read,omitempty"`
	Check        *RemotionCheck        `bson:"check,omitempty" json:"check,omitempty"`
	// The project map (D167): made on demand (and after an import) by a
	// remotion-task job; the map itself is an object in the media store.
	Map *RemotionMapRef `bson:"map,omitempty" json:"map,omitempty"`
	// Words as settings (D168): the files that hold its on-screen words, and
	// — once a person approved the change that made them settings — its
	// settings (the words as written in the code, by key).
	Copy        []RemotionCopyFile    `bson:"copy,omitempty" json:"copy,omitempty"`
	WordsHelper string                `bson:"wordsHelper,omitempty" json:"wordsHelper,omitempty"`
	Settings    []RemotionWordSetting `bson:"settings,omitempty" json:"settings,omitempty"`
	// Pictures as settings (D209): the pictures its code draws (its public/
	// files named in staticFile, the picture files it imports), each a setting
	// once a person approved the change that wrapped it in picture("NAME", …);
	// PicturesLeft the references its code works out as it plays (they stay
	// code); PicturesHelper the helper's file once it has one.
	Pictures       []RemotionPicture    `bson:"pictures,omitempty" json:"pictures,omitempty"`
	PicturesLeft   []RemotionPictureUse `bson:"picturesLeft,omitempty" json:"picturesLeft,omitempty"`
	PicturesHelper string               `bson:"picturesHelper,omitempty" json:"picturesHelper,omitempty"`
	// PicturesRead: its pictures were read (versions read by builds before
	// D209 get them read once, when someone looks at its pictures).
	PicturesRead bool `bson:"picturesRead,omitempty" json:"picturesRead,omitempty"`
	// What people decided.
	State      string             `bson:"state" json:"state" jsonschema:"enum=waiting,enum=approved,enum=rejected,enum=revoked"`
	Decisions  []RemotionDecision `bson:"decisions,omitempty" json:"decisions,omitempty"`
	ApprovedBy *Actor             `bson:"approvedBy,omitempty" json:"approvedBy,omitempty"`
	ApprovedAt *time.Time         `bson:"approvedAt,omitempty" json:"approvedAt,omitempty"`
	CreatedAt  time.Time          `bson:"createdAt" json:"createdAt"`
	UpdatedAt  time.Time          `bson:"updatedAt" json:"updatedAt"`
}

// Short is the version as people write it (8 characters).
func (v *RemotionVersion) Short() string { return ShortHash(v.Hash) }

// Composition finds a composition by its ID.
func (v *RemotionVersion) Composition(id string) *RemotionComposition {
	for i := range v.Compositions {
		if v.Compositions[i].ID == id {
			return &v.Compositions[i]
		}
	}
	return nil
}

// ShortHash is a hash as people write it (8 characters).
func ShortHash(h string) string {
	if len(h) > 8 {
		return h[:8]
	}
	return h
}

// RemotionObject is a file in the media store.
type RemotionObject struct {
	Key    string `bson:"key" json:"-"`
	SHA256 string `bson:"sha256" json:"sha256"`
	Size   int64  `bson:"size" json:"size"`
	Width  int    `bson:"width,omitempty" json:"width,omitempty"`
	Height int    `bson:"height,omitempty" json:"height,omitempty"`
}

// RemotionPackage is one package a project names in package.json.
type RemotionPackage struct {
	Name    string `bson:"name" json:"name"`
	Version string `bson:"version" json:"version" jsonschema:"description=The version the lockfile installs."`
	Dev     bool   `bson:"dev,omitempty" json:"dev,omitempty"`
	// Kind: core (Remotion and React), tools (TypeScript, types, linting:
	// never part of the video) or extra (anything else the video may use).
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=core,enum=tools,enum=extra"`
	// Used: the project's code imports it.
	Used bool `bson:"used,omitempty" json:"used,omitempty"`
}

// RemotionPackageChange is a package a version adds, removes or changes.
type RemotionPackageChange struct {
	Name string `bson:"name" json:"name"`
	From string `bson:"from,omitempty" json:"from,omitempty" jsonschema:"description=Version before (empty: added)."`
	To   string `bson:"to,omitempty" json:"to,omitempty" jsonschema:"description=Version now (empty: removed)."`
}

// RemotionFont is a Google Font the project loads (@remotion/google-fonts):
// downloaded once at import and served from the bundle while rendering.
type RemotionFont struct {
	Family string             `bson:"family" json:"family"`
	Module string             `bson:"module" json:"module"`
	Files  []RemotionFontFile `bson:"files" json:"files"`
}

// RemotionFontFile is one font file (its path on fonts.gstatic.com).
type RemotionFontFile struct {
	Path   string `bson:"path" json:"path"`
	SHA256 string `bson:"sha256" json:"sha256"`
	Size   int64  `bson:"size" json:"size"`
}

// RemotionConfigNote is one setting in remotion.config.ts, read as text
// (the file is never run) and applied only when it is safe.
type RemotionConfigNote struct {
	Setting string `bson:"setting" json:"setting"`
	Value   string `bson:"value,omitempty" json:"value,omitempty"`
	Applied bool   `bson:"applied" json:"applied"`
	Note    string `bson:"note" json:"note"`
	Line    int    `bson:"line,omitempty" json:"line,omitempty"`
}

// RemotionProblem is a finding, located in the source when it can be.
type RemotionProblem struct {
	Severity string `bson:"severity" json:"severity" jsonschema:"enum=error,enum=warning"`
	Code     string `bson:"code" json:"code"`
	File     string `bson:"file,omitempty" json:"file,omitempty"`
	Line     int    `bson:"line,omitempty" json:"line,omitempty"`
	Message  string `bson:"message" json:"message"`
	Hint     string `bson:"hint,omitempty" json:"hint,omitempty"`
}

// RemotionStatic is what reading the source as text found.
type RemotionStatic struct {
	Compositions []RemotionComposition `bson:"compositions" json:"compositions"`
	// Imports lists, for each code file, the files and packages it imports
	// (the project map and the check's "what can this change affect" read it).
	Imports  []RemotionFileImports `bson:"imports,omitempty" json:"imports,omitempty"`
	Problems []RemotionProblem     `bson:"problems,omitempty" json:"problems,omitempty"`
	// StaticFiles are the public/ paths the code names in quotes.
	StaticFiles []string `bson:"staticFiles,omitempty" json:"staticFiles,omitempty"`
}

// RemotionFileImports is one code file's imports.
type RemotionFileImports struct {
	File     string   `bson:"file" json:"file"`
	Files    []string `bson:"files,omitempty" json:"files,omitempty" jsonschema:"description=Project files it imports."`
	Packages []string `bson:"packages,omitempty" json:"packages,omitempty"`
}

// RemotionComposition is one composition (or still) a project registers.
type RemotionComposition struct {
	ID               string  `bson:"id" json:"id"`
	Width            int     `bson:"width" json:"width"`
	Height           int     `bson:"height" json:"height"`
	FPS              float64 `bson:"fps" json:"fps"`
	DurationInFrames int     `bson:"durationInFrames" json:"durationInFrames"`
	Duration         float64 `bson:"duration" json:"duration" jsonschema:"description=Seconds."`
	Folder           string  `bson:"folder,omitempty" json:"folder,omitempty"`
	Still            bool    `bson:"still,omitempty" json:"still,omitempty"`
	// Computed: its length or size is computed in code (calculateMetadata or
	// an expression); only the jailed read knows it.
	Computed     bool   `bson:"computed,omitempty" json:"computed,omitempty"`
	DefaultProps string `bson:"defaultProps,omitempty" json:"defaultProps,omitempty" jsonschema:"description=Its default props (JSON)."`
	File         string `bson:"file,omitempty" json:"file,omitempty"`
	Line         int    `bson:"line,omitempty" json:"line,omitempty"`
	// Component is the file its component comes from; Files every project
	// file that component can reach through imports.
	Component string   `bson:"component,omitempty" json:"component,omitempty"`
	Files     []string `bson:"files,omitempty" json:"files,omitempty"`
	// Words are the paths into the copy files' constants its code reads
	// (END.tagline; CONNECT.sources reads every word under it, D169).
	Words []string `bson:"words,omitempty" json:"words,omitempty"`
	// Pictures are the names of the pictures its code draws (D209).
	Pictures []string `bson:"pictures,omitempty" json:"pictures,omitempty"`
	// Poster is a small picture of its middle frame (drawn in the jail).
	Poster *RemotionObject `bson:"poster,omitempty" json:"poster,omitempty"`
}

// RemotionRead is how the jailed browser read the compositions.
type RemotionRead struct {
	At      time.Time `bson:"at" json:"at"`
	Machine string    `bson:"machine,omitempty" json:"machine,omitempty"`
	OK      bool      `bson:"ok" json:"ok"`
	Problem string    `bson:"problem,omitempty" json:"problem,omitempty"`
	Detail  string    `bson:"detail,omitempty" json:"detail,omitempty" jsonschema:"description=What the renderer said when it stopped (for Details)."`
	Blocked []string  `bson:"blocked,omitempty" json:"blocked,omitempty" jsonschema:"description=Places the code tried to reach (MetaRush blocked them)."`
	Seconds float64   `bson:"seconds,omitempty" json:"seconds,omitempty"`
}

// RemotionCheck is what checking a version found (D137).
type RemotionCheck struct {
	At      time.Time `bson:"at" json:"at"`
	By      Actor     `bson:"by" json:"by"`
	Machine string    `bson:"machine,omitempty" json:"machine,omitempty"`
	OK      bool      `bson:"ok" json:"ok"`
	// Stage is where a failing check stopped: files, packages, read, types, render.
	Stage    string               `bson:"stage,omitempty" json:"stage,omitempty"`
	Summary  string               `bson:"summary" json:"summary"`
	Changes  []RemotionFileChange `bson:"changes,omitempty" json:"changes,omitempty"`
	Words    []RemotionWordChange `bson:"words,omitempty" json:"words,omitempty"`
	Problems []RemotionProblem    `bson:"problems,omitempty" json:"problems,omitempty"`
	// Affected are the compositions the change can reach; Changed those whose
	// picture changed in the low-resolution pass.
	Affected []string `bson:"affected,omitempty" json:"affected,omitempty"`
	// Compared are the affected compositions the before/after pass looked at
	// (the rest were left out by its limit or by --composition).
	Compared []string         `bson:"compared,omitempty" json:"compared,omitempty"`
	Changed  []string         `bson:"changed,omitempty" json:"changed,omitempty"`
	Moments  []RemotionMoment `bson:"moments,omitempty" json:"moments,omitempty"`
	Stills   []RemotionStill  `bson:"stills,omitempty" json:"stills,omitempty"`
	Blocked  []string         `bson:"blocked,omitempty" json:"blocked,omitempty"`
	Seconds  float64          `bson:"seconds,omitempty" json:"seconds,omitempty"`
}

// RemotionFileChange is one file a version changes against its parent.
type RemotionFileChange struct {
	Path   string `bson:"path" json:"path"`
	Change string `bson:"change" json:"change" jsonschema:"enum=added,enum=changed,enum=removed"`
	Lines  int    `bson:"lines,omitempty" json:"lines,omitempty" jsonschema:"description=Lines added or removed (text files)."`
}

// RemotionWordChange is text in quotes that changed (on-screen words, usually).
type RemotionWordChange struct {
	File   string `bson:"file" json:"file"`
	Line   int    `bson:"line" json:"line"`
	Before string `bson:"before,omitempty" json:"before,omitempty"`
	After  string `bson:"after,omitempty" json:"after,omitempty"`
}

// RemotionMoment is a stretch of a composition whose picture changed.
type RemotionMoment struct {
	Composition string  `bson:"composition" json:"composition"`
	From        float64 `bson:"from" json:"from"`
	To          float64 `bson:"to" json:"to"`
}

// RemotionStill is one moment drawn before and after a change.
type RemotionStill struct {
	Composition string          `bson:"composition" json:"composition"`
	Frame       int             `bson:"frame" json:"frame"`
	At          float64         `bson:"at" json:"at"`
	Before      *RemotionObject `bson:"before,omitempty" json:"before,omitempty"`
	After       RemotionObject  `bson:"after" json:"after"`
	// Difference is how much the two differ (0 same … 1 completely).
	Difference float64 `bson:"difference" json:"difference"`
}

// RemotionDecision is one person's decision about a version (or its packages).
type RemotionDecision struct {
	State string    `bson:"state" json:"state" jsonschema:"enum=approved,enum=rejected,enum=revoked,enum=packages"`
	By    Actor     `bson:"by" json:"by"`
	At    time.Time `bson:"at" json:"at"`
	Note  string    `bson:"note,omitempty" json:"note,omitempty"`
}

// RemotionRender is one cached render of a composition (D135): the same key
// never renders twice. _id = <workspaceId>/<key>.
type RemotionRender struct {
	ID            string `bson:"_id" json:"-"`
	WorkspaceID   string `bson:"workspaceId" json:"-"`
	Key           string `bson:"key" json:"key"`
	SourceID      string `bson:"sourceId" json:"sourceId"`
	ProjectID     string `bson:"projectId" json:"projectId"`
	Version       string `bson:"version" json:"version"`
	VersionNumber int    `bson:"versionNumber" json:"versionNumber"`
	Composition   string `bson:"composition" json:"composition"`
	Props         string `bson:"props,omitempty" json:"props,omitempty" jsonschema:"description=The props it was rendered with (canonical JSON)."`
	Frames        []int  `bson:"frames,omitempty" json:"frames,omitempty" jsonschema:"description=First and last frame when only part was rendered."`
	Settings      string `bson:"settings" json:"settings" jsonschema:"description=The render settings the key covers."`
	// Words: the word settings it was rendered with (canonical JSON of the
	// keys its composition uses that differ from the code, D169); WordsNumber
	// the change they come from.
	Words       string `bson:"words,omitempty" json:"words,omitempty"`
	WordsNumber int    `bson:"wordsNumber,omitempty" json:"wordsNumber,omitempty"`
	// Pictures: the pictures it was rendered with in place of the project's
	// own (canonical JSON of name → served file "<sha256>.<ext>", D209);
	// PictureValues those pictures (to serve them); PicturesNumber the
	// source's change they come from; OwnPictures the clip's own choices
	// (pictures given for this clip only), which win over the source's.
	Pictures       string                 `bson:"pictures,omitempty" json:"pictures,omitempty"`
	PictureValues  []RemotionPictureValue `bson:"pictureValues,omitempty" json:"pictureValues,omitempty"`
	PicturesNumber int                    `bson:"picturesNumber,omitempty" json:"picturesNumber,omitempty"`
	OwnPictures    []RemotionPictureValue `bson:"ownPictures,omitempty" json:"ownPictures,omitempty"`
	// Sound: "" the composition's own sound mixed in; "layers" the picture
	// alone, with its sounds (Sounds) for the cut's tracks (D170).
	Sound    string                   `bson:"sound,omitempty" json:"sound,omitempty" jsonschema:"enum=,enum=layers"`
	Sounds   []RemotionSoundPlacement `bson:"sounds,omitempty" json:"sounds,omitempty"`
	State    string                   `bson:"state" json:"state" jsonschema:"enum=queued,enum=rendering,enum=done,enum=failed"`
	JobID    string                   `bson:"jobId,omitempty" json:"jobId,omitempty"`
	AssetID  string                   `bson:"assetId,omitempty" json:"assetId,omitempty"`
	SHA256   string                   `bson:"sha256,omitempty" json:"sha256,omitempty"`
	Duration float64                  `bson:"duration,omitempty" json:"duration,omitempty"`
	Width    int                      `bson:"width,omitempty" json:"width,omitempty"`
	Height   int                      `bson:"height,omitempty" json:"height,omitempty"`
	FPS      float64                  `bson:"fps,omitempty" json:"fps,omitempty"`
	Error    string                   `bson:"error,omitempty" json:"error,omitempty"`
	Blocked  []string                 `bson:"blocked,omitempty" json:"blocked,omitempty"`
	Machine  string                   `bson:"machine,omitempty" json:"machine,omitempty"`
	// AddTo are cuts waiting for this clip (remotion use, approval updates).
	AddTo      []RemotionAddTo `bson:"addTo,omitempty" json:"addTo,omitempty"`
	CreatedAt  time.Time       `bson:"createdAt" json:"createdAt"`
	CreatedBy  Actor           `bson:"createdBy" json:"createdBy"`
	StartedAt  *time.Time      `bson:"startedAt,omitempty" json:"startedAt,omitempty"`
	FinishedAt *time.Time      `bson:"finishedAt,omitempty" json:"finishedAt,omitempty"`
	UpdatedAt  time.Time       `bson:"updatedAt" json:"updatedAt"`
}

// RemotionAddTo is a cut waiting for a render: add a clip, or move clips
// from an older version (Update).
type RemotionAddTo struct {
	ProjectID string   `bson:"projectId" json:"projectId"`
	Branch    string   `bson:"branch,omitempty" json:"branch,omitempty"`
	After     string   `bson:"after,omitempty" json:"after,omitempty" jsonschema:"description=The clip it goes after (empty: at the end)."`
	ClipID    string   `bson:"clipId,omitempty" json:"clipId,omitempty"`
	In        *float64 `bson:"in,omitempty" json:"in,omitempty"`
	Out       *float64 `bson:"out,omitempty" json:"out,omitempty"`
	Update    bool     `bson:"update,omitempty" json:"update,omitempty"`
	// Sound "layers": the clip goes in with its sounds as the cut's own tracks (D170).
	Sound string    `bson:"sound,omitempty" json:"sound,omitempty"`
	By    Actor     `bson:"by" json:"by"`
	At    time.Time `bson:"at" json:"at"`
}

// RemotionSoundPlacement is one sound a composition plays, as rendered (D170):
// the public/ file, where it starts in the composition (seconds), from which
// point of the file, for how long, at which speed and level (its level per
// frame when it changes, at most one value per frame).
type RemotionSoundPlacement struct {
	File    string    `bson:"file" json:"file"`
	From    float64   `bson:"from" json:"from"`
	Seconds float64   `bson:"seconds" json:"seconds"`
	Start   float64   `bson:"start" json:"start"`
	Rate    float64   `bson:"rate,omitempty" json:"rate,omitempty"`
	Volumes []float64 `bson:"volumes" json:"volumes"`
	FPS     float64   `bson:"fps" json:"fps"`
	Video   bool      `bson:"video,omitempty" json:"video,omitempty"`
}

// RemotionClipSource is the provenance of a clip made from a Remotion source
// (AssetSource.Platform "remotion").
type RemotionClipSource struct {
	SourceID      string `bson:"sourceId" json:"sourceId"`
	SourceName    string `bson:"sourceName" json:"sourceName"`
	SourceTitle   string `bson:"sourceTitle,omitempty" json:"sourceTitle,omitempty"`
	Version       string `bson:"version" json:"version"`
	VersionNumber int    `bson:"versionNumber" json:"versionNumber"`
	// Kind: render (MetaRush rendered the composition), existing (a video
	// the project already had in out/ when it was imported: Path) or sound (a
	// file from public/ a composition plays, brought in as a track, D170).
	Kind        string `bson:"kind" json:"kind" jsonschema:"enum=render,enum=existing,enum=sound"`
	Composition string `bson:"composition,omitempty" json:"composition,omitempty"`
	Props       string `bson:"props,omitempty" json:"props,omitempty"`
	Frames      []int  `bson:"frames,omitempty" json:"frames,omitempty"`
	RenderKey   string `bson:"renderKey,omitempty" json:"renderKey,omitempty"`
	// Words / WordsNumber: the word settings it was made with (D169).
	Words       string `bson:"words,omitempty" json:"words,omitempty"`
	WordsNumber int    `bson:"wordsNumber,omitempty" json:"wordsNumber,omitempty"`
	// Pictures / PicturesNumber: the pictures it was made with in place of the
	// project's own (D209); OwnPictures the clip's own (kept when it moves).
	Pictures       string                 `bson:"pictures,omitempty" json:"pictures,omitempty"`
	PicturesNumber int                    `bson:"picturesNumber,omitempty" json:"picturesNumber,omitempty"`
	OwnPictures    []RemotionPictureValue `bson:"ownPictures,omitempty" json:"ownPictures,omitempty"`
	// Sound "layers": the picture alone (its sounds are tracks of the cut, D170).
	Sound string `bson:"sound,omitempty" json:"sound,omitempty"`
	// SoundOf: for a sound file brought in as a track, the composition clip it belongs to.
	SoundOf string `bson:"soundOf,omitempty" json:"soundOf,omitempty"`
	Path    string `bson:"path,omitempty" json:"path,omitempty"`
	Origin  string `bson:"origin,omitempty" json:"origin,omitempty" jsonschema:"description=Where the project came from\\, in a few words."`
}

// IsRemotion reports a clip made from a Remotion source.
func (s *AssetSource) IsRemotion() bool {
	return s != nil && s.Platform == PlatformRemotion && s.Remotion != nil
}

// RemotionLine is the clip's provenance in one line.
func (s *AssetSource) RemotionLine() string {
	r := s.Remotion
	name := r.SourceTitle
	if name == "" {
		name = r.SourceName
	}
	if r.Kind == SourceRemotionExisting {
		return "A video the Remotion project “" + name + "” already had (" + r.Path + ")"
	}
	l := "Made from the Remotion project “" + name + "”"
	if r.Composition != "" {
		l += " · " + r.Composition
	}
	if r.VersionNumber > 0 {
		l += " · version " + strconv.Itoa(r.VersionNumber)
	}
	if r.WordsNumber > 0 {
		l += " · its words as changed (" + strconv.Itoa(r.WordsNumber) + ")"
	}
	switch {
	case len(r.OwnPictures) > 0:
		l += " · pictures of its own"
	case r.PicturesNumber > 0:
		l += " · its pictures as changed (" + strconv.Itoa(r.PicturesNumber) + ")"
	}
	if r.Sound == RemotionSoundLayers {
		l += " · its sound in layers"
	}
	return l
}

// Sound of a clip made from a composition (D170).
const (
	// RemotionSoundMixed: the composition's own sound, mixed into the clip (the default).
	RemotionSoundMixed = ""
	// RemotionSoundLayers: the picture alone; its sounds are the cut's own tracks.
	RemotionSoundLayers = "layers"
)

// RemotionWordSetting is one word setting a version's code has (D168): a
// string in a constant its copy files wrap in words("NAME", …), by key
// (END.tagline, ACTS.0), with the words as written and where.
type RemotionWordSetting struct {
	Key  string `bson:"key" json:"key"`
	Text string `bson:"text" json:"text" jsonschema:"description=The words as the code writes them."`
	File string `bson:"file" json:"file"`
	Line int    `bson:"line" json:"line"`
	Name string `bson:"name" json:"name" jsonschema:"description=The constant it is in."`
	// OnScreen: it reads like words (not a code value such as a colour or a kind).
	OnScreen bool `bson:"onScreen" json:"onScreen"`
}

// RemotionWords are the words people changed as settings (D169).
type RemotionWords struct {
	// Number counts the changes (1, 2, 3 …); every change makes new renders.
	Number int                 `bson:"number" json:"number"`
	Values []RemotionWordValue `bson:"values" json:"values" jsonschema:"description=The settings that differ from the code."`
	At     time.Time           `bson:"at" json:"at"`
	By     Actor               `bson:"by" json:"by"`
	// Changes are the latest changes, newest first (at most 50).
	Changes []RemotionWordsChange `bson:"changes,omitempty" json:"changes,omitempty"`
}

// RemotionWordValue is one setting's words.
type RemotionWordValue struct {
	Key  string `bson:"key" json:"key"`
	Text string `bson:"text" json:"text"`
}

// RemotionWordsChange is one change of settings and what checking it found.
type RemotionWordsChange struct {
	Number  int                 `bson:"number" json:"number"`
	At      time.Time           `bson:"at" json:"at"`
	By      Actor               `bson:"by" json:"by"`
	Set     []RemotionWordValue `bson:"set" json:"set" jsonschema:"description=The settings it changed (empty text: back to the code's words)."`
	Note    string              `bson:"note,omitempty" json:"note,omitempty"`
	Summary string              `bson:"summary,omitempty" json:"summary,omitempty"`
	Check   *RemotionWordsCheck `bson:"check,omitempty" json:"check,omitempty"`
}

// RemotionWordsCheck is what MetaRush found drawing a change of words in the
// render jail (D169): lengths before and after, words that spill out of their
// box, before/after stills where the words are.
type RemotionWordsCheck struct {
	OK       bool                   `bson:"ok" json:"ok"`
	Version  string                 `bson:"version" json:"version" jsonschema:"description=The version it was checked with."`
	Lengths  []RemotionLengthChange `bson:"lengths,omitempty" json:"lengths,omitempty"`
	Problems []RemotionProblem      `bson:"problems,omitempty" json:"problems,omitempty"`
	Stills   []RemotionStill        `bson:"stills,omitempty" json:"stills,omitempty"`
}

// RemotionLengthChange is a composition whose length a change of words changes.
type RemotionLengthChange struct {
	Composition string  `bson:"composition" json:"composition"`
	Before      float64 `bson:"before" json:"before"`
	After       float64 `bson:"after" json:"after"`
}
