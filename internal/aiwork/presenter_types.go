package aiwork

import (
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// Presenters (D273–D277): a person's own presenter is begun (the provider's
// consent words asked for), made from a training take and the person reading
// those words (priced first), tested, approved and, when the person changes
// their mind, retired — each step a person's. Clips of a presenter reading
// MetaRush's own sound are ordinary AI work (ai.make kind presenter).

// PresenterStartIn begins a person's own presenter. People only.
type PresenterStartIn struct {
	// Person is whose presenter it is (a name or ID from People).
	Person string `json:"person,omitempty" jsonschema:"maxLength=120,description=Whose presenter: a name or ID from People."`
	// Again asks the service for new consent words for a presenter still
	// waiting for its training take (the words expire).
	Again string `json:"again,omitempty" jsonschema:"maxLength=80,description=Ask for new consent words for this presenter (still waiting)."`
	// Look is the person's approved look it is made from (default: their newest approved look).
	Look string `json:"look,omitempty" jsonschema:"maxLength=80,description=The person's approved look (default: their newest)."`
	// Name is a short name (default: from the person's name); Title how it is shown.
	Name  string `json:"name,omitempty" jsonschema:"maxLength=40"`
	Title string `json:"title,omitempty" jsonschema:"maxLength=80"`
	// Model is the service and model (provider/model); default: the usual one.
	Model string `json:"model,omitempty" jsonschema:"maxLength=160,description=provider/model (default: the one Content Settings choose\\, else the usual one)."`
	// ProjectID: whose Content Settings decide which services may be used (default: the default settings).
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$"`
	// GreenScreen: the training take will be filmed against a green screen
	// (so a colour can be put behind the presenter).
	GreenScreen bool `json:"greenScreen,omitempty"`
	// Language is the language of the service's consent words (default en).
	Language string `json:"language,omitempty" jsonschema:"maxLength=10"`
}

// PresenterMakeIn sends a presenter's training take and the person reading
// the service's consent words. People only; priced first.
type PresenterMakeIn struct {
	ID string `json:"id" jsonschema:"minLength=1,maxLength=80,description=The presenter (prs_… or its short name)."`
	// Take is a clip of the person speaking to the camera, on their own.
	Take     string   `json:"take" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=A clip of the person speaking to the camera\\, on their own."`
	TakeFrom *float64 `json:"takeFrom,omitempty" jsonschema:"minimum=0"`
	TakeTo   *float64 `json:"takeTo,omitempty" jsonschema:"minimum=0"`
	// Reading is the person reading the service's consent words (for
	// services that check consent themselves): the consent recording itself
	// (the default), with the part where they read them (D274).
	Reading     string   `json:"reading,omitempty" jsonschema:"pattern=^(ast_[0-9a-z]{26})?$,description=The consent recording\\, where the person reads the service's consent words (the default; no other recording is taken)."`
	ReadingFrom *float64 `json:"readingFrom,omitempty" jsonschema:"minimum=0"`
	ReadingTo   *float64 `json:"readingTo,omitempty" jsonschema:"minimum=0"`
	// Consent is the consent it relies on (default: the newest in force that
	// covers presenters and names the service).
	Consent string `json:"consent,omitempty" jsonschema:"pattern=^(cst_[0-9a-z]{26})?$"`
	// ShowsPerson: the person says the take shows the presenter's person —
	// counted only where MetaRush hasn't looked for faces in it; once it has,
	// every face must be named as the person (D274).
	ShowsPerson bool     `json:"showsPerson,omitempty" jsonschema:"description=You say the take shows the person (counted only where MetaRush hasn't looked for faces in it)."`
	ProjectID   string   `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$,description=The project whose budget pays (default: the team's daily limit only)."`
	MaxUSD      *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0"`
	DryRun      bool     `json:"dryRun,omitempty"`
}

// PresenterIDIn names a presenter.
type PresenterIDIn struct {
	ID string `json:"id" jsonschema:"minLength=1,maxLength=80,description=The presenter (prs_… or its short name)."`
}

// PresenterDecideIn approves or retires a presenter. People only.
type PresenterDecideIn struct {
	ID   string `json:"id" jsonschema:"minLength=1,maxLength=80"`
	Note string `json:"note,omitempty" jsonschema:"maxLength=500"`
}

// PresenterListIn lists presenters.
type PresenterListIn struct {
	Person string `json:"person,omitempty" jsonschema:"maxLength=120,description=One person's (name or ID)."`
	// Retired: list retired ones too.
	Retired bool `json:"retired,omitempty"`
}

// PresenterView is a presenter with what people read about it.
type PresenterView struct {
	models.Presenter
	Summary string `json:"summary"`
	// Words are what the person reads on camera for the service's consent
	// check, until WordsUntil (while it waits for its training take).
	Words      string     `json:"words,omitempty"`
	WordsUntil *time.Time `json:"wordsUntil,omitempty"`
	// Next says what to do next, in plain words.
	Next string `json:"next,omitempty"`
	// TakeAdvice says how to film the training take.
	TakeAdvice string `json:"takeAdvice,omitempty"`
	// Erasure is the service's own route for erasing what it keeps of a face
	// (a step only the person can take, shown when it is retired).
	Erasure        string `json:"erasure,omitempty"`
	ConsentSummary string `json:"consentSummary,omitempty"`
	// Clips counts the clips it read; UsedIn are the cuts that use them (show only).
	Clips    int          `json:"clips"`
	UsedIn   []ops.UseRow `json:"usedIn,omitempty"`
	Training *WorkView    `json:"training,omitempty"`
	Link     string       `json:"link"`
}

// PresenterListOut lists presenters, newest first.
type PresenterListOut struct {
	Presenters []PresenterView `json:"presenters"`
	Summary    string          `json:"summary"`
}

// PresenterOut is a presenter after a step.
type PresenterOut struct {
	Presenter PresenterView `json:"presenter"`
	// Estimate is what making it costs (presenter.make).
	Estimate *AIEstimate `json:"estimate,omitempty"`
	DryRun   bool        `json:"dryRun,omitempty"`
	JobID    string      `json:"jobId,omitempty"`
	// Stopped are clips waiting to be made with it that were stopped (retire).
	Stopped []string `json:"stopped,omitempty"`
	Summary string   `json:"summary"`
}

// PresenterTestIn makes a test clip of a presenter not yet approved. People only; priced first.
type PresenterTestIn struct {
	ID        string   `json:"id" jsonschema:"minLength=1,maxLength=80"`
	Clip      string   `json:"clip" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=The sound it reads: a clip (AI speech in the person's voice copy\\, or a recording of them)."`
	From      *float64 `json:"from,omitempty" jsonschema:"minimum=0"`
	To        *float64 `json:"to,omitempty" jsonschema:"minimum=0"`
	ProjectID string   `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$"`
	MaxUSD    *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0"`
	DryRun    bool     `json:"dryRun,omitempty"`
}

// PresenterStockIn lists a service's stock presenters.
type PresenterStockIn struct {
	Provider string `json:"provider,omitempty" jsonschema:"maxLength=40,description=The service (default: every service with presenters)."`
}

// PresenterStockService is one service's stock presenters and their licence.
type PresenterStockService struct {
	Provider      string                      `json:"provider"`
	ProviderTitle string                      `json:"providerTitle"`
	Licence       aiservices.StockLicence     `json:"licence"`
	Offered       bool                        `json:"offered" jsonschema:"description=Their licence covers commercial use and client work."`
	KeyReady      bool                        `json:"keyReady"`
	Presenters    []aiservices.StockPresenter `json:"presenters"`
	Note          string                      `json:"note,omitempty"`
}

// PresenterStockOut lists stock presenters by service.
type PresenterStockOut struct {
	Services []PresenterStockService `json:"services"`
	Summary  string                  `json:"summary"`
}

// PresenterOptOutIn records (or takes back) that a service's training opt-out is
// set for the account whose key this computer uses (D276). People only.
type PresenterOptOutIn struct {
	Provider string `json:"provider" jsonschema:"minLength=1,maxLength=40"`
	// How says what was done, in the person's words.
	How    string `json:"how,omitempty" jsonschema:"maxLength=500,description=What you did\\, in your words (wrote to its privacy team on 25 Oct)."`
	Remove bool   `json:"remove,omitempty" jsonschema:"description=Take the record back (the opt-out isn't set any more)."`
}

// PresenterOptOutOut is a service's training opt-out record.
type PresenterOptOutOut struct {
	Provider      string                 `json:"provider"`
	ProviderTitle string                 `json:"providerTitle"`
	Trains        string                 `json:"trains" jsonschema:"description=What the service's terms say: no\\, unless-opted-out\\, yes."`
	OptOut        *models.TrainingOptOut `json:"optOut,omitempty"`
	Summary       string                 `json:"summary"`
}
