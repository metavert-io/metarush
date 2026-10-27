package intake_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// The resources feature end to end against the test database: resources and
// their versions, linked folders, project links (whole and kept after
// unlinking), nested cuts (pinned, updated on request, cycles refused), the
// brand kit, and moving and deleting footage (people only, refused while
// anything uses it, shared bytes kept).

var person = ops.Caller{Human: true}

func call[T any](t *testing.T, a *app.App, op string, in any) *T {
	t.Helper()
	out, err := ops.CallTyped[T](context.Background(), a, op, in, person)
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return out
}

func callErr(t *testing.T, a *app.App, op string, in any, c ops.Caller) error {
	t.Helper()
	b, _ := json.Marshal(in)
	_, err := ops.Call(context.Background(), a, op, b, c)
	return err
}

// logoPNG writes a small logo: a coloured block on a transparent background.
func logoPNG(t *testing.T, dir, name string, c color.RGBA) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 120, 48))
	for y := 8; y < 40; y++ {
		for x := 10; x < 110; x++ {
			img.Set(x, y, c)
		}
	}
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	return p
}

// settle dates a file in the past, as a file copied in a while ago (a linked
// folder only takes files that stopped changing).
func settle(t *testing.T, p string, ago time.Duration) {
	t.Helper()
	at := time.Now().Add(-ago)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

func newProject(t *testing.T, a *app.App, name string) models.Project {
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

// footage stores a small "video" (its bytes) and records it as available
// footage of the given projects.
func footage(t *testing.T, a *app.App, name string, projectIDs ...string) models.Asset {
	t.Helper()
	ctx := context.Background()
	b := make([]byte, 4096)
	_, _ = rand.Read(b)
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(ctx, key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if projectIDs == nil {
		projectIDs = []string{}
	}
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: projectIDs, Kind: "video", Name: name, SHA256: sha, Size: int64(len(b)), StorageKey: key,
		State: models.AssetAvailable, Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 10, Width: 1920, Height: 1080, FPS: 30}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	return as
}

// saveCut checks then saves a whole cut (a project's first, or a replacement).
func saveCut(t *testing.T, a *app.App, projectID, base string, spec editspec.Spec) string {
	t.Helper()
	plan, _ := json.Marshal(editspec.Plan{ProjectID: projectID, BaseRevisionID: base, Instruction: "test cut", Spec: &spec})
	chk := call[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: plan})
	if !chk.Valid {
		t.Fatalf("cut not valid: %+v", chk.Errors)
	}
	out := call[ops.EditApplyOut](t, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken})
	return out.Revision.ID
}

func vertical() editspec.Output {
	return editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30, Platform: "shorts"}
}

// cut is a vertical cut of the given clips.
func cut(clips ...editspec.Clip) editspec.Spec {
	return editspec.Spec{SchemaVersion: 1, Output: vertical(), Clips: clips}
}

// checked runs a cut verb as a dry run, then saves it with the token.
func checked[In any](t *testing.T, a *app.App, op string, set func(c *ops.CutEditCommon) In) *ops.CutEditOut {
	t.Helper()
	var common ops.CutEditCommon
	common.DryRun = true
	dry := call[ops.CutEditOut](t, a, op, set(&common))
	if dry.Check == nil || !dry.Check.Valid {
		t.Fatalf("%s dry run: %+v", op, dry.Problems)
	}
	common.DryRun, common.ConfirmToken = false, dry.Check.ConfirmToken
	out := call[ops.CutEditOut](t, a, op, set(&common))
	if out.Applied == nil {
		t.Fatalf("%s: not saved: %+v", op, out)
	}
	return out
}

