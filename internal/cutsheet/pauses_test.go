package cutsheet

import (
	"math"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

const astPauses = "ast_01jpausesabcdefghjkmnpqrs"

// "Hello … there, friend … again." with a 2 s and a 1.5 s pause, a short 0.6 s
// one, and a last sentence whose times are only estimated (no words).
func pausesTranscript() []editspec.TranscriptSegment {
	return []editspec.TranscriptSegment{
		{From: 1, To: 6.8, Speaker: "Ada", Words: tw("Hello", 1.0, 1.4, "there,", 3.4, 3.8, "friend", 4.4, 4.9, "again.", 6.4, 6.8)},
		{From: 8, To: 9, Speaker: "Ada", Text: "Something said quickly"},
	}
}

func pausesSpec(speed float64) *editspec.Spec {
	s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetVertical, FPS: 30}, Clips: []editspec.Clip{
		{ID: "talk", AssetID: astPauses, In: 0.5, Out: 9.5, Speed: speed},
	}}
	editspec.Normalize(s)
	return s
}

func pausesTx(id string) []editspec.TranscriptSegment {
	if id == astPauses {
		return pausesTranscript()
	}
	return nil
}

func TestPausesAreShortenedInTheMiddleOfTheQuiet(t *testing.T) {
	got := Pauses(pausesSpec(0), pausesTx, 1, 0.5)
	if len(got) != 2 {
		t.Fatalf("want the 2 s and the 1.5 s pauses, got %+v", got)
	}
	a, b := got[0], got[1]
	// A quarter of a second stays on each side of the cut.
	if a.Kind != RemovePause || a.From != 1.65 || a.To != 3.15 || a.Seconds != 1.5 || a.Words != "" || a.WordsFrom != 1.4 || a.WordsTo != 3.4 {
		t.Fatalf("first pause: %+v", a)
	}
	if a.Reason != "a pause of 2 s" || a.Before != "Hello" || !strings.HasPrefix(a.After, "there, friend") || a.Speaker != "Ada" {
		t.Fatalf("first pause's words: %+v", a)
	}
	if b.From != 5.15 || b.To != 6.15 || b.Seconds != 1 || math.Abs(b.At-4.65) > 0.001 {
		t.Fatalf("second pause: %+v", b)
	}
	ops, err := RemovalOps(pausesSpec(0), got, false)
	if err != nil || len(ops) != 2 {
		t.Fatalf("ops %v %v", ops, err)
	}
	for _, op := range ops {
		if op.Op != "removeWords" || op.Text == nil || *op.Text != "" {
			t.Fatalf("a pause is a stretch with no words: %+v", op)
		}
	}
	if s := editspec.OpsSummary(ops); s != "Shortened 2 pauses" {
		t.Fatalf("summary %q", s)
	}
	before := specSeconds(pausesSpec(0))
	out, _, err := editspec.ApplyOps(pausesSpec(0), ops, false)
	if err != nil {
		t.Fatal(err)
	}
	if d := before - specSeconds(out); math.Abs(d-2.5) > 0.04 {
		t.Fatalf("the cut should be 2.5 s shorter, is %.3f", d)
	}
	// Every word is still heard.
	var heard []string
	for _, c := range out.Clips {
		for _, w := range wordsIn(flattenWords(pausesTranscript()), c.In, c.Out) {
			heard = append(heard, w.text)
		}
	}
	if got := strings.Join(heard, " "); !strings.HasPrefix(got, "Hello there, friend again.") {
		t.Fatalf("heard %q", got)
	}
}

func TestPausesFollowTheClipsSpeedAndOverlappingVoices(t *testing.T) {
	// At double speed a 2 s pause plays for 1 s: over 0.6 s, kept to 0.3 s as it plays.
	got := Pauses(pausesSpec(2), pausesTx, 0.6, 0.3)
	if len(got) == 0 || got[0].From != 1.7 || got[0].To != 3.1 || got[0].Seconds != 0.7 {
		t.Fatalf("at double speed: %+v", got)
	}
	// Someone else's "yeah" until 2.0 s: the quiet starts when the last voice ends.
	tx := func(id string) []editspec.TranscriptSegment {
		segs := pausesTranscript()
		segs = append(segs, editspec.TranscriptSegment{From: 1.2, To: 2.0, Speaker: "Bo", Words: tw("yeah", 1.2, 2.0)})
		return segs
	}
	got = Pauses(pausesSpec(0), tx, 1, 0.5)
	if len(got) == 0 || got[0].WordsFrom != 2 || got[0].From != 2.25 || got[0].To != 3.15 {
		t.Fatalf("overlapping voices: %+v", got)
	}
	// Nothing over 3 s; a length to keep that isn't shorter falls back to half.
	if got := Pauses(pausesSpec(0), pausesTx, 3, 0.5); len(got) != 0 {
		t.Fatalf("nothing is that long: %+v", got)
	}
}

func specSeconds(s *editspec.Spec) float64 {
	_, total := editspec.Timeline(s)
	return float64(total) / float64(s.Output.FPS)
}
