package qa

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// fakeTx returns fixed words: speech continuity logic without a speech model.
type fakeTx struct{ words []models.Word }

func (f fakeTx) Name() string  { return "fake" }
func (f fakeTx) Model() string { return "fixed" }
func (f fakeTx) Transcribe(context.Context, string, string, string) (*transcribe.Transcript, error) {
	return &transcribe.Transcript{Segments: []transcribe.Segment{{Words: f.words}}}, nil
}

func wordsAt(text string, start, per float64) []models.Word {
	var out []models.Word
	t := start
	for _, w := range strings.Fields(text) {
		out = append(out, models.Word{Text: w, From: t, To: t + per*0.8})
		t += per
	}
	return out
}

func toTx(ws []models.Word) []editspec.TranscriptWord {
	var out []editspec.TranscriptWord
	for _, w := range ws {
		out = append(out, editspec.TranscriptWord{Text: w.Text, From: w.From, To: w.To})
	}
	return out
}

// speechPlan: one clip (source 0–6 s) whose transcript is the given words.
func speechPlan(t *testing.T, words []models.Word) (*editspec.Spec, *editspec.RenderPlan, *fakeFacts) {
	f := facts(true)
	f.assets[astA] = editspec.AssetFacts{ID: astA, Name: "talk.mov", State: "available", InProject: true, Duration: 60, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, TranscriptPrecision: "word"}
	f.tx[astA] = []editspec.TranscriptSegment{{From: words[0].From, To: words[len(words)-1].To, Words: toTx(words)}}
	s := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30}, Clips: []editspec.Clip{{ID: "talk", AssetID: astA, In: 0, Out: 6}}}
	return s, resolve(t, s, f), f
}

func continuityInput(spec *editspec.Spec, f *fakeFacts, tx Transcriber) func(*Input) {
	return func(in *Input) {
		in.ExpectedSpeech, in.UnknownSpeech, in.SpeechEdges = editspec.ExpectedSpeech(spec, f)
		in.Transcriber = tx
	}
}

func TestSpeechContinuityFindings(t *testing.T) {
	ts := toolset(t)
	planned := wordsAt("we came for the northern lights and stayed for the silence", 0.5, 0.42)
	spec, plan, f := speechPlan(t, planned)
	path := render(t, ts, "640x360", 6, "", okTone)
	// Clean: what is heard matches the plan (with a filler and a transcription slip).
	heard := append(wordsAt("we came for", 0.5, 0.42), models.Word{Text: "um", From: 1.75, To: 1.8})
	heard = append(heard, wordsAt("the northen lights and stayed for the silence", 1.76, 0.42)...)
	rep := analyze(t, ts, path, plan, spec, continuityInput(spec, f, fakeTx{heard}))
	for _, k := range []string{"speech_repeated", "speech_missing", "speech_unexpected"} {
		if find(rep, k) != nil {
			dump(t, rep)
			t.Fatalf("clean speech flagged %s", k)
		}
	}
	if rep.Measurements.SpeechWordsExpected != 11 || rep.Measurements.SpeechWordsMatched != 11 {
		t.Fatalf("measurements: %+v", rep.Measurements)
	}
	// A word heard twice (a glitched slice).
	glitch := append(wordsAt("we came for the northern", 0.5, 0.42), models.Word{Text: "northern", From: 2.6, To: 2.9})
	glitch = append(glitch, wordsAt("lights and stayed for the silence", 3.0, 0.42)...)
	rep = analyze(t, ts, path, plan, spec, continuityInput(spec, f, fakeTx{glitch}))
	rf := find(rep, "speech_repeated")
	if rf == nil || rf.Severity != models.QAWarning || rf.Basis != models.QAObserved || !strings.HasPrefix(rf.Message, "Around 0:02 a word from the “talk” clip repeats in the render") {
		dump(t, rep)
		t.Fatalf("repeated word: %+v", rf)
	}
	// A whole phrase dropped is an error.
	dropped := wordsAt("we came for the northern lights", 0.5, 0.42)
	rep = analyze(t, ts, path, plan, spec, continuityInput(spec, f, fakeTx{dropped}))
	if mf := find(rep, "speech_missing"); mf == nil || mf.Severity != models.QAError || !strings.Contains(mf.Message, "a whole phrase from the “talk” clip is missing from the render") {
		dump(t, rep)
		t.Fatalf("missing phrase: %+v", mf)
	}
	// Speech nothing accounts for.
	extra := append(wordsAt("we came for the northern lights and stayed for the silence", 0.5, 0.42), wordsAt("buy now limited offer", 5.2, 0.15)...)
	rep = analyze(t, ts, path, plan, spec, continuityInput(spec, f, fakeTx{extra}))
	if uf := find(rep, "speech_unexpected"); uf == nil || !strings.Contains(uf.Message, "speech the cut doesn't explain") {
		dump(t, rep)
		t.Fatalf("unexpected speech: %+v", uf)
	}
	// No transcriber: skipped, said plainly.
	rep = analyze(t, ts, path, plan, spec, continuityInput(spec, f, nil))
	for _, c := range rep.Checks {
		if c.Name == "speech_continuity" && (c.Status != "skipped" || !strings.Contains(c.Detail, "no transcriber set up")) {
			t.Fatalf("skip: %+v", c)
		}
	}
}

