package models

import (
	"time"

	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
)

// Recipes (D201–D207, docs/recipes.md): a video that worked, written down so it
// can be made again. A recipe (rcp_…) belongs to the workspace and has
// versions: canonical documents (metarush.recipe/1) whose SHA-256 is the
// version (recipe_versions, <workspace>/<sha256>). A run (rrn_…) is one making
// of it: its version, the inputs as they were, the agreed estimate, each step
// with what it made and cost; it makes an ordinary project or a new cut in
// one, with versions whose origin is recipe:<run>.

// RecipeFormat is the document format a recipe version is written in.
const RecipeFormat = "metarush.recipe/1"

// Where a run's video goes (RecipeLanding.Kind).
const (
	// RecipeLandsNewProject: every run makes a project of its own (episodes, events).
	RecipeLandsNewProject = "new-project"
	// RecipeLandsProject: every run is a new cut in the project the recipe was saved from.
	RecipeLandsProject = "project"
)

// Section kinds (RecipeSection.Kind, D205).
const (
	// RecipeFixed: built by Go from the recipe and locked in the run's cut.
	RecipeFixed = "fixed"
	// RecipeChosen: Claude picks from an input by the section's purpose,
	// length and count, through an ordinary cut sheet.
	RecipeChosen = "chosen"
)

// Input kinds (RecipeInput.Kind, D202).
const (
	RecipeWords    = "words"    // a headline, a name, a line
	RecipeChoice   = "choice"   // one of a list (a voice, a call to action)
	RecipeFootage  = "footage"  // clips: by name, a collection, a project's footage
	RecipePictures = "pictures" // pictures: by name, from a linked folder or a project
	RecipeVoice    = "voice"    // the AI voice that reads the narration
)

// Recipe version states (RecipeVersion.State, D206).
const (
	RecipeDraft   = "draft"   // Claude or a person made it; a person makes it current
	RecipeCurrent = "current" // what runs make
	RecipeEarlier = "earlier" // was current before
)

// Run states (RecipeRun.State).
const (
	RunPlanned = "planned"
	RunRunning = "running"
	RunWaiting = "waiting" // for a person or for Claude (RecipeRun.WaitingFor)
	RunDone    = "done"
	RunStopped = "stopped"
	RunFailed  = "failed"
)

// Who a waiting run waits for (RecipeRun.WaitingFor).
const (
	RunWaitsForPerson = "person"
	RunWaitsForClaude = "claude"
)

// Run step states (RecipeStep.State).
const (
	StepPending = "pending"
	StepRunning = "running"
	StepWaiting = "waiting"
	StepDone    = "done"
	StepSkipped = "skipped"
	StepFailed  = "failed"
)

