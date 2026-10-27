package assistant

import (
	"encoding/json"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// Conversation states.
const (
	ConvIdle    = "idle"    // waiting for the next message (Claude Code may be asleep)
	ConvWorking = "working" // a turn is running
)

// Turn states. A turn ends in exactly one terminal state.
const (
	TurnRunning     = "running"
	TurnSucceeded   = "succeeded"
	TurnFailed      = "failed"
	TurnInterrupted = "interrupted" // the person interrupted it; the conversation goes on
	TurnStopped     = "stopped"     // the person ended Claude Code during the turn
	TurnTimedOut    = "timed_out"
	TurnBudget      = "budget" // the conversation reached its budget
	TurnLost        = "lost"   // the process holding the session stopped during the turn
)

// TurnTerminal reports whether a turn state is final.
func TurnTerminal(state string) bool {
	switch state {
	case TurnSucceeded, TurnFailed, TurnInterrupted, TurnStopped, TurnTimedOut, TurnBudget, TurnLost:
		return true
	}
	return false
}

// AssistantStatus says whether the dashboard can run local Claude Code, and why not.
type AssistantStatus struct {
	Available     bool   `json:"available"`
	Reason        string `json:"reason,omitempty" jsonschema:"description=Why the assistant is unavailable (shown to the person)."`
	Hint          string `json:"hint,omitempty"`
	ClaudeVersion string `json:"claudeVersion,omitempty"`
	LoggedIn      *bool  `json:"loggedIn,omitempty" jsonschema:"description=Claude Code sign-in state; absent when Claude Code cannot report it."`
	AuthMethod    string `json:"authMethod,omitempty" jsonschema:"description=How Claude Code authenticates (e.g. claude.ai subscription or an API key)."`
	// Account is whose Claude account answers in the panel (this computer's
	// Claude Code sign-in) and its data terms (D239).
	Account        *ClaudeAccount `json:"account,omitempty"`
	Billing        string         `json:"billing" jsonschema:"enum=plan,enum=api,enum=unknown,description=plan: a Claude subscription sign-in (usage counts toward the plan's limits; dollar figures are API-equivalent estimates); api: an API key or cloud provider (billed per token)."`
	BillingLabel   string         `json:"billingLabel" jsonschema:"description=One line describing the billing mode."`
	BudgetUSD      float64        `json:"budgetUsd" jsonschema:"description=Default dollar cap per conversation with an API key (assistant.maxBudgetUsd); 0 on a Claude plan."`
	TokenLimitM    float64        `json:"tokenLimitMillions" jsonschema:"description=Default limit per conversation in millions of tokens."`
	PlanUsage      *PlanUsage     `json:"planUsage,omitempty" jsonschema:"description=The Claude plan's own usage as Claude Code last reported it (plan sign-in only)."`
	MaxSteps       int            `json:"maxSteps" jsonschema:"description=Step limit for one message (claude --max-turns)."`
	TimeoutMinutes int            `json:"timeoutMinutes" jsonschema:"description=Time limit for one message."`
	IdleMinutes    int            `json:"idleMinutes" jsonschema:"description=An idle Claude Code process is ended after this long; the conversation resumes on the next message."`
	Model          string         `json:"model,omitempty"`
	Machine        string         `json:"machine" jsonschema:"description=This installation's name (conversations can be continued only on the machine that holds them)."`
	InstallationID string         `json:"installationId"`
	Live           int            `json:"live" jsonschema:"description=Claude Code processes this daemon holds right now."`
	Waiting        int            `json:"waiting" jsonschema:"description=Cards and questions waiting for the person asking\\, in their conversations on this machine."`
	Working        []string       `json:"working" jsonschema:"description=Conversations with a turn running on this machine."`
	ActiveRunID    string         `json:"activeRunId,omitempty" jsonschema:"description=The first conversation in working (kept for older dashboards)."`
	CheckedAt      time.Time      `json:"checkedAt"`
}

// AssistantMessageIn is one message to the assistant. Context IDs are resolved (and
// validated) on the server; only the prompt is free text.
type AssistantMessageIn struct {
	Prompt     string   `json:"prompt" jsonschema:"minLength=1,maxLength=8000"`
	ProjectID  string   `json:"projectId,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	RevisionID string   `json:"revisionId,omitempty" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	AssetID    string   `json:"assetId,omitempty" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	ClipID     string   `json:"clipId,omitempty" jsonschema:"maxLength=64"`
	Time       *float64 `json:"time,omitempty" jsonschema:"minimum=0"`
	TimeBase   string   `json:"timeBase,omitempty" jsonschema:"enum=source,enum=timeline"`
	// Attachments are pictures and PDFs the person attached (stored first with
	// assistant.attach): Claude can read them; MetaRush asks whether to keep them (D166).
	Attachments []models.KeepFile `json:"attachments,omitempty" jsonschema:"maxItems=10,description=Pictures and PDFs attached to the message (from assistant.attach)."`
}

// AssistantAskIn starts a new conversation with its first message.
type AssistantAskIn struct {
	AssistantMessageIn
	BudgetUSD float64 `json:"budgetUsd,omitempty" jsonschema:"minimum=0,maximum=100,description=Dollar cap for the whole conversation with an API-key sign-in (default assistant.maxBudgetUsd); a Claude plan has none."`
	// TokenLimitM is the conversation's limit in millions of tokens (D181).
	TokenLimitM float64 `json:"tokenLimitMillions,omitempty" jsonschema:"minimum=0,maximum=1000,description=Limit for the whole conversation in millions of tokens (default assistant.tokenLimitMillions\\, 50)."`
	Model       string  `json:"model,omitempty" jsonschema:"maxLength=80,description=claude --model (an alias such as sonnet or a full model name the installed Claude Code offers)."`
	Effort      string  `json:"effort,omitempty" jsonschema:"enum=,enum=default,enum=low,enum=medium,enum=high,enum=xhigh,enum=max,description=claude --effort."`
	Title       string  `json:"title,omitempty" jsonschema:"maxLength=120"`
}

// AssistantContinueIn sends a follow-up message; it is queued while a turn is running.
// Model and Effort, when given, change the conversation's settings first.
type AssistantContinueIn struct {
	ID string `json:"id" jsonschema:"description=Conversation ID."`
	AssistantMessageIn
	Model  *string `json:"model,omitempty" jsonschema:"maxLength=80"`
	Effort *string `json:"effort,omitempty"`
}

// ConversationSendOut is the conversation after a message was sent or queued.
type ConversationSendOut struct {
	Conversation Conversation `json:"conversation"`
	Turn         int          `json:"turn,omitempty" jsonschema:"description=The turn this message started (0 when it was queued)."`
	Queued       bool         `json:"queued"`
	QueueID      string       `json:"queueId,omitempty"`
}

// ConversationIn names a conversation.
type ConversationIn struct {
	ID string `json:"id"`
}

// ConversationShowIn reads one conversation.
type ConversationShowIn struct {
	ID     string `json:"id"`
	Events *bool  `json:"events,omitempty" jsonschema:"description=Include the condensed transcript (default true)."`
	Raw    bool   `json:"raw,omitempty" jsonschema:"description=Also return the exact messages sent to Claude Code, the system rules and raw tool inputs and outputs."`
}

// AssistantSuggestionsIn asks for prompt suggestions for the current context.
type AssistantSuggestionsIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
}

// AssistantSuggestionsOut are short, natural prompts that fit the current state.
type AssistantSuggestionsOut struct {
	Suggestions []AssistantSuggestion `json:"suggestions"`
}

// AssistantSuggestion is one prompt the person can pick (it fills the input; nothing is sent).
type AssistantSuggestion struct {
	Label  string `json:"label" jsonschema:"description=Short chip text."`
	Prompt string `json:"prompt" jsonschema:"description=The full prompt to put in the input."`
	Reason string `json:"reason,omitempty" jsonschema:"description=Why it is offered now."`
}

// ConversationListIn filters the conversation history (every machine of the
// workspace). Whose conversations (D179): a project's list is everyone's, the
// workspace-wide list the asking person's, unless AllUsers or Mine says otherwise.
type ConversationListIn struct {
	ProjectID string `json:"projectId,omitempty"`
	Search    string `json:"search,omitempty" jsonschema:"maxLength=200,description=Words in the title or in any message."`
	Archived  bool   `json:"archived,omitempty" jsonschema:"description=List archived conversations instead of active ones."`
	AllUsers  bool   `json:"allUsers,omitempty" jsonschema:"description=Include other people's conversations in this workspace (the default with a project)."`
	Mine      bool   `json:"mine,omitempty" jsonschema:"description=Only the asking person's conversations (the default without a project)."`
	Limit     int    `json:"limit,omitempty" jsonschema:"minimum=0,maximum=200"`
}

// ConversationListOut is the conversation history, most recently active first, without transcripts.
type ConversationListOut struct {
	Conversations  []Conversation `json:"conversations"`
	Machine        string         `json:"machine"`
	InstallationID string         `json:"installationId"`
	// AllUsers and Others say whose conversations these are (D179).
	AllUsers bool `json:"allUsers" jsonschema:"description=Other people's conversations are listed too (otherwise only the asking person's)."`
	Others   int  `json:"others" jsonschema:"description=How many conversations other people started match too: listed when allUsers\\, left out otherwise."`
}

// ConversationRenameIn renames a conversation.
type ConversationRenameIn struct {
	ID    string `json:"id"`
	Title string `json:"title" jsonschema:"minLength=1,maxLength=120"`
}

// ConversationArchiveIn archives (or restores) a conversation.
type ConversationArchiveIn struct {
	ID       string `json:"id"`
	Archived *bool  `json:"archived,omitempty" jsonschema:"description=false restores it (default true)."`
}

// ConversationConfigureIn changes a conversation's limits, model or effort (on the machine that holds it).
type ConversationConfigureIn struct {
	ID          string   `json:"id"`
	BudgetUSD   *float64 `json:"budgetUsd,omitempty" jsonschema:"minimum=0.05,maximum=100,description=Dollar cap (API-key sign-in only)."`
	TokenLimitM *float64 `json:"tokenLimitMillions,omitempty" jsonschema:"minimum=1,maximum=1000,description=Limit in millions of tokens."`
	Model       *string  `json:"model,omitempty" jsonschema:"maxLength=80,description=Model alias or name; empty or default uses the installation default."`
	Effort      *string  `json:"effort,omitempty" jsonschema:"description=low/medium/high/xhigh/max; empty or default uses the installation default."`
}

// ConversationDequeueIn removes a queued message before it is sent.
type ConversationDequeueIn struct {
	ID      string `json:"id"`
	QueueID string `json:"queueId"`
}

// ConversationInterruptOut is the conversation after its turn was interrupted. Queued
// messages are handed back (not sent) so the person can edit or resend them.
type ConversationInterruptOut struct {
	Conversation Conversation    `json:"conversation"`
	Requeued     []QueuedMessage `json:"requeued"`
}

// ConversationApplyIn applies one of a conversation's plan proposals with the confirm token
// that `assistant proposals` printed (the same gate as `edit apply`).
type ConversationApplyIn struct {
	ID           string `json:"id" jsonschema:"description=Conversation ID."`
	Proposal     string `json:"proposal" jsonschema:"description=Proposal ref as listed (t<turn>/<name>.plan.json), or just the file name for the latest turn that wrote it."`
	ConfirmToken string `json:"confirmToken,omitempty"`
	AllowUnlock  bool   `json:"allowUnlock,omitempty"`
}

// TurnContext is what the server resolved from a message's IDs.
type TurnContext struct {
	ProjectID          string   `bson:"projectId,omitempty" json:"projectId,omitempty"`
	ProjectName        string   `bson:"projectName,omitempty" json:"projectName,omitempty"`
	HeadRevisionID     string   `bson:"headRevisionId,omitempty" json:"headRevisionId,omitempty"`
	HeadRevisionNumber int      `bson:"headRevisionNumber,omitempty" json:"headRevisionNumber,omitempty"`
	RevisionID         string   `bson:"revisionId,omitempty" json:"revisionId,omitempty"`
	RevisionNumber     int      `bson:"revisionNumber,omitempty" json:"revisionNumber,omitempty"`
	AssetID            string   `bson:"assetId,omitempty" json:"assetId,omitempty"`
	AssetName          string   `bson:"assetName,omitempty" json:"assetName,omitempty"`
	ClipID             string   `bson:"clipId,omitempty" json:"clipId,omitempty"`
	Time               *float64 `bson:"time,omitempty" json:"time,omitempty"`
	TimeBase           string   `bson:"timeBase,omitempty" json:"timeBase,omitempty"`
	// people is who speaks in the footage or project in view, with the owner's
	// notes about them: written into the message Claude receives, not kept as
	// part of the turn's context (the dashboard shows the context line).
	people []string
	// library is what the project can use beyond its own footage: linked
	// projects and resources ("12 here, plus 40 from Brand"); message-only too.
	library []string
	// memory is what the project keeps (D166): its rules, brief, sign-offs
	// waiting and what was offered to keep before; message-only too.
	memory []string
	// viewing is the page's project when it isn't the conversation's (D185):
	// a one-line hint, never a move to it; message-only too.
	viewing string
}

// Conversation is a multi-turn Claude Code session started from the dashboard
// or the CLI. It is stored in MongoDB (browsable from every machine); the Claude
// Code session itself lives on the machine named by InstallationName.
type Conversation struct {
	ID               string `bson:"_id" json:"id"`
	WorkspaceID      string `bson:"workspaceId" json:"-"`
	Title            string `bson:"title" json:"title"`
	TitleSource      string `bson:"titleSource" json:"titleSource" jsonschema:"enum=prompt,enum=claude,enum=user"`
	UserID           string `bson:"userId" json:"userId"`
	UserName         string `bson:"userName" json:"userName"`
	InstallationID   string `bson:"installationId" json:"installationId"`
	InstallationName string `bson:"installationName" json:"installationName" jsonschema:"description=The machine that holds the Claude Code session (the only one that can continue it)."`
	ProjectID        string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	ProjectName      string `bson:"projectName,omitempty" json:"projectName,omitempty"`
	// ProjectArchived: its project was deleted (archived, D196): it can be
	// read, and continued once the project is restored (filled when one
	// conversation is shown).
	ProjectArchived bool `bson:"-" json:"projectArchived,omitempty"`
	// KeptHere says what its client's Content Settings keep from Claude
	// (What may leave, D290), in a line the panel shows (filled when one
	// conversation is shown).
	KeptHere        string  `bson:"-" json:"keptHere,omitempty"`
	State           string  `bson:"state" json:"state" jsonschema:"enum=idle,enum=working"`
	Archived        bool    `bson:"archived" json:"archived"`
	Legacy          bool    `bson:"legacy,omitempty" json:"legacy,omitempty" jsonschema:"description=A single request from before conversations (read-only)."`
	SessionLost     bool    `bson:"sessionLost,omitempty" json:"sessionLost,omitempty" jsonschema:"description=Claude Code no longer has this session on its machine (it cannot be resumed)."`
	PendingActions  int     `bson:"pendingActions" json:"pendingActions" jsonschema:"description=Cards and question cards waiting for a person."`
	Watching        int     `bson:"watching,omitempty" json:"watching,omitempty" jsonschema:"description=Cards whose work (a render\\, a snapshot\\, an import\\, a check) is still running; Claude hears when each finishes."`
	ClaudeSessionID string  `bson:"claudeSessionId,omitempty" json:"claudeSessionId,omitempty"`
	SessionStarted  bool    `bson:"sessionStarted" json:"-"`
	HolderPID       int     `bson:"holderPid" json:"-"`
	BudgetUSD       float64 `bson:"budgetUsd" json:"budgetUsd" jsonschema:"description=Dollar cap with an API-key sign-in (0: none\\, a Claude plan)."`
	CostUSD         float64 `bson:"costUsd" json:"costUsd" jsonschema:"description=Claude Code's API-price estimate (real spend only with an API key)."`
	// TokenLimitM is this conversation's own limit in millions of tokens
	// (D181; 0: the installation's default); LimitTokens is the limit that
	// applies, in tokens (filled when it is shown).
	TokenLimitM float64 `bson:"tokenLimitM,omitempty" json:"tokenLimitMillions,omitempty" jsonschema:"description=The conversation's own limit in millions of tokens (absent: the installation default)."`
	LimitTokens int64   `bson:"-" json:"limitTokens,omitempty" jsonschema:"description=The limit that applies\\, in tokens."`
	// Tokens is what the conversation has used (counted since D181).
	Tokens TokenUsage `bson:"tokens" json:"tokens"`
	// SessionTokens is Claude Code's running total for the session when it
	// was last seen; Counting says it is this conversation's own (a
	// conversation from before D181 starts counting with its next message).
	SessionTokens TokenUsage `bson:"sessionTokens" json:"-"`
	Counting      bool       `bson:"counting,omitempty" json:"-"`
	// Compactions counts the summaries of the conversation (D181);
	// CompactedTo is how many tokens Claude's memory held after the latest.
	Compactions int `bson:"compactions,omitempty" json:"compactions,omitempty"`
	CompactedTo int `bson:"compactedTo,omitempty" json:"compactedTo,omitempty"`
	// MetaRushVersion is the MetaRush build its latest message ran on: a
	// message on a newer one tells Claude what's new (D185).
	MetaRushVersion string `bson:"metarushVersion,omitempty" json:"metarushVersion,omitempty" jsonschema:"description=The MetaRush version its latest message ran on (Claude hears what's new after an update)."`
	// Inbox holds notes for Claude from elsewhere (a version turned down
	// with a note, D186) until the machine holding the conversation sends them.
	Inbox         []InboxNote `bson:"inbox,omitempty" json:"-"`
	Model         string      `bson:"model,omitempty" json:"model,omitempty" jsonschema:"description=Model override (claude --model); empty means the installation default."`
	Effort        string      `bson:"effort,omitempty" json:"effort,omitempty" jsonschema:"description=Effort override (claude --effort); empty means the installation default."`
	ModelUsed     string      `bson:"modelUsed,omitempty" json:"modelUsed,omitempty"`
	ClaudeVersion string      `bson:"claudeVersion,omitempty" json:"claudeVersion,omitempty"`
	// Account is whose Claude account answered its latest message, and its
	// terms (D239): the Claude Code sign-in of the computer that holds it.
	Account       *ClaudeAccount `bson:"account,omitempty" json:"account,omitempty"`
	ContextTokens int            `bson:"contextTokens,omitempty" json:"contextTokens,omitempty" jsonschema:"description=Tokens in Claude's context after the latest reply."`
	ContextWindow int            `bson:"contextWindow,omitempty" json:"contextWindow,omitempty"`
	TurnCount     int            `bson:"turnCount" json:"turnCount"`
	FirstPrompt   string         `bson:"firstPrompt,omitempty" json:"firstPrompt,omitempty"`
	LastPrompt    string         `bson:"lastPrompt,omitempty" json:"lastPrompt,omitempty"`
	ProposalCount int            `bson:"proposalCount" json:"proposalCount"`
	Compacted     bool           `bson:"compacted,omitempty" json:"compacted,omitempty" jsonschema:"description=Older tool rows were dropped to keep the stored transcript small (the full stream stays on the owning machine)."`
	SystemPrompt  string         `bson:"-" json:"systemPrompt,omitempty" jsonschema:"description=The rules appended to Claude Code's system prompt (raw view only)."`
	// WaitingFor is who the waiting cards are for (D179): each card is for the
	// person whose message Claude was answering when it asked.
	WaitingFor []WaitingCount `bson:"waitingFor,omitempty" json:"waitingFor,omitempty" jsonschema:"description=Who the cards and questions waiting are for: each is for the person whose message Claude was answering."`
	// Turns, Queue, Actions and Events are omitted from conversation lists.
	Turns     []Turn            `bson:"turns" json:"turns,omitempty"`
	Queue     []QueuedMessage   `bson:"queue" json:"queue,omitempty"`
	Actions   []AssistantAction `bson:"actions" json:"actions,omitempty" jsonschema:"description=Changes Claude asked for that a person runs or dismisses."`
	Events    []AssistantEvent  `bson:"events" json:"events,omitempty" jsonschema:"description=Condensed transcript (omitted in lists)."`
	CreatedAt time.Time         `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time         `bson:"updatedAt" json:"updatedAt"`
	// Computed for the caller, never stored.
	Here      bool `bson:"-" json:"here" jsonschema:"description=This machine holds the session (it can be continued here)."`
	Live      bool `bson:"-" json:"live" jsonschema:"description=A Claude Code process is running for it on this machine."`
	Resumable bool `bson:"-" json:"resumable" jsonschema:"description=It can take more messages (on the machine that holds it)."`
	// Mine and WaitingForYou are about the person asking (D179).
	Mine          bool `bson:"-" json:"mine" jsonschema:"description=The person asking started it."`
	WaitingForYou int  `bson:"-" json:"waitingForYou" jsonschema:"description=Cards and questions waiting for the person asking: the ones Claude raised answering their messages."`
}

// WaitingCount is how many cards and questions of a conversation wait for one person.
type WaitingCount struct {
	UserID   string `bson:"userId" json:"userId"`
	UserName string `bson:"userName" json:"userName"`
	Count    int    `bson:"count" json:"count"`
}

// Turn is one message and Claude Code's work on it.
type Turn struct {
	N             int         `bson:"n" json:"n"`
	Prompt        string      `bson:"prompt" json:"prompt" jsonschema:"description=What the person typed."`
	Sent          string      `bson:"sent,omitempty" json:"sent,omitempty" jsonschema:"description=The exact message sent to Claude Code: context preamble plus the prompt (raw view only)."`
	Context       TurnContext `bson:"context" json:"context"`
	User          string      `bson:"user,omitempty" json:"user,omitempty" jsonschema:"description=Who sent it (their name)."`
	UserID        string      `bson:"userId,omitempty" json:"userId,omitempty" jsonschema:"description=Who sent it: the person who typed it or answered\\, or for a note the person whose decision it reports (absent on older messages: the person who started the conversation)."`
	State         string      `bson:"state" json:"state" jsonschema:"enum=running,enum=succeeded,enum=failed,enum=interrupted,enum=stopped,enum=timed_out,enum=budget,enum=lost"`
	StartedAt     time.Time   `bson:"startedAt" json:"startedAt"`
	EndedAt       *time.Time  `bson:"endedAt,omitempty" json:"endedAt,omitempty"`
	CostUSD       float64     `bson:"costUsd" json:"costUsd"`
	DurationMs    int64       `bson:"durationMs" json:"durationMs"`
	Steps         int         `bson:"steps" json:"steps" jsonschema:"description=Claude Code's agentic steps in this turn."`
	Denials       int         `bson:"denials" json:"denials" jsonschema:"description=Tool calls the read-only allowlist refused."`
	Error         string      `bson:"error,omitempty" json:"error,omitempty"`
	Proposals     int         `bson:"proposals" json:"proposals"`
	ContextTokens int         `bson:"contextTokens,omitempty" json:"contextTokens,omitempty"`
	Tokens        *TokenUsage `bson:"tokens,omitempty" json:"tokens,omitempty" jsonschema:"description=What this message used."`
	Resumed       bool        `bson:"resumed,omitempty" json:"resumed,omitempty" jsonschema:"description=Claude Code was restarted with --resume for this turn."`
	Compact       bool        `bson:"compact,omitempty" json:"compact,omitempty" jsonschema:"description=MetaRush had Claude summarize the conversation\\, not something the person typed."`
	Model         string      `bson:"model,omitempty" json:"model,omitempty"`
	Effort        string      `bson:"effort,omitempty" json:"effort,omitempty"`
	Note          bool        `bson:"note,omitempty" json:"note,omitempty" jsonschema:"description=A dashboard update (an action's outcome) rather than something the person typed."`
	// Shown is what the person reads for this message when it differs from
	// what Claude was sent: a note's plain line ("You saved version 2"), or
	// a question card's answer (the prompt adds the question for Claude).
	Shown string `bson:"shown,omitempty" json:"shown,omitempty" jsonschema:"description=What the person reads for this message (a note's plain line\\, an answer); the prompt is what Claude was sent."`
	// AnswerTo is the question card this message answers.
	AnswerTo string `bson:"answerTo,omitempty" json:"answerTo,omitempty"`
	// Attachments are the pictures and PDFs the person attached to it.
	Attachments []models.KeepFile `bson:"attachments,omitempty" json:"attachments,omitempty"`
	Folder      string            `bson:"folder" json:"-"`    // proposals folder, relative to the assistant folder
	RefPrefix   string            `bson:"refPrefix" json:"-"` // "t<n>/" (empty for imported single requests)
}

// QueuedMessage waits for the running turn to finish.
type QueuedMessage struct {
	ID       string   `bson:"id" json:"id"`
	Prompt   string   `bson:"prompt" json:"prompt"`
	Shown    string   `bson:"shown,omitempty" json:"shown,omitempty"`
	AnswerTo string   `bson:"answerTo,omitempty" json:"answerTo,omitempty"`
	Question string   `bson:"question,omitempty" json:"-"` // the question an answer is for (told to Claude with it)
	News     []string `bson:"news,omitempty" json:"-"`     // card outcomes told with the message
	// Attachments are the pictures and PDFs attached to it.
	Attachments []models.KeepFile `bson:"attachments,omitempty" json:"attachments,omitempty"`
	Context     TurnContext       `bson:"context" json:"context"`
	User        string            `bson:"user,omitempty" json:"user,omitempty"`
	UserID      string            `bson:"userId,omitempty" json:"userId,omitempty"`
	At          time.Time         `bson:"at" json:"at"`
	Note        bool              `bson:"note,omitempty" json:"note,omitempty"`
}

// TurnResult is Claude Code's report for one turn.
type TurnResult struct {
	Subtype       string  `bson:"subtype" json:"subtype" jsonschema:"description=Claude Code result subtype (success or error_max_budget_usd or error_max_turns or error_during_execution)."`
	IsError       bool    `bson:"isError" json:"isError"`
	Text          string  `bson:"text,omitempty" json:"text,omitempty"`
	CostUSD       float64 `bson:"costUsd" json:"costUsd" jsonschema:"description=Spent in this turn."`
	TotalCostUSD  float64 `bson:"totalCostUsd" json:"totalCostUsd" jsonschema:"description=Spent in the conversation so far."`
	DurationMs    int64   `bson:"durationMs" json:"durationMs"`
	Steps         int     `bson:"steps" json:"steps"`
	Denials       int     `bson:"denials" json:"denials"`
	ContextTokens int     `bson:"contextTokens,omitempty" json:"contextTokens,omitempty"`
	ContextWindow int     `bson:"contextWindow,omitempty" json:"contextWindow,omitempty"`
	// Tokens is what this message used (D181); TotalTokens what the
	// conversation has used so far.
	Tokens      *TokenUsage `bson:"tokens,omitempty" json:"tokens,omitempty"`
	TotalTokens int64       `bson:"totalTokens,omitempty" json:"totalTokens,omitempty"`
	// session is Claude Code's running total for the session (by model,
	// summed), message the result's own usage: finishTurnLocked counts them.
	session TokenUsage
	message TokenUsage
}

// AssistantEvent is one entry of a conversation's condensed transcript, streamed
// over SSE (GET /api/assistant/conversations/{id}/events; the SSE id is Seq).
type AssistantEvent struct {
	Seq     int          `bson:"seq" json:"seq"`
	At      time.Time    `bson:"at" json:"at"`
	Turn    int          `bson:"turn" json:"turn"`
	Kind    string       `bson:"kind" json:"kind" jsonschema:"enum=turn,enum=text,enum=tool,enum=tool_result,enum=proposals,enum=action,enum=notice,enum=error,enum=turn_end"`
	State   string       `bson:"state,omitempty" json:"state,omitempty" jsonschema:"description=For kind=turn_end: the turn's final state; for kind=turn: note when it is a dashboard update."`
	Text    string       `bson:"text,omitempty" json:"text,omitempty" jsonschema:"description=The person's message (turn), assistant prose (text), a note (notice) or the message (error)."`
	User    string       `bson:"user,omitempty" json:"user,omitempty" jsonschema:"description=For kind=turn: who sent it (their name)."`
	UserID  string       `bson:"userId,omitempty" json:"userId,omitempty" jsonschema:"description=For kind=turn: who sent it (absent on older messages: the person who started the conversation)."`
	Context *TurnContext `bson:"context,omitempty" json:"context,omitempty"`
	Tool    *ToolCall    `bson:"tool,omitempty" json:"tool,omitempty"`
	Result  *TurnResult  `bson:"result,omitempty" json:"result,omitempty"`
	// Attachments are the files attached to the person's message (kind turn).
	Attachments []models.KeepFile `bson:"attachments,omitempty" json:"attachments,omitempty"`
	Count       int               `bson:"count,omitempty" json:"count,omitempty" jsonschema:"description=For kind=proposals: how many proposal files this turn wrote."`
	ActionID    string            `bson:"actionId,omitempty" json:"actionId,omitempty" jsonschema:"description=For kind=action: the action card (see Conversation.actions)."`
}

// ToolCall is a condensed tool use (kind=tool) or its outcome (kind=tool_result).
type ToolCall struct {
	ID    string `bson:"id" json:"id"`
	Name  string `bson:"name,omitempty" json:"name,omitempty"`
	Label string `bson:"label,omitempty" json:"label,omitempty" jsonschema:"description=Readable one-line description (e.g. Read transcript · Interview A 0:11–0:18)."`
	// Group and Many group consecutive steps of one kind into one line: steps
	// with the same Group show as Many with their count ("Looked at 19 stills
	// from “Lena”"; %d is the count), their pictures together.
	Group    string     `bson:"group,omitempty" json:"group,omitempty" jsonschema:"description=Consecutive steps with the same group show as one line."`
	Many     string     `bson:"many,omitempty" json:"many,omitempty" jsonschema:"description=The line for several steps of this group (%d is how many)."`
	Summary  string     `bson:"summary,omitempty" json:"summary,omitempty" jsonschema:"description=Condensed input: the command or file or skill."`
	IsError  bool       `bson:"isError,omitempty" json:"isError,omitempty"`
	Denied   bool       `bson:"denied,omitempty" json:"denied,omitempty" jsonschema:"description=The read-only allowlist refused this call."`
	Input    string     `bson:"input,omitempty" json:"input,omitempty" jsonschema:"description=Raw tool input as JSON, clipped (raw view only)."`
	Output   string     `bson:"output,omitempty" json:"output,omitempty" jsonschema:"description=Start of the tool's raw output (tool_result; raw view only)."`
	Outcome  string     `bson:"outcome,omitempty" json:"outcome,omitempty" jsonschema:"description=Short outcome in plain words (e.g. looks good\\, 2 notes)."`
	Images   []ImageRef `bson:"images,omitempty" json:"images,omitempty" jsonschema:"description=Images Claude looked at (served by GET /api/assistant/image)."`
	Todos    []TodoItem `bson:"todos,omitempty" json:"todos,omitempty" jsonschema:"description=The checklist when Claude used TodoWrite."`
	Proposal string     `bson:"proposal,omitempty" json:"proposal,omitempty" jsonschema:"description=The proposal file this call wrote (t<turn>/<name>)."`
}

// ImageRef is an image inside the scratch folder, the media cache or the assistant folder.
type ImageRef struct {
	Root string `bson:"root" json:"root" jsonschema:"enum=scratch,enum=cache,enum=assistant"`
	Path string `bson:"path" json:"path" jsonschema:"description=Slash-separated path relative to the root."`
}

// TodoItem is one entry of Claude Code's checklist.
type TodoItem struct {
	Content    string `bson:"content" json:"content"`
	Status     string `bson:"status" json:"status" jsonschema:"enum=pending,enum=in_progress,enum=completed"`
	ActiveForm string `bson:"activeForm,omitempty" json:"activeForm,omitempty"`
}

// AssistantPartial is the SSE `partial` event: text Claude is still writing.
// From is the offset Text continues at (0 replaces what the client has).
type AssistantPartial struct {
	Turn  int    `json:"turn"`
	Block int    `json:"block"`
	Phase string `json:"phase" jsonschema:"enum=,enum=text,enum=thinking,enum=tool"`
	From  int    `json:"from"`
	Text  string `json:"text"`
}

// ConversationProposals are the plans and render requests of every turn, re-checked now.
type ConversationProposals struct {
	ConversationID string          `json:"conversationId"`
	Here           bool            `json:"here" jsonschema:"description=The proposal files are on this machine (otherwise only counts are known)."`
	Machine        string          `json:"machine"`
	Turns          []TurnProposals `json:"turns"`
	CheckedAt      time.Time       `json:"checkedAt"`
}

// TurnProposals are the files one turn wrote.
type TurnProposals struct {
	Turn     int              `json:"turn"`
	Plans    []PlanProposal   `json:"plans"`
	Renders  []RenderProposal `json:"renders"`
	Problems []string         `json:"problems,omitempty" jsonschema:"description=Files that were ignored and why."`
}

// PlanProposal is one edit plan file, validated with edit.check. The dashboard
// applies Plan verbatim through edit.apply (the person is the approval gate);
// the CLI applies it with `assistant apply` and the confirm token.
type PlanProposal struct {
	File        string            `json:"file"`
	Ref         string            `json:"ref" jsonschema:"description=What to pass to assistant apply (t<turn>/<file>)."`
	Origin      string            `json:"origin" jsonschema:"description=Recorded on the revision when applied (assistant:<conversation>/t<turn>/<file>)."`
	Kind        string            `json:"kind" jsonschema:"enum=ops,enum=spec,enum=invalid"`
	OpCount     int               `json:"opCount"`
	Instruction string            `json:"instruction,omitempty"`
	Rationale   string            `json:"rationale,omitempty"`
	Plan        json.RawMessage   `json:"plan,omitempty" jsonschema:"type=object,description=The exact plan to apply (origin set by the server)."`
	Check       *ops.EditCheckOut `json:"check,omitempty"`
	Error       *apperr.Error     `json:"error,omitempty"` // why the plan could not be checked (e.g. the branch moved)
	Applied     *AppliedProposal  `json:"applied,omitempty"`
}

// AppliedProposal is the revision a proposal became.
type AppliedProposal struct {
	RevisionID string    `json:"revisionId"`
	Number     int       `json:"number"`
	ProjectID  string    `json:"projectId"`
	By         string    `json:"by,omitempty"`
	At         time.Time `json:"at"`
}

// RenderRequest is the file format the assistant writes to propose a render
// (<name>.render.json in the turn's proposals folder).
type RenderRequest struct {
	RevisionID string `json:"revisionId,omitempty" jsonschema:"pattern=^rev_[0-9a-z]{26}$,description=Render this existing revision."`
	Plan       string `json:"plan,omitempty" jsonschema:"description=Or: render the revision created by applying this plan file (same folder)."`
	Quality    string `json:"quality,omitempty" jsonschema:"enum=review,enum=final"`
	Preset     string `json:"preset,omitempty" jsonschema:"enum=vertical-1080p,enum=landscape-1080p,enum=square-1080"`
	Reason     string `json:"reason,omitempty" jsonschema:"maxLength=500"`
}

// RenderProposal is a render request with what the dashboard needs to offer a button.
type RenderProposal struct {
	File           string `json:"file"`
	Ref            string `json:"ref"`
	RevisionID     string `json:"revisionId,omitempty"`
	RevisionNumber int    `json:"revisionNumber,omitempty"`
	ProjectID      string `json:"projectId,omitempty"`
	Plan           string `json:"plan,omitempty"`
	Quality        string `json:"quality" jsonschema:"enum=review,enum=final"`
	Preset         string `json:"preset,omitempty"`
	Reason         string `json:"reason,omitempty"`
	Ready          bool   `json:"ready" jsonschema:"description=The render can be queued now."`
	Waiting        string `json:"waiting,omitempty" jsonschema:"description=What has to happen first (e.g. apply the plan)."`
	Error          string `json:"error,omitempty"`
	RenderID       string `json:"renderId,omitempty" jsonschema:"description=An existing render of this revision at this quality."`
	RenderState    string `json:"renderState,omitempty"`
}
