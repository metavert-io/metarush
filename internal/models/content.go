package models

import (
	"time"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Content Settings (D192, D193): what every video made with them uses — the
// brand, the spelling list, the narration voice and house rules. They belong
// to a project; each project uses the default (the project whose settings
// are "used for new projects"), another project's (followed live, through
// chains) or its own. Money is never part of them: budgets, credits used and
// daily limits stay on each project and the workspace.

// What a project's Content Settings come from (ContentUse.Uses).
const (
	ContentDefault = "default"
	ContentProject = "project"
	ContentOwn     = "own"
)

// ContentUse is which Content Settings a project uses. A project without one
// uses the default.
type ContentUse struct {
	Uses string `bson:"uses" json:"uses" jsonschema:"enum=default,enum=project,enum=own"`
	// Project is the project followed (Uses project): its settings, live.
	Project string    `bson:"project,omitempty" json:"project,omitempty"`
	Since   time.Time `bson:"since" json:"since"`
	By      *Actor    `bson:"by,omitempty" json:"by,omitempty"`
}

// ContentDefaultRef is the project whose Content Settings new projects (and
// footage in no project) use: its "Use for new projects" is ticked. One per
// workspace (content_defaults, keyed by the workspace).
type ContentDefaultRef struct {
	ID        string    `bson:"_id" json:"id" jsonschema:"description=The workspace."`
	ProjectID string    `bson:"projectId" json:"projectId"`
	SetAt     time.Time `bson:"setAt" json:"setAt"`
	SetBy     *Actor    `bson:"setBy,omitempty" json:"setBy,omitempty"`
}

// ContentSettings are one project's own Content Settings (cnt_…). Projects
// that follow it (or the default, when it is the default) use them live. The
// spelling list is the project's spelling entries (spelling_entries with its
// projectId).
type ContentSettings struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	ProjectID   string `bson:"projectId" json:"projectId"`
	// BrandKit is the brand: logos, colours and font (D82, D165).
	BrandKit *BrandKit `bson:"brandKit,omitempty" json:"brandKit,omitempty"`
	// Voice are the narration voice choices by provider (elevenlabs): the AI
	// voice, its model and settings, the region and the favourite voices.
	Voice map[string]VoiceChoices `bson:"voice,omitempty" json:"voice,omitempty"`
	// HouseRules are rules for every video using these settings ("the name
	// bar never animates"); a project's own rules stay on the project.
	HouseRules []ProjectRule `bson:"houseRules,omitempty" json:"houseRules,omitempty" jsonschema:"maxItems=100"`
	// Style is the house style (D199): what new cuts of every project using
	// these settings start with — captions, the name bar, name titles,
	// framing, sound at cuts, an intro and an end card.
	Style *editspec.HouseStyle `bson:"style,omitempty" json:"style,omitempty"`
	// Pack is the layout pack (D263): the title cards, lower thirds, end
	// screens, quote and chapter cards and call layouts every project using
	// these settings draws with. Nil: MetaRush's own starter pack in the house
	// style.
	Pack *editspec.LayoutPack `bson:"pack,omitempty" json:"pack,omitempty"`
	// PackVersion counts every change to the pack, going back to MetaRush's
	// own included (the pack's own version is lost with it), so a draft or a
	// card made before any of them is refused.
	PackVersion int `bson:"packVersion,omitempty" json:"packVersion,omitempty"`
	// PackDraft is a pack proposed for these settings (D266) — by Claude or
	// anyone — that becomes the pack only when a person accepts it.
	PackDraft *PackDraft `bson:"packDraft,omitempty" json:"packDraft,omitempty"`
	// Signoffs are house sign-offs (D200): OKs every project using these
	// settings needs before a video goes out ("Legal OKs it before export").
	// Whether one was given is the project's (Project.HouseSignoffs).
	Signoffs []Signoff `bson:"signoffs,omitempty" json:"signoffs,omitempty" jsonschema:"maxItems=50"`
	// AIServices say which AI services the work of every project using these
	// settings may go to (D225): none at all, never some providers, and a
	// model pinned per kind of work.
	AIServices *AIServiceRules `bson:"aiServices,omitempty" json:"aiServices,omitempty"`
	// Gate says whether the videos of every project using these settings
	// wait for the quality check and their sign-offs before they go out
	// (D298), over the workspace's choice; nil: the workspace's choice.
	Gate *GateSetting `bson:"gate,omitempty" json:"gate,omitempty"`
	// Leave says what of every project using these settings may leave this
	// computer, and to whom (D288): AI services, abilities, Claude, cloud
	// storage and delivery.
	Leave     *LeaveRules `bson:"leave,omitempty" json:"leave,omitempty"`
	CreatedAt time.Time   `bson:"createdAt" json:"createdAt"`
	CreatedBy Actor       `bson:"createdBy" json:"createdBy"`
	UpdatedAt time.Time   `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy *Actor      `bson:"updatedBy,omitempty" json:"updatedBy,omitempty"`
}

// AIServiceRules are which AI services a client's work may go to (D225), a
// Content Settings part: nothing at all, never some providers, or a model
// pinned for a kind of work. Generated video and every other kind of AI work
// follow them; money never follows (budgets stay per project).
type AIServiceRules struct {
	// None: nothing of these projects goes to any AI service.
	None bool `bson:"none,omitempty" json:"none,omitempty"`
	// Forbid lists providers never used (gemini, elevenlabs).
	Forbid []string `bson:"forbid,omitempty" json:"forbid,omitempty" jsonschema:"maxItems=20"`
	// Pin is the model to use for a kind of work: kind → "provider/model".
	Pin map[string]string `bson:"pin,omitempty" json:"pin,omitempty"`
	// ForbidKinds lists kinds of AI work never made for these projects, from
	// any provider: presenters (D277), music…
	ForbidKinds []string `bson:"forbidKinds,omitempty" json:"forbidKinds,omitempty" jsonschema:"maxItems=20"`
	// Note says why, in the person's own words ("the client's contract").
	Note      string    `bson:"note,omitempty" json:"note,omitempty" jsonschema:"maxLength=500"`
	UpdatedAt time.Time `bson:"updatedAt,omitempty" json:"updatedAt,omitempty"`
	UpdatedBy *Actor    `bson:"updatedBy,omitempty" json:"updatedBy,omitempty"`
}

// Empty reports rules that change nothing.
func (r *AIServiceRules) Empty() bool {
	return r == nil || (!r.None && len(r.Forbid) == 0 && len(r.Pin) == 0 && len(r.ForbidKinds) == 0)
}

// AllowsKind reports whether a kind of AI work may be made (a presenter's
// making and its clips count as presenters).
func (r *AIServiceRules) AllowsKind(kind string) bool {
	if r == nil {
		return true
	}
	// "No AI services" is about where work goes, not what kind it is: it
	// still allows the models on this computer (D278), through the provider rules.
	if kind == "presenter-make" {
		kind = "presenter"
	}
	for _, k := range r.ForbidKinds {
		if k == kind {
			return false
		}
	}
	return true
}

// Allows reports whether a provider may be used.
func (r *AIServiceRules) Allows(provider string) bool {
	if r == nil {
		return true
	}
	if r.None {
		return false
	}
	for _, f := range r.Forbid {
		if f == provider {
			return false
		}
	}
	return true
}

// PackDraft is a layout pack waiting for a person to accept it (D266).
type PackDraft struct {
	Pack editspec.LayoutPack `bson:"pack" json:"pack"`
	// BaseVersion is the version of the settings' pack it was made from (0:
	// they had none): accepting it after the pack changed is refused, so a
	// draft never silently undoes a change made since.
	BaseVersion int `bson:"baseVersion" json:"baseVersion"`
	// Note says what it is for, in its maker's words.
	Note string `bson:"note,omitempty" json:"note,omitempty" jsonschema:"maxLength=500"`
	// Origin is where it came from (assistant:<conversation> for Claude's).
	Origin    string    `bson:"origin,omitempty" json:"origin,omitempty"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	CreatedBy Actor     `bson:"createdBy" json:"createdBy"`
}

