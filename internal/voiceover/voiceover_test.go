package voiceover

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/worker"
)

type fx struct {
	t       *testing.T
	a       *app.App
	project string
	clip    string // video asset
	dir     string
}

func ffmpeg(t *testing.T, args ...string) {
	t.Helper()
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	if out, err := exec.Command(ts.Path(tools.FFmpeg), append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
}

func setup(t *testing.T) *fx {
	t.Helper()
	keyring.MockInit()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	wsID := ids.New(ids.Workspace)
	cfg.Workspace = config.Ref{ID: wsID, Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "studio"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "Ada"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")}}
	cfg.Secrets = "env"
	cfg.Tools.ModelsDir = filepath.Join(os.Getenv("HOME"), ".local", "share", "metarush-models")
	ctx := context.Background()
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	now := time.Now().UTC()
	_ = a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: wsID, WorkspaceID: wsID, Name: "T", CreatedAt: now})
	pid := ids.New(ids.Project)
	_ = a.DB.Insert(ctx, store.CProjects, models.Project{ID: pid, WorkspaceID: wsID, Name: "Film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now})
	// Film's Content Settings are the default (D192), like a workspace's first project: the voice is chosen there.
	if _, err := store.MoveContentSettings(ctx, a.DB, wsID); err != nil {
		t.Fatal(err)
	}
	f := &fx{t: t, a: a, project: pid, dir: t.TempDir()}
	video := filepath.Join(f.dir, "shot.mp4")
	ffmpeg(t, "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30", "-f", "lavfi", "-i", "sine=frequency=550:sample_rate=48000", "-t", "12",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", video)
	r := ingest.File(ctx, a, video, ingest.Options{ProjectID: pid, Via: "cli"})
	if r.AssetID == "" {
		t.Fatalf("ingest: %+v", r)
	}
	f.clip = r.AssetID
	return f
}

// apply checks and applies a plan, returning the new revision ID.
func (f *fx) apply(plan editspec.Plan) string {
	f.t.Helper()
	ctx := context.Background()
	raw, _ := json.Marshal(plan)
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, f.a, "edit.check", ops.EditCheckIn{Plan: raw}, ops.Caller{})
	if err != nil {
		f.t.Fatal(err)
	}
	if !chk.Valid {
		f.t.Fatalf("plan invalid: %+v", chk.Errors)
	}
	out, err := ops.CallTyped[ops.EditApplyOut](ctx, f.a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: chk.ConfirmToken}, ops.Caller{})
	if err != nil {
		f.t.Fatal(err)
	}
	return out.Revision.ID
}

func (f *fx) applyResult(res *PlanResult) string {
	f.t.Helper()
	if res == nil {
		f.t.Fatal("no plan")
	}
	if !res.Check.Valid {
		f.t.Fatalf("generated plan invalid: %+v", res.Check.Errors)
	}
	return f.apply(res.Plan)
}

// cut makes a two-shot cut with a three-line script.
func (f *fx) cut(lines ...string) string {
	spec := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
		Clips: []editspec.Clip{{ID: "open", AssetID: f.clip, In: 0, Out: 5, Label: "opening"}, {ID: "close", AssetID: f.clip, In: 6, Out: 12, Label: "closing"}}}
	if len(lines) > 0 {
		v := &editspec.Voiceover{}
		for i, l := range lines {
			an := editspec.Anchor{Kind: editspec.AnchorAfter}
			if i == 0 {
				an = editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "open", Offset: 0.3}
			}
			v.Beats = append(v.Beats, editspec.Beat{ID: "line-" + string(rune('1'+i)), Text: l, Anchor: an})
		}
		spec.Voiceover = v
	}
	return f.apply(editspec.Plan{ProjectID: f.project, Instruction: "test cut", Spec: spec})
}

func call[O any](t *testing.T, a *app.App, name string, in any) (*O, error) {
	t.Helper()
	return ops.CallTyped[O](context.Background(), a, name, in, ops.Caller{Human: true})
}

func (f *fx) spec(rev string) *editspec.Spec {
	var r models.Revision
	if err := f.a.DB.GetByID(context.Background(), store.CRevisions, f.a.WorkspaceID(), rev, &r); err != nil {
		f.t.Fatal(err)
	}
	return &r.Spec
}

