package visual

import (
	"math"
	"testing"

	"github.com/metavert-io/metarush/internal/vision"
)

func unit(axis int, mix float64) []float32 {
	v := make([]float32, 128)
	v[axis] = float32(math.Sqrt(1 - mix*mix))
	v[(axis+64)%128] = float32(mix)
	return v
}

// Faces keep their best fingerprints, and fingerprints keep two people apart
// in one continuous shot: one walks out on the left, another walks in on the
// right, and they are two faces, not one.
func TestFingerprintsFollowTracks(t *testing.T) {
	var s []Sample
	for i := 0; i < 10; i++ {
		at := float64(i) + 0.5
		box, print := vision.Box{0.2, 0.3, 0.15, 0.3}, unit(1, 0.1)
		if at >= 5 {
			box, print = vision.Box{0.6, 0.3, 0.15, 0.3}, unit(2, 0.1)
		}
		f := vision.Frame{W: 1280, H: 720, Faces: []vision.Face{{Conf: 1, Box: box, Quality: q(0.3 + 0.05*float64(i%5))}}}
		s = append(s, Sample{At: at, Frame: f, Prints: [][]float32{print}})
	}
	idx := Build(s, 1, 10, nil, "proxy")
	if len(idx.Tracks) != 2 {
		t.Fatalf("two people, two faces: %+v", idx.Tracks)
	}
	for _, tr := range idx.Tracks {
		if len(tr.Prints) == 0 || len(tr.Prints) > MaxTrackPrints {
			t.Fatalf("track %d keeps its best fingerprints: %d", tr.ID, len(tr.Prints))
		}
	}
	if idx.Tracks[0].Prints[0].Vec[1] < 0.9 || idx.Tracks[1].Prints[0].Vec[2] < 0.9 {
		t.Fatal("each track keeps its own person's fingerprints")
	}
	// The same person moving to another place (and no fingerprints to say
	// otherwise) is still one face, as before.
	for i := range s {
		s[i].Prints = nil
	}
	if idx := Build(s, 1, 10, nil, "proxy"); len(idx.Tracks) != 1 {
		t.Fatalf("without fingerprints, one face moving: %d tracks", len(idx.Tracks))
	}
	// Ten frames of one person: at most MaxTrackPrints, the best ones.
	var one []Sample
	for i := 0; i < 10; i++ {
		f := vision.Frame{W: 1280, H: 720, Faces: []vision.Face{{Conf: 1, Box: vision.Box{0.4, 0.3, 0.2, 0.4}, Quality: q(float64(i) / 10)}}}
		one = append(one, Sample{At: float64(i) + 0.5, Frame: f, Prints: [][]float32{unit(3, 0.1)}})
	}
	tr := Build(one, 1, 10, nil, "proxy").Tracks[0]
	worst := 1.0
	for _, p := range tr.Prints {
		worst = math.Min(worst, p.Quality)
	}
	if len(tr.Prints) != MaxTrackPrints || worst < 0.4 {
		t.Fatalf("the best frames are kept: %d, worst quality %.2f", len(tr.Prints), worst)
	}
}

