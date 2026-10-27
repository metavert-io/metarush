package models

import (
	"strings"
	"time"
)

// Generative video (D123–D127, docs/generative-video.md): clips made by a
// provider (Google Gemini first) from a prompt, a look and frames of real
// footage. Every generation is priced first, paid from a reserved budget,
// recorded once and never changed, and its clips are marked as AI-generated
// everywhere.

// ID prefixes.
const (
	GenerationPrefix = "gen" // gen_… a generation record
	LookPrefix       = "lok" // lok_… a look (a person's or a character's)
	ConsentPrefix    = "cst" // cst_… a consent record
)

// PlatformGenerated marks a clip a generative-video provider made (AssetSource.Platform).
const PlatformGenerated = "generated"

// SourceGenerated is how a generated clip was obtained (AssetSource.Method).
const SourceGenerated = "generated"

// TagAIGenerated marks AI-generated clips (asset tags).
const TagAIGenerated = "ai-generated"

// GeneratedSource is what made an AI-generated clip: the provider, the model
// and the generation record that says how it was made (AssetSource.Generated).
type GeneratedSource struct {
	GenerationID  string `bson:"generationId" json:"generationId"`
	Provider      string `bson:"provider" json:"provider"`
	ProviderTitle string `bson:"providerTitle" json:"providerTitle"`
	Model         string `bson:"model" json:"model"`
	ModelTitle    string `bson:"modelTitle,omitempty" json:"modelTitle,omitempty"`
	Operation     string `bson:"operation" json:"operation" jsonschema:"enum=shot,enum=again,enum=variation,enum=extend,enum=edit,enum=look-test,enum=make"`
	// Kind is the kind of AI work that made it (D226): video (or empty, made
	// before), voice, sound-effect, music, voice-change, dubbing.
	Kind string `bson:"kind,omitempty" json:"kind,omitempty"`
	Take int    `bson:"take,omitempty" json:"take,omitempty"`
	// Prompt is the prompt as the person (or Claude) wrote it, shortened.
	Prompt string `bson:"prompt,omitempty" json:"prompt,omitempty"`
	// Look names the look it shows (a person's or a character's), when one was used.
	Look string `bson:"look,omitempty" json:"look,omitempty"`
	// Likeness says it shows a real person's likeness (a person look, or frames of them).
	Likeness bool `bson:"likeness,omitempty" json:"likeness,omitempty"`
	// Presenter names the presenter who reads the lines (D273): a person's
	// own (prs_…, with their consent) or a provider's stock presenter.
	Presenter      string `bson:"presenter,omitempty" json:"presenter,omitempty"`
	PresenterTitle string `bson:"presenterTitle,omitempty" json:"presenterTitle,omitempty"`
	// Consent is the consent record a person's likeness in it relies on.
	Consent string `bson:"consent,omitempty" json:"consent,omitempty"`
	// Watermark is the provider's invisible watermark ("SynthID").
	Watermark string `bson:"watermark,omitempty" json:"watermark,omitempty"`
	// Said is what a clip of new words in someone's own voice replaces (D308).
	Said *GeneratedSaid `bson:"said,omitempty" json:"said,omitempty"`
}

// IsGenerated reports whether a source is an AI-generated clip.
func (s *AssetSource) IsGenerated() bool {
	return s != nil && (s.Platform == PlatformGenerated || s.Generated != nil)
}

// GeneratedLine is a generated clip's provenance in one line: "AI-generated with Google Gemini · take 2",
// "AI music made with ElevenLabs".
func (s *AssetSource) GeneratedLine() string {
	if !s.IsGenerated() {
		return ""
	}
	g := s.Generated
	parts := []string{"AI-generated"}
	if g != nil {
		if w := genKindWords[g.Kind]; w != "" {
			parts[0] = w + " made"
		}
		if g.ProviderTitle != "" {
			parts[0] += " with " + g.ProviderTitle
		}
	}
	if g != nil && g.PresenterTitle != "" {
		parts = append(parts, g.PresenterTitle)
	}
	if g != nil && g.Take > 1 {
		parts = append(parts, "take "+itoa(g.Take))
	}
	return strings.Join(parts, " · ")
}

// genKindWords say what an AI-generated clip is, by its kind of work.
var genKindWords = map[string]string{"voice": "AI speech", "sound-effect": "An AI sound effect", "music": "AI music",
	"voice-change": "AI voice change", "dubbing": "An AI dub", "presenter": "An AI presenter"}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// Generation operations.
const (
	GenShot      = "shot"      // a new shot from a prompt (and a look, frames, pictures)
	GenAgain     = "again"     // made the same way again: a new take
	GenVariation = "variation" // one of several takes made the same way, asked for at once
	GenExtend    = "extend"    // a continuation of a clip
	GenEdit      = "edit"      // a clip changed by an instruction
	GenLookTest  = "look-test" // a take for a look's test sheet
	GenMake      = "make"      // AI work of another kind than video (D226): a picture, speech, music…
)

