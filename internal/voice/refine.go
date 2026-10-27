package voice

import (
	"fmt"
	"math"
	"sort"
)

// Embedder computes a unit-length speaker embedding of 16 kHz mono samples.
type Embedder interface {
	Embed(samples []float32) ([]float32, error)
}

// Turn is one diarized stretch of speech by one speaker label.
type Turn struct {
	From, To float64
	Label    string
}

// Exemplar is the embedding of one clean window of a speaker's speech.
type Exemplar struct {
	Span
	Vec []float32
}

// Cluster is one speaker as found in one recording, with its fingerprint.
type Cluster struct {
	Label string
	// Seconds is the total length of the speaker's turns; Embedded is how much
	// of it was clean enough (no overlap, ≥ MinEmbed) to fingerprint.
	Seconds, Embedded float64
	Turns             int
	First             float64
	Centroid          []float32
	Exemplars         []Exemplar
	// Minor marks a brief voice that matched nobody: less than Params.Thin of
	// fingerprinted speech and less than Params.MinorShare of everything said
	// in the recording. Possibly a real person who barely spoke, possibly
	// laughter or crosstalk. It is never auto-labeled or nagged about. (In a
	// short clip, someone with a few seconds is a main voice, not a brief one.)
	Minor bool
	// Samples are 2–3 short listenable snippets (the most typical clean,
	// non-overlapping speech of 2–5 s when there is any).
	Samples []Span
	// Pitch is the median pitch (Hz) of the voice's clean speech over Voiced
	// seconds of voiced frames (0 when unknown).
	Pitch, Voiced float64
}

// Params tune Refine. DefaultParams were measured on real speech (docs/speakers.md).
type Params struct {
	Edge       float64 // trimmed from each side of a turn before fingerprinting
	MinEmbed   float64 // shortest clean span that is fingerprinted
	Window     float64 // longest exemplar window
	MinCluster float64 // labels with less fingerprinted speech are folded into a confident speaker
	Thin       float64 // labels with less than this are "thin": merged when similar, else minor
	MinorShare float64 // ...unless they say at least this share of the recording's speech (short clips)
	Merge      float64 // confident labels at least this similar are one speaker
	ThinMerge  float64 // a thin label at least this similar to a confident one joins it
	Absorb     float64 // a short turn joins the most similar speaker if at least this similar (else the nearest in time)
	Exemplars  int     // exemplars kept per cluster
	Samples    int     // listenable samples per cluster
}

// DefaultParams are the tuned defaults.
var DefaultParams = Params{Edge: 0.15, MinEmbed: 1.0, Window: 3.0, MinCluster: 1.5, Thin: 4.0, MinorShare: 0.15, Merge: 0.55, ThinMerge: 0.45, Absorb: 0.30, Exemplars: 12, Samples: 3}

// Result is refined diarization: relabeled turns (speaker_00… by first
// appearance) and one fingerprinted cluster per remaining speaker.
type Result struct {
	Turns    []Turn
	Clusters []Cluster
	// Raw is the number of labels the diarizer produced; Merged counts labels
	// merged into another, Absorbed the short turns reassigned.
	Raw, Merged, Absorbed int
	Notes                 []string
}

// Speakers counts the clusters that are not minor.
func (r *Result) Speakers() int {
	n := 0
	for _, c := range r.Clusters {
		if !c.Minor {
			n++
		}
	}
	return n
}

type turnInfo struct {
	Turn
	clean []Span
	wins  []Exemplar
	vec   []float32 // mean of wins, or a whole-turn embedding of a short turn
}

type labelAgg struct {
	embedded float64
	wins     []Exemplar
	centroid []float32
}

func (a *labelAgg) recompute() {
	vs, ws := exemplarVecs(a.wins)
	a.centroid = Mean(vs, ws)
}