// The index keeps the footage's cuts and where the eye is drawn; the subject
// is the main face of each shot, else what stands out.
func TestSubjectFollowsTheMainFaceThenWhatStandsOut(t *testing.T) {
	var s []Sample
	for i := 0; i < 12; i++ {
		at := float64(i) + 0.5
		f := vision.Frame{W: 1280, H: 720, Attention: &vision.Attention{At: [2]float64{0.7, 0.5}, Peak: 0.8, Objects: []vision.Detection{{Conf: 0.6, Box: vision.Box{0.75, 0.4, 0.1, 0.2}}}}}
		if at < 6 {
			x := 0.1 + 0.1*float64(i)
			// The main face walks right; a smaller face stays put.
			f.Faces = []vision.Face{{Conf: 1, Box: vision.Box{x, 0.2, 0.2, 0.35}}, {Conf: 1, Box: vision.Box{0.85, 0.5, 0.06, 0.1}}}
		}
		s = append(s, Sample{At: at, Frame: f})
	}
	idx := Build(s, 1, 12, []float64{6}, "proxy")
	if len(idx.Cuts) != 1 || idx.Cuts[0] != 6 || len(idx.Attention) != 12 {
		t.Fatalf("cuts %v, attention %d", idx.Cuts, len(idx.Attention))
	}
	sub, cuts, ok := Subject(idx, 0, 12)
	if !ok || len(cuts) != 1 || len(sub) != 12 {
		t.Fatalf("subject: %v %v %d", ok, cuts, len(sub))
	}
	for _, p := range sub {
		switch {
		case p.At < 6 && (!p.Face || math.Abs(p.X-(0.1+0.1*(p.At-0.5)+0.1)) > 0.001):
			t.Fatalf("before the cut the main face: %+v", p)
		case p.At > 6 && (p.Face || math.Abs(p.X-0.8) > 0.001):
			t.Fatalf("after the cut, no face: the region that stands out: %+v", p)
		}
	}
	if Spread(sub) < 0.5 {
		t.Fatalf("the subject moves: %.2f", Spread(sub))
	}
	if _, _, ok := Subject(nil, 0, 1); ok {
		t.Fatal("no index, nothing to follow")
	}
}

// hand builds hand joints: the wrist below, the fingers above it.
func hand(extended [4]bool, thumbUp bool) vision.Hand {
	js := make([]vision.Joint, vision.HandJoints)
	set := func(i int, x, y float64) { js[i] = vision.Joint{x, y, 0.9} }
	set(vision.HandWrist, 0.5, 0.8)
	for f, ext := range extended {
		mcp := vision.HandIndexMCP + 4*f
		x := 0.44 + 0.04*float64(f)
		set(mcp, x, 0.7)
		set(mcp+1, x, 0.65)
		if ext {
			set(mcp+2, x, 0.6)
			set(mcp+3, x, 0.55)
		} else { // curled back toward the palm
			set(mcp+2, x, 0.68)
			set(mcp+3, x, 0.72)
		}
	}
	set(vision.HandThumbCMC, 0.42, 0.78)
	set(vision.HandThumbMP, 0.4, 0.74)
	if thumbUp {
		set(vision.HandThumbIP, 0.4, 0.66)
		set(vision.HandThumbTip, 0.4, 0.58)
	} else {
		set(vision.HandThumbIP, 0.42, 0.72)
		set(vision.HandThumbTip, 0.45, 0.72)
	}
	return vision.Hand{Conf: 0.9, Joints: js}
}

// Hands: pointing (and which way), an open hand, a thumbs up; nothing for a
// hand that is unsure.
func TestHandGestures(t *testing.T) {
	if k, dir := HandGesture(hand([4]bool{true, false, false, false}, false), 1000, 1000); k != GesturePoint || dir != "up" {
		t.Fatalf("pointing up: %q %q", k, dir)
	}
	if k, _ := HandGesture(hand([4]bool{true, true, true, true}, false), 1000, 1000); k != GestureOpen {
		t.Fatalf("open hand: %q", k)
	}
	if k, _ := HandGesture(hand([4]bool{false, false, false, false}, true), 1000, 1000); k != GestureThumbsUp {
		t.Fatalf("thumbs up: %q", k)
	}
	unsure := hand([4]bool{true, false, false, false}, false)
	unsure.Conf = 0.2
	if k, _ := HandGesture(unsure, 1000, 1000); k != "" {
		t.Fatalf("an unsure hand says nothing: %q", k)
	}
	// Pointing to the left: the index finger lies along the picture.
	left := hand([4]bool{true, false, false, false}, false)
	left.Joints[vision.HandIndexPIP] = vision.Joint{0.4, 0.7, 0.9}
	left.Joints[vision.HandIndexDIP] = vision.Joint{0.36, 0.7, 0.9}
	left.Joints[vision.HandIndexTip] = vision.Joint{0.32, 0.7, 0.9}
	left.Joints[vision.HandIndexMCP] = vision.Joint{0.44, 0.7, 0.9}
	if k, dir := HandGesture(left, 1000, 1000); k != GesturePoint || dir != "left" {
		t.Fatalf("pointing left: %q %q", k, dir)
	}
}

