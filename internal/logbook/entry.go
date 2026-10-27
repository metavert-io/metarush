// Package logbook is MetaRush's activity log: a record of everything it does —
// every operation call, job lifecycle event, external program run and Claude
// prompt/response — for observability, bug reports and the improvement
// pipeline (`metarush improve`). Entries live in the workspace database
// (collection log_entries); long texts go to the media store gzipped and
// content-addressed. Logging is best effort: entries are buffered and written
// in the background, spooled to <home>/state/log-spool when the database or the
// media store is unreachable, and never slow or fail the work they describe.
// Everything is redacted (configured secret values, known credential patterns,
// secret-looking field names) before it is written anywhere.
package logbook

import (
	"time"
)

// EntryPrefix prefixes entry IDs (log_<ulid>).
const EntryPrefix = "log"

// Entry kinds.
const (
	KindOp        = "op"         // one operation call
	KindOpSummary = "op_summary" // dashboard reads, counted per operation and channel
	KindJob       = "job"        // a job lifecycle event
	KindTool      = "tool"       // one run of an external program
	KindClaude    = "claude"     // one prompt and response exchange with Claude Code
	KindAction    = "action"     // a person's decision on a change Claude asked for
	KindEvent     = "event"      // something MetaRush did outside an operation (watched folder, uploads)
	KindProvider  = "provider"   // one request to an online service (ElevenLabs)
)

// Kinds lists every entry kind.
var Kinds = []string{KindOp, KindOpSummary, KindJob, KindTool, KindClaude, KindAction, KindEvent, KindProvider}

// Levels.
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Channels say where something came from.
const (
	ChannelCLI            = "cli"             // the command line, typed by a person or a script
	ChannelDashboard      = "dashboard"       // the dashboard (a person in the browser, or the daemon for it)
	ChannelAssistant      = "assistant"       // the dashboard's Claude panel (headless Claude Code, read-only)
	ChannelWorker         = "worker"          // a job worker
	ChannelWatch          = "watch"           // the watched folder
	ChannelSkillEval      = "skill-eval"      // `metarush skills test --live`
	ChannelClaudeTerminal = "claude-terminal" // Claude Code in a terminal ran the command (CLAUDECODE=1)
	ChannelMCP            = "mcp"             // an app connected through `metarush mcp serve` (Claude Desktop, Claude Code)
	ChannelRecipe         = "recipe"          // a run of a recipe, for the person who started it (D204)
)

// Channels lists every channel.
var Channels = []string{ChannelCLI, ChannelDashboard, ChannelAssistant, ChannelWorker, ChannelWatch, ChannelSkillEval, ChannelClaudeTerminal, ChannelMCP, ChannelRecipe}

