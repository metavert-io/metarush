package editspec

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
)

const astSam = "ast_01jabcdefghjkmnpqrstvwxyz4"
const astMaya = "ast_01jabcdefghjkmnpqrstvwxyz5"

// words builds word timings: pairs of (text, from, to).
func words(spec ...any) []TranscriptWord {
	var out []TranscriptWord
	for i := 0; i+2 < len(spec); i += 3 {
		out = append(out, TranscriptWord{Text: spec[i].(string), From: spec[i+1].(float64), To: spec[i+2].(float64)})
	}
	return out
}

type splitFacts struct {
	loudFacts
	rng map[string]float64 // assetId -> LUFS for any range
}

func (f splitFacts) RangeLoudness(id string, from, to float64) (float64, bool) {
	v, ok := f.rng[id]
	return v, ok
}

// conversationFacts: Sam (one recording) and Maya (another), each speaking in
// sentences with pauses; b-roll without sound.
func conversationFacts() splitFacts {
	f := facts()
	f.assets[astSam] = AssetFacts{ID: astSam, Name: "sam.mov", Kind: "video", State: "available", InProject: true, Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, TranscriptPrecision: "word"}
	f.assets[astMaya] = AssetFacts{ID: astMaya, Name: "maya.mov", Kind: "video", State: "available", InProject: true, Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, TranscriptPrecision: "word"}
	f.tx[astSam] = []TranscriptSegment{
		{From: 1.0, To: 2.4, Speaker: "Sam", Text: "So how did it all start?", Words: words("So", 1.0, 1.2, "how", 1.25, 1.5, "did", 1.55, 1.75, "it", 1.8, 1.85, "all", 1.9, 2.05, "start?", 2.1, 2.4)},
		{From: 4.0, To: 5.8, Speaker: "Sam", Text: "Really, tell me.", Words: words("Really,", 4.0, 4.5, "tell", 5.0, 5.3, "me.", 5.35, 5.8)},
		{From: 10.2, To: 11.4, Speaker: "Sam", Text: "That is amazing.", Words: words("That", 10.2, 10.4, "is", 10.45, 10.6, "amazing.", 10.65, 11.4)},
	}
	f.tx[astMaya] = []TranscriptSegment{
		{From: 3.15, To: 4.7, Speaker: "Maya", Text: "It started in my garage.", Words: words("It", 3.15, 3.3, "started", 3.35, 3.75, "in", 3.8, 3.9, "my", 3.95, 4.1, "garage.", 4.15, 4.7)},
		{From: 6.6, To: 8.0, Speaker: "Maya", Text: "With a broken drone.", Words: words("With", 6.6, 6.8, "a", 6.85, 6.9, "broken", 6.95, 7.4, "drone.", 7.45, 8.0)},
	}
	return splitFacts{loudFacts: loudFacts{fakeFacts: f, loud: map[string][2]float64{astSam: {-20, -6}, astMaya: {-30, -16}, astA: {-20, -6}}}, rng: map[string]float64{}}
}

func planClipByID(p *RenderPlan, id string) *PlanClip {
	for i := range p.Clips {
		if p.Clips[i].ID == id {
			return &p.Clips[i]
		}
	}
	return nil
}

func issueByCode(is []Issue, code string) *Issue {
	for i := range is {
		if is[i].Code == code {
			return &is[i]
		}
	}
	return nil
}

