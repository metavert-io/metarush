package logbook

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// JobAdvice says what to do about a failed job in plain words. The ops package
// sets it (job details use the same function).
var JobAdvice func(j *models.Job) string

// jobDetailKeys are payload and result values worth keeping with job events.
var jobDetailKeys = map[string]bool{"quality": true, "preset": true, "force": true, "language": true, "speakers": true, "seconds": true,
	"reportId": true, "verdict": true, "voices": true, "scenes": true, "silences": true, "words": true, "segments": true, "renderId": true,
	"revisionId": true, "revisionNumber": true, "variant": true,
	// Narration: what kind of work, how many lines, and what it cost.
	"action": true, "lines": true, "generated": true, "reused": true, "credits": true,
	// Link imports: the site and the video's title (public facts about the original).
	"platform": true, "title": true,
	// Moving the media: the new storage in words, and how many files were copied.
	"storage": true, "copied": true,
	// Generated video: what was asked for, from whom, which take and what it cost (never the prompt).
	"generationId": true, "what": true, "provider": true, "resolution": true, "take": true, "costUsd": true,
	// Remotion sources: which composition a render made (never the project's code or props).
	"composition": true, "compositions": true, "versionNumber": true, "videos": true}

// JobEvent records a job lifecycle event (jobs.Queue.OnEvent). Progress is
// recorded sparsely: when it passes a quarter, half and three quarters.
func (l *Logger) JobEvent(ctx context.Context, ev jobs.Event) {
	if l == nil {
		return
	}
	j := ev.Job
	switch ev.Kind {
	case jobs.EventProgress:
		if !l.milestone(j.ID, ev.Progress.Pct) {
			return
		}
	case jobs.EventSucceeded, jobs.EventFailed, jobs.EventCanceled, jobs.EventReleased:
		l.mu.Lock()
		delete(l.jobPct, j.ID)
		l.mu.Unlock()
	case jobs.EventLeased:
		l.mu.Lock()
		l.jobPct[j.ID] = 0
		l.mu.Unlock()
	}
	sc := ScopeOf(ctx)
	if sc.rec != nil { // an operation running the job itself (narration, --local renders)
		switch ev.Kind {
		case jobs.EventLeased:
			sc.rec.setJob(j.ID)
		case jobs.EventSucceeded, jobs.EventFailed, jobs.EventCanceled, jobs.EventReleased:
			sc.rec.endJob(j.ID)
		}
	}
	refs := Refs{JobID: j.ID, ProjectID: j.ProjectID, AssetID: j.AssetID, RenderID: j.RenderID}
	if rid, _ := j.Payload["renderId"].(string); rid != "" && refs.RenderID == "" {
		refs.RenderID = rid
	}
	if rev, _ := j.Payload["revisionId"].(string); rev != "" {
		refs.RevisionID = rev
	}
	if q, _ := j.Result["reportId"].(string); q != "" {
		refs.QAReportID = q
	}
	refs.merge(sc.Refs)
	ch := sc.Channel
	switch {
	case sc.rec != nil: // run by the operation itself: its channel
	case ev.Kind == jobs.EventLeased, ev.Kind == jobs.EventProgress, ev.Kind == jobs.EventSucceeded, ev.Kind == jobs.EventFailed, ev.Kind == jobs.EventRetry,
		ev.Kind == jobs.EventReleased:
		ch = ChannelWorker
	case ev.Kind == jobs.EventCanceled && ev.WorkerID != "":
		ch = ChannelWorker
	}
	if ch == "" {
		ch = l.channelFor(l.actor.Via)
	}
	info := &JobInfo{Type: j.Type, Event: ev.Kind, State: j.State, Attempt: j.AttemptCount, MaxAttempts: j.MaxAttempts, Heavy: j.Heavy,
		Worker: ev.WorkerName, Message: j.Progress.Message, InProcess: sc.rec != nil && ev.Kind != jobs.EventQueued}
	if info.Worker == "" && j.Lease != nil {
		info.Worker = j.Lease.OwnerName
	}
	if info.Worker == "" && len(j.Attempts) > 0 {
		info.Worker = j.Attempts[len(j.Attempts)-1].WorkerName
	}
	detail := map[string]any{}
	for k, v := range j.Payload {
		if jobDetailKeys[k] {
			detail[k] = v
		}
	}
	if ev.Kind == jobs.EventSucceeded {
		for k, v := range j.Result {
			if jobDetailKeys[k] {
				detail[k] = v
			}
		}
	}
	if len(detail) > 0 {
		info.Detail = detail
	}
	e := Entry{Kind: KindJob, Channel: ch, UserID: j.CreatedBy.UserID, UserName: j.CreatedBy.UserName, ParentID: sc.EntryID, Refs: refs, Job: info,
		Summary: fmt.Sprintf("%s job %s", j.Type, ev.Kind)}
	p := &pending{e: e}
	switch ev.Kind {
	case jobs.EventProgress:
		pct := ev.Progress.Pct
		info.Pct, info.Message = &pct, ev.Progress.Message
	case jobs.EventLeased:
		start := j.CreatedAt
		if j.NextRunAt.After(start) {
			start = j.NextRunAt
		}
		if n := len(j.Attempts); n > 0 && !start.IsZero() {
			if w := j.Attempts[n-1].StartedAt.Sub(start); w > 0 {
				info.WaitedMs = ms(w)
			}
		}
	case jobs.EventSucceeded:
		if n := len(j.Attempts); n > 0 {
			p.e.DurationMs = ms(time.Since(j.Attempts[n-1].StartedAt))
		}
	case jobs.EventRetry:
		p.e.Level = LevelWarn
		p.e.Error = &EntryError{Message: Clip(ev.Error, 2000)}
		if !ev.NextRunAt.IsZero() {
			t := ev.NextRunAt.UTC()
			info.NextRunAt = &t
		}
	case jobs.EventFailed:
		p.e.Level = LevelError
		p.e.Error = &EntryError{Message: Clip(firstNonEmpty(ev.Error, j.Error), 2000)}
		if JobAdvice != nil {
			jj := j
			jj.State, jj.Error = models.JobFailed, firstNonEmpty(ev.Error, j.Error)
			p.e.Error.Advice = JobAdvice(&jj)
		}
		if n := len(j.Attempts); n > 0 {
			p.e.DurationMs = ms(time.Since(j.Attempts[n-1].StartedAt))
		}
		p.versions = "-"
	case jobs.EventCanceled:
		p.e.Level = LevelWarn
	}
	l.add(p)
}

