package qa

import (
	"math"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// talking gives an asset word-timed speech: a word every 0.5 s from `from` to `to`.
func talking(f *fakeFacts, id string, from, to float64, speaker string) {
	seg := editspec.TranscriptSegment{From: from, To: to, Speaker: speaker, Text: "talk"}
	for t := from; t+0.35 <= to; t += 0.5 {
		seg.Words = append(seg.Words, editspec.TranscriptWord{Text: "word", From: t, To: t + 0.35})
	}
	f.tx[id] = append(f.tx[id], seg)
	a := f.assets[id]
	a.TranscriptPrecision = "word"
	f.assets[id] = a
}

func withCuts(s *editspec.Spec, f *fakeFacts) func(*Input) {
	return func(in *Input) {
		in.Cuts, in.Shots = editspec.CutReport(s, f), editspec.Shots(s, f)
		in.ExpectedSpeech, in.UnknownSpeech, in.SpeechEdges = editspec.ExpectedSpeech(s, f)
	}
}

func TestSplitAndTransitionChecks(t *testing.T) {
	ts := toolset(t)
	f := facts(true)
	talking(f, astA, 0, 60, "Ada")
	talking(f, astB, 0, 60, "Ben")
	s := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30, Platform: "generic"},
		Clips: []editspec.Clip{
			{ID: "one", AssetID: astA, In: 2, Out: 5},
			// Ben's voice should lead by 0.4 s, but his clip starts 0.2 s into his recording.
			{ID: "two", AssetID: astB, In: 0.2, Out: 3, Transition: &editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundLead, Duration: 0.4}}},
			// A dissolve while both talk.
			{ID: "three", AssetID: astA, In: 10, Out: 13, Transition: &editspec.Transition{Type: editspec.TransitionCrossfade, Duration: 1}},
			// Two frames of picture.
			{ID: "blip", AssetID: astB, In: 20, Out: 20.067},
			{ID: "four", AssetID: astA, In: 30, Out: 33},
		}}
	plan := resolve(t, s, f)
	path := render(t, ts, "960x540", float64(plan.DurationInFrames)/30, "", okTone)
	rep := analyze(t, ts, path, plan, s, withCuts(s, f))
	dump(t, rep)
	short := find(rep, "split_too_short")
	if short == nil || !strings.Contains(short.Message, "Ben's clip has only 0.2 s of sound before it starts, so Ben's voice starts 0.2 s early instead of 0.4 s") ||
		short.Fix == nil || len(short.Fix.Ops) != 1 || short.Fix.Ops[0].Op != "setTransition" || short.Fix.Ops[0].Transition.Audio.Duration != 0.2 {
		t.Fatalf("split short of footage: %+v", short)
	}
	ov := find(rep, "voices_overlap")
	if ov == nil || ov.Fix == nil || ov.Fix.Ops[0].Transition.Type != editspec.TransitionCrossfade || ov.Fix.Ops[0].Transition.Audio.Mode != editspec.SoundAuto {
		t.Fatalf("two voices at once: %+v", ov)
	}
	fl := find(rep, "flash_shot")
	if fl == nil || fl.Fix == nil || fl.Fix.Ops[0].Op != "removeClip" || fl.Fix.Ops[0].ID != "blip" {
		t.Fatalf("flash: %+v", fl)
	}
	// The fix ops apply to the rendered spec.
	for _, k := range []string{"split_too_short", "voices_overlap", "flash_shot"} {
		if _, _, err := editspec.ApplyOps(s, find(rep, k).Fix.Ops, false); err != nil {
			t.Fatalf("%s fix does not apply: %v", k, err)
		}
	}
	var check *models.QACheck
	for i := range rep.Checks {
		if rep.Checks[i].Name == "transitions" {
			check = &rep.Checks[i]
		}
	}
	if check == nil || check.Status != "warning" {
		t.Fatalf("transitions check: %+v", check)
	}
}

func TestLoudnessJumpAcrossACut(t *testing.T) {
	ts := toolset(t)
	f := facts(true)
	talking(f, astA, 0, 60, "Ada")
	talking(f, astB, 0, 60, "Ben")
	s := twoClips(editspec.PresetLandscape)
	plan := resolve(t, s, f)
	// Ada (first 3 s) is loud, Ben (next 3 s) 20 dB quieter: a different microphone.
	path := render(t, ts, "960x540", 6, "", "if(lt(t\\,3)\\,0.3*sin(2*PI*440*t)\\,0.03*sin(2*PI*440*t))")
	rep := analyze(t, ts, path, plan, s, withCuts(s, f))
	dump(t, rep)
	j := find(rep, "loudness_jump")
	if j == nil || j.Basis != models.QAObserved || !strings.Contains(j.Message, "quieter") {
		t.Fatalf("loudness jump: %+v", j)
	}
	if n := len(rep.Measurements.CutLoudness); n != 1 || math.Abs(rep.Measurements.CutLoudness[0].Jump+20) > 1.5 {
		t.Fatalf("measured: %+v", rep.Measurements.CutLoudness)
	}
	// Without speech leveling the fix turns it on; with it, the louder clip goes down.
	if j.Fix == nil || len(j.Fix.Ops) != 1 || j.Fix.Ops[0].Op != "setMix" || j.Fix.Ops[0].Mix.LevelSpeech == nil || !*j.Fix.Ops[0].Mix.LevelSpeech {
		t.Fatalf("fix without leveling: %+v", j.Fix)
	}
	on := true
	s.Mix = &editspec.Mix{LevelSpeech: &on}
	plan = resolve(t, s, f)
	rep = analyze(t, ts, path, plan, s, withCuts(s, f))
	j = find(rep, "loudness_jump")
	if j == nil || j.Fix == nil || len(j.Fix.Ops) != 1 || j.Fix.Ops[0].Op != "updateClip" || j.Fix.Ops[0].ID != "one" || !strings.Contains(string(j.Fix.Ops[0].Set), `"volume":0.1`) {
		t.Fatalf("fix with leveling on: %+v", j.Fix)
	}
}
