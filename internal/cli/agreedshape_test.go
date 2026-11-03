package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeai"
	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/videogen"
)

// The terminal agrees a price with its shape (D394): `generate shot` and
// `ai make` with --max-cost and no --aspect are refused in plain words that
// name the shape the estimate shows and the flag that says it, nothing sent;
// with --aspect they are made in that shape.
func TestTheTerminalAgreesAPriceWithItsShape(t *testing.T) {
	keyring.MockInit()
	gkey := "gk-" + strings.Repeat("Tm7", 10) + "-fake"
	gen := fakevideogen.New(gkey)
	defer gen.Close()
	t.Setenv(videogen.EnvGeminiBaseURL, gen.URL)
	t.Setenv(providers.EnvVar(videogen.GeminiName), gkey)
	akey := "fk-" + strings.Repeat("Tm7", 10) + "-fake"
	ai := fakeai.Install(t, akey)
	t.Setenv(providers.EnvVar(fakeai.Name), akey)
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	pc, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Phone ad", Brief: models.Brief{Platforms: []string{"Shorts"}}}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	p := pc.Project.ID
	home := a.Cfg.Home()

	shot := []string{"generate", "shot", "a red paper boat", "--project", p, "--seconds", "3", "--resolution", "360p", "--max-cost", "0.5"}
	r := runCLI(t, home, shot...)
	if r.code == 0 || !strings.Contains(r.stderr, "it would be made tall (9:16)") || !strings.Contains(r.stderr, "Say the shape with the price: --aspect 9:16.") ||
		!strings.Contains(r.stderr, "--aspect 9:16") {
		t.Fatalf("--max-cost without --aspect: code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if gen.Calls("create") != 0 {
		t.Fatal("nothing is sent for a shape nobody agreed to")
	}
	if r = runCLI(t, home, append(shot, "--aspect", "9:16")...); r.code != 0 {
		t.Fatalf("with the shape: code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	var gs []models.Generation
	if err := a.DB.Find(ctx, store.CGenerations, ws, bson.M{"projectId": p, "operation": models.GenShot}, &gs); err != nil || len(gs) != 1 || gs[0].Params.AspectRatio != "9:16" {
		t.Fatalf("one shot, asked for tall: %+v %v", gs, err)
	}

	// A picture for a cut, from the terminal: the same.
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: ws, ProjectID: p, Branch: "main", Number: 1,
		Spec: editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetVertical, Width: 1080, Height: 1920}}, CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CRevisions, rev); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Update(ctx, store.CProjects, ws, p, bson.M{"$set": bson.M{"branches.main": models.BranchHead{Head: rev.ID}}}); err != nil {
		t.Fatal(err)
	}
	pic := []string{"ai", "make", "picture", "a soft green background", "--project", p, "--cut", "main", "--model", "fake/fake-picture", "--max-cost", "1"}
	r = runCLI(t, home, pic...)
	if r.code == 0 || !strings.Contains(r.stderr, "it would be made 9:16") || !strings.Contains(r.stderr, "Say the shape with the price: --aspect 9:16.") {
		t.Fatalf("ai make --max-cost without --aspect: code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if n := len(ai.Requests()); n != 0 {
		t.Fatalf("nothing is sent: %d", n)
	}
	if r = runCLI(t, home, append(pic, "--aspect", "9:16")...); r.code != 0 {
		t.Fatalf("ai make with the shape: code %d\n%s%s", r.code, r.stdout, r.stderr)
	}
}
