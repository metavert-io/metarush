package derive

import (
	"math"
	"testing"
)

func reasons(kf []Keyframe) map[string]int {
	out := map[string]int{}
	for _, k := range kf {
		out[k.Reason]++
	}
	return out
}

func TestSmartSheetShowsEveryShotEvenShortOnes(t *testing.T) {
	// A 60 s clip with a 0.8 s shot that 12 evenly spaced frames would miss.
	in := KeyframeInput{From: 0, To: 60, Cuts: []float64{20, 20.8, 41}, Cap: 20}
	kf := PickKeyframes(in)
	shots := map[int]bool{}
	for _, k := range kf {
		shots[k.Shot] = true
	}
	for s := 1; s <= 4; s++ {
		if !shots[s] {
			t.Fatalf("shot %d has no frame: %+v", s, kf)
		}
	}
	short := false
	for _, k := range kf {
		if k.At > 20 && k.At < 20.8 {
			short = true
		}
	}
	if !short {
		t.Fatalf("the 0.8 s shot needs a frame inside it: %+v", kf)
	}
	for _, k := range EvenKeyframes(0, 60, 12) {
		if k.At > 20 && k.At < 20.8 {
			t.Fatalf("even spacing happened to hit the short shot; the test needs another gap")
		}
	}
}

func TestSmartSheetPriorityAndCap(t *testing.T) {
	in := KeyframeInput{From: 0, To: 120, Cap: 16,
		Cuts:    []float64{30, 60, 90},
		Changes: []KeyframeChange{{Kind: "gradual", At: 40, Frames: []float64{35, 42, 49}, Score: 0.4}, {Kind: "motion", At: 75, Frames: []float64{74, 75, 76}, Score: 0.5}},
		Turns:   []float64{10, 55, 100},
	}
	kf := PickKeyframes(in)
	if len(kf) > 16 {
		t.Fatalf("cap: %d tiles", len(kf))
	}
	r := reasons(kf)
	if r[ReasonShot] != 4 || r[ReasonChange] != 3 || r[ReasonMovement] != 1 || r[ReasonSpeaker] != 3 {
		t.Fatalf("every shot, change, movement and speaker before coverage: %v %+v", r, kf)
	}
	for i := 1; i < len(kf); i++ {
		if kf[i].At <= kf[i-1].At {
			t.Fatalf("not in time order: %+v", kf)
		}
	}
	// Coverage fills: no stretch longer than the coverage gap (allowing one spacing step).
	gap := CoverageGap(120, 16)
	last := 0.0
	for _, k := range kf {
		if k.At-last > gap*1.5 {
			t.Fatalf("a %.1f s stretch without a frame (coverage gap %.1f): %+v", k.At-last, gap, kf)
		}
		last = k.At
	}
}

func TestSmartSheetManyShotsSpreadOut(t *testing.T) {
	// A montage: 100 one-second shots, cap 20: the chosen shots spread over the whole clip.
	var cuts []float64
	for i := 1; i < 100; i++ {
		cuts = append(cuts, float64(i))
	}
	kf := PickKeyframes(KeyframeInput{From: 0, To: 100, Cuts: cuts, Cap: 20})
	if len(kf) != 20 {
		t.Fatalf("want the cap of 20, got %d", len(kf))
	}
	if kf[0].At > 10 || kf[len(kf)-1].At < 90 {
		t.Fatalf("frames should span the clip: first %.1f last %.1f", kf[0].At, kf[len(kf)-1].At)
	}
	maxGap := 0.0
	for i := 1; i < len(kf); i++ {
		maxGap = math.Max(maxGap, kf[i].At-kf[i-1].At)
	}
	if maxGap > 12 {
		t.Fatalf("frames bunch up: largest gap %.1f s", maxGap)
	}
}

func TestSmartSheetRange(t *testing.T) {
	kf := PickKeyframes(KeyframeInput{From: 10, To: 20, Cuts: []float64{5, 15, 25}, Turns: []float64{13.5}, Cap: 6})
	for _, k := range kf {
		if k.At < 10 || k.At >= 20 {
			t.Fatalf("outside the range: %+v", kf)
		}
	}
	if r := reasons(kf); r[ReasonShot] != 2 || r[ReasonSpeaker] != 1 {
		t.Fatalf("two shots in the range and one new speaker: %v", r)
	}
}

func TestWrapCaption(t *testing.T) {
	fc, _ := sheetFaces()
	lines := wrap(fc, "0:42.5 · Maya: “and then the bunny hopped over the fence and ran away into the woods”", 200, 2)
	if len(lines) != 2 {
		t.Fatalf("two lines: %q", lines)
	}
	last := lines[1]
	if r := []rune(last); r[len(r)-1] != '…' {
		t.Fatalf("a long caption ends with an ellipsis: %q", lines)
	}
}