// GenKindVideo is generated video's kind of work (Generation.Kind; records made
// before D226 have none). The other kinds are internal/aiservices'.
const GenKindVideo = "video"

// VideoKinds matches generation records of generated video: made before
// D226 (no kind) or of kind video.
var VideoKinds = []any{nil, "", GenKindVideo}

// KindOf is a generation's kind of work (video for records made before D226).
func (g *Generation) KindOf() string {
	if g.Kind == "" {
		return GenKindVideo
	}
	return g.Kind
}

// GenOptions are an AI request's settings, as asked for (D226); each kind of
// work reads its own.
type GenOptions struct {
	// Pictures: the shape (16:9, 1:1…), the size (1K, 2K, 4K) and the file type (image/png, image/jpeg).
	AspectRatio string `bson:"aspectRatio,omitempty" json:"aspectRatio,omitempty"`
	Size        string `bson:"size,omitempty" json:"size,omitempty"`
	Format      string `bson:"format,omitempty" json:"format,omitempty"`
	// Larger pictures: how many times wider and taller (2 or 4; D279).
	Scale int `bson:"scale,omitempty" json:"scale,omitempty"`
	// Sound effects and music: how long, in seconds; a sound effect that
	// loops; music without singing.
	Seconds      float64 `bson:"seconds,omitempty" json:"seconds,omitempty"`
	Loop         bool    `bson:"loop,omitempty" json:"loop,omitempty"`
	Instrumental bool    `bson:"instrumental,omitempty" json:"instrumental,omitempty"`
	// Voices and voice change: the provider's voice (a voice's name or ID)
	// and, for voices that take one, how to say it.
	Voice string `bson:"voice,omitempty" json:"voice,omitempty"`
	Style string `bson:"style,omitempty" json:"style,omitempty"`
	// Voices, transcription and dubbing: the language spoken (a code such as
	// en or en-US; empty: the provider finds out); dubbing: the language to
	// dub into.
	Language       string `bson:"language,omitempty" json:"language,omitempty"`
	TargetLanguage string `bson:"targetLanguage,omitempty" json:"targetLanguage,omitempty"`
	// Dubbing: how many people speak (0: the provider finds out); the
	// speakers' own voices (each needs consent) instead of similar voices.
	Speakers  int  `bson:"speakers,omitempty" json:"speakers,omitempty"`
	OwnVoices bool `bson:"ownVoices,omitempty" json:"ownVoices,omitempty"`
	// Transcription: when each word is said (MetaRush's transcripts need it).
	WordTimes bool `bson:"wordTimes,omitempty" json:"wordTimes,omitempty"`
	// Voice copies: the new voice's name and the language of the consent script.
	Name          string `bson:"name,omitempty" json:"name,omitempty"`
	ConsentLocale string `bson:"consentLocale,omitempty" json:"consentLocale,omitempty"`
	// Region is the provider's place for the data (ElevenLabs: us, eu, in, sg; "" its default).
	Region string `bson:"region,omitempty" json:"region,omitempty"`
	// Presenters (D273): who reads the lines (the provider's ID of a person's
	// presenter, or of a stock presenter), what is behind them (a colour
	// "#rrggbb"; "" the provider's own), and, when a presenter is made, its
	// training take was filmed against a green screen and the provider's
	// consent request it answers.
	Presenter   string `bson:"presenter,omitempty" json:"presenter,omitempty"`
	Background  string `bson:"background,omitempty" json:"background,omitempty"`
	Stock       bool   `bson:"stock,omitempty" json:"stock,omitempty"`
	GreenScreen bool   `bson:"greenScreen,omitempty" json:"greenScreen,omitempty"`
	ConsentID   string `bson:"consentId,omitempty" json:"consentId,omitempty"`
}

// GenTerms are the licence and data terms of the model a generation used, as
// the catalogue said when it was asked for (D225): they travel with what it
// made, into delivery notes and receipts.
type GenTerms struct {
	Licence    string `bson:"licence" json:"licence"`
	Commercial string `bson:"commercial" json:"commercial" jsonschema:"enum=yes,enum=no,enum=plan,enum=unstated"`
	Broadcast  string `bson:"broadcast" json:"broadcast" jsonschema:"enum=yes,enum=no,enum=plan,enum=unstated"`
	Data       string `bson:"data" json:"data"`
	Source     string `bson:"source,omitempty" json:"source,omitempty"`
	Checked    string `bson:"checked,omitempty" json:"checked,omitempty"`
}

// GenChoice says why a generation used its model (D225): the usual one, a
// person's or Claude's choice, or the project's Content Settings; and what
// else the estimate offered.
type GenChoice struct {
	By           string   `bson:"by" json:"by" jsonschema:"enum=default,enum=asked,enum=content-settings"`
	Alternatives []string `bson:"alternatives,omitempty" json:"alternatives,omitempty" jsonschema:"description=The other models the estimate offered (provider/model)."`
}