func (f *fx) plan(rev string) *editspec.RenderPlan {
	out, err := call[editspec.RenderPlan](f.t, f.a, "revision.plan", ops.RevisionPlanIn{ID: rev})
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func TestParseShowAndFit(t *testing.T) {
	f := setup(t)
	rev := f.cut()
	out, err := call[ParseOut](t, f.a, "voiceover.parse", ParseIn{RevisionID: rev, Text: "[@opening+0.5] (warm) Out here, the light arrives slowly.\n\n[@closing] " + strings.Repeat("many words ", 20) + "end."})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Beats) != 2 || out.Result == nil || !out.Result.Check.Valid {
		t.Fatalf("parse: %+v", out)
	}
	rev2 := f.applyResult(out.Result)
	sh, err := call[ShowOut](t, f.a, "voiceover.show", RevisionIn{RevisionID: rev2})
	if err != nil {
		t.Fatal(err)
	}
	if len(sh.Beats) != 2 || sh.Beats[0].Placement != "0.5 s after shot 1 (“opening”) starts" || sh.Beats[0].Start != 0.5 || sh.Beats[1].TakeLabel != "no audio yet" {
		t.Fatalf("show: %+v", sh.Beats)
	}
	// Line 2 has 41 words at 155 wpm (~16 s) but only 6 s before the end.
	fit, err := call[FitOut](t, f.a, "voiceover.fit", FitIn{RevisionID: rev2})
	if err != nil || fit.Over != 1 || !strings.Contains(fit.Lines[1].Message, "41 words") {
		t.Fatalf("fit: %+v %v", fit, err)
	}
	fixed, err := call[FitOut](t, f.a, "voiceover.fit", FitIn{RevisionID: rev2, Strategy: "preferred"})
	if err != nil || len(fixed.Rewrite) != 1 || fixed.Result != nil {
		t.Fatalf("an unrecorded line that runs over is rewritten, not stretched: %+v %v", fixed, err)
	}
}

func TestScratchVoice(t *testing.T) {
	f := setup(t)
	if !f.a.Tools.Has(tools.Say) && !f.a.Tools.Has(tools.EspeakNG) {
		_, err := call[GenerateOut](t, f.a, "voiceover.scratch", ScratchIn{RevisionID: f.cut("Hello there.")})
		if !apperr.Is(err, apperr.ToolMissing) {
			t.Fatalf("no scratch voice must be reported honestly: %v", err)
		}
		t.Skip("no scratch voice on this machine")
	}
	rev := f.cut("Out here, the light arrives slowly.", "And then it is everywhere.")
	out, err := call[GenerateOut](t, f.a, "voiceover.scratch", ScratchIn{RevisionID: rev})
	if err != nil {
		t.Fatal(err)
	}
	if out.Generated != 2 || len(out.Takes) != 2 || !out.Takes[0].AIVoice || out.Takes[0].Timing != "estimated" || out.Takes[0].Duration < 1 {
		t.Fatalf("scratch: %+v", out)
	}
	rev2 := f.applyResult(out.Result)
	plan := f.plan(rev2)
	var vo []editspec.PlanAudio
	for _, a := range plan.Audio {
		if a.Bus == editspec.BusVoiceover {
			vo = append(vo, a)
		}
	}
	if len(vo) != 2 || vo[0].From != 9 || plan.Voiceover == nil || !plan.Voiceover.Beats[0].HasAudio {
		t.Fatalf("narration in the render plan: %+v", vo)
	}
	// Unchanged lines are reused.
	again, err := call[GenerateOut](t, f.a, "voiceover.scratch", ScratchIn{RevisionID: rev2, Replace: true})
	if err != nil || again.Reused != 2 || again.Generated != 0 {
		t.Fatalf("cache: %+v %v", again, err)
	}
}

