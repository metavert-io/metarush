package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/tools"
)

func TestMain(m *testing.M) {
	fakeclaude.MaybeRun() // this binary doubles as the fake `claude`
	os.Exit(m.Run())
}

func testApp(t *testing.T, claude string) *app.App {
	t.Helper()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: ids.New(ids.Workspace), Name: "Assistant test"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "test-machine"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "Tester", Email: "tester@example.com"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Tools.Claude = claude
	// Safe steps wait for a click in these tests (their cards are what they
	// check); TestSafeStepsRunByThemselves turns the setting on (D141).
	off := false
	cfg.Assistant.SafeSteps = &off
	// News is told at once in these tests (their note turns are what they
	// check); TestNewsWaitsForTheNextMessage holds it (D181).
	cfg.Assistant.HoldNews = &off
	a, err := app.OpenWith(context.Background(), cfg, "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

// withFake points the app at a fake claude and returns a manager for it.
func withFake(t *testing.T, a *app.App, o fakeclaude.Options) *Manager {
	t.Helper()
	a.Cfg.Tools.Claude = fakeclaude.Write(t, o)
	a.Tools = tools.New(a.Cfg.Tools)
	m := New(a, "/usr/local/bin/metarush-test")
	m.grace = 400 * time.Millisecond
	m.workEvery, m.tellAfter = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(m.Close)
	return m
}

// fixture creates a project with one available asset and a first revision
// (clips hook 1–6 s and broll 10–13 s).
func fixture(t *testing.T, a *app.App) (projectID, assetID, headID string) {
	t.Helper()
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Assistant film"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "interview.mov",
		SHA256: strings.Repeat("ab", 32), Size: 1000, MIME: "video/quicktime", StorageKey: "k", State: models.AssetAvailable,
		Media:    models.MediaInfo{Container: "mov", Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	plan := fmt.Sprintf(`{"projectId":%q,"instruction":"first cut","spec":{"schemaVersion":1,"title":"Cut","output":{"preset":"vertical-1080p","fps":30},
	  "clips":[{"id":"hook","assetId":%q,"in":1,"out":6},{"id":"broll","assetId":%q,"in":10,"out":13}]}}`, p.Project.ID, as.ID, as.ID)
	out, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan)}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	return p.Project.ID, as.ID, out.Revision.ID
}

func code(err error) string {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func reason(err error) any {
	var ae *apperr.Error
	if errors.As(err, &ae) {
		if d, ok := ae.Details.(map[string]any); ok {
			return d["reason"]
		}
	}
	return nil
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// waitIdle waits until the conversation has no running turn and nothing queued.
func waitIdle(t *testing.T, m *Manager, id string) *Conversation {
	t.Helper()
	var cv *Conversation
	eventually(t, "the conversation to finish", func() bool {
		c, err := m.Get(context.Background(), id, true, true)
		if err != nil {
			t.Fatal(err)
		}
		cv = c
		if c.State != ConvIdle || len(c.Queue) > 0 || len(c.Turns) == 0 {
			return false
		}
		return TurnTerminal(c.Turns[len(c.Turns)-1].State)
	})
	return cv
}

func running(m *Manager, id string) bool {
	c, err := m.Get(context.Background(), id, false, false)
	return err == nil && c.State == ConvWorking
}

func live(m *Manager, id string) bool {
	c, err := m.Get(context.Background(), id, false, false)
	return err == nil && c.Live
}

func has(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func after(xs []string, flag string) string {
	for i, v := range xs {
		if v == flag && i+1 < len(xs) {
			return xs[i+1]
		}
	}
	return ""
}

func tool(name string, input map[string]any, result string) fakeclaude.Step {
	return fakeclaude.Step{Tool: &fakeclaude.ToolStep{Name: name, Input: input, Result: result}}
}

func text(s string) fakeclaude.Step { return fakeclaude.Step{Text: s} }

// lockedDown are the flags every spawn (and every resume) must carry.
func assertLockedDown(t *testing.T, argv []string, m *Manager, convID string) {
	t.Helper()
	joined := strings.Join(argv, "\n")
	for _, want := range []string{"-p", "--input-format", "--output-format", "stream-json", "--verbose", "--include-partial-messages",
		"--setting-sources", "project,local", "--strict-mcp-config", "--permission-mode", "dontAsk", "--tools", "--max-turns",
		"--append-system-prompt", "--allowedTools", "--disallowedTools", "--autocompact"} {
		if !has(argv, want) {
			t.Errorf("argv lacks %q", want)
		}
	}
	for _, bad := range []string{"--no-session-persistence", "--dangerously-skip-permissions", "bypassPermissions", "--permission-prompt-tool"} {
		if has(argv, bad) {
			t.Errorf("argv has %q", bad)
		}
	}
	allow := after(argv, "--allowedTools")
	for _, want := range []string{"Bash(metarush project show *)", "Bash(metarush edit check *)", "Bash(metarush schema show *)", "/scratch/**)", "/cache/**)",
		"Edit(/" + realPath(filepath.Join(m.convDir(convID), "proposals")) + "/**)"} {
		if !strings.Contains(allow, want) {
			t.Errorf("allowlist lacks %q: %s", want, allow)
		}
	}
	for _, bad := range []string{"metarush edit apply", "metarush render submit", "Bash(metarush schema *)", "metarush assistant", ",Read,", ",Bash,"} {
		if strings.Contains(allow, bad) {
			t.Errorf("allowlist is too broad (%q): %s", bad, allow)
		}
	}
	deny := after(argv, "--disallowedTools")
	for _, want := range []string{"Bash(metarush edit apply *)", "Bash(metarush assistant *)", "WebFetch"} {
		if !strings.Contains(deny, want) {
			t.Errorf("denylist lacks %q", want)
		}
	}
	if strings.Contains(joined, "Message:") {
		t.Error("the person's message must travel on stdin, not in argv")
	}
}

func TestConversationRunsManyTurnsOnOneProcess(t *testing.T) {
	t.Setenv("SOME_UNRELATED_SECRET", "do-not-pass")
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	plan := fmt.Sprintf(`{"baseRevisionId":%q,"instruction":"tighten the b-roll","rationale":"The b-roll holds too long.","origin":"forged:by-model","ops":[{"op":"updateClip","id":"broll","set":{"out":11.8}}]}`, head)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "What's in this project", CostUSD: 0.02, Steps: []fakeclaude.Step{
			tool("Skill", map[string]any{"skill": "video-project"}, "Launching skill: video-project"),
			tool("Bash", map[string]any{"command": "metarush project show " + projectID + " --json"}, `{"ok":true,"data":{}}`),
			text("Two interviews and some b-roll.\n\n[title: Project footage overview]"),
		}},
		{Match: "Tighten the b-roll", CostUSD: 0.03, Steps: []fakeclaude.Step{
			{Write: &fakeclaude.File{Name: "tighten.plan.json", Content: plan}},
			tool("Bash", map[string]any{"command": "metarush edit check --plan {{proposals}}/tighten.plan.json --json"}, `{"ok":true,"data":{"valid":true,"warnings":[{"code":"w","message":"x"}],"errors":[]}}`),
			text("I propose trimming the b-roll to 1.8 s."),
		}},
	}}})
	ctx := context.Background()
	st := m.Status(ctx, true)
	if !st.Available || st.ClaudeVersion != "9.9.9" || st.Billing != "plan" || st.BudgetUSD != 0 || st.TokenLimitM != config.DefaultAssistantTokenLimitM || st.MaxSteps != 60 {
		t.Fatalf("status: %+v", st)
	}
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "What's in this project?", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	if out.Turn != 1 || out.Queued || out.Conversation.Title != "What's in this project" {
		t.Fatalf("ask: %+v", out)
	}
	id := out.Conversation.ID
	waitIdle(t, m, id)
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "Tighten the b-roll in the current cut", ProjectID: projectID, ClipID: "broll"}}); err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, id)

	spawns := fakeclaude.Spawns(t, m.convDir(id))
	if len(spawns) != 1 {
		t.Fatalf("two messages took %d processes, want 1", len(spawns))
	}
	assertLockedDown(t, spawns[0], m, id)
	if after(spawns[0], "--session-id") != cv.ClaudeSessionID || cv.ClaudeSessionID == "" || has(spawns[0], "--resume") {
		t.Fatalf("first spawn must create the session: %q", spawns[0])
	}
	if len(cv.Turns) != 2 || cv.Turns[0].State != TurnSucceeded || cv.Turns[1].State != TurnSucceeded {
		t.Fatalf("turns: %+v", cv.Turns)
	}
	if fmt.Sprintf("%.2f %.2f %.2f", cv.Turns[0].CostUSD, cv.Turns[1].CostUSD, cv.CostUSD) != "0.02 0.03 0.05" {
		t.Fatalf("costs: turn %.3f %.3f total %.3f", cv.Turns[0].CostUSD, cv.Turns[1].CostUSD, cv.CostUSD)
	}
	if cv.Title != "Project footage overview" || cv.TitleSource != "claude" {
		t.Fatalf("title: %q (%s)", cv.Title, cv.TitleSource)
	}
	received := fakeclaude.Received(t, m.convDir(id))
	if len(received) != 2 || !strings.Contains(received[0], "/t1") || !strings.Contains(received[1], "/t2") || !strings.Contains(received[1], `Selected clip: \"broll\"`) {
		t.Fatalf("stdin messages: %q", received)
	}
	var labels, texts []string
	for _, e := range cv.Events {
		switch e.Kind {
		case "tool":
			labels = append(labels, e.Tool.Label)
		case "text":
			texts = append(texts, e.Text)
		}
	}
	wantLabels := []string{"Looked at your projects", "Opened “Assistant film”", "Drafted the change “tighten”", "Checked the change “tighten”"}
	if strings.Join(labels, "|") != strings.Join(wantLabels, "|") {
		t.Fatalf("labels:\n got %q\nwant %q", labels, wantLabels)
	}
	if len(texts) != 2 || strings.Contains(strings.Join(texts, ""), "[title:") {
		t.Fatalf("texts: %q", texts)
	}
	outcomes := map[string]string{}
	for _, e := range cv.Events {
		if e.Kind == "tool_result" {
			outcomes[e.Tool.ID] = e.Tool.Outcome
		}
	}
	found := false
	for _, o := range outcomes {
		found = found || o == "looks good, 1 note"
	}
	if !found {
		t.Fatalf("edit check outcome missing: %v", outcomes)
	}

	// Clean view hides the machinery; raw shows it.
	clean, _ := m.Get(ctx, id, true, false)
	if clean.Turns[1].Sent != "" || clean.SystemPrompt != "" {
		t.Fatal("the clean view leaks the sent message or the rules")
	}
	for _, e := range clean.Events {
		if e.Tool != nil && (e.Tool.Input != "" || e.Tool.Output != "") {
			t.Fatal("the clean view leaks raw tool I/O")
		}
	}
	raw, _ := m.Get(ctx, id, true, true)
	if !strings.Contains(raw.Turns[1].Sent, "Proposals folder for this message:") || !strings.Contains(raw.SystemPrompt, "MetaRush dashboard assistant") {
		t.Fatalf("raw view lacks the sent message or the rules")
	}

	// Proposals: per turn, origin set by the server, checked against the head.
	props, err := m.Proposals(ctx, id, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(props.Turns) != 1 || props.Turns[0].Turn != 2 || len(props.Turns[0].Plans) != 1 {
		t.Fatalf("proposals: %+v", props)
	}
	pp := props.Turns[0].Plans[0]
	if pp.Ref != "t2/tighten.plan.json" || pp.Origin != Origin(id, "t2/tighten.plan.json") || pp.Check == nil || !pp.Check.Valid || pp.Check.ConfirmToken != "" {
		t.Fatalf("plan: %+v %+v", pp, pp.Check)
	}

	// Persisted in MongoDB.
	var doc Conversation
	if err := a.DB.GetByID(ctx, store.CAssistantConversations, a.WorkspaceID(), id, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Turns) != 2 || len(doc.Events) == 0 || doc.InstallationName != "test-machine" || doc.Title != "Project footage overview" {
		t.Fatalf("stored: turns=%d events=%d machine=%q title=%q", len(doc.Turns), len(doc.Events), doc.InstallationName, doc.Title)
	}
	env, _ := os.ReadFile(filepath.Join(m.convDir(id), "env.txt"))
	if strings.Contains(string(env), "do-not-pass") || !strings.Contains(string(env), "PATH="+m.binDir()+":") {
		t.Fatal("Claude Code's environment is not the clean one")
	}
	if _, err := os.Stat(filepath.Join(m.workspaceDir(), ".claude", "settings.json")); err == nil {
		t.Fatal("the workspace must not carry a settings file (permissions come from flags)")
	}
}

