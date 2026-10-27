package cli

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/conversation"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// The plain output speaks to an amateur video editor (skills/_shared/voice.md):
// a scripted run of the commands people use most, against a seeded workspace,
// is checked line by line with internal/plainwords — no internal IDs, confirm
// tokens, flags, commands, file paths, JSON, internal codes or unexplained
// jargon. --details and --json are where that detail lives, so they are not
// checked for words; they are checked to still carry it. PLAINGUARD_DUMP=<file>
// writes every command's plain output for review.

// plainAllow holds what every plain output may contain: the pointer to the
// disclosure itself.
var plainAllow = plainwords.Allow{"flag:--details"}

type cliRun struct {
	args           []string
	stdout, stderr string
	code           int
}

// runCLI runs one command in this process, as the metarush binary would.
func runCLI(t *testing.T, home string, args ...string) cliRun {
	t.Helper()
	return runCLIContext(t, context.Background(), home, args...)
}

// runCLIContext is runCLI under ctx: a deadline ends a command that would
// otherwise wait forever (as Ctrl-C would).
func runCLIContext(t *testing.T, ctx context.Context, home string, args ...string) cliRun {
	t.Helper()
	root := &cobra.Command{Use: "metarush", SilenceUsage: true, SilenceErrors: true,
		PersistentPreRunE: func(c *cobra.Command, _ []string) error { return assistantGuard(c) }}
	root.PersistentFlags().StringVar(&homeFlag, "home", "", "")
	root.PersistentFlags().Bool("json", false, "")
	root.PersistentFlags().Bool("details", false, "")
	root.PersistentFlags().String("agent", "", "")
	root.PersistentFlags().Bool("propose", false, "")
	for _, c := range commands() {
		root.AddCommand(c)
	}
	root.SetArgs(append([]string{"--home", home}, args...))
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outC, errC := make(chan string), make(chan string)
	go func() { b, _ := io.ReadAll(outR); outC <- string(b) }()
	go func() { b, _ := io.ReadAll(errR); errC <- string(b) }()
	cmd, err := root.ExecuteContextC(ctx)
	code := 0
	if err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			code = ee.code
		} else {
			// What Execute prints for a command typed wrong.
			fmt.Fprintln(os.Stderr, sentence(err.Error()))
			if cmd != nil {
				fmt.Fprintf(os.Stderr, "%s --help shows what it takes.\n", cmd.CommandPath())
			}
			code = 1
		}
	}
	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return cliRun{args: args, stdout: <-outC, stderr: <-errC, code: code}
}

type plainSeed struct {
	project, interview, interview2, music, call, rev1, rev2, render, failedJob string
	plan, badPlan, sheet, script                                               string
}

