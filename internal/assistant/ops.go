package assistant

import (
	"context"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ops"
)

// The assistant's operations, in the one registry the CLI and the dashboard
// share (POST /api/ops/assistant.*). Each has a dedicated `metarush assistant`
// command. Operations that need the live Claude Code process run in the process
// that holds it: the CLI forwards them to the local daemon when one is running.
// Every mutating one is refused on the assistant channel itself, so a
// conversation cannot drive conversations.

// StatusIn asks for the availability checks.
type StatusIn struct {
	Refresh bool `json:"refresh,omitempty" jsonschema:"description=Re-run the checks instead of using the cached result."`
}

// ModelsIn asks for the model catalog.
type ModelsIn struct {
	Refresh bool `json:"refresh,omitempty"`
}

func init() {
	ops.Def("assistant.status", "Check whether Claude Code can run on this machine (version, sign-in, how it is billed, limits); nothing is sent to Claude", ops.Flags{},
		func(ctx context.Context, a *app.App, in *StatusIn) (*AssistantStatus, error) {
			st := For(a).Status(AsPerson(ctx, a), in.Refresh)
			return &st, nil
		})
	ops.Def("assistant.models", "List the models and effort levels this machine's Claude Code offers (nothing is sent to Claude)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ModelsIn) (*AssistantModelsOut, error) {
			return For(a).Models(AsPerson(ctx, a), in.Refresh)
		})
	ops.Def("assistant.settings", "Show this machine's Claude Code settings (model, effort, limits, how it is billed)", ops.Flags{},
		func(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*AssistantSettingsOut, error) {
			return For(a).Settings(AsPerson(ctx, a)), nil
		})
	ops.Def("assistant.settings.set", "Change this machine's Claude Code settings (model, effort, spending limit per conversation, the switch for steps that run by themselves); owners", ops.Flags{Mutating: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *AssistantSettingsIn) (*AssistantSettingsOut, error) {
			return For(a).SetSettings(AsPerson(ctx, a), *in)
		})
	ops.Def("assistant.list", "List the Claude Code conversations (every machine of the workspace), most recent first", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ConversationListIn) (*ConversationListOut, error) {
			return For(a).List(AsPerson(ctx, a), *in)
		})
	ops.Def("assistant.show", "Show one conversation: its messages and what Claude did (with raw, the exact messages and tool output too)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ConversationShowIn) (*Conversation, error) {
			events := in.Events == nil || *in.Events
			return For(a).Get(AsPerson(ctx, a), in.ID, events, in.Raw)
		})
	ops.Def("assistant.suggestions", "Suggest a few natural prompts for the current project or workspace", ops.Flags{},
		func(ctx context.Context, a *app.App, in *AssistantSuggestionsIn) (*AssistantSuggestionsOut, error) {
			return For(a).Suggestions(AsPerson(ctx, a), *in)
		})
	ops.Def("assistant.ask", "Start a conversation with this machine's Claude Code (it only looks and suggests; people make the changes)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *AssistantAskIn) (*ConversationSendOut, error) {
			return For(a).Ask(AsPerson(ctx, a), *in)
		})
	ops.Def("assistant.continue", "Send a follow-up message (it waits while Claude is working); only on the machine the conversation runs on", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *AssistantContinueIn) (*ConversationSendOut, error) {
			return For(a).Continue(AsPerson(ctx, a), *in)
		})
	ops.Def("assistant.interrupt", "Stop what Claude is doing now (the conversation goes on; waiting messages are handed back)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ConversationIn) (*ConversationInterruptOut, error) {
			return For(a).Interrupt(AsPerson(ctx, a), in.ID)
		})
	ops.Def("assistant.stop", "Stop the conversation's Claude Code (a later message picks it up again)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ConversationIn) (*Conversation, error) {
			return For(a).Stop(AsPerson(ctx, a), in.ID)
		})
	ops.Def("assistant.dequeue", "Remove a waiting message before it is sent", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ConversationDequeueIn) (*Conversation, error) {
			return For(a).Dequeue(AsPerson(ctx, a), *in)
		})
	ops.Def("assistant.rename", "Rename a conversation", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ConversationRenameIn) (*Conversation, error) {
			return For(a).Rename(AsPerson(ctx, a), *in)
		})
	ops.Def("assistant.archive", "Archive (or restore) a conversation", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ConversationArchiveIn) (*Conversation, error) {
			return For(a).Archive(AsPerson(ctx, a), *in)
		})
	ops.Def("assistant.configure", "Change a conversation's model, effort or spending limit (from its next message on)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ConversationConfigureIn) (*Conversation, error) {
			return For(a).Configure(AsPerson(ctx, a), *in)
		})
	ops.Def("assistant.proposals", "List the changes and render requests Claude wrote in a conversation, each checked against the latest version of the cut", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ConversationIn) (*ConversationProposals, error) {
			return For(a).Proposals(AsPerson(ctx, a), in.ID, a.Actor.Via == "dashboard")
		})
	ops.Def("assistant.apply", "Apply one of the changes Claude wrote in a conversation, after checking it with the list of proposals", ops.Flags{Mutating: true, LocalOnly: true},
		func(ctx context.Context, a *app.App, in *ConversationApplyIn) (*ops.EditApplyOut, error) {
			return For(a).Apply(AsPerson(ctx, a), a, ops.Caller{}, *in)
		})
	ops.Def("assistant.act", "Do, answer or set aside what Claude asked for (one card, several at once, or a question card); people only", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *ConversationActIn) (*ConversationActOut, error) {
			return For(a).Act(AsPerson(ctx, a), a, ops.Caller{Human: true}, *in)
		})
}
