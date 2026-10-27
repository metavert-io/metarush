package transcribe

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/voice"
)

func TestAssembleWordsBakeoffRules(t *testing.T) {
	// Real sherpa-onnx sample from the bake-off (phone-rotated), ys_log_probs omitted.
	sl := sherpaLine{
		Timestamps: []float64{0.24, 0.48, 0.48, 0.64, 0.80, 0.96, 1.12, 1.28, 1.52, 1.68, 1.92, 2.16, 2.32, 2.56, 2.80, 3.04, 3.20, 3.36, 3.52, 3.84, 4.48},
		Durations:  []float64{0.24, 0.00, 0.16, 0.16, 0.16, 0.16, 0.16, 0.24, 0.16, 0.24, 0.24, 0.16, 0.24, 0.24, 0.24, 0.16, 0.16, 0.16, 0.32, 0.32, 0.32},
		Tokens:     []string{" ", "Q", "u", "ick", " t", "est", " from", " my", " ph", "one", ".", " Is", " this", " thing", " rec", "ord", "ing", " s", "ide", "ways", "?"},
	}
	ws := assembleWords(sl, 10)
	var texts []string
	for _, w := range ws {
		texts = append(texts, w.Text)
	}
	want := []string{"Quick", "test", "from", "my", "phone.", "Is", "this", "thing", "recording", "sideways?"}
	if len(texts) != len(want) {
		t.Fatalf("words %v", texts)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("word %d = %q, want %q (%v)", i, texts[i], want[i], texts)
		}
	}
	// "phone." ends at "one" (1.68+0.24), not at the period; offsets include the chunk start.
	if ws[4].From != 11.52 || ws[4].To != 11.92 {
		t.Fatalf("phone. timing %v–%v", ws[4].From, ws[4].To)
	}
}

func TestChunkPlanCutsAtSilences(t *testing.T) {
	plan := chunkPlan(70, []span{{20, 21}, {28, 29}, {45, 46}, {60, 60.5}})
	if len(plan) != 3 || plan[0].to != 28.5 || plan[1].to != 45.5 || plan[2].to != 70 {
		t.Fatalf("plan %+v", plan)
	}
	hard := chunkPlan(65, nil)
	if len(hard) != 3 || hard[0].to != 30 || hard[1].to != 60 {
		t.Fatalf("hard cuts %+v", hard)
	}
}

// Speakers are assigned the same way whatever recognized the words: a
// whisper.cpp segment that spans a turn change is split at the change, words
// take the speaker they overlap most (nearest edge when none overlaps), and
// segment-timed output takes its largest overlap.
func TestAssignSpeakersIsProviderIndependent(t *testing.T) {
	turns := []voice.Turn{{From: 0, To: 3.1, Label: "speaker_00"}, {From: 3.3, To: 6, Label: "speaker_01"}}
	whisperSeg := Segment{From: 0.2, To: 5.8, Text: "so what did you build? a robot.", Words: []models.Word{
		{Text: "so", From: 0.2, To: 0.4}, {Text: "what", From: 0.5, To: 0.8}, {Text: "did", From: 0.9, To: 1.1}, {Text: "you", From: 1.2, To: 1.4},
		{Text: "build?", From: 1.5, To: 3.15}, // bleeds a little past the turn edge
		{Text: "a", From: 3.18, To: 3.25},     // inside the gap: nearest edge wins
		{Text: "robot.", From: 3.6, To: 5.8},
	}}
	segmentOnly := Segment{From: 3.4, To: 5.9, Text: "a robot."}
	out := AssignSpeakers([]Segment{whisperSeg, segmentOnly}, turns, 6)
	if len(out) != 3 {
		t.Fatalf("want the whisper segment split in two plus the segment-timed one, got %d: %+v", len(out), out)
	}
	if out[0].Speaker != "speaker_00" || out[0].Text != "so what did you build?" || out[0].To != 3.15 {
		t.Fatalf("first part: %+v", out[0])
	}
	if out[1].Speaker != "speaker_01" || !strings.HasPrefix(out[1].Text, "a robot") {
		t.Fatalf("second part: %+v", out[1])
	}
	if out[2].Speaker != "speaker_01" {
		t.Fatalf("segment-timed output takes its largest overlap: %+v", out[2])
	}
	// Without diarization nothing is labeled.
	for _, s := range AssignSpeakers([]Segment{{From: 0, To: 1, Text: "x", Speaker: "old"}}, nil, 1) {
		if s.Speaker != "" {
			t.Fatal("speakers must be cleared when there are no turns")
		}
	}
}

func TestWhisperGatesFillerOverSilence(t *testing.T) {
	segs := []Segment{{From: 0, To: 2, Text: "Thank you."}, {From: 3, To: 5, Text: "Real words here."}, {From: 6, To: 30, Text: "you"}}
	out := gateWhisper(segs, []span{{0, 2.5}}, nil, 8)
	if len(out) != 2 || out[0].Text != "Real words here." || out[1].To != 8 {
		t.Fatalf("gated %+v", out)
	}
}

// Other languages' fillers and subtitle credits over silence go too (D244:
// measured with whisper.cpp large-v3-turbo); the same words over speech stay,
// and a segment with no letters goes wherever it is.
func TestWhisperGatesOtherLanguagesFillers(t *testing.T) {
	quiet := []span{{0, 40}}
	for _, text := range []string{"Gracias por ver el video.", "Gracias.", "Sous-titrage Société Radio-Canada", "Sous-titrage ST' 501",
		"Untertitelung des ZDF, 2020", "Vielen Dank.", "Obrigado.", "ご視聴ありがとうございました", "¡Gracias!"} {
		if out := gateWhisper([]Segment{{From: 1, To: 3, Text: text}}, quiet, nil, 40); len(out) != 0 {
			t.Errorf("%q over silence was kept", text)
		}
	}
	if out := gateWhisper([]Segment{{From: 1, To: 3, Text: "Vielen Dank."}}, nil, nil, 40); len(out) != 1 {
		t.Error("thanks someone says are kept")
	}
	if out := gateWhisper([]Segment{{From: 1, To: 3, Text: "..."}}, nil, nil, 40); len(out) != 0 {
		t.Error("a segment with no letters is dropped")
	}
	if out := gateWhisper([]Segment{{From: 1, To: 3, Text: "Die Untertitel kommen später."}}, nil, nil, 40); len(out) != 1 {
		t.Error("a sentence about subtitles, spoken, is kept")
	}
	// A whole invented sentence over true quiet goes, whatever it says; over
	// a louder room's pauses it stays unless it is a filler.
	invented := []Segment{{From: 0, To: 4, Text: "Vertraue und glaube, es hilft, es heilt die göttliche Kraft!"}}
	if out := gateWhisper(invented, quiet, quiet, 40); len(out) != 0 {
		t.Error("a sentence over true quiet is kept")
	}
	if out := gateWhisper(invented, quiet, nil, 40); len(out) != 1 {
		t.Error("a sentence about subtitles, spoken, is kept")
	}
}
