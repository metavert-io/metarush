package faces

import (
	"math"
	"sort"

	"github.com/metavert-io/metarush/internal/names"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// Names the video shows (D156): text from the on-screen index that reads like
// a person's name and sits under or near a face while it is on screen — a
// call app's name tile, a lower third — or a title just before the face first
// appears. Evidence for a question, never a name by itself.

// Kinds of shown names, strongest first.
const (
	ShownTile  = "tile"  // there most of the time the face is (a call app's name under the picture)
	ShownLower = "lower" // a lower third while the face is on screen
	ShownTitle = "title" // a title just before the face first appears, nobody on screen
)

// Thresholds for shown names.
const (
	tileShare    = 0.6 // a name tile is there for at least this share of the group's time
	exclusive    = 0.6 // …and at least this share of the name's time is with this group
	lowerSeconds = 2.0 // a lower third shows for at least this long with the face
	titleBefore  = 5.0 // a title ends at most this long before the face first appears
	// shownSure: a name nobody in People has is read only from text the index
	// is this sure of (Apple Vision says 1.0 for a clean name tile and 0.5 for a
	// misread sign: "SALE ENDS SUNDAY" read as "SHLE ERS SUHDAY"); the name of
	// someone in People counts however sure.
	shownSure = 0.8
)

// shownAcc counts how often a name is shown with one face track.
type shownAcc struct {
	name  string
	text  string
	n     int
	lower int
	first float64
}

// screenNames reads the index's text as names and finds, for each face track,
// the names shown under or near it (per sampled moment) and how many moments
// the track was sampled at; titles are names shown with nobody on screen.
type screenNames struct {
	byTrack map[int]map[string]*shownAcc
	samples map[int]int
	titles  []shownTitle
	step    float64
}

type shownTitle struct {
	name, text string
	from, to   float64
}

// readScreenNames: known resolves text to the name of someone in People
// (case and accents ignored), so their names count even when written oddly.
func readScreenNames(idx *visual.Index, known func(string) (string, bool)) *screenNames {
	out := &screenNames{byTrack: map[int]map[string]*shownAcc{}, samples: map[int]int{}, step: idx.Step}
	if out.step <= 0 {
		out.step = 1
	}
	type span struct {
		t    visual.TextSpan
		name string
	}
	var spans []span
	for _, t := range idx.Text {
		if t.Changing {
			continue
		}
		n, ok := known(t.Text)
		if !ok && t.Conf >= shownSure {
			n, ok = names.FromScreen(t.Text)
		}
		if ok {
			spans = append(spans, span{t, n})
		}
	}
	faceAt := map[float64]bool{}
	for _, fs := range idx.Faces {
		if len(fs.Boxes) > 0 {
			faceAt[fs.At] = true
		}
		for _, tr := range fs.Tracks {
			out.samples[tr]++
		}
		if len(spans) == 0 {
			continue
		}
		for _, sp := range spans {
			if fs.At < sp.t.From || fs.At > sp.t.To {
				continue
			}
			k := under(sp.t.Box, fs.Boxes)
			if k < 0 || k >= len(fs.Tracks) {
				continue
			}
			tr := fs.Tracks[k]
			m := out.byTrack[tr]
			if m == nil {
				m = map[string]*shownAcc{}
				out.byTrack[tr] = m
			}
			key := names.Fold(sp.name)
			a := m[key]
			if a == nil {
				a = &shownAcc{name: sp.name, text: sp.t.Text, first: fs.At}
				m[key] = a
			}
			a.n++
			if sp.t.Box[1]+sp.t.Box[3]/2 >= 0.6 {
				a.lower++
			}
			if fs.At < a.first {
				a.first = fs.At
			}
		}
	}
	// Titles: names shown while no face is.
	for _, sp := range spans {
		nobody := true
		for at := range faceAt {
			if at >= sp.t.From && at <= sp.t.To {
				nobody = false
				break
			}
		}
		if nobody {
			out.titles = append(out.titles, shownTitle{name: sp.name, text: sp.t.Text, from: sp.t.From, to: sp.t.To})
		}
	}
	sort.SliceStable(out.titles, func(i, j int) bool { return out.titles[i].from < out.titles[j].from })
	return out
}

// under says which face a piece of text is the name of (its index in boxes),
// or -1: with one face on screen, text in the lower part of the picture or
// under the face; with several, a face the text is below (a gallery view's
// tiles put the name at the bottom left of the tile, a few face widths from a
// face in the middle of it), the one closest across, then closest down.
func under(t vision.Box, boxes []vision.Box) int {
	tx, ty, tw, th := t[0], t[1], t[2], t[3]
	cx := tx + tw/2
	// Text right under a face, in line with it, is on their clothes or a
	// badge (a name tape, a logo), not a name tile or a lower third.
	for _, f := range boxes {
		fbottom := f[1] + f[3]
		if cx >= f[0]-0.1*f[2] && cx <= f[0]+1.1*f[2] && ty >= fbottom-0.1*f[3] && ty <= fbottom+1.2*f[3] {
			return -1
		}
	}
	if len(boxes) == 1 {
		f := boxes[0]
		if ty+th/2 >= 0.6 {
			return 0
		}
		if ty >= f[1]+f[3]/2 && math.Abs(cx-(f[0]+f[2]/2)) <= 2.5*f[2] {
			return 0
		}
		return -1
	}
	best, bestD := -1, math.Inf(1)
	for k, f := range boxes {
		fcx, fbottom := f[0]+f[2]/2, f[1]+f[3]
		if ty < f[1]+f[3]/2 || ty > fbottom+3*f[3] {
			continue
		}
		dx := math.Abs(cx - fcx)
		if dx > 4*f[2] {
			continue
		}
		if d := dx + 0.5*math.Abs(ty-fbottom); d < bestD {
			best, bestD = k, d
		}
	}
	return best
}

// ShownName is a name the video shows with a group of faces.
type ShownName struct {
	Name    string  `json:"name"`
	Text    string  `json:"text" jsonschema:"description=As read on screen."`
	Kind    string  `json:"kind" jsonschema:"enum=tile,enum=lower,enum=title"`
	Share   float64 `json:"share" jsonschema:"description=Of the moments the faces are on screen\\, the share the name is shown with them."`
	Seconds float64 `json:"seconds"`
	At      float64 `json:"at" jsonschema:"description=First time it is shown with them (source seconds)."`
}

// shownFor sums what the tracks of one group were shown with and decides
// which names count: a tile (there most of the time, mostly with this group),
// a lower third (≥ 2 s in the lower part of the picture, mostly with this
// group) or a title just before the group first appears. Strongest first.
// same says whether a track outside the group is already named as that name
// (its time with the name doesn't count against the group).
func (sn *screenNames) shownFor(tracks []int, first float64, same func(track int, name string) bool) []ShownName {
	if sn == nil {
		return nil
	}
	mineTr := map[int]bool{}
	for _, tr := range tracks {
		mineTr[tr] = true
	}
	total := 0
	acc := map[string]*shownAcc{}
	for _, tr := range tracks {
		total += sn.samples[tr]
		for k, a := range sn.byTrack[tr] {
			b := acc[k]
			if b == nil {
				c := *a
				c.n, c.lower = 0, 0
				b = &c
				acc[k] = b
			}
			b.n += a.n
			b.lower += a.lower
			b.first = math.Min(b.first, a.first)
		}
	}
	var out []ShownName
	if total > 0 {
		for k, a := range acc {
			// How much of this name's time is with other people's faces.
			others := 0
			for tr, m := range sn.byTrack {
				if x, ok := m[k]; ok && !mineTr[tr] && (same == nil || !same(tr, a.name)) {
					others += x.n
				}
			}
			mine := float64(a.n) / math.Max(1, float64(a.n+others))
			share := float64(a.n) / float64(total)
			sec := float64(a.n) * sn.step
			switch {
			case share >= tileShare && mine >= exclusive:
				out = append(out, ShownName{Name: a.name, Text: a.text, Kind: ShownTile, Share: round2(share), Seconds: sec, At: a.first})
			case a.lower*2 >= a.n && sec >= lowerSeconds && mine >= exclusive:
				out = append(out, ShownName{Name: a.name, Text: a.text, Kind: ShownLower, Share: round2(share), Seconds: sec, At: a.first})
			}
		}
	}
	// The title closest before the group's first appearance.
	for i := len(sn.titles) - 1; i >= 0; i-- {
		t := sn.titles[i]
		if t.to <= first+0.5 && first-t.to <= titleBefore {
			out = append(out, ShownName{Name: t.name, Text: t.text, Kind: ShownTitle, Seconds: t.to - t.from, At: t.from})
			break
		}
	}
	rank := map[string]int{ShownTile: 0, ShownLower: 1, ShownTitle: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Kind] != rank[out[j].Kind] {
			return rank[out[i].Kind] < rank[out[j].Kind]
		}
		return out[i].Share > out[j].Share
	})
	return out
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }
