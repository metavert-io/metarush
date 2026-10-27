package wsarchive_test

import (
	"context"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// The level before Content Settings (migration 36) and the renamed fields
// (37): well before 0.14.0's.
const olderLevel = 35

// An archive from an older migration level: a database built the way an
// older MetaRush left it is exported without a single change to it, restored
// into an empty database, brought up to date by the migrations it lacked
// (the brand kit kept on the workspace then moves into Content Settings), and
// opens: MetaRush reads the workspace, the project and its brand.
func TestOlderLevelExportsRestoresAndMigratesForward(t *testing.T) {
	ctx := context.Background()
	src := emptyDB(t)
	migrateTo(t, src, olderLevel)
	st := fsStore(t)
	ws := ids.New(ids.Workspace)
	owner := ids.New(ids.User)
	prj := ids.New(ids.Project)
	now := time.Now().UTC().Truncate(time.Millisecond)
	for coll, doc := range map[string]bson.M{
		store.CWorkspaces: {"_id": ws, "workspaceId": ws, "name": "FernBrook Studio", "createdAt": now,
			"mediaStore": bson.M{"provider": "filesystem", "locator": st.Locator()},
			// Where the brand lived before Content Settings.
			"brandKit": bson.M{"primary": "#1F6FEB", "accent": "#F97316", "font": "Inter"}},
		store.CUsers:    {"_id": owner, "workspaceId": ws, "name": "Maya Lin", "email": "maya@example.com", "role": "owner", "createdAt": now},
		store.CProjects: {"_id": prj, "workspaceId": ws, "name": "Spring launch", "archived": false, "createdAt": now, "updatedAt": now},
	} {
		if _, err := src.C(coll).InsertOne(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	putObject(t, st, ws, fixture(t, "dog.jpg"))
	before := fingerprint(t, src)
	plan, err := wsarchive.MakePlan(ctx, wsarchive.ExportOptions{DB: src, Workspace: ws, Store: st, To: filepath.Join(t.TempDir(), "plan")})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Level.Older || plan.Level.Highest != olderLevel || plan.Media.Files != 1 || !strings.Contains(strings.Join(plan.Notes, " "), "older migration level") {
		t.Fatalf("plan: %+v", plan)
	}
	res := exportTo(t, src, st, ws, wsarchive.MediaReferenced, true)
	if after := fingerprint(t, src); after != before {
		t.Fatal("exporting an older database changed it")
	}
	if l := res.Manifest.Level; l.Highest != olderLevel || !l.Older || l.ThisMetaRush != store.SchemaVersion() {
		t.Fatalf("level: %+v", l)
	}
	if collection(res.Manifest, store.CContentSettings) != nil {
		t.Fatal("a collection the older database didn't have went in")
	}
	dst := emptyDB(t)
	rep, err := restoreInto(t, res.Path, dst, st, false)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if !rep.Verified || rep.FromLevel != olderLevel || rep.ToLevel != store.SchemaVersion() || len(rep.Migrated) != len(wsarchive.Missing(res.Manifest.Level)) {
		t.Fatalf("report: from %d to %d, migrated %v", rep.FromLevel, rep.ToLevel, rep.Migrated)
	}
	if !strings.HasPrefix(rep.Migrated[0], "36 ") {
		t.Fatalf("the first migration run forward: %v", rep.Migrated)
	}
	if pending, _ := dst.PendingMigrations(ctx); len(pending) != 0 {
		t.Fatalf("the copy isn't up to date: %v", pending)
	}
	// It opens.
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: ws, Name: "FernBrook Studio"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "mini"}
	cfg.User = config.UserRef{ID: owner, Name: "Maya Lin", Email: "maya@example.com"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: dst.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: st.Root}}
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	show, err := ops.CallTyped[ops.WorkspaceShowOut](ctx, a, "workspace.show", ops.WorkspaceShowIn{}, ops.Caller{})
	if err != nil || show.Workspace.Name != "FernBrook Studio" {
		t.Fatalf("workspace.show on the copy: %+v %v", show, err)
	}
	list, err := ops.CallTyped[ops.ProjectListOut](ctx, a, "project.list", ops.ProjectListIn{}, ops.Caller{})
	if err != nil || len(list.Projects) != 1 || list.Projects[0].Name != "Spring launch" {
		t.Fatalf("project.list on the copy: %+v %v", list, err)
	}
	brand, err := ops.CallTyped[ops.BrandShowOut](ctx, a, "brand.show", ops.BrandShowIn{ProjectID: prj}, ops.Caller{})
	if err != nil || !strings.EqualFold(brand.Kit.Primary, "#1F6FEB") {
		t.Fatalf("the brand moved forward into Content Settings: %+v %v", brand, err)
	}
}

// A big collection streams both ways: an export and a restore of far more
// documents than memory may hold at once keep the live heap small.
func TestBigCollectionsStream(t *testing.T) {
	if testing.Short() {
		t.Skip("writes about 100 MB of documents")
	}
	ctx := context.Background()
	src := testutil.DB(t)
	st := fsStore(t)
	ws := ids.New(ids.Workspace)
	if _, err := src.C(store.CWorkspaces).InsertOne(ctx, bson.M{"_id": ws, "workspaceId": ws, "name": "Big"}); err != nil {
		t.Fatal(err)
	}
	const n, perBatch = 80000, 2000
	text := strings.Repeat("words spoken in a long interview, ", 40) // ~1.4 KB
	for i := 0; i < n; i += perBatch {
		batch := make([]any, 0, perBatch)
		for j := i; j < i+perBatch; j++ {
			batch = append(batch, bson.M{"_id": ids.New(ids.Segment), "workspaceId": ws, "assetId": "ast_big", "idx": int32(j), "text": text})
		}
		if _, err := src.C(store.CSegments).InsertMany(ctx, batch); err != nil {
			t.Fatal(err)
		}
	}
	defer debug.SetGCPercent(debug.SetGCPercent(10))
	measure := func(f func()) uint64 {
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		base := ms.HeapAlloc
		var peak atomic.Uint64
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(10 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					var m runtime.MemStats
					runtime.ReadMemStats(&m)
					if m.HeapAlloc > base && m.HeapAlloc-base > peak.Load() {
						peak.Store(m.HeapAlloc - base)
					}
				}
			}
		}()
		f()
		close(stop)
		wg.Wait()
		return peak.Load()
	}
	var res *wsarchive.Result
	grew := measure(func() { res = exportTo(t, src, st, ws, wsarchive.MediaReferenced, false) })
	c := collection(res.Manifest, store.CSegments)
	if c == nil || c.Count != n || c.Bytes < 100<<20 {
		t.Fatalf("segments: %+v", c)
	}
	const limit = 48 << 20
	if grew > limit {
		t.Fatalf("exporting %s grew the heap by %s: it doesn't stream", wsarchive.HumanBytes(c.Bytes), wsarchive.HumanBytes(int64(grew)))
	}
	t.Logf("export: %s written, heap grew at most %s", wsarchive.HumanBytes(c.Bytes), wsarchive.HumanBytes(int64(grew)))
	dst := emptyDB(t)
	var rep *wsarchive.RestoreReport
	var err error
	grew = measure(func() { rep, err = restoreInto(t, res.Path, dst, st, false) })
	if err != nil || !rep.Verified {
		t.Fatalf("restore: %v", err)
	}
	if grew > limit {
		t.Fatalf("restoring grew the heap by %s: it doesn't stream", wsarchive.HumanBytes(int64(grew)))
	}
	t.Logf("restore: heap grew at most %s", wsarchive.HumanBytes(int64(grew)))
}
