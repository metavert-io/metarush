// Package aiwork runs every kind of AI work other than video through one
// lifecycle (D226), shared by every adapter: a model chosen in the open
// (Content Settings may pin one or forbid providers, D225) and priced first
// with up to two alternatives; consent for voices and faces (D227); the money
// set aside against the project's budget and the team's daily limit, the same
// ledger as generated video (D124); a generate job pinned to the machine with
// the key that saves the ticket before it waits; what comes back kept with how
// it was made (pictures in the resources library, sound as clips, a dub as a
// new clip, a transcript as the clip's words, a voice copy as a voice); and
// everything deleted at the provider after the download. Records are
// ordinary generation records (D125) with a kind, so lists, receipts and the
// activity bar read every kind the same way.
package aiwork

import (
	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
)

// MakeIn asks for AI work of one kind (or, with dryRun, only prices and
// checks it). Voice copies have their own, people-only operation
// (ai.voices.copy).
type MakeIn struct {
	Kind      string `json:"kind" jsonschema:"enum=picture,enum=voice,enum=voice-change,enum=sound-effect,enum=music,enum=dubbing,enum=transcription,enum=presenter,enum=background-removal,enum=upscale,enum=lip-sync,description=The kind of AI work."`
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$,description=The project it is for (its budget\\, its Content Settings\\, where the result goes)."`
	// Model is "provider/model" or a model's ID or title; default: the one
	// the project's Content Settings pin, else MetaRush's usual one.
	Model string `json:"model,omitempty" jsonschema:"maxLength=160,description=provider/model (or a model's name); default: the one Content Settings pin\\, else the usual one."`
	// Prompt is what to make (pictures, sound effects, music), the words to
	// say (voices) or how to change the pictures.
	Prompt string `json:"prompt,omitempty" jsonschema:"maxLength=10000,description=What to make\\, the words to say\\, or how to change the pictures."`
	// Pictures are resources-library pictures to change or refer to.
	Pictures []string `json:"pictures,omitempty" jsonschema:"maxItems=14,description=Pictures from the resources library (short name or name@version) to change or refer to."`
	// Clip is the recording to change, dub or write down.
	Clip string `json:"clip,omitempty" jsonschema:"pattern=^(ast_[0-9a-z]{26})?$,description=The clip to change the voice of\\, dub\\, write down or lip-sync."`
	// Sound is the new sound a clip's lips are matched to (lip sync, D279).
	Sound string   `json:"sound,omitempty" jsonschema:"pattern=^(ast_[0-9a-z]{26})?$,description=Lip sync: the clip whose sound the lips are matched to."`
	From  *float64 `json:"from,omitempty" jsonschema:"minimum=0,description=Where the part of the clip starts (seconds; default: its start)."`
	To    *float64 `json:"to,omitempty" jsonschema:"minimum=0,description=Where it ends (default: its end)."`
	// Options are the kind's settings.
	Options models.GenOptions `json:"options,omitempty"`
	// Presenter is who reads the sound (presenters, D273): a person's own
	// presenter (prs_… or its short name), or "stock:<id>" for one of a
	// service's stock presenters (with the service as the model). Default for
	// a clip dubbed from a presenter clip: the same presenter.
	Presenter string `json:"presenter,omitempty" jsonschema:"maxLength=120,description=Presenters: who reads it — a presenter (prs_… or its short name)\\, or stock:<id> for a service's stock presenter."`
	// Line is a narration line of the project's cut whose sound the
	// presenter reads (1 is the first line; instead of a clip).
	Line   int    `json:"line,omitempty" jsonschema:"minimum=0,maximum=10000,description=Presenters: a narration line of the project's cut to read (1 is the first)."`
	Branch string `json:"branch,omitempty" jsonschema:"maxLength=80,description=The cut whose narration line it is (default: the main one)."`
	// NoPeople: the person says the pictures show no real people.
	NoPeople bool     `json:"noPeople,omitempty" jsonschema:"description=You say the pictures show no real people (needed when MetaRush can't tell)."`
	MaxUSD   *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0,description=The most you agree to pay\\, in US dollars (from the estimate). Required unless your estimates are off (Settings → Preferences)."`
	DryRun   bool     `json:"dryRun,omitempty" jsonschema:"description=Price and check it only; nothing is sent or spent."`
	Origin   string   `json:"origin,omitempty" jsonschema:"maxLength=200,description=Where the request came from (a Claude panel card)."`
	// Use is the consent use the voice copy's person must have named, with
	// this service (new-words: a word they said changed, D309). Only Go
	// callers set it; the op's input never carries it.
	Use string `json:"-"`
	// Feature is the origin of work another feature asks for in Go (say:<change>),
	// whose result goes to that feature's keeper; ID, when set, is the
	// generation's ID, so the feature knows its work before any worker can
	// finish it. Never from the op's input: a request can't pass itself off
	// as a feature's (Origin, which it may carry, never reaches a keeper).
	Feature string `json:"-"`
	ID      string `json:"-"`
}

