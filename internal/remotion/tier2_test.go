package remotion

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func TestLiftNames(t *testing.T) {
	for in, want := range map[string]string{"kite": "kite", "Fixture Film": "fixture-film", "EndV3": "end-v3", "3d-intro": "d-intro", "teaser_v2!": "teaser-v2"} {
		if got := motionSlug(in); got != want {
			t.Errorf("motionSlug(%q) = %q, want %q", in, got, want)
		}
	}
	if got := defaultLibraryName(&models.RemotionSource{Name: "fixture-film"}); got != "fixture-film-theme" {
		t.Errorf("the theme library: %q", got)
	}
	long := &models.RemotionSource{Name: "a-remotion-project-with-a-very-long-short-name"}
	if got := defaultLibraryName(long); len(got) > 40 || !motionNameOK(got) {
		t.Errorf("a long name still makes a library name: %q", got)
	}
	inter := []liftFont{{Path: "s/inter/v20/a.woff2", Family: "Inter", Style: "normal", Subset: "latin"}}
	if got := fontResourceName(inter[0], inter); got != "inter" {
		t.Errorf("one file of a family: %q", got)
	}
	two := []liftFont{{Path: "a", Family: "JetBrains Mono", Style: "normal", Subset: "latin"}, {Path: "b", Family: "JetBrains Mono", Style: "normal", Subset: "latin-ext"}, {Path: "c", Family: "JetBrains Mono", Style: "italic", Subset: "latin"}}
	got := []string{fontResourceName(two[0], two), fontResourceName(two[1], two), fontResourceName(two[2], two)}
	if got[0] != "jetbrains-mono-latin" || got[1] != "jetbrains-mono-latin-ext" || got[2] != "jetbrains-mono-italic" {
		t.Errorf("several files of one family: %v", got)
	}
}

func motionNameOK(s string) bool {
	return len(s) > 0 && s[0] >= 'a' && s[0] <= 'z'
}

// A version that waits for a person asks on the To-dos page and rings the
// bell for the person who made it, once; deciding closes the question (D172).
func TestWaitingVersionsAskAPerson(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	s := newSource(t, a, "teaser")
	v1 := version(t, a, s, "a", "")
	remindApproval(ctx, a, s, v1)
	remindApproval(ctx, a, s, v1) // twice: still one question, one notification
	var todos []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"kind": models.TodoRemotionApproval}, &todos); err != nil {
		t.Fatal(err)
	}
	if len(todos) != 1 || todos[0].State != models.TodoOpen || !todos[0].HumanOnly || todos[0].Context.Remotion == nil || todos[0].Context.Remotion.Version != v1.Hash {
		t.Fatalf("one open question about version 1: %+v", todos)
	}
	var notes []models.Notification
	if err := a.DB.Find(ctx, store.CNotifications, a.WorkspaceID(), bson.M{"kind": models.NotifyRemotionWaiting}, &notes); err != nil {
		t.Fatal(err)
	}
	if len(notes) != 1 || notes[0].UserID != a.Actor.UserID || notes[0].Link == "" {
		t.Fatalf("one notification for the person who made it: %+v", notes)
	}
	// A newer version waiting replaces the question; an older one stops asking.
	v2 := version(t, a, s, "b", v1.Hash)
	remindApproval(ctx, a, s, v2)
	remindApproval(ctx, a, s, v1)
	open := openQuestions(t, a)
	if len(open) != 1 || open[0].Context.Remotion.Number != 2 {
		t.Fatalf("only the newest version asks: %+v", open)
	}
	// A version whose check failed can't be approved, so it doesn't ask.
	if err := setVersion(ctx, a, v2.Hash, bson.M{"check": &models.RemotionCheck{OK: false}}); err != nil {
		t.Fatal(err)
	}
	fresh, _ := getVersion(ctx, a, v2.Hash)
	remindApproval(ctx, a, s, fresh)
	if open := openQuestions(t, a); len(open) != 0 {
		t.Fatalf("a failed check closes the question: %+v", open)
	}
	// Deciding closes it, wherever the decision was made.
	v3 := version(t, a, s, "c", v2.Hash)
	remindApproval(ctx, a, s, v3)
	if open := openQuestions(t, a); len(open) != 1 {
		t.Fatalf("version 3 asks: %+v", open)
	}
	if _, err := decide(ctx, a, s, v3, verbApprove, a.Actor, ""); err != nil {
		t.Fatal(err)
	}
	if open := openQuestions(t, a); len(open) != 0 {
		t.Fatalf("approving answers the question: %+v", open)
	}
}

func openQuestions(t *testing.T, a *app.App) []models.Todo {
	t.Helper()
	var out []models.Todo
	if err := a.DB.Find(context.Background(), store.CTodos, a.WorkspaceID(), bson.M{"kind": models.TodoRemotionApproval, "state": models.TodoOpen}, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The question is answered on the To-dos page like a cut's approval: only by a person.
func TestApprovalQuestionIsForPeople(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	s := newSource(t, a, "teaser")
	if err := a.DB.Insert(ctx, store.CProjects, &models.Project{ID: s.ProjectID, WorkspaceID: a.WorkspaceID(), Name: "Teaser"}); err != nil {
		t.Fatal(err)
	}
	v1 := version(t, a, s, "a", "")
	remindApproval(ctx, a, s, v1)
	open := openQuestions(t, a)
	if len(open) != 1 || open[0].Context.ProjectName != "Teaser" {
		t.Fatalf("one question: %+v", open)
	}
	in := ops.TodoAnswerIn{ID: open[0].ID, Suggestion: 1}
	if _, err := ops.CallTyped[ops.TodoAnswerOut](ctx, a, "todos.answer", in, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("Claude can't approve: %v", err)
	}
	out, err := ops.CallTyped[ops.TodoAnswerOut](ctx, a, "todos.answer", in, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := getVersion(ctx, a, v1.Hash); v.State != models.RemotionApproved || out.Todo.State != models.TodoAnswered {
		t.Fatalf("a person's answer approves it: %s %+v", v.State, out.Todo)
	}
}

func TestWordsCheckPictures(t *testing.T) {
	c := &models.RemotionWordsChange{Number: 3, Check: &models.RemotionWordsCheck{OK: true, Version: "abc", Stills: []models.RemotionStill{{Composition: "End", At: 1, After: models.RemotionObject{SHA256: "x"}, Before: &models.RemotionObject{SHA256: "y"}}}}}
	made := wordsCheckOut(c, true)
	if len(made.Stills) != 1 || made.Stills[0].AfterURL != "/media/remotion/abc/words/3-0/after" || made.Stills[0].BeforeURL != "/media/remotion/abc/words/3-0/before" {
		t.Fatalf("a change's pictures: %+v", made.Stills)
	}
	if pending := wordsCheckOut(c, false); pending.Stills[0].AfterURL != "/media/remotion/abc/words/0-0/after" || pending.Made {
		t.Fatalf("a change only checked is change 0: %+v", pending.Stills)
	}
	if wordsCheckOut(nil, true) != nil {
		t.Fatal("no change, nothing to show")
	}
}
