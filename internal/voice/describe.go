package voice

import (
	"math"
	"sort"
)

// Describe fingerprints one voice from the stretches where it speaks (own),
// as Refine does for a diarized speaker: clean windows (edges trimmed, other
// voices' stretches removed), exemplars, centroid, listening samples and
// pitch. It is used when a person fixes who said what, so the voice is learned
// only from speech a person confirmed. With a nil embedder only the samples,
// times and pitch are filled in.
func Describe(label string, own, others []Span, pcm []float32, e Embedder, p Params) (Cluster, error) {
	if p.Window <= 0 {
		p = DefaultParams
	}
	c := Cluster{Label: label, First: -1}
	var ts []*turnInfo
	for _, s := range own {
		if s.To > s.From {
			ts = append(ts, &turnInfo{Turn: Turn{From: s.From, To: s.To, Label: label}})
		}
	}
	if len(ts) == 0 {
		c.First = 0
		return c, nil
	}
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].From < ts[j].From })
	c.First = round3(ts[0].From)
	var clean []Span
	for _, t := range ts {
		pieces := []Span{{t.From + p.Edge, t.To - p.Edge}}
		for _, o := range others {
			if o.To > t.From && o.From < t.To {
				pieces = subtract(pieces, o)
			}
		}
		for _, s := range pieces {
			if s.Len() >= p.MinEmbed {
				t.clean = append(t.clean, s)
				clean = append(clean, s)
			}
		}
		c.Turns++
		c.Seconds += t.To - t.From
	}
	c.Seconds = round3(c.Seconds)
	var wins []Exemplar
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
						return c, err
					}
					t.wins = append(t.wins, Exemplar{Span: w, Vec: v})
				}
			}
			if len(t.wins) > 0 {
				vs, ws := exemplarVecs(t.wins)
				t.vec = Mean(vs, ws)
			}
			wins = append(wins, t.wins...)
		}
	}
	if len(wins) > 0 {
		vs, ws := exemplarVecs(wins)
		c.Centroid = Mean(vs, ws)
		for _, w := range wins {
			c.Embedded += w.Len()
		}
		c.Embedded = round3(c.Embedded)
		c.Exemplars = pickExemplars(wins, c.Centroid, p.Exemplars)
	}
	c.Samples = pickSamples(ts, map[string]string{label: label}, &c, p)
	pt := MeasurePitch(pcm, clean)
	c.Pitch, c.Voiced = pt.Hz, pt.Voiced
	return c, nil
}

// EmbedSpan is the fingerprint of one stretch of speech (nil when it is too
// short or embeddings are unavailable).
func EmbedSpan(pcm []float32, s Span, e Embedder) []float32 {
	if e == nil {
		return nil
	}
	v, err := e.Embed(slice(pcm, s))
	if err != nil {
		return nil
	}
	return v
}