// The real thing: speech made with macOS say, transcribed locally; a render
// with a 0.4 s slice of audio duplicated is caught, the clean one is not.
func TestSpeechContinuityGlitchedRender(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("uses macOS say")
	}
	ts := toolset(t)
	cfg := config.Tools{ModelsDir: filepath.Join(os.Getenv("HOME"), ".local", "share", "metarush-models")}
	ts = tools.New(cfg)
	tx := transcribe.Select(config.Transcription{Provider: "auto"}, ts)
	if tx == nil {
		t.Skip("no local transcriber installed")
	}
	say, err := exec.LookPath("say")
	if err != nil {
		t.Skip("say not installed")
	}
	dir := t.TempDir()
	txt := filepath.Join(dir, "s.txt")
	_ = os.WriteFile(txt, []byte("Every morning starts in the dark. Out here, the light arrives slowly, and you learn to wait for it."), 0o600)
	speech := filepath.Join(dir, "speech.wav")
	if out, err := exec.Command(say, "-v", "Samantha", "-r", "165", "-f", txt, "-o", speech, "--data-format=LEI16@22050").CombinedOutput(); err != nil {
		t.Skipf("say: %v %s", err, out)
	}
	ff := ts.Path(tools.FFmpeg)
	mux := func(audioFilter, out string) {
		args := []string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=640x360:rate=30:duration=7", "-i", speech}
		if audioFilter != "" {
			args = append(args, "-filter_complex", audioFilter, "-map", "0:v", "-map", "[a]")
		} else {
			args = append(args, "-map", "0:v", "-map", "1:a", "-af", "apad=whole_dur=7")
		}
		args = append(args, "-t", "7", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", out)
		if o, err := exec.Command(ff, args...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg: %v %s", err, o)
		}
	}
	clean := filepath.Join(dir, "clean.mp4")
	glitched := filepath.Join(dir, "glitched.mp4")
	mux("", clean)
	// Duplicate 0.4 s of audio at 1.6 s (inside "starts in the dark"), as a broken render would.
	mux("[1:a]asplit=3[a0][a1][a2];[a0]atrim=0:2.0,asetpts=PTS-STARTPTS[p];[a1]atrim=1.6:2.0,asetpts=PTS-STARTPTS[d];[a2]atrim=start=2.0,asetpts=PTS-STARTPTS[r];[p][d][r]concat=n=3:v=0:a=1,apad=whole_dur=7[a]", glitched)
	// What the plan expects: the source's own transcript (same model, as in production).
	wav := filepath.Join(dir, "src16k.wav")
	if o, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y", "-i", speech, "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", wav).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, o)
	}
	src, err := tx.Transcribe(context.Background(), wav, dir, "en")
	if err != nil {
		t.Fatal(err)
	}
	var words []models.Word
	for _, s := range src.Segments {
		words = append(words, s.Words...)
	}
	if len(words) < 15 {
		t.Fatalf("source transcript too short: %+v", words)
	}
	spec, plan, f := speechPlan(t, words)
	spec.Clips[0].Out = 6.9
	plan = resolve(t, spec, f)
	rep := analyze(t, ts, clean, plan, spec, continuityInput(spec, f, tx))
	for _, k := range []string{"speech_repeated", "speech_missing", "speech_unexpected"} {
		if fd := find(rep, k); fd != nil {
			dump(t, rep)
			t.Fatalf("the clean render was flagged: %s %s", k, fd.Message)
		}
	}
	rep = analyze(t, ts, glitched, plan, spec, continuityInput(spec, f, tx))
	if os.Getenv("QA_DEBUG") != "" {
		gw := filepath.Join(dir, "g16k.wav")
		_ = exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y", "-i", glitched, "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", gw).Run()
		g, _ := tx.Transcribe(context.Background(), gw, dir, "en")
		for _, s := range g.Segments {
			for _, w := range s.Words {
				t.Logf("heard %-12s %.2f-%.2f", w.Text, w.From, w.To)
			}
		}
		for _, w := range words {
			t.Logf("planned %-12s %.2f-%.2f", w.Text, w.From, w.To)
		}
	}
	rf := find(rep, "speech_repeated")
	if rf == nil || rf.From < 1.0 || rf.From > 3.2 {
		dump(t, rep)
		t.Fatalf("the duplicated slice was not caught as a repeat near 0:02: %+v", rf)
	}
	t.Logf("glitch finding: %s", rf.Message)
	// A whole word played twice, and 0.4 s of speech skipped.
	word := filepath.Join(dir, "word.mp4")
	mux("[1:a]asplit=3[a0][a1][a2];[a0]atrim=0:0.72,asetpts=PTS-STARTPTS[p];[a1]atrim=0.32:0.72,asetpts=PTS-STARTPTS[d];[a2]atrim=start=0.72,asetpts=PTS-STARTPTS[r];[p][d][r]concat=n=3:v=0:a=1,apad=whole_dur=7[a]", word)
	rep = analyze(t, ts, word, plan, spec, continuityInput(spec, f, tx))
	if rf := find(rep, "speech_repeated"); rf == nil || rf.From > 1.6 {
		dump(t, rep)
		t.Fatalf("a repeated word was not caught: %+v", rf)
	} else {
		t.Logf("repeated word: %s", rf.Message)
	}
	skip := filepath.Join(dir, "skip.mp4")
	mux("[1:a]asplit=2[a0][a1];[a0]atrim=0:2.6,asetpts=PTS-STARTPTS[p];[a1]atrim=start=3.0,asetpts=PTS-STARTPTS[r];[p][r]concat=n=2:v=0:a=1,apad=whole_dur=7[a]", skip)
	rep = analyze(t, ts, skip, plan, spec, continuityInput(spec, f, tx))
	if mf := find(rep, "speech_missing"); mf == nil || mf.From < 1.8 || mf.From > 3.6 {
		dump(t, rep)
		t.Fatalf("skipped speech was not caught: %+v", mf)
	} else {
		t.Logf("skipped speech: %s", mf.Message)
	}
}