func TestTransitionLibraryTimeline(t *testing.T) {
	s := &Spec{Output: Output{Preset: PresetLandscape, FPS: 30}, Clips: []Clip{
		{ID: "c1", AssetID: astA, In: 1, Out: 5, Transition: &Transition{Type: TransitionSlide, Duration: 0.4}}, // first: becomes a fade in
		{ID: "c2", AssetID: astA, In: 6, Out: 10, Transition: &Transition{Type: TransitionWipe, Duration: 0.5, Direction: DirRight}},
		{ID: "c3", AssetID: astA, In: 10, Out: 14, Transition: &Transition{Type: TransitionPush, Duration: 0.4}},
		{ID: "c4", AssetID: astA, In: 14, Out: 18, Transition: &Transition{Type: TransitionZoom, Duration: 0.3, Direction: DirUp}},
		{ID: "c5", AssetID: astA, In: 18, Out: 22, Transition: &Transition{Type: TransitionWhip, Duration: 0.2, Direction: DirUp, Easing: EaseIn}},
		{ID: "c6", AssetID: astA, In: 22, Out: 26, Transition: &Transition{Type: TransitionDipWhite, Duration: 0.8}},
		{ID: "c7", AssetID: astA, In: 26, Out: 29, Transition: &Transition{Type: TransitionCrossfade, Duration: 0.5, Easing: EaseLinear}},
	}}
	Normalize(s)
	clips, total := Timeline(s)
	if c := clips[0].TransitionIn; c.Type != TransitionFade || c.Direction != "" || c.Easing != "" || c.DurationInFrames != 12 {
		t.Fatalf("first clip: %+v", c)
	}
	// Overlaps: wipe 15, push 12, zoom 9, whip 6; crossfade 15; dip-to-white none.
	want := []int{0, 120 - 15, 225 - 12, 333 - 9, 444 - 6, 558, 678 - 15}
	for i, w := range want {
		if clips[i].From != w {
			t.Fatalf("clip %d from %d want %d (%+v)", i, clips[i].From, w, clips[i].TransitionIn)
		}
	}
	if total != 663+90 {
		t.Fatalf("total %d", total)
	}
	if tr := clips[1].TransitionIn; tr.Direction != DirRight || tr.Easing != EaseInOut {
		t.Fatalf("wipe: %+v", tr)
	}
	if tr := clips[3].TransitionIn; tr.Direction != "" {
		t.Fatalf("zoom has no direction: %+v", tr)
	}
	if tr := clips[4].TransitionIn; tr.Direction != DirUp || tr.Easing != EaseIn {
		t.Fatalf("whip: %+v", tr)
	}
	if tr := clips[6].TransitionIn; tr.Easing != EaseLinear || tr.Direction != "" {
		t.Fatalf("dissolve with an easing: %+v", tr)
	}
	// The outgoing shot moves only for push, zoom and whip.
	for i, wantOut := range []string{"", "push", "zoom", "whip", "", "", ""} {
		got := ""
		if clips[i].TransitionOut != nil {
			got = clips[i].TransitionOut.Type
		}
		if got != wantOut {
			t.Fatalf("clip %d transitionOut %q want %q", i, got, wantOut)
		}
	}
	if o := clips[1].TransitionOut; o.DurationInFrames != 12 || o.Direction != DirLeft {
		t.Fatalf("push out: %+v", o)
	}
	if clips[4].FadeOutFrames != 12 {
		t.Fatalf("dip-to-white sets the previous fade-out: %d", clips[4].FadeOutFrames)
	}
	issues := Validate(s, facts(), nil, false)
	if is := issueByCode(issues, "direction_ignored"); is == nil || !strings.Contains(is.Message, "zoom doesn't move in a direction") {
		t.Fatalf("direction on a zoom: %+v", issues)
	}
	if HasErrors(issues) {
		t.Fatalf("errors: %+v", issues)
	}
}

func TestTransitionClampWarnsInPlainWords(t *testing.T) {
	s := &Spec{Output: Output{Preset: PresetLandscape}, Clips: []Clip{
		{ID: "a", AssetID: astA, In: 1, Out: 2},
		{ID: "b", AssetID: astA, In: 5, Out: 9, Label: "snow field", Transition: &Transition{Type: TransitionPush, Duration: 2}},
	}}
	Normalize(s)
	is := issueByCode(Validate(s, facts(), nil, false), "transition_clamped")
	if is == nil || is.Message != "The push into the “snow field” shot is shortened to 0.5 s so it fits the shots around it." {
		t.Fatalf("clamp warning: %+v", is)
	}
}

// conversation: Sam asks (his question ends at 2.4 s of his recording, the
// picture cuts at 3.0), Maya answers (her first word at 3.15 s of hers, her
// picture starts at 3.4, inside "started"), then back to Sam.
func conversation(maya, sam2 *SoundTransition) *Spec {
	s := &Spec{Output: Output{Preset: PresetLandscape, FPS: 30}, Clips: []Clip{
		{ID: "sam", AssetID: astSam, In: 0.5, Out: 3.0},
		{ID: "maya", AssetID: astMaya, In: 3.4, Out: 6.0, Transition: &Transition{Type: TransitionCut, Audio: maya}},
		{ID: "sam2", AssetID: astSam, In: 9.5, Out: 12.0, Transition: &Transition{Type: TransitionCut, Audio: sam2}},
	}}
	Normalize(s)
	return s
}

