// Package visual turns what Apple Vision saw in a clip's frames (internal/vision,
// about one frame a second plus the moments the picture changes) into a compact
// per-clip timeline (D73): label spans (scene, things, animals, people),
// on-screen text spans, face positions grouped into tracks, and the shot type
// (close-up, medium, wide) from face size. From it come the search words, the
// plain "what's on screen" readout and the focus point for automatic framing.
// It is pure computation; the worker stores the result as the clip's "visual"
// insight and its face tracks in the faces collection.
package visual

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/vision"
)

// Version identifies the index format and thresholds (a new version re-indexes clips).
// vision-2: face fingerprints for recognizing people across clips (D114), where
// the eye is drawn and the footage's cuts (framing that follows movement, D115),
// gestures and text in any language (D117).
const Version = "vision-2"

// InsightKind is the insight that keeps a clip's index.
const InsightKind = "visual"

// Shot types.
const (
	CloseUp = "close-up"
	Medium  = "medium"
	Wide    = "wide"
	NoOne   = "none" // nobody in the picture
)

// Label kinds.
const (
	KindScene  = "scene"
	KindThing  = "thing"
	KindAnimal = "animal"
	KindPerson = "person"
)

// Thresholds. Changing them changes the index: bump Version.
const (
	labelMin      = 0.30 // a classification counts at this confidence when Apple says it is precise there…
	labelSure     = 0.55 // …or at this confidence anyway
	labelSolo     = 0.60 // a label seen in one frame only must be at least this sure
	animalMin     = 0.40
	personMin     = 0.50
	faceMin       = 0.50
	textMin       = 0.40
	closeUpFace   = 0.25 // face height (share of the picture) of a close-up…
	mediumFace    = 0.10 // …and of a medium shot (smaller is wide)
	bodyMedium    = 0.60 // a body this tall with no face visible: medium, else wide
	trackIoU      = 0.20
	trackCenter   = 0.15
	printOther    = 0.25 // a face this unlike a track's last fingerprint is someone else, wherever it is
	printSame     = 0.45 // a face that jumped to another place must look at least this alike to be the same person
	maxLabelSpans = 3000
	maxTextSpans  = 1000
	maxTerms      = 2000
)

// Sample is one analyzed frame and when it is.
type Sample struct {
	At    float64
	Frame vision.Frame
	// Prints are the faces' fingerprints (by index in Frame.Faces; nil where a
	// face had none: too small, no landmarks, or recognition not set up).
	Prints [][]float32
}

// LabelSpan is something seen from From to To.
type LabelSpan struct {
	Label string  `json:"label" bson:"label" jsonschema:"description=Plain words (palm tree, dog, person)."`
	Kind  string  `json:"kind" bson:"kind" jsonschema:"enum=scene,enum=thing,enum=animal,enum=person"`
	From  float64 `json:"from" bson:"from"`
	To    float64 `json:"to" bson:"to"`
	Conf  float64 `json:"confidence" bson:"confidence"`
}

// TextSpan is on-screen text from From to To.
type TextSpan struct {
	Text string     `json:"text" bson:"text"`
	From float64    `json:"from" bson:"from"`
	To   float64    `json:"to" bson:"to"`
	Box  vision.Box `json:"box" bson:"box" jsonschema:"description=Where it is: x, y, width, height from the top-left, 0 to 1."`
	Conf float64    `json:"confidence" bson:"confidence"`
	// Changing: the text kept changing (a clock, a counter); Text is its first reading.
	Changing bool `json:"changing,omitempty" bson:"changing,omitempty"`
}

// ShotSpan is how the picture is framed from From to To.
type ShotSpan struct {
	Type   string  `json:"type" bson:"type" jsonschema:"enum=close-up,enum=medium,enum=wide,enum=none"`
	From   float64 `json:"from" bson:"from"`
	To     float64 `json:"to" bson:"to"`
	Faces  int     `json:"faces" bson:"faces" jsonschema:"description=Most faces seen at once."`
	People int     `json:"people" bson:"people" jsonschema:"description=Most people (faces or bodies) seen at once."`
}

