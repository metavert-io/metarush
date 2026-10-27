package qa

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// Tests build deliberately broken (and deliberately dark) renders with ffmpeg and
// check the findings against hand-made plans resolved by the real editspec code.

type fakeFacts struct {
	assets map[string]editspec.AssetFacts
	tx     map[string][]editspec.TranscriptSegment
}

func (f *fakeFacts) Asset(id string) (editspec.AssetFacts, bool) { a, ok := f.assets[id]; return a, ok }
func (f *fakeFacts) Transcript(id string) []editspec.TranscriptSegment {
	return f.tx[id]
}

const (
	astA = "ast_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	astB = "ast_bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func facts(audio bool) *fakeFacts {
	return &fakeFacts{assets: map[string]editspec.AssetFacts{
		astA: {ID: astA, Name: "a.mov", State: "available", InProject: true, Duration: 60, HasVideo: true, HasAudio: audio, Width: 1920, Height: 1080, FPS: 30},
		astB: {ID: astB, Name: "b.mov", State: "available", InProject: true, Duration: 60, HasVideo: true, HasAudio: audio, Width: 1920, Height: 1080, FPS: 30},
	}, tx: map[string][]editspec.TranscriptSegment{}}
}

func toolset(t *testing.T) *tools.Set {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) || !ts.Has(tools.FFprobe) {
		t.Skip("ffmpeg/ffprobe not installed")
	}
	return ts
}

// render writes a synthetic "render": video filter graph on testsrc2 plus an audio expression.
func render(t *testing.T, ts *tools.Set, size string, secs float64, vf, audioExpr string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "render.mp4")
	args := []string{"-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=30:duration=" + ftoa(secs)}
	fc := "[0:v]" + orDefault(vf, "null") + ",format=yuv420p[v]"
	maps := []string{"-map", "[v]"}
	if audioExpr != "" {
		args = append(args, "-f", "lavfi", "-i", "aevalsrc="+audioExpr+":s=48000:d="+ftoa(secs))
		maps = append(maps, "-map", "1:a")
	}
	args = append(args, "-filter_complex", fc)
	args = append(args, maps...)
	args = append(args, "-c:v", "libx264", "-preset", "ultrafast", "-r", "30")
	if audioExpr != "" {
		args = append(args, "-c:a", "aac", "-b:a", "128k")
	}
	args = append(args, "-t", ftoa(secs), out)
	if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
		t.Fatal(err)
	}
	return out
}

func ftoa(v float64) string { b, _ := json.Marshal(v); return string(b) }

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func resolve(t *testing.T, s *editspec.Spec, f *fakeFacts) *editspec.RenderPlan {
	t.Helper()
	editspec.Normalize(s)
	if issues := editspec.Validate(s, f, nil, false); editspec.HasErrors(issues) {
		t.Fatalf("test spec invalid: %+v", issues)
	}
	return editspec.Resolve(editspec.Clone(s), f, "rev_test", "prj_test", "hash")
}

func twoClips(preset string) *editspec.Spec {
	return &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: preset, FPS: 30, Platform: "generic"},
		Clips: []editspec.Clip{{ID: "one", AssetID: astA, In: 0, Out: 3}, {ID: "two", AssetID: astB, In: 10, Out: 13}}}
}

