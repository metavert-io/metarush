package mattes_test

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/mattes"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// saveCut saves a first cut of the clip (0.4 to 1.6 s) and returns its version.
func (e *env) saveCut(t *testing.T) string {
	t.Helper()
	spec := editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Width: 640, Height: 360, FPS: 25}, Clips: []editspec.Clip{{ID: "host", AssetID: e.clip.ID, In: 0.4, Out: 1.6}}}
	plan, _ := json.Marshal(editspec.Plan{ProjectID: e.project, Instruction: "a first cut", Spec: &spec})
	chk, err := ops.CallTyped[ops.EditCheckOut](context.Background(), e.a, "edit.check", ops.EditCheckIn{Plan: plan}, ops.Caller{})
	if err != nil || !chk.Valid {
		t.Fatalf("the first cut: %+v %v", chk, err)
	}
	out, err := ops.CallTyped[ops.EditApplyOut](context.Background(), e.a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	return out.Revision.ID
}

func (e *env) background(t *testing.T, in mattes.CutBackgroundIn) (*mattes.CutBackgroundOut, error) {
	t.Helper()
	in.ProjectID = e.project
	if in.Clip == "" {
		in.Clip = "host"
	}
	return ops.CallTyped[mattes.CutBackgroundOut](context.Background(), e.a, "cut.background", in, ops.Caller{})
}

