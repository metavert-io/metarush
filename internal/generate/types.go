// Package generate makes clips with generative-video providers (D123–D127):
// a shot from a prompt, a look and frames of real footage, new takes made the
// same way, extensions and edits. Every request is priced first and paid from
// a reserved budget (D124), runs as a durable job pinned to the machine with
// the key and is recorded once in the generations collection (D125); a
// person's likeness needs a look and that person's consent (D126); generated
// clips are ordinary clips marked as AI-generated everywhere (D127).
package generate

import (
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// FrameRef is a moment of a clip, or a picture from the resources library
// (a photo the person added: Generate video in the drop box, D154).
type FrameRef struct {
	AssetID string  `json:"assetId,omitempty" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	At      float64 `json:"at,omitempty" jsonschema:"minimum=0,description=Seconds into the clip."`
	// Picture is a resources-library picture (short name, name@version or
	// res_…) instead of a clip's frame.
	Picture string `json:"picture,omitempty" jsonschema:"maxLength=200,description=A picture from the resources library (name\\, name@version or ID) instead of a clip's frame."`
}

// GenShotIn asks for a new shot (or, with dryRun, only prices and checks it).
type GenShotIn struct {
	ProjectID   string    `json:"projectId" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	Prompt      string    `json:"prompt" jsonschema:"minLength=1,maxLength=4000,description=What the shot shows\\, in your words."`
	Look        string    `json:"look,omitempty" jsonschema:"maxLength=80,description=A look's short name or ID: who (or which character) appears."`
	Seconds     int       `json:"seconds,omitempty" jsonschema:"minimum=0,maximum=60,description=How long (the model's range; default 6)."`
	AspectRatio string    `json:"aspectRatio,omitempty" jsonschema:"enum=,enum=16:9,enum=9:16"`
	Quality     string    `json:"quality,omitempty" jsonschema:"enum=,enum=draft,enum=final,description=draft (cheaper\\, to try ideas) or final; default draft."`
	Resolution  string    `json:"resolution,omitempty" jsonschema:"enum=,enum=360p,enum=720p,enum=1080p,enum=4k,description=Overrides the quality's resolution."`
	Model       string    `json:"model,omitempty" jsonschema:"maxLength=120"`
	From        *FrameRef `json:"from,omitempty" jsonschema:"description=Start from this frame (continue the shot)."`
	To          *FrameRef `json:"to,omitempty" jsonschema:"description=End on this frame (with from: a shot between two frames)."`
	Images      []string  `json:"images,omitempty" jsonschema:"maxItems=10,description=Pictures from the resources library (short name or name@version)\\, referred to in the prompt as <IMAGE_REF_0>\\, <IMAGE_REF_1>…"`
	NoPeople    bool      `json:"noPeople,omitempty" jsonschema:"description=You say the frames and pictures show no real people (needed when MetaRush can't tell)."`
	Takes       int       `json:"takes,omitempty" jsonschema:"minimum=0,maximum=4,description=How many takes to make at once (default 1)."`
	MaxUSD      *float64  `json:"maxUsd,omitempty" jsonschema:"minimum=0,description=The most you agree to pay for all takes\\, in US dollars (from the estimate). Required to generate\\, unless your estimates are off (Settings → Preferences)."`
	DryRun      bool      `json:"dryRun,omitempty" jsonschema:"description=Price and check it only; nothing is sent or spent."`
	Origin      string    `json:"origin,omitempty" jsonschema:"maxLength=200,description=Where the request came from (a Claude panel card)."`
}

// GenAgainIn makes new takes of an earlier generation, made the same way
// (Generate again, Variations). Nothing is replaced: every take is a new clip.
type GenAgainIn struct {
	ID         string   `json:"id" jsonschema:"description=A generation (gen_…) or a generated clip (ast_…)."`
	Takes      int      `json:"takes,omitempty" jsonschema:"minimum=0,maximum=4,description=How many new takes (default 1; more is Variations)."`
	Quality    string   `json:"quality,omitempty" jsonschema:"enum=,enum=draft,enum=final"`
	Resolution string   `json:"resolution,omitempty" jsonschema:"enum=,enum=360p,enum=720p,enum=1080p,enum=4k"`
	MaxUSD     *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0"`
	DryRun     bool     `json:"dryRun,omitempty"`
	Origin     string   `json:"origin,omitempty" jsonschema:"maxLength=200"`
}

// GenExtendIn continues a clip (generated or real footage).
type GenExtendIn struct {
	ID         string   `json:"id" jsonschema:"description=A generation (gen_…) or a clip (ast_…) to continue."`
	Seconds    int      `json:"seconds,omitempty" jsonschema:"minimum=0,maximum=60,description=How many seconds to add (the model's range; default 6)."`
	Prompt     string   `json:"prompt,omitempty" jsonschema:"maxLength=4000,description=What happens next (default: the scene simply goes on)."`
	From       *float64 `json:"from,omitempty" jsonschema:"minimum=0,description=For a long clip: where the part to continue starts (default: its last seconds)."`
	ProjectID  string   `json:"projectId,omitempty" jsonschema:"description=The project the new clip goes to (default: the clip's)."`
	Quality    string   `json:"quality,omitempty" jsonschema:"enum=,enum=draft,enum=final"`
	Resolution string   `json:"resolution,omitempty" jsonschema:"enum=,enum=360p,enum=720p,enum=1080p,enum=4k"`
	NoPeople   bool     `json:"noPeople,omitempty"`
	MaxUSD     *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0"`
	DryRun     bool     `json:"dryRun,omitempty"`
	Origin     string   `json:"origin,omitempty" jsonschema:"maxLength=200"`
}

// GenEditIn changes a clip by an instruction ("make it night").
type GenEditIn struct {
	ID          string   `json:"id" jsonschema:"description=A generation (gen_…) or a clip (ast_…) to change."`
	Instruction string   `json:"instruction" jsonschema:"minLength=1,maxLength=4000"`
	From        *float64 `json:"from,omitempty" jsonschema:"minimum=0,description=For a long clip: where the part to change starts."`
	To          *float64 `json:"to,omitempty" jsonschema:"minimum=0,description=Where it ends (at most the model's limit after from)."`
	ProjectID   string   `json:"projectId,omitempty"`
	Quality     string   `json:"quality,omitempty" jsonschema:"enum=,enum=draft,enum=final"`
	Resolution  string   `json:"resolution,omitempty" jsonschema:"enum=,enum=360p,enum=720p,enum=1080p,enum=4k"`
	NoPeople    bool     `json:"noPeople,omitempty"`
	MaxUSD      *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0"`
	DryRun      bool     `json:"dryRun,omitempty"`
	Origin      string   `json:"origin,omitempty" jsonschema:"maxLength=200"`
}

// GenLimit is one spending limit and what counts against it.
type GenLimit struct {
	LimitUSD *float64 `json:"limitUsd,omitempty" jsonschema:"description=The limit (absent: no limit)."`
	SpentUSD float64  `json:"spentUsd"`
	HeldUSD  float64  `json:"heldUsd" jsonschema:"description=Set aside for shots being made."`
	LeftUSD  *float64 `json:"leftUsd,omitempty"`
	Default  bool     `json:"default" jsonschema:"description=The limit is MetaRush's default (nobody set one)."`
	Day      string   `json:"day,omitempty" jsonschema:"description=The UTC day (daily cap)."`
}

// GenBudgetView is a project's budget and today's cap.
type GenBudgetView struct {
	Project *GenLimit `json:"project,omitempty"`
	Today   GenLimit  `json:"today"`
}

// GenConsentView says whether the people a request shows have consented.
type GenConsentView struct {
	Needed     bool     `json:"needed"`
	OK         bool     `json:"ok"`
	People     []string `json:"people,omitempty"`
	ConsentIDs []string `json:"consentIds,omitempty"`
	Message    string   `json:"message,omitempty"`
}

// GenEstimate prices a request before anything is spent, with the checks
// that decide whether it may go ahead.
type GenEstimate struct {
	// ProjectID is the project whose budget it is paid from (a take's
	// parent's, a clip's own for an extension or an edit).
	ProjectID     string `json:"projectId,omitempty"`
	Provider      string `json:"provider"`
	ProviderTitle string `json:"providerTitle"`
	Model         string `json:"model"`
	ModelTitle    string `json:"modelTitle"`
	Stage         string `json:"stage"`
	Operation     string `json:"operation"`
	Task          string `json:"task"`
	Takes         int    `json:"takes"`
	Seconds       int    `json:"seconds"`
	Resolution    string `json:"resolution"`
	AspectRatio   string `json:"aspectRatio"`
	Quality       string `json:"quality"`
	// Upscaled: the model makes this resolution by upscaling.
	Upscaled       bool    `json:"upscaled,omitempty"`
	USD            float64 `json:"usd" jsonschema:"description=All takes\\, in US dollars."`
	PerTakeUSD     float64 `json:"perTakeUsd"`
	PerSecondUSD   float64 `json:"perSecondUsd"`
	PriceTable     string  `json:"priceTable"`
	VideoTokens    int64   `json:"videoTokens" jsonschema:"description=Per take."`
	InputTokens    int64   `json:"inputTokens"`
	ThinkingTokens int64   `json:"thinkingTokens"`
	Look           string  `json:"look,omitempty"`
	// References say what is sent with the prompt, in plain words.
	References []string       `json:"references,omitempty"`
	Likeness   bool           `json:"likeness" jsonschema:"description=It shows a real person's likeness."`
	Consent    GenConsentView `json:"consent"`
	Budget     GenBudgetView  `json:"budget"`
	KeyReady   bool           `json:"keyReady" jsonschema:"description=This computer has the provider's key."`
	Allowed    bool           `json:"allowed"`
	// Problems say, in plain words, why it can't go ahead.
	Problems []string `json:"problems,omitempty"`
	Notes    []string `json:"notes,omitempty"`
	Summary  string   `json:"summary"`
	// Cost is the estimate part by part (D208): the video, what is sent and
	// the thinking allowance, with the model, the source and each part's price.
	Cost costs.Estimate `json:"cost"`
}

// GenClip is a clip a generation made.
type GenClip struct {
	AssetID   string  `json:"assetId"`
	Title     string  `json:"title"`
	Duration  float64 `json:"duration"`
	ProjectID string  `json:"projectId,omitempty"`
	HasThumb  bool    `json:"hasThumb"`
	Link      string  `json:"link"`
}

// GenJobBrief is the job running a generation.
type GenJobBrief struct {
	ID       string          `json:"id"`
	State    string          `json:"state"`
	Progress models.Progress `json:"progress"`
}

// GenBrief names another generation (lineage).
type GenBrief struct {
	ID        string `json:"id"`
	Operation string `json:"operation"`
	Take      int    `json:"take"`
	State     string `json:"state"`
	Prompt    string `json:"prompt"`
}

// Generation states as people read them.
const (
	StateWaiting  = "waiting"
	StateSending  = "sending"
	StateMaking   = "making"
	StateDone     = models.GenDone
	StateFailed   = models.GenFailed
	StateRefused  = models.GenRefused
	StateCanceled = models.GenCanceled
)

// GenView is a generation record with what people read about it.
type GenView struct {
	models.Generation
	State   string       `json:"state" jsonschema:"enum=waiting,enum=sending,enum=making,enum=done,enum=failed,enum=refused,enum=canceled"`
	Summary string       `json:"summary"`
	CostUSD float64      `json:"costUsd" jsonschema:"description=What it cost (or is estimated to cost while it runs)."`
	Clips   []GenClip    `json:"clips"`
	Job     *GenJobBrief `json:"job,omitempty"`
	// On show: where its clips are used, where it came from, what came from it.
	UsedIn   []ops.UseRow `json:"usedIn,omitempty"`
	Parent   *GenBrief    `json:"parent,omitempty"`
	Children []GenBrief   `json:"children,omitempty"`
	// ConsentRevoked: a consent it relied on was revoked since.
	ConsentRevoked bool `json:"consentRevoked,omitempty"`
}

// GenRunOut is the result of asking for generations.
type GenRunOut struct {
	DryRun      bool        `json:"dryRun"`
	Estimate    GenEstimate `json:"estimate"`
	Generations []GenView   `json:"generations"`
	JobIDs      []string    `json:"jobIds"`
	Summary     string      `json:"summary"`
}

// GenListIn lists generations.
type GenListIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=One project's (default: all)."`
	Look      string `json:"look,omitempty" jsonschema:"description=Only ones with this look."`
	AssetID   string `json:"assetId,omitempty" jsonschema:"description=The generation that made this clip\\, and its takes."`
	Limit     int    `json:"limit,omitempty" jsonschema:"minimum=0,maximum=500"`
}

