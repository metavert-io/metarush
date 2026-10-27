package editspec

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

const astVO = "ast_01jabcdefghjkmnpqrstvwxyz2"
const astMusic = "ast_01jabcdefghjkmnpqrstvwxyz3"

type loudFacts struct {
	fakeFacts
	loud map[string][2]float64
}

func (f loudFacts) Loudness(id string) (float64, float64, bool) {
	v, ok := f.loud[id]
	return v[0], v[1], ok
}

func voFacts() loudFacts {
	f := facts()
	f.assets[astVO] = AssetFacts{ID: astVO, Name: "vo.wav", Kind: "audio", State: "available", InProject: true, Duration: 20, HasAudio: true, TranscriptPrecision: "word"}
	f.assets[astMusic] = AssetFacts{ID: astMusic, Name: "bed.m4a", Kind: "audio", State: "available", InProject: true, Duration: 60, HasAudio: true}
	f.tx[astVO] = []TranscriptSegment{{From: 0.2, To: 1.6, Text: "Out here, the light arrives slowly.", Words: []TranscriptWord{
		{Text: "Out", From: 0.2, To: 0.4}, {Text: "here,", From: 0.4, To: 0.6}, {Text: "the", From: 0.6, To: 0.7}, {Text: "light", From: 0.7, To: 0.95}, {Text: "arrives", From: 0.95, To: 1.3}, {Text: "slowly.", From: 1.3, To: 1.6}}}}
	return loudFacts{fakeFacts: f, loud: map[string][2]float64{astA: {-20, -6}, astB: {-20, -6}, astVO: {-18, -3}, astMusic: {-16, -4}}}
}

func ptr(v float64) *float64 { return &v }

func voSpec() *Spec {
	s := baseSpec() // hook (A 1–4s, 90f), broll (B 0–3s, crossfade 0.5s), end (A 10–14s, locked)
	s.Voiceover = &Voiceover{Beats: []Beat{
		{ID: "b1", Text: "Out here, the light arrives slowly.", Anchor: Anchor{Kind: AnchorClip, ClipID: "broll", Offset: 0.5},
			Take: &TakeRef{AssetID: astVO, In: 0, Out: 1.8, Source: "recorded"}},
		{ID: "b2", Text: "And then it is everywhere.", Anchor: Anchor{Kind: AnchorMoment, AssetID: astA, At: ptr(12), Quote: "she laughs"}},
		{ID: "b3", Text: "Hello.", Anchor: Anchor{Kind: AnchorTime, At: ptr(2)}},
	}}
	Normalize(s)
	return s
}

func beatByID(bs []ResolvedBeat, id string) ResolvedBeat {
	for _, b := range bs {
		if b.ID == id {
			return b
		}
	}
	return ResolvedBeat{}
}

// Clip- and moment-anchored lines move with their clips when the cut is
// re-edited; clock-time lines stay; a line whose clip is gone is reported.
func TestAnchorsSurviveReedits(t *testing.T) {
	f := voFacts()
	s := voSpec()
	clips, total := Timeline(s)
	bs := ResolveBeats(s, clips, total, f)
	// broll starts at frame 75 (crossfade overlap), +0.5 s = 90.
	if b := beatByID(bs, "b1"); b.Start != 90 || b.Problem != "" {
		t.Fatalf("b1 %+v", b)
	}
	// "end" starts at 75+90=165 with source in 10s: 12s → 165 + 60 = 225.
	if b := beatByID(bs, "b2"); b.Start != 225 {
		t.Fatalf("b2 %+v", b)
	}
	if b := beatByID(bs, "b3"); b.Start != 60 {
		t.Fatalf("b3 %+v", b)
	}
	// Tighten the hook by 0.5 s: everything after it moves 15 frames earlier.
	next, _, err := ApplyOps(s, []Op{{Op: "updateClip", ID: "hook", Set: json.RawMessage(`{"out":3.5}`)}}, false)
	if err != nil {
		t.Fatal(err)
	}
	Normalize(next)
	clips, total = Timeline(next)
	bs = ResolveBeats(next, clips, total, f)
	if beatByID(bs, "b1").Start != 75 || beatByID(bs, "b2").Start != 210 || beatByID(bs, "b3").Start != 60 {
		t.Fatalf("after tightening: %+v", bs)
	}
	// Remove the b-roll: b1 cannot be placed (an error), b2 still follows its moment.
	gone, _, _ := ApplyOps(next, []Op{{Op: "removeClip", ID: "broll"}}, false)
	Normalize(gone)
	issues := Validate(gone, f, nil, false)
	var unresolved *Issue
	for i := range issues {
		if issues[i].Code == "anchor_unresolved" {
			unresolved = &issues[i]
		}
	}
	if unresolved == nil || unresolved.Severity != "error" || !strings.Contains(unresolved.Message, "no longer in the video") || strings.Contains(unresolved.Message, "broll") {
		t.Fatalf("missing clip: %+v", issues)
	}
	clips, total = Timeline(gone)
	if b := beatByID(ResolveBeats(gone, clips, total, f), "b2"); b.Problem != "" || b.Start != 75+60 {
		t.Fatalf("b2 after removing broll: %+v", b)
	}
}

