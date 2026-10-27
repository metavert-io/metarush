package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func insert(t *testing.T, a *app.App, coll string, doc any) {
	t.Helper()
	if err := a.DB.Insert(context.Background(), coll, doc); err != nil {
		t.Fatal(err)
	}
}

// fill gives a test asset what ingest always sets: a unique content hash and a
// (possibly empty) project list.
func fill(as models.Asset) models.Asset {
	if as.SHA256 == "" {
		as.SHA256 = strings.Repeat("0", 16) + strings.ToLower(strings.TrimPrefix(as.ID, "ast_"))
	}
	if as.ProjectIDs == nil {
		as.ProjectIDs = []string{}
	}
	return as
}

func call[T any](t *testing.T, a *app.App, op string, in any) *T {
	t.Helper()
	out, err := CallTyped[T](context.Background(), a, op, in, Caller{})
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return out
}

func TestMatchRanks(t *testing.T) {
	cases := []struct {
		text, query, want string
	}{
		{"Then my neighbor asked me a simple question.", "simple question", RankExact},
		{"A question, simple as that.", "simple question", RankAll},
		{"The bunny hopped", "bunny", RankAll},
		{"Bunnies everywhere", "bunny", RankForms},
		{"A quiet neighborhood", "neighb", RankPartial},
		{"Nothing to see", "bunny", ""},
	}
	for _, c := range cases {
		words := queryWords(c.query)
		phrase := ""
		if len(words) > 1 {
			phrase = strings.Join(words, " ")
		}
		got, spans := match(c.text, words, phrase)
		if got != c.want {
			t.Errorf("match(%q, %q) = %q, want %q", c.text, c.query, got, c.want)
		}
		if got != "" && len(spans) == 0 {
			t.Errorf("match(%q, %q): no highlight spans", c.text, c.query)
		}
	}
	// The snippet marks exactly the matched word.
	r, spans := match("Then my neighbor leaned over", []string{"neighb"}, "")
	parts := snippet("Then my neighbor leaned over", spans, 180)
	var hits []string
	for _, p := range parts {
		if p.Hit {
			hits = append(hits, p.Text)
		}
	}
	if r != RankPartial || strings.Join(hits, "|") != "neighb" {
		t.Fatalf("snippet hits %v (rank %s)", hits, r)
	}
}

func TestSearchGroupsRanksAndTimecodes(t *testing.T) {
	a := testapp.New(t)
	ws := a.WorkspaceID()
	now := time.Now().UTC()
	proj := models.Project{ID: ids.New(ids.Project), WorkspaceID: ws, Name: "Backyard film", Brief: models.Brief{Goal: "A short about our bunny"},
		Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CProjects, proj)
	clip := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: ws, Kind: "video", Name: "IMG_2391.MOV", Title: "Garden interview", ProjectIDs: []string{proj.ID},
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 60}, CreatedAt: now, UpdatedAt: now}
	alias := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: ws, Kind: "video", Name: "IMG_0007.MOV", Aliases: []models.AssetAlias{{Name: "bunny take 2.mov", AddedAt: now}},
		State: models.AssetAvailable, CreatedAt: now, UpdatedAt: now}
	titled := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: ws, Kind: "video", Name: "C0001.MP4", Title: "Bunny close-up", State: models.AssetAvailable, CreatedAt: now, UpdatedAt: now}
	for _, as := range []models.Asset{clip, alias, titled} {
		insert(t, a, store.CAssets, fill(as))
	}
	seg := func(i int, from float64, text string, words ...models.Word) {
		insert(t, a, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: ws, AssetID: clip.ID, Index: i, From: from, To: from + 4, Text: text, Words: words, CreatedAt: now})
	}
	seg(0, 10, "Then my neighbor said the bunny was gone.", models.Word{Text: "Then", From: 10}, models.Word{Text: "my", From: 10.3}, models.Word{Text: "neighbor", From: 10.5},
		models.Word{Text: "said", From: 11}, models.Word{Text: "the", From: 11.3}, models.Word{Text: "bunny", From: 11.6}, models.Word{Text: "was", From: 12}, models.Word{Text: "gone.", From: 12.2})
	seg(1, 20, "Bunnies were everywhere in the garden.")
	seg(2, 30, "Nothing about rabbits here.")
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: ws, ProjectID: proj.ID, Branch: "main", Number: 2, Instruction: "Open on the bunny shot",
		Spec: editspec.Spec{SchemaVersion: 1, Title: "Second cut"}, CreatedAt: now}
	insert(t, a, store.CRevisions, rev)
	insert(t, a, store.CComments, models.Comment{ID: ids.New(ids.Comment), WorkspaceID: ws, ProjectID: proj.ID, Body: "More bunny, please", CreatedAt: now})

	out := call[SearchOut](t, a, "search", SearchIn{Query: "bunny"})
	if len(out.Moments) != 2 {
		t.Fatalf("moments %+v", out.Moments)
	}
	first := out.Moments[0]
	if first.Rank != RankAll || first.AssetTitle != "Garden interview" || first.ProjectName != "Backyard film" {
		t.Fatalf("best moment %+v", first)
	}
	if first.At != 11.6 || first.Timecode == "" || strings.Join(first.Terms, ",") != "bunny" {
		t.Fatalf("the moment should start at the word itself: at %v timecode %q terms %v", first.At, first.Timecode, first.Terms)
	}
	if out.Moments[1].Rank != RankForms || out.Moments[1].From != 20 {
		t.Fatalf("word forms (bunnies) rank below the exact word: %+v", out.Moments[1])
	}
	videos := map[string]SearchAsset{}
	for _, v := range out.Videos {
		videos[v.AssetID] = v
	}
	if v := videos[titled.ID]; v.MatchedIn != "title" || v.Title != "Bunny close-up" {
		t.Fatalf("title match %+v", v)
	}
	if v := videos[alias.ID]; v.MatchedIn != "alias" || v.Title != "IMG_0007" {
		t.Fatalf("alias match %+v (untitled assets show their filename without the extension)", v)
	}
	if len(out.Projects) != 1 || out.Projects[0].MatchedIn != "goal" || len(out.Revisions) != 1 || out.Revisions[0].Number != 2 || len(out.Comments) != 1 {
		t.Fatalf("projects %+v revisions %+v comments %+v", out.Projects, out.Revisions, out.Comments)
	}
	if out.Total != len(out.Moments)+len(out.Videos)+len(out.Projects)+len(out.Revisions)+len(out.Comments) {
		t.Fatalf("total %d", out.Total)
	}

	// Partial words find the start of a word (and highlight only that part).
	part := call[SearchOut](t, a, "search", SearchIn{Query: "neighb"})
	if len(part.Moments) != 1 || part.Moments[0].Rank != RankPartial || part.Moments[0].At != 10.5 {
		t.Fatalf("partial %+v", part.Moments)
	}
	// Scoped to a project: the inbox clips' titles are left out.
	scoped := call[SearchOut](t, a, "search", SearchIn{Query: "bunny", ProjectID: proj.ID})
	if len(scoped.Videos) != 0 || len(scoped.Moments) != 2 || len(scoped.Projects) != 0 {
		t.Fatalf("scoped videos %+v moments %d projects %d", scoped.Videos, len(scoped.Moments), len(scoped.Projects))
	}
}

