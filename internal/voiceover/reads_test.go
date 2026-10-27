package voiceover

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// liveRead records lines with macOS say (silence before, pauses between),
// ingests and transcribes the file, and returns the asset and the moments each
// line's sound really starts (from the samples).
func (f *fx) liveRead(name string, lead float64, lines ...string) (string, []float64, []float64) {
	t := f.t
	t.Helper()
	if runtime.GOOS != "darwin" || !f.a.Tools.Has(tools.Say) {
		t.Skip("uses macOS say to make the take")
	}
	if transcribe.Select(f.a.Cfg.Transcription, f.a.Tools) == nil {
		t.Skip("no local transcriber installed")
	}
	txt := filepath.Join(f.dir, name+".txt")
	_ = os.WriteFile(txt, []byte(strings.Join(lines, " [[slnc 1300]] ")), 0o600)
	speech := filepath.Join(f.dir, name+"-speech.wav")
	if out, err := exec.Command(f.a.Tools.Path(tools.Say), "-v", "Samantha", "-r", "165", "-f", txt, "-o", speech, "--data-format=LEI16@48000").CombinedOutput(); err != nil {
		t.Skipf("say: %v %s", err, out)
	}
	wav := filepath.Join(f.dir, name+".wav")
	ffmpeg(t, "-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono", "-i", speech, "-filter_complex",
		"[0]atrim=0:"+ftoa(lead)+"[s];[1]aformat=sample_rates=48000:channel_layouts=mono[r];[s][r]concat=n=2:v=0:a=1,apad=pad_dur=1.5", "-ac", "1", "-ar", "48000", "-c:a", "pcm_s16le", wav)
	onsets, quiet := soundOnsets(t, wav)
	ctx := context.Background()
	res := ingest.File(ctx, f.a, wav, ingest.Options{ProjectID: f.project, Via: "dashboard", Transcribe: true})
	if res.AssetID == "" {
		t.Fatalf("ingest: %+v", res)
	}
	runJobs(t, f.a, res.JobIDs)
	return res.AssetID, onsets, quiet
}

func ftoa(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// longCut is an 18-second cut (three shots) with lines like cut's.
func (f *fx) longCut(lines ...string) string {
	spec := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
		Clips: []editspec.Clip{{ID: "open", AssetID: f.clip, In: 0, Out: 6, Label: "opening"}, {ID: "close", AssetID: f.clip, In: 6, Out: 12, Label: "closing"},
			{ID: "again", AssetID: f.clip, In: 0, Out: 6, Label: "again"}}}
	if len(lines) > 0 {
		v := &editspec.Voiceover{}
		for i, l := range lines {
			an := editspec.Anchor{Kind: editspec.AnchorAfter}
			if i == 0 {
				an = editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "open", Offset: 0.3}
			}
			v.Beats = append(v.Beats, editspec.Beat{ID: "line-" + string(rune('1'+i)), Text: l, Anchor: an})
		}
		spec.Voiceover = v
	}
	return f.apply(editspec.Plan{ProjectID: f.project, Instruction: "test cut", Spec: spec})
}

// soundOnsets reads a 16-bit mono WAV and returns where each stretch of sound
// starts after at least half a second of silence, and a function's worth of
// data: the RMS of every 10 ms (for checking cuts fall in silence).
func soundOnsets(t *testing.T, wav string) ([]float64, []float64) {
	t.Helper()
	b, err := os.ReadFile(wav)
	if err != nil {
		t.Fatal(err)
	}
	i := 12
	for i+8 <= len(b) && string(b[i:i+4]) != "data" {
		i += 8 + int(binary.LittleEndian.Uint32(b[i+4:]))
	}
	pcm := b[i+8:]
	const win = 480 // 10 ms at 48 kHz
	var rms []float64
	for k := 0; k+2*win <= len(pcm); k += 2 * win {
		s := 0.0
		for j := 0; j < win; j++ {
			v := float64(int16(binary.LittleEndian.Uint16(pcm[k+2*j:]))) / 32768
			s += v * v
		}
		rms = append(rms, math.Sqrt(s/win))
	}
	var onsets []float64
	silent := 50
	for k, v := range rms {
		if v > 0.01 {
			if silent >= 50 {
				onsets = append(onsets, float64(k)*0.01)
			}
			silent = 0
		} else {
			silent++
		}
	}
	return onsets, rms
}

