package ops_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// The Library (D96–D98) against the test database: the Inbox as a triage
// pile, the Archive, collections (and a project using one through a link),
// the two-step Trash with its reference checks, emptying it only when a
// person turned that on, and a search across every view.

var person = ops.Caller{Human: true}

func do[T any](t *testing.T, a *app.App, op string, in any) *T {
	t.Helper()
	out, err := ops.CallTyped[T](context.Background(), a, op, in, person)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return out
}

func doErr(a *app.App, op string, in any, c ops.Caller) error {
	b, _ := json.Marshal(in)
	_, err := ops.Call(context.Background(), a, op, b, c)
	return err
}

func code(err error) string { return apperr.From(err).Code }

func project(t *testing.T, a *app.App, name string) models.Project {
	t.Helper()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: name, Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(context.Background(), store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	// Like project.create: a workspace's first project holds the default Content Settings (D192).
	if _, err := store.MoveContentSettings(context.Background(), a.DB, a.WorkspaceID()); err != nil {
		t.Fatal(err)
	}
	return p
}

// clip stores a small "video" (its bytes) and records it as available footage.
func clip(t *testing.T, a *app.App, title string, projectIDs ...string) models.Asset {
	t.Helper()
	ctx := context.Background()
	b := make([]byte, 2048)
	_, _ = rand.Read(b)
	p := filepath.Join(t.TempDir(), "f.mov")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(ctx, key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	if projectIDs == nil {
		projectIDs = []string{}
	}
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: projectIDs, Kind: "video", Name: strings.ToLower(strings.ReplaceAll(title, " ", "-")) + ".mov", Title: title,
		SHA256: sha, Size: int64(len(b)), StorageKey: key, State: models.AssetAvailable, Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 10, Width: 1920, Height: 1080, FPS: 30},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	return as
}

func reload(t *testing.T, a *app.App, id string) models.Asset {
	t.Helper()
	var as models.Asset
	if err := a.DB.GetByID(context.Background(), store.CAssets, a.WorkspaceID(), id, &as); err != nil {
		t.Fatalf("asset %s: %v", id, err)
	}
	return as
}

// saveCut checks then saves a whole cut playing the given clips.
func saveCut(t *testing.T, a *app.App, projectID, base string, clipIDs ...string) string {
	t.Helper()
	spec := editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30, Platform: "shorts"}}
	for i, id := range clipIDs {
		spec.Clips = append(spec.Clips, editspec.Clip{ID: "c" + string(rune('a'+i)), AssetID: id, In: 0, Out: 3})
	}
	plan, _ := json.Marshal(editspec.Plan{ProjectID: projectID, BaseRevisionID: base, Instruction: "test cut", Spec: &spec})
	chk := do[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: plan})
	if !chk.Valid {
		t.Fatalf("cut not valid: %+v", chk.Errors)
	}
	return do[ops.EditApplyOut](t, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}).Revision.ID
}

func library(t *testing.T, a *app.App, in ops.LibraryIn) *ops.LibraryOut {
	t.Helper()
	return do[ops.LibraryOut](t, a, "library.list", in)
}