func TestResourceVersionsAndSuggestedTags(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	brand := newProject(t, a, "Brand")
	dir := t.TempDir()
	p := logoPNG(t, dir, "acme-logo.png", color.RGBA{220, 30, 120, 255})

	r1, err := resources.AddFile(ctx, a, p, resources.AddOptions{ProjectID: brand.ID, FileName: "acme-logo.png", Via: "cli"})
	if err != nil || r1.Status != resources.StatusAdded {
		t.Fatalf("add: %+v %v", r1, err)
	}
	res := r1.Resource
	if res.Kind != models.ResourceImage || res.Name != "acme-logo" || !slices.Contains(res.AllTags(), "logo") {
		t.Fatalf("resource %+v (tags %v)", res, res.AllTags())
	}
	v := res.Latest()
	if v.Width != 120 || v.Height != 48 || !v.HasAlpha || len(v.Colors) == 0 || !strings.EqualFold(v.Colors[0], "#DC1E78") {
		t.Fatalf("picture facts %+v", v)
	}
	// The same bytes again are the same resource; changed bytes under the same name are a new version.
	r2, err := resources.AddFile(ctx, a, p, resources.AddOptions{ProjectID: brand.ID, FileName: "acme-logo.png", Via: "cli"})
	if err != nil || r2.Status != resources.StatusSame || r2.Resource.ID != res.ID {
		t.Fatalf("same: %+v %v", r2, err)
	}
	logoPNG(t, dir, "acme-logo.png", color.RGBA{20, 90, 220, 255})
	r3, err := resources.AddFile(ctx, a, p, resources.AddOptions{ProjectID: brand.ID, FileName: "acme-logo.png", Via: "cli"})
	if err != nil || r3.Status != resources.StatusVersion || r3.Resource.ID != res.ID || len(r3.Resource.Versions) != 2 || r3.Resource.Current == v.SHA256 {
		t.Fatalf("version: %+v %v", r3, err)
	}
	list := call[ops.ResourceListOut](t, a, "resources.list", ops.ResourceListIn{ProjectID: "Brand"})
	if len(list.Resources) != 1 || list.Here != 1 || list.Resources[0].Versions != 2 {
		t.Fatalf("list %+v", list)
	}
	// Search finds it by its suggested tag.
	found := call[ops.SearchOut](t, a, "search", ops.SearchIn{Query: "logo", ProjectID: brand.ID})
	if len(found.Resources) != 1 || found.Resources[0].ResourceID != res.ID {
		t.Fatalf("search %+v", found.Resources)
	}
}

func TestLinkedFolderStaysInStep(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := newProject(t, a, "Docs")
	dir := t.TempDir()
	settle(t, logoPNG(t, dir, "badge.png", color.RGBA{10, 160, 90, 255}), time.Minute)
	out, err := intake.LinkFolder(ctx, a, &intake.LinkedFolderLinkIn{Path: dir, ProjectID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if out.Scan == nil || out.Scan.Added != 1 {
		t.Fatalf("first look: %+v", out.Scan)
	}
	rescan := func() {
		t.Helper()
		call[intake.LinkedFolderOut](t, a, "resources.folder", intake.LinkedFolderSetIn{Folder: out.Folder.ID, Rescan: true})
	}
	find := func() models.Resource {
		t.Helper()
		var rs []models.Resource
		if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), map[string]any{"projectId": p.ID}, &rs); err != nil || len(rs) != 1 {
			t.Fatalf("resources %v %v", rs, err)
		}
		return rs[0]
	}
	// A file still being copied in waits; once it settles, a changed file is
	// a new version of the same resource.
	logoPNG(t, dir, "badge.png", color.RGBA{200, 160, 20, 255})
	rescan()
	if r := find(); len(r.Versions) != 1 {
		t.Fatalf("a file that is still changing was taken: %d versions", len(r.Versions))
	}
	settle(t, filepath.Join(dir, "badge.png"), 30*time.Second)
	rescan()
	if r := find(); len(r.Versions) != 2 || r.Folder == nil || r.Folder.RelPath != "badge.png" {
		t.Fatalf("after a change: %+v", r)
	}
	// A deleted file leaves the resource, marked missing from its folder.
	if err := os.Remove(filepath.Join(dir, "badge.png")); err != nil {
		t.Fatal(err)
	}
	rescan()
	if r := find(); !r.Missing || r.Removed {
		t.Fatalf("after deleting the file: missing=%v removed=%v", r.Missing, r.Removed)
	}
	rows := call[intake.LinkedFolderListOut](t, a, "resources.folders", intake.LinkedFolderListIn{ProjectID: p.ID})
	if len(rows.Folders) != 1 || rows.Folders[0].HerePath == "" {
		t.Fatalf("folders %+v", rows.Folders)
	}
}

