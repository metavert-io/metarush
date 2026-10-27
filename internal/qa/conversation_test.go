package qa

import (
	"math"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// convCall is a 50 s two-person call: Maya (recording A) asks for 11 s,
// Sam (recording B, which froze for 2 s at 0:20) answers for 35 s.
func convCall(shots []editspec.Shot) (*editspec.Spec, *fakeFacts) {
	f := facts(true)
	talking(f, astA, 1, 12, "speaker_0")
	talking(f, astB, 13, 48, "speaker_0")
	s := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30, Platform: "generic"},
		Conversations: map[string]editspec.ConversationPin{"call": {ID: "cnv_01jabcdefghjkmnpqrstvwxyz0", Members: []editspec.ConversationMember{
			{ID: "maya", Name: "Maya", AssetID: astA},
			{ID: "sam", Name: "Sam", AssetID: astB},
		}}},
		Clips: []editspec.Clip{{ID: "talk", AssetID: editspec.ConvKey("call"), In: 0, Out: 50, Shots: shots}}}
	return s, f
}

func withConversation(s *editspec.Spec, plan *editspec.RenderPlan, f *fakeFacts, extra func(*Conversation)) func(*Input) {
	return func(in *Input) {
		for _, p := range editspec.ConversationPlays(s, plan, f) {
			cv := Conversation{ConversationPlay: p, Style: "podcast", MaxShot: 30}
			if extra != nil {
				extra(&cv)
			}
			in.Conversations = append(in.Conversations, cv)
		}
		in.Source = map[string]SourceEvidence{astA: {Available: true}, astB: {Available: true, Freezes: []Span{{From: 20, To: 22}}}}
		in.AssetNames = map[string]string{astA: "maya.mp4", astB: "sam.mp4"}
	}
}

