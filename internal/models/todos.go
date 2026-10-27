package models

import "time"

// To-do kinds: questions MetaRush cannot answer by itself.
const (
	TodoSpeakerUnnamed   = "speaker_unnamed"   // who is this voice?
	TodoSpeakerMatch     = "speaker_match"     // is this voice <person>?
	TodoMusicRights      = "music_rights"      // may we use this music?
	TodoBriefMissing     = "brief_missing"     // the project brief lacks goal/audience/tone
	TodoDuplicatePerson  = "duplicate_person"  // are these two people the same person?
	TodoRevisionApproval = "revision_approval" // a cut is waiting for approval
	TodoFacePerson       = "face_person"       // is this confirmed face really <person>?
	TodoSpelling         = "spelling"          // put a word people keep fixing on the spelling list? (D147)
	TodoFaceGroup        = "face_group"        // who is this (a group of one person's faces in a clip, D155)?
	TodoRemotionApproval = "remotion_approval" // a version of a Remotion source waits for a person (D172)
	TodoRecipeRun        = "recipe_run"        // a run of a recipe waits for a person or for Claude (D204)
	TodoAbilityApproval  = "ability_approval"  // a version of an ability waits for an owner (D231)
	TodoShipGate         = "ship_gate"         // hold versions back until the quality check passes and sign-offs are given? (D298, asked once)
)

// TodoKinds lists every kind (for validation and filters).
var TodoKinds = []string{TodoSpeakerUnnamed, TodoSpeakerMatch, TodoMusicRights, TodoBriefMissing, TodoDuplicatePerson, TodoRevisionApproval, TodoFacePerson, TodoSpelling, TodoFaceGroup, TodoRemotionApproval, TodoRecipeRun, TodoAbilityApproval, TodoShipGate}

// To-do states and resolutions.
const (
	TodoOpen      = "open"
	TodoAnswered  = "answered"
	TodoDismissed = "dismissed"

	ResolvedAnswered  = "answered"  // someone answered the to-do
	ResolvedDismissed = "dismissed" // someone chose not to answer
	ResolvedElsewhere = "resolved"  // the underlying data was fixed another way
	ResolvedObsolete  = "obsolete"  // the question no longer applies
)

// TodoSuggestion is a ready-made answer.
type TodoSuggestion struct {
	Label    string  `bson:"label" json:"label"`                           // what the person sees ("Ada (86% match)")
	Value    string  `bson:"value" json:"value"`                           // the answer it stands for
	PersonID string  `bson:"personId,omitempty" json:"personId,omitempty"` // speaker answers: the person
	Score    float64 `bson:"score,omitempty" json:"score,omitempty"`
	// New: the answer names someone who isn't in People yet (a name shown or
	// said in the video, D156); answering adds them.
	New bool `bson:"new,omitempty" json:"new,omitempty"`
	// Basis is what a name on a question about a person rests on (D189):
	// voice_cue, looks, tile, lower, title, said, voice, aside. MetaRush keeps
	// score of how often each kind was right.
	Basis []string `bson:"basis,omitempty" json:"basis,omitempty" jsonschema:"enum=voice_cue,enum=looks,enum=tile,enum=lower,enum=title,enum=said,enum=voice,enum=aside"`
}

// Kinds of evidence a name on a question about a person can rest on (D189).
const (
	// BasisVoiceCue: the face is alone on screen while the person's voice
	// talks (or, for a voice, the person's face is alone on screen while it talks).
	BasisVoiceCue = "voice_cue"
	BasisLooks    = "looks" // the face looks like faces a person confirmed
	BasisTile     = "tile"  // a call app prints the name under the face
	BasisLower    = "lower" // a lower third shows the name while the face is on screen
	BasisTitle    = "title" // a title shows the name just before the face appears
	BasisSaid     = "said"  // the name is said in the transcript ("I'm Ada", "Thanks, Ada")
	BasisVoice    = "voice" // the voice sounds like voices a person confirmed
	BasisAside    = "aside" // the face was left out when the person's other faces were named
)

// BasisKinds lists the kinds of evidence, in the order they are shown.
var BasisKinds = []string{BasisVoiceCue, BasisLooks, BasisTile, BasisLower, BasisTitle, BasisSaid, BasisVoice, BasisAside}

// VoiceCueSeconds is how long a face must be alone on screen while one
// person's voice talks before that voice is evidence of who the face is
// (D189; it was 2 s, right about a third of the time in calls, whose picture
// switches late and often shows the listener).
const VoiceCueSeconds = 6.0

// TodoGuess is one name a question about a person offered, what it rested on
// and whether the person's answer agreed (D189). Recorded when the question
// is answered; MetaRush scores each kind of evidence from them.
type TodoGuess struct {
	Name     string   `bson:"name" json:"name"`
	PersonID string   `bson:"personId,omitempty" json:"personId,omitempty"`
	Basis    []string `bson:"basis" json:"basis"`
	Agreed   bool     `bson:"agreed" json:"agreed"`
	// Main: the question's suggestion ("Is this Ada?"), not a pick.
	Main bool `bson:"main,omitempty" json:"main,omitempty"`
	// Seconds is how long the voice was heard with the face (a voice cue).
	Seconds float64 `bson:"seconds,omitempty" json:"seconds,omitempty"`
}

