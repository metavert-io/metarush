package improve

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func entry(kind string, at time.Time, f func(e *logbook.Entry)) logbook.Entry {
	e := logbook.Entry{ID: ids.New(logbook.EntryPrefix), At: at, Kind: kind, Level: logbook.LevelInfo, Channel: logbook.ChannelCLI}
	f(&e)
	return e
}

// seededLog is a week of trouble: every kind of signal at least once.
func seededLog(now time.Time) []logbook.Entry {
	h := func(n int) time.Time { return now.Add(-time.Duration(n) * time.Hour) }
	conv := "asc_01h00000000000000000000009"
	return []logbook.Entry{
		entry(logbook.KindOp, h(1), func(e *logbook.Entry) {
			e.Level, e.Op = logbook.LevelError, &logbook.OpInfo{Name: "render.submit"}
			e.Error = &logbook.EntryError{Code: "E_VALIDATION", Message: "revision rev_01h00000000000000000000001 has no clips"}
		}),
		entry(logbook.KindOp, h(2), func(e *logbook.Entry) {
			e.Level, e.Op = logbook.LevelError, &logbook.OpInfo{Name: "render.submit"}
			e.Error = &logbook.EntryError{Code: "E_VALIDATION", Message: "revision rev_01h00000000000000000000002 has no clips"}
		}),
		entry(logbook.KindOp, h(3), func(e *logbook.Entry) {
			e.Level, e.Op, e.Channel = logbook.LevelError, &logbook.OpInfo{Name: "edit.apply", Mutating: true}, logbook.ChannelAssistant
			e.Error = &logbook.EntryError{Code: "E_UNAUTHORIZED", Message: "edit.apply changes shared state", Reason: "assistant_read_only"}
		}),
		entry(logbook.KindOp, h(4), func(e *logbook.Entry) { e.Op, e.DurationMs = &logbook.OpInfo{Name: "footage.moments", OK: true}, 14000 }),
		entry(logbook.KindTool, h(5), func(e *logbook.Entry) {
			e.Level, e.Tool = logbook.LevelError, &logbook.ToolInfo{Program: "ffmpeg", ExitCode: 1, StderrTail: "frame=1\nError opening input file /tmp/a.mov."}
		}),
		entry(logbook.KindJob, h(6), func(e *logbook.Entry) {
			e.Level, e.Job = logbook.LevelError, &logbook.JobInfo{Type: "transcribe", Event: logbook.JobFailed}
			e.Error = &logbook.EntryError{Message: "model file not found"}
		}),
		entry(logbook.KindJob, h(7), func(e *logbook.Entry) { e.Job = &logbook.JobInfo{Type: "proxy", Event: logbook.JobRetry} }),
		entry(logbook.KindJob, h(7), func(e *logbook.Entry) {
			e.Job = &logbook.JobInfo{Type: "render", Event: logbook.JobLeased, WaitedMs: float64((20 * time.Minute).Milliseconds())}
		}),
		entry(logbook.KindAction, h(8), func(e *logbook.Entry) {
			e.Action = &logbook.ActionInfo{ID: "act_a", Title: "Render a review copy of r3", Op: "render.submit", Decision: logbook.DecisionDismissed}
		}),
		// A conversation: a correction, a rephrase, a workaround, a long chain, an ignored card.
		entry(logbook.KindClaude, h(50), func(e *logbook.Entry) {
			e.Refs.ConversationID = conv
			e.Claude = &logbook.ClaudeInfo{Source: logbook.SourceAssistant, Privacy: logbook.PrivacyWorkspace, Turn: 1, Prompt: "cut the opening at the word ready",
				Response: "MetaRush has no built-in way to split a clip at a word, so I'll work around it by making two clips manually.", ToolCount: 22,
				Actions: []logbook.ActionInfo{{ID: "act_b", Title: "Apply the shorter opening", Op: "edit.apply", Decision: logbook.DecisionProposed}},
				Tools:   []logbook.ClaudeTool{{Name: "Bash", Summary: "metarush edit apply --plan p.json", Denied: true}}}
		}),
		entry(logbook.KindClaude, h(49), func(e *logbook.Entry) {
			e.Refs.ConversationID = conv
			e.Claude = &logbook.ClaudeInfo{Source: logbook.SourceAssistant, Privacy: logbook.PrivacyWorkspace, Turn: 2, Prompt: "no, that's not what I meant",
				Response: "Sorry. I can't change the cut directly from here."}
		}),
		entry(logbook.KindClaude, h(49).Add(5*time.Minute), func(e *logbook.Entry) {
			e.Refs.ConversationID = conv
			e.Claude = &logbook.ClaudeInfo{Source: logbook.SourceAssistant, Privacy: logbook.PrivacyWorkspace, Turn: 3, Prompt: "no, that is not what i meant at all",
				Response: "Understood."}
		}),
		// A turn whose words are not recorded is counted as unreadable, not guessed at.
		entry(logbook.KindClaude, h(10), func(e *logbook.Entry) {
			e.Claude = &logbook.ClaudeInfo{Source: logbook.SourceClaudeTerminal, Privacy: logbook.PrivacyOff, Turn: 1}
		}),
		// A dashboard update (an action's outcome) is not a person correcting Claude.
		entry(logbook.KindClaude, h(9), func(e *logbook.Entry) {
			e.Claude = &logbook.ClaudeInfo{Source: logbook.SourceAssistant, Privacy: logbook.PrivacyWorkspace, Note: true, Prompt: "Not now: render. Don't ask again."}
		}),
	}
}