// GenWithdrawn records that what a generation made at the provider — a voice
// copy — is withdrawn (D227): by a person, or because the consent it relied on
// was withdrawn. Nothing more is made with it from then on; deleting it at the
// provider is a job retried until done (JobID), recorded in Generation.Removed.
type GenWithdrawn struct {
	At     time.Time `bson:"at" json:"at"`
	By     Actor     `bson:"by" json:"by"`
	Reason string    `bson:"reason,omitempty" json:"reason,omitempty"`
	JobID  string    `bson:"jobId,omitempty" json:"jobId,omitempty"`
}

// Reference roles: what a picture or clip sent with a generation is for.
const (
	RefStartFrame = "start-frame" // the shot starts from this frame ("continue this shot")
	RefEndFrame   = "end-frame"   // the shot ends on this frame (between two frames)
	RefImage      = "image"       // a picture referenced in the prompt (<IMAGE_REF_n>)
	RefLookStill  = "look-still"  // a still of the look's person or character
	RefLookClip   = "look-clip"   // a short clip of the look's person (likeness)
	RefInput      = "input-video" // the clip an edit or an extension works on
	// AI work of other kinds (D226).
	RefPicture = "picture" // a picture to change, or one the words refer to
	RefAudio   = "audio"   // a recording to change the voice of, or to write down
	RefMedia   = "media"   // a recording or video to dub
	RefSample  = "sample"  // a sample of the voice to copy
	RefConsent = "consent" // the speaker reading the provider's consent script
	RefFootage = "footage" // a presenter's training take (D273)
)

// GenReference is one picture or clip sent with a generation: what it is for,
// exactly which bytes (SHA-256), and where they came from.
type GenReference struct {
	Role string `bson:"role" json:"role" jsonschema:"enum=start-frame,enum=end-frame,enum=image,enum=look-still,enum=look-clip,enum=input-video,enum=picture,enum=audio,enum=media,enum=sample,enum=consent,enum=footage"`
	// Ref is a stable name for it: a resource "name@version", a look's
	// "look/still-1@version", or a clip and time ("clip@12.40").
	Ref    string `bson:"ref" json:"ref"`
	SHA256 string `bson:"sha256" json:"sha256"`
	MIME   string `bson:"mime" json:"mime"`
	Size   int64  `bson:"size" json:"size"`
	// StorageKey is where MetaRush keeps the exact bytes (content addressed).
	StorageKey string `bson:"storageKey,omitempty" json:"storageKey,omitempty"`
	// AssetID and At / From–To: the real footage it was taken from.
	AssetID string  `bson:"assetId,omitempty" json:"assetId,omitempty"`
	At      float64 `bson:"at,omitempty" json:"at,omitempty"`
	From    float64 `bson:"from,omitempty" json:"from,omitempty"`
	To      float64 `bson:"to,omitempty" json:"to,omitempty"`
	// ResourceID is set for a picture from the resources library.
	ResourceID string `bson:"resourceId,omitempty" json:"resourceId,omitempty"`
	// People are who the frame shows, by the faces index (names a person gave).
	People []string `bson:"people,omitempty" json:"people,omitempty"`
}

// GenParams are the settings a generation was asked with.
type GenParams struct {
	Seconds     int    `bson:"seconds" json:"seconds"`
	AspectRatio string `bson:"aspectRatio" json:"aspectRatio" jsonschema:"enum=16:9,enum=9:16,enum="`
	Resolution  string `bson:"resolution" json:"resolution" jsonschema:"enum=360p,enum=720p,enum=1080p,enum=4k,enum="`
	Quality     string `bson:"quality" json:"quality" jsonschema:"enum=draft,enum=final,enum="`
	Task        string `bson:"task" json:"task"`
	Delivery    string `bson:"delivery,omitempty" json:"delivery,omitempty"`
}

// GenTemplate is how the prompt sent was put together: the template and its
// values (the look's description, the picture tags, the shot it continues).
type GenTemplate struct {
	Name string            `bson:"name" json:"name"`
	Vars map[string]string `bson:"vars,omitempty" json:"vars,omitempty"`
}

// GenLookRef pins the look a generation used.
type GenLookRef struct {
	ID        string `bson:"id" json:"id"`
	Name      string `bson:"name" json:"name"`
	Title     string `bson:"title" json:"title"`
	Version   string `bson:"version" json:"version"`
	Subject   string `bson:"subject" json:"subject" jsonschema:"enum=person,enum=character"`
	PersonID  string `bson:"personId,omitempty" json:"personId,omitempty"`
	ConsentID string `bson:"consentId,omitempty" json:"consentId,omitempty"`
}