func TestProjectLinksNestingAndKeptAfterUnlink(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	brand := newProject(t, a, "Brand")
	promo := newProject(t, a, "Promo")
	intro := footage(t, a, "intro.mov", brand.ID)
	b1 := saveCut(t, a, brand.ID, "", cut(editspec.Clip{ID: "intro", AssetID: intro.ID, In: 0, Out: 4}))
	if _, err := resources.AddFile(ctx, a, logoPNG(t, t.TempDir(), "brand-logo.png", color.RGBA{220, 30, 120, 255}), resources.AddOptions{ProjectID: brand.ID, FileName: "brand-logo.png", Via: "cli"}); err != nil {
		t.Fatal(err)
	}

	// Promo uses everything from Brand: its footage and resources show up "from Brand", nothing copied.
	link := call[ops.ProjectLinkOut](t, a, "project.link", ops.ProjectLinkIn{ProjectID: "Promo", From: "Brand"})
	if link.Link == nil || !link.Link.Whole || link.Link.Counts.Footage != 1 || link.Link.Counts.Resources != 1 || !link.Link.Counts.Cut {
		t.Fatalf("link %+v", link)
	}
	show := call[ops.ProjectShowOut](t, a, "project.show", ops.ProjectShowIn{ID: promo.ID})
	if len(show.Assets) != 1 || show.Assets[0].ID != intro.ID || show.Assets[0].From != "Brand" || len(show.Links) != 1 {
		t.Fatalf("promo sees %+v / links %+v", show.Assets, show.Links)
	}
	list := call[ops.ResourceListOut](t, a, "resources.list", ops.ResourceListIn{ProjectID: promo.ID})
	if len(list.Resources) != 1 || list.Resources[0].From != "Brand" || list.Linked != 1 {
		t.Fatalf("promo resources %+v", list)
	}

	// Promo's cut plays Brand's cut (pinned to version 1).
	nest := checked(t, a, "cut.nest", func(c *ops.CutEditCommon) ops.CutNestIn {
		c.ProjectID = promo.ID
		return ops.CutNestIn{CutEditCommon: *c, From: "Brand"}
	})
	rev := nest.Applied.Revision
	var saved models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rev.ID, &saved); err != nil {
		t.Fatal(err)
	}
	key := editspec.NestedKey(brand.ID, b1)
	if len(saved.Spec.Clips) != 1 || saved.Spec.Clips[0].AssetID != key {
		t.Fatalf("nested clip %+v", saved.Spec.Clips)
	}
	plan := call[editspec.RenderPlan](t, a, "revision.plan", ops.RevisionPlanIn{ID: rev.ID})
	np, ok := plan.Nested[key]
	if !ok || np.Plan == nil || len(np.Plan.Clips) != 1 || plan.DurationInFrames != 120 || np.Number != 1 {
		t.Fatalf("plan nested %+v (duration %d)", plan.Nested, plan.DurationInFrames)
	}
	if plan.Sources[intro.ID] == "" {
		t.Fatalf("the nested cut's footage has no preview source: %v", plan.Sources)
	}

	// Brand's cut changes: Promo keeps version 1 until it updates.
	b2 := saveCut(t, a, brand.ID, b1, cut(editspec.Clip{ID: "intro", AssetID: intro.ID, In: 1, Out: 6}))
	plan = call[editspec.RenderPlan](t, a, "revision.plan", ops.RevisionPlanIn{ID: rev.ID})
	if plan.Nested[key].NewerRevisionID != b2 || plan.Nested[key].NewerNumber != 2 {
		t.Fatalf("newer version not noticed: %+v", plan.Nested[key])
	}
	up := checked(t, a, "cut.update", func(c *ops.CutEditCommon) ops.CutUpdateIn {
		c.ProjectID = promo.ID
		return ops.CutUpdateIn{CutEditCommon: *c}
	})
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), up.Applied.Revision.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Spec.Clips[0].AssetID != editspec.NestedKey(brand.ID, b2) {
		t.Fatalf("not updated: %s", saved.Spec.Clips[0].AssetID)
	}

	// A cut can't play itself: Brand nesting Promo (which plays Brand) is refused.
	call[ops.ProjectLinkOut](t, a, "project.link", ops.ProjectLinkIn{ProjectID: "Brand", From: "Promo"})
	cyc := call[ops.CutEditOut](t, a, "cut.nest", ops.CutNestIn{CutEditCommon: ops.CutEditCommon{ProjectID: brand.ID, DryRun: true}, From: "Promo"})
	if cyc.Check == nil || cyc.Check.Valid || !slices.ContainsFunc(cyc.Check.Errors, func(is editspec.Issue) bool { return is.Code == "nested_cycle" }) {
		t.Fatalf("cycle not refused: %+v", cyc.Check)
	}

	// Unlinking keeps what Promo's cut uses (Brand's cut), and says so.
	un := call[ops.ProjectLinkOut](t, a, "project.unlink", ops.ProjectUnlinkIn{ProjectID: "Promo", From: "Brand"})
	if !slices.ContainsFunc(un.Kept, func(k ops.LinkItemRow) bool { return k.Kind == models.LinkCuts }) {
		t.Fatalf("unlink kept %+v (%s)", un.Kept, un.Message)
	}
	if _, err := ops.CallTyped[editspec.RenderPlan](ctx, a, "revision.plan", ops.RevisionPlanIn{ID: up.Applied.Revision.ID}, person); err != nil {
		t.Fatalf("the kept nested cut no longer plays: %v", err)
	}
	where := call[ops.CutWhereOut](t, a, "cut.where", ops.CutWhereIn{ProjectID: brand.ID})
	if len(where.Uses) == 0 || !strings.Contains(where.Uses[0].Text, "Promo") {
		t.Fatalf("cut.where %+v", where)
	}
}

