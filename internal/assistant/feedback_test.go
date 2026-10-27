package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/version"
)

// sentOf is the exact message Claude was sent for turn n.
func sentOf(t *testing.T, m *Manager, id string, n int) string {
	t.Helper()
	c, err := m.Get(context.Background(), id, false, true)
	if err != nil || len(c.Turns) < n {
		t.Fatalf("turn %d of %s: %v", n, id, err)
	}
	return c.Turns[n-1].Sent
}

// A conversation keeps the project it started in (D185): a message typed on
// another project's page carries the conversation's project (its current
// version) with a one-line hint, and the page's selection only goes along
// when the page is that project. A whole-workspace conversation stays one.
func TestFeedbackConversationKeepsItsProject(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	ctx := context.Background()
	other, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Garden show"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	m := withFake(t, a, fakeclaude.Options{})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Tighten the opening", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	waitIdle(t, m, id)

	// Typed on the other project's page, with a clip selected there.
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "And the music?", ProjectID: other.Project.ID, ClipID: "intro"}}); err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, id)
	sent := cv.Turns[1].Sent
	if cv.ProjectID != projectID || cv.Turns[1].Context.ProjectID != projectID || !strings.Contains(sent, `Project "Assistant film"`) ||
		!strings.Contains(sent, "Current head of main: r1") || !strings.Contains(sent, `looking at another project, "Garden show"`) || strings.Contains(sent, "Selected clip") {
		t.Fatalf("a message from another page keeps the conversation's project: %s\n%s", cv.ProjectID, sent)
	}
	// On its own project's page the selection goes along.
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "This clip", ProjectID: projectID, ClipID: "hook"}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, id)
	if s := sentOf(t, m, id, 3); !strings.Contains(s, `Selected clip: "hook"`) || strings.Contains(s, "another project") {
		t.Fatalf("on the project's own page: %s", s)
	}
	// From a page with no project: still the project's context.
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "Anything else?"}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, id)
	if s := sentOf(t, m, id, 4); !strings.Contains(s, `Project "Assistant film"`) || strings.Contains(s, "whole workspace") {
		t.Fatalf("from a page without a project: %s", s)
	}
	if l, _ := m.List(ctx, ConversationListIn{ProjectID: projectID}); len(l.Conversations) != 1 {
		t.Fatalf("it stays in its project's list: %+v", l)
	}

	// A whole-workspace conversation stays one; the page is a hint.
	out, err = m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "What's in my library?"}})
	if err != nil {
		t.Fatal(err)
	}
	ws := out.Conversation.ID
	waitIdle(t, m, ws)
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: ws, AssistantMessageIn: AssistantMessageIn{Prompt: "And here?", ProjectID: other.Project.ID}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, ws)
	if s := cv.Turns[1].Sent; cv.ProjectID != "" || !strings.Contains(s, "about the whole workspace") || !strings.Contains(s, `The person is looking at project "Garden show"`) ||
		strings.Contains(s, "Current head of main") {
		t.Fatalf("a workspace conversation stays workspace-level: %q\n%s", cv.ProjectID, s)
	}
}

