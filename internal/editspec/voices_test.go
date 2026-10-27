package editspec

import (
	"math"
	"strings"
	"testing"
)

const (
	astTalk    = "ast_01jabcdefghjkmnpqrstvwxyt1"
	astCopy    = "ast_01jabcdefghjkmnpqrstvwxyt2"
	astCopy2   = "ast_01jabcdefghjkmnpqrstvwxyt3"
	astBed     = "ast_01jabcdefghjkmnpqrstvwxyt4"
	astDlgTalk = "ast_01jabcdefghjkmnpqrstvwxyt5"
)

type soundFacts struct {
	fakeFacts
	beats map[string][]float64
	grids map[string]*MusicGrid
}

func (f soundFacts) Beats(id string) ([]float64, bool) { b, ok := f.beats[id]; return b, ok }
func (f soundFacts) MusicGrid(id string) (*MusicGrid, bool) {
	g, ok := f.grids[id]
	return g, ok
}

// A two-person recording: Maya 0.5–3.5 s and 6–8.5 s, Sam 4–5.5 s.
func talkFacts() soundFacts {
	return soundFacts{fakeFacts: fakeFacts{
		assets: map[string]AssetFacts{
			astTalk:    {ID: astTalk, Name: "talk.mov", Kind: "video", State: "available", InProject: true, Duration: 20, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30},
			astCopy:    {ID: astCopy, Name: "talk (clearer).wav", Kind: "audio", State: "available", Duration: 12, HasAudio: true},
			astCopy2:   {ID: astCopy2, Name: "talk (clearer 2).wav", Kind: "audio", State: "available", Duration: 5, HasAudio: true},
			astBed:     {ID: astBed, Name: "bed.m4a", Kind: "audio", State: "available", InProject: true, Duration: 30, HasAudio: true},
			astDlgTalk: {ID: astDlgTalk, Name: "lav.wav", Kind: "audio", State: "available", InProject: true, Duration: 20, HasAudio: true},
		},
		tx: map[string][]TranscriptSegment{
			astTalk: {
				{From: 0.5, To: 3.5, Text: "Hello there", Speaker: "Maya"},
				{From: 4, To: 5.5, Text: "Hi", Speaker: "Sam"},
				{From: 6, To: 8.5, Text: "Again", Speaker: "Maya"},
			},
			astDlgTalk: {{From: 0, To: 4, Text: "Lav", Speaker: "Sam"}},
		},
	}, beats: map[string][]float64{}}
}

func talkSpec() *Spec {
	s := &Spec{Output: Output{Preset: PresetLandscape, FPS: 30}, Clips: []Clip{{ID: "talk", AssetID: astTalk, In: 0, Out: 10}}}
	Normalize(s)
	return s
}