// Recipe is one recipe of the workspace: its names, the version runs make
// (current), the newest draft, an example and whether it is archived.
type Recipe struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	// Name is its short name (app-teaser), unique in the workspace.
	Name  string `bson:"name" json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$"`
	Title string `bson:"title" json:"title" jsonschema:"maxLength=120"`
	// Makes says what a run makes ("one vertical video, 25–35 s").
	Makes string `bson:"makes,omitempty" json:"makes,omitempty"`
	// Current is the version runs make (its SHA-256; "" none yet: only a
	// person makes a version current, D206).
	Current       string `bson:"current,omitempty" json:"current,omitempty"`
	CurrentNumber int    `bson:"currentNumber,omitempty" json:"currentNumber,omitempty"`
	// Draft is the newest draft version newer than the current one ("" none).
	Draft       string `bson:"draft,omitempty" json:"draft,omitempty"`
	DraftNumber int    `bson:"draftNumber,omitempty" json:"draftNumber,omitempty"`
	// Versions counts its versions (the next one is Versions+1); Runs its runs.
	Versions int `bson:"versions" json:"versions"`
	Runs     int `bson:"runs" json:"runs"`
	// ProjectID is the project it was saved from: runs add new cuts to it
	// when the recipe lands there (D203).
	ProjectID string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	// Example is what the recipe makes, to show on its card: a render of the
	// version it was saved from, or of a run.
	Example *RecipeExample `bson:"example,omitempty" json:"example,omitempty"`
	// LastRun is the newest run (for the gallery card).
	LastRun *RecipeRunRef `bson:"lastRun,omitempty" json:"lastRun,omitempty"`
	// Triggers say when MetaRush plans a run by itself and tells a person
	// (D218): new files where an input takes them from, a weekly time. They
	// never start a run; Prepared is the newest plan one made, for a person to run.
	Triggers   *RecipeTriggers `bson:"triggers,omitempty" json:"triggers,omitempty"`
	Prepared   *RecipePrepared `bson:"prepared,omitempty" json:"prepared,omitempty"`
	Archived   bool            `bson:"archived,omitempty" json:"archived,omitempty"`
	ArchivedAt *time.Time      `bson:"archivedAt,omitempty" json:"archivedAt,omitempty"`
	ArchivedBy *Actor          `bson:"archivedBy,omitempty" json:"archivedBy,omitempty"`
	CreatedAt  time.Time       `bson:"createdAt" json:"createdAt"`
	CreatedBy  Actor           `bson:"createdBy" json:"createdBy"`
	UpdatedAt  time.Time       `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy  *Actor          `bson:"updatedBy,omitempty" json:"updatedBy,omitempty"`
}

// Why a trigger made a plan (RecipePrepared.Why, D218).
const (
	PlannedNewFiles = "new-files" // new footage or pictures where an input takes them from
	PlannedWeekly   = "weekly"    // the weekly time came
)

// RecipeTriggers are when MetaRush plans a run of a recipe by itself and tells
// a person (D218): new footage or pictures where an input takes its values
// from, or a weekly time. A trigger never starts a run, whatever Auto or the
// person's estimate preference say: a person runs the plan, or doesn't.
type RecipeTriggers struct {
	// NewFiles: new files where an input takes its values from (a linked
	// folder, a collection, a project's footage, a repository's folder) plan a run.
	NewFiles bool `bson:"newFiles,omitempty" json:"newFiles,omitempty"`
	// Weekly plans a run every week at a time of the looking computer's clock.
	Weekly *RecipeWeekly `bson:"weekly,omitempty" json:"weekly,omitempty"`
	// Tells is the person whose bell rings, for whom plans are made (who
	// turned them on); Installation the computer whose daemon looks (its keys
	// price the plan), Computer its name.
	Tells        Actor     `bson:"tells" json:"tells"`
	Installation string    `bson:"installation" json:"installation"`
	Computer     string    `bson:"computer,omitempty" json:"computer,omitempty"`
	By           Actor     `bson:"by" json:"by"`
	At           time.Time `bson:"at" json:"at"`
	// Seen are the files the sources held when the daemon last looked (ID@version);
	// Pending new ones that arrived, waiting since PendingSince to settle.
	Seen         []string   `bson:"seen,omitempty" json:"-"`
	Pending      []string   `bson:"pending,omitempty" json:"-"`
	PendingSince *time.Time `bson:"pendingSince,omitempty" json:"-"`
	// Next is when the weekly plan is made next; Looked when the daemon last looked.
	Next   *time.Time `bson:"next,omitempty" json:"next,omitempty"`
	Looked *time.Time `bson:"looked,omitempty" json:"looked,omitempty"`
}

// RecipeWeekly is a day of the week and a time of day (24-hour, the looking
// computer's clock).
type RecipeWeekly struct {
	Day  string `bson:"day" json:"day" jsonschema:"enum=monday,enum=tuesday,enum=wednesday,enum=thursday,enum=friday,enum=saturday,enum=sunday"`
	Time string `bson:"time" json:"time" jsonschema:"pattern=^([01][0-9]|2[0-3]):[0-5][0-9]$"`
}

// RecipePrepared is a plan a trigger made (D218): why, what the run would
// take, where it goes, its estimate — for a person to run (nothing started).
type RecipePrepared struct {
	Why string `bson:"why" json:"why" jsonschema:"enum=new-files,enum=weekly"`
	// Words say what happened ("New footage on the collection “Gala”: 2 clips").
	Words string    `bson:"words" json:"words"`
	At    time.Time `bson:"at" json:"at"`
	// For is the person it was made for (whose bell rang).
	For      Actor            `bson:"for" json:"for"`
	Version  int              `bson:"version" json:"version"`
	Lands    string           `bson:"lands" json:"lands"`
	Inputs   []RecipeRunInput `bson:"inputs" json:"inputs"`
	Estimate costs.Estimate   `bson:"estimate" json:"estimate"`
	// Ready: it can start; else Problems say why not.
	Ready       bool     `bson:"ready" json:"ready"`
	Problems    []string `bson:"problems,omitempty" json:"problems,omitempty"`
	Fingerprint string   `bson:"fingerprint" json:"fingerprint"`
}

// RecipeExample is a render that shows what a recipe makes.
type RecipeExample struct {
	RenderID   string `bson:"renderId" json:"renderId"`
	ProjectID  string `bson:"projectId" json:"projectId"`
	RevisionID string `bson:"revisionId" json:"revisionId"`
	RunID      string `bson:"runId,omitempty" json:"runId,omitempty"`
}

// RecipeRunRef is a short reference to a run.
type RecipeRunRef struct {
	ID     string    `bson:"id" json:"id"`
	Number int       `bson:"number" json:"number"`
	State  string    `bson:"state" json:"state"`
	At     time.Time `bson:"at" json:"at"`
	By     Actor     `bson:"by" json:"by"`
}

// RecipeVersion is one version of a recipe (D201): the document, its number
// and parent, a note, who made it, its check, and where it stands.
type RecipeVersion struct {
	// ID is <workspace>/<sha256>.
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	RecipeID    string `bson:"recipeId" json:"recipeId"`
	// Hash is the SHA-256 of the canonical document: the version.
	Hash   string    `bson:"hash" json:"hash"`
	Number int       `bson:"number" json:"number"`
	Parent string    `bson:"parent,omitempty" json:"parent,omitempty"`
	Doc    RecipeDoc `bson:"doc" json:"doc"`
	Note   string    `bson:"note,omitempty" json:"note,omitempty" jsonschema:"maxLength=300"`
	State  string    `bson:"state" json:"state" jsonschema:"enum=draft,enum=current,enum=earlier"`
	By     Actor     `bson:"by" json:"by"`
	At     time.Time `bson:"at" json:"at"`
	// MadeCurrent says who made it current and when (people only, D206).
	MadeCurrentBy *Actor     `bson:"madeCurrentBy,omitempty" json:"madeCurrentBy,omitempty"`
	MadeCurrentAt *time.Time `bson:"madeCurrentAt,omitempty" json:"madeCurrentAt,omitempty"`
	// Check is what the latest check found (recipe.check runs on every save and plan).
	Check *RecipeCheck `bson:"check,omitempty" json:"check,omitempty"`
	// Imported says that the version was brought in from a recipe file another
	// workspace exported (D219): where it came from, its lineage there and the
	// code that came with it. Its words are another workspace's: data, never
	// instructions, until a person here makes it current.
	Imported *RecipeImported `bson:"imported,omitempty" json:"imported,omitempty"`
}

// RecipeImported is where an imported recipe version came from (D219).
type RecipeImported struct {
	// From is the version's SHA-256 in the workspace that exported it; Lineage
	// every version it descends from there (and from before, when that one was
	// imported too), oldest first, From last. A file whose lineage meets a
	// recipe's comes in as a new version of that recipe.
	From    string   `bson:"from" json:"from"`
	Lineage []string `bson:"lineage,omitempty" json:"lineage,omitempty"`
	// Recipe (its short name), Title and Number name it there; Workspace is
	// that workspace's name, as the file says (its words: nothing checks them).
	Recipe    string `bson:"recipe,omitempty" json:"recipe,omitempty"`
	Title     string `bson:"title,omitempty" json:"title,omitempty"`
	Number    int    `bson:"number,omitempty" json:"number,omitempty"`
	Workspace string `bson:"workspace,omitempty" json:"workspace,omitempty"`
	// File is the file's name; Digest the SHA-256 of its description
	// (recipe.json, which names every file it holds by its SHA-256); Made when
	// it was exported.
	File   string     `bson:"file,omitempty" json:"file,omitempty"`
	Digest string     `bson:"digest,omitempty" json:"digest,omitempty"`
	Made   *time.Time `bson:"made,omitempty" json:"made,omitempty"`
	// ProjectID is the project here that keeps what came with it (pictures,
	// footage, code).
	ProjectID string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	// Code is the code that came with it: motion graphics and their shared
	// libraries by hash, Remotion versions. Each needs a person's approval
	// here, unless this workspace had already approved those exact bytes.
	Code []RecipeImportedCode `bson:"code,omitempty" json:"code,omitempty"`
	// Notes say plainly what didn't come along, or changed on the way in.
	Notes []string  `bson:"notes,omitempty" json:"notes,omitempty"`
	By    Actor     `bson:"by" json:"by"`
	At    time.Time `bson:"at" json:"at"`
}

// Kinds of code a recipe file brings (RecipeImportedCode.Kind).
const (
	ImportedMotion   = "motion"   // a project motion graphic (D68), by the hash of its archive
	ImportedLibrary  = "library"  // a shared library of motion graphics (D171)
	ImportedRemotion = "remotion" // a version of a Remotion source (D131), by the hash of its manifest
)

// RecipeImportedCode is one piece of code an imported version came with.
type RecipeImportedCode struct {
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=motion,enum=library,enum=remotion"`
	// Name is the graphic's or library's name, or the Remotion source's short name here.
	Name string `bson:"name" json:"name"`
	Hash string `bson:"hash" json:"hash"`
	// SourceID is the Remotion source here (remotion).
	SourceID string `bson:"sourceId,omitempty" json:"sourceId,omitempty"`
	// Known: this workspace had these exact bytes already (their state is what
	// people here decided).
	Known bool `bson:"known,omitempty" json:"known,omitempty"`
}