// A live take lands where it was spoken: each line starts where it was said
// (to within a frame or two), the take is cut only in the pauses, the read
// plays as one piece, and the lines can go to their planned spots and back.
// A re-recorded line (a punch-in) replaces only that line; both takes stay.
func TestLiveTakeLandsWhereSpoken(t *testing.T) {
	f := setup(t)
	script := []string{"We came for the northern lights.", "And we stayed for the silence.", "Every morning starts in the dark."}
	rev := f.longCut(script...)
	asset, onsets, rms := f.liveRead("take1", 1.5, script...)
	if len(onsets) != 3 {
		t.Fatalf("the test take should have three lines of sound: %v", onsets)
	}
	from := 2.0
	out, err := call[PlaceOut](t, f.a, "voiceover.place", PlaceIn{RevisionID: rev, AssetID: asset, From: &from})
	if err != nil {
		t.Fatal(err)
	}
	if out.Placed != 3 || out.NotHeard != 0 || out.Result == nil || !out.Result.Check.Valid || out.ReadID == "" {
		t.Fatalf("placement: %+v", out)
	}
	rev2 := f.applyResult(out.Result)
	plan := f.plan(rev2)
	sp := f.spec(rev2)
	for i, pb := range plan.Voiceover.Beats {
		spoken := from + onsets[i]
		at := float64(pb.From) / 30
		// The line starts just before it was said: the cut is in the pause, a breath before.
		if d := spoken - at; d < -0.04 || d > 0.15 {
			t.Errorf("line %d starts at %.3f; it was said at %.3f (%.3f s apart)", i+1, at, spoken, d)
		} else {
			t.Logf("line %d starts %.3f s before it is said (the cut sits in the pause)", i+1, d)
		}
		b := sp.Voiceover.Beats[i]
		if b.Take == nil || b.Take.AssetID != asset || b.Take.Source != models.TakeRecorded || b.Anchor.Kind == editspec.AnchorAfter {
			t.Errorf("line %d: %+v", i+1, b)
		}
		// Cut only in silence: around each piece's start the take is quiet.
		k := int(b.Take.In / 0.01)
		if i > 0 && k < len(rms) && rms[k] > 0.01 {
			t.Errorf("line %d's piece starts inside sound (%.3f at %.2f s)", i+1, rms[k], b.Take.In)
		}
		if i > 0 && math.Abs(sp.Voiceover.Beats[i-1].Take.Out-b.Take.In) > 1e-9 {
			t.Errorf("lines %d and %d don't meet: %.3f / %.3f", i, i+1, sp.Voiceover.Beats[i-1].Take.Out, b.Take.In)
		}
	}
	var vo []editspec.PlanAudio
	for _, a := range plan.Audio {
		if a.Role == editspec.RoleVoiceover {
			vo = append(vo, a)
		}
	}
	if len(vo) != 1 || len(vo[0].BeatIDs) != 3 {
		t.Fatalf("the read should play as one piece: %+v", vo)
	}
	// The planned spots are kept: line 1 was planned 0.3 s into the opening shot.
	if out.Lines[0].Planned != "0.3 s after shot 1 (“opening”) starts" || out.Lines[0].Moved == nil {
		t.Fatalf("planned spot: %+v", out.Lines[0])
	}
	show, err := call[ShowOut](t, f.a, "voiceover.show", RevisionIn{RevisionID: rev2})
	if err != nil {
		t.Fatal(err)
	}
	if show.Beats[0].ReadID != out.ReadID || show.Beats[0].Planned == "" || len(show.Reads) != 1 || show.Reads[0].InUse != 3 {
		t.Fatalf("show: %+v / reads %+v", show.Beats[0], show.Reads)
	}

	// Snap lines to their planned spots, then back to where they were said.
	snap, err := call[PlaceOut](t, f.a, "voiceover.place", PlaceIn{RevisionID: rev2, Read: out.ReadID, Where: "planned"})
	if err != nil {
		t.Fatal(err)
	}
	rev3 := f.applyResult(snap.Result)
	if a := f.spec(rev3).Voiceover.Beats[0].Anchor; a.Kind != editspec.AnchorClip || a.ClipID != "open" || a.Offset != 0.3 {
		t.Fatalf("snapped line 1: %+v", a)
	}
	if a := f.spec(rev3).Voiceover.Beats[1].Anchor; a.Kind != editspec.AnchorAfter {
		t.Fatalf("snapped line 2 should follow line 1 again: %+v", a)
	}
	back, err := call[PlaceOut](t, f.a, "voiceover.place", PlaceIn{RevisionID: rev3, Read: out.ReadID})
	if err != nil {
		t.Fatal(err)
	}
	rev4 := f.applyResult(back.Result)
	if got, want := f.spec(rev4).Voiceover.Beats, sp.Voiceover.Beats; !sameAnchor(got[1].Anchor, want[1].Anchor) {
		t.Fatalf("back where it was said: %+v, want %+v", got[1].Anchor, want[1].Anchor)
	}

	// Punch-in: line 2 again, recorded from half a second before line 1 has
	// finished (a lead-in longer than the pause): line 1 keeps its last words,
	// because it stops where the new take's sound begins, not where the
	// recording started.
	asset2, onsets2, _ := f.liveRead("take2", 0.8, script[1])
	lastSound1 := 0.0
	for k, v := range rms {
		if at := float64(k) * 0.01; v > 0.01 && at < onsets[1] {
			lastSound1 = at + 0.01
		}
	}
	from2 := from + lastSound1 - 0.5
	until := float64(plan.Voiceover.Beats[2].From)/30 - 0.05
	punch, err := call[PlaceOut](t, f.a, "voiceover.place", PlaceIn{RevisionID: rev4, AssetID: asset2, From: &from2, Until: &until})
	if err != nil {
		t.Fatal(err)
	}
	if punch.Placed != 1 || punch.Lines[1].State != PlacedLineIn {
		t.Fatalf("punch-in: %+v", punch)
	}
	rev5 := f.applyResult(punch.Result)
	bs := f.spec(rev5).Voiceover.Beats
	if bs[1].Take.AssetID != asset2 || bs[0].Take.AssetID != asset || bs[2].Take.AssetID != asset {
		t.Fatalf("only line 2 should change: %s %s %s", bs[0].Take.AssetID, bs[1].Take.AssetID, bs[2].Take.AssetID)
	}
	if d := (from2 + onsets2[0]) - float64(f.plan(rev5).Voiceover.Beats[1].From)/30; d < -0.04 || d > 0.15 {
		t.Errorf("the re-recorded line 2 starts %.3f s from where it was said", d)
	}
	if !sameAnchor(bs[2].Anchor, sp.Voiceover.Beats[2].Anchor) || bs[2].Take.In != sp.Voiceover.Beats[2].Take.In {
		t.Errorf("line 3 should be untouched: %+v", bs[2])
	}
	if bs[0].Take.Out < lastSound1 {
		t.Errorf("line 1 should keep its last words: it now ends at %.3f s of the take, its sound at %.3f s", bs[0].Take.Out, lastSound1)
	}
	// Both takes of line 2 are kept; the first one goes back where it was said.
	tk, err := call[TakesOut](t, f.a, "voiceover.takes", TakesIn{RevisionID: rev5, Beat: "2"})
	if err != nil || len(tk.Takes) != 2 {
		t.Fatalf("takes of line 2: %+v %v", tk, err)
	}
	var first string
	for _, x := range tk.Takes {
		if x.AssetID == asset {
			first = x.ID
		}
	}
	ab, err := call[PlanResult](t, f.a, "voiceover.choose", ChooseIn{RevisionID: rev5, Beat: "2", TakeID: first})
	if err != nil {
		t.Fatal(err)
	}
	if b := f.spec(f.apply(ab.Plan)).Voiceover.Beats[1]; b.Take.AssetID != asset || !sameAnchor(b.Anchor, sp.Voiceover.Beats[1].Anchor) {
		t.Fatalf("A/B back to take 1: %+v", b)
	}
}

