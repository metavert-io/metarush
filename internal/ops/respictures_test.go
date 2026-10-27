package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// fixtureRepo builds acme/widget with its character art (scripts/make-fixture-repo.sh)
// and points GitHub addresses at it.
func fixtureRepo(t *testing.T, steps ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	base := t.TempDir()
	for _, step := range append([]string{""}, steps...) {
		args := []string{filepath.Join("..", "..", "scripts", "make-fixture-repo.sh"), base}
		if step != "" {
			args = append(args, step)
		}
		if out, err := exec.Command("bash", args...).CombinedOutput(); err != nil {
			t.Fatalf("fixture repo: %v %s", err, out)
		}
	}
	t.Setenv("METARUSH_GIT_BASE", base)
	return base
}

// A picture kept from a repository's folder is credited with the file at its
// commit in the delivery notes (D216).
func TestRepositoryPictureCredit(t *testing.T) {
	fixtureRepo(t, "art")
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Teaser"})
	f := call[ResourceFetchOut](t, a, "resources.fetch", ResourceFetchIn{URL: "https://github.com/acme/widget", ProjectID: p.Project.ID, Pictures: []string{"art/characters"}})
	var j models.Job
	if err := a.DB.GetByID(ctx, store.CJobs, a.WorkspaceID(), f.JobID, &j); err != nil {
		t.Fatal(err)
	}
	if _, err := resources.RunFetch(ctx, a, &j, nil); err != nil {
		t.Fatal(err)
	}
	pics := call[RepoPicturesOut](t, a, "resources.pictures", RepoPicturesIn{ResourceRefIn: ResourceRefIn{Ref: "widget", ProjectID: p.Project.ID}})
	alice := pics.Pictures[0]
	now := time.Now().UTC()
	clip := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "talk.mov", State: models.AssetAvailable,
		Media: models.MediaInfo{Duration: 10, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, clip)
	plan := fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"talk","assetId":%q,"in":0,"out":6}],
	 "overlays":[{"id":"end","template":"endcard.cta","at":2,"duration":3,"params":{"headline":"Meet Alice","cta":"Read free","image":"widget-alice"}}],
	 "resources":{"widget-alice":{"id":%q,"sha256":%q}}}}`, p.Project.ID, clip.ID, alice.ResourceID, alice.SHA256)
	chk := call[EditCheckOut](t, a, "edit.check", EditCheckIn{Plan: json.RawMessage(plan)})
	if !chk.Valid {
		t.Fatalf("plan %+v", chk.Errors)
	}
	applied, err := CallTyped[EditApplyOut](ctx, a, "edit.apply", EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken}, Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	var rev models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), applied.Revision.ID, &rev)
	var proj models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), p.Project.ID, &proj)
	assets, _ := deliveryAssets(ctx, a, &rev.Spec)
	extras, err := LoadRenderExtras(ctx, a, &rev.Spec)
	if err != nil {
		t.Fatal(err)
	}
	sc := buildSidecar(a, &deliveryItem{rev: rev, proj: proj, render: models.Render{ID: "rnd_x", Output: &models.RenderOutput{}}, assets: assets, extras: extras}, "cut.mp4")
	want := "Includes “alice” from github.com/acme/widget@" + pics.Commit[:8] + ":art/characters/alice.png (MIT): https://github.com/acme/widget"
	found := false
	for _, c := range sc.Credits {
		found = found || c.Credit == want
	}
	if !found {
		t.Fatalf("credits %+v, want %q", sc.Credits, want)
	}
}

// A repository keeps the pictures in its folders (D216): resources.fetch with
// pictures, resources.show and resources.pictures say what it keeps,
// resources.pictures.set changes the folders (a wider one takes the place of
// the folders it holds; dropping one keeps its pictures), and promoting the
// repository takes its pictures along.
func TestRepositoryPicturesOps(t *testing.T) {
	fixtureRepo(t, "art")
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Teaser"})
	run := func(jobID string) {
		t.Helper()
		var j models.Job
		if err := a.DB.GetByID(ctx, store.CJobs, a.WorkspaceID(), jobID, &j); err != nil {
			t.Fatal(err)
		}
		if _, err := resources.RunFetch(ctx, a, &j, nil); err != nil {
			t.Fatal(err)
		}
	}
	f := call[ResourceFetchOut](t, a, "resources.fetch", ResourceFetchIn{URL: "https://github.com/acme/widget", ProjectID: p.Project.ID, Pictures: []string{"art/characters/more/"}})
	if f.Kind != resources.FetchRepo || f.JobID == "" {
		t.Fatalf("fetch %+v", f)
	}
	run(f.JobID)
	show := call[ResourceShowOut](t, a, "resources.show", ResourceRefIn{Ref: "widget", ProjectID: p.Project.ID})
	if show.Repo == nil || show.Repo.Pictures == nil || show.Repo.Pictures.Kept != 1 || strings.Join(show.Repo.Pictures.Paths, ",") != "art/characters/more" ||
		!strings.Contains(show.Repo.Pictures.Summary, "1 picture kept from “art/characters/more”") {
		t.Fatalf("show %+v", show.Repo)
	}
	pics := call[RepoPicturesOut](t, a, "resources.pictures", RepoPicturesIn{ResourceRefIn: ResourceRefIn{Ref: "widget", ProjectID: p.Project.ID}})
	if pics.Kept != 1 || len(pics.Pictures) != 1 || pics.Pictures[0].Name != "widget-dot" || pics.Pictures[0].Path != "art/characters/more/dot.gif" ||
		!strings.Contains(pics.Pictures[0].ThumbURL, "?v=") || !pics.UpToDate || pics.Repo.Kind != models.ResourceRepo {
		t.Fatalf("pictures %+v", pics)
	}
	// A wider folder takes the place of the one it holds; its pictures come at the commit the repository shows.
	set := call[RepoPicturesOut](t, a, "resources.pictures.set", RepoPicturesSetIn{ResourceRefIn: ResourceRefIn{Ref: "widget", ProjectID: p.Project.ID}, Keep: []string{"art/characters"}})
	if strings.Join(set.Paths, ",") != "art/characters" || set.JobID == "" || set.UpToDate || !strings.Contains(set.Message, "Keeping the pictures in “art/characters” of acme/widget") {
		t.Fatalf("set %+v", set)
	}
	run(set.JobID)
	pics = call[RepoPicturesOut](t, a, "resources.pictures", RepoPicturesIn{ResourceRefIn: ResourceRefIn{Ref: "widget", ProjectID: p.Project.ID}})
	if pics.Kept != 5 || !pics.UpToDate || pics.Skipped != 5 || len(pics.LeftOut) != 5 || len(pics.SkippedWhy) == 0 {
		t.Fatalf("after %+v", pics)
	}
	var repo models.Resource
	_ = a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), pics.Repo.ID, &repo)
	if len(repo.Versions) != 1 {
		t.Fatalf("keeping pictures makes no new version of its files: %d", len(repo.Versions))
	}
	under := call[RepoPicturesOut](t, a, "resources.pictures", RepoPicturesIn{ResourceRefIn: ResourceRefIn{Ref: "widget", ProjectID: p.Project.ID}, Path: "art/characters/more"})
	if under.Kept != 1 {
		t.Fatalf("under a folder %+v", under)
	}
	// Keeping what it keeps changes nothing.
	same := call[RepoPicturesOut](t, a, "resources.pictures.set", RepoPicturesSetIn{ResourceRefIn: ResourceRefIn{Ref: "widget", ProjectID: p.Project.ID}, Keep: []string{"art/characters/more"}})
	if same.JobID != "" || same.Message != "Nothing changed." || len(same.Notes) != 1 {
		t.Fatalf("same %+v", same)
	}
	// Dropping a folder keeps its pictures in the library.
	drop := call[RepoPicturesOut](t, a, "resources.pictures.set", RepoPicturesSetIn{ResourceRefIn: ResourceRefIn{Ref: "widget", ProjectID: p.Project.ID}, Drop: []string{"art/characters"}})
	if len(drop.Paths) != 0 || drop.JobID != "" || drop.Kept != 0 || !strings.Contains(drop.Message, "No longer keeps “art/characters” in step") ||
		!strings.Contains(drop.Summary, "the 5 pictures it kept stay in the library") {
		t.Fatalf("drop %+v", drop)
	}
	if n, _ := a.DB.C(store.CResources).CountDocuments(ctx, map[string]any{"workspaceId": a.WorkspaceID(), "repoFile.repoId": repo.ID, "removed": map[string]any{"$ne": true}}); n != 5 {
		t.Fatalf("%d pictures left", n)
	}
	// Promoting the repository takes its pictures to the workspace library too.
	call[ResourceOut](t, a, "resources.promote", ResourceRefIn{Ref: "widget", ProjectID: p.Project.ID})
	if n, _ := a.DB.C(store.CResources).CountDocuments(ctx, map[string]any{"workspaceId": a.WorkspaceID(), "repoFile.repoId": repo.ID, "scope": models.ScopeWorkspace}); n != 5 {
		t.Fatalf("%d pictures promoted", n)
	}
	// What is refused, plainly.
	for _, c := range []struct {
		name string
		in   any
	}{
		{"resources.pictures.set", RepoPicturesSetIn{ResourceRefIn: ResourceRefIn{Ref: "widget"}}},
		{"resources.pictures.set", RepoPicturesSetIn{ResourceRefIn: ResourceRefIn{Ref: "widget"}, Keep: []string{"../outside"}}},
		{"resources.pictures.set", RepoPicturesSetIn{ResourceRefIn: ResourceRefIn{Ref: "widget"}, Drop: []string{"art/elsewhere"}}},
		{"resources.pictures", RepoPicturesIn{ResourceRefIn: ResourceRefIn{Ref: "widget-dot"}}},
		{"resources.fetch", ResourceFetchIn{URL: "https://example.com", Pictures: []string{"art"}}},
	} {
		if _, err := CallTyped[RepoPicturesOut](ctx, a, c.name, c.in, Caller{}); err == nil || apperr.From(err).Code != apperr.InvalidInput {
			t.Errorf("%s %+v: %v", c.name, c.in, err)
		}
	}
}