func itemIDs(items []ops.LibraryItem) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func TestInboxArchiveAndBack(t *testing.T) {
	a := testapp.New(t)
	promo := project(t, a, "Spring promo")
	loose := clip(t, a, "Pan across the lake")
	inPromo := clip(t, a, "Kitchen chat", promo.ID)
	used := clip(t, a, "Interview", promo.ID)
	saveCut(t, a, promo.ID, "", used.ID)

	lib := library(t, a, ops.LibraryIn{})
	if lib.View != ops.ViewInbox || !slices.Equal(itemIDs(lib.Items), []string{loose.ID}) || lib.Counts.Inbox != 1 || lib.Counts.Projects != 2 {
		t.Fatalf("inbox %+v counts %+v", itemIDs(lib.Items), lib.Counts)
	}
	if lib.Items[0].Where != "In the Inbox" {
		t.Fatalf("where %q", lib.Items[0].Where)
	}

	// Archive: out of the Inbox, on the one unnamed shelf, nothing deleted.
	out := do[ops.LibraryActionOut](t, a, "asset.archive", ops.LibraryActionIn{AssetIDs: []string{"Pan across the lake"}})
	if out.Done != 1 || out.Message != "Archived “Pan across the lake”." || out.Items[0].Where != "In the Archive" {
		t.Fatalf("archive %+v", out)
	}
	if l := library(t, a, ops.LibraryIn{}); l.Counts.Inbox != 0 || l.Counts.Archived != 1 {
		t.Fatalf("after archive %+v", l.Counts)
	}
	inbox := do[ops.AssetListOut](t, a, "asset.list", ops.AssetListIn{Unassigned: true})
	if len(inbox.Assets) != 0 {
		t.Fatalf("asset list --unassigned still has %d", len(inbox.Assets))
	}
	again := do[ops.LibraryActionOut](t, a, "asset.archive", ops.LibraryActionIn{AssetIDs: []string{loose.ID}})
	if again.Already != 1 || again.Done != 0 {
		t.Fatalf("archived twice %+v", again)
	}
	back := do[ops.LibraryActionOut](t, a, "asset.unarchive", ops.LibraryActionIn{AssetIDs: []string{loose.ID}})
	if back.Done != 1 || !strings.Contains(back.Message, "in the Inbox now") {
		t.Fatalf("unarchive %+v", back)
	}

	// From a project's Sources: it leaves that project for the Archive, unless the project's cut uses it.
	fromProject := do[ops.LibraryActionOut](t, a, "asset.archive", ops.LibraryActionIn{AssetIDs: []string{inPromo.ID}, ProjectID: "Spring promo"})
	if fromProject.Done != 1 || len(reload(t, a, inPromo.ID).ProjectIDs) != 0 || reload(t, a, inPromo.ID).ArchivedAt == nil {
		t.Fatalf("archive from project %+v", fromProject)
	}
	err := doErr(a, "asset.archive", ops.LibraryActionIn{AssetIDs: []string{used.ID}, ProjectID: promo.ID}, person)
	if code(err) != apperr.Conflict || !strings.Contains(err.Error(), "Spring promo's cut (version 1) uses it") {
		t.Fatalf("archiving what the cut uses: %v", err)
	}
	// Several at once: one goes, one is refused, and the answer says which.
	spare := clip(t, a, "Spare", promo.ID)
	mixed := do[ops.LibraryActionOut](t, a, "asset.archive", ops.LibraryActionIn{AssetIDs: []string{used.ID, spare.ID}, ProjectID: promo.ID})
	if mixed.Done != 1 || mixed.Refused != 1 || mixed.Message != "Archived “Spare”. “Interview” wasn't archived: Spring promo's cut (version 1) uses it; take it out of the cut first." {
		t.Fatalf("mixed %+v", mixed)
	}
	// Nothing at all could be done: the whole call is refused, with the reasons.
	if err := doErr(a, "asset.archive", ops.LibraryActionIn{AssetIDs: []string{used.ID, loose.ID}, ProjectID: promo.ID}, person); code(err) != apperr.Conflict ||
		!strings.Contains(err.Error(), "it isn't in Spring promo") {
		t.Fatalf("all refused: %v", err)
	}
}