func TestSplitEditsMoveTheSoundNotThePicture(t *testing.T) {
	f := conversationFacts()
	s := conversation(&SoundTransition{Mode: SoundLead, Duration: 0.4}, &SoundTransition{Mode: SoundCarry, Duration: 0.3})
	plan := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	sam, maya, sam2 := planClipByID(plan, "sam"), planClipByID(plan, "maya"), planClipByID(plan, "sam2")
	// The picture is where it always was.
	if sam.From != 0 || maya.From != 75 || sam2.From != 153 {
		t.Fatalf("picture moved: %d %d %d", sam.From, maya.From, sam2.From)
	}
	// J-cut: Maya's sound starts 12 frames before her picture; Sam's fades out over 3 frames from there.
	if a := maya.Audio; a == nil || a.From != 63 || a.BlendInFrames != 3 || a.TrimBefore != 102-12 {
		t.Fatalf("maya sound window: %+v", maya.Audio)
	}
	if a := sam.Audio; a == nil || a.From != 0 || a.DurationInFrames != 66 || a.BlendOutFrames != 3 {
		t.Fatalf("sam sound window: %+v", sam.Audio)
	}
	if ta := maya.TransitionIn.Audio; ta == nil || ta.Result != SoundLead || ta.EditFrame != 63 || ta.CutFrame != 75 || ta.BlendFrames != 3 || ta.Limited != "" {
		t.Fatalf("maya cut audio: %+v", maya.TransitionIn.Audio)
	}
	// L-cut: Maya's sound carries 9 frames past the cut into Sam's second shot, then blends.
	if a := maya.Audio; a.From+a.DurationInFrames != sam2.From+9+3 || a.BlendOutFrames != 3 {
		t.Fatalf("maya carry: %+v (sam2 from %d)", a, sam2.From)
	}
	if a := sam2.Audio; a == nil || a.From != sam2.From+9 || a.TrimBefore != int(math.Round(9.5*30))+9 {
		t.Fatalf("sam2 sound window: %+v", sam2.Audio)
	}
	// The picture plays silently when the sound has its own window: no clip envelope.
	if len(maya.Gain) != 0 || len(maya.Audio.Gain) == 0 {
		t.Fatalf("envelopes: clip %v, sound %v", maya.Gain, maya.Audio.Gain)
	}
	// Linear blends at frame centers: in 0.25, 0.5, 0.75 then full; out the mirror.
	for k, want := range []float64{0.25, 0.5, 0.75, 1} {
		if g := GainAt(maya.Audio.Gain, float64(k)); math.Abs(g-want) > 0.002 {
			t.Fatalf("maya fade-in frame %d: %.4f want %.2f", k, g, want)
		}
	}
	for k, want := range []float64{0.75, 0.5, 0.25} {
		if g := GainAt(sam.Audio.Gain, float64(63+k)); math.Abs(g-want) > 0.002 {
			t.Fatalf("sam fade-out frame %d: %.4f want %.2f", k, g, want)
		}
	}
	// Captions and the speech check follow the sound: Maya's first word ("It", at
	// 3.15 s of her recording) is heard and captioned before we see her.
	s.Captions = &Captions{Enabled: true}
	Normalize(s)
	plan = Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	var first *PlanWord
	for _, l := range plan.Captions.Lines {
		for i := range l.Words {
			if l.Words[i].Text == "It" {
				first = &l.Words[i]
			}
		}
	}
	if first == nil || first.From != 75+int(math.Round((3.15-3.4)*30)) {
		t.Fatalf("caption of the early words: %+v", first)
	}
	words, _, edges := ExpectedSpeech(s, f)
	got := map[string]SpeechWord{}
	for _, w := range words {
		got[w.Text+"/"+w.ClipID] = w
	}
	if w, ok := got["It/maya"]; !ok || math.Abs(w.From-67.0/30) > 1e-6 {
		t.Fatalf("speech check expects Maya's early words before the cut: %+v", got)
	}
	if !containsF(edges, 63.0/30) || !containsF(edges, float64(sam2.From+12)/30) {
		t.Fatalf("edges follow the sound: %v", edges)
	}
	// Ducking under dialogue follows the sound too: after silent b-roll, Maya's
	// voice ducks the music from when it starts, before her picture.
	br := &Spec{Output: Output{Preset: PresetLandscape, FPS: 30}, Clips: []Clip{
		{ID: "broll", AssetID: astB, In: 0, Out: 2.5},
		{ID: "maya", AssetID: astMaya, In: 3.4, Out: 6.0, Transition: &Transition{Type: TransitionCut, Audio: &SoundTransition{Mode: SoundLead, Duration: 0.4}}},
	}}
	Normalize(br)
	if p := Resolve(Clone(br), f, "rev_x", "prj_x", Hash(br)); len(p.DialogueRanges) == 0 || p.DialogueRanges[0].From >= 75 || p.DialogueRanges[0].From < 63 {
		t.Fatalf("dialogue ranges: %+v", p.DialogueRanges)
	}
}

