package assistant

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// A conversation two people share (D179). Theo (this installation's person)
// starts one in a project and one outside any; Maya, signed in as herself,
// finds the project's in her list (a project's list is everyone's; the
// workspace-wide one stays hers unless she asks for everyone's), continues it
// and decides a card Theo was asked. Every message, card and decision is the
// person's who sent or made it; what waits for someone counts only for them.
func TestSharedConversationsArePeoples(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	note := func(body string) fakeclaude.Step {
		return denied(`metarush comment add "` + body + `" --project ` + projectID + ` --json`)
	}
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "a slower opening", Steps: []fakeclaude.Step{note("A slower opening"), text("Your note is on a card.")}},
		{Match: "the kitchen shot", Steps: []fakeclaude.Step{note("Keep the kitchen shot"),
			{Write: &fakeclaude.File{Name: "music.question.json", Content: `{"question":"Which music?","choices":["Calm","Bright"]}`}},
			text("A note and a question for you.")}},
		{Match: "Done from the dashboard", Steps: []fakeclaude.Step{text("Noted.")}},
		{Match: "answers your question card", Steps: []fakeclaude.Step{text("Calm it is.")}},
	}}})
	var ended []Turn
	var endedMu sync.Mutex
	m.OnTurnEnd = func(_ Conversation, t Turn) {
		endedMu.Lock()
		ended = append(ended, t)
		endedMu.Unlock()
	}
	ctx := context.Background()
	theoID := a.Cfg.User.ID
	theo := AsPerson(ctx, a)
	mayaApp := *a
	mayaApp.Actor = models.Actor{UserID: ids.New(ids.User), UserName: "Maya", InstallationID: a.Cfg.Installation.ID, Via: "dashboard"}
	maya := AsPerson(ctx, &mayaApp)
	mayaID := mayaApp.Actor.UserID

	// Theo's two conversations: one in the project, one in the workspace.
	out, err := m.Ask(theo, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "I'd like a slower opening", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	inProject := out.Conversation.ID
	waitIdle(t, m, inProject)
	out, err = m.Ask(theo, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "What's in the workspace?"}})
	if err != nil {
		t.Fatal(err)
	}
	inWorkspace := out.Conversation.ID
	waitIdle(t, m, inWorkspace)
	eventually(t, "Theo's card", func() bool {
		c, _ := m.Get(theo, inProject, false, false)
		return c != nil && len(c.Actions) == 1
	})

	// Whose conversations a list shows.
	listed := func(l *ConversationListOut) []string {
		var out []string
		for _, c := range l.Conversations {
			out = append(out, c.ID)
		}
		return out
	}
	list := func(ctx context.Context, in ConversationListIn) *ConversationListOut {
		t.Helper()
		l, err := m.List(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	l := list(maya, ConversationListIn{ProjectID: projectID})
	if got := listed(l); len(got) != 1 || got[0] != inProject || !l.AllUsers || l.Others != 1 {
		t.Fatalf("Maya's project list is everyone's by default: %v allUsers=%v others=%d", got, l.AllUsers, l.Others)
	}
	if c := l.Conversations[0]; c.Mine || c.UserID != theoID || c.UserName != "Tester" || c.PendingActions != 1 || c.WaitingForYou != 0 ||
		len(c.WaitingFor) != 1 || c.WaitingFor[0].UserID != theoID {
		t.Fatalf("Theo's conversation as Maya sees it: %+v", c)
	}
	if l := list(maya, ConversationListIn{ProjectID: projectID, Mine: true}); len(l.Conversations) != 0 || l.AllUsers || l.Others != 1 {
		t.Fatalf("just hers, in the project: %v %v %d", listed(l), l.AllUsers, l.Others)
	}
	if l := list(maya, ConversationListIn{}); len(l.Conversations) != 0 || l.AllUsers || l.Others != 2 {
		t.Fatalf("the workspace-wide list is hers by default: %v %v %d", listed(l), l.AllUsers, l.Others)
	}
	if l := list(maya, ConversationListIn{AllUsers: true}); len(l.Conversations) != 2 || !l.AllUsers {
		t.Fatalf("everyone's in the workspace: %v", listed(l))
	}
	if l := list(theo, ConversationListIn{ProjectID: projectID}); len(l.Conversations) != 1 || !l.Conversations[0].Mine || l.Others != 0 ||
		l.Conversations[0].WaitingForYou != 1 {
		t.Fatalf("Theo's own project list: %+v", l)
	}
	if _, err := m.List(maya, ConversationListIn{AllUsers: true, Mine: true}); code(err) != apperr.InvalidInput {
		t.Fatalf("everyone's and just mine at once: %v", err)
	}
	// The same through the op, as the dashboard calls it.
	in, _ := json.Marshal(ConversationListIn{ProjectID: projectID})
	res, err := ops.Call(ctx, &mayaApp, "assistant.list", in, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	if l := res.(*ConversationListOut); len(l.Conversations) != 1 || !l.AllUsers || l.Conversations[0].Mine {
		t.Fatalf("assistant.list as Maya: %+v", l)
	}
	if n := m.Status(maya, false).Waiting; n != 0 {
		t.Fatalf("Theo's card waits for Theo, not Maya: %d", n)
	}
	if n := m.Status(theo, false).Waiting; n != 1 {
		t.Fatalf("Theo's card waits for him: %d", n)
	}

	// Maya continues Theo's conversation: her message, her cards.
	if _, err := m.Continue(maya, AssistantContinueIn{ID: inProject, AssistantMessageIn: AssistantMessageIn{Prompt: "Maya here: keep the kitchen shot", ProjectID: projectID}}); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, inProject)
	var cv *Conversation
	eventually(t, "Maya's card and question", func() bool {
		cv, _ = m.Get(maya, inProject, true, false)
		return cv != nil && len(cv.Actions) == 3
	})
	if t2 := cv.Turns[1]; t2.User != "Maya" || t2.UserID != mayaID {
		t.Fatalf("her message is hers: %+v", t2)
	}
	if t1 := cv.Turns[0]; t1.User != "Tester" || t1.UserID != theoID {
		t.Fatalf("his is his: %+v", t1)
	}
	for _, e := range cv.Events {
		if e.Kind == "turn" && e.Turn == 2 && (e.User != "Maya" || e.UserID != mayaID) {
			t.Fatalf("the thread says who wrote it: %+v", e)
		}
	}
	var theoCard, mayaCard, question AssistantAction
	for _, act := range cv.Actions {
		switch {
		case act.Question != nil:
			question = act
		case act.Turn == 1:
			theoCard = act
		default:
			mayaCard = act
		}
	}
	if theoCard.ForID != theoID || theoCard.ForName != "Tester" || mayaCard.ForID != mayaID || mayaCard.ForName != "Maya" || question.ForID != mayaID {
		t.Fatalf("who each card is for: theo %+v / maya %+v / question %+v", theoCard, mayaCard, question)
	}
	if cv.Mine || cv.WaitingForYou != 2 || cv.PendingActions != 3 {
		t.Fatalf("as Maya: mine=%v waitingForYou=%d pending=%d", cv.Mine, cv.WaitingForYou, cv.PendingActions)
	}
	if asTheo, _ := m.Get(theo, inProject, false, false); !asTheo.Mine || asTheo.WaitingForYou != 1 {
		t.Fatalf("as Theo: mine=%v waitingForYou=%d", asTheo.Mine, asTheo.WaitingForYou)
	}
	// The badges read the stored per-person counts, which the conversation's
	// save writes just after the cards show in memory: wait for it.
	var badgeMaya, badgeTheo int
	for deadline := time.Now().Add(25 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if badgeMaya, badgeTheo = m.Status(maya, false).Waiting, m.Status(theo, false).Waiting; badgeMaya == 2 && badgeTheo == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the badges: Maya %d, Theo %d", badgeMaya, badgeTheo)
		}
	}
	// Stored per person, so a list from another process says the same.
	var doc struct {
		WaitingFor []WaitingCount `bson:"waitingFor"`
	}
	if err := a.DB.GetByID(ctx, store.CAssistantConversations, a.WorkspaceID(), inProject, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.WaitingFor) != 2 || countFor(doc.WaitingFor, mayaID) != 2 || countFor(doc.WaitingFor, theoID) != 1 {
		t.Fatalf("stored counts: %+v", doc.WaitingFor)
	}
	// Her turn's notification and audit are hers.
	eventually(t, "the turn's audit", func() bool { return auditBy(t, a, "assistant.turn", 2) != "" })
	if who := auditBy(t, a, "assistant.turn", 2); who != "Maya" {
		t.Fatalf("turn 2 audited as %q", who)
	}
	if who := auditBy(t, a, "assistant.turn", 1); who != "Tester" {
		t.Fatalf("turn 1 audited as %q", who)
	}
	var hers *Turn
	eventually(t, "the turn's end", func() bool {
		endedMu.Lock()
		defer endedMu.Unlock()
		for i := range ended {
			if ended[i].N == 2 {
				hers = &ended[i]
			}
		}
		return hers != nil
	})
	if hers.UserID != mayaID {
		t.Fatalf("the reply to her message notifies her: %+v", hers)
	}

	// She decides the card Theo was asked: the decision is hers, and so is the note.
	in, _ = json.Marshal(ConversationActIn{ID: inProject, ActionID: theoCard.ID})
	res, err = ops.Call(maya, &mayaApp, "assistant.act", in, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	if done := res.(*ConversationActOut).Action; done.State != ActionDone || done.By != "Maya" || done.ByID != mayaID || done.ForID != theoID {
		t.Fatalf("her decision on Theo's card: %+v", done)
	}
	eventually(t, "Claude to hear it", func() bool {
		c, _ := m.Get(maya, inProject, false, false)
		return c != nil && len(c.Turns) == 3 && TurnTerminal(c.Turns[2].State)
	})
	cv = waitIdle(t, m, inProject)
	if n := cv.Turns[2]; !n.Note || n.User != "Maya" || n.UserID != mayaID {
		t.Fatalf("the note reports her decision as hers: %+v", n)
	}
	if who := auditBy(t, a, "assistant.action", 0); who != "Maya" {
		t.Fatalf("the decision audited as %q", who)
	}
	for deadline := time.Now().Add(25 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if badgeMaya, badgeTheo = m.Status(maya, false).Waiting, m.Status(theo, false).Waiting; badgeMaya == 2 && badgeTheo == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after her decision: Maya %d, Theo %d", badgeMaya, badgeTheo)
		}
	}

	// Her answer to the question is her reply.
	in, _ = json.Marshal(ConversationActIn{ID: inProject, ActionID: question.ID, Answer: &QuestionAnswer{Choices: []int{1}}})
	if _, err := ops.Call(maya, &mayaApp, "assistant.act", in, ops.Caller{Human: true, Remote: true}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "the answer turn", func() bool {
		c, _ := m.Get(maya, inProject, false, false)
		return c != nil && len(c.Turns) == 4 && TurnTerminal(c.Turns[3].State)
	})
	cv = waitIdle(t, m, inProject)
	if ans := cv.Turns[3]; ans.Note || ans.AnswerTo != question.ID || ans.User != "Maya" || ans.UserID != mayaID {
		t.Fatalf("her answer: %+v", ans)
	}
	for _, act := range cv.Actions {
		if act.ID == question.ID && (act.By != "Maya" || act.ByID != mayaID) {
			t.Fatalf("the question was answered by her: %+v", act)
		}
	}
}

