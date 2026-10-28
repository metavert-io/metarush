package assistant

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// holderOf is a conversation's state, holder and handover mark as stored.
func holderOf(t *testing.T, m *Manager, id string) (string, int, *ConvHandingOver) {
	t.Helper()
	var d struct {
		State       string           `bson:"state"`
		HolderPID   int              `bson:"holderPid"`
		HandingOver *ConvHandingOver `bson:"handingOver"`
	}
	if err := m.App.DB.C(store.CAssistantConversations).FindOne(context.Background(), bson.M{"_id": id}).Decode(&d); err != nil {
		t.Fatal(err)
	}
	return d.State, d.HolderPID, d.HandingOver
}

// idleConversation is a conversation with one reply whose Claude Code
// process has ended (an idle timeout, or room made for another).
func idleConversation(t *testing.T) (*Manager, *conv, string) {
	t.Helper()
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{})
	out, err := m.Ask(context.Background(), AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "hello"}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	waitIdle(t, m, id)
	m.mu.Lock()
	c := m.convs[id]
	m.mu.Unlock()
	m.endProcess(c)
	eventually(t, "the holder to let go", func() bool { _, pid, _ := holderOf(t, m, id); return pid == 0 })
	return m, c, id
}

// A message claims its conversation with its state and this process as its
// holder in one write: from then until Claude Code is up — whatever is saved
// meanwhile — a handover finds it busy and never marks it (one history).
func TestHandoverNeverCrossesAMessageBeingStarted(t *testing.T) {
	m, c, id := idleConversation(t)
	ctx := context.Background()
	mark := HandingOverMark{Handover: "hov_01m60000000000000000000000", To: "ins_01m60000000000000000000001", ToName: "desk"}
	c.mu.Lock()
	c.starting = true // what send does before startTurn
	c.mu.Unlock()
	if err := m.mayContinue(c); err != nil {
		t.Fatal(err)
	}
	if st, pid, _ := holderOf(t, m, id); st != ConvWorking || pid != os.Getpid() {
		t.Fatalf("claimed: state %s holder %d", st, pid)
	}
	if marked, busy, err := MarkHandingOver(ctx, m.App, []string{id}, mark); err != nil || len(marked) != 0 || len(busy) != 1 {
		t.Fatalf("a message being started was marked: %v %v %v", marked, busy, err)
	}
	// A save before the process is up (a card decided, a note) keeps it so.
	m.save(c, true)
	if st, pid, _ := holderOf(t, m, id); st != ConvWorking || pid != os.Getpid() {
		t.Fatalf("after a save: state %s holder %d", st, pid)
	}
	if marked, _, _ := MarkHandingOver(ctx, m.App, []string{id}, mark); len(marked) != 0 {
		t.Fatal("marked after a save")
	}
	if held, _ := Held(ctx, m.App, m.App.Cfg.Installation.ID); len(held) != 1 || !held[0].Busy {
		t.Fatalf("the dry run says it is busy: %+v", held)
	}
	c.mu.Lock()
	c.starting = false
	c.data.State, c.data.HolderPID = ConvIdle, 0
	c.mu.Unlock()
	m.save(c, true)
	// Idle again: now a handover marks it, and the next message is refused.
	if marked, _, _ := MarkHandingOver(ctx, m.App, []string{id}, mark); len(marked) != 1 {
		t.Fatal("an idle conversation wasn't marked")
	}
	if err := m.mayContinue(c); reason(err) != "handing_over" {
		t.Fatalf("a message to a conversation that is moving: %v", err)
	}
}

// A message sent while a handover marks conversations: either the message
// goes ahead and the conversation isn't marked, or it is marked and the
// message is refused — never both.
func TestMessageAndHandoverNeverBothWin(t *testing.T) {
	m, c, id := idleConversation(t)
	ctx := context.Background()
	mark := HandingOverMark{Handover: "hov_01m60000000000000000000002", To: "ins_01m60000000000000000000003", ToName: "desk"}
	for i := range 25 {
		m.endProcess(c)
		eventually(t, "the holder to let go", func() bool { st, pid, _ := holderOf(t, m, id); return pid == 0 && st == ConvIdle })
		done := make(chan error, 1)
		go func() {
			_, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "go on"}})
			done <- err
		}()
		marked := false
	race:
		for {
			select {
			case err := <-done:
				_, _, h := holderOf(t, m, id)
				switch {
				case err == nil && h != nil && marked:
					t.Fatalf("attempt %d: the message went to Claude here and the conversation was marked as moving", i)
				case err != nil && reason(err) != "handing_over":
					t.Fatalf("attempt %d: %v", i, err)
				}
				break race
			default:
				if mk, _, _ := MarkHandingOver(ctx, m.App, []string{id}, mark); len(mk) > 0 {
					marked = true
				}
			}
		}
		waitIdleOrRefused(t, m, id)
		_, _ = UnmarkHandingOver(ctx, m.App, mark.Handover, m.App.Cfg.Installation.ID)
	}
}

// waitIdleOrRefused waits until no message runs (one may have been refused).
func waitIdleOrRefused(t *testing.T, m *Manager, id string) {
	t.Helper()
	eventually(t, "the conversation to be idle", func() bool {
		c, err := m.Get(context.Background(), id, false, false)
		return err == nil && c.State == ConvIdle && len(c.Queue) == 0 && (len(c.Turns) == 0 || TurnTerminal(c.Turns[len(c.Turns)-1].State))
	})
}

