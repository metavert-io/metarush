package improve

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// fakeReporter is a Claude Code that reads what MetaRush prepared in its
// folder and writes report.md there, citing the first entry it was given.
const fakeReporter = `cat > /dev/null
[ -f signals.json ] && [ -f .claude/skills/metarush-improve/SKILL.md ] || { echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"nothing prepared"}'; exit 1; }
id=$(ls entries | head -1 | sed 's/\.json$//')
cat > report.md <<EOF
# This week in MetaRush

Claude Code worked around a missing export once.

## Export animated GIFs
- kind: feature-gap
- impact: medium
- frequency: 1
- evidence: $id
- key: gif-export

Someone asked for a GIF and Claude had to work around it.
EOF
echo '{"type":"result","subtype":"success","is_error":false,"num_turns":4,"result":"Wrote report.md","total_cost_usd":0.0123}'`

// The whole pipeline: signals, the entries behind them, a headless Claude Code
// that can only read its folder and write report.md, then the ideas saved.
func TestReportRunsClaudeHeadlessAndSaves(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	a.Log.Claude(ctx, logbook.Turn{Source: logbook.SourceAssistant, Prompt: "make a gif of the intro", Response: "MetaRush doesn't support GIF export, so I'll work around it.", StartedAt: time.Now()})
	a.Log.Flush(ctx)
	a.Cfg.Tools.Claude = fakeclaude.Write(t, fakeclaude.Options{Body: fakeReporter})
	a.Tools = tools.New(a.Cfg.Tools)

	dry, err := ops.CallTyped[ReportOut](ctx, a, "improve.report", ReportIn{DryRun: true}, ops.Caller{})
	if err != nil || dry.Ran || dry.Signals < 1 || dry.Entries < 1 || !strings.Contains(dry.Message, "nothing was sent") {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	out, err := ops.CallTyped[ReportOut](ctx, a, "improve.report", ReportIn{}, ops.Caller{})
	if err != nil {
		t.Fatalf("report: %v (%+v)", err, apperr.From(err))
	}
	if !out.Ran || out.Created != 1 || out.CostUSD != 0.0123 || out.Saved == nil || out.Saved.Title != "This week in MetaRush" || !strings.Contains(out.Message, "1 new") {
		t.Fatalf("report: %+v", out)
	}
	// Claude Code ran without a shell, reading only its folder and writing only report.md.
	dir, _ := filepath.EvalSymlinks(out.Dir)
	argv := fakeclaude.Argv(t, out.Dir)
	flag := func(name string) string {
		i := slices.Index(argv, name)
		if i < 0 || i+1 >= len(argv) {
			t.Fatalf("no %s in %v", name, argv)
		}
		return argv[i+1]
	}
	if got := flag("--allowedTools"); got != "Skill,Read(/"+dir+"/**),Write(/"+dir+"/report.md)" {
		t.Fatalf("allowed: %s", got)
	}
	if got := flag("--disallowedTools"); !strings.Contains(got, "Bash") || !strings.Contains(got, "WebFetch") {
		t.Fatalf("denied: %s", got)
	}
	if flag("--tools") != "Read,Write,Skill" || flag("--permission-mode") != "dontAsk" || flag("--max-budget-usd") != "2.00" || flag("--setting-sources") != "project,local" || !slices.Contains(argv, "-p") {
		t.Fatalf("argv: %v", argv)
	}
	if b, err := os.ReadFile(filepath.Join(out.Dir, ".claude", "skills", "metarush-improve", "SKILL.md")); err != nil || strings.Contains(string(b), "allowed-tools:") {
		t.Fatalf("skill in the run folder: %v", err)
	}
	list, err := ops.CallTyped[ListOut](ctx, a, "improve.list", ListIn{}, ops.Caller{})
	if err != nil || len(list.Items) != 1 || list.Items[0].Title != "Export animated GIFs" || len(list.Items[0].Evidence) != 1 {
		t.Fatalf("ideas: %+v %v", list, err)
	}
}

// With nothing in the log there is no report and no Claude Code run.
func TestReportWithNothingToSay(t *testing.T) {
	a := testapp.New(t)
	a.Cfg.Tools.Claude = filepath.Join(t.TempDir(), "never-run")
	a.Tools = tools.New(a.Cfg.Tools)
	out, err := MakeReport(context.Background(), a, &ReportIn{})
	if err != nil || out.Ran || !strings.Contains(out.Message, "Nothing stood out") {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestWeeklyScheduleIsDueOncePerSlot(t *testing.T) {
	loc := time.FixedZone("test", 2*3600)
	mon10 := time.Date(2026, 10, 19, 10, 0, 0, 0, loc) // a Monday
	slot := time.Date(2026, 10, 19, 9, 0, 0, 0, loc)
	none := func(time.Time) int { return 0 }
	at := func(t time.Time) *time.Time { return &t }
	cfg := config.Improve{}
	if due(cfg, mon10, scheduleState{}, none) {
		t.Fatal("off by default")
	}
	cfg.Weekly = true
	if !due(cfg, mon10, scheduleState{}, none) {
		t.Fatal("never made: due")
	}
	if due(cfg, mon10, scheduleState{LastSuccess: at(slot.Add(time.Minute))}, none) {
		t.Fatal("made this week already")
	}
	if !due(cfg, mon10, scheduleState{LastSuccess: at(slot.Add(-7 * 24 * time.Hour))}, none) {
		t.Fatal("made last week: due")
	}
	if due(cfg, mon10, scheduleState{}, func(time.Time) int { return 1 }) {
		t.Fatal("another machine made this week's report")
	}
	if due(cfg, mon10, scheduleState{LastAttempt: at(mon10.Add(-time.Hour))}, none) {
		t.Fatal("a failed try an hour ago waits")
	}
	if !due(cfg, mon10.Add(6*time.Hour), scheduleState{LastAttempt: at(mon10.Add(-time.Hour))}, none) {
		t.Fatal("a failed try is retried after six hours")
	}
	// Sunday evening: this week's slot is still last Monday's.
	sun := time.Date(2026, 10, 25, 20, 0, 0, 0, loc)
	if got := slotBefore(sun, time.Monday, 9); !got.Equal(slot) {
		t.Fatalf("slot before Sunday: %v", got)
	}
	if got := slotBefore(time.Date(2026, 10, 19, 8, 0, 0, 0, loc), time.Monday, 9); !got.Equal(slot.AddDate(0, 0, -7)) {
		t.Fatalf("slot before Monday 8:00: %v", got)
	}
	h := 17
	cfg.Day, cfg.Hour = "fri", &h
	if cfg.ReportDay() != time.Friday || cfg.ReportHour() != 17 {
		t.Fatal(cfg.ReportDay(), cfg.ReportHour())
	}
}

// The schedule is a setting in config.yaml that only a person turns on; the
// daemon's tick then makes the report once and not again that week.
func TestScheduleSettingAndTick(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	s, err := ops.CallTyped[ScheduleOut](ctx, a, "improve.schedule", ScheduleIn{}, ops.Caller{})
	if err != nil || s.Weekly || s.Day != "monday" || s.Hour != 9 || s.BudgetUSD != 2 || s.NextRun != nil {
		t.Fatalf("default: %+v %v", s, err)
	}
	on := true
	if _, err := ops.CallTyped[ScheduleOut](ctx, a, "improve.schedule.set", ScheduleSetIn{Weekly: &on}, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("an agent turned the weekly report on: %v", err)
	}
	h := 0
	s, err = ops.CallTyped[ScheduleOut](ctx, a, "improve.schedule.set", ScheduleSetIn{Weekly: &on, Day: "monday", Hour: &h}, ops.Caller{Human: true})
	if err != nil || !s.Weekly || s.NextRun == nil || s.Hour != 0 {
		t.Fatalf("on: %+v %v", s, err)
	}
	cfg, err := config.Load(a.Cfg.Home())
	if err != nil || !cfg.Improve.Weekly {
		t.Fatalf("config.yaml: %+v %v", cfg.Improve, err)
	}
	a.Log.Claude(ctx, logbook.Turn{Source: logbook.SourceAssistant, Prompt: "make a gif", Response: "MetaRush doesn't support GIF export, so I'll work around it.", StartedAt: time.Now()})
	a.Log.Flush(ctx)
	a.Cfg.Tools.Claude = fakeclaude.Write(t, fakeclaude.Options{Body: fakeReporter})
	a.Tools = tools.New(a.Cfg.Tools)
	var said []string
	logf := func(f string, x ...any) { said = append(said, f) }
	// A Monday after midnight: this week's slot has passed.
	now := time.Now()
	for now.Weekday() != time.Monday {
		now = now.AddDate(0, 0, -1)
	}
	now = time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, time.Local)
	if !RunIfDue(ctx, a, now, logf) {
		t.Fatalf("the report was due: %v", said)
	}
	if RunIfDue(ctx, a, now.Add(time.Hour), logf) {
		t.Fatal("made twice in one week")
	}
	st := loadState(a)
	if st.LastSuccess == nil || !strings.Contains(st.LastResult, "1 new") {
		t.Fatalf("state: %+v (said %v)", st, said)
	}
	off := false
	if _, err := ops.CallTyped[ScheduleOut](ctx, a, "improve.schedule.set", ScheduleSetIn{Weekly: &off}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if RunIfDue(ctx, a, now.AddDate(0, 0, 7), logf) {
		t.Fatal("ran while turned off")
	}
}

// The weekly schedule goes by the database server's clock, like the reports'
// stamps (D360): a report saved on a computer ten minutes fast is stamped
// with the server's time, the tick on a computer ten minutes slow tells the
// slot by the server's time in its own time zone, and it counts that report
// as this week's.
func TestWeeklyScheduleGoesByTheServersClock(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	off := func(got, want time.Time) bool { d := got.Sub(want); return d < -time.Minute || d > time.Minute }
	restore := testutil.SkewClock(t, 10*time.Minute)
	out, err := Save(ctx, a, strings.Replace(report, "%s", "log_01h00000000000000000000001", 1))
	if err != nil {
		t.Fatal(err)
	}
	server, err := a.DB.ServerNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if off(out.Report.CreatedAt, server) {
		t.Fatalf("a report saved on a computer ten minutes fast is stamped %v; the server's time is %v", out.Report.CreatedAt, server)
	}
	restore()

	restore = testutil.SkewClock(t, -10*time.Minute)
	defer restore()
	now := tickNow(ctx, a)
	if off(now, server) || now.Location() != time.Local {
		t.Fatalf("the tick on a computer ten minutes slow goes by %v; the server's time is %v", now, server)
	}
	// This week's slot is the hour the report was made in: it was made since.
	made := out.Report.CreatedAt.Local()
	h := made.Hour()
	cfg := config.Improve{Weekly: true, Day: made.Weekday().String(), Hour: &h}
	if due(cfg, now, scheduleState{}, func(slot time.Time) int { return reportsSince(ctx, a, slot) }) {
		t.Fatal("another computer's report this week wasn't counted")
	}
}