func TestElevenLabsTTSCacheBudgetAndTiming(t *testing.T) {
	f := setup(t)
	key := "sk" + "_" + strings.Repeat("ab12", 10) + "c0de"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	rev := f.cut("Out here, the light arrives slowly.", "And then it is everywhere.")
	// Not configured: an honest message.
	if _, err := call[EstimateOut](t, f.a, "voiceover.estimate", TTSIn{RevisionID: rev}); !apperr.Is(err, apperr.NotConfigured) {
		t.Fatalf("not configured: %v", err)
	}
	if _, err := call[providers.Status](t, f.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}); err != nil {
		t.Fatal(err)
	}
	// No voice chosen yet.
	if _, err := call[EstimateOut](t, f.a, "voiceover.estimate", TTSIn{RevisionID: rev}); !apperr.Is(err, apperr.NotConfigured) {
		t.Fatalf("no voice: %v", err)
	}
	if _, err := call[providers.SettingsOut](t, f.a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, Voice: "Ada"}); err != nil {
		t.Fatal(err)
	}
	est, err := call[EstimateOut](t, f.a, "voiceover.estimate", TTSIn{RevisionID: rev})
	if err != nil {
		t.Fatal(err)
	}
	chars := len("Out here, the light arrives slowly.") + len("And then it is everywhere.")
	if est.Characters != chars || est.Credits != float64(chars) || est.CachedLines != 0 || !est.Allowed || est.Voice.ModelID != "eleven_multilingual_v2" {
		t.Fatalf("estimate: %+v", est)
	}
	// In dollars, line by line (D208): ElevenLabs, the model, the line's words,
	// characters and credits, at the dated price per 1,000 credits.
	if c := est.Cost; len(c.Parts) != 2 || c.Parts[0].Service != "ElevenLabs" || c.Parts[0].Source != "Line 1: “Out here, the light arrives slowly.”" ||
		c.Parts[0].Units != "35 characters (as many credits)" || c.Parts[0].Model == "" || !strings.Contains(c.Parts[0].Rate, "per 1,000 credits") ||
		c.USD != costs.Round(providers.SpeechUSD(35)+providers.SpeechUSD(float64(chars-35))) || c.About != costs.About(c.USD) ||
		!strings.Contains(est.Summary, c.About) || len(c.Notes) == 0 {
		t.Fatalf("the estimate's parts: %+v (summary %q)", c, est.Summary)
	}
	if fake.Calls("tts") != 0 {
		t.Fatal("an estimate must not generate anything")
	}
	out, err := call[GenerateOut](t, f.a, "voiceover.tts", TTSIn{RevisionID: rev})
	if err != nil {
		t.Fatal(err)
	}
	if out.Generated != 2 || fake.Calls("tts") != 2 || out.Credits != float64(chars) || !out.Takes[0].AIVoice || out.Takes[0].Timing != "word" {
		t.Fatalf("generate: %+v", out)
	}
	// Word timing from the alignment is the asset's transcript.
	var segs []models.TranscriptSegment
	_ = f.a.DB.Find(context.Background(), store.CSegments, f.a.WorkspaceID(), bson.M{"assetId": out.Takes[0].AssetID}, &segs)
	if len(segs) != 1 || len(segs[0].Words) != 6 || segs[0].Words[0].Text != "Out" || segs[0].Provider != "elevenlabs-alignment" {
		t.Fatalf("alignment words: %+v", segs)
	}
	rev2 := f.applyResult(out.Result)
	// Captions from the narration use the provider's word timing.
	sp := f.spec(rev2)
	sp.Captions = &editspec.Captions{Enabled: true, Source: "voiceover", Style: editspec.CaptionEmphasis}
	b, _ := json.Marshal(map[string]any{"enabled": true, "source": "voiceover", "style": "emphasis"})
	_ = b
	rev3 := f.apply(editspec.Plan{BaseRevisionID: rev2, Instruction: "captions", Ops: []editspec.Op{{Op: "setCaptions", Captions: sp.Captions}}})
	plan := f.plan(rev3)
	if plan.Captions == nil || plan.Captions.Precision != "word" || len(plan.Captions.Lines) < 2 || plan.Captions.Lines[0].Speaker != "" {
		t.Fatalf("captions: %+v", plan.Captions)
	}
	// Spending is recorded against the project.
	bs, _ := providers.ProjectBudget(context.Background(), f.a, f.project)
	if bs.UsedCredits != float64(chars) {
		t.Fatalf("used credits %.1f", bs.UsedCredits)
	}
	// Unchanged lines are never billed twice.
	// Against the old version: the work is kept and the person is told to use the latest cut.
	stale, err := call[GenerateOut](t, f.a, "voiceover.tts", TTSIn{RevisionID: rev})
	if err != nil || stale.Result != nil || len(stale.Warnings) == 0 || fake.Calls("tts") != 2 {
		t.Fatalf("stale base: %+v %v", stale, err)
	}
	again, err := call[GenerateOut](t, f.a, "voiceover.tts", TTSIn{RevisionID: rev3, Beats: []string{"1", "2"}})
	if err != nil || again.Reused != 2 || again.Generated != 0 || fake.Calls("tts") != 2 {
		t.Fatalf("cache: %+v %v (tts calls %d)", again, err, fake.Calls("tts"))
	}
	// Made before: the estimate's parts are free.
	if made, err := call[EstimateOut](t, f.a, "voiceover.estimate", TTSIn{RevisionID: rev3, Beats: []string{"1", "2"}}); err != nil || !made.Cost.AllFree() ||
		made.Cost.USD != 0 || made.Cost.About != "nothing (made before)" {
		t.Fatalf("an estimate of lines made before: %+v %v", made, err)
	}
	// A setting change is a new generation (and costs again).
	sp1 := 1.1
	again, err = call[GenerateOut](t, f.a, "voiceover.tts", TTSIn{RevisionID: rev3, Speed: &sp1, Beats: []string{"1"}})
	if err != nil || again.Generated != 1 || fake.Calls("tts") != 3 {
		t.Fatalf("speed change: %+v %v", again, err)
	}
	if fake.TTS[2].Settings["speed"] != 1.1 {
		t.Fatalf("speed not sent: %+v", fake.TTS[2].Settings)
	}
	// Budget: refused before anything is spent.
	if _, err := call[providers.SettingsOut](t, f.a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, ProjectID: f.project, BudgetCredits: fptr(70)}); err != nil {
		t.Fatal(err)
	}
	sp2 := 0.9
	_, err = call[GenerateOut](t, f.a, "voiceover.tts", TTSIn{RevisionID: rev3, Speed: &sp2})
	if !apperr.Is(err, apperr.Budget) || fake.Calls("tts") != 3 {
		t.Fatalf("budget: %v (tts calls %d)", err, fake.Calls("tts"))
	}
	est, _ = call[EstimateOut](t, f.a, "voiceover.estimate", TTSIn{RevisionID: rev3, Speed: &sp2})
	if est.Allowed || len(est.Warnings) == 0 || !strings.Contains(est.Warnings[len(est.Warnings)-1], "budget") {
		t.Fatalf("estimate over budget: %+v", est)
	}
	// Two narrators: a line's own voice wins.
	sp3 := f.spec(rev2)
	set, _ := json.Marshal(map[string]any{"voice": editspec.VoiceRef{Provider: providers.ElevenLabsName, VoiceID: "voice_bram"}})
	_ = sp3
	rev4 := f.apply(editspec.Plan{BaseRevisionID: rev3, Instruction: "second narrator", Ops: []editspec.Op{{Op: "updateBeat", ID: "line-2", Set: set}}})
	if _, err := call[providers.SettingsOut](t, f.a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, ProjectID: f.project, BudgetCredits: fptr(0)}); err != nil {
		t.Fatal(err)
	}
	gen, err := call[GenerateOut](t, f.a, "voiceover.tts", TTSIn{RevisionID: rev4, Beats: []string{"line-2"}, Regenerate: true})
	if err != nil || gen.Generated != 1 || fake.TTS[len(fake.TTS)-1].VoiceID != "voice_bram" {
		t.Fatalf("per-line voice: %+v %v", gen, err)
	}
}

