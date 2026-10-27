// Package models holds the MongoDB documents. These bson+json structs are the
// source of truth for shared state; the TypeScript client types are generated from
// them. Every document carries WorkspaceID, and asset references are portable IDs —
// never machine-local paths.
package models

import (
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Actor attributes a change to a person and the installation they used.
type Actor struct {
	UserID         string `bson:"userId" json:"userId"`
	UserName       string `bson:"userName,omitempty" json:"userName,omitempty"`
	InstallationID string `bson:"installationId" json:"installationId"`
	Via            string `bson:"via" json:"via"` // cli | dashboard | worker | watch | assistant | assistant-auto
	// Agent names the AI app acting through the command line or MCP (codex,
	// cursor, claude-code…, D248): what it saves says which agent made it.
	Agent string `bson:"agent,omitempty" json:"agent,omitempty"`
}

// ViaAssistant marks commands run by the dashboard assistant (headless Claude Code
// through the read-only shim). Ops refuse every mutating or human-only operation
// on this channel, whatever Claude Code's own permission settings say.
const ViaAssistant = "assistant"

// ViaAssistantAuto marks a safe step Claude asked for in the dashboard that
// started by itself, as the person's setting allows (D141): a check of
// Claude's motion graphic, a review copy, a link the person gave. It runs as
// that person on the human channel (never on the assistant's), so it is
// recorded as theirs and says Claude started it.
const ViaAssistantAuto = "assistant-auto"

// Workspace is the logical boundary for shared projects and media.
type Workspace struct {
	ID          string    `bson:"_id" json:"id"`
	WorkspaceID string    `bson:"workspaceId" json:"workspaceId"` // equals ID; lets scoped accessors load it
	Name        string    `bson:"name" json:"name"`
	CreatedAt   time.Time `bson:"createdAt" json:"createdAt"`
	CreatedBy   Actor     `bson:"createdBy" json:"createdBy"`
	// MediaStore records the canonical store so a joining installation can check it
	// is pointed at the same place (never credentials).
	MediaStore StoreIdentity `bson:"mediaStore" json:"mediaStore"`
	// Providers holds the team's default, non-secret provider choices by provider
	// name (e.g. elevenlabs: voice, model, settings, favorites). Keys never live here.
	Providers map[string]ProviderSettings `bson:"providers,omitempty" json:"providers,omitempty"`
	// BrandKit was the workspace's logos, colours and font (D82) before
	// Content Settings (D192): migration 36 moves it into the default
	// project's settings. Nothing reads it any more.
	BrandKit *BrandKit `bson:"brandKit,omitempty" json:"brandKit,omitempty"`
	// DescribeScenes lets MetaRush have the person's own Claude Code write a
	// sentence or two per scene of new clips (D74; off by default; projects can override).
	DescribeScenes bool `bson:"describeScenes,omitempty" json:"describeScenes,omitempty"`
	// Trash holds "Empty the Trash after N days" (D98): off unless a person
	// turns it on; nothing is ever emptied automatically otherwise.
	Trash *TrashSettings `bson:"trash,omitempty" json:"trash,omitempty"`
	// Auth is the sign-in method (D183; nil: single, just the owner).
	Auth *AuthSettings `bson:"auth,omitempty" json:"auth,omitempty"`
	// ReviewFollow is "a review copy follows the latest version" (D187):
	// nil or Off false means on, the default.
	ReviewFollow *ReviewFollowSettings `bson:"reviewFollow,omitempty" json:"reviewFollow,omitempty"`
	// Estimates is what owners decide about estimates for everyone (D208):
	// nil means each person's own preference counts.
	Estimates *EstimateSettings `bson:"estimates,omitempty" json:"estimates,omitempty"`
	// ShipGate is whether a version waits for the quality check and its
	// sign-offs before it goes out to a client (D298). nil: nobody decided
	// yet — a workspace made before the gate, where it stays off and owners
	// are asked once; new workspaces start with it on.
	ShipGate *ShipGateSettings `bson:"shipGate,omitempty" json:"shipGate,omitempty"`
	// ReviewLinks is the owners' word on links for people outside the
	// workspace (D314): nil means anyone may share, for up to 7 days.
	ReviewLinks *ReviewLinkSettings `bson:"reviewLinks,omitempty" json:"reviewLinks,omitempty"`
	// DeletionsPaused is set on a restored copy (D333): nothing deletes by
	// itself there — the Trash on its schedule, review links ending and their
	// pages going, old activity cleared out — while the original may still use
	// the same storage. An owner lifts it (workspace.adopt).
	DeletionsPaused *DeletionsPause `bson:"deletionsPaused,omitempty" json:"deletionsPaused,omitempty"`
}

// DeletionsPause is when a restored copy's background deletions were paused,
// and from which archive.
type DeletionsPause struct {
	Since time.Time `bson:"since" json:"since"`
	// ArchiveMadeAt is when the archive the copy was restored from was made.
	ArchiveMadeAt time.Time `bson:"archiveMadeAt,omitempty" json:"archiveMadeAt,omitempty"`
}

// EstimateSettings is the owners' word on estimates of what outside AI
// services cost (D208), with who changed it last.
type EstimateSettings struct {
	// Required: everyone sees the estimate and agrees to it before an edit uses
	// paid outside AI services, whatever their own preference says.
	Required  bool      `bson:"required,omitempty" json:"required,omitempty"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy Actor     `bson:"updatedBy" json:"updatedBy"`
}

// ReviewFollowSettings turns the review copy that follows each cut's latest
// version off or back on (D187), with who changed it last.
type ReviewFollowSettings struct {
	Off       bool      `bson:"off,omitempty" json:"off,omitempty"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy Actor     `bson:"updatedBy" json:"updatedBy"`
}

// StoreIdentity describes a canonical media store without secrets.
type StoreIdentity struct {
	Provider string `bson:"provider" json:"provider"`
	Locator  string `bson:"locator" json:"locator"` // e.g. "s3://bucket/prefix@endpoint-host" or "fs:<root>"
	Shared   bool   `bson:"shared" json:"shared"`
}

// Installation is one machine's copy of MetaRush joined to a workspace.
type Installation struct {
	ID          string    `bson:"_id" json:"id"`
	WorkspaceID string    `bson:"workspaceId" json:"workspaceId"`
	Name        string    `bson:"name" json:"name"`
	Hostname    string    `bson:"hostname" json:"hostname"`
	OS          string    `bson:"os" json:"os"`
	Arch        string    `bson:"arch" json:"arch"`
	Version     string    `bson:"version" json:"version"`
	UserID      string    `bson:"userId" json:"userId"`
	CreatedAt   time.Time `bson:"createdAt" json:"createdAt"`
	LastSeenAt  time.Time `bson:"lastSeenAt" json:"lastSeenAt"`
	// Version above is the MetaRush it runs (0.2.0, 0.2.0-dev.3); Commit and
	// Built are that build's commit and build time (D130). Set when it joins,
	// and kept current by its daemon (background service or dashboard).
	Commit string `bson:"commit,omitempty" json:"commit,omitempty"`
	Built  string `bson:"built,omitempty" json:"built,omitempty"`
	// KnowsMoves is set by a MetaRush that knows about moving the database:
	// the build that said so (its version and commit). A record whose
	// version or commit says another build — an older one started there
	// since — doesn't count (D334).
	KnowsMoves string `bson:"knowsMoves,omitempty" json:"knowsMoves,omitempty"`
	// Machine lifecycle (additive; see `metarush machine`). Empty means active.
	State string `bson:"state,omitempty" json:"state,omitempty" jsonschema:"enum=invited,enum=active,enum=revoked"`
	// StorageKeyID is the object-store access key ID this installation is
	// configured with (an identifier, never the secret). Recorded by init/join.
	StorageKeyID string `bson:"storageKeyId,omitempty" json:"storageKeyId,omitempty"`
	// StorageKey is the per-machine key minted by `machine invite`.
	StorageKey *StorageKeyRef `bson:"storageKey,omitempty" json:"storageKey,omitempty"`
	// DBUser is the per-machine database user created by `machine invite --atlas`.
	DBUser          string     `bson:"dbUser,omitempty" json:"dbUser,omitempty"`
	DBUserDeletedAt *time.Time `bson:"dbUserDeletedAt,omitempty" json:"dbUserDeletedAt,omitempty"`
	InvitedAt       *time.Time `bson:"invitedAt,omitempty" json:"invitedAt,omitempty"`
	InvitedBy       *Actor     `bson:"invitedBy,omitempty" json:"invitedBy,omitempty"`
	JoinedAt        *time.Time `bson:"joinedAt,omitempty" json:"joinedAt,omitempty"`
	RevokedAt       *time.Time `bson:"revokedAt,omitempty" json:"revokedAt,omitempty"`
	RevokedBy       *Actor     `bson:"revokedBy,omitempty" json:"revokedBy,omitempty"`
}

// Installation states.
const (
	InstallationInvited = "invited"
	InstallationActive  = "active"
	InstallationRevoked = "revoked"
)

// StorageKeyRef describes a per-machine object-store key without its secret.
type StorageKeyRef struct {
	Provider  string     `bson:"provider" json:"provider"` // tigris | manual
	KeyID     string     `bson:"keyId" json:"keyId"`
	Label     string     `bson:"label" json:"label"`
	Bucket    string     `bson:"bucket" json:"bucket"`
	Prefix    string     `bson:"prefix,omitempty" json:"prefix,omitempty"`
	Scope     string     `bson:"scope" json:"scope" jsonschema:"enum=prefix,enum=bucket"`
	Role      string     `bson:"role,omitempty" json:"role,omitempty"`
	PolicyARN string     `bson:"policyArn,omitempty" json:"policyArn,omitempty"`
	Managed   bool       `bson:"managed" json:"managed"` // minted (and revocable) by MetaRush
	CreatedAt time.Time  `bson:"createdAt" json:"createdAt"`
	DeletedAt *time.Time `bson:"deletedAt,omitempty" json:"deletedAt,omitempty"`
}

// User is a collaborator in a workspace.
type User struct {
	ID          string    `bson:"_id" json:"id"`
	WorkspaceID string    `bson:"workspaceId" json:"workspaceId"`
	Name        string    `bson:"name" json:"name"`
	Email       string    `bson:"email" json:"email"`
	CreatedAt   time.Time `bson:"createdAt" json:"createdAt"`
	// Logins are the Tailscale accounts (lower case) that are this person when
	// they sign in through remote access (D129).
	Logins []string `bson:"logins,omitempty" json:"logins,omitempty"`
	// LastSignedInAt is when this person last signed in with Tailscale.
	LastSignedInAt *time.Time `bson:"lastSignedInAt,omitempty" json:"lastSignedInAt,omitempty"`
	// Source says how the person was added: "" (set up MetaRush on a
	// computer), "tailscale" (first signed in through remote access) or
	// "invite" (D183).
	Source string `bson:"source,omitempty" json:"source,omitempty"`
	// Accounts (D183, docs/accounts.md). EmailKey is the email trimmed and
	// lower-cased (unique per workspace); Role owner|member ("" member);
	// State active|invited|disabled|earlier ("" earlier: did things before
	// accounts, no sign-in yet). SessionsValidAfter ends every session made
	// before it, on every computer.
	EmailKey           string     `bson:"emailKey,omitempty" json:"-"`
	Role               string     `bson:"role,omitempty" json:"role,omitempty" jsonschema:"enum=owner,enum=member"`
	State              string     `bson:"state,omitempty" json:"state,omitempty" jsonschema:"enum=active,enum=invited,enum=disabled,enum=earlier"`
	SessionsValidAfter *time.Time `bson:"sessionsValidAfter,omitempty" json:"-"`
	PasswordSetAt      *time.Time `bson:"passwordSetAt,omitempty" json:"passwordSetAt,omitempty"`
	InvitedBy          *Actor     `bson:"invitedBy,omitempty" json:"invitedBy,omitempty"`
	// Avatar is their picture (D184; nil: their initials show).
	Avatar *Avatar `bson:"avatar,omitempty" json:"avatar,omitempty"`
	// Preferences are their own choices (D208, Settings → Preferences; nil:
	// the defaults). Their Ask first or Auto choice is assistantMode (D186),
	// read by the assistant.
	Preferences *UserPreferences `bson:"preferences,omitempty" json:"preferences,omitempty"`
	UpdatedAt   *time.Time       `bson:"updatedAt,omitempty" json:"updatedAt,omitempty"`
	UpdatedBy   *Actor           `bson:"updatedBy,omitempty" json:"updatedBy,omitempty"`
}

// UserPreferences are a person's own choices (D208).
type UserPreferences struct {
	// EstimatesOff: "Estimate costs of outside AI services before edits" is
	// off, so edits that use paid outside AI services run within budgets,
	// daily limits and project caps without the estimate first. False (the
	// default): the estimate comes first.
	EstimatesOff bool       `bson:"estimatesOff,omitempty" json:"estimatesOff,omitempty"`
	UpdatedAt    *time.Time `bson:"updatedAt,omitempty" json:"updatedAt,omitempty"`
}

// Avatar is a person's picture: a 256-pixel square PNG in the media store by its SHA-256 (D184).
type Avatar struct {
	SHA256    string    `bson:"sha256" json:"sha256"`
	Size      int64     `bson:"size" json:"size"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

// User roles and states (D183).
const (
	RoleOwner     = "owner"
	RoleMember    = "member"
	UserActive    = "active"
	UserInvited   = "invited"
	UserDisabled  = "disabled"
	UserEarlier   = "earlier"
	AuthSingle    = "single"
	AuthPassword  = "password"
	AuthTailscale = "tailscale"
)

// AuthSettings is a workspace's sign-in method (D183): single (just the owner
// on their computer, the default), password or tailscale.
type AuthSettings struct {
	Method string    `bson:"method" json:"method" jsonschema:"enum=single,enum=password,enum=tailscale"`
	SetAt  time.Time `bson:"setAt" json:"setAt"`
	SetBy  Actor     `bson:"setBy" json:"setBy"`
}

// Brief captures the creative intent for a project.
type Brief struct {
	Goal        string   `bson:"goal,omitempty" json:"goal,omitempty"`
	Audience    string   `bson:"audience,omitempty" json:"audience,omitempty"`
	Tone        string   `bson:"tone,omitempty" json:"tone,omitempty"`
	Platforms   []string `bson:"platforms,omitempty" json:"platforms,omitempty"`
	TargetSecs  float64  `bson:"targetSeconds,omitempty" json:"targetSeconds,omitempty"`
	Constraints []string `bson:"constraints,omitempty" json:"constraints,omitempty"`
	// TransitionStyle is how the editing skills choose transitions for this
	// project: natural | snappy | smooth | playful (see metarush edit transitions).
	TransitionStyle string `bson:"transitionStyle,omitempty" json:"transitionStyle,omitempty" jsonschema:"enum=natural,enum=snappy,enum=smooth,enum=playful"`
	// Deliverables are where the video goes and in what shape (D166): "by
	// email" and "on the website" widescreen, "social shorts" vertical. Motion
	// checks draw in their shapes when no cut uses a graphic yet (D164).
	Deliverables []Deliverable `bson:"deliverables,omitempty" json:"deliverables,omitempty" jsonschema:"maxItems=12"`
}

// Deliverable is one place a project's video goes, and the shape it needs.
type Deliverable struct {
	Name     string `bson:"name" json:"name" jsonschema:"minLength=1,maxLength=80,description=Where it goes, in the person's words (by email, on the website, social shorts)."`
	Preset   string `bson:"preset,omitempty" json:"preset,omitempty" jsonschema:"enum=vertical-1080p,enum=landscape-1080p,enum=square-1080,description=The shape it needs."`
	Platform string `bson:"platform,omitempty" json:"platform,omitempty" jsonschema:"enum=generic,enum=youtube,enum=shorts,enum=tiktok,enum=reels"`
}

// ProjectRule is a plainly worded rule a project keeps (D166): a style rule
// ("First names only on screen") or a check (a problem the person doesn't
// want to see again). Claude reads them at the start of every turn; the
// quality check measures the ones it can (Measure).
type ProjectRule struct {
	ID   string `bson:"id" json:"id"`
	Text string `bson:"text" json:"text" jsonschema:"minLength=1,maxLength=300"`
	// Check: a problem to check for in every quality check of the project.
	Check bool `bson:"check,omitempty" json:"check,omitempty"`
	// Measure is what the quality check measures for it ("" none): captions-top,
	// captions-bottom, first-names, no-flashes, max-length:<seconds>[@<shape>],
	// shape:<landscape|vertical|square>, and for promises (D300) captions,
	// logo-end:<seconds>, logo-start:<seconds>, speech-over-music:<steps>,
	// disclosed, faces-in-frame, min-size:<pixels>, min-fps:<frames>.
	Measure string `bson:"measure,omitempty" json:"measure,omitempty" jsonschema:"maxLength=40"`
	// Promise: a promise made to the client ("captions on every video"), not
	// only a rule (D300). It must be measurable; every quality check measures
	// it and says the result, and a broken promise is a problem that holds
	// the version back from going out (D298).
	Promise bool      `bson:"promise,omitempty" json:"promise,omitempty"`
	From    string    `bson:"from,omitempty" json:"from,omitempty" jsonschema:"description=Where it came from (a conversation\\, a terminal)."`
	AddedBy Actor     `bson:"addedBy" json:"addedBy"`
	AddedAt time.Time `bson:"addedAt" json:"addedAt"`
	// House marks a house rule of the Content Settings a project uses (D193)
	// where a project's rules and house rules are read together (Claude's
	// context, the quality check); HouseOf is whose settings hold it. Never stored.
	House   bool   `bson:"-" json:"house,omitempty"`
	HouseOf string `bson:"-" json:"houseOf,omitempty"`
}

// SignoffState is whether a house sign-off (D200) was given for one of a
// project's cuts: the sign-off lives in the Content Settings, the OK in the
// project, so ticking it in one project never ticks it in another.
type SignoffState struct {
	// ID is the house sign-off's.
	ID     string     `bson:"id" json:"id"`
	Branch string     `bson:"branch,omitempty" json:"branch,omitempty" jsonschema:"description=The cut it is for (default main)."`
	Done   bool       `bson:"done,omitempty" json:"done,omitempty"`
	DoneBy *Actor     `bson:"doneBy,omitempty" json:"doneBy,omitempty"`
	DoneAt *time.Time `bson:"doneAt,omitempty" json:"doneAt,omitempty"`
	Note   string     `bson:"note,omitempty" json:"note,omitempty" jsonschema:"maxLength=300"`
	// Version is the cut's version number the OK was for (0 unknown).
	Version int `bson:"version,omitempty" json:"version,omitempty"`
}

// Signoff is one OK a cut needs before it goes out (D166): "Lena OKs the
// final version". Open ones remind people before approving, exporting and
// delivering; they never block.
type Signoff struct {
	ID     string     `bson:"id" json:"id"`
	Text   string     `bson:"text" json:"text" jsonschema:"minLength=1,maxLength=200"`
	Branch string     `bson:"branch,omitempty" json:"branch,omitempty" jsonschema:"description=The cut it is for (default main)."`
	Done   bool       `bson:"done,omitempty" json:"done,omitempty"`
	DoneBy *Actor     `bson:"doneBy,omitempty" json:"doneBy,omitempty"`
	DoneAt *time.Time `bson:"doneAt,omitempty" json:"doneAt,omitempty"`
	Note   string     `bson:"note,omitempty" json:"note,omitempty" jsonschema:"maxLength=300"`
	// Version is the cut's version number the OK was for (0 unknown).
	Version int       `bson:"version,omitempty" json:"version,omitempty"`
	AddedBy Actor     `bson:"addedBy" json:"addedBy"`
	AddedAt time.Time `bson:"addedAt" json:"addedAt"`
	// House marks a house sign-off of the Content Settings a project uses
	// (D200) where a cut's sign-offs are read together; HouseOf is whose
	// settings hold it. Never stored on the sign-off.
	House   bool   `bson:"-" json:"house,omitempty"`
	HouseOf string `bson:"-" json:"houseOf,omitempty"`
}

// BranchHead points a named branch at a revision.
type BranchHead struct {
	Head      string    `bson:"head" json:"head"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy Actor     `bson:"updatedBy" json:"updatedBy"`
	// Title is the cut's descriptive name ("Main open day video"), kept
	// outside the edit so renaming it never saves a version (D150); TitleBy
	// says who chose it (metarush | person). Empty: the dashboard shows
	// MetaRush's suggestion.
	Title   string `bson:"title,omitempty" json:"title,omitempty"`
	TitleBy string `bson:"titleBy,omitempty" json:"titleBy,omitempty" jsonschema:"enum=,enum=metarush,enum=person"`
}

// Project groups footage, revisions and renders.
type Project struct {
	ID           string                `bson:"_id" json:"id"`
	WorkspaceID  string                `bson:"workspaceId" json:"workspaceId"`
	Name         string                `bson:"name" json:"name"`
	Brief        Brief                 `bson:"brief" json:"brief"`
	Branches     map[string]BranchHead `bson:"branches" json:"branches"`
	RevisionSeq  int                   `bson:"revisionSeq" json:"revisionSeq"`
	AssetCount   int                   `bson:"assetCount" json:"assetCount"`
	CoverAssetID string                `bson:"coverAssetId,omitempty" json:"coverAssetId,omitempty"`
	// Archived: someone deleted the project (D196: deleting a project archives
	// it). It is gone from Home, lists and search and waits in Archived
	// projects until someone restores it or an owner clears it out for good.
	// ArchivedAt and ArchivedBy say when and who (absent for projects archived
	// before D196).
	Archived   bool       `bson:"archived" json:"archived"`
	ArchivedAt *time.Time `bson:"archivedAt,omitempty" json:"archivedAt,omitempty"`
	ArchivedBy *Actor     `bson:"archivedBy,omitempty" json:"archivedBy,omitempty"`
	// Purging: an owner started clearing it out for good (D196). Running the
	// clear-out again finishes it; it can't be restored meanwhile.
	Purging   *ProjectPurging `bson:"purging,omitempty" json:"purging,omitempty"`
	CreatedAt time.Time       `bson:"createdAt" json:"createdAt"`
	CreatedBy Actor           `bson:"createdBy" json:"createdBy"`
	UpdatedAt time.Time       `bson:"updatedAt" json:"updatedAt"`
	// UpdatedBy is the person whose change was last (D183; system work doesn't count).
	UpdatedBy *Actor `bson:"updatedBy,omitempty" json:"updatedBy,omitempty"`
	// Providers carries the project's spending budget by provider name, and
	// for generated video its model and resolutions. Its narration voice
	// choices are part of its Content Settings (D193; migration 36 moved them).
	Providers map[string]ProviderSettings `bson:"providers,omitempty" json:"providers,omitempty"`
	// DescribeScenes turns written scene descriptions on or off for this project
	// (nil: the workspace's choice).
	DescribeScenes *bool `bson:"describeScenes,omitempty" json:"describeScenes,omitempty"`
	// Rules are the project's plainly worded style rules and checks (D166).
	Rules []ProjectRule `bson:"rules,omitempty" json:"rules,omitempty" jsonschema:"maxItems=100"`
	// Signoffs are the OKs its cuts need before they go out (D166).
	Signoffs []Signoff `bson:"signoffs,omitempty" json:"signoffs,omitempty" jsonschema:"maxItems=50"`
	// HouseSignoffs are which house sign-offs (D200) of the Content Settings
	// it uses were given for its cuts.
	HouseSignoffs []SignoffState `bson:"houseSignoffs,omitempty" json:"houseSignoffs,omitempty" jsonschema:"maxItems=200"`
	// BrandKit was this project's own brand kit (D165) before Content
	// Settings (D192): migration 36 moves it into the project's own settings.
	// Nothing reads it any more.
	BrandKit *BrandKit `bson:"brandKit,omitempty" json:"brandKit,omitempty"`
	// Content says which Content Settings it uses (D192): nil, the default's.
	Content *ContentUse `bson:"content,omitempty" json:"content,omitempty"`
}

// ProjectPurging marks a project an owner started clearing out for good (D196).
type ProjectPurging struct {
	At time.Time `bson:"at" json:"at"`
	By Actor     `bson:"by" json:"by"`
}

// Asset states form the ingest state machine.
const (
	AssetStaging   = "staging"
	AssetVerified  = "verified"
	AssetAvailable = "available"
	AssetFailed    = "failed"
)

// Stream describes one media stream from ffprobe.
type Stream struct {
	Index      int     `bson:"index" json:"index"`
	Type       string  `bson:"type" json:"type"` // video | audio | subtitle | data
	Codec      string  `bson:"codec" json:"codec"`
	Profile    string  `bson:"profile,omitempty" json:"profile,omitempty"`
	Width      int     `bson:"width,omitempty" json:"width,omitempty"`
	Height     int     `bson:"height,omitempty" json:"height,omitempty"`
	PixFmt     string  `bson:"pixFmt,omitempty" json:"pixFmt,omitempty"`
	RFrameRate string  `bson:"rFrameRate,omitempty" json:"rFrameRate,omitempty"`
	AvgRate    string  `bson:"avgFrameRate,omitempty" json:"avgFrameRate,omitempty"`
	TimeBase   string  `bson:"timeBase,omitempty" json:"timeBase,omitempty"`
	Rotation   int     `bson:"rotation,omitempty" json:"rotation,omitempty"`
	ColorSpace string  `bson:"colorSpace,omitempty" json:"colorSpace,omitempty"`
	ColorTRC   string  `bson:"colorTransfer,omitempty" json:"colorTransfer,omitempty"`
	ColorPrim  string  `bson:"colorPrimaries,omitempty" json:"colorPrimaries,omitempty"`
	SampleRate int     `bson:"sampleRate,omitempty" json:"sampleRate,omitempty"`
	Channels   int     `bson:"channels,omitempty" json:"channels,omitempty"`
	Duration   float64 `bson:"duration,omitempty" json:"duration,omitempty"`
	BitRate    int64   `bson:"bitRate,omitempty" json:"bitRate,omitempty"`
}

// MediaInfo is the probed, normalized description of a media file.
type MediaInfo struct {
	Container    string  `bson:"container" json:"container"`
	Duration     float64 `bson:"duration" json:"duration"`
	BitRate      int64   `bson:"bitRate,omitempty" json:"bitRate,omitempty"`
	HasVideo     bool    `bson:"hasVideo" json:"hasVideo"`
	HasAudio     bool    `bson:"hasAudio" json:"hasAudio"`
	Width        int     `bson:"width,omitempty" json:"width,omitempty"`   // display width (rotation applied)
	Height       int     `bson:"height,omitempty" json:"height,omitempty"` // display height (rotation applied)
	FPS          float64 `bson:"fps,omitempty" json:"fps,omitempty"`
	VFR          bool    `bson:"vfr" json:"vfr"`
	HDR          bool    `bson:"hdr" json:"hdr"`
	Rotation     int     `bson:"rotation,omitempty" json:"rotation,omitempty"`
	CreationTime string  `bson:"creationTime,omitempty" json:"creationTime,omitempty"`
	// Device is the camera or phone the file says recorded it ("Apple iPhone
	// 15 Pro"), Software the app or system that wrote it ("iOS 18.1", "Zoom");
	// TagTitle and TagArtist the title and artist the file carries (music,
	// exported videos). From the file's own tags; empty when it has none.
	Device        string   `bson:"device,omitempty" json:"device,omitempty"`
	Software      string   `bson:"software,omitempty" json:"software,omitempty"`
	TagTitle      string   `bson:"tagTitle,omitempty" json:"tagTitle,omitempty"`
	TagArtist     string   `bson:"tagArtist,omitempty" json:"tagArtist,omitempty"`
	Streams       []Stream `bson:"streams" json:"streams"`
	ProbeWarnings []string `bson:"probeWarnings,omitempty" json:"probeWarnings,omitempty"`
}

// Variant kinds produced by derive jobs.
const (
	VariantProxy        = "proxy"
	VariantThumbnail    = "thumbnail"
	VariantContactSheet = "contact_sheet"
	VariantWaveform     = "waveform"
	VariantPeaks        = "peaks"
	VariantMezzanine    = "mezzanine"
)

// Variant is a derived file (proxy, thumbnail…) stored in the canonical store.
type Variant struct {
	Kind       string            `bson:"kind" json:"kind"`
	StorageKey string            `bson:"storageKey" json:"storageKey"`
	SHA256     string            `bson:"sha256" json:"sha256"`
	Size       int64             `bson:"size" json:"size"`
	MIME       string            `bson:"mime" json:"mime"`
	Width      int               `bson:"width,omitempty" json:"width,omitempty"`
	Height     int               `bson:"height,omitempty" json:"height,omitempty"`
	Duration   float64           `bson:"duration,omitempty" json:"duration,omitempty"`
	Generator  string            `bson:"generator" json:"generator"`
	Params     map[string]string `bson:"params,omitempty" json:"params,omitempty"`
	CreatedAt  time.Time         `bson:"createdAt" json:"createdAt"`
}

// Asset is an immutable original plus its derived variants.
type Asset struct {
	ID          string   `bson:"_id" json:"id"`
	WorkspaceID string   `bson:"workspaceId" json:"workspaceId"`
	ProjectIDs  []string `bson:"projectIds" json:"projectIds"`
	Kind        string   `bson:"kind" json:"kind"` // video | audio | image
	Name        string   `bson:"name" json:"name"` // original filename (immutable file info)
	// Title is the editable descriptive name (default: the filename without its
	// extension). Older assets have none; read paths fill it in (DisplayTitle).
	Title string `bson:"title,omitempty" json:"title,omitempty"`
	// TitleBy says who chose Title (D150): "" or "file" (the file name),
	// "metarush" (named from MetaRush's first look, a link's title, a
	// generated shot's prompt) or "person" (typed by someone; never replaced).
	TitleBy string `bson:"titleBy,omitempty" json:"titleBy,omitempty" jsonschema:"enum=,enum=file,enum=metarush,enum=person"`
	// NameAuto lets MetaRush rename it by itself while its first look runs
	// (sources added since descriptive names; cleared once the first analyses
	// settle or a person names it). Afterwards better names are suggestions.
	NameAuto bool `bson:"nameAuto,omitempty" json:"nameAuto,omitempty"`
	// NameSuggestion is MetaRush's descriptive name for it and why, when it
	// differs from the title (shown on the info card; applied by a person).
	NameSuggestion *NameSuggestion `bson:"nameSuggestion,omitempty" json:"nameSuggestion,omitempty"`
	// Fixes are its pre-processing (D152): the kind of source, what MetaRush
	// measured, the fixes a person ticked and the fixed copy in use.
	Fixes *SourceFixes `bson:"fixes,omitempty" json:"fixes,omitempty"`
	// Aliases are other filenames the same bytes arrived under ("Keep both" on a
	// duplicate, or a deduplicated re-ingest). The bytes are stored once.
	Aliases []AssetAlias `bson:"aliases,omitempty" json:"aliases,omitempty"`
	SHA256  string       `bson:"sha256" json:"sha256"`
	Size    int64        `bson:"size" json:"size"`
	// Fingerprint is a cheap pre-upload identity: "fp1:" + SHA-256 of the first and
	// last MiB (ingest.Fingerprint). Older assets get it on demand.
	Fingerprint string `bson:"fingerprint,omitempty" json:"fingerprint,omitempty"`
	// Sample is the sampled verification hash ("sp1:", media.SampleFile) that
	// confirms a fingerprint match before a file is treated as a duplicate.
	Sample      string             `bson:"sample,omitempty" json:"sample,omitempty"`
	MIME        string             `bson:"mime" json:"mime"`
	StorageKey  string             `bson:"storageKey" json:"storageKey"`
	State       string             `bson:"state" json:"state"`
	StateReason string             `bson:"stateReason,omitempty" json:"stateReason,omitempty"`
	Media       MediaInfo          `bson:"media" json:"media"`
	Variants    map[string]Variant `bson:"variants" json:"variants"`
	Tags        []string           `bson:"tags,omitempty" json:"tags,omitempty"`
	Via         string             `bson:"via" json:"via"` // cli | dashboard | watch | extension
	Transcript  *TranscriptStatus  `bson:"transcript,omitempty" json:"transcript,omitempty"`
	// Language is the language the clip is spoken in (D243): heard by
	// whisper.cpp's language detection, or set by a person; its words are
	// written down by the recognizer for that language.
	Language *SpokenLanguage `bson:"language,omitempty" json:"language,omitempty"`
	// TextFixes is where the transcript stands with people's fixes (D145):
	// its revision, which machine transcript it is, the fixes in force.
	TextFixes *TranscriptState `bson:"textFixes,omitempty" json:"textFixes,omitempty"`
	Speakers  *SpeakersStatus  `bson:"speakers,omitempty" json:"speakers,omitempty"`
	Rights    *MusicRights     `bson:"rights,omitempty" json:"rights,omitempty"` // licensing of music / third-party audio
	// Captions are captions files kept with the clip (D285): English
	// captions an ability made for a clip spoken in another language.
	Captions []AssetCaptions `bson:"captions,omitempty" json:"captions,omitempty"`
	// Source is where an imported clip came from (a YouTube, TikTok or X post):
	// platform, address, account, dates, stated license and the tool that fetched it.
	Source    *AssetSource      `bson:"source,omitempty" json:"source,omitempty"`
	Analysis  map[string]string `bson:"analysis,omitempty" json:"analysis,omitempty"` // kind -> state
	CreatedAt time.Time         `bson:"createdAt" json:"createdAt"`
	CreatedBy Actor             `bson:"createdBy" json:"createdBy"`
	UpdatedAt time.Time         `bson:"updatedAt" json:"updatedAt"`
	// UpdatedBy is the person whose change was last (D183; system work doesn't count).
	UpdatedBy *Actor `bson:"updatedBy,omitempty" json:"updatedBy,omitempty"`
	// CollectionIDs are the named shelves it is on ("B-roll", "Family"; D96).
	CollectionIDs []string `bson:"collectionIds,omitempty" json:"collectionIds,omitempty"`
	// ArchivedAt is when a person put it in the Archive, the one unnamed
	// "keep for later" shelf (D97); nil when it isn't there.
	ArchivedAt *time.Time `bson:"archivedAt,omitempty" json:"archivedAt,omitempty"`
	ArchivedBy *Actor     `bson:"archivedBy,omitempty" json:"archivedBy,omitempty"`
	// Trash is set while it is in the Trash (D98): it is in no project,
	// collection or Archive meanwhile, and Restore puts it back where it was.
	Trash *AssetTrash `bson:"trash,omitempty" json:"trash,omitempty"`
	// NotOnScreen are people a person said appear nowhere on screen in this
	// clip ("Not Lena anywhere in this clip", D189): never suggested for its
	// faces, and their voice is no evidence for them.
	NotOnScreen []string `bson:"notOnScreen,omitempty" json:"notOnScreen,omitempty"`
}

// AssetAlias is another filename an asset's bytes arrived under.
type AssetAlias struct {
	Name      string    `bson:"name" json:"name"`
	ProjectID string    `bson:"projectId,omitempty" json:"projectId,omitempty"`
	Via       string    `bson:"via" json:"via"`
	AddedBy   Actor     `bson:"addedBy" json:"addedBy"`
	AddedAt   time.Time `bson:"addedAt" json:"addedAt"`
}

// Duplicate actions (what happened to a file whose bytes were already stored).
const (
	DuplicateSkipped  = "skipped"   // nothing changed; the existing asset stays as it was
	DuplicateKeptBoth = "kept-both" // the filename was recorded as an alias (and linked to the target project)
	DuplicateSameFile = "same-file" // the same file again (same name); linked to the target project if any
)

// DuplicateOf describes the existing asset a file turned out to be identical to.
type DuplicateOf struct {
	AssetID    string   `json:"assetId"`
	Title      string   `json:"title"`
	Name       string   `json:"name" jsonschema:"description=The existing asset's original filename."`
	Where      string   `json:"where" jsonschema:"description=Inbox or the project names it is in."`
	ProjectIDs []string `json:"projectIds"`
	Action     string   `json:"action,omitempty" jsonschema:"enum=skipped,enum=kept-both,enum=same-file"`
}

// DefaultTitle is the display title for a filename: the base name without its extension.
func DefaultTitle(name string) string {
	base := name
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	if base = strings.TrimSpace(base); base == "" {
		return name
	}
	return base
}

// DisplayTitle is the asset's title, or the default derived from its filename.
// MadeNarration reports whether MetaRush made this audio itself for narration
// (scratch voice, AI voice, a cleaned-up version, a take from its recorder): its
// narration job or the recorder reports on it, so it gets no "file ready"
// notification or activity row of its own.
// TagEnhanced marks a clearer copy of a recording that MetaRush made (sound
// enhance): not footage of its own, so it stays out of the library's lists.
const TagEnhanced = "enhanced"

// HasTag reports whether the asset carries a tag.
func (a *Asset) HasTag(tag string) bool {
	for _, t := range a.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func (a *Asset) MadeNarration() bool {
	narration, made := false, false
	for _, t := range a.Tags {
		switch {
		case t == "voiceover":
			narration = true
		case t == "scratch" || t == "tts" || t == "ai-voice" || t == "recorded" || strings.HasPrefix(t, "cleanup:"):
			made = true
		}
	}
	return narration && made
}

func (a *Asset) DisplayTitle() string {
	if t := strings.TrimSpace(a.Title); t != "" {
		return t
	}
	return DefaultTitle(a.Name)
}

// HasName reports whether name is the asset's filename or one of its aliases.
func (a *Asset) HasName(name string) bool {
	if a.Name == name {
		return true
	}
	for _, al := range a.Aliases {
		if al.Name == name {
			return true
		}
	}
	return false
}

// TranscriptStatus summarizes an asset's transcript.
type TranscriptStatus struct {
	State     string    `bson:"state" json:"state"` // pending | available | failed | none
	Provider  string    `bson:"provider,omitempty" json:"provider,omitempty"`
	Model     string    `bson:"model,omitempty" json:"model,omitempty"`
	Precision string    `bson:"precision,omitempty" json:"precision,omitempty"` // word | segment
	Language  string    `bson:"language,omitempty" json:"language,omitempty"`
	Segments  int       `bson:"segments" json:"segments"`
	Words     int       `bson:"words" json:"words"`
	Speakers  int       `bson:"speakers,omitempty" json:"speakers,omitempty"`
	Error     string    `bson:"error,omitempty" json:"error,omitempty"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

// How a clip's language was decided (SpokenLanguage.By).
const (
	LanguageHeard    = "heard"    // whisper.cpp's language detection heard it
	LanguagePerson   = "person"   // a person said which language it is
	LanguageSettings = "settings" // the workspace's language: too little speech to tell, or detection is off
)

// SpokenLanguage is the language a clip is spoken in (D243).
type SpokenLanguage struct {
	// Code is the language's code as whisper.cpp writes it ("es", "ja").
	Code string `bson:"code" json:"code"`
	By   string `bson:"by" json:"by" jsonschema:"enum=heard,enum=person,enum=settings"`
	// Score is how sure the detection was, 0 to 1 (heard only).
	Score float64 `bson:"score,omitempty" json:"score,omitempty"`
	// Listened is how many seconds of the clip's speech it listened to.
	Listened float64 `bson:"listened,omitempty" json:"listened,omitempty"`
	// Heard is another language the detection heard, not surely enough to
	// switch the clip away from the workspace's language (By "settings").
	Heard string `bson:"heard,omitempty" json:"heard,omitempty"`
	// UserID is who set it (person only).
	UserID string    `bson:"userId,omitempty" json:"userId,omitempty"`
	At     time.Time `bson:"at" json:"at"`
}

// IngestSession tracks one file's journey through staging → verified → available.
type IngestSession struct {
	ID             string      `bson:"_id" json:"id"`
	WorkspaceID    string      `bson:"workspaceId" json:"workspaceId"`
	InstallationID string      `bson:"installationId" json:"installationId"`
	ProjectID      string      `bson:"projectId,omitempty" json:"projectId,omitempty"`
	SourceName     string      `bson:"sourceName" json:"sourceName"`
	Size           int64       `bson:"size" json:"size"`
	SHA256         string      `bson:"sha256,omitempty" json:"sha256,omitempty"`
	AssetID        string      `bson:"assetId,omitempty" json:"assetId,omitempty"`
	State          string      `bson:"state" json:"state"` // hashing | uploading | verifying | available | deduped | failed
	Upload         UploadState `bson:"upload" json:"upload"`
	BytesDone      int64       `bson:"bytesDone" json:"bytesDone"`
	Error          string      `bson:"error,omitempty" json:"error,omitempty"`
	Via            string      `bson:"via" json:"via"`
	// JobID is the link import this ingest belongs to (its activity row covers it).
	JobID     string    `bson:"jobId,omitempty" json:"jobId,omitempty"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

// UploadState persists resumable-upload progress (provider specific).
type UploadState struct {
	Key       string `bson:"key,omitempty" json:"key,omitempty"`
	UploadID  string `bson:"uploadId,omitempty" json:"uploadId,omitempty"`
	PartSize  int64  `bson:"partSize,omitempty" json:"partSize,omitempty"`
	PartsDone int    `bson:"partsDone,omitempty" json:"partsDone,omitempty"`
	// SessionURI is a GCS resumable-upload session. It authorizes writing this
	// one object (with the checksums declared at start) for up to a week, so it
	// is stored but never printed by the CLI or sent to the dashboard.
	SessionURI string `bson:"sessionUri,omitempty" json:"-"`
	Offset     int64  `bson:"offset,omitempty" json:"offset,omitempty"` // bytes the store has committed
}

// Word is a timed word in a transcript (seconds in source time).
type Word struct {
	Text  string  `bson:"w" json:"w"`
	From  float64 `bson:"f" json:"f"`
	To    float64 `bson:"t" json:"t"`
	Score float64 `bson:"p,omitempty" json:"p,omitempty"`
	// Fixed: a person wrote this word (by hand, with find and replace or from
	// the spelling list), so it has no confidence score (D145).
	Fixed bool `bson:"x,omitempty" json:"x,omitempty"`
}

// TranscriptSegment is one timed segment of speech in source time.
type TranscriptSegment struct {
	ID          string  `bson:"_id" json:"id"`
	WorkspaceID string  `bson:"workspaceId" json:"workspaceId"`
	AssetID     string  `bson:"assetId" json:"assetId"`
	Index       int     `bson:"idx" json:"idx"`
	From        float64 `bson:"from" json:"from"`
	To          float64 `bson:"to" json:"to"`
	Text        string  `bson:"text" json:"text"`
	Speaker     string  `bson:"speaker,omitempty" json:"speaker,omitempty"` // voice label in this asset (speaker_00…)
	// SpeakerName is the voice's name once someone named it (or it was
	// recognized); kept in sync with the asset's speakers.
	SpeakerName string `bson:"speakerName,omitempty" json:"speakerName,omitempty"`
	// SpeakerFrom is the voice label the segment had before its voice was merged
	// into another one (so the merge can be undone).
	SpeakerFrom string    `bson:"speakerFrom,omitempty" json:"-"`
	Words       []Word    `bson:"words,omitempty" json:"words,omitempty"`
	Provider    string    `bson:"provider" json:"provider"`
	Model       string    `bson:"model" json:"model"`
	CreatedAt   time.Time `bson:"createdAt" json:"createdAt"`
}

// Insight is grounded analysis output (technical detectors now, AI providers later).
type Insight struct {
	ID            string         `bson:"_id" json:"id"`
	WorkspaceID   string         `bson:"workspaceId" json:"workspaceId"`
	AssetID       string         `bson:"assetId" json:"assetId"`
	Kind          string         `bson:"kind" json:"kind"` // technical | visual | summary
	Provider      string         `bson:"provider" json:"provider"`
	Model         string         `bson:"model,omitempty" json:"model,omitempty"`
	PromptVersion string         `bson:"promptVersion,omitempty" json:"promptVersion,omitempty"`
	Digest        string         `bson:"digest" json:"digest"` // asset sha + provider/model/prompt version
	Data          map[string]any `bson:"data" json:"data"`
	CreatedAt     time.Time      `bson:"createdAt" json:"createdAt"`
	// Media is the SHA-256 of the file analysed when it was a fixed copy
	// (D152); empty for the original. A version reads the analysis of the
	// media it plays, so its sound and framing never move when a copy's
	// analysis arrives.
	Media string `bson:"media,omitempty" json:"media,omitempty"`
}

// Approval states for revisions.
const (
	ApprovalDraft    = "draft"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
)

// Approval is the only mutable part of a revision, and only humans change it.
type Approval struct {
	State string     `bson:"state" json:"state"`
	By    *Actor     `bson:"by,omitempty" json:"by,omitempty"`
	At    *time.Time `bson:"at,omitempty" json:"at,omitempty"`
	Note  string     `bson:"note,omitempty" json:"note,omitempty"`
}

// Revision is an immutable, content-hashed edit spec in a branch's lineage.
type Revision struct {
	ID          string           `bson:"_id" json:"id"`
	WorkspaceID string           `bson:"workspaceId" json:"workspaceId"`
	ProjectID   string           `bson:"projectId" json:"projectId"`
	Branch      string           `bson:"branch" json:"branch"`
	Number      int              `bson:"number" json:"number"`
	ParentID    string           `bson:"parentId,omitempty" json:"parentId,omitempty"`
	RebasedFrom string           `bson:"rebasedFrom,omitempty" json:"rebasedFrom,omitempty"`
	Spec        editspec.Spec    `bson:"spec" json:"spec"`
	SpecHash    string           `bson:"specHash" json:"specHash"`
	Ops         []editspec.Op    `bson:"ops,omitempty" json:"ops,omitempty"`
	Detached    bool             `bson:"detached,omitempty" json:"detached,omitempty"` // lost a head race; never shown as a branch tip
	Instruction string           `bson:"instruction,omitempty" json:"instruction,omitempty"`
	Rationale   string           `bson:"rationale,omitempty" json:"rationale,omitempty"`
	Origin      string           `bson:"origin,omitempty" json:"origin,omitempty"` // e.g. assistant:<run>/<file> for an applied assistant proposal
	Warnings    []editspec.Issue `bson:"warnings,omitempty" json:"warnings,omitempty"`
	// Transcripts are the transcripts the version was made with, one per clip
	// that had one (D146): it renders those words forever. Versions saved
	// before have none and use each clip's transcript as the machine wrote it.
	Transcripts []TranscriptPin `bson:"transcripts,omitempty" json:"transcripts,omitempty"`
	Duration    float64         `bson:"duration" json:"duration"`
	Approval    Approval        `bson:"approval" json:"approval"`
	CreatedBy   Actor           `bson:"createdBy" json:"createdBy"`
	CreatedAt   time.Time       `bson:"createdAt" json:"createdAt"`
	// Note says in a few words what this version changed ("flicker fixes and
	// headline cards", D150). Like the approval it sits outside the spec and
	// its hash, so changing it never changes what the version plays; NoteBy
	// says who wrote it (metarush | claude | person | agent).
	Note   string `bson:"note,omitempty" json:"note,omitempty"`
	NoteBy string `bson:"noteBy,omitempty" json:"noteBy,omitempty" jsonschema:"enum=,enum=metarush,enum=claude,enum=person,enum=agent"`
	// ReviewAsked records that someone asked to have this version reviewed
	// (D188): they asked for a review copy of it, or pressed Ask for review.
	// Like the approval it sits outside the spec and its hash. "Awaiting your
	// review" lists versions Claude made and versions someone asked about,
	// not every hand edit saved in the background.
	ReviewAsked *ReviewAsk `bson:"reviewAsked,omitempty" json:"reviewAsked,omitempty"`
	// DeliveryNotes are the title, description, chapters and show notes that
	// go out with this version (D237); outside the spec and its hash.
	DeliveryNotes *DeliveryNotes `bson:"deliveryNotes,omitempty" json:"deliveryNotes,omitempty"`
	// DraftID: an agent's draft (D248). The version is not in the cut (no
	// branch's latest version) until a person approves the draft.
	DraftID string `bson:"draftId,omitempty" json:"draftId,omitempty"`
	// Includes are the drafts whose changes this version carries because it
	// was made on top of their versions (D252): their changes are in it.
	Includes []string `bson:"includes,omitempty" json:"includes,omitempty"`
	// Publications are the times a person posted it to a video platform (D305).
	Publications []Publication `bson:"publications,omitempty" json:"publications,omitempty"`
	// ShipOverrides are the times a person sent this version out although
	// something held it (D299): who, when, their note and what held it.
	// Outside the spec and its hash, like the approval.
	ShipOverrides []ShipOverride `bson:"shipOverrides,omitempty" json:"shipOverrides,omitempty"`
	// SentOut are the times it went out past what held it, with the notes
	// that let it out (D299): the receipt says "went out" only for these.
	// Both lists only grow.
	SentOut []ShipSent `bson:"sentOut,omitempty" json:"sentOut,omitempty"`
}

// How a version was asked to be reviewed (ReviewAsk.How).
const (
	ReviewAskCopy  = "review-copy" // someone asked for a review copy of it
	ReviewAskAsked = "asked"       // someone pressed Ask for review
)

// ReviewAsk is who asked to have a version reviewed, when and how.
type ReviewAsk struct {
	By  Actor     `bson:"by" json:"by"`
	At  time.Time `bson:"at" json:"at"`
	How string    `bson:"how" json:"how" jsonschema:"enum=review-copy,enum=asked,description=review-copy: they asked for a review copy of it; asked: they pressed Ask for review."`
}

// Job states.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCanceled  = "canceled"
)

// Lease marks a job as owned by one worker until ExpiresAt.
type Lease struct {
	Owner     string    `bson:"owner" json:"owner"`
	OwnerName string    `bson:"ownerName,omitempty" json:"ownerName,omitempty"`
	ExpiresAt time.Time `bson:"expiresAt" json:"expiresAt"`
}

// Progress is a job's latest progress report.
type Progress struct {
	Pct     float64 `bson:"pct" json:"pct"`
	Message string  `bson:"message,omitempty" json:"message,omitempty"`
	// Stage, Done/Total and Rate describe a job with byte progress (a link
	// import: looking up, downloading, adding); other jobs leave them empty.
	Stage string  `bson:"stage,omitempty" json:"stage,omitempty"`
	Done  int64   `bson:"done,omitempty" json:"done,omitempty" jsonschema:"description=Bytes done in this stage."`
	Total int64   `bson:"total,omitempty" json:"total,omitempty" jsonschema:"description=Bytes in this stage (0 when unknown)."`
	Rate  float64 `bson:"rate,omitempty" json:"rate,omitempty" jsonschema:"description=Bytes per second."`
}

// Attempt records one execution of a job.
type Attempt struct {
	Worker     string     `bson:"worker" json:"worker"`                             // installation ID
	WorkerName string     `bson:"workerName,omitempty" json:"workerName,omitempty"` // its name when the attempt started
	StartedAt  time.Time  `bson:"startedAt" json:"startedAt"`
	EndedAt    *time.Time `bson:"endedAt,omitempty" json:"endedAt,omitempty"`
	Outcome    string     `bson:"outcome,omitempty" json:"outcome,omitempty"`
	Error      string     `bson:"error,omitempty" json:"error,omitempty"`
}

// Job is a durable unit of work leased atomically by workers.
type Job struct {
	ID            string `bson:"_id" json:"id"`
	WorkspaceID   string `bson:"workspaceId" json:"workspaceId"`
	Type          string `bson:"type" json:"type"`
	SchemaVersion int    `bson:"schemaVersion" json:"schemaVersion"`
	State         string `bson:"state" json:"state"`
	Priority      int    `bson:"priority" json:"priority"`
	Heavy         bool   `bson:"heavy" json:"heavy"`
	ProjectID     string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	AssetID       string `bson:"assetId,omitempty" json:"assetId,omitempty"`
	RenderID      string `bson:"renderId,omitempty" json:"renderId,omitempty"`
	// Installation pins the job to one machine: only that installation's
	// worker leases it (a link import runs where it was asked for: that
	// machine's network and, when asked, its browser sign-in).
	Installation    string         `bson:"installation,omitempty" json:"installation,omitempty"`
	Payload         map[string]any `bson:"payload" json:"payload"`
	IdempotencyKey  string         `bson:"idempotencyKey" json:"idempotencyKey"`
	Lease           *Lease         `bson:"lease,omitempty" json:"lease,omitempty"`
	AttemptCount    int            `bson:"attemptCount" json:"attemptCount"`
	MaxAttempts     int            `bson:"maxAttempts" json:"maxAttempts"`
	Attempts        []Attempt      `bson:"attempts" json:"attempts"`
	NextRunAt       time.Time      `bson:"nextRunAt" json:"nextRunAt"`
	CancelRequested bool           `bson:"cancelRequested" json:"cancelRequested"`
	// AllowOnBattery lets a heavy job run on a worker that would otherwise defer
	// it for battery ("Process now" in the dashboard, `metarush job expedite`).
	AllowOnBattery bool           `bson:"allowOnBattery,omitempty" json:"allowOnBattery,omitempty"`
	Progress       Progress       `bson:"progress" json:"progress"`
	Result         map[string]any `bson:"result,omitempty" json:"result,omitempty"`
	Error          string         `bson:"error,omitempty" json:"error,omitempty"`
	CreatedBy      Actor          `bson:"createdBy" json:"createdBy"`
	CreatedAt      time.Time      `bson:"createdAt" json:"createdAt"`
	UpdatedAt      time.Time      `bson:"updatedAt" json:"updatedAt"`
	FinishedAt     *time.Time     `bson:"finishedAt,omitempty" json:"finishedAt,omitempty"`
}

// WorkerDevice advertises a worker's capabilities and liveness.
type WorkerDevice struct {
	ID          string            `bson:"_id" json:"id"` // installation ID
	WorkspaceID string            `bson:"workspaceId" json:"workspaceId"`
	Name        string            `bson:"name" json:"name"`
	Hostname    string            `bson:"hostname" json:"hostname"`
	OS          string            `bson:"os" json:"os"`
	Arch        string            `bson:"arch" json:"arch"`
	CPUs        int               `bson:"cpus" json:"cpus"`
	MemoryGB    float64           `bson:"memoryGB" json:"memoryGB"`
	Tools       map[string]string `bson:"tools" json:"tools"`
	JobTypes    []string          `bson:"jobTypes" json:"jobTypes"`
	// Lacks are kinds of work it can't do for want of a component, with
	// what a waiting job says (D326).
	Lacks        []WorkerLack `bson:"lacks,omitempty" json:"lacks,omitempty"`
	Concurrency  int          `bson:"concurrency" json:"concurrency"`
	Running      int          `bson:"running" json:"running"`
	AcceptsHeavy bool         `bson:"acceptsHeavy" json:"acceptsHeavy"` // takes heavy jobs right now (config allows them and not deferring for battery)
	OnBattery    bool         `bson:"onBattery" json:"onBattery"`
	// Battery state and the heavy-job policy (worker.heavyBatteryThreshold).
	BatteryPercent        *int       `bson:"batteryPercent,omitempty" json:"batteryPercent,omitempty"`
	Charging              bool       `bson:"charging,omitempty" json:"charging,omitempty"`
	HeavyBatteryThreshold int        `bson:"heavyBatteryThreshold" json:"heavyBatteryThreshold"` // percent; 0 = never defer
	HeavyEnabled          bool       `bson:"heavyEnabled" json:"heavyEnabled"`                   // config allows heavy jobs at all
	DeferringHeavy        bool       `bson:"deferringHeavy,omitempty" json:"deferringHeavy,omitempty"`
	DeferReason           string     `bson:"deferReason,omitempty" json:"deferReason,omitempty"`
	DeferSince            *time.Time `bson:"deferSince,omitempty" json:"deferSince,omitempty"`
	Version               string     `bson:"version" json:"version"`
	StartedAt             time.Time  `bson:"startedAt" json:"startedAt"`
	LastHeartbeat         time.Time  `bson:"lastHeartbeat" json:"lastHeartbeat"`
}

// WorkerLack is a kind of work a computer can't do until a component is
// added: the job type, the component and the plain words ("Making
// transcripts needs the transcriber and its speech models (about 550 MB).
// Add it in Settings → Components.").
type WorkerLack struct {
	JobType   string `bson:"jobType" json:"jobType"`
	Component string `bson:"component" json:"component"`
	Says      string `bson:"says" json:"says"`
}

// Render states.
const (
	RenderQueued    = "queued"
	RenderRendering = "rendering"
	RenderUploading = "uploading"
	RenderAvailable = "available"
	RenderFailed    = "failed"
	RenderCanceled  = "canceled"
)

// RenderOutput describes a finished render artifact in the canonical store.
type RenderOutput struct {
	StorageKey   string  `bson:"storageKey" json:"storageKey"`
	SHA256       string  `bson:"sha256" json:"sha256"`
	Size         int64   `bson:"size" json:"size"`
	Duration     float64 `bson:"duration" json:"duration"`
	Width        int     `bson:"width" json:"width"`
	Height       int     `bson:"height" json:"height"`
	FPS          float64 `bson:"fps" json:"fps"`
	ThumbnailKey string  `bson:"thumbnailKey,omitempty" json:"thumbnailKey,omitempty"`
}

// Render is a request to turn a revision into a playable file.
type Render struct {
	ID               string            `bson:"_id" json:"id"`
	WorkspaceID      string            `bson:"workspaceId" json:"workspaceId"`
	ProjectID        string            `bson:"projectId" json:"projectId"`
	RevisionID       string            `bson:"revisionId" json:"revisionId"`
	RevisionNumber   int               `bson:"revisionNumber" json:"revisionNumber"`
	SpecHash         string            `bson:"specHash" json:"specHash"`
	Preset           string            `bson:"preset" json:"preset"`
	Quality          string            `bson:"quality" json:"quality"` // review | final
	JobID            string            `bson:"jobId" json:"jobId"`
	State            string            `bson:"state" json:"state"`
	Output           *RenderOutput     `bson:"output,omitempty" json:"output,omitempty"`
	RendererVersion  string            `bson:"rendererVersion,omitempty" json:"rendererVersion,omitempty"`
	RendererID       string            `bson:"rendererId,omitempty" json:"rendererId,omitempty"` // the renderer that drew it: version + package digest (D75), or "checkout"
	TemplateVersions map[string]string `bson:"templateVersions,omitempty" json:"templateVersions,omitempty"`
	RenderedBy       string            `bson:"renderedBy,omitempty" json:"renderedBy,omitempty"`         // installation ID
	RenderedByName   string            `bson:"renderedByName,omitempty" json:"renderedByName,omitempty"` // its name at render time
	Error            string            `bson:"error,omitempty" json:"error,omitempty"`
	CreatedBy        Actor             `bson:"createdBy" json:"createdBy"`
	CreatedAt        time.Time         `bson:"createdAt" json:"createdAt"`
	CompletedAt      *time.Time        `bson:"completedAt,omitempty" json:"completedAt,omitempty"`
	QA               *RenderQA         `bson:"qa,omitempty" json:"qa,omitempty"` // latest QA summary (qa.run)
	// Follow: MetaRush made this review copy by itself, because it follows the
	// cut's latest version (D187); nobody asked for it.
	Follow bool `bson:"follow,omitempty" json:"follow,omitempty" jsonschema:"description=MetaRush made this review copy by itself to follow the cut's latest version."`
}

// RenderLabel names a render in plain words (D188): "Review copy of version
// 3", "Final render of version 3".
func RenderLabel(quality string, revisionNumber int) string {
	q := "Render"
	switch quality {
	case "review":
		q = "Review copy"
	case "":
	default:
		q = strings.ToUpper(quality[:1]) + quality[1:] + " render"
	}
	return q + " of version " + strconv.Itoa(revisionNumber)
}

// Comment is a review note on a project, revision or asset (optionally time-pinned).
type Comment struct {
	ID          string    `bson:"_id" json:"id"`
	WorkspaceID string    `bson:"workspaceId" json:"workspaceId"`
	ProjectID   string    `bson:"projectId" json:"projectId"`
	RevisionID  string    `bson:"revisionId,omitempty" json:"revisionId,omitempty"`
	AssetID     string    `bson:"assetId,omitempty" json:"assetId,omitempty"`
	At          *float64  `bson:"at,omitempty" json:"at,omitempty"`
	Body        string    `bson:"body" json:"body"`
	Author      Actor     `bson:"author" json:"author"`
	Resolved    bool      `bson:"resolved" json:"resolved"`
	CreatedAt   time.Time `bson:"createdAt" json:"createdAt"`
	// Outside: someone outside the workspace left it through a review link
	// (D315); Author.UserName is "<their name> (outside)".
	Outside *CommentOutside `bson:"outside,omitempty" json:"outside,omitempty"`
	// Quoted is set when an AI reads an outside reviewer's comment: Body is
	// their words in quotes, and this says how to read them. Never stored.
	Quoted string `bson:"-" json:"quoted,omitempty"`
}

// AuditEvent is an append-only record of a meaningful action.
type AuditEvent struct {
	ID          string         `bson:"_id" json:"id"`
	WorkspaceID string         `bson:"workspaceId" json:"workspaceId"`
	At          time.Time      `bson:"at" json:"at"`
	Actor       Actor          `bson:"actor" json:"actor"`
	Action      string         `bson:"action" json:"action"`
	TargetType  string         `bson:"targetType" json:"targetType"`
	TargetID    string         `bson:"targetId" json:"targetId"`
	ProjectID   string         `bson:"projectId,omitempty" json:"projectId,omitempty"`
	Details     map[string]any `bson:"details,omitempty" json:"details,omitempty"`
}
