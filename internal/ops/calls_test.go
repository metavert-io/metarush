package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/calls"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// zoomClip stores a 30 s call recording whose speaker view switches between
// Lena and Maya (a split-second of Maya at 3.0–3.2), the names Zoom
// printed bottom left, and its switches found to the frame.
func zoomClip(t *testing.T, a *app.App, projectID string) models.Asset {
	t.Helper()
	now := time.Now().UTC()
	as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{projectID}, Kind: "video", Name: "video1763049187.mp4",
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, as)
	box := func(x, y, w, h float64) vision.Box { return vision.Box{x, y, w, h} }
	idx := &visual.Index{Version: visual.Version, Duration: 30, Step: 1, Source: "proxy",
		Text: []visual.TextSpan{
			{Text: "Lena O.", From: 0.5, To: 2.9, Box: box(0.01, 0.93, 0.1, 0.04)},
			{Text: "Lena O.", From: 3.5, To: 11.5, Box: box(0.01, 0.93, 0.1, 0.04)},
			{Text: "Maya Lindqvist", From: 12.5, To: 19.5, Box: box(0.01, 0.93, 0.14, 0.04)},
			{Text: "Lena O.", From: 20.5, To: 29.5, Box: box(0.01, 0.93, 0.1, 0.04)},
		}}
	insert(t, a, store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind,
		Provider: "apple-vision", Digest: "d", Data: visual.ToMap(idx), CreatedAt: now})
	st := &calls.Stored{Version: calls.Version, Source: "proxy", FPS: 30, Duration: 30, Switches: []calls.Switch{
		{At: 3.0, Last: 2.967, Score: 0.6, By: "scene"}, {At: 3.2, Last: 3.167, Score: 0.6, By: "scene"},
		{At: 12.0, Last: 11.967, Score: 0.6, By: "scene"}, {At: 20.0, Last: 19.967, Score: 0.6, By: "scene"}}}
	if _, err := calls.Save(context.Background(), a.DB, a.WorkspaceID(), as.ID, st, "test"); err != nil {
		t.Fatal(err)
	}
	return as
}

// The call recording's clean-up (D160–D162): the switches and who is on
// screen, the flash the edit check warns about, trimmed at the exact switch in
// one checked change, and the printed names covered by the built-in name bar
// (first names, as the project's rule says), replacing the name title.
func TestCallRecordingCleanUp(t *testing.T) {
	a := testapp.New(t)
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Awareness"})
	call[RuleAddOut](t, a, "project.rules.add", RuleAddIn{ProjectID: p.Project.ID, Text: "First names only on screen"})
	zoom := zoomClip(t, a, p.Project.ID)

	sw := call[FootageSwitchesOut](t, a, "footage.switches", FootageSwitchesIn{AssetID: zoom.ID})
	if sw.State != "ready" || len(sw.Switches) != 4 || len(sw.Shots) != 5 || sw.Names == nil || sw.Names.Corner != "bottom-left" {
		t.Fatalf("switches: %+v", sw)
	}
	if sw.Shots[2].Person != "Lena O." || sw.Shots[3].Person != "Maya Lindqvist" {
		t.Fatalf("who is on screen: %+v", sw.Shots)
	}

	plan := fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"answer","assetId":%q,"in":3.1,"out":11}],
		"overlays":[{"id":"who","template":"lower-third.clean","at":0.5,"duration":3,"params":{"name":"Lena O."}}]}}`, p.Project.ID, zoom.ID)
	chk := call[EditCheckOut](t, a, "edit.check", EditCheckIn{Plan: json.RawMessage(plan)})
	var warned []string
	for _, w := range chk.Warnings {
		warned = append(warned, w.Code+": "+w.Message)
	}
	if !strings.Contains(strings.Join(warned, "\n"), "edge_flash: ") || !strings.Contains(strings.Join(warned, "\n"), "printed_names: ") {
		t.Fatalf("the edit check should warn: %v", warned)
	}
	savePlan(t, a, plan)

	dry := call[CutFlashesOut](t, a, "cut.flashes", CutFlashesIn{CutEditCommon: CutEditCommon{ProjectID: p.Project.ID, DryRun: true}})
	if len(dry.Flashes) != 1 || dry.Check == nil || !dry.Check.Valid {
		t.Fatalf("flashes dry run: %+v", dry)
	}
	saved := call[CutFlashesOut](t, a, "cut.flashes", CutFlashesIn{CutEditCommon: CutEditCommon{ProjectID: p.Project.ID, ConfirmToken: dry.Check.ConfirmToken}})
	if !saved.Saved || saved.Applied == nil {
		t.Fatalf("flashes not saved: %+v", saved)
	}
	s := specOf(t, a, saved.Applied.Revision.ID)
	if s.Clips[0].In != 3.2 {
		t.Fatalf("the clip should start at the switch: %+v", s.Clips[0])
	}

	dryN := call[CutNamesOut](t, a, "cut.names", CutNamesIn{CutEditCommon: CutEditCommon{ProjectID: p.Project.ID, DryRun: true}})
	if dryN.NameBar == nil || len(dryN.NameBar.Removed) != 1 || dryN.Check == nil || !dryN.Check.Valid {
		t.Fatalf("names dry run: %+v", dryN)
	}
	savedN := call[CutNamesOut](t, a, "cut.names", CutNamesIn{CutEditCommon: CutEditCommon{ProjectID: p.Project.ID, ConfirmToken: dryN.Check.ConfirmToken}})
	s = specOf(t, a, savedN.Applied.Revision.ID)
	if len(s.Overlays) != 1 || s.Overlays[0].Template != editspec.TplNameBar || s.Overlays[0].Span == nil {
		t.Fatalf("the name bar: %+v", s.Overlays)
	}
	var names []string
	for _, c := range s.Overlays[0].Changes {
		var p editspec.NameBarParams
		_ = json.Unmarshal(c.Params, &p)
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "Lena" {
		t.Fatalf("first names from the printed names: %v", names)
	}
	// The cut sheet spells it, and reads back the same.
	sheet := call[CutShowOut](t, a, "cut.show", CutShowIn{ProjectID: p.Project.ID})
	if !strings.Contains(sheet.Sheet, "name.bar for the whole video") || !strings.Contains(sheet.Sheet, "Change: at ") {
		t.Fatalf("cut sheet:\n%s", sheet.Sheet)
	}
	chk2 := call[CutCheckOut](t, a, "cut.check", CutCheckIn{Sheet: sheet.Sheet})
	if !chk2.Valid || len(chk2.Changes) != 0 {
		t.Fatalf("the sheet should read back unchanged: %+v", chk2)
	}
}
