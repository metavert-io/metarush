package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// plainOK fails when plain output has something a person should not have to
// read (skills/_shared/voice.md); the pointer to --details itself is fine.
func plainOK(t *testing.T, what, out string) {
	t.Helper()
	if fs := (plainwords.Allow{"flag:--details"}).Filter(plainwords.CheckShown(out)); len(fs) > 0 {
		t.Errorf("plain %s shows %v:\n%s", what, fs, out)
	}
}

func wantIn(t *testing.T, what, out string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, out)
		}
	}
}

// edit check and apply: plain says whether the change can be saved and what
// it changes; codes, plan paths, IDs and the saving command are --details.
func TestEditCheckAndApplySpeakPlainly(t *testing.T) {
	chk := &ops.EditCheckOut{Valid: true, BaseRevisionID: "rev_01jabcdefghjkmnpqrstvwxyz0", Duration: 32.3,
		Warnings:     []editspec.Issue{{Severity: "warning", Code: "heavy_crop", Path: "/clips/0/framing", Message: "Cropping the hook clip cuts off 68% of the picture."}},
		Diff:         &editspec.Diff{Summary: []string{"2 narration lines changed"}},
		ConfirmToken: "ct_1792344972_833cae70be773312a48168387418a683"} // gitleaks:allow (made up for the test)
	next := commandLine(detailsCmd(false), "metarush edit apply", "--plan", "plan.json", "--confirm", chk.ConfirmToken)
	plain := stdout(t, func() { printCheck(detailsCmd(false), chk, next) })
	plainOK(t, "edit check", plain)
	wantIn(t, "plain edit check", plain, "Looks good: the cut will be 32.3 s long.", "  ! Cropping the hook clip", "  - 2 narration lines changed", "Ready to save. Nothing has changed yet.")
	detailed := stdout(t, func() { printCheck(detailsCmd(true), chk, next) })
	wantIn(t, "--details edit check", detailed, "(heavy_crop at /clips/0/framing)", "next: metarush edit apply --plan plan.json --confirm ct_1792344972_", "rev_01jabcdefghjkmnpqrstvwxyz0")

	bad := &ops.EditCheckOut{Errors: []editspec.Issue{{Severity: "error", Code: "in_after_out", Path: "/clips/0/out", Message: "The hook clip ends before it starts."}},
		Next: "Fix the errors (paths are JSON pointers into the plan) and run edit check again"}
	plainBad := stdout(t, func() { printCheck(detailsCmd(false), bad, "") })
	plainOK(t, "failed edit check", plainBad)
	wantIn(t, "plain failed edit check", plainBad, "This can't be saved yet: 1 problem to fix.", "  ✗ The hook clip ends before it starts.")

	applied := &ops.EditApplyOut{Revision: ops.RevisionSummary{ID: "rev_01jabcdefghjkmnpqrstvwxyz1", Number: 1, Duration: 32.3}, Rebased: true,
		Diff: editspec.Diff{Summary: []string{"first cut: 3 clips"}}, URL: "http://127.0.0.1:4845/p/prj_01jabcdefghjkmnpqrstvwxyz0/r/rev_01jabcdefghjkmnpqrstvwxyz1",
		Warnings: []editspec.Issue{{Severity: "warning", Code: "rights_unknown", Path: "/audio/0/rights", Message: "The music bed has no rights recorded."}}}
	plainApplied := stdout(t, func() { printApplied(applied, false) })
	plainOK(t, "edit apply", plainApplied)
	wantIn(t, "plain edit apply", plainApplied, "Saved version 1 (32.3 s).", "both are kept", "http://127.0.0.1:4845/p/", "  ! The music bed has no rights recorded.")
	wantIn(t, "--details edit apply", stdout(t, func() { printApplied(applied, true) }), "version: rev_01jabcdefghjkmnpqrstvwxyz1", "rights_unknown")
}