// A conversation that starts in a project hears what happened there (D185):
// versions with their notes and approvals, renders, earlier conversations and
// what was decided on their cards, open cards; bounded; readable by Claude
// again through the op.
func TestFeedbackProjectSoFar(t *testing.T) {
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	ctx := context.Background()
	if _, err := ops.Call(ctx, a, "revision.approve", mustJSON(t, ops.RevisionApproveIn{ID: head, Reject: true, Note: "The opening drags"}), ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: projectID, RevisionID: head, RevisionNumber: 1, Quality: "review",
		State: models.RenderAvailable, CreatedAt: now, QA: &models.RenderQA{Verdict: "warn", Warnings: 2, At: now}}
	if err := a.DB.Insert(ctx, store.CRenders, rd); err != nil {
		t.Fatal(err)
	}
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "two notes", Steps: []fakeclaude.Step{denied(`metarush comment add "A slower opening" --project ` + projectID + ` --json`),
			denied(`metarush comment add "Brighter music" --project ` + projectID + ` --json`), text("Two notes for you.")}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Leave two notes", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	first := out.Conversation.ID
	cv := waitIdle(t, m, first)
	if len(cv.Actions) != 2 {
		t.Fatalf("two cards: %+v", cv.Actions)
	}
	if _, err := m.Act(ctx, a, ops.Caller{Human: true}, ConversationActIn{ID: first, ActionID: cv.Actions[0].ID, Dismiss: true}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, first)
	if _, err := m.Act(ctx, a, ops.Caller{Human: true}, ConversationActIn{ID: first, ActionID: cv.Actions[1].ID, NotQuite: "Only in the second half"}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, first)

	// Through the op, as Claude reads it again.
	sf, err := ops.CallTyped[ProjectSoFarOut](ctx, a, "project.sofar", ProjectSoFarIn{ProjectID: projectID}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sf.Versions) != 1 || sf.Versions[0].Approval != models.ApprovalRejected || sf.Versions[0].ApprovalNote != "The opening drags" ||
		len(sf.Renders) != 1 || sf.Renders[0].QA == "" || len(sf.Conversations) != 1 || len(sf.Conversations[0].Cards) != 2 {
		t.Fatalf("the record: %+v", sf)
	}
	for _, want := range []string{"version 1 (" + head + ")", "needs changes by Tester: “The opening drags”", "review copy of version 1",
		"2 things worth checking", "not quite: Add a note to “Assistant film” (Tester: “Only in the second half”)", "not now: Add a note"} {
		if !strings.Contains(sf.Text, want) {
			t.Errorf("the record lacks %q:\n%s", want, sf.Text)
		}
	}
	// A new conversation in the project hears it with its first message;
	// the next message doesn't repeat it.
	out, err = m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Where are we?", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, out.Conversation.ID)
	if s := sentOf(t, m, out.Conversation.ID, 1); !strings.Contains(s, "The project so far") || !strings.Contains(s, "Leave two notes") ||
		!strings.Contains(s, "metarush project so-far "+projectID) {
		t.Fatalf("the first message carries the project so far:\n%s", s)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: out.Conversation.ID, AssistantMessageIn: AssistantMessageIn{Prompt: "ok", ProjectID: projectID}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, out.Conversation.ID)
	if s := sentOf(t, m, out.Conversation.ID, 2); strings.Contains(s, "The project so far") {
		t.Fatalf("only the first message: %s", s)
	}
	// A workspace conversation has none.
	out, err = m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Hello"}})
	if err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, out.Conversation.ID)
	if s := sentOf(t, m, out.Conversation.ID, 1); strings.Contains(s, "The project so far") {
		t.Fatalf("a workspace conversation: %s", s)
	}
	if !AllowsCommand("metarush project so-far") {
		t.Fatal("Claude may read the project so far")
	}
}

// The record stays short however much happened: oldest first out, and says so.
func TestFeedbackProjectSoFarIsBounded(t *testing.T) {
	o := &ProjectSoFarOut{}
	now := time.Now()
	long := strings.Repeat("a change that took many words to describe ", 8)
	for i := 16; i >= 1; i-- {
		o.Versions = append(o.Versions, SoFarVersion{RevisionID: ids.New(ids.Revision), Number: i, By: "Maya", At: now, Note: long, Approval: models.ApprovalRejected, ApprovalNote: long})
	}
	for i := 0; i < 10; i++ {
		var cards []SoFarCard
		for j := 0; j < 12; j++ {
			cards = append(cards, SoFarCard{Title: "Save the cut with the tighter opening", State: "not_quite", By: "Theo", Note: long})
		}
		o.Conversations = append(o.Conversations, SoFarConversation{Title: fmt.Sprintf("Conversation %d", i), StartedBy: "Theo", At: now, Cards: cards})
	}
	text, more := soFarLines(o, now)
	if len(text) > soFarMaxChars+80 || !more || !strings.Contains(text, "version 16") || !strings.Contains(text, "left out") {
		t.Fatalf("bounded (%d chars, more=%v):\n%s", len(text), more, text)
	}
	if strings.Contains(text, "version 1 (") {
		t.Fatal("the oldest version goes first")
	}
}