func TestQueueAndPartialText(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "slow one", Steps: []fakeclaude.Step{
			{Emit: `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}},"parent_tool_use_id":null}`},
			{Emit: `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}},"parent_tool_use_id":null}`},
			{Emit: `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"wor"}},"parent_tool_use_id":null}`},
			{SleepMs: 5000}, // long enough to see the partial text and queue a message on a busy machine
			text("Hello world"),
		}},
		{Match: "second please", Steps: []fakeclaude.Step{text("Second answer")}},
	}}})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "slow one"}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	eventually(t, "partial text", func() bool {
		st, err := m.Follow(ctx, id, 0, -1, -1, false)
		return err == nil && st.Partial != nil && st.Partial.Text == "Hello wor" && st.Partial.Phase == "text"
	})
	q, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "second please"}})
	if err != nil || !q.Queued || q.QueueID == "" || len(q.Conversation.Queue) != 1 {
		t.Fatalf("queued while working: %+v %v", q, err)
	}
	cv := waitIdle(t, m, id)
	if len(cv.Turns) != 2 || cv.Turns[1].Prompt != "second please" || cv.Turns[1].State != TurnSucceeded {
		t.Fatalf("turns: %+v", cv.Turns)
	}
	if n := len(fakeclaude.Spawns(t, m.convDir(id))); n != 1 {
		t.Fatalf("queued message used %d processes", n)
	}
	st, _ := m.Follow(ctx, id, 0, -1, -1, false)
	if st.Partial == nil || st.Partial.Text != "" {
		t.Fatalf("partial text not cleared after the reply: %+v", st.Partial)
	}
}

func TestInterruptKeepsTheProcessAndStopEndsIt(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "long task", Steps: []fakeclaude.Step{{SleepMs: 15000}, text("never")}},
		{Match: "after interrupt", Steps: []fakeclaude.Step{text("still here")}},
		{Match: "resumed please", Steps: []fakeclaude.Step{text("resumed fine")}},
	}}})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "long task"}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	eventually(t, "the turn to run", func() bool { return running(m, id) })
	if q, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "queued msg"}}); err != nil || !q.Queued {
		t.Fatalf("queue: %+v %v", q, err)
	}
	io, err := m.Interrupt(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(io.Requeued) != 1 || io.Requeued[0].Prompt != "queued msg" {
		t.Fatalf("queued messages are handed back: %+v", io.Requeued)
	}
	cv := waitIdle(t, m, id)
	if cv.Turns[0].State != TurnInterrupted || len(cv.Turns) != 1 || !cv.Live {
		t.Fatalf("after interrupt: turns=%+v live=%v", cv.Turns, cv.Live)
	}
	if _, err := m.Interrupt(ctx, id); code(err) != apperr.Conflict {
		t.Fatalf("interrupting an idle conversation: %v", err)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "after interrupt"}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, id)
	if cv.Turns[1].State != TurnSucceeded || len(fakeclaude.Spawns(t, m.convDir(id))) != 1 {
		t.Fatalf("the conversation continues on the same process: %+v", cv.Turns)
	}

	// Stop ends the process; the next message resumes the session.
	if _, err := m.Stop(ctx, id); err != nil {
		t.Fatal(err)
	}
	if live(m, id) {
		t.Fatal("stop left the process running")
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "resumed please"}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, id)
	spawns := fakeclaude.Spawns(t, m.convDir(id))
	if len(spawns) != 2 || after(spawns[1], "--resume") != cv.ClaudeSessionID || has(spawns[1], "--session-id") {
		t.Fatalf("resume spawn: %d %q", len(spawns), spawns[len(spawns)-1])
	}
	assertLockedDown(t, spawns[1], m, id)
	last := cv.Turns[len(cv.Turns)-1]
	if last.State != TurnSucceeded || !last.Resumed {
		t.Fatalf("resumed turn: %+v", last)
	}
}

func TestStopDuringATurn(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{{Match: "long", Steps: []fakeclaude.Step{{SleepMs: 15000}}}}}})
	ctx := context.Background()
	out, _ := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "long job"}})
	eventually(t, "the turn to run", func() bool { return running(m, out.Conversation.ID) })
	if _, err := m.Stop(ctx, out.Conversation.ID); err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	if cv.Turns[0].State != TurnStopped || cv.Live {
		t.Fatalf("stopped during a turn: %+v live=%v", cv.Turns[0], cv.Live)
	}
}

func TestRespawnAfterTheProcessDies(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "first", ExitAfter: true, Steps: []fakeclaude.Step{text("one")}},
		{Match: "second", Steps: []fakeclaude.Step{text("two")}},
		{Match: "crash now", Steps: []fakeclaude.Step{text("about to"), {Die: true}}},
	}}})
	ctx := context.Background()
	out, _ := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "first"}})
	id := out.Conversation.ID
	waitIdle(t, m, id)
	eventually(t, "the process to exit", func() bool { return !live(m, id) })
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "second"}}); err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, id)
	spawns := fakeclaude.Spawns(t, m.convDir(id))
	if len(spawns) != 2 || after(spawns[1], "--resume") != cv.ClaudeSessionID || cv.Turns[1].State != TurnSucceeded {
		t.Fatalf("respawn: %d spawns, turns %+v", len(spawns), cv.Turns)
	}
	assertLockedDown(t, spawns[1], m, id)
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "crash now"}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, id)
	if last := cv.Turns[2]; last.State != TurnFailed || !strings.Contains(last.Error, "fake claude crashed mid-turn") {
		t.Fatalf("crash: %+v", last)
	}
}

