package calls

import (
	"testing"

	"github.com/metavert-io/metarush/internal/visual"
)

// The on-screen index's shots and text, and the evidence's cuts, land on the
// switches found to the frame (D160); the index itself is left as it was.
func TestRefinePutsTheIndexOnTheSwitches(t *testing.T) {
	idx := &visual.Index{Duration: 18,
		Shots: []visual.ShotSpan{{Type: "close-up", From: 0, To: 5.917}, {Type: "close-up", From: 5.917, To: 11.917}, {Type: "close-up", From: 11.917, To: 18}},
		Text:  []visual.TextSpan{{Text: "LENA O", From: 0.5, To: 5.917}, {Text: "MAYA L", From: 6.5, To: 11.5}, {Text: "EXIT", From: 14, To: 15}},
	}
	st := &Stored{Switches: []Switch{{At: 6, Last: 5.967}, {At: 12, Last: 11.967}, {At: 16.2, Last: 16.167}}}
	got, cuts := Refine(idx, []float64{5.917, 11.917}, st)
	if got.Shots[0].To != 6 || got.Shots[1].From != 6 || got.Shots[1].To != 12 || got.Shots[2].From != 12 || got.Shots[2].To != 18 {
		t.Fatalf("shots: %+v", got.Shots)
	}
	if got.Text[0].To != 6 || got.Text[1].From != 6 || got.Text[1].To != 12 || got.Text[2].From != 14 {
		t.Fatalf("text: %+v", got.Text)
	}
	if len(cuts) != 3 || cuts[0] != 6 || cuts[1] != 12 || cuts[2] != 16.2 {
		t.Fatalf("cuts: %v", cuts)
	}
	if idx.Shots[0].To != 5.917 || idx.Text[0].To != 5.917 {
		t.Fatal("the stored index must not change")
	}
	if same, c := Refine(idx, []float64{5.917}, nil); same != idx || len(c) != 1 {
		t.Fatal("no switches: nothing changes")
	}
}