func TestCollectionsAndAProjectUsingOne(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	promo := project(t, a, "Spring promo")
	waves := clip(t, a, "Waves")
	gulls := clip(t, a, "Gulls")
	kid := clip(t, a, "Birthday")

	do[ops.CollectionOut](t, a, "collection.create", ops.CollectionCreateIn{Name: "B-roll"})
	if err := doErr(a, "collection.create", ops.CollectionCreateIn{Name: "  b-ROLL "}, person); code(err) != apperr.Conflict {
		t.Fatalf("same name again: %v", err)
	}
	add := do[ops.LibraryActionOut](t, a, "collection.add", ops.CollectionItemsIn{Collection: "b-roll", AssetIDs: []string{waves.ID, "Gulls"}})
	if add.Done != 2 || add.Collection == nil || add.Collection.Count != 2 || add.Message != "Added 2 clips to B-roll." {
		t.Fatalf("add %+v", add)
	}
	if err := doErr(a, "collection.add", ops.CollectionItemsIn{Collection: "Family", AssetIDs: []string{kid.ID}}, person); code(err) != apperr.NotFound {
		t.Fatalf("a collection that doesn't exist: %v", err)
	}
	fam := do[ops.LibraryActionOut](t, a, "collection.add", ops.CollectionItemsIn{Collection: "Family", AssetIDs: []string{kid.ID, waves.ID}, Create: true})
	if !fam.Created || fam.Done != 2 || !strings.HasPrefix(fam.Message, "Created the Family collection.") {
		t.Fatalf("add with create %+v", fam)
	}
	// With create, only that exact name counts: "Fam" is a new collection, not Family.
	fm := do[ops.LibraryActionOut](t, a, "collection.add", ops.CollectionItemsIn{Collection: "Fam", AssetIDs: []string{kid.ID}, Create: true})
	if !fm.Created || fm.Collection == nil || fm.Collection.Name != "Fam" {
		t.Fatalf("a close name took the clip: %+v", fm)
	}
	do[ops.CollectionDeleteOut](t, a, "collection.delete", ops.CollectionRefIn{Collection: fm.Collection.ID})
	// The same name again (any case) is the same collection.
	again := do[ops.LibraryActionOut](t, a, "collection.add", ops.CollectionItemsIn{Collection: "FAMILY", AssetIDs: []string{kid.ID}, Create: true})
	if again.Created || again.Already != 1 {
		t.Fatalf("same name again %+v", again)
	}
	lib := library(t, a, ops.LibraryIn{})
	if lib.Counts.Inbox != 0 || lib.Counts.Collections != 3 || len(lib.Collections) != 2 {
		t.Fatalf("library %+v %+v", lib.Counts, lib.Collections)
	}
	broll := library(t, a, ops.LibraryIn{Collection: "B-roll"})
	if broll.Collection == nil || broll.Total != 2 || broll.Summary != "2 clips on the B-roll collection." {
		t.Fatalf("one collection %+v", broll)
	}
	for _, it := range broll.Items {
		if it.ID == waves.ID && it.Where != "In the B-roll and Family collections" {
			t.Fatalf("where %q", it.Where)
		}
	}

	// "Use everything from B-roll in this project": a project link to a collection.
	link := do[ops.ProjectLinkOut](t, a, "project.link", ops.ProjectLinkIn{ProjectID: promo.ID, From: "B-roll"})
	if link.Link == nil || link.Link.SourceKind != "collection" || link.Link.Counts.Footage != 2 || !strings.Contains(link.Message, "B-roll collection") {
		t.Fatalf("link %+v", link)
	}
	show := do[ops.ProjectShowOut](t, a, "project.show", ops.ProjectShowIn{ID: promo.ID})
	var seen []string
	for _, as := range show.Assets {
		if as.FromCollection && as.From == "B-roll" {
			seen = append(seen, as.ID)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("the project doesn't see the collection's clips: %+v", show.Assets)
	}
	// Its cut may use them; new clips on the collection show up by themselves.
	saveCut(t, a, promo.ID, "", waves.ID)
	extra := clip(t, a, "Harbour")
	do[ops.LibraryActionOut](t, a, "collection.add", ops.CollectionItemsIn{Collection: "B-roll", AssetIDs: []string{extra.ID}})
	if l := do[ops.ProjectLinksOut](t, a, "project.links", ops.ProjectLinksIn{ProjectID: promo.ID}); l.Links[0].Counts.Footage != 3 {
		t.Fatalf("new clip on the collection: %+v", l.Links[0])
	}
	where := do[ops.AssetWhereOut](t, a, "asset.where", ops.AssetRefIn{AssetID: waves.ID})
	var texts []string
	for _, u := range where.Uses {
		texts = append(texts, u.Text)
	}
	joined := strings.Join(texts, " | ")
	if !strings.Contains(joined, "On the B-roll collection") || !strings.Contains(joined, "On the Family collection") ||
		!strings.Contains(joined, "Seen in Spring promo through its link to the B-roll collection") || !strings.Contains(joined, "Used in Spring promo's cut") {
		t.Fatalf("where %s", joined)
	}
	// Taking a clip the cut uses off the collection is refused; another one is fine.
	if err := doErr(a, "collection.remove", ops.CollectionItemsIn{Collection: "B-roll", AssetIDs: []string{waves.ID}}, person); code(err) != apperr.Conflict {
		t.Fatalf("remove what the cut uses: %v", err)
	}
	rm := do[ops.LibraryActionOut](t, a, "collection.remove", ops.CollectionItemsIn{Collection: "B-roll", AssetIDs: []string{gulls.ID}})
	if rm.Done != 1 || rm.Items[0].Where != "In the Inbox" {
		t.Fatalf("remove %+v", rm)
	}
	// Renaming keeps the link; deleting the collection deletes no clip, and the
	// clip the cut uses through it joins the project.
	do[ops.CollectionOut](t, a, "collection.rename", ops.CollectionRenameIn{Collection: "B-roll", Name: "Stock footage"})
	if l := do[ops.ProjectLinksOut](t, a, "project.links", ops.ProjectLinksIn{ProjectID: promo.ID}); l.Links[0].SourceName != "Stock footage" {
		t.Fatalf("renamed link %+v", l.Links[0])
	}
	del := do[ops.CollectionDeleteOut](t, a, "collection.delete", ops.CollectionRefIn{Collection: "stock footage"})
	if del.Clips != 2 || len(del.Kept) != 1 || del.Kept[0].ID != waves.ID || !strings.Contains(del.Message, "Spring promo's cut uses 1 clip from it") {
		t.Fatalf("delete %+v", del)
	}
	if w := reload(t, a, waves.ID); !slices.Contains(w.ProjectIDs, promo.ID) || slices.ContainsFunc(w.CollectionIDs, func(c string) bool { return c == link.Link.SourceID }) {
		t.Fatalf("waves after delete %+v", w)
	}
	if h := reload(t, a, extra.ID); !h.InInbox() {
		t.Fatalf("harbour should be back in the Inbox: %+v", h)
	}
	n, _ := a.DB.C(store.CProjectLinks).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": promo.ID})
	if n != 0 {
		t.Fatalf("the link to the deleted collection is still there")
	}
}

func TestTrashIsTwoSteps(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	promo := project(t, a, "Spring promo")
	summer := project(t, a, "Summer")
	blur := clip(t, a, "Blurry take")
	inTwo := clip(t, a, "Shared shot", promo.ID, summer.ID)
	live := clip(t, a, "Opening", promo.ID)
	old := clip(t, a, "Old idea", promo.ID)
	r1 := saveCut(t, a, promo.ID, "", live.ID, old.ID)
	saveCut(t, a, promo.ID, r1, live.ID) // version 2 no longer uses "Old idea"
	do[ops.LibraryActionOut](t, a, "collection.add", ops.CollectionItemsIn{Collection: "Outtakes", AssetIDs: []string{blur.ID}, Create: true})
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CTodos, models.Todo{ID: ids.New(ids.Todo), WorkspaceID: a.WorkspaceID(), Kind: models.TodoMusicRights, GroupKey: "rights:" + blur.ID,
		AssetID: blur.ID, State: models.TodoOpen, Question: "May we use the music?", ProjectIDs: []string{}, Suggestions: []models.TodoSuggestion{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// Move to Trash is instant: out of its collection, its questions put aside.
	tr := do[ops.LibraryActionOut](t, a, "asset.trash", ops.LibraryActionIn{AssetIDs: []string{blur.ID}})
	if tr.Done != 1 || tr.Message != "Moved “Blurry take” to the Trash." {
		t.Fatalf("trash %+v", tr)
	}
	b := reload(t, a, blur.ID)
	if b.Trash == nil || len(b.CollectionIDs) != 0 || len(b.Trash.CollectionIDs) != 1 || b.InInbox() {
		t.Fatalf("trashed %+v", b)
	}
	var open []models.Todo
	_ = a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"assetId": blur.ID, "state": models.TodoOpen}, &open)
	if len(open) != 0 {
		t.Fatalf("questions about a clip in the Trash are still open")
	}
	trash := do[ops.TrashListOut](t, a, "trash.list", ops.TrashListIn{})
	if trash.Count != 1 || trash.Items[0].Trash == nil || trash.Items[0].Trash.From != "From the Outtakes collection" || !trash.Items[0].CanDelete ||
		!strings.Contains(trash.Summary, "Nothing here is deleted until you empty the Trash") {
		t.Fatalf("trash list %+v", trash)
	}
	// Global search leaves the Trash out; the Library's search finds it.
	if s := do[ops.SearchOut](t, a, "search", ops.SearchIn{Query: "blurry"}); len(s.Videos) != 0 {
		t.Fatalf("search found a clip in the Trash: %+v", s.Videos)
	}
	if l := library(t, a, ops.LibraryIn{Query: "blurry"}); l.Counts.Trash != 1 || len(l.Items) != 1 || l.Items[0].MatchedIn != "title" {
		t.Fatalf("library search %+v", l)
	}
	// Restore puts it back where it was, questions too.
	rs := do[ops.LibraryActionOut](t, a, "asset.restore", ops.LibraryActionIn{AssetIDs: []string{"Blurry take"}})
	if rs.Done != 1 || rs.Message != "Restored “Blurry take”. It's back in the Outtakes collection." {
		t.Fatalf("restore %+v", rs)
	}
	_ = a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"assetId": blur.ID, "state": models.TodoOpen}, &open)
	if len(open) != 1 {
		t.Fatalf("the question didn't come back: %d open", len(open))
	}

	// Refused: another project has it; any project's latest cut uses it.
	err := doErr(a, "asset.trash", ops.LibraryActionIn{AssetIDs: []string{inTwo.ID}, ProjectID: promo.ID}, person)
	if code(err) != apperr.Conflict || !strings.Contains(err.Error(), "it's also in Summer") {
		t.Fatalf("trash a clip another project has: %v", err)
	}
	err = doErr(a, "asset.trash", ops.LibraryActionIn{AssetIDs: []string{live.ID}, ProjectID: promo.ID}, person)
	if code(err) != apperr.Conflict || !strings.Contains(err.Error(), "Spring promo's cut (version 2") {
		t.Fatalf("trash what the cut uses: %v", err)
	}
	// From its project's Sources: allowed while only an older version uses it.
	gone := do[ops.LibraryActionOut](t, a, "asset.trash", ops.LibraryActionIn{AssetIDs: []string{old.ID}, ProjectID: "Spring promo"})
	if gone.Done != 1 || len(reload(t, a, old.ID).ProjectIDs) != 0 {
		t.Fatalf("trash from project %+v", gone)
	}
	do[ops.LibraryActionOut](t, a, "asset.trash", ops.LibraryActionIn{AssetIDs: []string{blur.ID}})

	// Deleting for good is for people only, with sizes; a clip an older cut uses stays.
	if err := doErr(a, "trash.empty", ops.TrashEmptyIn{}, ops.Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("a script emptied the Trash: %v", err)
	}
	dry := do[ops.TrashEmptyOut](t, a, "trash.empty", ops.TrashEmptyIn{DryRun: true})
	if dry.Count != 1 || dry.Items[0].AssetID != blur.ID || len(dry.Kept) != 1 || dry.Kept[0].AssetID != old.ID ||
		!strings.HasPrefix(dry.Question, "Permanently delete “Blurry take” (2.0 KB)?") || !strings.Contains(dry.Message, "can't be undone") ||
		!strings.Contains(dry.Kept[0].Reason, "Used in an older version of Spring promo's cut") {
		t.Fatalf("dry run %+v", dry)
	}
	if err := doErr(a, "asset.delete", ops.AssetDeleteIn{AssetID: old.ID, Forever: true}, person); code(err) != apperr.Conflict {
		t.Fatalf("deleting what an older cut uses: %v", err)
	}
	emptied := do[ops.TrashEmptyOut](t, a, "trash.empty", ops.TrashEmptyIn{})
	if !emptied.Deleted || emptied.Count != 1 || emptied.Size != blur.Size || len(emptied.Kept) != 1 || emptied.Removed["questions"] != 1 {
		t.Fatalf("empty %+v", emptied)
	}
	if _, err := a.Media.Stat(ctx, blur.StorageKey); err == nil {
		t.Fatal("the clip's bytes are still stored")
	}
	if l := do[ops.TrashListOut](t, a, "trash.list", ops.TrashListIn{}); l.Count != 1 || l.Items[0].CanDelete || len(l.Items[0].Uses) != 1 {
		t.Fatalf("what stays %+v", l)
	}
	// A clip in the Trash can't be put anywhere until it is restored.
	if err := doErr(a, "asset.link", ops.AssetLinkIn{AssetID: old.ID, ProjectID: summer.ID}, person); code(err) != apperr.Conflict {
		t.Fatalf("add a clip in the Trash to a project: %v", err)
	}
	if err := doErr(a, "collection.add", ops.CollectionItemsIn{Collection: "Outtakes", AssetIDs: []string{old.ID}}, person); code(err) != apperr.Conflict {
		t.Fatalf("add a clip in the Trash to a collection: %v", err)
	}
	back := do[ops.LibraryActionOut](t, a, "asset.restore", ops.LibraryActionIn{AssetIDs: []string{old.ID}})
	if back.Message != "Restored “Old idea”. It's back in Spring promo." || !slices.Contains(reload(t, a, old.ID).ProjectIDs, promo.ID) {
		t.Fatalf("restore into its project %+v", back)
	}
}