func TestAssetDeleteIsForPeopleAndRefusedWhileUsed(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	test := newProject(t, a, "Test project")
	fernbrook := newProject(t, a, "fernbrook")
	clip := footage(t, a, "interview.mov", test.ID)
	saveCut(t, a, test.ID, "", cut(editspec.Clip{ID: "a", AssetID: clip.ID, In: 0, Out: 3}))
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: clip.ID, Text: "hello", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}

	// Not by Claude or a script: people only.
	if err := callErr(t, a, "asset.delete", ops.AssetDeleteIn{AssetID: clip.ID, Forever: true}, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("non-human delete: %v", err)
	}
	// Refused while a cut (any version) uses it, saying where.
	dry := call[ops.AssetDeleteOut](t, a, "asset.delete", ops.AssetDeleteIn{AssetID: clip.ID, ProjectID: test.ID, Forever: true, DryRun: true})
	if dry.Allowed || len(dry.Blockers) != 1 || !strings.Contains(dry.Blockers[0], "Used in Test project's cut (version 1") || len(dry.Uses) != 1 {
		t.Fatalf("dry run %+v", dry)
	}
	if err := callErr(t, a, "asset.delete", ops.AssetDeleteIn{AssetID: clip.ID, ProjectID: test.ID, Forever: true}, person); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("delete while used: %v", err)
	}
	// Refused while another project has it.
	other := footage(t, a, "broll.mov", test.ID, fernbrook.ID)
	dry = call[ops.AssetDeleteOut](t, a, "asset.delete", ops.AssetDeleteIn{AssetID: other.ID, ProjectID: test.ID, DryRun: true})
	if dry.Allowed || !strings.Contains(dry.Blockers[0], "Also in fernbrook") {
		t.Fatalf("other project: %+v", dry)
	}

	// Allowed: derived data goes; stored bytes something else shares (here a
	// render someone imported as footage) stay.
	lone := footage(t, a, "outtake.mov", test.ID)
	if err := a.DB.Insert(ctx, store.CRenders, models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: fernbrook.ID, State: models.RenderAvailable,
		Output: &models.RenderOutput{StorageKey: lone.StorageKey, SHA256: lone.SHA256, Size: lone.Size}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: lone.ID, Text: "bye", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// A face the on-screen index followed in it, named by a person.
	if err := a.DB.Insert(ctx, store.CFaces, models.FaceTrack{ID: ids.New(ids.Face), WorkspaceID: a.WorkspaceID(), AssetID: lone.ID, Track: 1, To: 2, Samples: 3,
		Box: []float64{0.4, 0.2, 0.2, 0.3}, Name: "Ada", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	out := call[ops.AssetDeleteOut](t, a, "asset.delete", ops.AssetDeleteIn{AssetID: lone.ID, ProjectID: test.ID, Forever: true})
	if !out.Deleted || out.Kept != 1 || out.Removed["transcript segments"] != 1 || out.Removed["faces"] != 1 {
		t.Fatalf("delete %+v", out)
	}
	var gone models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), lone.ID, &gone); err == nil {
		t.Fatal("the asset is still there")
	}
	var segs []models.TranscriptSegment
	if err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), map[string]any{"assetId": lone.ID}, &segs); err != nil || len(segs) != 0 {
		t.Fatalf("its transcript is still there: %v %v", segs, err)
	}
	if _, err := a.Media.Stat(ctx, lone.StorageKey); err != nil {
		t.Fatal("bytes a render uses were deleted")
	}
	// Footage nothing else shares takes its bytes with it.
	solo := footage(t, a, "blooper.mov")
	out = call[ops.AssetDeleteOut](t, a, "asset.delete", ops.AssetDeleteIn{AssetID: solo.ID, Forever: true})
	if !out.Deleted || out.Kept != 0 || out.Removed["stored files"] != 1 {
		t.Fatalf("second delete %+v", out)
	}
	if _, err := a.Media.Stat(ctx, solo.StorageKey); err == nil {
		t.Fatal("unshared bytes were kept")
	}
	// Without --forever it doesn't happen.
	keep := footage(t, a, "keep.mov")
	if err := callErr(t, a, "asset.delete", ops.AssetDeleteIn{AssetID: keep.ID}, person); apperr.From(err).Code != apperr.ConfirmRequired {
		t.Fatalf("delete without forever: %v", err)
	}
}