// auditBy is who the latest audit event of an action (of a turn, when turn > 0) names.
func auditBy(t *testing.T, a *app.App, action string, turn int) string {
	t.Helper()
	filter := bson.M{"action": action}
	if turn > 0 {
		filter["details.turn"] = turn
	}
	var evs []models.AuditEvent
	if err := a.DB.Find(context.Background(), store.CAudit, a.WorkspaceID(), filter, &evs, options.Find().SetSort(bson.D{{Key: "at", Value: 1}})); err != nil {
		t.Fatal(err)
	}
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].Actor.UserName
}

// Older conversations (before D179) have no sender on their messages and no
// per-person counts: everything is the person's who started them.
func TestOlderConversationsAreTheirStartersCards(t *testing.T) {
	d := Conversation{UserID: "usr_theo", UserName: "Theo", Turns: []Turn{{N: 1, User: "Theo"}, {N: 2, User: "Maya", UserID: "usr_maya"}},
		Actions: []AssistantAction{{ID: "a1", Turn: 1, State: ActionPending}, {ID: "a2", Turn: 2, State: ActionPending}, {ID: "a3", Turn: 2, State: ActionDone}}}
	if p := d.sender(1); p.id != "usr_theo" || p.name != "Theo" {
		t.Fatalf("an older message is the starter's: %+v", p)
	}
	if p := d.sender(2); p.id != "usr_maya" {
		t.Fatalf("a newer one is its sender's: %+v", p)
	}
	ws := d.WaitingCounts()
	if len(ws) != 2 || countFor(ws, "usr_theo") != 1 || countFor(ws, "usr_maya") != 1 || countFor(ws, "") != 2 {
		t.Fatalf("counts: %+v", ws)
	}
	listed := Conversation{UserID: "usr_theo", UserName: "Theo", PendingActions: 2} // a list entry saved before D179
	listed.personalize("usr_maya")
	if listed.WaitingForYou != 0 || len(listed.WaitingFor) != 1 || listed.WaitingFor[0].UserID != "usr_theo" || listed.WaitingFor[0].Count != 2 || listed.Mine {
		t.Fatalf("an older list entry as Maya sees it: %+v", listed)
	}
	listed.personalize("usr_theo")
	if listed.WaitingForYou != 2 || !listed.Mine {
		t.Fatalf("as Theo: %+v", listed)
	}
	d.personalize("usr_maya")
	if d.Actions[0].ForID != "usr_theo" || d.Actions[1].ForID != "usr_maya" || d.Actions[1].ForName != "Maya" || d.WaitingForYou != 1 {
		t.Fatalf("live: %+v", d)
	}
}

