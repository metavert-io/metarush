package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/worker"
)

// QA of a render through the ops registry and a real worker job, its visual
// evidence shared through the bucket, and delivery rules (approval, no
// overwrites, verified copies, sidecars).
func TestQAReportAndDelivery(t *testing.T) {
	in, ts := setup(t)
	a := in.app
	ctx := context.Background()
	proj := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "QA film", Brief: models.Brief{Platforms: []string{"youtube"}}})
	src := clip(t, ts, t.TempDir(), "take.mp4", "testsrc2=size=1280x720:rate=30:duration=4")
	res := ingest.Paths(ctx, a, []string{src}, ingest.Options{ProjectID: proj.Project.ID, Via: "cli"})
	if len(res) != 1 || res[0].Status != "ingested" {
		t.Fatalf("ingest: %+v", res)
	}
	runJobs(t, a, res[0].JobIDs)
	assetID := res[0].AssetID

	// Revision: two clips with a planned dip to black between them, plus a music bed without rights.
	plan := map[string]any{"projectId": proj.Project.ID, "instruction": "two halves with a dip", "rationale": "test cut", "spec": map[string]any{
		"schemaVersion": 1, "title": "Dip test 4s", "output": map[string]any{"preset": "landscape-1080p", "fps": 30, "platform": "youtube"},
		"clips": []any{
			map[string]any{"id": "first", "assetId": assetID, "in": 0, "out": 2},
			map[string]any{"id": "second", "assetId": assetID, "in": 2, "out": 4, "transitionIn": map[string]any{"type": "dip-to-black", "duration": 0.4}},
		},
		"audio": []any{map[string]any{"id": "bed", "assetId": assetID, "role": "music", "in": 0, "out": 4, "at": 0, "gainDb": -18}}}}
	raw, _ := json.Marshal(plan)
	chk := call[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: raw})
	if !chk.Valid {
		t.Fatalf("plan invalid: %+v", chk.Errors)
	}
	rev := call[ops.EditApplyOut](t, a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: chk.ConfirmToken}).Revision

	// A "render" with the planned dip (1.8–2.2s) and an unplanned black gap (3.0–3.5s).
	rd := fakeRender(t, ts, a, rev, "drawbox=enable='between(t,1.8,2.2)+between(t,3.0,3.5)':color=black:t=fill")

	// QA before the render is finished is refused.
	if _, err := ops.CallTyped[ops.QARunOut](ctx, a, "qa.run", ops.QARunIn{ID: rev.ID + "x"}, ops.Caller{}); err == nil {
		t.Fatal("bad id accepted")
	}
	run := call[ops.QARunOut](t, a, "qa.run", ops.QARunIn{ID: rev.ID})
	if !run.Created || run.Job == nil || run.RenderID != rd.ID || run.Job.RenderID != "" {
		t.Fatalf("qa.run: %+v", run)
	}
	runJobs(t, a, []string{run.Job.ID})
	show := call[ops.QAShowOut](t, a, "qa.show", ops.QAShowIn{ID: rev.ID, LocalFiles: true})
	rep := show.Report
	var gap *models.QAFinding
	for i, f := range rep.Findings {
		t.Logf("%s %s %s %s %s", f.ID, f.Severity, f.Kind, f.Timecode, f.Message)
		if f.Kind == "black_frames" {
			gap = &rep.Findings[i]
		}
		if (f.Kind == "black_frames" || f.Kind == "flash_frame") && f.From < 2.5 {
			t.Fatalf("the planned dip was flagged: %+v", f)
		}
	}
	if gap == nil || gap.Severity != models.QAError || gap.From < 2.9 || gap.To > 3.6 || gap.ClipIDs[0] != "second" || len(gap.Frames) == 0 {
		t.Fatalf("expected the 3.0–3.5 gap as an error in clip second: %+v", gap)
	}
	if rep.Verdict != models.QAFail || rep.RenderSHA256 != rd.Output.SHA256 || rep.Measurements.LoudnessTarget.Platform != "youtube" {
		t.Fatalf("report: verdict %s sha %s target %+v", rep.Verdict, rep.RenderSHA256, rep.Measurements.LoudnessTarget)
	}
	if rep.ContactSheet == nil || rep.ContactSheet.Key == "" || len(rep.Frames) < 3 {
		t.Fatalf("visual evidence missing: %+v", rep.ContactSheet)
	}
	for _, p := range []string{rep.ContactSheet.Path, rep.MomentsSheet.Path, rep.Frames[0].Path} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("local image %s: %v", p, err)
		}
	}
	var stored models.Render
	_ = a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), rd.ID, &stored)
	if stored.QA == nil || stored.QA.ReportID != rep.ID || stored.QA.Errors < 1 || stored.State != models.RenderAvailable {
		t.Fatalf("render QA summary: %+v (state %s)", stored.QA, stored.State)
	}
	// Same render, same checks: the existing report is returned, nothing re-queued.
	again := call[ops.QARunOut](t, a, "qa.run", ops.QARunIn{ID: rd.ID})
	if again.Created || again.Report == nil || again.Report.ID != rep.ID {
		t.Fatalf("expected the existing report: %+v", again)
	}
	// Another machine (empty cache) gets the same images from the bucket.
	B := newInstall(t, a.DB, a.Cfg.Media.S3.Endpoint, a.WorkspaceID(), "studio-b")
	showB := call[ops.QAShowOut](t, B.app, "qa.show", ops.QAShowIn{ID: rep.ID, LocalFiles: true})
	if h, _ := media.HashFile(showB.Report.ContactSheet.Path); h != rep.ContactSheet.SHA256 || !strings.HasPrefix(showB.Report.ContactSheet.Path, B.home) {
		t.Fatalf("B's contact sheet %s does not match", showB.Report.ContactSheet.Path)
	}

	// Delivery: drafts are refused without --allow-draft.
	out := t.TempDir()
	_, err := ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rev.ID}, Out: out, Quality: "review"}, ops.Caller{})
	if !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "isn't approved yet") {
		t.Fatalf("draft delivered: %v", err)
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Fatal("refused delivery wrote files")
	}
	// No final render exists.
	_, err = ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rev.ID}, Out: out, AllowDraft: true}, ops.Caller{})
	if !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("expected E_NOT_FOUND without a final render: %v", err)
	}
	d := call[ops.DeliverOut](t, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rev.ID}, Out: out, Quality: "review", AllowDraft: true})
	if len(d.Files) != 1 {
		t.Fatalf("deliver: %+v", d)
	}
	f := d.Files[0]
	if !strings.HasPrefix(filepath.Base(f.Path), "qa-film_dip-test-4s_r1_landscape-1080p_4s_review") || f.QAVerdict != models.QAFail {
		t.Fatalf("file: %+v", f)
	}
	if h, _ := media.HashFile(f.Path); h != rd.Output.SHA256 {
		t.Fatal("delivered bytes differ from the render")
	}
	var sc ops.DeliverySidecar
	b, _ := os.ReadFile(f.Sidecar)
	if err := json.Unmarshal(b, &sc); err != nil {
		t.Fatal(err)
	}
	if sc.Revision.ID != rev.ID || sc.Revision.Number != 1 || sc.Revision.SpecHash != rev.SpecHash || sc.Revision.Rationale != "test cut" || sc.Render.SHA256 != rd.Output.SHA256 ||
		len(sc.Sources) != 1 || sc.Sources[0].Name != "take.mp4" || len(sc.Sources[0].UsedAs) != 3 || len(sc.MusicRights) != 1 || sc.MusicRights[0].Recorded ||
		sc.Revision.Approval.State != models.ApprovalDraft || len(sc.Warnings) < 2 {
		t.Fatalf("sidecar: %s", b)
	}
	// Never overwrite: the same export again is refused and leaves the file intact.
	_, err = ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rev.ID}, Out: out, Quality: "review", AllowDraft: true}, ops.Caller{})
	if !apperr.Is(err, apperr.Conflict) || !strings.Contains(err.Error(), "overwrite") {
		t.Fatalf("expected a refusal to overwrite: %v", err)
	}
	if h, _ := media.HashFile(f.Path); h != rd.Output.SHA256 {
		t.Fatal("existing delivery was modified")
	}
	// A person approves (simulated here); approved revisions export without --allow-draft.
	now := time.Now().UTC()
	_ = a.DB.Update(ctx, store.CRevisions, a.WorkspaceID(), rev.ID, bson.M{"$set": bson.M{"approval": models.Approval{State: models.ApprovalApproved, By: &a.Actor, At: &now}}})
	d2 := call[ops.DeliverOut](t, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rev.ID}, Out: filepath.Join(out, "approved"), Quality: "review"})
	if len(d2.Files) != 1 || d2.Files[0].Approval != models.ApprovalApproved {
		t.Fatalf("approved delivery: %+v", d2)
	}
	_, err = ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rev.ID}, Out: t.TempDir(), Quality: "review", Presets: []string{"square-1080"}}, ops.Caller{})
	if !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("expected E_NOT_FOUND for an unrendered preset: %v", err)
	}
}

