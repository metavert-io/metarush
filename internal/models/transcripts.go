package models

import "time"

// Transcript fixes (D145–D149, docs/transcripts.md): a person's corrections to
// a clip's transcript, the machine's own transcript kept beside them, the
// spelling lists and the transcript state each saved version of a cut uses.

// What a fix changed.
const (
	FixWords   = "words"   // words retyped, joined or split (by hand, find and replace or the spelling list)
	FixSpeaker = "speaker" // who says a stretch
	// FixTranscribed records that the clip was transcribed again (a new machine
	// transcript; people's fixes carried over where they could be). Not a fix
	// of its own and never undone.
	FixTranscribed = "transcribed"
)

// Where a fix came from.
const (
	FixByHand     = "hand"     // typed by a person (the transcript, a caption, the CLI)
	FixByReplace  = "replace"  // find and replace
	FixBySpelling = "spelling" // the spelling list corrected the words
	FixCarried    = "carried"  // carried over to a new transcript of the clip (transcribed again)
	FixByMachine  = "machine"  // the clip was transcribed again
)

// TranscriptFix is one change to one stretch of a clip's transcript. A find
// and replace or a spelling list makes one per place, sharing a batch. Fixes
// are never deleted: undoing one records who undid it and when.
type TranscriptFix struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	AssetID     string `bson:"assetId" json:"assetId"`
	// ProjectID is the project the fix was made from, when there was one
	// (spelling suggestions are per project).
	ProjectID string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	// Rev is the clip's transcript revision this fix made (TranscriptState.Rev).
	Rev int `bson:"rev" json:"rev"`
	// Base is the machine transcript it changed (1; 2 after the clip was transcribed again…).
	Base   int    `bson:"base" json:"base"`
	Kind   string `bson:"kind" json:"kind" jsonschema:"enum=words,enum=speaker,enum=transcribed"`
	Source string `bson:"source" json:"source" jsonschema:"enum=hand,enum=replace,enum=spelling,enum=carried,enum=machine"`
	// BatchID groups the fixes of one change (a retyped passage, a find and
	// replace, one use of the spelling list); undo can take back the whole batch.
	BatchID string `bson:"batchId" json:"batchId"`
	// From and To are where the stretch is in the clip (source seconds).
	From float64 `bson:"from" json:"from"`
	To   float64 `bson:"to" json:"to"`
	// Before and After are the words (words fixes). Lines without word timing
	// carry their words with estimated times and Line set.
	Before     []Word `bson:"before,omitempty" json:"before,omitempty"`
	After      []Word `bson:"after,omitempty" json:"after,omitempty"`
	BeforeText string `bson:"beforeText" json:"beforeText"`
	AfterText  string `bson:"afterText" json:"afterText"`
	// Line: the stretch is in a line without word timing (Before and After
	// carry estimated times).
	Line bool `bson:"line,omitempty" json:"line,omitempty"`
	// Speaker is set for speaker fixes.
	Speaker *SpeakerFix `bson:"speaker,omitempty" json:"speaker,omitempty"`
	// Query is what was searched for (find and replace) or the spelling list's
	// word ("FernBrook"), so history can say "Fern Brook → FernBrook".
	Query string `bson:"query,omitempty" json:"query,omitempty"`
	// CarriedFrom is the fix this one carried over from an earlier transcript.
	CarriedFrom string    `bson:"carriedFrom,omitempty" json:"carriedFrom,omitempty"`
	By          Actor     `bson:"by" json:"by"`
	At          time.Time `bson:"at" json:"at"`
	// Undone is set once the fix was taken back.
	Undone *FixUndone `bson:"undone,omitempty" json:"undone,omitempty"`
	// Superseded: the clip was transcribed again and this fix could not be
	// carried over (the new transcript's words there are different).
	Superseded bool `bson:"superseded,omitempty" json:"superseded,omitempty"`
}

// FixUndone says who took a fix back, when, and the transcript revision that made.
type FixUndone struct {
	Rev int       `bson:"rev" json:"rev"`
	By  Actor     `bson:"by" json:"by"`
	At  time.Time `bson:"at" json:"at"`
}

// SpeakerFix is who says a stretch: the voices it had and the one it was given.
type SpeakerFix struct {
	// Before are the stretch's words by who said them before the fix.
	Before []SpeakerRun `bson:"before" json:"before"`
	// Label is the voice the words were given (speaker_00…); Name the person's
	// name ("" for someone not named yet).
	Label    string `bson:"label" json:"label"`
	Name     string `bson:"name,omitempty" json:"name,omitempty"`
	PersonID string `bson:"personId,omitempty" json:"personId,omitempty"`
	Unsure   bool   `bson:"unsure,omitempty" json:"unsure,omitempty"`
	// Run is the detection of voices the fix was made on (finding the voices
	// again makes it final).
	Run string `bson:"run,omitempty" json:"-"`
	// Voices are the clip's voices as they were before the fix, without their
	// fingerprints (to put them back when the fix is undone).
	Voices []VoiceBefore `bson:"voices,omitempty" json:"-"`
}