// CopyIn makes a copy of a person's voice, with their consent (D227).
// People only.
type CopyIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$"`
	Model     string `json:"model,omitempty" jsonschema:"maxLength=160,description=provider/model; default: the usual one."`
	// Person is whose voice it is (a name or ID from People); their consent must be in force.
	Person string `json:"person" jsonschema:"minLength=1,maxLength=120,description=Whose voice: a name or ID from People. Their consent must be recorded."`
	Name   string `json:"name" jsonschema:"minLength=1,maxLength=80,description=The new voice's name."`
	// Sample is a clip of clean speech of the voice.
	Sample string   `json:"sample" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=A clip with clean speech of the voice."`
	From   *float64 `json:"from,omitempty" jsonschema:"minimum=0"`
	To     *float64 `json:"to,omitempty" jsonschema:"minimum=0"`
	// ConsentClip is the person reading the provider's consent script (for
	// providers that want one).
	ConsentClip string   `json:"consentClip,omitempty" jsonschema:"pattern=^(ast_[0-9a-z]{26})?$,description=For services that want it: a clip of the same person reading the service's consent script."`
	ConsentFrom *float64 `json:"consentFrom,omitempty" jsonschema:"minimum=0"`
	ConsentTo   *float64 `json:"consentTo,omitempty" jsonschema:"minimum=0"`
	Language    string   `json:"language,omitempty" jsonschema:"maxLength=10,description=The language of the consent script (en-US)."`
	MaxUSD      *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0"`
	DryRun      bool     `json:"dryRun,omitempty"`
}

// ConsentView says whether the people a request relies on have consented.
type ConsentView struct {
	Needed     bool     `json:"needed"`
	OK         bool     `json:"ok"`
	People     []string `json:"people,omitempty"`
	ConsentIDs []string `json:"consentIds,omitempty"`
	Message    string   `json:"message,omitempty"`
}

// AIEstimate prices AI work before anything is spent, with the checks that
// decide whether it may go ahead.
type AIEstimate struct {
	// ProjectID is the project it is for and paid from ("" for none: the
	// team's daily limit only).
	ProjectID     string `json:"projectId,omitempty" jsonschema:"description=The project whose budget it is paid from."`
	Kind          string `json:"kind"`
	KindTitle     string `json:"kindTitle"`
	What          string `json:"what" jsonschema:"description=What it makes\\, in a few words: a picture\\, a piece of music."`
	Task          string `json:"task"`
	Provider      string `json:"provider"`
	ProviderTitle string `json:"providerTitle"`
	Model         string `json:"model"`
	ModelTitle    string `json:"modelTitle"`
	Stage         string `json:"stage"`
	// Choice says why this model: the usual one, asked for, or the project's Content Settings.
	Choice string `json:"choice" jsonschema:"enum=default,enum=asked,enum=content-settings"`
	// Alternatives are up to two other models: a cheaper one and a better ranked one.
	Alternatives []aiservices.Alternative `json:"alternatives"`
	USD          float64                  `json:"usd" jsonschema:"description=In US dollars."`
	Units        string                   `json:"units"`
	Rate         string                   `json:"rate"`
	PriceTable   string                   `json:"priceTable"`
	// Licence and data terms of the model and provider, as the catalogue says.
	Licence   aiservices.Licence `json:"licence"`
	Data      string             `json:"data"`
	Watermark string             `json:"watermark,omitempty"`
	RetiredOn string             `json:"retiredOn,omitempty"`
	// Inputs say what is sent, in plain words.
	Inputs   []string               `json:"inputs,omitempty"`
	Consent  ConsentView            `json:"consent"`
	Budget   generate.GenBudgetView `json:"budget"`
	KeyReady bool                   `json:"keyReady" jsonschema:"description=This computer has the provider's key (for a model on this computer: it is set up here)."`
	// KeyNeeded: the provider takes a key kept on this computer (not a model
	// here, nor an ability, which brings its own keys: D278, D281).
	KeyNeeded bool `json:"keyNeeded" jsonschema:"description=The provider takes a key kept on this computer."`
	// Here: the model runs on this computer — free, no key, nothing leaves it (D278).
	Here     bool           `json:"here,omitempty" jsonschema:"description=It runs on this computer: free\\, no key\\, and nothing leaves it."`
	Allowed  bool           `json:"allowed"`
	Problems []string       `json:"problems,omitempty"`
	Notes    []string       `json:"notes,omitempty"`
	Summary  string         `json:"summary"`
	Cost     costs.Estimate `json:"cost"`
}

