package fixes

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
)

var gammaRe = regexp.MustCompile(`eq=gamma=([0-9.]+)`)

func gammaOf(t *testing.T, filter string) float64 {
	t.Helper()
	m := gammaRe.FindStringSubmatch(filter)
	if m == nil {
		t.Fatalf("no brightness step in %q", filter)
	}
	g, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// ffmpeg's eq raises each level to 1/gamma: over 1 brightens, under 1
// darkens. A picture still dark after its levels is brightened (at most
// 1.6), one still bright is calmed (at least 0.75).
func TestColourBrightnessGoesTheRightWay(t *testing.T) {
	night := &ColourFacts{Low: [3]float64{0, 0, 0}, High: [3]float64{0.99, 0.99, 0.99}, Mean: [3]float64{0.2, 0.2, 0.22}}
	if g := gammaOf(t, colourFilter(night)); g <= 1 || g > 1.6 {
		t.Fatalf("a dark picture: gamma %.3f", g)
	}
	almost := &ColourFacts{Low: [3]float64{0, 0, 0}, High: [3]float64{1, 1, 1}, Mean: [3]float64{0.35, 0.35, 0.35}}
	if g := gammaOf(t, colourFilter(almost)); g <= 1 || g >= 1.6 {
		t.Fatalf("a slightly dark picture: gamma %.3f", g)
	}
	snow := &ColourFacts{Low: [3]float64{0.1, 0.1, 0.1}, High: [3]float64{1, 1, 1}, Mean: [3]float64{0.92, 0.92, 0.94}}
	if g := gammaOf(t, colourFilter(snow)); g >= 1 || g < 0.75 {
		t.Fatalf("a bright picture: gamma %.3f", g)
	}
}

// fingerprintBeforeColour2 is Fingerprint before the colour fix changed.
func fingerprintBeforeColour2(ticked []string, take int) string {
	fs := copyFixes(ticked)
	b, _ := json.Marshal(map[string]any{"v": 1, "fixes": fs, "take": take})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// Copies the colour fix made before it was corrected are never reused; the
// other fixes' copies still are.
func TestColourFingerprintIsNewSinceTheBrightnessFix(t *testing.T) {
	for _, ticked := range [][]string{{models.FixColour}, {models.FixSteady, models.FixColour}} {
		if Fingerprint(ticked, 0) == fingerprintBeforeColour2(ticked, 0) {
			t.Errorf("%v: the old copy would be reused", ticked)
		}
	}
	for _, ticked := range [][]string{{models.FixSteady}, {models.FixHum, models.FixVoices}, {models.FixBackground, models.FixEdges}} {
		if Fingerprint(ticked, 0) != fingerprintBeforeColour2(ticked, 0) {
			t.Errorf("%v: its copies must still be reused", ticked)
		}
	}
}
