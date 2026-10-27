package voiceover

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

func (f *fx) head() string {
	f.t.Helper()
	var p models.Project
	if err := f.a.DB.GetByID(context.Background(), store.CProjects, f.a.WorkspaceID(), f.project, &p); err != nil {
		f.t.Fatal(err)
	}
	return p.Branches["main"].Head
}

// The mixer previews a change as the plan the dashboard plays (nothing saved),
// and the saved change resolves to exactly that plan.
func TestMixPreviewAndSet(t *testing.T) {
	f := setup(t)
	rev := f.cut()
	before := f.plan(rev)
	gain := func(pc *editspec.PlanClip) []editspec.GainPoint { _, _, g := pc.Sound(); return g }
	if gain(&before.Clips[0]) == nil {
		t.Fatal("clips carry gain envelopes")
	}
	// The test footage's sound is mono and has no proxy yet: the preview plays
	// the original, and says it is mono (D64).
	if ch := before.SourceChannels[f.clip]; ch != 1 {
		t.Fatalf("source channels: %+v", before.SourceChannels)
	}
	in := MixSetIn{RevisionID: rev, Buses: []BusChange{{Bus: editspec.BusSource, GainDB: fptr(-6)}}}
	pv, err := call[MixPreviewOut](t, f.a, "mix.preview", in)
	if err != nil {
		t.Fatal(err)
	}
	// Compare in the middle of the first clip (away from its blends).
	ratio := editspec.GainAt(gain(&pv.Plan.Clips[0]), 60) / editspec.GainAt(gain(&before.Clips[0]), 60)
	if math.Abs(ratio-math.Pow(10, -6.0/20)) > 0.002 {
		t.Fatalf("camera sound 6 dB quieter: ratio %.4f", ratio)
	}
	if f.head() != rev {
		t.Fatal("a preview saves nothing")
	}
	if len(pv.Mix.Buses) != 4 || pv.Mix.Buses[0].Level != "6 dB quieter" || !strings.Contains(pv.Summary, "quieter") {
		t.Fatalf("the change in plain words: %+v", pv.Mix.Buses)
	}
	if pv.Plan.SourceChannels[f.clip] != 1 || pv.Plan.Sources[f.clip] == "" {
		t.Fatalf("preview plans carry their sources: %+v", pv.Plan.Sources)
	}
	in.Apply = true
	set, err := call[MixSetOut](t, f.a, "mix.set", in)
	if err != nil || set.Applied == nil || set.Applied.Problem != "" || !strings.Contains(set.Summary, "in the cut now as version") {
		t.Fatalf("set: %+v %v", set, err)
	}
	after := f.plan(set.Applied.RevisionID)
	for i := range after.Clips {
		if !equalGains(gain(&after.Clips[i]), gain(&pv.Plan.Clips[i])) {
			t.Fatalf("clip %d: the saved mix plays differently from its preview\n saved %v\n preview %v", i, gain(&after.Clips[i]), gain(&pv.Plan.Clips[i]))
		}
	}
	sp := f.spec(set.Applied.RevisionID)
	if sp.Mix == nil || sp.Mix.Source == nil || sp.Mix.Source.GainDB == nil || *sp.Mix.Source.GainDB != -6 || sp.Mix.LevelSpeech == nil {
		t.Fatalf("saved mix: %+v", sp.Mix)
	}
	// Back to normal removes the part's setting; one clip's own sound goes off.
	rev2 := set.Applied.RevisionID
	set, err = call[MixSetOut](t, f.a, "mix.set", MixSetIn{RevisionID: rev2, Buses: []BusChange{{Bus: editspec.BusSource, GainDB: fptr(0)}},
		Clips: []ClipSound{{Clip: "closing", Muted: true}}, Apply: true})
	if err != nil || set.Applied == nil || set.Applied.Problem != "" {
		t.Fatalf("set 2: %+v %v", set, err)
	}
	sp = f.spec(set.Applied.RevisionID)
	if sp.Mix.Source != nil || !sp.Clips[1].Muted || sp.Clips[0].Muted {
		t.Fatalf("normal level and one clip muted: mix %+v clips %+v", sp.Mix, sp.Clips)
	}
	if !strings.Contains(strings.Join(set.Changes, "|"), "turn off the sound of the shot “closing”") {
		t.Fatalf("changes: %v", set.Changes)
	}
	// The whole cut's camera sound off, and solo narration: in plain words, nothing applied without apply.
	chk, err := call[MixSetOut](t, f.a, "mix.set", MixSetIn{RevisionID: set.Applied.RevisionID, MuteSource: boolPtr(true), Buses: []BusChange{{Bus: "voiceover", Solo: boolPtr(true)}}})
	if err != nil || chk.Result == nil || !chk.Result.Check.Valid || chk.Applied != nil || f.head() != set.Applied.RevisionID {
		t.Fatalf("check only: %+v %v", chk, err)
	}
	if low := strings.ToLower(chk.Summary); !strings.Contains(low, "turn off the camera sound") || !strings.Contains(low, "hear only the narration") {
		t.Fatalf("summary: %q", chk.Summary)
	}
	if _, err := call[MixPreviewOut](t, f.a, "mix.preview", MixSetIn{RevisionID: rev, Apply: true, Buses: in.Buses}); !apperr.Is(err, apperr.InvalidInput) {
		t.Fatalf("a preview never applies: %v", err)
	}
	// The platform's usual loudness again: the target goes away.
	lufs := -16.0
	set, err = call[MixSetOut](t, f.a, "mix.set", MixSetIn{RevisionID: f.head(), TargetLUFS: &lufs, Apply: true})
	if err != nil || f.spec(set.Applied.RevisionID).Mix.Loudness.TargetLUFS == nil {
		t.Fatalf("target: %+v %v", set, err)
	}
	set, err = call[MixSetOut](t, f.a, "mix.set", MixSetIn{RevisionID: f.head(), LoudnessAuto: true, Apply: true})
	if err != nil || f.spec(set.Applied.RevisionID).Mix.Loudness != nil {
		t.Fatalf("automatic loudness: %+v %v", f.spec(set.Applied.RevisionID).Mix, err)
	}
}