// Work states as people read them (those of generated video).
const (
	StateWaiting   = generate.StateWaiting
	StatePreparing = "preparing"
	StateSending   = generate.StateSending
	StateMaking    = generate.StateMaking
	StateDone      = generate.StateDone
	StateFailed    = generate.StateFailed
	StateRefused   = generate.StateRefused
	StateCanceled  = generate.StateCanceled
)

// WorkOutput is one thing AI work made, where it is in MetaRush.
type WorkOutput struct {
	// Kind: clip (sound or a dub, in Sources), picture (in the resources
	// library), transcript (the clip's words), voice (a voice copy).
	Kind     string  `json:"kind" jsonschema:"enum=clip,enum=picture,enum=transcript,enum=voice,enum=presenter"`
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Duration float64 `json:"duration,omitempty"`
	Link     string  `json:"link,omitempty"`
}

// WorkView is a generation record of AI work with what people read about it.
type WorkView struct {
	models.Generation
	State     string                `json:"state" jsonschema:"enum=waiting,enum=preparing,enum=sending,enum=making,enum=done,enum=failed,enum=refused,enum=canceled"`
	KindTitle string                `json:"kindTitle"`
	Summary   string                `json:"summary"`
	CostUSD   float64               `json:"costUsd" jsonschema:"description=What it cost (or is estimated to cost while it runs)."`
	Outputs   []WorkOutput          `json:"outputs"`
	Job       *generate.GenJobBrief `json:"job,omitempty"`
	// ConsentRevoked: a consent it relied on was withdrawn since.
	ConsentRevoked bool `json:"consentRevoked,omitempty"`
}

// MakeOut is the result of asking for AI work.
type MakeOut struct {
	DryRun   bool       `json:"dryRun"`
	Estimate AIEstimate `json:"estimate"`
	Work     *WorkView  `json:"work,omitempty"`
	JobID    string     `json:"jobId,omitempty"`
	Summary  string     `json:"summary"`
}

// ListIn lists AI work.
type ListIn struct {
	Kind      string `json:"kind,omitempty" jsonschema:"enum=,enum=picture,enum=voice,enum=voice-copy,enum=voice-change,enum=sound-effect,enum=music,enum=dubbing,enum=transcription,enum=presenter,enum=presenter-make,enum=background-removal,enum=upscale,enum=lip-sync"`
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$"`
	Limit     int    `json:"limit,omitempty" jsonschema:"minimum=0,maximum=500"`
}

// ListOut is a list of AI work, newest first.
type ListOut struct {
	Work     []WorkView `json:"work"`
	SpentUSD float64    `json:"spentUsd" jsonschema:"description=What the listed work cost."`
	Summary  string     `json:"summary"`
}

// IDIn names one piece of AI work (a generation, or a clip or picture it made).
type IDIn struct {
	ID string `json:"id" jsonschema:"minLength=1,maxLength=80,description=The work (gen_…)\\, or a clip (ast_…) or picture (res_…) it made."`
}

// CancelOut is AI work after asking it to stop.
type CancelOut struct {
	Work    WorkView `json:"work"`
	Summary string   `json:"summary"`
}

// ModelsIn lists the models MetaRush knows.
type ModelsIn struct {
	Kind      string `json:"kind,omitempty" jsonschema:"enum=,enum=video,enum=picture,enum=voice,enum=voice-copy,enum=voice-change,enum=sound-effect,enum=music,enum=dubbing,enum=transcription,enum=presenter,enum=presenter-make,enum=background-removal,enum=upscale,enum=lip-sync"`
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$,description=Say what this project's Content Settings allow."`
}

