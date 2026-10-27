package models

import (
	"time"

	"github.com/metavert-io/metarush/internal/editspec"
)

// ProviderSettings are the non-secret choices for an external provider (for
// ElevenLabs: the voice, the model and voice settings), shared through MongoDB.
// Workspace settings are the team default; a project's settings override them;
// a narration line's own voice overrides both. API keys are never stored here —
// they live in each machine's keychain.
type ProviderSettings struct {
	VoiceID   string                     `bson:"voiceId,omitempty" json:"voiceId,omitempty"`
	VoiceName string                     `bson:"voiceName,omitempty" json:"voiceName,omitempty"`
	ModelID   string                     `bson:"modelId,omitempty" json:"modelId,omitempty"`
	ModelName string                     `bson:"modelName,omitempty" json:"modelName,omitempty"`
	Voice     *editspec.TTSVoiceSettings `bson:"voice,omitempty" json:"voice,omitempty"`
	// Region selects the provider's data-residency endpoint (ElevenLabs: us, eu,
	// in, sg; empty = the default global endpoint). Workspace scope.
	Region string `bson:"region,omitempty" json:"region,omitempty" jsonschema:"enum=,enum=us,enum=eu,enum=in,enum=sg"`
	// Favorites are voices the team marked (workspace scope).
	Favorites []FavoriteVoice `bson:"favorites,omitempty" json:"favorites,omitempty"`
	// BudgetCredits caps what a project may spend with the provider (project
	// scope; credits ≈ characters for most ElevenLabs models). UsedCredits is
	// what MetaRush has spent so far, updated atomically after each generation.
	BudgetCredits *float64 `bson:"budgetCredits,omitempty" json:"budgetCredits,omitempty"`
	UsedCredits   float64  `bson:"usedCredits,omitempty" json:"usedCredits,omitempty"`
	// Generative video (D124), in US dollars: a project's budget (project
	// scope) and the most the whole team may spend in a day (workspace scope;
	// a project may set a lower one). Spending itself is kept in generation_spend.
	BudgetUSD   *float64 `bson:"budgetUsd,omitempty" json:"budgetUsd,omitempty"`
	DailyCapUSD *float64 `bson:"dailyCapUsd,omitempty" json:"dailyCapUsd,omitempty"`
	// Who set the project's budget, the team's daily limit and the project's
	// budget in credits, and when (D241): Auto spends only within limits a
	// person set. Absent on limits set before MetaRush kept who.
	BudgetBy        *SetBy `bson:"budgetBy,omitempty" json:"budgetBy,omitempty"`
	DailyCapBy      *SetBy `bson:"dailyCapBy,omitempty" json:"dailyCapBy,omitempty"`
	BudgetCreditsBy *SetBy `bson:"budgetCreditsBy,omitempty" json:"budgetCreditsBy,omitempty"`
	// The model and resolutions new shots use (generative video).
	Model           string    `bson:"model,omitempty" json:"model,omitempty"`
	DraftResolution string    `bson:"draftResolution,omitempty" json:"draftResolution,omitempty" jsonschema:"enum=,enum=360p,enum=720p"`
	FinalResolution string    `bson:"finalResolution,omitempty" json:"finalResolution,omitempty" jsonschema:"enum=,enum=1080p,enum=4k"`
	UpdatedAt       time.Time `bson:"updatedAt,omitempty" json:"updatedAt,omitempty"`
	UpdatedBy       *Actor    `bson:"updatedBy,omitempty" json:"updatedBy,omitempty"`
}

// FavoriteVoice is a voice the team marked as a favorite.
type FavoriteVoice struct {
	VoiceID string `bson:"voiceId" json:"voiceId"`
	Name    string `bson:"name,omitempty" json:"name,omitempty"`
}

// VoiceTakePrefix is the ID prefix of voiceover takes (tk_…).
const VoiceTakePrefix = "tk"

// Take sources.
const (
	TakeScratch  = "scratch"
	TakeTTS      = "tts"
	TakeRecorded = "recorded"
	TakeImported = "imported"
)