// SpeakerRun is a stretch of words said by one voice.
type SpeakerRun struct {
	From  float64 `bson:"from" json:"from"`
	To    float64 `bson:"to" json:"to"`
	Label string  `bson:"label" json:"label"`
	Name  string  `bson:"name,omitempty" json:"name,omitempty"`
}

// VoiceBefore is what undoing a speaker fix needs of a voice: who it was and
// where a person confirmed it speaks.
type VoiceBefore struct {
	ID          string         `bson:"id" json:"id"`
	Label       string         `bson:"label" json:"label"`
	Name        string         `bson:"name,omitempty" json:"name,omitempty"`
	PersonID    string         `bson:"personId,omitempty" json:"personId,omitempty"`
	Source      string         `bson:"source,omitempty" json:"source,omitempty"`
	Score       float64        `bson:"score,omitempty" json:"score,omitempty"`
	Suggestions []SpeakerMatch `bson:"suggestions,omitempty" json:"suggestions,omitempty"`
	Rejected    []string       `bson:"rejected,omitempty" json:"rejected,omitempty"`
	Confirmed   []TimeSpan     `bson:"confirmed,omitempty" json:"confirmed,omitempty"`
	Manual      bool           `bson:"manual,omitempty" json:"manual,omitempty"`
	Color       int            `bson:"color" json:"color"`
	Run         string         `bson:"run,omitempty" json:"run,omitempty"`
	MergedInto  string         `bson:"mergedInto,omitempty" json:"mergedInto,omitempty"`
	// Naming is who had named it (D274): an undo gives it back with the name.
	Naming *Naming `bson:"naming,omitempty" json:"naming,omitempty"`
}

// TranscriptState is where a clip's transcript stands with people's fixes
// (kept apart from TranscriptStatus, which transcription rewrites).
type TranscriptState struct {
	// Rev goes up with every fix, undo and new machine transcript; saved
	// versions of a cut record the revision they used (TranscriptPin).
	Rev int `bson:"rev" json:"rev"`
	// Base is which machine transcript the clip has (1 = the first one kept).
	Base int `bson:"base" json:"base"`
	// Fixes counts the fixes in force (not undone).
	Fixes     int       `bson:"fixes" json:"fixes"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	// Carry is what happened to the fixes the last time the clip was
	// transcribed again.
	Carry *CarryReport `bson:"carry,omitempty" json:"carry,omitempty"`
	// Lock keeps two fixes to the same clip from overlapping (never sent).
	Lock *TranscriptLock `bson:"lock,omitempty" json:"-"`
}

// TranscriptLock is a short lease on a clip's transcript while a fix is written.
type TranscriptLock struct {
	Token string    `bson:"token"`
	Until time.Time `bson:"until"`
}

// CarryReport says which fixes made it to a new transcript of the clip.
type CarryReport struct {
	At      time.Time   `bson:"at" json:"at"`
	Carried int         `bson:"carried" json:"carried"`
	Already int         `bson:"already" json:"already" jsonschema:"description=Fixes the new transcript already had right."`
	Missed  []CarryMiss `bson:"missed" json:"missed"`
}

// CarryMiss is a fix that could not be carried over.
type CarryMiss struct {
	FixID     string  `bson:"fixId" json:"fixId"`
	From      float64 `bson:"from" json:"from"`
	To        float64 `bson:"to" json:"to"`
	AfterText string  `bson:"afterText" json:"afterText"`
	// Heard is what the new transcript says there.
	Heard string `bson:"heard" json:"heard"`
}

// TranscriptPin is the transcript a saved version of a cut uses for one clip:
// the clip's transcript revision then, and the hash of the words the version
// was made with (a snapshot keeps them).
type TranscriptPin struct {
	AssetID string `bson:"assetId" json:"assetId"`
	Rev     int    `bson:"rev" json:"rev"`
	Hash    string `bson:"hash" json:"hash"`
}

// SpellingEntry is one word on a spelling list: how a name or term is always
// written (D147). A project's list holds its own entries; entries without a
// project are the whole workspace's.
type SpellingEntry struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	ProjectID   string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	// Word is how it is always written: "FernBrook", "Elmwood", "Maple Grove".
	Word string `bson:"word" json:"word"`
	// Key is the word in plain letters (unique per list): "fernbrook", "maplegrove".
	Key string `bson:"key" json:"key"`
	// HeardAs are other ways a transcript writes it that differ by more than
	// case, spaces and punctuation ("Fernbrooke"); those are always found anyway.
	HeardAs   []string  `bson:"heardAs,omitempty" json:"heardAs,omitempty"`
	Note      string    `bson:"note,omitempty" json:"note,omitempty"`
	AddedBy   Actor     `bson:"addedBy" json:"addedBy"`
	AddedAt   time.Time `bson:"addedAt" json:"addedAt"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	// FromFixes is how many fixes suggested it, when it came from a suggestion.
	FromFixes int `bson:"fromFixes,omitempty" json:"fromFixes,omitempty"`
}
