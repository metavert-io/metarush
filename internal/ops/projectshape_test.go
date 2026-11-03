package ops

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// putCut makes branch's latest version of a project's cut at w × h, showing
// the motion graphic name when one is given (written straight to the
// database: what the shape rules read, without a check of the graphic).
func putCut(t *testing.T, a *app.App, projectID, branch string, w, h int, name string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	s := editspec.Spec{SchemaVersion: 1, Title: branch + " cut", Output: editspec.Output{Width: w, Height: h, FPS: 30},
		Clips: []editspec.Clip{{ID: "a", AssetID: ids.New(ids.Asset), In: 0, Out: 4}}}
	if name != "" {
		s.Overlays = []editspec.Overlay{{ID: "g", At: 1, Duration: 2, Motion: &editspec.MotionRef{Name: name, Hash: strings.Repeat("d", 64)}}}
	}
	n, err := a.DB.C(store.CRevisions).CountDocuments(ctx, bson.M{"projectId": projectID})
	if err != nil {
		t.Fatal(err)
	}
	r := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: projectID, Branch: branch, Number: int(n) + 1, Spec: s, SpecHash: editspec.Hash(&s), CreatedAt: now}
	insert(t, a, store.CRevisions, r)
	if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": projectID}, bson.M{"$set": bson.M{"branches." + branch: models.BranchHead{Head: r.ID, UpdatedAt: now}}}); err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func putFootage(t *testing.T, a *app.App, projectID string, w, h, rotation int, secs float64) {
	t.Helper()
	now := time.Now().UTC()
	// The probe stores the picture as it plays; a record from before says
	// its coded size and the turn.
	coded := []models.Stream{{Type: "video", Width: w, Height: h}}
	if rotation == 90 || rotation == 270 {
		coded = []models.Stream{{Type: "video", Width: w, Height: h, Rotation: rotation}}
	}
	insert(t, a, store.CAssets, fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{projectID}, Kind: "video", Name: "clip.mov",
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: secs, HasVideo: true, Width: w, Height: h, Rotation: rotation, FPS: 30, Streams: coded},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}))
}

func projectByID(t *testing.T, a *app.App, id string) *models.Project {
	t.Helper()
	p, err := resolveProject(context.Background(), a, id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// A project's shape (D378): its main cut's latest version, else any cut's;
// the brief's deliverables; the shape most of its footage is in (rotation
// honoured); else vertical, not known.
func TestProjectShapeOrder(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Shape order"}).Project
	shape := func() ProjectShapeOut { return ProjectShape(ctx, a, projectByID(t, a, p.ID)) }
	says := func(ps ProjectShapeOut) string {
		return fmt.Sprintf("%s %s %dx%d known=%v", ps.From, ps.Shape, ps.Width, ps.Height, ps.Known)
	}

	if got := says(shape()); got != "default vertical 1080x1920 known=false" {
		t.Fatalf("nothing known: %s", got)
	}
	if ProjectShape(ctx, a, nil).Known {
		t.Fatal("no project is not known")
	}

	// Footage: two vertical clips (one a record of a phone video turned a
	// quarter, stored at its coded size) against one widescreen.
	putFootage(t, a, p.ID, 1920, 1080, 0, 30)
	putFootage(t, a, p.ID, 1080, 1920, 0, 5)
	putFootage(t, a, p.ID, 1920, 1080, 90, 5)
	if got := says(shape()); got != "footage vertical 1080x1920 known=true" {
		t.Fatalf("from the footage: %s", got)
	}

	// The brief's deliverables come before the footage.
	if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.ID}, bson.M{"$set": bson.M{"brief.deliverables": []models.Deliverable{
		{Name: "on the website", Preset: "landscape-1080p"}, {Name: "social shorts", Preset: "vertical-1080p"}}}}); err != nil {
		t.Fatal(err)
	}
	if ps := shape(); says(ps) != "deliverables landscape 1920x1080 known=true" || ps.Preset != editspec.PresetLandscape || !strings.Contains(ps.Why, "on the website") {
		t.Fatalf("from the brief: %s %+v", says(ps), ps)
	}

	// Any cut comes before the brief …
	putCut(t, a, p.ID, "teaser", 1080, 1350, "")
	if ps := shape(); says(ps) != "cut vertical 1080x1350 known=true" || ps.Branch != "teaser" || ps.Preset != "" || ps.Ratio != "4:5" {
		t.Fatalf("from another cut: %s %+v", says(ps), ps)
	}
	// … and the main cut before any other.
	main := putCut(t, a, p.ID, "main", 1080, 1080, "")
	if ps := shape(); says(ps) != "cut square 1080x1080 known=true" || ps.RevisionID != main {
		t.Fatalf("from the main cut: %s %+v", says(ps), ps)
	}

	// What the dashboard reads: the project, its cuts, its versions.
	show := call[ProjectShowOut](t, a, "project.show", ProjectShowIn{ID: p.ID})
	if show.Shape.From != ShapeFromCut || show.Shape.Shape != editspec.ShapeSquare {
		t.Fatalf("project.show shape: %+v", show.Shape)
	}
	shapes := map[string]string{}
	for _, c := range show.Cuts {
		if c.Shape != nil {
			shapes[c.Branch] = c.Shape.Key()
		}
	}
	if shapes["main"] != "1080x1080" || shapes["teaser"] != "1080x1350" {
		t.Fatalf("cuts' shapes: %v", shapes)
	}
	for _, r := range show.Revisions {
		if r.Shape.Key() == "" {
			t.Fatalf("a version without its shape: %+v", r)
		}
	}
	list := call[ProjectListOut](t, a, "project.list", ProjectListIn{})
	for _, row := range list.Projects {
		if row.ID == p.ID && (row.HeadShape == nil || row.HeadShape.Shape != editspec.ShapeSquare) {
			t.Fatalf("project.list head shape: %+v", row.HeadShape)
		}
	}
}