// VoiceTake is one piece of narration audio made for (or found for) a line of
// the script: a range of an ordinary audio asset plus how it was made. Takes
// are listed per line; the chosen one is referenced by the beat in the spec.
type VoiceTake struct {
	ID          string    `bson:"_id" json:"id"`
	WorkspaceID string    `bson:"workspaceId" json:"workspaceId"`
	ProjectID   string    `bson:"projectId" json:"projectId"`
	BeatID      string    `bson:"beatId,omitempty" json:"beatId,omitempty"`
	Text        string    `bson:"text" json:"text"`
	TextHash    string    `bson:"textHash" json:"textHash"`
	Source      string    `bson:"source" json:"source" jsonschema:"enum=scratch,enum=tts,enum=recorded,enum=imported"`
	AssetID     string    `bson:"assetId" json:"assetId"`
	In          float64   `bson:"in" json:"in"`
	Out         float64   `bson:"out" json:"out"`
	Duration    float64   `bson:"duration" json:"duration"`
	Words       int       `bson:"words" json:"words"`
	WPM         float64   `bson:"wpm,omitempty" json:"wpm,omitempty"`
	AIVoice     bool      `bson:"aiVoice" json:"aiVoice"`
	Provider    string    `bson:"provider,omitempty" json:"provider,omitempty"`
	VoiceID     string    `bson:"voiceId,omitempty" json:"voiceId,omitempty"`
	VoiceName   string    `bson:"voiceName,omitempty" json:"voiceName,omitempty"`
	ModelID     string    `bson:"modelId,omitempty" json:"modelId,omitempty"`
	SettingsKey string    `bson:"settingsKey,omitempty" json:"settingsKey,omitempty"` // canonical voice settings (part of the cache key)
	CacheKey    string    `bson:"cacheKey,omitempty" json:"cacheKey,omitempty"`       // sha256(provider|model|voice|settings|text)
	Characters  int       `bson:"characters,omitempty" json:"characters,omitempty"`
	Credits     float64   `bson:"credits,omitempty" json:"credits,omitempty"`
	Timing      string    `bson:"timing" json:"timing" jsonschema:"enum=word,enum=estimated,enum=none"` // word timings: provider alignment / transcript, estimated, none
	Match       *float64  `bson:"match,omitempty" json:"match,omitempty"`                               // share of the line's words heard in it (recordings)
	Cleanups    []Cleanup `bson:"cleanups,omitempty" json:"cleanups,omitempty"`
	Note        string    `bson:"note,omitempty" json:"note,omitempty"`
	CreatedBy   Actor     `bson:"createdBy" json:"createdBy"`
	CreatedAt   time.Time `bson:"createdAt" json:"createdAt"`
	// A piece of a live read (D85): one recording made while the cut played.
	// ReadID groups the pieces of one read; Pin is where this line was spoken
	// (its start point when the take plays where it was recorded); Planned is
	// where the line was meant to start before the read placed it (what "Snap
	// lines to their planned spots" goes back to).
	ReadID  string           `bson:"readId,omitempty" json:"readId,omitempty"`
	Pin     *editspec.Anchor `bson:"pin,omitempty" json:"pin,omitempty"`
	Planned *editspec.Anchor `bson:"planned,omitempty" json:"planned,omitempty"`
	// PinAt is where the line was spoken on the timeline of RecordedOn (seconds).
	PinAt      *float64 `bson:"pinAt,omitempty" json:"pinAt,omitempty"`
	RecordedOn string   `bson:"recordedOn,omitempty" json:"recordedOn,omitempty" jsonschema:"description=The version of the cut it was recorded against."`
}

// ReadPrefix is the ID prefix of live reads (rd_…).
const ReadPrefix = "rd"

// Cleanup is a processed version of a take's asset (voice cleanup preset).
type Cleanup struct {
	Preset    string    `bson:"preset" json:"preset"`
	AssetID   string    `bson:"assetId" json:"assetId"`
	Offset    float64   `bson:"offset" json:"offset"` // seconds trimmed from the start (silence trim)
	Filters   string    `bson:"filters" json:"filters"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
}

// SetBy is who set something and when.
type SetBy struct {
	UserID   string    `bson:"userId" json:"userId"`
	UserName string    `bson:"userName,omitempty" json:"userName,omitempty"`
	Via      string    `bson:"via,omitempty" json:"via,omitempty"`
	At       time.Time `bson:"at" json:"at"`
}

// SetNow is a's actor setting something now.
func SetNow(a Actor) *SetBy {
	return &SetBy{UserID: a.UserID, UserName: a.UserName, Via: a.Via, At: time.Now().UTC()}
}