func TestAfterAnchorsChain(t *testing.T) {
	s := voSpec()
	s.Voiceover.Beats[1].Anchor = Anchor{Kind: AnchorAfter, Gap: ptr(0.5)}
	clips, total := Timeline(s)
	bs := ResolveBeats(s, clips, total, voFacts())
	b1 := beatByID(bs, "b1")
	if b2 := beatByID(bs, "b2"); b2.Start != b1.End()+15 {
		t.Fatalf("after: b1 ends %d, b2 starts %d", b1.End(), b2.Start)
	}
}

func TestFitWarnings(t *testing.T) {
	f := voFacts()
	s := voSpec()
	long := strings.Repeat("word ", 27) + "end."
	s.Voiceover.Beats[0] = Beat{ID: "b1", Text: long, Anchor: Anchor{Kind: AnchorClip, ClipID: "broll", Offset: 0.5}}
	s.Voiceover.Beats[1] = Beat{ID: "b2", Text: "Short.", Anchor: Anchor{Kind: AnchorClip, ClipID: "end"}}
	s.Voiceover.Beats = s.Voiceover.Beats[:2]
	Normalize(s)
	issues := Validate(s, f, nil, false)
	var fit *Issue
	for i := range issues {
		if issues[i].Code == "beat_fit" {
			fit = &issues[i]
		}
	}
	// b1 starts at 3.0 s, b2 at 5.5 s: a 2.5 s window for 28 words at 155 wpm ≈ 10.8 s.
	if fit == nil || !strings.Contains(fit.Message, "28 words") || !strings.Contains(fit.Message, "2.5 seconds") || !strings.Contains(fit.Message, "about 10.8 seconds") {
		t.Fatalf("fit warning: %+v", issues)
	}
	if !strings.HasPrefix(fit.Message, "Narration line 1 (“word word") {
		t.Fatalf("lines are named for people: %q", fit.Message)
	}
	rep := FitReport(s, f)
	if rep[0].Status != "over" || rep[0].Preferred != FixShorten {
		t.Fatalf("fit report: %+v", rep[0])
	}
	var shorten *FitFix
	for i := range rep[0].Fixes {
		if rep[0].Fixes[i].Strategy == FixShorten {
			shorten = &rep[0].Fixes[i]
		}
	}
	if shorten == nil || shorten.TargetWords < 5 || shorten.TargetWords > 7 {
		t.Fatalf("shorten target: %+v", rep[0].Fixes)
	}
	// A recorded take that overruns prefers holding the shot longer.
	s.Voiceover.Beats[0].Take = &TakeRef{AssetID: astVO, In: 0, Out: 4, Source: "recorded"}
	rep = FitReport(s, f)
	if rep[0].Preferred != FixExtendShot {
		t.Fatalf("recorded overrun: %+v", rep[0])
	}
	plan := Plan{Ops: nil}
	for _, fx := range rep[0].Fixes {
		if fx.Strategy == FixExtendShot {
			plan.Ops = fx.Ops
		}
	}
	fixed, _, err := ApplyOps(s, plan.Ops, false)
	if err != nil {
		t.Fatal(err)
	}
	if r := FitReport(fixed, f); r[0].Status == "over" {
		t.Fatalf("extending the shot did not fix it: %+v", r[0])
	}
	// TTS within 8% speeds up.
	s.Voiceover.Beats[0].Take = &TakeRef{AssetID: astVO, In: 0, Out: 2.6, Source: "tts", AIVoice: true}
	rep = FitReport(s, f)
	if rep[0].Preferred != FixSpeedUp {
		t.Fatalf("tts overrun: %+v", rep[0])
	}
}