// FaceSample is where the faces are at one moment.
type FaceSample struct {
	At     float64      `json:"at" bson:"at"`
	Boxes  []vision.Box `json:"boxes" bson:"boxes"`
	Tracks []int        `json:"tracks" bson:"tracks" jsonschema:"description=The face track each box belongs to."`
}

// BestFace is the frame that shows a face track best.
type BestFace struct {
	At      float64    `json:"at" bson:"at"`
	Box     vision.Box `json:"box" bson:"box"`
	Quality float64    `json:"quality" bson:"quality"`
}

// Track is one face followed from frame to frame within a shot.
type Track struct {
	ID      int        `json:"id" bson:"id"`
	From    float64    `json:"from" bson:"from"`
	To      float64    `json:"to" bson:"to"`
	Samples int        `json:"samples" bson:"samples"`
	Box     vision.Box `json:"box" bson:"box" jsonschema:"description=Average place in the picture."`
	Best    BestFace   `json:"best" bson:"best"`
	// Prints are the track's best face fingerprints (kept with the face in the
	// faces collection, never in the index or sent to clients).
	Prints []TrackPrint `json:"-" bson:"-"`
}

// TrackPrint is one face fingerprint of a track and how usable its frame was.
type TrackPrint struct {
	At      float64
	Quality float64
	Vec     []float32
}

// MaxTrackPrints is how many fingerprints a face keeps (the best frames).
const MaxTrackPrints = 6

// Index is a clip's on-screen timeline.
type Index struct {
	Version  string       `json:"version" bson:"version"`
	Source   string       `json:"source" bson:"source" jsonschema:"enum=proxy,enum=original"`
	Step     float64      `json:"step" bson:"step" jsonschema:"description=Seconds between regular samples."`
	Frames   int          `json:"frames" bson:"frames" jsonschema:"description=Frames looked at."`
	Duration float64      `json:"duration" bson:"duration"`
	Labels   []LabelSpan  `json:"labels" bson:"labels"`
	Text     []TextSpan   `json:"text" bson:"text"`
	Shots    []ShotSpan   `json:"shots" bson:"shots"`
	Faces    []FaceSample `json:"faces" bson:"faces"`
	Tracks   []Track      `json:"tracks" bson:"tracks"`
	Terms    []string     `json:"terms" bson:"terms" jsonschema:"description=Search words: labels and on-screen text."`
	Summary  string       `json:"summary" bson:"summary"`
	// Cuts are the footage's own hard cuts the index was built with (vision-2).
	Cuts []float64 `json:"cuts,omitempty" bson:"cuts,omitempty"`
	// Attention is where the eye is drawn in each frame looked at (vision-2):
	// framing that follows movement uses it where no face is seen (D115).
	Attention []AttentionSample `json:"attention,omitempty" bson:"attention,omitempty"`
	// Gestures are hands pointing, raised or giving a thumbs up (vision-2, D117).
	Gestures []GestureSpan `json:"gestures,omitempty" bson:"gestures,omitempty"`
}

// AttentionSample is where the eye is drawn at one moment: the middle of
// what stands out (0–1 from the left and top) and how strongly it stands out.
type AttentionSample struct {
	At       float64 `json:"at" bson:"at"`
	X        float64 `json:"x" bson:"x"`
	Y        float64 `json:"y" bson:"y"`
	Strength float64 `json:"strength" bson:"strength" jsonschema:"description=0 (nothing stands out) to 1."`
	// Box is the region that stands out most, when Apple Vision found one.
	Box *vision.Box `json:"box,omitempty" bson:"box,omitempty"`
}

// sceneLabels are the classifier labels that describe where a shot is.
var sceneLabels = map[string]bool{"outdoor": true, "interior_room": true, "interior_shop": true, "beach": true, "kitchen": true, "kitchen_room": true,
	"living_room": true, "bedroom": true, "bathroom": true, "bathroom_room": true, "dining_room": true, "classroom": true, "office": true, "street": true,
	"forest": true, "mountain": true, "park": true, "garden": true, "desert": true, "snow": true, "sky": true, "ocean": true, "lake": true, "river": true,
	"city": true, "restaurant": true, "stadium": true, "arena": true, "stage": true, "studio": true, "field": true, "farm": true, "playground": true,
	"swimming_pool": true, "airport": true, "station": true, "road": true, "highway": true, "bridge": true, "night_sky": true, "underwater": true}