// After an update, the next message tells Claude what's new, once (D185).
func TestFeedbackWhatsNewOncePerUpgrade(t *testing.T) {
	notes := version.ParseChangelog("# Changelog\n\n## Unreleased\n- **Cards say when** (D9). Times.\n\n## 0.3.0 — 2026-10-21\n- **Auto runs changes that can be undone** (D8). More.\n- For developers: x.\n\n## 0.2.0 — 2026-10-20\n- **Old thing** (D1).\n")
	if got := whatsNewHeadlines(notes, "0.2.0", "0.3.0"); len(got) != 1 || got[0] != "Auto runs changes that can be undone" {
		t.Fatalf("0.2.0 → 0.3.0: %q", got)
	}
	if got := whatsNewHeadlines(notes, "0.2.0", "0.3.0-dev.4"); len(got) != 2 || got[0] != "Cards say when" {
		t.Fatalf("0.2.0 → a build past 0.3.0: %q", got)
	}
	if got := whatsNewHeadlines(notes, "0.3.0-dev.1", "0.3.0-dev.4"); len(got) != 0 {
		t.Fatalf("between builds past the same release: %q", got)
	}
	if n := whatsNewNote(notes, "0.2.0", "0.3.0"); !strings.Contains(n, "MetaRush was updated from 0.2.0 to 0.3.0. New things you can do: Auto runs changes that can be undone.") ||
		!strings.Contains(n, "check again") {
		t.Fatalf("the note: %s", n)
	}

	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	ctx := context.Background()
	m := withFake(t, a, fakeclaude.Options{})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Hi", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	if cv.MetaRushVersion != version.Current().Version || strings.Contains(cv.Turns[0].Sent, "was updated") {
		t.Fatalf("the version is recorded, nothing to tell: %q", cv.MetaRushVersion)
	}
	c, _ := m.live(ctx, id, false)
	c.mu.Lock()
	c.data.MetaRushVersion = "0.1.0" // its last message ran on an older MetaRush
	c.mu.Unlock()
	for i := 2; i <= 3; i++ {
		if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "Again", ProjectID: projectID}}); err != nil {
			t.Fatal(err)
		}
		waitIdle(t, m, id)
	}
	if s := sentOf(t, m, id, 2); !strings.Contains(s, "MetaRush was updated from 0.1.0 to "+version.Current().Version) {
		t.Fatalf("the first message after the update says so:\n%s", s)
	}
	if s := sentOf(t, m, id, 3); strings.Contains(s, "was updated") {
		t.Fatalf("once per update: %s", s)
	}
	var doc struct {
		V string `bson:"metarushVersion"`
	}
	_ = a.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": id}).Decode(&doc)
	if doc.V != version.Current().Version {
		t.Fatalf("stored: %q", doc.V)
	}
}