// VoiceChoices are a provider's narration voice choices in Content Settings:
// never a budget, credits used or a daily limit (those stay per project).
type VoiceChoices struct {
	VoiceID   string                     `bson:"voiceId,omitempty" json:"voiceId,omitempty"`
	VoiceName string                     `bson:"voiceName,omitempty" json:"voiceName,omitempty"`
	ModelID   string                     `bson:"modelId,omitempty" json:"modelId,omitempty"`
	ModelName string                     `bson:"modelName,omitempty" json:"modelName,omitempty"`
	Voice     *editspec.TTSVoiceSettings `bson:"voice,omitempty" json:"voice,omitempty"`
	// Region selects the provider's data-residency endpoint (ElevenLabs: us, eu, in, sg).
	Region    string          `bson:"region,omitempty" json:"region,omitempty" jsonschema:"enum=,enum=us,enum=eu,enum=in,enum=sg"`
	Favorites []FavoriteVoice `bson:"favorites,omitempty" json:"favorites,omitempty"`
	UpdatedAt time.Time       `bson:"updatedAt,omitempty" json:"updatedAt,omitempty"`
	UpdatedBy *Actor          `bson:"updatedBy,omitempty" json:"updatedBy,omitempty"`
}

// Empty reports voice choices nobody made.
func (v *VoiceChoices) Empty() bool {
	return v == nil || (v.VoiceID == "" && v.ModelID == "" && v.Region == "" && len(v.Favorites) == 0 && (v.Voice == nil || *v.Voice == (editspec.TTSVoiceSettings{})))
}