func equalGains(a, b []editspec.GainPoint) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Lines are added, changed, moved and removed through one op; a line dropped
// at a point of the video is tied to what plays there (conversational), or with
// exact to the clock.
func TestLineOpPlacesConversationally(t *testing.T) {
	f := setup(t)
	rev := f.cut("Out here, the light arrives slowly.", "And then it is everywhere.")
	to := 7.04 // 2.04 s into "closing" (which starts at 5.0)
	mv, err := call[LineOut](t, f.a, "voiceover.line", LineIn{RevisionID: rev, Action: "move", Line: "2", To: &to, Apply: true})
	if err != nil || mv.Applied == nil || mv.Applied.Problem != "" {
		t.Fatalf("move: %+v %v", mv, err)
	}
	if mv.Placement != "2.0 s after shot 2 (“closing”) starts" {
		t.Fatalf("placement: %q", mv.Placement)
	}
	a := f.spec(mv.Applied.RevisionID).Voiceover.Beats[1].Anchor
	if a.Kind != editspec.AnchorClip || a.ClipID != "close" || a.Offset != 2 {
		t.Fatalf("anchor: %+v", a)
	}
	// Exact: a clock time.
	ex, err := call[LineOut](t, f.a, "voiceover.line", LineIn{RevisionID: mv.Applied.RevisionID, Action: "move", Line: "line-2", To: &to, Exact: true, Apply: true})
	if err != nil || ex.Placement != "at 0:07.0" {
		t.Fatalf("exact: %+v %v", ex, err)
	}
	// A line that follows the previous one stays "after" when dropped where it already starts.
	r := ex.Applied.RevisionID
	sh, _ := call[ShowOut](t, f.a, "voiceover.show", RevisionIn{RevisionID: r})
	add, err := call[LineOut](t, f.a, "voiceover.line", LineIn{RevisionID: r, Action: "add", Text: strPtr("A third line."), Apply: true})
	if err != nil || add.Applied == nil || add.Placement != "0.3 s after the previous line" {
		t.Fatalf("add after the previous line: %+v %v", add, err)
	}
	r = add.Applied.RevisionID
	sh, _ = call[ShowOut](t, f.a, "voiceover.show", RevisionIn{RevisionID: r})
	at := sh.Beats[2].Start
	keep, err := call[LineOut](t, f.a, "voiceover.line", LineIn{RevisionID: r, Action: "move", Line: "3", To: &at})
	if err != nil || keep.Placement != "0.3 s after the previous line" {
		t.Fatalf("still after the previous line: %+v %v", keep, err)
	}
	// Its own voice, with its name; a new voice never keeps the old one's name.
	ch, err := call[LineOut](t, f.a, "voiceover.line", LineIn{RevisionID: r, Action: "change", Line: "1", Voice: strPtr("voice_bram"), VoiceName: "Bram", Delivery: strPtr("warm"), Apply: true})
	if err != nil || ch.Applied == nil || ch.Applied.Problem != "" {
		t.Fatalf("change: %+v %v", ch, err)
	}
	b := f.spec(ch.Applied.RevisionID).Voiceover.Beats[0]
	if b.Voice == nil || b.Voice.VoiceID != "voice_bram" || b.Voice.Name != "Bram" || b.Delivery != "warm" {
		t.Fatalf("voice: %+v", b)
	}
	ch, err = call[LineOut](t, f.a, "voiceover.line", LineIn{RevisionID: ch.Applied.RevisionID, Action: "change", Line: "1", Voice: strPtr("voice_cleo"), Apply: true})
	if err != nil || ch.Applied == nil {
		t.Fatalf("change 2: %+v %v", ch, err)
	}
	if v := f.spec(ch.Applied.RevisionID).Voiceover.Beats[0].Voice; v == nil || v.VoiceID != "voice_cleo" || v.Name != "" {
		t.Fatalf("the new voice must not keep Bram's name: %+v", v)
	}
	rm, err := call[LineOut](t, f.a, "voiceover.line", LineIn{RevisionID: ch.Applied.RevisionID, Action: "remove", Line: "3", Apply: true})
	if err != nil || len(f.spec(rm.Applied.RevisionID).Voiceover.Beats) != 2 {
		t.Fatalf("remove: %+v %v", rm, err)
	}
	if _, err := call[LineOut](t, f.a, "voiceover.line", LineIn{RevisionID: rev, Action: "shuffle", Line: "1"}); !apperr.Is(err, apperr.InvalidInput) {
		t.Fatalf("unknown action: %v", err)
	}
}