// Test frames follow the cuts by exact size (D377): the project's cuts that
// use the graphic; else cuts elsewhere in the workspace that use a graphic of
// that name (also with no project at all); else the project's shape; else
// both widescreen and vertical, never silently one.
func TestMotionShapesWithoutAProjectAndNothingKnown(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	labels := func(set motionShapeSet) string {
		var out []string
		for _, s := range motion.TidyShapes(set.Shapes) {
			out = append(out, fmt.Sprintf("%s %s", s.Label, s.Key()))
		}
		return set.From + ": " + strings.Join(out, ", ")
	}

	// Nothing known: both.
	if got := labels(motionShapes(ctx, a, nil, "lower-title", "generic")); got != "nothing: widescreen 1920x1080, vertical 1080x1920" {
		t.Fatalf("nothing known: %s", got)
	}
	empty := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Empty"}).Project
	if got := labels(motionShapes(ctx, a, projectByID(t, a, empty.ID), "lower-title", "generic")); got != "nothing: widescreen 1920x1080, vertical 1080x1920" {
		t.Fatalf("a project that says nothing: %s", got)
	}

	// A cut elsewhere in the workspace uses a graphic of that name: its shape,
	// with no project and for a project none of whose cuts uses it.
	other := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Launch"}).Project
	putCut(t, a, other.ID, "main", 1920, 1080, "lower-title")
	if got := labels(motionShapes(ctx, a, nil, "lower-title", "generic")); got != "workspace: widescreen 1920x1080" {
		t.Fatalf("no project, a workspace cut uses it: %s", got)
	}
	putCut(t, a, empty.ID, "main", 1080, 1920, "")
	if got := labels(motionShapes(ctx, a, projectByID(t, a, empty.ID), "lower-title", "generic")); got != "workspace: widescreen 1920x1080" {
		t.Fatalf("the project's cuts don't use it: %s", got)
	}
	// Archived projects' cuts don't count.
	if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": other.ID}, bson.M{"$set": bson.M{"archived": true}}); err != nil {
		t.Fatal(err)
	}
	if got := labels(motionShapes(ctx, a, projectByID(t, a, empty.ID), "lower-title", "generic")); got != "project: vertical 1080x1920" {
		t.Fatalf("only an archived project's cut uses it: the project's shape: %s", got)
	}

	// Two vertical sizes are two shapes, kept apart by size and named by ratio.
	putCut(t, a, empty.ID, "main", 1080, 1920, "lower-title")
	putCut(t, a, empty.ID, "feed", 1080, 1350, "lower-title")
	set := motionShapes(ctx, a, projectByID(t, a, empty.ID), "lower-title", "generic")
	if got := labels(set); got != "cuts: vertical 9:16 1080x1920, vertical 4:5 1080x1350" {
		t.Fatalf("two vertical sizes: %s", got)
	}

	// A check asked for a shape draws that one; a size works too.
	o, got, err := checkFrame(ctx, a, &MotionCheckIn{ProjectID: empty.ID, Shape: "1080x1350"}, t.TempDir()+"/lower-title", nil)
	if err != nil || got.From != shapesAsked || len(o.Shapes) != 1 || o.Shapes[0].Key() != "1080x1350" || o.Width != 1080 || o.Height != 1350 {
		t.Fatalf("asked for 1080x1350: %v %+v %+v", err, got, o.Shapes)
	}
	if _, _, err := checkFrame(ctx, a, &MotionCheckIn{ProjectID: empty.ID, Shape: "wide"}, t.TempDir()+"/lower-title", nil); code(err) != apperr.InvalidInput {
		t.Fatalf("a shape that isn't one: %v", err)
	}
	// Drawn again (D381): only the cuts' ratios it lacks; what it was drawn in stays in its record.
	again := &models.MotionComponent{Name: "lower-title", ProjectID: empty.ID, Shapes: []models.MotionShape{{Label: "vertical", Width: 720, Height: 1280}}}
	o, _, err = checkFrame(ctx, a, &MotionCheckIn{}, t.TempDir()+"/lower-title", again)
	if err != nil || o.ProjectID != empty.ID || len(o.Shapes) != 1 || o.Shapes[0].Key() != "1080x1350" {
		t.Fatalf("drawn again: %v %+v", err, o.Shapes)
	}
	// A 4K cut's shape is drawn at a test size, never 4K (D379).
	putCut(t, a, empty.ID, "uhd", 3840, 2160, "lower-title")
	if got := labels(motionShapes(ctx, a, projectByID(t, a, empty.ID), "lower-title", "generic")); !strings.Contains(got, "widescreen 1920x1080") || strings.Contains(got, "3840") {
		t.Fatalf("a 4K cut: %s", got)
	}
	// Past the overall bound, a redraw is refused in plain words.
	var many []models.MotionShape
	for i := 0; i < MaxDrawnShapes; i++ {
		many = append(many, models.MotionShape{Width: 1000 + 2*i, Height: 1000})
	}
	full := &models.MotionComponent{Name: "lower-title", Shapes: many}
	if _, _, err := checkFrame(ctx, a, &MotionCheckIn{Shape: "1080x1920"}, t.TempDir()+"/lower-title", full); code(err) != apperr.Conflict {
		t.Fatalf("past the bound: %v", err)
	}
}