func TestTrashEmptiesItselfOnlyWhenAPersonSaysSo(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T"}); err != nil {
		t.Fatal(err)
	}
	promo := project(t, a, "Spring promo")
	stale := clip(t, a, "Long gone")
	recent := clip(t, a, "Yesterday")
	keep := clip(t, a, "Still used", promo.ID)
	r1 := saveCut(t, a, promo.ID, "", keep.ID)
	saveCut(t, a, promo.ID, r1, clip(t, a, "Other", promo.ID).ID)
	do[ops.LibraryActionOut](t, a, "asset.trash", ops.LibraryActionIn{AssetIDs: []string{stale.ID, recent.ID}})
	do[ops.LibraryActionOut](t, a, "asset.trash", ops.LibraryActionIn{AssetIDs: []string{keep.ID}, ProjectID: promo.ID})
	backdate := func(id string, days int) {
		t.Helper()
		if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), id, bson.M{"$set": bson.M{"trash.at": time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)}}); err != nil {
			t.Fatal(err)
		}
	}
	backdate(stale.ID, 40)
	backdate(keep.ID, 40)
	backdate(recent.ID, 1)

	// Off by default: nothing goes, however old.
	if n, err := ops.EmptyExpiredTrash(ctx, a, time.Now().UTC()); err != nil || n != 0 {
		t.Fatalf("emptied while off: %d %v", n, err)
	}
	if s := do[ops.TrashSettingsOut](t, a, "trash.settings", ops.TrashSettingsIn{}); s.EmptyAfterDays != 0 || s.Summary != "Nothing in the Trash is deleted until you empty it." {
		t.Fatalf("settings %+v", s)
	}
	// Turning it on is for people only.
	if err := doErr(a, "trash.configure", ops.TrashConfigureIn{EmptyAfterDays: 30}, ops.Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("a script turned on emptying: %v", err)
	}
	set := do[ops.TrashSettingsOut](t, a, "trash.configure", ops.TrashConfigureIn{EmptyAfterDays: 30})
	if set.EmptyAfterDays != 30 || set.UpdatedBy == "" {
		t.Fatalf("configure %+v", set)
	}
	if l := library(t, a, ops.LibraryIn{View: ops.ViewTrash}); l.EmptyAfterDays != 30 || l.Items[0].Trash.DeletesOn == nil {
		t.Fatalf("trash view %+v", l)
	}
	n, err := ops.EmptyExpiredTrash(ctx, a, time.Now().UTC())
	if err != nil || n != 1 {
		t.Fatalf("emptied %d %v", n, err)
	}
	var left []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"trash": bson.M{"$exists": true}}, &left)
	var titles []string
	for _, as := range left {
		titles = append(titles, as.DisplayTitle())
	}
	slices.Sort(titles)
	if !slices.Equal(titles, []string{"Still used", "Yesterday"}) {
		t.Fatalf("left in the Trash: %v", titles)
	}
	// A second machine running at the same time finds nothing left to do.
	if n, _ := ops.EmptyExpiredTrash(ctx, a, time.Now().UTC()); n != 0 {
		t.Fatalf("emptied again: %d", n)
	}
	do[ops.TrashSettingsOut](t, a, "trash.configure", ops.TrashConfigureIn{EmptyAfterDays: 0})
	backdate(recent.ID, 400)
	if n, _ := ops.EmptyExpiredTrash(ctx, a, time.Now().UTC()); n != 0 {
		t.Fatalf("emptied after turning it off: %d", n)
	}
}

