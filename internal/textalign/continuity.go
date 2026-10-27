package textalign

import "math"

// Issue kinds found by Continuity.
const (
	Repeated   = "repeated"   // words heard twice where the plan has them once
	Missing    = "missing"    // expected words that are not heard
	Unexpected = "unexpected" // speech nothing in the plan accounts for
	// Jump: the speech after a point runs late (a slice of sound played twice,
	// or stretched) or early (a slice skipped) — even when the stutter itself
	// is too short for the transcriber to hear as a word.
	Jump = "jump"
)

// Issue is one continuity problem. Times are timeline seconds: observed times
// for repeated and unexpected speech, expected times for missing speech.
type Issue struct {
	Kind   string   `json:"kind"`
	From   float64  `json:"from"`
	To     float64  `json:"to"`
	Words  []string `json:"words"`
	Phrase bool     `json:"phrase"` // a whole phrase (≥ PhraseWords words, or a whole group) duplicated or dropped
	Group  int      `json:"group"`  // group of the nearest expected word
}

// ContinuityOptions tune tolerance to transcription noise.
type ContinuityOptions struct {
	// MinMissing / MinUnexpected are the shortest runs (non-filler words) reported.
	MinMissing, MinUnexpected int
	// PhraseWords is the run length that makes a duplicate or a gap a whole phrase.
	PhraseWords int
	// Edges are timeline seconds where audio is cut (clip boundaries); single-word
	// differences within EdgeTolerance of one are expected (clipped words).
	Edges         []float64
	EdgeTolerance float64
	// JumpSeconds is the smallest sudden change in timing reported as a jump
	// (0 turns jump detection off).
	JumpSeconds float64
}

// DefaultContinuity returns conservative defaults.
func DefaultContinuity() ContinuityOptions {
	return ContinuityOptions{MinMissing: 2, MinUnexpected: 2, PhraseWords: 3, EdgeTolerance: 0.35, JumpSeconds: 0.3}
}

// Continuity reads an alignment of observed (heard) against expected (planned)
// speech and reports repeated, missing and unexpected words. Single substituted
// words are treated as transcription noise; fillers never count.
func Continuity(exp, obs []Word, steps []Step, o ContinuityOptions) []Issue {
	if o.PhraseWords <= 0 {
		o.PhraseWords = 3
	}
	nearEdge := func(t float64) bool {
		for _, e := range o.Edges {
			if math.Abs(t-e) <= o.EdgeTolerance {
				return true
			}
		}
		return false
	}
	groupSize := map[int]int{}
	for _, w := range exp {
		if !IsFiller(w.Norm) {
			groupSize[w.Group]++
		}
	}
	// Observed indices that the alignment matched, in order.
	var out []Issue
	for k := 0; k < len(steps); {
		s := steps[k]
		switch s.Kind {
		case Ins:
			end := k
			for end < len(steps) && steps[end].Kind == Ins {
				end++
			}
			var run []int
			for _, st := range steps[k:end] {
				if !IsFiller(obs[st.O].Norm) {
					run = append(run, st.O)
				}
			}
			if len(run) > 0 {
				if iss, ok := repeatIssue(exp, obs, steps, k, end, run, o); ok {
					out = append(out, iss)
				} else if len(run) >= max(1, o.MinUnexpected) {
					from, to := obs[run[0]].From, obs[run[len(run)-1]].To
					if !(len(run) == 1 && nearEdge(from)) {
						out = append(out, Issue{Kind: Unexpected, From: from, To: to, Words: texts(obs, run), Group: nearestGroup(exp, steps, k)})
					}
				}
			}
			k = end
		case Del:
			end := k
			for end < len(steps) && steps[end].Kind == Del {
				end++
			}
			var run []int
			for _, st := range steps[k:end] {
				if !IsFiller(exp[st.E].Norm) {
					run = append(run, st.E)
				}
			}
			if len(run) >= max(1, o.MinMissing) {
				from, to := exp[run[0]].From, exp[run[len(run)-1]].To
				g := exp[run[0]].Group
				whole := groupSize[g] > 0 && countGroup(exp, run, g) == groupSize[g]
				if !(len(run) == 1 && nearEdge(from)) {
					out = append(out, Issue{Kind: Missing, From: from, To: to, Words: texts(exp, run), Group: g,
						Phrase: len(run) > o.PhraseWords || (whole && len(run) >= 2)})
				}
			}
			k = end
		default:
			k++
		}
	}
	return out
}