func analyze(t *testing.T, ts *tools.Set, path string, plan *editspec.RenderPlan, spec *editspec.Spec, mod func(*Input)) *models.QAReport {
	t.Helper()
	dir := t.TempDir()
	in := Input{Path: path, Plan: plan, Spec: spec, Scale: 0.5, OutDir: filepath.Join(dir, "out"), Scratch: dir}
	if mod != nil {
		mod(&in)
	}
	rep, err := Analyze(context.Background(), ts, in)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

func kinds(rep *models.QAReport, sev ...string) []string {
	var out []string
	for _, f := range rep.Findings {
		for _, s := range sev {
			if f.Severity == s {
				out = append(out, f.Kind)
			}
		}
	}
	return out
}

func find(rep *models.QAReport, kind string) *models.QAFinding {
	for i := range rep.Findings {
		if rep.Findings[i].Kind == kind {
			return &rep.Findings[i]
		}
	}
	return nil
}

func dump(t *testing.T, rep *models.QAReport) {
	t.Helper()
	for _, f := range rep.Findings {
		t.Logf("%s %-8s %-22s %-9s %s %s", f.ID, f.Severity, f.Kind, f.Basis, f.Timecode, f.Message)
	}
}

// A sine at 0.2 amplitude (mono) measures ≈ −17.7 LUFS: inside generic's −16 ±2.
const okTone = "0.2*sin(2*PI*1000*t)"

func TestCleanRenderPassesWithEvidence(t *testing.T) {
	ts := toolset(t)
	spec := twoClips(editspec.PresetLandscape)
	plan := resolve(t, spec, facts(true))
	path := render(t, ts, "960x540", 6, "", okTone)
	rep := analyze(t, ts, path, plan, spec, nil)
	dump(t, rep)
	if errs := kinds(rep, models.QAError, models.QAWarning); len(errs) != 0 {
		t.Fatalf("clean render has findings: %v", errs)
	}
	m := rep.Measurements
	if m.Frames != 180 || m.ExpectedFrames != 180 || m.Width != 960 || m.ExpectedWidth != 960 || m.VideoCodec != "h264" || !m.HasAudio || !m.ExpectsAudio {
		t.Fatalf("measurements: %+v", m)
	}
	if m.IntegratedLUFS == nil || math.Abs(*m.IntegratedLUFS+17.7) > 1.5 || m.LoudnessTarget.IntegratedLUFS != -16 {
		t.Fatalf("loudness: %v target %+v", m.IntegratedLUFS, m.LoudnessTarget)
	}
	if rep.Verdict != models.QAPass {
		t.Fatalf("verdict %s", rep.Verdict)
	}
	// Visual evidence: an overview sheet, a moments sheet, a frame just after the cut.
	for _, p := range []string{rep.ContactSheet.Path, rep.MomentsSheet.Path} {
		if st, err := os.Stat(p); err != nil || st.Size() < 1000 {
			t.Fatalf("sheet %s missing: %v", p, err)
		}
	}
	cut := false
	for _, f := range rep.Frames {
		if _, err := os.Stat(f.Path); err != nil {
			t.Fatalf("frame %s missing", f.Path)
		}
		if f.Reason == "cut" && math.Abs(f.At-3.067) < 0.05 && strings.Contains(f.Label, "cut→two") {
			cut = true
		}
	}
	if !cut {
		t.Fatalf("no frame at the cut: %+v", rep.Frames)
	}
}

func TestBlackGapMidCutIsAnError(t *testing.T) {
	ts := toolset(t)
	spec := twoClips(editspec.PresetLandscape)
	plan := resolve(t, spec, facts(true))
	path := render(t, ts, "960x540", 6, "drawbox=enable='between(t,1.9,2.6)':color=black:t=fill", okTone)
	rep := analyze(t, ts, path, plan, spec, func(in *Input) {
		in.Source = map[string]SourceEvidence{astA: {Available: true}, astB: {Available: true}}
	})
	dump(t, rep)
	f := find(rep, "black_frames")
	if f == nil || f.Severity != models.QAError || f.From > 1.95 || f.To < 2.55 || len(f.ClipIDs) != 1 || f.ClipIDs[0] != "one" {
		t.Fatalf("expected a black_frames error in clip one around 1.9–2.6: %+v", f)
	}
	if len(f.Frames) == 0 || f.Fix == nil || !strings.Contains(f.Fix.Summary, "the render most likely went wrong") {
		t.Fatalf("finding needs a frame and a fix: %+v", f)
	}
	if rep.Verdict != models.QAFail {
		t.Fatalf("verdict %s", rep.Verdict)
	}
	// The same black, but the source evidence says it is in the footage: a trim suggestion.
	rep = analyze(t, ts, path, plan, spec, func(in *Input) {
		in.Source = map[string]SourceEvidence{astA: {Available: true, Black: []Span{{From: 1.9, To: 2.6}}}, astB: {Available: true}}
	})
	f = find(rep, "source_black")
	if f == nil || f.Severity != models.QAWarning || f.Fix == nil || len(f.Fix.Ops) != 2 || f.Fix.Ops[0].Op != "updateClip" || f.Fix.Ops[1].Op != "insertClip" {
		t.Fatalf("expected a source_black warning with split ops: %+v", f)
	}
	// The suggested ops must apply cleanly to the spec.
	if _, _, err := editspec.ApplyOps(spec, f.Fix.Ops, false); err != nil {
		t.Fatalf("suggested ops do not apply: %v", err)
	}
}

func TestWrongDurationAndSize(t *testing.T) {
	ts := toolset(t)
	spec := twoClips(editspec.PresetLandscape)
	plan := resolve(t, spec, facts(true))
	path := render(t, ts, "1280x720", 5, "", okTone)
	rep := analyze(t, ts, path, plan, spec, nil)
	dump(t, rep)
	if f := find(rep, "duration_mismatch"); f == nil || f.Severity != models.QAError || !strings.Contains(f.Message, "1 second (30 frames) shorter than the cut") {
		t.Fatalf("expected duration_mismatch error: %+v", f)
	}
	if f := find(rep, "dimension_mismatch"); f == nil || f.Severity != models.QAError {
		t.Fatalf("expected dimension_mismatch error: %+v", f)
	}
}

func TestClippedAudio(t *testing.T) {
	ts := toolset(t)
	spec := twoClips(editspec.PresetLandscape)
	plan := resolve(t, spec, facts(true))
	path := render(t, ts, "960x540", 6, "", "clip(3*sin(2*PI*220*t)\\,-1\\,1)")
	rep := analyze(t, ts, path, plan, spec, nil)
	dump(t, rep)
	f := find(rep, "audio_clipping")
	if f == nil || f.Severity != models.QAError || rep.Measurements.TruePeakDBTP == nil || *rep.Measurements.TruePeakDBTP <= 0 {
		t.Fatalf("expected audio_clipping error: %+v tp=%v", f, rep.Measurements.TruePeakDBTP)
	}
	if f.Fix == nil || len(f.Fix.Ops) != 2 {
		t.Fatalf("expected volume ops for both clips: %+v", f.Fix)
	}
	if _, _, err := editspec.ApplyOps(spec, f.Fix.Ops, false); err != nil {
		t.Fatalf("ops do not apply: %v", err)
	}
}

func TestLoudnessTargetFollowsBriefWhenOutputIsGeneric(t *testing.T) {
	r := &run{in: Input{Spec: &editspec.Spec{Output: editspec.Output{Platform: "generic"}}, Platforms: []string{"instagram", "tiktok"}}}
	if tg := r.loudnessTarget(); tg.Platform != "tiktok" || tg.IntegratedLUFS != -14 {
		t.Fatalf("target %+v", tg)
	}
	r.in.Spec.Output.Platform = "youtube"
	if tg := r.loudnessTarget(); tg.Platform != "youtube" {
		t.Fatalf("target %+v", tg)
	}
}

func TestQuietMixSuggestsGain(t *testing.T) {
	ts := toolset(t)
	spec := twoClips(editspec.PresetLandscape)
	plan := resolve(t, spec, facts(true))
	path := render(t, ts, "960x540", 6, "", "0.02*sin(2*PI*1000*t)")
	rep := analyze(t, ts, path, plan, spec, nil)
	f := find(rep, "loudness_low")
	if f == nil || f.Severity != models.QAWarning || f.Fix == nil || len(f.Fix.Ops) == 0 {
		t.Fatalf("expected loudness_low warning with ops: %+v", f)
	}
}

func TestSilenceWhereDialogueIsExpected(t *testing.T) {
	ts := toolset(t)
	spec := twoClips(editspec.PresetLandscape)
	fx := facts(true)
	fx.assets[astA] = func() editspec.AssetFacts { a := fx.assets[astA]; a.TranscriptPrecision = "segment"; return a }()
	fx.tx[astA] = []editspec.TranscriptSegment{{From: 0.3, To: 2.9, Text: "a long sentence that keeps going"}}
	plan := resolve(t, spec, fx)
	if len(plan.DialogueRanges) == 0 {
		t.Fatal("plan has no dialogue ranges")
	}
	path := render(t, ts, "960x540", 6, "", "if(between(t\\,0.8\\,2.6)\\,0\\,"+okTone+")")
	// Segment-level timing (plan.DialogueRanges).
	rep := analyze(t, ts, path, plan, spec, nil)
	dump(t, rep)
	f := find(rep, "silence_in_dialogue")
	if f == nil || f.From < 0.7 || f.To > 2.7 || len(f.ClipIDs) == 0 || f.ClipIDs[0] != "one" {
		t.Fatalf("expected silence_in_dialogue in clip one around 0.8–2.6: %+v", f)
	}
	// Word-level timing: the same gap is an error.
	rep = analyze(t, ts, path, plan, spec, func(in *Input) {
		in.Speech = []Span{{0.3, 0.7}, {0.8, 1.2}, {1.3, 1.9}, {2.0, 2.5}, {2.6, 2.9}}
		in.SpeechPrec = "word"
	})
	if f := find(rep, "silence_in_dialogue"); f == nil || f.Severity != models.QAError {
		t.Fatalf("expected an error with word timing: %+v", f)
	}
	// Speech between pauses, no dropout: nothing flagged.
	clean := render(t, ts, "960x540", 6, "", okTone)
	rep = analyze(t, ts, clean, plan, spec, nil)
	if f := find(rep, "silence_in_dialogue"); f != nil {
		t.Fatalf("false positive: %+v", f)
	}
}

func TestFrozenFrames(t *testing.T) {
	ts := toolset(t)
	spec := twoClips(editspec.PresetLandscape)
	plan := resolve(t, spec, facts(true))
	path := render(t, ts, "960x540", 6, "split[a][b];[a][b]freezeframes=first=30:last=105:replace=30", okTone)
	rep := analyze(t, ts, path, plan, spec, func(in *Input) {
		in.Source = map[string]SourceEvidence{astA: {Available: true}, astB: {Available: true}}
	})
	dump(t, rep)
	f := find(rep, "frozen_frames")
	if f == nil || f.Severity != models.QAError || f.From > 1.1 || f.To < 3.4 {
		t.Fatalf("expected frozen_frames error over 1.0–3.5 (source moves): %+v", f)
	}
	// Without source evidence it is only a warning (could be static content).
	rep = analyze(t, ts, path, plan, spec, nil)
	if f := find(rep, "frozen_frames"); f == nil || f.Severity != models.QAWarning {
		t.Fatalf("expected a warning without source evidence: %+v", f)
	}
	// Static in the source too: info.
	rep = analyze(t, ts, path, plan, spec, func(in *Input) {
		in.Source = map[string]SourceEvidence{astA: {Available: true, Freezes: []Span{{1.0, 3.0}}}, astB: {Available: true, Freezes: []Span{{10, 10.6}}}}
	})
	if f := find(rep, "frozen_frames"); f != nil {
		t.Fatalf("static source should not be a defect: %+v", f)
	}
	if find(rep, "static_source") == nil {
		t.Fatal("expected static_source info")
	}
}

// Planned darkness — a fade-in, a dip to black, a fade-out and a full-frame end
// card — must not be reported as black or frozen defects.
func TestIntentionalDipsAndEndCardsAreNotFlagged(t *testing.T) {
	ts := toolset(t)
	spec := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30, Platform: "generic"},
		Clips: []editspec.Clip{
			{ID: "one", AssetID: astA, In: 0, Out: 3, Transition: &editspec.Transition{Type: "fade", Duration: 0.5}},
			{ID: "two", AssetID: astB, In: 10, Out: 13, Transition: &editspec.Transition{Type: "dip-to-black", Duration: 0.6}},
			{ID: "three", AssetID: astA, In: 20, Out: 23, FadeOut: 0.5},
		},
		Overlays: []editspec.Overlay{{ID: "end", Template: editspec.TplEndCard, At: 7.5, Duration: 1.5, Params: json.RawMessage(`{"headline":"Thanks"}`)}}}
	plan := resolve(t, spec, facts(true))
	if plan.DurationInFrames != 270 {
		t.Fatalf("plan frames %d", plan.DurationInFrames)
	}
	// Synthesize what the renderer draws: black for the fade-in and the dip, then a
	// static dark end card with a small headline block.
	vf := "drawbox=enable='lt(t,0.45)':color=black:t=fill," +
		"drawbox=enable='between(t,2.72,3.28)':color=black:t=fill," +
		"drawbox=enable='gte(t,7.5)':color=0x0B0B0E:t=fill," +
		"drawbox=enable='gte(t,7.5)':x=380:y=250:w=200:h=30:color=white:t=fill"
	path := render(t, ts, "960x540", 9, vf, okTone)
	rep := analyze(t, ts, path, plan, spec, func(in *Input) {
		in.Source = map[string]SourceEvidence{astA: {Available: true}, astB: {Available: true}}
	})
	dump(t, rep)
	for _, f := range rep.Findings {
		switch f.Kind {
		case "black_frames", "flash_frame", "frozen_frames", "source_black":
			t.Fatalf("intentional darkness flagged: %+v", f)
		}
	}
	explained := 0
	for _, b := range append(rep.Measurements.Black, rep.Measurements.Frozen...) {
		t.Logf("detected %.3f–%.3f explained by: %s", b.From, b.To, b.Explained)
		if b.Explained != "" {
			explained++
		}
	}
	if len(rep.Measurements.Black) < 2 || explained < 2 {
		t.Fatalf("expected detected-and-explained dark spans: black=%+v frozen=%+v", rep.Measurements.Black, rep.Measurements.Frozen)
	}
}