func TestBudgetCapStopsTheConversation(t *testing.T) {
	a := testApp(t, "")
	a.Cfg.Assistant.MaxBudgetUSD = 0.05
	m := withFake(t, a, fakeclaude.Options{AuthMethod: "api_key", Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "expensive", CostUSD: 0.06, Steps: []fakeclaude.Step{text("a lot of work")}},
	}, Default: &fakeclaude.Turn{CostUSD: 0.01, Steps: []fakeclaude.Step{text("fine")}}}})
	ctx := context.Background()
	if st := m.Status(ctx, true); st.Billing != "api" || st.BudgetUSD != 0.05 || !strings.Contains(st.BillingLabel, "API key") {
		t.Fatalf("api billing: %+v", st)
	}
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "expensive job"}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	if cv.Turns[0].State != TurnBudget || !strings.Contains(cv.Turns[0].Error, "$0.05") {
		t.Fatalf("budget turn: %+v", cv.Turns[0])
	}
	if after(fakeclaude.Spawns(t, m.convDir(id))[0], "--max-budget-usd") != "0.05" {
		t.Fatal("the cap is not passed to Claude Code")
	}
	_, err = m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "more"}})
	if code(err) != apperr.Conflict || reason(err) != "budget" {
		t.Fatalf("over budget: %v", err)
	}
	raise := 1.0
	if _, err := m.Configure(ctx, ConversationConfigureIn{ID: id, BudgetUSD: &raise}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "more"}}); err != nil {
		t.Fatalf("after raising the budget: %v", err)
	}
	cv = waitIdle(t, m, id)
	spawns := fakeclaude.Spawns(t, m.convDir(id))
	if cv.Turns[1].State != TurnSucceeded || len(spawns) < 2 || after(spawns[len(spawns)-1], "--max-budget-usd") != "0.94" {
		t.Fatalf("respawned with the remaining budget: %+v %q", cv.Turns, spawns[len(spawns)-1])
	}
}

func TestIdleProcessesAreReaped(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{})
	m.idle = 150 * time.Millisecond
	ctx := context.Background()
	out, _ := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "hi"}})
	id := out.Conversation.ID
	waitIdle(t, m, id)
	eventually(t, "the idle process to be reaped", func() bool { return !live(m, id) && m.Status(ctx, false).Live == 0 })
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "again"}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, id)
	if spawns := fakeclaude.Spawns(t, m.convDir(id)); len(spawns) != 2 || !has(spawns[1], "--resume") {
		t.Fatalf("after reaping, the next message resumes: %d", len(spawns))
	}
}

func TestTurnTimeout(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{{SleepMs: 20000}}}}})
	m.timeout = 300 * time.Millisecond
	out, _ := m.Ask(context.Background(), AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "slow"}})
	cv := waitIdle(t, m, out.Conversation.ID)
	if cv.Turns[0].State != TurnTimedOut || !strings.Contains(cv.Turns[0].Error, "the time limit for one message") {
		t.Fatalf("timeout: %+v", cv.Turns[0])
	}
}

func TestHistoryLegacyRunsOtherMachinesAndRecovery(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{})
	ctx := context.Background()
	// A single request from the previous version, on disk.
	runID := ids.New(RunPrefix)
	dir := m.legacyDir(runID)
	_ = os.MkdirAll(filepath.Join(dir, "proposals"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "proposals", "old.plan.json"), []byte(`{"ops":[]}`), 0o600)
	created := time.Now().Add(-time.Hour).UTC()
	legacy := map[string]any{"id": runID, "state": "succeeded", "prompt": "Explain the cut please", "context": map[string]any{"projectName": "Old film"},
		"user": "Tester", "budgetUsd": 1, "createdAt": created, "endedAt": created.Add(time.Minute), "proposals": 1,
		"result": map[string]any{"subtype": "success", "costUsd": 0.4, "durationMs": 60000, "turns": 3},
		"events": []map[string]any{{"seq": 1, "kind": "state", "state": "running"}, {"seq": 2, "kind": "text", "text": "The cut opens on the hook."},
			{"seq": 3, "kind": "tool", "tool": map[string]any{"id": "t1", "name": "Bash", "summary": "metarush revision show x"}},
			{"seq": 4, "kind": "result", "result": map[string]any{"subtype": "success", "costUsd": 0.4, "durationMs": 60000, "turns": 3}},
			{"seq": 5, "kind": "proposals", "count": 1}}}
	b, _ := json.Marshal(legacy)
	_ = os.WriteFile(filepath.Join(dir, "run.json"), b, 0o600)

	// Another machine's conversation, and one this machine left "working" when it stopped.
	other := Conversation{ID: ids.New(ConvPrefix), WorkspaceID: a.WorkspaceID(), Title: "On the laptop", TitleSource: "user", UserID: a.Cfg.User.ID,
		InstallationID: ids.New(ids.Installation), InstallationName: "edit-laptop", State: ConvIdle, BudgetUSD: 2, TurnCount: 1,
		Turns: []Turn{{N: 1, Prompt: "hello", State: TurnSucceeded}}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	stuck := Conversation{ID: ids.New(ConvPrefix), WorkspaceID: a.WorkspaceID(), Title: "Stuck", TitleSource: "prompt", UserID: a.Cfg.User.ID,
		InstallationID: a.Cfg.Installation.ID, InstallationName: "test-machine", State: ConvWorking, HolderPID: 999999, BudgetUSD: 2, TurnCount: 1,
		Turns:  []Turn{{N: 1, Prompt: "work", State: TurnRunning, StartedAt: time.Now().UTC()}},
		Events: []AssistantEvent{{Seq: 1, Turn: 1, Kind: "turn", Text: "work"}}, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	for _, d := range []Conversation{other, stuck} {
		if err := a.DB.Insert(ctx, store.CAssistantConversations, d); err != nil {
			t.Fatal(err)
		}
	}
	if pidAlive(999999) {
		t.Skip("pid 999999 is alive on this machine")
	}

	list, err := m.List(ctx, ConversationListIn{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Conversation{}
	for _, c := range list.Conversations {
		byID[c.ID] = c
		if c.Events != nil || c.Turns != nil {
			t.Fatal("lists carry no transcripts")
		}
	}
	lg, ok := byID[runID]
	if !ok || !lg.Legacy || lg.TurnCount != 1 || lg.Title != "Explain the cut please" || lg.Resumable || !lg.Here || lg.ProposalCount != 1 {
		t.Fatalf("legacy run as a conversation: %+v", lg)
	}
	full, _ := m.Get(ctx, runID, true, false)
	kinds := []string{}
	for _, e := range full.Events {
		kinds = append(kinds, e.Kind)
	}
	if strings.Join(kinds, ",") != "turn,text,tool,proposals,turn_end" || full.Turns[0].CostUSD != 0.4 {
		t.Fatalf("legacy events: %v", kinds)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: runID, AssistantMessageIn: AssistantMessageIn{Prompt: "more"}}); reason(err) != "legacy" {
		t.Fatalf("continue a legacy run: %v", err)
	}
	props, _ := m.Proposals(ctx, runID, true)
	if len(props.Turns) != 1 || props.Turns[0].Plans[0].Origin != Origin(runID, "old.plan.json") {
		t.Fatalf("legacy proposals keep their origins: %+v", props)
	}
	if o := byID[other.ID]; o.Here || o.InstallationName != "edit-laptop" {
		t.Fatalf("other machine's conversation: %+v", o)
	}
	for name, err := range map[string]error{
		"continue": func() error {
			_, err := m.Continue(ctx, AssistantContinueIn{ID: other.ID, AssistantMessageIn: AssistantMessageIn{Prompt: "x"}})
			return err
		}(),
		"interrupt": func() error { _, err := m.Interrupt(ctx, other.ID); return err }(),
		"stop":      func() error { _, err := m.Stop(ctx, other.ID); return err }(),
	} {
		if reason(err) != "other_machine" || !strings.Contains(err.Error(), "edit-laptop") {
			t.Errorf("%s on another machine's conversation: %v", name, err)
		}
	}
	// Rename and archive work from any machine.
	if r, err := m.Rename(ctx, ConversationRenameIn{ID: other.ID, Title: "Laptop chat"}); err != nil || r.Title != "Laptop chat" {
		t.Fatalf("rename: %v %+v", err, r)
	}
	yes := true
	if _, err := m.Archive(ctx, ConversationArchiveIn{ID: other.ID, Archived: &yes}); err != nil {
		t.Fatal(err)
	}
	if l, _ := m.List(ctx, ConversationListIn{Archived: true}); len(l.Conversations) != 1 || l.Conversations[0].ID != other.ID {
		t.Fatalf("archived list: %+v", l)
	}
	if l, _ := m.List(ctx, ConversationListIn{Search: "explain THE"}); len(l.Conversations) != 1 || l.Conversations[0].ID != runID {
		t.Fatalf("search: %+v", l)
	}
	// The stuck conversation was recovered.
	sc, _ := m.Get(ctx, stuck.ID, true, false)
	if sc.State != ConvIdle || sc.Turns[0].State != TurnLost || sc.Events[len(sc.Events)-1].Kind != "turn_end" {
		t.Fatalf("recovery: %+v", sc)
	}
	// A restarted daemon sees the same history.
	m2 := New(a, "")
	defer m2.Close()
	if l, err := m2.List(ctx, ConversationListIn{AllUsers: true}); err != nil || len(l.Conversations) != 2 {
		t.Fatalf("after restart: %v %d", err, len(l.Conversations))
	}
}

func TestActionCardsRunOnTheHumanChannel(t *testing.T) {
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "render it", Steps: []fakeclaude.Step{
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush render submit --revision " + head + " --quality review --json"}}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush project list --json | head"}}},
			// Unknown commands and requests without a valid target are not cards.
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush footage list --json"}}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush render submit --render x.render.json --json"}}},
			text("I asked for a review render."),
		}},
		{Match: "Done from the dashboard", Steps: []fakeclaude.Step{text("It is rendering now.")}},
		{Match: "approve it", Steps: []fakeclaude.Step{
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush revision approve " + head}}},
			text("Approving is up to you."),
		}},
	}}})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "render it please", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	if len(cv.Actions) != 1 {
		t.Fatalf("actions (shell syntax must not become a card): %+v", cv.Actions)
	}
	act := cv.Actions[0]
	if act.Kind != "render submit" || act.Op != "render.submit" || act.State != ActionPending || act.Title != "Render a review copy of version 1, “first cut”" || act.Turn != 1 {
		t.Fatalf("action: %+v", act)
	}
	if !strings.Contains(fmt.Sprint(cv.Events), "action") || cv.PendingActions != 0 && false {
		t.Fatal("no action event in the transcript")
	}
	in, _ := json.Marshal(ConversationActIn{ID: id, ActionID: act.ID})
	if _, err := ops.Call(ctx, a, "assistant.act", in, ops.Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("an action without a person: %v", err)
	}
	as := *a
	as.Actor.Via = models.ViaAssistant
	if _, err := ops.Call(ctx, &as, "assistant.act", in, ops.Caller{Human: true}); code(err) != apperr.Unauthorized {
		t.Fatalf("the assistant channel runs its own requests: %v", err)
	}
	dash := *a
	dash.Actor.Via = "dashboard"
	res, err := ops.Call(ctx, &dash, "assistant.act", in, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	done := res.(*ConversationActOut)
	if done.Action.State != ActionDone || done.Action.Result != "The review copy of version 1 is waiting to start" || !strings.HasPrefix(done.Action.Link, "/renders/rnd_") ||
		done.Action.Work == nil || done.Action.Work.State != WorkRunning {
		t.Fatalf("ran: %+v", done.Action)
	}
	var renders []models.Render
	_ = a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"revisionId": head}, &renders)
	if len(renders) != 1 || renders[0].CreatedBy.Via != "dashboard" {
		t.Fatalf("render created on the human channel: %+v", renders)
	}
	if _, err := ops.Call(ctx, &dash, "assistant.act", in, ops.Caller{Human: true}); code(err) != apperr.Conflict {
		t.Fatalf("running an action twice: %v", err)
	}
	// Starting the render wakes nobody (D142): Claude hears when it and its
	// quality check are done.
	time.Sleep(400 * time.Millisecond)
	if cv = waitIdle(t, m, id); len(cv.Turns) != 1 {
		t.Fatalf("Claude was woken when the render only started: %+v", cv.Turns)
	}
	report := finishRender(t, a, renders[0].ID, models.QAWarnings, 1)
	eventually(t, "Claude to hear the render is done", func() bool {
		c, _ := m.Get(ctx, id, false, false)
		return c != nil && len(c.Turns) == 2 && TurnTerminal(c.Turns[1].State)
	})
	cv = waitIdle(t, m, id)
	if len(cv.Turns) != 2 || !cv.Turns[1].Note || cv.Turns[1].State != TurnSucceeded {
		t.Fatalf("Claude is told the outcome: %+v", cv.Turns)
	}
	// The person reads it in plain words; Claude's note also has the render's
	// and the report's IDs to carry on with.
	note := cv.Turns[1].Prompt
	if !strings.Contains(note, "Result: The review copy of version 1 is waiting to start ("+renders[0].ID+").") || !strings.Contains(note, report) ||
		!strings.Contains(note, "is ready") {
		t.Fatalf("the note to Claude: %q", note)
	}
	if shown := cv.Turns[1].Shown; shown != "The review copy of version 1 is ready (5 s). The quality check found 1 thing worth checking." {
		t.Fatalf("the person's line: %q", shown)
	}
	// Human-only requests are cards too, and dismissing tells Claude.
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "approve it"}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, id)
	ap := cv.Actions[1]
	if !ap.HumanOnly || ap.Op != "revision.approve" || ap.Title != "Approve version 1, “first cut”" {
		t.Fatalf("approval card: %+v", ap)
	}
	din, _ := json.Marshal(ConversationActIn{ID: id, ActionID: ap.ID, Dismiss: true, Quiet: true})
	if _, err := ops.Call(ctx, &dash, "assistant.act", din, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	var rev models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &rev)
	if rev.Approval.State == models.ApprovalApproved {
		t.Fatal("a dismissed card approved the revision")
	}
}

