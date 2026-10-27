package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
)

// actIn runs assistant.act from the dashboard as a person.
func actIn(t *testing.T, m *Manager, in ConversationActIn) (*ConversationActOut, error) {
	t.Helper()
	dash := *m.App
	dash.Actor.Via = "dashboard"
	b, _ := json.Marshal(in)
	res, err := ops.Call(context.Background(), &dash, "assistant.act", b, ops.Caller{Human: true, Remote: true})
	if err != nil {
		return nil, err
	}
	return res.(*ConversationActOut), nil
}

func cardBy(cv *Conversation, f func(AssistantAction) bool) AssistantAction {
	for _, a := range cv.Actions {
		if f(a) {
			return a
		}
	}
	return AssistantAction{}
}

func turnsOf(t *testing.T, m *Manager, id string, n int) *Conversation {
	t.Helper()
	eventually(t, "the conversation's turns", func() bool {
		c, _ := m.Get(context.Background(), id, false, false)
		return c != nil && len(c.Turns) >= n && TurnTerminal(c.Turns[len(c.Turns)-1].State) && c.State == ConvIdle && len(c.Queue) == 0
	})
	return waitIdle(t, m, id)
}

// With the person's setting on (the default), safe steps start by themselves
// (D141): a review render, a snapshot of a page the person typed, a video
// import from a link the person typed. A final render, a page Claude found
// itself and an approval still wait for a click. Each runs as the person,
// marked as started by itself in the audit and the activity log; Claude hears
// once, when their work has finished, in one note (D142).
func TestSafeStepsRunByThemselves(t *testing.T) {
	a := testApp(t, "")
	on := true
	a.Cfg.Assistant.SafeSteps = &on
	projectID, _, head := fixture(t, a)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "acme.example", Steps: []fakeclaude.Step{
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush render submit --revision {{head}} --quality review --json"}}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush render submit --revision {{head}} --quality final --json"}}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush resources add https://www.acme.example/ --project " + projectID + " --json"}}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush resources add https://acme.example/donate --project " + projectID + " --json"}}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush import 'https://www.youtube.com/watch?v=aqz-KE-bpKQ' --project " + projectID + " --json"}}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush revision approve {{head}}"}}},
			text("I asked for a review copy, the website and the video."),
		}},
		{Match: "Done by itself", Steps: []fakeclaude.Step{text("All three are in.")}},
	}}})
	// The window must hold all three outcomes even on a busy machine (the jobs
	// are marked done one database call at a time).
	m.workEvery, m.tellAfter = 300*time.Millisecond, 10*time.Second
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Render it, take their logo from acme.example and bring in https://www.youtube.com/watch?v=aqz-KE-bpKQ", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	var cv *Conversation
	if c := waitIdle(t, m, id); len(c.Actions) != 6 {
		for _, x := range c.Actions {
			t.Logf("card %s %s auto=%v %q %q", x.Op, x.State, x.Auto, x.Result, x.Error)
		}
		for _, e := range c.Events {
			t.Logf("event %s %q", e.Kind, e.Text)
		}
		t.Fatalf("six cards: %d", len(c.Actions))
	}
	eventually(t, "the safe steps to start", func() bool {
		c, _ := m.Get(ctx, id, true, false)
		cv = c
		started := 0
		for _, x := range c.Actions {
			if x.Auto && x.State == ActionDone {
				started++
			}
		}
		if started < 3 && len(c.Actions) == 6 && c.State == ConvIdle {
			for _, x := range c.Actions {
				t.Logf("card %s %s auto=%v %q %q", x.Op, x.State, x.Auto, x.Result, x.Error)
			}
		}
		return c.State == ConvIdle && len(c.Actions) == 6 && started == 3
	})
	review := cardBy(cv, func(x AssistantAction) bool { return x.Op == "render.submit" && strings.Contains(x.Title, "review") })
	final := cardBy(cv, func(x AssistantAction) bool { return x.Op == "render.submit" && strings.Contains(x.Title, "final") })
	page := cardBy(cv, func(x AssistantAction) bool {
		return x.Op == "resources.fetch" && strings.Contains(x.Input, "www.acme.example")
	})
	found := cardBy(cv, func(x AssistantAction) bool { return x.Op == "resources.fetch" && strings.Contains(x.Input, "donate") })
	video := cardBy(cv, func(x AssistantAction) bool { return x.Op == "ingest.url" })
	approve := cardBy(cv, func(x AssistantAction) bool { return x.Op == "revision.approve" })
	for name, c := range map[string]AssistantAction{"review render": review, "typed page": page, "typed video": video} {
		if !c.Auto || c.State != ActionDone || c.Work == nil || c.Work.State != WorkRunning {
			t.Fatalf("%s starts by itself and is watched: %+v", name, c)
		}
	}
	for name, c := range map[string]AssistantAction{"final render": final, "a page Claude found": found, "approval": approve} {
		if c.Auto || c.State != ActionPending {
			t.Fatalf("%s waits for a click: %+v", name, c)
		}
	}
	if cv.PendingActions != 3 {
		t.Fatalf("three cards wait for the person: %d", cv.PendingActions)
	}
	// It ran as the person, marked as started by itself.
	var rd models.Render
	if err := a.DB.C(store.CRenders).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "revisionId": head, "quality": "review"}).Decode(&rd); err != nil ||
		rd.CreatedBy.Via != models.ViaAssistantAuto || rd.CreatedBy.UserID != a.Cfg.User.ID {
		t.Fatalf("the render is the person's, started by itself: %+v %v", rd.CreatedBy, err)
	}
	// runAuto marks a card done before it writes the audit: on a busy machine the
	// last audit lands a moment after the third card says done.
	var audits []models.AuditEvent
	eventually(t, "the audit of the safe steps", func() bool {
		audits = nil
		_ = a.DB.Find(ctx, store.CAudit, a.WorkspaceID(), bson.M{"action": "assistant.action", "details.auto": true}, &audits)
		return len(audits) >= 3
	})
	if len(audits) != 3 || audits[0].Details["setting"] != "assistant.safeSteps" {
		t.Fatalf("the audit says the setting started them: %+v", audits)
	}
	var logged []logbook.Entry
	eventually(t, "the activity log", func() bool {
		logged = logEntries(t, ctx, m, bson.M{"kind": logbook.KindAction, "action.auto": true})
		return len(logged) == 3
	})
	if text := logbook.Describe(&logged[0], nil, ""); !strings.Contains(text, "by itself, as your setting for safe steps allows") {
		t.Fatalf("the log says so in plain words: %q", text)
	}
	// Nothing wakes Claude until the work is done; then one note for all three.
	time.Sleep(500 * time.Millisecond)
	if cv = waitIdle(t, m, id); len(cv.Turns) != 1 {
		t.Fatalf("Claude was woken when the work only started: %+v", cv.Turns)
	}
	finishRender(t, a, rd.ID, models.QAPass, 0)
	now := time.Now().UTC()
	for _, c := range []AssistantAction{page, video} {
		var res bson.M
		if c.Op == "resources.fetch" {
			res = bson.M{"resourceIds": bson.A{"res_01jabcdefghjkmnpqrstvwxyz0"}, "names": bson.A{"fernbrook-example"}, "message": "A snapshot of fernbrook.example."}
		} else {
			res = bson.M{"assetId": "ast_01jabcdefghjkmnpqrstvwxyz9", "title": "Big Buck Bunny"}
		}
		for _, jid := range c.Work.JobIDs {
			if err := a.DB.Update(ctx, store.CJobs, a.WorkspaceID(), jid, bson.M{"$set": bson.M{"state": models.JobSucceeded, "finishedAt": now, "result": res}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	cv = turnsOf(t, m, id, 2)
	if len(cv.Turns) != 2 || !cv.Turns[1].Note {
		t.Fatalf("one note for the three: %+v", cv.Turns)
	}
	shown := strings.Split(cv.Turns[1].Shown, "\n")
	if len(shown) != 3 || !strings.Contains(cv.Turns[1].Shown, "The review copy of version 1 is ready (5 s). The quality check found nothing wrong.") ||
		!strings.Contains(cv.Turns[1].Shown, "“fernbrook-example” is in the resources") || !strings.Contains(cv.Turns[1].Shown, "“Big Buck Bunny” is in") {
		t.Fatalf("the person's lines: %q", cv.Turns[1].Shown)
	}
	for _, l := range shown {
		if fs := plainwords.CheckPanel(l); len(fs) > 0 {
			t.Errorf("%q shows %v", l, fs)
		}
	}
	if p := cv.Turns[1].Prompt; !strings.Contains(p, rd.ID) || !strings.Contains(p, "fernbrook-example") || strings.Count(p, "Done by itself") != 3 {
		t.Fatalf("Claude's note: %q", p)
	}

	// The setting off: safe steps wait like the rest.
	off := false
	a.Cfg.Assistant.SafeSteps = &off
	c, _ := m.live(ctx, id, false)
	x := &AssistantAction{ID: "act_x", Op: "render.submit", Input: `{"revisionId":"` + head + `","quality":"review"}`, State: ActionPending}
	if m.startAuto(c, x) || x.State != ActionPending {
		t.Fatal("with the setting off, a review render waits for a click")
	}
}

// Claude asks with a question file; the card's answer goes to Claude as the
// person's reply, with the question (D143). Question cards count as waiting.
func TestQuestionCards(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	q := `{"question": "Which of these shorts should I set up?", "choices": ["The garden in spring", "Building the shed", "Lessons learned", "Lessons learned"],
	  "multiple": true, "other": true, "why": "Each becomes its own vertical short."}`
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "shorts", Steps: []fakeclaude.Step{
			{Write: &fakeclaude.File{Name: "shorts.question.json", Content: q}},
			{Write: &fakeclaude.File{Name: "broken.question.json", Content: `{"choices": ["a", "b"]}`}},
			{Write: &fakeclaude.File{Name: "keep.question.json", Content: `{"question": "Keep the listening shots?", "choices": ["Keep them", "Cover them"]}`}},
			text("Two questions for you."),
		}},
		{Match: "This message answers your question card", Steps: []fakeclaude.Step{text("Setting those up.")}},
	}}})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Make some shorts", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	var cv *Conversation
	eventually(t, "the question cards", func() bool {
		c, _ := m.Get(ctx, id, true, false)
		cv = c
		return len(c.Actions) == 2
	})
	shorts := cardBy(cv, func(x AssistantAction) bool { return strings.HasPrefix(x.Title, "Which") })
	keep := cardBy(cv, func(x AssistantAction) bool { return strings.HasPrefix(x.Title, "Keep") })
	if shorts.Question == nil || len(shorts.Question.Choices) != 3 || !shorts.Question.Multiple || !shorts.Question.Other || shorts.Question.Why == "" ||
		shorts.Kind != "question" || shorts.State != ActionPending || keep.Question == nil || keep.Question.Other || len(keep.Question.Choices) != 2 {
		t.Fatalf("question cards: %+v %+v", shorts.Question, keep.Question)
	}
	if cv.PendingActions != 2 {
		t.Fatalf("questions count as waiting: %d", cv.PendingActions)
	}
	notice := false
	for _, e := range cv.Events {
		notice = notice || (e.Kind == "notice" && strings.Contains(e.Text, "it has no question"))
	}
	if !notice {
		t.Fatal("a question file without a question says so")
	}
	for _, bad := range []QuestionAnswer{{}, {Choices: []int{4}}, {Text: strings.Repeat("x", 2001)}} {
		if _, err := actIn(t, m, ConversationActIn{ID: id, ActionID: shorts.ID, Answer: &bad}); code(err) != apperr.InvalidInput {
			t.Fatalf("answer %+v: %v", bad, err)
		}
	}
	if _, err := actIn(t, m, ConversationActIn{ID: id, ActionID: keep.ID, Answer: &QuestionAnswer{Choices: []int{1, 2}}}); code(err) != apperr.InvalidInput {
		t.Fatalf("one answer only: %v", err)
	}
	if _, err := actIn(t, m, ConversationActIn{ID: id, ActionID: keep.ID, Answer: &QuestionAnswer{Text: "something else"}}); code(err) != apperr.InvalidInput {
		t.Fatalf("no answer of their own here: %v", err)
	}
	if _, err := actIn(t, m, ConversationActIn{ID: id, ActionID: shorts.ID}); code(err) != apperr.Conflict {
		t.Fatalf("a question isn't run: %v", err)
	}
	res, err := actIn(t, m, ConversationActIn{ID: id, ActionID: shorts.ID, Answer: &QuestionAnswer{Choices: []int{3, 1}, Text: "Make them 45 seconds."}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Action.State != ActionDone || res.Action.Result != "The garden in spring; Lessons learned. Make them 45 seconds." {
		t.Fatalf("answered: %+v", res.Action)
	}
	cv = turnsOf(t, m, id, 2)
	t2 := cv.Turns[1]
	if t2.Note || t2.Prompt != res.Action.Result || t2.AnswerTo != shorts.ID || cv.PendingActions != 1 {
		t.Fatalf("the answer is the person's reply: %+v", t2)
	}
	raw, _ := m.Get(ctx, id, false, true)
	if !strings.Contains(raw.Turns[1].Sent, `This message answers your question card "Which of these shorts should I set up?"`) {
		t.Fatalf("Claude gets the question with the answer: %q", raw.Turns[1].Sent)
	}
	// Not now on a question tells Claude like any card.
	if _, err := actIn(t, m, ConversationActIn{ID: id, ActionID: keep.ID, Dismiss: true}); err != nil {
		t.Fatal(err)
	}
	cv = turnsOf(t, m, id, 3)
	if !cv.Turns[2].Note || cv.Turns[2].Shown != "Not now: Keep the listening shots?" {
		t.Fatalf("not now: %+v", cv.Turns[2])
	}
}

// Several cards decided at once run in order and Claude hears about all of
// them in one note, once the review copy a save asked for is done (D144).
func TestDecideSeveralAndSaveWithAReviewCopy(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	ctx := context.Background()
	show, err := ops.CallTyped[ops.CutShowOut](ctx, a, "cut.show", ops.CutShowIn{ProjectID: projectID}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	shorter := strings.Replace(show.Sheet, "0:10 → 0:13", "0:10 → 0:12", 1)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "tighten", Steps: []fakeclaude.Step{
			{Write: &fakeclaude.File{Name: "shorter.cut.md", Content: shorter}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": `metarush comment add "The ending lands." --project ` + projectID + ` --json`}}},
			text("A shorter cut, and a note."),
		}},
		{Match: "Done from the dashboard", Steps: []fakeclaude.Step{text("Saved, rendered and checked.")}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "tighten the end", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	var cv *Conversation
	eventually(t, "both cards", func() bool {
		c, _ := m.Get(ctx, id, false, false)
		cv = c
		return len(c.Actions) == 2
	})
	sheet := cardBy(cv, func(x AssistantAction) bool { return x.Op == "cut.apply" })
	note := cardBy(cv, func(x AssistantAction) bool { return x.Op == "comment.add" })
	if !sheet.SavesVersion || note.SavesVersion {
		t.Fatalf("the save card offers a review copy: %+v / %+v", sheet, note)
	}
	res, err := actIn(t, m, ConversationActIn{ID: id, ActionIDs: []string{sheet.ID, note.ID}, ReviewCopy: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Actions) != 2 || res.Actions[0].State != ActionDone || res.Actions[1].State != ActionDone {
		t.Fatalf("both done: %+v", res.Actions)
	}
	saved := cardBy(&res.Conversation, func(x AssistantAction) bool { return x.ID == sheet.ID })
	if saved.Work == nil || saved.Work.Kind != WorkRender || !strings.HasPrefix(saved.Result, "Saved as version 2. Rendering a review copy") {
		t.Fatalf("saved and rendering: %+v", saved)
	}
	var rd models.Render
	if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), saved.Work.RenderID, &rd); err != nil || rd.Quality != "review" || rd.RevisionNumber != 2 {
		t.Fatalf("a review copy of version 2: %+v %v", rd, err)
	}
	time.Sleep(300 * time.Millisecond)
	if cv = waitIdle(t, m, id); len(cv.Turns) != 1 {
		t.Fatalf("Claude waits for the whole decision: %+v", cv.Turns)
	}
	finishRender(t, a, rd.ID, models.QAPass, 0)
	cv = turnsOf(t, m, id, 2)
	if lines := strings.Split(cv.Turns[1].Shown, "\n"); len(lines) != 2 || !strings.Contains(cv.Turns[1].Shown, "Added the note") ||
		!strings.Contains(cv.Turns[1].Shown, "Saved as version 2. The review copy of version 2 is ready (5 s). The quality check found nothing wrong.") {
		t.Fatalf("one note for the decision: %q", cv.Turns[1].Shown)
	}
	// Only a question card takes an answer; a decided card can't be decided again.
	if _, err := actIn(t, m, ConversationActIn{ID: id, ActionIDs: []string{sheet.ID}}); code(err) != apperr.Conflict {
		t.Fatalf("decided twice: %v", err)
	}
}

// Work a card started is watched again after a restart (D142).
func TestWorkIsWatchedAfterARestart(t *testing.T) {
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	script := &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "render", Steps: []fakeclaude.Step{
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush render submit --revision " + head + " --quality review --json"}}},
			text("Asked."),
		}},
		{Match: "Done from the dashboard", Steps: []fakeclaude.Step{text("Looked at it.")}},
	}}
	m := withFake(t, a, fakeclaude.Options{Script: script})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "render it", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	eventually(t, "the card", func() bool { c, _ := m.Get(ctx, id, false, false); cv = c; return len(c.Actions) == 1 })
	res, err := actIn(t, m, ConversationActIn{ID: id, ActionID: cv.Actions[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	m.Close() // MetaRush stops while the render runs
	var stored Conversation
	if err := a.DB.GetByID(ctx, store.CAssistantConversations, a.WorkspaceID(), id, &stored); err != nil || stored.Watching != 1 {
		t.Fatalf("the watch is stored: %d %v", stored.Watching, err)
	}
	m2 := New(a, "/usr/local/bin/metarush-test")
	m2.grace, m2.workEvery, m2.tellAfter = 400*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(m2.Close)
	if _, err := m2.Get(ctx, id, false, false); err != nil { // starts the manager: it picks the watch up again
		t.Fatal(err)
	}
	finishRender(t, a, res.Action.Work.RenderID, models.QAPass, 0)
	cv = turnsOf(t, m2, id, 2)
	if !cv.Turns[1].Note || !strings.HasPrefix(cv.Turns[1].Shown, "The review copy of version 1 is ready") {
		t.Fatalf("heard after the restart: %+v", cv.Turns[1])
	}
	var j models.Job
	_ = a.DB.C(store.CJobs).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "type": jobs.TypeRender}).Decode(&j)
	if j.CreatedBy.Via != "dashboard" {
		t.Fatalf("the person's click made the render: %+v", j.CreatedBy)
	}
}