// A note's message is from the person whose decision it reports: the latest
// one; a safe step or work that finished by itself is the card's person's. A
// safe step runs as the person the card is for.
func TestNotesAndSafeStepsArePeoples(t *testing.T) {
	at := func(m int) *time.Time { v := time.Date(2026, 10, 20, 9, m, 0, 0, time.UTC); return &v }
	c := newConv(Conversation{UserID: "usr_theo", UserName: "Theo",
		Turns: []Turn{{N: 1, User: "Theo", UserID: "usr_theo"}, {N: 2, User: "Maya", UserID: "usr_maya"}},
		Actions: []AssistantAction{
			{ID: "a1", Turn: 1, Title: "Add the note", State: ActionDone, Result: "Added the note", Batch: "b1", By: "Maya", ByID: "usr_maya", DecidedAt: at(5)},
			{ID: "a2", Turn: 1, Title: "Add another note", State: ActionDone, Result: "Added the note", Batch: "b2", By: "Theo", ByID: "usr_theo", DecidedAt: at(3)},
		}})
	c.mu.Lock()
	lines, _, from := c.readyLocked()
	c.mu.Unlock()
	if len(lines) != 2 || from.id != "usr_maya" || from.name != "Maya" {
		t.Fatalf("the note is from the latest decision's person: %v %+v", lines, from)
	}
	c = newConv(Conversation{UserID: "usr_theo", UserName: "Theo",
		Turns:   []Turn{{N: 1, User: "Theo", UserID: "usr_theo"}, {N: 2, User: "Maya", UserID: "usr_maya"}},
		Actions: []AssistantAction{{ID: "a1", Turn: 2, Title: "Render a review copy", State: ActionDone, Auto: true, Result: "Rendering", Batch: "b1", DecidedAt: at(1)}}})
	c.mu.Lock()
	_, _, from = c.readyLocked()
	c.mu.Unlock()
	if from.id != "usr_maya" {
		t.Fatalf("a safe step's news is its card's person's: %+v", from)
	}
	a := testApp(t, "")
	m := New(a, "")
	defer m.Close()
	if run := m.autoApp(c, c.data.Actions[0]); run.Actor.UserID != "usr_maya" || run.Actor.UserName != "Maya" || run.Actor.Via != models.ViaAssistantAuto {
		t.Fatalf("a safe step runs as the person it is for: %+v", run.Actor)
	}
}