func TestApplyCardPointsAtItsPlan(t *testing.T) {
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	plan := fmt.Sprintf(`{"baseRevisionId":%q,"instruction":"shorter b","rationale":"Less waiting before the answer.","ops":[{"op":"updateClip","id":"broll","set":{"out":11}}]}`, head)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		{Write: &fakeclaude.File{Name: "shorter.plan.json", Content: plan}},
		{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush edit apply --plan {{proposals}}/shorter.plan.json --confirm ct_x --json"}}},
		text("I asked you to apply it.")}}}})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "make b shorter", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	if len(cv.Actions) != 1 {
		t.Fatalf("actions: %+v", cv.Actions)
	}
	act := cv.Actions[0]
	// The card names the plan it applies (the dashboard then shows one card, not two),
	// says why and what changes in plain words.
	if act.Op != "edit.apply" || act.Proposal != "t1/shorter.plan.json" || act.Rationale != "Less waiting before the answer." || act.Title != "Apply the change: shorter b" ||
		len(act.Details) == 0 || !strings.Contains(act.Details[0], "s shorter (now ") || !strings.Contains(act.Details[0], "1 clip changed") {
		t.Fatalf("apply card: %+v", act)
	}
}

// A settings file in the workspace would be loaded on the next launch (setting
// sources project,local) and could grant permissions: it never survives.
func TestPrepareWorkspaceRemovesSettings(t *testing.T) {
	dir := t.TempDir()
	for _, rel := range []string{".claude/settings.json", ".claude/settings.local.json", "CLAUDE.local.md", ".mcp.json"} {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(`{"permissions":{"allow":["Bash"]}}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := prepareWorkspace(dir); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".claude/settings.json", ".claude/settings.local.json", "CLAUDE.local.md", ".mcp.json"} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("%s survived", rel)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills", "video-project", "SKILL.md")); err != nil {
		t.Fatalf("skills: %v", err)
	}
}

func TestFakeClaudeFillsInTheHead(t *testing.T) {
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		{Write: &fakeclaude.File{Name: "b.plan.json", Content: `{"baseRevisionId":"{{head}}","instruction":"shorter b","ops":[{"op":"updateClip","id":"broll","set":{"out":11}}]}`}},
		text("Wrote a plan.")}}}})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "shorter", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, out.Conversation.ID)
	props, err := m.Proposals(ctx, out.Conversation.ID, false)
	if err != nil || len(props.Turns) != 1 || len(props.Turns[0].Plans) != 1 {
		t.Fatalf("proposals: %+v %v", props, err)
	}
	if p := props.Turns[0].Plans[0]; p.Check == nil || !p.Check.Valid || p.Check.BaseRevisionID != head {
		t.Fatalf("the plan is based on the head from the message: %+v %+v", p.Check, p.Error)
	}
}

func TestApplyFromTheCLINeedsTheConfirmToken(t *testing.T) {
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	plan := fmt.Sprintf(`{"baseRevisionId":%q,"instruction":"shorter b","ops":[{"op":"updateClip","id":"broll","set":{"out":11}}]}`, head)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		{Write: &fakeclaude.File{Name: "shorter.plan.json", Content: plan}}, text("Wrote a plan.")}}}})
	ctx := context.Background()
	out, _ := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "make b shorter", ProjectID: projectID}})
	id := out.Conversation.ID
	waitIdle(t, m, id)
	cli := *a
	cli.Actor.Via = "cli"
	if _, err := m.Apply(ctx, &cli, ops.Caller{}, ConversationApplyIn{ID: id, Proposal: "shorter.plan.json"}); code(err) != apperr.ConfirmRequired {
		t.Fatalf("apply without a token: %v", err)
	}
	props, _ := m.Proposals(ctx, id, false)
	tok := props.Turns[0].Plans[0].Check.ConfirmToken
	if tok == "" || !strings.Contains(props.Turns[0].Plans[0].Check.Next, "metarush assistant apply "+id+" t1/shorter.plan.json") {
		t.Fatalf("CLI proposals carry the token and the next step: %+v", props.Turns[0].Plans[0].Check)
	}
	res, err := m.Apply(ctx, &cli, ops.Caller{}, ConversationApplyIn{ID: id, Proposal: "t1/shorter.plan.json", ConfirmToken: tok})
	if err != nil {
		t.Fatal(err)
	}
	if res.Revision.Origin != Origin(id, "t1/shorter.plan.json") || res.Revision.Number != 2 {
		t.Fatalf("applied: %+v", res.Revision)
	}
	props, _ = m.Proposals(ctx, id, true)
	if ap := props.Turns[0].Plans[0].Applied; ap == nil || ap.Number != 2 {
		t.Fatalf("shown as applied: %+v", props.Turns[0].Plans[0])
	}
}

func TestModelsEffortAndSettings(t *testing.T) {
	a := testApp(t, "")
	if err := config.Save(a.Cfg.Home(), config.Default(a.Cfg.Home())); err != nil {
		t.Fatal(err)
	}
	raw0, _ := os.ReadFile(config.Path(a.Cfg.Home()))
	_ = os.WriteFile(config.Path(a.Cfg.Home()), append(raw0, []byte("# keep me\n")...), 0o600)
	m := withFake(t, a, fakeclaude.Options{})
	ctx := context.Background()
	cat, err := m.Models(ctx, true)
	if err != nil || cat.Source != "claude-code" || len(cat.Models) != 4 {
		t.Fatalf("models: %+v %v", cat, err)
	}
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "hi"}, Model: "sonnet", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	waitIdle(t, m, id)
	sp := fakeclaude.Spawns(t, m.convDir(id))[0]
	if after(sp, "--model") != "sonnet" || after(sp, "--effort") != "high" {
		t.Fatalf("model/effort flags: %q", sp)
	}
	bad := map[string]ConversationConfigureIn{
		"sonnet has no xhigh":       {ID: id, Effort: ptr("xhigh")},
		"haiku takes no effort":     {ID: id, Model: ptr("haiku")},
		"unknown model":             {ID: id, Model: ptr("gpt-5")},
		"not an effort":             {ID: id, Effort: ptr("extreme")},
		"model with shell metachar": {ID: id, Model: ptr("sonnet;rm")},
	}
	for name, in := range bad {
		if _, err := m.Configure(ctx, in); code(err) != apperr.InvalidInput {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := m.Configure(ctx, ConversationConfigureIn{ID: id, Model: ptr("claude-fake-haiku"), Effort: ptr("default")}); err != nil {
		t.Fatalf("a full model name without effort: %v", err)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "again"}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, id)
	spawns := fakeclaude.Spawns(t, m.convDir(id))
	last := spawns[len(spawns)-1]
	if len(spawns) != 2 || after(last, "--model") != "claude-fake-haiku" || has(last, "--effort") || !has(last, "--resume") {
		t.Fatalf("new settings re-applied on resume: %q", last)
	}
	assertLockedDown(t, last, m, id)

	// Installation defaults: written to config.yaml without touching the rest.
	if _, err := m.SetSettings(ctx, AssistantSettingsIn{Model: ptr("opus"), Effort: ptr("low")}); err != nil {
		t.Fatal(err)
	}
	rawCfg, _ := os.ReadFile(config.Path(a.Cfg.Home()))
	if !strings.Contains(string(rawCfg), `model: "opus"`) || !strings.Contains(string(rawCfg), `effort: "low"`) || !strings.Contains(string(rawCfg), "# keep me") {
		t.Fatalf("config.yaml:\n%s", rawCfg)
	}
	reloaded, err := config.Load(a.Cfg.Home())
	if err != nil || reloaded.Assistant.Model != "opus" || reloaded.Assistant.Effort != "low" {
		t.Fatalf("reloaded: %v %+v", err, reloaded.Assistant)
	}
	if _, err := m.SetSettings(ctx, AssistantSettingsIn{Effort: ptr("max"), Model: ptr("haiku")}); code(err) != apperr.InvalidInput {
		t.Fatalf("haiku with effort as the default: %v", err)
	}
	o2, _ := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "defaults"}})
	waitIdle(t, m, o2.Conversation.ID)
	sp2 := fakeclaude.Spawns(t, m.convDir(o2.Conversation.ID))[0]
	if after(sp2, "--model") != "opus" || after(sp2, "--effort") != "low" {
		t.Fatalf("installation defaults: %q", sp2)
	}
	if s, _ := m.SetSettings(ctx, AssistantSettingsIn{Model: ptr("default"), Effort: ptr("default")}); s.Model != "" || s.Effort != "" {
		t.Fatalf("reset: %+v", s)
	}
	// Safe steps (D141): off is written; on is the default again (the line goes).
	if s, err := m.SetSettings(ctx, AssistantSettingsIn{SafeSteps: ptr(false)}); err != nil || s.SafeSteps {
		t.Fatalf("safe steps off: %+v %v", s, err)
	}
	if rawCfg, _ := os.ReadFile(config.Path(a.Cfg.Home())); !strings.Contains(string(rawCfg), "safeSteps: false") || !strings.Contains(string(rawCfg), "# keep me") {
		t.Fatalf("config.yaml:\n%s", rawCfg)
	}
	if reloaded, _ := config.Load(a.Cfg.Home()); reloaded.Assistant.RunsSafeSteps() {
		t.Fatal("safe steps are off after a reload")
	}
	if s, _ := m.SetSettings(ctx, AssistantSettingsIn{SafeSteps: ptr(true)}); !s.SafeSteps {
		t.Fatalf("safe steps on: %+v", s)
	}
	if rawCfg, _ := os.ReadFile(config.Path(a.Cfg.Home())); strings.Contains(string(rawCfg), "safeSteps") {
		t.Fatalf("on is the default (no line):\n%s", rawCfg)
	}
}

func ptr[T any](v T) *T { return &v }

func TestModelsFallBackWhenClaudeCannotList(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{NoModels: true}})
	cat, err := m.Models(context.Background(), true)
	if err != nil || cat.Source != "fallback" || cat.Note == "" {
		t.Fatalf("fallback: %+v %v", cat, err)
	}
}

func TestImagesAreConfined(t *testing.T) {
	a := testApp(t, "")
	m := New(a, "")
	defer m.Close()
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")
	write := func(p string, b []byte) {
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	scratch := a.Cfg.Scratch.Dir
	write(filepath.Join(scratch, "frames", "ast_x", "sheet.jpg"), jpeg)
	write(filepath.Join(scratch, "frames", "fake.jpg"), []byte("not really an image"))
	write(filepath.Join(m.dir(), "conversations", "asc_y", "proposals", "t1", "look.png"), png)
	write(filepath.Join(a.Cfg.Cache.Dir, "objects", "ab", "abcdef0123"), jpeg)
	write(filepath.Join(a.Cfg.Cache.Dir, "objects", "cd", "cdef4567"), []byte("a video, not an image"))
	outside := filepath.Join(t.TempDir(), "secret.jpg")
	write(outside, jpeg)
	_ = os.Symlink(outside, filepath.Join(scratch, "frames", "link.jpg"))
	if f, ct, _, err := m.OpenImage("scratch", "frames/ast_x/sheet.jpg"); err != nil || ct != "image/jpeg" {
		t.Fatalf("a contact sheet: %v %s", err, ct)
	} else {
		f.Close()
	}
	if f, ct, _, err := m.OpenImage("assistant", "conversations/asc_y/proposals/t1/look.png"); err != nil || ct != "image/png" {
		t.Fatalf("an image in the assistant folder: %v %s", err, ct)
	} else {
		f.Close()
	}
	if f, ct, _, err := m.OpenImage("cache", "objects/ab/abcdef0123"); err != nil || ct != "image/jpeg" {
		t.Fatalf("a precomputed contact sheet in the cache: %v %s", err, ct)
	} else {
		f.Close()
	}
	for _, c := range []struct{ root, path string }{
		{"cache", "objects/cd/cdef4567"}, {"cache", "../config.yaml"},
		{"scratch", "../secret.jpg"}, {"scratch", "frames/../../x.jpg"}, {"scratch", "/etc/passwd.jpg"}, {"scratch", outside},
		{"scratch", "frames\\ast_x\\sheet.jpg"}, {"scratch", "frames/link.jpg"}, {"scratch", "frames/fake.jpg"}, {"scratch", "frames/ast_x/sheet.txt"},
		{"home", "config.yaml"}, {"scratch", ""}, {"scratch", "frames/./ast_x/sheet.jpg"}, {"scratch", "frames/missing.jpg"},
	} {
		if f, _, _, err := m.OpenImage(c.root, c.path); err == nil {
			f.Close()
			t.Errorf("served %s/%s", c.root, c.path)
		}
	}
}

func TestParserLabelsAndShell(t *testing.T) {
	names := map[string]string{"ast_01jabcdefghjkmnpqrstvwxyz0": "Interview A", "rev_01jabcdefghjkmnpqrstvwxyz0": "r3", "prj_01jabcdefghjkmnpqrstvwxyz0": "Test project"}
	p := newParser("/ws", "/as/conversations/asc_01jabcdefghjkmnpqrstvwxyz0", "/scratch", "/as", func(id string) string { return names[id] })
	p.aliases = [][2]string{{"cache", "/cache"}}
	lines := []string{
		`{"type":"system","subtype":"init","model":"claude-x","claude_code_version":"9","session_id":"s1"}`,
		`{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"a","name":"Bash","input":{"command":"metarush footage transcript ast_01jabcdefghjkmnpqrstvwxyz0 --from 11 --to 18.4 --json"}}],"usage":{"input_tokens":10,"cache_read_input_tokens":20000,"cache_creation_input_tokens":500}},"parent_tool_use_id":null}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"a","content":"{\"ok\":true,\"data\":{\"segments\":[{},{}]}}"}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"b","name":"Read","input":{"file_path":"/scratch/frames/ast_01jabcdefghjkmnpqrstvwxyz0/sheet-0-30.jpg"}}]},"parent_tool_use_id":null}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"b","content":[{"type":"image","source":{}}]}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"k","name":"Read","input":{"file_path":"/cache/objects/00/0087341a9c"}}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"p","name":"Bash","input":{"command":"metarush footage sheet ast_01jabcdefghjkmnpqrstvwxyz0 --json 2>&1 | head -100"}}]},"parent_tool_use_id":null}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"p","content":"{\"ok\":true,\"data\":{\"sheet\":{\"path\":\"/cache/objects/3d/3dc0\"}}}"}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"q","name":"Read","input":{"file_path":"/cache/objects/3d/3dc0"}}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"c","name":"TodoWrite","input":{"todos":[{"content":"Read transcripts","status":"completed","activeForm":"Reading"},{"content":"Write the plan","status":"in_progress"}]}}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"d","name":"Write","input":{"file_path":"/as/conversations/asc_01jabcdefghjkmnpqrstvwxyz0/proposals/t2/tight.plan.json","content":"{}"}}]},"parent_tool_use_id":null}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Inside a subagent"}]},"parent_tool_use_id":"x"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Here is the summary.\n[title: Tight opening for r3]\n"}]},"parent_tool_use_id":null}`,
		`not json`,
		`{"type":"result","subtype":"success","is_error":false,"duration_ms":10,"num_turns":4,"result":"Here is the summary.\n[title: Tight opening for r3]","total_cost_usd":0.5,"modelUsage":{"m":{"contextWindow":200000}}}`,
	}
	var evs []AssistantEvent
	var res *TurnResult
	var partials []*partialOp
	for _, l := range lines {
		out := p.line(l)
		evs = append(evs, out.events...)
		if out.result != nil {
			res = out.result
		}
		if out.partial != nil {
			partials = append(partials, out.partial)
		}
	}
	if p.model != "claude-x" || p.sessionID != "s1" || p.title != "Tight opening for r3" {
		t.Fatalf("parser state: %+v", p)
	}
	if res == nil || res.Text != "Here is the summary." || res.ContextTokens != 20510 || res.ContextWindow != 200000 || res.Steps != 4 {
		t.Fatalf("result: %+v", res)
	}
	if len(partials) == 0 || !partials[0].start || partials[0].phase != "thinking" {
		t.Fatalf("partials: %+v", partials)
	}
	var labels []string
	var outcome string
	var imgs []ImageRef
	var todos []TodoItem
	var proposal string
	for _, e := range evs {
		switch e.Kind {
		case "tool":
			labels = append(labels, e.Tool.Label)
			imgs = append(imgs, e.Tool.Images...)
			todos = append(todos, e.Tool.Todos...)
			if e.Tool.Proposal != "" {
				proposal = e.Tool.Proposal
			}
		case "tool_result":
			if e.Tool.ID == "a" {
				outcome = e.Tool.Outcome
			}
		case "text":
			if strings.Contains(e.Text, "subagent") || strings.Contains(e.Text, "[title") {
				t.Fatalf("text: %q", e.Text)
			}
		}
	}
	want := []string{"Read the transcript of Interview A (0:11-0:18)", "Looked at the stills from Interview A", "Looked at the stills", "Pulled stills from Interview A", "Looked at the stills from Interview A", "Updated the checklist", "Drafted the change “tight”"}
	if strings.Join(labels, "|") != strings.Join(want, "|") {
		t.Fatalf("labels:\n got %q\nwant %q", labels, want)
	}
	if outcome != "2 lines" || len(imgs) != 3 || imgs[0] != (ImageRef{Root: "scratch", Path: "frames/ast_01jabcdefghjkmnpqrstvwxyz0/sheet-0-30.jpg"}) || imgs[1] != (ImageRef{Root: "cache", Path: "objects/00/0087341a9c"}) {
		t.Fatalf("outcome %q images %+v", outcome, imgs)
	}
	if len(todos) != 2 || todos[1].Status != "in_progress" || proposal != "t2/tight.plan.json" {
		t.Fatalf("todos %+v proposal %q", todos, proposal)
	}
	if got := p.short("metarush edit check --plan /as/conversations/asc_01jabcdefghjkmnpqrstvwxyz0/proposals/t2/a.plan.json --json"); got != "metarush edit check --plan proposals/t2/a.plan.json --json" {
		t.Fatalf("short: %q", got)
	}
	for in, ok := range map[string]bool{
		`metarush render submit --revision rev_x --quality review`: true, `metarush comment add --project p 'it''s "fine"'`: true,
		`metarush project list | head`: false, `metarush x; rm -rf /`: false, `metarush $(whoami)`: false, `metarush "a$HOME"`: false,
		`metarush * `: false, `metarush ~/x`: false, "metarush a\nb": false, `metarush 'unterminated`: false,
	} {
		_, err := splitCommand(in)
		if (err == nil) != ok {
			t.Errorf("splitCommand(%q): %v", in, err)
		}
	}
	words, _ := splitCommand(`metarush comment add --project prj_1 --at 3.5 "Tighten the 'end' here"`)
	call, ok := parseMetarush(words)
	if !ok || call.Path != "comment add" || call.Flags["project"] != "prj_1" || call.Flags["at"] != "3.5" || call.Args[0] != "Tighten the 'end' here" {
		t.Fatalf("parse: %+v", call)
	}
	if w, err := splitCommand("FOO=1 metarush x"); err != nil {
		t.Fatal(err)
	} else if _, ok := parseMetarush(w); ok {
		t.Fatal("an env assignment is not a metarush call")
	}
	if c, _ := parseMetarush([]string{"metarush", "edit", "plan", "validate", "x.json"}); c.Path != "edit plan validate" {
		t.Fatalf("three-word path: %q", c.Path)
	}
	for _, tc := range []struct {
		words []string
		path  string
		args  int
	}{
		{[]string{"metarush", "footage", "speakers", "rename", "ast_x", "Speaker 2", "Maya"}, "footage speakers rename", 3},
		{[]string{"metarush", "todos", "answer", "tdo_x", "Maya", "--details"}, "todos answer", 2},
		{[]string{"metarush", "ingest", "precheck", "a.mov"}, "ingest precheck", 1},
		{[]string{"metarush", "ingest", "footage"}, "ingest", 1},
		{[]string{"metarush", "people", "merge", "Tess", "Tessa"}, "people merge", 2},
		// Remotion sources (D131–D176, D209): a source's name is never a subcommand.
		{[]string{"metarush", "remotion", "list", "--project", "Film"}, "remotion list", 0},
		{[]string{"metarush", "remotion", "words", "teaser", "--json"}, "remotion words", 1},
		{[]string{"metarush", "remotion", "words", "set", "teaser", "END.tagline=Hi", "--check"}, "remotion words set", 2},
		{[]string{"metarush", "remotion", "pictures", "list", "teaser"}, "remotion pictures list", 1},
		{[]string{"metarush", "remotion", "pictures", "set", "teaser", "HERO", "art.png", "--check", "--wait"}, "remotion pictures set", 3},
		{[]string{"metarush", "remotion", "refresh", "plan", "teaser", "--step", "npm run vo"}, "remotion refresh plan", 1},
	} {
		if c, _ := parseMetarush(tc.words); c.Path != tc.path || len(c.Args) != tc.args {
			t.Errorf("parse %q: path %q args %q, want %q with %d args", tc.words, c.Path, c.Args, tc.path, tc.args)
		}
	}
	if deriveTitle("What's in this project? Look at a contact sheet of each clip.") != "What's in this project" || deriveTitle("Look at interview-a.mov and broll.mp4 and tell me more") != "Look at interview-a.mov and broll.mp4" {
		t.Fatalf("first-sentence titles: %q %q", deriveTitle("What's in this project? Look at a contact sheet of each clip."), deriveTitle("Look at interview-a.mov and broll.mp4 and tell me more"))
	}
	if deriveTitle("tighten the b-roll in the current cut please, now!") != "Tighten the b-roll in the current" || deriveTitle("   ") != "New conversation" {
		t.Fatalf("titles: %q", deriveTitle("tighten the b-roll in the current cut please, now!"))
	}
}

