package assistant

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// A deleted (archived) project's conversations stay readable and say so, but
// take no new message until it is restored; clearing the project out for good
// removes them with their folders, and a conversation held in memory is
// never saved back (D196).
func TestConversationsOfDeletedAndClearedOutProjects(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "", Steps: []fakeclaude.Step{text("Noted.")}},
	}}})
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "A slower opening", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	waitIdle(t, m, id)

	if _, err := ops.CallTyped[ops.ProjectArchiveOut](ctx, a, "project.delete", ops.ProjectDeleteIn{ID: projectID}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	cv, err := m.Get(ctx, id, true, false)
	if err != nil || !cv.ProjectArchived || len(cv.Turns) != 1 {
		t.Fatalf("an archived project's conversation reads: %+v %v", cv, err)
	}
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: id, AssistantMessageIn: AssistantMessageIn{Prompt: "And the ending"}}); code(err) != apperr.Conflict || reason(err) != "project_archived" {
		t.Fatalf("a message in an archived project: %v", err)
	}
	if _, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Start over", ProjectID: projectID}}); reason(err) != "project_archived" {
		t.Fatalf("a new conversation in an archived project: %v", err)
	}
	if _, err := ops.CallTyped[ops.ProjectArchiveOut](ctx, a, "project.restore", ops.ProjectRestoreIn{ID: projectID}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if cv, _ := m.Get(ctx, id, false, false); cv.ProjectArchived {
		t.Fatal("restored, it still says archived")
	}

	// Cleared out: the record and this machine's folder go, and the
	// conversation this process holds is never saved back.
	if _, err := ops.CallTyped[ops.ProjectArchiveOut](ctx, a, "project.delete", ops.ProjectDeleteIn{ID: projectID}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[ops.ProjectPurgeOut](ctx, a, "project.purge", ops.ProjectPurgeIn{ID: projectID, Forever: true}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(m.convDir(id)); !os.IsNotExist(err) {
		t.Fatalf("its folder: %v", err)
	}
	m.mu.Lock()
	held := m.convs[id] != nil
	m.mu.Unlock()
	if held {
		t.Fatal("the manager still holds it")
	}
	time.Sleep(2 * time.Second) // a save that was due would have run by now
	if n, _ := a.DB.C(store.CAssistantConversations).CountDocuments(ctx, bson.M{"_id": id}); n != 0 {
		t.Fatal("the conversation was saved back")
	}
}

// A conversation folder whose record is gone (its project was cleared out on
// another machine) is removed when MetaRush starts; one that is recent, or
// whose record is there, stays.
func TestSweepRemovesFoldersOfGoneConversations(t *testing.T) {
	a := testApp(t, "")
	m := withFake(t, a, fakeclaude.Options{})
	ctx := context.Background()
	gone, fresh, kept := ids.New(ConvPrefix), ids.New(ConvPrefix), ids.New(ConvPrefix)
	if _, err := a.DB.C(store.CAssistantConversations).InsertOne(ctx, bson.M{"_id": kept, "workspaceId": a.WorkspaceID(), "title": "kept"}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, id := range []string{gone, fresh, kept} {
		dir := filepath.Join(m.convDir(id), "proposals")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if id != fresh {
			_ = os.Chtimes(m.convDir(id), old, old)
		}
	}
	m.sweepGone(ctx)
	if _, err := os.Stat(m.convDir(gone)); !os.IsNotExist(err) {
		t.Fatal("a gone conversation's folder stayed")
	}
	for _, id := range []string{fresh, kept} {
		if _, err := os.Stat(m.convDir(id)); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
}