func envAt(env []GainPoint, f int) float64 { return GainAt(env, float64(f)) }

type channelFacts struct {
	loudFacts
	ch map[string]int
}

func (f channelFacts) AudioChannels(id string) int { return f.ch[id] }

// The renderer upmixes mono with equal power: a mono narration file's peaks land
// 3 dB lower in the stereo render, so the peak guard allows 3 dB more master gain.
func TestMasterGainKnowsMonoSourcesPeakLower(t *testing.T) {
	s := voSpec()
	s.Voiceover.Beats = s.Voiceover.Beats[:1]
	s.Mix = &Mix{MuteSource: true}
	s.Output.Platform = "youtube" // -14 LUFS
	f := voFacts()
	f.loud[astVO] = [2]float64{-20, -2} // quiet, peaky narration: the peak guard decides the gain
	stereo := Resolve(s, f, "rev_x", "prj_x", Hash(s))
	if want := -1 - CodecPeakMarginDB + 2; !stereo.Mix.PeakLimited || math.Abs(stereo.Mix.MasterGainDB-want) > 0.01 {
		t.Fatalf("stereo source: gain %.3f limited %v, want %+.1f dB (the -1 dBTP ceiling less the codec margin)", stereo.Mix.MasterGainDB, stereo.Mix.PeakLimited, want)
	}
	mono := Resolve(s, channelFacts{loudFacts: f, ch: map[string]int{astVO: 1}}, "rev_x", "prj_x", Hash(s))
	if d := mono.Mix.MasterGainDB - stereo.Mix.MasterGainDB; math.Abs(d+MonoUpmixDB) > 0.01 {
		t.Fatalf("mono source: gain %.3f vs stereo %.3f, want %.2f dB more", mono.Mix.MasterGainDB, stereo.Mix.MasterGainDB, -MonoUpmixDB)
	}
	if p := PeakInMix(channelFacts{loudFacts: f, ch: map[string]int{astVO: 2}}, astVO, -2); p != -2 {
		t.Fatalf("stereo peak moved: %v", p)
	}
}

