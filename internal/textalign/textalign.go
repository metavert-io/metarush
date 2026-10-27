// Package textalign aligns two word sequences the way people compare speech:
// case and punctuation do not matter, filler words ("um", "uh") are free, and
// small transcription errors still count as the same word. It is used to fit a
// recorded or imported voiceover to its script, to check a recording against
// the words it was meant to say, and by QA to compare the speech in a render
// with the speech its plan expects (repeated, missing and unexpected words).
package textalign

import (
	"math"
	"strings"
	"unicode"
)

// Word is one timed word. Group lets callers tag words (beat index, clip index).
type Word struct {
	Text  string
	Norm  string
	From  float64
	To    float64
	Group int
}

// Normalize lowercases a word, folds typographic quotes and dashes and strips
// surrounding punctuation ("Twice." → "twice", "don’t" → "don't").
func Normalize(w string) string {
	w = strings.ToLower(strings.TrimSpace(w))
	w = strings.NewReplacer("’", "'", "‘", "'", "“", "", "”", "", "—", "", "–", "").Replace(w)
	w = strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	return w
}

var fillers = map[string]bool{"um": true, "umm": true, "uh": true, "uhh": true, "uhm": true, "er": true, "erm": true,
	"ah": true, "ahh": true, "hmm": true, "hm": true, "mm": true, "mhm": true, "eh": true}

// IsFiller reports a normalized filler word.
func IsFiller(norm string) bool { return fillers[norm] }

// Tokens splits text into words (whitespace; hyphenated compounds stay one word).
func Tokens(text string) []string {
	var out []string
	for _, f := range strings.Fields(text) {
		if Normalize(f) != "" {
			out = append(out, f)
		}
	}
	return out
}

// FromText turns text into untimed Words with the given group.
func FromText(text string, group int) []Word {
	var out []Word
	for _, t := range Tokens(text) {
		out = append(out, Word{Text: t, Norm: Normalize(t), Group: group})
	}
	return out
}

// Similar reports whether two normalized words are the same spoken word allowing
// for transcription noise: equal; one a long prefix of the other (a word clipped
// at a cut); or a small edit distance (≤1 for 4–7 letters, ≤2 from 8).
func Similar(a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	ra, rb := []rune(a), []rune(b)
	short, long := ra, rb
	if len(short) > len(long) {
		short, long = long, short
	}
	if len(short) >= 4 && strings.HasPrefix(string(long), string(short)) && float64(len(short)) >= 0.7*float64(len(long)) {
		return true
	}
	// Plural / possessive variants ("cut" vs "cuts", "maren" vs "maren's").
	if len(short) >= 3 && strings.HasPrefix(string(long), string(short)) && len(long)-len(short) <= 2 {
		suf := string(long[len(short):])
		if suf == "s" || suf == "'s" || suf == "es" {
			return true
		}
	}
	if len(short) < 4 {
		return false
	}
	if len(long) <= 5 {
		// Short words: only a dropped or added letter ("form"/"from" and
		// "light"/"night" are different words).
		return len(long)-len(short) == 1 && levenshtein(ra, rb, 1) <= 1
	}
	max := 1
	if len(short) >= 9 {
		max = 2
	}
	return levenshtein(ra, rb, max) <= max
}

