package aiwork_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
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
	if !strings.HasPrefix(est.Shape, "9:16, because") || !strings.Contains(est.Shape, "is vertical") {
		t.Fatalf("the estimate should say the cut's shape: %q", est.Shape)
	}
	e.make(t, pic)
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