func TestAssetMoveAndWhere(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	test := newProject(t, a, "Test project")
	fernbrook := newProject(t, a, "fernbrook")
	clip := footage(t, a, "drop.mov", test.ID)
	used := footage(t, a, "used.mov", test.ID)
	saveCut(t, a, test.ID, "", cut(editspec.Clip{ID: "a", AssetID: used.ID, In: 0, Out: 3}))

	// Names work as well as IDs (a misplaced video moved with "fernbrook").
	mv := call[ops.AssetMoveOut](t, a, "asset.move", ops.AssetMoveIn{AssetID: clip.ID, From: "Test project", To: "fernbrook"})
	if !slices.Equal(mv.Asset.ProjectIDs, []string{fernbrook.ID}) {
		t.Fatalf("moved %+v", mv.Asset.ProjectIDs)
	}
	// Footage the current cut uses can't leave.
	if err := callErr(t, a, "asset.move", ops.AssetMoveIn{AssetID: used.ID, From: test.ID, To: fernbrook.ID}, person); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("move while used: %v", err)
	}
	w := call[ops.AssetWhereOut](t, a, "asset.where", ops.AssetRefIn{AssetID: used.ID})
	if len(w.Uses) != 2 || w.Uses[0].Kind != ops.UseProject || w.Uses[1].Kind != ops.UseCut || !strings.HasPrefix(w.Uses[1].Link, "/p/"+test.ID+"/r/") {
		t.Fatalf("where %+v", w.Uses)
	}
	_ = ctx
}