// "Not quite…" sends a card back with the person's note, as their message
// (D186); "Not now" asks Claude to wait, not to drop it.
func TestFeedbackNotQuiteAndNotNow(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	ctx := context.Background()
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "notes", Steps: []fakeclaude.Step{denied(`metarush comment add "A slower opening" --project ` + projectID + ` --json`),
			denied(`metarush comment add "Brighter music" --project ` + projectID + ` --json`),
			{Write: &fakeclaude.File{Name: "music.question.json", Content: `{"question":"Which music?","choices":["Calm","Bright"]}`}},
			text("Two notes and a question.")}},
		{Match: "Not quite:", Steps: []fakeclaude.Step{text("Here's a new version.")}},
		{Match: "Not now:", Steps: []fakeclaude.Step{text("I'll leave it.")}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Leave notes", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	slow := cardBy(cv, func(x AssistantAction) bool { return strings.Contains(x.Input, "slower") })
	bright := cardBy(cv, func(x AssistantAction) bool { return strings.Contains(x.Input, "Brighter") })
	question := cardBy(cv, func(x AssistantAction) bool { return x.Question != nil })
	for _, bad := range []ConversationActIn{
		{ID: id, ActionID: slow.ID, NotQuite: "   "},
		{ID: id, ActionID: question.ID, NotQuite: "Neither"},
		{ID: id, ActionID: slow.ID, NotQuite: "x", Dismiss: true},
	} {
		if _, err := m.Act(ctx, a, ops.Caller{Human: true}, bad); err == nil {
			t.Fatalf("refused: %+v", bad)
		}
	}
	maya := *a
	maya.Actor = models.Actor{UserID: ids.New(ids.User), UserName: "Maya", InstallationID: a.Cfg.Installation.ID, Via: "dashboard"}
	res, err := ops.CallTyped[ConversationActOut](ctx, &maya, "assistant.act", ConversationActIn{ID: id, ActionID: slow.ID, NotQuite: "Only the first ten seconds"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if x := res.Action; x.State != ActionNotQuite || x.Note != "Only the first ten seconds" || x.ByID != maya.Actor.UserID || x.DecidedAt == nil {
		t.Fatalf("the card is not quite, with who, when and the note: %+v", x)
	}
	cv = waitIdle(t, m, id)
	nq := cv.Turns[1]
	if nq.Note || nq.UserID != maya.Actor.UserID || nq.Prompt != "Not quite: Add a note to “Assistant film”. Only the first ten seconds. Make a new version with that change." ||
		nq.Shown != "Not quite: Add a note to “Assistant film”. Only the first ten seconds." {
		t.Fatalf("the note went at once, as Maya's message: %+v", nq)
	}
	if _, err := m.Act(ctx, a, ops.Caller{Human: true}, ConversationActIn{ID: id, ActionID: slow.ID, NotQuite: "again"}); code(err) != apperr.Conflict {
		t.Fatalf("decided already: %v", err)
	}
	// Not now: wait, not never.
	if _, err := m.Act(ctx, a, ops.Caller{Human: true}, ConversationActIn{ID: id, ActionID: bright.ID, Dismiss: true}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, id)
	last := cv.Turns[len(cv.Turns)-1]
	if !last.Note || !strings.Contains(last.Prompt, "Not now: Add a note to “Assistant film”. Leave it for now; I may come back to it.") || strings.Contains(last.Prompt, "Don't ask") {
		t.Fatalf("Not now asks Claude to wait: %q", last.Prompt)
	}
}

// A version or graphic turned down with a note: the note reaches the
// conversation that made it as the person's message (D186), from the
// dashboard at once, from a terminal through the conversation's inbox.
func TestFeedbackTurnedDownNotesReachTheConversation(t *testing.T) {
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	ctx := context.Background()
	plan := fmt.Sprintf(`{"baseRevisionId":%q,"instruction":"shorter b","ops":[{"op":"updateClip","id":"broll","set":{"out":11}}]}`, head)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "shorter", Steps: []fakeclaude.Step{{Write: &fakeclaude.File{Name: "shorter.plan.json", Content: plan}},
			denied("metarush edit apply --plan {{proposals}}/shorter.plan.json --json"), text("A shorter b to save.")}},
		{Match: "I turned down", Steps: []fakeclaude.Step{text("I'll make another.")}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "make b shorter", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	done, err := m.Act(ctx, a, ops.Caller{Human: true}, ConversationActIn{ID: id, ActionID: cv.Actions[0].ID})
	if err != nil || done.Action.State != ActionDone {
		t.Fatalf("saved: %+v %v", done, err)
	}
	cv = waitIdle(t, m, id)
	var v2 models.Revision
	if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"projectId": projectID, "number": 2}).Decode(&v2); err != nil {
		t.Fatal(err)
	}
	turns := len(cv.Turns)
	rej, err := ops.CallTyped[ops.RevisionSummary](ctx, a, "revision.approve", ops.RevisionApproveIn{ID: v2.ID, Reject: true, Note: "Too abrupt at the end"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if rej.ToldClaude == nil || rej.ToldClaude.ConversationID != id {
		t.Fatalf("the note went to the conversation that made it: %+v", rej.ToldClaude)
	}
	cv = waitIdle(t, m, id)
	if len(cv.Turns) != turns+1 {
		t.Fatalf("one message: %d turns", len(cv.Turns))
	}
	got := cv.Turns[turns]
	if got.Note || got.Shown != "I turned down version 2: Too abrupt at the end" || !strings.Contains(got.Prompt, v2.ID) || got.UserID != a.Cfg.User.ID {
		t.Fatalf("as the person's message: %+v", got)
	}

	// Claude's own "Reject version 2" card, run by a person: Claude hears the
	// card's outcome, not its own note back.
	quiet, err := ops.CallTyped[ops.RevisionSummary](ops.QuietTurnedDown(ctx), a, "revision.approve", ops.RevisionApproveIn{ID: v2.ID, Reject: true, Note: "Claude's own note"}, ops.Caller{Human: true})
	if err != nil || quiet.ToldClaude != nil {
		t.Fatalf("a card's own note isn't sent back: %+v %v", quiet.ToldClaude, err)
	}

	// From a terminal command (it never holds the conversation): the inbox, then
	// the daemon's MetaRush sends it.
	home := a.Cfg.Home()
	term := NewTerminal(a, "/usr/local/bin/metarush-test")
	rej, err = ops.CallTyped[ops.RevisionSummary](ctx, a, "revision.approve", ops.RevisionApproveIn{ID: v2.ID, Reject: true, Note: "Still abrupt"}, ops.Caller{Human: true})
	term.Close()
	managers.Lock()
	managers.m[home] = m
	managers.Unlock()
	if err != nil || rej.ToldClaude == nil || !rej.ToldClaude.Queued || rej.ToldClaude.Machine != "test-machine" {
		t.Fatalf("waiting for the holding machine: %+v %v", rej.ToldClaude, err)
	}
	var doc struct {
		Inbox []InboxNote `bson:"inbox"`
	}
	// The note waits in the inbox; the daemon's own loop (every few seconds) or
	// this call sends it — whichever comes first, it goes exactly once.
	_ = a.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": id}).Decode(&doc)
	if len(doc.Inbox) > 1 || (len(doc.Inbox) == 1 && doc.Inbox[0].Shown != "I turned down version 2: Still abrupt") {
		t.Fatalf("the inbox: %+v", doc.Inbox)
	}
	if n := m.deliverInbox(ctx); n > 1 {
		t.Fatalf("delivered: %d", n)
	}
	cv = waitIdle(t, m, id)
	if s := cv.Turns[len(cv.Turns)-1].Shown; s != "I turned down version 2: Still abrupt" {
		t.Fatalf("sent from the inbox: %q", s)
	}
	sent := 0
	for _, tu := range cv.Turns {
		if tu.Shown == "I turned down version 2: Still abrupt" {
			sent++
		}
	}
	_ = a.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": id}).Decode(&doc)
	if len(doc.Inbox) != 0 || sent != 1 {
		t.Fatalf("taken once: inbox %+v, sent %d times", doc.Inbox, sent)
	}

	// Nobody made it: no conversation, the dashboard offers to send it.
	rej, err = ops.CallTyped[ops.RevisionSummary](ctx, a, "revision.approve", ops.RevisionApproveIn{ID: head, Reject: true, Note: "Not this one"}, ops.Caller{Human: true})
	if err != nil || rej.ToldClaude != nil {
		t.Fatalf("version 1 was made by hand: %+v %v", rej.ToldClaude, err)
	}

	// A graphic's turn-down finds the conversation whose card checked it.
	hash := strings.Repeat("ab", 32)
	c, _ := m.live(ctx, id, false)
	c.mu.Lock()
	c.data.Actions = append(c.data.Actions, AssistantAction{ID: "act_motion", Turn: 1, Op: "motion.check", State: ActionDone, Link: "/motion/" + hash, CreatedAt: time.Now().UTC()})
	c.mu.Unlock()
	m.save(c, true)
	if got := originConversation(ctx, a, ops.TurnedDown{MotionHash: hash, MotionName: "lower-bar"}); got != id {
		t.Fatalf("the graphic's conversation: %q", got)
	}
}

// Auto runs every card that can be undone by itself, as the person it is for
// (D186); it still asks for money, final renders, approvals, setup, questions
// and keep cards; the computer's master switch turns it off.
func TestFeedbackAutoRunsWhatCanBeUndone(t *testing.T) {
	a := testApp(t, "")
	on := true
	a.Cfg.Assistant.SafeSteps = &on
	projectID, _, head := fixture(t, a)
	ctx := context.Background()
	if err := a.DB.Insert(ctx, store.CUsers, models.User{ID: a.Cfg.User.ID, WorkspaceID: a.WorkspaceID(), Name: "Tester", Email: "tester@example.com", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	plan := fmt.Sprintf(`{"baseRevisionId":%q,"instruction":"shorter b","ops":[{"op":"updateClip","id":"broll","set":{"out":11}}]}`, head)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "everything", Steps: []fakeclaude.Step{
			{Write: &fakeclaude.File{Name: "shorter.plan.json", Content: plan}},
			denied("metarush edit apply --plan {{proposals}}/shorter.plan.json --json"),
			denied(`metarush comment add "A slower opening" --project ` + projectID + ` --json`),
			denied("metarush render submit --revision {{head}} --quality final --json"),
			denied("metarush revision approve {{head}}"),
			denied(`metarush keep rule 'First names only' --project ` + projectID),
			{Write: &fakeclaude.File{Name: "music.question.json", Content: `{"question":"Which music?","choices":["Calm","Bright"]}`}},
			text("Lots of cards.")}},
		{Match: "Done by itself", Steps: []fakeclaude.Step{text("Noted.")}},
	}}})
	m.workEvery, m.tellAfter = 200*time.Millisecond, 200*time.Millisecond

	// Ask first is the default; the person chooses Auto (people only).
	mo, err := ops.CallTyped[AssistantModeOut](ctx, a, "assistant.mode", AssistantModeIn{}, ops.Caller{})
	if err != nil || mo.Mode != ModeAsk || !mo.Offered {
		t.Fatalf("ask first by default: %+v %v", mo, err)
	}
	if _, err := ops.Call(ctx, a, "assistant.mode.set", mustJSON(t, AssistantModeSetIn{Mode: "auto"}), ops.Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("people only: %v", err)
	}
	if mo, err = ops.CallTyped[AssistantModeOut](ctx, a, "assistant.mode.set", AssistantModeSetIn{Mode: "auto"}, ops.Caller{Human: true}); err != nil || mo.Mode != ModeAuto {
		t.Fatalf("auto: %+v %v", mo, err)
	}

	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Do everything", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	var cv *Conversation
	eventually(t, "Auto to run what it may", func() bool {
		c, _ := m.Get(ctx, id, false, false)
		cv = c
		save := cardBy(c, func(x AssistantAction) bool { return x.Op == "edit.apply" })
		note := cardBy(c, func(x AssistantAction) bool { return x.Op == "comment.add" })
		return c.State == ConvIdle && save.State == ActionDone && note.State == ActionDone
	})
	save := cardBy(cv, func(x AssistantAction) bool { return x.Op == "edit.apply" })
	note := cardBy(cv, func(x AssistantAction) bool { return x.Op == "comment.add" })
	for name, x := range map[string]AssistantAction{"the save": save, "the note": note} {
		if !x.Auto || !x.AutoMode || x.State != ActionDone {
			t.Fatalf("%s ran by itself under Auto: %+v", name, x)
		}
	}
	if save.Work == nil || save.Work.Kind != WorkRender || !strings.Contains(save.Result, "Saved as version 2") {
		t.Fatalf("a save renders its review copy: %+v %+v", save.Result, save.Work)
	}
	var v2 models.Revision
	if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"projectId": projectID, "number": 2}).Decode(&v2); err != nil || v2.CreatedBy.Via != models.ViaAssistantAuto ||
		v2.CreatedBy.UserID != a.Cfg.User.ID {
		t.Fatalf("saved as the person, by itself: %+v %v", v2.CreatedBy, err)
	}
	for name, f := range map[string]func(AssistantAction) bool{
		"the final render": func(x AssistantAction) bool { return x.Op == "render.submit" },
		"the approval":     func(x AssistantAction) bool { return x.Op == "revision.approve" },
		"the keep card":    func(x AssistantAction) bool { return x.Keeping != nil },
		"the question":     func(x AssistantAction) bool { return x.Question != nil },
	} {
		if x := cardBy(cv, f); x.ID == "" || x.Auto || x.State != ActionPending {
			t.Fatalf("%s asks: %+v", name, x)
		}
	}
	// What it never runs, whatever the person chose.
	c, _ := m.live(ctx, id, false)
	for name, x := range map[string]*AssistantAction{
		"a cloud voice":    {Op: "sound.enhance", Input: `{"revisionId":"` + head + `","target":"Sam","mode":"cloud"}`, State: ActionPending},
		"a studio setup":   {Op: "sound.enhance", Input: `{"revisionId":"` + head + `","target":"Sam","mode":"studio"}`, SetUp: []string{"studio"}, State: ActionPending},
		"a generated shot": {Op: "generate.shot", Input: `{}`, State: ActionPending},
		"deleting footage": {Op: "asset.delete", Input: `{}`, State: ActionPending},
		"people only":      {Op: "motion.approve", Input: `{}`, HumanOnly: true, State: ActionPending},
	} {
		if c.autoStep(x) {
			t.Errorf("Auto runs %s", name)
		}
	}
	if !c.autoStep(&AssistantAction{Op: "sound.enhance", Input: `{"revisionId":"` + head + `","target":"Sam","mode":"clean"}`, State: ActionPending}) {
		t.Error("a clean voice on this computer is free and can be undone")
	}
	// The master switch off: nothing runs by itself and Auto isn't offered.
	off := false
	a.Cfg.Assistant.SafeSteps = &off
	if mo, _ := m.Mode(ctx); mo.Offered || mo.Mode != ModeAsk {
		t.Fatalf("not offered: %+v", mo)
	}
	if _, err := ops.Call(ctx, a, "assistant.mode.set", mustJSON(t, AssistantModeSetIn{Mode: "auto"}), ops.Caller{Human: true}); code(err) != apperr.Conflict {
		t.Fatalf("refused while off: %v", err)
	}
	x := &AssistantAction{ID: "act_x", Turn: 1, Op: "comment.add", Input: `{"projectId":"` + projectID + `","body":"x"}`, State: ActionPending}
	if m.startAuto(c, x) || x.State != ActionPending {
		t.Fatal("with the master switch off, Auto waits for a click")
	}
	a.Cfg.Assistant.SafeSteps = &on
	if !m.startAuto(c, x) || !x.AutoMode {
		t.Fatal("back on, the person's Auto runs it")
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