// Versions, renders, projects and clips are named by number and title.
func TestListsAndShowsNameThingsByTitle(t *testing.T) {
	now := time.Now()
	rev := ops.RevisionSummary{ID: "rev_01jabcdefghjkmnpqrstvwxyz3", Branch: "main", Number: 3, Duration: 32.3, Approval: models.Approval{State: "draft"}, IsHead: true,
		Instruction: "Scratch narration for 2 lines", CreatedBy: models.Actor{UserName: "Theo"}, CreatedAt: now}
	row := stdout(t, func() { printRevisionRow(rev, false) })
	plainOK(t, "revision list", row)
	wantIn(t, "plain revision row", row, "* version 3 · 32.3 s · draft · Theo · “Scratch narration for 2 lines”")
	wantIn(t, "--details revision row", stdout(t, func() { printRevisionRow(rev, true) }), "rev_01jabcdefghjkmnpqrstvwxyz3  main")

	rd := models.Render{ID: "rnd_01jabcdefghjkmnpqrstvwxyz4", RevisionNumber: 3, Preset: "vertical-1080p", Quality: "review", State: models.RenderAvailable, CreatedAt: now,
		Output: &models.RenderOutput{Width: 540, Height: 960, Duration: 32.3, Size: 19 << 20}, QA: &models.RenderQA{Verdict: models.QAWarnings, Warnings: 5}}
	status := &ops.RenderOut{Render: rd, URL: "http://127.0.0.1:4845/renders/rnd_01jabcdefghjkmnpqrstvwxyz4",
		Job: &models.Job{ID: "job_01jabcdefghjkmnpqrstvwxyz5", Type: "render", State: models.JobSucceeded, Progress: models.Progress{Pct: 1, Message: "done"}}}
	plainStatus := stdout(t, func() { printRenderStatus(status, false) })
	plainOK(t, "render status", plainStatus)
	wantIn(t, "plain render status", plainStatus, "The review copy of version 3 is ready (32.3 s, 19.0 MB).", "Quality check: 5 to check.")
	wantIn(t, "--details render status", stdout(t, func() { printRenderStatus(status, true) }), "render rnd_01jabcdefghjkmnpqrstvwxyz4", "job job_01jabcdefghjkmnpqrstvwxyz5")

	proj := &ops.ProjectShowOut{Project: models.Project{ID: "prj_01jabcdefghjkmnpqrstvwxyz0", Name: "Garden story"}, HeadRevisionID: rev.ID, OpenJobs: 1,
		URL:       "http://127.0.0.1:4845/p/prj_01jabcdefghjkmnpqrstvwxyz0",
		Assets:    []ops.AssetSummary{{ID: "ast_01jabcdefghjkmnpqrstvwxyz6", Title: "interview-a", Name: "interview-a.mov", Kind: "video", State: models.AssetAvailable, Duration: 27, Variants: []string{"proxy"}}},
		Revisions: []ops.RevisionSummary{rev}, Renders: []models.Render{rd}}
	plainProject := stdout(t, func() { printProjectShow(proj, false) })
	plainOK(t, "project show", plainProject)
	wantIn(t, "plain project show", plainProject, "Garden story", "Latest: version 3 (32.3 s, draft).", "interview-a.mov", "* version 3", "quality check: 5 to check")
	wantIn(t, "--details project show", stdout(t, func() { printProjectShow(proj, true) }), "prj_01jabcdefghjkmnpqrstvwxyz0", "ast_01jabcdefghjkmnpqrstvwxyz6", "rnd_01jabcdefghjkmnpqrstvwxyz4")

	as := &ops.AssetShowOut{Asset: models.Asset{ID: "ast_01jabcdefghjkmnpqrstvwxyz6", Kind: "video", Name: "interview-a.mov", Title: "Garage interview", State: models.AssetAvailable,
		SHA256: strings.Repeat("ab", 32), Size: 2 << 20, Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 27, Width: 1920, Height: 1080, FPS: 30, VFR: true},
		Transcript: &models.TranscriptStatus{State: "available", Words: 412, Speakers: 2}, Variants: map[string]models.Variant{models.VariantProxy: {}}},
		Jobs: []models.Job{{ID: "job_01jabcdefghjkmnpqrstvwxyz7", Type: "transcribe", State: models.JobFailed, Error: "sherpa: exit status 1"}}}
	plainAsset := stdout(t, func() { printAssetShow(as, false) })
	plainOK(t, "asset show", plainAsset)
	wantIn(t, "plain asset show", plainAsset, "“Garage interview” (“interview-a.mov”)", "Video, 27.0 s, 1920×1080 at 30 frames a second, with sound, 2.0 MB.",
		"changes its frame rate while it plays", "Transcript: 412 words, 2 speakers.", "Preview copy: ready.", "Didn't finish: transcription.")
	wantIn(t, "--details asset show", stdout(t, func() { printAssetShow(as, true) }), "ast_01jabcdefghjkmnpqrstvwxyz6", "job_01jabcdefghjkmnpqrstvwxyz7", "sherpa")
}

