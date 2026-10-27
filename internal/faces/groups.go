package faces

import (
	"math"
	"sort"

	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/visual"
)

// One question per person, not per appearance (D155). A call's speaker view
// starts a new face track at every switch between speakers, so asking once per
// track asked about one person dozens of times. The unnamed faces of a clip are
// grouped first — by how they look, and by who is speaking while they are on
// screen — and each group is asked about once; one answer names the group.

// Grouping thresholds (cosine similarity of face fingerprints; measured in
// docs/visual-index.md).
const (
	// GroupLink: clusters of a clip's faces join while their average
	// similarity is at least this. One person within one recording scores
	// 0.6–0.9; different people never passed 0.49 on the faceset.
	GroupLink = 0.45
	// VoiceLink: two clusters on screen alone with the same voice join when
	// they look at least this alike (the same person, another angle or light).
	VoiceLink = 0.30
	// GroupFit: when a group is named, a face fits when it is at least this
	// alike to the rest of the group; the others are set aside.
	GroupFit = 0.40
	// PlaceLink: a face without a fingerprint joins a group of its voice when
	// it overlaps that group's place in the picture at least this much.
	PlaceLink = 0.30
	// MinFaceSeconds: a face seen for less than this is never asked about on
	// its own (it is named with its group).
	MinFaceSeconds = 1.0
	// MaxSamples: sample faces a question shows.
	MaxSamples = 6
	// MaxGroupFaces: a group answer teaches what someone looks like through at
	// most this many of its clearest faces.
	MaxGroupFaces = 6
	// SameClipUnlike: a group that looks this unlike the faces a person
	// confirmed as the voice's person in the same clip is someone else.
	SameClipUnlike = 0.30
)

// voiceTie is how long one face was alone on screen while each voice talked.
type voiceTie struct {
	by    map[string]float64 // voice label → seconds
	total float64            // seconds alone on screen while anyone talked
}

func (t *voiceTie) add(o *voiceTie) {
	if o == nil {
		return
	}
	for l, s := range o.by {
		t.by[l] += s
	}
	t.total += o.total
}

// strongest is the voice heard most while the face was alone on screen, when
// it is enough to tie them (MinSeconds, MinShare: the voice cue); "" otherwise.
func (t *voiceTie) strongest() (string, float64, float64) { return t.strongestAt(MinSeconds) }

// strongestAt is strongest with another bar for the seconds (SpeaksSeconds:
// someone speaks while the face is alone on screen).
func (t *voiceTie) strongestAt(minSeconds float64) (string, float64, float64) {
	if t == nil || t.total <= 0 {
		return "", 0, 0
	}
	labels := make([]string, 0, len(t.by))
	for l := range t.by {
		labels = append(labels, l)
	}
	sort.Strings(labels)
	best, sec := "", 0.0
	for _, l := range labels {
		if t.by[l] > sec {
			best, sec = l, t.by[l]
		}
	}
	share := sec / t.total
	if sec < minSeconds || share < MinShare {
		return "", sec, share
	}
	return best, sec, share
}

// aloneVoices measures, for every face track, the voices heard while it was
// the only face on screen (samples of the index, one step each).
func aloneVoices(idx *visual.Index, segs []models.TranscriptSegment) map[int]*voiceTie {
	out := map[int]*voiceTie{}
	if idx == nil {
		return out
	}
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].From < segs[j].From })
	speakerAt := func(t float64) string {
		i := sort.Search(len(segs), func(i int) bool { return segs[i].To >= t })
		if i < len(segs) && segs[i].From <= t {
			return segs[i].Speaker
		}
		return ""
	}
	step := idx.Step
	if step <= 0 {
		step = 1
	}
	for _, fs := range idx.Faces {
		if len(fs.Tracks) != 1 { // only a face alone on screen tells whose voice goes with it
			continue
		}
		label := speakerAt(fs.At)
		if label == "" {
			continue
		}
		t := out[fs.Tracks[0]]
		if t == nil {
			t = &voiceTie{by: map[string]float64{}}
			out[fs.Tracks[0]] = t
		}
		t.by[label] += step
		t.total += step
	}
	return out
}