func fptr(v float64) *float64 { return &v }

// A recording made from a known script (macOS say) is transcribed locally,
// matched to the script, split at the lines and placed at their anchors.
func TestImportedRecordingAlignsToScript(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("uses macOS say to make the recording")
	}
	f := setup(t)
	if !f.a.Tools.Has(tools.Say) {
		t.Skip("say not installed")
	}
	if transcribe.Select(f.a.Cfg.Transcription, f.a.Tools) == nil {
		t.Skip("no local transcriber installed")
	}
	script := []string{"We came for the northern lights.", "And we stayed for the silence.", "Every morning starts in the dark."}
	rev := f.cut(script...)
	// One continuous recording with pauses between the lines.
	txt := filepath.Join(f.dir, "vo.txt")
	_ = os.WriteFile(txt, []byte(strings.Join(script, " [[slnc 1200]] ")), 0o600)
	wav := filepath.Join(f.dir, "narration.wav")
	if out, err := exec.Command(f.a.Tools.Path(tools.Say), "-v", "Samantha", "-r", "170", "-f", txt, "-o", wav, "--data-format=LEI16@22050").CombinedOutput(); err != nil {
		t.Skipf("say: %v %s", err, out)
	}
	ctx := context.Background()
	res := ingest.File(ctx, f.a, wav, ingest.Options{ProjectID: f.project, Via: "cli", Transcribe: true, Tags: []string{"voiceover"}})
	if res.AssetID == "" {
		t.Fatalf("ingest: %+v", res)
	}
	runJobs(t, f.a, res.JobIDs)
	out, err := call[AlignOut](t, f.a, "voiceover.align", AlignIn{RevisionID: rev, AssetID: res.AssetID})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Takes) != 3 || out.Coverage < 0.9 || out.MeasuredWPM < 130 || out.MeasuredWPM > 230 {
		t.Fatalf("alignment: %+v", out)
	}
	for i := 1; i < 3; i++ {
		if out.Lines[i].From <= out.Lines[i-1].To-0.05 {
			t.Fatalf("segments overlap or are out of order: %+v", out.Lines)
		}
	}
	rev2 := f.applyResult(out.Result)
	sp := f.spec(rev2)
	if sp.Voiceover.Settings == nil || sp.Voiceover.Settings.RateSource != "measured" {
		t.Fatalf("measured rate not stored: %+v", sp.Voiceover.Settings)
	}
	plan := f.plan(rev2)
	// Line 1 sits at its anchor (0.3 s): its piece of the recording starts there.
	var first *editspec.PlanAudio
	for i := range plan.Audio {
		if plan.Audio[i].BeatID == "line-1" {
			first = &plan.Audio[i]
		}
	}
	if first == nil || first.From != 9 || first.TrimBefore != int(math.Round(out.Lines[0].From*30)) {
		t.Fatalf("line 1 placement: %+v", first)
	}
	// Cleanup makes a second version; the line switches to it and can switch back.
	cl, err := call[CleanupOut](t, f.a, "voiceover.cleanup", CleanupIn{RevisionID: rev2})
	if err != nil {
		t.Fatal(err)
	}
	if len(cl.Lines) != 3 || cl.Result == nil || !cl.Result.Check.Valid || cl.Lines[0].AssetID == res.AssetID {
		t.Fatalf("cleanup: %+v", cl)
	}
	rev3 := f.applyResult(cl.Result)
	sp = f.spec(rev3)
	if tk := sp.Voiceover.Beats[0].Take; tk.Processed == nil || !tk.UseProcessed || tk.Processed.Preset != "voice-clean" {
		t.Fatalf("processed ref: %+v", tk)
	}
	ab, err := call[PlanResult](t, f.a, "voiceover.choose", ChooseIn{RevisionID: rev3, Beat: "1", Version: "original"})
	if err != nil || !ab.Check.Valid {
		t.Fatalf("A/B: %+v %v", ab, err)
	}
	rev4 := f.apply(ab.Plan)
	if tk := f.spec(rev4).Voiceover.Beats[0].Take; tk.UseProcessed || tk.Processed == nil {
		t.Fatalf("switch back to the original: %+v", tk)
	}
	// The cleaned-up file is quieter in noise and normalized: it plays from its own asset.
	if p := f.plan(rev3); !strings.Contains(strings.Join(func() []string {
		var ids []string
		for _, a := range p.Audio {
			ids = append(ids, a.AssetID)
		}
		return ids
	}(), ","), cl.Lines[0].AssetID) {
		t.Fatal("the render plan does not play the cleaned-up version")
	}
}