// generic labels say little on their own: kept for search, left out of the readout.
var generic = map[string]bool{"people": true, "adult": true, "structure": true, "document": true, "art": true, "decoration": true, "material": true,
	"liquid": true, "clothing": true, "screenshot": true, "chart": true, "diagram": true, "recreation": true, "performance": true, "entertainer": true,
	"cloudy": true, "blue_sky": true, "sky": true, "plant": true, "tree": true, "water": true, "water_body": true, "conveyance": true, "machine": true,
	"textile": true, "furniture": true, "wood_processed": true, "consumer_electronics": true, "optical_equipment": true, "cord": true, "light": true,
	"animal": true, "mammal": true, "outdoor": true, "interior_room": true, "land": true, "sport": true, "watersport": true, "equipment": true}

func words(id string) string { return strings.ReplaceAll(id, "_", " ") }

// Generic reports labels that say little on their own ("people", "adult",
// "liquid"): search keeps them, readouts leave them out.
func Generic(label string) bool { return generic[strings.ReplaceAll(label, " ", "_")] }

// indoor reports a classifier label that means "inside".
func indoor(id string) bool {
	return strings.HasPrefix(id, "interior_") || strings.HasSuffix(id, "_room") || id == "kitchen" || id == "bedroom" || id == "bathroom" ||
		id == "classroom" || id == "office" || id == "restaurant" || id == "studio"
}

type obs struct {
	label string
	kind  string
	conf  float64
}

// seen lists the labels of one frame (classification, animals, people).
func seen(f vision.Frame) []obs {
	var out []obs
	have := map[string]bool{}
	add := func(l, k string, c float64) {
		if have[l] {
			return
		}
		have[l] = true
		out = append(out, obs{l, k, c})
	}
	for _, l := range f.Labels {
		if (l.Precise && l.Conf >= labelMin) || l.Conf >= labelSure {
			k := KindThing
			if sceneLabels[l.ID] || indoor(l.ID) {
				k = KindScene
			}
			add(words(l.ID), k, l.Conf)
		}
	}
	for _, an := range f.Animals {
		if an.Conf >= animalMin {
			add(strings.ToLower(an.ID), KindAnimal, an.Conf)
		}
	}
	if n := people(f); n > 0 {
		best := 0.0
		for _, fc := range f.Faces {
			best = math.Max(best, fc.Conf)
		}
		for _, h := range f.Humans {
			best = math.Max(best, h.Conf)
		}
		add("person", KindPerson, best)
	}
	return out
}

func faces(f vision.Frame) []vision.Face {
	var out []vision.Face
	for _, fc := range f.Faces {
		if fc.Conf >= faceMin {
			out = append(out, fc)
		}
	}
	return out
}

// people counts the people in a frame: faces, or bodies when there are more bodies.
func people(f vision.Frame) int {
	nf, nh := len(faces(f)), 0
	for _, h := range f.Humans {
		if h.Conf >= personMin {
			nh++
		}
	}
	return max(nf, nh)
}

// ShotType frames a picture by the biggest face, or the biggest body when no face shows.
func ShotType(f vision.Frame) string {
	fh := 0.0
	for _, fc := range faces(f) {
		fh = math.Max(fh, fc.Box[3])
	}
	switch {
	case fh >= closeUpFace:
		return CloseUp
	case fh >= mediumFace:
		return Medium
	case fh > 0:
		return Wide
	}
	bh := 0.0
	for _, h := range f.Humans {
		if h.Conf >= personMin {
			bh = math.Max(bh, h.Box[3])
		}
	}
	switch {
	case bh >= bodyMedium:
		return Medium
	case bh > 0:
		return Wide
	}
	return NoOne
}

func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

func rbox(b vision.Box) vision.Box { return vision.Box{r3(b[0]), r3(b[1]), r3(b[2]), r3(b[3])} }

