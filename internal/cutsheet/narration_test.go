package cutsheet

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// A hand-written narration and mix read the way a person means them.
func TestNarrationSheet(t *testing.T) {
	text := "---\ncut: 1\ntitle: Narrated\nvoiceover:\n  fitting: ask\n  pause: 0.4 s\nmix:\n  camera sound: off\n  music: -6 dB\n  ducking: on\n  loudness: -14 LUFS\n" +
		"footage:\n  interview-a: ast_01jabcdefghjkmnpqrstvwxyz0\n  narration: ast_01jabcdefghjkmnpqrstvwxyz3\n---\n" +
		"## Clips\n1. hook: interview-a 0:01 → 0:05\n2. garage: interview-a 0:10 → 0:14\n" +
		"## Voiceover\n" +
		"- intro: \"When I started, I had no idea.\" · 0.5 s after hook starts · voice scratch\n" +
		"  Delivery: warm, a little slower\n" +
		"  Take: narration 0:00 → 0:02.4 · recorded · cleaned version narration \"voice\" · plays the cleaned version\n" +
		"  Why: Sets up the doubt.\n" +
		"- \"So we built it twice.\"\n" +
		"- end: \"What would you build?\" · when garage ends · locked\n" +
		"- moment: \"Listen.\" · at interview-a 0:12.3 in garage \"right after she laughs\"\n" +
		"- clock: \"Last word.\" · at 0:20 · volume -2 dB · status final\n"
	sheet, probs := Parse(text, ParseOptions{})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	v := sheet.Spec.Voiceover
	if v == nil || len(v.Beats) != 5 {
		t.Fatalf("narration: %+v", v)
	}
	b := v.Beats
	if b[0].Anchor.Kind != "clip" || b[0].Anchor.ClipID != "hook" || b[0].Anchor.Offset != 0.5 || b[0].Voice == nil || b[0].Voice.Provider != "local" ||
		b[0].Delivery != "warm, a little slower" || b[0].Take == nil || b[0].Take.Source != "recorded" || !b[0].Take.UseProcessed || b[0].Take.Processed == nil ||
		b[0].Rationale != "Sets up the doubt." {
		t.Fatalf("line 1: %+v %+v", b[0], b[0].Take)
	}
	if b[1].ID != "line-2" || b[1].Anchor.Kind != "after" {
		t.Fatalf("a line without a name or start point follows the previous one: %+v", b[1])
	}
	if b[2].Anchor.Edge != "end" || !b[2].Locked {
		t.Fatalf("line 3: %+v", b[2])
	}
	if a := b[3].Anchor; a.Kind != "moment" || a.ClipID != "garage" || a.At == nil || *a.At != 12.3 || a.Quote != "right after she laughs" {
		t.Fatalf("line 4: %+v", a)
	}
	if a := b[4].Anchor; a.Kind != "time" || *a.At != 20 || b[4].State != "final" || *b[4].GainDB != -2 {
		t.Fatalf("line 5: %+v", b[4])
	}
	if v.Settings == nil || v.Settings.FitPolicy != "ask" || v.Settings.Gap != 0.4 {
		t.Fatalf("settings: %+v", v.Settings)
	}
	m := sheet.Spec.Mix
	if m == nil || !m.MuteSource || m.Music == nil || *m.Music.GainDB != -6 || m.Ducking == nil || !*m.Ducking.Enabled || *m.Loudness.TargetLUFS != -14 {
		t.Fatalf("mix: %+v", m)
	}
	// Printing writes the same narration back, with exact numbers and names.
	out := Print(sheet.Spec, Options{Info: testInfo()})
	for _, want := range []string{"- intro: \"When I started, I had no idea.\" · 0.5 s after hook starts · voice scratch", "  Delivery: warm, a little slower",
		"- line-2: \"So we built it twice.\" · after the previous line", "· when garage ends · locked", "· at interview-a 0:12.3 in garage \"right after she laughs\"",
		"camera sound: off", "music: -6 dB", "ducking: on", "loudness: -14 LUFS", "fitting: ask", "pause: 0.4 s"} {
		if !strings.Contains(out, want) {
			t.Errorf("printed sheet lacks %q:\n%s", want, out)
		}
	}
	again, probs := Parse(out, ParseOptions{Info: testInfo()})
	if HasErrors(probs) || editspec.Hash(again.Spec) != editspec.Hash(sheet.Spec) {
		t.Fatalf("narration does not round-trip: %s\n%s", problemsText(probs), out)
	}
}

type timedInfo struct{ fakeInfo }

func (timedInfo) LineTimes(s *editspec.Spec) map[string]string {
	out := map[string]string{}
	for _, b := range s.Voiceover.Beats {
		out[b.ID] = "starts at 0:01.5 · fits (2.1 of 3 s)"
	}
	return out
}