func containsF(xs []float64, v float64) bool {
	for _, x := range xs {
		if math.Abs(x-v) < 1e-6 {
			return true
		}
	}
	return false
}

func TestSplitClampedByFootageSaysWhy(t *testing.T) {
	f := conversationFacts()
	s := conversation(&SoundTransition{Mode: SoundLead, Duration: 0.4}, nil)
	s.Clips[1].In = 0.2 // only 0.2 s of Maya's recording before her clip
	s.Clips[1].Out = 3.4
	plan := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	maya := planClipByID(plan, "maya")
	if ta := maya.TransitionIn.Audio; ta == nil || ta.Limited != "footage" || ta.RequestedFrames != 12 || maya.Audio.From != maya.From-6 || maya.Audio.TrimBefore != 0 {
		t.Fatalf("lead limited by footage: %+v / %+v", maya.TransitionIn.Audio, maya.Audio)
	}
	is := issueByCode(Validate(s, f, nil, false), "split_limited")
	if is == nil || is.Message != "Maya's clip has only 0.2 s of sound before it starts, so Maya's voice starts 0.2 s early instead of 0.4 s." {
		t.Fatalf("plain warning: %+v", is)
	}
	// A carry past the end of the recording: Maya's clip ends 0.2 s before it does.
	s = conversation(nil, &SoundTransition{Mode: SoundCarry, Duration: 0.5})
	a := f.assets[astMaya]
	a.Duration = 6.2
	f.assets[astMaya] = a
	plan = Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	if ta := planClipByID(plan, "sam2").TransitionIn.Audio; ta == nil || ta.Limited != "footage" || ta.EditFrame-ta.CutFrame != 3 {
		t.Fatalf("carry limited: %+v", ta)
	}
	is = issueByCode(Validate(s, f, nil, false), "split_limited")
	if is == nil || is.Message != "Maya's clip has only 0.2 s of sound after it ends, so Maya's voice carries over 0.1 s instead of 0.5 s." {
		t.Fatalf("carry warning: %+v", is)
	}
	// No sound at all before the clip.
	s = conversation(&SoundTransition{Mode: SoundLead, Duration: 0.4}, nil)
	s.Clips[1].In = 0
	is = issueByCode(Validate(s, f, nil, false), "split_limited")
	if is == nil || !strings.Contains(is.Message, "has no sound before it starts, so Maya's voice starts with the picture instead of 0.4 s early") {
		t.Fatalf("no footage: %+v", is)
	}
}

func TestAutoSplitFindsThePause(t *testing.T) {
	f := conversationFacts()
	// Sam's picture ends at 4.3 s, in the middle of "Really," (4.0–4.5): auto lets
	// his voice finish the word, then switches in the pause before "tell" (5.0).
	s := conversation(&SoundTransition{Mode: SoundAuto}, nil)
	s.Clips[0].Out = 4.3
	s.Clips[1].In, s.Clips[1].Out = 5.0, 6.4 // Maya's picture starts in her pause
	plan := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	ta := planClipByID(plan, "maya").TransitionIn.Audio
	if ta == nil || ta.Result != SoundCarry || ta.NoPause {
		t.Fatalf("auto: %+v", ta)
	}
	editSrc := 0.5 + float64(ta.EditFrame)/30
	if editSrc < 4.5+wordPostMargin-1e-6 || editSrc+float64(ta.BlendFrames)/30 > 5.0-wordPreMargin+1e-6 {
		t.Fatalf("auto edit at Sam's %.3f s (blend %d)", editSrc, ta.BlendFrames)
	}
	for _, ci := range CutReport(s, f) {
		if len(ci.Clipped) > 0 || len(ci.DroppedPrev) > 0 || len(ci.DroppedNext) > 0 {
			t.Fatalf("auto cut words: %+v", ci)
		}
	}
	// Maya's picture starts in the middle of "started" (3.35–3.75): auto starts
	// her voice before "It" (3.15), the one word before it with no pause between.
	s = conversation(&SoundTransition{Mode: SoundAuto}, nil)
	s.Clips[1].In = 3.5
	plan = Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	ta = planClipByID(plan, "maya").TransitionIn.Audio
	if ta == nil || ta.Result != SoundLead || ta.NoPause {
		t.Fatalf("auto lead: %+v", ta)
	}
	if startSrc := 3.5 + float64(ta.EditFrame-ta.CutFrame)/30; startSrc+float64(ta.BlendFrames)/30 > 3.15-wordPreMargin+1e-6 {
		t.Fatalf("Maya's voice starts at %.3f s of her recording, inside It", startSrc)
	}
	for _, ci := range CutReport(s, f) {
		if len(ci.Clipped) > 0 {
			t.Fatalf("auto lead cut words: %+v", ci)
		}
	}
	// Nowhere to go (both talk across the cut, and auto may move only 0.1 s): flagged.
	s = conversation(&SoundTransition{Mode: SoundAuto, Duration: 0.1}, nil)
	s.Clips[0].Out = 2.0 // inside "all"
	s.Clips[1].In = 3.5  // inside "started"
	issues := Validate(s, f, nil, false)
	if is := issueByCode(issues, "split_no_pause"); is == nil || !strings.Contains(is.Message, "no pause near the cut at 0:01.5") {
		t.Fatalf("no pause: %+v", issues)
	}
	// A plain cut there is not second-guessed (no warning without a sound setting).
	s = conversation(nil, nil)
	s.Clips[0].Out = 2.0
	if is := issueByCode(Validate(s, f, nil, false), "split_cuts_word"); is != nil {
		t.Fatalf("plain cut warned: %+v", is)
	}
}