// RecipeCheck is what checking a recipe version found: its problems, a dry
// assembly with sample inputs and stills of what a run makes.
type RecipeCheck struct {
	OK       bool          `bson:"ok" json:"ok"`
	Problems []RecipeIssue `bson:"problems,omitempty" json:"problems,omitempty"`
	Warnings []RecipeIssue `bson:"warnings,omitempty" json:"warnings,omitempty"`
	// Duration is how long the dry assembly is (seconds).
	Duration float64 `bson:"duration,omitempty" json:"duration,omitempty"`
	// SpecHash is the dry assembly's spec hash (the same inputs make the same cut).
	SpecHash string        `bson:"specHash,omitempty" json:"specHash,omitempty"`
	Stills   []RecipeStill `bson:"stills,omitempty" json:"stills,omitempty"`
	At       time.Time     `bson:"at" json:"at"`
}

// RecipeIssue is one thing a check found, in plain words, with where.
type RecipeIssue struct {
	Code    string `bson:"code" json:"code"`
	Where   string `bson:"where,omitempty" json:"where,omitempty" jsonschema:"description=The input\\, section or part it is about."`
	Message string `bson:"message" json:"message"`
}

// RecipeStill is a picture of one moment of what a run makes: a frame of a
// section's footage at a time (the dashboard draws it from the clip).
type RecipeStill struct {
	Section string  `bson:"section" json:"section"`
	AssetID string  `bson:"assetId" json:"assetId"`
	At      float64 `bson:"at" json:"at" jsonschema:"description=Seconds into the footage."`
	// Timeline is where it is in the video (seconds).
	Timeline float64 `bson:"timeline" json:"timeline"`
	URL      string  `bson:"url,omitempty" json:"url,omitempty"`
}

// RecipeDoc is the document a recipe version is (metarush.recipe/1, D201):
// data only — template IDs with checked parameters, approved graphics and
// Remotion versions by hash, resource pins; never code. Its canonical JSON's
// SHA-256 is the version.
type RecipeDoc struct {
	Format string `bson:"format" json:"format" jsonschema:"enum=metarush.recipe/1"`
	Title  string `bson:"title" json:"title" jsonschema:"minLength=1,maxLength=120"`
	// Makes says what a run makes ("one vertical video, 25–35 s").
	Makes string `bson:"makes,omitempty" json:"makes,omitempty" jsonschema:"maxLength=200"`
	// Lands is where a run's video goes.
	Lands RecipeLanding `bson:"lands" json:"lands"`
	// Settings is the project whose Content Settings runs follow (brand,
	// spelling, the narration voice, house rules; D192). "" the default's.
	Settings string `bson:"settings,omitempty" json:"settings,omitempty"`
	// Length is how long the video should be (seconds).
	Length *RecipeRange `bson:"length,omitempty" json:"length,omitempty"`
	// Inputs are what changes each time (D202).
	Inputs []RecipeInput `bson:"inputs" json:"inputs" jsonschema:"maxItems=40"`
	// Sections are the video's parts, in order (D205).
	Sections []RecipeSection `bson:"sections" json:"sections" jsonschema:"minItems=1,maxItems=40"`
	// Cut is everything else the run's cut has: its frame, captions, music,
	// graphics, narration, mix, brand and pins (the clips are the sections').
	Cut RecipeCut `bson:"cut" json:"cut"`
	// Bindings say where each input goes.
	Bindings []RecipeBinding `bson:"bindings,omitempty" json:"bindings,omitempty" jsonschema:"maxItems=200"`
	// Rules become the run project's rules (measured by the quality check
	// where it can); Signoffs the OKs its cut needs (D207).
	Rules    []RecipeRule `bson:"rules,omitempty" json:"rules,omitempty" jsonschema:"maxItems=50"`
	Signoffs []string     `bson:"signoffs,omitempty" json:"signoffs,omitempty" jsonschema:"maxItems=20"`
	// Hints guide Claude ("Name the app in the first two seconds").
	Hints []string `bson:"hints,omitempty" json:"hints,omitempty" jsonschema:"maxItems=20"`
	// From is the version of a cut the recipe was saved from.
	From *RecipeFrom `bson:"from,omitempty" json:"from,omitempty"`
	// Pack are the other videos every run makes besides its main one (D214):
	// shorts of a conversation the video plays, each its own project.
	Pack []RecipeOutput `bson:"pack,omitempty" json:"pack,omitempty" jsonschema:"maxItems=10"`
}

// Pack output kinds (RecipeOutput.Kind, D214).
const (
	// RecipeShorts: stretches of a conversation the run's video plays, each
	// made into its own project (conversation.short).
	RecipeShorts = "shorts"
)

