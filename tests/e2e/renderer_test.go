package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	metarush "github.com/metavert-io/metarush"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakenode"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/worker"
)

// Renders run with this binary's own renderer in <home>/renderer/<version>
// (D75): an installation that used to render from a source checkout moves to
// it, editing the checkout afterwards changes nothing about the next render,
// and once the renderer is deleted the machine stops offering renders and
// says how to set it up again. Node is a fake (it lays down packages and
// renders an ffmpeg test pattern), so this runs anywhere ffmpeg does.
func TestRenderUsesThisVersionsRenderer(t *testing.T) {
	A, _, ts := twoMachines(t)
	ctx := context.Background()
	pkg, err := renderer.Embedded()
	if err != nil {
		t.Fatal(err)
	}
	fake := fakenode.Write(t, pkg.Remotion())
	// An older installation's setting: renders came from a source checkout.
	checkout := filepath.Join(t.TempDir(), "video")
	src, _ := fs.ReadFile(metarush.RendererFiles, "video/render/render.mjs")
	if err := os.MkdirAll(filepath.Join(checkout, "render"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "render", "render.mjs"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	A.app.Cfg.Tools.Node, A.app.Cfg.Tools.NPM, A.app.Cfg.Tools.VideoDir = fake.Node, fake.NPM, checkout
	A.app.Tools = tools.New(A.app.Cfg.Tools)
	if w := worker.New(A.app); w.Can(jobs.TypeRender) {
		t.Fatal("a machine whose renderer is not set up offered to render")
	}
	st, err := renderer.New(A.app.Cfg, A.app.Tools).Install(ctx, renderer.InstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(A.home, "renderer", pkg.ID()); !sameDir(st.Dir, want) || !strings.Contains(st.Notice, "renderer dev") {
		t.Fatalf("renderer at %s (want %s), notice %q", st.Dir, want, st.Notice)
	}

	proj := call[ops.ProjectOut](t, A.app, "project.create", ops.ProjectCreateIn{Name: "renderer per version"})
	clipPath := clip(t, ts, t.TempDir(), "take.mp4", "testsrc2=size=1280x720:rate=30:duration=4")
	res := ingest.Paths(ctx, A.app, []string{clipPath}, ingest.Options{ProjectID: proj.Project.ID, Via: "cli"})
	if len(res) != 1 || res[0].Status != "ingested" {
		t.Fatalf("ingest: %+v", res)
	}
	plan, _ := json.Marshal(map[string]any{"projectId": proj.Project.ID, "instruction": "two seconds", "spec": map[string]any{
		"schemaVersion": 1, "output": map[string]any{"preset": "vertical-1080p", "fps": 30},
		"clips": []any{map[string]any{"id": "a", "assetId": res[0].AssetID, "in": 0.5, "out": 2.5}}}})
	chk := call[ops.EditCheckOut](t, A.app, "edit.check", ops.EditCheckIn{Plan: plan})
	if !chk.Valid {
		t.Fatalf("plan invalid: %+v", chk.Errors)
	}
	rev := call[ops.EditApplyOut](t, A.app, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken})

	wctx, stop := context.WithCancel(ctx)
	defer stop()
	w := worker.New(A.app)
	w.IgnoreBattery = true
	w.Log = func(string, ...any) {}
	go func() { _ = w.Run(wctx) }()
	render := func(key string) models.Render {
		t.Helper()
		sub := call[ops.RenderOut](t, A.app, "render.submit", ops.RenderSubmitIn{RevisionID: rev.Revision.ID, Quality: "review", IdempotencyKey: key, NoQA: true})
		waitJob(t, A.app.Queue, sub.Job.ID, 3*time.Minute)
		var rd models.Render
		if err := A.app.DB.GetByID(ctx, store.CRenders, A.app.WorkspaceID(), sub.Render.ID, &rd); err != nil {
			t.Fatal(err)
		}
		return rd
	}
	lastRender := func() string {
		runs := fake.Runs(t)
		for i := len(runs) - 1; i >= 0; i-- {
			if dir, ok := strings.CutPrefix(runs[i], "render "); ok {
				return dir
			}
		}
		t.Fatalf("no render ran: %v", runs)
		return ""
	}

	r1 := render("first")
	if !sameDir(lastRender(), st.VideoDir) || r1.RendererID != pkg.ID() || r1.State != models.RenderAvailable {
		t.Fatalf("render ran in %s with renderer %q (want %s, %s)", lastRender(), r1.RendererID, st.VideoDir, pkg.ID())
	}
	if !bytes.Equal(fake.LastRenderScript(t), src) {
		t.Fatal("the render did not run this version's render.mjs")
	}

	// Editing the checkout (a merge into main, say) does not change the next render.
	if err := os.WriteFile(filepath.Join(checkout, "render", "render.mjs"), []byte("// a newer, different renderer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r2 := render("second")
	if !sameDir(lastRender(), st.VideoDir) || r2.RendererID != pkg.ID() || !bytes.Equal(fake.LastRenderScript(t), src) {
		t.Fatalf("after editing the checkout the render ran in %s with %q", lastRender(), r2.RendererID)
	}
	stop()

	// Deleting the renderer: renders are no longer offered here, and the
	// machine says how to set it up again.
	if err := os.RemoveAll(st.Dir); err != nil {
		t.Fatal(err)
	}
	err = worker.RenderReady(A.app)
	if err == nil || !strings.Contains(err.Error(), "removed") || !strings.Contains(err.Error(), "metarush renderer install") {
		t.Fatalf("after deleting the renderer: %v", err)
	}
	if worker.New(A.app).Can(jobs.TypeRender) {
		t.Fatal("a machine whose renderer was deleted still offers to render")
	}
}

func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}