func TestSuggestionsFollowTheState(t *testing.T) {
	a := testApp(t, "")
	m := New(a, "")
	defer m.Close()
	ctx := context.Background()
	p, _ := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Empty one"}, ops.Caller{Human: true})
	labels := func(projectID string) string {
		out, err := m.Suggestions(ctx, AssistantSuggestionsIn{ProjectID: projectID})
		if err != nil {
			t.Fatal(err)
		}
		var ls []string
		for _, s := range out.Suggestions {
			if strings.Contains(strings.ToLower(s.Label+s.Prompt), "metarush") {
				t.Fatalf("suggestions never mention the product: %+v", s)
			}
			ls = append(ls, s.Label)
		}
		if len(ls) > 6 {
			t.Fatalf("too many suggestions: %v", ls)
		}
		return strings.Join(ls, "|")
	}
	if got := labels(p.Project.ID); !strings.Contains(got, "What do I need to get started?") {
		t.Fatalf("empty project: %s", got)
	}
	projectID, _, _ := fixture(t, a)
	if got := labels(projectID); !strings.Contains(got, "Tighten the middle, keep the ending") || !strings.Contains(got, "Render a review copy") || !strings.Contains(got, "Make a 16:9 version") {
		t.Fatalf("project with a cut: %s", got)
	}
	now := time.Now().UTC()
	inbox := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{}, Kind: "video", Name: "loose.mov", SHA256: strings.Repeat("ef", 32),
		State: models.AssetAvailable, Media: models.MediaInfo{Streams: []models.Stream{}}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	_ = a.DB.Insert(ctx, store.CAssets, inbox)
	if got := labels(""); !strings.Contains(got, "Sort my inbox into projects") {
		t.Fatalf("workspace with inbox items: %s", got)
	}
	// Questions waiting on the person come first: unnamed voices, else the rest.
	if _, err := todos.Upsert(ctx, a, models.Todo{Kind: models.TodoMusicRights, Scope: "project", ProjectID: projectID, ProjectIDs: []string{projectID},
		GroupKey: "music:test", Question: "Who owns the music in the first cut?", FreeText: true}); err != nil {
		t.Fatal(err)
	}
	if got := labels(projectID); !strings.HasPrefix(got, "Go through the open questions|") {
		t.Fatalf("open questions first: %s", got)
	}
	if _, err := todos.Upsert(ctx, a, models.Todo{Kind: models.TodoSpeakerUnnamed, Scope: "asset", ProjectID: projectID, ProjectIDs: []string{projectID},
		GroupKey: "speaker:test", Question: "Who is Speaker 2 in the interview?", FreeText: true}); err != nil {
		t.Fatal(err)
	}
	if got := labels(projectID); !strings.HasPrefix(got, "Help me name the voices|") {
		t.Fatalf("unnamed voices first: %s", got)
	}
	if got := labels(""); !strings.HasPrefix(got, "Help me name the voices|") {
		t.Fatalf("workspace with unnamed voices: %s", got)
	}
	if _, err := m.Suggestions(ctx, AssistantSuggestionsIn{ProjectID: "prj_bad"}); code(err) != apperr.InvalidInput {
		t.Fatalf("bad project: %v", err)
	}
}

func TestVoicesAndQuestionsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	projectID, assetID, _ := fixture(t, a)
	ctx := context.Background()
	if _, err := todos.Upsert(ctx, a, models.Todo{Kind: models.TodoMusicRights, Scope: "project", ProjectID: projectID, ProjectIDs: []string{projectID},
		GroupKey: "music:cards", Question: "Who owns the music in the first cut?", FreeText: true}); err != nil {
		t.Fatal(err)
	}
	var td models.Todo
	if err := a.DB.C(store.CTodos).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "groupKey": "music:cards"}).Decode(&td); err != nil {
		t.Fatal(err)
	}
	// A voice Claude refers to by its ID with flags (seen with a real model).
	voice := models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: a.WorkspaceID(), AssetID: assetID, Run: "r1", Label: "speaker_00"}
	if err := a.DB.Insert(ctx, store.CSpeakers, voice); err != nil {
		t.Fatal(err)
	}
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		// Reading who speaks is allowed (no card); naming a voice and answering are changes.
		{Tool: &fakeclaude.ToolStep{Name: "Bash", Input: map[string]any{"command": "metarush footage speakers list " + assetID + " --json"}, Result: `{"ok":true,"data":{}}`}},
		{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": `metarush footage speakers rename ` + assetID + ` "Speaker 2" "Maya" --json`}}},
		{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": `metarush people merge "Tess" "Tessa" --json`}}},
		{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush todos dismiss " + td.ID + " --json"}}},
		{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush todos answer tdo_nope --json"}}},
		{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush footage speakers rename --speaker " + voice.ID + " --name Sam --json"}}},
		text("I asked you to confirm those.")}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "who is speaking? name them", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	got := map[string]AssistantAction{}
	for _, act := range cv.Actions {
		if _, seen := got[act.Op]; !seen {
			got[act.Op] = act
		}
	}
	if len(cv.Actions) != 4 {
		t.Fatalf("cards (a malformed answer is not one): %+v", cv.Actions)
	}
	if last := cv.Actions[3]; last.Op != "footage.speakers.rename" || !strings.HasPrefix(last.Title, "Name Speaker 1 in ") || !strings.HasSuffix(last.Title, "“Sam”") ||
		!strings.Contains(last.Input, `"speaker":"speaker_00"`) || !strings.Contains(last.Input, assetID) {
		t.Fatalf("flag form with a voice ID: %+v (input %s)", last, last.Input)
	}
	if r := got["footage.speakers.rename"]; !strings.HasPrefix(r.Title, "Name Speaker 2 in ") || !strings.HasSuffix(r.Title, "“Maya”") || r.State != ActionPending {
		t.Fatalf("rename card: %+v", r)
	}
	if mg := got["people.merge"]; mg.Title != "“Tess” and “Tessa” are the same person" {
		t.Fatalf("merge card: %+v", mg)
	}
	ds := got["todos.dismiss"]
	if ds.Title != "Skip the question: Who owns the music in the first cut?" {
		t.Fatalf("dismiss card: %+v", ds)
	}
	dash := *a
	dash.Actor.Via = "dashboard"
	in, _ := json.Marshal(ConversationActIn{ID: cv.ID, ActionID: ds.ID, Quiet: true})
	res, err := ops.Call(ctx, &dash, "assistant.act", in, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	done := res.(*ConversationActOut).Action
	if done.State != ActionDone || done.Result == "" || strings.Contains(done.Result, "{") {
		t.Fatalf("dismissed through the card, with a plain result: %+v", done)
	}
	_ = a.DB.C(store.CTodos).FindOne(ctx, bson.M{"_id": td.ID}).Decode(&td)
	if td.State != models.TodoDismissed {
		t.Fatalf("to-do state %q", td.State)
	}
	if voiceLabel("speaker_01") != "Speaker 2" || voiceLabel("3") != "Speaker 3" || voiceLabel("Speaker 2") != "Speaker 2" || voiceLabel("Maya") != "“Maya”" {
		t.Fatal("voice labels")
	}
}

func TestInputIsValidated(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{})
	ctx := context.Background()
	bad := 1e12
	for name, in := range map[string]AssistantAskIn{
		"empty prompt":      {AssistantMessageIn: AssistantMessageIn{Prompt: "   "}},
		"huge prompt":       {AssistantMessageIn: AssistantMessageIn{Prompt: strings.Repeat("x", maxPromptRunes+1)}},
		"malformed project": {AssistantMessageIn: AssistantMessageIn{Prompt: "x", ProjectID: "prj_../../etc"}},
		"malformed clip":    {AssistantMessageIn: AssistantMessageIn{Prompt: "x", ClipID: "Clip One; rm -rf"}},
		"bad time":          {AssistantMessageIn: AssistantMessageIn{Prompt: "x", Time: &bad}},
		"bad time base":     {AssistantMessageIn: AssistantMessageIn{Prompt: "x", TimeBase: "wallclock"}},
		"budget too low":    {AssistantMessageIn: AssistantMessageIn{Prompt: "x"}, BudgetUSD: 0.001},
		"bad model":         {AssistantMessageIn: AssistantMessageIn{Prompt: "x"}, Model: "--dangerously-skip-permissions"},
		"bad effort":        {AssistantMessageIn: AssistantMessageIn{Prompt: "x"}, Effort: "ultra"},
	} {
		if _, err := m.Ask(ctx, in); code(err) != apperr.InvalidInput {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "x", ProjectID: ids.New(ids.Project)}}); code(err) != apperr.NotFound {
		t.Errorf("unknown project: %v", err)
	}
	if _, err := m.Get(ctx, "asc_../../x", true, false); code(err) != apperr.NotFound {
		t.Errorf("path-like id: %v", err)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: ids.New(ConvPrefix), AssistantMessageIn: AssistantMessageIn{Prompt: "x"}}); code(err) != apperr.NotFound {
		t.Errorf("unknown conversation: %v", err)
	}
	if _, err := m.Rename(ctx, ConversationRenameIn{ID: ids.New(ConvPrefix), Title: "a\nb"}); code(err) != apperr.InvalidInput {
		t.Errorf("multi-line title: %v", err)
	}
}

func TestErrorsAreReportedHonestly(t *testing.T) {
	ctx := context.Background()
	// The status checks share one app (a test database takes seconds to set
	// up); each points it at its own Claude Code and its own manager.
	shared := testApp(t, "")
	t.Run("not signed in", func(t *testing.T) {
		m := withFake(t, shared, fakeclaude.Options{LoggedOut: true})
		st := m.Status(ctx, true)
		if st.Available || !strings.Contains(st.Reason, "not signed in") || st.Hint == "" {
			t.Fatalf("status: %+v", st)
		}
		if _, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "hi"}}); code(err) != apperr.NotConfigured {
			t.Fatalf("ask: %v", err)
		}
	})
	t.Run("not installed", func(t *testing.T) {
		shared.Cfg.Tools.Claude = filepath.Join(t.TempDir(), "no-such-claude")
		shared.Tools = tools.New(shared.Cfg.Tools)
		m := New(shared, "")
		defer m.Close()
		if st := m.Status(ctx, true); st.Available || !strings.Contains(st.Reason, "not installed") {
			t.Fatalf("status: %+v", st)
		}
	})
	t.Run("too old", func(t *testing.T) {
		m := withFake(t, shared, fakeclaude.Options{Help: "Usage: claude --print --output-format --max-budget-usd"})
		if st := m.Status(ctx, true); st.Available || !strings.Contains(st.Reason, "too old") {
			t.Fatalf("status: %+v", st)
		}
	})
	t.Run("help fails is not too old", func(t *testing.T) {
		m := withFake(t, shared, fakeclaude.Options{HelpFails: true})
		if st := m.Status(ctx, true); st.Available || !strings.Contains(st.Reason, "did not answer") {
			t.Fatalf("status: %+v", st)
		}
	})
	t.Run("a canceled request does not poison the cache", func(t *testing.T) {
		m := withFake(t, shared, fakeclaude.Options{})
		gone, cancel := context.WithCancel(ctx)
		cancel()
		if st := m.Status(gone, true); !st.Available {
			t.Fatalf("probe under a canceled context: %+v", st)
		}
	})
	t.Run("turned off", func(t *testing.T) {
		m := withFake(t, shared, fakeclaude.Options{})
		shared.Cfg.Assistant.Disabled = true
		defer func() { shared.Cfg.Assistant.Disabled = false }()
		if st := m.Status(ctx, true); st.Available || !strings.Contains(st.Reason, "turned off") {
			t.Fatalf("status: %+v", st)
		}
	})
	t.Run("claude reports an error", func(t *testing.T) {
		a := testApp(t, "")
		m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{IsError: true, Subtype: "success", Result: "Invalid API key · Please run /login"}}})
		out, _ := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "hi"}})
		if cv := waitIdle(t, m, out.Conversation.ID); cv.Turns[0].State != TurnFailed || !strings.Contains(cv.Turns[0].Error, "Invalid API key") {
			t.Fatalf("failed turn: %+v", cv.Turns[0])
		}
	})
	t.Run("claude does not start", func(t *testing.T) {
		a := testApp(t, "")
		m := withFake(t, a, fakeclaude.Options{Body: "echo 'segfault in the fake' >&2\nexit 3"})
		out, _ := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "hi"}})
		if cv := waitIdle(t, m, out.Conversation.ID); cv.Turns[0].State != TurnFailed || !strings.Contains(cv.Turns[0].Error, "segfault in the fake") {
			t.Fatalf("crashed: %+v", cv.Turns[0])
		}
	})
	t.Run("a lost session cannot be resumed", func(t *testing.T) {
		a := testApp(t, "")
		m := withFake(t, a, fakeclaude.Options{})
		out, _ := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "hi"}})
		id := out.Conversation.ID
		waitIdle(t, m, id)
		_, _ = m.Stop(ctx, id)
		_ = os.RemoveAll(filepath.Join(m.convDir(id), "fake-sessions")) // as if ~/.claude/projects were cleared
		_, _ = m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "again"}})
		cv := waitIdle(t, m, id)
		if cv.Turns[1].State != TurnFailed || !strings.Contains(cv.Turns[1].Error, "no longer on this machine") || cv.Resumable {
			t.Fatalf("lost session: %+v resumable=%v", cv.Turns[1], cv.Resumable)
		}
		if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "x"}}); reason(err) != "session_lost" {
			t.Fatalf("continue a lost session: %v", err)
		}
	})
}