func strPtr(s string) *string { return &s }

// Narration written for one clip goes where that clip plays and replaces only
// the lines over it.
func TestParseForOneClip(t *testing.T) {
	f := setup(t)
	rev := f.cut("Over the opening.", "Still the opening.")
	// Move line 2 over "closing" so the closing has a line to replace.
	to := 6.0
	mv, err := call[LineOut](t, f.a, "voiceover.line", LineIn{RevisionID: rev, Action: "move", Line: "2", To: &to, Apply: true})
	if err != nil || mv.Applied == nil {
		t.Fatalf("move: %+v %v", mv, err)
	}
	out, err := call[ParseOut](t, f.a, "voiceover.parse", ParseIn{RevisionID: mv.Applied.RevisionID, Clip: "closing", Text: "(calm) The light goes.\n\nAnd so do we."})
	if err != nil || out.Result == nil || !out.Result.Check.Valid {
		t.Fatalf("parse for a clip: %+v %v", out, err)
	}
	if !strings.Contains(out.Summary, "for “closing”") || !strings.Contains(out.Summary, "in place of the 1 line") {
		t.Fatalf("summary: %q", out.Summary)
	}
	rev2 := f.applyResult(out.Result)
	bs := f.spec(rev2).Voiceover.Beats
	if len(bs) != 3 || bs[0].Text != "Over the opening." || bs[1].Text != "The light goes." || bs[2].Text != "And so do we." {
		t.Fatalf("script: %+v", bs)
	}
	if bs[1].Anchor.Kind != editspec.AnchorClip || bs[1].Anchor.ClipID != "close" || bs[1].Anchor.Offset != 0.3 || bs[1].Delivery != "calm" || bs[2].Anchor.Kind != editspec.AnchorAfter {
		t.Fatalf("anchors: %+v %+v", bs[1], bs[2])
	}
	if bs[1].ID == bs[0].ID || bs[2].ID == bs[0].ID {
		t.Fatalf("ids: %+v", bs)
	}
	if _, err := call[ParseOut](t, f.a, "voiceover.parse", ParseIn{RevisionID: rev2, Clip: "nope", Text: "x"}); !apperr.Is(err, apperr.NotFound) {
		t.Fatalf("unknown clip: %v", err)
	}
}

