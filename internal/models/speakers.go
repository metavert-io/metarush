package models

import (
	"fmt"
	"strconv"
	"time"
)

// TimeSpan is a range in source seconds.
type TimeSpan struct {
	From float64 `bson:"from" json:"from"`
	To   float64 `bson:"to" json:"to"`
}

// SpeakersStatus summarizes speaker detection for an asset.
type SpeakersStatus struct {
	State    string `bson:"state" json:"state" jsonschema:"enum=running,enum=available,enum=unavailable,enum=failed"`
	Run      string `bson:"run,omitempty" json:"run,omitempty"`           // id of the diarization run the speakers come from
	Diarizer string `bson:"diarizer,omitempty" json:"diarizer,omitempty"` // models used
	// Hint is the number of people the user said speak (0 = detect automatically).
	Hint int `bson:"hint,omitempty" json:"hint,omitempty"`
	// Raw is how many clusters the diarizer produced before MetaRush repaired
	// over-segmentation; Count is the speakers kept (Minor brief voices extra).
	Raw   int      `bson:"raw" json:"raw"`
	Count int      `bson:"count" json:"count"`
	Minor int      `bson:"minor,omitempty" json:"minor,omitempty"`
	Named int      `bson:"named" json:"named"`
	Notes []string `bson:"notes,omitempty" json:"notes,omitempty"`
	// Fingerprints reports whether voice fingerprints were computed (needed to
	// recognize people across recordings).
	Fingerprints bool      `bson:"fingerprints" json:"fingerprints"`
	Error        string    `bson:"error,omitempty" json:"error,omitempty"`
	UpdatedAt    time.Time `bson:"updatedAt" json:"updatedAt"`
}

// Speaker label sources.
const (
	SpeakerByPerson = "person" // someone named or confirmed it
	SpeakerByAuto   = "auto"   // matched an enrolled person above the auto threshold
)

// SpeakerMatch is a candidate person for a voice.
type SpeakerMatch struct {
	PersonID string  `bson:"personId" json:"personId"`
	Name     string  `bson:"name" json:"name"`
	Score    float64 `bson:"score" json:"score" jsonschema:"description=Voice similarity: cosine from 0 to 1."`
}