// cardConversation is an idle conversation with one pending card that
// renames a project.
func cardConversation(t *testing.T) (*Manager, *conv, string, string) {
	t.Helper()
	m, c, id := idleConversation(t)
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, m.App, "project.create", ops.ProjectCreateIn{Name: "Before"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	in, _ := json.Marshal(ops.ProjectUpdateIn{ID: p.Project.ID, Name: "After"})
	c.mu.Lock()
	c.data.Actions = append(c.data.Actions, AssistantAction{ID: "act_rename", Turn: 1, Kind: "project update", Title: "Rename the project", Op: "project.update",
		Input: string(in), State: ActionPending})
	c.touch()
	c.mu.Unlock()
	m.save(c, true)
	return m, c, id, p.Project.ID
}

func projectName(t *testing.T, m *Manager, id string) string {
	t.Helper()
	var p models.Project
	if err := m.App.DB.GetByID(context.Background(), store.CProjects, m.App.WorkspaceID(), id, &p); err != nil {
		t.Fatal(err)
	}
	return p.Name
}

func storedCard(t *testing.T, m *Manager, id, card string) string {
	t.Helper()
	var d Conversation
	if err := m.App.DB.GetByID(context.Background(), store.CAssistantConversations, m.App.WorkspaceID(), id, &d); err != nil {
		t.Fatal(err)
	}
	for _, a := range d.Actions {
		if a.ID == card {
			return a.State
		}
	}
	return ""
}

// A card of a conversation that moved to another computer isn't run by the
// old one (it would run again there: paid work twice), nor are its settings
// changed there; while it is on its way, its cards wait for the computer it
// goes to.
func TestCardsRunOnlyWhereTheConversationIs(t *testing.T) {
	m, _, id, project := cardConversation(t)
	ctx := context.Background()
	b := *m.App
	b.Actor.Via = "dashboard"
	act := ConversationActIn{ID: id, ActionID: "act_rename"}

	// On its way: refused, the card waits.
	mark := HandingOverMark{Handover: "hov_01m60000000000000000000004", To: "ins_01m60000000000000000000005", ToName: "desk"}
	if mk, _, err := MarkHandingOver(ctx, m.App, []string{id}, mark); err != nil || len(mk) != 1 {
		t.Fatalf("mark: %v %v", mk, err)
	}
	if _, err := m.Act(ctx, &b, ops.Caller{Human: true}, act); reason(err) != "handing_over" {
		t.Fatalf("a card while handing over: %v", err)
	}
	if projectName(t, m, project) != "Before" || storedCard(t, m, id, "act_rename") != ActionPending {
		t.Fatal("the card ran, or isn't pending any more")
	}
	// Moved: refused as another computer's, nothing runs, nothing changes.
	if _, err := m.App.DB.C(store.CAssistantConversations).UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"installationId": "ins_01m60000000000000000000005", "installationName": "desk"}, "$unset": bson.M{"handingOver": ""}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Act(ctx, &b, ops.Caller{Human: true}, act); reason(err) != "other_machine" {
		t.Fatalf("a card of a conversation that moved: %v", err)
	}
	if projectName(t, m, project) != "Before" || storedCard(t, m, id, "act_rename") != ActionPending {
		t.Fatal("the old computer ran a card of a conversation that moved")
	}
	model := "sonnet"
	if _, err := m.Configure(ctx, ConversationConfigureIn{ID: id, Model: &model}); reason(err) != "other_machine" {
		t.Fatalf("settings of a conversation that moved: %v", err)
	}
}

// Two decisions on one card at once run it once.
func TestACardIsClaimedOnce(t *testing.T) {
	m, _, id, project := cardConversation(t)
	ctx := context.Background()
	b := *m.App
	b.Actor.Via = "dashboard"
	errs := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := m.Act(ctx, &b, ops.Caller{Human: true}, ConversationActIn{ID: id, ActionID: "act_rename"})
			errs <- err
		}()
	}
	ok := 0
	for range 2 {
		if err := <-errs; err == nil {
			ok++
		} else if code(err) != apperr.Conflict {
			t.Fatalf("the second decision: %v", err)
		}
	}
	if ok != 1 || projectName(t, m, project) != "After" || storedCard(t, m, id, "act_rename") != ActionDone {
		t.Fatalf("decided %d times; card %s", ok, storedCard(t, m, id, "act_rename"))
	}
	// A conversation with a card running isn't taken by a handover.
	m2, c2, id2, _ := cardConversation(t)
	release, err := m2.claimCards(ctx, c2, []string{"act_rename"})
	if err != nil {
		t.Fatal(err)
	}
	if mk, busy, _ := MarkHandingOver(ctx, m2.App, []string{id2}, HandingOverMark{Handover: "hov_01m60000000000000000000006", To: "x", ToName: "desk"}); len(mk) != 0 || len(busy) != 1 {
		t.Fatalf("marked while a card runs: %v %v", mk, busy)
	}
	release()
	if storedCard(t, m2, id2, "act_rename") != ActionPending {
		t.Fatal("a released card isn't pending again")
	}
}