func texts(e *logbook.Entry) (string, string, bool) {
	c := e.Claude
	if c.Privacy == logbook.PrivacyOff {
		return "", "", false
	}
	return c.Prompt, c.Response, true
}

func TestSignalsFromASeededLog(t *testing.T) {
	now := time.Now().UTC()
	sigs, unreadable := Extract(seededLog(now), texts, nil, now)
	byKind := map[string][]Signal{}
	for _, s := range sigs {
		byKind[s.Kind] = append(byKind[s.Kind], s)
	}
	expect := map[string]int{SigOpError: 1, SigDenied: 2, SigSlow: 2, SigToolError: 1, SigJobFailure: 1, SigRetry: 1, SigActionRejected: 1,
		SigActionIgnored: 1, SigCorrection: 1, SigRephrased: 1, SigMissing: 4, SigLongChain: 1}
	for k, n := range expect {
		if len(byKind[k]) != n {
			t.Errorf("%s: %d signals, want %d (%+v)", k, len(byKind[k]), n, byKind[k])
		}
	}
	// The two render.submit errors group together despite different IDs.
	if op := byKind[SigOpError]; len(op) == 1 && op[0].Count != 2 {
		t.Errorf("render.submit errors not grouped: %+v", op[0])
	}
	if sigs[0].Count < 2 {
		t.Errorf("not sorted by frequency: %+v", sigs[0])
	}
	var workaround *Signal
	for i := range byKind[SigMissing] {
		if byKind[SigMissing][i].Key == "workaround" {
			workaround = &byKind[SigMissing][i]
		}
	}
	if workaround == nil || !strings.Contains(workaround.Examples[0].Excerpt, "split a clip at a word") || !strings.HasPrefix(workaround.Examples[0].Link, "/settings/log?entry=log_") {
		t.Fatalf("workaround signal: %+v", workaround)
	}
	if c := byKind[SigCorrection]; len(c) == 1 && c[0].Count != 2 {
		t.Errorf("corrections: %+v (the dashboard note must not count)", c[0])
	}
	if unreadable != 1 {
		t.Errorf("unreadable turns: %d", unreadable)
	}
}

const report = `# Improvement report, October

The log shows people editing around missing word-level cuts.

## Split a clip at a word
- kind: feature-gap
- impact: high
- frequency: 3
- evidence: %s, log_01h00000000000000000000000z
- key: split-at-word

Claude worked around it by making two clips manually.

## Renders of empty versions fail late
- kind: bug
- impact: medium
- frequency: 2

render.submit refuses versions with no clips; the dashboard could say so before queueing.
`

func TestParseReport(t *testing.T) {
	pr, err := ParseReport(strings.Replace(report, "%s", "log_01h00000000000000000000001", 1))
	if err != nil {
		t.Fatal(err)
	}
	if pr.Title != "Improvement report, October" || len(pr.Items) != 2 {
		t.Fatalf("report: %+v", pr)
	}
	a, b := pr.Items[0], pr.Items[1]
	if a.Key != "split-at-word" || a.Kind != KindFeatureGap || a.Impact != ImpactHigh || a.Frequency != 3 || len(a.Evidence) != 2 ||
		!strings.Contains(a.Description, "two clips") || strings.Contains(a.Description, "- kind") {
		t.Fatalf("item a: %+v", a)
	}
	if b.Key != "renders-of-empty-versions-fail-late" || b.Kind != KindBug || Score(b.Frequency, b.Impact) != 4 {
		t.Fatalf("item b: %+v", b)
	}
	if _, err := ParseReport("# Nothing here\n\nJust words."); err == nil {
		t.Fatal("a report without items was accepted")
	}
}

