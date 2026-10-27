package ops

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// cutStore cuts the first download of one object after limit bytes (a dropped
// connection mid-prefetch) and records every Get offset.
type cutStore struct {
	media.Store
	mu      sync.Mutex
	cutKey  string
	limit   int64
	cut     bool
	offsets map[string][]int64
}

type cutReader struct {
	io.ReadCloser
	left int64
}

func (r *cutReader) Read(p []byte) (int, error) {
	if r.left <= 0 {
		return 0, errors.New("connection reset by peer")
	}
	if int64(len(p)) > r.left {
		p = p[:r.left]
	}
	n, err := r.ReadCloser.Read(p)
	r.left -= int64(n)
	return n, err
}

func (s *cutStore) Get(ctx context.Context, key string, off, n int64) (io.ReadCloser, media.ObjectInfo, error) {
	rc, info, err := s.Store.Get(ctx, key, off, n)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.offsets[key] = append(s.offsets[key], off)
	if err == nil && key == s.cutKey && !s.cut {
		s.cut = true
		return &cutReader{rc, s.limit}, info, nil
	}
	return rc, info, err
}

type fixture struct {
	a                     *app.App
	store                 *cutStore
	project, revision     string
	assetA, assetVFR      string // in the revision
	assetC                string // project only
	assetStaging          string
	renderID              string
	origA, mezzVFR, origV models.Variant
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: ids.New(ids.Workspace), Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "test-machine"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "tester"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")}}
	cfg.Dashboard.Port = 4859
	ctx := context.Background()
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	f := &fixture{a: a}
	put := func(kind string, size int) models.Variant {
		b := make([]byte, size)
		_, _ = rand.Read(b)
		p := filepath.Join(t.TempDir(), kind)
		_ = os.WriteFile(p, b, 0o644)
		sha, _ := media.HashFile(p)
		key := media.ObjectKey(a.WorkspaceID(), sha)
		if _, err := a.Media.PutFile(ctx, key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
			t.Fatal(err)
		}
		return models.Variant{Kind: kind, StorageKey: key, SHA256: sha, Size: int64(size), MIME: "application/octet-stream"}
	}
	now := time.Now().UTC()
	f.project = ids.New(ids.Project)
	if err := a.DB.Insert(ctx, store.CProjects, models.Project{ID: f.project, WorkspaceID: a.WorkspaceID(), Name: "Offline film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	asset := func(name string, orig models.Variant, vfr bool, state string, variants ...models.Variant) string {
		id := ids.New(ids.Asset)
		vs := map[string]models.Variant{}
		for _, v := range variants {
			vs[v.Kind] = v
		}
		as := models.Asset{ID: id, WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{f.project}, Kind: "video", Name: name, SHA256: orig.SHA256, Size: orig.Size,
			StorageKey: orig.StorageKey, State: state, Media: models.MediaInfo{HasVideo: true, VFR: vfr, Duration: 10}, Variants: vs, CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.origA = put("original", 3<<20+17)
	f.assetA = asset("interview.mov", f.origA, false, models.AssetAvailable, put(models.VariantProxy, 1<<20), put(models.VariantThumbnail, 2000), put(models.VariantPeaks, 900), put(models.VariantContactSheet, 5000))
	f.origV = put("original", 2<<20)
	f.mezzVFR = put(models.VariantMezzanine, 1<<20+5)
	f.assetVFR = asset("phone.mp4", f.origV, true, models.AssetAvailable, f.mezzVFR, put(models.VariantProxy, 300000))
	f.assetC = asset("broll.mp4", put("original", 400000), false, models.AssetAvailable, put(models.VariantProxy, 100000))
	f.assetStaging = asset("uploading.mov", models.Variant{StorageKey: media.ObjectKey(a.WorkspaceID(), strings.Repeat("ab", 32)), SHA256: strings.Repeat("ab", 32), Size: 999}, false, models.AssetStaging)
	f.revision = ids.New(ids.Revision)
	rev := models.Revision{ID: f.revision, WorkspaceID: a.WorkspaceID(), ProjectID: f.project, Branch: "main", Number: 1, CreatedAt: now,
		Spec: editspec.Spec{SchemaVersion: 1, Clips: []editspec.Clip{{ID: "a", AssetID: f.assetA, In: 0, Out: 2}, {ID: "b", AssetID: f.assetVFR, In: 1, Out: 3}, {ID: "c", AssetID: f.assetA, In: 4, Out: 5}}}}
	if err := a.DB.Insert(ctx, store.CRevisions, rev); err != nil {
		t.Fatal(err)
	}
	out, thumb := put("render", 700000), put("thumb", 3000)
	f.renderID = ids.New(ids.Render)
	if err := a.DB.Insert(ctx, store.CRenders, models.Render{ID: f.renderID, WorkspaceID: a.WorkspaceID(), ProjectID: f.project, RevisionID: f.revision, RevisionNumber: 1, Preset: "vertical-1080p",
		Quality: "review", State: models.RenderAvailable, Output: &models.RenderOutput{StorageKey: out.StorageKey, SHA256: out.SHA256, Size: out.Size, ThumbnailKey: thumb.StorageKey}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	f.store = &cutStore{Store: a.Media, offsets: map[string][]int64{}}
	a.Media = f.store
	return f
}

func prefetch(t *testing.T, a *app.App, in PrefetchIn) *PrefetchOut {
	t.Helper()
	out, err := CallTyped[PrefetchOut](context.Background(), a, "cache.prefetch", in, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPrefetchRevisionResumesAfterInterruption(t *testing.T) {
	f := newFixture(t)
	a := f.a
	f.store.cutKey, f.store.limit = f.origA.StorageKey, 1<<20

	first := prefetch(t, a, PrefetchIn{RevisionID: f.revision, Pin: true})
	// interview: thumbnail, peaks, proxy, original; phone (VFR): proxy, mezzanine; render + its thumbnail.
	if first.Objects != 8 || first.Failed != 1 || first.Downloaded != 7 || first.OfflineReady {
		t.Fatalf("first prefetch: %+v", first)
	}
	roles := map[string]string{}
	for _, it := range first.Items {
		roles[it.AssetID+"/"+it.Role] = it.Status
		if it.AssetID == f.assetVFR && it.Role == "original" {
			t.Fatal("a VFR source with a mezzanine must prefetch the mezzanine (what renders read), not the original")
		}
		if it.Role == models.VariantContactSheet {
			t.Fatal("contact sheets are not needed to render or review")
		}
	}
	if roles[f.assetA+"/original"] != "failed" || roles[f.assetVFR+"/mezzanine"] != "downloaded" {
		t.Fatalf("roles %v", roles)
	}
	if got := a.Cache.PartialSize(f.origA.SHA256); got != 1<<20 {
		t.Fatalf("partial download kept %d bytes, want 1 MiB", got)
	}
	if first.Pinned != 7 {
		t.Fatalf("pinned %d (only verified objects are pinned)", first.Pinned)
	}

	second := prefetch(t, a, PrefetchIn{RevisionID: f.revision, Pin: true})
	if second.Resumed != 1 || second.Failed != 0 || second.AlreadyCached != 7 || !second.OfflineReady || second.Pinned != 8 {
		t.Fatalf("second prefetch: %+v", second)
	}
	if offs := f.store.offsets[f.origA.StorageKey]; len(offs) != 2 || offs[1] != 1<<20 {
		t.Fatalf("original downloads started at %v; the retry must resume at 1 MiB", offs)
	}
	if ok, _ := a.Cache.Verify(f.origA.SHA256); !ok {
		t.Fatal("resumed original failed SHA-256 verification")
	}
	pins := a.Cache.Pinned()
	if !pins[f.origA.SHA256] || !pins[f.mezzVFR.SHA256] {
		t.Fatal("--pin must pin the render sources")
	}
	// The render worker picks the same object for every asset.
	var vfr models.Asset
	_ = a.DB.GetByID(context.Background(), store.CAssets, a.WorkspaceID(), f.assetVFR, &vfr)
	if _, _, sha, _ := vfr.RenderSource(); sha != f.mezzVFR.SHA256 {
		t.Fatal("RenderSource disagrees with prefetch")
	}

	third := prefetch(t, a, PrefetchIn{RevisionID: f.revision})
	if third.AlreadyCached != 8 || third.Downloaded+third.Resumed != 0 {
		t.Fatalf("third prefetch: %+v", third)
	}
	// Project scope adds the b-roll asset; the staging asset is skipped, not failed.
	dry := prefetch(t, a, PrefetchIn{ProjectID: f.project, DryRun: true})
	want := 0
	for _, it := range dry.Items {
		if it.Status == "would-download" {
			want++
			if it.AssetID != f.assetC {
				t.Fatalf("unexpected download in dry run: %+v", it)
			}
		}
		if it.AssetID == f.assetStaging && it.Status != "skipped" {
			t.Fatalf("staging asset: %+v", it)
		}
	}
	if want != 2 || dry.OfflineReady || dry.DryRun != true {
		t.Fatalf("dry run: %+v", dry)
	}
	if _, err := CallTyped[PrefetchOut](context.Background(), a, "cache.prefetch", PrefetchIn{}, Caller{}); err == nil {
		t.Fatal("scope is required")
	}
	if _, err := Call(context.Background(), a, "cache.prefetch", []byte(`{"projectId":"`+f.project+`"}`), Caller{Remote: true, Human: true}); err == nil {
		t.Fatal("prefetch is local-only")
	}
}

func TestSyncStatus(t *testing.T) {
	f := newFixture(t)
	a := f.a
	ctx := context.Background()
	_ = prefetch(t, a, PrefetchIn{RevisionID: f.revision})
	_ = a.Cache.Pin(f.origA.SHA256)
	now := time.Now().UTC()
	for _, s := range []models.IngestSession{
		{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), InstallationID: "inst_other", ProjectID: f.project, SourceName: "take9.mov", Size: 1000, BytesDone: 400, AssetID: f.assetStaging, State: "uploading", CreatedAt: now, UpdatedAt: now.Add(-10 * time.Minute)},
		{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), InstallationID: a.Cfg.Installation.ID, ProjectID: f.project, SourceName: "live.mov", Size: 1000, BytesDone: 10, State: "uploading", CreatedAt: now, UpdatedAt: now},
		{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), InstallationID: a.Cfg.Installation.ID, ProjectID: f.project, SourceName: "done.mov", State: "available", CreatedAt: now, UpdatedAt: now},
	} {
		if err := a.DB.Insert(ctx, store.CIngest, s); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeProxy, IdempotencyKey: "proxy:" + f.assetC, AssetID: f.assetC, Payload: map[string]any{"assetId": f.assetC}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeRender, IdempotencyKey: "render:x", ProjectID: f.project, Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if j, err := a.Queue.Lease(ctx, jobs.Worker{ID: a.Cfg.Installation.ID, Name: "test-machine", JobTypes: []string{jobs.TypeRender}, AcceptsHeavy: true}); err != nil || j == nil {
		t.Fatalf("lease: %v", err)
	}
	if err := a.DB.Insert(ctx, store.CWorkers, models.WorkerDevice{ID: a.Cfg.Installation.ID, WorkspaceID: a.WorkspaceID(), Name: "test-machine", LastHeartbeat: now}); err != nil {
		t.Fatal(err)
	}
	release := a.Cache.Acquire(f.mezzVFR.SHA256)
	defer release()

	out, err := CallTyped[SyncStatusOut](ctx, a, "sync.status", SyncStatusIn{ProjectID: f.project}, Caller{Remote: true}) // read-only: allowed over HTTP
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Assets) != 4 || len(out.Renders) != 1 {
		t.Fatalf("assets %d renders %d", len(out.Assets), len(out.Renders))
	}
	byID := map[string]SyncAsset{}
	for _, as := range out.Assets {
		byID[as.ID] = as
	}
	obj := func(asset, role string) SyncObject {
		for _, o := range byID[asset].Objects {
			if o.Role == role {
				return o
			}
		}
		t.Fatalf("no %s object for %s", role, asset)
		return SyncObject{}
	}
	if o := obj(f.assetA, "original"); !o.Cached || !o.Pinned || !o.Needed || o.Size != f.origA.Size {
		t.Fatalf("interview original: %+v", o)
	}
	if o := obj(f.assetA, models.VariantContactSheet); o.Cached || o.Needed {
		t.Fatalf("contact sheet is canonical-only and not needed: %+v", o)
	}
	if o := obj(f.assetVFR, models.VariantMezzanine); !o.Cached || !o.InUse || !o.Needed {
		t.Fatalf("mezzanine: %+v", o)
	}
	if o := obj(f.assetVFR, "original"); o.Cached || o.Needed {
		t.Fatalf("VFR original is not needed for renders: %+v", o)
	}
	if !byID[f.assetA].RenderReady || byID[f.assetC].RenderReady || byID[f.assetStaging].State != models.AssetStaging {
		t.Fatal("render readiness per asset")
	}
	if out.OfflineReady || out.Totals.NeededMissing != 2 || out.Totals.Cached != 8 || out.Totals.Pinned != 1 {
		t.Fatalf("totals %+v offlineReady=%v", out.Totals, out.OfflineReady)
	}
	status := map[string]string{}
	for _, s := range out.Ingest {
		status[s.SourceName] = s.Status
	}
	if len(out.Ingest) != 2 || status["take9.mov"] != "interrupted" || status["live.mov"] != "active" {
		t.Fatalf("ingest sessions %+v", out.Ingest)
	}
	if out.Jobs.Queued != 1 || out.Jobs.Running != 1 {
		t.Fatalf("jobs %+v", out.Jobs)
	}
	if !out.ThisWorkerOnline || len(out.Workers) != 1 || !out.Workers[0].ThisMachine {
		t.Fatalf("workers %+v", out.Workers)
	}
	if !out.Database.Reachable || out.Database.LatencyMs <= 0 || !out.MediaStore.Reachable || out.Daemon.Running {
		t.Fatalf("reachability db=%+v store=%+v daemon=%+v", out.Database, out.MediaStore, out.Daemon)
	}
	if out.Cache.InUse != 1 || out.Cache.Objects != 8 {
		t.Fatalf("cache stats %+v", out.Cache)
	}
	joined := strings.Join(out.Summary, "\n")
	for _, want := range []string{"8 of", "Not ready to work offline", "1 uploading, 1 interrupted", "1 waiting to start, 1 running", "this one is taking jobs", "isn't running in the background"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("summary lacks %q:\n%s", want, joined)
		}
	}

	// Without a database the report still describes this machine.
	oa, err := app.OpenOffline(a.Cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	off, err := SyncStatusOffline(ctx, oa, &SyncStatusIn{}, errors.New("cannot reach MongoDB: server selection timeout"))
	if err != nil {
		t.Fatal(err)
	}
	if off.Database.Reachable || !strings.Contains(off.Database.Error, "server selection") || !off.MediaStore.Reachable || off.Cache.Objects != 8 || len(off.Assets) != 0 {
		t.Fatalf("offline report %+v", off)
	}
	if !strings.HasPrefix(off.Summary[0], "MetaRush can't reach its database") {
		t.Fatalf("offline summary %v", off.Summary)
	}
}