// A line skipped in a live take loses its old sound, follows the line before
// and is flagged; the lines around it land where they were said.
func TestLiveTakeLineNotHeard(t *testing.T) {
	f := setup(t)
	script := []string{"We came for the northern lights.", "And we stayed for the silence.", "Every morning starts in the dark."}
	rev := f.longCut(script...)
	asset, onsets, _ := f.liveRead("skip", 1.0, script[0], script[2])
	from := 1.0
	out, err := call[PlaceOut](t, f.a, "voiceover.place", PlaceIn{RevisionID: rev, AssetID: asset, From: &from})
	if err != nil {
		t.Fatal(err)
	}
	if out.Placed != 2 || out.NotHeard != 1 || len(out.Lines) != 3 || out.Lines[1].State != PlacedNotHeard || out.Lines[1].Message == "" || len(out.Warnings) == 0 {
		t.Fatalf("a skipped line: %+v", out)
	}
	if !strings.Contains(out.Summary, "wasn't heard") {
		t.Errorf("the summary should say a line wasn't heard: %q", out.Summary)
	}
	rev2 := f.applyResult(out.Result)
	bs := f.spec(rev2).Voiceover.Beats
	if bs[1].Take != nil || bs[1].Anchor.Kind != editspec.AnchorAfter || bs[0].Take == nil || bs[2].Take == nil || bs[2].Take.AssetID != asset {
		t.Fatalf("line 2 should have no sound and follow line 1; lines 1 and 3 play the take: %+v", bs)
	}
	plan := f.plan(rev2)
	if d := (from + onsets[1]) - float64(plan.Voiceover.Beats[2].From)/30; d < -0.04 || d > 0.15 {
		t.Errorf("line 3 starts %.3f s from where it was said", d)
	}
}

