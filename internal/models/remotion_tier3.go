package models

import "time"

// Remotion sources, Tier 3 (D173–D176, docs/remotion-sources.md): a source's own
// build steps run as a refresh a person starts, and an approved version's
// changes sent back to the repository it came from.

// RemotionKindRefresh is a version made by a refresh (D175): the outputs of
// the project's own steps on top of the source's newest version.
const RemotionKindRefresh = "refresh"

// States of a refresh and of a push back.
const (
	RemotionRunQueued   = "queued"
	RemotionRunRunning  = "running"
	RemotionRunDone     = "done"
	RemotionRunFailed   = "failed"
	RemotionRunCanceled = "canceled"
)

// Kinds of a refresh step.
const (
	RemotionStepScript = "script" // npm run <script> (a script in its package.json)
	RemotionStepNode   = "node"   // node <file> (one of its files)
	RemotionStepNPX    = "npx"    // npx --no <tool> (a tool its packages already have)
)

// RemotionRefreshSteps are what a person picked for a refresh (D173): the
// steps in order, the keys passed and the places the steps may write.
type RemotionRefreshSteps struct {
	Steps   []RemotionStep `bson:"steps" json:"steps"`
	Keys    []RemotionKey  `bson:"keys" json:"keys"`
	Outputs []string       `bson:"outputs" json:"outputs" jsonschema:"description=Folders (public/voiceover/)\\, files or patterns (public/**/*.wav) the steps may write; only changes there become part of the new version."`
}

// RemotionStep is one step of a refresh.
type RemotionStep struct {
	// Run is the step as written: "npm run vo", "node scripts/make.mjs --fast", "npx --no playwright install chromium".
	Run  string   `bson:"run" json:"run"`
	Kind string   `bson:"kind" json:"kind" jsonschema:"enum=script,enum=node,enum=npx"`
	Name string   `bson:"name" json:"name" jsonschema:"description=The script\\, file or tool."`
	Args []string `bson:"args,omitempty" json:"args,omitempty"`
	// Command is the script's own line in package.json (what npm runs).
	Command string `bson:"command,omitempty" json:"command,omitempty"`
}

// RemotionKey is one key a refresh passes: a provider's key from Settings →
// Integrations on the computer that runs it, under the variable name the
// project reads.
type RemotionKey struct {
	Provider string `bson:"provider" json:"provider" jsonschema:"enum=elevenlabs,enum=gemini"`
	Env      string `bson:"env" json:"env" jsonschema:"pattern=^[A-Z][A-Z0-9_]{1\\,63}$"`
}

// RemotionHost is a place a refresh may reach, and why MetaRush thinks so.
type RemotionHost struct {
	Host string `bson:"host" json:"host"`
	Why  string `bson:"why" json:"why"`
}

// RemotionStepRun is how one step of a refresh went.
type RemotionStepRun struct {
	Run     string  `bson:"run" json:"run"`
	OK      bool    `bson:"ok" json:"ok"`
	Exit    int     `bson:"exit" json:"exit"`
	Seconds float64 `bson:"seconds" json:"seconds"`
	// Last is the end of what it printed when it failed (keys hidden).
	Last string `bson:"last,omitempty" json:"last,omitempty"`
}

// RemotionFileLeftOut is a file a refresh changed that didn't become part of
// the new version, and why.
type RemotionFileLeftOut struct {
	Path string `bson:"path" json:"path"`
	Why  string `bson:"why" json:"why"`
}