// GenCost is money and tokens, as estimated or as the provider reported.
type GenCost struct {
	USD            float64 `bson:"usd" json:"usd"`
	VideoTokens    int64   `bson:"videoTokens" json:"videoTokens"`
	InputTokens    int64   `bson:"inputTokens" json:"inputTokens"`
	ThinkingTokens int64   `bson:"thinkingTokens" json:"thinkingTokens"`
	TextTokens     int64   `bson:"textTokens,omitempty" json:"textTokens,omitempty"`
	// Unit and Amount: what other kinds of AI work are counted in (seconds,
	// minutes, characters, pictures, songs) and how many (D226).
	Unit   string  `bson:"unit,omitempty" json:"unit,omitempty"`
	Amount float64 `bson:"amount,omitempty" json:"amount,omitempty"`
	// Basis says where the numbers come from: estimate (price table), usage
	// (what the provider reported) or assumed (no usage reported: the estimate counts).
	Basis string `bson:"basis" json:"basis" jsonschema:"enum=estimate,enum=usage,enum=assumed,enum=none"`
}

// GenBudget is the money set aside for a generation before it was sent
// (D124): reserved against the project's budget and the day's cap, then
// settled with what it cost.
type GenBudget struct {
	ReservedUSD float64 `bson:"reservedUsd" json:"reservedUsd"`
	Day         string  `bson:"day" json:"day" jsonschema:"description=The UTC day the money counts against (YYYY-MM-DD)."`
}

// GenSettlement is what a generation finally counted against the budget.
type GenSettlement struct {
	USD  float64   `bson:"usd" json:"usd"`
	At   time.Time `bson:"at" json:"at"`
	Note string    `bson:"note,omitempty" json:"note,omitempty"`
}

// GenTicket is the provider's handle on a running generation, saved before
// MetaRush waits for it, so a restart resumes instead of sending it again.
type GenTicket struct {
	ID    string   `bson:"id" json:"id" jsonschema:"description=The provider's ID for the request (Gemini: the interaction)."`
	Files []string `bson:"files,omitempty" json:"files,omitempty" jsonschema:"description=Inputs uploaded to the provider for this request (deleted afterwards)."`
	// Extra are other handles the provider gave (a transcript's or a history item's ID), deleted afterwards where it allows.
	Extra       map[string]string `bson:"extra,omitempty" json:"extra,omitempty"`
	SubmittedAt time.Time         `bson:"submittedAt" json:"submittedAt"`
}

// GenSend is one attempt to send a generation to the provider.
type GenSend struct {
	At     time.Time `bson:"at" json:"at"`
	Result string    `bson:"result" json:"result" jsonschema:"enum=accepted,enum=rejected,enum=failed"`
	Code   string    `bson:"code,omitempty" json:"code,omitempty"`
	Detail string    `bson:"detail,omitempty" json:"detail,omitempty"`
	// Files are what a send whose answer was lost uploaded to the provider
	// (D277): deleted with whatever is found by the request's tag.
	Files []string `bson:"files,omitempty" json:"files,omitempty"`
}

// GenSafety is what the provider's safety filters did.
type GenSafety struct {
	Blocked       bool     `bson:"blocked" json:"blocked"`
	Side          string   `bson:"side,omitempty" json:"side,omitempty" jsonschema:"enum=input,enum=output"`
	FilteredCount int      `bson:"filteredCount,omitempty" json:"filteredCount,omitempty"`
	SupportCodes  []string `bson:"supportCodes,omitempty" json:"supportCodes,omitempty"`
	Category      string   `bson:"category,omitempty" json:"category,omitempty" jsonschema:"description=What kind of content it was about, as far as the provider said (person, child, public-figure, sexual, violence, other)."`
	Message       string   `bson:"message,omitempty" json:"message,omitempty" jsonschema:"description=The provider's own words."`
}

// GenOutput is a clip a generation made.
type GenOutput struct {
	AssetID        string  `bson:"assetId" json:"assetId"`
	SHA256         string  `bson:"sha256" json:"sha256"`
	Size           int64   `bson:"size" json:"size"`
	Duration       float64 `bson:"duration" json:"duration"`
	Width          int     `bson:"width" json:"width"`
	Height         int     `bson:"height" json:"height"`
	FPS            float64 `bson:"fps,omitempty" json:"fps,omitempty"`
	HasAudio       bool    `bson:"hasAudio" json:"hasAudio"`
	MIME           string  `bson:"mime" json:"mime"`
	Delivery       string  `bson:"delivery" json:"delivery" jsonschema:"enum=inline,enum=uri,enum=transcript,enum=voice,enum=presenter"`
	ProviderFileID string  `bson:"providerFileId,omitempty" json:"providerFileId,omitempty"`
	// Status is what adding it did: a new clip, or the same bytes as a clip MetaRush had.
	Status string `bson:"status" json:"status"`
	// Other kinds of AI work (D226): a picture kept in the resources library,
	// a voice copy at the provider, a transcript (kept as a file, for the
	// clip AssetID names) and its language.
	ResourceID string `bson:"resourceId,omitempty" json:"resourceId,omitempty"`
	VoiceID    string `bson:"voiceId,omitempty" json:"voiceId,omitempty"`
	// PresenterID is the provider's ID of a presenter it made (D273).
	PresenterID string `bson:"presenterId,omitempty" json:"presenterId,omitempty"`
	StorageKey  string `bson:"storageKey,omitempty" json:"storageKey,omitempty"`
	Language    string `bson:"language,omitempty" json:"language,omitempty"`
	Words       int    `bson:"words,omitempty" json:"words,omitempty"`
}

