package ops

import (
	"reflect"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// A conflict over a narration line (the timeline's narration lane moves lines
// too) names the line the way people count them, not as a clip.
func TestConflictItemsNameNarrationLines(t *testing.T) {
	s := &editspec.Spec{
		Clips:     []editspec.Clip{{ID: "kitchen", Label: "Kitchen"}, {ID: "garden"}},
		Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{{ID: "line-1"}, {ID: "line-2"}}},
	}
	got := plainItems([]string{"line-2", "kitchen", "garden", "beats-order"}, s)
	want := []string{"narration line 2", "the “Kitchen” clip", "the garden clip", "the order of the narration"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