func TestSplitThatCutsWordsIsReported(t *testing.T) {
	f := conversationFacts()
	// A long lead cuts into Sam's question: "all" is cut short and "start?" is lost.
	s := conversation(&SoundTransition{Mode: SoundLead, Duration: 1.2}, nil)
	issues := Validate(s, f, nil, false)
	if is := issueByCode(issues, "split_drops_words"); is == nil || is.Message != "With Maya's voice starting 1.2 s early, the end of Sam's clip loses “start?”." {
		t.Fatalf("dropped words: %+v", issues)
	}
	if is := issueByCode(issues, "split_cuts_word"); is == nil || !strings.Contains(is.Message, "cuts “it all” short") {
		t.Fatalf("cut word: %+v", issues)
	}
	// Two voices at once during a dissolve whose sound follows the picture.
	s = conversation(nil, nil)
	s.Clips[0].Out = 4.4 // "Really," is still being said
	s.Clips[1].Transition = &Transition{Type: TransitionCrossfade, Duration: 1}
	if is := issueByCode(Validate(s, f, nil, false), "voices_overlap"); is == nil || !strings.HasPrefix(is.Message, "During the dissolve at") {
		t.Fatalf("overlap: %+v", is)
	}
}

func TestCutBlendsKeepStraightCutsClickFree(t *testing.T) {
	f := conversationFacts()
	s := conversation(nil, nil)
	blend := 0.1
	s.Mix = &Mix{CutBlend: &blend}
	plan := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	sam, maya := planClipByID(plan, "sam"), planClipByID(plan, "maya")
	// 3 frames centered on the cut: Maya's sound starts 1 frame early, Sam's ends 2 frames late.
	if maya.Audio == nil || maya.Audio.From != 74 || sam.Audio.From+sam.Audio.DurationInFrames != 77 {
		t.Fatalf("blend windows: %+v %+v", sam.Audio, maya.Audio)
	}
	if ta := maya.TransitionIn.Audio; ta == nil || ta.Mode != SoundCut || ta.Result != SoundCut || ta.BlendFrames != 3 {
		t.Fatalf("cut audio: %+v", ta)
	}
	// The very first sound fades in, the last fades out (3 frames each).
	if g := GainAt(sam.Audio.Gain, 0); math.Abs(g-0.25) > 0.002 {
		t.Fatalf("first frame %.4f", g)
	}
	last := planClipByID(plan, "sam2")
	if g := GainAt(last.Audio.Gain, float64(last.Audio.DurationInFrames-1)); math.Abs(g-0.25) > 0.002 {
		t.Fatalf("last frame %.4f", g)
	}
	// Cut settings alone keep the original mixer: no automatic ducking.
	if plan.Mix.Ducking.Auto || plan.Mix.CutBlendFrames != 3 || plan.Mix.LevelSpeech {
		t.Fatalf("mix: %+v", plan.Mix)
	}
	// An empty mix section (no cut settings) still turns the mixer on, as before.
	s.Mix = &Mix{}
	if p := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s)); !p.Mix.Ducking.Auto {
		t.Fatal("an empty mix section changed meaning")
	}
}