// Generation outcome states.
const (
	GenDone     = "done"
	GenFailed   = "failed"
	GenRefused  = "refused" // the provider's safety filters declined it
	GenCanceled = "canceled"
)

// GenOutcome is how a generation ended (written once).
type GenOutcome struct {
	State   string      `bson:"state" json:"state" jsonschema:"enum=done,enum=failed,enum=refused,enum=canceled"`
	At      time.Time   `bson:"at" json:"at"`
	Outputs []GenOutput `bson:"outputs,omitempty" json:"outputs,omitempty"`
	Actual  *GenCost    `bson:"actual,omitempty" json:"actual,omitempty"`
	Safety  *GenSafety  `bson:"safety,omitempty" json:"safety,omitempty"`
	// Code, Message and Hint say what went wrong in plain words (stable code
	// for skills); Detail keeps the provider's own words.
	Code    string `bson:"code,omitempty" json:"code,omitempty"`
	Message string `bson:"message,omitempty" json:"message,omitempty"`
	Hint    string `bson:"hint,omitempty" json:"hint,omitempty"`
	Detail  string `bson:"detail,omitempty" json:"detail,omitempty"`
	// Partial: fewer seconds came back than were asked for.
	Partial bool `bson:"partial,omitempty" json:"partial,omitempty"`
}

// GenForgotten records deleting the request and its inputs at the provider
// right after the result was downloaded (likeness pictures are not kept
// there for weeks; D125).
type GenForgotten struct {
	At     time.Time `bson:"at" json:"at"`
	OK     bool      `bson:"ok" json:"ok"`
	Detail string    `bson:"detail,omitempty" json:"detail,omitempty"`
	// Kept: the provider offers no way to delete it (its data terms say how
	// long it keeps it, D226); Detail says so in plain words.
	Kept bool `bson:"kept,omitempty" json:"kept,omitempty"`
}

