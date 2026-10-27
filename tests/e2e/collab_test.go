// Package e2e exercises two independent installations sharing one database and one
// S3-compatible bucket (an in-process emulator), with separate homes and caches.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"

	"github.com/metavert-io/metarush/internal/api"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/worker"
)

type install struct {
	app  *app.App
	home string
}

func newInstall(t *testing.T, db *store.DB, s3url, wsID, name string) *install {
	t.Helper()
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: wsID, Name: "E2E"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: name}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: name + "-user", Email: name + "@example.com"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "s3", S3: config.S3{Endpoint: s3url, Region: "us-east-1", Bucket: "media", Prefix: "team", AccessKeyID: "k", SecretAccessKey: "s", ForcePathStyle: true}}
	cfg.Worker.Concurrency = 3
	ctx := context.Background()
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CInstallations, models.Installation{ID: cfg.Installation.ID, WorkspaceID: wsID, Name: name, CreatedAt: now, LastSeenAt: now}); err != nil {
		t.Fatal(err)
	}
	return &install{app: a, home: h}
}

func clip(t *testing.T, ts *tools.Set, dir, name, src string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	_, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", src, "-f", "lavfi", "-i", "sine=frequency=330:duration=4",
		"-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", p}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func call[O any](t *testing.T, a *app.App, name string, in any) *O {
	t.Helper()
	out, err := ops.CallTyped[O](context.Background(), a, name, in, ops.Caller{})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func TestTwoInstallationsShareProjectsAndMedia(t *testing.T) {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	db := testutil.DB(t)
	backend := s3mem.New()
	_ = backend.CreateBucket("media")
	s3srv := httptest.NewServer(gofakes3.New(backend).Server())
	defer s3srv.Close()
	wsID := ids.New(ids.Workspace)
	ctx := context.Background()
	if err := db.Insert(ctx, store.CWorkspaces, models.Workspace{ID: wsID, WorkspaceID: wsID, Name: "E2E", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	A := newInstall(t, db, s3srv.URL, wsID, "laptop-a")
	B := newInstall(t, db, s3srv.URL, wsID, "studio-b")
	if A.app.Cfg.Cache.Dir == B.app.Cfg.Cache.Dir {
		t.Fatal("installations must have independent caches")
	}

	// A creates a project and ingests two real files.
	proj := call[ops.ProjectOut](t, A.app, "project.create", ops.ProjectCreateIn{Name: "Shared film"})
	src := t.TempDir()
	f1 := clip(t, ts, src, "take1.mp4", "testsrc2=size=1280x720:rate=30:duration=4")
	f2 := clip(t, ts, src, "take2.mov", "mandelbrot=size=640x360:rate=25")
	before1, _ := media.HashFile(f1)
	before2, _ := media.HashFile(f2)
	res := ingest.Paths(ctx, A.app, []string{f1, f2}, ingest.Options{ProjectID: proj.Project.ID, Via: "cli", Transcribe: false})
	var jobIDs []string
	for _, r := range res {
		if r.Status != "ingested" {
			t.Fatalf("ingest %s: %s %s", r.Name, r.Status, r.Error)
		}
		jobIDs = append(jobIDs, r.JobIDs...)
	}
	// A's worker processes derivatives, then A goes offline.
	wctx, stop := context.WithCancel(ctx)
	w := worker.New(A.app)
	w.Log = func(string, ...any) {}
	w.IgnoreBattery = true // a laptop on battery would hold heavy jobs back
	go func() { _ = w.Run(wctx) }()
	deadline := time.Now().Add(90 * time.Second)
	for {
		done := 0
		for _, id := range jobIDs {
			j, _ := A.app.Queue.Get(ctx, id)
			if j.State == models.JobSucceeded {
				done++
			} else if j.State == models.JobFailed {
				t.Fatalf("job %s %s failed: %s", j.ID, j.Type, j.Error)
			}
		}
		if done == len(jobIDs) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("derivative jobs did not finish")
		}
		time.Sleep(300 * time.Millisecond)
	}
	stop()
	A.app.Close()

	// B (with an empty cache) sees the same project, metadata and variants.
	ps := call[ops.ProjectShowOut](t, B.app, "project.show", ops.ProjectShowIn{ID: proj.Project.ID})
	if len(ps.Assets) != 2 {
		t.Fatalf("B sees %d assets", len(ps.Assets))
	}
	var asset ops.AssetSummary
	for _, as := range ps.Assets {
		if !strings.Contains(strings.Join(as.Variants, ","), "proxy") {
			t.Fatalf("asset %s missing proxy on B: %v", as.Name, as.Variants)
		}
		if as.Name == "take1.mp4" {
			asset = as
		}
	}
	if st, _ := B.app.Cache.Status(); st.Objects != 0 {
		t.Fatalf("B's cache should start empty, has %d", st.Objects)
	}

	// B's dashboard: sign in with a one-time nonce, then range-read the proxy.
	srv := api.New(B.app)
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	B.app.Cfg.Dashboard.Port = mustPort(t, hs.URL)
	srv2 := api.New(B.app) // rebuild with the right port for the Host check
	hs.Config.Handler = srv2.Handler()
	nonce, _ := api.MintLaunchNonce(B.app.Cfg.StateDir())
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	if res, err := client.Get(hs.URL + "/auth?t=" + nonce); err != nil || res.StatusCode != 200 {
		t.Fatalf("sign-in failed: %v %v", err, res)
	}
	req, _ := http.NewRequest("GET", hs.URL+"/media/assets/"+asset.ID+"/proxy", nil)
	req.Header.Set("Range", "bytes=1000-1999")
	resp, err := client.Do(req) // follows the presigned redirect to the bucket
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || len(body) != 1000 {
		t.Fatalf("ranged proxy read: status %d, %d bytes", resp.StatusCode, len(body))
	}
	var full models.Asset
	_ = B.app.DB.GetByID(ctx, store.CAssets, wsID, asset.ID, &full)
	pv := full.Variants[models.VariantProxy]
	p, err := B.app.Cache.Ensure(ctx, B.app.Media, pv.StorageKey, pv.SHA256, pv.Size, nil)
	if err != nil {
		t.Fatal(err)
	}
	pb, _ := os.ReadFile(p)
	if !bytes.Equal(pb[1000:2000], body) {
		t.Fatal("bytes served by B's dashboard differ from the canonical proxy")
	}
	// Unauthenticated and cross-host requests are refused.
	if r, _ := http.Get(hs.URL + "/api/status"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status: %d", r.StatusCode)
	}
	evil, _ := http.NewRequest("GET", hs.URL+"/api/health", nil)
	evil.Host = "rebind.attacker.test"
	if r, _ := http.DefaultClient.Do(evil); r.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host accepted: %d", r.StatusCode)
	}

	// Originals are unchanged.
	if h, _ := media.HashFile(f1); h != before1 {
		t.Fatal("original take1 modified")
	}
	if h, _ := media.HashFile(f2); h != before2 {
		t.Fatal("original take2 modified")
	}

	// B creates a revision; a stale full-spec plan from "A" is rejected (no silent overwrite).
	A2 := newInstall(t, db, s3srv.URL, wsID, "laptop-a-again")
	plan := map[string]any{"projectId": proj.Project.ID, "instruction": "first cut", "spec": map[string]any{
		"schemaVersion": 1, "output": map[string]any{"preset": "landscape-1080p", "fps": 30},
		"clips": []any{map[string]any{"id": "a", "assetId": asset.ID, "in": 0.5, "out": 3.5}}}}
	raw, _ := json.Marshal(plan)
	chk := call[ops.EditCheckOut](t, B.app, "edit.check", ops.EditCheckIn{Plan: raw})
	if !chk.Valid {
		t.Fatalf("plan invalid: %+v", chk.Errors)
	}
	applied := call[ops.EditApplyOut](t, B.app, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: chk.ConfirmToken})
	stale := map[string]any{"baseRevisionId": applied.Revision.ID, "spec": plan["spec"], "instruction": "A's version"}
	// B moves the head first...
	ops1, _ := json.Marshal(map[string]any{"baseRevisionId": applied.Revision.ID, "ops": []any{map[string]any{"op": "updateClip", "id": "a", "set": map[string]any{"out": 3.0}}}})
	c1 := call[ops.EditCheckOut](t, B.app, "edit.check", ops.EditCheckIn{Plan: ops1})
	call[ops.EditApplyOut](t, B.app, "edit.apply", ops.EditApplyIn{Plan: ops1, ConfirmToken: c1.ConfirmToken})
	// ...then A2 tries to apply a full spec written against the old head.
	rawStale, _ := json.Marshal(stale)
	_, err = ops.CallTyped[ops.EditCheckOut](ctx, A2.app, "edit.check", ops.EditCheckIn{Plan: rawStale}, ops.Caller{})
	if !apperr.Is(err, apperr.RevisionConflict) {
		t.Fatalf("expected E_REVISION_CONFLICT for a stale full-spec plan, got %v", err)
	}
}

func mustPort(t *testing.T, raw string) int {
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	var p int
	for _, c := range u.Port() {
		p = p*10 + int(c-'0')
	}
	return p
}