// TodoFace is one face a question shows: where it is in which frame.
type TodoFace struct {
	FaceID string    `bson:"faceId" json:"faceId"`
	At     float64   `bson:"at" json:"at" jsonschema:"description=The frame that shows the face (source seconds)."`
	Box    []float64 `bson:"box" json:"box" jsonschema:"description=Where the face is in that frame: x\\, y\\, width\\, height from the top-left\\, 0 to 1."`
}

// TodoEvidence is one reason MetaRush suggests a name for a group of faces
// (D156), in plain words.
type TodoEvidence struct {
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=voice,enum=looks,enum=shown,enum=said,enum=aside"`
	Text string `bson:"text" json:"text" jsonschema:"description=Plain words\\, e.g. The video shows “Ada Park” under this face."`
	// Name is the name this evidence is for; PersonID when someone in People has it.
	Name     string   `bson:"name,omitempty" json:"name,omitempty"`
	PersonID string   `bson:"personId,omitempty" json:"personId,omitempty"`
	At       *float64 `bson:"at,omitempty" json:"at,omitempty" jsonschema:"description=Where to see or hear it (source seconds)."`
	Score    float64  `bson:"score,omitempty" json:"score,omitempty"`
}

// TodoContext is what a person needs to answer: where it is, what to listen to or look at.
type TodoContext struct {
	AssetName   string     `bson:"assetName,omitempty" json:"assetName,omitempty"`   // the clip's filename
	AssetTitle  string     `bson:"assetTitle,omitempty" json:"assetTitle,omitempty"` // the clip as people see it (its title)
	ProjectName string     `bson:"projectName,omitempty" json:"projectName,omitempty"`
	Label       string     `bson:"label,omitempty" json:"label,omitempty"`     // voice label (speaker_00)
	Speaker     string     `bson:"speaker,omitempty" json:"speaker,omitempty"` // how the voice is shown ("Speaker 2")
	Samples     []TimeSpan `bson:"samples,omitempty" json:"samples,omitempty"` // snippets to listen to
	FrameAt     *float64   `bson:"frameAt,omitempty" json:"frameAt,omitempty"` // a picture to look at
	Seconds     float64    `bson:"seconds,omitempty" json:"seconds,omitempty"`
	PersonID    string     `bson:"personId,omitempty" json:"personId,omitempty"`
	PersonName  string     `bson:"personName,omitempty" json:"personName,omitempty"`
	OtherID     string     `bson:"otherId,omitempty" json:"otherId,omitempty"`
	OtherName   string     `bson:"otherName,omitempty" json:"otherName,omitempty"`
	TrackID     string     `bson:"trackId,omitempty" json:"trackId,omitempty"`
	Field       string     `bson:"field,omitempty" json:"field,omitempty"` // brief field (goal, audience, tone)
	Revision    int        `bson:"revision,omitempty" json:"revision,omitempty"`
	Score       float64    `bson:"score,omitempty" json:"score,omitempty"`
	// FaceID and Box: the face a question is about and where it is in the
	// FrameAt picture ([x, y, width, height] from the top-left, 0 to 1).
	FaceID string    `bson:"faceId,omitempty" json:"faceId,omitempty"`
	Box    []float64 `bson:"box,omitempty" json:"box,omitempty"`
	// A question about a group of one person's faces in a clip (D155): the
	// group, a few sample faces, how often and how long it is on screen,
	// whether a latest cut uses it, and why a name is suggested.
	GroupID     string         `bson:"groupId,omitempty" json:"groupId,omitempty"`
	Faces       []TodoFace     `bson:"faces,omitempty" json:"faces,omitempty"`
	Appearances int            `bson:"appearances,omitempty" json:"appearances,omitempty" jsonschema:"description=How many times the faces show up in the clip."`
	OnScreen    float64        `bson:"onScreen,omitempty" json:"onScreen,omitempty" jsonschema:"description=Seconds on screen in all."`
	InCut       bool           `bson:"inCut,omitempty" json:"inCut,omitempty" jsonschema:"description=A latest cut of one of the clip's projects shows these faces."`
	Evidence    []TodoEvidence `bson:"evidence,omitempty" json:"evidence,omitempty"`
	// Shown is the name the video shows under these faces, when the
	// suggestion is that name ("Use the names shown in the video").
	Shown string `bson:"shown,omitempty" json:"shown,omitempty"`
	// Link is where to look before answering (a dashboard path).
	Link string `bson:"link,omitempty" json:"link,omitempty"`
	// Remotion: the version of a Remotion source a question is about (D172).
	Remotion *TodoRemotion `bson:"remotion,omitempty" json:"remotion,omitempty"`
	// Recipe: the run (Recipes) a question is about (D204).
	Recipe *TodoRecipe `bson:"recipe,omitempty" json:"recipe,omitempty"`
	// Ability: the version of an ability a question is about (D231).
	Ability *TodoAbility `bson:"ability,omitempty" json:"ability,omitempty"`
}