func TestSpeechLevelingEvensOutClips(t *testing.T) {
	f := conversationFacts()
	f.rng[astSam], f.rng[astMaya] = -19, -31 // the parts each clip plays
	s := conversation(nil, nil)
	on := true
	s.Mix = &Mix{LevelSpeech: &on}
	plan := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	sam, maya, sam2 := planClipByID(plan, "sam"), planClipByID(plan, "maya"), planClipByID(plan, "sam2")
	if sam.SpeechLUFS == nil || *sam.SpeechLUFS != -19 || maya.SpeechLUFS == nil || *maya.SpeechLUFS != -31 {
		t.Fatalf("measured: %v %v", sam.SpeechLUFS, maya.SpeechLUFS)
	}
	// Sam speaks longer (two clips), so his level is the median: Maya goes up 12 dB,
	// limited to 10 dB, and her peaks (-16 dBFS) leave room for it.
	if *plan.Mix.SpeechTargetLUFS != -19 || *sam.LevelDB != 0 || *maya.LevelDB != 10 || *sam2.LevelDB != 0 {
		t.Fatalf("levels: target %v sam %v maya %v sam2 %v", *plan.Mix.SpeechTargetLUFS, *sam.LevelDB, *maya.LevelDB, *sam2.LevelDB)
	}
	if g := envAt(maya.Gain, 40); math.Abs(g-dbToLin(10)) > 0.003 {
		t.Fatalf("maya gain %.4f", g)
	}
	// The peak guard: peaks at -4 dBFS leave 2.5 dB under -1 dBTP (less the codec margin).
	f.loud[astMaya] = [2]float64{-30, -4}
	plan = Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	if j := planClipByID(plan, "maya"); *j.LevelDB != 2.5 {
		t.Fatalf("peak-limited level %v", *j.LevelDB)
	}
	// Off (or absent, as in older cuts): nothing is leveled.
	off := false
	s.Mix = &Mix{LevelSpeech: &off}
	if p := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s)); planClipByID(p, "maya").LevelDB != nil || p.Mix.SpeechTargetLUFS != nil {
		t.Fatal("leveled with levelSpeech off")
	}
}

func TestNewCutDefaults(t *testing.T) {
	s := conversation(nil, nil)
	if !NewCutDefaults(s, nil) || !s.LevelsSpeech() || *s.Mix.CutBlend != DefaultCutBlend {
		t.Fatalf("first cut: %+v", s.Mix)
	}
	old := conversation(nil, nil)
	next := conversation(nil, nil)
	if NewCutDefaults(next, old) || next.Mix != nil {
		t.Fatal("a rewrite of an older cut must keep its sound")
	}
	off := false
	newer := conversation(nil, nil)
	newer.Mix = &Mix{LevelSpeech: &off}
	b := 0.2
	newer.Mix.CutBlend = &b
	rewrite := conversation(nil, nil)
	if !NewCutDefaults(rewrite, newer) || rewrite.LevelsSpeech() || *rewrite.Mix.CutBlend != 0.2 {
		t.Fatalf("rewrite keeps the base's settings: %+v", rewrite.Mix)
	}
	explicit := conversation(nil, nil)
	explicit.Mix = &Mix{LevelSpeech: &off}
	NewCutDefaults(explicit, nil)
	if explicit.LevelsSpeech() || explicit.Mix.CutBlend == nil {
		t.Fatalf("explicit setting kept: %+v", explicit.Mix)
	}
}

