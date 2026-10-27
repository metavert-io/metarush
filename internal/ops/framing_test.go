package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// savePlan checks a plan and saves it as a person would.
func savePlan(t *testing.T, a *app.App, plan string) *EditApplyOut {
	t.Helper()
	chk := call[EditCheckOut](t, a, "edit.check", EditCheckIn{Plan: json.RawMessage(plan)})
	if !chk.Valid {
		t.Fatalf("plan not valid: %+v", chk.Errors)
	}
	out, err := CallTyped[EditApplyOut](context.Background(), a, "edit.apply", EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken}, Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func specOf(t *testing.T, a *app.App, revID string) editspec.Spec {
	t.Helper()
	return call[RevisionShowOut](t, a, "revision.show", RevisionShowIn{ID: revID}).Spec
}

func framingOf(t *testing.T, a *app.App, revID string) (editspec.PlanFraming, string) {
	t.Helper()
	p := call[editspec.RenderPlan](t, a, "revision.plan", RevisionPlanIn{ID: revID})
	b, _ := json.Marshal(p.Clips)
	return p.Clips[0].Framing, string(b)
}

// indexFace stores an on-screen index for the clip: one face, right of center.
func indexFace(t *testing.T, a *app.App, assetID string) {
	t.Helper()
	var samples []visual.Sample
	for i := 0; i < 12; i++ {
		f := vision.Frame{W: 1920, H: 1080, Faces: []vision.Face{{Conf: 1, Box: vision.Box{0.7, 0.2, 0.1, 0.3}}}}
		samples = append(samples, visual.Sample{At: float64(i) + 0.5, Frame: f})
	}
	idx := visual.Build(samples, 1, 12, nil, "proxy")
	insert(t, a, store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: assetID, Kind: visual.InsightKind,
		Provider: "apple-vision", Digest: "d", Data: visual.ToMap(idx), CreatedAt: time.Now().UTC()})
}

// Framing is what the cut says (D73): a new cut is written with faces kept in
// frame and its cut sheet shows it; a cut that does not say so keeps exactly its
// framing when its footage is indexed later; opting it in is an ordinary edit
// (an edit plan or a cut sheet line) that makes a new version.
func TestFramingIsWhatTheCutSays(t *testing.T) {
	a := testapp.New(t)
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Garden"})
	now := time.Now().UTC()
	talk := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "talk.mov",
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 12, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, talk)

	// 1. A new cut says faces, and its cut sheet shows it.
	r1 := savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"talk","assetId":%q,"in":0,"out":6}]}}`,
		p.Project.ID, talk.ID))
	if s := specOf(t, a, r1.Revision.ID); s.Output.Framing != editspec.FramingFaces {
		t.Fatalf("a new cut is written with faces: %+v", s.Output)
	}
	sheet := call[CutShowOut](t, a, "cut.show", CutShowIn{ProjectID: p.Project.ID})
	if !strings.Contains(sheet.Sheet, "\n  framing: faces") {
		t.Fatalf("the cut sheet says it:\n%s", sheet.Sheet)
	}

	// 2. A cut that doesn't say (as cuts were made before): the same plan before
	// and after its footage is indexed.
	r2 := savePlan(t, a, fmt.Sprintf(`{"baseRevisionId":%q,"ops":[{"op":"setOutput","output":{"preset":"vertical-1080p","width":1080,"height":1920,"fps":30}}]}`, r1.Revision.ID))
	if s := specOf(t, a, r2.Revision.ID); s.Output.Framing != "" {
		t.Fatalf("this version says nothing about framing: %+v", s.Output)
	}
	_, before := framingOf(t, a, r2.Revision.ID)
	indexFace(t, a, talk.ID)
	fr, after := framingOf(t, a, r2.Revision.ID)
	if before != after || fr.From != "" || fr.FocusX != 0.5 {
		t.Fatalf("indexing changed a cut that doesn't ask for faces:\n before %s\n after  %s", before, after)
	}
	// The faces-cut follows the face now that there is one.
	if fr, _ := framingOf(t, a, r1.Revision.ID); fr.From != editspec.FramingFaces || fr.FocusX < 0.7 {
		t.Fatalf("the new cut keeps the face in frame: %+v", fr)
	}

	// 3. Opting in is an edit: a line in the cut sheet, checked and saved.
	sheet = call[CutShowOut](t, a, "cut.show", CutShowIn{ProjectID: p.Project.ID})
	if strings.Contains(sheet.Sheet, "framing:") {
		t.Fatalf("r2 says nothing about framing:\n%s", sheet.Sheet)
	}
	edited := strings.Replace(sheet.Sheet, "\n  frame rate: 30", "\n  frame rate: 30\n  framing: focus on faces", 1)
	if edited == sheet.Sheet {
		t.Fatalf("no frame rate line to edit after:\n%s", sheet.Sheet)
	}
	chk := call[CutCheckOut](t, a, "cut.check", CutCheckIn{Sheet: edited})
	if !chk.Valid || chk.NoChanges || len(chk.Changes) == 0 {
		t.Fatalf("the edit is a change: %+v", chk)
	}
	r3, err := CallTyped[CutApplyOut](context.Background(), a, "cut.apply", CutApplyIn{Sheet: edited, ConfirmToken: chk.ConfirmToken}, Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if s := specOf(t, a, r3.Revision.ID); s.Output.Framing != editspec.FramingFaces || r3.Revision.ID == r2.Revision.ID {
		t.Fatalf("a new version that says faces: %+v", s.Output)
	}
	if fr, _ := framingOf(t, a, r3.Revision.ID); fr.From != editspec.FramingFaces {
		t.Fatalf("the opted-in version follows the face: %+v", fr)
	}
	if _, again := framingOf(t, a, r2.Revision.ID); again != before {
		t.Fatal("the older version's plan changed")
	}
}