// Entry is one thing MetaRush did. Summary is a short plain description written
// when the entry was made; readers compute the row text again with current
// names (Describe), so a renamed project reads correctly in old entries.
type Entry struct {
	ID          string    `bson:"_id" json:"id"`
	WorkspaceID string    `bson:"workspaceId" json:"workspaceId"`
	At          time.Time `bson:"at" json:"at"`
	Kind        string    `bson:"kind" json:"kind" jsonschema:"enum=op,enum=op_summary,enum=job,enum=tool,enum=claude,enum=action,enum=event,enum=provider"`
	Level       string    `bson:"level" json:"level" jsonschema:"enum=info,enum=warn,enum=error"`
	Channel     string    `bson:"channel" json:"channel" jsonschema:"enum=cli,enum=dashboard,enum=assistant,enum=worker,enum=watch,enum=skill-eval,enum=claude-terminal,enum=mcp,enum=recipe"` // recipe: Recipes' runs
	UserID      string    `bson:"userId,omitempty" json:"userId,omitempty"`
	UserName    string    `bson:"userName,omitempty" json:"userName,omitempty"`
	// Agent names the AI app that made the call (codex, cursor…, D248).
	Agent          string            `bson:"agent,omitempty" json:"agent,omitempty"`
	InstallationID string            `bson:"installationId" json:"installationId"`
	Machine        string            `bson:"machine,omitempty" json:"machine,omitempty" jsonschema:"description=Name of the installation that wrote the entry."`
	Version        string            `bson:"version,omitempty" json:"version,omitempty" jsonschema:"description=MetaRush version that wrote the entry."`
	Summary        string            `bson:"summary" json:"summary"`
	DurationMs     float64           `bson:"durationMs,omitempty" json:"durationMs,omitempty"`
	ParentID       string            `bson:"parentId,omitempty" json:"parentId,omitempty" jsonschema:"description=The operation entry a program run or event belongs to."`
	Refs           Refs              `bson:"refs" json:"refs"`
	Error          *EntryError       `bson:"error,omitempty" json:"error,omitempty"`
	Op             *OpInfo           `bson:"op,omitempty" json:"op,omitempty"`
	Counts         *CountInfo        `bson:"counts,omitempty" json:"counts,omitempty"`
	Job            *JobInfo          `bson:"job,omitempty" json:"job,omitempty"`
	Tool           *ToolInfo         `bson:"tool,omitempty" json:"tool,omitempty"`
	Claude         *ClaudeInfo       `bson:"claude,omitempty" json:"claude,omitempty"`
	Action         *ActionInfo       `bson:"action,omitempty" json:"action,omitempty"`
	Event          *EventInfo        `bson:"event,omitempty" json:"event,omitempty"`
	Provider       *ProviderInfo     `bson:"provider,omitempty" json:"provider,omitempty"`
	Versions       map[string]string `bson:"versions,omitempty" json:"versions,omitempty" jsonschema:"description=Program versions\\, recorded with errors."`
}

// Refs are the objects an entry is about.
type Refs struct {
	ProjectID      string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	AssetID        string `bson:"assetId,omitempty" json:"assetId,omitempty"`
	RevisionID     string `bson:"revisionId,omitempty" json:"revisionId,omitempty"`
	RenderID       string `bson:"renderId,omitempty" json:"renderId,omitempty"`
	JobID          string `bson:"jobId,omitempty" json:"jobId,omitempty"`
	QAReportID     string `bson:"qaReportId,omitempty" json:"qaReportId,omitempty"`
	TodoID         string `bson:"todoId,omitempty" json:"todoId,omitempty"`
	ConversationID string `bson:"conversationId,omitempty" json:"conversationId,omitempty" jsonschema:"description=The Claude conversation this belongs to: a dashboard conversation\\, a skill-test case or a Claude Code terminal session."`
}

// EntryError is what went wrong.
type EntryError struct {
	Code    string `bson:"code,omitempty" json:"code,omitempty"`
	Message string `bson:"message" json:"message"`
	Hint    string `bson:"hint,omitempty" json:"hint,omitempty"`
	Reason  string `bson:"reason,omitempty" json:"reason,omitempty" jsonschema:"description=Machine-readable reason from the error details (e.g. assistant_read_only)."`
	Advice  string `bson:"advice,omitempty" json:"advice,omitempty" jsonschema:"description=What to do about it\\, in plain words."`
}

// OpInfo describes an operation call.
type OpInfo struct {
	Name     string         `bson:"name" json:"name"`
	Mutating bool           `bson:"mutating" json:"mutating"`
	Human    bool           `bson:"human,omitempty" json:"human,omitempty" jsonschema:"description=A person confirmed it (a dashboard click or an interactive terminal)."`
	Remote   bool           `bson:"remote,omitempty" json:"remote,omitempty" jsonschema:"description=It arrived over HTTP (the dashboard)."`
	OK       bool           `bson:"ok" json:"ok"`
	Input    map[string]any `bson:"input,omitempty" json:"input,omitempty" jsonschema:"description=The input\\, redacted and shortened."`
	Output   *OutputInfo    `bson:"output,omitempty" json:"output,omitempty"`
	Command  string         `bson:"command,omitempty" json:"command,omitempty" jsonschema:"description=The command line that ran it (command-line channels)\\, redacted."`
	Actions  []string       `bson:"actions,omitempty" json:"actions,omitempty" jsonschema:"description=Audited changes the operation made (e.g. asset.ingest)."`
}

