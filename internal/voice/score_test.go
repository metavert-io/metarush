//go:build voiceeval

package voice

// Scoring helpers for the evaluation harness (eval_test.go).

import (
	"math"
	"sort"
)

// Reference speaker turn for scoring.
type refTurn struct {
	From, To float64
	Speaker  string
}

// derScore is frame-based diarization error (10 ms frames) with a collar
// around every reference boundary, optimal one-to-one label mapping.
type derScore struct {
	Total, Miss, FalseAlarm, Confusion float64 // seconds of scored reference speech / errors
	Mapping                            map[string]string
}

func (d derScore) DER() float64 {
	if d.Total == 0 {
		return 0
	}
	return (d.Miss + d.FalseAlarm + d.Confusion) / d.Total
}

func scoreDER(ref []refTurn, hyp []Turn, collar, from, to float64) derScore {
	const step = 0.01
	n := int(math.Ceil((to - from) / step))
	skip := make([]bool, n)
	for _, r := range ref {
		for _, b := range []float64{r.From, r.To} {
			for i := int((b - collar - from) / step); i <= int((b+collar-from)/step); i++ {
				if i >= 0 && i < n {
					skip[i] = true
				}
			}
		}
	}
	refAt := make([][]string, n)
	hypAt := make([][]string, n)
	for _, r := range ref {
		for i := max(0, int((r.From-from)/step)); i < min(n, int((r.To-from)/step)); i++ {
			refAt[i] = append(refAt[i], r.Speaker)
		}
	}
	for _, h := range hyp {
		for i := max(0, int((h.From-from)/step)); i < min(n, int((h.To-from)/step)); i++ {
			if !contains(hypAt[i], h.Label) {
				hypAt[i] = append(hypAt[i], h.Label)
			}
		}
	}
	// Co-occurrence for the mapping.
	co := map[[2]string]float64{}
	for i := 0; i < n; i++ {
		if skip[i] {
			continue
		}
		for _, r := range refAt[i] {
			for _, h := range hypAt[i] {
				co[[2]string{h, r}] += step
			}
		}
	}
	mapping := greedyMap(co)
	var d derScore
	d.Mapping = mapping
	for i := 0; i < n; i++ {
		if skip[i] {
			continue
		}
		nr, nh := len(refAt[i]), len(hypAt[i])
		d.Total += float64(nr) * step
		correct := 0
		for _, h := range hypAt[i] {
			if contains(refAt[i], mapping[h]) {
				correct++
			}
		}
		if nr > nh {
			d.Miss += float64(nr-nh) * step
		} else {
			d.FalseAlarm += float64(nh-nr) * step
		}
		d.Confusion += float64(min(nr, nh)-correct) * step
	}
	return d
}

func greedyMap(co map[[2]string]float64) map[string]string {
	type kv struct {
		k [2]string
		v float64
	}
	var all []kv
	for k, v := range co {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k[0]+all[i].k[1] < all[j].k[0]+all[j].k[1]
	})
	m := map[string]string{}
	usedRef := map[string]bool{}
	for _, x := range all {
		if _, ok := m[x.k[0]]; ok || usedRef[x.k[1]] {
			continue
		}
		m[x.k[0]] = x.k[1]
		usedRef[x.k[1]] = true
	}
	return m
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// majority returns the hypothesis label covering most of [from, to).
func majority(hyp []Turn, from, to float64) string {
	cov := map[string]float64{}
	for _, h := range hyp {
		if ov := math.Min(to, h.To) - math.Max(from, h.From); ov > 0 {
			cov[h.Label] += ov
		}
	}
	best, bv := "", 0.0
	for l, v := range cov {
		if v > bv || (v == bv && l < best) {
			best, bv = l, v
		}
	}
	return best
}
