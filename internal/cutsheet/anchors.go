package cutsheet

import (
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/textalign"
)

// Word anchors: a clip can start `from "because I'm going"` and end
// `to "very quickly"` instead of at times. The phrase is found in that
// footage's transcript; the in point lands just before the first word and the
// out point just after the last one, inside the silence around them, so no word
// is clipped and nothing of the neighbouring words is included.

const (
	padBefore = 0.10 // seconds of air kept before the first word, when the gap allows
	padAfter  = 0.15 // seconds kept after the last word (word ends trail off)
)

type wordRef struct {
	text     string
	norm     string
	from, to float64
	approx   bool   // timing interpolated from a sentence-level segment
	speaker  string // who says it (a name or "Speaker N"; empty when unknown)
}

// normWord lowercases and keeps letters and digits only: "I'm" → "im",
// "Quickly." → "quickly".
func normWord(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// flattenWords lists every timed word in source order. Segments without word
// timing get evenly spread, approximate word times.
func flattenWords(segs []editspec.TranscriptSegment) []wordRef {
	var out []wordRef
	for _, s := range segs {
		if len(s.Words) > 0 {
			for _, w := range s.Words {
				if n := normWord(w.Text); n != "" {
					out = append(out, wordRef{text: strings.TrimSpace(w.Text), norm: n, from: w.From, to: w.To, speaker: s.Speaker})
				}
			}
			continue
		}
		parts := strings.Fields(s.Text)
		if len(parts) == 0 || s.To <= s.From {
			continue
		}
		step := (s.To - s.From) / float64(len(parts))
		// float64(…) rounds each product, so no machine fuses it into the add
		// (the times are the same everywhere, as in the dashboard's copy).
		for i, t := range parts {
			if n := normWord(t); n != "" {
				out = append(out, wordRef{text: t, norm: n, from: s.From + float64(float64(i)*step), to: s.From + float64(float64(i+1)*step), approx: true, speaker: s.Speaker})
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].from < out[j].from })
	return out
}

// wordsIn returns the words spoken inside [in, out) (by their midpoint, the
// same rule captions use).
func wordsIn(ws []wordRef, in, out float64) []wordRef {
	var got []wordRef
	for _, w := range ws {
		mid := (w.from + w.to) / 2
		if mid >= in && mid < out {
			got = append(got, w)
		}
	}
	return got
}

type phraseMatch struct {
	first, last int // word indices (inclusive)
	exact       bool
	dist        int
}

func phraseTokens(phrase string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(phrase, func(r rune) bool { return unicode.IsSpace(r) || r == '-' || r == '—' || r == '–' }) {
		if n := normWord(f); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// hasUnspaced reports whether text has letters of a script written without
// spaces between words (textalign.Unspaced).
func hasUnspaced(text string) bool {
	for _, r := range text {
		if textalign.Unspaced(r) && (unicode.IsLetter(r) || r == 'ー' || r == '々') {
			return true
		}
	}
	return false
}

func joined(ws []wordRef) string {
	var b strings.Builder
	for _, w := range ws {
		b.WriteString(w.norm)
	}
	return b.String()
}

// findPhrase finds the phrase in the words: exact matches first, then matches
// that differ only in how words are split ("every one" / "everyone"), then
// small spelling differences.
func findPhrase(ws []wordRef, phrase string) []phraseMatch {
	toks := phraseTokens(phrase)
	n := len(toks)
	if n == 0 {
		return nil
	}
	var exact []phraseMatch
	for i := 0; i+n <= len(ws); i++ {
		ok := true
		for k := 0; k < n; k++ {
			if ws[i+k].norm != toks[k] {
				ok = false
				break
			}
		}
		if ok {
			exact = append(exact, phraseMatch{first: i, last: i + n - 1, exact: true})
		}
	}
	if len(exact) > 0 {
		return exact
	}
	want := strings.Join(toks, "")
	// Japanese, Chinese and Thai words are pieces of a line (D244): a quote
	// matches any run of pieces that joins into it, however many there are.
	if hasUnspaced(phrase) {
		var runs []phraseMatch
		for i := range ws {
			got := ""
			for j := i; j < len(ws) && len(got) < len(want); j++ {
				got += ws[j].norm
				if !strings.HasPrefix(want, got) {
					break
				}
				if got == want {
					runs = append(runs, phraseMatch{first: i, last: j, exact: true})
				}
			}
		}
		return runs
	}
	var split []phraseMatch
	for _, L := range []int{n - 1, n + 1, n - 2, n + 2} {
		if L < 1 {
			continue
		}
		for i := 0; i+L <= len(ws); i++ {
			if joined(ws[i:i+L]) == want {
				split = append(split, phraseMatch{first: i, last: i + L - 1, exact: true})
			}
		}
		if len(split) > 0 {
			return split
		}
	}
	var fuzzy []phraseMatch
	budget := max(1, len([]rune(want))/6)
	for i := 0; i+n <= len(ws); i++ {
		total, ok := 0, true
		for k := 0; k < n && ok; k++ {
			d := editDistance(ws[i+k].norm, toks[k])
			if d > allowance(toks[k]) {
				ok = false
			}
			total += d
		}
		if ok && total <= budget {
			fuzzy = append(fuzzy, phraseMatch{first: i, last: i + n - 1, dist: total})
		}
	}
	if len(fuzzy) == 0 {
		return nil
	}
	best := fuzzy[0].dist
	for _, m := range fuzzy {
		best = min(best, m.dist)
	}
	var out []phraseMatch
	for _, m := range fuzzy {
		if m.dist == best {
			out = append(out, m)
		}
	}
	return out
}

func allowance(tok string) int {
	switch n := len([]rune(tok)); {
	case n <= 3:
		return 0
	case n <= 7:
		return 1
	default:
		return 2
	}
}

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

// snapIn is the in point for a clip starting at word i.
func snapIn(ws []wordRef, i int) float64 {
	w := ws[i]
	v := w.from - padBefore
	if i > 0 {
		gap := w.from - ws[i-1].to
		switch {
		case gap >= 2*padBefore:
		case gap > 0:
			v = ws[i-1].to + gap/2
		default:
			v = w.from
		}
	}
	return math.Max(0, math.Floor(v*1000)/1000)
}

// snapOut is the out point for a clip ending at word j.
func snapOut(ws []wordRef, j int, duration float64) float64 {
	w := ws[j]
	v := w.to + padAfter
	if j+1 < len(ws) {
		gap := ws[j+1].from - w.to
		switch {
		case gap >= 2*padAfter:
		case gap > 0:
			v = w.to + gap/2
		default:
			v = w.to
		}
	}
	v = math.Ceil(v*1000) / 1000
	if duration > 0 && v > duration {
		v = duration
	}
	return v
}

// context shows the matched words with a little around them.
func context(ws []wordRef, m phraseMatch) string {
	lo, hi := max(0, m.first-2), min(len(ws)-1, m.last+2)
	var parts []string
	for i := lo; i <= hi; i++ {
		parts = append(parts, ws[i].text)
	}
	s := strings.Join(parts, " ")
	if lo > 0 {
		s = "… " + s
	}
	if hi < len(ws)-1 {
		s += " …"
	}
	return s
}

func matchedWords(ws []wordRef, m phraseMatch) string {
	var parts []string
	for i := m.first; i <= m.last; i++ {
		parts = append(parts, ws[i].text)
	}
	return strings.Join(parts, " ")
}

// closest finds the transcript passage that looks most like the phrase (for a
// "did you mean" hint).
func closest(ws []wordRef, phrase string) (phraseMatch, bool) {
	toks := phraseTokens(phrase)
	n := len(toks)
	if n == 0 || len(ws) == 0 {
		return phraseMatch{}, false
	}
	want := strings.Join(toks, "")
	best, bestD := phraseMatch{}, math.MaxInt
	for i := 0; i+n <= len(ws); i++ {
		if d := editDistance(joined(ws[i:i+n]), want); d < bestD {
			best, bestD = phraseMatch{first: i, last: i + n - 1, dist: d}, d
		}
	}
	if bestD > max(2, len([]rune(want))/2) {
		return phraseMatch{}, false
	}
	return best, true
}
