package ops_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	_ "github.com/metavert-io/metarush/internal/assistant" // its clear-out of Claude Code conversations
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Deleting a project archives it, and an owner clears archived projects out
// for good (D196), against the test database.

// stored puts bytes in the media store and returns their key and SHA-256.
func stored(t *testing.T, a *app.App, b []byte) (string, string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(context.Background(), key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	return key, sha
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func isStored(a *app.App, key string) bool {
	_, err := a.Media.Stat(context.Background(), key)
	return err == nil
}

// render records a finished render of a version with its video, picture and
// a quality check with one frame; video is the render's bytes (nil: new ones).
func render(t *testing.T, a *app.App, projectID, revisionID string, video []byte) (models.Render, models.QAReport) {
	t.Helper()
	ctx := context.Background()
	if video == nil {
		video = randomBytes(4096)
	}
	vkey, vsha := stored(t, a, video)
	tkey, _ := stored(t, a, randomBytes(512))
	fkey, fsha := stored(t, a, randomBytes(700))
	now := time.Now().UTC()
	r := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: projectID, RevisionID: revisionID, RevisionNumber: 1, Preset: "vertical-1080p",
		Quality: "review", State: models.RenderAvailable, Output: &models.RenderOutput{StorageKey: vkey, SHA256: vsha, Size: int64(len(video)), ThumbnailKey: tkey}, CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CRenders, r); err != nil {
		t.Fatal(err)
	}
	q := models.QAReport{ID: ids.New("qar"), WorkspaceID: a.WorkspaceID(), ProjectID: projectID, RevisionID: revisionID, RenderID: r.ID, RenderSHA256: vsha,
		Verdict: "pass", Frames: []models.QAFrame{{ID: "f1", Label: "start", Reason: "start", Key: fkey, SHA256: fsha, Size: 700}}, CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CQAReports, q); err != nil {
		t.Fatal(err)
	}
	return r, q
}

func count(t *testing.T, a *app.App, coll string, filter bson.M) int {
	t.Helper()
	filter["workspaceId"] = a.WorkspaceID()
	n, err := a.DB.C(coll).CountDocuments(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func callerHTTPMember() (context.Context, ops.Caller) {
	return ops.OverHTTP(context.Background(), func() bool { return false }), ops.Caller{Human: true, Remote: true}
}

// Deleting a project archives it: people only, gone from Home's lists and
// search, its footage still in the Library, links from other projects kept,
// back as it was when restored.
func TestDeleteAndRestoreProject(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	promo := project(t, a, "Spring promo")
	other := project(t, a, "Summer teaser")
	kitchen := clip(t, a, "Kitchen chat", promo.ID)
	saveCut(t, a, promo.ID, "", kitchen.ID)
	do[ops.ProjectLinkOut](t, a, "project.link", ops.ProjectLinkIn{ProjectID: other.ID, From: promo.ID})

	// People only, on either path.
	if err := doErr(a, "project.delete", ops.ProjectDeleteIn{ID: promo.ID}, ops.Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("a script deleted it: %v", err)
	}
	arch := true
	if err := doErr(a, "project.update", ops.ProjectUpdateIn{ID: promo.ID, Archived: &arch}, ops.Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("a script archived it: %v", err)
	}
	// Any member: not owners only.
	mctx, mc := callerHTTPMember()
	in, _ := json.Marshal(ops.ProjectDeleteIn{ID: "Spring promo"})
	out, err := ops.Call(mctx, a, "project.delete", in, mc)
	if err != nil {
		t.Fatalf("a member deletes a project: %v", err)
	}
	del := out.(*ops.ProjectArchiveOut)
	if !del.Project.Archived || del.Project.ArchivedAt == nil || del.Project.ArchivedBy == nil || del.Project.ArchivedBy.UserID != a.Actor.UserID ||
		!strings.Contains(del.Message, "Archived projects") || !strings.HasSuffix(del.ArchivedURL, ops.ArchivedProjectsPath) {
		t.Fatalf("deleted: %+v", del)
	}
	if again := do[ops.ProjectArchiveOut](t, a, "project.delete", ops.ProjectDeleteIn{ID: promo.ID}); !again.Already {
		t.Fatalf("deleting it again: %+v", again)
	}

	// Gone from the lists and search; in Archived projects.
	list := do[ops.ProjectListOut](t, a, "project.list", ops.ProjectListIn{})
	if len(list.Projects) != 1 || list.Projects[0].ID != other.ID {
		t.Fatalf("the project list: %+v", list.Projects)
	}
	archived := do[ops.ProjectListOut](t, a, "project.list", ops.ProjectListIn{ArchivedOnly: true})
	if len(archived.Projects) != 1 || archived.Projects[0].ID != promo.ID {
		t.Fatalf("archived projects: %+v", archived.Projects)
	}
	if all := do[ops.ProjectListOut](t, a, "project.list", ops.ProjectListIn{IncludeArchived: true}); len(all.Projects) != 2 {
		t.Fatalf("with archived: %d", len(all.Projects))
	}
	_ = do[models.Comment](t, a, "comment.add", ops.CommentAddIn{ProjectID: promo.ID, Body: "Spring colours please"})
	found := do[ops.SearchOut](t, a, "search", ops.SearchIn{Query: "spring"})
	if len(found.Projects) != 0 || len(found.Comments) != 0 || len(found.Revisions) != 0 {
		t.Fatalf("search finds the archived project: %+v %+v %+v", found.Projects, found.Comments, found.Revisions)
	}
	vids := do[ops.SearchOut](t, a, "search", ops.SearchIn{Query: "kitchen"})
	if len(vids.Videos) != 1 || vids.Videos[0].ProjectID != "" || vids.Videos[0].Filed != "Spring promo (archived)" {
		t.Fatalf("its footage in search: %+v", vids.Videos)
	}
	// Its footage stays in the Library, saying where.
	lib := library(t, a, ops.LibraryIn{View: ops.ViewProjects})
	if !slices.Contains(itemIDs(lib.Items), kitchen.ID) || lib.Items[0].Where != "In Spring promo (archived)" {
		t.Fatalf("its footage in the Library: %+v", lib.Items)
	}
	// A project using it keeps seeing its footage; it still opens (the dashboard says it is archived).
	if seen := do[ops.ProjectShowOut](t, a, "project.show", ops.ProjectShowIn{ID: other.ID}); len(seen.Assets) != 1 || seen.Assets[0].ID != kitchen.ID {
		t.Fatalf("the linked project's footage: %+v", seen.Assets)
	}
	if shown := do[ops.ProjectShowOut](t, a, "project.show", ops.ProjectShowIn{ID: promo.ID}); !shown.Project.Archived {
		t.Fatal("the archived project doesn't say so")
	}

	// Restore: back as it was (anyone, a script too).
	back, err := ops.CallTyped[ops.ProjectArchiveOut](ctx, a, "project.restore", ops.ProjectRestoreIn{ID: promo.ID}, ops.Caller{})
	if err != nil || back.Project.Archived || back.Project.ArchivedAt != nil || !strings.Contains(back.Message, "back in your projects") {
		t.Fatalf("restore: %+v %v", back, err)
	}
	if list := do[ops.ProjectListOut](t, a, "project.list", ops.ProjectListIn{}); len(list.Projects) != 2 {
		t.Fatalf("after restore: %d projects", len(list.Projects))
	}
	if again := do[ops.ProjectArchiveOut](t, a, "project.restore", ops.ProjectRestoreIn{ID: promo.ID}); !again.Already {
		t.Fatalf("restoring it again: %+v", again)
	}
	// project.update's archived flag is the same delete, for people.
	upd := do[ops.ProjectOut](t, a, "project.update", ops.ProjectUpdateIn{ID: promo.ID, Archived: &arch})
	if !upd.Project.Archived || upd.Project.ArchivedAt == nil {
		t.Fatalf("archived through update: %+v", upd.Project)
	}
	// Being cleared out: it can't come back.
	if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), promo.ID, bson.M{"$set": bson.M{"purging": models.ProjectPurging{At: time.Now(), By: a.Actor}}}); err != nil {
		t.Fatal(err)
	}
	if err := doErr(a, "project.restore", ops.ProjectRestoreIn{ID: promo.ID}, person); code(err) != apperr.Conflict {
		t.Fatalf("restoring one being cleared out: %v", err)
	}
}

// Clearing out is for owners, people, and archived projects only; a dry run
// says what goes and moves without changing anything, and it asks first.
func TestPurgeIsAnOwnersForArchivedProjects(t *testing.T) {
	a := testapp.New(t)
	promo := project(t, a, "Spring promo")
	kept := clip(t, a, "Kitchen chat", promo.ID)
	rev := saveCut(t, a, promo.ID, "", kept.ID)
	render(t, a, promo.ID, rev, nil)

	if err := doErr(a, "project.purge", ops.ProjectPurgeIn{ID: promo.ID, Forever: true}, person); code(err) != apperr.Conflict ||
		apperr.From(err).Details.(map[string]any)["reason"] != "not_archived" {
		t.Fatalf("clearing out a project that isn't archived: %v", err)
	}
	do[ops.ProjectArchiveOut](t, a, "project.delete", ops.ProjectDeleteIn{ID: promo.ID})
	if err := doErr(a, "project.purge", ops.ProjectPurgeIn{ID: promo.ID, Forever: true}, ops.Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("a script cleared it out: %v", err)
	}
	mctx, mc := callerHTTPMember()
	in, _ := json.Marshal(ops.ProjectPurgeIn{ID: promo.ID, Forever: true})
	if _, err := ops.Call(mctx, a, "project.purge", in, mc); apperr.From(err).Details.(map[string]any)["reason"] != "owner_only" {
		t.Fatalf("a member cleared it out: %v", err)
	}
	dry := do[ops.ProjectPurgeOut](t, a, "project.purge", ops.ProjectPurgeIn{ID: promo.ID, DryRun: true})
	if len(dry.Projects) != 1 || dry.Projects[0].Status != "would" || dry.Projects[0].Counts.Versions != 1 || dry.Projects[0].Counts.Renders != 1 ||
		dry.Projects[0].Counts.ToInbox != 1 || dry.Question != "Delete “Spring promo” for good?" {
		t.Fatalf("dry run: %+v", dry)
	}
	goes := strings.Join(dry.Projects[0].Goes, "; ")
	moves := strings.Join(dry.Projects[0].Moves, "; ")
	if !strings.Contains(goes, "1 version of its cut") || !strings.Contains(goes, "1 render") || !strings.Contains(moves, "1 clip only it had goes back to the Inbox") {
		t.Fatalf("what goes: %s | moves: %s", goes, moves)
	}
	if count(t, a, store.CRenders, bson.M{"projectId": promo.ID}) != 1 {
		t.Fatal("a dry run removed something")
	}
	if err := doErr(a, "project.purge", ops.ProjectPurgeIn{ID: promo.ID}, person); code(err) != apperr.ConfirmRequired {
		t.Fatalf("clearing out without saying it's for good: %v", err)
	}
	gone := do[ops.ProjectPurgeOut](t, a, "project.purge", ops.ProjectPurgeIn{ID: promo.ID, Forever: true})
	if gone.Purged != 1 || !strings.Contains(gone.Message, "Cleared out “Spring promo” for good") {
		t.Fatalf("cleared out: %+v", gone)
	}
	if empty := do[ops.ProjectPurgeOut](t, a, "project.purge", ops.ProjectPurgeIn{AllArchived: true, DryRun: true}); len(empty.Projects) != 0 {
		t.Fatalf("nothing left: %+v", empty)
	}
}

// What clearing out removes, moves and keeps: its versions, renders (their
// files unless something else uses the same bytes), quality checks,
// conversations and their folders, kept things, comments, its own to-dos and
// notifications, links both ways and its Content Settings go; footage only it
// had goes back to the Inbox, footage another project's cut uses through a
// link joins that project, footage elsewhere stays, nothing is deleted from
// footage; its pictures move to the workspace library; projects that followed
// its settings use the default and are told; the audit trail stays.
func TestPurgeRemovesTheProjectsThingsAndKeepsFootage(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	other := project(t, a, "Summer teaser") // the first project holds the default Content Settings
	promo := project(t, a, "Spring promo")
	follower := project(t, a, "Autumn reel")
	shelf := do[ops.CollectionOut](t, a, "collection.create", ops.CollectionCreateIn{Name: "B-roll"})

	only := clip(t, a, "Kitchen chat", promo.ID)
	shared := clip(t, a, "Harbour pan", promo.ID, other.ID)
	onShelf := clip(t, a, "Waves at dawn", promo.ID)
	do[ops.LibraryActionOut](t, a, "collection.add", ops.CollectionItemsIn{Collection: shelf.Collection.ID, AssetIDs: []string{onShelf.ID}})
	usedByOther := clip(t, a, "Interview", promo.ID)
	rev := saveCut(t, a, promo.ID, "", only.ID, onShelf.ID)
	// Summer teaser uses everything of Spring promo, and its cut plays the interview from there.
	do[ops.ProjectLinkOut](t, a, "project.link", ops.ProjectLinkIn{ProjectID: other.ID, From: promo.ID})
	saveCut(t, a, other.ID, "", usedByOther.ID, shared.ID)
	// Spring promo uses Summer teaser's things too.
	do[ops.ProjectLinkOut](t, a, "project.link", ops.ProjectLinkIn{ProjectID: promo.ID, From: other.ID})

	// Its renders: one whose video was also imported as footage (the same bytes).
	r1, q1 := render(t, a, promo.ID, rev, nil)
	videoBytes := randomBytes(3000)
	r2, _ := render(t, a, promo.ID, rev, videoBytes)
	imported := clip(t, a, "Imported render") // in the Inbox
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), imported.ID, bson.M{"$set": bson.M{"sha256": r2.Output.SHA256, "storageKey": r2.Output.StorageKey}}); err != nil {
		t.Fatal(err)
	}

	// Its Content Settings, which another project follows.
	do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: promo.ID})
	do[ops.ContentShowOut](t, a, "content.follow", ops.ContentFollowIn{ProjectID: follower.ID, Follow: promo.ID})
	// A picture of its own, a comment, a to-do, a notification, a kept thing.
	res := models.Resource{ID: ids.New("res"), WorkspaceID: a.WorkspaceID(), Name: "logo", Title: "Logo", Kind: models.ResourceImage, Scope: models.ScopeProject, ProjectID: promo.ID,
		Versions: []models.ResourceVersion{}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := a.DB.Insert(ctx, store.CResources, res); err != nil {
		t.Fatal(err)
	}
	do[models.Comment](t, a, "comment.add", ops.CommentAddIn{ProjectID: promo.ID, Body: "Warmer please"})
	for _, x := range []struct {
		coll string
		doc  any
	}{
		{store.CTodos, models.Todo{ID: ids.New("todo"), WorkspaceID: a.WorkspaceID(), Kind: models.TodoBriefMissing, Scope: "project", ProjectID: promo.ID, ProjectIDs: []string{promo.ID}, GroupKey: "brief:" + promo.ID, State: "open"}},
		{store.CNotifications, models.Notification{ID: ids.New("ntf"), WorkspaceID: a.WorkspaceID(), UserID: a.Actor.UserID, Kind: "render_finished", GroupKey: "r:" + r1.ID, ProjectID: promo.ID, Title: "Review copy ready"}},
		{store.CKeep, models.KeepItem{ID: ids.New("kp"), WorkspaceID: a.WorkspaceID(), ProjectID: promo.ID, Kind: "rule", Key: "rule:warm", Text: "Warm colours", State: "kept"}},
	} {
		if err := a.DB.Insert(ctx, x.coll, x.doc); err != nil {
			t.Fatal(err)
		}
	}
	// A Claude Code conversation with a saved proposal on this machine.
	conv := ids.New("asc")
	if _, err := a.DB.C(store.CAssistantConversations).InsertOne(ctx, bson.M{"_id": conv, "workspaceId": a.WorkspaceID(), "projectId": promo.ID, "installationId": a.Cfg.Installation.ID,
		"title": "A slower opening", "state": "idle", "archived": false, "turns": bson.A{}, "events": bson.A{}, "actions": bson.A{}, "queue": bson.A{}}); err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(a.Cfg.AssistantDir(), "conversations", conv, "proposals", "t1")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(folder, "cut.json"), []byte(`{}`), 0o600)

	do[ops.ProjectArchiveOut](t, a, "project.delete", ops.ProjectDeleteIn{ID: promo.ID})
	dry := do[ops.ProjectPurgeOut](t, a, "project.purge", ops.ProjectPurgeIn{ID: promo.ID, DryRun: true}).Projects[0]
	c := dry.Counts
	if c.ToInbox != 1 || c.Joins != 1 || c.Stays != 2 || c.Resources != 1 || c.Links != 2 || c.Conversations != 1 || c.Kept != 1 || c.Comments != 1 || c.Todos < 1 ||
		!c.ContentSettings || len(c.Followers) != 1 || c.Followers[0] != "Autumn reel" || c.Renders != 2 || c.Checks != 2 {
		t.Fatalf("what it says goes and moves: %+v", c)
	}
	out := do[ops.ProjectPurgeOut](t, a, "project.purge", ops.ProjectPurgeIn{ID: promo.ID, Forever: true})
	if out.Purged != 1 || out.Projects[0].Counts.StoredKept != 1 || out.Projects[0].Counts.StoredRemoved != 5 {
		t.Fatalf("cleared out: %+v", out.Projects[0].Counts)
	}

	// The project's things are gone.
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), promo.ID, &models.Project{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the project is still there: %v", err)
	}
	for _, coll := range []string{store.CRevisions, store.CRenders, store.CQAReports, store.CComments, store.CKeep, store.CNotifications, store.CTodos, store.CContentSettings, store.CAssistantConversations} {
		if n := count(t, a, coll, bson.M{"projectId": promo.ID}); n != 0 {
			t.Errorf("%s: %d left", coll, n)
		}
	}
	if n := count(t, a, store.CProjectLinks, bson.M{"$or": bson.A{bson.M{"projectId": promo.ID}, bson.M{"sourceId": promo.ID}}}); n != 0 {
		t.Errorf("%d links left", n)
	}
	if _, err := os.Stat(filepath.Join(a.Cfg.AssistantDir(), "conversations", conv)); !os.IsNotExist(err) {
		t.Errorf("the conversation's folder is still there: %v", err)
	}
	// Stored files: the renders' and the checks' go, the bytes footage uses stay.
	if isStored(a, r1.Output.StorageKey) || isStored(a, r1.Output.ThumbnailKey) || isStored(a, q1.Frames[0].Key) {
		t.Error("a render's files are still stored")
	}
	if !isStored(a, r2.Output.StorageKey) {
		t.Error("a render's video that footage also is was deleted")
	}
	// Footage is never deleted.
	for _, as := range []models.Asset{only, shared, onShelf, usedByOther, imported} {
		got := reload(t, a, as.ID)
		if slices.Contains(got.ProjectIDs, promo.ID) || !isStored(a, as.StorageKey) {
			t.Errorf("%s: %+v", as.Title, got.ProjectIDs)
		}
	}
	inbox := library(t, a, ops.LibraryIn{})
	if !slices.Contains(itemIDs(inbox.Items), only.ID) || slices.Contains(itemIDs(inbox.Items), onShelf.ID) {
		t.Errorf("the Inbox: %v", itemIDs(inbox.Items))
	}
	if got := reload(t, a, usedByOther.ID); !slices.Equal(got.ProjectIDs, []string{other.ID}) {
		t.Errorf("the clip Summer teaser's cut uses: %v", got.ProjectIDs)
	}
	if got := reload(t, a, shared.ID); !slices.Equal(got.ProjectIDs, []string{other.ID}) {
		t.Errorf("the shared clip: %v", got.ProjectIDs)
	}
	// Its picture is in the workspace library now.
	var moved models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), res.ID, &moved); err != nil || moved.Scope != models.ScopeWorkspace || moved.PromotedFrom != promo.ID {
		t.Errorf("its picture: %+v %v", moved, err)
	}
	// The project that followed its settings uses the default, and its maker is told.
	cs := do[ops.ContentShowOut](t, a, "content.show", ops.ContentShowIn{ProjectID: follower.ID})
	if cs.Uses != models.ContentDefault {
		t.Errorf("the follower uses %q", cs.Uses)
	}
	if n := count(t, a, store.CNotifications, bson.M{"kind": models.NotifyContentSettings, "projectId": follower.ID}); n == 0 {
		t.Error("nobody was told the follower uses the default now")
	}
	// The audit trail stays and says so.
	if n := count(t, a, store.CAudit, bson.M{"action": "project.purge", "targetId": promo.ID}); n != 1 {
		t.Errorf("the audit trail has %d clear-outs", n)
	}
	if n := count(t, a, store.CAudit, bson.M{"projectId": promo.ID, "action": "project.delete"}); n != 1 {
		t.Errorf("the audit trail lost the delete: %d", n)
	}
}

