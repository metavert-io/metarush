package visual

import (
	"math"
	"sort"

	"github.com/metavert-io/metarush/internal/vision"
)

// Gestures (D117): from the hand and body poses Apple Vision finds, a person
// pointing (and which way, in the picture), a hand raised, a thumbs up or an
// open hand. The joints are machine guesses like everything in the index, so a
// gesture needs confident joints and is kept when it lasts two frames looked
// at (or one very clear one).

// Gesture kinds.
const (
	GesturePoint    = "pointing"
	GestureRaised   = "hand raised"
	GestureThumbsUp = "thumbs up"
	GestureOpen     = "open hand"
)

// GestureSpan is a gesture seen from From to To.
type GestureSpan struct {
	Kind string  `json:"kind" bson:"kind" jsonschema:"enum=pointing,enum=hand raised,enum=thumbs up,enum=open hand"`
	From float64 `json:"from" bson:"from"`
	To   float64 `json:"to" bson:"to"`
	// Toward is where a pointing hand points, in the picture: left, right, up or down.
	Toward string     `json:"toward,omitempty" bson:"toward,omitempty" jsonschema:"enum=left,enum=right,enum=up,enum=down"`
	Box    vision.Box `json:"box" bson:"box" jsonschema:"description=Where the hand is (x, y, width, height from the top-left, 0 to 1)."`
	Conf   float64    `json:"confidence" bson:"confidence"`
}

// Words is the gesture in plain words ("pointing left").
func (g GestureSpan) Words() string {
	if g.Kind == GesturePoint && g.Toward != "" {
		return "pointing " + g.Toward
	}
	return g.Kind
}

// GestureWords are the search words a gesture answers to.
func GestureWords(g GestureSpan) []string {
	ws := []string{"gesture"}
	switch g.Kind {
	case GesturePoint:
		ws = append(ws, "pointing", "points", "point")
	case GestureRaised:
		ws = append(ws, "hand", "raised", "raises")
	case GestureThumbsUp:
		ws = append(ws, "thumbs", "thumb", "up")
	case GestureOpen:
		ws = append(ws, "hand", "open", "palm")
	}
	return ws
}

const (
	jointMin   = 0.3  // joints less sure than this are not used
	handMin    = 0.5  // hands less sure than this are not read
	gestureOne = 0.85 // a gesture seen in one frame only needs a hand this sure
)

type pt struct{ x, y float64 }

// joint is a joint in pixels (ok false when it isn't sure enough).
func joint(js []vision.Joint, i int, w, h float64) (pt, bool) {
	if i >= len(js) || js[i][2] < jointMin {
		return pt{}, false
	}
	return pt{js[i][0] * w, js[i][1] * h}, true
}

func dist(a, b pt) float64 { return math.Hypot(a.x-b.x, a.y-b.y) }

// angle is the angle at b (degrees) between b→a and b→c.
func angle(a, b, c pt) float64 {
	v1x, v1y, v2x, v2y := a.x-b.x, a.y-b.y, c.x-b.x, c.y-b.y
	n := math.Hypot(v1x, v1y) * math.Hypot(v2x, v2y)
	if n == 0 {
		return 0
	}
	return math.Acos(math.Max(-1, math.Min(1, (v1x*v2x+v1y*v2y)/n))) * 180 / math.Pi
}

// finger says whether a finger (its MCP, PIP, DIP and tip joints) is
// stretched out (true), curled (false) or unclear (ok false).
func finger(js []vision.Joint, mcp int, w, h float64) (extended, ok bool) {
	wr, ok0 := joint(js, vision.HandWrist, w, h)
	m, ok1 := joint(js, mcp, w, h)
	p, ok2 := joint(js, mcp+1, w, h)
	t, ok3 := joint(js, mcp+3, w, h)
	if !ok0 || !ok1 || !ok2 || !ok3 {
		return false, false
	}
	straight := angle(m, p, t) > 150
	far := dist(wr, t) > 1.2*dist(wr, p)
	if straight && far {
		return true, true
	}
	if dist(wr, t) < dist(wr, p)*1.02 || angle(m, p, t) < 110 {
		return false, true
	}
	return false, false
}

// direction is where a vector points in the picture.
func direction(dx, dy float64) string {
	if math.Abs(dx) >= math.Abs(dy) {
		if dx < 0 {
			return "left"
		}
		return "right"
	}
	if dy < 0 {
		return "up"
	}
	return "down"
}

// handBox is the box around a hand's sure joints.
func handBox(js []vision.Joint) vision.Box {
	x0, y0, x1, y1 := 1.0, 1.0, 0.0, 0.0
	for _, j := range js {
		if j[2] < jointMin {
			continue
		}
		x0, y0, x1, y1 = math.Min(x0, j[0]), math.Min(y0, j[1]), math.Max(x1, j[0]), math.Max(y1, j[1])
	}
	if x1 < x0 {
		return vision.Box{}
	}
	return rbox(vision.Box{x0, y0, x1 - x0, y1 - y0})
}

