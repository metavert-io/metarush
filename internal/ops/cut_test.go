package ops

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// A narration line that just fits says, in the cut sheet, what it has no pause
// before, the way the fit report says it: the next line, a later line that now
// plays next (rearranged shots), or, for the last line, the end of the video.
func TestLineTimesSayWhatATightLineRunsUpTo(t *testing.T) {
	at := func(v float64) editspec.Anchor { return editspec.Anchor{Kind: editspec.AnchorTime, At: &v} }
	take := func(sec float64) *editspec.TakeRef {
		return &editspec.TakeRef{AssetID: "ast_01jabcdefghjkmnpqrstvwxyz1", In: 0, Out: sec}
	}
	spec := func(beats ...editspec.Beat) *editspec.Spec {
		s := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetLandscape},
			Clips:     []editspec.Clip{{ID: "a", AssetID: "ast_01jabcdefghjkmnpqrstvwxyz0", In: 0, Out: 6}},
			Voiceover: &editspec.Voiceover{Beats: beats}}
		editspec.Normalize(s)
		return s
	}
	check := func(s *editspec.Spec, want map[string]string) {
		t.Helper()
		got := lineTimes(s, nil)
		short := map[string]string{}
		for _, f := range editspec.FitReport(editspec.Clone(s), nil) {
			short[f.BeatID] = f.Short
		}
		for id, w := range want {
			if !strings.Contains(got[id], "just fits") || !strings.HasSuffix(got[id], ", "+w) {
				t.Errorf("%s: %q, want it to end with %q", id, got[id], w)
			}
			if !strings.HasSuffix(short[id], "— "+w) {
				t.Errorf("%s: the fit report says %q; the sheet should say the same (%q)", id, short[id], w)
			}
		}
	}
	// In script order: the first line runs up to the next, the last to the end.
	check(spec(
		editspec.Beat{ID: "one", Text: "First.", Anchor: at(0), Take: take(2.9)},
		editspec.Beat{ID: "two", Text: "Last.", Anchor: at(3), Take: take(2.9)},
	), map[string]string{"one": "no pause before the next line", "two": "it ends right as the video does"})
	// Rearranged: line 3 now plays second, so line 1 runs up to it.
	check(spec(
		editspec.Beat{ID: "one", Text: "First.", Anchor: at(0), Take: take(1.9)},
		editspec.Beat{ID: "two", Text: "Last.", Anchor: at(4), Take: take(1.9)},
		editspec.Beat{ID: "three", Text: "Middle.", Anchor: at(2), Take: take(1.9)},
	), map[string]string{"one": "no pause before line 3", "two": "it ends right as the video does", "three": "no pause before line 2"})
}