// Adding the same file again brings a clip back from the Trash (the person
// wants it); a copy under another name that is skipped leaves it there.
func TestReimportBringsAClipBackFromTheTrash(t *testing.T) {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) || !ts.Has(tools.FFprobe) {
		t.Skip("ffmpeg not installed")
	}
	a := testapp.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	p := filepath.Join(dir, "garden.mp4")
	if out, err := exec.Command(ts.Path(tools.FFmpeg), "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=160x90:rate=10:duration=1",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-shortest", "-pix_fmt", "yuv420p", p).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	first := ingest.File(ctx, a, p, ingest.Options{Via: "cli"})
	if first.Status != "ingested" || first.AssetID == "" {
		t.Fatalf("first import %+v", first)
	}
	do[ops.LibraryActionOut](t, a, "asset.trash", ops.LibraryActionIn{AssetIDs: []string{first.AssetID}})
	// Another name for the same bytes, skipped: it stays in the Trash, and says so.
	copyPath := filepath.Join(dir, "garden-copy.mp4")
	b, _ := os.ReadFile(p)
	if err := os.WriteFile(copyPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	skipped := ingest.File(ctx, a, copyPath, ingest.Options{Via: "cli"})
	if skipped.Status != "duplicate" || skipped.Duplicate == nil || skipped.Duplicate.Where != "the Trash" || reload(t, a, first.AssetID).Trash == nil {
		t.Fatalf("skipped copy %+v %+v", skipped, skipped.Duplicate)
	}
	// The same file again: back where it was (the Inbox).
	again := ingest.File(ctx, a, p, ingest.Options{Via: "cli"})
	if again.Status != "deduped" || again.Duplicate == nil || again.Duplicate.Where != "Inbox" {
		t.Fatalf("again %+v %+v", again, again.Duplicate)
	}
	if as := reload(t, a, first.AssetID); as.Trash != nil || !as.InInbox() {
		t.Fatalf("not back from the Trash: %+v", as)
	}
}

func TestLibrarySearchCoversEveryView(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	promo := project(t, a, "Spring promo")
	archived := clip(t, a, "Beach day")
	trashed := clip(t, a, "Beach sunset")
	onShelf := clip(t, a, "Kitchen")
	said := clip(t, a, "Interview")
	inProject := clip(t, a, "Beach run", promo.ID)
	do[ops.LibraryActionOut](t, a, "asset.archive", ops.LibraryActionIn{AssetIDs: []string{archived.ID}})
	do[ops.LibraryActionOut](t, a, "asset.trash", ops.LibraryActionIn{AssetIDs: []string{trashed.ID}})
	do[ops.LibraryActionOut](t, a, "collection.add", ops.CollectionItemsIn{Collection: "Beach trips", AssetIDs: []string{onShelf.ID}, Create: true})
	if err := a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: said.ID, Text: "we went to the beach early", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	l := library(t, a, ops.LibraryIn{Query: "beach", View: ops.ViewAll})
	got := map[string]string{}
	for _, it := range l.Items {
		got[it.ID] = it.MatchedIn
	}
	want := map[string]string{archived.ID: "title", trashed.ID: "title", onShelf.ID: "collection", said.ID: "words", inProject.ID: "title"}
	for id, m := range want {
		if got[id] != m {
			t.Fatalf("search: %v, want %v", got, want)
		}
	}
	if l.Counts.All != 5 || l.Counts.Trash != 1 || l.Counts.Archived != 1 || l.Counts.Collections != 1 || l.Counts.Projects != 1 || l.Counts.Inbox != 1 {
		t.Fatalf("counts %+v", l.Counts)
	}
	// Within one view the search narrows that view.
	if l := library(t, a, ops.LibraryIn{Query: "beach", View: ops.ViewArchived}); len(l.Items) != 1 || l.Items[0].ID != archived.ID {
		t.Fatalf("archived search %+v", itemIDs(l.Items))
	}
	// The palette's search says where a clip in no project is filed.
	s := do[ops.SearchOut](t, a, "search", ops.SearchIn{Query: "beach"})
	for _, v := range s.Videos {
		if v.AssetID == archived.ID && v.Filed != "the Archive" {
			t.Fatalf("filed %q", v.Filed)
		}
		if v.AssetID == trashed.ID {
			t.Fatal("the palette found a clip in the Trash")
		}
	}
}