// RecipeOutput is one kind of video a run makes besides its main one (D214, a
// pack): shorts of a conversation the main video plays, each its own project
// made through conversation.short. Their stretches are first picks from the
// conversation's best moments (D215) that Claude keeps or changes.
type RecipeOutput struct {
	Name string `bson:"name" json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$"`
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=shorts"`
	// Conversation is the conversation the shorts come from: its name in the
	// cut ("" the first one the video plays).
	Conversation string `bson:"conversation,omitempty" json:"conversation,omitempty" jsonschema:"maxLength=80"`
	// Footage is a footage input the shorts come from instead (D295): stretches
	// of the clips a run gives it — a call recording that isn't a conversation —
	// each short a new cut of the run's project (footage.short, D246).
	Footage string `bson:"footage,omitempty" json:"footage,omitempty" jsonschema:"maxLength=40,description=A footage input whose clips the shorts come from (instead of a conversation): each short is a new cut of the run's project."`
	// Count is how many a run makes (it makes up to Max when there are that
	// many good moments, at least Min; default 1–3).
	Count *RecipeRange `bson:"count,omitempty" json:"count,omitempty"`
	// Length is how long each one is (seconds; default 15–60).
	Length *RecipeRange `bson:"length,omitempty" json:"length,omitempty"`
	// Purpose says what to look for (Claude reads it when it keeps or changes the picks).
	Purpose string `bson:"purpose,omitempty" json:"purpose,omitempty" jsonschema:"maxLength=300"`
	// Shape is vertical (the default), landscape or square.
	Shape string `bson:"shape,omitempty" json:"shape,omitempty" jsonschema:"enum=,enum=vertical,enum=landscape,enum=square"`
	// Style is how each short's own cut moves between people (podcast, interview…; default the conversation's).
	Style string `bson:"style,omitempty" json:"style,omitempty" jsonschema:"maxLength=40"`
	// Captions: off leaves them out (default on).
	Captions *bool `bson:"captions,omitempty" json:"captions,omitempty"`
	// Named names each short's project: {title} (the recipe's), {n} (the run's
	// number), {k} (the short's), {first} (its first words); default the
	// conversation's name and the short's first words.
	Named string `bson:"named,omitempty" json:"named,omitempty" jsonschema:"maxLength=120"`
}

// RecipeLanding says where a run's video goes (D203).
type RecipeLanding struct {
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=new-project,enum=project"`
	// ProjectID: the project new cuts go into (kind project).
	ProjectID string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	// Name names a new project ("{title} — {date}"; {input} takes an input's words).
	Name string `bson:"name,omitempty" json:"name,omitempty" jsonschema:"maxLength=120"`
}

// RecipeRange is a smallest and a largest number (seconds, or a count).
type RecipeRange struct {
	Min float64 `bson:"min,omitempty" json:"min,omitempty" jsonschema:"minimum=0"`
	Max float64 `bson:"max,omitempty" json:"max,omitempty" jsonschema:"minimum=0"`
}

// RecipeFrom is the cut version a recipe was saved from.
type RecipeFrom struct {
	ProjectID  string `bson:"projectId" json:"projectId"`
	Branch     string `bson:"branch,omitempty" json:"branch,omitempty"`
	RevisionID string `bson:"revisionId" json:"revisionId"`
	Number     int    `bson:"number" json:"number"`
	Approved   bool   `bson:"approved,omitempty" json:"approved,omitempty"`
}

// RecipeInput is one thing that changes each time (D202). Limits come from
// where it is used: a graphic field's longest words, a word setting's box, a
// count of pictures.
type RecipeInput struct {
	Name  string `bson:"name" json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$"`
	Kind  string `bson:"kind" json:"kind" jsonschema:"enum=words,enum=choice,enum=footage,enum=pictures,enum=voice"`
	Label string `bson:"label,omitempty" json:"label,omitempty" jsonschema:"maxLength=120,description=What it is\\, in a few words (The headline)."`
	// Optional inputs keep their default when a run gives none; required
	// ones must be given every run.
	Required bool `bson:"required,omitempty" json:"required,omitempty"`
	// Default is what the saved video used (and what a run uses unless it
	// gives another).
	Default RecipeValue `bson:"default" json:"default"`
	// From is where values come from by default: a linked folder or a
	// collection (a run again takes the newest), or a project's footage.
	From *RecipeSource `bson:"from,omitempty" json:"from,omitempty"`
	// MaxLength is the longest words it takes (words; 0 no limit).
	MaxLength int `bson:"maxLength,omitempty" json:"maxLength,omitempty"`
	// Choices are what a choice may be.
	Choices []string `bson:"choices,omitempty" json:"choices,omitempty" jsonschema:"maxItems=40"`
	// Count is how many clips or pictures it takes.
	Count *RecipeRange `bson:"count,omitempty" json:"count,omitempty"`
}

// RecipeValue is an input's value: words, or things in MetaRush (clips,
// pictures, a voice) by ID.
type RecipeValue struct {
	Text  string   `bson:"text,omitempty" json:"text,omitempty" jsonschema:"maxLength=2000"`
	Items []string `bson:"items,omitempty" json:"items,omitempty" jsonschema:"maxItems=100,description=Clips (ast_…)\\, pictures (res_…) or a voice."`
}

// RecipeSource is where an input's values come from by default.
type RecipeSource struct {
	// Folder is a linked folder (fld_…): its newest pictures.
	Folder string `bson:"folder,omitempty" json:"folder,omitempty"`
	// Collection is a collection (col_…): its newest clips.
	Collection string `bson:"collection,omitempty" json:"collection,omitempty"`
	// Project is a project (prj_…): its newest footage.
	Project string `bson:"project,omitempty" json:"project,omitempty"`
	// Repo is a repository resource (res_…) and Path one of its picture paths
	// (D216): the pictures it keeps there at its current commit, in path order
	// ("" or . all it keeps). A run records the commit and each picture's hash.
	Repo string `bson:"repo,omitempty" json:"repo,omitempty"`
	Path string `bson:"path,omitempty" json:"path,omitempty" jsonschema:"maxLength=512"`
	// Drive is a Google Drive folder read through the person's own rclone,
	// without syncing it (D220): the rclone remote and the folder's path in it
	// (gdrive:Teaser/Art). Each run lists it, takes the newest files it needs
	// and fetches only those; it records each file's Drive ID and hash.
	Drive string `bson:"drive,omitempty" json:"drive,omitempty" jsonschema:"maxLength=1100,description=A Google Drive folder as rclone names it (remote:path)."`
	// Dropbox is a Dropbox folder read the same way (D306): files fetched by
	// their Dropbox IDs, checked against Dropbox's content hash.
	Dropbox string `bson:"dropbox,omitempty" json:"dropbox,omitempty" jsonschema:"maxLength=1100,description=A Dropbox folder as rclone names it (remote:path)."`
}

// RecipeSection is one part of the video, in order (D205).
type RecipeSection struct {
	Name string `bson:"name" json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$"`
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=fixed,enum=chosen"`
	// Purpose says what the section is for (Claude reads it for chosen ones).
	Purpose string `bson:"purpose,omitempty" json:"purpose,omitempty" jsonschema:"maxLength=300"`
	// Clips are a fixed section's clips, as saved (inputs bound to them change
	// their footage); a chosen section has none.
	Clips []editspec.Clip `bson:"clips,omitempty" json:"clips,omitempty" jsonschema:"maxItems=200"`
	// From is the input a chosen section picks from.
	From string `bson:"from,omitempty" json:"from,omitempty"`
	// Count is how many moments a chosen section has; Length how long it is (seconds).
	Count  *RecipeRange `bson:"count,omitempty" json:"count,omitempty"`
	Length *RecipeRange `bson:"length,omitempty" json:"length,omitempty"`
}

