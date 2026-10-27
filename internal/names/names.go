// Package names matches what a person types against the people a workspace
// has named: case and accents are ignored, and candidates are ranked exact,
// prefix, word prefix, substring, then a small typo tolerance (one edit for
// short names, two for long ones). The dashboard's person picker
// (web/src/lib/names.ts) mirrors this ranking; both are tested against the same
// table of cases (testdata/cases.json).
package names

import (
	"sort"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// Kind is how a candidate matched (lower is better).
type Kind int

// Match kinds, best first.
const (
	Exact Kind = iota
	Prefix
	WordPrefix
	Substring
	Typo
	None
)

func (k Kind) String() string {
	switch k {
	case Exact:
		return "exact"
	case Prefix:
		return "prefix"
	case WordPrefix:
		return "word"
	case Substring:
		return "substring"
	case Typo:
		return "typo"
	}
	return "none"
}

// Fold is a name as it is compared: accents removed, lower case, punctuation
// dropped (hyphens and underscores become spaces), spaces collapsed.
func Fold(s string) string {
	var b strings.Builder
	space := false
	for _, r := range norm.NFD.String(s) {
		switch {
		case unicode.Is(unicode.Mn, r):
			continue
		case r == '-' || r == '_' || unicode.IsSpace(r):
			space = true
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteString(strings.ToLower(string(r)))
		}
	}
	return b.String()
}

// Tolerance is how many edits a query of this many letters may be off by:
// none under 3, one up to 6, two from 7.
func Tolerance(q string) int {
	n := len([]rune(q))
	switch {
	case n < 3:
		return 0
	case n < 7:
		return 1
	}
	return 2
}

// Distance is the optimal-string-alignment edit distance (insertions,
// deletions, substitutions and swaps of neighbours count one each).
func Distance(a, b string) int {
	x, y := []rune(a), []rune(b)
	n, m := len(x), len(y)
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
		d[i][0] = i
	}
	for j := 0; j <= m; j++ {
		d[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := 1
			if x[i-1] == y[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && x[i-1] == y[j-2] && x[i-2] == y[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[n][m]
}

// Classify says how a folded query matches a folded name, with the edit
// distance for typo matches.
func Classify(q, name string) (Kind, int) {
	if q == "" {
		return None, 0
	}
	if q == name {
		return Exact, 0
	}
	if strings.HasPrefix(name, q) {
		return Prefix, 0
	}
	if wordPrefix(q, name) {
		return WordPrefix, 0
	}
	if len([]rune(q)) >= 2 && strings.Contains(name, q) {
		return Substring, 0
	}
	if tol := Tolerance(q); tol > 0 {
		best := Distance(q, name)
		for _, w := range strings.Fields(name) {
			best = min(best, Distance(q, w))
		}
		if best <= tol {
			return Typo, best
		}
	}
	return None, 0
}

// wordPrefix: every query word starts a name word, in order ("lind" finds
// "maya lindqvist", "may lind" too).
func wordPrefix(q, name string) bool {
	qs, ws := strings.Fields(q), strings.Fields(name)
	i := 0
	for _, w := range ws {
		if i < len(qs) && strings.HasPrefix(w, qs[i]) {
			i++
		}
	}
	return len(qs) > 0 && i == len(qs)
}

// Candidate is someone who can be matched.
type Candidate struct {
	ID   string
	Name string
	// Weight breaks ties between equally good matches (how often the person
	// appears); higher first.
	Weight float64
}

// Hit is a ranked match.
type Hit struct {
	Candidate
	Kind     Kind
	Distance int
}

// Rank returns the candidates that match q, best first: by kind, then edit
// distance, then weight, then name.
func Rank(q string, cands []Candidate) []Hit {
	fq := Fold(q)
	var out []Hit
	for _, c := range cands {
		k, d := Classify(fq, Fold(c.Name))
		if k == None {
			continue
		}
		out = append(out, Hit{Candidate: c, Kind: k, Distance: d})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Distance != b.Distance {
			return a.Distance < b.Distance
		}
		if a.Weight != b.Weight {
			return a.Weight > b.Weight
		}
		return Fold(a.Name) < Fold(b.Name)
	})
	return out
}

// Result is what a typed name refers to.
type Result struct {
	// Found is the one person meant (an exact match, or the only one whose
	// name or a word of it starts with what was typed).
	Found *Candidate
	// Ambiguous lists the people it could be when several match equally well.
	Ambiguous []Candidate
	// Suggest lists close names when nobody matches ("did you mean").
	Suggest []Candidate
}

// Resolve finds the person a typed name refers to. Exact matches win; a
// unique prefix (of the name or of a word in it) is accepted; otherwise close
// names are suggested.
func Resolve(q string, cands []Candidate) Result {
	hits := Rank(q, cands)
	var res Result
	pick := func(k ...Kind) []Candidate {
		var out []Candidate
		for _, h := range hits {
			for _, want := range k {
				if h.Kind == want {
					out = append(out, h.Candidate)
				}
			}
		}
		return out
	}
	if ex := pick(Exact); len(ex) == 1 {
		res.Found = &ex[0]
		return res
	} else if len(ex) > 1 {
		res.Ambiguous = ex
		return res
	}
	if pre := pick(Prefix, WordPrefix); len(pre) == 1 {
		res.Found = &pre[0]
		return res
	} else if len(pre) > 1 {
		res.Ambiguous = pre
		return res
	}
	for _, h := range hits {
		if len(res.Suggest) == 3 {
			break
		}
		res.Suggest = append(res.Suggest, h.Candidate)
	}
	return res
}

// Close lists people whose names are close to a new name someone typed (a
// prefix, word prefix or typo away, but not the same name): naming someone
// "Mya" when "Maya" exists is probably a slip, not a new person.
func Close(q string, cands []Candidate) []Candidate {
	var out []Candidate
	for _, h := range Rank(q, cands) {
		if h.Kind == Exact {
			return nil
		}
		if h.Kind == Prefix || h.Kind == WordPrefix || h.Kind == Typo {
			out = append(out, h.Candidate)
		}
		if len(out) == 3 {
			break
		}
	}
	return out
}

// Quote lists names for a message: “Maya”, “Tessa” or “Jules”.
func Quote(cs []Candidate) string {
	var parts []string
	for _, c := range cs {
		parts = append(parts, "“"+c.Name+"”")
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " or " + parts[1]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}
