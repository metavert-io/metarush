package intake_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakerclone"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

func TestMain(m *testing.M) {
	fakerclone.MaybeRun() // the stand-in rclone (D220, D306)
	os.Exit(m.Run())
}

// useRclone points the app at a fake rclone.
func useRclone(a *app.App, tool string) {
	cfg := a.Cfg.Tools
	cfg.Rclone = tool
	ts := tools.New(cfg)
	ts.OnRun = a.Tools.OnRun
	a.Tools = ts
}

// Files from a Dropbox folder (D306) through the person's own rclone: listed
// with their IDs, brought in by name, ID or all, each checked and added like
// the Add box does — footage with where it came from — and never a remote of
// another kind.
func TestImportFromDropbox(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	root := t.TempDir()
	shoot := filepath.Join(root, "Shoot")
	_ = os.MkdirAll(shoot, 0o755)
	wav := tone(t, shoot, "A001.wav", 1.5, 440)
	_ = wav
	_ = os.WriteFile(filepath.Join(shoot, "notes.txt"), []byte("call sheet"), 0o644)
	tool, _ := fakerclone.Write(t, fakerclone.State{Remotes: []fakerclone.Remote{{Name: "dropbox", Type: "dropbox", Root: root}, {Name: "photos", Type: "s3", Root: t.TempDir()}}})
	useRclone(a, tool)
	p := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Garden walk"})

	list := call[intake.CloudListOut](t, a, "add.cloud.list", intake.CloudListIn{Folder: "dropbox:Shoot"})
	if list.Service != "Dropbox" || len(list.Files) != 2 || !strings.Contains(list.Summary, "“Shoot” in the Dropbox “dropbox”") {
		t.Fatalf("list %+v", list)
	}
	var a001 intake.CloudFile
	for _, f := range list.Files {
		if f.Name == "A001.wav" {
			a001 = f
		}
	}
	if !strings.HasPrefix(a001.ID, "id:") || a001.What != "footage" || !strings.HasPrefix(a001.Checksum, "dropbox:") {
		t.Fatalf("A001 %+v", a001)
	}
	out := call[intake.AddPathsOut](t, a, "add.cloud", intake.CloudAddIn{Folder: "dropbox:Shoot", IDs: []string{a001.ID}, ProjectID: p.Project.ID})
	if len(out.Files) != 1 || out.Files[0].Status != "ingested" || out.Files[0].AssetID == "" {
		t.Fatalf("added %+v", out.Files)
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), out.Files[0].AssetID, &as); err != nil {
		t.Fatal(err)
	}
	if as.Source == nil || as.Source.Platform != models.PlatformDropbox || as.Source.VideoID != a001.ID || as.Source.Tool != "rclone" || len(as.ProjectIDs) != 1 ||
		as.Source.Checksum != a001.Checksum {
		t.Fatalf("provenance %+v", as.Source)
	}
	// Several files at once: the folder is listed once, not again for each file.
	tone(t, shoot, "A002.wav", 1.2, 550)
	tone(t, shoot, "A003.wav", 1.1, 660)
	log := filepath.Join(t.TempDir(), "rclone.log")
	t.Setenv("METARUSH_FAKE_RCLONE_LOG", log)
	all := call[intake.AddPathsOut](t, a, "add.cloud", intake.CloudAddIn{Folder: "dropbox:Shoot", All: true, ProjectID: p.Project.ID})
	if len(all.Files) != 3 {
		t.Fatalf("all: %+v", all.Files)
	}
	b, _ := os.ReadFile(log)
	if n := strings.Count(string(b), `"lsjson"`); n != 1 {
		t.Fatalf("the folder was listed %d times:\n%s", n, b)
	}
	// By name too; something not there is said plainly.
	_, err := ops.CallTyped[intake.AddPathsOut](ctx, a, "add.cloud", intake.CloudAddIn{Folder: "dropbox:Shoot", Names: []string{"B001.wav"}}, ops.Caller{})
	if apperr.From(err).Code != apperr.NotFound || !strings.Contains(err.Error(), "B001.wav") {
		t.Fatalf("missing file: %v", err)
	}
	// Only Google Drive and Dropbox remotes.
	_, err = ops.CallTyped[intake.CloudListOut](ctx, a, "add.cloud.list", intake.CloudListIn{Folder: "photos:x"}, ops.Caller{})
	if apperr.From(err).Code != apperr.InvalidInput || !strings.Contains(err.Error(), "only Google Drive and Dropbox") {
		t.Fatalf("an s3 remote: %v", err)
	}
	// Never from over the network: the rclone is this computer's.
	_, err = ops.CallTyped[intake.CloudListOut](ctx, a, "add.cloud.list", intake.CloudListIn{Folder: "dropbox:Shoot"}, ops.Caller{Remote: true})
	if apperr.From(err).Code != apperr.Unauthorized {
		t.Fatalf("over the network: %v", err)
	}
}