// runJobs runs a worker until the given jobs finish.
func runJobs(t *testing.T, a *app.App, jobIDs []string) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	w := worker.New(a)
	w.Log = func(string, ...any) {}
	w.IgnoreBattery = true // a laptop on battery would hold heavy jobs back
	go func() { _ = w.Run(ctx) }()
	deadline := time.Now().Add(120 * time.Second)
	for {
		done := 0
		for _, id := range jobIDs {
			j, _ := a.Queue.Get(ctx, id)
			switch j.State {
			case models.JobSucceeded:
				done++
			case models.JobFailed, models.JobCanceled:
				if j.Type == "transcribe" {
					done++
					continue
				}
				t.Fatalf("job %s %s %s: %s", j.ID, j.Type, j.State, j.Error)
			}
		}
		if done == len(jobIDs) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("jobs did not finish")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// fakeRender stores an MP4 (synthesized with ffmpeg at review scale) as an
// available render of rev, as the render worker would after Remotion.
func fakeRender(t *testing.T, ts *tools.Set, a *app.App, rev ops.RevisionSummary, vf string) models.Render {
	t.Helper()
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "render.mp4")
	if _, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=960x540:rate=30:duration=4", "-f", "lavfi", "-i", "aevalsrc=0.2*sin(2*PI*1000*t):s=48000:d=4",
		"-vf", vf + ",format=yuv420p", "-map", "0:v", "-map", "1:a", "-c:v", "libx264", "-preset", "ultrafast", "-c:a", "aac", "-t", "4", p}}); err != nil {
		t.Fatal(err)
	}
	sha, _ := media.HashFile(p)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	info, err := a.Media.PutFile(ctx, key, p, media.PutOptions{ContentType: "video/mp4", SHA256: sha}, media.Resume{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: rev.ProjectID, RevisionID: rev.ID, RevisionNumber: rev.Number, SpecHash: rev.SpecHash,
		Preset: "landscape-1080p", Quality: "review", State: models.RenderAvailable, CreatedBy: a.Actor, CreatedAt: now, CompletedAt: &now,
		Output: &models.RenderOutput{StorageKey: key, SHA256: sha, Size: info.Size, Duration: 4, Width: 960, Height: 540, FPS: 30}}
	if err := a.DB.Insert(ctx, store.CRenders, rd); err != nil {
		t.Fatal(err)
	}
	return rd
}