func TestMixDuckingMuteSolo(t *testing.T) {
	f := voFacts()
	s := voSpec()
	s.Audio = []AudioTrack{{ID: "bed", AssetID: astMusic, Role: RoleMusic, In: 0, Out: 30, At: 0, GainDB: ptr(-6), Rights: &Rights{License: "CC0"}}}
	plan := Resolve(s, f, "rev_x", "prj_x", Hash(s))
	var bed, vo *PlanAudio
	for i := range plan.Audio {
		switch plan.Audio[i].ID {
		case "bed":
			bed = &plan.Audio[i]
		case "vo-b1":
			vo = &plan.Audio[i]
		}
	}
	if bed == nil || vo == nil || vo.From != 90 || vo.Bus != BusVoiceover || vo.BeatID != "b1" {
		t.Fatalf("audio elements: %+v", plan.Audio)
	}
	master := dbToLin(plan.Mix.MasterGainDB)
	// Music: -6 dB, and 14 dB lower while narration plays (frames 90–144).
	if g := envAt(bed.Gain, 100); math.Abs(g-dbToLin(-20)*master) > 0.003 {
		t.Fatalf("music under narration %.4f want %.4f", g, dbToLin(-20)*master)
	}
	if g := envAt(bed.Gain, 150+20); math.Abs(g-dbToLin(-6)*master) > 0.003 {
		t.Fatalf("music after narration (release) %.4f want %.4f", g, dbToLin(-6)*master)
	}
	// The hook clip's audio is ducked 18 dB under narration where they overlap (none here);
	// the broll has no audio; the end clip (from 165) overlaps narration? b1 ends at 144: no.
	var hook *PlanClip
	for i := range plan.Clips {
		if plan.Clips[i].ID == "hook" {
			hook = &plan.Clips[i]
		}
	}
	if hook == nil || len(hook.Gain) == 0 || math.Abs(envAt(hook.Gain, 10)-master) > 0.003 {
		t.Fatalf("hook gain: %+v", hook)
	}
	// Mute the source: clips are muted and do not count as dialogue.
	s.Mix = &Mix{MuteSource: true}
	plan = Resolve(s, f, "rev_x", "prj_x", Hash(s))
	for _, c := range plan.Clips {
		if !c.Muted {
			t.Fatalf("clip %s not muted", c.ID)
		}
	}
	if len(plan.DialogueRanges) != 0 {
		t.Fatalf("muted clips must not duck music: %+v", plan.DialogueRanges)
	}
	// Solo the music: narration and clips are silent.
	s.Mix = &Mix{Music: &Bus{Solo: true}}
	plan = Resolve(s, f, "rev_x", "prj_x", Hash(s))
	for _, a := range plan.Audio {
		if a.Bus != BusMusic {
			t.Fatalf("soloed music, but %s plays", a.ID)
		}
	}
	if !plan.Mix.Buses[BusVoiceover].Muted || !plan.Mix.Buses[BusSource].Muted || plan.Mix.Buses[BusMusic].Muted {
		t.Fatalf("buses: %+v", plan.Mix.Buses)
	}
	// Bus gain applies on top of the track gain.
	s.Mix = &Mix{Music: &Bus{GainDB: ptr(-4)}, Loudness: &MasterLoudness{Normalize: boolp(false)}}
	plan = Resolve(s, f, "rev_x", "prj_x", Hash(s))
	for _, a := range plan.Audio {
		if a.ID == "bed" && math.Abs(envAt(a.Gain, 162)-dbToLin(-10)) > 0.003 {
			t.Fatalf("bus gain: %.4f", envAt(a.Gain, 162))
		}
	}
}

func boolp(b bool) *bool { return &b }

// Without a voiceover or mix section the envelopes reproduce the original audio
// behavior exactly (clip transitions, track fades, explicit ducking only).
func TestLegacyMixParity(t *testing.T) {
	f := facts()
	s := baseSpec()
	f.assets[astMusic] = AssetFacts{ID: astMusic, Name: "bed", State: "available", InProject: true, Duration: 60, HasAudio: true}
	s.Audio = []AudioTrack{{ID: "bed", AssetID: astMusic, Role: RoleMusic, In: 0, Out: 20, At: 0, GainDB: ptr(-8), FadeIn: 0.5, FadeOut: 1.5, DuckUnderDialogue: true, DuckDB: ptr(-12)}}
	plan := Resolve(s, f, "rev_x", "prj_x", Hash(s))
	if plan.Mix == nil || plan.Mix.Ducking.Auto || plan.Mix.MasterGainDB != 0 {
		t.Fatalf("legacy mix must not auto-duck or normalize: %+v", plan.Mix)
	}
	ramp := max(2, int(math.Round(30*0.2)))
	bed := plan.Audio[0]
	for fr := 0; fr < bed.DurationInFrames; fr++ {
		want := dbToLin(-8)
		if fr < 15 {
			want *= smoothstep(float64(fr) / 15)
		}
		if bed.DurationInFrames-fr < 45 {
			want *= smoothstep(float64(bed.DurationInFrames-fr) / 45)
		}
		want *= dbToLin(-12 * duckDepth(fr, plan.DialogueRanges, ramp, ramp))
		if got := envAt(bed.Gain, fr); math.Abs(got-want) > 0.0025 {
			t.Fatalf("frame %d: envelope %.5f, legacy formula %.5f", fr, got, want)
		}
	}
	for i := range plan.Clips {
		c := &plan.Clips[i]
		if !c.HasAudio {
			if len(c.Gain) != 0 {
				t.Fatalf("silent clip %s has an envelope", c.ID)
			}
			continue
		}
		var next *PlanClip
		if i+1 < len(plan.Clips) {
			next = &plan.Clips[i+1]
		}
		for fr := 0; fr < c.DurationInFrames; fr++ {
			if got, want := envAt(c.Gain, fr), legacyClipGain(c, next, fr); math.Abs(got-want) > 0.0025 {
				t.Fatalf("clip %s frame %d: %.5f vs %.5f", c.ID, fr, got, want)
			}
		}
	}
	if n := len(bed.Gain); n > 120 {
		t.Fatalf("envelope not compressed: %d points", n)
	}
}