// runJobs runs queued jobs in this process (like `--wait` with no worker online).
func runJobs(t *testing.T, a *app.App, jobIDs []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	for _, id := range jobIDs {
		j, err := a.Queue.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if j.Type != "transcribe" {
			continue
		}
		w := worker.New(a)
		w.OnlyJobID, w.IgnoreBattery, w.AcceptsHeavy = id, true, true
		w.Log = func(string, ...any) {}
		if err := w.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if j, _ = a.Queue.Get(ctx, id); j.State != models.JobSucceeded {
			t.Fatalf("job %s: %s %s", id, j.State, j.Error)
		}
	}
}

func TestCloneConsentGate(t *testing.T) {
	f := setup(t)
	key := "sk" + "_" + strings.Repeat("cd34", 10) + "beef"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	if _, err := call[providers.Status](t, f.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}); err != nil {
		t.Fatal(err)
	}
	sample := filepath.Join(f.dir, "me.wav")
	_ = os.WriteFile(sample, fakeeleven.SpeechWAV(3, 200, 22050, func(float64) bool { return true }), 0o600)
	r := ingest.File(context.Background(), f.a, sample, ingest.Options{ProjectID: f.project, Via: "cli"})
	ctx := context.Background()
	in := CloneIn{Name: "My voice", Samples: []string{r.AssetID}, OwnVoice: true}
	// People only.
	if _, err := ops.CallTyped[CloneOut](ctx, f.a, "voiceover.clone", in, ops.Caller{}); !apperr.Is(err, apperr.HumanOnly) {
		t.Fatalf("clone must be human-only: %v", err)
	}
	if _, err := call[CloneOut](t, f.a, "voiceover.clone", in); !apperr.Is(err, apperr.ConsentRequired) {
		t.Fatalf("no consent statement: %v", err)
	}
	in.Consent = "I, Ada, agree to an AI copy of my voice being made for my own videos."
	other := in
	other.OwnVoice, other.Speaker = false, "Grace"
	if _, err := call[CloneOut](t, f.a, "voiceover.clone", other); !apperr.Is(err, apperr.ConsentRequired) {
		t.Fatalf("someone else's voice without their recorded consent: %v", err)
	}
	if len(fake.Clones) != 0 {
		t.Fatal("nothing may reach the provider before consent")
	}
	out, err := call[CloneOut](t, f.a, "voiceover.clone", in)
	if err != nil || out.VoiceID == "" || len(fake.Clones) != 1 || fake.Clones[0].Files != 1 {
		t.Fatalf("clone: %+v %v", out, err)
	}
	var evs []models.AuditEvent
	_ = f.a.DB.Find(ctx, store.CAudit, f.a.WorkspaceID(), bson.M{"action": "voiceover.clone"}, &evs)
	if len(evs) != 1 || evs[0].Details["consent"] != in.Consent || evs[0].Details["ownVoice"] != true {
		t.Fatalf("consent not in the audit log: %+v", evs)
	}
	// With the person's recorded consent, another voice is allowed.
	other.ConsentFrom = r.AssetID
	if _, err := call[CloneOut](t, f.a, "voiceover.clone", other); err != nil {
		t.Fatalf("recorded consent: %v", err)
	}
}

