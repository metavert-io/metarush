package models

import "time"

// Presenters (D273–D277, docs/presenters.md): a person's own presenter is a
// double kept at an AI service, made from their approved look, a training
// take of them speaking and their recorded consent naming that service; it
// reads lines on camera from MetaRush's own audio. Making, approving and
// retiring one are people only. Retiring it (or withdrawing the consent it
// relies on) deletes it at the service.

// PresenterPrefix is the ID prefix of a presenter (prs_…).
const PresenterPrefix = "prs"

// Presenter states.
const (
	// PresenterWaiting: recorded in MetaRush, waiting for the training take
	// (and, where the service asks, the person reading its consent words).
	PresenterWaiting = "waiting"
	// PresenterTraining: the service is making it.
	PresenterTraining = "training"
	// PresenterDraft: made; a test clip shows it, then a person approves it.
	PresenterDraft = "draft"
	// PresenterApproved: it reads lines in clips.
	PresenterApproved = "approved"
	// PresenterFailed: the service couldn't make it (Problem says why).
	PresenterFailed = "failed"
	// PresenterRetired: withdrawn; nothing more is made with it, and it is
	// deleted at the service (Removed says when).
	PresenterRetired = "retired"
)

// PresenterChallenge is a service's own consent request (D-ID's): the words
// the person reads on camera, with the code it checks, until when.
type PresenterChallenge struct {
	ID string `bson:"id" json:"id" jsonschema:"description=The service's ID of its consent request."`
	// Words are what the person reads, with their name put in.
	Words     string     `bson:"words" json:"words"`
	Language  string     `bson:"language,omitempty" json:"language,omitempty"`
	AskedAt   time.Time  `bson:"askedAt" json:"askedAt"`
	ExpiresAt *time.Time `bson:"expiresAt,omitempty" json:"expiresAt,omitempty"`
	Machine   string     `bson:"machine,omitempty" json:"machine,omitempty"`
}

// PresenterTake is a stretch of a clip a presenter is made from: the
// training take, or the person reading the service's consent words.
type PresenterTake struct {
	AssetID string  `bson:"assetId" json:"assetId"`
	Title   string  `bson:"title,omitempty" json:"title,omitempty"`
	From    float64 `bson:"from" json:"from"`
	To      float64 `bson:"to" json:"to"`
	Seconds float64 `bson:"seconds" json:"seconds"`
}

// PresenterTest is a test clip of a presenter (priced first, like any clip).
type PresenterTest struct {
	GenerationID string    `bson:"generationId" json:"generationId"`
	At           time.Time `bson:"at" json:"at"`
}

// Presenter is a person's own presenter at an AI service (D273).
type Presenter struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	// Name is a short name (lower case, unique): ada, ada-green.
	Name       string `bson:"name" json:"name" jsonschema:"description=Short name (lower case\\, unique): ada\\, ada-green."`
	Title      string `bson:"title" json:"title"`
	PersonID   string `bson:"personId" json:"personId"`
	PersonName string `bson:"personName" json:"personName"`
	// The approved look it is made from, pinned to its version.
	LookID      string `bson:"lookId" json:"lookId"`
	LookName    string `bson:"lookName" json:"lookName"`
	LookVersion string `bson:"lookVersion" json:"lookVersion"`
	// ConsentID is the consent it relies on (recorded, covering presenters,
	// naming the service: D274).
	ConsentID     string `bson:"consentId" json:"consentId"`
	Provider      string `bson:"provider" json:"provider"`
	ProviderTitle string `bson:"providerTitle" json:"providerTitle"`
	Model         string `bson:"model" json:"model"`
	// GreenScreen: the training take was filmed against a green screen, so
	// the service can put any colour behind the presenter.
	GreenScreen bool                `bson:"greenScreen" json:"greenScreen"`
	Challenge   *PresenterChallenge `bson:"challenge,omitempty" json:"challenge,omitempty"`
	Reading     *PresenterTake      `bson:"reading,omitempty" json:"reading,omitempty"`
	Take        *PresenterTake      `bson:"take,omitempty" json:"take,omitempty"`
	// TrainingID is the generation that made it at the service (kind presenter-make).
	TrainingID string `bson:"trainingId,omitempty" json:"trainingId,omitempty"`
	// ProviderID is the service's ID of the presenter, once made.
	ProviderID string          `bson:"providerId,omitempty" json:"providerId,omitempty"`
	Tests      []PresenterTest `bson:"tests,omitempty" json:"tests,omitempty"`
	State      string          `bson:"state" json:"state" jsonschema:"enum=waiting,enum=training,enum=draft,enum=approved,enum=failed,enum=retired"`
	Problem    string          `bson:"problem,omitempty" json:"problem,omitempty"`
	ApprovedBy *Actor          `bson:"approvedBy,omitempty" json:"approvedBy,omitempty"`
	ApprovedAt *time.Time      `bson:"approvedAt,omitempty" json:"approvedAt,omitempty"`
	RetiredBy  *Actor          `bson:"retiredBy,omitempty" json:"retiredBy,omitempty"`
	RetiredAt  *time.Time      `bson:"retiredAt,omitempty" json:"retiredAt,omitempty"`
	RetireNote string          `bson:"retireNote,omitempty" json:"retireNote,omitempty"`
	// RetireJobID is the job deleting it at the service, tried until done.
	RetireJobID string `bson:"retireJobId,omitempty" json:"retireJobId,omitempty"`
	// Removed records deleting it at the service (written once).
	Removed   *GenForgotten `bson:"removed,omitempty" json:"removed,omitempty"`
	CreatedBy Actor         `bson:"createdBy" json:"createdBy"`
	CreatedAt time.Time     `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time     `bson:"updatedAt" json:"updatedAt"`
}

// Usable reports whether a presenter may read lines: approved, or a draft
// for its test clips.
func (p *Presenter) Usable(test bool) bool {
	if p == nil || p.ProviderID == "" {
		return false
	}
	return p.State == PresenterApproved || (test && p.State == PresenterDraft)
}

// TrainingOptOut records that a person set an AI service's training opt-out
// for the account whose key a computer uses (D276): where a service trains
// its models on what it is sent unless told not to, a person's presenter is
// made there only with one of these.
type TrainingOptOut struct {
	ID            string `bson:"_id" json:"id"`
	WorkspaceID   string `bson:"workspaceId" json:"workspaceId"`
	Provider      string `bson:"provider" json:"provider"`
	ProviderTitle string `bson:"providerTitle" json:"providerTitle"`
	// KeyEnding is the key's last characters (masked), so another account's
	// key on another computer isn't taken for this one.
	KeyEnding string `bson:"keyEnding" json:"keyEnding"`
	// How says what the person did, in their words ("wrote to its privacy team on 25 Oct").
	How        string     `bson:"how" json:"how"`
	RecordedBy Actor      `bson:"recordedBy" json:"recordedBy"`
	RecordedAt time.Time  `bson:"recordedAt" json:"recordedAt"`
	Machine    string     `bson:"machine" json:"machine"`
	RemovedAt  *time.Time `bson:"removedAt,omitempty" json:"removedAt,omitempty"`
	RemovedBy  *Actor     `bson:"removedBy,omitempty" json:"removedBy,omitempty"`
}