// Refine fingerprints every speaker label and repairs common diarization
// mistakes: one person split into several labels is merged back (matching
// fingerprints), and labels with too little clean speech — short interjections
// the clusterer split off — are folded into the most similar (or, when their
// voice is unclear, the neighbouring) confident speaker. A short line that
// clearly sounds like a known person (not the speaker it would be folded into)
// is kept as its own voice. With a nil embedder it only relabels and picks
// samples.
func Refine(turns []Turn, pcm []float32, e Embedder, p Params, known ...Profile) (*Result, error) {
	if p.Window <= 0 {
		p = DefaultParams
	}
	ts := make([]*turnInfo, 0, len(turns))
	for _, t := range turns {
		if t.To > t.From {
			ts = append(ts, &turnInfo{Turn: t})
		}
	}
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].From < ts[j].From })
	res := &Result{}
	aggs := map[string]*labelAgg{}
	for _, t := range ts {
		if aggs[t.Label] == nil {
			aggs[t.Label] = &labelAgg{}
		}
	}
	res.Raw = len(aggs)
	if len(ts) == 0 {
		return res, nil
	}
	// 1. Clean spans: trimmed edges, minus every other speaker's turns.
	for i, t := range ts {
		pieces := []Span{{t.From + p.Edge, t.To - p.Edge}}
		for j, o := range ts {
			if j != i && o.Label != t.Label && o.To > t.From && o.From < t.To {
				pieces = subtract(pieces, Span{o.From, o.To})
			}
		}
		for _, s := range pieces {
			if s.Len() >= p.MinEmbed {
				t.clean = append(t.clean, s)
			}
		}
	}
	// 2. Exemplar windows (≤ Window s) and per-turn embeddings.
	if e != nil {
		for _, t := range ts {
			for _, s := range t.clean {
				n := int(math.Ceil(s.Len()/p.Window - 1e-9))
				step := s.Len() / float64(n)
				for k := 0; k < n; k++ {
					w := Span{round3(s.From + float64(k)*step), round3(s.From + float64(k+1)*step)}
					v, err := e.Embed(slice(pcm, w))
					if err == ErrTooShort {
						continue
					}
					if err != nil {
						return nil, err
					}
					t.wins = append(t.wins, Exemplar{Span: w, Vec: v})
				}
			}
			if len(t.wins) > 0 {
				vs, ws := exemplarVecs(t.wins)
				t.vec = Mean(vs, ws)
			} else if t.To-t.From >= 0.5 {
				if v, err := e.Embed(slice(pcm, Span{t.From, t.To})); err == nil {
					t.vec = v
				}
			}
			a := aggs[t.Label]
			for _, w := range t.wins {
				a.embedded += w.Len()
				a.wins = append(a.wins, w)
			}
		}
		for _, a := range aggs {
			a.recompute()
		}
	}
	// 3. Tiers by fingerprinted speech.
	tier := func(l string) int { // 0 weak, 1 thin, 2 strong
		switch a := aggs[l]; {
		case a.embedded >= p.Thin:
			return 2
		case a.embedded >= p.MinCluster:
			return 1
		}
		return 0
	}
	alias := map[string]string{}
	root := func(l string) string {
		for alias[l] != "" {
			l = alias[l]
		}
		return l
	}
	live := func(minTier int) []string {
		var ls []string
		for l := range aggs {
			if root(l) == l && tier(l) >= minTier {
				ls = append(ls, l)
			}
		}
		sort.Strings(ls)
		return ls
	}
	merge := func(drop, keep string, sim float64, why string) {
		alias[drop] = keep
		aggs[keep].wins = append(aggs[keep].wins, aggs[drop].wins...)
		aggs[keep].embedded += aggs[drop].embedded
		aggs[keep].recompute()
		res.Merged++
		res.Notes = append(res.Notes, fmt.Sprintf("merged %s into %s (%s, fingerprints %.0f%% similar)", drop, keep, why, 100*sim))
	}
	if e != nil {
		// 3a. Confident labels that sound the same are one person.
		for {
			ls := live(2)
			bi, bj, bs := "", "", -2.0
			for i := range ls {
				for j := i + 1; j < len(ls); j++ {
					if s := Cosine(aggs[ls[i]].centroid, aggs[ls[j]].centroid); s > bs {
						bi, bj, bs = ls[i], ls[j], s
					}
				}
			}
			if bi == "" || bs < p.Merge {
				break
			}
			if aggs[bj].embedded > aggs[bi].embedded {
				bi, bj = bj, bi
			}
			merge(bj, bi, bs, "same voice")
		}
		// 3b. Thin labels join a confident speaker they clearly sound like.
		strong := live(2)
		for _, l := range live(1) {
			if tier(l) != 1 || len(strong) == 0 {
				continue
			}
			best, second, bl := bestOf(aggs[l].centroid, strong, aggs)
			if best >= p.ThinMerge && best-second >= 0.05 {
				merge(l, bl, best, "brief voice")
			}
		}
	}
	// 4. Fold the turns of weak labels into a confident speaker.
	anchors := live(2)
	if len(anchors) == 0 {
		anchors = live(1)
	}
	if e == nil { // no fingerprints: trust the diarizer's labels as they are
		anchors = live(0)
	}
	if len(anchors) == 0 { // nothing fingerprinted: keep the label with the most speech
		most, ms := "", -1.0
		for l := range aggs {
			s := 0.0
			for _, t := range ts {
				if t.Label == l {
					s += t.To - t.From
				}
			}
			if s > ms || (s == ms && l < most) {
				most, ms = l, s
			}
		}
		anchors = []string{most}
	}
	isAnchor := map[string]bool{}
	for _, l := range anchors {
		isAnchor[l] = true
	}
	weakLabels := map[string]bool{}
	for i, t := range ts {
		l := root(t.Label)
		if isAnchor[l] || (tier(l) >= 1 && e != nil) {
			continue
		}
		target := ""
		if t.vec != nil {
			best, second, bl := bestOf(t.vec, anchors, aggs)
			if best >= p.Absorb && (len(anchors) == 1 || best-second >= 0.05) {
				target = bl
			}
			// A short line that sounds like a known person who is not the
			// best anchor stays a separate voice (matching names it later).
			if k := bestKnown(t.vec, known); target != "" && k >= p.ThinMerge && k-best >= 0.15 {
				target = ""
			}
		}
		if target == "" && (t.vec == nil || t.To-t.From < 1.0) {
			// Too short to tell by voice: it belongs to whoever spoke next to it.
			target = nearestOf(ts, i, isAnchor, root)
		}
		if target != "" && target != l {
			weakLabels[l] = true
			t.Label = target
			res.Absorbed++
		}
	}
	if res.Absorbed > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("folded %d short turn(s) from %d low-evidence label(s) into confident speakers", res.Absorbed, len(weakLabels)))
	}
	for _, t := range ts {
		t.Label = root(t.Label)
	}
	// 5. Relabel by first appearance (real speakers first, brief voices after,
	// so the people in a video are Speaker 1…N); build clusters. A voice is
	// brief only when it is also a small part of the recording: in a 10-second
	// clip, 3 seconds is one of the people in it.
	total, said := 0.0, map[string]float64{}
	for _, t := range ts {
		total += t.To - t.From
		said[t.Label] += t.To - t.From
	}
	minorOf := func(l string) bool {
		return e != nil && !isAnchor[l] && aggs[l].embedded < p.Thin && (total <= 0 || said[l]/total < p.MinorShare)
	}
	order := map[string]string{}
	for _, pass := range []bool{false, true} {
		for _, t := range ts {
			if _, ok := order[t.Label]; !ok && minorOf(t.Label) == pass {
				order[t.Label] = fmt.Sprintf("speaker_%02d", len(order))
			}
		}
	}
	byLabel := map[string]*Cluster{}
	var clusters []*Cluster
	for _, t := range ts {
		nl := order[t.Label]
		c := byLabel[nl]
		if c == nil {
			c = &Cluster{Label: nl, First: round3(t.From)}
			byLabel[nl] = c
			clusters = append(clusters, c)
		}
		c.Turns++
		c.Seconds += t.To - t.From
		res.Turns = append(res.Turns, Turn{From: t.From, To: t.To, Label: nl})
	}
	for old, nl := range order {
		c, a := byLabel[nl], aggs[old]
		if len(a.wins) > 0 {
			c.Centroid = a.centroid
			c.Embedded = round3(a.embedded)
			c.Exemplars = pickExemplars(a.wins, a.centroid, p.Exemplars)
		}
		c.Minor = minorOf(old)
	}
	sort.SliceStable(clusters, func(i, j int) bool { return clusters[i].Label < clusters[j].Label })
	for _, c := range clusters {
		c.Seconds = round3(c.Seconds)
		c.Samples = pickSamples(ts, order, c, p)
		var clean []Span
		for _, t := range ts {
			if order[t.Label] == c.Label {
				clean = append(clean, t.clean...)
			}
		}
		pt := MeasurePitch(pcm, clean)
		c.Pitch, c.Voiced = pt.Hz, pt.Voiced
		res.Clusters = append(res.Clusters, *c)
	}
	return res, nil
}

