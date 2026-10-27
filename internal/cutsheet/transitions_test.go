package cutsheet

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

// twoPeople is footage where Sam talks in the first recording and Maya in the
// second, so phrases can name whose voice starts early or carries over.
func twoPeople() fakeInfo {
	a, b := specgen.Assets[0], specgen.Assets[1]
	return fakeInfo{
		footage: map[string]Footage{
			a: {Name: "interview-a.mov", Duration: 30, Kind: "video"},
			b: {Name: "broll-c.mp4", Duration: 30, Kind: "video"},
		},
		tx: map[string][]editspec.TranscriptSegment{
			a: {{From: 0, To: 20, Text: "So that is how it started.", Speaker: "Sam"}},
			b: {{From: 0, To: 20, Text: "And then what happened?", Speaker: "Maya"}},
		},
	}
}

func cutInto(tr *editspec.Transition) *editspec.Spec {
	s := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetVertical}, Clips: []editspec.Clip{
		{ID: "sam", AssetID: specgen.Assets[0], In: 2, Out: 6},
		{ID: "maya", AssetID: specgen.Assets[1], In: 3, Out: 7, Transition: tr},
	}}
	editspec.Normalize(s)
	return s
}

func arrowLine(text string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, "↳ ") {
			return l
		}
	}
	return ""
}

// Every transition type and sound mode prints as the words an editor would
// use, and reads back to the same cut.
func TestTransitionLines(t *testing.T) {
	for _, c := range []struct {
		tr   editspec.Transition
		info bool
		want string
	}{
		{editspec.Transition{Type: editspec.TransitionCrossfade, Duration: 0.5}, false, "↳ dissolve 0.5 s"},
		{editspec.Transition{Type: editspec.TransitionDipWhite, Duration: 1}, false, "↳ fade through white 1 s"},
		{editspec.Transition{Type: editspec.TransitionDip, Duration: 0.8}, false, "↳ fade through black 0.8 s"},
		{editspec.Transition{Type: editspec.TransitionSlide, Duration: 0.4, Direction: editspec.DirLeft}, false, "↳ slide left 0.4 s"},
		{editspec.Transition{Type: editspec.TransitionWipe, Duration: 0.5, Direction: editspec.DirUp, Easing: editspec.EaseLinear}, false, "↳ wipe up 0.5 s · even speed"},
		{editspec.Transition{Type: editspec.TransitionPush, Duration: 0.4, Direction: editspec.DirRight, Easing: editspec.EaseOut}, false, "↳ push right 0.4 s · slows down"},
		{editspec.Transition{Type: editspec.TransitionZoom, Duration: 0.4, Easing: editspec.EaseIn}, false, "↳ zoom 0.4 s · speeds up"},
		{editspec.Transition{Type: editspec.TransitionWhip, Duration: 0.3}, false, "↳ whip 0.3 s"},
		{editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundLead, Duration: 0.4}}, true, "↳ cut · Maya's voice starts 0.4 s early"},
		{editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundLead, Duration: 0.4}}, false, "↳ cut · voice starts 0.4 s early"},
		{editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundCarry, Duration: 0.3}}, true, "↳ cut · Sam's voice carries 0.3 s"},
		{editspec.Transition{Type: editspec.TransitionCrossfade, Duration: 0.5, Audio: &editspec.SoundTransition{Mode: editspec.SoundCarry, Duration: 0.3}}, false, "↳ dissolve 0.5 s · voice carries 0.3 s"},
		{editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundAuto}}, false, "↳ cut · sound changes in a pause"},
		{editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundAuto, Duration: 0.8, Blend: f64(0)}}, false, "↳ cut · sound changes in a pause within 0.8 s with no blend"},
		{editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundCut, Blend: f64(0.2)}}, false, "↳ cut · sound changes at the cut with a 0.2 s blend"},
		{editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundLead, Duration: 0.5, Blend: f64(0.25)}}, true, "↳ cut · Maya's voice starts 0.5 s early with a 0.25 s blend"},
		// What no phrase says stays exact in a block.
		{editspec.Transition{Type: editspec.TransitionCrossfade, Duration: 0.4, Direction: editspec.DirLeft}, false, `↳ dissolve 0.4 s · {direction: "left"}`},
		{editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundCut, Duration: 0.3}}, false, `↳ cut · {audio: {mode: "cut", duration: 0.3}}`},
		{editspec.Transition{Type: editspec.TransitionSlide, Duration: 0.4, Direction: "sideways"}, false, `↳ slide 0.4 s · {direction: "sideways"}`},
	} {
		tr := c.tr
		s := cutInto(&tr)
		o := Options{}
		if c.info {
			o.Info = twoPeople()
		}
		text := mustRoundTrip(t, c.want, s, o)
		if got := arrowLine(text); got != c.want {
			t.Errorf("printed %q, want %q", got, c.want)
		}
	}
}

