package models

import "time"

// What a project keeps from its conversations (D166, docs/keeping.md): every
// reusable thing a conversation brought in is offered once ("Keep this?"),
// then kept or declined for good.

// Kinds of things a conversation can offer to keep.
const (
	KeepSource   = "source"   // a website, video or repository → the project's sources
	KeepPicture  = "picture"  // a logo or picture → the project's resources (and its brand kit)
	KeepBrief    = "brief"    // goals, audience, deliverables → the project's brief
	KeepRule     = "rule"     // a style rule → the project's rules
	KeepPerson   = "person"   // a fact about a person → their People notes
	KeepSignoff  = "signoff"  // who must OK the video → a sign-off on the cut
	KeepCheck    = "check"    // a problem raised twice → a check in every quality check
	KeepImprove  = "improve"  // something MetaRush couldn't do → the improvement list
	KeepSpelling = "spelling" // how a word is spelled → the spelling list
	// KeepPromise is a promise made to the client ("captions on every video",
	// D301): a rule the quality check measures, which holds delivery when broken.
	KeepPromise = "promise"
)

// KeepKinds lists them in the order the panel explains them.
var KeepKinds = []string{KeepSource, KeepPicture, KeepBrief, KeepRule, KeepPromise, KeepPerson, KeepSignoff, KeepCheck, KeepImprove, KeepSpelling}

// Keep item states.
const (
	KeepAsked    = "asked"
	KeepKept     = "kept"
	KeepDeclined = "declined"
)

// KeepItem is one thing a conversation offered to keep: asked once per
// project and content (Key), then kept or declined.
type KeepItem struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"-"`
	ProjectID   string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	Kind        string `bson:"kind" json:"kind" jsonschema:"enum=source,enum=picture,enum=brief,enum=rule,enum=promise,enum=person,enum=signoff,enum=check,enum=improve,enum=spelling"`
	// Key is the kind and the content compared (lower case, letters and
	// digits; a link by its address): unique per project.
	Key string `bson:"key" json:"-"`
	// Text is the thing in words: the rule, the sign-off, the fact, the link.
	Text   string     `bson:"text" json:"text"`
	Detail KeepDetail `bson:"detail" json:"detail"`
	State  string     `bson:"state" json:"state" jsonschema:"enum=asked,enum=kept,enum=declined"`
	// From says who offered it: MetaRush (a link the person typed, a file they
	// dropped, a logo a snapshot found) or Claude.
	From           string     `bson:"from" json:"from" jsonschema:"enum=metarush,enum=claude,enum=person"`
	ConversationID string     `bson:"conversationId,omitempty" json:"conversationId,omitempty"`
	ActionID       string     `bson:"actionId,omitempty" json:"actionId,omitempty"`
	AskedAt        time.Time  `bson:"askedAt" json:"askedAt"`
	DecidedAt      *time.Time `bson:"decidedAt,omitempty" json:"decidedAt,omitempty"`
	DecidedBy      *Actor     `bson:"decidedBy,omitempty" json:"decidedBy,omitempty"`
	// Outcome is what keeping did, in plain words.
	Outcome string `bson:"outcome,omitempty" json:"outcome,omitempty"`
}

// KeepDetail is what keeping needs, by kind.
type KeepDetail struct {
	URL        string `bson:"url,omitempty" json:"url,omitempty" jsonschema:"description=source: the address."`
	As         string `bson:"as,omitempty" json:"as,omitempty" jsonschema:"enum=,enum=page,enum=repo,enum=picture,enum=video,description=source: what the address is."`
	ResourceID string `bson:"resourceId,omitempty" json:"resourceId,omitempty" jsonschema:"description=picture: a resource already in the library (a logo a snapshot found)."`
	Attachment string `bson:"attachment,omitempty" json:"attachment,omitempty" jsonschema:"description=picture: a file dropped into the conversation (att_…)."`
	// File is that file: its name and bytes in the media store (by SHA-256).
	File     *KeepFile `bson:"file,omitempty" json:"file,omitempty"`
	BrandKit bool      `bson:"brandKit,omitempty" json:"brandKit,omitempty" jsonschema:"description=picture: also the project's brand kit logo (and colours)."`
	FromSite string    `bson:"fromSite,omitempty" json:"fromSite,omitempty" jsonschema:"description=picture: the website snapshot whose brand to use (its colours and font too)."`
	Person   string    `bson:"person,omitempty" json:"person,omitempty" jsonschema:"description=person: who the fact is about."`
	Goal     string    `bson:"goal,omitempty" json:"goal,omitempty"`
	Audience string    `bson:"audience,omitempty" json:"audience,omitempty"`
	Tone     string    `bson:"tone,omitempty" json:"tone,omitempty"`
	// Deliverables: where the video goes ("by email", "social shorts").
	Deliverables []Deliverable `bson:"deliverables,omitempty" json:"deliverables,omitempty"`
	Measure      string        `bson:"measure,omitempty" json:"measure,omitempty" jsonschema:"description=rule or check: what the quality check measures."`
	HeardAs      []string      `bson:"heardAs,omitempty" json:"heardAs,omitempty" jsonschema:"description=spelling: how transcripts write it instead."`
	Why          string        `bson:"why,omitempty" json:"why,omitempty" jsonschema:"description=Where it came from (what the person said)."`
}

// KeepFile is a file a person dropped into a conversation, in the media store.
type KeepFile struct {
	Name   string `bson:"name" json:"name"`
	SHA256 string `bson:"sha256" json:"sha256"`
	Size   int64  `bson:"size" json:"size"`
	MIME   string `bson:"mime" json:"mime"`
	Width  int    `bson:"width,omitempty" json:"width,omitempty"`
	Height int    `bson:"height,omitempty" json:"height,omitempty"`
}