// bestKnown is the highest similarity of v to any known person's exemplars.
func bestKnown(v []float32, known []Profile) float64 {
	best := -2.0
	for _, k := range known {
		if s := Score(Cluster{Centroid: v}, k).Score; s > best {
			best = s
		}
	}
	return best
}

func bestOf(v []float32, labels []string, aggs map[string]*labelAgg) (best, second float64, bl string) {
	best, second = -2, -2
	for _, l := range labels {
		s := Cosine(v, aggs[l].centroid)
		if s > best {
			best, second, bl = s, best, l
		} else if s > second {
			second = s
		}
	}
	return
}

func exemplarVecs(ws []Exemplar) ([][]float32, []float64) {
	vs := make([][]float32, len(ws))
	wt := make([]float64, len(ws))
	for i, w := range ws {
		vs[i], wt[i] = w.Vec, w.Len()
	}
	return vs, wt
}

// subtract removes cut from every span.
func subtract(spans []Span, cut Span) []Span {
	var out []Span
	for _, s := range spans {
		if cut.To <= s.From || cut.From >= s.To {
			out = append(out, s)
			continue
		}
		if cut.From > s.From {
			out = append(out, Span{s.From, cut.From})
		}
		if cut.To < s.To {
			out = append(out, Span{cut.To, s.To})
		}
	}
	return out
}

