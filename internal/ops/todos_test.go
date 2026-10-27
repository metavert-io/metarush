package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/todos"
)

func todoApp(t *testing.T) (*app.App, string, string, string) {
	t.Helper()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: ids.New(ids.Workspace), Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "test-machine"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "tester"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")}}
	ctx := context.Background()
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	p, err := CallTyped[ProjectOut](ctx, a, "project.create", ProjectCreateIn{Name: "Launch teaser"}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	asset := func(name, kind string, video bool) string {
		id := ids.New(ids.Asset)
		as := models.Asset{ID: id, WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: kind, Name: name, SHA256: ids.New("sha"), State: models.AssetAvailable,
			Media: models.MediaInfo{HasVideo: video, HasAudio: true, Duration: 30, Width: 1920, Height: 1080, FPS: 30}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			t.Fatal(err)
		}
		return id
	}
	return a, p.Project.ID, asset("interview.mov", "video", true), asset("theme.m4a", "audio", false)
}

func listOpen(t *testing.T, a *app.App, in TodoListIn) *TodoListOut {
	t.Helper()
	out, err := CallTyped[TodoListOut](context.Background(), a, "todos.list", in, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func byKind(ts []models.Todo, kind string) []models.Todo {
	var out []models.Todo
	for _, t := range ts {
		if t.Kind == kind {
			out = append(out, t)
		}
	}
	return out
}

func TestTodosFromStoryEditsAnswerThroughNormalOps(t *testing.T) {
	a, project, video, music := todoApp(t)
	ctx := context.Background()
	plan := json.RawMessage(fmt.Sprintf(`{"projectId":%q,"instruction":"first cut","spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},
		"clips":[{"id":"a","assetId":%q,"in":0,"out":5}],"audio":[{"id":"bed","assetId":%q,"role":"music","in":0,"out":5,"at":0}]}}`, project, video, music))
	// Asking for a story edit with an empty brief raises goal/audience/tone questions.
	chk, err := CallTyped[EditCheckOut](ctx, a, "edit.check", EditCheckIn{Plan: plan}, Caller{})
	if err != nil || !chk.Valid {
		t.Fatalf("check: %v %+v", err, chk)
	}
	open := listOpen(t, a, TodoListIn{ProjectID: project})
	if n := len(byKind(open.Todos, models.TodoBriefMissing)); n != 3 {
		t.Fatalf("want 3 brief questions, got %d", n)
	}
	// Checking again does not duplicate them.
	if _, err := CallTyped[EditCheckOut](ctx, a, "edit.check", EditCheckIn{Plan: plan}, Caller{}); err != nil {
		t.Fatal(err)
	}
	if n := len(byKind(listOpen(t, a, TodoListIn{ProjectID: project}).Todos, models.TodoBriefMissing)); n != 3 {
		t.Fatalf("idempotency: %d brief questions", n)
	}
	// Applying creates the cut: unlicensed music and an approval are asked about.
	if _, err := CallTyped[EditApplyOut](ctx, a, "edit.apply", EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}, Caller{}); err != nil {
		t.Fatal(err)
	}
	open = listOpen(t, a, TodoListIn{ProjectID: project})
	rights := byKind(open.Todos, models.TodoMusicRights)
	if len(rights) != 1 || rights[0].AssetID != music || len(rights[0].Suggestions) == 0 {
		t.Fatalf("music rights question: %+v", rights)
	}
	approval := byKind(open.Todos, models.TodoRevisionApproval)
	if len(approval) != 1 || !approval[0].HumanOnly {
		t.Fatalf("approval question: %+v", approval)
	}
	if open.Counts.ByProject[project] != 5 || open.Counts.ByAsset[music] != 1 || open.Counts.Total != 5 {
		t.Fatalf("counts: %+v", open.Counts)
	}
	// The kind filter works.
	if got := listOpen(t, a, TodoListIn{Kind: models.TodoMusicRights}); len(got.Todos) != 1 {
		t.Fatalf("kind filter: %d", len(got.Todos))
	}

	// Answer the goal with text: it lands in the brief.
	goal := byKind(open.Todos, models.TodoBriefMissing)[0]
	for _, x := range byKind(open.Todos, models.TodoBriefMissing) {
		if x.Context.Field == "goal" {
			goal = x
		}
	}
	ans, err := CallTyped[TodoAnswerOut](ctx, a, "todos.answer", TodoAnswerIn{ID: goal.ID, Answer: "Get people to the launch event"}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if ans.Todo.State != models.TodoAnswered || ans.Todo.Resolution != models.ResolvedAnswered || ans.Done == "" {
		t.Fatalf("answer: %+v", ans.Todo)
	}
	var p models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), project, &p)
	if p.Brief.Goal != "Get people to the launch event" {
		t.Fatalf("brief not updated: %+v", p.Brief)
	}
	// Answering twice is refused.
	if _, err := CallTyped[TodoAnswerOut](ctx, a, "todos.answer", TodoAnswerIn{ID: goal.ID, Answer: "again"}, Caller{}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("second answer should conflict, got %v", err)
	}
	// Filling the brief another way closes the matching question.
	tone := "warm"
	if _, err := CallTyped[ProjectOut](ctx, a, "project.update", ProjectUpdateIn{ID: project, Brief: &models.Brief{Goal: p.Brief.Goal, Tone: tone}}, Caller{}); err != nil {
		t.Fatal(err)
	}
	if n := len(byKind(listOpen(t, a, TodoListIn{ProjectID: project}).Todos, models.TodoBriefMissing)); n != 1 {
		t.Fatalf("only the audience question should remain, got %d", n)
	}

	// Music rights: pick a suggestion; the file's rights are recorded and the
	// rights warning disappears from new checks.
	if _, err := CallTyped[TodoAnswerOut](ctx, a, "todos.answer", TodoAnswerIn{ID: rights[0].ID, Suggestion: 2}, Caller{}); err != nil {
		t.Fatal(err)
	}
	var m models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), music, &m)
	if m.Rights == nil || m.Rights.License != rights[0].Suggestions[1].Value {
		t.Fatalf("rights not recorded: %+v", m.Rights)
	}
	chk2, err := CallTyped[EditCheckOut](ctx, a, "edit.check", EditCheckIn{Plan: json.RawMessage(fmt.Sprintf(`{"baseRevisionId":%q,"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},
		"clips":[{"id":"a","assetId":%q,"in":0,"out":6}],"audio":[{"id":"bed","assetId":%q,"role":"music","in":0,"out":6,"at":0}]}}`, approval[0].RevisionID, video, music))}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range chk2.Warnings {
		if w.Code == "rights_unknown" {
			t.Fatalf("rights recorded on the file should satisfy the check: %+v", w)
		}
	}

	// Approval is for people only.
	if _, err := CallTyped[TodoAnswerOut](ctx, a, "todos.answer", TodoAnswerIn{ID: approval[0].ID, Suggestion: 1}, Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("approval by a non-person must be refused, got %v", err)
	}
	if _, err := CallTyped[TodoAnswerOut](ctx, a, "todos.answer", TodoAnswerIn{ID: approval[0].ID, Suggestion: 1}, Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	var rev models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), approval[0].RevisionID, &rev)
	if rev.Approval.State != models.ApprovalApproved {
		t.Fatalf("revision not approved: %+v", rev.Approval)
	}

	// Dismissed questions stay dismissed through a re-check.
	left := listOpen(t, a, TodoListIn{ProjectID: project})
	if len(left.Todos) != 1 {
		t.Fatalf("want 1 open question left, got %d", len(left.Todos))
	}
	if _, err := CallTyped[TodoAnswerOut](ctx, a, "todos.dismiss", TodoDismissIn{ID: left.Todos[0].ID}, Caller{}); err != nil {
		t.Fatal(err)
	}
	if _, err := CallTyped[TodoSyncOut](ctx, a, "todos.sync", TodoSyncIn{ProjectID: project}, Caller{}); err != nil {
		t.Fatal(err)
	}
	c, err := CallTyped[todos.Counts](ctx, a, "todos.counts", TodoCountsIn{}, Caller{})
	if err != nil || c.Total != 0 {
		t.Fatalf("nothing should be open after dismissing, got %+v %v", c, err)
	}
}