// People write transitions in many ways; each one reads as the same cut.
func TestTransitionPhrasesRead(t *testing.T) {
	lead := func(d float64) *editspec.SoundTransition {
		return &editspec.SoundTransition{Mode: editspec.SoundLead, Duration: d}
	}
	carry := func(d float64) *editspec.SoundTransition {
		return &editspec.SoundTransition{Mode: editspec.SoundCarry, Duration: d}
	}
	for _, c := range []struct {
		line string
		want editspec.Transition
	}{
		{"↳ cut · J-cut 0.4 s", editspec.Transition{Type: "cut", Audio: lead(0.4)}},
		{"↳ cut · J cut 0.4s", editspec.Transition{Type: "cut", Audio: lead(0.4)}},
		{"↳ cut · Ada Lovelace's voice starts 0.5 s early", editspec.Transition{Type: "cut", Audio: lead(0.5)}},
		{"↳ cut · the sound comes in 0.2 seconds before", editspec.Transition{Type: "cut", Audio: lead(0.2)}},
		{"↳ cut · L cut 0.3 s", editspec.Transition{Type: "cut", Audio: carry(0.3)}},
		{"↳ cut · her voice carries over 0.25 s", editspec.Transition{Type: "cut", Audio: carry(0.25)}},
		{"↳ dissolve 0.5 s · O’Brien's voice runs on 0.3 s", editspec.Transition{Type: "crossfade", Duration: 0.5, Audio: carry(0.3)}},
		{"↳ cut · auto", editspec.Transition{Type: "cut", Audio: &editspec.SoundTransition{Mode: editspec.SoundAuto}}},
		{"↳ cut · sound auto", editspec.Transition{Type: "cut", Audio: &editspec.SoundTransition{Mode: editspec.SoundAuto}}},
		{"↳ cut · auto within 0.6 s with a 0.2 s blend", editspec.Transition{Type: "cut", Audio: &editspec.SoundTransition{Mode: editspec.SoundAuto, Duration: 0.6, Blend: f64(0.2)}}},
		{"↳ cut · the voices change in the nearest pause", editspec.Transition{Type: "cut", Audio: &editspec.SoundTransition{Mode: editspec.SoundAuto}}},
		{"↳ cut · sound changes with the picture blended over 0.1 s", editspec.Transition{Type: "cut", Audio: &editspec.SoundTransition{Mode: editspec.SoundCut, Blend: f64(0.1)}}},
		{"↳ crossfade 0.6 s · ease-in", editspec.Transition{Type: "crossfade", Duration: 0.6, Easing: editspec.EaseIn}},
		{"↳ dip to white 1 s", editspec.Transition{Type: "dip-to-white", Duration: 1}},
		{"↳ dip-to-white 1 s", editspec.Transition{Type: "dip-to-white", Duration: 1}},
		{"↳ whip pan 0.3 s · linear", editspec.Transition{Type: "whip", Duration: 0.3, Easing: editspec.EaseLinear}},
		{"↳ Slide Right 0.4 s · Slows Down", editspec.Transition{Type: "slide", Duration: 0.4, Direction: editspec.DirRight, Easing: editspec.EaseOut}},
		{"=> push down 0.4 s", editspec.Transition{Type: "push", Duration: 0.4, Direction: editspec.DirDown}},
	} {
		s, probs := Parse(sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n"+c.line+"\n2. b: interview-a 0:03 → 0:04\n"), ParseOptions{})
		if HasErrors(probs) {
			t.Errorf("%s: %s", c.line, problemsText(probs))
			continue
		}
		got := s.Spec.Clips[1].Transition
		if got == nil || !jsonEqual(*got, c.want) {
			t.Errorf("%s: got %+v (audio %+v), want %+v", c.line, got, got.Audio, c.want)
		}
	}
	// The sound said twice (or a phrase and the block) is an error, not a pick.
	for _, line := range []string{"↳ cut · auto · voice carries 0.3 s", `↳ slide left 0.4 s · {direction: "right"}`, "↳ cut · slows down · smooth"} {
		_, probs := Parse(sheet("## Clips\n1. a: interview-a 0:01 → 0:02\n"+line+"\n2. b: interview-a 0:03 → 0:04\n"), ParseOptions{})
		if !hasCode(probs, "duplicate") {
			t.Errorf("%s: expected a duplicate error, got %s", line, problemsText(probs))
		}
	}
}

// A changed ↳ line is saved as one setTransition (so the clip before it is
// covered by locks and conflicts); other clip changes stay updateClip.
func TestSheetTransitionBecomesSetTransition(t *testing.T) {
	base := cutInto(nil)
	text := Print(base, Options{})
	edited := strings.Replace(text, "\n2. maya:", "\n↳ cut · voice starts 0.4 s early\n\n2. maya:", 1)
	sh, probs := Parse(edited, ParseOptions{})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	ops, err := editspec.DiffOps(base, sh.Spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || ops[0].Op != "setTransition" || ops[0].ID != "maya" || ops[0].Transition.Audio.Mode != editspec.SoundLead {
		t.Fatalf("expected one setTransition into maya, got %+v", ops)
	}
	// Changing it back to no line removes it the same way.
	back, err := editspec.DiffOps(sh.Spec, base)
	if err != nil || len(back) != 1 || back[0].Op != "setTransition" || back[0].Transition != nil {
		t.Fatalf("expected a setTransition back to a plain cut, got %+v (%v)", back, err)
	}
	// A locked clip before the cut refuses a sound change that reaches into it.
	locked := editspec.Clone(base)
	locked.Clips[0].Locked = true
	if _, _, err := editspec.ApplyOps(locked, ops, false); err == nil || !strings.Contains(err.Error(), "touches “sam”, which is locked") {
		t.Fatalf("expected the locked clip before the cut to refuse, got %v", err)
	}
}

// Speech leveling and the blend at cuts are settings of the mix.
func TestMixSoundSettings(t *testing.T) {
	yes, blend := true, 0.1
	s := cutInto(nil)
	s.Mix = &editspec.Mix{LevelSpeech: &yes, CutBlend: &blend}
	text := mustRoundTrip(t, "mix", s, Options{})
	for _, want := range []string{"even out voices: on", "blend at cuts: 0.1 s"} {
		if !strings.Contains(text, want) {
			t.Errorf("expected %q in:\n%s", want, text)
		}
	}
	alt := strings.Replace(strings.Replace(text, "even out voices: on", "level speech: off", 1), "blend at cuts: 0.1 s", "blend at cuts: 0.25s", 1)
	sh, probs := Parse(alt, ParseOptions{})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	if m := sh.Spec.Mix; m == nil || m.LevelSpeech == nil || *m.LevelSpeech || m.CutBlend == nil || *m.CutBlend != 0.25 {
		t.Fatalf("unexpected mix %+v", sh.Spec.Mix)
	}
	got := Describe(s, sh.Spec, nil)
	if !strings.Contains(strings.Join(got, " "), "no longer evened out") || !strings.Contains(strings.Join(got, " "), "blends over 0.25 s") {
		t.Fatalf("changes not described: %v", got)
	}
}