// RecipeCut is everything a run's cut has besides its clips: the same fields
// as an edit spec (editspec.Spec), read and written by the same code.
type RecipeCut struct {
	Title         string                              `bson:"title,omitempty" json:"title,omitempty"`
	Output        editspec.Output                     `bson:"output" json:"output"`
	Audio         []editspec.AudioTrack               `bson:"audio,omitempty" json:"audio,omitempty"`
	Captions      *editspec.Captions                  `bson:"captions,omitempty" json:"captions,omitempty"`
	Overlays      []editspec.Overlay                  `bson:"overlays,omitempty" json:"overlays,omitempty"`
	Brand         *editspec.Brand                     `bson:"brand,omitempty" json:"brand,omitempty"`
	Notes         string                              `bson:"notes,omitempty" json:"notes,omitempty"`
	Voiceover     *editspec.Voiceover                 `bson:"voiceover,omitempty" json:"voiceover,omitempty"`
	Mix           *editspec.Mix                       `bson:"mix,omitempty" json:"mix,omitempty"`
	Resources     map[string]editspec.ResourcePin     `bson:"resources,omitempty" json:"resources,omitempty"`
	Conversations map[string]editspec.ConversationPin `bson:"conversations,omitempty" json:"conversations,omitempty"`
	Fixed         map[string]editspec.FixedPin        `bson:"fixed,omitempty" json:"fixed,omitempty"`
}

// Binding targets (RecipeBinding.To): kind:where.
const (
	BindClip     = "clip"     // clip:<clipId> — footage: the clip shows this footage
	BindParam    = "param"    // param:<overlayId>.<field> — words or a choice: a graphic's field
	BindResource = "resource" // resource:<name> — pictures: the picture the cut's resources name
	BindWords    = "words"    // words:<clipId>.<KEY> — words: a Remotion clip's word setting (D168)
	// BindPictures is pictures:<clipId>.<NAME> — pictures: a Remotion clip's
	// picture setting (D209), drawn with the run's picture as the clip's own
	// (remotion.use's `pictures`); the version's pictures must be settings.
	BindPictures = "pictures"
	BindLine     = "line"  // line:<beatId> — words: a narration line's words
	BindVoice    = "voice" // voice — a voice: the narration lines read in the recipe's voice
	BindTitle    = "title" // title — words: the cut's title
	// BindPrompt is prompt:<clipId> — words: the clip is a generated shot made
	// again from these words (the same model, length and look), priced first.
	BindPrompt = "prompt"
)

// ViaRecipe marks what a recipe run does for the person who started it
// (D204): the versions, review copies and AI voice it makes are theirs, and
// the activity log shows them in the recipe channel.
const ViaRecipe = "recipe"

// RecipeBinding says where an input goes.
type RecipeBinding struct {
	Input string `bson:"input" json:"input"`
	To    string `bson:"to" json:"to" jsonschema:"maxLength=200,description=clip:<clip>\\, param:<graphic>.<field>\\, resource:<name>\\, words:<clip>.<KEY>\\, pictures:<clip>.<KEY>\\, line:<line>\\, prompt:<clip>\\, voice or title."`
}

// RecipeRule is a rule a run's project keeps (D207): measured by the
// quality check where it can (Measure), else a sign-off a person ticks.
type RecipeRule struct {
	Text    string `bson:"text" json:"text" jsonschema:"minLength=1,maxLength=300"`
	Measure string `bson:"measure,omitempty" json:"measure,omitempty" jsonschema:"maxLength=40"`
	Check   bool   `bson:"check,omitempty" json:"check,omitempty"`
}