func TestLayoutHeuristics(t *testing.T) {
	ts := toolset(t)
	spec := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetVertical, FPS: 30, Platform: "generic"},
		Clips: []editspec.Clip{{ID: "one", AssetID: astA, In: 0, Out: 5, Framing: &editspec.Framing{Mode: "fit"}}, {ID: "two", AssetID: astB, In: 0, Out: 5, Framing: &editspec.Framing{Mode: "fit"}}},
		Captions: &editspec.Captions{Enabled: true, Source: "manual", Position: "bottom", Segments: []editspec.CaptionSegment{
			{From: 0.2, To: 2.0, Text: "the first thing I built"}, {From: 2.1, To: 4.0, Text: "was a terrible robot"},
			{From: 4.1, To: 4.2, Text: "and"}, {From: 6.4, To: 7.8, Text: "nobody saw it"}}},
		Overlays: []editspec.Overlay{
			{ID: "who", Template: editspec.TplLowerThirdClean, At: 0.5, Duration: 3, Params: json.RawMessage(`{"name":"A builder","title":"Teacher"}`)},
			{ID: "end", Template: editspec.TplEndCard, At: 6, Duration: 2, Params: json.RawMessage(`{"headline":"Build something"}`)},
		}}
	fx := facts(false)
	plan := resolve(t, spec, fx)
	path := render(t, ts, "540x960", 10, "", "")
	rep := analyze(t, ts, path, plan, spec, func(in *Input) { in.Platforms = []string{"shorts"} })
	dump(t, rep)
	for _, k := range []string{"caption_overlay_stack", "captions_hidden", "platform_safe_area", "caption_flash"} {
		f := find(rep, k)
		if f == nil || f.Basis != models.QAHeuristic || f.Severity != models.QAWarning {
			t.Fatalf("expected heuristic warning %s: %+v", k, f)
		}
		if f.Fix == nil || len(f.Fix.Ops) == 0 {
			t.Fatalf("%s needs fix ops: %+v", k, f.Fix)
		}
		if _, _, err := editspec.ApplyOps(spec, f.Fix.Ops, false); err != nil {
			t.Fatalf("%s ops do not apply: %v", k, err)
		}
	}
	if f := find(rep, "platform_safe_area"); f.Fix.Ops[0].Output.Platform != "shorts" {
		t.Fatalf("platform fix: %+v", f.Fix.Ops[0])
	}
	if rep.Measurements.ExpectsAudio || find(rep, "audio_missing") != nil {
		t.Fatal("muted plan should not expect audio")
	}
}