// A project another project's cut plays inside it can't be cleared out (the
// answer names it); cleared out together, the one that plays the other goes first.
func TestPurgeRefusedWhileNested(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	inner := project(t, a, "Logo sting")
	outer := project(t, a, "Launch film")
	c := clip(t, a, "Sting", inner.ID)
	rev := saveCut(t, a, inner.ID, "", c.ID)
	// The outer cut plays the inner one (a nested cut).
	spec := editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30},
		Clips: []editspec.Clip{{ID: "sting", AssetID: editspec.NestedKey(inner.ID, rev), In: 0, Out: 2}}}
	outerRev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: outer.ID, Branch: "main", Number: 1, Spec: spec, CreatedAt: time.Now()}
	if err := a.DB.Insert(ctx, store.CRevisions, outerRev); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), outer.ID, bson.M{"$set": bson.M{"branches.main": models.BranchHead{Head: outerRev.ID, UpdatedAt: time.Now()}}}); err != nil {
		t.Fatal(err)
	}
	do[ops.ProjectArchiveOut](t, a, "project.delete", ops.ProjectDeleteIn{ID: inner.ID})
	err := doErr(a, "project.purge", ops.ProjectPurgeIn{ID: inner.ID, Forever: true}, person)
	if code(err) != apperr.Conflict || !strings.Contains(err.Error(), "“Launch film” plays it inside its cut (version 1)") {
		t.Fatalf("clearing out a nested cut: %v", err)
	}
	dry := do[ops.ProjectPurgeOut](t, a, "project.purge", ops.ProjectPurgeIn{ID: inner.ID, DryRun: true})
	if dry.Refused != 1 || dry.Projects[0].Status != "refused" {
		t.Fatalf("the dry run: %+v", dry)
	}
	// Both archived: cleared out together, the outer first.
	do[ops.ProjectArchiveOut](t, a, "project.delete", ops.ProjectDeleteIn{ID: outer.ID})
	both := do[ops.ProjectPurgeOut](t, a, "project.purge", ops.ProjectPurgeIn{AllArchived: true, Forever: true})
	if both.Purged != 2 || both.Refused != 0 {
		t.Fatalf("both: %+v", both)
	}
	if n := count(t, a, store.CProjects, bson.M{}); n != 0 {
		t.Fatalf("%d projects left", n)
	}
	if got := reload(t, a, c.ID); len(got.ProjectIDs) != 0 {
		t.Fatalf("the sting's footage: %v", got.ProjectIDs)
	}
}

