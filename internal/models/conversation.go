package models

import (
	"time"

	"github.com/metavert-io/metarush/internal/editspec"
)

// ConversationPrefix starts a conversation's ID (D99). (Claude Code
// conversations in the dashboard are assistant_conversations, "asc_".)
const ConversationPrefix = "cnv"

// Conversation is several recordings of one talk aligned on one clock (D99,
// docs/conversations.md): one per person from an online call, or per-person
// sound plus a shared picture. Cuts pin what they render with
// (spec.conversations); this record is the library's copy, which changes: a
// nudge, a new alignment, clean-up suggestions, colour measurements.
type Conversation struct {
	ID          string   `bson:"_id" json:"id"`
	WorkspaceID string   `bson:"workspaceId" json:"workspaceId"`
	ProjectIDs  []string `bson:"projectIds" json:"projectIds"`
	Name        string   `bson:"name" json:"name"`
	// Members are the recordings, in order (the first is the reference unless
	// the sync says another).
	Members []ConversationMember `bson:"members" json:"members"`
	// Length is the conversation's length on its clock (seconds).
	Length float64           `bson:"length" json:"length"`
	Sync   *ConversationSync `bson:"sync,omitempty" json:"sync,omitempty"`
	// Source is where it came from (an import of a Zoom, Riverside or
	// SquadCast folder); nil when someone picked the recordings.
	Source *ConversationSource `bson:"source,omitempty" json:"source,omitempty"`
	// Cleanup holds the clean-up suggestions found and what people decided.
	Cleanup   []CleanupSuggestion `bson:"cleanup,omitempty" json:"cleanup,omitempty"`
	CleanupAt *time.Time          `bson:"cleanupAt,omitempty" json:"cleanupAt,omitempty"`
	// Style and Layout are the last style and split layout people chose;
	// MaxShot is that style's longest shot of one person (seconds) and
	// Interviewer the member it keeps off camera (interview style), which QA
	// checks the cut against.
	Style       string    `bson:"style,omitempty" json:"style,omitempty"`
	Layout      string    `bson:"layout,omitempty" json:"layout,omitempty"`
	MaxShot     float64   `bson:"maxShot,omitempty" json:"maxShot,omitempty"`
	Interviewer string    `bson:"interviewer,omitempty" json:"interviewer,omitempty"`
	CreatedAt   time.Time `bson:"createdAt" json:"createdAt"`
	CreatedBy   Actor     `bson:"createdBy" json:"createdBy"`
	UpdatedAt   time.Time `bson:"updatedAt" json:"updatedAt"`
	// Version counts changes (compare-and-swap for concurrent edits).
	Version int `bson:"version" json:"version"`
}

// ConversationMember is one recording of a conversation, with what a cut pins
// for it and how it was aligned.
type ConversationMember struct {
	editspec.ConversationMember `bson:",inline" json:",inline"`
	Align                       *MemberAlign `bson:"align,omitempty" json:"align,omitempty"`
	// Color is the measured picture (for webcams that match) and the look
	// fitted to the reference member.
	Color *MemberColor `bson:"color,omitempty" json:"color,omitempty"`
}

// MemberAlign is how a member's place on the conversation's clock was found.
type MemberAlign struct {
	// Method: reference (the others were aligned to it), mix (against a
	// recording with everyone in it), two-way (each microphone picked up the
	// other; the midpoint), one-way, together (files taken to start
	// together), app (a call app's files that start together), clap, mark
	// (moments a person picked), manual or clock (a screen recording's
	// tracks, measured on the browser's clock while recording, D259).
	Method     string  `bson:"method" json:"method" jsonschema:"enum=reference,enum=mix,enum=two-way,enum=one-way,enum=together,enum=app,enum=clap,enum=mark,enum=manual,enum=clock"`
	Confidence float64 `bson:"confidence" json:"confidence" jsonschema:"minimum=0,maximum=1"`
	// Against is the member it was aligned to.
	Against string `bson:"against,omitempty" json:"against,omitempty"`
	// Delay is the call's delay between the two (seconds), when two-way pick-up showed it.
	Delay float64 `bson:"delay,omitempty" json:"delay,omitempty"`
	// Windows and Agree: how many 30-second windows were measured and how
	// many agreed with the fitted line (within a frame); Residual their spread (ms).
	Windows  int     `bson:"windows,omitempty" json:"windows,omitempty"`
	Agree    int     `bson:"agree,omitempty" json:"agree,omitempty"`
	Residual float64 `bson:"residual,omitempty" json:"residual,omitempty"`
	// Nudged is the manual adjustment on top of what was found (seconds).
	Nudged float64   `bson:"nudged,omitempty" json:"nudged,omitempty"`
	At     time.Time `bson:"at" json:"at"`
}

