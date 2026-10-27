package sound

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// talkCut is a cut of a recording where Ada and Ben take turns over a hiss,
// with ElevenLabs' fake set up on this computer for cloud voices.
func talkCut(t *testing.T) (*sfx, *fakeeleven.Server, string, string) {
	t.Helper()
	f := newSfx(t)
	key := "sk" + "_" + strings.Repeat("ab12", 10) + "cafe" // made at run time: no literal for the secret scanner
	fake := fakeeleven.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	rec := filepath.Join(f.dir, "talk.mp4")
	f.ffmpeg("-f", "lavfi", "-i", "testsrc2=size=320x180:rate=30",
		"-f", "lavfi", "-i", "aevalsrc='0.3*sin(2*PI*(220+110*gte(t\\,4))*t)*gt(sin(2*PI*2.5*t)\\,-0.3)*between(mod(t\\,4)\\,0.5\\,3.5)':s=48000:c=mono",
		"-f", "lavfi", "-i", "anoisesrc=color=pink:amplitude=0.03:seed=5:sample_rate=48000",
		"-filter_complex", "[1:a][2:a]amix=inputs=2:normalize=0:duration=first[a]", "-map", "0:v", "-map", "[a]", "-t", "8",
		"-c:v", "libx264", "-preset", "veryfast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-ac", "2", rec)
	src := f.ingest(rec)
	f.transcript(src.ID, []turn{{Speaker: "Ada", Start: 0.5, End: 3.5, Text: "One two three."}, {Speaker: "Ben", Start: 4.5, End: 7.5, Text: "Four five six."}})
	rev := f.cut(editspec.Clip{ID: "talk", AssetID: src.ID, In: 0, Out: 8})
	if _, err := scall[providers.Status](t, f.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}); err != nil {
		t.Fatal(err)
	}
	return f, fake, src.ID, rev
}

