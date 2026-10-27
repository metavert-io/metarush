package calls

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/visual"
)

// Printed names (D162).
const (
	tileShotMin   = 0.8 // shots shorter than this say nothing about printed names
	tileShare     = 0.6 // the share of shots that must show a name in a corner
	tileLongShot  = 3.0 // and at least one shot this long
	tileMaxHeight = 0.1 // a printed name is small
	tileMaxChars  = 40  // and short
	tileMaxWords  = 5   //
	tilePadX      = 0.015
	tilePadY      = 0.012
	tileEdge      = 0.03 // an area this close to the picture's edge reaches it
	shortShot     = 1.5  // in shots shorter than this, a printed name may belong to the shot before
)

// corner names where a box sits, or "" when it isn't in a corner.
func corner(b [4]float64) string {
	cx, cy := b[0]+b[2]/2, b[1]+b[3]/2
	h := ""
	switch {
	case cx < 0.4:
		h = "left"
	case cx > 0.6:
		h = "right"
	default:
		return ""
	}
	switch {
	case cy > 0.78:
		return "bottom-" + h
	case cy < 0.22:
		return "top-" + h
	}
	return ""
}

// nameLike reports text that can be a printed name: short, few words, letters.
func nameLike(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || len([]rune(s)) > tileMaxChars || len(strings.Fields(s)) > tileMaxWords {
		return false
	}
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	return letters >= 2 && letters*2 >= len([]rune(s))
}

type tile struct {
	text   string
	box    [4]float64
	corner string
	cover  float64 // seconds of the shot it covers
}

// shotTile is the printed name a shot shows: corner text covering most of it.
func shotTile(idx *visual.Index, from, to float64) *tile {
	if idx == nil || to-from < tileShotMin {
		return nil
	}
	var best *tile
	for _, t := range idx.Text {
		if t.Changing || !nameLike(t.Text) || t.Box[3] > tileMaxHeight {
			continue
		}
		c := corner([4]float64(t.Box))
		if c == "" {
			continue
		}
		ov := math.Min(to, t.To) - math.Max(from, t.From)
		// A span that reaches into a shot from one side also counts for that
		// shot when it covers most of it (the index sees about a frame a second).
		if ov < 0.5*(to-from) && ov < 1.0 {
			continue
		}
		if best == nil || ov > best.cover {
			best = &tile{text: strings.TrimSpace(t.Text), box: [4]float64(t.Box), corner: c, cover: ov}
		}
	}
	return best
}

// FaceNames gives a footage's face tracks their person's name and whether a
// person confirmed it (else it was recognized by looks); "" when nobody is
// known.
type FaceNames func(track int) (name string, confirmed bool)

// shotFace is the face seen most in a shot, by name.
func shotFace(idx *visual.Index, names FaceNames, from, to float64) (string, bool) {
	if idx == nil || names == nil {
		return "", false
	}
	count := map[int]float64{}
	for _, fs := range idx.Faces {
		if fs.At < from || fs.At >= to {
			continue
		}
		for k, tr := range fs.Tracks {
			size := 0.0
			if k < len(fs.Boxes) {
				size = fs.Boxes[k][3]
			}
			count[tr] += 1 + size
		}
	}
	bestT, bestV := -1, 0.0
	for tr, v := range count {
		if v > bestV || (v == bestV && tr < bestT) {
			bestT, bestV = tr, v
		}
	}
	if bestT < 0 {
		return "", false
	}
	return names(bestT)
}

// matchPerson finds the person a printed name means: the whole name, or the
// first name, of one of people (ignoring case); else "".
func matchPerson(printed string, people []string) string {
	p := strings.ToLower(strings.TrimSpace(printed))
	if p == "" {
		return ""
	}
	first := strings.ToLower(strings.TrimRight(strings.Fields(p)[0], ".,;:"))
	var byFirst []string
	for _, n := range people {
		ln := strings.ToLower(strings.TrimSpace(n))
		if ln == p {
			return n
		}
		if f := strings.Fields(ln); len(f) > 0 && f[0] == first {
			byFirst = append(byFirst, n)
		}
	}
	if len(byFirst) == 1 {
		return byFirst[0]
	}
	return ""
}