// A take made with no script writes the script from what was said, each line
// where it was said.
func TestLiveTakeWithoutAScript(t *testing.T) {
	f := setup(t)
	rev := f.longCut()
	asset, onsets, _ := f.liveRead("noscript", 1.0, "The light arrives slowly.", "Then it is everywhere.")
	from := 0.5
	out, err := call[PlaceOut](t, f.a, "voiceover.place", PlaceIn{RevisionID: rev, AssetID: asset, From: &from})
	if err != nil {
		t.Fatal(err)
	}
	rev2 := f.applyResult(out.Result)
	sp := f.spec(rev2)
	if sp.Voiceover == nil || len(sp.Voiceover.Beats) == 0 || sp.Voiceover.Original != "" {
		t.Fatalf("lines from the take: %+v", sp.Voiceover)
	}
	if d := (from + onsets[0]) - float64(f.plan(rev2).Voiceover.Beats[0].From)/30; d < -0.04 || d > 0.15 {
		t.Errorf("line 1 starts %.3f s from where it was said", d)
	}
}

// Script versions (D86): the original is kept exactly; Claude's fitted
// version is used line by line; switching back and forth loses nothing.
func TestScriptVersions(t *testing.T) {
	f := setup(t)
	rev := f.cut()
	text := "We came for the northern lights.\n\nAnd we stayed for the silence,  the long  silence.\n"
	parsed, err := call[ParseOut](t, f.a, "voiceover.parse", ParseIn{RevisionID: rev, Text: text})
	if err != nil {
		t.Fatal(err)
	}
	rev2 := f.applyResult(parsed.Result)
	v := f.spec(rev2).Voiceover
	if v.Original != text || len(v.Beats) != 2 || v.Beats[0].Anchor.Kind != editspec.AnchorAfter || v.Beats[1].Anchor.Kind != editspec.AnchorAfter {
		t.Fatalf("as-is: %q %+v", v.Original, v.Beats)
	}
	p := &FitProposal{Note: "Tied the lines to their shots.", Lines: []FitProposalLine{
		{ID: "1", Text: "We came for the northern lights.", Anchor: &editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "open", Offset: 0.5}, Reason: "Starts as the sky opens."},
		{ID: "line-2", Text: "We stayed for the silence.", At: "0:05.4", Reason: "Trimmed to fit before the end."},
	}}
	fo, err := call[FittedOut](t, f.a, "voiceover.fitted", FittedIn{RevisionID: rev2, Proposal: p})
	if err != nil {
		t.Fatal(err)
	}
	if fo.Changed != 2 || fo.Lines[0].Change != "timing" || fo.Lines[1].Change != "both" || fo.Result == nil || !fo.Result.Check.Valid {
		t.Fatalf("fitted: %+v", fo)
	}
	del, add := 0, 0
	for _, w := range fo.Lines[1].Words {
		switch w.Op {
		case "del":
			del++
		case "add":
			add++
		}
	}
	if del != 4 || add != 0 || fo.Lines[1].Words[5].Was != "silence," {
		t.Fatalf("the word diff should show what changed: %+v", fo.Lines[1].Words)
	}
	rev3 := f.applyResult(fo.Result)
	v = f.spec(rev3).Voiceover
	if v.Active != editspec.VersionFitted || v.Beats[1].Text != "We stayed for the silence." || v.Beats[1].Anchor.ClipID != "close" || len(v.Parked) != 2 || v.Original != text {
		t.Fatalf("fitted in use: %+v", v)
	}
	// Keep the person's own words on line 2.
	kp, err := call[FittedOut](t, f.a, "voiceover.fitted", FittedIn{RevisionID: rev3, Keep: []string{"2"}})
	if err != nil {
		t.Fatal(err)
	}
	rev4 := f.applyResult(kp.Result)
	v = f.spec(rev4).Voiceover
	if v.Beats[1].Text != "And we stayed for the silence, the long silence." || !v.Fitted.Lines[1].Kept || v.Beats[0].Anchor.Offset != 0.5 {
		t.Fatalf("kept line 2: %+v", v.Beats)
	}
	// Switch to the original and back: nothing is lost.
	u, err := call[UseOut](t, f.a, "voiceover.use", UseIn{RevisionID: rev4, Version: "original"})
	if err != nil {
		t.Fatal(err)
	}
	rev5 := f.applyResult(u.Result)
	v = f.spec(rev5).Voiceover
	if v.ActiveVersion() != editspec.VersionOriginal || v.Beats[0].Anchor.Kind != editspec.AnchorAfter || len(v.Parked) != 2 {
		t.Fatalf("original in use: %+v", v)
	}
	u, err = call[UseOut](t, f.a, "voiceover.use", UseIn{RevisionID: rev5, Version: "fitted"})
	if err != nil {
		t.Fatal(err)
	}
	v = f.spec(f.applyResult(u.Result)).Voiceover
	if v.Active != editspec.VersionFitted || v.Beats[0].Anchor.Offset != 0.5 || v.Beats[1].Text != "And we stayed for the silence, the long silence." || v.Beats[1].Anchor.Kind != editspec.AnchorAfter || v.Original != text {
		t.Fatalf("fitted again: %+v", v)
	}
	// A proposal naming a line the script doesn't have is refused plainly.
	if _, err := call[FittedOut](t, f.a, "voiceover.fitted", FittedIn{RevisionID: rev2, Proposal: &FitProposal{Lines: []FitProposalLine{{ID: "line-9", Text: "x"}}}}); err == nil || !strings.Contains(err.Error(), "doesn't have") {
		t.Fatalf("unknown line: %v", err)
	}
}
