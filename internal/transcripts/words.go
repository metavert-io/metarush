// Package transcripts fixes transcripts and remembers what changed (D145–D149,
// docs/transcripts.md): words retyped by hand, find and replace across videos,
// spelling lists, who speaks a stretch; every change is a fix record with who
// and when that can be undone, the machine's own transcript is kept, saved
// versions of a cut keep the words they were made with (pins and snapshots),
// and fixes waiting for a new version are listed per cut.
//
// The words of a clip live in its transcript segments (store.CSegments), which
// always hold the current text: every reader (captions of new versions, the
// Script view, search, footage reads, cut sheet quotes) follows fixes without
// knowing about them. This file is the word-level part: finding words, and
// writing new words over old ones so their time stays where it was spoken.
package transcripts

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/textalign"
)

// Unit is one word of a clip's transcript, in order: a timed word, or, in a
// line without word timing, one of its words with times spread evenly.
type Unit struct {
	Seg  int // index into the clip's segments
	Word int // index into the segment's words; -1 for a word of a line without word times (WordInText is then its place)
	// WordInText is the word's place among the line's words (lines without word times).
	WordInText int
	Text       string
	From, To   float64
	Score      float64
	Fixed      bool
}

// Units lists a clip's words in order.
func Units(segs []models.TranscriptSegment) []Unit {
	var out []Unit
	for si, s := range segs {
		if len(s.Words) > 0 {
			for wi, w := range s.Words {
				t := strings.TrimSpace(w.Text)
				if t == "" {
					continue
				}
				out = append(out, Unit{Seg: si, Word: wi, Text: t, From: w.From, To: w.To, Score: w.Score, Fixed: w.Fixed})
			}
			continue
		}
		parts := strings.Fields(s.Text)
		if len(parts) == 0 {
			continue
		}
		step := 0.0
		if s.To > s.From {
			step = (s.To - s.From) / float64(len(parts))
		}
		for i, p := range parts {
			out = append(out, Unit{Seg: si, Word: -1, WordInText: i, Text: p, From: round3(s.From + float64(i)*step), To: round3(s.From + float64(i+1)*step)})
		}
	}
	return out
}

// fold is how words compare when case doesn't matter: lowercase, with
// typographic apostrophes and quotes made plain. It maps rune to rune, so
// positions in a folded string are positions in the original.
func fold(r rune, matchCase bool) rune {
	switch r {
	case '’', '‘', 'ʼ':
		r = '\''
	}
	if !matchCase {
		r = unicode.ToLower(r)
	}
	return r
}

func foldString(s string, matchCase bool) string {
	var b strings.Builder
	for _, r := range s {
		b.WriteRune(fold(r, matchCase))
	}
	return b.String()
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
}

// Split separates a written word into the punctuation before it, the word
// itself and the punctuation after it: `"Brook,"` → `"`, `Brook`, `,"`.
// Apostrophes and hyphens inside a word belong to it ("don't", "Fern-Brook").
func Split(text string) (lead, core, trail string) {
	rs := []rune(text)
	i := 0
	for i < len(rs) && !isWordRune(rs[i]) {
		i++
	}
	j := len(rs)
	for j > i && !isWordRune(rs[j-1]) {
		j--
	}
	return string(rs[:i]), string(rs[i:j]), string(rs[j:])
}

// Norm is a word for comparing spellings: lowercase letters and digits only
// ("Fern-Brook," → "fernbrook", "Maple Grove" → "maplegrove").
func Norm(text string) string {
	var b strings.Builder
	for _, r := range text {
		r = fold(r, false)
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Span is a match: units I..J (inclusive) of a clip, and what is kept of the
// first and last unit around the matched text (punctuation in whole-word mode,
// the rest of the word in part-of-a-word mode).
type Span struct {
	I, J int
	Pre  string
	Post string
}

// MatchOptions say how a query matches.
type MatchOptions struct {
	// Parts: the query may be part of a word ("Brook" finds "FernBrook"); by default
	// only whole words match.
	Parts     bool
	MatchCase bool
}

// QueryTokens splits a query into the words it looks for.
func QueryTokens(q string) []string {
	var out []string
	for _, f := range strings.Fields(q) {
		if _, core, _ := Split(f); core != "" {
			out = append(out, core)
		}
	}
	return out
}

// Match finds every place the query is said in the units, left to right,
// without overlaps. Whole words compare word by word, punctuation around a
// word aside ("Fern Brook" finds "Fern. Brook,"); parts of words compare the text with
// one space between words. A query in a script written without spaces
// (Japanese, Chinese, Thai: D244) compares the text as it is written, its
// pieces joined without spaces and punctuation aside ("南の方" finds the pieces
// 南 の 方, and "は南" finds "は、南"), wherever the pieces begin or end.
func Match(units []Unit, query string, o MatchOptions) []Span {
	if hasUnspaced(query) {
		return matchUnspaced(units, query, o.MatchCase)
	}
	if o.Parts {
		return matchParts(units, query, o.MatchCase)
	}
	toks := QueryTokens(query)
	if len(toks) == 0 {
		return nil
	}
	want := make([]string, len(toks))
	for i, t := range toks {
		want[i] = foldString(t, o.MatchCase)
	}
	cores := make([]string, len(units))
	for i, u := range units {
		_, c, _ := Split(u.Text)
		cores[i] = foldString(c, o.MatchCase)
	}
	var out []Span
	for i := 0; i+len(want) <= len(units); {
		ok := true
		for k, w := range want {
			u := units[i+k]
			if cores[i+k] != w || (k > 0 && !joinable(units[i+k-1], u)) {
				ok = false
				break
			}
		}
		if !ok {
			i++
			continue
		}
		j := i + len(want) - 1
		lead, _, _ := Split(units[i].Text)
		_, _, trail := Split(units[j].Text)
		out = append(out, Span{I: i, J: j, Pre: lead, Post: trail})
		i = j + 1
	}
	return out
}

// joinable reports whether two neighbouring units can be one match: a match
// never spans two lines without word times, or a line without word times and
// a timed one.
func joinable(a, b Unit) bool {
	if a.Word >= 0 && b.Word >= 0 {
		return true
	}
	return a.Seg == b.Seg
}

func matchParts(units []Unit, query string, matchCase bool) []Span {
	q := []rune(foldString(strings.Join(strings.Fields(query), " "), matchCase))
	if len(q) == 0 || len(units) == 0 {
		return nil
	}
	// The clip's text with one space between words, and where each word starts.
	var text []rune
	start := make([]int, len(units))
	for i, u := range units {
		if i > 0 {
			text = append(text, ' ')
		}
		start[i] = len(text)
		text = append(text, []rune(foldString(u.Text, matchCase))...)
	}
	unitAt := func(pos int) int { // the unit holding text position pos (a space belongs to the next word)
		k := sort.Search(len(start), func(k int) bool { return start[k] > pos }) - 1
		if k < 0 {
			k = 0
		}
		if pos >= start[k]+utf8.RuneCountInString(units[k].Text) && k+1 < len(units) {
			k++
		}
		return k
	}
	var out []Span
	for s := 0; s+len(q) <= len(text); {
		if !equalRunes(text[s:s+len(q)], q) {
			s++
			continue
		}
		e := s + len(q) // exclusive
		i, j := unitAt(s), unitAt(e-1)
		ok := true
		for k := i + 1; k <= j; k++ {
			if !joinable(units[k-1], units[k]) {
				ok = false
			}
		}
		if !ok || text[s] == ' ' || text[e-1] == ' ' {
			s++
			continue
		}
		first, last := []rune(units[i].Text), []rune(units[j].Text)
		pre := string(first[:s-start[i]])
		post := string(last[e-start[j]:])
		out = append(out, Span{I: i, J: j, Pre: pre, Post: post})
		s = e
	}
	return out
}

// hasUnspaced reports whether text has letters of a script written without
// spaces between words.
func hasUnspaced(text string) bool {
	for _, r := range text {
		if textalign.Unspaced(r) && (unicode.IsLetter(r) || r == 'ー' || r == '々') {
			return true
		}
	}
	return false
}

// matchUnspaced finds a query in the text as written (pieces joined as their
// script writes them), comparing letters and digits only, so punctuation and
// the spaces a query may have between pieces don't matter.
func matchUnspaced(units []Unit, query string, matchCase bool) []Span {
	keep := func(r rune) bool { return isWordRune(r) || r == 'ー' || r == '々' }
	var q []rune
	for _, r := range foldString(query, matchCase) {
		if keep(r) {
			q = append(q, r)
		}
	}
	if len(q) == 0 || len(units) == 0 {
		return nil
	}
	// The kept letters of the clip, each with its unit and its place in that unit's text.
	type at struct{ unit, pos int }
	var text []rune
	var where []at
	for i, u := range units {
		for k, r := range []rune(foldString(u.Text, matchCase)) {
			if keep(r) {
				text = append(text, r)
				where = append(where, at{i, k})
			}
		}
	}
	var out []Span
	for s := 0; s+len(q) <= len(text); {
		if !equalRunes(text[s:s+len(q)], q) {
			s++
			continue
		}
		e := s + len(q) - 1
		i, j := where[s].unit, where[e].unit
		ok := true
		for k := i + 1; k <= j; k++ {
			if !joinable(units[k-1], units[k]) {
				ok = false
			}
		}
		if !ok {
			s++
			continue
		}
		first, last := []rune(units[i].Text), []rune(units[j].Text)
		out = append(out, Span{I: i, J: j, Pre: string(first[:where[s].pos]), Post: string(last[where[e].pos+1:])})
		s = e + 1
	}
	return out
}

func equalRunes(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Text joins units I..J as they are written: one space between words, none
// between pieces of a script written without spaces.
func Text(units []Unit, i, j int) string {
	parts := make([]string, 0, j-i+1)
	for k := i; k <= j; k++ {
		parts = append(parts, units[k].Text)
	}
	return textalign.JoinWords(parts)
}

// Spread writes new words over the time old words took: the same number of
// words keep their times one for one; otherwise the stretch is shared out by
// letters (joining "Fern Brook" into "FernBrook" spans both words, splitting
// "Maplegrove" into "Maple Grove" cuts it where the letters fall). The first
// new word starts exactly where the old ones started and the last ends
// exactly where they ended, so cuts and quoted words around them stay put.
// New words carry no confidence and are marked as written by a person.
func Spread(old []models.Word, tokens []string) []models.Word {
	if len(tokens) == 0 {
		return nil
	}
	out := make([]models.Word, len(tokens))
	if len(old) == 0 {
		for i, t := range tokens {
			out[i] = models.Word{Text: t, Fixed: true}
		}
		return out
	}
	if len(old) == len(tokens) {
		for i, t := range tokens {
			out[i] = models.Word{Text: t, From: old[i].From, To: old[i].To, Fixed: true}
		}
		return out
	}
	// Letters of the old words, each word over its own time.
	lens := make([]float64, len(old))
	starts := make([]float64, len(old))
	total := 0.0
	for i, w := range old {
		starts[i] = total
		lens[i] = float64(max(1, utf8.RuneCountInString(strings.TrimSpace(w.Text))))
		total += lens[i]
	}
	timeAt := func(c float64, atEnd bool) float64 {
		if c <= 0 {
			return old[0].From
		}
		if c >= total {
			return old[len(old)-1].To
		}
		for i := range old {
			lo, hi := starts[i], starts[i]+lens[i]
			if c > lo && c < hi || (!atEnd && c == lo) || (atEnd && c == hi) {
				return old[i].From + (old[i].To-old[i].From)*(c-lo)/lens[i]
			}
		}
		return old[len(old)-1].To
	}
	newLens := make([]float64, len(tokens))
	newTotal := 0.0
	for i, t := range tokens {
		newLens[i] = float64(max(1, utf8.RuneCountInString(t)))
		newTotal += newLens[i]
	}
	pos := 0.0
	for i, t := range tokens {
		a := pos * total / newTotal
		pos += newLens[i]
		b := pos * total / newTotal
		from, to := round3(timeAt(a, false)), round3(timeAt(b, true))
		if i == 0 {
			from = old[0].From
		}
		if i == len(tokens)-1 {
			to = old[len(old)-1].To
		}
		out[i] = models.Word{Text: t, From: from, To: to, Fixed: true}
	}
	// Times never run backwards.
	for i := range out {
		if i > 0 && out[i].From < out[i-1].To {
			out[i].From = out[i-1].To
		}
		if out[i].To < out[i].From {
			out[i].To = out[i].From
		}
	}
	return out
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// Block is one changed stretch of a retyped passage: old words I..J (J < I:
// none) become the new words.
type Block struct {
	I, J  int
	Words []models.Word
}

// Retype compares new text with the old words and returns only the stretches
// that changed, each with new words spread over the old ones' time (words
// that stay the same keep their times and confidence). A word added between
// two old ones joins the old word after it (or before it, at the end), so it
// has time to take; a word taken out is removed.
func Retype(old []models.Word, text string) []Block {
	toks := retypeTokens(old, text)
	n, m := len(old), len(toks)
	if n*m > 400_000 {
		return []Block{{I: 0, J: n - 1, Words: Spread(old, toks)}}
	}
	// Longest common subsequence of the written words.
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if strings.TrimSpace(old[i].Text) == toks[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type pair struct{ oi, oj, ni, nj int } // old [oi, oj), new [ni, nj)
	var diffs []pair
	i, j := 0, 0
	cur := pair{-1, -1, -1, -1}
	open := false
	flush := func() {
		if open {
			diffs = append(diffs, cur)
			open = false
		}
	}
	for i < n || j < m {
		if i < n && j < m && strings.TrimSpace(old[i].Text) == toks[j] {
			flush()
			i++
			j++
			continue
		}
		if !open {
			cur, open = pair{i, i, j, j}, true
		}
		if j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]) {
			j++
			cur.nj = j
		} else {
			i++
			cur.oj = i
		}
	}
	flush()
	// Stretches of old words [oi, oj) and the words that replace them.
	type stretch struct {
		oi, oj int
		words  []string
	}
	var ss []stretch
	for _, d := range diffs {
		st := stretch{d.oi, d.oj, append([]string{}, toks[d.ni:d.nj]...)}
		if st.oi == st.oj { // only added words: they take the next old word's time (or the previous one's at the end)
			switch {
			case st.oj < n:
				st.words = append(st.words, strings.TrimSpace(old[st.oj].Text))
				st.oj++
			case st.oi > 0:
				st.oi--
				st.words = append([]string{strings.TrimSpace(old[st.oi].Text)}, st.words...)
			}
		}
		// Stretches that share a borrowed word or touch become one.
		if k := len(ss); k > 0 && st.oi <= ss[k-1].oj && ss[k-1].oj > ss[k-1].oi {
			prev := &ss[k-1]
			if st.oi < prev.oj { // both borrowed the same old word: keep it once
				prev.words = append(prev.words, st.words[1:]...)
			} else {
				prev.words = append(prev.words, st.words...)
			}
			prev.oj = max(prev.oj, st.oj)
			continue
		}
		ss = append(ss, st)
	}
	out := make([]Block, 0, len(ss))
	for _, st := range ss {
		out = append(out, Block{I: st.oi, J: st.oj - 1, Words: Spread(old[st.oi:st.oj], st.words)})
	}
	return out
}

// retypeTokens splits retyped text into words: at spaces, and in a script
// written without spaces (D244) where the old pieces began, found by lining
// the old line up with the new one letter by letter — so pieces that didn't
// change keep their times and only the changed ones are written down again.
// Words joined across a space ("Fern Brook" → "FernBrook") are never cut back apart.
func retypeTokens(old []models.Word, text string) []string {
	fields := strings.Fields(text)
	if !hasUnspaced(text) || len(old) == 0 {
		return fields
	}
	var oldTexts []string
	for _, w := range old {
		if t := strings.TrimSpace(w.Text); t != "" {
			oldTexts = append(oldTexts, t)
		}
	}
	a, b := []rune(textalign.JoinWords(oldTexts)), []rune(textalign.JoinWords(fields))
	n, m := len(a), len(b)
	if n*m > 2_000_000 {
		return fields
	}
	starts := make([]bool, n+1) // where an old piece begins in a
	pos, prev := 0, ""
	for _, t := range oldTexts {
		if prev != "" && textalign.SpaceBetween(prev, t) {
			pos++
		}
		starts[pos] = true
		pos += len([]rune(t))
		prev = t
	}
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	cut := make([]bool, m+1)
	i, j := 0, 0
	for i < n || j < m {
		if i < n && starts[i] {
			cut[j] = true
		}
		switch {
		case i < n && j < m && a[i] == b[j]:
			i, j = i+1, j+1
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			j++
		default:
			i++
		}
	}
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, string(cur))
			cur = nil
		}
	}
	for k, r := range b {
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		if cut[k] && k > 0 && (textalign.Unspaced(b[k-1]) || textalign.Unspaced(r)) && !isPunctRune(r) {
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

func isPunctRune(r rune) bool { return unicode.IsPunct(r) && !isWordRune(r) }

// WordsOf returns the timed words of units I..J (for lines without word
// times, the units' estimated times).
func WordsOf(units []Unit, i, j int) []models.Word {
	out := make([]models.Word, 0, j-i+1)
	for k := i; k <= j; k++ {
		u := units[k]
		out = append(out, models.Word{Text: u.Text, From: u.From, To: u.To, Score: u.Score, Fixed: u.Fixed})
	}
	return out
}

// JoinWords writes words as a line: one space between them, none between
// pieces of a script written without spaces (D244).
func JoinWords(ws []models.Word) string {
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		if t := strings.TrimSpace(w.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return textalign.JoinWords(parts)
}
