package visual

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Line is one stretch of the "what's on screen" readout.
type Line struct {
	From    float64  `json:"from"`
	To      float64  `json:"to"`
	Shot    string   `json:"shot" jsonschema:"enum=close-up,enum=medium,enum=wide,enum=none"`
	Faces   int      `json:"faces" jsonschema:"description=Most faces seen at once."`
	People  int      `json:"people" jsonschema:"description=Most people (faces or bodies) seen at once."`
	Names   []string `json:"names" jsonschema:"description=Whose faces these are, where a person confirmed it; maybe-names are questions still open."`
	Setting string   `json:"setting,omitempty" jsonschema:"description=Indoors or outdoors and the place, when the picture makes it clear."`
	Things  []string `json:"things" jsonschema:"description=Things or animals there for most of the stretch."`
	Text    []string `json:"text" jsonschema:"description=On-screen text."`
	// Gestures are hands pointing, raised or giving a thumbs up in the stretch.
	Gestures []string `json:"gestures,omitempty" jsonschema:"description=Gestures seen: pointing left, hand raised, thumbs up, open hand."`
	Line     string   `json:"line" jsonschema:"description=The whole stretch in plain words."`
}

// FaceName says whose face a track is: a confirmed name, or a possible one (sure false).
type FaceName func(track int) (name string, sure bool)

var shotLine = map[string]string{CloseUp: "close-up", Medium: "medium shot", Wide: "wide shot", NoOne: "no people"}

// counterText stands for a clock or counter in a stretch's text.
const counterText = "\x00counter"

// Clock formats seconds as M:SS, with tenths when they matter.
func Clock(t float64) string {
	t = math.Round(t*10) / 10
	m := int(t) / 60
	s := t - float64(m*60)
	if math.Abs(s-math.Round(s)) < 0.05 {
		return fmt.Sprintf("%d:%02d", m, int(math.Round(s)))
	}
	return fmt.Sprintf("%d:%04.1f", m, s)
}

type seg struct {
	from, to float64
	shot     string
	faces    int
	people   int
	names    []string
	text     []string
}

func (s seg) key() string {
	return fmt.Sprintf("%s|%d|%d|%s|%s", s.shot, s.faces, s.people, strings.Join(s.names, ","), strings.Join(s.text, "\x00"))
}

// Readout describes [from, to) in stretches that change at cuts, at a new
// framing or number of faces, and where text appears or goes.
func Readout(idx *Index, cuts []float64, from, to float64, names FaceName) []Line {
	if to <= 0 || to > idx.Duration {
		to = idx.Duration
	}
	if from < 0 {
		from = 0
	}
	if to-from <= 0 || len(idx.Shots) == 0 {
		return []Line{}
	}
	cutSet := map[float64]bool{}
	bset := map[float64]bool{from: true, to: true}
	add := func(t float64) {
		if t > from+0.01 && t < to-0.01 {
			bset[r3(t)] = true
		}
	}
	for _, c := range cuts {
		add(c)
		cutSet[r3(c)] = true
	}
	for _, s := range idx.Shots {
		add(s.From)
		add(s.To)
	}
	for _, t := range idx.Text {
		add(t.From)
		add(t.To)
	}
	var bounds []float64
	for t := range bset {
		bounds = append(bounds, t)
	}
	sort.Float64s(bounds)
	var segs []seg
	for i := 0; i+1 < len(bounds); i++ {
		a, b := bounds[i], bounds[i+1]
		s := describe(idx, a, b, names)
		if n := len(segs); n > 0 && segs[n-1].key() == s.key() && !cutSet[r3(a)] {
			segs[n-1].to = b
			continue
		}
		segs = append(segs, s)
	}
	// Slivers shorter than a second join their neighbor in the same shot.
	for i := 0; i < len(segs); {
		s := segs[i]
		if s.to-s.from >= 1 || len(segs) == 1 {
			i++
			continue
		}
		switch {
		case i > 0 && !cutSet[r3(s.from)]:
			segs[i-1].to = s.to
		case i+1 < len(segs) && !cutSet[r3(s.to)]:
			segs[i+1].from = s.from
		default:
			i++
			continue
		}
		segs = append(segs[:i], segs[i+1:]...)
	}
	out := make([]Line, 0, len(segs))
	for _, s := range segs {
		setting, things := Notable(idx, s.from, s.to, 2, 0.5)
		ln := Line{From: r3(s.from), To: r3(s.to), Shot: s.shot, Faces: s.faces, People: s.people, Names: s.names, Setting: setting, Things: things, Text: s.text}
		if ln.Names == nil {
			ln.Names = []string{}
		}
		if ln.Things == nil {
			ln.Things = []string{}
		}
		if ln.Text == nil {
			ln.Text = []string{}
		}
		parts := []string{shotLine[s.shot]}
		switch {
		case s.faces > 0:
			f := count(s.faces, "face", "faces")
			if len(s.names) > 0 {
				f += " (" + strings.Join(s.names, ", ") + ")"
			}
			parts = append(parts, f)
		case s.people > 0:
			parts = append(parts, count(s.people, "person", "people")+", face not visible")
		}
		if setting != "" {
			parts = append(parts, setting)
		}
		parts = append(parts, things...)
		var q []string
		counter := false
		for _, t := range s.text {
			if t == counterText {
				counter = true
				continue
			}
			q = append(q, "“"+t+"”")
		}
		switch {
		case len(q) > 0:
			parts = append(parts, "text "+strings.Join(q, ", "))
		case !counter:
			parts = append(parts, "no text")
		}
		if counter {
			parts = append(parts, "a clock or counter")
		}
		ln.Gestures = gesturesIn(idx, s.from, s.to)
		parts = append(parts, ln.Gestures...)
		ln.Line = Clock(s.from) + "–" + Clock(s.to) + " " + strings.Join(parts, " · ")
		out = append(out, ln)
	}
	return out
}

