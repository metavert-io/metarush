package voice

import (
	"fmt"
	"math"
	"sort"
)

// Profile is a named person's voice: several exemplars from every recording
// in which someone confirmed who was speaking, plus their mean. Keeping
// exemplars (not only an average) lets a new recording match the closest
// conditions the person was heard in (another microphone, another room).
type Profile struct {
	ID, Name  string
	Centroid  []float32
	Exemplars []ProfileExemplar
}

// ProfileExemplar is one enrolled embedding and where it came from.
type ProfileExemplar struct {
	Source  string    `bson:"source" json:"source"` // <assetId>/<label>
	Vec     []float32 `bson:"v" json:"-"`
	Seconds float64   `bson:"s" json:"seconds"`
}

// Enrollment caps: per recording and in total (oldest recordings drop first).
const (
	MaxExemplarsPerSource = 6
	MaxExemplars          = 60
)

// Enroll adds a cluster's exemplars to the profile under source (replacing
// what that source contributed before) and recomputes the centroid.
func (p Profile) Enroll(c Cluster, source ...string) Profile {
	src := c.Label
	if len(source) > 0 && source[0] != "" {
		src = source[0]
	}
	out := p
	out.Exemplars = nil
	for _, e := range p.Exemplars {
		if e.Source != src {
			out.Exemplars = append(out.Exemplars, e)
		}
	}
	for i, e := range c.Exemplars {
		if i >= MaxExemplarsPerSource {
			break
		}
		out.Exemplars = append(out.Exemplars, ProfileExemplar{Source: src, Vec: e.Vec, Seconds: round3(e.Len())})
	}
	if len(out.Exemplars) > MaxExemplars {
		out.Exemplars = out.Exemplars[len(out.Exemplars)-MaxExemplars:]
	}
	out.Centroid = profileCentroid(out.Exemplars)
	return out
}

// Forget removes what source contributed.
func (p Profile) Forget(source string) Profile {
	out := p
	out.Exemplars = nil
	for _, e := range p.Exemplars {
		if e.Source != source {
			out.Exemplars = append(out.Exemplars, e)
		}
	}
	out.Centroid = profileCentroid(out.Exemplars)
	return out
}

func profileCentroid(es []ProfileExemplar) []float32 {
	vs := make([][]float32, len(es))
	ws := make([]float64, len(es))
	for i, e := range es {
		vs[i], ws[i] = e.Vec, e.Seconds
	}
	return Mean(vs, ws)
}

// Match scores one cluster against one profile.
type Match struct {
	PersonID string  `json:"personId"`
	Name     string  `json:"name"`
	Score    float64 `json:"score"`
	// Centroid and Exemplar are the two parts of the score: similarity of the
	// means, and the mean of the three most similar enrolled exemplars.
	Centroid float64 `json:"centroid"`
	Exemplar float64 `json:"exemplar"`
}

// Score is how likely the cluster is this person (cosine similarity, 0–1 in
// practice): the better of mean-to-mean similarity and the average of the
// three closest exemplars.
func Score(c Cluster, p Profile) Match {
	m := Match{PersonID: p.ID, Name: p.Name}
	if len(c.Centroid) == 0 || len(p.Exemplars) == 0 {
		return m
	}
	m.Centroid = Cosine(c.Centroid, p.Centroid)
	sims := make([]float64, 0, len(p.Exemplars))
	for _, e := range p.Exemplars {
		sims = append(sims, Cosine(c.Centroid, e.Vec))
	}
	sort.Sort(sort.Reverse(sort.Float64Slice(sims)))
	k := min(3, len(sims))
	for _, s := range sims[:k] {
		m.Exemplar += s
	}
	m.Exemplar /= float64(k)
	m.Score = round3(math.Max(m.Centroid, m.Exemplar))
	m.Centroid, m.Exemplar = round3(m.Centroid), round3(m.Exemplar)
	return m
}

