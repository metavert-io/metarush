package ops

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
)

func TestSheetCaptionsSayWhoAndWhat(t *testing.T) {
	segs := []models.TranscriptSegment{
		{From: 40, To: 44, Speaker: "speaker_00", SpeakerName: "Maya", Text: "and then the bunny hopped over the fence",
			Words: []models.Word{{Text: "and", From: 40, To: 40.3}, {Text: "then", From: 40.3, To: 40.6}, {Text: "the", From: 42.0, To: 42.2},
				{Text: "bunny", From: 42.2, To: 42.6}, {Text: "hopped", From: 42.6, To: 43}, {Text: "over", From: 43, To: 43.2}, {Text: "the", From: 43.2, To: 43.4},
				{Text: "fence", From: 43.4, To: 44}}},
		{From: 50, To: 52, Speaker: "speaker_01", Text: "Wow."},
	}
	who, words := speechAt(segs, true, 42.1)
	if who != "Maya" || words != "the bunny hopped over the…" {
		t.Fatalf("at 42.1: %q %q", who, words)
	}
	who, words = speechAt(segs, true, 50.5)
	if who != "Speaker 2" || words != "Wow." {
		t.Fatalf("an unnamed voice among several is Speaker N: %q %q", who, words)
	}
	if who, _ := speechAt(segs, false, 50.5); who != "" {
		t.Fatalf("a single unnamed voice is not named: %q", who)
	}
	if who, words := speechAt(segs, true, 47); who != "" || words != "" {
		t.Fatalf("nobody speaks at 47: %q %q", who, words)
	}
	if got := sheetTimecode(42.04); got != "0:42.0" {
		t.Fatalf("timecode: %q", got)
	}
	if got := sheetTimecode(65.96); got != "1:06.0" {
		t.Fatalf("timecode: %q", got)
	}
}

func TestCleanScenes(t *testing.T) {
	got, err := cleanScenes([]SceneDescription{{From: 6, To: 14, Text: "  A beach,\n\tslowly. "}, {From: 0, To: 6, Text: "A woman talks."}}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].From != 0 || got[1].Text != "A beach, slowly." || got[1].To != 10 {
		t.Fatalf("sorted, tidied, inside the clip: %+v", got)
	}
	for _, bad := range [][]SceneDescription{
		nil,
		{{From: 0, To: 1, Text: " "}},
		{{From: 3, To: 1, Text: "x"}},
		{{From: 20, To: 21, Text: "after the end"}},
		{{From: 0, To: 1, Text: strings.Repeat("x", 601)}},
	} {
		if _, err := cleanScenes(bad, 10); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}