// textKey groups readings of the same text: case and digits do not matter (a clock).
func textKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsDigit(r):
			b.WriteRune('0')
		case unicode.IsSpace(r):
			if b.Len() > 0 && !strings.HasSuffix(b.String(), " ") {
				b.WriteByte(' ')
			}
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// DigitHeavy reports text that is mostly digits (a clock or a counter).
func DigitHeavy(s string) bool {
	n, l := alnum(s), letters(s)
	return n > 0 && float64(l) < 0.3*float64(n)
}

func letters(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// editDistance is the Levenshtein distance between two short strings.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func alnum(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

// span grows runs of times into spans (a gap of up to maxGap joins them).
type run struct {
	from, to float64
	n        int
	conf     float64
}

func runsOf(times []float64, confs []float64, maxGap float64) []run {
	var out []run
	for i, t := range times {
		if n := len(out); n > 0 && t-out[n-1].to <= maxGap {
			out[n-1].to, out[n-1].n = t, out[n-1].n+1
			out[n-1].conf = math.Max(out[n-1].conf, confs[i])
			continue
		}
		out = append(out, run{t, t, 1, confs[i]})
	}
	return out
}

// Build makes the index. step is the spacing of the regular samples; cuts are
// the clip's hard cuts (faces are not followed across them).
func Build(samples []Sample, step, duration float64, cuts []float64, source string) *Index {
	sort.SliceStable(samples, func(i, j int) bool { return samples[i].At < samples[j].At })
	idx := &Index{Version: Version, Source: source, Step: step, Frames: len(samples), Duration: r3(duration),
		Labels: []LabelSpan{}, Text: []TextSpan{}, Shots: []ShotSpan{}, Faces: []FaceSample{}, Tracks: []Track{}, Terms: []string{}}
	cuts = append([]float64(nil), cuts...)
	sort.Float64s(cuts)
	for _, c := range cuts {
		if c > 0 && c < duration {
			idx.Cuts = append(idx.Cuts, r3(c))
		}
	}
	if len(samples) == 0 {
		idx.Summary = "Nothing looked at yet."
		return idx
	}
	half := step / 2
	maxGap := 2.5 * step
	// Shot k runs from cut k-1 to cut k. A frame taken at a cut shows the new
	// shot (frames are taken at cuts on purpose), so "at" allows a hair before it.
	const atCut = 0.05
	shotOf := func(t float64) int { return sort.Search(len(cuts), func(i int) bool { return cuts[i] > t+atCut }) }
	// Something seen at t is on screen from half a step before to half a step
	// after, inside its shot (a cut is where it starts or stops being seen).
	clampF := func(t float64) float64 {
		f := math.Max(0, t-half)
		if k := shotOf(t); k > 0 {
			f = math.Max(f, cuts[k-1])
		}
		return r3(f)
	}
	clampT := func(t float64) float64 {
		e := math.Min(duration, t+half)
		if k := shotOf(t); k < len(cuts) {
			e = math.Min(e, cuts[k])
		}
		return r3(e)
	}

	// Labels.
	type acc struct {
		kind  string
		times []float64
		confs []float64
	}
	labels := map[string]*acc{}
	for _, s := range samples {
		if s.Frame.Error != "" {
			continue
		}
		for _, o := range seen(s.Frame) {
			a := labels[o.label]
			if a == nil {
				a = &acc{kind: o.kind}
				labels[o.label] = a
			}
			a.times, a.confs = append(a.times, s.At), append(a.confs, o.conf)
		}
	}
	for l, a := range labels {
		for _, r := range runsOf(a.times, a.confs, maxGap) {
			if r.n == 1 && r.conf < labelSolo {
				continue
			}
			idx.Labels = append(idx.Labels, LabelSpan{Label: l, Kind: a.kind, From: clampF(r.from), To: clampT(r.to), Conf: r3(r.conf)})
		}
	}
	sort.SliceStable(idx.Labels, func(i, j int) bool {
		if idx.Labels[i].From != idx.Labels[j].From {
			return idx.Labels[i].From < idx.Labels[j].From
		}
		return idx.Labels[i].Label < idx.Labels[j].Label
	})
	if len(idx.Labels) > maxLabelSpans {
		sort.SliceStable(idx.Labels, func(i, j int) bool { return idx.Labels[i].To-idx.Labels[i].From > idx.Labels[j].To-idx.Labels[j].From })
		idx.Labels = idx.Labels[:maxLabelSpans]
		sort.SliceStable(idx.Labels, func(i, j int) bool { return idx.Labels[i].From < idx.Labels[j].From })
	}

	// On-screen text. Readings of the same words join into spans; numbers that
	// keep changing in one place (a clock, a counter) are one span marked
	// Changing; a reading seen in one frame only counts when it is clearly words
	// and not a misreading of text around it.
	type tacc struct {
		times, confs []float64
		boxes        []vision.Box // where each reading was (a span is where its first reading was)
		variants     map[string]int
		first        string
		box, last    vision.Box
		digits       bool
	}
	var accs []*tacc
	byKey := map[string]*tacc{}
	for _, s := range samples {
		for _, t := range s.Frame.Text {
			str := strings.Join(strings.Fields(t.S), " ")
			if t.Conf < textMin || alnum(str) < 2 {
				continue
			}
			digits := DigitHeavy(str)
			var a *tacc
			if digits {
				for _, c := range accs { // the same place a moment ago: the same counter
					if c.digits && iou(c.last, t.Box) >= 0.3 && s.At-c.times[len(c.times)-1] <= maxGap {
						a = c
						break
					}
				}
			} else {
				a = byKey[textKey(str)]
			}
			if a == nil {
				a = &tacc{variants: map[string]int{}, first: str, box: rbox(t.Box), digits: digits}
				accs = append(accs, a)
				if !digits {
					byKey[textKey(str)] = a
				}
			}
			a.last = t.Box
			if n := len(a.times); n > 0 && a.times[n-1] == s.At {
				continue // the same text twice in one frame
			}
			a.times, a.confs, a.boxes = append(a.times, s.At), append(a.confs, t.Conf), append(a.boxes, rbox(t.Box))
			a.variants[str]++
		}
	}
	type pending struct {
		span TextSpan
		n    int
	}
	var spans []pending
	for _, a := range accs {
		at := 0
		for _, r := range runsOf(a.times, a.confs, maxGap) {
			// The same words shown again elsewhere (a call's name tile in
			// speaker view, then under a gallery tile) are where they are now.
			box := a.box
			if at < len(a.boxes) {
				box = a.boxes[at]
			}
			at += r.n
			spans = append(spans, pending{TextSpan{Text: a.first, From: clampF(r.from), To: clampT(r.to), Box: box, Conf: r3(r.conf), Changing: len(a.variants) > 1}, r.n})
		}
	}
	for _, p := range spans {
		if p.n == 1 {
			if DigitHeavy(p.span.Text) || letters(p.span.Text) < 3 || p.span.Conf < 0.6 {
				continue // a lone reading that is not clearly words
			}
			misread := false
			for _, q := range spans {
				if q.n > 1 && q.span.To >= p.span.From && q.span.From <= p.span.To && iou(q.span.Box, p.span.Box) >= 0.3 &&
					editDistance(strings.ToLower(q.span.Text), strings.ToLower(p.span.Text)) <= max(1, len([]rune(p.span.Text))/4) {
					misread = true
					break
				}
			}
			if misread {
				continue
			}
		}
		idx.Text = append(idx.Text, p.span)
	}
	sort.SliceStable(idx.Text, func(i, j int) bool { return idx.Text[i].From < idx.Text[j].From })
	if len(idx.Text) > maxTextSpans {
		idx.Text = idx.Text[:maxTextSpans]
	}

	// Faces, followed within each shot.
	type live struct {
		id    int
		box   vision.Box
		at    float64
		shot  int
		print []float32 // the last fingerprint seen, when there was one
	}
	// Fingerprints, where faces have them, keep two people apart: a face that
	// clearly isn't the person a track has followed starts a track of its own.
	differs := func(l *live, p []float32, need float64) bool {
		return len(p) > 0 && len(l.print) > 0 && faceid.Cosine(l.print, p) < need
	}
	var active []*live
	tracks := map[int]*Track{}
	sums := map[int]*[4]float64{}
	next := 1
	for _, s := range samples {
		fs, fk := faceIndices(s.Frame)
		if len(fs) == 0 {
			continue
		}
		fsm := FaceSample{At: r3(s.At), Boxes: []vision.Box{}, Tracks: []int{}}
		used := map[int]bool{}
		for fi, fc := range fs {
			var fp []float32
			if k := fk[fi]; k < len(s.Prints) {
				fp = s.Prints[k]
			}
			var best *live
			bestScore := 0.0
			for _, l := range active {
				if used[l.id] || l.shot != shotOf(s.At) || s.At-l.at > maxGap || differs(l, fp, printOther) {
					continue
				}
				sc := iou(l.box, fc.Box)
				if sc < trackIoU && centerDist(l.box, fc.Box) <= trackCenter {
					sc = trackIoU
				}
				if sc >= trackIoU && sc > bestScore {
					best, bestScore = l, sc
				}
			}
			if best == nil && len(fs) == 1 {
				// One face now and one face a moment ago in the same shot, about the
				// same size: the same person moving (walking in, turning).
				var only *live
				n := 0
				for _, l := range active {
					if !used[l.id] && l.shot == shotOf(s.At) && s.At-l.at <= maxGap {
						only, n = l, n+1
					}
				}
				if n == 1 && !differs(only, fp, printSame) {
					if r := fc.Box[3] / math.Max(1e-6, only.box[3]); r >= 0.6 && r <= 1.6 {
						best = only
					}
				}
			}
			if best == nil {
				best = &live{id: next, shot: shotOf(s.At)}
				next++
				active = append(active, best)
				tracks[best.id] = &Track{ID: best.id, From: s.At}
				sums[best.id] = &[4]float64{}
			}
			used[best.id] = true
			best.box, best.at = fc.Box, s.At
			if len(fp) > 0 {
				best.print = fp
			}
			tr := tracks[best.id]
			tr.To, tr.Samples = s.At, tr.Samples+1
			for k := 0; k < 4; k++ {
				sums[best.id][k] += fc.Box[k]
			}
			q := 0.0
			if fc.Quality != nil {
				q = *fc.Quality
			}
			// The best picture: capture quality, then size.
			if tr.Samples == 1 || q > tr.Best.Quality || (q == tr.Best.Quality && fc.Box[3] > tr.Best.Box[3]) {
				tr.Best = BestFace{At: r3(s.At), Box: rbox(fc.Box), Quality: r3(q)}
			}
			if k := fk[fi]; k < len(s.Prints) && len(s.Prints[k]) > 0 {
				// Usable: capture quality, and size up to a close-up.
				tr.Prints = keepPrint(tr.Prints, TrackPrint{At: r3(s.At), Quality: r3(q + 0.25*math.Min(1, fc.Box[3]/closeUpFace)), Vec: s.Prints[k]})
			}
			fsm.Boxes = append(fsm.Boxes, rbox(fc.Box))
			fsm.Tracks = append(fsm.Tracks, best.id)
		}
		idx.Faces = append(idx.Faces, fsm)
	}
	for id := 1; id < next; id++ {
		tr := tracks[id]
		n := float64(tr.Samples)
		tr.Box = rbox(vision.Box{sums[id][0] / n, sums[id][1] / n, sums[id][2] / n, sums[id][3] / n})
		tr.From, tr.To = clampF(tr.From), clampT(tr.To)
		idx.Tracks = append(idx.Tracks, *tr)
	}

	// Shot types: a lone frame that disagrees with both neighbors takes theirs.
	types := make([]string, len(samples))
	counts := make([]int, len(samples))
	nfaces := make([]int, len(samples))
	for i, s := range samples {
		types[i], counts[i], nfaces[i] = ShotType(s.Frame), people(s.Frame), len(faces(s.Frame))
	}
	for i := 1; i+1 < len(types); i++ {
		if types[i-1] == types[i+1] && types[i] != types[i-1] && shotOf(samples[i-1].At) == shotOf(samples[i+1].At) {
			types[i] = types[i-1]
		}
	}
	for i, s := range samples {
		if n := len(idx.Shots); n > 0 && idx.Shots[n-1].Type == types[i] && shotOf(s.At) == shotOf(samples[i-1].At) && s.At-samples[i-1].At <= maxGap {
			sp := &idx.Shots[n-1]
			sp.To, sp.Faces, sp.People = clampT(s.At), max(sp.Faces, nfaces[i]), max(sp.People, counts[i])
			continue
		}
		from := clampF(s.At)
		if i > 0 && shotOf(s.At) != shotOf(samples[i-1].At) {
			from = r3(cuts[shotOf(s.At)-1]) // a new shot starts at its cut
		}
		idx.Shots = append(idx.Shots, ShotSpan{Type: types[i], From: from, To: clampT(s.At), Faces: nfaces[i], People: counts[i]})
	}
	for i := 1; i < len(idx.Shots); i++ { // spans meet: no slivers between them
		if idx.Shots[i].From > idx.Shots[i-1].To {
			idx.Shots[i-1].To = idx.Shots[i].From
		}
	}

	idx.Attention = attentionOf(samples)
	idx.Gestures = gestureSpans(samples, maxGap, clampF, clampT, shotOf)

	idx.Terms = Terms(idx)
	idx.Summary = summary(idx)
	return idx
}

// faceIndices are a frame's faces above the confidence floor and their index
// in the frame.
func faceIndices(f vision.Frame) ([]vision.Face, []int) {
	var out []vision.Face
	var at []int
	for k, fc := range f.Faces {
		if fc.Conf >= faceMin {
			out = append(out, fc)
			at = append(at, k)
		}
	}
	return out, at
}

// keepPrint keeps a track's best fingerprints: the most usable frames, at
// most MaxTrackPrints.
func keepPrint(ps []TrackPrint, p TrackPrint) []TrackPrint {
	ps = append(ps, p)
	if len(ps) <= MaxTrackPrints {
		return ps
	}
	worst := 0
	for i := range ps {
		if ps[i].Quality < ps[worst].Quality {
			worst = i
		}
	}
	return append(ps[:worst], ps[worst+1:]...)
}

// attentionOf keeps where the eye is drawn in each frame that says.
func attentionOf(samples []Sample) []AttentionSample {
	var out []AttentionSample
	for _, s := range samples {
		a := s.Frame.Attention
		if a == nil || s.Frame.Error != "" {
			continue
		}
		as := AttentionSample{At: r3(s.At), X: r3(a.At[0]), Y: r3(a.At[1]), Strength: r3(math.Max(0, math.Min(1, a.Peak)))}
		if len(a.Objects) > 0 {
			b := rbox(a.Objects[0].Box)
			as.Box = &b
		}
		out = append(out, as)
	}
	return out
}

func iou(a, b vision.Box) float64 {
	x1, y1 := math.Max(a[0], b[0]), math.Max(a[1], b[1])
	x2, y2 := math.Min(a[0]+a[2], b[0]+b[2]), math.Min(a[1]+a[3], b[1]+b[3])
	inter := math.Max(0, x2-x1) * math.Max(0, y2-y1)
	u := a[2]*a[3] + b[2]*b[3] - inter
	if u <= 0 {
		return 0
	}
	return inter / u
}

func centerDist(a, b vision.Box) float64 {
	return math.Hypot(a[0]+a[2]/2-(b[0]+b[2]/2), a[1]+a[3]/2-(b[1]+b[3]/2))
}

// Words splits text into lowercase search words.
func Words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' })
}

