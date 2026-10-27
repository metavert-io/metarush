package models

import "time"

// CaptionTranslation is one caption line in another language (D238): the
// line as the transcript says it and its translation, kept per project and
// language. Claude Code translates on the person's own Claude plan (MetaRush
// makes no AI calls of its own), using the spelling list as its glossary;
// Machine marks a translation no person wrote. Lines are found again by
// their words (any version that shows the same line uses it) and by where
// they are said (a line fixed in the transcript since: the fix is carried
// into the translation when its words appear there unchanged, otherwise the
// line is said to need translating again).
type CaptionTranslation struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	ProjectID   string `bson:"projectId" json:"projectId"`
	// Language is the language it is in (a code such as es or pt-BR); From the
	// language of the line it translates.
	Language string `bson:"language" json:"language"`
	From     string `bson:"from,omitempty" json:"from,omitempty"`
	// Source is the caption line as the transcript said it; SourceHash its
	// SHA-256 (spaces evened out).
	Source     string `bson:"source" json:"source"`
	SourceHash string `bson:"sourceHash" json:"sourceHash"`
	Text       string `bson:"text" json:"text"`
	// Place is where the line's words are said: <footage or narration line>@<from>-<to> (seconds).
	Place string `bson:"place,omitempty" json:"place,omitempty"`
	// Machine: translated by Claude, not by a person.
	Machine    bool      `bson:"machine" json:"machine"`
	RevisionID string    `bson:"revisionId,omitempty" json:"revisionId,omitempty" jsonschema:"description=The version it was translated for."`
	By         Actor     `bson:"by" json:"by"`
	At         time.Time `bson:"at" json:"at"`
}