// GenListOut is a list of generations, newest first.
type GenListOut struct {
	Generations []GenView `json:"generations"`
	SpentUSD    float64   `json:"spentUsd" jsonschema:"description=What the listed generations cost."`
	Summary     string    `json:"summary"`
}

// GenIDIn names one generation (or a clip one made).
type GenIDIn struct {
	ID string `json:"id" jsonschema:"description=A generation (gen_…) or a generated clip (ast_…)."`
}

// GenCancelOut is a generation after asking it to stop.
type GenCancelOut struct {
	Generation GenView `json:"generation"`
	Summary    string  `json:"summary"`
}

// GenModelView is one model with its prices.
type GenModelView struct {
	ID           string             `json:"id"`
	Title        string             `json:"title"`
	Stage        string             `json:"stage"`
	Describe     string             `json:"describe"`
	Default      bool               `json:"default"`
	MinSeconds   int                `json:"minSeconds"`
	MaxSeconds   int                `json:"maxSeconds"`
	Resolutions  []string           `json:"resolutions"`
	Upscaled     []string           `json:"upscaled,omitempty"`
	AspectRatios []string           `json:"aspectRatios"`
	PerSecondUSD map[string]float64 `json:"perSecondUsd" jsonschema:"description=Price of one second of video by resolution."`
	PriceTable   string             `json:"priceTable"`
	RetiredOn    string             `json:"retiredOn,omitempty"`
	Watermark    string             `json:"watermark,omitempty"`
}