// RecipeRun is one making of a recipe (D202–D204).
type RecipeRun struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	RecipeID    string `bson:"recipeId" json:"recipeId"`
	RecipeName  string `bson:"recipeName" json:"recipeName"`
	RecipeTitle string `bson:"recipeTitle" json:"recipeTitle"`
	// Number counts the recipe's runs (run 4).
	Number int `bson:"number" json:"number"`
	// Version is the recipe version it makes (its SHA-256).
	Version       string `bson:"version" json:"version"`
	VersionNumber int    `bson:"versionNumber" json:"versionNumber"`
	// Repeats is the run this one makes again (recipe again): the result is
	// a new version of the same cut.
	Repeats string `bson:"repeats,omitempty" json:"repeats,omitempty"`
	// Inputs are what every input was (D202): words, each file's SHA-256,
	// a picture's version.
	Inputs []RecipeRunInput `bson:"inputs" json:"inputs"`
	// Note is what the person asked for this run ("a warmer voice").
	Note string `bson:"note,omitempty" json:"note,omitempty" jsonschema:"maxLength=500"`
	// Settings are the Content Settings it followed, as resolved.
	Settings *RecipeRunSettings `bson:"settings,omitempty" json:"settings,omitempty"`
	// ProjectID and Branch are where its video is; NewProject: the run makes
	// (made) the project, called ProjectName.
	ProjectID   string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	ProjectName string `bson:"projectName,omitempty" json:"projectName,omitempty"`
	Branch      string `bson:"branch,omitempty" json:"branch,omitempty"`
	NewProject  bool   `bson:"newProject,omitempty" json:"newProject,omitempty"`
	// ProjectGone: its project was cleared out for good (D196); the run's
	// record stays.
	ProjectGone bool `bson:"projectGone,omitempty" json:"projectGone,omitempty"`
	// Fingerprint is the plan the person agreed to (recipe.plan).
	Fingerprint string `bson:"fingerprint" json:"fingerprint"`
	// Estimate is what the plan said it would cost (D208's estimate: a total
	// in dollars that opens to each step's parts — the service, its model, what
	// it draws on, the units and each part's price); Agreed what a person agreed
	// to (D204: one estimate agreed per run; more asks again).
	Estimate costs.Estimate   `bson:"estimate" json:"estimate"`
	Agreed   *RecipeAgreement `bson:"agreed,omitempty" json:"agreed,omitempty"`
	// Spent is what its steps cost so far.
	Spent RecipeSpent  `bson:"spent" json:"spent"`
	Steps []RecipeStep `bson:"steps" json:"steps"`
	State string       `bson:"state" json:"state" jsonschema:"enum=planned,enum=running,enum=waiting,enum=done,enum=stopped,enum=failed"`
	// Waiting says plainly what it waits for ("Waiting for Claude to choose
	// the moments"); WaitingFor whom (person, claude).
	Waiting    string `bson:"waiting,omitempty" json:"waiting,omitempty"`
	WaitingFor string `bson:"waitingFor,omitempty" json:"waitingFor,omitempty" jsonschema:"enum=,enum=person,enum=claude"`
	// WaitingUSD is how much more a run waiting for a person's OK asks to
	// spend (US dollars): what going on agrees to (recipe.continue moreUsd).
	WaitingUSD float64 `bson:"waitingUsd,omitempty" json:"waitingUsd,omitempty" jsonschema:"description=How much more it asks to spend in US dollars\\, when it waits for a person's OK to spend more."`
	// Problem says plainly why it failed or stopped.
	Problem string `bson:"problem,omitempty" json:"problem,omitempty"`
	// Revision is the newest version of the cut the run made; Render its
	// review copy; QA what the quality check found.
	Revision       string `bson:"revision,omitempty" json:"revision,omitempty"`
	RevisionNumber int    `bson:"revisionNumber,omitempty" json:"revisionNumber,omitempty"`
	Render         string `bson:"render,omitempty" json:"render,omitempty"`
	QA             string `bson:"qa,omitempty" json:"qa,omitempty" jsonschema:"description=What the quality check found\\, in a few words."`
	// StandIns are the clips the run put in chosen sections for Claude to
	// replace, or its first picks (D215) for Claude to keep or change; the run
	// goes on once none is left as MetaRush put it (kept ones count as chosen).
	StandIns []RecipeStandIn `bson:"standIns,omitempty" json:"standIns,omitempty"`
	// Outputs are the other videos of the run's pack (D214): each short's
	// stretch (a first pick Claude keeps or changes), then its own project,
	// version and review copy.
	Outputs []RecipeRunOutput `bson:"outputs,omitempty" json:"outputs,omitempty"`
	// Made are the clips the run made for sections (by the recipe's clip
	// names): compositions drawn with its words, shots generated from them.
	Made map[string]RecipeMadeClip `bson:"made,omitempty" json:"made,omitempty"`
	// Sample is the run's sample (D294): before the rest of a run with an
	// expensive kind of work, one priced sample of each kind that a person
	// approves before the rest is planned — or why a person skipped it.
	Sample *RecipeSample `bson:"sample,omitempty" json:"sample,omitempty"`
	// Installation is the computer that advances it (its files and keys);
	// Lease keeps two loops from advancing it at once.
	Installation string     `bson:"installation" json:"installation"`
	Lease        *RunLease  `bson:"lease,omitempty" json:"-"`
	CreatedBy    Actor      `bson:"createdBy" json:"createdBy"`
	CreatedAt    time.Time  `bson:"createdAt" json:"createdAt"`
	UpdatedAt    time.Time  `bson:"updatedAt" json:"updatedAt"`
	FinishedAt   *time.Time `bson:"finishedAt,omitempty" json:"finishedAt,omitempty"`
}

// Where a run's sample stands (RecipeSample.State, D294).
const (
	SampleMaking   = "making"    // the sample is being made
	SampleWaiting  = "waiting"   // made: a person watches and listens, then approves it or not
	SampleApproved = "approved"  // a person approved it: the rest of the run was planned
	SampleNotQuite = "not-quite" // a person said it isn't right: the run stopped
	SampleSkipped  = "skipped"   // a person skipped it, with a reason: no sample was made
	SampleStopped  = "stopped"   // the run stopped or didn't finish before a person decided
)

// Kinds of expensive work a sample is made of (RecipeSampleKind.Kind, D294).
const (
	SampleVoice   = "voice"    // the AI voice: a short sample line
	SampleShots   = "shots"    // generated shots: one shot
	SampleCleanUp = "clean-up" // clearer voices in the cloud: one clip's clean-up
)

// RecipeSample is a run's sample (D294): one priced sample of each expensive
// kind of work the run's estimate has — the shortest line in the AI voice, one
// generated shot, a stretch of the new footage made clearer in the cloud —
// made in a short cut of its own in the run's place (its review copy is what a
// person watches), costing what its own estimate says, before the rest of the
// run is planned. A person approves it (the rest is planned then, and agreed to
// as usual) or says it isn't right (the run stops); people may skip it with a
// reason, recorded. Auto never makes or approves one.
type RecipeSample struct {
	State string `bson:"state" json:"state" jsonschema:"enum=making,enum=waiting,enum=approved,enum=not-quite,enum=skipped,enum=stopped"`
	// Kinds are the expensive kinds of work it samples, each with what its
	// sample is and what that kind costs in the whole run.
	Kinds []RecipeSampleKind `bson:"kinds" json:"kinds"`
	// Estimate is what the sample costs (the services' own parts); Spent what it spent.
	Estimate costs.Estimate `bson:"estimate" json:"estimate"`
	Spent    RecipeSpent    `bson:"spent" json:"spent"`
	// Branch is the sample's own cut in the run's project; Revision its newest
	// version; Render its review copy; QA what its quality check found.
	Branch         string `bson:"branch,omitempty" json:"branch,omitempty"`
	Revision       string `bson:"revision,omitempty" json:"revision,omitempty"`
	RevisionNumber int    `bson:"revisionNumber,omitempty" json:"revisionNumber,omitempty"`
	Render         string `bson:"render,omitempty" json:"render,omitempty"`
	QA             string `bson:"qa,omitempty" json:"qa,omitempty"`
	// By and At say who approved it, turned it down or skipped it, and when;
	// Note is what they said (why it isn't right).
	By   *Actor     `bson:"by,omitempty" json:"by,omitempty"`
	At   *time.Time `bson:"at,omitempty" json:"at,omitempty"`
	Note string     `bson:"note,omitempty" json:"note,omitempty" jsonschema:"maxLength=500"`
	// Skipped is the reason a person gave for starting without the sample.
	Skipped string `bson:"skipped,omitempty" json:"skipped,omitempty" jsonschema:"maxLength=500"`
	// Rest is what the rest of the run was planned to cost when the sample was
	// approved (its estimate then; the run's own estimate is the same).
	Rest *costs.Estimate `bson:"rest,omitempty" json:"rest,omitempty"`
}