// body builds a standing person, arms down unless changed.
func body() vision.Body {
	js := make([]vision.Joint, vision.BodyJoints)
	set := func(i int, x, y float64) { js[i] = vision.Joint{x, y, 0.9} }
	set(vision.BodyNose, 0.5, 0.2)
	set(vision.BodyLeftShoulder, 0.56, 0.3)
	set(vision.BodyRightShoulder, 0.44, 0.3)
	set(vision.BodyLeftElbow, 0.58, 0.42)
	set(vision.BodyRightElbow, 0.42, 0.42)
	set(vision.BodyLeftWrist, 0.58, 0.54)
	set(vision.BodyRightWrist, 0.42, 0.54)
	return vision.Body{Conf: 0.9, Joints: js}
}

// Bodies: a hand raised above the head, an arm stretched out to the side.
func TestBodyGesturesAndSpans(t *testing.T) {
	if gs := BodyGestures(body(), 1000, 1000); len(gs) != 0 {
		t.Fatalf("arms down: %+v", gs)
	}
	raised := body()
	raised.Joints[vision.BodyRightElbow] = vision.Joint{0.42, 0.22, 0.9}
	raised.Joints[vision.BodyRightWrist] = vision.Joint{0.42, 0.1, 0.9}
	if gs := BodyGestures(raised, 1000, 1000); len(gs) != 1 || gs[0].Kind != GestureRaised {
		t.Fatalf("hand raised: %+v", gs)
	}
	point := body()
	point.Joints[vision.BodyLeftElbow] = vision.Joint{0.66, 0.3, 0.9}
	point.Joints[vision.BodyLeftWrist] = vision.Joint{0.76, 0.3, 0.9}
	if gs := BodyGestures(point, 1000, 1000); len(gs) != 1 || gs[0].Kind != GesturePoint || gs[0].Toward != "right" {
		t.Fatalf("arm out to the right: %+v", gs)
	}
	// Two frames pointing make a gesture span, readable and searchable.
	var s []Sample
	for i := 0; i < 6; i++ {
		f := vision.Frame{W: 1000, H: 1000}
		if i == 2 || i == 3 {
			f.Bodies = []vision.Body{point}
		}
		s = append(s, Sample{At: float64(i) + 0.5, Frame: f})
	}
	idx := Build(s, 1, 6, nil, "proxy")
	if len(idx.Gestures) != 1 || idx.Gestures[0].Words() != "pointing right" || idx.Gestures[0].From != 2 || idx.Gestures[0].To != 4 {
		t.Fatalf("gesture span: %+v", idx.Gestures)
	}
	hits := Find(idx, Words("points at the screen"))
	if len(hits) == 0 || hits[0].What != "gesture" || !hits[0].Partial {
		t.Fatalf("search finds the pointing: %+v", hits)
	}
	found := false
	for _, w := range idx.Terms {
		found = found || w == "pointing"
	}
	if !found {
		t.Fatalf("search words: %v", idx.Terms)
	}
	lines := Readout(idx, nil, 0, 6, nil)
	saw := false
	for _, l := range lines {
		for _, g := range l.Gestures {
			saw = saw || g == "pointing right"
		}
	}
	if !saw {
		t.Fatalf("the readout says it: %+v", lines)
	}
	// One frame of a gesture is not enough unless it is very clear.
	one := []Sample{{At: 0.5, Frame: vision.Frame{W: 1000, H: 1000, Bodies: []vision.Body{func() vision.Body { b := point; b.Conf = 0.6; return b }()}}}, {At: 1.5, Frame: vision.Frame{W: 1000, H: 1000}}}
	if idx := Build(one, 1, 2, nil, "proxy"); len(idx.Gestures) != 0 {
		t.Fatalf("a passing glimpse: %+v", idx.Gestures)
	}
}
