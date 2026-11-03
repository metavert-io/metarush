package generate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// cutOf gives the project a cut on a branch whose latest version has this
// output (only its output is read for its shape).
func (e *env) cutOf(t *testing.T, branch string, out editspec.Output) {
	t.Helper()
	ctx := context.Background()
	n, _ := e.a.DB.C(store.CRevisions).CountDocuments(ctx, bson.M{"projectId": e.project})
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: e.a.WorkspaceID(), ProjectID: e.project, Branch: branch, Number: int(n) + 1,
		Spec: editspec.Spec{SchemaVersion: 1, Output: out}, CreatedAt: time.Now().UTC()}
	if err := e.a.DB.Insert(ctx, store.CRevisions, rev); err != nil {
		t.Fatal(err)
	}
	if err := e.a.DB.Update(ctx, store.CProjects, e.a.WorkspaceID(), e.project, bson.M{"$set": bson.M{"branches." + branch: models.BranchHead{Head: rev.ID}}}); err != nil {
		t.Fatal(err)
	}
}

// A vertical project's shot is priced and made vertical (D385): asked for no
// shape, the shot is made in the project's (here its brief's "social
// shorts"), the estimate says which and why before the price is agreed, the
// provider is asked for 9:16 and the clip comes back tall. A shape asked for
// is made as asked, with the shape that would fit said; a shot for a named
// cut is made in that cut's shape; a square cut gets the nearest the model
// makes, saying so.
func TestAVerticalProjectsShotIsPricedAndMadeVertical(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if err := e.a.DB.Update(ctx, store.CProjects, e.a.WorkspaceID(), e.project, bson.M{"$set": bson.M{"brief.deliverables": []models.Deliverable{{Name: "Social shorts", Preset: editspec.PresetVertical, Platform: "shorts"}}}}); err != nil {
		t.Fatal(err)
	}
	in := generate.GenShotIn{ProjectID: e.project, Prompt: "a red paper boat drifting on a pond", Seconds: 3, Resolution: "360p"}
	est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if est.AspectRatio != "9:16" || est.ShapeFrom != "project" || est.FitsCut != "9:16" || !strings.Contains(est.Summary, "tall") ||
		est.ShapeWhy != "Tall (9:16), because its brief says “Social shorts” is vertical." {
		t.Fatalf("a vertical project's shot should be priced tall, saying why: %+v", est)
	}
	in.MaxUSD = usd(est.USD)
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Generations[0].Params.AspectRatio != "9:16" {
		t.Fatalf("recorded as %q", out.Generations[0].Params.AspectRatio)
	}
	if _, err := e.run(t, ctx, out.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	if its := e.fake.Interactions(); len(its) != 1 || its[0].AspectRatio != "9:16" {
		t.Fatalf("the provider should be asked for 9:16: %+v", its)
	}
	v, err := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	var as models.Asset
	if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), v.Clips[0].AssetID, &as); err != nil {
		t.Fatal(err)
	}
	if as.Media.Height <= as.Media.Width {
		t.Fatalf("the clip should be tall: %dx%d", as.Media.Width, as.Media.Height)
	}

	// A shape asked for is made as asked; the one that fits is said.
	in.MaxUSD, in.AspectRatio = nil, "16:9"
	asked, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if asked.AspectRatio != "16:9" || asked.ShapeFrom != "asked" || asked.FitsCut != "9:16" || !strings.Contains(asked.ShapeWhy, "tall (9:16) would fit it") {
		t.Fatalf("asked for wide: %+v", asked)
	}

	// For a named cut: that cut's shape, over the project's.
	e.cutOf(t, "wide", editspec.Output{Preset: editspec.PresetLandscape, Width: 1920, Height: 1080})
	in.AspectRatio, in.Cut = "", "wide"
	forCut, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if forCut.AspectRatio != "16:9" || forCut.ShapeFrom != "cut" || !strings.Contains(forCut.ShapeWhy, "is widescreen") {
		t.Fatalf("a shot for the widescreen cut: %+v", forCut)
	}
	// A square cut: the nearest the model makes, said plainly.
	e.cutOf(t, "feed", editspec.Output{Preset: editspec.PresetSquare, Width: 1080, Height: 1080})
	in.Cut = "feed"
	square, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if square.AspectRatio != "16:9" || !strings.Contains(square.ShapeWhy, "makes only wide or tall shots, so it is the nearest and the cut frames it square") {
		t.Fatalf("a square cut: %+v", square)
	}
	in.Cut = "nope"
	if _, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false); codeOf(err) != apperr.NotFound {
		t.Fatalf("a cut the project hasn't: %v", err)
	}
}

// A project nothing says the shape of still gets the model's own (Gemini's
// 16:9), said as such.
func TestAShotForAProjectThatSaysNothing(t *testing.T) {
	e := setup(t)
	est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", generate.GenShotIn{ProjectID: e.project, Prompt: "a boat", Seconds: 3, Resolution: "360p"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if est.AspectRatio != "16:9" || est.ShapeFrom != "default" || est.FitsCut != "" || !strings.Contains(est.ShapeWhy, "nothing about the project says yet") {
		t.Fatalf("estimate %+v", est)
	}
}