// RecipeSampleKind is one expensive kind of work in a run and its sample.
type RecipeSampleKind struct {
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=voice,enum=shots,enum=clean-up"`
	// What says what the sample is ("a short sample line in the AI voice");
	// Source what it draws on ("line 1: “Welcome back…”", "0:12–0:42 of “Call”").
	What   string `bson:"what" json:"what"`
	Source string `bson:"source,omitempty" json:"source,omitempty"`
	// USD is what the sample of this kind costs (its estimate); RunUSD what this
	// kind costs in the whole run.
	USD    float64 `bson:"usd" json:"usd"`
	RunUSD float64 `bson:"runUsd" json:"runUsd"`
	// Clip is the recipe's clip a shot is made for; Line the narration line
	// sampled and Text its words (the whole line, which the run reuses);
	// AssetID, From and To the stretch of new footage made clearer.
	Clip    string  `bson:"clip,omitempty" json:"clip,omitempty"`
	Line    string  `bson:"line,omitempty" json:"line,omitempty"`
	Text    string  `bson:"text,omitempty" json:"text,omitempty"`
	AssetID string  `bson:"assetId,omitempty" json:"assetId,omitempty"`
	From    float64 `bson:"from,omitempty" json:"from,omitempty"`
	To      float64 `bson:"to,omitempty" json:"to,omitempty"`
}

// RunLease is who advances a run until when.
type RunLease struct {
	By    string    `bson:"by"`
	Until time.Time `bson:"until"`
}

// RecipeRunInput is what one input was in a run (D202).
type RecipeRunInput struct {
	Name string `bson:"name" json:"name"`
	Kind string `bson:"kind" json:"kind"`
	// Text is words or a choice.
	Text string `bson:"text,omitempty" json:"text,omitempty"`
	// Items are the clips or pictures, as they were.
	Items []RecipeRunItem `bson:"items,omitempty" json:"items,omitempty"`
	// Repo and Commit: pictures taken from a repository path (D216) — the
	// repository resource and the commit they were kept from.
	Repo   string `bson:"repo,omitempty" json:"repo,omitempty"`
	Commit string `bson:"commit,omitempty" json:"commit,omitempty"`
	// Drive is the Google Drive folder the files came from (D220: remote:path);
	// DriveKind says it was a Dropbox folder instead (D306: "dropbox").
	Drive     string `bson:"drive,omitempty" json:"drive,omitempty"`
	DriveKind string `bson:"driveKind,omitempty" json:"driveKind,omitempty" jsonschema:"enum=,enum=drive,enum=dropbox"`
	// Default: the recipe's default was used; Changed: it differs from the
	// run this one repeats.
	Default bool `bson:"default,omitempty" json:"default,omitempty"`
	Changed bool `bson:"changed,omitempty" json:"changed,omitempty"`
	// Words says what it was in a few words ("3 pictures from “Character art”").
	Words string `bson:"words" json:"words"`
}

// RecipeRunItem is one clip, picture or voice an input was, by what it is.
type RecipeRunItem struct {
	ID    string `bson:"id" json:"id"`
	Title string `bson:"title,omitempty" json:"title,omitempty"`
	// SHA256 is the file's (a clip's, or the picture version's).
	SHA256   string  `bson:"sha256,omitempty" json:"sha256,omitempty"`
	Version  int     `bson:"version,omitempty" json:"version,omitempty" jsonschema:"description=A picture's version number."`
	Duration float64 `bson:"duration,omitempty" json:"duration,omitempty"`
	// Path is a picture's file in the repository it was kept from (D216).
	Path string `bson:"path,omitempty" json:"path,omitempty"`
	// Drive is the Google Drive file it is (D220). Until the run fetches it,
	// ID is empty and SHA256 is Google's checksum when it gave one.
	Drive *RecipeDriveFile `bson:"drive,omitempty" json:"drive,omitempty"`
}

// RecipeDriveFile is one file of a Google Drive folder a run uses (D220).
type RecipeDriveFile struct {
	// ID is its Drive ID; Name its name in the folder.
	ID       string    `bson:"id" json:"id"`
	Name     string    `bson:"name" json:"name"`
	Size     int64     `bson:"size" json:"size"`
	Modified time.Time `bson:"modified" json:"modified"`
	// Checksum is Google's (sha256:… or md5:…; "" when it gave none).
	Checksum string `bson:"checksum,omitempty" json:"checksum,omitempty"`
	// Fetched: the run fetched it (a clip or picture of its own now).
	Fetched bool `bson:"fetched,omitempty" json:"fetched,omitempty"`
}

// RecipeRunSettings are the Content Settings a run followed, as they were.
type RecipeRunSettings struct {
	// Owner is the project whose settings they are ("" nothing set up).
	Owner     string `bson:"owner,omitempty" json:"owner,omitempty"`
	OwnerName string `bson:"ownerName,omitempty" json:"ownerName,omitempty"`
	// Voice is the narration voice the settings name (elevenlabs).
	VoiceID   string `bson:"voiceId,omitempty" json:"voiceId,omitempty"`
	VoiceName string `bson:"voiceName,omitempty" json:"voiceName,omitempty"`
	// Brand is the brand kit's colours and logo pins in a few words.
	Brand string `bson:"brand,omitempty" json:"brand,omitempty"`
}

// RecipeSpent is what a run or a step spent: in US dollars (what the
// services' estimates count: ElevenLabs' credits at its pay-as-you-go prices,
// D208) and the ElevenLabs credits themselves, which the project's budget
// counts.
type RecipeSpent struct {
	USD     float64 `bson:"usd,omitempty" json:"usd,omitempty"`
	Credits float64 `bson:"credits,omitempty" json:"credits,omitempty"`
}

// RecipeAgreement is a person's OK to spend up to an amount on a run (D204:
// one estimate agreed per run; going past it asks again).
type RecipeAgreement struct {
	// UpTo is what the person agreed to spend in US dollars, in all (later OKs
	// included); at least the plan's estimate.
	UpTo float64   `bson:"upTo" json:"upTo"`
	By   Actor     `bson:"by" json:"by"`
	At   time.Time `bson:"at" json:"at"`
	// Skipped: the person turned "Estimate costs of outside AI services before
	// edits" off (Settings → Preferences, D208), so the run started without the
	// estimate step; budgets, daily limits and project caps still stop spending.
	Skipped bool `bson:"skipped,omitempty" json:"skipped,omitempty"`
	// More are later OKs to spend more than the estimate.
	More []RecipeMore `bson:"more,omitempty" json:"more,omitempty"`
}

// RecipeMore is a later OK to spend more on a run (US dollars).
type RecipeMore struct {
	USD float64   `bson:"usd" json:"usd"`
	By  Actor     `bson:"by" json:"by"`
	At  time.Time `bson:"at" json:"at"`
}

