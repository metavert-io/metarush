package generate_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
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
	in.MaxUSD, in.AspectRatio = usd(est.USD), est.AspectRatio // the price agreed for the shape it was shown for (D394)
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Generations[0].Params.AspectRatio != "9:16" || out.Estimate.ShapeFrom != "project" {
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

// A price is agreed for the shape the estimate showed (D394), the reviewer's
// case: priced wide while nothing said the project's shape, then the project
// got a vertical cut; the same request with that price and no shape is
// refused plainly (nothing sent), the agreed shape sent with it is made as
// agreed, and a shape sent that is MetaRush's own keeps its reason.
func TestAPriceIsAgreedForItsShape(t *testing.T) {
	e := setup(t)
	in := generate.GenShotIn{ProjectID: e.project, Prompt: "a red paper boat drifting on a pond", Seconds: 3, Resolution: "360p"}
	est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if est.AspectRatio != "16:9" || est.ShapeFrom != "default" {
		t.Fatalf("priced wide, nothing says: %+v", est)
	}
	e.cutOf(t, "main", editspec.Output{Preset: editspec.PresetVertical, Width: 1080, Height: 1920})

	in.MaxUSD = usd(est.USD)
	_, err = call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if codeOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "didn't say the shot's shape, and it would be made tall (9:16), because its cut") {
		t.Fatalf("a price agreed without its shape, now tall: %v", err)
	}
	if d, _ := apperr.From(err).Details.(map[string]any); d["reason"] != "shape_not_agreed" || d["aspectRatio"] != "9:16" {
		t.Fatalf("details: %+v", apperr.From(err).Details)
	}
	if e.fake.Calls("create") != 0 {
		t.Fatal("nothing is sent for a shape nobody agreed to")
	}

	// The agreed shape sent with the price: made as agreed.
	in.AspectRatio = est.AspectRatio
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if g := out.Generations[0]; g.Params.AspectRatio != "16:9" || out.Estimate.ShapeFrom != "asked" {
		t.Fatalf("made as agreed: %+v %+v", g.Params, out.Estimate)
	}
	// The shape MetaRush would choose, sent with the price: its own reason stays.
	now, err := call[generate.GenEstimate](t, e.a, "generate.estimate", generate.GenShotIn{ProjectID: e.project, Prompt: in.Prompt, Seconds: 3, Resolution: "360p"}, false)
	if err != nil {
		t.Fatal(err)
	}
	in.AspectRatio, in.MaxUSD = now.AspectRatio, usd(now.USD)
	out, err = call[generate.GenRunOut](t, e.a, "generate.shot", in, false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Generations[0].Params.AspectRatio != "9:16" || out.Estimate.ShapeFrom != "cut" || !strings.HasPrefix(out.Estimate.ShapeWhy, "Tall (9:16), because its cut") {
		t.Fatalf("agreed tall, MetaRush's own reason kept: %+v", out.Estimate)
	}
	// The race (D394): priced at 9:16 for a Shorts brief, then nothing says the
	// project's shape — a price alone is refused (naming the shape it would be
	// made in now), never made in the model's own; the Generate panel's body
	// (agreedRequest: the price with the estimate's shape) is made at 9:16.
	e2 := setup(t)
	if err := e2.a.DB.Update(context.Background(), store.CProjects, e2.a.WorkspaceID(), e2.project, bson.M{"$set": bson.M{"brief.platforms": []string{"Shorts"}}}); err != nil {
		t.Fatal(err)
	}
	panelIn := map[string]any{"projectId": e2.project, "prompt": "a boat", "seconds": 3, "quality": "draft", "resolution": "360p", "takes": 1}
	raw := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	res, err := ops.Call(context.Background(), e2.a, "generate.estimate", raw(panelIn), ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	tall := res.(*generate.GenEstimate)
	if tall.AspectRatio != "9:16" {
		t.Fatalf("priced tall: %+v", tall)
	}
	if err := e2.a.DB.Update(context.Background(), store.CProjects, e2.a.WorkspaceID(), e2.project, bson.M{"$unset": bson.M{"brief.platforms": ""}}); err != nil {
		t.Fatal(err)
	}
	priceOnly := map[string]any{"maxUsd": tall.USD}
	for k, v := range panelIn {
		priceOnly[k] = v
	}
	if _, err := ops.Call(context.Background(), e2.a, "generate.shot", raw(priceOnly), ops.Caller{}); codeOf(err) != apperr.Conflict ||
		!strings.Contains(err.Error(), "didn't say the shot's shape, and it would be made wide (16:9)") {
		t.Fatalf("priced tall, nothing says now, a price alone: %v", err)
	}
	agreed := map[string]any{"aspectRatio": tall.AspectRatio}
	for k, v := range priceOnly {
		agreed[k] = v
	}
	res, err = ops.Call(context.Background(), e2.a, "generate.shot", raw(agreed), ops.Caller{})
	if err != nil {
		t.Fatalf("the panel's agreed body: %v", err)
	}
	if g := res.(*generate.GenRunOut).Generations[0]; g.Params.AspectRatio != "9:16" {
		t.Fatalf("made in the shape it was priced at: %+v", g.Params)
	}
	if e2.fake.Calls("create") != 0 {
		t.Fatal("the refused request sent nothing (and the agreed one waits for its job)")
	}
}