// milestone reports whether progress pct passed a new quarter for job id.
func (l *Logger) milestone(id string, pct float64) bool {
	q := int(pct * 4)
	if q < 1 || q > 3 {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if last, ok := l.jobPct[id]; ok && last >= q {
		return false
	}
	if len(l.jobPct) > 5000 { // a worker that never saw these jobs finish: forget the oldest state
		l.jobPct = map[string]int{}
	}
	l.jobPct[id] = q
	return true
}

// ToolRun records one run of an external program (tools.Set.OnRun): program,
// redacted arguments, duration, exit status, and the end of its error output
// only when it failed. Its normal output is never kept.
func (l *Logger) ToolRun(ctx context.Context, info tools.RunInfo) {
	if l == nil {
		return
	}
	sc := ScopeOf(ctx)
	ch := sc.Channel
	if ch == "" {
		ch = l.channelFor(l.actor.Via)
	}
	ok := info.Err == nil
	e := Entry{Kind: KindTool, Channel: ch, UserID: l.actor.UserID, UserName: l.actor.UserName, ParentID: sc.EntryID, Refs: refsOf(sc),
		DurationMs: ms(info.Duration), Summary: "ran " + info.Tool,
		Tool: &ToolInfo{Program: info.Tool, Args: append([]string(nil), info.Args...), ExitCode: info.ExitCode, OK: ok, Canceled: info.Canceled}}
	p := &pending{e: e}
	switch {
	case ok:
	case info.Canceled:
		// Not a problem in itself: a person stopped the work (its job says so)
		// or MetaRush was shutting down.
		p.e.Summary = info.Tool + " was stopped"
	default:
		p.e.Level = LevelError
		msg := info.Err.Error()
		var ef *tools.ErrFailed
		if asFailed(info.Err, &ef) {
			msg = ef.Tool + " failed: " + ef.Err.Error() // the error output is kept separately
		}
		p.e.Error = &EntryError{Message: Clip(msg, 1000)}
		p.e.Summary = info.Tool + " failed"
		if tail := Tail(string(info.Stderr), maxStderrTail); tail != "" {
			p.bodies = append(p.bodies, spoolBody{Slot: "stderr", Text: tail})
		}
		p.versions = info.Tool
	}
	l.add(p)
}

func asFailed(err error, dst **tools.ErrFailed) bool {
	for err != nil {
		if f, ok := err.(*tools.ErrFailed); ok {
			*dst = f
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Turn is one prompt and Claude Code's work on it, from the dashboard's Claude
// panel, a skill test or a Claude Code terminal session.
type Turn struct {
	Source         string // assistant | skill-eval | claude-terminal
	ConversationID string
	Turn           int
	Title          string
	Prompt         string
	Response       string
	Tools          []ClaudeTool
	Actions        []ActionInfo
	Model, Effort  string
	State          string // succeeded, failed, interrupted…
	Error          string
	CostUSD        float64
	Steps, Denials int
	StartedAt      time.Time
	Duration       time.Duration
	Refs           Refs // the project, video or version the turn was about
	UserID         string
	UserName       string
	Case           string // skill-eval case
	Pass           *bool  // skill-eval verdict
	// ToolUses counts every tool call of the turn when Tools lists only some
	// (terminal sessions keep only the MetaRush commands).
	ToolUses int
	// PromptChars is the prompt's length when the text itself was not kept.
	PromptChars int
	// Note marks a message MetaRush wrote (an action's outcome), not a person.
	Note bool
}

// Claude records one turn: the full prompt and final response (kept as
// log.prompts says), tool calls with short results and the action cards it
// produced. It returns the entry's ID.
func (l *Logger) Claude(ctx context.Context, t Turn) string {
	if l == nil {
		return ""
	}
	privacy := l.Privacy()
	ch := map[string]string{SourceAssistant: ChannelAssistant, SourceSkillEval: ChannelSkillEval, SourceClaudeTerminal: ChannelClaudeTerminal}[t.Source]
	if ch == "" {
		ch = l.channelFor(l.actor.Via)
	}
	refs := t.Refs
	refs.ConversationID = firstNonEmpty(t.ConversationID, refs.ConversationID)
	info := &ClaudeInfo{Source: t.Source, Turn: t.Turn, Title: t.Title, Privacy: privacy, PromptChars: utf8.RuneCountInString(t.Prompt),
		ResponseChars: utf8.RuneCountInString(t.Response), Tools: t.Tools, ToolCount: len(t.Tools), Actions: t.Actions, Model: t.Model,
		Effort: t.Effort, CostUSD: t.CostUSD, Steps: t.Steps, Denials: t.Denials, State: t.State, Case: t.Case, Pass: t.Pass, Note: t.Note}
	if len(info.Tools) > 200 {
		info.Tools = info.Tools[:200]
	}
	if t.ToolUses > info.ToolCount {
		info.ToolCount = t.ToolUses
	}
	if info.PromptChars == 0 {
		info.PromptChars = t.PromptChars
	}
	e := Entry{Kind: KindClaude, Channel: ch, At: t.StartedAt.UTC(), UserID: firstNonEmpty(t.UserID, l.actor.UserID),
		UserName: firstNonEmpty(t.UserName, l.actor.UserName), DurationMs: ms(t.Duration), Refs: refs, Claude: info}
	if e.At.IsZero() {
		e.At = l.now()
	}
	switch privacy {
	case PrivacyWorkspace:
		e.Summary = "asked Claude Code: " + Clip(oneLine(t.Prompt), 100)
	case PrivacyLocal:
		e.Summary = "asked Claude Code (the text is kept on " + firstNonEmpty(l.machine, "this machine") + ")"
	default:
		e.Summary = "asked Claude Code (the text is not recorded)"
	}
	switch t.State {
	case "", "succeeded", "success":
	case "interrupted", "stopped", "canceled":
		e.Level = LevelWarn
	default:
		e.Level = LevelError
	}
	if t.Error != "" {
		e.Error = &EntryError{Message: Clip(t.Error, 2000)}
	}
	e.ID = newID()
	p := &pending{e: e}
	if privacy != PrivacyOff {
		p.bodies = []spoolBody{{Slot: "prompt", Text: t.Prompt}, {Slot: "response", Text: t.Response}}
	}
	l.add(p)
	return e.ID
}

// ActionOutcome is a person's decision on an action card.
type ActionOutcome struct {
	Source         string
	ConversationID string
	Action         ActionInfo
	Refs           Refs
	UserID         string
	UserName       string
}

// ActionDecision records what a person did with a change Claude asked for.
func (l *Logger) ActionDecision(ctx context.Context, o ActionOutcome) {
	if l == nil {
		return
	}
	refs := o.Refs
	refs.ConversationID = firstNonEmpty(o.ConversationID, refs.ConversationID)
	a := o.Action
	e := Entry{Kind: KindAction, Channel: ChannelDashboard, UserID: firstNonEmpty(o.UserID, l.actor.UserID), UserName: firstNonEmpty(o.UserName, l.actor.UserName),
		Refs: refs, Action: &a, Summary: "a change Claude asked for was " + a.Decision}
	if o.Source == SourceClaudeTerminal {
		e.Channel = ChannelClaudeTerminal
	}
	switch a.Decision {
	case DecisionFailed:
		e.Level = LevelError
		e.Error = &EntryError{Message: Clip(a.Error, 2000)}
	case DecisionDismissed, DecisionUnavailable:
		e.Level = LevelInfo
	}
	l.add(&pending{e: e})
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Conversation lifecycle events (ConversationEvent).
const (
	ConvLaunch    = "launch"    // a Claude Code process started for a conversation
	ConvResume    = "resume"    // … and picked up its earlier session
	ConvInterrupt = "interrupt" // a person interrupted a turn
	ConvStop      = "stop"      // a person ended the process
	ConvBudget    = "budget"    // the conversation reached its spending limit
)

// ConversationEvent records a Claude Code conversation lifecycle event.
func (l *Logger) ConversationEvent(ctx context.Context, source, conversationID, action string, refs Refs, details map[string]any) {
	if l == nil {
		return
	}
	refs.ConversationID = conversationID
	ch := map[string]string{SourceAssistant: ChannelAssistant, SourceClaudeTerminal: ChannelClaudeTerminal, SourceSkillEval: ChannelSkillEval}[source]
	if ch == "" {
		ch = ChannelAssistant
	}
	e := Entry{Kind: KindEvent, Channel: ch, UserID: l.actor.UserID, UserName: l.actor.UserName, Refs: refs, Summary: "conversation " + action,
		Event: &EventInfo{Action: "conversation." + action, TargetType: "assistant_conversation", TargetID: conversationID, Details: details}}
	if action == ConvBudget {
		e.Level = LevelWarn
	}
	l.add(&pending{e: e})
}