// item is one unnamed face as the grouping sees it.
type item struct {
	vec   []float32 // nil without fingerprints
	q     float64   // how usable its best picture is
	group string    // the group it was in ("" new)
	tie   *voiceTie
	box   []float64
}

// cluster is a group being formed.
type cluster struct {
	items []int
	group string // an existing group's ID it keeps
	fixed bool   // an existing group (two existing groups never join by themselves)
	tie   *voiceTie
	voice string // the voice it is on screen alone with ("" none)
	alive bool
}

// groupItems groups a clip's unnamed faces (D155): existing groups stay (new
// faces may join them), faces with fingerprints join while clusters look alike
// on average (GroupLink), clusters on screen with the same voice join when they
// look somewhat alike (VoiceLink), and faces without fingerprints join the
// group of their voice and place. It returns each face's group (by index); new
// groups get IDs from newID.
func groupItems(items []item, newID func() string) []string {
	var cs []*cluster
	byGroup := map[string]*cluster{}
	var loose []int // new faces without fingerprints
	for i, it := range items {
		switch {
		case it.group != "":
			c := byGroup[it.group]
			if c == nil {
				c = &cluster{group: it.group, fixed: true, alive: true}
				byGroup[it.group] = c
				cs = append(cs, c)
			}
			c.items = append(c.items, i)
		case it.vec != nil:
			cs = append(cs, &cluster{items: []int{i}, alive: true})
		default:
			loose = append(loose, i)
		}
	}
	retie := func(c *cluster) {
		c.tie = &voiceTie{by: map[string]float64{}}
		for _, i := range c.items {
			c.tie.add(items[i].tie)
		}
		c.voice, _, _ = c.tie.strongest()
	}
	for _, c := range cs {
		retie(c)
	}
	// Average linkage (sums of pairwise similarities between clusters, kept up
	// to date as clusters join: Lance–Williams for the mean).
	m := len(cs)
	sum := make([][]float64, m)
	cnt := make([][]float64, m)
	for a := range cs {
		sum[a], cnt[a] = make([]float64, m), make([]float64, m)
	}
	for a := 0; a < m; a++ {
		for b := a + 1; b < m; b++ {
			s, n := 0.0, 0.0
			for _, i := range cs[a].items {
				if items[i].vec == nil {
					continue
				}
				for _, j := range cs[b].items {
					if items[j].vec != nil {
						s += faceid.Cosine(items[i].vec, items[j].vec)
						n++
					}
				}
			}
			sum[a][b], sum[b][a], cnt[a][b], cnt[b][a] = s, s, n, n
		}
	}
	merge := func(link float64, sameVoice bool) {
		for {
			ba, bb, best := -1, -1, link
			for a := 0; a < m; a++ {
				if !cs[a].alive {
					continue
				}
				for b := a + 1; b < m; b++ {
					if !cs[b].alive || (cs[a].fixed && cs[b].fixed) || cnt[a][b] == 0 {
						continue
					}
					if sameVoice && (cs[a].voice == "" || cs[a].voice != cs[b].voice) {
						continue
					}
					if s := sum[a][b] / cnt[a][b]; s >= best {
						ba, bb, best = a, b, s
					}
				}
			}
			if ba < 0 {
				return
			}
			keep, gone := ba, bb
			if cs[bb].fixed { // the existing group keeps its ID
				keep, gone = bb, ba
			}
			cs[keep].items = append(cs[keep].items, cs[gone].items...)
			cs[keep].fixed = cs[keep].fixed || cs[gone].fixed
			cs[gone].alive = false
			retie(cs[keep])
			for c := 0; c < m; c++ {
				if c == keep || c == gone {
					continue
				}
				sum[keep][c] += sum[gone][c]
				cnt[keep][c] += cnt[gone][c]
				sum[c][keep], cnt[c][keep] = sum[keep][c], cnt[keep][c]
			}
		}
	}
	merge(GroupLink, false)
	merge(VoiceLink, true)
	var alive []*cluster
	for _, c := range cs {
		if c.alive {
			alive = append(alive, c)
		}
	}
	cs = alive
	// Faces without fingerprints: the group of their voice whose place they share.
	place := func(c *cluster, box []float64) float64 {
		best := 0.0
		for _, i := range c.items {
			best = math.Max(best, boxIoU(items[i].box, box))
		}
		return best
	}
	for _, i := range loose {
		l, _, _ := items[i].tie.strongest()
		var to *cluster
		bestSec := 0.0
		if l != "" {
			for _, c := range cs {
				if c.voice != l || place(c, items[i].box) < PlaceLink {
					continue
				}
				if sec := c.tie.by[l]; to == nil || sec > bestSec {
					to, bestSec = c, sec
				}
			}
		}
		if to == nil {
			to = &cluster{alive: true}
			cs = append(cs, to)
		}
		to.items = append(to.items, i)
		retie(to)
	}
	out := make([]string, len(items))
	for _, c := range cs {
		id := c.group
		if id == "" {
			id = newID()
		}
		for _, i := range c.items {
			out[i] = id
		}
	}
	return out
}