// OutputInfo summarizes an operation's result without storing it.
type OutputInfo struct {
	Bytes  int            `bson:"bytes" json:"bytes"`
	IDs    []string       `bson:"ids,omitempty" json:"ids,omitempty" jsonschema:"description=IDs of the main objects in the result."`
	Counts map[string]int `bson:"counts,omitempty" json:"counts,omitempty" jsonschema:"description=Sizes of lists in the result."`
	Facts  map[string]any `bson:"facts,omitempty" json:"facts,omitempty" jsonschema:"description=A few small result values used to describe the entry (e.g. ingested counts)."`
}

// CountInfo is a periodic count of dashboard reads of one operation.
type CountInfo struct {
	Op      string    `bson:"op" json:"op"`
	Count   int       `bson:"count" json:"count"`
	TotalMs float64   `bson:"totalMs" json:"totalMs"`
	MaxMs   float64   `bson:"maxMs" json:"maxMs"`
	From    time.Time `bson:"from" json:"from"`
	To      time.Time `bson:"to" json:"to"`
}

// Job lifecycle events.
const (
	JobQueued          = "queued"
	JobLeased          = "leased"
	JobProgress        = "progress"
	JobSucceeded       = "succeeded"
	JobFailed          = "failed"
	JobRetry           = "retry"            // failed; will be tried again automatically
	JobCanceled        = "canceled"         // stopped
	JobCancelRequested = "cancel_requested" // a person asked to stop it
	JobRequeued        = "requeued"         // a person asked to try a failed job again
	JobReleased        = "released"         // the worker running it stopped; it waits for the next one
	JobExpedited       = "expedited"        // a person asked to run it now
)

// JobInfo describes a job lifecycle event.
type JobInfo struct {
	Type        string         `bson:"type" json:"type"`
	Event       string         `bson:"event" json:"event" jsonschema:"enum=queued,enum=leased,enum=progress,enum=succeeded,enum=failed,enum=retry,enum=canceled,enum=cancel_requested,enum=requeued,enum=expedited,enum=released"`
	State       string         `bson:"state,omitempty" json:"state,omitempty"`
	Attempt     int            `bson:"attempt,omitempty" json:"attempt,omitempty"`
	MaxAttempts int            `bson:"maxAttempts,omitempty" json:"maxAttempts,omitempty"`
	Worker      string         `bson:"worker,omitempty" json:"worker,omitempty" jsonschema:"description=The machine that leased or ran it."`
	Pct         *float64       `bson:"pct,omitempty" json:"pct,omitempty"`
	Message     string         `bson:"message,omitempty" json:"message,omitempty"`
	NextRunAt   *time.Time     `bson:"nextRunAt,omitempty" json:"nextRunAt,omitempty"`
	WaitedMs    float64        `bson:"waitedMs,omitempty" json:"waitedMs,omitempty" jsonschema:"description=For leased: how long it waited to start."`
	Heavy       bool           `bson:"heavy,omitempty" json:"heavy,omitempty"`
	Detail      map[string]any `bson:"detail,omitempty" json:"detail,omitempty" jsonschema:"description=A few payload or result values (quality\\, preset\\, seconds…)."`
	InProcess   bool           `bson:"inProcess,omitempty" json:"inProcess,omitempty" jsonschema:"description=The command or request that asked for it ran it itself (narration\\, --local renders); that operation's entry tells the story."`
}