func TestVoicesModelsAndLocal(t *testing.T) {
	f := setup(t)
	key := "sk" + "_" + strings.Repeat("ef56", 10) + "f00d"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	if _, err := call[VoicesOut](t, f.a, "voiceover.voices", VoicesIn{}); !apperr.Is(err, apperr.NotConfigured) {
		t.Fatalf("voices without a key: %v", err)
	}
	if _, err := call[providers.Status](t, f.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}); err != nil {
		t.Fatal(err)
	}
	_, _ = call[providers.SettingsOut](t, f.a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, Favorite: "voice_cleo"})
	vs, err := call[VoicesOut](t, f.a, "voiceover.voices", VoicesIn{})
	if err != nil || len(vs.Voices) != 3 || vs.Voices[0].Name != "Cleo" || !vs.Voices[0].Favorite || vs.Voices[1].Traits != "female · british · middle aged · warm · narration" {
		t.Fatalf("voices (favorites first, plain traits): %+v %v", vs, err)
	}
	lib, err := call[VoicesOut](t, f.a, "voiceover.voices", VoicesIn{Library: true, Search: "dawn"})
	if err != nil || len(lib.Voices) != 1 || !lib.Voices[0].Library || lib.Voices[0].OwnerID == "" {
		t.Fatalf("library: %+v %v", lib, err)
	}
	ms, err := call[ModelsOut](t, f.a, "voiceover.models", ops.EmptyIn{})
	if err != nil || len(ms.Models) != 2 || ms.Default != "eleven_multilingual_v2" {
		t.Fatalf("models (text-to-speech only, default from the live list): %+v %v", ms, err)
	}
	b, ct, err := PreviewAudio(context.Background(), f.a, providers.ElevenLabsName, "voice_ada", false)
	if err != nil || ct != "audio/wav" || len(b) < 1000 || fake.Calls("preview") != 1 {
		t.Fatalf("preview: %v %s %d", err, ct, len(b))
	}
	if f.a.Tools.Has(tools.Say) || f.a.Tools.Has(tools.EspeakNG) {
		lv, err := call[VoicesOut](t, f.a, "voiceover.voices", VoicesIn{Provider: "local"})
		if err != nil || len(lv.Voices) == 0 || lv.Provider != "local" {
			t.Fatalf("local voices: %+v %v", lv, err)
		}
	}
}
