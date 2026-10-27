package ops

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A cut's captions take one of the vetted looks (D245): checked first with a
// confirm token, saved as a new version that the cut sheet shows; a new look
// brings its words per line; off and on; nothing to do says so; an unknown
// look is refused with the looks named; the looks are listed with the house
// style's.
func TestCutCaptionsLooks(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Garden"})
	now := time.Now().UTC()
	talk := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "talk.mov",
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 12, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, talk)
	r1 := savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"talk","assetId":%q,"in":0,"out":6}],"captions":{"enabled":true,"source":"manual","segments":[{"from":0,"to":2,"text":"Hello garden"}]}}}`,
		p.Project.ID, talk.ID))

	dry := call[CutCaptionsOut](t, a, "cut.captions", CutCaptionsIn{ProjectID: p.Project.ID, Look: "Big and bold", DryRun: true})
	if dry.Saved || dry.Check == nil || !dry.Check.Valid || dry.Check.ConfirmToken == "" || !strings.Contains(dry.Summary, "“Big and bold”") || !strings.Contains(dry.Summary, "were “Clean”") {
		t.Fatalf("dry run: %+v", dry)
	}
	// Outside the dashboard it needs the token.
	if again := call[CutCaptionsOut](t, a, "cut.captions", CutCaptionsIn{ProjectID: p.Project.ID, Look: "bold"}); again.Saved {
		t.Fatal("saved without a confirm token")
	}
	saved := call[CutCaptionsOut](t, a, "cut.captions", CutCaptionsIn{ProjectID: p.Project.ID, Look: "bold", ConfirmToken: dry.Check.ConfirmToken})
	if !saved.Saved || saved.Applied == nil || saved.Applied.Revision.ID == r1.Revision.ID {
		t.Fatalf("saved: %+v", saved)
	}
	s := specOf(t, a, saved.Applied.Revision.ID)
	if s.Captions.Style != editspec.CaptionBold || s.Captions.MaxWordsPerLine != 3 || !s.Captions.Enabled {
		t.Fatalf("bold, three words a line: %+v", s.Captions)
	}
	sheet := call[CutShowOut](t, a, "cut.show", CutShowIn{ProjectID: p.Project.ID})
	if !strings.Contains(sheet.Sheet, "style: bold") {
		t.Fatalf("the cut sheet says it:\n%s", sheet.Sheet)
	}
	// The same again is nothing to do.
	if same := call[CutCaptionsOut](t, a, "cut.captions", CutCaptionsIn{ProjectID: p.Project.ID, Look: "bold"}); !same.NoChange || !strings.Contains(same.Summary, "already") {
		t.Fatalf("same: %+v", same)
	}
	// Off, and a size and place turning them on again.
	off := call[CutCaptionsOut](t, a, "cut.captions", CutCaptionsIn{ProjectID: p.Project.ID, On: boolp(false), DryRun: true})
	if off.Now != "off" || off.Check == nil {
		t.Fatalf("off: %+v", off)
	}
	off = call[CutCaptionsOut](t, a, "cut.captions", CutCaptionsIn{ProjectID: p.Project.ID, On: boolp(false), ConfirmToken: off.Check.ConfirmToken})
	if s := specOf(t, a, off.Applied.Revision.ID); s.Captions.Enabled {
		t.Fatalf("captions off: %+v", s.Captions)
	}
	on := call[CutCaptionsOut](t, a, "cut.captions", CutCaptionsIn{ProjectID: p.Project.ID, Size: "large", Position: "top", DryRun: true})
	if !strings.Contains(on.Summary, "Captions on") || !strings.Contains(on.Now, "large, at the top") {
		t.Fatalf("on: %+v", on)
	}
	// Unknown looks and nothing asked are refused plainly.
	if _, err := CallTyped[CutCaptionsOut](ctx, a, "cut.captions", CutCaptionsIn{ProjectID: p.Project.ID, Look: "glitter"}, Caller{}); apperr.From(err).Code != apperr.InvalidInput ||
		!strings.Contains(apperr.From(err).Hint, "subtitle") {
		t.Fatalf("unknown look: %v", err)
	}
	if _, err := CallTyped[CutCaptionsOut](ctx, a, "cut.captions", CutCaptionsIn{ProjectID: p.Project.ID}, Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("nothing asked: %v", err)
	}
	looks := call[CaptionLooksOut](t, a, "captions.looks", CaptionLooksIn{ProjectID: p.Project.ID})
	if len(looks.Looks) != len(editspec.CaptionLooks) || looks.House != "" {
		t.Fatalf("looks: %+v", looks)
	}
}

func boolp(v bool) *bool { return &v }