// GenSettingsIn asks for the generative-video settings (and a project's budget).
type GenSettingsIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$"`
}

// GenSettingsOut is what new shots use and what may be spent.
type GenSettingsOut struct {
	Provider        string         `json:"provider"`
	ProviderTitle   string         `json:"providerTitle"`
	KeyReady        bool           `json:"keyReady"`
	Models          []GenModelView `json:"models"`
	Model           string         `json:"model"`
	DraftResolution string         `json:"draftResolution"`
	FinalResolution string         `json:"finalResolution"`
	Seconds         int            `json:"seconds"`
	AspectRatio     string         `json:"aspectRatio"`
	DailyCapUSD     float64        `json:"dailyCapUsd"`
	DailyCapDefault bool           `json:"dailyCapDefault"`
	// ProjectBudgetUSD is the project's limit (absent: no limit); Default: nobody set one.
	ProjectBudgetUSD     *float64 `json:"projectBudgetUsd,omitempty"`
	ProjectBudgetDefault bool     `json:"projectBudgetDefault"`
	// Who set the daily limit and the project's budget, and when (D241: Auto
	// spends only within limits a person set); empty when not recorded.
	DailyCapBy      *models.SetBy `json:"dailyCapBy,omitempty"`
	ProjectBudgetBy *models.SetBy `json:"projectBudgetBy,omitempty"`
	Budget          GenBudgetView `json:"budget"`
	// SpendLimitsPer10Minutes are the provider's own limits by usage tier.
	SpendLimitsPer10Minutes []float64 `json:"spendLimitsPer10Minutes,omitempty"`
	Summary                 string    `json:"summary"`
}

// GenConfigureIn changes the generative-video settings. People only: these
// are spending decisions.
type GenConfigureIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$"`
	// DailyCapUSD is the most the team may spend in a UTC day (workspace).
	DailyCapUSD *float64 `json:"dailyCapUsd,omitempty" jsonschema:"minimum=1,maximum=1000"`
	// BudgetUSD is the project's limit; 0 removes the limit (the daily cap still applies).
	BudgetUSD *float64 `json:"budgetUsd,omitempty" jsonschema:"minimum=0,maximum=100000"`
	// ClearBudget puts the project back on the default limit.
	ClearBudget     bool   `json:"clearBudget,omitempty"`
	Model           string `json:"model,omitempty" jsonschema:"maxLength=120"`
	DraftResolution string `json:"draftResolution,omitempty" jsonschema:"enum=,enum=360p,enum=720p"`
	FinalResolution string `json:"finalResolution,omitempty" jsonschema:"enum=,enum=1080p,enum=4k"`
}