// TodoAbility names a version of an ability that waits for an owner.
type TodoAbility struct {
	Name    string `bson:"name" json:"name"`
	Title   string `bson:"title" json:"title"`
	Version string `bson:"version" json:"version"`
	Number  int    `bson:"number" json:"number"`
}

// TodoRecipe names a run (Recipes) and what it waits for.
type TodoRecipe struct {
	RunID  string `bson:"runId" json:"runId"`
	Name   string `bson:"name" json:"name" jsonschema:"description=Its short name."` // the Recipes entry's
	Title  string `bson:"title" json:"title"`
	Number int    `bson:"number" json:"number"`
	// WaitingFor is who it waits for: person (to go on, perhaps spending
	// more) or claude (to choose the moments).
	WaitingFor string `bson:"waitingFor" json:"waitingFor" jsonschema:"enum=person,enum=claude"`
	// More is how much more it asks a person to agree to ("" nothing more).
	More string `bson:"more,omitempty" json:"more,omitempty"`
}

// TodoRemotion names a version of a Remotion source.
type TodoRemotion struct {
	SourceID string `bson:"sourceId" json:"sourceId"`
	Name     string `bson:"name" json:"name" jsonschema:"description=The source's short name."`
	Title    string `bson:"title" json:"title"`
	Version  string `bson:"version" json:"version"`
	Number   int    `bson:"number" json:"number"`
}

// Todo is an open question for a person, created by the system (idempotently,
// one open to-do per GroupKey) and resolved by answering it — which changes
// the underlying data through the normal operations — or automatically when
// the data is fixed another way.
type Todo struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	Kind        string `bson:"kind" json:"kind" jsonschema:"enum=speaker_unnamed,enum=speaker_match,enum=music_rights,enum=brief_missing,enum=duplicate_person,enum=revision_approval,enum=face_person,enum=spelling,enum=face_group,enum=remotion_approval,enum=recipe_run,enum=ability_approval,enum=ship_gate"`
	Scope       string `bson:"scope" json:"scope" jsonschema:"enum=asset,enum=project,enum=revision,enum=workspace"`
	AssetID     string `bson:"assetId,omitempty" json:"assetId,omitempty"`
	ProjectID   string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	RevisionID  string `bson:"revisionId,omitempty" json:"revisionId,omitempty"`
	// ProjectIDs are the projects the to-do shows up in (an asset's projects).
	ProjectIDs  []string         `bson:"projectIds" json:"projectIds"`
	GroupKey    string           `bson:"groupKey" json:"groupKey"`
	Question    string           `bson:"question" json:"question"`
	Detail      string           `bson:"detail,omitempty" json:"detail,omitempty"`
	Context     TodoContext      `bson:"context" json:"context"`
	Suggestions []TodoSuggestion `bson:"suggestions" json:"suggestions"`
	// FreeText means a typed answer is accepted (not only a suggestion).
	FreeText   bool       `bson:"freeText" json:"freeText"`
	HumanOnly  bool       `bson:"humanOnly,omitempty" json:"humanOnly,omitempty"`
	Priority   int        `bson:"priority" json:"priority"` // 1 high … 3 low
	State      string     `bson:"state" json:"state" jsonschema:"enum=open,enum=answered,enum=dismissed"`
	Answer     string     `bson:"answer,omitempty" json:"answer,omitempty"`
	Resolution string     `bson:"resolution,omitempty" json:"resolution,omitempty" jsonschema:"enum=answered,enum=dismissed,enum=resolved,enum=obsolete"`
	AnsweredBy *Actor     `bson:"answeredBy,omitempty" json:"answeredBy,omitempty"`
	AnsweredAt *time.Time `bson:"answeredAt,omitempty" json:"answeredAt,omitempty"`
	// Guesses are the names a question about a person offered, what each
	// rested on and whether the answer agreed (D189); Scored once worked out
	// (also for answers that judged no name, so it is done once).
	Guesses   []TodoGuess `bson:"guesses,omitempty" json:"guesses,omitempty"`
	Scored    bool        `bson:"scored,omitempty" json:"scored,omitempty"`
	CreatedAt time.Time   `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time   `bson:"updatedAt" json:"updatedAt"`
}

// PersonQuestion says whether a kind of to-do asks who someone is (its
// answers score the evidence, D189).
func PersonQuestion(kind string) bool {
	switch kind {
	case TodoFaceGroup, TodoFacePerson, TodoSpeakerMatch, TodoSpeakerUnnamed:
		return true
	}
	return false
}

// OpenQuestions says that open to-dos are waiting about something (a clip), in
// plain words and with where to answer them. Job details, the activity bar and
// the bell all build it with todos.AboutClip, so they say and link the same.
type OpenQuestions struct {
	Count int    `json:"count"`
	Text  string `json:"text" jsonschema:"description=Plain words, e.g. 2 questions about this clip."`
	Link  string `json:"link" jsonschema:"description=Dashboard path of the To-dos page filtered to the clip."`
}
