package models

import "time"

// A spoken change is a word or short phrase someone said, said again
// differently in their own voice copy, with their consent (D308–D312): what
// was said and what is said now, whose voice and consent, the speech made,
// how it was fitted and checked, and the versions it went into. The
// recording and its transcript are never changed; a cut plays the new sound
// over the stretch (mix.said).

// Spoken change states.
const (
	SpokenMaking    = "making"    // the speech is being made
	SpokenReady     = "ready"     // made, fitted and checked: a person can put it in the cut
	SpokenFailed    = "failed"    // it couldn't be made, or the check didn't pass (why says)
	SpokenPlaced    = "placed"    // a version of the cut plays it
	SpokenUndone    = "undone"    // taken out again: the cut plays what was said
	SpokenWithdrawn = "withdrawn" // the consent it relied on was withdrawn
)

// SpokenChange is one change's record (collection spoken_changes, swc_…).
type SpokenChange struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	ProjectID   string `bson:"projectId" json:"projectId"`
	// Branch is the cut it was asked for, and BaseRevision its version then.
	Branch       string `bson:"branch" json:"branch"`
	BaseRevision string `bson:"baseRevision,omitempty" json:"baseRevision,omitempty"`
	// AssetID is the recording; From and To the words that change (seconds).
	AssetID string  `bson:"assetId" json:"assetId"`
	From    float64 `bson:"from" json:"from"`
	To      float64 `bson:"to" json:"to"`
	// Before is what was said; Words what is said now.
	Before string `bson:"before" json:"before"`
	Words  string `bson:"words" json:"words"`
	// Context is the words sent to be spoken: the new words with the
	// speaker's words around them, so they sound like part of the sentence.
	Context string `bson:"context,omitempty" json:"context,omitempty"`
	// Speaker is whose voice it is and how MetaRush knows.
	Speaker SpokenSpeaker `bson:"speaker" json:"speaker"`
	// Voice is the voice copy that speaks (a voice-copy generation) and ConsentID
	// the consent that covers new words with its service.
	VoiceGeneration string `bson:"voiceGeneration" json:"voiceGeneration"`
	VoiceName       string `bson:"voiceName,omitempty" json:"voiceName,omitempty"`
	ConsentID       string `bson:"consentId" json:"consentId"`
	Provider        string `bson:"provider" json:"provider"`
	ProviderTitle   string `bson:"providerTitle,omitempty" json:"providerTitle,omitempty"`
	Model           string `bson:"model,omitempty" json:"model,omitempty"`
	// GenerationID is the speech made (its price, terms and what was sent),
	// JobID the job making it.
	GenerationID string  `bson:"generationId,omitempty" json:"generationId,omitempty"`
	JobID        string  `bson:"jobId,omitempty" json:"jobId,omitempty"`
	EstimateUSD  float64 `bson:"estimateUsd" json:"estimateUsd"`
	// Fit is how the new words were fitted into the stretch; Check how they were checked.
	Fit   *SpokenFit   `bson:"fit,omitempty" json:"fit,omitempty"`
	Check *SpokenCheck `bson:"check,omitempty" json:"check,omitempty"`
	// Sound is the new sound (a clip MetaRush made), ready to play over the stretch.
	Sound string `bson:"sound,omitempty" json:"sound,omitempty"`
	// Mouth is the stretch with the mouth matched to the new words, when a person asked (D312).
	Mouth *SpokenMouth `bson:"mouth,omitempty" json:"mouth,omitempty"`
	State string       `bson:"state" json:"state" jsonschema:"enum=making,enum=ready,enum=failed,enum=placed,enum=undone,enum=withdrawn"`
	// Why says, in plain words, why it failed or was withdrawn.
	Why string `bson:"why,omitempty" json:"why,omitempty"`
	// Placed lists the versions that put it in (and Undone the ones that took it out).
	Placed    []SpokenVersion `bson:"placed,omitempty" json:"placed,omitempty"`
	Undone    []SpokenVersion `bson:"undone,omitempty" json:"undone,omitempty"`
	CreatedBy Actor           `bson:"createdBy" json:"createdBy"`
	CreatedAt time.Time       `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time       `bson:"updatedAt" json:"updatedAt"`
}

// SpokenSpeaker is who says the words, as a person named them.
type SpokenSpeaker struct {
	PersonID string `bson:"personId" json:"personId"`
	Name     string `bson:"name" json:"name"`
	// Label is the voice in the recording (speaker_00…) a person named.
	Label string `bson:"label" json:"label"`
	// Match is how much the voice there sounds like the person's other
	// recordings (0–1; absent when there are none to compare).
	Match *float64 `bson:"match,omitempty" json:"match,omitempty"`
}

// SpokenFit is how the new words were fitted: the stretch the new sound
// covers (recording seconds, the recording's own sound at both ends), where
// the new words are heard, how much they were sped up, the level change and
// the room's own quiet laid under them.
type SpokenFit struct {
	From      float64 `bson:"from" json:"from"`
	To        float64 `bson:"to" json:"to"`
	WordsFrom float64 `bson:"wordsFrom" json:"wordsFrom"`
	WordsTo   float64 `bson:"wordsTo" json:"wordsTo"`
	// Speed is how much faster the new words play than they were made (1 = as made).
	Speed float64 `bson:"speed" json:"speed"`
	// GainDB is the level change that matches the speaker's level there.
	GainDB float64 `bson:"gainDb" json:"gainDb"`
	// RoomFrom and RoomTo are the quiet stretch of the recording laid under the new words.
	RoomFrom float64 `bson:"roomFrom" json:"roomFrom"`
	RoomTo   float64 `bson:"roomTo" json:"roomTo"`
}

// SpokenCheck is what the checks found: the words the recognizer on this
// computer heard in the new sound, and how much the new voice sounds like the
// person (TitaNet, 0–1) against the bar it had to reach.
type SpokenCheck struct {
	Heard       string   `bson:"heard" json:"heard"`
	WordsMatch  bool     `bson:"wordsMatch" json:"wordsMatch"`
	Voice       *float64 `bson:"voice,omitempty" json:"voice,omitempty"`
	VoiceNeeded float64  `bson:"voiceNeeded" json:"voiceNeeded"`
	VoiceMatch  bool     `bson:"voiceMatch" json:"voiceMatch"`
	// Seconds is how much of the speaker's voice the voice check heard.
	Seconds float64 `bson:"seconds" json:"seconds"`
	// WordsVoice is how much the stretch around the new words alone sounds
	// like the person (WordsSeconds of it, at least 1.5 s), against the bar
	// for that length: the new words can't pass on the words around them.
	WordsVoice   *float64  `bson:"wordsVoice,omitempty" json:"wordsVoice,omitempty"`
	WordsSeconds float64   `bson:"wordsSeconds,omitempty" json:"wordsSeconds,omitempty"`
	Passed       bool      `bson:"passed" json:"passed"`
	At           time.Time `bson:"at" json:"at"`
}

// SpokenMouth is the stretch with the mouth matched to the new words (lip sync).
type SpokenMouth struct {
	GenerationID string `bson:"generationId,omitempty" json:"generationId,omitempty"`
	JobID        string `bson:"jobId,omitempty" json:"jobId,omitempty"`
	AssetID      string `bson:"assetId,omitempty" json:"assetId,omitempty"`
	// State: making, ready, failed, or withdrawn (a consent the lip sync relied on was withdrawn).
	State string    `bson:"state" json:"state" jsonschema:"enum=making,enum=ready,enum=failed,enum=withdrawn"`
	Why   string    `bson:"why,omitempty" json:"why,omitempty"`
	By    Actor     `bson:"by" json:"by"`
	At    time.Time `bson:"at" json:"at"`
}

// SpokenVersion is a version that put a change in or took it out.
type SpokenVersion struct {
	RevisionID string    `bson:"revisionId" json:"revisionId"`
	Branch     string    `bson:"branch" json:"branch"`
	By         Actor     `bson:"by" json:"by"`
	At         time.Time `bson:"at" json:"at"`
}

// GeneratedSaid is what a clip of new words in someone's voice replaces
// (AssetSource.Generated.Said): the recording, the stretch and what was said.
type GeneratedSaid struct {
	ChangeID string  `bson:"changeId" json:"changeId"`
	AssetID  string  `bson:"assetId" json:"assetId"`
	From     float64 `bson:"from" json:"from"`
	To       float64 `bson:"to" json:"to"`
	Before   string  `bson:"before" json:"before"`
	Words    string  `bson:"words" json:"words"`
	Who      string  `bson:"who,omitempty" json:"who,omitempty"`
}