// The assistant channel cannot mutate, even with a misconfigured Claude Code.
func TestAssistantChannelIsReadOnly(t *testing.T) {
	a := testApp(t, "")
	_, _, head := fixture(t, a)
	as := *a
	as.Actor.Via = models.ViaAssistant
	ctx := context.Background()
	plan := json.RawMessage(fmt.Sprintf(`{"baseRevisionId":%q,"ops":[{"op":"updateClip","id":"broll","set":{"out":12}}]}`, head))
	if _, err := ops.CallTyped[ops.EditCheckOut](ctx, &as, "edit.check", ops.EditCheckIn{Plan: plan}, ops.Caller{}); err != nil {
		t.Fatalf("edit check must stay available: %v", err)
	}
	for name, in := range map[string]any{"edit.apply": ops.EditApplyIn{Plan: plan}, "project.create": ops.ProjectCreateIn{Name: "x"}, "revision.approve": ops.RevisionApproveIn{ID: head},
		"assistant.ask": AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "x"}}, "assistant.settings.set": AssistantSettingsIn{Model: ptr("opus")}} {
		b, _ := json.Marshal(in)
		_, err := ops.Call(ctx, &as, name, b, ops.Caller{Human: true})
		var ae *apperr.Error
		if !errors.As(err, &ae) || ae.Code != apperr.Unauthorized || ae.Details.(map[string]any)["reason"] != "assistant_read_only" {
			t.Errorf("%s on the assistant channel: %v", name, err)
		}
	}
}