// MemberColor is a member's picture measured (mean and spread of red, green
// and blue, 0–1, over frames every 10 s) and the fitted look.
type MemberColor struct {
	Mean   [3]float64     `bson:"mean" json:"mean"`
	Spread [3]float64     `bson:"spread" json:"spread"`
	Frames int            `bson:"frames" json:"frames"`
	Faces  bool           `bson:"faces" json:"faces" jsonschema:"description=Measured over the face box (else the middle of the picture)."`
	Look   *editspec.Look `bson:"look,omitempty" json:"look,omitempty"`
	// DeltaE is the mean colour difference to the reference before and after
	// the look (CIE76, Lab); Light the brightness difference (L*).
	DeltaEBefore float64   `bson:"deltaEBefore" json:"deltaEBefore"`
	DeltaEAfter  float64   `bson:"deltaEAfter" json:"deltaEAfter"`
	LightBefore  float64   `bson:"lightBefore" json:"lightBefore"`
	LightAfter   float64   `bson:"lightAfter" json:"lightAfter"`
	At           time.Time `bson:"at" json:"at"`
}

// ConversationSync is the conversation's alignment as a whole.
type ConversationSync struct {
	Reference string `bson:"reference" json:"reference" jsonschema:"description=The member the others were aligned to."`
	Method    string `bson:"method" json:"method"`
	// Confidence is the least sure member's.
	Confidence float64   `bson:"confidence" json:"confidence"`
	Search     float64   `bson:"search,omitempty" json:"search,omitempty" jsonschema:"description=Seconds searched either way."`
	Took       float64   `bson:"took,omitempty" json:"took,omitempty" jsonschema:"description=Seconds the alignment took."`
	At         time.Time `bson:"at" json:"at"`
	By         Actor     `bson:"by" json:"by"`
}

// ConversationSource is the folder a conversation was imported from.
type ConversationSource struct {
	Provider string `bson:"provider" json:"provider" jsonschema:"enum=zoom,enum=riverside,enum=squadcast,enum=folder,enum=recorder"`
	Folder   string `bson:"folder,omitempty" json:"folder,omitempty" jsonschema:"description=The folder's name (not its path)."`
	// Aligned: the provider's files start together (Zoom's separate audio
	// since 2021, Riverside's aligned tracks).
	Aligned bool `bson:"aligned,omitempty" json:"aligned,omitempty"`
}

// CleanupSuggestion is something in a call worth cutting or fixing (D103).
type CleanupSuggestion struct {
	ID   string `bson:"id" json:"id"`
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=freeze,enum=dropout,enum=dead-air,enum=call-talk,enum=chrome"`
	// From and To are on the conversation's clock (seconds).
	From   float64 `bson:"from" json:"from"`
	To     float64 `bson:"to" json:"to"`
	Member string  `bson:"member,omitempty" json:"member,omitempty"`
	// Text says it in plain words; Quote is what was said (call talk).
	Text  string `bson:"text" json:"text"`
	Quote string `bson:"quote,omitempty" json:"quote,omitempty"`
	// Fix is what accepting does: cut (take the stretch out), switch (show
	// someone else over it), crop (the member's crop) or flag (nothing to
	// do automatically).
	Fix  string         `bson:"fix" json:"fix" jsonschema:"enum=cut,enum=switch,enum=crop,enum=flag"`
	Crop *editspec.Crop `bson:"crop,omitempty" json:"crop,omitempty"`
	// Keep is how much of a cut stretch stays (dead air shortened, not removed).
	Keep  float64    `bson:"keep,omitempty" json:"keep,omitempty"`
	State string     `bson:"state" json:"state" jsonschema:"enum=open,enum=accepted,enum=skipped"`
	By    *Actor     `bson:"by,omitempty" json:"by,omitempty"`
	At    *time.Time `bson:"at,omitempty" json:"at,omitempty"`
	// Revision is the version accepting it made.
	Revision string `bson:"revision,omitempty" json:"revision,omitempty"`
}

// Pin is the conversation as a cut renders it (spec.conversations).
func (c *Conversation) Pin() editspec.ConversationPin {
	p := editspec.ConversationPin{ID: c.ID}
	for _, m := range c.Members {
		p.Members = append(p.Members, m.ConversationMember)
	}
	return p
}

// Member finds a member by short name.
func (c *Conversation) Member(id string) (*ConversationMember, int) {
	for i := range c.Members {
		if c.Members[i].ID == id {
			return &c.Members[i], i
		}
	}
	return nil, -1
}
