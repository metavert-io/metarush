package assistant

import (
	"context"
	"os"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
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