// Terms are the index's search words: every label (and each of its words), the
// words of on-screen text, and "face"/"people" when faces show.
func Terms(idx *Index) []string {
	set := map[string]bool{}
	add := func(w string) {
		if len([]rune(w)) >= 2 {
			set[w] = true
		}
	}
	for _, l := range idx.Labels {
		add(l.Label)
		for _, w := range Words(l.Label) {
			add(w)
		}
		if l.Kind == KindAnimal {
			add("animal")
		}
		if l.Kind == KindScene && indoor(strings.ReplaceAll(l.Label, " ", "_")) {
			add("indoors")
		}
		if l.Label == "outdoor" {
			add("outdoors")
		}
	}
	for _, t := range idx.Text {
		if t.Changing && DigitHeavy(t.Text) {
			add("clock")
			add("counter")
			continue // its readings change all the time: not words to search for
		}
		for _, w := range Words(t.Text) {
			add(w)
		}
		add("text")
	}
	if len(idx.Faces) > 0 {
		add("face")
		add("person")
	}
	for _, g := range idx.Gestures {
		for _, w := range GestureWords(g) {
			add(w)
		}
	}
	out := make([]string, 0, len(set))
	for w := range set {
		out = append(out, w)
	}
	sort.Strings(out)
	if len(out) > maxTerms {
		out = out[:maxTerms]
	}
	return out
}