// ModelView is one model with its price and terms.
type ModelView struct {
	aiservices.Model
	Default  bool   `json:"default"`
	Rate     string `json:"rate,omitempty" jsonschema:"description=Its price in words\\, with the day it was read."`
	Retired  bool   `json:"retired,omitempty"`
	KeyReady bool   `json:"keyReady"`
	Allowed  bool   `json:"allowed" jsonschema:"description=The project's Content Settings allow it."`
	Built    bool   `json:"built" jsonschema:"description=MetaRush can send work to it (an adapter exists)."`
	// NotHere says why a model that runs on this computer can't run here yet,
	// and how to set it up (D278).
	NotHere   string `json:"notHere,omitempty" jsonschema:"description=Why a model that runs on this computer can't run here yet."`
	PinnedFor bool   `json:"pinned,omitempty"`
}

// KindView is one kind of work and its models.
type KindView struct {
	aiservices.KindInfo
	Models []ModelView `json:"models"`
}

// ProviderView is a provider's terms.
type ProviderView struct {
	Name     string               `json:"name"`
	Title    string               `json:"title"`
	Route    string               `json:"route"`
	KeyReady bool                 `json:"keyReady"`
	Allowed  bool                 `json:"allowed"`
	Data     aiservices.DataTerms `json:"data"`
}

// ModelsOut is every kind of AI work with its models, prices and terms.
type ModelsOut struct {
	Kinds     []KindView     `json:"kinds"`
	Providers []ProviderView `json:"providers"`
	Summary   string         `json:"summary"`
}

// VoicesIn lists voice copies.
type VoicesIn struct {
	Person string `json:"person,omitempty" jsonschema:"maxLength=120,description=One person's (name or ID)."`
}

// VoiceView is a voice copy MetaRush made.
type VoiceView struct {
	GenerationID  string `json:"generationId"`
	Provider      string `json:"provider"`
	ProviderTitle string `json:"providerTitle"`
	Model         string `json:"model"`
	VoiceID       string `json:"voiceId"`
	Name          string `json:"name"`
	Person        string `json:"person"`
	// State: ready; withdrawing (withdrawn, being deleted at the provider by
	// a job retried until done, on a computer with the key); withdrawn
	// (deleted there).
	State   string `json:"state" jsonschema:"enum=ready,enum=withdrawing,enum=withdrawn"`
	Summary string `json:"summary"`
}

// VoicesOut lists voice copies, newest first.
type VoicesOut struct {
	Voices  []VoiceView `json:"voices"`
	Summary string      `json:"summary"`
}

// VoiceRemoveIn withdraws a voice copy: deleted at the provider. People only.
type VoiceRemoveIn struct {
	ID   string `json:"id" jsonschema:"minLength=1,maxLength=120,description=The voice copy: its work (gen_…) or the provider's voice ID."`
	Note string `json:"note,omitempty" jsonschema:"maxLength=500"`
}

// VoiceRemoveOut is a voice copy after withdrawing it.
type VoiceRemoveOut struct {
	Voice   VoiceView `json:"voice"`
	Summary string    `json:"summary"`
}

// RulesIn shows which AI services a project's work may go to.
type RulesIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$,description=The project (default: the default settings)."`
}

// RulesOut is the AI services part of the Content Settings a project uses.
type RulesOut struct {
	Rules models.AIServiceRules `json:"rules"`
	// Owner is the project whose settings they are, and its name.
	Owner     string `json:"owner,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
	Summary   string `json:"summary"`
}

// RulesSetIn changes the AI services part of a project's own Content
// Settings (without a project, the default's).
type RulesSetIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$"`
	// None: nothing of these projects goes to any AI service (false allows them again).
	None *bool `json:"none,omitempty" jsonschema:"description=Nothing goes to any AI service (false allows them again)."`
	// Forbid and Allow add providers to, or take them off, the forbidden list.
	Forbid []string `json:"forbid,omitempty" jsonschema:"maxItems=20"`
	Allow  []string `json:"allow,omitempty" jsonschema:"maxItems=20"`
	// ForbidKinds and AllowKinds add kinds of work to, or take them off, the
	// list of kinds never made for these projects (presenters, D277).
	ForbidKinds []string `json:"forbidKinds,omitempty" jsonschema:"maxItems=20,description=Kinds of AI work never made for these projects (presenter\\, music…)."`
	AllowKinds  []string `json:"allowKinds,omitempty" jsonschema:"maxItems=20"`
	// Pin sets the model for kinds of work ("kind=provider/model"); Unpin takes kinds' pins off.
	Pin   map[string]string `json:"pin,omitempty"`
	Unpin []string          `json:"unpin,omitempty" jsonschema:"maxItems=20"`
	Note  *string           `json:"note,omitempty" jsonschema:"maxLength=500"`
	// Clear takes every rule off.
	Clear bool `json:"clear,omitempty"`
}