// A recording the dashboard uploads is marked as narration when it becomes a take.
func TestAddTakeMarksTheRecording(t *testing.T) {
	f := setup(t)
	rev := f.cut("Hello there.")
	wav := filepath.Join(f.dir, "take.wav")
	ffmpeg(t, "-f", "lavfi", "-i", "sine=frequency=300:sample_rate=48000", "-t", "2", "-ac", "1", wav)
	r := ingest.File(context.Background(), f.a, wav, ingest.Options{ProjectID: f.project, Via: "dashboard"})
	if r.AssetID == "" {
		t.Fatalf("ingest: %+v", r)
	}
	tv, err := call[TakeView](t, f.a, "voiceover.addTake", AddTakeIn{ProjectID: f.project, BeatID: "line-1", Text: "Hello there.", AssetID: r.AssetID, In: 0.2, Out: 1.8, Source: models.TakeRecorded})
	if err != nil || tv.Duration != 1.6 {
		t.Fatalf("take: %+v %v", tv, err)
	}
	var as models.Asset
	_ = f.a.DB.GetByID(context.Background(), store.CAssets, f.a.WorkspaceID(), r.AssetID, &as)
	if !contains(as.Tags, "voiceover") || !contains(as.Tags, "recorded") {
		t.Fatalf("tags: %v", as.Tags)
	}
	ch, err := call[PlanResult](t, f.a, "voiceover.choose", ChooseIn{RevisionID: rev, Beat: "1", TakeID: tv.ID})
	if err != nil || !ch.Check.Valid {
		t.Fatalf("choose: %+v %v", ch, err)
	}
	// Mono recordings are flagged in the preview plan (the renderer plays them 3 dB lower per channel).
	rev2 := f.applyResult(ch)
	if p := f.plan(rev2); p.SourceChannels[r.AssetID] != 1 {
		t.Fatalf("narration channels: %+v", p.SourceChannels)
	}
}

// AI voice estimates name the voices, also when one is given by ID.
func TestEstimateNamesTheVoices(t *testing.T) {
	f := setup(t)
	key := "sk" + "_" + strings.Repeat("aa77", 10) + "d00d"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), key)
	rev := f.cut("Out here, the light arrives slowly.", "And then it is everywhere.")
	est, err := call[EstimateOut](t, f.a, "voiceover.estimate", TTSIn{RevisionID: rev, Voice: "voice_bram"})
	if err != nil || est.Voice.VoiceName != "Bram" || strings.Join(est.VoiceNames, ",") != "Bram" || est.Lines[0].VoiceName != "Bram" {
		t.Fatalf("named by ID: %+v %v", est, err)
	}
	if _, err := call[providers.SettingsOut](t, f.a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, Voice: "Ada"}); err != nil {
		t.Fatal(err)
	}
	// Line 2 has its own voice, saved without a name.
	ch, err := call[LineOut](t, f.a, "voiceover.line", LineIn{RevisionID: rev, Action: "change", Line: "2", Voice: strPtr("voice_cleo"), Apply: true})
	if err != nil || ch.Applied == nil {
		t.Fatalf("own voice: %+v %v", ch, err)
	}
	est, err = call[EstimateOut](t, f.a, "voiceover.estimate", TTSIn{RevisionID: ch.Applied.RevisionID})
	if err != nil || strings.Join(est.VoiceNames, ",") != "Ada,Cleo" {
		t.Fatalf("two narrators: %+v %v", est.VoiceNames, err)
	}
}