// levenshtein returns the edit distance, or max+1 once it is exceeded.
func levenshtein(a, b []rune, max int) int {
	if d := len(a) - len(b); d > max || -d > max {
		return max + 1
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		best := cur[0]
		for j := 1; j <= len(b); j++ {
			c := 1
			if a[i-1] == b[j-1] {
				c = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+c)
			if cur[j] < best {
				best = cur[j]
			}
		}
		if best > max {
			return max + 1
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// Kind is one step of an alignment.
type Kind byte

// Step kinds.
const (
	Match Kind = '=' // the same word (Similar)
	Subst Kind = '~' // a different word in the same place
	Del   Kind = '-' // an expected word that is missing
	Ins   Kind = '+' // an observed word nothing expected
)

// Step pairs an expected index E with an observed index O (-1 when absent).
type Step struct {
	Kind Kind
	E, O int
}

const (
	costSubst      = 1.0
	costGap        = 0.75
	costFillerGap  = 0.05
	costTimeBroken = 9.0 // matching words this far apart in time is never chosen
)

// maxCells bounds the alignment matrix; longer inputs are aligned in chunks.
const maxCells = 16_000_000

// Align computes a minimum-cost alignment of observed against expected words.
// Filler words cost almost nothing to skip. When skew > 0 and both words carry
// times (To > 0), words more than skew seconds apart cannot match.
func Align(exp, obs []Word, skew float64) []Step {
	n, m := len(exp), len(obs)
	if n == 0 && m == 0 {
		return nil
	}
	if n*m > maxCells && n > 1 && m > 1 {
		return alignChunked(exp, obs, skew)
	}
	norm := func(w *Word) string {
		if w.Norm == "" {
			w.Norm = Normalize(w.Text)
		}
		return w.Norm
	}
	for i := range exp {
		norm(&exp[i])
	}
	for j := range obs {
		norm(&obs[j])
	}
	gapE := func(i int) float64 {
		if IsFiller(exp[i].Norm) {
			return costFillerGap
		}
		return costGap
	}
	gapO := func(j int) float64 {
		if IsFiller(obs[j].Norm) {
			return costFillerGap
		}
		return costGap
	}
	sub := func(i, j int) (float64, bool) {
		e, o := exp[i], obs[j]
		if skew > 0 && e.To > 0 && o.To > 0 && math.Abs(mid(e)-mid(o)) > skew {
			return costTimeBroken, false
		}
		if Similar(e.Norm, o.Norm) {
			return 0, true
		}
		return costSubst, false
	}
	// dir: 0 diag, 1 up (deletion of expected), 2 left (insertion of observed)
	dir := make([]byte, (n+1)*(m+1))
	prev := make([]float64, m+1)
	cur := make([]float64, m+1)
	for j := 1; j <= m; j++ {
		prev[j] = prev[j-1] + gapO(j-1)
		dir[j] = 2
	}
	for i := 1; i <= n; i++ {
		cur[0] = prev[0] + gapE(i-1)
		dir[i*(m+1)] = 1
		for j := 1; j <= m; j++ {
			c, _ := sub(i-1, j-1)
			best, d := prev[j-1]+c, byte(0)
			if v := prev[j] + gapE(i-1); v < best {
				best, d = v, 1
			}
			if v := cur[j-1] + gapO(j-1); v < best {
				best, d = v, 2
			}
			cur[j], dir[i*(m+1)+j] = best, d
		}
		prev, cur = cur, prev
	}
	steps := make([]Step, 0, n+m)
	for i, j := n, m; i > 0 || j > 0; {
		switch dir[i*(m+1)+j] {
		case 0:
			if i == 0 || j == 0 {
				panic("textalign: bad backtrack")
			}
			if _, ok := sub(i-1, j-1); ok {
				steps = append(steps, Step{Match, i - 1, j - 1})
			} else {
				steps = append(steps, Step{Subst, i - 1, j - 1})
			}
			i, j = i-1, j-1
		case 1:
			steps = append(steps, Step{Del, i - 1, -1})
			i--
		default:
			steps = append(steps, Step{Ins, -1, j - 1})
			j--
		}
	}
	for a, b := 0, len(steps)-1; a < b; a, b = a+1, b-1 {
		steps[a], steps[b] = steps[b], steps[a]
	}
	return steps
}

func mid(w Word) float64 { return (w.From + w.To) / 2 }

// alignChunked splits very long inputs at proportional points and aligns each
// part (only used beyond maxCells; precision at chunk edges is slightly lower).
func alignChunked(exp, obs []Word, skew float64) []Step {
	parts := int(math.Ceil(float64(len(exp)*len(obs)) / float64(maxCells)))
	var out []Step
	for k := 0; k < parts; k++ {
		e0, e1 := len(exp)*k/parts, len(exp)*(k+1)/parts
		o0, o1 := len(obs)*k/parts, len(obs)*(k+1)/parts
		for _, s := range Align(exp[e0:e1], obs[o0:o1], skew) {
			if s.E >= 0 {
				s.E += e0
			}
			if s.O >= 0 {
				s.O += o0
			}
			out = append(out, s)
		}
	}
	return out
}

// Score summarizes an alignment: matched expected words over non-filler expected words.
func Score(exp []Word, steps []Step) (matched, total int) {
	for _, s := range steps {
		if s.E < 0 || IsFiller(exp[s.E].Norm) {
			continue
		}
		total++
		if s.Kind == Match {
			matched++
		}
	}
	return matched, total
}