func TestBrandKitColoursAndNewCuts(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: a.WorkspaceID(), WorkspaceID: a.WorkspaceID(), Name: "T", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	p := newProject(t, a, "Launch")
	clip := footage(t, a, "launch.mov", p.ID)
	r, err := resources.AddFile(ctx, a, logoPNG(t, t.TempDir(), "logo.png", color.RGBA{230, 70, 30, 255}), resources.AddOptions{FileName: "logo.png", Via: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	logo := r.Resource.ID
	kit := call[ops.BrandShowOut](t, a, "brand.set", ops.BrandSetIn{Logo: &logo})
	if kit.Logo == nil || kit.Logo.ID != logo || !strings.EqualFold(kit.Brand.Accent, "#E6461E") {
		t.Fatalf("kit %+v (brand %+v)", kit.Kit, kit.Brand)
	}
	// A person's colour stays when the logo changes.
	green := "#2E7D32"
	call[ops.BrandShowOut](t, a, "brand.set", ops.BrandSetIn{Accent: &green})
	r2, _ := resources.AddFile(ctx, a, logoPNG(t, t.TempDir(), "logo2.png", color.RGBA{20, 40, 200, 255}), resources.AddOptions{FileName: "logo2.png", Via: "cli"})
	logo2 := r2.Resource.ID
	kit = call[ops.BrandShowOut](t, a, "brand.set", ops.BrandSetIn{Logo: &logo2})
	if !strings.EqualFold(kit.Brand.Accent, green) {
		t.Fatalf("edited colour lost: %+v", kit.Brand)
	}
	// A new cut starts with the kit: its tokens, and the logo pinned as "logo".
	rev := saveCut(t, a, p.ID, "", cut(editspec.Clip{ID: "a", AssetID: clip.ID, In: 0, Out: 3}))
	var saved models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rev, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Spec.Brand == nil || !strings.EqualFold(saved.Spec.Brand.Accent, green) || saved.Spec.Resources["logo"].ID != logo2 {
		t.Fatalf("new cut brand %+v resources %+v", saved.Spec.Brand, saved.Spec.Resources)
	}
}

// A cut that ends on a locked clip: another project's cut goes just before
// that ending when nobody said where (the ending stays last).
func TestNestGoesBeforeALockedEnding(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	film := newProject(t, a, "Film")
	brand := newProject(t, a, "Brand")
	interview := footage(t, a, "interview.mov", film.ID)
	intro := footage(t, a, "intro.mov", brand.ID)
	saveCut(t, a, film.ID, "", cut(
		editspec.Clip{ID: "hook", AssetID: interview.ID, In: 1, Out: 4},
		editspec.Clip{ID: "payoff", AssetID: interview.ID, In: 5, Out: 8, Locked: true},
	))
	saveCut(t, a, brand.ID, "", cut(editspec.Clip{ID: "intro", AssetID: intro.ID, In: 0, Out: 3}))
	call[ops.ProjectLinkOut](t, a, "project.link", ops.ProjectLinkIn{ProjectID: film.ID, From: brand.ID})
	nest := checked(t, a, "cut.nest", func(c *ops.CutEditCommon) ops.CutNestIn {
		c.ProjectID = film.ID
		return ops.CutNestIn{CutEditCommon: *c, From: "Brand"}
	})
	if !strings.Contains(nest.Summary, "before the locked ending") {
		t.Fatalf("summary %q", nest.Summary)
	}
	var saved models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), nest.Applied.Revision.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if n := len(saved.Spec.Clips); n != 3 || saved.Spec.Clips[n-1].ID != "payoff" || !strings.HasPrefix(saved.Spec.Clips[1].AssetID, "cut:") {
		t.Fatalf("clips %+v", saved.Spec.Clips)
	}
	// Said outright, "end" still means after the ending, which its lock refuses.
	end := call[ops.CutEditOut](t, a, "cut.nest", ops.CutNestIn{CutEditCommon: ops.CutEditCommon{ProjectID: film.ID, DryRun: true}, From: "Brand", Position: "end"})
	if end.Check == nil || end.Check.Valid {
		t.Fatalf("after a locked ending: %+v", end.Check)
	}
}

// What a video shows through a nested cut is shown by the video: the delivery
// sidecar and QA list the nested cut's resources (and the cut itself) too.
func TestNestedCutsResourcesAreShown(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	brand := newProject(t, a, "Brand")
	promo := newProject(t, a, "Promo")
	intro := footage(t, a, "intro.mov", brand.ID)
	added, err := resources.AddFile(ctx, a, logoPNG(t, t.TempDir(), "brand-logo.png", color.RGBA{220, 30, 120, 255}), resources.AddOptions{ProjectID: brand.ID, FileName: "brand-logo.png", Via: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	logo := added.Resource
	spec := cut(editspec.Clip{ID: "intro", AssetID: intro.ID, In: 0, Out: 4})
	spec.Resources = map[string]editspec.ResourcePin{"logo": {ID: logo.ID, SHA256: logo.Current}}
	saveCut(t, a, brand.ID, "", spec)
	call[ops.ProjectLinkOut](t, a, "project.link", ops.ProjectLinkIn{ProjectID: promo.ID, From: brand.ID})
	nest := checked(t, a, "cut.nest", func(c *ops.CutEditCommon) ops.CutNestIn {
		c.ProjectID = promo.ID
		return ops.CutNestIn{CutEditCommon: *c, From: "Brand"}
	})
	var saved models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), nest.Applied.Revision.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Spec.Resources) != 0 {
		t.Fatalf("Promo's cut pins nothing itself: %+v", saved.Spec.Resources)
	}
	x, err := ops.LoadRenderExtras(ctx, a, &saved.Spec)
	if err != nil {
		t.Fatal(err)
	}
	shown := x.ShownResources(&saved.Spec)
	if len(shown) != 1 || shown[0].Name != "logo" || shown[0].Doc.ID != logo.ID || shown[0].Pin.SHA256 != logo.Current || shown[0].In != "Brand" {
		t.Fatalf("shown %+v", shown)
	}
	uses := x.NestedCuts(&saved.Spec)
	if len(uses) != 1 || uses[0].In != "" || uses[0].Source.ProjectName != "Brand" || uses[0].ClipID != saved.Spec.Clips[0].ID {
		t.Fatalf("nested cuts %+v", uses)
	}
}