// A clear-out stopped halfway leaves the project archived and marked; it can't
// be restored, and running it again finishes it (every step can run again).
func TestPurgeStoppedHalfwayFinishesWhenRunAgain(t *testing.T) {
	a := testapp.New(t)
	promo := project(t, a, "Spring promo")
	only := clip(t, a, "Kitchen chat", promo.ID)
	rev := saveCut(t, a, promo.ID, "", only.ID)
	r, _ := render(t, a, promo.ID, rev, nil)
	do[ops.ProjectArchiveOut](t, a, "project.delete", ops.ProjectDeleteIn{ID: promo.ID})

	for _, stopAt := range []string{"footage", "files"} {
		ops.SetPurgeStep(func(step string) error {
			if step == stopAt {
				return errors.New("the power went out")
			}
			return nil
		})
		err := doErr(a, "project.purge", ops.ProjectPurgeIn{ID: promo.ID, Forever: true}, person)
		ops.SetPurgeStep(nil)
		if err == nil || !strings.Contains(err.Error(), "Run it again to finish it") {
			t.Fatalf("stopped at %s: %v", stopAt, err)
		}
		var p models.Project
		if err := a.DB.GetByID(context.Background(), store.CProjects, a.WorkspaceID(), promo.ID, &p); err != nil || p.Purging == nil || !p.Archived {
			t.Fatalf("stopped at %s: the project %+v %v", stopAt, p, err)
		}
		if err := doErr(a, "project.restore", ops.ProjectRestoreIn{ID: promo.ID}, person); code(err) != apperr.Conflict {
			t.Fatalf("restoring one half cleared out: %v", err)
		}
		if l := do[ops.ProjectListOut](t, a, "project.list", ops.ProjectListIn{ArchivedOnly: true}); len(l.Projects) != 1 || l.Projects[0].Purging == nil {
			t.Fatalf("archived projects while stopped: %+v", l.Projects)
		}
	}
	done := do[ops.ProjectPurgeOut](t, a, "project.purge", ops.ProjectPurgeIn{ID: promo.ID, Forever: true})
	if done.Purged != 1 {
		t.Fatalf("run again: %+v", done)
	}
	if isStored(a, r.Output.StorageKey) || count(t, a, store.CRenders, bson.M{"projectId": promo.ID}) != 0 {
		t.Fatal("the renders weren't cleared out")
	}
	if got := reload(t, a, only.ID); len(got.ProjectIDs) != 0 || !isStored(a, only.StorageKey) {
		t.Fatalf("the footage: %+v", got.ProjectIDs)
	}
}