// covered is how much of [from, to) a label covers.
func covered(spans []LabelSpan, label string, from, to float64) float64 {
	c := 0.0
	for _, s := range spans {
		if s.Label == label {
			c += math.Max(0, math.Min(to, s.To)-math.Max(from, s.From))
		}
	}
	return c
}

// Notable are the labels that best describe [from, to): the setting
// (indoors/outdoors and a place) and up to n things or animals that are there
// for at least minShare of it.
func Notable(idx *Index, from, to float64, n int, minShare float64) (setting string, things []string) {
	span := math.Max(0.001, to-from)
	type cand struct {
		label string
		kind  string
		share float64
		conf  float64
	}
	best := map[string]*cand{}
	for _, l := range idx.Labels {
		if l.To <= from || l.From >= to {
			continue
		}
		c := best[l.Label]
		if c == nil {
			c = &cand{label: l.Label, kind: l.Kind}
			best[l.Label] = c
		}
		c.conf = math.Max(c.conf, l.Conf)
	}
	var list []*cand
	for _, c := range best {
		c.share = covered(idx.Labels, c.label, from, to) / span
		list = append(list, c)
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].share != list[j].share {
			return list[i].share > list[j].share
		}
		return list[i].conf > list[j].conf
	})
	in, out := 0.0, 0.0
	place := ""
	for _, c := range list {
		id := strings.ReplaceAll(c.label, " ", "_")
		switch {
		case c.label == "outdoor":
			out = math.Max(out, c.share)
		case indoor(id):
			in = math.Max(in, c.share)
		}
		if c.kind == KindScene && place == "" && c.share >= 0.4 && !generic[id] && !indoor(id) {
			place = c.label
		}
	}
	switch {
	case out >= 0.4 && out >= in:
		setting = "outdoors"
	case in >= 0.4:
		setting = "indoors"
	}
	if place != "" {
		if setting != "" {
			setting += ", " + place
		} else {
			setting = place
		}
	}
	for _, c := range list {
		id := strings.ReplaceAll(c.label, " ", "_")
		if len(things) >= n || c.share < minShare {
			break
		}
		if c.kind == KindScene || c.kind == KindPerson || generic[id] {
			continue
		}
		things = append(things, c.label)
	}
	return setting, things
}