// ToolInfo describes one external program run.
type ToolInfo struct {
	Program    string   `bson:"program" json:"program"`
	Args       []string `bson:"args,omitempty" json:"args,omitempty" jsonschema:"description=Arguments\\, redacted and shortened."`
	ExitCode   int      `bson:"exitCode" json:"exitCode"`
	OK         bool     `bson:"ok" json:"ok"`
	Canceled   bool     `bson:"canceled,omitempty" json:"canceled,omitempty"`
	StderrTail string   `bson:"stderrTail,omitempty" json:"stderrTail,omitempty" jsonschema:"description=End of the error output (failures only)."`
	StderrRef  *BodyRef `bson:"stderrRef,omitempty" json:"stderrRef,omitempty"`
}

// Where Claude conversation text is kept (log.prompts).
const (
	PrivacyWorkspace = "workspace"
	PrivacyLocal     = "local"
	PrivacyOff       = "off"
)

// Claude sources.
const (
	SourceAssistant      = "assistant"
	SourceSkillEval      = "skill-eval"
	SourceClaudeTerminal = "claude-terminal"
)

// ClaudeInfo is one prompt and Claude Code's work on it.
type ClaudeInfo struct {
	Source        string       `bson:"source" json:"source" jsonschema:"enum=assistant,enum=skill-eval,enum=claude-terminal"`
	Turn          int          `bson:"turn,omitempty" json:"turn,omitempty"`
	Title         string       `bson:"title,omitempty" json:"title,omitempty"`
	Privacy       string       `bson:"privacy" json:"privacy" jsonschema:"enum=workspace,enum=local,enum=off,description=Where the prompt and response text is kept."`
	Prompt        string       `bson:"prompt,omitempty" json:"prompt,omitempty" jsonschema:"description=The prompt (when it is short and kept in the workspace)."`
	PromptRef     *BodyRef     `bson:"promptRef,omitempty" json:"promptRef,omitempty"`
	PromptChars   int          `bson:"promptChars" json:"promptChars"`
	Response      string       `bson:"response,omitempty" json:"response,omitempty" jsonschema:"description=The final response (when it is short and kept in the workspace)."`
	ResponseRef   *BodyRef     `bson:"responseRef,omitempty" json:"responseRef,omitempty"`
	ResponseChars int          `bson:"responseChars" json:"responseChars"`
	Tools         []ClaudeTool `bson:"tools,omitempty" json:"tools,omitempty"`
	ToolCount     int          `bson:"toolCount" json:"toolCount"`
	Actions       []ActionInfo `bson:"actions,omitempty" json:"actions,omitempty" jsonschema:"description=Changes Claude asked for (action cards) in this turn."`
	Model         string       `bson:"model,omitempty" json:"model,omitempty"`
	Effort        string       `bson:"effort,omitempty" json:"effort,omitempty"`
	CostUSD       float64      `bson:"costUsd,omitempty" json:"costUsd,omitempty"`
	Steps         int          `bson:"steps,omitempty" json:"steps,omitempty"`
	Denials       int          `bson:"denials,omitempty" json:"denials,omitempty"`
	State         string       `bson:"state,omitempty" json:"state,omitempty"`
	Case          string       `bson:"case,omitempty" json:"case,omitempty" jsonschema:"description=Skill-test case name (skill-eval)."`
	Pass          *bool        `bson:"pass,omitempty" json:"pass,omitempty" jsonschema:"description=Whether the skill-test case passed (skill-eval)."`
	Note          bool         `bson:"note,omitempty" json:"note,omitempty" jsonschema:"description=The message was a dashboard update (an action's outcome)\\, not something a person typed."`
}

// ClaudeTool is one tool call Claude made, shortened.
type ClaudeTool struct {
	Name    string `bson:"name" json:"name"`
	Summary string `bson:"summary,omitempty" json:"summary,omitempty" jsonschema:"description=The command\\, file or skill (redacted\\, shortened)."`
	Result  string `bson:"result,omitempty" json:"result,omitempty" jsonschema:"description=Short outcome."`
	IsError bool   `bson:"isError,omitempty" json:"isError,omitempty"`
	Denied  bool   `bson:"denied,omitempty" json:"denied,omitempty" jsonschema:"description=The permissions refused this call."`
}