func TestTitlesDefaultRenameAndAudit(t *testing.T) {
	a := testapp.New(t)
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "IMG_2391.MOV", State: models.AssetAvailable, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CAssets, fill(as))
	list := call[AssetListOut](t, a, "asset.list", AssetListIn{Unassigned: true})
	if len(list.Assets) != 1 || list.Assets[0].Title != "IMG_2391" || list.Assets[0].Name != "IMG_2391.MOV" {
		t.Fatalf("default title: %+v", list.Assets)
	}
	if _, err := CallTyped[AssetLinkOut](context.Background(), a, "asset.update", AssetUpdateIn{ID: as.ID, Title: "   "}, Caller{}); err == nil {
		t.Fatal("blank title accepted")
	}
	up := call[AssetLinkOut](t, a, "asset.update", AssetUpdateIn{ID: as.ID, Title: "  Garage   interview "})
	if up.Asset.Title != "Garage interview" || up.Asset.Name != "IMG_2391.MOV" {
		t.Fatalf("renamed %+v", up.Asset)
	}
	audit := call[AuditListOut](t, a, "audit.list", AuditListIn{TargetID: as.ID})
	if len(audit.Events) != 1 || audit.Events[0].Action != "asset.update" || audit.Events[0].Details["previous"] != "IMG_2391" {
		t.Fatalf("audit %+v", audit.Events)
	}
}