// centroid is the quality-weighted mean fingerprint of the faces that have one
// (nil when none does).
func centroid(vecs [][]float32, qs []float64) []float32 {
	var vs [][]float32
	var ws []float64
	for i, v := range vecs {
		if v == nil {
			continue
		}
		vs = append(vs, v)
		w := 1.0
		if i < len(qs) && qs[i] > 0 {
			w = qs[i]
		}
		ws = append(ws, w)
	}
	if len(vs) == 0 {
		return nil
	}
	return faceid.Mean(vs, ws)
}

// fits says, for each face of a group, whether it looks enough like the rest
// to be named with them (GroupFit, against the mean of the others). A face
// without a fingerprint fits: it was grouped by its voice and place.
func fits(vecs [][]float32, qs []float64) []bool {
	out := make([]bool, len(vecs))
	n := 0
	for _, v := range vecs {
		if v != nil {
			n++
		}
	}
	for i, v := range vecs {
		if v == nil || n < 2 {
			out[i] = true
			continue
		}
		rest := make([][]float32, 0, len(vecs)-1)
		ws := make([]float64, 0, len(vecs)-1)
		for j, w := range vecs {
			if j == i || w == nil {
				continue
			}
			rest = append(rest, w)
			ws = append(ws, math.Max(qs[j], 0.01))
		}
		out[i] = faceid.Cosine(v, faceid.Mean(rest, ws)) >= GroupFit
	}
	return out
}

// samplesOf picks up to MaxSamples faces to show for a group: the clearest
// first, then spread over the clip (faces seen for at least a second first).
func samplesOf(fs []models.FaceTrack) []models.FaceTrack {
	if len(fs) == 0 {
		return nil
	}
	cands := make([]models.FaceTrack, 0, len(fs))
	for _, f := range fs {
		if f.To-f.From >= MinFaceSeconds && len(f.Box) == 4 {
			cands = append(cands, f)
		}
	}
	if len(cands) == 0 {
		for _, f := range fs {
			if len(f.Box) == 4 {
				cands = append(cands, f)
			}
		}
	}
	if len(cands) <= MaxSamples {
		out := append([]models.FaceTrack(nil), cands...)
		sort.SliceStable(out, func(i, j int) bool { return out[i].Quality > out[j].Quality })
		return out
	}
	// The clearest, then the clearest of each stretch of the clip.
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].From < cands[j].From })
	best := 0
	for i, f := range cands {
		if f.Quality > cands[best].Quality {
			best = i
		}
	}
	out := []models.FaceTrack{cands[best]}
	buckets := MaxSamples - 1
	per := float64(len(cands)) / float64(buckets)
	for b := 0; b < buckets; b++ {
		lo, hi := int(math.Round(float64(b)*per)), int(math.Round(float64(b+1)*per))
		pick := -1
		for i := lo; i < hi && i < len(cands); i++ {
			if i == best {
				continue
			}
			if pick < 0 || cands[i].Quality > cands[pick].Quality {
				pick = i
			}
		}
		if pick >= 0 {
			out = append(out, cands[pick])
		}
	}
	return out
}