// RecipeStep is one step of a run: what it does, where it stands, what it
// made and cost.
type RecipeStep struct {
	ID string `bson:"id" json:"id" jsonschema:"enum=inputs,enum=place,enum=sample,enum=approve,enum=code,enum=shots,enum=assemble,enum=voice,enum=claude,enum=sound,enum=review,enum=pack,enum=finish"`
	// Label says what it does ("Bring in the inputs").
	Label string `bson:"label" json:"label"`
	// Who does it: metarush, claude or person.
	Who   string `bson:"who" json:"who" jsonschema:"enum=metarush,enum=claude,enum=person"`
	State string `bson:"state" json:"state" jsonschema:"enum=pending,enum=running,enum=waiting,enum=done,enum=skipped,enum=failed"`
	// Detail says what it did or waits for, plainly.
	Detail string `bson:"detail,omitempty" json:"detail,omitempty"`
	// Estimate is what the plan said the step costs, in US dollars (its parts
	// are in the run's estimate, under the step's label).
	Estimate float64     `bson:"estimate,omitempty" json:"estimate,omitempty"`
	Spent    RecipeSpent `bson:"spent" json:"spent"`
	Jobs     []string    `bson:"jobs,omitempty" json:"jobs,omitempty"`
	Made     []string    `bson:"made,omitempty" json:"made,omitempty" jsonschema:"description=What it made: clips\\, a version\\, a render."`
	// Work is what the step keeps between looks (renders and generations it
	// asked for, by the recipe's clip names).
	Work    map[string]string `bson:"work,omitempty" json:"-"`
	Started *time.Time        `bson:"started,omitempty" json:"started,omitempty"`
	Ended   *time.Time        `bson:"ended,omitempty" json:"ended,omitempty"`
}

// RecipeMadeClip is a clip a run made for a section.
type RecipeMadeClip struct {
	AssetID  string  `bson:"assetId" json:"assetId"`
	Duration float64 `bson:"duration" json:"duration"`
	// Whole: the recipe's clip played all of what it was made from, so the
	// new one plays all of its own (words can change a composition's length).
	Whole bool `bson:"whole,omitempty" json:"whole,omitempty"`
}

// RecipeStandIn is a clip a run put in a chosen section: a stand-in for Claude
// to replace, or a first pick (D215) from the footage's moments for Claude to
// keep or change.
type RecipeStandIn struct {
	Section string  `bson:"section" json:"section"`
	ClipID  string  `bson:"clipId" json:"clipId"`
	AssetID string  `bson:"assetId" json:"assetId"`
	In      float64 `bson:"in" json:"in"`
	Out     float64 `bson:"out" json:"out"`
	// First: a first pick from the footage's moments (else a stand-in spread
	// over the footage); Why says why it might work, Words what is said in it.
	First bool     `bson:"first,omitempty" json:"first,omitempty"`
	Why   []string `bson:"why,omitempty" json:"why,omitempty"`
	Words string   `bson:"words,omitempty" json:"words,omitempty"`
	// Kept: Claude or a person looked at it and kept it as it is.
	Kept   bool   `bson:"kept,omitempty" json:"kept,omitempty"`
	KeptBy *Actor `bson:"keptBy,omitempty" json:"keptBy,omitempty"`
}

// Where a pick stands (RecipeRunOutput.Pick, D215).
const (
	PickFirst   = "first"   // MetaRush's first pick; Claude hasn't looked yet
	PickKept    = "kept"    // Claude or a person kept it
	PickChanged = "changed" // Claude or a person changed it
	PickDropped = "dropped" // taken out: that short isn't made
)

// Where one video of a pack stands (RecipeRunOutput.State).
const (
	OutputPlanned = "planned" // its stretch is picked; not made yet
	OutputMaking  = "making"  // its project and cut are made; its review copy is coming
	OutputReady   = "ready"   // its review copy is ready (and checked)
	OutputDropped = "dropped" // not made
	OutputFailed  = "failed"
)

// RecipeRunOutput is one video of a run's pack (D214): a short of a
// conversation the run's video plays, made into its own project.
type RecipeRunOutput struct {
	// Output is the pack output it is one of (its name in the recipe); N its
	// number among them (1, 2…).
	Output string `bson:"output" json:"output"`
	N      int    `bson:"n" json:"n"`
	Kind   string `bson:"kind" json:"kind" jsonschema:"enum=shorts"`
	// Conversation (cnv_…) and its name; From and To the stretch on its clock (seconds).
	Conversation     string `bson:"conversation" json:"conversation"`
	ConversationName string `bson:"conversationName,omitempty" json:"conversationName,omitempty"`
	// AssetID is the clip a short of footage plays instead (D295; From and To
	// are then seconds into it), FootageName its name.
	AssetID     string  `bson:"assetId,omitempty" json:"assetId,omitempty"`
	FootageName string  `bson:"footageName,omitempty" json:"footageName,omitempty"`
	From        float64 `bson:"from" json:"from"`
	To          float64 `bson:"to" json:"to"`
	// Why says why it might work, First its first words (the hook).
	Why   []string `bson:"why,omitempty" json:"why,omitempty"`
	First string   `bson:"first,omitempty" json:"first,omitempty"`
	// Pick is where the stretch stands: first (MetaRush's), kept, changed or dropped.
	Pick   string `bson:"pick" json:"pick" jsonschema:"enum=first,enum=kept,enum=changed,enum=dropped"`
	PickBy *Actor `bson:"pickBy,omitempty" json:"pickBy,omitempty"`
	// State: planned, making (its project is made), ready (its review copy is), dropped, failed.
	State   string `bson:"state" json:"state" jsonschema:"enum=planned,enum=making,enum=ready,enum=dropped,enum=failed"`
	Problem string `bson:"problem,omitempty" json:"problem,omitempty"`
	// Where it is: its own project and cut, the version made, its review copy and what its quality check found.
	ProjectID      string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	ProjectName    string `bson:"projectName,omitempty" json:"projectName,omitempty"`
	ProjectGone    bool   `bson:"projectGone,omitempty" json:"projectGone,omitempty"`
	Branch         string `bson:"branch,omitempty" json:"branch,omitempty"`
	Revision       string `bson:"revision,omitempty" json:"revision,omitempty"`
	RevisionNumber int    `bson:"revisionNumber,omitempty" json:"revisionNumber,omitempty"`
	Render         string `bson:"render,omitempty" json:"render,omitempty"`
	QA             string `bson:"qa,omitempty" json:"qa,omitempty"`
}
