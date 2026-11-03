package ops_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// saveWhole checks and saves a whole cut (a new branch's first version, or a
// full rewrite of base) and returns the version saved.
func saveWhole(t *testing.T, a *app.App, projectID, branch, base string, spec editspec.Spec) models.Revision {
	t.Helper()
	pl := editspec.Plan{ProjectID: projectID, Branch: branch, BaseRevisionID: base, Instruction: "test cut", Spec: &spec}
	if base != "" {
		pl.ProjectID = ""
	}
	plan, _ := json.Marshal(pl)
	chk := do[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: plan})
	if !chk.Valid {
		t.Fatalf("cut not valid: %+v", chk.Errors)
	}
	id := do[ops.EditApplyOut](t, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}).Revision.ID
	var r models.Revision
	if err := a.DB.GetByID(context.Background(), store.CRevisions, a.WorkspaceID(), id, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// New cuts in a landscape-footage project start landscape (D386): the first
// sheet, and a whole cut that names no shape, take the project's shape (its
// footage here); a full rewrite that names none keeps its own cut's; with
// nothing known, MetaRush's vertical as before. A cut that names a shape
// keeps it.
func TestNewCutsInALandscapeFootageProjectStartLandscape(t *testing.T) {
	a := testapp.New(t)
	p := project(t, a, "Garden story")
	c := clip(t, a, "Garage interview", p.ID) // 1920×1080

	sheet := do[ops.CutShowOut](t, a, "cut.show", ops.CutShowIn{ProjectID: p.ID})
	if !sheet.Starter || !strings.Contains(sheet.Sheet, "landscape") {
		t.Fatalf("the first sheet of a landscape-footage project should start landscape:\n%s", sheet.Sheet)
	}
	unshaped := editspec.Spec{SchemaVersion: 1, Clips: []editspec.Clip{{ID: "ca", AssetID: c.ID, In: 0, Out: 3}}}
	first := saveWhole(t, a, p.ID, "", "", unshaped)
	if o := first.Spec.Output; o.Preset != editspec.PresetLandscape || o.Width != 1920 || o.Height != 1080 {
		t.Fatalf("a new cut naming no shape should start in the project's (landscape): %+v", o)
	}

	// A vertical cut of its own, and a full rewrite of it naming no shape: it stays vertical.
	vertical := unshaped
	vertical.Output = editspec.Output{Preset: editspec.PresetVertical, Platform: "shorts"}
	short := saveWhole(t, a, p.ID, "short", "", vertical)
	if short.Spec.Output.Preset != editspec.PresetVertical {
		t.Fatalf("a cut that names its shape keeps it: %+v", short.Spec.Output)
	}
	rewrite := saveWhole(t, a, p.ID, "short", short.ID, unshaped)
	if o := rewrite.Spec.Output; o.Preset != editspec.PresetVertical || o.Platform != "shorts" {
		t.Fatalf("a full rewrite naming no shape keeps its cut's: %+v", o)
	}

	// Nothing known: vertical, MetaRush's default, as before.
	empty := project(t, a, "Nothing yet")
	if sh := do[ops.CutShowOut](t, a, "cut.show", ops.CutShowIn{ProjectID: empty.ID}); !strings.Contains(sh.Sheet, "vertical") {
		t.Fatalf("a project nothing says the shape of starts vertical:\n%s", sh.Sheet)
	}

	// A brief that says YouTube starts widescreen for YouTube; one that says Shorts, vertical for Shorts.
	yt := project(t, a, "Channel trailer")
	if err := a.DB.Update(context.Background(), store.CProjects, a.WorkspaceID(), yt.ID, bson.M{"$set": bson.M{"brief.platforms": []string{"YouTube", "Shorts"}}}); err != nil {
		t.Fatal(err)
	}
	ytc := clip(t, a, "Trailer take", yt.ID)
	got := saveWhole(t, a, yt.ID, "", "", editspec.Spec{SchemaVersion: 1, Clips: []editspec.Clip{{ID: "ca", AssetID: ytc.ID, In: 0, Out: 3}}})
	if o := got.Spec.Output; o.Preset != editspec.PresetLandscape || o.Platform != "youtube" {
		t.Fatalf("a YouTube project's first cut: %+v", o)
	}
	ps := do[ops.ProjectShowOut](t, a, "project.show", ops.ProjectShowIn{ID: empty.ID})
	if ps.Shape.Known || ps.Shape.Shape != editspec.ShapeVertical {
		t.Fatalf("nothing known: %+v", ps.Shape)
	}
}

// Words for a place the video goes that name no shape take the project's
// (D386); words that name one keep theirs.
func TestAPlaceWithNoShapeTakesTheProjects(t *testing.T) {
	a := testapp.New(t)
	p := project(t, a, "Phone ads")
	if err := a.DB.Update(context.Background(), store.CProjects, a.WorkspaceID(), p.ID, bson.M{"$set": bson.M{"brief.platforms": []string{"tiktok"}}}); err != nil {
		t.Fatal(err)
	}
	b := models.Brief{Platforms: []string{"tiktok"}, Deliverables: []models.Deliverable{{Name: "Client review"}, {Name: "On the website"}}}
	up := do[ops.ProjectOut](t, a, "project.update", ops.ProjectUpdateIn{ID: p.ID, Brief: &b})
	if ds := up.Project.Brief.Deliverables; ds[0].Preset != editspec.PresetVertical || ds[1].Preset != editspec.PresetLandscape {
		t.Fatalf("deliverables: %+v", ds)
	}
	empty := project(t, a, "Nothing yet")
	b = models.Brief{Deliverables: []models.Deliverable{{Name: "Client review"}}}
	if ds := do[ops.ProjectOut](t, a, "project.update", ops.ProjectUpdateIn{ID: empty.ID, Brief: &b}).Project.Brief.Deliverables; ds[0].Preset != editspec.PresetLandscape {
		t.Fatalf("with nothing known, widescreen as before: %+v", ds)
	}
	if pr, _ := ops.DeliverableShape("Client review"); pr != "" {
		t.Fatalf("words that name no shape: %q", pr)
	}
}

// A render in another shape re-picks what depends on the shape for that
// render only (D388): it is marked so and has a key of its own, so a copy
// asked for before keeps what it drew; a render in the version's own shape
// is not marked.
func TestARenderInAnotherShapeIsMarkedForThatRenderOnly(t *testing.T) {
	a := testapp.New(t)
	p := project(t, a, "Garden story")
	c := clip(t, a, "Garage interview", p.ID)
	rev := saveCut(t, a, p.ID, "", c.ID) // vertical
	own := do[ops.RenderOut](t, a, "render.submit", ops.RenderSubmitIn{RevisionID: rev, Quality: "review", NoQA: true})
	if own.Render.ShapeRule != 0 || own.Job.IdempotencyKey != "render:"+rev+":vertical-1080p:review" || own.Job.Type != jobs.TypeRender {
		t.Fatalf("its own shape: %+v %s", own.Render, own.Job.IdempotencyKey)
	}
	same := do[ops.RenderOut](t, a, "render.submit", ops.RenderSubmitIn{RevisionID: rev, Preset: editspec.PresetVertical, Quality: "review", NoQA: true})
	if same.Created || same.Render.ID != own.Render.ID {
		t.Fatal("asking for its own preset is the same render")
	}
	wide := do[ops.RenderOut](t, a, "render.submit", ops.RenderSubmitIn{RevisionID: rev, Preset: editspec.PresetLandscape, Quality: "review", NoQA: true})
	// Its own job type: only a MetaRush that knows the rule takes it (D396).
	if wide.Render.ShapeRule != editspec.ReshapeRule || wide.Job.IdempotencyKey != "render:"+rev+":landscape-1080p:review:shape-1" || wide.Job.Type != jobs.TypeRenderShape {
		t.Fatalf("a widescreen copy: %+v %s %s", wide.Render, wide.Job.IdempotencyKey, wide.Job.Type)
	}
	if listed := do[ops.JobListOut](t, a, "job.list", ops.JobListIn{Type: jobs.TypeRender}); len(listed.Jobs) != 2 {
		t.Fatalf("both kinds of render are listed as renders: %d", len(listed.Jobs))
	}
	if again := do[ops.RenderOut](t, a, "render.submit", ops.RenderSubmitIn{RevisionID: rev, Preset: editspec.PresetLandscape, Quality: "review", NoQA: true}); again.Created || again.Render.ID != wide.Render.ID {
		t.Fatal("asking again gives the same widescreen copy")
	}
}

// The render of a version that is shown or sent is the one in its own shape
// when there is one (D391), else another, said plainly.
func TestTheVersionsOwnShapeIsPicked(t *testing.T) {
	rev := &models.Revision{Number: 3, Spec: editspec.Spec{Output: editspec.Output{Preset: editspec.PresetLandscape, Width: 1920, Height: 1080}}}
	newer := models.Render{ID: "rnd_v", Preset: editspec.PresetVertical}
	older := models.Render{ID: "rnd_l", Preset: editspec.PresetLandscape}
	if rd, own := ops.PickRender([]models.Render{newer, older}, rev); rd == nil || rd.ID != "rnd_l" || !own {
		t.Fatalf("picked %+v %v", rd, own)
	}
	if rd, own := ops.PickRender([]models.Render{newer}, rev); rd == nil || rd.ID != "rnd_v" || own {
		t.Fatalf("only another shape: %+v %v", rd, own)
	}
	if w := ops.OtherShapeWords(&newer, rev, "final render"); w != "Version 3 is widescreen, but it has no final render in that shape yet, so its vertical copy is used." {
		t.Fatalf("words: %q", w)
	}
	tall := &models.Revision{Number: 4, Spec: editspec.Spec{Output: editspec.Output{Width: 1080, Height: 1350}}}
	if w := ops.OtherShapeWords(&models.Render{Preset: editspec.PresetVertical}, tall, "review copy"); w != "Version 4 is 4:5, but it has no review copy in that shape yet, so its 9:16 copy is used." {
		t.Fatalf("two vertical shapes are told apart by their ratios: %q", w)
	}
	if ops.OtherShapeWords(&older, rev, "final render") != "" || !ops.InOwnShape(&models.Render{}, rev) {
		t.Fatal("its own shape says nothing")
	}
	// 1280×720 and 1920×1080 are the same shape.
	small := &models.Revision{Spec: editspec.Spec{Output: editspec.Output{Width: 1280, Height: 720}}}
	if !ops.InOwnShape(&models.Render{Preset: editspec.PresetLandscape}, small) {
		t.Fatal("a 1080p copy of a 720p widescreen version is in its own shape")
	}
}
