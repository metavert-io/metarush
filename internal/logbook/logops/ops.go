// Package logops registers the activity log's operations (log.*): list,
// search, show, export, purge, settings and the Claude Code hooks. Reading the
// log is open to every channel, but the dashboard assistant never sees the
// words of Claude conversations (its own or anyone else's): prompts, replies
// and tool details are left out on that channel.
package logops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// LogListIn filters the log. Every filter is optional.
type LogListIn struct {
	Since        string   `json:"since,omitempty" jsonschema:"description=How far back: a duration (2h\\, 30d) or a date (2026-10-01). Default: 7d (30d when searching)."`
	Until        string   `json:"until,omitempty" jsonschema:"description=Up to this time (same forms as since)."`
	Kind         []string `json:"kind,omitempty" jsonschema:"description=Entry kinds: op\\, op_summary\\, job\\, tool\\, claude\\, action\\, event\\, provider."`
	Level        []string `json:"level,omitempty" jsonschema:"description=info\\, warn\\, error."`
	Problems     bool     `json:"problems,omitempty" jsonschema:"description=Only errors and warnings."`
	Channel      []string `json:"channel,omitempty" jsonschema:"description=cli\\, dashboard\\, assistant\\, worker\\, watch\\, skill-eval\\, claude-terminal\\, mcp."`
	Op           string   `json:"op,omitempty" jsonschema:"maxLength=80"`
	Project      string   `json:"project,omitempty" jsonschema:"description=Project ID."`
	Asset        string   `json:"asset,omitempty" jsonschema:"description=Clip (asset) ID."`
	Job          string   `json:"job,omitempty"`
	Render       string   `json:"render,omitempty"`
	Conversation string   `json:"conversation,omitempty" jsonschema:"description=Claude conversation: a dashboard conversation\\, a skill-test case or a Claude Code session ID."`
	Machine      string   `json:"machine,omitempty" jsonschema:"description=Installation ID."`
	IDs          []string `json:"ids,omitempty" jsonschema:"description=These entries only."`
	Query        string   `json:"query,omitempty" jsonschema:"maxLength=200,description=Words to search for."`
	Programs     bool     `json:"programs,omitempty" jsonschema:"description=Include successful program runs (ffmpeg\\, ffprobe…) and online-service requests (ElevenLabs); failed ones are always included."`
	Before       string   `json:"before,omitempty" jsonschema:"description=Continue after a previous page (its next value)."`
	Limit        int      `json:"limit,omitempty" jsonschema:"minimum=0,maximum=1000"`
}

// LogRow is an entry with its plain row text and where its object lives.
type LogRow struct {
	logbook.Entry
	Text         string          `json:"text" jsonschema:"description=What happened\\, in plain words."`
	Duration     string          `json:"duration,omitempty" jsonschema:"description=How long it took\\, for people (12 s)."`
	ChannelLabel string          `json:"channelLabel" jsonschema:"description=Where it came from\\, for people."`
	Target       *logbook.Target `json:"target,omitempty"`
}

// LogListOut is a page of the log, newest first.
type LogListOut struct {
	Entries []LogRow  `json:"entries"`
	Next    string    `json:"next,omitempty" jsonschema:"description=Pass as before to read the next (older) page."`
	Since   time.Time `json:"since"`
	Privacy string    `json:"privacy" jsonschema:"enum=workspace,enum=local,enum=off,description=Where this machine keeps the words of Claude conversations."`
}

// LogShowIn names one entry.
type LogShowIn struct {
	ID string `json:"id" jsonschema:"pattern=^log_[0-9a-z]{26}$"`
}

// LogShowOut is one entry with its full texts and what belongs to it.
type LogShowOut struct {
	Entry    LogRow   `json:"entry"`
	Prompt   string   `json:"prompt,omitempty" jsonschema:"description=The full prompt (Claude turns)."`
	Response string   `json:"response,omitempty" jsonschema:"description=Claude's full final reply."`
	Stderr   string   `json:"stderr,omitempty" jsonschema:"description=The end of a failed program's error output."`
	Note     string   `json:"note,omitempty" jsonschema:"description=Why a text is not shown (kept on another machine\\, not recorded)."`
	Parent   *LogRow  `json:"parent,omitempty"` // the operation this entry belongs to
	Children []LogRow `json:"children" jsonschema:"description=Program runs and events this operation caused."`
	Related  []LogRow `json:"related" jsonschema:"description=Other entries of the same conversation or job (newest first)."`
}