// Cards an earlier MetaRush decided were told to Claude the moment they were
// decided (they carry no batch): a newer MetaRush never tells them again.
func TestCardsDecidedEarlierAreNotToldAgain(t *testing.T) {
	c := newConv(Conversation{Actions: []AssistantAction{
		{ID: "act_old", Title: "Add the note", State: ActionDone, Result: "Added the note"},
		{ID: "act_new", Title: "Save the shorter cut", State: ActionDone, Result: "Saved as version 2", Batch: "b_new"},
		{ID: "act_wait", Title: "Approve the graphic", State: ActionPending},
	}})
	if n := untoldCount(c.data.Actions); n != 1 {
		t.Fatalf("untold before: %d", n)
	}
	c.mu.Lock()
	lines, shown, _ := c.readyLocked()
	c.mu.Unlock()
	if len(lines) != 1 || !strings.Contains(lines[0], "Save the shorter cut") || len(shown) != 1 || shown[0] != "Saved as version 2" {
		t.Fatalf("told: %q / %q", lines, shown)
	}
	if n := untoldCount(c.data.Actions); n != 0 {
		t.Fatalf("untold after: %d", n)
	}
}

// News Claude hadn't heard when MetaRush stopped (the render finished, the
// note not sent yet) is told when MetaRush starts again (D142).
func TestNewsWaitingAtARestartIsTold(t *testing.T) {
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	script := &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "render", Steps: []fakeclaude.Step{
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush render submit --revision " + head + " --quality review --json"}}},
			text("Asked."),
		}},
		{Match: "Done from the dashboard", Steps: []fakeclaude.Step{text("Looked at it.")}},
	}}
	m := withFake(t, a, fakeclaude.Options{Script: script})
	m.tellAfter = time.Hour // MetaRush stops before the note goes
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "render it", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	eventually(t, "the card", func() bool { c, _ := m.Get(ctx, id, false, false); cv = c; return len(c.Actions) == 1 })
	res, err := actIn(t, m, ConversationActIn{ID: id, ActionID: cv.Actions[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	finishRender(t, a, res.Action.Work.RenderID, models.QAPass, 0)
	eventually(t, "the work finished", func() bool {
		c, _ := m.Get(ctx, id, false, false)
		return c != nil && c.Actions[0].Work != nil && c.Actions[0].Work.State == WorkFinished
	})
	m.Close()
	var stored struct {
		Untold   int `bson:"untold"`
		Watching int `bson:"watching"`
	}
	stored.Untold = -1
	if err := a.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": id}).Decode(&stored); err != nil || stored.Untold != 1 || stored.Watching != 0 {
		t.Fatalf("stored before the restart: %+v %v", stored, err)
	}
	m2 := New(a, "/usr/local/bin/metarush-test")
	m2.grace, m2.workEvery, m2.tellAfter = 400*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(m2.Close)
	if _, err := m2.Get(ctx, id, false, false); err != nil { // starts the manager: it tells Claude
		t.Fatal(err)
	}
	cv = turnsOf(t, m2, id, 2)
	if !cv.Turns[1].Note || !strings.HasPrefix(cv.Turns[1].Shown, "The review copy of version 1 is ready") {
		t.Fatalf("heard after the restart: %+v", cv.Turns[1])
	}
	eventually(t, "nothing left untold", func() bool {
		stored.Untold = -1
		_ = a.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": id}).Decode(&stored)
		return stored.Untold == 0
	})
}

// A terminal command's manager never acts on conversations in the background
// (a command run while the dashboard daemon is up must not race it): it
// doesn't follow work, wake Claude or start safe steps by itself. A message
// sent from it brings the running work up to date and takes the news along.
func TestATerminalLeavesWorkAndNewsToTheDaemon(t *testing.T) {
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	review := "metarush render submit --revision " + head + " --quality review --json"
	script := &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "render it", Steps: []fakeclaude.Step{
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": review}}},
			text("Asked."),
		}},
		{Match: "From the dashboard since your last reply", Steps: []fakeclaude.Step{
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": review}}},
			text("Heard it with your message; one more copy?"),
		}},
		{Match: "Done from the dashboard", Steps: []fakeclaude.Step{text("A note woke me.")}},
	}}
	m := withFake(t, a, fakeclaude.Options{Script: script}) // safe steps off (testApp): the person clicks the first one
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "render it", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	eventually(t, "the card", func() bool { c, _ := m.Get(ctx, id, false, false); cv = c; return len(c.Actions) == 1 })
	res, err := actIn(t, m, ConversationActIn{ID: id, ActionID: cv.Actions[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	m.Close() // the daemon stops while the render runs
	eventually(t, "the daemon's watcher to stop", func() bool { m.mu.Lock(); defer m.mu.Unlock(); return !m.watching })
	on := true
	a.Cfg.Assistant.SafeSteps = &on

	tm := NewTerminal(a, "/usr/local/bin/metarush-test")
	tm.grace, tm.workEvery, tm.tellAfter = 400*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(tm.Close)
	if _, err := tm.Get(ctx, id, false, false); err != nil {
		t.Fatal(err)
	}
	finishRender(t, a, res.Action.Work.RenderID, models.QAPass, 0)
	time.Sleep(800 * time.Millisecond) // the daemon would have noticed and told Claude by now
	tm.mu.Lock()
	watching := tm.watching
	tm.mu.Unlock()
	cv, _ = tm.Get(ctx, id, false, false)
	if watching || len(cv.Turns) != 1 || cv.Actions[0].Work == nil || cv.Actions[0].Work.State != WorkRunning {
		t.Fatalf("the terminal followed the work or woke Claude: watching=%v turns=%d work=%+v", watching, len(cv.Turns), cv.Actions[0].Work)
	}
	// The person's next message, from the terminal, takes the news along.
	if _, err := tm.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "what now?"}}); err != nil {
		t.Fatal(err)
	}
	turnsOf(t, tm, id, 2)
	cv, _ = tm.Get(ctx, id, false, true)
	if cv.Turns[1].Note || !strings.Contains(cv.Turns[1].Sent, "Done from the dashboard") || cv.Actions[0].Work.State != WorkFinished {
		t.Fatalf("the news went with the message: note=%v work=%+v sent=%q", cv.Turns[1].Note, cv.Actions[0].Work, cv.Turns[1].Sent)
	}
	// Claude's next review render waits for a click in a terminal, setting or not.
	if len(cv.Actions) != 2 || cv.Actions[1].State != ActionPending || cv.Actions[1].Auto {
		t.Fatalf("a safe step started by itself in a terminal: %+v", cv.Actions[len(cv.Actions)-1])
	}
	var stored struct {
		Untold int `bson:"untold"`
	}
	if err := a.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": id}).Decode(&stored); err != nil || stored.Untold != 0 {
		t.Fatalf("untold after the message: %d %v", stored.Untold, err)
	}
}