// RemotionRefresh is one refresh (remotion_refreshes, rmf_…).
type RemotionRefresh struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"-"`
	SourceID    string `bson:"sourceId" json:"sourceId"`
	ProjectID   string `bson:"projectId" json:"projectId"`
	// Number counts a source's refreshes (1, 2, 3…).
	Number       int                  `bson:"number" json:"number"`
	RefreshSteps RemotionRefreshSteps `bson:"refreshSteps" json:"refreshSteps"`
	Hosts        []RemotionHost       `bson:"hosts" json:"hosts"`
	// Fingerprint is the plan the person saw and started (D173).
	Fingerprint string `bson:"fingerprint" json:"fingerprint"`
	// From is the version it started from; Version the one it made.
	From          string `bson:"from" json:"from"`
	FromNumber    int    `bson:"fromNumber" json:"fromNumber"`
	Version       string `bson:"version,omitempty" json:"version,omitempty"`
	VersionNumber int    `bson:"versionNumber,omitempty" json:"versionNumber,omitempty"`
	State         string `bson:"state" json:"state" jsonschema:"enum=queued,enum=running,enum=done,enum=failed,enum=canceled"`
	By            Actor  `bson:"by" json:"by"`
	// Machine is the computer that runs it (its keys), Installation its ID.
	Machine      string `bson:"machine" json:"machine"`
	Installation string `bson:"installation" json:"-"`
	JobID        string `bson:"jobId,omitempty" json:"jobId,omitempty"`
	// Isolation is how its steps were kept apart (D174): macos-sandbox or bubblewrap.
	Isolation string                `bson:"isolation,omitempty" json:"isolation,omitempty"`
	Steps     []RemotionStepRun     `bson:"steps,omitempty" json:"steps,omitempty"`
	Log       *RemotionObject       `bson:"log,omitempty" json:"log,omitempty" jsonschema:"description=What its steps printed (keys hidden)."`
	Changes   []RemotionFileChange  `bson:"changes,omitempty" json:"changes,omitempty" jsonschema:"description=What became part of the new version."`
	LeftOut   []RemotionFileLeftOut `bson:"leftOut,omitempty" json:"leftOut,omitempty" jsonschema:"description=Files it changed that were left out\\, and why."`
	Summary   string                `bson:"summary,omitempty" json:"summary,omitempty"`
	Error     string                `bson:"error,omitempty" json:"error,omitempty"`
	StartedAt *time.Time            `bson:"startedAt,omitempty" json:"startedAt,omitempty"`
	// Ran is set when its steps began: a refresh cut short is never run again by itself.
	Ran        bool       `bson:"ran,omitempty" json:"ran,omitempty"`
	FinishedAt *time.Time `bson:"finishedAt,omitempty" json:"finishedAt,omitempty"`
	CreatedAt  time.Time  `bson:"createdAt" json:"createdAt"`
	UpdatedAt  time.Time  `bson:"updatedAt" json:"updatedAt"`
}

// Kinds of the place a push back goes (D176).
const (
	RemotionPushGitHub = "github" // a GitHub repository (a pull request can be opened)
	RemotionPushGit    = "git"    // any other git address (another host, a path, file://)
	RemotionPushFolder = "folder" // a folder's own repository on this computer
)

// RemotionPushTarget is where a push back goes.
type RemotionPushTarget struct {
	Kind string `bson:"kind" json:"kind" jsonschema:"enum=github,enum=git,enum=folder"`
	// Where says it for people: github.com/acme/teaser, the folder teaser.
	Where string `bson:"where" json:"where"`
	// URL is the address pushed to (never with a password).
	URL string `bson:"url" json:"url"`
	// Repo is owner/name on GitHub.
	Repo string `bson:"repo,omitempty" json:"repo,omitempty"`
	// Default is its default branch (never pushed to).
	Default string `bson:"default,omitempty" json:"default,omitempty"`
}

// RemotionPush is one push back (remotion_pushes, rmp_…).
type RemotionPush struct {
	ID            string             `bson:"_id" json:"id"`
	WorkspaceID   string             `bson:"workspaceId" json:"-"`
	SourceID      string             `bson:"sourceId" json:"sourceId"`
	ProjectID     string             `bson:"projectId" json:"projectId"`
	Number        int                `bson:"number" json:"number"`
	Version       string             `bson:"version" json:"version"`
	VersionNumber int                `bson:"versionNumber" json:"versionNumber"`
	Target        RemotionPushTarget `bson:"target" json:"target"`
	Branch        string             `bson:"branch" json:"branch"`
	// Base is the original commit; Parent the commit the new one goes on (the
	// base, or the branch's tip when MetaRush made the branch); Commit the new one.
	Base   string `bson:"base" json:"base"`
	Parent string `bson:"parent" json:"parent"`
	Commit string `bson:"commit,omitempty" json:"commit,omitempty"`
	// PR: open a pull request (GitHub); PRURL the one opened or found.
	PR          bool                 `bson:"pr" json:"pr"`
	PRURL       string               `bson:"prUrl,omitempty" json:"prUrl,omitempty"`
	Words       bool                 `bson:"words" json:"words" jsonschema:"description=The current word settings were written into the code."`
	Changes     []RemotionFileChange `bson:"changes" json:"changes"`
	Fingerprint string               `bson:"fingerprint" json:"fingerprint"`
	State       string               `bson:"state" json:"state" jsonschema:"enum=queued,enum=running,enum=done,enum=failed,enum=canceled"`
	By          Actor                `bson:"by" json:"by"`
	Machine     string               `bson:"machine" json:"machine"`
	JobID       string               `bson:"jobId,omitempty" json:"jobId,omitempty"`
	Summary     string               `bson:"summary,omitempty" json:"summary,omitempty"`
	Error       string               `bson:"error,omitempty" json:"error,omitempty"`
	FinishedAt  *time.Time           `bson:"finishedAt,omitempty" json:"finishedAt,omitempty"`
	CreatedAt   time.Time            `bson:"createdAt" json:"createdAt"`
	UpdatedAt   time.Time            `bson:"updatedAt" json:"updatedAt"`
}