func TestChildEnvAndRules(t *testing.T) {
	env := childEnv([]string{"PATH=/usr/bin", "HOME=/h", "AWS_SECRET_ACCESS_KEY=s", "GITHUB_TOKEN=g", "ANTHROPIC_API_KEY=k", "METARUSH_MONGODB_URI=mongodb://x",
		"METARUSH_MONGODB_ADMIN_URI=mongodb://admin", "METARUSH_S3_SECRET_ACCESS_KEY=s3", "METARUSH_ELEVENLABS_API_KEY=el", "METARUSH_GEMINI_API_KEY=ge",
		"METARUSH_TAILSCALE=/fake/tailscale", "METARUSH_TEST_MONGODB_URI=t", "METARUSH_VIA=cli", "CLAUDECODE=1", "CLAUDE_CODE_ENTRYPOINT=cli",
		"METARUSH_PUBLISH_YOUTUBE_CLIENT={}", "METARUSH_PUBLISH_YOUTUBE_PERSON_USR_ABC={}"}, "/ws/bin", "/home", "/ws/runs/r")
	got := strings.Join(env, "\n")
	for _, want := range []string{"PATH=/ws/bin:/usr/bin", "HOME=/h", "ANTHROPIC_API_KEY=k", "METARUSH_TAILSCALE=/fake/tailscale", "METARUSH_ASSISTANT_RUN_DIR=/ws/runs/r", "TERM=dumb"} {
		if !strings.Contains(got, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	// MetaRush's secrets never reach Claude Code: the shim reads them from the home itself.
	for _, bad := range []string{"AWS_SECRET", "GITHUB_TOKEN", "METARUSH_TEST_", "METARUSH_VIA", "CLAUDECODE", "CLAUDE_CODE_ENTRYPOINT",
		"METARUSH_MONGODB_URI", "METARUSH_MONGODB_ADMIN_URI", "METARUSH_S3_SECRET", "METARUSH_ELEVENLABS_API_KEY", "METARUSH_GEMINI_API_KEY", "METARUSH_PUBLISH_"} {
		if strings.Contains(got, bad) {
			t.Errorf("env passes %s", bad)
		}
	}
	if _, err := allowedTools("/a/scr(atch)", "/c", "/p"); err == nil {
		t.Error("a path that breaks rule syntax must be refused")
	}
	// A scratch or cache folder that is (or contains) the installation home would
	// also expose config.yaml and .env: that read rule is left out.
	if readRoot("/h", "/h") != "" || readRoot("/", "/h") != "" || readRoot("/h/cache", "/h") != "/h/cache" || readRoot("/hx", "/h") != "/hx" {
		t.Errorf("readRoot: %q %q %q %q", readRoot("/h", "/h"), readRoot("/", "/h"), readRoot("/h/cache", "/h"), readRoot("/hx", "/h"))
	}
	if rules, err := allowedTools(readRoot("/h", "/h"), "/h/cache", "/h/assistant/p"); err != nil || strings.Contains(strings.Join(rules, ","), "Read(//h/**)") ||
		!strings.Contains(strings.Join(rules, ","), "Read(//h/cache/**)") {
		t.Errorf("rules: %v %v", rules, err)
	}
	if s := shim("/opt/it's/metarush", "/h"); !strings.Contains(s, `'/opt/it'\''s/metarush'`) {
		t.Errorf("shim quoting: %s", s)
	}
	if !AllowsCommand("metarush edit check") || AllowsCommand("metarush edit apply") || AllowsCommand("metarush schema export") || !AllowsCommand("metarush schema show") ||
		AllowsCommand("metarush assistant conversations list") {
		t.Error("read-only command list")
	}
	// Claude can answer "who speaks here?" and read the open questions, but naming
	// voices or answering them are changes a person confirms.
	for c, ok := range map[string]bool{"metarush footage speakers list": true, "metarush people list": true, "metarush people show": true,
		"metarush todos list": true, "metarush todos show": true, "metarush todos count": true, "metarush footage speakers rename": false,
		"metarush footage speakers": false, "metarush people merge": false, "metarush todos answer": false, "metarush todos dismiss": false} {
		if AllowsCommand(c) != ok {
			t.Errorf("AllowsCommand(%q) = %v", c, !ok)
		}
	}
	caps := claudeCaps{dontAsk: true, tools: true, strictMCP: true, partial: true, sessionID: true, effort: true, autoCompact: true}
	cases := []struct {
		o    spawnOpts
		want []string
		not  []string
	}{
		{spawnOpts{steps: 60, sessionID: "s"}, []string{"--session-id", "--max-turns"}, []string{"--model", "--effort", "--resume", "--max-budget-usd"}},
		{spawnOpts{capped: true, budget: 2, model: "opus", effort: "max", sessionID: "s", resume: true}, []string{"--model", "opus", "--effort", "max", "--resume", "--max-budget-usd"}, []string{"--session-id"}},
		{spawnOpts{capped: true, budget: 0, model: "claude-fake-haiku", sessionID: "s"}, []string{"0.01", "claude-fake-haiku"}, []string{"--effort"}},
		{spawnOpts{sessionID: "s", autoCompact: 50_000}, []string{"--autocompact", "100000"}, []string{"--max-budget-usd"}},
	}
	for _, c := range cases {
		argv := claudeArgs(c.o, caps, []string{"Skill"}, nil)
		for _, w := range c.want {
			if !has(argv, w) {
				t.Errorf("%+v: argv lacks %q", c.o, w)
			}
		}
		for _, n := range c.not {
			if has(argv, n) {
				t.Errorf("%+v: argv has %q", c.o, n)
			}
		}
	}
	if argv := claudeArgs(spawnOpts{effort: "high"}, claudeCaps{}, nil, nil); has(argv, "--effort") {
		t.Error("--effort is only passed when Claude Code supports it")
	}
	for _, want := range []string{"reference/voice.md", "a video editor talking to a creator", "Never talk about code, files", "need to say",
		"Never ask the person to run commands", ".question.json", "when it has\nFINISHED", "Never ask the person to tell you when something", "Safe steps start by themselves"} {
		if !strings.Contains(systemRules, want) {
			t.Errorf("system rules lack %q", want)
		}
	}
	if _, err := os.Stat(filepath.Join("..", "..", "skills", "_shared", "voice.md")); err != nil {
		t.Errorf("the voice guide is missing: %v", err)
	}
}