// Jumps finds sudden timing changes between consecutive matched words: where
// the plan and the render agree on the words but, from some point on, the
// render says them later (inserted sound) or earlier (skipped sound). Steps
// within EdgeTolerance of a cut are ignored. Delta is Issue.To-Issue.From.
func Jumps(exp, obs []Word, steps []Step, o ContinuityOptions) []Issue {
	if o.JumpSeconds <= 0 {
		return nil
	}
	nearEdge := func(t float64) bool {
		for _, e := range o.Edges {
			if math.Abs(t-e) <= o.EdgeTolerance {
				return true
			}
		}
		return false
	}
	type pair struct{ e, o int }
	var ps []pair
	for _, s := range steps {
		if s.Kind == Match && exp[s.E].To > 0 && obs[s.O].To > 0 && !IsFiller(exp[s.E].Norm) {
			ps = append(ps, pair{s.E, s.O})
		}
	}
	var out []Issue
	for i := 1; i < len(ps); i++ {
		a, b := ps[i-1], ps[i]
		da := obs[a.o].From - exp[a.e].From
		db := obs[b.o].From - exp[b.e].From
		step := db - da
		if math.Abs(step) < o.JumpSeconds || nearEdge(exp[b.e].From) || nearEdge(exp[a.e].To) {
			continue
		}
		// The new timing must hold for the next matched word too (not one mistimed word).
		if i+1 < len(ps) {
			c := ps[i+1]
			if dc := obs[c.o].From - exp[c.e].From; math.Abs(dc-db) > o.JumpSeconds/2 {
				continue
			}
		}
		out = append(out, Issue{Kind: Jump, From: obs[a.o].To, To: obs[a.o].To + step, Words: []string{exp[a.e].Text, exp[b.e].Text}, Group: exp[b.e].Group})
	}
	return out
}

// repeatIssue reports an inserted run that repeats the words heard right before
// or right after it ("we built built it", "and stayed for the — and stayed for the").
func repeatIssue(exp, obs []Word, steps []Step, k, end int, run []int, o ContinuityOptions) (Issue, bool) {
	n := len(run)
	// Observed words around the run that the alignment accounted for.
	var before, after []int
	for i := k - 1; i >= 0 && len(before) < n; i-- {
		if steps[i].O >= 0 && !IsFiller(obs[steps[i].O].Norm) {
			before = append([]int{steps[i].O}, before...)
		}
	}
	for i := end; i < len(steps) && len(after) < n; i++ {
		if steps[i].O >= 0 && !IsFiller(obs[steps[i].O].Norm) {
			after = append(after, steps[i].O)
		}
	}
	same := func(ctx []int) bool {
		if len(ctx) != n {
			return false
		}
		for i := range run {
			if !Similar(obs[run[i]].Norm, obs[ctx[i]].Norm) {
				return false
			}
		}
		return true
	}
	// A partial repeat of a single clipped word ("nor northern") also counts.
	partial := n == 1 && len(after) == 1 && isPrefixFragment(obs[run[0]].Norm, obs[after[0]].Norm)
	if !same(before) && !same(after) && !partial {
		return Issue{}, false
	}
	from, to := obs[run[0]].From, obs[run[n-1]].To
	return Issue{Kind: Repeated, From: from, To: to, Words: texts(obs, run), Phrase: n >= o.PhraseWords, Group: nearestGroup(exp, steps, k)}, true
}

func isPrefixFragment(frag, word string) bool {
	return len(frag) >= 2 && len(word) > len(frag) && word[:len(frag)] == frag
}

func texts(ws []Word, idx []int) []string {
	out := make([]string, len(idx))
	for i, j := range idx {
		out[i] = ws[j].Text
	}
	return out
}

func countGroup(exp []Word, idx []int, g int) int {
	n := 0
	for _, i := range idx {
		if exp[i].Group == g {
			n++
		}
	}
	return n
}

func nearestGroup(exp []Word, steps []Step, k int) int {
	for d := 0; d < len(steps); d++ {
		for _, i := range []int{k - d, k + d} {
			if i >= 0 && i < len(steps) && steps[i].E >= 0 {
				return exp[steps[i].E].Group
			}
		}
	}
	return 0
}
