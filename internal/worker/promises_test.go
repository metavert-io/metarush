package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// End to end through the worker (D300): a cut that plays another project's
// cut holding an AI-generated shot, with the promise "nothing generated
// without disclosure" and nothing disclosing it, is rendered and checked: the
// check finds the nested shot and the promise is broken.
func TestNestedGeneratedShotBreaksTheDisclosurePromise(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	fake := s.renderer()
	// The fake draws the video at the cut's length: 2 s of this project's own, 2 s of the other's.
	if err := os.WriteFile(filepath.Join(fake.Dir, "render-seconds"), []byte("4"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := s.worker()
	// The other project's cut: one shot an AI service made.
	dreams, err := ops.CallTyped[ops.ProjectOut](ctx, s.a, "project.create", ops.ProjectCreateIn{Name: "Dreams"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	gen := ingest.File(ctx, s.a, s.footage("dream", 3, false, false), ingest.Options{ProjectID: dreams.Project.ID, Via: "cli"})
	if gen.AssetID == "" {
		t.Fatalf("ingest %+v", gen)
	}
	src := models.AssetSource{Platform: models.PlatformGenerated, Generated: &models.GeneratedSource{Provider: "fake", ProviderTitle: "Test video", Model: "m", Operation: "shot"}}
	if _, err := s.a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": gen.AssetID}, bson.M{"$set": bson.M{"source": src, "title": "Dream shot"}}); err != nil {
		t.Fatal(err)
	}
	plan := fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"dream","assetId":%q,"in":0,"out":2}]}}`, dreams.Project.ID, gen.AssetID)
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, s.a, "edit.check", ops.EditCheckIn{Plan: json.RawMessage(plan)}, ops.Caller{})
	if err != nil || !chk.Valid {
		t.Fatalf("check: %+v %v", chk, err)
	}
	if _, err := ops.CallTyped[ops.EditApplyOut](ctx, s.a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	// This project's cut: its own footage, then that cut played inside it.
	own := ingest.File(ctx, s.a, s.footage("harbour", 3, false, true), ingest.Options{ProjectID: s.project, Via: "cli"})
	s.cut(own.AssetID, 0, 2)
	nest := func(c ops.CutEditCommon) ops.CutNestIn {
		c.ProjectID = s.project
		return ops.CutNestIn{CutEditCommon: c, From: dreams.Project.ID}
	}
	dry, err := ops.CallTyped[ops.CutEditOut](ctx, s.a, "cut.nest", nest(ops.CutEditCommon{DryRun: true}), ops.Caller{})
	if err != nil || dry.Check == nil || !dry.Check.Valid {
		t.Fatalf("nest dry run: %+v %v", dry, err)
	}
	saved, err := ops.CallTyped[ops.CutEditOut](ctx, s.a, "cut.nest", nest(ops.CutEditCommon{ConfirmToken: dry.Check.ConfirmToken}), ops.Caller{})
	if err != nil || saved.Applied == nil {
		t.Fatalf("nest: %+v %v", saved, err)
	}
	rev := saved.Applied.Revision.ID
	if _, err := ops.CallTyped[ops.RuleAddOut](ctx, s.a, "project.rules.add", ops.RuleAddIn{ProjectID: s.project, Text: "Nothing generated without disclosure", Promise: true}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	r, err := ops.CallTyped[ops.RenderOut](ctx, s.a, "render.submit", ops.RenderSubmitIn{RevisionID: rev, Quality: "review"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if j := runJobs(t, w, r.Job.ID)[r.Job.ID]; j.State != models.JobSucceeded {
		t.Fatalf("render %s: %s", j.State, j.Error)
	}
	checks := s.jobsOf(jobs.TypeQA, bson.M{"payload.renderId": r.Render.ID})
	if len(checks) != 1 {
		t.Fatalf("checks %+v", checks)
	}
	j := runJobs(t, w, checks[0].ID)[checks[0].ID]
	if j.State != models.JobSucceeded {
		t.Fatalf("quality check %s: %s", j.State, j.Error)
	}
	var rep models.QAReport
	if err := s.a.DB.GetByID(ctx, store.CQAReports, s.a.WorkspaceID(), j.Result["reportId"].(string), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Promises) != 1 {
		t.Fatalf("promises %+v", rep.Promises)
	}
	p := rep.Promises[0]
	if p.Status != models.PromiseBroken || !strings.Contains(p.Measured, "“Dream shot”") || !strings.Contains(p.Measured, "in a cut played in this one") ||
		p.Expected != "Made with AI: the shot “Dream shot” (Test video)." {
		t.Fatalf("the nested generated shot: %+v", p)
	}
}

// The reviewer's case in the quality check, end to end through the worker
// (D460): a cut shows a picture the starter ability comfyui-local made from
// words, one from an ability whose manifest says nothing about AI, and a stock
// photo an ability found (its manifest says none). With the promise "nothing
// generated without disclosure" and nothing disclosing them, the check counts
// the first two as AI pictures — the same answer delivery and Content
// Credentials give — and the promise is broken; the stock photo isn't counted.
func TestAnAbilitysAIPictureIsDisclosedInTheCheck(t *testing.T) {
	s := newShop(t)
	ctx := context.Background()
	fake := s.renderer()
	if err := os.WriteFile(filepath.Join(fake.Dir, "render-seconds"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := s.worker()
	clip := ingest.File(ctx, s.a, s.footage("harbour", 3, false, true), ingest.Options{ProjectID: s.project, Via: "cli"})
	if clip.AssetID == "" {
		t.Fatalf("ingest %+v", clip)
	}
	picture := func(file, colour, title, ability, abilityTitle, ai string) *models.Resource {
		t.Helper()
		path := filepath.Join(t.TempDir(), file)
		if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c="+colour+":size=64x64:duration=1", "-frames:v", "1", path).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v %s", err, b)
		}
		mw := &models.AbilityMadeWith{Ability: ability + "@1a2b3c4d", Name: ability, Title: abilityTitle, Hash: strings.Repeat("1a2b3c4d", 8), RunID: "abr_test", AI: ai}
		added, err := resources.AddFile(ctx, s.a, path, resources.AddOptions{ProjectID: s.project, Scope: models.ScopeProject, Title: title, Fresh: true,
			Source: models.ResourceSource{Kind: models.FromGenerated, Tool: "ability", MadeWith: &models.ResourceMadeWith{Kind: "ability", Ability: mw}}})
		if err != nil || added.Resource == nil {
			t.Fatalf("add %s: %+v %v", title, added, err)
		}
		return added.Resource
	}
	// A clip from an ability whose manifest says nothing about AI: counted as made.
	waves := ingest.File(ctx, s.a, s.footage("waves", 3, false, true), ingest.Options{ProjectID: s.project, Via: "cli"})
	if waves.AssetID == "" {
		t.Fatalf("ingest %+v", waves)
	}
	wsrc := models.AssetSource{Platform: models.PlatformAbility, Title: "waves", Method: "ability",
		MadeWith: &models.AbilityMadeWith{Ability: "clip-maker@1a2b3c4d", Name: "clip-maker", Title: "Clip maker", Hash: strings.Repeat("1a2b3c4d", 8), RunID: "abr_test"}}
	if _, err := s.a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": waves.AssetID}, bson.M{"$set": bson.M{"source": wsrc, "title": "Waves"}}); err != nil {
		t.Fatal(err)
	}
	dawn := picture("dawn.png", "0xcc6622", "Harbour at dawn", "comfyui-local", "Pictures from ComfyUI on this computer", models.AIWorkMade)
	light := picture("light.png", "0x2266cc", "Lighthouse", "picture-maker", "Picture maker", "")
	pier := picture("pier.png", "0x22cc66", "Pier photo", "pexels-stock", "Free stock from Pexels", models.AIWorkNone)
	s.cut(clip.AssetID, 0, 2)
	pin := func(r *models.Resource) string {
		return fmt.Sprintf(`%q:{"id":%q,"sha256":%q}`, r.Name, r.ID, r.Latest().SHA256)
	}
	corner := func(id string, at float64, r *models.Resource) string {
		return fmt.Sprintf(`{"id":%q,"template":"logo.corner","at":%g,"duration":0.6,"params":{"logo":%q}}`, id, at, r.Name)
	}
	spec := fmt.Sprintf(`{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":0,"out":1},{"id":"b","assetId":%q,"in":0,"out":1}],`+
		`"resources":{%s,%s,%s},"overlays":[%s,%s,%s]}`, clip.AssetID, waves.AssetID, pin(dawn), pin(light), pin(pier),
		corner("dawn", 0, dawn), corner("light", 0.6, light), corner("pier", 1.2, pier))
	plan := fmt.Sprintf(`{"projectId":%q,"baseRevisionId":%q,"instruction":"the pictures","spec":%s}`, s.project, s.head, spec)
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, s.a, "edit.check", ops.EditCheckIn{Plan: json.RawMessage(plan)}, ops.Caller{})
	if err != nil || !chk.Valid {
		t.Fatalf("check: %+v %v", chk, err)
	}
	applied, err := ops.CallTyped[ops.EditApplyOut](ctx, s.a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[ops.RuleAddOut](ctx, s.a, "project.rules.add", ops.RuleAddIn{ProjectID: s.project, Text: "Nothing generated without disclosure", Promise: true}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	r, err := ops.CallTyped[ops.RenderOut](ctx, s.a, "render.submit", ops.RenderSubmitIn{RevisionID: applied.Revision.ID, Quality: "review"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if j := runJobs(t, w, r.Job.ID)[r.Job.ID]; j.State != models.JobSucceeded {
		t.Fatalf("render %s: %s", j.State, j.Error)
	}
	checks := s.jobsOf(jobs.TypeQA, bson.M{"payload.renderId": r.Render.ID})
	if len(checks) != 1 {
		t.Fatalf("checks %+v", checks)
	}
	j := runJobs(t, w, checks[0].ID)[checks[0].ID]
	if j.State != models.JobSucceeded {
		t.Fatalf("quality check %s: %s", j.State, j.Error)
	}
	var rep models.QAReport
	if err := s.a.DB.GetByID(ctx, store.CQAReports, s.a.WorkspaceID(), j.Result["reportId"].(string), &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Promises) != 1 {
		t.Fatalf("promises %+v", rep.Promises)
	}
	p := rep.Promises[0]
	if p.Status != models.PromiseBroken || !strings.Contains(p.Expected, "the picture “Harbour at dawn”") || !strings.Contains(p.Expected, "the picture “Lighthouse”") ||
		!strings.Contains(p.Expected, "the shot “Waves” (the ability “Clip maker”)") || strings.Contains(p.Expected, "Pier photo") || strings.Contains(p.Measured, "Pier photo") {
		t.Fatalf("what the abilities made: %+v", p)
	}
}