func TestParseScript(t *testing.T) {
	s := baseSpec()
	s.Clips[1].Label = "Snow field"
	text := "# narration draft\n[@broll+0.5] (warm) Out here, the light\narrives slowly.\n\n[0:01.5] Hello there.\n\n[after+0.8] Every morning.\n\n[@end:end-1.2] Goodbye.\n\n[@snow-field] By label.\n\n[@nope] Unknown."
	beats, issues := ParseScript(text, s)
	if len(issues) != 1 || issues[0].Code != "script_markup" || !strings.Contains(issues[0].Message, "nope") {
		t.Fatalf("issues: %+v", issues)
	}
	if len(beats) != 6 {
		t.Fatalf("beats: %+v", beats)
	}
	b := beats[0]
	if b.ID != "line-1" || b.Text != "Out here, the light arrives slowly." || b.Delivery != "warm" || b.Anchor.Kind != AnchorClip || b.Anchor.ClipID != "broll" || b.Anchor.Offset != 0.5 {
		t.Fatalf("line 1: %+v", b)
	}
	if a := beats[1].Anchor; a.Kind != AnchorTime || *a.At != 1.5 {
		t.Fatalf("clock: %+v", a)
	}
	if a := beats[2].Anchor; a.Kind != AnchorAfter || a.Gap == nil || *a.Gap != 0.8 {
		t.Fatalf("after: %+v", a)
	}
	if a := beats[3].Anchor; a.ClipID != "end" || a.Edge != "end" || a.Offset != -1.2 {
		t.Fatalf("end edge: %+v", a)
	}
	if a := beats[4].Anchor; a.ClipID != "broll" {
		t.Fatalf("label: %+v", a)
	}
	// No markup at all: paragraphs are spread over the cuts.
	beats, _ = ParseScript("First line.\n\nSecond line.\n\nThird line.", s)
	if len(beats) != 3 || beats[0].Anchor.ClipID != "hook" || beats[1].Anchor.ClipID != "broll" || beats[2].Anchor.ClipID != "end" {
		t.Fatalf("auto-assigned: %+v", beats)
	}
	// Clip IDs with dashes and numbers stay whole.
	s.Clips[0].ID = "clip-2"
	beats, _ = ParseScript("[@clip-2] A.\n\n[@clip-2-0.5] B.", s)
	if beats[0].Anchor.ClipID != "clip-2" || beats[0].Anchor.Offset != 0 || beats[1].Anchor.Offset != -0.5 {
		t.Fatalf("dashed ids: %+v", beats)
	}
	if v, ok := ParseClock("1:02:03.5"); !ok || v != 3723.5 {
		t.Fatal("clock parse")
	}
}

func TestAlignScriptToRecording(t *testing.T) {
	beats := []Beat{{ID: "b1", Text: "We came for the northern lights."}, {ID: "b2", Text: "And stayed for the silence."}}
	w := func(text string, from, to float64) TranscriptWord {
		return TranscriptWord{Text: text, From: from, To: to}
	}
	words := []TranscriptWord{w("um", 0.1, 0.3), w("We", 0.5, 0.7), w("came", 0.7, 0.9), w("for", 0.9, 1.0), w("the", 1.0, 1.1), w("northen", 1.1, 1.5), w("lights.", 1.5, 1.9),
		w("And", 3.0, 3.2), w("stayed", 3.2, 3.5), w("for", 3.5, 3.6), w("the", 3.6, 3.7), w("silence.", 3.7, 4.2), w("Okay", 5.0, 5.3)}
	al := AlignScript(beats, words, 6)
	if !al.Beats[0].Found || !al.Beats[1].Found || al.Coverage != 1 {
		t.Fatalf("alignment: %+v", al)
	}
	if al.Beats[0].From != 0.38 || al.Beats[0].To != 2.15 || al.Beats[1].From != 2.88 {
		t.Fatalf("segments: %+v", al.Beats)
	}
	if strings.Join(al.Beats[1].Extra, " ") != "Okay" {
		t.Fatalf("extra words: %+v", al.Beats[1])
	}
	if al.WPM < 150 || al.WPM > 260 {
		t.Fatalf("wpm %.0f", al.WPM)
	}
}

