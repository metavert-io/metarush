package activity

import (
	"testing"

	"github.com/metavert-io/metarush/internal/models"
)

// A generated shot's length reads as English in the activity bar: "An
// 8-second shot", not "A 8-second shot" (8 is in the range people choose).
func TestGenerationWhatSaysAnBeforeAVowelSound(t *testing.T) {
	cases := map[int]string{1: "A 1-second shot", 6: "A 6-second shot", 8: "An 8-second shot", 10: "A 10-second shot", 11: "An 11-second shot",
		18: "An 18-second shot", 28: "A 28-second shot", 80: "An 80-second shot", 86: "An 86-second shot", 100: "A 100-second shot", 800: "An 800-second shot",
		1000: "A 1000-second shot", 8000: "An 8000-second shot", 11000: "An 11000-second shot"}
	for secs, want := range cases {
		g := models.Generation{Operation: "shot", Params: models.GenParams{Seconds: secs}, Take: 1}
		if got := genWhat(g); got != want {
			t.Errorf("%d seconds: %q, want %q", secs, got, want)
		}
	}
	again := models.Generation{Operation: "again", Params: models.GenParams{Seconds: 8}, Take: 2}
	if got := genWhat(again); got != "An 8-second shot (take 2)" {
		t.Errorf("a second take: %q", got)
	}
}