// A quality check reads like the dashboard's: what was found, where, and the
// fix; measurements, check names, fixes as changes and frame paths are --details.
func TestQualityCheckSpeaksPlainly(t *testing.T) {
	lufs, peak := -14.8, 0.0
	out := &ops.QAShowOut{URL: "http://127.0.0.1:4845/renders/rnd_01jabcdefghjkmnpqrstvwxyz4", Dir: "/Users/you/.metarush/scratch/qa/qar_01jabcdefghjkmnpqrstvwxyz8",
		Report: models.QAReport{ID: "qar_01jabcdefghjkmnpqrstvwxyz8", RenderID: "rnd_01jabcdefghjkmnpqrstvwxyz4", RevisionNumber: 3, Preset: "vertical-1080p", Quality: "review",
			Verdict: models.QAWarnings, Counts: models.QACounts{Warnings: 2}, RunBy: "studio-mac", CreatedAt: time.Now(),
			Measurements: models.QAMeasurements{Duration: 32.3, Width: 540, Height: 960, IntegratedLUFS: &lufs, TruePeakDBTP: &peak, VideoCodec: "h264", PixelFormat: "yuv420p"},
			Findings: []models.QAFinding{
				{ID: "f1", Severity: models.QAWarning, Kind: "true_peak_high", Basis: models.QAObserved, From: 0, To: 32.3, Timecode: "0:00.000–0:32.300",
					Message: "the loudest moments hit the ceiling, so they may crackle once the video is shared.",
					Fix:     &models.QAFix{Summary: "Turn the overall loudness down a little and render again.", Ops: []editspec.Op{{Op: "setMix", Mix: &editspec.Mix{}}}}},
				{ID: "f2", Severity: models.QAWarning, Kind: "narration_over_dialogue", Basis: models.QAHeuristic, From: 21.3, To: 23.5, Timecode: "0:21.333–0:23.500",
					Message: "Around 0:21 the narration talks over someone speaking on camera.", Frames: []string{"fr1"}}},
			Frames:      []models.QAFrame{{ID: "fr1", Path: "/Users/you/.metarush/scratch/qa/qar_01jabcdefghjkmnpqrstvwxyz8/frames/07.jpg"}},
			Checks:      []models.QACheck{{Name: "loudness", Status: "warning"}},
			Limitations: []string{"This is a review copy at half size."}}}
	plain := stdout(t, func() { printQA(out, false) })
	plainOK(t, "qa show", plain)
	wantIn(t, "plain qa show", plain, "Quality check of the review copy of version 3: 2 things worth checking.", "Checked today", "on studio-mac",
		"  ! The whole video: The loudest moments hit the ceiling", "    Fix: Turn the overall loudness down", "  ! Around 0:21 the narration talks over someone speaking on camera. (estimated)")
	detailed := stdout(t, func() { printQA(out, true) })
	wantIn(t, "--details qa show", detailed, "true_peak_high", "-14.8 LUFS", `"op":"setMix"`, "frames/07.jpg", "checks: loudness warning", "limitation:", "qar_01jabcdefghjkmnpqrstvwxyz8")
}

// While a command waits, each job is a line about the clip by its title; the
// IDs, job types and worker messages are --details.
func TestJobProgressLinesNameTheClip(t *testing.T) {
	ctx := context.Background()
	names := newJobWords(nil)
	names.titles["ast_1"] = "interview-a"
	names.renders["rnd_1"] = &models.Render{ID: "rnd_1", Quality: "review", RevisionNumber: 3}
	for _, tc := range []struct {
		job  models.Job
		want string
	}{
		{models.Job{Type: "transcribe", AssetID: "ast_1", State: models.JobQueued}, ""},
		{models.Job{Type: "transcribe", AssetID: "ast_1", State: models.JobRunning, Progress: models.Progress{Pct: 0.42, Message: "transcribing locally with parakeet"}}, "Transcribing “interview-a”… 40%"},
		{models.Job{Type: "transcribe", AssetID: "ast_1", State: models.JobSucceeded}, "“interview-a”: transcript ready"},
		{models.Job{Type: "proxy", AssetID: "ast_1", State: models.JobSucceeded}, "“interview-a”: preview copy ready"},
		{models.Job{Type: "evidence", AssetID: "ast_1", State: models.JobFailed, Error: "ffmpeg: exit status 1"}, "“interview-a”: the scene analysis didn't finish"},
		{models.Job{Type: "transcribe", AssetID: "ast_1", State: models.JobQueued, AttemptCount: 1}, "Trying again: transcribing “interview-a”…"},
		{models.Job{Type: "render", RenderID: "rnd_1", State: models.JobRunning, Progress: models.Progress{Pct: 0.55, Message: "rendering 55% encoding"}}, "Making the review copy of version 3… 50%"},
		{models.Job{Type: "render", RenderID: "rnd_1", State: models.JobSucceeded}, "The review copy of version 3 is ready."},
		{models.Job{Type: "qa", Payload: map[string]any{"renderId": "rnd_1"}, State: models.JobSucceeded}, "The quality check of the review copy of version 3 is done."},
	} {
		got := names.progress(ctx, &tc.job)
		if got != tc.want {
			t.Errorf("%s %s: got %q, want %q", tc.job.Type, tc.job.State, got, tc.want)
		}
		plainOK(t, "progress line", got)
	}
}