func TestVoiceoverOpsLocksAndRebase(t *testing.T) {
	s := voSpec()
	next, touched, err := ApplyOps(s, []Op{
		{Op: "addBeat", Beat: &Beat{ID: "b0", Text: "First.", Anchor: Anchor{Kind: AnchorTime, At: ptr(0.2)}}, Before: "b1"},
		{Op: "updateBeat", ID: "b2", Set: json.RawMessage(`{"text":"And then, everywhere."}`)},
		{Op: "moveBeat", ID: "b3", After: "b0"},
		{Op: "setMix", Mix: &Mix{MuteSource: true}},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, b := range next.Voiceover.Beats {
		ids = append(ids, b.ID)
	}
	if strings.Join(ids, ",") != "b0,b3,b1,b2" || next.Voiceover.Beats[3].Text != "And then, everywhere." || next.Mix == nil || !touched["mix"] || !touched["beats-order"] {
		t.Fatalf("ops: %v %+v", ids, touched)
	}
	d := Compare(s, next)
	if len(d.BeatsAdded) != 1 || len(d.BeatsChanged) != 1 || !d.BeatsReordered || !d.MixChanged {
		t.Fatalf("diff: %+v", d)
	}
	if !strings.Contains(strings.Join(d.Summary, "; "), "1 narration line added") {
		t.Fatalf("summary: %v", d.Summary)
	}
	// Lock a beat: ops on it fail, validation catches script replacements.
	locked, _, _ := ApplyOps(s, []Op{{Op: "lock", ID: "b1"}}, false)
	if _, _, err := ApplyOps(locked, []Op{{Op: "updateBeat", ID: "b1", Set: json.RawMessage(`{"text":"x"}`)}}, false); err == nil {
		t.Fatal("a locked beat changed")
	}
	repl, _, err := ApplyOps(locked, []Op{{Op: "setScript", Voiceover: &Voiceover{Beats: []Beat{{ID: "z", Text: "Only.", Anchor: Anchor{Kind: AnchorAfter}}}}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if v := Validate(repl, voFacts(), locked, false); !HasErrors(v) {
		t.Fatal("replacing the script must not drop a locked line")
	}
	// Rebase: someone else edited b2, our b1 edit rebases; editing b2 too conflicts.
	head, _, _ := ApplyOps(s, []Op{{Op: "updateBeat", ID: "b2", Set: json.RawMessage(`{"delivery":"slower"}`)}}, false)
	if _, conflicts, err := Rebase(s, head, []Op{{Op: "updateBeat", ID: "b1", Set: json.RawMessage(`{"delivery":"warm"}`)}}, false); err != nil || len(conflicts) != 0 {
		t.Fatalf("disjoint beat edits must rebase: %v %v", conflicts, err)
	}
	if _, conflicts, _ := Rebase(s, head, []Op{{Op: "updateBeat", ID: "b2", Set: json.RawMessage(`{"text":"y."}`)}}, false); len(conflicts) != 1 || conflicts[0] != "b2" {
		t.Fatalf("same beat must conflict: %v", conflicts)
	}
	// The whole voiceover locked.
	vl, _, _ := ApplyOps(s, []Op{{Op: "lock", ID: "voiceover"}}, false)
	if _, _, err := ApplyOps(vl, []Op{{Op: "addBeat", Beat: &Beat{ID: "q", Text: "Q.", Anchor: Anchor{Kind: AnchorAfter}}}}, false); err == nil {
		t.Fatal("added a beat to a locked voiceover")
	}
	if _, _, err := ApplyOps(s, []Op{{Op: "updateVoiceover", Set: json.RawMessage(`{"beats":[]}`)}}, false); err == nil {
		t.Fatal("updateVoiceover must not replace beats")
	}
	up, _, err := ApplyOps(s, []Op{{Op: "updateVoiceover", Set: json.RawMessage(`{"settings":{"speakingRateWpm":172,"rateSource":"measured"}}`)}}, false)
	if err != nil || up.SpeakingRate() != 172 {
		t.Fatalf("updateVoiceover: %v", err)
	}
}

func TestCaptionsFromVoiceover(t *testing.T) {
	f := voFacts()
	s := voSpec()
	s.Captions = &Captions{Enabled: true, Source: "voiceover", Style: CaptionEmphasis}
	Normalize(s)
	plan := Resolve(s, f, "rev_x", "prj_x", Hash(s))
	pc := plan.Captions
	if pc == nil || pc.Precision != "segment" || len(pc.Lines) < 3 { // b2/b3 have no take: estimated
		t.Fatalf("captions: %+v", pc)
	}
	first := pc.Lines[0]
	for _, l := range pc.Lines {
		if l.From < first.From {
			first = l
		}
	}
	if first.Speaker != "" || first.Text != "Hello." {
		t.Fatalf("first line (narration only: no name above it): %+v", first)
	}
	var b1 *PlanCaptionLine
	for i := range pc.Lines {
		if strings.HasPrefix(pc.Lines[i].Text, "Out here") {
			b1 = &pc.Lines[i]
		}
	}
	// b1 plays from frame 90; its word "Out" is 0.2 s into the take: frame 96.
	if b1 == nil || b1.Words[0].From != 96 {
		t.Fatalf("take-timed line: %+v", b1)
	}
	// Speaker names: narration-only captions have one voice, so none are shown.
	s.Captions.ShowSpeakers = true
	plan = Resolve(s, f, "rev_x", "prj_x", Hash(s))
	if plan.Captions.ShowSpeakers {
		t.Fatal("narration-only captions should not reserve room for speaker names")
	}
	// Both: dialogue lines appear too, and the narration is named "Narrator".
	s.Captions.Source = "both"
	plan = Resolve(s, f, "rev_x", "prj_x", Hash(s))
	dlg, narr := 0, 0
	for _, l := range plan.Captions.Lines {
		if l.Speaker == NarrationSpeaker {
			narr++
		} else {
			dlg++
		}
	}
	if dlg == 0 || narr == 0 || !plan.Captions.ShowSpeakers || NarrationSpeaker != "Narrator" {
		t.Fatalf("both: %d dialogue, %d narration lines, showSpeakers %v", dlg, narr, plan.Captions.ShowSpeakers)
	}
}

func TestContinuousDrift(t *testing.T) {
	f := voFacts()
	s := voSpec()
	s.Voiceover.Layout = LayoutContinuous
	s.Voiceover.Continuous = &ContinuousVO{AssetID: astVO, In: 0, Out: 12, Anchor: Anchor{Kind: AnchorClip, ClipID: "hook"}}
	// The recording was made for the old cut: b1 at 3.0 s into the take.
	s.Voiceover.Beats[0].Take = &TakeRef{AssetID: astVO, In: 3.0, Out: 4.8, Source: "recorded"}
	s.Voiceover.Beats = s.Voiceover.Beats[:1]
	Normalize(s)
	if v := Validate(s, f, nil, false); HasErrors(v) {
		t.Fatalf("errors: %+v", v)
	}
	// Tighten the hook: b1's spot moves 0.5 s earlier but the recording does not.
	next, _, _ := ApplyOps(s, []Op{{Op: "updateClip", ID: "hook", Set: json.RawMessage(`{"out":3.5}`)}}, false)
	Normalize(next)
	found := false
	for _, is := range Validate(next, f, nil, false) {
		if is.Code == "beat_after_anchor" || is.Code == "beat_before_anchor" {
			found = true
		}
	}
	if !found {
		t.Fatal("continuous narration drifting from its line's spot must warn")
	}
	plan := Resolve(next, f, "rev_x", "prj_x", Hash(next))
	if len(plan.Audio) != 1 || plan.Audio[0].ID != "voiceover" || plan.Voiceover.Layout != LayoutContinuous {
		t.Fatalf("continuous plays as one element: %+v", plan.Audio)
	}
}