// Action decisions.
const (
	DecisionProposed    = "proposed"
	DecisionApplied     = "applied"
	DecisionDismissed   = "dismissed"
	DecisionFailed      = "failed"
	DecisionUnavailable = "unavailable"
)

// ActionInfo is a change Claude asked for (an action card) and what a person did with it.
type ActionInfo struct {
	ID       string `bson:"id" json:"id"`
	Turn     int    `bson:"turn,omitempty" json:"turn,omitempty"`
	Kind     string `bson:"kind,omitempty" json:"kind,omitempty" jsonschema:"description=The command Claude asked for (e.g. render submit)."`
	Op       string `bson:"op,omitempty" json:"op,omitempty"`
	Title    string `bson:"title" json:"title"`
	Decision string `bson:"decision" json:"decision" jsonschema:"enum=proposed,enum=applied,enum=dismissed,enum=failed,enum=unavailable"`
	Result   string `bson:"result,omitempty" json:"result,omitempty"`
	Error    string `bson:"error,omitempty" json:"error,omitempty"`
	By       string `bson:"by,omitempty" json:"by,omitempty"`
	// Auto: a safe step that started by itself, as the person's setting
	// allows (D141), not a click.
	Auto bool `bson:"auto,omitempty" json:"auto,omitempty" jsonschema:"description=It started by itself (a safe step\\, under the person's setting)."`
}

// EventInfo is an audited change made outside an operation call.
type EventInfo struct {
	Action     string         `bson:"action" json:"action"`
	TargetType string         `bson:"targetType,omitempty" json:"targetType,omitempty"`
	TargetID   string         `bson:"targetId,omitempty" json:"targetId,omitempty"`
	Details    map[string]any `bson:"details,omitempty" json:"details,omitempty"`
}

// ProviderInfo is one request to an online service. It never holds the key,
// the text sent or the answer: only what the request did, how it went and what
// it cost.
type ProviderInfo struct {
	Name       string `bson:"name" json:"name" jsonschema:"description=The integration (elevenlabs)."`
	Title      string `bson:"title" json:"title" jsonschema:"description=Its display name (ElevenLabs)."`
	What       string `bson:"what" json:"what" jsonschema:"description=What the request did\\, in plain words (make speech\\, list voices)."`
	Method     string `bson:"method" json:"method"`
	Endpoint   string `bson:"endpoint" json:"endpoint" jsonschema:"description=The address path without its query."`
	Status     int    `bson:"status,omitempty" json:"status,omitempty" jsonschema:"description=HTTP status (0: no answer)."`
	OK         bool   `bson:"ok" json:"ok"`
	Characters int    `bson:"characters,omitempty" json:"characters,omitempty" jsonschema:"description=Characters of text sent for speech: what the provider bills."`
	Bytes      int    `bson:"bytes,omitempty" json:"bytes,omitempty" jsonschema:"description=Size of the answer."`
	RequestID  string `bson:"requestId,omitempty" json:"requestId,omitempty" jsonschema:"description=The provider's ID for the request (for its support)."`
}

// Body stores.
const (
	StoreWorkspace = "workspace" // the media store, gzipped, content-addressed
	StoreLocal     = "local"     // this machine's installation home only
)

// BodyRef points at a long text stored outside the entry.
type BodyRef struct {
	Store          string `bson:"store" json:"store" jsonschema:"enum=workspace,enum=local"`
	Key            string `bson:"key" json:"key" jsonschema:"description=Media-store key (workspace) or a path under the machine's log-bodies folder (local)."`
	SHA256         string `bson:"sha256" json:"sha256" jsonschema:"description=SHA-256 of the (redacted) text."`
	Size           int    `bson:"size" json:"size" jsonschema:"description=Length of the text in bytes."`
	InstallationID string `bson:"installationId,omitempty" json:"installationId,omitempty" jsonschema:"description=The machine that holds a local body."`
}