// Shots are a footage's shots between its switches and who is on screen in
// each (D160): a face a person named seen most in the shot, else the printed
// name matched to People (or as printed), else a face recognized by looks
// (not confirmed: the call app's printed name is surer), else the named voice
// that talks most (unsure: the picture can show someone listening).
func Shots(st *Stored, idx *visual.Index, names FaceNames, segs []editspec.TranscriptSegment, people []string, duration float64) []editspec.FootageShot {
	if st == nil {
		return nil
	}
	if duration <= 0 {
		duration = st.Duration
	}
	bounds := []float64{0}
	for _, s := range st.Switches {
		if s.At > bounds[len(bounds)-1]+1e-6 && s.At < duration {
			bounds = append(bounds, s.At)
		}
	}
	bounds = append(bounds, duration)
	out := make([]editspec.FootageShot, 0, len(bounds)-1)
	for i := 0; i+1 < len(bounds); i++ {
		from, to := bounds[i], bounds[i+1]
		sh := editspec.FootageShot{From: r3(from), To: r3(to)}
		if t := shotTile(idx, from, to); t != nil {
			sh.Printed = t.text
		}
		voice := editspec.SpeakerIn(segs, from, to)
		face, confirmed := shotFace(idx, names, from, to)
		switch {
		case face != "" && confirmed:
			sh.Person, sh.By = face, "face"
		case to-from < shortShot && voice != "":
			// A flip shorter than the index's step: the printed name read there
			// may still be the shot before's; who talks is the better guess.
			sh.Person, sh.By = voice, "voice"
		case sh.Printed != "":
			if p := matchPerson(sh.Printed, people); p != "" {
				sh.Person = p
			} else {
				sh.Person = sh.Printed
			}
			sh.By = "printed"
		case face != "":
			sh.Person, sh.By = face, "looks"
		case voice != "":
			sh.Person, sh.By = voice, "voice"
		}
		out = append(out, sh)
	}
	return out
}

// NameTiles finds where a call app printed names in the picture (D162): the
// corner most shots show a short, small name in, with the area to cover.
func NameTiles(st *Stored, idx *visual.Index) *editspec.PrintedNames {
	if st == nil || idx == nil || len(idx.Text) == 0 {
		return nil
	}
	shots := Shots(st, idx, nil, nil, nil, st.Duration)
	type acc struct {
		n      int
		long   bool
		x0, y0 float64
		x1, y1 float64
	}
	byCorner := map[string]*acc{}
	eligible := 0
	for _, sh := range shots {
		if sh.To-sh.From < tileShotMin {
			continue
		}
		eligible++
		t := shotTile(idx, sh.From, sh.To)
		if t == nil {
			continue
		}
		a := byCorner[t.corner]
		if a == nil {
			a = &acc{x0: 1, y0: 1}
			byCorner[t.corner] = a
		}
		a.n++
		a.long = a.long || sh.To-sh.From >= tileLongShot
		a.x0, a.y0 = math.Min(a.x0, t.box[0]), math.Min(a.y0, t.box[1])
		a.x1, a.y1 = math.Max(a.x1, t.box[0]+t.box[2]), math.Max(a.y1, t.box[1]+t.box[3])
	}
	if eligible == 0 {
		return nil
	}
	keys := make([]string, 0, len(byCorner))
	for k := range byCorner {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var best string
	for _, k := range keys {
		if best == "" || byCorner[k].n > byCorner[best].n {
			best = k
		}
	}
	if best == "" {
		return nil
	}
	a := byCorner[best]
	if float64(a.n) < tileShare*float64(eligible) || !a.long {
		return nil
	}
	x0, y0 := math.Max(0, a.x0-tilePadX), math.Max(0, a.y0-tilePadY)
	x1, y1 := math.Min(1, a.x1+tilePadX), math.Min(1, a.y1+tilePadY)
	if x0 < tileEdge {
		x0 = 0
	}
	if y0 < tileEdge {
		y0 = 0
	}
	if 1-x1 < tileEdge {
		x1 = 1
	}
	if 1-y1 < tileEdge {
		y1 = 1
	}
	return &editspec.PrintedNames{Corner: best, Box: editspec.CoverBox{X: r3(x0), Y: r3(y0), W: r3(x1 - x0), H: r3(y1 - y0)}, Shots: a.n}
}