func TestSaveKeepDismissExport(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	evidence := a.Log.Claude(ctx, logbook.Turn{Source: logbook.SourceAssistant, Prompt: "split it", Response: "I'll work around it manually.", StartedAt: time.Now()})
	a.Log.Flush(ctx)
	md := strings.Replace(report, "%s", evidence, 1)
	out, err := ops.CallTyped[SaveOut](ctx, a, "improve.save", SaveIn{Markdown: md}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Created != 2 || out.Report.ItemCount != 2 || len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "not in this workspace's log") {
		t.Fatalf("save: %+v", out)
	}
	split := out.Items[0]
	if split.State != StateNew || split.Score != 9 || len(split.Evidence) != 1 || split.Evidence[0] != evidence {
		t.Fatalf("item: %+v", split)
	}
	// Dismiss one; saving the report again keeps it dismissed and counts the sighting.
	if _, err := ops.CallTyped[DecideOut](ctx, a, "improve.dismiss", DecideIn{IDs: []string{split.ID}}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	again, err := ops.CallTyped[SaveOut](ctx, a, "improve.save", SaveIn{Markdown: md}, ops.Caller{})
	if err != nil || again.Created != 0 || again.Updated != 2 {
		t.Fatalf("second save: %+v %v", again, err)
	}
	if again.Items[0].State != StateDismissed || again.Items[0].Seen != 2 {
		t.Fatalf("dismissed item after a new report: %+v", again.Items[0])
	}
	list, err := ops.CallTyped[ListOut](ctx, a, "improve.list", ListIn{}, ops.Caller{})
	if err != nil || len(list.Items) != 1 || list.Counts.Dismissed != 1 || list.Counts.New != 1 || len(list.Reports) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	if _, err := ops.CallTyped[DecideOut](ctx, a, "improve.keep", DecideIn{IDs: []string{split.ID}}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	show, err := ops.CallTyped[ShowOut](ctx, a, "improve.show", ShowIn{ID: split.ID}, ops.Caller{})
	if err != nil || show.Item.State != StateKept || len(show.Evidence) != 1 {
		t.Fatalf("show: %+v %v", show, err)
	}
	exp, err := ops.CallTyped[ExportOut](ctx, a, "improve.export", ExportIn{}, ops.Caller{})
	if err != nil || exp.Count != 2 || !strings.Contains(exp.Markdown, "## Split a clip at a word") || !strings.HasSuffix(exp.Filename, ".md") {
		t.Fatalf("export: %+v %v", exp, err)
	}
	// The exported Markdown is itself a valid report.
	if pr, err := ParseReport(exp.Markdown); err != nil || len(pr.Items) != 2 {
		t.Fatalf("export does not parse back: %v", err)
	}
}

func TestSignalsOpAndAssistantChannel(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	a.Log.Claude(ctx, logbook.Turn{Source: logbook.SourceAssistant, Prompt: "make a gif", Response: "MetaRush doesn't support GIF export, so I'll work around it.", StartedAt: time.Now()})
	a.Log.Flush(ctx)
	out, err := ops.CallTyped[SignalsOut](ctx, a, "improve.signals", SignalsIn{}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range out.Signals {
		if s.Kind == SigMissing && strings.Contains(s.Examples[0].Excerpt, "GIF") {
			found = true
		}
	}
	if !found || out.Scanned < 1 {
		t.Fatalf("signals: %+v", out)
	}
	as := *a
	as.Actor.Via = models.ViaAssistant
	if _, err := ops.CallTyped[SignalsOut](ctx, &as, "improve.signals", SignalsIn{}, ops.Caller{}); apperr.From(err).Code != apperr.Unauthorized {
		t.Fatalf("assistant ran improve.signals: %v", err)
	}
	// An exported log can be analyzed instead of the workspace's.
	now := time.Now().UTC()
	fromFile, err := ops.CallTyped[SignalsOut](ctx, a, "improve.signals", SignalsIn{Entries: seededLog(now)}, ops.Caller{})
	if err != nil || fromFile.Scanned != len(seededLog(now)) {
		t.Fatalf("from entries: %+v %v", fromFile, err)
	}
}