// Adding footage says what was added, by title, and what to expect next.
func TestIngestSummaryIsPlain(t *testing.T) {
	res := &ingest.FilesOut{Ingested: 1, Deduped: 1, Duplicates: 1, Note: "The originals are safely stored.",
		Results: []ingest.Result{
			{Name: "broll-c.mp4", Title: "broll-c", Status: "ingested", AssetID: "ast_01jabcdefghjkmnpqrstvwxyz9", JobIDs: []string{"job_01jabcdefghjkmnpqrstvwxyz5"}},
			{Name: "interview-a.mov", Status: "deduped", AssetID: "ast_01jabcdefghjkmnpqrstvwxyz6", Duplicate: &models.DuplicateOf{AssetID: "ast_01jabcdefghjkmnpqrstvwxyz6", Title: "interview-a", Where: "Garden story", Action: models.DuplicateSameFile}},
			{Name: "copy.mov", Status: "duplicate", Duplicate: &models.DuplicateOf{AssetID: "ast_01jabcdefghjkmnpqrstvwxyz6", Title: "interview-a", Where: "Garden story", Action: models.DuplicateSkipped}},
		}}
	plain := stdout(t, func() { printIngest(res, nil, false, false) })
	plainOK(t, "ingest", plain)
	wantIn(t, "plain ingest", plain, "Added “broll-c” (broll-c.mp4)", "interview-a.mov: already stored as “interview-a” (in Garden story)",
		"1 added, 1 was already in MetaRush, 1 identical to a clip you have, skipped.", "The originals are safely stored.")
	waited := []models.Job{{State: models.JobSucceeded}, {State: models.JobFailed}}
	wantIn(t, "plain ingest after waiting", stdout(t, func() { printIngest(res, waited, true, false) }), "1 step didn't finish")
	wantIn(t, "--details ingest", stdout(t, func() { printIngest(res, nil, false, true) }), "ingested  ast_01jabcdefghjkmnpqrstvwxyz9", "job_01jabcdefghjkmnpqrstvwxyz5")
}

// The command that saves a checked small edit is the one that ran, quoted for a
// shell, without what only shows or checks, plus its token.
func TestSameCommandWithRebuildsTheCommand(t *testing.T) {
	root := &cobra.Command{Use: "metarush"}
	root.PersistentFlags().String("home", "", "")
	root.PersistentFlags().Bool("details", false, "")
	move := &cobra.Command{Use: "move", RunE: func(*cobra.Command, []string) error { return nil }}
	move.Flags().Bool("dry-run", false, "")
	move.Flags().StringP("message", "m", "", "")
	cut := &cobra.Command{Use: "cut"}
	cut.AddCommand(move)
	root.AddCommand(cut)
	root.SetArgs([]string{"cut", "move", "Spring promo", "hook", "start", "--dry-run", "--details", "-m", "open on it", "--home", "/tmp/mr home"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	got := sameCommandWith(move, "--confirm", "ct_1_abc")
	if want := "metarush cut move 'Spring promo' hook start --home '/tmp/mr home' --message 'open on it' --confirm ct_1_abc"; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := commandLine(move, "metarush edit apply", "--plan", "my plan.json"); got != "metarush edit apply --plan 'my plan.json' --home '/tmp/mr home'" {
		t.Errorf("commandLine: %s", got)
	}
}

func TestFolderPlaceNamesFoldersWithoutPaths(t *testing.T) {
	for in, want := range map[string]string{
		"~/Movies/MetaRush Inbox": "“MetaRush Inbox” in your Movies folder",
		"~/Desktop/Drop":          "“Drop” on your Desktop",
		"~/work/clips/Drop":       "“Drop” (in work/clips, in your home folder)",
		"/Volumes/Card/DCIM":      "“DCIM” on the drive “Card”",
		"~":                       "your home folder",
	} {
		if got := folderPlace(in); got != want {
			t.Errorf("%s: got %q, want %q", in, got, want)
		}
		plainOK(t, "folder", folderPlace(in))
	}
}