// seedPlain makes a small workspace: a project with two interviews (one with a
// transcript) and a music track, two versions (the music without recorded
// rights, the interviews cropped hard for vertical), a review render with a
// quality check, a failed transcript job, a job waiting for a machine and a
// few notifications.
func seedPlain(t *testing.T, a *app.App) plainSeed {
	t.Helper()
	ctx := context.Background()
	ws := a.WorkspaceID()
	now := time.Now().UTC()
	var s plainSeed
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "Sweep", CreatedAt: now, CreatedBy: a.Actor}); err != nil {
		t.Fatal(err)
	}
	pc, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Garden story", Brief: models.Brief{Tone: "warm"}}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	s.project = pc.Project.ID
	clip := func(name, title string, video bool, w, h int, dur float64) string {
		as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: ws, ProjectIDs: []string{s.project}, Kind: "video", Name: name, Title: title,
			Size: 2 << 20, State: models.AssetAvailable,
			Media: models.MediaInfo{HasVideo: video, HasAudio: true, Duration: dur, Width: w, Height: h, FPS: 30}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
		if !video {
			as.Kind = "audio"
		}
		as.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(as.ID)))
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			t.Fatal(err)
		}
		return as.ID
	}
	s.interview = clip("interview-a.mov", "Garage interview", true, 1920, 1080, 27)
	s.interview2 = clip("interview-b.mp4", "Advice", true, 1280, 720, 19.8)
	s.music = clip("music-bed.m4a", "Music bed", false, 0, 0, 30)
	// A call app's file, titled by its file name: the person's own words, which
	// look like a code (D113).
	s.call = clip("riverside_sam_raw-video-cfr_garden_show_0002.mp4", "", true, 1280, 720, 27)
	words := func(from float64, text string) []models.Word {
		var ws []models.Word
		for i, w := range strings.Fields(text) {
			ws = append(ws, models.Word{Text: w, From: from + float64(i)*0.3, To: from + float64(i)*0.3 + 0.25})
		}
		return ws
	}
	for i, l := range []struct {
		from float64
		text string
	}{{0.2, "When I started building my first robot in my tiny garage, I had no idea."}, {11.6, "Then my neighbor leaned over the fence and asked me one simple question."}} {
		seg := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: ws, AssetID: s.interview, Index: i, From: l.from, To: l.from + 4, Text: l.text, Words: words(l.from, l.text), CreatedAt: now}
		if err := a.DB.Insert(ctx, store.CSegments, seg); err != nil {
			t.Fatal(err)
		}
	}
	for i, l := range []struct {
		from float64
		text string
	}{{0.4, "Thanks for having me on the show today."}, {9.0, "The trick is a deep pot and good soil."}} {
		seg := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: ws, AssetID: s.call, Index: i, From: l.from, To: l.from + 3, Text: l.text, Words: words(l.from, l.text), CreatedAt: now}
		if err := a.DB.Insert(ctx, store.CSegments, seg); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{s.interview, s.call} {
		st := models.TranscriptStatus{State: "available", Provider: "test", Precision: "word", Language: "en", Segments: 2, Speakers: 1, UpdatedAt: now}
		if err := a.DB.Update(ctx, store.CAssets, ws, id, bson.M{"$set": bson.M{"transcript": st}}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ops.CallTyped[conversation.CallOut](ctx, a, "conversation.create", conversation.CallCreateIn{ProjectID: s.project, Name: "Garden call", Align: "together",
		Members: []conversation.CallMemberIn{{AssetID: s.interview, Name: "Maya"}, {AssetID: s.call, Name: "Sam"}}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write := func(name string, v any) string {
		p := filepath.Join(dir, name)
		var b []byte
		if s, ok := v.(string); ok {
			b = []byte(s)
		} else {
			b, _ = json.Marshal(v)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	spec := editspec.Spec{SchemaVersion: 1, Title: "Garden story", Output: editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30, Platform: "shorts"},
		Clips: []editspec.Clip{{ID: "hook", AssetID: s.interview, In: 0.2, Out: 15.2}, {ID: "advice", AssetID: s.interview2, In: 8, Out: 19}},
		Audio: []editspec.AudioTrack{{ID: "bed", AssetID: s.music, Role: "music", In: 0, Out: 26, At: 0}}}
	plan, _ := json.Marshal(editspec.Plan{ProjectID: s.project, Instruction: "A short vertical story", Spec: &spec})
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, a, "edit.check", ops.EditCheckIn{Plan: plan}, ops.Caller{})
	if err != nil || !chk.Valid {
		t.Fatalf("seed cut: %v %+v", err, chk)
	}
	ap, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	s.rev1 = ap.Revision.ID
	move, _ := json.Marshal(editspec.Plan{ProjectID: s.project, BaseRevisionID: s.rev1, Instruction: "Open on the advice",
		Ops: []editspec.Op{{Op: "moveClip", ID: "advice", Before: "hook"}}})
	chk2, err := ops.CallTyped[ops.EditCheckOut](ctx, a, "edit.check", ops.EditCheckIn{Plan: move}, ops.Caller{})
	if err != nil || !chk2.Valid {
		t.Fatalf("seed second version: %v %+v", err, chk2)
	}
	ap2, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: move, ConfirmToken: chk2.ConfirmToken}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	s.rev2 = ap2.Revision.ID
	// Changes people check next: a trim (valid, with the warnings the cut has) and one that can't work.
	s.plan = write("plan.json", map[string]any{"baseRevisionId": s.rev2, "instruction": "Tighten the hook",
		"ops": []map[string]any{{"op": "updateClip", "id": "hook", "set": map[string]any{"out": 14.5}}}})
	s.badPlan = write("bad-plan.json", map[string]any{"baseRevisionId": s.rev2, "instruction": "Broken",
		"ops": []map[string]any{{"op": "updateClip", "id": "hook", "set": map[string]any{"in": 20, "out": 2}}}})
	s.sheet = filepath.Join(dir, "garden.cut.md")
	s.script = write("script.txt", "[@hook+0.5] It started in a tiny garage.\n\n[@advice] And then he found the one moment that matters.\n")

	// A finished review render with a quality check.
	done := now.Add(-time.Minute)
	rnd := models.Render{ID: ids.New(ids.Render), WorkspaceID: ws, ProjectID: s.project, RevisionID: s.rev2, RevisionNumber: 2, Preset: "vertical-1080p", Quality: "review",
		State: models.RenderAvailable, Output: &models.RenderOutput{Width: 540, Height: 960, Duration: 26, Size: 19 << 20, SHA256: fmt.Sprintf("%064x", 7)},
		CreatedBy: a.Actor, CreatedAt: now.Add(-2 * time.Minute), CompletedAt: &done}
	rep := models.QAReport{ID: ids.New(models.QAReportPrefix), WorkspaceID: ws, ProjectID: s.project, RevisionID: s.rev2, RevisionNumber: 2, RenderID: rnd.ID,
		Preset: "vertical-1080p", Quality: "review", Version: "qa-3", Verdict: models.QAWarnings, Counts: models.QACounts{Warnings: 1},
		Findings: []models.QAFinding{{ID: "f1", Severity: models.QAWarning, Kind: "true_peak_high", Basis: models.QAObserved, From: 0, To: 26, Timecode: "0:00.000–0:26.000",
			Message: "The loudest moments hit the ceiling, so they may crackle once the video is shared.",
			Fix:     &models.QAFix{Summary: "Turn the overall loudness down a little and render again.", Ops: []editspec.Op{{Op: "setMix", Mix: &editspec.Mix{}}}}}},
		Checks: []models.QACheck{{Name: "conformance", Status: "pass", Basis: models.QAObserved, Detail: "Length, size and frame rate match the plan."}},
		Frames: []models.QAFrame{}, Limitations: []string{"This is a review copy at half size."}, CreatedBy: a.Actor, CreatedAt: now}
	rnd.QA = &models.RenderQA{ReportID: rep.ID, Verdict: rep.Verdict, Warnings: 1, At: now}
	if err := a.DB.Insert(ctx, store.CRenders, rnd); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Insert(ctx, store.CQAReports, rep); err != nil {
		t.Fatal(err)
	}
	s.render = rnd.ID

	// A transcript that failed with a program's error, and one waiting for a machine.
	fin := now.Add(-30 * time.Second)
	failed := models.Job{ID: ids.New(ids.Job), WorkspaceID: ws, Type: "transcribe", State: models.JobFailed, Heavy: true, ProjectID: s.project, AssetID: s.interview2,
		Payload: map[string]any{"assetId": s.interview2}, IdempotencyKey: "plain-failed", AttemptCount: 3, MaxAttempts: 3, Attempts: []models.Attempt{},
		Error: "sherpa-onnx-offline failed: exit status 1: /tmp/model/tokens.txt: no such file", CreatedBy: a.Actor, CreatedAt: now.Add(-time.Hour), UpdatedAt: fin, FinishedAt: &fin}
	waiting := models.Job{ID: ids.New(ids.Job), WorkspaceID: ws, Type: "visual", State: models.JobQueued, Heavy: true, ProjectID: s.project, AssetID: s.interview,
		Payload: map[string]any{"assetId": s.interview}, IdempotencyKey: "plain-waiting", MaxAttempts: 3, Attempts: []models.Attempt{}, NextRunAt: now.Add(-time.Minute),
		CreatedBy: a.Actor, CreatedAt: now, UpdatedAt: now}
	for _, j := range []models.Job{failed, waiting} {
		if err := a.DB.Insert(ctx, store.CJobs, j); err != nil {
			t.Fatal(err)
		}
	}
	s.failedJob = failed.ID
	notify.RenderOutcome(ctx, a, rnd.ID)
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, ws, s.interview, &as); err != nil {
		t.Fatal(err)
	}
	notify.DuplicateSkipped(ctx, a, &as, "interview-a copy.mov", "cli")
	return s
}

func TestPlainOutputSpeaksPlainly(t *testing.T) {
	a := testapp.New(t)
	home := a.Cfg.Home()
	s := seedPlain(t, a)
	seedRemotionPlain(t, a, s.project)
	ws := a.Cfg.Workspace.ID
	cases := []struct {
		args  []string
		allow plainwords.Allow // what this command may say on purpose (with a reason)
	}{
		{args: []string{"project", "list"}},
		{args: []string{"project", "show", s.project}},
		{args: []string{"asset", "list", s.project}},
		{args: []string{"asset", "show", s.interview}},
		{args: []string{"footage", "transcript", s.interview}},
		{args: []string{"footage", "speakers", "list", s.interview}},
		{args: []string{"footage", "evidence", s.interview}},
		{args: []string{"footage", "moments", s.project}},
		{args: []string{"revision", "list", s.project}},
		{args: []string{"revision", "show", s.rev2}},
		{args: []string{"revision", "compare", s.rev1, s.rev2, "--project", s.project}},
		{args: []string{"edit", "check", "--plan", s.plan}},
		{args: []string{"edit", "check", "--plan", s.badPlan}},
		{args: []string{"edit", "preview", "--plan", s.plan}},
		{args: []string{"edit", "apply", "--plan", s.plan}},
		{args: []string{"edit", "templates"}},
		{args: []string{"edit", "transitions"}},
		{args: []string{"cut", "show", s.project, "--out", s.sheet}},
		{args: []string{"cut", "check", s.sheet}},
		{args: []string{"cut", "move", s.project, "hook", "start"}},
		{args: []string{"cut", "trim", s.project, "hook", "--end", "-0.5"}},
		{args: []string{"cut", "remove-words", s.project, "leaned over the fence"}},
		{args: []string{"cut", "transition", s.project, "hook", "dissolve", "0.6"}},
		{args: []string{"voiceover", "script", "import", s.script, "--project", s.project}},
		{args: []string{"voiceover", "script", "show", "--project", s.project}},
		{args: []string{"voiceover", "fit", "--project", s.project}},
		{args: []string{"mix", "show", "--project", s.project}},
		{args: []string{"sound", "show", "--project", s.project}},
		{args: []string{"sound", "levels", "--project", s.project, "--part", "music", "--gain", "-3"}},
		{args: []string{"conversation", "list"}},
		{args: []string{"conversation", "show", "Garden call"}},
		{args: []string{"asset", "show", s.call}},
		{args: []string{"footage", "transcript", s.call}},
		{args: []string{"render", "list", "--revision", s.rev2}},
		{args: []string{"render", "status", s.render}},
		{args: []string{"qa", "show", s.render}},
		{args: []string{"job", "show", s.failedJob}},
		{args: []string{"job", "list"}},
		{args: []string{"activity"}},
		{args: []string{"todos", "list"}},
		{args: []string{"notifications", "list"}},
		{args: []string{"log", "list"}},
		{args: []string{"search", "neighbor"}},
		{args: []string{"library"}},
		{args: []string{"collection", "list"}},
		{args: []string{"trash", "list"}},
		{args: []string{"people", "list"}},
		{args: []string{"resources", "list", "--project", s.project}},
		{args: []string{"brand", "show"}},
		{args: []string{"workspace", "show"}},
		{args: []string{"worker", "list"}},
		{args: []string{"worker", "settings"}},
		{args: []string{"watch", "status"}},
		{args: []string{"cache", "status"}},
		{args: []string{"sync", "status"}},
		{args: []string{"storage", "status"}},
		{args: []string{"renderer", "status"}},
		{args: []string{"remote", "status"}},
		{args: []string{"providers", "status", "elevenlabs"}},
		{args: []string{"assistant", "status"}},
		{args: []string{"account", "preferences"}},
		{args: []string{"motion", "list"}},
		{args: []string{"remotion", "list"}},
		{args: []string{"remotion", "show", "teaser", "--project", s.project}},
		{args: []string{"remotion", "show", "teaser@2", "--project", s.project}},
		// Recipes (D201–D207): a proposal, a draft saved from a script, the list, one recipe, a plan, runs.
		{args: []string{"recipe", "propose", "--from", s.project}},                         // metarush recipe propose
		{args: []string{"recipe", "save", "--from", s.project, "--name", "garden-recipe"}}, // metarush recipe save
		{args: []string{"recipe", "list"}},                                                 // metarush recipe list
		{args: []string{"recipe", "show", "garden-recipe"}},                                // metarush recipe show
		{args: []string{"recipe", "check", "garden-recipe"}},                               // metarush recipe check
		{args: []string{"recipe", "plan", "garden-recipe"}},                                // metarush recipe plan
		{args: []string{"recipe", "runs"}},                                                 // metarush recipe runs
		{args: []string{"recipe", "run", "garden-recipe"}},                                 // metarush recipe run
		{args: []string{"remotion", "pictures", "list", "teaser", "--project", s.project}},
		{args: []string{"version"}},
		// Finding what does a task (D221): the commands' own help lines, the names and commands behind --details.
		{args: []string{"ops", "find", "make", "a", "review", "copy"}},
		{args: []string{"ops", "find", "turn", "the", "music", "down"}},
		{args: []string{"ops", "describe", "footage.moments"}},
		{args: []string{"ops", "describe", "revision.approve"}},
		{args: []string{"skills", "eval"}}, // the tool-selection eval (D223), no model
		// The menu of abilities (D272): what works here and what each needs.
		{args: []string{"catalogue", "list"}},
		{args: []string{"catalogue", "list", "--works-now", "--group", "understand"}},
		{args: []string{"catalogue", "show", "steady-footage"}},
		{args: []string{"ops", "find", "steady", "my", "shaky", "footage"}},
		// Errors read as plain words too.
		{args: []string{"project", "show", ids.New(ids.Project)}},
		{args: []string{"qa", "show", ids.New(ids.Render)}},
		{args: []string{"job", "show", ids.New(ids.Job)}},
		{args: []string{"asset", "show", "no-such-clip"}},
		{args: []string{"edit", "apply", "--plan", s.plan, "--confirm", "ct_1_00"}},
		{args: []string{"deliver", "--revision", s.rev2, "--out", t.TempDir()}},
	}
	var dump strings.Builder
	for _, tc := range cases {
		r := runCLI(t, home, tc.args...)
		out := r.stdout + r.stderr
		fmt.Fprintf(&dump, "$ metarush %s   (exit %d)\n%s\n", strings.Join(tc.args, " "), r.code, out)
		if strings.Contains(out, "panic") {
			t.Errorf("metarush %s panicked:\n%s", strings.Join(tc.args, " "), out)
			continue
		}
		var bad []string
		for _, f := range tc.allow.Filter(plainAllow.Filter(plainwords.CheckShown(out))) {
			bad = append(bad, f.String())
		}
		if len(bad) > 0 {
			t.Errorf("metarush %s — plain output a person should not have to read: %s\n%s", strings.Join(tc.args, " "), strings.Join(bad, "; "), indentOut(out))
		}
	}
	if p := os.Getenv("PLAINGUARD_DUMP"); p != "" {
		_ = os.WriteFile(p, []byte(dump.String()), 0o644)
	}

	// The detail is still there for those who ask: --details shows it…
	for _, d := range []struct {
		args []string
		want string
	}{
		{[]string{"revision", "list", s.project, "--details"}, s.rev2},
		{[]string{"edit", "check", "--plan", s.plan, "--details"}, "--confirm ct_"},
		{[]string{"job", "show", s.failedJob, "--details"}, "sherpa-onnx-offline"},
		{[]string{"qa", "show", s.render, "--details"}, "true_peak_high"},
		{[]string{"project", "show", ids.New(ids.Project), "--details"}, "E_NOT_FOUND"},
		{[]string{"ops", "find", "make", "a", "review", "copy", "--details"}, "metarush render submit"},
		{[]string{"ops", "describe", "footage_moments", "--details"}, "command: metarush footage moments"},
	} {
		if r := runCLI(t, home, d.args...); !strings.Contains(r.stdout+r.stderr, d.want) {
			t.Errorf("metarush %s does not show %q:\n%s", strings.Join(d.args, " "), d.want, indentOut(r.stdout+r.stderr))
		}
	}
	// …and --json is one document with everything, as before.
	for _, args := range [][]string{{"project", "show", s.project}, {"revision", "list", s.project}, {"qa", "show", s.render}, {"activity"}, {"workspace", "show"}} {
		r := runCLI(t, home, append(args, "--json")...)
		var doc struct {
			OK   bool            `json:"ok"`
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || !doc.OK || len(doc.Data) < 3 {
			t.Errorf("metarush %s --json: %v\n%s", strings.Join(args, " "), err, indentOut(r.stdout+r.stderr))
		}
	}
	if r := runCLI(t, home, "workspace", "show", "--json"); !strings.Contains(r.stdout, ws) {
		t.Errorf("workspace show --json lost the workspace ID:\n%s", r.stdout)
	}
}

func indentOut(s string) string {
	return "    " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n    ")
}

// seedRemotionPlain records a Remotion source brought in from GitHub (version 1,
// approved) and an edit Claude checked (version 2, waiting for a person), the
// way remotion import and remotion check leave them (D131–D138).
func seedRemotionPlain(t *testing.T, a *app.App, projectID string) {
	t.Helper()
	ctx := context.Background()
	ws := a.WorkspaceID()
	now := time.Now().UTC()
	h1, h2 := strings.Repeat("a1", 32), strings.Repeat("b2", 32)
	lock := strings.Repeat("c3", 32)
	src := models.RemotionSource{ID: ids.New(ids.RemotionSource), WorkspaceID: ws, ProjectID: projectID, Name: "teaser", Title: "Launch teaser",
		Description: "The product teaser, made in code.",
		Origin:      models.RemotionOrigin{Kind: models.RemotionFromGitHub, URL: "https://github.com/example/launch-teaser", Repo: "example/launch-teaser", Commit: strings.Repeat("5e", 20), Branch: "main"},
		Head:        h2, HeadNumber: 2, Approved: h1, ApprovedNumber: 1, Versions: 2,
		Allowed: []models.RemotionAllowed{{Lock: lock, Version: h1, By: a.Actor, At: now, Import: true}},
		LeftOut: []models.RemotionLeftOut{{Why: "holds secrets (keys, passwords or settings with them)", Count: 1, Examples: []string{".env"}}},
		// Pictures as settings (D209): the hero replaced once.
		Pictures: &models.RemotionPictures{Number: 1, At: now, By: a.Actor, Values: []models.RemotionPictureValue{{Name: "HERO", ResourceID: "res_hero", Resource: "new-hero", Title: "New hero",
			SHA256: strings.Repeat("d4", 32), Ext: "png", Size: 2048, Width: 800, Height: 800}},
			Changes: []models.RemotionPicturesChange{{Number: 1, At: now, By: a.Actor, Summary: "HERO: the code's own picture became “New hero”. The new pictures show where the old ones were."}}},
		CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	comps := []models.RemotionComposition{
		{ID: "Intro", Width: 1920, Height: 1080, FPS: 30, DurationInFrames: 120, Duration: 4, File: "src/Root.tsx", Line: 12, Component: "Intro", Files: []string{"src/Intro.tsx", "src/story.ts"},
			Pictures: []string{"HERO", "LOGO"}},
		{ID: "Card", Width: 1080, Height: 1920, FPS: 30, DurationInFrames: 45, Duration: 1.5, Folder: "Cards", Computed: true, DefaultProps: `{"headline":"Ship faster"}`},
	}
	pk := []models.RemotionPackage{{Name: "remotion", Version: "4.0.535", Kind: "core"}, {Name: "zod", Version: "4.5.4", Kind: "extra", Used: true}}
	read := &models.RemotionRead{At: now, Machine: "Studio Mac", OK: true, Seconds: 12}
	v1 := models.RemotionVersion{ID: ws + "/" + h1, WorkspaceID: ws, SourceID: src.ID, ProjectID: projectID, Hash: h1, Number: 1, Kind: models.RemotionKindImport,
		Message: "Brought in from github.com/example/launch-teaser", By: a.Actor, At: now, Files: 12, Bytes: 40 << 10, PublicFiles: 3, PublicBytes: 2 << 20, Lock: lock,
		Remotion: "4.0.535", Packages: pk, PackagesAllowed: true, Entry: "src/index.ts", Compositions: comps, Read: read, State: models.RemotionApproved,
		Config:    []models.RemotionConfigNote{{Setting: "setVideoImageFormat", Value: "jpeg", Applied: true, Note: "How each frame is captured before it is encoded; MetaRush uses the same."}},
		Decisions: []models.RemotionDecision{{State: models.RemotionApproved, By: a.Actor, At: now}}, ApprovedBy: &a.Actor, ApprovedAt: &now, CreatedAt: now, UpdatedAt: now,
		PicturesHelper: "src/metarush-pictures.ts", PicturesRead: true, Pictures: []models.RemotionPicture{
			{Name: "HERO", File: "public/hero.png", Setting: true, Uses: []models.RemotionPictureUse{{File: "src/Intro.tsx", Line: 9, How: models.RemotionPictureStatic, Wrapped: true}}},
			{Name: "LOGO", File: "src/art/logo.svg", Setting: true, Uses: []models.RemotionPictureUse{{File: "src/theme.ts", Line: 2, How: models.RemotionPictureImport, Wrapped: true}}},
			{Name: "CREW", File: "public/crew.jpg", Uses: []models.RemotionPictureUse{{File: "src/Intro.tsx", Line: 14, How: models.RemotionPictureImport, Why: "its import brings more than the picture"}}}},
		PicturesLeft: []models.RemotionPictureUse{{File: "src/Cast.tsx", Line: 7, How: models.RemotionPictureCode, Code: "staticFile(`cast/${id}.png`)", Why: "its file is worked out as the video plays"}}}
	v2 := v1
	v2.ID, v2.Hash, v2.Parent, v2.Number, v2.Kind, v2.Message, v2.State = ws+"/"+h2, h2, h1, 2, models.RemotionKindEdit, "a shorter headline", models.RemotionWaiting
	v2.Decisions, v2.ApprovedBy, v2.ApprovedAt, v2.Config = nil, nil, nil, nil
	v2.Check = &models.RemotionCheck{At: now, By: a.Actor, Machine: "Studio Mac", OK: true, Seconds: 31,
		Summary:  "Version 2 of “Launch teaser” (“a shorter headline”): “Ship faster than ever” became “Ship faster”. The picture changes in Intro 0:01.0–0:04.0. It waits for a person to approve it.",
		Changes:  []models.RemotionFileChange{{Path: "src/story.ts", Change: "changed", Lines: 2}},
		Words:    []models.RemotionWordChange{{File: "src/story.ts", Line: 3, Before: "Ship faster than ever", After: "Ship faster"}},
		Affected: []string{"Intro"}, Compared: []string{"Intro"}, Changed: []string{"Intro"},
		Moments:  []models.RemotionMoment{{Composition: "Intro", From: 1, To: 4}},
		Problems: []models.RemotionProblem{{Severity: "warning", Code: "types_before", Message: "The project's code has 2 problems the type check finds; this change didn't add any."}}}
	for coll, doc := range map[string]any{store.CRemotionSources: src, store.CRemotionVersions + "/1": v1, store.CRemotionVersions + "/2": v2} {
		if err := a.DB.Insert(ctx, strings.Split(coll, "/")[0], doc); err != nil {
			t.Fatal(err)
		}
	}
}