// A clearer voice, seen everywhere people look at a cut's sound: the Sound
// tab (each person, what plays, the ways that work here), the before/after
// comparison, the quality check's note; switched off and on (A/B) and taken
// away without doing or paying for anything again.
func TestAClearerVoiceIsShownComparedAndSwitched(t *testing.T) {
	f, fake, _, rev := talkCut(t)
	ctx := context.Background()
	// Before: nothing clearer anywhere.
	cmp, err := scall[CompareOut](t, f.a, "sound.compare", CompareIn{RevisionID: rev})
	if err != nil || len(cmp.Scores) != 0 || cmp.Summary != "No voice in this cut has a clearer version yet. Make one on the Sound tab." {
		t.Fatalf("compare before: %+v %v", cmp, err)
	}
	show, err := scall[ShowOut](t, f.a, "sound.show", voiceover.RevisionIn{RevisionID: rev})
	if err != nil || show.Summary != "Everything plays as it was recorded." || len(show.People) != 2 || show.People[0].Name != "Ada" || show.People[1].Name != "Ben" ||
		show.People[0].Seconds != 4 || show.People[1].Seconds != 4 || show.People[0].Level != "As recorded" || show.People[0].Enhance != nil {
		t.Fatalf("show before: %+v %v", show, err)
	}
	var cloud *ModeView
	for i := range show.Modes {
		if show.Modes[i].Mode == "cloud" {
			cloud = &show.Modes[i]
		}
	}
	if cloud == nil || !cloud.Ready || cloud.Problem != "" || !strings.Contains(cloud.Cost, "credits") {
		t.Fatalf("cloud with a key here: %+v", cloud)
	}
	present := map[string]bool{}
	for _, p := range show.Parts {
		present[p.Part] = p.Present
	}
	if !present[editspec.BusSource] || present[editspec.BusMusic] {
		t.Fatalf("parts %+v", show.Parts)
	}

	// Ben, clearer in the cloud.
	out, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev, Target: "Ben", Mode: "cloud", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev2 := applied(t, out.Applied)
	if !strings.HasPrefix(out.Summary, "Ben sounds clearer (in the cloud, 100% strength).") || !strings.Contains(out.Summary, "ElevenLabs credits") ||
		!strings.Contains(out.Summary, "It is in the cut now as version 2.") {
		t.Fatalf("summary %q", out.Summary)
	}
	show, err = scall[ShowOut](t, f.a, "sound.show", voiceover.RevisionIn{RevisionID: rev2})
	if err != nil {
		t.Fatal(err)
	}
	ben := show.People[1]
	if ben.Enhance == nil || ben.Enhance.Mode != "cloud" || ben.Enhance.State != "ready" || ben.Enhance.Off || ben.Enhance.Words != "Ben sounds clearer (in the cloud, 100%)." ||
		show.People[0].Enhance != nil || show.Summary != "Ben sounds clearer." {
		t.Fatalf("show after: %+v %q", ben.Enhance, show.Summary)
	}
	for _, line := range append(show.Explanations, show.Summary, ben.Enhance.Words) {
		if fs := plainwords.Check(line); len(fs) > 0 {
			t.Errorf("not plain: %q %v", line, fs)
		}
	}
	// Compared: Ben's before and after; Ada has none.
	cmp, err = scall[CompareOut](t, f.a, "sound.compare", CompareIn{RevisionID: rev2})
	if err != nil || len(cmp.Scores) != 1 || cmp.Scores[0].Speaker != "Ben" || !strings.HasPrefix(cmp.Summary, "Ben: ") {
		t.Fatalf("compare: %+v %v", cmp, err)
	}
	if one, err := scall[CompareOut](t, f.a, "sound.compare", CompareIn{RevisionID: rev2, Target: "ben"}); err != nil || len(one.Scores) != 1 {
		t.Fatalf("compare Ben: %+v %v", one, err)
	}
	if none, err := scall[CompareOut](t, f.a, "sound.compare", CompareIn{RevisionID: rev2, Target: "ada"}); err != nil || len(none.Scores) != 0 ||
		none.Summary != "Ada has no clearer version in this cut." {
		t.Fatalf("compare Ada: %+v %v", none, err)
	}
	// The quality check says it too.
	notes := clearerVoicesIn(ctx, f.a, f.plan(rev2))
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "Clearer voice for Ben (in the cloud): ") {
		t.Fatalf("QA notes %q", notes)
	}
	if clearerVoicesIn(ctx, f.a, f.plan(rev)) != nil {
		t.Fatal("QA notes for a cut without clearer voices")
	}

	// A/B: the original plays, the clearer one is kept; nothing is made or paid again.
	off := true
	out, err = scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev2, Target: "Ben", Off: &off, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev3 := applied(t, out.Applied)
	if !strings.HasPrefix(out.Summary, "Ben plays the original voice; the clearer version is kept for when you switch back.") || len(fake.Isolations) != 1 {
		t.Fatalf("switched off: %q (isolations %d)", out.Summary, len(fake.Isolations))
	}
	show, _ = scall[ShowOut](t, f.a, "sound.show", voiceover.RevisionIn{RevisionID: rev3})
	if e := show.People[1].Enhance; e == nil || !e.Off || e.Words != "Ben plays as recorded for now (a clearer version is kept: in the cloud, 100%)." ||
		show.Summary != "Everything plays as it was recorded." {
		t.Fatalf("show switched off: %+v %q", show.People[1].Enhance, show.Summary)
	}
	// The preview's A/B on the clearer version: every voice as recorded, nothing saved.
	pv, err := scall[PreviewOut](t, f.a, "sound.preview", LevelsIn{RevisionID: rev2, Original: true})
	if err != nil || len(pv.Changes) != 1 || pv.Changes[0] != "every voice as recorded" || pv.Summary != "With this change: every voice as recorded. Nothing is saved yet." {
		t.Fatalf("preview A/B: %+v %v", pv, err)
	}
	for _, c := range pv.Plan.Clips {
		if c.Audio != nil && c.Audio.Variant != nil {
			t.Fatalf("the preview's original still plays the clearer copy: %+v", c.Audio)
		}
	}
	if pv, err := scall[PreviewOut](t, f.a, "sound.preview", LevelsIn{RevisionID: rev2}); err != nil || pv.Summary != "As saved." {
		t.Fatalf("preview as saved: %+v %v", pv, err)
	}
	// Taken away: as recorded again.
	out, err = scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev3, Target: "Ben", Remove: true, Apply: true})
	if err != nil || !strings.HasPrefix(out.Summary, "Ben plays as recorded again.") {
		t.Fatalf("removed: %+v %v", out, err)
	}
	// What can't be made clearer.
	for _, c := range []struct {
		name string
		in   EnhanceIn
		code string
	}{
		{"an unknown way", EnhanceIn{RevisionID: rev, Target: "Ben", Mode: "magic", Estimate: true}, apperr.InvalidInput},
		{"recorded narration that isn't there", EnhanceIn{RevisionID: rev, Target: "narration", Estimate: true}, apperr.InvalidInput},
	} {
		if _, err := scall[EnhanceOut](t, f.a, "sound.enhance", c.in); !apperr.Is(err, c.code) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// Levels for single people, and the parts of the mix, through the ops: checked
// first (nothing saved), applied as a version, shown on the Sound tab in the
// same words; a preview never saves; the levels op never takes the A/B.
func TestPeoplesLevelsThroughTheOps(t *testing.T) {
	f, _, _, rev := talkCut(t)
	ctx := context.Background()
	chk, err := scall[LevelsOut](t, f.a, "sound.levels", LevelsIn{RevisionID: rev, People: []PersonLevel{{Name: "Ben", GainDB: 3}, {Name: "Ada", GainDB: -2}}})
	if err != nil || chk.Applied != nil || chk.Result == nil || chk.Result.Check == nil || !chk.Result.Check.Valid ||
		chk.Summary != "Make Ben louder (+3 dB), make Ada quieter (-2 dB). Apply the change to put it in the cut." {
		t.Fatalf("checked: %+v %v", chk, err)
	}
	out, err := scall[LevelsOut](t, f.a, "sound.levels", LevelsIn{RevisionID: rev, People: []PersonLevel{{Name: "Ben", GainDB: 3}, {Name: "Ada", GainDB: -2}}, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev2 := applied(t, out.Applied)
	if !strings.HasSuffix(out.Summary, "It is in the cut now as version 2.") {
		t.Fatalf("applied: %q", out.Summary)
	}
	show, err := scall[ShowOut](t, f.a, "sound.show", voiceover.RevisionIn{RevisionID: rev2})
	if err != nil || show.People[0].Level != "A little quieter (2 dB)" || show.People[1].Level != "A little louder (3 dB)" || show.People[1].GainDB != 3 ||
		show.Summary != "Ada a little quieter (2 dB); Ben a little louder (3 dB)." {
		t.Fatalf("show %+v %q %v", show.People, show.Summary, err)
	}
	found := false
	for _, e := range show.Explanations {
		found = found || e == "Ben is a little louder (3 dB) over their speech."
	}
	if !found {
		t.Fatalf("explanations %q", show.Explanations)
	}
	// The preview of a change saves nothing; nor does a card's preview with apply.
	pv, err := scall[PreviewOut](t, f.a, "sound.preview", LevelsIn{RevisionID: rev2, People: []PersonLevel{{Name: "Ben", GainDB: 0}}})
	if err != nil || len(pv.Changes) != 1 || pv.Changes[0] != "put Ben back at their recorded level" || !strings.HasSuffix(pv.Summary, "Nothing is saved yet.") {
		t.Fatalf("preview: %+v %v", pv, err)
	}
	card, err := PreviewLevels(ctx, f.a, LevelsIn{RevisionID: rev2, People: []PersonLevel{{Name: "Ada", GainDB: 0}}, Apply: true})
	if err != nil || card.Applied != nil {
		t.Fatalf("a card's preview applied: %+v %v", card, err)
	}
	est, err := PreviewEnhance(ctx, f.a, EnhanceIn{RevisionID: rev2, Target: "Ada", Mode: "cloud", Apply: true})
	if err != nil || est.Applied != nil || est.Estimate.Credits <= 0 || !est.Estimate.Allowed || est.Estimate.Cost == nil || est.Estimate.Cost.USD <= 0 {
		t.Fatalf("a card's estimate: %+v %v", est, err)
	}
	if clean, err := PreviewEnhance(ctx, f.a, EnhanceIn{RevisionID: rev2, Target: "Ada", Mode: "clean", Apply: true}); err != nil || clean.Estimate.Cost != nil {
		t.Fatalf("clean is free on this computer: no estimate of an outside service: %+v %v", clean.Estimate, err)
	}
	if StudioReady(f.a) || SetupRunning(ctx, f.a) {
		t.Fatal("studio isn't set up here, and nothing is being set up")
	}
	for _, c := range []struct {
		name string
		op   string
		in   LevelsIn
		says string
	}{
		{"the A/B saved", "sound.levels", LevelsIn{RevisionID: rev2, Original: true}, "only for the preview"},
		{"a preview applied", "sound.preview", LevelsIn{RevisionID: rev2, Apply: true}, "A preview saves nothing."},
		{"nothing", "sound.levels", LevelsIn{RevisionID: rev2}, "Nothing to change in the sound."},
	} {
		var err error
		if c.op == "sound.levels" {
			_, err = scall[LevelsOut](t, f.a, c.op, c.in)
		} else {
			_, err = scall[PreviewOut](t, f.a, c.op, c.in)
		}
		if !apperr.Is(err, apperr.InvalidInput) || !strings.Contains(apperr.From(err).Message, c.says) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
}

// A recorded narration take made clearer in the cloud: priced first, the
// take's clearer version played by its line (the take is kept, A/B), shown
// and compared as the narration, noted by the quality check, switched off
// and taken away again.
func TestCloudNarrationTakeIsMadeClearer(t *testing.T) {
	f, fake, _, rev := talkCut(t)
	ctx := context.Background()
	take := filepath.Join(f.dir, "narration.wav")
	f.ffmpeg("-f", "lavfi", "-i", "aevalsrc='0.3*sin(2*PI*180*t)*gt(sin(2*PI*2*t)\\,-0.2)':s=48000:c=mono:d=4",
		"-f", "lavfi", "-i", "anoisesrc=color=pink:amplitude=0.02:seed=9:sample_rate=48000:duration=4",
		"-filter_complex", "[0:a][1:a]amix=inputs=2:normalize=0:duration=first[a]", "-map", "[a]", "-c:a", "pcm_s16le", take)
	tk := f.ingest(take, "voiceover")
	spec := f.spec(rev)
	spec.Voiceover = &editspec.Voiceover{Beats: []editspec.Beat{{ID: "line-1", Text: "Here is the garden in spring.",
		Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "talk", Offset: 0.5},
		Take:   &editspec.TakeRef{AssetID: tk.ID, In: 0, Out: tk.Media.Duration, Source: "recorded"}}}}
	head := f.apply(editspec.Plan{ProjectID: f.project, BaseRevisionID: rev, Instruction: "narrated", Spec: spec})

	est, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: head, Target: "narration", Mode: "cloud", Estimate: true})
	if err != nil || !est.Estimate.Allowed || est.Estimate.Credits != CloudCredits(tk.Media.Duration) || est.Mode != "cloud" || est.Strength != 1 || len(fake.Isolations) != 0 {
		t.Fatalf("estimate %+v %v", est, err)
	}
	// In dollars, stretch by stretch (D208): ElevenLabs' Voice Isolator on the
	// take, its minutes and credits, at the dated price.
	if c := est.Estimate.Cost; c == nil || len(c.Parts) != 1 || c.Parts[0].Service != "ElevenLabs" || c.Parts[0].Model != "Voice Isolator" || c.Parts[0].Free ||
		c.Parts[0].Amount != est.Estimate.Credits || !strings.HasPrefix(c.Parts[0].Source, "“") || !strings.Contains(c.Parts[0].Source, " from 0:00 to 0:0") ||
		!strings.Contains(c.Parts[0].Units, "of sound (") || c.USD != costs.Round(providers.IsolationUSD(est.Estimate.Credits)) || !strings.Contains(est.Summary, "("+c.About+")") {
		t.Fatalf("the cloud's estimate: %+v (summary %q)", est.Estimate.Cost, est.Summary)
	}
	out, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: head, Target: "narration", Mode: "cloud", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev2 := applied(t, out.Applied)
	if len(fake.Isolations) != 1 || !strings.HasPrefix(out.Summary, "The narration sounds clearer (in the cloud, 100% strength).") || len(out.Scores) != 1 {
		t.Fatalf("made %q %+v (isolations %d)", out.Summary, out.Scores, len(fake.Isolations))
	}
	b := f.spec(rev2).Voiceover.Beats[0].Take
	if b.Processed == nil || b.Processed.Preset != "cloud 100%" || !b.UseProcessed || b.AssetID != tk.ID {
		t.Fatalf("the take's clearer version: %+v", b)
	}
	show, err := scall[ShowOut](t, f.a, "sound.show", voiceover.RevisionIn{RevisionID: rev2})
	if err != nil || show.Narration == nil || show.Narration.Recorded != 1 || show.Narration.Enhance == nil || show.Narration.Enhance.Mode != "cloud" ||
		show.Narration.Enhance.Words != "The narration sounds clearer (in the cloud, 100%)." || show.Summary != "The narration sounds clearer." {
		t.Fatalf("show %+v %q %v", show.Narration, show.Summary, err)
	}
	cmp, err := scall[CompareOut](t, f.a, "sound.compare", CompareIn{RevisionID: rev2, Target: "narration"})
	if err != nil || len(cmp.Scores) != 1 || cmp.Scores[0].Speaker != "Narration" || !strings.HasPrefix(cmp.Summary, "Narration: ") {
		t.Fatalf("compare %+v %v", cmp, err)
	}
	if notes := clearerVoicesIn(ctx, f.a, f.plan(rev2)); len(notes) != 1 || !strings.HasPrefix(notes[0], "Clearer voice for the narration (in the cloud): ") {
		t.Fatalf("QA notes %q", notes)
	}
	// A/B: the original recording plays, the clearer one is kept; nothing is sent again.
	off := true
	out, err = scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev2, Target: "narration", Off: &off, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev3 := applied(t, out.Applied)
	if b := f.spec(rev3).Voiceover.Beats[0].Take; b.Processed == nil || b.UseProcessed || len(fake.Isolations) != 1 ||
		!strings.HasPrefix(out.Summary, "The narration plays the original voice; the clearer version is kept for when you switch back.") {
		t.Fatalf("switched off: %+v %q", b, out.Summary)
	}
	// Taken away: the take plays as recorded.
	out, err = scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev3, Target: "narration", Remove: true, Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	if b := f.spec(applied(t, out.Applied)).Voiceover.Beats[0].Take; b.Processed != nil || !strings.HasPrefix(out.Summary, "The narration plays as recorded again.") {
		t.Fatalf("taken away: %+v %q", b, out.Summary)
	}
}

// When a person's settings change after their clearer voice was made, the
// Sound tab says it is out of date and what to do.
func TestAClearerVoiceMadeWithOtherSettingsIsSaidToBeOutOfDate(t *testing.T) {
	f, _, _, rev := talkCut(t)
	out, err := scall[EnhanceOut](t, f.a, "sound.enhance", EnhanceIn{RevisionID: rev, Target: "Ben", Mode: "cloud", Apply: true})
	if err != nil {
		t.Fatal(err)
	}
	rev2 := applied(t, out.Applied)
	mix := f.spec(rev2).Mix
	for i := range mix.Voices {
		if mix.Voices[i].Name == "Ben" && mix.Voices[i].Enhance != nil {
			s := 0.6
			mix.Voices[i].Enhance.Strength = &s
		}
	}
	rev3 := f.apply(editspec.Plan{ProjectID: f.project, BaseRevisionID: rev2, Instruction: "Ben a little less processed", Ops: []editspec.Op{{Op: "setMix", Mix: mix}}})
	show, err := scall[ShowOut](t, f.a, "sound.show", voiceover.RevisionIn{RevisionID: rev3})
	if err != nil {
		t.Fatal(err)
	}
	e := show.People[1].Enhance
	if e == nil || e.State != "outdated" || e.Strength != 0.6 ||
		e.Words != "Ben sounds clearer (in the cloud, 60%), but the settings changed since it was made: make it again to hear them." {
		t.Fatalf("out of date: %+v", e)
	}
}