func (e *env) head(t *testing.T) *models.Revision {
	t.Helper()
	var p models.Project
	if err := e.a.DB.GetByID(context.Background(), store.CProjects, e.a.WorkspaceID(), e.project, &p); err != nil {
		t.Fatal(err)
	}
	var r models.Revision
	if err := e.a.DB.GetByID(context.Background(), store.CRevisions, e.a.WorkspaceID(), p.Branches["main"].Head, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

// Replacing a clip's background makes its cut-out first (a little longer
// than the clip), then — asked again — pins it in a checked change with what
// goes behind; the preview's plan plays the cut-out from the same moment as
// the clip; a new kind of background reuses it; off takes it out (D510).
func TestReplacingAClipsBackground(t *testing.T) {
	e := setup(t)
	e.saveCut(t)
	ctx := context.Background()
	dry, err := e.background(t, mattes.CutBackgroundIn{CutEditCommon: ops.CutEditCommon{DryRun: true}, With: mattes.WithColour, Colour: "#00B140"})
	if err != nil || dry.Waiting || dry.Cutout == nil || !dry.Cutout.DryRun || dry.Check != nil || !strings.HasPrefix(dry.Summary, "First the people in") {
		t.Fatalf("a dry run before the cut-out: %+v %v", dry, err)
	}
	asked, err := e.background(t, mattes.CutBackgroundIn{With: mattes.WithColour, Colour: "#00B140"})
	if err != nil || !asked.Waiting || asked.Cutout == nil || asked.Cutout.JobID == "" || asked.Saved {
		t.Fatalf("asked: %+v %v", asked, err)
	}
	if v := asked.Cutout.Matte; v.From != 0 || v.To != 2 {
		t.Fatalf("the part it covers: %v to %v", v.From, v.To)
	}
	if _, err := e.run(t, asked.Cutout.JobID); err != nil {
		t.Fatal(err)
	}
	chk, err := e.background(t, mattes.CutBackgroundIn{With: mattes.WithColour, Colour: "#00B140"})
	if err != nil || chk.Waiting || chk.Check == nil || !chk.Check.Valid || chk.Check.ConfirmToken == "" || chk.Saved || chk.Now != "the colour #00b140" || chk.Was != "its own background" {
		t.Fatalf("checked: %+v %v", chk, err)
	}
	saved, err := e.background(t, mattes.CutBackgroundIn{CutEditCommon: ops.CutEditCommon{ConfirmToken: chk.Check.ConfirmToken}, With: mattes.WithColour, Colour: "#00B140"})
	if err != nil || !saved.Saved || saved.Applied == nil {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	head := e.head(t)
	b := head.Spec.Clips[0].Background
	m, _ := mattes.BySHA(ctx, e.a, b.Matte)
	if b == nil || b.With != editspec.BackgroundColour || b.Colour != "#00b140" || m == nil || m.ID != asked.Cutout.Matte.ID {
		t.Fatalf("the version: %+v %+v", b, m)
	}
	// The preview plays the cut-out from the clip's own moment (0.4 s in, its first frame at 0).
	plan, err := ops.RevisionPlan(ctx, e.a, head)
	if err != nil {
		t.Fatal(err)
	}
	pb := plan.Clips[0].Background
	if pb == nil || pb.CutOut != b.Matte || pb.CutOutTrimBefore != float64(plan.Clips[0].TrimBefore) || plan.CutOutSources[b.Matte] != ops.CutOutURL(b.Matte) {
		t.Fatalf("the preview's plan: %+v %v", pb, plan.CutOutSources)
	}
	// A blur instead: the same cut-out, no new one.
	blur, err := e.background(t, mattes.CutBackgroundIn{CutEditCommon: ops.CutEditCommon{DryRun: true}, With: mattes.WithBlur, Blur: 40})
	if err != nil || blur.Waiting || blur.Check == nil || !blur.Check.Valid || blur.Now != "a blur of the clip itself" {
		t.Fatalf("a blur: %+v %v", blur, err)
	}
	// Another clip behind them, from 0:01.
	other := e.footageOf(t, "beach", 3)
	behind, err := e.background(t, mattes.CutBackgroundIn{CutEditCommon: ops.CutEditCommon{DryRun: true}, With: mattes.WithFootage, Footage: other.ID, FootageAt: 0.5})
	if err != nil || behind.Check == nil || !behind.Check.Valid || behind.Now != "“"+other.DisplayTitle()+"” from 0:00.5" || !strings.HasPrefix(behind.Summary, "Puts “") {
		t.Fatalf("another clip: %+v %v", behind, err)
	}
	// A picture from the resources library: pinned with the change.
	pic := filepath.Join(t.TempDir(), "sunset.png")
	img := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	img.Set(1, 1, color.RGBA{255, 0, 0, 255})
	fh, _ := os.Create(pic)
	_ = png.Encode(fh, img)
	_ = fh.Close()
	if _, err := resources.AddFile(ctx, e.a, pic, resources.AddOptions{ProjectID: e.project, Name: "sunset"}); err != nil {
		t.Fatal(err)
	}
	pictured, err := e.background(t, mattes.CutBackgroundIn{CutEditCommon: ops.CutEditCommon{DryRun: true}, With: mattes.WithPicture, Picture: "sunset"})
	if err != nil || pictured.Check == nil || !pictured.Check.Valid {
		t.Fatalf("a picture: %+v %v", pictured, err)
	}
	var steps editspec.Plan
	_ = json.Unmarshal(pictured.Plan, &steps)
	if len(steps.Ops) != 2 || steps.Ops[0].Op != "setResource" || steps.Ops[0].ID != "sunset" {
		t.Fatalf("the picture is pinned with it: %+v", steps.Ops)
	}
	// Off: the clip as recorded again.
	off, err := e.background(t, mattes.CutBackgroundIn{CutEditCommon: ops.CutEditCommon{DryRun: true}, Off: true})
	if err != nil || off.Check == nil || !off.Check.Valid || !strings.Contains(off.Summary, "plays its own background again") {
		t.Fatalf("off: %+v %v", off, err)
	}
	// Bad requests say so plainly.
	for _, in := range []mattes.CutBackgroundIn{{With: mattes.WithColour, Colour: "green"}, {With: "sky"}, {Clip: "9"}, {With: mattes.WithPicture, Picture: "nope"}} {
		if _, err := e.background(t, in); err == nil {
			t.Fatalf("%+v passed", in)
		} else if ae := apperr.From(err); strings.Contains(ae.Message, "--") {
			t.Fatalf("not plain: %q", ae.Message)
		}
	}
}

// Someone else's video is never shown behind the people, and a reference's
// people are never cut out (D441).
func TestReferencesStayOutOfBackgrounds(t *testing.T) {
	e := setup(t)
	e.saveCut(t)
	ref := e.footageOf(t, "theirs", 3)
	if ref.ID == e.clip.ID {
		t.Fatal("the same clip")
	}
	if err := e.a.DB.Update(context.Background(), store.CAssets, e.a.WorkspaceID(), ref.ID, bson.M{"$set": bson.M{"reference": models.AssetReference{Why: "marked", At: time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	_, err := e.background(t, mattes.CutBackgroundIn{With: mattes.WithFootage, Footage: ref.ID})
	if err == nil || !strings.Contains(apperr.From(err).Message, "someone else's video") {
		t.Fatalf("a reference behind the people: %v", err)
	}
}

// Footage deleted for good takes its cut-outs with it: the records and the
// files (D505).
func TestDeletingFootageDeletesItsCutOuts(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	out, err := e.cutout(t, mattes.CutoutIn{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	m := e.matte(t, out.Matte.ID)
	if m.File == nil {
		t.Fatal("no file")
	}
	del, err := ops.CallTyped[ops.AssetDeleteOut](ctx, e.a, "asset.delete", ops.AssetDeleteIn{AssetID: e.clip.ID, ProjectID: e.project, Forever: true}, ops.Caller{Human: true})
	if err != nil {
		t.Fatalf("delete: %+v %v", del, err)
	}
	if n, _ := e.a.DB.C(store.CMattes).CountDocuments(ctx, bson.M{"workspaceId": e.a.WorkspaceID()}); n != 0 {
		t.Fatalf("%d cut-outs left", n)
	}
	if _, err := e.a.Media.Stat(ctx, m.File.Key); err == nil {
		t.Fatal("the cut-out's file is still in the media store")
	}
}

// A person deletes a cut-out for good (it can be made again): not while the
// latest version of a cut plays it, itself or in a cut that nests it; older
// versions that play it are named, since they can't show it again; never by
// an agent, never without "forever".
func TestDeletingACutOut(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	out, err := e.cutout(t, mattes.CutoutIn{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	m := e.matte(t, out.Matte.ID)
	del := func(in mattes.CutoutDeleteIn, c ops.Caller) (*mattes.CutoutDeleteOut, error) {
		in.ID = m.ID
		return ops.CallTyped[mattes.CutoutDeleteOut](ctx, e.a, "footage.cutout.delete", in, c)
	}
	if d, err := del(mattes.CutoutDeleteIn{DryRun: true}, ops.Caller{Human: true}); err != nil || !d.Allowed || len(d.Blockers) != 0 || len(d.Older) != 0 ||
		!strings.Contains(d.Summary, "can be deleted") {
		t.Fatalf("nothing plays it: %+v %v", d, err)
	}
	// The cut's latest version plays it.
	apply := func(in mattes.CutBackgroundIn) {
		t.Helper()
		chk, err := e.background(t, in)
		if err != nil || chk.Check == nil || chk.Check.ConfirmToken == "" {
			t.Fatalf("checked: %+v %v", chk, err)
		}
		in.ConfirmToken = chk.Check.ConfirmToken
		if out, err := e.background(t, in); err != nil || !out.Saved {
			t.Fatalf("saved: %+v %v", out, err)
		}
	}
	e.saveCut(t)
	apply(mattes.CutBackgroundIn{With: mattes.WithBlur})
	first := e.head(t)
	if first.Spec.Clips[0].Background == nil || first.Spec.Clips[0].Background.Matte != m.File.SHA256 {
		t.Fatalf("the cut plays it: %+v", first.Spec.Clips[0].Background)
	}
	d, err := del(mattes.CutoutDeleteIn{DryRun: true}, ops.Caller{Human: true})
	if err != nil || d.Allowed || len(d.Blockers) != 1 || !strings.Contains(d.Blockers[0], "“Garden film”'s cut (version") || !strings.Contains(d.Summary, "Take the background out") {
		t.Fatalf("its latest version plays it: %+v %v", d, err)
	}
	if _, err := del(mattes.CutoutDeleteIn{Forever: true}, ops.Caller{Human: true}); apperr.From(err) == nil || apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("refused: %v", err)
	}
	// A cut of another project nests that version: once the cut itself moves on, the nesting cut still plays it.
	outer := ids.New(ids.Project)
	now := time.Now().UTC()
	nest := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: e.a.WorkspaceID(), ProjectID: outer, Branch: "main", Number: 1, SpecHash: "h", CreatedAt: now,
		Spec: editspec.Spec{SchemaVersion: 1, Clips: []editspec.Clip{{ID: "teaser", AssetID: editspec.NestedKey(e.project, first.ID), In: 0, Out: 1}}}}
	if err := e.a.DB.Insert(ctx, store.CRevisions, nest); err != nil {
		t.Fatal(err)
	}
	if err := e.a.DB.Insert(ctx, store.CProjects, models.Project{ID: outer, WorkspaceID: e.a.WorkspaceID(), Name: "Showreel",
		Branches: map[string]models.BranchHead{"main": {Head: nest.ID, UpdatedAt: now}}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	apply(mattes.CutBackgroundIn{Off: true})
	d, err = del(mattes.CutoutDeleteIn{DryRun: true}, ops.Caller{Human: true})
	if err != nil || d.Allowed || len(d.Blockers) != 1 || !strings.Contains(d.Blockers[0], "“Showreel”'s cut (version 1)") || len(d.Older) != 1 {
		t.Fatalf("a cut that nests it: %+v %v", d, err)
	}
	// Only older versions play it now: allowed, and they are named.
	if _, err := e.a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": outer}, bson.M{"$set": bson.M{"archived": true}}); err != nil {
		t.Fatal(err)
	}
	d, err = del(mattes.CutoutDeleteIn{DryRun: true}, ops.Caller{Human: true})
	if err != nil || !d.Allowed || len(d.Older) != 2 || !strings.Contains(d.Summary, "can't show their new background again") {
		t.Fatalf("only older versions: %+v %v", d, err)
	}
	// People only, and for good only when they say so.
	if _, err := del(mattes.CutoutDeleteIn{Forever: true}, ops.Caller{}); apperr.From(err) == nil || apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("an agent: %v", err)
	}
	if _, err := del(mattes.CutoutDeleteIn{}, ops.Caller{Human: true}); apperr.From(err) == nil || apperr.From(err).Code != apperr.ConfirmRequired {
		t.Fatalf("without forever: %v", err)
	}
	d, err = del(mattes.CutoutDeleteIn{Forever: true}, ops.Caller{Human: true})
	if err != nil || !d.Deleted {
		t.Fatalf("deleted: %+v %v", d, err)
	}
	if n, _ := e.a.DB.C(store.CMattes).CountDocuments(ctx, bson.M{"_id": m.ID}); n != 0 {
		t.Fatal("the record is still there")
	}
	if _, err := e.a.Media.Stat(ctx, m.File.Key); err == nil {
		t.Fatal("the cut-out's file is still in the media store")
	}
	// It can be made again.
	again, err := e.cutout(t, mattes.CutoutIn{})
	if err != nil || again.Reused || again.JobID == "" {
		t.Fatalf("made again: %+v %v", again, err)
	}
}