// HandGesture reads one hand (w×h is the picture's size, for angles).
func HandGesture(hd vision.Hand, w, h float64) (kind, toward string) {
	if hd.Conf < handMin || len(hd.Joints) < vision.HandJoints {
		return "", ""
	}
	js := hd.Joints
	idx, okI := finger(js, vision.HandIndexMCP, w, h)
	mid, okM := finger(js, vision.HandMiddleMCP, w, h)
	ring, okR := finger(js, vision.HandRingMCP, w, h)
	lit, okL := finger(js, vision.HandLittleMCP, w, h)
	if !okI || !okM || !okR || !okL {
		return "", ""
	}
	switch {
	case idx && !mid && !ring && !lit:
		m, _ := joint(js, vision.HandIndexMCP, w, h)
		t, _ := joint(js, vision.HandIndexTip, w, h)
		return GesturePoint, direction(t.x-m.x, t.y-m.y)
	case idx && mid && ring && lit:
		return GestureOpen, ""
	case !idx && !mid && !ring && !lit:
		tip, ok1 := joint(js, vision.HandThumbTip, w, h)
		ip, ok2 := joint(js, vision.HandThumbIP, w, h)
		mp, ok3 := joint(js, vision.HandThumbMP, w, h)
		knuckle, ok4 := joint(js, vision.HandIndexMCP, w, h)
		if ok1 && ok2 && ok3 && ok4 && angle(mp, ip, tip) > 140 && tip.y < ip.y && ip.y < mp.y && tip.y < knuckle.y-0.5*dist(mp, tip) {
			return GestureThumbsUp, ""
		}
	}
	return "", ""
}

// BodyGestures reads a body pose: a hand raised above the head, or an arm
// stretched out to the side (pointing that way).
func BodyGestures(b vision.Body, w, h float64) []struct{ Kind, Toward string } {
	var out []struct{ Kind, Toward string }
	if b.Conf < handMin || len(b.Joints) < vision.BodyJoints {
		return out
	}
	js := b.Joints
	nose, okN := joint(js, vision.BodyNose, w, h)
	ls, okLS := joint(js, vision.BodyLeftShoulder, w, h)
	rs, okRS := joint(js, vision.BodyRightShoulder, w, h)
	if !okLS || !okRS {
		return out
	}
	shoulders := dist(ls, rs)
	if shoulders < 8 {
		return out
	}
	for _, side := range [][3]int{{vision.BodyLeftShoulder, vision.BodyLeftElbow, vision.BodyLeftWrist}, {vision.BodyRightShoulder, vision.BodyRightElbow, vision.BodyRightWrist}} {
		s, ok1 := joint(js, side[0], w, h)
		e, ok2 := joint(js, side[1], w, h)
		wr, ok3 := joint(js, side[2], w, h)
		if !ok1 || !ok2 || !ok3 {
			continue
		}
		top := s.y - 0.5*shoulders
		if okN {
			top = math.Min(top, nose.y)
		}
		switch {
		case wr.y < top:
			out = append(out, struct{ Kind, Toward string }{GestureRaised, ""})
		case angle(s, e, wr) > 155 && math.Abs(wr.x-s.x) > 1.3*shoulders && math.Abs(wr.y-s.y) < 0.7*math.Abs(wr.x-s.x):
			out = append(out, struct{ Kind, Toward string }{GesturePoint, direction(wr.x-s.x, 0)})
		}
	}
	return out
}

type gestureHit struct {
	key, kind, toward string
	at, conf          float64
	box               vision.Box
}

// gestureSpans joins the gestures seen frame by frame into spans.
func gestureSpans(samples []Sample, maxGap float64, clampF, clampT func(float64) float64, shotOf func(float64) int) []GestureSpan {
	var hits []gestureHit
	for _, s := range samples {
		f := s.Frame
		if f.Error != "" || (len(f.Hands) == 0 && len(f.Bodies) == 0) {
			continue
		}
		w, h := float64(max(1, f.W)), float64(max(1, f.H))
		seen := map[string]bool{}
		for _, hd := range f.Hands {
			kind, toward := HandGesture(hd, w, h)
			if kind == "" || kind == GestureOpen && hd.Conf < 0.7 {
				continue
			}
			key := kind + "/" + toward
			if !seen[key] {
				seen[key] = true
				hits = append(hits, gestureHit{key, kind, toward, s.At, hd.Conf, handBox(hd.Joints)})
			}
		}
		for _, b := range f.Bodies {
			for _, g := range BodyGestures(b, w, h) {
				key := g.Kind + "/" + g.Toward
				if !seen[key] {
					seen[key] = true
					hits = append(hits, gestureHit{key, g.Kind, g.Toward, s.At, b.Conf * 0.9, handBox(b.Joints)})
				}
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].at < hits[j].at })
	type run struct {
		g    GestureSpan
		n    int
		last float64
	}
	var open []*run
	var done []run
	for _, h := range hits {
		var r *run
		for _, o := range open {
			if o.g.Kind+"/"+o.g.Toward == h.key && h.at-o.last <= maxGap && shotOf(h.at) == shotOf(o.last) {
				r = o
				break
			}
		}
		if r == nil {
			r = &run{g: GestureSpan{Kind: h.kind, Toward: h.toward, From: h.at, Box: h.box, Conf: h.conf}}
			open = append(open, r)
		}
		r.n++
		r.last = h.at
		r.g.To = h.at
		if h.conf > r.g.Conf {
			r.g.Conf, r.g.Box = h.conf, h.box
		}
	}
	for _, o := range open {
		done = append(done, *o)
	}
	out := []GestureSpan{}
	for _, r := range done {
		if r.n < 2 && r.g.Conf < gestureOne {
			continue
		}
		g := r.g
		g.From, g.To, g.Conf = clampF(g.From), clampT(g.To), r3(g.Conf)
		out = append(out, g)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}
