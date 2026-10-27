package faceid

import (
	"math"
	"sort"
)

// Profile is a named person's face: fingerprints from faces a person confirmed
// are them (several clips, several frames each), plus their mean. Keeping the
// exemplars lets a new face match the closest conditions the person was seen
// in (another light, another angle, years apart).
type Profile struct {
	ID, Name  string
	Centroid  []float32
	Exemplars []Exemplar
}

// Exemplar is one enrolled fingerprint and where it came from.
type Exemplar struct {
	Source string // <faceId>: the confirmed face it came from
	Vec    []float32
	Weight float64 // how usable the picture was (capture quality)
}

// Enrollment caps: per confirmed face and in total.
const (
	MaxExemplarsPerFace = 4
	MaxExemplars        = 80
)

// NewProfile builds a profile from exemplars (the newest last; the oldest
// drop first past MaxExemplars).
func NewProfile(id, name string, ex []Exemplar) Profile {
	if len(ex) > MaxExemplars {
		ex = ex[len(ex)-MaxExemplars:]
	}
	vs := make([][]float32, len(ex))
	ws := make([]float64, len(ex))
	for i, e := range ex {
		vs[i], ws[i] = e.Vec, e.Weight
	}
	return Profile{ID: id, Name: name, Exemplars: ex, Centroid: Mean(vs, ws)}
}

// Match scores one face against one profile.
type Match struct {
	PersonID string  `json:"personId"`
	Name     string  `json:"name"`
	Score    float64 `json:"score"`
	// Centroid and Exemplar are the two parts of the score: similarity to the
	// mean, and the mean of the three most similar enrolled exemplars.
	Centroid float64 `json:"centroid"`
	Exemplar float64 `json:"exemplar"`
}

// Score is how likely a face (its fingerprint) is this person: the better of
// similarity to the profile's mean and the average of the three closest
// exemplars (cosine similarity).
func Score(v []float32, p Profile) Match {
	m := Match{PersonID: p.ID, Name: p.Name}
	if len(v) == 0 || len(p.Exemplars) == 0 {
		return m
	}
	m.Centroid = Cosine(v, p.Centroid)
	sims := make([]float64, 0, len(p.Exemplars))
	for _, e := range p.Exemplars {
		sims = append(sims, Cosine(v, e.Vec))
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

// Rank scores a face against every profile, best first.
func Rank(v []float32, people []Profile) []Match {
	out := make([]Match, 0, len(people))
	for _, p := range people {
		out = append(out, Score(v, p))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

// Thresholds decide what matching does with a score (cosine similarity).
type Thresholds struct {
	// Auto names a face without asking when the best match also beats the
	// runner-up by Margin.
	Auto, Margin float64
	// FewAuto is the score an automatic name needs while fewer than FewOthers
	// other people have confirmed faces: with one or two people known, "beats
	// the runner-up" says little, so the bar is higher.
	FewAuto   float64
	FewOthers int
	// Suggest is the lowest score asked about ("Is this Ada?").
	Suggest float64
}

// DefaultThresholds were measured with tools/vision/eval/faces.py on
// public-domain photos (docs/visual-index.md, D114): different people never
// scored above 0.40 against a profile, the same person scored 0.45–0.85.
var DefaultThresholds = Thresholds{Auto: 0.60, Margin: 0.15, FewAuto: 0.66, FewOthers: 2, Suggest: 0.38}

// AutoNeed is the score an automatic name needs when candidates people have
// confirmed faces (the margin applies as well).
func (th Thresholds) AutoNeed(candidates int) float64 {
	need := th.Auto
	if candidates-1 < th.FewOthers && th.FewAuto > need {
		need = th.FewAuto
	}
	return need
}

// Decision is what matching concluded for one face.
type Decision struct {
	Auto        *Match  // named without asking
	Suggestions []Match // plausible people (≥ Suggest), best first, at most 3
}

// Decide matches one face against the people with confirmed faces; people it
// was said not to be are left out.
func Decide(v []float32, people []Profile, not map[string]bool, th Thresholds) Decision {
	var d Decision
	if len(v) == 0 {
		return d
	}
	var ms []Match
	for _, m := range Rank(v, people) {
		if !not[m.PersonID] {
			ms = append(ms, m)
		}
	}
	for _, m := range ms {
		if m.Score >= th.Suggest && len(d.Suggestions) < 3 {
			d.Suggestions = append(d.Suggestions, m)
		}
	}
	if len(ms) == 0 {
		return d
	}
	need := th.AutoNeed(len(people))
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

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