// AssetSpeaker is one voice heard in one asset: a repaired diarization cluster
// with its fingerprint and samples. Speaker labels (speaker_00…) are per run;
// names and people links are what carry across recordings.
type AssetSpeaker struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	AssetID     string `bson:"assetId" json:"assetId"`
	Run         string `bson:"run" json:"run"`
	Label       string `bson:"label" json:"label"`
	Name        string `bson:"name,omitempty" json:"name,omitempty"`
	PersonID    string `bson:"personId,omitempty" json:"personId,omitempty"`
	// Source says who decided the name: a person, or automatic matching.
	Source string `bson:"source,omitempty" json:"source,omitempty" jsonschema:"enum=person,enum=auto"`
	// Naming says who gave a name that isn't automatic — a person, or
	// software acting for one (D274); consent checks count only a person's.
	Naming *Naming `bson:"naming,omitempty" json:"naming,omitempty"`
	Score  float64 `bson:"score,omitempty" json:"score,omitempty"`
	// Suggestions are plausible people when the voice is not named yet.
	Suggestions []SpeakerMatch `bson:"suggestions,omitempty" json:"suggestions,omitempty"`
	// Rejected are people someone said this voice is not.
	Rejected      []string   `bson:"rejected,omitempty" json:"rejected,omitempty"`
	Seconds       float64    `bson:"seconds" json:"seconds"`
	Fingerprinted float64    `bson:"fingerprinted" json:"fingerprinted"`
	Turns         int        `bson:"turns" json:"turns"`
	Words         int        `bson:"words" json:"words"`
	First         float64    `bson:"first" json:"first"`
	Minor         bool       `bson:"minor,omitempty" json:"minor,omitempty"`
	Samples       []TimeSpan `bson:"samples" json:"samples"`
	// Color is a stable palette index for this voice in this asset.
	Color int `bson:"color" json:"color"`
	// MergedInto is set when someone gave this voice the same name as another
	// voice of the asset (its words now carry that label).
	MergedInto string `bson:"mergedInto,omitempty" json:"mergedInto,omitempty"`
	// MergedBy says whether a person confirmed the merge ("person") or it
	// happened because both voices were recognized as the same person ("auto").
	MergedBy string `bson:"mergedBy,omitempty" json:"mergedBy,omitempty" jsonschema:"enum=person,enum=auto"`
	// Confirmed are spans a person said this voice speaks in (fixing who said a
	// line). They carry over when the voices are found again.
	Confirmed []TimeSpan `bson:"confirmed,omitempty" json:"confirmed,omitempty"`
	// Manual marks a voice a person created by fixing who said a line.
	Manual bool `bson:"manual,omitempty" json:"manual,omitempty"`
	// Pitch is the median voice pitch in Hz over Voiced seconds of voiced
	// speech (0 when unknown): a cheap check that two voices really are alike.
	Pitch  float64 `bson:"pitch,omitempty" json:"pitch,omitempty"`
	Voiced float64 `bson:"voiced,omitempty" json:"voiced,omitempty"`
	// Fingerprint (never sent to clients).
	Model     string      `bson:"model,omitempty" json:"model,omitempty"`
	Centroid  []float32   `bson:"centroid,omitempty" json:"-"`
	Exemplars [][]float32 `bson:"exemplars,omitempty" json:"-"`
	ExSeconds []float64   `bson:"exSeconds,omitempty" json:"-"`
	// Before is the voice this one was merged into, as it was before the merge
	// (so the merge can be undone). Never sent to clients.
	Before    *SpeakerBefore `bson:"before,omitempty" json:"-"`
	CreatedAt time.Time      `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time      `bson:"updatedAt" json:"updatedAt"`
}

// SpeakerBefore is what a merge changed on the voice that was kept.
type SpeakerBefore struct {
	Name          string         `bson:"name,omitempty"`
	PersonID      string         `bson:"personId,omitempty"`
	Source        string         `bson:"source,omitempty"`
	Score         float64        `bson:"score,omitempty"`
	Suggestions   []SpeakerMatch `bson:"suggestions,omitempty"`
	Rejected      []string       `bson:"rejected,omitempty"`
	Seconds       float64        `bson:"seconds"`
	Fingerprinted float64        `bson:"fingerprinted"`
	Turns         int            `bson:"turns"`
	Words         int            `bson:"words"`
	First         float64        `bson:"first"`
	Minor         bool           `bson:"minor,omitempty"`
	Samples       []TimeSpan     `bson:"samples"`
	Confirmed     []TimeSpan     `bson:"confirmed,omitempty"`
	Pitch         float64        `bson:"pitch,omitempty"`
	Voiced        float64        `bson:"voiced,omitempty"`
}

// DisplayName is the name, or "Speaker N" (1-based by first appearance).
func (s AssetSpeaker) DisplayName() string {
	if s.Name != "" {
		return s.Name
	}
	return SpeakerDisplayLabel(s.Label)
}

// SpeakerLabel is the label of the n-th voice (0-based): speaker_00, speaker_01…
func SpeakerLabel(n int) string { return fmt.Sprintf("speaker_%02d", n) }

// SpeakerDisplayLabel turns speaker_00 into "Speaker 1".
func SpeakerDisplayLabel(label string) string {
	n := 0
	for _, r := range label {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
		}
	}
	if label == "" {
		return ""
	}
	return "Speaker " + strconv.Itoa(n+1)
}

// VoiceExemplar is one enrolled embedding of a person and where it came from.
type VoiceExemplar struct {
	Source  string    `bson:"source" json:"source"` // <assetId>/<label>
	Vec     []float32 `bson:"v" json:"-"`
	Seconds float64   `bson:"s" json:"seconds"`
}

// Person is someone a workspace has named: their voice profile (several
// exemplars from every recording where a person confirmed them) lets future
// recordings be labeled automatically.
type Person struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	Name        string `bson:"name" json:"name"`
	NameKey     string `bson:"nameKey" json:"-"`
	// Recordings and Seconds count where the person speaks: voices a person
	// named plus voices recognized automatically. Exemplars come only from
	// voices a person named or confirmed.
	Recordings int             `bson:"recordings" json:"recordings"`
	Seconds    float64         `bson:"seconds" json:"seconds"`
	Exemplars  []VoiceExemplar `bson:"exemplars,omitempty" json:"-"`
	Centroid   []float32       `bson:"centroid,omitempty" json:"-"`
	Model      string          `bson:"model,omitempty" json:"model,omitempty"`
	// Notes are what people want MetaRush and Claude to know about this person:
	// who they are, how to name or title them, what to highlight or avoid.
	Notes string `bson:"notes,omitempty" json:"notes,omitempty" jsonschema:"maxLength=2000"`
	// Distinct lists people confirmed to be someone else (never offered as duplicates).
	Distinct   []string  `bson:"distinct,omitempty" json:"distinct,omitempty"`
	MergedInto string    `bson:"mergedInto,omitempty" json:"mergedInto,omitempty"`
	CreatedAt  time.Time `bson:"createdAt" json:"createdAt"`
	CreatedBy  Actor     `bson:"createdBy" json:"createdBy"`
	UpdatedAt  time.Time `bson:"updatedAt" json:"updatedAt"`
}

// MusicRights records licensing on a music or third-party audio asset. Edit
// specs may also carry rights per track; either satisfies the rights check.
type MusicRights struct {
	License     string     `bson:"license,omitempty" json:"license,omitempty" jsonschema:"maxLength=200"`
	Attribution string     `bson:"attribution,omitempty" json:"attribution,omitempty" jsonschema:"maxLength=500"`
	Source      string     `bson:"source,omitempty" json:"source,omitempty" jsonschema:"maxLength=500"`
	RecordedBy  *Actor     `bson:"recordedBy,omitempty" json:"recordedBy,omitempty"`
	RecordedAt  *time.Time `bson:"recordedAt,omitempty" json:"recordedAt,omitempty"`
}