// gesturesIn are the gestures seen during [a, b) in words, each once.
func gesturesIn(idx *Index, a, b float64) []string {
	var out []string
	seen := map[string]bool{}
	for _, g := range idx.Gestures {
		if g.To > a && g.From < b && !seen[g.Words()] {
			seen[g.Words()] = true
			out = append(out, g.Words())
		}
	}
	return out
}

// describe sums up [a, b).
func describe(idx *Index, a, b float64, names FaceName) seg {
	s := seg{from: a, to: b, shot: NoOne}
	best := 0.0
	for _, sp := range idx.Shots {
		if ov := math.Min(b, sp.To) - math.Max(a, sp.From); ov > best {
			best, s.shot = ov, sp.Type
		}
		if sp.To > a && sp.From < b {
			s.people = max(s.people, sp.People)
		}
	}
	seenTrack := map[int]bool{}
	var tracks []int
	for _, f := range idx.Faces {
		if f.At < a || f.At >= b {
			continue
		}
		s.faces = max(s.faces, len(f.Boxes))
		for _, t := range f.Tracks {
			if !seenTrack[t] {
				seenTrack[t] = true
				tracks = append(tracks, t)
			}
		}
	}
	if s.faces > s.people {
		s.people = s.faces
	}
	if names != nil {
		seenName := map[string]bool{}
		for _, t := range tracks {
			n, sure := names(t)
			if n == "" {
				continue
			}
			if !sure {
				n = "maybe " + n
			}
			if !seenName[n] {
				seenName[n] = true
				s.names = append(s.names, n)
			}
		}
	}
	seenText := map[string]bool{}
	for _, t := range idx.Text {
		if t.Changing && DigitHeavy(t.Text) {
			if t.To > a && t.From < b && !seenText[counterText] {
				seenText[counterText] = true
				s.text = append(s.text, counterText)
			}
			continue
		}
		if t.To > a && t.From < b && !seenText[t.Text] {
			// Mostly inside this stretch, or covering most of it.
			if ov := math.Min(b, t.To) - math.Max(a, t.From); ov >= 0.5*math.Min(b-a, t.To-t.From) {
				seenText[t.Text] = true
				s.text = append(s.text, t.Text)
			}
		}
	}
	return s
}

