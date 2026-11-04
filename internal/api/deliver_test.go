package api

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakec2patool"
)

// The dashboard's Deliver (D422): signed-in requests with the CSRF header
// only; MetaRush picks a new folder under the exports folder each time, runs
// delivery as the person (nothing left behind when it refuses), and the
// videos carry Content Credentials where they are on.
func TestDashboardDeliverWritesIntoItsOwnFolder(t *testing.T) {
	keyring.MockInit()
	exports := t.TempDir()
	t.Setenv("METARUSH_EXPORTS_DIR", exports)
	d, _, rev := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	ctx := context.Background()
	person := ops.Caller{Human: true}
	if _, err := ops.CallTyped[ops.RevisionSummary](ctx, d.a, "revision.approve", ops.RevisionApproveIn{ID: rev}, person); err != nil {
		t.Fatal(err)
	}
	in := DeliverHereIn{RevisionID: rev}
	if code, _ := d.do("POST", "/api/deliver", in, true); code != 401 {
		t.Fatalf("without a session: %d", code)
	}
	d.signIn()
	if code, env := d.do("POST", "/api/deliver", in, false); code != 401 || env.Error.Details["reason"] != "csrf" {
		t.Fatalf("without the CSRF header: %d %+v", code, env.Error)
	}
	// No final render yet: refused in plain words, and no folder is left.
	if code, env := d.do("POST", "/api/deliver", in, true); code == 200 || env.Error.Code != "E_NOT_FOUND" {
		t.Fatalf("nothing rendered: %d %+v", code, env.Error)
	}
	if left, _ := os.ReadDir(exports); len(left) != 0 {
		t.Fatalf("a folder was left: %v", left)
	}
	// A final render.
	var r models.Revision
	if err := d.a.DB.GetByID(ctx, store.CRevisions, d.a.WorkspaceID(), rev, &r); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 4096)
	_, _ = rand.Read(b)
	p := filepath.Join(t.TempDir(), "final.mp4")
	_ = os.WriteFile(p, b, 0o644)
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(d.a.WorkspaceID(), sha)
	if _, err := d.a.Media.PutFile(ctx, key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := d.a.DB.Insert(ctx, store.CRenders, models.Render{ID: ids.New(ids.Render), WorkspaceID: d.a.WorkspaceID(), ProjectID: r.ProjectID, RevisionID: r.ID,
		RevisionNumber: r.Number, SpecHash: r.SpecHash, Preset: r.Spec.Output.Preset, Quality: "final", State: models.RenderAvailable, CreatedAt: now, CompletedAt: &now,
		Output: &models.RenderOutput{StorageKey: key, SHA256: sha, Size: int64(len(b)), Duration: r.Duration, Width: 1080, Height: 1920, FPS: 30}}); err != nil {
		t.Fatal(err)
	}
	code, env := d.do("POST", "/api/deliver", in, true)
	if code != 200 {
		t.Fatalf("deliver: %d %+v", code, env.Error)
	}
	var out DeliverHereOut
	_ = json.Unmarshal(env.Data, &out)
	if filepath.Dir(out.Folder) != exports || !strings.HasSuffix(out.Folder, "API film version 1") || len(out.Deliver.Files) != 1 || filepath.Dir(out.Deliver.Files[0].Path) != out.Folder {
		t.Fatalf("delivered: %+v", out)
	}
	if got, err := os.ReadFile(out.Deliver.Files[0].Path); err != nil || string(got) != string(b) || out.Deliver.Files[0].ContentCredentials != nil {
		t.Fatalf("the delivered video: %v %+v", err, out.Deliver.Files[0].ContentCredentials)
	}
	// With Content Credentials on: a second folder, the video signed.
	runs := filepath.Join(t.TempDir(), "c2patool.log")
	tool, _ := fakec2patool.Write(t, fakec2patool.State{Log: runs})
	d.a.Cfg.Tools.C2PATool = tool
	d.a.Tools.Rescan(d.a.Cfg.Tools)
	if err := d.a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: d.a.WorkspaceID(), WorkspaceID: d.a.WorkspaceID(), Name: "API test"}); err != nil {
		t.Fatal(err) // the workspace's record, as init makes it
	}
	if _, err := ops.CallTyped[ops.CredentialsSetupOut](ctx, d.a, "credentials.setup", ops.CredentialsSetupIn{Test: true}, person); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[ops.CredentialsStatusOut](ctx, d.a, "credentials.set", ops.CredentialsSetIn{Mode: models.CredentialsOn}, person); err != nil {
		t.Fatal(err)
	}
	code, env = d.do("POST", "/api/deliver", in, true)
	var signed DeliverHereOut
	_ = json.Unmarshal(env.Data, &signed)
	if code != 200 || signed.Folder == out.Folder || filepath.Dir(signed.Folder) != exports {
		t.Fatalf("again: %d %+v %+v", code, env.Error, signed)
	}
	if c := signed.Deliver.Files[0].ContentCredentials; c == nil || !c.Signed || !c.Verified || !strings.Contains(signed.Deliver.Note, "carries Content Credentials") {
		for _, r := range fakec2patool.Calls(runs) { // what c2patool was asked and said
			t.Logf("c2patool %q: exit %d, said %q", r.Args, r.Exit, r.Said)
		}
		t.Fatalf("signed: %+v %s", c, signed.Deliver.Note)
	}
	// Show in Finder takes the delivery's folder like a timeline's.
	if code, env := d.do("POST", "/api/export/reveal", ExportRevealIn{Folder: "/etc"}, true); code == 200 || env.Error.Code != "E_UNAUTHORIZED" {
		t.Fatalf("reveal outside the exports folder: %d %+v", code, env.Error)
	}
}