// Rank scores a cluster against every profile, best first.
func Rank(c Cluster, people []Profile) []Match {
	out := make([]Match, 0, len(people))
	for _, p := range people {
		out = append(out, Score(c, p))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// Thresholds decide what matching does with a score (cosine similarity).
type Thresholds struct {
	// Auto labels a cluster with a person without asking, when the best match
	// also beats the runner-up by Margin. Clusters with less than AutoSeconds
	// of fingerprinted speech need AutoShort instead (short embeddings are noisy).
	Auto, AutoShort, Margin, AutoSeconds float64
	// FewAuto is the score automatic labels need when there are fewer than
	// FewOthers other people to compare with: with one or two known people,
	// "beats the runner-up" says nothing, so the bar is higher (and anything
	// between Suggest and FewAuto becomes an "Is this …?" question).
	FewAuto   float64
	FewOthers int
	// Suggest is the lowest score offered as "probably X" (a to-do asks).
	Suggest float64
}

// DefaultThresholds were measured on real recordings (docs/speakers.md):
// whole speakers of the same person scored 0.71–0.97 (0.80 across a 2.5-year
// gap and another room), 3 s pieces of the same person ≥ 0.58 in 95% of cases,
// and different people never above 0.35 (p99 0.25). The higher bar with few
// people (0.75) follows a real install where one wrongly named person was
// applied to other clips at 0.68 and 0.77 (docs/speakers.md).
var DefaultThresholds = Thresholds{Auto: 0.60, AutoShort: 0.55, Margin: 0.20, AutoSeconds: 4, FewAuto: 0.75, FewOthers: 2, Suggest: 0.40}

// AutoNeed is the score an automatic label needs for a cluster when
// candidates people are compared (the margin applies as well).
func (th Thresholds) AutoNeed(c Cluster, candidates int) float64 {
	need := th.Auto
	if c.Embedded < th.AutoSeconds || c.Minor {
		need = th.AutoShort
	}
	if candidates-1 < th.FewOthers && th.FewAuto > need {
		need = th.FewAuto
	}
	return need
}

// Decision is what matching concluded for one cluster.
type Decision struct {
	Auto        *Match  // labeled without asking
	Suggestions []Match // plausible people (≥ Suggest), best first, at most 3
}

// Decide matches one cluster against the people a workspace has named.
func Decide(c Cluster, people []Profile, th Thresholds) Decision {
	var d Decision
	if len(c.Centroid) == 0 {
		return d
	}
	ms := Rank(c, people)
	for _, m := range ms {
		if m.Score >= th.Suggest && len(d.Suggestions) < 3 {
			d.Suggestions = append(d.Suggestions, m)
		}
	}
	if len(ms) == 0 {
		return d
	}
	need := th.AutoNeed(c, len(people))
	second := -1.0
	if len(ms) > 1 {
		second = ms[1].Score
	}
	if ms[0].Score >= need && ms[0].Score-second >= th.Margin {
		m := ms[0]
		d.Auto = &m
	}
	return d
}

// Consolidate decides every cluster and merges clusters that were labeled
// with the same person (one person split by the diarizer). It returns the
// decision per remaining label; res is updated in place.
func Consolidate(res *Result, people []Profile, th Thresholds) map[string]Decision {
	out := map[string]Decision{}
	owner := map[string]string{} // personID → label that keeps the person
	into := map[string]string{}  // merged label → kept label
	for i := range res.Clusters {
		c := &res.Clusters[i]
		d := Decide(*c, people, th)
		if d.Auto != nil {
			c.Minor = false // a known person, however briefly heard
			if keep, ok := owner[d.Auto.PersonID]; ok {
				into[c.Label] = keep
				res.Merged++
				res.Notes = append(res.Notes, fmt.Sprintf("merged %s into %s (both sound like %s)", c.Label, keep, d.Auto.Name))
				continue
			}
			owner[d.Auto.PersonID] = c.Label
		}
		out[c.Label] = d
	}
	if len(into) == 0 {
		return out
	}
	byLabel := map[string]*Cluster{}
	var kept []Cluster
	for _, c := range res.Clusters {
		if _, gone := into[c.Label]; !gone {
			kept = append(kept, c)
		}
	}
	for i := range kept {
		byLabel[kept[i].Label] = &kept[i]
	}
	for _, c := range res.Clusters {
		k, gone := into[c.Label]
		if !gone {
			continue
		}
		dst := byLabel[k]
		dst.Seconds = round3(dst.Seconds + c.Seconds)
		dst.Embedded = round3(dst.Embedded + c.Embedded)
		dst.Turns += c.Turns
		dst.First = math.Min(dst.First, c.First)
		all := append(append([]Exemplar(nil), dst.Exemplars...), c.Exemplars...)
		vs, ws := exemplarVecs(all)
		dst.Centroid = Mean(vs, ws)
		dst.Exemplars = pickExemplars(all, dst.Centroid, max(len(dst.Exemplars), len(c.Exemplars)))
		dst.Samples = mergeSamples(dst.Samples, c.Samples, 3)
		pt := CombinePitch([]Pitch{{dst.Pitch, dst.Voiced}, {c.Pitch, c.Voiced}})
		dst.Pitch, dst.Voiced = pt.Hz, pt.Voiced
	}
	for i := range res.Turns {
		if k, gone := into[res.Turns[i].Label]; gone {
			res.Turns[i].Label = k
		}
	}
	res.Clusters = kept
	return out
}

// mergeSamples keeps up to n samples, longest first, in time order.
func mergeSamples(a, b []Span, n int) []Span {
	all := append(append([]Span(nil), a...), b...)
	sort.SliceStable(all, func(i, j int) bool { return all[i].Len() > all[j].Len() })
	if len(all) > n {
		all = all[:n]
	}
	sort.Slice(all, func(i, j int) bool { return all[i].From < all[j].From })
	return all
}