func summary(idx *Index) string {
	var parts []string
	total := map[string]float64{}
	for _, s := range idx.Shots {
		total[s.Type] += s.To - s.From
	}
	most, mostT := "", 0.0
	for t, d := range total {
		if d > mostT {
			most, mostT = t, d
		}
	}
	maxPeople := 0
	for _, s := range idx.Shots {
		maxPeople = max(maxPeople, s.People)
	}
	switch {
	case maxPeople == 0:
		parts = append(parts, "no people")
	case most != "" && most != NoOne:
		parts = append(parts, fmt.Sprintf("%s, mostly %s", count(maxPeople, "person", "people"), shotWords[most]))
	default:
		parts = append(parts, count(maxPeople, "person", "people"))
	}
	setting, things := Notable(idx, 0, idx.Duration, 3, 0.1)
	if setting != "" {
		parts = append(parts, setting)
	}
	parts = append(parts, things...)
	var texts []string
	seenT := map[string]bool{}
	for _, t := range idx.Text {
		if !t.Changing && !seenT[t.Text] && len(texts) < 3 {
			seenT[t.Text] = true
			texts = append(texts, "“"+t.Text+"”")
		}
	}
	if len(texts) > 0 {
		parts = append(parts, "text "+strings.Join(texts, ", "))
	}
	return strings.Join(parts, " · ")
}

var shotWords = map[string]string{CloseUp: "close-ups", Medium: "medium shots", Wide: "wide shots", NoOne: "no people"}

func count(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