// Sam's answer is 15 dB quieter than Maya's question in the render, the
// picture stays on Sam for 37 s, Sam's webcam freezes on screen, his sound
// drops out once and his recording was lined up unsurely: each is a finding
// in plain words, and the fixes apply to the cut.
func TestConversationChecks(t *testing.T) {
	ts := toolset(t)
	s, f := convCall([]editspec.Shot{{At: 0, Member: "maya"}, {At: 12.5, Member: "sam"}})
	plan := resolve(t, s, f)
	path := render(t, ts, "960x540", 50, "", "if(between(t\\,1\\,12)\\,0.3*sin(2*PI*440*t)\\,if(between(t\\,13\\,48)\\,0.05*sin(2*PI*440*t)\\,0))")
	rep := analyze(t, ts, path, plan, s, withConversation(s, plan, f, func(cv *Conversation) {
		cv.Dropouts = []Glitch{{Member: "sam", From: 30, To: 30.4}}
		cv.Unsure = []string{"Sam"}
	}))
	dump(t, rep)
	q := find(rep, "conversation_quiet")
	if q == nil || !strings.Contains(q.Message, "Sam sounds much quieter than Maya (about 1") {
		t.Fatalf("quiet voice: %+v", q)
	}
	if q.Fix == nil || len(q.Fix.Ops) != 1 || q.Fix.Ops[0].Op != "setMix" || q.Fix.Ops[0].Mix.VoiceFor("Sam") == nil || *q.Fix.Ops[0].Mix.VoiceFor("Sam").GainDB < 10 {
		t.Fatalf("quiet fix: %+v", q.Fix)
	}
	if len(rep.Measurements.Conversations) != 1 {
		t.Fatalf("measurements: %+v", rep.Measurements.Conversations)
	}
	m := rep.Measurements.Conversations[0]
	j, sm := m.People[0], m.People[1]
	if j.Loudness == nil || sm.Loudness == nil || math.Abs(*j.Loudness-*sm.Loudness-15.6) > 1.5 {
		t.Fatalf("loudness: %+v %+v", j, sm)
	}
	if math.Abs(sm.Speaking-35) > 0.6 || math.Abs(sm.OnScreen-37.5) > 0.1 || math.Abs(m.Longest-37.5) > 0.1 || m.Limit != 30 {
		t.Fatalf("Sam: %+v (%+v)", sm, m)
	}
	one := find(rep, "conversation_one_sided")
	if one == nil || !strings.Contains(one.Message, "The picture stays on Sam for 38 s") {
		t.Fatalf("one-sided: %+v", one)
	}
	fr := find(rep, "conversation_freeze")
	if fr == nil || math.Abs(fr.From-20) > 0.05 || math.Abs(fr.To-22) > 0.05 || !strings.Contains(fr.Message, "Sam's picture freezes for 2 s at 0:20") {
		t.Fatalf("freeze: %+v", fr)
	}
	if d := find(rep, "conversation_dropout"); d == nil || !strings.Contains(d.Message, "Sam's sound drops out for 0.4 s at 0:30") {
		t.Fatalf("dropout: %+v", d)
	}
	if sy := find(rep, "conversation_sync"); sy == nil || !strings.Contains(sy.Message, "Sam's recording may be out of step") {
		t.Fatalf("sync: %+v", sy)
	}
	for _, k := range []string{"conversation_unseen", "conversation_balance", "conversation_inaudible"} {
		if find(rep, k) != nil {
			t.Fatalf("unexpected %s: %+v", k, find(rep, k))
		}
	}
	// The fixes are edits of this cut: the freeze shows Maya over it and
	// goes back to Sam; the long shot gets everyone together in the middle.
	for _, k := range []string{"conversation_quiet", "conversation_freeze", "conversation_one_sided"} {
		fx := find(rep, k).Fix
		if fx == nil || len(fx.Ops) == 0 {
			t.Fatalf("%s has no edit: %+v", k, fx)
		}
		ns, _, err := editspec.ApplyOps(s, fx.Ops, false)
		if err != nil {
			t.Fatalf("%s fix does not apply: %v", k, err)
		}
		if k == "conversation_freeze" {
			sh := ns.Clips[0].Shots
			if len(sh) != 4 || sh[2].At != 20 || sh[2].Member != "maya" || sh[3].At != 22 || sh[3].Member != "sam" {
				t.Fatalf("freeze fix shots: %+v", sh)
			}
		}
		if k == "conversation_one_sided" {
			sh := ns.Clips[0].Shots
			if len(sh) != 4 || sh[2].Layout != editspec.LayoutSideBySide || math.Abs(sh[2].At-31.25) > 0.01 || sh[3].Member != "sam" {
				t.Fatalf("one-sided fix shots: %+v", sh)
			}
		}
	}
}

// With the picture on Maya throughout, Sam talks unseen and gets far less
// screen time than talking time.
func TestConversationUnseenTalker(t *testing.T) {
	ts := toolset(t)
	s, f := convCall([]editspec.Shot{{At: 0, Member: "maya"}})
	plan := resolve(t, s, f)
	path := render(t, ts, "960x540", 50, "", "if(between(t\\,1\\,12)+between(t\\,13\\,48)\\,0.3*sin(2*PI*440*t)\\,0)")
	rep := analyze(t, ts, path, plan, s, withConversation(s, plan, f, nil))
	dump(t, rep)
	u := find(rep, "conversation_unseen")
	if u == nil || !strings.Contains(u.Message, "Sam talks for 35 s") || u.Fix == nil || len(u.Fix.Ops) != 1 {
		t.Fatalf("unseen: %+v", u)
	}
	if b := find(rep, "conversation_balance"); b == nil || !strings.Contains(b.Message, "Sam does 76% of the talking but is on screen only 0% of the time") {
		t.Fatalf("balance: %+v", b)
	}
	if find(rep, "conversation_quiet") != nil || find(rep, "conversation_freeze") != nil {
		t.Fatalf("Sam is as loud as Maya and never on screen frozen")
	}
	// An off-camera interviewer is not expected on screen.
	rep = analyze(t, ts, path, plan, s, withConversation(s, plan, f, func(cv *Conversation) { cv.OffCamera = []string{"sam"} }))
	if find(rep, "conversation_unseen") != nil || find(rep, "conversation_balance") != nil {
		t.Fatalf("off camera on purpose: %v", kinds(rep, "warning"))
	}
}