func TestNarrationHeuristics(t *testing.T) {
	ts := toolset(t)
	const astVO = "ast_vvvvvvvvvvvvvvvvvvvvvvvvvv"
	const astMu = "ast_mmmmmmmmmmmmmmmmmmmmmmmmmm"
	f := facts(true)
	f.assets[astA] = editspec.AssetFacts{ID: astA, Name: "talk.mov", State: "available", InProject: true, Duration: 60, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, TranscriptPrecision: "word"}
	f.tx[astA] = []editspec.TranscriptSegment{{From: 1, To: 3, Words: toTx(wordsAt("she says something on camera", 1, 0.4))}}
	f.assets[astVO] = editspec.AssetFacts{ID: astVO, Name: "vo.wav", State: "available", InProject: true, Duration: 10, HasAudio: true, TranscriptPrecision: "word"}
	f.tx[astVO] = []editspec.TranscriptSegment{{From: 0.1, To: 2, Words: toTx(wordsAt("totally different words here", 0.1, 0.4))}}
	f.assets[astMu] = editspec.AssetFacts{ID: astMu, Name: "bed.m4a", State: "available", InProject: true, Duration: 60, HasAudio: true}
	s := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
		Clips: []editspec.Clip{{ID: "talk", AssetID: astA, In: 0, Out: 6}},
		Audio: []editspec.AudioTrack{{ID: "bed", AssetID: astMu, Role: editspec.RoleMusic, In: 0, Out: 6, At: 0, Rights: &editspec.Rights{License: "CC0"}}},
		Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{{ID: "b1", Text: "Out here the light arrives slowly", Anchor: editspec.Anchor{Kind: editspec.AnchorTime, At: ptrF(1)},
			Take: &editspec.TakeRef{AssetID: astVO, In: 0, Out: 2, Source: models.TakeRecorded}}}},
		Mix: &editspec.Mix{Ducking: &editspec.Ducking{MusicUnderVoiceoverDB: ptrF(-2)}, Loudness: &editspec.MasterLoudness{Normalize: boolP(false)}}}
	plan := resolve(t, s, f)
	path := render(t, ts, "640x360", 6, "", okTone)
	rep := analyze(t, ts, path, plan, s, func(in *Input) {
		in.Loudness = map[string]Loudness{astVO: {LUFS: -20, TruePeak: -0.05}, astMu: {LUFS: -16, TruePeak: -3}, astA: {LUFS: -22, TruePeak: -4}}
		in.TakeWords = map[string][]editspec.TranscriptWord{astVO: f.tx[astVO][0].Words}
	})
	for _, k := range []string{"narration_over_dialogue", "narration_masked", "narration_clipping", "narration_script_mismatch"} {
		fd := find(rep, k)
		if fd == nil || fd.Basis != models.QAHeuristic {
			dump(t, rep)
			t.Fatalf("missing %s", k)
		}
		if strings.Contains(fd.Message, "LUFS") || strings.Contains(fd.Message, "dBTP") || strings.Contains(fd.Message, "ast_") {
			t.Fatalf("%s is not in plain words: %s", k, fd.Message)
		}
	}
	if m := find(rep, "narration_masked"); !strings.Contains(m.Message, "the music is almost as loud") {
		t.Fatalf("masking names the music: %s", m.Message)
	}
}

