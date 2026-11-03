package aiwork_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeheygen"
)

// A picture made for a cut — a background for it — is the cut's shape
// (D385), as near as the model makes, and the estimate says so; a shape
// asked for wins; a picture for no cut keeps the model's own default.
func TestAPictureForACutIsTheCutsShape(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: e.a.WorkspaceID(), ProjectID: e.project, Branch: "main", Number: 1,
		Spec: editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetVertical, Width: 1080, Height: 1920}}, CreatedAt: time.Now().UTC()}
	if err := e.a.DB.Insert(ctx, store.CRevisions, rev); err != nil {
		t.Fatal(err)
	}
	if err := e.a.DB.Update(ctx, store.CProjects, e.a.WorkspaceID(), e.project, bson.M{"$set": bson.M{"branches.main": models.BranchHead{Head: rev.ID}}}); err != nil {
		t.Fatal(err)
	}
	pic := aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "a soft green background", Branch: "main"}
	est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", pic, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(est.Shape, "9:16, because") || !strings.Contains(est.Shape, "is vertical") || est.AspectRatio != "9:16" {
		t.Fatalf("the estimate should say the cut's shape: %q %q", est.Shape, est.AspectRatio)
	}
	// A price agreed without the shape it was shown for is refused (D394).
	unagreed := pic
	unagreed.MaxUSD = usd(est.USD)
	if _, err := call[aiwork.MakeOut](t, e.a, "ai.make", unagreed, false); codeOf(err) != apperr.Conflict ||
		!strings.Contains(err.Error(), "didn't say its shape, and it would be made 9:16, because") {
		t.Fatalf("a price without its shape: %v", err)
	}
	if n := len(e.fake.Requests()); n != 0 {
		t.Fatalf("nothing is sent for a shape nobody agreed to: %d", n)
	}
	e.make(t, pic) // agrees the price with the shape it shows
	last := func() string {
		rs := e.fake.Requests()
		return rs[len(rs)-1].Options.AspectRatio
	}
	if got := last(); got != "9:16" {
		t.Fatalf("a background for a vertical cut is made %q", got)
	}
	asked := pic
	asked.Options.AspectRatio = "1:1"
	e.make(t, asked)
	if got := last(); got != "1:1" {
		t.Fatalf("a shape asked for wins: %q", got)
	}
	free := pic
	free.Branch = ""
	if est, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", free, false); err != nil || est.Shape != "" {
		t.Fatalf("a picture for no cut: %q %v", est.Shape, err)
	}
	e.make(t, free)
	if got := last(); got != "" {
		t.Fatalf("a picture for no cut keeps the model's default: %q", got)
	}
}

// A presenter clip asked for from the dashboard is made in the shape it was
// priced at (D394), driven with exactly the bodies the dashboard sends — no
// helper adds the shape. The reviewer's case: a Shorts brief prices the clip
// at 9:16; the body Presenters.tsx sent before ({...makeIn, maxUsd}) is
// refused, nothing sent; the body it sends now (agreedMake: the price with
// options.aspectRatio from the estimate) is made at 9:16. And the race:
// priced at 9:16, then nothing says the project's shape any more — a price
// alone is still refused, never made in the service's own shape.
func TestADashboardPresenterClipIsMadeInTheShapeItWasPricedAt(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	key := "hg" + strings.Repeat("Sh4", 11) + "fake"
	fake := fakeheygen.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvHeyGenBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.HeyGenName), key)
	if err := e.a.DB.Update(ctx, store.CProjects, e.a.WorkspaceID(), e.project, bson.M{"$set": bson.M{"brief.platforms": []string{"Shorts"}}}); err != nil {
		t.Fatal(err)
	}
	line := e.sound(t, "a plain line", 3)
	// Presenters.tsx's request (makeIn), as JSON.
	makeIn := map[string]any{"kind": "presenter", "projectId": e.project, "presenter": "stock:lk_amber_studio", "model": "heygen/avatar-iv", "clip": line.ID, "options": map[string]any{}}
	raw := func(m map[string]any) json.RawMessage { b, _ := json.Marshal(m); return b }
	res, err := ops.Call(ctx, e.a, "ai.estimate", raw(makeIn), ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	est := res.(*aiwork.AIEstimate)
	if est.AspectRatio != "9:16" || !strings.Contains(est.Shape, "YouTube Shorts") || est.USD <= 0 {
		t.Fatalf("priced at 9:16 for the Shorts brief: %+v", est)
	}
	// The body the dashboard sent before: the price without the shape.
	before := map[string]any{}
	for k, v := range makeIn {
		before[k] = v
	}
	before["maxUsd"] = est.USD
	if _, err := ops.Call(ctx, e.a, "ai.make", raw(before), ops.Caller{}); codeOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "didn't say its shape, and it would be made 9:16") {
		t.Fatalf("a price without its shape: %v", err)
	}
	if n := fake.Calls("POST /v3/videos"); n != 0 {
		t.Fatalf("nothing is sent: %d", n)
	}
	// The body it sends now (agreedMake).
	agreed := map[string]any{}
	for k, v := range before {
		agreed[k] = v
	}
	agreed["options"] = map[string]any{"aspectRatio": est.AspectRatio}
	res, err = ops.Call(ctx, e.a, "ai.make", raw(agreed), ops.Caller{})
	if err != nil {
		t.Fatalf("the agreed body: %v", err)
	}
	if g := e.gen(t, res.(*aiwork.MakeOut).Work.ID); g.Options.AspectRatio != "9:16" {
		t.Fatalf("asked for in the shape it was priced at: %+v", g.Options)
	}
	// The race: priced at 9:16, then nothing says the shape — a price alone is refused.
	if err := e.a.DB.Update(ctx, store.CProjects, e.a.WorkspaceID(), e.project, bson.M{"$unset": bson.M{"brief.platforms": ""}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.Call(ctx, e.a, "ai.make", raw(before), ops.Caller{}); codeOf(err) != apperr.Conflict || !strings.Contains(err.Error(), "it would be made 16:9, HeyGen Avatar IV's usual shape: nothing about the project says yet") {
		t.Fatalf("priced at 9:16, nothing says now, a price alone: %v", err)
	}
	if _, err := ops.Call(ctx, e.a, "ai.make", raw(agreed), ops.Caller{}); err != nil {
		t.Fatalf("the agreed shape still goes: %v", err)
	}
}