// Where each line lands is shown under it and ignored when read back.
func TestNarrationTimingLines(t *testing.T) {
	s := minimalSpec()
	s.Voiceover = &editspec.Voiceover{Beats: []editspec.Beat{{ID: "intro", Text: "Hello.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "a"}}}}
	editspec.Normalize(s)
	text := mustRoundTrip(t, "timing", s, Options{Info: timedInfo{testInfo()}})
	if !strings.Contains(text, "- intro: \"Hello.\" · when a starts\n  > starts at 0:01.5 · fits (2.1 of 3 s)\n") {
		t.Fatalf("timing line missing:\n%s", text)
	}
}

// A finding about a narration line sits on that line and names it the way the
// sheet does, as a sentence.
func TestNarrationFindingsOnTheirLine(t *testing.T) {
	text := "---\ncut: 1\nfootage:\n  interview-a: ast_01jabcdefghjkmnpqrstvwxyz0\n---\n## Clips\n1. hook: interview-a 0:01 → 0:05\n" +
		"## Voiceover\n- intro: \"Hello.\" · when hook starts\n- close: \"That is why I teach now, \\\"really\\\", and why.\" · after the previous line\n"
	sheet, probs := Parse(text, ParseOptions{Info: testInfo()})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	who := editspec.LineName(1, sheet.Spec.Voiceover.Beats[1].Text)
	if who != "Narration line 2 (“That is why I teach now,…”)" {
		t.Fatalf("validation names the line by its number and words: %q", who)
	}
	is := editspec.Issue{Severity: "warning", Code: "beat_fit", Path: "/spec/voiceover/beats/1",
		Message: who + " has 12 words but only 1.5 seconds before the next line — it needs about 4.6 seconds at the narrator's pace."}
	p := Locate(is, sheet.Spec, sheet.Map, testInfo(), nil)
	if p.Line != 10 {
		t.Errorf("finding on line %d, want 10 (the close bullet)", p.Line)
	}
	want := "Narration line close has 12 words but only 1.5 seconds before the next line — it needs about 4.6 seconds at the narrator's pace."
	if p.Message != want {
		t.Errorf("message:\n got %q\nwant %q", p.Message, want)
	}
	// Inside a sentence, and from other words of the line (a message made from
	// an older text): named by its bullet all the same.
	is.Message = "The recording of n" + who[1:] + " runs 3.0 seconds, but there are only 1.5 seconds before the next line, so they overlap by 1.5 seconds."
	if p := Locate(is, sheet.Spec, sheet.Map, testInfo(), nil); p.Message != "The recording of narration line close runs 3.0 seconds, but there are only 1.5 seconds before the next line, so they overlap by 1.5 seconds." {
		t.Errorf("inside a sentence: %q", p.Message)
	}
	is.Message = "Narration line 2 (“That was, “really”, why…”) can't be placed: it was set to start with a clip that is no longer in the video. Choose where it starts again."
	if p := Locate(is, sheet.Spec, sheet.Map, testInfo(), nil); p.Message != "Narration line close can't be placed: it was set to start with a clip that is no longer in the video. Choose where it starts again." {
		t.Errorf("other words: %q", p.Message)
	}
}

// Narration and mix changes read as plain sentences.
func TestNarrationDescribe(t *testing.T) {
	a := minimalSpec()
	a.Voiceover = &editspec.Voiceover{Beats: []editspec.Beat{
		{ID: "intro", Text: "Hello.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "a"}},
		{ID: "two", Text: "Bye.", Anchor: editspec.Anchor{Kind: editspec.AnchorAfter}},
	}}
	editspec.Normalize(a)
	b := editspec.Clone(a)
	b.Voiceover.Beats[0].Text = "Hello there."
	b.Voiceover.Beats[1].Anchor = editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "a", Edge: "end"}
	b.Voiceover.Beats = append(b.Voiceover.Beats, editspec.Beat{ID: "three", Text: "One more.", Anchor: editspec.Anchor{Kind: editspec.AnchorAfter}})
	gain := -6.0
	b.Mix = &editspec.Mix{MuteSource: true, Music: &editspec.Bus{GainDB: &gain}}
	got := strings.Join(Describe(a, b, nil), "\n")
	for _, want := range []string{"Narration line intro now says “Hello there.”.", "Narration line two now starts when a ends.", "New narration line three: “One more.”.",
		"The camera sound is now off.", "Music: now 6 dB quieter (was normal)."} {
		if !strings.Contains(got, want) {
			t.Errorf("changes lack %q:\n%s", want, got)
		}
	}

	// New narration with settings, and a mix that only sets ducking and loudness:
	// each setting in words, no vague catch-all.
	c := minimalSpec()
	editspec.Normalize(c)
	e := editspec.Clone(c)
	duck, lufs, off := -14.0, -14.0, false
	e.Voiceover = &editspec.Voiceover{
		Beats:    []editspec.Beat{{ID: "intro", Text: "Hello.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "a"}}},
		Settings: &editspec.VoiceoverSettings{FitPolicy: editspec.FitAsk, Gap: 0.5},
	}
	e.Mix = &editspec.Mix{Ducking: &editspec.Ducking{MusicUnderVoiceoverDB: &duck}, Loudness: &editspec.MasterLoudness{TargetLUFS: &lufs, Normalize: &off}}
	got = strings.Join(Describe(c, e, nil), "\n")
	for _, want := range []string{"Narration added: 1 line.", "When a narration line runs long, MetaRush now asks you first.",
		"The pause between narration lines is now 0.5 s.", "Music now gets 14 dB quieter while the narration plays (was the usual amount).",
		"The whole video is now made about as loud as YouTube and the social apps expect.", "The whole video is no longer turned up or down to reach a loudness."} {
		if !strings.Contains(got, want) {
			t.Errorf("changes lack %q:\n%s", want, got)
		}
	}
	// Plain words: no vague catch-alls, no sound-engineering jargon (voice.md).
	for _, vague := range []string{"settings changed", "Mix settings were added", "(s)", "LUFS", "dBTP", "duck", "true peak"} {
		if strings.Contains(got, vague) {
			t.Errorf("changes should be specific, not %q:\n%s", vague, got)
		}
	}
	// A mix with nothing to say in words still gets a sentence.
	f := editspec.Clone(c)
	f.Mix = &editspec.Mix{}
	if got := Describe(c, f, nil); !strings.Contains(strings.Join(got, "\n"), "Mix settings were added.") {
		t.Errorf("an empty mix: %v", got)
	}
}
