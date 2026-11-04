package matting

import (
	"math"
	"testing"
)

// frameOf is a w×h RGB frame of one brightness.
func frameOf(w, h int, v byte) []byte {
	f := make([]byte, 3*w*h)
	for i := range f {
		f[i] = v
	}
	return f
}

// Where the picture holds still, an edge that shimmers in the model's mattes
// holds steady; where it moves, the matte follows at once; a cut starts
// afresh; the flicker it measured says so.
func TestSmootherHoldsStillEdgesAndFollowsMovement(t *testing.T) {
	const w, h = 8, 1
	s := newSmoother(w, h)
	still := frameOf(w, h, 100)
	var outs [][]byte
	for k := 0; k < 12; k++ {
		raw := make([]byte, w*h)
		v := byte(100)
		if k%2 == 1 {
			v = 160 // the edge shimmers frame to frame
		}
		for i := range raw {
			raw[i] = v
		}
		outs = append(outs, s.next(raw, still, 3, "rgb24"))
	}
	jump := math.Abs(float64(outs[11][0]) - float64(outs[10][0]))
	if jump >= 40 {
		t.Fatalf("a still edge kept shimmering: %d → %d", outs[10][0], outs[11][0])
	}
	if s.flicker(true) >= s.flicker(false) || s.flicker(false) < 50 {
		t.Fatalf("flicker before %.2f, after %.2f", s.flicker(false), s.flicker(true))
	}
	// The picture moves: the matte follows the model at once.
	moved := frameOf(w, h, 100)
	moved[0], moved[1], moved[2] = 220, 220, 220
	raw := make([]byte, w*h)
	raw[0] = 255
	got := s.next(raw, moved, 3, "rgb24")
	if got[0] < 240 {
		t.Fatalf("a moving pixel lagged: %d", got[0])
	}
	// A cut: everything changes, nothing carried over.
	cut := frameOf(w, h, 250)
	raw = make([]byte, w*h)
	for i := range raw {
		raw[i] = 7
	}
	got = s.next(raw, cut, 3, "rgb24")
	for i := range got {
		if got[i] != 7 {
			t.Fatalf("a cut carried the matte over: %v", got)
		}
	}
	// Blue first (the helper's frames) reads the same brightness.
	l1, l2 := make([]float32, 1), make([]float32, 1)
	luma([]byte{10, 20, 30, 0}, 4, "bgra", l1)
	luma([]byte{30, 20, 10}, 3, "rgb24", l2)
	if l1[0] != l2[0] {
		t.Fatalf("brightness %v vs %v", l1, l2)
	}
	if newSmoother(2, 2).flicker(true) != 0 {
		t.Fatal("flicker with nothing seen")
	}
}