func TestSetTransitionOpLocksAndRebase(t *testing.T) {
	base := conversation(nil, nil)
	op := Op{Op: "setTransition", ID: "maya", Transition: &Transition{Type: TransitionCut, Audio: &SoundTransition{Mode: SoundLead, Duration: 0.4}}}
	next, touched, err := ApplyOps(base, []Op{op}, false)
	if err != nil || next.Clips[1].Transition.Audio.Mode != SoundLead {
		t.Fatalf("apply: %v", err)
	}
	if !touched["maya"] || !touched["sam"] || !touched["cut:sam>maya"] {
		t.Fatalf("touched: %v", touched)
	}
	// A plain cut resets the transition.
	reset, _, _ := ApplyOps(next, []Op{{Op: "setTransition", ID: "maya", Transition: &Transition{Type: TransitionCut}}}, false)
	if reset.Clips[1].Transition != nil {
		t.Fatalf("reset: %+v", reset.Clips[1].Transition)
	}
	// The end of a clip.
	end, _, err := ApplyOps(base, []Op{{Op: "setTransition", ID: "sam2", Edge: "end", Transition: &Transition{Type: TransitionFade, Duration: 1.5}}}, false)
	if err != nil || end.Clips[2].FadeOut != 1.5 {
		t.Fatalf("end fade: %v %+v", err, end.Clips[2])
	}
	if _, _, err := ApplyOps(base, []Op{{Op: "setTransition", ID: "sam2", Edge: "end", Transition: &Transition{Type: TransitionWipe}}}, false); err == nil {
		t.Fatal("a wipe cannot end a clip")
	}
	// Locks: the clip itself, or the previous one when the transition reaches into it.
	locked, _, _ := ApplyOps(base, []Op{{Op: "lock", ID: "maya"}}, false)
	var le *LockedError
	if _, _, err := ApplyOps(locked, []Op{op}, false); !errors.As(err, &le) || le.ID != "maya" {
		t.Fatalf("locked target: %v", err)
	}
	prevLocked, _, _ := ApplyOps(base, []Op{{Op: "lock", ID: "sam"}}, false)
	if _, _, err := ApplyOps(prevLocked, []Op{op}, false); !errors.As(err, &le) || le.ID != "sam" {
		t.Fatalf("a split reaches into the locked previous clip: %v", err)
	}
	if _, _, err := ApplyOps(prevLocked, []Op{{Op: "setTransition", ID: "maya", Transition: &Transition{Type: TransitionFade, Duration: 0.5}}}, false); err != nil {
		t.Fatalf("a fade-in does not touch the previous clip: %v", err)
	}
	// Rebase: an unrelated change rebases; a change to either clip of the cut conflicts.
	other, _, _ := ApplyOps(base, []Op{{Op: "updateClip", ID: "sam2", Set: json.RawMessage(`{"out":11.5}`)}}, false)
	if r, conflicts, err := Rebase(base, other, []Op{op}, false); err != nil || len(conflicts) != 0 || r.Clips[2].Out != 11.5 {
		t.Fatalf("disjoint: %v %v", conflicts, err)
	}
	trimmed, _, _ := ApplyOps(base, []Op{{Op: "updateClip", ID: "sam", Set: json.RawMessage(`{"out":2.6}`)}}, false)
	if _, conflicts, _ := Rebase(base, trimmed, []Op{op}, false); len(conflicts) != 1 || conflicts[0] != "sam" {
		t.Fatalf("the outgoing clip changed: %v", conflicts)
	}
	// Someone reordered the shots: the cut the transition was written for is gone.
	moved, _, _ := ApplyOps(base, []Op{{Op: "moveClip", ID: "sam2", Before: "maya"}}, false)
	if _, conflicts, _ := Rebase(base, moved, []Op{op}, false); !contains(conflicts, "cut:sam>maya") {
		t.Fatalf("reordered: %v", conflicts)
	}
	d := Compare(base, next)
	if !strings.Contains(strings.Join(d.Summary, "; "), "1 transition changed") {
		t.Fatalf("diff summary: %v", d.Summary)
	}
}

// The cut rules hold however an op changes a transition: a clip update, a
// replacement or an insert that carries transitionIn, and removing a
// transition that reached into a locked clip.
func TestCutRulesForEveryOp(t *testing.T) {
	base := conversation(nil, nil)
	prevLocked, _, _ := ApplyOps(base, []Op{{Op: "lock", ID: "sam"}}, false)
	var le *LockedError
	dissolve := json.RawMessage(`{"transitionIn":{"type":"crossfade","duration":0.5}}`)
	if _, _, err := ApplyOps(prevLocked, []Op{{Op: "updateClip", ID: "maya", Set: dissolve}}, false); !errors.As(err, &le) || le.ID != "sam" {
		t.Fatalf("a dissolve set with updateClip reaches into the locked clip: %v", err)
	}
	if _, _, err := ApplyOps(prevLocked, []Op{{Op: "updateClip", ID: "maya", Set: json.RawMessage(`{"out":7.5}`)}}, false); err != nil {
		t.Fatalf("other fields of the next clip stay editable: %v", err)
	}
	ins := Clip{ID: "new", AssetID: base.Clips[0].AssetID, In: 1, Out: 2, Transition: &Transition{Type: TransitionCut, Audio: &SoundTransition{Mode: SoundCarry, Duration: 0.3}}}
	if _, _, err := ApplyOps(prevLocked, []Op{{Op: "insertClip", After: "sam", Clip: &ins}}, false); !errors.As(err, &le) || le.ID != "sam" {
		t.Fatalf("an inserted clip whose sound reaches back: %v", err)
	}
	ins.Transition = nil
	if _, _, err := ApplyOps(prevLocked, []Op{{Op: "insertClip", After: "sam", Clip: &ins}}, false); err != nil {
		t.Fatalf("a straight cut after a locked clip is fine: %v", err)
	}
	withDissolve, _, _ := ApplyOps(base, []Op{{Op: "setTransition", ID: "maya", Transition: &Transition{Type: TransitionCrossfade, Duration: 0.5}}}, false)
	withDissolve.Clips[0].Locked = true
	if _, _, err := ApplyOps(withDissolve, []Op{{Op: "setTransition", ID: "maya", Transition: nil}}, false); !errors.As(err, &le) || le.ID != "sam" {
		t.Fatalf("removing a dissolve changes the locked clip's end too: %v", err)
	}
	// A transition changed by updateClip touches the cut like setTransition.
	_, touched, err := ApplyOps(base, []Op{{Op: "updateClip", ID: "maya", Set: dissolve}}, false)
	if err != nil || !touched["sam"] || !touched["cut:sam>maya"] {
		t.Fatalf("touched %v (%v)", touched, err)
	}
	trimmed, _, _ := ApplyOps(base, []Op{{Op: "updateClip", ID: "sam", Set: json.RawMessage(`{"out":2.6}`)}}, false)
	if _, conflicts, _ := Rebase(base, trimmed, []Op{{Op: "updateClip", ID: "maya", Set: dissolve}}, false); !contains(conflicts, "sam") {
		t.Fatalf("a concurrent trim of the outgoing clip conflicts: %v", conflicts)
	}
}