func TestEndCardThatStopsBeforeTheEnd(t *testing.T) {
	ts := toolset(t)
	spec := twoClips(editspec.PresetLandscape)
	spec.Overlays = []editspec.Overlay{{ID: "end", Template: editspec.TplEndCard, At: 3.5, Duration: 2.2, Params: json.RawMessage(`{"headline":"Thanks"}`)}}
	plan := resolve(t, spec, facts(true))
	path := render(t, ts, "960x540", 6, "", okTone)
	rep := analyze(t, ts, path, plan, spec, nil)
	f := find(rep, "endcard_gap")
	if f == nil || f.Severity != models.QAWarning || f.Fix == nil || len(f.Fix.Ops) != 1 || math.Abs(f.From-5.7) > 0.01 {
		t.Fatalf("expected endcard_gap: %+v", f)
	}
	s, _, err := editspec.ApplyOps(spec, f.Fix.Ops, false)
	if err != nil || math.Abs(s.Overlays[0].At+s.Overlays[0].Duration-6) > 0.001 {
		t.Fatalf("fix does not reach the end: %v %+v", err, s.Overlays)
	}
}

func TestSubtract(t *testing.T) {
	got := subtract(0, 100, []fspan{{10, 20, "a"}, {15, 30, "b"}, {90, 120, "c"}}, 0)
	want := [][2]int{{0, 10}, {30, 90}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("subtract = %v", got)
	}
	if got := subtract(10, 20, []fspan{{11, 19, "x"}}, 2); len(got) != 0 {
		t.Fatalf("tolerance not applied: %v", got)
	}
}
