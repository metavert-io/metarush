package ops_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// The settings owner cleared out for good (D196 with D198–D200): its pictures
// move to the workspace library, so the logo files a follower's cut pins stay
// usable; the follower goes back to the default settings (none are set up
// now), without the gone house style and house sign-offs; nothing of the
// purged project's settings is left.
func TestPurgedSettingsOwnerKeepsItsLogosUsable(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	fernbrook := project(t, a, "fernbrook") // the first project: its settings are the default
	client := project(t, a, "Client")
	add := func(name, fill string) *models.Resource {
		p := filepath.Join(t.TempDir(), name)
		svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64"><rect width="64" height="64" rx="14" fill="` + fill + `"/></svg>`
		if err := os.WriteFile(p, []byte(svg), 0o644); err != nil {
			t.Fatal(err)
		}
		res, err := resources.AddFile(ctx, a, p, resources.AddOptions{ProjectID: fernbrook.ID, FileName: name})
		if err != nil || res.Resource == nil {
			t.Fatalf("add %s: %v %+v", name, err, res)
		}
		return res.Resource
	}
	main, white := add("fernbrook-logo.svg", "#2E7D50"), add("fernbrook-white.svg", "#F7F5F0")
	do[ops.BrandShowOut](t, a, "content.logo.set", ops.ContentLogoSetIn{Which: "main", Resource: main.ID})
	do[ops.BrandShowOut](t, a, "content.logo.set", ops.ContentLogoSetIn{Which: "dark", Resource: white.ID})
	do[ops.HouseSignoffsOut](t, a, "content.signoffs.add", ops.HouseSignoffAddIn{Text: "Legal OKs it before export"})
	on := "on"
	do[ops.ContentStyleOut](t, a, "content.style.set", ops.ContentStyleSetIn{Captions: &on})

	talk := clip(t, a, "Talk", client.ID)
	rev := saveCut(t, a, client.ID, "", talk.ID)
	spec := do[ops.RevisionShowOut](t, a, "revision.show", ops.RevisionShowIn{ID: rev}).Spec
	if spec.Resources["logo-dark"].ID != white.ID || spec.Brand == nil || spec.Brand.LogoDark != "logo-dark" || spec.Captions == nil || !spec.Captions.Enabled {
		t.Fatalf("Client's new cut has fernbrook's brand and house style: %+v %+v %+v", spec.Resources, spec.Brand, spec.Captions)
	}
	if so := do[ops.SignoffsOut](t, a, "cut.signoffs.list", ops.SignoffsIn{ProjectID: client.ID}); len(so.Signoffs) != 1 || !so.Signoffs[0].House {
		t.Fatalf("Client waits for fernbrook's house sign-off: %+v", so)
	}

	do[ops.ProjectArchiveOut](t, a, "project.delete", ops.ProjectDeleteIn{ID: fernbrook.ID})
	do[ops.ProjectPurgeOut](t, a, "project.purge", ops.ProjectPurgeIn{ID: fernbrook.ID, Forever: true})

	for _, r := range []*models.Resource{main, white} {
		var got models.Resource
		if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), r.ID, &got); err != nil || got.Scope != models.ScopeWorkspace || got.Removed {
			t.Fatalf("the logo %s is in the workspace library: %v %+v", r.Name, err, got.Scope)
		}
	}
	if n := count(t, a, store.CContentSettings, bson.M{"projectId": fernbrook.ID}); n != 0 {
		t.Fatalf("fernbrook's settings are gone: %d left", n)
	}
	// Client's cut still checks with the logos it pins.
	title := "After the clear-out"
	plan, _ := json.Marshal(editspec.Plan{BaseRevisionID: rev, Instruction: "rename", Ops: []editspec.Op{{Op: "setTitle", Text: &title}}})
	if chk := do[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: plan}); !chk.Valid {
		t.Fatalf("the cut's logos stay usable: %+v", chk.Errors)
	}
	// Client uses the default again, and no project's settings are the default now.
	if so := do[ops.SignoffsOut](t, a, "cut.signoffs.list", ops.SignoffsIn{ProjectID: client.ID}); len(so.Signoffs) != 0 {
		t.Fatalf("the gone house sign-off isn't waited for: %+v", so)
	}
	if st := do[ops.ContentStyleOut](t, a, "content.style.show", ops.ContentStyleIn{ProjectID: client.ID}); !st.Empty {
		t.Fatalf("no house style now: %+v", st)
	}
}