// FaceFocus is where the main face is during [from, to): the median center of
// the biggest face in each frame that shows one (0–1 from the left and top).
func FaceFocus(idx *Index, from, to float64) (x, y float64, frames int) {
	var xs, ys []float64
	for _, f := range idx.Faces {
		if f.At < from || f.At >= to || len(f.Boxes) == 0 {
			continue
		}
		big := f.Boxes[0]
		for _, b := range f.Boxes[1:] {
			if b[2]*b[3] > big[2]*big[3] {
				big = b
			}
		}
		xs = append(xs, big[0]+big[2]/2)
		ys = append(ys, big[1]+big[3]/2)
	}
	if len(xs) == 0 {
		return 0, 0, 0
	}
	sort.Float64s(xs)
	sort.Float64s(ys)
	return r3(xs[len(xs)/2]), r3(ys[len(ys)/2]), len(xs)
}

// FaceBox is the main face's box during [from, to): the median of the
// biggest face in each frame that shows one (x, y, width, height from the
// top-left, 0–1). Conversation layouts scale faces to one size with it (D102).
func FaceBox(idx *Index, from, to float64) ([4]float64, bool) {
	var xs, ys, ws, hs []float64
	for _, f := range idx.Faces {
		if f.At < from || f.At >= to || len(f.Boxes) == 0 {
			continue
		}
		big := f.Boxes[0]
		for _, b := range f.Boxes[1:] {
			if b[2]*b[3] > big[2]*big[3] {
				big = b
			}
		}
		xs, ys, ws, hs = append(xs, big[0]), append(ys, big[1]), append(ws, big[2]), append(hs, big[3])
	}
	if len(xs) == 0 {
		return [4]float64{}, false
	}
	med := func(v []float64) float64 { sort.Float64s(v); return r3(v[len(v)/2]) }
	return [4]float64{med(xs), med(ys), med(ws), med(hs)}, true
}

// Hit is a stretch of a clip where query words show up on screen.
type Hit struct {
	What  string  `json:"what" jsonschema:"enum=label,enum=text,enum=gesture"`
	Text  string  `json:"text"`
	From  float64 `json:"from"`
	To    float64 `json:"to"`
	Exact bool    `json:"exact" jsonschema:"description=Every query word matched a whole word."`
	// Partial: only some of the query words are on screen here (EXIT for "EXIT sign" when no sign was recognized).
	Partial bool `json:"partial,omitempty"`
}

// Find returns the stretches of the clip where the query words show up as a
// label or in on-screen text (whole words; a word may also be the start of a
// longer one when it has at least four letters).
func Find(idx *Index, words []string) []Hit {
	if len(words) == 0 {
		return nil
	}
	match := func(text string) (bool, bool) {
		ws := Words(text)
		all, exact := true, true
		for _, q := range words {
			found, whole := false, false
			for _, w := range ws {
				if w == q {
					found, whole = true, true
					break
				}
				if len([]rune(q)) >= 4 && strings.HasPrefix(w, q) {
					found = true
				}
			}
			if !found {
				all = false
				break
			}
			exact = exact && whole
		}
		return all, all && exact
	}
	var out []Hit
	for _, l := range idx.Labels {
		if ok, ex := match(l.Label); ok {
			out = append(out, Hit{What: "label", Text: l.Label, From: l.From, To: l.To, Exact: ex})
		}
	}
	for _, t := range idx.Text {
		if ok, ex := match(t.Text); ok {
			out = append(out, Hit{What: "text", Text: t.Text, From: t.From, To: t.To, Exact: ex})
		}
	}
	for _, g := range idx.Gestures {
		if ok, ex := match(g.Words() + " " + strings.Join(GestureWords(g), " ")); ok {
			out = append(out, Hit{What: "gesture", Text: g.Words(), From: g.From, To: g.To, Exact: ex})
		}
	}
	// A query that spans a label and text ("EXIT sign"): any part matching both at once.
	if len(out) == 0 && len(words) > 1 {
		for _, l := range idx.Labels {
			for _, t := range idx.Text {
				if ok, _ := match(l.Label + " " + t.Text); ok && l.To > t.From && l.From < t.To {
					out = append(out, Hit{What: "text", Text: t.Text + " (" + l.Label + ")", From: math.Max(l.From, t.From), To: math.Min(l.To, t.To)})
				}
			}
		}
	}
	// Still nothing: what matches some of the words (of three letters or more).
	if len(out) == 0 && len(words) > 1 {
		for _, q := range words {
			if len([]rune(q)) < 3 {
				continue
			}
			for _, h := range Find(idx, []string{q}) {
				h.Exact, h.Partial = false, true
				out = append(out, h)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}
