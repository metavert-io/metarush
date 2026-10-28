package handover_test

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/handover"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/recipes"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/tools"

	_ "github.com/metavert-io/metarush/internal/ingest" // watch.set
	_ "github.com/metavert-io/metarush/internal/intake" // resources.folder
)

func TestMain(m *testing.M) {
	fakeclaude.MaybeRun() // this binary doubles as the fake `claude`
	handover.ScryptWorkFactor = 10
	os.Exit(m.Run())
}

const pass = "correct horse battery staple"

// computer is one installation of the workspace, with its own home and its
// own Claude Code folder (CLAUDE_CONFIG_DIR stands in for that computer's
// ~/.claude while it is the one at work).
type computer struct {
	a      *app.App
	name   string
	claude string
	m      *assistant.Manager
}

type workspace struct {
	db    *store.DB
	id    string
	media string
}

func newWorkspace(t *testing.T) *workspace {
	t.Helper()
	db := testutil.DB(t)
	w := &workspace{db: db, id: ids.New(ids.Workspace), media: t.TempDir()}
	if err := db.Insert(context.Background(), store.CWorkspaces, models.Workspace{ID: w.id, WorkspaceID: w.id, Name: "Handover", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	return w
}

func (w *workspace) computer(t *testing.T, name string) *computer {
	t.Helper()
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: w.id, Name: "Handover"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: name}
	cfg.User = config.UserRef{ID: "usr_01k8handovertestperson000", Name: "Maya", Email: "maya@example.com"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: w.db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: w.media, Shared: true}}
	off := false
	cfg.Assistant.SafeSteps, cfg.Assistant.HoldNews = &off, &off
	cfg.Secrets = "env"
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	a, err := app.OpenWith(context.Background(), cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	now := time.Now().UTC()
	if err := a.DB.Insert(context.Background(), store.CInstallations, models.Installation{ID: cfg.Installation.ID, WorkspaceID: w.id, Name: name,
		State: models.InstallationActive, CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	return &computer{a: a, name: name, claude: t.TempDir()}
}

// at makes this computer the one at work: its Claude Code folder, and a
// manager with the fake Claude Code.
func (c *computer) at(t *testing.T, script *fakeclaude.Script) *assistant.Manager {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", c.claude)
	if c.m == nil {
		c.a.Cfg.Tools.Claude = fakeclaude.Write(t, fakeclaude.Options{Script: script})
		c.a.Tools = tools.New(c.a.Cfg.Tools)
		c.m = assistant.New(c.a, "/usr/local/bin/metarush-test")
		t.Cleanup(c.m.Close)
	}
	return c.m
}

func remembers() *fakeclaude.Script {
	return &fakeclaude.Script{Sessions: "claude", Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{{Text: "I remember {{remembered}} messages."}}, CostUSD: 0.001}}
}

func person() ops.Caller { return ops.Caller{Human: true} }

func call[O any](t *testing.T, ctx context.Context, a *app.App, name string, in any) *O {
	t.Helper()
	out, err := ops.CallTyped[O](ctx, a, name, in, person())
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func refused(t *testing.T, err error, code, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s/%s, got nothing", code, reason)
	}
	ae := apperr.From(err)
	got := ""
	if d, ok := ae.Details.(map[string]any); ok {
		got, _ = d["reason"].(string)
	}
	if ae.Code != code || (reason != "" && got != reason) {
		t.Fatalf("expected %s/%s, got %s/%s: %v", code, reason, ae.Code, got, err)
	}
}

// talk sends a message and waits for Claude's reply; it returns the reply.
func talk(t *testing.T, m *assistant.Manager, id, prompt string) (string, string) {
	t.Helper()
	ctx := context.Background()
	if id == "" {
		out, err := m.Ask(ctx, assistant.AssistantAskIn{AssistantMessageIn: assistant.AssistantMessageIn{Prompt: prompt}})
		if err != nil {
			t.Fatal(err)
		}
		id = out.Conversation.ID
	} else if _, err := m.Continue(ctx, assistant.AssistantContinueIn{ID: id, AssistantMessageIn: assistant.AssistantMessageIn{Prompt: prompt}}); err != nil {
		t.Fatal(err)
	}
	var c *assistant.Conversation
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		got, err := m.Get(ctx, id, true, true)
		if err != nil {
			t.Fatal(err)
		}
		if got.State == assistant.ConvIdle && len(got.Queue) == 0 && len(got.Turns) > 0 && assistant.TurnTerminal(got.Turns[len(got.Turns)-1].State) {
			c = got
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if c == nil {
		t.Fatal("the reply didn't come")
	}
	last := c.Turns[len(c.Turns)-1]
	if last.State != assistant.TurnSucceeded {
		t.Fatalf("turn %d %s: %s", last.N, last.State, last.Error)
	}
	reply := ""
	for _, e := range c.Events {
		if e.Kind == "text" && e.Turn == last.N {
			reply += e.Text
		}
	}
	return id, reply
}

func conv(t *testing.T, a *app.App, id string) assistant.Conversation {
	t.Helper()
	var d assistant.Conversation
	if err := a.DB.GetByID(context.Background(), store.CAssistantConversations, a.WorkspaceID(), id, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func lines(t *testing.T, file string) []fakeclaude.SessionLine {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var out []fakeclaude.SessionLine
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var s fakeclaude.SessionLine
		if json.Unmarshal([]byte(l), &s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// sessionAt is where Claude Code keeps a session of a computer's working folder.
func sessionAt(t *testing.T, c *computer, sid string) string {
	t.Helper()
	wd, err := filepath.EvalSymlinks(filepath.Join(c.a.Cfg.AssistantDir(), "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(c.claude, "projects", fakeclaude.ProjectKey(wd), sid+".jsonl")
}

// Two computers in one test: a conversation goes on, through the handover
// file, on the new computer where it left off (the same session, appended);
// the old computer refuses to continue it from the moment it is handed over.
func TestHandoverCarriesConversations(t *testing.T) {
	w := newWorkspace(t)
	old, nu := w.computer(t, "Studio MacBook"), w.computer(t, "studio-mini")
	ctx := context.Background()

	mOld := old.at(t, remembers())
	id, reply := talk(t, mOld, "", "Hello there, let's plan the teaser.")
	if reply != "I remember 0 messages." {
		t.Fatalf("first reply %q", reply)
	}
	_, reply = talk(t, mOld, id, "Make it thirty seconds.")
	if reply != "I remember 1 messages." {
		t.Fatalf("second reply %q", reply)
	}
	sid := conv(t, old.a, id).ClaudeSessionID
	oldSession := sessionAt(t, old, sid)
	if n := len(lines(t, oldSession)); n != 4 {
		t.Fatalf("the old session has %d lines", n)
	}
	// Something the conversation wrote in its own folder travels too.
	if err := os.WriteFile(filepath.Join(assistant.ConversationFolder(old.a, id), "proposals", "t2", "plan.json"), []byte(`{"note":"kept"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// The dry run.
	plan := call[handover.HandoverPlan](t, ctx, old.a, "machine.handover.plan", handover.HandoverPlanIn{To: "STUDIO-MINI"})
	if !plan.Ready || plan.To == nil || plan.To.ID != nu.a.Cfg.Installation.ID || len(plan.Conversations) != 1 || plan.Conversations[0].Session != "here" {
		t.Fatalf("plan %+v", plan)
	}
	if len(plan.Steps) == 0 || !strings.Contains(plan.Steps[0].Words, "Claude account") {
		t.Fatalf("the plan says whose Claude account answers: %+v", plan.Steps)
	}

	// The handover file, private and protected.
	out := filepath.Join(t.TempDir(), "teaser.age")
	pctx := handover.WithPassphrase(ctx, pass)
	ex := call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "studio-mini", Out: out})
	if !ex.Encrypted || ex.Conversations != 1 || ex.Sessions != 1 || ex.Handover.State != handover.StateExported {
		t.Fatalf("export %+v", ex)
	}
	if st, err := os.Stat(out); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("file %v %v", st, err)
	}
	if enc, _ := handover.Encrypted(out); !enc {
		t.Fatal("the file isn't an age file")
	}
	// From now on the old computer doesn't continue it.
	if _, err := mOld.Continue(ctx, assistant.AssistantContinueIn{ID: id, AssistantMessageIn: assistant.AssistantMessageIn{Prompt: "One more thing"}}); err == nil {
		t.Fatal("the old computer continued a conversation it is handing over")
	} else {
		refused(t, err, apperr.Conflict, "handing_over")
	}
	if g, _ := mOld.Get(ctx, id, false, false); g.Resumable || g.HandingOver == nil || g.HandingOver.ToName != "studio-mini" {
		t.Fatalf("the panel says it is moving: %+v", g.HandingOver)
	}
	// Running the export again makes another file for the same handover.
	ex2 := call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "studio-mini", Out: out + "2"})
	if ex2.Handover.ID != ex.Handover.ID || ex2.Conversations != 1 {
		t.Fatalf("export again %+v", ex2)
	}

	// The new computer receives it.
	mNew := nu.at(t, remembers())
	rplan := call[handover.HandoverReceiveOut](t, pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out, Plan: true})
	if !rplan.Plan || len(rplan.Conversations) != 1 || rplan.Conversations[0].Session != "here" {
		t.Fatalf("receive plan %+v", rplan)
	}
	rec := call[handover.HandoverReceiveOut](t, pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out})
	if rec.Moved.Conversations != 1 || rec.Moved.Summaries != 0 || rec.Handover.State != handover.StateReceived {
		t.Fatalf("receive %+v", rec)
	}
	newSession := sessionAt(t, nu, sid)
	if n := len(lines(t, newSession)); n != 4 {
		t.Fatalf("the session put where Claude Code looks has %d lines", n)
	}
	if b, err := os.ReadFile(filepath.Join(assistant.ConversationFolder(nu.a, id), "proposals", "t2", "plan.json")); err != nil || !strings.Contains(string(b), "kept") {
		t.Fatalf("the conversation's folder came along: %v", err)
	}
	d := conv(t, nu.a, id)
	if d.InstallationID != nu.a.Cfg.Installation.ID || d.HandingOver != nil || d.Moved == nil || d.Moved.FromName != "Studio MacBook" {
		t.Fatalf("re-pinned %+v %+v", d.InstallationName, d.Moved)
	}

	// It goes on here where it left off: the same session, resumed in the new
	// working folder, and Claude hears once where the files are now.
	_, reply = talk(t, mNew, id, "Where were we?")
	if reply != "I remember 2 messages." {
		t.Fatalf("reply on the new computer %q", reply)
	}
	spawns := fakeclaude.SpawnLog(t, assistant.ConversationFolder(nu.a, id))
	last := spawns[len(spawns)-1]
	wd, _ := filepath.EvalSymlinks(filepath.Join(nu.a.Cfg.AssistantDir(), "workspace"))
	if last.Resumed() != sid || last.Cwd != wd || last.ConfigDir != nu.claude {
		t.Fatalf("spawn %+v", last)
	}
	got := lines(t, newSession)
	if len(got) != 6 || !strings.Contains(got[4].Text, "This conversation moved to another computer: what was in "+old.a.Cfg.AssistantDir()+" is now in "+nu.a.Cfg.AssistantDir()) {
		t.Fatalf("the moved note: %d lines, %q", len(got), got[len(got)-2].Text)
	}
	_, reply = talk(t, mNew, id, "And then?")
	if reply != "I remember 3 messages." || strings.Contains(lines(t, newSession)[6].Text, "moved to another computer") {
		t.Fatalf("the note is said once: %q", reply)
	}
	// One history: the old session is as it was, and the old computer still
	// refuses (it lives elsewhere now).
	if n := len(lines(t, oldSession)); n != 4 {
		t.Fatalf("the old session grew to %d lines", n)
	}
	if _, err := mOld.Continue(ctx, assistant.AssistantContinueIn{ID: id, AssistantMessageIn: assistant.AssistantMessageIn{Prompt: "Still there?"}}); err == nil {
		t.Fatal("the old computer continued it after it moved")
	} else {
		refused(t, err, apperr.Conflict, "other_machine")
	}

	// Receiving again changes nothing.
	again := call[handover.HandoverReceiveOut](t, pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out})
	if again.Conversations[0].Words != "Already here." {
		t.Fatalf("again %+v", again.Conversations)
	}
	if n := len(lines(t, newSession)); n != 8 {
		t.Fatalf("receiving again touched the session: %d lines", n)
	}

	// The old computer steps down (nothing of its own runs), then works as a
	// client; clean is offered there and removes only its copies.
	role, waiting, err := handover.Look(ctx, old.a)
	if err != nil || role.Role != handover.RoleClient || len(waiting) != 0 {
		t.Fatalf("step down: %+v %v %v", role, waiting, err)
	}
	st := call[handover.HandoverStatusOut](t, ctx, old.a, "machine.handover.status", handover.HandoverStatusIn{})
	if st.Role.Role != handover.RoleClient || st.Clean == nil || !strings.Contains(st.Summary, "no longer the host") {
		t.Fatalf("status %+v", st)
	}
	if r, _ := handover.RoleOf(ctx, nu.a, nu.a.Cfg.Installation.ID); r.Role != handover.RoleHost {
		t.Fatalf("the new computer is the host: %+v", r)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", old.claude) // clean runs on the old computer
	cp := call[handover.HandoverCleanOut](t, ctx, old.a, "machine.handover.clean", handover.HandoverCleanIn{})
	if cp.Removed || cp.Files < 2 {
		t.Fatalf("clean plan %+v", cp)
	}
	if _, err := os.Stat(oldSession); err != nil {
		t.Fatal("clean without yes removed something")
	}
	cl := call[handover.HandoverCleanOut](t, ctx, old.a, "machine.handover.clean", handover.HandoverCleanIn{Yes: true})
	if !cl.Removed {
		t.Fatalf("clean %+v", cl)
	}
	if _, err := os.Stat(oldSession); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the old copy of the session stayed")
	}
	if _, err := os.Stat(assistant.ConversationFolder(old.a, id)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the old copy of the folder stayed")
	}
	if _, err := os.Stat(newSession); err != nil {
		t.Fatal("clean touched the new computer's session")
	}
}

// A session that didn't come along (missing on the old computer, or
// unreadable) goes on from a summary MetaRush makes of the conversation.
func TestSummaryWhenTheSessionDidNotComeAlong(t *testing.T) {
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	mOld := old.at(t, remembers())
	gone, _ := talk(t, mOld, "", "Cut the intro to five seconds please.")
	broken, _ := talk(t, mOld, "", "Pick a warmer music track.")
	_ = os.Remove(sessionAt(t, old, conv(t, old.a, gone).ClaudeSessionID))
	if err := os.WriteFile(sessionAt(t, old, conv(t, old.a, broken).ClaudeSessionID), []byte("not json at all\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pctx := handover.WithPassphrase(ctx, pass)
	out := filepath.Join(t.TempDir(), "h.age")
	ex := call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	if ex.Summaries != 1 || ex.Sessions != 1 {
		t.Fatalf("export %+v", ex)
	}
	mNew := nu.at(t, remembers())
	rec := call[handover.HandoverReceiveOut](t, pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out})
	if rec.Moved.Conversations != 2 || rec.Moved.Summaries != 2 {
		t.Fatalf("receive %+v", rec.Moved)
	}
	for _, id := range []string{gone, broken} {
		d := conv(t, nu.a, id)
		if d.Moved == nil || !d.Moved.Summary || d.SessionStarted {
			t.Fatalf("%s goes on from a summary: %+v", id, d.Moved)
		}
		if g, _ := mNew.Get(ctx, id, false, false); !g.Resumable {
			t.Fatal("a conversation going on from a summary takes messages")
		}
	}
	if conv(t, nu.a, broken).Moved.Why != "unreadable" || conv(t, nu.a, gone).Moved.Why != "missing" {
		t.Fatal("why it goes on from a summary")
	}
	_, reply := talk(t, mNew, gone, "So, what did I ask for?")
	if reply != "I remember 0 messages." {
		t.Fatalf("a new session: %q", reply)
	}
	runDir := assistant.ConversationFolder(nu.a, gone)
	spawns := fakeclaude.SpawnLog(t, runDir)
	if spawns[len(spawns)-1].Resumed() != "" {
		t.Fatal("a summary starts a new session")
	}
	sent := strings.Join(fakeclaude.Received(t, runDir), "\n")
	if !strings.Contains(sent, "continues on this computer from a summary") || !strings.Contains(sent, "Cut the intro to five seconds please.") || !strings.Contains(sent, "I remember 0 messages.") {
		t.Fatalf("the summary carries the conversation: %s", sent)
	}
	g, _ := mNew.Get(ctx, gone, true, false)
	notice := false
	for _, e := range g.Events {
		notice = notice || (e.Kind == "notice" && strings.Contains(e.Text, "Continuing from a summary"))
	}
	if !notice || !g.Moved.Primed {
		t.Fatal("the panel says it continues from a summary")
	}
	// The next message goes on in that new session (no second summary).
	_, reply = talk(t, mNew, gone, "Thanks.")
	if reply != "I remember 1 messages." {
		t.Fatalf("after the summary: %q", reply)
	}
}

// Pinned work moves by check and set: nothing runs twice. Running work and
// started link imports finish on the old computer, which then steps down.
func TestPinnedWorkMovesWithoutDoubleRuns(t *testing.T) {
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	q := old.a.Queue
	job := func(typ string, attempts int) string {
		j, _, err := q.Enqueue(ctx, jobs.EnqueueOptions{Type: typ, Actor: old.a.Actor, Installation: old.a.Cfg.Installation.ID, IdempotencyKey: ids.New("tst"), MaxAttempts: 3})
		if err != nil {
			t.Fatal(err)
		}
		if attempts > 0 {
			_, _ = old.a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID}, bson.M{"$set": bson.M{"attemptCount": attempts}})
		}
		return j.ID
	}
	fetch := job(jobs.TypeFetch, 0)
	importStarted := job(jobs.TypeImport, 1)
	importNew := job(jobs.TypeImport, 0)
	render := job(jobs.TypeRender, 0)
	upload := job(jobs.TypeResourceUpload, 0)
	// The old computer's worker takes the render now: it finishes there.
	oldWorker := jobs.Worker{ID: old.a.Cfg.Installation.ID, Name: "laptop", JobTypes: []string{jobs.TypeRender}, AcceptsHeavy: true}
	if j, err := q.LeaseAmong(ctx, oldWorker, []string{render}); err != nil || j == nil {
		t.Fatalf("lease %v %v", j, err)
	}
	// A recipe run being looked at, one free, and a recipe that plans by itself.
	runFree, runLooked := ids.New("rrn"), ids.New("rrn")
	for i, id := range []string{runFree, runLooked} {
		if err := old.a.DB.Insert(ctx, store.CRecipeRuns, models.RecipeRun{ID: id, WorkspaceID: w.id, RecipeID: "rcp_teaser", RecipeTitle: "Teaser", Number: i + 1, State: models.RunRunning,
			Installation: old.a.Cfg.Installation.ID, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := old.a.DB.C(store.CRecipeRuns).UpdateOne(ctx, bson.M{"_id": runLooked}, mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "lease", Value: bson.D{
		{Key: "by", Value: "laptop-look"}, {Key: "until", Value: bson.D{{Key: "$add", Value: bson.A{"$$NOW", 300000}}}}}}}}}}); err != nil {
		t.Fatal(err)
	}
	recipeID := ids.New("rcp")
	if err := old.a.DB.Insert(ctx, store.CRecipes, models.Recipe{ID: recipeID, WorkspaceID: w.id, Name: "teaser", Title: "Teaser",
		Triggers: &models.RecipeTriggers{NewFiles: true, Installation: old.a.Cfg.Installation.ID, Computer: "laptop", At: time.Now().UTC()}}); err != nil {
		t.Fatal(err)
	}

	plan := call[handover.HandoverPlan](t, ctx, old.a, "machine.handover.plan", handover.HandoverPlanIn{To: "desk"})
	moves := map[string]bool{}
	for _, wk := range plan.Work {
		moves[wk.ID] = wk.Moves
	}
	if !moves[fetch] || moves[importStarted] || !moves[importNew] || moves[render] || moves[upload] || !moves[runFree] || !moves[recipeID] {
		t.Fatalf("plan's work %+v", plan.Work)
	}
	pctx := handover.WithPassphrase(ctx, pass)
	out := filepath.Join(t.TempDir(), "h.age")
	call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	rec := call[handover.HandoverReceiveOut](t, pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out})
	if rec.Moved.Jobs != 2 || rec.Moved.Runs != 1 || rec.Moved.Triggers != 1 {
		t.Fatalf("moved %+v", rec.Moved)
	}
	pinned := func(id string) string {
		var j models.Job
		_ = old.a.DB.GetByID(ctx, store.CJobs, w.id, id, &j)
		return j.Installation
	}
	n := nu.a.Cfg.Installation.ID
	o := old.a.Cfg.Installation.ID
	if pinned(fetch) != n || pinned(importNew) != n || pinned(importStarted) != o || pinned(render) != o || pinned(upload) != o {
		t.Fatal("jobs pinned where they should be")
	}
	// The old computer's worker never takes what moved; the new one does.
	oldAll := jobs.Worker{ID: o, Name: "laptop", JobTypes: []string{jobs.TypeFetch, jobs.TypeImport, jobs.TypeRender}, AcceptsHeavy: true}
	if j, _ := q.LeaseAmong(ctx, oldAll, []string{fetch, importNew}); j != nil {
		t.Fatalf("the old computer took %s after it moved", j.Type)
	}
	newAll := jobs.Worker{ID: n, Name: "desk", JobTypes: []string{jobs.TypeFetch, jobs.TypeImport}, AcceptsHeavy: true}
	if j, _ := nu.a.Queue.LeaseAmong(ctx, newAll, []string{fetch}); j == nil {
		t.Fatal("the new computer can take what moved")
	}
	// The run being looked at stays until its look ends; the old engine no
	// longer advances the run that moved.
	var r models.RecipeRun
	_ = old.a.DB.GetByID(ctx, store.CRecipeRuns, w.id, runLooked, &r)
	if r.Installation != o {
		t.Fatal("a run under a look moved")
	}
	if got, _ := recipes.Advance(ctx, old.a, runFree); got == nil || got.Installation != n || got.UpdatedAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("the old engine and the moved run: %+v", got)
	}
	var rc models.Recipe
	_ = old.a.DB.GetByID(ctx, store.CRecipes, w.id, recipeID, &rc)
	if rc.Triggers.Installation != n || rc.Triggers.Computer != "desk" {
		t.Fatalf("triggers %+v", rc.Triggers)
	}
	if planned, err := recipes.TriggerLook(ctx, old.a, time.Now().UTC()); err != nil || len(planned) != 0 {
		t.Fatal("the old computer still plans by itself")
	}

	// The old computer waits for its own work before stepping down.
	role, waiting, err := handover.Look(ctx, old.a)
	if err != nil || role.Role != handover.RoleSteppingDown || len(waiting) < 3 {
		t.Fatalf("stepping down: %+v %d %v", role.Role, len(waiting), err)
	}
	// Its look at the run ends; the render, the started import and the upload finish.
	_, _ = old.a.DB.C(store.CRecipeRuns).UpdateOne(ctx, bson.M{"_id": runLooked}, bson.M{"$unset": bson.M{"lease": ""}})
	_, _ = old.a.DB.C(store.CJobs).UpdateMany(ctx, bson.M{"_id": bson.M{"$in": bson.A{render, importStarted, upload}}}, bson.M{"$set": bson.M{"state": models.JobSucceeded}})
	role, waiting, err = handover.Look(ctx, old.a)
	if err != nil || role.Role != handover.RoleClient || len(waiting) != 0 {
		t.Fatalf("stepped down: %+v %v %v", role, waiting, err)
	}
	_ = old.a.DB.GetByID(ctx, store.CRecipeRuns, w.id, runLooked, &r)
	if r.Installation != n {
		t.Fatal("the run moved once its look ended")
	}
	// A new handover from the old computer is refused while it is a client.
	_, err = ops.CallTyped[handover.HandoverExportOut](pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: filepath.Join(t.TempDir(), "x.age")}, person())
	refused(t, err, apperr.Conflict, "not_host")
}

// Taking over from a computer that is gone: refused while it was seen
// recently or runs work, allowed once it is quiet and a person confirms it
// is off; its conversations come from a plain copy of its folders.
func TestTakeOverFromAComputerThatIsGone(t *testing.T) {
	w := newWorkspace(t)
	gone, nu := w.computer(t, "Old MacBook"), w.computer(t, "mini")
	ctx := context.Background()
	mGone := gone.at(t, remembers())
	withSession, _ := talk(t, mGone, "", "Tighten the pauses in the interview.")
	lost, _ := talk(t, mGone, "", "Find the best take of the intro.")
	sid := conv(t, gone.a, withSession).ClaudeSessionID
	// A person's plain copy of its folders: Claude Code's projects folder,
	// MetaRush's conversations folder and its config.yaml.
	cp := t.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("cp", "-R", filepath.Join(gone.claude, "projects"), filepath.Join(cp, "claude-projects"))
	_ = os.Remove(filepath.Join(cp, "claude-projects", filepath.Base(filepath.Dir(sessionAt(t, gone, sid))), conv(t, gone.a, lost).ClaudeSessionID+".jsonl"))
	run("mkdir", "-p", filepath.Join(cp, "metarush", "assistant"))
	run("cp", "-R", filepath.Join(gone.a.Cfg.AssistantDir(), "conversations"), filepath.Join(cp, "metarush", "assistant", "conversations"))
	watched := t.TempDir()
	cfgYAML := "workspace:\n    id: " + w.id + "\nworker:\n    watch:\n        enabled: true\n        dir: /Users/you/Inbox\n        stableSeconds: 5\n" +
		"media:\n    s3:\n        secretAccessKey: never-read-this-0123456789\n"
	if err := os.WriteFile(filepath.Join(cp, "metarush", "config.yaml"), []byte(cfgYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	// Work it had: a link import it started, a queued fetch.
	j, _, _ := gone.a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeImport, Actor: gone.a.Actor, Installation: gone.a.Cfg.Installation.ID, IdempotencyKey: "i1"})
	_, _ = gone.a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID}, bson.M{"$set": bson.M{"attemptCount": 1}})

	mNew := nu.at(t, remembers())
	in := handover.HandoverTakeoverIn{From: "old macbook", Source: cp, Plan: true}
	// Seen a moment ago: not yet.
	p := call[handover.HandoverTakeoverOut](t, ctx, nu.a, "machine.takeover", in)
	if p.Allowed || !strings.Contains(p.Why, "was seen") {
		t.Fatalf("recent %+v", p)
	}
	go1 := in
	go1.Plan, go1.ConfirmedOff = false, "Old MacBook"
	_, err := ops.CallTyped[handover.HandoverTakeoverOut](ctx, nu.a, "machine.takeover", go1, person())
	refused(t, err, apperr.Conflict, "seen_recently")
	// Off for an hour (by the server's clock), but running work under a live lease: not yet.
	_, _ = nu.a.DB.C(store.CInstallations).UpdateOne(ctx, bson.M{"_id": gone.a.Cfg.Installation.ID}, mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "lastSeenAt", Value: bson.D{{Key: "$subtract", Value: bson.A{"$$NOW", int64(time.Hour / time.Millisecond)}}}}}}}})
	busy := jobs.Worker{ID: gone.a.Cfg.Installation.ID, Name: "Old MacBook", JobTypes: []string{jobs.TypeImport}, AcceptsHeavy: true}
	if lj, _ := gone.a.Queue.LeaseAmong(ctx, busy, []string{j.ID}); lj == nil {
		t.Fatal("lease")
	}
	if p = call[handover.HandoverTakeoverOut](t, ctx, nu.a, "machine.takeover", in); p.Allowed || !strings.Contains(p.Why, "running work") {
		t.Fatalf("running work %+v", p)
	}
	// Its lease ended (it stopped mid-import): allowed, once a person confirms.
	_, _ = nu.a.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID}, bson.M{"$set": bson.M{"lease.expiresAt": time.Now().Add(-time.Hour)}})
	p = call[handover.HandoverTakeoverOut](t, ctx, nu.a, "machine.takeover", in)
	if !p.Allowed || len(p.Conversations) != 2 || len(p.Folders) != 1 || p.Folders[0].Path != "/Users/you/Inbox" {
		t.Fatalf("plan %+v", p)
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), "never-read-this") {
		t.Fatal("a key in config.yaml was read")
	}
	go1.Folders = []handover.HandoverFolderChoice{{Path: "/Users/you/Inbox", To: watched}}
	go1.ConfirmedOff = "someone else"
	_, err = ops.CallTyped[handover.HandoverTakeoverOut](ctx, nu.a, "machine.takeover", go1, person())
	refused(t, err, apperr.InvalidInput, "confirm_off")
	go1.ConfirmedOff = " old macbook "
	done := call[handover.HandoverTakeoverOut](t, ctx, nu.a, "machine.takeover", go1)
	if done.Moved.Conversations != 2 || done.Moved.Summaries != 1 || done.Moved.Jobs != 1 || done.Moved.Folders != 1 || done.Handover.Kind != handover.KindTakeover {
		t.Fatalf("take-over %+v", done.Moved)
	}
	if !strings.Contains(done.Next[0], `metarush machine revoke "Old MacBook"`) {
		t.Fatalf("next %v", done.Next)
	}
	var jb models.Job
	_ = nu.a.DB.GetByID(ctx, store.CJobs, w.id, j.ID, &jb)
	if jb.Installation != nu.a.Cfg.Installation.ID {
		t.Fatal("the started import of a gone computer moves")
	}
	if cfg, _ := config.Load(nu.a.Cfg.Home()); cfg.Worker.Watch.Dir != watched || cfg.Worker.Watch.StableSeconds != 5 {
		t.Fatalf("the watched folder here: %+v", cfg.Worker.Watch)
	}
	_, reply := talk(t, mNew, withSession, "Where were we?")
	if reply != "I remember 1 messages." {
		t.Fatalf("the copied session goes on: %q", reply)
	}
	if d := conv(t, nu.a, lost); d.Moved == nil || !d.Moved.Summary {
		t.Fatal("the one without a session goes on from a summary")
	}
	if r, _ := handover.RoleOf(ctx, gone.a, gone.a.Cfg.Installation.ID); r.Role != handover.RoleClient {
		t.Fatalf("a gone computer that comes back is a client: %+v", r)
	}
}

// The passphrase: a wrong one is refused, none given asks for it, and it is
// never in the log or the audit; without one the file is a plain archive.
func TestPassphraseRoundTrip(t *testing.T) {
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	mOld := old.at(t, remembers())
	talk(t, mOld, "", "Hello.")
	out := filepath.Join(t.TempDir(), "h.age")
	_, err := ops.CallTyped[handover.HandoverExportOut](handover.WithPassphrase(ctx, "short"), old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out}, person())
	refused(t, err, apperr.InvalidInput, "")
	_, err = ops.CallTyped[handover.HandoverExportOut](ctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out}, person())
	refused(t, err, apperr.InvalidInput, "")
	call[handover.HandoverExportOut](t, handover.WithPassphrase(ctx, pass), old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	// Never over a file that is there.
	_, err = ops.CallTyped[handover.HandoverExportOut](handover.WithPassphrase(ctx, pass), old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out}, person())
	refused(t, err, apperr.Conflict, "")

	nu.at(t, remembers())
	_, err = ops.CallTyped[handover.HandoverReceiveOut](handover.WithPassphrase(ctx, "not the passphrase at all"), nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out}, person())
	refused(t, err, apperr.Unauthorized, "wrong_passphrase")
	_, err = ops.CallTyped[handover.HandoverReceiveOut](ctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out}, person())
	refused(t, err, apperr.InvalidInput, "needs_passphrase")
	call[handover.HandoverReceiveOut](t, handover.WithPassphrase(ctx, pass), nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out})

	// Nowhere in what MetaRush keeps.
	old.a.Log.Flush(ctx)
	nu.a.Log.Flush(ctx)
	for _, coll := range []string{store.CLogEntries, store.CAudit, store.CHandovers, store.CAssistantConversations} {
		n, err := w.db.C(coll).CountDocuments(ctx, bson.M{"$expr": bson.M{"$regexMatch": bson.M{"input": bson.M{"$toString": "$$ROOT"}, "regex": "horse battery"}}})
		if err != nil {
			// $toString of a whole document isn't supported: read them instead.
			n = 0
			cur, ferr := w.db.C(coll).Find(ctx, bson.M{})
			if ferr != nil {
				t.Fatal(ferr)
			}
			var docs []bson.M
			_ = cur.All(ctx, &docs)
			for _, d := range docs {
				if b, _ := json.Marshal(d); strings.Contains(string(b), "horse battery") {
					n++
				}
			}
		}
		if n != 0 {
			t.Fatalf("the passphrase is in %s", coll)
		}
	}

	// Without a passphrase: a plain archive, and a plain warning.
	w2 := newWorkspace(t)
	o2, n2 := w2.computer(t, "laptop"), w2.computer(t, "desk")
	talk(t, o2.at(t, remembers()), "", "Hi.")
	plain := filepath.Join(t.TempDir(), "h.tar.gz")
	ex := call[handover.HandoverExportOut](t, ctx, o2.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: plain, NoPassphrase: true})
	if ex.Encrypted || len(ex.Warnings) == 0 || !strings.Contains(ex.Warnings[0], "anyone who gets it can read") {
		t.Fatalf("no passphrase %+v", ex)
	}
	f, _ := os.Open(plain)
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
	}
	f.Close()
	if !names["handover.json"] {
		t.Fatalf("archive %v", names)
	}
	n2.at(t, remembers())
	call[handover.HandoverReceiveOut](t, ctx, n2.a, "machine.handover.receive", handover.HandoverReceiveIn{File: plain})
}

// The file never goes where git would commit it or a sync service uploads it.
func TestHandoverFileStaysPrivate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	w := newWorkspace(t)
	old, _ := w.computer(t, "laptop"), w.computer(t, "desk")
	repo := t.TempDir()
	if out, err := exec.Command("git", "-C", repo, "-c", "maintenance.auto=false", "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	_, err := ops.CallTyped[handover.HandoverExportOut](handover.WithPassphrase(context.Background(), pass), old.a, "machine.handover.export",
		handover.HandoverExportIn{To: "desk", Out: filepath.Join(repo, "h.age")}, person())
	if err == nil {
		t.Fatal("a handover file was written into a repository")
	}
}

// People only and owners only: never Claude, Auto, an AI app, MCP, the
// review desk, a call without a person, or a member who isn't an owner.
func TestHandoverIsAPersonsAndAnOwners(t *testing.T) {
	w := newWorkspace(t)
	old, _ := w.computer(t, "laptop"), w.computer(t, "desk")
	a := old.a
	ctx := context.Background()
	all := map[string]any{
		"machine.handover.plan":    handover.HandoverPlanIn{To: "desk"},
		"machine.handover.status":  handover.HandoverStatusIn{},
		"machine.handover.export":  handover.HandoverExportIn{To: "desk", Out: filepath.Join(t.TempDir(), "x.age")},
		"machine.handover.receive": handover.HandoverReceiveIn{File: filepath.Join(t.TempDir(), "x.age")},
		"machine.handover.abandon": handover.HandoverAbandonIn{},
		"machine.handover.clean":   handover.HandoverCleanIn{},
		"machine.takeover":         handover.HandoverTakeoverIn{From: "desk"},
	}
	member := ops.OverHTTP(ctx, func() bool { return false })
	for name, in := range all {
		raw, _ := json.Marshal(in)
		op, _ := ops.Get(name)
		if !op.HumanOnly {
			t.Errorf("%s isn't people only", name)
		}
		if name != "machine.handover.status" {
			if !op.OwnerOnly {
				t.Errorf("%s isn't owners only", name)
			}
			_, err := ops.Call(member, a, name, raw, ops.Caller{Human: true, Remote: true})
			refused(t, err, apperr.Unauthorized, "owner_only")
		}
		if _, err := ops.Call(ctx, a, name, raw, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
			t.Errorf("%s without a person: %v", name, err)
		}
		for what, change := range map[string]func(b *app.App){
			"Claude": func(b *app.App) { b.Actor.Via = models.ViaAssistant },
			"Auto":   func(b *app.App) { b.Actor.Via = models.ViaAssistantAuto },
			"an app": func(b *app.App) { b.Actor.Agent = "codex" },
			"MCP":    func(b *app.App) { b.Actor.Via = "mcp" },
			"drafts": func(b *app.App) { b.ProposeOnly = true },
		} {
			b := *a
			change(&b)
			_, err := ops.Call(ctx, &b, name, raw, ops.Caller{Human: true})
			if c := apperr.From(err).Code; c != apperr.HumanOnly && c != apperr.Unauthorized {
				t.Errorf("%s by %s: %v", name, what, err)
			}
		}
		d := *a
		d.Actor.Agent = "codex"
		if _, err := ops.CallTyped[ops.DraftView](ctx, &d, "draft.save", ops.DraftSaveIn{Op: name, Input: raw}, ops.Caller{}); err == nil {
			t.Errorf("a review-desk draft holds %s", name)
		}
		if strings.HasSuffix(name, ".export") || strings.HasSuffix(name, ".receive") || strings.HasSuffix(name, ".clean") || name == "machine.takeover" {
			if !op.LocalOnly {
				t.Errorf("%s works over the network", name)
			}
		}
	}
}

// Abandoning puts the conversations back on the old computer.
func TestAbandonPutsConversationsBack(t *testing.T) {
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	m := old.at(t, remembers())
	id, _ := talk(t, m, "", "Hello.")
	out := filepath.Join(t.TempDir(), "h.age")
	pctx := handover.WithPassphrase(ctx, pass)
	call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	ab := call[handover.HandoverAbandonOut](t, ctx, old.a, "machine.handover.abandon", handover.HandoverAbandonIn{})
	if ab.Conversations != 1 || ab.Handover.State != handover.StateAbandoned {
		t.Fatalf("abandon %+v", ab)
	}
	if _, reply := talk(t, m, id, "Still here?"); reply != "I remember 1 messages." {
		t.Fatalf("it goes on here: %q", reply)
	}
	nu.at(t, remembers())
	_, err := ops.CallTyped[handover.HandoverReceiveOut](pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: out}, person())
	refused(t, err, apperr.Conflict, "abandoned")
}

// A computer that made a handover file and then went away is taken over from
// that file: its conversations go on from the sessions in it, and the
// handover it never finished ends.
func TestTakeOverFromAHandoverFile(t *testing.T) {
	w := newWorkspace(t)
	gone, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	id, _ := talk(t, gone.at(t, remembers()), "", "Plan the launch video.")
	pctx := handover.WithPassphrase(ctx, pass)
	out := filepath.Join(t.TempDir(), "h.age")
	ex := call[handover.HandoverExportOut](t, pctx, gone.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: out})
	// Then it went away for good.
	_, _ = nu.a.DB.C(store.CInstallations).UpdateOne(ctx, bson.M{"_id": gone.a.Cfg.Installation.ID}, mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "lastSeenAt", Value: bson.D{{Key: "$subtract", Value: bson.A{"$$NOW", int64(2 * time.Hour / time.Millisecond)}}}}}}}})
	m := nu.at(t, remembers())
	_, err := ops.CallTyped[handover.HandoverTakeoverOut](ctx, nu.a, "machine.takeover", handover.HandoverTakeoverIn{From: "laptop", Source: out, Plan: true}, person())
	refused(t, err, apperr.InvalidInput, "needs_passphrase")
	done := call[handover.HandoverTakeoverOut](t, pctx, nu.a, "machine.takeover", handover.HandoverTakeoverIn{From: "laptop", Source: out, ConfirmedOff: "laptop",
		Folders: []handover.HandoverFolderChoice{}})
	if done.Moved.Conversations != 1 || done.Moved.Summaries != 0 {
		t.Fatalf("take-over %+v", done.Moved)
	}
	if _, reply := talk(t, m, id, "Where were we?"); reply != "I remember 1 messages." {
		t.Fatalf("the session from the file goes on: %q", reply)
	}
	var r handover.HandoverRecord
	if err := nu.a.DB.GetByID(ctx, store.CHandovers, w.id, ex.Handover.ID, &r); err != nil || r.State != handover.StateAbandoned || r.Active {
		t.Fatalf("the unfinished handover: %+v %v", r, err)
	}
}
