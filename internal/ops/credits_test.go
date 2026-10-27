package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Outside footage keeps its source everywhere it is described: the clip
// summary, the cut sheet's footage list, footage.md, job details and the
// delivery sidecar's credits.
func TestOutsideFootageIsCredited(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Reaction video"})
	now := time.Now().UTC()
	src := &models.AssetSource{Platform: "youtube", URL: "https://www.youtube.com/watch?v=aqz-KE-bpKQ", VideoID: "aqz-KE-bpKQ", Uploader: "Blender", Handle: "@BlenderOfficial",
		UploadDate: "2014-11-10", Title: "Big Buck Bunny 60fps 4K", License: "Creative Commons Attribution license (reuse allowed)", DownloadedAt: now, Tool: "yt-dlp", ToolVersion: "2026.08.19", Method: models.SourceDownload}
	theirs := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "Big Buck Bunny 60fps 4K.mp4",
		Title: "Big Buck Bunny 60fps 4K", State: models.AssetAvailable, Media: models.MediaInfo{Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, Source: src, CreatedAt: now, UpdatedAt: now})
	mine := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "my-reaction.mov",
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, theirs)
	insert(t, a, store.CAssets, mine)

	list := call[AssetListOut](t, a, "asset.list", AssetListIn{ProjectID: p.Project.ID})
	for _, s := range list.Assets {
		if s.ID == theirs.ID && (s.Source == nil || s.Source.Line() != "From YouTube · @BlenderOfficial · 10 Nov 2014") {
			t.Fatalf("clip summary carries the source: %+v", s.Source)
		}
		if s.ID == mine.ID && s.Source != nil {
			t.Fatal("the person's own footage has no outside source")
		}
	}

	plan := fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"me","assetId":%q,"in":0,"out":4},{"id":"bunny","assetId":%q,"in":8,"out":12}]}}`,
		p.Project.ID, mine.ID, theirs.ID)
	chkPlan := call[EditCheckOut](t, a, "edit.check", EditCheckIn{Plan: json.RawMessage(plan)})
	rev, err := CallTyped[EditApplyOut](ctx, a, "edit.apply", EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chkPlan.ConfirmToken}, Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	sheet := call[CutShowOut](t, a, "cut.show", CutShowIn{ProjectID: p.Project.ID})
	var line string
	for _, l := range strings.Split(sheet.Sheet, "\n") {
		if strings.Contains(l, theirs.ID) {
			line = l
		}
	}
	if !strings.Contains(line, "from @BlenderOfficial on YouTube") {
		t.Fatalf("the cut sheet's footage comment names the source: %q", line)
	}
	// The comment is derived: the sheet still reads back as the same cut.
	chk := call[CutCheckOut](t, a, "cut.check", CutCheckIn{Sheet: sheet.Sheet})
	if !chk.Valid || !chk.NoChanges {
		t.Fatalf("round trip: %+v", chk.Problems)
	}

	info := newSheetInfo(ctx, a)
	var proj models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), p.Project.ID, &proj)
	md, err := footageReference(ctx, a, info, &proj, []cutsheet.FootageRef{{Name: "bunny", AssetID: theirs.ID}})
	if err != nil || !strings.Contains(md, "From YouTube · @BlenderOfficial · 10 Nov 2014 · https://www.youtube.com/watch?v=aqz-KE-bpKQ") || !strings.Contains(md, "License stated: Creative Commons") {
		t.Fatalf("footage.md names the source: %v\n%s", err, md)
	}

	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rev.Revision.ID, &r)
	assets, _ := deliveryAssets(ctx, a, &r.Spec)
	it := &deliveryItem{rev: r, proj: proj, render: models.Render{ID: "rnd_x", Output: &models.RenderOutput{}}, assets: assets}
	sc := buildSidecar(a, it, "cut.mp4")
	if len(sc.Credits) != 1 || sc.Credits[0].Credit != "Includes footage from @BlenderOfficial on YouTube: https://www.youtube.com/watch?v=aqz-KE-bpKQ" ||
		sc.Credits[0].License == "" || sc.Credits[0].AssetID != theirs.ID {
		t.Fatalf("sidecar credits: %+v", sc.Credits)
	}
	for _, w := range sc.Warnings {
		if strings.Contains(strings.ToLower(w), "footage") {
			t.Fatalf("outside footage is informational, never a warning: %q", w)
		}
	}
}