// Generation is one request to make a video with a provider (D125): how it
// was made, exactly what was sent, what it cost and what came back. It is
// written once and never changed: each part is filled in when it becomes known
// (the ticket when the provider accepts it, the outcome when it ends) and is
// never overwritten. A new take is a new generation.
type Generation struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	ProjectID   string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	// Kind is the kind of AI work (D226): video, picture, voice, voice-copy,
	// voice-change, sound-effect, music, dubbing, transcription, presenter
	// (a presenter reading lines), presenter-make (a person's presenter
	// made at a provider, D273), background-removal, upscale and lip-sync
	// (D279). Records made before D226 have none: they are video.
	Kind      string `bson:"kind,omitempty" json:"kind,omitempty" jsonschema:"enum=video,enum=picture,enum=voice,enum=voice-copy,enum=voice-change,enum=sound-effect,enum=music,enum=dubbing,enum=transcription,enum=presenter,enum=presenter-make,enum=background-removal,enum=upscale,enum=lip-sync,enum="`
	Operation string `bson:"operation" json:"operation" jsonschema:"enum=shot,enum=again,enum=variation,enum=extend,enum=edit,enum=look-test,enum=make"`
	// GroupID is the first generation made this way; takes share it. Take is
	// this one's number among them (1, 2, …).
	GroupID string `bson:"groupId" json:"groupId"`
	Take    int    `bson:"take" json:"take"`
	// The provider and model, its launch stage and the price table used.
	Provider      string `bson:"provider" json:"provider"`
	ProviderTitle string `bson:"providerTitle" json:"providerTitle"`
	Model         string `bson:"model" json:"model"`
	ModelTitle    string `bson:"modelTitle" json:"modelTitle"`
	// ModelVersion is the version of the model the plan used, when the same
	// name can change (an ability's approved version, D281): the work runs
	// only with that version.
	ModelVersion string `bson:"modelVersion,omitempty" json:"modelVersion,omitempty"`
	Stage        string `bson:"stage" json:"stage" jsonschema:"enum=ga,enum=preview,enum=experimental"`
	PriceTable   string `bson:"priceTable" json:"priceTable" jsonschema:"description=The date of the price table the estimate used (YYYY-MM-DD)."`
	// Prompt is verbatim as written; SentPrompt is what was sent when MetaRush
	// added to it (the look's description, picture tags); Template says how.
	Prompt     string         `bson:"prompt" json:"prompt"`
	SentPrompt string         `bson:"sentPrompt,omitempty" json:"sentPrompt,omitempty"`
	Template   *GenTemplate   `bson:"template,omitempty" json:"template,omitempty"`
	Look       *GenLookRef    `bson:"look,omitempty" json:"look,omitempty"`
	References []GenReference `bson:"references,omitempty" json:"references,omitempty"`
	Params     GenParams      `bson:"params" json:"params"`
	// Options are another kind's settings (D226); Terms the model's licence
	// and data terms then; Choice why this model.
	Options *GenOptions `bson:"options,omitempty" json:"options,omitempty"`
	Terms   *GenTerms   `bson:"terms,omitempty" json:"terms,omitempty"`
	Choice  *GenChoice  `bson:"choice,omitempty" json:"choice,omitempty"`
	// RequestHash is the SHA-256 of the canonical request (pictures and clips
	// by their SHA-256; never a key).
	RequestHash string `bson:"requestHash" json:"requestHash"`
	// Consent: the consent records this generation relies on (likeness), and
	// whether the person said the frames show no people.
	ConsentIDs []string `bson:"consentIds,omitempty" json:"consentIds,omitempty"`
	NoPeople   bool     `bson:"noPeople,omitempty" json:"noPeople,omitempty"`
	// Money: the estimate, the reservation and (later) the settlement.
	Estimate GenCost        `bson:"estimate" json:"estimate"`
	Budget   *GenBudget     `bson:"budget,omitempty" json:"budget,omitempty"`
	Settled  *GenSettlement `bson:"settled,omitempty" json:"settled,omitempty"`
	// Talking to the provider: every send, the ticket, the outcome, the clean-up.
	Sends     []GenSend     `bson:"sends,omitempty" json:"sends,omitempty"`
	Sending   []time.Time   `bson:"sending,omitempty" json:"sending,omitempty" jsonschema:"description=When each send began (one more than sends means a send was interrupted)."`
	Ticket    *GenTicket    `bson:"ticket,omitempty" json:"ticket,omitempty"`
	Outcome   *GenOutcome   `bson:"outcome,omitempty" json:"outcome,omitempty"`
	Forgotten *GenForgotten `bson:"forgotten,omitempty" json:"forgotten,omitempty"`
	// Withdrawn: what it made at the provider (a voice copy) is withdrawn;
	// Removed: it was deleted there (written once, by the deletion job).
	Withdrawn *GenWithdrawn `bson:"withdrawn,omitempty" json:"withdrawn,omitempty"`
	Removed   *GenForgotten `bson:"removed,omitempty" json:"removed,omitempty"`
	// Lineage: the generation it came from, and the real footage it used.
	ParentID     string   `bson:"parentId,omitempty" json:"parentId,omitempty"`
	SourceAssets []string `bson:"sourceAssets,omitempty" json:"sourceAssets,omitempty"`
	// Who asked, on which machine, through which channel; the job that runs it.
	CreatedBy Actor  `bson:"createdBy" json:"createdBy"`
	Machine   string `bson:"machine" json:"machine"`
	Channel   string `bson:"channel" json:"channel"`
	// Origin is what the work was asked for by, when another feature asked
	// (say:<change>: new words in a speaker's own voice, D310).
	Origin    string    `bson:"origin,omitempty" json:"origin,omitempty"`
	JobID     string    `bson:"jobId" json:"jobId"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
}

// Look subjects and styles.
const (
	LookPerson    = "person"
	LookCharacter = "character"
	LookRealistic = "realistic"
	LookStylized  = "stylized"
)

// Look states.
const (
	LookDraft    = "draft"
	LookApproved = "approved"
	LookRevoked  = "revoked"
)

// LookStill is a still of a look's person or character: a frame of real
// footage (or a picture) kept as exact bytes.
type LookStill struct {
	Ref        string  `bson:"ref" json:"ref" jsonschema:"description=still-1\\, still-2…"`
	AssetID    string  `bson:"assetId,omitempty" json:"assetId,omitempty"`
	At         float64 `bson:"at,omitempty" json:"at,omitempty"`
	ResourceID string  `bson:"resourceId,omitempty" json:"resourceId,omitempty"`
	SHA256     string  `bson:"sha256" json:"sha256"`
	StorageKey string  `bson:"storageKey" json:"storageKey"`
	Size       int64   `bson:"size" json:"size"`
	Width      int     `bson:"width,omitempty" json:"width,omitempty"`
	Height     int     `bson:"height,omitempty" json:"height,omitempty"`
	FaceID     string  `bson:"faceId,omitempty" json:"faceId,omitempty"`
}

// LookClip is a short clip (3 seconds or less) of a look's person, taken from
// real footage: likeness references work best with them.
type LookClip struct {
	Ref        string  `bson:"ref" json:"ref" jsonschema:"description=clip-1\\, clip-2…"`
	AssetID    string  `bson:"assetId" json:"assetId"`
	From       float64 `bson:"from" json:"from"`
	To         float64 `bson:"to" json:"to"`
	SHA256     string  `bson:"sha256" json:"sha256"`
	StorageKey string  `bson:"storageKey" json:"storageKey"`
	Size       int64   `bson:"size" json:"size"`
	Seconds    float64 `bson:"seconds" json:"seconds"`
}

// LookDefaults are a look's usual settings for new shots.
type LookDefaults struct {
	AspectRatio string `bson:"aspectRatio,omitempty" json:"aspectRatio,omitempty" jsonschema:"enum=,enum=16:9,enum=9:16"`
	Seconds     int    `bson:"seconds,omitempty" json:"seconds,omitempty"`
	Quality     string `bson:"quality,omitempty" json:"quality,omitempty" jsonschema:"enum=,enum=draft,enum=final"`
}

// LookTest is a take on a look's test sheet.
type LookTest struct {
	GenerationID string    `bson:"generationId" json:"generationId"`
	Prompt       string    `bson:"prompt" json:"prompt"`
	At           time.Time `bson:"at" json:"at"`
}

// GenLook is a look (D126): how a person or a fictional character appears in
// generated video — 2 or 3 stills and up to three short clips from real
// footage, a fixed description of who they are and how they look, defaults
// and a test sheet. Only people create, approve or revoke a look; a person's
// look relies on their consent record.
type GenLook struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	Name        string `bson:"name" json:"name" jsonschema:"description=Short name (lower case\\, unique): theo\\, captain-blue."`
	Title       string `bson:"title" json:"title"`
	Subject     string `bson:"subject" json:"subject" jsonschema:"enum=person,enum=character"`
	PersonID    string `bson:"personId,omitempty" json:"personId,omitempty"`
	PersonName  string `bson:"personName,omitempty" json:"personName,omitempty"`
	Style       string `bson:"style" json:"style" jsonschema:"enum=realistic,enum=stylized"`
	// StyleNote describes a stylized look ("a watercolor illustration").
	StyleNote   string       `bson:"styleNote,omitempty" json:"styleNote,omitempty"`
	Description string       `bson:"description" json:"description"`
	Stills      []LookStill  `bson:"stills" json:"stills"`
	Clips       []LookClip   `bson:"clips" json:"clips"`
	Defaults    LookDefaults `bson:"defaults" json:"defaults"`
	ConsentID   string       `bson:"consentId,omitempty" json:"consentId,omitempty"`
	// Version is a fingerprint of what the look sends (stills, clips,
	// description, style); generations pin it (name@version).
	Version    string     `bson:"version" json:"version"`
	Tests      []LookTest `bson:"tests,omitempty" json:"tests,omitempty"`
	State      string     `bson:"state" json:"state" jsonschema:"enum=draft,enum=approved,enum=revoked"`
	ApprovedBy *Actor     `bson:"approvedBy,omitempty" json:"approvedBy,omitempty"`
	ApprovedAt *time.Time `bson:"approvedAt,omitempty" json:"approvedAt,omitempty"`
	RevokedBy  *Actor     `bson:"revokedBy,omitempty" json:"revokedBy,omitempty"`
	RevokedAt  *time.Time `bson:"revokedAt,omitempty" json:"revokedAt,omitempty"`
	RevokeNote string     `bson:"revokeNote,omitempty" json:"revokeNote,omitempty"`
	CreatedBy  Actor      `bson:"createdBy" json:"createdBy"`
	CreatedAt  time.Time  `bson:"createdAt" json:"createdAt"`
	UpdatedAt  time.Time  `bson:"updatedAt" json:"updatedAt"`
}

// ConsentScope says what a consent covers.
type ConsentScope struct {
	// AllProjects: every project; otherwise Projects lists the ones it covers.
	AllProjects bool     `bson:"allProjects" json:"allProjects"`
	Projects    []string `bson:"projects,omitempty" json:"projects,omitempty"`
	// Note is anything the person limited it to, in their words ("only for the family channel").
	Note string `bson:"note,omitempty" json:"note,omitempty"`
}

// Consent is a person's permission to have their likeness made with
// generative video (D126). Your own likeness needs a statement only you can
// make; anyone else's needs their recorded consent (a sound or video clip of
// them saying so), with what it covers and until when. It can be revoked at
// any time. MetaRush never records consent for a child or a public figure.
type Consent struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	PersonID    string `bson:"personId" json:"personId"`
	PersonName  string `bson:"personName" json:"personName"`
	// Self: the person who recorded it said it is their own likeness.
	Self bool `bson:"self" json:"self"`
	// Statement is the consent in the person's own words.
	Statement string `bson:"statement" json:"statement"`
	// RecordingID is a sound or video clip of the person giving consent
	// (anyone else's likeness, and anyone's presenter, D274).
	RecordingID string       `bson:"recordingId,omitempty" json:"recordingId,omitempty"`
	Scope       ConsentScope `bson:"scope" json:"scope"`
	// Uses are what it covers (D274, D279): shots (generated shots and
	// pictures of the person), voice (copies of their voice and what is said
	// with them), presenters, lip-sync (their lips matched to new sound) and
	// new-words (a word they said changed in their own voice copy, D309).
	// Records made before D274 have none: shots and voice.
	Uses []string `bson:"uses,omitempty" json:"uses,omitempty" jsonschema:"description=What it covers: shots\\, voice\\, presenters\\, lip-sync\\, new-words (a word they said changed in their own voice copy) (none: shots and voice)."`
	// Providers are the AI services that may hold the recording and the
	// footage of the person (D274) or do a use (lip sync, D279): a presenter
	// is made, and lips matched, only at one named here.
	Providers []string   `bson:"providers,omitempty" json:"providers,omitempty"`
	ExpiresAt *time.Time `bson:"expiresAt,omitempty" json:"expiresAt,omitempty"`
	// ExpiresZone is the time zone whose midnight ends its last day (D274).
	ExpiresZone string `bson:"expiresZone,omitempty" json:"expiresZone,omitempty"`
	// Adult and PublicFigure are what the person recording it confirmed: the
	// person is 18 or older, and not a public figure.
	Adult        bool       `bson:"adult" json:"adult"`
	PublicFigure bool       `bson:"publicFigure" json:"publicFigure"`
	RecordedBy   Actor      `bson:"recordedBy" json:"recordedBy"`
	RecordedAt   time.Time  `bson:"recordedAt" json:"recordedAt"`
	Machine      string     `bson:"machine" json:"machine"`
	RevokedAt    *time.Time `bson:"revokedAt,omitempty" json:"revokedAt,omitempty"`
	RevokedBy    *Actor     `bson:"revokedBy,omitempty" json:"revokedBy,omitempty"`
	RevokeNote   string     `bson:"revokeNote,omitempty" json:"revokeNote,omitempty"`
}

// Active reports whether a consent can be relied on at t.
func (c *Consent) Active(t time.Time) bool {
	return c != nil && c.RevokedAt == nil && c.Adult && !c.PublicFigure && (c.ExpiresAt == nil || t.Before(*c.ExpiresAt))
}

// Consent uses (D274, D279).
const (
	ConsentShots      = "shots"      // generated shots and pictures of the person
	ConsentVoice      = "voice"      // copies of their voice, and speech in it
	ConsentPresenters = "presenters" // a presenter of the person, kept at a provider
	ConsentUseLipSync = "lip-sync"   // their lips matched to new sound (D279)
	// ConsentUseNewWords: a word or short phrase they said changed in their own
	// voice copy (D309) — new words they never said, with the service named.
	ConsentUseNewWords = "new-words"
)

// ConsentUses lists every use a consent can cover.
var ConsentUses = []string{ConsentShots, ConsentVoice, ConsentPresenters, ConsentUseLipSync, ConsentUseNewWords}

// UsesOf are the uses a consent covers (records made before D274: shots and
// voice; never a presenter, lip sync or new words unless named).
func (c *Consent) UsesOf() []string {
	if len(c.Uses) == 0 {
		return []string{ConsentShots, ConsentVoice}
	}
	return c.Uses
}

// CoversUse reports whether a consent covers a use ("" = any).
func (c *Consent) CoversUse(use string) bool {
	if c == nil {
		return false
	}
	if use == "" {
		return true
	}
	for _, u := range c.UsesOf() {
		if u == use {
			return true
		}
	}
	return false
}

// NamesProvider reports whether a consent names a provider: one that may
// hold the recording and footage (a presenter) or do a use (lip sync).
func (c *Consent) NamesProvider(provider string) bool {
	if c == nil {
		return false
	}
	for _, p := range c.Providers {
		if strings.EqualFold(p, provider) {
			return true
		}
	}
	return false
}

// CoversUseAt reports whether a consent names both a use and the service it
// may go to for it (lip sync with sync., D279; consent recorded before covers neither).
func (c *Consent) CoversUseAt(use, service string) bool {
	return c.CoversUse(use) && c.NamesProvider(service)
}

// HoldsNewWords reports whether a consent still covers new words in the
// person's voice in a project, with a service (D309): in force there, naming
// new words and the service, and covering their voice.
func (c *Consent) HoldsNewWords(projectID, service string, t time.Time) bool {
	return c.Covers(projectID, t) && c.CoversUseAt(ConsentUseNewWords, service) && c.CoversUse(ConsentVoice)
}

// Covers reports whether an active consent covers a project ("" = no project).
func (c *Consent) Covers(projectID string, t time.Time) bool {
	if !c.Active(t) {
		return false
	}
	if c.Scope.AllProjects {
		return true
	}
	for _, p := range c.Scope.Projects {
		if p == projectID {
			return true
		}
	}
	return false
}

// GenSpend is money spent and set aside for generative video in one scope
// (D124): a project (its budget) or a UTC day (the workspace's daily cap).
// Amounts are in micro-dollars so concurrent updates add up exactly.
type GenSpend struct {
	ID          string    `bson:"_id" json:"id"`
	WorkspaceID string    `bson:"workspaceId" json:"workspaceId"`
	Scope       string    `bson:"scope" json:"scope" jsonschema:"enum=project,enum=day"`
	Key         string    `bson:"key" json:"key"`
	SpentMicros int64     `bson:"spentMicros" json:"spentMicros"`
	HeldMicros  int64     `bson:"heldMicros" json:"heldMicros"`
	Count       int       `bson:"count" json:"count"`
	UpdatedAt   time.Time `bson:"updatedAt" json:"updatedAt"`
}