// nearestOf is the anchor speaker of the turn closest in time to ts[i].
func nearestOf(ts []*turnInfo, i int, anchor map[string]bool, root func(string) string) string {
	best, bd := "", math.MaxFloat64
	t := ts[i]
	for j, o := range ts {
		if j == i || !anchor[root(o.Label)] {
			continue
		}
		d := math.Max(0, math.Max(o.From-t.To, t.From-o.To))
		if d < bd {
			best, bd = root(o.Label), d
		}
	}
	return best
}

// pickExemplars keeps the n windows most typical of the speaker, longest first on ties.
func pickExemplars(ws []Exemplar, centroid []float32, n int) []Exemplar {
	cp := append([]Exemplar(nil), ws...)
	sort.SliceStable(cp, func(i, j int) bool {
		si, sj := Cosine(cp[i].Vec, centroid), Cosine(cp[j].Vec, centroid)
		if math.Abs(si-sj) > 1e-9 {
			return si > sj
		}
		return cp[i].Len() > cp[j].Len()
	})
	if n > 0 && len(cp) > n {
		cp = cp[:n]
	}
	return cp
}

// pickSamples chooses up to p.Samples listenable snippets for a cluster:
// clean, non-overlapping speech of 2–5 s, the most typical first, spread
// across the recording, returned in time order.
func pickSamples(ts []*turnInfo, order map[string]string, c *Cluster, p Params) []Span {
	type cand struct {
		s     Span
		score float64
	}
	var cands []cand
	add := func(s Span, sim float64) {
		w := math.Sqrt(math.Min(s.Len(), 4))
		if s.Len() < 2 {
			w *= 0.5 // usable, but only when nothing longer exists
		}
		cands = append(cands, cand{Span{round3(s.From), round3(s.To)}, sim * w})
	}
	for _, t := range ts {
		if order[t.Label] != c.Label {
			continue
		}
		for _, cl := range t.clean {
			// a little air at the turn's own edges, never into another speaker's turn
			s := cl
			if math.Abs(cl.From-(t.From+p.Edge)) < 1e-6 {
				s.From = math.Max(t.From, cl.From-p.Edge*0.5)
			}
			if math.Abs(cl.To-(t.To-p.Edge)) < 1e-6 {
				s.To = math.Min(t.To, cl.To+p.Edge*0.5)
			}
			if s.Len() < 1.2 {
				continue
			}
			var inside []Exemplar
			for _, w := range t.wins {
				if w.From >= cl.From-1e-6 && w.To <= cl.To+1e-6 {
					inside = append(inside, w)
				}
			}
			if s.Len() <= 5 || len(inside) == 0 {
				sim := 0.5
				if t.vec != nil && c.Centroid != nil {
					sim = Cosine(t.vec, c.Centroid)
				}
				if s.Len() > 5 {
					s = Span{s.From + (s.Len()-4)/2, s.From + (s.Len()+4)/2}
				}
				add(s, sim)
				continue
			}
			for _, w := range inside { // long turn: 4 s around each typical window
				mid := (w.From + w.To) / 2
				from := math.Max(s.From, math.Min(mid-2, s.To-4))
				add(Span{from, from + 4}, Cosine(w.Vec, c.Centroid))
			}
		}
	}
	sort.SliceStable(cands, func(i, j int) bool { return cands[i].score > cands[j].score })
	var out []Span
	for _, cd := range cands {
		if len(out) >= p.Samples {
			break
		}
		ok := true
		for _, o := range out {
			if cd.s.From < o.To+1 && o.From < cd.s.To+1 { // overlapping or nearly adjacent
				ok = false
			}
		}
		if ok {
			out = append(out, cd.s)
		}
	}
	if len(out) == 0 { // no clean stretch: the longest turn (≤ 5 s), however short
		var best *turnInfo
		for _, t := range ts {
			if order[t.Label] == c.Label && (best == nil || t.To-t.From > best.To-best.From) {
				best = t
			}
		}
		if best != nil {
			out = append(out, Span{round3(best.From), round3(math.Min(best.To, best.From+5))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}