// Provider is the choices as provider settings (no money in them).
func (v *VoiceChoices) Provider() *ProviderSettings {
	if v == nil {
		return nil
	}
	return &ProviderSettings{VoiceID: v.VoiceID, VoiceName: v.VoiceName, ModelID: v.ModelID, ModelName: v.ModelName, Voice: v.Voice, Region: v.Region,
		Favorites: v.Favorites, UpdatedAt: v.UpdatedAt, UpdatedBy: v.UpdatedBy}
}

// BrandKitEmpty reports a brand kit nobody set up.
func BrandKitEmpty(k *BrandKit) bool {
	return k == nil || (k.Logo == "" && k.LogoDark == "" && k.LogoLight == "" && k.LogoIcon == "" && k.LogoWordmark == "" && k.FontResource == "" && k.Font == "" &&
		k.Primary == "" && k.Accent == "" && k.Text == "" && k.Background == "")
}

// The named logos of a brand (D198): which logo, as ops and the CLI name it.
const (
	LogoMain     = "main"
	LogoDark     = "dark"
	LogoLight    = "light"
	LogoIcon     = "icon"
	LogoWordmark = "wordmark"
)

// LogoKinds are the named logos in the order people see them.
var LogoKinds = []string{LogoMain, LogoDark, LogoLight, LogoIcon, LogoWordmark}

// LogoField is where a kit keeps a named logo (nil for an unknown name).
func (k *BrandKit) LogoField(which string) *string {
	switch which {
	case LogoMain:
		return &k.Logo
	case LogoDark:
		return &k.LogoDark
	case LogoLight:
		return &k.LogoLight
	case LogoIcon:
		return &k.LogoIcon
	case LogoWordmark:
		return &k.LogoWordmark
	}
	return nil
}

// LogoLabel is a named logo the way people see it.
func LogoLabel(which string) string {
	switch which {
	case LogoMain:
		return "Main"
	case LogoDark:
		return "For dark backgrounds"
	case LogoLight:
		return "For light backgrounds"
	case LogoIcon:
		return "Square icon"
	case LogoWordmark:
		return "Wordmark"
	}
	return which
}