func ptrF(v float64) *float64 { return &v }
func boolP(b bool) *bool      { return &b }

// When the mixer sets the master gain itself, loudness fixes move the mix's own
// target or ceiling (per-clip gain changes would be undone), and a mix already
// limited by its loudest narration moments gets advice instead of ops.
func TestLoudnessFixesForANormalizingMix(t *testing.T) {
	tgt := models.QALoudnessTarget{Platform: "youtube", IntegratedLUFS: -14, ToleranceLU: 2, MaxTruePeakDBTP: -1}
	newRun := func(limited bool) *run {
		spec := &editspec.Spec{Output: editspec.Output{Platform: "youtube", FPS: 30}, Mix: &editspec.Mix{MuteSource: true}}
		plan := &editspec.RenderPlan{DurationInFrames: 300, FPS: 30,
			Mix:       &editspec.PlanMix{Normalize: true, PeakLimited: limited, TargetLUFS: -14, TruePeakDBTP: -1},
			Voiceover: &editspec.PlanVoiceover{Beats: []editspec.PlanBeat{{ID: "line-1"}}}}
		return &run{in: Input{Spec: spec}, plan: plan, fps: 30, rep: &models.QAReport{}}
	}
	r := newRun(true)
	r.in.Spec.Voiceover = &editspec.Voiceover{Beats: []editspec.Beat{{ID: "line-1", Text: "Hello there.", Take: &editspec.TakeRef{AssetID: "ast_x", Out: 1, Source: models.TakeRecorded}}}}
	r.loudness(tgt, -17.4, -1.4)
	if len(r.pend) != 1 || r.pend[0].Kind != "loudness_low" || r.pend[0].Fix == nil || len(r.pend[0].Fix.Ops) != 0 ||
		!strings.Contains(r.pend[0].Fix.Summary, "Clean up the narration on the Voiceover tab") {
		t.Fatalf("peak-limited mix with a raw recording: %+v", r.pend)
	}
	// Cleaned up already, narration for a third of the video, music under it: turn the music up.
	r = newRun(true)
	r.in.Spec.Voiceover = &editspec.Voiceover{Beats: []editspec.Beat{{ID: "line-1", Text: "Hello there.", Take: &editspec.TakeRef{AssetID: "ast_x", Out: 1,
		Source: models.TakeRecorded, UseProcessed: true, Processed: &editspec.ProcessedRef{AssetID: "ast_y", Preset: "voice-clean"}}}}}
	r.plan.Mix.VoiceRanges = []editspec.FrameRange{{From: 30, To: 130}}
	r.plan.Audio = []editspec.PlanAudio{{ID: "bed", AssetID: "ast_m", Role: editspec.RoleMusic, DurationInFrames: 300, GainDB: -10}}
	r.in.Spec.Mix.Music = &editspec.Bus{GainDB: ptrF(-2)}
	r.loudness(tgt, -18, -1.4)
	if f := r.pend[0]; f.Kind != "loudness_low" || len(f.Fix.Ops) != 1 || *f.Fix.Ops[0].Mix.Music.GainDB != 2 || !strings.Contains(f.Fix.Summary, "music up about 4 dB") {
		t.Fatalf("cleaned narration over quiet music, no music loudness known: want the music up 4 dB: %+v %+v", r.pend, r.pend[0].Fix)
	}
	// With the music's measured loudness (-20 LUFS at -10 dB = -30 as heard): up 12 dB, capped.
	r.pend = nil
	r.in.Loudness = map[string]Loudness{"ast_m": {LUFS: -20, TruePeak: -8}}
	r.loudness(tgt, -18, -1.4)
	if f := r.pend[0]; *f.Fix.Ops[0].Mix.Music.GainDB != 10 || !strings.Contains(f.Fix.Summary, "about 12 dB") {
		t.Fatalf("want the music up 12 dB (to about -18 LUFS where nobody talks): %+v", f.Fix)
	}
	r = newRun(false)
	r.loudness(tgt, -17.0, -6)
	f := r.pend[0]
	if f.Kind != "loudness_low" || len(f.Fix.Ops) != 1 || f.Fix.Ops[0].Op != "setMix" || f.Fix.Ops[0].Mix.Loudness == nil ||
		*f.Fix.Ops[0].Mix.Loudness.TargetLUFS != -11 || !f.Fix.Ops[0].Mix.MuteSource {
		t.Fatalf("estimate off by 3 dB: want the target 3 dB higher, keeping the rest of the mix: %+v", f.Fix)
	}
	if _, _, err := editspec.ApplyOps(r.in.Spec, f.Fix.Ops, false); err != nil {
		t.Fatalf("fix ops do not apply: %v", err)
	}
	r = newRun(true)
	r.loudness(tgt, -14, -0.4)
	if f := r.pend[0]; f.Kind != "true_peak_high" || len(f.Fix.Ops) != 1 || *f.Fix.Ops[0].Mix.Loudness.TruePeakDBTP != -1.9 {
		t.Fatalf("peaks over the ceiling in a limited mix: lower the ceiling: %+v", r.pend)
	}
}