func TestSpeakerSpansGiveEveryMomentToTheNearestSpeaker(t *testing.T) {
	segs := talkFacts().tx[astTalk]
	got := SpeakerSpans(segs, 0, 10)
	want := []SpeakerSpan{{0, 3.75, "Maya"}, {3.75, 5.75, "Sam"}, {5.75, 10, "Maya"}}
	if len(got) != len(want) {
		t.Fatalf("spans %+v, want %+v", got, want)
	}
	for i := range want {
		if math.Abs(got[i].From-want[i].From) > 1e-9 || math.Abs(got[i].To-want[i].To) > 1e-9 || got[i].Speaker != want[i].Speaker {
			t.Fatalf("span %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if sp := SpeakerSpans(nil, 2, 3); len(sp) != 1 || sp[0].Speaker != "" {
		t.Fatalf("no segments: %+v", sp)
	}
	if got := SpeakersIn(segs, 4.2, 5); len(got) != 1 || got[0] != "Sam" {
		t.Fatalf("speakers in Sam's stretch: %v", got)
	}
}

// Maya a bit louder: only her stretches change, with short ramps at the edges.
func TestPersonLevelAppliesOnlyOverTheirSpeech(t *testing.T) {
	s := talkSpec()
	g := 6.0
	s.Mix = &Mix{Voices: []VoiceSound{{Name: "maya", GainDB: &g}}} // names match whatever the case
	plan := Resolve(s, talkFacts(), "", "", "")
	pc := plan.Clips[0]
	if len(pc.Gain) == 0 {
		t.Fatal("no gain envelope")
	}
	at := func(sec float64) float64 { return GainAt(pc.Gain, sec*30) }
	if v := at(2); math.Abs(v-dbToLin(6)) > 0.01 {
		t.Fatalf("inside Maya's speech the gain is %.3f, want %.3f", v, dbToLin(6))
	}
	if v := at(4.75); math.Abs(v-1) > 0.01 {
		t.Fatalf("inside Sam's speech the gain is %.3f, want 1", v)
	}
	if v := at(7); math.Abs(v-dbToLin(6)) > 0.01 {
		t.Fatalf("Maya's second stretch: %.3f", v)
	}
	// The change happens within the crossfade around the edge (3.75 s), not before.
	if v := at(3.6); math.Abs(v-dbToLin(6)) > 0.01 {
		t.Fatalf("before the ramp: %.3f", v)
	}
	if v := at(3.9); math.Abs(v-1) > 0.01 {
		t.Fatalf("after the ramp: %.3f", v)
	}
	// Legacy mixer behavior stays (no automatic ducking or loudness for a voice setting).
	if plan.Mix.Ducking.Auto || plan.Mix.Normalize {
		t.Fatalf("a voice setting switched the mixer: %+v", plan.Mix)
	}
	// No setting, no change: the same plan as without a mix.
	s2 := talkSpec()
	s2.Mix = &Mix{Voices: []VoiceSound{{Name: "Nobody", GainDB: &g}}}
	p2 := Resolve(s2, talkFacts(), "", "", "")
	p3 := Resolve(talkSpec(), talkFacts(), "", "", "")
	if !equalJSON(p2.Clips, p3.Clips) {
		t.Fatalf("a setting for someone who is not in the clip changed it")
	}
}

func TestClearerCopyPlaysAsTheClipsSound(t *testing.T) {
	s := talkSpec()
	s.Mix = &Mix{Voices: []VoiceSound{{Name: "Maya", Enhance: &Enhance{Mode: EnhanceClean}}},
		Enhanced: []EnhancedSound{{AssetID: astTalk, Start: 0, Variant: astCopy, Settings: "Maya: clean 100%"}}}
	plan := Resolve(s, talkFacts(), "", "", "")
	pc := plan.Clips[0]
	if pc.Audio == nil || pc.Audio.Variant == nil || pc.Audio.Variant.AssetID != astCopy {
		t.Fatalf("the clip does not play the copy: %+v", pc.Audio)
	}
	if pc.Audio.From != pc.From || pc.Audio.DurationInFrames != pc.DurationInFrames || pc.Audio.Variant.TrimBefore != pc.TrimBefore {
		t.Fatalf("the copy's timing differs from the clip's: %+v vs clip %d+%d trim %d", pc.Audio, pc.From, pc.DurationInFrames, pc.TrimBefore)
	}
	if len(pc.Gain) != 0 || len(pc.Audio.Gain) == 0 {
		t.Fatalf("the envelope belongs to the sound element")
	}
	found := false
	for _, id := range AssetIDs(s) {
		found = found || id == astCopy
	}
	if !found {
		t.Fatal("renders must fetch the copy")
	}
	if is := validateSound(s, talkFacts(), plan.Clips); len(is) != 0 {
		t.Fatalf("a matching copy is fine: %+v", is)
	}
	// A copy that starts later: trim is counted from the copy's start.
	s.Clips[0].In, s.Clips[0].Out = 3, 7
	s.Mix.Enhanced = []EnhancedSound{{AssetID: astTalk, Start: 2, Variant: astCopy2, Settings: "Maya: clean 100%"}}
	plan = Resolve(s, talkFacts(), "", "", "")
	pc = plan.Clips[0]
	if pc.Audio == nil || pc.Audio.Variant == nil || pc.Audio.Variant.TrimBefore != pc.TrimBefore-60 {
		t.Fatalf("copy from 2 s: %+v (clip trim %d)", pc.Audio, pc.TrimBefore)
	}
	// A copy that does not cover the clip is not used, and validation says so.
	s.Clips[0].In, s.Clips[0].Out = 0, 10
	plan = Resolve(s, talkFacts(), "", "", "")
	if plan.Clips[0].Audio != nil {
		t.Fatalf("a copy that covers only part of the clip must not play")
	}
	is := validateSound(s, talkFacts(), plan.Clips)
	if !hasIssue(is, "enhance_missing") {
		t.Fatalf("expected enhance_missing: %+v", is)
	}
	// Changed settings: the copy is out of date.
	s.Mix.Enhanced = []EnhancedSound{{AssetID: astTalk, Start: 0, Variant: astCopy, Settings: "Maya: clean 100%"}}
	st := 0.5
	s.Mix.Voices[0].Enhance.Strength = &st
	if is := validateSound(s, talkFacts(), plan.Clips); !hasIssue(is, "enhanced_outdated") {
		t.Fatalf("expected enhanced_outdated: %+v", is)
	}
}

func TestVoiceSettingsDescribeWhatShapesACopy(t *testing.T) {
	m := &Mix{Voices: []VoiceSound{
		{Name: "Sam", Enhance: &Enhance{Mode: EnhanceStudio}},
		{Name: "Maya", GainDB: f64p(3), Enhance: &Enhance{Mode: EnhanceClean, Strength: f64p(0.65), Off: true}},
		{AssetID: astTalk, Enhance: &Enhance{Mode: EnhanceCloud}},
	}}
	got := m.VoiceSettingsFor(astTalk, []string{"Sam", "Maya", "Nobody"})
	want := "Sam: studio 100%; everyone else: cloud 100%" // Maya's is off: her stretches stay original
	if got != want {
		t.Fatalf("settings %q, want %q", got, want)
	}
	if (&Mix{}).VoiceSettingsFor(astTalk, []string{"Sam"}) != "" {
		t.Fatal("nothing enhanced: empty")
	}
}

func TestDialogueTrackFollowsPersonLevelAndCopy(t *testing.T) {
	s := talkSpec()
	g := -6.0
	s.Audio = []AudioTrack{{ID: "lav", AssetID: astDlgTalk, Role: RoleDialogue, In: 0, Out: 4, At: 0}}
	s.Mix = &Mix{Voices: []VoiceSound{{Name: "Sam", GainDB: &g, Enhance: &Enhance{Mode: EnhanceClean}}},
		Enhanced: []EnhancedSound{{AssetID: astDlgTalk, Start: 0, Variant: astCopy2, Settings: "Sam: clean 100%"}}}
	plan := Resolve(s, talkFacts(), "", "", "")
	var lav *PlanAudio
	for i := range plan.Audio {
		if plan.Audio[i].ID == "lav" {
			lav = &plan.Audio[i]
		}
	}
	if lav == nil || lav.Variant == nil || lav.Variant.AssetID != astCopy2 {
		t.Fatalf("the dialogue track should play its copy: %+v", lav)
	}
	if v := GainAt(lav.Gain, 2*30); math.Abs(v-dbToLin(-6)) > 0.01 {
		t.Fatalf("Sam's level on the dialogue track: %.3f", v)
	}
}

func hasIssue(is []Issue, code string) bool {
	for _, i := range is {
		if i.Code == code {
			return true
		}
	}
	return false
}

func f64p(v float64) *float64 { return &v }

func TestNormalizeKeepsVoiceSettingsCanonical(t *testing.T) {
	s := talkSpec()
	s.Mix = &Mix{Voices: []VoiceSound{{Name: "Maya", GainDB: f64p(2.04), Enhance: &Enhance{Mode: EnhanceClean, Strength: f64p(0.8049)}}},
		Enhanced: []EnhancedSound{{AssetID: astTalk, Start: 3.7, Variant: astCopy}}}
	Normalize(s)
	if *s.Mix.Voices[0].GainDB != 2 || *s.Mix.Voices[0].Enhance.Strength != 0.8 || s.Mix.Enhanced[0].Start != 3 {
		t.Fatalf("not canonical: %+v %+v %+v", *s.Mix.Voices[0].GainDB, *s.Mix.Voices[0].Enhance.Strength, s.Mix.Enhanced[0].Start)
	}
	if !strings.Contains(Hash(s), "") {
		t.Fatal("hash")
	}
}

// A cut played inside another (D84) keeps its clearer voices: the sounds the
// outer plan takes over carry their copies, and the copies are fetched.
func TestNestedCutsKeepClearerCopies(t *testing.T) {
	p := &RenderPlan{
		Clips: []PlanClip{{ID: "talk", AssetID: astTalk, HasAudio: true, From: 0, DurationInFrames: 90, PlaybackRate: 1,
			Audio: &PlanClipAudio{From: 0, DurationInFrames: 90, TrimBefore: 30, Variant: &PlanVariant{AssetID: astCopy, TrimBefore: 30}}}},
		Audio: []PlanAudio{{ID: "dlg", AssetID: astDlgTalk, Role: RoleDialogue, DurationInFrames: 60, Variant: &PlanVariant{AssetID: astCopy2, TrimBefore: 0}}},
	}
	snds := innerSounds(p)
	if len(snds) != 2 || snds[0].variant == nil || snds[0].variant.AssetID != astCopy || snds[1].variant == nil || snds[1].variant.AssetID != astCopy2 {
		t.Fatalf("inner sounds: %+v", snds)
	}
	ids := PlanSourceIDs(p)
	for _, want := range []string{astTalk, astCopy, astDlgTalk, astCopy2} {
		found := false
		for _, id := range ids {
			found = found || id == want
		}
		if !found {
			t.Fatalf("%s is not fetched: %v", want, ids)
		}
	}
}