func TestDuplicateKeepRecordsAliasSkipNotifies(t *testing.T) {
	a := testapp.New(t)
	now := time.Now().UTC()
	proj := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Trip", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CProjects, proj)
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "beach.mov", Title: "Beach", State: models.AssetAvailable, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CAssets, fill(as))

	kept := call[AssetDuplicateOut](t, a, "asset.duplicate", AssetDuplicateIn{AssetID: as.ID, Name: "beach copy.mov", Action: "keep", ProjectID: proj.ID})
	if kept.Duplicate.Action != models.DuplicateKeptBoth || kept.Duplicate.Title != "Beach" {
		t.Fatalf("keep: %+v", kept.Duplicate)
	}
	var got models.Asset
	if err := a.DB.GetByID(context.Background(), store.CAssets, a.WorkspaceID(), as.ID, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Aliases) != 1 || got.Aliases[0].Name != "beach copy.mov" || len(got.ProjectIDs) != 1 || got.ProjectIDs[0] != proj.ID || !got.HasName("beach copy.mov") {
		t.Fatalf("alias not recorded or not linked: %+v", got)
	}
	// Keeping the same name twice does not add it twice.
	call[AssetDuplicateOut](t, a, "asset.duplicate", AssetDuplicateIn{AssetID: as.ID, Name: "beach copy.mov", Action: "keep"})
	_ = a.DB.GetByID(context.Background(), store.CAssets, a.WorkspaceID(), as.ID, &got)
	if len(got.Aliases) != 1 {
		t.Fatalf("aliases %+v", got.Aliases)
	}
	var count int64
	if n, err := a.DB.C(store.CAssets).CountDocuments(context.Background(), bson.M{"workspaceId": a.WorkspaceID()}); err == nil {
		count = n
	}
	if count != 1 {
		t.Fatalf("%d assets; keeping both must not store a second copy", count)
	}

	skipped := call[AssetDuplicateOut](t, a, "asset.duplicate", AssetDuplicateIn{AssetID: as.ID, Name: "beach (1).mov", Action: "skip"})
	if skipped.Duplicate.Action != models.DuplicateSkipped {
		t.Fatalf("skip: %+v", skipped.Duplicate)
	}
	var ns []models.Notification
	_ = a.DB.Find(context.Background(), store.CNotifications, a.WorkspaceID(), bson.M{"kind": models.NotifyDuplicateSkipped}, &ns)
	if len(ns) != 1 || !strings.Contains(ns[0].Title, "beach (1).mov") {
		t.Fatalf("duplicate notification %+v", ns)
	}
}

func TestJobShowRelatedAndTarget(t *testing.T) {
	f := newFixture(t)
	a := f.a
	now := time.Now().UTC()
	fin := now.Add(-time.Minute)
	job := func(j models.Job) models.Job {
		j.ID, j.WorkspaceID = ids.New(ids.Job), a.WorkspaceID()
		j.Payload, j.Attempts, j.IdempotencyKey, j.MaxAttempts = map[string]any{}, []models.Attempt{}, ids.New("k"), 3
		j.CreatedAt, j.UpdatedAt, j.NextRunAt = now.Add(-2*time.Minute), now, now.Add(-2*time.Minute)
		j.CreatedBy = models.Actor{UserID: a.Cfg.User.ID, Via: "dashboard"}
		insert(t, a, store.CJobs, j)
		return j
	}
	render := job(models.Job{Type: jobs.TypeRender, State: models.JobSucceeded, ProjectID: f.project, RenderID: f.renderID, FinishedAt: &fin,
		Payload: map[string]any{"renderId": f.renderID}})
	out, err := JobDetail(context.Background(), a, render.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Related.Render == nil || out.Related.Render.ID != f.renderID || out.Related.Project == nil || out.Related.Project.Name != "Offline film" {
		t.Fatalf("related %+v", out.Related)
	}
	if out.Target == nil || out.Target.Label != "Go to the render" || !strings.Contains(out.Target.Link, f.renderID) {
		t.Fatalf("target %+v", out.Target)
	}
	if !strings.Contains(out.Title, "render of Offline film, version 1") || out.StartedBy == "" || out.CanRetry {
		t.Fatalf("title %q startedBy %q canRetry %v", out.Title, out.StartedBy, out.CanRetry)
	}

	failed := job(models.Job{Type: jobs.TypeTranscribe, State: models.JobFailed, AssetID: f.assetA, Heavy: true, FinishedAt: &fin,
		Error: "sherpa-onnx-offline failed: exit status 1: model file not found"})
	out, err = JobDetail(context.Background(), a, failed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Related.Asset == nil || out.Related.Asset.ID != f.assetA || out.Target == nil || out.Target.Label != "Go to the clip" {
		t.Fatalf("clip job related %+v target %+v", out.Related, out.Target)
	}
	if !out.CanRetry || !strings.Contains(out.Advice, "missing a file") || strings.Contains(out.Advice, "metarush") {
		t.Fatalf("failed job: canRetry %v advice %q", out.CanRetry, out.Advice)
	}
}

func TestPreviewPrepareMakesSourcesLocal(t *testing.T) {
	f := newFixture(t)
	out := call[PreviewPrepareOut](t, f.a, "preview.prepare", PreviewPrepareIn{RevisionID: f.revision, Wait: true})
	if out.Total == 0 || out.Ready != out.Total {
		t.Fatalf("prepare: %d of %d ready: %+v", out.Ready, out.Total, out.Sources)
	}
	roles := map[string]string{}
	for _, s := range out.Sources {
		if !s.Local || s.Error != "" {
			t.Fatalf("source not local: %+v", s)
		}
		roles[s.AssetID] = s.Role
	}
	// The preview plays proxies (small, same-origin), never originals when a proxy exists.
	if roles[f.assetA] != "proxy" || roles[f.assetVFR] != "proxy" {
		t.Fatalf("roles %v", roles)
	}
	again := call[PreviewPrepareOut](t, f.a, "preview.prepare", PreviewPrepareIn{RevisionID: f.revision})
	if again.Ready != again.Total {
		t.Fatalf("second call should find everything local: %+v", again)
	}
}