// LogExportIn selects entries to export.
type LogExportIn struct {
	LogListIn
	Bodies bool `json:"bodies,omitempty" jsonschema:"description=Include the full texts (prompts\\, replies\\, error output) kept outside the entries."`
}

// ExportBodies are an entry's long texts.
type ExportBodies struct {
	Prompt   string `json:"prompt,omitempty"`
	Response string `json:"response,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// ExportEntry is an exported entry.
type ExportEntry struct {
	logbook.Entry
	Bodies *ExportBodies `json:"bodies,omitempty"`
}

// LogExportOut is a page of exported entries (newest first).
type LogExportOut struct {
	Entries []ExportEntry `json:"entries"`
	Next    string        `json:"next,omitempty"`
}

// LogPurgeIn deletes entries older than a date.
type LogPurgeIn struct {
	Before      string `json:"before" jsonschema:"minLength=1,description=Delete entries older than this: a date (2026-09-01) or a duration back from now (90d)."`
	ThisMachine bool   `json:"thisMachine,omitempty" jsonschema:"description=Only this machine's entries."`
}

// LogPurgeOut says what was deleted.
type LogPurgeOut struct {
	logbook.PurgeResult
	Before time.Time `json:"before"`
}

// LogSettingsOut is the log's configuration and health on this machine.
type LogSettingsOut struct {
	logbook.Status
	Hooks       logbook.HookStatus `json:"hooks"`
	Explanation string             `json:"explanation" jsonschema:"description=What the settings mean\\, in plain words."`
}

// LogConfigureIn changes the log settings (config.yaml, log section).
type LogConfigureIn struct {
	Prompts       *string `json:"prompts,omitempty" jsonschema:"enum=workspace,enum=local,enum=off"`
	RetentionDays *int    `json:"retentionDays,omitempty" jsonschema:"minimum=0,maximum=36500,description=0 keeps everything until you delete it."`
}

// HooksIn changes nothing (the hooks use this machine's Claude Code settings).
type HooksIn struct{}

// HooksOut is what turning the Claude Code hooks on or off did.
type HooksOut struct {
	Change *logbook.HookChange `json:"change"`
	Status logbook.HookStatus  `json:"status"`
	Note   string              `json:"note"`
}

var channelLabels = map[string]string{
	logbook.ChannelCLI: "command line", logbook.ChannelDashboard: "dashboard", logbook.ChannelAssistant: "Claude in the dashboard",
	logbook.ChannelWorker: "MetaRush in the background", logbook.ChannelWatch: "watched folder", logbook.ChannelSkillEval: "skill test",
	logbook.ChannelClaudeTerminal: "Claude Code in a terminal", logbook.ChannelMCP: "a connected app", logbook.ChannelRecipe: "a recipe run",
}

// ChannelLabel says where an entry came from, for people.
func ChannelLabel(ch string) string {
	if s, ok := channelLabels[ch]; ok {
		return s
	}
	return ch
}

func assistantChannel(a *app.App) bool { return a.Actor.Via == models.ViaAssistant }

// filter turns the input into a query (with defaults).
func filter(in *LogListIn, now time.Time) (logbook.Filter, error) {
	f := logbook.Filter{Kinds: in.Kind, Levels: in.Level, Channels: in.Channel, Op: in.Op, ProjectID: in.Project, AssetID: in.Asset,
		JobID: in.Job, RenderID: in.Render, ConversationID: in.Conversation, InstallationID: in.Machine, IDs: in.IDs, Before: in.Before,
		Limit: in.Limit, Query: in.Query}
	for _, k := range in.Kind {
		if !contains(logbook.Kinds, k) {
			return f, apperr.New(apperr.InvalidInput, "unknown kind %q (use %s)", k, strings.Join(logbook.Kinds, ", "))
		}
	}
	for _, c := range in.Channel {
		if !contains(logbook.Channels, c) {
			return f, apperr.New(apperr.InvalidInput, "unknown channel %q (use %s)", c, strings.Join(logbook.Channels, ", "))
		}
	}
	if in.Problems {
		f.Levels = []string{logbook.LevelWarn, logbook.LevelError}
	}
	// Successful program runs are details: shown when asked for, by kind, by
	// job or by ID.
	f.HideRoutine = !in.Programs && !contains(in.Kind, logbook.KindTool) && !contains(in.Kind, logbook.KindProvider) && len(in.IDs) == 0 && in.Job == ""
	// The everyday view: a job a command ran itself is told by the command's entry.
	f.HideInProcess = len(in.Kind) == 0 && !in.Programs && len(in.IDs) == 0 && in.Job == ""
	since := in.Since
	if since == "" && len(in.IDs) == 0 && in.Job == "" && in.Conversation == "" && in.Render == "" {
		since = "7d"
		if in.Query != "" {
			since = "30d"
		}
	}
	var err error
	if f.Since, err = logbook.ParseWhen(since, now); err != nil {
		return f, apperr.New(apperr.InvalidInput, "%s", err.Error())
	}
	if f.Until, err = logbook.ParseWhen(in.Until, now); err != nil {
		return f, apperr.New(apperr.InvalidInput, "%s", err.Error())
	}
	return f, nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// Rows describes entries for people. On the assistant channel the words of
// Claude conversations are removed first.
func Rows(ctx context.Context, a *app.App, es []logbook.Entry) []LogRow {
	if assistantChannel(a) {
		for i := range es {
			hideConversation(&es[i])
		}
	}
	names := logbook.LoadNames(ctx, a.DB, a.WorkspaceID(), es)
	out := make([]LogRow, 0, len(es))
	for i := range es {
		e := &es[i]
		summary := ""
		if e.Op != nil {
			if op, ok := ops.Get(e.Op.Name); ok {
				summary = op.Summary
			}
		}
		out = append(out, LogRow{Entry: *e, Text: logbook.Describe(e, names, summary), Duration: logbook.FormatDuration(e.DurationMs),
			ChannelLabel: ChannelLabel(e.Channel), Target: logbook.TargetOf(e, names)})
	}
	return out
}

// hideConversation removes what Claude was asked and answered (the dashboard
// assistant may not read conversations).
func hideConversation(e *logbook.Entry) {
	if c := e.Claude; c != nil {
		c.Prompt, c.Response, c.PromptRef, c.ResponseRef, c.Title = "", "", nil, nil, ""
		for i := range c.Tools {
			c.Tools[i].Summary, c.Tools[i].Result = "", ""
		}
		e.Summary = "asked Claude Code something"
		e.Error = nil
	}
	if e.Op != nil {
		if _, ok := e.Op.Input["prompt"]; ok {
			e.Op.Input["prompt"] = "[not shown to Claude]"
		}
	}
}

func init() {
	ops.Def("log.list", "Read the activity log (newest first): operations, jobs, program runs, Claude turns and decisions, with filters", ops.Flags{},
		func(ctx context.Context, a *app.App, in *LogListIn) (*LogListOut, error) {
			f, err := filter(in, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			f.NoPrompts = assistantChannel(a)
			es, next, err := logbook.List(ctx, a.DB, a.WorkspaceID(), f)
			if err != nil {
				return nil, err
			}
			return &LogListOut{Entries: Rows(ctx, a, es), Next: next, Since: f.Since, Privacy: a.Log.Privacy()}, nil
		})
	ops.Def("log.search", "Search the activity log for words (summaries, errors, prompts and replies), newest first", ops.Flags{},
		func(ctx context.Context, a *app.App, in *LogListIn) (*LogListOut, error) {
			if strings.TrimSpace(in.Query) == "" {
				return nil, apperr.New(apperr.InvalidInput, "say what to search for")
			}
			f, err := filter(in, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			f.NoPrompts = assistantChannel(a)
			es, next, err := logbook.List(ctx, a.DB, a.WorkspaceID(), f)
			if err != nil {
				return nil, err
			}
			return &LogListOut{Entries: Rows(ctx, a, es), Next: next, Since: f.Since, Privacy: a.Log.Privacy()}, nil
		})
	ops.Def("log.show", "Show one log entry in full: the prompt and reply of a Claude turn, a failed program's error output, and what belongs to it", ops.Flags{},
		func(ctx context.Context, a *app.App, in *LogShowIn) (*LogShowOut, error) {
			return show(ctx, a, in.ID)
		})
	ops.Def("log.export", "Export log entries (newest first) with their full texts, for a bug report or analysis", ops.Flags{},
		func(ctx context.Context, a *app.App, in *LogExportIn) (*LogExportOut, error) {
			if assistantChannel(a) {
				return nil, apperr.New(apperr.Unauthorized, "the dashboard assistant cannot export the log").WithDetails(map[string]any{"reason": "assistant_log_export"})
			}
			f, err := filter(&in.LogListIn, time.Now().UTC())
			if err != nil {
				return nil, err
			}
			// An export is the full record: routine program runs and requests and the
			// events of jobs a command ran itself are included.
			f.HideRoutine, f.HideInProcess = false, false
			es, next, err := logbook.List(ctx, a.DB, a.WorkspaceID(), f)
			if err != nil {
				return nil, err
			}
			out := &LogExportOut{Entries: make([]ExportEntry, 0, len(es)), Next: next}
			for _, e := range es {
				x := ExportEntry{Entry: e}
				if in.Bodies {
					x.Bodies = bodies(ctx, a, &e)
				}
				out.Entries = append(out.Entries, x)
			}
			return out, nil
		})
	ops.Def("log.purge", "Delete log entries older than a date (and the texts only they used); people only, owners", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *LogPurgeIn) (*LogPurgeOut, error) {
			before, err := logbook.ParseWhen(in.Before, time.Now().UTC())
			if err != nil || before.IsZero() {
				return nil, apperr.New(apperr.InvalidInput, "say which date to delete before (for example 2026-09-01 or 90d)")
			}
			if before.After(time.Now().UTC()) {
				return nil, apperr.New(apperr.InvalidInput, "that date is in the future")
			}
			o := logbook.PurgeOptions{Before: before}
			if in.ThisMachine {
				o.InstallationID = a.Cfg.Installation.ID
			}
			res, err := logbook.Purge(ctx, a.Log, o)
			if err != nil {
				return nil, err
			}
			a.Audit(ctx, "log.purge", "workspace", a.WorkspaceID(), "", map[string]any{"before": before, "deleted": res.Deleted, "bodies": res.Bodies, "thisMachine": in.ThisMachine})
			return &LogPurgeOut{PurgeResult: *res, Before: before}, nil
		})
	ops.Def("log.settings", "Show the log settings on this machine: where Claude conversation text is kept, how long entries stay, the Claude Code recording, what waits to be saved", ops.Flags{},
		func(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*LogSettingsOut, error) {
			return settings(a), nil
		})
	ops.Def("log.configure", "Change the log settings in config.yaml: where Claude conversation text is kept (workspace, local, off) and how many days entries stay (0 = until you delete them); owners", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *LogConfigureIn) (*LogSettingsOut, error) {
			return configure(ctx, a, in)
		})
	ops.Def("log.hooks.status", "Check whether this machine's Claude Code records MetaRush terminal sessions into the log (opt-in hooks)", ops.Flags{},
		func(ctx context.Context, a *app.App, _ *HooksIn) (*logbook.HookStatus, error) {
			st := hookStatus(a)
			return &st, nil
		})
	ops.Def("log.hooks.enable", "Record Claude Code terminal sessions that use MetaRush: adds hooks to this machine's Claude Code settings; people only, owners", ops.Flags{Mutating: true, HumanOnly: true, LocalOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, _ *HooksIn) (*HooksOut, error) {
			return setHooks(ctx, a, true)
		})
	ops.Def("log.hooks.disable", "Stop recording Claude Code terminal sessions: removes MetaRush's hooks (and nothing else) from Claude Code's settings; people only, owners", ops.Flags{Mutating: true, HumanOnly: true, LocalOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, _ *HooksIn) (*HooksOut, error) {
			return setHooks(ctx, a, false)
		})
}

// Show is log.show without recording a read in the log (the weekly
// improvement report hands Claude Code the entries behind its signals).
func Show(ctx context.Context, a *app.App, id string) (*LogShowOut, error) { return show(ctx, a, id) }

func show(ctx context.Context, a *app.App, id string) (*LogShowOut, error) {
	e, err := logbook.Get(ctx, a.DB, a.WorkspaceID(), id)
	if err != nil {
		return nil, err
	}
	out := &LogShowOut{Children: []LogRow{}, Related: []LogRow{}}
	if !assistantChannel(a) {
		if b := bodies(ctx, a, e); b != nil {
			out.Prompt, out.Response, out.Stderr = b.Prompt, b.Response, b.Stderr
		}
		out.Note = bodyNote(a, e)
		if e.Claude != nil && out.Prompt == "" {
			out.Prompt = e.Claude.Prompt
		}
	} else if e.Claude != nil {
		out.Note = "The words of Claude conversations are not shown to Claude in the dashboard."
	}
	row := Rows(ctx, a, []logbook.Entry{*e})[0]
	out.Entry = row
	if kids, _, err := logbook.List(ctx, a.DB, a.WorkspaceID(), logbook.Filter{ParentID: e.ID, Limit: 200}); err == nil {
		out.Children = Rows(ctx, a, kids)
	}
	if e.ParentID != "" {
		if p, err := logbook.Get(ctx, a.DB, a.WorkspaceID(), e.ParentID); err == nil {
			pr := Rows(ctx, a, []logbook.Entry{*p})[0]
			out.Parent = &pr
		}
	}
	// Related: the rest of a Claude conversation, or the rest of a job's story.
	var rel logbook.Filter
	switch {
	case e.Refs.ConversationID != "" && (e.Kind == logbook.KindClaude || e.Kind == logbook.KindAction || e.Kind == logbook.KindEvent):
		rel = logbook.Filter{ConversationID: e.Refs.ConversationID, Kinds: []string{logbook.KindClaude, logbook.KindAction, logbook.KindEvent}, Limit: 100}
	case e.Refs.JobID != "" && (e.Kind == logbook.KindJob || e.Kind == logbook.KindTool || e.Kind == logbook.KindProvider):
		rel = logbook.Filter{JobID: e.Refs.JobID, Limit: 100}
	}
	if rel.Limit > 0 {
		if es, _, err := logbook.List(ctx, a.DB, a.WorkspaceID(), rel); err == nil {
			var keep []logbook.Entry
			for _, x := range es {
				if x.ID != e.ID {
					keep = append(keep, x)
				}
			}
			out.Related = Rows(ctx, a, keep)
		}
	}
	return out, nil
}

// bodies reads an entry's long texts from wherever log.prompts put them.
func bodies(ctx context.Context, a *app.App, e *logbook.Entry) *ExportBodies {
	b := &ExportBodies{}
	if c := e.Claude; c != nil {
		b.Prompt, b.Response = c.Prompt, c.Response
		if c.PromptRef != nil {
			if s, err := a.Log.ReadBody(ctx, c.PromptRef); err == nil {
				b.Prompt = s
			}
		}
		if c.ResponseRef != nil {
			if s, err := a.Log.ReadBody(ctx, c.ResponseRef); err == nil {
				b.Response = s
			}
		}
	}
	if t := e.Tool; t != nil {
		b.Stderr = t.StderrTail
		if t.StderrRef != nil {
			if s, err := a.Log.ReadBody(ctx, t.StderrRef); err == nil {
				b.Stderr = s
			}
		}
	}
	if b.Prompt == "" && b.Response == "" && b.Stderr == "" {
		return nil
	}
	return b
}

func bodyNote(a *app.App, e *logbook.Entry) string {
	c := e.Claude
	if c == nil {
		return ""
	}
	for _, r := range []*logbook.BodyRef{c.PromptRef, c.ResponseRef} {
		if r != nil && r.Store == logbook.StoreLocal && r.InstallationID != a.Cfg.Installation.ID {
			return "The words of this conversation are kept only on " + firstNonEmpty(e.Machine, "the machine that recorded it") + "."
		}
	}
	if c.Privacy == logbook.PrivacyOff {
		return "The words of this conversation were not recorded: the log was set not to keep them."
	}
	return ""
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

func hookStatus(a *app.App) logbook.HookStatus {
	p, err := logbook.ClaudeSettingsPath()
	if err != nil {
		return logbook.HookStatus{Error: err.Error(), Events: []string{}}
	}
	return logbook.HooksStatus(p, a.Cfg.Home())
}

func explain(s logbook.Status, h logbook.HookStatus) string {
	var parts []string
	switch s.Prompts {
	case config.PromptsLocal:
		if s.Problem != "" {
			parts = append(parts, capitalize(s.Problem)+".")
		} else {
			parts = append(parts, "What you ask Claude and what it answers is kept only on this machine.")
		}
	case config.PromptsOff:
		parts = append(parts, "What you ask Claude and what it answers is not recorded; the log still says that a conversation happened.")
	default:
		parts = append(parts, "What you ask Claude and what it answers is kept with the workspace, so anyone who can open this workspace's database can read it.")
	}
	if s.RetentionDays > 0 {
		parts = append(parts, fmt.Sprintf("Entries from this machine are removed after %d days.", s.RetentionDays))
	} else {
		parts = append(parts, "Entries stay until you delete them.")
	}
	switch {
	case h.Enabled && !h.OtherHome:
		parts = append(parts, "Claude Code sessions in a terminal that use MetaRush are recorded too.")
	case h.Enabled:
		parts = append(parts, "Claude Code terminal sessions are recorded by another MetaRush setup on this machine.")
	default:
		parts = append(parts, "Claude Code sessions in a terminal are not recorded.")
	}
	if s.Spool.Entries > 0 {
		parts = append(parts, fmt.Sprintf("%d entries are waiting to be saved because the database could not be reached.", s.Spool.Entries))
	}
	return strings.Join(parts, " ")
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func settings(a *app.App) *LogSettingsOut {
	st := a.Log.Status()
	h := hookStatus(a)
	return &LogSettingsOut{Status: st, Hooks: h, Explanation: explain(st, h)}
}

func configure(ctx context.Context, a *app.App, in *LogConfigureIn) (*LogSettingsOut, error) {
	if in.Prompts == nil && in.RetentionDays == nil {
		return nil, apperr.New(apperr.InvalidInput, "Say what to change: where Claude conversation words are kept, or how many days entries stay.")
	}
	next := a.Log.Settings()
	prev := next
	if in.Prompts != nil {
		switch *in.Prompts {
		case config.PromptsWorkspace, config.PromptsLocal, config.PromptsOff:
		default:
			return nil, apperr.New(apperr.InvalidInput, "prompts must be workspace, local or off")
		}
		if err := config.SetField(a.Cfg.Home(), []string{"log", "prompts"}, *in.Prompts); err != nil {
			return nil, apperr.Wrap(apperr.Internal, err, "Couldn't save the log settings in config.yaml on this machine.")
		}
		next.Prompts = *in.Prompts
	}
	if in.RetentionDays != nil {
		if *in.RetentionDays < 0 {
			return nil, apperr.New(apperr.InvalidInput, "The number of days must be 0 (keep everything) or more.")
		}
		if err := config.SetField(a.Cfg.Home(), []string{"log", "retentionDays"}, *in.RetentionDays); err != nil {
			return nil, apperr.Wrap(apperr.Internal, err, "Couldn't save the log settings in config.yaml on this machine.")
		}
		next.RetentionDays = *in.RetentionDays
	}
	a.Log.SetSettings(next)
	a.Audit(ctx, "log.configure", "installation", a.Cfg.Installation.ID, "", map[string]any{"prompts": next.PromptsMode(), "retentionDays": next.RetentionDays,
		"previousPrompts": prev.PromptsMode(), "previousRetentionDays": prev.RetentionDays})
	if ops.OnSettingsChanged != nil {
		ops.OnSettingsChanged()
	}
	return settings(a), nil
}

// hooksErr says in plain words why Claude Code's settings were not changed;
// the technical reason stays in Details.
func hooksErr(err error) error {
	var se *logbook.SettingsError
	if errors.As(err, &se) {
		return apperr.Wrap(apperr.InvalidInput, err, "%s", se.Plain).WithDetails(map[string]any{"error": se.Err.Error()})
	}
	return apperr.Wrap(apperr.InvalidInput, err, "%s", err.Error())
}

func setHooks(ctx context.Context, a *app.App, on bool) (*HooksOut, error) {
	p, err := logbook.ClaudeSettingsPath()
	if err != nil {
		return nil, apperr.Wrap(apperr.NotConfigured, err, "Couldn't find Claude Code's settings on this machine.")
	}
	var ch *logbook.HookChange
	if on {
		exe, err := os.Executable()
		if err != nil {
			return nil, apperr.Wrap(apperr.Internal, err, "Couldn't find where MetaRush is installed on this machine.")
		}
		if ch, err = logbook.EnableHooks(p, exe, a.Cfg.Home()); err != nil {
			return nil, hooksErr(err)
		}
		_ = logbook.WriteFingerprints(a.Cfg.ClaudeHooksDir(), a.Log.Redactor())
	} else {
		if ch, err = logbook.DisableHooks(p); err != nil {
			return nil, hooksErr(err)
		}
	}
	a.Audit(ctx, map[bool]string{true: "log.hooks.enable", false: "log.hooks.disable"}[on], "installation", a.Cfg.Installation.ID, "",
		map[string]any{"changed": ch.Changed, "added": ch.Added, "updated": ch.Updated, "removed": ch.Removed})
	out := &HooksOut{Change: ch, Status: hookStatus(a)}
	if on {
		out.Note = "Claude Code now tells MetaRush about every session on this machine, but only sessions that use MetaRush are recorded; the others are forgotten at the end of each reply. New Claude Code sessions pick this up when they start."
	} else {
		out.Note = "Claude Code sessions are no longer recorded. What was recorded stays in the log."
	}
	return out, nil
}