// DiffOps writes a changed cut as setTransition (after any reorder, so it names
// the final pair); a plain-cut transition that setTransition would drop stays
// a field update so the result is exact.
func TestDiffOpsTransitions(t *testing.T) {
	base := conversation(nil, nil)
	next := Clone(base)
	next.Clips[1].Transition = &Transition{Type: TransitionCut, Audio: &SoundTransition{Mode: SoundLead, Duration: 0.4}}
	next.Clips[1].Out += 0.5
	ops, err := DiffOps(base, next)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, o := range ops {
		kinds = append(kinds, o.Op)
	}
	if strings.Join(kinds, ",") != "updateClip,setTransition" || strings.Contains(string(ops[0].Set), "transitionIn") {
		t.Fatalf("ops %v: %s", kinds, ops[0].Set)
	}
	// Reordered and given a transition: the move first, then the cut it now has.
	moved := Clone(base)
	moved.Clips[1], moved.Clips[2] = moved.Clips[2], moved.Clips[1]
	moved.Clips[1].Transition = &Transition{Type: TransitionCrossfade, Duration: 0.5}
	ops, err = DiffOps(base, moved)
	if err != nil {
		t.Fatal(err)
	}
	if last := ops[len(ops)-1]; last.Op != "setTransition" || last.ID != moved.Clips[1].ID {
		t.Fatalf("expected setTransition last, got %+v", ops)
	}
	// {type: cut} with nothing else: setTransition would store nothing, so it is a field update.
	none := Clone(base)
	none.Clips[1].Transition = nil
	plain := Clone(none)
	plain.Clips[1].Transition = &Transition{Type: TransitionCut}
	ops, err = DiffOps(none, plain)
	if err != nil || len(ops) != 1 || ops[0].Op != "updateClip" {
		t.Fatalf("plain cut: %+v %v", ops, err)
	}
	// And the other way round: {type: cut} to nothing is setTransition (it stores nil).
	ops, err = DiffOps(plain, none)
	if err != nil || len(ops) != 1 || ops[0].Op != "setTransition" || ops[0].Transition != nil {
		t.Fatalf("back to no transition: %+v %v", ops, err)
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// schema/transitions.json is the one transition catalog (CLI, dashboard picker).
func TestTransitionCatalogIsCurrent(t *testing.T) {
	b, err := os.ReadFile("../../schema/transitions.json")
	if err != nil {
		t.Fatalf("schema/transitions.json missing (run `metarush schema export --dir schema`): %v", err)
	}
	var got TransitionsCatalog
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, TransitionCatalog()) {
		t.Fatal("schema/transitions.json is stale; run `metarush schema export --dir schema`")
	}
	// Every spec transition type is in the catalog and vice versa.
	for _, ty := range []string{TransitionCut, TransitionFade, TransitionCrossfade, TransitionDip, TransitionDipWhite, TransitionWipe, TransitionSlide, TransitionPush, TransitionZoom, TransitionWhip} {
		if _, ok := TransitionByType(ty); !ok {
			t.Errorf("%s missing from the catalog", ty)
		}
	}
	if len(Transitions) != 10 {
		t.Fatalf("catalog has %d transitions", len(Transitions))
	}
}
